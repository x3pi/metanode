// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Unified Epoch Monitor
//!
//! A single monitor that handles epoch transitions for BOTH SyncOnly and Validator nodes.
//! This replaces the previous fragmented approach of separate monitors.

mod stall_recovery;
mod sync_only_advance;
mod validator_transition;

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

        let mut stall_last_go_block: u64 = 0;
        let mut stall_count: u32 = 0;

        let mut last_known_epoch: u64 = 0;
        let mut last_known_mode: Option<crate::node::NodeMode> = None;
        let mut last_known_phase: Option<consensus_core::coordination_hub::NodeConsensusPhase> = None;

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
            // DEADLOCK PREVENTION: Use non-blocking try_lock with a fallback to the last known
            // state. During epoch transition, transition_to_epoch_from_system_tx locks the node
            // and calls poll_go_until_synced which awaits. If epoch_monitor calls lock().await,
            // it deadlocks, preventing STALL RECOVERY from executing to help Go catch up.
            let lock_res = if let Some(node_arc) = crate::node::get_transition_handler_node().await {
                match node_arc.try_read() {
                    Ok(node_guard) => {
                        last_known_epoch = node_guard.current_epoch;
                        last_known_mode = Some(node_guard.node_mode.clone());
                        let phase = node_guard.coordination_hub.get_phase();
                        last_known_phase = Some(phase);
                        Some((node_guard.current_epoch, node_guard.node_mode.clone(), phase))
                    }
                    Err(_) => {
                        if let Some(ref mode) = last_known_mode {
                            let phase = last_known_phase.unwrap_or(consensus_core::coordination_hub::NodeConsensusPhase::Initializing);
                            debug!("⏳ [EPOCH MONITOR] Node registry lock is busy (transition in progress). Falling back to last known: epoch={}, mode={:?}, phase={:?}", last_known_epoch, mode, phase);
                            Some((last_known_epoch, mode.clone(), phase))
                        } else {
                            None
                        }
                    }
                }
            } else {
                None
            };

            let (rust_epoch, current_mode, current_phase) = match lock_res {
                Some(res) => res,
                None => {
                    debug!("⚠️ [EPOCH MONITOR] Node not registered yet or lock busy with no last known state, waiting...");
                    continue;
                }
            };

            // 4. Stall check for Validator mode
            if matches!(current_mode, crate::node::NodeMode::Validator)
                && !config_clone.peer_rpc_addresses.is_empty()
                && current_phase == consensus_core::coordination_hub::NodeConsensusPhase::Healthy
            {
                // Get current Go block number
                let go_block = match client_arc.get_last_block_number().await {
                    Ok((b, _, _, _, _)) => b,
                    Err(_) => {
                        continue;
                    }
                };

                if let Err(e) = stall_recovery::recover_from_block_stall(
                    &client_arc,
                    &config_clone,
                    peer_best_block,
                    go_block,
                    &mut stall_last_go_block,
                    &mut stall_count,
                    &mut fast_cycles_remaining,
                    fast_cycles_max,
                )
                .await
                {
                    warn!("⚠️ [EPOCH MONITOR] Stall recovery failure: {}", e);
                }
            }

            // 5. Check if transition needed
            if network_epoch <= rust_epoch {
                continue;
            }

            // 6. Handle transition depending on NodeMode
            match current_mode {
                crate::node::NodeMode::SyncOnly => {
                    if let Err(e) = sync_only_advance::advance_sync_only_epoch(
                        &client_arc,
                        &config_clone,
                        local_go_epoch,
                        network_epoch,
                    )
                    .await
                    {
                        warn!("⚠️ [EPOCH MONITOR] SyncOnly epoch advancement failed: {}", e);
                    }
                }
                crate::node::NodeMode::Validator => {
                    if current_phase == consensus_core::coordination_hub::NodeConsensusPhase::Healthy {
                        if let Err(e) = validator_transition::validator_multi_epoch_transition(
                            &client_arc,
                            &config_clone,
                            rust_epoch,
                            network_epoch,
                            local_go_epoch,
                            &mut fast_cycles_remaining,
                            fast_cycles_max,
                        )
                        .await
                        {
                            warn!("⚠️ [EPOCH MONITOR] Validator transition failed: {}", e);
                        }
                    } else {
                        debug!("⏳ [EPOCH MONITOR] Skipping validator transition because node phase is {:?}", current_phase);
                    }
                }
            }
        }
    });

    Ok(Some(handle))
}

/// Stop the epoch monitor task
pub async fn stop_epoch_monitor(handle: Option<JoinHandle<()>>) {
    if let Some(h) = handle {
        h.abort();
        let _ = h.await;
        info!("🛑 [EPOCH MONITOR] Stopped unified epoch monitor");
    }
}
