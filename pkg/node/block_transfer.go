package node

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// HandleBlockRequest xử lý yêu cầu block đơn lẻ qua TCP route "BlockRequest".
// Request body: block number as string (e.g., "12345")
// Response: block data bytes gửi lại qua MessageSender
func (node *HostNode) HandleBlockRequest(request network.Request) error {
	bodyStr := strings.TrimSpace(string(request.Message().Body()))
	blockNumber, err := strconv.ParseUint(bodyStr, 10, 64)
	if err != nil {
		logger.Error("❌ [BLOCK REQUEST] Invalid block number received: %v", err)
		return err
	}

	conn := request.Connection()
	if conn == nil || !conn.IsConnect() {
		logger.Error("❌ [BLOCK REQUEST] Connection not available for block #%d response", blockNumber)
		return fmt.Errorf("connection not available for BlockRequest response")
	}

	logger.Info("📡 [BLOCK REQUEST] Master received request for block #%d from %s", blockNumber, conn.Address().Hex()[:8]+"...")

	// 1. Try local storage first (memory + topic backup)
	blockData, err := node.GetBlockStorageLocal(blockNumber)
	if err != nil || len(blockData) == 0 {
		// 2. Fallback: try direct backup storage read (handles case where
		//    HandleSyncBlocksRequest wrote to PebbleDB but GetBlockStorageLocal
		//    can't find it due to LazyPebbleDB cache timing)
		backupStore := node.GetBackupStorageDirect()
		if backupStore != nil {
			key := fmt.Sprintf("block_data_topic-%d", blockNumber)
			if directData, directErr := backupStore.Get([]byte(key)); directErr == nil && len(directData) > 0 {
				logger.Info("📡 [BLOCK REQUEST] Found block #%d via direct backup storage fallback (size=%d bytes)", blockNumber, len(directData))
				blockData = directData
				// Cache it for future requests
				node.KeyValueStore.Add(createBlockDataKey(blockNumber), directData)
			}
		}
	}

	if len(blockData) == 0 {
		logger.Warn("⚠️ [BLOCK REQUEST] Block #%d not found locally (err: %v)", blockNumber, err)
		return nil // Block not found — don't error, just skip
	}

	logger.Info("📤 [BLOCK REQUEST] Found block #%d locally (size=%d bytes), sending BlockResponse...", blockNumber, len(blockData))
	sendErr := node.MessageSender.SendBytes(conn, "BlockResponse", blockData)
	if sendErr != nil {
		logger.Error("❌ [BLOCK REQUEST] Failed to send BlockResponse for block #%d: %v", blockNumber, sendErr)
	} else {
		logger.Info("✅ [BLOCK REQUEST] Sent BlockResponse for block #%d successfully", blockNumber)
	}
	return sendErr
}

// HandleBlockResponse xử lý phản hồi block data gửi từ master.
// CRITICAL FIX: This was previously a no-op that dropped all received blocks.
// Now it deserializes the backup data, caches it in KeyValueStore, and pushes
// it to BlockProcessingQueue for the TxsProcessor to process.
func (node *HostNode) HandleBlockResponse(request network.Request) error {
	blockData := request.Message().Body()
	if len(blockData) == 0 {
		return fmt.Errorf("empty block data in BlockResponse")
	}

	// Deserialize to extract block number for cache key
	backupDb, err := storage.DeserializeBackupDb(blockData)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to deserialize BlockResponse: %v", err))
		return nil // Don't error the handler, just log
	}

	blockNumber := backupDb.BockNumber
	key := createBlockDataKey(blockNumber)

	// Cache in KeyValueStore so GetBlockStorage finds it
	node.KeyValueStore.Add(key, blockData)
	logger.Info(fmt.Sprintf("✅ [BlockResponse] Received and cached block #%d (size=%d bytes)", blockNumber, len(blockData)))

	// Push to BlockProcessingQueue for immediate processing by TxsProcessor
	select {
	case node.BlockProcessingQueue <- &backupDb:
		logger.Info(fmt.Sprintf("✅ [BlockResponse] Pushed block #%d to BlockProcessingQueue", blockNumber))
	default:
		logger.Warn(fmt.Sprintf("⚠️ [BlockResponse] BlockProcessingQueue full, block #%d cached but not queued", blockNumber))
	}

	return nil
}

// Request body: "startBlock-endBlock" (e.g., "100-200")
// Response: gửi từng block data về cho requester
func (node *HostNode) HandleBlockRangeRequest(request network.Request) error {
	bodyStr := strings.TrimSpace(string(request.Message().Body()))
	parts := strings.Split(bodyStr, "-")
	if len(parts) != 2 {
		return fmt.Errorf("invalid range format, expected start-end, got: %s", bodyStr)
	}

	startBlock, err1 := strconv.ParseUint(parts[0], 10, 64)
	endBlock, err2 := strconv.ParseUint(parts[1], 10, 64)
	if err1 != nil || err2 != nil || endBlock < startBlock {
		return fmt.Errorf("invalid block numbers in range request: %s", bodyStr)
	}

	// Cap range to prevent abuse
	if endBlock-startBlock > 200 {
		endBlock = startBlock + 200
	}

	conn := request.Connection()
	if conn == nil || !conn.IsConnect() {
		return fmt.Errorf("connection not available for BlockRangeRequest response")
	}

	if node.MessageSender == nil {
		return fmt.Errorf("MessageSender not initialized")
	}

	// Gửi từng block
	sentCount := 0
	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		blockData, err := node.GetBlockStorageLocal(blockNum)
		if err != nil || len(blockData) == 0 {
			continue // Skip missing blocks
		}

		if err := node.MessageSender.SendBytes(conn, "BlockResponse", blockData); err != nil {
			logger.Debug(fmt.Sprintf("Failed to send block %d in range response: %v", blockNum, err))
			break
		}
		sentCount++
	}

	if sentCount > 0 {
		logger.Debug(fmt.Sprintf("Served %d blocks for range %d-%d via TCP", sentCount, startBlock, endBlock))
	}

	return nil
}
