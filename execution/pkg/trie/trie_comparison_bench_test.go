package trie

import (
	"crypto/rand"
	"fmt"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
)

// ══════════════════════════════════════════════════════════════════════════════
// Unified Benchmarks: FlatStateTrie vs MerklePatriciaTrie vs VerkleStateTrie
//
// Run ALL benchmarks:
//   go test -bench=BenchmarkTrieCompare -benchtime=3s -benchmem ./pkg/trie/
//
// Run specific benchmark:
//   go test -bench=BenchmarkTrieCompare_FullCycle -benchtime=5s -benchmem ./pkg/trie/
//
// NOTE: Verkle is excluded from repeated-operation benchmarks because go-verkle
// v0.1.1 panics with nil pointer in InsertValuesAtStem on repeated inserts.
// When the library is fixed, set skipRepeated=false for Verkle below.
// ══════════════════════════════════════════════════════════════════════════════

// ---------- helpers ----------

func cmpKey(i int) []byte {
	key := make([]byte, 32)
	key[0] = byte(i >> 16)
	key[1] = byte(i >> 8)
	key[2] = byte(i)
	key[31] = byte(i)
	return key
}

func cmpValue(size int) []byte {
	val := make([]byte, size)
	rand.Read(val)
	return val
}

type trieFactory struct {
	name         string
	newTrie      func() StateTrie
	skipRepeated bool // skip benchmarks that repeat operations (Verkle panics)
}

func cmpTrieFactories() []trieFactory {
	return []trieFactory{
		{
			name: "Flat",
			newTrie: func() StateTrie {
				return NewFlatStateTrie(newMemFlatDB(), true)
			},
		},
		{
			name: "MPT",
			newTrie: func() StateTrie {
				t, err := New(e_common.Hash{}, newMemFlatDB(), true)
				if err != nil {
					panic(err)
				}
				return t
			},
		},
		// Verkle is excluded: go-verkle v0.1.1 panics on repeated tree insertions
		// due to nil pointer dereference in InternalNode.InsertValuesAtStem.
		// Uncomment when the library is fixed:
		// {
		// 	name: "Verkle",
		// 	newTrie: func() StateTrie {
		// 		return NewVerkleStateTrie(newMemFlatDB(), true)
		// 	},
		// },
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 1. Single-key Update throughput
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_Update(b *testing.B) {
	for _, tf := range cmpTrieFactories() {
		b.Run(tf.name, func(b *testing.B) {
			t := tf.newTrie()
			key := cmpKey(0)
			value := cmpValue(64)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = t.Update(key, value)
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 2. Single-key Get (dirty cache hit)
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_Get_Dirty(b *testing.B) {
	for _, tf := range cmpTrieFactories() {
		b.Run(tf.name, func(b *testing.B) {
			t := tf.newTrie()
			key := cmpKey(0)
			_ = t.Update(key, cmpValue(64))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = t.Get(key)
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 3. BatchUpdate — 100 / 1K / 10K keys
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_BatchUpdate(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		keys := make([][]byte, size)
		values := make([][]byte, size)
		for i := 0; i < size; i++ {
			keys[i] = cmpKey(i)
			values[i] = cmpValue(64)
		}

		for _, tf := range cmpTrieFactories() {
			b.Run(fmt.Sprintf("%s/%d", tf.name, size), func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					t := tf.newTrie()
					_ = t.BatchUpdate(keys, values)
				}
			})
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 4. Hash — compute root after N dirty entries
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_Hash(b *testing.B) {
	for _, size := range []int{100, 1000} {
		for _, tf := range cmpTrieFactories() {
			b.Run(fmt.Sprintf("%s/%d", tf.name, size), func(b *testing.B) {
				t := tf.newTrie()
				for i := 0; i < size; i++ {
					_ = t.Update(cmpKey(i), cmpValue(64))
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = t.Hash()
				}
			})
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 5. Commit — full flush to storage after N dirty entries
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_Commit(b *testing.B) {
	for _, size := range []int{100, 1000} {
		keys := make([][]byte, size)
		values := make([][]byte, size)
		for i := 0; i < size; i++ {
			keys[i] = cmpKey(i)
			values[i] = cmpValue(64)
		}

		for _, tf := range cmpTrieFactories() {
			b.Run(fmt.Sprintf("%s/%d", tf.name, size), func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					t := tf.newTrie()
					_ = t.BatchUpdate(keys, values)
					b.StartTimer()
					_, _, _, _ = t.Commit(false)
				}
			})
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 6. Copy — shallow clone with 100 dirty entries
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_Copy(b *testing.B) {
	for _, tf := range cmpTrieFactories() {
		b.Run(tf.name, func(b *testing.B) {
			t := tf.newTrie()
			for i := 0; i < 100; i++ {
				_ = t.Update(cmpKey(i), cmpValue(64))
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = t.Copy()
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 7. Full Cycle: BatchUpdate + Hash + Commit (simulates block processing)
//    ★ Most important benchmark — directly correlates to TPS.
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_FullCycle(b *testing.B) {
	for _, size := range []int{100, 1000, 5000} {
		keys := make([][]byte, size)
		values := make([][]byte, size)
		for i := 0; i < size; i++ {
			keys[i] = cmpKey(i)
			values[i] = cmpValue(64)
		}

		for _, tf := range cmpTrieFactories() {
			b.Run(fmt.Sprintf("%s/%d", tf.name, size), func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					t := tf.newTrie()
					_ = t.BatchUpdate(keys, values)
					_ = t.Hash()
					_, _, _, _ = t.Commit(false)
				}
			})
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 8. Mixed Read/Write (simulates realistic block with reads + writes)
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_MixedReadWrite(b *testing.B) {
	const numEntries = 500
	const readRatio = 3 // 3 reads per write

	keys := make([][]byte, numEntries)
	values := make([][]byte, numEntries)
	for i := 0; i < numEntries; i++ {
		keys[i] = cmpKey(i)
		values[i] = cmpValue(64)
	}

	for _, tf := range cmpTrieFactories() {
		b.Run(tf.name, func(b *testing.B) {
			t := tf.newTrie()
			_ = t.BatchUpdate(keys, values)
			_, _, _, _ = t.Commit(false)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				idx := i % numEntries
				// Reads
				for r := 0; r < readRatio; r++ {
					_, _ = t.Get(keys[(idx+r)%numEntries])
				}
				// Write
				_ = t.Update(keys[idx], cmpValue(64))
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// 9. Value size sensitivity — how does value size affect throughput?
// ══════════════════════════════════════════════════════════════════════════════

func BenchmarkTrieCompare_ValueSize(b *testing.B) {
	for _, valSize := range []int{32, 128, 512, 2048} {
		for _, tf := range cmpTrieFactories() {
			b.Run(fmt.Sprintf("%s/%dB", tf.name, valSize), func(b *testing.B) {
				t := tf.newTrie()
				key := cmpKey(0)
				value := cmpValue(valSize)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = t.Update(key, value)
				}
			})
		}
	}
}
