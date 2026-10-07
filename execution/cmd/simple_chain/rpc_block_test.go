package main

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
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
