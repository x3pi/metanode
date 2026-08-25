package relayer_daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRelayerDaemon_Lifecycle(t *testing.T) {
	const sourceChainID = 101
	const destChainID = 202
	const epoch = 1

	kpVal := bls.GenerateKeyPair()
	validatorEntry := cross_chain.ValidatorEntry{
		PubkeyBLS: kpVal.PublicKey().Bytes(),
		Stake:     1000,
	}

	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	relayerKeyHex := hex.EncodeToString(crypto.FromECDSA(relayerKey))
	relayerAddr := crypto.PubkeyToAddress(relayerKey.PublicKey)

	sender := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	target := common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999"),
		SourceChainID: sourceChainID,
		DestChainID:   destChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        target,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(0),
		Payload:       []byte{0x01, 0x02, 0x03},
		Tip:           big.NewInt(0),
		Ordered:       false,
	}
	// Real 2-leaf commit tree (message leaf + AggregateValueLeaf, Section 2.3.1) — attestCommit()
	// verifies aggregateProof against commitRoot itself now, not a separately-declared StateRoot.
	commitRoot, commitLayers, _, aggIndex, errTree := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, errTree)
	messageProof := cross_chain.GetMerkleProof(commitLayers, 0)
	aggregateProof := cross_chain.GetMerkleProof(commitLayers, aggIndex["0"])

	// Step 1: Mock Root Anchor Server
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kpVal.PrivateKey(), commitMsg)

	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	require.NoError(t, err)

	rootAnchorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "eth_chainId":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  hexutil.EncodeBig(big.NewInt(9099)),
			})

		case "eth_call":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			callObj, _ := params[0].(map[string]interface{})
			dataHex, _ := callObj["data"].(string)
			calldata, _ := hexutil.Decode(dataHex)

			if len(calldata) >= 4 {
				if calldata[0] == 0x9f || len(calldata) == 36 { // getChainRegistry(uint256)
					packed, err := parsedABI.Methods["getChainRegistry"].Outputs.Pack(
						true,
						[][]byte{validatorEntry.PubkeyBLS},
						[]uint64{validatorEntry.Stake},
						[][]byte{validatorEntry.PopSignature},
						uint64(epoch),
						uint64(6667),
						common.Address{},
						common.Hash{}, // StateRoot
						common.Hash{}, // AccountTreeRoot
						"",
						uint64(0),
					)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": req.ID, "result": hexutil.Encode(packed),
					})
					return
				} else {
					// getCommitAttestationShares
					packed, err := parsedABI.Methods["getCommitAttestationShares"].Outputs.Pack(
						[][]byte{validatorEntry.PubkeyBLS}, [][]byte{sig.Bytes()},
					)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": req.ID, "result": hexutil.Encode(packed),
					})
					return
				}
			}

		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": "0x0",
			})
		}
	}))
	defer rootAnchorSrv.Close()

	// Step 2: Mock Destination Chain Server
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10_000), map[uint64]*big.Int{sourceChainID: big.NewInt(10_000)})
	require.NoError(t, err)

	destEngine := cross_chain.NewGatewayEngine(destChainID, map[uint64]cross_chain.ChainRegistry{
		sourceChainID: {
			ChainID:   sourceChainID,
			Committee: []cross_chain.ValidatorEntry{validatorEntry},
			Epoch:     epoch,
		},
	}, ledger)

	var nonceCounter uint64
	destSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "eth_chainId":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeBig(big.NewInt(destChainID)),
			})

		case "eth_getTransactionCount":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeUint64(nonceCounter),
			})

		case "eth_sendRawTransaction":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			rawHex, _ := params[0].(string)
			rawBytes, _ := hexutil.Decode(rawHex)

			var ethTx ethtypes.Transaction
			_ = ethTx.UnmarshalBinary(rawBytes)
			signer := ethtypes.NewEIP155Signer(big.NewInt(destChainID))
			from, _ := ethtypes.Sender(signer, &ethTx)
			assert.Equal(t, relayerAddr, from)

			// Execute directly on destEngine
			cert := cross_chain.QuorumCert{
				Epoch:              epoch,
				AggregateSignature: sig.Bytes(),
				SignerBitmap:       []byte{0x01},
			}
			st, err := destEngine.VerifyAndExecute(msg, aggregateProof, cert, messageProof, commitRoot, from)
			assert.NoError(t, err)
			assert.Equal(t, cross_chain.MessageStatusSuccess, st)

			nonceCounter++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": ethTx.Hash().Hex(),
			})

		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": "0x0",
			})
		}
	}))
	defer destSrv.Close()

	// Step 3: Instantiate and run RelayerDaemon
	cfg := DaemonConfig{
		RelayerKeyHex:  relayerKeyHex,
		RootAnchorURLs: []string{rootAnchorSrv.URL},
		ChainRPCURLs: map[uint64]string{
			destChainID: destSrv.URL,
		},
		PollInterval:      10 * time.Millisecond,
		MaxPollIterations: 10,
	}

	daemon, err := NewRelayerDaemon(cfg)
	require.NoError(t, err)
	defer daemon.Stop()

	assert.Equal(t, relayerAddr, daemon.Address())

	// Step 4: Relay single message
	txHash, err := daemon.RelayMessage(context.Background(), msg, commitRoot, epoch, aggregateProof, messageProof)
	require.NoError(t, err)
	assert.NotEmpty(t, txHash.Hex())

	// Verify destination engine state
	assert.Equal(t, cross_chain.MessageStatusSuccess, destEngine.GetMessageStatus(msg.MessageID))

	// Step 5: Duplicate relay rejected
	_, err = daemon.RelayMessage(context.Background(), msg, commitRoot, epoch, cross_chain.MerkleProof{}, cross_chain.MerkleProof{})
	assert.Error(t, err, "Duplicate relay of already processed message must be rejected by daemon")
}
