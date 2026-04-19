package storage

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// MemoryDB — advanced tests (Size, Snapshot, Iterator, no-op methods)
// ══════════════════════════════════════════════════════════════════════════════

func newCleanMemDB() *MemoryDB { return NewMemoryDb() }

func mkKey(b byte) []byte {
	key := make([]byte, 32)
	key[0] = b
	return key
}

// ---------- Size ----------

func TestMemoryDB_Size_Empty(t *testing.T) {
	db := newCleanMemDB()
	assert.Equal(t, 0, db.Size())
}

func TestMemoryDB_Size_AfterInserts(t *testing.T) {
	db := newCleanMemDB()
	db.Put(mkKey(1), []byte("v1"))
	db.Put(mkKey(2), []byte("v2"))
	db.Put(mkKey(3), []byte("v3"))
	assert.Equal(t, 3, db.Size())
}

func TestMemoryDB_Size_AfterDelete(t *testing.T) {
	db := newCleanMemDB()
	db.Put(mkKey(1), []byte("v1"))
	db.Put(mkKey(2), []byte("v2"))
	db.Delete(mkKey(1))
	assert.Equal(t, 1, db.Size())
}

// ---------- No-op methods ----------

func TestMemoryDB_Open(t *testing.T) {
	db := newCleanMemDB()
	assert.Nil(t, db.Open())
}

func TestMemoryDB_Close(t *testing.T) {
	db := newCleanMemDB()
	assert.Nil(t, db.Close())
}

func TestMemoryDB_Compact(t *testing.T) {
	db := newCleanMemDB()
	assert.Nil(t, db.Compact())
}

// ---------- Snapshot Isolation ----------

func TestMemoryDB_GetSnapShot_Isolation(t *testing.T) {
	db := newCleanMemDB()
	db.Put(mkKey(1), []byte("original"))
	db.Put(mkKey(2), []byte("two"))

	snap := db.GetSnapShot()
	snapDB := snap.(*MemoryDB)

	// Verify snapshot has the same data
	assert.Equal(t, 2, snapDB.Size())
	val, err := snapDB.Get(mkKey(1))
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), val)

	// Modify original DB — snapshot should NOT be affected
	db.Put(mkKey(1), []byte("modified"))
	db.Put(mkKey(3), []byte("three"))

	assert.Equal(t, 2, snapDB.Size()) // snapshot still 2
	val, err = snapDB.Get(mkKey(1))
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), val) // snapshot still "original"
}

// ---------- Has ----------

func TestMemoryDB_Has_Missing(t *testing.T) {
	db := newCleanMemDB()
	assert.False(t, db.Has(mkKey(99)))
}

func TestMemoryDB_Has_Present(t *testing.T) {
	db := newCleanMemDB()
	db.Put(mkKey(1), []byte("v"))
	assert.True(t, db.Has(mkKey(1)))
}

// ---------- Delete errors ----------

func TestMemoryDB_Delete_Missing(t *testing.T) {
	db := newCleanMemDB()
	err := db.Delete(mkKey(99))
	assert.Error(t, err)
}

// ---------- BatchPut ----------

func TestMemoryDB_BatchPut_Multiple(t *testing.T) {
	db := newCleanMemDB()
	kvs := [][2][]byte{
		{mkKey(1), []byte("a")},
		{mkKey(2), []byte("b")},
		{mkKey(3), []byte("c")},
	}
	err := db.BatchPut(kvs)
	require.NoError(t, err)
	assert.Equal(t, 3, db.Size())

	val, _ := db.Get(mkKey(2))
	assert.Equal(t, []byte("b"), val)
}

// ---------- Iterator ----------

func TestMemoryDB_Iterator_Empty(t *testing.T) {
	db := newCleanMemDB()
	iter := db.GetIterator()
	assert.False(t, iter.Next())
}

func TestMemoryDB_Iterator_FullScan(t *testing.T) {
	db := newCleanMemDB()
	db.Put(mkKey(1), []byte("v1"))
	db.Put(mkKey(2), []byte("v2"))
	db.Put(mkKey(3), []byte("v3"))

	iter := db.GetIterator()
	count := 0
	for iter.Next() {
		key := iter.Key()
		val := iter.Value()
		assert.NotEmpty(t, key)
		assert.NotEmpty(t, val)
		count++
	}
	assert.Equal(t, 3, count)
}

func TestMemoryDB_Iterator_KeysSorted(t *testing.T) {
	db := newCleanMemDB()
	// Insert in reverse order
	db.Put(mkKey(3), []byte("c"))
	db.Put(mkKey(1), []byte("a"))
	db.Put(mkKey(2), []byte("b"))

	iter := db.GetIterator()
	var keys []string
	for iter.Next() {
		keys = append(keys, common.Bytes2Hex(iter.Key()))
	}
	assert.Equal(t, 3, len(keys))
	// Verify sorted order
	for i := 1; i < len(keys); i++ {
		assert.True(t, keys[i-1] <= keys[i], "keys should be sorted")
	}
}

func TestMemoryDB_Iterator_Error(t *testing.T) {
	db := newCleanMemDB()
	iter := db.GetIterator()
	assert.Nil(t, iter.Error())
}
