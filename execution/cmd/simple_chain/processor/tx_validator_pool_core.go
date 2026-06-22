// @title processor/transaction_processor_pool.go
// @markdown processor/transaction_processor_pool.go - Transaction pool processing, grouping, and batch operations
package processor

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor/pipeline"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
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
	pendingTxManager  *PendingTransactionManager
	excludedItems     []grouptxns.Item

	futureTxTimeMap map[common.Hash]time.Time

	// FORK-SAFETY: Shared lock — Lock() during real block execution blocks
	// all virtual execution goroutines that hold RLock().
	blockProcessingLock *sync.RWMutex
}

func NewTxValidatorPool(
	env ITransactionProcessorEnvironment,
	offChainProcessor tx_processor.OffChainProcessor,
	chainState *blockchain.ChainState,
	storageManager *storage.StorageManager,
	eventSystem *mt_filters.EventSystem,
	transactionPool *transaction_pool.TransactionPool,
	pendingTxManager *PendingTransactionManager,
	blockProcessingLock *sync.RWMutex,
) *TxValidatorPool {
	vp := &TxValidatorPool{
		env:                 env,
		offChainProcessor:   offChainProcessor,
		chainState:          chainState,
		storageManager:      storageManager,
		eventSystem:         eventSystem,
		transactionPool:     transactionPool,
		pendingTxManager:    pendingTxManager,
		excludedItems:       make([]grouptxns.Item, 0),
		futureTxTimeMap:     make(map[common.Hash]time.Time),
		blockProcessingLock: blockProcessingLock,
	}

	vp.StartMemoryMonitor()
	return vp
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

	// FORK-SAFETY: Ensure RelatedAddresses are ALWAYS populated before verification.
	// Native TXs (e.g. BLS registration) need FromAddress and ToAddress in RelatedAddresses
	// so the EVM's isAddressAllowed check passes during ProcessNonceOnly.
	// This MUST be done centrally here — not per entry path — because P2P-forwarded TXs
	// (ProcessTransactionsFromSub) call AddTransactionToPool directly.
	tx.AddRelatedAddress(tx.FromAddress())
	tx.AddRelatedAddress(tx.ToAddress())

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

	// upload file
	if tx.ToAddress() == file_handler.PredictContractAddress(common.HexToAddress(vp.chainState.GetConfig().OwnerFileStorageAddress)) {
		fileHandler, err := file_handler.GetFileHandlerOnChain(vp.offChainProcessor, vp.storageManager, vp.chainState)
		if err != nil {
			logger.Error("GetFileHandler error: %v", err)
			return transaction.UploadChunkError.Code, fmt.Errorf(transaction.UploadChunkError.Description)
		}
		isPrevent, err := fileHandler.HandleFileTransactionNoReceipt(context.Background(), tx)
		if err != nil {
			logger.Error("HandleFileTransactionNoReceipt error: %v", err)
			return transaction.UploadChunkError.Code, fmt.Errorf(err.Error())
		}
		if isPrevent {
			rcp := receipt.NewReceipt(
				tx.Hash(),
				tx.FromAddress(),
				tx.ToAddress(),
				big.NewInt(0),
				pb.RECEIPT_STATUS_RETURNED, // trạng thái tạm thời: returned (thay đổi nếu cần)
				[]byte{},                   // return data empty
				pb.EXCEPTION_NONE,          // no exception
				mt_common.MINIMUM_BASE_FEE,
				uint64(0),          // gas used
				[]types.EventLog{}, // event logs empty
				uint64(0),
				common.Hash{},
				0,
			)
			rcp.SetRHash(tx.RHash())

			if vp.env != nil {
				vp.env.BroadCastReceipts([]types.Receipt{rcp})
			}
			return 0, nil
		}
	}
	// Sử dụng pendingTxManager đã được tối ưu
	conflict := vp.pendingTxManager.HasNonceConflict(tx)
	if conflict {
		logger.Error("❌ [TX FLOW] Transaction conflict: nonce conflict detected for address %s: txHash: %s", tx.FromAddress().Hex(), tx.Hash().Hex())
		return transaction.NonceConflictError.Code, fmt.Errorf(transaction.NonceConflictError.Description)
	}

	err := vp.transactionPool.AddTransaction(tx)
	if err != nil {
		logger.Error("❌ [TX FLOW] Failed to add transaction to pool: %v", err)
		return transaction.AddToPoolError.Code, fmt.Errorf("failed to add transaction %s to pool: %w", tx.Hash().Hex(), err)
	}

	// *** THAY ĐỔI: Thêm vào pending manager với trạng thái InPool ***
	vp.pendingTxManager.Add(tx, StatusInPool)
	// ***************************************************************

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
	preloadSet := make(map[common.Address]struct{}, len(txs))
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		tx.AddRelatedAddress(tx.FromAddress())
		tx.AddRelatedAddress(tx.ToAddress())
		preloadSet[tx.FromAddress()] = struct{}{}
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
	for _, addr := range preloadAddrs {
		if as, err := vp.chainState.GetAccountStateDB().AccountStateReadOnly(addr); err == nil && as != nil {
			senderStates[addr] = as
		}
	}
	statePreloadDuration := time.Since(t1)

	t2 := time.Now()
	// Phase 2 (TPS Optimization): Parallel VerifyTransactionWithState
	// BLS verification is CPU heavy (~1ms per tx). 2000 txs = 2 seconds if sequential.
	// PERF: Cap workers at numCPU/2 (max 48) to reduce sync.Map contention on
	// verifiedSignaturesCache. 104 goroutines cause excessive cache-line bouncing.
	if !skipVerification {
		numWorkers := runtime.NumCPU() / 2
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
	numWorkersConflict := runtime.NumCPU() / 2
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

				if tx.ToAddress() == file_handler.PredictContractAddress(common.HexToAddress(vp.chainState.GetConfig().OwnerFileStorageAddress)) {
					errorsList[i] = fmt.Errorf(transaction.UploadChunkError.Description)
					continue
				}

				if vp.pendingTxManager.HasNonceConflict(tx) {
					errorsList[i] = fmt.Errorf(transaction.NonceConflictError.Description)
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

		// Bulk insert to pending tracking manager (avoids redundant map ops overhead context switching)
		vp.pendingTxManager.AddBatch(validTxs, StatusInPool)

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

	var totalWaitGoUs int64
	var totalWaitRustUs int64
	var waitCount int64

	now := time.Now()
	if vp.env != nil {
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
			}
		}
	}

	avgWaitGoUs := int64(0)
	avgWaitRustUs := int64(0)
	if waitCount > 0 {
		avgWaitGoUs = totalWaitGoUs / waitCount
		avgWaitRustUs = totalWaitRustUs / waitCount
	}
	pipeline.GlobalBlockTraceStore.SetWaitTime(blockNum, avgWaitGoUs, avgWaitRustUs)

	var processedTxs []types.Transaction
	processedTxs = append(processedTxs, txs...)
	ev := mt_filters.NewTxsEvent{
		Txs: processedTxs,
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
					if size > 0 {
						logger.Warn("🧹 [AUTO-FLUSH] PebbleDB MemTableSize %d MB reached / %d TXs. Flushing LazyPebbleDB to disk async...", size/1024/1024, count)
					} else {
						logger.Warn("🧹 [AUTO-FLUSH] Reached %d TXs (threshold %d). Flushing LazyPebbleDB to disk async...", count, tx_processor.FlushThresholdTxs)
					}
					err := sm.FlushAll()
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
	for _, tx := range txs {
		tx.AddRelatedAddress(tx.FromAddress())
		tx.AddRelatedAddress(tx.ToAddress())
	}
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
	for i, tx := range txs {
		items = append(items, grouptxns.Item{
			ID:      i,
			Array:   grouptxns.BuildDeterministicGroupAddrs(tx),
			GroupID: 0,
			Tx:      tx,
		})
	}

	groupedGroups := grouptxns.GroupTransactionsDeterministic(items)

	logger.Info("🔒 [FORK-SAFETY] Deterministic grouping: %d TXs → %d parallel groups (bypassed GroupAndLimit)", len(txs), len(groupedGroups))

	// Wait for preload to finish before proceeding to execution
	<-preloadDone

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
	res, execErr := tx_processor.ProcessTransactions(baseCtx, vp.chainState, groupedGroups, enableTrace, true, blockTime, leaderAddr, blockNum)
	vp.blockProcessingLock.Unlock()
	execDuration := time.Since(startExecution)

	if len(txs) > 0 {
		logger.Warn("⏱️  [BLOCK-PERF] Block #%d: TXs=%d | VirtualExec=%v | Consensus=%v | LockWait=%v | RealExec=%v",
			blockNum, len(txs), virtualDuration.Round(time.Microsecond), consensusDuration.Round(time.Millisecond), lockWaitDuration.Round(time.Millisecond), execDuration.Round(time.Millisecond))
			
		pipeline.GlobalBlockTraceStore.AddConsensusAndExecTime(blockNum, len(txs), consensusDuration.Microseconds(), execDuration.Microseconds())
	}

	if execDuration.Microseconds() > 100 {
		logger.Info("⏱️  [PERF] tx_processor.ProcessTransactions (EVM/State) of %d TXs took %v (WaitLock: %v)", len(txs), execDuration, lockWaitDuration)
	}

	return res, execErr
}

// ProcessTransactionsInPool retrieves transactions from the pool and processes them
func (vp *TxValidatorPool) ProcessTransactionsInPool(setEmptyBlock bool, blockTime uint64, leaderAddr common.Address, blockNum uint64) (
	tx_processor.ProcessResult,
	error,
) {
	var txs []types.Transaction
	if setEmptyBlock {
		txs = make([]types.Transaction, 0)
	} else {
		txs, _ = vp.transactionPool.TransactionsWithAggSign()
	}

	if len(txs) > 0 {
		// *** THAY ĐỔI: Cập nhật trạng thái thành Processing ***
		for _, tx := range txs {
			vp.pendingTxManager.UpdateStatus(tx.Hash(), StatusProcessing)
		}
		// ****************************************************
		storage.SetCommitLock(true)
	}

	vp.removeOldExcludedItems()

	var processedTxs []types.Transaction
	processedTxs = append(processedTxs, txs...)
	ev := mt_filters.NewTxsEvent{
		Txs: processedTxs,
	}
	vp.eventSystem.TxsFeed.Send(ev)

	items := make([]grouptxns.Item, 0, len(txs)+len(vp.excludedItems))
	items = append(items, vp.excludedItems...)
	for i, tx := range txs {
		items = append(items, grouptxns.Item{
			ID:        i + len(vp.excludedItems),
			Array:     grouptxns.BuildDeterministicGroupAddrs(tx),
			GroupID:   0,
			Tx:        tx,
			TimeStart: time.Now(),
		})
	}

	groupedGroups, excludedItems, err := grouptxns.GroupAndLimitTransactionsOptimized(items, mt_common.MAX_GROUP_GAS, mt_common.MAX_TOTAL_GAS, mt_common.MAX_GROUP_TIME, mt_common.MAX_TOTAL_TIME)
	vp.AddExcludedItems(excludedItems)

	if err != nil {
		logger.Error("GroupAndLimitTransactionsOptimized failed: %v", err)
		return tx_processor.ProcessResult{}, fmt.Errorf("GroupAndLimitTransactionsOptimized failed: %w", err)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// DETERMINISTIC RE-GROUPING (Proposer Path Alignment)
	//
	// Although GroupAndLimitTransactionsOptimized selected and limited the transactions
	// to fit block size / gas constraints, we MUST execute them using the EXACT same
	// deterministic grouping algorithm (GroupTransactionsDeterministic) and native
	// address filtering as the validator / replay path.
	//
	// This ensures that:
	//   1. The execution order of transaction groups matches the replay path.
	//   2. Sequential GroupID assignment matches, yielding identical mvmId (C++ DB paths).
	//   3. Receipts' GroupIndex and TransactionIndex are stamped identically.
	//   4. The resulting stateRoot computed by the proposer matches the validator's.
	// ═══════════════════════════════════════════════════════════════════════════
	var selectedTxs []types.Transaction
	for _, group := range groupedGroups {
		for _, item := range group.Items {
			tx := item.Tx
			tx.AddRelatedAddress(tx.FromAddress())
			tx.AddRelatedAddress(tx.ToAddress())
			selectedTxs = append(selectedTxs, tx)
		}
	}

	deterministicItems := make([]grouptxns.Item, 0, len(selectedTxs))
	for i, tx := range selectedTxs {
		deterministicItems = append(deterministicItems, grouptxns.Item{
			ID:      i,
			Array:   grouptxns.BuildDeterministicGroupAddrs(tx),
			GroupID: 0,
			Tx:      tx,
		})
	}

	deterministicGroups := grouptxns.GroupTransactionsDeterministic(deterministicItems)

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
	// FORK-SAFETY: Acquire EXCLUSIVE lock during real block execution (pool path).
	vp.blockProcessingLock.Lock()
	result, err := tx_processor.ProcessTransactions(baseCtx, vp.chainState, deterministicGroups, enableTrace, true, blockTime, leaderAddr, blockNum)
	vp.blockProcessingLock.Unlock()
	return result, err
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
			for _, addr := range preloadAddrs {
				as, err := vp.chainState.GetAccountStateDB().AccountStateReadOnly(addr)
				if err == nil && as != nil {
					nonceMap[addr] = as.Nonce()
				} else {
					nonceMap[addr] = 0
				}
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
			f.WriteString(fmt.Sprintf("ProcessPoolSub: allTxs=%d, validTxs=%d, futureTxs=%d\n", len(allTxs), len(validTxs), len(futureTxs)))
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

// removeOldExcludedItems removes excluded items older than MAX_TIME_PENDING
func (vp *TxValidatorPool) removeOldExcludedItems() (grouptxns.GroupResult, []grouptxns.Item) {
	fiveMinutesAgo := time.Now().Add(-mt_common.MAX_TIME_PENDING * time.Minute)
	newExcludedItems := make([]grouptxns.Item, 0)

	gRs := grouptxns.GroupResult{
		Transactions:     []types.Transaction{},
		Receipts:         []types.Receipt{},
		ExecuteSCResults: []types.ExecuteSCResult{},
		Error:            nil,
	}

	for _, item := range vp.excludedItems {
		b, _ := transaction.TimeoutPending.Marshal()

		if item.TimeStart.After(fiveMinutesAgo) {
			tx := item.Tx
			newExcludedItems = append(newExcludedItems, item)
			rcp := receipt.NewReceipt(
				tx.Hash(),
				tx.FromAddress(),
				tx.ToAddress(),
				tx.Amount(),
				pb.RECEIPT_STATUS_TRANSACTION_ERROR,
				b,
				pb.EXCEPTION_NONE,
				uint64(0),
				uint64(0),
				[]types.EventLog{},
				uint64(0),
				common.Hash{},
				0,
			)
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
		}
	}
	vp.excludedItems = newExcludedItems
	return gRs, newExcludedItems
}

// ProcessAndPartitionTransactions groups and partitions transactions for parallel processing
func (vp *TxValidatorPool) ProcessAndPartitionTransactions(n int) ([][]grouptxns.RelativeGroup, error) {
	txs, _ := vp.transactionPool.TransactionsWithAggSign()

	if len(txs) == 0 {
		return nil, nil
	}

	items := make([]grouptxns.Item, 0, len(txs))
	for i, tx := range txs {
		items = append(items, grouptxns.Item{
			ID:        i,
			Array:     grouptxns.BuildDeterministicGroupAddrs(tx),
			GroupID:   0,
			Tx:        tx,
			TimeStart: time.Now(),
		})
	}

	relativeGroups, _, err := grouptxns.GroupAndLimitTransactionsOptimized(items, mt_common.MAX_GROUP_GAS, mt_common.MAX_TOTAL_GAS, mt_common.MAX_GROUP_TIME, mt_common.MAX_TOTAL_TIME)
	if err != nil {
		logger.Error("GroupAndLimitTransactionsOptimized failed:", err)
		return nil, fmt.Errorf("GroupAndLimitTransactionsOptimized failed: %w", err)
	}

	partitionedGroups, err := grouptxns.PartitionRelativeGroups(relativeGroups, n)
	if err != nil {
		logger.Error("PartitionRelativeGroups failed:", err)
		return nil, fmt.Errorf("PartitionRelativeGroups failed: %w", err)
	}

	return partitionedGroups, nil
}
