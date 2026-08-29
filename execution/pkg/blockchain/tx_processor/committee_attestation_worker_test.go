package tx_processor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
	stake_state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommitteeAttestationWorker_SingleValidatorFullLifecycle is the Milestone C capstone test:
// a real CommitteeAttestationWorker, driven by a real epoch-transition signal, against a real
// stake-registered validator on one ChainState and a real Root Anchor stand-in (the actual
// GatewayHandler behind a real HTTP JSON-RPC server, decoding a REAL signed go-ethereum
// transaction) on another. A single validator with 100% stake is used so one worker instance can
// drive the whole submit -> observe-quorum -> aggregate -> finalize pipeline without needing to
// simulate multiple physical nodes.
func TestCommitteeAttestationWorker_SingleValidatorFullLifecycle(t *testing.T) {
	rootAnchorCS, _, _, _ := newPersistentTestChainState(t)

	const localChainID = 777
	const oldEpoch = 10

	kp := bls.GenerateKeyPair()
	popSig := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	oldEntry := cross_chain.ValidatorEntry{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 1000, PopSignature: popSig.Bytes()}

	raEngine, err := loadGatewayEngine(rootAnchorCS)
	if err != nil {
		t.Fatalf("loadGatewayEngine (seed): %v", err)
	}
	raEngine.ChainRegistry[localChainID] = cross_chain.ChainRegistry{
		ChainID:         localChainID,
		Committee:       []cross_chain.ValidatorEntry{oldEntry},
		Epoch:           oldEpoch,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(rootAnchorCS, raEngine); err != nil {
		t.Fatalf("saveGatewayEngine (seed): %v", err)
	}

	// Root Anchor's own chain ID (for EIP-155 tx signing) — arbitrary, distinct from localChainID.
	rootAnchorChainID := big.NewInt(9099)
	srv := newRootAnchorTestServer(t, rootAnchorCS, rootAnchorChainID)
	defer srv.Close()

	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("rootanchor.NewClient: %v", err)
	}

	// --- The "private chain" whose epoch just transitioned. Register 1 real validator: real
	// stake-state registration + a real min-pk PublicKeyBls on its account (exactly how genesis
	// ties them together, per the Milestone C plan doc's finding). ---
	privateChainCS, _, _, _ := newPersistentTestChainState(t)
	// newPersistentTestChainState only wires an account trie + smart-contract DB (Milestone A/B's
	// gateway-only tests never needed a stake DB) — this test needs a real one for GetAllValidators.
	stakeStorage := storage.NewMemoryDb()
	stakeTrie, err := trie.NewStateTrie(common.Hash{}, stakeStorage, true)
	if err != nil {
		t.Fatalf("create stake trie: %v", err)
	}
	privateChainCS.SetStakeStateDB(stake_state_db.NewStakeStateDB(stakeTrie, stakeStorage))

	validatorAddr := common.HexToAddress("0x5555555555555555555555555555555555555555")
	if err := privateChainCS.GetStakeStateDB().CreateRegisterWithKeys(
		validatorAddr, "node-0", "", "", "", 5,
		big.NewInt(0), "127.0.0.1:6200", "127.0.0.1:4012", "/ip4/127.0.0.1/tcp/9100",
		"", []byte{0x01}, []byte{0x02}, "node-0", []byte{0x03},
	); err != nil {
		t.Fatalf("CreateRegisterWithKeys: %v", err)
	}
	stakeAmount, ok := new(big.Int).SetString("1000000000000000000000", 10) // 1000e18
	if !ok {
		t.Fatal("failed to parse stake amount")
	}
	if err := privateChainCS.GetStakeStateDB().Delegate(validatorAddr, validatorAddr, stakeAmount); err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if err := privateChainCS.GetAccountStateDB().SetPublicKeyBls(validatorAddr, kp.PublicKey().Bytes()); err != nil {
		t.Fatalf("SetPublicKeyBls: %v", err)
	}
	// CreateRegisterWithKeys/Delegate only mark validator state dirty in memory — GetAllValidators
	// reads directly from the trie (stake_state_db.go), so the dirty state must be flushed first.
	if _, err := privateChainCS.GetStakeStateDB().IntermediateRoot(); err != nil {
		t.Fatalf("IntermediateRoot (stake): %v", err)
	}

	// Submitter key: any funded secp256k1 key works against this test server (no gas accounting).
	submitterKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate submitter key: %v", err)
	}
	submitterHex := hex.EncodeToString(crypto.FromECDSA(submitterKey))

	worker := NewCommitteeAttestationWorker(
		privateChainCS, client, localChainID, validatorAddr,
		hex.EncodeToString(kp.BytesPrivateKey()),
		submitterHex,
	)
	worker.pollInterval = 20 * time.Millisecond
	worker.maxPollAttempts = 50

	newEpoch := uint64(oldEpoch + 1)
	boundaryBlock := uint64(0) // newPersistentTestChainState's header is block 0

	done := make(chan struct{})
	go func() {
		worker.handleEpochTransition(context.Background(), epochSignal{newEpoch: newEpoch, boundaryBlock: boundaryBlock})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handleEpochTransition did not complete in time")
	}

	// --- Verify: Root Anchor's ChainRegistry for localChainID must now be at the new epoch,
	// with the new committee containing our one validator's real min-pk key, applied via the
	// REAL cross_chain.ApplyCommitteeUpdate verification (BLS aggregate + stake quorum), not a
	// caller-supplied bool. ---
	raEngineAfter, err := loadGatewayEngine(rootAnchorCS)
	if err != nil {
		t.Fatalf("loadGatewayEngine (after): %v", err)
	}
	updated, ok := raEngineAfter.ChainRegistry[localChainID]
	if !ok {
		t.Fatal("ChainRegistry entry disappeared")
	}
	if updated.Epoch != newEpoch {
		t.Fatalf("registry epoch = %d, want %d", updated.Epoch, newEpoch)
	}
	if len(updated.Committee) != 1 {
		t.Fatalf("registry committee size = %d, want 1", len(updated.Committee))
	}
	if string(updated.Committee[0].PubkeyBLS) != string(kp.PublicKey().Bytes()) {
		t.Fatal("new committee does not contain the expected validator's min-pk key")
	}
	if updated.Committee[0].Stake != 1000 {
		t.Fatalf("new committee stake = %d, want 1000 (1000e18 wei / 1e18)", updated.Committee[0].Stake)
	}
}

// newRootAnchorTestServer serves eth_call / eth_sendRawTransaction / eth_getTransactionCount /
// eth_chainId backed by the REAL GatewayHandler against rootAnchorCS — a genuine end-to-end
// stand-in for Root Anchor's JSON-RPC surface, not a hand-rolled mock of GatewayHandler's
// behavior. eth_sendRawTransaction decodes a REAL signed go-ethereum transaction (exactly what
// CommitteeAttestationWorker.signAndSubmit produces) and recovers the sender via ecrecover,
// mirroring what a real node's tx-submission path would do.
func newRootAnchorTestServer(t *testing.T, rootAnchorCS *blockchain.ChainState, chainID *big.Int) *httptest.Server {
	t.Helper()
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}
	var nonceCounter uint64

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "eth_call":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			callObj, _ := params[0].(map[string]interface{})
			dataHex, _ := callObj["data"].(string)
			calldata, decErr := hexutil.Decode(dataHex)
			if decErr != nil {
				writeRPCError(w, req.ID, decErr.Error())
				return
			}
			sender := common.HexToAddress("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")
			viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
			result, callErr := h.HandleOffChainQuery(rootAnchorCS, viewTx)
			if callErr != nil {
				writeRPCError(w, req.ID, callErr.Error())
				return
			}
			writeJSONRPCResult(w, req.ID, hexutil.Encode(result))

		case "eth_getTransactionCount":
			writeJSONRPCResult(w, req.ID, hexutil.EncodeUint64(nonceCounter))

		case "eth_chainId":
			writeJSONRPCResult(w, req.ID, hexutil.EncodeBig(chainID))

		case "eth_sendRawTransaction":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			rawHex, _ := params[0].(string)
			rawBytes, decErr := hexutil.Decode(rawHex)
			if decErr != nil {
				writeRPCError(w, req.ID, decErr.Error())
				return
			}
			var ethTx ethtypes.Transaction
			if unmarshalErr := ethTx.UnmarshalBinary(rawBytes); unmarshalErr != nil {
				writeRPCError(w, req.ID, unmarshalErr.Error())
				return
			}
			signer := ethtypes.NewEIP155Signer(chainID)
			from, senderErr := ethtypes.Sender(signer, &ethTx)
			if senderErr != nil {
				writeRPCError(w, req.ID, senderErr.Error())
				return
			}
			gwTx := newTx(from, mt_common.GATEWAY_CONTRACT_ADDRESS, ethTx.Nonce(), big.NewInt(0), marshalCallData(t, ethTx.Data()))
			rcp, _, failed := h.HandleTransaction(context.Background(), rootAnchorCS, gwTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
			nonceCounter++
			if failed {
				reason := ""
				if rcp != nil {
					reason = string(rcp.Return())
				}
				writeRPCError(w, req.ID, reason)
				return
			}
			writeJSONRPCResult(w, req.ID, gwTx.Hash().Hex())

		default:
			writeRPCError(w, req.ID, "unsupported method in this test server: "+req.Method)
		}
	}))
}

func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func TestCommitteeAttestationWorker_NonMemberSkipsSilently(t *testing.T) {
	rootAnchorCS, _, _, _ := newPersistentTestChainState(t)
	const localChainID = 777
	const oldEpoch = 10

	// Root Anchor committee has kpMember
	kpMember := bls.GenerateKeyPair()
	popSig := cross_chain.PopSign(kpMember.PrivateKey(), kpMember.PublicKey())
	oldEntry := cross_chain.ValidatorEntry{PubkeyBLS: kpMember.PublicKey().Bytes(), Stake: 1000, PopSignature: popSig.Bytes()}

	raEngine, err := loadGatewayEngine(rootAnchorCS)
	require.NoError(t, err)
	raEngine.ChainRegistry[localChainID] = cross_chain.ChainRegistry{
		ChainID:         localChainID,
		Committee:       []cross_chain.ValidatorEntry{oldEntry},
		Epoch:           oldEpoch,
		QuorumThreshold: 6667,
	}
	require.NoError(t, saveGatewayEngine(rootAnchorCS, raEngine))

	srv := newRootAnchorTestServer(t, rootAnchorCS, big.NewInt(9099))
	defer srv.Close()

	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	require.NoError(t, err)

	// Worker uses kpNonMember (not in committee)
	kpNonMember := bls.GenerateKeyPair()
	privateChainCS, _, _, _ := newPersistentTestChainState(t)
	valAddr := common.HexToAddress("0x6666666666666666666666666666666666666666")
	require.NoError(t, privateChainCS.GetAccountStateDB().SetPublicKeyBls(valAddr, kpNonMember.PublicKey().Bytes()))

	submitterKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	worker := NewCommitteeAttestationWorker(
		privateChainCS, client, localChainID, valAddr,
		hex.EncodeToString(kpNonMember.BytesPrivateKey()),
		hex.EncodeToString(crypto.FromECDSA(submitterKey)),
	)

	// handleEpochTransition should skip silently without error or state mutation
	worker.handleEpochTransition(context.Background(), epochSignal{newEpoch: oldEpoch + 1, boundaryBlock: 0})

	raEngineAfter, err := loadGatewayEngine(rootAnchorCS)
	require.NoError(t, err)
	assert.Equal(t, uint64(oldEpoch), raEngineAfter.ChainRegistry[localChainID].Epoch, "Epoch must not change if non-member triggers worker")
}

func TestCommitteeAttestationWorker_HandlesRootAnchorOfflineGracefully(t *testing.T) {
	privateChainCS, _, _, _ := newPersistentTestChainState(t)
	valAddr := common.HexToAddress("0x7777777777777777777777777777777777777777")
	kp := bls.GenerateKeyPair()
	require.NoError(t, privateChainCS.GetAccountStateDB().SetPublicKeyBls(valAddr, kp.PublicKey().Bytes()))

	// Client pointing to dead port / offline server
	client, err := rootanchor.NewClient([]string{"http://127.0.0.1:59999"}, nil)
	require.NoError(t, err)

	submitterKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	worker := NewCommitteeAttestationWorker(
		privateChainCS, client, 777, valAddr,
		hex.EncodeToString(kp.BytesPrivateKey()),
		hex.EncodeToString(crypto.FromECDSA(submitterKey)),
	)

	// Must handle offline RPC gracefully without panic
	require.NotPanics(t, func() {
		worker.handleEpochTransition(context.Background(), epochSignal{newEpoch: 11, boundaryBlock: 0})
	})
}

func TestCommitteeAttestationWorker_SignalChannelBufferDrop(t *testing.T) {
	worker := NewCommitteeAttestationWorker(nil, nil, 777, common.Address{}, "", "")
	// Buffer size is 8
	for i := uint64(1); i <= 8; i++ {
		worker.OnEpochAdvanced(i, i*10)
	}
	assert.Equal(t, 8, len(worker.signalChan))

	// 9th call should drop without blocking
	worker.OnEpochAdvanced(9, 90)
	assert.Equal(t, 8, len(worker.signalChan))
}
