package syncorder

import (
	"testing"
)

func TestEvaluateBlockOrder_Sequential(t *testing.T) {
	result := EvaluateBlockOrder(5, 5, 4, 0, true)
	if result.Decision != Process {
		t.Errorf("expected Process, got %s: %s", result.Decision, result.Reason)
	}
	if result.NewNextExpected != 6 {
		t.Errorf("expected NewNextExpected=6, got %d", result.NewNextExpected)
	}
}

func TestEvaluateBlockOrder_Duplicate(t *testing.T) {
	result := EvaluateBlockOrder(3, 5, 4, 0, false)
	if result.Decision != Skip {
		t.Errorf("expected Skip, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateBlockOrder_FutureBlock_Buffer(t *testing.T) {
	// Gap of 3 (gei=8, next=5), DB at 4, only 2 pending — should buffer
	result := EvaluateBlockOrder(8, 5, 4, 2, true)
	if result.Decision != Buffer {
		t.Errorf("expected Buffer, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateBlockOrder_FreshStartGapSkip(t *testing.T) {
	// DB is empty (0), gap ≤ 16 → fresh start gap skip
	result := EvaluateBlockOrder(9, 1, 0, 0, true)
	if result.Decision != GapSkipFreshStart {
		t.Errorf("expected GapSkipFreshStart, got %s: %s", result.Decision, result.Reason)
	}
	if result.NewNextExpected != 9 {
		t.Errorf("expected NewNextExpected=9, got %d", result.NewNextExpected)
	}
}

func TestEvaluateBlockOrder_FreshStartGapTooLarge(t *testing.T) {
	// DB is empty (0), gap > 16 → should buffer (not a fresh-start skip)
	result := EvaluateBlockOrder(20, 1, 0, 0, true)
	if result.Decision != Buffer {
		t.Errorf("expected Buffer, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateBlockOrder_RestoreGapSkip(t *testing.T) {
	// Large gap (>200), many pending (>50) → restore transition
	result := EvaluateBlockOrder(500, 100, 50, 60, true)
	if result.Decision != GapSkipRestore {
		t.Errorf("expected GapSkipRestore, got %s: %s", result.Decision, result.Reason)
	}
}

func TestEvaluateBlockOrder_DBSyncAhead_BlockBecamesSequential(t *testing.T) {
	// DB is at 99, nextExpected=50, incoming block is 100 → SyncFromDB
	result := EvaluateBlockOrder(100, 50, 99, 0, true)
	if result.Decision != SyncFromDB {
		t.Errorf("expected SyncFromDB, got %s: %s", result.Decision, result.Reason)
	}
	if result.NewNextExpected != 100 {
		t.Errorf("expected NewNextExpected=100, got %d", result.NewNextExpected)
	}
}

func TestEvaluateBlockOrder_DBSyncAhead_BlockBecomesOld(t *testing.T) {
	// DB is at 99, nextExpected=50, incoming block is 80 → Skip
	result := EvaluateBlockOrder(80, 50, 99, 0, true)
	if result.Decision != Skip {
		t.Errorf("expected Skip, got %s: %s", result.Decision, result.Reason)
	}
}

func TestSortedPendingGEIs(t *testing.T) {
	pending := map[uint64]string{
		15: "",
		10: "",
		20: "",
		12: "",
	}
	sorted := SortedPendingGEIs(pending)
	expected := []uint64{10, 12, 15, 20}
	if len(sorted) != len(expected) {
		t.Fatalf("expected %d elements, got %d", len(expected), len(sorted))
	}
	for i, v := range sorted {
		if v != expected[i] {
			t.Errorf("index %d: expected %d, got %d", i, expected[i], v)
		}
	}
}

func TestSortedPendingGEIs_Empty(t *testing.T) {
	sorted := SortedPendingGEIs(map[uint64]string{})
	if len(sorted) != 0 {
		t.Errorf("expected empty slice, got %d elements", len(sorted))
	}
}

func TestFindMinBufferedGEI(t *testing.T) {
	pending := map[uint64]string{
		15: "",
		10: "",
		20: "",
	}
	min := FindMinBufferedGEI(pending)
	if min != 10 {
		t.Errorf("expected 10, got %d", min)
	}
}

func TestFindMinBufferedGEI_Empty(t *testing.T) {
	min := FindMinBufferedGEI(map[uint64]string{})
	if min != 0 {
		t.Errorf("expected 0, got %d", min)
	}
}

func TestDecisionString(t *testing.T) {
	tests := []struct {
		d    Decision
		want string
	}{
		{Process, "Process"},
		{Skip, "Skip"},
		{Buffer, "Buffer"},
		{GapSkipFreshStart, "GapSkipFreshStart"},
		{GapSkipRestore, "GapSkipRestore"},
		{SyncFromDB, "SyncFromDB"},
		{Decision(99), "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.d.String(); got != tt.want {
			t.Errorf("Decision(%d).String() = %q, want %q", tt.d, got, tt.want)
		}
	}
}
