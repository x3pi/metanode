package node

import (
	"errors"
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/syndtr/goleveldb/leveldb"
)

// --- Hằng số tiền tố cho key block ---
const blockDataKeyPrefix = common.BlockDataTopic


// createBlockDataKey generates the standardized key for block data.
func createBlockDataKey(blockNumber uint64) string {
	return fmt.Sprintf("%s-%d", blockDataKeyPrefix, blockNumber)
}

// GetBlockStorage kiểm tra block cục bộ trong memory hoặc backup storage.
// Nếu không tìm thấy, hàm sẽ trả về lỗi. Quá trình fetch block P2P
// đã được chuyển giao toàn quyền cho Rust xử lý.
func (node *HostNode) GetBlockStorage(blockNumber uint64) ([]byte, error) {
	key := createBlockDataKey(blockNumber)

	// 1. Check In-Memory Store
	value, memOk := node.KeyValueStore.Get(key)
	if memOk {
		return value, nil
	}

	// 2. Check Backup Storage
	backupStorageInstance, backupExists := node.GetTopicStorage(BackupStorageKey)
	if backupExists {
		type storageGetter interface {
			Get(key []byte) ([]byte, error)
		}
		getter, ok := backupStorageInstance.(storageGetter)
		if !ok {
			logger.Error(fmt.Sprintf("Internal error: backup storage instance ('%s') does not implement Get method", BackupStorageKey))
		} else {
			data, err := getter.Get([]byte(key))
			if err == nil {
				node.KeyValueStore.Add(key, data)
				return data, nil
			}
		}
	}

	return nil, fmt.Errorf("block %d not found locally", blockNumber)
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


