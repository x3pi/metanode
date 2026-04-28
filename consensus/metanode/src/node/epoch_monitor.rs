// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Unified Epoch Monitor
//!
//! A single monitor that handles epoch transitions for BOTH SyncOnly and Validator nodes.
//! This replaces the previous fragmented approach of separate monitors.
//!
//! ## Design Principles
//! 1. **Single Source of Truth**: Go layer epoch is authoritative
//! 2. **Always Running**: Monitor never exits - runs continuously for all node modes
//! 3. **Fork-Safe**: Uses `boundary_block` from `get_epoch_boundary_data()`
//! 4. **Unified Logic**: Same code path for SyncOnly and Validator nodes

use crate::config::NodeConfig;
use anyhow::Result;
use std::sync::Arc;
use std::time::Duration;
use tokio::task::JoinHandle;
use tracing::{debug, info, warn};

/// Start the unified epoch monitor for ALL node types (SyncOnly and Validator)
///
/// This monitor:
/// 1. Polls Go epoch every N seconds
/// 2. Detects when Rust epoch falls behind Go epoch
/// 3. Fetches epoch boundary data (fork-safe)
/// 4. Triggers appropriate transition (SyncOnly→Validator or epoch update)
///
/// IMPORTANT: This monitor NEVER exits - it runs continuously for the lifetime of the node.
/// This prevents the bug where Validators get stuck when they miss EndOfEpoch transactions.
pub fn start_unified_epoch_monitor(
    executor_client: &Option<Arc<crate::node::executor_client::ExecutorClient>>,
    config: &NodeConfig,
) -> Result<Option<JoinHandle<()>>> {
    let client_arc = match executor_client {
        Some(client) => client.clone(),
        None => {
            warn!("⚠️ [EPOCH MONITOR] Cannot start - no executor client");
            return Ok(None);
        }
    };

    let node_id = config.node_id;
    let config_clone = config.clone();
    // Default poll interval: configurable, default 10 seconds
    let poll_interval_secs = config.epoch_monitor_poll_interval_secs.unwrap_or(10);

    info!(
        "🔄 [EPOCH MONITOR] Starting unified epoch monitor for node-{} (poll_interval={}s)",
        node_id, poll_interval_secs
    );

    let handle = tokio::spawn(async move {
        // T3-4: Adaptive polling state
        // - Normal: poll_interval_secs (default 10s) — low IPC overhead
        // - After epoch gap detected: 1s for 30 cycles — fast transition detection
        let normal_interval = Duration::from_secs(poll_interval_secs);
        let fast_interval = Duration::from_secs(1);
        let fast_cycles_max: u32 = 30; // Stay fast for 30 cycles (30s at 1s interval)
        let mut fast_cycles_remaining: u32 = 0;

        // ═══════════════════════════════════════════════════════════════
        // BLOCK STALL DETECTOR: Track Go block progress for Validators.
        // If Go blocks stop advancing while peers are ahead, fetch blocks
        // via P2P to un-stall CommitSyncer (which needs highest_handled_index
        // to advance for DAG fast-forward/catch-up).
        // ═══════════════════════════════════════════════════════════════
        let mut stall_last_go_block: u64 = 0;
        let mut stall_count: u32 = 0;
        const STALL_THRESHOLD: u32 = 3;       // 3 consecutive stalls → trigger recovery (30s at 10s poll)
        const STALL_MIN_GAP: u64 = 10;        // Minimum block gap to consider "stalled"
        const STALL_FETCH_BATCH: u64 = 500;   // Max blocks to fetch per recovery cycle

        loop {
            // T3-4: Use adaptive interval
            let current_interval = if fast_cycles_remaining > 0 {
                fast_cycles_remaining -= 1;
                fast_interval
            } else {
                normal_interval
            };
            tokio::time::sleep(current_interval).await;

            // 1. Get LOCAL Go epoch (may be stale for late-joiners!)
            let local_go_epoch = match client_arc.get_current_epoch().await {
                Ok(epoch) => epoch,
                Err(e) => {
                    debug!("⚠️ [EPOCH MONITOR] Failed to get local Go epoch: {}", e);
                    continue;
                }
            };

            // 2. Get NETWORK epoch from peers (critical for late-joiners!)
            // Use peer_rpc_addresses for WAN-based discovery
            // Also capture peer's best block number for stall detection
            let (network_epoch, peer_best_block) = {
                let peer_rpc = config_clone.peer_rpc_addresses.clone();

                if !peer_rpc.is_empty() {
                    // WAN-based discovery (TCP) - recommended for cross-node sync
                    match crate::network::peer_rpc::query_peer_epochs_network(&peer_rpc).await {
                        Ok((epoch, block, peer, _global_exec_index)) => {
                            if epoch > local_go_epoch {
                                info!(
                                    "🌐 [EPOCH MONITOR] Network epoch {} from peer {} is AHEAD of local Go epoch {}",
                                    epoch, peer, local_go_epoch
                                );
                            }
                            (epoch, block)
                        }
                        Err(_) => (local_go_epoch, 0u64), // Fallback to local
                    }
                } else {
                    // No WAN peers configured - use local Go epoch
                    (local_go_epoch, 0u64)
                }
            };

            // 3. Get current Rust epoch from node
            let (rust_epoch, current_mode) =
                if let Some(node_arc) = crate::node::get_transition_handler_node().await {
                    let node_guard = node_arc.lock().await;
                    (node_guard.current_epoch, node_guard.node_mode.clone())
                } else {
                    debug!("⚠️ [EPOCH MONITOR] Node not registered yet, waiting...");
                    continue;
                };

            // ═══════════════════════════════════════════════════════════════
            // BLOCK SYNC: REMOVED — sync_loop is the sole owner of block sync.
            // Previously this section fetched blocks and called sync_blocks(),
            // but it RACED with sync_loop doing the same thing → both calling
            // sync_blocks() on the same UDS socket simultaneously → broken pipe
            // → sync stops permanently. Block sync is now exclusively handled
            // by sync_loop.rs with turbo mode for fast catch-up.
            // ═══════════════════════════════════════════════════════════════

            // 4. Check if transition needed (NETWORK epoch ahead of Rust)
            // Use network_epoch instead of local_go_epoch!
            if network_epoch <= rust_epoch {
                // ═══════════════════════════════════════════════════════════
                // SAME-EPOCH: No epoch transition needed.
                //
                // DESIGN: No mid-epoch validator promotion/demotion.
                // Validator role changes ONLY happen during epoch transitions.
                //
                // HOWEVER: If this Validator's Go blocks are stalled (not
                // advancing), we need to intervene by fetching blocks from
                // peers via P2P. This un-stalls CommitSyncer which needs
                // Go's highest_handled_index to advance for DAG catch-up.
                // ═══════════════════════════════════════════════════════════
                if matches!(current_mode, crate::node::NodeMode::Validator)
                    && !config_clone.peer_rpc_addresses.is_empty()
                {
                    // Get current Go block number
                    let go_block = match client_arc.get_last_block_number().await {
                        Ok((b, _, _, _, _)) => b,
                        Err(_) => {
                            continue;
                        }
                    };

                    // Check if Go blocks are stalled: not advancing AND peers are ahead
                    if peer_best_block > go_block + STALL_MIN_GAP {
                        if go_block == stall_last_go_block && go_block > 0 {
                            stall_count += 1;
                        } else if go_block == 0 && stall_last_go_block == 0 {
                            // Fresh node at block 0 — also counts as stalled
                            stall_count += 1;
                        } else {
                            // Block advanced — reset stall counter
                            stall_count = 0;
                        }
                        stall_last_go_block = go_block;

                        if stall_count >= STALL_THRESHOLD {
                            let fetch_to = std::cmp::min(
                                go_block + STALL_FETCH_BATCH,
                                peer_best_block,
                            );
                            warn!(
                                "🚨 [STALL RECOVERY] Validator blocks stalled! go_block={}, peer_block={}, stall_count={}. Fetching blocks {}→{} from peers...",
                                go_block, peer_best_block, stall_count, go_block + 1, fetch_to
                            );

                            match crate::network::peer_rpc::fetch_blocks_from_peer(
                                &config_clone.peer_rpc_addresses,
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
                                    fast_cycles_remaining = fast_cycles_max;
                                }
                                Ok(_) => {
                                    info!(
                                        "ℹ️ [STALL RECOVERY] No blocks available from peers (go_block={}, peer_block={})",
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
                            stall_count = 0;
                        } else {
                            debug!(
                                "⏳ [STALL DETECT] go_block={}, peer_block={}, stall_count={}/{}",
                                go_block, peer_best_block, stall_count, STALL_THRESHOLD
                            );
                        }
                    } else {
                        // No stall condition — blocks are advancing or no gap
                        if stall_count > 0 {
                            info!(
                                "✅ [STALL CLEARED] go_block={}, peer_block={} (gap < {}). Resuming normal monitoring.",
                                go_block, peer_best_block, STALL_MIN_GAP
                            );
                        }
                        stall_count = 0;
                        stall_last_go_block = go_block;
                    }
                }

                continue;
            }

            // ═══════════════════════════════════════════════════════════════
            // SyncOnly nodes: advance Go Master epoch by fetching blocks + advance_epoch
            // Previously this was a complete `continue` which left Go permanently behind.
            // SyncOnly nodes don't run full transitions, but Go must advance epoch
            // to serve blocks at the correct epoch to other nodes and to itself.
            // ═══════════════════════════════════════════════════════════════
            if matches!(current_mode, crate::node::NodeMode::SyncOnly) {
                // Skip if Go is already caught up
                if local_go_epoch >= network_epoch {
                    continue;
                }
                info!(
                    "🔄 [EPOCH MONITOR] SyncOnly mode: advancing Go epoch {} → {} (fetching blocks + advance_epoch)",
                    local_go_epoch, network_epoch
                );

                // Fetch boundary data from peers
                let peer_rpc = config_clone.peer_rpc_addresses.clone();
                if peer_rpc.is_empty() {
                    warn!(
                        "[EPOCH MONITOR] SyncOnly: no peer_rpc_addresses, cannot advance Go epoch"
                    );
                    continue;
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

                                // Fetch blocks up to boundary from peers
                                let (go_block, _, _go_ready, _, _) = client_arc
                                    .get_last_block_number()
                                    .await
                                    .unwrap_or((0, 0, false, [0; 32], 0));
                                if go_block < data.boundary_block {
                                    // Fetch missing blocks
                                    match crate::network::peer_rpc::fetch_blocks_from_peer(
                                        &peer_rpc,
                                        go_block + 1,
                                        data.boundary_block,
                                    )
                                    .await
                                    {
                                        Ok(blocks) if !blocks.is_empty() => {
                                            match client_arc.sync_blocks(blocks).await {
                                                Ok((synced, last)) => {
                                                    info!(
                                                        "✅ [EPOCH MONITOR] SyncOnly: synced {} blocks to Go (last: {})",
                                                        synced, last
                                                    );
                                                }
                                                Err(e) => {
                                                    warn!("⚠️ [EPOCH MONITOR] SyncOnly: sync_blocks failed: {}", e);
                                                }
                                            }
                                        }
                                        _ => {}
                                    }
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

                continue;
            }

            let epoch_gap = network_epoch - rust_epoch;
            info!(
                "🔄 [EPOCH MONITOR] Epoch gap detected: Rust={} Network={} (gap={})",
                rust_epoch, network_epoch, epoch_gap
            );

            // T3-4: Switch to fast polling during epoch transitions
            fast_cycles_remaining = fast_cycles_max;

            // ═══════════════════════════════════════════════════════════════
            // MULTI-EPOCH CATCH-UP: Step through each intermediate epoch
            // Transition N→N+2 requires going through N→N+1→N+2 because
            // each epoch needs its own boundary data and committee setup.
            // ═══════════════════════════════════════════════════════════════
            let local_executor_client = client_arc.clone();
            let peer_rpc = config_clone.peer_rpc_addresses.clone();

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
                    match local_executor_client
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

                let (new_epoch, epoch_timestamp_ms, boundary_block, boundary_gei) = boundary_data;

                // First ensure Go has enough blocks for this epoch
                let (go_block, _, _go_ready, _, _) = client_arc
                    .get_last_block_number()
                    .await
                    .unwrap_or((0, 0, false, [0; 32], 0));
                if go_block < boundary_block && !peer_rpc.is_empty() {
                    match crate::network::peer_rpc::fetch_blocks_from_peer(
                        &peer_rpc,
                        go_block + 1,
                        boundary_block,
                    )
                    .await
                    {
                        Ok(blocks) if !blocks.is_empty() => {
                            let count = blocks.len();
                            // Phase 1 fix: Use sync_and_execute_blocks instead of sync_blocks.
                            // This executes blocks through NOMT, preventing GEI inflation.
                            // GEI now always reflects actually-executed state → no fork.
                            match client_arc.sync_and_execute_blocks(blocks).await {
                                Ok((synced, last, _gei)) => {
                                    info!("✅ [EPOCH MONITOR] Executed {} blocks to Go for epoch {} boundary (last: {})", synced, target_epoch, last);
                                }
                                Err(e) => {
                                    tracing::error!("🚨 [EPOCH MONITOR] sync_and_execute_blocks failed: {}. Will retry next epoch monitor cycle. NO store-only fallback.", e);
                                    break; // Stop multi-epoch loop — retry in next monitor cycle
                                }
                            }
                            let _ = count;
                        }
                        _ => {}
                    }
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
                    .try_start_epoch_transition(new_epoch, "epoch_monitor")
                    .await
                {
                    warn!(
                        "⏳ [EPOCH MONITOR] Cannot start transition to Rust epoch {}: {}",
                        new_epoch, e
                    );
                    // Break the loop! If Rust cannot transition natively yet (maybe it's still syncing),
                    // we MUST NOT skip to the next epoch and push Go further ahead.
                    break;
                }

                if let Some(node_arc) = crate::node::get_transition_handler_node().await {
                    let mut node_guard = node_arc.lock().await;

                    let synced_global_exec_index = if boundary_gei > 0 {
                        boundary_gei
                    } else {
                        let go_gei = client_arc.get_last_global_exec_index().await.unwrap_or(0);
                        std::cmp::max(go_gei, boundary_block)
                    };

                    match node_guard
                        .transition_to_epoch_from_system_tx(
                            new_epoch,
                            epoch_timestamp_ms,
                            boundary_block,
                            synced_global_exec_index,
                            &config_clone,
                        )
                        .await
                    {
                        Ok(()) => {
                            epoch_manager.complete_epoch_transition(new_epoch).await;
                            current_rust_epoch = new_epoch;
                            info!(
                                "✅ [EPOCH MONITOR] Transitioned to epoch {} ({}/{})",
                                new_epoch,
                                new_epoch - rust_epoch,
                                epoch_gap
                            );
                        }
                        Err(e) => {
                            epoch_manager.fail_transition(&e.to_string()).await;
                            warn!(
                                "❌ [EPOCH MONITOR] Failed transition to epoch {}: {}. Stopping at epoch {}.",
                                new_epoch, e, current_rust_epoch
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

            // CRITICAL: Do NOT exit the loop! Monitor continues running
            // This is the key fix - monitor runs for the entire node lifetime
        }
    });

    Ok(Some(handle))
}

/// Stop the epoch monitor task
#[allow(dead_code)]
pub async fn stop_epoch_monitor(handle: Option<JoinHandle<()>>) {
    if let Some(h) = handle {
        h.abort();
        info!("🛑 [EPOCH MONITOR] Stopped unified epoch monitor");
    }
}
