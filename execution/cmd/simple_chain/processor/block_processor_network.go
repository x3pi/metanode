// @title processor/block_processor_network.go
// @markdown processor/block_processor_network.go - Network communication and socket handling
package processor

import (
	"fmt"
	"time"

	// "github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor/pipeline"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"

	"github.com/meta-node-blockchain/meta-node/pkg/fatal"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"

	// "github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
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

	// Inject SetClearNoncesCacheCallback for SyncOnly transaction mempool invalidation
	reqHandler.SetClearNoncesCacheCallback(func() {
		if bp.transactionProcessor != nil {
			bp.transactionProcessor.ClearNoncesCache()
		}
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
	go bp.StartCommitterLoop()

	// The runUnixSocket caller expects this function to block/run in background
	// We can simply return here, or keep it alive like it did. It's normally run as a goroutine.
	// Since the previous function had a block wait, we can mimic it or just let it exit.
	// The processRustEpochData is backgrounded now.
	for {
		time.Sleep(30 * time.Second)
		logger.Debug("🔌 [FFI BRIDGE] Main thread monitoring FFI alive")
	}
}

// processRustEpochData processes epoch data from Rust
func (bp *BlockProcessor) processRustEpochData(dataChan <-chan *pb.ExecutableBlock) {
	logger.Info("🎧 [PROCESSOR] Starting speculative ingestion loop...")
	authQueue := executor.GetAuthoritativeBlockQueue()

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
		}

		lastBlock := bp.GetLastBlock()
		var lastHeader types.BlockHeader
		if lastBlock != nil {
			lastHeader = lastBlock.Header()
		}

		// Dispatch to Speculative Executor for background processing
		bp.speculativeExecutor.ExecuteSpeculative(epochData, lastHeader, authRespCh)
	}

	logger.Warn("🔄 [TRANSITION] dataChan closed! Speculative ingestion loop exited.")

	// ═══════════════════════════════════════════════════════════════════════════
	// TRANSITION GUARD: Ensure all pending state is fully committed to DB
	// before P2P sync starts writing blocks. This prevents a race where:
	// - Go executor has blocks in memory (bp.lastBlock) but not yet in DB
	// - P2P sync starts writing blocks from peers with different parent hashes
	// - Result: fork between consensus-generated and P2P-synced blocks
	// ═══════════════════════════════════════════════════════════════════════════
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
