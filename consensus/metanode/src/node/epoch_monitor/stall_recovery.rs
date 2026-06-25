// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use anyhow::Result;
use std::sync::Arc;
use tracing::{debug, info, warn};

const STALL_THRESHOLD: u32 = 3;       // 3 consecutive stalls → trigger recovery (30s at 10s poll)
const STALL_MIN_GAP: u64 = 2;         // Minimum block gap to consider "stalled"
const STALL_FETCH_BATCH: u64 = 500;   // Max blocks to fetch per recovery cycle

/// Verify and recover if Validator's Go blocks are stalled (not advancing)
pub(super) async fn recover_from_block_stall(
    client_arc: &Arc<ExecutorClient>,
    config: &NodeConfig,
    peer_best_block: u64,
    go_block: u64,
    stall_last_go_block: &mut u64,
    stall_count: &mut u32,
    fast_cycles_remaining: &mut u32,
    fast_cycles_max: u32,
) -> Result<()> {
    // Check if Go blocks are stalled: not advancing AND peers are ahead
    if peer_best_block > go_block + STALL_MIN_GAP {
        if go_block == *stall_last_go_block && go_block > 0 {
            *stall_count += 1;
        } else if go_block == 0 && *stall_last_go_block == 0 {
            // Fresh node at block 0 — also counts as stalled
            *stall_count += 1;
        } else {
            // Block advanced — reset stall counter
            *stall_count = 0;
        }
        *stall_last_go_block = go_block;

        if *stall_count >= STALL_THRESHOLD {
            let fetch_to = std::cmp::min(
                go_block + STALL_FETCH_BATCH,
                peer_best_block,
            );
            warn!(
                "🚨 [STALL RECOVERY] Validator blocks stalled! go_block={}, peer_block={}, stall_count={}. Fetching blocks {}→{} from peers...",
                go_block, peer_best_block, *stall_count, go_block + 1, fetch_to
            );

            match crate::network::peer_rpc::fetch_blocks_from_peer(
                &config.peer_rpc_addresses,
                go_block + 1,
                fetch_to,
            )
            .await
            {
                Ok(blocks) if !blocks.is_empty() => {
                    let count = blocks.len();
                    match client_arc.sync_and_execute_blocks(blocks).await {
                        Ok((synced, last, _gei)) => {
                            info!(
                                "✅ [STALL RECOVERY] Executed {} blocks (last={}). CommitSyncer should resume DAG catch-up.",
                                synced, last
                            );
                            // Query Go for the last handled commit index to align Rust CommitConsumerMonitor
                            match client_arc.get_last_handled_commit_index().await {
                                Ok((last_commit_index, _, _, _, _, _, _)) => {
                                    info!("✅ [STALL RECOVERY] Go last handled commit is {}", last_commit_index);
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
                                                warn!("⚠️ [STALL RECOVERY] Failed to lock ConsensusNode to update commit_consumer_monitor");
                                            }
                                        }
                                    }
                                }
                                Err(e) => {
                                    warn!("⚠️ [STALL RECOVERY] Failed to query last handled commit index from Go: {}", e);
                                }
                            }
                        }
                        Err(e) => {
                            warn!(
                                "⚠️ [STALL RECOVERY] sync_and_execute_blocks failed: {}",
                                e
                            );
                        }
                    }
                    let _ = count;

                    // Switch to fast polling to detect rapid recovery
                    *fast_cycles_remaining = fast_cycles_max;
                }
                Ok(_) => {
                    info!(
                        "ℹ [STALL RECOVERY] No blocks available from peers (go_block={}, peer_block={})",
                        go_block, peer_best_block
                    );
                }
                Err(e) => {
                    warn!(
                        "⚠️ [STALL RECOVERY] Block fetch failed: {}",
                        e
                    );
                }
            }

            // Reset stall counter after recovery attempt (will re-trigger if still stalled)
            *stall_count = 0;
        } else {
            debug!(
                "⏳ [STALL DETECT] go_block={}, peer_block={}, stall_count={}/{}",
                go_block, peer_best_block, *stall_count, STALL_THRESHOLD
            );
        }
    } else {
        // No stall condition — blocks are advancing or no gap
        if *stall_count > 0 {
            info!(
                "✅ [STALL CLEARED] go_block={}, peer_block={} (gap < {}). Resuming normal monitoring.",
                go_block, peer_best_block, STALL_MIN_GAP
            );
        }
        *stall_count = 0;
        *stall_last_go_block = go_block;
    }
    Ok(())
}
