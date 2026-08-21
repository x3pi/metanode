package blockchain

import (
	"encoding/binary"
	"encoding/hex"
	"log"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	mtn_types "github.com/meta-node-blockchain/meta-node/types"
)

var ErrDataPruned = errors.New("data has been pruned")

const (
	blockNumberPrefix       = "blockNumber_"
	txHashPrefix            = "txHashPrefix"            // Tiền tố cho key
	ethHashMapBlsHashPrefix = "ethHashMapBlsHashPrefix" // Tiền tố cho key

	// Cấu hình TTL (Thời gian sống của cache)
	txCacheTTL               = 2 * time.Minute
	blockCacheTTL            = 10 * time.Minute
	mappingCacheTTL          = 30 * time.Minute
	walkbackNegativeCacheTTL = 2 * time.Second

	// walkbackAcquireTimeout bounds how long a caller waits for a free
	// walkback slot (see walkbackSem below) before giving up and treating the
	// lookup as a miss. Keeps behavior elastic under load: when slots are
	// free (normal operation) this never matters: acquisition is instant.
	// When the system is flooded with walkback-triggering lookups, callers
	// shed load quickly instead of queuing indefinitely and piling up
	// goroutines doing expensive block-history scans in parallel.
	walkbackAcquireTimeout = 200 * time.Millisecond

	// Cấu hình Worker dọn dẹp
	cleanupInterval = 1 * time.Minute // Quét dọn mỗi 1 phút
)

var (
	blockChainInstance *BlockChain
	once               sync.Once
	storeLimiter       = make(chan struct{}, 2000) // Tăng limit lên 2000 cho high concurrency

	// walkbackSem bounds how many rebuildTxMappingByWalkback scans (each up
	// to 2000 blocks deep) can run concurrently. Sized relative to
	// GOMAXPROCS so it scales with the machine (and shrinks correctly when
	// GOMAXPROCS is capped for co-located test nodes sharing one box — see
	// main.go). Without this, a flood of eth_getTransactionReceipt polls for
	// hashes that were never actually accepted (buggy client, or many
	// still-pending hashes at once) can spawn thousands of concurrent scans,
	// pegging all CPUs and stalling the Go committer loop — observed
	// directly under a chaotic load-test run, see
	// project_tx_dup_check_walkback_bottleneck memory.
	walkbackSem = make(chan struct{}, walkbackSemCapacity())
)

func walkbackSemCapacity() int {
	capacity := runtime.GOMAXPROCS(0) / 4
	if capacity < 2 {
		capacity = 2
	}
	if capacity > 16 {
		capacity = 16
	}
	return capacity
}

// BlockChain quản lý bộ nhớ đệm và tương tác DB.
// Sử dụng sync.Map cho concurrent read và Background Worker cho việc dọn dẹp.
type BlockChain struct {
	// Cache Layers (Read-Heavy optimization)
	blockCache             *sync.Map
	receiptsCache          *sync.Map
	txsCache               *sync.Map
	blockNumberToHashCache *sync.Map
	txHashToBlockNumber    *txHashToBlockNumberMap
	ethHashMapBlsHash      *ethHashMapBlsHashMap
	// Short-TTL negative cache for the LAZY FALLBACK walkback below: a pending
	// (not-yet-included) tx is a *guaranteed* walkback miss on every single
	// poll a client makes while waiting for its receipt — without this, each
	// eth_getTransactionReceipt poll for a still-pending tx re-scans up to
	// 2000 committed blocks. See project_tx_dup_check_walkback_bottleneck memory.
	walkbackNotFound *sync.Map

	blockDatabase  *block.BlockDatabase
	storageManager *storage.StorageManager
	changelogDB    *state_changelog.StateChangelogDB // Reference to state changelog DB for historical lookups

	// Dirty Storage (Write buffer)
	// Sử dụng pointer để có thể swap nhanh khi commit
	dirtyStorage *dirtyStorageMap
	dirtyLock    sync.RWMutex // Lock nhẹ để bảo vệ việc tráo đổi con trỏ dirtyStorage

	// Worker control
	stopCleanup chan struct{}
	wg          sync.WaitGroup

	// Pruning tracking
	lastPrunedBlockNumber atomic.Uint64
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
	txsCount := len(bl.Transactions())
	// Pre-allocate slice to avoid repeated growing
	mappingBatchKVs := make([][2][]byte, 0, 1+txsCount+len(txs))

	// 1. Block number -> hash
	blockNum := bl.Header().BlockNumber()
	key := blockNumberPrefix + strconv.FormatUint(blockNum, 10)
	mappingBatchKVs = append(mappingBatchKVs, [2][]byte{[]byte(key), bl.Header().Hash().Bytes()})

	// 2. Tx hash -> block number
	blockNumberBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockNumberBytes, blockNum)

	for _, txHash := range bl.Transactions() {
		// txHashPrefix is "txHashPrefix" (length 12) + "0x" (length 2) + hex (64) = 78
		txKeyBytes := make([]byte, 78)
		copy(txKeyBytes[0:12], "txHashPrefix")
		copy(txKeyBytes[12:14], "0x")
		hex.Encode(txKeyBytes[14:], txHash[:])
		mappingBatchKVs = append(mappingBatchKVs, [2][]byte{txKeyBytes, blockNumberBytes})
	}

	// 3. Eth hash -> Bls hash
	// Pre-allocate maps
	dirtyKVs := make(map[string][]byte, len(txs))
	ethKVs := make(map[common.Hash]cachedHash, len(txs))
	now := time.Now()
	for _, tx := range txs {
		ethHash := tx.EthHash()
		if ethHash != (common.Hash{}) {
			// ethHashMapBlsHashPrefix is "ethHashMapBlsHashPrefix" (length 23) + "0x" (length 2) + hex (64) = 89
			ethKeyBytes := make([]byte, 89)
			copy(ethKeyBytes[0:23], "ethHashMapBlsHashPrefix")
			copy(ethKeyBytes[23:25], "0x")
			hex.Encode(ethKeyBytes[25:], ethHash[:])
			mappingBatchKVs = append(mappingBatchKVs, [2][]byte{ethKeyBytes, tx.Hash().Bytes()})

			ethKey := string(ethKeyBytes)
			dirtyKVs[ethKey] = tx.Hash().Bytes()
			ethKVs[ethHash] = cachedHash{
				hash:    tx.Hash(),
				addedAt: now,
			}
		}
	}
	if len(dirtyKVs) > 0 {
		bc.storeBatchToDirty(dirtyKVs)
	}
	if len(ethKVs) > 0 {
		bc.ethHashMapBlsHash.StoreBatch(ethKVs)
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
			txHashToBlockNumber:    newTxHashToBlockNumberMap(),
			ethHashMapBlsHash:      newEthHashMapBlsHashMap(),
			walkbackNotFound:       new(sync.Map),

			dirtyStorage: newDirtyStorageMap(), // Khởi tạo pointer

			blockDatabase:  blockDatabase,
			storageManager: storageManager,
			stopCleanup:    make(chan struct{}),
		}

		if storageManager != nil && storageManager.GetStorageMapping() != nil {
			key := []byte("last_pruned_block_number")
			data, err := storageManager.GetStorageMapping().Get(key)
			if err == nil && len(data) == 8 {
				blockChainInstance.lastPrunedBlockNumber.Store(binary.BigEndian.Uint64(data))
			}
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
				bc.pruneWalkbackNotFoundCache(now)
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

func (bc *BlockChain) pruneWalkbackNotFoundCache(now time.Time) {
	bc.walkbackNotFound.Range(func(key, value any) bool {
		if until, ok := value.(time.Time); !ok || now.After(until) {
			bc.walkbackNotFound.Delete(key)
		}
		return true
	})
}

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
	bc.txHashToBlockNumber.Prune(expireBefore)
}

func (bc *BlockChain) pruneEthHashCache(expireBefore time.Time) {
	bc.ethHashMapBlsHash.Prune(expireBefore)
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

func (bc *BlockChain) storeBatchToDirty(kvs map[string][]byte) {
	bc.dirtyLock.RLock()
	defer bc.dirtyLock.RUnlock()
	bc.dirtyStorage.StoreBatch(kvs)
}

func (bc *BlockChain) SetBlockNumberToHash(blockNumber uint64, blockHash common.Hash) error {
	key := blockNumberPrefix + strconv.FormatUint(blockNumber, 10)

	bc.storeToDirty(key, blockHash.Bytes())

	bc.blockNumberToHashCache.Store(blockNumber, cachedHash{
		hash:    blockHash,
		addedAt: time.Now(),
	})
	return nil
}

func (bc *BlockChain) GetBlockHashByNumber(blockNumber uint64) (common.Hash, bool) {
	if lastPruned := bc.GetLastPrunedBlockNumber(); lastPruned > 0 && blockNumber <= lastPruned {
		return common.Hash{}, false
	}

	if value, ok := bc.blockNumberToHashCache.Load(blockNumber); ok {
		if cached, ok := value.(cachedHash); ok {
			if time.Since(cached.addedAt) <= mappingCacheTTL {
				return cached.hash, true
			}
			bc.blockNumberToHashCache.Delete(blockNumber)
		}
	}

	key := []byte(blockNumberPrefix + strconv.FormatUint(blockNumber, 10))
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
	lastPruned := bc.GetLastPrunedBlockNumber()
	if lastPruned > 0 && targetBlockNumber <= lastPruned {
		return common.Hash{}, false
	}

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
	const maxDepth = 2000
	var depth int

	for blk != nil && depth < maxDepth {
		bNum := blk.Header().BlockNumber()
		if lastPruned > 0 && bNum <= lastPruned {
			break
		}

		// Check if mapping already exists for this block
		if _, exists := bc.blockNumberToHashCache.Load(bNum); !exists {
			// Also check DB
			dbKey := []byte(blockNumberPrefix + strconv.FormatUint(bNum, 10))
			dbData, dbErr := bc.storageManager.GetStorageMapping().Get(dbKey)
			if dbErr != nil || dbData == nil || len(dbData) != common.HashLength {
				// Missing mapping — rebuild it
				bHash := blk.Header().Hash()
				bc.storeToDirty(blockNumberPrefix+strconv.FormatUint(bNum, 10), bHash.Bytes())
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
		depth++
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

	key := txHashPrefix + txHash.Hex()
	blockNumberBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockNumberBytes, blockNumber)

	bc.storeToDirty(key, blockNumberBytes)

	bc.txHashToBlockNumber.Store(txHash, cachedUint64{
		value:   blockNumber,
		addedAt: time.Now(),
	})
	return nil
}

func (bc *BlockChain) SetTxHashMapBlockNumberBatch(txHashes []common.Hash, blockNumber uint64) error {
	blockNumberBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockNumberBytes, blockNumber)

	dirtyKVs := make(map[string][]byte)
	cacheKVs := make(map[common.Hash]cachedUint64)
	now := time.Now()

	for _, txHash := range txHashes {
		key := txHashPrefix + txHash.Hex()
		dirtyKVs[key] = blockNumberBytes
		cacheKVs[txHash] = cachedUint64{
			value:   blockNumber,
			addedAt: now,
		}
	}

	bc.storeBatchToDirty(dirtyKVs)
	bc.txHashToBlockNumber.StoreBatch(cacheKVs)
	return nil
}

// MarkSubmittedPending pre-seeds the walkback negative-cache for a tx hash at
// the moment it's accepted into the mempool, before the client can possibly
// poll for its receipt. A just-submitted tx is guaranteed not to be in any
// committed block yet, so the very first eth_getTransactionReceipt poll for
// it would otherwise pay a full (up to 2000-block) block-history scan
// for a guaranteed miss — observed under sustained load as hundreds of
// goroutines piled up in rebuildTxMappingByWalkback simultaneously, one per
// distinct newly-pending hash. Seeding here means that guaranteed-miss walk
// never happens; the reactive negative-cache (set inside GetBlockNumberByTxHash
// after a real walkback miss) continues to cover polls beyond the TTL if
// confirmation takes longer than walkbackNegativeCacheTTL.
func (bc *BlockChain) MarkSubmittedPending(txHash common.Hash) {
	bc.walkbackNotFound.Store(txHash, time.Now().Add(walkbackNegativeCacheTTL))
}

// GetBlockNumberByTxHashFast checks only the in-memory cache and the direct
// mapping-DB entry — O(1), no block-history scan. Suitable for hot paths that
// need a cheap "have we already committed this tx" check (e.g. duplicate-tx
// rejection for a brand-new submission), where the overwhelming majority of
// lookups are genuine misses and a full history walk would be wasted work.
// It does NOT see transactions whose mapping entry was lost (e.g. after a
// crash before the mapping was flushed) — callers that need that guarantee
// (RPC tx/receipt lookups for a hash a client claims already exists) should
// use GetBlockNumberByTxHash instead.
func (bc *BlockChain) GetBlockNumberByTxHashFast(txHash common.Hash) (uint64, bool) {
	if value, ok := bc.txHashToBlockNumber.Load(txHash); ok {
		if cached, ok := value.(cachedUint64); ok {
			if time.Since(cached.addedAt) <= mappingCacheTTL {
				return cached.value, true
			}
			bc.txHashToBlockNumber.Delete(txHash)
		}
	}

	key := []byte(txHashPrefix + txHash.Hex())
	data, err := bc.storageManager.GetStorageMapping().Get(key)
	if err == nil && data != nil && len(data) == 8 {
		blockNumber := binary.BigEndian.Uint64(data)
		bc.txHashToBlockNumber.Store(txHash, cachedUint64{
			value:   blockNumber,
			addedAt: time.Now(),
		})
		return blockNumber, true
	}

	return 0, false
}

func (bc *BlockChain) GetBlockNumberByTxHash(txHash common.Hash) (uint64, bool) {
	if blockNumber, ok := bc.GetBlockNumberByTxHashFast(txHash); ok {
		return blockNumber, true
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// LAZY FALLBACK (June 2026): Rebuild transaction mapping from block DB.
	//
	// Negative-cache guard (Aug 2026): a still-pending tx is a guaranteed
	// walkback miss on every poll a client makes while waiting for its
	// receipt (eth_getTransactionReceipt is typically polled every tens of
	// ms until confirmed). Without this, that's a full up-to-2000-block scan
	// per poll per pending tx. Cache "not found" for a short TTL so repeated
	// polls for the same still-pending hash don't re-walk the chain; a real
	// crash-recovery lookup (rare, and not latency-sensitive) still gets a
	// fresh walkback once the short TTL expires.
	// ═══════════════════════════════════════════════════════════════════════════
	if v, ok := bc.walkbackNotFound.Load(txHash); ok {
		if until, ok := v.(time.Time); ok && time.Now().Before(until) {
			return 0, false
		}
		bc.walkbackNotFound.Delete(txHash)
	}

	if bc.blockDatabase != nil {
		// Elastic load-shedding: bound how many walkback scans run at once
		// (walkbackSem, sized off GOMAXPROCS) and how long a caller waits for
		// a free slot (walkbackAcquireTimeout). Under normal load a slot is
		// free instantly; under a flood, callers give up quickly and are
		// treated as a miss (negative-cached, same as a real miss) instead of
		// piling up thousands of concurrent expensive scans.
		select {
		case walkbackSem <- struct{}{}:
			blockNumber, ok := bc.rebuildTxMappingByWalkback(txHash)
			<-walkbackSem
			if ok {
				logger.Info("✅ [LAZY-FALLBACK] Walkback search found transaction %s in block #%d", txHash.Hex(), blockNumber)
				return blockNumber, true
			}
		case <-time.After(walkbackAcquireTimeout):
			logger.Warn("⚠️ [LAZY-FALLBACK] Walkback slot busy, shedding load for %s", txHash.Hex())
		}
		bc.walkbackNotFound.Store(txHash, time.Now().Add(walkbackNegativeCacheTTL))
	}

	return 0, false
}

func (bc *BlockChain) rebuildTxMappingByWalkback(targetTxHash common.Hash) (uint64, bool) {
	lastBlock, err := bc.blockDatabase.GetLastBlock()
	if err != nil || lastBlock == nil {
		logger.Error("❌ [LAZY-TX-REBUILD] GetLastBlock failed: %v", err)
		return 0, false
	}

	lastPruned := bc.GetLastPrunedBlockNumber()

	// Walk backwards from lastBlock
	blk := lastBlock
	const maxDepth = 2000 // Safely scan up to 2000 blocks to prevent RPC hang
	var depth int

	for blk != nil && depth < maxDepth {
		bNum := blk.Header().BlockNumber()
		if lastPruned > 0 && bNum <= lastPruned {
			break
		}

		txHashes := blk.Transactions()
		if len(txHashes) > 0 {
			txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blk.Header().TransactionsRoot(), bc.storageManager.GetStorageTransaction())
			if err == nil && txDB != nil {
				found := false
				var txList []mtn_types.Transaction
				for _, tHash := range txHashes {
					tx, err := txDB.GetTransaction(tHash)
					if err == nil && tx != nil {
						txList = append(txList, tx)
						if tx.Hash() == targetTxHash || tx.EthHash() == targetTxHash {
							found = true
						}
					}
				}

				if found {
					logger.Info("🔄 [LAZY-TX-REBUILD] Found tx %s in block #%d. Rebuilding block tx mappings...", targetTxHash.Hex()[:18], bNum)

					// Rebuild mappings for all transactions in this block
					var hashes []common.Hash
					for _, t := range txList {
						hashes = append(hashes, t.Hash())
						hashes = append(hashes, t.EthHash())
						bc.SetEthHashMapblsHash(t.EthHash(), t.Hash())
					}
					bc.SetTxHashMapBlockNumberBatch(hashes, bNum)

					// Commit to DB so mappings persist
					if commitErr := bc.Commit(); commitErr != nil {
						logger.Error("❌ [LAZY-TX-REBUILD] Failed to commit rebuilt tx mappings: %v", commitErr)
					} else if bc.storageManager != nil {
						if flushErr := bc.storageManager.GetStorageMapping().Flush(); flushErr != nil {
							logger.Error("❌ [LAZY-TX-REBUILD] Failed to flush mapping DB: %v", flushErr)
						}
					}
					return bNum, true
				}
			}
		}

		if bNum == 0 {
			break
		}

		parentHash := blk.Header().LastBlockHash()
		if parentHash == (common.Hash{}) {
			break
		}
		parentBlk, pErr := bc.blockDatabase.GetBlockByHash(parentHash)
		if pErr != nil || parentBlk == nil {
			break
		}
		blk = parentBlk
		depth++
	}

	return 0, false
}

func (bc *BlockChain) SetEthHashMapblsHash(ethHash common.Hash, blsHash common.Hash) error {
	key := ethHashMapBlsHashPrefix + ethHash.Hex()
	bc.storeToDirty(key, blsHash.Bytes())

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

	key := []byte(ethHashMapBlsHashPrefix + ethHash.Hex())
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
	newCleanMap := newDirtyStorageMap()

	// 2. Lock và tráo con trỏ (Swap Pointer)
	// Thao tác này cực nhanh, chỉ chặn các lệnh Set trong vài nano giây
	bc.dirtyLock.Lock()
	mapToProcess := bc.dirtyStorage // Lấy map hiện tại để xử lý
	bc.dirtyStorage = newCleanMap   // Gán map mới cho các lệnh Set tiếp theo
	bc.dirtyLock.Unlock()

	// 3. Xử lý map cũ (mapToProcess) một cách bất đồng bộ với các luồng ghi mới
	var batch [][2][]byte

	mapToProcess.Range(func(key, value any) bool {
		if k, ok := key.(string); ok {
			if v, ok := value.([]byte); ok {
				batch = append(batch, [2][]byte{[]byte(k), v})
			}
		}
		return true
	})

	if len(batch) > 0 {
		// TPS OPTIMIZATION: Write mapping batch to LevelDB/PebbleDB asynchronously.
		// Since read queries query in-memory caches first (which are updated synchronously),
		// returning immediately here avoids blocking the block commit worker thread
		// on disk I/O, speeding up block execution by ~100ms per block.
		go func(b [][2][]byte) {
			err := bc.storageManager.GetStorageMapping().BatchPut(b)
			if err != nil {
				logger.Error("Storage BatchPut failed: %v", err)
			}
		}(batch)
	}

	// mapToProcess sẽ được GC dọn dẹp sau khi hàm này kết thúc
	return nil
}

// Discard hủy bỏ thay đổi toàn bộ (CẢNH BÁO: Không dùng trong pipeline xử lý block vì sẽ xóa block đang chờ commit)
func (bc *BlockChain) Discard() {
	bc.dirtyLock.Lock()
	defer bc.dirtyLock.Unlock()
	bc.dirtyStorage = newDirtyStorageMap()
}

func (bc *BlockChain) removeFromDirty(key string) {
	bc.dirtyLock.RLock()
	defer bc.dirtyLock.RUnlock()
	bc.dirtyStorage.Delete(key)
}

// DiscardBlockMappings safely removes only the mappings associated with a specific block number
func (bc *BlockChain) DiscardBlockMappings(blockNumber uint64) {
	key := blockNumberPrefix + strconv.FormatUint(blockNumber, 10)
	bc.removeFromDirty(key)
	bc.blockNumberToHashCache.Delete(blockNumber)
}

// DeleteBlockHashMapping permanently removes the mapping from the database for pruning
func (bc *BlockChain) DeleteBlockHashMapping(blockNumber uint64) error {
	key := []byte(blockNumberPrefix + strconv.FormatUint(blockNumber, 10))
	bc.blockNumberToHashCache.Delete(blockNumber)
	return bc.storageManager.GetStorageMapping().Delete(key)
}

func (bc *BlockChain) SetLastPrunedBlockNumber(blockNumber uint64) error {
	bc.lastPrunedBlockNumber.Store(blockNumber)

	// Persist to DB so it survives restarts
	if bc.storageManager != nil && bc.storageManager.GetStorageMapping() != nil {
		key := []byte("last_pruned_block_number")
		blockNumberBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(blockNumberBytes, blockNumber)
		return bc.storageManager.GetStorageMapping().Put(key, blockNumberBytes)
	}
	return nil
}

func (bc *BlockChain) GetLastPrunedBlockNumber() uint64 {
	return bc.lastPrunedBlockNumber.Load()
}

// ============================================================================
// CUSTOM MUTEX-PROTECTED MAPS FOR HIGH WRITE PERFORMANCE
// ============================================================================

type dirtyStorageMap struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newDirtyStorageMap() *dirtyStorageMap {
	return &dirtyStorageMap{
		data: make(map[string][]byte),
	}
}

func (m *dirtyStorageMap) Store(key, value any) {
	m.mu.Lock()
	k, ok1 := key.(string)
	v, ok2 := value.([]byte)
	if ok1 && ok2 {
		m.data[k] = v
	}
	m.mu.Unlock()
}

func (m *dirtyStorageMap) StoreBatch(kvs map[string][]byte) {
	m.mu.Lock()
	for k, v := range kvs {
		m.data[k] = v
	}
	m.mu.Unlock()
}

func (m *dirtyStorageMap) Load(key string) (any, bool) {
	m.mu.RLock()
	val, ok := m.data[key]
	m.mu.RUnlock()
	return val, ok
}

func (m *dirtyStorageMap) Delete(key any) {
	m.mu.Lock()
	if k, ok := key.(string); ok {
		delete(m.data, k)
	}
	m.mu.Unlock()
}

func (m *dirtyStorageMap) Range(f func(key, value any) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.data {
		if !f(k, v) {
			break
		}
	}
}

type ethHashMapBlsHashMap struct {
	mu   sync.RWMutex
	data map[common.Hash]cachedHash
}

func newEthHashMapBlsHashMap() *ethHashMapBlsHashMap {
	return &ethHashMapBlsHashMap{
		data: make(map[common.Hash]cachedHash),
	}
}

func (m *ethHashMapBlsHashMap) Store(key, value any) {
	m.mu.Lock()
	k, ok1 := key.(common.Hash)
	v, ok2 := value.(cachedHash)
	if ok1 && ok2 {
		m.data[k] = v
	}
	m.mu.Unlock()
}

func (m *ethHashMapBlsHashMap) StoreBatch(kvs map[common.Hash]cachedHash) {
	m.mu.Lock()
	for k, v := range kvs {
		m.data[k] = v
	}
	m.mu.Unlock()
}

func (m *ethHashMapBlsHashMap) Load(key common.Hash) (any, bool) {
	m.mu.RLock()
	val, ok := m.data[key]
	m.mu.RUnlock()
	return val, ok
}

func (m *ethHashMapBlsHashMap) Delete(key any) {
	m.mu.Lock()
	if k, ok := key.(common.Hash); ok {
		delete(m.data, k)
	}
	m.mu.Unlock()
}

func (m *ethHashMapBlsHashMap) Prune(expireBefore time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.data {
		if v.addedAt.Before(expireBefore) {
			delete(m.data, k)
		}
	}
}

type txHashToBlockNumberMap struct {
	mu   sync.RWMutex
	data map[common.Hash]cachedUint64
}

func newTxHashToBlockNumberMap() *txHashToBlockNumberMap {
	return &txHashToBlockNumberMap{
		data: make(map[common.Hash]cachedUint64),
	}
}

func (m *txHashToBlockNumberMap) Store(key, value any) {
	m.mu.Lock()
	k, ok1 := key.(common.Hash)
	v, ok2 := value.(cachedUint64)
	if ok1 && ok2 {
		m.data[k] = v
	}
	m.mu.Unlock()
}

func (m *txHashToBlockNumberMap) StoreBatch(kvs map[common.Hash]cachedUint64) {
	m.mu.Lock()
	for k, v := range kvs {
		m.data[k] = v
	}
	m.mu.Unlock()
}

func (m *txHashToBlockNumberMap) Load(key common.Hash) (any, bool) {
	m.mu.RLock()
	val, ok := m.data[key]
	m.mu.RUnlock()
	return val, ok
}

func (m *txHashToBlockNumberMap) Delete(key any) {
	m.mu.Lock()
	if k, ok := key.(common.Hash); ok {
		delete(m.data, k)
	}
	m.mu.Unlock()
}

func (m *txHashToBlockNumberMap) Prune(expireBefore time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.data {
		if v.addedAt.Before(expireBefore) {
			delete(m.data, k)
		}
	}
}
