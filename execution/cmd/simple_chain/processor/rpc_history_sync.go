package processor

import (
	"encoding/binary"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// StartRPCHistorySync starts a background goroutine to fetch missing historical data
// for nodes that require full historical transactions and receipts (RPC).
func (bp *BlockProcessor) StartRPCHistorySync() {
	if bp.config == nil {
		return
	}
	// Kích hoạt đồng bộ PebbleDB lịch sử nếu là node RPC
	if !bp.config.IsRPCNode {
		return
	}

	go func() {
		logger.Info("🔍 [HISTORY-SYNC] Starting background history sync for Explorer/RPC node...")

		// Wait for node to catch up initially (Wait until StartupCatchUp is mostly done)
		for {
			if bp.IsSyncCompleted() {
				break
			}
			time.Sleep(5 * time.Second)
		}

		logger.Info("🔍 [HISTORY-SYNC] Node synced. Scanning backwards for missing history...")

		// Keep checking periodically because new blocks might be synced via hybrid consensus
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		var lastCheckedBlock uint64 = 0

		syncKey := []byte("rpc_sync_last_checked_block")
		dbReceipt := bp.storageManager.GetStorageReceipt()
		if val, err := dbReceipt.Get(syncKey); err == nil && len(val) == 8 {
			lastCheckedBlock = binary.BigEndian.Uint64(val)
			logger.Info("🔍 [HISTORY-SYNC] Resuming history scan from block %d (loaded from storage)", lastCheckedBlock)
		}

		for {
			currentTip := bp.GetLastBlock().Header().BlockNumber()

			// Only scan if there's new blocks or we just started
			if currentTip > lastCheckedBlock {
				start := lastCheckedBlock + 1
				if lastCheckedBlock == 0 {
					start = 1 // Start from block 1 on first run
				}

				// We scan from start to currentTip
				for blockNum := start; blockNum <= currentTip; blockNum++ {
					// 1. Get the block
					blockData := blockchain.GetBlockChainInstance().GetBlockByNumber(blockNum)
					if blockData == nil {
						// Block not found locally, might need to wait or it's genuinely missing
						logger.Warn("⚠️ [HISTORY-SYNC] Block %d not found in local DB. Pausing scan to wait for sync to catch up...", blockNum)
						break
					}

					// 2. Check if block has transactions
					txs := blockData.Transactions()
					if len(txs) == 0 {
						lastCheckedBlock = blockNum
						if blockNum%1000 == 0 { // Lên lịch lưu sau mỗi 1000 block để giảm disk I/O
							buf := make([]byte, 8)
							binary.BigEndian.PutUint64(buf, blockNum)
							dbReceipt.Put(syncKey, buf)
						}
						continue // No txs, no receipts to sync
					}

					// 3. Check if we have the receipts for this block
					receiptRoot := blockData.Header().ReceiptRoot()
					rcpDb, err := receipt.NewReceiptsFromRoot(receiptRoot, dbReceipt)

					var missing bool
					if err != nil {
						missing = true
					} else {
						// Try to fetch the first receipt to ensure data actually exists in DB
						_, err := rcpDb.GetReceipt(txs[0])
						if err != nil {
							missing = true
						}
					}

					// 4. If missing, fetch the BackupDb from Master and insert data
					if missing {
						logger.Info("🔍 [HISTORY-SYNC] Detected missing receipt data at block %d (hash=%s). Triggering fetch from Master...", blockNum, blockData.Header().Hash().Hex())
						success := bp.fetchAndInjectHistory(blockNum)
						if !success {
							// If failed, we might want to retry later, so we don't advance lastCheckedBlock past this point
							break
						}
					}

					// Successfully checked (and potentially fixed) this block
					lastCheckedBlock = blockNum
					
					// Save to DB
					buf := make([]byte, 8)
					binary.BigEndian.PutUint64(buf, blockNum)
					dbReceipt.Put(syncKey, buf)

					// Small sleep to yield CPU and prevent locking up the node during heavy sync
					time.Sleep(10 * time.Millisecond)
				}
			}

			if lastCheckedBlock > 0 {
				buf := make([]byte, 8)
				binary.BigEndian.PutUint64(buf, lastCheckedBlock)
				dbReceipt.Put(syncKey, buf)
			}

			<-ticker.C
		}
	}()
}

// fetchAndInjectHistory fetches a block's BackupDb from peers and injects missing history
func (bp *BlockProcessor) fetchAndInjectHistory(blockNum uint64) bool {
	// Request block batch from master
	fetchedBlocks, missingCount := bp.node.GetBlockStorageBatch(blockNum, blockNum)

	if missingCount > 0 || fetchedBlocks[blockNum] == nil {
		logger.Warn("⚠️ [HISTORY-SYNC] Could not fetch block %d history from peers", blockNum)
		return false
	}

	data := fetchedBlocks[blockNum]
	backupDb, err := storage.DeserializeBackupDb(data)
	if err != nil {
		logger.Error("❌ [HISTORY-SYNC] Failed to deserialize BackupDb for block %d: %v", blockNum, err)
		return false
	}

	// Inject Transactions
	if len(backupDb.TxBatchPut) > 0 {
		txBatch, err := storage.DeserializeBatch(backupDb.TxBatchPut)
		if err == nil && len(txBatch) > 0 {
			err = bp.storageManager.GetStorageTransaction().BatchPut(txBatch)
			if err != nil {
				logger.Error("❌ [HISTORY-SYNC] Failed to insert TxBatchPut for block %d: %v", blockNum, err)
			} else {
				logger.Debug("✅ [HISTORY-SYNC] Injected %d transaction entries for block %d", len(txBatch), blockNum)
			}
		}
	}

	// Inject Receipts
	if len(backupDb.ReceiptBatchPut) > 0 {
		rcpBatch, err := storage.DeserializeBatch(backupDb.ReceiptBatchPut)
		if err == nil && len(rcpBatch) > 0 {
			err = bp.storageManager.GetStorageReceipt().BatchPut(rcpBatch)
			if err != nil {
				logger.Error("❌ [HISTORY-SYNC] Failed to insert ReceiptBatchPut for block %d: %v", blockNum, err)
			} else {
				logger.Info("🔍 [HISTORY-SYNC] Extracted BackupDb size %d bytes for block %d", len(backupDb.ReceiptBatchPut), blockNum)
			}
		}
	}

	logger.Info("✅ [HISTORY-SYNC] Successfully restored history for block %d", blockNum)
	return true
}
