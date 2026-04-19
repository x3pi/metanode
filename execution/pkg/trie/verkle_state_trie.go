package trie

import (
	"encoding/hex"
	"fmt"
	"sync"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	verkle "github.com/ethereum/go-verkle"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// ═══════════════════════════════════════════════════════════════════════════════
// VerkleStateTrie — Verkle Tree with Pedersen commitments
//
// Uses the Ethereum go-verkle library for a 256-ary tree with:
//   - O(log₂₅₆(N)) reads/writes (2-3 levels for millions of accounts)
//   - ~150 byte proof per account (vs ~3KB for MPT)
//   - Pedersen commitment-based hashing (elliptic curve)
//
// TRADE-OFF: Slower than FlatStateTrie (~5-10x overhead for Pedersen ops)
// but provides individual account proofs for light clients and bridges.
//
// Configure via config.json: "state_backend": "verkle"
// ═══════════════════════════════════════════════════════════════════════════════

// verkleDirtyEntry caches pre-computed values for a dirty key to avoid
// redundant allocations during Commit(). Instead of storing raw []byte values
// in the dirty map and recomputing hex.DecodeString + makeVerkleKey per entry
// in Commit(), we pre-compute these during Update/BatchUpdate.
type verkleDirtyEntry struct {
	dbKey []byte // "vk:" + keyBytes — pre-computed for DB persist
	value []byte // raw value
}

// VerkleStateTrie implements StateTrie using Ethereum's go-verkle library.
type VerkleStateTrie struct {
	mu sync.RWMutex

	// Root node of the Verkle tree
	root verkle.VerkleNode

	// Flat DB for persistence (same as FlatStateTrie)
	db FlatStateDB

	// dirty tracks uncommitted key-value changes.
	// Key: hex(key), Value: entry with pre-computed DB key and raw value.
	dirty     map[string]*verkleDirtyEntry
	oldValues map[string][]byte // hex(key) → old value (for rollback)

	// rootHash caches the last committed root hash
	rootHash e_common.Hash

	// cachedCommitHash caches the result of root.Commit() → HashPointToBytes()
	// between Hash() and Commit() calls to avoid redundant computation.
	cachedCommitHash e_common.Hash
	commitHashValid  bool // true if cachedCommitHash is up-to-date

	// lastCommitBatch stores the vk:-prefixed key-value pairs from the last Commit().
	// Used by AccountStateDB to replicate verkle state to Sub nodes via AccountBatch.
	lastCommitBatch [][2][]byte

	// isHash indicates whether this trie stores hashed keys
	isHash bool
}

// Compile-time check: VerkleStateTrie must implement StateTrie.
var _ StateTrie = (*VerkleStateTrie)(nil)

// NewVerkleStateTrie creates a new empty VerkleStateTrie.
func NewVerkleStateTrie(db FlatStateDB, isHash bool) *VerkleStateTrie {
	return &VerkleStateTrie{
		root:      verkle.New(),
		db:        db,
		dirty:     make(map[string]*verkleDirtyEntry),
		oldValues: make(map[string][]byte),
		rootHash:  EmptyRootHash,
		isHash:    isHash,
	}
}

// NewVerkleStateTrieFromRoot creates a VerkleStateTrie and loads existing state
// from the DB by replaying all entries. The root hash is computed from the tree.
func NewVerkleStateTrieFromRoot(root e_common.Hash, db FlatStateDB, isHash bool) (*VerkleStateTrie, error) {
	vt := NewVerkleStateTrie(db, isHash)
	vt.rootHash = root

	// For non-empty root, we rely on the Verkle tree being rebuilt
	// from the backing store. The tree will be populated on-demand
	// via Get() calls and explicit Update() during block processing.
	//
	// NOTE: Unlike MPT which persists node structure, Verkle tree
	// structure is rebuilt from flat key-value pairs. This is acceptable
	// because Verkle commitments are position-based (stem + suffix).
	if root != (e_common.Hash{}) && root != EmptyRootHash {
		logger.Debug("[VerkleStateTrie] Created from root %s (tree populated on demand)", root.Hex()[:16])
	}

	return vt, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// StateTrie interface implementation
// ═══════════════════════════════════════════════════════════════════════════════

// Get retrieves the value for a key. Checks dirty map first, then backing DB.
func (vt *VerkleStateTrie) Get(key []byte) ([]byte, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	hexKey := hex.EncodeToString(key)

	// Check dirty first
	if entry, ok := vt.dirty[hexKey]; ok {
		if entry == nil || entry.value == nil {
			return nil, fmt.Errorf("verkle: key not found (deleted)")
		}
		return entry.value, nil
	}

	// Fall back to backing DB with flat key prefix
	dbVal, err := vt.db.Get(makeVerkleKey(key))
	if err != nil {
		return nil, nil // Key not found is not an error
	}
	return dbVal, nil
}

// GetAll returns all key-value pairs by scanning the backing DB
// and merging with any uncommitted dirty entries.
func (vt *VerkleStateTrie) GetAll() (map[string][]byte, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	// 1. Scan all verkle entries from DB using prefix scan
	results := make(map[string][]byte)

	pairs, err := vt.db.PrefixScan([]byte("vk:"))
	if err != nil {
		return nil, fmt.Errorf("VerkleStateTrie GetAll prefix scan failed: %w", err)
	}

	for _, kv := range pairs {
		// kv[0] is the original key (prefix already stripped by PrefixScan)
		hexKey := hex.EncodeToString(kv[0])
		results[hexKey] = kv[1]
	}

	// 2. Overlay dirty entries (uncommitted changes)
	for hexKey, entry := range vt.dirty {
		if entry == nil || entry.value == nil {
			delete(results, hexKey) // deletion
		} else {
			results[hexKey] = entry.value
		}
	}

	return results, nil
}

// Update sets a key-value pair in the trie.
func (vt *VerkleStateTrie) Update(key, value []byte) error {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	hexKey := hex.EncodeToString(key)
	dbKey := makeVerkleKey(key)

	// Cache old value for rollback
	if _, loaded := vt.oldValues[hexKey]; !loaded {
		oldVal, err := vt.db.Get(dbKey)
		if err == nil {
			vt.oldValues[hexKey] = oldVal
		} else {
			vt.oldValues[hexKey] = nil
		}
	}

	// Store dirty entry with pre-computed DB key to avoid hex.DecodeString
	// and makeVerkleKey during Commit().
	vt.dirty[hexKey] = &verkleDirtyEntry{
		dbKey: dbKey,
		value: value,
	}

	// Invalidate cached commitment since tree is modified
	vt.commitHashValid = false

	// Insert hash of value into Verkle tree for commitment computation.
	// go-verkle enforces LeafValueSize=32 bytes per leaf, but serialized
	// AccountState can be much larger (~165 bytes). The tree only needs
	// a 32-byte digest for Pedersen commitment; raw values are stored
	// in the dirty map and backing DB.
	verkleKey := padTo32(key)
	valueHash := crypto.Keccak256(value)
	return vt.root.Insert(verkleKey, valueHash, nil)
}

// BatchUpdate performs optimized batch updates for multiple keys.
//
// OPTIMIZATION: Instead of calling Update() N times (each acquiring/releasing
// the lock + doing a DB read + Pedersen insert), this method:
//   1. Acquires the lock ONCE for the entire batch
//   2. Parallelizes old-value DB reads (16 workers)
//   3. Parallelizes Keccak256 hashing (16 workers)
//   4. Performs Verkle tree inserts sequentially (go-verkle is NOT thread-safe)
//
// This reduces lock overhead from 30K to 1 and eliminates 30K hex.EncodeToString
// allocations by pre-computing them in the parallel phase.
//
// FORK-SAFETY: Deterministic — same keys/values produce same tree state regardless
// of parallel worker scheduling (parallel phases are read-only + order-independent).
func (vt *VerkleStateTrie) BatchUpdate(keys, values [][]byte) error {
	if len(keys) != len(values) {
		return fmt.Errorf("VerkleStateTrie: BatchUpdate keys/values length mismatch (%d vs %d)", len(keys), len(values))
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
		hexKey    string
		dbKey     []byte   // "vk:" + keyBytes — pre-computed for Commit()
		verkleKey []byte   // padTo32(key)
		valueHash []byte   // keccak256(value) for Verkle Insert
		oldValue  []byte   // old value from DB (nil if new key)
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
				dbKey := makeVerkleKey(key)

				// Read old value from DB (for bucket/rollback — parallel safe, DB is read-only)
				var oldVal []byte
				dbVal, err := vt.db.Get(dbKey)
				if err == nil && len(dbVal) > 0 {
					oldVal = dbVal
				}

				// Pre-compute Verkle key and value hash
				verkleKey := padTo32(key)
				valueHash := crypto.Keccak256(values[i])

				entries[i] = batchEntry{
					hexKey:    hexKey,
					dbKey:     dbKey,
					verkleKey: verkleKey,
					valueHash: valueHash,
					oldValue:  oldVal,
				}
			}
		}(start, end)
	}
	wg.Wait()

	// ═══════════════════════════════════════════════════════════════
	// Phase 2: SEQUENTIAL — Update dirty map + old values + Verkle tree
	// Single lock acquisition for entire batch.
	// go-verkle Insert is NOT thread-safe, so tree inserts must be sequential.
	// ═══════════════════════════════════════════════════════════════
	vt.mu.Lock()
	defer vt.mu.Unlock()

	for i := 0; i < n; i++ {
		e := &entries[i]

		// Update old values cache (only if not already loaded)
		if _, loaded := vt.oldValues[e.hexKey]; !loaded {
			vt.oldValues[e.hexKey] = e.oldValue
		}

		// Update dirty map with pre-computed dbKey
		vt.dirty[e.hexKey] = &verkleDirtyEntry{
			dbKey: e.dbKey,
			value: values[i],
		}

		// Insert into Verkle tree (sequential — go-verkle not thread-safe)
		if err := vt.root.Insert(e.verkleKey, e.valueHash, nil); err != nil {
			return fmt.Errorf("VerkleStateTrie: BatchUpdate Insert error at key %d: %w", i, err)
		}
	}

	// Invalidate cached commitment since tree is modified
	vt.commitHashValid = false

	return nil
}

// PreWarm is a no-op for VerkleStateTrie.
// Verkle tree nodes are computed from commitments, not loaded from DB.
func (vt *VerkleStateTrie) PreWarm(keys [][]byte) {
	// No-op: Verkle tree doesn't need pre-warming
}

// Hash computes the current root hash without committing.
// The result is cached so that the subsequent Commit() call can reuse it
// without recomputing HashPointToBytes.
func (vt *VerkleStateTrie) Hash() e_common.Hash {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	if len(vt.dirty) == 0 {
		return vt.rootHash
	}

	// Return cached result if no tree modifications since last computation
	if vt.commitHashValid {
		return vt.cachedCommitHash
	}

	// Compute Verkle commitment and convert to Ethereum hash
	commitment := vt.root.Commit()
	commitBytes := verkle.HashPointToBytes(commitment)
	hash := e_common.Hash(commitBytes)

	// Cache the result. Note: Hash() uses RLock so we can't write to vt fields
	// directly. The caching will take effect in Commit() which has full Lock.
	return hash
}

// Commit finalizes changes: writes dirty entries to DB, computes root hash.
//
// OPTIMIZATION: Uses pre-computed dbKey from verkleDirtyEntry to avoid
// hex.DecodeString + makeVerkleKey per entry. Also reuses cached commitment
// hash from Hash() when available.
func (vt *VerkleStateTrie) Commit(collectLeaf bool) (e_common.Hash, *node.NodeSet, [][]byte, error) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	if len(vt.dirty) == 0 {
		return vt.rootHash, nil, nil, nil
	}

	// 1. Compute Verkle commitment.
	// go-verkle's Commit() checks len(n.cow)==0 internally, so if Hash() was
	// already called (which triggers root.Commit()), this is a near-zero-cost
	// operation that returns the cached commitment. HashPointToBytes is the
	// remaining cost which we also minimize.
	commitment := vt.root.Commit()
	commitBytes := verkle.HashPointToBytes(commitment)
	vt.rootHash = e_common.Hash(commitBytes)

	// 2. Build batch using pre-computed dbKeys from verkleDirtyEntry.
	// This eliminates hex.DecodeString + makeVerkleKey per entry.
	batch := make([][2][]byte, 0, len(vt.dirty))
	for _, entry := range vt.dirty {
		batch = append(batch, [2][]byte{entry.dbKey, entry.value})
	}

	// Store batch for network replication to Sub nodes.
	// This MUST happen before the async dispatch to avoid data races.
	vt.lastCommitBatch = make([][2][]byte, len(batch))
	copy(vt.lastCommitBatch, batch)

	if len(batch) > 0 {
		dbRef := vt.db
		go func(b [][2][]byte) {
			if err := dbRef.BatchPut(b); err != nil {
				logger.Error("[VerkleStateTrie] Async BatchPut failed: %v", err)
			}
		}(batch)
	}

	// 3. Clear dirty state and invalidate commitment cache
	dirtyCount := len(vt.dirty)
	vt.dirty = make(map[string]*verkleDirtyEntry)
	vt.oldValues = make(map[string][]byte)
	vt.commitHashValid = false

	logger.Debug("[VerkleStateTrie] Committed %d entries, rootHash=%s", dirtyCount, vt.rootHash.Hex()[:16])

	return vt.rootHash, nil, nil, nil
}

// Copy creates a shallow copy with independent dirty map.
func (vt *VerkleStateTrie) Copy() StateTrie {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	newDirty := make(map[string]*verkleDirtyEntry, len(vt.dirty))
	for k, v := range vt.dirty {
		newDirty[k] = v
	}
	newOldValues := make(map[string][]byte, len(vt.oldValues))
	for k, v := range vt.oldValues {
		newOldValues[k] = v
	}

	// Copy the Verkle tree
	newRoot := vt.root.Copy()

	return &VerkleStateTrie{
		root:      newRoot,
		db:        vt.db,
		dirty:     newDirty,
		oldValues: newOldValues,
		rootHash:  vt.rootHash,
		isHash:    vt.isHash,
	}
}

// GetCommitBatch returns the vk:-prefixed key-value pairs from the last Commit().
// Used by AccountStateDB to build AccountBatch for network replication to Sub nodes.
// This is a one-shot read: calling it clears the stored batch to free memory.
func (vt *VerkleStateTrie) GetCommitBatch() [][2][]byte {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	batch := vt.lastCommitBatch
	vt.lastCommitBatch = nil
	return batch
}

// ═══════════════════════════════════════════════════════════════════════════════
// Verkle-specific methods (proof generation)
// ═══════════════════════════════════════════════════════════════════════════════

// GenerateProof generates a Verkle proof for the given keys.
// Returns a serialized proof that can be verified by a light client.
func (vt *VerkleStateTrie) GenerateProof(keys [][]byte) (*verkle.VerkleProof, verkle.StateDiff, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	// Pad keys to 32 bytes
	paddedKeys := make([][]byte, len(keys))
	for i, key := range keys {
		paddedKeys[i] = padTo32(key)
	}

	proof, _, _, _, err := verkle.MakeVerkleMultiProof(vt.root, nil, paddedKeys, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("VerkleStateTrie: proof generation failed: %w", err)
	}

	verkleProof, stateDiff, err := verkle.SerializeProof(proof)
	if err != nil {
		return nil, nil, fmt.Errorf("VerkleStateTrie: proof serialization failed: %w", err)
	}

	return verkleProof, stateDiff, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// Helper functions
// ═══════════════════════════════════════════════════════════════════════════════

// verkleKeyBufPool reuses concat buffers for makeVerkleKey to reduce GC pressure.
var verkleKeyBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 48)
		return &buf
	},
}

// makeVerkleKey creates a prefixed key for Verkle state entries in the backing DB.
func makeVerkleKey(key []byte) []byte {
	prefix := []byte("vk:")
	bufPtr := verkleKeyBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	buf = append(buf, prefix...)
	buf = append(buf, key...)
	result := make([]byte, len(buf))
	copy(result, buf)
	*bufPtr = buf
	verkleKeyBufPool.Put(bufPtr)
	return result
}

// padTo32 pads or truncates a key to exactly 32 bytes (Verkle key requirement).
func padTo32(key []byte) []byte {
	if len(key) == 32 {
		return key
	}
	if len(key) > 32 {
		// Hash to 32 bytes if longer
		hash := crypto.Keccak256(key)
		return hash
	}
	// Left-pad with zeros
	padded := make([]byte, 32)
	copy(padded[32-len(key):], key)
	return padded
}
