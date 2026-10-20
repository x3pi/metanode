package utils

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// defaultStuckAbortSecs is how long a block-execution phase may stay stuck before the process fail-stops.
// A healthy phase takes well under a second even at saturation, so 5 minutes only ever trips on a real hang
// (e.g. a NOMT commit that never returns and blocks every state read behind it).
const defaultStuckAbortSecs = 300

// abortProcess is replaced in tests. It dumps all goroutines (for the post-mortem) and then raises SIGABRT,
// which no recover() can swallow; systemd (Restart=on-failure, GOTRACEBACK=crash, LimitCORE=infinity) restarts
// the node and it re-syncs from its peers. This is a fail-stop, NOT a dispatch decision: nothing is committed,
// skipped or guessed, so it cannot cause a fork (halt rather than guess).
var abortProcess = func(name string, stuckFor time.Duration) {
	buf := make([]byte, 32<<20)
	n := runtime.Stack(buf, true)
	path := fmt.Sprintf("%s/metanode_stuck_%s_%d.txt", os.TempDir(), name, os.Getpid())
	if err := os.WriteFile(path, buf[:n], 0o644); err == nil {
		logger.Error("🛑 [WATCHDOG-%s] stuck for %v: goroutine dump written to %s; aborting process (fail-stop) so it restarts and re-syncs", name, stuckFor.Round(time.Second), path)
	} else {
		logger.Error("🛑 [WATCHDOG-%s] stuck for %v; aborting process (fail-stop) so it restarts and re-syncs", name, stuckFor.Round(time.Second))
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGABRT)
}

// stuckAbortAfter returns the fail-stop threshold. METANODE_EXEC_STUCK_ABORT_SECS=0 disables it.
func stuckAbortAfter() time.Duration {
	if v := os.Getenv("METANODE_EXEC_STUCK_ABORT_SECS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultStuckAbortSecs * time.Second
}

// StartWatchdog starts a background goroutine that prints a warning if it takes longer than the timeout.
// getContext is called every time the timer fires to get the latest status.
// It returns a Stop function that MUST be called when the tracked process finishes.
func StartWatchdog(name string, timeout time.Duration, getContext func() string) func() {
	return startWatchdog(name, timeout, 0, getContext)
}

// StartWatchdogFatal is StartWatchdog plus a fail-stop: if the tracked operation is still running after
// METANODE_EXEC_STUCK_ABORT_SECS (default 300 s, 0 = disabled) the process aborts instead of hanging forever.
func StartWatchdogFatal(name string, timeout time.Duration, getContext func() string) func() {
	return startWatchdog(name, timeout, stuckAbortAfter(), getContext)
}

func startWatchdog(name string, timeout, abortAfter time.Duration, getContext func() string) func() {
	doneChan := make(chan struct{})
	startTime := time.Now()
	// stopMu orders stop() against the abort: once stop() has returned, abortProcess can no longer start. Without it a
	// timer that expires at the same instant as stop() lets the select pick the timer case and abort a healthy process
	// (it also made the unit test flaky on a loaded CI runner).
	var stopMu sync.Mutex
	stopped := false
	var stopOnce sync.Once

	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				select {
				case <-doneChan: // stopped while the timer was firing
					return
				default:
				}
				logger.Warn("🆘 [WATCHDOG-%s] TIẾN TRÌNH KẸT QUÁ LÂU (%v)! Ngữ cảnh: %s", name, time.Since(startTime), getContext())
				if abortAfter > 0 && time.Since(startTime) >= abortAfter {
					stopMu.Lock()
					if !stopped {
						abortProcess(name, time.Since(startTime))
					}
					stopMu.Unlock()
				}
				timer.Reset(timeout)
			case <-doneChan:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			stopMu.Lock()
			stopped = true
			stopMu.Unlock()
			close(doneChan)
		})
	}
}
