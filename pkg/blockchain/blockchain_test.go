package blockchain

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestBlockChain creates a minimal BlockChain for cache/pruning tests.
// Does NOT require a real BlockDatabase or StorageManager.
func newTestBlockChain() *BlockChain {
	return &BlockChain{
		blockCache:             new(sync.Map),
		receiptsCache:          new(sync.Map),
		txsCache:               new(sync.Map),
		blockNumberToHashCache: new(sync.Map),
		txHashToBlockNumber:    new(sync.Map),
		ethHashMapBlsHash:      new(sync.Map),
		dirtyStorage:           new(sync.Map),
		stopCleanup:            make(chan struct{}),
	}
}

func testTxHash(b byte) common.Hash {
	h := common.Hash{}
	h[0] = b
	h[31] = b
	return h
}

func testBlockHash(b byte) common.Hash {
	h := common.Hash{}
	h[0] = 0xBB
	h[31] = b
	return h
}

// ══════════════════════════════════════════════════════════════════════════════
// TxCache Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestBlockChain_AddTxToCache_And_Get(t *testing.T) {
	bc := newTestBlockChain()

	txHash := testTxHash(0x01)
	rawTx := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	bc.AddTxToCache(txHash, rawTx)

	got, ok := bc.GetTxFromCache(txHash)
	require.True(t, ok, "should find cached tx")
	assert.Equal(t, rawTx, got)
}

func TestBlockChain_GetTxFromCache_Miss(t *testing.T) {
	bc := newTestBlockChain()

	got, ok := bc.GetTxFromCache(testTxHash(0x99))
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestBlockChain_AddTxToCache_Overwrite(t *testing.T) {
	bc := newTestBlockChain()

	txHash := testTxHash(0x01)
	bc.AddTxToCache(txHash, []byte{0x01})
	bc.AddTxToCache(txHash, []byte{0x02})

	got, ok := bc.GetTxFromCache(txHash)
	require.True(t, ok)
	assert.Equal(t, []byte{0x02}, got, "overwrite should return latest value")
}

func TestBlockChain_AddTxToCache_CopiesData(t *testing.T) {
	bc := newTestBlockChain()

	txHash := testTxHash(0x01)
	original := []byte{0x01, 0x02, 0x03}
	bc.AddTxToCache(txHash, original)

	// Mutate original — cached copy should be unaffected
	original[0] = 0xFF

	got, ok := bc.GetTxFromCache(txHash)
	require.True(t, ok)
	assert.Equal(t, byte(0x01), got[0], "cache should hold independent copy")
}

func TestBlockChain_GetTxFromCache_ReturnsCopy(t *testing.T) {
	bc := newTestBlockChain()

	txHash := testTxHash(0x01)
	bc.AddTxToCache(txHash, []byte{0x01, 0x02, 0x03})

	got1, _ := bc.GetTxFromCache(txHash)
	got1[0] = 0xFF // mutate returned copy

	got2, _ := bc.GetTxFromCache(txHash)
	assert.Equal(t, byte(0x01), got2[0], "GetTxFromCache should return independent copy")
}

func TestBlockChain_AddTxToCache_NilTxsCache(t *testing.T) {
	bc := newTestBlockChain()
	bc.txsCache = nil

	// Should not panic
	bc.AddTxToCache(testTxHash(0x01), []byte{0x01})

	got, ok := bc.GetTxFromCache(testTxHash(0x01))
	assert.False(t, ok)
	assert.Nil(t, got)
}

// ══════════════════════════════════════════════════════════════════════════════
// Pruning Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestBlockChain_PruneTxCache_StaleRemoved(t *testing.T) {
	bc := newTestBlockChain()

	// Add stale entry
	staleHash := testTxHash(0x01)
	bc.txsCache.Store(staleHash, cachedTx{
		raw:     []byte{0x01},
		addedAt: time.Now().Add(-5 * time.Minute), // older than TTL
	})

	// Add fresh entry
	freshHash := testTxHash(0x02)
	bc.txsCache.Store(freshHash, cachedTx{
		raw:     []byte{0x02},
		addedAt: time.Now(), // just added
	})

	bc.pruneTxCache(time.Now().Add(-txCacheTTL))

	_, staleFound := bc.txsCache.Load(staleHash)
	assert.False(t, staleFound, "stale entry should be pruned")

	_, freshFound := bc.txsCache.Load(freshHash)
	assert.True(t, freshFound, "fresh entry should survive")
}

func TestBlockChain_PruneBlockNumberCache_StaleRemoved(t *testing.T) {
	bc := newTestBlockChain()

	// Add stale mapping
	bc.blockNumberToHashCache.Store(uint64(100), cachedHash{
		hash:    testBlockHash(0x01),
		addedAt: time.Now().Add(-1 * time.Hour),
	})

	// Add fresh mapping
	bc.blockNumberToHashCache.Store(uint64(200), cachedHash{
		hash:    testBlockHash(0x02),
		addedAt: time.Now(),
	})

	bc.pruneBlockNumberCache(time.Now().Add(-mappingCacheTTL))

	_, staleFound := bc.blockNumberToHashCache.Load(uint64(100))
	assert.False(t, staleFound, "stale mapping should be pruned")

	_, freshFound := bc.blockNumberToHashCache.Load(uint64(200))
	assert.True(t, freshFound, "fresh mapping should survive")
}

func TestBlockChain_PruneTxCache_InvalidType(t *testing.T) {
	bc := newTestBlockChain()

	// Store a value with wrong type — should be cleaned up
	bc.txsCache.Store(testTxHash(0x01), "invalid_type")

	bc.pruneTxCache(time.Now())

	_, found := bc.txsCache.Load(testTxHash(0x01))
	assert.False(t, found, "invalid type entries should be removed")
}

// ══════════════════════════════════════════════════════════════════════════════
// Mapping Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestBlockChain_SetBlockNumberToHash_And_GetFromCache(t *testing.T) {
	bc := newTestBlockChain()

	blockHash := testBlockHash(0x01)
	err := bc.SetBlockNumberToHash(42, blockHash)
	require.NoError(t, err)

	got, ok := bc.GetBlockHashByNumber(42)
	require.True(t, ok)
	assert.Equal(t, blockHash, got)
}

func TestBlockChain_GetBlockHashByNumber_ExpiredCacheEntry(t *testing.T) {
	bc := newTestBlockChain()

	// Store an expired cache entry
	bc.blockNumberToHashCache.Store(uint64(999), cachedHash{
		hash:    testBlockHash(0x01),
		addedAt: time.Now().Add(-1 * time.Hour), // expired
	})

	// GetBlockHashByNumber will find expired entry, delete it, then fall through to DB
	// We can't test the DB path without a StorageManager, so just verify
	// the cache was cleaned up via pruning
	bc.pruneBlockNumberCache(time.Now().Add(-mappingCacheTTL))
	_, found := bc.blockNumberToHashCache.Load(uint64(999))
	assert.False(t, found, "expired entry should be pruned")
}

// ══════════════════════════════════════════════════════════════════════════════
// DirtyStorage / Commit / Discard Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestBlockChain_StoreToDirty_And_Discard(t *testing.T) {
	bc := newTestBlockChain()

	bc.storeToDirty("key1", []byte{0x01})
	bc.storeToDirty("key2", []byte{0x02})

	// Verify entries exist
	_, ok1 := bc.dirtyStorage.Load("key1")
	_, ok2 := bc.dirtyStorage.Load("key2")
	assert.True(t, ok1)
	assert.True(t, ok2)

	// Discard should clear all dirty entries
	bc.Discard()

	_, ok1 = bc.dirtyStorage.Load("key1")
	_, ok2 = bc.dirtyStorage.Load("key2")
	assert.False(t, ok1, "dirty entries should be discarded")
	assert.False(t, ok2, "dirty entries should be discarded")
}

func TestBlockChain_MappingBatch(t *testing.T) {
	bc := newTestBlockChain()

	assert.Nil(t, bc.GetMappingBatch(), "should be nil initially")

	bc.SetMappingBatch([]byte{0x01, 0x02})
	got := bc.GetMappingBatch()
	assert.Equal(t, []byte{0x01, 0x02}, got)

	// Second call should return nil (one-shot)
	assert.Nil(t, bc.GetMappingBatch())
}

// ══════════════════════════════════════════════════════════════════════════════
// Cleanup Worker Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestBlockChain_Stop(t *testing.T) {
	bc := newTestBlockChain()
	bc.StartCleanupWorker()

	// Should stop gracefully without deadlock
	bc.Stop()
}

// ══════════════════════════════════════════════════════════════════════════════
// Concurrent Access Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestBlockChain_Concurrent_TxCache(t *testing.T) {
	bc := newTestBlockChain()

	const numGoroutines = 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	// Writers
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			bc.AddTxToCache(testTxHash(byte(idx)), []byte{byte(idx)})
		}(i)
	}

	// Readers
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			bc.GetTxFromCache(testTxHash(byte(idx)))
		}(i)
	}

	wg.Wait()
	// Should complete without race conditions
}

func TestBlockChain_Concurrent_DirtyStorage(t *testing.T) {
	bc := newTestBlockChain()

	const numGoroutines = 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			bc.storeToDirty(string(rune(idx)), []byte{byte(idx)})
		}(i)
	}

	wg.Wait()
	bc.Discard()
}
