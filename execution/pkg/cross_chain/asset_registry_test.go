package cross_chain

import (
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// P6 — ASSET REGISTRY & MULTI-ASSET CROSS-CHAIN PROTOCOL TEST SUITE (P6 DoD)
// ══════════════════════════════════════════════════════════════════════════════

func setupTestAssetRegistry() (*AssetRegistryEngine, *GovernanceEngine, map[uint64]ChainRegistry) {
	chainRegistry := make(map[uint64]ChainRegistry)
	activeChains := []uint64{101, 102, 103, 104}
	for _, cid := range activeChains {
		chainRegistry[cid] = ChainRegistry{
			ChainID:          cid,
			Epoch:            1,
			QuorumThreshold:  6667,
			GatewayContract:  common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
			StateRoot:        common.HexToHash("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
			ArchivalEndpoint: "http://archive.metanode.test",
			RegisteredAt:     1000,
		}
	}

	gov := NewGovernanceEngineWithTimelock(activeChains, 72*3600)
	engine := NewAssetRegistryEngine(chainRegistry, gov)
	return engine, gov, chainRegistry
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P6.1: AssetEntry Registration & Governance Security (DoD)
// ──────────────────────────────────────────────────────────────────────────────
func TestP6_1_AssetRegistrationAndGovernanceSecurity(t *testing.T) {
	engine, gov, _ := setupTestAssetRegistry()

	assetID := big.NewInt(888)
	canonicalContract := common.HexToAddress("0x1111222233334444555566667777888899990000")
	wrapped102 := common.HexToAddress("0x2222333344445555666677778888999900001111")
	wrapped103 := common.HexToAddress("0x3333444455556666777788889999000011112222")

	assetPayload := AssetEntry{
		AssetID:           assetID,
		HomeChainID:       101,
		CanonicalContract: canonicalContract,
		WrappedContracts: map[uint64]common.Address{
			102: wrapped102,
			103: wrapped103,
		},
	}
	payloadBytes, _ := json.Marshal(assetPayload)

	// Step 1: Unauthorized registration without Governance -> MUST FAIL
	unauthProposal := &GovernanceProposal{
		Kind:     ProposalRegisterAsset,
		Payload:  payloadBytes,
		Executed: false, // Not approved / not executed
	}
	_, errUnauth := engine.RegisterAssetOnRootAnchor(unauthProposal, big.NewInt(1_000_000))
	assert.ErrorIs(t, errUnauth, ErrUnauthorizedRegistration, "Direct unapproved asset registration must be blocked")

	// Step 2: Propose on Governance
	now := uint64(time.Now().Unix())
	propID, err := gov.Propose(ProposalRegisterAsset, payloadBytes, now)
	require.NoError(t, err)

	// Step 3: Vote (Need >= 3 of 4 active chains for 2/3 quorum)
	_, err = gov.Vote(propID, 101, now)
	require.NoError(t, err)
	_, err = gov.Vote(propID, 102, now)
	require.NoError(t, err)
	status, err := gov.Vote(propID, 103, now)
	require.NoError(t, err)
	assert.Equal(t, ProposalStatusTimelocked, status)

	// Step 4: Execute before 72h timelock -> MUST FAIL
	_, errEarly := gov.Execute(propID, now+3600)
	assert.ErrorIs(t, errEarly, ErrTimelockNotExpired)

	// Step 5: Execute after 72h timelock -> MUST SUCCEED
	executedProp, err := gov.Execute(propID, now+72*3600+1)
	require.NoError(t, err)
	require.True(t, executedProp.Executed)

	// Step 6: Register asset on Root Anchor
	registeredEntry, err := engine.RegisterAssetOnRootAnchor(executedProp, big.NewInt(1_000_000))
	require.NoError(t, err)
	assert.Equal(t, assetID, registeredEntry.AssetID)
	assert.True(t, registeredEntry.Active)

	// Step 7: Fraud attempt - Chain 104 tries to claim an unowned home chain -> BLOCKED
	fraudPayload := AssetEntry{
		AssetID:           big.NewInt(999),
		HomeChainID:       999, // Unregistered chain
		CanonicalContract: canonicalContract,
	}
	fraudBytes, _ := json.Marshal(fraudPayload)
	fraudProp := &GovernanceProposal{
		Kind:     ProposalRegisterAsset,
		Payload:  fraudBytes,
		Executed: true,
	}
	_, errFraud := engine.RegisterAssetOnRootAnchor(fraudProp, big.NewInt(500_000))
	assert.ErrorIs(t, errFraud, ErrInvalidHomeChain, "Registration claiming unregistered home chain must be rejected")
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P6.2: Multi-Asset Lock & Mint Lifecycle & Exact Conservation (DoD)
// ──────────────────────────────────────────────────────────────────────────────
func TestP6_2_MultiAssetLockMintLifecycleAndExactConservation(t *testing.T) {
	engine, _, _ := setupTestAssetRegistry()

	assetID := big.NewInt(777)
	totalSupply := big.NewInt(1_000_000)

	entry := AssetEntry{
		AssetID:           assetID,
		HomeChainID:       101,
		CanonicalContract: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		WrappedContracts: map[uint64]common.Address{
			102: common.HexToAddress("0x2222222222222222222222222222222222222222"),
			103: common.HexToAddress("0x3333333333333333333333333333333333333333"),
		},
	}
	entryBytes, _ := json.Marshal(entry)
	prop := &GovernanceProposal{
		Kind:     ProposalRegisterAsset,
		Payload:  entryBytes,
		Executed: true,
	}
	_, err := engine.RegisterAssetOnRootAnchor(prop, totalSupply)
	require.NoError(t, err)

	userA := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	userB := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")

	// Phase 1: Lock 250,000 tokens on Home Chain 101 -> Bridge to Chain 102
	bridgeAmount1 := big.NewInt(250_000)
	msg1, err := engine.LockAndBridgeAsset(101, 102, userA, userB, assetID, bridgeAmount1, big.NewInt(1))
	require.NoError(t, err)
	assert.Equal(t, bridgeAmount1, msg1.Value)
	assert.Equal(t, assetID, msg1.AssetID)

	// Settle on Chain 102 (Mint 250,000 wrapped tokens)
	require.NoError(t, engine.ReceiveAndSettleAsset(102, userB, assetID, bridgeAmount1))

	// Invariant Check 1
	ok1, err := engine.VerifyAssetConservationInvariant(assetID)
	require.NoError(t, err)
	assert.True(t, ok1)
	assert.Equal(t, big.NewInt(750_000), engine.CirculationBalances["777:101"])
	assert.Equal(t, big.NewInt(250_000), engine.CirculationBalances["777:102"])
	assert.Equal(t, big.NewInt(250_000), engine.VaultBalances["777:101"])

	// Phase 2: Transfer 100,000 wrapped tokens from Chain 102 -> Chain 103
	bridgeAmount2 := big.NewInt(100_000)
	msg2, err := engine.LockAndBridgeAsset(102, 103, userB, userA, assetID, bridgeAmount2, big.NewInt(1))
	require.NoError(t, err)
	assert.Equal(t, bridgeAmount2, msg2.Value)

	// Settle on Chain 103 (Mint 100,000 wrapped tokens)
	require.NoError(t, engine.ReceiveAndSettleAsset(103, userA, assetID, bridgeAmount2))

	// Invariant Check 2
	ok2, err := engine.VerifyAssetConservationInvariant(assetID)
	require.NoError(t, err)
	assert.True(t, ok2)
	assert.Equal(t, big.NewInt(750_000), engine.CirculationBalances["777:101"])
	assert.Equal(t, big.NewInt(150_000), engine.CirculationBalances["777:102"])
	assert.Equal(t, big.NewInt(100_000), engine.CirculationBalances["777:103"])

	// Phase 3: Return 100,000 wrapped tokens from Chain 103 back to Home Chain 101 (Burn & Unlock from Vault)
	bridgeAmount3 := big.NewInt(100_000)
	msg3, err := engine.LockAndBridgeAsset(103, 101, userA, userA, assetID, bridgeAmount3, big.NewInt(1))
	require.NoError(t, err)
	assert.Equal(t, bridgeAmount3, msg3.Value)

	// Settle on Home Chain 101 (Unlocks from Vault back into circulation)
	require.NoError(t, engine.ReceiveAndSettleAsset(101, userA, assetID, bridgeAmount3))

	// Invariant Check 3
	ok3, err := engine.VerifyAssetConservationInvariant(assetID)
	require.NoError(t, err)
	assert.True(t, ok3)
	assert.Equal(t, big.NewInt(850_000), engine.CirculationBalances["777:101"])
	assert.Equal(t, big.NewInt(150_000), engine.CirculationBalances["777:102"])
	assert.Zero(t, engine.CirculationBalances["777:103"].Sign())
	assert.Equal(t, big.NewInt(150_000), engine.VaultBalances["777:101"])
}

// ──────────────────────────────────────────────────────────────────────────────
// TEST P6.3: Fuzz Testing Asset Conservation Invariant Under 500 Random Swaps
// ──────────────────────────────────────────────────────────────────────────────
func TestP6_3_FuzzAssetConservationInvariant(t *testing.T) {
	engine, _, _ := setupTestAssetRegistry()

	assetID := big.NewInt(555)
	totalSupply := big.NewInt(10_000_000)

	entry := AssetEntry{
		AssetID:           assetID,
		HomeChainID:       101,
		CanonicalContract: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		WrappedContracts: map[uint64]common.Address{
			102: common.HexToAddress("0x2222222222222222222222222222222222222222"),
			103: common.HexToAddress("0x3333333333333333333333333333333333333333"),
			104: common.HexToAddress("0x4444444444444444444444444444444444444444"),
		},
	}
	entryBytes, _ := json.Marshal(entry)
	prop := &GovernanceProposal{
		Kind:     ProposalRegisterAsset,
		Payload:  entryBytes,
		Executed: true,
	}
	_, err := engine.RegisterAssetOnRootAnchor(prop, totalSupply)
	require.NoError(t, err)

	chains := []uint64{101, 102, 103, 104}
	rng := rand.New(rand.NewSource(42))
	user := common.HexToAddress("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE")

	for i := 0; i < 500; i++ {
		srcIdx := rng.Intn(len(chains))
		dstIdx := rng.Intn(len(chains))
		for srcIdx == dstIdx {
			dstIdx = rng.Intn(len(chains))
		}
		src := chains[srcIdx]
		dst := chains[dstIdx]

		srcBal := engine.CirculationBalances[fmt.Sprintf("555:%d", src)]
		if srcBal == nil || srcBal.Sign() <= 0 {
			continue
		}

		// Random amount between 1 and min(srcBal, 50,000)
		maxSend := new(big.Int).Set(srcBal)
		if maxSend.Cmp(big.NewInt(50_000)) > 0 {
			maxSend = big.NewInt(50_000)
		}
		amount := new(big.Int).Rand(rng, maxSend)
		if amount.Sign() <= 0 {
			amount = big.NewInt(1)
		}

		// Outbound
		_, err := engine.LockAndBridgeAsset(src, dst, user, user, assetID, amount, big.NewInt(1))
		if err != nil {
			continue
		}

		// Settle
		err = engine.ReceiveAndSettleAsset(dst, user, assetID, amount)
		require.NoError(t, err)

		// Invariant must hold 100% on every single random step
		valid, err := engine.VerifyAssetConservationInvariant(assetID)
		require.NoError(t, err)
		require.True(t, valid, "Invariant violation on step %d", i)
	}

	// Final verification
	valid, err := engine.VerifyAssetConservationInvariant(assetID)
	require.NoError(t, err)
	assert.True(t, valid, "Total supply must be 100% conserved after 500 fuzz transfers")
}
