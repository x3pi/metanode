// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use crate::node::ConsensusNode;
use std::sync::Arc;
use tracing::{info, warn};

impl ConsensusNode {
    /// Determines the effective last global execution index and commit hash from local Go, peers, and persisted state.
    pub(crate) async fn calculate_last_global_exec_index(
        config: &NodeConfig,
        executor_client: &Arc<ExecutorClient>,
        best_socket: &str,
        peer_last_block: u64,
        current_epoch: u64,
    ) -> (u64, u64, [u8; 32], Option<u32>, u64) {
        if !config.executor_read_enabled {
            return (0, 0, [0; 32], None, 0);
        }

        let (local_go_block, local_go_gei, _go_ready, last_executed_commit_hash) = loop {
            match executor_client.get_last_block_number().await {
                Ok((block, gei, true, hash, _)) => break (block, gei, true, hash),
                Ok((block, gei, false, _hash, _)) => {
                    warn!(
                        "⏳ [STARTUP] Go Master not ready (block={}, gei={}). Retrying in 1s...",
                        block, gei
                    );
                    tokio::time::sleep(tokio::time::Duration::from_secs(1)).await;
                }
                Err(e) => {
                    warn!(
                        "⚠️ [STARTUP] Failed to get last block from Go: {}. Using defaults.",
                        e
                    );
                    break (0, 0, false, [0; 32]);
                }
            }
        };

        let (last_handled_commit_index, last_block_timestamp_ms) = match executor_client.get_last_handled_commit_index().await {
            Ok((commit_index, _, _, go_epoch, _, ts, state_root)) => {
                let state_root_hex = hex::encode(&state_root);
                tracing::info!(
                    "🔑 [GO-AUTH GEI] Post-init query: state_root=0x{}, go_epoch={}, rust_epoch={}",
                    state_root_hex, go_epoch, current_epoch
                );
                if go_epoch != current_epoch && commit_index > 0 {
                    tracing::error!(
                        "🚨 [STARTUP] EPOCH MISMATCH: Go epoch={} != Rust epoch={}! Go commit_index={} is from wrong epoch. Forcing last_handled_commit_index=None.",
                        go_epoch, current_epoch, commit_index
                    );
                    (None, ts)
                } else {
                    (Some(commit_index), ts)
                }
            },
            Err(e) => {
                warn!("⚠️ [STARTUP] Failed to get last_handled_commit_index from Go: {}", e);
                (None, 0)
            }
        };

        let storage_path = &config.storage_path;

        let (persisted_index, persisted_commit) =
            crate::node::executor_client::load_persisted_last_index(storage_path).unwrap_or((0, 0));

        let peer_last_block =
            if !best_socket.is_empty() && peer_last_block > 0 {
                peer_last_block
            } else {
                0
            };

        if peer_last_block > 0 {
            info!(
                "📊 [STARTUP] Sync Check: LocalGoBlock={}, PeerBlock={}, PersistedGEI=({}, commit={}) (from {})",
                local_go_block, peer_last_block, persisted_index, persisted_commit, best_socket
            );

            let sources_match =
                local_go_block == peer_last_block || local_go_block.abs_diff(peer_last_block) <= 5;
            if !sources_match {
                warn!("⚠️ [STARTUP] INDEX DISCREPANCY DETECTED:");
                warn!(
                    "   LocalGoBlock={}, PeerBlock={}, PersistedGEI={}, LocalGEI={}",
                    local_go_block, peer_last_block, persisted_index, local_go_gei
                );
                warn!("   This may indicate network partition or stale data.");
            }

            if local_go_block > peer_last_block + 5 {
                warn!("🚨 [STARTUP] STALE CHAIN DETECTED: Local ({}) is ahead of Peer ({})! Forcing resync from Peer.", 
                       local_go_block, peer_last_block);
                // In recovery we just use the local GEI anyway because Go Master blocks handles actual rollback if needed
                (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
            } else if local_go_block < peer_last_block.saturating_sub(5) {
                let lag = peer_last_block - local_go_block;
                info!(
                    "ℹ️ [STARTUP] Local Go Master ({}) is behind Peer ({}) by {} blocks. Using Local {} to trigger recovery/backfill.",
                    local_go_block, peer_last_block, lag, local_go_block
                );
                // Flag as lagging if behind by more than 50 blocks
                (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
            } else {
                info!(
                    "✅ [STARTUP] Local and Peer are in sync (LocalBlock={}, PeerBlock={}). Using Local Go GEI: {} as authoritative.",
                    local_go_block, peer_last_block, local_go_gei
                );
                (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
            }
        } else {
            if persisted_index > local_go_gei {
                warn!("⚠️ [STARTUP] Persisted Index (GEI) {} > Local Go GEI {}. Go is behind (possible rollback/crash). Using Local Go GEI {} to force resync/replay.", 
                    persisted_index, local_go_gei, local_go_gei);
            }
            info!(
                "📊 [STARTUP] No peer reference, using Local Go Last GEI: {} (Block: {})",
                local_go_gei, local_go_block
            );
            (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
        }
    }
}
