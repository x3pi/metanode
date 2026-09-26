package account_state_db

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────
// CommitPipeline Tests
// ──────────────────────────────────────────────

func TestCommitPipeline_Basic(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xF0)

	// Modify some data
	err := adb.AddBalance(addr, big.NewInt(5000))
	require.NoError(t, err)

	// IntermediateRoot locks; CommitPipeline expects locked
	_, err = adb.IntermediateRoot(true)
	require.NoError(t, err)

	result, err := adb.CommitPipeline()
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotEqual(t, common.Hash{}, result.FinalHash, "final hash should not be empty")
}

func TestCommitPipeline_Then_PersistAsync(t *testing.T) {
	adb := newTestDB(t)
	addr1 := testAddr(0xF1)
	addr2 := testAddr(0xF2)

	err := adb.AddBalance(addr1, big.NewInt(1000))
	require.NoError(t, err)
	err = adb.AddBalance(addr2, big.NewInt(2000))
	require.NoError(t, err)

	_, err = adb.IntermediateRoot(true)
	require.NoError(t, err)

	result, err := adb.CommitPipeline()
	require.NoError(t, err)
	require.NotNil(t, result)

	// PersistAsync should persist the data
	err = adb.PersistAsync(result)
	require.NoError(t, err)

	// After persist, origin root hash should be updated
	assert.Equal(t, result.FinalHash, adb.GetOriginRootHash())

	// Data should still be readable
	as, err := adb.AccountState(addr1)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(1000).Cmp(as.TotalBalance()))
}

func TestCommitPipeline_EmptyDirty(t *testing.T) {
	adb := newTestDB(t)

	// Lock without any changes
	_, err := adb.IntermediateRoot(true)
	require.NoError(t, err)

	result, err := adb.CommitPipeline()
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestCommitPipeline_PreservesState(t *testing.T) {
	adb := newTestDB(t)
	addr := testAddr(0xF3)

	// Set multiple fields
	err := adb.AddBalance(addr, big.NewInt(9999))
	require.NoError(t, err)
	err = adb.SetNonce(addr, 42)
	require.NoError(t, err)

	_, err = adb.IntermediateRoot(true)
	require.NoError(t, err)

	result, err := adb.CommitPipeline()
	require.NoError(t, err)

	err = adb.PersistAsync(result)
	require.NoError(t, err)

	// Verify state is preserved after full commit+persist
	as, err := adb.AccountState(addr)
	require.NoError(t, err)
	assert.Equal(t, 0, big.NewInt(9999).Cmp(as.TotalBalance()))
	assert.Equal(t, uint64(42), as.Nonce())
}

type errorStorageForTest struct {
	*testMemoryDB
	failBatchPut bool
}

func (e *errorStorageForTest) BatchPut(b [][2][]byte) error {
	if e.failBatchPut {
		return fmt.Errorf("injected disk failure in BatchPut")
	}
	return e.testMemoryDB.BatchPut(b)
}

func TestPersistAsync_ErrorPropagationAndGateUnblock(t *testing.T) {
	memDB := newTestMemoryDB()
	errStorage := &errorStorageForTest{testMemoryDB: memDB}
	tr, err := p_trie.New(common.Hash{}, errStorage, true)
	require.NoError(t, err)
	adb := NewAccountStateDB(tr, errStorage)

	addr := testAddr(0xFE)
	err = adb.AddBalance(addr, big.NewInt(12345))
	require.NoError(t, err)

	_, err = adb.IntermediateRoot(true)
	require.NoError(t, err)

	result, err := adb.CommitPipeline()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.PersistChannel)

	// Inject error before PersistAsync
	errStorage.failBatchPut = true

	// PersistAsync should return the injected error
	err = adb.PersistAsync(result)
	assert.Error(t, err, "PersistAsync must return error when underlying BatchPut fails")
	assert.Contains(t, err.Error(), "injected disk failure")

	// Gate MUST be unblocked (closed) to prevent consensus deadlock
	select {
	case <-result.PersistChannel:
		// Passed: channel was closed in defer
	default:
		t.Fatal("PersistChannel must be closed even on failure to avoid consensus deadlock")
	}
}
