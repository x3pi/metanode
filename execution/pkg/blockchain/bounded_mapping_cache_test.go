package blockchain

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to generate unique common.Hash
func makeTestHash(i int) common.Hash {
	var h common.Hash
	h[0] = byte(i >> 24)
	h[1] = byte(i >> 16)
	h[2] = byte(i >> 8)
	h[3] = byte(i)
	h[31] = 0xAA
	return h
}

// TestBoundedTwoGenMap_SwapAndFallbackPebble verifies that:
//  1. Filling MaxEntries triggers a pointer swap (old = current, current = new).
//  2. Old entries are still readable from old generation, and reading promotes them to current.
//  3. After two swaps, unread entries are evicted from RAM, but GetBlockNumberByTxHashFast
//     and GetEthHashMapblsHash return the correct values via Pebble DB fallback and re-populate RAM.
func TestBoundedTwoGenMap_SwapAndFallbackPebble(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "pebble_bounded_cache_test")
	pebbleDb, err := storage.NewShardelDB(dbDir, 1, 1, storage.TypePebbleDB, "")
	require.NoError(t, err)
	require.NoError(t, pebbleDb.Open())
	defer pebbleDb.Close()

	sm := storage.NewStorageManager()
	require.NoError(t, sm.AddStorageMapping(pebbleDb))

	blockStore := storage.NewMemoryDb()
	bc := newTestBlockChain()
	bc.storageManager = sm
	bc.blockDatabase = block.NewBlockDatabase(blockStore)

	// Set a small test capacity of 5 entries to test generation rotation deterministically
	const testCap = 5
	bc.txHashToBlockNumber = newTxHashToBlockNumberMapWithCap(testCap)
	bc.ethHashMapBlsHash = newEthHashMapBlsHashMapWithCap(testCap)

	// Generation 1: Store 5 entries (fill current map)
	var txHashes [15]common.Hash
	var ethHashes [15]common.Hash
	var blsHashes [15]common.Hash

	for i := 0; i < 15; i++ {
		txHashes[i] = makeTestHash(100 + i)
		ethHashes[i] = makeTestHash(200 + i)
		blsHashes[i] = makeTestHash(300 + i)
	}

	// Commit batch 0..4 to DB and RAM
	for i := 0; i < testCap; i++ {
		require.NoError(t, bc.SetTxHashMapBlockNumberBatch([]common.Hash{txHashes[i]}, uint64(i+1)))
		require.NoError(t, bc.SetEthHashMapblsHash(ethHashes[i], blsHashes[i]))
	}
	require.NoError(t, bc.Commit())
	require.NoError(t, pebbleDb.Flush())

	// Verify all 5 are in current generation, old is empty
	assert.Equal(t, testCap, len(bc.txHashToBlockNumber.current))
	assert.Equal(t, 0, len(bc.txHashToBlockNumber.old))

	// Generation 2: Adding 6th entry triggers first swap:
	// old receives generation 1 (entries 0..4), current gets entry 5.
	require.NoError(t, bc.SetTxHashMapBlockNumberBatch([]common.Hash{txHashes[5]}, uint64(6)))
	require.NoError(t, bc.SetEthHashMapblsHash(ethHashes[5], blsHashes[5]))
	require.NoError(t, bc.Commit())
	require.NoError(t, pebbleDb.Flush())

	assert.Equal(t, 1, len(bc.txHashToBlockNumber.current))
	assert.Equal(t, testCap, len(bc.txHashToBlockNumber.old))

	// Entry 0 is in old, entry 5 is in current. Both must be readable from RAM!
	vOld, okOld := bc.txHashToBlockNumber.Load(txHashes[0])
	require.True(t, okOld, "Entry 0 must still be readable from old generation")
	assert.Equal(t, uint64(1), vOld.(cachedUint64).value)

	// Notice: Loading entry 0 promoted it from old to current!
	_, inCurrent := bc.txHashToBlockNumber.current[txHashes[0]]
	assert.True(t, inCurrent, "Entry 0 should be promoted to current generation upon Load")

	// Now fill up current generation until second swap: add entries 6, 7, 8, 9, 10
	for i := 6; i <= 10; i++ {
		require.NoError(t, bc.SetTxHashMapBlockNumberBatch([]common.Hash{txHashes[i]}, uint64(i+1)))
		require.NoError(t, bc.SetEthHashMapblsHash(ethHashes[i], blsHashes[i]))
	}
	require.NoError(t, bc.Commit())
	require.NoError(t, pebbleDb.Flush())

	// Entry 1 was NEVER accessed during generation 2, so after second swap it was evicted from RAM
	_, okEvictedTx := bc.txHashToBlockNumber.Load(txHashes[1])
	assert.False(t, okEvictedTx, "Entry 1 must be evicted from RAM after two swaps without access")
	_, okEvictedEth := bc.ethHashMapBlsHash.Load(ethHashes[1])
	assert.False(t, okEvictedEth, "Eth entry 1 must be evicted from RAM after two swaps without access")

	// Querying evicted entry via GetBlockNumberByTxHashFast transparently falls back to Pebble DB
	bnRead, okBn := bc.GetBlockNumberByTxHashFast(txHashes[1])
	require.True(t, okBn, "GetBlockNumberByTxHashFast must succeed via Pebble DB fallback")
	assert.Equal(t, uint64(2), bnRead)

	// Querying evicted ethHash via GetEthHashMapblsHash transparently falls back to Pebble DB
	blsRead, okBls := bc.GetEthHashMapblsHash(ethHashes[1])
	require.True(t, okBls, "GetEthHashMapblsHash must succeed via Pebble DB fallback")
	assert.Equal(t, blsHashes[1], blsRead)

	// Verify that fallback read re-populated RAM cache
	vReloadTx, okReloadTx := bc.txHashToBlockNumber.Load(txHashes[1])
	require.True(t, okReloadTx, "txHash 1 must be re-populated in RAM after Pebble DB fallback")
	assert.Equal(t, uint64(2), vReloadTx.(cachedUint64).value)

	vReloadEth, okReloadEth := bc.ethHashMapBlsHash.Load(ethHashes[1])
	require.True(t, okReloadEth, "ethHash 1 must be re-populated in RAM after Pebble DB fallback")
	assert.Equal(t, blsHashes[1], vReloadEth.(cachedHash).hash)
}

// TestBoundedTwoGenMap_CapacityUpperBound_10xMaxEntries verifies that:
// When storing 10x MaxEntries into the map, total memory entries never exceed 2*MaxEntries.
func TestBoundedTwoGenMap_CapacityUpperBound_10xMaxEntries(t *testing.T) {
	const capacity = 1000
	const numEntries = 10 * capacity // 10,000 entries

	txMap := newTxHashToBlockNumberMapWithCap(capacity)
	ethMap := newEthHashMapBlsHashMapWithCap(capacity)

	for i := 0; i < numEntries; i++ {
		h := makeTestHash(i)
		txMap.Store(h, cachedUint64{value: uint64(i)})
		ethMap.Store(h, cachedHash{hash: h})

		// Invariant check at every 100th step
		if i%100 == 0 {
			require.LessOrEqual(t, txMap.Len(), 2*capacity, "txMap.Len() must stay <= 2*MaxEntries")
			require.LessOrEqual(t, ethMap.Len(), 2*capacity, "ethMap.Len() must stay <= 2*MaxEntries")
			require.LessOrEqual(t, len(txMap.current), capacity, "len(current) must stay <= MaxEntries")
			require.LessOrEqual(t, len(ethMap.current), capacity, "len(current) must stay <= MaxEntries")
		}
	}

	// Final verification after 10x inserts
	assert.LessOrEqual(t, txMap.Len(), 2*capacity)
	assert.LessOrEqual(t, ethMap.Len(), 2*capacity)
	assert.LessOrEqual(t, len(txMap.current), capacity)
	assert.LessOrEqual(t, len(txMap.old), capacity)
	assert.LessOrEqual(t, len(ethMap.current), capacity)
	assert.LessOrEqual(t, len(ethMap.old), capacity)

	// Verify StoreBatch also strictly respects 2*MaxEntries
	batchTx := make(map[common.Hash]cachedUint64, capacity*3)
	batchEth := make(map[common.Hash]cachedHash, capacity*3)
	for i := 0; i < capacity*3; i++ {
		h := makeTestHash(100000 + i)
		batchTx[h] = cachedUint64{value: uint64(i)}
		batchEth[h] = cachedHash{hash: h}
	}
	txMap.StoreBatch(batchTx)
	ethMap.StoreBatch(batchEth)

	assert.LessOrEqual(t, txMap.Len(), 2*capacity)
	assert.LessOrEqual(t, ethMap.Len(), 2*capacity)
}

// TestBoundedTwoGenMap_ConcurrentReadWriteRace verifies thread safety under intense
// concurrent read, write, batch store, and delete operations using go test -race.
func TestBoundedTwoGenMap_ConcurrentReadWriteRace(t *testing.T) {
	const capacity = 100
	txMap := newTxHashToBlockNumberMapWithCap(capacity)
	ethMap := newEthHashMapBlsHashMapWithCap(capacity)

	const numGoroutines = 20
	const opsPerGoroutine = 500

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 4)

	// Goroutine pool 1: Single Store
	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				idx := (gid*opsPerGoroutine + i) % 500
				h := makeTestHash(idx)
				txMap.Store(h, cachedUint64{value: uint64(idx)})
				ethMap.Store(h, cachedHash{hash: h})
			}
		}(g)
	}

	// Goroutine pool 2: Concurrent Load (triggering promotions from old to current)
	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				idx := (gid*opsPerGoroutine + i) % 500
				h := makeTestHash(idx)
				_, _ = txMap.Load(h)
				_, _ = ethMap.Load(h)
			}
		}(g)
	}

	// Goroutine pool 3: StoreBatch
	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine/10; i++ {
				batchTx := make(map[common.Hash]cachedUint64, 10)
				batchEth := make(map[common.Hash]cachedHash, 10)
				for j := 0; j < 10; j++ {
					idx := (gid*100 + i*10 + j) % 500
					h := makeTestHash(idx)
					batchTx[h] = cachedUint64{value: uint64(idx)}
					batchEth[h] = cachedHash{hash: h}
				}
				txMap.StoreBatch(batchTx)
				ethMap.StoreBatch(batchEth)
			}
		}(g)
	}

	// Goroutine pool 4: Concurrent Delete
	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				idx := (gid*opsPerGoroutine + i) % 500
				h := makeTestHash(idx)
				txMap.Delete(h)
				ethMap.Delete(h)
			}
		}(g)
	}

	wg.Wait()

	// Invariant must strictly hold after concurrent execution
	assert.LessOrEqual(t, txMap.Len(), 2*capacity)
	assert.LessOrEqual(t, ethMap.Len(), 2*capacity)
}

// TestBoundedTwoGenMap_DeleteFromBothGenerations verifies that Delete removes keys
// from both current and old maps.
func TestBoundedTwoGenMap_DeleteFromBothGenerations(t *testing.T) {
	const capacity = 5
	txMap := newTxHashToBlockNumberMapWithCap(capacity)

	h1 := makeTestHash(1)
	h2 := makeTestHash(2)
	txMap.Store(h1, cachedUint64{value: 1})
	txMap.Store(h2, cachedUint64{value: 2})

	// Fill to trigger swap so h1 and h2 move to old
	for i := 3; i <= 8; i++ {
		txMap.Store(makeTestHash(i), cachedUint64{value: uint64(i)})
	}

	// Verify h1 is in old
	_, inOld := txMap.old[h1]
	require.True(t, inOld, "h1 must be in old generation")

	// Delete h1
	txMap.Delete(h1)

	// Verify h1 deleted from old
	_, inOldAfter := txMap.old[h1]
	assert.False(t, inOldAfter, "h1 must be deleted from old generation")
	_, okLoad := txMap.Load(h1)
	assert.False(t, okLoad, "h1 must not be loadable after deletion")
}
