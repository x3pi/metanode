// @title processor/block_processor_indexing.go
// @markdown processor/block_processor_indexing.go - Block indexing and search functionality
package processor

import (
	"context"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
)

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

	for {
		select {
		case blockNum := <-bp.indexingChannel:
			bp.isSyncCompleted.Store(false)
			bp.indexSingleBlock(blockNum)

		case <-time.After(2 * time.Second):
			if len(bp.indexingChannel) == 0 {
				if !bp.isSyncCompleted.Load() {
					logger.Info("Indexing queue empty, marking sync completed")
					bp.isSyncCompleted.Store(true)
				}
			}
		}
	}
}

// indexSingleBlock indexes a single block
func (bp *BlockProcessor) indexSingleBlock(blockNum uint64) {
	if _, loaded := bp.indexingLocks.LoadOrStore(blockNum, true); loaded {
		return
	}
	defer bp.indexingLocks.Delete(blockNum)

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
		if err := bp.storageManager.GetExplorerSearchService().IndexTransaction(tx, rpc, block.Header()); err != nil {
			logger.Error("❌ [INDEXING] Cannot index transaction %s in block #%d (skipping): %v", txHash.Hex(), blockNum, err)
			continue
		}
	}

	bp.storageManager.GetExplorerSearchService().Commit()
	err = bp.storageManager.GetExplorerSearchService().AddBlockToIndexRanges(block.Header().BlockNumber())
	if err != nil {
		logger.Error("Error updating block ranges after indexing block #%d: %v", block.Header().BlockNumber(), err)
	}
}
