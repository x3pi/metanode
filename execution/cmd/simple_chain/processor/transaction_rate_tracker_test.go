package processor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TestRateTracker_AddAndCount
// ============================================================================
func TestRateTracker_AddAndCount(t *testing.T) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	assert.Equal(t, int64(0), trt.GetTotalTransactions())

	// Add transactions
	for i := 0; i < 100; i++ {
		trt.AddTransaction()
	}

	assert.Equal(t, int64(100), trt.GetTotalTransactions())
}

// ============================================================================
// TestRateTracker_CurrentTPS
// ============================================================================
func TestRateTracker_CurrentTPS(t *testing.T) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	// Add 50 transactions right now
	for i := 0; i < 50; i++ {
		trt.AddTransaction()
	}

	count, tps := trt.GetTransactionRate()
	assert.Equal(t, 50, count, "should see 50 recent transactions")
	assert.InDelta(t, 50.0, tps, 5.0, "TPS should be approximately 50")
}

// ============================================================================
// TestRateTracker_WindowExpiry
// ============================================================================
func TestRateTracker_WindowExpiry(t *testing.T) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	// Add some transactions
	for i := 0; i < 20; i++ {
		trt.AddTransaction()
	}

	// Total should always be 20
	assert.Equal(t, int64(20), trt.GetTotalTransactions())

	// Wait for window to expire (1s window + buffer)
	time.Sleep(1200 * time.Millisecond)

	// Recent count should be 0 after expiry
	count, _ := trt.GetTransactionRate()
	assert.Equal(t, 0, count, "recent count should be 0 after window expires")

	// But total should still be 20
	assert.Equal(t, int64(20), trt.GetTotalTransactions())
}

// ============================================================================
// TestRateTracker_Reset
// ============================================================================
func TestRateTracker_Reset(t *testing.T) {
	trt := NewTransactionRateTracker()

	for i := 0; i < 100; i++ {
		trt.AddTransaction()
	}
	require.Equal(t, int64(100), trt.GetTotalTransactions())

	trt.Reset()
	assert.Equal(t, int64(0), trt.GetTotalTransactions())

	count, tps := trt.GetTransactionRate()
	assert.Equal(t, 0, count)
	assert.Equal(t, 0.0, tps)
}

// ============================================================================
// TestRateTracker_DetailedStats
// ============================================================================
func TestRateTracker_DetailedStats(t *testing.T) {
	trt := NewTransactionRateTracker()
	defer trt.Reset()

	for i := 0; i < 30; i++ {
		trt.AddTransaction()
	}

	recentCount, recentTps, totalCount, _ := trt.GetDetailedStats()
	assert.Equal(t, 30, recentCount)
	assert.True(t, recentTps > 0, "recent TPS should be positive")
	assert.Equal(t, int64(30), totalCount)
}
