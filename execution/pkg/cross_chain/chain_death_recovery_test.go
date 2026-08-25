package cross_chain

import (
	"crypto/rand"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
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

	// 2. Simulate Chain 101 Permanent Death (Liveness Failure)
	// Execute Governance Proposal: DeclareDead(101)
	activeChains := []uint64{101, 102, 103, 991} // 4 active founding chains
	govEngine := NewGovernanceEngineWithTimelock(activeChains, 72*3600)

	proposalPayload := []byte{101}
	now := uint64(time.Now().Unix())

	proposalID, err := govEngine.Propose(ProposalDeclareChainDead, proposalPayload, now)
	require.NoError(t, err)

	// 3 out of 4 chains vote FOR (3/4 = 75% >= 66.7% Quorum)
	st1, _ := govEngine.Vote(proposalID, 102, now+10)
	st2, _ := govEngine.Vote(proposalID, 103, now+20)
	st3, _ := govEngine.Vote(proposalID, 991, now+30)
	assert.Equal(t, ProposalStatusActive, st1)
	assert.Equal(t, ProposalStatusActive, st2)
	assert.Equal(t, ProposalStatusTimelocked, st3, "Reached >= 2/3 threshold -> Timelocked status")

	// Timelock passes (+72h) -> Execute Declare Dead
	_, errExec := govEngine.Execute(proposalID, now+30+72*3600+1)
	require.NoError(t, errExec)
	assert.Equal(t, ProposalStatusExecuted, govEngine.ProposalStatus[proposalID])

	// Transition Gateway state
	gateway.DeadChains[deadChainID] = true
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
