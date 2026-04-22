package trie

import (
	"sync"

	e_common "github.com/ethereum/go-ethereum/common"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	trie_db "github.com/meta-node-blockchain/meta-node/pkg/trie/db"
)

// ═══════════════════════════════════════════════════════════════════════════
// GLOBAL TRIE NODE CACHE
//
// This LRU cache persists across trie.Commit() → trie.New() cycles.
// After Commit(), a new trie is created from the root hash where ALL nodes
// are HashNodes (requiring PebbleDB reads to resolve). This cache stores
// the raw blob bytes keyed by node hash, so subsequent resolveAndTrack()
// calls hit memory instead of disk.
//
// FORK-SAFETY: This is a read-cache only. It caches the raw bytes that
// would be returned by PebbleDB.Get(hash). The trie logic is identical
// whether the blob comes from cache or disk.
//
// Size: 2M entries × ~200 bytes avg = ~400MB RAM (server has 157GB)
// ═══════════════════════════════════════════════════════════════════════════
var (
	globalTrieNodeCacheOnce sync.Once
	globalTrieNodeCache     *lru.Cache[e_common.Hash, []byte]
)

func getGlobalTrieNodeCache() *lru.Cache[e_common.Hash, []byte] {
	globalTrieNodeCacheOnce.Do(func() {
		cache, err := lru.New[e_common.Hash, []byte](2_000_000)
		if err != nil {
			logger.Error("Failed to create global trie node cache: %v", err)
			return
		}
		logger.Info("✅ [TRIE] Created global trie node cache (2M entries)")
		globalTrieNodeCache = cache
	})
	return globalTrieNodeCache
}

type TrieReader struct {
	db trie_db.DB
}

// newTrieReader initializes the trie reader with the given node reader.
func newTrieReader(db trie_db.DB) (*TrieReader, error) {
	// Ensure global cache is initialized
	getGlobalTrieNodeCache()
	return &TrieReader{
		db: db,
	}, nil
}

// node retrieves the rlp-encoded trie node with the provided trie node
// information. An MissingNodeError will be returned in case the node is
// not found or any error is encountered.
//
// OPTIMIZATION: Checks global in-memory LRU cache BEFORE PebbleDB read.
// Cache survives trie.Commit() → trie.New() cycles, preventing the
// re-resolve problem where all nodes become HashNodes after each block.
func (r *TrieReader) node(path []byte, hash e_common.Hash) ([]byte, error) {
	// Check global cache first
	if cache := getGlobalTrieNodeCache(); cache != nil {
		if blob, ok := cache.Get(hash); ok {
			return blob, nil
		}
	}

	// Cache miss — read from PebbleDB
	blob, err := r.db.Get(hash.Bytes())
	if err != nil || len(blob) == 0 {
		return nil, &MissingNodeError{
			NodeHash: hash, Path: path, err: err,
		}
	}

	// Store in cache for future blocks
	if cache := getGlobalTrieNodeCache(); cache != nil {
		cache.Add(hash, blob)
	}

	return blob, nil
}
