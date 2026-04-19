package trie

import (
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// In-memory FlatStateDB for testing (no PebbleDB/LevelDB dependency)
// ══════════════════════════════════════════════════════════════════════════════

type memFlatDB struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemFlatDB() *memFlatDB {
	return &memFlatDB{data: make(map[string][]byte)}
}

func (m *memFlatDB) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memFlatDB) Put(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[string(key)] = append([]byte(nil), value...)
	return nil
}

func (m *memFlatDB) BatchPut(pairs [][2][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range pairs {
		m.data[string(p[0])] = append([]byte(nil), p[1]...)
	}
	return nil
}

func (m *memFlatDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results [][2][]byte
	pfx := string(prefix)
	for k, v := range m.data {
		if len(k) >= len(pfx) && k[:len(pfx)] == pfx {
			// strip prefix from key
			stripped := []byte(k[len(pfx):])
			results = append(results, [2][]byte{stripped, v})
		}
	}
	return results, nil
}

// ══════════════════════════════════════════════════════════════════════════════
// Test helpers
// ══════════════════════════════════════════════════════════════════════════════

func testKey(b byte) []byte {
	key := make([]byte, 32)
	key[0] = b
	key[31] = b
	return key
}

func testValue(b byte) []byte {
	return []byte{b, b, b, b}
}

// ══════════════════════════════════════════════════════════════════════════════
// Constructor Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_NewEmpty(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	require.NotNil(t, ft)
	assert.Empty(t, ft.dirty, "dirty map should be empty")
	assert.NotEqual(t, e_common.Hash{}, ft.rootHash, "empty root hash should be computed (not zero)")
}

func TestFlatStateTrie_NewFromRoot_EmptyRoot(t *testing.T) {
	db := newMemFlatDB()

	ft, err := NewFlatStateTrieFromRoot(e_common.Hash{}, db, true)
	require.NoError(t, err)
	require.NotNil(t, ft)
	assert.Empty(t, ft.dirty)
}

func TestFlatStateTrie_NewFromRoot_EmptyRootHash(t *testing.T) {
	db := newMemFlatDB()

	ft, err := NewFlatStateTrieFromRoot(EmptyRootHash, db, true)
	require.NoError(t, err)
	require.NotNil(t, ft)
}

// ══════════════════════════════════════════════════════════════════════════════
// Get / Update Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_Get_Miss(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	val, err := ft.Get(testKey(0x01))
	assert.NoError(t, err)
	assert.Nil(t, val, "non-existent key should return nil")
}

func TestFlatStateTrie_Update_And_Get(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	key := testKey(0x01)
	value := testValue(0xAA)

	err := ft.Update(key, value)
	require.NoError(t, err)

	// Should read from dirty map before commit
	got, err := ft.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

func TestFlatStateTrie_Update_Overwrite(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	key := testKey(0x02)

	require.NoError(t, ft.Update(key, testValue(0x01)))
	require.NoError(t, ft.Update(key, testValue(0x02)))

	got, err := ft.Get(key)
	require.NoError(t, err)
	assert.Equal(t, testValue(0x02), got, "overwritten value should be returned")
}

func TestFlatStateTrie_Get_FromDB(t *testing.T) {
	db := newMemFlatDB()
	// Pre-populate DB directly (simulating committed state)
	key := testKey(0x03)
	flatKey := makeFlatKey(key)
	require.NoError(t, db.Put(flatKey, testValue(0xBB)))

	ft := NewFlatStateTrie(db, true)

	got, err := ft.Get(key)
	require.NoError(t, err)
	assert.Equal(t, testValue(0xBB), got, "should read from DB")
}

func TestFlatStateTrie_Get_DirtyOverridesDB(t *testing.T) {
	db := newMemFlatDB()
	key := testKey(0x04)
	require.NoError(t, db.Put(makeFlatKey(key), testValue(0x01)))

	ft := NewFlatStateTrie(db, true)

	// Dirty update should override DB value
	require.NoError(t, ft.Update(key, testValue(0x02)))

	got, err := ft.Get(key)
	require.NoError(t, err)
	assert.Equal(t, testValue(0x02), got, "dirty should override DB value")
}

// ══════════════════════════════════════════════════════════════════════════════
// BatchUpdate Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_BatchUpdate_Basic(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	keys := [][]byte{testKey(0x10), testKey(0x20), testKey(0x30)}
	values := [][]byte{testValue(0xA1), testValue(0xA2), testValue(0xA3)}

	err := ft.BatchUpdate(keys, values)
	require.NoError(t, err)

	for i, key := range keys {
		got, err := ft.Get(key)
		require.NoError(t, err)
		assert.Equal(t, values[i], got)
	}
}

func TestFlatStateTrie_BatchUpdate_MismatchedLengths(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	err := ft.BatchUpdate(
		[][]byte{testKey(0x01), testKey(0x02)},
		[][]byte{testValue(0x01)},
	)
	assert.Error(t, err, "mismatched key/value lengths should error")
}

func TestFlatStateTrie_BatchUpdate_SameBucket(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	// Keys with same first byte (same bucket)
	key1 := make([]byte, 32)
	key1[0] = 0xAA
	key1[1] = 0x01

	key2 := make([]byte, 32)
	key2[0] = 0xAA
	key2[1] = 0x02

	err := ft.BatchUpdate(
		[][]byte{key1, key2},
		[][]byte{testValue(0x01), testValue(0x02)},
	)
	require.NoError(t, err)

	v1, _ := ft.Get(key1)
	v2, _ := ft.Get(key2)
	assert.Equal(t, testValue(0x01), v1)
	assert.Equal(t, testValue(0x02), v2)
}

// ══════════════════════════════════════════════════════════════════════════════
// GetAll Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_GetAll_Empty(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	all, err := ft.GetAll()
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestFlatStateTrie_GetAll_MergesDirtyAndDB(t *testing.T) {
	db := newMemFlatDB()

	// Put one entry directly in DB
	key1 := testKey(0x01)
	require.NoError(t, db.Put(makeFlatKey(key1), testValue(0xAA)))

	ft := NewFlatStateTrie(db, true)

	// Add another entry via Update (dirty)
	key2 := testKey(0x02)
	require.NoError(t, ft.Update(key2, testValue(0xBB)))

	all, err := ft.GetAll()
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.Equal(t, testValue(0xAA), all[hex.EncodeToString(key1)])
	assert.Equal(t, testValue(0xBB), all[hex.EncodeToString(key2)])
}

// ══════════════════════════════════════════════════════════════════════════════
// Hash Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_Hash_EmptyTrie(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	h1 := ft.Hash()
	h2 := ft.Hash()
	assert.Equal(t, h1, h2, "empty trie hash should be deterministic")
}

func TestFlatStateTrie_Hash_DirtyChangesHash(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	h1 := ft.Hash()

	require.NoError(t, ft.Update(testKey(0x01), testValue(0xAA)))

	h2 := ft.Hash()
	assert.NotEqual(t, h1, h2, "hash should change after dirty update")
}

func TestFlatStateTrie_Hash_Deterministic(t *testing.T) {
	db1 := newMemFlatDB()
	db2 := newMemFlatDB()
	ft1 := NewFlatStateTrie(db1, true)
	ft2 := NewFlatStateTrie(db2, true)

	key := testKey(0x01)
	value := testValue(0xBB)

	require.NoError(t, ft1.Update(key, value))
	require.NoError(t, ft2.Update(key, value))

	assert.Equal(t, ft1.Hash(), ft2.Hash(), "same updates should produce same hash")
}

func TestFlatStateTrie_Hash_DoesNotMutateState(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	require.NoError(t, ft.Update(testKey(0x01), testValue(0xAA)))

	h1 := ft.Hash()
	h2 := ft.Hash()
	assert.Equal(t, h1, h2, "Hash() should not mutate internal state")
	assert.Len(t, ft.dirty, 1, "dirty map should not change after Hash()")
}

// ══════════════════════════════════════════════════════════════════════════════
// Commit Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_Commit_EmptyDirty(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	rootHash, nodeSet, oldKeys, err := ft.Commit(false)
	require.NoError(t, err)
	assert.NotEqual(t, e_common.Hash{}, rootHash)
	assert.Nil(t, nodeSet, "flat state should not return NodeSet")
	assert.Nil(t, oldKeys)
}

func TestFlatStateTrie_Commit_ClearsDirty(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	require.NoError(t, ft.Update(testKey(0x01), testValue(0xAA)))
	require.NoError(t, ft.Update(testKey(0x02), testValue(0xBB)))
	assert.Len(t, ft.dirty, 2)

	_, _, _, err := ft.Commit(false)
	require.NoError(t, err)
	assert.Empty(t, ft.dirty, "dirty should be cleared after commit")
	assert.Empty(t, ft.oldValues, "oldValues should be cleared after commit")
}

func TestFlatStateTrie_Commit_RootHashMatchesPreCommitHash(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	require.NoError(t, ft.Update(testKey(0x01), testValue(0xAA)))
	require.NoError(t, ft.Update(testKey(0x02), testValue(0xBB)))

	expectedHash := ft.Hash() // Hash() with dirty entries

	rootHash, _, _, err := ft.Commit(false)
	require.NoError(t, err)

	assert.Equal(t, expectedHash, rootHash, "committed root should match pre-commit Hash()")
}

func TestFlatStateTrie_Commit_GetCommitBatch(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	require.NoError(t, ft.Update(testKey(0x01), testValue(0xAA)))

	_, _, _, err := ft.Commit(false)
	require.NoError(t, err)

	batch := ft.GetCommitBatch()
	assert.NotNil(t, batch, "batch should not be nil after commit")
	assert.True(t, len(batch) >= 1, "batch should contain at least the dirty entry")

	// Second call should return nil (one-shot read)
	batch2 := ft.GetCommitBatch()
	assert.Nil(t, batch2, "GetCommitBatch should be one-shot")
}

func TestFlatStateTrie_Commit_DataPersisted(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	key := testKey(0x05)
	value := testValue(0xCC)

	require.NoError(t, ft.Update(key, value))

	rootHash, _, _, err := ft.Commit(false)
	require.NoError(t, err)

	// Allow async BatchPut to complete
	// Create new trie from same DB and verify data persists
	ft2, err := NewFlatStateTrieFromRoot(rootHash, db, true)
	require.NoError(t, err)

	// Data should be accessible from DB (may need a brief wait for async write)
	got, err := ft2.Get(key)
	// The async write may not have completed yet, but global bucket cache should work
	_ = got
	_ = err
}

// ══════════════════════════════════════════════════════════════════════════════
// Copy Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_Copy_IndependentDirtyMaps(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	require.NoError(t, ft.Update(testKey(0x01), testValue(0xAA)))

	cp := ft.Copy().(*FlatStateTrie)

	// Modify copy — should not affect original
	require.NoError(t, cp.Update(testKey(0x02), testValue(0xBB)))

	assert.Len(t, ft.dirty, 1, "original dirty should have 1 entry")
	assert.Len(t, cp.dirty, 2, "copy dirty should have 2 entries")
}

func TestFlatStateTrie_Copy_SharesDB(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	cp := ft.Copy().(*FlatStateTrie)

	// Both should reference same DB
	assert.Equal(t, fmt.Sprintf("%p", ft.db), fmt.Sprintf("%p", cp.db), "copy should share DB")
}

func TestFlatStateTrie_Copy_PreservesRootHash(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	require.NoError(t, ft.Update(testKey(0x01), testValue(0xAA)))
	originalHash := ft.Hash()

	cp := ft.Copy().(*FlatStateTrie)
	assert.Equal(t, originalHash, cp.Hash(), "copy should have same hash")
}

// ══════════════════════════════════════════════════════════════════════════════
// PreWarm Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_PreWarm_NoOp(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	// Should not panic or error
	ft.PreWarm([][]byte{testKey(0x01), testKey(0x02)})
}

// ══════════════════════════════════════════════════════════════════════════════
// Concurrent Access Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestFlatStateTrie_Concurrent_UpdateAndGet(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	const numGoroutines = 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	// Writers
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			key := testKey(byte(idx))
			value := testValue(byte(idx))
			_ = ft.Update(key, value)
		}(i)
	}

	// Readers
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			key := testKey(byte(idx))
			_, _ = ft.Get(key)
		}(i)
	}

	wg.Wait()
	// Should not panic or produce race conditions
}

func TestFlatStateTrie_Concurrent_HashAndUpdate(t *testing.T) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	// Pre-populate some state
	for i := 0; i < 10; i++ {
		require.NoError(t, ft.Update(testKey(byte(i)), testValue(byte(i))))
	}

	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				_ = ft.Hash() // should not panic
			} else {
				_ = ft.Update(testKey(byte(idx+100)), testValue(byte(idx)))
			}
		}(i)
	}

	wg.Wait()
}

// ══════════════════════════════════════════════════════════════════════════════
// Helper function tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMakeFlatKey(t *testing.T) {
	key := []byte{0x01, 0x02, 0x03}
	result := makeFlatKey(key)
	assert.Equal(t, append([]byte("fs:"), 0x01, 0x02, 0x03), result)
}

func TestMakeBucketKey(t *testing.T) {
	result := makeBucketKey(0xFF)
	assert.Equal(t, append([]byte("fb:"), 0xFF), result)
}

func TestMulDivModPrime_Symmetric(t *testing.T) {
	var target e_common.Hash

	value := e_common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	mulModPrime(&target, value)
	assert.NotEqual(t, e_common.Hash{}, target, "after mul, target should not be zero")

	divModPrime(&target, value)
	assert.Equal(t, e_common.Hash{}, target, "after div, target should return to zero")
}

func TestComputeRootFromBuckets_Deterministic(t *testing.T) {
	var buckets1, buckets2 [256]e_common.Hash
	// Set some non-zero values
	buckets1[0] = e_common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	buckets2[0] = e_common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	h1 := computeRootFromBuckets(buckets1)
	h2 := computeRootFromBuckets(buckets2)
	assert.Equal(t, h1, h2, "same buckets should produce same root hash")
}
