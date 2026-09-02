package transaction_pool

import (
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func makeTestTx(fromByte byte, nonce uint64) types.Transaction {
	from := common.Address{}
	from[0] = fromByte
	to := common.Address{}
	to[0] = 0xFF

	return transaction.NewTransaction(
		from,
		to,
		big.NewInt(100),
		21000,         // maxGas
		1,             // maxGasPrice
		0,             // maxTimeUse
		nil,           // data
		nil,           // relatedAddresses
		common.Hash{}, // lastDeviceKey
		common.Hash{}, // newDeviceKey
		nonce,
		1, // chainId
	)
}

// ──────────────────────────────────────────────
// Constructor
// ──────────────────────────────────────────────

func TestNewTransactionPool(t *testing.T) {
	pool := NewTransactionPool()
	require.NotNil(t, pool)
	assert.Equal(t, 0, pool.CountTransactions())
}

// ──────────────────────────────────────────────
// AddTransaction
// ──────────────────────────────────────────────

func TestAddTransaction_Single(t *testing.T) {
	pool := NewTransactionPool()
	tx := makeTestTx(0x01, 0)

	err := pool.AddTransaction(tx)
	require.NoError(t, err)
	assert.Equal(t, 1, pool.CountTransactions())
}

func TestAddTransaction_Duplicate(t *testing.T) {
	pool := NewTransactionPool()
	tx := makeTestTx(0x01, 0)

	err := pool.AddTransaction(tx)
	require.NoError(t, err)

	err = pool.AddTransaction(tx)
	assert.Error(t, err, "duplicate transaction should be rejected")
	assert.Equal(t, 1, pool.CountTransactions(), "count should still be 1")
}

func TestAddTransaction_SameAddressDifferentNonce(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x01, 1)

	require.NoError(t, pool.AddTransaction(tx1))
	require.NoError(t, pool.AddTransaction(tx2))
	assert.Equal(t, 2, pool.CountTransactions())
}

func TestAddTransaction_DifferentAddressSameNonce(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x02, 0)

	require.NoError(t, pool.AddTransaction(tx1))
	require.NoError(t, pool.AddTransaction(tx2))
	assert.Equal(t, 2, pool.CountTransactions())
}

// ──────────────────────────────────────────────
// AddTransactions (batch)
// ──────────────────────────────────────────────

func TestAddTransactions_Batch(t *testing.T) {
	pool := NewTransactionPool()
	txs := []types.Transaction{
		makeTestTx(0x01, 0),
		makeTestTx(0x02, 0),
		makeTestTx(0x03, 0),
	}

	pool.AddTransactions(txs)
	assert.Equal(t, 3, pool.CountTransactions())
}

func TestAddTransactions_BatchWithDuplicates(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x02, 0)
	tx3 := makeTestTx(0x01, 0) // same from+nonce as tx1

	// Batch add with internal duplicates — AddTransactions deduplicates within the batch
	pool.AddTransactions([]types.Transaction{tx1, tx2, tx3})
	assert.Equal(t, 2, pool.CountTransactions(), "duplicate within batch should be skipped")
}

func TestAddTransactions_EmptyBatch(t *testing.T) {
	pool := NewTransactionPool()
	pool.AddTransactions([]types.Transaction{})
	assert.Equal(t, 0, pool.CountTransactions())
}

// ──────────────────────────────────────────────
// TransactionsWithAggSign
// ──────────────────────────────────────────────

func TestTransactionsWithAggSign_ReturnsAndClears(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x02, 0)

	require.NoError(t, pool.AddTransaction(tx1))
	require.NoError(t, pool.AddTransaction(tx2))

	txs, aggSign := pool.TransactionsWithAggSign()
	assert.Len(t, txs, 2, "should return 2 transactions")
	assert.Nil(t, aggSign, "aggSign is currently nil per implementation")

	// Pool should be empty after drain
	assert.Equal(t, 0, pool.CountTransactions(), "pool should be empty after drain")
}

func TestTransactionsWithAggSign_EmptyPool(t *testing.T) {
	pool := NewTransactionPool()

	txs, aggSign := pool.TransactionsWithAggSign()
	assert.Empty(t, txs, "empty pool should return empty slice")
	assert.Nil(t, aggSign)
}

func TestTransactionsWithAggSign_CanReAddAfterDrain(t *testing.T) {
	pool := NewTransactionPool()

	require.NoError(t, pool.AddTransaction(makeTestTx(0x01, 0)))
	txs, _ := pool.TransactionsWithAggSign()
	assert.Len(t, txs, 1)

	// Should be able to add new transactions after drain
	require.NoError(t, pool.AddTransaction(makeTestTx(0x02, 0)))
	assert.Equal(t, 1, pool.CountTransactions())
}

func TestTransactionsWithAggSign_RotatesDrainStartAcrossCalls(t *testing.T) {
	pool := NewTransactionPool()

	// One tx in shard 0x00 and one in shard 0x01, re-added identically before
	// every drain so each call sees the exact same two-shard pool.
	refill := func() {
		require.NoError(t, pool.AddTransaction(makeTestTx(0x00, 0)))
		require.NoError(t, pool.AddTransaction(makeTestTx(0x01, 0)))
	}

	firstAddrByte := func() byte {
		txs, _ := pool.TransactionsWithAggSign()
		require.Len(t, txs, 2)
		return txs[0].FromAddress()[0]
	}

	refill()
	first := firstAddrByte()

	// Regression guard for the pre-fix behavior: draining always started at
	// shard 0, so the first element was 0x00 on every single call. That's
	// exactly what let a low-numbered shard's traffic perpetually crowd out
	// a higher-numbered shard's under the caller's per-tick cap (see
	// TransactionsWithAggSign's comment) — reproduced live as a single
	// transaction waiting 10+ seconds (~30-40x the normal ~300ms) behind an
	// unrelated 1000-worker burst. The rotating cursor must eventually make
	// shard 0x01 come first instead.
	sawOtherFirst := false
	for i := 0; i < NumShards+1; i++ {
		refill()
		if firstAddrByte() != first {
			sawOtherFirst = true
			break
		}
	}
	assert.True(t, sawOtherFirst,
		"drain start must rotate across calls — got the same first shard every time in a full cycle")
}

// ──────────────────────────────────────────────
// GetTransactionByHash
// ──────────────────────────────────────────────

func TestGetTransactionByHash_ZeroHash(t *testing.T) {
	pool := NewTransactionPool()
	tx, found := pool.GetTransactionByHash(common.Hash{})
	assert.Nil(t, tx)
	assert.False(t, found, "zero hash should return not found")
}

func TestGetTransactionByHash_NotFound(t *testing.T) {
	pool := NewTransactionPool()
	require.NoError(t, pool.AddTransaction(makeTestTx(0x01, 0)))

	randomHash := common.HexToHash("0xdeadbeefdeadbeef")
	found, ok := pool.GetTransactionByHash(randomHash)
	assert.Nil(t, found)
	assert.False(t, ok)
}

// ──────────────────────────────────────────────
// Concurrent Tests
// ──────────────────────────────────────────────

func TestConcurrent_AddAndDrain(t *testing.T) {
	pool := NewTransactionPool()
	const numGoroutines = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			tx := makeTestTx(byte(idx), 0)
			_ = pool.AddTransaction(tx) // ignore duplicate errors
		}(i)
	}

	wg.Wait()

	// Drain and verify
	txs, _ := pool.TransactionsWithAggSign()
	assert.Equal(t, numGoroutines, len(txs),
		"all unique transactions should be in pool")
	assert.Equal(t, 0, pool.CountTransactions(), "pool should be empty after drain")
}

// Regression test for a 2026-09-02 production incident: EvictLowestGasPrice's
// per-shard removal used to size newTxs as
// len(shard.transactions)-len(hashesToRemove), where hashesToRemove was decided
// from an earlier RLock snapshot pass taken *before* this removal pass's write
// lock. TransactionsWithAggSign (the block-forming drain, run continuously by
// its own goroutine in production) can fully empty a shard in between those two
// passes -- nothing coordinates the two. When that race hit, shard.transactions
// was already empty here while hashesToRemove still held entries from the stale
// scan, so the subtraction went negative and
// make([]types.Transaction, 0, <negative>) panicked. Because the unlock wasn't
// deferred, that panic (recovered per-request by net/http, so the process
// itself survived) left that one shard's mutex locked forever -- wedging every
// later caller of AddTransaction/TransactionsWithAggSign/EvictLowestGasPrice
// that happened to hash to the same shard. In production this was the real
// root cause of a multi-minute full consensus stall with no crash and no error
// visible from the Rust side (Rust's DAG kept committing empty rounds normally
// throughout; the hang was entirely downstream in Go's mempool).
// This hammers AddTransaction, EvictLowestGasPrice, and TransactionsWithAggSign
// concurrently against a small, heavily-shared set of shards to make that race
// window likely to hit on every run; the only correctness requirement is that
// none of it ever panics (an unrecovered panic in any goroutine here fails the
// whole test binary).
func TestConcurrent_EvictAndDrain_NoPanic(t *testing.T) {
	pool := NewTransactionPool()
	const numAddrs = 64
	const perAddrTxs = 500
	const iterations = 300

	var wg sync.WaitGroup

	// Adders: each address injects a burst of sequential-nonce txs, so there's
	// always fresh material for both the drainer and the evictor to race over.
	for i := 0; i < numAddrs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for n := uint64(0); n < perAddrTxs; n++ {
				_ = pool.AddTransaction(makeTestTx(byte(idx), n))
			}
		}(i)
	}

	// Drainer: repeatedly does exactly what the block-forwarding loop does in
	// production -- pull everything currently queued.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			pool.TransactionsWithAggSign()
		}
	}()

	// Evictor: repeatedly evicts small batches, same as the mempool-full path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			pool.EvictLowestGasPrice(numAddrs)
		}
	}()

	wg.Wait() // must return without a panic anywhere above
}

func TestConcurrent_AddAndCount(t *testing.T) {
	pool := NewTransactionPool()

	var wg sync.WaitGroup
	const writers = 20
	const readers = 10

	wg.Add(writers + readers)

	for i := 0; i < writers; i++ {
		go func(idx int) {
			defer wg.Done()
			tx := makeTestTx(byte(idx), 0)
			_ = pool.AddTransaction(tx)
		}(i)
	}

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			_ = pool.CountTransactions() // should not panic
		}()
	}

	wg.Wait()
	assert.Equal(t, writers, pool.CountTransactions())
}
