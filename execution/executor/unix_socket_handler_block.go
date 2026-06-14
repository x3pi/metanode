package executor

import (
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
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
