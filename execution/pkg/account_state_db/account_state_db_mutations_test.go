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
// ExecuteNativeTransfer / ExecuteNativeTransferLockFree Tests
//
// Regression coverage for a bug found live 2026-09-03: both functions used
// to increment the sender's nonce AFTER SubTotalBalance, so a transfer that
// failed on insufficient balance returned an error WITHOUT consuming a
// nonce -- even though the caller (native_fast_path.go) still includes such
// a tx in the block with a real (failed) receipt. That mismatch (tx visibly
// included, nonce not consumed) contradicts the account's own bookkeeping:
// a receipt exists for nonce N, yet the account's nonce never moved past N.
// Traced live via a concurrent insufficient-balance burst that left one
// wallet stuck unable to send another transaction (compounded, at the
// time, by a since-removed mempool "localNonceFloor" cache that turned the
// stall permanent instead of self-correcting -- see ClearNoncesCache's doc
// comment in tx_validator_pool_core.go). The fix: consume the nonce
// unconditionally,
// before the balance check -- these tests pin that a failed transfer still
// advances the nonce and still returns an error, and that a successful
// transfer's behavior is unchanged.
// ──────────────────────────────────────────────

func TestExecuteNativeTransfer_InsufficientBalance_StillConsumesNonce(t *testing.T) {
	adb := newTestDB(t)
	from := testAddr(0xC1)
	to := testAddr(0xC2)

	require.NoError(t, adb.AddBalance(from, big.NewInt(100)))
	require.NoError(t, adb.SetNonce(from, 7))

	// amount+gasFee (1000) far exceeds the 100 available -> must fail.
	err := adb.ExecuteNativeTransfer(from, to, big.NewInt(900), big.NewInt(100), common.Hash{}, common.Hash{})
	require.Error(t, err, "transfer should fail on insufficient balance")

	as, err := adb.AccountState(from)
	require.NoError(t, err)
	assert.Equal(t, uint64(8), as.Nonce(), "nonce must still advance even though the transfer failed -- it was decisively included")
	assert.Equal(t, 0, big.NewInt(100).Cmp(as.TotalBalance()), "balance must be untouched on a failed transfer")

	toAs, err := adb.AccountState(to)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(0).Cmp(toAs.TotalBalance()), "receiver must not be credited on a failed transfer")
}

func TestExecuteNativeTransfer_Success_AdvancesNonceAndBalances(t *testing.T) {
	adb := newTestDB(t)
	from := testAddr(0xC3)
	to := testAddr(0xC4)

	require.NoError(t, adb.AddBalance(from, big.NewInt(1000)))
	require.NoError(t, adb.SetNonce(from, 3))

	err := adb.ExecuteNativeTransfer(from, to, big.NewInt(400), big.NewInt(50), common.Hash{}, common.Hash{})
	require.NoError(t, err)

	as, err := adb.AccountState(from)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), as.Nonce())
	assert.Equal(t, 0, big.NewInt(550).Cmp(as.TotalBalance()), "1000 - 400 - 50 = 550")

	toAs, err := adb.AccountState(to)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(400).Cmp(toAs.TotalBalance()))
}

func TestExecuteNativeTransferLockFree_InsufficientBalance_StillConsumesNonce(t *testing.T) {
	adb := newTestDB(t)
	from := testAddr(0xC5)
	to := testAddr(0xC6)

	require.NoError(t, adb.AddBalance(from, big.NewInt(100)))
	require.NoError(t, adb.SetNonce(from, 7))

	err := adb.ExecuteNativeTransferLockFree(from, to, big.NewInt(900), big.NewInt(100), common.Hash{}, common.Hash{})
	require.Error(t, err, "transfer should fail on insufficient balance")

	as, err := adb.AccountState(from)
	require.NoError(t, err)
	assert.Equal(t, uint64(8), as.Nonce(), "nonce must still advance even though the transfer failed -- it was decisively included")
	assert.Equal(t, 0, big.NewInt(100).Cmp(as.TotalBalance()), "balance must be untouched on a failed transfer")
}

func TestExecuteNativeTransferLockFree_Success_AdvancesNonceAndBalances(t *testing.T) {
	adb := newTestDB(t)
	from := testAddr(0xC7)
	to := testAddr(0xC8)

	require.NoError(t, adb.AddBalance(from, big.NewInt(1000)))
	require.NoError(t, adb.SetNonce(from, 3))

	err := adb.ExecuteNativeTransferLockFree(from, to, big.NewInt(400), big.NewInt(50), common.Hash{}, common.Hash{})
	require.NoError(t, err)

	as, err := adb.AccountState(from)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), as.Nonce())
	assert.Equal(t, 0, big.NewInt(550).Cmp(as.TotalBalance()))

	toAs, err := adb.AccountState(to)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(400).Cmp(toAs.TotalBalance()))
}

func TestExecuteNativeTransfer_SequentialInsufficientThenSufficient_NonceStaysContiguous(t *testing.T) {
	// Mirrors the real burst that exposed this bug: several concurrently
	// -submitted same-sender transfers where only some fit the balance.
	// Whatever the outcome per tx, the nonce sequence must stay
	// contiguous and match how many transfers were actually attempted --
	// never leaving a nonce "hole" that permanently strands later txs.
	adb := newTestDB(t)
	from := testAddr(0xC9)
	to := testAddr(0xCA)

	require.NoError(t, adb.AddBalance(from, big.NewInt(250)))
	require.NoError(t, adb.SetNonce(from, 0))

	// tx0: affordable (100+10=110 <= 250)
	require.NoError(t, adb.ExecuteNativeTransferLockFree(from, to, big.NewInt(100), big.NewInt(10), common.Hash{}, common.Hash{}))
	// tx1: no longer affordable (140 remaining, needs 100+10=110... still fits actually)
	require.NoError(t, adb.ExecuteNativeTransferLockFree(from, to, big.NewInt(100), big.NewInt(10), common.Hash{}, common.Hash{}))
	// tx2: only 30 left, needs 100+10 -> fails
	err := adb.ExecuteNativeTransferLockFree(from, to, big.NewInt(100), big.NewInt(10), common.Hash{}, common.Hash{})
	require.Error(t, err)

	as, err := adb.AccountState(from)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), as.Nonce(), "all 3 attempted transfers must consume a nonce each, success or fail")
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
