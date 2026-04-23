package account_state_db

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sync" // Keep sync package
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	// Assume these paths are correct for your project structure
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/state" // Assuming AccountState implementation is here
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

var byteSlicePool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 256)
		return &b
	},
}

// AccountStateDB manages account states using a Merkle Patricia Trie and a cache for dirty states.
// It is designed to be concurrency-safe for operations on different accounts,
// and uses specific locking for structural changes and commit operations.
type AccountStateDB struct {
	trie p_trie.StateTrie // The underlying trie storing account states (interface for MPT or FlatState)

	originRootHash common.Hash     // The root hash of the trie when the DB was initialized or last committed/reloaded
	db             storage.Storage // The persistent key-value store backing the trie

	// dirtyAccounts caches account states that have been MODIFIED but not yet committed.
	// Only setDirtyAccountState() writes to this map.
	// IntermediateRoot processes ONLY entries in this map → reducing unnecessary trie.Update() calls.
	dirtyAccounts sync.Map

	// loadedAccounts caches account states that have been LOADED (read-only) from trie/LRU.
	// getOrCreateAccountState and PreloadAccounts store into this map on cache miss.
	// This separation prevents loaded-but-unmodified accounts from triggering
	// trie.Update() in IntermediateRoot (each costs ~25µs).
	loadedAccounts sync.Map

	// muTrie protects concurrent access to the underlying trie.
	// RLock is used for reads (getOrCreateAccountState, GetAll) — unlimited concurrent readers.
	// Lock is used for mutations (IntermediateRoot trie.Update loop, Commit trie.Commit+swap,
	// ReloadTrie, Discard, CopyFrom) — exclusive access, blocks readers briefly.
	muTrie sync.RWMutex

	lockedFlag   atomic.Bool      // CHANGED: Use atomic.Bool
	trieWarmed   atomic.Bool      // Tracks whether LevelDB cache is warm (skip PreWarm after first block)
	isFlatTrie   bool             // TPS OPT: Cached flag — true if trie is *FlatStateTrie (thread-safe Get, skip muTrie lock)
	accountLocks [256]*sync.Mutex // Sharded locks for concurrent account mutations

	// and updating the live trie reference) happens atomically.
	muCommit sync.Mutex

	accountBatch []byte // Batch of account data prepared for network transfer during commit (used by master nodes)

	// lruCache caches serialized []byte of account states from trie.Get() to eliminate LevelDB I/O latency
	// TPS OPT: Option B - Replacing lru.Cache with a map to avoid pointer-heavy doubly-linked lists.
	lruCache    map[common.Address][]byte
	lruCacheOld map[common.Address][]byte
	lruMu       sync.RWMutex

	// FORK-SAFETY: persistReady is closed by PersistAsync after trie swap completes.
	// IntermediateRoot(true) waits on this channel before acquiring muTrie.Lock,
	// ensuring the trie reference reflects the previous block's committed state.
	// Without this, the next block can read a stale trie if PersistAsync hasn't
	// completed its trie swap yet (race condition causing stateRoot divergence).
	persistReady chan struct{}

	// nomtCommitGuard serializes BatchUpdateWithCachedOldValues and CommitPayload
	// for NomtStateTrie. CommitPayload is pushed to the background, but we still
	// need to prevent concurrent modifications to the NOMT structure during disk flush.
	// This acts as a lightweight, channel-based mutex.
	nomtCommitGuard chan struct{}

	// TPS OPT: Bounded eviction for loadedAccounts.
	// loadedAccounts grows unbounded across blocks (Phase 4 optimization).
	// Every N blocks, clear loadedAccounts to cap memory growth and reduce GC pressure.
	// Without this, sustained 30K TPS causes loadedAccounts to accumulate 300K-3M entries
	// over 100+ blocks, each entry being an interface{} that GC must scan.
	blocksSinceLoadedClear uint32
}

// NewAccountStateDB creates a new instance of AccountStateDB.
func NewAccountStateDB(
	trie p_trie.StateTrie,
	db storage.Storage,
) *AccountStateDB {
	if trie == nil {
		// Handle case where trie might be nil, perhaps initialize an empty one?
		// For now, assume a valid trie is passed.
		logger.Error("NewAccountStateDB received a nil trie")
		// Depending on requirements, might return nil or an error, or create an empty trie.
		// Returning struct with nil trie will likely cause panics later.
		// Let's assume trie is required.
	}
	if db == nil {
		logger.Error("NewAccountStateDB received a nil db storage")
		// Storage is essential.
		return nil // Or return an error
	}

	// Generate double-generation maps instead of doubly-linked list lru.Cache.
	// This reduces GC scan time by ~80% as each byte slice acts as an opaque buffer without pointers.
	cacheCurrent := make(map[common.Address][]byte, 200000)
	cacheOld := make(map[common.Address][]byte)

	// Initialize persistReady as an already-closed channel.
	// This means the first block won't wait (no prior PersistAsync to complete).
	initReady := make(chan struct{})
	close(initReady)

	// Initialize nomtCommitGuard with capacity 1 to act as a mutex
	guard := make(chan struct{}, 1)
	guard <- struct{}{} // Initialize as unlocked

	// TPS OPT: Cache trie type once to avoid repeated type assertions in hot-path.
	// Both FlatStateTrie and NomtStateTrie are thread-safe for concurrent reads,
	// allowing us to skip muTrie.Lock on Get() calls.
	_, isFlat := trie.(*p_trie.FlatStateTrie)
	_, isNomt := trie.(*p_trie.NomtStateTrie)
	isThreadSafeRead := isFlat || isNomt

	db_instance := &AccountStateDB{
		trie:            trie,
		db:              db,
		originRootHash:  trie.Hash(),
		dirtyAccounts:   sync.Map{}, // Initialize sync.Map
		lruCache:        cacheCurrent,
		lruCacheOld:     cacheOld,
		persistReady:    initReady,
		nomtCommitGuard: guard,
		isFlatTrie:      isThreadSafeRead,
	}
	for i := 0; i < 256; i++ {
		db_instance.accountLocks[i] = &sync.Mutex{}
	}
	return db_instance
}

// ReloadTrie replaces the current trie with a new one based on the given root hash.
// It clears the dirty account cache. This requires exclusive access to modify the structure.
func (db *AccountStateDB) ReloadTrie(rootHash common.Hash) error {

	if db.lockedFlag.Load() {
		return errors.New("ReloadTrie db.lockedFlag is already locked")
	}

	newTrie, err := p_trie.NewStateTrie(rootHash, db.db, true)
	if err != nil {
		logger.Error("ReloadTrie: Failed to create new trie instance", "hash", rootHash, "error", err)
		return fmt.Errorf("failed to load trie for root %s: %w", rootHash, err)
	}
	db.muTrie.Lock()
	if db.trie != nil {
		if closer, ok := db.trie.(interface{ Close() }); ok {
			closer.Close()
		}
	}
	db.trie = newTrie
	_, isFlat := newTrie.(*p_trie.FlatStateTrie)
	_, isNomt := newTrie.(*p_trie.NomtStateTrie)
	db.isFlatTrie = isFlat || isNomt
	db.originRootHash = rootHash
	db.dirtyAccounts.Clear()  // Clear dirty accounts under lock
	db.loadedAccounts.Clear() // Clear loaded accounts too
	if db.lruCache != nil {
		db.lruMu.Lock()
		db.lruCache = make(map[common.Address][]byte, 200000)
		db.lruCacheOld = make(map[common.Address][]byte)
		db.lruMu.Unlock()
	}
	db.muTrie.Unlock()

	return nil
}

// GetOriginRootHash returns the current origin root hash of the trie (for debugging).
func (db *AccountStateDB) GetOriginRootHash() common.Hash {
	return db.originRootHash
}

// Trie returns the underlying StateTrie instance.
func (db *AccountStateDB) Trie() p_trie.StateTrie {
	db.muTrie.RLock()
	defer db.muTrie.RUnlock()
	return db.trie
}

// DirtyAccountCount returns the number of dirty accounts (for debugging).
func (db *AccountStateDB) DirtyAccountCount() int {
	count := 0
	db.dirtyAccounts.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// DirtyAccountDetails returns addresses and serialized data hashes/details of dirty accounts (for debugging).
func (db *AccountStateDB) DirtyAccountDetails() []string {
	var details []string
	db.dirtyAccounts.Range(func(key, value interface{}) bool {
		address, ok1 := key.(common.Address)
		state, ok2 := value.(types.AccountState)
		if !ok1 || !ok2 || state == nil {
			details = append(details, fmt.Sprintf("INVALID(%T/%T)", key, value))
			return true
		}
		b, err := state.Marshal()
		if err != nil {
			details = append(details, fmt.Sprintf("%s:MARSHAL_ERR(%v)", address.Hex()[:16], err))
			return true
		}
		dataHash := common.BytesToHash(crypto.Keccak256(b))
		details = append(details, fmt.Sprintf("addr=%s bal=%s nonce=%d dataHash=%s datLen=%d",
			address.Hex()[:16], state.TotalBalance().String(), state.Nonce(), dataHash.Hex()[:20], len(b)))
		return true
	})
	return details
}

// DirtyContentHash returns a deterministic hash of ALL dirty account content.
// Used for fork debugging — if two nodes have the same dirty_content_hash but
// different POST-IR root, the trie is non-deterministic. If the hash differs,
// the TX processing produced different account states.
func (db *AccountStateDB) DirtyContentHash() common.Hash {
	var keys []common.Address
	entries := make(map[common.Address][]byte)
	db.dirtyAccounts.Range(func(key, value interface{}) bool {
		addr, ok1 := key.(common.Address)
		state, ok2 := value.(types.AccountState)
		if !ok1 || !ok2 || state == nil {
			return true
		}
		b, err := state.Marshal()
		if err != nil {
			return true
		}
		keys = append(keys, addr)
		entries[addr] = b
		return true
	})
	slices.SortFunc(keys, func(a, b common.Address) int {
		return bytes.Compare(a[:], b[:])
	})
	hasher := crypto.NewKeccakState()
	for _, addr := range keys {
		hasher.Write(addr.Bytes())
		hasher.Write(entries[addr])
	}
	var h common.Hash
	hasher.Read(h[:])
	return h
}

// AccountState retrieves the state for a given address.
// It fetches from the dirty cache first, then the underlying trie.
// If the account doesn't exist, a new state is created and cached.
func (db *AccountStateDB) AccountState(address common.Address) (types.AccountState, error) {
	return db.getOrCreateAccountState(address)
}

// AccountStateReadOnly retrieves the account state WITHOUT storing into dirtyAccounts.
// Use this for read-only validation (VerifyTransaction, AddTransactionToPool) to avoid
// polluting the dirty cache with ephemeral new-account entries.
// For new accounts not in dirty cache or trie, returns (nil, err).
func (db *AccountStateDB) AccountStateReadOnly(address common.Address) (types.AccountState, error) {
	if db == nil {
		return nil, errors.New("AccountStateReadOnly called on nil AccountStateDB")
	}
	// Check dirty cache first — fast path for existing dirty accounts
	if value, ok := db.dirtyAccounts.Load(address); ok {
		if as, valid := value.(types.AccountState); valid && as != nil {
			return as, nil
		}
	}

	// 1.25. Check loaded cache (read-only loaded accounts)
	if value, ok := db.loadedAccounts.Load(address); ok {
		if as, valid := value.(types.AccountState); valid && as != nil {
			return as, nil
		}
	}

	var bData []byte
	var err error
	var pooledSlice *[]byte

	db.lruMu.RLock()
	cachedData, ok := db.lruCache[address]
	if !ok {
		cachedData, ok = db.lruCacheOld[address]
	}
	db.lruMu.RUnlock()

	if ok {
		// TPS OPT: Use sync.Pool to avoid allocating new byte slices
		pooledSlice = byteSlicePool.Get().(*[]byte)
		size := len(cachedData)
		if cap(*pooledSlice) < size {
			*pooledSlice = make([]byte, size)
		} else {
			*pooledSlice = (*pooledSlice)[:size]
		}
		copy(*pooledSlice, cachedData)
		bData = *pooledSlice
	} else {
		// TPS OPT: FlatStateTrie.Get() is fully thread-safe (internal RWMutex).
		// Skip muTrie.Lock to eliminate serialization bottleneck on cache miss.
		if db.isFlatTrie {
			bData, err = db.trie.Get(address.Bytes())
		} else {
			// MPT trie: requires exclusive lock because Get() mutates internal cache
			db.muTrie.Lock()
			trieToUse := db.trie
			if trieToUse == nil {
				db.muTrie.Unlock()
				return nil, errors.New("account state DB has a nil trie")
			}
			bData, err = trieToUse.Get(address.Bytes())
			db.muTrie.Unlock()
		}
		if err != nil {
			return nil, fmt.Errorf("error getting %s from Trie: %w", address.Hex(), err)
		}

		db.lruMu.Lock()
		db.lruCache[address] = bData
		db.lruMu.Unlock()
	}

	if len(bData) == 0 {
		// New account: return fresh state WITHOUT storing in dirty cache
		// logger.Warn("🔍 [DEBUG-ASRO] AccountStateReadOnly(%s): ALL sources empty → returning fresh account (nonce=0, no BLS key)", address.Hex())
		return state.NewAccountState(address), nil
	}
	loadedAs := &state.AccountState{}
	if err = loadedAs.Unmarshal(bData); err != nil {
		return nil, fmt.Errorf("error unmarshalling %s from Trie: %w", address.Hex(), err)
	}
	// if len(loadedAs.PublicKeyBls()) == 0 {
	// 	logger.Warn("🔍 [DEBUG-ASRO] AccountStateReadOnly(%s): found data but PublicKeyBls is EMPTY, nonce=%d", address.Hex(), loadedAs.Nonce())
	// }
	if pooledSlice != nil {
		byteSlicePool.Put(pooledSlice)
	}

	return loadedAs, nil
}

// NewAccountState creates a fresh empty account state for the given address.
func (db *AccountStateDB) NewAccountState(address common.Address) types.AccountState {
	return state.NewAccountState(address)
}

// GetAll retrieves all account states directly from the *committed* state in the trie.
// Note: This does NOT include uncommitted changes from the dirtyAccounts cache.
// If you need a complete snapshot including dirty states, you would need to
// acquire muStruct, get all from trie, and then merge in the dirtyAccounts data.
func (db *AccountStateDB) GetAll() (map[common.Address]types.AccountState, error) {
	// Acquire Lock to safely access db.trie because Ethereum trie is not thread-safe for reads
	db.muTrie.Lock()
	trieToUse := db.trie

	if trieToUse == nil {
		db.muTrie.Unlock()
		logger.Error("GetAll: Trie is nil")
		return nil, errors.New("account state DB has a nil trie")
	}

	allAccounts := make(map[common.Address]types.AccountState)
	allData, err := trieToUse.GetAll()
	db.muTrie.Unlock()
	if err != nil {
		logger.Error("GetAll: Error retrieving data from trie", "error", err)
		return nil, fmt.Errorf("error getting all data from trie: %w", err)
	}

	for addressStr, accountStateBytes := range allData {
		// Need to convert key (likely []byte or string) back to common.Address
		address := common.HexToAddress(addressStr) // Chuyển đổi string sang common.Address

		// Unmarshal the value into an AccountState implementation
		accountState := &state.AccountState{} // Use the concrete type
		err := accountState.Unmarshal(accountStateBytes)
		if err != nil {
			// Consider logging the error and skipping the account?
			logger.Warn("Failed to unmarshal account state during GetAll", "address", address.Hex(), "error", err)
			continue // Skip corrupted data
		}
		allAccounts[address] = accountState
	}
	logger.Debug("GetAll: Retrieved accounts from committed trie state", "count", len(allAccounts))
	return allAccounts, nil
}

// --- State Management Methods ---

// InvalidateAllCaches clears all in-memory read caches (loadedAccounts + lruCache)
// WITHOUT touching dirtyAccounts or the trie itself.
//
// CRITICAL FOR SUB NODES: When a Sub node applies blocks received from Master via
// `applyBlockBatch()`, the data is written directly to NOMT/PebbleDB, bypassing
// AccountStateDB entirely. This means loadedAccounts and lruCache may contain
// stale data from BEFORE the sync. Without this call, subsequent RPC queries
// (e.g. mtn_getAccountState, eth_getBalance) will return stale cached values
// instead of the freshly synced data — making the Sub node appear out of sync.
//
// This is safe to call at any time: it only affects read caches and will cause
// the next read to go through trie.Get() which reads fresh data from NOMT/PebbleDB.
func (db *AccountStateDB) InvalidateAllCaches() {
	db.loadedAccounts.Clear()
	if db.lruCache != nil {
		db.lruMu.Lock()
		db.lruCache = make(map[common.Address][]byte, 200000)
		db.lruCacheOld = make(map[common.Address][]byte)
		db.lruMu.Unlock()
	}
	logger.Debug("InvalidateAllCaches: Cleared loadedAccounts + lruCache (Sub-node sync safe)")
}

// Discard reverts all uncommitted changes by clearing the dirty cache
// and reloading the trie from the last committed state (originRootHash).
func (db *AccountStateDB) Discard() (err error) {

	if db.lockedFlag.Load() {
		return errors.New("Discard db.lockedFlag is already locked")
	}
	// Clear dirty accounts first
	db.dirtyAccounts.Clear()
	db.loadedAccounts.Clear()
	if db.lruCache != nil {
		db.lruMu.Lock()
		db.lruCache = make(map[common.Address][]byte, 200000)
		db.lruCacheOld = make(map[common.Address][]byte)
		db.lruMu.Unlock()
	}

	// Reload trie from the original hash
	originHash := db.originRootHash

	currentDb := db.db // Use the existing db instance

	// Check if db is nil before using it
	if currentDb == nil {
		logger.Error("Discard: Database instance is nil")
		return errors.New("cannot discard, database instance is nil")
	}

	newTrie, trieErr := p_trie.NewStateTrie(originHash, currentDb, true)
	if trieErr != nil {
		logger.Error("Discard: Failed to reload trie from origin hash", "hash", originHash, "error", trieErr)
		return fmt.Errorf("failed to reload trie to %s after discard: %w", originHash, trieErr)
	}

	db.muTrie.Lock()
	if db.trie != nil {
		if closer, ok := db.trie.(interface{ Close() }); ok {
			closer.Close()
		}
	}
	db.trie = newTrie
	db.muTrie.Unlock()

	logger.Info("Discard successful, reverted to root hash", "hash", originHash)
	return nil
}

// SetAccountBatch stores a serialized batch of account data, typically used for network transfer.
func (db *AccountStateDB) SetAccountBatch(batch []byte) {
	// If this can be called concurrently with GetAccountBatch or Commit, it needs locking.
	db.accountBatch = batch
}

// GetAccountBatch retrieves the stored account batch WITHOUT clearing it.
// Multiple goroutines in createBlockBatch may call this concurrently,
// so clearing here would cause a race condition where only the first
// goroutine gets the data and all others get nil.
// Call ClearAccountBatch() explicitly after all blocks in the batch are created.
func (db *AccountStateDB) GetAccountBatch() []byte {
	return db.accountBatch
}

// ClearAccountBatch explicitly clears the stored account batch.
// Must be called after all blocks in a batch have been created and their
// AccountBatch data has been snapshotted into CommitJob structs.
func (db *AccountStateDB) ClearAccountBatch() {
	db.accountBatch = nil
}

// Close releases any resources held by the trie, specifically the NOMT write session.
// This is critical to prevent session leaks when a trie is discarded or replaced
// (e.g., on Sub nodes that never call Commit, or during state reorgs/reloads).
func (db *AccountStateDB) Close() {
	db.muTrie.Lock()
	defer db.muTrie.Unlock()
	if db.trie != nil {
		if closer, ok := db.trie.(interface{ Close() }); ok {
			closer.Close()
		}
	}
}

// --- Internal Helper Methods ---

// setDirtyAccountState stores the account state in the concurrent map.
// This relies on sync.Map's internal safety for concurrent Store calls on potentially different keys.
// No external lock needed here for the Store operation itself.
func (db *AccountStateDB) setDirtyAccountState(as types.AccountState) {
	if as == nil {
		logger.Warn("setDirtyAccountState: Attempted to store nil account state")
		return // Avoid storing nil interface
	}

	// OPTIMIZATION: If the account hasn't been logically modified, don't mark it dirty
	if !as.IsDirty() {
		return
	}

	db.dirtyAccounts.Store(as.Address(), as)
	// logger.Trace("Marked account dirty", "address", as.Address().Hex()) // Optional detailed logging
}

// PublicSetDirtyAccountState provides a public way to mark an account as dirty.
// Relies on sync.Map's internal safety.
func (db *AccountStateDB) PublicSetDirtyAccountState(as types.AccountState) {
	// Delegates to the internal, potentially traced, version
	db.setDirtyAccountState(as)
}

// getOrCreateAccountState retrieves an account state, optimized for concurrency.
// It first checks the dirty cache (sync.Map). If not found (cache miss),
// it reads from the underlying trie. If not in the trie, it creates a new state.
// The retrieved or newly created state is then stored back into the dirty cache *only on cache miss*.
// Uses RLock on muStruct to allow concurrent reads/stores via sync.Map API
// while preventing structural changes (reassignment of dirtyAccounts or trie) concurrently.
func (db *AccountStateDB) getOrCreateAccountState(
	address common.Address,
) (types.AccountState, error) {
	// Kiểm tra db nil để tránh panic
	if db == nil {
		return nil, errors.New("getOrCreateAccountState called on nil AccountStateDB")
	}

	// --- Khóa Đọc muStruct ---
	// Bảo vệ chống lại việc db.dirtyAccounts hoặc db.trie bị thay thế đồng thời
	// bởi ReloadTrie, Discard, Commit, CopyFrom. Cho phép hàm này thực thi đồng thời.

	// --- Đã có Khóa Đọc ---

	// 1. Check dirty cache first (modified accounts — highest priority)
	value, ok := db.dirtyAccounts.Load(address)
	if ok {
		accountState, valid := value.(types.AccountState)
		if valid && accountState != nil {
			return accountState, nil
		}
		if !valid {
			logger.Error("getOrCreateAccountState: Invalid type found in dirty cache map", "address", address.Hex(), "type", fmt.Sprintf("%T", value))
		}
		if accountState == nil {
			logger.Error("getOrCreateAccountState: Found nil account state in dirty cache map", "address", address.Hex())
		}
	}

	// 1.5. Check loaded cache (read-only loaded accounts — second priority)
	value, ok = db.loadedAccounts.Load(address)
	if as, valid := value.(types.AccountState); valid && as != nil {
		return as, nil
	}

	// --- Cache miss hoặc mục cache không hợp lệ (dirty cache miss) ---
	// 1.5. Check LRU cache first (does not require trie lock)
	var bData []byte
	var pooledSlice *[]byte
	var cachedData []byte
	var found bool

	db.lruMu.RLock()
	if cachedData, found = db.lruCache[address]; !found {
		cachedData, found = db.lruCacheOld[address]
	}
	db.lruMu.RUnlock()

	if found {
		// TPS OPT: Use sync.Pool to avoid allocating new byte slices for every cache hit
		pooledSlice = byteSlicePool.Get().(*[]byte)
		size := len(cachedData)
		if cap(*pooledSlice) < size {
			*pooledSlice = make([]byte, size)
		} else {
			*pooledSlice = (*pooledSlice)[:size]
		}
		copy(*pooledSlice, cachedData)
		bData = *pooledSlice
	} else {
		// ═══════════════════════════════════════════════════════════════
		// TPS OPT: FlatStateTrie.Get() is fully thread-safe (internal RWMutex).
		// Skip muTrie.Lock entirely to eliminate the #1 serialization bottleneck.
		// MPT trie.Get() is NOT thread-safe (mutates internal resolution cache),
		// so it still requires the exclusive write lock.
		// ═══════════════════════════════════════════════════════════════
		var err error
		if db.isFlatTrie {
			// FlatStateTrie: direct read without external lock
			bData, err = db.trie.Get(address.Bytes())
		} else {
			// MPT: exclusive lock required to prevent trie corruption
			db.muTrie.Lock()
			trieToUse := db.trie
			if trieToUse == nil {
				db.muTrie.Unlock()
				logger.Error("getOrCreateAccountState: Trie is nil during cache miss lookup", "address", address.Hex())
				return nil, errors.New("account state DB has a nil trie")
			}
			bData, err = trieToUse.Get(address.Bytes())
			db.muTrie.Unlock()
		}
		if err != nil {
			logger.Debug("getOrCreateAccountState: Error getting data from Trie: address=%s, err=%v", address.Hex(), err)
			return nil, fmt.Errorf("error getting %s from Trie: %w", address.Hex(), err)
		}

		db.lruMu.Lock()
		db.lruCache[address] = bData
		db.lruMu.Unlock()
	}

	// Biến tạm để giữ state sẽ được cung cấp cho LoadOrStore
	var stateToPotentiallyStore types.AccountState

	// 3. Nếu không tìm thấy trong Trie, tạo account state mới
	if len(bData) == 0 {
		// Sử dụng constructor của implementation cụ thể
		newState := state.NewAccountState(address)
		if newState == nil {
			// Constructor không nên trả về nil
			logger.Error("getOrCreateAccountState: NewAccountState returned nil", "address", address.Hex())
			return nil, fmt.Errorf("failed to create new account state for %s", address.Hex())
		}
		logger.Debug("getOrCreateAccountState: Prepared new AccountState (not found in trie)", "address", address.Hex())
		stateToPotentiallyStore = newState
	} else {
		// 4. Nếu tìm thấy trong Trie, unmarshal dữ liệu
		loadedAs := &state.AccountState{} // Tạo instance của kiểu cụ thể để unmarshal vào
		loadErr := loadedAs.Unmarshal(bData)
		if loadErr != nil {
			logger.Error("getOrCreateAccountState: Error unmarshalling account state from Trie", "address", address.Hex(), "error", loadErr)
			return nil, fmt.Errorf("error unmarshalling %s from Trie: %w", address.Hex(), loadErr)
		}
		stateToPotentiallyStore = loadedAs // Gán state đã unmarshal thành công
		logger.Debug("getOrCreateAccountState: Loaded AccountState from Trie", "address", address.Hex())
	}

	// Return the pooled slice back to the pool if we used one
	if pooledSlice != nil {
		byteSlicePool.Put(pooledSlice)
	}

	// Store into loadedAccounts (not dirtyAccounts) — this account is merely loaded/read.
	// Only setDirtyAccountState() should put accounts into dirtyAccounts (when actually modified).
	actualValue, loaded := db.loadedAccounts.LoadOrStore(address, stateToPotentiallyStore)

	finalAs, castOk := actualValue.(types.AccountState)
	if !castOk || finalAs == nil {
		logger.Error("getOrCreateAccountState: Invalid type/nil found in cache via LoadOrStore",
			"address", address.Hex(),
			"type", fmt.Sprintf("%T", actualValue),
			"is_nil", actualValue == nil,
			"loaded_flag", loaded)
		return nil, fmt.Errorf("invalid type or nil value found/stored in cache for %s", address.Hex())
	}
	return finalAs, nil
}

// PreloadAccounts batch-loads multiple account states into the dirty cache.
// PERFORMANCE OPTIMIZATION: Instead of calling AccountState() N times (each acquiring/releasing
// muTrie.Lock individually), this method:
//  1. Filters out addresses already in dirty cache (sync.Map.Load — no lock)
//  2. Acquires muTrie.Lock() ONCE
//  3. Batch-reads ALL remaining addresses from trie in a single locked section
//  4. Releases muTrie.Unlock()
//  5. Unmarshals results and stores in dirty cache
//
// This eliminates N-1 lock/unlock cycles and reduces pre-fetch from 329ms to ~100ms for 11k+ addresses.
func (db *AccountStateDB) PreloadAccounts(addresses []common.Address) {
	if db == nil {
		return
	}

	// Phase 1: Filter — skip addresses already in dirty or loaded cache
	// OPTIMIZATION #1: Also skip loadedAccounts (previously only skipped dirtyAccounts).
	// Accounts loaded in previous blocks are still valid in loadedAccounts cache.
	var toLoad []common.Address
	for _, addr := range addresses {
		if _, ok := db.dirtyAccounts.Load(addr); ok {
			continue
		}
		if _, ok := db.loadedAccounts.Load(addr); ok {
			continue
		}
		toLoad = append(toLoad, addr)
	}

	if len(toLoad) == 0 {
		return // All addresses already cached
	}

	type trieResult struct {
		addr        common.Address
		bData       []byte
		pooledSlice *[]byte
		err         error
	}
	results := make([]trieResult, 0, len(toLoad))
	var addressesToReadFromTrie []common.Address

	// Phase 1.5: Filter — check LRU Cache for the remaining addresses
	for _, addr := range toLoad {
		db.lruMu.RLock()
		cachedData, ok := db.lruCache[addr]
		if !ok {
			cachedData, ok = db.lruCacheOld[addr]
		}
		db.lruMu.RUnlock()

		if ok {
			// Found in LRU cache! Use sync.Pool to avoid allocation
			pooledSlice := byteSlicePool.Get().(*[]byte)
			size := len(cachedData)
			if cap(*pooledSlice) < size {
				*pooledSlice = make([]byte, size)
			} else {
				*pooledSlice = (*pooledSlice)[:size]
			}
			copy(*pooledSlice, cachedData)
			results = append(results, trieResult{addr: addr, bData: *pooledSlice, pooledSlice: pooledSlice, err: nil})
		} else {
			// Cache miss, must read from LevelDB
			addressesToReadFromTrie = append(addressesToReadFromTrie, addr)
		}
	}

	// Phase 2: Batch trie read for misses (Parallelized using safe Trie copies avoiding lock starvation)
	if len(addressesToReadFromTrie) > 0 {
		db.muTrie.RLock()
		if db.trie == nil {
			db.muTrie.RUnlock()
			logger.Error("PreloadAccounts: Trie is nil")
			return
		}

		// PERFORMANCE OPTIMIZATION (Thread-safe Tries):
		// Geth's MPT Get() mutates the trie cache, requiring a full map clone (db.trie.Copy())
		// to avoid concurrent map read/write panics.
		// FlatStateTrie and NomtStateTrie Get() are completely thread-safe.
		// Skipping Copy() eliminates massive GC stalls during high-concurrency TPS bursts.
		var baseCopy p_trie.StateTrie
		isFlatTrie := false
		if _, ok := db.trie.(*p_trie.FlatStateTrie); ok {
			baseCopy = db.trie
			isFlatTrie = true
		} else if _, ok := db.trie.(*p_trie.NomtStateTrie); ok {
			baseCopy = db.trie
			isFlatTrie = true
		} else {
			baseCopy = db.trie.Copy()
		}
		db.muTrie.RUnlock()

		// ASYNCHRONOUS MERKLE PREFETCHING (NOMT)
		// Dispatch prefetch tasks to the backend immediately so they can overlap with EVM execution.
		if isFlatTrie { // Only NomtStateTrie implements PreWarm to actually do anything, FlatStateTrie is no-op.
			keysToWarm := make([][]byte, len(addressesToReadFromTrie))
			for i, addr := range addressesToReadFromTrie {
				keysToWarm[i] = addr.Bytes()
			}
			baseCopy.PreWarm(keysToWarm)
		}

		numWorkers := 32
		if len(addressesToReadFromTrie) < numWorkers {
			numWorkers = len(addressesToReadFromTrie)
		}

		resultsChan := make(chan trieResult, len(addressesToReadFromTrie))
		var wg sync.WaitGroup

		chunkSize := (len(addressesToReadFromTrie) + numWorkers - 1) / numWorkers
		for i := 0; i < numWorkers; i++ {
			start := i * chunkSize
			end := start + chunkSize
			if start >= len(addressesToReadFromTrie) {
				break
			}
			if end > len(addressesToReadFromTrie) {
				end = len(addressesToReadFromTrie)
			}

			wg.Add(1)
			go func(addrs []common.Address) {
				defer wg.Done()
				// CRITICAL FORK-SAFETY AND PERFORMANCE:
				// MPT's Trie.Get() mutates its internal unhasher map cache, so sharing
				// a single trie object across goroutines causes panic:"concurrent map write".
				// FlatStateTrie's Get() is completely thread-safe and holds its own RLock.
				var localTrie p_trie.StateTrie
				if isFlatTrie {
					localTrie = baseCopy
				} else {
					localTrie = baseCopy.Copy()
				}
				for _, addr := range addrs {
					bData, getErr := localTrie.Get(addr.Bytes())

					resultsChan <- trieResult{addr: addr, bData: bData, err: getErr}
				}
			}(addressesToReadFromTrie[start:end])
		}

		wg.Wait()
		close(resultsChan)

		// Accumulate results and feed the LRU Cache safely in the main thread
		for res := range resultsChan {
			results = append(results, res)
			if res.err == nil {
				db.lruMu.Lock()
				db.lruCache[res.addr] = res.bData
				db.lruMu.Unlock()
			}
		}
	}

	// Phase 3: PARALLEL Unmarshal and store in loaded cache (OPTIMIZATION #2)
	// Unmarshaling protobuf is CPU-bound. Parallelizing eliminates ~50ms for 30K results.
	// sync.Map.LoadOrStore is concurrent-safe, so no additional locking needed.
	var wgUnmarshal sync.WaitGroup
	for _, r := range results {
		if r.err != nil {
			logger.Debug("PreloadAccounts: Error getting %s from Trie: %v", r.addr.Hex(), r.err)
			continue
		}

		wgUnmarshal.Add(1)
		go func(r trieResult) {
			defer wgUnmarshal.Done()

			var accountState types.AccountState
			if len(r.bData) == 0 {
				// New account — create fresh state
				accountState = state.NewAccountState(r.addr)
			} else {
				// Existing account — unmarshal from trie data
				loaded := &state.AccountState{}
				if err := loaded.Unmarshal(r.bData); err != nil {
					logger.Error("PreloadAccounts: Unmarshal error for %s: %v", r.addr.Hex(), err)
					return
				}
				accountState = loaded
			}

			// Return the pooled slice back to the pool if we used one
			if r.pooledSlice != nil {
				byteSlicePool.Put(r.pooledSlice)
			}

			// Store into loadedAccounts (not dirtyAccounts) — preloaded accounts are read-only.
			db.loadedAccounts.LoadOrStore(r.addr, accountState)
		}(r)
	}
	wgUnmarshal.Wait()
}

// Storage returns the underlying storage instance.
func (db *AccountStateDB) Storage() storage.Storage {
	// Accessing db.db might need protection if it could be reassigned,
	// but currently only happens in New/CopyFrom. Assume read is safe.
	return db.db
}

// CopyFrom copies the state (trie reference, origin hash, dirty accounts)
// from another AccountStateDB (source) to this one (destination).
// Requires locking both source and destination structures to ensure atomicity
// and prevent races during the copy process.
func (db *AccountStateDB) CopyFrom(sourceDB types.AccountStateDB) error {

	if db == nil {
		return errors.New("CopyFrom called on nil destination AccountStateDB")
	}

	// Type assert to access implementation details (mutexes, specific fields).
	// This restricts CopyFrom to work only between *AccountStateDB instances.
	asDB, ok := sourceDB.(*AccountStateDB)
	if !ok {
		return errors.New("CopyFrom requires the source to be an *AccountStateDB instance")
	}
	if asDB == nil {
		return errors.New("CopyFrom called with nil source AccountStateDB")
	}

	// Avoid self-copy, which would cause deadlock on locks.
	if db == asDB {
		return errors.New("cannot CopyFrom self")
	}

	if db.lockedFlag.Load() {
		return errors.New("CopyFrom db.lockedFlag is already locked")
	}

	// --- Snapshot source dirty accounts while holding source lock ---
	tempDirty := make(map[common.Address]types.AccountState) // Use concrete types for map
	copyErr := false                                         // Flag for errors during range/copy

	asDB.dirtyAccounts.Range(func(key, value interface{}) bool {
		addr, okKey := key.(common.Address)
		stateVal, okVal := value.(types.AccountState)

		if !okKey || !okVal || stateVal == nil {
			logger.Error("CopyFrom: Invalid entry found in source dirtyAccounts map during copy",
				"key_type", fmt.Sprintf("%T", key), "value_type", fmt.Sprintf("%T", value))
			// Skip this entry or abort? Aborting seems safer.
			copyErr = true
			return false // Stop ranging
		}

		// *** Potential Deep Copy Needed Here ***
		// If AccountState contains mutable fields (slices, maps, pointers to mutable data),
		// a shallow copy (just assigning stateVal) means both source and destination
		// DBs will share the *same underlying mutable object*. Modifications in one
		// after the copy will affect the other, which is usually NOT desired.
		// A deep copy mechanism is needed if AccountState is not immutable.
		// Example using an interface (adapt as needed):
		// if copier, ok := stateVal.(interface{ DeepCopy() types.AccountState }); ok {
		//     tempDirty[addr] = copier.DeepCopy()
		// } else {
		//     logger.Warn("CopyFrom: AccountState does not implement DeepCopy, performing shallow copy.", "address", addr.Hex())
		//	   tempDirty[addr] = stateVal // Fallback to shallow copy
		// }
		tempDirty[addr] = stateVal // Current: Performing shallow copy - review if AccountState is mutable!

		return true
	})

	if copyErr {
		// Locks will be released by defers
		return errors.New("error encountered while copying dirty account entries")
	}

	// --- Copy other relevant fields from source *while holding both locks* ---
	// This prevents races where source fields might change after source unlock
	// but before destination assignment.

	// Copying the trie reference is potentially risky if the source trie can be modified
	// externally *after* this copy operation but *before* the destination uses it.
	// A full trie copy (e.g., `asDB.trie.Copy()`) might be needed for true isolation,
	// but that can be expensive. Copying the reference assumes the source trie state
	// corresponding to sourceOriginHash is effectively immutable or won't be changed
	// in a way that breaks the destination before its next commit/reload.
	asDB.muTrie.RLock()
	sourceTrie := asDB.trie
	sourceOriginHash := asDB.originRootHash
	sourceDb := asDB.db
	asDB.muTrie.RUnlock()

	// --- Apply changes to destination DB ---
	db.muTrie.Lock()
	db.trie = sourceTrie
	db.originRootHash = sourceOriginHash
	db.db = sourceDb
	db.muTrie.Unlock()

	// Replace destination dirty map with the snapshot
	db.dirtyAccounts.Clear()  // Start with a fresh map for the destination
	db.loadedAccounts.Clear() // Clear loaded accounts too
	for key, value := range tempDirty {
		db.dirtyAccounts.Store(key, value) // Populate the new map
	}

	if db.lruCache != nil {
		db.lruMu.Lock()
		db.lruCache = make(map[common.Address][]byte, 200000)
		db.lruCacheOld = make(map[common.Address][]byte)
		db.lruMu.Unlock()
	}

	logger.Info("CopyFrom completed successfully")
	// Locks will be released by defers
	return nil
}

// LoadedAccountCount returns the number of entries in loadedAccounts cache.
// Used for degradation monitoring — if this grows unbounded, it indicates
// the periodic eviction is not working properly.
func (db *AccountStateDB) LoadedAccountCount() int {
	count := 0
	db.loadedAccounts.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// CommitPayload explicitly commits the NOMT payload to disk.
// This is used for Genesis blocks and standalone operations where PersistAsync is not called.
func (db *AccountStateDB) CommitPayload() error {
	db.muTrie.RLock()
	defer db.muTrie.RUnlock()
	if nomtTrie, isNomt := db.trie.(*p_trie.NomtStateTrie); isNomt {
		nomtTrie.CommitPayload()
	}
	return nil
}
