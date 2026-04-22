package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/syndtr/goleveldb/leveldb"
)

// --- Hằng số tiền tố cho key block ---
const blockDataKeyPrefix = common.BlockDataTopic

// Lỗi mới để báo hiệu việc tìm kiếm block đang diễn ra ở nền
var ErrBlockFetchInProgress = errors.New("block fetch from peers is in progress")

// createBlockDataKey generates the standardized key for block data.
func createBlockDataKey(blockNumber uint64) string {
	return fmt.Sprintf("%s-%d", blockDataKeyPrefix, blockNumber)
}

// GetBlockStorage kiểm tra block cục bộ (memory, backup).
// Nếu không tìm thấy, nó sẽ khởi chạy một goroutine nền (nếu chưa chạy)
// để yêu cầu block từ các peer qua TCP và trả về lỗi ErrBlockFetchInProgress.
func (node *HostNode) GetBlockStorage(blockNumber uint64) ([]byte, error) {
	key := createBlockDataKey(blockNumber)

	// 1. Check In-Memory Store (KeyValueStore - LRU cache)
	value, memOk := node.KeyValueStore.Get(key)
	if memOk {
		return value, nil
	}

	// 2. Check Backup Storage (nếu được cấu hình)
	backupStorageInstance, backupExists := node.GetTopicStorage(BackupStorageKey)
	if backupExists {
		type storageGetter interface {
			Get(key []byte) ([]byte, error)
		}
		getter, ok := backupStorageInstance.(storageGetter)
		if !ok {
			logger.Error(fmt.Sprintf("Internal error: backup storage instance ('%s') does not implement Get method for Block %d", BackupStorageKey, blockNumber))
		} else {
			data, err := getter.Get([]byte(key))
			if err == nil {
				logger.Debug(fmt.Sprintf("Retrieved Block %d (key: '%s') from backup storage.", blockNumber, key))
				node.KeyValueStore.Add(key, data)
				return data, nil
			}
			if !errors.Is(err, leveldb.ErrNotFound) && err.Error() != "pebble: not found" {
				logger.Error(fmt.Sprintf("Backup storage error retrieving Block %d (key: '%s'): %v", blockNumber, key, err))
			}
		}
	}

	// 3. Không tìm thấy cục bộ, kiểm tra và khởi chạy tìm kiếm nền
	logger.Debug(fmt.Sprintf("Block %d (key: '%s') not found locally. Checking/Initiating peer fetch.", blockNumber, key))

	_, loaded := node.fetchingBlocks.LoadOrStore(blockNumber, true)
	if loaded {
		logger.Debug(fmt.Sprintf("Block %d fetch already in progress.", blockNumber))
		return nil, ErrBlockFetchInProgress
	}

	go node.fetchBlockFromPeersAsync(blockNumber)
	return nil, ErrBlockFetchInProgress
}

// FetchBlockFromMaster is a public wrapper for fetchBlockFromPeersAsync.
// Used by Sub-node StartupCatchUp to fetch a specific block from Master for hash validation.
func (node *HostNode) FetchBlockFromMaster(blockNumber uint64) {
	node.fetchBlockFromPeersAsync(blockNumber)
}

// fetchBlockFromPeersAsync chạy ở chế độ nền để yêu cầu block từ Master qua TCP.
// Gửi request "BlockRequest" qua MessageSender tới master connections.
func (node *HostNode) fetchBlockFromPeersAsync(blockNumber uint64) {
	defer node.fetchingBlocks.Delete(blockNumber)

	ctx := node.ctx

	if node.ConnectionsManager == nil || node.MessageSender == nil {
		logger.Warn(fmt.Sprintf("Network components not initialized for Block %d fetch.", blockNumber))
		return
	}

	// Tìm master connections
	masterConns := node.ConnectionsManager.ConnectionsByType(
		common.MapConnectionTypeToIndex(common.MASTER_CONNECTION_TYPE))

	if len(masterConns) == 0 {
		logger.Warn(fmt.Sprintf("No connected master peers found to fetch Block %d.", blockNumber))
		return
	}

	// Thử từng master connection
	for _, conn := range masterConns {
		if ctx.Err() != nil {
			return
		}
		if conn == nil || !conn.IsConnect() {
			continue
		}

		// Gửi block request qua TCP
		blockNumBytes := []byte(fmt.Sprintf("%d", blockNumber))
		err := node.MessageSender.SendBytes(conn, "BlockRequest", blockNumBytes)
		if err != nil {
			logger.Debug(fmt.Sprintf("Failed to send BlockRequest for Block %d: %v", blockNumber, err))
			continue
		}

		// NOTE: Response sẽ đến qua route "BlockRequest" handler và được push vào cache
		// Caller sẽ retry GetBlockStorage sau đó
		logger.Debug(fmt.Sprintf("Sent BlockRequest for Block %d to master.", blockNumber))
		return
	}

	logger.Warn(fmt.Sprintf("Failed to request Block %d from any master peer.", blockNumber))
}

// GetBlockStorageLocal chỉ tìm kiếm block trong bộ nhớ và backup storage cục bộ.
func (node *HostNode) GetBlockStorageLocal(blockNumber uint64) ([]byte, error) {
	key := createBlockDataKey(blockNumber)

	// Check In-Memory Store
	value, memOk := node.KeyValueStore.Get(key)
	if memOk {
		return value, nil
	}

	// Check Backup Storage
	backupStorageInstance, backupExists := node.GetTopicStorage(BackupStorageKey)
	if backupExists {
		type storageGetter interface {
			Get(key []byte) ([]byte, error)
		}
		getter, ok := backupStorageInstance.(storageGetter)
		if !ok {
			logger.Error(fmt.Sprintf("Internal error: backup storage instance unusable (missing Get method) for Block %d", blockNumber))
		} else {
			data, err := getter.Get([]byte(key))
			if err == nil {
				node.KeyValueStore.Add(key, data)
				return data, nil
			}
			if !errors.Is(err, leveldb.ErrNotFound) && err.Error() != "pebble: not found" {
				logger.Error(fmt.Sprintf("Backup storage error retrieving Block %d (key: '%s'): %v", blockNumber, key, err))
			}
		}
	}

	return nil, fmt.Errorf("block %d not found locally (memory/backup)", blockNumber)
}

// SetStorage stores a key-value pair in the in-memory KeyValueStore.
func (node *HostNode) SetStorage(key string, value []byte) {
	node.KeyValueStore.Add(key, value)
}

// DeleteStorage removes a key from the in-memory KeyValueStore.
func (node *HostNode) DeleteStorage(key string) {
	node.KeyValueStore.Remove(key)
}

// GetBlockStorageBatch fetches multiple blocks efficiently.
// First checks local memory/backup, then requests missing blocks from Master via TCP.
func (node *HostNode) GetBlockStorageBatch(startBlock, endBlock uint64) (map[uint64][]byte, int) {
	result := make(map[uint64][]byte)
	var missingBlocks []uint64

	// 1. Check local storage first for each block
	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		data, err := node.GetBlockStorageLocal(blockNum)
		if err == nil && len(data) > 0 {
			result[blockNum] = data
		} else {
			missingBlocks = append(missingBlocks, blockNum)
		}
	}

	if len(missingBlocks) == 0 {
		return result, 0
	}

	if node.ConnectionsManager == nil || node.MessageSender == nil {
		logger.Warn(fmt.Sprintf("Network components not initialized for batch fetch of blocks %d-%d", startBlock, endBlock))
		return result, len(missingBlocks)
	}

	// 2. Tìm master connections
	masterConns := node.ConnectionsManager.ConnectionsByType(
		common.MapConnectionTypeToIndex(common.MASTER_CONNECTION_TYPE))

	if len(masterConns) == 0 {
		logger.Warn(fmt.Sprintf("No connected master peers for batch fetch of blocks %d-%d", startBlock, endBlock))
		return result, len(missingBlocks)
	}

	// 3. Gửi batch block request cho từng block thiếu
	ctx := node.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	for _, conn := range masterConns {
		if conn == nil || !conn.IsConnect() {
			continue
		}

		for _, blockNum := range missingBlocks {
			if _, exists := result[blockNum]; exists {
				continue // Already found
			}
			if ctx.Err() != nil {
				break
			}

			blockNumBytes := []byte(fmt.Sprintf("%d", blockNum))
			err := node.MessageSender.SendBytes(conn, "BlockRequest", blockNumBytes)
			if err != nil {
				logger.Debug(fmt.Sprintf("Failed to send BlockRequest for Block %d in batch: %v", blockNum, err))
			}
		}
		break // Only try the first master connection
	}

	stillMissing := len(missingBlocks) // Requests sent but responses arrive async
	logger.Info(fmt.Sprintf("📤 [BATCH-FETCH] Sent requests for %d missing blocks in range %d-%d",
		stillMissing, startBlock, endBlock))

	return result, stillMissing
}
