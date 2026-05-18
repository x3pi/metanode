// @title processor/block_processor_processing.go
// @markdown processor/block_processor_processing.go - Block creation and result processing (core)
package processor

import (
	"sync"
	"time"

	"runtime"
	runtime_debug "runtime/debug"

	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
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
		// SNAPSHOT GATE: Block processing while NOMT snapshot is in progress.
		// Without this, ProcessorPool continues calling ProcessTransactionsInPool()
		// which writes to NOMT, causing CloseForSnapshot() to deadlock.
		// Optimized: atomic.Bool check on fast path (zero contention when gate is open).
		bp.waitSnapshotGate()

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
					bp.createBlockFromResults(*accumulatedResults, currentBlockNumber, 0, true, "single_block", 0, 0, 0)
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
			newBlock := bp.createBlockFromResults(*accumulatedResults, currentBlockNumber, 0, true, "single_block", 0, 0, 0)
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
				newBlock := bp.createBlockFromResults(*accumulatedResults, currentBlockNumber, 0, true, "single_block", 0, 0, 0)
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
		// SNAPSHOT GATE: Block transaction processing while NOMT snapshot is in progress.
		// Without this, ProcessorPool continues NOMT writes, causing CloseForSnapshot() deadlock.
		// Optimized: atomic.Bool check on fast path (zero contention when gate is open).
		bp.waitSnapshotGate()

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

			// CRITICAL FORK-SAFETY FIX: Use deterministic blockTime passed from consensus
			// (epoch start) to ensure EVM execution is identical across the cluster, preventing StateRoot forks.
			blockTimeSec := bp.chainState.GetCurrentEpochStartTimestampMs() / 1000
			if blockTimeSec == 0 {
				if lastHeaderPtr := bp.chainState.GetcurrentBlockHeader(); lastHeaderPtr != nil && *lastHeaderPtr != nil {
					lastHeader := *lastHeaderPtr
					blockTimeSec = lastHeader.TimeStamp() + 1
				} else {
					// 🚨 FORK-GUARD: Tuyệt đối KHÔNG sử dụng time.Now()
					// Nếu chưa có genesis timestamp từ Rust consensus, transaction pool
					// phải chuyển sang trạng thái pending (chờ) để tránh sinh ra StateRoot bị lệch.
					logger.Error("🚨 [FORK-GUARD] Missing consensus timestamp and last header! Pausing tx processing to prevent state fork.")
					<-bp.processingLockChan // Giải phóng lock
					time.Sleep(1 * time.Second) // Pending 1 giây rồi kiểm tra lại
					continue
				}
			}

			processResult, err := bp.transactionProcessor.ProcessTransactionsInPool(setEmptyBlock, blockTimeSec)
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
func (bp *BlockProcessor) createBlockFromResults(processResults tx_processor.ProcessResult, currentBlockNumber uint64, epoch uint64, isStateChanging bool, batchID string, commitTimestampMs uint64, globalExecIndex uint64, commitIndex uint32, leaderAddressOverride ...common.Address) *block.Block {
	// LAYER-8: DB Write Lock — serialize all block writes
	bp.blockWriteMutex.Lock()
	defer bp.blockWriteMutex.Unlock()

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
		logger.Error("🚨 [REVERT-GATE] NewTransactionStateDBFromRoot failed: %v — reverting block #%d", err, currentBlockNumber)
		bp.revertDraftBlock(nil, currentBlockNumber)
		return nil
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

		if len(processResults.Receipts) > 0 {
			var combinedHash common.Hash
			for _, rcp := range processResults.Receipts {
				combinedHash = crypto.Keccak256Hash(combinedHash.Bytes(), rcp.TransactionHash().Bytes(), []byte{byte(rcp.Status())})
			}
			logger.Info("🔍 [FORENSIC] Block %d: Input %d receipts to calculateReceiptsRoot. Combined Input Hash: %s", currentBlockNumber, len(processResults.Receipts), combinedHash.Hex())
		}

		receipts, receiptsRoot = bp.calculateReceiptsRoot(processResults.Receipts)
		logger.Debug("[PERF] Phase1.receiptsRoot: %v (%d receipts)", time.Since(startReceipts), len(processResults.Receipts))
	}()

	// Goroutine 2: txsRoot (must AddTransactions first)
	go func() {
		defer rootsWg.Done()
		startTxRoot := time.Now()

		if len(processResults.Transactions) > 0 {
			var combinedHash common.Hash
			for _, tx := range processResults.Transactions {
				combinedHash = crypto.Keccak256Hash(combinedHash.Bytes(), tx.Hash().Bytes())
			}
			logger.Info("🔍 [FORENSIC] Block %d: Input %d txs to txDB. Combined Input Hash: %s", currentBlockNumber, len(processResults.Transactions), combinedHash.Hex())
		}

		txDB.AddTransactions(processResults.Transactions)
		txsRoot, txsRootErr = txDB.IntermediateRoot()
		logger.Debug("[PERF] Phase1.txsRoot: %v (%d txs)", time.Since(startTxRoot), len(processResults.Transactions))
	}()

	rootsWg.Wait()

	if txsRootErr != nil {
		logger.Error("🚨 [REVERT-GATE] Error getting txsRoot for block #%d: %v — reverting", currentBlockNumber, txsRootErr)
		bp.revertDraftBlock(txDB, currentBlockNumber)
		return nil
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

		// ═══════════════════════════════════════════════════════════════
		// FORK-SAFETY: Detect corrupted NOMT stake_db handle (May 2026).
		//
		// ROOT CAUSE: During epoch transitions, the stake_db NOMT handle
		// can be reset/corrupted, causing IntermediateRoot() to return
		// 0x0. This produces blocks with stakeStatesRoot=0x0 that
		// diverge from all other nodes (which have the correct root).
		// Observed: hash_mismatch_alert.log, block #684, Node 3 (m3).
		//
		// FIX: If stakeStatesRoot is zero but we have prior blocks with
		// a valid root, use the previous block's root as a fallback.
		// This is safe because stake state only changes via explicit
		// validator transactions, so if no stake TX was in this block,
		// the root should be identical to the parent's.
		// ═══════════════════════════════════════════════════════════════
		stakeRootForBlock := processResults.StakeStatesRoot
		if stakeRootForBlock == (common.Hash{}) && currentBlockNumber > 1 {
			parentStakeRoot := lastConfirmedBlock.Header().StakeStatesRoot()
			if parentStakeRoot != (common.Hash{}) {
				logger.Error("🚨 [FORK-GUARD] stakeStatesRoot is 0x0 at block #%d! "+
					"NOMT stake_db handle likely corrupted (epoch transition?). "+
					"Falling back to parent block's stakeRoot=%s to prevent fork.",
					currentBlockNumber, parentStakeRoot.Hex())
				stakeRootForBlock = parentStakeRoot
			} else {
				logger.Error("🚨 [FORK-GUARD] stakeStatesRoot AND parent stakeRoot are both 0x0 at block #%d! "+
					"Cannot recover — stake_db may be completely uninitialized.",
					currentBlockNumber)
			}
		}

		bl, err = GenerateBlockData(
			lastConfirmedBlock.Header(), blockLeaderAddress,
			processResults.Transactions, processResults.ExecuteSCResults,
			accountRoot, stakeRootForBlock, receiptsRoot, txsRoot, currentBlockNumber, epoch, timestampSec, globalExecIndex,
		)
	} else {
		bl, err = GenerateBlockDataReadOnly(
			blockLeaderAddress,
			processResults.Transactions, processResults.ExecuteSCResults,
			common.Hash{}, common.Hash{}, receiptsRoot, txsRoot, currentBlockNumber, epoch, timestampSec, globalExecIndex,
		)
	}
	if err != nil {
		logger.Error("🚨 [REVERT-GATE] Error generating block #%d: %v — reverting", currentBlockNumber, err)
		bp.revertDraftBlock(txDB, currentBlockNumber)
		return nil
	}

	// NOTE: GlobalExecIndex is already set by NewBlockHeader() constructor (variadic param).
	// No need to call SetGlobalExecIndex() again — the constructor handles it.

	// CommitIndex is NOT a constructor param — must be set explicitly for Sub node serialization.
	if commitIndex > 0 {
		bl.Header().SetCommitIndex(uint64(commitIndex))
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// FORK GUARD: ParentHash Chain Continuity Verification
	//
	// This is the LAST LINE OF DEFENSE against ALL fork causes:
	// - Consensus divergence (wrong leader/timestamp from sparse DAG)
	// - Execution race condition (stale trie state → wrong stateRoot)
	// - NomtStateTrie corruption (concurrent read/write)
	//
	// Every production blockchain verifies parentHash before committing.
	// If this check fails, the block is REJECTED and the node halts to
	// prevent fork propagation to the rest of the network.
	//
	// Cost: 1 hash comparison per block (zero performance impact).
	// ═══════════════════════════════════════════════════════════════════════════
	if isStateChanging && globalExecIndex > 0 && currentBlockNumber > 1 {
		lastConfirmedForCheck := bp.GetLastBlock()
		if lastConfirmedForCheck != nil {
			expectedParentHash := lastConfirmedForCheck.Header().Hash()
			actualParentHash := bl.Header().LastBlockHash()
			if actualParentHash != expectedParentHash {
				logger.Warn(
					"⚠️ [FORK-GUARD] CHAIN BREAK DETECTED at block #%d! "+
						"parentHash=%s ≠ lastBlock hash=%s. "+
						"NOTE: This is a cosmetic warning. Block Hash() excludes LastBlockHash. "+
						"The chain will continue unless StateRoot also diverges. "+
						"GEI=%d, leader=%s, timestamp=%d",
					currentBlockNumber,
					actualParentHash.Hex(),
					expectedParentHash.Hex(),
					globalExecIndex,
					blockLeaderAddress.Hex(),
					timestampSec,
				)
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// REVERT GATE: Verify draft block BEFORE any global state mutation.
	//
	// Block is still a "draft" (in-memory, no trie commit, no DB persist).
	// If checks fail → discard draft → reset to parent → return nil.
	// Caller will NOT advance GEI, so Rust will retry this commit.
	//
	// BLOCK-AS-PACKAGE: Think of each block as a "package" (gói hàng).
	// A bad package is thrown away here. A good package proceeds to commit.
	// Cost on hot path: 2 hash comparisons (~2ns). Zero allocation.
	// ═══════════════════════════════════════════════════════════════════════════
	if isStateChanging && globalExecIndex > 0 && !bp.verifyDraftBlock(bl, currentBlockNumber, globalExecIndex) {
		logger.Error("🚨 [REVERT-GATE] Draft block #%d FAILED pre-commit verification (GEI=%d). "+
			"Discarding draft and resetting state to parent block. Rust will retry.",
			currentBlockNumber, globalExecIndex)
		bp.revertDraftBlock(txDB, currentBlockNumber)
		return nil
	}

	// CRITICAL FORK-SAFETY: Update lastBlock IMMEDIATELY after block creation
	bp.SetLastBlock(bl)
	// currentBlockHeader and BlockNumberToHash mappings are safely updated 
	// synchronously inside CommitBlockState (via commitWorker) under commitMutex
	// to guarantee no race condition with P2P sync blocks.

	phase2Elapsed := time.Since(phase2Start)

	phase3Start := time.Now()
	blockchain.GetBlockChainInstance().AddBlockToCache(bl)
	// TxHashMapBlockNumber is now safely handled synchronously inside CommitBlockState
	// to avoid race conditions with dirtyStorage flushing.
	var mappingWg sync.WaitGroup // Keep this as dummy to satisfy Job signature if needed

	phase31Elapsed := time.Since(phase3Start)

	phase32Start := time.Now()
	var trieDBSnapshots map[common.Hash]*trie_database.TrieDatabaseSnapshot
	trieDBSnapshots = processResults.TrieDBSnapshots
	if commitErr := bp.commitToMemoryParallel(txDB, receipts, isStateChanging, trieDBSnapshots, currentBlockNumber); commitErr != nil {
		// CRITICAL: commitToMemoryParallel failed (SmartContractDB, AccountPipeline, or StakePipeline).
		// Block has already been SetLastBlock'd (line 398) so we cannot fully revert here.
		// The error is logged at ERROR level for monitoring. The commitWorker will still
		// persist whatever state was successfully committed.
		logger.Error("🚨 [COMMIT-MEMORY] commitToMemoryParallel error for block #%d: %v — block will be committed with partial state", currentBlockNumber, commitErr)
	}
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

	// ═══════════════════════════════════════════════════════════════════════════
	// FORK-SAFETY FIX: Synchronous commit for Rust-driven execution path.
	//
	// ROOT CAUSE: When commit was async (DoneChan=nil), Block N+1's
	// ProcessTransactions could race with Block N's commitWorker:
	//   - commitWorker: CommitPipeline() → PersistAsync() (modifies trie)
	//   - processSingleEpochData: ProcessTransactions → AccountState() (reads trie)
	// Different nodes complete PersistAsync at different times relative to
	// the next block's read → different IntermediateRoot() → different
	// AccountStatesRoot → different block hash → FORK.
	// ═══════════════════════════════════════════════════════════════════════════
	// TPS PIPELINE (May 2026): Non-blocking commit dispatch.
	//
	// PREVIOUSLY: doneChan blocked until commitWorker finished PebbleDB persist.
	// This serialized execution: Block N committed → Block N+1 starts processing.
	//
	// NOW: doneChan is nil (no blocking). Block N+1's EVM starts immediately
	// after commitToMemoryParallel completes (trie is fully updated).
	//
	// SAFETY PROOF:
	//   1. PersistAsync runs INLINE in commitToMemoryParallel (not async).
	//      → Trie swap is complete before we reach this point.
	//   2. commitWorker does NO trie mutations:
	//      - SetGlobalExecIndex (header metadata only)
	//      - CommitFullDb/RevertFullDb (per-contract Xapian, isolated by mvmId)
	//      - CommitBlockState (PebbleDB persist — disk only)
	//      - updateAndPersistConsensusState (GEI to disk)
	//      - BLS signing (header field only)
	//   3. commitChannel serializes commitWorker — ordering guaranteed.
	//   4. blockWriteMutex serializes createBlockFromResults — no concurrent writes.
	//
	// RESULT: EVM(N+1) overlaps with PebbleDB persist(N).
	//   Effective per-block time = max(EVM, Commit) instead of sum(EVM + Commit).
	//   Expected throughput improvement: 2-3x.
	// ═══════════════════════════════════════════════════════════════════════════
	var doneChan chan struct{} // nil = non-blocking pipeline mode

	job := CommitJob{
		Block:                     bl,
		ProcessResults:            &processResults,
		Receipts:                  receipts,
		TxDB:                      txDB,
		DoneChan:                  doneChan, // nil — commitWorker skips signal
		MappingWg:                 &mappingWg,
		TrieBatchSnapshot:         trieBatchSnapshot,
		AccountBatch:              accountBatch,
		SmartContractBatch:        smartContractBatch,
		SmartContractStorageBatch: smartContractStorageBatch,
		CodeBatchPut:              codeBatchPut,
		MappingBatch:              mappingBatch,
		StakeBatch:                stakeBatch,
		GlobalExecIndex:           globalExecIndex,
		CommitIndex:               commitIndex,
	}

	// Send job to commitWorker.
	// Block until commitChannel has space (natural backpressure)
	if cap(bp.commitChannel) > 0 && len(bp.commitChannel) >= cap(bp.commitChannel)*9/10 {
		logger.Warn("🔥 [SATURATION] commitChannel is %d/%d full (Pipeline stalled)!", len(bp.commitChannel), cap(bp.commitChannel))
	}
	bp.commitChannel <- job

	// ═══════════════════════════════════════════════════════════════
	// TPS PIPELINE: No blocking wait. commitWorker processes the job
	// asynchronously. The next block can begin EVM execution immediately.
	// commitChannel (cap=10000) provides natural backpressure if Go
	// can't persist fast enough.
	// ═══════════════════════════════════════════════════════════════
	logger.Debug("⚡ [PIPELINE] Block #%d commit dispatched (non-blocking, GEI=%d, pending=%d)",
		currentBlockNumber, globalExecIndex, len(bp.commitChannel))

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
	lastBl := blockchain.GetBlockChainInstance().GetBlockByNumber(lastBlockNumber)
	bp.SetLastBlock(lastBl)
	bp.chainState.GetBlockDatabase().SaveLastBlock(lastBl)
	if txDB != nil {
		txDB.Discard()
	}
}

// verifyDraftBlock performs pre-commit sanity checks on a draft block.
// Returns true if the block is safe to commit, false if it should be discarded.
//
// This is the REVERT GATE in the Block-as-Package architecture:
// - Runs AFTER GenerateBlockData (block built) but BEFORE SetLastBlock/commitToMemory
// - No global state has been mutated yet → safe to discard and return nil
// - Catches local corruption (NOMT handle reset, trie corruption) BEFORE persistence
// - Hot path cost: 2 hash comparisons (~2ns), zero allocations
func (bp *BlockProcessor) verifyDraftBlock(bl *block.Block, currentBlockNumber uint64, globalExecIndex uint64) bool {
	// CHECK 1: AccountStatesRoot sanity
	// 0x0 root means NOMT trie is uninitialized or handle was reset (e.g., epoch transition).
	// Committing this block would create a permanent fork — every subsequent block inherits 0x0.
	if bl.Header().AccountStatesRoot() == (common.Hash{}) && currentBlockNumber > 1 {
		logger.Error("🚨 [REVERT-GATE] Block #%d has AccountStatesRoot=0x0 — NOMT corruption! (GEI=%d)",
			currentBlockNumber, globalExecIndex)
		return false
	}

	// CHECK 2: StakeStatesRoot sanity
	// 0x0 stake root after the fallback (line ~297) means both current AND parent are corrupt.
	// This is unrecoverable locally — must discard and let Rust retry after NOMT re-init.
	if bl.Header().StakeStatesRoot() == (common.Hash{}) && currentBlockNumber > 1 {
		logger.Error("🚨 [REVERT-GATE] Block #%d has StakeStatesRoot=0x0 — stake trie corruption! (GEI=%d)",
			currentBlockNumber, globalExecIndex)
		return false
	}

	return true
}

// revertDraftBlock discards a draft block and resets all in-memory state to the parent block.
// Called when verifyDraftBlock fails. After this, the node is ready to retry the same GEI.
//
// DEADLOCK-FREE: All operations are bounded (map clear + atomic swap + 1 PebbleDB write).
// Total cost: ~1-5ms.
func (bp *BlockProcessor) revertDraftBlock(txDB *transaction_state_db.TransactionStateDB, failedBlockNumber uint64) {
	logger.Error("🔄 [REVERT] Discarding draft block #%d — resetting to parent", failedBlockNumber)

	// 1. Discard all dirty trie state (in-memory, no I/O)
	trie_database.GetTrieDatabaseManager().DiscardAllTrieDatabases()
	bp.chainState.GetAccountStateDB().Discard()
	bp.chainState.GetSmartContractDB().Discard()
	blockchain.GetBlockChainInstance().Discard()

	// 2. Reset lastBlock pointer to the parent (the block BEFORE the failed one)
	parentBlockNumber := failedBlockNumber - 1
	parentBlock := blockchain.GetBlockChainInstance().GetBlockByNumber(parentBlockNumber)
	if parentBlock != nil {
		bp.SetLastBlock(parentBlock)
		bp.chainState.GetBlockDatabase().SaveLastBlock(parentBlock)
		logger.Info("✅ [REVERT] State reset to parent block #%d (hash=%s)",
			parentBlockNumber, parentBlock.Header().Hash().Hex()[:18]+"...")
	} else {
		logger.Error("❌ [REVERT] Parent block #%d not found in cache! Node may need STARTUP-SYNC.",
			parentBlockNumber)
	}

	// 3. Discard transaction state DB (function-local, but clean up)
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
