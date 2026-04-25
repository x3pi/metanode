package processor

import (
	"sync"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
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

// NOTE: TestCheckConnectionInitialized_* tests removed.
// The checkConnectionInitialized function was deleted — it had a 5-second
// spin-wait retry loop that blocked TX processing goroutines.
// TX validation is now handled by signature/nonce checks.
