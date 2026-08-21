package mvm

// TA-hang auto-recovery watchdog (2026-08-22).
//
// WHY a full board reboot, not a live kill+relaunch of just /mvm_ta:
// investigated the alternative first (tz-llm-trustzone DEPLOYED_STATE.md's
// "watchdog/auto-recovery" entry has the full writeup) and it is NOT
// reliably achievable on this kernel:
//   - The only process-kill primitive, sys_kill_group (tee_os_kernel/
//     kernel/object/recycle.c), is restricted to procmgr by the kernel's
//     own doc comment ("Only procmgr could call this function") -- mvm_ta's
//     own launcher (mvm_launcher, a separate binary specifically so it does
//     NOT depend on chanmgr/procmgr internals, see that binary's own doc
//     comment) has no capability to invoke it on mvm_ta's cap group.
//   - Even if it could: sys_kill_group is a COOPERATIVE mechanism -- it
//     sets thread_exit_state to TE_EXITING and relies on the target
//     thread's own execution path checking that flag at some scheduling
//     point. A thread genuinely spinning in a tight loop after libstdc++'s
//     std::terminate()/abort() misbehaves in this environment (the exact
//     failure this watchdog exists to recover from, root-caused the same
//     session -- see DEPLOYED_STATE.md's setjmp/longjmp entries) may never
//     reach such a point at all.
// Given that, a full board reboot is the only recovery this project can
// actually stand behind today. Coarser than a live restart, but it turns
// "mvm_ta wedged, node silently dead until a human power-cycles it" into
// "board self-heals unattended within roughly a boot cycle" -- a real
// reliability improvement for an unattended deployment, not a cosmetic one.
//
// Trigger: tzHardwareRoundTrip's own round-trip timeout
// (tzHardwareRoundTripTimeout, tz_hardware_engine.go) -- the single choke
// point every one of the 6 forward commands (Call/Execute/Deploy/
// SendNative/ProcessNativeMintBurn/NoncePlusOne) funnels through. Firing
// once is sufficient evidence: per this project's entire hardware history
// (DEPLOYED_STATE.md), a genuine mvm_ta hang has never been observed to
// self-clear within a boot session -- retrying before rebooting would only
// spend another tzHardwareRoundTripTimeout hitting the same permanent
// condition.

import (
	"sync"
	"syscall"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// tzHardwareRebootFunc actually reboots the board. A variable, not a
// hardcoded call, so tz_hardware_watchdog_test.go can substitute a no-op
// spy, and so a deployment wanting a different mechanism (e.g. shelling
// out to a wrapper that flushes logs / notifies an operator first) can
// override it without editing this file. Defaults to the raw Linux
// reboot(2) syscall (LINUX_REBOOT_CMD_RESTART) -- no dependency on an
// external `reboot` binary being on $PATH, but does require this process
// to have CAP_SYS_BOOT (root; the production node process on this
// single-purpose board is expected to run as root -- same assumption
// tz-llm-trustzone's own flash/recovery tooling makes throughout).
var tzHardwareRebootFunc = func() error {
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
}

// tzHardwareWatchdogEnabled gates the whole mechanism. Defaults to true --
// this file is only ever compiled into the same binary as the rest of
// pkg/mvm regardless of execution mode, but tzHardwareOnRoundTripTimeout
// is only ever actually called from tzHardwareRoundTrip, which nothing
// reaches unless ModeTrustzoneHardware was explicitly selected via
// SetExecutionMode -- so gating on execution mode again here would be
// redundant. Exists as a separate var (rather than being implicit) so a
// deployment or a debugging session can force it off explicitly.
var tzHardwareWatchdogEnabled = true

// tzHardwareRebootOnce ensures at most one reboot attempt per process,
// even though in practice tzSessionMu already serializes every forward
// command through one round trip at a time (so at most one timeout can
// be in flight system-wide) -- cheap extra safety, not load-bearing. A
// *sync.Once (not a value) so tz_hardware_watchdog_test.go can swap in a
// fresh one per test without copying a sync.Once by value (go vet
// correctly flags that as a bug).
var tzHardwareRebootOnce = &sync.Once{}

// tzHardwareOnRoundTripTimeout is called by tzHardwareRoundTrip exactly
// where it already detects a timeout, before that timeout's error reaches
// its caller. It does not change what tzHardwareRoundTrip returns --
// either the reboot syscall succeeds (the kernel starts tearing this
// process down shortly after, making the return value moot) or it logs
// loudly and falls through, leaving the original timeout error/panic
// behavior exactly as it was before this watchdog existed.
func tzHardwareOnRoundTripTimeout(cmd int, timeout time.Duration) {
	logger.Error("🚨 [TZ_HW_WATCHDOG] mvm_ta round-trip TIMEOUT after %s (cmd=%d) -- "+
		"per this project's hardware history this means mvm_ta is genuinely, "+
		"permanently stuck for the rest of this boot (not slow-but-working). "+
		"No safe way to kill+relaunch just the TA process exists on this "+
		"kernel (see this file's own doc comment) -- rebooting the whole "+
		"board now.", timeout, cmd)

	if !tzHardwareWatchdogEnabled {
		logger.Error("🚨 [TZ_HW_WATCHDOG] disabled (tzHardwareWatchdogEnabled=false) -- NOT rebooting, letting the original timeout error propagate instead")
		return
	}

	tzHardwareRebootOnce.Do(func() {
		if err := tzHardwareRebootFunc(); err != nil {
			logger.Error("🚨 [TZ_HW_WATCHDOG] reboot attempt FAILED: %v -- board will stay wedged until a human intervenes (check CAP_SYS_BOOT/root)", err)
			return
		}
		logger.Error("🚨 [TZ_HW_WATCHDOG] reboot syscall accepted -- board should restart within seconds")
		// If Reboot(2) actually took effect the kernel is tearing everything
		// down right about now -- this just keeps the calling goroutine from
		// racing ahead and returning a (by then meaningless) timeout error
		// to its caller while that happens, rather than any real recovery
		// logic of its own.
		time.Sleep(30 * time.Second)
	})
}
