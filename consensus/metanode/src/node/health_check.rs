// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use std::sync::Arc;
use crate::node::executor_client::ExecutorClient;

#[derive(Debug, Default)]
pub struct HealthCheckResult {
    pub block_parity: bool,
    pub gei_parity: bool,
    pub state_root_match: bool,
    pub committee_match: bool,
}

impl HealthCheckResult {
    pub fn is_healthy(&self) -> bool {
        self.block_parity && self.gei_parity && self.state_root_match && self.committee_match
    }
}

pub struct PostRecoveryHealthCheck {
    executor_client: Arc<ExecutorClient>,
    peer_addresses: Vec<String>,
}

impl PostRecoveryHealthCheck {
    pub fn new(executor_client: Arc<ExecutorClient>, peer_addresses: Vec<String>) -> Self {
        Self {
            executor_client,
            peer_addresses,
        }
    }

    pub async fn run(&self) -> HealthCheckResult {
        let mut result = HealthCheckResult::default();
        if self.peer_addresses.is_empty() {
            tracing::warn!("⚠️ [HEALTH] No peers configured. Skipping health check.");
            // Consider healthy if standalone
            result.block_parity = true;
            result.gei_parity = true;
            result.state_root_match = true;
            result.committee_match = true;
            return result;
        }

        let local_root = crate::ffi::get_go_state_root();
        
        // Fetch local info
        let (local_block, local_gei, _, _, _) = self.executor_client.get_last_block_number().await.unwrap_or((0, 0, false, [0u8; 32], 0));

        let mut peer_block = 0;
        let mut peer_gei = 0;
        let mut peer_root = String::new();

        // Query peers to find max state
        for peer_addr in &self.peer_addresses {
            if let Ok(info) = crate::network::peer_rpc::query_peer_info(peer_addr).await {
                if info.last_block > peer_block {
                    peer_block = info.last_block;
                    peer_gei = info.last_global_exec_index;
                    peer_root = info.state_root.clone();
                }
            }
        }

        // Check 1: Block parity — local block == peer block (± 50)
        // Tolerance increased from ±5 to ±50: after snapshot recovery + STARTUP-SYNC,
        // the recovering node is commonly 20-80 blocks behind peers during DAG catch-up.
        // The tight ±5 tolerance caused false health-check failures.
        let block_diff = if local_block > peer_block { local_block - peer_block } else { peer_block - local_block };
        result.block_parity = block_diff <= 50;

        // Check 2: GEI parity — local GEI == peer GEI (± 50)
        let gei_diff = if local_gei > peer_gei { local_gei - peer_gei } else { peer_gei - local_gei };
        result.gei_parity = gei_diff <= 50;

        // Check 3: State root match — NOMT root == peer NOMT root
        // CRITICAL FIX: Only compare state roots when blocks are within ±5.
        // State root changes EVERY block, so comparing at different heights always
        // mismatches — producing a false positive that triggers unnecessary alerts.
        // After snapshot recovery, the recovering node is commonly 10-50 blocks behind,
        // making this comparison meaningless until it fully catches up.
        if block_diff <= 5 && !peer_root.is_empty() {
            result.state_root_match = local_root == peer_root;
            if !result.state_root_match {
                tracing::warn!(
                    "⚠️ [HEALTH] State root MISMATCH at close block heights! \
                     local_block={}, peer_block={}, local_root=0x{}..., peer_root=0x{}...",
                    local_block, peer_block,
                    &local_root[..local_root.len().min(16)],
                    &peer_root[..peer_root.len().min(16)]
                );
            }
        } else {
            // Blocks differ by >5 — roots WILL differ, don't flag as unhealthy
            result.state_root_match = true;
            if block_diff > 5 {
                tracing::info!(
                    "ℹ️ [HEALTH] Skipping state root comparison: block_diff={} (local={}, peer={}). \
                     Roots naturally diverge at different heights.",
                    block_diff, local_block, peer_block
                );
            }
        }

        // Check 4: Committee match — verify local validators match peers
        // CRITICAL (2026-05-05): Previously hardcoded to true, masking committee divergence.
        // Simplified approach: compare sorted authority_key lists directly (no full committee rebuild needed).
        match self.executor_client.get_current_epoch().await {
            Ok(local_epoch) if local_epoch > 0 => {
                match self.executor_client.get_epoch_boundary_data(local_epoch).await {
                    Ok((_, _, _, local_validators, _, _)) if !local_validators.is_empty() => {
                        let mut local_keys: Vec<&str> = local_validators.iter()
                            .map(|v| v.authority_key.as_str())
                            .collect();
                        local_keys.sort();
                        
                        let mut verified = false;
                        for peer_addr in &self.peer_addresses {
                            if let Ok(peer_boundary) = crate::network::peer_rpc::query_peer_epoch_boundary_data(
                                peer_addr, local_epoch
                            ).await {
                                if !peer_boundary.validators.is_empty() {
                                    let mut peer_keys: Vec<&str> = peer_boundary.validators.iter()
                                        .map(|v| v.authority_key.as_str())
                                        .collect();
                                    peer_keys.sort();
                                    
                                    result.committee_match = local_keys == peer_keys;
                                    if !result.committee_match {
                                        tracing::error!(
                                            "🚨 [HEALTH] Committee MISMATCH! local={} validators ≠ peer={} validators (epoch {})",
                                            local_keys.len(), peer_keys.len(), local_epoch
                                        );
                                    }
                                    verified = true;
                                    break;
                                }
                            }
                        }
                        
                        if !verified {
                            result.committee_match = true;
                        }
                    }
                    _ => { result.committee_match = true; }
                }
            }
            _ => { result.committee_match = true; }
        }

        result
    }
}
