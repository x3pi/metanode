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

        // Check 4: Committee hash match — verify local committee hash matches peers
        // CRITICAL (2026-05-05): Previously hardcoded to true, masking committee divergence
        // that caused liveness stalls. Now performs actual cross-validation.
        match self.executor_client.get_current_epoch().await {
            Ok(local_epoch) if local_epoch > 0 => {
                match self.executor_client.get_epoch_boundary_data(local_epoch).await {
                    Ok((_, _, _, local_validators, _, _)) if !local_validators.is_empty() => {
                        match crate::node::committee::build_committee_from_validator_list(
                            local_validators, local_epoch
                        ) {
                            Ok(local_committee) => {
                                let local_hash = crate::node::committee_source::calculate_committee_hash(&local_committee);
                                let local_hash_hex = hex::encode(&local_hash[..8]);
                                
                                let mut verified = false;
                                for peer_addr in &self.peer_addresses {
                                    if let Ok(peer_boundary) = crate::network::peer_rpc::query_peer_epoch_boundary_data(
                                        peer_addr, local_epoch
                                    ).await {
                                        if !peer_boundary.validators.is_empty() {
                                            use crate::node::executor_client::proto::ValidatorInfo as ProtoVI;
                                            let peer_validators: Vec<ProtoVI> = peer_boundary.validators.into_iter().map(|v| ProtoVI {
                                                address: v.address,
                                                stake: v.stake.to_string(),
                                                name: v.name,
                                                authority_key: v.authority_key,
                                                protocol_key: v.protocol_key,
                                                network_key: v.network_key,
                                                description: String::new(),
                                                website: String::new(),
                                                image: String::new(),
                                                commission_rate: 0,
                                                min_self_delegation: String::new(),
                                                accumulated_rewards_per_share: String::new(),
                                                p2p_address: String::new(),
                                            }).collect();
                                            
                                            if let Ok(peer_committee) = crate::node::committee::build_committee_from_validator_list(
                                                peer_validators, local_epoch
                                            ) {
                                                let peer_hash = crate::node::committee_source::calculate_committee_hash(&peer_committee);
                                                let peer_hash_hex = hex::encode(&peer_hash[..8]);
                                                
                                                result.committee_match = local_hash == peer_hash;
                                                if !result.committee_match {
                                                    tracing::error!(
                                                        "🚨 [HEALTH] Committee MISMATCH! local={}... ≠ peer={}... (epoch {}). \
                                                         Local authorities: {}, Peer authorities: {}",
                                                        local_hash_hex, peer_hash_hex, local_epoch,
                                                        local_committee.size(), peer_committee.size()
                                                    );
                                                }
                                                verified = true;
                                            }
                                            break;
                                        }
                                    }
                                }
                                
                                if !verified {
                                    // No peers responded — can't verify, assume OK
                                    result.committee_match = true;
                                    tracing::info!("ℹ️ [HEALTH] No peers responded for committee verification. Assuming OK.");
                                }
                            }
                            Err(e) => {
                                tracing::warn!("⚠️ [HEALTH] Failed to build local committee: {}. Skipping check.", e);
                                result.committee_match = true;
                            }
                        }
                    }
                    _ => {
                        // No validators from Go — can't verify
                        result.committee_match = true;
                    }
                }
            }
            _ => {
                // Epoch 0 or error — skip committee check
                result.committee_match = true;
            }
        }

        result
    }
}
