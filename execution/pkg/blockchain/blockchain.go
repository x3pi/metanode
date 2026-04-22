package blockchain

import (
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	mtn_types "github.com/meta-node-blockchain/meta-node/types"
)

const (
	blockNumberPrefix       = "blockNumber_"
	txHashPrefix            = "txHashPrefix"            // Tiền tố cho key
	ethHashMapBlsHashPrefix = "ethHashMapBlsHashPrefix" // Tiền tố cho key

	// Cấu hình TTL (Thời gian sống của cache)
	txCacheTTL      = 2 * time.Minute
	blockCacheTTL   = 10 * time.Minute
	mappingCacheTTL = 30 * time.Minute

	// Cấu hình Worker dọn dẹp
	cleanupInterval = 1 * time.Minute // Quét dọn mỗi 1 phút
)

var (
	blockChainInstance *BlockChain
	once               sync.Once
	storeLimiter       = make(chan struct{}, 2000) // Tăng limit lên 2000 cho high concurrency
)

// BlockChain quản lý bộ nhớ đệm và tương tác DB.
// Sử dụng sync.Map cho concurrent read và Background Worker cho việc dọn dẹp.
type BlockChain struct {
	// Cache Layers (Read-Heavy optimization)
	blockCache             *sync.Map
	receiptsCache          *sync.Map
	txsCache               *sync.Map
	blockNumberToHashCache *sync.Map
	txHashToBlockNumber    *sync.Map
	ethHashMapBlsHash      *sync.Map

	blockDatabase  *block.BlockDatabase
	storageManager *storage.StorageManager

	// Dirty Storage (Write buffer)
	// Sử dụng pointer *sync.Map để có thể swap nhanh khi commit
	dirtyStorage *sync.Map
	dirtyLock    sync.RWMutex // Lock nhẹ để bảo vệ việc tráo đổi con trỏ dirtyStorage

	mappingBatch []byte

	// Worker control
	stopCleanup chan struct{}
	wg          sync.WaitGroup
}

// Structs lưu trong cache kèm thời gian để dọn dẹp
type cachedTx struct {
	raw     []byte
	addedAt time.Time
}

type cachedBlock struct {
	block   mtn_types.Block
	addedAt time.Time
}

type cachedHash struct {
	hash    common.Hash
	addedAt time.Time
}

type cachedUint64 struct {
	value   uint64
	addedAt time.Time
}

func (bc *BlockChain) SetMappingBatch(batch []byte) {
	bc.mappingBatch = batch
}

func (bc *BlockChain) GetMappingBatch() []byte {
	batch := bc.mappingBatch
	bc.mappingBatch = nil
	return batch
}

// InitBlockChain khởi tạo singleton.
func InitBlockChain(size int, blockDatabase *block.BlockDatabase, storageManager *storage.StorageManager) {
	once.Do(func() {
		blockChainInstance = &BlockChain{
			blockCache:             new(sync.Map),
			receiptsCache:          new(sync.Map),
			txsCache:               new(sync.Map),
			blockNumberToHashCache: new(sync.Map),
			txHashToBlockNumber:    new(sync.Map),
			ethHashMapBlsHash:      new(sync.Map),

			dirtyStorage: new(sync.Map), // Khởi tạo pointer

			blockDatabase:  blockDatabase,
			storageManager: storageManager,
			stopCleanup:    make(chan struct{}),
		}

		// Kích hoạt Worker chạy ngầm để dọn cache
		blockChainInstance.StartCleanupWorker()

		log.Println("BlockChain instance initialized with Background Cleanup Worker (High Perf Mode)")
	})
}

// GetBlockChainInstance trả về instance singleton.
// Returns nil if InitBlockChain() has not been called yet.
func GetBlockChainInstance() *BlockChain {
	if blockChainInstance == nil {
		logger.Warn("BlockChain instance has not been initialized. Call InitBlockChain() first.")
	}
	return blockChainInstance
}

// ============================================================================
// BACKGROUND WORKER (KEY PERFORMANCE IMPROVEMENT)
// ============================================================================

func (bc *BlockChain) StartCleanupWorker() {
	bc.wg.Add(1)
	go func() {
		defer bc.wg.Done()
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-bc.stopCleanup:
				logger.Info("Stopping blockchain cleanup worker...")
				return
			case <-ticker.C:
				// Thực hiện dọn dẹp định kỳ
				now := time.Now()
				bc.pruneTxCache(now.Add(-txCacheTTL))
				bc.pruneBlockCache(now.Add(-blockCacheTTL))
				bc.pruneBlockNumberCache(now.Add(-mappingCacheTTL))
				bc.pruneTxHashCache(now.Add(-mappingCacheTTL))
				bc.pruneEthHashCache(now.Add(-mappingCacheTTL))
			}
		}
	}()
}

// Stop dừng worker khi tắt node (Graceful shutdown)
func (bc *BlockChain) Stop() {
	close(bc.stopCleanup)
	bc.wg.Wait()
}

// ============================================================================
// CACHE OPERATIONS (Optimized: O(1) Write, No synchronous pruning)
// ============================================================================

func (bc *BlockChain) AddTxToCache(txHash common.Hash, rawTx []byte) {
	if bc.txsCache == nil {
		return
	}

	// Copy dữ liệu để tránh giữ tham chiếu buffer ngoài (Memory safety)
	snapshot := append([]byte(nil), rawTx...)

	bc.txsCache.Store(txHash, cachedTx{
		raw:     snapshot,
		addedAt: time.Now(),
	})
	// logger.Debug("Stored transaction in txsCache:", txHash.Hex())
	// KHÔNG gọi prune ở đây nữa!
}

func (bc *BlockChain) GetTxFromCache(txHash common.Hash) ([]byte, bool) {
	if bc.txsCache == nil {
		return nil, false
	}

	value, ok := bc.txsCache.Load(txHash)
	if !ok {
		return nil, false
	}

	cached, ok := value.(cachedTx)
	if !ok {
		bc.txsCache.Delete(txHash)
		return nil, false
	}

	// Double check TTL (Lazy expiration) phòng trường hợp Worker chưa kịp quét
	if time.Since(cached.addedAt) > txCacheTTL {
		bc.txsCache.Delete(txHash)
		return nil, false
	}

	return append([]byte(nil), cached.raw...), true
}

// ============================================================================
// PRUNING LOGIC (Called by Worker only)
// ============================================================================

func (bc *BlockChain) pruneTxCache(expireBefore time.Time) {
	bc.txsCache.Range(func(key, value any) bool {
		if cached, ok := value.(cachedTx); ok {
			if cached.addedAt.Before(expireBefore) {
				bc.txsCache.Delete(key)
			}
		} else {
			bc.txsCache.Delete(key) // Xóa dữ liệu rác/sai kiểu
		}
		return true
	})
}

func (bc *BlockChain) pruneBlockCache(expireBefore time.Time) {
	bc.blockCache.Range(func(key, value any) bool {
		if cached, ok := value.(cachedBlock); ok {
			if cached.addedAt.Before(expireBefore) {
				bc.blockCache.Delete(key)
			}
		} else {
			bc.blockCache.Delete(key)
		}
		return true
	})
}

func (bc *BlockChain) pruneBlockNumberCache(expireBefore time.Time) {
	bc.blockNumberToHashCache.Range(func(key, value any) bool {
		if cached, ok := value.(cachedHash); ok {
			if cached.addedAt.Before(expireBefore) {
				bc.blockNumberToHashCache.Delete(key)
			}
		} else {
			bc.blockNumberToHashCache.Delete(key)
		}
		return true
	})
}

func (bc *BlockChain) pruneTxHashCache(expireBefore time.Time) {
	bc.txHashToBlockNumber.Range(func(key, value any) bool {
		if cached, ok := value.(cachedUint64); ok {
			if cached.addedAt.Before(expireBefore) {
				bc.txHashToBlockNumber.Delete(key)
			}
		} else {
			bc.txHashToBlockNumber.Delete(key)
		}
		return true
	})
}

func (bc *BlockChain) pruneEthHashCache(expireBefore time.Time) {
	bc.ethHashMapBlsHash.Range(func(key, value any) bool {
		if cached, ok := value.(cachedHash); ok {
			if cached.addedAt.Before(expireBefore) {
				bc.ethHashMapBlsHash.Delete(key)
			}
		} else {
			bc.ethHashMapBlsHash.Delete(key)
		}
		return true
	})
}

// ============================================================================
// BLOCK & DB OPERATIONS
// ============================================================================

func (bc *BlockChain) AddBlockToCache(block mtn_types.Block) {
	if block == nil || bc.blockCache == nil {
		return
	}
	bc.blockCache.Store(block.Header().Hash(), cachedBlock{
		block:   block,
		addedAt: time.Now(),
	})
}

func (bc *BlockChain) GetBlock(hash common.Hash) mtn_types.Block {
	// 1. Check Cache
	if value, ok := bc.blockCache.Load(hash); ok {
		if cached, ok := value.(cachedBlock); ok {
			if time.Since(cached.addedAt) <= blockCacheTTL {
				return cached.block
			}
			bc.blockCache.Delete(hash)
		}
	}

	// 2. Check DB
	block, err := bc.blockDatabase.GetBlockByHash(hash)
	if err != nil {
		return nil
	}

	// 3. Store Cache (O(1))
	bc.blockCache.Store(hash, cachedBlock{
		block:   block,
		addedAt: time.Now(),
	})
	return block
}

func (bc *BlockChain) GetBlockByNumber(number uint64) mtn_types.Block {
	hash, ok := bc.GetBlockHashByNumber(number)
	if !ok {
		return nil
	}
	return bc.GetBlock(hash)
}

func (bc *BlockChain) GetLastBlock() mtn_types.Block {
	block, err := bc.blockDatabase.GetLastBlock()
	if err != nil {
		return nil
	}
	return block
}

func (bc *BlockChain) NewAccountStateDBFromBlock(blockHeader mtn_types.BlockHeader) (*account_state_db.AccountStateDB, error) {
	accountStateTrie, err := trie.NewStateTrie(
		blockHeader.AccountStatesRoot(),
		bc.storageManager.GetStorageAccount(),
		true,
	)
	if err != nil {
		return nil, err
	}
	return account_state_db.NewAccountStateDB(
		accountStateTrie,
		bc.storageManager.GetStorageAccount()), nil
}

// ============================================================================
// MAPPING & DIRTY STORAGE (Thread-Safe Pointer Swapping)
// ============================================================================

// helper để ghi vào dirty storage an toàn
func (bc *BlockChain) storeToDirty(key string, value []byte) {
	bc.dirtyLock.RLock() // Chỉ cần Read Lock để lấy con trỏ map hiện tại
	defer bc.dirtyLock.RUnlock()
	bc.dirtyStorage.Store(key, value)
}

func (bc *BlockChain) SetBlockNumberToHash(blockNumber uint64, blockHash common.Hash) error {
	key := fmt.Sprintf("%s%d", blockNumberPrefix, blockNumber)

	bc.storeToDirty(key, blockHash.Bytes())

	bc.blockNumberToHashCache.Store(blockNumber, cachedHash{
		hash:    blockHash,
		addedAt: time.Now(),
	})
	return nil
}

func (bc *BlockChain) GetBlockHashByNumber(blockNumber uint64) (common.Hash, bool) {
	if value, ok := bc.blockNumberToHashCache.Load(blockNumber); ok {
		if cached, ok := value.(cachedHash); ok {
			if time.Since(cached.addedAt) <= mappingCacheTTL {
				return cached.hash, true
			}
			bc.blockNumberToHashCache.Delete(blockNumber)
		}
	}

	key := []byte(fmt.Sprintf("%s%d", blockNumberPrefix, blockNumber))
	data, err := bc.storageManager.GetStorageMapping().Get(key)
	if err != nil || data == nil || len(data) != common.HashLength {
		return common.Hash{}, false
	}
	blockHash := common.BytesToHash(data)

	bc.blockNumberToHashCache.Store(blockNumber, cachedHash{
		hash:    blockHash,
		addedAt: time.Now(),
	})
	return blockHash, true
}

func (bc *BlockChain) SetTxHashMapBlockNumber(txHash common.Hash, blockNumber uint64) error {
	// Rate limiting nhẹ nhàng
	select {
	case storeLimiter <- struct{}{}:
		defer func() { <-storeLimiter }()
	default:
		// Drop or wait strategy? Với logic hiện tại, ta chờ 1 chút
		time.Sleep(1 * time.Millisecond)
		storeLimiter <- struct{}{}
		defer func() { <-storeLimiter }()
	}

	key := fmt.Sprintf("%s%s", txHashPrefix, txHash.Hex())
	blockNumberBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockNumberBytes, blockNumber)

	bc.storeToDirty(key, blockNumberBytes)

	bc.txHashToBlockNumber.Store(txHash, cachedUint64{
		value:   blockNumber,
		addedAt: time.Now(),
	})
	return nil
}

func (bc *BlockChain) GetBlockNumberByTxHash(txHash common.Hash) (uint64, bool) {
	if value, ok := bc.txHashToBlockNumber.Load(txHash); ok {
		if cached, ok := value.(cachedUint64); ok {
			if time.Since(cached.addedAt) <= mappingCacheTTL {
				return cached.value, true
			}
			bc.txHashToBlockNumber.Delete(txHash)
		}
	}

	key := []byte(fmt.Sprintf("%s%s", txHashPrefix, txHash.Hex()))
	data, err := bc.storageManager.GetStorageMapping().Get(key)
	if err != nil || data == nil || len(data) != 8 {
		return 0, false
	}
	blockNumber := binary.BigEndian.Uint64(data)

	bc.txHashToBlockNumber.Store(txHash, cachedUint64{
		value:   blockNumber,
		addedAt: time.Now(),
	})
	return blockNumber, true
}

func (bc *BlockChain) SetEthHashMapblsHash(ethHash common.Hash, blsHash common.Hash) error {
	// Lưu ý: Hàm cũ ghi thẳng vào DB, hàm này giữ logic cũ hay chuyển sang dirty?
	// Theo logic cũ: put thẳng vào DB.
	key := fmt.Sprintf("%s%s", ethHashMapBlsHashPrefix, ethHash.Hex())
	err := bc.storageManager.GetStorageMapping().Put([]byte(key), blsHash.Bytes())
	if err != nil {
		return err
	}

	bc.ethHashMapBlsHash.Store(ethHash, cachedHash{
		hash:    blsHash,
		addedAt: time.Now(),
	})
	return nil
}

func (bc *BlockChain) GetEthHashMapblsHash(ethHash common.Hash) (common.Hash, bool) {
	if value, ok := bc.ethHashMapBlsHash.Load(ethHash); ok {
		if cached, ok := value.(cachedHash); ok {
			if time.Since(cached.addedAt) <= mappingCacheTTL {
				return cached.hash, true
			}
			bc.ethHashMapBlsHash.Delete(ethHash)
		}
	}

	key := []byte(fmt.Sprintf("%s%s", ethHashMapBlsHashPrefix, ethHash.Hex()))
	data, err := bc.storageManager.GetStorageMapping().Get(key)
	if err != nil || data == nil || len(data) != common.HashLength {
		return common.Hash{}, false
	}
	blsHash := common.BytesToHash(data)

	bc.ethHashMapBlsHash.Store(ethHash, cachedHash{
		hash:    blsHash,
		addedAt: time.Now(),
	})
	return blsHash, true
}

// Commit ghi tất cả các thay đổi trong dirtyStorage xuống DB và reset map.
// Sử dụng Pointer Swap để đảm bảo Thread-Safety và Hiệu suất cao.
func (bc *BlockChain) Commit() error {
	// 1. Tạo một map mới sạch sẽ
	newCleanMap := new(sync.Map)

	// 2. Lock và tráo con trỏ (Swap Pointer)
	// Thao tác này cực nhanh, chỉ chặn các lệnh Set trong vài nano giây
	bc.dirtyLock.Lock()
	mapToProcess := bc.dirtyStorage // Lấy map hiện tại để xử lý
	bc.dirtyStorage = newCleanMap   // Gán map mới cho các lệnh Set tiếp theo
	bc.dirtyLock.Unlock()

	// 3. Xử lý map cũ (mapToProcess) một cách bất đồng bộ với các luồng ghi mới
	var batch [][2][]byte

	mapToProcess.Range(func(key, value interface{}) bool {
		if k, ok := key.(string); ok {
			if v, ok := value.([]byte); ok {
				batch = append(batch, [2][]byte{[]byte(k), v})
			}
		}
		return true
	})

	if len(batch) > 0 {
		err := bc.storageManager.GetStorageMapping().BatchPut(batch)
		if err != nil {
			logger.Error("Storage BatchPut failed: %v", err)
			return err
		}
		if config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
			data, err := storage.SerializeBatch(batch)
			if err != nil {
				logger.Error("SerializeBatch: %v", err)
			}
			bc.SetMappingBatch(data)
		}
	}

	// mapToProcess sẽ được GC dọn dẹp sau khi hàm này kết thúc
	return nil
}

// Discard hủy bỏ thay đổi
func (bc *BlockChain) Discard() {
	bc.dirtyLock.Lock()
	defer bc.dirtyLock.Unlock()
	bc.dirtyStorage = new(sync.Map)
}
