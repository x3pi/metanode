package cross_chain

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// P8 — CHAIN-DEATH RECOVERY TEST SUITE & DRILL EXERCISE (P8.1 / DoD T3.c)
// ══════════════════════════════════════════════════════════════════════════════

func setupChainDeathTestEnv() (*GatewayEngine, *GlobalSupplyLedger, map[uint64]ChainRegistry) {
	genesisTotalSupply := big.NewInt(10_000_000)
	initialAllocations := map[uint64]*big.Int{
		101: big.NewInt(5_000_000), // Victim Chain
		102: big.NewInt(3_000_000), // Safe Destination Chain
		991: big.NewInt(2_000_000), // Public Root Anchor / Reserve
	}
	supplyLedger, err := NewGlobalSupplyLedger(genesisTotalSupply, initialAllocations)
	if err != nil {
		panic(err)
	}

	chainRegistry := make(map[uint64]ChainRegistry)
	chainRegistry[101] = ChainRegistry{ChainID: 101, Epoch: 10, StateRoot: common.Hash{}}
	chainRegistry[102] = ChainRegistry{ChainID: 102, Epoch: 10, StateRoot: common.Hash{}}
	chainRegistry[991] = ChainRegistry{ChainID: 991, Epoch: 10, StateRoot: common.Hash{}}

	gateway := NewGatewayEngine(991, chainRegistry, supplyLedger)
	return gateway, supplyLedger, chainRegistry
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P8.1: Full Lifecycle Drill — Governance Declare-Dead -> Proof -> Claim
// ──────────────────────────────────────────────────────────────────────────────
func TestP8_1_CompleteChainDeathRecoveryLifecycle(t *testing.T) {
	gateway, supplyLedger, _ := setupChainDeathTestEnv()
	deadChainID := uint64(101)

	// 1. Setup 4 victim accounts on Chain 101
	alice := AccountLeaf{Account: common.HexToAddress("0x1111111111111111111111111111111111111111"), Balance: big.NewInt(1_500)}
	bob := AccountLeaf{Account: common.HexToAddress("0x2222222222222222222222222222222222222222"), Balance: big.NewInt(2_500)}
	charlie := AccountLeaf{Account: common.HexToAddress("0x3333333333333333333333333333333333333333"), Balance: big.NewInt(1_000)}
	dave := AccountLeaf{Account: common.HexToAddress("0x4444444444444444444444444444444444444444"), Balance: big.NewInt(500)}

	accounts := []AccountLeaf{alice, bob, charlie, dave}
	anchoredStateRoot, proofs, err := BuildAccountMerkleTree(accounts)
	require.NoError(t, err)

	// Root Anchor records the last verified state root and account tree snapshot
	realTrieStateRoot := common.HexToHash("0x9999888877776666555544443333222211110000aabbccddeeff001122334455")
	gateway.ChainRegistry[deadChainID] = ChainRegistry{
		ChainID:         deadChainID,
		Epoch:           15,
		StateRoot:       realTrieStateRoot,
		AccountTreeRoot: anchoredStateRoot,
	}

	// 2. Simulate Chain 101 Permanent Death. The production route that sets DeadChains is
	// SlashOnEquivocation (RecoveryCommittee/DeclareChainDeadWithCert were removed 2026-09-24,
	// see security_bond_test.go); here the flag is set directly to exercise the claim path.
	markChainDeadForTest(gateway, deadChainID)
	assert.True(t, gateway.DeadChains[deadChainID])

	// 3. Alice & Bob submit proofs and Claim their funds
	proofAlice := proofs[0]
	proofBob := proofs[1]
	leafAliceHash := HashAccountLeaf(alice)
	leafBobHash := HashAccountLeaf(bob)

	// Alice claims 1,500 MTN
	errAliceClaim := gateway.ClaimDeadChainBalance(deadChainID, alice.Account, alice.Balance, proofAlice, leafAliceHash)
	require.NoError(t, errAliceClaim, "Alice should successfully recover funds from dead chain")

	// Bob claims 2,500 MTN
	errBobClaim := gateway.ClaimDeadChainBalance(deadChainID, bob.Account, bob.Balance, proofBob, leafBobHash)
	require.NoError(t, errBobClaim, "Bob should successfully recover funds from dead chain")

	// 4. Verify Dead Chain Allocation is accurately deducted
	remainingAlloc := supplyLedger.GetAllocation(deadChainID)
	// Initial 5,000,000 - 1,500 - 2,500 = 4,996,000
	assert.Equal(t, big.NewInt(4_996_000), remainingAlloc)
	assert.True(t, supplyLedger.VerifyInvariant(), "Global supply invariant must remain perfectly preserved")
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P8.2: Adversarial Security & Error Handling Defenses
// ──────────────────────────────────────────────────────────────────────────────
func TestP8_2_AdversarialSecurityAndDoubleClaimDefenses(t *testing.T) {
	gateway, supplyLedger, _ := setupChainDeathTestEnv()
	deadChainID := uint64(101)

	alice := AccountLeaf{Account: common.HexToAddress("0x1111111111111111111111111111111111111111"), Balance: big.NewInt(1_500)}
	bob := AccountLeaf{Account: common.HexToAddress("0x2222222222222222222222222222222222222222"), Balance: big.NewInt(2_500)}

	accounts := []AccountLeaf{alice, bob}
	anchoredStateRoot, proofs, err := BuildAccountMerkleTree(accounts)
	require.NoError(t, err)

	gateway.ChainRegistry[deadChainID] = ChainRegistry{
		ChainID:         deadChainID,
		Epoch:           10,
		StateRoot:       common.HexToHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		AccountTreeRoot: anchoredStateRoot,
	}

	proofAlice := proofs[0]
	leafAliceHash := HashAccountLeaf(alice)
	leafBobHash := HashAccountLeaf(bob)

	// Adversarial Case 1: Claim before chain is declared Dead
	errNotDead := gateway.ClaimDeadChainBalance(deadChainID, alice.Account, alice.Balance, proofAlice, leafAliceHash)
	assert.ErrorIs(t, errNotDead, ErrChainNotDead, "Claiming must be rejected if chain is not declared dead")

	// Declare chain dead
	gateway.DeadChains[deadChainID] = true

	// Adversarial Case 2: Alice attempts to claim with a Tampered Fake Balance (1,500,000 MTN instead of 1,500)
	fakeAlice := AccountLeaf{Account: alice.Account, Balance: big.NewInt(1_500_000)}
	fakeLeafHash := HashAccountLeaf(fakeAlice)
	errTampered := gateway.ClaimDeadChainBalance(deadChainID, alice.Account, fakeAlice.Balance, proofAlice, fakeLeafHash)
	assert.ErrorIs(t, errTampered, ErrInvalidMerkleProof, "Tampered balance leaf must fail Merkle verification")

	// Valid claim
	errValid := gateway.ClaimDeadChainBalance(deadChainID, alice.Account, alice.Balance, proofAlice, leafAliceHash)
	require.NoError(t, errValid)

	// Adversarial Case 3: Double-Claim Attack (Alice submits the same proof again)
	errDoubleClaim := gateway.ClaimDeadChainBalance(deadChainID, alice.Account, alice.Balance, proofAlice, leafAliceHash)
	assert.ErrorIs(t, errDoubleClaim, ErrDeadChainAlreadyClaimed, "Double claim attack must be rejected")

	// Adversarial Case 4: Overdraw remaining allocation
	// Set dead chain allocation to only 1,000 MTN while Bob wants 2,500 MTN
	supplyLedger.PerChainAllocation[deadChainID] = big.NewInt(1_000)
	proofBob := proofs[1]
	errOverdraw := gateway.ClaimDeadChainBalance(deadChainID, bob.Account, bob.Balance, proofBob, leafBobHash)
	assert.ErrorIs(t, errOverdraw, ErrInsufficientAllocation, "Claim exceeding dead chain remaining allocation must revert")
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P8.3: Randomized Multi-Account Fuzz Drill (DoD Invariant Conservation)
// ──────────────────────────────────────────────────────────────────────────────
func TestP8_3_FuzzMultiAccountDeadChainRescue(t *testing.T) {
	gateway, supplyLedger, _ := setupChainDeathTestEnv()
	deadChainID := uint64(101)
	gateway.DeadChains[deadChainID] = true

	const numAccounts = 30
	accounts := make([]AccountLeaf, numAccounts)
	totalClaimExpected := new(big.Int)

	for i := 0; i < numAccounts; i++ {
		var addrBytes [20]byte
		rand.Read(addrBytes[:])
		addr := common.BytesToAddress(addrBytes[:])
		bal := big.NewInt(int64(100 + i*10)) // 100 to 390 MTN

		accounts[i] = AccountLeaf{Account: addr, Balance: bal}
		totalClaimExpected.Add(totalClaimExpected, bal)
	}

	stateRoot, proofs, err := BuildAccountMerkleTree(accounts)
	require.NoError(t, err)

	gateway.ChainRegistry[deadChainID] = ChainRegistry{
		ChainID:         deadChainID,
		StateRoot:       common.HexToHash("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
		AccountTreeRoot: stateRoot,
	}

	initialAlloc := new(big.Int).Set(supplyLedger.GetAllocation(deadChainID))

	// Claim all 30 accounts sequentially
	for i := 0; i < numAccounts; i++ {
		proof := proofs[i]
		leafHash := HashAccountLeaf(accounts[i])

		errClaim := gateway.ClaimDeadChainBalance(deadChainID, accounts[i].Account, accounts[i].Balance, proof, leafHash)
		require.NoError(t, errClaim)
	}

	// Verify exact allocation subtraction
	expectedRemaining := new(big.Int).Sub(initialAlloc, totalClaimExpected)
	actualRemaining := supplyLedger.GetAllocation(deadChainID)
	assert.Equal(t, expectedRemaining, actualRemaining)
	assert.True(t, supplyLedger.VerifyInvariant())
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P8.4: DeadChains actually stops new value movement (Quick Win #0)
// ──────────────────────────────────────────────────────────────────────────────

// TestP8_4_DeadChain_BlocksAttestCommitAndOutbound is the regression test for Quick Win #0
// (note/cross_chain/root_anchor_production_security_hardening_plan.md): before this fix,
// DeadChains[chainID]=true was only ever read by ClaimDeadChainBalance -- the forfeit path set the flag but nothing actually stopped a captured
// committee from continuing to attestCommit() and draining PerChainAllocation as normal, and
// nothing stopped a NEW Outbound() message being queued to a destination already known dead
// (locking the sender's own funds into a message no relayer could ever deliver). Both paths must
// now fail closed with ErrChainDeclaredDead the moment a chain is declared dead.
func TestP8_4_DeadChain_BlocksAttestCommitAndOutbound(t *testing.T) {
	gateway, _, _ := setupChainDeathTestEnv()
	deadChainID := uint64(101)
	gateway.ReserveChainID = 991 // gateway IS chain 991 here (setupChainDeathTestEnv) -- C8 gate

	kp := bls.GenerateKeyPair()
	pop := PopSign(kp.PrivateKey(), kp.PublicKey())
	gateway.ChainRegistry[deadChainID] = ChainRegistry{
		ChainID:   deadChainID,
		Epoch:     10,
		Committee: []ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 10000, PopSignature: pop.Bytes()}},
	}

	// Mark chain 101 dead -- same effect as TestP8_1 (see markChainDeadForTest).
	markChainDeadForTest(gateway, deadChainID)
	assert.True(t, gateway.DeadChains[deadChainID])

	// A "captured" committee for the now-dead chain 101 must NOT be able to attestCommit anymore --
	// before this fix, this call succeeded and happily debited PerChainAllocation[101].
	leaf := AggregateValueLeaf{AssetID: big.NewInt(0), AggregateAmount: big.NewInt(100)}
	root := HashAggregateValueLeaf(leaf)
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), root.Bytes()...)
	commitSig := bls.Sign(kp.PrivateKey(), commitMsg)
	attestCert := QuorumCert{Epoch: 10, AggregateSignature: commitSig.Bytes(), SignerBitmap: []byte{0x01}}
	_, errAttest := gateway.AttestCommit(deadChainID, root, big.NewInt(100), big.NewInt(0), MerkleProof{}, attestCert, 0)
	assert.ErrorIs(t, errAttest, ErrChainDeclaredDead, "attestCommit against a chain declared dead must be rejected")

	// A NEW outbound message TO the now-dead chain 101 must also be rejected -- before this fix,
	// the sender's funds would burn/lock successfully into a message no relayer could ever deliver.
	sender := common.HexToAddress("0x5555555555555555555555555555555555555555")
	target := common.HexToAddress("0x6666666666666666666666666666666666666666")
	params := OutboundParams{
		DestChainID: deadChainID, Target: target, Payload: []byte{},
		AssetID: big.NewInt(0), Value: big.NewInt(0), Tip: big.NewInt(0), GasFee: big.NewInt(0), HopCount: 0,
	}
	_, errOutbound := gateway.Outbound(sender, params, common.HexToHash("0x7777777777777777777777777777777777777777777777777777777777777777"))
	assert.ErrorIs(t, errOutbound, ErrChainDeclaredDead, "outbound() to a chain declared dead must be rejected")
}
