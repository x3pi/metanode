package transaction_pool

import (
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func makeTestTx(fromByte byte, nonce uint64) types.Transaction {
	from := common.Address{}
	from[0] = fromByte
	to := common.Address{}
	to[0] = 0xFF

	return transaction.NewTransaction(
		from,
		to,
		big.NewInt(100),
		21000,         // maxGas
		1,             // maxGasPrice
		0,             // maxTimeUse
		nil,           // data
		nil,           // relatedAddresses
		common.Hash{}, // lastDeviceKey
		common.Hash{}, // newDeviceKey
		nonce,
		1, // chainId
	)
}

// ──────────────────────────────────────────────
// Constructor
// ──────────────────────────────────────────────

func TestNewTransactionPool(t *testing.T) {
	pool := NewTransactionPool()
	require.NotNil(t, pool)
	assert.Equal(t, 0, pool.CountTransactions())
}

// ──────────────────────────────────────────────
// AddTransaction
// ──────────────────────────────────────────────

func TestAddTransaction_Single(t *testing.T) {
	pool := NewTransactionPool()
	tx := makeTestTx(0x01, 0)

	err := pool.AddTransaction(tx)
	require.NoError(t, err)
	assert.Equal(t, 1, pool.CountTransactions())
}

func TestAddTransaction_Duplicate(t *testing.T) {
	pool := NewTransactionPool()
	tx := makeTestTx(0x01, 0)

	err := pool.AddTransaction(tx)
	require.NoError(t, err)

	err = pool.AddTransaction(tx)
	assert.Error(t, err, "duplicate transaction should be rejected")
	assert.Equal(t, 1, pool.CountTransactions(), "count should still be 1")
}

func TestAddTransaction_SameAddressDifferentNonce(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x01, 1)

	require.NoError(t, pool.AddTransaction(tx1))
	require.NoError(t, pool.AddTransaction(tx2))
	assert.Equal(t, 2, pool.CountTransactions())
}

func TestAddTransaction_DifferentAddressSameNonce(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x02, 0)

	require.NoError(t, pool.AddTransaction(tx1))
	require.NoError(t, pool.AddTransaction(tx2))
	assert.Equal(t, 2, pool.CountTransactions())
}

// ──────────────────────────────────────────────
// AddTransactions (batch)
// ──────────────────────────────────────────────

func TestAddTransactions_Batch(t *testing.T) {
	pool := NewTransactionPool()
	txs := []types.Transaction{
		makeTestTx(0x01, 0),
		makeTestTx(0x02, 0),
		makeTestTx(0x03, 0),
	}

	pool.AddTransactions(txs)
	assert.Equal(t, 3, pool.CountTransactions())
}

func TestAddTransactions_BatchWithDuplicates(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x02, 0)
	tx3 := makeTestTx(0x01, 0) // same from+nonce as tx1

	// Batch add with internal duplicates — AddTransactions deduplicates within the batch
	pool.AddTransactions([]types.Transaction{tx1, tx2, tx3})
	assert.Equal(t, 2, pool.CountTransactions(), "duplicate within batch should be skipped")
}

func TestAddTransactions_EmptyBatch(t *testing.T) {
	pool := NewTransactionPool()
	pool.AddTransactions([]types.Transaction{})
	assert.Equal(t, 0, pool.CountTransactions())
}

// ──────────────────────────────────────────────
// TransactionsWithAggSign
// ──────────────────────────────────────────────

func TestTransactionsWithAggSign_ReturnsAndClears(t *testing.T) {
	pool := NewTransactionPool()
	tx1 := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x02, 0)

	require.NoError(t, pool.AddTransaction(tx1))
	require.NoError(t, pool.AddTransaction(tx2))

	txs, aggSign := pool.TransactionsWithAggSign()
	assert.Len(t, txs, 2, "should return 2 transactions")
	assert.Nil(t, aggSign, "aggSign is currently nil per implementation")

	// Pool should be empty after drain
	assert.Equal(t, 0, pool.CountTransactions(), "pool should be empty after drain")
}

func TestTransactionsWithAggSign_EmptyPool(t *testing.T) {
	pool := NewTransactionPool()

	txs, aggSign := pool.TransactionsWithAggSign()
	assert.Empty(t, txs, "empty pool should return empty slice")
	assert.Nil(t, aggSign)
}

func TestTransactionsWithAggSign_CanReAddAfterDrain(t *testing.T) {
	pool := NewTransactionPool()

	require.NoError(t, pool.AddTransaction(makeTestTx(0x01, 0)))
	txs, _ := pool.TransactionsWithAggSign()
	assert.Len(t, txs, 1)

	// Should be able to add new transactions after drain
	require.NoError(t, pool.AddTransaction(makeTestTx(0x02, 0)))
	assert.Equal(t, 1, pool.CountTransactions())
}

// ──────────────────────────────────────────────
// GetTransactionByHash
// ──────────────────────────────────────────────

func TestGetTransactionByHash_ZeroHash(t *testing.T) {
	pool := NewTransactionPool()
	tx, found := pool.GetTransactionByHash(common.Hash{})
	assert.Nil(t, tx)
	assert.False(t, found, "zero hash should return not found")
}

func TestGetTransactionByHash_NotFound(t *testing.T) {
	pool := NewTransactionPool()
	require.NoError(t, pool.AddTransaction(makeTestTx(0x01, 0)))

	randomHash := common.HexToHash("0xdeadbeefdeadbeef")
	found, ok := pool.GetTransactionByHash(randomHash)
	assert.Nil(t, found)
	assert.False(t, ok)
}

// ──────────────────────────────────────────────
// Concurrent Tests
// ──────────────────────────────────────────────

func TestConcurrent_AddAndDrain(t *testing.T) {
	pool := NewTransactionPool()
	const numGoroutines = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			tx := makeTestTx(byte(idx), 0)
			_ = pool.AddTransaction(tx) // ignore duplicate errors
		}(i)
	}

	wg.Wait()

	// Drain and verify
	txs, _ := pool.TransactionsWithAggSign()
	assert.Equal(t, numGoroutines, len(txs),
		"all unique transactions should be in pool")
	assert.Equal(t, 0, pool.CountTransactions(), "pool should be empty after drain")
}

func TestConcurrent_AddAndCount(t *testing.T) {
	pool := NewTransactionPool()

	var wg sync.WaitGroup
	const writers = 20
	const readers = 10

	wg.Add(writers + readers)

	for i := 0; i < writers; i++ {
		go func(idx int) {
			defer wg.Done()
			tx := makeTestTx(byte(idx), 0)
			_ = pool.AddTransaction(tx)
		}(i)
	}

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			_ = pool.CountTransactions() // should not panic
		}()
	}

	wg.Wait()
	assert.Equal(t, writers, pool.CountTransactions())
}
