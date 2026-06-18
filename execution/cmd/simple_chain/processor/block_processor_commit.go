// @title processor/block_processor_commit.go
// @markdown processor/block_processor_commit.go - Block commit and persistence functionality
package processor

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	stake_state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor/pipeline"
	"github.com/meta-node-blockchain/meta-node/types"
)

// commitWorker handles committing and broadcasting blocks after creation
func (bp *BlockProcessor) commitWorker() {
	logger.Info("🚀 [COMMIT] commitWorker loop started")
	logger.Info("✅ Commit Worker initiated")
	for job := range bp.commitChannel {
		if job.Block == nil {
			logger.Info("🔧 [COMMIT] commitWorker: received FENCE job (commitChannel=%d/%d)", len(bp.commitChannel), cap(bp.commitChannel))
			if job.GlobalExecIndex > 0 || job.CommitIndex > 0 || job.Epoch > 0 {
				// FENCE jobs have no block — use job.Epoch from async update
				bp.updateAndPersistConsensusState(job.GlobalExecIndex, job.CommitIndex, job.Epoch)
			}
			if job.DoneChan != nil {
				close(job.DoneChan)
				logger.Info("🔧 [COMMIT] commitWorker: FENCE signaled (DoneChan closed)")
			}
			continue
		}

		start := time.Now()
		blockNum := job.Block.Header().BlockNumber()
		txCount := len(job.Block.Transactions())

		// ══════════════════════════════════════════════════════════════════
		// NORMAL PATH: Blocks with transactions — full processing
		// ══════════════════════════════════════════════════════════════════

		// T2-6: Construct batch_id for end-to-end tracing
		batchID := fmt.Sprintf("E%dC0G%d", job.Block.Header().Epoch(), job.GlobalExecIndex)

		logger.Debug("[batch_id=%s] 📋 [COMMIT] CommitWorker: Block %d (txs=%d) dequeued, queueLen=%d",
			batchID, blockNum, txCount, len(bp.commitChannel))
		// Rust will call GetValidatorsAtBlockRequest to check if block is committed
		// Ensure block is committed to DB before sending doneChan signal
		startSave := time.Now()

		// CRITICAL FIX: Set GlobalExecIndex on the block header BEFORE saving to DB
		// Otherwise snapshot restores will start with GEI=0 because the header is empty
		if job.GlobalExecIndex > 0 {
			job.Block.Header().SetGlobalExecIndex(job.GlobalExecIndex)
		}

		// ══════════════════════════════════════════════════════════════════
		// XAPIAN FLUSH FOR KEEPALIVE SNAPSHOTS
		// MVM Xapian database changes must be flushed to disk before `SaveLastBlock`,
		// because `UpdateLastBlockNumber` can trigger an asynchronous node snapshot.
		// ══════════════════════════════════════════════════════════════════
		if job.ProcessResults != nil {
			// OPTIMIZATION: Dedup mvmIds — multiple TXs to the same contract share
			// one MVMApi instance. CommitFullDb/RevertFullDb only needs to be called
			// once per contract, not once per TX.
			committedMvmIds := make(map[common.Address]struct{}, 64)

			for _, tx := range job.ProcessResults.Transactions {
				isCall := tx.IsCallContract()
				isDeploy := tx.IsDeployContract()

				if (isCall || isDeploy) && !tx.GetReadOnly() && tx.ToAddress() != utils.GetAddressSelector(p_common.ACCOUNT_SETTING_ADDRESS_SELECT) && tx.ToAddress() != utils.GetAddressSelector(p_common.IDENTIFIER_STAKE) {
					mvmId, exists := job.ProcessResults.MvmIdMap[tx.Hash()]
					if !exists {
						if isCall {
							mvmId = tx.ToAddress()
						}
					}
					// Skip if this mvmId was already committed/reverted
					if _, done := committedMvmIds[mvmId]; done {
						continue
					}
					committedMvmIds[mvmId] = struct{}{}
				}
			}

			// ═══════════════════════════════════════════════════════════════
			// PARALLEL XAPIAN FLUSH: Each mvmId has an isolated Xapian DB
			// (separate directory on disk). CommitFullDb for mvmId_A has zero
			// shared state with mvmId_B, so they can safely flush in parallel.
			// For blocks with N contracts, this reduces commit time from
			// N × flush_time to max(flush_time) — significant when N > 2.
			// ═══════════════════════════════════════════════════════════════
			if len(committedMvmIds) > 1 {
				var xapianWg sync.WaitGroup
				for mvmId := range committedMvmIds {
					xapianWg.Add(1)
					go func(id common.Address) {
						defer xapianWg.Done()
						mvmAPI := mvm.GetMVMApi(id)
						if mvmAPI != nil {
							mvmAPI.CommitFullDb()
							mvm.UnprotectMVMApi(id)
						}
					}(mvmId)
				}
				xapianWg.Wait()
			} else {
				// Single mvmId — no goroutine overhead needed
				for mvmId := range committedMvmIds {
					mvmAPI := mvm.GetMVMApi(mvmId)
					if mvmAPI != nil {
						mvmAPI.CommitFullDb()
						mvm.UnprotectMVMApi(mvmId)
					}
				}
			}

			// MEMORY OPTIMIZATION: Eagerly clear all committed MVMApi instances
			// instead of relying solely on RemoveOldApiInstances() GC (50K threshold).
			// Safe because no consumer reads MVMApi after commit/revert phase.
			for mvmId := range committedMvmIds {
				mvm.ClearMVMApi(mvmId)
			}
			mvm.RemoveOldApiInstances()
		}

		// ══════════════════════════════════════════════════════════════════
		// CRITICAL FIX: Use centralized CommitBlockState to atomically update ALL
		// chain state components, including blockNumber→hash and tx→blockNumber mappings.
		// Without this, eth_getBlockByNumber returns null for organically produced blocks.
		// By adding WithCommitMappings(), we ensure dirtyStorage is flushed to LevelDB immediately
		// AFTER it is populated, rather than asynchronously via commitToMemoryParallel.
		// Write changelog synchronously in commitWorker BEFORE CommitBlockState to guarantee sequential progression
		// and visibility of historical states when block counter is advanced.
		if job.AccountNomtPayload != nil {
			if payload, ok := job.AccountNomtPayload.(interface{ WriteChangelog() }); ok {
				payload.WriteChangelog()
			}
		}
		if job.StakeNomtPayload != nil {
			if payload, ok := job.StakeNomtPayload.(interface{ WriteChangelog() }); ok {
				payload.WriteChangelog()
			}
		}

		lastBlockBeforeCommit := storage.GetLastBlockNumber()
		logger.Debug("📋 [COMMIT-WORKER] CommitBlockState for block #%d (txs=%d, lastBlockNum_before=%d, commitChannelLen=%d/%d)",
			blockNum, txCount, lastBlockBeforeCommit, len(bp.commitChannel), cap(bp.commitChannel))
		if _, err := bp.chainState.CommitBlockState(job.Block, blockchain.WithPersistToDB(), blockchain.WithSaveTxMapping(), blockchain.WithCommitMappings()); err != nil {
			logger.Error("commitWorker: CommitBlockState failed for block #%d: %v", blockNum, err)
		} else {
			// Flush NOMT payloads asynchronously now that the block is safely written to block database (PebbleDB)
			if job.AccountNomtPayload != nil {
				if payload, ok := job.AccountNomtPayload.(interface{ CommitAsync() }); ok {
					payload.CommitAsync()
				}
			}
			if job.StakeNomtPayload != nil {
				if payload, ok := job.StakeNomtPayload.(interface{ CommitAsync() }); ok {
					payload.CommitAsync()
				}
			}

			// Remove from pending store now that it is fully committed
			bp.RemovePendingCommitBlock(blockNum)
			lastBlockAfterCommit := storage.GetLastBlockNumber()
			if lastBlockAfterCommit != blockNum {
				logger.Error("🚨 [COMMIT-WORKER] Block #%d CommitBlockState completed but lastBlockNumber=%d (expected %d) — BLOCK MAY HAVE BEEN REJECTED!",
					blockNum, lastBlockAfterCommit, blockNum)
			}
		}
		saveDuration := time.Since(startSave)
		pipeline.GlobalBlockTraceStore.UpdateSaveDBTime(blockNum, saveDuration.Milliseconds())

		// CRITICAL CRASH-SAFETY FIX: Update GEI after block save.
		// Ensures block data is safely on disk before GEI advances,
		// preventing the Rust consensus from skipping un-saved blocks after a restart.
		if job.GlobalExecIndex > 0 || job.CommitIndex > 0 {
			// PIPELINE-SAFE: Use epoch from this block's header, not bp.GetLastBlock()
			bp.updateAndPersistConsensusState(job.GlobalExecIndex, job.CommitIndex, job.Block.Header().Epoch())
		}

		logger.Debug("[PERF] Block Commit phase 1 (Save DB): %v, block: %v", saveDuration, blockNum)

		// CRITICAL FOR SNAPSHOT: Verify block is committed to DB
		lastCommittedBlockNumber := storage.GetLastBlockNumber()
		if lastCommittedBlockNumber != blockNum {
			logger.Error("❌ [SNAPSHOT] CRITICAL: Block #%d commit verification failed! Expected last_committed_block=%d, but got %d",
				blockNum, blockNum, lastCommittedBlockNumber)
		} else {
			logger.Debug("✅ [SNAPSHOT] Block #%d commit verified: last_committed_block=%d (Rust can now query this block for snapshot)",
				blockNum, lastCommittedBlockNumber)
		}

		header := job.Block.Header()
		logger.Debug("[batch_id=%s] 📋 [MASTER] Block #%d committed (tx_count=%d, save=%v): %s",
			batchID, header.BlockNumber(), txCount, saveDuration, header.String())

		// ══════════════════════════════════════════════════════════════════
		// DEADLOCK FIX (May 2026): Epoch auto-update and FlushAll MOVED
		// to AFTER DoneChan signal. See below (after line "DoneChan <- ...").
		//
		// ROOT CAUSE: CheckAndUpdateEpochFromBlock can trigger
		// OnEpochAdvanced → snapshot → PauseExecution() → needs
		// ExecutionMutex.Lock(). But processRustEpochData holds
		// ExecutionMutex.RLock() and is blocked on DoneChan.
		// commitWorker is the goroutine that signals DoneChan,
		// but it was stuck here doing FlushAll BEFORE signaling.
		// ══════════════════════════════════════════════════════════════════

		// logger.Debug("✅ [TX COMMIT] Block #%d saved to database successfully: hash=%s, tx_count=%d",
		// 	blockNum, blockHash[:16]+"...", txCount)

		// CRITICAL: Only send to indexingChannel if is_explorer is enabled in config
		// Non-blocking send to prevent blocking commitWorker
		// If indexingChannel is full, skip indexing for this block rather than blocking
		// This ensures commitWorker can continue and send doneChan signal
		if bp.storageManager.IsExplorer() {
			select {
			case bp.indexingChannel <- job.Block.Header().BlockNumber():
				// Successfully sent to indexing channel
			default:
				// Indexing channel is full, skip indexing for this block to avoid blocking
				logger.Warn("⚠️  [INDEXING] indexingChannel is full, skipping indexing for block #%d to avoid blocking commitWorker",
					blockNum)
			}
		}

		// Pipeline stats: track committed TXs and block timing
		GlobalPipelineStats.IncrTxsCommitted(int64(txCount))
		GlobalPipelineStats.SetLastBlock(int64(blockNum))
		GlobalPipelineStats.SetLastCommitTimeUs(time.Since(start).Microseconds())

		// ══════════════════════════════════════════════════════════════════
		// BLS BLOCK SIGNING: Sign block hash BEFORE DoneChan signal.
		// CRITICAL: Must happen before DoneChan because Rust may trigger a
		// snapshot immediately after receiving the done signal. If signing runs
		// after DoneChan, the snapshot captures a block without its BLS signature
		// → Sub-node restore fails signature verification.
		// BLS sign ~0.5ms — negligible compared to block execution time.
		// ══════════════════════════════════════════════════════════════════
		if bp.blockSigner != nil {
			signingHash := job.Block.Header().Hash()
			signature := bp.blockSigner.SignBlockHash(signingHash)
			job.Block.Header().SetAggregateSignature(signature)
			logger.Debug("🔏 [BLOCK SIGN] Signed block #%d: hash=%s, sig_len=%d",
				blockNum, signingHash.Hex()[:16]+"...", len(signature))
		}

		// ══════════════════════════════════════════════════════════════════
		// TPS OPTIMIZATION: Send DoneChan BEFORE BackupDb serialization.
		// DoneChan only requires primary block data (SaveLastBlock + GEI + BLS sig)
		// to be safely on disk. BackupDb is for Sub-node replication only
		// and can be prepared after unblocking Rust consensus.
		//
		// CRASH SAFETY: If crash occurs between DoneChan and BackupDb persist,
		// Sub-nodes will fetch the block from Master's primary BlockDatabase
		// via the existing network sync mechanism (HandleSyncBlocksRequest).
		// ══════════════════════════════════════════════════════════════════
		if job.DoneChan != nil {
			logger.Debug("📤 [SNAPSHOT] Sending doneChan signal for block #%d (block committed to primary DB, GEI persisted, BLS signed)",
				blockNum)
			job.DoneChan <- struct{}{}
		}

		// ══════════════════════════════════════════════════════════════════
		// EPOCH AUTO-SYNC (MOVED HERE — was before DoneChan, caused deadlock)
		// Safe to run AFTER DoneChan because:
		//   1. Block data is already fully persisted (CommitBlockState above)
		//   2. processRustEpochData has been unblocked by DoneChan signal
		//   3. ExecutionMutex.RLock() is no longer held (Fix 1 releases it)
		//   4. If this triggers a snapshot, PauseExecution() can now succeed
		// ══════════════════════════════════════════════════════════════════
		if bp.chainState.CheckAndUpdateEpochFromBlock(header.Epoch(), header.TimeStamp()) {
			logger.Info("🔄 [MASTER] Epoch auto-synced from block #%d to epoch %d",
				header.BlockNumber(), header.Epoch())

			// STALL-PREVENTION (May 2026): FlushAll deferred to background goroutine
			// to avoid blocking commitWorker from processing the next block's CommitJob.
			// PebbleDB flushes can take 100-500ms under heavy write load.
			//
			// FORK-SAFETY: Tracked by backupDbWg so WaitForPersistence() catches it
			// before any snapshot. Without this, snapshot could capture state before
			// PebbleDB memtable is flushed → fork on restore.
			bp.backupDbWg.Add(1)
			go func() {
				defer bp.backupDbWg.Done()
				logger.Info("💾 [PERSISTENCE] Epoch boundary detected. Flushing PebbleDB to SST (background, tracked).")
				if err := bp.storageManager.FlushAll(); err != nil {
					logger.Error("❌ [PERSISTENCE] Failed to flush PebbleDB at epoch boundary: %v", err)
				}
			}()
		}

		// ══════════════════════════════════════════════════════════════════
		// BACKUP: Serialize and persist BackupDb is DEFERRED to a background goroutine.
		// This uses a coalescing queue to skip intermediate backups when catching up.
		// ══════════════════════════════════════════════════════════════════
		if bp.storageManager.GetStorageBackupDb() != nil {
			bp.backupDbWg.Add(1)
			select {
			case bp.backupDbChannel <- job:
				// enqueued successfully
			default:
				// queue full, DO NOT DROP! Spawn a transient worker to prevent data loss
				// without blocking the commitWorker pipeline.
				logger.Warn("⚠️ [BACKUP] backupDbChannel full, spawning transient worker for block #%d to prevent SyncOnly stall", blockNum)
				go func(j CommitJob) {
					bp.persistBackupDbAsync(j)
					bp.backupDbWg.Done()
				}(job)
			}
		}

		// ══════════════════════════════════════════════════════════════════
		// STATE ATTESTATION: Log + sign state hash every N blocks for fork detection.
		// Lightweight check — only runs at interval boundaries.
		// ══════════════════════════════════════════════════════════════════
		go bp.checkAndLogAttestation(blockNum)

		// ══════════════════════════════════════════════════════════════════
		// BROADCAST EVENTS AND RECEIPTS ALONGSIDE MAPPING WAIT
		// ══════════════════════════════════════════════════════════════════
		if job.ProcessResults != nil {
			var allEventLogs []types.EventLog
			for _, logs := range job.ProcessResults.EventLogs {
				allEventLogs = append(allEventLogs, logs...)
			}

			go func(wg *sync.WaitGroup, block types.Block, receipts []types.Receipt, events []types.EventLog) {
				if wg != nil {
					wg.Wait()
				}
				bp.broadcastEventsAndReceipts(block, receipts, events)
			}(job.MappingWg, job.Block, job.ProcessResults.Receipts, allEventLogs)
		}

		totalDuration := time.Since(start)
		trace := pipeline.GlobalBlockTraceStore.UpdateTotalBlockTime(blockNum, totalDuration.Milliseconds())
		
		if txCount > 0 {
			logger.Info("📊 [BLOCK-TRACE] Block #%d | TXs: %d | Rust: %dms | EVM: %dms | Roots: %dms | Mem: %dms | DB: %dms | Total: %dms",
				trace.BlockNumber, trace.TxCount,
				trace.ConsensusDurationMs,
				trace.ProcessTxsDurationMs,
				trace.Phase1TotalDurationMs - trace.ProcessTxsDurationMs,
				trace.CommitMemoryDurationMs,
				trace.SaveDBDurationMs,
				trace.TotalBlockDurationMs,
			)
		} else {
			logger.Debug("[PERF] COMMIT_WORKER: Block %v critical path: %v, txs: %v", blockNum, totalDuration, txCount)
		}
	}
}

// commitToMemoryParallel performs parallel memory commit operations.
// PIPELINE COMMIT: AccountStateDB and StakeStateDB use CommitPipeline() (fast, releases locks early)
// instead of Commit() (slow, holds locks until BatchPut completes).
// PersistAsync runs inline (synchronous) to guarantee trie swap completes before
// the next block starts processing — eliminating the fork race condition.
func (bp *BlockProcessor) commitToMemoryParallel(txDB *transaction_state_db.TransactionStateDB, receipts types.Receipts, isStateChanging bool, trieDBSnapshots map[common.Hash]*trie_database.TrieDatabaseSnapshot, blockNumber uint64) (accountBatch []byte, stakeBatch []byte, smartContractBatch []byte, smartContractStorageBatch []byte, codeBatchPut []byte, err error) {
	overallStart := time.Now()

	// Will hold the pipeline results for async persistence
	var accountPipelineResult *account_state_db.PipelineCommitResult
	var stakePipelineResult *stake_state_db.StakePipelineCommitResult
	var receiptPipelineResult *types.ReceiptPipelineResult

	type taskResult struct {
		name     string
		err      error
		duration time.Duration
	}

	var scDuration time.Duration

	// Count total tasks: txDB + Receipts + (if stateChanging: AccountPipeline + StakePipeline + TrieDB)
	totalTasks := 2
	if isStateChanging {
		if tx_processor.NomtAheadReplayMode.Load() {
			logger.Warn("🛡️ [NOMT-AHEAD-REPLAY-COMMIT] Bypassing trie and state DB parallel commit because NomtAheadReplayMode is active.")
			// Discard in-memory states to prevent leaks and double mutations
			bp.chainState.GetAccountStateDB().Discard()
			bp.chainState.GetSmartContractDB().Discard()
			bp.chainState.GetStakeStateDB().Discard()
			trie_database.GetTrieDatabaseManager().DiscardAllTrieDatabases()

			// We only run txDB and Receipts commits
			totalTasks = 2
		} else {
			// CRITICAL FIX: SmartContractDB MUST commit sequentially BEFORE AccountStateDB!
			// SmartContractDB.Commit() computes the new StorageRoot for contracts and late-binds
			// them into AccountStateDB. If this runs in parallel with AccountStateDB.CommitPipeline(),
			// a severe race condition occurs causing non-deterministic StateRoots (i.e. cluster forks).
			scStart := time.Now()
			if err := bp.chainState.GetSmartContractDB().Commit(); err != nil {
				logger.Error("🚨 [COMMIT] Sequential SmartContractDB commit error: %v — cannot proceed", err)
				return nil, nil, nil, nil, nil, fmt.Errorf("SmartContractDB commit failed: %w", err)
			}
			scDuration = time.Since(scStart)
			logger.Debug("[PERF] SmartContractDB (Sequential): %v", scDuration)

			totalTasks += 3
		}
	}

	var wg sync.WaitGroup
	resultsChan := make(chan taskResult, totalTasks)

	// Always run txDB and Receipts commits
	wg.Add(2)
	go func() {
		defer wg.Done()
		start := time.Now()
		_, err := txDB.Commit()
		resultsChan <- taskResult{name: "txDB", err: err, duration: time.Since(start)}
	}()
	go func() {
		defer wg.Done()
		start := time.Now()
		var err error
		receiptPipelineResult, err = receipts.CommitPipeline()
		resultsChan <- taskResult{name: "Receipts", err: err, duration: time.Since(start)}
	}()

	if isStateChanging && !tx_processor.NomtAheadReplayMode.Load() {
		// Launch ALL state-changing commits in parallel
		wg.Add(3)

		// AccountStateDB.CommitPipeline — the heaviest task (~600-900ms for 50k TXs)
		go func() {
			defer wg.Done()
			start := time.Now()
			var err error
			// Set blockNumber for StateChangelog BEFORE CommitPipeline.
			// CRITICAL: Use SetTrieCommitBlock() instead of Trie() to avoid deadlock.
			// muTrie.Lock() is held by IntermediateRoot(true) from block creation,
			// so calling Trie() (which needs muTrie.RLock()) would deadlock.
			bp.chainState.GetAccountStateDB().SetTrieCommitBlock(blockNumber)
			accountPipelineResult, err = bp.chainState.GetAccountStateDB().CommitPipeline()
			resultsChan <- taskResult{name: "AccountPipeline", err: err, duration: time.Since(start)}
		}()

		// StakeStateDB.CommitPipeline
		go func() {
			defer wg.Done()
			start := time.Now()
			var err error
			bp.chainState.GetStakeStateDB().SetTrieCommitBlock(blockNumber)
			stakePipelineResult, err = bp.chainState.GetStakeStateDB().CommitPipeline()
			resultsChan <- taskResult{name: "StakePipeline", err: err, duration: time.Since(start)}
		}()

		// TrieDatabases (MVM smart contract storage) - Commit from Snapshot to avoid data race
		go func() {
			defer wg.Done()
			start := time.Now()
			err := trie_database.GetTrieDatabaseManager().CommitSnapshots(trieDBSnapshots)
			resultsChan <- taskResult{name: "TrieDatabases", err: err, duration: time.Since(start)}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results and check for errors
	var maxDuration time.Duration
	var maxTask string
	var commitErrors []string
	for result := range resultsChan {
		if result.err != nil {
			logger.Error("🚨 [COMMIT] Parallel commit error (%s): %v — skipping persist for this component", result.name, result.err)
			commitErrors = append(commitErrors, fmt.Sprintf("%s: %v", result.name, result.err))
			if result.name == "AccountPipeline" {
				accountPipelineResult = nil
			} else if result.name == "StakePipeline" {
				stakePipelineResult = nil
			} else if result.name == "Receipts" {
				receiptPipelineResult = nil
			}
		}
		if result.duration > maxDuration {
			maxDuration = result.duration
			maxTask = result.name
		}
	}
	if len(commitErrors) > 0 {
		for _, errStr := range commitErrors {
			if len(errStr) > 15 && (errStr[:15] == "AccountPipeline" || errStr[:13] == "StakePipeline") {
				logger.Error("🚨 [COMMIT] CRITICAL pipeline task failed: %s — block MUST be reverted to prevent fork", errStr)
				return nil, nil, nil, nil, nil, fmt.Errorf("critical commit failure: %s", errStr)
			}
		}
		logger.Error("🚨 [COMMIT] %d non-critical commit tasks failed: %v — node continues (will self-heal)",
			len(commitErrors), commitErrors)
	}

	var accountPersistDuration, stakePersistDuration, receiptPersistDuration time.Duration

	if accountPipelineResult != nil {
		accountBatch = accountPipelineResult.AccountBatch
		bp.pendingAccountPayload = accountPipelineResult.NomtPayload
		go func(res *account_state_db.PipelineCommitResult) {
			startPersist := time.Now()
			if err := bp.chainState.GetAccountStateDB().PersistAsync(res); err != nil {
				logger.Error("🚨 [COMMIT] PersistAsync failed for AccountStateDB: %v", err)
			}
			if d := time.Since(startPersist); d > 10*time.Millisecond {
				logger.Debug("[PERF] AccountStateDB PersistAsync (async): %v", d)
			}
		}(accountPipelineResult)
	}
	if stakePipelineResult != nil {
		stakeBatch = stakePipelineResult.StakeBatch
		bp.pendingStakePayload = stakePipelineResult.NomtPayload
		go func(res *stake_state_db.StakePipelineCommitResult) {
			startPersist := time.Now()
			if err := bp.chainState.GetStakeStateDB().PersistAsync(res); err != nil {
				logger.Error("🚨 [COMMIT] PersistAsync failed for StakeStateDB: %v", err)
			}
			if d := time.Since(startPersist); d > 10*time.Millisecond {
				logger.Debug("[PERF] StakeStateDB PersistAsync (async): %v", d)
			}
		}(stakePipelineResult)
	}
	if receiptPipelineResult != nil {
		go func(res *types.ReceiptPipelineResult) {
			startPersist := time.Now()
			if err := receipts.PersistAsync(res); err != nil {
				logger.Error("🚨 [COMMIT] PersistAsync failed for Receipts: %v", err)
			}
			if d := time.Since(startPersist); d > 10*time.Millisecond {
				logger.Debug("[PERF] Receipts PersistAsync (async): %v", d)
			}
		}(receiptPipelineResult)
	}

	// Capture contract batches while we are sequentially safe inside commitToMemoryParallel
	if isStateChanging && !tx_processor.NomtAheadReplayMode.Load() {
		smartContractBatch = bp.chainState.GetSmartContractDB().GetSmartContractBatch()
		smartContractStorageBatch = bp.chainState.GetSmartContractDB().GetSmartContractStorageBatch()
		codeBatchPut = bp.chainState.GetSmartContractDB().GetCodeBatchPut()
	}

	overallDuration := time.Since(overallStart)
	if overallDuration > 10*time.Millisecond {
		logger.Info("[PERF] Block #%d commitToMemoryParallel Breakdown:\n   - SmartContractDB (Seq):    %v\n   - Tasks (Parallel Max):     %v (task: %s)\n   - Persist Account DB:       %v\n   - Persist Stake DB:         %v\n   - Persist Receipts DB:      %v\n   - 🚀 TOTAL COMMIT MEMORY:    %v",
			blockNumber, scDuration, maxDuration, maxTask, accountPersistDuration, stakePersistDuration, receiptPersistDuration, overallDuration)
	}

	return accountBatch, stakeBatch, smartContractBatch, smartContractStorageBatch, codeBatchPut, nil
}

// persistWorker REMOVED (May 2026): Was a no-op fence goroutine. PersistAsync
// runs inline in commitToMemoryParallel. WaitForPersistence now drains via
// commitChannel fence + backupDbWg.Wait() directly — no persist fence needed.

// backupDbWorker processes BackupDb serialization in the background using a fixed worker pool.
// Previously spawned one goroutine per block (unbounded), which under high load could create
// hundreds of concurrent serialization goroutines → memory spikes + GC pressure.
// Now uses a fixed pool of 4 workers to bound concurrency.
func (bp *BlockProcessor) backupDbWorker() {
	const numWorkers = 4
	logger.Info("✅ BackupDb Worker initiated (fixed pool of %d workers)", numWorkers)

	workChan := make(chan CommitJob, 8)

	// Start fixed worker pool
	for i := 0; i < numWorkers; i++ {
		go func() {
			for job := range workChan {
				bp.persistBackupDbAsync(job)
				bp.backupDbWg.Done()
			}
		}()
	}

	for job := range bp.backupDbChannel {
		// IMPORTANT: Do NOT drop intermediary blocks (coalescing), as BackupDb contains
		// critical block-level state deltas needed by peers to sync.
		select {
		case workChan <- job:
			// Dispatched to worker
		default:
			// All workers busy — serialize inline to prevent data loss
			logger.Warn("⚠️ [BACKUP] All %d workers busy, serializing block #%d inline", numWorkers, job.Block.Header().BlockNumber())
			bp.persistBackupDbAsync(job)
			bp.backupDbWg.Done()
		}
	}
	close(workChan)
}

// persistBackupDbAsync performs the heavy serialization of BackUpDb asynchronously.
// Sub-nodes rely on this backup payload to rebuild state during synchronization.
func (bp *BlockProcessor) persistBackupDbAsync(job CommitJob) {
	startBackup := time.Now()
	blockNum := job.Block.Header().BlockNumber()

	rawBlockBytes, marshalErr := job.Block.Marshal()
	var bockBatchSerialized []byte
	if marshalErr == nil {
		blockBatch := [][2][]byte{
			{job.Block.Header().Hash().Bytes(), rawBlockBytes},
		}
		bockBatchSerialized, _ = storage.SerializeBatch(blockBatch)
	}

	var receiptBatchSerialized []byte
	if job.Receipts != nil {
		receiptBatchSerialized = job.Receipts.GetReceiptBatchPut()
	}
	if len(receiptBatchSerialized) == 0 && job.ProcessResults != nil && len(job.ProcessResults.Receipts) > 0 {
		var rb [][2][]byte
		for _, r := range job.ProcessResults.Receipts {
			b, err := r.Marshal()
			if err == nil {
				rb = append(rb, [2][]byte{r.TransactionHash().Bytes(), b})
			}
		}
		receiptBatchSerialized, _ = storage.SerializeBatch(rb)
	}

	var txBatchSerialized []byte
	if job.ProcessResults != nil && len(job.ProcessResults.Transactions) > 0 {
		var tb [][2][]byte
		for _, tx := range job.ProcessResults.Transactions {
			b, err := tx.Marshal()
			if err == nil {
				tb = append(tb, [2][]byte{tx.Hash().Bytes(), b})
			}
		}
		txBatchSerialized, _ = storage.SerializeBatch(tb)
	}

	var fullDbLogs []map[string][]byte
	if job.ProcessResults != nil {
		fullDbLogs = job.ProcessResults.FullDbLogs
	}

	backupData := storage.BackUpDb{
		BockNumber:                blockNum,
		BockBatch:                 bockBatchSerialized,
		AccountBatch:              job.AccountBatch,
		CodeBatchPut:              job.CodeBatchPut,
		SmartContractBatch:        job.SmartContractBatch,
		SmartContractStorageBatch: job.SmartContractStorageBatch,
		ReceiptBatchPut:           receiptBatchSerialized,
		TxBatchPut:                txBatchSerialized,
		MapppingBatch:             job.MappingBatch,
		StakeState:                job.StakeBatch,
		TrieDatabaseBatchPut:      job.TrieBatchSnapshot,
		FullDbLogs:                fullDbLogs,
	}

	backupBytes, err := storage.SerializeBackupDb(backupData)
	if err == nil {
		primaryKey := []byte(fmt.Sprintf("block_data_topic-%d", blockNum))
		errPut := bp.storageManager.GetStorageBackupDb().Put(primaryKey, backupBytes)
		if errPut != nil {
			logger.Error("❌ [BACKUP] Failed to persist BackupDb for block #%d: %v", blockNum, errPut)
		} else {
			logger.Debug("✅ [BACKUP] Persisted BackUpDb for block #%d, key=%s, len=%d bytes (took %v)", blockNum, string(primaryKey), len(backupBytes), time.Since(startBackup))
		}
	} else {
		logger.Error("❌ [BACKUP] Failed to serialize BackupDb for block #%d: %v", blockNum, err)
	}
}
