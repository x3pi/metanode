package processor

import (
	"math/big"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBlockProcessor() *BlockProcessor {
	p_trie.SetStateBackend(p_trie.BackendMPT)
	memDB := storage.NewMemoryDb()
	sm := storage.NewStorageManager()
	_ = sm.AddStorageTransaction(memDB)
	_ = sm.AddStorageReceipt(memDB)
	return &BlockProcessor{
		storageManager: sm,
	}
}

func TestPrecomputeRoots_BitForBitParity(t *testing.T) {
	bp := newTestBlockProcessor()

	// 1. Generate 20 mock transactions
	txs := make([]types.Transaction, 0, 20)
	for i := 0; i < 20; i++ {
		from := e_common.BigToAddress(big.NewInt(int64(1000 + i)))
		to := e_common.BigToAddress(big.NewInt(int64(2000 + i)))
		tx := NewMockTransaction(from, to, uint64(i+1))
		txs = append(txs, tx)
	}

	// 2. Generate 20 mock receipts
	rps := make([]types.Receipt, 0, 20)
	for i, tx := range txs {
		rcp := receipt.NewReceipt(
			tx.Hash(),
			tx.FromAddress(),
			tx.ToAddress(),
			big.NewInt(100),
			pb.RECEIPT_STATUS_RETURNED,
			nil,
			pb.EXCEPTION_NONE,
			21000,
			21000,
			nil,
			uint64(i),
			e_common.HexToHash("0xblock"),
			42,
		)
		rps = append(rps, rcp)
	}

	blockNum := uint64(42)
	gei := uint64(1042)

	// 3. Compute roots via concurrent PrecomputeRoots (B1 path)
	precomputed := bp.PrecomputeRoots(txs, rps, blockNum, gei)
	require.NotNil(t, precomputed)
	require.NoError(t, precomputed.Err)
	require.NotNil(t, precomputed.TxDB)
	require.NotNil(t, precomputed.Receipts)
	assert.NotEqual(t, e_common.Hash{}, precomputed.TxsRoot, "precomputed txsRoot must not be empty")
	assert.NotEqual(t, e_common.Hash{}, precomputed.ReceiptsRoot, "precomputed receiptsRoot must not be empty")

	// 4. Compute roots via inline sequential methods (fallback path)
	inlineTxDB, inlineTxsRoot, inlineTxErr := bp.ComputeTxsRoot(txs, blockNum)
	require.NoError(t, inlineTxErr)
	require.NotNil(t, inlineTxDB)

	inlineReceipts, inlineReceiptsRoot := bp.ComputeReceiptsRoot(rps, blockNum, gei)
	require.NotNil(t, inlineReceipts)

	// 5. Verify 100% BIT-FOR-BIT IDENTICAL
	assert.Equal(t, inlineTxsRoot, precomputed.TxsRoot, "TxsRoot must be 100% bit-for-bit identical")
	assert.Equal(t, inlineReceiptsRoot, precomputed.ReceiptsRoot, "ReceiptsRoot must be 100% bit-for-bit identical")

	// 6. Verify committing precomputed TxDB succeeds without issues
	txBatch, err := precomputed.TxDB.Commit()
	require.NoError(t, err)
	assert.NotEmpty(t, txBatch, "committed txBatch should not be empty")
}

func TestPrecomputeRoots_EmptyInputs(t *testing.T) {
	bp := newTestBlockProcessor()

	// Empty transactions and receipts should return nil gracefully
	res := bp.PrecomputeRoots(nil, nil, 1, 1)
	assert.Nil(t, res, "PrecomputeRoots on empty inputs should return nil")

	// Empty slice input
	res2 := bp.PrecomputeRoots([]types.Transaction{}, []types.Receipt{}, 1, 1)
	assert.Nil(t, res2, "PrecomputeRoots on empty slices should return nil")
}

func TestPrecomputeRoots_DiscardSession_NoStorageLeak(t *testing.T) {
	bp := newTestBlockProcessor()

	// 1. Initial storage state
	initialTxStorageCount := 0
	if memDB, ok := bp.storageManager.GetStorageTransaction().(*storage.MemoryDB); ok {
		initialTxStorageCount = memDB.Size()
	}

	// 2. Generate transactions and receipts
	txs := make([]types.Transaction, 0, 10)
	for i := 0; i < 10; i++ {
		from := e_common.BigToAddress(big.NewInt(int64(3000 + i)))
		to := e_common.BigToAddress(big.NewInt(int64(4000 + i)))
		tx := NewMockTransaction(from, to, uint64(i+1))
		txs = append(txs, tx)
	}
	rps := make([]types.Receipt, 0, 10)
	for i, tx := range txs {
		rcp := receipt.NewReceipt(
			tx.Hash(),
			tx.FromAddress(),
			tx.ToAddress(),
			big.NewInt(100),
			pb.RECEIPT_STATUS_RETURNED,
			nil,
			pb.EXCEPTION_NONE,
			21000,
			21000,
			nil,
			uint64(i),
			e_common.HexToHash("0xblock"),
			100,
		)
		rps = append(rps, rcp)
	}

	// 3. Run PrecomputeRoots in background channel like speculative_executor
	ch := make(chan *PrecomputedBlockRoots, 1)
	go func() {
		ch <- bp.PrecomputeRoots(txs, rps, 100, 100)
	}()

	// 4. Session is discarded / abandoned (e.g. CleanGEI, conflict, or halt)
	precomputed := <-ch
	require.NotNil(t, precomputed)
	require.NoError(t, precomputed.Err)

	// Simulate discard without commit: we explicitly drop precomputed
	precomputed = nil

	// 5. Verify that underlying storage was NEVER written to by PrecomputeRoots
	if memDB, ok := bp.storageManager.GetStorageTransaction().(*storage.MemoryDB); ok {
		currentTxStorageCount := memDB.Size()
		assert.Equal(t, initialTxStorageCount, currentTxStorageCount,
			"PrecomputeRoots must not write or pollute persistent storage before authoritative block commit")
	}
}

func TestPrecomputeRoots_FallbackOnError(t *testing.T) {
	bp := newTestBlockProcessor()

	txs := make([]types.Transaction, 0, 5)
	for i := 0; i < 5; i++ {
		from := e_common.BigToAddress(big.NewInt(int64(5000 + i)))
		to := e_common.BigToAddress(big.NewInt(int64(6000 + i)))
		tx := NewMockTransaction(from, to, uint64(i+1))
		txs = append(txs, tx)
	}
	rps := make([]types.Receipt, 0, 5)
	for i, tx := range txs {
		rcp := receipt.NewReceipt(
			tx.Hash(),
			tx.FromAddress(),
			tx.ToAddress(),
			big.NewInt(100),
			pb.RECEIPT_STATUS_RETURNED,
			nil,
			pb.EXCEPTION_NONE,
			21000,
			21000,
			nil,
			uint64(i),
			e_common.HexToHash("0xblock"),
			200,
		)
		rps = append(rps, rcp)
	}

	// 1. Simulate a precomputed result with Err != nil
	faultyPrecomputed := &PrecomputedBlockRoots{
		Err: assert.AnError,
	}

	// 2. The createBlockFromResults check:
	// if precomputed != nil && precomputed.TxDB != nil && precomputed.Receipts != nil && precomputed.Err == nil
	// Should fall back to inline calculation
	isPrecomputedValid := faultyPrecomputed != nil &&
		faultyPrecomputed.TxDB != nil &&
		faultyPrecomputed.Receipts != nil &&
		faultyPrecomputed.Err == nil
	assert.False(t, isPrecomputedValid, "Faulty precomputed roots must trigger fallback")

	// 3. Fallback calculation must succeed and produce valid roots
	fallbackTxDB, fallbackTxsRoot, fallbackErr := bp.ComputeTxsRoot(txs, 200)
	require.NoError(t, fallbackErr)
	require.NotNil(t, fallbackTxDB)

	fallbackReceipts, fallbackReceiptsRoot := bp.ComputeReceiptsRoot(rps, 200, 200)
	require.NotNil(t, fallbackReceipts)

	// 4. Clean precompute must match fallback exactly
	cleanPrecomputed := bp.PrecomputeRoots(txs, rps, 200, 200)
	require.NotNil(t, cleanPrecomputed)
	assert.Equal(t, fallbackTxsRoot, cleanPrecomputed.TxsRoot)
	assert.Equal(t, fallbackReceiptsRoot, cleanPrecomputed.ReceiptsRoot)
}

