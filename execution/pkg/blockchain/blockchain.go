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
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
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
	changelogDB    *state_changelog.StateChangelogDB // Reference to state changelog DB for historical lookups

	// Dirty Storage (Write buffer)
	// Sử dụng pointer *sync.Map để có thể swap nhanh khi commit
	dirtyStorage *sync.Map
	dirtyLock    sync.RWMutex // Lock nhẹ để bảo vệ việc tráo đổi con trỏ dirtyStorage

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

func (bc *BlockChain) GenerateMappingBatchForBlock(bl mtn_types.Block, txs []mtn_types.Transaction) ([]byte, error) {
	var mappingBatchKVs [][2][]byte

	// 1. Block number -> hash
	blockNum := bl.Header().BlockNumber()
	key := fmt.Sprintf("%s%d", blockNumberPrefix, blockNum)
	mappingBatchKVs = append(mappingBatchKVs, [2][]byte{[]byte(key), bl.Header().Hash().Bytes()})

	// 2. Tx hash -> block number
	blockNumberBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockNumberBytes, blockNum)
	for _, txHash := range bl.Transactions() {
		txKey := fmt.Sprintf("%s%s", txHashPrefix, txHash.Hex())
		mappingBatchKVs = append(mappingBatchKVs, [2][]byte{[]byte(txKey), blockNumberBytes})
	}

	// 3. Eth hash -> Bls hash
	for _, tx := range txs {
		ethTx := tx.ToEthTransaction()
		if ethTx != nil {
			ethHash := ethTx.Hash()
			if ethHash != (common.Hash{}) {
				ethKey := fmt.Sprintf("%s%s", ethHashMapBlsHashPrefix, ethHash.Hex())
				mappingBatchKVs = append(mappingBatchKVs, [2][]byte{[]byte(ethKey), tx.Hash().Bytes()})
			}
		}
	}

	serializedMappingBatch, err := storage.SerializeBatch(mappingBatchKVs)
	if err != nil {
		logger.Error("GenerateMappingBatchForBlock: failed to serialize mapping batch: %v", err)
		return nil, err
	}
	return serializedMappingBatch, nil
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

func (bc *BlockChain) SetChangelogDB(db *state_changelog.StateChangelogDB) {
	bc.changelogDB = db
}

func (bc *BlockChain) GetChangelogDB() *state_changelog.StateChangelogDB {
	return bc.changelogDB
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
	if nomtTrie, ok := accountStateTrie.(*trie.NomtStateTrie); ok && bc.changelogDB != nil {
		nomtTrie.SetChangelogDB(bc.changelogDB)
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
	if err == nil && data != nil && len(data) == common.HashLength {
		blockHash := common.BytesToHash(data)
		bc.blockNumberToHashCache.Store(blockNumber, cachedHash{
			hash:    blockHash,
			addedAt: time.Now(),
		})
		return blockHash, true
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// LAZY FALLBACK (May 2026): Mapping missing from both cache and DB.
	// This happens when dirtyStorage was not flushed before a crash/SIGTERM.
	// Walk backwards from lastBlock via parentHash chain to find and rebuild
	// the missing mapping. This is O(N) worst-case but only triggers on
	// actual cache/DB misses, and rebuilds all intermediate mappings too.
	// ═══════════════════════════════════════════════════════════════════════════
	if bc.blockDatabase != nil {
		hash, ok := bc.rebuildMappingByWalkback(blockNumber)
		if ok {
			return hash, true
		}
	}

	return common.Hash{}, false
}

// rebuildMappingByWalkback walks backwards from lastBlock through parentHash
// chain to find the block at blockNumber and rebuild all missing mappings.
// Returns the hash of the target block if found.
func (bc *BlockChain) rebuildMappingByWalkback(targetBlockNumber uint64) (common.Hash, bool) {
	lastBlock, err := bc.blockDatabase.GetLastBlock()
	if err != nil || lastBlock == nil {
		return common.Hash{}, false
	}

	lastBlockNum := lastBlock.Header().BlockNumber()
	if targetBlockNumber > lastBlockNum {
		return common.Hash{}, false
	}

	// Walk backwards from lastBlock
	blk := lastBlock
	var rebuiltCount int
	var targetHash common.Hash
	found := false

	for blk != nil {
		bNum := blk.Header().BlockNumber()

		// Check if mapping already exists for this block
		if _, exists := bc.blockNumberToHashCache.Load(bNum); !exists {
			// Also check DB
			dbKey := []byte(fmt.Sprintf("%s%d", blockNumberPrefix, bNum))
			dbData, dbErr := bc.storageManager.GetStorageMapping().Get(dbKey)
			if dbErr != nil || dbData == nil || len(dbData) != common.HashLength {
				// Missing mapping — rebuild it
				bHash := blk.Header().Hash()
				bc.storeToDirty(fmt.Sprintf("%s%d", blockNumberPrefix, bNum), bHash.Bytes())
				bc.blockNumberToHashCache.Store(bNum, cachedHash{
					hash:    bHash,
					addedAt: time.Now(),
				})
				rebuiltCount++

				if bNum == targetBlockNumber {
					targetHash = bHash
					found = true
				}
			} else {
				// Mapping exists in DB — cache it and stop if we're past our target
				existingHash := common.BytesToHash(dbData)
				bc.blockNumberToHashCache.Store(bNum, cachedHash{
					hash:    existingHash,
					addedAt: time.Now(),
				})
				if bNum == targetBlockNumber {
					targetHash = existingHash
					found = true
				}
				if bNum <= targetBlockNumber {
					break // All mappings at or below target exist, stop walking
				}
			}
		} else if bNum <= targetBlockNumber {
			// Cache hit at or below target — we've already rebuilt everything needed
			if bNum == targetBlockNumber {
				if cached, ok := bc.blockNumberToHashCache.Load(bNum); ok {
					if ch, ok := cached.(cachedHash); ok {
						targetHash = ch.hash
						found = true
					}
				}
			}
			break
		}

		if bNum == 0 {
			break
		}

		// Walk to parent
		parentHash := blk.Header().LastBlockHash()
		if parentHash == (common.Hash{}) {
			break
		}
		parentBlk, pErr := bc.blockDatabase.GetBlockByHash(parentHash)
		if pErr != nil || parentBlk == nil {
			break
		}
		blk = parentBlk
	}

	// Commit rebuilt mappings to DB so they survive restart
	if rebuiltCount > 0 {
		logger.Info("🔄 [LAZY-REBUILD] Rebuilt %d missing block→hash mappings (target=#%d, found=%v)", rebuiltCount, targetBlockNumber, found)
		if commitErr := bc.Commit(); commitErr != nil {
			logger.Error("❌ [LAZY-REBUILD] Failed to commit rebuilt mappings: %v", commitErr)
		} else if bc.storageManager != nil {
			if flushErr := bc.storageManager.GetStorageMapping().Flush(); flushErr != nil {
				logger.Error("❌ [LAZY-REBUILD] Failed to flush mapping DB: %v", flushErr)
			}
		}
	}

	return targetHash, found
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
	}

	// mapToProcess sẽ được GC dọn dẹp sau khi hàm này kết thúc
	return nil
}

// Discard hủy bỏ thay đổi toàn bộ (CẢNH BÁO: Không dùng trong pipeline xử lý block vì sẽ xóa block đang chờ commit)
func (bc *BlockChain) Discard() {
	bc.dirtyLock.Lock()
	defer bc.dirtyLock.Unlock()
	bc.dirtyStorage = new(sync.Map)
}

func (bc *BlockChain) removeFromDirty(key string) {
	bc.dirtyLock.RLock()
	defer bc.dirtyLock.RUnlock()
	bc.dirtyStorage.Delete(key)
}

// DiscardBlockMappings safely removes only the mappings associated with a specific block number
func (bc *BlockChain) DiscardBlockMappings(blockNumber uint64) {
	key := fmt.Sprintf("%s%d", blockNumberPrefix, blockNumber)
	bc.removeFromDirty(key)
	bc.blockNumberToHashCache.Delete(blockNumber)
}
