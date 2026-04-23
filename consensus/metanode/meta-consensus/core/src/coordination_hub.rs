// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! ConsensusCoordinationHub State Machine
//!
//! Provides a unified state machine for tracking and orchestrating the operational phase
//! of the consensus node. This replaces fragmented phase-tracking variables across
//! CommitSyncer, DagState, and the main Node modes.

use std::sync::Arc;
use parking_lot::RwLock;

/// Represents the global operational phase of the node.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NodeConsensusPhase {
    /// Node is verifying identity, connecting to Go executor, and checking database state.
    Bootstrapping,
    
    /// Node is significantly lagging behind the network quorum.
    /// Aggressive commit synchronization is active.
    CatchingUp,

    /// Normal consensus operation and DAG building.
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
            phase: Arc::new(RwLock::new(NodeConsensusPhase::Bootstrapping)),
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
}

impl Default for ConsensusCoordinationHub {
    fn default() -> Self {
        Self::new()
    }
}
