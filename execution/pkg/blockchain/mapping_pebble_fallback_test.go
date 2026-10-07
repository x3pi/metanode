package blockchain

import (
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPebbleDBFallbackWhenEvictedFromRAM verifies that when mapping entries are
// evicted or deleted from in-memory RAM caches (txHashToBlockNumber / ethHashMapBlsHash),
// GetBlockNumberByTxHashFast and GetEthHashMapblsHash transparently fall back to
// reading from the underlying persistent Pebble DB storage and re-populate the cache.
func TestPebbleDBFallbackWhenEvictedFromRAM(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "pebble_mapping_test")
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

	// 1. Prepare sample transaction hash and hash mapping
	txHash := common.HexToHash("0xaaa111222333444555666777888999aaabbbcccdddeeefff0001112223334445")
	targetBlockNum := uint64(42)

	ethHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	blsHash := common.HexToHash("0xfedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321")

	// 2. Commit mappings to Pebble DB via BlockChain write path
	require.NoError(t, bc.SetTxHashMapBlockNumberBatch([]common.Hash{txHash}, targetBlockNum))
	require.NoError(t, bc.SetEthHashMapblsHash(ethHash, blsHash))
	require.NoError(t, bc.Commit())
	require.NoError(t, pebbleDb.Flush())

	// 3. Confirm both entries exist in RAM cache immediately after commit
	v1, ok1 := bc.txHashToBlockNumber.Load(txHash)
	require.True(t, ok1, "txHash must be in RAM cache")
	assert.Equal(t, targetBlockNum, v1.(cachedUint64).value)

	v2, ok2 := bc.ethHashMapBlsHash.Load(ethHash)
	require.True(t, ok2, "ethHash must be in RAM cache")
	assert.Equal(t, blsHash, v2.(cachedHash).hash)

	// 4. EVICT FROM RAM: simulate cache eviction, TTL expiry, or bounded cache eviction
	bc.txHashToBlockNumber.Delete(txHash)
	bc.ethHashMapBlsHash.Delete(ethHash)

	_, ok1AfterEvict := bc.txHashToBlockNumber.Load(txHash)
	require.False(t, ok1AfterEvict, "txHash must NOT be in RAM after eviction")
	_, ok2AfterEvict := bc.ethHashMapBlsHash.Load(ethHash)
	require.False(t, ok2AfterEvict, "ethHash must NOT be in RAM after eviction")

	// 5. Query via Fast lookup methods — verify fallback to Pebble DB disk read
	bnRead, okBn := bc.GetBlockNumberByTxHashFast(txHash)
	require.True(t, okBn, "GetBlockNumberByTxHashFast must succeed via Pebble DB fallback")
	assert.Equal(t, targetBlockNum, bnRead)

	blsRead, okBls := bc.GetEthHashMapblsHash(ethHash)
	require.True(t, okBls, "GetEthHashMapblsHash must succeed via Pebble DB fallback")
	assert.Equal(t, blsHash, blsRead)

	// 6. Verify that querying re-populated the in-memory RAM cache
	v1Reload, ok1Reload := bc.txHashToBlockNumber.Load(txHash)
	require.True(t, ok1Reload, "txHash must be re-populated in RAM after Pebble DB fallback")
	assert.Equal(t, targetBlockNum, v1Reload.(cachedUint64).value)

	v2Reload, ok2Reload := bc.ethHashMapBlsHash.Load(ethHash)
	require.True(t, ok2Reload, "ethHash must be re-populated in RAM after Pebble DB fallback")
	assert.Equal(t, blsHash, v2Reload.(cachedHash).hash)

	// 7. Test persistent durability across restart: open new BlockChain instance on existing Pebble DB
	sm2 := storage.NewStorageManager()
	require.NoError(t, sm2.AddStorageMapping(pebbleDb))
	bcRestart := newTestBlockChain()
	bcRestart.storageManager = sm2
	bcRestart.blockDatabase = block.NewBlockDatabase(blockStore)

	// In the freshly started BlockChain, RAM caches are completely empty
	_, okRestart1 := bcRestart.txHashToBlockNumber.Load(txHash)
	require.False(t, okRestart1, "New instance must start with empty RAM cache")
	_, okRestart2 := bcRestart.ethHashMapBlsHash.Load(ethHash)
	require.False(t, okRestart2, "New instance must start with empty RAM cache")

	// Querying must read from Pebble DB disk directly
	bnRestart, okBnRestart := bcRestart.GetBlockNumberByTxHashFast(txHash)
	require.True(t, okBnRestart, "Restarted instance must successfully read tx blockNumber from Pebble DB")
	assert.Equal(t, targetBlockNum, bnRestart)

	blsRestart, okBlsRestart := bcRestart.GetEthHashMapblsHash(ethHash)
	require.True(t, okBlsRestart, "Restarted instance must successfully read blsHash from Pebble DB")
	assert.Equal(t, blsHash, blsRestart)
}
