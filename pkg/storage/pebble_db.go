package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// PebbleDB implements ShardDBInterface using CockroachDB's Pebble engine.
// Pebble provides better concurrent read performance, lower write amplification,
// and is the storage engine used by Ethereum's go-ethereum since v1.14.
//
// FORK-SAFETY: This is a storage-layer-only change. It does not affect:
//   - Trie hash computation (deterministic, storage-agnostic)
//   - Account state serialization/deserialization (identical bytes)
//   - Block creation logic or consensus
//
// All nodes using PebbleDB will produce identical stateRoots as LevelDB
// because the Merkle Patricia Trie operates on in-memory nodes and only
// uses the storage backend for persistence.
type PebbleDB struct {
	db   *pebble.DB
	path string
	mu   sync.RWMutex
}

// sharedPebbleCache is a global block cache shared by all PebbleDB instances.
// Sharing a single large cache is more effective than multiple small caches:
//   - Each LRU cache only evicts entries from its own pool
//   - A shared 1GB cache allows the account trie nodes to reuse capacity from
//     mapping, stake, and tx DBs when those are idle
//
// Pebble ref-counts the cache internally and cleans up on last Close().
var (
	sharedPebbleCacheOnce sync.Once
	sharedPebbleCache     *pebble.Cache
)

func getSharedPebbleCache() *pebble.Cache {
	sharedPebbleCacheOnce.Do(func() {
		// 1GB shared block cache across all PebbleDB instances.
		// With 4 nodes × 4 DBs per node, each DB gets ~64MB effective cache
		// but reads that warm one DB benefit all others via the shared LRU.
		// The server has 157GB RAM so 1GB is a negligible overhead.
		sharedPebbleCache = pebble.NewCache(4 << 30) // 4GB
		logger.Info("✅ [PEBBLE] Created 4GB shared block cache")
	})
	sharedPebbleCache.Ref()
	return sharedPebbleCache
}

// NewPebbleDB creates a new PebbleDB instance (not yet opened).
func NewPebbleDB(path string) *PebbleDB {
	return &PebbleDB{path: path}
}

// Open initializes the Pebble database with optimized settings for blockchain workloads.
// parallelism parameter controls the max concurrent compactions.
func (p *PebbleDB) Open(parallelism int) error {
	if err := createDirIfNotExists(p.path); err != nil {
		return fmt.Errorf("pebble: failed to create directory %s: %w", p.path, err)
	}

	if parallelism < 1 {
		parallelism = 2
	}

	dbPath := p.path
	opts := &pebble.Options{
		// ── Memory & Cache ──────────────────────────────────────────
		// 256MB memtable — fewer flushes under sustained write load
		MemTableSize: 256 << 20,
		// Block cache: use a process-wide shared 4GB cache for better cross-DB
		// cache utilization.
		Cache: getSharedPebbleCache(),
		// Up to 4 memtables before stalling (allows more write buffering)
		MemTableStopWritesThreshold: 4,

		// Compaction ─────────────────────────────────────────────
		// Min 4 concurrent compactions — keep compactions fast even with low parallelism
		MaxConcurrentCompactions: func() int {
			if parallelism < 4 {
				return 4
			}
			return parallelism
		},
		// L0 compaction threshold — allow more L0 files before triggering compaction
		L0CompactionThreshold: 12,
		// L0 stop writes threshold — higher tolerance before stalling writes
		L0StopWritesThreshold: 32,
		// Larger base level reduces compaction frequency under sustained writes
		LBaseMaxBytes: 512 << 20, // 512MB base level

		// ── Bloom Filters ──────────────────────────────────────────
		// 10-bit bloom filter per table — reduces point lookup I/O
		Levels: []pebble.LevelOptions{
			{FilterPolicy: bloom.FilterPolicy(10), TargetFileSize: 16 << 20},
			{FilterPolicy: bloom.FilterPolicy(10), TargetFileSize: 32 << 20},
			{FilterPolicy: bloom.FilterPolicy(10), TargetFileSize: 64 << 20},
			{FilterPolicy: bloom.FilterPolicy(10), TargetFileSize: 128 << 20},
			{FilterPolicy: bloom.FilterPolicy(10), TargetFileSize: 256 << 20},
			{FilterPolicy: bloom.FilterPolicy(10), TargetFileSize: 512 << 20},
			{FilterPolicy: bloom.FilterPolicy(10), TargetFileSize: 512 << 20},
		},

		// NOTE: WALBytesPerSync and BytesPerSync left at default (0 = disabled).
		// Forced periodic fsync caused checksum mismatch errors during compaction.
		// Crash safety relies on: (1) graceful shutdown with 15s timeout,
		// (2) repairCorruptSSTFiles() on startup, (3) peer sync for recovery.

		// ── Background Error Handler ────────────────────────────────
		// ── Table Cache ─────────────────────────────────────────────
		// Limit max open files per database to control table cache goroutines.
		// With 44+ PebbleDB instances (sharded), the default (unlimited) creates
		// 4000+ goroutines in tableCacheShard.releaseLoop, causing scheduling overhead.
		// 500 per DB × 44 DBs = 22K max open files (well within OS ulimit 100K).
		MaxOpenFiles: 500,

		EventListener: &pebble.EventListener{
			BackgroundError: func(err error) {
				logger.Error("🔴 [PEBBLE] Background error in %s: %v", dbPath, err)
			},
			TableDeleted: func(info pebble.TableDeleteInfo) {
				logger.Debug("[PEBBLE] Table deleted in %s: %v", dbPath, info)
			},
		},
	}

	var err error
	var maxRetries = 5
	for i := 0; i < maxRetries; i++ {
		p.db, err = pebble.Open(p.path, opts)
		if err == nil {
			break
		}
		logger.Error("PebbleDB: Failed to open (attempt %d/%d): %s, error: %v", i+1, maxRetries, p.path, err)

		// ── CORRUPTION RECOVERY (Mar 2026) ──────────────────────────
		// If open fails with table errors ("invalid table", "file size is too small"),
		// try to remove corrupt SST files and retry. PebbleDB can rebuild from WAL.
		errStr := err.Error()
		if strings.Contains(errStr, "invalid table") ||
			strings.Contains(errStr, "file size is too small") ||
			strings.Contains(errStr, "backing file") {
			logger.Warn("🔧 [PEBBLE] Attempting corruption recovery for %s...", p.path)
			repairCorruptSSTFiles(p.path)
		}

		time.Sleep(time.Duration(1000*(i+1)) * time.Millisecond)
	}

	if err != nil {
		return fmt.Errorf("pebble: failed to open %s after %d retries: %w", p.path, maxRetries, err)
	}

	logger.Info("✅ [PEBBLE] Opened database: %s (parallelism=%d)", p.path, parallelism)
	return nil
}

// Get retrieves a value by key. Returns error if key not found (matching LevelDB convention).
func (p *PebbleDB) Get(key []byte) ([]byte, error) {
	value, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			// CRITICAL: Must return error (not nil) to match LevelDB behavior.
			// The entire codebase checks `err != nil` from Get() to detect missing keys.
			return nil, fmt.Errorf("pebble: not found")
		}
		return nil, err
	}
	// IMPORTANT: Must copy value before closing — Pebble reuses internal buffers
	result := make([]byte, len(value))
	copy(result, value)
	closer.Close()
	return result, nil
}

// Put stores a key-value pair.
func (p *PebbleDB) Put(key, value []byte) error {
	return p.db.Set(key, value, pebble.NoSync)
}

// Delete removes a key from the database.
func (p *PebbleDB) Delete(key []byte) error {
	return p.db.Delete(key, pebble.NoSync)
}

// BatchPut writes multiple key-value pairs in a single atomic batch.
// Uses Pebble's optimized batch which is faster than LevelDB's batch:
// - No WAL contention (Pebble has parallel WAL writes)
// - Batch is applied to memtable without holding global lock
func (p *PebbleDB) BatchPut(kvs [][2][]byte) error {
	batch := p.db.NewBatch()
	defer batch.Close()

	for _, kv := range kvs {
		if err := batch.Set(kv[0], kv[1], nil); err != nil {
			return fmt.Errorf("pebble batch set error: %w", err)
		}
	}

	// NoSync for maximum throughput — crash recovery handled by blockchain replay
	return batch.Commit(pebble.NoSync)
}

// PrefixScan iterates all keys with the given prefix and returns key-value pairs.
// Keys in results have the prefix stripped.
func (p *PebbleDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	if p.db == nil {
		return nil, fmt.Errorf("pebble: database not opened")
	}

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("pebble: failed to create iterator: %w", err)
	}
	defer iter.Close()

	var results [][2][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		key := make([]byte, len(iter.Key())-len(prefix))
		copy(key, iter.Key()[len(prefix):])
		value := make([]byte, len(iter.Value()))
		copy(value, iter.Value())
		results = append(results, [2][]byte{key, value})
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("pebble: iterator error: %w", err)
	}
	return results, nil
}

// prefixUpperBound computes the upper bound for prefix iteration.
// For prefix "fs:", returns "fs;" (next byte after ':').
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil // prefix was all 0xFF — no upper bound
}

// Close closes the Pebble database, flushing all pending writes.
func (p *PebbleDB) Close() error {
	if p.db == nil {
		return nil
	}

	// Flush memtable to SST files
	if err := p.db.Flush(); err != nil {
		logger.Error("PebbleDB: Flush error before close: %v", err)
	}

	err := p.db.Close()
	if err != nil {
		logger.Error("PebbleDB: Close error: %v", err)
		return err
	}
	p.db = nil
	logger.Info("🛑 [PEBBLE] Closed database: %s", p.path)
	return nil
}

// Flush forces the PebbleDB memtable to flush to disk (SST files).
// NOTE: Does NOT call Sync — that is only done during Close() for shutdown safety.
// The periodic flusher calls this every 5s across ~32 shards, so Sync here would
// cause excessive I/O and potential checksum mismatch issues.
func (p *PebbleDB) Flush() error {
	if p.db != nil {
		return p.db.Flush()
	}
	return nil
}

// Checkpoint creates an atomic, consistent snapshot of the database at destDir.
// This uses Pebble's native checkpoint which hardlinks SST files and copies
// the MANIFEST and WAL atomically — safe for concurrent reads/writes.
// This is the CORRECT way to snapshot PebbleDB (instead of manual hardlink copy).
func (p *PebbleDB) Checkpoint(destDir string) error {
	if p.db == nil {
		return fmt.Errorf("pebble: database not opened")
	}
	logger.Info("📸 [PEBBLE] Creating checkpoint: %s → %s", p.path, destDir)
	return p.db.Checkpoint(destDir)
}

// ═══════════════════════════════════════════════════════════════════════
// LazyPebbleDB — Memory-buffered wrapper (same pattern as LazyLevelDB)
// ═══════════════════════════════════════════════════════════════════════

// LazyPebbleDB wraps PebbleDB with the same memory-buffering strategy
// as LazyLevelDB: buffers writes in a sync.Map and periodically flushes
// to disk. This keeps the exact same write behavior for the blockchain.
type LazyPebbleDB struct {
	db            *PebbleDB
	memoryCache   *ShardedMap
	flushingCache *ShardedMap
	flushTicker   *time.Ticker
	closeChan     chan struct{}
	wg            sync.WaitGroup
	isClosed      bool
	mu            sync.RWMutex
	flushCounter  int
}

// NewLazyPebbleDB creates a new LazyPebbleDB with memory buffering.
func NewLazyPebbleDB(path string) *LazyPebbleDB {
	return &LazyPebbleDB{
		db:          NewPebbleDB(path),
		memoryCache: NewShardedMap(),
		closeChan:   make(chan struct{}),
	}
}

// Open initializes the underlying PebbleDB and starts the background flusher.
func (lp *LazyPebbleDB) Open(parallelism int) error {
	if err := lp.db.Open(parallelism); err != nil {
		return err
	}

	lp.flushTicker = time.NewTicker(FlushInterval)
	lp.wg.Add(1)
	go lp.backgroundFlusher()

	logger.Info("✅ [LAZY PEBBLE] Opened database with Async Memory Cache at: %s", lp.db.path)
	return nil
}

func (lp *LazyPebbleDB) backgroundFlusher() {
	defer lp.wg.Done()
	for {
		select {
		case <-lp.flushTicker.C:
			lp.flushToDisk()
		case <-lp.closeChan:
			logger.Info("🛑 [LAZY PEBBLE] Stopping background flusher for %s", lp.db.path)
			lp.flushToDisk()
			return
		}
	}
}

func (lp *LazyPebbleDB) flushToDisk() {
	lp.mu.Lock()
	if lp.isClosed {
		lp.mu.Unlock()
		return
	}

	oldCache := lp.memoryCache
	lp.flushingCache = oldCache
	lp.memoryCache = NewShardedMap()
	lp.mu.Unlock()

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
		return true
	})

	if count == 0 {
		lp.mu.Lock()
		lp.flushingCache = nil
		lp.mu.Unlock()
		return
	}

	for _, key := range deletedKeys {
		if err := lp.db.Delete(key); err != nil {
			logger.Error("❌ [LAZY PEBBLE FLUSH] Failed to delete key: %v", err)
		}
	}

	if len(batch) > 0 {
		lp.flushCounter++
		// start := time.Now()
		// Flush in chunks of 10K entries to prevent large batch storms
		// that stall PebbleDB compaction and increase tail latency.
		const chunkSize = 10000
		for i := 0; i < len(batch); i += chunkSize {
			end := i + chunkSize
			if end > len(batch) {
				end = len(batch)
			}
			if err := lp.db.BatchPut(batch[i:end]); err != nil {
				logger.Error("❌ [LAZY PEBBLE FLUSH] Failed to BatchPut chunk: %v", err)
			}
		}
		// logger.Info("💾 [LAZY PEBBLE FLUSH #%d] Flushed %d records (%d chunks) to disk in %v (%s)",
		// 	lp.flushCounter, len(batch), (len(batch)+chunkSize-1)/chunkSize, time.Since(start), lp.db.path)
	}

	lp.mu.Lock()
	lp.flushingCache = nil
	lp.mu.Unlock()
}

// Get checks memory cache → flushing cache → PebbleDB
func (lp *LazyPebbleDB) Get(key []byte) ([]byte, error) {
	keyStr := string(key)

	lp.mu.RLock()
	val, ok := lp.memoryCache.Load(keyStr)
	var flushVal []byte
	var flushOk bool
	if !ok && lp.flushingCache != nil {
		flushVal, flushOk = lp.flushingCache.Load(keyStr)
	}
	lp.mu.RUnlock()

	if ok {
		if val == nil {
			return nil, fmt.Errorf("pebble: not found (tombstoned in cache)")
		}
		return val, nil
	}
	if flushOk {
		if flushVal == nil {
			return nil, fmt.Errorf("pebble: not found (tombstoned in cache)")
		}
		return flushVal, nil
	}
	return lp.db.Get(key)
}

// Put buffers the write in memory.
func (lp *LazyPebbleDB) Put(key, value []byte) error {
	lp.mu.RLock()
	defer lp.mu.RUnlock()
	if lp.isClosed {
		return fmt.Errorf("database is closed")
	}
	valCopy := make([]byte, len(value))
	copy(valCopy, value)
	lp.memoryCache.Store(string(key), valCopy)
	return nil
}

// Delete buffers the delete as a tombstone.
func (lp *LazyPebbleDB) Delete(key []byte) error {
	lp.mu.RLock()
	defer lp.mu.RUnlock()
	if lp.isClosed {
		return fmt.Errorf("database is closed")
	}
	lp.memoryCache.Store(string(key), nil)
	return nil
}

// BatchPut buffers multiple writes in memory.
func (lp *LazyPebbleDB) BatchPut(kvs [][2][]byte) error {
	lp.mu.RLock()
	defer lp.mu.RUnlock()
	if lp.isClosed {
		return fmt.Errorf("database is closed")
	}
	for _, kv := range kvs {
		valCopy := make([]byte, len(kv[1]))
		copy(valCopy, kv[1])
		lp.memoryCache.Store(string(kv[0]), valCopy)
	}
	return nil
}

// PrefixScan merges results from memory cache, flushing cache, and disk.
// Returns key-value pairs with prefix stripped from keys.
func (lp *LazyPebbleDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	prefixStr := string(prefix)

	// 1. Scan disk first
	diskResults, err := lp.db.PrefixScan(prefix)
	if err != nil {
		return nil, err
	}

	// 2. Build merged map: disk results as base
	merged := make(map[string][]byte)
	for _, kv := range diskResults {
		merged[string(kv[0])] = kv[1]
	}

	// 3. Overlay flushing cache
	lp.mu.RLock()
	if lp.flushingCache != nil {
		lp.flushingCache.Range(func(kStr string, vBytes []byte) bool {
			if len(kStr) >= len(prefixStr) && kStr[:len(prefixStr)] == prefixStr {
				strippedKey := kStr[len(prefixStr):]
				if vBytes == nil {
					delete(merged, strippedKey) // tombstone
				} else {
					merged[strippedKey] = vBytes
				}
			}
			return true
		})
	}

	// 4. Overlay memory cache (most recent writes)
	lp.memoryCache.Range(func(kStr string, vBytes []byte) bool {
		if len(kStr) >= len(prefixStr) && kStr[:len(prefixStr)] == prefixStr {
			strippedKey := kStr[len(prefixStr):]
			if vBytes == nil {
				delete(merged, strippedKey) // tombstone
			} else {
				merged[strippedKey] = vBytes
			}
		}
		return true
	})
	lp.mu.RUnlock()

	// 5. Convert map to slice and sort keys for determinism
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

// Close flushes and closes the database.
func (lp *LazyPebbleDB) Close() error {
	lp.mu.Lock()
	if lp.isClosed {
		lp.mu.Unlock()
		return nil
	}
	lp.isClosed = true
	lp.mu.Unlock()

	if lp.flushTicker != nil {
		lp.flushTicker.Stop()
	}
	close(lp.closeChan)
	lp.wg.Wait()

	return lp.db.Close()
}

// Flush synchronously flushes ALL pending data to durable storage:
//  1. Go-level memory cache → PebbleDB memtable (via flushToDisk)
//  2. PebbleDB memtable → SST files on disk (via db.Flush)
//
// CRASH SAFETY (Mar 2026 fix): Previously this only did step 1 and relied on
// PebbleDB's internal memtable flush threshold (128MB). This meant data could
// exist only in WAL, which can be lost/corrupt on SIGKILL. The lastBlockHashKey
// (critical for restart) was particularly vulnerable — its loss caused the system
// to re-initialize genesis, wiping all state.
//
// The periodic flush interval has been increased to 10s (from 5s) to compensate
// for the additional I/O from the full flush.
func (lp *LazyPebbleDB) Flush() error {
	lp.flushToDisk()
	// CRITICAL: Also flush PebbleDB memtable → SST files on disk
	// Without this, data only exists in WAL which can be lost on crash
	if lp.db != nil && lp.db.db != nil {
		if err := lp.db.Flush(); err != nil {
			logger.Error("❌ [LAZY PEBBLE] Failed to flush PebbleDB memtable→SST: %v (%s)", err, lp.db.path)
			return err
		}
	}
	return nil
}

// Checkpoint creates an atomic snapshot: flush memory cache → PebbleDB → SST → checkpoint.
// The checkpoint is a consistent point-in-time snapshot safe for concurrent writes.
// CRITICAL: We must call db.Flush() to force memtable→SST compaction before checkpoint.
// Without this, data in PebbleDB memtable may not be fully captured in WAL-only checkpoints,
// resulting in data loss on restore (e.g. missing epoch data, wrong block height).
func (lp *LazyPebbleDB) Checkpoint(destDir string) error {
	// Step 1: Flush Go-level cache to PebbleDB memtable
	lp.flushToDisk()
	// Step 2: Force PebbleDB memtable → SST (ensures ALL data is on disk)
	if err := lp.db.Flush(); err != nil {
		return fmt.Errorf("pebble flush before checkpoint failed: %w", err)
	}
	// Step 3: Create atomic checkpoint (hardlinks SST + copies WAL/MANIFEST)
	return lp.db.Checkpoint(destDir)
}

// ═══════════════════════════════════════════════════════════════════════
// createDirIfNotExists is a helper (already exists in replicated_leveldb.go)
// but we reference it here to avoid import cycle issues.
// ═══════════════════════════════════════════════════════════════════════

func createPebbleDirIfNotExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.MkdirAll(path, os.ModePerm)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════
// repairCorruptSSTFiles removes ALL SST files from a corrupt database directory.
// PebbleDB can rebuild from WAL on the next Open() attempt.
//
// This handles the scenario where a process crash (SIGKILL) during flush leaves
// SST files with size mismatch vs MANIFEST (e.g., 1084 bytes on disk vs 1083 in MANIFEST).
// Since we cannot reliably determine which SSTs are corrupt without parsing MANIFEST,
// we remove all SSTs and let PebbleDB rebuild from WAL.
// ═══════════════════════════════════════════════════════════════════════
func repairCorruptSSTFiles(dbPath string) {
	entries, err := os.ReadDir(dbPath)
	if err != nil {
		logger.Error("🔧 [PEBBLE REPAIR] Failed to read directory %s: %v", dbPath, err)
		return
	}

	sstFiles := 0
	removedFiles := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sst") {
			continue
		}
		sstFiles++

		fullPath := filepath.Join(dbPath, name)
		corruptPath := fullPath + ".corrupt"
		if renameErr := os.Rename(fullPath, corruptPath); renameErr != nil {
			logger.Warn("🔧 [PEBBLE REPAIR] Rename failed, deleting SST: %s", name)
			os.Remove(fullPath)
		} else {
			logger.Warn("🔧 [PEBBLE REPAIR] Moved SST to %s", corruptPath)
		}
		removedFiles++
	}

	// Also remove the MANIFEST since it references the old SSTs
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "MANIFEST-") {
			manifestPath := filepath.Join(dbPath, entry.Name())
			logger.Warn("🔧 [PEBBLE REPAIR] Removing stale MANIFEST: %s", entry.Name())
			os.Remove(manifestPath)
		}
	}

	if removedFiles > 0 {
		logger.Info("🔧 [PEBBLE REPAIR] Removed %d/%d SST files + MANIFEST from %s. PebbleDB will rebuild from WAL.", removedFiles, sstFiles, dbPath)
	} else {
		logger.Info("🔧 [PEBBLE REPAIR] No SST files found in %s. Error may be in WAL.", dbPath)
	}
}
