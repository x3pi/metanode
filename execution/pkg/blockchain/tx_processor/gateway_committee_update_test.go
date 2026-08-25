package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// mustPackGetRegisteredPop / mustPackGetCommitteeAttestationShares mirror
// mustPackGetMessageStatus's helper style (gateway_handler_test.go).
func mustPackGetRegisteredPop(t *testing.T, h *GatewayHandler, pubkeyBls []byte) []byte {
	t.Helper()
	data, err := h.abi.Pack("getRegisteredPop", pubkeyBls)
	if err != nil {
		t.Fatalf("pack getRegisteredPop() calldata: %v", err)
	}
	return marshalCallData(t, data)
}

func mustPackGetCommitteeAttestationShares(t *testing.T, h *GatewayHandler, sourceChainID, oldEpoch uint64, payloadHash common.Hash) []byte {
	t.Helper()
	data, err := h.abi.Pack("getCommitteeAttestationShares", new(big.Int).SetUint64(sourceChainID), oldEpoch, payloadHash)
	if err != nil {
		t.Fatalf("pack getCommitteeAttestationShares() calldata: %v", err)
	}
	return marshalCallData(t, data)
}

// committeeMember bundles what one simulated validator needs across the test: its BLS keypair
// and the ValidatorEntry it contributes to the OLD (currently registered) committee.
type committeeMember struct {
	kp    *bls.KeyPair
	entry cross_chain.ValidatorEntry
}

func newCommitteeMember(t *testing.T, stake uint64) committeeMember {
	t.Helper()
	kp := bls.GenerateKeyPair()
	popSig := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	return committeeMember{
		kp: kp,
		entry: cross_chain.ValidatorEntry{
			PubkeyBLS:    kp.PublicKey().Bytes(),
			Stake:        stake,
			PopSignature: popSig.Bytes(),
		},
	}
}

// TestGatewayHandler_CommitteeUpdate_FullQuorumLifecycle exercises the entire Milestone C
// pipeline end to end through real ABI-encoded transactions: 4 old-committee members register
// their PoP, 3 of them (>= 2/3 stake) submit real BLS attestation shares over the SAME
// domain-separated digest, the aggregate is built with the exact primitives
// attestCommitInternal uses (bls.CreateAggregateSign), and committeeUpdate() is submitted and
// verified for real (not a caller-supplied bool) via cross_chain.ApplyCommitteeUpdate.
func TestGatewayHandler_CommitteeUpdate_FullQuorumLifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	const sourceChainID = 101
	const oldEpoch = 5

	// --- Seed the OLD committee (4 members, equal stake) directly, standing in for a prior
	// CommitteeUpdate/genesis registration (Milestone D) — not what this test exercises. ---
	old := []committeeMember{
		newCommitteeMember(t, 1000),
		newCommitteeMember(t, 1000),
		newCommitteeMember(t, 1000),
		newCommitteeMember(t, 1000),
	}
	oldCommittee := make([]cross_chain.ValidatorEntry, len(old))
	for i, m := range old {
		oldCommittee[i] = m.entry
	}
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine (seed): %v", err)
	}
	engine.ChainRegistry[sourceChainID] = cross_chain.ChainRegistry{
		ChainID:         sourceChainID,
		Committee:       oldCommittee,
		Epoch:           oldEpoch,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine (seed): %v", err)
	}

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	var nonce uint64

	// --- Every OLD member registers their PoP (Milestone C's fix for the PoP-durability gap). ---
	for _, m := range old {
		calldata, err := h.abi.Pack("registerCommitteePop", m.entry.PubkeyBLS, m.entry.PopSignature)
		if err != nil {
			t.Fatalf("pack registerCommitteePop: %v", err)
		}
		tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, nonce, big.NewInt(0), marshalCallData(t, calldata))
		nonce++
		if rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
			reason := ""
			if rcp != nil {
				reason = string(rcp.Return())
			}
			t.Fatalf("registerCommitteePop failed for a member: %q", reason)
		}
	}

	// A brand new member joins the committee (proves this isn't just re-confirming the same list).
	newMember := newCommitteeMember(t, 500)
	{
		calldata, err := h.abi.Pack("registerCommitteePop", newMember.entry.PubkeyBLS, newMember.entry.PopSignature)
		if err != nil {
			t.Fatalf("pack registerCommitteePop (new member): %v", err)
		}
		tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, nonce, big.NewInt(0), marshalCallData(t, calldata))
		nonce++
		if rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
			reason := ""
			if rcp != nil {
				reason = string(rcp.Return())
			}
			t.Fatalf("registerCommitteePop failed for new member: %q", reason)
		}
	}

	// --- Verify getRegisteredPop round-trips a real PoP. ---
	viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), mustPackGetRegisteredPop(t, h, old[0].entry.PubkeyBLS))
	popData, err := h.HandleOffChainQuery(cs, viewTx)
	if err != nil {
		t.Fatalf("getRegisteredPop failed: %v", err)
	}
	outValues, err := h.abi.Unpack("getRegisteredPop", popData)
	if err != nil {
		t.Fatalf("unpack getRegisteredPop: %v", err)
	}
	gotPop, _ := outValues[0].([]byte)
	if string(gotPop) != string(old[0].entry.PopSignature) {
		t.Fatal("getRegisteredPop did not return the registered PoP")
	}

	// --- Build the new committee (old 4 + 1 new member) and compute the shared digest. ---
	newCommittee := append(append([]cross_chain.ValidatorEntry{}, oldCommittee...), newMember.entry)
	newEpoch := uint64(oldEpoch + 1)
	stateRoot := common.HexToHash("0xFEEDFACEFEEDFACEFEEDFACEFEEDFACEFEEDFACEFEEDFACEFEEDFACEFEEDFACE")
	accountTreeRoot := common.HexToHash("0xCAFEBABEDEADBEAFCAFEBABEDEADBEAFCAFEBABEDEADBEAFCAFEBABEDEADBEAF")
	payloadHash := cross_chain.ComputeCommitteeUpdateDigest(sourceChainID, newEpoch, newCommittee, stateRoot, accountTreeRoot)

	// --- 3 of 4 OLD members (3000/4000 = 75% >= 2/3) submit real BLS attestation shares. ---
	signers := old[:3]
	for _, m := range signers {
		sig := bls.Sign(m.kp.PrivateKey(), payloadHash.Bytes())
		calldata, err := h.abi.Pack("submitCommitteeAttestation",
			new(big.Int).SetUint64(sourceChainID), uint64(oldEpoch), payloadHash, m.entry.PubkeyBLS, sig.Bytes(),
		)
		if err != nil {
			t.Fatalf("pack submitCommitteeAttestation: %v", err)
		}
		tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, nonce, big.NewInt(0), marshalCallData(t, calldata))
		nonce++
		if rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
			reason := ""
			if rcp != nil {
				reason = string(rcp.Return())
			}
			t.Fatalf("submitCommitteeAttestation failed: %q", reason)
		}
	}

	// --- getCommitteeAttestationShares must report exactly the 3 submitted shares. ---
	sharesViewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0),
		mustPackGetCommitteeAttestationShares(t, h, sourceChainID, oldEpoch, payloadHash))
	sharesData, err := h.HandleOffChainQuery(cs, sharesViewTx)
	if err != nil {
		t.Fatalf("getCommitteeAttestationShares failed: %v", err)
	}
	sharesOut, err := h.abi.Unpack("getCommitteeAttestationShares", sharesData)
	if err != nil {
		t.Fatalf("unpack getCommitteeAttestationShares: %v", err)
	}
	gotPubkeys, _ := sharesOut[0].([][]byte)
	gotSigs, _ := sharesOut[1].([][]byte)
	if len(gotPubkeys) != 3 || len(gotSigs) != 3 {
		t.Fatalf("expected 3 shares, got pubkeys=%d sigs=%d", len(gotPubkeys), len(gotSigs))
	}

	// --- Aggregate client-side (exactly like a real worker would) and submit committeeUpdate. ---
	aggSigBytes := make([][]byte, len(gotSigs))
	copy(aggSigBytes, gotSigs)
	aggSignature := bls.CreateAggregateSign(aggSigBytes)

	newPubkeys := make([][]byte, len(newCommittee))
	newStakes := make([]uint64, len(newCommittee))
	newPops := make([][]byte, len(newCommittee))
	for i, v := range newCommittee {
		newPubkeys[i] = v.PubkeyBLS
		newStakes[i] = v.Stake
		newPops[i] = v.PopSignature
	}

	calldata, err := h.abi.Pack("committeeUpdate",
		new(big.Int).SetUint64(sourceChainID), newEpoch,
		newPubkeys, newStakes, newPops,
		uint64(6667), stateRoot, accountTreeRoot, payloadHash,
		gotPubkeys, aggSignature,
	)
	if err != nil {
		t.Fatalf("pack committeeUpdate: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, nonce, big.NewInt(0), marshalCallData(t, calldata))
	nonce++
	if rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		reason := ""
		if rcp != nil {
			reason = string(rcp.Return())
		}
		t.Fatalf("committeeUpdate failed: %q", reason)
	}

	// --- ChainRegistry must now reflect the new committee/epoch, applied via the REAL
	// cross_chain.ApplyCommitteeUpdate verification path (not a caller-supplied bool). ---
	engineAfter, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine (after): %v", err)
	}
	updated, ok := engineAfter.ChainRegistry[sourceChainID]
	if !ok {
		t.Fatal("ChainRegistry entry disappeared after committeeUpdate")
	}
	if updated.Epoch != newEpoch {
		t.Fatalf("registry epoch = %d, want %d", updated.Epoch, newEpoch)
	}
	if len(updated.Committee) != len(newCommittee) {
		t.Fatalf("registry committee size = %d, want %d", len(updated.Committee), len(newCommittee))
	}
	if updated.StateRoot != stateRoot {
		t.Fatalf("registry stateRoot = %s, want %s", updated.StateRoot.Hex(), stateRoot.Hex())
	}
	if updated.AccountTreeRoot != accountTreeRoot {
		t.Fatalf("registry accountTreeRoot = %s, want %s", updated.AccountTreeRoot.Hex(), accountTreeRoot.Hex())
	}

	// --- Pending shares for this now-applied update must be cleared. ---
	key := committeeAttestationKey(sourceChainID, oldEpoch, payloadHash)
	if shares := engineAfter.PendingCommitteeAttestations[key]; len(shares) != 0 {
		t.Fatalf("expected PendingCommitteeAttestations to be cleared, got %d shares", len(shares))
	}

	// --- Idempotency: a second committeeUpdate attempt for the already-applied epoch must be
	// rejected (NonSequentialEpoch) — this is what makes "multiple validators race to submit
	// the final tx" safe (Milestone C plan doc, no single leader required). ---
	tx2 := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, nonce, big.NewInt(0), marshalCallData(t, calldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); !failed {
		t.Fatal("expected a second committeeUpdate for the same (now-old) epoch to fail")
	}
}

// TestGatewayHandler_CommitteeUpdate_InsufficientQuorumRejected proves the stake threshold is a
// real check, not decorative: 1 of 4 equal-stake signers (25%) must be rejected.
func TestGatewayHandler_CommitteeUpdate_InsufficientQuorumRejected(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	const sourceChainID = 202
	const oldEpoch = 1

	old := []committeeMember{
		newCommitteeMember(t, 1000),
		newCommitteeMember(t, 1000),
		newCommitteeMember(t, 1000),
		newCommitteeMember(t, 1000),
	}
	oldCommittee := make([]cross_chain.ValidatorEntry, len(old))
	for i, m := range old {
		oldCommittee[i] = m.entry
	}
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine (seed): %v", err)
	}
	engine.ChainRegistry[sourceChainID] = cross_chain.ChainRegistry{
		ChainID:         sourceChainID,
		Committee:       oldCommittee,
		Epoch:           oldEpoch,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine (seed): %v", err)
	}

	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")
	newEpoch := uint64(oldEpoch + 1)
	stateRoot := common.HexToHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	accountTreeRoot := common.HexToHash("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	payloadHash := cross_chain.ComputeCommitteeUpdateDigest(sourceChainID, newEpoch, oldCommittee, stateRoot, accountTreeRoot)

	// Only 1 of 4 signs — 25% stake, below the 2/3 threshold.
	sig := bls.Sign(old[0].kp.PrivateKey(), payloadHash.Bytes())
	newPubkeys := [][]byte{old[0].entry.PubkeyBLS}
	newStakes := []uint64{old[0].entry.Stake}
	newPops := [][]byte{old[0].entry.PopSignature}

	// Register PoP for the one signer only (not required for this test's rejection reason, but
	// keeps the failure specifically about quorum, not PoP).
	popCalldata, _ := h.abi.Pack("registerCommitteePop", old[0].entry.PubkeyBLS, old[0].entry.PopSignature)
	popTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, popCalldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, popTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); failed {
		t.Fatal("registerCommitteePop unexpectedly failed")
	}

	calldata, err := h.abi.Pack("committeeUpdate",
		new(big.Int).SetUint64(sourceChainID), newEpoch,
		newPubkeys, newStakes, newPops,
		uint64(6667), stateRoot, accountTreeRoot, payloadHash,
		[][]byte{old[0].entry.PubkeyBLS}, sig.Bytes(),
	)
	if err != nil {
		t.Fatalf("pack committeeUpdate: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, calldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); !failed {
		t.Fatal("expected committeeUpdate with only 25% stake to be rejected for insufficient quorum")
	}
}

// TestGatewayHandler_SubmitCommitteeAttestation_RejectsNonMember proves a signature from a key
// that isn't part of the chain's currently-registered committee is rejected outright, even if
// the BLS signature itself is cryptographically valid.
func TestGatewayHandler_SubmitCommitteeAttestation_RejectsNonMember(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	const sourceChainID = 303
	const oldEpoch = 2

	member := newCommitteeMember(t, 1000)
	engine, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine (seed): %v", err)
	}
	engine.ChainRegistry[sourceChainID] = cross_chain.ChainRegistry{
		ChainID:         sourceChainID,
		Committee:       []cross_chain.ValidatorEntry{member.entry},
		Epoch:           oldEpoch,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine (seed): %v", err)
	}

	outsider := newCommitteeMember(t, 1000) // never added to the committee
	sender := common.HexToAddress("0x3333333333333333333333333333333333333333")
	payloadHash := common.HexToHash("0xBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEFBEEF")
	sig := bls.Sign(outsider.kp.PrivateKey(), payloadHash.Bytes())

	calldata, err := h.abi.Pack("submitCommitteeAttestation",
		new(big.Int).SetUint64(sourceChainID), uint64(oldEpoch), payloadHash, outsider.entry.PubkeyBLS, sig.Bytes(),
	)
	if err != nil {
		t.Fatalf("pack submitCommitteeAttestation: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); !failed {
		t.Fatal("expected submitCommitteeAttestation from a non-member pubkey to be rejected")
	}
}

// TestGatewayHandler_RegisterCommitteePop_RejectsInvalidPop proves PopVerify is actually
// enforced, not decorative — a mismatched signature/pubkey pair must be rejected.
func TestGatewayHandler_RegisterCommitteePop_RejectsInvalidPop(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler() error: %v", err)
	}

	a := newCommitteeMember(t, 1000)
	b := newCommitteeMember(t, 1000)
	sender := common.HexToAddress("0x4444444444444444444444444444444444444444")

	// a's pubkey with b's PoP signature — must fail.
	calldata, err := h.abi.Pack("registerCommitteePop", a.entry.PubkeyBLS, b.entry.PopSignature)
	if err != nil {
		t.Fatalf("pack registerCommitteePop: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	if _, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0); !failed {
		t.Fatal("expected registerCommitteePop with a mismatched PoP to be rejected")
	}
}
