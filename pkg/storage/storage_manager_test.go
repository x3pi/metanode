package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// StorageManager tests
// ══════════════════════════════════════════════════════════════════════════════

// testStorage wraps MemoryDB and adds missing interface methods
// so it satisfies the full Storage interface.
type testStorage struct {
	*MemoryDB
}

func (ts *testStorage) BatchDelete(keys [][]byte) error {
	for _, k := range keys {
		ts.Delete(k)
	}
	return nil
}

func (ts *testStorage) Flush() error {
	return nil
}

func (ts *testStorage) GetBackupPath() string { return "" }

func newTestStorage() Storage {
	return &testStorage{MemoryDB: NewMemoryDb()}
}

// ---------- StorageType.String ----------

func TestStorageType_String_All(t *testing.T) {
	cases := []struct {
		st   StorageType
		want string
	}{
		{STORAGE_ACCOUNT, "ACCOUNT_DB"},
		{STORAGE_BACKUP_DEVICE_KEY, "STORAGE_BACKUP_DEVICE_KEY"},
		{STORAGE_RECEIPTS, "STORAGE_RECEIPTS"},
		{STORAGE_SMART_CONTRACT, "STORAGE_SMART_CONTRACT"},
		{STORAGE_CODE, "STORAGE_CODE"},
		{STORAGE_DATABASE_TRIE, "STORAGE_DATABASE_TRIE"},
		{STORAGE_BLOCK, "STORAGE_BLOCK"},
		{STORAGE_BACKUP_DB, "STORAGE_BACKUP_DB"},
		{STORAGE_MAPPING_DB, "STORAGE_MAPPING_DB"},
		{STORAGE_TRANSACTION, "STORAGE_TRANSACTION"},
		{STORAGE_STAKE, "STORAGE_STAKE"},
		{StorageType(999), "UNKNOWN"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.st.String())
	}
}

// ---------- AddStorage / GetStorage ----------

func TestStorageManager_AddAndGet(t *testing.T) {
	sm := NewStorageManager()
	s := newTestStorage()

	err := sm.AddStorage(STORAGE_ACCOUNT, s)
	require.NoError(t, err)

	got := sm.GetStorage(STORAGE_ACCOUNT)
	assert.Equal(t, s, got)
}

func TestStorageManager_GetStorage_Missing(t *testing.T) {
	sm := NewStorageManager()
	got := sm.GetStorage(STORAGE_BLOCK)
	assert.Nil(t, got)
}

func TestStorageManager_AddStorage_Duplicate(t *testing.T) {
	sm := NewStorageManager()
	err := sm.AddStorage(STORAGE_ACCOUNT, newTestStorage())
	require.NoError(t, err)

	err = sm.AddStorage(STORAGE_ACCOUNT, newTestStorage())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestStorageManager_AddStorage_InvalidType(t *testing.T) {
	sm := NewStorageManager()
	err := sm.AddStorage(StorageType(999), newTestStorage())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

// ---------- Typed helpers (spot-check) ----------

func TestStorageManager_TypedHelpers(t *testing.T) {
	sm := NewStorageManager()

	require.NoError(t, sm.AddStorageAccount(newTestStorage()))
	assert.NotNil(t, sm.GetStorageAccount())

	require.NoError(t, sm.AddStorageTransaction(newTestStorage()))
	assert.NotNil(t, sm.GetStorageTransaction())

	require.NoError(t, sm.AddStorageBlock(newTestStorage()))
	assert.NotNil(t, sm.GetStorageBlock())

	require.NoError(t, sm.AddStorageReceipt(newTestStorage()))
	assert.NotNil(t, sm.GetStorageReceipt())

	require.NoError(t, sm.AddStorageCode(newTestStorage()))
	assert.NotNil(t, sm.GetStorageCode())

	require.NoError(t, sm.AddStorageSmartContract(newTestStorage()))
	assert.NotNil(t, sm.GetStorageSmartContract())

	require.NoError(t, sm.AddStorageDatabaseTrie(newTestStorage()))
	assert.NotNil(t, sm.GetStorageDatabaseTrie())

	require.NoError(t, sm.AddStorageBackupDb(newTestStorage()))
	assert.NotNil(t, sm.GetStorageBackupDb())

	require.NoError(t, sm.AddStorageMapping(newTestStorage()))
	assert.NotNil(t, sm.GetStorageMapping())

	require.NoError(t, sm.AddStorageStake(newTestStorage()))
	assert.NotNil(t, sm.GetStorageStake())

	require.NoError(t, sm.AddStorageBackupDeviceKey(newTestStorage()))
	assert.NotNil(t, sm.GetStorageBackupDeviceKey())
}

// ---------- CloseAll ----------

func TestStorageManager_CloseAll(t *testing.T) {
	sm := NewStorageManager()
	sm.AddStorageAccount(newTestStorage())
	sm.AddStorageBlock(newTestStorage())
	sm.AddStorageTransaction(newTestStorage())

	err := sm.CloseAll()
	assert.NoError(t, err)

	// After CloseAll, storages should be removed
	assert.Nil(t, sm.GetStorageAccount())
	assert.Nil(t, sm.GetStorageBlock())
}

// ---------- Explorer / Mining flags ----------

func TestStorageManager_IsExplorer_Default(t *testing.T) {
	sm := NewStorageManager()
	assert.False(t, sm.IsExplorer())
}

func TestStorageManager_IsMining_Default(t *testing.T) {
	sm := NewStorageManager()
	assert.False(t, sm.IsMining())
}

// ---------- FlushAll ----------

func TestStorageManager_FlushAll(t *testing.T) {
	sm := NewStorageManager()
	sm.AddStorageAccount(newTestStorage())
	sm.AddStorageBlock(newTestStorage())
	sm.AddStorageTransaction(newTestStorage())

	// FlushAll should succeed (testStorage.Flush returns nil)
	err := sm.FlushAll()
	assert.NoError(t, err)

	// Storages should still be accessible after flush (unlike CloseAll)
	assert.NotNil(t, sm.GetStorageAccount())
	assert.NotNil(t, sm.GetStorageBlock())
	assert.NotNil(t, sm.GetStorageTransaction())
}

func TestStorageManager_FlushAll_Empty(t *testing.T) {
	sm := NewStorageManager()
	err := sm.FlushAll()
	assert.NoError(t, err)
}
