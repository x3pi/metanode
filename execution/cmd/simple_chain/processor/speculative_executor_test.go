package processor

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
)

// TestSpeculativeExecutor_CancelInFlight_SpecificGEI tests cancelling a specific in-flight session
func TestSpeculativeExecutor_CancelInFlight_SpecificGEI(t *testing.T) {
	se := NewSpeculativeExecutor(nil)

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())

	se.inFlight.Store(uint64(10), &inFlightSession{cancel: cancel1})
	se.inFlight.Store(uint64(11), &inFlightSession{cancel: cancel2})

	// Cancel only GEI=10
	se.CancelInFlight(10)

	assert.ErrorIs(t, ctx1.Err(), context.Canceled, "GEI=10 should be cancelled")
	assert.NoError(t, ctx2.Err(), "GEI=11 should NOT be cancelled")

	// Cancel GEI=11
	se.CancelInFlight(11)
	assert.ErrorIs(t, ctx2.Err(), context.Canceled, "GEI=11 should now be cancelled")
}

// TestSpeculativeExecutor_CancelInFlight_All tests cancelling all in-flight sessions when no GEIs are specified
func TestSpeculativeExecutor_CancelInFlight_All(t *testing.T) {
	se := NewSpeculativeExecutor(nil)

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	ctx3, cancel3 := context.WithCancel(context.Background())

	se.inFlight.Store(uint64(100), &inFlightSession{cancel: cancel1})
	se.inFlight.Store(uint64(101), &inFlightSession{cancel: cancel2})
	se.inFlight.Store(uint64(102), &inFlightSession{cancel: cancel3})

	// Cancel all
	se.CancelInFlight()

	assert.ErrorIs(t, ctx1.Err(), context.Canceled)
	assert.ErrorIs(t, ctx2.Err(), context.Canceled)
	assert.ErrorIs(t, ctx3.Err(), context.Canceled)
}

// TestSpeculativeExecutor_CleanGEI_CancelsInFlight tests that CleanGEI cancels in-flight contexts
func TestSpeculativeExecutor_CleanGEI_CancelsInFlight(t *testing.T) {
	se := NewSpeculativeExecutor(nil)

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())

	respCh1 := make(chan *pb.ExecuteBlockResponse, 1)

	se.inFlight.Store(uint64(5), &inFlightSession{respCh: respCh1, cancel: cancel1})
	se.inFlight.Store(uint64(10), &inFlightSession{cancel: cancel2})

	se.activeSessions.Store(uint64(5), &SpeculativeResult{
		GEI:        5,
		BlockNum:   5,
		AuthRespCh: respCh1,
	})

	// Clean up to GEI=5
	se.CleanGEI(5)

	assert.ErrorIs(t, ctx1.Err(), context.Canceled, "GEI=5 should have its context cancelled")
	assert.NoError(t, ctx2.Err(), "GEI=10 should NOT be cancelled")

	_, inFlight5 := se.inFlight.Load(uint64(5))
	assert.False(t, inFlight5, "GEI=5 should be removed from inFlight")

	_, inFlight10 := se.inFlight.Load(uint64(10))
	assert.True(t, inFlight10, "GEI=10 should remain in inFlight")
}

// TestSpeculativeExecutor_WaitForInFlight tests WaitForInFlight timing and draining
func TestSpeculativeExecutor_WaitForInFlight(t *testing.T) {
	se := NewSpeculativeExecutor(nil)

	var wg sync.WaitGroup
	wg.Add(1)

	_, cancel := context.WithCancel(context.Background())
	se.inFlight.Store(uint64(20), &inFlightSession{cancel: cancel})
	se.activeWorkers.Add(1)

	go func() {
		defer wg.Done()
		defer se.activeWorkers.Add(-1)
		time.Sleep(30 * time.Millisecond)
		se.inFlight.Delete(uint64(20))
	}()

	start := time.Now()
	se.WaitForInFlight(500 * time.Millisecond)
	elapsed := time.Since(start)

	assert.True(t, elapsed >= 25*time.Millisecond, "Should have waited for worker to finish")
	assert.True(t, elapsed < 200*time.Millisecond, "Should return quickly once drained")

	wg.Wait()
}

// TestBlockProcessor_CancelSpeculativeExecution tests BlockProcessor wrapper
func TestBlockProcessor_CancelSpeculativeExecution(t *testing.T) {
	bp := &BlockProcessor{}
	se := NewSpeculativeExecutor(bp)
	bp.speculativeExecutor = se

	ctx, cancel := context.WithCancel(context.Background())
	se.inFlight.Store(uint64(50), &inFlightSession{cancel: cancel})

	bp.CancelSpeculativeExecution(50)

	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

// TestSpeculativeExecutor_AbortAllSpeculative tests forcefully cancelling in-flight workers,
// clearing activeSessions, and sending rescue responses to waiting FFI channels.
func TestSpeculativeExecutor_AbortAllSpeculative(t *testing.T) {
	se := NewSpeculativeExecutor(nil)

	// 1. Setup in-flight sessions
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	se.inFlight.Store(uint64(100), &inFlightSession{cancel: cancel1})
	se.inFlight.Store(uint64(101), &inFlightSession{cancel: cancel2})
	se.activeWorkers.Store(2)

	// Simulate background workers exiting upon cancellation
	go func() {
		<-ctx1.Done()
		se.activeWorkers.Add(-1)
	}()
	go func() {
		<-ctx2.Done()
		se.activeWorkers.Add(-1)
	}()

	// 2. Setup activeSessions waiting for committer
	respCh1 := make(chan *pb.ExecuteBlockResponse, 1)
	respCh2 := make(chan *pb.ExecuteBlockResponse, 1)

	se.activeSessions.Store(uint64(100), &SpeculativeResult{
		GEI:         100,
		BlockNum:    5000,
		AuthRespCh:  respCh1,
		ClonedState: nil,
	})
	se.activeSessions.Store(uint64(101), &SpeculativeResult{
		GEI:         101,
		BlockNum:    5001,
		AuthRespCh:  respCh2,
		ClonedState: nil,
	})

	// 3. Perform AbortAllSpeculative
	se.AbortAllSpeculative()

	// 4. Assert all in-flight contexts are canceled
	assert.ErrorIs(t, ctx1.Err(), context.Canceled)
	assert.ErrorIs(t, ctx2.Err(), context.Canceled)

	// 5. Assert rescue responses were sent to unblock waiting Rust channels
	select {
	case resp := <-respCh1:
		assert.True(t, resp.Success)
		assert.Equal(t, uint64(100), resp.ActualGei)
		assert.Equal(t, uint64(0), resp.GeisConsumed)
	default:
		t.Fatal("Expected rescue response on respCh1")
	}

	select {
	case resp := <-respCh2:
		assert.True(t, resp.Success)
		assert.Equal(t, uint64(101), resp.ActualGei)
		assert.Equal(t, uint64(0), resp.GeisConsumed)
	default:
		t.Fatal("Expected rescue response on respCh2")
	}

	// 6. Assert maps are cleaned
	var activeCount int
	se.activeSessions.Range(func(_, _ interface{}) bool {
		activeCount++
		return true
	})
	assert.Equal(t, 0, activeCount, "activeSessions should be empty")

	var inFlightCount int
	se.inFlight.Range(func(_, _ interface{}) bool {
		inFlightCount++
		return true
	})
	assert.Equal(t, 0, inFlightCount, "inFlight should be empty")
}

// TestBlockProcessor_CancelSpeculativeExecution_EmptyArgs tests BlockProcessor wrapper with empty args
func TestBlockProcessor_CancelSpeculativeExecution_EmptyArgs(t *testing.T) {
	bp := &BlockProcessor{}
	se := NewSpeculativeExecutor(bp)
	bp.speculativeExecutor = se

	respCh := make(chan *pb.ExecuteBlockResponse, 1)
	se.activeSessions.Store(uint64(200), &SpeculativeResult{
		GEI:        200,
		BlockNum:   6000,
		AuthRespCh: respCh,
	})

	bp.CancelSpeculativeExecution() // empty args -> AbortAllSpeculative

	select {
	case resp := <-respCh:
		assert.True(t, resp.Success)
		assert.Equal(t, uint64(200), resp.ActualGei)
	default:
		t.Fatal("Expected rescue response on respCh")
	}

	var activeCount int
	se.activeSessions.Range(func(_, _ interface{}) bool {
		activeCount++
		return true
	})
	assert.Equal(t, 0, activeCount)
}
