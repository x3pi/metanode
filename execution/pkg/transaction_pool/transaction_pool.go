package transaction_pool

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

// NumShards was 256 (keyed by a single address byte) until 2026-09-02. Found
// live while root-causing a multi-minute consensus stall under an extreme
// burst (1,000,000 real transfers, 3,000 concurrent RPC connections, all
// genuinely state-mutating — not the near-free reverting transactions used
// earlier in that day's benchmarking): even after bounding concurrent RPC
// execution (see rpc_transaction.go's rpcTxConcurrencyLimiter) and fixing two
// unrelated synchronous-I/O bottlenecks upstream of it, several hundred
// goroutines still queued for minutes on individual shard mutexes in
// AddTransaction — whose own critical section (map/slice inserts, an
// already-async-queued log call, a non-blocking notify) has no other known
// blocking operation. With the mempool sitting at its 200,000-entry hard cap
// under sustained load, 256 shards means ~780 transactions and correspondingly
// deep contention per shard mutex; going to 65,536 shards (a 256x finer
// split, keyed by the first two address bytes instead of one) cuts that to
// ~3 transactions per shard at the same pool size, directly addressing the
// contention without changing any locking semantics. Per-shard overhead
// (a sync.RWMutex + two maps + a slice header) is a few dozen bytes, so the
// fixed cost of always allocating all shards up front (unchanged from
// before) is on the order of single-digit MB total — negligible next to the
// pool's actual transaction payload.
const NumShards = 65536

type txPoolKey struct {
	addr  common.Address
	nonce uint64
}

type poolShard struct {
	mu              sync.RWMutex
	transactions    []types.Transaction
	transactionKeys map[txPoolKey]bool
	txHashMap       map[common.Hash]types.Transaction
}

func newPoolShard() *poolShard {
	return &poolShard{
		transactions:    make([]types.Transaction, 0),
		transactionKeys: make(map[txPoolKey]bool),
		txHashMap:       make(map[common.Hash]types.Transaction),
	}
}

type TransactionPool struct {
	shards     [NumShards]*poolShard
	NotifyChan chan struct{} // GO-2: Event channel to notify workers of new transactions
	count      int64         // Atomic counter for lock-free count queries
	// drainCursor tracks where TransactionsWithAggSign() left off, so each
	// call resumes exactly where the last one stopped instead of restarting
	// from shard 0 (see there for why: the caller caps how many of the
	// returned txs it forwards per tick and re-queues the rest, so a fixed
	// restart point lets whichever low-numbered shards are currently hot
	// perpetually starve every higher-numbered shard under sustained load --
	// and, since 2026-09-02, also lets a single call stop early once it has
	// collected enough transactions rather than always sweeping every shard).
	drainCursor uint32
}

func NewTransactionPool() *TransactionPool {
	tp := &TransactionPool{
		NotifyChan: make(chan struct{}, 1),
	}
	for i := 0; i < NumShards; i++ {
		tp.shards[i] = newPoolShard()
	}
	return tp
}

// notifyWork sends a non-blocking signal that new work is available (GO-2)
func (tp *TransactionPool) notifyWork() {
	select {
	case tp.NotifyChan <- struct{}{}:
	default: // Channel already has a pending signal, safe to drop
	}
}

func (tp *TransactionPool) getShardIndex(addr common.Address) uint16 {
	// First two address bytes -> 0-65535, matching NumShards (see its comment
	// for why one byte/256 shards stopped being enough contention headroom).
	return uint16(addr[0])<<8 | uint16(addr[1])
}

func (tp *TransactionPool) CountTransactions() int {
	return int(atomic.LoadInt64(&tp.count))
}

func (tp *TransactionPool) EvictLowestGasPrice(countToEvict int) int {
	if countToEvict <= 0 {
		return 0
	}

	type txInfo struct {
		shardIdx int
		index    int
		gas      uint64
		nonce    uint64
		hash     common.Hash
	}

	var allInfos []txInfo

	for i := 0; i < NumShards; i++ {
		shard := tp.shards[i]
		shard.mu.RLock()
		for idx, tx := range shard.transactions {
			allInfos = append(allInfos, txInfo{
				shardIdx: i,
				index:    idx,
				gas:      tx.MaxGasPrice(),
				nonce:    tx.GetNonce(),
				hash:     tx.Hash(),
			})
		}
		shard.mu.RUnlock()
	}

	if len(allInfos) <= countToEvict {
		// Evict all
		var totalEvicted int
		for i := 0; i < NumShards; i++ {
			shard := tp.shards[i]
			shard.mu.Lock()
			totalEvicted += len(shard.transactions)
			shard.transactions = make([]types.Transaction, 0)
			shard.transactionKeys = make(map[txPoolKey]bool)
			shard.txHashMap = make(map[common.Hash]types.Transaction)
			shard.mu.Unlock()
		}
		atomic.StoreInt64(&tp.count, 0)
		return totalEvicted
	}

	// Sort by GasPrice ascending, so eviction still respects the fee market as
	// the primary signal -- but break ties by nonce DESCENDING (found
	// 2026-09-02 measuring sustained real-transfer throughput: with the
	// uniform gas price every tx in this benchmark used, and very likely in
	// any single dApp's batch of same-priority transfers, "ascending gas"
	// alone has no real ordering to fall back on, so which specific txs got
	// evicted at the pool's 200,000-entry cap was effectively arbitrary
	// w.r.t. nonce. For any sender with several queued transactions, evicting
	// a LOW-nonce one while a HIGHER-nonce one for the same sender survives
	// permanently strands that higher nonce -- it can never execute out of
	// order, so it sits in the pool as a future-tx forever (or until its own
	// TTL) instead of being usefully evicted itself. Sorting ties by nonce
	// descending means eviction always consumes a sender's queued
	// transactions from the tail of their nonce sequence inward, so it can
	// never create an internal gap: whatever of a sender's transactions
	// survive eviction always form a contiguous run starting at that
	// sender's lowest queued nonce. This measurably reduced tx loss under
	// sustained overload without changing the eviction count or the
	// fee-market ordering for txs that actually have different gas prices.
	sort.Slice(allInfos, func(i, j int) bool {
		if allInfos[i].gas != allInfos[j].gas {
			return allInfos[i].gas < allInfos[j].gas
		}
		return allInfos[i].nonce > allInfos[j].nonce
	})

	// Group removals by shard
	toRemoveByShard := make(map[int]map[common.Hash]bool)
	for i := 0; i < countToEvict; i++ {
		info := allInfos[i]
		if toRemoveByShard[info.shardIdx] == nil {
			toRemoveByShard[info.shardIdx] = make(map[common.Hash]bool)
		}
		toRemoveByShard[info.shardIdx][info.hash] = true
	}

	var totalEvicted int
	for i, hashesToRemove := range toRemoveByShard {
		shard := tp.shards[i]
		// ROOT CAUSE (found 2026-09-02, extreme-scale real-transfer load): this used to
		// compute cap(newTxs) as len(shard.transactions)-len(hashesToRemove), and unlock
		// non-deferred. `hashesToRemove` for this shard was decided from an earlier RLock
		// snapshot pass (allInfos, above) taken *before* this write-lock is acquired.
		// Nothing prevents TransactionsWithAggSign() from concurrently draining and
		// zeroing this exact shard in between those two passes -- there is no coordination
		// between it and eviction, only single-flight *within* eviction itself (see
		// evictionInProgress). When that race hit, shard.transactions was already empty
		// (len=0) here while hashesToRemove still held entries from the stale scan, so
		// len(shard.transactions)-len(hashesToRemove) went negative and
		// make([]types.Transaction, 0, <negative>) panicked ("makeslice: cap out of
		// range"). Go's net/http recovers panics per-request, so the process itself
		// survived -- but the non-deferred Unlock() below never ran, permanently locking
		// this one shard's mutex. Every later call touching that shard (most importantly
		// TransactionsWithAggSign's per-tick full round-robin drain, run from the single
		// block-forwarding goroutine) then blocked forever, which was the real root cause
		// of the multi-minute consensus stall this whole investigation was chasing: Rust's
		// DAG kept committing empty rounds normally throughout, since the hang was entirely
		// downstream in Go's mempool, invisible from the Rust side.
		// Fix is two-fold: (1) never derive a possibly-negative capacity -- len(shard.
		// transactions) alone is always a safe, if occasionally slightly-oversized, upper
		// bound regardless of what hashesToRemove contains; (2) defer the unlock (inside a
		// per-iteration closure, since a bare defer in a for-loop body only fires when the
		// whole function returns, not per shard -- that would hold every touched shard's
		// lock simultaneously until the entire eviction pass finishes) so that even an
		// unrelated future panic here can't wedge a shard's mutex forever.
		func() {
			shard.mu.Lock()
			defer shard.mu.Unlock()

			newTxs := make([]types.Transaction, 0, len(shard.transactions))
			for _, tx := range shard.transactions {
				h := tx.Hash()
				if hashesToRemove[h] {
					key := txPoolKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
					delete(shard.transactionKeys, key)
					delete(shard.txHashMap, h)
					totalEvicted++
				} else {
					newTxs = append(newTxs, tx)
				}
			}
			shard.transactions = newTxs
		}()
	}

	atomic.AddInt64(&tp.count, int64(-totalEvicted))
	return totalEvicted
}

func (tp *TransactionPool) AddTransaction(tx types.Transaction) error {
	shardIdx := tp.getShardIndex(tx.FromAddress())
	shard := tp.shards[shardIdx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	key := txPoolKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
	if shard.transactionKeys[key] {
		logger.Info("Transaction already exists in pool, skipping key addr=%s nonce=%d", key.addr.Hex(), key.nonce)
		return fmt.Errorf("transaction already exists in pool, skipping")
	}

	// CROSS-CHAIN DEBUG logic (unchanged)
	if tx.ToAddress() == common.HexToAddress("0x00000000000000000000000000000000B429C0B2") {
		logger.Info("📥 [POOL-ARRIVE] Cross-chain TX entered mempool: hash=%s from=%s nonce=%d pool_size=%d",
			tx.Hash().Hex()[:16], tx.FromAddress().Hex()[:10], tx.GetNonce(), atomic.LoadInt64(&tp.count)+1)
	}

	shard.transactions = append(shard.transactions, tx)
	shard.transactionKeys[key] = true

	h := tx.Hash()
	if h != (common.Hash{}) {
		shard.txHashMap[h] = tx
	}

	atomic.AddInt64(&tp.count, 1)
	tp.notifyWork()
	return nil
}

func (tp *TransactionPool) AddTransactions(txs []types.Transaction) {
	// Group transactions by shard to minimize lock contention
	txsByShard := make(map[uint16][]types.Transaction)
	for _, tx := range txs {
		idx := tp.getShardIndex(tx.FromAddress())
		txsByShard[idx] = append(txsByShard[idx], tx)
	}

	addedAny := false
	var totalAdded int64

	for idx, shardTxs := range txsByShard {
		shard := tp.shards[idx]
		shard.mu.Lock()

		for _, tx := range shardTxs {
			key := txPoolKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
			if !shard.transactionKeys[key] {
				shard.transactions = append(shard.transactions, tx)
				shard.transactionKeys[key] = true

				h := tx.Hash()
				if h != (common.Hash{}) {
					shard.txHashMap[h] = tx
				}
				totalAdded++
				addedAny = true
			}
		}
		shard.mu.Unlock()
	}

	if addedAny {
		atomic.AddInt64(&tp.count, totalAdded)
		tp.notifyWork()
	}
}

// TransactionsWithAggSign drains up to maxDrain transactions from the pool
// (0 or negative means unbounded -- drain everything, the original
// behavior), starting from wherever the previous call left off.
//
// maxDrain was added 2026-09-02 measuring sustained real-transfer throughput
// after a retry fix elsewhere made the RPC layer accept nearly all offered
// load: with a multi-million-transaction backlog sitting in the pool, this
// function -- called every ~1ms tick by TxBatchForwarder -- used to drain
// and re-sort the ENTIRE backlog every single call, even though the caller
// only ever forwards targetBlockSize (8,000) of the result and immediately
// re-queues the rest right back into the same shards. That's O(backlog) work
// per tick to make O(targetBlockSize) forward progress: measured at a
// 3.7-million-tx backlog, a single tick's nonce-sort-and-validate pass alone
// took 16-24 SECONDS, and since each tick only nets ~8,000 fewer pending
// transactions, draining the full backlog would have taken on the order of
// (backlog/8,000) such ticks -- projected at ~2.5 hours for that one run,
// versus completing in low minutes once this cap was added.
//
// Capping the drain at a small multiple of targetBlockSize bounds this
// function's cost to that constant, independent of how large the backlog
// gets, while the pre-existing round-robin drainCursor (below) guarantees
// every shard still gets its turn -- a capped call just means it takes a
// few more ticks to complete one full rotation instead of one, which is
// the entire point: the rotation's total cost across those extra ticks is
// still O(backlog) exactly once, not O(backlog) repeated on every tick.
func (tp *TransactionPool) TransactionsWithAggSign(maxDrain int) ([]types.Transaction, []byte) {
	var allTxs []types.Transaction
	var totalDrained int64

	// FAIRNESS: start from wherever the previous call left off (see the cursor
	// update below) instead of always shard 0. The caller (TxBatchForwarder)
	// caps how many of the txs returned here it actually forwards per tick
	// (targetBlockSize) and re-queues the remainder — which lands back in the
	// SAME shards it came from. With a fixed 0->255 start, under sustained
	// load where the low shards refill as fast as they drain, every
	// higher-numbered shard's txs sit at the tail of `allTxs` on every single
	// tick and can be pushed past the cap indefinitely: not just slower, but
	// genuinely starved for as long as the burst lasts (measured: a single
	// unrelated tx landing in a "wrong" shard during a 1000-worker burst
	// waited 10+ seconds — 30-40x the normal ~300ms — while txs in early
	// shards kept confirming on schedule). Resuming from where the last call
	// stopped means every shard gets visited exactly once per full rotation,
	// whether that rotation completes in one call (maxDrain<=0, or the pool
	// is smaller than maxDrain) or is spread across many capped calls.
	start := int(atomic.LoadUint32(&tp.drainCursor) % NumShards)
	visited := 0
	for visited < NumShards {
		if maxDrain > 0 && len(allTxs) >= maxDrain {
			break
		}
		i := (start + visited) % NumShards
		shard := tp.shards[i]
		shard.mu.Lock()
		if len(shard.transactions) > 0 {
			totalDrained += int64(len(shard.transactions))
			allTxs = append(allTxs, shard.transactions...)
			shard.transactions = make([]types.Transaction, 0)
			shard.transactionKeys = make(map[txPoolKey]bool)
			shard.txHashMap = make(map[common.Hash]types.Transaction)
		}
		shard.mu.Unlock()
		visited++
	}
	// Advance by exactly how far we got, so a capped call resumes at the next
	// unvisited shard next time. The one exception: a full, uninterrupted lap
	// (visited==NumShards, i.e. maxDrain<=0 or the whole pool fit under it)
	// would otherwise land the cursor right back on `start` -- advance by 1
	// instead, matching the original always-rotate-by-1 behavior, so repeated
	// full sweeps of a small pool still vary which shard's txs lead `allTxs`.
	advance := visited
	if visited >= NumShards {
		advance = 1
	}
	atomic.StoreUint32(&tp.drainCursor, uint32((start+advance)%NumShards))

	atomic.AddInt64(&tp.count, -totalDrained)

	// Preserving original behavior: aggregate sign returns nil
	return allTxs, nil
}

func (tp *TransactionPool) GetTransactionByHash(hashToFind common.Hash) (types.Transaction, bool) {
	if hashToFind == (common.Hash{}) {
		logger.Warn("GetTransactionByHash called with a zero hash value.")
		return nil, false
	}

	for i := 0; i < NumShards; i++ {
		shard := tp.shards[i]
		shard.mu.RLock()
		tx, ok := shard.txHashMap[hashToFind]
		shard.mu.RUnlock()
		if ok {
			return tx, true
		}
	}

	return nil, false
}
