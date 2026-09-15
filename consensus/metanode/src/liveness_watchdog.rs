// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! LIVENESS WATCHDOG (2026-09-15, "Phuong an A" mục 21 UPDATE 4 -- see project memory).
//!
//! IMMEDIATE MITIGATION for the still-not-root-caused total-Rust-runtime freeze that has now
//! recurred 3x live on this cluster (node-1, node-2, node-0), each time confirmed via two
//! `sudo gdb -p <pid> -batch -ex "thread apply all bt"` snapshots seconds apart showing every
//! tokio worker thread byte-identical -- i.e. the ENTIRE tokio runtime stops scheduling anything,
//! including unrelated tasks' own timers, well past every bounded-wait timeout in the codebase
//! (parking_lot's own `try_read_for`/`try_write_for` included). Strong suspicion is an upstream
//! `parking_lot` limitation (github.com/Amanieu/parking_lot#212, fix PR #533 still unmerged as of
//! this writing), but this module does NOT depend on that diagnosis being correct: it detects
//! "the tokio scheduler has stopped making ANY progress" by the most direct means possible --
//! a tiny task that must be polled once a second, watched from a plain OS thread that is NOT
//! part of the tokio runtime and therefore keeps running even if every tokio worker is wedged.
//!
//! This is a halt-rather-than-guess safety net, not a fix: it bounds how long any single freeze
//! can last (a full process restart is already the operationally-verified recovery -- every one
//! of the 3 live incidents was cleared within seconds by `systemctl restart
//! metanode-execution-<N>.service`) while the real root cause is still being investigated. It
//! must keep working regardless of which theory about the root cause turns out to be right.
//!
//! On trip, this calls `libc::abort()` (SIGABRT) rather than `std::process::exit()`:
//!   - It cannot be swallowed by any `catch_unwind` in this codebase (there are several, all the
//!     way out to the outer resilience loop in `metanode_start_consensus` -- an abort bypasses
//!     unwinding entirely, so it always actually kills the process).
//!   - The systemd unit already sets `LimitCORE=infinity` and `GOTRACEBACK=crash`, so a SIGABRT
//!     here produces a full core dump (and Go's own goroutine dump) FOR FREE on every future
//!     occurrence -- meaning the next incident gets a real post-mortem artifact to `gdb` into at
//!     leisure, instead of needing to catch a live freeze with `sudo gdb` under time pressure like
//!     the first 3 incidents required.
//!   - `Restart=on-failure` (already configured, `RestartSec=15s`) then brings the node back
//!     automatically, matching the manual recovery already verified live 3 times.
//!
//! Both knobs are operator-overridable via env var so a freeze can still be caught live with
//! `sudo gdb` when that's what's actually wanted for a specific investigation:
//!   - `METANODE_DISABLE_LIVENESS_WATCHDOG=true` disables this entirely.
//!   - `METANODE_LIVENESS_WATCHDOG_SECS=<n>` overrides the default 30s trip threshold (chosen as
//!     a generous multiple of every bounded-wait timeout in this codebase -- 3s/15s for the
//!     GLOBAL_TX_CACHE locks -- while staying far below the multi-minute-to-hours freezes
//!     actually observed, so real incidents still get caught promptly).

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Once;
use std::time::Duration;

/// Wall-clock milliseconds (since UNIX_EPOCH) of the last time the tokio scheduler was
/// confirmed to still be making progress. Updated once a second by a task spawned via
/// `spawn_heartbeat_task` below; read once a second by the watchdog OS thread.
static LAST_HEARTBEAT_MS: AtomicU64 = AtomicU64::new(0);

static WATCHDOG_THREAD_STARTED: Once = Once::new();

fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn watchdog_timeout() -> Duration {
    let secs = std::env::var("METANODE_LIVENESS_WATCHDOG_SECS")
        .ok()
        .and_then(|v| v.parse::<u64>().ok())
        .filter(|&s| s > 0)
        .unwrap_or(30);
    Duration::from_secs(secs)
}

fn watchdog_disabled() -> bool {
    std::env::var("METANODE_DISABLE_LIVENESS_WATCHDOG")
        .map(|v| v.eq_ignore_ascii_case("true") || v == "1")
        .unwrap_or(false)
}

/// Starts the watchdog OS thread. Idempotent (safe to call on every FFI restart of the tokio
/// runtime) -- only the first call actually spawns anything, via `Once`.
pub fn ensure_started() {
    WATCHDOG_THREAD_STARTED.call_once(|| {
        if watchdog_disabled() {
            tracing::warn!(
                "🐕 [LIVENESS WATCHDOG] disabled via METANODE_DISABLE_LIVENESS_WATCHDOG -- a total \
                 tokio-runtime freeze will NOT auto-restart this process. Only set this while \
                 deliberately trying to catch a live freeze with `sudo gdb`."
            );
            return;
        }
        let timeout = watchdog_timeout();
        LAST_HEARTBEAT_MS.store(now_ms(), Ordering::Relaxed);

        let spawned = std::thread::Builder::new()
            .name("liveness-watchdog".to_string())
            .spawn(move || watchdog_loop(timeout));

        match spawned {
            Ok(_) => {
                tracing::info!(
                    "🐕 [LIVENESS WATCHDOG] started (timeout={}s) -- see liveness_watchdog.rs doc \
                     comment / project memory Phuong an A mục 21",
                    timeout.as_secs()
                );
            }
            Err(e) => {
                // This is a plain std::thread::spawn on a dedicated OS thread -- failure here
                // means the OS refused to create any new thread at all (e.g. resource
                // exhaustion). Nothing to fall back to; log loudly rather than fail silently.
                tracing::error!(
                    "🚨 [LIVENESS WATCHDOG] failed to spawn watchdog thread: {} -- liveness \
                     protection is NOT active for this process",
                    e
                );
            }
        }
    });
}

fn watchdog_loop(timeout: Duration) -> ! {
    loop {
        // Deliberately `std::thread::sleep`, not `tokio::time::sleep`: this thread must keep
        // ticking even when every tokio worker thread is wedged, which is exactly the condition
        // being watched for.
        std::thread::sleep(Duration::from_secs(1));

        let last = LAST_HEARTBEAT_MS.load(Ordering::Relaxed);
        if last == 0 {
            // Heartbeat task hasn't published its first tick yet (very early startup, or the
            // tokio runtime is still being built) -- nothing to judge yet.
            continue;
        }
        let stalled_ms = now_ms().saturating_sub(last);
        if stalled_ms >= timeout.as_millis() as u64 {
            // Use both eprintln! (guaranteed to land on stderr synchronously, independent of
            // whether the tracing subscriber's own writer -- which round-trips through the same
            // Go FFI callback the rest of this incident class is about -- can still be scheduled)
            // and tracing::error! (best effort, for the normal execution.log).
            eprintln!(
                "🚨🚨🚨 [LIVENESS WATCHDOG] tokio scheduler has not progressed in {}ms (>= {}ms \
                 threshold) -- aborting process so systemd (Restart=on-failure) restarts it. This \
                 is the halt-rather-than-guess safety net for the still-open Phuong an A mục 21 \
                 freeze, NOT a fix -- see liveness_watchdog.rs.",
                stalled_ms,
                timeout.as_millis()
            );
            tracing::error!(
                "🚨 [LIVENESS WATCHDOG] FATAL: no scheduler progress in {}ms (threshold {}ms) -- \
                 aborting for auto-restart. Set METANODE_DISABLE_LIVENESS_WATCHDOG=true to catch \
                 the next occurrence live with `sudo gdb` instead.",
                stalled_ms,
                timeout.as_millis()
            );
            // Best-effort: give the tracing writer (which itself round-trips through Go, and may
            // itself be stuck if this IS the freeze class being guarded against) a brief window
            // to flush before we abort -- but do not wait indefinitely for it either.
            std::thread::sleep(Duration::from_millis(200));

            // SIGABRT: cannot be caught by any catch_unwind in this codebase, triggers a core
            // dump (LimitCORE=infinity) + Go's own GOTRACEBACK=crash goroutine dump. systemd's
            // Restart=on-failure then restarts the whole node process automatically.
            unsafe {
                libc::abort();
            }
        }
    }
}

/// Spawns the tokio task that publishes the heartbeat. Must be called from within the tokio
/// runtime (i.e. inside `rt.block_on(...)`), once per (re)built runtime -- the outer resilience
/// loop in `metanode_start_consensus` rebuilds the runtime on a panic, and each fresh runtime
/// needs its own heartbeat task since the old one died with the old runtime.
pub fn spawn_heartbeat_task() {
    tokio::spawn(async {
        loop {
            LAST_HEARTBEAT_MS.store(now_ms(), Ordering::Relaxed);
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn timeout_defaults_to_30s_and_respects_env_override() {
        // SAFETY: test-only env mutation; consensus-core/metanode's test suites already do this
        // pattern elsewhere (e.g. TX_TRACE_ENABLED-style flags) and `cargo test` runs each test
        // binary's tests in threads within one process, so this is inherently racy against other
        // tests touching the same var -- there are none in this crate, so it's safe here.
        std::env::remove_var("METANODE_LIVENESS_WATCHDOG_SECS");
        assert_eq!(watchdog_timeout(), Duration::from_secs(30));

        std::env::set_var("METANODE_LIVENESS_WATCHDOG_SECS", "45");
        assert_eq!(watchdog_timeout(), Duration::from_secs(45));

        // Invalid/zero values fall back to the default rather than e.g. a 0s busy-loop.
        std::env::set_var("METANODE_LIVENESS_WATCHDOG_SECS", "0");
        assert_eq!(watchdog_timeout(), Duration::from_secs(30));
        std::env::set_var("METANODE_LIVENESS_WATCHDOG_SECS", "notanumber");
        assert_eq!(watchdog_timeout(), Duration::from_secs(30));

        std::env::remove_var("METANODE_LIVENESS_WATCHDOG_SECS");
    }

    #[test]
    fn disabled_flag_recognizes_true_and_1() {
        std::env::remove_var("METANODE_DISABLE_LIVENESS_WATCHDOG");
        assert!(!watchdog_disabled());

        std::env::set_var("METANODE_DISABLE_LIVENESS_WATCHDOG", "true");
        assert!(watchdog_disabled());

        std::env::set_var("METANODE_DISABLE_LIVENESS_WATCHDOG", "1");
        assert!(watchdog_disabled());

        std::env::set_var("METANODE_DISABLE_LIVENESS_WATCHDOG", "false");
        assert!(!watchdog_disabled());

        std::env::remove_var("METANODE_DISABLE_LIVENESS_WATCHDOG");
    }
}
