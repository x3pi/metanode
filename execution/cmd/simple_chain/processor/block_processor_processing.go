// @title processor/block_processor_processing.go
// @markdown processor/block_processor_processing.go - Block creation and result processing (core)
package processor

import (
	"sync"
	"time"

	"runtime"
	runtime_debug "runtime/debug"

	"github.com/ethereum/go-ethereum/common"
	"context"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/tracing"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// GenerateBlock generates blocks
func (bp *BlockProcessor) GenerateBlock() {
	currentBlockNumber := storage.GetLastBlockNumber() + 1
	var accumulatedResults *tx_processor.ProcessResult = nil
	// Use centralized constants from constants.go
	const minTxsForImmediateBlock = MinTxsForImmediateBlock
	const maxTxsInAccumulatedResults = MaxTxsInAccumulatedResults

	for {
		// T1-4: Priority-select pattern — always drain ProcessResultChan before checking timeout.
		// Go's native select has uniform random selection when multiple cases are ready.
		// Under high load, timeoutChan may fire while ProcessResultChan also has data,
		// causing premature flush with fewer TXs. This pattern drains all available
		// results first, then checks if we should flush.

		// Phase 1: Non-blocking drain of all available results
		drained := false
		for {
			select {
			case processResults := <-bp.transactionProcessor.ProcessResultChan:
				bp.inputTxCounter.Add(int64(len(processResults.Transactions)))
				if accumulatedResults == nil {
					accumulatedResults = &processResults
				} else {
					accumulatedResults.Transactions = append(accumulatedResults.Transactions, processResults.Transactions...)
					accumulatedResults.Receipts = append(accumulatedResults.Receipts, processResults.Receipts...)
					accumulatedResults.ExecuteSCResults = append(accumulatedResults.ExecuteSCResults, processResults.ExecuteSCResults...)
				}
				drained = true

				// Check max size limit to avoid memory leak
				if len(accumulatedResults.Transactions) >= maxTxsInAccumulatedResults {
					logger.Warn("GenerateBlock: accumulatedResults reached max size (%d), force flush", maxTxsInAccumulatedResults)
					bp.createBlockFromResults(*accumulatedResults, currentBlockNumber, 0, true, "single_block", 0, 0)
					accumulatedResults = nil
					currentBlockNumber++
				}
			default:
				goto FLUSH_CHECK
			}
		}

	FLUSH_CHECK:
		// Phase 2: Check if we should flush or wait
		if accumulatedResults != nil && len(accumulatedResults.Transactions) >= minTxsForImmediateBlock {
			// Enough TXs accumulated — flush immediately
			newBlock := bp.createBlockFromResults(*accumulatedResults, currentBlockNumber, 0, true, "single_block", 0, 0)
			accumulatedResults = nil
			currentBlockNumber++
			logger.Info("Created block #%d with %d txs", newBlock.Header().BlockNumber(), len(newBlock.Transactions()))
			continue
		}

		if !drained && accumulatedResults != nil && len(accumulatedResults.Transactions) > 0 {
			// No new results arrived and we have pending data — use timer to flush
			select {
			case processResults := <-bp.transactionProcessor.ProcessResultChan:
				bp.inputTxCounter.Add(int64(len(processResults.Transactions)))
				accumulatedResults.Transactions = append(accumulatedResults.Transactions, processResults.Transactions...)
				accumulatedResults.Receipts = append(accumulatedResults.Receipts, processResults.Receipts...)
				accumulatedResults.ExecuteSCResults = append(accumulatedResults.ExecuteSCResults, processResults.ExecuteSCResults...)
			case <-bp.forceCommitChan:
				// Event-driven flush — create block with whatever we have immediately
				newBlock := bp.createBlockFromResults(*accumulatedResults, currentBlockNumber, 0, true, "single_block", 0, 0)
				accumulatedResults = nil
				currentBlockNumber++
				logger.Info("Created block #%d with %d txs (event-driven flush)", newBlock.Header().BlockNumber(), len(newBlock.Transactions()))
			}
		} else if !drained {
			// No pending results and no new data — blocking wait for first result
			processResults := <-bp.transactionProcessor.ProcessResultChan
			bp.inputTxCounter.Add(int64(len(processResults.Transactions)))
			accumulatedResults = &processResults
		}
	}
}

// ProcessorPool ensures only one goroutine executes ProcessTransactionsInPool at a time.
// T2-3: Uses blocking channel send instead of spin-wait to avoid burning CPU when lock is held.
func (bp *BlockProcessor) ProcessorPool() {
	for {
		// Only check when transaction pool has data or excluded items left to avoid unnecessary loops
		if bp.transactionProcessor.transactionPool.CountTransactions() > 0 || bp.transactionProcessor.GetExcludedItemsCount() > 0 {
			// T2-3 FIX: Blocking send replaces select+default+Sleep(10µs) spin-wait.
			// When the lock is held by another goroutine, this blocks cleanly on the
			// channel send without burning CPU cycles in a tight loop.
			bp.processingLockChan <- struct{}{}

			// Acquired lock, proceed with processing
			// TPS OPTIMIZATION: CommitLock check removed — was blocking here
			// during the entire block commit cycle (71% of wall time).
			// AccountStateDB.lockedFlag provides the necessary safety.
			setEmptyBlock := false
			processResult, err := bp.transactionProcessor.ProcessTransactionsInPool(setEmptyBlock)
			if err == nil {
				bp.inputTxCounter.Add(int64(len(processResult.Transactions)))
				bp.ProcessedInputTxCount.Add(uint64(len(processResult.Transactions)))
				logger.Info("ProcessorPool processResult %v", processResult.Transactions)

				// Monitor and warn when channel is full
				select {
				case bp.transactionProcessor.ProcessResultChan <- processResult:
					// Sent successfully, no blocking
				default:
					// Channel full, sending will block. Log for monitoring.
					logger.Warn("ProcessResultChan full. Transaction processing speed higher than block creation speed. Processing stream will block.")
					bp.transactionProcessor.ProcessResultChan <- processResult // Send and wait
				}
			}
			// Release lock after processing
			<-bp.processingLockChan
		} else {
			// GO-2: Wait non-blocking for event notification instead of busy-sleep
			<-bp.transactionProcessor.transactionPool.NotifyChan
		}
	}
}

// createBlockFromResults creates a block from processing results
// CRITICAL FORK-SAFETY: commitTimestampMs should come from Rust consensus to ensure all nodes
// produce identical block hashes. Pass 0 for backward compatibility (will use time.Now()).
// CRITICAL FORK-SAFETY: leaderAddressOverride (optional, variadic) allows passing leader address
// from Rust consensus. If not provided, falls back to bp.validatorAddress (for local processing).
func (bp *BlockProcessor) createBlockFromResults(processResults tx_processor.ProcessResult, currentBlockNumber uint64, epoch uint64, isStateChanging bool, batchID string, commitTimestampMs uint64, globalExecIndex uint64, leaderAddressOverride ...common.Address) *block.Block {
	overallStart := time.Now()

	tracer := tracing.GetTracer()
	_, span := tracer.Start(context.Background(), "BlockProcessor.createBlockFromResults",
		trace.WithAttributes(
			attribute.Int64("block_number", int64(currentBlockNumber)),
			attribute.Int("txs_count", len(processResults.Transactions)),
			attribute.Int64("epoch", int64(epoch)),
		))
	defer span.End()

	// Phase 1: Calculate Roots — PARALLEL (receiptsRoot and txsRoot are independent)
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(common.Hash{}, bp.storageManager.GetStorageTransaction())
	if err != nil {
		logger.Fatal("NewTransactionStateDBFromRoot failed: %v", err)
	}

	// Run receiptsRoot and txsRoot in parallel — they operate on independent data structures
	var receipts types.Receipts
	var receiptsRoot common.Hash
	var txsRoot common.Hash
	var txsRootErr error

	var rootsWg sync.WaitGroup
	rootsWg.Add(2)

	// Goroutine 1: receiptsRoot
	go func() {
		defer rootsWg.Done()
		startReceipts := time.Now()
		receipts, receiptsRoot = bp.calculateReceiptsRoot(processResults.Receipts)
		logger.Debug("[PERF] Phase1.receiptsRoot: %v (%d receipts)", time.Since(startReceipts), len(processResults.Receipts))
	}()

	// Goroutine 2: txsRoot (must AddTransactions first)
	go func() {
		defer rootsWg.Done()
		startTxRoot := time.Now()
		txDB.AddTransactions(processResults.Transactions)
		txsRoot, txsRootErr = txDB.IntermediateRoot()
		logger.Debug("[PERF] Phase1.txsRoot: %v (%d txs)", time.Since(startTxRoot), len(processResults.Transactions))
	}()

	rootsWg.Wait()

	if txsRootErr != nil {
		bp.handleBlockGenerationError(txDB, currentBlockNumber-1)
		logger.Fatal("Error getting txsRoot for block #%d: %v", currentBlockNumber, txsRootErr)
	}

	if len(processResults.Transactions) > 0 {
		logger.Debug("[PERF] createBlock #%d Roots (txCount=%d) computed in parallel",
			currentBlockNumber, len(processResults.Transactions))
	}

	// Record phase times
	phase1Elapsed := time.Since(overallStart)

	// Phase 2: Create Block Data
	phase2Start := time.Now()
	// CRITICAL FORK-SAFETY: Convert commitTimestampMs (from Rust) to seconds for BlockHeader
	timestampSec := commitTimestampMs / 1000 // 0 if commitTimestampMs is 0 (fallback to time.Now())

	// CRITICAL FORK-SAFETY: Use leader address from Rust consensus if provided, else fallback to local validator
	blockLeaderAddress := bp.validatorAddress
	if len(leaderAddressOverride) > 0 && leaderAddressOverride[0] != (common.Address{}) {
		blockLeaderAddress = leaderAddressOverride[0]
	}

	var bl *block.Block
	if isStateChanging {
		accountRoot := processResults.Root
		lastConfirmedBlock := bp.GetLastBlock()
		
		bl, err = GenerateBlockData(
			lastConfirmedBlock.Header(), blockLeaderAddress,
			processResults.Transactions, processResults.ExecuteSCResults,
			accountRoot, processResults.StakeStatesRoot, receiptsRoot, txsRoot, currentBlockNumber, epoch, timestampSec, globalExecIndex,
		)
	} else {
		bl, err = GenerateBlockDataReadOnly(
			blockLeaderAddress,
			processResults.Transactions, processResults.ExecuteSCResults,
			common.Hash{}, common.Hash{}, receiptsRoot, txsRoot, currentBlockNumber, epoch, timestampSec, globalExecIndex,
		)
	}
	if err != nil {
		bp.handleBlockGenerationError(txDB, currentBlockNumber-1)
		logger.Fatal("Error generating block #%d: %v", currentBlockNumber, err)
	}

	// CRITICAL FIX: Set GlobalExecIndex on the block immediately after creation, before returning.
	if globalExecIndex > 0 {
		bl.Header().SetGlobalExecIndex(globalExecIndex)
	}

	// CRITICAL FORK-SAFETY: Update lastBlock IMMEDIATELY after block creation
	bp.SetLastBlock(bl)
	headerCopy := bl.Header()
	bp.chainState.SetcurrentBlockHeader(&headerCopy)

	phase2Elapsed := time.Since(phase2Start)

	phase3Start := time.Now()
	err = blockchain.GetBlockChainInstance().SetBlockNumberToHash(uint64(bl.Header().BlockNumber()), bl.Header().Hash())
	if err != nil {
		bp.handleBlockGenerationError(txDB, currentBlockNumber-1)
		logger.Fatal("Error when setting BlockNumberToHash for block #%d: %v", currentBlockNumber, err)
	}
	blockchain.GetBlockChainInstance().AddBlockToCache(bl)
	// TxHashMapBlockNumber is a non-critical in-memory index for TX lookup.
	// Run it async but track via WaitGroup so commitWorker waits before broadcasting receipts.
	var mappingWg sync.WaitGroup
	if bp.serviceType == p_common.ServiceTypeMaster && isStateChanging {
		txsCopy := processResults.Transactions // capture for goroutine
		blockNum := currentBlockNumber
		mappingWg.Add(1)
		go func() {
			defer mappingWg.Done()
			for _, tx := range txsCopy {
				blockchain.GetBlockChainInstance().SetTxHashMapBlockNumber(tx.Hash(), blockNum)
			}
		}()
	}
	phase31Elapsed := time.Since(phase3Start)

	phase32Start := time.Now()
	var trieDBSnapshots map[common.Hash]*trie_database.TrieDatabaseSnapshot
	trieDBSnapshots = processResults.TrieDBSnapshots
	bp.commitToMemoryParallel(txDB, receipts, isStateChanging, trieDBSnapshots)
	phase32Elapsed := time.Since(phase32Start)

	phase4Start := time.Now()
	// TPS OPTIMIZATION: SetCommitLock(false) removed — CommitLock is now a no-op

	// CRITICAL FIX: Snapshot TrieDB collected batches NOW before they get overwritten
	// by next block's commitToMemoryParallel() → CommitAllTrieDatabases().
	// Only TrieDB needs snapshotting because it's explicitly reset via ResetCollectedBatches().
	// Other batch data (Account, SmartContract, etc.) is safe — each block creates fresh data.
	var trieBatchSnapshot map[string][]byte
	var accountBatch []byte
	var smartContractBatch []byte
	var smartContractStorageBatch []byte
	var codeBatchPut []byte
	var mappingBatch []byte
	var stakeBatch []byte

	if isStateChanging {
		trieBatchSnapshot = trie_database.GetTrieDatabaseManager().GetCollectedBatches()
		trie_database.GetTrieDatabaseManager().ResetCollectedBatches()

		// Phase 6 FIX: Synchronously snapshot all other generated Database Batches BEFORE the async dispatch
		accountBatch = bp.chainState.GetAccountStateDB().GetAccountBatch()
		smartContractBatch = bp.chainState.GetSmartContractDB().GetSmartContractBatch()
		smartContractStorageBatch = bp.chainState.GetSmartContractDB().GetSmartContractStorageBatch()
		codeBatchPut = bp.chainState.GetSmartContractDB().GetCodeBatchPut()
		stakeBatch = bp.chainState.GetStakeStateDB().GetStakeBatch()
	}

	mappingBatch = blockchain.GetBlockChainInstance().GetMappingBatch()

	job := CommitJob{
		Block:                     bl,
		ProcessResults:            &processResults,
		Receipts:                  receipts,
		TxDB:                      txDB,
		DoneChan:                  nil, // ASYNC: No blocking — disk persistence runs concurrently
		MappingWg:                 &mappingWg,
		TrieBatchSnapshot:         trieBatchSnapshot,
		AccountBatch:              accountBatch,
		SmartContractBatch:        smartContractBatch,
		SmartContractStorageBatch: smartContractStorageBatch,
		CodeBatchPut:              codeBatchPut,
		MappingBatch:              mappingBatch,
		StakeBatch:                stakeBatch,
		GlobalExecIndex:           globalExecIndex,
	}

	// ASYNC COMMIT: Send job to commitWorker without blocking.
	// The commitWorker handles disk persistence (SaveLastBlock, PrepareBackup, Broadcast)
	// CONCURRENTLY while the execution loop processes the next block.
	// This eliminates the 32-second gap caused by waiting for large block commits.
	select {
	case bp.commitChannel <- job:
		// Sent successfully — commitWorker will process async
	case <-time.After(5 * time.Second):
		logger.Error("❌ [CRITICAL] commitChannel full for more than 5 seconds! Block #%d could not be sent. Check commitWorker.",
			currentBlockNumber)
		bp.commitChannel <- job // Fallback: blocking send
	}

	// NO MORE BLOCKING: execution loop immediately continues to next block
	// Disk persistence (SaveLastBlock, PrepareBackup) runs async in commitWorker
	phase4Elapsed := time.Since(phase4Start)
	overallElapsed := time.Since(overallStart)

	// ── TPS Degradation Monitor (Phase 6) ──────────────────────────────
	// Log key metrics every 50 blocks to track performance degradation
	// over sustained load. Compare block #50 vs #500 to detect:
	// - loadedAccounts unbounded growth (should stay < 100K)
	// - GC frequency increase (PauseTotal should not grow > 2x)
	// - Goroutine leak (should stay stable)
	if currentBlockNumber%50 == 0 {
		var gcStats runtime_debug.GCStats
		runtime_debug.ReadGCStats(&gcStats)
		loadedSize := bp.chainState.GetAccountStateDB().LoadedAccountCount()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		logger.Info("📊 [DEGRADATION MONITOR] Block #%d: loadedAccounts=%d, HeapAlloc=%dMB, GC_pause_total=%v, GC_num=%d, goroutines=%d",
			currentBlockNumber, loadedSize, m.HeapAlloc/(1024*1024),
			gcStats.PauseTotal, gcStats.NumGC, runtime.NumGoroutine())
	}

	// ── Prometheus instrumentation ──────────────────────────────────────
	metrics.BlocksProcessedTotal.Inc()
	metrics.BlockProcessingDuration.Observe(overallElapsed.Seconds())
	metrics.CurrentBlock.Set(float64(bl.Header().BlockNumber()))
	metrics.TxsProcessedTotal.Add(float64(len(processResults.Transactions)))

	if len(processResults.Transactions) > 1000 {
		logger.Info("📦 [batch_id=%s] === Block Creation Time %d (In Memory) [%d txs] === 📦\n   - Phase 1 (Root Calc):      %v\n   - Phase 2 (Block Data):     %v\n   - Phase 3.1 (Mapping):      %v\n   - Phase 3.2 (Trie Commit):  %v\n   - Phase 4 (Job Prep & Snap):%v\n   - 🚀 TOTAL (IN-MEMORY):     %v",
			batchID, bl.Header().BlockNumber(), len(processResults.Transactions),
			phase1Elapsed, phase2Elapsed, phase31Elapsed, phase32Elapsed, phase4Elapsed, overallElapsed)
	}

	return bl
}

// ProcessorPoolReadOnly reads from transaction processor channel
func (bp *BlockProcessor) ProcessorPoolReadOnly() {
	for readOnlyResult := range bp.transactionProcessor.readOnlyResultChan {
		txCount := len(readOnlyResult.Transactions)
		if txCount > 0 {
			bp.inputTxCounter.Add(int64(txCount))
			bp.ProcessedInputTxCount.Add(uint64(txCount))
			bp.transactionProcessor.ProcessResultChan <- readOnlyResult
		}
	}
}

func (bp *BlockProcessor) postProcessBlock(lastBlock types.Block, txHashes []common.Hash) {
	blockNumber := lastBlock.Header().BlockNumber()
	logger.Info("🔄 [PENDING POOL] postProcessBlock called for block #%d, transactions=%d",
		blockNumber, len(lastBlock.Transactions()))

	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(lastBlock.Header().TransactionsRoot(), bp.storageManager.GetStorageTransaction())
	if err != nil {
		logger.Error("⚠️ [PENDING POOL] Error creating TransactionStateDB with root %s for block #%d: %v — skipping post-processing",
			lastBlock.Header().TransactionsRoot().Hex(), blockNumber, err)
		return
	}
	rcpDb, err := receipt.NewReceiptsFromRoot(lastBlock.Header().ReceiptRoot(), bp.storageManager.GetStorageReceipt())
	if err != nil {
		logger.Error("⚠️ [PENDING POOL] Error creating ReceiptsDB with root %s for block #%d: %v — skipping post-processing",
			lastBlock.Header().ReceiptRoot().Hex(), blockNumber, err)
		return
	}

	removedCount := 0
	skippedCount := 0
	for _, txHash := range txHashes {
		tx, err := txDB.GetTransaction(txHash)
		if err != nil {
			logger.Error("⚠️ [PENDING POOL] Transaction not found in block #%d: hash=%s, err=%v — skipping",
				blockNumber, txHash.Hex(), err)
			skippedCount++
			continue
		}
		rpc, err := rcpDb.GetReceipt(txHash)
		if err != nil {
			logger.Error("⚠️ [PENDING POOL] Receipt not found in block #%d: hash=%s, err=%v — skipping",
				blockNumber, txHash.Hex(), err)
			skippedCount++
			continue
		}
		if bp.storageManager.IsExplorer() {
			if err := bp.storageManager.GetExplorerSearchService().IndexTransaction(tx, rpc, lastBlock.Header()); err != nil {
				logger.Error("Error indexing transaction with hash %s: %v", txHash.Hex(), err)
			}
		}

		// Remove transaction from pending pool
		removed := bp.transactionProcessor.pendingTxManager.Remove(txHash)
		if removed {
			removedCount++
		}
	}

	if skippedCount > 0 {
		logger.Warn("⚠️ [PENDING POOL] postProcessBlock for block #%d: skipped %d/%d transactions due to lookup failures",
			blockNumber, skippedCount, len(txHashes))
	}
	logger.Info("✅ [PENDING POOL] postProcessBlock completed for block #%d: removed %d/%d transactions from pending pool",
		blockNumber, removedCount, len(txHashes))
	if bp.storageManager.IsExplorer() {
		bp.storageManager.GetExplorerSearchService().Commit()
		bp.storageManager.GetExplorerSearchService().AddBlockToIndexRanges(lastBlock.Header().BlockNumber())
	}
}

// handleBlockGenerationError handles block generation errors
func (bp *BlockProcessor) handleBlockGenerationError(txDB *transaction_state_db.TransactionStateDB, lastBlockNumber uint64) {
	logger.Error("❌ [BLOCK GEN] Block generation failed at block #%d — discarding trie state and rolling back", lastBlockNumber)
	trie_database.GetTrieDatabaseManager().DiscardAllTrieDatabases()
	bp.chainState.GetAccountStateDB().Discard()
	bp.chainState.GetSmartContractDB().Discard()
	blockchain.GetBlockChainInstance().Discard()
	lastBl := blockchain.GetBlockChainInstance().GetBlockByNumber(lastBlockNumber - 1)
	bp.SetLastBlock(lastBl)
	bp.chainState.GetBlockDatabase().SaveLastBlock(lastBl)
	if txDB != nil {
		txDB.Discard()
	}
}
// ForceCommit triggers an immediate block generation by sending a signal to forceCommitChan
func (bp *BlockProcessor) ForceCommit() {
	select {
	case bp.forceCommitChan <- struct{}{}:
		// Signal sent successfully
	default:
		// Channel full, already signaled
	}
}
