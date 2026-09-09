package relayer_daemon

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// TestRelayerDaemon_UnrelayedBatchSurvivesProcessRestart is the regression test for the review
// finding on PR #102 (2026-09-05): that PR's unrelayedBatches retry queue lived only in RAM, so a
// relayer PROCESS restart (not just a destination-chain restart) while a batch was stuck unrelayed
// would lose it forever -- the on-chain PendingOutboundMessages queue was already drained by the
// batchOutboundCommit() that produced the stuck batch, so a fresh process's normal BatchAndRelay
// flow (which only ever looks at getPendingOutboundCount) could never rediscover it.
//
// This drives the actual fix end-to-end: daemon "A" persists a stuck batch to disk (simulating a
// crash right after batchOutboundCommit succeeded but before RelayBatch finished), then a
// completely separate daemon "B" -- pointed at the same persistence file, with NO source-chain RPC
// configured at all -- loads it on construction and successfully relays it purely via the retry
// path, proving the retry never needed to go back through getPendingOutboundCount/
// batchOutboundCommit.
func TestRelayerDaemon_UnrelayedBatchSurvivesProcessRestart(t *testing.T) {
	const sourceChainID = 701
	const destChainID = 702
	const epoch = uint64(0)

	kpVal := bls.GenerateKeyPair()
	validatorEntry := cross_chain.ValidatorEntry{PubkeyBLS: kpVal.PublicKey().Bytes(), Stake: 1000}

	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	relayerKeyHex := common.Bytes2Hex(crypto.FromECDSA(relayerKey))

	sender := common.HexToAddress("0xAAAA5555AAAA5555AAAA5555AAAA5555AAAA5555")
	target := common.HexToAddress("0xBBBB6666BBBB6666BBBB6666BBBB6666BBBB6666")

	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	require.NoError(t, err)

	// Build a real, already-batched commit exactly as BatchAndRelay would have produced right
	// before the daemon "crashed".
	sourceEngine := cross_chain.NewGatewayEngine(sourceChainID, map[uint64]cross_chain.ChainRegistry{}, nil)
	_, err = sourceEngine.Outbound(sender, cross_chain.OutboundParams{
		DestChainID: destChainID, Target: target, Payload: []byte{0x01},
		AssetID: big.NewInt(0), Value: big.NewInt(0), Tip: big.NewInt(0), GasFee: big.NewInt(0), HopCount: 1,
	}, common.HexToHash("0xD001"))
	require.NoError(t, err)
	commitRoot, messages, err := sourceEngine.BatchOutboundCommit(destChainID, epoch)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10_000), map[uint64]*big.Int{sourceChainID: big.NewInt(10_000)})
	require.NoError(t, err)
	destEngine := cross_chain.NewGatewayEngine(destChainID, map[uint64]cross_chain.ChainRegistry{
		sourceChainID: {ChainID: sourceChainID, Committee: []cross_chain.ValidatorEntry{validatorEntry}, Epoch: epoch, QuorumThreshold: 6667},
	}, ledger)

	rootAnchorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result interface{}) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "eth_chainId":
			reply(hexutil.EncodeBig(big.NewInt(9299)))
		case "eth_call":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			callObj, _ := params[0].(map[string]interface{})
			calldata, _ := hexutil.Decode(callObj["data"].(string))
			method, mErr := parsedABI.MethodById(calldata[:4])
			require.NoError(t, mErr)
			switch method.Name {
			case "getChainRegistry":
				packed, _ := method.Outputs.Pack(
					true, [][]byte{validatorEntry.PubkeyBLS}, []uint64{validatorEntry.Stake},
					[][]byte{validatorEntry.PopSignature}, uint64(epoch), uint64(6667),
					common.Address{}, common.Hash{}, common.Hash{}, "", uint64(0),
					common.Address{}, common.Hash{},
				)
				reply(hexutil.Encode(packed))
			case "getCommitAttestationShares":
				commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
				sig := bls.Sign(kpVal.PrivateKey(), commitMsg)
				packed, _ := method.Outputs.Pack([][]byte{validatorEntry.PubkeyBLS}, [][]byte{sig.Bytes()})
				reply(hexutil.Encode(packed))
			default:
				t.Fatalf("unexpected eth_call to root anchor: %s", method.Name)
			}
		default:
			reply("0x0")
		}
	}))
	defer rootAnchorSrv.Close()

	type storedReceipt struct{ status uint64 }
	var receiptsMu sync.Mutex
	receipts := make(map[common.Hash]storedReceipt)
	var destNonce uint64

	destSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result interface{}) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "eth_chainId":
			reply(hexutil.EncodeBig(big.NewInt(destChainID)))
		case "eth_gasPrice":
			reply(hexutil.EncodeBig(big.NewInt(1_000_000_000)))
		case "eth_getTransactionCount":
			reply(hexutil.EncodeUint64(destNonce))
		case "eth_call":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			callObj, _ := params[0].(map[string]interface{})
			calldata, _ := hexutil.Decode(callObj["data"].(string))
			method, mErr := parsedABI.MethodById(calldata[:4])
			require.NoError(t, mErr)
			require.Equal(t, "getMessageStatus", method.Name)
			args, uErr := method.Inputs.Unpack(calldata[4:])
			require.NoError(t, uErr)
			status := destEngine.GetMessageStatus(common.Hash(args[0].([32]byte)))
			packed, _ := method.Outputs.Pack(uint8(status))
			reply(hexutil.Encode(packed))
		case "eth_sendRawTransaction":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			rawBytes, _ := hexutil.Decode(params[0].(string))
			var ethTx ethtypes.Transaction
			require.NoError(t, ethTx.UnmarshalBinary(rawBytes))
			calldata := ethTx.Data()
			method, mErr := parsedABI.MethodById(calldata[:4])
			require.NoError(t, mErr)
			args, uErr := method.Inputs.Unpack(calldata[4:])
			require.NoError(t, uErr)

			signer := ethtypes.NewEIP155Signer(big.NewInt(destChainID))
			from, sErr := ethtypes.Sender(signer, &ethTx)
			require.NoError(t, sErr)

			var status uint64 = 1
			switch method.Name {
			case "attestCommit":
				proof := cross_chain.MerkleProof{
					LeafIndex: args[4].(*big.Int).Uint64(),
					Siblings:  bytes32SliceToHashes(args[5].([][32]byte)),
				}
				cert := cross_chain.QuorumCert{
					Epoch:              args[6].(uint64),
					AggregateSignature: args[7].([]byte),
					SignerBitmap:       args[8].([]byte),
				}
				_, attestErr := destEngine.AttestCommit(args[0].(*big.Int).Uint64(), common.Hash(args[1].([32]byte)), args[2].(*big.Int), args[3].(*big.Int), proof, cert)
				if attestErr != nil {
					status = 0
				}
			case "claimMessage":
				msg := cross_chain.CrossChainMessage{
					MessageID:     common.Hash(args[0].([32]byte)),
					SourceChainID: args[1].(*big.Int).Uint64(),
					DestChainID:   args[2].(*big.Int).Uint64(),
					Sequence:      args[3].(*big.Int).Uint64(),
					HopCount:      args[4].(uint8),
					Sender:        args[5].(common.Address),
					Target:        args[6].(common.Address),
					AssetID:       args[7].(*big.Int),
					Value:         args[8].(*big.Int),
					Payload:       args[9].([]byte),
					Tip:           args[10].(*big.Int),
					GasFee:        args[11].(*big.Int),
					Ordered:       args[12].(bool),
				}
				proof := cross_chain.MerkleProof{
					LeafIndex: args[13].(*big.Int).Uint64(),
					Siblings:  bytes32SliceToHashes(args[14].([][32]byte)),
				}
				cr := common.Hash(args[15].([32]byte))
				_, claimErr := destEngine.ClaimMessage(msg, proof, cr, from)
				if claimErr != nil {
					status = 0
				}
			default:
				t.Fatalf("unexpected write to destination chain: %s", method.Name)
			}

			receiptsMu.Lock()
			receipts[ethTx.Hash()] = storedReceipt{status: status}
			receiptsMu.Unlock()
			destNonce++
			reply(ethTx.Hash().Hex())
		case "eth_getTransactionReceipt":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			txHash := common.HexToHash(params[0].(string))
			receiptsMu.Lock()
			rcp, exists := receipts[txHash]
			receiptsMu.Unlock()
			if !exists {
				reply(nil)
				return
			}
			reply(map[string]interface{}{"status": hexutil.EncodeUint64(rcp.status), "return": hexutil.Encode(nil)})
		default:
			reply("0x0")
		}
	}))
	defer destSrv.Close()

	persistPath := filepath.Join(t.TempDir(), "unrelayed-batches.json")

	// --- "Daemon A": the process that batched the commit and then crashed before finishing the
	// relay. Deliberately never call RelayBatch through it -- we simulate the crash by writing the
	// pending state to disk directly, exactly like BatchAndRelay does right after a successful
	// batchOutboundCommit but before RelayBatch returns.
	daemonA, err := NewRelayerDaemon(DaemonConfig{
		RelayerKeyHex:               relayerKeyHex,
		RootAnchorURLs:              []string{rootAnchorSrv.URL},
		ChainRPCURLs:                map[uint64]string{destChainID: destSrv.URL},
		PollInterval:                5 * time.Millisecond,
		MaxPollIterations:           40,
		UnrelayedBatchesPersistPath: persistPath,
	})
	require.NoError(t, err)
	pairKey := "701:702"
	daemonA.mu.Lock()
	daemonA.unrelayedBatches[pairKey] = &unrelayedBatch{CommitRoot: commitRoot, Epoch: epoch, Messages: messages}
	snapshot := daemonA.snapshotUnrelayedBatchesLocked()
	daemonA.mu.Unlock()
	daemonA.persistUnrelayedBatches(snapshot)
	daemonA.Stop()

	require.FileExists(t, persistPath, "the stuck batch must actually be on disk before we simulate a restart")

	// --- "Daemon B": a completely fresh process, pointed at the same persistence file. Note: NO
	// RPC client for sourceChainID is configured at all -- if the retry path ever fell back to the
	// normal getPendingOutboundCount/batchOutboundCommit flow, this test would fail with "no RPC
	// client configured for source chain 701" instead of succeeding, proving the retry never
	// touches the source chain.
	daemonB, err := NewRelayerDaemon(DaemonConfig{
		RelayerKeyHex:               relayerKeyHex,
		RootAnchorURLs:              []string{rootAnchorSrv.URL},
		ChainRPCURLs:                map[uint64]string{destChainID: destSrv.URL},
		PollInterval:                5 * time.Millisecond,
		MaxPollIterations:           40,
		UnrelayedBatchesPersistPath: persistPath,
	})
	require.NoError(t, err)
	defer daemonB.Stop()

	daemonB.mu.RLock()
	_, resumed := daemonB.unrelayedBatches[pairKey]
	daemonB.mu.RUnlock()
	require.True(t, resumed, "daemon B must have loaded the stuck batch from disk on construction")

	n, err := daemonB.BatchAndRelay(context.Background(), sourceChainID, destChainID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, cross_chain.MessageStatusSuccess, destEngine.GetMessageStatus(common.HexToHash("0xD001")))

	daemonB.mu.RLock()
	_, stillPending := daemonB.unrelayedBatches[pairKey]
	daemonB.mu.RUnlock()
	assert.False(t, stillPending, "the retry queue must be cleared once the batch is fully relayed")

	// The persistence file itself must reflect the clear too (not just in-memory state) -- a THIRD
	// restart must not try to re-relay an already-completed batch.
	data, err := os.ReadFile(persistPath)
	require.NoError(t, err)
	var onDisk map[string]*unrelayedBatch
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Empty(t, onDisk, "the on-disk file must be updated to empty once the batch is relayed, not left stale")
}
