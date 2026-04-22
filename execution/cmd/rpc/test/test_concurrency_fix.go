package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	fmt.Printf("Testing BLS concurrency fix with %d goroutines\n", runtime.NumCPU()*10)

	// Test data
	testMessage := []byte("test message for signing")

	// Generate test key pair using random generation
	keyPair := bls.GenerateKeyPair()
	if keyPair == nil {
		fmt.Println("ERROR: Failed to generate key pair")
		return
	}
	priv := keyPair.PrivateKey()
	pub := keyPair.PublicKey()
	addr := keyPair.Address()

	fmt.Printf("Test key generated - Address: %s\n", addr.Hex())

	// Test concurrent signing
	fmt.Println("Testing concurrent BLS signing...")

	const numGoroutines = 100
	const numIterations = 50

	var wg sync.WaitGroup
	errorCount := int64(0)
	var errorMutex sync.Mutex

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numIterations; j++ {
				// Test signing
				signature := bls.Sign(priv, testMessage)
				if signature.Bytes() == nil {
					errorMutex.Lock()
					errorCount++
					errorMutex.Unlock()
					continue
				}

				// Test verification
				isValid := bls.VerifySign(pub, signature, testMessage)
				if !isValid {
					errorMutex.Lock()
					errorCount++
					errorMutex.Unlock()
					continue
				}

				// Small delay to simulate real usage
				if j%10 == 0 {
					time.Sleep(time.Millisecond)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	fmt.Printf("Test completed in %v\n", duration)
	fmt.Printf("Total operations: %d\n", numGoroutines*numIterations)
	fmt.Printf("Operations per second: %.2f\n", float64(numGoroutines*numIterations)/duration.Seconds())
	fmt.Printf("Errors: %d\n", errorCount)

	if errorCount == 0 {
		fmt.Println("✅ SUCCESS: No errors during concurrent BLS operations!")
		fmt.Println("✅ BLS concurrency fix is working correctly.")
	} else {
		fmt.Printf("❌ FAILURE: %d errors occurred during testing\n", errorCount)
	}

	// Memory stats
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	fmt.Printf("\nMemory stats:\n")
	fmt.Printf("  Alloc: %d KB\n", m.Alloc/1024)
	fmt.Printf("  TotalAlloc: %d KB\n", m.TotalAlloc/1024)
	fmt.Printf("  Sys: %d KB\n", m.Sys/1024)
	fmt.Printf("  NumGC: %d\n", m.NumGC)
}
