package processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// readRouteHarness builds a BlockProcessor / StateProcessor over a real (in-memory) ChainState, so the state-read
// routes run against real dependencies instead of nil ones (a nil dependency would show up as a panic on a valid body too).
func readRouteHarness(t *testing.T) (*BlockProcessor, *StateProcessor) {
	t.Helper()
	prev := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prev) })

	sm := storage.NewStorageManager()
	for _, add := range []func(storage.Storage) error{
		sm.AddStorageAccount, sm.AddStorageTransaction, sm.AddStorageBackupDeviceKey, sm.AddStorageReceipt,
		sm.AddStorageSmartContract, sm.AddStorageCode, sm.AddStorageDatabaseTrie, sm.AddStorageBlock,
		sm.AddStorageBackupDb, sm.AddStorageMapping, sm.AddStorageStake,
	} {
		require.NoError(t, add(storage.NewMemoryDb()))
	}
	blockDB := block.NewBlockDatabase(storage.NewMemoryDb())
	header := block.NewBlockHeader(common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{}, common.Address{}, 0, common.Hash{}, 0)
	cfg := &config.SimpleChainConfig{ChainId: big.NewInt(991)}
	cs, err := blockchain.NewChainStateWithGenesis(sm, blockDB, header, cfg, map[common.Address]struct{}{}, &config.GenesisConfig{}, "skip_epoch_data")
	require.NoError(t, err)
	blockchain.InitBlockChain(100, blockDB, sm)

	bp := &BlockProcessor{
		chainState: cs, storageManager: sm, config: cfg, messageSender: NewMockMessageSender(),
		CacheManager: NewCacheManager(), BlockBuffers: NewBlockBuffers(), ReceiptTracker: NewReceiptTracker(), ConsensusContext: NewConsensusContext(),
	}
	bp.lastBlock.Store(block.NewBlock(header, nil, nil))
	return bp, NewStateProcessor(NewMockMessageSender(), nil, bp, sm)
}

func TestReadRouteHarness_Builds(t *testing.T) {
	bp, sp := readRouteHarness(t)
	require.NotNil(t, bp.chainState.GetBlockDatabase())
	require.NotNil(t, sp)
}
