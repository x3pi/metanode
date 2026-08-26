package cross_chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGatewayEngine_BatchOutboundCommit closes the real gap found while building P4 relayer
// automation: CommitAttestationWorker (Milestone F) has always existed to BLS-sign a commit
// root, but nothing ever decided "these pending outbound() messages now form a committed
// batch" -- BatchOutboundCommit is that missing link. Proves: pending messages for the target
// destination get swept into a deterministic commit root (matching a fresh BuildCommitTree call
// over the same messages), the pending queue is cleared, CommittedBatches records the exact
// message list + epoch for later retrieval (getCommitBatch), and messages queued for a
// DIFFERENT destination are left untouched (the per-destination scoping this exists for).
func TestGatewayEngine_BatchOutboundCommit(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")

	msgA, err := engine.Outbound(sender, OutboundParams{
		DestChainID: 102, Target: target, Payload: []byte{0x01},
		AssetID: big.NewInt(0), Value: big.NewInt(0), Tip: big.NewInt(0), GasFee: big.NewInt(0),
		HopCount: 1,
	}, common.HexToHash("0xA001"))
	require.NoError(t, err)

	msgB, err := engine.Outbound(sender, OutboundParams{
		DestChainID: 102, Target: target, Payload: []byte{0x02},
		AssetID: big.NewInt(0), Value: big.NewInt(0), Tip: big.NewInt(0), GasFee: big.NewInt(0),
		HopCount: 1,
	}, common.HexToHash("0xA002"))
	require.NoError(t, err)

	// A message queued for a DIFFERENT destination must be untouched by batching chain 102.
	_, err = engine.Outbound(sender, OutboundParams{
		DestChainID: 103, Target: target, Payload: []byte{0x03},
		AssetID: big.NewInt(0), Value: big.NewInt(0), Tip: big.NewInt(0), GasFee: big.NewInt(0),
		HopCount: 1,
	}, common.HexToHash("0xA003"))
	require.NoError(t, err)

	require.Len(t, engine.PendingOutboundMessages[102], 2)
	require.Len(t, engine.PendingOutboundMessages[103], 1)

	commitRoot, messages, err := engine.BatchOutboundCommit(102, 7)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, *msgA, messages[0])
	assert.Equal(t, *msgB, messages[1])

	// The real, defining assertion: commitRoot must be exactly what an independent
	// BuildCommitTree call over the same 2 messages produces -- proving it's a real, verifiable
	// Merkle commitment, not an arbitrary/opaque value.
	wantRoot, _, _, _, err := BuildCommitTree(messages)
	require.NoError(t, err)
	assert.Equal(t, wantRoot, commitRoot)

	// Pending queue for 102 cleared; 103's own pending message untouched.
	assert.Empty(t, engine.PendingOutboundMessages[102])
	assert.Len(t, engine.PendingOutboundMessages[103], 1)

	// Durably recorded for later retrieval (a relayer, or this chain's own restart).
	batch, exists := engine.CommittedBatches[commitRoot]
	require.True(t, exists)
	assert.Equal(t, uint64(7), batch.Epoch)
	assert.Equal(t, messages, batch.Messages)
}

func TestGatewayEngine_BatchOutboundCommit_EmptyQueueFails(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	_, _, err := engine.BatchOutboundCommit(999, 1)
	require.Error(t, err)
}
