package blockchain

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	mtn_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mapStorage is a minimal string-keyed storage used for the mapping DB in tests.
// storage.MemoryDB truncates keys to 32 bytes, which would collide for the long
// "txHashPrefix0x..." / "ethHashMapBlsHashPrefix0x..." keys, so it is not used here.
type mapStorage struct {
	storage.Storage
	mu   sync.RWMutex
	data map[string][]byte
}

func newMapStorage() *mapStorage { return &mapStorage{data: make(map[string][]byte)} }

func (m *mapStorage) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}

func (m *mapStorage) Put(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[string(key)] = append([]byte(nil), value...)
	return nil
}

func (m *mapStorage) BatchPut(kvs [][2][]byte) error {
	for _, kv := range kvs {
		if err := m.Put(kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

func (m *mapStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *mapStorage) Flush() error { return nil }

// rebuildFixture holds a synthetic chain of blocks 0..n-1 with optional transactions.
type rebuildFixture struct {
	bc      *BlockChain
	mapping *mapStorage
	blockDB *block.BlockDatabase
	blocks  []mtn_types.Block
	txs     map[uint64][]mtn_types.Transaction
}

func newRebuildFixture(t *testing.T, n int, txPerBlock int) *rebuildFixture {
	t.Helper()

	// Use the in-process MPT backend so opening a txDB does not need the NOMT FFI runtime.
	prevBackend := p_trie.GetStateBackend()
	p_trie.SetStateBackend(p_trie.BackendMPT)
	t.Cleanup(func() { p_trie.SetStateBackend(prevBackend) })

	mapping := newMapStorage()
	txStore := storage.NewMemoryDb() // keys are 32-byte tx hashes -> no truncation
	blockStore := storage.NewMemoryDb()

	sm := storage.NewStorageManager()
	require.NoError(t, sm.AddStorageMapping(mapping))
	require.NoError(t, sm.AddStorageTransaction(txStore))

	bc := newTestBlockChain()
	bc.storageManager = sm
	bc.blockDatabase = block.NewBlockDatabase(blockStore)

	f := &rebuildFixture{
		bc:      bc,
		mapping: mapping,
		blockDB: bc.blockDatabase,
		txs:     make(map[uint64][]mtn_types.Transaction),
	}

	from := common.HexToAddress("0x1000000000000000000000000000000000000001")
	to := common.HexToAddress("0x2000000000000000000000000000000000000002")
	parent := common.Hash{}
	nonce := uint64(0)
	for i := 0; i < n; i++ {
		bNum := uint64(i)
		var hashes []common.Hash
		for j := 0; j < txPerBlock; j++ {
			tx := transaction.NewTransaction(from, to, big.NewInt(1), 21000, 1, 0, nil, nil,
				common.Hash{}, common.Hash{}, nonce, 991)
			nonce++
			raw, err := tx.Marshal()
			require.NoError(t, err)
			// Empty TransactionsRoot -> txDB trie is empty, GetTransaction falls back to db.Get(hash).
			require.NoError(t, txStore.Put(tx.Hash().Bytes(), raw))
			hashes = append(hashes, tx.Hash())
			f.txs[bNum] = append(f.txs[bNum], tx)
		}
		header := block.NewBlockHeader(parent, bNum, common.Hash{}, common.Hash{}, common.Hash{},
			common.Address{}, 1000+bNum, common.Hash{}, 1)
		blk := block.NewBlock(header, hashes, nil)
		require.NoError(t, f.blockDB.SaveBlockByHash(blk))

		// Reload through the DB so the hash we compare against is the persisted one.
		stored, err := f.blockDB.GetBlockByHash(blk.Header().Hash())
		require.NoError(t, err)
		require.Equal(t, bNum, stored.Header().BlockNumber())
		f.blocks = append(f.blocks, stored)
		parent = stored.Header().Hash()
	}
	return f
}

func (f *rebuildFixture) last() mtn_types.Block { return f.blocks[len(f.blocks)-1] }

func (f *rebuildFixture) blockKey(n uint64) []byte {
	return []byte(fmt.Sprintf("%s%d", blockNumberPrefix, n))
}

func (f *rebuildFixture) persistedBlockHash(n uint64) (common.Hash, bool) {
	v, err := f.mapping.Get(f.blockKey(n))
	if err != nil || len(v) != common.HashLength {
		return common.Hash{}, false
	}
	return common.BytesToHash(v), true
}

func (f *rebuildFixture) persistedTxBlock(h common.Hash) (uint64, bool) {
	v, err := f.mapping.Get([]byte(txHashPrefix + h.Hex()))
	if err != nil || len(v) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(v), true
}

func (f *rebuildFixture) persistedEthMap(ethHash common.Hash) (common.Hash, bool) {
	v, err := f.mapping.Get([]byte(ethHashMapBlsHashPrefix + ethHash.Hex()))
	if err != nil || len(v) != common.HashLength {
		return common.Hash{}, false
	}
	return common.BytesToHash(v), true
}

// Missing block->hash, tx->block and eth->bls mappings are fully restored and persisted.
func TestRebuildMappingsFromBlock_RestoresAllMissingMappings(t *testing.T) {
	f := newRebuildFixture(t, 20, 3)

	rebuilt, err := f.bc.RebuildMappingsFromBlock(f.last(), 0)
	require.NoError(t, err)

	ethCount := 0
	for n, blk := range f.blocks {
		got, ok := f.persistedBlockHash(uint64(n))
		require.True(t, ok, "block #%d mapping missing", n)
		assert.Equal(t, blk.Header().Hash(), got)

		for _, tx := range f.txs[uint64(n)] {
			bn, ok := f.persistedTxBlock(tx.Hash())
			require.True(t, ok, "tx %s mapping missing", tx.Hash().Hex())
			assert.Equal(t, uint64(n), bn)

			if eh := tx.EthHash(); eh != (common.Hash{}) {
				ethCount++
				bls, ok := f.persistedEthMap(eh)
				require.True(t, ok, "eth->bls mapping missing for %s", eh.Hex())
				assert.Equal(t, tx.Hash(), bls)
			}
		}
	}
	require.Equal(t, 20*3, ethCount, "every legacy tx must expose a non-zero EthHash")
	assert.Equal(t, 20+20*3+ethCount, rebuilt)

	// Second pass is a no-op: everything intact.
	rebuilt, err = f.bc.RebuildMappingsFromBlock(f.last(), 0)
	require.NoError(t, err)
	assert.Equal(t, 0, rebuilt)
}

// maxBlocks=50 walks exactly 50 blocks and never loads the parent of the 50th block.
func TestRebuildMappingsFromBlock_MaxBlocksDoesNotReadParent(t *testing.T) {
	f := newRebuildFixture(t, 60, 0) // blocks 0..59; start 59 -> walks 59..10
	// Remove block #9 (parent of the 50th walked block). Reading it would error.
	require.NoError(t, f.blockDB.DeleteBlockByHash(f.blocks[9].Header().Hash()))

	rebuilt, err := f.bc.RebuildMappingsFromBlock(f.last(), 50)
	require.NoError(t, err)
	assert.Equal(t, 50, rebuilt)

	for n := uint64(10); n <= 59; n++ {
		_, ok := f.persistedBlockHash(n)
		assert.True(t, ok, "block #%d should be rebuilt", n)
	}
	_, ok := f.persistedBlockHash(9)
	assert.False(t, ok, "block #9 is outside the 50-block window")
}

// lastPruned>0: walk stops at lastPruned+1 without a "cannot walk to parent" error.
func TestRebuildMappingsFromBlock_StopsAtPrunedBoundary(t *testing.T) {
	f := newRebuildFixture(t, 40, 1)
	const lastPruned = 20
	for n := 0; n <= lastPruned; n++ {
		require.NoError(t, f.blockDB.DeleteBlockByHash(f.blocks[n].Header().Hash()))
	}
	f.bc.lastPrunedBlockNumber.Store(lastPruned)

	_, err := f.bc.RebuildMappingsFromBlock(f.last(), 0)
	require.NoError(t, err)

	for n := uint64(lastPruned + 1); n < 40; n++ {
		_, ok := f.persistedBlockHash(n)
		assert.True(t, ok, "block #%d should be rebuilt", n)
	}
	_, ok := f.persistedBlockHash(lastPruned)
	assert.False(t, ok, "pruned block must not be touched")
}

// An existing mapping with the wrong hash is corrected.
func TestRebuildMappingsFromBlock_CorrectsWrongHash(t *testing.T) {
	f := newRebuildFixture(t, 10, 0)
	// Pre-populate correct mappings, then corrupt block #5.
	for n, blk := range f.blocks {
		require.NoError(t, f.mapping.Put(f.blockKey(uint64(n)), blk.Header().Hash().Bytes()))
	}
	wrong := common.HexToHash("0xdeadbeef")
	require.NoError(t, f.mapping.Put(f.blockKey(5), wrong.Bytes()))

	rebuilt, err := f.bc.RebuildMappingsFromBlock(f.last(), 0)
	require.NoError(t, err)
	assert.Equal(t, 1, rebuilt)

	got, ok := f.persistedBlockHash(5)
	require.True(t, ok)
	assert.Equal(t, f.blocks[5].Header().Hash(), got)
}

// Missing parent returns an error (no panic). Documents current behaviour: entries rebuilt
// before the failure stay in dirtyStorage (not yet persisted) and are written by the next Commit.
func TestRebuildMappingsFromBlock_MissingParentReturnsError(t *testing.T) {
	f := newRebuildFixture(t, 30, 0)
	require.NoError(t, f.blockDB.DeleteBlockByHash(f.blocks[10].Header().Hash()))

	var rebuilt int
	var err error
	require.NotPanics(t, func() {
		rebuilt, err = f.bc.RebuildMappingsFromBlock(f.last(), 0)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot walk to parent of block #11")
	assert.Equal(t, 19, rebuilt) // blocks 29..11

	// Not persisted yet (batch < 5000, error path returns before final flush).
	_, ok := f.persistedBlockHash(11)
	assert.False(t, ok)

	// Next commit (normal block pipeline) persists the already-rebuilt entries.
	require.NoError(t, f.bc.Commit())
	for n := uint64(11); n < 30; n++ {
		got, ok := f.persistedBlockHash(n)
		require.True(t, ok, "block #%d should be persisted after Commit", n)
		assert.Equal(t, f.blocks[n].Header().Hash(), got)
	}
}

// GetBlockNumberByTxHash no longer lazily walks back: lost mapping -> (0, false).
func TestGetBlockNumberByTxHash_NoLazyWalkbackAfterMappingLoss(t *testing.T) {
	f := newRebuildFixture(t, 5, 2)
	_, err := f.bc.RebuildMappingsFromBlock(f.last(), 0)
	require.NoError(t, err)

	target := f.txs[3][0].Hash()
	bn, ok := f.bc.GetBlockNumberByTxHash(target)
	require.True(t, ok)
	assert.Equal(t, uint64(3), bn)

	// Simulate mapping loss in DB and in the in-memory cache.
	require.NoError(t, f.mapping.Delete([]byte(txHashPrefix+target.Hex())))
	f.bc.txHashToBlockNumber.Delete(target)

	bn, ok = f.bc.GetBlockNumberByTxHash(target)
	assert.False(t, ok, "no walkback fallback any more")
	assert.Equal(t, uint64(0), bn)
}
