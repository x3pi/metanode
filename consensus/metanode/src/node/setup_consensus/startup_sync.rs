// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::node::ConsensusNode;
use crate::config::NodeConfig;
use crate::node::StorageSetup;
use crate::node::executor_client::ExecutorClient;
use consensus_core::CommitConsumerArgs;
use std::sync::Arc;
use anyhow::Result;
use hex;

impl ConsensusNode {
    pub(super) async fn perform_startup_sync(
        config: &NodeConfig,
        storage: &mut StorageSetup,
        coordination_hub: &consensus_core::coordination_hub::ConsensusCoordinationHub,
        commit_consumer: &mut CommitConsumerArgs,
        commit_processor: &mut crate::consensus::commit_processor::CommitProcessor,
        executor_client_for_proc: &Arc<ExecutorClient>,
        shared_last_global_exec_index: &Arc<std::sync::atomic::AtomicU64>,
        go_replay_after: u32,
    ) -> Result<(u64, u64)> {
        let mut startup_total_synced_blocks: u64 = 0;
        let mut startup_local_block: u64 = 0;

                if config.executor_read_enabled {
                    coordination_hub.set_startup_sync_active(true);
        
                    let barrier_client = executor_client_for_proc.clone();
                    let barrier_peers = config.peer_rpc_addresses.clone();
        
                    tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;
        
                    let mut early_peer_server_handle = None;
                    if let Some(peer_port) = config.peer_rpc_port {
                        if peer_port > 0 {
                            let peer_server = crate::network::peer_rpc::PeerRpcServer::new(
                                config.node_id,
                                peer_port,
                                config.network_address.clone(),
                                executor_client_for_proc.clone(),
                                shared_last_global_exec_index.clone(),
                            );
                            tracing::info!("📡 [PEER RPC] Starting EARLY server on 0.0.0.0:{} to prevent STARTUP-SYNC deadlock", peer_port);
                            early_peer_server_handle = Some(tokio::spawn(async move {
                                if let Err(e) = peer_server.start().await {
                                    tracing::error!("Early Peer RPC server error: {}", e);
                                }
                            }));
                        }
                    }
        
                    let mut local_block = storage.latest_block_number;
                    tracing::info!(
                        "📊 [STARTUP-SYNC] Barrier using verified block={} from setup_storage()",
                        local_block
                    );
        
                    match barrier_client.get_last_block_number().await {
                        Ok((requery_block, _gei, true, _hash, _)) => {
                            if requery_block > local_block {
                                tracing::info!(
                                    "📊 [STARTUP-SYNC] Go advanced since setup: {} -> {}. Using higher value.",
                                    local_block, requery_block
                                );
                                local_block = requery_block;
                            } else if requery_block < local_block {
                                tracing::warn!(
                                    "🚨 [STARTUP-SYNC] Go re-query returned STALE value ({} < {}). \
                                     Ignoring — this prevents re-execution of existing blocks.",
                                    requery_block, local_block
                                );
                            }
                        }
                        Ok((requery_block, _gei, false, _hash, _)) => {
                            tracing::warn!(
                                "⚠️ [STARTUP-SYNC] Go re-query not ready (block={}). Trusting setup_storage value={}.",
                                requery_block, local_block
                            );
                        }
                        Err(e) => {
                            tracing::warn!(
                                "⚠️ [STARTUP-SYNC] Go re-query failed ({}). Trusting setup_storage value={}.",
                                e, local_block
                            );
                        }
                    }
        
                    if local_block > 0 && !barrier_peers.is_empty() {
                        const MAX_VERIFY_RETRIES: u32 = 10;
                        const VERIFY_RETRY_DELAY_SECS: u64 = 3;
                        let mut verify_attempt = 0u32;
                        let mut anti_fork_verified = false;
        
                        loop {
                            let peer_result = crate::network::peer_rpc::fetch_blocks_from_peer(
                                &barrier_peers, local_block, local_block,
                            ).await;
        
                            let peer_blocks = match peer_result {
                                Ok(blocks) if !blocks.is_empty() => blocks,
                                Ok(_) => {
                                    verify_attempt += 1;
                                    if verify_attempt >= MAX_VERIFY_RETRIES {
                                        tracing::error!(
                                            "⚠️ [ANTI-FORK] Cannot verify block #{} — peers returned no data after {} attempts. \
                                             DEFERRING to POST-GATE-VERIFY (peers may be restarting simultaneously).",
                                            local_block, MAX_VERIFY_RETRIES
                                        );
                                        break;
                                    }
                                    tracing::warn!(
                                        "⏳ [ANTI-FORK] Peer returned no data for block #{} (attempt {}/{}). \
                                         Retrying in {}s...",
                                        local_block, verify_attempt, MAX_VERIFY_RETRIES, VERIFY_RETRY_DELAY_SECS
                                    );
                                    tokio::time::sleep(std::time::Duration::from_secs(VERIFY_RETRY_DELAY_SECS)).await;
                                    continue;
                                }
                                Err(e) => {
                                    verify_attempt += 1;
                                    if verify_attempt >= MAX_VERIFY_RETRIES {
                                        tracing::error!(
                                            "⚠️ [ANTI-FORK] Cannot verify block #{} — peer fetch failed after {} attempts: {}. \
                                             DEFERRING to POST-GATE-VERIFY (peers may be restarting simultaneously).",
                                            local_block, MAX_VERIFY_RETRIES, e
                                        );
                                        break;
                                    }
                                    tracing::warn!(
                                        "⏳ [ANTI-FORK] Could not fetch block #{} from peer (attempt {}/{}): {}. \
                                         Retrying in {}s...",
                                        local_block, verify_attempt, MAX_VERIFY_RETRIES, e, VERIFY_RETRY_DELAY_SECS
                                    );
                                    tokio::time::sleep(std::time::Duration::from_secs(VERIFY_RETRY_DELAY_SECS)).await;
                                    continue;
                                }
                            };
        
                            let local_blocks = match barrier_client.get_blocks_range(local_block, local_block).await {
                                Ok(blocks) if !blocks.is_empty() => blocks,
                                Ok(_) | Err(_) => {
                                    verify_attempt += 1;
                                    if verify_attempt >= MAX_VERIFY_RETRIES {
                                        tracing::error!(
                                            "⚠️ [ANTI-FORK] Cannot fetch block #{} from Go after {} attempts. \
                                             DEFERRING to POST-GATE-VERIFY (Go may still be initializing).",
                                            local_block, MAX_VERIFY_RETRIES
                                        );
                                        break;
                                    }
                                    tracing::warn!(
                                        "⏳ [ANTI-FORK] Could not fetch block #{} from Go (attempt {}/{}). \
                                         Retrying in {}s...",
                                        local_block, verify_attempt, MAX_VERIFY_RETRIES, VERIFY_RETRY_DELAY_SECS
                                    );
                                    tokio::time::sleep(std::time::Duration::from_secs(VERIFY_RETRY_DELAY_SECS)).await;
                                    continue;
                                }
                            };
        
                            let local_b = &local_blocks[0];
                            let peer_b = &peer_blocks[0];
        
                            if local_b.raw_block_bytes != peer_b.raw_block_bytes {
                                let mut warn_msg = format!(
                                    "⚠️ [ANTI-FORK] Block #{} hash MISMATCH between Go and peer! Go has stale/corrupt data.",
                                    local_block
                                );
        
                                warn_msg.push_str(&format!(
                                    "\n- Block Hash: local={}, peer={}",
                                    hex::encode(&local_b.block_hash),
                                    hex::encode(&peer_b.block_hash)
                                ));
        
                                if local_b.timestamp_ms != peer_b.timestamp_ms {
                                    warn_msg.push_str(&format!(
                                        "\n- Timestamp Mismatch: local={}, peer={}",
                                        local_b.timestamp_ms, peer_b.timestamp_ms
                                    ));
                                }
                                if local_b.transactions_root != peer_b.transactions_root {
                                    warn_msg.push_str(&format!(
                                        "\n- TxRoot Mismatch: local={}, peer={}",
                                        hex::encode(&local_b.transactions_root),
                                        hex::encode(&peer_b.transactions_root)
                                    ));
                                }
                                if local_b.receipts_root != peer_b.receipts_root {
                                    warn_msg.push_str(&format!(
                                        "\n- ReceiptsRoot Mismatch: local={}, peer={}",
                                        hex::encode(&local_b.receipts_root),
                                        hex::encode(&peer_b.receipts_root)
                                    ));
                                }
                                if local_b.state_root != peer_b.state_root {
                                    warn_msg.push_str(&format!(
                                        "\n- StateRoot Mismatch: local={}, peer={}",
                                        hex::encode(&local_b.state_root),
                                        hex::encode(&peer_b.state_root)
                                    ));
                                }
                                if local_b.extra_data != peer_b.extra_data {
                                    warn_msg.push_str("\n- ExtraData (LeaderAddress/Committee) Mismatch");
                                }
        
                                warn_msg.push_str("\n\nResetting local block cursor to 0 to trigger automatic state realignment from peers (will execute & overwrite).");
                                tracing::warn!("{}", warn_msg);

                                local_block = 0;
                                anti_fork_verified = false;
                                break;
                            } else {
                                tracing::info!(
                                    "✅ [ANTI-FORK] Block #{} verified: Go matches peer. State integrity confirmed.",
                                    local_block
                                );
                                anti_fork_verified = true;
                                break;
                            }
                        }
        
                        if !anti_fork_verified {
                            tracing::warn!(
                                "⚠️ [ANTI-FORK] Pre-sync verification DEFERRED — relying on POST-GATE-VERIFY \
                                 to catch any state corruption once peers are online."
                            );
                        }
                    }
        
                    const ACCEPTABLE_GAP: u64 = 2;
                    const INITIAL_RETRY_DELAY_MS: u64 = 500;
                    const MAX_RETRY_DELAY_MS: u64 = 5000;
                    let mut total_synced_blocks: u64 = 0;
        
                    loop {
                        let mut sync_round = 0;
                        loop {
                            let mut max_peer_block = 0u64;
                            let mut max_peer_gei = 0u64;
                            let mut reached_peers = 0;
                            for peer_addr in &barrier_peers {
                                match crate::network::peer_rpc::query_peer_info(peer_addr).await {
                                    Ok(info) => {
                                        reached_peers += 1;
                                        if info.last_block > max_peer_block {
                                            max_peer_block = info.last_block;
                                            max_peer_gei = info.last_global_exec_index;
                                        }
                                    }
                                    Err(e) => {
                                        tracing::debug!("⚠️ [STARTUP-SYNC] Peer {} info query failed: {}", peer_addr, e);
                                    }
                                }
                            }
        
                            if reached_peers == 0 {
                                if sync_round % 12 == 0 {
                                    tracing::error!("🚨 [STARTUP-SYNC] Could not reach any peers (round {}). Node is ISOLATED and BLOCKED pending peer discovery...", sync_round);
                                } else {
                                    tracing::warn!("⚠️ [STARTUP-SYNC] Could not reach any peers (round {}). Retrying...", sync_round);
                                }
                                
                                const MAX_ISOLATION_ROUNDS: u64 = 60;
                                if sync_round >= MAX_ISOLATION_ROUNDS {
                                    tracing::error!(
                                        "🚨 [STARTUP-SYNC] Node ISOLATED for {} rounds (~{}s). \
                                         Breaking out to let CommitSyncer handle sync. \
                                         Proposals remain BLOCKED until state is verified.",
                                        sync_round, sync_round * INITIAL_RETRY_DELAY_MS / 1000
                                    );
                                    break;
                                }
                                
                                tokio::time::sleep(std::time::Duration::from_millis(INITIAL_RETRY_DELAY_MS)).await;
                                sync_round += 1;
                                continue;
                            }
        
                            if max_peer_block == 0 {
                                tracing::info!("✅ [STARTUP-SYNC] Network is at Genesis (highest block is 0). Starting consensus...");
                                break;
                            }
        
                            if local_block > 0 && local_block + ACCEPTABLE_GAP >= max_peer_block {
                                tracing::info!(
                                    "✅ [STARTUP-SYNC] Local state in sync (local_block={}, peer_block={}, round={}). Starting consensus...",
                                    local_block, max_peer_block, sync_round
                                );
                                break;
                            }
        
                            let gap = max_peer_block - local_block;
                            tracing::warn!(
                                "🚨 [STARTUP-SYNC] Round {}: Local state BEHIND network! local_block={}, peer_block={}, gap={}, peer_gei={}. Syncing...",
                                sync_round, local_block, max_peer_block, gap, max_peer_gei
                            );
        
                            // FORK-SAFETY (May 2026): Fetch starting from local_block instead of local_block + 1.
                            // If the local tip block (local_block) has a mismatched/forked hash, Go must execute
                            // and overwrite it to align with peer consensus BEFORE executing local_block + 1,
                            // otherwise parent-hash check on local_block + 1 will fail and block catchup.
                            let from_block = if local_block > 0 { local_block } else { 1 };
                            let to_block = max_peer_block;
        
                            use rand::seq::SliceRandom;
                            let mut shuffled_peers = barrier_peers.clone();
                            shuffled_peers.shuffle(&mut rand::thread_rng());
        
                            match crate::network::peer_rpc::fetch_blocks_from_peer(&shuffled_peers, from_block, to_block).await {
                                Ok(blocks) if !blocks.is_empty() => {
                                    tracing::info!(
                                        "🔄 [STARTUP-SYNC] Round {}: Fetched {} blocks ({}-{}). Validating cryptographic stream...",
                                        sync_round,
                                        blocks.len(),
                                        blocks.first().map(|b| b.block_number).unwrap_or(0),
                                        blocks.last().map(|b| b.block_number).unwrap_or(0)
                                    );
        
                                    let mut is_valid = true;
                                    for i in 1..blocks.len() {
                                        let prev_hash = &blocks[i - 1].block_hash;
                                        let curr_parent = &blocks[i].parent_hash;
                                        if prev_hash != curr_parent {
                                            tracing::error!(
                                                "🚨 [ANTI-FORK] Hash chain broken at block {}! Expected parent hash {:?} but got {:?}",
                                                blocks[i].block_number, prev_hash, curr_parent
                                            );
                                            is_valid = false;
                                            break;
                                        }
                                    }
        
                                    if !is_valid {
                                        tracing::error!("🚨 [ANTI-FORK] Aborting sync round due to invalid block chain from peer. Node BLOCKED until valid data is received.");
                                        tokio::time::sleep(std::time::Duration::from_millis(INITIAL_RETRY_DELAY_MS)).await;
                                        sync_round += 1;
                                        continue;
                                    }
        
                                    let mut chunks = Vec::new();
                                    let mut current_chunk = Vec::new();
                                    let mut current_chunk_epoch = blocks[0].epoch;
        
                                    for block in blocks {
                                        if block.epoch > current_chunk_epoch {
                                            chunks.push(current_chunk);
                                            current_chunk = Vec::new();
                                            current_chunk_epoch = block.epoch;
                                        }
                                        current_chunk.push(block);
                                    }
                                    if !current_chunk.is_empty() {
                                        chunks.push(current_chunk);
                                    }
        
                                    let mut total_synced_this_round = 0;
                                    let mut round_last_block = local_block;
                                    let mut chunk_sync_failed = false;
        
                                    for chunk in chunks {
                                        let chunk_len = chunk.len();
                                        tracing::info!("🔄 [STARTUP-SYNC] Executing chunk of {} blocks (Epoch {})", chunk_len, chunk[0].epoch);
                                        match barrier_client.sync_and_execute_blocks(chunk).await {
                                            Ok((synced, last_block, _gei)) => {
                                                total_synced_this_round += synced;
                                                round_last_block = last_block;
                                            }
                                            Err(e) => {
                                                tracing::error!("❌ [STARTUP-SYNC] Chunk sync failed: {}", e);
                                                chunk_sync_failed = true;
                                                break;
                                            }
                                        }
                                    }
                                    
                                    if chunk_sync_failed {
                                        tracing::error!("🚨 [STARTUP-SYNC] Halting sync round due to chunk failure. Node BLOCKED pending successful Go sync.");
                                        tokio::time::sleep(std::time::Duration::from_millis(INITIAL_RETRY_DELAY_MS)).await;
                                        sync_round += 1;
                                        continue;
                                    }
        
                                    tracing::info!(
                                        "✅ [STARTUP-SYNC] Round {}: Synced {} blocks total this round (last_block={})",
                                        sync_round, total_synced_this_round, round_last_block
                                    );
                                    total_synced_blocks += total_synced_this_round;
                                    local_block = round_last_block;
                                            
                                            if let Ok((_, new_gei, _, _, _)) = barrier_client.get_last_block_number().await {
                                                coordination_hub.set_initial_global_exec_index(new_gei).await;
                                                
                                                {
                                                    shared_last_global_exec_index.store(new_gei, std::sync::atomic::Ordering::SeqCst);
                                                    tracing::info!("🔄 [STARTUP-SYNC] Updated shared_last_global_exec_index to {}", new_gei);
                                                }
                                                
                                                let mut post_sync_epoch = storage.current_epoch;
                                                let new_handled = match barrier_client.get_last_handled_commit_index().await {
                                                    Ok((commit_idx, _, _, go_epoch, _, last_ts, _state_root)) => {
                                                        post_sync_epoch = go_epoch;
                                                        if commit_idx > 0 {
                                                            tracing::info!(
                                                                "🔑 [STARTUP-SYNC] Got fresh lastHandledCommitIndex={} from Go RPC (post-sync), ts={}, go_epoch={}",
                                                                commit_idx, last_ts, go_epoch
                                                            );
                                                            commit_consumer.update_last_block_timestamp_ms(last_ts);
                                                            commit_idx
                                                        } else {
                                                            tracing::warn!(
                                                                "⚠️ [STARTUP-SYNC] Go returned lastHandledCommitIndex=0 after sync (go_epoch={}). Using go_replay_after={}",
                                                                go_epoch, go_replay_after
                                                            );
                                                            go_replay_after
                                                        }
                                                    }
                                                    Err(e) => {
                                                        tracing::warn!(
                                                            "⚠️ [STARTUP-SYNC] Failed to query Go for lastHandledCommitIndex: {}. Falling back to go_replay_after={}",
                                                            e, go_replay_after
                                                        );
                                                        go_replay_after
                                                    }
                                                };
                                                commit_consumer.monitor().set_highest_handled_commit(new_handled);
                                                commit_consumer.update_replay_after_commit_index(new_handled);
                                                tracing::info!(
                                                    "🔄 [STARTUP-SYNC] Round {}: Re-queried Go: gei={}, highest_handled_commit={}, replay_after_updated={}",
                                                    sync_round, new_gei, new_handled, new_handled
                                                );
                                                
                                                commit_processor.update_go_last_commit_index(new_handled);
                                                commit_processor.update_next_expected_index(new_handled + 1);
                                                tracing::info!(
                                                    "🔄 [STARTUP-SYNC] Updated CommitProcessor: go_last_commit_index={}, next_expected_index={}",
                                                    new_handled, new_handled + 1
                                                );
        
                                                if post_sync_epoch > storage.current_epoch {
                                                    tracing::info!(
                                                        "🔄 [STARTUP-SYNC] Updating Rust current_epoch from {} to {} based on synced Go DB",
                                                        storage.current_epoch, post_sync_epoch
                                                    );
                                                    storage.current_epoch = post_sync_epoch;
                                                    commit_processor.update_epoch(post_sync_epoch);
        
                                                    if let Ok((_, _, _, validators, _, _)) = barrier_client.get_epoch_boundary_data(post_sync_epoch).await {
                                                        if let Ok((_, new_eth_addrs)) = crate::node::committee::build_committee_with_eth_addresses(validators, post_sync_epoch) {
                                                            let epoch_eth_addresses_arc = commit_processor.get_epoch_eth_addresses_arc();
                                                            let mut map = epoch_eth_addresses_arc.write().await;
                                                            map.insert(post_sync_epoch, new_eth_addrs);
                                                            tracing::info!("🔄 [STARTUP-SYNC] Populated epoch_eth_addresses for epoch {}", post_sync_epoch);
                                                        } else {
                                                            tracing::error!("🚨 [STARTUP-SYNC] Failed to build committee ETH addresses for epoch {}", post_sync_epoch);
                                                        }
                                                    } else {
                                                        tracing::error!("🚨 [STARTUP-SYNC] Failed to fetch epoch boundary data for epoch {}", post_sync_epoch);
                                                    }
                                                }
                                            }
                                        }
                                Ok(_) => {
                                    tracing::warn!("⚠️ [STARTUP-SYNC] Round {}: Fetched 0 blocks from peers. Retrying...", sync_round);
                                }
                                Err(e) => {
                                    if sync_round % 12 == 0 {
                                        tracing::error!("🚨 [STARTUP-SYNC] Round {}: Failed to fetch blocks from peers: {}. Node is BLOCKED pending network sync...", sync_round, e);
                                    } else {
                                        tracing::warn!("⚠️ [STARTUP-SYNC] Round {}: Failed to fetch blocks from peers: {}. Retrying...", sync_round, e);
                                    }
                                }
                            }
        
                            let delay = std::cmp::min(
                                INITIAL_RETRY_DELAY_MS * (1 << sync_round.min(4)),
                                MAX_RETRY_DELAY_MS
                            );
                            tokio::time::sleep(tokio::time::Duration::from_millis(delay)).await;
                            sync_round += 1;
                        }
        
                        let mut final_handled_commit = commit_processor.go_last_commit_index;
                        if total_synced_blocks > 0 {
                            match executor_client_for_proc.get_last_handled_commit_index().await {
                                Ok(final_go_state) => {
                                    final_handled_commit = final_go_state.0;
                                }
                                Err(e) => {
                                    tracing::error!("❌ [STARTUP-SYNC] Failed to get final handled commit index from Go: {}", e);
                                }
                            }
        
                            let new_go_last = final_handled_commit;
                            let new_next_expected = new_go_last + 1;
                            tracing::warn!(
                                "🔧 [STARTUP-SYNC] Advancing CommitProcessor: go_last_commit_index {} → {}, \
                                 next_expected {} → {} (synced {} blocks during STARTUP-SYNC)",
                                commit_processor.go_last_commit_index, new_go_last,
                                commit_processor.next_expected_index, new_next_expected,
                                total_synced_blocks
                            );
                            commit_processor.go_last_commit_index = new_go_last;
                            commit_processor.next_expected_index = new_next_expected;
                        }
        
                        if total_synced_blocks > 0 && !barrier_peers.is_empty() {
                            tracing::info!(
                                "🔍 [POST-SYNC-VERIFY] Verifying block #{} integrity against peers...",
                                local_block
                            );
                            match crate::network::peer_rpc::fetch_blocks_from_peer(
                                &barrier_peers, local_block, local_block,
                            ).await {
                                Ok(peer_blocks) if !peer_blocks.is_empty() => {
                                    match barrier_client.get_blocks_range(local_block, local_block).await {
                                        Ok(local_blocks) if !local_blocks.is_empty() => {
                                            let local_raw = &local_blocks[0].raw_block_bytes;
                                            let peer_raw = &peer_blocks[0].raw_block_bytes;
                                            if local_raw == peer_raw {
                                                tracing::info!(
                                                    "✅ [POST-SYNC-VERIFY] Block #{} verified: local matches peer ({} bytes). \
                                                     State integrity confirmed.",
                                                    local_block, local_raw.len()
                                                );
                                            } else {
                                                tracing::error!(
                                                    "🚨 [POST-SYNC-VERIFY] Block #{} MISMATCH! local_bytes={} peer_bytes={}. \
                                                     Data corruption detected after sync!",
                                                    local_block, local_raw.len(), peer_raw.len()
                                                );
                                                tracing::warn!("🔄 [RECOVERY] Transient mismatch detected. Resetting local state and restarting STARTUP-SYNC...");
                                                local_block = 0;
                                                total_synced_blocks = 0;
                                                continue;
                                            }
                                        }
                                        _ => {
                                            tracing::warn!(
                                                "⚠️ [POST-SYNC-VERIFY] Could not fetch block #{} from Go for verification. Skipping.",
                                                local_block
                                            );
                                        }
                                    }
                                }
                                Ok(_) => {
                                    tracing::warn!(
                                        "⚠️ [POST-SYNC-VERIFY] Peer returned no data for block #{}. Skipping verification.",
                                        local_block
                                    );
                                }
                                Err(e) => {
                                    tracing::warn!(
                                        "⚠️ [POST-SYNC-VERIFY] Could not fetch block #{} from peer: {}. Skipping verification.",
                                        local_block, e
                                    );
                                }
                            }
        
                            let local_root = crate::ffi::get_go_state_root();
                            if !local_root.is_empty() && local_root != "0000000000000000000000000000000000000000000000000000000000000000" {
                                tracing::info!(
                                    "📊 [POST-SYNC-VERIFY] Local state root at block {}: 0x{}",
                                    local_block, local_root
                                );
                            }
                        }
        
                        if total_synced_blocks > 0 && !barrier_peers.is_empty() {
                            for gate_check in 0..3u32 {
                                tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;
                                let mut max_peer_block = 0u64;
                                for peer_addr in &barrier_peers {
                                    if let Ok(info) = crate::network::peer_rpc::query_peer_info(peer_addr).await {
                                        max_peer_block = max_peer_block.max(info.last_block);
                                    }
                                }
                                let gap = max_peer_block.saturating_sub(local_block);
                                if gap > ACCEPTABLE_GAP {
                                    tracing::warn!(
                                        "🔄 [FINAL-GATE] Check {}: Gap still {} (local={}, peer={}). Fetching remaining blocks...",
                                        gate_check, gap, local_block, max_peer_block
                                    );
                                    match crate::network::peer_rpc::fetch_blocks_from_peer(
                                        &barrier_peers, local_block + 1, max_peer_block
                                    ).await {
                                        Ok(blocks) if !blocks.is_empty() => {
                                            match barrier_client.sync_and_execute_blocks(blocks).await {
                                                Ok((synced, last_block, _gei)) => {
                                                    tracing::info!(
                                                        "✅ [FINAL-GATE] Synced {} more blocks (last={})",
                                                        synced, last_block
                                                    );
                                                    local_block = last_block;
                                                }
                                                Err(e) => {
                                                    tracing::warn!("⚠️ [FINAL-GATE] Sync failed: {}. Proceeding.", e);
                                                }
                                            }
                                        }
                                        _ => {
                                            tracing::warn!("⚠️ [FINAL-GATE] No blocks fetched. Proceeding.");
                                        }
                                    }
                                } else {
                                    tracing::info!(
                                        "✅ [FINAL-GATE] Check {}: Gap={} ≤ {} — network stable. Proceeding.",
                                        gate_check, gap, ACCEPTABLE_GAP
                                    );
                                    break;
                                }
                            }
        
                            match executor_client_for_proc.get_last_handled_commit_index().await {
                                Ok((commit_idx, gei, _, _, _, _, _state_root)) if commit_idx > 0 => {
                                    tracing::info!(
                                        "🔑 [FINAL-GATE] Updated CommitProcessor: last_handled={} → {}",
                                        commit_processor.go_last_commit_index, commit_idx
                                    );
                                    commit_processor.go_last_commit_index = commit_idx;
                                    commit_processor.next_expected_index = commit_idx + 1;
                                    commit_consumer.monitor().set_highest_handled_commit(commit_idx);
                                    commit_consumer.update_replay_after_commit_index(commit_idx);
        
                                    {
                                        shared_last_global_exec_index.store(gei, std::sync::atomic::Ordering::SeqCst);
                                        tracing::info!("🔄 [FINAL-GATE] Updated shared_last_global_exec_index to {}", gei);
                                    }
                                }
                                _ => {}
                            }
                        }
        
                        startup_total_synced_blocks = total_synced_blocks;
                        startup_local_block = local_block;
        
                        break;
                    }
        
                    if let Some(handle) = early_peer_server_handle {
                        tracing::info!("📡 [PEER RPC] Stopping early server. Handing over to full server in startup.rs...");
                        handle.abort();
                        tokio::time::sleep(tokio::time::Duration::from_millis(100)).await;
                    }
                }

        Ok((startup_total_synced_blocks, startup_local_block))
    }
}
