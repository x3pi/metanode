package transaction_pool

import (
	"errors"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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
		txs, dID, _ := pool.TransactionsWithDrainID(0)
		require.Len(t, txs, 2)
		pool.ReleaseInFlightWithDrainID(txs, dID)
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

func makeTestTxWithGas(fromByte byte, nonce uint64, gasPrice uint64, data []byte) types.Transaction {
	from := common.Address{}
	from[0] = fromByte
	to := common.Address{}
	to[0] = 0xFF

	return transaction.NewTransaction(
		from,
		to,
		big.NewInt(100),
		21000,         // maxGas
		gasPrice,      // maxGasPrice
		0,             // maxTimeUse
		data,          // data (differentiates hash)
		nil,           // relatedAddresses
		common.Hash{}, // lastDeviceKey
		common.Hash{}, // newDeviceKey
		nonce,
		1, // chainId
	)
}

func TestTransactionPool_ReplacementTx_IdenticalHash(t *testing.T) {
	pool := NewTransactionPool()
	tx := makeTestTxWithGas(0x01, 0, 100, []byte{0x01})

	require.NoError(t, pool.AddTransaction(tx))
	err := pool.AddTransaction(tx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, transaction.ErrAlreadyKnown), "identical tx must return ErrAlreadyKnown")
}

func TestTransactionPool_ReplacementTx_Underpriced(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTxWithGas(0x01, 0, 100, []byte{0x01})
	require.NoError(t, pool.AddTransaction(tx1))

	// 5% bump: 105 < 110 threshold
	tx2 := makeTestTxWithGas(0x01, 0, 105, []byte{0x02})
	err := pool.AddTransaction(tx2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, transaction.ErrReplacementUnderpriced), "underpriced bump must return ErrReplacementUnderpriced")

	// Lower price: 90 < 110
	tx3 := makeTestTxWithGas(0x01, 0, 90, []byte{0x03})
	err = pool.AddTransaction(tx3)
	require.Error(t, err)
	assert.True(t, errors.Is(err, transaction.ErrReplacementUnderpriced), "lower price must return ErrReplacementUnderpriced")

	// Pool count still 1
	assert.Equal(t, 1, pool.CountTransactions())
}

func TestTransactionPool_ReplacementTx_Success(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTxWithGas(0x01, 0, 100, []byte{0x01})
	require.NoError(t, pool.AddTransaction(tx1))

	// 15% bump: 115 >= 110 threshold
	tx2 := makeTestTxWithGas(0x01, 0, 115, []byte{0x02})
	require.NoError(t, pool.AddTransaction(tx2))

	// Count remains 1
	assert.Equal(t, 1, pool.CountTransactions())

	// tx1 removed from hash map, tx2 present
	_, foundOld := pool.GetTransactionByHash(tx1.Hash())
	assert.False(t, foundOld, "old tx hash should be removed from txHashMap")

	foundNewTx, foundNew := pool.GetTransactionByHash(tx2.Hash())
	assert.True(t, foundNew, "new tx hash should be present in txHashMap")
	assert.Equal(t, uint64(115), foundNewTx.MaxGasPrice())
}

func TestTransactionPool_BatchReplacement(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTxWithGas(0x01, 0, 100, []byte{0x01})
	require.NoError(t, pool.AddTransaction(tx1))

	// Batch replace with 20% bump
	tx2 := makeTestTxWithGas(0x01, 0, 120, []byte{0x02})
	pool.AddTransactions([]types.Transaction{tx2})

	assert.Equal(t, 1, pool.CountTransactions())
	foundNewTx, foundNew := pool.GetTransactionByHash(tx2.Hash())
	assert.True(t, foundNew)
	assert.Equal(t, uint64(120), foundNewTx.MaxGasPrice())
}

func TestTransactionPool_GetPendingNonce_Consecutive(t *testing.T) {
	pool := NewTransactionPool()
	fromByte := byte(0x42)
	addr := common.Address{0: fromByte}

	// Pool empty
	assert.Equal(t, uint64(5), pool.GetPendingNonce(addr, 5))

	// Add 5 consecutive txs: nonces 5, 6, 7, 8, 9
	for n := uint64(5); n < 10; n++ {
		require.NoError(t, pool.AddTransaction(makeTestTxWithGas(fromByte, n, 10, []byte{byte(n)})))
	}

	assert.Equal(t, 5, pool.GetPendingCount(addr))
	assert.Equal(t, uint64(10), pool.GetPendingNonce(addr, 5))
}

func TestTransactionPool_GetPendingNonce_NonceGap(t *testing.T) {
	pool := NewTransactionPool()
	fromByte := byte(0x43)
	addr := common.Address{0: fromByte}

	// Add nonces 0, 1, 3 (gap at nonce 2)
	require.NoError(t, pool.AddTransaction(makeTestTxWithGas(fromByte, 0, 10, []byte{0})))
	require.NoError(t, pool.AddTransaction(makeTestTxWithGas(fromByte, 1, 10, []byte{1})))
	require.NoError(t, pool.AddTransaction(makeTestTxWithGas(fromByte, 3, 10, []byte{3})))

	// Starts from 0: sees 0, 1, then stops at gap 2
	assert.Equal(t, uint64(2), pool.GetPendingNonce(addr, 0))

	// Fill the gap by adding nonce 2
	require.NoError(t, pool.AddTransaction(makeTestTxWithGas(fromByte, 2, 10, []byte{2})))

	// Now sees 0, 1, 2, 3 -> returns 4
	assert.Equal(t, uint64(4), pool.GetPendingNonce(addr, 0))
}

func TestTransactionPool_GetPendingNonce_StaleMempoolTx(t *testing.T) {
	pool := NewTransactionPool()
	fromByte := byte(0x44)
	addr := common.Address{0: fromByte}

	// Pool has nonces 3, 4, 5, 6
	for n := uint64(3); n <= 6; n++ {
		require.NoError(t, pool.AddTransaction(makeTestTxWithGas(fromByte, n, 10, []byte{byte(n)})))
	}

	// On-chain state nonce is already 5 (nonces 3 and 4 were already mined on chain)
	// Progression starts at 5: sees 5, 6 -> returns 7
	assert.Equal(t, uint64(7), pool.GetPendingNonce(addr, 5))
}

// TestEvictLowestGasPrice_BigIntFeeCapNoTruncation ensures that an EIP-1559 transaction
// with GasPriceCap > 2^64 does not get truncated down to a tiny uint64 and wrongly evicted.
func TestEvictLowestGasPrice_BigIntFeeCapNoTruncation(t *testing.T) {
	pool := NewTransactionPool()

	to := common.HexToAddress("0x222")

	// Tx 1: Legacy tx with low fee = 500
	txLow := makeTestTxWithGas(0x10, 0, 500, []byte{1})
	require.NoError(t, pool.AddTransaction(txLow))

	// Tx 2: EIP-1559 tx with huge fee cap = 2^64 + 50
	// If truncated to uint64, it would be 50, which is < 500!
	hugeFeeCap := new(big.Int).Add(new(big.Int).SetUint64(^uint64(0)), big.NewInt(51))
	ethTx := e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(100),
		GasFeeCap: hugeFeeCap,
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(100),
	})
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	signedEthTx, err := e_types.SignTx(ethTx, e_types.NewLondonSigner(big.NewInt(1)), key)
	require.NoError(t, err)
	txHuge, err := transaction.NewTransactionFromEth(signedEthTx)
	require.NoError(t, err)
	require.NoError(t, pool.AddTransaction(txHuge))

	// Evict 1 transaction: the lowest fee tx (txLow with 500) MUST be evicted, NOT txHuge!
	evicted := pool.EvictLowestGasPrice(1)
	assert.Equal(t, 1, evicted)

	remaining, _ := pool.TransactionsWithAggSign(0)
	require.Len(t, remaining, 1)
	assert.Equal(t, txHuge.Hash(), remaining[0].Hash(), "Huge fee cap tx must not be evicted ahead of lower fee tx")
}

func TestEvictLowestGasPrice_EvictAllPreservesConcurrentCount(t *testing.T) {
	pool := NewTransactionPool()
	const initialCount = 100
	for i := 0; i < initialCount; i++ {
		require.NoError(t, pool.AddTransaction(makeTestTxWithGas(byte(i%250), uint64(i/250), 10, []byte{byte(i)})))
	}

	var wg sync.WaitGroup
	insertedConcurrently := int64(0)
	stopInsert := make(chan struct{})

	// Concurrently insert higher-paying txs (gas price 500)
	wg.Add(1)
	go func() {
		defer wg.Done()
		idx := initialCount
		for {
			select {
			case <-stopInsert:
				return
			default:
				tx := makeTestTxWithGas(byte(idx%250), uint64(idx/250+10), 500, []byte{byte(idx)})
				if err := pool.AddTransaction(tx); err == nil {
					atomic.AddInt64(&insertedConcurrently, 1)
				}
				idx++
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Evict low-paying txs using floor 200 (must only evict gas 10 txs, never concurrent gas 500 txs)
	time.Sleep(2 * time.Millisecond)
	evicted := pool.EvictLowestGasPriceThan(initialCount, big.NewInt(200))
	assert.GreaterOrEqual(t, evicted, 1)
	assert.LessOrEqual(t, evicted, initialCount)

	close(stopInsert)
	wg.Wait()

	// Verify pool.CountTransactions() equals the sum of transactions across all shards
	actualCount := 0
	for i := 0; i < NumShards; i++ {
		shard := pool.shards[i]
		shard.mu.RLock()
		actualCount += len(shard.transactions)
		for _, tx := range shard.transactions {
			if tx.EffectiveGasPrice().Uint64() >= 200 {
				assert.Equal(t, uint64(500), tx.EffectiveGasPrice().Uint64())
			}
		}
		shard.mu.RUnlock()
	}

	assert.Equal(t, actualCount, pool.CountTransactions(), "CountTransactions must exactly match physical shard transactions count under concurrency")
}

func TestCheckAddStatus_Cases(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x20, 0)
	require.NoError(t, pool.AddTransaction(tx1))

	// 1. Same transaction hash -> Duplicate
	status, err := pool.CheckAddStatus(tx1)
	assert.ErrorIs(t, err, transaction.ErrAlreadyKnown)
	assert.Equal(t, TxAddStatusDuplicate, status)

	// 2. Same sender and nonce, but different gas price
	from := tx1.FromAddress()
	to := tx1.ToAddress()
	// Same nonce 0, different payload/amount, lower or same gas price -> Underpriced
	txUnderpriced := transaction.NewTransaction(
		from, to, big.NewInt(200), 21000, 1, 0, nil, nil,
		common.Hash{}, common.Hash{}, 0, 1,
	)
	status, err = pool.CheckAddStatus(txUnderpriced)
	assert.ErrorIs(t, err, transaction.ErrReplacementUnderpriced)
	assert.Equal(t, TxAddStatusUnderpriced, status)

	// Same nonce 0, higher gas price (>= 10% bump) -> Replace
	txReplacement := transaction.NewTransaction(
		from, to, big.NewInt(100), 21000, 2, 0, nil, nil,
		common.Hash{}, common.Hash{}, 0, 1,
	)
	status, err = pool.CheckAddStatus(txReplacement)
	assert.NoError(t, err)
	assert.Equal(t, TxAddStatusReplace, status)

	// 3. New nonce or new sender -> New
	txNew := makeTestTx(0x21, 0)
	status, err = pool.CheckAddStatus(txNew)
	assert.NoError(t, err)
	assert.Equal(t, TxAddStatusNew, status)
}

func TestEvictLowestGasPriceThan_FloorRespected(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTxWithGas(0x30, 0, 100, []byte{1})
	require.NoError(t, pool.AddTransaction(tx1))

	// An incoming transaction has gas price 50 (< existing 100)
	// It should NOT evict existing higher-paying transaction!
	evicted := pool.EvictLowestGasPriceThan(1, big.NewInt(50))
	assert.Equal(t, 0, evicted, "Must not evict tx that pays more than incoming tx")
	assert.Equal(t, 1, pool.CountTransactions())

	// An incoming transaction has gas price 200 (> existing 100)
	// Now it CAN evict the lower-paying transaction
	evicted = pool.EvictLowestGasPriceThan(1, big.NewInt(200))
	assert.Equal(t, 1, evicted)
	assert.Equal(t, 0, pool.CountTransactions())
}

func TestEvictLowestGasPriceThan_FullPoolNoCandidateFastPath(t *testing.T) {
	pool := NewTransactionPool()
	const count = 2000
	for i := 0; i < count; i++ {
		require.NoError(t, pool.AddTransaction(makeTestTxWithGas(byte(i%250), uint64(i/250), 100, []byte{byte(i)})))
	}
	assert.Equal(t, count, pool.CountTransactions())

	// Incoming transaction has gas price 100 (same as all txs in pool).
	// Under flat-fee market, no pool tx is strictly cheaper than 100.
	start := time.Now()
	evicted := pool.EvictLowestGasPriceThan(100, big.NewInt(100))
	duration := time.Since(start)

	assert.Equal(t, 0, evicted, "Must not evict any tx when none is cheaper")
	assert.Equal(t, count, pool.CountTransactions())
	assert.Less(t, duration, 500*time.Millisecond, "Fast path must return quickly without sorting entire pool")
}

func BenchmarkEvictLowestGasPriceThan_NoCheaperTx(b *testing.B) {
	pool := NewTransactionPool()
	const count = 10000
	for i := 0; i < count; i++ {
		_ = pool.AddTransaction(makeTestTxWithGas(byte(i%250), uint64(i/250), 100, []byte{byte(i)}))
	}
	minGas := big.NewInt(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pool.EvictLowestGasPriceThan(1000, minGas)
	}
}

func TestTransactionPool_Reservation_ABAPrevention(t *testing.T) {
	pool := NewTransactionPool()
	pool.SetMaxCapacity(10)

	txA := makeTestTxWithGas(0x01, 0, 100, []byte("txA"))
	require.NoError(t, pool.AddTransaction(txA))
	assert.Equal(t, 1, pool.CountTransactions())
	assert.Equal(t, 0, pool.InFlightTransactions())
	assert.Equal(t, 1, pool.UsedSlots())

	// Round 1: Drain txA -> gets drainID1
	drained1, drainID1, _ := pool.TransactionsWithDrainID(1)
	require.Len(t, drained1, 1)
	assert.Equal(t, txA.Hash(), drained1[0].Hash())
	assert.NotZero(t, drainID1)
	assert.Equal(t, 0, pool.CountTransactions())
	assert.Equal(t, 1, pool.InFlightTransactions())
	assert.Equal(t, 1, pool.UsedSlots())

	// Round 1: txA is requeued (e.g. forward failed or future nonce)
	requeued := pool.RequeueTransactionsWithDrainID(drained1, drainID1)
	assert.Equal(t, 1, requeued)
	assert.Equal(t, 1, pool.CountTransactions())
	assert.Equal(t, 0, pool.InFlightTransactions())
	assert.Equal(t, 1, pool.UsedSlots())

	// Round 2: Drain txA again -> gets drainID2 (different generation token)
	drained2, drainID2, _ := pool.TransactionsWithDrainID(1)
	require.Len(t, drained2, 1)
	assert.Equal(t, txA.Hash(), drained2[0].Hash())
	assert.NotEqual(t, drainID1, drainID2)
	assert.Equal(t, 0, pool.CountTransactions())
	assert.Equal(t, 1, pool.InFlightTransactions())
	assert.Equal(t, 1, pool.UsedSlots())

	// ABA Scenario: A delayed release or requeue from Round 1 arrives late with drainID1
	releasedStale := pool.ReleaseInFlightWithDrainID(drained2, drainID1)
	assert.Equal(t, 0, releasedStale, "Stale release from round 1 must be rejected and release 0 slots")
	assert.Equal(t, 1, pool.InFlightTransactions(), "In-flight reservation for round 2 must be preserved")
	assert.Equal(t, 1, pool.UsedSlots())

	// Stale requeue with drainID1 must also be rejected
	requeuedStale := pool.RequeueTransactionsWithDrainID(drained2, drainID1)
	assert.Equal(t, 0, requeuedStale, "Stale requeue from round 1 must be rejected")
	assert.Equal(t, 0, pool.CountTransactions())
	assert.Equal(t, 1, pool.InFlightTransactions())

	// Legitimate release with drainID2 succeeds
	releasedValid := pool.ReleaseInFlightWithDrainID(drained2, drainID2)
	assert.Equal(t, 1, releasedValid)
	assert.Equal(t, 0, pool.InFlightTransactions())
	assert.Equal(t, 0, pool.UsedSlots())
	assert.Equal(t, 10, pool.AvailableSlots())
}

func TestTransactionPool_TokenizedRequeue_CollisionAndValidation(t *testing.T) {
	pool := NewTransactionPool()
	pool.SetMaxCapacity(5)

	tx1 := makeTestTxWithGas(0x02, 0, 100, []byte("tx1"))
	tx2 := makeTestTxWithGas(0x03, 0, 100, []byte("tx2"))
	require.NoError(t, pool.AddTransaction(tx1))
	require.NoError(t, pool.AddTransaction(tx2))
	assert.Equal(t, 2, pool.CountTransactions())

	drained, drainID, _ := pool.TransactionsWithDrainID(2)
	require.Len(t, drained, 2)
	assert.Equal(t, 0, pool.CountTransactions())
	assert.Equal(t, 2, pool.InFlightTransactions())

	// 1. Requeue an unreserved transaction (random hash) -> must be rejected
	bogusTx := makeTestTxWithGas(0x04, 0, 100, []byte("bogus"))
	requeuedBogus := pool.RequeueTransactionsWithDrainID([]types.Transaction{bogusTx}, drainID)
	assert.Equal(t, 0, requeuedBogus, "Unreserved transaction must be rejected from requeue")
	assert.Equal(t, 0, pool.CountTransactions())
	assert.Equal(t, 2, pool.InFlightTransactions())

	// 2. Net-new requeue for tx1 -> converts reservation to in-pool slot
	requeued1 := pool.RequeueTransactionsWithDrainID([]types.Transaction{tx1}, drainID)
	assert.Equal(t, 1, requeued1)
	assert.Equal(t, 1, pool.CountTransactions())
	assert.Equal(t, 1, pool.InFlightTransactions())

	// 3. Duplicate requeue for tx1 (already in shard) with same drainID -> reservation already consumed
	requeuedDup := pool.RequeueTransactionsWithDrainID([]types.Transaction{tx1}, drainID)
	assert.Equal(t, 0, requeuedDup, "Requeuing already-consumed tx must be rejected")
	assert.Equal(t, 1, pool.CountTransactions())
	assert.Equal(t, 1, pool.InFlightTransactions())

	// 4. Shard collision with higher gas price:
	// A new tx with address 0x03, nonce 0 (same as tx2) was added while tx2 was in-flight
	tx2Collision := makeTestTxWithGas(0x03, 0, 100, []byte("tx2_collision"))
	require.NoError(t, pool.AddTransaction(tx2Collision))
	assert.Equal(t, 2, pool.CountTransactions())
	assert.Equal(t, 1, pool.InFlightTransactions()) // tx2 is still in-flight
	assert.Equal(t, 3, pool.UsedSlots())

	// Requeue tx2: address and nonce already exist in shard
	requeuedBump := pool.RequeueTransactionsWithDrainID([]types.Transaction{tx2}, drainID)
	assert.Equal(t, 1, requeuedBump)
	// Crucial check: count must NOT increase because the slot was already occupied by tx2Collision!
	assert.Equal(t, 0, pool.InFlightTransactions())
	assert.Equal(t, 2, pool.UsedSlots())
}

// TestTransactionPool_InFlightDedup_RejectsDuplicateAndDoesNotIncreaseCount verifies that
// transactions currently in-flight cannot be re-admitted as duplicates, and that
// re-submitting an in-flight transaction leaves reservation and counters unchanged.
func TestTransactionPool_InFlightDedup_RejectsDuplicateAndDoesNotIncreaseCount(t *testing.T) {
	pool := NewTransactionPool()
	pool.SetMaxCapacity(10)

	txA := makeTestTx(0x01, 0)
	require.NoError(t, pool.AddTransaction(txA))
	assert.Equal(t, 1, pool.CountTransactions())
	assert.Equal(t, 1, pool.UsedSlots())

	// Drain txA -> becomes in-flight
	drained, drainID, _ := pool.TransactionsWithDrainID(1)
	require.Len(t, drained, 1)
	assert.Equal(t, 0, pool.CountTransactions())
	assert.Equal(t, 1, pool.InFlightTransactions())
	assert.Equal(t, 1, pool.UsedSlots())

	// Client re-submits exact same transaction txA while in-flight
	err := pool.AddTransaction(txA)
	assert.Error(t, err)
	assert.ErrorIs(t, err, transaction.ErrAlreadyKnown)

	// Counters must NOT change
	assert.Equal(t, 0, pool.CountTransactions(), "Pool count must remain 0")
	assert.Equal(t, 1, pool.InFlightTransactions(), "In-flight count must remain 1")
	assert.Equal(t, 1, pool.UsedSlots(), "Used slots must remain 1")

	// CheckAddStatus on txA must also report duplicate / already known
	status, err := pool.CheckAddStatus(txA)
	assert.Equal(t, TxAddStatusDuplicate, status)
	assert.ErrorIs(t, err, transaction.ErrAlreadyKnown)

	// Requeue txA using its valid drainID token
	requeued := pool.RequeueTransactionsWithDrainID(drained, drainID)
	assert.Equal(t, 1, requeued)
	assert.Equal(t, 1, pool.CountTransactions())
	assert.Equal(t, 0, pool.InFlightTransactions())
	assert.Equal(t, 1, pool.UsedSlots())
}



