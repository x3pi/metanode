package account_state_db

import (
	"math/big"
	"os"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// TestMain forces the state backend to MPT for unit tests.
// The default backend is NOMT which requires FFI initialization (InitNomtDB).
// MPT works purely in-memory and is appropriate for testing AccountStateDB logic.
func TestMain(m *testing.M) {
	p_trie.SetStateBackend(p_trie.BackendMPT)
	os.Exit(m.Run())
}

// testMemoryDB wraps MemoryDB to satisfy the full Storage interface for tests.
type testMemoryDB struct {
	*storage.MemoryDB
}

func newTestMemoryDB() *testMemoryDB {
	return &testMemoryDB{MemoryDB: storage.NewMemoryDb()}
}

func (t *testMemoryDB) GetBackupPath() string           { return "" }
func (db *testMemoryDB) BatchDelete(keys [][]byte) error {
	return nil
}
func (db *testMemoryDB) Flush() error {
	return nil
}

// newTestDB creates an AccountStateDB backed by in-memory storage for tests.
func newTestDB(t *testing.T) *AccountStateDB {
	t.Helper()
	db := newTestMemoryDB()
	tr, err := p_trie.New(common.Hash{}, db, true)
	require.NoError(t, err, "failed to create test trie")
	adb := NewAccountStateDB(tr, db)
	require.NotNil(t, adb, "NewAccountStateDB should not return nil")
	return adb
}

func testAddr(b byte) common.Address {
	var addr common.Address
	addr[0] = b
	return addr
}

// ──────────────────────────────────────────────
// Constructor Tests
// ──────────────────────────────────────────────

func TestNewAccountStateDB_Valid(t *testing.T) {
	adb := newTestDB(t)
	assert.NotNil(t, adb)
	assert.Equal(t, 0, adb.DirtyAccountCount())
}

func TestNewAccountStateDB_NilDB(t *testing.T) {
	tr, err := p_trie.New(common.Hash{}, newTestMemoryDB(), true)
	require.NoError(t, err)
	adb := NewAccountStateDB(tr, nil)
	assert.Nil(t, adb, "should return nil when db is nil")
}

// ──────────────────────────────────────────────
// Read/Write Tests
// ──────────────────────────────────────────────

func TestAccountState_NewAccount(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x01)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	require.NotNil(t, as)
	assert.Equal(t, big.NewInt(0).Cmp(as.TotalBalance()), 0, "new account balance should be 0")
	assert.Equal(t, uint64(0), as.Nonce())
}

func TestAccountStateReadOnly_NewAccount(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x02)

	as, err := adb.AccountStateReadOnly(addr)
	require.NoError(t, err)
	require.NotNil(t, as)
	// ReadOnly should NOT pollute dirty cache
	assert.Equal(t, 0, adb.DirtyAccountCount(), "ReadOnly should not create dirty entry")
}

func TestAccountStateReadOnly_NilDB(t *testing.T) {
	var adb *AccountStateDB
	_, err := adb.AccountStateReadOnly(testAddr(0x01))
	assert.Error(t, err, "should return error on nil db")
}

// ──────────────────────────────────────────────
// Mutation Tests
// ──────────────────────────────────────────────

func TestAddBalance(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x10)

	err := adb.AddBalance(addr, big.NewInt(1000))
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(1000).Cmp(as.TotalBalance()), "balance should be 1000")
	assert.Equal(t, 1, adb.DirtyAccountCount(), "should have 1 dirty account")
}

func TestAddBalance_ZeroAmount(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x11)

	err := adb.AddBalance(addr, big.NewInt(0))
	require.NoError(t, err)
	// Zero amount is a no-op
	assert.Equal(t, 0, adb.DirtyAccountCount())
}

func TestSubBalance(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x20)

	// Add first, then subtract
	err := adb.AddBalance(addr, big.NewInt(500))
	require.NoError(t, err)

	err = adb.SubBalance(addr, big.NewInt(200))
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(300).Cmp(as.TotalBalance()), "balance should be 300 after sub")
}

func TestSubBalance_Insufficient(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x21)

	err := adb.AddBalance(addr, big.NewInt(100))
	require.NoError(t, err)

	err = adb.SubBalance(addr, big.NewInt(200))
	assert.Error(t, err, "should fail when subtracting more than balance")
}

func TestPlusOneNonce(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x30)

	err := adb.PlusOneNonce(addr)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), as.Nonce())

	err = adb.PlusOneNonce(addr)
	require.NoError(t, err)

	as, err = adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), as.Nonce())
}

func TestSetNonce(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x31)

	err := adb.SetNonce(addr, 42)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), as.Nonce())
}

func TestSetLastHash(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x32)
	hash := common.HexToHash("0xdeadbeef")

	err := adb.SetLastHash(addr, hash)
	require.NoError(t, err)

	got, err := adb.GetLastHash(addr)
	require.NoError(t, err)
	assert.Equal(t, hash, got)
}

func TestSetPublicKeyBls(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x33)
	blsKey := make([]byte, 48)
	copy(blsKey, []byte("test-bls-public-key-data-padded-to-48-bytes!!!!!"))

	err := adb.SetPublicKeyBls(addr, blsKey)
	require.NoError(t, err)

	got, err := adb.GetPublicKeyBls(addr)
	require.NoError(t, err)
	assert.Equal(t, blsKey, got)
}

func TestAddPendingBalance(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x40)

	err := adb.AddPendingBalance(addr, big.NewInt(500))
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(500).Cmp(as.PendingBalance()))
}

func TestSubPendingBalance(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x41)

	err := adb.AddPendingBalance(addr, big.NewInt(500))
	require.NoError(t, err)

	err = adb.SubPendingBalance(addr, big.NewInt(200))
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(300).Cmp(as.PendingBalance()))
}

func TestRefreshPendingBalance(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x42)

	// Add balance + pending
	err := adb.AddBalance(addr, big.NewInt(1000))
	require.NoError(t, err)
	err = adb.AddPendingBalance(addr, big.NewInt(300))
	require.NoError(t, err)

	// Refresh should move pending → balance
	err = adb.RefreshPendingBalance(addr)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(0).Cmp(as.PendingBalance()), "pending should be 0 after refresh")
	assert.Equal(t, 0, big.NewInt(1300).Cmp(as.TotalBalance()), "balance should include refreshed pending")
}

func TestSetCodeHash(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x50)
	codeHash := common.HexToHash("0xabcdef")

	err := adb.SetCodeHash(addr, codeHash)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	scState := as.SmartContractState()
	require.NotNil(t, scState)
	assert.Equal(t, codeHash, scState.CodeHash())
}

func TestSetStorageRoot(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0x51)
	root := common.HexToHash("0x123456")

	err := adb.SetStorageRoot(addr, root)
	require.NoError(t, err)

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	scState := as.SmartContractState()
	require.NotNil(t, scState)
	assert.Equal(t, root, scState.StorageRoot())
}

// ──────────────────────────────────────────────
// Lock Flag Guard Tests
// ──────────────────────────────────────────────

func TestLockedFlag_ReturnsError(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xA0)

	// Simulate locked state (as during IntermediateRoot → Commit)
	adb.lockedFlag.Store(true)

	tests := []struct {
		name string
		fn   func() error
	}{
		{"AddBalance", func() error { return adb.AddBalance(addr, big.NewInt(1)) }},
		{"SubBalance", func() error { return adb.SubBalance(addr, big.NewInt(1)) }},
		{"SubTotalBalance", func() error { return adb.SubTotalBalance(addr, big.NewInt(1)) }},
		{"SubPendingBalance", func() error { return adb.SubPendingBalance(addr, big.NewInt(1)) }},
		{"AddPendingBalance", func() error { return adb.AddPendingBalance(addr, big.NewInt(1)) }},
		{"RefreshPendingBalance", func() error { return adb.RefreshPendingBalance(addr) }},
		{"PlusOneNonce", func() error { return adb.PlusOneNonce(addr) }},
		{"SetNonce", func() error { return adb.SetNonce(addr, 1) }},
		{"SetLastHash", func() error { return adb.SetLastHash(addr, common.Hash{}) }},
		{"SetNewDeviceKey", func() error { return adb.SetNewDeviceKey(addr, common.Hash{}) }},
		{"SetPublicKeyBls", func() error { return adb.SetPublicKeyBls(addr, []byte{}) }},
		{"SetCodeHash", func() error { return adb.SetCodeHash(addr, common.Hash{}) }},
		{"SetStorageRoot", func() error { return adb.SetStorageRoot(addr, common.Hash{}) }},
		{"SetStorageAddress", func() error { return adb.SetStorageAddress(addr, common.Address{}) }},
		{"ReloadTrie", func() error { return adb.ReloadTrie(common.Hash{}) }},
		{"Discard", func() error { return adb.Discard() }},
		{"SetCreatorPublicKey", func() error { return adb.SetCreatorPublicKey(addr, [48]byte{}) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			assert.Error(t, err, "%s should return error when locked", tt.name)
			assert.Contains(t, err.Error(), "locked", "%s error should mention locked", tt.name)
		})
	}

	// Test methods that return (value, error) with different signatures
	t.Run("GetPublicKeyBls", func(t *testing.T) {
		_, err := adb.GetPublicKeyBls(addr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "locked")
	})

	t.Run("GetLastHash", func(t *testing.T) {
		_, err := adb.GetLastHash(addr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "locked")
	})

	// Commit/CommitPipeline require lockedFlag=false (opposite check)
	adb.lockedFlag.Store(false)

	t.Run("Commit_NotLocked", func(t *testing.T) {
		_, err := adb.Commit()
		assert.Error(t, err, "Commit should fail when NOT locked")
		assert.Contains(t, err.Error(), "locked")
	})

	t.Run("CommitPipeline_NotLocked", func(t *testing.T) {
		_, err := adb.CommitPipeline()
		assert.Error(t, err, "CommitPipeline should fail when NOT locked")
		assert.Contains(t, err.Error(), "locked")
	})
}

// ──────────────────────────────────────────────
// Commit & Discard Tests
// ──────────────────────────────────────────────

func TestIntermediateRoot_And_Commit(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xB0)

	origHash := adb.GetOriginRootHash()

	// Make changes
	err := adb.AddBalance(addr, big.NewInt(999))
	require.NoError(t, err)

	// IntermediateRoot should lock and compute new hash
	irHash, err := adb.IntermediateRoot(true)
	require.NoError(t, err)
	assert.NotEqual(t, origHash, irHash, "intermediate root should differ after changes")

	// Commit should persist and produce the same (or updated) hash
	commitHash, err := adb.Commit()
	require.NoError(t, err)
	assert.NotEqual(t, common.Hash{}, commitHash, "commit hash should not be empty")

	// Origin root should be updated after commit
	assert.Equal(t, commitHash, adb.GetOriginRootHash())
}

func TestDiscard(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xC0)

	origHash := adb.GetOriginRootHash()

	// Make changes
	err := adb.AddBalance(addr, big.NewInt(500))
	require.NoError(t, err)
	assert.Equal(t, 1, adb.DirtyAccountCount())

	// Discard changes
	err = adb.Discard()
	require.NoError(t, err)
	assert.Equal(t, 0, adb.DirtyAccountCount(), "dirty count should be 0 after discard")
	assert.Equal(t, origHash, adb.GetOriginRootHash(), "origin hash should not change after discard")
}

// ──────────────────────────────────────────────
// Concurrent Access Tests
// ──────────────────────────────────────────────

func TestConcurrent_AddBalance(t *testing.T) {
	adb := newTestDB(t)

	const numGoroutines = 50
	const balancePerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			addr := testAddr(byte(idx)) // Different addresses → no contention on same account
			err := adb.AddBalance(addr, big.NewInt(int64(balancePerGoroutine)))
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()
	assert.Equal(t, numGoroutines, adb.DirtyAccountCount())
}

func TestConcurrent_SameAccount(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xFF) // Same address for all goroutines

	const numGoroutines = 20

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := adb.AddBalance(addr, big.NewInt(10))
			assert.NoError(t, err)
		}()
	}

	wg.Wait()

	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	expected := big.NewInt(int64(numGoroutines * 10))
	assert.Equal(t, 0, expected.Cmp(as.TotalBalance()),
		"balance should be %d, got %s", expected, as.TotalBalance())
}

// ──────────────────────────────────────────────
// Utility Tests
// ──────────────────────────────────────────────

func TestDirtyContentHash_Deterministic(t *testing.T) {
	adb := newTestDB(t)
	addr1 := testAddr(0xD0)
	addr2 := testAddr(0xD1)

	err := adb.AddBalance(addr1, big.NewInt(100))
	require.NoError(t, err)
	err = adb.AddBalance(addr2, big.NewInt(200))
	require.NoError(t, err)

	hash1 := adb.DirtyContentHash()
	hash2 := adb.DirtyContentHash()
	assert.Equal(t, hash1, hash2, "DirtyContentHash should be deterministic")
	assert.NotEqual(t, common.Hash{}, hash1, "hash should not be empty")
}

func TestSetState(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xE0)

	as := adb.NewAccountState(addr)
	as.AddBalance(big.NewInt(777))

	adb.SetState(as)
	assert.Equal(t, 1, adb.DirtyAccountCount())

	got, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(777).Cmp(got.TotalBalance()))
}

func TestSetState_Nil(t *testing.T) {
	adb := newTestDB(t)
	adb.SetState(nil) // Should not panic
	assert.Equal(t, 0, adb.DirtyAccountCount())
}

func TestAccountBatch(t *testing.T) {
	adb := newTestDB(t)

	// Initially nil
	assert.Nil(t, adb.GetAccountBatch())

	adb.SetAccountBatch([]byte("test-batch-data"))
	got := adb.GetAccountBatch()
	assert.Equal(t, []byte("test-batch-data"), got)

	adb.ClearAccountBatch()
	assert.Nil(t, adb.GetAccountBatch())
}
