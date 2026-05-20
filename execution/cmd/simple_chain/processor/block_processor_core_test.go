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
