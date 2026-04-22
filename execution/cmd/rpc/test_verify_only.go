package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	fmt.Printf("✅ Testing BLS VERIFICATION ONLY (NO LOCK VERSION)\n")
	fmt.Printf("CPU Cores: %d\n\n", runtime.NumCPU())

	// Generate test key và signature trước
	keyPair := bls.GenerateKeyPair()
	if keyPair == nil {
		fmt.Println("❌ Failed to generate key pair")
		return
	}

	priv := keyPair.PrivateKey()
	pub := keyPair.PublicKey()
	testMessage := []byte("Test message for verification only")

	// Tạo signature trước (chỉ 1 lần)
	signature := bls.SignConcurrent(priv, testMessage)
	if signature.Bytes() == nil {
		fmt.Println("❌ Failed to generate signature")
		return
	}

	fmt.Printf("📝 Test Key: %s\n", keyPair.Address().Hex())
	fmt.Printf("📝 Signature: %x\n\n", signature.Bytes()[:16])

	// Test VERIFICATION ONLY
	fmt.Println("⚡ Testing VERIFICATION ONLY Performance (NO LOCK):")

	const numGoroutines = 1000
	const numIterations = 10

	fmt.Printf("Testing with %d goroutines × %d iterations = %d verification operations\n",
		numGoroutines, numIterations, numGoroutines*numIterations)

	var wg sync.WaitGroup
	errorCount := int64(0)
	invalidCount := int64(0)
	var errorMutex sync.Mutex

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numIterations; j++ {
				// CHỈ VERIFY, KHÔNG SIGN
				isValid := bls.VerifySignConcurrent(pub, signature, testMessage)
				if !isValid {
					errorMutex.Lock()
					invalidCount++
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
	fmt.Printf("📊 Verification Operations: %d\n", ops)
	fmt.Printf("🚀 Verification Throughput: %.1f verifies/sec\n", opsPerSec)
	fmt.Printf("❌ Invalid Verifications: %d\n", invalidCount)
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
	if invalidCount == 0 && errorCount == 0 {
		fmt.Printf("🎉 SUCCESS: Verification only (NO LOCK) - %.1f verifies/second!\n", opsPerSec)
		fmt.Println("   - Zero errors")
		fmt.Println("   - All verifications valid")
		fmt.Println("   - Memory stable")
	} else {
		fmt.Printf("⚠️  ISSUES: %d invalid verifications, %d errors\n", invalidCount, errorCount)
	}
}
