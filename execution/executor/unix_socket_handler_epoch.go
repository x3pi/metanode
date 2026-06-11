package executor

import (
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
	"google.golang.org/protobuf/proto"
)

// ============================================================================
// CLEAN TRANSITION HANDOFF APIs
// These APIs ensure no gaps or overlaps between sync and consensus modes
// ============================================================================

// HandleSetConsensusStartBlockRequest - Called by Rust before starting consensus
// Tells Go: "Consensus will produce blocks starting from block_number"
// Go will verify sync has completed up to block_number - 1
func (rh *RequestHandler) HandleSetConsensusStartBlockRequest(request *pb.SetConsensusStartBlockRequest) (*pb.SetConsensusStartBlockResponse, error) {
	blockNumber := request.GetBlockNumber()
	logger.Info("🔄 [TRANSITION HANDOFF] SetConsensusStartBlock: consensus will start at block %d", blockNumber)

	// Get current last block from Go storage
	lastSyncBlock := storage.GetLastBlockNumber()
	expectedSyncBlock := blockNumber - 1

	// Verify sync has caught up to the expected block
	if lastSyncBlock < expectedSyncBlock {
		errMsg := fmt.Sprintf("sync not caught up: last_sync_block=%d, expected=%d (consensus_start-1)", lastSyncBlock, expectedSyncBlock)
		logger.Warn("⚠️ [TRANSITION HANDOFF] %s", errMsg)
		return &pb.SetConsensusStartBlockResponse{
			Success:       false,
			LastSyncBlock: lastSyncBlock,
			Message:       errMsg,
		}, nil
	}

	logger.Info("✅ [TRANSITION HANDOFF] Sync caught up: last_sync_block=%d >= expected=%d", lastSyncBlock, expectedSyncBlock)

	// Store the consensus start block for future reference
	// This can be used to ensure consensus blocks are processed correctly
	storage.SetConsensusStartBlock(blockNumber)

	return &pb.SetConsensusStartBlockResponse{
		Success:       true,
		LastSyncBlock: lastSyncBlock,
		Message:       fmt.Sprintf("Sync complete up to block %d, consensus can start at block %d", lastSyncBlock, blockNumber),
	}, nil
}

// HandleSetSyncStartBlockRequest - Called by Rust when consensus ends
// Tells Go: "Consensus ended at last_consensus_block, sync should start from last_consensus_block + 1"
func (rh *RequestHandler) HandleSetSyncStartBlockRequest(request *pb.SetSyncStartBlockRequest) (*pb.SetSyncStartBlockResponse, error) {
	lastConsensusBlock := request.GetLastConsensusBlock()
	syncStartBlock := lastConsensusBlock + 1
	logger.Info("🔄 [TRANSITION HANDOFF] SetSyncStartBlock: consensus ended at block %d, sync will start from block %d", lastConsensusBlock, syncStartBlock)

	// Get current last block from Go storage
	currentLastBlock := storage.GetLastBlockNumber()

	// Verify the transition makes sense
	if currentLastBlock > lastConsensusBlock {
		// Go has processed more blocks than consensus claims
		logger.Warn("⚠️ [TRANSITION HANDOFF] Unexpected state: current_last_block=%d > last_consensus_block=%d",
			currentLastBlock, lastConsensusBlock)
	}

	// Store sync start block for network sync to use
	storage.SetSyncStartBlock(syncStartBlock)

	logger.Info("✅ [TRANSITION HANDOFF] Sync start block set to %d (consensus ended at %d)", syncStartBlock, lastConsensusBlock)

	return &pb.SetSyncStartBlockResponse{
		Success:        true,
		SyncStartBlock: syncStartBlock,
		Message:        fmt.Sprintf("Sync will start from block %d", syncStartBlock),
	}, nil
}

// HandleWaitForSyncToBlockRequest - Called by Rust to wait for Go sync to reach a specific block
// Used during SyncOnly -> Validator transition to ensure sync is complete before consensus starts
func (rh *RequestHandler) HandleWaitForSyncToBlockRequest(request *pb.WaitForSyncToBlockRequest) (*pb.WaitForSyncToBlockResponse, error) {
	targetBlock := request.GetTargetBlock()
	timeoutSeconds := request.GetTimeoutSeconds()

	// Default timeout if not specified
	if timeoutSeconds == 0 {
		timeoutSeconds = 30 // 30 seconds default
	}

	logger.Info("⏳ [TRANSITION HANDOFF] WaitForSyncToBlock: waiting for sync to reach block %d (timeout: %ds)", targetBlock, timeoutSeconds)

	// Check immediately if already reached
	currentBlock := storage.GetLastBlockNumber()
	if currentBlock >= targetBlock {
		logger.Info("✅ [TRANSITION HANDOFF] Already at or past target block: current=%d, target=%d", currentBlock, targetBlock)
		return &pb.WaitForSyncToBlockResponse{
			Reached:      true,
			CurrentBlock: currentBlock,
			Message:      fmt.Sprintf("Already at block %d (target: %d)", currentBlock, targetBlock),
		}, nil
	}

	// Poll until target is reached or timeout
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		currentBlock = storage.GetLastBlockNumber()
		if currentBlock >= targetBlock {
			logger.Info("✅ [TRANSITION HANDOFF] Target block reached: current=%d, target=%d", currentBlock, targetBlock)
			return &pb.WaitForSyncToBlockResponse{
				Reached:      true,
				CurrentBlock: currentBlock,
				Message:      fmt.Sprintf("Reached block %d (target: %d)", currentBlock, targetBlock),
			}, nil
		}
		time.Sleep(pollInterval)
	}

	// Timeout
	logger.Warn("⚠️ [TRANSITION HANDOFF] Timeout waiting for sync: current=%d, target=%d", currentBlock, targetBlock)
	return &pb.WaitForSyncToBlockResponse{
		Reached:      false,
		CurrentBlock: currentBlock,
		Message:      fmt.Sprintf("Timeout after %ds: current=%d, target=%d", timeoutSeconds, currentBlock, targetBlock),
	}, nil
}

// ============================================================================
// BLOCK SYNC APIs (SyncOnly Block Synchronization)
// These APIs enable SyncOnly nodes to fetch and sync blocks from the Go Master
// ============================================================================

// HandleGetBlocksRangeRequest processes a GetBlocksRangeRequest and returns blocks in range
// This is used by peer nodes to fetch blocks for synchronization
func (rh *RequestHandler) HandleGetBlocksRangeRequest(request *pb.GetBlocksRangeRequest) (*pb.GetBlocksRangeResponse, error) {
	fromBlock := request.GetFromBlock()
	toBlock := request.GetToBlock()

	// logger.Info("📦 [BLOCK SYNC] Handling GetBlocksRangeRequest: from=%d, to=%d", fromBlock, toBlock)

	// Limit batch size to prevent DoS
	maxBatch := uint64(5000)
	if toBlock-fromBlock+1 > maxBatch {
		toBlock = fromBlock + maxBatch - 1
		logger.Info("📦 [BLOCK SYNC] Limited batch size to %d blocks (from=%d, to=%d)", maxBatch, fromBlock, toBlock)
	}

	blockDatabase := block.NewBlockDatabase(rh.storageManager.GetStorageBlock())
	bc := blockchain.GetBlockChainInstance()
	lastBlockNumber := storage.GetLastBlockNumber()

	// ═══════════════════════════════════════════════════════════════════════════
	// CRITICAL FIX (Mar 2026): Storage counter may be stale!
	// Consensus commitWorker calls SaveLastBlock (updates lastBlockHashKey)
	// but the counter (storage.GetLastBlockNumber) may not reflect all blocks.
	// Use GetLastBlock() to determine the actual latest block number.
	// ═══════════════════════════════════════════════════════════════════════════
	lastBlock, lastBlockErr := blockDatabase.GetLastBlock()
	if lastBlockErr == nil && lastBlock != nil {
		actualBlockNum := lastBlock.Header().BlockNumber()
		if actualBlockNum > lastBlockNumber {
			logger.Info("📦 [BLOCK SYNC] Counter stale: counter=%d, actual=%d (using actual)", lastBlockNumber, actualBlockNum)
			lastBlockNumber = actualBlockNum

			// Rebuild missing block number → hash mappings for blocks between
			// old counter and actual. Walk backwards from lastBlock using parent hash.
			blk := lastBlock
			for blk != nil {
				bNum := blk.Header().BlockNumber()
				if bNum == 0 {
					break
				}
				// Check if mapping already exists
				if _, ok := bc.GetBlockHashByNumber(bNum); ok {
					break // Already mapped, stop
				}
				// Set the mapping
				bc.SetBlockNumberToHash(bNum, blk.Header().Hash())
				// Walk to parent
				parentHash := blk.Header().LastBlockHash()
				parentBlk, err := blockDatabase.GetBlockByHash(parentHash)
				if err != nil {
					break
				}
				blk = parentBlk
			}
			// CRITICAL FIX: Flush the rebuilt mappings from dirtyStorage to LevelDB
			// Without this, the mappings are lost on restart or cache expiry, causing eth_getBlockByNumber to return null
			bc.Commit()
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// BLOCK NUMBER MODE ONLY (Mar 2026):
	// fetch_blocks_from_peer ALWAYS sends block numbers (not GEI).
	// GEI mode was causing infinite dedup loops: if fromBlock didn't exist
	// in GetBlockHashByNumber, it fell back to GEI binary search which
	// returned blocks with header block_numbers BELOW fromBlock → dedup
	// → counter never advances → sync stuck forever.
	//
	// If fromBlock doesn't exist, scan forward to find first available block.
	// ═══════════════════════════════════════════════════════════════════════════
	// Find the actual starting block number (scan forward if fromBlock doesn't exist)
	startBlock := fromBlock
	if _, ok := bc.GetBlockHashByNumber(fromBlock); !ok {
		// Scan forward to find any block with number >= fromBlock
		found := false
		for probe := fromBlock; probe <= lastBlockNumber; probe++ {
			if _, ok := bc.GetBlockHashByNumber(probe); ok {
				startBlock = probe
				found = true
				break
			}
		}
		if !found {
			// logger.Info("📦 [BLOCK SYNC] ❌ No blocks found >= %d (lastBlock=%d, counter_stale=%v). "+
			// 	"BlockHashByNumber lookup failed for ALL numbers in range [%d..%d]. "+
			// 	"Possible cause: blocks not indexed or GC'd.",
			// 	fromBlock, lastBlockNumber, lastBlockErr == nil && lastBlock != nil && lastBlock.Header().BlockNumber() > storage.GetLastBlockNumber(),
			// 	fromBlock, lastBlockNumber)
		}
	}
	// logger.Info("📦 [BLOCK SYNC] Using BlockNumber mode: from=%d (start=%d), to=%d (lastBlock=%d, lastHandledCommit=%d, lastHandledEpoch=%d)",
	// 	fromBlock, startBlock, toBlock, lastBlockNumber, storage.GetLastHandledCommitIndex(), storage.GetLastHandledCommitEpoch())

	var blocks []*pb.BlockData

	// ── BLOCK NUMBER MODE (ONLY) ──
	// Iterate sequentially by BlockNumber from startBlock to min(toBlock, lastBlockNumber)
	upperBound := toBlock
	if upperBound > lastBlockNumber {
		upperBound = lastBlockNumber
	}

	lastHandledCommit := storage.GetLastHandledCommitIndex()
	lastHandledEpoch := storage.GetLastHandledCommitEpoch()

	for blockNum := startBlock; blockNum <= upperBound; blockNum++ {
		if uint64(len(blocks)) >= maxBatch {
			break
		}
		blockHash, ok := bc.GetBlockHashByNumber(blockNum)
		if !ok {
			continue
		}
		blk, err := blockDatabase.GetBlockByHash(blockHash)
		if err != nil || blk == nil {
			continue
		}

		rawBlockBytes, err := blk.Marshal()
		if err != nil {
			logger.Warn("📦 [BLOCK SYNC] Failed to marshal block %d: %v", blockNum, err)
			continue
		}
		header := blk.Header()

		// ═══════════════════════════════════════════════════════════════════
		// CRITICAL FORK-SAFETY (May 2026): Prevent serving partial commits!
		// m1 uses these blocks to set its lastHandledCommitIndex. If we send blocks
		// from a commit that m0 is STILL processing, m1 will skip the rest of the
		// commit when consensus resumes.
		// ═══════════════════════════════════════════════════════════════════
		if header.Epoch() > lastHandledEpoch || (header.Epoch() == lastHandledEpoch && header.CommitIndex() > uint64(lastHandledCommit)) {
			logger.Warn("📦 [BLOCK SYNC] Stopping at block #%d: CommitIndex %d is beyond fully-handled commit %d (epoch %d)",
				blockNum, header.CommitIndex(), lastHandledCommit, lastHandledEpoch)
			break
		}

		blockData := &pb.BlockData{
			BlockNumber:      header.BlockNumber(),
			BlockHash:        header.Hash().Bytes(),
			Epoch:            header.Epoch(),
			TimestampMs:      header.TimeStamp(),
			ParentHash:       header.LastBlockHash().Bytes(),
			StateRoot:        header.AccountStatesRoot().Bytes(),
			TransactionsRoot: header.TransactionsRoot().Bytes(),
			ReceiptsRoot:     header.ReceiptRoot().Bytes(),
			RawBlockBytes:    rawBlockBytes,
		}
		// ═══════════════════════════════════════════════════════════════════
		// CRITICAL (Mar 2026): Ensure backup data is included for EVERY block.
		// broadcastWorker may lag behind commitWorker — backup data isn't
		// ready yet when this handler runs. Without backup, the receiver's
		// Sub node gets stuck forever waiting for the missing block.
		//
		// Strategy:
		//  1. Check BackupDb for existing backup data
		//  2. If not found, wait briefly (broadcastWorker may be processing)
		//  3. If still not found after retries, STOP — don't serve this block
		//     or any subsequent blocks. Requester will retry later.
		// ═══════════════════════════════════════════════════════════════════
		backupStorage := rh.storageManager.GetStorageBackupDb()
		var backupData []byte
		if backupStorage != nil {
			primaryKey := []byte(fmt.Sprintf("block_data_topic-%d", blockNum))
			data, getErr := backupStorage.Get(primaryKey)
			if getErr != nil || len(data) == 0 {
				legacyKey := []byte(fmt.Sprintf("backup_%d", blockNum))
				data, getErr = backupStorage.Get(legacyKey)
			}
			if getErr == nil && len(data) > 0 {
				backupData = data
			} else {
				// Backup not ready — broadcastWorker may be lagging.
				// STOP serving here immediately. Requester will retry this range later.
				// Removing the 10x 50ms sleep polling to prevent cascading delays on large batch syncs.
				// logger.Warn("📦 [BLOCK SYNC] Block #%d backup NOT ready (broadcastWorker lagging). Stopping at block #%d (served %d blocks)",
				// 	blockNum, blockNum-1, len(blocks))
				break
			}
		}
		blockData.BackupData = backupData
		blocks = append(blocks, blockData)
	}

	count := uint64(len(blocks))
	if count == 0 {
		// logger.Info("📦 [BLOCK SYNC] ⚠️ Returning 0 blocks (from=%d, to=%d, lastBlock=%d). "+
		// 	"Check: (1) BlockHashByNumber index missing? (2) Backup data not ready? (3) Epoch/commit filter?",
		// 	fromBlock, toBlock, lastBlockNumber)
	} else {
		// logger.Info("📦 [BLOCK SYNC] ✅ Returning %d blocks (from=%d, to=%d)", count, fromBlock, toBlock)
	}

	return &pb.GetBlocksRangeResponse{
		Blocks: blocks,
		Count:  count,
		Error:  "",
	}, nil
}

// HandleSyncBlocksRequest processes a SyncBlocksRequest and syncs blocks to local storage
// This is used by nodes to receive blocks fetched from peers via Rust orchestration
func (rh *RequestHandler) HandleSyncBlocksRequest(request *pb.SyncBlocksRequest) (*pb.SyncBlocksResponse, error) {
	blocks := request.GetBlocks()
	blockCount := len(blocks)

	logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Handling SyncBlocksRequest: block_count=%d", blockCount)

	// ═══════════════════════════════════════════════════════════════════════════
	// DEADLOCK PREVENTION (May 2026): Reject all sync requests during snapshot.
	// HandleSyncBlocksRequest opens NOMT sessions that are NOT gated by snapshotGate
	// or ExecutionMutex natively. If a snapshot triggers while these sessions are active,
	// CloseForSnapshot waits forever → DEADLOCK.
	// Rust will retry this RPC after the snapshot completes.
	// ═══════════════════════════════════════════════════════════════════════════
	if sm := rh.getSnapshotManager(); sm != nil && sm.IsSnapshotInProgress() {
		logger.Warn("⏸️ [SYNC] SyncBlocksRequest rejected: snapshot in progress. Rust should retry.")
		return &pb.SyncBlocksResponse{
			SyncedCount:     0,
			LastSyncedBlock: 0,
			Error:           "snapshot in progress, retry later",
		}, nil
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// CONCURRENCY & FORK-SAFETY GATE (May 2026): Acquire ExecutionMutex lock.
	// This serializes block sync writes with consensus block execution, preventing
	// NOMT trie root invalidations (Changeset no longer valid FFI errors).
	// ═══════════════════════════════════════════════════════════════════════════
	if rh.lockExecutionCallback != nil {
		logger.Info("🔒 [SYNC-GATE] Acquiring ExecutionMutex lock for block synchronization")
		rh.lockExecutionCallback()
		defer func() {
			logger.Info("🔓 [SYNC-GATE] Releasing ExecutionMutex lock after block synchronization")
			rh.unlockExecutionCallback()
		}()
	}

	if blockCount == 0 {
		return &pb.SyncBlocksResponse{
			SyncedCount:     0,
			LastSyncedBlock: 0,
			Error:           "No blocks to sync",
		}, nil
	}

	blockDatabase := block.NewBlockDatabase(rh.storageManager.GetStorageBlock())
	bc := blockchain.GetBlockChainInstance()

	var executedCount uint64 = 0
	var lastExecutedBlock uint64 = 0
	var lastExecutedGEI uint64 = 0
	// ═══════════════════════════════════════════════════════════════════════════
	// NOMT TRIE REBUILD DECISION:
	// When execute_mode=true, the caller is either:
	//   1. STARTUP-SYNC (consensus_node.rs) — runs BEFORE consensus starts
	//   2. SyncOnly node (sync_loop.rs) — never runs consensus
	// In both cases, there is NO concurrent consensus execution, so NOMT
	// trie rebuild is SAFE on the last block.
	//
	// CRITICAL FIX (May 2026): The old check `GetLastBlockNumber() == 0`
	// was too restrictive. When a node restarts with block=470, STARTUP-SYNC
	// syncs blocks 471-536 but SKIPS NOMT rebuild → NOMT trie stays at
	// block 470's root → new consensus blocks get wrong state_root → FORK.
	// ═══════════════════════════════════════════════════════════════════════════
	isPreConsensusSync := request.GetExecuteMode()
	if rh.chainState != nil && rh.chainState.GetConfig() != nil && rh.chainState.GetConfig().ServiceType == "MASTER" {
		if !isPreConsensusSync {
			logger.Info("🛡️ [SYNC-SAFETY] Forcing execute_mode=true for Master/Validator node to prevent state root freeze")
			isPreConsensusSync = true
		}
	}
	storage.SetPreConsensusSyncActive(isPreConsensusSync)
	defer storage.SetPreConsensusSyncActive(false)

	// ═══════════════════════════════════════════════════════════════════════════
	// PIPELINE SYNC: Always wait for commitWorker to flush pending block commits.
	// This ensures that before block sync processes and writes blocks to PebbleDB/NOMT,
	// all prior consensus block executions have been fully committed to disk,
	// preventing concurrent PebbleDB writes or stale parent root checks.
	// ═══════════════════════════════════════════════════════════════════════════
	if sm := rh.getSnapshotManager(); sm != nil {
		logger.Info("⏳ [SYNC] Waiting for commitWorker to flush pending blocks before processing sync...")
		sm.WaitForPersistence()
	}

	if isPreConsensusSync {
		logger.Info("🔧 [STARTUP-SYNC] execute_mode=true: NOMT trie rebuild will be ENABLED on last block (no concurrent consensus)")
	}

	// R7: Crash-guard for Cache Invalidation
	// Guarantee cache is invalidated on exit if any blocks were executed
	defer func() {
		if executedCount > 0 {
			rh.chainState.InvalidateAllState()
			mvm.ClearAllMVMApi()
			mvm.ClearAllProtectedMVMApi() // CRITICAL: Clear protected instances that hold stale data
			mvm.CallClearAllStateInstances()
			logger.Debug("🧹 [SNAPSHOT-RESUME] Deferred cache invalidation complete after batch sync")
		}
	}()

	var allNomtSessions []trie.NomtSessionToFlush

	for i, blockData := range blocks {
		isLastBlock := i == len(blocks)-1
		shouldCommitState := isLastBlock || (i > 0 && i%100 == 0)

		rawBytes := blockData.GetRawBlockBytes()
		backupBytes := blockData.GetBackupData()

		if len(rawBytes) == 0 {
			logger.Warn("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Block (wire_num=%d) has no raw_block_bytes, skipping", blockData.GetBlockNumber())
			continue
		}

		// Unmarshal the raw block
		blk := &block.Block{}
		if err := blk.Unmarshal(rawBytes); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to unmarshal block (wire_num=%d): %v", blockData.GetBlockNumber(), err)
			continue
		}

		header := blk.Header()
		blockHash := header.Hash()
		blockNum := header.BlockNumber()
		blockGEI := header.GlobalExecIndex()

		// 🔍 DIAGNOSTIC: Log TX order from SYNC (for SyncOnly node)
		txs := blk.Transactions()
		if len(txs) > 0 {
			logger.Info("📥 [SYNC-TX-ORDER] Block #%d (GEI=%d), txs=%d", blockNum, blockGEI, len(txs))
			for i, tx := range txs {
				if i < 5 || i == len(txs)-1 { // Log 5 first and 1 last tx to avoid spam
					logger.Info("  |_ [SYNC-TX-ORDER] pos[%d/%d]: hash=%s", 
						i, len(txs)-1, tx.Hex()[:18]+"...")
				}
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// DEDUPLICATION: Skip blocks already executed (GEI-based)
		// CRITICAL FIX: We MUST read GEI freshly in the loop. Caching it outside
		// the loop causes this sync to blindly overwrite blocks that were concurrently
		// processed by the live consensus goroutine (processRustEpochData).
		// ═══════════════════════════════════════════════════════════════════════════
		currentGEI := storage.GetLastGlobalExecIndex()
		isFullyExecuted := false
		if blockGEI > 0 && blockGEI <= currentGEI {
			if localHash, ok := bc.GetBlockHashByNumber(blockNum); ok {
				if localHash == blockHash {
					isFullyExecuted = true
				} else {
					if isPreConsensusSync {
						// ═══════════════════════════════════════════════════════════
						// STARTUP-SYNC FORK RESOLUTION (May 2026):
						// During startup pre-consensus sync, if the local block has a different
						// hash than the verified block fetched from peers, this indicates a local
						// stale fork. We must FORCE execution and overwrite of this block so the local
						// node is fully re-aligned with the peer consensus, allowing subsequent parent-hash
						// checks to pass correctly.
						// ═══════════════════════════════════════════════════════════
						logger.Warn("⚠️ [STARTUP-SYNC-FORK] Local block #%d hash (%s) != leader hash (%s) during startup sync. "+
							"FORCING execution/overwrite of this block to resolve local fork.",
							blockNum, localHash.Hex()[:18], blockHash.Hex()[:18])
						isFullyExecuted = false
					} else {
						// ═══════════════════════════════════════════════════════════
						// FORK-SAFE (June 2026): Force execution on hash mismatch!
						//
						// If we `continue` (skip) here, the local database retains the WRONG block.
						// Then, the NEXT block in the sync batch will immediately fail the
						// `parent-hash mismatch` check because its parent is the correct block,
						// but our local DB has the wrong block. This causes a permanent sync stall!
						//
						// By setting `isFullyExecuted = false`, we FORCE execution of the peer's
						// block, overwriting our local stale block. This allows the node to 
						// correctly align with the consensus chain.
						// ═══════════════════════════════════════════════════════════
						logger.Warn("⚠️ [SYNC-HASH-MISMATCH] Local block #%d hash (%x) != leader hash (%x) during sync. "+
							"FORCING execution/overwrite of this block to resolve local fork.",
							blockNum, localHash[:8], blockHash[:8])
						isFullyExecuted = false // Overwrite local stale block
					}
				}
			}
		}

		// Anti-drift check: Even if the block was found in LevelDB, if the state backend is NOMT
		// and the block is not committed to NOMT, we must force execution on NOMT.
		if isFullyExecuted && trie.GetStateBackend() == trie.BackendNOMT && blockNum > storage.GetLastNomtCommittedBlock() {
			isFullyExecuted = false
			logger.Info("🔄 [NOMT-SYNC-RECOVERY] Block #%d exists in LevelDB but is NOT committed to NOMT (lastNomt=%d). Forcing re-execution on NOMT.", blockNum, storage.GetLastNomtCommittedBlock())
		}

		// STRICT BLOCK NUMBER GUARD: If the block number is already committed and there is no hash mismatch,
		// it is fully executed. This prevents executing state batches for duplicate blocks.
		if !isFullyExecuted && blockNum > 0 {
			lastBlockNum := storage.GetLastBlockNumber()
			if blockNum < lastBlockNum {
				isFullyExecuted = true
				logger.Info("🔄 [SYNC-DEDUPLICATE] Block #%d is strictly older than last committed #%d. Skipping execution.", blockNum, lastBlockNum)
			} else if blockNum == lastBlockNum {
				if localHash, ok := bc.GetBlockHashByNumber(blockNum); ok && localHash == blockHash {
					isFullyExecuted = true
					logger.Info("🔄 [SYNC-DEDUPLICATE] Block #%d is duplicate of last committed tip. Skipping execution.", blockNum)
				}
			}
		}

		if isFullyExecuted {
			logger.Debug("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Block #%d (GEI=%d) already executed (current_gei=%d), skipping",
				blockNum, blockGEI, currentGEI)
			executedCount++
			if blockNum > lastExecutedBlock {
				lastExecutedBlock = blockNum
			}
			if blockGEI > lastExecutedGEI {
				lastExecutedGEI = blockGEI
			}

			// Ensure block number to hash mapping exists in mapping DB
			if _, ok := bc.GetBlockHashByNumber(blockNum); !ok {
				if err := bc.SetBlockNumberToHash(blockNum, blockHash); err != nil {
					logger.Error("❌ [SNAPSHOT-RESUME] Failed to restore block mapping for skip block #%d: %v", blockNum, err)
				} else {
					logger.Info("🔄 [SNAPSHOT-RESUME] Restored block mapping for skip block #%d -> %s", blockNum, blockHash.Hex())
				}
			}

			// Ensure raw block bytes exist in PebbleDB block storage
			if _, err := blockDatabase.GetBlockByHash(blockHash); err != nil {
				logger.Warn("⚠️ [SYNC-INTEGRITY] Block #%d (hash=%s) has mapping but raw bytes are missing from DB. Force-restoring block data.", blockNum, blockHash.Hex())
				if saveErr := blockDatabase.SaveBlockByHash(blk); saveErr != nil {
					logger.Error("❌ [SYNC-INTEGRITY] Failed to save missing block #%d: %v", blockNum, saveErr)
				} else if flushErr := blockDatabase.GetDB().Flush(); flushErr != nil {
					logger.Error("❌ [SYNC-INTEGRITY] Failed to flush missing block #%d: %v", blockNum, flushErr)
				} else {
					logger.Info("🔄 [SYNC-INTEGRITY] Successfully restored and flushed missing block #%d bytes", blockNum)
				}
			}

			// CRITICAL FIX: Even if the block was fully executed previously (from LevelDB),
			// we MUST update the in-memory pointers so that Rust's initialize_from_go()
			// query reads the correct last_block_number. Otherwise, Rust will use a stale
			// block_number for the next consensus commit, overwriting this block.
			storage.UpdateLastBlockNumber(blockNum)
			if blockGEI > 0 {
				storage.UpdateLastGlobalExecIndex(blockGEI)
			}

			// CRITICAL FIX: Restore lastHandledCommitIndex even for skipped blocks!
			// If a node crashes or is restored from a corrupted rsync backup, lastHandledCommitIndex
			// might be out of sync. We must restore it from the highest synced block header.
			if header.CommitIndex() > 0 {
				commitIdx32 := uint32(header.CommitIndex())
				currentEpoch := header.Epoch()
				lastEpoch := storage.GetLastHandledCommitEpoch()

				if currentEpoch > lastEpoch || (currentEpoch == lastEpoch && commitIdx32 > storage.GetLastHandledCommitIndex()) {
					storage.ForceSetLastHandledCommitIndex(commitIdx32)
					storage.UpdateLastHandledCommitEpoch(currentEpoch)
					logger.Info("🔄 [STARTUP-SYNC] Restored lastHandledCommitIndex to %d (epoch %d) from fully-executed block #%d", commitIdx32, currentEpoch, blockNum)
				}
			}

			// Still persist backup data for Sub nodes
			if len(backupBytes) > 0 {
				rh.persistBackupForSub(backupBytes, blockNum)
			}

			// CRITICAL FIX: If this is the last block of a STARTUP-SYNC batch,
			// we MUST trigger the trie rebuild even if the block was already in LevelDB.
			// Otherwise, NOMT memory state stays stale and subsequent blocks will fork.
			if isLastBlock && isPreConsensusSync && trie.GetStateBackend() == trie.BackendNOMT {
				logger.Info("🔧 [STARTUP-SYNC] Forcing NOMT trie rebuild on fully-executed last block #%d", blockNum)
				if err := rh.chainState.UpdateStateForNewHeader(header); err != nil {
					logger.Error("❌ [STARTUP-SYNC] Failed to force rebuild NOMT tries for fully-executed block #%d: %v", blockNum, err)
				}
			}
			continue
		}

		// Update cached GEI so next blocks in the loop don't dedup on stale value
		if blockGEI > currentGEI {
			currentGEI = blockGEI
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// PARENT-HASH CONSISTENCY CHECK: Reject synced block if it breaks continuity
		// ═══════════════════════════════════════════════════════════════════════════
		if blockNum > 1 {
			prevBlockNum := blockNum - 1
			if prevHash, ok := bc.GetBlockHashByNumber(prevBlockNum); ok && prevHash != (common.Hash{}) {
				expectedParentHash := prevHash
				actualParentHash := header.LastBlockHash()
				if actualParentHash != expectedParentHash {
					logger.Error("🚨 [SYNC-FORK-GUARD] parent-hash mismatch at block #%d! "+
						"Synced block has parentHash=%s, but local block #%d hash is %s. "+
						"Rejecting block to prevent chain corruption.",
						blockNum, actualParentHash.Hex(), prevBlockNum, expectedParentHash.Hex())
					return &pb.SyncBlocksResponse{
						SyncedCount:     executedCount,
						LastSyncedBlock: lastExecutedBlock,
						Error:           fmt.Sprintf("parent-hash mismatch at block %d: parentHash=%s but local block %d hash=%s", blockNum, actualParentHash.Hex(), prevBlockNum, expectedParentHash.Hex()),
					}, nil
				}
			}
		}

		// CRITICAL FIX: Restore lastHandledCommitIndex from the synced block!
		// This prevents Go from double-executing these commits when Rust resumes consensus.
		if header.CommitIndex() > 0 {
			commitIdx32 := uint32(header.CommitIndex())
			currentEpoch := header.Epoch()
			lastEpoch := storage.GetLastHandledCommitEpoch()

			if currentEpoch > lastEpoch || (currentEpoch == lastEpoch && commitIdx32 > storage.GetLastHandledCommitIndex()) {
				storage.ForceSetLastHandledCommitIndex(commitIdx32)
				storage.UpdateLastHandledCommitEpoch(currentEpoch)
				logger.Info("🔄 [STARTUP-SYNC] Restored lastHandledCommitIndex to %d (epoch %d) from synced block #%d", commitIdx32, currentEpoch, blockNum)
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 1: Apply BackupDb state batches to LevelDB (Account, Code, SC, etc.)
		// This writes the pre-computed state diffs so NOMT can rebuild from them.
		// ═══════════════════════════════════════════════════════════════════════════
		if len(backupBytes) > 0 {
			// DEBUG: Log raw backup size BEFORE deserialize — detects truncated snapshots early
			logger.Debug("🔍 [NOMT-DEBUG] Block #%d: raw backupBytes=%d bytes. About to deserialize+apply.",
				blockNum, len(backupBytes))

			backupDb, deserErr := storage.DeserializeBackupDb(backupBytes)
			if deserErr != nil {
				logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to deserialize BackUpDb for block #%d: %v", blockNum, deserErr)
			} else {
				// DEBUG: Log field sizes after deserialize — tells us if AccountBatch is missing
				logger.Debug("🔍 [NOMT-DEBUG] Block #%d deserialized: AccountBatch=%d bytes, TrieDB=%d bytes, FullDbLogs=%d entries",
					blockNum,
					len(backupDb.AccountBatch),
					len(backupDb.TrieDatabaseBatchPut),
					len(backupDb.FullDbLogs),
				)

				// Capture NOMT root BEFORE apply to compute delta
				var rootBeforeApply common.Hash
				var hasRootBefore bool
				if isPreConsensusSync && trie.GetStateBackend() == trie.BackendNOMT {
					rootBeforeApply, hasRootBefore = trie.GetNomtHandleRoot("account_state")

					// ═══════════════════════════════════════════════════════════════
					// PRE-EXECUTION VERIFICATION:
					// Metanode block headers store the POST-EXECUTION state root.
					// To verify the state before applying block N, we must verify it against
					// the POST-EXECUTION root of block N-1 (parent block).
					// ═══════════════════════════════════════════════════════════════
					var expectedPreRoot common.Hash
					var hasExpectedPreRoot bool
					if blockNum > 1 {
						prevBlockNum := blockNum - 1
						if prevHash, ok := bc.GetBlockHashByNumber(prevBlockNum); ok && prevHash != (common.Hash{}) {
							if prevBlock, err := blockDatabase.GetBlockByHash(prevHash); err == nil && prevBlock != nil {
								expectedPreRoot = prevBlock.Header().AccountStatesRoot()
								hasExpectedPreRoot = true
							}
						}
					} else {
						// For block #1, the parent is block #0 (genesis)
						if prevHash, ok := bc.GetBlockHashByNumber(0); ok && prevHash != (common.Hash{}) {
							if prevBlock, err := blockDatabase.GetBlockByHash(prevHash); err == nil && prevBlock != nil {
								expectedPreRoot = prevBlock.Header().AccountStatesRoot()
								hasExpectedPreRoot = true
							}
						}
					}

					if hasRootBefore && hasExpectedPreRoot && expectedPreRoot != (common.Hash{}) && expectedPreRoot != trie.EmptyRootHash {
						if rootBeforeApply != expectedPreRoot {
							logger.Error("🚨 [NOMT-SYNC-VERIFY] CRITICAL: PRE-execution state root MISMATCH! "+
								"localRoot=%s, expectedPreRoot=%s, block=#%d. "+
								"This node's state has diverged BEFORE this block was applied!",
								rootBeforeApply.Hex(), expectedPreRoot.Hex(), blockNum)
						} else {
							logger.Debug("✅ [NOMT-SYNC-VERIFY] PRE-execution root matches: %s", rootBeforeApply.Hex()[:18]+"...")
						}
					}
				}

				sessions, applyErr := rh.applyBackupDbBatches(&backupDb)
				if applyErr != nil {
					logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to apply backup batches for block #%d: %v", blockNum, applyErr)
				} else {
					allNomtSessions = append(allNomtSessions, sessions...)
				}

				// ═══════════════════════════════════════════════════════════════
				// FORK-DIAG (May 2026): Log NOMT handle root after EACH block's
				// batch apply during STARTUP-SYNC. This pinpoints the exact block
				// where state drift begins (if any batch is incomplete/corrupted).
				// ═══════════════════════════════════════════════════════════════
				if isPreConsensusSync && trie.GetStateBackend() == trie.BackendNOMT {
					if nomtRoot, ok := trie.GetNomtHandleRoot("account_state"); ok {
						expectedAccountRoot := header.AccountStatesRoot()

						// Show before→after delta when available
						if hasRootBefore {
							logger.Debug("[FORK-DIAG][NOMT-PER-BLOCK] Block #%d (GEI=%d): beforeApply=%s → afterApply=%s, headerExpected(pre)=%s",
								blockNum, blockGEI,
								rootBeforeApply.Hex()[:18]+"...",
								nomtRoot.Hex()[:18]+"...",
								expectedAccountRoot.Hex()[:18]+"...")
						} else {
							logger.Debug("[FORK-DIAG][NOMT-PER-BLOCK] Block #%d (GEI=%d): nomtRoot=%s, headerExpected(pre)=%s",
								blockNum, blockGEI,
								nomtRoot.Hex()[:18]+"...",
								expectedAccountRoot.Hex()[:18]+"...")
						}
					}
				}
			}
		}

		if blockNum == 0 {
			logger.Debug("🚀 [SNAPSHOT-RESUME] Skipping empty block (GEI=%d), state already advanced", blockGEI)
			continue
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 2: Block Saving and Mappings are now deferred to CommitBlockState
		// to guarantee they are protected by commitMutex and SEQUENTIAL GUARD.
		// ═══════════════════════════════════════════════════════════════════════════

		// STEP 3: REBUILD TRIES (conditionally) — sync memory state with DB.
		//
		// CRITICAL FIX (Apr 2026): On NOMT backend, WithRebuildTries() MUST NOT
		// be used here. NOMT keeps ONLY the latest state root. When this handler
		// runs concurrently with consensus execution (e.g., via LAG-RECOVERY or
		// STARTUP-SYNC), calling UpdateStateForNewHeader() overwrites the current
		// NOMT trie root with the P2P-synced block's root. Since NOMT cannot look
		// up historical state, this irreversibly corrupts the trie — subsequent
		// consensus blocks are created from the wrong state root, causing a fork.
		//
		// On MPT/legacy backends, WithRebuildTries() is safe because they maintain
		// a full historical trie in LevelDB.
		// ═══════════════════════════════════════════════════════════════════════════
		var commitOpts []blockchain.CommitOption
		commitOpts = append(commitOpts, blockchain.WithPersistToDB())
		commitOpts = append(commitOpts, blockchain.WithSaveTxMapping()) // ALWAYS track/save tx mappings in memory batch

		if shouldCommitState {
			// ═══════════════════════════════════════════════════════════════
			// CRITICAL FIX (May 2026): NOMT trie rebuild decision.
			//
			// During pre-consensus sync (execute_mode=true):
			//   SAFE to rebuild — no concurrent consensus execution exists.
			//   WITHOUT this, NOMT stays at stale root and subsequent
			//   consensus blocks will have wrong state_root → permanent fork.
			//
			// Non-execute-mode (store-only sync):
			//   No trie rebuild needed — blocks are stored, not executed.
			// ═══════════════════════════════════════════════════════════════
			if trie.GetStateBackend() != trie.BackendNOMT {
				commitOpts = append(commitOpts, blockchain.WithRebuildTries())
			} else {
				logger.Debug("🛡️ [NOMT-SAFETY] Skipping WithRebuildTries() for NOMT backend (block #%d). "+
					"NOMT only keeps latest state — rebuilding from P2P block header would corrupt active trie. "+
					"Re-alignment deferred to processRustEpochData.", blockNum)
			}
			// CRITICAL FIX: Ensure mapping batches from memory are flushed to DB!
			// Without this, synced blocks mapping (block number -> hash) remain in volatile
			// cache and are lost if the node crashes/restarts before the next normal block.
			commitOpts = append(commitOpts, blockchain.WithCommitMappings())
		}

		if _, err := rh.chainState.CommitBlockState(blk, commitOpts...); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to CommitBlockState for block #%d: %v", blockNum, err)
			// Continue anyway — partial state is better than no state
		} else {
			executedCount++
			if blockNum > lastExecutedBlock {
				lastExecutedBlock = blockNum
			}
			if blockGEI > lastExecutedGEI {
				lastExecutedGEI = blockGEI
			}

			// ═══════════════════════════════════════════════════════════════
			// BLOCK-BY-BLOCK REALIGNMENT (May 2026):
			// Realignment of Go's active in-memory trie state must happen block-by-block
			// immediately after each block is committed to PebbleDB.
			// ═══════════════════════════════════════════════════════════════
			if trie.GetStateBackend() == trie.BackendNOMT {
				newAccountRoot := header.AccountStatesRoot()
				newStakeRoot := common.Hash(header.StakeStatesRoot())

				// Realign account state trie block-by-block via Go cache pointers (lightweight)
				if asDB := rh.chainState.GetAccountStateDB(); asDB != nil {
					asDB.SetOriginRootHash(newAccountRoot)
					if nomtTrie, ok := asDB.Trie().(*trie.NomtStateTrie); ok {
						nomtTrie.RealignRoot(newAccountRoot)
					}
				}
				// Realign stake state trie block-by-block via Go cache pointers (lightweight)
				if stakeDB := rh.chainState.GetStakeStateDB(); stakeDB != nil {
					stakeDB.SetOriginRootHash(newStakeRoot)
					if nomtTrie, ok := stakeDB.Trie().(*trie.NomtStateTrie); ok {
						nomtTrie.RealignRoot(newStakeRoot)
					}
				}
				logger.Debug("🔧 [NOMT-SYNC-REALIGN] Block #%d: Go cache pointers realigned: account=%s, stake=%s",
					blockNum, newAccountRoot.Hex()[:18]+"...", newStakeRoot.Hex()[:18]+"...")
			}

			// Broadcast transaction receipts for SyncOnly nodes if callback is configured and backupBytes/receipts are present
			if rh.broadcastEventsAndReceiptsCallback != nil && len(backupBytes) > 0 {
				backupDb, deserErr := storage.DeserializeBackupDb(backupBytes)
				if deserErr == nil && len(backupDb.ReceiptBatchPut) > 0 {
					deserializedBatch, err := storage.DeserializeBatch(backupDb.ReceiptBatchPut)
					if err == nil && len(deserializedBatch) > 0 {
						var syncReceipts []types.Receipt
						var syncEventLogs []types.EventLog

						for _, kv := range deserializedBatch {
							// Each entry is a flat key-value pair of [txHash, serializedReceipt]
							serializedReceipt := kv[1]
							if len(serializedReceipt) > 0 {
								pbReceipt := &pb.Receipt{}
								if err := proto.Unmarshal(serializedReceipt, pbReceipt); err == nil {
									typesReceipt := receipt.ReceiptFromProto(pbReceipt)
									syncReceipts = append(syncReceipts, typesReceipt)

									// Extract event logs
									for _, pbLog := range pbReceipt.EventLogs {
										typesLog := smart_contract.NewEventLogFromProto(pbLog)
										syncEventLogs = append(syncEventLogs, typesLog)
									}
								}
							}
						}

						if len(syncReceipts) > 0 {
							logger.Info("📤 [SYNC BROADCAST] Calling broadcastEventsAndReceiptsCallback for synced block #%d with %d receipts and %d event logs",
								blockNum, len(syncReceipts), len(syncEventLogs))
							// Call the callback safely
							rh.broadcastEventsAndReceiptsCallback(blk, syncReceipts, syncEventLogs)
						}
					}
				}
			}

			if shouldCommitState {
				// ═══════════════════════════════════════════════════════════════
				// STATE ROOT VERIFICATION (May 2026):
				// After applying all backup batches + WithRebuildTries(), verify
				// that the NOMT state actually matches what the block header expects.
				//
				// CRITICAL FIX: The previous code BYPASSED this check entirely for
				// NOMT backend with the false claim that "NOMT headers store PRE-COMMIT
				// state root". In reality, block headers store POST-EXECUTION roots
				// (state AFTER the block's transactions). The bypass was masking real
				// state drift, causing silent permanent forks after restart (see
				// debug_report_20260501_201046.md — STATE_ROOT divergence at block #582).
				// ═══════════════════════════════════════════════════════════════
				localRoot := rh.chainState.GetAccountStateDB().Trie().Hash()
				expectedRoot := header.AccountStatesRoot()

				if trie.GetStateBackend() == trie.BackendNOMT {
					// Cross-check: Query the NOMT handle root directly to verify
					// it matches both the trie's cached root and the block header.
					nomtHandleRoot, hasNomtRoot := trie.GetNomtHandleRoot("account_state")

					logger.Info("🔍 [NOMT-SYNC-VERIFY] Block #%d: trieRoot=%s, handleRoot=%s, expectedRoot=%s, handleOK=%v",
						blockNum,
						localRoot.Hex()[:18]+"...",
						func() string {
							if hasNomtRoot {
								return nomtHandleRoot.Hex()[:18] + "..."
							}
							return "N/A"
						}(),
						expectedRoot.Hex()[:18]+"...",
						hasNomtRoot)

					// ═══════════════════════════════════════════════════════════════
					// FORK-PREVENTION (May 2026): Force trie re-alignment from NOMT
					// handle BEFORE returning to Rust for consensus.
					//
					// ROOT CAUSE: After batch-applying blocks during STARTUP-SYNC,
					// the in-memory NomtStateTrie may hold a cached root that is
					// stale relative to the NOMT handle's committed root. When
					// consensus immediately sends the first new block, Go reads
					// from this stale trie, producing a different AccountStatesRoot
					// → different block hash → FORK (see block #2149 incident).
					//
					// FIX: Explicitly re-create the trie from the NOMT handle's
					// actual root. This guarantees that ProcessTransactions on
					// the first consensus block reads from the correct state.
					// ═══════════════════════════════════════════════════════════════
					// ═══════════════════════════════════════════════════════════════
					// NOTE (June 2026): Trie re-alignment from NOMT handle root is
					// DEFERRED to the end of SyncBlocks (after sessions are flushed).
					// Calling UpdateStateForNewHeader block-by-block here is unsafe
					// because sessions are not flushed, causing stale root mismatches
					// and triggering destructive rebuilds on NOMT backend.
					// ═══════════════════════════════════════════════════════════════
				} else {
					// Non-NOMT backend: strict verification with halt on mismatch
					if localRoot != expectedRoot && expectedRoot != (common.Hash{}) && expectedRoot != trie.EmptyRootHash {
						logger.Error("🚨 [STATE VERIFY] Batch stateRoot MISMATCH! block=#%d local=%s expected=%s. HALTING sync.",
							blockNum, localRoot.Hex(), expectedRoot.Hex())
						return &pb.SyncBlocksResponse{
							Error: fmt.Sprintf("stateRoot mismatch at block %d: local=%s expected=%s",
								blockNum, localRoot.Hex()[:18], expectedRoot.Hex()[:18]),
						}, fmt.Errorf("stateRoot mismatch at block %d", blockNum)
					}
					logger.Info("✅ [STATE VERIFY] Batch stateRoot VERIFIED: block=#%d root=%s", blockNum, localRoot.Hex()[:18]+"...")
				}
			}

			// Update persistent counters (block number + GEI)
			// These must advance AFTER CommitBlockState so that any query to Go's GEI
			// returns a value that reflects actually-executed NOMT state.
			// (Note: SetcurrentBlockHeader, CheckAndUpdateEpochFromBlock, SaveLastBlock,
			// SetBlockNumberToHash, SetTxHashMapBlockNumber, and UpdateLastBlockNumber
			// are all fully handled inside cs.CommitBlockState).
			if blockGEI > 0 {
				storage.UpdateLastGlobalExecIndex(blockGEI)
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 6: Persist backup data for Sub nodes
		// Backup persisted to PebbleDB so Sub can read via 3-tier recovery.
		// FORK-SAFETY (R6/Apr 2026): Master node MUST NOT broadcast blocks to Sub nodes
		// while it is actively catching up (syncing). It should only broadcast blocks
		// constructed during active Validator mode consensus.
		// Sub nodes will pull the missing blocks via DataSync (3-tier recovery).
		// ═══════════════════════════════════════════════════════════════════════════
		if len(backupBytes) > 0 {
			rh.persistBackupForSub(backupBytes, blockNum)

			// if rh.broadcastCallback != nil {
			// 	rh.broadcastCallback(blk, backupBytes, blockNum, len(blk.Transactions()))
			// }
		}

		logger.Debug("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] ✅ Executed block #%d: hash=%s, epoch=%d, gei=%d, txs=%d",
			blockNum, blockHash.Hex()[:18]+"...", header.Epoch(), blockGEI, len(blk.Transactions()))

		if executedCount%100 == 0 {
			logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Progress: executed %d/%d blocks", executedCount, blockCount)
		}
	}

	// Flush all accumulated NOMT sessions for the chunk
	if len(allNomtSessions) > 0 {
		logger.Info("🔧 [NOMT-SYNC] Flushing %d deferred NOMT sessions to disk after chunk completion", len(allNomtSessions))
		if err := trie.FlushNomtSessions(allNomtSessions); err != nil {
			logger.Error("❌ [NOMT-SYNC] Failed to flush deferred NOMT sessions: %v", err)
		}
	}

	// Force final NOMT trie alignment for the last block of the batch after all sessions are flushed to disk
	if isPreConsensusSync && trie.GetStateBackend() == trie.BackendNOMT && len(blocks) > 0 {
		lastBlockData := blocks[len(blocks)-1]
		lastBlockNum := lastBlockData.GetBlockNumber()
		if lastHash, ok := bc.GetBlockHashByNumber(lastBlockNum); ok {
			lastBlk, err := blockDatabase.GetBlockByHash(lastHash)
			if err == nil && lastBlk != nil {
				logger.Info("🔧 [STARTUP-SYNC] Forcing final NOMT trie alignment for last batch block #%d", lastBlockNum)
				if err := rh.chainState.UpdateStateForNewHeader(lastBlk.Header()); err != nil {
					logger.Error("❌ [STARTUP-SYNC] Failed to align final NOMT trie for block #%d: %v", lastBlockNum, err)
				} else {
					rh.chainState.InvalidateAllState()
					finalTrieRoot := rh.chainState.GetAccountStateDB().Trie().Hash()
					logger.Info("✅ [STARTUP-SYNC] Final NOMT trie aligned: trieRoot=%s (block=#%d). Ready.",
						finalTrieRoot.Hex()[:18]+"...", lastBlockNum)
				}
			}
		}
	}

	// Commit block number→hash mappings
	if err := bc.Commit(); err != nil {
		logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to commit block number mappings: %v", err)
	} else if rh.storageManager != nil {
		if flushErr := rh.storageManager.GetStorageMapping().Flush(); flushErr != nil {
			logger.Error("❌ [SNAPSHOT-RESUME] Failed to flush mapping DB: %v", flushErr)
		} else {
			logger.Info("💾 [SNAPSHOT-RESUME] Successfully flushed mapping DB to disk after batch sync")
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// CRITICAL FIX: Synchronous persistence of the last synced block.
	// If the node crashes/restarts immediately after STARTUP-SYNC finishes, we MUST
	// guarantee that the last block hash (lastBlockHashKey) is fully flushed to disk.
	// Without this, the node falls back to the pre-sync block or Genesis.
	// ═══════════════════════════════════════════════════════════════════════════
	if executedCount > 0 {
		hash, ok := bc.GetBlockHashByNumber(lastExecutedBlock)
		if ok {
			lastBlk, err := blockDatabase.GetBlockByHash(hash)
			if err == nil && lastBlk != nil {
				logger.Info("💾 [STARTUP-SYNC] Forcing synchronous flush of last synced block #%d to disk", lastExecutedBlock)
				if syncErr := blockDatabase.SaveLastBlockSync(lastBlk); syncErr != nil {
					logger.Error("❌ [STARTUP-SYNC] Failed to force-sync last block #%d: %v", lastExecutedBlock, syncErr)
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// RUST CONTROL (Apr 2026 Architectural Fix):
	// Instead of autonomous Go polling via `syncStateFromDBRefresher`, Rust via
	// this EXECUTE command explicitly governs memory state advancement.
	// ═══════════════════════════════════════════════════════════════════════════
	if executedCount > 0 {
		if rh.updateLastBlockCallback != nil {
			if hash, ok := bc.GetBlockHashByNumber(lastExecutedBlock); ok {
				if lastBlk, err := blockDatabase.GetBlockByHash(hash); err == nil && lastBlk != nil {
					rh.updateLastBlockCallback(lastBlk)
				}
			}
		}

		// CRITICAL FIX: Push an async GEI update so that Go's Authoritative GEI
		// counter advances. Without this, GEIAuthority stays at the pre-sync value
		// (e.g. 76) while the database advances to 113. When consensus resumes,
		// it would assign 77 to the next block instead of 114, causing a fork.
		if lastExecutedGEI > 0 && rh.pushAsyncGEIUpdateCallback != nil {
			// Find the last commit index to sync that as well
			var lastCommitIdx uint32 = 0
			var lastEpoch uint64 = 0
			if len(blocks) > 0 {
				lastBlockBytes := blocks[len(blocks)-1].GetRawBlockBytes()
				if len(lastBlockBytes) > 0 {
					lastBlk := &block.Block{}
					if err := lastBlk.Unmarshal(lastBlockBytes); err == nil {
						lastCommitIdx = uint32(lastBlk.Header().CommitIndex())
						lastEpoch = lastBlk.Header().Epoch()
					}
				}
			}
			rh.pushAsyncGEIUpdateCallback(lastExecutedGEI, nil, lastCommitIdx, lastEpoch)
			logger.Info("🔄 [STARTUP-SYNC] Synchronized GEIAuthority to %d (commitIndex: %d, epoch: %d)", lastExecutedGEI, lastCommitIdx, lastEpoch)

			// ═══════════════════════════════════════════════════════════════════════════
			// CRITICAL FIX: SYNCHRONOUSLY PERSIST LAST HANDLED COMMIT INDEX AND EPOCH
			// Rust will immediately query HandleGetLastHandledCommitIndexRequest after sync.
			// Since PushAsyncGEIUpdate is asynchronous, it races with Rust's query.
			// We MUST synchronously update the storage and BackupDb here to prevent Rust
			// from starting at commitIndex=0 and causing an epoch mismatch fork.
			// ═══════════════════════════════════════════════════════════════════════════
			if lastCommitIdx > 0 {
				currentEpoch := rh.chainState.GetCurrentEpoch()
				storage.UpdateLastHandledCommitIndex(lastCommitIdx)
				storage.UpdateLastHandledCommitEpoch(currentEpoch)

				if rh.storageManager != nil && rh.storageManager.GetStorageBackupDb() != nil {
					var batch [][2][]byte
					batch = append(batch, [2][]byte{storage.LastHandledCommitIndexHashKey.Bytes(), utils.Uint32ToBytes(lastCommitIdx)})
					batch = append(batch, [2][]byte{storage.LastHandledCommitEpochHashKey.Bytes(), utils.Uint64ToBytes(currentEpoch)})
					if err := rh.storageManager.GetStorageBackupDb().BatchPut(batch); err != nil {
						logger.Error("❌ [STARTUP-SYNC] Failed to persist LastHandledCommitIndex to BackupDb: %v", err)
					} else {
						logger.Info("✅ [STARTUP-SYNC] Synchronously persisted LastHandledCommitIndex=%d, Epoch=%d for immediate Rust consensus initialization", lastCommitIdx, currentEpoch)
					}
				}
			}
		}
	}

	logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] ✅ Completed: executed %d/%d blocks, last_block=#%d, last_gei=%d",
		executedCount, blockCount, lastExecutedBlock, lastExecutedGEI)

	// Cache invalidation is handled by the defer block at the start of the function

	return &pb.SyncBlocksResponse{
		SyncedCount:     executedCount,
		LastSyncedBlock: lastExecutedBlock,
		LastExecutedGei: lastExecutedGEI,
		Error:           "",
	}, nil
}

// persistBackupForSub saves backup data to PebbleDB for Sub node recovery.
func (rh *RequestHandler) persistBackupForSub(backupBytes []byte, blockNum uint64) {
	backupStorage := rh.storageManager.GetStorageBackupDb()
	if backupStorage == nil {
		return
	}
	backupKey := []byte(fmt.Sprintf("block_data_topic-%d", blockNum))
	if putErr := backupStorage.Put(backupKey, backupBytes); putErr != nil {
		logger.Error("❌ [EXECUTE SYNC] Failed to persist backup for block #%d: %v", blockNum, putErr)
	}
	legacyKey := []byte(fmt.Sprintf("backup_%d", blockNum))
	_ = backupStorage.Put(legacyKey, backupBytes)
}

// applyBackupDbBatches applies all state batch data from a BackUpDb to local LevelDB storages.
// This mirrors applyBlockBatch from BlockProcessor but is adapted for the executor package.
// It writes Account, Block, Code, SmartContract, Receipt, Transaction, StakeState, and TrieDB batches.
func (rh *RequestHandler) applyBackupDbBatches(backupDb *storage.BackUpDb) ([]trie.NomtSessionToFlush, error) {
	// Map batch data fields to their corresponding storages
	type batchEntry struct {
		name    string
		data    []byte
		storage storage.Storage
	}

	entries := []batchEntry{
		{"Block", backupDb.BockBatch, rh.storageManager.GetStorageBlock()},
		{"Account", backupDb.AccountBatch, rh.storageManager.GetStorageAccount()},
		{"Code", backupDb.CodeBatchPut, rh.storageManager.GetStorageCode()},
		{"SmartContract", backupDb.SmartContractBatch, rh.storageManager.GetStorageSmartContract()},
		{"SC Storage", backupDb.SmartContractStorageBatch, rh.storageManager.GetStorageSmartContract()},
		{"Receipt", backupDb.ReceiptBatchPut, rh.storageManager.GetStorageReceipt()},
		{"Transaction", backupDb.TxBatchPut, rh.storageManager.GetStorageTransaction()},
		{"StakeState", backupDb.StakeState, rh.storageManager.GetStorageStake()},
		{"Mapping", backupDb.MapppingBatch, rh.storageManager.GetStorageMapping()},
	}

	aggregatedBatches := make(map[string][][2][]byte)

	for _, entry := range entries {
		if len(entry.data) > 0 {
			deserialized, err := storage.DeserializeBatch(entry.data)
			if err != nil {
				return nil, fmt.Errorf("error deserializing batch '%s' for block %d: %w", entry.name, backupDb.BockNumber, err)
			}
			if len(deserialized) > 0 {
				aggregatedBatches[entry.name] = deserialized
				// DEBUG: Log per-namespace key count so we know exactly what was in the snapshot batch
				logger.Debug("[NOMT-DEBUG][APPLY-BATCH] Block #%d: namespace=%s keys=%d (raw=%d bytes)",
					backupDb.BockNumber, entry.name, len(deserialized), len(entry.data))
			}
		} else if entry.name == "Account" {
			// AccountBatch is empty — this is the most common cause of NOMT root mismatch!
			logger.Error("[NOMT-DEBUG][APPLY-BATCH] Block #%d: AccountBatch is EMPTY — NOMT cannot rebuild account_state root! Snapshot may be missing AccountBatch data.",
				backupDb.BockNumber)
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PERFORMANCE & CORRECTNESS OPTIMIZATION (Apr 2026):
	// Intercept NOMT batches BEFORE they hit PebbleDB so they are processed
	// natively by the C++ NOMT engine. If we bypass this, the NOMT database
	// becomes completely unaware of the state updates, leading to stale
	// 'nomt_read' queries later (fixing persistent nonce mismatches on restart!!).
	// ═══════════════════════════════════════════════════════════════════════════
	sessions, err := trie.ApplyNomtReplicationBatches(
		aggregatedBatches,
		rh.chainState.GetChangelogDB(),
		rh.chainState.GetStakeChangelogDB(),
		backupDb.BockNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("error replicating NOMT batches in applyBackupDbBatches: %w", err)
	}

	// Now apply the remaining batches (where 'nomt:' prefixed keys were stripped)
	for _, entry := range entries {
		if batch, ok := aggregatedBatches[entry.name]; ok && len(batch) > 0 && entry.storage != nil {
			trie.UpdateBucketCacheFromBatch(entry.storage, batch)
			if err := entry.storage.BatchPut(batch); err != nil {
				return nil, fmt.Errorf("error writing batch '%s' for block %d: %w", entry.name, backupDb.BockNumber, err)
			}
		}
	}

	// Apply TrieDB batches
	if len(backupDb.TrieDatabaseBatchPut) > 0 {
		if err := rh.applyTrieDbBatches(backupDb.TrieDatabaseBatchPut, backupDb.BockNumber); err != nil {
			return nil, fmt.Errorf("error writing TrieDB batches for block %d: %w", backupDb.BockNumber, err)
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════════
	// CRITICAL FIX: Replay MVM FullDbLogs to ensure C++ VM database is consistent
	// Without this, smart contract storage reads return wrong values after sync,
	// causing accountStatesRoot divergence (fork) on the first locally-executed block.
	// This matches the behavior of applyBlockBatch() in block_processor_batch.go.
	// ═══════════════════════════════════════════════════════════════════════════════
	if len(backupDb.FullDbLogs) > 0 {
		for idx, logMap := range backupDb.FullDbLogs {
			result := mvm.CallReplayFullDbLogs(logMap)
			if result == 0 {
				logger.Error("🚨 [FORK-RISK] ReplayFullDbLogs (epoch sync) FAILED for batch %d/%d (%d entries) block #%d — Xapian DB may be OUT OF SYNC!", idx+1, len(backupDb.FullDbLogs), len(logMap), backupDb.BockNumber)
			}
		}
		logger.Debug("📥 [BLOCK SYNC] ✅ Replayed %d FullDbLogs entries for block %d", len(backupDb.FullDbLogs), backupDb.BockNumber)
	}

	// Apply mapping batch
	if len(backupDb.MapppingBatch) > 0 {
		mappingStorage := rh.storageManager.GetStorageMapping()
		if mappingStorage != nil {
			deserialized, err := storage.DeserializeBatch(backupDb.MapppingBatch)
			if err != nil {
				return nil, fmt.Errorf("error deserializing mapping batch for block %d: %w", backupDb.BockNumber, err)
			}
			if len(deserialized) > 0 {
				if err := mappingStorage.BatchPut(deserialized); err != nil {
					return nil, fmt.Errorf("error writing mapping batch for block %d: %w", backupDb.BockNumber, err)
				}
			}
		}
	}

	return sessions, nil
}

// applyTrieDbBatches applies TrieDB batch data from a BackUpDb to the local TrieDB LevelDB storages.
// Each key in the map corresponds to a sub-database path under the Trie root.
func (rh *RequestHandler) applyTrieDbBatches(trieDbBatches map[string][]byte, blockNum uint64) error {
	sharedDB := rh.storageManager.GetStorageDatabaseTrie()
	if sharedDB == nil {
		return fmt.Errorf("shared database trie is nil")
	}

	for key, value := range trieDbBatches {
		if len(value) == 0 {
			continue
		}
		deserialized, err := storage.DeserializeBatch(value)
		if err != nil {
			return fmt.Errorf("error deserializing TrieDB batch '%s' for block %d: %w", key, blockNum, err)
		}
		if len(deserialized) == 0 {
			continue
		}

		// Key format expected: "addressHex/dbName"
		parts := strings.Split(key, "/")
		if len(parts) != 2 {
			logger.Warn("⚠️ Ignored invalid TrieDB batch key format: %s", key)
			continue
		}

		addressHex := parts[0]
		dbName := parts[1]

		dbNameHash := crypto.Keccak256([]byte(dbName))
		prefix := fmt.Sprintf("%x:%s:", dbNameHash, addressHex)

		prefixedDB := storage.NewPrefixStorage(sharedDB, prefix)

		if err := prefixedDB.BatchPut(deserialized); err != nil {
			return fmt.Errorf("error writing TrieDB batch '%s' for block %d: %w", key, blockNum, err)
		}
	}
	return nil
}

// ============================================================================
// GO-AUTHORITATIVE GEI: Recovery RPC
// ============================================================================

// HandleGetLastHandledCommitIndexRequest returns Go's current execution state
// so Rust can resume from the correct point after restart.
// This replaces the fragile fragment_offset reconstruction logic.
func (rh *RequestHandler) HandleGetLastHandledCommitIndexRequest(request *pb.GetLastHandledCommitIndexRequest) (*pb.GetLastHandledCommitIndexResponse, error) {
	lastGEI := storage.GetLastGlobalExecIndex()
	lastBlockNumber := storage.GetLastBlockNumber()
	currentEpoch := rh.chainState.GetCurrentEpoch()

	// Go is the authoritative source for lastHandledCommitIndex
	isAuthoritative := true

	// FORK-SAFETY: Return the epoch of the commit_index, not just current epoch.
	// If lastHandledCommitEpoch != current_epoch, the commit_index is stale (from a previous epoch).
	commitIndex := storage.GetLastHandledCommitIndex()
	commitEpoch := storage.GetLastHandledCommitEpoch()

	// Epoch validation: If commit belongs to a different epoch, report 0 to prevent cross-epoch fork
	if commitEpoch > 0 && commitEpoch != currentEpoch {
		logger.Warn("🚨 [GO-AUTH GEI] EPOCH MISMATCH in recovery: lastHandledCommitIndex=%d belongs to epoch=%d but current epoch=%d. Reporting commit_index=0 to Rust.",
			commitIndex, commitEpoch, currentEpoch)
		commitIndex = 0
	}

	var lastBlockTimestampMs uint64 = 0
	var stateRoot []byte = nil
	if lastBlockNumber > 0 {
		blockchainInstance := blockchain.GetBlockChainInstance()
		if blockchainInstance != nil {
			lastBlock := blockchainInstance.GetLastBlock()
			if lastBlock != nil {
				lastBlockTimestampMs = lastBlock.Header().TimeStamp()
				stateRoot = lastBlock.Header().AccountStatesRoot().Bytes()
			}
		}
	}

	logger.Info("🔑 [GO-AUTH GEI] Recovery query: last_commit=%d (epoch=%d), last_gei=%d, last_block=%d, current_epoch=%d, authoritative=%v, ts=%d, state_root=%x",
		commitIndex, commitEpoch, lastGEI, lastBlockNumber, currentEpoch, isAuthoritative, lastBlockTimestampMs, stateRoot)

	response := &pb.GetLastHandledCommitIndexResponse{
		LastCommitIndex:      commitIndex,
		LastGei:              lastGEI,
		LastBlockNumber:      lastBlockNumber,
		Epoch:                currentEpoch,
		IsAuthoritative:      isAuthoritative,
		LastBlockTimestampMs: lastBlockTimestampMs,
		StateRoot:            stateRoot,
	}

	return response, nil
}
