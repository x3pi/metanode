package account_state_db

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// ══════════════════════════════════════════════════════════════════════════════
// Benchmarks for AccountStateDB hot-path operations
// ══════════════════════════════════════════════════════════════════════════════

func benchAddr(i int) common.Address {
	addr := common.Address{}
	addr[0] = byte(i >> 8)
	addr[1] = byte(i)
	return addr
}

// newBenchDB creates an AccountStateDB for benchmarks.
func newBenchDB(b *testing.B) *AccountStateDB {
	b.Helper()
	db := &testMemoryDB{MemoryDB: storage.NewMemoryDb()}
	tr, err := p_trie.New(common.Hash{}, db, true)
	if err != nil {
		b.Fatal("failed to create test trie:", err)
	}
	adb := NewAccountStateDB(tr, db)
	if adb == nil {
		b.Fatal("NewAccountStateDB returned nil")
	}
	return adb
}

func BenchmarkAddBalance(b *testing.B) {
	db := newBenchDB(b)
	addr := benchAddr(0)
	amount := big.NewInt(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = db.AddBalance(addr, amount)
	}
}

func BenchmarkPlusOneNonce(b *testing.B) {
	db := newBenchDB(b)
	addr := benchAddr(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = db.PlusOneNonce(addr)
	}
}

func BenchmarkAccountState_Read(b *testing.B) {
	db := newBenchDB(b)
	addr := benchAddr(0)

	_ = db.AddBalance(addr, big.NewInt(1000))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = db.AccountState(addr)
	}
}

// BenchmarkCommit_10Accounts benchmarks the full IntermediateRoot+Commit cycle.
// Each iteration creates a fresh DB because Commit has lock lifecycle requirements.
func BenchmarkCommit_10Accounts(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db := newBenchDB(b)
		for j := 0; j < 10; j++ {
			_ = db.AddBalance(benchAddr(j), big.NewInt(int64(j+1)*1000))
		}
		// IntermediateRoot(true) locks; Commit() expects locked state
		_, _ = db.IntermediateRoot(true)
		b.StartTimer()

		_, _ = db.Commit()
	}
}
