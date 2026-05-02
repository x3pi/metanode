// @title processor/block_processor_db_sync.go
// @markdown processor/block_processor_db_sync.go - Periodic DB sync for bp.lastBlock
package processor

import (
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// syncLastBlockFromDB periodically checks if storage.GetLastBlockNumber() is ahead
// of bp.lastBlock and refreshes bp.lastBlock from DB.
//
// WHY THIS IS NEEDED:
// HandleSyncBlocksRequest (called by Rust sync_node via FFI) writes blocks to LevelDB
// and updates storage.GetLastBlockNumber(), but it runs in the UDS handler goroutine
// and has NO access to bp.lastBlock. Meanwhile, the processRustEpochData channel only
// receives ExecutableBlock data from consensus — it never processes synced blocks.
// Without this goroutine, RPC eth_blockNumber returns a stale value because it reads
// from bp.lastBlock.Header().BlockNumber().
//
// The fast-path LAZY REFRESH in processSingleEpochData handles this during active
// consensus, but when node is SyncOnly and only receiving empty commits, the fast-path
// may not trigger frequently enough or may be blocked by stateRoot checks.
func (bp *BlockProcessor) syncLastBlockFromDB() {
	// FORK-SAFETY FIX (2026-04-29): Master nodes MUST NOT adopt P2P-synced blocks
	// from LevelDB. Their bp.lastBlock is updated by the consensus execution pipeline
	// (processSingleEpochData → createBlockFromResults → SetLastBlock). Importing
	// P2P blocks here causes trie state divergence because P2P blocks are created
	// by different leaders with different state roots.
	// This goroutine is only useful for Sub/SyncOnly nodes.
	if bp.serviceType == p_common.ServiceTypeMaster {
		logger.Debug("🔒 [DB-SYNC-REFRESH] Disabled for Master node — bp.lastBlock managed by consensus pipeline")
		return
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		storageBlockNum := storage.GetLastBlockNumber()
		currentBlock := bp.GetLastBlock()

		currentBlockNum := uint64(0)
		if currentBlock != nil {
			currentBlockNum = currentBlock.Header().BlockNumber()
		}

		if storageBlockNum <= currentBlockNum {
			continue // Already up to date
		}

		// Storage is ahead of bp.lastBlock — refresh from DB
		bc := blockchain.GetBlockChainInstance()
		if bc == nil {
			continue
		}

		blockHash, ok := bc.GetBlockHashByNumber(storageBlockNum)
		if !ok {
			continue
		}

		freshBlock, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
		if err != nil || freshBlock == nil {
			continue
		}

		bp.SetLastBlock(freshBlock)
		bp.nextBlockNumber.Store(storageBlockNum + 1)
		headerCopy := freshBlock.Header()
		bp.chainState.SetcurrentBlockHeader(&headerCopy)

		logger.Info("🔄 [DB-SYNC-REFRESH] Updated bp.lastBlock from DB: block #%d → #%d (RPC will now report correct block number)",
			currentBlockNum, storageBlockNum)
	}
}
