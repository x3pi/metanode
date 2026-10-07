package main

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

func newTestMetaAPI(memDb storage.Storage) *MetaAPI {
	sm := storage.NewStorageManager()
	_ = sm.AddStorageReceipt(memDb)

	app := &App{
		config: &config.SimpleChainConfig{
			ChainId: big.NewInt(991),
		},
		storageManager: sm,
	}

	api := &MetaAPI{
		App: app,
	}
	api.initCaches()
	return api
}

func makeTestBlock(header mt_types.BlockHeader, txHashes []common.Hash) mt_types.Block {
	return block.NewBlock(header, txHashes, nil)
}

func TestBlockGasInfo_EmptyBlock(t *testing.T) {
	memDb := storage.NewMemoryDb()
	api := newTestMetaAPI(memDb)

	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		100,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		common.HexToHash("0xrcproot"),
		common.HexToAddress("0xleader"),
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, nil)

	info1 := api.getBlockGasInfo(blk)
	require.NotNil(t, info1)
	assert.Equal(t, uint64(0), info1.TotalGasUsed)
	assert.Empty(t, info1.CumulativeGas)

	// Second call should return identical cached pointer
	info2 := api.getBlockGasInfo(blk)
	assert.Same(t, info1, info2, "second call must return cached pointer")
}

func TestBlockGasInfo_MultiTxBlock(t *testing.T) {
	require.NoError(t, trie.InitNomtDB(t.TempDir(), 1, 16, 16))
	defer trie.CloseNomtDB()

	memDb := storage.NewMemoryDb()
	api := newTestMetaAPI(memDb)

	rcpDb, err := receipt.NewReceipts(memDb)
	require.NoError(t, err)

	gasAmounts := []uint64{21000, 30000, 45000, 21000, 50000}
	txHashes := make([]common.Hash, len(gasAmounts))
	expectedCumulative := make([]uint64, len(gasAmounts))
	var runningGas uint64

	for i, gas := range gasAmounts {
		txH := common.HexToHash(fmt.Sprintf("0x%02x", i+1))
		txHashes[i] = txH
		runningGas += gas
		expectedCumulative[i] = runningGas

		r := receipt.NewReceipt(
			txH,
			common.HexToAddress("0xfrom"),
			common.HexToAddress("0xto"),
			big.NewInt(100),
			pb.RECEIPT_STATUS_RETURNED,
			nil,
			pb.EXCEPTION_NONE,
			gas*100000,
			gas,
			nil,
			uint64(i),
			common.HexToHash("0xblock"),
			100,
		)
		err := rcpDb.AddReceipt(r)
		require.NoError(t, err)
	}

	rootHash, err := rcpDb.Commit()
	require.NoError(t, err)

	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		100,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		rootHash,
		common.HexToAddress("0xleader"),
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, txHashes)

	info := api.getBlockGasInfo(blk)
	require.NotNil(t, info)
	assert.Equal(t, runningGas, info.TotalGasUsed)
	assert.Equal(t, expectedCumulative, info.CumulativeGas)

	// Last tx cumulativeGasUsed MUST equal block totalGasUsed
	assert.Equal(t, info.TotalGasUsed, info.CumulativeGas[len(info.CumulativeGas)-1],
		"cumulativeGasUsed of last tx must equal totalGasUsed of block")

	// Cache test
	infoCached := api.getBlockGasInfo(blk)
	assert.Same(t, info, infoCached, "must hit LRU cache on subsequent call")
}

func TestMarshalBlockToMapWithGas_UsesPrecomputed(t *testing.T) {
	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		100,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		common.HexToHash("0xrcproot"),
		common.HexToAddress("0xleader"),
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, []common.Hash{common.HexToHash("0x1")})

	precomputedGas := uint64(123456)
	gasInfo := &BlockGasInfo{
		TotalGasUsed:  precomputedGas,
		CumulativeGas: []uint64{precomputedGas},
	}

	// Passing nil storageReceipt ensures that if it tries to read disk, it will not find it
	blockMap, err := MarshalBlockToMapWithGas(blk, false, nil, nil, gasInfo)
	require.NoError(t, err)
	require.NotNil(t, blockMap)

	assert.Equal(t, hexutil.EncodeUint64(precomputedGas), blockMap["gasUsed"])
}

func TestBlockGasInfo_5000TxsLatency(t *testing.T) {
	require.NoError(t, trie.InitNomtDB(t.TempDir(), 1, 16, 16))
	defer trie.CloseNomtDB()

	memDb := storage.NewMemoryDb()
	api := newTestMetaAPI(memDb)

	rcpDb, err := receipt.NewReceipts(memDb)
	require.NoError(t, err)

	const txCount = 5000
	txHashes := make([]common.Hash, txCount)
	var expectedTotal uint64

	for i := 0; i < txCount; i++ {
		txH := common.HexToHash(fmt.Sprintf("0x%06x", i+1))
		txHashes[i] = txH
		gas := uint64(21000 + (i % 100))
		expectedTotal += gas

		r := receipt.NewReceipt(
			txH,
			common.HexToAddress("0xfrom"),
			common.HexToAddress("0xto"),
			big.NewInt(1),
			pb.RECEIPT_STATUS_RETURNED,
			nil,
			pb.EXCEPTION_NONE,
			gas*100000,
			gas,
			nil,
			uint64(i),
			common.HexToHash("0xblock"),
			200,
		)
		require.NoError(t, rcpDb.AddReceipt(r))
	}

	rootHash, err := rcpDb.Commit()
	require.NoError(t, err)

	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		200,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		rootHash,
		common.HexToAddress("0xleader"),
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, txHashes)

	// Step 1: First call populates cache
	t0 := time.Now()
	info := api.getBlockGasInfo(blk)
	buildDuration := time.Since(t0)
	t.Logf("BlockGasInfo build for 5,000 txs took: %v", buildDuration)

	require.NotNil(t, info)
	assert.Equal(t, expectedTotal, info.TotalGasUsed)
	assert.Len(t, info.CumulativeGas, txCount)
	assert.Equal(t, expectedTotal, info.CumulativeGas[txCount-1])

	// Verify correctness outside timing loop
	for i := 1; i < txCount; i++ {
		assert.GreaterOrEqual(t, info.CumulativeGas[i], info.CumulativeGas[i-1], "cumulative gas must be monotonic")
	}

	// Step 2: 5,000 queries for cumulative gas (simulating eth_getTransactionReceipt for all txs)
	tQueryStart := time.Now()
	for i := 0; i < txCount; i++ {
		cachedInfo := api.getBlockGasInfo(blk)
		_ = cachedInfo.CumulativeGas[i]
	}
	queryDuration := time.Since(tQueryStart)
	avgPerQuery := queryDuration / txCount
	t.Logf("5,000 queries for cumulative gas took total %v (avg %v/query)", queryDuration, avgPerQuery)

	if !raceEnabled {
		// Strict production performance gate under standard execution
		assert.Less(t, queryDuration, 50*time.Millisecond, "5,000 in-memory queries must be < 50ms total")
	} else {
		// Under -race, Go runtime shadow memory tracking adds overhead; verify relative speedup over building cache
		assert.Less(t, queryDuration, buildDuration, "cached queries must be faster than building block gas info")
	}
}

func TestBlockGasInfo_MissingReceipt(t *testing.T) {
	require.NoError(t, trie.InitNomtDB(t.TempDir(), 1, 16, 16))
	defer trie.CloseNomtDB()

	memDb := storage.NewMemoryDb()
	api := newTestMetaAPI(memDb)

	rcpDb, err := receipt.NewReceipts(memDb)
	require.NoError(t, err)

	txH0 := common.HexToHash("0x01")
	txH1 := common.HexToHash("0x02") // Missing receipt!
	txHashes := []common.Hash{txH0, txH1}

	r0 := receipt.NewReceipt(
		txH0,
		common.HexToAddress("0xfrom"),
		common.HexToAddress("0xto"),
		big.NewInt(100),
		pb.RECEIPT_STATUS_RETURNED,
		nil,
		pb.EXCEPTION_NONE,
		21000*100000,
		21000,
		nil,
		0,
		common.HexToHash("0xblock"),
		100,
	)
	require.NoError(t, rcpDb.AddReceipt(r0))
	// Notice: txH1 receipt is NOT added to rcpDb

	rootHash, err := rcpDb.Commit()
	require.NoError(t, err)

	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		100,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		rootHash,
		common.HexToAddress("0xleader"),
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, txHashes)

	// In the presence of a missing receipt, getBlockGasInfo MUST return nil and NOT cache!
	info := api.getBlockGasInfo(blk)
	assert.Nil(t, info, "getBlockGasInfo must return nil when receipt is missing")

	// Ensure NOT cached
	_, cached := api.blockGasCache.Get(blk.Header().Hash())
	assert.False(t, cached, "result must NOT be cached when receipt is missing")
}

func TestBlockGasInfo_Metrics(t *testing.T) {
	memDb := storage.NewMemoryDb()
	api := newTestMetaAPI(memDb)

	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		100,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		common.HexToHash("0xrcproot"),
		common.HexToAddress("0xleader"),
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, nil)

	missesBefore := testutil.ToFloat64(metrics.BlockGasCacheMissesTotal)
	hitsBefore := testutil.ToFloat64(metrics.BlockGasCacheHitsTotal)

	// First query: cache miss
	info1 := api.getBlockGasInfo(blk)
	require.NotNil(t, info1)
	assert.Equal(t, missesBefore+1, testutil.ToFloat64(metrics.BlockGasCacheMissesTotal))
	assert.Equal(t, hitsBefore, testutil.ToFloat64(metrics.BlockGasCacheHitsTotal))

	// Second query: cache hit
	info2 := api.getBlockGasInfo(blk)
	require.NotNil(t, info2)
	assert.Equal(t, missesBefore+1, testutil.ToFloat64(metrics.BlockGasCacheMissesTotal))
	assert.Equal(t, hitsBefore+1, testutil.ToFloat64(metrics.BlockGasCacheHitsTotal))
}

func TestBlockGasInfo_MultiGroupBlockExecutionOrder(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")

	require.NoError(t, trie.InitNomtDB(t.TempDir(), 1, 16, 16))
	defer trie.CloseNomtDB()

	// Setup chainState
	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)
	cs, err := blockchain.NewChainStateRemote(
		header,
		storage.NewDummyStorage(""),
		storage.NewDummyStorage(""),
		storage.NewDummyStorage(""),
		map[common.Address]struct{}{},
	)
	require.NoError(t, err)

	// Create 3 senders and receivers
	s1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	r1 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	s2 := common.HexToAddress("0x3333333333333333333333333333333333333333")
	r2 := common.HexToAddress("0x4444444444444444444444444444444444444444")
	s3 := common.HexToAddress("0x5555555555555555555555555555555555555555")
	r3 := common.HexToAddress("0x6666666666666666666666666666666666666666")

	as1 := mt_state.NewAccountState(s1)
	as1.AddBalance(big.NewInt(10_000_000))
	cs.GetAccountStateDB().SetState(as1)

	as2 := mt_state.NewAccountState(s2)
	as2.AddBalance(big.NewInt(10_000_000))
	cs.GetAccountStateDB().SetState(as2)

	as3 := mt_state.NewAccountState(s3)
	as3.AddBalance(big.NewInt(10_000_000))
	cs.GetAccountStateDB().SetState(as3)

	newTx := func(from, to common.Address, nonce uint64, val *big.Int, data []byte) mt_types.Transaction {
		return transaction.NewTransaction(
			from, to, val,
			21000,
			1,
			1000,
			data,
			nil,
			common.Hash{},
			common.Hash{},
			nonce,
			991,
		)
	}

	tx1 := newTx(s1, r1, 0, big.NewInt(100), nil)
	tx2 := newTx(s2, r2, 0, big.NewInt(200), nil)
	tx3 := newTx(s3, r3, 0, big.NewInt(300), nil)

	items := []grouptxns.Item{
		{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx1), Tx: tx1},
		{ID: 1, Array: grouptxns.BuildDeterministicGroupAddrs(tx2), Tx: tx2},
		{ID: 2, Array: grouptxns.BuildDeterministicGroupAddrs(tx3), Tx: tx3},
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	require.GreaterOrEqual(t, len(groups), 2, "must have multiple groups")

	leaderAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")
	allTxs, allRcps, _, _ := tx_processor.ProcessTransactionsOptimistic(
		context.Background(), cs, groups, header, false, false, 1000, leaderAddr, true,
	)
	require.Equal(t, 3, len(allTxs))
	require.Equal(t, 3, len(allRcps))

	// 1. Check receipt execution order and transaction indices
	for i, rcp := range allRcps {
		require.Equal(t, uint64(i), rcp.TransactionIndex(), "receipt %d must have matching TransactionIndex", i)
		require.Equal(t, allTxs[i].Hash(), rcp.TransactionHash(), "receipt %d must have matching TransactionHash", i)
	}

	// 2. Put receipts into receipt storage DB
	memDb := storage.NewMemoryDb()
	rcpDb, err := receipt.NewReceipts(memDb)
	require.NoError(t, err)

	txHashes := make([]common.Hash, len(allTxs))
	for i, rcp := range allRcps {
		txHashes[i] = rcp.TransactionHash()
		require.NoError(t, rcpDb.AddReceipt(rcp))
	}
	rcpRoot, err := rcpDb.Commit()
	require.NoError(t, err)

	blkHeader := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		100,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		rcpRoot,
		leaderAddr,
		1000,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(blkHeader, txHashes)

	// Check block.Transactions() order matches execution order
	for i, txH := range blk.Transactions() {
		require.Equal(t, allTxs[i].Hash(), txH, "block.Transactions()[%d] must match allTxs[%d].Hash()", i, i)
		require.Equal(t, allRcps[i].TransactionHash(), txH, "block.Transactions()[%d] must match allRcps[%d].TransactionHash()", i, i)
	}

	api := newTestMetaAPI(memDb)
	gasInfo := api.getBlockGasInfo(blk)
	require.NotNil(t, gasInfo)
	require.Len(t, gasInfo.CumulativeGas, 3)

	var runningGas uint64
	for i, rcp := range allRcps {
		runningGas += rcp.GasUsed()
		assert.Equal(t, runningGas, gasInfo.CumulativeGas[i], "CumulativeGas[%d] must match running sum", i)
	}
	assert.Equal(t, runningGas, gasInfo.TotalGasUsed)
	assert.Equal(t, gasInfo.TotalGasUsed, gasInfo.CumulativeGas[len(gasInfo.CumulativeGas)-1],
		"cumulativeGasUsed of last tx must equal totalGasUsed of block")

	// Verify MarshalBlockToMapWithGas
	blockMap, err := MarshalBlockToMapWithGas(blk, false, nil, memDb, gasInfo)
	require.NoError(t, err)
	assert.Equal(t, hexutil.EncodeUint64(gasInfo.TotalGasUsed), blockMap["gasUsed"])
}
