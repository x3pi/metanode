package cross_chain

import (
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// SECURITY & ADVERSARIAL AUDIT TEST SUITE
// ══════════════════════════════════════════════════════════════════════════════

func setupSecurityAuditEnvironment() (*GatewayEngine, map[uint64]ChainRegistry, *GlobalSupplyLedger, *bls.KeyPair) {
	kp := bls.GenerateKeyPair()
	popSig := PopSign(kp.PrivateKey(), kp.PublicKey())

	registry := make(map[uint64]ChainRegistry)
	registry[1000] = ChainRegistry{
		ChainID: 1000,
		Committee: []ValidatorEntry{
			{
				PubkeyBLS:    kp.BytesPublicKey(),
				Stake:        10000,
				PopSignature: popSig.Bytes(),
			},
		},
		Epoch:           1,
		QuorumThreshold: 6667,
		StateRoot:       common.HexToHash("0x1000100010001000100010001000100010001000100010001000100010001000"),
	}
	registry[101] = ChainRegistry{
		ChainID: 101,
		Committee: []ValidatorEntry{
			{
				PubkeyBLS:    kp.BytesPublicKey(),
				Stake:        10000,
				PopSignature: popSig.Bytes(),
			},
		},
		Epoch:           1,
		QuorumThreshold: 6667,
		StateRoot:       common.HexToHash("0x1010101010101010101010101010101010101010101010101010101010101010"),
	}
	registry[102] = ChainRegistry{
		ChainID: 102,
		Committee: []ValidatorEntry{
			{
				PubkeyBLS:    kp.BytesPublicKey(),
				Stake:        10000,
				PopSignature: popSig.Bytes(),
			},
		},
		Epoch:           1,
		QuorumThreshold: 6667,
		StateRoot:       common.HexToHash("0x1020102010201020102010201020102010201020102010201020102010201020"),
	}

	allocs := map[uint64]*big.Int{
		1000: big.NewInt(100_000),
		101:  big.NewInt(5_000),
		102:  big.NewInt(5_000),
	}
	ledger, err := NewGlobalSupplyLedger(big.NewInt(110_000), allocs)
	if err != nil {
		panic(err)
	}

	engine := NewGatewayEngine(102, registry, ledger)
	// C8 fix (2026-08-27): this suite exercises AttestCommit's ceiling enforcement directly
	// (engine, as chain 102, attesting other chains' commits) -- under the fix, only a chain
	// configured as its own Reserve may do that for a nonzero-value commit. Every scenario
	// here is conceptually "the Reserve attesting a source chain's commit" (Section 2.3 step
	// 2), so engine plays that role for these tests.
	engine.ReserveChainID = 102
	return engine, registry, ledger, kp
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 1: BLS Verification & Quorum Certificate Integrity
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_BLSQuorumCertAndRogueKeyDefense(t *testing.T) {
	engine, _, _, kp := setupSecurityAuditEnvironment()

	commitRoot := common.HexToHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	// 1. Invalid / Empty BLS Signature -> MUST FAIL-CLOSED (fails before the Merkle proof check is
	// ever reached, so commitRoot's value doesn't matter here).
	certInvalid := QuorumCert{
		Epoch:              1,
		AggregateSignature: make([]byte, 48), // Empty signature
		SignerBitmap:       []byte{0xFF},
	}
	_, errInvalidBLS := engine.AttestCommit(101, commitRoot, big.NewInt(100), big.NewInt(0), MerkleProof{}, certInvalid)
	assert.Error(t, errInvalidBLS, "Empty/invalid BLS signature must fail-closed")

	// 2. Valid Real BLS Signature -> MUST PASS. commitRoot is now the declared amount's own
	// AggregateValueLeaf hash (proof = no siblings) — a real Merkle-proof binding (Section 2.3.1).
	commitRootValid := HashAggregateValueLeaf(AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(100)})
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRootValid.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	certValid := QuorumCert{
		Epoch:              1,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0xFF},
	}
	attested, errValidBLS := engine.AttestCommit(101, commitRootValid, big.NewInt(100), big.NewInt(0), MerkleProof{}, certValid)
	require.NoError(t, errValidBLS)
	assert.Equal(t, big.NewInt(100), attested.FundedAmount)

	// 3. PoP Proof-of-Possession & Rogue Key Defense
	kp1 := bls.GenerateKeyPair()
	pub1 := kp1.PublicKey()
	pri1 := kp1.PrivateKey()
	popSig1 := PopSign(pri1, pub1)

	valid, err := PopVerify(pub1.Bytes(), popSig1.Bytes())
	require.NoError(t, err)
	assert.True(t, valid)

	legitEntry := ValidatorEntry{
		PubkeyBLS:    pub1.Bytes(),
		Stake:        2500,
		PopSignature: popSig1.Bytes(),
	}
	require.NoError(t, ValidateCommitteeEntry(legitEntry))

	// Rogue-key attacker creates linearly dependent key without PoP knowledge
	rogueEntry := ValidatorEntry{
		PubkeyBLS:    pub1.Bytes(), // Copy victim's pubkey without knowing privkey
		Stake:        1000,
		PopSignature: make([]byte, 96), // Bogus PoP signature
	}
	errRogue := ValidateCommitteeEntry(rogueEntry)
	assert.ErrorIs(t, errRogue, ErrPopVerifyFailed, "Rogue key attack must be blocked by PopVerify")
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 2: Merkle Proof Integrity & Tamper-Resistance (Bit-Flip Attacks)
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_MerkleProofTamperResistance(t *testing.T) {
	leaves := []common.Hash{
		common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
		common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444"),
	}

	root, layers := BuildMerkleTree(leaves)
	proof := GetMerkleProof(layers, 0)

	// 1. Legitimate proof must verify
	assert.True(t, VerifyMerkleProof(leaves[0], proof, root), "Valid Merkle proof must pass")

	// 2. Tampered Leaf (Bit-Flip Attack) -> MUST FAIL
	tamperedLeaf := leaves[0]
	tamperedLeaf[0] ^= 0xFF
	assert.False(t, VerifyMerkleProof(tamperedLeaf, proof, root), "Tampered leaf must fail verification")

	// 3. Tampered Sibling in Proof -> MUST FAIL
	tamperedProof := MerkleProof{
		Siblings: []common.Hash{common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999"), layers[1][1]},
	}
	assert.False(t, VerifyMerkleProof(leaves[0], tamperedProof, root), "Tampered sibling must fail verification")

	// 4. Swapped Siblings Order -> MUST FAIL
	swappedProof := MerkleProof{
		Siblings: []common.Hash{layers[1][1], layers[0][1]},
	}
	assert.False(t, VerifyMerkleProof(leaves[0], swappedProof, root), "Swapped siblings must fail verification")
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 3: Anti-Replay & Concurrent Double-Claim Race Resistance
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_AntiReplayAndConcurrentDoubleClaim(t *testing.T) {
	engine, _, _, kp := setupSecurityAuditEnvironment()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x9999999999999999999999999999999999999999")
	txHash := common.HexToHash("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	params := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte("audit-test"),
		Value:       big.NewInt(500),
		Tip:         big.NewInt(10),
		HopCount:    1,
	}

	msg, err := engine.Outbound(sender, params, txHash)
	require.NoError(t, err)

	commitRoot, layers, aggAmounts, aggIndex, errTree := BuildCommitTree([]CrossChainMessage{*msg})
	require.NoError(t, errTree)
	proof := GetMerkleProof(layers, 0)
	aggregateProof := GetMerkleProof(layers, aggIndex["0"])

	// Attest on Root Anchor / Gateway with real BLS signature
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{
		Epoch:              1,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0xFF},
	}
	_, err = engine.AttestCommit(102, commitRoot, aggAmounts["0"], big.NewInt(0), aggregateProof, cert)
	require.NoError(t, err)

	// Stress Test: 50 concurrent workers try to claim the EXACT SAME message simultaneously
	concurrency := 50
	var wg sync.WaitGroup
	var successCount int32
	var rejectedCount int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := engine.ClaimMessage(*msg, proof, commitRoot, relayer)
			if err == nil && status == MessageStatusSuccess {
				atomic.AddInt32(&successCount, 1)
			} else if err != nil {
				atomic.AddInt32(&rejectedCount, 1)
			}
		}()
	}
	wg.Wait()

	// Invariant: Exactly 1 transaction must succeed, all other 49 must be rejected
	assert.Equal(t, int32(1), successCount, "Exactly ONE claim must succeed")
	assert.Equal(t, int32(concurrency-1), rejectedCount, "All other concurrent attempts must be rejected")
	assert.Equal(t, MessageStatusSuccess, engine.GetMessageStatus(msg.MessageID))
	assert.Equal(t, big.NewInt(10), engine.RelayerBalances[relayer], "Tip must be credited only once")
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 4: Anti-Double-Mint via Refund Pathway Race Guard
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_AntiDoubleMintViaRefundRaceGuard(t *testing.T) {
	engine, _, _, kp := setupSecurityAuditEnvironment()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x9999999999999999999999999999999999999999")
	txHash := common.HexToHash("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")

	params := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte("refund-audit"),
		Value:       big.NewInt(300),
		Tip:         big.NewInt(5),
		HopCount:    1,
	}

	msg, err := engine.Outbound(sender, params, txHash)
	require.NoError(t, err)

	commitRoot, layers, aggAmounts, aggIndex, errTree := BuildCommitTree([]CrossChainMessage{*msg})
	require.NoError(t, errTree)
	proof := GetMerkleProof(layers, 0)
	aggregateProof := GetMerkleProof(layers, aggIndex["0"])

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{
		Epoch:              1,
		AggregateSignature: sig.Bytes(),
		SignerBitmap:       []byte{0xFF},
	}
	_, err = engine.AttestCommit(102, commitRoot, aggAmounts["0"], big.NewInt(0), aggregateProof, cert)
	require.NoError(t, err)

	// Step 1: Claim message successfully
	status, err := engine.ClaimMessage(*msg, proof, commitRoot, relayer)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusSuccess, status)

	// Create valid destination failure cert
	failMsg := ComputeMessageFailureAttestMessage(msg.MessageID, 102)
	failSig := bls.Sign(kp.PrivateKey(), failMsg)
	destFailureCert := QuorumCert{
		Epoch:              1,
		AggregateSignature: failSig.Bytes(),
		SignerBitmap:       []byte{0xFF},
	}

	// Step 2: Attacker tries to submit a Refund for the same claimed message (Double-Mint Attack)
	errRefundAttack := engine.Refund(*msg, proof, commitRoot, destFailureCert)
	assert.ErrorIs(t, errRefundAttack, ErrInvalidRefundState, "Refund on already claimed message must be blocked")

	// Step 3: Attacker tries to mint free allocation via forged unused messageID
	forgedMsg := *msg
	forgedMsg.MessageID = common.HexToHash("0xDEADC001DEADC001DEADC001DEADC001DEADC001DEADC001DEADC001DEADC001")
	errForgedMsg := engine.Refund(forgedMsg, proof, commitRoot, destFailureCert)
	assert.Error(t, errForgedMsg, "Refund on forged/uncommitted message must fail closed")

	// Create a second, un-claimed message to test Merkle proof and BLS verification specifically
	txHash2 := common.HexToHash("0xDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD")
	msgPending, err := engine.Outbound(sender, params, txHash2)
	require.NoError(t, err)

	commitRoot2, layers2, aggAmounts2, aggIndex2, errTree2 := BuildCommitTree([]CrossChainMessage{*msgPending})
	require.NoError(t, errTree2)
	proof2 := GetMerkleProof(layers2, 0)
	aggregateProof2 := GetMerkleProof(layers2, aggIndex2["0"])

	commitMsg2 := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot2.Bytes()...)
	sig2 := bls.Sign(kp.PrivateKey(), commitMsg2)
	cert2 := QuorumCert{
		Epoch:              1,
		AggregateSignature: sig2.Bytes(),
		SignerBitmap:       []byte{0xFF},
	}
	_, err = engine.AttestCommit(102, commitRoot2, aggAmounts2["0"], big.NewInt(0), aggregateProof2, cert2)
	require.NoError(t, err)

	failMsg2 := ComputeMessageFailureAttestMessage(msgPending.MessageID, 102)
	failSig2 := bls.Sign(kp.PrivateKey(), failMsg2)
	destFailureCert2 := QuorumCert{
		Epoch:              1,
		AggregateSignature: failSig2.Bytes(),
		SignerBitmap:       []byte{0xFF},
	}

	// Step 4: Attacker tries to inflate refund amount -> Merkle proof fails
	forgedAmountMsg := *msgPending
	forgedAmountMsg.Value = big.NewInt(999999999)
	errForgedAmount := engine.Refund(forgedAmountMsg, proof2, commitRoot2, destFailureCert2)
	assert.ErrorIs(t, errForgedAmount, ErrInvalidMerkleProof, "Refund with forged amount must fail Merkle check")

	// Step 5: Attacker provides fake failure cert -> BLS verification fails
	fakeCert := destFailureCert2
	fakeCert.AggregateSignature = bls.Sign(bls.GenerateKeyPair().PrivateKey(), failMsg2).Bytes()
	errInvalidProof := engine.Refund(*msgPending, proof2, commitRoot2, fakeCert)
	assert.ErrorIs(t, errInvalidProof, ErrInvalidRefundProof, "Refund with invalid failure cert must be rejected")

	// Step 6: Valid refund on pending message -> SUCCESS
	errValidRefund := engine.Refund(*msgPending, proof2, commitRoot2, destFailureCert2)
	require.NoError(t, errValidRefund)
	assert.Equal(t, MessageStatusRefunded, engine.GetMessageStatus(msgPending.MessageID))
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 5: Origin-Sender Context Security (Mục 2.6.4 điểm 2)
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_OriginSenderContextIntegrity(t *testing.T) {
	engine, _, _, _ := setupSecurityAuditEnvironment()

	// 1. Direct call outside Gateway -> ActiveContext must be nil
	assert.Nil(t, engine.ActiveContext, "ActiveContext must be nil when not called by Gateway")

	// 2. Caller verifies Gateway authorization
	assert.False(t, engine.IsCalledByGateway(), "isCalledByGateway must return false when no active context")

	// 3. Forged context without IsGateway flag -> Authorization rejected
	engine.ActiveContext = &CrossChainContext{
		OriginalSender: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		SourceChainID:  101,
		IsGateway:      false, // Forged / unauthorized
	}
	assert.False(t, engine.IsCalledByGateway(), "isCalledByGateway must reject unauthorized context")

	// 4. Authorized Gateway context -> Verified
	engine.ActiveContext.IsGateway = true
	assert.True(t, engine.IsCalledByGateway(), "isCalledByGateway must accept legitimate Gateway execution context")

	// Reset
	engine.ActiveContext = nil
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 6: Hop-Count Strict Boundary Enforcement (Mục 2.6.2)
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_HopCountBoundaryEnforcement(t *testing.T) {
	engine, _, _, _ := setupSecurityAuditEnvironment()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Boundary Tests: 0, 1, 5, 6 (MUST PASS)
	validHops := []uint8{0, 1, 5, 6}
	for _, hop := range validHops {
		params := OutboundParams{
			DestChainID: 102,
			Target:      target,
			HopCount:    hop,
		}
		msg, err := engine.Outbound(sender, params, common.HexToHash("0x11"))
		require.NoError(t, err, "Hop count %d should be valid", hop)
		assert.Equal(t, hop, msg.HopCount)
	}

	// Boundary Tests: 7, 8, 255 (MUST FAIL)
	invalidHops := []uint8{7, 8, 255}
	for _, hop := range invalidHops {
		params := OutboundParams{
			DestChainID: 102,
			Target:      target,
			HopCount:    hop,
		}
		_, err := engine.Outbound(sender, params, common.HexToHash("0x22"))
		assert.ErrorIs(t, err, ErrHopCountExceeded, "Hop count %d must be rejected", hop)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 7: Adversarial Overdraw & Ceiling Enforcement (Scenario 10.7)
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_AdversarialOverdrawAndSupplyCeiling(t *testing.T) {
	engine, _, ledger, kp := setupSecurityAuditEnvironment()

	// Initial Allocation: Chain 101 has 5,000 MTN. Each declared amount now needs its own
	// commitRoot (its own AggregateValueLeaf hash, Section 2.3.1) and its own matching signature —
	// a different amount is cryptographically a different commit, it can't reuse one fixed root.
	signFor := func(amount *big.Int) (common.Hash, QuorumCert) {
		leaf := AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: amount}
		root := HashAggregateValueLeaf(leaf)
		commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), root.Bytes()...)
		sig := bls.Sign(kp.PrivateKey(), commitMsg)
		return root, QuorumCert{Epoch: 1, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0xFF}}
	}

	// Attack 1: Overdraw attempt 10,000,000 MTN -> BLOCKED
	rootOverdraw, certOverdraw := signFor(big.NewInt(10_000_000))
	_, errOverdraw := engine.AttestCommit(101, rootOverdraw, big.NewInt(10_000_000), big.NewInt(0), MerkleProof{}, certOverdraw)
	assert.ErrorIs(t, errOverdraw, ErrAllocationExceeded, "Overdraw attempt must be blocked")

	// Attack 2: Exact boundary + 1 wei -> BLOCKED
	rootPlus1, certPlus1 := signFor(big.NewInt(5_001))
	_, errBoundaryPlus1 := engine.AttestCommit(101, rootPlus1, big.NewInt(5_001), big.NewInt(0), MerkleProof{}, certPlus1)
	assert.ErrorIs(t, errBoundaryPlus1, ErrAllocationExceeded, "Allocation + 1 wei must be blocked")

	// Valid 1: Exact allocation 5,000 MTN -> PASS
	rootExact, certExactCert := signFor(big.NewInt(5_000))
	attested, errExact := engine.AttestCommit(101, rootExact, big.NewInt(5_000), big.NewInt(0), MerkleProof{}, certExactCert)
	require.NoError(t, errExact)
	assert.Equal(t, big.NewInt(5_000), attested.FundedAmount)
	assert.Zero(t, ledger.PerChainAllocation[101].Sign())

	// Attack 3: Subsequent request when allocation is 0 -> BLOCKED
	rootExhausted, certExhausted := signFor(big.NewInt(1))
	_, errExhausted := engine.AttestCommit(101, rootExhausted, big.NewInt(1), big.NewInt(0), MerkleProof{}, certExhausted)
	assert.ErrorIs(t, errExhausted, ErrAllocationExceeded, "Exhausted allocation must be blocked")
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 8: Fail-Closed Epoch Alignment Check (Mục 5.3)
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_FailClosedEpochAlignment(t *testing.T) {
	engine, registry, _, kp := setupSecurityAuditEnvironment()
	assert.Equal(t, uint64(1), registry[101].Epoch)

	commitRoot := common.HexToHash("0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	// Attack 1: Old Epoch Cert (Epoch = 0) -> BLOCKED
	oldCert := QuorumCert{
		Epoch:              0,
		AggregateSignature: sig.Bytes(),
	}
	_, errOldEpoch := engine.AttestCommit(101, commitRoot, big.NewInt(100), big.NewInt(0), MerkleProof{}, oldCert)
	assert.ErrorIs(t, errOldEpoch, ErrEpochMismatch, "Old epoch cert must be rejected")

	// Attack 2: Future Epoch Cert (Epoch = 2) -> BLOCKED
	futureCert := QuorumCert{
		Epoch:              2,
		AggregateSignature: sig.Bytes(),
	}
	_, errFutureEpoch := engine.AttestCommit(101, commitRoot, big.NewInt(100), big.NewInt(0), MerkleProof{}, futureCert)
	assert.ErrorIs(t, errFutureEpoch, ErrEpochMismatch, "Future epoch cert must be rejected")

	// Attack 3: Unknown Chain ID (Chain = 999) -> BLOCKED
	unknownCert := QuorumCert{
		Epoch:              1,
		AggregateSignature: sig.Bytes(),
	}
	_, errUnknownChain := engine.AttestCommit(999, commitRoot, big.NewInt(100), big.NewInt(0), MerkleProof{}, unknownCert)
	assert.ErrorIs(t, errUnknownChain, ErrUnknownSourceChain, "Unknown source chain must be rejected")
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 9: Zero-Fork Invariant & Destination Offline Stability (Scenario 10.4)
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_ZeroForkDestinationOfflineStability(t *testing.T) {
	engine, _, _, _ := setupSecurityAuditEnvironment()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	txHash := common.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777")

	params := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Value:       big.NewInt(100),
		Tip:         big.NewInt(2),
		HopCount:    1,
	}

	// 1. Message is submitted while destination chain is unreachable
	msg, err := engine.Outbound(sender, params, txHash)
	require.NoError(t, err)

	// 2. Zero-Fork Invariant (Part 2.5): Must remain PENDING without timeout-based force dispatch
	assert.Equal(t, MessageStatusPending, engine.GetMessageStatus(msg.MessageID))

	// 3. No arbitrary state corruption or balance minting can happen without QuorumCert
	unattestedCommit := common.HexToHash("0x8888888888888888888888888888888888888888888888888888888888888888")
	proof := MerkleProof{Siblings: []common.Hash{}}
	status, errUnattested := engine.ClaimMessage(*msg, proof, unattestedCommit, sender)
	assert.ErrorIs(t, errUnattested, ErrCommitNotAttested)
	assert.Equal(t, MessageStatusPending, status)
	assert.Equal(t, MessageStatusPending, engine.GetMessageStatus(msg.MessageID))
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 10: Only the configured Reserve chain may attest a nonzero-value commit from
// another chain (C8, note/cross_chain_attack_scenario_catalog.md). Before this fix,
// GlobalSupplyLedger being per-chain-LOCAL state meant ANY chain could independently call
// AttestCommit for ANY other chain's commit -- "route value through Reserve" was enforced only
// by the relayer's own convention (relayer.go's DaemonConfig.ReserveChainID), never by the
// GatewayEngine itself, so nothing on-chain stopped a non-Reserve chain from accepting a direct,
// non-Reserve-routed attestation for real value.
// ──────────────────────────────────────────────────────────────────────────────
func TestAudit_OnlyReserveMayAttestNonzeroValueCommit(t *testing.T) {
	engine, _, _, kp := setupSecurityAuditEnvironment()

	signFor := func(amount *big.Int) (common.Hash, QuorumCert) {
		leaf := AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: amount}
		root := HashAggregateValueLeaf(leaf)
		commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), root.Bytes()...)
		sig := bls.Sign(kp.PrivateKey(), commitMsg)
		return root, QuorumCert{Epoch: 1, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0xFF}}
	}

	// 1. ReserveChainID unconfigured (zero value) -> fails closed, not open, even for a
	// legitimate-looking BLS-signed commit.
	engine.ReserveChainID = 0
	root, cert := signFor(big.NewInt(100))
	_, errUnconfigured := engine.AttestCommit(101, root, big.NewInt(100), big.NewInt(0), MerkleProof{}, cert)
	assert.ErrorIs(t, errUnconfigured, ErrReserveChainNotConfigured)

	// 2. ReserveChainID configured, but points to a DIFFERENT chain than the one currently
	// attesting -- this chain (102) is not Reserve, so it may not perform this attestation even
	// though it knows who Reserve is.
	engine.ReserveChainID = 1000 // some other chain is Reserve, not engine's own LocalChainID (102)
	_, errNotReserve := engine.AttestCommit(101, root, big.NewInt(100), big.NewInt(0), MerkleProof{}, cert)
	assert.ErrorIs(t, errNotReserve, ErrNonReserveCeilingAttestation)

	// 3. Zero-value commits (message type (a), Section 2.2 -- pure contract calls with no value)
	// are exempt: they never touch SupplyLedger's ceiling regardless (Sub(x, 0) is a no-op), so
	// direct A->B messaging without Reserve is unaffected by this fix.
	zeroRoot, zeroCert := signFor(big.NewInt(0))
	attestedZero, errZero := engine.AttestCommit(101, zeroRoot, big.NewInt(0), big.NewInt(0), MerkleProof{}, zeroCert)
	require.NoError(t, errZero, "zero-value commits must remain attestable by any chain, not just Reserve")
	assert.Equal(t, big.NewInt(0), attestedZero.FundedAmount)

	// 4. Once engine is correctly configured AS Reserve, the identical nonzero-value attestation
	// succeeds normally.
	engine.ReserveChainID = 102 // engine's own LocalChainID
	attested, errOk := engine.AttestCommit(101, root, big.NewInt(100), big.NewInt(0), MerkleProof{}, cert)
	require.NoError(t, errOk)
	assert.Equal(t, big.NewInt(100), attested.FundedAmount)
}

// ──────────────────────────────────────────────────────────────────────────────
// AUDIT TEST 11: FinalizeFailedAfterExecutionRevert correctly reverses ClaimMessage's provisional
// Success side-effects (2026-09-05 fix, security_audit_findings.md finding #1 / mục 2.4 point 1).
// ClaimMessage decides Success purely from proof/cert verification, BEFORE the caller
// (gateway_handler.go) ever attempts the real destination payload execution -- when that execution
// later reverts for a genuine business-logic reason, the message must finalize as Failed with its
// provisional ceiling/tip credits reversed, not silently stay a phantom Success nor permanently
// consume ceiling capacity for a delivery that never happened.
// ──────────────────────────────────────────────────────────────────────────────
func TestGatewayEngine_FinalizeFailedAfterExecutionRevert_ReversesProvisionalCredits(t *testing.T) {
	engine, _, ledger, kp := setupSecurityAuditEnvironment()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x9999999999999999999999999999999999999999")
	txHash := common.HexToHash("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE")

	params := OutboundParams{
		DestChainID: 102,
		Target:      target,
		Payload:     []byte("finalize-failed-audit"),
		Value:       big.NewInt(200),
		Tip:         big.NewInt(7),
		HopCount:    1,
	}
	msg, err := engine.Outbound(sender, params, txHash)
	require.NoError(t, err)

	commitRoot, layers, aggAmounts, aggIndex, errTree := BuildCommitTree([]CrossChainMessage{*msg})
	require.NoError(t, errTree)
	proof := GetMerkleProof(layers, 0)
	aggregateProof := GetMerkleProof(layers, aggIndex["0"])

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	cert := QuorumCert{Epoch: 1, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0xFF}}
	_, err = engine.AttestCommit(msg.SourceChainID, commitRoot, aggAmounts["0"], big.NewInt(0), aggregateProof, cert)
	require.NoError(t, err)

	allocBefore := new(big.Int).Set(ledger.GetAllocation(102))

	status, err := engine.ClaimMessage(*msg, proof, commitRoot, relayer)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusSuccess, status)
	assert.Equal(t, big.NewInt(7), engine.RelayerBalances[relayer], "tip must be provisionally credited by ClaimMessage")
	assert.True(t, ledger.GetAllocation(102).Cmp(allocBefore) > 0, "ceiling allocation must be provisionally credited by ClaimMessage")

	key := fmt.Sprintf("%d:%s:0", msg.SourceChainID, commitRoot.Hex())
	attestedBefore := engine.AttestedCommits[key]
	assert.Equal(t, big.NewInt(200), attestedBefore.ClaimedAmount, "ClaimedAmount must be provisionally incremented by ClaimMessage")

	// Now simulate the destination payload execution reverting -- finalize Failed instead of the
	// hard-revert-the-whole-transaction behavior this fix replaces.
	require.NoError(t, engine.FinalizeFailedAfterExecutionRevert(*msg, commitRoot, relayer))

	assert.Equal(t, MessageStatusFailed, engine.GetMessageStatus(msg.MessageID), "message must be terminally Failed")
	// SECURITY FIX (2026-09-05, finding #8, "Double Refund of Tip and GasFee on Reverted
	// Executions"): the relayer's Tip credit is NO LONGER reversed here -- the relayer genuinely
	// did relay this message (that's why claimMessage ran and reverted at all), so clawing back
	// their Tip both griefed them and, combined with the OLD refund() path also restoring Tip on
	// the source chain, caused a real double-refund/inflation bug. The relayer keeps it.
	assert.Equal(t, big.NewInt(7), engine.RelayerBalances[relayer], "relayer keeps the Tip they earned even though the payload later reverted")
	assert.Zero(t, ledger.GetAllocation(102).Cmp(allocBefore), "ceiling allocation credit must be fully reversed")
	attestedAfter := engine.AttestedCommits[key]
	assert.Zero(t, attestedAfter.ClaimedAmount.Sign(), "ClaimedAmount must be reversed back to 0 -- a failed message must not permanently shrink the commit's ceiling for other messages sharing it")

	// Terminal: neither a second finalize call nor a retry claim may be silently reapplied.
	errSecondFinalize := engine.FinalizeFailedAfterExecutionRevert(*msg, commitRoot, relayer)
	assert.Error(t, errSecondFinalize, "must reject finalizing an already-Failed message a second time")

	_, errRetryClaim := engine.ClaimMessage(*msg, proof, commitRoot, relayer)
	assert.ErrorIs(t, errRetryClaim, ErrAlreadyClaimed, "a Failed message must be terminal -- no retry claim allowed")
}
