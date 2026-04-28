// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Node Lifecycle Coordinator
//!
//! Replaces fragmented `tokio::spawn` architecture with a central, structured actor
//! managing state transitions, phase barriers, and unified event broadcasting.

use std::sync::Arc;
use tokio::sync::{broadcast, RwLock};
use tracing::{info, warn, error};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NodePhase {
    Bootstrapping,
    ColdRecovery,
    NetworkSync,
    ActiveValidator,
    ActiveSyncOnly,
    Transitioning,
    ShuttingDown,
}

#[derive(Debug, Clone)]
pub enum SystemEvent {
    /// Request to change phase
    PhaseChange(NodePhase),
    /// Critical component failed
    ComponentCrash(String),
    /// Go master lagged out
    SevereLagDetected { rust_gei: u64, go_gei: u64 },
    /// Epoch transition trigger
    EpochEndReached(u64),
}

/// The Central Coordinator overseeing all sub-components.
pub struct ConsensusCoordinator {
    pub current_phase: Arc<RwLock<NodePhase>>,
    event_tx: broadcast::Sender<SystemEvent>,
    task_tracker: Arc<tokio::sync::Mutex<tokio::task::JoinSet<()>>>,
}

impl ConsensusCoordinator {
    pub fn new() -> (Self, broadcast::Receiver<SystemEvent>) {
        let (tx, rx) = broadcast::channel(100);
        let coordinator = Self {
            current_phase: Arc::new(RwLock::new(NodePhase::Bootstrapping)),
            event_tx: tx,
            task_tracker: Arc::new(tokio::sync::Mutex::new(tokio::task::JoinSet::new())),
        };
        (coordinator, rx)
    }

    /// Retrieve a sender attached to the central event bus
    pub fn event_sender(&self) -> broadcast::Sender<SystemEvent> {
        self.event_tx.clone()
    }

    /// Spawn a critical background task. If it exits with an error, it will emit a ComponentCrash event.
    pub async fn spawn_critical_task<F>(&self, name: &'static str, future: F)
    where
        F: std::future::Future<Output = anyhow::Result<()>> + Send + 'static,
    {
        let event_tx = self.event_tx.clone();
        let name_str = name.to_string();
        
        let mut tracker = self.task_tracker.lock().await;
        tracker.spawn(async move {
            if let Err(e) = future.await {
                error!("💀 [COORDINATOR] Critical Component '{}' Crashed! Error: {:?}", name_str, e);
                let _ = event_tx.send(SystemEvent::ComponentCrash(name_str));
            } else {
                warn!("⚠️ [COORDINATOR] Critical Component '{}' exited cleanly unexpectedly.", name_str);
            }
        });
    }

    /// Change the node phase and broadcast
    pub async fn transition_to(&self, new_phase: NodePhase) {
        let mut phase = self.current_phase.write().await;
        if *phase == new_phase { return; }
        
        info!("🔄 [COORDINATOR] Phase Transition: {:?} -> {:?}", *phase, new_phase);
        *phase = new_phase;
        
        // Broadcast the transition change
        let _ = self.event_tx.send(SystemEvent::PhaseChange(new_phase));
    }
}
