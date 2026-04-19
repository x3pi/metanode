package trie

import (
	"sync"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// VerkleStateTrie Tests
// Uses the same memFlatDB helper defined in flat_state_trie_test.go
// ══════════════════════════════════════════════════════════════════════════════

// ══════════════════════════════════════════════════════════════════════════════
// Constructor Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_NewEmpty(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	require.NotNil(t, vt)
	assert.Empty(t, vt.dirty, "dirty map should be empty on new trie")
	assert.Equal(t, EmptyRootHash, vt.rootHash, "root hash should be EmptyRootHash initially")
}

func TestVerkleStateTrie_NewFromRoot_ZeroHash(t *testing.T) {
	db := newMemFlatDB()
	vt, err := NewVerkleStateTrieFromRoot(e_common.Hash{}, db, false)
	require.NoError(t, err)
	require.NotNil(t, vt)
}

func TestVerkleStateTrie_NewFromRoot_NonZeroHash(t *testing.T) {
	db := newMemFlatDB()
	root := e_common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	vt, err := NewVerkleStateTrieFromRoot(root, db, true)
	require.NoError(t, err)
	require.NotNil(t, vt)
	// Root hash is stored but tree populated on demand
	assert.Equal(t, root, vt.rootHash)
}

// ══════════════════════════════════════════════════════════════════════════════
// Get / Update Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_Get_MissReturnsNil(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	val, err := vt.Get(testKey(0x01))
	// Missing key returns nil, nil (consistent with FlatStateTrie and backing DB pattern)
	assert.NoError(t, err, "missing key is not an error, just returns nil")
	assert.Nil(t, val)
}

func TestVerkleStateTrie_Update_And_Get(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	key := testKey(0x01)
	value := testValue(0xAA)

	err := vt.Update(key, value)
	require.NoError(t, err)

	// Should read from dirty map before commit
	got, err := vt.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

func TestVerkleStateTrie_Update_Overwrite(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	key := testKey(0x02)
	require.NoError(t, vt.Update(key, testValue(0x01)))
	require.NoError(t, vt.Update(key, testValue(0x02)))

	got, err := vt.Get(key)
	require.NoError(t, err)
	assert.Equal(t, testValue(0x02), got, "latest update should win")
}

func TestVerkleStateTrie_Update_ShortKey(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	// Short key (< 32 bytes) should be padded by padTo32
	shortKey := []byte{0xAB, 0xCD}
	value := testValue(0x55)

	err := vt.Update(shortKey, value)
	require.NoError(t, err)

	got, err := vt.Get(shortKey)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

func TestVerkleStateTrie_Update_LongKey(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	// Long key (> 32 bytes) should be hashed by padTo32
	longKey := make([]byte, 64)
	for i := range longKey {
		longKey[i] = byte(i)
	}
	value := testValue(0x66)

	err := vt.Update(longKey, value)
	require.NoError(t, err)

	got, err := vt.Get(longKey)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

func TestVerkleStateTrie_Update_LargeValue(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	key := testKey(0x42)
	// Simulate a real serialized AccountState (~165 bytes)
	largeValue := make([]byte, 165)
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	// Update must succeed — value is hashed to 32 bytes before Verkle insertion
	err := vt.Update(key, largeValue)
	require.NoError(t, err, "Update with large value (>32 bytes) must succeed")

	// Get must return the original raw value (not the hash)
	got, err := vt.Get(key)
	require.NoError(t, err)
	assert.Equal(t, largeValue, got, "Get must return original raw value")

	// Hash must not panic and must produce a non-empty root
	h := vt.Hash()
	assert.NotEqual(t, EmptyRootHash, h, "hash should change after large-value update")

	// Commit must succeed
	rootHash, _, _, err := vt.Commit(false)
	require.NoError(t, err, "Commit with large value must succeed")
	assert.NotEqual(t, e_common.Hash{}, rootHash)
}

func TestVerkleStateTrie_BatchUpdate_LargeValues(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	keys := [][]byte{testKey(0x10), testKey(0x20), testKey(0x30)}
	values := make([][]byte, 3)
	for i := range values {
		values[i] = make([]byte, 100+i*50) // 100, 150, 200 bytes
		for j := range values[i] {
			values[i][j] = byte((i + j) % 256)
		}
	}

	err := vt.BatchUpdate(keys, values)
	require.NoError(t, err, "BatchUpdate with large values must succeed")

	for i, key := range keys {
		got, err := vt.Get(key)
		require.NoError(t, err)
		assert.Equal(t, values[i], got)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// GetAll Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_GetAll_EmptyTrie(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	result, err := vt.GetAll()
	// GetAll() IS supported via PrefixScan — returns empty map for empty trie
	assert.NoError(t, err, "GetAll on empty trie should succeed")
	assert.NotNil(t, result)
	assert.Empty(t, result, "empty trie should return empty map")
}

// ══════════════════════════════════════════════════════════════════════════════
// BatchUpdate Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_BatchUpdate_Basic(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	keys := [][]byte{testKey(0x10), testKey(0x20), testKey(0x30)}
	values := [][]byte{testValue(0xA1), testValue(0xA2), testValue(0xA3)}

	err := vt.BatchUpdate(keys, values)
	require.NoError(t, err)

	for i, key := range keys {
		got, err := vt.Get(key)
		require.NoError(t, err)
		assert.Equal(t, values[i], got)
	}
}

func TestVerkleStateTrie_BatchUpdate_Empty(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	err := vt.BatchUpdate([][]byte{}, [][]byte{})
	assert.NoError(t, err, "empty batch update should succeed")
}

// ══════════════════════════════════════════════════════════════════════════════
// Hash Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_Hash_EmptyTrie(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	// Empty trie with no dirty — returns cached EmptyRootHash
	h := vt.Hash()
	assert.Equal(t, EmptyRootHash, h, "empty trie hash should be EmptyRootHash")
}

func TestVerkleStateTrie_Hash_ChangesAfterUpdate(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	h1 := vt.Hash()
	require.NoError(t, vt.Update(testKey(0x01), testValue(0xAA)))
	h2 := vt.Hash()

	assert.NotEqual(t, h1, h2, "hash must change after update")
}

func TestVerkleStateTrie_Hash_Deterministic(t *testing.T) {
	db1 := newMemFlatDB()
	db2 := newMemFlatDB()
	vt1 := NewVerkleStateTrie(db1, true)
	vt2 := NewVerkleStateTrie(db2, true)

	key := testKey(0x42)
	value := testValue(0x77)

	require.NoError(t, vt1.Update(key, value))
	require.NoError(t, vt2.Update(key, value))

	assert.Equal(t, vt1.Hash(), vt2.Hash(), "same key-value should produce same Verkle hash")
}

func TestVerkleStateTrie_Hash_DoesNotMutateState(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	require.NoError(t, vt.Update(testKey(0x01), testValue(0xAA)))

	h1 := vt.Hash()
	h2 := vt.Hash()
	assert.Equal(t, h1, h2, "Hash() must be idempotent")
	assert.Len(t, vt.dirty, 1, "Hash() must not clear dirty map")
}

// ══════════════════════════════════════════════════════════════════════════════
// Commit Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_Commit_NoDirty(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	rootHash, nodeSet, oldKeys, err := vt.Commit(false)
	require.NoError(t, err)
	assert.Equal(t, EmptyRootHash, rootHash)
	assert.Nil(t, nodeSet, "VerkleStateTrie should not return NodeSet")
	assert.Nil(t, oldKeys)
}

func TestVerkleStateTrie_Commit_ClearsDirty(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	require.NoError(t, vt.Update(testKey(0x01), testValue(0xAA)))
	require.NoError(t, vt.Update(testKey(0x02), testValue(0xBB)))
	assert.Len(t, vt.dirty, 2)

	_, _, _, err := vt.Commit(false)
	require.NoError(t, err)
	assert.Empty(t, vt.dirty, "dirty should be cleared after commit")
	assert.Empty(t, vt.oldValues, "oldValues should be cleared after commit")
}

func TestVerkleStateTrie_Commit_RootHashNonZero(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	require.NoError(t, vt.Update(testKey(0xAB), testValue(0xFF)))

	rootHash, _, _, err := vt.Commit(false)
	require.NoError(t, err)
	assert.NotEqual(t, e_common.Hash{}, rootHash, "committed root should not be zero")
}

func TestVerkleStateTrie_Commit_HashConsistency(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	require.NoError(t, vt.Update(testKey(0x05), testValue(0xCC)))

	preCommitHash := vt.Hash()
	rootHash, _, _, err := vt.Commit(false)
	require.NoError(t, err)

	assert.Equal(t, preCommitHash, rootHash, "Commit() root should match pre-commit Hash()")
}

// ══════════════════════════════════════════════════════════════════════════════
// Copy Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_Copy_IsIndependent(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	require.NoError(t, vt.Update(testKey(0x01), testValue(0xAA)))

	cp := vt.Copy().(*VerkleStateTrie)

	// Modify copy — original should not be affected
	require.NoError(t, cp.Update(testKey(0x02), testValue(0xBB)))

	assert.Len(t, vt.dirty, 1, "original should have 1 dirty entry")
	assert.Len(t, cp.dirty, 2, "copy should have 2 dirty entries")
}

func TestVerkleStateTrie_Copy_SharesDB(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)
	cp := vt.Copy().(*VerkleStateTrie)

	// Both should reference same underlying DB
	assert.Equal(t, vt.db, cp.db, "copy should share the same DB reference")
}

func TestVerkleStateTrie_Copy_PreservesRootHash(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	require.NoError(t, vt.Update(testKey(0xCC), testValue(0xDD)))
	expectedHash := vt.Hash()

	cp := vt.Copy().(*VerkleStateTrie)
	assert.Equal(t, expectedHash, cp.Hash(), "copy should have same root hash")
}

// ══════════════════════════════════════════════════════════════════════════════
// PreWarm Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_PreWarm_IsNoOp(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	// PreWarm is a no-op for Verkle — must not panic
	vt.PreWarm([][]byte{testKey(0x01), testKey(0x02)})
	assert.Empty(t, vt.dirty, "PreWarm should not modify dirty state")
}

// ══════════════════════════════════════════════════════════════════════════════
// Concurrent Access Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestVerkleStateTrie_Concurrent_UpdateAndGet(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	const numGoroutines = 30
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	// Writers
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			key := testKey(byte(idx % 256))
			_ = vt.Update(key, testValue(byte(idx%256)))
		}(i)
	}

	// Readers
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			key := testKey(byte(idx % 256))
			_, _ = vt.Get(key)
		}(i)
	}

	wg.Wait()
	// Must not race or panic
}

func TestVerkleStateTrie_Concurrent_HashAndUpdate(t *testing.T) {
	db := newMemFlatDB()
	vt := NewVerkleStateTrie(db, true)

	for i := 0; i < 5; i++ {
		require.NoError(t, vt.Update(testKey(byte(i)), testValue(byte(i))))
	}

	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				_ = vt.Hash()
			} else {
				_ = vt.Update(testKey(byte(idx+100)), testValue(byte(idx)))
			}
		}(i)
	}

	wg.Wait()
}

// ══════════════════════════════════════════════════════════════════════════════
// Helper function unit tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMakeVerkleKey_Prefix(t *testing.T) {
	key := []byte{0x01, 0x02, 0x03}
	result := makeVerkleKey(key)
	assert.Equal(t, append([]byte("vk:"), 0x01, 0x02, 0x03), result,
		"verkle DB key must have 'vk:' prefix")
}

func TestPadTo32_ShortKey(t *testing.T) {
	key := []byte{0xAB}
	result := padTo32(key)
	assert.Len(t, result, 32, "short key should be padded to 32 bytes")
	// Left-padded: last byte is the original
	assert.Equal(t, byte(0xAB), result[31])
	// First 31 bytes are zero
	for i := 0; i < 31; i++ {
		assert.Equal(t, byte(0x00), result[i])
	}
}

func TestPadTo32_ExactKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	result := padTo32(key)
	assert.Equal(t, key, result, "32-byte key should not be modified")
}

func TestPadTo32_LongKey(t *testing.T) {
	key := make([]byte, 64)
	for i := range key {
		key[i] = byte(i)
	}
	result := padTo32(key)
	assert.Len(t, result, 32, "long key should be hashed to 32 bytes")
	// Keccak256 of a 64-byte input is deterministic
	result2 := padTo32(key)
	assert.Equal(t, result, result2, "padTo32 long key must be deterministic")
}

func TestPadTo32_Deterministic(t *testing.T) {
	key := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	r1 := padTo32(key)
	r2 := padTo32(key)
	assert.Equal(t, r1, r2, "padTo32 must be deterministic")
}
