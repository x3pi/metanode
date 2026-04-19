package storage

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// LazyLevelDB is a wrapper around ReplicatedLevelDB that buffers Put and Delete
// operations in memory and asynchronously flushes them to disk to avoid I/O bottlenecks.
type LazyLevelDB struct {
	db            *ReplicatedLevelDB
	memoryCache   *ShardedMap // string -> []byte (nil value means deleted)
	flushingCache *ShardedMap // holds the cache currently being written to disk
	flushTicker   *time.Ticker
	closeChan     chan struct{}
	wg            sync.WaitGroup
	isClosed      bool
	mu            sync.RWMutex
	flushCounter  int
}

const (
	// FlushInterval defines how often the memory cache is written to disk.
	// 10s balances crash-safety with I/O pressure. LazyPebbleDB now does full
	// memtable→SST flush (not just Go cache→memtable), so longer interval
	// reduces compaction pressure while still ensuring data reaches disk promptly.
	FlushInterval = 10 * time.Second
)

// stringToBytes handles zero allocation string to byte slice conversion safely here
func stringToBytes(s string) []byte {
	return []byte(s)
}

func NewLazyLevelDB(path string) *LazyLevelDB {
	lazyDB := &LazyLevelDB{
		db:          NewReplicatedLevelDB(path),
		memoryCache: NewShardedMap(),
		closeChan:   make(chan struct{}),
	}
	return lazyDB
}

// Open initializes the underlying DB and starts the background flusher Goroutine.
func (ldb *LazyLevelDB) Open(parallelism int) error {
	if err := ldb.db.Open(parallelism); err != nil {
		return err
	}

	ldb.flushTicker = time.NewTicker(FlushInterval)
	ldb.wg.Add(1)
	go ldb.backgroundFlusher()

	logger.Info("✅ [LAZY DB] Opened database with Async Memory Cache at: %s", ldb.db.path)
	return nil
}

// backgroundFlusher periodically writes all dirty records from memory to disk.
func (ldb *LazyLevelDB) backgroundFlusher() {
	defer ldb.wg.Done()
	for {
		select {
		case <-ldb.flushTicker.C:
			ldb.flushToDisk()
		case <-ldb.closeChan:
			logger.Info("🛑 [LAZY DB] Stopping background flusher for %s", ldb.db.path)
			ldb.flushToDisk() // Final flush before closing
			return
		}
	}
}

// flushToDisk takes a snapshot of the current memory cache, clears it, and writes
// the snapshot to the underlying LevelDB in a single batch.
func (ldb *LazyLevelDB) flushToDisk() {
	ldb.mu.Lock()
	if ldb.isClosed {
		ldb.mu.Unlock()
		return
	}

	// We don't want to hold ldb.mu for the entire BatchPut operation to allow concurrent Gets/Puts.
	// However, if we simply swap the cache and release the lock, a concurrent Get might check
	// the empty new memoryCache, miss, check the underlying DB, miss (since BatchPut hasn't finished),
	// and incorrectly return "not found".
	//
	// Fix: Keep a reference to the old cache in the LazyLevelDB struct (flushingCache) while flushing.

	oldCache := ldb.memoryCache
	ldb.flushingCache = oldCache // Expose it to Get()
	ldb.memoryCache = NewShardedMap()
	ldb.mu.Unlock()

	var batch [][2][]byte
	var deletedKeys [][]byte
	var count int

	oldCache.Range(func(kStr string, vBytes []byte) bool {
		kBytes := stringToBytes(kStr)

		if vBytes == nil {
			deletedKeys = append(deletedKeys, kBytes)
		} else {
			batch = append(batch, [2][]byte{kBytes, vBytes})
		}
		count++
		return true // Continue iteration
	})

	if count == 0 {
		ldb.mu.Lock()
		ldb.flushingCache = nil
		ldb.mu.Unlock()
		return // Nothing to flush
	}

	// 1. Process deletes
	for _, key := range deletedKeys {
		if err := ldb.db.Delete(key); err != nil {
			logger.Error("❌ [LAZY DB FLUSH] Failed to delete key from underlying DB: %v", err)
		}
	}

	// 2. Process puts as a batch (chunked to prevent large batch storms)
	if len(batch) > 0 {
		ldb.flushCounter++
		start := time.Now()
		const chunkSize = 10000
		for i := 0; i < len(batch); i += chunkSize {
			end := i + chunkSize
			if end > len(batch) {
				end = len(batch)
			}
			if err := ldb.db.BatchPut(batch[i:end]); err != nil {
				logger.Error("❌ [LAZY DB FLUSH] Failed to BatchPut chunk: %v", err)
			}
		}
		logger.Info("💾 [LAZY DB FLUSH #%d] Flushed %d records (%d chunks) to disk in %v (%s)",
			ldb.flushCounter, len(batch), (len(batch)+chunkSize-1)/chunkSize, time.Since(start), ldb.db.path)
	}

	// Clear flushingCache now that disk write is done
	ldb.mu.Lock()
	ldb.flushingCache = nil
	ldb.mu.Unlock()
}

// Get checks the memory cache first. If found, it returns the value (or error if deleted).
// If not found in cache, it retrieves it from the underlying DB.
func (ldb *LazyLevelDB) Get(key []byte) ([]byte, error) {
	keyStr := string(key)

	ldb.mu.RLock()
	val, ok := ldb.memoryCache.Load(keyStr)

	// Check flushing cache if not found in active cache
	var flushVal []byte
	var flushOk bool
	if !ok && ldb.flushingCache != nil {
		flushVal, flushOk = ldb.flushingCache.Load(keyStr)
	}
	ldb.mu.RUnlock()

	// 1. Check active memory cache
	if ok {
		if val == nil {
			return nil, fmt.Errorf("leveldb: not found (tombstoned in cache)")
		}
		return val, nil
	}

	// 2. Check flushing cache (currently being written to disk)
	if flushOk {
		if flushVal == nil {
			return nil, fmt.Errorf("leveldb: not found (tombstoned in cache)")
		}
		return flushVal, nil
	}

	// 3. Not in any memory cache, fetch from disk
	return ldb.db.Get(key)
}

// Put buffers the write operation in memory.
func (ldb *LazyLevelDB) Put(key, value []byte) error {
	ldb.mu.RLock()
	defer ldb.mu.RUnlock()

	if ldb.isClosed {
		return fmt.Errorf("database is closed")
	}

	// We must copy the value slice because the caller might modify it after Put returns
	valCopy := make([]byte, len(value))
	copy(valCopy, value)

	ldb.memoryCache.Store(string(key), valCopy)
	return nil
}

// Delete buffers the delete operation as a tombstone (nil value) in memory.
func (ldb *LazyLevelDB) Delete(key []byte) error {
	ldb.mu.RLock()
	defer ldb.mu.RUnlock()

	if ldb.isClosed {
		return fmt.Errorf("database is closed")
	}

	ldb.memoryCache.Store(string(key), nil) // nil represents a tombstone
	return nil
}

// BatchPut buffers multiple write operations in memory.
func (ldb *LazyLevelDB) BatchPut(keys [][2][]byte) error {
	ldb.mu.RLock()
	defer ldb.mu.RUnlock()

	if ldb.isClosed {
		return fmt.Errorf("database is closed")
	}

	for _, kv := range keys {
		valCopy := make([]byte, len(kv[1]))
		copy(valCopy, kv[1])
		ldb.memoryCache.Store(string(kv[0]), valCopy)
	}

	return nil
}

// PrefixScan merges results from memory cache, flushing cache, and disk.
// Returns key-value pairs with prefix stripped from keys.
func (ldb *LazyLevelDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	prefixStr := string(prefix)

	// 1. Scan disk first
	diskResults, err := ldb.db.PrefixScan(prefix)
	if err != nil {
		return nil, err
	}

	// 2. Build merged map: disk results as base
	merged := make(map[string][]byte)
	for _, kv := range diskResults {
		merged[string(kv[0])] = kv[1]
	}

	// 3. Overlay flushing cache + memory cache
	ldb.mu.RLock()
	if ldb.flushingCache != nil {
		ldb.flushingCache.Range(func(kStr string, vBytes []byte) bool {
			if len(kStr) >= len(prefixStr) && kStr[:len(prefixStr)] == prefixStr {
				strippedKey := kStr[len(prefixStr):]
				if vBytes == nil {
					delete(merged, strippedKey)
				} else {
					merged[strippedKey] = vBytes
				}
			}
			return true
		})
	}

	ldb.memoryCache.Range(func(kStr string, vBytes []byte) bool {
		if len(kStr) >= len(prefixStr) && kStr[:len(prefixStr)] == prefixStr {
			strippedKey := kStr[len(prefixStr):]
			if vBytes == nil {
				delete(merged, strippedKey)
			} else {
				merged[strippedKey] = vBytes
			}
		}
		return true
	})
	ldb.mu.RUnlock()

	// 4. Convert map to slice and sort keys for determinism
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	results := make([][2][]byte, 0, len(merged))
	for _, k := range keys {
		results = append(results, [2][]byte{[]byte(k), merged[k]})
	}
	return results, nil
}

func (ldb *LazyLevelDB) Close() error {
	ldb.mu.Lock()
	if ldb.isClosed {
		ldb.mu.Unlock()
		return nil
	}
	ldb.isClosed = true
	ldb.mu.Unlock()

	// Signal flusher to stop and perform final flush
	if ldb.flushTicker != nil {
		ldb.flushTicker.Stop()
	}
	close(ldb.closeChan)

	// Wait for background flusher to finish its final flush
	ldb.wg.Wait()

	// Close underlying database
	return ldb.db.Close()
}

// Flush synchronously writes all buffered records from memory to disk.
func (ldb *LazyLevelDB) Flush() error {
	ldb.flushToDisk()
	return nil
}

// Checkpoint flushes memory buffers and creates an atomic snapshot using hardlinks.
// This natively supports LevelDB checkpoints mirroring PebbleDB behavior.
func (ldb *LazyLevelDB) Checkpoint(destDir string) error {
	// Step 1: Flush Go-level cache to LevelDB
	if err := ldb.Flush(); err != nil {
		return fmt.Errorf("leveldb flush before checkpoint failed: %w", err)
	}
	// Step 2: Create atomic checkpoint (hardlinks SST + copies WAL/MANIFEST)
	return ldb.db.Checkpoint(destDir)
}
