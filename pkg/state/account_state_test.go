package state

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// AccountState tests — constructor, getters/setters, balance ops, serialization
// ══════════════════════════════════════════════════════════════════════════════

var testAddr = common.HexToAddress("0x1111111111111111111111111111111111111111")

// ---------- Constructor ----------

func TestNewAccountState_Defaults(t *testing.T) {
	as := NewAccountState(testAddr)
	assert.Equal(t, testAddr, as.Address())
	assert.Equal(t, big.NewInt(0), as.Balance())
	assert.Equal(t, big.NewInt(0), as.PendingBalance())
	assert.Equal(t, uint64(0), as.Nonce())
	assert.False(t, as.IsDirty())
}

// ---------- Balance operations ----------

func TestAccountState_AddBalance(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(1000))
	assert.Equal(t, big.NewInt(1000), as.Balance())
	assert.True(t, as.IsDirty())
}

func TestAccountState_SubBalance_OK(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(1000))
	err := as.SubBalance(big.NewInt(300))
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(700), as.Balance())
}

func TestAccountState_SubBalance_Insufficient(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(100))
	err := as.SubBalance(big.NewInt(200))
	assert.ErrorIs(t, err, ErrorInvalidSubBalanceAmount)
	assert.Equal(t, big.NewInt(100), as.Balance()) // unchanged
}

func TestAccountState_AddPendingBalance(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddPendingBalance(big.NewInt(500))
	assert.Equal(t, big.NewInt(500), as.PendingBalance())
}

func TestAccountState_SubPendingBalance_OK(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddPendingBalance(big.NewInt(500))
	err := as.SubPendingBalance(big.NewInt(200))
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(300), as.PendingBalance())
}

func TestAccountState_SubPendingBalance_Insufficient(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddPendingBalance(big.NewInt(100))
	err := as.SubPendingBalance(big.NewInt(200))
	assert.ErrorIs(t, err, ErrorInvalidSubPendingAmount)
}

func TestAccountState_TotalBalance(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(1000))
	as.AddPendingBalance(big.NewInt(500))
	assert.Equal(t, big.NewInt(1500), as.TotalBalance())
}

func TestAccountState_SubTotalBalance(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(1000))
	as.AddPendingBalance(big.NewInt(500))

	err := as.SubTotalBalance(big.NewInt(1200))
	require.NoError(t, err)
	// pendingBalance becomes 0, balance = 1500 - 1200 = 300
	assert.Equal(t, big.NewInt(0), as.PendingBalance())
	assert.Equal(t, big.NewInt(300), as.Balance())
}

func TestAccountState_SubTotalBalance_Insufficient(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(100))
	err := as.SubTotalBalance(big.NewInt(200))
	assert.Error(t, err)
}

// ---------- Nonce ----------

func TestAccountState_Nonce(t *testing.T) {
	as := NewAccountState(testAddr)
	assert.Equal(t, uint64(0), as.Nonce())
	as.SetNonce(5)
	assert.Equal(t, uint64(5), as.Nonce())
}

func TestAccountState_PlusOneNonce(t *testing.T) {
	as := NewAccountState(testAddr)
	as.SetNonce(10)
	as.PlusOneNonce()
	assert.Equal(t, uint64(11), as.Nonce())
}

// ---------- Setters ----------

func TestAccountState_SetLastHash(t *testing.T) {
	as := NewAccountState(testAddr)
	h := common.HexToHash("0xabcdef")
	as.SetLastHash(h)
	assert.Equal(t, h, as.LastHash())
}

func TestAccountState_SetNewDeviceKey(t *testing.T) {
	as := NewAccountState(testAddr)
	dk := common.HexToHash("0x1234")
	as.SetNewDeviceKey(dk)
	assert.Equal(t, dk, as.DeviceKey())
}

func TestAccountState_SetAccountType_Valid(t *testing.T) {
	as := NewAccountState(testAddr)
	err := as.SetAccountType(pb.ACCOUNT_TYPE_REGULAR_ACCOUNT)
	require.NoError(t, err)
	assert.Equal(t, pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, as.AccountType())
}

func TestAccountState_SetAccountType_Invalid(t *testing.T) {
	as := NewAccountState(testAddr)
	err := as.SetAccountType(pb.ACCOUNT_TYPE(999))
	assert.Error(t, err)
}

func TestAccountState_SetPublicKeyBls_Valid(t *testing.T) {
	as := NewAccountState(testAddr)
	key := make([]byte, 48)
	key[0] = 0xAA
	err := as.SetPublicKeyBls(key)
	require.NoError(t, err)
	assert.Equal(t, key, as.PublicKeyBls())
}

func TestAccountState_SetPublicKeyBls_InvalidLength(t *testing.T) {
	as := NewAccountState(testAddr)
	err := as.SetPublicKeyBls([]byte{1, 2, 3})
	assert.Error(t, err)
}

func TestAccountState_SetPublicKeyBls_AlreadySet(t *testing.T) {
	as := NewAccountState(testAddr)
	key := make([]byte, 48)
	as.SetPublicKeyBls(key)
	err := as.SetPublicKeyBls(make([]byte, 48))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already set")
}

// ---------- SmartContractState ----------

func TestAccountState_GetOrCreateSmartContractState(t *testing.T) {
	as := NewAccountState(testAddr).(*AccountState)
	assert.Nil(t, as.SmartContractState())

	sc := as.GetOrCreateSmartContractState()
	assert.NotNil(t, sc)
	assert.NotNil(t, as.SmartContractState())
}

func TestAccountState_SetStorageAddress(t *testing.T) {
	as := NewAccountState(testAddr)
	addr := common.HexToAddress("0x2222")
	as.SetStorageAddress(addr)
	assert.Equal(t, addr, as.SmartContractState().StorageAddress())
}

func TestAccountState_SetCodeHash(t *testing.T) {
	as := NewAccountState(testAddr)
	h := common.HexToHash("0xdeadbeef")
	as.SetCodeHash(h)
	assert.Equal(t, h, as.SmartContractState().CodeHash())
}

// ---------- Serialization ----------

func TestAccountState_MarshalUnmarshal_RoundTrip(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(42000))
	as.AddPendingBalance(big.NewInt(100))
	as.SetNonce(7)
	as.SetLastHash(common.HexToHash("0xface"))
	as.SetNewDeviceKey(common.HexToHash("0xcafe"))

	data, err := as.Marshal()
	require.NoError(t, err)

	as2 := &AccountState{}
	err = as2.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, as.Address(), as2.Address())
	assert.Equal(t, as.Balance(), as2.Balance())
	assert.Equal(t, as.PendingBalance(), as2.PendingBalance())
	assert.Equal(t, as.Nonce(), as2.Nonce())
	assert.Equal(t, as.LastHash(), as2.LastHash())
	assert.Equal(t, as.DeviceKey(), as2.DeviceKey())
}

// ---------- Copy ----------

func TestAccountState_Copy_Independent(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(1000))

	cp := as.Copy()
	cp.AddBalance(big.NewInt(500))

	assert.Equal(t, big.NewInt(1000), as.Balance())
	assert.Equal(t, big.NewInt(1500), cp.Balance())
}

// ---------- String ----------

func TestAccountState_String(t *testing.T) {
	as := NewAccountState(testAddr)
	s := as.String()
	assert.Contains(t, s, "address")
	assert.Contains(t, s, "balance")
}

// ---------- IsDirty / MarkDirty ----------

func TestAccountState_IsDirty(t *testing.T) {
	as := NewAccountState(testAddr)
	assert.False(t, as.IsDirty())
	as.MarkDirty()
	assert.True(t, as.IsDirty())
}

// ---------- JSON roundtrip ----------

func TestJsonAccountState_RoundTrip(t *testing.T) {
	as := &AccountState{
		address:        testAddr,
		balance:        big.NewInt(9999),
		pendingBalance: big.NewInt(100),
		nonce:          5,
	}

	j := &JsonAccountState{}
	j.FromAccountState(as)

	as2 := j.ToAccountState()
	assert.Equal(t, as.address, as2.address)
	assert.Equal(t, as.balance, as2.balance)
	assert.Equal(t, as.nonce, as2.nonce)
}

// ---------- MarshalAccountStateWithIdRequest ----------

func TestMarshalUnmarshal_AccountStateWithIdRequest(t *testing.T) {
	as := NewAccountState(testAddr)
	as.AddBalance(big.NewInt(500))
	id := "req-123"

	data, err := MarshalAccountStateWithIdRequest(as, id)
	require.NoError(t, err)

	as2, id2, err := UnmarshalAccountStateWithIdRequest(data)
	require.NoError(t, err)
	assert.Equal(t, id, id2)
	assert.Equal(t, as.Address(), as2.Address())
}

func TestMarshalUnmarshal_GetAccountStateWithIdRequest(t *testing.T) {
	addr := testAddr
	id := "lookup-001"

	data, err := MarshalGetAccountStateWithIdRequest(addr, id)
	require.NoError(t, err)

	addr2, id2, err := UnmarshalGetAccountStateWithIdRequest(data)
	require.NoError(t, err)
	assert.Equal(t, addr, addr2)
	assert.Equal(t, id, id2)
}

// ---------- MarshalSCStatesWithBlockNumber ----------

func TestMarshalUnmarshal_SCStatesWithBlockNumber(t *testing.T) {
	addr := common.HexToAddress("0x3333")
	scState := NewEmptySmartContractState()
	scState.SetCodeHash(common.HexToHash("0xabc"))

	states := map[common.Address]types.SmartContractState{
		addr: scState,
	}

	data, err := MarshalSCStatesWithBlockNumber(states, 99)
	require.NoError(t, err)

	decoded, bn, err := UnmarshalSCStatesWithBlockNumber(data)
	require.NoError(t, err)
	assert.Equal(t, uint64(99), bn)
	assert.Equal(t, 1, len(decoded))
}
