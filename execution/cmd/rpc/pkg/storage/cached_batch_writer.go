package storage

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/syndtr/goleveldb/leveldb"
)

const (
	// ShardCount số lượng shard cho cache (nên là power of 2 để tối ưu modulo)
	ShardCount = 32
)

type BatchWriteItem interface {
	GetID() string
}
type CachedItem interface {
	GetCachedAt() time.Time
}
type BatchWriteFunc func(item BatchWriteItem) ([][2][]byte, error)

// CacheShard là một mảnh cache với mutex riêng
type CacheShard struct {
	items map[string]CachedItem
	mu    sync.RWMutex
	size  int64 // Atomic counter cho size
}

// CachedBatchWriter quản lý cả cache (sharded) và batch write async cho LevelDB
type CachedBatchWriter struct {
	db            *leveldb.DB
	serializeFunc BatchWriteFunc

	// Sharded cache management
	shards       []*CacheShard
	maxCacheSize int // Giới hạn cache tổng (chia đều cho các shard)
	maxShardSize int // Giới hạn mỗi shard

	// Batch write management
	writeChan    chan BatchWriteItem
	batchSize    int           // Số lượng items trong một batch
	batchTimeout time.Duration // Timeout để flush batch
	stopChan     chan struct{}
	flushReqChan chan chan struct{}
	wg           sync.WaitGroup
}

// NewCachedBatchWriter tạo mới CachedBatchWriter (gộp cache + batch write)
func NewCachedBatchWriter(
	db *leveldb.DB,
	maxCacheSize int,
	batchSize int,
	batchTimeout time.Duration,
	channelBuffer int,
	serializeFunc BatchWriteFunc,
) *CachedBatchWriter {
	// Khởi tạo shards
	shards := make([]*CacheShard, ShardCount)
	for i := 0; i < ShardCount; i++ {
		shards[i] = &CacheShard{
			items: make(map[string]CachedItem),
			size:  0,
		}
	}

	maxShardSize := maxCacheSize / ShardCount
	if maxShardSize < 1 {
		maxShardSize = 1
	}

	cbw := &CachedBatchWriter{
		db:            db,
		serializeFunc: serializeFunc,
		shards:        shards,
		maxCacheSize:  maxCacheSize,
		maxShardSize:  maxShardSize,
		writeChan:     make(chan BatchWriteItem, channelBuffer),
		batchSize:     batchSize,
		batchTimeout:  batchTimeout,
		stopChan:      make(chan struct{}),
		flushReqChan:  make(chan chan struct{}),
	}
	cbw.wg.Add(1)
	go cbw.batchWriter()

	return cbw
}

// getShard chọn shard dựa trên hash của key (nhanh và đều)
func (cbw *CachedBatchWriter) getShard(key string) *CacheShard {
	hash := uint64(14695981039346656037)
	for i := 0; i < len(key); i++ {
		hash ^= uint64(key[i])
		hash *= 1099511628211
	}
	return cbw.shards[hash&(ShardCount-1)]
}

// StoreCache lưu item vào cache (sharded)
func (cbw *CachedBatchWriter) StoreCache(key string, item CachedItem) {
	shard := cbw.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	// Kiểm tra xem đã có trong cache chưa
	_, exists := shard.items[key]
	if !exists {
		// Nếu shard đầy, xóa 1 item nhanh (first item hoặc random)
		currentSize := int(atomic.LoadInt64(&shard.size))
		if currentSize >= cbw.maxShardSize {
			cbw.evictOneFromShard(shard)
		}
		atomic.AddInt64(&shard.size, 1)
	}

	// Update cache
	shard.items[key] = item
}

// evictOneFromShard xóa 1 item từ shard (nhanh - lấy item đầu tiên)
// Note: Đây là eviction nhanh, không cần scan toàn bộ như LRU
func (cbw *CachedBatchWriter) evictOneFromShard(shard *CacheShard) {
	// Xóa item đầu tiên (fastest - O(1) với map iteration)
	// Map iteration order là random, nên đây là eviction gần như random
	for k := range shard.items {
		delete(shard.items, k)
		atomic.AddInt64(&shard.size, -1)
		return
	}
}

// LoadCache lấy item từ cache (sharded)
func (cbw *CachedBatchWriter) LoadCache(key string) (CachedItem, bool) {
	shard := cbw.getShard(key)
	shard.mu.RLock()
	defer shard.mu.RUnlock()

	item, ok := shard.items[key]
	return item, ok
}

// DeleteCache xóa item khỏi cache (sharded)
func (cbw *CachedBatchWriter) DeleteCache(key string) {
	shard := cbw.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if _, exists := shard.items[key]; exists {
		delete(shard.items, key)
		atomic.AddInt64(&shard.size, -1)
	}
}

// GetCacheSize trả về số lượng items trong cache (tổng từ tất cả shards)
func (cbw *CachedBatchWriter) GetCacheSize() int {
	total := int64(0)
	for _, shard := range cbw.shards {
		total += atomic.LoadInt64(&shard.size)
	}
	return int(total)
}

// GetCacheMaxSize trả về giới hạn cache
func (cbw *CachedBatchWriter) GetCacheMaxSize() int {
	return cbw.maxCacheSize
}

// ClearCache xóa tất cả items trong cache (tất cả shards)
func (cbw *CachedBatchWriter) ClearCache() {
	for _, shard := range cbw.shards {
		shard.mu.Lock()
		for k := range shard.items {
			delete(shard.items, k)
		}
		atomic.StoreInt64(&shard.size, 0)
		shard.mu.Unlock()
	}
}

// Write gửi item vào channel để batch write (non-blocking)
func (cbw *CachedBatchWriter) Write(item BatchWriteItem) error {
	select {
	case cbw.writeChan <- item:
		return nil
	default:
		logger.Warn("⚠️ Batch write channel full, dropping item: %s", item.GetID())
		return nil // Non-blocking, chỉ log warning
	}
}

// Flush yêu cầu ghi dữ liệu xuống DB ngay lập tức (Synchronous)
func (cbw *CachedBatchWriter) Flush() {
	done := make(chan struct{})
	cbw.flushReqChan <- done
	<-done
}

// batchWriter xử lý batch write async
func (cbw *CachedBatchWriter) batchWriter() {
	defer cbw.wg.Done()
	pending := make(map[string]BatchWriteItem) // ID -> Item (để tránh duplicate)
	ticker := time.NewTicker(cbw.batchTimeout)
	defer ticker.Stop()

	flush := func() {
		if len(pending) == 0 {
			return
		}

		batch := new(leveldb.Batch)
		totalItems := 0

		// Serialize và add vào batch
		for _, item := range pending {
			kvPairs, err := cbw.serializeFunc(item)
			if err != nil {
				logger.Error("❌ Failed to serialize item %s: %v", item.GetID(), err)
				continue
			}

			// Add tất cả key-value pairs vào batch
			for _, kv := range kvPairs {
				batch.Put(kv[0], kv[1])
			}
			totalItems++
		}

		// Write batch
		if err := cbw.db.Write(batch, nil); err != nil {
			logger.Error("❌ Failed to write batch: %v", err)
		} else {
			logger.Debug("✅ Flushed %d items (%d key-value pairs) to DB", totalItems, batch.Len())
		}

		// Clear pending
		for k := range pending {
			delete(pending, k)
		}
	}

	for {
		select {
		case item := <-cbw.writeChan:
			if item == nil {
				continue
			}
			// Dùng ID để tránh duplicate (item mới sẽ overwrite item cũ cùng ID)
			pending[item.GetID()] = item
			// Flush nếu đủ batch size
			if len(pending) >= cbw.batchSize {
				flush()
			}
		case done := <-cbw.flushReqChan: // Xử lý yêu cầu Flush chủ động
			flush()
			close(done) // Báo cho hàm Flush() ở ngoài biết là đã xong
		case <-ticker.C:
			flush()

		case <-cbw.stopChan:
			// Flush tất cả trước khi stop
			flush()
			return
		}
	}
}

// Close đóng cached batch writer và flush tất cả pending writes
func (cbw *CachedBatchWriter) Close() error {
	close(cbw.stopChan)
	cbw.wg.Wait()
	close(cbw.writeChan)
	return nil
}
