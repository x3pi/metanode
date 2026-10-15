package processor

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceiptTracker_InFlightGaugeAndSelfHealing(t *testing.T) {
	rt := NewReceiptTracker()
	require.Equal(t, 0, rt.InFlightTxCount())

	h1 := common.HexToHash("0x1111")
	h2 := common.HexToHash("0x2222")

	// First store increments gauge; duplicate store for same txHash does not double-increment.
	rt.StoreTxHashConnEntry(h1, TxHashConnEntry{MsgID: "m1"})
	assert.Equal(t, 1, rt.InFlightTxCount())
	rt.StoreTxHashConnEntry(h1, TxHashConnEntry{MsgID: "m1-updated"})
	assert.Equal(t, 1, rt.InFlightTxCount())

	rt.StoreTxHashConnEntry(h2, TxHashConnEntry{MsgID: "m2"})
	assert.Equal(t, 2, rt.InFlightTxCount())

	// TakeTxHashConnEntry decrements once; second take is a no-op and never goes negative.
	entry, ok := rt.TakeTxHashConnEntry(h1)
	require.True(t, ok)
	assert.Equal(t, "m1-updated", entry.MsgID)
	assert.Equal(t, 1, rt.InFlightTxCount())

	_, ok = rt.TakeTxHashConnEntry(h1)
	assert.False(t, ok)
	assert.Equal(t, 1, rt.InFlightTxCount())

	// Simulate counter drift and verify ReconcileInFlightTxCount self-heals to exact map size.
	rt.inFlightCount.Store(999999)
	reconciled := rt.ReconcileInFlightTxCount()
	assert.Equal(t, 1, reconciled)
	assert.Equal(t, 1, rt.InFlightTxCount())

	// Delete remaining entry.
	assert.True(t, rt.DeleteTxHashConnEntry(h2))
	assert.False(t, rt.DeleteTxHashConnEntry(h2))
	assert.Equal(t, 0, rt.InFlightTxCount())
}

func TestResolveIngressWatermarks_AndComputeSystemLoad(t *testing.T) {
	t.Setenv("META_INGRESS_HIGH_WATERMARK", "240000")
	t.Setenv("META_INGRESS_LOW_WATERMARK", "160000")
	high, low := resolveIngressWatermarks()
	assert.Equal(t, 240000, high)
	assert.Equal(t, 160000, low)

	// Invalid low >= high clamps low to 2/3 of high.
	t.Setenv("META_INGRESS_HIGH_WATERMARK", "150000")
	t.Setenv("META_INGRESS_LOW_WATERMARK", "200000")
	high, low = resolveIngressWatermarks()
	assert.Equal(t, 150000, high)
	assert.Equal(t, 100000, low)

	bp := &BlockProcessor{
		ReceiptTracker: NewReceiptTracker(),
	}
	bp.StoreTxHashConnEntry(common.HexToHash("0xaaaa"), TxHashConnEntry{MsgID: "a"})
	bp.StoreTxHashConnEntry(common.HexToHash("0xbbbb"), TxHashConnEntry{MsgID: "b"})

	cur, base, inFlight := computeSystemLoad(nil, bp)
	assert.Equal(t, 0, base)
	assert.Equal(t, 2, inFlight)
	assert.Equal(t, 2, cur)
}
