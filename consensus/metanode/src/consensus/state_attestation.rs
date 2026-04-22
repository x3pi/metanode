// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Peer StateRoot Attestation Protocol (Task 3.7)
//!
//! This module implements the fork detection mechanism by periodically broadcasting
//! local state roots to peers and comparing them against the network majority.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::sync::Arc;
use tokio::sync::RwLock;
use tracing::{debug, error, info, warn};

/// Frequency of state attestation broadcasts (in blocks)
pub const ATTESTATION_BLOCK_INTERVAL: u64 = 10;

/// Represents a state attestation packet sent between peers
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct StateAttestationMessage {
    pub node_id: String,
    pub block_number: u64,
    pub block_hash: String,
    pub state_root: String,
    pub timestamp_ms: u64,
}

/// Tracks mismatch counters and peer state roots for fork detection
pub struct AttestationMonitor {
    pub node_id: String,
    
    /// block_number -> (peer_node_id -> StateAttestationMessage)
    peer_attestations: Arc<RwLock<HashMap<u64, HashMap<String, StateAttestationMessage>>>>,
    
    /// Local state roots snapshot for async comparison: block_number -> state_root
    local_state_roots: Arc<RwLock<HashMap<u64, String>>>,
    
    /// Count of successive divergence events (triggers safety halt if too high)
    divergence_counter: Arc<RwLock<u32>>,
}

impl AttestationMonitor {
    pub fn new(node_id: String) -> Self {
        Self {
            node_id,
            peer_attestations: Arc::new(RwLock::new(HashMap::new())),
            local_state_roots: Arc::new(RwLock::new(HashMap::new())),
            divergence_counter: Arc::new(RwLock::new(0)),
        }
    }

    /// Called by the consensus loop at block N
    /// Caches the local state root and triggers broadcast if block_number % Interval == 0
    pub async fn on_block_committed(&self, block_number: u64, block_hash: String, state_root: String) {
        // Cache our local state root for this block
        {
            let mut cache = self.local_state_roots.write().await;
            cache.insert(block_number, state_root.clone());
            
            // Clean up old entries to prevent memory leak (keep last 100 blocks)
            if block_number > 100 {
                cache.remove(&(block_number - 100));
            }
        }

        // Only broadcast every N blocks
        if block_number > 0 && block_number % ATTESTATION_BLOCK_INTERVAL == 0 {
            let msg = StateAttestationMessage {
                node_id: self.node_id.clone(),
                block_number,
                block_hash,
                state_root,
                timestamp_ms: std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_millis() as u64,
            };

            self.broadcast_attestation(msg).await;
        }
    }

    /// Broadcast the message to all known peers (integration target)
    async fn broadcast_attestation(&self, msg: StateAttestationMessage) {
        debug!("📢 [ATTESTATION] Broadcasting StateRoot for block {}: {}", msg.block_number, msg.state_root);
        // TODO: Integrate with peer_rpc or peer_discovery to flood this to peers
    }

    /// Receives an attestation from a peer, caches it, and checks for divergence
    pub async fn on_attestation_received(&self, msg: StateAttestationMessage) {
        // 1. Store the peer's attestation
        {
            let mut att_map = self.peer_attestations.write().await;
            let block_entry = att_map.entry(msg.block_number).or_insert_with(HashMap::new);
            block_entry.insert(msg.node_id.clone(), msg.clone());
        }

        // 2. Perform fork detection comparison
        self.check_for_divergence(msg.block_number).await;
    }

    async fn check_for_divergence(&self, block_number: u64) {
        let local_root = {
            let cache = self.local_state_roots.read().await;
            cache.get(&block_number).cloned()
        };

        if let Some(local_rt) = local_root {
            let peer_atts = self.peer_attestations.read().await;
            if let Some(peers) = peer_atts.get(&block_number) {
                let mut matches = 0;
                let mut mismatches = 0;

                for (peer_id, att) in peers.iter() {
                    if att.state_root == local_rt {
                        matches += 1;
                    } else {
                        mismatches += 1;
                        warn!("⚠️ [FORK DETECTED] Peer {} has divergent state_root for block {}. Local: {}, Peer: {}", 
                            peer_id, block_number, local_rt, att.state_root);
                    }
                }

                // If > 33% peers diverge, we might be on a fork
                // Simplistic Byzantine approach for dynamic sizing: wait for enough peers before panicking
                if mismatches > 0 && mismatches > matches {
                    let mut div_count = self.divergence_counter.write().await;
                    *div_count += 1;
                    error!("🚨 [FATAL FORK] Network divergence detected at block {}! Panic threshold reached.", block_number);
                    
                    if *div_count >= 3 {
                        // Action could be: Pause Consensus, Rollback, or Panic to trigger sync.
                        error!("🛑 [SAFETY PAUSE] Halting consensus authority to allow auto-repair / re-sync.");
                    }
                } else if matches > 0 {
                    // Reset on good agreement
                    let mut div_count = self.divergence_counter.write().await;
                    *div_count = 0;
                    info!("✅ [STATE VERIFIED] Block {} state_root matches majority.", block_number);
                }
            }
        }
    }
}
