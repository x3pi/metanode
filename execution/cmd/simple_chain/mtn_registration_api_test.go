package main

import (
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
)

type apiFakeParent struct {
	mu       sync.Mutex
	registry map[common.Address]cm.PublicKey
	sends    int
}

func (p *apiFakeParent) SendRegisterAccount(common.Address, cm.PublicKey, []byte, cm.Sign) (common.Hash, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends++
	return common.Hash{1}, nil
}

func (p *apiFakeParent) GetAccountRegistry(a common.Address) (cm.PublicKey, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k, ok := p.registry[a]
	return k, ok, nil
}

type regAPIHarness struct {
	api    *MtnAPI
	parent *apiFakeParent
	kp     *bls.KeyPair
	flags  sync.Map
}

func newRegAPIHarness(gate bool) *regAPIHarness {
	h := &regAPIHarness{parent: &apiFakeParent{registry: map[common.Address]cm.PublicKey{}}, kp: bls.GenerateKeyPair()}
	cfg := &config.SimpleChainConfig{ChainId: big.NewInt(991)}
	if gate {
		cfg.TxSignatureMode = config.TxSignatureModeSecp
		cfg.AccountGate = config.AccountGateParentRegistered
	}
	relay := rollup.NewRegistrationRelay(h.parent, h.kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(h.kp.PrivateKey(), d) },
		func(a common.Address) bool { v, ok := h.flags.Load(a); return ok && v.(bool) })
	h.api = &MtnAPI{App: &App{config: cfg, regRelay: relay}}
	return h
}

func (h *regAPIHarness) sign(t *testing.T) (common.Address, hexutil.Bytes, *RegistrationMessage) {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(key.PublicKey)
	msg, err := h.api.GetRegistrationMessage(addr)
	require.NoError(t, err)
	hash, err := hexutil.Decode(msg.HashToSign)
	require.NoError(t, err)
	sig, err := crypto.Sign(hash, key)
	require.NoError(t, err)
	return addr, sig, msg
}

// Everything is refused when the gate is off, so a legacy chain exposes no registration surface.
func TestRegistrationAPI_DisabledWhenGateOff(t *testing.T) {
	h := newRegAPIHarness(false)
	_, err := h.api.GetClusterIdentity()
	assert.ErrorIs(t, err, errRegistrationDisabled)
	_, err = h.api.GetRegistrationMessage(common.Address{1})
	assert.ErrorIs(t, err, errRegistrationDisabled)
	_, err = h.api.RegisterAccount(common.Address{1}, make([]byte, 65))
	assert.ErrorIs(t, err, errRegistrationDisabled)
	_, err = h.api.GetRegistrationStatus(common.Address{1})
	assert.ErrorIs(t, err, errRegistrationDisabled)

	// and a node without a relay at all
	none := &MtnAPI{App: &App{config: &config.SimpleChainConfig{ChainId: big.NewInt(1), TxSignatureMode: config.TxSignatureModeSecp, AccountGate: config.AccountGateParentRegistered}}}
	_, err = none.GetClusterIdentity()
	assert.ErrorIs(t, err, errRegistrationDisabled)
	_, err = (&MtnAPI{}).RegisterAccount(common.Address{1}, nil)
	assert.ErrorIs(t, err, errRegistrationDisabled)
}

func TestRegistrationAPI_ClusterIdentityAndMessage(t *testing.T) {
	h := newRegAPIHarness(true)
	id, err := h.api.GetClusterIdentity()
	require.NoError(t, err)
	assert.Equal(t, "0x"+common.Bytes2Hex(h.kp.PublicKey().Bytes()), id.ClusterKey)
	assert.Equal(t, uint64(991), id.ChainID)
	assert.True(t, id.AccountGate)
	assert.Equal(t, string(parentchain.RegisterAccountDomainTag), id.MessageTag)

	addr := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	msg, err := h.api.GetRegistrationMessage(addr)
	require.NoError(t, err)
	want := parentchain.ComputeRegisterAccountMessage(addr, h.kp.PublicKey())
	assert.Equal(t, hexutil.Encode(want), msg.Digest, "the digest is exactly what the parent chain verifies")
	assert.Equal(t, hexutil.Encode(crypto.Keccak256(want)), msg.HashToSign)
}

func TestRegistrationAPI_RegisterThenStatusFlow(t *testing.T) {
	h := newRegAPIHarness(true)
	addr, sig, _ := h.sign(t)

	st, err := h.api.GetRegistrationStatus(addr)
	require.NoError(t, err)
	assert.Equal(t, "NONE", st.Status)

	info, err := h.api.RegisterAccount(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, "PENDING", info.Status)

	// the relay finishes the job without the user doing anything else
	h.api.App.regRelay.ProcessBatch()
	assert.Equal(t, 1, h.parent.sends)
	h.parent.mu.Lock()
	h.parent.registry[addr] = h.kp.PublicKey()
	h.parent.mu.Unlock()
	h.api.App.regRelay.ProcessBatch()
	st, _ = h.api.GetRegistrationStatus(addr)
	assert.Equal(t, "PENDING", st.Status)

	h.flags.Store(addr, true) // the ordered registration event sets the on-chain flag
	st, _ = h.api.GetRegistrationStatus(addr)
	assert.Equal(t, "CONFIRMED", st.Status)
	again, err := h.api.RegisterAccount(addr, sig)
	require.NoError(t, err)
	assert.Equal(t, "CONFIRMED", again.Status, "resubmitting a confirmed account is a harmless no-op")
}

func TestRegistrationAPI_RejectsInvalidSignatureAndReportsLoser(t *testing.T) {
	h := newRegAPIHarness(true)
	addr, sig, _ := h.sign(t)

	bad := append(hexutil.Bytes(nil), sig...)
	bad[10] ^= 0xFF
	_, err := h.api.RegisterAccount(addr, bad)
	assert.Error(t, err)
	_, err = h.api.RegisterAccount(addr, sig[:60])
	assert.Error(t, err)

	winner := bls.GenerateKeyPair().PublicKey()
	h.parent.registry[addr] = winner
	_, err = h.api.RegisterAccount(addr, sig)
	require.NoError(t, err)
	h.api.App.regRelay.ProcessBatch()
	st, err := h.api.GetRegistrationStatus(addr)
	require.NoError(t, err)
	assert.Equal(t, "REJECTED", st.Status)
	assert.Equal(t, "0x"+common.Bytes2Hex(winner.Bytes()), st.HomeCluster)
	assert.Equal(t, 0, h.parent.sends)
}
