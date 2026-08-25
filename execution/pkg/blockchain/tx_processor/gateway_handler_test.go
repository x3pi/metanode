package tx_processor

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
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
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.Ordered,
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
		big.NewInt(102), target, []byte{}, big.NewInt(0), big.NewInt(100), big.NewInt(0), uint8(1), false,
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
	if len(outValues) != 11 {
		t.Fatalf("expected 11 output values, got %d", len(outValues))
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
		big.NewInt(102), target, []byte{}, big.NewInt(0), big.NewInt(100), big.NewInt(10), uint8(1), false,
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
		big.NewInt(102), target, []byte{}, big.NewInt(0), big.NewInt(100), big.NewInt(10), uint8(100), false,
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
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.Ordered,
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

func TestGatewayHandler_BootstrapFoundingChains_CoordinatorGuards(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	coordinator := common.HexToAddress("0xCC00CC00CC00CC00CC00CC00CC00CC00CC00CC00")
	attacker := common.HexToAddress("0xAA00AA00AA00AA00AA00AA00AA00AA00AA00AA00")

	engine := cross_chain.NewGatewayEngine(9099, map[uint64]cross_chain.ChainRegistry{}, nil)
	engine.GenesisCoordinator = coordinator
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine failed: %v", err)
	}

	var payloads [][]byte
	for _, id := range []uint64{101, 102, 103, 104} {
		payloads = append(payloads, makeFoundingChainPayload(t, id))
	}

	calldata, err := h.abi.Pack("bootstrapFoundingChains", payloads)
	if err != nil {
		t.Fatalf("pack bootstrapFoundingChains: %v", err)
	}

	// Attacker tries to bootstrap -> must fail
	attackTx := newTx(attacker, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	_, _, failedAttack := h.HandleTransaction(context.Background(), cs, attackTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !failedAttack {
		t.Fatalf("expected attacker bootstrap to fail due to GenesisCoordinator check")
	}

	// Coordinator submits -> must succeed
	successTx := newTx(coordinator, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	_, _, failedSuccess := h.HandleTransaction(context.Background(), cs, successTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if failedSuccess {
		t.Fatalf("expected coordinator bootstrap to succeed")
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
		big.NewInt(int64(destChainID)), target, []byte{}, assetID, big.NewInt(100), big.NewInt(0), uint8(1), false,
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
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.Ordered,
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
