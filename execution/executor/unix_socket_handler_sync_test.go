package executor

import (
	"testing"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
)

// TestRequestHandler_SetCancelSpeculativeCallback verifies setting and calling the cancel callback
func TestRequestHandler_SetCancelSpeculativeCallback(t *testing.T) {
	rh := &RequestHandler{}

	called := false
	var receivedGEIs []uint64

	rh.SetCancelSpeculativeCallback(func(geis ...uint64) {
		called = true
		receivedGEIs = append(receivedGEIs, geis...)
	})

	assert.NotNil(t, rh.cancelSpeculativeCallback)

	rh.cancelSpeculativeCallback(10, 20)
	assert.True(t, called)
	assert.Equal(t, []uint64{10, 20}, receivedGEIs)
}

// TestRequestHandler_CancelSpeculativeOnSync verifies HandleSyncBlocksRequest invokes cancelSpeculativeCallback
func TestRequestHandler_CancelSpeculativeOnSync(t *testing.T) {
	rh := &RequestHandler{}

	called := false
	rh.SetCancelSpeculativeCallback(func(geis ...uint64) {
		called = true
	})

	// Call HandleSyncBlocksRequest with empty blocks (early return after cancel)
	resp, err := rh.HandleSyncBlocksRequest(&pb.SyncBlocksRequest{
		Blocks: nil,
	})

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, called, "HandleSyncBlocksRequest should invoke cancelSpeculativeCallback")
}
