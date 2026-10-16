// @title processor/block_processor_indexing.go
// @markdown processor/block_processor_indexing.go - Block indexing and search functionality
package processor

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
)

type pendingIndexBlock struct {
	header   types.BlockHeader
	txs      []types.Transaction
	receipts []types.Receipt
}

const (
	explorerCommitBatchBlocks = 64
	maxPendingIndexBlocks     = 32
)

func (bp *BlockProcessor) popPendingIndexBlock(blockNum uint64) *pendingIndexBlock {
	raw, loaded := bp.pendingIndexBlocks.LoadAndDelete(blockNum)
	if !loaded {
		return nil
	}
	bp.pendingIndexBlockCount.Add(-1)
	if pending, ok := raw.(*pendingIndexBlock); ok {
		return pending
	}
	return nil
}

// startIndexingProcess starts the indexing process
func (bp *BlockProcessor) startIndexingProcess() {
	logger.Info("✅ Indexing process initiated")
	bp.isSyncCompleted.Store(false)

	go func() {
		logger.Info("Starting scan for missing blocks to add to queue...")
		time.Sleep(5 * time.Second)

		missingRanges := bp.storageManager.GetExplorerSearchService().GetMissingBlockRanges()
		initialMissingCount := 0
		skippedCount := 0
		for _, gap := range missingRanges {
			for blockNum := gap.Start; blockNum <= gap.End; blockNum++ {
				// CRITICAL: Non-blocking send to prevent blocking if channel is full
				select {
				case bp.indexingChannel <- blockNum:
					initialMissingCount++
				default:
					// Channel is full, skip this block to avoid blocking
					skippedCount++
					if skippedCount <= 10 {
						logger.Warn("⚠️  [INDEXING] indexingChannel is full, skipping block #%d during initial scan", blockNum)
					}
				}
			}
		}
		if initialMissingCount > 0 {
			logger.Info("Added %d missing blocks to queue (skipped %d blocks due to full channel)", initialMissingCount, skippedCount)
		} else {
			logger.Info("No missing blocks found during startup")
			bp.isSyncCompleted.Store(true)
		}
		if skippedCount > 0 {
			logger.Warn("⚠️  [INDEXING] Total %d blocks were skipped during initial scan due to full indexingChannel", skippedCount)
		}
	}()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	go func() {
		nodeCtx := context.Background()
		for {
			select {
			case <-ticker.C:
				// Change to Debug level to reduce spam
				logger.Debug("Periodic scan: Searching for missing blocks...")
				missingRanges := bp.storageManager.GetExplorerSearchService().GetMissingBlockRanges()
				periodicMissingCount := 0
				for _, gap := range missingRanges {
					for blockNum := gap.Start; blockNum <= gap.End; blockNum++ {
						// SUPPLEMENT: Check and regulate producer speed
						if len(bp.indexingChannel) > 480000 { // When channel is 96% full
							logger.Warn("Indexing queue almost full, pausing new block pushes for 2 seconds...")
							time.Sleep(2 * time.Second)
						}
						if _, loaded := bp.indexingLocks.Load(blockNum); !loaded {
							// CRITICAL: Non-blocking send to prevent blocking if channel is full
							select {
							case bp.indexingChannel <- blockNum:
								periodicMissingCount++
							default:
								// Channel is full, skip this block to avoid blocking
								logger.Warn("⚠️  [INDEXING] indexingChannel is full, skipping block #%d during periodic scan", blockNum)
							}
						}
					}
				}
				// Only log when missing blocks are found
				if periodicMissingCount > 0 {
					logger.Info("Periodic scan: Found and added %d missing blocks to queue", periodicMissingCount)
				}
			case <-nodeCtx.Done():
				return
			}
		}
	}()

	tickerSync := time.NewTicker(2 * time.Second)
	defer tickerSync.Stop()

	for {
		select {
		case blockNum := <-bp.indexingChannel:
			bp.isSyncCompleted.Store(false)
			bp.indexSingleBlock(blockNum)

		case <-tickerSync.C:
			if len(bp.indexingChannel) == 0 {
				explorerSvc := bp.storageManager.GetExplorerSearchService()
				if explorerSvc != nil && explorerSvc.QueueDepth() > 0 {
					continue
				}
				if bp.uncommittedIndexBlocks > 0 {
					if explorerSvc != nil {
						if err := explorerSvc.Commit(); err != nil {
							logger.Error("❌ [INDEXING] Failed to commit explorer search index on idle tick: %v", err)
						}
					}
					bp.uncommittedIndexBlocks = 0
				}
				if !bp.isSyncCompleted.Load() {
					logger.Info("Indexing queue empty, marking sync completed")
					bp.isSyncCompleted.Store(true)
				}
			}
		}
	}
}

func (bp *BlockProcessor) finalizeIndexedBlock(blockNumber uint64, indexDroppedCount int) {
	explorerSvc := bp.storageManager.GetExplorerSearchService()
	if explorerSvc == nil {
		return
	}
	bp.uncommittedIndexBlocks++
	// Coalesce 32-shard Xapian commits every 64 blocks during bursts; when indexingChannel
	// becomes idle, tickerSync (every 2s in startIndexingProcess) flushes any remaining blocks.
	if bp.uncommittedIndexBlocks >= explorerCommitBatchBlocks {
		if err := explorerSvc.Commit(); err != nil {
			logger.Error("❌ [INDEXING] Failed to commit explorer search index for block #%d: %v", blockNumber, err)
		}
		bp.uncommittedIndexBlocks = 0
	}
	if indexDroppedCount == 0 {
		if err := explorerSvc.AddBlockToIndexRanges(blockNumber); err != nil {
			logger.Error("Error updating block ranges after indexing block #%d: %v", blockNumber, err)
		}
	} else {
		logger.Warn("⚠️ [EXPLORER] Block #%d had %d index drops — NOT marking as indexed. Periodic re-indexer will retry.",
			blockNumber, indexDroppedCount)
	}
}

// indexSingleBlock indexes a single block
func (bp *BlockProcessor) indexSingleBlock(blockNum uint64) {
	if _, loaded := bp.indexingLocks.LoadOrStore(blockNum, true); loaded {
		bp.popPendingIndexBlock(blockNum)
		return
	}
	defer bp.indexingLocks.Delete(blockNum)

	explorerSvc := bp.storageManager.GetExplorerSearchService()
	if explorerSvc == nil {
		bp.popPendingIndexBlock(blockNum)
		return
	}

	// Fast path: index directly from in-memory block data handed off by commitWorker,
	// avoiding 2x trie re-opens and 16,000 protobuf unmarshals per block.
	if pending := bp.popPendingIndexBlock(blockNum); pending != nil && pending.header != nil {
		blockHashHex := pending.header.Hash().Hex()
		indexDroppedCount := 0
		var rcptByHash map[common.Hash]types.Receipt
		for i, tx := range pending.txs {
			if tx == nil {
				continue
			}
			var rpc types.Receipt
			if i < len(pending.receipts) && pending.receipts[i] != nil && pending.receipts[i].TransactionHash() == tx.Hash() {
				rpc = pending.receipts[i]
			} else {
				if rcptByHash == nil {
					rcptByHash = make(map[common.Hash]types.Receipt, len(pending.receipts))
					for _, r := range pending.receipts {
						if r != nil {
							rcptByHash[r.TransactionHash()] = r
						}
					}
				}
				rpc = rcptByHash[tx.Hash()]
			}
			if rpc == nil {
				continue
			}
			bp.ProcessedIndexTxCount.Add(1)
			if err := explorerSvc.IndexTransactionFast(tx, rpc, pending.header, blockHashHex, false); err != nil {
				indexDroppedCount++
				continue
			}
		}
		bp.finalizeIndexedBlock(pending.header.BlockNumber(), indexDroppedCount)
		return
	}

	var block types.Block
	for {
		block = blockchain.GetBlockChainInstance().GetBlockByNumber(blockNum)
		if block != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(block.Header().TransactionsRoot(), bp.storageManager.GetStorageTransaction())
	if err != nil {
		logger.Error("❌ [INDEXING] Cannot create TransactionStateDB for block #%d (skipping): %v", blockNum, err)
		return
	}
	rcpDb, err := receipt.NewReceiptsFromRoot(block.Header().ReceiptRoot(), bp.storageManager.GetStorageReceipt())
	if err != nil {
		logger.Error("❌ [INDEXING] Cannot create ReceiptsDB for block #%d (skipping): %v", blockNum, err)
		return
	}

	blockHashHex := block.Header().Hash().Hex()
	indexDroppedCount := 0
	for _, txHash := range block.Transactions() {
		tx, err := txDB.GetTransaction(txHash)
		if err != nil {
			logger.Error("❌ [INDEXING] Cannot get transaction %s from DB for block #%d (skipping): %v", txHash.Hex(), blockNum, err)
			continue
		}
		rpc, err := rcpDb.GetReceipt(txHash)
		if err != nil {
			logger.Error("❌ [INDEXING] Cannot get receipt for tx %s from DB for block #%d (skipping): %v", txHash.Hex(), blockNum, err)
			continue
		}

		bp.ProcessedIndexTxCount.Add(1)
		if err := explorerSvc.IndexTransactionFast(tx, rpc, block.Header(), blockHashHex, true); err != nil {
			indexDroppedCount++
			continue
		}
	}

	bp.finalizeIndexedBlock(block.Header().BlockNumber(), indexDroppedCount)
}
