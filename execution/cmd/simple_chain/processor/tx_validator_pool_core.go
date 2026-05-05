// @title processor/transaction_processor_pool.go
// @markdown processor/transaction_processor_pool.go - Transaction pool processing, grouping, and batch operations
package processor

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
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
	pendingTxManager  *PendingTransactionManager
	excludedItems     []grouptxns.Item
}

func NewTxValidatorPool(
	env ITransactionProcessorEnvironment,
	offChainProcessor tx_processor.OffChainProcessor,
	chainState *blockchain.ChainState,
	storageManager *storage.StorageManager,
	eventSystem *mt_filters.EventSystem,
	transactionPool *transaction_pool.TransactionPool,
	pendingTxManager *PendingTransactionManager,
) *TxValidatorPool {
	return &TxValidatorPool{
		env:               env,
		offChainProcessor: offChainProcessor,
		chainState:        chainState,
		storageManager:    storageManager,
		eventSystem:       eventSystem,
		transactionPool:   transactionPool,
		pendingTxManager:  pendingTxManager,
		excludedItems:     make([]grouptxns.Item, 0),
	}
}

// SetEnvironment updates the environment reference
func (vp *TxValidatorPool) SetEnvironment(env ITransactionProcessorEnvironment) {
	vp.env = env
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
	_, _ = vp.chainState.GetAccountStateDB().AccountStateReadOnly(tx.FromAddress())
	if !tx.IsDeployContract() {
		_, _ = vp.chainState.GetAccountStateDB().AccountStateReadOnly(tx.ToAddress())
	}

	if !skipVerification {
		if err := tx_processor.VerifyTransaction(tx, vp.chainState); err != nil {
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
	if len(txs) == 0 {
		return nil
	}

	if storage.GetLastBlockNumberFromMaster() > storage.GetLastBlockNumber()+3 {
		err := fmt.Errorf(transaction.NodeSyncingError.Description)
		errs := make([]error, len(txs))
		for i := range errs {
			errs[i] = err
		}
		return errs
	}

	var validTxs []types.Transaction
	var errorsList = make([]error, len(txs))

	// Phase 1.5 (TPS Optimization): Batch Cache Warming
	// Collect unique addresses to fetch in parallel without blocking muTrie.Lock
	preloadSet := make(map[common.Address]struct{}, len(txs)*2)
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		tx.AddRelatedAddress(tx.FromAddress())
		tx.AddRelatedAddress(tx.ToAddress())
		preloadSet[tx.FromAddress()] = struct{}{}
		if !tx.IsDeployContract() {
			preloadSet[tx.ToAddress()] = struct{}{}
		}
	}
	if len(preloadSet) > 0 {
		preloadAddrs := make([]common.Address, 0, len(preloadSet))
		for addr := range preloadSet {
			preloadAddrs = append(preloadAddrs, addr)
		}
		vp.chainState.GetAccountStateDB().PreloadAccounts(preloadAddrs)
	}

	// Phase 1.6 (TPS Optimization): Pre-compute CrossChainHandler ONCE per batch
	// Previously, GetCrossChainHandler() was called inside VerifyTransaction for EVERY TX.
	// For 500 cross-chain TXs, that's 500 redundant function calls with potential lock contention.
	var batchCCHandler *cross_chain_handler.CrossChainHandler
	if ccH, ccErr := cross_chain_handler.GetCrossChainHandler(); ccErr == nil && ccH != nil {
		batchCCHandler = ccH
	}

	// Phase 1.7 (TPS Optimization): Pre-load AccountStates into local slice
	// PreloadAccounts above warmed the loadedAccounts sync.Map cache.
	// Now we read states into a plain []AccountState slice — subsequent parallel
	// verification uses slice indexing (zero contention) instead of sync.Map lookups.
	preloadedStates := make([]types.AccountState, len(txs))
	preloadedCCFlags := make([]bool, len(txs))
	for i, tx := range txs {
		if tx == nil {
			continue
		}
		as, asErr := vp.chainState.GetAccountStateDB().AccountStateReadOnly(tx.FromAddress())
		if asErr != nil || as == nil {
			// Fallback: fresh account state (VerifyTransactionWithState will handle validation)
			as = state.NewAccountState(tx.FromAddress())
		}
		preloadedStates[i] = as

		// Pre-compute isCrossChainBatchSubmit per TX using the batch-cached handler
		if tx.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS && batchCCHandler != nil {
			preloadedCCFlags[i] = batchCCHandler.IsBatchSubmitTx(tx.CallData().Input())
		}
	}

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
					if txs[i] == nil || preloadedStates[i] == nil {
						continue
					}
					if err := tx_processor.VerifyTransactionWithState(txs[i], vp.chainState, preloadedStates[i], preloadedCCFlags[i]); err != nil {
						errorsList[i] = fmt.Errorf("[code:%d] %s", err.Code, err.Description)
					}
				}
			}(start, end)
		}
		wg.Wait()
	}

	for i, tx := range txs {
		if tx == nil {
			errorsList[i] = fmt.Errorf("tx nil")
			continue
		}

		if errorsList[i] != nil {
			continue // Failed in parallel verification Phase
		}

		if tx.ToAddress() == file_handler.PredictContractAddress(common.HexToAddress(vp.chainState.GetConfig().OwnerFileStorageAddress)) {
			// File uploads not supported in batch optimized path for simplicity; log error
			logger.Error("HandleFileTransactionNoReceipt not supported in AddTransactionsToPool yet")
			errorsList[i] = fmt.Errorf(transaction.UploadChunkError.Description)
			continue
		}

		conflict := vp.pendingTxManager.HasNonceConflict(tx)
		if conflict {
			errorsList[i] = fmt.Errorf(transaction.NonceConflictError.Description)
			continue
		}

		validTxs = append(validTxs, tx)
	}

	if len(validTxs) > 0 {
		// Bulk insert to pool (acquires lock ONCE)
		vp.transactionPool.AddTransactions(validTxs)

		// Bulk insert to pending tracking manager (avoids redundant map ops overhead context switching)
		vp.pendingTxManager.AddBatch(validTxs, StatusInPool)

		GlobalPipelineStats.IncrTxsReceived(int64(len(validTxs)))
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

// ProcessTransactions processes a batch of transactions through grouping and execution.
// blockTime is the deterministic timestamp (in seconds) from Rust consensus for EVM block.timestamp.
func (vp *TxValidatorPool) ProcessTransactions(txs []types.Transaction, blockTime uint64, externalPreload <-chan struct{}) (
	tx_processor.ProcessResult,
	error,
) {

	if len(txs) > 0 {
		storage.SetCommitLock(true)
	}

	var processedTxs []types.Transaction
	processedTxs = append(processedTxs, txs...)
	ev := mt_filters.NewTxsEvent{
		Txs: processedTxs,
	}
	vp.eventSystem.TxsFeed.Send(ev)

	// --- AUTO-FLUSH LOGIC (OOM Prevention) ---
	// Increment the global TX counter and flush if threshold reached.
	// PERF OPT: Flush runs ASYNC in background goroutine to avoid stalling
	// the block processing hot path (~200-500ms per flush).
	// CAS guard prevents concurrent flushes from racing.
	currentCount := atomic.AddUint64(&tx_processor.GlobalTxProcessCounter, uint64(len(txs)))
	if currentCount > tx_processor.FlushThresholdTxs {
		// CAS: only one goroutine triggers the flush (prevent concurrent flushes)
		if atomic.CompareAndSwapUint64(&tx_processor.GlobalTxProcessCounter, currentCount, 0) {
			sm := vp.chainState.GetStorageManager()
			if sm != nil {
				go func(count uint64) {
					startFlush := time.Now()
					logger.Warn("🧹 [AUTO-FLUSH] Reached %d TXs (threshold %d). Flushing LazyPebbleDB to disk async...", count, tx_processor.FlushThresholdTxs)
					err := sm.FlushAll()
					if err != nil {
						logger.Error("❌ [AUTO-FLUSH] Failed to flush storage: %v", err)
					} else {
						logger.Warn("✅ [AUTO-FLUSH] Successfully flushed %d TXs to disk in %v (async)", count, time.Since(startFlush))
					}
				}(currentCount)
			}
		}
	}

	// CRITICAL FORK-SAFETY: Clear excludedItems before processing Rust committed blocks.
	if len(vp.excludedItems) > 0 {
		logger.Info("🔒 [FORK-SAFETY] Clearing %d excludedItems before processing Rust committed block", len(vp.excludedItems))
		vp.excludedItems = nil
	}

	// CRITICAL FORK-SAFETY: Ensure RelatedAddresses are populated for all TXs.
	// TXs committed via Rust consensus arrive WITHOUT RelatedAddresses because Rust
	// does not track/forward this field. Without addresses, the EVM's isAddressAllowed
	// check will fail for native TXs (BLS registration) causing receipt status=0x0
	// on some nodes but 0x1 on others → receiptsRoot divergence → FORK.
	for _, tx := range txs {
		tx.AddRelatedAddress(tx.FromAddress())
		tx.AddRelatedAddress(tx.ToAddress())
	}

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
	// Native contract addresses that are shared "dispatch" points, NOT shared state.
	// TXs to these addresses only modify the SENDER's own account (BLS key, account type,
	// staking), so they can safely run in parallel across different senders.
	// Excluding them from grouping Array prevents Union-Find from merging all native TXs
	// into a single group.
	accountSettingAddr := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	nativeParallelAddrs := map[common.Address]struct{}{
		accountSettingAddr:                   {},
		mt_common.VALIDATOR_CONTRACT_ADDRESS: {},
	}
	for i, tx := range txs {
		// Build grouping addresses: filter out native dispatch addresses
		groupAddrs := make([]common.Address, 0, len(tx.RelatedAddresses()))
		for _, addr := range tx.RelatedAddresses() {
			if _, isNative := nativeParallelAddrs[addr]; !isNative {
				groupAddrs = append(groupAddrs, addr)
			}
		}
		// Safety: always have at least FromAddress
		if len(groupAddrs) == 0 {
			groupAddrs = append(groupAddrs, tx.FromAddress())
		}
		items = append(items, grouptxns.Item{
			ID:      i,
			Array:   groupAddrs,
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

	startExecution := time.Now()
	res, execErr := tx_processor.ProcessTransactions(baseCtx, vp.chainState, groupedGroups, enableTrace, true, blockTime)
	execDuration := time.Since(startExecution)

	if execDuration.Milliseconds() > 100 {
		logger.Info("⏱️  [PERF] tx_processor.ProcessTransactions (EVM/State) of %d TXs took %v", len(txs), execDuration)
	}

	return res, execErr
}

// ProcessTransactionsInPool retrieves transactions from the pool and processes them
func (vp *TxValidatorPool) ProcessTransactionsInPool(setEmptyBlock bool) (
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
			Array:     tx.RelatedAddresses(),
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
	return tx_processor.ProcessTransactions(baseCtx, vp.chainState, groupedGroups, enableTrace, true, uint64(time.Now().Unix()))
}

// ProcessTransactionsInPoolSub retrieves transactions from pool for sub-node forwarding
func (vp *TxValidatorPool) ProcessTransactionsInPoolSub(setEmptyBlock bool) []types.Transaction {
	var txs []types.Transaction
	if setEmptyBlock {
		txs = make([]types.Transaction, 0)
	} else {
		txs, _ = vp.transactionPool.TransactionsWithAggSign()
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
			Array:     tx.RelatedAddresses(),
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
