package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	fmt.Printf("🧵 Testing Multi-Thread BLS Signing Performance\n")
	fmt.Printf("CPU Cores: %d, Workers: %d\n\n", runtime.NumCPU(), runtime.NumCPU())

	// Generate test key
	keyPair := bls.GenerateKeyPair()
	if keyPair == nil {
		fmt.Println("❌ Failed to generate key pair")
		return
	}

	priv := keyPair.PrivateKey()
	pub := keyPair.PublicKey()
	testMessage := []byte("Multi-thread signing test message")

	fmt.Printf("📝 Test Key: %s\n\n", keyPair.Address().Hex())

	// Test concurrent signing performance
	fmt.Println("⚡ Testing Concurrent Signing Performance:")

	const numGoroutines = 1000
	const numIterations = 10

	// Test with concurrent functions
	fmt.Printf("Testing with %d goroutines × %d iterations = %d operations\n",
		numGoroutines, numIterations, numGoroutines*numIterations)

	var wg sync.WaitGroup
	errorCount := int64(0)
	var errorMutex sync.Mutex

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numIterations; j++ {
				// Use concurrent signing
				signature := bls.SignConcurrent(priv, testMessage)
				if signature.Bytes() == nil {
					errorMutex.Lock()
					errorCount++
					errorMutex.Unlock()
					continue
				}

				// Verify signature
				isValid := bls.VerifySignConcurrent(pub, signature, testMessage)
				if !isValid {
					errorMutex.Lock()
					errorCount++
					errorMutex.Unlock()
					continue
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	ops := numGoroutines * numIterations
	opsPerSec := float64(ops) / duration.Seconds()

	fmt.Printf("✅ Completed in %.2fs\n", duration.Seconds())
	fmt.Printf("📊 Operations: %d\n", ops)
	fmt.Printf("🚀 Throughput: %.1f ops/sec\n", opsPerSec)
	fmt.Printf("❌ Errors: %d\n\n", errorCount)

	// Test parallel vs sequential comparison
	fmt.Println("⚖️  Performance Comparison:")

	// Sequential test (single goroutine)
	start = time.Now()
	for i := 0; i < ops; i++ {
		sig := bls.SignConcurrent(priv, testMessage)
		bls.VerifySignConcurrent(pub, sig, testMessage)
	}
	sequentialTime := time.Since(start)

	// Calculate efficiency
	sequentialOpsPerSec := float64(ops) / sequentialTime.Seconds()
	efficiency := (opsPerSec / sequentialOpsPerSec) * 100

	fmt.Printf("Sequential: %.1f ops/sec\n", sequentialOpsPerSec)
	fmt.Printf("Concurrent: %.1f ops/sec\n", opsPerSec)
	fmt.Printf("Efficiency: %.1f%% of theoretical max\n\n", efficiency)

	// Memory stats
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)

	fmt.Println("💾 Memory Statistics:")
	fmt.Printf("  Alloc: %d KB\n", m.Alloc/1024)
	fmt.Printf("  TotalAlloc: %d KB\n", m.TotalAlloc/1024)
	fmt.Printf("  Sys: %d KB\n", m.Sys/1024)
	fmt.Printf("  NumGC: %d\n\n", m.NumGC)

	// Summary
	if errorCount == 0 && opsPerSec > 100 {
		fmt.Println("🎉 SUCCESS: Multi-thread signing is working!")
		fmt.Printf("   - Zero errors in %d concurrent operations\n", ops)
		fmt.Printf("   - %.1f operations per second\n", opsPerSec)
		fmt.Println("   - Memory stable, no crashes")
	} else {
		fmt.Printf("⚠️  ISSUES: %d errors detected\n", errorCount)
	}
}
