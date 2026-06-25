// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use anyhow::Result;
use std::sync::Arc;
use std::time::Duration;
use tracing::{debug, info, warn};

/// Manages Validator multi-epoch catchup transition steps
pub(super) async fn validator_multi_epoch_transition(
    client_arc: &Arc<ExecutorClient>,
    config: &NodeConfig,
    rust_epoch: u64,
    network_epoch: u64,
    local_go_epoch: u64,
    fast_cycles_remaining: &mut u32,
    fast_cycles_max: u32,
) -> Result<()> {
    let peer_rpc = config.peer_rpc_addresses.clone();
    let mut current_rust_epoch = rust_epoch;

    for target_epoch in (rust_epoch + 1)..=network_epoch {
        info!(
            "🔄 [EPOCH MONITOR] Multi-epoch step: {} → {} (target: {})",
            current_rust_epoch, target_epoch, network_epoch
        );

        // Get boundary data from peer (authoritative) or local Go
        let boundary_data = if local_go_epoch < target_epoch && !peer_rpc.is_empty() {
            // LOCAL Go is behind — query PEER for authoritative timestamp
            let mut peer_data: Option<(u64, u64, u64, u64)> = None;
            for peer_addr in &peer_rpc {
                match crate::network::peer_rpc::query_peer_epoch_boundary_data(
                    peer_addr,
                    target_epoch,
                )
                .await
                {
                    Ok(data) => {
                        info!(
                            "✅ [EPOCH MONITOR] Got boundary from PEER {}: epoch={}, timestamp={}ms, boundary={}",
                            peer_addr, data.epoch, data.timestamp_ms, data.boundary_block
                        );
                        peer_data = Some((
                            data.epoch,
                            data.timestamp_ms,
                            data.boundary_block,
                            data.boundary_gei,
                        ));
                        break;
                    }
                    Err(e) => {
                        debug!(
                            "⚠️ [EPOCH MONITOR] Peer {} failed for epoch {}: {}",
                            peer_addr, target_epoch, e
                        );
                    }
                }
            }
            match peer_data {
                Some(data) => data,
                None => {
                    warn!("⚠️ [EPOCH MONITOR] All peers failed for epoch {}. Stopping at epoch {}.", target_epoch, current_rust_epoch);
                    break;
                }
            }
        } else {
            // LOCAL Go has this epoch data
            match client_arc
                .get_safe_epoch_boundary_data(target_epoch, &peer_rpc)
                .await
            {
                Ok((epoch, timestamp_ms, boundary_block, _validators, _, boundary_gei)) => {
                    (epoch, timestamp_ms, boundary_block, boundary_gei)
                }
                Err(e) => {
                    info!("⏳ [EPOCH MONITOR] Local Go not ready for epoch {}: {}. Trying peer fallback...", target_epoch, e);
                    // Try peer fallback
                    let mut peer_data: Option<(u64, u64, u64, u64)> = None;
                    for peer_addr in &peer_rpc {
                        if let Ok(data) =
                            crate::network::peer_rpc::query_peer_epoch_boundary_data(
                                peer_addr,
                                target_epoch,
                            )
                            .await
                        {
                            peer_data = Some((
                                data.epoch,
                                data.timestamp_ms,
                                data.boundary_block,
                                data.boundary_gei,
                            ));
                            break;
                        }
                    }
                    match peer_data {
                        Some(data) => data,
                        None => {
                            warn!("⚠️ [EPOCH MONITOR] No source for epoch {} boundary. Stopping at epoch {}.", target_epoch, current_rust_epoch);
                            break;
                        }
                    }
                }
            }
        };

        let (_new_epoch, epoch_timestamp_ms, boundary_block, boundary_gei) = boundary_data;

        // First ensure Go has enough blocks for this epoch
        let (go_block, _, _go_ready, _, _) = client_arc
            .get_last_block_number()
            .await
            .unwrap_or((0, 0, false, [0; 32], 0));
        
        if go_block < boundary_block {
            info!("⏳ [EPOCH MONITOR] Go is at block {}, waiting to reach boundary block {} for epoch {}. Triggering active P2P sync for boundary catch-up...", go_block, boundary_block, target_epoch);
            if !peer_rpc.is_empty() {
                match crate::network::peer_rpc::fetch_blocks_from_peer(
                    &peer_rpc,
                    go_block + 1,
                    boundary_block,
                )
                .await
                {
                    Ok(blocks) if !blocks.is_empty() => {
                        info!("🚨 [EPOCH MONITOR] Fetched {} blocks up to boundary block {}. Executing blocks to un-stall epoch transition...", blocks.len(), boundary_block);
                        match client_arc.sync_and_execute_blocks(blocks).await {
                            Ok((synced, last, _gei)) => {
                                info!("✅ [EPOCH MONITOR] Executed {} blocks (last={}) up to boundary.", synced, last);
                                // Query Go for the last handled commit index to align Rust CommitConsumerMonitor
                                match client_arc.get_last_handled_commit_index().await {
                                    Ok((last_commit_index, _, _, _, _, _, _)) => {
                                        info!("✅ [EPOCH MONITOR] Go last handled commit is {}", last_commit_index);
                                        if let Some(node_arc) = crate::node::get_transition_handler_node().await {
                                            match node_arc.try_read() {
                                                Ok(node_guard) => {
                                                    let old_handled = node_guard.commit_consumer_monitor.highest_handled_commit();
                                                    if last_commit_index > old_handled {
                                                        node_guard.commit_consumer_monitor.set_highest_handled_commit(last_commit_index);
                                                        info!("✅ [STALL RECOVERY] Updated CommitConsumerMonitor highest_handled_commit from {} to {}", old_handled, last_commit_index);
                                                    }
                                                }
                                                Err(_) => {
                                                    warn!("⚠️ [EPOCH MONITOR] Failed to lock ConsensusNode to update commit_consumer_monitor");
                                                }
                                            }
                                        }
                                    }
                                    Err(e) => {
                                        warn!("⚠️ [EPOCH MONITOR] Failed to query last handled commit index from Go: {}", e);
                                    }
                                }
                            }
                            Err(e) => {
                                warn!("⚠️ [EPOCH MONITOR] sync_and_execute_blocks failed: {}", e);
                            }
                        }
                    }
                    Ok(_) => {
                        info!("ℹ [EPOCH MONITOR] No boundary blocks available from peers (go_block={}, boundary_block={})", go_block, boundary_block);
                    }
                    Err(e) => {
                        warn!("⚠️ [EPOCH MONITOR] Boundary block fetch failed: {}", e);
                    }
                }
            }
            break; // Stop multi-epoch loop — retry in next monitor cycle
        }

        // Advance Go epoch if needed
        let current_go_epoch = client_arc.get_current_epoch().await.unwrap_or(0);
        if current_go_epoch < target_epoch {
            if let Err(e) = client_arc
                .advance_epoch(
                    target_epoch,
                    epoch_timestamp_ms,
                    boundary_block,
                    boundary_gei,
                )
                .await
            {
                warn!(
                    "⚠️ [EPOCH MONITOR] Failed to advance Go to epoch {}: {}",
                    target_epoch, e
                );
            } else {
                info!("✅ [EPOCH MONITOR] Advanced Go to epoch {}", target_epoch);
            }
        }

        // Try Rust transition via EpochTransitionManager
        let epoch_manager = match crate::node::epoch_transition_manager::get_epoch_manager()
        {
            Some(m) => m,
            None => {
                debug!("⏳ [EPOCH MONITOR] Epoch manager not initialized yet");
                break;
            }
        };

        if let Err(e) = epoch_manager
            .try_start_epoch_transition(target_epoch, "epoch_monitor")
            .await
        {
            warn!(
                "⏳ [EPOCH MONITOR] Cannot start transition to Rust epoch {}: {}",
                target_epoch, e
            );
            // Break the loop! If Rust cannot transition natively yet (maybe it's still syncing),
            // we MUST NOT skip to the next epoch and push Go further ahead.
            break;
        }

        if let Some(node_arc) = crate::node::get_transition_handler_node().await {
            let mut node_guard = node_arc.write().await;

            let synced_global_exec_index = if boundary_gei > 0 {
                boundary_gei
            } else {
                let go_gei = client_arc.get_last_global_exec_index().await.unwrap_or(0);
                go_gei
            };

            match node_guard
                .transition_to_epoch_from_system_tx(
                    target_epoch,
                    epoch_timestamp_ms,
                    boundary_block,
                    synced_global_exec_index,
                    config,
                )
                .await
            {
                Ok(()) => {
                    epoch_manager.complete_epoch_transition(target_epoch).await;
                    current_rust_epoch = target_epoch;
                    info!(
                        "✅ [EPOCH MONITOR] Transitioned to epoch {} ({}/{})",
                        target_epoch,
                        target_epoch - rust_epoch,
                        network_epoch - rust_epoch
                    );
                }
                Err(e) => {
                    epoch_manager.fail_transition(&e.to_string()).await;
                    warn!(
                        "❌ [EPOCH MONITOR] Failed transition to epoch {}: {}. Stopping at epoch {}.",
                        target_epoch, e, current_rust_epoch
                    );
                    break;
                }
            }
        } else {
            epoch_manager.fail_transition("Node not registered").await;
            break;
        }

        // Small delay between epoch transitions to let state settle
        tokio::time::sleep(Duration::from_millis(200)).await;
    }

    *fast_cycles_remaining = fast_cycles_max;
    Ok(())
}
