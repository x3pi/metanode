package transaction_pool

import (
	"fmt"
	"math/big"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// TEMPORARY DIAGNOSTIC (2026-09-02): traces individual transaction lifecycle
// events to /tmp/tx_lifecycle.log when METANODE_TX_TRACE=1, to chase the
// ~0.5-1% of transactions still going permanently missing under extreme
// sustained overload despite zero evictions, zero send errors, and no known
// duplicate/race in the classification path. Off by default (a single
// atomic.Bool check) so it costs nothing in the normal build. Remove once
// root-caused.
var txTraceEnabled = os.Getenv("METANODE_TX_TRACE") == "1"
var txTraceMu sync.Mutex
var txTraceFile *os.File

// TxTraceEnabled reports whether transaction lifecycle tracing is enabled.
func TxTraceEnabled() bool {
	return txTraceEnabled
}

// TraceTx is the exported form of traceTx, for the same diagnostic use from
// other packages (e.g. processor.ProcessTransactionsInPoolSub's
// valid/future/past classification, which happens outside this package).
func TraceTx(event string, addr common.Address, nonce uint64, extra string) {
	if !txTraceEnabled {
		return
	}
	traceTx(event, addr, nonce, extra)
}

func traceTx(event string, addr common.Address, nonce uint64, extra string) {
	if !txTraceEnabled {
		return
	}
	txTraceMu.Lock()
	defer txTraceMu.Unlock()
	if txTraceFile == nil {
		f, err := os.OpenFile("/tmp/tx_lifecycle.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		txTraceFile = f
	}
	fmt.Fprintf(txTraceFile, "%s %s addr=%s nonce=%d %s\n",
		time.Now().Format("15:04:05.000000"), event, addr, nonce, extra)
}

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
	shards      [NumShards]*poolShard
	NotifyChan  chan struct{}          // GO-2: Event channel to notify workers of new transactions
	capacityMu  sync.Mutex             // Short lock protecting mempool capacity invariant: used = count + len(inFlightTxs)
	count       int64                  // Counter for in-pool transactions
	inFlightTxs map[common.Hash]uint64 // Map of txHash -> drainID holding reservations (solves ABA)
	nextDrainID uint64                 // Generation token counter for drain operations
	maxCapacity int64                  // Hard limit on total committed transactions (in-pool + in-flight)
	drainCursor uint32
}

func NewTransactionPool() *TransactionPool {
	tp := &TransactionPool{
		NotifyChan:  make(chan struct{}, 1),
		inFlightTxs: make(map[common.Hash]uint64),
		nextDrainID: 1,
		maxCapacity: 200000,
	}
	for i := 0; i < NumShards; i++ {
		tp.shards[i] = newPoolShard()
	}
	return tp
}

// SetMaxCapacity sets the maximum capacity for the pool.
func (tp *TransactionPool) SetMaxCapacity(c int64) {
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	tp.maxCapacity = c
}

// MaxCapacity returns the pool's configured capacity.
func (tp *TransactionPool) MaxCapacity() int {
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	if tp.maxCapacity <= 0 {
		return 200000
	}
	return int(tp.maxCapacity)
}

// InFlightTransactions returns the count of drained in-flight transactions holding reservations.
func (tp *TransactionPool) InFlightTransactions() int {
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	return len(tp.inFlightTxs)
}

// UsedSlots returns total capacity currently committed: in-pool transactions plus in-flight reservations.
func (tp *TransactionPool) UsedSlots() int {
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	return int(tp.count) + len(tp.inFlightTxs)
}

// AvailableSlots returns the number of slots available for new admissions.
func (tp *TransactionPool) AvailableSlots() int {
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	cap := tp.maxCapacity
	if cap <= 0 {
		cap = 200000
	}
	used := tp.count + int64(len(tp.inFlightTxs))
	if used >= cap {
		return 0
	}
	return int(cap - used)
}

// ReleaseInFlightWithDrainID frees in-flight reservation slots for specific transactions matching drainID.
// Protects against ABA problem where delayed release from an earlier round could release a newer round's reservation.
func (tp *TransactionPool) ReleaseInFlightWithDrainID(txs []types.Transaction, drainID uint64) int {
	if len(txs) == 0 || drainID == 0 {
		return 0
	}
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	released := 0
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		h := tx.Hash()
		if id, ok := tp.inFlightTxs[h]; ok && id == drainID {
			delete(tp.inFlightTxs, h)
			released++
		}
	}
	return released
}

// ReleaseInFlightHashesWithDrainID frees in-flight reservation slots for specific tx hashes matching drainID.
func (tp *TransactionPool) ReleaseInFlightHashesWithDrainID(hashes []common.Hash, drainID uint64) int {
	if len(hashes) == 0 || drainID == 0 {
		return 0
	}
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	released := 0
	for _, h := range hashes {
		if id, ok := tp.inFlightTxs[h]; ok && id == drainID {
			delete(tp.inFlightTxs, h)
			released++
		}
	}
	return released
}

// ReleaseInFlight frees n ARBITRARY in-flight reservation slots (map order). TEST HELPER ONLY: it bypasses the drainID check
// that prevents a late release from freeing a newer drain's reservation, so production code must use
// ReleaseInFlightWithDrainID.
func (tp *TransactionPool) ReleaseInFlight(count int) {
	if count <= 0 {
		return
	}
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	for h := range tp.inFlightTxs {
		delete(tp.inFlightTxs, h)
		count--
		if count == 0 {
			break
		}
	}
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
	tp.capacityMu.Lock()
	defer tp.capacityMu.Unlock()
	return int(tp.count)
}

func (tp *TransactionPool) EvictLowestGasPrice(countToEvict int) int {
	return tp.EvictLowestGasPriceThan(countToEvict, nil)
}

func (tp *TransactionPool) EvictLowestGasPriceThan(countToEvict int, minNewGas *big.Int) int {
	if countToEvict <= 0 {
		return 0
	}

	type txInfo struct {
		shardIdx int
		index    int
		gas      *big.Int
		nonce    uint64
		hash     common.Hash
	}

	var allInfos []txInfo

	for i := 0; i < NumShards; i++ {
		shard := tp.shards[i]
		shard.mu.RLock()
		for idx, tx := range shard.transactions {
			// Compared as *big.Int (a fee cap above 2^64 must not wrap). The value is only read, so use it
			// directly: this scan visits every tx in the pool and must not allocate per tx.
			gas := tx.GasPriceCap()
			if gas == nil {
				gas = new(big.Int).SetUint64(tx.MaxGasPrice())
			}
			if minNewGas != nil && gas.Cmp(minNewGas) >= 0 {
				continue // pays >= minNewGas: never an eviction candidate
			}
			allInfos = append(allInfos, txInfo{
				shardIdx: i,
				index:    idx,
				gas:      gas,
				nonce:    tx.GetNonce(),
				hash:     tx.Hash(),
			})
		}
		shard.mu.RUnlock()
	}

	if len(allInfos) == 0 {
		return 0
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
		cmp := allInfos[i].gas.Cmp(allInfos[j].gas)
		if cmp != 0 {
			return cmp < 0
		}
		return allInfos[i].nonce > allInfos[j].nonce
	})

	if countToEvict > len(allInfos) {
		countToEvict = len(allInfos)
	}

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
					// Confirm candidate still qualifies for eviction (has not been replaced with higher gas)
					if minNewGas != nil {
						g := tx.GasPriceCap()
						if g == nil {
							g = new(big.Int).SetUint64(tx.MaxGasPrice())
						}
						if g.Cmp(minNewGas) >= 0 {
							newTxs = append(newTxs, tx)
							continue
						}
					}
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

	if totalEvicted > 0 {
		tp.capacityMu.Lock()
		tp.count -= int64(totalEvicted)
		if tp.count < 0 {
			tp.count = 0
		}
		tp.capacityMu.Unlock()
	}
	return totalEvicted
}

// checkPriceBump verifies that newPrice is at least 10% higher than oldPrice (go-ethereum standard).
func checkPriceBump(oldPrice, newPrice *big.Int) bool {
	if newPrice == nil || newPrice.Sign() <= 0 {
		return false
	}
	if oldPrice == nil || oldPrice.Sign() <= 0 {
		return true
	}
	// threshold = oldPrice + max(1, oldPrice * 10 / 100)
	bump := new(big.Int).Mul(oldPrice, big.NewInt(10))
	bump.Div(bump, big.NewInt(100))
	if bump.Sign() == 0 {
		bump.SetInt64(1)
	}
	threshold := new(big.Int).Add(oldPrice, bump)
	return newPrice.Cmp(threshold) >= 0
}

// TxAddStatus indicates whether a transaction is new, an in-place replacement, duplicate, or underpriced.
type TxAddStatus int

const (
	TxAddStatusNew TxAddStatus = iota
	TxAddStatusReplace
	TxAddStatusDuplicate
	TxAddStatusUnderpriced
)

// CheckAddStatus inspects whether tx would be a net-new addition, an in-place replacement,
// a duplicate, or an underpriced replacement. It does not modify pool state.
func (tp *TransactionPool) CheckAddStatus(tx types.Transaction) (TxAddStatus, error) {
	if tx == nil {
		return TxAddStatusNew, fmt.Errorf("tx nil")
	}
	from := tx.FromAddress()
	shardIdx := tp.getShardIndex(from)
	shard := tp.shards[shardIdx]

	shard.mu.RLock()
	defer shard.mu.RUnlock()

	h := tx.Hash()
	if h != (common.Hash{}) {
		tp.capacityMu.Lock()
		_, inFlight := tp.inFlightTxs[h]
		tp.capacityMu.Unlock()
		if inFlight {
			return TxAddStatusDuplicate, transaction.ErrAlreadyKnown
		}
	}

	key := txPoolKey{addr: from, nonce: tx.GetNonce()}
	if !shard.transactionKeys[key] {
		return TxAddStatusNew, nil
	}

	for _, t := range shard.transactions {
		if t.FromAddress() == key.addr && t.GetNonce() == key.nonce {
			if t.Hash() == tx.Hash() {
				return TxAddStatusDuplicate, transaction.ErrAlreadyKnown
			}
			if !checkPriceBump(t.EffectiveGasPrice(), tx.EffectiveGasPrice()) {
				return TxAddStatusUnderpriced, transaction.ErrReplacementUnderpriced
			}
			return TxAddStatusReplace, nil
		}
	}
	return TxAddStatusDuplicate, transaction.ErrAlreadyKnown
}

func (tp *TransactionPool) addTxToShardLocked(shard *poolShard, shardIdx uint16, tx types.Transaction) (bool, error) {
	from := tx.FromAddress()
	nonce := tx.GetNonce()
	key := txPoolKey{addr: from, nonce: nonce}

	// 0. In-flight dedup: if transaction is currently in-flight, reject as already known
	h := tx.Hash()
	if h != (common.Hash{}) {
		tp.capacityMu.Lock()
		_, inFlight := tp.inFlightTxs[h]
		tp.capacityMu.Unlock()
		if inFlight {
			logger.Info("Transaction is currently in-flight, skipping key addr=%s nonce=%d hash=%s", key.addr.Hex(), key.nonce, h.Hex())
			return false, fmt.Errorf("%w: transaction is currently in-flight", transaction.ErrAlreadyKnown)
		}
	}

	if shard.transactionKeys[key] {
		// Look up existing transaction with the same key
		var oldTx types.Transaction
		var oldIdx = -1
		for idx, t := range shard.transactions {
			if t.FromAddress() == key.addr && t.GetNonce() == key.nonce {
				oldTx = t
				oldIdx = idx
				break
			}
		}

		if oldTx != nil {
			// 1. Same hash -> duplicate / already known
			if oldTx.Hash() == tx.Hash() {
				logger.Info("Transaction already exists in pool, skipping key addr=%s nonce=%d", key.addr.Hex(), key.nonce)
				if txTraceEnabled {
					traceTx("REJECT-DUP", from, key.nonce, fmt.Sprintf("shard=%d", shardIdx))
				}
				return false, fmt.Errorf("%w: transaction already exists in pool, skipping", transaction.ErrAlreadyKnown)
			}

			// 2. Replacement transaction: check price bump (standard >= 10%)
			oldPrice := oldTx.EffectiveGasPrice()
			newPrice := tx.EffectiveGasPrice()
			if !checkPriceBump(oldPrice, newPrice) {
				logger.Info("Replacement transaction underpriced: addr=%s nonce=%d oldPrice=%v newPrice=%v",
					key.addr.Hex(), key.nonce, oldPrice, newPrice)
				if txTraceEnabled {
					traceTx("REJECT-UNDERPRICED", from, key.nonce, fmt.Sprintf("old=%v new=%v", oldPrice, newPrice))
				}
				return false, fmt.Errorf("%w: replacement transaction underpriced", transaction.ErrReplacementUnderpriced)
			}

			// 3. Price bump satisfied: replace old transaction in place
			oldHash := oldTx.Hash()
			delete(shard.txHashMap, oldHash)
			shard.transactions[oldIdx] = tx
			newHash := tx.Hash()
			if newHash != (common.Hash{}) {
				shard.txHashMap[newHash] = tx
			}
			logger.Info("🔄 [REPLACEMENT-TX] Replaced tx addr=%s nonce=%d oldHash=%s newHash=%s (bumped %v -> %v)",
				key.addr.Hex(), key.nonce, oldHash.Hex(), newHash.Hex(), oldPrice, newPrice)
			if txTraceEnabled {
				traceTx("REPLACE", from, key.nonce, fmt.Sprintf("oldHash=%s newHash=%s oldPrice=%v newPrice=%v",
					oldHash.Hex(), newHash.Hex(), oldPrice, newPrice))
			}
			return false, nil // replaced in place, did not increase count
		}

		return false, fmt.Errorf("%w: transaction already exists in pool, skipping", transaction.ErrAlreadyKnown)
	}

	// Net-new transaction: enforce capacity and reserve slot under capacityMu
	tp.capacityMu.Lock()
	maxCap := tp.maxCapacity
	if maxCap <= 0 {
		maxCap = 200000
	}
	if tp.count+int64(len(tp.inFlightTxs)) >= maxCap {
		tp.capacityMu.Unlock()
		return false, fmt.Errorf("%w (limit=%d) and transactions underpriced to evict", transaction.ErrPoolFull, maxCap)
	}
	tp.count++
	curCount := tp.count
	tp.capacityMu.Unlock()

	// CROSS-CHAIN DEBUG logic (unchanged)
	if tx.ToAddress() == common.HexToAddress("0x00000000000000000000000000000000B429C0B2") {
		logger.Info("📥 [POOL-ARRIVE] Cross-chain TX entered mempool: hash=%s from=%s nonce=%d pool_size=%d",
			tx.Hash().Hex()[:16], from.Hex()[:10], key.nonce, curCount)
	}

	shard.transactions = append(shard.transactions, tx)
	shard.transactionKeys[key] = true
	h = tx.Hash()
	if h != (common.Hash{}) {
		shard.txHashMap[h] = tx
	}

	if txTraceEnabled {
		traceTx("ADD", from, key.nonce, fmt.Sprintf("shard=%d newCount=%d", shardIdx, len(shard.transactions)))
	}
	return true, nil // added net-new
}

// AddTransaction inserts tx. The pool always enforces its capacity (count + in-flight reservations <= maxCapacity), rejecting a
// net-new tx with transaction.ErrPoolFull; evicting cheaper txs to make room is the caller's job (see TxValidatorPool).
func (tp *TransactionPool) AddTransaction(tx types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("tx nil")
	}
	from := tx.FromAddress()
	shardIdx := tp.getShardIndex(from)
	shard := tp.shards[shardIdx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	_, err := tp.addTxToShardLocked(shard, shardIdx, tx)
	if err != nil {
		return err
	}
	tp.notifyWork()
	return nil
}

// AddTransactions inserts a batch of transactions sequentially under shard locks,
// allowing subsequent transactions with the same sender/nonce to see earlier ones,
// and returns exact insertion errors for each transaction.
func (tp *TransactionPool) AddTransactions(txs []types.Transaction) []error {
	if len(txs) == 0 {
		return nil
	}

	errorsList := make([]error, len(txs))

	var lockedShard *poolShard
	var lockedIdx uint16
	defer func() {
		if lockedShard != nil {
			lockedShard.mu.Unlock()
		}
	}()

	for i, tx := range txs {
		if tx == nil {
			errorsList[i] = fmt.Errorf("tx nil")
			continue
		}
		from := tx.FromAddress()
		idx := tp.getShardIndex(from)
		if lockedShard == nil || idx != lockedIdx {
			if lockedShard != nil {
				lockedShard.mu.Unlock()
			}
			lockedIdx = idx
			lockedShard = tp.shards[idx]
			lockedShard.mu.Lock()
		}

		_, err := tp.addTxToShardLocked(lockedShard, idx, tx)
		errorsList[i] = err
	}

	tp.notifyWork()
	return errorsList
}

// RequeueTransactionsWithDrainID re-inserts in-flight transactions back into the pool shards.
// Enforces:
// 1. Transaction must hold a valid in-flight reservation matching drainID (or drainID==0 for untokenized).
// 2. If tx is net-new in shard: transitions slot from inFlightTxs to count (count++).
// 3. If tx already exists in shard (duplicate or higher-fee replacement): consumes reservation,
//    updates shard if replacement, but DOES NOT increment count.
// 4. If tx has no valid reservation: explicitly rejected, never admitted silently.
func (tp *TransactionPool) RequeueTransactionsWithDrainID(txs []types.Transaction, drainID uint64) int {
	if len(txs) == 0 || drainID == 0 {
		return 0
	}

	var lockedShard *poolShard
	var lockedIdx uint16
	defer func() {
		if lockedShard != nil {
			lockedShard.mu.Unlock()
		}
	}()

	requeued := 0
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		from := tx.FromAddress()
		idx := tp.getShardIndex(from)
		if lockedShard == nil || idx != lockedIdx {
			if lockedShard != nil {
				lockedShard.mu.Unlock()
			}
			lockedIdx = idx
			lockedShard = tp.shards[idx]
			lockedShard.mu.Lock()
		}

		h := tx.Hash()

		// Verify and consume reservation under capacityMu
		tp.capacityMu.Lock()
		curDrainID, hasReservation := tp.inFlightTxs[h]
		if !hasReservation || curDrainID != drainID {
			// No valid in-flight reservation or token mismatch -> reject
			tp.capacityMu.Unlock()
			logger.Warn("⚠️ [REQUEUE-REJECTED] Tx has no valid reservation or drainID mismatch: hash=%s drainID=%d curDrainID=%d hasRes=%v",
				h.Hex(), drainID, curDrainID, hasReservation)
			continue
		}

		// Valid reservation: consume token
		delete(tp.inFlightTxs, h)

		key := txPoolKey{addr: from, nonce: tx.GetNonce()}
		if !lockedShard.transactionKeys[key] {
			// Net-new to shard: transition reservation to in-pool slot
			tp.count++
			tp.capacityMu.Unlock()

			lockedShard.transactions = append(lockedShard.transactions, tx)
			lockedShard.transactionKeys[key] = true
			if h != (common.Hash{}) {
				lockedShard.txHashMap[h] = tx
			}
			requeued++
		} else {
			// Already in shard: do NOT increment count
			tp.capacityMu.Unlock()

			var oldTx types.Transaction
			var oldIdx = -1
			for i, t := range lockedShard.transactions {
				if t.FromAddress() == key.addr && t.GetNonce() == key.nonce {
					oldTx = t
					oldIdx = i
					break
				}
			}
			if oldTx != nil && oldTx.Hash() != h && checkPriceBump(oldTx.EffectiveGasPrice(), tx.EffectiveGasPrice()) {
				// Replaced higher-fee tx in place
				delete(lockedShard.txHashMap, oldTx.Hash())
				lockedShard.transactions[oldIdx] = tx
				if h != (common.Hash{}) {
					lockedShard.txHashMap[h] = tx
				}
				logger.Info("🔄 [REQUEUE-REPLACE] Replaced tx addr=%s nonce=%d oldHash=%s newHash=%s",
					key.addr.Hex(), key.nonce, oldTx.Hash().Hex(), h.Hex())
			}
			requeued++
		}
	}
	if requeued < len(txs) {
		logger.Warn("⚠️ [REQUEUE-PARTIAL] Requeued %d/%d transactions (drainID=%d)", requeued, len(txs), drainID)
	}
	if requeued > 0 {
		tp.notifyWork()
	}
	return requeued
}

// RequeueTransactions re-inserts drained in-flight transactions, inferring the drainID from the first tx that holds a
// reservation. TEST HELPER ONLY: production code knows its drainID and must use RequeueTransactionsWithDrainID.
func (tp *TransactionPool) RequeueTransactions(txs []types.Transaction) {
	tp.capacityMu.Lock()
	var dID uint64
	for _, tx := range txs {
		if tx != nil {
			if id, ok := tp.inFlightTxs[tx.Hash()]; ok {
				dID = id
				break
			}
		}
	}
	tp.capacityMu.Unlock()
	tp.RequeueTransactionsWithDrainID(txs, dID)
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
func (tp *TransactionPool) TransactionsWithDrainID(maxDrain int) ([]types.Transaction, uint64, []byte) {
	var allTxs []types.Transaction
	if maxDrain > 0 {
		initCap := maxDrain
		if initCap > 16384 {
			initCap = 16384
		}
		if cnt := tp.CountTransactions(); cnt > 0 && cnt < initCap {
			initCap = cnt
		}
		if initCap > 0 {
			allTxs = make([]types.Transaction, 0, initCap)
		}
	}
	var drainID uint64
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
			numDrained := int64(len(shard.transactions))
			totalDrained += numDrained

			// Atomically register in-flight reservations under capacityMu before releasing shard.mu
			tp.capacityMu.Lock()
			if drainID == 0 {
				drainID = tp.nextDrainID
				tp.nextDrainID++
				if tp.nextDrainID == 0 {
					tp.nextDrainID = 1
				}
			}
			tp.count -= numDrained
			if tp.count < 0 {
				tp.count = 0
			}
			for _, tx := range shard.transactions {
				if tx != nil {
					tp.inFlightTxs[tx.Hash()] = drainID
				}
			}
			tp.capacityMu.Unlock()

			allTxs = append(allTxs, shard.transactions...)
			if txTraceEnabled {
				for _, tx := range shard.transactions {
					traceTx("DRAIN", tx.FromAddress(), tx.GetNonce(), fmt.Sprintf("shard=%d", i))
				}
			}
			if cap(shard.transactions) <= 64 {
				clear(shard.transactions)
				shard.transactions = shard.transactions[:0]
				clear(shard.transactionKeys)
				clear(shard.txHashMap)
			} else {
				shard.transactions = make([]types.Transaction, 0)
				shard.transactionKeys = make(map[txPoolKey]bool)
				shard.txHashMap = make(map[common.Hash]types.Transaction)
			}
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

	// Preserving original behavior: aggregate sign returns nil
	return allTxs, drainID, nil
}

// TransactionsWithAggSign drains up to maxDrain transactions from the pool.
// TEST HELPER ONLY: it discards the drainID, so the in-flight reservations of the returned txs can only be released through
// the test helpers above. Production code must use TransactionsWithDrainID and release or requeue every drained tx.
func (tp *TransactionPool) TransactionsWithAggSign(maxDrain int) ([]types.Transaction, []byte) {
	txs, _, aggSign := tp.TransactionsWithDrainID(maxDrain)
	return txs, aggSign
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

// GetPendingNonce returns the next available nonce for addr starting from stateNonce,
// accounting for consecutive pending transactions in the mempool.
// Any nonce gaps halt the progression, matching standard go-ethereum behavior.
func (tp *TransactionPool) GetPendingNonce(addr common.Address, stateNonce uint64) uint64 {
	shardIdx := tp.getShardIndex(addr)
	shard := tp.shards[shardIdx]

	shard.mu.RLock()
	defer shard.mu.RUnlock()

	nextNonce := stateNonce
	for {
		key := txPoolKey{addr: addr, nonce: nextNonce}
		if shard.transactionKeys[key] {
			nextNonce++
		} else {
			break
		}
	}
	return nextNonce
}

// GetPendingCount returns the count of transactions currently in the pool for addr.
func (tp *TransactionPool) GetPendingCount(addr common.Address) int {
	shardIdx := tp.getShardIndex(addr)
	shard := tp.shards[shardIdx]

	shard.mu.RLock()
	defer shard.mu.RUnlock()

	count := 0
	for _, tx := range shard.transactions {
		if tx.FromAddress() == addr {
			count++
		}
	}
	return count
}

