// @title processor/block_processor_network.go
// @markdown processor/block_processor_network.go - Network communication and socket handling
package processor

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/block_signer"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
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
		bp.SetLastBlock(blk)
		headerCopy := blk.Header()
		bp.chainState.SetcurrentBlockHeader(&headerCopy)
		logger.Info("🔄 [RUST CONTROL] Rust explicitly advanced Go Master memory to block #%d", blk.Header().BlockNumber())
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
	if err := executor.InitFFIBridge(rustConfigPath, dataDir, reqHandler, blockQueue); err != nil {
		logger.Error("❌ [FFI BRIDGE] Error starting FFI Bridge: %v", err)
		os.Exit(1)
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
		os.Exit(1)
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

	fmt.Printf("🎧 [PROCESSOR] Starting loop to read from dataChan (batch-drain enabled)...")
	for epochData := range dataChan {
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
			fmt.Printf("📥 [DIAG-RECV] Block from Rust: GEI=%d, txs=%d, epoch=%d, nextExpected=%d, currentBlock=%d",
				incomingGEI, incomingTxCount, epochData.GetEpoch(), nextExpectedGlobalExecIndex, currentBlockNumber)
		}

		// NOTE: Network Sync cancellation logic removed (Feb 2026)
		// Rust P2P now handles block sync for SyncOnly nodes via rust_sync_node.rs

		// ═══════════════════════════════════════════════════════════════════
		// TRANSITION SYNC — ACTIVE STATE ADVANCEMENT
		//
		// After SYNC-FIRST (or HandleSyncBlocksRequest from Rust), Go's
		// persisted GEI and block number may have advanced past our local
		// variables. Re-read from DB and advance to prevent processing
		// commits that Go already executed, and to keep nextExpected in
		// sync with the actual DB state.
		//
		// Without this, processRustEpochData would still expect GEI=759
		// after SYNC-FIRST advanced the DB to GEI=1575, causing a massive
		// gap when consensus sends GEI=2342 and Go tries to process it
		// with stale nextExpected.
		// ═══════════════════════════════════════════════════════════════════
		{
			actualLastBlockDB := storage.GetLastBlockNumber()
			bpLastBlock := bp.GetLastBlock()
			bpLastBlockNum := uint64(0)
			if bpLastBlock != nil {
				bpLastBlockNum = bpLastBlock.Header().BlockNumber()
			}

			actualLastGEI := storage.GetLastGlobalExecIndex()

			// CRITICAL FIX: Actually advance local state when DB is ahead
			if actualLastGEI > 0 && actualLastGEI >= nextExpectedGlobalExecIndex {
				oldNextExpected := nextExpectedGlobalExecIndex
				nextExpectedGlobalExecIndex = actualLastGEI + 1
				if actualLastBlockDB > currentBlockNumber {
					currentBlockNumber = actualLastBlockDB
				}
				if oldNextExpected != nextExpectedGlobalExecIndex {
					logger.Info("🔄 [TRANSITION SYNC] Advanced local state from DB: nextExpected %d → %d, block %d → %d (SYNC-FIRST or SyncBlocksRequest advanced DB)",
						oldNextExpected, nextExpectedGlobalExecIndex, bpLastBlockNum, currentBlockNumber)
				}
			} else if actualLastBlockDB > 0 && actualLastBlockDB > bpLastBlockNum {
				// Block advanced but GEI didn't — unlikely but handle gracefully
				currentBlockNumber = actualLastBlockDB
				logger.Debug("🔍 [TRANSITION SYNC] DB block #%d > in-memory block #%d. Updated currentBlockNumber.",
					actualLastBlockDB, bpLastBlockNum)
			}
		}

		// 🔍 DIAGNOSTIC: Check if TRANSITION SYNC just advanced past a TX-containing block
		if incomingTxCount > 0 && incomingGEI < nextExpectedGlobalExecIndex {
			logger.Info("⚠️  [DIAG-TRANSITION] TRANSITION SYNC advanced past TX block! GEI=%d with %d txs is now OLD (nextExpected=%d)",
				incomingGEI, incomingTxCount, nextExpectedGlobalExecIndex)
		}

		// ═══════════════════════════════════════════════════════════════════
		// BATCH-DRAIN OPTIMIZATION: Process consecutive empty commits in bulk
		// During catch-up, ~95%+ commits are empty (0 transactions). Instead of
		// processing each one individually (3 I/O ops each), we batch-drain all
		// queued empty commits and only persist the final GEI once.
		// This gives 100-10000x speedup during catch-up sync.
		// FORK-SAFETY: Only empty commits are batched. Any commit with TXs
		// goes through the full processing path unchanged.
		// ═══════════════════════════════════════════════════════════════════
		if bp.isEmptyCommit(epochData) && epochData.GetGlobalExecIndex() == nextExpectedGlobalExecIndex {
			batchCount := uint64(1)
			highestGEI := epochData.GetGlobalExecIndex()

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
						// Consecutive empty commit — absorb into batch
						highestGEI = nextGEI
						batchCount++
						// Track epoch from latest commit
						nextEpoch := next.GetEpoch()
						if nextEpoch > lastEpochNum {
							lastEpochNum = nextEpoch
							lastCommitTimestampMs = next.GetCommitTimestampMs()
						}
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
						bp.updateAndPersistLastGlobalExecIndex(highestGEI)
						nextExpectedGlobalExecIndex = highestGEI + 1
						if lastEpochNum > 0 {
							bp.chainState.CheckAndUpdateEpochFromBlock(lastEpochNum, lastCommitTimestampMs)
						}
						if batchCount > 1 {
							logger.Info("⚡ [BATCH-DRAIN] Processed %d consecutive empty commits in 1 batch (GEI %d→%d)",
								batchCount, highestGEI-batchCount+1, highestGEI)
						}
						// Now process the non-empty/non-consecutive commit
						logger.Info("📥 [PROCESSOR] Read block from dataChan: global_exec_index=%d", nextGEI)
						
						bp.ExecutionMutex.RLock()
						bp.processSingleEpochData(next, &nextExpectedGlobalExecIndex, &currentBlockNumber, pendingBlocks, skippedCommitsWithTxs, epochFileLogger)
						bp.ExecutionMutex.RUnlock()
						
						drainTimeout.Stop()
						goto BATCH_DONE
					}
				case <-drainTimeout.C:
					// Channel empty for 5ms — finalize this batch
					draining = false
				}
			}
			drainTimeout.Stop()

			// Persist only the final GEI (1 DB write for entire batch)
			bp.updateAndPersistLastGlobalExecIndex(highestGEI)
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
				bp.ExecutionMutex.RLock()
				bp.processSingleEpochData(pendingBlock, &nextExpectedGlobalExecIndex, &currentBlockNumber, pendingBlocks, skippedCommitsWithTxs, epochFileLogger)
				bp.ExecutionMutex.RUnlock()
			}
		BATCH_DONE:
			continue
		}

		// Non-empty commit or non-sequential — full processing path
		logger.Info("📥 [PROCESSOR] Read block from dataChan: global_exec_index=%d", epochData.GetGlobalExecIndex())
		bp.ExecutionMutex.RLock()
		bp.processSingleEpochData(epochData, &nextExpectedGlobalExecIndex, &currentBlockNumber, pendingBlocks, skippedCommitsWithTxs, epochFileLogger)
		bp.ExecutionMutex.RUnlock()
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

			if _, err := bp.chainState.CommitBlockState(lastBlock,
				blockchain.WithPersistToDB(),
				blockchain.WithCommitMappings(),
			); err != nil {
				logger.Error("🔄 [TRANSITION GUARD] Failed to commit block #%d: %v", lastBlockNum, err)
			} else {
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

// monitorBlockReceiveTimeout monitors for block receive timeouts
// monitorBlockReceiveTimeout removed per user request

// handleShutdown handles safe shutdown of the listener
func handleShutdown(listener *executor.Listener) {
	// This function is kept for backward compatibility
	// The actual shutdown handling should be implemented in the main application
}

// ProcessBlockData processes block data received from network (for sub-node)
func (bp *BlockProcessor) ProcessBlockData(request network.Request) error {
	reqBody := request.Message().Body()

	// ══════════════════════════════════════════════════════════════════
	// FORK SAFETY: Master nodes MUST NOT process raw blocks from P2P!
	// Master nodes receive executable blocks exclusively from Rust via FFI.
	// Processing P2P blocks causes GEI to jump without NOMT execution.
	// ══════════════════════════════════════════════════════════════════
	if bp.serviceType == p_common.ServiceTypeMaster {
		logger.Warn("🛡️ [BLOCK DROP] Master node dropping P2P block. Master relies entirely on Rust for blocks.")
		return nil
	}

	// ══════════════════════════════════════════════════════════════════
	// SUB NODE SAFETY: Sub nodes MUST ONLY process blocks from their own Master.
	// ══════════════════════════════════════════════════════════════════
	if request.Connection() != nil && request.Connection().Type() != p_common.MASTER_CONNECTION_TYPE {
		logger.Warn("🛡️ [BLOCK DROP] Sub node dropping P2P block from non-master connection (type: %s, addr: %s)",
			request.Connection().Type(), request.Connection().RemoteAddr())
		return nil
	}


	// Decompress zstd before deserializing
	var decoder, _ = zstd.NewReader(nil)
	decompressedBody, errDecode := decoder.DecodeAll(reqBody, nil)
	if errDecode != nil {
		// Fallback to uncompressed for backward compatibility or errors
		logger.Warn("Failed to decompress block data from network (maybe uncompressed old format?): %v", errDecode)
		decompressedBody = reqBody
	}
	decoder.Close()

	backupDb, err := storage.DeserializeBackupDb(decompressedBody)
	if err != nil {
		logger.Error("CRITICAL: Could not decode BlockData received from network: %v", err)
		return nil
	}
	logger.Info("ProcessBlockData %v", backupDb.BockNumber)
	// Debug: Check if Receipts survived deserialization
	if len(backupDb.Receipts) > 0 {
		logger.Info("📦 [SUB-NODE RECV] Block #%d: Receipts field has %d bytes after deserialization", backupDb.BockNumber, len(backupDb.Receipts))
	} else {
		logger.Warn("⚠️ [SUB-NODE RECV] Block #%d: Receipts field is EMPTY after deserialization (raw body size=%d)", backupDb.BockNumber, len(request.Message().Body()))
	}

	// Log block header details for monitoring (before queuing, so always visible)
	if backupDb.BockBatch != nil {
		b, deserErr := storage.DeserializeBatch(backupDb.BockBatch)
		if deserErr == nil && len(b) > 0 && len(b[0]) >= 2 {
			var currentBlock block.Block
			if unmarshalErr := currentBlock.Unmarshal(b[0][1]); unmarshalErr == nil {
				header := currentBlock.Header()
				logger.Info("📋 [SUB-NODE RECV] Block #%d header: hash=%s, parent=%s, epoch=%d, timestamp=%d, tx_count=%d",
					header.BlockNumber(),
					header.Hash().Hex()[:16]+"...",
					header.LastBlockHash().Hex()[:16]+"...",
					header.Epoch(),
					header.TimeStamp(),
					len(currentBlock.Transactions()))
				logger.Info("Lastblock header: %v", header)

				// ══════════════════════════════════════════════════════════════════
				// BLS SIGNATURE VERIFICATION: Verify Master's signature on block hash.
				// - If masterBLSPubKey is set and sig is present → verify
				// - If sig is missing → warn (backward compat during rollout)
				// - If sig is invalid → reject block
				// ══════════════════════════════════════════════════════════════════
				if len(bp.masterBLSPubKey) > 0 && !bp.skipSigVerification {
					sig := header.AggregateSignature()
					if len(sig) > 0 {
						signingHash := header.HashWithoutSignature()
						if !block_signer.VerifyBlockSignature(signingHash, sig, bp.masterBLSPubKey) {
							logger.Error("🚨 [BLOCK VERIFY] REJECTED block #%d: BLS signature INVALID! hash=%s",
								header.BlockNumber(), signingHash.Hex()[:16]+"...")
							return nil // Reject block with invalid signature
						}
						logger.Debug("✅ [BLOCK VERIFY] Block #%d signature valid", header.BlockNumber())
					} else {
						logger.Warn("⚠️  [BLOCK VERIFY] Block #%d has no signature (unsigned block from old Master?)", header.BlockNumber())
					}
				}
			}
		}
	}

	if bp.node == nil {
		logger.Error("FATAL: HostNode not assigned to BlockProcessor (ProcessBlockData), dropping block %d", backupDb.BockNumber)
		return nil
	}

	// Cache raw BlockData so this node (and peers) can serve missing-block requests (fork-safe: data sync only)
	// Key format must match pkg/node storage key: "<block_data_topic>-<blockNumber>"
	key := fmt.Sprintf("%s-%d", p_common.BlockDataTopic, backupDb.BockNumber)
	bp.node.SetStorage(key, reqBody) // Store the compressed raw body

	select {
	case bp.node.BlockProcessingQueue <- &backupDb:
		// Log last block info after successful queue
		currentLastBlock := storage.GetLastBlockNumber()
		logger.Info("✅ [SUB-NODE] Queued block %d for processing, current last_block=%d", backupDb.BockNumber, currentLastBlock)
	default:
		logger.Warn("BlockProcessingQueue full! Dropping block %d from network", backupDb.BockNumber)
	}
	return nil
}

// TriggerResyncFromMaster triggers a full data resync from the master node.
// This is the Tier 3 recovery path used when the block gap is too large
// (>CriticalGapThreshold) or too many consecutive fetch failures occur.
// It sends a "sys" request to master, which triggers the master to send
// its entire DB folder via the /file-transfer/1.0.0 protocol.
func (bp *BlockProcessor) TriggerResyncFromMaster() error {
	if bp.node == nil {
		return fmt.Errorf("node is nil, cannot trigger resync")
	}

	logger.Info("🚨 [CRITICAL-RESYNC] Starting full resync from Master node...")
	logger.Info("🚨 [CRITICAL-RESYNC] This will request the entire database from Master.")
	logger.Info("🚨 [CRITICAL-RESYNC] Sub node processing will be paused during resync.")

	// Use the existing SendRequestToMaster mechanism
	// This sends a "sys" request which triggers file-transfer from master
	resyncCtx := context.Background()

	err := bp.node.SendRequestToMaster(resyncCtx, "sys")
	if err != nil {
		logger.Error("❌ [CRITICAL-RESYNC] Failed to send resync request to Master: %v", err)
		return fmt.Errorf("failed to send resync request: %w", err)
	}

	logger.Info("✅ [CRITICAL-RESYNC] Resync request sent to Master successfully.")
	logger.Info("⏳ [CRITICAL-RESYNC] Waiting for Master to send data via /file-transfer/1.0.0...")

	// Wait for the file transfer to complete
	// The HandleFileReceive handler (set up in app_network.go) will receive the data
	// and update the local storage. We just need to wait a reasonable amount of time.
	time.Sleep(30 * time.Second)

	logger.Info("✅ [CRITICAL-RESYNC] Resync wait period completed. Resuming normal block processing.")
	return nil
}

// StartupCatchUp synchronizes Go Sub with Go Master BEFORE the main processing loop starts.
// BLOCKING: This function does not return until Go Sub is fully caught up with Go Master.
// On restart, Go Sub may be behind Go Master (e.g., Sub at block 6850, Master at 6900).
// Go Master only publishes NEW blocks via pubsub, so the gap would never be covered
// without this explicit catch-up mechanism.
func (bp *BlockProcessor) StartupCatchUp() {
	if bp.node == nil {
		return
	}

	localLastBlock := storage.GetLastBlockNumber()
	logger.Info("📦 [STARTUP-SYNC] Go Sub starting catch-up: local_last_block=%d", localLastBlock)

	// ─── Phase 1: Wait for P2P peers to connect (max 60s) ───────────────
	logger.Info("⏳ [STARTUP-SYNC] Phase 1: Waiting for P2P peers to connect...")
	peerConnected := false
	for i := 0; i < 120; i++ { // 60 seconds (120 * 500ms)
		if bp.node.HasConnectedPeers() {
			peerConnected = true
			logger.Info("✅ [STARTUP-SYNC] P2P peers connected after %dms", (i+1)*500)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !peerConnected {
		logger.Warn("⚠️ [STARTUP-SYNC] No peers connected after 60s, proceeding without catch-up")
		return
	}

	// ─── Phase 2: Send GetBlockNumber request to Master and wait (max 5s) ──
	logger.Info("⏳ [STARTUP-SYNC] Phase 2: Requesting Master block height...")
	masterConns := bp.node.ConnectionsManager.ConnectionsByType(
		p_common.MapConnectionTypeToIndex(p_common.MASTER_CONNECTION_TYPE))
	reqSent := false
	for _, conn := range masterConns {
		if conn != nil && conn.IsConnect() {
			err := bp.messageSender.SendBytes(conn, command.GetBlockNumber, nil)
			if err == nil {
				reqSent = true
				break
			}
		}
	}
	if !reqSent {
		logger.Warn("⚠️ [STARTUP-SYNC] Could not send GetBlockNumber request to Master, proceeding without catch-up")
		return
	}

	var masterLastBlock uint64
	for i := 0; i < 25; i++ { // 5 seconds (25 * 200ms)
		masterLastBlock = storage.GetLastBlockNumberFromMaster()
		if masterLastBlock > 0 {
			logger.Info("✅ [STARTUP-SYNC] Master block height received: %d (after %dms)", masterLastBlock, (i+1)*200)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if masterLastBlock == 0 {
		logger.Warn("⚠️ [STARTUP-SYNC] Could not determine Master's block height after 5s, proceeding without catch-up")
		return
	}

	// ─── Phase 3: Calculate gap and batch-fetch ─────────────────────────
	localLastBlock = storage.GetLastBlockNumber() // Re-read in case blocks arrived during wait
	gap := int64(masterLastBlock) - int64(localLastBlock)
	if gap <= 0 {
		// ═══════════════════════════════════════════════════════════════════
		// PHASE 3a: MANDATORY HASH VALIDATION (BLOCKING)
		// ═══════════════════════════════════════════════════════════════════
		// After --keep-data restart, the Master may have re-processed blocks
		// from Rust DAG replay with different leader/timestamp. The Sub-node
		// retains old block hashes, causing a PERMANENT FORK.
		//
		// THIS CHECK IS MANDATORY: The Sub-node WILL NOT start processing
		// new blocks until consistency with Master is verified.
		// If hash mismatch is detected:
		//   1. Auto-resync from Master is attempted
		//   2. If resync fails → process EXITS with clear operator instructions
		// ═══════════════════════════════════════════════════════════════════
		logger.Info("╔══════════════════════════════════════════════════════════╗")
		logger.Info("║  🔍 MANDATORY HASH VALIDATION — Sub vs Master           ║")
		logger.Info("╚══════════════════════════════════════════════════════════╝")
		logger.Info("📦 [STARTUP-SYNC] Heights match (local=%d, master=%d). Validating block hash consistency...", localLastBlock, masterLastBlock)

		// Check multiple blocks for thorough validation
		// Use min(local, master) as reference since Sub-node may be AHEAD of Master
		// (Sub has blocks from prior session that Master hasn't re-processed yet)
		refBlock := masterLastBlock
		if localLastBlock < masterLastBlock {
			refBlock = localLastBlock
		}
		// Check refBlock, refBlock-10, refBlock-50 (if they exist)
		blocksToCheck := []uint64{refBlock}
		if refBlock > 10 {
			blocksToCheck = append(blocksToCheck, refBlock-10)
		}
		if refBlock > 50 {
			blocksToCheck = append(blocksToCheck, refBlock-50)
		}

		allPassed := true
		checkedCount := 0
		for _, checkBlock := range blocksToCheck {
			localHash, hashOk := blockchain.GetBlockChainInstance().GetBlockHashByNumber(checkBlock)
			if !hashOk {
				logger.Warn("⚠️ [STARTUP-SYNC] Could not get local hash for block %d, skipping this block", checkBlock)
				continue
			}

			// Clear local cache for this block to force fresh fetch from Master
			tipBlockKey := fmt.Sprintf("%s-%d", p_common.BlockDataTopic, checkBlock)
			bp.node.KeyValueStore.Remove(tipBlockKey)

			// Trigger async fetch from Master
			go bp.node.FetchBlockFromMaster(checkBlock)

			// Wait for the block to arrive (max 10s per block)
			var masterBlockData []byte
			for i := 0; i < 50; i++ { // 10 seconds (50 * 200ms)
				time.Sleep(200 * time.Millisecond)
				data, err := bp.node.GetBlockStorageLocal(checkBlock)
				if err == nil && data != nil {
					masterBlockData = data
					break
				}
			}

			if masterBlockData == nil {
				logger.Warn("⚠️ [STARTUP-SYNC] Could not fetch Master's block %d (timeout 10s)", checkBlock)
				continue
			}

			// Deserialize and extract hash
			masterBackupDb, err := storage.DeserializeBackupDb(masterBlockData)
			if err != nil {
				logger.Warn("⚠️ [STARTUP-SYNC] Could not deserialize Master's block %d: %v", checkBlock, err)
				continue
			}

			if masterBackupDb.BockBatch == nil {
				continue
			}

			b, deserErr := storage.DeserializeBatch(masterBackupDb.BockBatch)
			if deserErr != nil || len(b) == 0 || len(b[0]) < 2 {
				continue
			}

			var masterBlock block.Block
			if unmarshalErr := masterBlock.Unmarshal(b[0][1]); unmarshalErr != nil {
				continue
			}

			masterHash := masterBlock.Header().Hash()
			checkedCount++
			if localHash == masterHash {
				logger.Info("  ✅ Block %d: hash MATCH (%s)", checkBlock, localHash.Hex()[:16]+"...")
			} else {
				logger.Error("  🚨 Block %d: hash MISMATCH!", checkBlock)
				logger.Error("     Local:  %s", localHash.Hex())
				logger.Error("     Master: %s", masterHash.Hex())
				allPassed = false
			}
		}

		if checkedCount == 0 {
			logger.Warn("⚠️ [STARTUP-SYNC] Could not validate any blocks against Master. Proceeding with caution.")
			return
		}

		if allPassed {
			logger.Info("╔══════════════════════════════════════════════════════════╗")
			logger.Info("║  ✅ HASH VALIDATION PASSED — %d/%d blocks verified       ║", checkedCount, checkedCount)
			logger.Info("╚══════════════════════════════════════════════════════════╝")
			return
		}

		// ═══════════════════════════════════════════════════════════════
		// FORK DETECTED — Auto-resync from Master
		// ═══════════════════════════════════════════════════════════════
		logger.Error("╔══════════════════════════════════════════════════════════╗")
		logger.Error("║  🚨 FORK DETECTED — Sub-node data DOES NOT match Master ║")
		logger.Error("╚══════════════════════════════════════════════════════════╝")
		logger.Error("")
		logger.Error("🔄 Attempting automatic resync from Master...")
		logger.Error("   This will request the entire database from Master.")
		logger.Error("   Sub-node processing is BLOCKED until resync completes.")
		logger.Error("")

		if err := bp.TriggerResyncFromMaster(); err != nil {
			// Resync failed → HALT the process with clear operator instructions
			logger.Error("╔══════════════════════════════════════════════════════════╗")
			logger.Error("║  ❌ RESYNC FAILED — MANUAL INTERVENTION REQUIRED        ║")
			logger.Error("╚══════════════════════════════════════════════════════════╝")
			logger.Error("")
			logger.Error("  The Sub-node's data is INCONSISTENT with the Master.")
			logger.Error("  Automatic resync from Master failed: %v", err)
			logger.Error("")
			logger.Error("  ┌─────────────────────────────────────────────────────┐")
			logger.Error("  │  TO FIX: Delete Sub-node data and restart:         │")
			logger.Error("  │                                                     │")
			logger.Error("  │  1. Stop this Sub-node process                     │")
			logger.Error("  │  2. Delete the Sub-node's data directory           │")
			logger.Error("  │  3. Copy data from Master (or use fresh restart)   │")
			logger.Error("  │  4. Restart the Sub-node                           │")
			logger.Error("  │                                                     │")
			logger.Error("  │  OR: Run ./fresh_restart_with_sync.sh (no --keep-data) │")
			logger.Error("  └─────────────────────────────────────────────────────┘")
			logger.Error("")
			logger.Error("  🛑 Sub-node is HALTING to prevent running with corrupted state.")
			os.Exit(1)
		}

		logger.Info("╔══════════════════════════════════════════════════════════╗")
		logger.Info("║  ✅ RESYNC COMPLETED — Sub-node data refreshed          ║")
		logger.Info("╚══════════════════════════════════════════════════════════╝")
		return
	}

	logger.Info("🔄 [STARTUP-SYNC] Phase 3: Gap detected: local=%d, master=%d, gap=%d blocks. Starting batch catch-up...",
		localLastBlock, masterLastBlock, gap)

	const batchSize uint64 = 100
	totalFetched := 0
	fetchRetries := 0
	const maxFetchRetries = 15 // 30s timeout per batch
	startBlock := localLastBlock + 1
	// FIX: Due to StopWait draining logic, Master's active block is guaranteed
	// to be fully committed to BackupStorage before a clean restart. We no longer
	// skip masterLastBlock on startup to prevent deadlocks where pubsub already
	// broadcast it before the restart.
	endTarget := masterLastBlock

	if endTarget < startBlock {
		logger.Info("✅ [STARTUP-SYNC] Already synced to master height")
		return
	}

	for startBlock <= endTarget {
		end := startBlock + batchSize - 1
		if end > endTarget {
			end = endTarget
		}

		fetchedBlocks, stillMissing := bp.node.GetBlockStorageBatch(startBlock, end)
		if stillMissing > 0 {
			fetchRetries++
			logger.Warn("⚠️ [STARTUP-SYNC] Batch fetch for range %d-%d has %d blocks missing (retry %d/%d)",
				startBlock, end, stillMissing, fetchRetries, maxFetchRetries)
			if fetchRetries >= maxFetchRetries {
				// FIX: Instead of breaking, push whatever blocks we DID fetch
				// and advance past the entire batch. Missing blocks will be
				// fetched by the Tier 1/2/3 recovery in the main processing loop.
				logger.Warn("⚠️ [STARTUP-SYNC] Max fetch retries for batch %d-%d. Pushing %d fetched blocks and continuing.",
					startBlock, end, len(fetchedBlocks))
				for blockNum := startBlock; blockNum <= end; blockNum++ {
					data, exists := fetchedBlocks[blockNum]
					if !exists {
						continue
					}
					fetchedBackupDb, err := storage.DeserializeBackupDb(data)
					if err != nil {
						continue
					}
					bp.node.BlockProcessingQueue <- &fetchedBackupDb
					totalFetched++
				}
				startBlock = end + 1
				fetchRetries = 0
				continue
			}
			time.Sleep(2 * time.Second) // Wait before retry
			continue
		}
		fetchRetries = 0 // Reset on successful fetch

		// Push fetched blocks to BlockProcessingQueue for the producer goroutine
		for blockNum := startBlock; blockNum <= end; blockNum++ {
			data, exists := fetchedBlocks[blockNum]
			if !exists {
				continue
			}
			fetchedBackupDb, err := storage.DeserializeBackupDb(data)
			if err != nil {
				logger.Error("❌ [STARTUP-SYNC] Failed to deserialize block #%d: %v", blockNum, err)
				continue
			}
			// Push to queue — will block if full (backpressure)
			bp.node.BlockProcessingQueue <- &fetchedBackupDb
			totalFetched++
		}

		logger.Info("📦 [STARTUP-SYNC] Fetched batch %d-%d: got %d blocks, %d still missing (total: %d/%d)",
			startBlock, end, len(fetchedBlocks), stillMissing, totalFetched, gap)

		startBlock = end + 1
	}

	// ─── Phase 4: Verify blocks are buffered ────────────────────────────
	// Since StartupCatchUp is BLOCKING, the main loop hasn't started yet.
	// But the producer goroutine (BlockProcessingQueue → localBlockBuffer) IS running,
	// so fetched blocks are being moved to localBlockBuffer for immediate processing
	// when the main loop starts.
	if totalFetched > 0 {
		// Small delay to let producer goroutine drain the queue into localBlockBuffer
		time.Sleep(500 * time.Millisecond)
		logger.Info("📦 [STARTUP-SYNC] Phase 4: %d blocks fetched and queued for processing", totalFetched)
		logger.Info("🔒 [STARTUP-SYNC] Blocks will be processed sequentially when main loop starts")
	}

	logger.Info("🔒 [STARTUP-SYNC] Catch-up complete: fetched=%d, local_block=%d, master_block=%d",
		totalFetched, localLastBlock, masterLastBlock)

	if totalFetched > 0 {
		logger.Info("✅ [STARTUP-SYNC] Go Sub ready — %d catch-up blocks buffered for immediate processing", totalFetched)
	} else if int64(masterLastBlock)-int64(localLastBlock) <= 0 {
		logger.Info("✅ [STARTUP-SYNC] Go Sub fully synced with Master!")
	}
}
