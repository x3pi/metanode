// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use anyhow::Result;
use std::sync::Arc;
use tracing::{debug, info, warn};

/// Sequential Go Master epoch advancement for SyncOnly mode nodes
pub(super) async fn advance_sync_only_epoch(
    client_arc: &Arc<ExecutorClient>,
    config: &NodeConfig,
    local_go_epoch: u64,
    network_epoch: u64,
) -> Result<()> {
    // Skip if Go is already caught up
    if local_go_epoch >= network_epoch {
        return Ok(());
    }
    info!(
        "🔄 [EPOCH MONITOR] SyncOnly mode: advancing Go epoch {} → {} (fetching blocks + advance_epoch)",
        local_go_epoch, network_epoch
    );

    // Fetch boundary data from peers
    let peer_rpc = config.peer_rpc_addresses.clone();
    if peer_rpc.is_empty() {
        warn!(
            "[EPOCH MONITOR] SyncOnly: no peer_rpc_addresses, cannot advance Go epoch"
        );
        return Ok(());
    }

    // Advance Go through each intermediate epoch sequentially
    let mut current_go_epoch = local_go_epoch;
    for target_epoch in (local_go_epoch + 1)..=network_epoch {
        // Get boundary data from peer for this epoch
        let mut boundary_found = false;
        for peer_addr in &peer_rpc {
            match crate::network::peer_rpc::query_peer_epoch_boundary_data(
                peer_addr,
                target_epoch,
            )
            .await
            {
                Ok(data) => {
                    info!(
                        "📦 [EPOCH MONITOR] SyncOnly: epoch {} boundary={}, timestamp={}ms (from {})",
                        target_epoch, data.boundary_block, data.timestamp_ms, peer_addr
                    );

                    // Wait for sync_loop to fetch blocks up to boundary
                    let (go_block, _, _go_ready, _, _) = client_arc
                        .get_last_block_number()
                        .await
                        .unwrap_or((0, 0, false, [0; 32], 0));
                    if go_block < data.boundary_block {
                        info!("⏳ [EPOCH MONITOR] SyncOnly: Go block {} < boundary {}. Waiting for sync_loop to catch up.", go_block, data.boundary_block);
                        break;
                    }

                    // Advance Go epoch
                    if current_go_epoch < target_epoch {
                        match client_arc
                            .advance_epoch(
                                target_epoch,
                                data.timestamp_ms,
                                data.boundary_block,
                                data.boundary_gei,
                            )
                            .await
                        {
                            Ok(_) => {
                                info!(
                                    "✅ [EPOCH MONITOR] SyncOnly: advanced Go to epoch {} (boundary={})",
                                    target_epoch, data.boundary_block
                                );
                                current_go_epoch = target_epoch;
                            }
                            Err(e) => {
                                warn!(
                                    "⚠️ [EPOCH MONITOR] SyncOnly: failed to advance Go to epoch {}: {}",
                                    target_epoch, e
                                );
                                break;
                            }
                        }
                    }

                    boundary_found = true;
                    break;
                }
                Err(e) => {
                    debug!(
                        "[EPOCH MONITOR] SyncOnly: peer {} failed for epoch {}: {}",
                        peer_addr, target_epoch, e
                    );
                }
            }
        }

        if !boundary_found {
            warn!(
                "⚠️ [EPOCH MONITOR] SyncOnly: no peer had boundary for epoch {}. Stopping at epoch {}.",
                target_epoch, current_go_epoch
            );
            break;
        }
    }
    Ok(())
}
