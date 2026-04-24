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

/// Centralized state machine for coordinating node phase transitions.
#[derive(Clone)]
pub struct ConsensusCoordinationHub {
    phase: Arc<RwLock<NodeConsensusPhase>>,
}

impl ConsensusCoordinationHub {
    pub fn new() -> Self {
        Self {
            phase: Arc::new(RwLock::new(NodeConsensusPhase::Initializing)),
        }
    }

    /// Retrieve the current consensus phase.
    pub fn get_phase(&self) -> NodeConsensusPhase {
        *self.phase.read()
    }

    /// Transition to a new consensus phase.
    pub fn set_phase(&self, new_phase: NodeConsensusPhase) {
        let mut w = self.phase.write();
        if *w != new_phase {
            tracing::info!(
                "🔄 [HUB] Phase transition: {:?} -> {:?}",
                *w,
                new_phase
            );
            *w = new_phase;
        }
    }

    /// Convenience check for whether we are in a normal operational mode.
    pub fn is_healthy(&self) -> bool {
        matches!(*self.phase.read(), NodeConsensusPhase::Healthy)
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
    /// Creates a hub starting in Healthy phase, for use in tests where
    /// proposals should work immediately without phase transitions.
    pub fn new_for_testing() -> Self {
        Self {
            phase: Arc::new(RwLock::new(NodeConsensusPhase::Healthy)),
        }
    }
}
