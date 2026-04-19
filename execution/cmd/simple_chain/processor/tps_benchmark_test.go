// tps_benchmark_test.go — Go micro-benchmarks for TPS-sensitive components.
//
// Run with:
//
//	cd /home/abc/chain-n/mtn-simple-2025
//	go test -bench=. -benchtime=5s -benchmem ./cmd/simple_chain/processor/
//
// Or run a specific benchmark:
//
//	go test -bench=BenchmarkRateTracker -benchtime=5s -benchmem ./cmd/simple_chain/processor/
package processor

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: TransactionRateTracker.AddTransaction (single-threaded)
// Measures: Pure counter increment throughput — ceiling for TPS tracking overhead
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkRateTracker_AddTransaction(b *testing.B) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trt.AddTransaction()
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: TransactionRateTracker.AddTransaction (concurrent — 16 goroutines)
// Measures: Contended counter throughput under realistic multi-goroutine load
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkRateTracker_AddTransaction_Concurrent(b *testing.B) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			trt.AddTransaction()
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: TransactionRateTracker.GetTransactionRate
// Measures: Rate calculation overhead during high-throughput reads
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkRateTracker_GetTransactionRate(b *testing.B) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	// Pre-populate with transactions
	for i := 0; i < 10000; i++ {
		trt.AddTransaction()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trt.GetTransactionRate()
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: Simulated TPS throughput (Add + periodic Rate check)
// Measures: Realistic combined workload — add transactions + periodic TPS query
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkRateTracker_SimulatedTPS(b *testing.B) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trt.AddTransaction()
		if i%1000 == 0 {
			trt.GetTransactionRate()
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: Channel throughput (simulate ProcessResultChan bottleneck)
// Measures: Max throughput of buffered channel — mimics tx pipeline channel
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkChannel_Throughput(b *testing.B) {
	ch := make(chan int, 1000) // Same buffer size as typical ProcessResultChan

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			select {
			case ch <- 1:
			default:
				<-ch
			}
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: Batch accumulation (simulate GenerateBlock accumulation pattern)
// Measures: Slice append throughput for transaction batching
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkBatchAccumulation(b *testing.B) {
	for _, batchSize := range []int{100, 1000, 5000, 10000, 50000} {
		b.Run(batchSizeName(batchSize), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				batch := make([]byte, 0, batchSize)
				for j := 0; j < batchSize; j++ {
					batch = append(batch, byte(j%256))
				}
				// Prevent compiler optimization
				if len(batch) != batchSize {
					b.Fatal("unexpected batch size")
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: Concurrent goroutine spawn/join (simulate processGroupsConcurrently)
// Measures: Goroutine management overhead for concurrent tx group processing
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkConcurrentGroupProcessing(b *testing.B) {
	for _, numGroups := range []int{1, 10, 50, 100} {
		b.Run(batchSizeName(numGroups), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				wg.Add(numGroups)
				results := make(chan int, numGroups)

				for g := 0; g < numGroups; g++ {
					go func(id int) {
						defer wg.Done()
						// Simulate minimal work per group
						sum := 0
						for k := 0; k < 100; k++ {
							sum += k
						}
						results <- sum
					}(g)
				}

				go func() {
					wg.Wait()
					close(results)
				}()

				total := 0
				for r := range results {
					total += r
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: Semaphore pattern (simulate MaxConcurrentWorkers pattern)
// Measures: Semaphore acquire/release overhead used in block batch creation
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkSemaphorePattern(b *testing.B) {
	for _, maxWorkers := range []int{10, 50, 100, 200} {
		b.Run(batchSizeName(maxWorkers), func(b *testing.B) {
			sem := make(chan struct{}, maxWorkers)

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					sem <- struct{}{} // acquire
					// Simulate minimal work
					x := 0
					for k := 0; k < 10; k++ {
						x += k
					}
					<-sem // release
					_ = x
				}
			})
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: Atomic counter throughput (simulate inputTxCounter pattern)
// Measures: Max TPS that atomic counters can support
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkAtomicCounter_Sequential(b *testing.B) {
	var counter int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		atomic.AddInt64(&counter, 1)
	}
}

func BenchmarkAtomicCounter_Concurrent(b *testing.B) {
	var counter int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			atomic.AddInt64(&counter, 1)
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark: End-to-end rate tracking simulation
// Measures: Combined TPS overhead — multiple goroutines adding txs + periodic reads
// ═══════════════════════════════════════════════════════════════════════════════
func BenchmarkE2E_RateTracking(b *testing.B) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	// Start a periodic reader (simulates the logging goroutine)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				trt.GetTransactionRate()
			case <-done:
				return
			}
		}
	}()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			trt.AddTransaction()
		}
	})
	b.StopTimer()
	close(done)
}

// helper
func batchSizeName(n int) string {
	switch {
	case n >= 1000:
		return string(rune('0'+n/1000)) + "k"
	default:
		s := ""
		if n >= 100 {
			s += string(rune('0' + n/100))
		}
		if n >= 10 {
			s += string(rune('0' + (n/10)%10))
		}
		s += string(rune('0' + n%10))
		return s
	}
}
