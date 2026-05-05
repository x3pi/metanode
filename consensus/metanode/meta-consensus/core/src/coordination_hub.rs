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

use std::sync::atomic::{AtomicBool, Ordering};

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
    quorum_commit_index: Arc<std::sync::atomic::AtomicU32>,
}

impl ConsensusCoordinationHub {
    pub fn new() -> Self {
        Self {
            phase: Arc::new(RwLock::new(NodeConsensusPhase::Initializing)),
            is_transitioning: Arc::new(AtomicBool::new(false)),
            global_exec_index: Arc::new(tokio::sync::Mutex::new(0)),
            quorum_commit_index: Arc::new(std::sync::atomic::AtomicU32::new(0)),
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
            // Phase changed.
            *w = new_phase;
        }
    }

    /// Convenience check for whether we are in a normal operational mode.
    pub fn is_healthy(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::Healthy)
    }

    /// Returns true if the node is Healthy.
    pub fn is_healthy_stable(&self) -> bool {
        self.is_healthy()
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
            quorum_commit_index: Arc::new(std::sync::atomic::AtomicU32::new(0)),
        }
    }

    /// Alias for `new_for_testing()` — both have network pre-confirmed.
    pub fn new_for_testing_stable() -> Self {
        Self::new_for_testing()
    }
}
