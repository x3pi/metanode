package trie

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
)

func openBenchNomtHandle(b *testing.B) *nomt_ffi.Handle {
	b.Helper()
	h, err := nomt_ffi.Open(filepath.Join(b.TempDir(), "nomt"), 1, 64, 64, 64000, false)
	if err != nil {
		b.Fatalf("nomt_ffi.Open: %v", err)
	}
	b.Cleanup(h.Close)
	return h
}

// BenchmarkNomtCommit_BlockPipeline measures consecutive block execution pipeline:
// Block N writes keys -> Commit(false) [IntermediateRoot] -> CreateNomtPayload().CommitAsync() [background disk persist & fsync]
// Block N+1 writes keys -> Commit(false) [which waits for commitWg if previous commit is still flushing].
func BenchmarkNomtCommit_BlockPipeline(b *testing.B) {
	for _, numKeys := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("Keys_%d", numKeys), func(b *testing.B) {
			h := openBenchNomtHandle(b)
			trie := NewNomtStateTrie(h, true, "account_state")

			keys := make([][]byte, numKeys)
			vals := make([][]byte, numKeys)
			for i := 0; i < numKeys; i++ {
				keys[i] = cmpKey(i)
				vals[i] = cmpValue(64)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < numKeys; j++ {
					vals[j][0] = byte(i)
				}
				_ = trie.BatchUpdate(keys, vals)

				// Commit session (waits for previous commitWg if still in-flight)
				t0 := time.Now()
				_, _, _, err := trie.Commit(false)
				if err != nil {
					b.Fatalf("Commit failed: %v", err)
				}
				_ = time.Since(t0)

				// Background async flush via CommitAsync
				payload := trie.ExtractPendingPayload()
				if payload != nil {
					payload.CommitAsync()
				}
			}
			b.StopTimer()
			_ = trie.WaitCommitPayload()
		})
	}
}

// BenchmarkNomtCommit_Sync measures fully synchronous commits:
// Block N writes keys -> Commit(false) -> CommitPayload() [synchronous wait for disk write and fsync]
func BenchmarkNomtCommit_Sync(b *testing.B) {
	for _, numKeys := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("Keys_%d", numKeys), func(b *testing.B) {
			h := openBenchNomtHandle(b)
			trie := NewNomtStateTrie(h, true, "account_state")

			keys := make([][]byte, numKeys)
			vals := make([][]byte, numKeys)
			for i := 0; i < numKeys; i++ {
				keys[i] = cmpKey(i)
				vals[i] = cmpValue(64)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < numKeys; j++ {
					vals[j][0] = byte(i)
				}
				_ = trie.BatchUpdate(keys, vals)
				_, _, _, err := trie.Commit(false)
				if err != nil {
					b.Fatalf("Commit failed: %v", err)
				}
				if err := trie.CommitPayload(); err != nil {
					b.Fatalf("CommitPayload failed: %v", err)
				}
			}
		})
	}
}
