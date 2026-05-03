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
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	stake_state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// commitWorker handles committing and broadcasting blocks after creation
func (bp *BlockProcessor) commitWorker() {
	logger.Info("🚀 [COMMIT] commitWorker loop started")
	logger.Info("✅ Commit Worker initiated")
	for job := range bp.commitChannel {
		if job.Block == nil {
			if job.GlobalExecIndex > 0 {
				bp.updateAndPersistLastGlobalExecIndex(job.GlobalExecIndex)
				bp.updateAndPersistLastHandledCommitIndex(job.CommitIndex)
			}
			if job.DoneChan != nil {
				close(job.DoneChan)
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
		if bp.serviceType == p_common.ServiceTypeMaster && job.ProcessResults != nil {
			for _, tx := range job.ProcessResults.Transactions {
				isCall := tx.IsCallContract()
				isDeploy := tx.IsDeployContract()

				if (isCall || isDeploy) && !tx.GetReadOnly() && tx.GetNonce() != 0 && tx.ToAddress() != utils.GetAddressSelector(p_common.ACCOUNT_SETTING_ADDRESS_SELECT) && tx.ToAddress() != utils.GetAddressSelector(p_common.IDENTIFIER_STAKE) {
					mvmId, exists := job.ProcessResults.MvmIdMap[tx.Hash()]
					if !exists {
						if isCall {
							mvmId = tx.ToAddress()
						}
					}
					mvmAPI := mvm.GetMVMApi(mvmId)
					if mvmAPI != nil {
						mvmRs := mvmAPI.GetExecuteResult()
						if mvmRs != nil {
							if mvmRs.Status == pb.RECEIPT_STATUS_THREW || mvmRs.Status == pb.RECEIPT_STATUS_HALTED {
								mvmAPI.RevertFullDb()
							} else {
								mvmAPI.CommitFullDb()
							}
						}
						// Unprotect the MVM API
						mvm.UnprotectMVMApi(mvmId)
					}
				}
			}
			mvm.RemoveOldApiInstances()
		}

		// ══════════════════════════════════════════════════════════════════
		// CRITICAL FIX: Use centralized CommitBlockState to atomically update ALL
		// chain state components, including blockNumber→hash and tx→blockNumber mappings.
		// Without this, eth_getBlockByNumber returns null for organically produced blocks.
		// ══════════════════════════════════════════════════════════════════
		if _, err := bp.chainState.CommitBlockState(job.Block, blockchain.WithPersistToDB(), blockchain.WithSaveTxMapping()); err != nil {
			logger.Error("commitWorker: CommitBlockState failed for block #%d: %v", blockNum, err)
		}
		saveDuration := time.Since(startSave)

		// CRITICAL CRASH-SAFETY FIX: Update GEI after block save.
		// Ensures block data is safely on disk before GEI advances,
		// preventing the Rust consensus from skipping un-saved blocks after a restart.
		if job.GlobalExecIndex > 0 {
			bp.updateAndPersistLastGlobalExecIndex(job.GlobalExecIndex)
			bp.updateAndPersistLastHandledCommitIndex(job.CommitIndex)
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

		// Auto-update epoch from incoming blocks (critical for late-joining nodes)
		if bp.chainState.CheckAndUpdateEpochFromBlock(header.Epoch(), header.TimeStamp()) {
			logger.Info("🔄 [MASTER] Epoch auto-synced from block #%d to epoch %d",
				header.BlockNumber(), header.Epoch())
		}

		// logger.Debug("✅ [TX COMMIT] Block #%d saved to database successfully: hash=%s, tx_count=%d",
		// 	blockNum, blockHash[:16]+"...", txCount)

		// CRITICAL: Only send to indexingChannel if is_explorer is enabled in config
		// Non-blocking send to prevent blocking commitWorker
		// If indexingChannel is full, skip indexing for this block rather than blocking
		// This ensures commitWorker can continue and send doneChan signal
		if bp.serviceType == p_common.ServiceTypeMaster && bp.storageManager.IsExplorer() {
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
			signingHash := job.Block.Header().HashWithoutSignature()
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
		// BACKUP: Serialize and persist BackupDb is DEFERRED to a background goroutine.
		// This uses a coalescing queue to skip intermediate backups when catching up.
		// ══════════════════════════════════════════════════════════════════
		if bp.serviceType == p_common.ServiceTypeMaster && bp.storageManager.GetStorageBackupDb() != nil {
			select {
			case bp.backupDbChannel <- job:
				// enqueued successfully
			default:
				// queue full, drop oldest and try again
				select {
				case <-bp.backupDbChannel:
				default:
				}
				// push newest
				select {
				case bp.backupDbChannel <- job:
				default:
				}
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

		logger.Debug("[PERF] COMMIT_WORKER: Block %v critical path: %v, txs: %v", blockNum, time.Since(start), txCount)
	}
}


// commitToMemoryParallel performs parallel memory commit operations.
// PIPELINE COMMIT: AccountStateDB and StakeStateDB use CommitPipeline() (fast, releases locks early)
// instead of Commit() (slow, holds locks until BatchPut completes).
// The persist jobs are sent to persistWorker for async LevelDB persistence.
func (bp *BlockProcessor) commitToMemoryParallel(txDB *transaction_state_db.TransactionStateDB, receipts types.Receipts, isStateChanging bool, trieDBSnapshots map[common.Hash]*trie_database.TrieDatabaseSnapshot, blockNumber uint64) {
	overallStart := time.Now()

	// ═══════════════════════════════════════════════════════════════
	// TPS OPTIMIZATION: Run ALL commit tasks in parallel, including
	// CommitPipeline for AccountStateDB and StakeStateDB.
	// Previously CommitPipeline ran sequentially BEFORE the other tasks,
	// adding ~600-900ms of sequential serialization time.
	// Now everything overlaps → wall-clock time = max(all tasks).
	//
	// FORK-SAFETY: CommitPipeline releases muTrie after nodeSet generation,
	// which is safe to run concurrently with SmartContractDB/TrieDB/BlockChain
	// commits because they operate on independent data structures.
	// ═══════════════════════════════════════════════════════════════

	// Will hold the pipeline results for async persistence
	var accountPipelineResult *account_state_db.PipelineCommitResult
	var stakePipelineResult *stake_state_db.StakePipelineCommitResult
	var receiptPipelineResult *types.ReceiptPipelineResult

	type taskResult struct {
		name     string
		err      error
		duration time.Duration
	}

	// Count total tasks: txDB + Receipts + (if stateChanging: AccountPipeline + StakePipeline + TrieDB + BlockChain)
	totalTasks := 2
	if isStateChanging {
		// CRITICAL FIX: SmartContractDB MUST commit sequentially BEFORE AccountStateDB!
		// SmartContractDB.Commit() computes the new StorageRoot for contracts and late-binds
		// them into AccountStateDB. If this runs in parallel with AccountStateDB.CommitPipeline(),
		// a severe race condition occurs causing non-deterministic StateRoots (i.e. cluster forks).
		scStart := time.Now()
		if err := bp.chainState.GetSmartContractDB().Commit(); err != nil {
			logger.Fatal("Sequential SmartContractDB commit error: %v", err)
		}
		logger.Debug("[PERF] SmartContractDB (Sequential): %v", time.Since(scStart))

		totalTasks += 4
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

	if isStateChanging {
		// Launch ALL state-changing commits in parallel
		wg.Add(4)

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

		// BlockChain
		go func() {
			defer wg.Done()
			start := time.Now()
			err := blockchain.GetBlockChainInstance().Commit()
			resultsChan <- taskResult{name: "BlockChain", err: err, duration: time.Since(start)}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results and check for errors
	var maxDuration time.Duration
	var maxTask string
	for result := range resultsChan {
		if result.err != nil {
			logger.Fatal("Parallel commit error (%s): %v", result.name, result.err)
		}
		if result.duration > maxDuration {
			maxDuration = result.duration
			maxTask = result.name
		}
	}

	// Log per-task timing for diagnostics (only for blocks that take noticeable time)
	overallDuration := time.Since(overallStart)
	if overallDuration > 50*time.Millisecond {
		logger.Debug("[PERF] commitToMemoryParallel: %v (bottleneck: %s=%v)",
			overallDuration, maxTask, maxDuration)
	}

	// ═══════════════════════════════════════════════════════════════
	// Send AccountStateDB + StakeStateDB persist jobs to background worker
	// This is non-blocking if persistChannel has capacity
	// ═══════════════════════════════════════════════════════════════
	if accountPipelineResult != nil || stakePipelineResult != nil || receiptPipelineResult != nil {
		select {
		case bp.persistChannel <- PersistJob{
			AccountResult: accountPipelineResult,
			StakeResult:   stakePipelineResult,
			ReceiptResult: receiptPipelineResult,
		}:
			logger.Debug("[PIPELINE] Sent persist job for AccountStateDB + StakeStateDB to background worker")
		default:
			// Channel full — persist synchronously as fallback
			logger.Warn("[PIPELINE] persistChannel full, persisting synchronously")
			if accountPipelineResult != nil {
				if err := bp.chainState.GetAccountStateDB().PersistAsync(accountPipelineResult); err != nil {
					logger.Fatal("Synchronous fallback persist failed for AccountStateDB: %v", err)
				}
			}
			if stakePipelineResult != nil {
				if err := bp.chainState.GetStakeStateDB().PersistAsync(stakePipelineResult); err != nil {
					logger.Fatal("Synchronous fallback persist failed for StakeStateDB: %v", err)
				}
			}
			if receiptPipelineResult != nil {
				if err := receipts.PersistAsync(receiptPipelineResult); err != nil {
					logger.Fatal("Synchronous fallback persist failed for Receipts: %v", err)
				}
			}
		}
	}
}

// persistWorker is a background goroutine that processes PersistJobs sequentially.
// It handles the slow disk I/O (BatchPut to LevelDB) and trie swap that was
// moved out of the critical path by CommitPipeline().
func (bp *BlockProcessor) persistWorker() {
	logger.Info("✅ Persist Worker initiated (pipeline commit background persistence)")
	for job := range bp.persistChannel {
		if job.DoneSignal != nil {
			close(job.DoneSignal)
			continue
		}

		start := time.Now()

		// Persist AccountStateDB trie nodes to LevelDB + swap trie
		if job.AccountResult != nil {
			if err := bp.chainState.GetAccountStateDB().PersistAsync(job.AccountResult); err != nil {
				logger.Error("❌ [PERSIST WORKER] AccountStateDB PersistAsync failed: %v", err)
				// Not fatal — old trie is still valid for reads, just not persisted yet.
				// Next block's IntermediateRoot will still work correctly.
				// The data will be re-generated on next commit.
			}
		}

		// Persist StakeStateDB trie nodes to DB + swap trie
		if job.StakeResult != nil {
			if err := bp.chainState.GetStakeStateDB().PersistAsync(job.StakeResult); err != nil {
				logger.Error("❌ [PERSIST WORKER] StakeStateDB PersistAsync failed: %v", err)
				// Not fatal — same resilience as AccountStateDB
			}
		}

		// Persist Receipts trie nodes to DB
		if job.ReceiptResult != nil {
			// Actually we need the receipts object, but it is created per block.
			// Let's instantiate a temporary Receipts wrapper or access `storageManager.GetStorageReceipt()`.
			// Since we just need to BatchPut, we can get storage explicitly or use an empty wrapper.
			if len(job.ReceiptResult.Batch) > 0 {
				err := bp.storageManager.GetStorageReceipt().BatchPut(job.ReceiptResult.Batch)
				if err != nil {
					logger.Error("❌ [PERSIST WORKER] Receipts BatchPut failed: %v", err)
				}
			}
		}

		elapsed := time.Since(start)
		if elapsed > 10*time.Millisecond {
			logger.Debug("[PERF] PersistWorker: async persist completed in %v", elapsed)
		}
	}
}

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
	if job.ProcessResults != nil && len(job.ProcessResults.Receipts) > 0 {
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
		FullDbLogs:                nil, 
	}

	backupBytes, err := storage.SerializeBackupDb(backupData)
	if err == nil {
		primaryKey := []byte(fmt.Sprintf("block_data_topic-%d", blockNum))
		errPut := bp.storageManager.GetStorageBackupDb().Put(primaryKey, backupBytes)
		if errPut != nil {
			logger.Error("❌ [BACKUP] Failed to persist BackupDb for block #%d: %v", blockNum, errPut)
		} else {
			logger.Info("✅ [BACKUP] Persisted BackUpDb for block #%d, key=%s, len=%d bytes (took %v)", blockNum, string(primaryKey), len(backupBytes), time.Since(startBackup))
		}
	} else {
		logger.Error("❌ [BACKUP] Failed to serialize BackupDb for block #%d: %v", blockNum, err)
	}
}
