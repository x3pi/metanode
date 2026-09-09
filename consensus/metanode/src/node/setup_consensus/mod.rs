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

mod fork_guard;
mod startup_sync;
mod verification;

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

        // Per-TX disk payload persistence removed — TxPayloadCache is now
        // in-memory only. See consensus_core::transaction::TX_PAYLOAD_DIR
        // doc comment for why (one-file-per-TX writes were a measured
        // contributor to multi-second stalls under burst load).

        let dag_has_history = Self::check_dag_history(config, storage);

        // Recovery barrier activation logic
        {
            let go_has_state = storage.latest_block_number > 0 || storage.last_handled_commit_index.map_or(false, |c| c > 0);
            if go_has_state {
                coordination_hub.activate_recovery_barrier();
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

        let go_replay_after = Self::get_go_replay_after(config, storage, dag_has_history);

        info!(
            "📊 [STARTUP] CommitConsumerArgs: go_replay_after={} (from authoritative Go/fallback)",
            go_replay_after
        );
        info!(
            "🤝 [HANDSHAKE] Passing last_executed_commit_hash from Go to Rust DAG: {:?}",
            hex::encode(storage.last_executed_commit_hash)
        );

        let (mut commit_consumer, commit_receiver, block_receiver) =
            CommitConsumerArgs::new(
                go_replay_after,
                go_replay_after,
                storage.last_executed_commit_hash,
                storage.last_block_timestamp_ms,
            );
        let current_commit_index = Arc::new(AtomicU32::new(0));
        let is_transitioning = coordination_hub.get_is_transitioning_ref();

        let (pending_transactions_queue, committed_transaction_hashes) =
            Self::load_startup_state(storage).await;

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

        // Stage 4 Conveyor Belt Buffer
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
        .with_lag_thresholds(config.moderate_lag_threshold, config.severe_lag_threshold)
        ;

        let digest_verifier_hub = coordination_hub.clone();
        let mut commit_processor = commit_processor.with_digest_verifier(move |index: u32| {
            if let Some(verifier) = digest_verifier_hub.get_digest_verifier() {
                verifier(index)
            } else {
                None
            }
        });

        let digest_checker_hub = coordination_hub.clone();
        commit_processor = commit_processor.with_digest_data_checker(move || {
            digest_checker_hub.has_digest_data()
        });

        let peer_attest_hub = coordination_hub.clone();
        commit_processor = commit_processor.with_peer_commit_attestation(move |index: u32, digest: [u8; 32]| {
            if let Some(attestor) = peer_attest_hub.get_peer_commit_attestation() {
                attestor(index, digest)
            } else {
                consensus_core::coordination_hub::PeerAttestResult::Insufficient
            }
        });

        let initial_next_expected = if config.executor_read_enabled {
            storage.last_global_exec_index + 1
        } else {
            1
        };

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
                initial_next_expected,
                Some(config.storage_path.clone()),
            );
            client.set_go_lag_handle(system_transaction_provider.go_lag_handle());
            client.set_go_rate_handle(system_transaction_provider.go_rate_handle());
            Arc::new(client)
        } else {
            Arc::new(ExecutorClient::new(
                false,
                false,
                None,
            ))
        };

        let client_clone = executor_client_for_proc.clone();
        commit_consumer = commit_consumer.with_align_executed_commit_hash(move |hash: [u8; 32]| {
            let client_inner = client_clone.clone();
            tokio::spawn(async move {
                tracing::info!("🔄 [ANTI-FORK] Aligner callback triggered, calling Go to set hash to: {}", hex::encode(hash));
                if let Err(e) = client_inner.set_last_executed_commit_hash(hash).await {
                    tracing::error!("❌ [ANTI-FORK] Failed to align Go last executed commit hash: {:?}", e);
                }
            });
        });

        let (startup_total_synced_blocks, startup_local_block) = Self::perform_startup_sync(
            config,
            storage,
            &coordination_hub,
            &mut commit_consumer,
            &mut commit_processor,
            &executor_client_for_proc,
            &shared_last_global_exec_index,
            go_replay_after,
        ).await?;

        Self::run_post_sync_consistency_checks(
            config,
            storage,
            go_replay_after,
            next_expected_commit_index,
            &executor_client_for_proc,
            &mut commit_processor,
            &shared_last_global_exec_index,
        ).await;

        let is_terminally_failed = Arc::new(AtomicBool::new(false));

        let epoch_eth_addresses_arc = commit_processor.get_epoch_eth_addresses_arc().clone();
        let epoch_eth_addresses_notify = commit_processor.get_epoch_eth_addresses_notify().clone();

        Self::crosscheck_epoch_and_resolve_committee(
            config,
            storage,
            &executor_client_for_proc,
            &epoch_eth_addresses_arc,
            &epoch_eth_addresses_notify,
        ).await?;

        coordination_hub.set_startup_go_sync_completed(true);

        let verify_config = config.clone();
        let verify_hub = coordination_hub.clone();
        let verify_client = executor_client_for_proc.clone();
        tokio::spawn(async move {
            Self::perform_post_gate_verification(
                &verify_config,
                &verify_hub,
                &verify_client,
            ).await;
        });

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
        } else {
            coordination_hub.set_startup_sync_active(false);
        }

        let commit_consumer_monitor = commit_consumer.monitor();

        let (lag_alert_sender, lag_alert_receiver) = tokio::sync::mpsc::channel::<
            crate::consensus::commit_processor::lag_monitor::LagAlert,
        >(1000);

        commit_processor = commit_processor.with_lag_alert_sender(lag_alert_sender);

        let parameters = Self::build_consensus_parameters(config, storage)?;
        let mut protocol_config = ProtocolConfig::get_for_max_version_UNSAFE();
        if let Some(limit) = config.consensus_max_num_transactions_in_block {
            protocol_config.set_consensus_max_num_transactions_in_block_for_testing(limit);
        }

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
                        coordination_hub.clone(),
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
                if let Err(e) = crate::node::recovery::perform_block_recovery_check(
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

        let tx_recycler = Arc::new(crate::consensus::tx_recycler::TxRecycler::new());
        info!("♻️ [TX RECYCLER] Created shared TxRecycler instance");

        commit_processor = commit_processor
            .with_executor_client(executor_client_for_proc.clone())
            .with_tx_recycler(tx_recycler.clone())
            .with_committed_transaction_hashes(committed_transaction_hashes.clone());

        Self::spawn_background_tasks(
            config,
            storage,
            &coordination_hub,
            executor_client_for_proc.clone(),
            delivery_rx,
            lag_alert_receiver,
            is_terminally_failed.clone(),
            commit_processor,
            block_receiver,
            startup_total_synced_blocks,
            startup_local_block,
            start_as_validator,
            system_transaction_provider.clone(),
        );

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
            epoch_eth_addresses_notify,
            commit_consumer_monitor,
        })
    }

    /// Check if database has history.
    fn check_dag_history(config: &NodeConfig, storage: &StorageSetup) -> bool {
        let epoch_db = config
            .storage_path
            .join("epochs")
            .join(format!("epoch_{}", storage.current_epoch))
            .join("consensus_db");
        epoch_db.exists()
            && std::fs::read_dir(&epoch_db)
                .map(|mut entries| entries.next().is_some())
                .unwrap_or(false)
    }

    /// Load persisted tx queue and committed transaction hashes.
    async fn load_startup_state(
        storage: &StorageSetup,
    ) -> (
        Arc<tokio::sync::Mutex<Vec<Vec<u8>>>>,
        Arc<dashmap::DashSet<Vec<u8>>>,
    ) {
        let persisted_queue = crate::node::queue::load_transaction_queue_static(&storage.storage_path)
            .await
            .unwrap_or_default();
        if !persisted_queue.is_empty() {
            info!("💾 Loaded {} persisted transactions", persisted_queue.len());
        }
        let pending_transactions_queue = Arc::new(tokio::sync::Mutex::new(persisted_queue));

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
        let dash_set = dashmap::DashSet::new();
        for hash in committed_hashes {
            dash_set.insert(hash);
        }
        let committed_transaction_hashes = Arc::new(dash_set);

        (pending_transactions_queue, committed_transaction_hashes)
    }

    /// Determine go_replay_after index from Go state or backup db
    fn get_go_replay_after(
        config: &NodeConfig,
        storage: &StorageSetup,
        dag_has_history: bool,
    ) -> u32 {
        if config.executor_read_enabled {
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
        }
    }

    /// Configure consensus parameters and database path.
    fn build_consensus_parameters(
        config: &NodeConfig,
        storage: &StorageSetup,
    ) -> Result<consensus_config::Parameters> {
        let mut parameters = consensus_config::Parameters::default();
        parameters.propagation_delay_stop_proposal_threshold = 100;
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

        Ok(parameters)
    }

    /// Consistency validation between Rust and Go, correcting expected indices.
    async fn run_post_sync_consistency_checks(
        config: &NodeConfig,
        storage: &StorageSetup,
        go_replay_after: u32,
        next_expected_commit_index: u32,
        executor_client_for_proc: &ExecutorClient,
        commit_processor: &mut crate::consensus::commit_processor::CommitProcessor,
        shared_last_global_exec_index: &Arc<std::sync::atomic::AtomicU64>,
    ) {
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

                    let cp_gei = shared_last_global_exec_index.load(std::sync::atomic::Ordering::SeqCst) + 1;
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
    }

    /// Cross-check epoch metadata with peers and resolve committee validators.
    async fn crosscheck_epoch_and_resolve_committee(
        config: &NodeConfig,
        storage: &mut StorageSetup,
        executor_client_for_proc: &ExecutorClient,
        epoch_eth_addresses_arc: &Arc<tokio::sync::RwLock<std::collections::HashMap<u64, Vec<Vec<u8>>>>>,
        epoch_eth_addresses_notify: &tokio::sync::Notify,
    ) -> Result<()> {
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
                                            let transition_hash = crate::node::committee_source::calculate_committee_hash(&new_committee);
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
                                                            let peer_hash = crate::node::committee_source::calculate_committee_hash(&peer_comm);
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
                                            tracing::info!("🔍 [LEAK TRACKING] epoch_eth_addresses hiện đang cache: {} epochs", map.len());
                                            drop(map);
                                            epoch_eth_addresses_notify.notify_waiters();
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
                        {
                            let mut map = epoch_eth_addresses_arc.write().await;
                            map.insert(storage.current_epoch, new_eth_addrs);
                            tracing::info!("🔍 [LEAK TRACKING] epoch_eth_addresses hiện đang cache: {} epochs", map.len());
                        }
                        epoch_eth_addresses_notify.notify_waiters();
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
        Ok(())
    }

    /// Spawn background managers, check loops, and tasks.
    #[allow(clippy::too_many_arguments)]
    fn spawn_background_tasks(
        config: &NodeConfig,
        _storage: &StorageSetup,
        coordination_hub: &consensus_core::coordination_hub::ConsensusCoordinationHub,
        executor_client_for_proc: Arc<ExecutorClient>,
        delivery_rx: tokio::sync::mpsc::Receiver<crate::node::block_delivery::ValidatedCommit>,
        lag_alert_receiver: tokio::sync::mpsc::Receiver<crate::consensus::commit_processor::lag_monitor::LagAlert>,
        is_terminally_failed: Arc<AtomicBool>,
        commit_processor: crate::consensus::commit_processor::CommitProcessor,
        block_receiver: tokio::sync::mpsc::UnboundedReceiver<consensus_core::CertifiedBlocksOutput>,
        _startup_total_synced_blocks: u64,
        startup_local_block: u64,
        start_as_validator: bool,
        system_transaction_provider: Arc<DefaultSystemTransactionProvider>,
    ) {
        // Spawn Lag Monitor Receiver
        let mut lag_rx = lag_alert_receiver;
        tokio::spawn(async move {
            while let Some(alert) = lag_rx.recv().await {
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

        // Spawn Commit Processor Run loop
        let failed_processor = is_terminally_failed.clone();
        let cp = commit_processor;
        tokio::spawn(async move {
            if let Err(e) = cp.run().await {
                tracing::error!("❌ [STATION 3: PROCESSOR] Fatal Error: {}", e);
                failed_processor.store(true, Ordering::SeqCst);
            } else {
                tracing::info!("🛑 [STATION 3: PROCESSOR] Gracefully Exited (Expected upon EndOfEpoch).");
            }
        });

        // Spawn Block Receiver loop
        let mut br = block_receiver;
        tokio::spawn(async move {
            while let Some(output) = br.recv().await {
                tracing::debug!("Received {} certified blocks", output.blocks.len());
            }
        });

        // Spawn Block Delivery Manager
        let peer_addrs = config.peer_rpc_addresses.clone();
        let executor_client_for_manager = executor_client_for_proc.clone();
        let del_rx = delivery_rx;
        tokio::spawn(async move {
            let metrics = std::sync::Arc::new(crate::node::sync_metrics::SyncMetrics::new(prometheus::default_registry()));
            let manager = crate::node::block_delivery::BlockDeliveryManager::new(
                executor_client_for_manager,
                del_rx,
                peer_addrs,
                metrics,
            );
            manager.run().await;
            tracing::info!("🛑 [STATION 4: DELIVERY] BlockDeliveryManager gracefully exited (expected on Epoch Transition).");
        });

        // Spawn Health Check
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

        // Spawn DAG-GATE Validator Sync Monitor
        if start_as_validator {
            let guard_hub = coordination_hub.clone();
            let guard_stp = system_transaction_provider;
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
        }

        // Spawn permanent background network fork guard
        if !config.peer_rpc_addresses.is_empty() {
            let guard_client = executor_client_for_proc.clone();
            let guard_peers = config.peer_rpc_addresses.clone();
            let guard_start_block = startup_local_block;
            let guard_terminally_failed = is_terminally_failed.clone();
            tokio::spawn(async move {
                Self::runtime_fork_guard(guard_client, guard_peers, guard_start_block, guard_terminally_failed).await;
            });
        }
    }
}
