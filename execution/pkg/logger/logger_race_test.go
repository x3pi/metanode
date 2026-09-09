package logger

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConcurrentOutputsMutationDoesNotRace is a regression test for the race fixed
// 2026-09-05 (PR #101): startLogWriter's async worker goroutine used to read
// l.Config.Outputs while SetConsoleOutputEnabled()/EnableFileLog()/CloseFileLog() mutated
// it via setOutputsUnsafe() WITHOUT holding writeMu -- a classic torn slice-header read
// that crashed a real validator node at startup with an unhandled SIGSEGV (addr=0x1) when
// main() called RedirectStderrToFile()/EnableFileLog() concurrently with the async worker
// draining logQueue.
//
// This drives both sides of that race concurrently under `go test -race`: N goroutines log
// through the async queue (Warn(), which enqueues rather than writing synchronously) while M
// goroutines concurrently flip SetConsoleOutputEnabled and Enable/CloseFileLog -- exactly the
// two code paths the original bug report named. `-race` failing this test would mean the fix
// regressed.
//
// That alone is necessary but not fully sufficient: a data race is only reliably reported
// when the detector actually observes it during this particular run. So this also asserts
// RecoveredPanicCount() stayed at 0 -- the panic-recovery guard added by the same PR is meant
// to be a last-resort safety net, not a substitute for actually staying race-free. A nonzero
// count here means the underlying corruption still happens, just with its visible symptom
// (a crash) now hidden behind a silently-caught recover() -- exactly the failure mode this
// test exists to catch even when the race detector itself stays quiet.
//
// NOTE: deliberately does NOT touch the global os.Stdout/os.Stderr *os.File variables (e.g.
// to capture their output) -- doing so from a test goroutine while the persistent async
// writer is still comparing `out == os.Stderr` in writeToOutputsSplit is itself a real data
// race, just one this package's product code was never meant to guard against (nothing in
// production ever reassigns those variables after process startup; RedirectStderrToFile()
// uses dup2/dup3 at the fd level instead, which is why it doesn't need to).
func TestConcurrentOutputsMutationDoesNotRace(t *testing.T) {
	origConsole := consoleOutputEnabled
	t.Cleanup(func() {
		SetConsoleOutputEnabled(origConsole)
		CloseFileLog()
	})

	logFile := filepath.Join(t.TempDir(), "concurrent_race_test.log")
	before := RecoveredPanicCount()

	const loggerGoroutines = 20
	const flipperGoroutines = 5
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(loggerGoroutines + flipperGoroutines)

	for i := 0; i < loggerGoroutines; i++ {
		go func(n int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				Warn("concurrent-race-test logger=%d iter=%d", n, j)
			}
		}(i)
	}

	for i := 0; i < flipperGoroutines; i++ {
		go func(n int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				SetConsoleOutputEnabled(j%2 == 0)
				if j%10 == 0 {
					if _, err := EnableFileLog(logFile); err != nil {
						t.Errorf("EnableFileLog: %v", err)
					}
				}
				if j%17 == 0 {
					CloseFileLog()
				}
			}
		}(i)
	}

	wg.Wait()

	// Give the async writer goroutine time to drain whatever is still in logQueue (and
	// therefore time for RecoveredPanicCount to reflect any panic from this run) before
	// checking it.
	time.Sleep(200 * time.Millisecond)

	if after := RecoveredPanicCount(); after != before {
		t.Fatalf("logger panicked %d time(s) during concurrent Outputs mutation (recovered, but should never happen)", after-before)
	}
}
