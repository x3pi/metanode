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
		GasFee:        big.NewInt(0),
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

func TestRelayerDaemon_MissingDestinationClient_ReturnsError(t *testing.T) {
	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	relayerKeyHex := hex.EncodeToString(crypto.FromECDSA(relayerKey))

	rootAnchorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "result": "0x0",
		})
	}))
	defer rootAnchorSrv.Close()

	// Empty ChainRPCURLs map
	cfg := DaemonConfig{
		RelayerKeyHex:     relayerKeyHex,
		RootAnchorURLs:    []string{rootAnchorSrv.URL},
		ChainRPCURLs:      map[uint64]string{},
		PollInterval:      10 * time.Millisecond,
		MaxPollIterations: 5,
	}

	daemon, err := NewRelayerDaemon(cfg)
	require.NoError(t, err)
	defer daemon.Stop()

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x1234"),
		SourceChainID: 101,
		DestChainID:   999, // No client for 999
	}

	_, err = daemon.RelayMessage(context.Background(), msg, common.Hash{}, 1, cross_chain.MerkleProof{}, cross_chain.MerkleProof{})
	assert.Error(t, err)
}

func TestRelayerDaemon_QuorumCertPollingTimeout(t *testing.T) {
	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	relayerKeyHex := hex.EncodeToString(crypto.FromECDSA(relayerKey))

	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	require.NoError(t, err)

	kpVal := bls.GenerateKeyPair()
	validatorEntry := cross_chain.ValidatorEntry{
		PubkeyBLS: kpVal.PublicKey().Bytes(),
		Stake:     1000,
	}

	// Server returns registry but ZERO attestation shares
	rootAnchorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "eth_call":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			callObj, _ := params[0].(map[string]interface{})
			dataHex, _ := callObj["data"].(string)
			calldata, _ := hexutil.Decode(dataHex)

			if len(calldata) >= 4 {
				if calldata[0] == 0x9f || len(calldata) == 36 { // getChainRegistry
					packed, _ := parsedABI.Methods["getChainRegistry"].Outputs.Pack(
						true,
						uint64(1),
						[]struct {
							PubkeyBLS    []byte   `json:"pubkeyBLS"`
							Stake        *big.Int `json:"stake"`
							PopSignature []byte   `json:"popSignature"`
						}{{PubkeyBLS: validatorEntry.PubkeyBLS, Stake: big.NewInt(1000), PopSignature: []byte{}}},
						uint64(6667),
						common.Address{},
						common.Hash{},
						common.Hash{},
						"",
						uint64(1000),
					)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": req.ID, "result": hexutil.Encode(packed),
					})
					return
				}
				if calldata[0] == 0x82 || len(calldata) == 100 { // getCommitAttestationShares -> return 0 shares
					packed, _ := parsedABI.Methods["getCommitAttestationShares"].Outputs.Pack([][]byte{}, [][]byte{})
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0", "id": req.ID, "result": hexutil.Encode(packed),
					})
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID, "result": "0x0",
		})
	}))
	defer rootAnchorSrv.Close()

	cfg := DaemonConfig{
		RelayerKeyHex:     relayerKeyHex,
		RootAnchorURLs:    []string{rootAnchorSrv.URL},
		ChainRPCURLs:      map[uint64]string{202: rootAnchorSrv.URL},
		PollInterval:      5 * time.Millisecond,
		MaxPollIterations: 3, // fast timeout
	}

	daemon, err := NewRelayerDaemon(cfg)
	require.NoError(t, err)
	defer daemon.Stop()

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x5555"),
		SourceChainID: 101,
		DestChainID:   202,
	}

	_, err = daemon.RelayMessage(context.Background(), msg, common.HexToHash("0x8888"), 1, cross_chain.MerkleProof{}, cross_chain.MerkleProof{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "QuorumCert")
}

// newRootAnchorAttestationMock builds a real Root Anchor mock server that answers
// getChainRegistry/getCommitAttestationShares for a single committee member (kpVal) attesting
// commitRoot at the given epoch -- the minimum real BLS apparatus pollAndAggregateCommitCert
// needs to produce a valid QuorumCert. Shared by the nonce-management tests below so each can
// focus on destination-chain nonce behavior instead of re-deriving BLS/commit-tree plumbing.
func newRootAnchorAttestationMock(t *testing.T, sourceChainID, epoch uint64, kpVal *bls.KeyPair, validatorEntry cross_chain.ValidatorEntry, commitRoot common.Hash) *httptest.Server {
	t.Helper()
	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	require.NoError(t, err)

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kpVal.PrivateKey(), commitMsg)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeBig(big.NewInt(9099)),
			})
		case "eth_call":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			callObj, _ := params[0].(map[string]interface{})
			dataHex, _ := callObj["data"].(string)
			calldata, _ := hexutil.Decode(dataHex)

			var packed []byte
			var packErr error
			if len(calldata) >= 4 && (calldata[0] == 0x9f || len(calldata) == 36) {
				packed, packErr = parsedABI.Methods["getChainRegistry"].Outputs.Pack(
					true,
					[][]byte{validatorEntry.PubkeyBLS},
					[]uint64{validatorEntry.Stake},
					[][]byte{validatorEntry.PopSignature},
					epoch,
					uint64(6667),
					common.Address{},
					common.Hash{},
					common.Hash{},
					"",
					uint64(0),
				)
			} else {
				packed, packErr = parsedABI.Methods["getCommitAttestationShares"].Outputs.Pack(
					[][]byte{validatorEntry.PubkeyBLS}, [][]byte{sig.Bytes()},
				)
			}
			require.NoError(t, packErr)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": hexutil.Encode(packed),
			})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": "0x0",
			})
		}
	}))
}

// TestRelayerDaemon_CachesNonceAcrossSends is the regression test for the nonce-caching half of
// the RelayerDaemon nonce fix: relaying 2 messages back to back through the SAME daemon must
// reuse the in-memory nonce counter (sequential nonces, exactly one eth_getTransactionCount
// call) rather than re-querying the destination chain's nonce before every single send.
func TestRelayerDaemon_CachesNonceAcrossSends(t *testing.T) {
	const sourceChainID, destChainID, epoch = uint64(101), uint64(202), uint64(1)

	kpVal := bls.GenerateKeyPair()
	validatorEntry := cross_chain.ValidatorEntry{PubkeyBLS: kpVal.PublicKey().Bytes(), Stake: 1000}

	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	relayerKeyHex := hex.EncodeToString(crypto.FromECDSA(relayerKey))
	relayerAddr := crypto.PubkeyToAddress(relayerKey.PublicKey)

	sender := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	target := common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	msgA := cross_chain.CrossChainMessage{
		MessageID: common.HexToHash("0xA001"), SourceChainID: sourceChainID, DestChainID: destChainID,
		Sequence: 1, HopCount: 1, Sender: sender, Target: target, AssetID: big.NewInt(0), Value: big.NewInt(0),
		Payload: []byte{0x01}, Tip: big.NewInt(0),
		GasFee: big.NewInt(0),
	}
	msgB := cross_chain.CrossChainMessage{
		MessageID: common.HexToHash("0xA002"), SourceChainID: sourceChainID, DestChainID: destChainID,
		Sequence: 2, HopCount: 1, Sender: sender, Target: target, AssetID: big.NewInt(0), Value: big.NewInt(0),
		Payload: []byte{0x02}, Tip: big.NewInt(0),
		GasFee: big.NewInt(0),
	}
	// Both messages batched into ONE real commit tree (a real relayer commonly batches several
	// messages per commit) -- one commitRoot, two independent message proofs.
	commitRoot, layers, _, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msgA, msgB})
	require.NoError(t, err)
	proofA := cross_chain.GetMerkleProof(layers, 0)
	proofB := cross_chain.GetMerkleProof(layers, 1)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	rootAnchorSrv := newRootAnchorAttestationMock(t, sourceChainID, epoch, kpVal, validatorEntry, commitRoot)
	defer rootAnchorSrv.Close()

	const startingNonce = uint64(7)
	var getTxCountCalls int
	var sentNonces []uint64
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
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeBig(big.NewInt(int64(destChainID)))})
		case "eth_getTransactionCount":
			getTxCountCalls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeUint64(startingNonce)})
		case "eth_sendRawTransaction":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			rawHex, _ := params[0].(string)
			rawBytes, _ := hexutil.Decode(rawHex)
			var ethTx ethtypes.Transaction
			require.NoError(t, ethTx.UnmarshalBinary(rawBytes))
			signer := ethtypes.NewEIP155Signer(big.NewInt(int64(destChainID)))
			from, _ := ethtypes.Sender(signer, &ethTx)
			assert.Equal(t, relayerAddr, from)
			sentNonces = append(sentNonces, ethTx.Nonce())
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": ethTx.Hash().Hex()})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x0"})
		}
	}))
	defer destSrv.Close()

	cfg := DaemonConfig{
		RelayerKeyHex:  relayerKeyHex,
		RootAnchorURLs: []string{rootAnchorSrv.URL},
		ChainRPCURLs:   map[uint64]string{destChainID: destSrv.URL},
		PollInterval:   5 * time.Millisecond, MaxPollIterations: 10,
	}
	daemon, err := NewRelayerDaemon(cfg)
	require.NoError(t, err)
	defer daemon.Stop()

	_, err = daemon.RelayMessage(context.Background(), msgA, commitRoot, epoch, aggregateProof, proofA)
	require.NoError(t, err)
	_, err = daemon.RelayMessage(context.Background(), msgB, commitRoot, epoch, aggregateProof, proofB)
	require.NoError(t, err)

	assert.Equal(t, 1, getTxCountCalls, "second RelayMessage must reuse the cached nonce, not re-query the destination chain")
	assert.Equal(t, []uint64{startingNonce, startingNonce + 1}, sentNonces, "nonces must be sequential across the two sends")
}

// TestRelayerDaemon_RecoversPendingNonceOnFreshDaemon is the regression test for the actual bug
// this fix closes: RelayerDaemon used to query eth_getTransactionCount with "latest" (confirmed-
// only) before every send. If the daemon process crashes right after broadcasting a transaction
// but before it's mined, a fresh daemon restarting afterward would see the stale "latest" nonce
// and reuse the exact nonce of the still-pending transaction -- a real double-submit/nonce
// collision. A brand-new daemon instance here (no in-memory cache, simulating a fresh process
// after restart) must query the PENDING count instead, correctly stepping past the transaction
// that's already sitting unconfirmed in the mempool from "before the crash".
func TestRelayerDaemon_RecoversPendingNonceOnFreshDaemon(t *testing.T) {
	const sourceChainID, destChainID, epoch = uint64(101), uint64(202), uint64(1)

	kpVal := bls.GenerateKeyPair()
	validatorEntry := cross_chain.ValidatorEntry{PubkeyBLS: kpVal.PublicKey().Bytes(), Stake: 1000}

	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	relayerKeyHex := hex.EncodeToString(crypto.FromECDSA(relayerKey))
	relayerAddr := crypto.PubkeyToAddress(relayerKey.PublicKey)

	sender := common.HexToAddress("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")
	target := common.HexToAddress("0xDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD")
	msg := cross_chain.CrossChainMessage{
		MessageID: common.HexToHash("0xB001"), SourceChainID: sourceChainID, DestChainID: destChainID,
		Sequence: 1, HopCount: 1, Sender: sender, Target: target, AssetID: big.NewInt(0), Value: big.NewInt(0),
		Payload: []byte{0x03}, Tip: big.NewInt(0),
		GasFee: big.NewInt(0),
	}
	commitRoot, layers, _, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, err)
	messageProof := cross_chain.GetMerkleProof(layers, 0)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	rootAnchorSrv := newRootAnchorAttestationMock(t, sourceChainID, epoch, kpVal, validatorEntry, commitRoot)
	defer rootAnchorSrv.Close()

	// A prior (now-crashed) instance of this same relayer already broadcast one transaction at
	// nonce 5 that hasn't been mined yet: "latest" (confirmed-only) is still 5, but "pending"
	// (mempool-aware) is 6. Reusing 5 here would collide with that in-flight transaction.
	const confirmedNonce = uint64(5)
	const pendingNonce = uint64(6)
	var sentNonce uint64
	var sawPendingQuery bool
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
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeBig(big.NewInt(int64(destChainID)))})
		case "eth_getTransactionCount":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			blockTag, _ := params[1].(string)
			result := confirmedNonce
			if blockTag == "pending" {
				sawPendingQuery = true
				result = pendingNonce
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeUint64(result)})
		case "eth_sendRawTransaction":
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			rawHex, _ := params[0].(string)
			rawBytes, _ := hexutil.Decode(rawHex)
			var ethTx ethtypes.Transaction
			require.NoError(t, ethTx.UnmarshalBinary(rawBytes))
			signer := ethtypes.NewEIP155Signer(big.NewInt(int64(destChainID)))
			from, _ := ethtypes.Sender(signer, &ethTx)
			assert.Equal(t, relayerAddr, from)
			sentNonce = ethTx.Nonce()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": ethTx.Hash().Hex()})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x0"})
		}
	}))
	defer destSrv.Close()

	// A fresh daemon instance -- its nonces map starts empty, exactly like a real process
	// restart -- must be the one used here, not one that already has this dest chain cached.
	cfg := DaemonConfig{
		RelayerKeyHex:  relayerKeyHex,
		RootAnchorURLs: []string{rootAnchorSrv.URL},
		ChainRPCURLs:   map[uint64]string{destChainID: destSrv.URL},
		PollInterval:   5 * time.Millisecond, MaxPollIterations: 10,
	}
	daemon, err := NewRelayerDaemon(cfg)
	require.NoError(t, err)
	defer daemon.Stop()

	_, err = daemon.RelayMessage(context.Background(), msg, commitRoot, epoch, aggregateProof, messageProof)
	require.NoError(t, err)

	assert.True(t, sawPendingQuery, "RelayerDaemon must query the PENDING nonce, not just latest/confirmed")
	assert.Equal(t, pendingNonce, sentNonce, "must send at the pending nonce (6), not the stale confirmed one (5) which would collide with the already-in-flight transaction from before the restart")
}

// TestRelayerDaemon_DropsCachedNonceOnNonceError is the regression test for the third piece of
// the nonce fix: if the destination chain itself rejects a broadcast with a nonce-related error
// (e.g. because the cached counter drifted out of sync with real chain state for any reason),
// the daemon must drop its cached nonce for that chain instead of continuing to hand out a
// counter it no longer trusts -- the next send re-establishes a fresh baseline from the real
// pending count rather than compounding the drift.
func TestRelayerDaemon_DropsCachedNonceOnNonceError(t *testing.T) {
	const sourceChainID, destChainID, epoch = uint64(101), uint64(202), uint64(1)

	kpVal := bls.GenerateKeyPair()
	validatorEntry := cross_chain.ValidatorEntry{PubkeyBLS: kpVal.PublicKey().Bytes(), Stake: 1000}

	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	relayerKeyHex := hex.EncodeToString(crypto.FromECDSA(relayerKey))

	sender := common.HexToAddress("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE")
	target := common.HexToAddress("0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	msgA := cross_chain.CrossChainMessage{
		MessageID: common.HexToHash("0xC001"), SourceChainID: sourceChainID, DestChainID: destChainID,
		Sequence: 1, HopCount: 1, Sender: sender, Target: target, AssetID: big.NewInt(0), Value: big.NewInt(0),
		Payload: []byte{0x04}, Tip: big.NewInt(0),
		GasFee: big.NewInt(0),
	}
	msgB := cross_chain.CrossChainMessage{
		MessageID: common.HexToHash("0xC002"), SourceChainID: sourceChainID, DestChainID: destChainID,
		Sequence: 2, HopCount: 1, Sender: sender, Target: target, AssetID: big.NewInt(0), Value: big.NewInt(0),
		Payload: []byte{0x05}, Tip: big.NewInt(0),
		GasFee: big.NewInt(0),
	}
	commitRoot, layers, _, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msgA, msgB})
	require.NoError(t, err)
	proofA := cross_chain.GetMerkleProof(layers, 0)
	proofB := cross_chain.GetMerkleProof(layers, 1)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	rootAnchorSrv := newRootAnchorAttestationMock(t, sourceChainID, epoch, kpVal, validatorEntry, commitRoot)
	defer rootAnchorSrv.Close()

	const startingNonce = uint64(3)
	const refreshedPendingNonce = uint64(9) // real chain state after the drift is discovered
	sendAttempt := 0
	var getTxCountCalls int
	var sentNonces []uint64
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
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeBig(big.NewInt(int64(destChainID)))})
		case "eth_getTransactionCount":
			getTxCountCalls++
			result := startingNonce
			if getTxCountCalls > 1 {
				result = refreshedPendingNonce
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": hexutil.EncodeUint64(result)})
		case "eth_sendRawTransaction":
			sendAttempt++
			if sendAttempt == 1 {
				// Simulate the destination chain rejecting the first send as stale/out of sync.
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "error": map[string]interface{}{"code": -32000, "message": "nonce too low"}})
				return
			}
			var params []interface{}
			_ = json.Unmarshal(req.Params, &params)
			rawHex, _ := params[0].(string)
			rawBytes, _ := hexutil.Decode(rawHex)
			var ethTx ethtypes.Transaction
			require.NoError(t, ethTx.UnmarshalBinary(rawBytes))
			sentNonces = append(sentNonces, ethTx.Nonce())
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": ethTx.Hash().Hex()})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x0"})
		}
	}))
	defer destSrv.Close()

	cfg := DaemonConfig{
		RelayerKeyHex:  relayerKeyHex,
		RootAnchorURLs: []string{rootAnchorSrv.URL},
		ChainRPCURLs:   map[uint64]string{destChainID: destSrv.URL},
		PollInterval:   5 * time.Millisecond, MaxPollIterations: 10,
	}
	daemon, err := NewRelayerDaemon(cfg)
	require.NoError(t, err)
	defer daemon.Stop()

	// First send fails with a nonce error -- must drop the cached nonce rather than keep
	// handing out counters relative to a baseline the chain just told us is wrong.
	_, err = daemon.RelayMessage(context.Background(), msgA, commitRoot, epoch, aggregateProof, proofA)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "nonce")

	// Second send (different message, same daemon) must re-query the destination chain for a
	// fresh pending nonce instead of reusing/incrementing the now-untrusted cached value.
	_, err = daemon.RelayMessage(context.Background(), msgB, commitRoot, epoch, aggregateProof, proofB)
	require.NoError(t, err)

	assert.Equal(t, 2, getTxCountCalls, "must re-query the destination chain's nonce after a nonce-related send failure")
	require.Len(t, sentNonces, 1)
	assert.Equal(t, refreshedPendingNonce, sentNonces[0], "must use the freshly re-queried pending nonce, not the stale cached counter")
}
