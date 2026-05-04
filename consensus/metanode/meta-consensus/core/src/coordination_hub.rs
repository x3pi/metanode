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

use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};

/// Minimum number of consecutive CertifiedCommits that must be processed
/// in Healthy phase before the local committer is unlocked.
///
/// A single CertifiedCommit is insufficient proof that the DAG is dense enough
/// for correct `calculate_commit_timestamp()` — the node may have received just
/// one lucky commit while most ancestor blocks are still missing.
///
/// 3 consecutive commits provide strong evidence that:
/// 1. The DAG is well-connected (each commit's sub-dag was linearized successfully)
/// 2. Ancestor blocks from prior rounds are present (timestamp medians are correct)
/// 3. The node is genuinely caught up, not just transiently at lag==0
const MIN_NETWORK_CONFIRMS: u32 = 3;

/// Centralized state machine for coordinating node phase transitions.
#[derive(Clone)]
pub struct ConsensusCoordinationHub {
    phase: Arc<RwLock<NodeConsensusPhase>>,
    
    /// Global flag indicating if the epoch is currently transitioning.
    /// Mutated during Start/End of epoch transition.
    /// Read by TX Receivers (UDS) to reject transactions, and by Executors to pause execution.
    is_transitioning: Arc<AtomicBool>,

    /// The highest Global Execution Index (GEI) that Go has executed or skipped.
    /// Mutated by CommitProcessor (skip) and CommitObserver (execution).
    /// Read by Peer P2P Sync to inform peers of local catch-up progress.
    global_exec_index: Arc<tokio::sync::Mutex<u64>>,

    /// The highest quorum commit index observed by CommitVoteMonitor.
    /// Used by Core to prevent the local committer from diverging when missing network commits.
    quorum_commit_index: Arc<AtomicU32>,

    /// FORK-SAFETY: Network confirmation counter.
    ///
    /// Reset to 0 whenever the node transitions to Healthy phase from a catch-up phase.
    /// Incremented by 1 each time a CertifiedCommit is successfully processed while Healthy.
    /// The local committer is BLOCKED until this counter reaches MIN_NETWORK_CONFIRMS.
    ///
    /// Why a counter instead of a boolean flag?
    /// A single CertifiedCommit is insufficient — the DAG might still be sparse.
    /// Requiring multiple consecutive confirms provides stronger evidence that:
    /// 1. The DAG is well-connected across multiple rounds
    /// 2. `calculate_commit_timestamp()` has enough ancestor blocks for correct medians
    /// 3. The node is genuinely caught up, not just transiently at lag==0
    ///
    /// This is deterministic and event-driven — no arbitrary time delays.
    network_confirm_count: Arc<AtomicU32>,
}

impl ConsensusCoordinationHub {
    pub fn new() -> Self {
        Self {
            phase: Arc::new(RwLock::new(NodeConsensusPhase::Initializing)),
            is_transitioning: Arc::new(AtomicBool::new(false)),
            global_exec_index: Arc::new(tokio::sync::Mutex::new(0)),
            quorum_commit_index: Arc::new(AtomicU32::new(0)),
            network_confirm_count: Arc::new(AtomicU32::new(0)),
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
    pub fn set_phase(&self, new_phase: NodeConsensusPhase) {
        let mut w = self.phase.write();
        if *w != new_phase {
            let old_phase = *w;
            tracing::info!(
                "🔄 [HUB] Phase transition: {:?} -> {:?}",
                old_phase,
                new_phase
            );
            // FORK-SAFETY: When transitioning TO Healthy from a catch-up phase,
            // require network confirmation before allowing local commit decisions.
            //
            // This blocks the local committer until a CertifiedCommit is processed,
            // proving the DAG is populated enough for correct timestamp calculation.
            //
            // EXCEPTION: Bootstrapping → Healthy (fresh cluster start) does NOT
            // require confirmation. In a fresh cluster all nodes start together and
            // there are no CertifiedCommits yet — all commits are locally decided.
            // Requiring confirmation here would deadlock the entire network.
            if new_phase == NodeConsensusPhase::Healthy {
                let needs_confirmation = matches!(
                    old_phase,
                    NodeConsensusPhase::CatchingUp
                        | NodeConsensusPhase::StateSyncing
                        | NodeConsensusPhase::Aligning
                );
                if needs_confirmation {
                    self.network_confirm_count.store(0, Ordering::Release);
                    tracing::info!(
                        "🛡️ [HUB] Network confirmation required (was {:?}). Local committer \
                         blocked until {} consecutive CertifiedCommits are processed in Healthy phase.",
                        old_phase, MIN_NETWORK_CONFIRMS
                    );
                } else {
                    // Fresh start or direct transition — local committer enabled immediately
                    // Set counter to MIN_NETWORK_CONFIRMS so is_healthy_stable() returns true
                    self.network_confirm_count.store(MIN_NETWORK_CONFIRMS, Ordering::Release);
                    tracing::info!(
                        "✅ [HUB] Direct transition {:?}→Healthy. Local committer enabled \
                         (no catch-up phase detected).",
                        old_phase
                    );
                }
            }
            *w = new_phase;
        }
    }

    /// Convenience check for whether we are in a normal operational mode.
    pub fn is_healthy(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::Healthy)
    }

    /// Called by Core::add_certified_commits() after successfully processing
    /// CertifiedCommits from the network. If the node is in Healthy phase,
    /// this clears the network confirmation flag, enabling the local committer.
    ///
    /// This is the ONLY way to unlock the local committer after a phase transition
    /// to Healthy. It proves the DAG is populated and the node is truly caught up.
    pub fn confirm_network_commit(&self) {
        if !self.is_healthy() {
            return;
        }
        let count = self.network_confirm_count.fetch_add(1, Ordering::AcqRel) + 1;
        if count == MIN_NETWORK_CONFIRMS {
            tracing::info!(
                "✅ [HUB] Network confirmed! {} consecutive CertifiedCommits processed \
                 while Healthy. Local committer is now enabled.",
                count
            );
        } else if count < MIN_NETWORK_CONFIRMS {
            tracing::info!(
                "🛡️ [HUB] Network confirmation progress: {}/{} CertifiedCommits. \
                 Local committer still blocked.",
                count, MIN_NETWORK_CONFIRMS
            );
        }
        // If count > MIN_NETWORK_CONFIRMS, no log needed — already confirmed
    }

    /// Returns true if the node is Healthy AND has been confirmed by the network
    /// (i.e., at least MIN_NETWORK_CONFIRMS consecutive CertifiedCommits have been
    /// processed while in Healthy phase).
    ///
    /// The local committer MUST use this instead of `is_healthy()` to prevent
    /// producing commits with divergent timestamps from a sparse DAG.
    ///
    /// After snapshot restore, the node oscillates CatchingUp↔Healthy rapidly.
    /// If the local committer fires during a brief Healthy window, it evaluates
    /// leaders on a sparse DAG → `calculate_commit_timestamp()` produces wrong
    /// median → timestamp divergence → fork.
    ///
    /// This guard is deterministic and event-driven:
    /// - Transitions to Healthy → `network_confirm_count = 0`
    /// - Each CertifiedCommit processed while Healthy → counter increments
    /// - Counter reaches MIN_NETWORK_CONFIRMS → local committer enabled
    /// - No arbitrary time delays
    pub fn is_healthy_stable(&self) -> bool {
        if !self.is_healthy() {
            return false;
        }
        let count = self.network_confirm_count.load(Ordering::Acquire);
        if count < MIN_NETWORK_CONFIRMS {
            tracing::debug!(
                "🛡️ [NETWORK-CONFIRM] Healthy but only {}/{} network confirmations. \
                 Local committer blocked until {} consecutive CertifiedCommits are processed.",
                count, MIN_NETWORK_CONFIRMS, MIN_NETWORK_CONFIRMS
            );
            return false;
        }
        true
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
    /// Proposals are only allowed in Healthy phase.
    pub fn should_skip_proposal(&self) -> bool {
        !self.is_healthy()
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
            is_transitioning: Arc::new(AtomicBool::new(false)),
            global_exec_index: Arc::new(tokio::sync::Mutex::new(0)),
            quorum_commit_index: Arc::new(AtomicU32::new(0)),
            // Network already confirmed — local committer works immediately in tests
            network_confirm_count: Arc::new(AtomicU32::new(MIN_NETWORK_CONFIRMS)),
        }
    }

    /// Alias for `new_for_testing()` — both have network pre-confirmed.
    pub fn new_for_testing_stable() -> Self {
        Self::new_for_testing()
    }
}
