package parentchain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

var oneUnit = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil) // 1 whole unit = 10^18 base units

func units(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), oneUnit) }

func floatOf(t *testing.T, s Store, k cm.PublicKey) *big.Int {
	t.Helper()
	b, err := s.GetFloat(crypto.Keccak256Hash(k[:]))
	require.NoError(t, err)
	return b
}

func restorePolicy(t *testing.T) {
	t.Cleanup(func() { SetClusterPolicy(ClusterPolicy{Open: true, AllowDeposit: true}) })
}

func transferTx(t *testing.T, from *bls.KeyPair, to cm.PublicKey, amount *big.Int, nonce uint64) *Receipt {
	t.Helper()
	tx, err := BuildAndSignBLSTx(from.PrivateKey(), from.PublicKey(), ParentChainGatewayAddress, nonce, EncodeTransferBalanceCallData(to, amount))
	require.NoError(t, err)
	rcpt, _ := ExecuteTx(storeUnderTest, tx, 1)
	return rcpt
}

var storeUnderTest Store

// Genesis float: balances exist after ApplyGenesisFloat, the supply equals their sum, the invariant holds and
// every holder can sign its own transactions (AccountRegistry).
func TestGenesisFloat_AppliesBalancesAndSupply(t *testing.T) {
	a, b := bls.GenerateKeyPair(), bls.GenerateKeyPair()
	store := NewMemoryStore()
	require.NoError(t, ApplyGenesisFloat(store, []FloatAllocation{
		{Key: a.PublicKey(), Balance: units(1500)},
		{Key: b.PublicKey(), Balance: units(10)},
	}))
	assert.Equal(t, units(1500), floatOf(t, store, a.PublicKey()))
	assert.Equal(t, units(10), floatOf(t, store, b.PublicKey()))
	total, _ := store.GetFloat(floatTotalSupplyKey)
	assert.Equal(t, units(1510), total)
	require.NoError(t, CheckFloatSupplyInvariant(store))
	got, found, _ := store.GetAccountRegistry(a.Address())
	assert.True(t, found)
	assert.Equal(t, a.PublicKey(), got)
}

func TestGenesis_ValidatesFloatAccounts(t *testing.T) {
	k := bls.GenerateKeyPair().PublicKey()
	hexKey := common.Bytes2Hex(k[:])
	_, err := (&Genesis{FloatAccounts: []GenesisFloatAccount{{BLSPublicKey: hexKey, Balance: "5"}, {BLSPublicKey: hexKey, Balance: "6"}}}).FloatAllocations()
	assert.ErrorIs(t, err, ErrGenesisInvalid, "duplicate key")
	_, err = (&Genesis{FloatAccounts: []GenesisFloatAccount{{BLSPublicKey: hexKey, Balance: "0"}}}).FloatAllocations()
	assert.ErrorIs(t, err, ErrGenesisInvalid, "zero balance")
	_, err = (&Genesis{FloatAccounts: []GenesisFloatAccount{{BLSPublicKey: "abcd", Balance: "5"}}}).FloatAllocations()
	assert.ErrorIs(t, err, ErrGenesisInvalid, "short key")
	_, err = (&Genesis{MinFloatToRegister: "-1"}).ClusterPolicy()
	assert.ErrorIs(t, err, ErrGenesisInvalid, "negative threshold")
	p, err := (&Genesis{MinFloatToRegister: units(1000).String()}).ClusterPolicy()
	require.NoError(t, err)
	assert.Equal(t, units(1000), p.MinFloat)
}

// Plain account-to-account transfer: moves balance, never changes the supply, rejects overdraft, replays and
// non-positive amounts, and lets the recipient sign its own next transaction.
func TestTransferBalance_ThroughExecuteTx(t *testing.T) {
	restorePolicy(t)
	SetClusterPolicy(ClusterPolicy{}) // closed, fixed supply: the production shape
	a, b := bls.GenerateKeyPair(), bls.GenerateKeyPair()
	store := NewMemoryStore()
	storeUnderTest = store
	require.NoError(t, ApplyGenesisFloat(store, []FloatAllocation{{Key: a.PublicKey(), Balance: units(1000)}}))

	r := transferTx(t, a, b.PublicKey(), units(400), 0)
	require.Equal(t, uint8(1), r.Status)
	assert.Equal(t, units(600), floatOf(t, store, a.PublicKey()))
	assert.Equal(t, units(400), floatOf(t, store, b.PublicKey()))
	require.NoError(t, CheckFloatSupplyInvariant(store), "supply is conserved and the recipient is counted")

	// Overdraft and replay are rejected; amount must be positive.
	r = transferTx(t, a, b.PublicKey(), units(601), 1)
	assert.Equal(t, uint8(0), r.Status)
	assert.Equal(t, uint32(224), uint32(r.ErrorCode))
	assert.Equal(t, units(600), floatOf(t, store, a.PublicKey()), "a failed transfer changes no balance")
	r = transferTx(t, a, b.PublicKey(), units(1), 0)
	assert.Equal(t, uint8(0), r.Status, "replayed nonce")
	r = transferTx(t, a, b.PublicKey(), big.NewInt(0), 2)
	assert.Equal(t, uint8(0), r.Status, "zero amount")

	// The recipient was never registered by a tx of its own, yet can now send (AccountRegistry set on receipt).
	c := bls.GenerateKeyPair()
	r = transferTx(t, b, c.PublicKey(), units(100), 0)
	require.Equal(t, uint8(1), r.Status)
	assert.Equal(t, units(100), floatOf(t, store, c.PublicKey()))
	require.NoError(t, CheckFloatSupplyInvariant(store))
}

// The cluster balance rule: founders always register; anyone else needs >= min_float_to_register; a key that
// cannot fund itself (no source) never qualifies.
func TestRegisterCluster_BalanceGate(t *testing.T) {
	restorePolicy(t)
	founder, rich, poor, stranger := bls.GenerateKeyPair(), bls.GenerateKeyPair(), bls.GenerateKeyPair(), bls.GenerateKeyPair()
	SetClusterPolicy(ClusterPolicy{
		Allowed:  map[cm.PublicKey]struct{}{founder.PublicKey(): {}},
		MinFloat: units(1000),
	})
	store := NewMemoryStore()
	storeUnderTest = store
	require.NoError(t, ApplyGenesisFloat(store, []FloatAllocation{
		{Key: rich.PublicKey(), Balance: units(1000)}, // exactly the threshold qualifies
		{Key: poor.PublicKey(), Balance: units(999)},
	}))
	register := func(kp *bls.KeyPair) *Receipt {
		tx, err := BuildAndSignBLSTx(kp.PrivateKey(), kp.PublicKey(), ParentChainGatewayAddress, 0, EncodeRegisterClusterCallData(kp.PublicKey(), 9))
		require.NoError(t, err)
		rcpt, _ := ExecuteTx(store, tx, 1)
		return rcpt
	}
	assert.Equal(t, uint8(1), register(founder).Status, "founding cluster")
	assert.Equal(t, uint8(1), register(rich).Status, "balance at the threshold")
	r := register(poor)
	assert.Equal(t, uint8(0), r.Status)
	assert.Equal(t, uint32(221), uint32(r.ErrorCode), "balance below the threshold")
	assert.Equal(t, uint32(221), uint32(register(stranger).ErrorCode), "no balance at all")

	e, found, _ := store.GetChainRegistry(crypto.Keccak256Hash(rich.PublicKey().Bytes()))
	require.True(t, found)
	assert.True(t, e.Authorized, "registerCluster marks the key as an authorized certifier")
}

// Production shape: the supply is fixed, depositToFloat (the only mint path) is rejected deterministically.
func TestDepositToFloat_DisabledByDefault(t *testing.T) {
	restorePolicy(t)
	SetClusterPolicy(ClusterPolicy{Allowed: map[cm.PublicKey]struct{}{}})
	kp := bls.GenerateKeyPair()
	store := NewMemoryStore()
	require.NoError(t, store.SetChainRegistry(crypto.Keccak256Hash(kp.PublicKey().Bytes()), ChainRegistryEntry{FloatIdentityKey: kp.PublicKey(), Authorized: true}))
	require.NoError(t, store.SetAccountRegistry(kp.Address(), kp.PublicKey()))
	msg := crypto.Keccak256Hash([]byte("m"))
	amt := big.NewInt(5)
	cert := bls.Sign(kp.PrivateKey(), ComputeDepositFloatMessage(kp.PublicKey(), 1, common.Address{}, common.Address{}, amt, msg))
	data := EncodeDepositToFloatCallData(kp.PublicKey(), kp.PublicKey(), 1, common.Address{}, common.Address{}, amt, msg, cert)
	tx, err := BuildAndSignBLSTx(kp.PrivateKey(), kp.PublicKey(), ParentChainGatewayAddress, 0, data)
	require.NoError(t, err)
	rcpt, err := ExecuteTx(store, tx, 1)
	assert.ErrorIs(t, err, ErrDepositDisabled)
	assert.Equal(t, uint32(222), uint32(rcpt.ErrorCode))
	total, _ := store.GetFloat(floatTotalSupplyKey)
	assert.Equal(t, 0, total.Sign(), "nothing was minted")
}

// Regression for the policy bypass: a key that merely RECEIVED float is not a trusted certifier, so it can neither
// certify a deposit, nor act as a TransferFloat source, nor submit a state root.
func TestReceivingFloatDoesNotMakeAKeyACertifier(t *testing.T) {
	store := NewMemoryStore()
	a, x, y := bls.GenerateKeyPair(), bls.GenerateKeyPair(), bls.GenerateKeyPair()
	require.NoError(t, registerAuthorizedCluster(store, crypto.Keccak256Hash(a.PublicKey().Bytes()), a.PublicKey(), 1))
	dep := func(src, dst *bls.KeyPair, id string, amt int64) error {
		msg := crypto.Keccak256Hash([]byte(id))
		amount := big.NewInt(amt)
		digest := ComputeDepositFloatMessage(dst.PublicKey(), 1, common.Address{}, common.Address{}, amount, msg)
		return DepositToFloat(store, src.PublicKey(), dst.PublicKey(), 1, common.Address{}, common.Address{}, amount, msg, bls.Sign(src.PrivateKey(), digest), 1)
	}
	require.NoError(t, dep(a, x, "a->x", 5)) // x now has a (lazily created, unauthorized) registry entry
	assert.ErrorIs(t, dep(x, y, "x->y", 1_000_000_000), ErrFloatUnknownSource, "a funded key must not mint")

	root := common.HexToHash("0x01")
	cert := bls.Sign(x.PrivateKey(), ComputeSubmitStateRootMessage(x.PublicKey(), 1, root))
	assert.Error(t, SubmitStateRoot(store, x.PublicKey(), 1, root, cert), "a funded key must not submit state roots")
}
