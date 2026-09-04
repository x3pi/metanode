package tx_processor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hashesToBytes32 converts a MerkleProof's []common.Hash siblings into the [][32]byte shape the
// GatewayABI's bytes32[] proofSiblings parameter expects.
func hashesToBytes32(hashes []common.Hash) [][32]byte {
	out := make([][32]byte, len(hashes))
	for i, h := range hashes {
		out[i] = h
	}
	return out
}

// newPersistentTestChainState is like newTestChainState (true_block_stm_integration_test.go)
// but backed by storage.NewMemoryDb() instead of storage.NewDummyStorage — DummyStorage's
// Get/Put/BatchPut are all no-ops (see storage/dummy_db.go), so it is unsuitable for a test that
// needs writes to actually be found again after rebuilding a ChainState from a committed root
// (i.e. simulating a restart). MemoryDb is a real (if non-persistent-to-disk) key/value store.
func newPersistentTestChainState(t *testing.T) (cs *blockchain.ChainState, accountStorage, codeStorage, scStorage storage.Storage) {
	t.Helper()

	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	accountStorage = storage.NewMemoryDb()
	codeStorage = storage.NewMemoryDb()
	scStorage = storage.NewMemoryDb()

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)

	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		t.Fatalf("failed to create persistent test chain state: %v", err)
	}
	return cs, accountStorage, codeStorage, scStorage
}

// TestGatewayHandler_OutboundPersistsAcrossChainStateReload is the Milestone A
// verification test (see plan doc "kind-gathering-lagoon"): a real ABI-encoded
// transaction to GATEWAY_CONTRACT_ADDRESS must (a) reach GatewayEngine.Outbound
// through the barrier-tx handler, (b) actually land in the real account/storage
// trie via the SAME production commit path tx_processor.go uses
// (SmartContractDB.LateBindRoots -> AccountStateDB.IntermediateRoot), and
// (c) still be readable after the ChainState wrapper is discarded and a FRESH
// one is reconstructed from the committed root + the same underlying storage
// (simulating a node restart) — proving this is real chainState persistence,
// not the in-memory Go maps GatewayEngine used before this wiring.
func TestGatewayHandler_OutboundPersistsAcrossChainStateReload(t *testing.T) {
	cs1, accountStorage, codeStorage, scStorage := newPersistentTestChainState(t)

	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Task 1.1: Seed initial real balance for sender (1000)
	if err := cs1.GetAccountStateDB().AddBalance(sender, big.NewInt(1000)); err != nil {
		t.Fatalf("AddBalance for sender failed: %v", err)
	}

	calldata, err := h.abi.Pack("outbound",
		big.NewInt(102),    // destChainId
		target,             // target
		[]byte{0xAA, 0xBB}, // payload
		big.NewInt(0),      // assetId
		big.NewInt(100),    // value
		big.NewInt(5),      // tip
		big.NewInt(0),      // gasFee
		uint8(1),           // hopCount
		false,              // ordered
	)
	if err != nil {
		t.Fatalf("pack outbound() calldata: %v", err)
	}
	callData := transaction.NewCallData(calldata)
	dataBytes, err := callData.Marshal()
	if err != nil {
		t.Fatalf("marshal CallData: %v", err)
	}

	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), dataBytes)
	messageID := tx.Hash()

	rcp, _, hasFailed := h.HandleTransaction(context.Background(), cs1, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if hasFailed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("outbound() transaction failed: status=%v returnData=%q", rcp, reason)
	}
	if rcp == nil || rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("expected successful receipt, got %+v", rcp)
	}

	// Verify real balance deduction (1000 - (100 value + 5 tip) = 895)
	as, err := cs1.GetAccountStateDB().AccountState(sender)
	if err != nil || as == nil || as.Balance().Cmp(big.NewInt(895)) != 0 {
		t.Fatalf("expected sender balance 895, got %v (err=%v)", as.Balance(), err)
	}

	// --- Commit through the REAL production sequence: tx_processor.go calls LateBindRoots()
	// mid-processing (binds the storage root into AccountState before AccountStateDB's own
	// IntermediateRoot runs, so the account root is deterministic); CommitAllStorage() (via
	// SmartContractDB.Commit(), called at block finalization by app_blockchain.go /
	// block_processor_commit.go) does the rest (code + event logs). Exercising both in sequence
	// here is what caught a real, pre-existing bug: LateBindRoots' non-NOMT branch committed a
	// throwaway trie.Copy() without persisting its nodes, silently relying on a LATER, separate
	// commit to do so — but that Copy()+Commit() marks the dirty state shared with the ORIGINAL
	// cached trie as clean, so the later commit found nothing left to write. Fixed in
	// smart_contract_db.go's LateBindRoots to persist immediately (matching what its NOMT branch
	// already did via CommitPayload()) — this test is the regression guard for that fix.
	if err := cs1.GetSmartContractDB().LateBindRoots(); err != nil {
		t.Fatalf("LateBindRoots failed: %v", err)
	}
	if err := cs1.GetSmartContractDB().CommitAllStorage(); err != nil {
		t.Fatalf("CommitAllStorage failed: %v", err)
	}
	if _, err := cs1.GetAccountStateDB().IntermediateRoot(true); err != nil {
		t.Fatalf("AccountStateDB.IntermediateRoot failed: %v", err)
	}
	// IntermediateRoot(true) (what tx_processor.go calls during block processing) only
	// computes the root in-memory; it does not persist trie nodes to storage. The actual
	// disk persistence — what a restart needs to find anything — happens at block
	// finalization via AccountStateDB.Commit() (block_state_commit.go's bc.Commit()).
	newAccountRoot, err := cs1.GetAccountStateDB().Commit()
	if err != nil {
		t.Fatalf("AccountStateDB.Commit failed: %v", err)
	}
	if newAccountRoot == (common.Hash{}) {
		t.Fatalf("expected non-zero account root after committing a Gateway write, got zero hash")
	}

	// Sanity: status IS observable on the same (uncommitted-reload) ChainState.
	viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), mustPackGetMessageStatus(t, h, messageID))
	statusBefore, err := h.HandleOffChainQuery(cs1, viewTx)
	if err != nil {
		t.Fatalf("getMessageStatus (pre-reload) failed: %v", err)
	}
	if len(statusBefore) == 0 || statusBefore[len(statusBefore)-1] != 0 {
		t.Fatalf("expected MessageStatusPending (0) pre-reload, got %x", statusBefore)
	}

	// --- Simulate a node restart: throw away cs1, rebuild a FRESH ChainState from
	// the committed root, reusing the SAME underlying storage.Storage instances. ---
	cs2 := reopenChainState(t, accountStorage, codeStorage, scStorage, newAccountRoot)

	statusAfter, err := h.HandleOffChainQuery(cs2, viewTx)
	if err != nil {
		t.Fatalf("getMessageStatus (post-reload) failed: %v", err)
	}
	if len(statusAfter) == 0 || statusAfter[len(statusAfter)-1] != 0 {
		t.Fatalf("Gateway state did NOT survive ChainState reload: expected MessageStatusPending (0), got %x — persistence is broken", statusAfter)
	}
}

func mustPackGetMessageStatus(t *testing.T, h *GatewayHandler, messageID common.Hash) []byte {
	t.Helper()
	data, err := h.abi.Pack("getMessageStatus", messageID)
	if err != nil {
		t.Fatalf("pack getMessageStatus() calldata: %v", err)
	}
	dataBytes, err := transaction.NewCallData(data).Marshal()
	if err != nil {
		t.Fatalf("marshal CallData: %v", err)
	}
	return dataBytes
}

// TestGatewayHandler_AttestCommitThenClaimMessage exercises the two highest-risk write paths
// (attestCommit, claimMessage) through real ABI-encoded transactions end-to-end — the part of
// gateway_handler.go with the most decode complexity (bytes32[] siblings, raw bytes signature/
// bitmap fields) that Outbound's test above doesn't touch. It seeds ChainRegistry directly via
// the package-internal load/saveGatewayEngine helpers (the real seeding path — governance /
// CommitteeUpdate over epoch transition — is Milestone C, not built yet; see the wiring plan).
func TestGatewayHandler_AttestCommitThenClaimMessage(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)

	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	// --- Seed a committee for source chain 101 (stand-in for Milestone C's CommitteeUpdate) ---
	kp := bls.GenerateKeyPair()
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine (seed) failed: %v", err)
	}
	engine.ChainRegistry[101] = cross_chain.ChainRegistry{
		ChainID: 101,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 100},
		},
		Epoch:           1,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine (seed) failed: %v", err)
	}

	sender := common.HexToAddress("0x3333333333333333333333333333333333333333")
	target := common.HexToAddress("0x4444444444444444444444444444444444444444")
	relayer := common.HexToAddress("0x5555555555555555555555555555555555555555")

	// Value=0 pure message (Section 2.2(a)) — keeps this test focused on decode/dispatch
	// correctness rather than also exercising the allocation ledger (already covered by
	// pkg/cross_chain's own extensive AttestCommit/ClaimMessage unit tests).
	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xCAFEBABECAFEBABECAFEBABECAFEBABECAFEBABECAFEBABECAFEBABECAFEBABE"),
		SourceChainID: 101,
		DestChainID:   0, // matches localChainID default when config.ConfigApp is nil (as in this test)
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
	// verifies aggregateProof against commitRoot itself, not a separately-declared StateRoot.
	commitRoot, commitLayers, _, aggIndex, errTree := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	if errTree != nil {
		t.Fatalf("BuildCommitTree: %v", errTree)
	}
	messageProof := cross_chain.GetMerkleProof(commitLayers, 0)
	aggregateProof := cross_chain.GetMerkleProof(commitLayers, aggIndex["0"])
	messageProofSiblings := hashesToBytes32(messageProof.Siblings)
	aggregateProofSiblings := hashesToBytes32(aggregateProof.Siblings)

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	// --- attestCommit(sourceChainId=101, commitRoot, aggregateAmount=0, assetId=0, proofLeafIndex, proofSiblings, epoch=1, sig, bitmap) ---
	attestCalldata, err := h.abi.Pack("attestCommit",
		big.NewInt(101), commitRoot, big.NewInt(0), big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof.LeafIndex), aggregateProofSiblings,
		uint64(1), sig.Bytes(), []byte{0x01},
	)
	if err != nil {
		t.Fatalf("pack attestCommit() calldata: %v", err)
	}
	attestTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata))
	rcp, _, hasFailed := h.HandleTransaction(context.Background(), cs, attestTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if hasFailed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("attestCommit() transaction failed: %q", reason)
	}

	// --- claimMessage(msg..., proof{leafIndex, siblings}, commitRoot) ---
	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		new(big.Int).SetUint64(messageProof.LeafIndex), messageProofSiblings, commitRoot,
	)
	if err != nil {
		t.Fatalf("pack claimMessage() calldata: %v", err)
	}
	claimTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	rcp2, _, hasFailed2 := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if hasFailed2 {
		reason := ""
		if rcp2 != nil {
			reason = string(rcp2.Return())
		}
		t.Fatalf("claimMessage() transaction failed: %q", reason)
	}

	// --- getMessageStatus(messageId) must now report Success (1) ---
	viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), mustPackGetMessageStatus(t, h, msg.MessageID))
	statusData, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getMessageStatus failed: %v", err)
	}
	if len(statusData) == 0 || statusData[len(statusData)-1] != uint8(cross_chain.MessageStatusSuccess) {
		t.Fatalf("expected MessageStatusSuccess (%d), got %x", cross_chain.MessageStatusSuccess, statusData)
	}

	// --- Double claim must fail ---
	rcp3, _, hasFailed3 := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !hasFailed3 {
		t.Fatalf("expected second claimMessage() to fail, but it succeeded: %+v", rcp3)
	}
}

// TestGatewayHandler_Refund covers the 4th and last write method (outbound, attestCommit,
// claimMessage covered above) through a real ABI-encoded transaction — refunding a message
// that was sent but never claimed (Section 2.4), then confirming double-refund is rejected.
func TestGatewayHandler_Refund(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)

	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	kp101 := bls.GenerateKeyPair()
	kp102 := bls.GenerateKeyPair()
	pop101 := cross_chain.PopSign(kp101.PrivateKey(), kp101.PublicKey())
	pop102 := cross_chain.PopSign(kp102.PrivateKey(), kp102.PublicKey())

	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine (seed) failed: %v", err)
	}
	engine.LocalChainID = 101
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {
			ChainID: 101,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: kp101.BytesPublicKey(), Stake: 1000, PopSignature: pop101.Bytes()},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		},
		102: {
			ChainID: 102,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: kp102.BytesPublicKey(), Stake: 1000, PopSignature: pop102.Bytes()},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		},
	}
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	if err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	engine.SupplyLedger = ledger
	engine.ReserveChainID = 101 // C8 fix: engine (chain 101) plays Reserve, attesting its own commit
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine (seed) failed: %v", err)
	}

	sender := common.HexToAddress("0x6666666666666666666666666666666666666666")
	target := common.HexToAddress("0x7777777777777777777777777777777777777777")

	// Task 1.1: Seed initial real balance for sender (500)
	if err := cs.GetAccountStateDB().AddBalance(sender, big.NewInt(500)); err != nil {
		t.Fatalf("AddBalance for sender failed: %v", err)
	}

	outboundCalldata, err := h.abi.Pack("outbound",
		big.NewInt(102), target, []byte{}, big.NewInt(0), big.NewInt(100), big.NewInt(0), big.NewInt(0), uint8(1), false,
	)
	if err != nil {
		t.Fatalf("pack outbound() calldata: %v", err)
	}
	outboundTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, outboundCalldata))
	messageID := outboundTx.Hash()
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, outboundTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("outbound() transaction failed: %q", reason)
	}

	// Verify balance after burn (500 - 100 = 400)
	asBeforeRefund, err := cs.GetAccountStateDB().AccountState(sender)
	if err != nil || asBeforeRefund == nil || asBeforeRefund.Balance().Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected sender balance 400 after outbound, got %v (err=%v)", asBeforeRefund.Balance(), err)
	}

	msg := cross_chain.CrossChainMessage{
		MessageID:     messageID,
		SourceChainID: 101,
		DestChainID:   102,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        target,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(100),
		Payload:       []byte{},
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
		Ordered:       false,
	}

	// Build commit tree
	commitRoot, layers, aggAmounts, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	if err != nil {
		t.Fatalf("BuildCommitTree failed: %v", err)
	}
	proof := cross_chain.GetMerkleProof(layers, 0)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	// Attest commit on chain 101
	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
	sig101 := bls.Sign(kp101.PrivateKey(), commitMsg)
	attestCalldata, err := h.abi.Pack("attestCommit",
		big.NewInt(101),
		commitRoot,
		aggAmounts["0"],
		big.NewInt(0),
		big.NewInt(int64(aggregateProof.LeafIndex)),
		aggregateProof.Siblings,
		uint64(1),
		sig101.Bytes(),
		[]byte{0x01},
	)
	if err != nil {
		t.Fatalf("pack attestCommit failed: %v", err)
	}
	attestTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, attestCalldata))
	if _, _, failAttest := h.HandleTransaction(context.Background(), cs, attestTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failAttest {
		t.Fatalf("attestCommit transaction failed")
	}

	// Chain 102 committee signs failure cert
	failMsg := cross_chain.ComputeMessageFailureAttestMessage(messageID, 102)
	failSig := bls.Sign(kp102.PrivateKey(), failMsg)

	refundCalldata, err := h.abi.Pack("refund",
		messageID,
		big.NewInt(101),
		big.NewInt(102),
		big.NewInt(1),
		uint8(1),
		sender,
		target,
		big.NewInt(0),
		big.NewInt(100),
		[]byte{},
		big.NewInt(0),
		big.NewInt(0),
		false,
		big.NewInt(int64(proof.LeafIndex)),
		proof.Siblings,
		commitRoot,
		uint64(1),
		failSig.Bytes(),
		[]byte{0x01},
	)
	if err != nil {
		t.Fatalf("pack refund() calldata: %v", err)
	}
	refundTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 2, big.NewInt(0), marshalCallData(t, refundCalldata))
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, refundTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("refund() transaction failed: %q", reason)
	}

	// Verify real balance restored back to sender (400 + 100 = 500)
	asAfterRefund, err := cs.GetAccountStateDB().AccountState(sender)
	if err != nil || asAfterRefund == nil || asAfterRefund.Balance().Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("expected sender balance restored to 500, got %v (err=%v)", asAfterRefund.Balance(), err)
	}

	viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), mustPackGetMessageStatus(t, h, messageID))
	statusData, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getMessageStatus failed: %v", err)
	}
	if len(statusData) == 0 || statusData[len(statusData)-1] != uint8(cross_chain.MessageStatusRefunded) {
		t.Fatalf("expected MessageStatusRefunded (%d), got %x", cross_chain.MessageStatusRefunded, statusData)
	}

	// Double refund must be rejected (idempotent guard, Section 2.4 point 3).
	refundTx2 := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 3, big.NewInt(0), marshalCallData(t, refundCalldata))
	rcpDup, _, failedDup := h.HandleTransaction(context.Background(), cs, refundTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !failedDup {
		t.Fatalf("expected second refund() to fail (already refunded), but it succeeded")
	}
	_ = rcpDup
}

// TestGatewayHandler_GetChainRegistry covers Milestone B's read side of the Go↔Root Anchor RPC
// channel: a remote chain reads this chain's ChainRegistry entry over eth_call. Exercises real
// ABI pack/unpack, not just the Go struct, since execution/pkg/cross_chain/rootanchor's client
// will decode this exact wire format.
func TestGatewayHandler_GetChainRegistry(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)

	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine (seed) failed: %v", err)
	}
	wantStateRoot := common.HexToHash("0xAAAABBBBCCCCDDDDAAAABBBBCCCCDDDDAAAABBBBCCCCDDDDAAAABBBBCCCCDDDD")
	wantAccountTreeRoot := common.HexToHash("0x1111222233334444111122223333444411112222333344441111222233334444")
	wantGatewayContract := common.HexToAddress("0x1234567890123456789012345678901234567890")
	engine.ChainRegistry[101] = cross_chain.ChainRegistry{
		ChainID: 101,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: kp1.PublicKey().Bytes(), Stake: 1000, PopSignature: []byte{0xAA, 0xBB}},
			{PubkeyBLS: kp2.PublicKey().Bytes(), Stake: 2000, PopSignature: []byte{0xCC, 0xDD}},
		},
		Epoch:            7,
		QuorumThreshold:  6667,
		GatewayContract:  wantGatewayContract,
		StateRoot:        wantStateRoot,
		AccountTreeRoot:  wantAccountTreeRoot,
		ArchivalEndpoint: "https://archive.example.com/chain-101",
		RegisteredAt:     1234567890,
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine (seed) failed: %v", err)
	}

	sender := common.HexToAddress("0x8888888888888888888888888888888888888888")

	calldata, err := h.abi.Pack("getChainRegistry", big.NewInt(101))
	if err != nil {
		t.Fatalf("pack getChainRegistry() calldata: %v", err)
	}
	viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))

	// Via HandleOffChainQuery directly (raw bytes)...
	data, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getChainRegistry (HandleOffChainQuery) failed: %v", err)
	}

	// ...and via HandleOffChainQueryResult (the ExecuteSCResult wrapper the eth_call dispatch
	// sites in transaction_processor_offchain.go actually call) — both must agree.
	result, err := h.HandleOffChainQueryResult(viewTx, cs)
	if err != nil {
		t.Fatalf("getChainRegistry (HandleOffChainQueryResult) failed: %v", err)
	}
	if result == nil || string(result.Return()) != string(data) {
		t.Fatalf("HandleOffChainQueryResult.Return() != HandleOffChainQuery output")
	}

	outValues, err := h.abi.Unpack("getChainRegistry", data)
	if err != nil {
		t.Fatalf("unpack getChainRegistry() output: %v", err)
	}
	if len(outValues) != 13 {
		t.Fatalf("expected 13 output values, got %d", len(outValues))
	}
	exists, _ := outValues[0].(bool)
	pubkeys, _ := outValues[1].([][]byte)
	stakes, _ := outValues[2].([]uint64)
	popSignatures, _ := outValues[3].([][]byte)
	epoch, _ := outValues[4].(uint64)
	quorumThreshold, _ := outValues[5].(uint64)
	gatewayContract, _ := outValues[6].(common.Address)
	stateRootRaw, _ := outValues[7].([32]byte)
	accountTreeRootRaw, _ := outValues[8].([32]byte)
	archivalEndpoint, _ := outValues[9].(string)
	registeredAt, _ := outValues[10].(uint64)
	genesisWallet, _ := outValues[11].(common.Address)
	genesisDigestRaw, _ := outValues[12].([32]byte)

	if !exists {
		t.Fatal("expected exists=true for a registered chain")
	}
	if len(pubkeys) != 2 || len(stakes) != 2 || len(popSignatures) != 2 {
		t.Fatalf("expected 2 committee members, got pubkeys=%d stakes=%d popSignatures=%d", len(pubkeys), len(stakes), len(popSignatures))
	}
	if stakes[0] != 1000 || stakes[1] != 2000 {
		t.Fatalf("stakes mismatch: got %v", stakes)
	}
	if string(pubkeys[0]) != string(kp1.PublicKey().Bytes()) || string(pubkeys[1]) != string(kp2.PublicKey().Bytes()) {
		t.Fatal("pubkeys mismatch")
	}
	if epoch != 7 {
		t.Fatalf("epoch = %d, want 7", epoch)
	}
	if quorumThreshold != 6667 {
		t.Fatalf("quorumThreshold = %d, want 6667", quorumThreshold)
	}
	if gatewayContract != wantGatewayContract {
		t.Fatalf("gatewayContract = %s, want %s", gatewayContract.Hex(), wantGatewayContract.Hex())
	}
	if common.Hash(stateRootRaw) != wantStateRoot {
		t.Fatalf("stateRoot = %s, want %s", common.Hash(stateRootRaw).Hex(), wantStateRoot.Hex())
	}
	if common.Hash(accountTreeRootRaw) != wantAccountTreeRoot {
		t.Fatalf("accountTreeRoot = %s, want %s", common.Hash(accountTreeRootRaw).Hex(), wantAccountTreeRoot.Hex())
	}
	if archivalEndpoint != "https://archive.example.com/chain-101" {
		t.Fatalf("archivalEndpoint = %q", archivalEndpoint)
	}
	if registeredAt != 1234567890 {
		t.Fatalf("registeredAt = %d, want 1234567890", registeredAt)
	}
	if genesisWallet != (common.Address{}) {
		t.Fatalf("genesisWallet = %s, want zero (this fixture registers via ProposalRegisterChain, not RegisterChainViaStake)", genesisWallet.Hex())
	}
	if common.Hash(genesisDigestRaw) != (common.Hash{}) {
		t.Fatalf("genesisDigest = %s, want zero (not yet published)", common.Hash(genesisDigestRaw).Hex())
	}
}

// TestGatewayHandler_GetChainRegistry_NotRegistered covers the fail-closed "not registered" case
// — a caller must be able to distinguish "chain not in registry" from "chain registered but
// happens to have zero-value fields" (a bug in a naive implementation would conflate the two).
func TestGatewayHandler_GetChainRegistry_NotRegistered(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)

	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	sender := common.HexToAddress("0x9999999999999999999999999999999999999999")
	calldata, err := h.abi.Pack("getChainRegistry", big.NewInt(999999))
	if err != nil {
		t.Fatalf("pack getChainRegistry() calldata: %v", err)
	}
	viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))

	data, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getChainRegistry failed: %v", err)
	}
	outValues, err := h.abi.Unpack("getChainRegistry", data)
	if err != nil {
		t.Fatalf("unpack getChainRegistry() output: %v", err)
	}
	exists, _ := outValues[0].(bool)
	if exists {
		t.Fatal("expected exists=false for an unregistered chain")
	}
}

func marshalCallData(t *testing.T, calldata []byte) []byte {
	t.Helper()
	dataBytes, err := transaction.NewCallData(calldata).Marshal()
	if err != nil {
		t.Fatalf("marshal CallData: %v", err)
	}
	return dataBytes
}

// reopenChainState rebuilds a ChainState pointed at newAccountRoot, reusing the SAME
// underlying storage.Storage instances — simulating a node restart (new process, same disk).
func reopenChainState(t *testing.T, accountStorage, codeStorage, scStorage storage.Storage, newAccountRoot common.Hash) *blockchain.ChainState {
	t.Helper()
	newHeader := block.NewBlockHeader(
		common.Hash{}, 0, newAccountRoot, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)
	cs2, err := blockchain.NewChainStateRemote(newHeader, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		t.Fatalf("failed to reopen chain state: %v", err)
	}
	return cs2
}

func TestGatewayHandler_OutboundFailsOnInsufficientBalance(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	sender := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	target := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")

	// Sender balance is 50 (insufficient for 100 value + 10 tip = 110)
	if err := cs.GetAccountStateDB().AddBalance(sender, big.NewInt(50)); err != nil {
		t.Fatalf("AddBalance failed: %v", err)
	}

	calldata, err := h.abi.Pack("outbound",
		big.NewInt(102), target, []byte{}, big.NewInt(0), big.NewInt(100), big.NewInt(10), big.NewInt(0), uint8(1), false,
	)
	if err != nil {
		t.Fatalf("pack outbound() calldata: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !failed {
		t.Fatalf("expected outbound() with insufficient balance to fail, but got success receipt: %+v", rcp)
	}
}

func TestGatewayHandler_OutboundFailsHopCountExceededDoesNotBurn(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	sender := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	target := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")

	// Seed balance
	if err := cs.GetAccountStateDB().AddBalance(sender, big.NewInt(1000)); err != nil {
		t.Fatalf("AddBalance failed: %v", err)
	}

	// MaxHopCount is 255. Set to 255 + something, but HopCount is uint8, so max is 255.
	// Wait, engine.Outbound fails if HopCount > cross_chain.MaxHopCount (which is 10).
	// Let's pass HopCount = 100.
	calldata, err := h.abi.Pack("outbound",
		big.NewInt(102), target, []byte{}, big.NewInt(0), big.NewInt(100), big.NewInt(10), big.NewInt(0), uint8(100), false,
	)
	if err != nil {
		t.Fatalf("pack outbound() calldata: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !failed {
		t.Fatalf("expected outbound() with HopCount=100 to fail, but got success receipt: %+v", rcp)
	}

	// Balance must remain unchanged at 1000
	as, err := cs.GetAccountStateDB().AccountState(sender)
	if err != nil || as == nil || as.Balance().Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("expected balance to remain 1000 after revert, got %v", as.Balance())
	}
}

func TestGatewayHandler_ClaimMessageMintsRealValue(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	kp := bls.GenerateKeyPair()
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	if err != nil {
		t.Fatalf("NewGlobalSupplyLedger failed: %v", err)
	}
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		101: {
			ChainID: 101,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		},
	}, ledger)
	engine.ReserveChainID = 102 // C8 fix: engine (chain 102) plays Reserve, attesting chain 101's commit

	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine failed: %v", err)
	}

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999"),
		SourceChainID: 101,
		DestChainID:   102,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        target,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(777),
		Payload:       []byte{0xDE, 0xAD},
		Tip:           big.NewInt(33),
		GasFee:        big.NewInt(0),
		Ordered:       false,
	}

	commitRoot, layers, aggAmounts, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	if err != nil {
		t.Fatalf("BuildCommitTree failed: %v", err)
	}
	messageProof := cross_chain.GetMerkleProof(layers, 0)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	// Attest commit
	attestCalldata, err := h.abi.Pack("attestCommit",
		big.NewInt(101), commitRoot, aggAmounts["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof.LeafIndex), hashesToBytes32(aggregateProof.Siblings),
		uint64(1), sig.Bytes(), []byte{0x01},
	)
	if err != nil {
		t.Fatalf("pack attestCommit failed: %v", err)
	}
	attestTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, attestTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		t.Fatalf("attestCommit failed")
	}

	// Claim message
	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
	)
	if err != nil {
		t.Fatalf("pack claimMessage failed: %v", err)
	}
	claimTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		t.Fatalf("claimMessage failed: status=%+v", rcp)
	}

	// Verify target received minted value (777)
	asTarget, err := cs.GetAccountStateDB().AccountState(target)
	if err != nil || asTarget == nil || asTarget.Balance().Cmp(big.NewInt(777)) != 0 {
		t.Fatalf("expected target balance 777, got %v (err=%v)", asTarget.Balance(), err)
	}

	// Verify relayer did NOT receive tip immediately (it accumulates in ledger)
	asRelayer, err := cs.GetAccountStateDB().AccountState(relayer)
	if err != nil || asRelayer == nil {
		// Valid, account might not exist
	} else if asRelayer.Balance().Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected relayer balance 0 (accumulates only), got %v (err=%v)", asRelayer.Balance(), err)
	}
}

// setupAndAttestRelayTestCommit builds a real 1-message commit tree, attests it on engine (chain
// 102 playing Reserve, matching TestGatewayHandler_ClaimMessageMintsRealValue's own fixture), and
// returns everything needed to then submit claimMessage against it. Shared by the 2-hop relay
// tests below to avoid repeating the BuildCommitTree/attestCommit boilerplate 3 times.
func setupAndAttestRelayTestCommit(t *testing.T, cs *blockchain.ChainState, h *GatewayHandler, msg cross_chain.CrossChainMessage, attesterKp *bls.KeyPair) (commitRoot common.Hash, messageProof cross_chain.MerkleProof) {
	t.Helper()
	commitRootLocal, layers, aggAmounts, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, err)
	messageProofLocal := cross_chain.GetMerkleProof(layers, 0)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRootLocal)
	sig := bls.Sign(attesterKp.PrivateKey(), commitMsg)

	attestCalldata, err := h.abi.Pack("attestCommit",
		new(big.Int).SetUint64(msg.SourceChainID), commitRootLocal, aggAmounts["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof.LeafIndex), hashesToBytes32(aggregateProof.Siblings),
		uint64(1), sig.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)
	attestTx := newTx(msg.Sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata))
	_, _, failed := h.HandleTransaction(context.Background(), cs, attestTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "attestCommit setup step must succeed")

	return commitRootLocal, messageProofLocal
}

// TestGatewayHandler_ClaimMessageRelaysOnwardViaReserve is the end-to-end regression test for the
// 2-hop A -> Reserve -> B value routing added 2026-08-28 (note/cross_chain_stake_and_value_flow.md):
// a claimMessage on Reserve (chain 102) for a message whose Payload carries a relay marker for
// chain 103 must (a) NOT credit the target's real balance directly on Reserve, and (b) queue a new
// real outbound message to chain 103 for the exact same Value/Target instead.
func TestGatewayHandler_ClaimMessageRelaysOnwardViaReserve(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp := bls.GenerateKeyPair()
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	require.NoError(t, err)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		// 103 must be a KNOWN chain for the relay-onward check to accept it as a valid final
		// destination -- an unregistered target fails closed (see the rejection test below).
		103: {ChainID: 103, Epoch: 0, QuorumThreshold: 6667},
	}, ledger)
	engine.ReserveChainID = 102
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222") // final recipient, on chain 103
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111AAAA1111AAAA1111AAAA1111"),
		SourceChainID: 101,
		DestChainID:   102, // immediate hop = Reserve, required for attestCommit's C8 ceiling check to pass
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        target,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(777),
		Payload:       cross_chain.EncodeRelayPayload(103, nil), // final destination
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
		Ordered:       false,
	}
	commitRoot, messageProof := setupAndAttestRelayTestCommit(t, cs, h, msg, kp)

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
	)
	require.NoError(t, err)
	claimTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "claimMessage with a valid relay marker must succeed: %+v", rcp)

	// (a) target must NOT have received a direct real-balance credit on Reserve (chain 102) --
	// that would double-spend the same Value once here and once on the eventual real destination.
	asTarget, err := cs.GetAccountStateDB().AccountState(target)
	if err == nil && asTarget != nil {
		assert.Equal(t, 0, asTarget.Balance().Sign(), "target must NOT be credited directly on the relay hub -- value must be relayed onward, not double-paid")
	}

	// (b) a new real outbound message to chain 103 must now be queued, for the same Target/Value,
	// with HopCount incremented.
	reloaded, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	pending := reloaded.PendingOutboundMessages[103]
	require.Len(t, pending, 1, "exactly one relayed-onward message must be queued for chain 103")
	assert.Equal(t, target, pending[0].Target)
	assert.Equal(t, 0, pending[0].Value.Cmp(big.NewInt(777)))
	assert.Equal(t, uint64(103), pending[0].DestChainID)
	assert.Equal(t, msg.HopCount+1, pending[0].HopCount)
}

// TestGatewayHandler_ClaimMessageRelay_ForwardsRealPayloadAndGasFee proves the extended relay
// marker (2026-08-29) correctly carries a REAL cross-chain CONTRACT_CALL payload and its locked
// GasFee budget onward -- not just a plain value transfer. Checks the queued leg-2 message's own
// fields directly; the leg-2 execution itself (a real claimMessage against a message whose
// Payload is real calldata) is exactly the pre-existing, already-proven
// TestGatewayHandler_ClaimMessagePayload_ExecutesRealContractCall code path, unmodified by this
// feature -- see TestGatewayHandler_ClaimMessageRelay_FullTwoHopContractCall below for the
// complete real-execution proof across 2 separate chain states.
func TestGatewayHandler_ClaimMessageRelay_ForwardsRealPayloadAndGasFee(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp := bls.GenerateKeyPair()
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	require.NoError(t, err)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		103: {ChainID: 103, Epoch: 0, QuorumThreshold: 6667},
	}, ledger)
	engine.ReserveChainID = 102
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222") // final contract on chain 103
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	realCalldata := []byte{0xa9, 0x05, 0x9c, 0xbb, 0xde, 0xad, 0xbe, 0xef} // stand-in ABI-encoded call, opaque to this hop
	gasFee := big.NewInt(500_000 * mt_common.MINIMUM_BASE_FEE)

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xDDDD4444DDDD4444DDDD4444DDDD4444DDDD4444DDDD4444DDDD4444DDDD4444"),
		SourceChainID: 101,
		DestChainID:   102,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        target,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(0), // pure CONTRACT_CALL relay: no value, just forwarding the call
		Payload:       cross_chain.EncodeRelayPayload(103, realCalldata),
		Tip:           big.NewInt(0),
		GasFee:        gasFee,
		Ordered:       false,
	}
	commitRoot, messageProof := setupAndAttestRelayTestCommit(t, cs, h, msg, kp)

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
	)
	require.NoError(t, err)
	claimTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "claimMessage relaying a real CONTRACT_CALL payload must succeed: %+v", rcp)

	reloaded, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	pending := reloaded.PendingOutboundMessages[103]
	require.Len(t, pending, 1)
	assert.Equal(t, realCalldata, pending[0].Payload, "the real inner calldata must be forwarded verbatim, not the relay marker itself")
	assert.Equal(t, 0, pending[0].GasFee.Cmp(gasFee), "the locked GasFee budget must carry forward unchanged for settlement at the final destination")
	assert.Equal(t, 0, pending[0].Value.Sign(), "pure CONTRACT_CALL relay carries no value")

	// No GasFee refund happens on THIS (intermediate) hop -- msg.Sender must NOT see any balance
	// change here, since GasFee settlement only ever happens at the FINAL destination's own
	// claimMessage (see TestGatewayHandler_ClaimMessageRelay_FullTwoHopContractCall).
	senderState, err := cs.GetAccountStateDB().AccountState(sender)
	if err == nil && senderState != nil {
		assert.Equal(t, 0, senderState.Balance().Sign(), "no GasFee refund should happen on an intermediate relay hop")
	}
}

// TestGatewayHandler_ClaimMessageRelay_FullTwoHopContractCall is the gold-standard, real-execution
// proof for A -> Reserve -> B CONTRACT_CALL routing: 2 SEPARATE chain states (csReserve, csB),
// a real deployed contract on csB, real BLS-attested commits for BOTH hops, and a real read of
// the contract's on-chain state afterwards -- not just claimMessage's own success/queue
// bookkeeping (mirrors TestGatewayHandler_ClaimMessagePayload_ExecutesRealContractCall's own
// "don't trust bookkeeping" rationale, extended across 2 hops).
func TestGatewayHandler_ClaimMessageRelay_FullTwoHopContractCall(t *testing.T) {
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp101 := bls.GenerateKeyPair()     // chain 101's real committee key (leg 1 attestation)
	kpReserve := bls.GenerateKeyPair() // Reserve (102)'s real committee key (leg 2 attestation)

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")
	deployer := common.HexToAddress("0x4444444444444444444444444444444444444444")

	// --- Chain B (103): where the real target contract actually lives ---
	csB, _, _, _ := newPersistentTestChainState(t)
	targetContract := deployTestWrappedAsset(t, csB, deployer, big.NewInt(0))
	engineB := cross_chain.NewGatewayEngine(103, map[uint64]cross_chain.ChainRegistry{
		102: {ChainID: 102, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpReserve.BytesPublicKey(), Stake: 1000}}, Epoch: 0, QuorumThreshold: 6667},
	}, nil)
	require.NoError(t, saveGatewayEngine(csB, engineB))

	parsedABI := testWrappedAssetABI(t)
	mintPayload, err := parsedABI.Pack("mint", recipient, big.NewInt(42))
	require.NoError(t, err)
	gasFee := big.NewInt(500_000 * mt_common.MINIMUM_BASE_FEE)

	// --- Chain Reserve (102): leg 1, A(101) -> Reserve, relay marker for B(103) ---
	csReserve, _, _, _ := newPersistentTestChainState(t)
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	require.NoError(t, err)
	engineReserve := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		103: {ChainID: 103, Epoch: 0, QuorumThreshold: 6667},
	}, ledger)
	engineReserve.ReserveChainID = 102
	require.NoError(t, saveGatewayEngine(csReserve, engineReserve))

	leg1Msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xEEEE5555EEEE5555EEEE5555EEEE5555EEEE5555EEEE5555EEEE5555EEEE5555"),
		SourceChainID: 101,
		DestChainID:   102,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        targetContract, // the REAL contract, deployed on csB above
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(0),
		Payload:       cross_chain.EncodeRelayPayload(103, mintPayload),
		Tip:           big.NewInt(0),
		GasFee:        gasFee,
		Ordered:       false,
	}
	commitRoot1, messageProof1 := setupAndAttestRelayTestCommit(t, csReserve, h, leg1Msg, kp101)

	claimCalldata1, err := h.abi.Pack("claimMessage",
		leg1Msg.MessageID, big.NewInt(int64(leg1Msg.SourceChainID)), big.NewInt(int64(leg1Msg.DestChainID)),
		big.NewInt(int64(leg1Msg.Sequence)), leg1Msg.HopCount, leg1Msg.Sender, leg1Msg.Target,
		leg1Msg.AssetID, leg1Msg.Value, leg1Msg.Payload, leg1Msg.Tip, leg1Msg.GasFee, leg1Msg.Ordered,
		new(big.Int).SetUint64(messageProof1.LeafIndex), hashesToBytes32(messageProof1.Siblings), commitRoot1,
	)
	require.NoError(t, err)
	claimTx1 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata1))
	rcp1, _, failed1 := h.HandleTransaction(context.Background(), csReserve, claimTx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed1, "leg 1 claimMessage on Reserve must succeed: %+v", rcp1)

	// Sanity: contract must NOT have been called yet -- leg 1 only ever runs on csReserve, which
	// has no knowledge of csB's state at all (2 fully independent ChainStates).
	require.Zero(t, realTokenBalanceOf(t, csB, targetContract, recipient).Sign(), "sanity: leg 1 alone must not have executed anything on chain B")

	// --- Extract the REAL queued leg-2 message exactly as a real relayer would read it ---
	reloadedReserve, err := loadGatewayEngine(csReserve)
	require.NoError(t, err)
	pending := reloadedReserve.PendingOutboundMessages[103]
	require.Len(t, pending, 1)
	leg2Msg := pending[0]
	assert.Equal(t, uint64(102), leg2Msg.SourceChainID, "leg 2's message must be sourced FROM Reserve itself")
	assert.Equal(t, mintPayload, leg2Msg.Payload)

	// --- Chain B: leg 2, Reserve -> B, real attestReserveIssuedCommit + real claimMessage ---
	commitRoot2, layers2, aggAmounts2, aggIndex2, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{leg2Msg})
	require.NoError(t, err)
	messageProof2 := cross_chain.GetMerkleProof(layers2, 0)
	aggregateProof2 := cross_chain.GetMerkleProof(layers2, aggIndex2["0"])
	commitMsg2 := cross_chain.ComputeCommitRootAttestMessage(commitRoot2)
	sig2 := bls.Sign(kpReserve.PrivateKey(), commitMsg2)

	attestCalldata2, err := h.abi.Pack("attestReserveIssuedCommit",
		big.NewInt(102), commitRoot2, aggAmounts2["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof2.LeafIndex), hashesToBytes32(aggregateProof2.Siblings),
		uint64(0), sig2.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)
	attestTx2 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata2))
	_, _, attestFailed2 := h.HandleTransaction(context.Background(), csB, attestTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, attestFailed2, "leg 2 attestReserveIssuedCommit on chain B must succeed -- a plain attestCommit here would have failed the C8 ceiling check since chain B is not the Reserve")

	claimCalldata2, err := h.abi.Pack("claimMessage",
		leg2Msg.MessageID, big.NewInt(int64(leg2Msg.SourceChainID)), big.NewInt(int64(leg2Msg.DestChainID)),
		big.NewInt(int64(leg2Msg.Sequence)), leg2Msg.HopCount, leg2Msg.Sender, leg2Msg.Target,
		leg2Msg.AssetID, leg2Msg.Value, leg2Msg.Payload, leg2Msg.Tip, leg2Msg.GasFee, leg2Msg.Ordered,
		new(big.Int).SetUint64(messageProof2.LeafIndex), hashesToBytes32(messageProof2.Siblings), commitRoot2,
	)
	require.NoError(t, err)
	claimTx2 := newHighGasTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata2))
	rcp2, _, failed2 := h.HandleTransaction(context.Background(), csB, claimTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed2, "leg 2 claimMessage on chain B must succeed: %+v", rcp2)

	// THE real, defining assertion: the contract's actual on-chain state on B changed for real.
	recipientBal := realTokenBalanceOf(t, csB, targetContract, recipient)
	assert.Equal(t, 0, recipientBal.Cmp(big.NewInt(42)), "expected the relayed mint(recipient, 42) call to have actually executed on chain B, got balance %s", recipientBal)

	// GasFee settles exactly once, at the TRUE final destination (B), not on the intermediate hop
	// (Reserve) -- some of the generous budget must have been refunded to msg.Sender HERE.
	senderStateB, err := csB.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.NotNil(t, senderStateB)
	assert.True(t, senderStateB.Balance().Sign() > 0, "expected a real unused-GasFee refund on chain B (the final destination), got %s", senderStateB.Balance())
	assert.True(t, senderStateB.Balance().Cmp(gasFee) < 0, "refund must be strictly less than the full locked gasFee (real gas was consumed)")
}

// TestGatewayHandler_ClaimMessageRelay_RejectsSelfLoop proves a relay marker naming the CLAIMING
// chain itself as the final destination fails closed rather than silently doing nothing useful.
func TestGatewayHandler_ClaimMessageRelay_RejectsSelfLoop(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp := bls.GenerateKeyPair()
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	require.NoError(t, err)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
	}, ledger)
	engine.ReserveChainID = 102
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	msg := cross_chain.CrossChainMessage{
		MessageID: common.HexToHash("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222BBBB2222BBBB2222BBBB2222"), SourceChainID: 101, DestChainID: 102,
		Sequence: 1, HopCount: 1, Sender: sender, Target: target, AssetID: big.NewInt(0), Value: big.NewInt(777),
		Payload: cross_chain.EncodeRelayPayload(102, nil), // self-loop: names the claiming chain itself
		Tip:     big.NewInt(0), GasFee: big.NewInt(0), Ordered: false,
	}
	commitRoot, messageProof := setupAndAttestRelayTestCommit(t, cs, h, msg, kp)

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
	)
	require.NoError(t, err)
	claimTx := newTx(common.HexToAddress("0x4444444444444444444444444444444444444444"), mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	_, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.True(t, failed, "a relay marker naming the claiming chain itself must fail closed")
}

// TestGatewayHandler_ClaimMessageRelay_RejectsUnknownDestination proves a relay marker naming a
// chain ID that isn't registered fails closed rather than silently crediting nothing to anyone.
func TestGatewayHandler_ClaimMessageRelay_RejectsUnknownDestination(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp := bls.GenerateKeyPair()
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	require.NoError(t, err)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		// note: chain 999 (the relay target below) is deliberately NOT registered here.
	}, ledger)
	engine.ReserveChainID = 102
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	msg := cross_chain.CrossChainMessage{
		MessageID: common.HexToHash("0xCCCC3333CCCC3333CCCC3333CCCC3333CCCC3333CCCC3333CCCC3333CCCC3333"), SourceChainID: 101, DestChainID: 102,
		Sequence: 1, HopCount: 1, Sender: sender, Target: target, AssetID: big.NewInt(0), Value: big.NewInt(777),
		Payload: cross_chain.EncodeRelayPayload(999, nil), // unregistered final destination
		Tip:     big.NewInt(0), GasFee: big.NewInt(0), Ordered: false,
	}
	commitRoot, messageProof := setupAndAttestRelayTestCommit(t, cs, h, msg, kp)

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
	)
	require.NoError(t, err)
	claimTx := newTx(common.HexToAddress("0x4444444444444444444444444444444444444444"), mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	_, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.True(t, failed, "a relay marker naming an unregistered chain must fail closed")
}

func TestGatewayHandler_WithdrawRelayerTip(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	relayer := common.HexToAddress("0x7777777777777777777777777777777777777777")
	engine := cross_chain.NewGatewayEngine(101, map[uint64]cross_chain.ChainRegistry{}, nil)
	engine.RelayerBalances[relayer] = big.NewInt(500)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine failed: %v", err)
	}

	withdrawCalldata, err := h.abi.Pack("withdrawRelayerTip")
	if err != nil {
		t.Fatalf("pack withdrawRelayerTip: %v", err)
	}
	withdrawTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, withdrawCalldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, withdrawTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		t.Fatalf("withdrawRelayerTip failed")
	}

	// Verify relayer received 500 in real balance
	asRelayer, err := cs.GetAccountStateDB().AccountState(relayer)
	if err != nil || asRelayer == nil || asRelayer.Balance().Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("expected relayer balance 500, got %v (err=%v)", asRelayer.Balance(), err)
	}

	// Second withdraw must fail because balance is empty
	withdrawTx2 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, withdrawCalldata))
	_, _, failed2 := h.HandleTransaction(context.Background(), cs, withdrawTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !failed2 {
		t.Fatalf("expected second withdrawRelayerTip to fail, but it succeeded")
	}
}

// TestGatewayHandler_RegisterChainViaStake_RequiresRealNativeStakeDeposit is the regression test
// for the 2026-08-28 rewrite of the vote-free registration path: BootstrapFoundingChains (a
// genesis-only, GenesisCoordinator-gated BATCH call) was retired, and RegisterChainViaStake's own
// stake check (previously against PerChainAllocation, a governance-only, non-wallet-transferable
// ledger entry) moved here, to gateway_handler.go, which is the only layer with real
// AccountStateDB access — gated by MinNativeStakeToRegister against the caller's REAL wallet
// balance (deliberately NOT any ERC-20-style token), burned/locked into GATEWAY_CONTRACT_ADDRESS
// as a permanent on-chain deposit on success.
func TestGatewayHandler_RegisterChainViaStake_RequiresRealNativeStakeDeposit(t *testing.T) {
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}
	minStake := big.NewInt(10_000)

	t.Run("unconfigured minimum fails closed", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		engine := cross_chain.NewGatewayEngine(9099, map[uint64]cross_chain.ChainRegistry{}, nil)
		// engine.MinNativeStakeToRegister left nil.
		if err := saveGatewayEngine(cs, engine); err != nil {
			t.Fatalf("saveGatewayEngine: %v", err)
		}

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
		if err := cs.GetAccountStateDB().AddBalance(caller, big.NewInt(1_000_000)); err != nil {
			t.Fatalf("AddBalance: %v", err)
		}
		calldata, err := h.abi.Pack("registerChainViaStake", makeFoundingChainPayload(t, 201), minStake)
		if err != nil {
			t.Fatalf("pack registerChainViaStake: %v", err)
		}
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		if _, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); !failed {
			t.Fatalf("expected registerChainViaStake to fail closed when MinNativeStakeToRegister is unconfigured, even with a well-funded caller")
		}
	})

	t.Run("insufficient real wallet balance fails closed, nothing registered, balance untouched", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		engine := cross_chain.NewGatewayEngine(9099, map[uint64]cross_chain.ChainRegistry{}, nil)
		engine.MinNativeStakeToRegister = minStake
		if err := saveGatewayEngine(cs, engine); err != nil {
			t.Fatalf("saveGatewayEngine: %v", err)
		}

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0001")
		if err := cs.GetAccountStateDB().AddBalance(caller, big.NewInt(5_000)); err != nil { // < minStake
			t.Fatalf("AddBalance: %v", err)
		}
		calldata, err := h.abi.Pack("registerChainViaStake", makeFoundingChainPayload(t, 202), minStake)
		if err != nil {
			t.Fatalf("pack registerChainViaStake: %v", err)
		}
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		if _, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); !failed {
			t.Fatalf("expected registerChainViaStake to fail closed with insufficient real balance")
		}

		as, err := cs.GetAccountStateDB().AccountState(caller)
		if err != nil || as == nil || as.Balance().Cmp(big.NewInt(5_000)) != 0 {
			t.Fatalf("expected caller balance to remain untouched at 5000, got %v", as.Balance())
		}
		reloaded, err := loadGatewayEngine(cs)
		if err != nil {
			t.Fatalf("loadGatewayEngine: %v", err)
		}
		if _, exists := reloaded.ChainRegistry[202]; exists {
			t.Fatalf("chain 202 must not be registered when the real stake deposit failed")
		}
	})

	t.Run("sufficient real wallet balance succeeds: balance debited, deposit locked, chain registered, no vote", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		engine := cross_chain.NewGatewayEngine(9099, map[uint64]cross_chain.ChainRegistry{}, nil)
		engine.MinNativeStakeToRegister = minStake
		if err := saveGatewayEngine(cs, engine); err != nil {
			t.Fatalf("saveGatewayEngine: %v", err)
		}

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0002")
		if err := cs.GetAccountStateDB().AddBalance(caller, big.NewInt(15_000)); err != nil {
			t.Fatalf("AddBalance: %v", err)
		}
		calldata, err := h.abi.Pack("registerChainViaStake", makeFoundingChainPayload(t, 203), minStake)
		if err != nil {
			t.Fatalf("pack registerChainViaStake: %v", err)
		}
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		if failed {
			t.Fatalf("expected registerChainViaStake to succeed with sufficient real balance: %+v", rcp)
		}

		as, err := cs.GetAccountStateDB().AccountState(caller)
		if err != nil || as == nil || as.Balance().Cmp(big.NewInt(5_000)) != 0 {
			t.Fatalf("expected caller balance to be debited by exactly minStake (15000-10000=5000), got %v", as.Balance())
		}
		gatewayAs, err := cs.GetAccountStateDB().AccountState(mt_common.GATEWAY_CONTRACT_ADDRESS)
		if err != nil || gatewayAs == nil || gatewayAs.Balance().Cmp(minStake) != 0 {
			t.Fatalf("expected GATEWAY_CONTRACT_ADDRESS to hold the locked deposit (%s), got %v", minStake.String(), gatewayAs)
		}

		reloaded, err := loadGatewayEngine(cs)
		if err != nil {
			t.Fatalf("loadGatewayEngine: %v", err)
		}
		if _, exists := reloaded.ChainRegistry[203]; !exists {
			t.Fatalf("chain 203 must be registered after a sufficient real stake deposit")
		}
		if !reloaded.Governance.ActiveChains[203] {
			t.Fatalf("chain 203 must also become a voting member, with no vote ever cast")
		}
	})
}

// computeProposalIDForTest mirrors GovernanceEngine.Propose's exact proposalID derivation
// (governance.go: keccak256(kind_byte || proposedAt_be64 || payload)) so the test can predict
// which proposalID a given claimed timestamp WOULD produce, without relying on the handler to
// tell it — the whole point is to prove the handler no longer echoes back whatever timestamp
// the caller claims.
func computeProposalIDForTest(kind uint8, proposedAt uint64, payload []byte) common.Hash {
	buf := []byte{kind}
	var tsBytes [8]byte
	binary.BigEndian.PutUint64(tsBytes[:], proposedAt)
	buf = append(buf, tsBytes[:]...)
	buf = append(buf, payload...)
	return crypto.Keccak256Hash(buf)
}

// TestGatewayHandler_GovernanceTimestamps_IgnoreCallerSuppliedValue is the regression test for
// the timestamp-trust gap found during live 2-node testing (note/
// cross_chain_production_readiness_plan.md Phase 0.9): propose()/vote()/executeProposal() used
// to trust a raw caller-supplied "currentTimestamp"/"proposedAt" ABI argument with nothing to
// cross-check it against, letting a caller claim an arbitrary future timestamp to make the
// mandatory 72h timelock appear satisfied immediately. The fix ignores that argument entirely
// and always uses the real, consensus-agreed block time instead.
func TestGatewayHandler_GovernanceTimestamps_IgnoreCallerSuppliedValue(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	kp := bls.GenerateKeyPair()
	popSig := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	registry := map[uint64]cross_chain.ChainRegistry{
		101: {
			ChainID:         101,
			Committee:       []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 10000, PopSignature: popSig.Bytes()}},
			Epoch:           1,
			QuorumThreshold: 6667,
		},
	}
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(0), map[uint64]*big.Int{})
	if err != nil {
		t.Fatalf("NewGlobalSupplyLedger: %v", err)
	}
	engine := cross_chain.NewGatewayEngine(101, registry, ledger)
	engine.EnsureGovernance()   // ActiveChains = {101} -> quorum threshold = 1
	engine.ReserveChainID = 999 // C7 fix: matches this test's payload chain_id below — this test
	// is about propose/vote/executeProposal timestamp handling, not allocation semantics, so it
	// just needs its ProposalAllocateSupply payload to target a validly-configured Reserve.
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	realProposeBlockTime := uint64(1_000_000)
	fakeFarFutureTimestamp := uint64(9_999_999_999) // an attacker's claimed timestamp, decades ahead

	kind := uint8(cross_chain.ProposalAllocateSupply)
	payload := []byte(`{"chain_id":999,"amount":1}`)

	proposeCalldata, err := h.abi.Pack("propose", kind, payload, fakeFarFutureTimestamp)
	if err != nil {
		t.Fatalf("pack propose: %v", err)
	}
	fee := big.NewInt(100_000_000_000_000_000)
	proposeTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, fee, marshalCallData(t, proposeCalldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, proposeTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, realProposeBlockTime); failed {
		t.Fatalf("expected propose to succeed")
	}

	// The proposalID a real caller would derive using the ATTACKER's claimed timestamp must
	// NOT exist — proving the handler never fed that value into Propose().
	fakeProposalID := computeProposalIDForTest(kind, fakeFarFutureTimestamp, payload)
	fakeCalldata, err := h.abi.Pack("getProposal", fakeProposalID)
	if err != nil {
		t.Fatalf("pack getProposal (fake): %v", err)
	}
	fakeOut, err := h.HandleOffChainQuery(cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, fakeCalldata)))
	if err != nil {
		t.Fatalf("getProposal (fake) query: %v", err)
	}
	fakeValues, err := h.abi.Unpack("getProposal", fakeOut)
	if err != nil {
		t.Fatalf("unpack getProposal (fake): %v", err)
	}
	if exists, _ := fakeValues[0].(bool); exists {
		t.Fatalf("proposal keyed on the attacker's claimed timestamp must not exist")
	}

	// The proposal actually keyed on the REAL block time must exist, and report proposedAt ==
	// the real block time, not the attacker's claim.
	realProposalID := computeProposalIDForTest(kind, realProposeBlockTime, payload)
	realCalldata, err := h.abi.Pack("getProposal", realProposalID)
	if err != nil {
		t.Fatalf("pack getProposal (real): %v", err)
	}
	realOut, err := h.HandleOffChainQuery(cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, realCalldata)))
	if err != nil {
		t.Fatalf("getProposal (real) query: %v", err)
	}
	realValues, err := h.abi.Unpack("getProposal", realOut)
	if err != nil {
		t.Fatalf("unpack getProposal (real): %v", err)
	}
	if exists, _ := realValues[0].(bool); !exists {
		t.Fatalf("proposal keyed on the real block time must exist")
	}
	if proposedAt, _ := realValues[4].(uint64); proposedAt != realProposeBlockTime {
		t.Fatalf("proposedAt = %d, want real block time %d (not the attacker's claim %d)", proposedAt, realProposeBlockTime, fakeFarFutureTimestamp)
	}

	// Vote, again claiming the fake far-future timestamp. A single vote reaches quorum
	// (threshold=1), transitioning straight to Timelocked.
	voteMsg := cross_chain.ComputeGovernanceVoteMessage(realProposalID, uint64(101))
	voteSig := bls.Sign(kp.PrivateKey(), voteMsg)
	voteCalldata, err := h.abi.Pack("vote", realProposalID, new(big.Int).SetUint64(101), fakeFarFutureTimestamp, kp.BytesPublicKey(), voteSig.Bytes())
	if err != nil {
		t.Fatalf("pack vote: %v", err)
	}
	realVoteBlockTime := realProposeBlockTime + 10
	voteTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, voteCalldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, voteTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, realVoteBlockTime); failed {
		t.Fatalf("expected vote to succeed")
	}

	// effectiveAt must be derived from the REAL vote block time, not the attacker's claim.
	postVoteOut, err := h.HandleOffChainQuery(cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, realCalldata)))
	if err != nil {
		t.Fatalf("getProposal (post-vote) query: %v", err)
	}
	postVoteValues, err := h.abi.Unpack("getProposal", postVoteOut)
	if err != nil {
		t.Fatalf("unpack getProposal (post-vote): %v", err)
	}
	wantEffectiveAt := realVoteBlockTime + cross_chain.DefaultGovernanceTimelockSeconds
	if effectiveAt, _ := postVoteValues[5].(uint64); effectiveAt != wantEffectiveAt {
		t.Fatalf("effectiveAt = %d, want %d (real vote time + 72h) — NOT derived from the attacker's claimed timestamp %d", effectiveAt, wantEffectiveAt, fakeFarFutureTimestamp)
	}

	// Attacker attempts executeProposal claiming the far-future timestamp — the REAL block
	// time attached to this transaction is still nowhere near the 72h timelock, so this MUST
	// still fail even though the calldata claims otherwise.
	executeCalldata, err := h.abi.Pack("executeProposal", realProposalID, fakeFarFutureTimestamp)
	if err != nil {
		t.Fatalf("pack executeProposal: %v", err)
	}
	earlyExecuteTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 2, big.NewInt(0), marshalCallData(t, executeCalldata))
	_, _, earlyFailed := h.HandleTransaction(context.Background(), cs, earlyExecuteTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, realVoteBlockTime+100)
	if !earlyFailed {
		t.Fatalf("expected executeProposal to fail — the attacker's claimed timestamp must not bypass the real 72h timelock")
	}

	// Once the REAL block time genuinely passes the timelock, execution succeeds.
	lateExecuteTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 3, big.NewInt(0), marshalCallData(t, executeCalldata))
	realExecuteBlockTime := wantEffectiveAt + 1
	if _, _, failed := h.HandleTransaction(context.Background(), cs, lateExecuteTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, realExecuteBlockTime); failed {
		t.Fatalf("expected executeProposal to succeed once the real block time genuinely passes the timelock")
	}
}

func TestGatewayHandler_CustomAsset_Outbound_ClaimMessage(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	sender := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	target := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")

	assetID := big.NewInt(999)
	homeChainID := uint64(101)
	destChainID := uint64(102)

	// Register a mock custom asset
	supplyLedger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(1000), nil)
	engine := cross_chain.NewGatewayEngine(homeChainID, map[uint64]cross_chain.ChainRegistry{}, supplyLedger)
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry, nil)

	entry := &cross_chain.AssetEntry{
		AssetID:           assetID,
		Active:            true,
		HomeChainID:       homeChainID,
		CanonicalContract: common.BytesToAddress([]byte{2}), // SHA256 precompile
		WrappedContracts: map[uint64]common.Address{
			destChainID: common.BytesToAddress([]byte{2}), // SHA256 precompile
		},
	}
	engine.AssetRegistry.Assets[assetID.String()] = entry
	engine.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetID.String(), homeChainID)] = big.NewInt(1000)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine failed: %v", err)
	}

	// 1. Outbound on Home Chain (101)
	outboundCalldata, err := h.abi.Pack("outbound",
		big.NewInt(int64(destChainID)), target, []byte{}, assetID, big.NewInt(100), big.NewInt(0), big.NewInt(0), uint8(1), false,
	)
	if err != nil {
		t.Fatalf("pack outbound: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, outboundCalldata))

	// Execute outbound (transferFrom will fail because the mock contract doesn't exist)
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !failed {
		t.Fatalf("expected outbound custom asset to fail due to missing contract, but it succeeded")
	}
	if !strings.Contains(string(rcp.Return()), "outbound custom asset transferFrom failed") {
		t.Fatalf("expected transferFrom EVM call to fail, got: %s", string(rcp.Return()))
	}

	// 2. ClaimMessage on Dest Chain (102)
	// We simulate receiving the message on chain 102
	engine3 := cross_chain.NewGatewayEngine(destChainID, map[uint64]cross_chain.ChainRegistry{
		homeChainID: {
			ChainID: homeChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, nil)
	engine3.AssetRegistry = engine.AssetRegistry // use the same registry config
	if err := saveGatewayEngine(cs, engine3); err != nil {
		t.Fatalf("saveGatewayEngine for claim failed: %v", err)
	}

	// Create fake proof just to bypass the verification (or use dummy zeroes since we only test the handler layer)
	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x123"),
		SourceChainID: homeChainID,
		DestChainID:   destChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        entry.WrappedContracts[destChainID],
		AssetID:       assetID,
		Value:         big.NewInt(100),
		Payload:       target.Bytes(), // recipient is in Payload
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
	}

	// Compute leaf hash to mock the root
	leafHash := cross_chain.ComputeMessageLeafHash(msg)

	// Manually inject the message into pending state to bypass AttestCommit
	engine3.MessageStatus[msg.MessageID] = cross_chain.MessageStatusPending
	engine3.AttestedCommits[fmt.Sprintf("%d:%s:%s", homeChainID, leafHash.Hex(), assetID.String())] = cross_chain.AttestedCommit{
		SourceChainID: homeChainID,
		CommitRoot:    leafHash,
		AssetID:       assetID,
		Epoch:         1,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}
	if err := saveGatewayEngine(cs, engine3); err != nil {
		t.Fatalf("saveGatewayEngine for pending message failed: %v", err)
	}

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		big.NewInt(0), [][32]byte{}, leafHash,
	)
	if err != nil {
		t.Fatalf("pack claimMessage: %v", err)
	}

	claimTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	rcpClaim, _, failedClaim := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

	if !failedClaim {
		t.Fatalf("expected claimMessage custom asset to fail due to missing contract, but it succeeded")
	}
	if !strings.Contains(string(rcpClaim.Return()), "claim custom asset execution failed") {
		t.Fatalf("expected EVM mint call to fail, got: %s", string(rcpClaim.Return()))
	}
}

func makeFoundingChainPayload(t *testing.T, chainID uint64) []byte {
	t.Helper()
	kp := bls.GenerateKeyPair()
	popSig := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	reg := cross_chain.ChainRegistry{
		ChainID: chainID,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000, PopSignature: popSig.Bytes()},
		},
		Epoch:            0,
		QuorumThreshold:  6667,
		GatewayContract:  common.Address{},
		StateRoot:        common.Hash{},
		AccountTreeRoot:  common.Hash{},
		ArchivalEndpoint: "",
		RegisteredAt:     0,
	}
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal ChainRegistry failed: %v", err)
	}
	return b
}

// TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce is the regression guard
// for a real chain-halting bug found + fixed 2026-08-26 via live E2E testing of the P4 relayer
// automation: HandleSuccessTransaction/HandleRevertedTransaction (receipt_helper.go) call
// VmProcessor.ExecuteNonceOnly to bump the sender's nonce, but ExecuteNonceOnly's own
// UpdateStateDB (vm_processor_state.go) deliberately SKIPS the sender's own address --
// its "NONCE-FIX" comment explains that regular parallel-EVM transactions already have their
// nonce bumped beforehand via mvccDB.PlusOneNonce (true_block_stm.go's runParallelSegment), so
// applying MVM's returned nonce there too would double-increment.
//
// That assumption does not hold for barrier transactions (GATEWAY_CONTRACT_ADDRESS /
// VALIDATOR_CONTRACT_ADDRESS, executed via runBarrierTx): they never go through
// runParallelSegment's pre-increment at all. Before this fix, a barrier transaction's sender
// nonce silently never advanced -- the FIRST gateway transaction from an address always
// succeeded (its own nonce check passed against the account's untouched starting nonce), but a
// SECOND one from the SAME sender could never pass runBarrierTx's own
// "fromAccount.Nonce() != tx.GetNonce()" guard, because the account's real nonce had not moved.
// Live, this manifested as the relayer's automated attestCommit() (nonce N) succeeding and then
// claimMessage() (nonce N+1) getting stuck as a permanently-unfulfillable "future" transaction
// in the pool -- which, worse, halted ALL further block production on the chain (not just that
// one transfer), since the executor's tx-forwarding loop only proceeds when the pool yields at
// least one valid transaction.
//
// This test exercises the exact same code path the live bug went through (HandleTransaction ->
// handleWrite -> HandleSuccessTransaction), without needing a live multi-node cluster: two
// outbound() calls from the same sender, at nonce 0 and nonce 1, both real ABI transactions.
func TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)

	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	sender := common.HexToAddress("0x4444444444444444444444444444444444444444")
	target := common.HexToAddress("0x5555555555555555555555555555555555555555")

	if err := cs.GetAccountStateDB().AddBalance(sender, big.NewInt(10_000)); err != nil {
		t.Fatalf("AddBalance for sender failed: %v", err)
	}

	packOutbound := func() []byte {
		calldata, err := h.abi.Pack("outbound",
			big.NewInt(102), target, []byte{}, big.NewInt(0), big.NewInt(1),
			big.NewInt(0), big.NewInt(0), uint8(0), false,
		)
		if err != nil {
			t.Fatalf("pack outbound() calldata: %v", err)
		}
		return marshalCallData(t, calldata)
	}

	assertNonce := func(want uint64) {
		t.Helper()
		as, err := cs.GetAccountStateDB().AccountState(sender)
		if err != nil || as == nil {
			t.Fatalf("AccountState(sender) failed: %v", err)
		}
		if as.Nonce() != want {
			t.Fatalf("expected sender nonce %d after processing, got %d", want, as.Nonce())
		}
	}

	assertNonce(0)

	tx1 := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), packOutbound())
	rcp1, _, hasFailed1 := h.HandleTransaction(context.Background(), cs, tx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if hasFailed1 || rcp1 == nil || rcp1.Status() != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("first outbound() (nonce 0) failed: hasFailed=%v rcp=%+v", hasFailed1, rcp1)
	}
	// The actual bug: without the fix, this stays 0 forever, and a second real transaction
	// from this sender can never be admitted by runBarrierTx's own nonce guard again.
	assertNonce(1)

	tx2 := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), packOutbound())
	rcp2, _, hasFailed2 := h.HandleTransaction(context.Background(), cs, tx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if hasFailed2 || rcp2 == nil || rcp2.Status() != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("second outbound() (nonce 1) failed: hasFailed=%v rcp=%+v", hasFailed2, rcp2)
	}
	assertNonce(2)
}
