package transaction_pool

import (
	"math/big"
	"sort"
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

	txs, aggSign := pool.TransactionsWithAggSign(0)
	assert.Len(t, txs, 2, "should return 2 transactions")
	assert.Nil(t, aggSign, "aggSign is currently nil per implementation")

	// Pool should be empty after drain
	assert.Equal(t, 0, pool.CountTransactions(), "pool should be empty after drain")
}

func TestTransactionsWithAggSign_EmptyPool(t *testing.T) {
	pool := NewTransactionPool()

	txs, aggSign := pool.TransactionsWithAggSign(0)
	assert.Empty(t, txs, "empty pool should return empty slice")
	assert.Nil(t, aggSign)
}

func TestTransactionsWithAggSign_CanReAddAfterDrain(t *testing.T) {
	pool := NewTransactionPool()

	require.NoError(t, pool.AddTransaction(makeTestTx(0x01, 0)))
	txs, _ := pool.TransactionsWithAggSign(0)
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
		txs, _ := pool.TransactionsWithAggSign(0)
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

// Regression test for the fix found 2026-09-02 measuring sustained
// real-transfer throughput: TransactionsWithAggSign() used to have no way to
// stop early, so with a huge backlog it re-drained (and the caller then
// re-sorted, re-nonce-validated, and mostly re-queued) the ENTIRE pool on
// every single tick to make progress on only a small slice of it -- O(n)
// work per tick regardless of how much of it was actually used, measured at
// 16-24 SECONDS per tick against a 3.7-million-tx backlog. This verifies the
// maxDrain parameter added to fix that: a single capped call must not return
// more than roughly maxDrain transactions, and repeated capped calls (as
// TxBatchForwarder makes every tick) must together return every originally
// queued transaction exactly once -- no duplicates, nothing skipped -- by
// resuming from wherever the previous call's cursor left off.
func TestTransactionsWithAggSign_MaxDrainCapsAndResumesAcrossCalls(t *testing.T) {
	pool := NewTransactionPool()
	const numAddrs = 200
	const maxDrain = 50

	seeded := make(map[common.Hash]bool, numAddrs)
	for i := 0; i < numAddrs; i++ {
		tx := makeTestTx(byte(i%256), uint64(i/256))
		require.NoError(t, pool.AddTransaction(tx))
		seeded[tx.Hash()] = true
	}
	require.Len(t, seeded, numAddrs, "test fixture sanity: every seeded tx must be unique")

	seen := make(map[common.Hash]bool, numAddrs)
	// Bounded loop: each call drains a small slice of the shard space, so
	// completing a full rotation takes multiple calls but must terminate
	// well within NumShards+1 of them (matching the rotation test above).
	for i := 0; i < NumShards+1 && len(seen) < numAddrs; i++ {
		txs, _ := pool.TransactionsWithAggSign(maxDrain)
		for _, tx := range txs {
			h := tx.Hash()
			require.False(t, seen[h], "the same transaction was returned twice across capped calls")
			seen[h] = true
		}
	}

	assert.Len(t, seen, numAddrs,
		"repeated capped calls must together return every originally queued transaction exactly once")
	assert.Equal(t, 0, pool.CountTransactions(), "pool must be fully drained once every transaction has been seen")
}

// White-box companion to the test above, pinning down the exact cursor
// arithmetic rather than just observing convergence over many calls (a naive
// "always advance by 1" bug would still eventually visit every shard within
// NumShards+1 calls, just wastefully re-scanning mostly-already-drained
// ground each time -- that regression wouldn't reliably show up as a failure
// in a call-count-bounded loop, only as pointless extra work). makeTestTx
// only varies address byte 0, so with getShardIndex hashing bytes 0 and 1,
// fromByte N lands in shard N*256 exactly -- letting this compute precisely
// how many shards a capped drain must visit to collect a known count of
// transactions, and assert the cursor lands exactly there afterward.
func TestTransactionsWithAggSign_CursorAdvancesByShardsActuallyVisited(t *testing.T) {
	pool := NewTransactionPool()
	const seededAddrs = 50 // fromByte 0..49 -> shards 0, 256, 512, ..., 12544
	const maxDrain = 10    // the 10th transaction (fromByte 9) sits at shard 9*256=2304

	for i := 0; i < seededAddrs; i++ {
		require.NoError(t, pool.AddTransaction(makeTestTx(byte(i), 0)))
	}

	txs, _ := pool.TransactionsWithAggSign(maxDrain)
	assert.Len(t, txs, maxDrain, "a call over a dense-enough pool must return exactly maxDrain, not more or fewer")

	wantCursor := uint32(9*256 + 1) // one past the shard the 10th (last-needed) tx was found in
	assert.Equal(t, wantCursor, pool.drainCursor,
		"cursor must advance by exactly the shards visited to collect maxDrain transactions, "+
			"not by a fixed amount -- otherwise the next call re-scans mostly-already-drained ground "+
			"instead of picking up where this one stopped")
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
	txs, _ := pool.TransactionsWithAggSign(0)
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
			pool.TransactionsWithAggSign(0)
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

// Regression test for a gap found 2026-09-02 measuring sustained real-transfer
// throughput: EvictLowestGasPrice sorted purely by gas price ascending, so
// with equal-gas-price transactions (the common case for same-priority
// transfers, and exactly what that benchmark used) which specific
// transactions got evicted at the pool's cap was effectively arbitrary with
// respect to nonce. Evicting a low-nonce transaction while a same-sender
// higher-nonce one survives permanently strands that higher nonce -- it can
// never execute out of order, so it just occupies pool space as an eternal
// future-tx instead of being usefully evicted itself. This verifies the fix:
// for a single sender queued at nonces 0..9, evicting a count that removes
// some-but-not-all of them must always remove exactly the highest nonces,
// leaving a contiguous gap-free run starting at nonce 0.
func TestEvictLowestGasPrice_PrefersHighestNonceOnGasTie(t *testing.T) {
	pool := NewTransactionPool()
	const sender = byte(0x01)
	const total = 10

	for n := uint64(0); n < total; n++ {
		require.NoError(t, pool.AddTransaction(makeTestTx(sender, n)))
	}

	const evictCount = 4
	evicted := pool.EvictLowestGasPrice(evictCount)
	assert.Equal(t, evictCount, evicted)

	remaining, _ := pool.TransactionsWithAggSign(0)
	require.Len(t, remaining, total-evictCount)

	remainingNonces := make([]uint64, 0, len(remaining))
	for _, tx := range remaining {
		remainingNonces = append(remainingNonces, tx.GetNonce())
	}
	sort.Slice(remainingNonces, func(i, j int) bool { return remainingNonces[i] < remainingNonces[j] })

	expected := make([]uint64, total-evictCount)
	for i := range expected {
		expected[i] = uint64(i) // 0..5: the lowest nonces, a contiguous run with no internal gap
	}
	assert.Equal(t, expected, remainingNonces,
		"eviction must remove the highest nonces first, leaving a gap-free run starting at 0")
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
