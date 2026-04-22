package processor

import (
	"sync"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TestGetLeaderAddress_Direct20Byte
// ============================================================================
func TestGetLeaderAddress_Direct20Byte(t *testing.T) {
	bp := &BlockProcessor{}
	expected := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")

	// 20-byte address → use directly
	result := bp.GetLeaderAddress(expected.Bytes(), 0)
	assert.Equal(t, expected, result)
}

// ============================================================================
// TestGetLeaderAddress_EmptyFallback
// ============================================================================
func TestGetLeaderAddress_EmptyFallback(t *testing.T) {
	fallbackAddr := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	bp := &BlockProcessor{
		validatorAddress: fallbackAddr,
		// chainState is nil → GetLeaderAddressByIndex will return validatorAddress
	}

	// Empty address → fallback
	result := bp.GetLeaderAddress([]byte{}, 0)
	assert.Equal(t, fallbackAddr, result, "empty address should fallback to validatorAddress")
}

// ============================================================================
// TestGetLeaderAddress_InvalidLengthFallback
// ============================================================================
func TestGetLeaderAddress_InvalidLengthFallback(t *testing.T) {
	fallbackAddr := e_common.HexToAddress("0xcccc000000000000000000000000000000000003")
	bp := &BlockProcessor{
		validatorAddress: fallbackAddr,
	}

	// 10-byte address (invalid) → fallback
	result := bp.GetLeaderAddress(make([]byte, 10), 0)
	assert.Equal(t, fallbackAddr, result, "non-20-byte should fallback to validatorAddress")

	// 32-byte address (too long) → fallback
	result = bp.GetLeaderAddress(make([]byte, 32), 0)
	assert.Equal(t, fallbackAddr, result, "32-byte should also fallback")
}

// ============================================================================
// TestGetLeaderAddressByIndex_NilChainState
// ============================================================================
func TestGetLeaderAddressByIndex_NilChainState(t *testing.T) {
	fallbackAddr := e_common.HexToAddress("0xdddd000000000000000000000000000000000004")
	bp := &BlockProcessor{
		validatorAddress: fallbackAddr,
	}

	result := bp.GetLeaderAddressByIndex(0)
	assert.Equal(t, fallbackAddr, result, "nil chainState should fallback to validatorAddress")
}

// ============================================================================
// TestGetState_SetState
// ============================================================================
func TestGetState_SetState(t *testing.T) {
	bp := &BlockProcessor{}

	// Default should be zero value
	assert.Equal(t, StateNotLook, bp.GetState())

	bp.SetState(StatePendingLook)
	assert.Equal(t, StatePendingLook, bp.GetState())

	bp.SetState(StateLook)
	assert.Equal(t, StateLook, bp.GetState())

	bp.SetState(StateNotLook)
	assert.Equal(t, StateNotLook, bp.GetState())
}

// ============================================================================
// TestSetLastBlock_GetLastBlock
// ============================================================================
func TestSetLastBlock_GetLastBlock(t *testing.T) {
	bp := &BlockProcessor{}

	// Initially nil
	assert.Nil(t, bp.GetLastBlock())

	// Setting nil should not overwrite
	bp.SetLastBlock(nil)
	assert.Nil(t, bp.GetLastBlock())
}

// ============================================================================
// TestSetLastBlock_GetLastBlock_Concurrent
// ============================================================================
func TestSetLastBlock_GetLastBlock_Concurrent(t *testing.T) {
	bp := &BlockProcessor{}

	var wg sync.WaitGroup
	const goroutines = 20

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			// Concurrent reads should not panic
			bp.GetLastBlock()
			bp.GetState()
		}()
	}
	wg.Wait()
}

// ============================================================================
// TestCheckConnectionInitialized_NilConn
// ============================================================================
func TestCheckConnectionInitialized_NilConn(t *testing.T) {
	tp := &TransactionProcessor{}

	err := tp.checkConnectionInitialized(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// ============================================================================
// TestCheckConnectionInitialized_EmptyAddress
// ============================================================================
func TestCheckConnectionInitialized_EmptyAddress(t *testing.T) {
	tp := &TransactionProcessor{}

	// MockConnection with zero address
	conn := NewMockConnection(e_common.Address{})
	err := tp.checkConnectionInitialized(conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// ============================================================================
// TestCheckConnectionInitialized_NoConnectionsManager
// ============================================================================
func TestCheckConnectionInitialized_NoConnectionsManager(t *testing.T) {
	tp := &TransactionProcessor{
		env: &BlockProcessor{
			// connectionsManager is nil
		},
	}

	addr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	conn := NewMockConnection(addr)

	err := tp.checkConnectionInitialized(conn)
	require.Error(t, err)
	// When connectionsManager is nil, ConnectionByTypeAndAddress returns nil for all types,
	// so the retry loop exhausts and returns "connection not initialized after N retries..."
	assert.Contains(t, err.Error(), "not initialized")
}

// ============================================================================
// TestCheckConnectionInitialized_ConnectionFoundInManager
// ============================================================================
func TestCheckConnectionInitialized_ConnectionFoundInManager(t *testing.T) {
	addr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	conn := NewMockConnection(addr)

	mcm := NewMockConnectionsManager()
	mcm.AddConnectionForType(0, conn)

	tp := &TransactionProcessor{
		env: &BlockProcessor{
			connectionsManager: mcm,
		},
	}

	err := tp.checkConnectionInitialized(conn)
	assert.NoError(t, err, "connection in manager should pass initialization check")
}
