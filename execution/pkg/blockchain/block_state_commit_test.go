package blockchain

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

func newCommitTestState(t *testing.T) (*ChainState, *block.Block) {
	t.Helper()
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })
	header := block.NewBlockHeader(common.Hash{}, 1, common.Hash{}, common.Hash{}, common.Hash{}, common.Address{}, 0, common.Hash{}, 0)
	cs, err := NewChainStateRemote(header, storage.NewDummyStorage(""), storage.NewDummyStorage(""), storage.NewDummyStorage(""), map[common.Address]struct{}{})
	require.NoError(t, err)
	return cs, block.NewBlock(header, nil, nil)
}

// withNilBlockChain runs fn with the BlockChain singleton unset (as during crash recovery before InitBlockChain),
// restoring it afterwards.
func withNilBlockChain(t *testing.T, fn func()) {
	t.Helper()
	prev := blockChainInstance
	blockChainInstance = nil
	defer func() { blockChainInstance = prev }()
	fn()
}

// Crash recovery used to dereference the nil singleton (panic). A commit that would persist the block or its
// mappings must now fail closed instead of reporting success while skipping the mappings.
func TestCommitBlockState_NilSingleton_FailsClosedForPersistingCommits(t *testing.T) {
	for name, opt := range map[string]CommitOption{
		"persist":    WithPersistToDB(),
		"txMapping":  WithSaveTxMapping(),
		"commitMaps": WithCommitMappings(),
	} {
		t.Run(name, func(t *testing.T) {
			cs, blk := newCommitTestState(t)
			withNilBlockChain(t, func() {
				assert.NotPanics(t, func() {
					_, err := cs.CommitBlockState(blk, WithRebuildTries(), opt)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "blockchain singleton not initialized")
				})
				// And without the rebuild flag.
				assert.NotPanics(t, func() {
					_, err := cs.CommitBlockState(blk, opt)
					require.Error(t, err)
				})
			})
		})
	}
	// No options at all is not a rebuild-only recovery commit either.
	cs, blk := newCommitTestState(t)
	withNilBlockChain(t, func() {
		_, err := cs.CommitBlockState(blk)
		require.Error(t, err)
	})
}

// Only a rebuild-only recovery commit may run without the singleton.
func TestCommitConfig_RequiresBlockChain(t *testing.T) {
	cfgOf := func(opts ...CommitOption) *commitConfig {
		c := &commitConfig{}
		for _, o := range opts {
			o(c)
		}
		return c
	}
	assert.False(t, cfgOf(WithRebuildTries()).requiresBlockChain(), "rebuild-only recovery commit is the one allowed case")
	assert.True(t, cfgOf().requiresBlockChain(), "no options")
	assert.True(t, cfgOf(WithPersistToDB()).requiresBlockChain())
	assert.True(t, cfgOf(WithSaveTxMapping()).requiresBlockChain())
	assert.True(t, cfgOf(WithCommitMappings()).requiresBlockChain())
	assert.True(t, cfgOf(WithRebuildTries(), WithPersistToDB()).requiresBlockChain())
	assert.True(t, cfgOf(WithRebuildTries(), WithSaveTxMapping(), WithCommitMappings()).requiresBlockChain())
}

func setupFullCommitTestEnv(t *testing.T) (*ChainState, *storage.StorageManager) {
	t.Helper()
	cs, _ := newCommitTestState(t)
	sm := storage.NewStorageManager()
	for _, add := range []func(storage.Storage) error{
		sm.AddStorageAccount, sm.AddStorageTransaction, sm.AddStorageBackupDeviceKey, sm.AddStorageReceipt,
		sm.AddStorageSmartContract, sm.AddStorageCode, sm.AddStorageDatabaseTrie, sm.AddStorageBlock,
		sm.AddStorageBackupDb, sm.AddStorageMapping, sm.AddStorageStake,
	} {
		require.NoError(t, add(storage.NewMemoryDb()))
	}
	cs.storageManager = sm
	cs.SetStakeStateDB(state_db.NewStakeStateDB(cs.GetAccountStateDB().Trie(), sm.GetStorageStake()))
	cs.blockDatabase = block.NewBlockDatabase(sm.GetStorageBlock())
	return cs, sm
}

func TestCommitBlockState_TrieRebuildFailureDoesNotMutateBlockDBOrMappings(t *testing.T) {
	cs, sm := setupFullCommitTestEnv(t)
	prevBC := blockChainInstance
	prevHeight := storage.GetLastBlockNumber()
	defer func() {
		blockChainInstance = prevBC
		storage.ResetAllBlockCounters(prevHeight)
	}()

	bc := newTestBlockChain()
	bc.storageManager = sm
	bc.blockDatabase = cs.blockDatabase
	blockChainInstance = bc

	accRoot := cs.GetAccountStateDB().GetOriginRootHash()
	stakeRoot := cs.GetStakeStateDB().GetOriginRootHash()

	storage.ResetAllBlockCounters(0)
	hdr1 := block.NewBlockHeader(common.Hash{}, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 100, common.Hash{}, 0)
	blk1 := block.NewBlock(hdr1, nil, nil)
	_, err := cs.CommitBlockState(blk1, WithPersistToDB(), WithSaveTxMapping(), WithCommitMappings())
	require.NoError(t, err)
	require.Equal(t, uint64(1), storage.GetLastBlockNumber())

	missingAccRoot := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000002")
	tx2 := common.HexToHash("0xaaaa000000000000000000000000000000000000000000000000000000000002")
	hdr2 := block.NewBlockHeader(blk1.Header().Hash(), 2, missingAccRoot, stakeRoot, common.Hash{}, common.Address{}, 200, common.Hash{}, 0)
	blk2 := block.NewBlock(hdr2, []common.Hash{tx2}, nil)

	_, err = cs.CommitBlockState(blk2, WithPersistToDB(), WithSaveTxMapping(), WithRebuildTries(), WithCommitMappings())
	require.Error(t, err, "CommitBlockState must fail closed when MPT trie node for AccountStatesRoot is missing")

	assert.Equal(t, uint64(1), storage.GetLastBlockNumber())
	lastBlk, lbErr := cs.blockDatabase.GetLastBlock()
	require.NoError(t, lbErr)
	require.NotNil(t, lastBlk)
	assert.Equal(t, uint64(1), lastBlk.Header().BlockNumber())

	_, getBlk2Err := cs.blockDatabase.GetBlockByHash(blk2.Header().Hash())
	assert.Error(t, getBlk2Err, "failed block #2 must not be saved in BlockDatabase")

	_, hasMap2 := blockChainInstance.GetBlockHashByNumberFast(2)
	assert.False(t, hasMap2, "failed block #2 must not leave blockNumber_2 mapping")

	_, hasTx2 := blockChainInstance.GetBlockNumberByTxHashFast(tx2)
	assert.False(t, hasTx2, "failed block #2 must not leave txHash mapping")

	curHdr := cs.GetcurrentBlockHeader()
	require.NotNil(t, curHdr)
	assert.Equal(t, uint64(1), (*curHdr).BlockNumber())
}

func TestCommitBlockState_DurabilityBarrierFailureRollsBackBlockDBAndMappings(t *testing.T) {
	cs, sm := setupFullCommitTestEnv(t)
	prevBC := blockChainInstance
	prevHeight := storage.GetLastBlockNumber()
	defer func() {
		blockChainInstance = prevBC
		storage.ResetAllBlockCounters(prevHeight)
	}()

	accRoot := cs.GetAccountStateDB().GetOriginRootHash()
	stakeRoot := cs.GetStakeStateDB().GetOriginRootHash()

	baseDB := sm.GetStorageBlock()
	goodBlockDB := block.NewBlockDatabase(baseDB)
	hdr1 := block.NewBlockHeader(common.Hash{}, 1, accRoot, stakeRoot, common.Hash{}, common.Address{}, 100, common.Hash{}, 0)
	blk1 := block.NewBlock(hdr1, nil, nil)
	require.NoError(t, goodBlockDB.SaveLastBlock(blk1))

	bc := newTestBlockChain()
	bc.storageManager = sm
	bc.blockDatabase = goodBlockDB
	blockChainInstance = bc
	require.NoError(t, blockChainInstance.SetBlockNumberToHash(1, blk1.Header().Hash()))
	hdr1Iface := blk1.Header()
	cs.SetcurrentBlockHeader(&hdr1Iface)
	storage.ResetAllBlockCounters(1)

	// Swap in failing SyncDurable wrapper over baseDB for block #2 commit
	failingDB := &auditFailedDurableStorage{Storage: baseDB}
	cs.blockDatabase = block.NewBlockDatabase(failingDB)
	bc.blockDatabase = cs.blockDatabase

	tx2 := common.HexToHash("0xbbbb000000000000000000000000000000000000000000000000000000000002")
	hdr2 := block.NewBlockHeader(blk1.Header().Hash(), 2, accRoot, stakeRoot, common.Hash{}, common.Address{}, 200, common.Hash{}, 0)
	blk2 := block.NewBlock(hdr2, []common.Hash{tx2}, nil)

	_, err := cs.CommitBlockState(blk2, WithPersistToDB(), WithSaveTxMapping())
	require.Error(t, err)

	assert.Equal(t, uint64(1), storage.GetLastBlockNumber())

	lastBlk, lbErr := cs.blockDatabase.GetLastBlock()
	require.NoError(t, lbErr)
	require.NotNil(t, lastBlk)
	assert.Equal(t, uint64(1), lastBlk.Header().BlockNumber(), "lastBlock must roll back to block #1")

	_, getBlk2Err := cs.blockDatabase.GetBlockByHash(blk2.Header().Hash())
	assert.Error(t, getBlk2Err, "block #2 raw bytes must be removed on durability failure")

	_, hasMap2 := blockChainInstance.GetBlockHashByNumberFast(2)
	assert.False(t, hasMap2, "blockNumber_2 mapping must be rolled back")

	_, hasTx2 := blockChainInstance.GetBlockNumberByTxHashFast(tx2)
	assert.False(t, hasTx2, "tx2 mapping must be rolled back")

	curHdr := cs.GetcurrentBlockHeader()
	require.NotNil(t, curHdr)
	assert.Equal(t, uint64(1), (*curHdr).BlockNumber(), "currentBlockHeader must roll back to block #1")
}
