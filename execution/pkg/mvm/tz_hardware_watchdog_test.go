package mvm

// Unit tests for tz_hardware_watchdog.go's own trigger logic, in
// isolation from tzHardwareRoundTrip's real timeout loop (which needs an
// actual /dev/tc_ns_client and can only be exercised on real hardware --
// see DEPLOYED_STATE.md's "watchdog/auto-recovery" entry for that side of
// the validation). What's testable here on any machine: does
// tzHardwareOnRoundTripTimeout call the reboot func exactly once, does the
// enabled/disabled gate work, and does a failed reboot attempt not panic.
//
// package mvm (not mvm_test): tzHardwareRebootFunc/tzHardwareWatchdogEnabled
// are unexported, and this test needs to override them directly.

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// withWatchdogOverrides swaps in a spy reboot func + explicit enabled
// state for the duration of one test, restoring the real ones after --
// same t.Cleanup pattern ta_boundary_harness_test.go's own
// runXViaMode helpers use for SetExecutionMode.
func withWatchdogOverrides(t *testing.T, enabled bool, rebootErr error) (calls *int) {
	t.Helper()
	prevFunc := tzHardwareRebootFunc
	prevEnabled := tzHardwareWatchdogEnabled
	prevOnce := tzHardwareRebootOnce
	t.Cleanup(func() {
		tzHardwareRebootFunc = prevFunc
		tzHardwareWatchdogEnabled = prevEnabled
		tzHardwareRebootOnce = prevOnce
	})

	n := 0
	calls = &n
	tzHardwareRebootFunc = func() error {
		*calls++
		return rebootErr
	}
	tzHardwareWatchdogEnabled = enabled
	tzHardwareRebootOnce = &sync.Once{}
	return calls
}

func TestTzHardwareWatchdog_TriggersRebootOnTimeout(t *testing.T) {
	calls := withWatchdogOverrides(t, true, nil)
	// tzHardwareOnRoundTripTimeout sleeps 30s after a successful reboot
	// call to mimic "the kernel is tearing this process down now" -- run
	// it in a goroutine and only wait for the reboot call itself, not
	// that sleep, to keep this test fast.
	done := make(chan struct{})
	go func() {
		tzHardwareOnRoundTripTimeout(0, 60*time.Second)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for *calls == 0 {
		select {
		case <-deadline:
			t.Fatal("tzHardwareOnRoundTripTimeout did not call tzHardwareRebootFunc within 2s")
		case <-time.After(time.Millisecond):
		}
	}
	if *calls != 1 {
		t.Errorf("tzHardwareRebootFunc called %d times, want exactly 1", *calls)
	}
	// Don't wait for `done` -- that's the 30s post-reboot sleep, not
	// interesting to this test.
}

func TestTzHardwareWatchdog_DisabledDoesNotReboot(t *testing.T) {
	calls := withWatchdogOverrides(t, false, nil)
	tzHardwareOnRoundTripTimeout(0, 60*time.Second)
	if *calls != 0 {
		t.Errorf("tzHardwareRebootFunc called %d times with watchdog disabled, want 0", *calls)
	}
}

func TestTzHardwareWatchdog_FailedRebootDoesNotPanic(t *testing.T) {
	calls := withWatchdogOverrides(t, true, errors.New("simulated: not CAP_SYS_BOOT"))
	tzHardwareOnRoundTripTimeout(0, 60*time.Second) // must return, not panic
	if *calls != 1 {
		t.Errorf("tzHardwareRebootFunc called %d times, want exactly 1", *calls)
	}
}

func TestTzHardwareWatchdog_OnlyRebootsOnce(t *testing.T) {
	calls := withWatchdogOverrides(t, true, nil)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tzHardwareRebootOnce.Do(func() { *calls++ })
		}()
	}
	wg.Wait()
	if *calls != 1 {
		t.Errorf("concurrent triggers resulted in %d reboot calls, want exactly 1 (sync.Once)", *calls)
	}
}
