package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
)

// TestGatewayHandler_RegisterAssetWithCert_Lifecycle is the end-to-end (real calldata, real
// dispatch) regression test for asset registration, authorized by the asset's own HomeChainID
// self-signing a real QuorumCert (2026-09-04, replacing the removed propose/vote/
// 72h-timelock/executeProposal(ProposalRegisterAsset)+registerAsset dance -- see
// AssetRegistryEngine.RegisterAssetOnRootAnchor's own doc comment).
func TestGatewayHandler_RegisterAssetWithCert_Lifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp101 := bls.GenerateKeyPair()
	pop101 := cross_chain.PopSign(kp101.PrivateKey(), kp101.PublicKey())
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.BytesPublicKey(), Stake: 100, PopSignature: pop101.Bytes()}}},
	}
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")

	assetEntry := cross_chain.AssetEntry{
		AssetID:           big.NewInt(777),
		HomeChainID:       101,
		CanonicalContract: common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
	}
	payload, err := json.Marshal(assetEntry)
	require.NoError(t, err)

	digest := cross_chain.ComputeRegisterAssetMessage(assetEntry.AssetID, assetEntry.HomeChainID, assetEntry.CanonicalContract)
	sig := bls.Sign(kp101.PrivateKey(), digest)
	regAssetCalldata, err := h.abi.Pack("registerAssetWithCert", payload, big.NewInt(1_000_000), uint64(1), sig.Bytes(), []byte{0x01})
	require.NoError(t, err)
	_, _, failed := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, regAssetCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 121)
	require.False(t, failed)

	// Query Asset via getAsset
	getAssetCalldata, _ := h.abi.Pack("getAsset", big.NewInt(777))
	assetRes, err := h.HandleOffChainQuery(cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, getAssetCalldata)))
	require.NoError(t, err)
	assetFields, err := h.abi.Unpack("getAsset", assetRes)
	require.NoError(t, err)
	assert.True(t, assetFields[0].(bool), "Asset 777 must exist")
	assert.Equal(t, big.NewInt(101), assetFields[1].(*big.Int), "HomeChainID must be 101")
	assert.Equal(t, common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), assetFields[2].(common.Address))
	assert.True(t, assetFields[3].(bool), "Asset must be active")

	// Negative Test: a forged cert (signed by a key that is NOT chain 101's real committee member)
	// cannot register a second asset.
	rogueKP := bls.GenerateKeyPair()
	otherAsset := cross_chain.AssetEntry{AssetID: big.NewInt(888), HomeChainID: 101, CanonicalContract: common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")}
	otherPayload, _ := json.Marshal(otherAsset)
	forgedDigest := cross_chain.ComputeRegisterAssetMessage(otherAsset.AssetID, otherAsset.HomeChainID, otherAsset.CanonicalContract)
	forgedSig := bls.Sign(rogueKP.PrivateKey(), forgedDigest)
	fakeRegCalldata, _ := h.abi.Pack("registerAssetWithCert", otherPayload, big.NewInt(100), uint64(1), forgedSig.Bytes(), []byte{0x01})
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, fakeRegCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 122)
	assert.True(t, failed, "a cert not actually signed by chain 101's own committee must be rejected")
}

// TestGatewayHandler_UpdateCommitteeWithRecoveryCert_Lifecycle is the end-to-end regression test
// for chain-committee recovery, authorized by RecoveryCommittee's real QuorumCert (2026-09-04,
// replacing the removed propose/vote/72h-timelock/executeProposal(ProposalUpdateCommittee) dance
// -- see GatewayEngine.UpdateCommitteeWithRecoveryCert's own doc comment).
func TestGatewayHandler_UpdateCommitteeWithRecoveryCert_Lifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp101 := bls.GenerateKeyPair()
	recoveryKP := bls.GenerateKeyPair()
	recoveryPop := cross_chain.PopSign(recoveryKP.PrivateKey(), recoveryKP.PublicKey())
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.PublicKey().Bytes(), Stake: 100}}},
	}
	engine.RecoveryCommittee = []cross_chain.ValidatorEntry{
		{PubkeyBLS: recoveryKP.BytesPublicKey(), Stake: 10000, PopSignature: recoveryPop.Bytes()},
	}
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x3333333333333333333333333333333333333333")

	kpNew1 := bls.GenerateKeyPair()
	kpNew2 := bls.GenerateKeyPair()
	popSigNew1 := cross_chain.PopSign(kpNew1.PrivateKey(), kpNew1.PublicKey())
	popSigNew2 := cross_chain.PopSign(kpNew2.PrivateKey(), kpNew2.PublicKey())

	newCommittee := []cross_chain.ValidatorEntry{
		{PubkeyBLS: kpNew1.BytesPublicKey(), Stake: 5000, PopSignature: popSigNew1.Bytes()},
		{PubkeyBLS: kpNew2.BytesPublicKey(), Stake: 5000, PopSignature: popSigNew2.Bytes()},
	}

	payloadObj := cross_chain.UpdateCommitteePayload{
		ChainID:         101,
		NewEpoch:        2,
		NewCommittee:    newCommittee,
		QuorumThreshold: 6700,
	}
	payload, err := json.Marshal(payloadObj)
	require.NoError(t, err)

	digest := cross_chain.ComputeRecoveryUpdateCommitteeMessage(payloadObj.ChainID, payloadObj.NewEpoch, payloadObj.NewCommittee, payloadObj.QuorumThreshold, payloadObj.StateRoot, payloadObj.AccountTreeRoot)
	sig := bls.Sign(recoveryKP.PrivateKey(), digest)
	updateCalldata, err := h.abi.Pack("updateCommitteeWithRecoveryCert", payload, uint64(0), sig.Bytes(), []byte{0x01})
	require.NoError(t, err)
	_, _, failed := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, updateCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 120)
	require.False(t, failed)

	// Verify updated ChainRegistry state
	engineAfter, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	reg101 := engineAfter.ChainRegistry[101]
	assert.Equal(t, uint64(2), reg101.Epoch)
	assert.Equal(t, uint64(6700), reg101.QuorumThreshold)
	assert.Equal(t, 2, len(reg101.Committee))
	assert.Equal(t, kpNew1.BytesPublicKey(), reg101.Committee[0].PubkeyBLS)
	assert.Equal(t, kpNew2.BytesPublicKey(), reg101.Committee[1].PubkeyBLS)
}

// TestGatewayHandler_RegisterChainViaStake_TracksChainCountViaMetric is the regression test for
// note/cross_chain_attack_scenario_catalog.md item C6: with BootstrapFoundingChains retired
// (2026-08-28), registerChainViaStake is now gated by a REAL native-coin deposit from the
// caller's own wallet instead (MinNativeStakeToRegister, checked+burned in gateway_handler.go),
// not a vote -- so a metric on ChainRegistry's growth stays just as important: a slowly-
// accumulating colluding coalition would look exactly like unremarkable steady growth without
// one. This proves the metric reflects reality across 4 real, individually-funded
// registerChainViaStake calls, mirroring the propose() test's own style above.
func TestGatewayHandler_RegisterChainViaStake_TracksChainCountViaMetric(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	minStake := big.NewInt(1000)
	engine := cross_chain.NewGatewayEngine(9099, map[uint64]cross_chain.ChainRegistry{}, nil)
	engine.MinNativeStakeToRegister = minStake
	require.NoError(t, saveGatewayEngine(cs, engine))

	callers := []common.Address{
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD01"),
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD02"),
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD03"),
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD04"),
	}
	for i, id := range []uint64{101, 102, 103, 104} {
		caller := callers[i]
		require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))
		calldata, err := h.abi.Pack("registerChainViaStake", makeFoundingChainPayload(t, id), minStake)
		require.NoError(t, err)
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.False(t, failed, "registerChainViaStake for chain %d with a real, sufficient deposit must succeed", id)
	}

	assert.Equal(t, float64(4), testutil.ToFloat64(metrics.RegisteredChainCount), "RegisteredChainCount must reflect the real ChainRegistry size after 4 individual registrations")
}

// TestGatewayHandler_RegisterChainViaStake_NoVoteRequired is the end-to-end (real calldata,
// real dispatch) regression test for the vote-free registration path: a candidate must be
// admitted into ChainRegistry via a SINGLE registerChainViaStake transaction backed by a REAL
// native-coin deposit from the caller's own wallet, with no propose()/vote()/executeProposal()
// transaction anywhere in this test -- that absence is the behavior under test, not an
// oversight.
func TestGatewayHandler_RegisterChainViaStake_NoVoteRequired(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	minStake := big.NewInt(1000)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{}, nil)
	engine.MinNativeStakeToRegister = minStake
	require.NoError(t, saveGatewayEngine(cs, engine))

	caller := common.HexToAddress("0xCC00CC00CC00CC00CC00CC00CC00CC00CC00CC00")
	// Fund the caller's REAL wallet -- not any PerChainAllocation/SupplyLedger primitive -- with
	// exactly the required deposit (see gateway_test.go's TestGateway_RegisterChainViaStake for
	// the unit-level proof that RegisterChainViaStake itself performs no stake check at all).
	require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))

	payload := makeFoundingChainPayload(t, 104)
	calldata, err := h.abi.Pack("registerChainViaStake", payload, minStake)
	require.NoError(t, err)

	tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "registerChainViaStake with a sufficiently-funded real wallet must succeed with zero votes cast")

	reloaded, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	_, exists := reloaded.ChainRegistry[104]
	assert.True(t, exists, "chain 104 must be registered")
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.RegisteredChainCount))
}

// TestGatewayHandler_RegisterChainViaStake_ForcesGenesisWalletToRealCaller is the regression test
// for the 2026-09-04 deterministic-genesis design's identity-forcing: whatever genesis_wallet a
// submitted payload claims must be silently overwritten with tx.FromAddress() -- the address that
// actually paid the stake -- never trusted from calldata. Otherwise a registrant could name an
// unrelated, well-known address as the chain's GenesisWallet, making it look like that address
// requested and funded a chain it never touched.
func TestGatewayHandler_RegisterChainViaStake_ForcesGenesisWalletToRealCaller(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	minStake := big.NewInt(1000)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{}, nil)
	engine.MinNativeStakeToRegister = minStake
	require.NoError(t, saveGatewayEngine(cs, engine))

	caller := common.HexToAddress("0xCC00CC00CC00CC00CC00CC00CC00CC00CC00CC00")
	impersonated := common.HexToAddress("0x9999999999999999999999999999999999999999")
	require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))

	// Payload claims genesis_wallet = impersonated -- a third party the caller does not control.
	kp := bls.GenerateKeyPair()
	popSig := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	reg := cross_chain.ChainRegistry{
		ChainID:       104,
		Committee:     []cross_chain.ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000, PopSignature: popSig.Bytes()}},
		QuorumThreshold: 6667,
		GenesisWallet: impersonated,
	}
	payload, err := json.Marshal(reg)
	require.NoError(t, err)
	calldata, err := h.abi.Pack("registerChainViaStake", payload, minStake)
	require.NoError(t, err)

	tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "registration itself must still succeed")

	reloaded, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	assert.Equal(t, caller, reloaded.ChainRegistry[104].GenesisWallet, "genesis_wallet must be forced to the real caller, never the impersonated address the payload claimed")
}

// TestGatewayHandler_SetGenesisDigest_UsesRealCallerNotCalldata is the end-to-end regression test
// proving the "setGenesisDigest" case forces tx.FromAddress() as the caller identity (never
// something read out of calldata) -- the exact property GatewayEngine.SetGenesisDigest's own doc
// comment says closes the front-running race. An attacker crafting a transaction FROM their own
// address can never successfully impersonate the real genesis wallet, no matter what the calldata
// itself claims.
func TestGatewayHandler_SetGenesisDigest_UsesRealCallerNotCalldata(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	genesisWallet := common.HexToAddress("0xfeedfeedfeedfeedfeedfeedfeedfeedfeedfeed")
	attacker := common.HexToAddress("0xbadbadbadbadbadbadbadbadbadbadbadbadbad")

	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		104: {ChainID: 104, Epoch: 1, QuorumThreshold: 6667, GenesisWallet: genesisWallet},
	}, nil)
	require.NoError(t, saveGatewayEngine(cs, engine))

	digest := common.HexToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	calldata, err := h.abi.Pack("setGenesisDigest", new(big.Int).SetUint64(104), digest)
	require.NoError(t, err)

	t.Run("attacker's own transaction cannot publish the digest even though calldata targets chain 104", func(t *testing.T) {
		tx := newTx(attacker, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "setGenesisDigest from a non-genesis-wallet address must fail")

		reloaded, err := loadGatewayEngine(cs)
		require.NoError(t, err)
		assert.Equal(t, common.Hash{}, reloaded.ChainRegistry[104].GenesisDigest, "a rejected attempt must never move the digest")
	})

	t.Run("the real genesis wallet's own transaction succeeds", func(t *testing.T) {
		tx := newTx(genesisWallet, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.False(t, failed, "setGenesisDigest from the real genesis wallet must succeed")

		reloaded, err := loadGatewayEngine(cs)
		require.NoError(t, err)
		assert.Equal(t, digest, reloaded.ChainRegistry[104].GenesisDigest)
	})
}
