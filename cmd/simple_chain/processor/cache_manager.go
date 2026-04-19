package processor

import (
	"sync"

	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// CacheManager handles the caching of StateTrie nodes internally used by the BlockProcessor.
// It was extracted from the monolithic BlockProcessor struct.
type CacheManager struct {
	// Cached account Trie nodes parsed during GetAccountState HTTP requests
	trieCache      map[string]mt_trie.StateTrie
	trieCacheKeys  []string
	trieCacheMutex sync.RWMutex
}

// NewCacheManager creates and initializes a new CacheManager.
func NewCacheManager() *CacheManager {
	return &CacheManager{
		trieCache:     make(map[string]mt_trie.StateTrie),
		trieCacheKeys: make([]string, 0),
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
