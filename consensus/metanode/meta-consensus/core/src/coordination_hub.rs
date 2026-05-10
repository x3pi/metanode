// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! ConsensusCoordinationHub State Machine
//!
//! Provides a unified state machine for tracking and orchestrating the operational phase
//! of the consensus node. This replaces fragmented phase-tracking variables across
//! CommitSyncer, DagState, and the main Node modes.
//!
//! ## Phase Lifecycle (Maps to CONSENSUS_ARCHITECTURE_VN.md)
//!
//! ```text
//! Initializing → Bootstrapping → CatchingUp → Aligning → Healthy
//!                                    ↓
//!                               StateSyncing → (restart) → Initializing
//! ```
//!
//! - **Initializing**: Phase 1+2 — Waiting for Go handshake + loading local DAG from RocksDB.
//! - **Bootstrapping**: Phase 3 pre-baseline — DAG loaded, establishing network baseline.
//! - **CatchingUp**: Phase 3A — Actively fetching missing commits from peers.
//! - **StateSyncing**: Phase 3B — Deep lag detected, waiting for state snapshot from Go engine.
//! - **Aligning**: Phase 5 — Aligning Go ↔ Rust state (filtering already-executed blocks).
//! - **Healthy**: Phase 6 — Active consensus participation (proposing, voting).

use std::sync::Arc;
use parking_lot::RwLock;
use crate::recovery_barrier::RecoveryBarrier;

/// Represents the global operational phase of the node.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NodeConsensusPhase {
    /// Phase 1+2: Node is connecting to Go executor, verifying identity,
    /// and loading DAG state from local RocksDB.
    /// Core must NOT propose blocks or connect to P2P in this phase.
    Initializing,

    /// Phase 3 pre-baseline: DAG has been loaded from local storage.
    /// CommitSyncer is establishing the network baseline (reset_to_network_baseline).
    /// Core must NOT propose blocks to avoid equivocation.
    Bootstrapping,
    
    /// Phase 3A: Node is significantly lagging behind the network quorum.
    /// Aggressive commit synchronization is active via CommitSyncer.
    CatchingUp,

    /// Phase 3B: Node is lagging by a huge margin (epoch boundary crossed multiple times).
    /// P2P streaming is suspended; Node awaits State Snapshot from Go engine.
    StateSyncing,

    /// Phase 5: Aligning Go execution state with Rust DAG state.
    /// CommitObserver is filtering already-executed blocks (number ≤ N).
    /// Anti-fork hash check occurs in this phase.
    Aligning,

    /// Phase 6: Normal consensus operation — proposing blocks, voting, and DAG building.
    Healthy,
}

use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

/// Centralized state machine for coordinating node phase transitions.
#[derive(Clone)]
pub struct ConsensusCoordinationHub {
    phase: Arc<RwLock<NodeConsensusPhase>>,
    
    /// ═══════════════════════════════════════════════════════════════
    /// UNIFIED RECOVERY BARRIER (May 2026 — Architectural Fix)
    ///
    /// Single source of truth for post-snapshot recovery safety.
    /// Replaces the interaction of: startup_sync_active, schedule_recovery_pending,
    /// go_sync_completed, network_synced_commits, and is_local_commit_unlocked.
    ///
    /// Phase progression: Inactive → GoSyncing → DagCatchingUp → ScheduleVerifying → Ready
    /// Only Ready or Inactive allows proposals.
    /// ═══════════════════════════════════════════════════════════════
    recovery_barrier: Arc<RecoveryBarrier>,

    /// Deterministic recovery guard for the local committer.
    /// After snapshot recovery, this is `false`. It is set to `true` ONLY when
    /// the node processes a real CertifiedCommit from the network (via `add_certified_commits`).
    /// This replaces the timeout-based DAG-GAP-GUARD with event-driven safety.
    recovery_local_commit_unlocked: Arc<AtomicBool>,
    
    /// Global flag indicating if the epoch is currently transitioning.
    /// Mutated during Start/End of epoch transition.
    /// Read by TX Receivers (UDS) to reject transactions, and by Executors to pause execution.
    is_transitioning: Arc<AtomicBool>,

    /// When true, STARTUP-SYNC is actively importing blocks from peers.
    /// Proposals are blocked regardless of phase to prevent fork from stale DAG metadata.
    /// Set by consensus_node.rs STARTUP-SYNC section.
    /// LEGACY: Now shadowed by recovery_barrier.is_go_syncing() — kept for backward compat.
    startup_sync_active: Arc<AtomicBool>,

    /// The highest Global Execution Index (GEI) that Go has executed or skipped.
    /// Mutated by CommitProcessor (skip) and CommitObserver (execution).
    /// Read by Peer P2P Sync to inform peers of local catch-up progress.
    global_exec_index: Arc<tokio::sync::Mutex<u64>>,

    /// The highest quorum commit index observed by CommitVoteMonitor.
    /// Used by Core to prevent the local committer from diverging when missing network commits.
    quorum_commit_index: Arc<std::sync::atomic::AtomicU32>,

    /// When true, Go has successfully synchronized with peers (or determined it is isolated).
    /// Used to safely break Bootstrapping deadlocks without relying on fixed timeouts.
    startup_go_sync_completed: Arc<AtomicBool>,

    /// LEGACY: Now shadowed by recovery_barrier.is_schedule_pending() — kept for backward compat.
    schedule_recovery_pending: Arc<AtomicBool>,

    /// FORK-SAFETY (May 2026 — Structural Fix 2):
    /// Set to `true` ONLY after POST-GATE-VERIFY in consensus_node.rs confirms
    /// that the local block hash at the tip matches the peer's block hash.
    /// CommitSyncer's determine_startup_sync_exit() uses this as Gate 5.
    /// This ensures the node CANNOT transition to Healthy or propose blocks
    /// until its state is bit-perfect verified against the network.
    block_hash_verified: Arc<AtomicBool>,

    /// Counter to track how many network commits we've processed since transitioning to Healthy.
    /// Used to prevent the local committer from evaluating a sparse DAG immediately after fast-forwarding.
    network_commits_since_healthy: Arc<AtomicUsize>,
}

impl ConsensusCoordinationHub {
    pub fn new() -> Self {
        Self {
            phase: Arc::new(RwLock::new(NodeConsensusPhase::Initializing)),
            recovery_barrier: Arc::new(RecoveryBarrier::new()),
            recovery_local_commit_unlocked: Arc::new(AtomicBool::new(false)),
            is_transitioning: Arc::new(AtomicBool::new(false)),
            startup_sync_active: Arc::new(AtomicBool::new(false)),
            global_exec_index: Arc::new(tokio::sync::Mutex::new(0)),
            quorum_commit_index: Arc::new(std::sync::atomic::AtomicU32::new(0)),
            startup_go_sync_completed: Arc::new(AtomicBool::new(false)),
            schedule_recovery_pending: Arc::new(AtomicBool::new(false)),
            block_hash_verified: Arc::new(AtomicBool::new(false)),
            network_commits_since_healthy: Arc::new(AtomicUsize::new(0)),
        }
    }

    /// Set initial GEI value (e.g., loaded from DB or network boundary)
    pub async fn set_initial_global_exec_index(&self, gei: u64) {
        let mut lock = self.global_exec_index.lock().await;
        *lock = gei;
    }

    /// Retrieve the shared reference to the Global Execution Index
    pub fn get_global_exec_index_ref(&self) -> Arc<tokio::sync::Mutex<u64>> {
        self.global_exec_index.clone()
    }

    /// Retrieve the shared reference to the transition flag
    pub fn get_is_transitioning_ref(&self) -> Arc<AtomicBool> {
        self.is_transitioning.clone()
    }

    /// Check if epoch is transitioning
    pub fn is_epoch_transitioning(&self) -> bool {
        self.is_transitioning.load(Ordering::Acquire)
    }

    /// Set epoch transitioning flag
    pub fn set_epoch_transitioning(&self, is_transitioning: bool) {
        self.is_transitioning.store(is_transitioning, Ordering::Release);
    }
    
    /// Atomically swap the epoch transitioning flag and return the old value
    pub fn swap_epoch_transitioning(&self, is_transitioning: bool) -> bool {
        self.is_transitioning.swap(is_transitioning, Ordering::SeqCst)
    }

    /// Update the highest quorum commit index observed
    pub fn update_quorum_commit_index(&self, index: u32) {
        let current = self.quorum_commit_index.load(Ordering::Relaxed);
        if index > current {
            self.quorum_commit_index.fetch_max(index, Ordering::Relaxed);
        }
    }

    /// Reset the highest quorum commit index observed (used during epoch transition)
    pub fn reset_quorum_commit_index(&self, index: u32) {
        self.quorum_commit_index.store(index, Ordering::Relaxed);
    }

    /// Retrieve the highest quorum commit index
    pub fn get_quorum_commit_index(&self) -> u32 {
        self.quorum_commit_index.load(Ordering::Relaxed)
    }

    /// Retrieve the current consensus phase.
    pub fn get_phase(&self) -> NodeConsensusPhase {
        *self.phase.read()
    }

    /// Transition to a new consensus phase.
    /// 
    /// FORK-SAFETY (May 2026): This is the **choke-point guard** for ALL transitions
    /// to Healthy. Rather than guarding each individual code path in CommitSyncer's
    /// update_state() (which has 6+ branches that can resolve to Healthy), we block
    /// the transition here at the single point where ALL paths converge.
    /// 
    /// If `startup_sync_active` is true, the node has NOT yet proven network parity
    /// (no certified commits fetched from peers). Transitioning to Healthy would allow
    /// the node to propose blocks with a diverged DAG view → consensus fork.
    pub fn set_phase(&self, new_phase: NodeConsensusPhase) {
        let mut w = self.phase.write();
        if *w != new_phase {
            // ═══════════════════════════════════════════════════════════════
            // CHOKE-POINT GUARD: Block ANY transition to Healthy if
            // startup_sync is still active OR if the RecoveryBarrier
            // indicates recovery is still in progress.
            // This catches ALL code paths including bootstrap exit,
            // update_state non-startup branch, and any future paths.
            //
            // DEFENSE-IN-DEPTH: Even if startup_sync_active is somehow
            // cleared early (bug), the barrier provides a second layer
            // of protection since it tracks the ACTUAL recovery phase.
            // ═══════════════════════════════════════════════════════════════
            if new_phase == NodeConsensusPhase::Healthy 
                && (self.startup_sync_active.load(Ordering::Acquire)
                    || !self.recovery_barrier.can_propose())
            {
                tracing::warn!(
                    "🚫 [HUB] BLOCKED {:?} → Healthy: recovery not complete! \
                     startup_sync={}, barrier_phase={}. \
                     Node must complete all recovery phases before transitioning.",
                    *w,
                    self.startup_sync_active.load(Ordering::Acquire),
                    self.recovery_barrier.phase()
                );
                return; // Refuse the transition
            }

            let old_phase = *w;
            tracing::info!(
                "🔄 [HUB] Phase transition: {:?} -> {:?}",
                old_phase,
                new_phase
            );
            // Phase changed.
            *w = new_phase;
            
            if new_phase == NodeConsensusPhase::Healthy {
                self.reset_network_commits_since_healthy();
            }
        }
    }

    /// Convenience check for whether we are in a normal operational mode.
    pub fn is_healthy(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::Healthy)
    }

    /// Convenience check for whether the node is Healthy.
    pub fn is_healthy_stable(&self) -> bool {
        self.is_healthy()
    }

    /// Returns true if the local committer is allowed to run.
    /// After snapshot recovery, this is false until the node processes
    /// a minimum number of CertifiedCommits from the network in Healthy phase,
    /// proving DAG density and consistency.
    pub fn is_local_commit_unlocked(&self) -> bool {
        self.recovery_local_commit_unlocked.load(Ordering::Acquire)
    }

    /// Reset the network commits counter when transitioning to Healthy.
    pub fn reset_network_commits_since_healthy(&self) {
        self.network_commits_since_healthy.store(0, Ordering::Release);
    }

    /// Increment the number of network commits processed since becoming Healthy.
    pub fn inc_network_commits_since_healthy(&self, count: usize) {
        self.network_commits_since_healthy.fetch_add(count, Ordering::Release);
    }

    /// Get the number of network commits processed since becoming Healthy.
    pub fn get_network_commits_since_healthy(&self) -> usize {
        self.network_commits_since_healthy.load(Ordering::Acquire)
    }

    /// Unlock the local committer after receiving enough real CertifiedCommits.
    /// Called from `Core::add_certified_commits()` when processing succeeds.
    /// Once unlocked, it stays unlocked for the remainder of this epoch.
    pub fn unlock_local_commit(&self) {
        let commits_processed = self.get_network_commits_since_healthy();
        // DAG DENSITY GUARD: Require 5 commits to be processed in Healthy phase
        // before allowing the local committer to run.
        if commits_processed >= 5 {
            if !self.recovery_local_commit_unlocked.swap(true, Ordering::Release) {
                tracing::info!(
                    "🔓 [RECOVERY-GUARD] Local committer UNLOCKED: processed {} CertifiedCommits from network in Healthy phase. \
                     DAG consistency and density confirmed. Local commit evaluation is now permitted.",
                    commits_processed
                );
            }
        } else {
            tracing::info!(
                "⏳ [RECOVERY-GUARD] Local committer remains locked. Processed {}/5 network commits in Healthy phase.",
                commits_processed
            );
        }
    }

    /// Convenience check for whether the node is explicitly catching up.
    pub fn is_catching_up(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::CatchingUp)
    }

    /// Convenience check for whether the node is waiting for a state snapshot sync.
    pub fn is_state_syncing(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::StateSyncing)
    }

    /// Convenience check for whether the node is still bootstrapping (pre-baseline).
    /// During this phase, Core must NOT propose blocks to avoid equivocation.
    pub fn is_bootstrapping(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::Bootstrapping)
    }

    /// Convenience check for whether the node is still initializing (loading DAG, handshake).
    /// During this phase, NO consensus operations should occur.
    pub fn is_initializing(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::Initializing)
    }

    /// Convenience check for whether the node is aligning Go ↔ Rust state.
    /// CommitObserver should filter already-executed blocks in this phase.
    pub fn is_aligning(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::Aligning)
    }

    /// Returns true if proposals are forbidden in the current phase.
    ///
    /// UNIFIED CHECK (May 2026): Uses RecoveryBarrier as the single source of truth.
    /// Proposals are blocked if:
    ///   1. Node is not in Healthy phase, OR
    ///   2. Recovery barrier is active (any phase except Inactive/Ready)
    ///
    /// This replaces the fragmented check of startup_sync_active + schedule_recovery_pending.
    pub fn should_skip_proposal(&self) -> bool {
        !self.is_healthy() || !self.recovery_barrier.can_propose()
    }

    /// Signal that STARTUP-SYNC has started/finished. While active, proposals are blocked.
    pub fn set_startup_sync_active(&self, active: bool) {
        self.startup_sync_active.store(active, Ordering::Release);
        if active {
            tracing::info!("🔒 [STARTUP-SYNC] Proposals LOCKED until sync completes");
        } else {
            tracing::info!("🔓 [STARTUP-SYNC] Proposals UNLOCKED — sync complete");
        }
    }

    /// Check if STARTUP-SYNC is currently active.
    pub fn is_startup_sync_active(&self) -> bool {
        self.startup_sync_active.load(Ordering::Acquire)
    }

    /// Sets whether Go has completed its startup peer-sync.
    pub fn set_startup_go_sync_completed(&self, completed: bool) {
        self.startup_go_sync_completed.store(completed, Ordering::Release);
        if completed {
            // When Go completes its peer sync, advance the barrier from GoSyncing → DagCatchingUp
            self.recovery_barrier.go_sync_done();
            tracing::info!("✅ [STARTUP-SYNC] Go sync completed, advancing barrier to DagCatchingUp");
        }
    }

    /// Returns whether Go has completed its startup peer-sync.
    pub fn is_startup_go_sync_completed(&self) -> bool {
        self.startup_go_sync_completed.load(Ordering::Acquire)
    }

    /// Marks that a snapshot recovery is in progress and the LeaderSchedule
    /// needs re-confirmation from the network before local commit evaluation.
    pub fn set_schedule_recovery_pending(&self, pending: bool) {
        let was_pending = self.schedule_recovery_pending.swap(pending, Ordering::Release);
        if pending && !was_pending {
            tracing::warn!(
                "🔒 [SCHEDULE-RECOVERY] LeaderSchedule recovery PENDING: \
                 auto-confirmed schedule is stale (snapshot recovery detected). \
                 Local committer blocked until 300-commit scoring cycle completes."
            );
        } else if !pending && was_pending {
            tracing::info!(
                "🔓 [SCHEDULE-RECOVERY] LeaderSchedule recovery CLEARED: \
                 scoring cycle completed with network-verified data. \
                 Local committer may now use the confirmed schedule."
            );
        }
    }

    /// Returns true if the LeaderSchedule needs re-confirmation after snapshot recovery.
    /// UPDATED (May 2026): Checks BOTH legacy flag AND recovery barrier for compatibility.
    pub fn is_schedule_recovery_pending(&self) -> bool {
        self.schedule_recovery_pending.load(Ordering::Acquire)
            || self.recovery_barrier.is_schedule_pending()
    }

    // ════════════════════════════════════════════════════════════════
    // Recovery Barrier API
    // ════════════════════════════════════════════════════════════════

    /// Get a reference to the recovery barrier.
    pub fn recovery_barrier(&self) -> &RecoveryBarrier {
        &self.recovery_barrier
    }

    /// Activate the recovery barrier for snapshot recovery.
    /// Called from consensus_node.rs when snapshot detection is epoch-agnostic.
    /// This replaces the fragmented `handled_commits >= 300` check.
    pub fn activate_recovery_barrier(&self) {
        self.recovery_barrier.activate();
        // Also set legacy flags for backward compatibility
        self.schedule_recovery_pending.store(true, Ordering::Release);
        // Reset block hash verification — must be re-verified after each recovery
        self.block_hash_verified.store(false, Ordering::Release);
    }

    /// Mark block hash as verified against peers.
    /// Called from consensus_node.rs after POST-GATE-VERIFY passes successfully.
    /// This is Gate 5 in determine_startup_sync_exit().
    pub fn set_block_hash_verified(&self, verified: bool) {
        let was = self.block_hash_verified.swap(verified, Ordering::Release);
        if verified && !was {
            tracing::info!(
                "✅ [BLOCK-HASH-GATE] Block hash VERIFIED against peers. \
                 Gate 5 cleared — node state is bit-perfect."
            );
        }
    }

    /// Returns whether the block hash at tip has been verified against peers.
    pub fn is_block_hash_verified(&self) -> bool {
        self.block_hash_verified.load(Ordering::Acquire)
    }

}

impl Default for ConsensusCoordinationHub {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
impl ConsensusCoordinationHub {
    /// Creates a hub starting in Healthy phase with network already confirmed,
    /// for use in tests where the local committer should work immediately.
    pub fn new_for_testing() -> Self {
        Self {
            phase: Arc::new(RwLock::new(NodeConsensusPhase::Healthy)),
            recovery_barrier: Arc::new(RecoveryBarrier::new()), // Inactive = can propose
            recovery_local_commit_unlocked: Arc::new(AtomicBool::new(true)),
            is_transitioning: Arc::new(AtomicBool::new(false)),
            startup_sync_active: Arc::new(AtomicBool::new(false)),
            global_exec_index: Arc::new(tokio::sync::Mutex::new(0)),
            quorum_commit_index: Arc::new(std::sync::atomic::AtomicU32::new(0)),
            startup_go_sync_completed: Arc::new(AtomicBool::new(true)),
            schedule_recovery_pending: Arc::new(AtomicBool::new(false)),
            block_hash_verified: Arc::new(AtomicBool::new(true)),
        }
    }

    /// Alias for `new_for_testing()` — both have network pre-confirmed.
    pub fn new_for_testing_stable() -> Self {
        Self::new_for_testing()
    }
}
