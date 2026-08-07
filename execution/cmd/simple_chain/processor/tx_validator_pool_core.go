// @title processor/transaction_processor_pool.go
// @markdown processor/transaction_processor_pool.go - Transaction pool processing, grouping, and batch operations
package processor

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
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
func (vp *TxValidatorPool) ClearNoncesCache() {
	vp.noncesCache.Store(&sync.Map{})
	logger.Debug("🧹 [POOL] Expected nonces cache cleared (block committed/reverted)")
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

// AddVerifiedTransactionsToPool adds a batch of pre-verified transactions to the pool
func (vp *TxValidatorPool) AddVerifiedTransactionsToPool(txs []types.Transaction) []error {
	return vp.addTransactionsToPoolInternal(txs, true)
}

// addTransactionToPoolInternal handles the core logic with an option to skip expensive verification
func (vp *TxValidatorPool) addTransactionToPoolInternal(tx types.Transaction, skipVerification bool) (int64, error) {

	if tx == nil {
		return transaction.InvalidTransaction.Code, fmt.Errorf("tx nil")
	}

	minGasPrice := vp.chainState.GetConfig().MinGasPrice
	if minGasPrice > 0 && tx.MaxGasPrice() < minGasPrice {
		return transaction.InvalidTransaction.Code, fmt.Errorf("transaction gas price (%d) is below node minimum (%d)", tx.MaxGasPrice(), minGasPrice)
	}

	// Limit pool size to prevent GC stall / OOM
	if vp.transactionPool.CountTransactions() >= MaxMempoolSize {
		logger.Warn("⚠️ Mempool is full (limit=%d). Evicting 100 lowest-fee transactions to make room for new txs.", MaxMempoolSize)
		evicted := vp.transactionPool.EvictLowestGasPrice(100)
		if evicted == 0 {
			return transaction.AddToPoolError.Code, fmt.Errorf("transaction pool is full (limit=%d) and could not evict", MaxMempoolSize)
		}
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

	if !skipVerification {
		if err := tx_processor.VerifyTransaction(tx, vp.chainState, as); err != nil {
			logger.Error("Transaction verification failed: %v", err)
			return transaction.VerifyTransactionError.Code, fmt.Errorf(err.Description)
		}
	}

	err := vp.transactionPool.AddTransaction(tx)
	if err != nil {
		logger.Error("❌ [TX FLOW] Failed to add transaction to pool: %v", err)
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
	// Disabled vp_debug.log writing in hot-path for performance

	if len(txs) == 0 {
		return nil
	}

	// Limit pool size to prevent GC stall / OOM
	if vp.transactionPool.CountTransactions()+len(txs) >= MaxMempoolSize {
		evictCount := len(txs)
		if evictCount < 1000 {
			evictCount = 1000
		}
		logger.Warn("⚠️ Mempool is near full (limit=%d). Evicting %d lowest-fee transactions to make room for batch.", MaxMempoolSize, evictCount)
		vp.transactionPool.EvictLowestGasPrice(evictCount)
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

	// Phase 1.5 (TPS Optimization): Batch Cache Warming
	// Collect unique addresses to fetch in parallel without blocking muTrie.Lock
	preloadSet := make(map[common.Address]struct{}, len(txs)*2)
	for _, tx := range txs {
		if tx == nil {
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
					if txs[i] == nil {
						continue
					}
					if minGasPrice > 0 && txs[i].MaxGasPrice() < minGasPrice {
						errorsList[i] = fmt.Errorf("[code:%d] transaction gas price (%d) is below node minimum (%d)", transaction.InvalidTransaction.Code, txs[i].MaxGasPrice(), minGasPrice)
						continue
					}
					var senderState types.AccountState
					if senderStates != nil {
						senderState = senderStates[txs[i].FromAddress()]
					}
					if err := tx_processor.VerifyTransaction(txs[i], vp.chainState, senderState); err != nil {
						errorsList[i] = fmt.Errorf("[code:%d] %s", err.Code, err.Description)
					}
				}
			}(start, end)
		}
		wg.Wait()
	}
	verificationDuration := time.Since(t2)

	t3 := time.Now()
	var logFile *os.File
	// Phase 3 (TPS Optimization): Parallel HasNonceConflict Checks
	// GOMAXPROCS(0), not NumCPU(): see native_fast_path.go for why.
	numWorkersConflict := runtime.GOMAXPROCS(0) / 2
	if numWorkersConflict < 4 {
		numWorkersConflict = 4
	}
	if numWorkersConflict > 48 {
		numWorkersConflict = 48
	}
	if len(txs) < numWorkersConflict {
		numWorkersConflict = len(txs)
	}

	var wgConflict sync.WaitGroup
	wgConflict.Add(numWorkersConflict)
	chunkSizeConflict := (len(txs) + numWorkersConflict - 1) / numWorkersConflict

	for w := 0; w < numWorkersConflict; w++ {
		start := w * chunkSizeConflict
		end := start + chunkSizeConflict
		if end > len(txs) {
			end = len(txs)
		}
		go func(s, e int) {
			defer wgConflict.Done()
			for i := s; i < e; i++ {
				tx := txs[i]
				if tx == nil {
					errorsList[i] = fmt.Errorf("tx nil")
					continue
				}
				if errorsList[i] != nil {
					continue
				}
			}
		}(start, end)
	}
	wgConflict.Wait()

	// Sequentially gather the valid transactions (extremely fast, zero contention)
	for i, tx := range txs {
		if tx != nil && errorsList[i] == nil {
			validTxs = append(validTxs, tx)
		}
	}
	conflictChecksDuration := time.Since(t3)

	t4 := time.Now()
	if len(validTxs) > 0 {
		if logFile != nil {
			logFile.WriteString(fmt.Sprintf("TxValidatorPool.addTransactionsToPoolInternal: vp.transactionPool.AddTransactions called with %d txs\n", len(validTxs)))
		}

		// Bulk insert to pool (acquires lock ONCE)
		vp.transactionPool.AddTransactions(validTxs)

		GlobalPipelineStats.IncrTxsReceived(int64(len(validTxs)))
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
					doneFlush := make(chan struct{})
					go func() {
						timer := time.NewTimer(5 * time.Second)
						defer timer.Stop()
						for {
							select {
							case <-timer.C:
								logger.Warn("🆘 [WATCHDOG-FLUSH] Tiến trình dọn RAM (FlushAll) đang BỊ ĐỨNG! Thời gian trôi qua: %v", time.Since(startFlush))
								timer.Reset(5 * time.Second)
							case <-doneFlush:
								return
							}
						}
					}()

					if size > 0 {
						logger.Warn("🧹 [AUTO-FLUSH] PebbleDB MemTableSize %d MB reached / %d TXs. Flushing LazyPebbleDB to disk async...", size/1024/1024, count)
					} else {
						logger.Warn("🧹 [AUTO-FLUSH] Reached %d TXs (threshold %d). Flushing LazyPebbleDB to disk async...", count, tx_processor.FlushThresholdTxs)
					}
					err := sm.FlushAll()
					close(doneFlush) // Tắt watchdog khi xong

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

	groupedGroups := grouptxns.GroupTransactionsDeterministic(items)

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
	doneExec := make(chan struct{})
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				logger.Warn("🆘 [WATCHDOG-BLOCK] Block #%d (chứa %d TXs) ĐANG BỊ KẸT ở ProcessTransactions! Thời gian trôi qua: %v", blockNum, len(txs), time.Since(startExecution))
				timer.Reset(5 * time.Second)
			case <-doneExec:
				return
			}
		}
	}()

	res, execErr := tx_processor.ProcessTransactions(baseCtx, vp.chainState, groupedGroups, enableTrace, true, blockTime, leaderAddr, blockNum, false)
	close(doneExec) // Tắt watchdog khi xong
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

// ProcessTransactionsInPoolSub retrieves transactions from pool for sub-node forwarding
func (vp *TxValidatorPool) ProcessTransactionsInPoolSub(setEmptyBlock bool) []types.Transaction {
	var txs []types.Transaction
	if setEmptyBlock {
		txs = make([]types.Transaction, 0)
	} else {
		allTxs, _ := vp.transactionPool.TransactionsWithAggSign()

		if len(allTxs) == 0 {
			return allTxs
		}

		startProcess := time.Now()

		var validTxs []types.Transaction
		var futureTxs []types.Transaction
		nonceMap := make(map[common.Address]uint64)

		// Preload accounts in batch to warm cache and avoid synchronous DB I/O inside loop
		preloadSet := make(map[common.Address]struct{}, len(allTxs))
		for _, tx := range allTxs {
			preloadSet[tx.FromAddress()] = struct{}{}
		}
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

			// First, resolve nonces using the cache
			var missingAddrs []common.Address
			for _, addr := range preloadAddrs {
				if val, ok := cache.Load(addr); ok {
					nonceMap[addr] = val.(uint64)
				} else {
					missingAddrs = append(missingAddrs, addr)
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
		sort.Slice(allTxs, func(i, j int) bool {
			cmp := allTxs[i].FromAddress().Cmp(allTxs[j].FromAddress())
			if cmp != 0 {
				return cmp < 0
			}
			return allTxs[i].GetNonce() < allTxs[j].GetNonce()
		})

		for _, tx := range allTxs {
			from := tx.FromAddress()

			expected := nonceMap[from]
			actual := tx.GetNonce()

			if actual > expected {
				// Future nonce, missing predecessors -> defer to next cycle
				// logger.Info("⏳ [TX POOL] Chờ nonce (Future TX): hash=%s, from=%s, actualNonce=%d, expectedNonce=%d", tx.Hash().Hex(), from.Hex(), actual, expected)
				// TTL Logic: Xóa nếu đã chờ quá thời gian FutureTxTimeout
				if insertTime, exists := vp.futureTxTimeMap[tx.Hash()]; exists {
					if time.Since(insertTime) > FutureTxTimeout {
						// logger.Info("🗑️ [TX POOL] Xóa giao dịch rác (quá timeout): hash=%s", tx.Hash().Hex())
						delete(vp.futureTxTimeMap, tx.Hash())
						continue // KHÔNG append vào futureTxs nữa -> Bị drop vĩnh viễn
					}
				} else {
					vp.futureTxTimeMap[tx.Hash()] = time.Now()
				}

				futureTxs = append(futureTxs, tx)
			} else if actual == expected {
				// Valid contiguous nonce
				// logger.Info("✅ [TX POOL] Chấp nhận tx: hash=%s, from=%s, nonce=%d", tx.Hash().Hex(), from.Hex(), actual)
				validTxs = append(validTxs, tx)
				nonceMap[from]++
				delete(vp.futureTxTimeMap, tx.Hash()) // Dọn dẹp map
			} else {
				// Past nonce (actual < expected) -> drop permanently
				// logger.Info("❌ [TX POOL] Bỏ qua tx (Past nonce): hash=%s, from=%s, actualNonce=%d, expectedNonce=%d", tx.Hash().Hex(), from.Hex(), actual, expected)
				delete(vp.futureTxTimeMap, tx.Hash()) // Dọn dẹp map
			}
		}

		// Re-add future transactions back to the pool
		if len(futureTxs) > 0 {
			vp.transactionPool.AddTransactions(futureTxs)
		}

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

		logger.Info("🔍 [DEBUG] ProcessPoolSub: allTxs=%d, validTxs=%d, futureTxs=%d", len(allTxs), len(validTxs), len(futureTxs))

		txs = validTxs
	}
	return txs
}
