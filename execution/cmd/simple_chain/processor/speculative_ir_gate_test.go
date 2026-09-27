package processor

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// GEIs are far above anything other tests put into the global storage counters.
const gateBase = uint64(1) << 40

func newGateExecutor() *SpeculativeExecutor {
	return &SpeculativeExecutor{commitWake: make(chan struct{})}
}

var (
	parentA = common.HexToHash("0xa")
	parentB = common.HexToHash("0xb")
)

func tipOf(h common.Hash) func() (common.Hash, bool) {
	return func() (common.Hash, bool) { return h, true }
}

// A speculative execution must not reach the state roots (the NOMT handle) before its predecessor is committed.
func TestIRGate_WaitsForPredecessorCommit(t *testing.T) {
	se := newGateExecutor()
	gate := se.newIRGate(gateBase+5, parentA, tipOf(parentA))
	done := make(chan error, 1)
	go func() { done <- gate(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("gate opened before GEI %d was committed (err=%v)", gateBase+4, err)
	case <-time.After(150 * time.Millisecond):
	}
	se.MarkCommitted(gateBase + 3) // not yet the predecessor
	select {
	case err := <-done:
		t.Fatalf("gate opened at %d, predecessor is %d (err=%v)", gateBase+3, gateBase+4, err)
	case <-time.After(100 * time.Millisecond):
	}
	se.MarkCommitted(gateBase + 4)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gate refused a valid execution: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not open after the predecessor was committed")
	}
}

// If the tip moved while the execution waited, it was built on a stale parent: refuse BEFORE any NOMT session.
func TestIRGate_RefusesStaleParent(t *testing.T) {
	se := newGateExecutor()
	se.MarkCommitted(gateBase + 9)
	gate := se.newIRGate(gateBase+10, parentA, tipOf(parentB))
	if err := gate(context.Background()); err != errSpeculativeParentStale {
		t.Fatalf("got %v, want errSpeculativeParentStale", err)
	}
}

func TestIRGate_ValidParentPasses(t *testing.T) {
	se := newGateExecutor()
	se.MarkCommitted(gateBase + 9)
	if err := se.newIRGate(gateBase+10, parentA, tipOf(parentA))(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Sync/consensus cancelling the execution must free a gate that is waiting.
func TestIRGate_CancelUnblocksWaiter(t *testing.T) {
	se := newGateExecutor()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- se.newIRGate(gateBase+50, parentA, tipOf(parentA))(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled execution is still stuck in the gate")
	}
}

// Later executions open in GEI order however they arrive: block n+1 cannot pass before block n is committed.
func TestIRGate_OpensInGEIOrder(t *testing.T) {
	se := newGateExecutor()
	order := make(chan uint64, 3)
	for _, g := range []uint64{gateBase + 3, gateBase + 2, gateBase + 4} { // arrive out of order
		g := g
		go func() {
			if err := se.newIRGate(g, parentA, tipOf(parentA))(context.Background()); err == nil {
				order <- g
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	for _, g := range []uint64{gateBase + 2, gateBase + 3, gateBase + 4} {
		se.MarkCommitted(g - 1)
		select {
		case got := <-order:
			if got != g {
				t.Fatalf("GEI %d passed the gate when only %d was unlocked", got, g)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("GEI %d did not pass after its predecessor committed", g)
		}
	}
}
