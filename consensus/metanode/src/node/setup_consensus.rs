// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! ConsensusNode Phase 2 consensus initialization.

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use crate::node::tx_submitter::TransactionClientProxy;
use crate::types::transaction::NoopTransactionVerifier;
use crate::node::ConsensusNode;
use crate::node::StorageSetup;
use crate::node::ConsensusSetup;
use anyhow::Result;
use consensus_core::{
    Clock, CommitConsumerArgs, ConsensusAuthority, DefaultSystemTransactionProvider, NetworkType,
    SystemTransactionProvider,
};
use meta_protocol_config::ProtocolConfig;
use prometheus::Registry;
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::Arc;
use std::time::Duration;
use tracing::{info, warn};

impl ConsensusNode {
    /// Builds the commit processor, consensus parameters, starts authority (or SyncOnly holder),
    /// and wires up all the shared state.
    pub(crate) async fn setup_consensus(
        config: &NodeConfig,
        storage: &mut StorageSetup,
        registry: &Registry,
        coordination_hub: consensus_core::coordination_hub::ConsensusCoordinationHub,
    ) -> Result<ConsensusSetup> {
        let clock = Arc::new(Clock::default());
        let transaction_verifier = Arc::new(NoopTransactionVerifier);
        // ═══════════════════════════════════════════════════════════════
        // FORK-SAFETY FIX v5: Use persisted commit_index, NOT GEI-derived value.
        // GEI includes fragment_offset, so (GEI - epoch_base) is HIGHER than actual commit_index.
        // Using inflated value causes CommitSyncer to PREVENT legitimate commits.
        //
        // CRITICAL: When DAG is wiped (snapshot restore), persistence files are also
        // gone. In that case we MUST set go_replay_after=0 to let the new DAG start
        // from commit 1. The CommitProcessor's AUTO-JUMP and executor's GEI guard
        // will handle skipping already-executed commits in Go.
        // ═══════════════════════════════════════════════════════════════
        // Detect empty DAG (snapshot restore) FIRST - needed for go_replay_after decision
        let dag_has_history = {
            let epoch_db = config
                .storage_path
                .join("epochs")
                .join(format!("epoch_{}", storage.current_epoch))
                .join("consensus_db");
            epoch_db.exists()
                && std::fs::read_dir(&epoch_db)
                    .map(|mut entries| entries.next().is_some())
                    .unwrap_or(false)
        };

        // ═══════════════════════════════════════════════════════════════
        // UNIFIED RECOVERY BARRIER (May 2026 — Architectural Fix)
        //
        // EPOCH-AGNOSTIC SNAPSHOT DETECTION:
        // If the DAG is empty but Go has blocks, this is a snapshot recovery
        // regardless of epoch number or commit count.
        //
        // OLD BUG: The previous check `handled_commits >= 300` failed on epoch 2+
        // because epoch-scoped commit indices reset to 0, always failing the threshold.
        // This caused the schedule_recovery_pending guard to never activate,
        // allowing premature Healthy transitions with stale LeaderSwapTables.
        //
        // NEW: Detect snapshot recovery by checking if Go has already executed blocks
        // (go_replay_after > 0 OR go_block > 0) while the DAG is empty. This works
        // across all epochs.
        // ═══════════════════════════════════════════════════════════════
        // CRITICAL FIX (May 2026): The old check `!dag_has_history` was WRONG.
        // After snapshot restore, the consensus_db from the previous epoch is
        // PRESERVED as part of the snapshot data — so dag_has_history=true even
        // though the node needs full recovery. This caused the recovery barrier
        // to NEVER activate after snapshot restore, allowing premature Healthy
        // transitions → fork.
        //
        // NEW: Activate the barrier whenever Go has prior execution state,
        // REGARDLESS of whether the consensus_db exists. The barrier is harmless
        // on normal restart (it will quickly reach Ready via existing DAG commits)
        // but critical on snapshot restore.
        {
            let go_has_state = storage.latest_block_number > 0 || storage.last_handled_commit_index.map_or(false, |c| c > 0);
            if go_has_state {
                coordination_hub.activate_recovery_barrier();
                // CRITICAL FIX: We MUST set schedule_recovery_pending=true so the node enters ScheduleVerifying
                // and naturally rebuilds its LeaderSchedule from the network instead of bypassing it.
                // This applies to BOTH snapshot restores and normal restarts to ensure identical strictness.
                coordination_hub.set_schedule_recovery_pending(true);
                
                info!(
                    "🛡️ [RECOVERY-BARRIER] Go has prior state (block={}, last_handled={:?}, dag_has_history={}). \
                     Activating unified recovery barrier. \
                     ALL proposals blocked until GoSyncing → DagCatchingUp → ScheduleVerifying → Ready.",
                    storage.latest_block_number, storage.last_handled_commit_index, dag_has_history
                );
            } else {
                info!(
                    "ℹ️ [RECOVERY-BARRIER] Go has no prior state (block=0). \
                     This is a fresh start (genesis). Recovery barrier NOT activated."
                );
            }
        }

        let go_replay_after = if config.executor_read_enabled {
            if let Some(commit_index) = storage.last_handled_commit_index {
                info!(
                    "🔑 [GO-AUTH GEI] Setting go_replay_after={} based on Go Authoritative LastHandledCommitIndex.",
                    commit_index
                );
                commit_index
            } else {
                warn!(
                    "⚠️ [GO-AUTH GEI] LastHandledCommitIndex not available from StorageSetup. \
                     Falling back to wipe_safe persistence."
                );
                // Fall back to persistence if Go RPC failed (e.g. timeout / rare startup race)
                let wipe_safe = crate::node::executor_client::persistence::load_persisted_last_index_wipe_safe(&config.storage_path);
                match wipe_safe {
                    Some((_gei, commit_index)) if commit_index > 0 => {
                        info!(
                            "📊 [FORK-SAFETY] Recovered commit_index={} from wipe-safe persistence (fallback)",
                            commit_index
                        );
                        commit_index
                    }
                    _ => {
                        if !dag_has_history {
                            info!(
                                "📊 [FORK-SAFETY] DAG empty + no persistence = first start or full reset. \
                                 Setting go_replay_after=0"
                            );
                            0
                        } else if storage.last_global_exec_index > storage.epoch_base_exec_index {
                            (storage.last_global_exec_index - storage.epoch_base_exec_index) as u32
                        } else {
                            0
                        }
                    }
                }
            }
        } else {
            0
        };
        info!(
            "📊 [STARTUP] CommitConsumerArgs: go_replay_after={} (from authoritative Go/fallback)",
            go_replay_after
        );
        // Phase 1 Handshake - Retrieve last_executed_commit_hash from Go.
        info!(
            "🤝 [HANDSHAKE] Passing last_executed_commit_hash from Go to Rust DAG: {:?}",
            hex::encode(storage.last_executed_commit_hash)
        );

        let (mut commit_consumer, commit_receiver, mut block_receiver) =
            CommitConsumerArgs::new(go_replay_after, go_replay_after, storage.last_executed_commit_hash, storage.last_block_timestamp_ms);
        let current_commit_index = Arc::new(AtomicU32::new(0));
        let is_transitioning = coordination_hub.get_is_transitioning_ref();

        // Load persisted transaction queue
        let persisted_queue = super::queue::load_transaction_queue_static(&storage.storage_path)
            .await
            .unwrap_or_default();
        if !persisted_queue.is_empty() {
            info!("💾 Loaded {} persisted transactions", persisted_queue.len());
        }
        let pending_transactions_queue = Arc::new(tokio::sync::Mutex::new(persisted_queue));

        // Load committed transaction hashes from current epoch for duplicate prevention
        let committed_hashes = crate::node::transition::load_committed_transaction_hashes(
            &storage.storage_path,
            storage.current_epoch,
        )
        .await;
        if !committed_hashes.is_empty() {
            info!(
                "💾 Loaded {} committed transaction hashes from epoch {}",
                committed_hashes.len(),
                storage.current_epoch
            );
        }
        let committed_transaction_hashes = Arc::new(tokio::sync::Mutex::new(committed_hashes));

        let (epoch_tx_sender, epoch_tx_receiver) =
            tokio::sync::mpsc::channel::<(u64, u64, u64, u64)>(1000);
        let epoch_transition_callback =
            crate::consensus::commit_callbacks::create_epoch_transition_callback(
                epoch_tx_sender.clone(),
            );

        let shared_last_global_exec_index = coordination_hub.get_global_exec_index_ref();
        
        let initial_gei_for_hub = if go_replay_after == 0 {
            info!("📊 [FORK-SAFETY] go_replay_after is 0. Initializing shared_gei to epoch_base_exec_index={} for mathematical reconstruction.", storage.epoch_base_exec_index);
            storage.epoch_base_exec_index
        } else {
            storage.last_global_exec_index
        };
        coordination_hub.set_initial_global_exec_index(initial_gei_for_hub).await;

        if !dag_has_history && storage.is_in_committee && storage.current_epoch > 0 {
            warn!(
                "⚠️ [FORK-SAFETY] DAG storage empty for epoch {} — snapshot restore detected. \
                 GEI guard in executor will skip commits Go has already executed.",
                storage.current_epoch
            );
        }

        // Stage 4 Conveyor Belt Buffer: BACKPRESSURE-TUNED buffer size.
        let (delivery_tx, delivery_rx) = tokio::sync::mpsc::channel(100);

        let next_expected_commit_index = if config.executor_read_enabled && go_replay_after > 0 {
            go_replay_after + 1
        } else {
            1
        };
        info!(
            "📊 [COMMIT PROCESSOR INIT] Startup: next_expected_commit_index={}, from go_replay_after={}",
            next_expected_commit_index, go_replay_after
        );

        let commit_processor = crate::consensus::commit_processor::CommitProcessor::new(
            commit_receiver,
        )
        .with_delivery_sender(delivery_tx)
        .with_commit_index_callback(
            crate::consensus::commit_callbacks::create_commit_index_callback(
                current_commit_index.clone(),
                commit_consumer.monitor(),
            ),
        )
        .with_global_exec_index_callback(
            crate::consensus::commit_callbacks::create_global_exec_index_callback(
                shared_last_global_exec_index.clone(),
            ),
        )
        .with_shared_last_global_exec_index(shared_last_global_exec_index.clone())
        .with_epoch_info(storage.current_epoch)
        .with_next_expected_index(next_expected_commit_index)
        .with_go_last_commit_index(go_replay_after as u32)
        .with_is_transitioning(is_transitioning.clone())
        .with_pending_transactions_queue(pending_transactions_queue.clone())
        .with_epoch_transition_callback(epoch_transition_callback)
        .with_epoch_eth_addresses({
            let mut map = std::collections::HashMap::new();
            map.insert(
                storage.current_epoch,
                storage.validator_eth_addresses.clone(),
            );
            Arc::new(tokio::sync::RwLock::new(map))
        })
        .with_storage_path(config.storage_path.clone())
        .with_quorum_commit_index(coordination_hub.get_quorum_commit_index_ref())
        .with_committee_size(storage.validator_eth_addresses.len())
        ;

        let digest_verifier_hub = coordination_hub.clone();
        let mut commit_processor = commit_processor.with_digest_verifier(move |index: u32| {
            if let Some(verifier) = digest_verifier_hub.get_digest_verifier() {
                verifier(index)
            } else {
                None // Monitor not yet initialized
            }
        });

        // COLD-START-FIX (May 2026): Wire digest data checker to CommitVoteMonitor.
        // This callback returns true ONLY when CommitVoteMonitor has received actual
        // digest votes from P2P blocks, NOT when CommitSyncer has merely set QCI > 0.
        let digest_checker_hub = coordination_hub.clone();
        commit_processor = commit_processor.with_digest_data_checker(move || {
            digest_checker_hub.has_digest_data()
        });

        // ZERO-TIMEOUT (May 2026): Wire peer commit attestation callback.
        // Replaces COLD-START-BYPASS (10s) and SUSTAINED-LOAD-BYPASS (5s).
        let peer_attest_hub = coordination_hub.clone();
        commit_processor = commit_processor.with_peer_commit_attestation(move |index: u32, digest: [u8; 32]| {
            if let Some(attestor) = peer_attest_hub.get_peer_commit_attestation() {
                attestor(index, digest)
            } else {
                consensus_core::coordination_hub::PeerAttestResult::Insufficient // Not yet initialized
            }
        });

        // ExecutorClient for commit processing
        let initial_next_expected = if config.executor_read_enabled {
            storage.last_global_exec_index + 1
        } else {
            1
        };

        // MOVED UP: Create system_transaction_provider BEFORE executor_client_for_proc
        // so we can wire the go_lag_handle for backpressure
        let epoch_duration_seconds = storage.epoch_duration_from_go;
        let system_transaction_provider = Arc::new(DefaultSystemTransactionProvider::new(
            storage.current_epoch,
            epoch_duration_seconds,
            storage.epoch_timestamp_ms,
            config.time_based_epoch_change,
        ));

        let executor_client_for_proc = if config.executor_read_enabled {
            let mut client = ExecutorClient::new_with_initial_index(
                true,
                config.executor_commit_enabled,
                config.executor_send_socket_path.clone(),
                config.executor_receive_socket_path.clone(),
                initial_next_expected,
                Some(config.storage_path.clone()),
            );
            client.set_go_lag_handle(system_transaction_provider.go_lag_handle());
            Arc::new(client)
        } else {
            Arc::new(ExecutorClient::new(
                false,
                false,
                "".to_string(),
                "".to_string(),
                None,
            ))
        };

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
                        let mut err_msg = format!(
                            "🚨 [ANTI-FORK] FATAL: Block #{} hash MISMATCH between Go and peer!\nGo has stale/corrupt data.",
                            local_block
                        );

                        err_msg.push_str(&format!(
                            "\n- Block Hash: local={}, peer={}",
                            hex::encode(&local_b.block_hash),
                            hex::encode(&peer_b.block_hash)
                        ));

                        if local_b.timestamp_ms != peer_b.timestamp_ms {
                            err_msg.push_str(&format!(
                                "\n- Timestamp Mismatch: local={}, peer={}",
                                local_b.timestamp_ms, peer_b.timestamp_ms
                            ));
                        }
                        if local_b.transactions_root != peer_b.transactions_root {
                            err_msg.push_str(&format!(
                                "\n- TxRoot Mismatch: local={}, peer={}",
                                hex::encode(&local_b.transactions_root),
                                hex::encode(&peer_b.transactions_root)
                            ));
                        }
                        if local_b.receipts_root != peer_b.receipts_root {
                            err_msg.push_str(&format!(
                                "\n- ReceiptsRoot Mismatch: local={}, peer={}",
                                hex::encode(&local_b.receipts_root),
                                hex::encode(&peer_b.receipts_root)
                            ));
                        }
                        if local_b.state_root != peer_b.state_root {
                            err_msg.push_str(&format!(
                                "\n- StateRoot Mismatch: local={}, peer={}",
                                hex::encode(&local_b.state_root),
                                hex::encode(&peer_b.state_root)
                            ));
                        }
                        if local_b.extra_data != peer_b.extra_data {
                            err_msg.push_str("\n- ExtraData (LeaderAddress/Committee) Mismatch");
                        }

                        err_msg.push_str("\n\nHALTING NODE to prevent fork. Operator MUST wipe the database and restart from scratch.");
                        tracing::error!("{}", err_msg);
                        panic!("{}", err_msg);
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
                                            let mut gei_guard = shared_last_global_exec_index.lock().await;
                                            *gei_guard = new_gei;
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
                                let mut gei_guard = shared_last_global_exec_index.lock().await;
                                *gei_guard = gei;
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

        if config.executor_read_enabled {
            match executor_client_for_proc.get_last_handled_commit_index().await {
                Ok((commit_idx, gei, block_num, go_epoch, _auth, _ts, _state_root)) => {
                    tracing::info!(
                        "🔍 [CONSISTENCY CHECK] Go state: block={}, gei={}, commit_index={}, epoch={}, \
                         Rust state: go_replay_after={}, current_epoch={}, next_expected_commit={}",
                        block_num, gei, commit_idx, go_epoch,
                        go_replay_after, storage.current_epoch, next_expected_commit_index
                    );

                    if go_epoch != storage.current_epoch && commit_idx > 0 {
                        tracing::error!(
                            "🚨 [CONSISTENCY CHECK] EPOCH MISMATCH: Go epoch={} != Rust epoch={}! \
                             Go reports commit_index={} which may belong to wrong epoch. \
                             Forcing go_replay_after=0 and next_expected=1 to prevent cross-epoch fork.",
                            go_epoch, storage.current_epoch, commit_idx
                        );
                        commit_processor.go_last_commit_index = 0;
                        commit_processor.next_expected_index = 1;
                    } else if block_num > 0 && commit_idx == 0 {
                        tracing::info!(
                            "ℹ️ [CONSISTENCY CHECK] Go reports block={} but commit_index=0. \
                             This is expected after epoch transition or snapshot restore. \
                             CommitProcessor starts from commit 1 — Go's GEI guard handles duplicates.",
                            block_num
                        );
                    } else if block_num == 0 && commit_idx > 0 {
                        tracing::error!(
                            "🚨 [CONSISTENCY CHECK] Go reports block=0 but lastHandledCommitIndex={}! \
                             Stale BackupDB data detected. Resetting CommitProcessor to commit 1.",
                            commit_idx
                        );
                        commit_processor.go_last_commit_index = 0;
                        commit_processor.next_expected_index = 1;
                    } else {
                        tracing::info!(
                            "✅ [CONSISTENCY CHECK] Post-sync state OK: block={}, gei={}, commit_index={}, epoch={}",
                            block_num, gei, commit_idx, go_epoch
                        );
                    }

                    if commit_processor.go_last_commit_index > 0 && commit_idx > 0 {
                        if commit_processor.go_last_commit_index > commit_idx + 100 {
                            tracing::error!(
                                "🚨 [SAFETY NET] Cross-epoch deadlock detected! \
                                 CommitProcessor.go_last_commit_index={} >> Go.commit_index={}. \
                                 Resetting to prevent permanent block stall.",
                                commit_processor.go_last_commit_index, commit_idx
                            );
                            commit_processor.go_last_commit_index = commit_idx;
                            commit_processor.next_expected_index = commit_idx + 1;
                        }
                    }
                }
                Err(e) => {
                    tracing::warn!(
                        "⚠️ [CONSISTENCY CHECK] Failed to query Go state: {}. Proceeding with current values.",
                        e
                    );
                }
            }
        }

        let is_terminally_failed = Arc::new(AtomicBool::new(false));

        executor_client_for_proc.initialize_from_go().await;
        tracing::info!(
            "✅ [STARTUP] initialize_from_go() completed synchronously (block/GEI guards updated)"
        );

        if config.executor_read_enabled {
            let executor_next_expected = {
                let guard = executor_client_for_proc.next_expected_index.lock().await;
                *guard
            };
            let cp_next_expected = commit_processor.next_expected_index;
            let cp_go_last = commit_processor.go_last_commit_index;

            match executor_client_for_proc.get_last_handled_commit_index().await {
                Ok((go_commit_idx, go_gei, go_block, go_epoch, _, _, state_root)) => {
                    let state_root_hex = hex::encode(&state_root);
                    tracing::info!(
                        "🔍 [SYNC-PARITY] Post-init state comparison:\n  \
                         CommitProcessor:  go_last_commit_index={}, next_expected_index={}\n  \
                         ExecutorClient:   next_expected_index={}\n  \
                         Go (authoritative): commit_index={}, gei={}, block={}, epoch={}\n  \
                         Go State Root: 0x{}",
                        cp_go_last, cp_next_expected,
                        executor_next_expected,
                        go_commit_idx, go_gei, go_block, go_epoch, state_root_hex
                    );

                    if cp_next_expected > go_commit_idx + 1 && go_commit_idx > 0 {
                        tracing::error!(
                            "🚨 [SYNC-PARITY] CommitProcessor AHEAD of Go! \
                             cp.next_expected={} > go.commit_index+1={}. \
                             Correcting CommitProcessor to prevent commit skipping.",
                            cp_next_expected, go_commit_idx + 1
                        );
                        commit_processor.go_last_commit_index = go_commit_idx;
                        commit_processor.next_expected_index = go_commit_idx + 1;
                    }

                    if go_commit_idx == 0 && cp_go_last > 0 {
                        tracing::warn!(
                            "⚠️ [SYNC-PARITY] Go at commit_index=0 (fresh epoch) but \
                             CommitProcessor.go_last_commit_index={}. \
                             Resetting to (0, 1) to match Go's epoch-scoped state.",
                            cp_go_last
                        );
                        commit_processor.go_last_commit_index = 0;
                        commit_processor.next_expected_index = 1;
                    }

                    let cp_gei = {
                        let gei_guard = shared_last_global_exec_index.lock().await;
                        *gei_guard + 1
                    };
                    let ec_cp_delta = (executor_next_expected as i64) - (cp_gei as i64);
                    if ec_cp_delta.abs() > 1 {
                        tracing::warn!(
                            "⚠️ [SYNC-PARITY] ExecutorClient/CommitProcessor GEI divergence: \
                             EC.next_expected_gei={} vs CP.expected_gei={} (delta={}). \
                             This indicates a potential sync race.",
                            executor_next_expected, cp_gei, ec_cp_delta
                        );
                    } else {
                        tracing::info!(
                            "✅ [SYNC-PARITY] ExecutorClient and CommitProcessor GEI matched \
                             (EC={}, CP={})",
                             executor_next_expected, cp_gei
                        );
                    }
                }
                Err(e) => {
                    tracing::warn!(
                        "⚠️ [SYNC-PARITY] Could not verify sync parity — Go query failed: {}. \
                         Proceeding with current state.",
                        e
                    );
                }
            }
        }

        if config.executor_read_enabled {
            match executor_client_for_proc.get_last_block_number().await {
                Ok((local_block, local_gei, true, _hash, _epoch)) => {
                    tracing::info!(
                        "🔍 [GEI-CROSSCHECK] Post-startup state: block={}, gei={}",
                        local_block, local_gei
                    );
                    match crate::consensus::commit_processor::gei_validator::validate_gei_against_peers(
                        local_gei,
                        local_block,
                        &config.peer_rpc_addresses,
                    ).await {
                        Ok(()) => {
                            tracing::info!(
                                "✅ [GEI-CROSSCHECK] Post-startup GEI validation passed (gei={}, block={})",
                                  local_gei, local_block
                            );
                        }
                        Err(e) => {
                            tracing::error!(
                                "🚨 [GEI-CROSSCHECK] Post-startup GEI mismatch detected: {}. \
                                 This means local state reconstruction produced a different GEI \
                                 than the cluster. Fork is likely if this is not resolved. \
                                 Continuing startup but monitor closely.",
                                e
                            );
                        }
                    }
                }
                _ => {}
            }
        }

        coordination_hub.set_startup_go_sync_completed(true);

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

        if startup_total_synced_blocks > 0 && !config.peer_rpc_addresses.is_empty() {
            let guard_client = executor_client_for_proc.clone();
            let guard_peers = config.peer_rpc_addresses.clone();
            let guard_start_block = startup_local_block;
            let guard_terminally_failed = is_terminally_failed.clone();
            tokio::spawn(async move {
                Self::runtime_fork_guard(guard_client, guard_peers, guard_start_block, guard_terminally_failed).await;
            });
        }

        let peer_addrs = config.peer_rpc_addresses.clone();
        let executor_client_for_manager = executor_client_for_proc.clone();
        tokio::spawn(async move {
            let manager = crate::node::block_delivery::BlockDeliveryManager::new(
                executor_client_for_manager,
                delivery_rx,
                peer_addrs,
            );
            manager.run().await;
            tracing::info!("🛑 [STATION 4: DELIVERY] BlockDeliveryManager gracefully exited (expected on Epoch Transition).");
        });

        let health_client = executor_client_for_proc.clone();
        let health_peers = config.peer_rpc_addresses.clone();
        tokio::spawn(async move {
            tokio::time::sleep(tokio::time::Duration::from_secs(30)).await;
            
            const MAX_HEALTH_RETRIES: u32 = 3;
            let mut passed = false;
            for attempt in 1..=MAX_HEALTH_RETRIES {
                let checker = crate::node::health_check::PostRecoveryHealthCheck::new(
                    health_client.clone(), health_peers.clone()
                );
                let result = checker.run().await;
                if result.is_healthy() {
                    tracing::info!("✅ [HEALTH] Post-recovery health check PASSED (attempt {}/{}): {:?}", attempt, MAX_HEALTH_RETRIES, result);
                    passed = true;
                    break;
                } else if attempt < MAX_HEALTH_RETRIES {
                    tracing::warn!(
                        "⚠️ [HEALTH] Post-recovery health check FAILED (attempt {}/{}): {:?}. \
                         Retrying in 30s — node may still be in schedule recovery...",
                        attempt, MAX_HEALTH_RETRIES, result
                    );
                    tokio::time::sleep(tokio::time::Duration::from_secs(30)).await;
                } else {
                    tracing::error!("🚨 [HEALTH] Post-recovery health check FAILED after {} attempts: {:?}", MAX_HEALTH_RETRIES, result);
                }
            }
            if !passed {
                tracing::error!("🚨 [HEALTH] Node did NOT recover within the health check window. Manual investigation required.");
            }
        });

        let tx_recycler = Arc::new(crate::consensus::tx_recycler::TxRecycler::new());
        info!("♻️ [TX RECYCLER] Created shared TxRecycler instance");

        commit_processor = commit_processor
            .with_executor_client(executor_client_for_proc.clone())
            .with_tx_recycler(tx_recycler.clone())
            .with_committed_transaction_hashes(committed_transaction_hashes.clone());

        let (lag_alert_sender, mut lag_alert_receiver) = tokio::sync::mpsc::channel::<
            crate::consensus::commit_processor::lag_monitor::LagAlert,
        >(1000);

        commit_processor = commit_processor.with_lag_alert_sender(lag_alert_sender);

        tokio::spawn(async move {
            while let Some(alert) = lag_alert_receiver.recv().await {
                match alert {
                    crate::consensus::commit_processor::lag_monitor::LagAlert::ModerateLag {
                        gap,
                        go_rate,
                        go_block_number,
                        ..
                    } => {
                        tracing::warn!("⚠️ [LAG-MONITOR] Go is {} blocks behind Rust (rate: {:.1} blk/s), go_block_number={}. Monitoring...", gap, go_rate, go_block_number);
                    }
                    crate::consensus::commit_processor::lag_monitor::LagAlert::SevereLag {
                        rust_gei,
                        go_gei,
                        go_block_number,
                        gap,
                        go_rate,
                    } => {
                        tracing::error!("🚨 [LAG-MONITOR] SEVERE: Go is {} blocks behind Rust! (rust={}, go_gei={}, go_block={}, rate={:.1} blk/s).",
                            gap, rust_gei, go_gei, go_block_number, go_rate);

                        tracing::warn!("⚠️ [LAG-RECOVERY] P2P block import is DISABLED for Master nodes. Allowing Go to catch up naturally via BATCH-DRAIN to maintain consensus parity.");
                    }
                    crate::consensus::commit_processor::lag_monitor::LagAlert::Recovered {
                        ..
                    } => {
                        tracing::info!("✅ [LAG-MONITOR] Go has caught up with Rust. Normal operations resumed.");
                    }
                }
            }
        });

        let epoch_eth_addresses_arc = commit_processor.get_epoch_eth_addresses_arc().clone();

        let failed_processor = is_terminally_failed.clone();
        tokio::spawn(async move {
            if let Err(e) = commit_processor.run().await {
                tracing::error!("❌ [STATION 3: PROCESSOR] Fatal Error: {}", e);
                failed_processor.store(true, Ordering::SeqCst);
            } else {
                tracing::info!("🛑 [STATION 3: PROCESSOR] Gracefully Exited (Expected upon EndOfEpoch).");
            }
        });

        tokio::spawn(async move {
            while let Some(output) = block_receiver.recv().await {
                tracing::debug!("Received {} certified blocks", output.blocks.len());
            }
        });

        let protocol_config = ProtocolConfig::get_for_max_version_UNSAFE();
        let mut parameters = consensus_config::Parameters::default();
        parameters.commit_sync_batch_size = config.commit_sync_batch_size;
        parameters.commit_sync_parallel_fetches = config.commit_sync_parallel_fetches;
        parameters.commit_sync_batches_ahead = config.commit_sync_batches_ahead;

        if let Some(ms) = config.min_round_delay_ms {
            parameters.min_round_delay = Duration::from_millis(ms);
        }

        parameters.adaptive_delay_enabled = config.adaptive_delay_enabled;

        if let Some(ms) = config.leader_timeout_ms {
            parameters.leader_timeout = Duration::from_millis(ms);
        } else if config.speed_multiplier != 1.0 {
            info!("Applying speed multiplier: {}x", config.speed_multiplier);
            parameters.leader_timeout =
                Duration::from_millis((200.0 / config.speed_multiplier) as u64);
        }

        let db_path = config
            .storage_path
            .join("epochs")
            .join(format!("epoch_{}", storage.current_epoch))
            .join("consensus_db");
        std::fs::create_dir_all(&db_path)?;
        parameters.db_path = db_path;

        if !config.peer_rpc_addresses.is_empty() && storage.current_epoch > 0 {
            let mut peer_epoch_counts: std::collections::HashMap<u64, u32> = std::collections::HashMap::new();
            let mut max_peer_epoch = storage.current_epoch;
            
            for peer_addr in &config.peer_rpc_addresses {
                match crate::network::peer_rpc::query_peer_info(peer_addr).await {
                    Ok(info) => {
                        *peer_epoch_counts.entry(info.epoch).or_insert(0) += 1;
                        if info.epoch > max_peer_epoch {
                            max_peer_epoch = info.epoch;
                        }
                    }
                    Err(e) => {
                        tracing::debug!("⚠️ [EPOCH-CROSSCHECK] Peer {} unreachable: {}", peer_addr, e);
                    }
                }
            }
            
            if max_peer_epoch > storage.current_epoch {
                let peers_at_higher = peer_epoch_counts.iter()
                    .filter(|(&epoch, _)| epoch > storage.current_epoch)
                    .map(|(_, &count)| count)
                    .sum::<u32>();
                let total_peers_reached = peer_epoch_counts.values().sum::<u32>();
                
                if peers_at_higher > 0 && peers_at_higher >= total_peers_reached / 2 {
                    tracing::warn!(
                        "⚡ [EPOCH-CROSSCHECK] MAJORITY of peers ({}/{}) at epoch {} \
                         (local epoch={}). Attempting epoch correction via Go...",
                        peers_at_higher, total_peers_reached,
                        max_peer_epoch, storage.current_epoch
                    );
                    
                    let mut correction_attempt = 0u32;
                    let max_correction_retries = 5u32;
                    
                    loop {
                        correction_attempt += 1;
                        match executor_client_for_proc.get_current_epoch().await {
                            Ok(go_epoch) if go_epoch > storage.current_epoch => {
                                tracing::info!(
                                    "✅ [EPOCH-CROSSCHECK] Go confirms epoch {} (was {}). \
                                     Updating storage.current_epoch to match cluster.",
                                    go_epoch, storage.current_epoch
                                );
                                storage.current_epoch = go_epoch;
                                
                                if let Ok((_, timestamp_ms, _, validators, _, boundary_gei)) = 
                                    executor_client_for_proc.get_epoch_boundary_data(go_epoch).await 
                                {
                                    if !validators.is_empty() {
                                        if let Ok((new_committee, new_eth_addrs)) = 
                                            crate::node::committee::build_committee_with_eth_addresses(
                                                validators, go_epoch
                                            ) 
                                        {
                                            let transition_hash = super::committee_source::calculate_committee_hash(&new_committee);
                                            let transition_hash_hex = hex::encode(&transition_hash[..8]);
                                            let mut epoch_match = 0u32;
                                            let mut epoch_mismatch = 0u32;
                                            for peer_addr in &config.peer_rpc_addresses {
                                                match tokio::time::timeout(
                                                    std::time::Duration::from_secs(5),
                                                    crate::network::peer_rpc::query_peer_epoch_boundary_data(peer_addr, go_epoch),
                                                ).await {
                                                    Ok(Ok(response)) if !response.validators.is_empty() => {
                                                        let peer_validators: Vec<crate::node::executor_client::proto::ValidatorInfo> = response.validators.iter().map(|v| {
                                                            crate::node::executor_client::proto::ValidatorInfo {
                                                                name: v.name.clone(),
                                                                address: v.address.clone(),
                                                                stake: v.stake.to_string(),
                                                                protocol_key: v.protocol_key.clone(),
                                                                network_key: v.network_key.clone(),
                                                                authority_key: v.authority_key.clone(),
                                                                p2p_address: v.p2p_address.clone(),
                                                                ..Default::default()
                                                            }
                                                        }).collect();
                                                        if let Ok((peer_comm, _)) = crate::node::committee::build_committee_with_eth_addresses(peer_validators, go_epoch) {
                                                            let peer_hash = super::committee_source::calculate_committee_hash(&peer_comm);
                                                            if transition_hash == peer_hash {
                                                                epoch_match += 1;
                                                            } else {
                                                                epoch_mismatch += 1;
                                                                tracing::error!(
                                                                    "🚨 [LAYER-7 EPOCH] Committee MISMATCH at epoch {} transition! \
                                                                     local={}... ≠ peer {}. CRITICAL.",
                                                                    go_epoch, transition_hash_hex, peer_addr
                                                                );
                                                            }
                                                        }
                                                    }
                                                    _ => {}
                                                }
                                            }
                                            if epoch_mismatch > 0 {
                                                tracing::error!(
                                                    "🛑 [LAYER-7 EPOCH] HALT: {} peer(s) have different committee for epoch {}. \
                                                     Stopping epoch transition to prevent fork.",
                                                    epoch_mismatch, go_epoch
                                                );
                                                break;
                                            }
                                            if epoch_match > 0 {
                                                tracing::info!(
                                                    "✅ [LAYER-7 EPOCH] Committee verified for epoch {}: {}/{} peers match (hash={}...)",
                                                    go_epoch, epoch_match, config.peer_rpc_addresses.len(), transition_hash_hex
                                                );
                                            }

                                            let mut map = epoch_eth_addresses_arc.write().await;
                                            map.insert(go_epoch, new_eth_addrs);
                                            storage.epoch_timestamp_ms = timestamp_ms;
                                            storage.epoch_base_exec_index = boundary_gei;
                                            tracing::info!(
                                                "✅ [EPOCH-CROSSCHECK] Populated epoch_eth_addresses for epoch {} \
                                                 (timestamp={}ms, boundary_gei={})",
                                                go_epoch, timestamp_ms, boundary_gei
                                            );
                                        }
                                    }
                                }
                                break;
                            }
                            Ok(go_epoch) => {
                                if correction_attempt >= max_correction_retries {
                                    tracing::error!(
                                        "🚨 [EPOCH-CROSSCHECK] Go still at epoch {} after {} retries \
                                         but peers at epoch {}. Proceeding with Go's epoch — \
                                         CommitSyncer will detect and handle the mismatch at runtime.",
                                        go_epoch, correction_attempt, max_peer_epoch
                                    );
                                    break;
                                }
                                tracing::warn!(
                                    "⏳ [EPOCH-CROSSCHECK] Go reports epoch {} (attempt {}/{}), \
                                     peers at {}. Waiting for Go to catch up...",
                                    go_epoch, correction_attempt, max_correction_retries, max_peer_epoch
                                );
                                tokio::time::sleep(std::time::Duration::from_secs(2)).await;
                            }
                            Err(e) => {
                                if correction_attempt >= max_correction_retries {
                                    tracing::error!(
                                        "🚨 [EPOCH-CROSSCHECK] Go RPC failed after {} retries: {}. \
                                         Proceeding with current epoch {}.",
                                        correction_attempt, e, storage.current_epoch
                                    );
                                    break;
                                }
                                tracing::warn!(
                                    "⚠️ [EPOCH-CROSSCHECK] Go RPC failed (attempt {}/{}): {}. Retrying...",
                                    correction_attempt, max_correction_retries, e
                                );
                                tokio::time::sleep(std::time::Duration::from_secs(1)).await;
                            }
                        }
                    }
                } else {
                    tracing::info!(
                        "✅ [EPOCH-CROSSCHECK] Peers confirm epoch {} matches local. No correction needed.",
                        storage.current_epoch
                    );
                }
            }
        }

        if storage.current_epoch > 0 {
            tracing::info!("🔄 [STARTUP] Resolving committee for post-sync epoch {}...", storage.current_epoch);
            
            let max_committee_retries = 3u32;
            let mut validators_opt = None;
            
            for attempt in 1..=max_committee_retries {
                match executor_client_for_proc.get_epoch_boundary_data(storage.current_epoch).await {
                    Ok((_, timestamp_ms, _, v, _, boundary_gei)) if !v.is_empty() => {
                        tracing::info!(
                            "✅ [STARTUP] Got {} validators from Go for epoch {} (attempt {}) with timestamp {}ms, boundary_gei {}",
                            v.len(), storage.current_epoch, attempt, timestamp_ms, boundary_gei
                        );
                        validators_opt = Some(v);
                        storage.epoch_timestamp_ms = timestamp_ms;
                        storage.epoch_base_exec_index = boundary_gei;
                        break;
                    }
                    Ok((_, _, _, v, _, _)) => {
                        tracing::warn!(
                            "⚠️ [STARTUP] Go returned {} validators for epoch {} (attempt {}/{}). Retrying...",
                            v.len(), storage.current_epoch, attempt, max_committee_retries
                        );
                    }
                    Err(e) => {
                        tracing::warn!(
                            "⚠️ [STARTUP] Go failed to provide committee for epoch {} (attempt {}/{}): {}",
                            storage.current_epoch, attempt, max_committee_retries, e
                        );
                    }
                }
                
                if attempt < max_committee_retries {
                    tokio::time::sleep(std::time::Duration::from_millis(500 * attempt as u64)).await;
                }
            }
            
            if validators_opt.is_none() {
                tracing::warn!("⚠️ [STARTUP] Go failed after {} retries. Falling back to peers...", max_committee_retries);
                match executor_client_for_proc.get_safe_epoch_boundary_data_with_force(
                    storage.current_epoch, &config.peer_rpc_addresses, true
                ).await {
                    Ok((_, timestamp_ms, _, v, _, boundary_gei)) if !v.is_empty() => {
                        tracing::info!(
                            "✅ [STARTUP] Got {} validators from peers for epoch {} with timestamp {}ms, boundary_gei {}",
                            v.len(), storage.current_epoch, timestamp_ms, boundary_gei
                        );
                        validators_opt = Some(v);
                        storage.epoch_timestamp_ms = timestamp_ms;
                        storage.epoch_base_exec_index = boundary_gei;
                    }
                    Ok((_, _, _, v, _, _)) => {
                        tracing::error!(
                            "🚨 [STARTUP] Peers returned {} validators for epoch {}. Gracefully degrading to SyncOnly mode.",
                            v.len(), storage.current_epoch
                        );
                    }
                    Err(e2) => {
                        tracing::error!(
                            "🚨 [STARTUP] Cannot determine committee for epoch {}. Peer error: {}. Gracefully degrading to SyncOnly mode.",
                            storage.current_epoch, e2
                        );
                    }
                }
            }
            
            if let Some(validators) = validators_opt {
                if let Ok((committee, new_eth_addrs)) = crate::node::committee::build_committee_with_eth_addresses(
                    validators, 
                    storage.current_epoch
                ) {
                    if !new_eth_addrs.is_empty() {
                        tracing::info!("✅ [STARTUP] Successfully resolved committee with {} authorities for epoch {}", new_eth_addrs.len(), storage.current_epoch);
                        storage.committee = committee.clone();
                        storage.validator_eth_addresses = new_eth_addrs.clone();
                        
                        let own_protocol_pubkey = storage.protocol_keypair.public();
                        let own_index_opt = committee.authorities().find_map(|(idx, auth)| {
                            if auth.protocol_key == own_protocol_pubkey {
                                Some(idx)
                            } else {
                                None
                            }
                        });
                        
                        storage.is_in_committee = own_index_opt.is_some();
                        if let Some(idx) = own_index_opt {
                            storage.own_index = idx;
                            tracing::info!("✅ [IDENTITY] Assigned own_index = {} for epoch {}", idx, storage.current_epoch);
                        } else {
                            tracing::info!("ℹ️ [IDENTITY] Not in committee for epoch {}", storage.current_epoch);
                        }
                        
                        epoch_eth_addresses_arc.write().await.insert(storage.current_epoch, new_eth_addrs);
                    } else {
                        tracing::error!("🚨 [STARTUP] Committee for epoch {} has 0 authorities! Gracefully degrading to SyncOnly mode.", storage.current_epoch);
                        storage.is_in_committee = false;
                    }
                } else {
                    tracing::error!("🚨 [STARTUP] Failed to build committee for epoch {}! Gracefully degrading to SyncOnly mode.", storage.current_epoch);
                    storage.is_in_committee = false;
                }
            } else {
                storage.is_in_committee = false;
            }
        }

        let is_designated_validator = storage.is_in_committee;
        let start_as_validator = is_designated_validator;

        if storage.current_epoch > 0 {
            system_transaction_provider.update_epoch(
                storage.current_epoch,
                storage.epoch_timestamp_ms,
            ).await;
            tracing::info!(
                "🔄 [STARTUP] SystemTransactionProvider re-synced: epoch={}, epoch_timestamp_ms={}",
                storage.current_epoch, storage.epoch_timestamp_ms
            );
        }

        if start_as_validator {
            coordination_hub.set_phase(
                consensus_core::coordination_hub::NodeConsensusPhase::Bootstrapping,
            );

            let guard_hub = coordination_hub.clone();
            let guard_stp = system_transaction_provider.clone();
            tokio::spawn(async move {
                tracing::info!("🛡️ [DAG-GATE] Waiting for CommitSyncer to explicitly complete historical sync...");
                loop {
                    if !guard_hub.is_startup_sync_active() {
                        tracing::info!("✅ [DAG-GATE] CommitSyncer explicitly unlocked node! Notifying SystemTransactionProvider.");
                        guard_stp.notify_healthy();
                        break;
                    }
                    tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;
                }
            });
        } else {
            coordination_hub.set_startup_sync_active(false);
        }

        let commit_consumer_monitor = commit_consumer.monitor();

        let (authority, commit_consumer_holder) = if start_as_validator {
            info!("🚀 Starting consensus authority node (phase=Bootstrapping)...");

            (
                Some(
                    ConsensusAuthority::start(
                        NetworkType::Tonic,
                        storage.epoch_timestamp_ms,
                        storage.epoch_base_exec_index,
                        storage.own_index,
                        storage.committee.clone(),
                        parameters.clone(),
                        protocol_config.clone(),
                        storage.protocol_keypair.clone(),
                        storage.network_keypair.clone(),
                        clock.clone(),
                        transaction_verifier.clone(),
                        commit_consumer,
                        registry.clone(),
                        0,
                        Some(system_transaction_provider.clone()
                            as Arc<dyn SystemTransactionProvider>),
                        None,
                        coordination_hub,
                    )
                    .await,
                ),
                None,
            )
        } else {
            info!("🔄 Starting as sync-only node (not in committee)");
            info!("📡 Keeping commit_consumer alive for SyncOnly mode to prevent channel close");
            (None, Some(commit_consumer))
        };

        if let Some(ref auth) = authority {
            if config.executor_read_enabled && storage.last_global_exec_index > 0 {
                let recovery_store = auth.take_store();
                info!("🔍 [RECOVERY] Initiating block recovery check using the active consensus store instance...");
                if let Err(e) = super::recovery::perform_block_recovery_check(
                    &executor_client_for_proc,
                    storage.last_global_exec_index,
                    storage.epoch_base_exec_index,
                    storage.current_epoch,
                    &recovery_store,
                    config.node_id as u32,
                )
                .await {
                    warn!("⚠️ [STARTUP MINOR] Block recovery check paused (this is normal during cold-start or snapshot restore): {}", e);
                }
            }
        }

        let transaction_client_proxy = authority
            .as_ref()
            .map(|auth| Arc::new(TransactionClientProxy::new(auth.transaction_client())));

        Ok(ConsensusSetup {
            authority,
            dag_has_history,
            commit_consumer_holder,
            transaction_client_proxy,
            executor_client_for_proc,
            current_commit_index,
            pending_transactions_queue,
            committed_transaction_hashes,
            epoch_tx_sender,
            epoch_tx_receiver,
            system_transaction_provider,
            protocol_config,
            parameters,
            clock,
            transaction_verifier,
            tx_recycler,
            is_terminally_failed,
            epoch_eth_addresses_arc,
            commit_consumer_monitor,
        })
    }

    /// Runtime Fork Guard — PERMANENT background block hash verification (Layer 6).
    async fn runtime_fork_guard(
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
                            if local_raw == peer_raw {
                                if next_check_block % 100 == 0 {
                                    tracing::info!(
                                        "✅ [LAYER-6] Block #{} verified ({} bytes match)",
                                        next_check_block, local_raw.len()
                                    );
                                }
                                consecutive_failures = 0;
                            } else {
                                tracing::error!(
                                    "🚨 [LAYER-6] Block #{} MISMATCH DETECTED! \
                                     local_bytes={} peer_bytes={}. \
                                     ENTERING PENDING MODE — will re-verify 3 times before action.",
                                    next_check_block,
                                    local_raw.len(), peer_raw.len()
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
                                                    if retry_local[0].raw_block_bytes == retry_peer_blocks[0].raw_block_bytes {
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
