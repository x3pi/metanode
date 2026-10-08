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
