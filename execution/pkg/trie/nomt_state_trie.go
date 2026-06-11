package trie

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
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

// ═══════════════════════════════════════════════════════════════════════════════
// ATOMIC POINTER SWAP ARCHITECTURE (Lock-Free Reads)
//
// PROBLEM: The old RWMutex design caused all 64 server workers to stall during
// Commit() because Commit() held n.mu.Lock() during slow FFI operations
// (BatchWrite + session.Finish = seconds), blocking all Get() calls that
// needed n.mu.RLock() for dirty/committing map lookups.
//
// SOLUTION: Split state into:
//   1. nomtReadView — immutable snapshot, swapped atomically (~1ns to read)
//   2. Writer-private state — only accessed by the single block-processor goroutine
//   3. Session state — protected by dedicated sessionMu (never held during FFI reads)
//
// INVARIANTS:
//   - readView is ALWAYS non-nil after construction
//   - readView.dirty and readView.committing maps are IMMUTABLE after Store()
//   - Writer creates new maps, never mutates published ones
//   - Only one writer (block processor) calls Update/BatchUpdate/Commit
// ═══════════════════════════════════════════════════════════════════════════════

// nomtReadView is an immutable snapshot of trie state for lock-free readers.
// Once stored via atomic.Pointer.Store(), the maps inside MUST NOT be mutated.
// Readers call atomic.Pointer.Load() (~1ns) to get a consistent view.
type nomtReadView struct {
	dirty      map[string]*nomtDirtyEntry // uncommitted changes (current block)
	committing map[string]*nomtDirtyEntry // being flushed to disk (previous block)
	rootHash   e_common.Hash              // last committed NOMT root
}

// NomtStateTrie implements StateTrie using NOMT as the backing store.
type NomtStateTrie struct {
	handle *nomt_ffi.Handle

	// namespace prefix for key isolation: different tries (AccountState, StakeState, etc.)
	// share the same NOMT database but use different namespace prefixes so their keys
	// don't collide. e.g. "acct:" for accounts, "stake:" for validators.
	namespace []byte

	// ─── LOCK-FREE READ PATH ────────────────────────────────────────────
	// Readers (Get, GetAll, Hash, HasUncommittedChanges) use atomic.Load()
	// which costs ~1ns and NEVER blocks, even during Commit() FFI operations.
	readView atomic.Pointer[nomtReadView]

	// ─── WRITER-PRIVATE STATE (protected by writerMu) ──────────────────
	// Only the block-processor goroutine accesses these fields.
	// writerMu serializes Update/BatchUpdate/Commit calls.
	writerMu  sync.RWMutex
	wDirty    map[string]*nomtDirtyEntry // live mutable dirty map
	wOldValues map[string][]byte         // pre-commit values for replication
	wOldLoaded map[string]bool           // tracks which old values were loaded

	// knownKeys tracks ALL original keys ever written to this trie instance.
	knownKeys   map[string][]byte
	knownKeysMu sync.RWMutex

	// ─── SESSION STATE (protected by sessionMu) ────────────────────────
	// Separate from writerMu to allow session drain without blocking writes.
	sessionMu              sync.Mutex
	sessionInitMu          sync.Mutex // Serializes session creation FFI
	activeSession          *nomt_ffi.Session
	pendingFinishedSession *nomt_ffi.FinishedSession

	// pendingChangelog holds changelog entries generated during Commit
	// to be asynchronously flushed to disk during CommitPayload
	pendingChangelog      []state_changelog.StateChange
	pendingChangelogBlock uint64
	pendingCommittingMap   map[string]*nomtDirtyEntry

	// lastCommitBatch for network replication (protected by writerMu)
	lastCommitBatch [][2][]byte

	isHash            bool
	isReplicationSync bool

	// tracks if knownKeys was modified since last commit
	registryChanged bool

	// Optional state changelog DB for historical queries
	changelogDB *state_changelog.StateChangelogDB

	// currentCommitBlock tracks the block number for the current commit
	currentCommitBlock uint64

	// commitWg waits for background CommitAsync operations
	commitWg sync.WaitGroup

	// asyncErr stores any error that occurred during background commits
	asyncErr atomic.Pointer[error]
}

type nomtDirtyEntry struct {
	originalKey []byte   // original 20-byte address
	keyPath     [32]byte // Keccak256(namespace + originalKey) — NOMT key
	value       []byte   // serialized account state
}

// Compile-time check: NomtStateTrie must implement StateTrie.
var _ StateTrie = (*NomtStateTrie)(nil)

// NomtSessionToFlush holds a finished session and its associated handle for deferred flushing.
type NomtSessionToFlush struct {
	Session *nomt_ffi.FinishedSession
	Handle  *nomt_ffi.Handle
}

// FlushNomtSessions performs disk I/O synchronously for a batch of sessions.
func FlushNomtSessions(sessions []NomtSessionToFlush) error {
	for _, s := range sessions {
		s.Handle.LockCommitPayload()
		err := s.Session.CommitPayload(s.Handle)
		s.Handle.UnlockCommitPayload()
		if err != nil {
			return err
		}
	}
	return nil
}

// nomtRegistryKeyPrefix is used to store the keys registry in NOMT.
// DEPRECATED: Registry is now stored in a separate file, NOT in the NOMT Merkle trie.
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
// DEPRECATED: Only used for backward-compatible loading from old NOMT data.
func registryKeyPath(namespace []byte) [32]byte {
	registryKey := []byte(nomtRegistryKeyPrefix)
	registryKey = append(registryKey, namespace...)
	return crypto.Keccak256Hash(registryKey)
}

// ═══════════════════════════════════════════════════════════════════════════════
// FILE-BASED REGISTRY — Persists knownKeys OUTSIDE the NOMT Merkle trie.
//
// The registry was previously stored as a special entry inside the NOMT trie,
// which caused persistent fork divergence because:
//  1. registryChanged flag is volatile in-memory state that diverges after restarts
//  2. Replication wrote registry at a different NOMT key path than native execution
//  3. Old-value tracking for registry depended on CommitPayload timing
//
// By storing the registry in a separate file, it has ZERO impact on the Merkle
// root computation, completely eliminating these sources of non-determinism.
// ═══════════════════════════════════════════════════════════════════════════════

// registryFilePath returns the filesystem path for storing a namespace's key registry.
func registryFilePath(handlePath string, namespace string) string {
	dir := filepath.Dir(handlePath)
	return filepath.Join(dir, "nomt_registry_"+namespace+".bin")
}

var (
	registryCacheMu sync.RWMutex
	registryCache   = make(map[string]map[string][]byte) // key: dbPath + ":" + namespace
)

// loadRegistryFromFile loads the knownKeys registry from a file or checks the memory cache first.
// Returns an empty map if the file doesn't exist or is corrupt.
func loadRegistryFromFile(handlePath string, namespace string) map[string][]byte {
	cacheKey := handlePath + ":" + namespace

	// Check memory cache first
	registryCacheMu.RLock()
	if cached, ok := registryCache[cacheKey]; ok {
		cloned := make(map[string][]byte, len(cached))
		for k, v := range cached {
			cloned[k] = v
		}
		registryCacheMu.RUnlock()
		return cloned
	}
	registryCacheMu.RUnlock()

	// Cache miss: load from disk
	knownKeys := make(map[string][]byte)
	filePath := registryFilePath(handlePath, namespace)

	data, err := os.ReadFile(filePath)
	if err != nil {
		// Cache the empty map so we don't keep doing failed reads
		registryCacheMu.Lock()
		registryCache[cacheKey] = knownKeys
		registryCacheMu.Unlock()
		return knownKeys // File not found or unreadable — start fresh
	}

	offset := 0
	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		keyLen := int(data[offset])
		offset++
		if offset+keyLen > len(data) {
			break
		}
		origKey := make([]byte, keyLen)
		copy(origKey, data[offset:offset+keyLen])
		offset += keyLen
		hexKey := hex.EncodeToString(origKey)
		knownKeys[hexKey] = origKey
	}

	// Update cache
	registryCacheMu.Lock()
	registryCache[cacheKey] = knownKeys
	registryCacheMu.Unlock()

	if len(knownKeys) > 0 {
		logger.Info("[NomtStateTrie] ✅ Loaded %d known keys from registry FILE (namespace=%s)", len(knownKeys), namespace)
	}
	return knownKeys
}

// updateRegistryCache updates the in-memory registry cache without writing to disk.
func (n *NomtStateTrie) updateRegistryCache() {
	n.knownKeysMu.RLock()
	defer n.knownKeysMu.RUnlock()

	dbPath := n.handle.GetPath()
	namespace := string(n.namespace)
	cacheKey := dbPath + ":" + namespace

	cloned := make(map[string][]byte, len(n.knownKeys))
	for k, v := range n.knownKeys {
		cloned[k] = v
	}

	registryCacheMu.Lock()
	registryCache[cacheKey] = cloned
	registryCacheMu.Unlock()
}

// persistRegistryToFile writes the knownKeys registry to a file and updates the memory cache.
// This is called after each Commit to ensure the registry is durable.
func (n *NomtStateTrie) persistRegistryToFile() {
	n.knownKeysMu.RLock()
	if len(n.knownKeys) == 0 {
		n.knownKeysMu.RUnlock()
		return
	}

	sortedKeys := make([]string, 0, len(n.knownKeys))
	for hexKey := range n.knownKeys {
		sortedKeys = append(sortedKeys, hexKey)
	}
	sort.Strings(sortedKeys)

	var data []byte
	for _, hexKey := range sortedKeys {
		origKey := n.knownKeys[hexKey]
		if len(origKey) > 255 {
			continue
		}
		data = append(data, byte(len(origKey)))
		data = append(data, origKey...)
	}
	n.knownKeysMu.RUnlock()

	filePath := registryFilePath(n.handle.GetPath(), string(n.namespace))
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		logger.Warn("[NomtStateTrie] Failed to persist registry to file %s: %v", filePath, err)
	}

	// Update the memory cache as well
	dbPath := n.handle.GetPath()
	namespace := string(n.namespace)
	cacheKey := dbPath + ":" + namespace

	cloned := make(map[string][]byte, len(sortedKeys))
	n.knownKeysMu.RLock()
	for k, v := range n.knownKeys {
		cloned[k] = v
	}
	n.knownKeysMu.RUnlock()

	registryCacheMu.Lock()
	registryCache[cacheKey] = cloned
	registryCacheMu.Unlock()
}

// RegisterKnownKey safely adds a key to the registry map.
// This is used for recovering missing validators from the authoritative epoch cache.
func (n *NomtStateTrie) RegisterKnownKey(key []byte) {
	n.knownKeysMu.Lock()
	defer n.knownKeysMu.Unlock()
	if n.knownKeys == nil {
		n.knownKeys = make(map[string][]byte)
	}
	hexKey := hex.EncodeToString(key)
	n.knownKeys[hexKey] = key
	logger.Debug("[NomtStateTrie] Registered recovered known key: %s", hexKey)
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

	skipRegistry := strings.HasPrefix(namespace, "smart_contract_storage") || namespace == "transaction_state" || namespace == "receipts"
	var knownKeys map[string][]byte

	if skipRegistry {
		knownKeys = make(map[string][]byte)
	} else {
		// ═══════════════════════════════════════════════════════════════════════
		// FORK-SAFE: Load registry from FILE first, fall back to NOMT for
		// backward compatibility with databases created before the file-based
		// registry fix.
		// ═══════════════════════════════════════════════════════════════════════
		knownKeys = loadRegistryFromFile(handle.GetPath(), namespace)

		// Backward compatibility: if no file-based registry, try loading from NOMT
		if len(knownKeys) == 0 {
			regKey := registryKeyPath([]byte(namespace))
			regData, found, readErr := handle.Read(regKey)
			if readErr == nil && found && len(regData) > 0 {
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
				logger.Info("[NomtStateTrie] ✅ Migrated %d known keys from NOMT registry to file (namespace=%s)", len(knownKeys), namespace)
			} else {
				logger.Info("[NomtStateTrie] ⚠️ No registry found for namespace=%s (fresh start)", namespace)
			}
		}
	}

	t := &NomtStateTrie{
		handle:            handle,
		namespace:         []byte(namespace),
		wDirty:            make(map[string]*nomtDirtyEntry),
		wOldValues:        make(map[string][]byte),
		wOldLoaded:        make(map[string]bool),
		knownKeys:         knownKeys,
		isHash:            isHash,
		isReplicationSync: false,
		registryChanged:   false,
	}
	// Initialize the atomic read view with empty maps and the current root hash.
	// This MUST be done before any Get() call.
	t.readView.Store(&nomtReadView{
		dirty:      make(map[string]*nomtDirtyEntry),
		committing: nil,
		rootHash:   rootHash,
	})
	return t
}

// publishReadView atomically publishes a new immutable snapshot for lock-free readers.
// The maps passed in MUST NOT be mutated after this call.
func (n *NomtStateTrie) publishReadView(dirty, committing map[string]*nomtDirtyEntry, rootHash e_common.Hash) {
	n.readView.Store(&nomtReadView{
		dirty:      dirty,
		committing: committing,
		rootHash:   rootHash,
	})
}

// loadReadView returns the current read view for lock-free access. ~1ns cost.
func (n *NomtStateTrie) loadReadView() *nomtReadView {
	return n.readView.Load()
}

// cloneDirtyMap creates a shallow copy of a dirty entry map.
// Used to produce immutable snapshots for readView — the cloned map
// will never be mutated after publishing, preventing concurrent map panics.
func cloneDirtyMap(src map[string]*nomtDirtyEntry) map[string]*nomtDirtyEntry {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]*nomtDirtyEntry, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// SetChangelogDB sets the state changelog database for historical querying.
func (n *NomtStateTrie) SetChangelogDB(db *state_changelog.StateChangelogDB) {
	n.writerMu.Lock()
	defer n.writerMu.Unlock()
	n.changelogDB = db
}

// GetChangelogDB gets the state changelog database for historical querying.
func (n *NomtStateTrie) GetChangelogDB() *state_changelog.StateChangelogDB {
	n.writerMu.RLock()
	defer n.writerMu.RUnlock()
	return n.changelogDB
}


// SetCurrentCommitBlock sets the block number for the upcoming commit.
func (n *NomtStateTrie) SetCurrentCommitBlock(blockNumber uint64) {
	n.writerMu.Lock()
	defer n.writerMu.Unlock()
	n.currentCommitBlock = blockNumber
}

// SetReplicationSync configures the trie to bypass registry tracking and modifications.
// This is critical during block synchronization (ApplyNomtReplicationBatches) where
// the registry keys are replicated directly via bytes, and tracking them dynamically
// would erroneously include the registry key itself inside the registry list,
// modifying its layout and causing divergent Merkle Roots.
func (n *NomtStateTrie) SetReplicationSync(syncMode bool) {
	n.writerMu.Lock()
	n.isReplicationSync = syncMode
	n.writerMu.Unlock()
}

// ═══════════════════════════════════════════════════════════════════════════════
// StateTrie interface implementation
// ═══════════════════════════════════════════════════════════════════════════════

// Get retrieves the value for a key from NOMT. O(1) via Beatree.
// LOCK-FREE: Uses atomic.Pointer.Load() (~1ns) instead of RWMutex.RLock().
// This eliminates the pipeline stall where Commit() held the write lock during
// slow FFI operations, blocking all concurrent Get() calls from server workers.
func (n *NomtStateTrie) Get(key []byte) ([]byte, error) {
	hexKey := hex.EncodeToString(key)

	n.writerMu.RLock()
	if entry, ok := n.wDirty[hexKey]; ok {
		n.writerMu.RUnlock()
		return entry.value, nil
	}
	n.writerMu.RUnlock()

	// Atomic load — ZERO contention, ~1ns, NEVER blocks
	view := n.loadReadView()

	// Check dirty first (current block's uncommitted changes)
	if view.dirty != nil {
		if entry, ok := view.dirty[hexKey]; ok {
			return entry.value, nil
		}
	}
	// Check committing (previous block's changes being flushed to disk)
	if view.committing != nil {
		if entry, ok := view.committing[hexKey]; ok {
			return entry.value, nil
		}
	}

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

// GetLockFree retrieves the value for a key from NOMT without taking any lock.
// This is safe to call concurrently even during Commit() because it bypasses writerMu.
func (n *NomtStateTrie) GetLockFree(key []byte) ([]byte, error) {
	hexKey := hex.EncodeToString(key)

	// Atomic load — ZERO contention, ~1ns, NEVER blocks
	view := n.loadReadView()

	// Check dirty first (current block's uncommitted changes)
	if view.dirty != nil {
		if entry, ok := view.dirty[hexKey]; ok {
			return entry.value, nil
		}
	}
	// Check committing (previous block's changes being flushed to disk)
	if view.committing != nil {
		if entry, ok := view.committing[hexKey]; ok {
			return entry.value, nil
		}
	}

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

// getNoLock retrieves a value without acquiring writerMu.RLock().
// MUST only be called when holding n.writerMu.Lock() or in single-threaded context.
func (n *NomtStateTrie) getNoLock(key []byte, hexKey string) ([]byte, error) {
	if entry, ok := n.wDirty[hexKey]; ok {
		return entry.value, nil
	}

	view := n.loadReadView()

	if view.dirty != nil {
		if entry, ok := view.dirty[hexKey]; ok {
			return entry.value, nil
		}
	}
	if view.committing != nil {
		if entry, ok := view.committing[hexKey]; ok {
			return entry.value, nil
		}
	}

	keyPath := addressToKeyPathWithNamespace(n.namespace, key)
	val, found, err := n.handle.Read(keyPath)
	if err != nil {
		return nil, fmt.Errorf("nomt read error for key %x: %w", key, err)
	}
	if !found {
		return nil, nil
	}
	return val, nil
}


// GetAll returns all key-value pairs by reading from the knownKeys registry.
// Each known key is fetched from NOMT individually.
// LOCK-FREE for dirty/committing access via atomic read view.
func (n *NomtStateTrie) GetAll() (map[string][]byte, error) {
	n.writerMu.RLock()
	wDirtySnapshot := make(map[string][]byte)
	for k, v := range n.wDirty {
		wDirtySnapshot[k] = v.originalKey
	}
	wDirtyValues := make(map[string][]byte)
	for k, v := range n.wDirty {
		wDirtyValues[k] = v.value
	}
	n.writerMu.RUnlock()

	// Atomic load — lock-free snapshot
	view := n.loadReadView()

	dirtySnapshot := make(map[string][]byte)
	if view.committing != nil {
		for hexKey, entry := range view.committing {
			dirtySnapshot[hexKey] = entry.value
		}
	}
	if view.dirty != nil {
		for hexKey, entry := range view.dirty {
			dirtySnapshot[hexKey] = entry.value
		}
	}

	// Merge dirty + knownKeys
	n.knownKeysMu.RLock()
	allHexKeys := make(map[string][]byte, len(n.knownKeys))
	for hexKey, origKey := range n.knownKeys {
		allHexKeys[hexKey] = origKey
	}
	n.knownKeysMu.RUnlock()

	// Also add keys from wDirty and view.dirty that bypass knownKeys tracking
	for hexKey, origKey := range wDirtySnapshot {
		allHexKeys[hexKey] = origKey
	}
	if view.dirty != nil {
		for hexKey, entry := range view.dirty {
			allHexKeys[hexKey] = entry.originalKey
		}
	}

	results := make(map[string][]byte, len(allHexKeys))

	for hexKey, origKey := range allHexKeys {
		// Check wDirty first
		if val, ok := wDirtyValues[hexKey]; ok {
			if len(val) > 0 {
				results[hexKey] = val
			}
			continue
		}
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
// PERFORMANCE FIX (May 2026): Old-value FFI read moved OUTSIDE writerMu.Lock().
// Previously, handle.Read() (50-200µs FFI call) was called under writer lock,
// blocking all concurrent Get() calls from 64 server workers. Now pre-fetched
// using lock-free readView + thread-safe handle.Read(), matching BatchUpdate() pattern.
func (n *NomtStateTrie) Update(key, value []byte) error {
	hexKey := hex.EncodeToString(key)
	keyPath := addressToKeyPathWithNamespace(n.namespace, key)

	// ═══════════════════════════════════════════════════════════════════════
	// Phase 1: PRE-FETCH old value OUTSIDE writerMu (NO lock contention)
	// handle.Read() is thread-safe. readView is lock-free atomic load (~1ns).
	// ═══════════════════════════════════════════════════════════════════════
	var prefetchedOldVal []byte
	var prefetchedLoaded bool

	// Quick check: if writer already loaded this key, skip prefetch entirely.
	// writerMu.RLock is cheap (~1ns) and doesn't block other readers.
	n.writerMu.RLock()
	alreadyLoaded := n.wOldLoaded[hexKey]
	// Also check if key is already in wDirty — if so, old value was captured
	// when the first Update() for this key ran, no need to prefetch again.
	_, alreadyDirty := n.wDirty[hexKey]
	n.writerMu.RUnlock()

	if !alreadyLoaded && !alreadyDirty {
		// Check committing from readView (lock-free, immutable snapshot)
		view := n.loadReadView()
		if view.committing != nil {
			if commEntry, ok := view.committing[hexKey]; ok {
				prefetchedOldVal = commEntry.value
				prefetchedLoaded = true
			}
		}
		if !prefetchedLoaded {
			// Thread-safe FFI read — NO lock held
			oldVal, found, err := n.handle.Read(keyPath)
			if err == nil && found && len(oldVal) > 0 {
				prefetchedOldVal = oldVal
			}
			prefetchedLoaded = true
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// Phase 2: ACQUIRE writerMu and mutate dirty map (fast, no FFI under lock)
	// ═══════════════════════════════════════════════════════════════════════
	n.writerMu.Lock()

	// Apply prefetched old value (only if not already loaded by a concurrent Update)
	if prefetchedLoaded && !n.wOldLoaded[hexKey] {
		if len(prefetchedOldVal) > 0 {
			n.wOldValues[hexKey] = prefetchedOldVal
		}
		n.wOldLoaded[hexKey] = true
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	n.wDirty[hexKey] = &nomtDirtyEntry{
		originalKey: keyCopy,
		keyPath:     keyPath,
		value:       value,
	}

	// Track key in knownKeys registry if not skipped
	skipRegistry := strings.HasPrefix(string(n.namespace), "smart_contract_storage") || string(n.namespace) == "transaction_state" || string(n.namespace) == "receipts"
	if !skipRegistry {
		n.knownKeysMu.Lock()
		if _, exists := n.knownKeys[hexKey]; !exists {
			n.knownKeys[hexKey] = keyCopy
			n.registryChanged = true
		}
		n.knownKeysMu.Unlock()
	}

	n.writerMu.Unlock()

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

	// CRITICAL CONCURRENCY FIX: Hold the write lock for the ENTIRE function
	// to prevent concurrent transaction groups from racing on Phase 1 reads vs Phase 2 writes.
	n.writerMu.Lock()
	defer n.writerMu.Unlock()

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

	// Restore high-performance parallel reads (dynamic CPU-based workers)
	numWorkers := runtime.NumCPU()
	if numWorkers > 32 {
		numWorkers = 32 // Cap at 32 to prevent excessive goroutine scheduling overhead
	}
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

				// Use getNoLock to avoid deadlocking since the main goroutine holds writerMu.Lock()
				var oldVal []byte
				var loaded bool
				val, getErr := n.getNoLock(keyCopy, hexKey)
				if getErr == nil {
					loaded = true
					if len(val) > 0 {
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
	skipRegistry := strings.HasPrefix(string(n.namespace), "smart_contract_storage") || string(n.namespace) == "transaction_state" || string(n.namespace) == "receipts"

	if !skipRegistry {
		n.knownKeysMu.Lock()
	}

	for i := 0; i < count; i++ {
		e := &entries[i]

		if !n.wOldLoaded[e.hexKey] {
			if e.oldLoaded {
				if e.oldValue != nil {
					n.wOldValues[e.hexKey] = e.oldValue
				}
				n.wOldLoaded[e.hexKey] = true
			}
		}

		n.wDirty[e.hexKey] = &nomtDirtyEntry{
			originalKey: e.originalKey,
			keyPath:     e.keyPath,
			value:       values[i],
		}

		if !skipRegistry {
			if _, exists := n.knownKeys[e.hexKey]; !exists {
				n.knownKeys[e.hexKey] = e.originalKey
				n.registryChanged = true
			}
		}
	}

	if !skipRegistry {
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

	// CRITICAL CONCURRENCY FIX: Hold the write lock for the ENTIRE function
	// to prevent concurrent transaction groups from racing on Phase 1 reads vs Phase 2 writes.
	n.writerMu.Lock()
	defer n.writerMu.Unlock()

	count := len(keys)

	// Phase 1: PARALLEL — compute key paths + pre-fetch old values if missing.
	// Safety Fallback: Use n.Get(key) in parallel to ensure deterministic wOldValues.
	type batchEntry struct {
		hexKey      string
		originalKey []byte
		keyPath     [32]byte
		oldValue    []byte
		oldLoaded   bool
	}
	entries := make([]batchEntry, count)

	// Restore high-performance parallel workers (dynamic CPU-based workers)
	numWorkers := runtime.NumCPU()
	if numWorkers > 32 {
		numWorkers = 32 // Cap at 32 to prevent excessive goroutine scheduling overhead
	}
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

				var oldValue []byte
				var oldLoaded bool

				// If the caller provided the cached old value, use it.
				// Otherwise, fall back to direct concurrent lookup.
				if oldValues != nil && i < len(oldValues) && len(oldValues[i]) > 0 {
					oldValue = oldValues[i]
					oldLoaded = true
				} else {
					// Use getNoLock to avoid deadlocking since the main goroutine holds writerMu.Lock()
					val, getErr := n.getNoLock(keyCopy, hex.EncodeToString(key))
					if getErr == nil {
						oldLoaded = true
						if len(val) > 0 {
							oldValue = val
						}
					}
				}

				entries[i] = batchEntry{
					hexKey:      hex.EncodeToString(key),
					originalKey: keyCopy,
					keyPath:     addressToKeyPathWithNamespace(n.namespace, key),
					oldValue:    oldValue,
					oldLoaded:   oldLoaded,
				}
			}
		}(start, end)
	}
	wg.Wait()

	// Phase 2: SEQUENTIAL — update dirty map + inject old values
	skipRegistry := strings.HasPrefix(string(n.namespace), "smart_contract_storage") || string(n.namespace) == "transaction_state" || string(n.namespace) == "receipts"

	if !skipRegistry {
		n.knownKeysMu.Lock()
	}

	for i := 0; i < count; i++ {
		e := &entries[i]

		if !n.wOldLoaded[e.hexKey] {
			if e.oldLoaded {
				if e.oldValue != nil {
					n.wOldValues[e.hexKey] = e.oldValue
				}
				n.wOldLoaded[e.hexKey] = true
			}
		}

		n.wDirty[e.hexKey] = &nomtDirtyEntry{
			originalKey: e.originalKey,
			keyPath:     e.keyPath,
			value:       values[i],
		}

		if !skipRegistry {
			if _, exists := n.knownKeys[e.hexKey]; !exists {
				n.knownKeys[e.hexKey] = e.originalKey
				n.registryChanged = true
			}
		}
	}

	if !skipRegistry {
		n.knownKeysMu.Unlock()
	}

	return nil
}

// getOrCreateSession ensures a session exists thread-safely.
// LOCK-FREE FAST PATH: Ensures PreWarm doesn't stall server workers
// when an async CommitPayload is performing slow disk I/O.
func (n *NomtStateTrie) getOrCreateSession() *nomt_ffi.Session {
	// Fast path check
	n.sessionMu.Lock()
	if n.activeSession != nil {
		s := n.activeSession
		n.sessionMu.Unlock()
		return s
	}
	n.sessionMu.Unlock()

	// Slow path: serialize initialization to prevent concurrent BeginSession FFI calls
	n.sessionInitMu.Lock()
	defer n.sessionInitMu.Unlock()

	n.sessionMu.Lock()
	if n.activeSession != nil {
		s := n.activeSession
		n.sessionMu.Unlock()
		return s
	}
	fs := n.pendingFinishedSession
	n.pendingFinishedSession = nil
	changes := n.pendingChangelog
	blockNum := n.pendingChangelogBlock
	n.pendingChangelog = nil
	n.pendingChangelogBlock = 0
	n.sessionMu.Unlock()

	// Perform slow FFI operations OUTSIDE sessionMu to avoid blocking the fast path
	if fs != nil || len(changes) > 0 {
		startDrain := time.Now()
		logger.Info("⏳ [NOMT-SYNC-DRAIN] Draining pendingFinishedSession synchronously before BeginSession (namespace=%s)", string(n.namespace))
		
		if fs != nil {
			n.handle.LockCommitPayload()
			err := fs.CommitPayload(n.handle)
			n.handle.UnlockCommitPayload()
			if err != nil {
				logger.Error("❌ [NOMT-SYNC-DRAIN] Failed to drain pendingFinishedSession (namespace=%s): %v", string(n.namespace), err)
			} else if string(n.namespace) == "account_state" && blockNum > 0 {
				storage.UpdateLastNomtCommittedBlock(blockNum)
			}
		}
		
		if n.changelogDB != nil && len(changes) > 0 {
			if err := n.changelogDB.WriteBlockChanges(blockNum, changes); err != nil {
				logger.Error("❌ [NOMT-SYNC-DRAIN] Failed to write changelog (namespace=%s): %v", string(n.namespace), err)
			}
		}
		
		logger.Info("✅ [NOMT-SYNC-DRAIN] Successfully drained pendingFinishedSession in %v (namespace=%s)", time.Since(startDrain), string(n.namespace))

		// Clear committing from readView since data is now on disk
		n.writerMu.Lock()
		view := n.loadReadView()
		n.publishReadView(view.dirty, nil, view.rootHash)
		n.writerMu.Unlock()
	}

	newSession := nomt_ffi.BeginSession(n.handle)

	n.sessionMu.Lock()
	n.activeSession = newSession
	n.sessionMu.Unlock()

	return newSession
}

func (n *NomtStateTrie) PreWarm(keys [][]byte) {
	// PreWarm is a no-op for NomtStateTrie.
	// We read directly from the handle in parallel during BatchUpdate,
	// which sufficiently warms up the page cache.
	// Bypassing PreWarm prevents session leaks on copies created for PreloadAccounts,
	// which caused deadlocks during CloseForSnapshot().
}

// Hash returns the current root hash.
// LOCK-FREE: reads from atomic readView.
func (n *NomtStateTrie) Hash() e_common.Hash {
	return n.loadReadView().rootHash
}

// RealignRoot updates the in-memory root hash to match the C++ NOMT engine's
// authoritative state after a DB fast-forward (e.g., P2P block sync via
// HandleSyncBlocksRequest). This is O(1) — no FFI calls, no file I/O.
//
// WHEN TO USE: After NOMT C++ has been updated via ApplyNomtReplicationBatches
// or direct CommitPayload, and Go's readView.rootHash is stale.
//
// WHEN NOT TO USE: During normal consensus execution — Commit() already
// updates rootHash atomically.
//
// FORK-SAFETY: This ONLY updates the in-memory view. It does NOT modify
// the C++ NOMT state. If the expectedRoot doesn't match the actual C++ handle
// root, subsequent Commit() calls will compute the correct root from dirty state.
func (n *NomtStateTrie) RealignRoot(expectedRoot e_common.Hash) {
	n.writerMu.Lock()
	view := n.loadReadView()
	oldRoot := view.rootHash
	n.publishReadView(view.dirty, view.committing, expectedRoot)
	n.writerMu.Unlock()
	if oldRoot != expectedRoot {
		logger.Info("🔧 [NOMT-REALIGN] namespace=%s, root %s → %s",
			string(n.namespace), oldRoot.Hex()[:18], expectedRoot.Hex()[:18])
	}
}

// AlignWithExpectedRoot verifies if the C++ NOMT Merkle root matches the expected root hash.
// If it does not match, it resets the database, reads all keys/values from storage/changelog,
// inserts them, and commits the state to align the C++ engine root with the consensus root.
func (n *NomtStateTrie) AlignWithExpectedRoot(storage storage.Storage, expectedRoot e_common.Hash, blockNumber uint64) error {
	nomtRootBytes, err := n.handle.Root()
	var currentRoot e_common.Hash
	if err == nil {
		currentRoot = e_common.BytesToHash(nomtRootBytes[:])
	}

	if currentRoot == expectedRoot {
		// Roots are already aligned. Just ensure Go's readView matches.
		n.RealignRoot(expectedRoot)
		return nil
	}

	logger.Warn("🔧 [NOMT-ALIGN-REBUILD] NOMT C++ root %s != expected %s. Rebuilding NOMT trie from storage for namespace %s...",
		currentRoot.Hex()[:18], expectedRoot.Hex()[:18], string(n.namespace))

	// 1. Retrieve all keys/values.
	// We attempt to fetch the state from historical changelogDB first to get the absolute source of truth.
	var kvs [][2][]byte
	var retrievedFromChangelog bool

	if n.changelogDB != nil {
		addresses, err := n.changelogDB.GetAllUniqueAddresses()
		if err == nil && len(addresses) > 0 {
			for _, addr := range addresses {
				val, err := n.changelogDB.GetStateAt(addr, blockNumber)
				if err == nil && len(val) > 0 {
					kvs = append(kvs, [2][]byte{addr, val})
				}
			}
			if len(kvs) > 0 {
				retrievedFromChangelog = true
				logger.Info("🔧 [NOMT-ALIGN-REBUILD] Retrieved %d keys/values from StateChangelogDB for block #%d to align namespace %s", len(kvs), blockNumber, string(n.namespace))
			}
		}
	}

	if !retrievedFromChangelog {
		// Fallback to legacy retrieval from registry + storage/handle (if changelog is disabled or empty)
		knownKeys := loadRegistryFromFile(n.handle.GetPath(), string(n.namespace))
		n.knownKeysMu.RLock()
		for hexKey, origKey := range n.knownKeys {
			keyCopy := make([]byte, len(origKey))
			copy(keyCopy, origKey)
			knownKeys[hexKey] = keyCopy
		}
		n.knownKeysMu.RUnlock()

		var readFromStorageCount, readFromHandleCount int
		for _, origKey := range knownKeys {
			// First attempt to read the key-value from the persistent flat DB (storage)
			// as it represents the true committed state, bypassing any C++ handle corruption.
			storageKey := append([]byte("nomt:"), origKey...)
			val, readErr := storage.Get(storageKey)
			if readErr == nil && len(val) > 0 {
				kvs = append(kvs, [2][]byte{origKey, val})
				readFromStorageCount++
			} else {
				// Fallback to reading from the old C++ handle if not found in storage
				keyPath := addressToKeyPathWithNamespace(n.namespace, origKey)
				val, found, readErr := n.handle.Read(keyPath)
				if readErr == nil && found && len(val) > 0 {
					kvs = append(kvs, [2][]byte{origKey, val})
					readFromHandleCount++
				}
			}
		}

		// Fallback to prefix scan from shared storage if no keys retrieved from registry
		if len(kvs) == 0 {
			var scanErr error
			// Scan only 'nomt:' prefixed keys which yields exact key-value pairs with 'nomt:' prefix stripped.
			kvs, scanErr = storage.PrefixScan([]byte("nomt:"))
			if scanErr != nil {
				return fmt.Errorf("failed to scan keys from storage: %w", scanErr)
			}
			if len(kvs) > 0 {
				logger.Info("🔧 [NOMT-ALIGN-REBUILD] Retrieved %d keys/values from storage PrefixScan for namespace %s", len(kvs), string(n.namespace))
			}
		} else {
			logger.Info("🔧 [NOMT-ALIGN-REBUILD] Retrieved %d keys/values (storage=%d, handle=%d) for rebuilding namespace %s",
				len(kvs), readFromStorageCount, readFromHandleCount, string(n.namespace))
		}
	}

	// Lock session creation to prevent concurrent BeginSession FFI calls during reset,
	// and abort any active/pending sessions that were bound to the old handle.
	n.sessionInitMu.Lock()
	n.sessionMu.Lock()
	if n.activeSession != nil {
		logger.Warn("⚠️ [NomtStateTrie] Aborting active session before ResetNomtHandle for namespace=%s", string(n.namespace))
		n.activeSession.Abort()
		n.activeSession = nil
	}
	
	fsToAbort := n.pendingFinishedSession
	n.pendingFinishedSession = nil
	n.sessionMu.Unlock()

	if fsToAbort != nil {
		logger.Warn("⚠️ [NomtStateTrie] Aborting finished session before ResetNomtHandle for namespace=%s", string(n.namespace))
		// ═══════════════════════════════════════════════════════════════════════════
		// CRITICAL DOUBLE FREE FIX:
		// We MUST acquire LockCommitPayload before aborting the pending finished session.
		// If PersistAsync() spawned a CommitAsync() goroutine, it holds LockCommitPayload()
		// while executing C.nomt_commit_payload. If we call fs.Abort() concurrently, it
		// will execute C.nomt_finished_session_abort on the exact same session pointer!
		// This concurrent C++ access caused the "double free or corruption (out)" crash.
		// ═══════════════════════════════════════════════════════════════════════════
		n.handle.LockCommitPayload()
		fsToAbort.Abort()
		n.handle.UnlockCommitPayload()
	}

	// 2. Reset the NOMT handle (closes, wipes, and reopens)
	newHandle, err := ResetNomtHandle(string(n.namespace))
	if err != nil {
		n.sessionInitMu.Unlock()
		return fmt.Errorf("failed to reset NOMT handle: %w", err)
	}

	// 3. Update handle and reset internal trie state under writerMu
	n.writerMu.Lock()
	n.handle = newHandle
	n.wDirty = make(map[string]*nomtDirtyEntry)
	n.wOldValues = make(map[string][]byte)
	n.wOldLoaded = make(map[string]bool)
	n.knownKeysMu.Lock()
	n.knownKeys = make(map[string][]byte)
	for _, kv := range kvs {
		n.knownKeys[hex.EncodeToString(kv[0])] = kv[0]
	}
	n.knownKeysMu.Unlock()
	n.registryChanged = true
	n.pendingChangelog = nil
	n.pendingChangelogBlock = 0
	n.pendingCommittingMap = nil

	// Reset readView to empty state
	n.readView.Store(&nomtReadView{
		dirty:      make(map[string]*nomtDirtyEntry),
		committing: nil,
		rootHash:   e_common.Hash{},
	})
	n.writerMu.Unlock()

	// 4. Save registry to file
	n.persistRegistryToFile()

	// 5. Populate keys/values into NOMT
	if len(kvs) > 0 {
		// Begin the session for rebuild while still holding sessionInitMu
		rebuildSession := nomt_ffi.BeginSession(newHandle)
		n.sessionMu.Lock()
		n.activeSession = rebuildSession
		n.sessionMu.Unlock()

		n.sessionInitMu.Unlock()

		keys := make([][]byte, len(kvs))
		values := make([][]byte, len(kvs))
		for i, kv := range kvs {
			keys[i] = kv[0]
			values[i] = kv[1]
		}

		// BatchUpdate will acquire n.writerMu
		if err := n.BatchUpdate(keys, values); err != nil {
			n.sessionMu.Lock()
			if n.activeSession == rebuildSession {
				rebuildSession.Abort()
				n.activeSession = nil
			}
			n.sessionMu.Unlock()
			return fmt.Errorf("failed to batch update in rebuilt trie: %w", err)
		}

		newRoot, _, _, err := n.Commit(true)
		if err != nil {
			return fmt.Errorf("failed to commit rebuilt trie: %w", err)
		}

		if err := n.CommitPayload(); err != nil {
			return fmt.Errorf("failed to commit payload for rebuilt trie: %w", err)
		}

		if newRoot != expectedRoot {
			logger.Error("🚨 [NOMT-ALIGN-REBUILD] CRITICAL: Rebuilt NOMT root %s still differs from expected root %s!",
				newRoot.Hex()[:18], expectedRoot.Hex()[:18])
			return fmt.Errorf("rebuilt root mismatch: got %s, expected %s", newRoot.Hex(), expectedRoot.Hex())
		}
		logger.Info("✅ [NOMT-ALIGN-REBUILD] Rebuilt NOMT successfully aligned to expected root: %s", expectedRoot.Hex()[:18])
	} else {
		n.sessionInitMu.Unlock()

		// Empty database
		n.writerMu.Lock()
		n.publishReadView(make(map[string]*nomtDirtyEntry), nil, e_common.Hash{})
		n.writerMu.Unlock()
		if expectedRoot != (e_common.Hash{}) && expectedRoot != EmptyRootHash {
			return fmt.Errorf("expected non-empty root %s but storage has 0 keys", expectedRoot.Hex())
		}
		logger.Info("✅ [NOMT-ALIGN-REBUILD] Wiped NOMT successfully aligned to empty root")
	}

	return nil
}


// Commit finalizes changes: writes all dirty entries to NOMT via batch session.
// Returns (rootHash, nil, nil, nil) — NOMT handles its own node management internally.
//
// ═══════════════════════════════════════════════════════════════════════════════
// ATOMIC POINTER SWAP COMMIT (3-Phase Lock-Free Architecture)
//
// Phase 1 (writerMu, ~microseconds): Snapshot wDirty → frozen copy, publish
//   readView with committing = frozen, clear wDirty. Grab session reference.
// Phase 2 (NO LOCK, ~milliseconds-seconds): Expensive FFI operations:
//   RecordRead, BatchWrite, session.Finish. Readers are NEVER blocked.
// Phase 3 (writerMu, ~microseconds): Update rootHash, publish final readView,
//   clear committing, store session state.
//
// This eliminates the pipeline stall where Commit() held n.mu.Lock() during
// slow FFI operations, blocking all 64 server workers via Get() → RLock().
// ═══════════════════════════════════════════════════════════════════════════════
func (n *NomtStateTrie) Commit(collectLeaf bool) (e_common.Hash, *node.NodeSet, [][]byte, error) {
	// ═══════════════════════════════════════════════════════════════════════
	// PHASE 1: Snapshot dirty state under writerMu (microseconds)
	// ═══════════════════════════════════════════════════════════════════════
	n.writerMu.Lock()

	if len(n.wDirty) == 0 {
		rootHash := n.loadReadView().rootHash
		n.writerMu.Unlock()
		return rootHash, nil, nil, nil
	}

	// Freeze the dirty map — this becomes the immutable "committing" snapshot.
	// Create a new wDirty for any concurrent Update() calls during Phase 2.
	committingSnapshot := n.wDirty
	oldValuesSnapshot := n.wOldValues
	n.wDirty = make(map[string]*nomtDirtyEntry)
	n.wOldValues = make(map[string][]byte)
	n.wOldLoaded = make(map[string]bool)

	// Publish readView: readers see committing entries via lock-free atomic load.
	// wDirty is empty, so readView.dirty is empty (new writes go to fresh wDirty).
	currentRoot := n.loadReadView().rootHash
	n.publishReadView(
		make(map[string]*nomtDirtyEntry), // empty dirty (fresh block)
		committingSnapshot,                // frozen previous dirty
		currentRoot,                       // root unchanged until Phase 3
	)

	n.writerMu.Unlock()
	// ← writerMu released! Get() calls from server workers proceed instantly.

	// Grab session (thread-safe, handles its own lock-free fast path)
	session := n.getOrCreateSession()

	if session == nil {
		return currentRoot, nil, nil, fmt.Errorf("NomtStateTrie: failed to begin session")
	}

	// ═══════════════════════════════════════════════════════════════════════
	// PHASE 2: Expensive FFI operations (NO LOCK — readers never blocked)
	// ═══════════════════════════════════════════════════════════════════════

	// FORK-SAFE: Registry is NO LONGER injected into the dirty map.

	// Build key/value arrays for batch write
	dirtyCount := len(committingSnapshot)
	nomtKeys := make([][32]byte, 0, dirtyCount)
	nomtVals := make([][]byte, 0, dirtyCount)
	replicationBatch := make([][2][]byte, 0, dirtyCount)

	// CRITICAL FORK-SAFETY: Sort dirty entries by hex key for deterministic order.
	sortedDirtyKeys := make([]string, 0, dirtyCount)
	for hexKey := range committingSnapshot {
		sortedDirtyKeys = append(sortedDirtyKeys, hexKey)
	}
	sort.Strings(sortedDirtyKeys)

	// FORK-DIAG: Hash the dirty snapshot + old values for cross-node comparison
	if strings.HasPrefix(string(n.namespace), "smart_contract_storage") && len(sortedDirtyKeys) > 0 {
		diagHash := sha256.New()
		for _, hexKey := range sortedDirtyKeys {
			entry := committingSnapshot[hexKey]
			diagHash.Write([]byte(hexKey))
			diagHash.Write(entry.value)
			if oldVal, ok := oldValuesSnapshot[hexKey]; ok {
				diagHash.Write(oldVal)
			}
		}
		digestHex := hex.EncodeToString(diagHash.Sum(nil)[:16])
		logger.Warn("[FORK-DIAG] Commit namespace=%s dirtyCount=%d oldCount=%d digest=%s",
			string(n.namespace), len(committingSnapshot), len(oldValuesSnapshot), digestHex)

		// FORK-DIAG: Per-key fingerprints for pinpointing exact divergence
		for i, hexKey := range sortedDirtyKeys {
			entry := committingSnapshot[hexKey]
			newHash := sha256.Sum256(entry.value)
			newFP := hex.EncodeToString(newHash[:8])
			keyPathHex := hex.EncodeToString(entry.keyPath[:8])

			oldFP := "nil"
			oldLen := 0
			if oldVal, ok := oldValuesSnapshot[hexKey]; ok && len(oldVal) > 0 {
				oldHash := sha256.Sum256(oldVal)
				oldFP = hex.EncodeToString(oldHash[:8])
				oldLen = len(oldVal)
			}

			logger.Warn("[FORK-DIAG][KEY-%d] ns=%s key=%s kpath=%s oldFP=%s(%d) newFP=%s(%d)",
				i, string(n.namespace), hexKey[:16], keyPathHex,
				oldFP, oldLen, newFP, len(entry.value))
		}
	}

	for _, hexKey := range sortedDirtyKeys {
		entry := committingSnapshot[hexKey]
		nomtKeys = append(nomtKeys, entry.keyPath)
		nomtVals = append(nomtVals, entry.value)

		replicationBatch = append(replicationBatch, [2][]byte{
			append([]byte("nomt:"), entry.originalKey...),
			entry.value,
		})
	}

	// CRITICAL FORK-SAFETY FIX: Record old values BEFORE writing.
	// We optimize this by batching all record reads to minimize FFI boundary crossing overhead.
	nomtReadKeys := make([][32]byte, 0, dirtyCount)
	nomtReadVals := make([][]byte, 0, dirtyCount)
	var insertCount, updateCount int
	for _, hexKey := range sortedDirtyKeys {
		entry := committingSnapshot[hexKey]
		nomtReadKeys = append(nomtReadKeys, entry.keyPath)
		if oldVal, ok := oldValuesSnapshot[hexKey]; ok && len(oldVal) > 0 {
			nomtReadVals = append(nomtReadVals, oldVal)
			updateCount++
		} else {
			nomtReadVals = append(nomtReadVals, nil)
			insertCount++
		}
	}

	if len(nomtReadKeys) > 0 {
		if err := session.BatchRecordRead(nomtReadKeys, nomtReadVals); err != nil {
			logger.Warn("[NomtStateTrie] BatchRecordRead failed: %v", err)
		}
	}

	if insertCount > 0 || updateCount > 0 {
		logger.Info("[FORK-DIAG][RECORD-READ] namespace=%s, inserts=%d, updates=%d, total=%d",
			string(n.namespace), insertCount, updateCount, insertCount+updateCount)
	}

	// Batch write to the session (single FFI call for all entries)
	if err := session.BatchWrite(nomtKeys, nomtVals); err != nil {
		session.Abort()
		return currentRoot, nil, nil, fmt.Errorf("NomtStateTrie Commit: batch write failed: %w", err)
	}
	// Finish the session in-memory — computes the Merkle root atomically
	newRootBytes, fs, err := session.Finish(n.handle)
	if err != nil {
		return currentRoot, nil, nil, fmt.Errorf("NomtStateTrie Commit: session finish failed: %w", err)
	}

	newRoot := e_common.BytesToHash(newRootBytes[:])

	// STATE CHANGELOG
	var changes []state_changelog.StateChange
	if n.changelogDB != nil && n.currentCommitBlock > 0 {
		for _, hexKey := range sortedDirtyKeys {
			entry := committingSnapshot[hexKey]
			changes = append(changes, state_changelog.StateChange{
				Key:      entry.originalKey,
				OldValue: oldValuesSnapshot[hexKey],
				NewValue: entry.value,
			})
		}
		// ═══════════════════════════════════════════════════════════════
		// FIX: Move changelog disk I/O out of the execution critical path.
		// Queue the changes to be asynchronously flushed to disk during CommitPayload.
		// ═══════════════════════════════════════════════════════════════
	}

	// ═══════════════════════════════════════════════════════════════════════
	// PHASE 3: Publish new root hash atomically (writerMu, ~microseconds)
	// ═══════════════════════════════════════════════════════════════════════
	n.writerMu.Lock()

	// Store session state
	n.sessionMu.Lock()
	n.pendingFinishedSession = fs
	n.activeSession = nil
	if len(changes) > 0 {
		n.pendingChangelog = changes
		n.pendingChangelogBlock = n.currentCommitBlock
	}
	n.pendingCommittingMap = committingSnapshot
	n.sessionMu.Unlock()

	n.lastCommitBatch = replicationBatch

	// Publish final readView: new rootHash, committing still visible for reads
	// until CommitPayload() flushes to disk and clears it.
	// NOTE: We keep committingSnapshot in readView so Get() can still serve
	// these values during the async disk flush. CommitPayload() will clear it.
	//
	// CRITICAL: We must clone wDirty before publishing — readView maps are
	// immutable after Store(). Publishing the live wDirty pointer would allow
	// concurrent readers to see writer mutations → panic on concurrent map access.
	frozenDirty := cloneDirtyMap(n.wDirty)
	n.publishReadView(
		frozenDirty,         // immutable clone of current writer dirty
		committingSnapshot,  // keep serving committed entries until disk flush
		newRoot,             // new Merkle root
	)

	logger.Info("[FORK-DIAG][NOMT-COMMIT] namespace=%s, entries=%d, oldRoot=%s → newRoot=%s",
		string(n.namespace), dirtyCount, currentRoot.Hex()[:18], newRoot.Hex()[:18])

	// FORK-SAFE: Persist knownKeys to file OUTSIDE the Merkle trie.
	if n.registryChanged {
		n.updateRegistryCache()
		n.persistRegistryToFile()
		n.registryChanged = false
	}

	n.writerMu.Unlock()

	return newRoot, nil, nil, nil
}

// Close releases any resources held by the trie, specifically the NOMT write session.
// This is critical to prevent session leaks when a trie is discarded or replaced
// (e.g., on Sub nodes that never call Commit, or during state reorgs/reloads).
func (n *NomtStateTrie) Close() {
	n.sessionMu.Lock()
	defer n.sessionMu.Unlock()

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

	n.pendingChangelog = nil
	n.pendingCommittingMap = nil
}

// HasUncommittedChanges returns true if there are dirty changes pending commit.
// LOCK-FREE: reads from atomic readView and safely checks writer dirty under RLock.
func (n *NomtStateTrie) HasUncommittedChanges() bool {
	n.writerMu.RLock()
	hasWDirty := len(n.wDirty) > 0
	n.writerMu.RUnlock()
	
	if hasWDirty {
		return true
	}

	view := n.loadReadView()
	return len(view.dirty) > 0 || len(view.committing) > 0
}

// Copy creates a shallow copy with independent dirty map.
// Used by PreloadAccounts for thread-safe parallel reads.
func (n *NomtStateTrie) Copy() StateTrie {
	// Read immutable view for dirty/committing
	view := n.loadReadView()

	newDirty := make(map[string]*nomtDirtyEntry, len(view.dirty))
	if view.dirty != nil {
		for k, v := range view.dirty {
			newDirty[k] = v
		}
	}
	newCommitting := make(map[string]*nomtDirtyEntry)
	if view.committing != nil {
		for k, v := range view.committing {
			newCommitting[k] = v
		}
	}

	// Copy writer-private state under writerMu
	n.writerMu.Lock()
	newOldValues := make(map[string][]byte, len(n.wOldValues))
	for k, v := range n.wOldValues {
		newOldValues[k] = v
	}
	newOldLoaded := make(map[string]bool, len(n.wOldLoaded))
	for k, v := range n.wOldLoaded {
		newOldLoaded[k] = v
	}
	n.writerMu.Unlock()

	n.knownKeysMu.RLock()
	newKnownKeys := make(map[string][]byte, len(n.knownKeys))
	for k, v := range n.knownKeys {
		newKnownKeys[k] = v
	}
	n.knownKeysMu.RUnlock()

	t := &NomtStateTrie{
		handle:            n.handle, // shared handle (NOMT is thread-safe for reads)
		namespace:         n.namespace,
		wDirty:            newDirty,
		wOldValues:        newOldValues,
		wOldLoaded:        newOldLoaded,
		knownKeys:         newKnownKeys,
		isReplicationSync: n.isReplicationSync,
		registryChanged:   n.registryChanged,
		isHash:            n.isHash,
	}
	// Initialize readView for the copy
	t.readView.Store(&nomtReadView{
		dirty:      newDirty,
		committing: newCommitting,
		rootHash:   view.rootHash,
	})
	return t
}

func (n *NomtStateTrie) CommitPayload() error {
	n.handle.LockCommitPayload()
	defer n.handle.UnlockCommitPayload()

	n.sessionMu.Lock()
	fs := n.pendingFinishedSession
	n.pendingFinishedSession = nil
	changes := n.pendingChangelog
	blockNum := n.pendingChangelogBlock
	n.pendingChangelog = nil
	n.pendingChangelogBlock = 0
	committingMap := n.pendingCommittingMap
	n.pendingCommittingMap = nil
	n.sessionMu.Unlock()

	if fs == nil && len(changes) == 0 {
		return nil // Nothing to commit to disk
	}

	if fs != nil {
		if err := fs.CommitPayload(n.handle); err != nil {
			return fmt.Errorf("NomtStateTrie CommitPayload failed: %w", err)
		}
		if string(n.namespace) == "account_state" && blockNum > 0 {
			storage.UpdateLastNomtCommittedBlock(blockNum)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// ASYNC CHANGELOG FLUSH
	// ═══════════════════════════════════════════════════════════════
	if n.changelogDB != nil && len(changes) > 0 {
		if err := n.changelogDB.WriteBlockChanges(blockNum, changes); err != nil {
			logger.Error("[NomtStateTrie] Failed to write to StateChangelogDB asynchronously: %v", err)
		}
	}

	// Data is now on disk. Clear committing from readView so Get() reads
	// directly from NOMT. This is safe because:
	// 1. writerMu prevents concurrent Commit Phase 1/3 from racing
	// 2. We re-read the latest readView under writerMu to avoid clobbering
	//    a newer commit's readView that may have been published during disk flush
	n.writerMu.Lock()
	view := n.loadReadView()
	if view.committing != nil && committingMap != nil &&
		reflect.ValueOf(view.committing).Pointer() == reflect.ValueOf(committingMap).Pointer() {
		n.publishReadView(
			view.dirty,    // preserve current dirty (already immutable)
			nil,           // committing cleared — data is on disk
			view.rootHash, // rootHash unchanged
		)
	} else if committingMap == nil {
		n.publishReadView(
			view.dirty,
			nil,
			view.rootHash,
		)
	}
	n.writerMu.Unlock()

	return nil
}

// NomtPayload holds the extracted pending finished session and changelog
// to be committed asynchronously.
type NomtPayload struct {
	trie          *NomtStateTrie
	fs            *nomt_ffi.FinishedSession
	changes       []state_changelog.StateChange
	blockNum      uint64
	committingMap interface{}
	doneOnce      sync.Once
}

// Discard decrements the WaitGroup counter. Safe for concurrent/repeated calls.
func (p *NomtPayload) Discard() {
	if p == nil {
		return
	}
	p.doneOnce.Do(func() {
		p.trie.commitWg.Done()
	})
}

// ExtractPendingPayload extracts the pending payload synchronously.
func (n *NomtStateTrie) ExtractPendingPayload() *NomtPayload {
	n.sessionMu.Lock()
	defer n.sessionMu.Unlock()

	fs := n.pendingFinishedSession
	n.pendingFinishedSession = nil
	changes := n.pendingChangelog
	blockNum := n.pendingChangelogBlock
	n.pendingChangelog = nil
	n.pendingChangelogBlock = 0
	committingMap := n.pendingCommittingMap
	n.pendingCommittingMap = nil

	if fs == nil && len(changes) == 0 {
		return nil
	}

	n.commitWg.Add(1)

	payload := &NomtPayload{
		trie:          n,
		fs:            fs,
		changes:       changes,
		blockNum:      blockNum,
		committingMap: committingMap,
	}

	runtime.SetFinalizer(payload, func(p *NomtPayload) {
		p.Discard()
	})

	return payload
}

// WriteChangelog writes the changes to the StateChangelogDB synchronously.
func (p *NomtPayload) WriteChangelog() {
	if p == nil || len(p.changes) == 0 || p.trie.changelogDB == nil {
		return
	}
	if err := p.trie.changelogDB.WriteBlockChanges(p.blockNum, p.changes); err != nil {
		logger.Error("[NomtStateTrie] Failed to write to StateChangelogDB: %v", err)
	}
}

// CommitAsync flushes the extracted payload to disk asynchronously in a background goroutine.
func (p *NomtPayload) CommitAsync() {
	if p == nil {
		return
	}
	if p.fs == nil && p.committingMap == nil {
		p.Discard()
		return
	}

	go func() {
		defer p.Discard()
		
		p.trie.handle.LockCommitPayload()
		defer p.trie.handle.UnlockCommitPayload()

		if p.fs != nil {
			if err := p.fs.CommitPayload(p.trie.handle); err != nil {
				logger.Error("[NomtStateTrie] CommitPayload failed in background: %v", err)
				p.trie.setAsyncError(fmt.Errorf("background CommitPayload failed for block #%d: %w", p.blockNum, err))
			}
			if string(p.trie.namespace) == "account_state" && p.blockNum > 0 {
				storage.UpdateLastNomtCommittedBlock(p.blockNum)
			}
		}

		p.trie.writerMu.Lock()
		view := p.trie.loadReadView()
		if view.committing != nil && p.committingMap != nil &&
			reflect.ValueOf(view.committing).Pointer() == reflect.ValueOf(p.committingMap).Pointer() {
			p.trie.publishReadView(
				view.dirty,
				nil,
				view.rootHash,
			)
		}
		p.trie.writerMu.Unlock()
	}()
}

// CommitPayloadAsync extracts pending finished session and changelogs synchronously
// to prevent subsequent block commits from overwriting them, and then flushes
// the payload to disk asynchronously in a background goroutine.
func (n *NomtStateTrie) CommitPayloadAsync() {
	payload := n.ExtractPendingPayload()
	if payload != nil {
		payload.WriteChangelog()
		payload.CommitAsync()
	}
}

func (n *NomtStateTrie) setAsyncError(err error) {
	if err == nil {
		return
	}
	n.asyncErr.Store(&err)
}

func (n *NomtStateTrie) getAsyncError() error {
	pErr := n.asyncErr.Load()
	if pErr == nil {
		return nil
	}
	return *pErr
}

func (n *NomtStateTrie) WaitCommitPayload() error {
	// Block until all background CommitAsync goroutines finish.
	// This guarantees that all in-flight disk writes are fully flushed,
	// which is required before triggering an atomic database snapshot.
	n.commitWg.Wait()
	return n.getAsyncError()
}

// GetCommitBatch returns the entries from the last Commit for network replication.
// One-shot read: clears the stored batch after retrieval.
func (n *NomtStateTrie) GetCommitBatch() [][2][]byte {
	n.writerMu.Lock()
	defer n.writerMu.Unlock()
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
func ApplyNomtReplicationBatches(
	aggregatedBatches map[string][][2][]byte,
	changelogDB *state_changelog.StateChangelogDB,
	stakeChangelogDB *state_changelog.StateChangelogDB,
	blockNumber uint64,
) ([]NomtSessionToFlush, error) {
	var sessionsToFlush []NomtSessionToFlush

	if globalStateBackend != BackendNOMT {
		return sessionsToFlush, nil
	}

	// We apply NOMT batches for BOTH Master and Sub nodes.
	// We also REMOVE the 'nomt:' prefixed keys from aggregatedBatches so that
	// they do NOT get written to PebbleDB again.

	namespaces := map[string]string{
		"Account":     "account_state",
		"SC Storage":  "smart_contract_storage",
		"StakeState":  "stake_db",
		"Receipt":     "receipts",
		"Transaction": "transaction_state",
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
				return sessionsToFlush, fmt.Errorf("failed to init NOMT handle for sync: %w", err)
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
						return sessionsToFlush, fmt.Errorf("failed to apply nomt sync batch for contract %s: %w", addrHex, err)
					}
					if _, _, _, err := t.Commit(true); err != nil {
						return sessionsToFlush, fmt.Errorf("failed to commit nomt sync batch for contract %s: %w", addrHex, err)
					}
					if err := t.CommitPayload(); err != nil {
						return sessionsToFlush, fmt.Errorf("failed to flush nomt sync batch for contract %s: %w", addrHex, err)
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
				
				// Set changelogDB based on namespace
				if namespace == "account_state" {
					trie.SetChangelogDB(changelogDB)
				} else if namespace == "stake_db" {
					trie.SetChangelogDB(stakeChangelogDB)
				}

				if blockNumber > 0 {
					trie.SetCurrentCommitBlock(blockNumber)
				}

				// CRITICAL FIX: For stake_db, we MUST NOT set isReplicationSync to true.
				// If we do, the knownKeys registry is bypassed, and subsequent calls
				// to GetAllValidators() will return 0 validators, causing consensus forks
				// (each node falls back to electing itself as leader).
				if namespace != "stake_db" {
					trie.SetReplicationSync(true)
				}
				
				if err := trie.BatchUpdate(nomtKeys, nomtValues); err != nil {
					return sessionsToFlush, fmt.Errorf("failed to apply nomt sync batch: %w", err)
				}
				if _, _, _, err := trie.Commit(true); err != nil {
					return sessionsToFlush, fmt.Errorf("failed to commit nomt sync batch: %w", err)
				}
				if err := trie.CommitPayload(); err != nil {
					return sessionsToFlush, fmt.Errorf("failed to flush nomt sync batch: %w", err)
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

	return sessionsToFlush, nil
}

// GenerateProof generates a Merkle proof for a specific key
func (n *NomtStateTrie) GenerateProof(key []byte) ([]byte, error) {
	// Hash the raw key to get the 32-byte KeyPath
	keyPath := addressToKeyPathWithNamespace(n.namespace, key)

	// Call the C-FFI bridge
	proof, err := n.handle.GenerateProof(keyPath)
	if err != nil {
		return nil, fmt.Errorf("NomtStateTrie GenerateProof failed: %w", err)
	}

	return proof, nil
}
