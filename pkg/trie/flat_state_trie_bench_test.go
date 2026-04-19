package trie

import (
	"crypto/rand"
	"testing"
)

// ══════════════════════════════════════════════════════════════════════════════
// Benchmarks for FlatStateTrie hot-path operations
// ══════════════════════════════════════════════════════════════════════════════

func benchKey(i int) []byte {
	key := make([]byte, 32)
	key[0] = byte(i >> 8)
	key[1] = byte(i)
	key[31] = byte(i)
	return key
}

func benchValue() []byte {
	val := make([]byte, 64)
	rand.Read(val)
	return val
}

func BenchmarkFlatStateTrie_Update(b *testing.B) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	key := benchKey(0)
	value := benchValue()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ft.Update(key, value)
	}
}

func BenchmarkFlatStateTrie_Get_Dirty(b *testing.B) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	key := benchKey(0)
	_ = ft.Update(key, benchValue())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ft.Get(key)
	}
}

func BenchmarkFlatStateTrie_Get_DB(b *testing.B) {
	db := newMemFlatDB()
	key := benchKey(0)
	_ = db.Put(makeFlatKey(key), benchValue())

	ft := NewFlatStateTrie(db, true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ft.Get(key)
	}
}

func BenchmarkFlatStateTrie_BatchUpdate_1K(b *testing.B) {
	const n = 1000
	keys := make([][]byte, n)
	values := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = benchKey(i)
		values[i] = benchValue()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db := newMemFlatDB()
		ft := NewFlatStateTrie(db, true)
		_ = ft.BatchUpdate(keys, values)
	}
}

func BenchmarkFlatStateTrie_Hash_1KDirty(b *testing.B) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	for i := 0; i < 1000; i++ {
		_ = ft.Update(benchKey(i), benchValue())
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ft.Hash()
	}
}

func BenchmarkFlatStateTrie_Commit_1KDirty(b *testing.B) {
	const n = 1000
	keys := make([][]byte, n)
	values := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = benchKey(i)
		values[i] = benchValue()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db := newMemFlatDB()
		ft := NewFlatStateTrie(db, true)
		_ = ft.BatchUpdate(keys, values)
		b.StartTimer()

		_, _, _, _ = ft.Commit(false)
	}
}

func BenchmarkFlatStateTrie_Copy(b *testing.B) {
	db := newMemFlatDB()
	ft := NewFlatStateTrie(db, true)

	for i := 0; i < 100; i++ {
		_ = ft.Update(benchKey(i), benchValue())
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ft.Copy()
	}
}
