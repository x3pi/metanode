package blockchain

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
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
