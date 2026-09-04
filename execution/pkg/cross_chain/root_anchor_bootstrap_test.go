package cross_chain

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file used to cover BootstrapFoundingChains -- a genesis-only, >= MinFoundingChains BATCH
// registration call from a single (optionally GenesisCoordinator-restricted) caller. Retired
// 2026-08-28 in favor of RegisterChainViaStake (already per-chain) becoming the universal,
// vote-free registration path for every chain, including chain #1 -- see
// note/cross_chain_stake_and_value_flow.md for the full rationale. GatewayEngine.
// RegisterChainViaStake itself performs no stake check (that lives one layer up, in
// gateway_handler.go, which has the AccountStateDB access needed to verify a real wallet
// balance -- see gateway_handler_test.go for that coverage); this file covers what
// RegisterChainViaStake is still responsible for on its own: PoP verification and
// duplicate-chain-ID rejection, now evaluated per-call instead of per-batch.

// makeRegistrationPayload builds a real, PoP-signed single-validator ChainRegistry JSON payload
// for chainID — the same shape register_chains' registerChainViaStake calls already send.
func makeRegistrationPayload(t *testing.T, chainID uint64) []byte {
	t.Helper()
	kp := bls.GenerateKeyPair()
	popSig := PopSign(kp.PrivateKey(), kp.PublicKey())
	reg := ChainRegistry{
		ChainID: chainID,
		Committee: []ValidatorEntry{
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
	require.NoError(t, err)
	return b
}

func TestGateway_RegisterChainViaStake_MultipleChainsSucceed(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	engine.EnsureAssetRegistry()

	for _, id := range []uint64{101, 102, 103, 104} {
		require.NoError(t, engine.RegisterChainViaStake(makeRegistrationPayload(t, id), nil))
	}

	assert.Len(t, engine.ChainRegistry, 4)
	for _, id := range []uint64{101, 102, 103, 104} {
		_, exists := engine.ChainRegistry[id]
		assert.True(t, exists)
	}
}

// TestGateway_RegisterChainViaStake_NoFoundingChainFloor proves the specific behavior change
// this retirement was for: unlike the old BootstrapFoundingChains, which refused to run at all
// below MinFoundingChains(=4) entries in a single batch, RegisterChainViaStake happily registers
// a single chain (chain #1 -- Root Anchor's very first registrant) with no count floor at this
// layer. MinFoundingChains(=4)/NewRootAnchorCommittee (root_anchor.go) is a completely separate,
// pre-genesis mechanism for Root Anchor's OWN validator committee and is untouched by this.
func TestGateway_RegisterChainViaStake_NoFoundingChainFloor(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	engine.EnsureAssetRegistry()

	require.NoError(t, engine.RegisterChainViaStake(makeRegistrationPayload(t, 101), nil))

	assert.Len(t, engine.ChainRegistry, 1)
	_, exists := engine.ChainRegistry[101]
	assert.True(t, exists)
}

func TestGateway_RegisterChainViaStake_RejectsDuplicateChainID(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	engine.EnsureAssetRegistry()

	require.NoError(t, engine.RegisterChainViaStake(makeRegistrationPayload(t, 101), nil))

	err := engine.RegisterChainViaStake(makeRegistrationPayload(t, 101), nil)
	assert.ErrorIs(t, err, ErrChainAlreadyRegistered)
	assert.Len(t, engine.ChainRegistry, 1, "the original registration must be unaffected")
}

func TestGateway_RegisterChainViaStake_RejectsForgedPop(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	engine.EnsureAssetRegistry()

	kp := bls.GenerateKeyPair()
	other := bls.GenerateKeyPair()
	forgedPop := PopSign(other.PrivateKey(), kp.PublicKey()) // signed by the WRONG key
	reg := ChainRegistry{
		ChainID:   104,
		Committee: []ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000, PopSignature: forgedPop.Bytes()}},
	}
	badPayload, err := json.Marshal(reg)
	require.NoError(t, err)

	err = engine.RegisterChainViaStake(badPayload, nil)
	require.Error(t, err)
	assert.Len(t, engine.ChainRegistry, 0, "a rejected registration must not apply")
}

// TestGateway_RegisterChainViaStake_RepeatableAcrossManyChains proves the specific behavior
// change from the old BootstrapFoundingChains's self-closing-after-first-success design: since
// this is now the universal, permanent registration path (not a one-shot genesis mechanism),
// it must keep working for chain after chain, with no "already bootstrapped" lockout.
func TestGateway_RegisterChainViaStake_RepeatableAcrossManyChains(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	engine.EnsureAssetRegistry()

	for _, id := range []uint64{101, 102, 103, 104} {
		require.NoError(t, engine.RegisterChainViaStake(makeRegistrationPayload(t, id), nil))
	}

	for _, id := range []uint64{201, 202, 203, 204} {
		require.NoError(t, engine.RegisterChainViaStake(makeRegistrationPayload(t, id), nil))
	}

	assert.Len(t, engine.ChainRegistry, 8, "later registrations must not be locked out by earlier ones")
	_, exists := engine.ChainRegistry[201]
	assert.True(t, exists)
}
