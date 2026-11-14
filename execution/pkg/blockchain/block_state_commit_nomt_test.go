package blockchain

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	account_state_db "github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// nomtPipelineEnv wires real NOMT-backed account/stake tries into a commit test ChainState.
type nomtPipelineEnv struct {
	cs                   *ChainState
	accTrie, stakeTrie   *trie.NomtStateTrie
	rootN_Acc, rootN_Stk common.Hash // roots carried by block N's header (being persisted by commitWorker)
	rootAhead_Acc        common.Hash // live roots after execution advanced to N+1
	rootAhead_Stk        common.Hash
}

func setupNomtPipelineEnv(t *testing.T) *nomtPipelineEnv {
	t.Helper()
	cs, sm := setupFullCommitTestEnv(t)
	prevBC := blockChainInstance
	prevHeight := storage.GetLastBlockNumber()
	t.Cleanup(func() {
		blockChainInstance = prevBC
		storage.ResetAllBlockCounters(prevHeight)
	})
	bc := newTestBlockChain()
	bc.storageManager = sm
	bc.blockDatabase = cs.blockDatabase
	blockChainInstance = bc
	storage.ResetAllBlockCounters(0)

	require.NoError(t, trie.InitNomtDB(t.TempDir(), 1, 16, 16))
	t.Cleanup(trie.CloseNomtDB)
	trie.SetStateBackend(trie.BackendNOMT) // restored by newCommitTestState cleanup

	accHandle, err := trie.GetOrInitNomtHandle("account_state")
	require.NoError(t, err)
	stakeHandle, err := trie.GetOrInitNomtHandle("stake_db")
	require.NoError(t, err)

	env := &nomtPipelineEnv{cs: cs}
	env.accTrie = trie.NewNomtStateTrie(accHandle, true, "account_state")
	env.stakeTrie = trie.NewNomtStateTrie(stakeHandle, true, "stake_db")
	asDB := account_state_db.NewAccountStateDB(env.accTrie, sm.GetStorageAccount())
	stakeDB := state_db.NewStakeStateDB(env.stakeTrie, sm.GetStorageStake())
	require.NotNil(t, asDB)
	require.NotNil(t, stakeDB)
	cs.SetAccountStateDB(asDB)
	cs.SetStakeStateDB(stakeDB)

	key := func(b byte) []byte { k := make([]byte, 32); k[31] = b; return k }
	// commit = execute one block on a trie: Commit(true) then flush the payload so the NOMT
	// session is closed before the next block opens one (mirrors commitWorker's CommitPayload).
	commit := func(tr *trie.NomtStateTrie, k, v []byte) common.Hash {
		require.NoError(t, tr.Update(k, v))
		root, _, _, cerr := tr.Commit(true)
		require.NoError(t, cerr)
		require.NoError(t, tr.CommitPayload())
		return root
	}

	// Block N: executed and committed in NOMT; header carries these roots.
	env.rootN_Acc = commit(env.accTrie, key(1), []byte("acc-block-N"))
	env.rootN_Stk = commit(env.stakeTrie, key(1), []byte("stake-block-N"))

	// Block N+1: execution advances the live roots before commitWorker persists block N.
	env.rootAhead_Acc = commit(env.accTrie, key(2), []byte("acc-block-N+1"))
	env.rootAhead_Stk = commit(env.stakeTrie, key(2), []byte("stake-block-N+1"))
	asDB.SetOriginRootHash(env.rootAhead_Acc)
	stakeDB.SetOriginRootHash(env.rootAhead_Stk)

	require.NotEqual(t, env.rootN_Acc, env.rootAhead_Acc, "precondition: live account root must be ahead of block N")
	require.NotEqual(t, env.rootN_Stk, env.rootAhead_Stk, "precondition: live stake root must be ahead of block N")
	return env
}

func (e *nomtPipelineEnv) blockN() *block.Block {
	hdr := block.NewBlockHeader(common.Hash{}, 1, e.rootN_Acc, e.rootN_Stk, common.Hash{}, common.Address{}, 100, common.Hash{}, 0)
	return block.NewBlock(hdr, nil, nil)
}

// Real NOMT: commitWorker persisting block N must leave every root source used to build block N+2's
// header (trie readView root, AccountStateDB/StakeStateDB origin root, IntermediateRoot with no dirty
// state, NOMT Commit with no dirty state) at the live N+1 root.
func TestCommitBlockState_NOMT_PipelinedCommitKeepsLiveRoots(t *testing.T) {
	env := setupNomtPipelineEnv(t)
	cs := env.cs

	_, err := cs.CommitBlockState(env.blockN(), WithPersistToDB(), WithSaveTxMapping(), WithCommitMappings())
	require.NoError(t, err)
	require.Equal(t, uint64(1), storage.GetLastBlockNumber(), "block N must be persisted")

	assert.Equal(t, env.rootAhead_Acc, env.accTrie.Hash(), "account NOMT readView root reverted to block N")
	assert.Equal(t, env.rootAhead_Stk, env.stakeTrie.Hash(), "stake NOMT readView root reverted to block N")
	assert.Equal(t, env.rootAhead_Acc, cs.GetAccountStateDB().GetOriginRootHash(), "account origin root reverted to block N")
	assert.Equal(t, env.rootAhead_Stk, cs.GetStakeStateDB().GetOriginRootHash(), "stake origin root reverted to block N")

	// Next block with no account/stake changes: the root that would go into its header.
	nextAcc, err := cs.GetAccountStateDB().IntermediateRoot()
	require.NoError(t, err)
	assert.Equal(t, env.rootAhead_Acc, nextAcc, "next header would carry a stale account root → fork")
	nextStk, err := cs.GetStakeStateDB().IntermediateRoot()
	require.NoError(t, err)
	assert.Equal(t, env.rootAhead_Stk, nextStk, "next header would carry a stale stake root → fork")

	accCommitRoot, _, _, err := env.accTrie.Commit(true)
	require.NoError(t, err)
	assert.Equal(t, env.rootAhead_Acc, accCommitRoot, "empty NOMT commit must return the live root")
}

// Every block in a pipelined window (commitWorker lagging several blocks behind) must keep the live roots.
func TestCommitBlockState_NOMT_PipelinedWindowKeepsLiveRoots(t *testing.T) {
	env := setupNomtPipelineEnv(t)
	cs := env.cs

	parent := common.Hash{}
	for n := uint64(1); n <= 4; n++ {
		// Lagging blocks all carry the old block-N roots; execution stays at N+1.
		hdr := block.NewBlockHeader(parent, n, env.rootN_Acc, env.rootN_Stk, common.Hash{}, common.Address{}, 100*n, common.Hash{}, 0)
		blk := block.NewBlock(hdr, nil, nil)
		_, err := cs.CommitBlockState(blk, WithPersistToDB(), WithSaveTxMapping(), WithCommitMappings())
		require.NoError(t, err, "block #%d", n)
		parent = hdr.Hash()

		require.Equal(t, n, storage.GetLastBlockNumber())
		require.Equal(t, env.rootAhead_Acc, env.accTrie.Hash(), "block #%d reverted live account root", n)
		require.Equal(t, env.rootAhead_Stk, env.stakeTrie.Hash(), "block #%d reverted live stake root", n)
		require.Equal(t, env.rootAhead_Acc, cs.GetAccountStateDB().GetOriginRootHash(), "block #%d reverted account origin", n)
		require.Equal(t, env.rootAhead_Stk, cs.GetStakeStateDB().GetOriginRootHash(), "block #%d reverted stake origin", n)
	}
}

// Recovery path (WithRebuildTries) must still realign to the header roots; NOMT C++ already holds
// the header state, so AlignWithExpectedRoot is a pure pointer realignment.
func TestCommitBlockState_NOMT_RebuildTriesRealignsToHeader(t *testing.T) {
	env := setupNomtPipelineEnv(t)
	cs := env.cs

	// Simulate a stale Go view after P2P replication: C++ is at rootAhead, Go pointers are behind.
	env.accTrie.RealignRoot(env.rootN_Acc)
	env.stakeTrie.RealignRoot(env.rootN_Stk)
	cs.GetAccountStateDB().SetOriginRootHash(env.rootN_Acc)
	cs.GetStakeStateDB().SetOriginRootHash(env.rootN_Stk)

	hdr := block.NewBlockHeader(common.Hash{}, 1, env.rootAhead_Acc, env.rootAhead_Stk, common.Hash{}, common.Address{}, 100, common.Hash{}, 0)
	_, err := cs.CommitBlockState(block.NewBlock(hdr, nil, nil), WithPersistToDB(), WithRebuildTries())
	require.NoError(t, err)

	assert.Equal(t, env.rootAhead_Acc, env.accTrie.Hash())
	assert.Equal(t, env.rootAhead_Stk, env.stakeTrie.Hash())
	assert.Equal(t, env.rootAhead_Acc, cs.GetAccountStateDB().GetOriginRootHash())
	assert.Equal(t, env.rootAhead_Stk, cs.GetStakeStateDB().GetOriginRootHash())
}

// A pipelined commit that fails after the trie step must leave the live roots untouched.
func TestCommitBlockState_NOMT_FailedPipelinedCommitKeepsLiveRoots(t *testing.T) {
	env := setupNomtPipelineEnv(t)
	cs := env.cs

	failingDB := &auditFailedDurableStorage{Storage: cs.storageManager.GetStorageBlock()}
	cs.blockDatabase = block.NewBlockDatabase(failingDB)
	blockChainInstance.blockDatabase = cs.blockDatabase

	_, err := cs.CommitBlockState(env.blockN(), WithPersistToDB(), WithSaveTxMapping())
	require.Error(t, err)
	assert.Equal(t, uint64(0), storage.GetLastBlockNumber())
	assert.Equal(t, env.rootAhead_Acc, env.accTrie.Hash())
	assert.Equal(t, env.rootAhead_Stk, env.stakeTrie.Hash())
	assert.Equal(t, env.rootAhead_Acc, cs.GetAccountStateDB().GetOriginRootHash())
	assert.Equal(t, env.rootAhead_Stk, cs.GetStakeStateDB().GetOriginRootHash())
}
