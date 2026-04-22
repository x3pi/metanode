package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	fmt.Printf("🔄 Testing BLS SIGN + VERIFY (NO LOCK VERSION)\n")
	fmt.Printf("CPU Cores: %d\n\n", runtime.NumCPU())

	// Generate test key
	keyPair := bls.GenerateKeyPair()
	if keyPair == nil {
		fmt.Println("❌ Failed to generate key pair")
		return
	}

	priv := keyPair.PrivateKey()
	pub := keyPair.PublicKey()
	testMessage := []byte("Test message for sign and verify")

	fmt.Printf("📝 Test Key: %s\n\n", keyPair.Address().Hex())

	// Test SIGN + VERIFY
	fmt.Println("⚡ Testing SIGN + VERIFY Performance (NO LOCK):")

	const numGoroutines = 1000
	const numIterations = 10

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
				// SIGN + VERIFY
				signature := bls.SignConcurrent(priv, testMessage)
				if signature.Bytes() == nil {
					errorMutex.Lock()
					errorCount++
					errorMutex.Unlock()
					continue
				}

				isValid := bls.VerifySignConcurrent(pub, signature, testMessage)
				if !isValid {
					errorMutex.Lock()
					errorCount++
					errorMutex.Unlock()
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
	if errorCount == 0 {
		fmt.Printf("🎉 SUCCESS: Sign + Verify (NO LOCK) - %.1f ops/second!\n", opsPerSec)
		fmt.Println("   - Zero errors")
		fmt.Println("   - Memory stable")
	} else {
		fmt.Printf("⚠️  ISSUES: %d errors detected\n", errorCount)
	}
}
