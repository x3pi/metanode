package trie

import (
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// ══════════════════════════════════════════════════════════════════════════════
// Test helpers for MPT
// ══════════════════════════════════════════════════════════════════════════════

// newTestMPT creates a fresh MerklePatriciaTrie backed by in-memory storage.
func newTestMPT(t *testing.T) *MerklePatriciaTrie {
	t.Helper()
	db := storage.NewMemoryDb()
	tr, err := New(e_common.Hash{}, db, true)
	require.NoError(t, err, "failed to create MPT")
	require.NotNil(t, tr)
	return tr
}

// mptKey creates a 32-byte test key from a byte.
func mptKey(b byte) []byte {
	key := make([]byte, 32)
	key[0] = b
	return key
}

// mptValue creates a test value from a byte.
func mptValue(b byte) []byte {
	return []byte{b, b, b, b}
}

// ══════════════════════════════════════════════════════════════════════════════
// Constructor Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_New_EmptyRoot(t *testing.T) {
	tr := newTestMPT(t)
	hash := tr.Hash()
	assert.Equal(t, EmptyRootHash, hash, "empty trie should have EmptyRootHash")
}

func TestMPT_New_ZeroHash(t *testing.T) {
	db := storage.NewMemoryDb()
	tr, err := New(e_common.Hash{}, db, true)
	require.NoError(t, err)
	assert.Equal(t, EmptyRootHash, tr.Hash())
}

// ══════════════════════════════════════════════════════════════════════════════
// Get / Update Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_Update_And_Get(t *testing.T) {
	tr := newTestMPT(t)

	key := mptKey(0x01)
	val := mptValue(0xAA)

	err := tr.Update(key, val)
	require.NoError(t, err)

	got, err := tr.Get(key)
	require.NoError(t, err)
	assert.Equal(t, val, got)
}

func TestMPT_Get_Miss(t *testing.T) {
	tr := newTestMPT(t)

	got, err := tr.Get(mptKey(0xFF))
	require.NoError(t, err)
	assert.Nil(t, got, "missing key should return nil")
}

func TestMPT_Update_Multiple(t *testing.T) {
	tr := newTestMPT(t)

	for i := byte(0); i < 10; i++ {
		err := tr.Update(mptKey(i), mptValue(i))
		require.NoError(t, err)
	}

	// Verify all keys
	for i := byte(0); i < 10; i++ {
		got, err := tr.Get(mptKey(i))
		require.NoError(t, err)
		assert.Equal(t, mptValue(i), got, "key %d mismatch", i)
	}
}

func TestMPT_Update_Overwrite(t *testing.T) {
	tr := newTestMPT(t)
	key := mptKey(0x01)

	err := tr.Update(key, mptValue(0x01))
	require.NoError(t, err)

	err = tr.Update(key, mptValue(0x02))
	require.NoError(t, err)

	got, err := tr.Get(key)
	require.NoError(t, err)
	assert.Equal(t, mptValue(0x02), got, "should return overwritten value")
}

func TestMPT_Update_Delete(t *testing.T) {
	tr := newTestMPT(t)
	key := mptKey(0x01)

	err := tr.Update(key, mptValue(0x01))
	require.NoError(t, err)

	// Deleting by updating with empty value
	err = tr.Update(key, []byte{})
	require.NoError(t, err)

	got, err := tr.Get(key)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted key should return nil")
}

// ══════════════════════════════════════════════════════════════════════════════
// Hash Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_Hash_EmptyTrie(t *testing.T) {
	tr := newTestMPT(t)
	assert.Equal(t, EmptyRootHash, tr.Hash())
}

func TestMPT_Hash_ChangesAfterUpdate(t *testing.T) {
	tr := newTestMPT(t)

	hashBefore := tr.Hash()
	err := tr.Update(mptKey(0x01), mptValue(0x01))
	require.NoError(t, err)
	hashAfter := tr.Hash()

	assert.NotEqual(t, hashBefore, hashAfter, "hash should change after update")
}

func TestMPT_Hash_Deterministic(t *testing.T) {
	// Same key-value pairs should produce same hash regardless of insertion order
	tr1 := newTestMPT(t)
	tr2 := newTestMPT(t)

	// Insert in different order
	_ = tr1.Update(mptKey(0x01), mptValue(0x01))
	_ = tr1.Update(mptKey(0x02), mptValue(0x02))
	_ = tr1.Update(mptKey(0x03), mptValue(0x03))

	_ = tr2.Update(mptKey(0x03), mptValue(0x03))
	_ = tr2.Update(mptKey(0x01), mptValue(0x01))
	_ = tr2.Update(mptKey(0x02), mptValue(0x02))

	assert.Equal(t, tr1.Hash(), tr2.Hash(), "same data in different order should produce same hash")
}

// ══════════════════════════════════════════════════════════════════════════════
// Copy Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_Copy_Independent(t *testing.T) {
	tr := newTestMPT(t)
	_ = tr.Update(mptKey(0x01), mptValue(0x01))

	cp := tr.Copy()

	// Modify copy — should not affect original
	_ = cp.Update(mptKey(0x02), mptValue(0x02))

	// Original should NOT have key 0x02
	got, err := tr.Get(mptKey(0x02))
	require.NoError(t, err)
	assert.Nil(t, got, "original trie should not be affected by copy's update")

	// Copy should have key 0x02
	got2, err := cp.Get(mptKey(0x02))
	require.NoError(t, err)
	assert.Equal(t, mptValue(0x02), got2)
}

func TestMPT_ParallelGet(t *testing.T) {
	tr := newTestMPT(t)

	for i := byte(0); i < 5; i++ {
		_ = tr.Update(mptKey(i), mptValue(i))
	}

	keys := make([][]byte, 5)
	for i := byte(0); i < 5; i++ {
		keys[i] = mptKey(i)
	}

	results, err := tr.ParallelGet(keys)
	require.NoError(t, err)
	require.Len(t, results, 5)

	for i := byte(0); i < 5; i++ {
		assert.Equal(t, mptValue(i), results[i], "ParallelGet result %d mismatch", i)
	}
}
