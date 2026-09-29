package executor

import (
	"testing"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

func TestCgoUpdateTxTrace_NilPointers(t *testing.T) {
	var calledWithHash common.Hash
	var calledWithStep, calledWithDetails string
	var callCount int

	RegisterTraceCallback(func(hash common.Hash, step string, details string) {
		calledWithHash = hash
		calledWithStep = step
		calledWithDetails = details
		callCount++
	})

	testHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	hashPtr := unsafe.Pointer(&testHash[0])

	// Case 1: Both step and details are NULL pointers - must NOT crash!
	assert.NotPanics(t, func() {
		updateTxTraceFromPointers(hashPtr, nil, nil)
	})
	assert.Equal(t, 1, callCount)
	assert.Equal(t, testHash, calledWithHash)
	assert.Equal(t, "", calledWithStep)
	assert.Equal(t, "", calledWithDetails)

	// Case 2: Only details is NULL - must NOT crash!
	stepBytes := append([]byte("PROPOSED"), 0)
	stepPtr := unsafe.Pointer(&stepBytes[0])
	assert.NotPanics(t, func() {
		updateTxTraceFromPointers(hashPtr, stepPtr, nil)
	})
	assert.Equal(t, 2, callCount)
	assert.Equal(t, "PROPOSED", calledWithStep)
	assert.Equal(t, "", calledWithDetails)

	// Case 3: Both valid
	detailBytes := append([]byte("commit=5"), 0)
	detailPtr := unsafe.Pointer(&detailBytes[0])
	assert.NotPanics(t, func() {
		updateTxTraceFromPointers(hashPtr, stepPtr, detailPtr)
	})
	assert.Equal(t, 3, callCount)
	assert.Equal(t, "PROPOSED", calledWithStep)
	assert.Equal(t, "commit=5", calledWithDetails)

	// Case 4: hashPtr is NULL - should no-op
	updateTxTraceFromPointers(nil, stepPtr, detailPtr)
	assert.Equal(t, 3, callCount, "should not increment call count when hashPtr is nil")
}
