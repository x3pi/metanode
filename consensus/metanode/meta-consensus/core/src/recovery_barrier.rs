// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Unified Recovery Barrier — Single Source of Truth for Snapshot Recovery Safety
//!
//! ## Problem
//! Post-snapshot recovery has historically been managed by 6+ independent boolean flags
//! (`startup_sync_active`, `schedule_recovery_pending`, `go_sync_completed`,
//! `network_synced_commits`, `is_local_commit_unlocked`, etc.) spread across
//! `coordination_hub.rs`, `commit_syncer.rs`, `commit_manager.rs`, and `consensus_node.rs`.
//!
//! These flags interact in ways that create edge cases:
//! - Epoch boundaries reset commit counts → `handled_commits >= 300` guard bypassed
//! - Sync-mode schedule updates clear `schedule_recovery_pending` prematurely
//! - `startup_sync_active` cleared too early → premature Healthy transition
//!
//! ## Solution
//! A single state machine that enforces a strict phase progression:
//!
//! ```text
//! Inactive → GoSyncing → DagCatchingUp → ScheduleVerifying → Ready
//!              ↑                                                 │
//!              └─────── (node restart from snapshot) ────────────┘
//! ```
//!
//! **Invariant**: The node may ONLY propose blocks when `phase == Ready`.
//! This is checked at a single point: `can_propose()`.
//!
//! **Epoch-agnostic**: The barrier doesn't depend on commit counts, epoch numbers,
//! or scoring window sizes. It tracks whether each recovery phase has completed.

use std::sync::atomic::{AtomicU8, Ordering};

/// Recovery phases — ordered from "most restricted" to "fully operational".
/// The u8 repr allows atomic operations.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
#[repr(u8)]
pub enum RecoveryPhase {
    /// No snapshot recovery in progress — node started with intact DAG
    /// or has completed all recovery phases. Proposals are ALLOWED.
    Inactive = 0,

    /// Go execution layer is syncing blocks from peers (STARTUP-SYNC active).
    /// Proposals are BLOCKED.
    GoSyncing = 1,

    /// Go is synced, Rust DAG is catching up via CertifiedCommits from peers.
    /// Proposals are BLOCKED.
    DagCatchingUp = 2,

    /// DAG has caught up to quorum. Waiting for a full LeaderSchedule scoring
    /// cycle to complete with network-verified data, ensuring the LeaderSwapTable
    /// matches the network's table.
    /// Proposals are BLOCKED.
    ScheduleVerifying = 3,

    /// All recovery phases complete. Schedule is verified. Node is safe to
    /// participate in consensus. Proposals are ALLOWED.
    Ready = 4,
}

impl RecoveryPhase {
    fn from_u8(v: u8) -> Self {
        match v {
            0 => Self::Inactive,
            1 => Self::GoSyncing,
            2 => Self::DagCatchingUp,
            3 => Self::ScheduleVerifying,
            4 => Self::Ready,
            _ => Self::Inactive, // fallback
        }
    }
}

impl std::fmt::Display for RecoveryPhase {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Inactive => write!(f, "Inactive"),
            Self::GoSyncing => write!(f, "GoSyncing"),
            Self::DagCatchingUp => write!(f, "DagCatchingUp"),
            Self::ScheduleVerifying => write!(f, "ScheduleVerifying"),
            Self::Ready => write!(f, "Ready"),
        }
    }
}

/// Unified Recovery Barrier.
///
/// Thread-safe state machine that tracks post-snapshot recovery progress.
/// All consensus-critical components check `can_propose()` or `phase()`
/// instead of checking individual boolean flags.
///
/// ## Thread Safety
/// Uses `AtomicU8` for lock-free reads (hot path: `can_propose()` is called
/// on every proposal attempt). Phase transitions use `compare_exchange` to
/// prevent race conditions.
#[derive(Debug)]
pub struct RecoveryBarrier {
    phase: AtomicU8,
    is_pre_verified: std::sync::atomic::AtomicBool,
}

impl RecoveryBarrier {
    /// Create a new barrier in Inactive state (no recovery needed).
    pub fn new() -> Self {
        Self {
            phase: AtomicU8::new(RecoveryPhase::Inactive as u8),
            is_pre_verified: std::sync::atomic::AtomicBool::new(false),
        }
    }

    /// Create a barrier for testing — already in Ready/Inactive state.
    #[cfg(test)]
    pub fn new_for_testing() -> Self {
        Self::new()
    }

    /// The ONLY question that matters for consensus safety.
    ///
    /// Returns `true` if the node is safe to propose blocks:
    /// - `Inactive`: no recovery in progress (normal start)
    /// - `Ready`: all recovery phases completed
    ///
    /// Returns `false` for all intermediate recovery phases.
    #[inline]
    pub fn can_propose(&self) -> bool {
        let phase = self.phase.load(Ordering::Acquire);
        phase == RecoveryPhase::Inactive as u8 || phase == RecoveryPhase::Ready as u8
    }

    /// Current recovery phase.
    pub fn phase(&self) -> RecoveryPhase {
        RecoveryPhase::from_u8(self.phase.load(Ordering::Acquire))
    }

    /// Is the barrier active? (i.e., snapshot recovery in progress)
    pub fn is_active(&self) -> bool {
        let phase = self.phase.load(Ordering::Acquire);
        phase != RecoveryPhase::Inactive as u8 && phase != RecoveryPhase::Ready as u8
    }

    /// Is the barrier waiting for a LeaderSchedule scoring cycle?
    pub fn is_schedule_pending(&self) -> bool {
        self.phase.load(Ordering::Acquire) == RecoveryPhase::ScheduleVerifying as u8
    }

    /// Is Go still syncing? (STARTUP-SYNC phase)
    pub fn is_go_syncing(&self) -> bool {
        self.phase.load(Ordering::Acquire) == RecoveryPhase::GoSyncing as u8
    }

    /// Is DAG catching up?
    pub fn is_dag_catching_up(&self) -> bool {
        self.phase.load(Ordering::Acquire) == RecoveryPhase::DagCatchingUp as u8
    }

    // ════════════════════════════════════════════════════════════════════
    // Phase Transitions — strict forward-only progression
    // ════════════════════════════════════════════════════════════════════

    /// Activate the barrier for snapshot recovery.
    /// Called when: `!dag_has_history && go_has_blocks` (epoch-agnostic detection).
    ///
    /// Transitions: `Inactive → GoSyncing`
    pub fn activate(&self) {
        let prev = self.phase.swap(RecoveryPhase::GoSyncing as u8, Ordering::Release);
        let prev_phase = RecoveryPhase::from_u8(prev);
        tracing::warn!(
            "🛡️ [RECOVERY-BARRIER] ACTIVATED: {} → GoSyncing. \
             Snapshot recovery detected. ALL proposals blocked until recovery completes.",
            prev_phase
        );
    }

    /// Go sync completed — advance to DAG catch-up phase.
    /// Called when: STARTUP-SYNC finishes syncing blocks from peers.
    ///
    /// Transitions: `GoSyncing → DagCatchingUp`
    pub fn go_sync_done(&self) {
        let result = self.phase.compare_exchange(
            RecoveryPhase::GoSyncing as u8,
            RecoveryPhase::DagCatchingUp as u8,
            Ordering::Release,
            Ordering::Acquire,
        );
        match result {
            Ok(_) => {
                tracing::info!(
                    "✅ [RECOVERY-BARRIER] GoSyncing → DagCatchingUp. \
                     Go execution layer synced. Waiting for Rust DAG to catch up via CertifiedCommits."
                );
            }
            Err(current) => {
                let current_phase = RecoveryPhase::from_u8(current);
                tracing::debug!(
                    "🔄 [RECOVERY-BARRIER] go_sync_done() called but phase is {} (not GoSyncing). \
                     No transition needed.",
                    current_phase
                );
            }
        }
    }

    /// DAG has caught up to quorum — advance to schedule verification (or Ready if pre-verified).
    /// Called when: `local_commit >= quorum_commit && quorum_commit > 0`.
    ///
    /// Transitions: `DagCatchingUp → ScheduleVerifying` (or `DagCatchingUp → Ready`)
    pub fn dag_caught_up(&self) {
        let next_phase = if self.is_pre_verified.load(Ordering::Acquire) {
            RecoveryPhase::Ready as u8
        } else {
            RecoveryPhase::ScheduleVerifying as u8
        };
        
        let result = self.phase.compare_exchange(
            RecoveryPhase::DagCatchingUp as u8,
            next_phase,
            Ordering::Release,
            Ordering::Acquire,
        );
        match result {
            Ok(_) => {
                if next_phase == RecoveryPhase::Ready as u8 {
                    tracing::info!(
                        "✅ [RECOVERY-BARRIER] DagCatchingUp → Ready. \
                         DAG reached quorum AND schedule was pre-verified via baseline reputation scores. \
                         Node is now safe to propose blocks."
                    );
                } else {
                    tracing::info!(
                        "✅ [RECOVERY-BARRIER] DagCatchingUp → ScheduleVerifying. \
                         DAG reached quorum. Waiting for a full 300-commit LeaderSchedule scoring cycle."
                    );
                }
            }
            Err(current) => {
                let current_phase = RecoveryPhase::from_u8(current);
                tracing::debug!(
                    "🔄 [RECOVERY-BARRIER] dag_caught_up() called but phase is {} (not DagCatchingUp). \
                     No transition needed.",
                    current_phase
                );
            }
        }
    }

    /// Mark the schedule as pre-verified.
    /// Called when the node successfully injects reputation scores from a network baseline.
    /// This allows `dag_caught_up()` to bypass the `ScheduleVerifying` phase and go straight to `Ready`.
    pub fn set_schedule_pre_verified(&self) {
        self.is_pre_verified.store(true, Ordering::Release);
        tracing::info!("🛡️ [RECOVERY-BARRIER] Schedule marked as PRE-VERIFIED via baseline.");
        
        // If we are ALREADY in ScheduleVerifying when this is called, we can jump to Ready!
        let _ = self.phase.compare_exchange(
            RecoveryPhase::ScheduleVerifying as u8,
            RecoveryPhase::Ready as u8,
            Ordering::Release,
            Ordering::Acquire,
        );
    }

    /// LeaderSchedule scoring cycle completed — recovery is complete.
    /// Called when: `commits_until_update == 0` (full 300-commit cycle) AND
    /// the barrier is in ScheduleVerifying phase.
    ///
    /// Transitions: `ScheduleVerifying → Ready`
    pub fn schedule_verified(&self) {
        let result = self.phase.compare_exchange(
            RecoveryPhase::ScheduleVerifying as u8,
            RecoveryPhase::Ready as u8,
            Ordering::Release,
            Ordering::Acquire,
        );
        match result {
            Ok(_) => {
                tracing::info!(
                    "✅ [RECOVERY-BARRIER] ScheduleVerifying → Ready. \
                     Full 300-commit scoring cycle completed. LeaderSwapTable is authoritative. \
                     Node is now safe to propose blocks."
                );
            }
            Err(current) => {
                let current_phase = RecoveryPhase::from_u8(current);
                tracing::debug!(
                    "🔄 [RECOVERY-BARRIER] schedule_verified() called but phase is {} (not ScheduleVerifying). \
                     No transition needed.",
                    current_phase
                );
            }
        }
    }

    /// Reset the barrier to Inactive (for epoch transitions or testing).
    pub fn reset(&self) {
        let prev = self.phase.swap(RecoveryPhase::Inactive as u8, Ordering::Release);
        self.is_pre_verified.store(false, Ordering::Release);
        let prev_phase = RecoveryPhase::from_u8(prev);
        if prev_phase != RecoveryPhase::Inactive {
            tracing::info!(
                "🔄 [RECOVERY-BARRIER] Reset: {} → Inactive.",
                prev_phase
            );
        }
    }
}

impl Default for RecoveryBarrier {
    fn default() -> Self {
        Self::new()
    }
}

impl Clone for RecoveryBarrier {
    fn clone(&self) -> Self {
        Self {
            phase: AtomicU8::new(self.phase.load(Ordering::Acquire)),
            is_pre_verified: std::sync::atomic::AtomicBool::new(self.is_pre_verified.load(Ordering::Acquire)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_normal_startup_can_propose() {
        let barrier = RecoveryBarrier::new();
        assert!(barrier.can_propose());
        assert_eq!(barrier.phase(), RecoveryPhase::Inactive);
        assert!(!barrier.is_active());
    }

    #[test]
    fn test_full_recovery_sequence() {
        let barrier = RecoveryBarrier::new();
        
        // Activate for snapshot recovery
        barrier.activate();
        assert!(!barrier.can_propose());
        assert!(barrier.is_active());
        assert_eq!(barrier.phase(), RecoveryPhase::GoSyncing);

        // Go sync done
        barrier.go_sync_done();
        assert!(!barrier.can_propose());
        assert_eq!(barrier.phase(), RecoveryPhase::DagCatchingUp);

        // DAG caught up
        barrier.dag_caught_up();
        assert!(!barrier.can_propose());
        assert_eq!(barrier.phase(), RecoveryPhase::ScheduleVerifying);

        // Schedule verified
        barrier.schedule_verified();
        assert!(barrier.can_propose());
        assert_eq!(barrier.phase(), RecoveryPhase::Ready);
    }

    #[test]
    fn test_out_of_order_transitions_rejected() {
        let barrier = RecoveryBarrier::new();
        barrier.activate();

        // Try to skip DagCatchingUp → ScheduleVerifying should fail
        barrier.dag_caught_up(); // should no-op (phase is GoSyncing, not DagCatchingUp)
        assert_eq!(barrier.phase(), RecoveryPhase::GoSyncing);

        // Try to skip directly to Ready
        barrier.schedule_verified(); // should no-op
        assert_eq!(barrier.phase(), RecoveryPhase::GoSyncing);
    }

    #[test]
    fn test_idempotent_transitions() {
        let barrier = RecoveryBarrier::new();
        barrier.activate();
        barrier.go_sync_done();
        barrier.go_sync_done(); // second call should be no-op
        assert_eq!(barrier.phase(), RecoveryPhase::DagCatchingUp);
    }

    #[test]
    fn test_reset() {
        let barrier = RecoveryBarrier::new();
        barrier.activate();
        barrier.go_sync_done();
        barrier.reset();
        assert!(barrier.can_propose());
        assert_eq!(barrier.phase(), RecoveryPhase::Inactive);
    }

    #[test]
    fn test_phase_queries() {
        let barrier = RecoveryBarrier::new();
        barrier.activate();
        assert!(barrier.is_go_syncing());
        assert!(!barrier.is_dag_catching_up());
        assert!(!barrier.is_schedule_pending());

        barrier.go_sync_done();
        assert!(!barrier.is_go_syncing());
        assert!(barrier.is_dag_catching_up());

        barrier.dag_caught_up();
        assert!(barrier.is_schedule_pending());
    }
}
