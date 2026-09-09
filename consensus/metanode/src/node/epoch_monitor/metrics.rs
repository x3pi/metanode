// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Metrics for the unified epoch monitor's validator stall-recovery loop
//! (see stall_recovery.rs).
//!
//! 2026-09-08: `recover_from_block_stall` already computes `peer_best_block - go_block`
//! internally every poll cycle to decide whether to trigger a fetch, but never exposed it —
//! during a real incident an operator had no way to tell a validator that's a few blocks behind
//! and self-healing (Tier 2 in note/deploy_incident_readiness_plan.md) apart from one that's
//! stuck and needs a manual snapshot restore (Tier 3), short of reading logs. This gauge makes
//! that distinction visible to Prometheus/Alertmanager.
//!
//! Uses the same global-singleton-via-Lazy pattern as sync_metrics.rs's SyncMetrics, for the
//! same reason: the FFI embedding restarts this whole module's owning task on a consensus
//! crash (see ffi.rs's restart loop), and a plain `register_gauge_with_registry!` call on a
//! fresh `Registry` each time would otherwise panic with "AlreadyReg" once mysten_metrics binds
//! a registry across restarts.

use once_cell::sync::Lazy;
use prometheus::{register_gauge_with_registry, Gauge};

static GLOBAL_EPOCH_MONITOR_METRICS: Lazy<EpochMonitorMetrics> =
    Lazy::new(|| EpochMonitorMetrics::register(prometheus::default_registry()));

#[derive(Clone)]
pub(super) struct EpochMonitorMetrics {
    /// How many blocks this validator's Go execution layer is behind the best-known peer
    /// block (peer_best_block.saturating_sub(go_block)). Updated every epoch-monitor poll
    /// cycle for a Validator-mode node in the Healthy phase, whether or not a stall is
    /// currently detected — a healthy node reads ~0, a node mid-recovery reads the shrinking
    /// gap, and a node stuck at a large, non-shrinking value is the real "needs a human"
    /// signal (see the MetanodeBlocksBehindPeer rule in alert_rules.yml).
    pub blocks_behind_peer: Gauge,
}

impl EpochMonitorMetrics {
    /// Get the global singleton metrics instance. Safe to call every poll cycle — metrics are
    /// registered exactly once regardless of how many times this is called or how many times
    /// the owning task restarts.
    pub(super) fn get() -> Self {
        GLOBAL_EPOCH_MONITOR_METRICS.clone()
    }

    fn register(registry: &prometheus::Registry) -> Self {
        Self {
            blocks_behind_peer: register_gauge_with_registry!(
                "consensus_blocks_behind_peer",
                "Blocks this validator's Go execution layer is behind the best-known peer \
                 block, as tracked by the stall-recovery monitor. 0 when caught up.",
                registry
            )
            .expect("valid metric: consensus_blocks_behind_peer"),
        }
    }
}
