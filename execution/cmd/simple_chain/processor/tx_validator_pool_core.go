// @title processor/transaction_processor_pool.go
// @markdown processor/transaction_processor_pool.go - Transaction pool processing, grouping, and batch operations
package processor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor/pipeline"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// TxValidatorPool manages the transaction mempool, pending validations, and batch grouping.
type TxValidatorPool struct {
	env               ITransactionProcessorEnvironment
	offChainProcessor tx_processor.OffChainProcessor
	chainState        *blockchain.ChainState
	storageManager    *storage.StorageManager
	eventSystem       *mt_filters.EventSystem
	transactionPool   *transaction_pool.TransactionPool
	excludedItems     []grouptxns.Item

	futureTxTimeMap map[common.Hash]time.Time

	// FORK-SAFETY: Shared lock — Lock() during real block execution blocks
	// all virtual execution goroutines that hold RLock().
	blockProcessingLock *sync.RWMutex

	noncesCache atomic.Value // Holds *sync.Map for expected nonces caching

	// evictionInProgress guards EvictLowestGasPrice against being triggered
	// concurrently — see addTransactionToPoolInternal for why this matters.
	evictionInProgress atomic.Bool

	// admitMu synchronizes status checks, capacity reservations, evictions, and pool insertions
	// across both single and batch paths, preventing concurrent capacity overshoots.
	admitMu sync.Mutex
	
	// RollupInterceptor allows simple_chain/app.go to intercept System Events (like CreditObserved)
	// before they are sent to the EVM processor, ensuring Zero State Drift across the cluster.
	RollupInterceptor func(tx types.Transaction) bool
}

func (vp *TxValidatorPool) SetRollupInterceptor(interceptor func(tx types.Transaction) bool) {
	vp.RollupInterceptor = interceptor
}

func NewTxValidatorPool(
	env ITransactionProcessorEnvironment,
	offChainProcessor tx_processor.OffChainProcessor,
	chainState *blockchain.ChainState,
	storageManager *storage.StorageManager,
	eventSystem *mt_filters.EventSystem,
	transactionPool *transaction_pool.TransactionPool,
	blockProcessingLock *sync.RWMutex,
) *TxValidatorPool {
	vp := &TxValidatorPool{
		env:                 env,
		offChainProcessor:   offChainProcessor,
		chainState:          chainState,
		storageManager:      storageManager,
		eventSystem:         eventSystem,
		transactionPool:     transactionPool,
		excludedItems:       make([]grouptxns.Item, 0),
		futureTxTimeMap:     make(map[common.Hash]time.Time),
		blockProcessingLock: blockProcessingLock,
	}
	vp.noncesCache.Store(&sync.Map{})

	vp.StartMemoryMonitor()
	return vp
}

// ClearNoncesCache clears the local cache of expected nonces.
// Called on block commits or reverts to reflect updated on-chain state.
//
// A "localNonceFloor" companion cache (advance an optimistic per-address
// nonce floor the moment a batch is handed to Rust for inclusion, so a
// stale-cache re-read right after commit can't misclassify an
// already-forwarded tx as a "future" nonce) was tried and removed
// 2026-09-03. It closed one narrow race but, per live evidence, opened a
// worse one: the "handed to Rust = will consume a nonce" assumption it
// depended on could go false (found via a native-transfer failure path
// that skipped its nonce bump, since fixed independently in
// account_state_db_mutations.go's ExecuteNativeTransfer/LockFree), and once
// that happened the floor -- which by design never regresses except on a
// full revert -- permanently stranded the address until a node restart,
// confirmed live and root-caused via METANODE_TX_TRACE. The race it was
// meant to prevent turned out to already be bounded on its own: a
// stale-cache "future" misclassification requeues (see futureTxs below)
// and gets re-evaluated on the very next tick once the NOMT flush lands --
// self-correcting within about one more commit cycle, not the 5-minute
// FutureTxTimeout ceiling, per the matching comment in
// block_processor_commit.go next to the CommitAsync() calls this races
// against. Given the "fix" was strictly worse than the bounded problem it
// solved, removing it (rather than iterating on it again) was the more
// robust call -- see git history for the full floor implementation this
// replaced.
func (vp *TxValidatorPool) ClearNoncesCache() {
	vp.noncesCache.Store(&sync.Map{})
	logger.Debug("🧹 [POOL] Expected nonces cache cleared (block committed)")
}

// AdvanceNoncesCacheForForwarded updates the shared per-address expected-nonce cache to
// reflect transactions the caller has CONFIRMED were actually forwarded on to Rust (i.e.
// already past any truncation/backpressure re-queue decision), so that address's very next
// pool tick sees its own just-forwarded progress immediately instead of waiting for a full
// commit-and-DB-refetch round trip.
//
// ROOT-CAUSE FIX (2026-09-11) for the two related, previously-abandoned bugs described in
// ProcessTransactionsInPoolSub's long comment above the `nonceMap[from]++` line and in
// ClearNoncesCache's call site (block_processor_commit.go): that 2026-09-02 investigation's
// own same-day attempt at this exact idea regressed a live 1M-tx test to ~50% confirmed
// because it advanced the cache from ProcessTransactionsInPoolSub's LOCAL, PRE-truncation
// nonceMap — crediting transactions TxBatchForwarder.StartForwardingLoop could still slice
// off (targetBlockSize) and re-queue into the pool moments later, so the NEXT tick saw them
// as "past" (already accounted for) and dropped them permanently (CLASSIFY-PAST-DROP).
//
// This version is safe against that exact failure because it is called from the FORWARDER,
// per FFI batch, ONLY with the transactions in THAT batch, and ONLY after
// executor.SubmitTransactionBatch has reported success for it — i.e. strictly after both the
// truncation decision AND the actual hand-off are already resolved, so there is nothing left
// for a later tick to re-queue out from under it. Never regresses a cache entry (compares
// against the current value first), and any residual staleness this can't reach (an address
// with no tx in this tick's batch) is still bounded to at most one commit cycle by
// ClearNoncesCache's existing full wipe on every commit — this function only closes the gap
// for the common case that round trip was paying for, it does not replace it.
func (vp *TxValidatorPool) AdvanceNoncesCacheForForwarded(txs []types.Transaction) {
	if len(txs) == 0 {
		return
	}

	cacheVal := vp.noncesCache.Load()
	var cache *sync.Map
	if cacheVal != nil {
		cache = cacheVal.(*sync.Map)
	} else {
		cache = &sync.Map{}
		vp.noncesCache.Store(cache)
	}

	// A batch is not guaranteed sorted by (address, nonce) — StartForwardingLoop slices
	// targetBlockSize/maxTransactionsPerBatch off the already-sorted validTxs list, so
	// within one such slice ordering per address is preserved, but take the max defensively
	// rather than assume it.
	highestForwardedNonce := make(map[common.Address]uint64, len(txs))
	for _, tx := range txs {
		addr := tx.FromAddress()
		nonce := tx.GetNonce()
		if cur, ok := highestForwardedNonce[addr]; !ok || nonce > cur {
			highestForwardedNonce[addr] = nonce
		}
	}

	for addr, maxNonce := range highestForwardedNonce {
		nextExpected := maxNonce + 1
		if existing, ok := cache.Load(addr); ok {
			if existingNonce, ok2 := existing.(uint64); ok2 && nextExpected <= existingNonce {
				continue // already at least this fresh -- never regress
			}
		}
		cache.Store(addr, nextExpected)
	}
}

// addressesSuspectedStaleCache is the pure decision logic behind
// ProcessTransactionsInPoolSub's forced-refresh of suspiciously-stuck addresses (see
// StaleFutureCacheThreshold's doc comment for the bug this closes) -- pulled out as its own
// function purely so it's directly unit-testable without needing a live AccountStateDB/trie.
// Returns every address with at least one transaction that has been continuously recorded in
// futureTxTimeMap for longer than threshold as of now.
func addressesSuspectedStaleCache(
	txs []types.Transaction,
	futureTxTimeMap map[common.Hash]time.Time,
	threshold time.Duration,
	now time.Time,
) map[common.Address]struct{} {
	suspect := make(map[common.Address]struct{})
	for _, tx := range txs {
		if insertTime, exists := futureTxTimeMap[tx.Hash()]; exists && now.Sub(insertTime) > threshold {
			suspect[tx.FromAddress()] = struct{}{}
		}
	}
	return suspect
}

// SetEnvironment updates the environment reference
func (vp *TxValidatorPool) SetEnvironment(env ITransactionProcessorEnvironment) {
	vp.env = env
}

// StartMemoryMonitor periodically checks OS memory usage and evicts transactions if approaching GoMemLimitGB.
func (vp *TxValidatorPool) StartMemoryMonitor() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if vp.chainState == nil || vp.chainState.GetConfig() == nil {
				continue
			}
			limitGB := vp.chainState.GetConfig().GoMemLimitGB
			if limitGB <= 0 {
				continue
			}

			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)

			limitBytes := uint64(limitGB) * 1024 * 1024 * 1024
			threshold := uint64(float64(limitBytes) * 0.90) // 90% threshold

			if memStats.Alloc > threshold {
				currentCount := vp.transactionPool.CountTransactions()
				if currentCount > 1000 {
					evictCount := currentCount / 2 // Evict 50% of mempool
					logger.Error("🚨 [DDoS PROTECTION] Memory usage high (%d MB > 90%% of limit %d GB). Evicting %d lowest-fee transactions!",
						memStats.Alloc/1024/1024, limitGB, evictCount)
					vp.transactionPool.EvictLowestGasPrice(evictCount)
				}
			}
		}
	}()
}

func (vp *TxValidatorPool) AddExcludedItems(items []grouptxns.Item) {
	vp.excludedItems = items
}

func (vp *TxValidatorPool) GetExcludedItemsCount() int {
	return len(vp.excludedItems)
}

// checkSecpProtoIngress applies the node-local admission rules of Type 0xFF (secp256k1 proto) transactions:
// the signed ChainID must be this node's chain (anti cross-chain replay) and the BLS Sign field must be empty.
// Shared by the single and batch ingress paths so both enforce the same rules. Signature validity itself is
// checked by tx_processor.VerifyTransaction.
func (vp *TxValidatorPool) checkSecpProtoIngress(tx types.Transaction) (int64, error) {
	if tx.Type() != 0xFF {
		return 0, nil
	}
	cfg := vp.chainState.GetConfig()
	if !cfg.SecpOnlyTxSignatures() {
		return transaction.InvalidSign.Code, fmt.Errorf("transaction type 0xFF is disabled on this chain")
	}
	nodeChainID := cfg.ChainId
	if nodeChainID == nil || nodeChainID.Sign() <= 0 {
		return transaction.InvalidChainId.Code, fmt.Errorf("node chain ID is not configured: cannot accept type 0xFF transactions")
	}
	if tx.GetChainID() != nodeChainID.Uint64() {
		return transaction.InvalidChainId.Code, fmt.Errorf("transaction chain ID (%d) does not match node chain ID (%d)", tx.GetChainID(), nodeChainID.Uint64())
	}
	if len(tx.SignBytes()) != 0 {
		return transaction.InvalidSign.Code, fmt.Errorf("transaction type 0xFF must not contain Sign bytes")
	}
	return 0, nil
}

// checkRaftTxSizeLimit verifies that a transaction's serialized size including Raft protobuf overhead
// does not exceed the configured MaxBatchBytes limit when Raft consensus mode is enabled.
func (vp *TxValidatorPool) checkRaftTxSizeLimit(tx types.Transaction) (int64, error) {
	if !raftfeed.Enabled() {
		return 0, nil
	}
	maxBatch := raftfeed.MaxBatchBytes() // always positive: an unset limit falls back to the default
	raw, err := tx.Marshal()
	if err != nil {
		return transaction.InvalidTransaction.Code, fmt.Errorf("failed to marshal transaction for size check: %w", err)
	}
	if size := raftfeed.EncodedTxSize(len(raw)); size > maxBatch {
		return transaction.InvalidTransaction.Code, fmt.Errorf("transaction serialized size %d exceeds max batch limit %d", size, maxBatch)
	}
	return 0, nil
}

// GetTransactionPool returns the underlying mempool transaction pool.
func (vp *TxValidatorPool) GetTransactionPool() *transaction_pool.TransactionPool {
	if vp == nil {
		return nil
	}
	return vp.transactionPool
}

// AddTransactionToPool validates and adds a transaction to the pool
func (vp *TxValidatorPool) AddTransactionToPool(tx types.Transaction) (int64, error) {
	return vp.addTransactionToPoolInternal(tx, false)
}

// AddVerifiedTransactionToPool adds a pre-verified transaction to the pool
// Used by Go Master when receiving transactions from Go Sub nodes that have already verified signatures.
func (vp *TxValidatorPool) AddVerifiedTransactionToPool(tx types.Transaction) (int64, error) {
	return vp.addTransactionToPoolInternal(tx, true)
}

// AddTransactionsToPool validates and adds a batch of transactions to the pool efficiently
func (vp *TxValidatorPool) AddTransactionsToPool(txs []types.Transaction) []error {
	return vp.addTransactionsToPoolInternal(txs, false)
}

// AddAdmittedTransactionsToPool is AddTransactionsToPool for callers that already reserved room in the mempool (the
// TCP submission path waits for room first, see TransactionProcessor.waitForPoolRoom). It skips the pool's own
// evict-lowest-fee-or-reject step: that step runs against a count the forwarder temporarily lowers while it holds a
// drained batch, so it can still fire right after admission and drop pending transactions of fire-and-forget senders.
func (vp *TxValidatorPool) AddAdmittedTransactionsToPool(txs []types.Transaction) []error {
	return vp.addTransactionsToPoolInternalCap(txs, false, false)
}

// AddAdmittedTransactionToPool is the single-transaction counterpart of AddAdmittedTransactionsToPool.
func (vp *TxValidatorPool) AddAdmittedTransactionToPool(tx types.Transaction) (int64, error) {
	return vp.addTransactionToPoolInternalCap(tx, false, false)
}

// AddVerifiedTransactionsToPool adds a batch of pre-verified transactions to the pool
func (vp *TxValidatorPool) AddVerifiedTransactionsToPool(txs []types.Transaction) []error {
	return vp.addTransactionsToPoolInternal(txs, true)
}

// RequeueTransactions re-adds previously drained transactions back into the mempool.
// Because these transactions held in-flight reservations since being drained, requeuing
// them transitions their reserved slots back into the pool shards without dropping any transactions.
func (vp *TxValidatorPool) RequeueTransactions(txs []types.Transaction) {
	if len(txs) == 0 || vp == nil || vp.transactionPool == nil {
		return
	}
	vp.transactionPool.RequeueTransactions(txs)
}

// RequeueTransactionsWithDrainID re-adds previously drained transactions holding a matching drainID reservation.
func (vp *TxValidatorPool) RequeueTransactionsWithDrainID(txs []types.Transaction, drainID uint64) int {
	if len(txs) == 0 || vp == nil || vp.transactionPool == nil {
		return 0
	}
	return vp.transactionPool.RequeueTransactionsWithDrainID(txs, drainID)
}

// addTransactionToPoolInternal handles the core logic with an option to skip expensive verification
func (vp *TxValidatorPool) addTransactionToPoolInternal(tx types.Transaction, skipVerification bool) (int64, error) {
	return vp.addTransactionToPoolInternalCap(tx, skipVerification, true)
}

// rejectIfPoolFullAndEvicting is the CHEAP early rejection for a saturated pool: when the pool cannot take n more txs
// and an eviction pass is already running, the caller would only be rejected after paying for signature verification, so
// reject it before any verification. Eviction itself still happens only AFTER a tx has been verified (so invalid spam
// can never evict legitimate txs). Returns nil when the caller should proceed.
func (vp *TxValidatorPool) rejectIfPoolFullAndEvicting(n int) error {
	if vp.evictionInProgress.Load() && vp.transactionPool.UsedSlots()+n >= MaxMempoolSize {
		return fmt.Errorf("%w (limit=%d), eviction already in progress", transaction.ErrPoolFull, MaxMempoolSize)
	}
	return nil
}

// addTransactionToPoolInternalCap is addTransactionToPoolInternal with the MaxMempoolSize evict-or-reject step optional.
// When allowEviction is false (e.g. TCP ingress), eviction of other transactions is disabled, but hard capacity is strictly enforced.
func (vp *TxValidatorPool) addTransactionToPoolInternalCap(tx types.Transaction, skipVerification bool, allowEviction bool) (int64, error) {
	if vp == nil {
		return transaction.InvalidTransaction.Code, fmt.Errorf("TxValidatorPool is nil")
	}
	if tx == nil {
		return transaction.InvalidTransaction.Code, fmt.Errorf("tx nil")
	}
	if allowEviction {
		if err := vp.rejectIfPoolFullAndEvicting(1); err != nil {
			return transaction.AddToPoolError.Code, err
		}
	}

	minGasPrice := vp.chainState.GetConfig().MinGasPrice
	if minGasPrice > 0 {
		gasCap := tx.GasPriceCap()
		if gasCap == nil || gasCap.Cmp(new(big.Int).SetUint64(minGasPrice)) < 0 {
			var capStr string
			if gasCap != nil {
				capStr = gasCap.String()
			} else {
				capStr = fmt.Sprintf("%d", tx.MaxGasPrice())
			}
			return transaction.InvalidTransaction.Code, fmt.Errorf("transaction gas price (%s) is below node minimum (%d)", capStr, minGasPrice)
		}
	}

	if code, err := vp.checkSecpProtoIngress(tx); err != nil {
		return code, err
	}

	if code, err := vp.checkRaftTxSizeLimit(tx); err != nil {
		return code, err
	}

	if storage.GetLastBlockNumberFromMaster() > storage.GetLastBlockNumber()+3 {
		return transaction.NodeSyncingError.Code, fmt.Errorf(transaction.NodeSyncingError.Description)
	}

	// FORK-SAFETY: Native TXs (e.g. BLS registration) need FromAddress and
	// ToAddress in RelatedAddresses so the EVM's isAddressAllowed check passes
	// during ProcessNonceOnly. This is guaranteed unconditionally by
	// Transaction.RelatedAddresses(), which always dynamically resolves to
	// [FromAddress, ToAddress] — no explicit population call is needed here.

	// Phase 1.5 (TPS Optimization): Cache Warming
	// Pre-fetch both sender and recipient into trie LRU cache now,
	// while we are in the async injection worker. This saves disk I/O
	// later during the critical block processing phase.
	as, _ := vp.chainState.GetAccountStateDB().AccountStateReadOnly(tx.FromAddress())
	if !tx.IsDeployContract() {
		_, _ = vp.chainState.GetAccountStateDB().AccountStateReadOnly(tx.ToAddress())
	}

	// ZERO-GRIEFING SECURITY: verify the transaction BEFORE evicting any pool entries.
	// Otherwise an attacker submitting invalid/forged transactions could trigger mass eviction of valid txs.
	if !skipVerification {
		if err := tx_processor.VerifyTransaction(tx, vp.chainState, as); err != nil {
			logger.Error("Transaction verification failed: %v", err)
			return err.Code, err
		}
	}

	// PRE-EVICTION SIFTING: estimate whether tx is net-new or replacement/duplicate.
	// Only net-new transactions need room made when pool sits at capacity.
	addStatus, _ := vp.transactionPool.CheckAddStatus(tx)

	// In-place replacement (TxAddStatusReplace) does not increase pool size, so no eviction is needed.
	// Only net-new transactions (TxAddStatusNew) need room made when pool sits at capacity.
	// Eviction runs outside locks with evictionInProgress CAS so heavy scan/sort never stalls concurrent callers.
	if allowEviction && addStatus == transaction_pool.TxAddStatusNew && vp.transactionPool.AvailableSlots() == 0 {
		newGas := tx.GasPriceCap()
		if newGas == nil {
			newGas = new(big.Int).SetUint64(tx.MaxGasPrice())
		}
		if vp.evictionInProgress.CompareAndSwap(false, true) {
			logger.Warn("⚠️ Mempool is full (limit=%d). Evicting lowest-fee transactions to make room for new txs.", MaxMempoolSize)
			evicted := vp.transactionPool.EvictLowestGasPriceThan(mempoolEvictBatchSize, newGas)
			vp.evictionInProgress.Store(false)
			if evicted == 0 || vp.transactionPool.AvailableSlots() == 0 {
				return transaction.ErrPoolFull.Code, fmt.Errorf("%w: transaction pool is full (limit=%d) and transaction underpriced to evict", transaction.ErrPoolFull, MaxMempoolSize)
			}
		}
	}

	// Insert atomically under shard lock (authoritatively enforces capacity, price bumps, and duplicates)
	err := vp.transactionPool.AddTransaction(tx)
	if err != nil {
		if errors.Is(err, transaction.ErrPoolFull) {
			return transaction.ErrPoolFull.Code, err
		}
		if errors.Is(err, transaction.ErrAlreadyKnown) {
			return transaction.ErrAlreadyKnown.Code, fmt.Errorf("%w: transaction already exists in pool, skipping", transaction.ErrAlreadyKnown)
		}
		if errors.Is(err, transaction.ErrReplacementUnderpriced) {
			return transaction.ErrReplacementUnderpriced.Code, fmt.Errorf("%w: replacement transaction underpriced", transaction.ErrReplacementUnderpriced)
		}
		logger.Error("❌ [TX FLOW] Failed to add transaction to pool: %v", err)
		var te *transaction.TransactionError
		if errors.As(err, &te) {
			return te.Code, err
		}
		return transaction.AddToPoolError.Code, fmt.Errorf("failed to add transaction %s to pool: %w", tx.Hash().Hex(), err)
	}

	// Pipeline stats: track TX received
	GlobalPipelineStats.IncrTxsReceived(1)

	// Log khi transaction được thêm vào pending pool và transaction pool
	// txHash := tx.Hash().Hex()
	// fromAddr := tx.FromAddress().Hex()
	// nonce := tx.GetNonce()
	// poolSize := vp.transactionPool.CountTransactions()
	// logger.Info("✅ [TX FLOW] Transaction added to pending pool and transaction pool: txHash=%s, from=%s, nonce=%d, status=InPool, pool_size=%d",
	// 	txHash, fromAddr[:10]+"...", nonce, poolSize)

	// NOTE: TX forwarding to Rust is handled by TxsProcessor2 (block_processor_txs.go)
	// which retrieves TXs from the pool periodically and forwards via UDS or TCP fallback.
	// DO NOT forward here — duplicate forwarding causes TX to be included in multiple blocks,
	// producing duplicate receipts (1 success + 1 "invalid nonce").

	return 0, nil
}

// addTransactionsToPoolInternal efficiently processes a batch of transactions.
// It verifies them individually but adds them to the pool and pending manager in bulk
// to minimize lock contention.
func (vp *TxValidatorPool) addTransactionsToPoolInternal(txs []types.Transaction, skipVerification bool) []error {
	return vp.addTransactionsToPoolInternalCap(txs, skipVerification, true)
}

// addTransactionsToPoolInternalCap is addTransactionsToPoolInternal with the MaxMempoolSize evict-or-reject step optional.
// When allowEviction is false (e.g. TCP ingress), eviction of other transactions is disabled, but hard capacity is strictly enforced.
func (vp *TxValidatorPool) addTransactionsToPoolInternalCap(txs []types.Transaction, skipVerification bool, allowEviction bool) []error {
	if vp == nil {
		errs := make([]error, len(txs))
		for i := range errs {
			errs[i] = fmt.Errorf("TxValidatorPool is nil")
		}
		return errs
	}
	// Disabled vp_debug.log writing in hot-path for performance

	if len(txs) == 0 {
		return nil
	}
	if allowEviction {
		if err := vp.rejectIfPoolFullAndEvicting(len(txs)); err != nil {
			errs := make([]error, len(txs))
			for i := range errs {
				errs[i] = err
			}
			return errs
		}
	}

	minGasPrice := vp.chainState.GetConfig().MinGasPrice

	if storage.GetLastBlockNumberFromMaster() > storage.GetLastBlockNumber()+3 {
		err := fmt.Errorf(transaction.NodeSyncingError.Description)
		errs := make([]error, len(txs))
		for i := range errs {
			errs[i] = err
		}
		return errs
	}

	t0 := time.Now()
	var validTxs []types.Transaction
	var errorsList = make([]error, len(txs))
	for i, tx := range txs {
		if tx == nil {
			continue
		}
		if code, err := vp.checkSecpProtoIngress(tx); err != nil {
			errorsList[i] = fmt.Errorf("[code:%d] %s", code, err.Error())
			continue
		}
		if code, err := vp.checkRaftTxSizeLimit(tx); err != nil {
			errorsList[i] = fmt.Errorf("[code:%d] %s", code, err.Error())
			continue
		}
	}

	// Phase 1.5 (TPS Optimization): Batch Cache Warming
	// Collect unique addresses to fetch in parallel without blocking muTrie.Lock
	preloadSet := make(map[common.Address]struct{}, len(txs)*2)
	for i, tx := range txs {
		if tx == nil || errorsList[i] != nil {
			continue
		}
		preloadSet[tx.FromAddress()] = struct{}{}
		if !tx.IsDeployContract() {
			preloadSet[tx.ToAddress()] = struct{}{}
		}
	}
	var preloadAddrs []common.Address
	if len(preloadSet) > 0 {
		preloadAddrs = make([]common.Address, 0, len(preloadSet))
		for addr := range preloadSet {
			preloadAddrs = append(preloadAddrs, addr)
		}
		vp.chainState.GetAccountStateDB().PreloadAccounts(preloadAddrs)
	}
	cacheWarmingDuration := time.Since(t0)

	t1 := time.Now()
	// Warm the local states map to pass directly to VerifyTransaction, avoiding concurrent sync.Map lookups.
	senderStates := make(map[common.Address]types.AccountState, len(preloadAddrs))
	if len(preloadAddrs) > 0 {
		var senderStatesMutex sync.Mutex
		// GOMAXPROCS(0), not NumCPU(): see native_fast_path.go for why.
		numPreloadWorkers := runtime.GOMAXPROCS(0) / 2
		if numPreloadWorkers < 4 {
			numPreloadWorkers = 4
		}
		if numPreloadWorkers > 64 {
			numPreloadWorkers = 64
		}
		if len(preloadAddrs) < numPreloadWorkers {
			numPreloadWorkers = len(preloadAddrs)
		}
		var wgPreload sync.WaitGroup
		wgPreload.Add(numPreloadWorkers)
		chunkSizePreload := (len(preloadAddrs) + numPreloadWorkers - 1) / numPreloadWorkers

		for w := 0; w < numPreloadWorkers; w++ {
			start := w * chunkSizePreload
			end := start + chunkSizePreload
			if end > len(preloadAddrs) {
				end = len(preloadAddrs)
			}
			go func(s, e int) {
				defer wgPreload.Done()
				localStates := make(map[common.Address]types.AccountState, e-s)
				for i := s; i < e; i++ {
					addr := preloadAddrs[i]
					if as, err := vp.chainState.GetAccountStateDB().AccountStateReadOnly(addr); err == nil && as != nil {
						localStates[addr] = as
					}
				}
				senderStatesMutex.Lock()
				for k, v := range localStates {
					senderStates[k] = v
				}
				senderStatesMutex.Unlock()
			}(start, end)
		}
		wgPreload.Wait()
	}
	statePreloadDuration := time.Since(t1)

	t2 := time.Now()
	// Phase 2 (TPS Optimization): Parallel VerifyTransactionWithState
	// BLS verification is CPU heavy (~1ms per tx). 2000 txs = 2 seconds if sequential.
	// PERF: Cap workers at numCPU/2 (max 48) to reduce sync.Map contention on
	// verifiedSignaturesCache. 104 goroutines cause excessive cache-line bouncing.
	if !skipVerification {
		// Batch-verify all BLS signatures first (blst random-linear-combination, ~2-3x cheaper CPU per
		// signature than one-by-one) so the per-tx VerifyTransaction calls below hit the warm cache.
		tx_processor.PrewarmSignatureCache(vp.chainState, txs, senderStates)
	}

	// GOMAXPROCS(0), not NumCPU(): see native_fast_path.go for why.
	numWorkers := runtime.GOMAXPROCS(0) / 2
	if numWorkers < 4 {
		numWorkers = 4
	}
	if numWorkers > 48 {
		numWorkers = 48
	}
	if len(txs) < numWorkers {
		numWorkers = len(txs)
	}

	var wg sync.WaitGroup
	wg.Add(numWorkers)
	chunkSize := (len(txs) + numWorkers - 1) / numWorkers

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(txs) {
			end = len(txs)
		}
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				if txs[i] == nil || errorsList[i] != nil {
					continue
				}
				if minGasPrice > 0 {
					gasCap := txs[i].GasPriceCap()
					if gasCap == nil || gasCap.Cmp(new(big.Int).SetUint64(minGasPrice)) < 0 {
						var capStr string
						if gasCap != nil {
							capStr = gasCap.String()
						} else {
							capStr = fmt.Sprintf("%d", txs[i].MaxGasPrice())
						}
						errorsList[i] = fmt.Errorf("[code:%d] transaction gas price (%s) is below node minimum (%d)", transaction.InvalidTransaction.Code, capStr, minGasPrice)
						continue
					}
				}
				if !skipVerification {
					var senderState types.AccountState
					if senderStates != nil {
						senderState = senderStates[txs[i].FromAddress()]
					}
					if err := tx_processor.VerifyTransaction(txs[i], vp.chainState, senderState); err != nil {
						errorsList[i] = fmt.Errorf("[code:%d] %w", err.Code, err)
					}
				}
			}
		}(start, end)
	}
	wg.Wait()

	verificationDuration := time.Since(t2)

	t3 := time.Now()
	validTxs = make([]types.Transaction, 0, len(txs))
	// Pre-screen valid transactions to estimate net-new count and batch floor for eviction.
	// Runs outside locks so heavy scan never stalls concurrent callers.
	netNewCount := 0
	var batchFloor *big.Int

	for i, tx := range txs {
		if tx == nil {
			errorsList[i] = fmt.Errorf("tx nil")
			continue
		}
		if errorsList[i] == nil {
			status, _ := vp.transactionPool.CheckAddStatus(tx)
			if status == transaction_pool.TxAddStatusNew {
				netNewCount++
				g := tx.GasPriceCap()
				if g == nil {
					g = new(big.Int).SetUint64(tx.MaxGasPrice())
				}
				if batchFloor == nil || g.Cmp(batchFloor) < 0 {
					batchFloor = g
				}
			}
			validTxs = append(validTxs, tx)
		}
	}
	conflictChecksDuration := time.Since(t3)

	t4 := time.Now()
	// Limit pool size to prevent GC stall / OOM
	// Eviction runs AFTER verification so invalid spam transactions do not evict legitimate mempool transactions.
	// Eviction runs OUTSIDE locks using single-flight CAS guard so heavy scan/sort never stalls concurrent callers.
	if allowEviction && netNewCount > 0 && vp.transactionPool.UsedSlots()+netNewCount > MaxMempoolSize {
		if vp.evictionInProgress.CompareAndSwap(false, true) {
			evictCount := netNewCount
			if evictCount < 1000 {
				evictCount = 1000
			}
			logger.Warn("⚠️ Mempool is near full (limit=%d). Evicting %d lowest-fee transactions to make room for batch.", MaxMempoolSize, evictCount)
			vp.transactionPool.EvictLowestGasPriceThan(evictCount, batchFloor)
			vp.evictionInProgress.Store(false)
		}
	}

	// Insert atomically through TransactionPool under per-shard locks.
	// Every transaction's outcome is authoritatively determined under the shard write lock.
	var txsToInsert []types.Transaction
	var origIndices []int
	for i, tx := range txs {
		if errorsList[i] == nil && tx != nil {
			txsToInsert = append(txsToInsert, tx)
			origIndices = append(origIndices, i)
		}
	}

	var totalInserted int
	if len(txsToInsert) > 0 {
		insertErrs := vp.transactionPool.AddTransactions(txsToInsert)
		for j, err := range insertErrs {
			origIdx := origIndices[j]
			if err != nil {
				if errors.Is(err, transaction.ErrAlreadyKnown) {
					// Idempotent duplicate: already known in pool
					errorsList[origIdx] = nil
				} else if errors.Is(err, transaction.ErrReplacementUnderpriced) {
					errorsList[origIdx] = fmt.Errorf("[code:%d] %w", transaction.ErrReplacementUnderpriced.Code, err)
				} else if errors.Is(err, transaction.ErrPoolFull) {
					errorsList[origIdx] = fmt.Errorf("[code:%d] %w", transaction.ErrPoolFull.Code, err)
				} else {
					errorsList[origIdx] = err
				}
			} else {
				errorsList[origIdx] = nil
				totalInserted++
			}
		}
	}

	if totalInserted > 0 {
		GlobalPipelineStats.IncrTxsReceived(int64(totalInserted))
	}
	poolInsertionDuration := time.Since(t4)

	totalDuration := time.Since(t0)
	if totalDuration > 10*time.Millisecond {
		logger.Warn("⏱️  [PERF-POOL-BATCH] addTransactionsToPoolInternal took %v (cache_warm=%v, state_preload=%v, verify=%v, conflicts=%v, insert=%v) for %d txs (valid=%d)",
			totalDuration, cacheWarmingDuration, statePreloadDuration, verificationDuration, conflictChecksDuration, poolInsertionDuration, len(txs), len(validTxs))
	}

	return errorsList
}

// StartPreloadAccounts initiates asynchronous account prefetching, returning a channel that unblocks when done.
func (vp *TxValidatorPool) StartPreloadAccounts(txs []types.Transaction) <-chan struct{} {
	preloadDone := make(chan struct{})
	go func() {
		defer close(preloadDone)
		uniqueMap := make(map[common.Address]struct{}, len(txs)*2)
		for _, tx := range txs {
			uniqueMap[tx.FromAddress()] = struct{}{}
			if !tx.IsDeployContract() {
				uniqueMap[tx.ToAddress()] = struct{}{}
			}
		}
		addrSlice := make([]common.Address, 0, len(uniqueMap))
		for addr := range uniqueMap {
			addrSlice = append(addrSlice, addr)
		}
		sort.Slice(addrSlice, func(i, j int) bool {
			return bytes.Compare(addrSlice[i].Bytes(), addrSlice[j].Bytes()) < 0
		})
		vp.chainState.GetAccountStateDB().PreloadAccounts(addrSlice)
	}()
	return preloadDone
}

var LastBlockProcessedTimeNano atomic.Int64

// ProcessTransactions processes a batch of transactions through grouping and execution.
// blockTime is the deterministic timestamp (in seconds) from Rust consensus for EVM block.timestamp.
func (vp *TxValidatorPool) ProcessTransactions(txs []types.Transaction, blockTime uint64, leaderAddr common.Address, externalPreload <-chan struct{}, blockNum uint64) (
	tx_processor.ProcessResult,
	error,
) {

	consensusStartNano := LastSendBatchTimeNano.Load()
	var consensusDuration time.Duration
	if consensusStartNano > 0 && len(txs) > 0 {
		lastProcessed := LastBlockProcessedTimeNano.Load()
		if consensusStartNano > lastProcessed {
			LastBlockProcessedTimeNano.Store(consensusStartNano)
			lastProcessed = consensusStartNano
		}
		consensusDuration = time.Since(time.Unix(0, lastProcessed))
	}

	if len(txs) > 0 {
		storage.SetCommitLock(true)
	}

	// PERF OPT (C): Sample-based WaitGo/WaitRust metrics instead of iterating all TXs.
	// We iterate through the batch to find up to 10 locally submitted transactions.
	// This prevents incorrect 0 metrics when testing with multi-node RPC pools.
	var avgWaitGoUs, avgWaitRustUs int64
	now := time.Now()
	if vp.env != nil && len(txs) > 0 {
		const maxSamples = 10
		var totalWaitGoUs, totalWaitRustUs int64
		var waitCount int64
		for _, tx := range txs {
			if entry, ok := vp.env.GetTxHashConnEntry(tx.Hash()); ok {
				waitCount++
				waitGoUs := entry.SentToRustAt.Sub(entry.CreatedAt).Microseconds()
				if waitGoUs < 0 {
					waitGoUs = 0
				}
				totalWaitGoUs += waitGoUs

				waitRustUs := now.Sub(entry.SentToRustAt).Microseconds()
				if waitRustUs < 0 {
					waitRustUs = 0
				}
				totalWaitRustUs += waitRustUs

				if waitCount >= maxSamples {
					break
				}
			}
		}
		if waitCount > 0 {
			avgWaitGoUs = totalWaitGoUs / waitCount
			avgWaitRustUs = totalWaitRustUs / waitCount
			// logger.Info("✅ [WAIT-DEBUG] waitCount=%d, avgWaitGoUs=%d, avgWaitRustUs=%d", waitCount, avgWaitGoUs, avgWaitRustUs)
		} else {
			logger.Warn("⚠️  [WAIT-DEBUG] waitCount=0! Could not find any local transactions in %d txs to measure WaitGo.", len(txs))
		}
	}
	pipeline.GlobalBlockTraceStore.SetWaitTime(blockNum, avgWaitGoUs, avgWaitRustUs)

	// PERF OPT (B): Send txs directly to event feed without copying the entire slice.
	ev := mt_filters.NewTxsEvent{
		Txs: txs,
	}
	vp.eventSystem.TxsFeed.Send(ev)

	// --- AUTO-FLUSH LOGIC (OOM Prevention) ---
	// Increment the global TX counter and check PebbleDB MemTable metrics.
	// PERF OPT: Flush runs ASYNC in background goroutine to avoid stalling
	// the block processing hot path (~200-500ms per flush).
	// CAS guard prevents concurrent flushes from racing.
	currentCount := atomic.AddUint64(&tx_processor.GlobalTxProcessCounter, uint64(len(txs)))

	shouldFlush := false
	var memSize uint64
	sm := vp.chainState.GetStorageManager()
	if sm != nil {
		memSize = sm.GetMemTableSize()
		if memSize > 64*1024*1024 { // 64MB threshold
			shouldFlush = true
		}
	}

	if shouldFlush || currentCount > tx_processor.FlushThresholdTxs {
		// CAS: only one goroutine triggers the flush (prevent concurrent flushes)
		if atomic.CompareAndSwapUint64(&tx_processor.GlobalTxProcessCounter, currentCount, 0) {
			if sm != nil {
				go func(count uint64, size uint64) {
					startFlush := time.Now()

					// Watchdog: Cảnh báo nếu dọn RAM bị đứng quá 5 giây
					stopWatchdogFlush := utils.StartWatchdog("FLUSH", 5*time.Second, func() string {
						return "Tiến trình dọn RAM (FlushAll) đang BỊ ĐỨNG!"
					})

					if size > 0 {
						logger.Warn("🧹 [AUTO-FLUSH] PebbleDB MemTableSize %d MB reached / %d TXs. Flushing LazyPebbleDB to disk async...", size/1024/1024, count)
					} else {
						logger.Warn("🧹 [AUTO-FLUSH] Reached %d TXs (threshold %d). Flushing LazyPebbleDB to disk async...", count, tx_processor.FlushThresholdTxs)
					}
					err := sm.FlushAll()
					stopWatchdogFlush() // Tắt watchdog khi xong

					if err != nil {
						logger.Error("❌ [AUTO-FLUSH] Failed to flush storage: %v", err)
					} else {
						logger.Warn("✅ [AUTO-FLUSH] Successfully flushed to disk in %v (async)", time.Since(startFlush))
					}
				}(currentCount, memSize)
			}
		}
	}

	// CRITICAL FORK-SAFETY: Clear excludedItems before processing Rust committed blocks.
	if len(vp.excludedItems) > 0 {
		logger.Info("🔒 [FORK-SAFETY] Clearing %d excludedItems before processing Rust committed block", len(vp.excludedItems))
		vp.excludedItems = nil
	}

	startVirtual := time.Now()
	virtualDuration := time.Since(startVirtual)

	// OPTIMIZATION: Wait for PreloadAccounts concurrently (deterministic — safe for fork-safety)
	var preloadDone <-chan struct{}
	if externalPreload != nil {
		preloadDone = externalPreload
	} else {
		preloadDone = vp.StartPreloadAccounts(txs)
	}
	// ═══════════════════════════════════════════════════════════════════════════
	// FORK-SAFE PARALLEL GROUPING: Group TXs by shared RelatedAddresses
	//
	// Previously, all TXs were placed in a SINGLE group → only 1 CPU used.
	// Now, GroupTransactionsDeterministic uses Union-Find to split TXs into
	// independent groups (no shared addresses between groups).
	//
	// FORK-SAFETY guarantees:
	//   - Same TXs → same groups on ALL nodes (Union-Find is deterministic)
	//   - Within each group: sorted by (FromAddress, Nonce, Hash)
	//   - Groups sorted by min TX hash → deterministic order
	//   - NO TX is dropped (no gas/time limits)
	//   - NO time.Now() or non-deterministic input
	//
	// PERFORMANCE: TXs from independent senders run in PARALLEL across NumCPU
	//   workers in processGroupsConcurrently. Only TXs sharing addresses
	//   (e.g., multiple TXs to same contract) are serialized within a group.
	// ═══════════════════════════════════════════════════════════════════════════
	items := make([]grouptxns.Item, 0, len(txs))
	// Optimize: Pre-allocate a single flat array to avoid per-tx memory allocation
	allAddrs := make([]common.Address, 0, len(txs)*3)

	for i, tx := range txs {
		if vp.RollupInterceptor != nil {
			vp.RollupInterceptor(tx)
		}

		startIdx := len(allAddrs)
		allAddrs = grouptxns.AppendDeterministicGroupAddrs(tx, allAddrs)
		endIdx := len(allAddrs)

		items = append(items, grouptxns.Item{
			ID:      i,
			Array:   allAddrs[startIdx:endIdx:endIdx], // Fix capacity leak
			GroupID: 0,
			Tx:      tx,
		})
	}

	groupedGroups := grouptxns.GroupTransactionsDeterministic(items, vp.chainState.HasCode)

	logger.Info("🔒 [FORK-SAFETY] Deterministic grouping: %d TXs → %d parallel groups (bypassed GroupAndLimit)", len(txs), len(groupedGroups))

	// Wait for preload to finish before proceeding to execution
	<-preloadDone

	// PERF OPT (A): Signature pre-verification REMOVED from hot path.
	// Rationale: addTransactionsToPoolInternal() already calls VerifyTransaction()
	// during TX injection, which populates the BLS verifiedSignaturesCache.
	// The pre-verify step here was redundant — re-fetching AccountState,
	// re-running VerifyTransaction for 40-50k TXs (all cache hits) added
	// 100-300ms per block. This delay cascaded as increased WaitRust
	// for subsequent blocks, causing E2E TPS regression from 5644 to 3590.
	// The BLS cache in processSingleGroup's VerifyTransaction call
	// will still hit the warm cache from injection.

	ctx := context.Background()

	var baseCtx context.Context
	var rootSpan *trace.Span
	enableTrace := false
	myCollector := trace.NewSpanCollector()

	if enableTrace {
		tracedCtx, actualSpan := trace.NewTrace(ctx, "ProcessBlockTransactions", map[string]interface{}{}, myCollector)
		baseCtx = tracedCtx
		rootSpan = actualSpan
		defer rootSpan.End()
		rootSpan.AddEvent("Starting transaction processing", nil)
	} else {
		baseCtx = ctx
		rootSpan = nil
	}

	waitLockStart := time.Now()
	vp.blockProcessingLock.Lock()
	lockWaitDuration := time.Since(waitLockStart)

	startExecution := time.Now()

	// Watchdog: Cảnh báo nếu block xử lý quá chậm hoặc bị kẹt
	stopWatchdogExec := utils.StartWatchdog("BLOCK", 5*time.Second, func() string {
		return fmt.Sprintf("Block #%d (chứa %d TXs) ĐANG BỊ KẸT ở ProcessTransactions!", blockNum, len(txs))
	})

	res, execErr := tx_processor.ProcessTransactions(baseCtx, vp.chainState, groupedGroups, enableTrace, true, blockTime, leaderAddr, blockNum, false)
	stopWatchdogExec() // Tắt watchdog khi xong
	vp.blockProcessingLock.Unlock()
	execDuration := time.Since(startExecution)

	if len(txs) > 0 {
		logger.Warn("⏱️  [BLOCK-PERF] Block #%d: TXs=%d | VirtualExec=%v | Consensus=%v | LockWait=%v | RealExec=%v",
			blockNum, len(res.Transactions), virtualDuration.Round(time.Microsecond), consensusDuration.Round(time.Millisecond), lockWaitDuration.Round(time.Millisecond), execDuration.Round(time.Millisecond))

		pipeline.GlobalBlockTraceStore.AddConsensusAndExecTime(blockNum, len(res.Transactions), consensusDuration.Microseconds(), execDuration.Microseconds())
	}

	if execDuration.Microseconds() > 100 {
		logger.Info("⏱️  [PERF] tx_processor.ProcessTransactions (EVM/State) of %d TXs took %v (WaitLock: %v)", len(txs), execDuration, lockWaitDuration)
	}

	return res, execErr
}

// ProcessTransactionsInPoolSub retrieves transactions from pool for sub-node
// forwarding. maxDrain bounds how many raw transactions get pulled from the
// pool this call (0 = unbounded) -- see TransactionsWithAggSign's comment for
// why: with a huge backlog, sorting/validating everything on every tick just
// to use a small slice of it made a single tick take tens of seconds.
func (vp *TxValidatorPool) ProcessTransactionsInPoolSub(setEmptyBlock bool, maxDrain int) ([]types.Transaction, uint64) {
	var txs []types.Transaction
	var drainID uint64
	if setEmptyBlock {
		txs = make([]types.Transaction, 0)
	} else {
		allTxs, dID, _ := vp.transactionPool.TransactionsWithDrainID(maxDrain)
		drainID = dID

		if len(allTxs) == 0 {
			return allTxs, drainID
		}

		startProcess := time.Now()
		traceEnabled := transaction_pool.TxTraceEnabled()

		// Preload accounts in batch to warm cache and avoid synchronous DB I/O inside loop
		preloadSet := make(map[common.Address]struct{}, len(allTxs))
		for _, tx := range allTxs {
			preloadSet[tx.FromAddress()] = struct{}{}
		}

		validTxs := make([]types.Transaction, 0, len(allTxs))
		var futureTxs []types.Transaction
		var droppedTxs []types.Transaction
		nonceMap := make(map[common.Address]uint64, len(preloadSet))

		if len(preloadSet) > 0 {
			preloadAddrs := make([]common.Address, 0, len(preloadSet))
			for addr := range preloadSet {
				preloadAddrs = append(preloadAddrs, addr)
			}
			vp.chainState.GetAccountStateDB().PreloadAccounts(preloadAddrs)

			// Load the current noncesCache
			cacheVal := vp.noncesCache.Load()
			var cache *sync.Map
			if cacheVal != nil {
				cache = cacheVal.(*sync.Map)
			} else {
				cache = &sync.Map{}
				vp.noncesCache.Store(cache)
			}

			// Addresses with a transaction that has been sitting classified "future" for
			// longer than StaleFutureCacheThreshold are suspected to be victims of the
			// noncesCache staleness race (see that constant's doc comment) -- force them
			// through the cache-miss/DB-refetch path below instead of trusting whatever is
			// currently cached, so they get a fresh, authoritative answer THIS tick rather
			// than waiting on an unrelated future commit to clear the whole cache.
			suspectStaleAddrs := addressesSuspectedStaleCache(allTxs, vp.futureTxTimeMap, StaleFutureCacheThreshold, time.Now())

			// First, resolve nonces using the cache
			var missingAddrs []common.Address
			for _, addr := range preloadAddrs {
				if _, suspectStale := suspectStaleAddrs[addr]; !suspectStale {
					if val, ok := cache.Load(addr); ok {
						nonceMap[addr] = val.(uint64)
						if traceEnabled {
							transaction_pool.TraceTx("CACHE-HIT", addr, val.(uint64), fmt.Sprintf("cachePtr=%p", cache))
						}
						continue
					}
				} else if traceEnabled {
					transaction_pool.TraceTx("CACHE-SUSPECT-STALE", addr, 0, "future tx stuck past StaleFutureCacheThreshold, forcing fresh DB read")
				}
				missingAddrs = append(missingAddrs, addr)
				if traceEnabled {
					transaction_pool.TraceTx("CACHE-MISS", addr, 0, fmt.Sprintf("cachePtr=%p", cache))
				}
			}

			// If there are cache misses, fetch nonces from DB in parallel
			if len(missingAddrs) > 0 {
				var nonceMapMutex sync.Mutex
				// GOMAXPROCS(0), not NumCPU(): see native_fast_path.go for why.
				numWorkers := runtime.GOMAXPROCS(0) / 2
				if numWorkers < 4 {
					numWorkers = 4
				}
				if numWorkers > 48 {
					numWorkers = 48
				}
				if len(missingAddrs) < numWorkers {
					numWorkers = len(missingAddrs)
				}
				var wg sync.WaitGroup
				wg.Add(numWorkers)
				chunkSize := (len(missingAddrs) + numWorkers - 1) / numWorkers

				for w := 0; w < numWorkers; w++ {
					start := w * chunkSize
					end := start + chunkSize
					if end > len(missingAddrs) {
						end = len(missingAddrs)
					}
					go func(s, e int) {
						defer wg.Done()
						localNonces := make(map[common.Address]uint64, e-s)
						for i := s; i < e; i++ {
							addr := missingAddrs[i]
							as, err := vp.chainState.GetAccountStateDB().AccountStateReadOnly(addr)
							var nonce uint64
							if err == nil && as != nil {
								nonce = as.Nonce()
							} else {
								nonce = 0
							}
							localNonces[addr] = nonce
							cache.Store(addr, nonce) // Cache for future ticks
							if traceEnabled {
								transaction_pool.TraceTx("DB-FETCH", addr, nonce, fmt.Sprintf("cachePtr=%p", cache))
							}
						}
						nonceMapMutex.Lock()
						for k, v := range localNonces {
							nonceMap[k] = v
						}
						nonceMapMutex.Unlock()
					}(start, end)
				}
				wg.Wait()
			}
		}

		// Sort by FromAddress and Nonce to ensure contiguous evaluation
		SortTxsByFromAndNonce(allTxs)

		for _, tx := range allTxs {
			from := tx.FromAddress()

			expected := nonceMap[from]
			actual := tx.GetNonce()

			if actual > expected {
				// Future nonce, missing predecessors -> defer to next cycle
				// TTL Logic: Xóa nếu đã chờ quá thời gian FutureTxTimeout
				txHash := tx.Hash()
				if insertTime, exists := vp.futureTxTimeMap[txHash]; exists {
					if time.Since(insertTime) > FutureTxTimeout {
						if traceEnabled {
							transaction_pool.TraceTx("TTL-DROP", from, actual, fmt.Sprintf("expected=%d waitedSince=%s", expected, insertTime.Format("15:04:05.000000")))
						}
						delete(vp.futureTxTimeMap, txHash)
						droppedTxs = append(droppedTxs, tx)
						continue // Dropped permanently, release in-flight reservation
					}
				} else {
					vp.futureTxTimeMap[txHash] = time.Now()
				}

				if traceEnabled {
					transaction_pool.TraceTx("CLASSIFY-FUTURE", from, actual, fmt.Sprintf("expected=%d", expected))
				}
				futureTxs = append(futureTxs, tx)
			} else if actual == expected {
				// Valid contiguous nonce
				if traceEnabled {
					transaction_pool.TraceTx("CLASSIFY-VALID", from, actual, "")
				}
				validTxs = append(validTxs, tx)
				nonceMap[from]++
				if len(vp.futureTxTimeMap) > 0 {
					delete(vp.futureTxTimeMap, tx.Hash())
				}
			} else {
				// Past nonce (actual < expected) -> drop permanently
				if traceEnabled {
					transaction_pool.TraceTx("CLASSIFY-PAST-DROP", from, actual, fmt.Sprintf("expected=%d", expected))
				}
				if len(vp.futureTxTimeMap) > 0 {
					delete(vp.futureTxTimeMap, tx.Hash())
				}
				droppedTxs = append(droppedTxs, tx)
			}
		}

		if len(droppedTxs) > 0 {
			vp.transactionPool.ReleaseInFlightWithDrainID(droppedTxs, drainID)
		}

		// NOTE (investigated at length 2026-09-02, measuring sustained
		// real-transfer throughput after adding TransactionsWithAggSign's
		// maxDrain cap): nonceMap[from]++ above only updates this call's
		// LOCAL map; noncesCache itself is only ever written on a cache MISS
		// here, never refreshed with the progress this tick's validation
		// just made. With a large backlog now spanning multiple capped
		// ticks (previously always one massive tick, before maxDrain
		// existed), an address split across ticks can have a later tick
		// reload a stale cached nonce and temporarily treat its own already-
		// valid-order transactions as "future" until a subsequent DB read
		// (itself refreshed on every commit via ClearNoncesCache) catches
		// up -- self-correcting via the future-tx requeue path, at the cost
		// of a delay, not a permanent loss.
		//
		// A same-day fix was attempted here: sync nonceMap's final values
		// back into noncesCache after every validating tick, so a split
		// address's later tick sees its own prior progress immediately
		// instead of waiting for a commit. It measurably helped at first,
		// but was reverted after finding a WORSE bug it introduced: this
		// function's nonceMap walks every transaction in validTxs before
		// returning, but the caller (TxBatchForwarder.StartForwardingLoop)
		// can subsequently truncate validTxs to targetBlockSize and re-queue
		// the excess back into the pool via AddTransactions -- transactions
		// this function had already counted as "done" for cache-advancement
		// purposes. Traced live via METANODE_TX_TRACE: nonces correctly
		// classified CLASSIFY-VALID, immediately re-inserted via ADD-BATCH
		// (the truncation requeue) moments later, then permanently rejected
		// as CLASSIFY-PAST-DROP the next time they were drained, because the
		// cache/floor had already been advanced past them by this function
		// on the very same tick that just put them back in the pool. A
		// following attempt to layer a never-cleared "floor" on top (only
		// ever advance, never regress) made this specific failure mode
		// worse, not better, since a floor wrongly advanced this way can
		// never self-correct via a fresh DB read the way noncesCache can.
		// Root cause of the ORIGINAL staleness this was chasing is real, and
		// any fix needs to key off what the caller actually forwards
		// (post-truncation), not what this function locally validated
		// before truncation is even decided.
		//
		// FIXED (2026-09-11), third attempt: `nonceMap` here is left exactly as
		// before (local-only, per-tick, drives nothing but THIS tick's own
		// contiguous-nonce classification) -- no cache write happens in this
		// function anymore, so the truncation-requeue failure mode above cannot
		// recur here. The cache-advance instead lives in
		// `TxValidatorPool.AdvanceNoncesCacheForForwarded`, called by
		// `TxBatchForwarder.StartForwardingLoop` per FFI batch, strictly after
		// both the truncation decision AND `executor.SubmitTransactionBatch`
		// have already succeeded for that exact batch -- i.e. only for
		// transactions with nothing left that could re-queue them. See that
		// function's doc comment for the full reasoning.

		// Re-add future transactions back to the pool with pool cap enforcement
		if len(futureTxs) > 0 {
			vp.RequeueTransactionsWithDrainID(futureTxs, drainID)
		}

		if traceEnabled {
			if f, errFile := os.OpenFile("/tmp/vp_debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); errFile == nil {
				elapsedStr := time.Since(startProcess).String()
				f.WriteString(fmt.Sprintf("[%s] ProcessPoolSub elapsed: %s, allTxs=%d, validTxs=%d, futureTxs=%d\n", time.Now().Format("15:04:05.000"), elapsedStr, len(allTxs), len(validTxs), len(futureTxs)))
				if len(allTxs) > 0 {
					expectedNonce := uint64(0)
					if nonceMap[allTxs[0].FromAddress()] > 0 {
						expectedNonce = nonceMap[allTxs[0].FromAddress()] - 1
					}
					f.WriteString(fmt.Sprintf("  Sample Tx: actualNonce=%d, expectedNonce=%d, from=%s\n", allTxs[0].GetNonce(), expectedNonce, allTxs[0].FromAddress().String()))
				}
				f.Close()
			}
		}

		logger.Info("🔍 [DEBUG] ProcessPoolSub: allTxs=%d, validTxs=%d, futureTxs=%d", len(allTxs), len(validTxs), len(futureTxs))

		txs = validTxs
	}
	return txs, drainID
}

// SortTxsByFromAndNonce sorts transactions by FromAddress and then Nonce to ensure contiguous evaluation.
// Sort keys are extracted once per tx: calling FromAddress() inside the comparator re-decodes the address
// bytes on every comparison (~180ms for 40k txs). Ordering is identical (address, then nonce).
func SortTxsByFromAndNonce(allTxs []types.Transaction) {
	type sortItem struct {
		from  common.Address
		nonce uint64
		tx    types.Transaction
	}
	sortItems := make([]sortItem, len(allTxs))
	for i, tx := range allTxs {
		sortItems[i] = sortItem{from: tx.FromAddress(), nonce: tx.GetNonce(), tx: tx}
	}
	sort.Slice(sortItems, func(i, j int) bool {
		cmp := sortItems[i].from.Cmp(sortItems[j].from)
		if cmp != 0 {
			return cmp < 0
		}
		return sortItems[i].nonce < sortItems[j].nonce
	})
	for i := range sortItems {
		allTxs[i] = sortItems[i].tx
	}
}

