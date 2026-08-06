package processor

import (
	"sync"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// CacheManager handles the caching of StateTrie nodes internally used by the BlockProcessor.
// It was extracted from the monolithic BlockProcessor struct.
type CacheManager struct {
	// Cached account Trie nodes parsed during GetAccountState HTTP requests
	trieCache      map[string]mt_trie.StateTrie
	trieCacheKeys  []string
	trieCacheMutex sync.RWMutex

	// Cached AccountStateDB wrappers keyed by stateRoot, reused across concurrent
	// RPC gateway calls (buildMetaTxFromEthTx) so repeated reads of the same
	// address within one block hit the in-memory loadedAccounts cache instead of
	// forcing a fresh NOMT FFI read every single call. AccountStateDB's internal
	// maps (dirtyAccounts/loadedAccounts) are already concurrency-safe for shared
	// reads across goroutines (sharded maps + per-shard mutexes), so sharing one
	// instance per stateRoot here is safe.
	accountDBCache      map[string]*account_state_db.AccountStateDB
	accountDBCacheKeys  []string
	accountDBCacheMutex sync.RWMutex
}

// NewCacheManager creates and initializes a new CacheManager.
func NewCacheManager() *CacheManager {
	return &CacheManager{
		trieCache:      make(map[string]mt_trie.StateTrie),
		trieCacheKeys:  make([]string, 0),
		accountDBCache: make(map[string]*account_state_db.AccountStateDB),
	}
}

// GetTrieCache retrieves a compiled mt_trie.StateTrie by its hex state root.
func (cm *CacheManager) GetTrieCache(stateRoot string) (mt_trie.StateTrie, bool) {
	cm.trieCacheMutex.RLock()
	defer cm.trieCacheMutex.RUnlock()
	t, ok := cm.trieCache[stateRoot]
	return t, ok
}

// SetTrieCache stores a compiled mt_trie.StateTrie by its hex state root.
// Limits the cache to 32 entries to prevent memory-leaks.
func (cm *CacheManager) SetTrieCache(stateRoot string, t mt_trie.StateTrie) {
	cm.trieCacheMutex.Lock()
	defer cm.trieCacheMutex.Unlock()
	if _, ok := cm.trieCache[stateRoot]; !ok {
		cm.trieCacheKeys = append(cm.trieCacheKeys, stateRoot)
		// Evict oldest if cache gets too large (max 32 blocks)
		if len(cm.trieCacheKeys) > 32 {
			oldestKey := cm.trieCacheKeys[0]
			delete(cm.trieCache, oldestKey)
			// G-C4 FIX: Copy to new slice instead of sub-slicing.
			// cm.trieCacheKeys[1:] creates a sub-slice that keeps the original
			// backing array alive, preventing GC of evicted string keys.
			// Over millions of blocks, this leaks memory proportional to block count.
			newKeys := make([]string, len(cm.trieCacheKeys)-1)
			copy(newKeys, cm.trieCacheKeys[1:])
			cm.trieCacheKeys = newKeys
		}
	}
	cm.trieCache[stateRoot] = t
}

// GetAccountStateDBCache retrieves a cached AccountStateDB by its hex state root.
func (cm *CacheManager) GetAccountStateDBCache(stateRoot string) (*account_state_db.AccountStateDB, bool) {
	cm.accountDBCacheMutex.RLock()
	defer cm.accountDBCacheMutex.RUnlock()
	db, ok := cm.accountDBCache[stateRoot]
	return db, ok
}

// SetAccountStateDBCache stores an AccountStateDB by its hex state root.
// Limits the cache to 32 entries to prevent memory leaks (same policy as trieCache).
func (cm *CacheManager) SetAccountStateDBCache(stateRoot string, db *account_state_db.AccountStateDB) {
	cm.accountDBCacheMutex.Lock()
	defer cm.accountDBCacheMutex.Unlock()
	if _, ok := cm.accountDBCache[stateRoot]; !ok {
		cm.accountDBCacheKeys = append(cm.accountDBCacheKeys, stateRoot)
		if len(cm.accountDBCacheKeys) > 32 {
			oldestKey := cm.accountDBCacheKeys[0]
			delete(cm.accountDBCache, oldestKey)
			newKeys := make([]string, len(cm.accountDBCacheKeys)-1)
			copy(newKeys, cm.accountDBCacheKeys[1:])
			cm.accountDBCacheKeys = newKeys
		}
	}
	cm.accountDBCache[stateRoot] = db
}
