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

// TestShouldImportPeerCommitIndex locks in the exact gating decision from
// note/startup_sync_commit_index_import_fork_design_2026-09.md: a Validator
// (preserveOwnCommitIndex=true) must NEVER have a peer's CommitIndex imported,
// regardless of the header's value -- that field is the sending peer's own
// node-local DAG round counter, not safe to graft onto a different node's
// bookkeeping (this exact mistake caused a real, live-reproduced fork).
func TestShouldImportPeerCommitIndex(t *testing.T) {
	cases := []struct {
		name                   string
		preserveOwnCommitIndex bool
		commitIndexFromHeader  uint64
		want                   bool
	}{
		{"SyncOnly (preserve=false) with a real CommitIndex: import it", false, 42, true},
		{"SyncOnly (preserve=false) with CommitIndex=0: nothing to import", false, 0, false},
		{"Validator (preserve=true) with a real CommitIndex: MUST NOT import", true, 42, false},
		{"Validator (preserve=true) with CommitIndex=0: still must not import", true, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldImportPeerCommitIndex(c.preserveOwnCommitIndex, c.commitIndexFromHeader)
			assert.Equal(t, c.want, got)
		})
	}
}
