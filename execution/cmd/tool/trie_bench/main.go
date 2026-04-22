package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// BenchStorage wraps MemoryDB to satisfy the full storage.Storage interface
type BenchStorage struct {
	*storage.MemoryDB
}

func NewBenchStorage() *BenchStorage {
	return &BenchStorage{MemoryDB: storage.NewMemoryDb()}
}

func (b *BenchStorage) GetBackupPath() string { return "" }
func (db *BenchStorage) BatchDelete(keys [][]byte) error {
	return nil
}
func (db *BenchStorage) Flush() error {
	return nil
}

// commitWithTiming performs IntermediateRoot (Stage A) then Commit (Stage B)
// and returns timing for each stage plus the final root hash.
func commitWithTiming(asDB *account_state_db.AccountStateDB, count int) (
	stageADuration, stageBDuration time.Duration, rootHash common.Hash, err error,
) {
	// Stage A: IntermediateRoot(true) — acquires lock, marshals dirty accounts, updates trie
	stageAStart := time.Now()
	_, err = asDB.IntermediateRoot(true) // lockProcess=true → sets lockedFlag
	stageADuration = time.Since(stageAStart)
	if err != nil {
		return
	}

	// Stage B: Commit() — expects lockedFlag=true, calls IntermediateRoot(false) internally
	//          which is a no-op since dirty accounts already flushed,
	//          then does: trie.Commit + BatchPut + trie.New
	stageBStart := time.Now()
	rootHash, err = asDB.Commit()
	stageBDuration = time.Since(stageBStart)
	return
}

func main() {
	count := flag.Int("count", 1000, "Number of accounts to insert and commit")
	flag.Parse()

	logger.SetConfig(&logger.LoggerConfig{Flag: 0})

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  🔬 TRIE COMMIT BENCHMARK")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  Accounts: %d\n", *count)
	fmt.Printf("  Backend:  MemoryDB (in-memory)\n")
	fmt.Printf("  CPU:      %d cores\n", runtime.NumCPU())
	fmt.Println("═══════════════════════════════════════════════════")

	// ─── Setup: Create empty trie + AccountStateDB ────────────
	db := NewBenchStorage()
	trie, err := p_trie.New(common.Hash{}, db, true)
	if err != nil {
		log.Fatalf("Failed to create trie: %v", err)
	}
	asDB := account_state_db.NewAccountStateDB(trie, db)

	// ─── Phase 1: Generate random addresses ──────────────────
	fmt.Printf("\n📦 Generating %d random addresses...\n", *count)
	addresses := make([]common.Address, *count)
	blsKey := make([]byte, 48) // Standard BLS public key length
	rand.Read(blsKey)

	for i := 0; i < *count; i++ {
		var addr common.Address
		rand.Read(addr[:])
		addresses[i] = addr
	}
	fmt.Printf("  ✅ Generated %d addresses\n", *count)

	// ─── Phase 2: Insert accounts (SetPublicKeyBls) ──────────
	fmt.Printf("\n📝 Inserting %d accounts via SetPublicKeyBls...\n", *count)
	insertStart := time.Now()

	for i, addr := range addresses {
		if err := asDB.SetPublicKeyBls(addr, blsKey); err != nil {
			fmt.Printf("  ❌ Error at account %d: %v\n", i, err)
			return
		}
	}

	insertDuration := time.Since(insertStart)
	insertRate := float64(*count) / insertDuration.Seconds()
	fmt.Printf("  ✅ Inserted %d accounts in %s (%.0f acc/s)\n",
		*count, insertDuration.Round(time.Microsecond), insertRate)

	// ─── Phase 3: Full commit with stage breakdown ───────────
	fmt.Println("\n🔄 Committing trie (IntermediateRoot → Commit)...")

	stageA, stageB, rootHash, err := commitWithTiming(asDB, *count)
	if err != nil {
		fmt.Printf("  ❌ Commit failed: %v\n", err)
		return
	}

	totalCommit := stageA + stageB
	commitRate := float64(*count) / totalCommit.Seconds()

	fmt.Printf("  ├─ Stage A (IntermediateRoot): %s (%.0f acc/s)\n",
		stageA.Round(time.Microsecond), float64(*count)/stageA.Seconds())
	fmt.Printf("  │   └─ Marshal + trie.Update per account\n")
	fmt.Printf("  ├─ Stage B (Commit):           %s\n",
		stageB.Round(time.Microsecond))
	fmt.Printf("  │   └─ trie.Commit + BatchPut + trie.New\n")
	fmt.Printf("  ├─ Total commit:               %s (%.0f acc/s)\n",
		totalCommit.Round(time.Microsecond), commitRate)
	fmt.Printf("  └─ Root hash: %s\n", rootHash.Hex()[:18]+"...")

	// ─── Phase 4: Incremental commit test ────────────────────
	fmt.Println("\n📊 Incremental commit test (add 1 account to trie with", *count, "accounts)...")

	var singleAddr common.Address
	rand.Read(singleAddr[:])

	singleInsertStart := time.Now()
	asDB.SetPublicKeyBls(singleAddr, blsKey)
	singleInsertDuration := time.Since(singleInsertStart)

	singleA, singleB, singleHash, err := commitWithTiming(asDB, 1)
	if err != nil {
		fmt.Printf("  ❌ Single commit failed: %v\n", err)
		return
	}
	singleTotal := singleA + singleB

	fmt.Printf("  ├─ Insert 1 account:    %s\n", singleInsertDuration.Round(time.Microsecond))
	fmt.Printf("  ├─ IntermediateRoot:     %s\n", singleA.Round(time.Microsecond))
	fmt.Printf("  ├─ Commit:              %s\n", singleB.Round(time.Microsecond))
	fmt.Printf("  ├─ Total:               %s\n", singleTotal.Round(time.Microsecond))
	fmt.Printf("  └─ Root: %s\n", singleHash.Hex()[:18]+"...")

	// ─── Phase 5: Batch size scaling test ────────────────────
	fmt.Println("\n📈 Batch size scaling test...")
	fmt.Printf("  %-10s  %-14s  %-14s  %-14s  %-14s  %-12s\n",
		"Batch", "Insert", "IntRoot", "Commit", "Total", "Rate")
	fmt.Println("  ────────────────────────────────────────────────────────────────────────────────")

	batchSizes := []int{1, 10, 50, 100, 500, 1000}
	if *count >= 5000 {
		batchSizes = append(batchSizes, 5000)
	}
	if *count >= 10000 {
		batchSizes = append(batchSizes, 10000)
	}

	for _, batchSize := range batchSizes {
		// Create fresh trie
		bdb := NewBenchStorage()
		btrie, _ := p_trie.New(common.Hash{}, bdb, true)
		basDB := account_state_db.NewAccountStateDB(btrie, bdb)

		// Insert batch
		bInsertStart := time.Now()
		for i := 0; i < batchSize; i++ {
			var addr common.Address
			rand.Read(addr[:])
			basDB.SetPublicKeyBls(addr, blsKey)
		}
		bInsertDuration := time.Since(bInsertStart)

		// Commit batch
		bA, bB, _, err := commitWithTiming(basDB, batchSize)
		if err != nil {
			fmt.Printf("  %-10d  ERROR: %v\n", batchSize, err)
			continue
		}
		bTotal := bA + bB
		bRate := float64(batchSize) / bTotal.Seconds()

		fmt.Printf("  %-10d  %-14s  %-14s  %-14s  %-14s  %.0f acc/s\n",
			batchSize,
			bInsertDuration.Round(time.Microsecond),
			bA.Round(time.Microsecond),
			bB.Round(time.Microsecond),
			bTotal.Round(time.Microsecond),
			bRate)
	}

	// ─── Memory stats ────────────────────────────────────────
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("\n💾 Memory: Alloc=%dMB, Sys=%dMB, GC=%d\n",
		m.Alloc/1024/1024, m.Sys/1024/1024, m.NumGC)

	// ─── Summary ─────────────────────────────────────────────
	fmt.Println("\n═══════════════════════════════════════════════════")
	fmt.Println("  📊 SUMMARY")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  %-30s  %d\n", "Accounts:", *count)
	fmt.Printf("  %-30s  %s (%.0f acc/s)\n", "Insert time:", insertDuration.Round(time.Microsecond), insertRate)
	fmt.Printf("  %-30s  %s (%.0f acc/s)\n", "Full commit time:", totalCommit.Round(time.Microsecond), commitRate)
	fmt.Printf("  %-30s  %s\n", "  └ IntermediateRoot:", stageA.Round(time.Microsecond))
	fmt.Printf("  %-30s  %s\n", "  └ Commit:", stageB.Round(time.Microsecond))
	fmt.Printf("  %-30s  %s\n", "Per-account commit:", (totalCommit / time.Duration(*count)).Round(time.Microsecond))

	// Theoretical TPS limit
	theoreticalTPS := float64(*count) / totalCommit.Seconds()
	observedTPS := 167.0
	fmt.Printf("  %-30s  %.0f tx/s\n", "Theoretical max TPS:", theoreticalTPS)
	fmt.Printf("  %-30s  %.0f tx/s\n", "Observed processing TPS:", observedTPS)

	if theoreticalTPS > observedTPS*3 {
		fmt.Println("\n  💡 Trie commit is NOT the primary bottleneck.")
		fmt.Println("     Look at: tx execution, block formation, consensus round-trip.")
	} else {
		fmt.Println("\n  ⚠️  Trie commit IS likely the bottleneck!")
		fmt.Println("     Optimizations: batch trie updates, skip intermediate commits, parallel hashing.")
	}
	fmt.Println("═══════════════════════════════════════════════════")

	_ = hex.EncodeToString(nil)
}
