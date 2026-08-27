// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::node::ConsensusNode;
use crate::node::executor_client::ExecutorClient;
use std::sync::atomic::AtomicBool;
use std::sync::Arc;

impl ConsensusNode {

    /// Runtime Fork Guard — PERMANENT background block hash verification (Layer 6).
    pub(crate) async fn runtime_fork_guard(
        client: Arc<ExecutorClient>,
        peers: Vec<String>,
        start_block: u64,
        is_terminally_failed: Arc<AtomicBool>,
    ) {
        const CHECK_INTERVAL: u64 = 10;
        let mut next_check_block = start_block + CHECK_INTERVAL;
        let mut consecutive_failures: u32 = 0;
        const MAX_CONSECUTIVE_FAILURES: u32 = 10;

        tracing::info!(
            "🛡️ [LAYER-6] Runtime Fork Guard started — PERMANENT monitoring from block {} (every {} blocks)",
            start_block, CHECK_INTERVAL
        );

        loop {
            loop {
                match client.get_last_block_number().await {
                    Ok((current, _, true, _, _)) if current >= next_check_block => break,
                    _ => {
                        tokio::time::sleep(std::time::Duration::from_secs(2)).await;
                    }
                }
            }

            match crate::network::peer_rpc::fetch_blocks_from_peer(
                &peers, next_check_block, next_check_block,
            ).await {
                Ok(peer_blocks) if !peer_blocks.is_empty() => {
                    match client.get_blocks_range(next_check_block, next_check_block).await {
                        Ok(local_blocks) if !local_blocks.is_empty() => {
                            let local_raw = &local_blocks[0].raw_block_bytes;
                            let peer_raw = &peer_blocks[0].raw_block_bytes;
                            let local_state_root = &local_blocks[0].state_root;
                            let peer_state_root = &peer_blocks[0].state_root;
                            if local_raw == peer_raw && local_state_root == peer_state_root {
                                if next_check_block % 100 == 0 {
                                    tracing::info!(
                                        "✅ [LAYER-6] Block #{} verified ({} bytes match, state_root match)",
                                        next_check_block, local_raw.len()
                                    );
                                }
                                consecutive_failures = 0;
                            } else {
                                tracing::error!(
                                    "🚨 [LAYER-6] Block #{} MISMATCH DETECTED! \
                                     local_bytes={} peer_bytes={}, local_root=0x{} peer_root=0x{}. \
                                     ENTERING PENDING MODE — will re-verify 3 times before action.",
                                    next_check_block,
                                    local_raw.len(), peer_raw.len(),
                                    hex::encode(local_state_root), hex::encode(peer_state_root)
                                );

                                let mut confirmed_mismatch = true;
                                for retry in 1..=3 {
                                    tracing::warn!(
                                        "⏳ [LAYER-6] Re-verify attempt {}/3 for block #{} in 5s...",
                                        retry, next_check_block
                                    );
                                    tokio::time::sleep(std::time::Duration::from_secs(5)).await;

                                    match crate::network::peer_rpc::fetch_blocks_from_peer(
                                        &peers, next_check_block, next_check_block,
                                    ).await {
                                        Ok(retry_peer_blocks) if !retry_peer_blocks.is_empty() => {
                                            match client.get_blocks_range(next_check_block, next_check_block).await {
                                                Ok(retry_local) if !retry_local.is_empty() => {
                                                    if retry_local[0].raw_block_bytes == retry_peer_blocks[0].raw_block_bytes
                                                        && retry_local[0].state_root == retry_peer_blocks[0].state_root
                                                    {
                                                        tracing::info!(
                                                            "✅ [LAYER-6] Re-verify {}/3: Block #{} NOW MATCHES! \
                                                             Was transient pipeline lag. Resuming.",
                                                            retry, next_check_block
                                                        );
                                                        confirmed_mismatch = false;
                                                        break;
                                                    } else {
                                                        tracing::error!(
                                                            "🚨 [LAYER-6] Re-verify {}/3: Block #{} STILL MISMATCHES!",
                                                            retry, next_check_block
                                                        );
                                                    }
                                                }
                                                _ => {
                                                    tracing::warn!(
                                                        "⚠️ [LAYER-6] Re-verify {}/3: Could not fetch local block #{}",
                                                        retry, next_check_block
                                                    );
                                                }
                                            }
                                        }
                                        _ => {
                                            tracing::warn!(
                                                "⚠️ [LAYER-6] Re-verify {}/3: Could not reach peer for block #{}",
                                                retry, next_check_block
                                            );
                                        }
                                    }
                                }

                                if confirmed_mismatch {
                                    tracing::error!(
                                        "🚨🚨🚨 [LAYER-6] CONFIRMED FORK at block #{}! \
                                         3/3 re-verifications failed. \
                                         Setting is_terminally_failed and halting process.",
                                        next_check_block
                                    );
                                    is_terminally_failed.store(true, std::sync::atomic::Ordering::SeqCst);
                                    tracing::error!(
                                        "🛑 [LAYER-6] Calling std::process::exit(1) to halt node. \
                                         FFI restart loop will trigger STARTUP-SYNC resync."
                                    );
                                    std::process::exit(1);
                                } else {
                                    consecutive_failures = 0;
                                }
                            }
                        }
                        _ => {
                            consecutive_failures += 1;
                        }
                    }
                }
                _ => {
                    consecutive_failures += 1;
                }
            }

            if consecutive_failures >= MAX_CONSECUTIVE_FAILURES {
                tracing::warn!(
                    "⚠️ [LAYER-6] {} consecutive peer failures. Backing off to 60s interval.",
                    consecutive_failures
                );
                tokio::time::sleep(std::time::Duration::from_secs(60)).await;
                consecutive_failures = 0;
            }

            next_check_block += CHECK_INTERVAL;
        }
    }
}
