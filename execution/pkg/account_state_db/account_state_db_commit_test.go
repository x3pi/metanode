package account_state_db

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
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

func TestCommitPipeline_NotLocked_Fails(t *testing.T) {
	adb := newTestDB(t)

	// CommitPipeline should fail if not locked
	_, err := adb.CommitPipeline()
	assert.Error(t, err, "CommitPipeline should fail when not locked")
	assert.Contains(t, err.Error(), "locked")
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
