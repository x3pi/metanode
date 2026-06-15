// @title processor/block_processor_network.go
// @markdown processor/block_processor_network.go - Network communication and socket handling
package processor

import (

	"fmt"
	"time"

	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"

	"github.com/meta-node-blockchain/meta-node/pkg/fatal"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/types"

)

// runUnixSocket starts the Unix socket for Rust queries / or FFI Bridge
func (bp *BlockProcessor) runUnixSocket() {
	logger.Info("🔌 [FFI BRIDGE] Initializing Rust MetaNode Consensus via FFI")

	// 1. Create the RequestHandler (was previously inside NewSocketExecutor)
	reqHandler := executor.NewRequestHandler(bp.storageManager, bp.chainState, bp.genesisPath)

	// Wire up snapshot manager to request handler for epoch transition notifications
	if sm := executor.GetGlobalSnapshotManager(); sm != nil {
		reqHandler.SetSnapshotManager(sm)
		logger.Info("📸 [SNAPSHOT] SnapshotManager wired into RequestHandler")
	}

	// Wire up network components for Master→Sub broadcast
	if bp.connectionsManager != nil && bp.messageSender != nil {
		reqHandler.SetNetworkComponents(bp.connectionsManager, bp.messageSender)
		logger.Info("📡 [SYNC→SUB] NetworkComponents wired into RequestHandler (Master push enabled)")
	}

	// Inject ForceCommit callback for Event-Driven Block Generation
	reqHandler.SetForceCommitCallback(func() {
		logger.Info("⚡ [RUST TRIGGER] Rust triggered ForceCommit! Generating block immediately.")
		bp.ForceCommit()
	})

	// Inject UpdateLastBlock callback for architectural purity (Rust manages state)
	reqHandler.SetUpdateLastBlockCallback(func(blk types.Block) {
		bp.UpdateLastBlockAndHeader(blk)
		logger.Info("🔄 [RUST CONTROL] Rust explicitly advanced Go Master memory to block #%d", blk.Header().BlockNumber())
	})

	// Inject PushAsyncGEIUpdate callback to prevent empty commit stalls
	reqHandler.SetPushAsyncGEIUpdateCallback(bp.PushAsyncGEIUpdate)

	// Inject ResetCommitIndex callback to prevent epoch-transition execution skips
	reqHandler.SetResetCommitIndexCallback(func(newEpoch uint64) {
		GetGEIAuthority().ResetCommitIndexForEpoch(newEpoch)
	})

	// Inject ExecutionMutex Lock and Unlock callbacks to serialize sync operations with consensus execution
	reqHandler.SetExecutionLockCallbacks(bp.ExecutionMutex.Lock, bp.ExecutionMutex.Unlock)

	// Inject SetBroadcastEventsAndReceiptsCallback for SyncOnly transaction receipt delivery
	reqHandler.SetBroadcastEventsAndReceiptsCallback(func(blk types.Block, receipts []types.Receipt, eventLogs []types.EventLog) {
		bp.broadcastEventsAndReceipts(blk, receipts, eventLogs)
	})

	// 2. Create the block ingestion channel (was listener.DataChannel())
	// In the legacy setup, processRustEpochData reads from this channel
	blockQueue := make(chan *pb.ExecutableBlock, 5000)

	// 3. Find Rust configuration path
	rustConfigPath := bp.config.RustConfigPath
	if rustConfigPath == "" {
		// Fallback for easy local testing if not in config
		logger.Warn("RustConfigPath not specified in config! Using default: ../consensus/metanode/config/node-0.toml")
		rustConfigPath = "../consensus/metanode/config/node-0.toml"
	}

	// 4. Initialize FFI Bridge
	dataDir := bp.config.Databases.RootPath
	executor.RegisterTraceCallback(tx_processor.GlobalTxTraceStore.UpdateTrace)
	if err := executor.InitFFIBridge(rustConfigPath, dataDir, reqHandler, blockQueue); err != nil {
		logger.Error("❌ [FFI BRIDGE] Error starting FFI Bridge: %v", err)
		fatal.Exit("Fatal exit from block_processor_network.go")
	}

	logger.Info("✅ [FFI BRIDGE] MetaNode Consensus initialized via FFI")

	// Log readiness — executor is now accepting blocks via FFI
	lastBlock := storage.GetLastBlockNumber()
	fmt.Printf("✅ [READY] Go Master executor initialized via FFI: block=%d\n", lastBlock)

	logger.Info("Main program waiting for data from FFI Module...")

	// 5. Start block processing loop asynchronously using the channel
	go bp.processRustEpochData(blockQueue)

	// The runUnixSocket caller expects this function to block/run in background
	// We can simply return here, or keep it alive like it did. It's normally run as a goroutine.
	// Since the previous function had a block wait, we can mimic it or just let it exit.
	// The processRustEpochData is backgrounded now.
	for {
		time.Sleep(30 * time.Second)
		logger.Debug("🔌 [FFI BRIDGE] Main thread monitoring FFI alive")
	}
}

// runSocketExecutor starts the socket executor for Rust communication
func (bp *BlockProcessor) runSocketExecutor(path string) {
	time.Sleep(5 * time.Second)

	// 1. Initialize listener from module
	listener := executor.NewListener(path)

	// 2. Start listening (non-blocking)
	if err := listener.Start(); err != nil {
		logger.Error("Could not start listener: %v", err)
		fatal.Exit("Fatal exit from block_processor_network.go")
	}

	// Create a goroutine to handle safe program shutdown
	handleShutdown(listener)

	// Log readiness — executor socket is now accepting Rust connections
	lastBlock := storage.GetLastBlockNumber()
	fmt.Printf("✅ [READY] Go Master executor socket listening: path=%s, block=%d", path, lastBlock)

	// 3. Listen for data from listener channel
	logger.Info("Main program waiting for data from module Listener...")
	dataChan := listener.DataChannel()

	bp.processRustEpochData(dataChan)
}

// processRustEpochData processes epoch data from Rust
func (bp *BlockProcessor) processRustEpochData(dataChan <-chan *pb.ExecutableBlock) {
	// CRITICAL FORK-SAFETY: Buffering mechanism to ensure blocks are processed in order
	// All nodes must execute blocks with the same global_exec_index in the same order
	pendingBlocks := make(map[uint64]*pb.ExecutableBlock) // Map: global_exec_index -> ExecutableBlock

	// CRITICAL: Retention policy for lagging nodes
	// Store skipped commits (with transactions) temporarily to allow processing if they arrive late
	// This handles the case where a node is lagging and sends commits out-of-order
	// Retention: Keep skipped commits for up to MAX_SKIPPED_COMMITS_RETENTION commits
	const MAX_SKIPPED_COMMITS_RETENTION = MaxSkippedCommitsRetention
	skippedCommitsWithTxs := make(map[uint64]*pb.ExecutableBlock) // Map: global_exec_index -> ExecutableBlock (only commits with transactions)

	// CRITICAL: Initialize nextExpectedGlobalExecIndex from LastGlobalExecIndex
	// After restart, Go Master must continue from where it left off
	// GlobalExecIndex is now decoupled from blockNumber (empty commits are skipped)
	var nextExpectedGlobalExecIndex uint64
	var currentBlockNumber uint64 // Track current Go block number (sequential, only increments for non-empty commits)

	// First, try to restore from LastGlobalExecIndex (decoupled tracking)
	lastGEI := storage.GetLastGlobalExecIndex()
	if lastGEI > 0 {
		nextExpectedGlobalExecIndex = lastGEI + 1
		lastBlock := bp.GetLastBlock()
		if lastBlock != nil {
			currentBlockNumber = lastBlock.Header().BlockNumber()
		} else {
			currentBlockNumber = storage.GetLastBlockNumber()
		}
		fmt.Printf("📊 [FORK-SAFETY] Initialized from LastGlobalExecIndex: lastGEI=%d, nextExpected=%d, lastBlockNumber=%d",
			lastGEI, nextExpectedGlobalExecIndex, currentBlockNumber)
	} else {
		// Fallback: legacy mode where blockNumber == globalExecIndex
		lastBlockFromDB := bp.GetLastBlock()
		if lastBlockFromDB != nil {
			lastBlockNumber := lastBlockFromDB.Header().BlockNumber()
			nextExpectedGlobalExecIndex = lastBlockNumber + 1
			currentBlockNumber = lastBlockNumber
			logger.Info("📊 [FORK-SAFETY] Initialized from last block (legacy): lastBlockNumber=%d, nextExpected=%d",
				lastBlockNumber, nextExpectedGlobalExecIndex)
		} else {
			lastBlockNumberFromDB := storage.GetLastBlockNumber()
			if lastBlockNumberFromDB > 0 {
				nextExpectedGlobalExecIndex = lastBlockNumberFromDB + 1
				currentBlockNumber = lastBlockNumberFromDB
				logger.Info("📊 [FORK-SAFETY] Initialized from DB (legacy): lastBlockNumber=%d, nextExpected=%d",
					lastBlockNumberFromDB, nextExpectedGlobalExecIndex)
			} else {
				nextExpectedGlobalExecIndex = uint64(1)
				currentBlockNumber = uint64(0)
				logger.Info("📊 [FORK-SAFETY] Initialized to 1 (new network, no blocks yet)")
			}
		}
	}

	// Start timeout monitor goroutine
	// CRITICAL: Monitor removed by user request to simplify flow
	// go bp.monitorBlockReceiveTimeout(...)

	// MEMORY FIX: Create FileLogger once (not per-epoch-data) to avoid leaking os.File handles
	epochFileLogger, _ := loggerfile.NewFileLogger(fmt.Sprintf("runSocketExecutor_" + ".log"))

	fmt.Printf("🎧 [PROCESSOR] Starting loop to read from dataChan and authQueue (hybrid: sync+5s timeout)...")
	authQueue := executor.GetAuthoritativeBlockQueue()

	// STALL DETECTOR (May 2026): Non-blocking heartbeat to log diagnostic state
	// when no blocks are processed for 15 seconds. This provides visibility
	// into pipeline stalls without breaking any safety invariants.
	stallTicker := time.NewTicker(15 * time.Second)
	defer stallTicker.Stop()
	lastProcessedTime := time.Now()
	var lastStallLogGEI uint64 // Track GEI at last stall-detect tick to detect DB-sync progress (SyncOnly nodes)

PROCESS_LOOP:
	for {
		var epochData *pb.ExecutableBlock
		var authRespCh chan<- *pb.ExecuteBlockResponse

		select {
		case req, ok := <-authQueue:
			if !ok {
				logger.Warn("Authoritative queue closed")
				break PROCESS_LOOP
			}
			epochData = req.Block
			authRespCh = req.ResponseCh
		case data, ok := <-dataChan:
			if !ok {
				logger.Info("Data channel closed")
				break PROCESS_LOOP
			}
			epochData = data
		case <-stallTicker.C:
			// STALL DETECTOR: Log diagnostic state when no blocks are being processed
			stallDuration := time.Since(lastProcessedTime)
			if stallDuration > 15*time.Second {
				// FIX (Jun 2026): SyncOnly nodes receive blocks via HandleSyncBlocksRequest→DB,
				// NOT via dataChan. When GEI advances through syncLocalStateWithDB, dataChan
				// being empty is EXPECTED — not a DIGEST-GATE stall.
				// Check whether GEI is advancing before emitting the alarming WARN log.
				geiAdvancing := nextExpectedGlobalExecIndex > lastStallLogGEI && lastStallLogGEI > 0
				if geiAdvancing {
					logger.Debug("🔄 [STALL-DETECT] dataChan idle but GEI advancing via DB sync (%d→%d). "+
						"SyncOnly node operating normally. dataChan=%d/%d, currentBlock=%d",
						lastStallLogGEI, nextExpectedGlobalExecIndex,
						len(dataChan), cap(dataChan), currentBlockNumber)
				} else {
					// Enhanced diagnostics: full pipeline state
					pendingTxCount := bp.transactionProcessor.pendingTxManager.Count()
					poolSize := bp.transactionProcessor.transactionPool.CountTransactions()
					authQueueDepth := len(authQueue)
					authQueueCap := cap(authQueue)

					logger.Warn("🔒 [STALL-DETECT] No blocks processed for %v. "+
						"dataChan=%d/%d, authQueue=%d/%d, commitCh=%d/%d, snapshotGate=%v, "+
						"nextExpectedGEI=%d, currentBlock=%d, "+
						"pendingTx=%d, poolSize=%d. "+
						"Investigate: Rust CommitProcessor may be stuck in DIGEST-GATE.",
						stallDuration,
						len(dataChan), cap(dataChan),
						authQueueDepth, authQueueCap,
						len(bp.commitChannel), cap(bp.commitChannel),
						bp.snapshotGateOpen.Load(),
						nextExpectedGlobalExecIndex, currentBlockNumber,
						pendingTxCount, poolSize)
				}
				lastStallLogGEI = nextExpectedGlobalExecIndex
			}
			bp.syncLocalStateWithDB(&nextExpectedGlobalExecIndex, &currentBlockNumber)
			continue
		}

		// Self-monitoring queue and processing rate
		bp.processedBlockCount++
		if bp.processedBlockCount%100 == 0 {
			elapsed := time.Since(bp.lastRateCheckTime)
			if elapsed > 10*time.Second {
				rate := float64(bp.processedBlockCount-bp.lastRateCheckCount) / elapsed.Seconds()
				queueLen := len(dataChan)

				if queueLen > 40000 { // 80% of 50K dataChan
					logger.Error("🚨 [SELF-MONITOR] Processing rate=%.1f blk/s, queue=%d/50000 (%.0f%% full). Go is falling behind!",
						rate, queueLen, float64(queueLen)/50000*100)
				} else if queueLen > 25000 { // 50% of 50K dataChan
					logger.Warn("⚠️ [SELF-MONITOR] Processing rate=%.1f blk/s, queue=%d/50000 (%.0f%% full). Monitor closely.",
						rate, queueLen, float64(queueLen)/50000*100)
				}

				bp.lastRateCheckTime = time.Now()
				bp.lastRateCheckCount = bp.processedBlockCount
			}
		}

		// 🔍 DIAGNOSTIC: Log EVERY block received from Rust
		incomingTxCount := len(epochData.Transactions)
		incomingGEI := epochData.GetGlobalExecIndex()
		if incomingTxCount > 0 {
			logger.Info("📥 [DIAG-RECV] Block from Rust: GEI=%d, txs=%d, epoch=%d, nextExpected=%d, currentBlock=%d",
				incomingGEI, incomingTxCount, epochData.GetEpoch(), nextExpectedGlobalExecIndex, currentBlockNumber)

			// 🔍 DIAGNOSTIC: Log TX order from Rust
			for i, txData := range epochData.Transactions {
				if i < 5 || i == incomingTxCount-1 { // Log 5 first and 1 last tx to avoid spam
					txHash := txData.GetDigest()
					hashStr := fmt.Sprintf("%x", txHash)
					if len(hashStr) > 16 {
						hashStr = hashStr[:16] + "..."
					}
					logger.Info("  |_ [RUST-TX-ORDER] GEI=%d, pos[%d/%d]: hash=%s, worker=%d",
						incomingGEI, i, incomingTxCount-1, hashStr, txData.GetWorkerId())
				}
			}
		}

		// NOTE: Network Sync cancellation logic removed (Feb 2026)
		// Rust P2P now handles block sync for SyncOnly nodes via rust_sync_node.rs

		// ═══════════════════════════════════════════════════════════════════
		// TRANSITION SYNC: Go's internal state may advance during epoch
		// transitions (SyncBlocksRequest updates DB). Detect if the DB's
		// lastGEI has advanced beyond our in-memory nextExpected, and
		// sync up. This prevents stale nextExpected after a DB fast-forward.
		// ═══════════════════════════════════════════════════════════════════
		bp.syncLocalStateWithDB(&nextExpectedGlobalExecIndex, &currentBlockNumber)

		// ═══════════════════════════════════════════════════════════════════
		// GEI BACKWARD GAP DIAGNOSTIC: In Phase-B architecture, Rust reads
		// GEI from Go's authoritative state. If incoming GEI is much lower
		// than nextExpected, it means Rust is replaying historical commits.
		// The GEI-REGRESSION guard in processSingleEpochData will skip them.
		// We log a diagnostic warning but do NOT set any bypass flags.
		// ═══════════════════════════════════════════════════════════════════
		if incomingGEI < nextExpectedGlobalExecIndex && incomingGEI > 0 {
			geiBackwardGap := nextExpectedGlobalExecIndex - incomingGEI
			if geiBackwardGap > 50 || incomingTxCount > 0 {
				logger.Warn("🔍 [GEI-BACKWARD] Incoming GEI=%d << nextExpected=%d (gap=%d, txs=%d). "+
					"Rust is replaying historical commits — GEI-REGRESSION guard will skip stale ones.",
					incomingGEI, nextExpectedGlobalExecIndex, geiBackwardGap, incomingTxCount)
			}
		}

		// ═══════════════════════════════════════════════════════════════════
		// BATCH-DRAIN OPTIMIZATION: Process consecutive empty commits in bulk
		// During catch-up, ~95%+ commits are empty (0 transactions). Instead of
		// processing each one individually (3 I/O ops each), we batch-drain all
		// queued empty commits and only persist the final GEI once.
		// This gives 100-10000x speedup during catch-up sync.
		// FORK-SAFETY: Only empty commits are batched. Any commit with TXs
		// goes through the full processing path unchanged.
		// BATCH-DRAIN OPTIMIZATION is skipped for authoritative commits (they require responses)
		// ═══════════════════════════════════════════════════════════════════
		if authRespCh == nil && bp.isEmptyCommit(epochData) && epochData.GetGlobalExecIndex() == nextExpectedGlobalExecIndex {
			batchCount := uint64(1)
			highestGEI := epochData.GetGlobalExecIndex()
			highestCommitIndex := epochData.GetCommitIndex()

			// Check epoch from commit
			lastEpochNum := epochData.GetEpoch()
			lastCommitTimestampMs := epochData.GetCommitTimestampMs()

			// Drain additional consecutive empty commits from channel buffer
			// OPTIMIZATION: Use timed drain window (5ms) instead of instant default exit.
			// Between Rust send bursts (~50-200ms), the channel goes empty briefly.
			// Waiting 5ms lets it refill, creating much larger batches (e.g., 2000
			// commits in 1 batch instead of 200 batches of 10). This reduces TX
			// processing latency from ~2 minutes to seconds.
			// FORK-SAFETY: TX-containing blocks always break immediately (line 235 case).
			// Empty commits only update GEI counter (idempotent on crash).
			draining := true
			drainTimeout := time.NewTimer(5 * time.Millisecond)
			for draining {
				select {
				case next, ok := <-dataChan:
					if !ok {
						draining = false
						break
					}
					nextGEI := next.GetGlobalExecIndex()
					if bp.isEmptyCommit(next) && nextGEI == highestGEI+1 {
						// ═══════════════════════════════════════════════════════════════
						// FORK-SAFETY FIX (May 2026): Epoch-boundary commits MUST NOT
						// be absorbed into the batch! The 5ms drain timer is non-
						// deterministic: different nodes batch different numbers of
						// commits depending on CPU/network timing. If an epoch-boundary
						// commit is absorbed by one node but not another, they produce
						// blocks with different epoch/GEI/leader at the same block
						// number → FORK. (Root cause of Block #530 fork in spam_xapian)
						//
						// FIX: Check epoch BEFORE absorbing. If epoch changes, treat
						// the commit as "non-empty" to force it through the full
						// processSingleEpochData path (boundary block, commitIndex
						// reset, etc.).
						// ═══════════════════════════════════════════════════════════════
						nextEpoch := next.GetEpoch()
						if nextEpoch > lastEpochNum {
							logger.Info("ð¡ï¸ [BATCH-DRAIN] Epoch boundary detected in batch! epoch %d→%d at GEI=%d. "+
								"Breaking drain to process epoch transition through full path.",
								lastEpochNum, nextEpoch, nextGEI)
							// Stop draining — treat this as a non-empty commit
							draining = false
							// Persist the batch up to this point (BEFORE the epoch boundary)
							bp.updateAndPersistConsensusState(highestGEI, highestCommitIndex, lastEpochNum)
							nextExpectedGlobalExecIndex = highestGEI + 1
							if batchCount > 1 {
								logger.Info("⚡ [BATCH-DRAIN] Processed %d consecutive empty commits in 1 batch (GEI %d→%d) before epoch boundary",
									batchCount, highestGEI-batchCount+1, highestGEI)
							}
							// Process the epoch-boundary commit through full path
							blockStart := time.Now()
							bp.ExecutionMutex.RLock()
							if err := bp.processSingleEpochData(next, &nextExpectedGlobalExecIndex, &currentBlockNumber, pendingBlocks, skippedCommitsWithTxs, epochFileLogger); err != nil {
								logger.Error("❌ [PROCESSOR] processSingleEpochData failed for epoch boundary block: %v", err)
							}
							bp.ExecutionMutex.RUnlock()
							lastProcessedTime = time.Now()
							if blockDur := time.Since(blockStart); blockDur > 500*time.Millisecond {
								logger.Warn("ð [SLOW-BLOCK] Epoch boundary block processing took %v (GEI=%d)", blockDur, nextGEI)
							}
							drainTimeout.Stop()
							goto BATCH_DONE
						}
						// Same-epoch empty commit — safe to absorb into batch
						highestGEI = nextGEI
						highestCommitIndex = next.GetCommitIndex()
						batchCount++
						// Reset drain timer — more data might follow
						if !drainTimeout.Stop() {
							select {
							case <-drainTimeout.C:
							default:
							}
						}
						drainTimeout.Reset(5 * time.Millisecond)
					} else {
						// Non-empty or non-consecutive — stop draining, process this batch,
						// then handle this commit normally below
						draining = false
						// Process the batch first
						// GO-AUTHORITATIVE FIX: Route through GEIAuthority so all paths
						// use the same atomic counter. Previously used Rust's hint GEI
						// directly, which could diverge from GEIAuthority's counter.
						bp.updateAndPersistConsensusState(highestGEI, highestCommitIndex, lastEpochNum)
						nextExpectedGlobalExecIndex = highestGEI + 1
						bp.updateAndPersistLastExecutedCommitHash(next.GetCommitHash())
						if lastEpochNum > 0 {
							bp.chainState.CheckAndUpdateEpochFromBlock(lastEpochNum, lastCommitTimestampMs)
						}
						if batchCount > 1 {
							logger.Info("⚡ [BATCH-DRAIN] Processed %d consecutive empty commits in 1 batch (GEI %d→%d)",
								batchCount, highestGEI-batchCount+1, highestGEI)
						}
						// Now process the non-empty/non-consecutive commit
						logger.Info("📥 [PROCESSOR] Read block from dataChan: global_exec_index=%d", nextGEI)

						blockStart := time.Now()
						bp.ExecutionMutex.RLock()
						if err := bp.processSingleEpochData(next, &nextExpectedGlobalExecIndex, &currentBlockNumber, pendingBlocks, skippedCommitsWithTxs, epochFileLogger); err != nil {
							logger.Error("❌ [PROCESSOR] processSingleEpochData failed for next block from channel: %v", err)
						}
						bp.ExecutionMutex.RUnlock()
						lastProcessedTime = time.Now()
						if blockDur := time.Since(blockStart); blockDur > 500*time.Millisecond {
							logger.Warn("🐌 [SLOW-BLOCK] Block processing took %v (GEI=%d, txs=%d)",
								blockDur, nextGEI, len(next.Transactions))
						}

						drainTimeout.Stop()
						goto BATCH_DONE
					}
				case <-drainTimeout.C:
					// Channel empty for 5ms — finalize this batch
					draining = false
				}
			}
			drainTimeout.Stop()

			// Persist only the final GEI and CommitIndex (1 atomic DB write for entire batch)
			// GO-AUTHORITATIVE FIX: Route through GEIAuthority so all paths
			// use the same atomic counter, preventing +1 offset divergence.
			bp.updateAndPersistConsensusState(highestGEI, highestCommitIndex, lastEpochNum)
			nextExpectedGlobalExecIndex = highestGEI + 1
			if lastEpochNum > 0 {
				bp.chainState.CheckAndUpdateEpochFromBlock(lastEpochNum, lastCommitTimestampMs)
			}
			if batchCount > 1 {
				logger.Info("⚡ [BATCH-DRAIN] Processed %d consecutive empty commits in 1 batch (GEI %d→%d)",
					batchCount, highestGEI-batchCount+1, highestGEI)
			}

			// Check pending blocks after batch drain
			if pendingBlock, exists := pendingBlocks[nextExpectedGlobalExecIndex]; exists {
				delete(pendingBlocks, nextExpectedGlobalExecIndex)
				blockStart := time.Now()
				bp.ExecutionMutex.RLock()
				if err := bp.processSingleEpochData(pendingBlock, &nextExpectedGlobalExecIndex, &currentBlockNumber, pendingBlocks, skippedCommitsWithTxs, epochFileLogger); err != nil {
					logger.Error("❌ [PROCESSOR] processSingleEpochData failed for pendingBlock: %v", err)
				}
				bp.ExecutionMutex.RUnlock()
				lastProcessedTime = time.Now()
				if blockDur := time.Since(blockStart); blockDur > 500*time.Millisecond {
					logger.Warn("🐌 [SLOW-BLOCK] Pending block processing took %v", blockDur)
				}
			}
		BATCH_DONE:
			continue
		} else {
			// Non-empty commit, authoritative, or non-sequential — full processing path
			logger.Info("📥 [PROCESSOR] Read block from channel: global_exec_index=%d, auth=%v", epochData.GetGlobalExecIndex(), authRespCh != nil)

			// ═══════════════════════════════════════════════════════════════
			// BOUNDED CONCURRENCY GUARD: Prevent unbounded map growth.
			// If a permanent GEI gap causes pendingBlocks to grow forever,
			// this cap prevents OOM. The cleared entries will be re-sent
			// by Rust on the next retry cycle.
			// ═══════════════════════════════════════════════════════════════
			if len(pendingBlocks) > 1000 {
				logger.Error("🚨 [BOUNDED] pendingBlocks overflow (%d entries) — clearing to prevent OOM. "+
					"Possible permanent GEI gap. Rust will retry.", len(pendingBlocks))
				pendingBlocks = make(map[uint64]*pb.ExecutableBlock)
			}
			if len(skippedCommitsWithTxs) > 500 {
				logger.Error("🚨 [BOUNDED] skippedCommitsWithTxs overflow (%d entries) — clearing to prevent OOM.",
					len(skippedCommitsWithTxs))
				skippedCommitsWithTxs = make(map[uint64]*pb.ExecutableBlock)
			}

			blockStart := time.Now()
			bp.ExecutionMutex.RLock()
			err := bp.processSingleEpochData(epochData, &nextExpectedGlobalExecIndex, &currentBlockNumber, pendingBlocks, skippedCommitsWithTxs, epochFileLogger)
			bp.ExecutionMutex.RUnlock()
			lastProcessedTime = time.Now()
			if blockDur := time.Since(blockStart); blockDur > 500*time.Millisecond {
				logger.Warn("🐌 [SLOW-BLOCK] Block processing took %v (GEI=%d, txs=%d)",
					blockDur, epochData.GetGlobalExecIndex(), len(epochData.Transactions))
			}

			// GO-AUTHORITATIVE GEI: Send Response
			if authRespCh != nil {
				if err != nil {
					logger.Error("❌ [PROCESSOR] Authoritative block execution FAILED: %v", err)
					authRespCh <- &pb.ExecuteBlockResponse{
						Success:      false,
						Error:        err.Error(),
						ActualGei:    incomingGEI,
						BlockNumber:  currentBlockNumber,
						GeisConsumed: 0,
					}
				} else {
					// The GEI authority has already advanced nextExpectedGlobalExecIndex inside processSingleEpochData
					assignedGei := nextExpectedGlobalExecIndex - 1
					authRespCh <- &pb.ExecuteBlockResponse{
						Success:      true,
						ActualGei:    assignedGei,
						BlockNumber:  currentBlockNumber,
						GeisConsumed: 1,
					}
				}
			}
		}
	}

	// Channel has been closed - can happen when:
	// 1. Rust MetaNode crashed or restarted
	// 2. This node was demoted from Validator to SyncOnly (Rust stopped consensus authority)
	// 3. Connection was lost unexpectedly

	// ═══════════════════════════════════════════════════════════════════════════
	// TRANSITION GUARD: Ensure all pending state is fully committed to DB
	// before P2P sync starts writing blocks. This prevents a race where:
	// - Go executor has blocks in memory (bp.lastBlock) but not yet in DB
	// - P2P sync starts writing blocks from peers with different parent hashes
	// - Result: fork between consensus-generated and P2P-synced blocks
	// ═══════════════════════════════════════════════════════════════════════════
	logger.Warn("🔄 [TRANSITION] dataChan closed! Last processed block: #%d, nextExpectedGlobalExecIndex=%d",
		currentBlockNumber, nextExpectedGlobalExecIndex)

	// Flush: Ensure the last block in memory is committed to DB
	// Uses centralized CommitBlockState to update ALL state components atomically
	lastBlock := bp.GetLastBlock()
	if lastBlock != nil {
		lastBlockNum := lastBlock.Header().BlockNumber()
		lastCommittedNum := storage.GetLastBlockNumber()

		if lastBlockNum > lastCommittedNum {
			logger.Warn("🔄 [TRANSITION GUARD] Flushing uncommitted block #%d to DB (last committed: #%d)",
				lastBlockNum, lastCommittedNum)

			// Write changelog synchronously BEFORE CommitBlockState to guarantee sequential progression
			// and visibility of historical states when block counter is advanced.
			if bp.pendingAccountPayload != nil {
				if payload, ok := bp.pendingAccountPayload.(interface{ WriteChangelog() }); ok {
					payload.WriteChangelog()
				}
			}
			if bp.pendingStakePayload != nil {
				if payload, ok := bp.pendingStakePayload.(interface{ WriteChangelog() }); ok {
					payload.WriteChangelog()
				}
			}

			if _, err := bp.chainState.CommitBlockState(lastBlock,
				blockchain.WithPersistToDB(),
				blockchain.WithCommitMappings(),
			); err != nil {
				logger.Error("🔄 [TRANSITION GUARD] Failed to commit block #%d: %v", lastBlockNum, err)
			} else {
				// Flush NOMT payloads asynchronously now that the block is safely written to block database (PebbleDB)
				if bp.pendingAccountPayload != nil {
					if payload, ok := bp.pendingAccountPayload.(interface{ CommitAsync() }); ok {
						payload.CommitAsync()
					}
					bp.pendingAccountPayload = nil
				}
				if bp.pendingStakePayload != nil {
					if payload, ok := bp.pendingStakePayload.(interface{ CommitAsync() }); ok {
						payload.CommitAsync()
					}
					bp.pendingStakePayload = nil
				}
				logger.Info("✅ [TRANSITION GUARD] Flushed block #%d to DB", lastBlockNum)
			}
		} else {
			logger.Info("✅ [TRANSITION GUARD] All blocks already committed (last_block=#%d, last_committed=#%d)",
				lastBlockNum, lastCommittedNum)
		}
	}

	// NOTE: Network Sync for SyncOnly nodes is now handled by Rust P2P (Feb 2026)
	// Rust will continue fetching blocks via RustSyncNode.fetch_from_peers()
	// No action needed here - just log and return
	logger.Info("🦀 [RUST P2P] Go network sync disabled - Rust handles block sync for SyncOnly nodes")
}

// syncLocalStateWithDB synchronizes Go's internal state (nextExpectedGlobalExecIndex and currentBlockNumber)
// with the database. This is critical when P2P Sync or Rust writes blocks to DB directly,
// bypassing the Go Master consensus processor loop (e.g. during dynamic catch-up sync).
func (bp *BlockProcessor) syncLocalStateWithDB(nextExpectedGlobalExecIndex *uint64, currentBlockNumber *uint64) {
	if storage.IsPreConsensusSyncActive() {
		return
	}
	actualLastGEI := storage.GetLastGlobalExecIndex()

	// CRITICAL FIX: Actually advance local state when DB is ahead
	if actualLastGEI > 0 && actualLastGEI >= *nextExpectedGlobalExecIndex {
		bp.chainState.LockCommit()
		defer bp.chainState.UnlockCommit()

		oldNextExpected := *nextExpectedGlobalExecIndex
		*nextExpectedGlobalExecIndex = actualLastGEI + 1

		// WE MUST ALSO ADVANCE currentBlockNumber !!!
		actualLastBlockDB := storage.GetLastBlockNumber()
		if actualLastBlockDB > 0 && actualLastBlockDB > *currentBlockNumber {
			oldBlockNumber := *currentBlockNumber
			*currentBlockNumber = actualLastBlockDB
			logger.Info("🔄 [TRANSITION SYNC] Advanced block number from DB: %d → %d",
				oldBlockNumber, *currentBlockNumber)

			// Fetch the fresh block from DB matching the new tip block number to update in-memory tip block
			bc := blockchain.GetBlockChainInstance()
			if bc != nil {
				if blockHash, ok := bc.GetBlockHashByNumber(actualLastBlockDB); ok {
					if freshBlock, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash); err == nil && freshBlock != nil {
						// Atomically update bp.lastBlock and header in memory
						bp.SetLastBlock(freshBlock)
						bp.nextBlockNumber.Store(actualLastBlockDB + 1)
						headerCopy := freshBlock.Header()
						bp.chainState.SetcurrentBlockHeader(&headerCopy)

						logger.Info("🔄 [TRANSITION SYNC] Updated bp.lastBlock in memory to #%d", actualLastBlockDB)

						// ═══════════════════════════════════════════════════════════
						// NOMT TRIE RE-ALIGNMENT: Since the DB fast-forwarded via
						// SyncBlocksRequest, the in-memory NomtStateTrie may still
						// hold the old state root. We MUST re-align it to the new
						// block's header before processing the next consensus block.
						// ═══════════════════════════════════════════════════════════
						if trie.GetStateBackend() == trie.BackendNOMT {
							logger.Info("🔧 [TRANSITION SYNC] Forcing NOMT trie re-alignment to block #%d (GEI=%d)",
								actualLastBlockDB, actualLastGEI)
							if err := bp.chainState.UpdateStateForNewHeaderUnlocked(freshBlock.Header()); err != nil {
								logger.Error("❌ [TRANSITION SYNC] Failed to re-align NOMT trie: %v", err)
							}
						}

						// CRITICAL C++ EVM CACHE INVALIDATION:
						// Since sync directly updated the LevelDB/NOMT database, we must clear/reset internal memory caches.
						bp.chainState.InvalidateAllState()
						mvm.ClearAllMVMApi()
						mvm.ClearAllProtectedMVMApi()
						mvm.CallClearAllStateInstances()
						trie_database.GetTrieDatabaseManager().ClearAllTrieDatabases()
					} else {
						logger.Error("❌ [TRANSITION SYNC] Failed to load fresh block #%d from DB: %v", actualLastBlockDB, err)
					}
				} else {
					logger.Error("❌ [TRANSITION SYNC] Failed to get block hash for block #%d from DB", actualLastBlockDB)
				}
			}
		}

		if oldNextExpected != *nextExpectedGlobalExecIndex {
			logger.Info("🔄 [TRANSITION SYNC] Advanced local state from DB: nextExpected %d → %d",
				oldNextExpected, *nextExpectedGlobalExecIndex)
		}
	}
}

// monitorBlockReceiveTimeout monitors for block receive timeouts
// monitorBlockReceiveTimeout removed per user request

// handleShutdown handles safe shutdown of the listener
func handleShutdown(listener *executor.Listener) {
	// This function is kept for backward compatibility
	// The actual shutdown handling should be implemented in the main application
}

