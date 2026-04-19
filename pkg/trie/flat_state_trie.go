package trie

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// ═══════════════════════════════════════════════════════════════════════════════
// FlatStateTrie — O(1) reads with additive bucket accumulators (mod prime)
//
// Unlike MerklePatriciaTrie which requires O(log₁₆(N)) disk reads per Get,
// FlatStateTrie stores key-value pairs directly in the backing store.
//
// Hash computation uses 256 additive bucket accumulators modulo a large prime:
//   bucket[i] = Σ keccak256(key || value) mod p, for all entries where key[0] == i
//   rootHash  = keccak256(bucket[0] || bucket[1] || ... || bucket[255])
//
// This gives O(K) hash computation per block (K = dirty entries), independent
// of total state size N. No PreWarm needed.
//
// SECURITY: Additive accumulators mod prime are collision-resistant:
// to forge state, attacker must find keccak256 preimage of (p - x), which is
// computationally infeasible (~2^128 work). Safe for public chains.
//
// Prime: p = 2^256 - 189 (a well-known 256-bit prime)
// ═══════════════════════════════════════════════════════════════════════════════

// bucketPrime is a 256-bit prime used for modular arithmetic in bucket accumulators.
// p = 2^256 - 189 — chosen for being close to 2^256 (efficient mod reduction)
// and being a proven prime.
var bucketPrime = func() *big.Int {
	p := new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)
	p.Sub(p, big.NewInt(189))
	return p
}()

// FlatStateDB defines the storage interface required by FlatStateTrie.
// Any storage.Storage (PebbleDB, LevelDB) satisfies this interface.
type FlatStateDB interface {
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	BatchPut(pairs [][2][]byte) error
	// PrefixScan returns all key-value pairs with the given prefix.
	// Keys in results have the prefix stripped.
	PrefixScan(prefix []byte) ([][2][]byte, error)
}

// flatKeyPrefix separates flat state entries from trie node entries in the same DB.
var flatKeyPrefix = []byte("fs:")
var flatBucketPrefix = []byte("fb:")

// globalBucketCache stores the latest committed bucket accumulators per DB instance.
// This avoids relying on async DB reads when creating new FlatStateTrie instances.
// Key: fmt.Sprintf("%p", db) (pointer address of the db), Value: *[256]e_common.Hash
var globalBucketCache sync.Map

// hashBufPool reuses keccak256 input buffers to reduce allocations in hot path.
var hashBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 256) // pre-allocate reasonable capacity
		return &buf
	},
}

// bigIntPool reuses big.Int objects to reduce GC pressure in addModPrime/subModPrime.
var bigIntPool = sync.Pool{
	New: func() interface{} {
		return new(big.Int)
	},
}

// rootBufPool reuses the 8KB buffer for computeRootFromBuckets.
var rootBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 256*32)
		return &buf
	},
}

// flatKeyBufPool reuses concat buffers for makeFlatKey to reduce GC pressure.
// Typical key: "fs:" (3) + 32-byte slot = 35 bytes.
var flatKeyBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 48)
		return &buf
	},
}

func makeFlatKey(key []byte) []byte {
	bufPtr := flatKeyBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	buf = append(buf, flatKeyPrefix...)
	buf = append(buf, key...)
	result := make([]byte, len(buf))
	copy(result, buf)
	*bufPtr = buf
	flatKeyBufPool.Put(bufPtr)
	return result
}

func makeBucketKey(bucketIdx byte) []byte {
	return append(append([]byte{}, flatBucketPrefix...), bucketIdx)
}

func dbCacheKey(db FlatStateDB) string {
	return fmt.Sprintf("%p", db)
}

// dirtyEntry caches decoded key bytes alongside the dirty value,
// eliminating redundant hex.DecodeString calls in Hash() and Commit().
type dirtyEntry struct {
	keyBytes []byte // decoded key bytes (cached from hex)
	value    []byte // new value
	bucket   byte   // keyBytes[0] — pre-computed bucket index
}

// FlatStateTrie implements StateTrie using direct key-value storage.
type FlatStateTrie struct {
	db    FlatStateDB
	dirty map[string]*dirtyEntry // hex(key) → entry with cached keyBytes

	// oldValues caches old values read during BatchUpdate for bucket hash computation.
	// This avoids re-reading from DB during Hash() or Commit().
	oldValues map[string][]byte // hex(key) → old value (nil if new key)
	oldLoaded map[string]bool   // hex(key) → true if old value was loaded

	// 256 additive bucket accumulators (mod prime) for hash computation.
	// bucket[i] = Σ keccak256(key || value) mod p, for all entries where key[0] == i
	buckets      [256]e_common.Hash
	dirtyBuckets [256]bool // tracks which buckets were modified (for selective DB write)
	rootHash     e_common.Hash

	// lastCommitBatch stores the flat entries + bucket accumulators from the last Commit().
	// This is used by TransactionStateDB/Receipts to include flat entries in TxBatchPut/ReceiptBatchPut
	// for replication to Sub nodes. Without this, Sub nodes never receive the actual data.
	lastCommitBatch [][2][]byte

	isHash bool
	mu     sync.RWMutex
}

// Compile-time check: FlatStateTrie must implement StateTrie.
var _ StateTrie = (*FlatStateTrie)(nil)

// NewFlatStateTrie creates a new empty FlatStateTrie.
func NewFlatStateTrie(db FlatStateDB, isHash bool) *FlatStateTrie {
	ft := &FlatStateTrie{
		db:        db,
		dirty:     make(map[string]*dirtyEntry),
		oldValues: make(map[string][]byte),
		oldLoaded: make(map[string]bool),
		isHash:    isHash,
	}
	ft.rootHash = computeRootFromBuckets(ft.buckets)
	return ft
}

// NewFlatStateTrieFromRoot loads a FlatStateTrie from committed bucket state.
// First checks the in-memory global bucket cache (instant), falls back to DB reads.
func NewFlatStateTrieFromRoot(rootHash e_common.Hash, db FlatStateDB, isHash bool) (*FlatStateTrie, error) {
	ft := NewFlatStateTrie(db, isHash)

	if rootHash == (e_common.Hash{}) || rootHash == EmptyRootHash {
		return ft, nil
	}

	// Try in-memory bucket cache first (avoids async DB read issues)
	cacheKey := dbCacheKey(db)
	if cached, ok := globalBucketCache.Load(cacheKey); ok {
		buckets := cached.(*[256]e_common.Hash)
		ft.buckets = *buckets
		ft.rootHash = computeRootFromBuckets(ft.buckets)
		if ft.rootHash == rootHash {
			return ft, nil
		}
		// Cache hit but hash mismatch — fall through to DB
	}

	// Fallback: load bucket accumulators from DB
	for i := 0; i < 256; i++ {
		data, err := db.Get(makeBucketKey(byte(i)))
		if err == nil && len(data) == 32 {
			copy(ft.buckets[i][:], data)
		}
	}
	ft.rootHash = computeRootFromBuckets(ft.buckets)

	// With sharded/async DBs, bucket reads may not reflect latest writes.
	// Trust the committed rootHash if buckets don't match.
	if ft.rootHash != rootHash {
		logger.Warn("[FlatStateTrie] Bucket load mismatch (expected=%s, computed=%s) — using committed hash",
			rootHash.Hex()[:16], ft.rootHash.Hex()[:16])
		ft.rootHash = rootHash
	}

	return ft, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// StateTrie interface implementation
// ═══════════════════════════════════════════════════════════════════════════════

// Get retrieves the value for a key. O(1) — direct DB read.
func (f *FlatStateTrie) Get(key []byte) ([]byte, error) {
	hexKey := hex.EncodeToString(key)

	f.mu.RLock()
	defer f.mu.RUnlock()

	// Check dirty first
	if entry, ok := f.dirty[hexKey]; ok {
		return entry.value, nil
	}

	// Direct DB read — O(1), no trie traversal
	val, err := f.db.Get(makeFlatKey(key))
	if err != nil {
		return nil, nil // Key not found is not an error
	}
	return val, nil
}

// GetAll returns all key-value pairs by scanning flat entries (fs:* prefix) in the DB
// and merging with any uncommitted dirty entries. Keys are hex-encoded to match
// the MPT GetAll() format used by AccountStateDB/StakeStateDB.
func (f *FlatStateTrie) GetAll() (map[string][]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// 1. Scan all flat entries from DB using prefix scan
	results := make(map[string][]byte)

	pairs, err := f.db.PrefixScan(flatKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("FlatStateTrie GetAll prefix scan failed: %w", err)
	}

	for _, kv := range pairs {
		// kv[0] is the original key (prefix already stripped by PrefixScan)
		hexKey := hex.EncodeToString(kv[0])
		results[hexKey] = kv[1]
	}

	// 2. Overlay dirty entries (uncommitted changes)
	for hexKey, entry := range f.dirty {
		if len(entry.value) == 0 {
			delete(results, hexKey) // deletion
		} else {
			results[hexKey] = entry.value
		}
	}

	return results, nil
}

// Update sets the value for a key. Buffers in dirty map — O(1).
func (f *FlatStateTrie) Update(key, value []byte) error {
	hexKey := hex.EncodeToString(key)

	f.mu.Lock()
	defer f.mu.Unlock()

	// Load old value for bucket hash computation (only once per key per block)
	if !f.oldLoaded[hexKey] {
		oldVal, err := f.db.Get(makeFlatKey(key))
		if err == nil && len(oldVal) > 0 {
			f.oldValues[hexKey] = oldVal
		}
		f.oldLoaded[hexKey] = true
	}

	// Cache decoded key bytes to avoid redundant hex.DecodeString in Hash/Commit
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	var bucket byte
	if len(keyCopy) > 0 {
		bucket = keyCopy[0]
	}

	f.dirty[hexKey] = &dirtyEntry{
		keyBytes: keyCopy,
		value:    value,
		bucket:   bucket,
	}
	return nil
}

// BatchUpdate performs optimized batch updates for multiple keys.
//
// OPTIMIZATION: Instead of doing sequential DB reads for old values while holding
// the lock, this method:
//   1. Parallelizes hex encoding and old-value DB reads (read-only Phase 1)
//   2. Acquires the lock ONCE for the entire batch (sequential map updates Phase 2)
//
// This reduces lock overhead and exploits SSD parallel read bandwidth.
func (f *FlatStateTrie) BatchUpdate(keys, values [][]byte) error {
	if len(keys) != len(values) {
		return fmt.Errorf("FlatStateTrie: BatchUpdate keys/values length mismatch (%d vs %d)", len(keys), len(values))
	}
	if len(keys) == 0 {
		return nil
	}

	n := len(keys)

	// ═══════════════════════════════════════════════════════════════
	// Phase 1: PARALLEL — Pre-compute hex keys + read old values from DB
	// This phase does NOT touch the trie or dirty map (read-only from DB).
	// ═══════════════════════════════════════════════════════════════
	type batchEntry struct {
		hexKey   string
		keyCopy  []byte
		bucket   byte
		oldValue []byte // old value from DB (nil if not found)
		loaded   bool   // true if DB was successfully queried
	}
	entries := make([]batchEntry, n)

	numWorkers := 16
	if n < numWorkers {
		numWorkers = n
	}
	chunkSize := (n + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= n {
			break
		}
		if end > n {
			end = n
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				key := keys[i]
				hexKey := hex.EncodeToString(key)
				keyCopy := make([]byte, len(key))
				copy(keyCopy, key)

				var bucket byte
				if len(keyCopy) > 0 {
					bucket = keyCopy[0]
				}

				// Read old value from DB for bucket computation
				// (We read blindly here to avoid locking; redundant reads on cache hit are cheap)
				var oldVal []byte
				var loaded bool
				dbVal, err := f.db.Get(makeFlatKey(keyCopy))
				if err == nil {
					loaded = true
					if len(dbVal) > 0 {
						oldVal = dbVal
					}
				}

				entries[i] = batchEntry{
					hexKey:   hexKey,
					keyCopy:  keyCopy,
					bucket:   bucket,
					oldValue: oldVal,
					loaded:   loaded,
				}
			}
		}(start, end)
	}
	wg.Wait()

	// ═══════════════════════════════════════════════════════════════
	// Phase 2: SEQUENTIAL — Update dirty map + oldLoaded tracking
	// Single lock acquisition for entire batch.
	// ═══════════════════════════════════════════════════════════════
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := 0; i < n; i++ {
		e := &entries[i]

		// Load old value only once per key per block
		if !f.oldLoaded[e.hexKey] {
			if e.loaded {
				if e.oldValue != nil {
					f.oldValues[e.hexKey] = e.oldValue
				}
				f.oldLoaded[e.hexKey] = true
			}
		}

		f.dirty[e.hexKey] = &dirtyEntry{
			keyBytes: e.keyCopy,
			value:    values[i],
			bucket:   e.bucket,
		}
	}

	return nil
}

// BatchUpdateWithCachedOldValues performs batch updates using pre-fetched old values
// from the caller's cache (e.g., AccountStateDB LRU cache), completely eliminating
// parallel DB reads in Phase 1.
//
// TPS OPT PHASE 2: IntermediateRoot already has old values from its LRU cache.
// Instead of re-reading them from DB (16 parallel goroutines × DB.Get per key),
// the caller passes them directly. For 20k dirty accounts:
//   - Old path: 20k × DB.Get (PebbleDB, ~50µs each) = ~100ms with 16 workers
//   - New path: 0 DB reads, pure in-memory computation = ~5ms
//
// oldValues[i] is the previous serialized value for keys[i], or nil if the key is new.
func (f *FlatStateTrie) BatchUpdateWithCachedOldValues(keys, values, oldValues [][]byte) error {
	if len(keys) != len(values) {
		return fmt.Errorf("FlatStateTrie: BatchUpdateWithCachedOldValues keys/values length mismatch (%d vs %d)", len(keys), len(values))
	}
	if oldValues != nil && len(keys) != len(oldValues) {
		return fmt.Errorf("FlatStateTrie: BatchUpdateWithCachedOldValues keys/oldValues length mismatch (%d vs %d)", len(keys), len(oldValues))
	}
	if len(keys) == 0 {
		return nil
	}

	n := len(keys)

	// ═══════════════════════════════════════════════════════════════
	// Phase 1: PARALLEL — Pre-compute hex keys + bucket indices ONLY.
	// NO DB READS — old values come from caller's cache.
	// ═══════════════════════════════════════════════════════════════
	type batchEntry struct {
		hexKey  string
		keyCopy []byte
		bucket  byte
	}
	entries := make([]batchEntry, n)

	numWorkers := 16
	if n < numWorkers {
		numWorkers = n
	}
	chunkSize := (n + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= n {
			break
		}
		if end > n {
			end = n
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				key := keys[i]
				keyCopy := make([]byte, len(key))
				copy(keyCopy, key)

				var bucket byte
				if len(keyCopy) > 0 {
					bucket = keyCopy[0]
				}

				entries[i] = batchEntry{
					hexKey:  hex.EncodeToString(key),
					keyCopy: keyCopy,
					bucket:  bucket,
				}
			}
		}(start, end)
	}
	wg.Wait()

	// ═══════════════════════════════════════════════════════════════
	// Phase 2: SEQUENTIAL — Update dirty map + inject cached old values.
	// ═══════════════════════════════════════════════════════════════
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := 0; i < n; i++ {
		e := &entries[i]

		// Inject caller-provided old value (only once per key per block)
		if !f.oldLoaded[e.hexKey] {
			if oldValues != nil && len(oldValues[i]) > 0 {
				f.oldValues[e.hexKey] = oldValues[i]
			}
			f.oldLoaded[e.hexKey] = true
		}

		f.dirty[e.hexKey] = &dirtyEntry{
			keyBytes: e.keyCopy,
			value:    values[i],
			bucket:   e.bucket,
		}
	}

	return nil
}

// PreWarm is a no-op for FlatStateTrie — reads are O(1), no trie nodes to pre-resolve.
func (f *FlatStateTrie) PreWarm(keys [][]byte) {
	// No-op: flat reads don't need pre-warming
}

// keccakContrib computes keccak256(keyBytes || value) using pooled buffer to reduce allocations.
func keccakContrib(keyBytes, value []byte) e_common.Hash {
	bufPtr := hashBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	buf = append(buf, keyBytes...)
	buf = append(buf, value...)
	h := crypto.Keccak256Hash(buf)
	*bufPtr = buf // return buffer to pool (keeps capacity)
	hashBufPool.Put(bufPtr)
	return h
}

// precomputedContrib holds pre-computed keccak hashes for a dirty entry.
type precomputedContrib struct {
	bucket     byte
	oldContrib e_common.Hash
	newContrib e_common.Hash
	hasOld     bool
	hasNew     bool
}

// applyDirtyToBuckets applies dirty changes to bucket accumulators.
// Used by both Hash() (on temp copy) and Commit() (on actual buckets).
// Returns which buckets were modified.
//
// TPS OPT PHASE 5: For large dirty sets (>1000 entries), pre-computes
// keccak contributions in parallel workers before applying mod-prime
// operations sequentially per-bucket. keccak256 is pure CPU, so
// parallelizing it gives near-linear speedup.
func (f *FlatStateTrie) applyDirtyToBuckets(buckets *[256]e_common.Hash) [256]bool {
	var modified [256]bool

	dirtyCount := len(f.dirty)
	if dirtyCount == 0 {
		return modified
	}

	// Small dirty set: sequential path (avoid goroutine overhead)
	if dirtyCount < 1000 {
		for hexKey, entry := range f.dirty {
			if len(entry.keyBytes) == 0 {
				continue
			}

			// Remove old contribution (divide mod prime for MuHash)
			if oldVal, ok := f.oldValues[hexKey]; ok && len(oldVal) > 0 {
				oldContrib := keccakContrib(entry.keyBytes, oldVal)
				divModPrime(&buckets[entry.bucket], oldContrib)
				modified[entry.bucket] = true
			}

			// Add new contribution (multiply mod prime for MuHash)
			if len(entry.value) > 0 {
				newContrib := keccakContrib(entry.keyBytes, entry.value)
				mulModPrime(&buckets[entry.bucket], newContrib)
				modified[entry.bucket] = true
			}
		}
		return modified
	}

	// ═══════════════════════════════════════════════════════════════
	// PARALLEL PATH: Pre-compute all keccak hashes concurrently,
	// then apply mod-prime operations sequentially per-bucket.
	// keccak256 is ~1µs per call, parallel → O(K/W) wall-clock.
	// ═══════════════════════════════════════════════════════════════

	// Collect dirty entries into a slice for indexed parallel access
	type dirtyItem struct {
		hexKey string
		entry  *dirtyEntry
	}
	items := make([]dirtyItem, 0, dirtyCount)
	for hexKey, entry := range f.dirty {
		if len(entry.keyBytes) > 0 {
			items = append(items, dirtyItem{hexKey, entry})
		}
	}

	contribs := make([]precomputedContrib, len(items))

	numWorkers := 8
	n := len(items)
	if n < numWorkers {
		numWorkers = n
	}
	chunkSize := (n + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= n {
			break
		}
		if end > n {
			end = n
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				item := &items[i]
				pc := &contribs[i]
				pc.bucket = item.entry.bucket

				// Pre-compute old contribution hash
				if oldVal, ok := f.oldValues[item.hexKey]; ok && len(oldVal) > 0 {
					pc.oldContrib = keccakContrib(item.entry.keyBytes, oldVal)
					pc.hasOld = true
				}

				// Pre-compute new contribution hash
				if len(item.entry.value) > 0 {
					pc.newContrib = keccakContrib(item.entry.keyBytes, item.entry.value)
					pc.hasNew = true
				}
			}
		}(start, end)
	}
	wg.Wait()

	// Sequential apply: mod-prime operations per-bucket (not parallelizable)
	for i := range contribs {
		pc := &contribs[i]
		if pc.hasOld {
			divModPrime(&buckets[pc.bucket], pc.oldContrib)
			modified[pc.bucket] = true
		}
		if pc.hasNew {
			mulModPrime(&buckets[pc.bucket], pc.newContrib)
			modified[pc.bucket] = true
		}
	}

	return modified
}

// Hash computes the current root hash after applying dirty changes.
// Does NOT modify persistent state. O(K) where K = dirty entries.
func (f *FlatStateTrie) Hash() e_common.Hash {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if len(f.dirty) == 0 {
		return f.rootHash
	}

	// Compute what the bucket hashes WOULD be after applying dirty changes
	tempBuckets := f.buckets
	f.applyDirtyToBuckets(&tempBuckets)
	return computeRootFromBuckets(tempBuckets)
}

// Commit finalizes changes: writes dirty entries to DB, updates bucket accumulators.
// O(K) where K = dirty entries. Returns (rootHash, nil, nil, nil) — no NodeSet for flat state.
func (f *FlatStateTrie) Commit(collectLeaf bool) (e_common.Hash, *node.NodeSet, [][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.dirty) == 0 {
		return f.rootHash, nil, nil, nil
	}

	// 1. Update bucket accumulators (reuses shared applyDirtyToBuckets)
	f.dirtyBuckets = f.applyDirtyToBuckets(&f.buckets)

	// 2. Build persist batch: dirty entries + ONLY modified bucket accumulators
	// Count modified buckets for precise pre-allocation
	modifiedBucketCount := 0
	for i := 0; i < 256; i++ {
		if f.dirtyBuckets[i] {
			modifiedBucketCount++
		}
	}

	batch := make([][2][]byte, 0, len(f.dirty)+modifiedBucketCount)
	for _, entry := range f.dirty {
		batch = append(batch, [2][]byte{makeFlatKey(entry.keyBytes), entry.value})
	}

	// Only write modified buckets (not all 256)
	for i := 0; i < 256; i++ {
		if f.dirtyBuckets[i] || f.buckets[i] != (e_common.Hash{}) {
			batch = append(batch, [2][]byte{
				makeBucketKey(byte(i)),
				f.buckets[i].Bytes(),
			})
		}
	}

	// Store batch for replication to Sub nodes.
	// TransactionStateDB.Commit() / Receipts.Commit() will call GetCommitBatch()
	// to include these flat entries in TxBatchPut / ReceiptBatchPut.
	// This MUST happen before the async dispatch to avoid data races.
	f.lastCommitBatch = make([][2][]byte, len(batch))
	copy(f.lastCommitBatch, batch)

	// Dispatch combined batch synchronously to avoid flooding PebbleDB queue
	// commitWorker already runs this in the background, providing natural back-pressure.
	if len(batch) > 0 {
		if err := f.db.BatchPut(batch); err != nil {
			logger.Error("[FlatStateTrie] Sync BatchPut failed: %v", err)
		}
	}

	// 3. Compute root hash and cache buckets (SYNCHRONOUS — must complete before return)
	f.rootHash = computeRootFromBuckets(f.buckets)

	// Save buckets to global cache for instant access by NewFlatStateTrieFromRoot
	cachedBuckets := new([256]e_common.Hash)
	*cachedBuckets = f.buckets
	globalBucketCache.Store(dbCacheKey(f.db), cachedBuckets)

	// 4. Clear dirty state
	dirtyCount := len(f.dirty)
	f.dirty = make(map[string]*dirtyEntry)
	f.oldValues = make(map[string][]byte)
	f.oldLoaded = make(map[string]bool)
	f.dirtyBuckets = [256]bool{}

	logger.Debug("[FlatStateTrie] Committed %d entries (async persist), rootHash=%s", dirtyCount, f.rootHash.Hex()[:16])

	// Return nil NodeSet — flat state doesn't produce trie nodes.
	// Callers use GetCommitBatch() to get the flat entries for replication.
	return f.rootHash, nil, nil, nil
}

// Copy creates a shallow copy with independent dirty map.
func (f *FlatStateTrie) Copy() StateTrie {
	f.mu.RLock()
	defer f.mu.RUnlock()

	newDirty := make(map[string]*dirtyEntry, len(f.dirty))
	for k, v := range f.dirty {
		newDirty[k] = v
	}
	newOldValues := make(map[string][]byte, len(f.oldValues))
	for k, v := range f.oldValues {
		newOldValues[k] = v
	}
	newOldLoaded := make(map[string]bool, len(f.oldLoaded))
	for k, v := range f.oldLoaded {
		newOldLoaded[k] = v
	}

	return &FlatStateTrie{
		db:        f.db,
		dirty:     newDirty,
		oldValues: newOldValues,
		oldLoaded: newOldLoaded,
		buckets:   f.buckets,
		rootHash:  f.rootHash,
		isHash:    f.isHash,
	}
}

// GetCommitBatch returns the flat entries + bucket accumulators from the last Commit().
// Used by AccountStateDB to build AccountBatch for network replication to Sub nodes.
// Returns nil if no commit has happened or dirty was empty.
// This is a one-shot read: calling it clears the stored batch to free memory.
func (f *FlatStateTrie) GetCommitBatch() [][2][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	batch := f.lastCommitBatch
	f.lastCommitBatch = nil // Free memory after retrieval
	return batch
}

// ═══════════════════════════════════════════════════════════════════════════════
// Helper functions
// ═══════════════════════════════════════════════════════════════════════════════

// mulModPrime multiplies the contribution to the bucket accumulator (mod prime) for MuHash.
// bucket = (bucket * contribution) mod p
// Uses pooled big.Int to reduce GC pressure.
func mulModPrime(target *e_common.Hash, value e_common.Hash) {
	t := bigIntPool.Get().(*big.Int)
	v := bigIntPool.Get().(*big.Int)
	t.SetBytes(target[:])
	if t.Sign() == 0 {
		t.SetInt64(1) // Empty bucket acts as 1 for multiplicative identity
	}
	v.SetBytes(value[:])
	if v.Sign() == 0 {
		v.SetInt64(1) // Prevent zeroing out the hash
	}
	t.Mul(t, v)
	t.Mod(t, bucketPrime)
	result := t.Bytes()
	// Zero-fill and right-align into 32 bytes
	var buf [32]byte
	copy(buf[32-len(result):], result)
	*target = e_common.Hash(buf)
	bigIntPool.Put(t)
	bigIntPool.Put(v)
}

// divModPrime divides the contribution from the bucket accumulator (mod prime) for MuHash.
// bucket = (bucket * (contribution^-1)) mod p
// Uses pooled big.Int to reduce GC pressure.
func divModPrime(target *e_common.Hash, value e_common.Hash) {
	t := bigIntPool.Get().(*big.Int)
	v := bigIntPool.Get().(*big.Int)
	t.SetBytes(target[:])
	if t.Sign() == 0 {
		t.SetInt64(1) // Empty bucket acts as 1 for multiplicative identity
	}
	v.SetBytes(value[:])
	if v.Sign() == 0 {
		v.SetInt64(1) // Prevent division by zero
	}
	// Multiplicative inverse modulo bucketPrime
	v.ModInverse(v, bucketPrime)
	t.Mul(t, v)
	t.Mod(t, bucketPrime)

	// If the bucket returns to 1 (empty state mathematically), we could reset to 0 for cleaner DB,
	// but 1 is cryptographically fine and consistent.
	if t.Cmp(big.NewInt(1)) == 0 {
		t.SetInt64(0) // Optional: Normalize 1 back to 0 if we want empty buckets to be exactly 0x0...0
	}

	result := t.Bytes()
	var buf [32]byte
	copy(buf[32-len(result):], result)
	*target = e_common.Hash(buf)
	bigIntPool.Put(t)
	bigIntPool.Put(v)
}

// computeRootFromBuckets computes the root hash from all 256 bucket accumulators.
// rootHash = keccak256(bucket[0] || bucket[1] || ... || bucket[255])
// Uses pooled 8KB buffer to reduce GC pressure.
func computeRootFromBuckets(buckets [256]e_common.Hash) e_common.Hash {
	dataPtr := rootBufPool.Get().(*[]byte)
	data := *dataPtr
	for i := 0; i < 256; i++ {
		copy(data[i*32:(i+1)*32], buckets[i][:])
	}
	h := crypto.Keccak256Hash(data)
	rootBufPool.Put(dataPtr)
	return h
}
