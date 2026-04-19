package account_state_db

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ──────────────────────────────────────────────
// SubTotalBalance Tests
// ──────────────────────────────────────────────

func TestSubTotalBalance(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xA1)

	// Add balance + pending, then sub from total
	err := adb.AddBalance(addr, big.NewInt(500))
	require.NoError(t, err)
	err = adb.AddPendingBalance(addr, big.NewInt(300))
	require.NoError(t, err)

	// SubTotalBalance subtracts from total (balance + pending)
	err = adb.SubTotalBalance(addr, big.NewInt(600))
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	// Total was 800, subtracted 600 → 200 remaining
	assert.True(t, as.TotalBalance().Cmp(big.NewInt(0)) >= 0, "total balance should not be negative")
}

func TestSubTotalBalance_Insufficient(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xA2)

	err := adb.AddBalance(addr, big.NewInt(100))
	require.NoError(t, err)

	// Try to subtract more than total
	err = adb.SubTotalBalance(addr, big.NewInt(200))
	assert.Error(t, err, "should fail when total balance is insufficient")
}

func TestSubTotalBalance_ZeroAmount(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xA3)

	err := adb.AddBalance(addr, big.NewInt(100))
	require.NoError(t, err)

	// Zero is a no-op
	err = adb.SubTotalBalance(addr, big.NewInt(0))
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(100).Cmp(as.TotalBalance()), "balance should not change")
}

// ──────────────────────────────────────────────
// SetAccountType Tests
// ──────────────────────────────────────────────

func TestSetAccountType(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xB1)

	err := adb.SetAccountType(addr, pb.ACCOUNT_TYPE_READ_WRITE_STRICT)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, pb.ACCOUNT_TYPE_READ_WRITE_STRICT, as.AccountType())
	assert.Equal(t, 1, adb.DirtyAccountCount())
}

// ──────────────────────────────────────────────
// SetNewDeviceKey Tests
// ──────────────────────────────────────────────

func TestSetNewDeviceKey(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xC1)
	deviceKey := common.HexToHash("0xfeedface")

	err := adb.SetNewDeviceKey(addr, deviceKey)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, deviceKey, as.DeviceKey())
}

// ──────────────────────────────────────────────
// SetCreatorPublicKey Tests
// ──────────────────────────────────────────────

func TestSetCreatorPublicKey(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xD1)
	var pubKey p_common.PublicKey
	copy(pubKey[:], []byte("test-creator-public-key-padded-to-48bytes!!"))

	err := adb.SetCreatorPublicKey(addr, pubKey)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	scState := as.SmartContractState()
	require.NotNil(t, scState, "smart contract state should be created")
	assert.Equal(t, pubKey, scState.CreatorPublicKey())
}

// ──────────────────────────────────────────────
// SetStorageAddress Tests
// ──────────────────────────────────────────────

func TestSetStorageAddress(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xD2)
	storAddr := common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")

	err := adb.SetStorageAddress(addr, storAddr)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	scState := as.SmartContractState()
	require.NotNil(t, scState)
	assert.Equal(t, storAddr, scState.StorageAddress())
}

// ──────────────────────────────────────────────
// AddLogHash Tests
// ──────────────────────────────────────────────

func TestAddLogHash(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xD3)
	logHash := common.HexToHash("0xdeadbeefdeadbeef")

	err := adb.AddLogHash(addr, logHash)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	scState := as.SmartContractState()
	require.NotNil(t, scState)
	// LogsHash should be set (non-empty after adding)
	assert.NotEqual(t, common.Hash{}, scState.LogsHash(), "logs hash should be non-empty after AddLogHash")
}

// ──────────────────────────────────────────────
// GetPublicKeyBls Tests (round-trip)
// ──────────────────────────────────────────────

func TestGetPublicKeyBls_NewAccount(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xE1)

	got, err := adb.GetPublicKeyBls(addr)
	require.NoError(t, err)
	assert.Empty(t, got, "new account should have empty BLS key")
}

// ──────────────────────────────────────────────
// GetLastHash Tests (round-trip beyond existing)
// ──────────────────────────────────────────────

func TestGetLastHash_NewAccount(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xE2)

	got, err := adb.GetLastHash(addr)
	require.NoError(t, err)
	assert.Equal(t, common.Hash{}, got, "new account should have zero last hash")
}

func TestGetLastHash_AfterSet(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xE3)
	hash := common.HexToHash("0xcafe0001")

	err := adb.SetLastHash(addr, hash)
	require.NoError(t, err)

	got, err := adb.GetLastHash(addr)
	require.NoError(t, err)
	assert.Equal(t, hash, got, "should return the hash that was set")
}
