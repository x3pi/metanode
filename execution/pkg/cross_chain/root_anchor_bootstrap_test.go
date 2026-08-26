package cross_chain

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeFoundingChainPayload builds a real, PoP-signed single-validator ChainRegistry JSON payload
// for chainID — the same shape register_private_chains_t2.py / propose(ProposalRegisterChain,...)
// already send.
func makeFoundingChainPayload(t *testing.T, chainID uint64) []byte {
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

func TestBootstrapFoundingChains_Success(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)

	var payloads [][]byte
	for _, id := range []uint64{101, 102, 103, 104} {
		payloads = append(payloads, makeFoundingChainPayload(t, id))
	}

	err := engine.BootstrapFoundingChains(payloads)
	require.NoError(t, err)

	assert.Len(t, engine.ChainRegistry, 4)
	assert.Len(t, engine.Governance.ActiveChains, 4)
	for _, id := range []uint64{101, 102, 103, 104} {
		assert.True(t, engine.Governance.ActiveChains[id])
		_, exists := engine.ChainRegistry[id]
		assert.True(t, exists)
	}

	// Governance must now actually work: a normal chain can vote using its own committee key.
	threshold, err := engine.Governance.QuorumThreshold()
	require.NoError(t, err)
	assert.Equal(t, uint64(3), threshold) // ceil(2*4/3) = 3
}

func TestBootstrapFoundingChains_RejectsFewerThanMinimum(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	var payloads [][]byte
	for _, id := range []uint64{101, 102, 103} { // only 3, need >= 4
		payloads = append(payloads, makeFoundingChainPayload(t, id))
	}
	err := engine.BootstrapFoundingChains(payloads)
	assert.ErrorIs(t, err, ErrInsufficientFoundingChains)
	assert.Len(t, engine.ChainRegistry, 0, "a rejected bootstrap must not partially apply")
}

func TestBootstrapFoundingChains_RejectsDuplicateChainID(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	payloads := [][]byte{
		makeFoundingChainPayload(t, 101),
		makeFoundingChainPayload(t, 102),
		makeFoundingChainPayload(t, 103),
		makeFoundingChainPayload(t, 101), // duplicate
	}
	err := engine.BootstrapFoundingChains(payloads)
	assert.ErrorIs(t, err, ErrDuplicateChainID)
	assert.Len(t, engine.ChainRegistry, 0)
}

func TestBootstrapFoundingChains_RejectsForgedPop(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)

	kp := bls.GenerateKeyPair()
	other := bls.GenerateKeyPair()
	forgedPop := PopSign(other.PrivateKey(), kp.PublicKey()) // signed by the WRONG key
	reg := ChainRegistry{
		ChainID:   104,
		Committee: []ValidatorEntry{{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000, PopSignature: forgedPop.Bytes()}},
	}
	badPayload, err := json.Marshal(reg)
	require.NoError(t, err)

	payloads := [][]byte{
		makeFoundingChainPayload(t, 101),
		makeFoundingChainPayload(t, 102),
		makeFoundingChainPayload(t, 103),
		badPayload,
	}
	err = engine.BootstrapFoundingChains(payloads)
	require.Error(t, err)
	assert.Len(t, engine.ChainRegistry, 0, "a rejected bootstrap must not partially apply, even if only 1 of N payloads is bad")
}

func TestBootstrapFoundingChains_SelfClosesAfterFirstSuccess(t *testing.T) {
	engine := NewGatewayEngine(9099, map[uint64]ChainRegistry{}, nil)
	var payloads [][]byte
	for _, id := range []uint64{101, 102, 103, 104} {
		payloads = append(payloads, makeFoundingChainPayload(t, id))
	}
	require.NoError(t, engine.BootstrapFoundingChains(payloads))

	// A second attempt — even with an entirely different, otherwise-valid set of chains — must
	// be rejected now that the registry is no longer genesis-empty.
	var payloads2 [][]byte
	for _, id := range []uint64{201, 202, 203, 204} {
		payloads2 = append(payloads2, makeFoundingChainPayload(t, id))
	}
	err := engine.BootstrapFoundingChains(payloads2)
	assert.ErrorIs(t, err, ErrAlreadyBootstrapped)
	assert.Len(t, engine.ChainRegistry, 4, "second bootstrap attempt must not add anything")
	_, exists := engine.ChainRegistry[201]
	assert.False(t, exists)
}
