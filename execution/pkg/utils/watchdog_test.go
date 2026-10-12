package utils

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchdogFatalAbortsOnlyWhenStuck(t *testing.T) {
	var aborted atomic.Int32
	orig := abortProcess
	abortProcess = func(string, time.Duration) { aborted.Add(1) }
	defer func() { abortProcess = orig }()

	// Stuck: never stopped, so after abortAfter it must abort.
	stop := startWatchdog("T", 10*time.Millisecond, 60*time.Millisecond, func() string { return "ctx" })
	time.Sleep(250 * time.Millisecond)
	stop()
	if aborted.Load() == 0 {
		t.Fatal("a watchdog that outlives abortAfter must fail-stop the process")
	}

	// Healthy: stopped well before abortAfter, must never abort.
	aborted.Store(0)
	stop = startWatchdog("T", 10*time.Millisecond, 400*time.Millisecond, func() string { return "ctx" })
	time.Sleep(50 * time.Millisecond)
	stop()
	time.Sleep(450 * time.Millisecond)
	if aborted.Load() != 0 {
		t.Fatal("a watchdog stopped before abortAfter must not abort")
	}

	// Disabled (abortAfter 0): must never abort however long it runs.
	stop = startWatchdog("T", 10*time.Millisecond, 0, func() string { return "ctx" })
	time.Sleep(100 * time.Millisecond)
	stop()
	if aborted.Load() != 0 {
		t.Fatal("abortAfter=0 must disable the fail-stop")
	}
}

func TestStuckAbortAfterEnv(t *testing.T) {
	t.Setenv("METANODE_EXEC_STUCK_ABORT_SECS", "")
	if got := stuckAbortAfter(); got != defaultStuckAbortSecs*time.Second {
		t.Fatalf("default = %v", got)
	}
	t.Setenv("METANODE_EXEC_STUCK_ABORT_SECS", "0")
	if got := stuckAbortAfter(); got != 0 {
		t.Fatalf("0 must disable, got %v", got)
	}
	t.Setenv("METANODE_EXEC_STUCK_ABORT_SECS", "42")
	if got := stuckAbortAfter(); got != 42*time.Second {
		t.Fatalf("override = %v", got)
	}
	t.Setenv("METANODE_EXEC_STUCK_ABORT_SECS", "garbage")
	if got := stuckAbortAfter(); got != defaultStuckAbortSecs*time.Second {
		t.Fatalf("garbage must fall back to default, got %v", got)
	}
}
