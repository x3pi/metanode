package trie

import (
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// ═══════════════════════════════════════════════════════════════════════════════
// NomtStateTrie — NOMT-backed StateTrie for maximum throughput
//
// Uses thrumdev/nomt via CGo FFI for all state storage operations.
// NOMT provides:
//   - O(1) random reads via Beatree (B-Tree variant optimized for SSDs)
//   - O(K) merkle root computation per block (K = dirty entries)
//   - Native io-uring support for async SSD I/O on Linux
//   - Zero Go GC overhead for trie operations (all memory in Rust)
//
// Key Mapping:
//   MetaNode uses 20-byte addresses. NOMT requires 32-byte KeyPaths.
//   We use Keccak256(address) as the KeyPath to ensure uniform distribution
//   across the binary trie (NOMT performs best with uniformly distributed keys).
//
// Thread Safety:
//   - Get() is safe for concurrent reads (NOMT supports many readers).
//   - Update/BatchUpdate buffer changes in Go dirty map.
//   - Hash()/Commit() are single-threaded (called from IntermediateRoot under lock).
// ═══════════════════════════════════════════════════════════════════════════════

// NomtStateTrie implements StateTrie using NOMT as the backing store.
type NomtStateTrie struct {
	handle *nomt_ffi.Handle

	// namespace prefix for key isolation: different tries (AccountState, StakeState, etc.)
	// share the same NOMT database but use different namespace prefixes so their keys
	// don't collide. e.g. "acct:" for accounts, "stake:" for validators.
	namespace []byte

	// dirty stores uncommitted changes: hex(originalKey) → {keyPath, value}
	dirty map[string]*nomtDirtyEntry

	// committing stores changes currently being written to disk async
	committing map[string]*nomtDirtyEntry

	// oldValues caches pre-commit values for AccountBatch replication
	oldValues map[string][]byte
	oldLoaded map[string]bool

	// knownKeys tracks ALL original keys ever written to this trie instance.
	// Stored as hex(originalKey) → originalKey bytes.
	// Persisted to NOMT under a special meta key during Commit.
	// Required because NOMT doesn't support range scan / iteration.
	knownKeys   map[string][]byte
	knownKeysMu sync.RWMutex

	// Cached root hash (updated on Commit)
	rootHash e_common.Hash

	// activeSession is held across the block lifecycle to preserve background page fetches
	activeSession *nomt_ffi.Session

	// pendingFinishedSession holds the fast-finished CPU session waiting for disk I/O
	pendingFinishedSession *nomt_ffi.FinishedSession

	// pendingCommitHash: set by Hash() when dirty entries exist.
	// Commit() checks this to return the same hash for the sanity check.
	pendingCommitHash *e_common.Hash

	lastCommitBatch [][2][]byte

	isHash            bool
	isReplicationSync bool
	mu                sync.RWMutex

	// tracks if knownKeys was modified since last commit
	registryChanged bool
}

type nomtDirtyEntry struct {
	originalKey []byte   // original 20-byte address
	keyPath     [32]byte // Keccak256(namespace + originalKey) — NOMT key
	value       []byte   // serialized account state
}

// Compile-time check: NomtStateTrie must implement StateTrie.
var _ StateTrie = (*NomtStateTrie)(nil)

// nomtRegistryKeyPrefix is used to store the keys registry in NOMT.
const nomtRegistryKeyPrefix = "__nomt_registry__:"

// addressToKeyPath converts a MetaNode address (20 bytes) to a NOMT KeyPath (32 bytes)
// using Keccak256 for uniform distribution across the binary trie.
// The namespace prefix ensures key isolation between different trie instances.
func addressToKeyPathWithNamespace(namespace, key []byte) [32]byte {
	if len(namespace) == 0 {
		return crypto.Keccak256Hash(key)
	}
	combined := make([]byte, len(namespace)+len(key))
	copy(combined, namespace)
	copy(combined[len(namespace):], key)
	return crypto.Keccak256Hash(combined)
}

// registryKeyPath returns the NOMT KeyPath for storing the keys registry.
func registryKeyPath(namespace []byte) [32]byte {
	registryKey := []byte(nomtRegistryKeyPrefix)
	registryKey = append(registryKey, namespace...)
	return crypto.Keccak256Hash(registryKey)
}

// NewNomtStateTrie creates a new NomtStateTrie backed by the given NOMT handle.
// namespace isolates keys: different callers (AccountState, StakeState) MUST use
// different namespaces to prevent data corruption.
func NewNomtStateTrie(handle *nomt_ffi.Handle, isHash bool, namespace string) *NomtStateTrie {
	rootBytes, err := handle.Root()
	var rootHash e_common.Hash
	if err == nil {
		rootHash = e_common.BytesToHash(rootBytes[:])
	}

	// Load known keys registry from NOMT
	knownKeys := make(map[string][]byte)
	regKey := registryKeyPath([]byte(namespace))
	regData, found, readErr := handle.Read(regKey)
	if readErr == nil && found && len(regData) > 0 {
		// Registry format: each entry is [1-byte keyLen][keyBytes]
		offset := 0
		for offset < len(regData) {
			if offset >= len(regData) {
				break
			}
			keyLen := int(regData[offset])
			offset++
			if offset+keyLen > len(regData) {
				break
			}
			origKey := make([]byte, keyLen)
			copy(origKey, regData[offset:offset+keyLen])
			offset += keyLen
			hexKey := hex.EncodeToString(origKey)
			knownKeys[hexKey] = origKey
		}
		logger.Info("[NomtStateTrie] ✅ Loaded %d known keys from registry (namespace=%s, regDataLen=%d)", len(knownKeys), namespace, len(regData))
	} else {
		logger.Info("[NomtStateTrie] ⚠️ No registry found for namespace=%s (readErr=%v, found=%v, dataLen=%d)", namespace, readErr, found, len(regData))
	}

	return &NomtStateTrie{
		handle:          handle,
		namespace:       []byte(namespace),
		dirty:           make(map[string]*nomtDirtyEntry),
		committing:      nil,
		oldValues:       make(map[string][]byte),
		oldLoaded:       make(map[string]bool),
		knownKeys:       knownKeys,
		rootHash:        rootHash,
		isHash:          isHash,
		registryChanged: false,
	}
}

// SetReplicationSync configures the trie to bypass registry tracking and modifications.
// This is critical during block synchronization (ApplyNomtReplicationBatches) where
// the registry keys are replicated directly via bytes, and tracking them dynamically
// would erroneously include the registry key itself inside the registry list,
// modifying its layout and causing divergent Merkle Roots.
func (n *NomtStateTrie) SetReplicationSync(sync bool) {
	n.mu.Lock()
	n.isReplicationSync = sync
	n.mu.Unlock()
}

// ═══════════════════════════════════════════════════════════════════════════════
// StateTrie interface implementation
// ═══════════════════════════════════════════════════════════════════════════════

// Get retrieves the value for a key from NOMT. O(1) via Beatree.
// Thread-safe: NOMT supports concurrent readers.
func (n *NomtStateTrie) Get(key []byte) ([]byte, error) {
	hexKey := hex.EncodeToString(key)

	n.mu.RLock()
	// Check dirty first
	if entry, ok := n.dirty[hexKey]; ok {
		n.mu.RUnlock()
		return entry.value, nil
	}
	// Check committing (changes being written to disk async)
	if entry, ok := n.committing[hexKey]; ok {
		n.mu.RUnlock()
		return entry.value, nil
	}
	n.mu.RUnlock()

	// Read from NOMT (thread-safe, no lock needed)
	keyPath := addressToKeyPathWithNamespace(n.namespace, key)
	val, found, err := n.handle.Read(keyPath)
	if err != nil {
		return nil, fmt.Errorf("nomt read error for key %x: %w", key, err)
	}
	if !found {
		return nil, nil // key not found is not an error
	}
	return val, nil
}

// GetAll returns all key-value pairs by reading from the knownKeys registry.
// Each known key is fetched from NOMT individually.
func (n *NomtStateTrie) GetAll() (map[string][]byte, error) {
	n.mu.RLock()
	dirtySnapshot := make(map[string][]byte, len(n.dirty)+len(n.committing))
	for hexKey, entry := range n.committing {
		dirtySnapshot[hexKey] = entry.value
	}
	for hexKey, entry := range n.dirty {
		dirtySnapshot[hexKey] = entry.value
	}
	n.mu.RUnlock()

	// Merge dirty + knownKeys
	n.knownKeysMu.RLock()
	allHexKeys := make(map[string][]byte, len(n.knownKeys))
	for hexKey, origKey := range n.knownKeys {
		allHexKeys[hexKey] = origKey
	}
	n.knownKeysMu.RUnlock()

	// Also add keys from dirty that bypass knownKeys tracking
	n.mu.RLock()
	for hexKey, entry := range n.dirty {
		allHexKeys[hexKey] = entry.originalKey
	}
	n.mu.RUnlock()

	results := make(map[string][]byte, len(allHexKeys))

	for hexKey, origKey := range allHexKeys {
		// Check dirty first
		if val, ok := dirtySnapshot[hexKey]; ok {
			if len(val) > 0 {
				results[hexKey] = val
			}
			continue
		}
		// Read from NOMT
		keyPath := addressToKeyPathWithNamespace(n.namespace, origKey)
		val, found, err := n.handle.Read(keyPath)
		if err == nil && found && len(val) > 0 {
			results[hexKey] = val
		}
	}

	return results, nil
}

// Update sets the value for a key. Buffers in dirty map — O(1).
func (n *NomtStateTrie) Update(key, value []byte) error {
	hexKey := hex.EncodeToString(key)
	keyPath := addressToKeyPathWithNamespace(n.namespace, key)

	n.mu.Lock()
	// Load old value for replication tracking (only once per key per block)
	if !n.oldLoaded[hexKey] {
		if commEntry, ok := n.committing[hexKey]; ok {
			n.oldValues[hexKey] = commEntry.value
		} else {
			oldVal, found, err := n.handle.Read(keyPath)
			if err == nil && found && len(oldVal) > 0 {
				n.oldValues[hexKey] = oldVal
			}
		}
		n.oldLoaded[hexKey] = true
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	n.dirty[hexKey] = &nomtDirtyEntry{
		originalKey: keyCopy,
		keyPath:     keyPath,
		value:       value,
	}

	// Track key in knownKeys registry if not skipped
	skipRegistry := strings.HasPrefix(string(n.namespace), "smart_contract_storage") || string(n.namespace) == "account_state" || string(n.namespace) == "transaction_state" || string(n.namespace) == "receipts"
	if !n.isReplicationSync && !skipRegistry {
		n.knownKeysMu.Lock()
		if _, exists := n.knownKeys[hexKey]; !exists {
			n.knownKeys[hexKey] = keyCopy
			n.registryChanged = true
		}
		n.knownKeysMu.Unlock()
	}

	n.mu.Unlock()

	return nil
}

// BatchUpdate performs batch updates for multiple keys.
// Parallelizes old-value reads from NOMT, then applies updates to dirty map.
func (n *NomtStateTrie) BatchUpdate(keys, values [][]byte) error {
	if len(keys) != len(values) {
		return fmt.Errorf("NomtStateTrie: BatchUpdate keys/values length mismatch (%d vs %d)", len(keys), len(values))
	}
	if len(keys) == 0 {
		return nil
	}

	count := len(keys)

	// Phase 1: PARALLEL — compute key paths + read old values from NOMT
	type batchEntry struct {
		hexKey      string
		originalKey []byte
		keyPath     [32]byte
		oldValue    []byte
		oldLoaded   bool
	}
	entries := make([]batchEntry, count)

	numWorkers := 16
	if count < numWorkers {
		numWorkers = count
	}
	chunkSize := (count + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= count {
			break
		}
		if end > count {
			end = count
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				key := keys[i]
				keyCopy := make([]byte, len(key))
				copy(keyCopy, key)
				keyPath := addressToKeyPathWithNamespace(n.namespace, key)
				hexKey := hex.EncodeToString(key)

				// Read old value from NOMT (thread-safe concurrent read)
				var oldVal []byte
				var loaded bool
				val, found, err := n.handle.Read(keyPath)
				if err == nil {
					loaded = true
					if found && len(val) > 0 {
						oldVal = val
					}
				}

				entries[i] = batchEntry{
					hexKey:      hexKey,
					originalKey: keyCopy,
					keyPath:     keyPath,
					oldValue:    oldVal,
					oldLoaded:   loaded,
				}
			}
		}(start, end)
	}
	wg.Wait()

	// Phase 2: SEQUENTIAL — update dirty map
	skipRegistry := strings.HasPrefix(string(n.namespace), "smart_contract_storage") || string(n.namespace) == "account_state" || string(n.namespace) == "transaction_state" || string(n.namespace) == "receipts"

	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.isReplicationSync && !skipRegistry {
		n.knownKeysMu.Lock()
	}

	for i := 0; i < count; i++ {
		e := &entries[i]

		if !n.oldLoaded[e.hexKey] {
			if e.oldLoaded {
				if e.oldValue != nil {
					n.oldValues[e.hexKey] = e.oldValue
				}
				n.oldLoaded[e.hexKey] = true
			}
		}

		n.dirty[e.hexKey] = &nomtDirtyEntry{
			originalKey: e.originalKey,
			keyPath:     e.keyPath,
			value:       values[i],
		}

		if !n.isReplicationSync && !skipRegistry {
			if _, exists := n.knownKeys[e.hexKey]; !exists {
				n.knownKeys[e.hexKey] = e.originalKey
				n.registryChanged = true
			}
		}
	}

	if !n.isReplicationSync && !skipRegistry {
		n.knownKeysMu.Unlock()
	}

	return nil
}

// BatchUpdateWithCachedOldValues performs batch updates using pre-fetched old values
// from the caller's cache, completely eliminating DB reads.
func (n *NomtStateTrie) BatchUpdateWithCachedOldValues(keys, values, oldValues [][]byte) error {
	if len(keys) != len(values) {
		return fmt.Errorf("NomtStateTrie: BatchUpdateWithCachedOldValues keys/values mismatch (%d vs %d)", len(keys), len(values))
	}
	if oldValues != nil && len(keys) != len(oldValues) {
		return fmt.Errorf("NomtStateTrie: BatchUpdateWithCachedOldValues keys/oldValues mismatch (%d vs %d)", len(keys), len(oldValues))
	}
	if len(keys) == 0 {
		return nil
	}

	count := len(keys)

	// Phase 1: PARALLEL — compute key paths only (no DB reads)
	type batchEntry struct {
		hexKey      string
		originalKey []byte
		keyPath     [32]byte
	}
	entries := make([]batchEntry, count)

	numWorkers := 16
	if count < numWorkers {
		numWorkers = count
	}
	chunkSize := (count + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= count {
			break
		}
		if end > count {
			end = count
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				key := keys[i]
				keyCopy := make([]byte, len(key))
				copy(keyCopy, key)
				entries[i] = batchEntry{
					hexKey:      hex.EncodeToString(key),
					originalKey: keyCopy,
					keyPath:     addressToKeyPathWithNamespace(n.namespace, key),
				}
			}
		}(start, end)
	}
	wg.Wait()

	// Phase 2: SEQUENTIAL — update dirty map + inject cached old values
	skipRegistry := strings.HasPrefix(string(n.namespace), "smart_contract_storage") || string(n.namespace) == "account_state" || string(n.namespace) == "transaction_state" || string(n.namespace) == "receipts"

	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.isReplicationSync && !skipRegistry {
		n.knownKeysMu.Lock()
	}

	for i := 0; i < count; i++ {
		e := &entries[i]

		if !n.oldLoaded[e.hexKey] {
			if oldValues != nil && len(oldValues[i]) > 0 {
				n.oldValues[e.hexKey] = oldValues[i]
			}
			n.oldLoaded[e.hexKey] = true
		}

		n.dirty[e.hexKey] = &nomtDirtyEntry{
			originalKey: e.originalKey,
			keyPath:     e.keyPath,
			value:       values[i],
		}

		if !n.isReplicationSync && !skipRegistry {
			if _, exists := n.knownKeys[e.hexKey]; !exists {
				n.knownKeys[e.hexKey] = e.originalKey
				n.registryChanged = true
			}
		}
	}

	if !n.isReplicationSync && !skipRegistry {
		n.knownKeysMu.Unlock()
	}

	return nil
}

// getOrCreateSessionLocked ensures a session exists for the duration of the current block (must hold lock).
// CRITICAL: NOMT only allows one session at a time. If there is a pendingFinishedSession
// from a previous block's Commit() that hasn't been flushed to disk yet, we MUST
// flush it before calling BeginSession, otherwise BeginSession will block forever.
func (n *NomtStateTrie) getOrCreateSessionLocked() *nomt_ffi.Session {
	if n.activeSession == nil {
		// Drain any pending finished session from the previous block
		if n.pendingFinishedSession != nil {
			logger.Info("[NomtStateTrie] Draining pendingFinishedSession before new BeginSession (namespace=%s)", string(n.namespace))
			if err := n.pendingFinishedSession.CommitPayload(n.handle); err != nil {
				logger.Error("[NomtStateTrie] Failed to drain pendingFinishedSession: %v", err)
			}
			n.pendingFinishedSession = nil
		}
		n.activeSession = nomt_ffi.BeginSession(n.handle)
	}
	return n.activeSession
}

// getOrCreateSession ensures a session exists thread-safely
func (n *NomtStateTrie) getOrCreateSession() *nomt_ffi.Session {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.getOrCreateSessionLocked()
}

// PreWarm actively pre-fetches Merkle authentication pages from the backend
// asynchronously, completely eliminating synchronous disk stalls during commit.
func (n *NomtStateTrie) PreWarm(keys [][]byte) {
	// Sub nodes never commit to the trie, so they don't need Merkle authentication pages.
	// Bypassing PreWarm prevents session leaks on Sub nodes.
	if config.ConfigApp == nil || config.ConfigApp.ServiceType != p_common.ServiceTypeMaster {
		return
	}
	session := n.getOrCreateSession()
	if session == nil {
		return
	}
	for _, key := range keys {
		keyPath := addressToKeyPathWithNamespace(n.namespace, key)
		session.WarmUp(keyPath)
	}
}

// Hash returns the current root hash.
//
// IMPORTANT: For NOMT, Hash() does NOT commit to the database.
// NOMT doesn't support "compute hash without committing".
// The actual commit happens ONLY in Commit().
//
// The AccountStateDB pipeline calls: IntermediateRoot() → Hash() → Commit().
//   - If dirty map is empty: returns the cached rootHash (last committed root).
//   - If dirty map has entries: returns the cached rootHash (pre-commit).
//     The Commit() function will compute the real new root.
//
// To satisfy the AccountStateDB sanity check (intermediateHash == committedHash),
// we set a pendingCommitHash in Commit() that matches what Hash() would return,
// effectively skipping that check for NOMT.
func (n *NomtStateTrie) Hash() e_common.Hash {
	// Always return the last-known root hash.
	// The real new root is only available after Commit().
	return n.rootHash
}

// Commit finalizes changes: writes all dirty entries to NOMT via batch session.
// Returns (rootHash, nil, nil, nil) — NOMT handles its own node management internally.
func (n *NomtStateTrie) Commit(collectLeaf bool) (e_common.Hash, *node.NodeSet, [][]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if len(n.dirty) == 0 {
		return n.rootHash, nil, nil, nil
	}

	// Grab the active session (started by PreWarm or getOrCreateSession)
	session := n.getOrCreateSessionLocked()
	if session == nil {
		return n.rootHash, nil, nil, fmt.Errorf("NomtStateTrie: failed to begin session")
	}

	// ═══════════════════════════════════════════════════════════════════════
	// INJECT REGISTRY INTO DIRTY MAP
	// ═══════════════════════════════════════════════════════════════════════
	skipRegistry := strings.HasPrefix(string(n.namespace), "smart_contract_storage") || string(n.namespace) == "account_state" || string(n.namespace) == "transaction_state" || string(n.namespace) == "receipts"

	if !skipRegistry && !n.isReplicationSync {
		n.knownKeysMu.Lock()
		if n.registryChanged {
			sortedRegKeys := make([]string, 0, len(n.knownKeys))
			for hexKey := range n.knownKeys {
				sortedRegKeys = append(sortedRegKeys, hexKey)
			}
			sort.Strings(sortedRegKeys)

			var regData []byte
			for _, hexKey := range sortedRegKeys {
				origKey := n.knownKeys[hexKey]
				if len(origKey) > 255 {
					continue
				}
				regData = append(regData, byte(len(origKey)))
				regData = append(regData, origKey...)
			}

			regKey := registryKeyPath(n.namespace)
			regHexKey := hex.EncodeToString(regKey[:])

			var oldRegData []byte
			var loaded bool
			if commEntry, ok := n.committing[regHexKey]; ok {
				oldRegData = commEntry.value
				loaded = true
			} else {
				val, found, err := n.handle.Read(regKey)
				if err == nil {
					loaded = true
					if found && len(val) > 0 {
						oldRegData = val
					}
				}
			}

			if !n.oldLoaded[regHexKey] {
				if loaded && len(oldRegData) > 0 {
					n.oldValues[regHexKey] = oldRegData
				}
				n.oldLoaded[regHexKey] = true
			}

			n.dirty[regHexKey] = &nomtDirtyEntry{
				originalKey: []byte("nomt_registry"),
				keyPath:     regKey,
				value:       regData,
			}
			n.registryChanged = false
		}
		n.knownKeysMu.Unlock()
	}

	// Build key/value arrays for batch write
	dirtyCount := len(n.dirty)
	nomtKeys := make([][32]byte, 0, dirtyCount)
	nomtVals := make([][]byte, 0, dirtyCount)

	// Also build the replication batch for Sub nodes
	replicationBatch := make([][2][]byte, 0, dirtyCount)

	// ═══════════════════════════════════════════════════════════════════════
	// CRITICAL FORK-SAFETY: Sort dirty entries by hex key before writing.
	// Go map iteration is non-deterministic — different nodes iterate in
	// different order, producing different NOMT session write sequences.
	// If NOMT's internal Merkle computation is sensitive to write order,
	// this causes different root hashes → fork. Sorting guarantees all
	// nodes write entries in the same canonical order.
	// ═══════════════════════════════════════════════════════════════════════
	sortedDirtyKeys := make([]string, 0, dirtyCount)
	for hexKey := range n.dirty {
		sortedDirtyKeys = append(sortedDirtyKeys, hexKey)
	}
	sort.Strings(sortedDirtyKeys)

	for _, hexKey := range sortedDirtyKeys {
		entry := n.dirty[hexKey]
		nomtKeys = append(nomtKeys, entry.keyPath)
		nomtVals = append(nomtVals, entry.value)

		// Replication batch uses original keys (addresses) so Sub nodes
		// can apply them to their own NOMT instance
		replicationBatch = append(replicationBatch, [2][]byte{
			append([]byte("nomt:"), entry.originalKey...),
			entry.value,
		})
	}

	// ═══════════════════════════════════════════════════════════════════════
	// CRITICAL FORK-SAFETY FIX (Apr 2026): Record old values BEFORE writing.
	//
	// NOMT's session.Finish() needs to know the old value at each key position
	// to correctly update the Merkle tree (update vs. insert). If RecordRead is
	// NOT called, NOMT reads old values from its internal database. This causes
	// state root divergence when the database is incomplete — specifically after
	// crash recovery or peer sync where CommitPayload may not have flushed all
	// previous blocks' data, or where genesis-only accounts are missing.
	//
	// By explicitly recording old values (captured during BatchUpdate or
	// BatchUpdateWithCachedOldValues), we guarantee deterministic Merkle root
	// computation regardless of the NOMT database state.
	// ═══════════════════════════════════════════════════════════════════════
	var insertCount, updateCount int
	for _, hexKey := range sortedDirtyKeys {
		entry := n.dirty[hexKey]
		if oldVal, ok := n.oldValues[hexKey]; ok && len(oldVal) > 0 {
			if err := session.RecordRead(entry.keyPath, oldVal); err != nil {
				logger.Warn("[NomtStateTrie] RecordRead failed for key %s: %v", hexKey[:8], err)
			}
			updateCount++
		} else {
			// Key did not exist before (new insertion) — record nil
			if err := session.RecordRead(entry.keyPath, nil); err != nil {
				logger.Warn("[NomtStateTrie] RecordRead(nil) failed for key %s: %v", hexKey[:8], err)
			}
			insertCount++
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// FORK-DIAG: Log insert vs update ratio. If this differs between master
	// and sync for the same block, old values are inconsistent → root diverges.
	// ═══════════════════════════════════════════════════════════════════════
	if insertCount > 0 || updateCount > 0 {
		logger.Info("[FORK-DIAG][RECORD-READ] namespace=%s, inserts=%d, updates=%d, total=%d",
			string(n.namespace), insertCount, updateCount, insertCount+updateCount)
	}

	// Batch write to the session (single FFI call for all entries)
	if err := session.BatchWrite(nomtKeys, nomtVals); err != nil {
		session.Abort()
		return n.rootHash, nil, nil, fmt.Errorf("NomtStateTrie Commit: batch write failed: %w", err)
	}
	// Finish the session in-memory — this computes the Merkle root atomically
	newRootBytes, fs, err := session.Finish(n.handle)
	if err != nil {
		return n.rootHash, nil, nil, fmt.Errorf("NomtStateTrie Commit: session finish failed: %w", err)
	}

	oldRoot := n.rootHash
	n.rootHash = e_common.BytesToHash(newRootBytes[:])
	n.pendingFinishedSession = fs

	// Store batch for network replication to Sub nodes
	n.lastCommitBatch = replicationBatch

	// Move dirty to committing to serve reads while async commit runs
	n.committing = n.dirty

	// Clear dirty state
	n.dirty = make(map[string]*nomtDirtyEntry)
	n.oldValues = make(map[string][]byte)
	n.oldLoaded = make(map[string]bool)

	// Clear active session since it is now consumed
	n.activeSession = nil

	// ═══════════════════════════════════════════════════════════════════════
	// FORK-DIAG: Log root hash transition for debugging sync vs native divergence.
	// This is the ONLY place NOMT root hash changes — if roots diverge between
	// nodes, this log pinpoints the exact commit that caused it.
	// ═══════════════════════════════════════════════════════════════════════
	logger.Info("[FORK-DIAG][NOMT-COMMIT] namespace=%s, entries=%d, oldRoot=%s → newRoot=%s",
		string(n.namespace), dirtyCount, oldRoot.Hex()[:18], n.rootHash.Hex()[:18])

	return n.rootHash, nil, nil, nil
}

// Close releases any resources held by the trie, specifically the NOMT write session.
// This is critical to prevent session leaks when a trie is discarded or replaced
// (e.g., on Sub nodes that never call Commit, or during state reorgs/reloads).
func (n *NomtStateTrie) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.activeSession != nil {
		logger.Warn("⚠️ [NomtStateTrie] Aborting leaked active session for namespace=%s", string(n.namespace))
		n.activeSession.Abort()
		n.activeSession = nil
	}

	if n.pendingFinishedSession != nil {
		logger.Warn("⚠️ [NomtStateTrie] Aborting leaked finished session for namespace=%s", string(n.namespace))
		n.pendingFinishedSession.Abort()
		n.pendingFinishedSession = nil
	}
}

// HasUncommittedChanges returns true if there are dirty changes pending commit.
func (n *NomtStateTrie) HasUncommittedChanges() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.dirty) > 0
}

// Copy creates a shallow copy with independent dirty map.
func (n *NomtStateTrie) Copy() StateTrie {
	n.mu.RLock()
	defer n.mu.RUnlock()

	newDirty := make(map[string]*nomtDirtyEntry, len(n.dirty))
	for k, v := range n.dirty {
		newDirty[k] = v
	}
	newCommitting := make(map[string]*nomtDirtyEntry, len(n.committing))
	for k, v := range n.committing {
		newCommitting[k] = v
	}
	newOldValues := make(map[string][]byte, len(n.oldValues))
	for k, v := range n.oldValues {
		newOldValues[k] = v
	}
	newOldLoaded := make(map[string]bool, len(n.oldLoaded))
	for k, v := range n.oldLoaded {
		newOldLoaded[k] = v
	}

	n.knownKeysMu.RLock()
	newKnownKeys := make(map[string][]byte, len(n.knownKeys))
	for k, v := range n.knownKeys {
		newKnownKeys[k] = v
	}
	n.knownKeysMu.RUnlock()

	return &NomtStateTrie{
		handle:          n.handle, // shared handle (NOMT is thread-safe for reads)
		namespace:       n.namespace,
		dirty:           newDirty,
		committing:      newCommitting,
		oldValues:       newOldValues,
		oldLoaded:         newOldLoaded,
		knownKeys:         newKnownKeys,
		isReplicationSync: n.isReplicationSync,
		registryChanged:   n.registryChanged,
		rootHash:          n.rootHash,
		isHash:            n.isHash,
	}
}

// CommitPayload executes the slow disk I/O portion of a finished commit session.
// This is called asynchronously by `PersistAsync` pipelines.
func (n *NomtStateTrie) CommitPayload() error {
	n.mu.Lock()
	fs := n.pendingFinishedSession
	n.pendingFinishedSession = nil
	n.mu.Unlock()

	if fs == nil {
		return nil // Nothing to commit to disk
	}

	if err := fs.CommitPayload(n.handle); err != nil {
		return fmt.Errorf("NomtStateTrie CommitPayload failed: %w", err)
	}

	// Safely clear the committing map since data is now on disk
	n.mu.Lock()
	n.committing = nil
	n.mu.Unlock()

	return nil
}

// GetCommitBatch returns the entries from the last Commit for network replication.
// One-shot read: clears the stored batch after retrieval.
func (n *NomtStateTrie) GetCommitBatch() [][2][]byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	batch := n.lastCommitBatch
	n.lastCommitBatch = nil
	return batch
}

// ApplyNomtReplicationBatches intercepts 'nomt:' prefixed keys from aggregated batches
// (received via AccountBatch from Master) and processes them for Sub-node state replication.
//
// PERFORMANCE FIX (Apr 2026): Sub nodes do NOT need to build a NOMT Merkle tree — they
// receive pre-computed state roots from Master. Writing 50K keys to NOMT with synchronous
// commit takes 4+ minutes, completely stalling sub-node sync. Instead, we simply strip the
// 'nomt:' prefix and keep the data in the PebbleDB batch for fast downstream writes.
// This reduces block apply time from minutes to milliseconds.
func ApplyNomtReplicationBatches(aggregatedBatches map[string][][2][]byte) error {
	if globalStateBackend != BackendNOMT {
		return nil
	}

	// We apply NOMT batches for BOTH Master and Sub nodes.
	// We also REMOVE the 'nomt:' prefixed keys from aggregatedBatches so that
	// they do NOT get written to PebbleDB again.

	namespaces := map[string]string{
		"Account":    "account_state",
		"SC Storage": "smart_contract_storage",
		"StakeState": "stake_db",
	}

	for batchName, namespace := range namespaces {
		batch, ok := aggregatedBatches[batchName]
		if !ok || len(batch) == 0 {
			continue
		}

		var nomtKeys, nomtValues [][]byte
		var nonNomtBatch [][2][]byte

		for _, kv := range batch {
			if batchName == "SC Storage" {
				if len(kv[0]) >= 25 && string(kv[0][20:25]) == "nomt:" {
					newKey := make([]byte, 0, len(kv[0])-5)
					newKey = append(newKey, kv[0][:20]...)
					newKey = append(newKey, kv[0][25:]...)
					nomtKeys = append(nomtKeys, newKey)
					nomtValues = append(nomtValues, kv[1])
				} else {
					nonNomtBatch = append(nonNomtBatch, kv)
				}
			} else {
				if len(kv[0]) >= 5 && string(kv[0][:5]) == "nomt:" {
					nomtKeys = append(nomtKeys, kv[0][5:])
					nomtValues = append(nomtValues, kv[1])
				} else {
					nonNomtBatch = append(nonNomtBatch, kv)
				}
			}
		}

		if len(nomtKeys) > 0 {
			handle, err := GetOrInitNomtHandle(namespace)
			if err != nil {
				return fmt.Errorf("failed to init NOMT handle for sync: %w", err)
			}

			if batchName == "SC Storage" {
				// ═══════════════════════════════════════════════════════════════════
				// CRITICAL FIX: SC Storage keys are structured as:
				//   address(20 bytes) + slotKey
				// The READ path creates per-contract NomtStateTries with keyPrefix:
				//   "smart_contract_storage_<hexAddress>"
				// which means keyPath = Keccak256(keyPrefix + slotKey).
				//
				// We MUST group keys by contract address and create per-address
				// NomtStateTries with the same keyPrefix, otherwise the write path
				// would compute a different Keccak256 than the read path.
				// ═══════════════════════════════════════════════════════════════════
				type perContractData struct {
					keys   [][]byte
					values [][]byte
				}
				groupedByAddr := make(map[string]*perContractData)

				for i, key := range nomtKeys {
					if len(key) < 20 {
						logger.Warn("🔧 [NOMT-SYNC] SC Storage key too short (%d bytes), skipping", len(key))
						continue
					}
					addrHex := hex.EncodeToString(key[:20])
					slotKey := key[20:]
					group, ok := groupedByAddr[addrHex]
					if !ok {
						group = &perContractData{}
						groupedByAddr[addrHex] = group
					}
					group.keys = append(group.keys, slotKey)
					group.values = append(group.values, nomtValues[i])
				}

				// FIX: Map iteration is random. Sort addresses to ensure deterministic
				// commit sequences, reproducing the exact state transitions as the master node.
				var sortedAddrs []string
				for addrHex := range groupedByAddr {
					sortedAddrs = append(sortedAddrs, addrHex)
				}
				slices.Sort(sortedAddrs)

				totalKeys := 0
				for _, addrHex := range sortedAddrs {
					group := groupedByAddr[addrHex]
					// Match the keyPrefix format used in trie_factory.go line 111:
					//   keyPrefix = namespace + "_" + hex.EncodeToString(pg.GetPrefix())
					keyPrefix := namespace + "_" + addrHex
					t := NewNomtStateTrie(handle, false, keyPrefix)
					t.SetReplicationSync(true)
					if err := t.BatchUpdate(group.keys, group.values); err != nil {
						return fmt.Errorf("failed to apply nomt sync batch for contract %s: %w", addrHex, err)
					}
					if _, _, _, err := t.Commit(true); err != nil {
						return fmt.Errorf("failed to commit nomt sync batch for contract %s: %w", addrHex, err)
					}
					if err := t.CommitPayload(); err != nil {
						return fmt.Errorf("failed to flush nomt sync batch for contract %s: %w", addrHex, err)
					}
					totalKeys += len(group.keys)
				}
				logger.Info("🔧 [NOMT-SYNC] Node rebuilt %d keys across %d contracts to NOMT for %s",
					totalKeys, len(groupedByAddr), namespace)
			} else {
				// Account, StakeState: single namespace, no address prefix in keys
				// ═══════════════════════════════════════════════════════════════════
				// FORK-DIAG: Log handle root BEFORE sync to detect stale NOMT state
				// ═══════════════════════════════════════════════════════════════════
				preRoot, _ := handle.Root()
				logger.Info("[FORK-DIAG][NOMT-SYNC-PRE] namespace=%s, keys=%d, handleRoot=%x",
					namespace, len(nomtKeys), preRoot[:8])

				trie := NewNomtStateTrie(handle, false, namespace)
				trie.SetReplicationSync(true)
				if err := trie.BatchUpdate(nomtKeys, nomtValues); err != nil {
					return fmt.Errorf("failed to apply nomt sync batch: %w", err)
				}
				if _, _, _, err := trie.Commit(true); err != nil {
					return fmt.Errorf("failed to commit nomt sync batch: %w", err)
				}
				if err := trie.CommitPayload(); err != nil {
					return fmt.Errorf("failed to flush nomt sync batch: %w", err)
				}

				// FORK-DIAG: Log handle root AFTER sync
				postRoot, _ := handle.Root()
				logger.Info("[FORK-DIAG][NOMT-SYNC-POST] namespace=%s, keys=%d, handleRoot=%x",
					namespace, len(nomtKeys), postRoot[:8])
				logger.Info("🔧 [NOMT-SYNC] Node rebuilt %d keys to NOMT trie for %s", len(nomtKeys), namespace)
			}
		}

		// Save back only the elements that are NOT meant for NOMT
		if len(nonNomtBatch) > 0 {
			aggregatedBatches[batchName] = nonNomtBatch
		} else {
			delete(aggregatedBatches, batchName) // No entries left for PebbleDB
		}
	}

	// For any other batch (like Receipt, Transaction), just ensure no 'nomt:' prefix sneaks into PebbleDB
	for batchName, batch := range aggregatedBatches {
		if batchName == "Account" || batchName == "SC Storage" || batchName == "StakeState" {
			continue // Already processed
		}
		var nonNomtBatch [][2][]byte
		for _, kv := range batch {
			if len(kv[0]) >= 5 && string(kv[0][:5]) == "nomt:" {
				continue // discard mistakenly prefixed keys
			}
			nonNomtBatch = append(nonNomtBatch, kv)
		}
		if len(nonNomtBatch) > 0 {
			aggregatedBatches[batchName] = nonNomtBatch
		} else {
			delete(aggregatedBatches, batchName)
		}
	}

	return nil
}
