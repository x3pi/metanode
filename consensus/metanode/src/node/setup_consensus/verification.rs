// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::node::ConsensusNode;
use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use std::sync::Arc;
use hex;

impl ConsensusNode {
    pub(super) async fn perform_post_gate_verification(
        config: &NodeConfig,
        coordination_hub: &consensus_core::coordination_hub::ConsensusCoordinationHub,
        executor_client_for_proc: &Arc<ExecutorClient>,
    ) {
                if !config.peer_rpc_addresses.is_empty() {
                    tracing::info!("🛡️ [POST-GATE-VERIFY] Entering STRICT verification loop with trusted nodes.");
                    const MAX_VERIFY_ROUNDS: u32 = 30;
                    let mut verify_round: u32 = 0;
                    loop {
                        verify_round += 1;
                        if verify_round > MAX_VERIFY_ROUNDS {
                            tracing::error!(
                                "🚨 [POST-GATE-VERIFY] Failed to verify block hash after {} rounds ({}s). \
                                 ALL peers unreachable (network partition?). \
                                 Proceeding with block_hash_verified=FALSE. \
                                 Node will NOT propose until CommitSyncer verifies state. \
                                 Self-recovery: verification resumes when peers come online.",
                                MAX_VERIFY_ROUNDS, MAX_VERIFY_ROUNDS * 5
                            );
                            coordination_hub.set_block_hash_verified(false);
                            break;
                        }
                        match executor_client_for_proc.get_last_block_number().await {
                            Ok((local_bn, _gei, true, _local_hash, _epoch)) => {
                                let check_block = local_bn;
                                
                                let effective_hash = if check_block > 0 {
                                    match executor_client_for_proc.get_blocks_range(check_block, check_block).await {
                                        Ok(blocks) if !blocks.is_empty() => blocks[0].block_hash.clone(),
                                        _ => {
                                            tracing::warn!(
                                                "⏳ [POST-GATE-VERIFY] Could not fetch block {} from local Go DB. \
                                                 Retrying... (round {}/{})",
                                                check_block, verify_round, MAX_VERIFY_ROUNDS
                                            );
                                            tokio::time::sleep(std::time::Duration::from_secs(3)).await;
                                            continue;
                                        }
                                    }
                                } else {
                                    vec![0; 32]
                                };
                                
                                let is_zero_hash = effective_hash.iter().all(|&b| b == 0);
                                if is_zero_hash && check_block > 0 {
                                    tracing::warn!(
                                        "⚠️ [POST-GATE-VERIFY] Block {} has zero hash (not yet persisted by Go). \
                                         Waiting for Go to finish executing this block... (round {}/{})",
                                        check_block, verify_round, MAX_VERIFY_ROUNDS
                                    );
                                    tokio::time::sleep(std::time::Duration::from_secs(3)).await;
                                    continue;
                                }
                                
                                match crate::network::peer_rpc::query_peer_epochs_network(&config.peer_rpc_addresses).await {
                                    Ok((_peer_epoch, peer_block, peer_addr, _)) => {
                                        if check_block == 0 && peer_block == 0 {
                                            tracing::info!(
                                                "✅ [POST-GATE-VERIFY] Local and Network both at genesis (block 0). Proceeding."
                                            );
                                            coordination_hub.set_block_hash_verified(true);
                                            break;
                                        }
                                        
                                        if check_block == 0 && peer_block > 0 {
                                            tracing::info!(
                                                "✅ [POST-GATE-VERIFY] Local is at genesis (0) while network has progressed to {}. \
                                                 Proceeding to start consensus so that background CatchingUp can sync blocks.",
                                                peer_block
                                            );
                                            coordination_hub.set_block_hash_verified(true);
                                            break;
                                        }
                                        
                                        match crate::network::peer_rpc::fetch_blocks_from_peer(
                                            &[peer_addr.clone()], check_block, check_block,
                                        ).await {
                                            Ok(peer_blocks) if !peer_blocks.is_empty() => {
                                                let peer_hash = &peer_blocks[0].block_hash;
                                                if effective_hash.as_slice() != peer_hash.as_slice() {
                                                    tracing::error!(
                                                        "🚨 [POST-GATE-VERIFY] Block {} hash MISMATCH! \
                                                         Local hash {} vs Peer hash {}. State is corrupted. \
                                                         HALTING to prevent fork. Node must be re-snapshot'd.",
                                                        check_block, hex::encode(effective_hash), hex::encode(peer_hash)
                                                    );
                                                    panic!(
                                                        "FORK-SAFETY: Block #{} hash mismatch after STARTUP-SYNC. \
                                                         Node state is corrupted — must re-snapshot.",
                                                        check_block
                                                    );
                                                } else {
                                                    tracing::info!(
                                                        "✅ [POST-GATE-VERIFY] Block {} hash matches trusted peer {}. \
                                                         State is bit-perfect. Setting block_hash_verified=true.",
                                                        check_block, peer_addr
                                                    );
                                                    coordination_hub.set_block_hash_verified(true);
                                                    break;
                                                }
                                            }
                                            _ => {
                                                tracing::warn!(
                                                    "⏳ [POST-GATE-VERIFY] Could not fetch block {} from trusted peer {}. \
                                                     Retrying... (round {}/{})",
                                                    check_block, peer_addr, verify_round, MAX_VERIFY_ROUNDS
                                                );
                                                tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                                                continue;
                                            }
                                        }
                                    }
                                    Err(e) => {
                                        tracing::warn!(
                                            "⏳ [POST-GATE-VERIFY] Could not query network for trusted state: {}. \
                                             Retrying... (round {}/{})",
                                            e, verify_round, MAX_VERIFY_ROUNDS
                                        );
                                        tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                                        continue;
                                    }
                                }
                            }
                            _ => {
                                tracing::warn!(
                                    "⏳ [POST-GATE-VERIFY] Go not ready. Retrying... (round {}/{})",
                                    verify_round, MAX_VERIFY_ROUNDS
                                );
                                tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                                continue;
                            }
                        }
                    }
                    
                    if !coordination_hub.is_block_hash_verified() {
                        let bg_hub = coordination_hub.clone();
                        let bg_client = executor_client_for_proc.clone();
                        let bg_peers = config.peer_rpc_addresses.clone();
                        tokio::spawn(async move {
                            tracing::info!(
                                "🔄 [BG-VERIFY] Starting background hash re-verification task. \
                                 Will retry every 30s until peers are reachable and hash matches."
                            );
                            loop {
                                tokio::time::sleep(std::time::Duration::from_secs(30)).await;
                                
                                if bg_hub.is_block_hash_verified() {
                                    tracing::info!("✅ [BG-VERIFY] Block hash already verified. Background task exiting.");
                                    break;
                                }
                                
                                let (local_bn, local_hash) = match bg_client.get_last_block_number().await {
                                    Ok((bn, _, true, hash, _)) => (bn, hash),
                                    _ => continue,
                                };
                                
                                let is_zero = local_hash.iter().all(|&b| b == 0);
                                if is_zero || local_bn == 0 { continue; }
                                
                                match crate::network::peer_rpc::query_peer_epochs_network(&bg_peers).await {
                                    Ok((_epoch, peer_block, peer_addr, _)) => {
                                        if peer_block == 0 { continue; }
                                        let check = std::cmp::min(local_bn, peer_block);
                                        
                                        let local_check_hash: Vec<u8> = if check == local_bn {
                                            local_hash.to_vec()
                                        } else {
                                            match bg_client.get_blocks_range(check, 1).await {
                                                Ok(blocks) if !blocks.is_empty() => {
                                                    blocks[0].block_hash.clone()
                                                }
                                                _ => continue,
                                            }
                                        };
                                        
                                        match crate::network::peer_rpc::fetch_blocks_from_peer(
                                            &[peer_addr.clone()], check, check,
                                        ).await {
                                            Ok(blocks) if !blocks.is_empty() => {
                                                if local_check_hash.as_slice() == blocks[0].block_hash.as_slice() {
                                                    tracing::info!(
                                                        "✅ [BG-VERIFY] Block {} hash MATCHES peer {}! \
                                                         Setting block_hash_verified=true. \
                                                         Node can now transition to Healthy.",
                                                        check, peer_addr
                                                    );
                                                    bg_hub.set_block_hash_verified(true);
                                                    break;
                                                } else {
                                                    tracing::error!(
                                                        "🚨 [BG-VERIFY] Block {} hash MISMATCH! \
                                                         Local={} Peer={}. State is CORRUPTED. \
                                                         Node will remain in degraded mode.",
                                                        check,
                                                        hex::encode(&local_check_hash),
                                                        hex::encode(&blocks[0].block_hash)
                                                    );
                                                }
                                            }
                                            _ => {}
                                        }
                                    }
                                    Err(_) => {}
                                }
                            }
                        });
                    }
                } else {
                    tracing::info!(
                        "ℹ️ [POST-GATE-VERIFY] No peers configured. \
                         Setting block_hash_verified=true (single-node mode)."
                    );
                    coordination_hub.set_block_hash_verified(true);
                }
    }
}
