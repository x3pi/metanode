package tx_processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/require"
)

func newChainStateForBench(b *testing.B) *blockchain.ChainState {
	b.Helper()
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	b.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	accountStorage := storage.NewMemoryDb()
	codeStorage := storage.NewMemoryDb()
	scStorage := storage.NewMemoryDb()

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)

	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		b.Fatalf("failed to create bench chain state: %v", err)
	}
	return cs
}

func TestAccountTreeRootAtBlock_Correctness(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	worker := NewCommitteeAttestationWorker(cs, nil, 101, common.Address{}, "", "")

	// 1. Empty accounts -> zero hash
	root0, err := worker.accountTreeRootAtBlock(1)
	require.NoError(t, err)
	require.Equal(t, common.Hash{}, root0)

	// 2. Add accounts
	asDB := cs.GetAccountStateDB()
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	require.NoError(t, asDB.AddBalance(addr1, big.NewInt(500)))
	require.NoError(t, asDB.AddBalance(addr2, big.NewInt(1000)))
	if _, err := asDB.IntermediateRoot(true); err != nil {
		require.NoError(t, err)
	}
	_, err = asDB.Commit()
	require.NoError(t, err)

	root, err := worker.accountTreeRootAtBlock(1)
	require.NoError(t, err)
	require.NotEqual(t, common.Hash{}, root)

	// 3. Verify deterministic Merkle proof against this derived root
	leaves := []cross_chain.AccountLeaf{
		{Account: addr1, Balance: big.NewInt(500)},
		{Account: addr2, Balance: big.NewInt(1000)},
	}
	expectedRoot, proofMap, err := cross_chain.BuildAccountSnapshot(leaves)
	require.NoError(t, err)
	require.Equal(t, expectedRoot, root)

	proof1 := proofMap[addr1]
	require.True(t, cross_chain.VerifyAccountMerkleProof(leaves[0], proof1, root))
}

func BenchmarkAccountTreeRootAtBlock_1K(b *testing.B) {
	benchmarkAccountTreeRoot(b, 1000)
}

func BenchmarkAccountTreeRootAtBlock_10K(b *testing.B) {
	benchmarkAccountTreeRoot(b, 10000)
}

func BenchmarkAccountTreeRootAtBlock_50K(b *testing.B) {
	benchmarkAccountTreeRoot(b, 50000)
}

func benchmarkAccountTreeRoot(b *testing.B, accountCount int) {
	cs := newChainStateForBench(b)
	asDB := cs.GetAccountStateDB()
	oneWei := big.NewInt(1000000000)
	for i := 0; i < accountCount; i++ {
		addr := common.BigToAddress(big.NewInt(int64(i + 1)))
		if err := asDB.AddBalance(addr, oneWei); err != nil {
			b.Fatalf("AddBalance: %v", err)
		}
	}
	if _, err := asDB.IntermediateRoot(true); err != nil {
		b.Fatalf("IntermediateRoot: %v", err)
	}
	if _, err := asDB.Commit(); err != nil {
		b.Fatalf("Commit: %v", err)
	}

	worker := NewCommitteeAttestationWorker(cs, nil, 101, common.Address{}, "", "")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		root, err := worker.accountTreeRootAtBlock(1)
		if err != nil {
			b.Fatalf("accountTreeRootAtBlock: %v", err)
		}
		if root == (common.Hash{}) {
			b.Fatalf("expected non-empty root for %d accounts", accountCount)
		}
	}
}
