package processor

import (
	"sync"
	"testing"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TestPendingTxManager_AddAndGet
// ============================================================================
func TestPendingTxManager_AddAndGet(t *testing.T) {
	ptm := NewPendingTransactionManager()
	tx := NewMockTransaction(
		e_common.HexToAddress("0x1111111111111111111111111111111111111111"),
		e_common.HexToAddress("0x2222222222222222222222222222222222222222"),
		1,
	)

	err := ptm.Add(tx, StatusInPool)
	require.NoError(t, err)

	got, exists := ptm.Get(tx.Hash())
	require.True(t, exists)
	assert.Equal(t, StatusInPool, got.Status)
	assert.Equal(t, tx.Hash(), got.Tx.Hash())
}

// ============================================================================
// TestPendingTxManager_AddNil
// ============================================================================
func TestPendingTxManager_AddNil(t *testing.T) {
	ptm := NewPendingTransactionManager()
	err := ptm.Add(nil, StatusInPool)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// ============================================================================
// TestPendingTxManager_AddDuplicate
// ============================================================================
func TestPendingTxManager_AddDuplicate(t *testing.T) {
	ptm := NewPendingTransactionManager()
	tx := NewMockTransaction(
		e_common.HexToAddress("0x1111111111111111111111111111111111111111"),
		e_common.HexToAddress("0x2222222222222222222222222222222222222222"),
		1,
	)

	err := ptm.Add(tx, StatusInPool)
	require.NoError(t, err)

	// Add same tx again — should not increase count
	err = ptm.Add(tx, StatusProcessing)
	require.NoError(t, err)
	assert.Equal(t, 1, ptm.Count(), "count should still be 1 after duplicate add")

	// Status should stay at original (LoadOrStore keeps first)
	got, exists := ptm.Get(tx.Hash())
	require.True(t, exists)
	assert.Equal(t, StatusInPool, got.Status, "duplicate add should not overwrite status")
}

// ============================================================================
// TestPendingTxManager_UpdateStatus
// ============================================================================
func TestPendingTxManager_UpdateStatus(t *testing.T) {
	ptm := NewPendingTransactionManager()
	tx := NewMockTransaction(
		e_common.HexToAddress("0xaaaa000000000000000000000000000000000001"),
		e_common.HexToAddress("0xbbbb000000000000000000000000000000000002"),
		42,
	)
	ptm.Add(tx, StatusInPool)

	updated := ptm.UpdateStatus(tx.Hash(), StatusProcessing)
	require.True(t, updated)

	got, _ := ptm.Get(tx.Hash())
	assert.Equal(t, StatusProcessing, got.Status)

	// Update non-existent hash
	fakeHash := e_common.HexToHash("0xdead")
	updated = ptm.UpdateStatus(fakeHash, StatusConfirmed)
	assert.False(t, updated, "updating non-existent tx should return false")
}

// ============================================================================
// TestPendingTxManager_Remove
// ============================================================================
func TestPendingTxManager_Remove(t *testing.T) {
	ptm := NewPendingTransactionManager()
	tx := NewMockTransaction(
		e_common.HexToAddress("0x1111111111111111111111111111111111111111"),
		e_common.HexToAddress("0x2222222222222222222222222222222222222222"),
		1,
	)
	ptm.Add(tx, StatusInPool)
	assert.Equal(t, 1, ptm.Count())

	removed := ptm.Remove(tx.Hash())
	assert.True(t, removed)
	assert.Equal(t, 0, ptm.Count())

	_, exists := ptm.Get(tx.Hash())
	assert.False(t, exists)

	// Remove non-existent
	removed = ptm.Remove(tx.Hash())
	assert.False(t, removed, "removing already-removed tx should return false")
}

// ============================================================================
// TestPendingTxManager_RemoveAll
// ============================================================================
func TestPendingTxManager_RemoveAll(t *testing.T) {
	ptm := NewPendingTransactionManager()
	for i := uint64(0); i < 10; i++ {
		tx := NewMockTransaction(
			e_common.BigToAddress(e_common.Big1),
			e_common.BigToAddress(e_common.Big2),
			i,
		)
		ptm.Add(tx, StatusInPool)
	}
	assert.Equal(t, 10, ptm.Count())

	ptm.RemoveAll()
	assert.Equal(t, 0, ptm.Count())
	assert.Empty(t, ptm.GetAll())
}

// ============================================================================
// TestPendingTxManager_Count
// ============================================================================
func TestPendingTxManager_Count(t *testing.T) {
	ptm := NewPendingTransactionManager()
	assert.Equal(t, 0, ptm.Count())

	txs := make([]*MockTransaction, 5)
	for i := 0; i < 5; i++ {
		txs[i] = NewMockTransaction(
			e_common.BigToAddress(e_common.Big1),
			e_common.BigToAddress(e_common.Big2),
			uint64(i),
		)
		ptm.Add(txs[i], StatusInPool)
	}
	assert.Equal(t, 5, ptm.Count())

	ptm.Remove(txs[0].Hash())
	assert.Equal(t, 4, ptm.Count())

	ptm.Remove(txs[1].Hash())
	ptm.Remove(txs[2].Hash())
	assert.Equal(t, 2, ptm.Count())
}

// ============================================================================
// TestPendingTxManager_HasNonceConflict
// ============================================================================
func TestPendingTxManager_HasNonceConflict(t *testing.T) {
	ptm := NewPendingTransactionManager()
	fromAddr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	toAddr := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")

	tx1 := NewMockTransaction(fromAddr, toAddr, 10)
	ptm.Add(tx1, StatusInPool)

	// Same address, same nonce, different hash → nonce replacement (no conflict)
	tx2 := NewMockTransaction(fromAddr, toAddr, 10)
	tx2.hash = e_common.HexToHash("0x9999") // different hash
	assert.False(t, ptm.HasNonceConflict(tx2), "should allow replacement: same from+nonce, different hash")

	// Same tx (same hash) → no conflict
	assert.False(t, ptm.HasNonceConflict(tx1), "same tx should not conflict with itself")

	// Different nonce → no conflict
	tx3 := NewMockTransaction(fromAddr, toAddr, 11)
	assert.False(t, ptm.HasNonceConflict(tx3), "different nonce should not conflict")

	// Different address, same nonce → no conflict
	otherAddr := e_common.HexToAddress("0xcccc000000000000000000000000000000000003")
	tx4 := NewMockTransaction(otherAddr, toAddr, 10)
	assert.False(t, ptm.HasNonceConflict(tx4), "different from address should not conflict")
}

// ============================================================================
// TestPendingTxManager_HasNonceConflict_Timeout
// ============================================================================
func TestPendingTxManager_HasNonceConflict_Timeout(t *testing.T) {
	ptm := NewPendingTransactionManager()
	fromAddr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	toAddr := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")

	// Add tx with old timestamp (simulate timeout)
	tx1 := NewMockTransaction(fromAddr, toAddr, 10)
	ptm.Add(tx1, StatusInPool)

	// Manually overwrite the timestamp to be very old
	ptm.pendingTxs.Store(tx1.Hash(), TransactionPending{
		Tx:        tx1,
		Status:    StatusInPool,
		Timestamp: time.Now().Add(-2 * PendingTimeout), // expired
	})

	// New tx with same nonce should NOT conflict because old one is timed out
	tx2 := NewMockTransaction(fromAddr, toAddr, 10)
	tx2.hash = e_common.HexToHash("0xbeef")
	assert.False(t, ptm.HasNonceConflict(tx2), "expired tx should not cause conflict")
}

// ============================================================================
// TestPendingTxManager_GetOldTransactions
// ============================================================================
func TestPendingTxManager_GetOldTransactions(t *testing.T) {
	ptm := NewPendingTransactionManager()
	fromAddr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	toAddr := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")

	// Add a recent tx
	recentTx := NewMockTransaction(fromAddr, toAddr, 1)
	ptm.Add(recentTx, StatusInPool)

	// Add an old tx with manually backdated timestamp
	oldTx := NewMockTransaction(fromAddr, toAddr, 2)
	ptm.Add(oldTx, StatusInPool)
	ptm.pendingTxs.Store(oldTx.Hash(), TransactionPending{
		Tx:        oldTx,
		Status:    StatusInPool,
		Timestamp: time.Now().Add(-10 * time.Minute),
	})

	threshold := 5 * time.Minute
	oldTxs := ptm.GetOldTransactionsForRemoval(threshold)
	assert.Len(t, oldTxs, 1, "should find exactly 1 old transaction")
	assert.Equal(t, oldTx.Hash(), oldTxs[0].Tx.Hash())
}

// ============================================================================
// TestPendingTxManager_ConcurrentAccess
// ============================================================================
func TestPendingTxManager_ConcurrentAccess(t *testing.T) {
	ptm := NewPendingTransactionManager()
	fromAddr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	toAddr := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")

	const goroutines = 50
	const txsPerRoutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gID int) {
			defer wg.Done()
			for i := 0; i < txsPerRoutine; i++ {
				nonce := uint64(gID*txsPerRoutine + i)
				tx := NewMockTransaction(fromAddr, toAddr, nonce)
				ptm.Add(tx, StatusInPool)
				ptm.Get(tx.Hash())
				ptm.HasNonceConflict(tx)
				ptm.UpdateStatus(tx.Hash(), StatusProcessing)
			}
		}(g)
	}

	wg.Wait()

	// Verify no panic and count is reasonable
	count := ptm.Count()
	assert.True(t, count > 0, "count should be positive after concurrent operations")
	assert.True(t, count <= goroutines*txsPerRoutine, "count should not exceed total adds")
}
