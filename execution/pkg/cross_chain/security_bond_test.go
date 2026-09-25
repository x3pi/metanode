package cross_chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// Phase A — SecurityBond / SlashOnEquivocation
// (note/cross_chain/root_anchor_production_security_hardening_plan.md, adapted from
// note/cross_chain/shard_design_ton_real.md mục 5.5/8.8)
// ══════════════════════════════════════════════════════════════════════════════

func TestSecurityBond_PostSecurityBond(t *testing.T) {
	engine, _ := setupTestGatewayEngine()

	// Unknown chain -- reject.
	err := engine.PostSecurityBond(9999, big.NewInt(100))
	assert.ErrorIs(t, err, ErrUnknownSourceChain)

	// Zero/negative amount -- reject.
	err = engine.PostSecurityBond(101, big.NewInt(0))
	assert.ErrorIs(t, err, ErrNoBondToPost)
	err = engine.PostSecurityBond(101, big.NewInt(-5))
	assert.ErrorIs(t, err, ErrNoBondToPost)

	// Real post -- accumulates.
	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(100)))
	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(50)))
	assert.Equal(t, big.NewInt(150), engine.SecurityBond.Bond[101])

	// Dead chain -- reject.
	engine.DeadChains[101] = true
	err = engine.PostSecurityBond(101, big.NewInt(10))
	assert.ErrorIs(t, err, ErrChainDeclaredDead)
}

func TestSecurityBond_PostSecurityBond_RejectsBelowMinimum(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	engine.MinSecurityBondToRegister = big.NewInt(200)

	// A single post that would leave the running total below the minimum is rejected outright --
	// and, matching this codebase's "no mutation on failure" convention everywhere else, does NOT
	// partially apply: Bond[101] must stay completely unset, not "100 and rejected".
	err := engine.PostSecurityBond(101, big.NewInt(100))
	assert.ErrorIs(t, err, ErrBelowMinSecurityBond)
	assert.Nil(t, engine.SecurityBond.Bond[101])

	// A post that meets the minimum in one shot succeeds.
	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(200)))
	assert.Equal(t, big.NewInt(200), engine.SecurityBond.Bond[101])

	// Once the running total already clears the minimum, further top-ups of any size succeed.
	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(50)))
	assert.Equal(t, big.NewInt(250), engine.SecurityBond.Bond[101])
}

// TestSecurityBond_BondLeverageZero_IsDisabledByDefault is the regression test for the
// deliberate default: BondLeverage==0 (never set) must be a strict no-op, identical to this
// feature not existing, so already-registered chains with no bond are never retroactively broken.
// Chain 101 has NO bond at all here -- a real TransferAllocationWithCert into it must still
// succeed exactly as it did before SecurityBond existed.
func TestSecurityBond_BondLeverageZero_IsDisabledByDefault(t *testing.T) {
	engine, _ := setupTestGatewayEngine()

	fromKP := bls.GenerateKeyPair()
	fromPop := PopSign(fromKP.PrivateKey(), fromKP.PublicKey())
	engine.ChainRegistry[102] = ChainRegistry{
		ChainID:   102,
		Committee: []ValidatorEntry{{PubkeyBLS: fromKP.BytesPublicKey(), Stake: 10000, PopSignature: fromPop.Bytes()}},
		Epoch:     0,
	}

	digest := ComputeTransferAllocationMessage(102, 101, big.NewInt(1000), 0)
	sig := bls.Sign(fromKP.PrivateKey(), digest)
	cert := QuorumCert{Epoch: 0, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	require.NoError(t, engine.TransferAllocationWithCert(102, 101, big.NewInt(1000), 0, cert))
	assert.Equal(t, big.NewInt(6000), engine.SupplyLedger.GetAllocation(101))
}

func TestSecurityBond_CheckBondCap_EnforcedWhenLeverageSet(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	// engine is chain 102 (Reserve); chain 101 is registered. Give chain 102 -> 101 a nonce-0
	// transfer path: sign with chain 102's OWN committee. setupTestGatewayEngine only registers
	// chain 101's committee, not chain 102's -- add one so TransferAllocationWithCert (which
	// verifies against fromRegistry = ChainRegistry[fromChainID]) has a real committee to check.
	fromKP := bls.GenerateKeyPair()
	fromPop := PopSign(fromKP.PrivateKey(), fromKP.PublicKey())
	engine.ChainRegistry[102] = ChainRegistry{
		ChainID:   102,
		Committee: []ValidatorEntry{{PubkeyBLS: fromKP.BytesPublicKey(), Stake: 10000, PopSignature: fromPop.Bytes()}},
		Epoch:     0,
	}

	engine.BondLeverage = 2
	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(100))) // cap = 2*100 = 200

	sign := func(fromChainID, toChainID uint64, amount *big.Int, nonce uint64) QuorumCert {
		digest := ComputeTransferAllocationMessage(fromChainID, toChainID, amount, nonce)
		sig := bls.Sign(fromKP.PrivateKey(), digest)
		return QuorumCert{Epoch: 0, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}
	}

	// Chain 101 starts with allocation 5000 (setupTestGatewayEngine) -- already over the 200 cap
	// even before any transfer, so ANY positive transfer into it must now be rejected.
	err := engine.TransferAllocationWithCert(102, 101, big.NewInt(1), 0, sign(102, 101, big.NewInt(1), 0))
	assert.ErrorIs(t, err, ErrBondCapExceeded)

	// The nonce must NOT have advanced on a bond-cap rejection (same "no mutation on failure"
	// guarantee TransferAllocationWithCert already gives for other rejection reasons).
	assert.Equal(t, uint64(0), engine.TransferAllocationNonce[102])
}

// TestSecurityBond_ForfeitBond_MovesBondToReserveAndKillsChain proves forfeitBond's behavior: the
// bond moves into Reserve's own circulating PerChainAllocation, and DeadChains is set (already
// covered independently by TestP8_4). The real production route into forfeitBond is
// SlashOnEquivocation, tested end-to-end below.
func TestSecurityBond_ForfeitBond_MovesBondToReserveAndKillsChain(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	engine.ReserveChainID = 102 // engine IS chain 102

	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(300)))
	reserveAllocBefore := engine.SupplyLedger.GetAllocation(102)

	markChainDeadForTest(engine, 101)

	assert.True(t, engine.DeadChains[101])
	_, stillHasBond := engine.SecurityBond.Bond[101]
	assert.False(t, stillHasBond, "bond must be removed from the active ledger once forfeited")
	reserveAllocAfter := engine.SupplyLedger.GetAllocation(102)
	assert.Equal(t, new(big.Int).Add(reserveAllocBefore, big.NewInt(300)), reserveAllocAfter)
}

// TestSecurityBond_UnregisterThenClaim_RespectsUnbondingPeriod is the regression test for the
// "hit and run" gap: Bond must NOT release immediately on UnregisterChainWithCert, only after
// ReleaseAt (blockTime + UnbondingPeriodSeconds) has passed -- and ClaimUnbondedBond must return
// the exact amount + the chain's own recorded GenesisWallet.
func TestSecurityBond_UnregisterThenClaim_RespectsUnbondingPeriod(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	engine.UnbondingPeriodSeconds = 1000
	genesisWallet := common.HexToAddress("0x9999999999999999999999999999999999999999")
	reg := engine.ChainRegistry[101]
	reg.GenesisWallet = genesisWallet
	engine.ChainRegistry[101] = reg

	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(500)))

	const unregisterAt = uint64(10_000)
	require.NoError(t, engine.UnregisterChainWithCert(101, 0, signUnregisterCert(engine, kp, 101, 0), unregisterAt))

	// ChainRegistry entry is gone (unchanged existing behavior), bond is no longer "active".
	_, stillRegistered := engine.ChainRegistry[101]
	assert.False(t, stillRegistered)
	_, stillActiveBond := engine.SecurityBond.Bond[101]
	assert.False(t, stillActiveBond)

	// Too early -- must reject.
	_, _, err := engine.ClaimUnbondedBond(101, unregisterAt+500)
	assert.ErrorIs(t, err, ErrUnbondingNotReady)

	// After the full period -- must succeed, with the exact amount and recorded GenesisWallet.
	amount, wallet, err := engine.ClaimUnbondedBond(101, unregisterAt+1000)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(500), amount)
	assert.Equal(t, genesisWallet, wallet)

	// Second claim -- nothing left to claim.
	_, _, err = engine.ClaimUnbondedBond(101, unregisterAt+2000)
	assert.ErrorIs(t, err, ErrNoUnbondingRequest)
}

func TestSecurityBond_UnregisterWithNoBond_UnchangedBehavior(t *testing.T) {
	engine, kp := setupTestGatewayEngine()

	require.NoError(t, engine.UnregisterChainWithCert(101, 0, signUnregisterCert(engine, kp, 101, 0), 12345))

	_, stillRegistered := engine.ChainRegistry[101]
	assert.False(t, stillRegistered)
	_, _, err := engine.ClaimUnbondedBond(101, 99999999)
	assert.ErrorIs(t, err, ErrNoUnbondingRequest, "a chain that never posted a bond must have nothing to unbond")
}

// TestSecurityBond_SlashOnEquivocation_RealProof_ForfeitsAndKillsChain proves the permissionless
// fast path: 2 genuinely conflicting QuorumCerts (same committee, same epoch, different commit
// roots) forfeit the bond and set DeadChains -- no third-party authorization needed at all.
func TestSecurityBond_SlashOnEquivocation_RealProof_ForfeitsAndKillsChain(t *testing.T) {
	engine, kp := setupTestGatewayEngine() // kp is chain 101's real committee key
	engine.ReserveChainID = 102
	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(700)))

	const epoch = uint64(5) // matches setupTestGatewayEngine's chain 101 Epoch
	rootA := common.HexToHash("0xAAAA000000000000000000000000000000000000000000000000000000000000")
	rootB := common.HexToHash("0xBBBB000000000000000000000000000000000000000000000000000000000000")
	sigA := bls.Sign(kp.PrivateKey(), ComputeCommitRootAttestMessage(rootA))
	sigB := bls.Sign(kp.PrivateKey(), ComputeCommitRootAttestMessage(rootB))
	certA := QuorumCert{Epoch: epoch, AggregateSignature: sigA.Bytes(), SignerBitmap: []byte{0x01}}
	certB := QuorumCert{Epoch: epoch, AggregateSignature: sigB.Bytes(), SignerBitmap: []byte{0x01}}

	require.NoError(t, engine.SlashOnEquivocation(101, epoch, rootA, rootB, certA, certB))

	assert.True(t, engine.DeadChains[101])
	_, stillHasBond := engine.SecurityBond.Bond[101]
	assert.False(t, stillHasBond)
	assert.Equal(t, big.NewInt(5700), engine.SupplyLedger.GetAllocation(102), "700 forfeited bond credited on top of chain 102's starting 5000 allocation")
}

func TestSecurityBond_SlashOnEquivocation_RejectsNonContradiction(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	const epoch = uint64(5)
	root := common.HexToHash("0xCCCC000000000000000000000000000000000000000000000000000000000000")
	sig := bls.Sign(kp.PrivateKey(), ComputeCommitRootAttestMessage(root))
	cert := QuorumCert{Epoch: epoch, AggregateSignature: sig.Bytes(), SignerBitmap: []byte{0x01}}

	err := engine.SlashOnEquivocation(101, epoch, root, root, cert, cert)
	assert.ErrorIs(t, err, ErrNotEquivocation)
}

func TestSecurityBond_SlashOnEquivocation_RejectsForgedSignature(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	attacker := bls.GenerateKeyPair() // NOT chain 101's real committee key
	const epoch = uint64(5)
	rootA := common.HexToHash("0xDDDD000000000000000000000000000000000000000000000000000000000000")
	rootB := common.HexToHash("0xEEEE000000000000000000000000000000000000000000000000000000000000")
	sigA := bls.Sign(attacker.PrivateKey(), ComputeCommitRootAttestMessage(rootA))
	sigB := bls.Sign(attacker.PrivateKey(), ComputeCommitRootAttestMessage(rootB))
	certA := QuorumCert{Epoch: epoch, AggregateSignature: sigA.Bytes(), SignerBitmap: []byte{0x01}}
	certB := QuorumCert{Epoch: epoch, AggregateSignature: sigB.Bytes(), SignerBitmap: []byte{0x01}}

	err := engine.SlashOnEquivocation(101, epoch, rootA, rootB, certA, certB)
	assert.Error(t, err, "a forged signature from a non-committee key must never slash a real chain's bond")
	assert.False(t, engine.DeadChains[101])
}

// TestSecurityBond_SlashOnEquivocation_WorksDuringUnbonding is the regression test for the
// "unregister then equivocate before anyone notices" hit-and-run gap: even after
// UnregisterChainWithCert has removed the chain's ChainRegistry entry, SlashOnEquivocation must
// still be able to verify (and forfeit) a real equivocation proof using the committee/epoch
// snapshot captured in the pending UnbondingRequest.
func TestSecurityBond_SlashOnEquivocation_WorksDuringUnbonding(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	engine.ReserveChainID = 102
	engine.UnbondingPeriodSeconds = 10_000

	require.NoError(t, engine.PostSecurityBond(101, big.NewInt(400)))

	const unregisterAt = uint64(1_000_000)
	require.NoError(t, engine.UnregisterChainWithCert(101, 0, signUnregisterCert(engine, kp, 101, 0), unregisterAt))

	_, stillRegistered := engine.ChainRegistry[101]
	require.False(t, stillRegistered, "sanity: chain really is gone from ChainRegistry now")

	const epoch = uint64(5) // the epoch chain 101 had at unregister time (setupTestGatewayEngine)
	rootA := common.HexToHash("0x1111000000000000000000000000000000000000000000000000000000000000")
	rootB := common.HexToHash("0x2222000000000000000000000000000000000000000000000000000000000000")
	sigA := bls.Sign(kp.PrivateKey(), ComputeCommitRootAttestMessage(rootA))
	sigB := bls.Sign(kp.PrivateKey(), ComputeCommitRootAttestMessage(rootB))
	certA := QuorumCert{Epoch: epoch, AggregateSignature: sigA.Bytes(), SignerBitmap: []byte{0x01}}
	certB := QuorumCert{Epoch: epoch, AggregateSignature: sigB.Bytes(), SignerBitmap: []byte{0x01}}

	require.NoError(t, engine.SlashOnEquivocation(101, epoch, rootA, rootB, certA, certB))
	assert.True(t, engine.DeadChains[101])

	// The bond that was pending unbonding must now be gone entirely (forfeited), not claimable.
	_, _, claimErr := engine.ClaimUnbondedBond(101, unregisterAt+10_000)
	assert.ErrorIs(t, claimErr, ErrNoUnbondingRequest, "a slashed bond must never still be claimable")
}

// TestUnregisterChain_SelfAuthorized_RejectsCertFromAnotherKey proves the removed RecoveryCommittee
// is not silently replaced by "anyone can unregister": only chain 101's OWN committee key works.
func TestUnregisterChain_SelfAuthorized_RejectsCertFromAnotherKey(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	otherKP := bls.GenerateKeyPair()

	err := engine.UnregisterChainWithCert(101, 0, signUnregisterCert(engine, otherKP, 101, 0), 1000)
	require.Error(t, err)
	_, stillRegistered := engine.ChainRegistry[101]
	assert.True(t, stillRegistered, "a cert from a key outside the chain's own committee must not unregister it")
}

func TestUnregisterChain_RejectsUnknownChain(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	err := engine.UnregisterChainWithCert(999, 0, signUnregisterCert(engine, kp, 999, 0), 1000)
	assert.ErrorIs(t, err, ErrUnknownChain)
}

func TestUnregisterChain_RejectsWrongNonce(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	err := engine.UnregisterChainWithCert(101, 1, signUnregisterCert(engine, kp, 101, 1), 1000)
	assert.ErrorIs(t, err, ErrInvalidUnregisterNonce)
	_, stillRegistered := engine.ChainRegistry[101]
	assert.True(t, stillRegistered)
}

// TestUnregisterChain_CertCannotBeReplayedAfterReRegistration is the regression test for the
// replay risk of a self-authorized unregister cert: it is public once submitted, and
// RegisterChainViaStake only requires the chainID to be currently unregistered, so without the
// nonce the identical cert could unregister a chain that legitimately registered again.
func TestUnregisterChain_CertCannotBeReplayedAfterReRegistration(t *testing.T) {
	engine, kp := setupTestGatewayEngine()
	savedReg := engine.ChainRegistry[101]
	cert0 := signUnregisterCert(engine, kp, 101, 0)

	require.NoError(t, engine.UnregisterChainWithCert(101, 0, cert0, 1000))
	assert.Equal(t, uint64(1), engine.UnregisterNonce[101], "nonce must survive the registry entry's deletion")

	engine.ChainRegistry[101] = savedReg // the same chainID registers again
	err := engine.UnregisterChainWithCert(101, 0, cert0, 2000)
	assert.ErrorIs(t, err, ErrInvalidUnregisterNonce, "replaying the old cert must fail")
	_, stillRegistered := engine.ChainRegistry[101]
	assert.True(t, stillRegistered)

	// A freshly signed cert at the new nonce still works.
	require.NoError(t, engine.UnregisterChainWithCert(101, 1, signUnregisterCert(engine, kp, 101, 1), 3000))
}
