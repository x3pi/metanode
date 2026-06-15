// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{sync::Arc, time::Instant};

use consensus_config::{AuthorityIndex, Committee, NetworkKeyPair, Parameters, ProtocolKeyPair};
use itertools::Itertools;
use meta_protocol_config::ProtocolConfig;
use parking_lot::RwLock;
use prometheus::Registry;
use tokio::{sync::broadcast, task::JoinHandle};
use tracing::info;

use crate::{
    adaptive_delay::AdaptiveDelayState,
    authority_service::AuthorityService,
    block_manager::BlockManager,
    block_verifier::SignedBlockVerifier,
    commit_observer::CommitObserver,
    commit_syncer::CommitSyncerHandle,
    commit_vote_monitor::CommitVoteMonitor,
    context::{Clock, Context},
    core::{Core, CoreSignals},
    core_thread::{ChannelCoreThreadDispatcher, CoreThreadHandle},
    dag_state::DagState,
    leader_schedule::LeaderSchedule,
    leader_timeout::{LeaderTimeoutTask, LeaderTimeoutTaskHandle},
    legacy_store::LegacyEpochStoreManager,
    metrics::initialise_metrics,
    network::{tonic_network::TonicManager, NetworkManager},
    proposed_block_handler::ProposedBlockHandler,
    round_prober::{RoundProber, RoundProberHandle},
    round_tracker::PeerRoundTracker,
    storage::rocksdb_store::RocksDBStore,
    subscriber::Subscriber,
    synchronizer::{Synchronizer, SynchronizerHandle},
    system_transaction_provider::SystemTransactionProvider,
    transaction::{TransactionClient, TransactionConsumer, TransactionVerifier},
    transaction_certifier::TransactionCertifier,
    CommitConsumerArgs,
};
use crate::dag_state_actor::DagStateActor;

/// ConsensusAuthority is used by Sui to manage the lifetime of AuthorityNode.
/// It hides the details of the implementation from the caller, MysticetiManager.
#[allow(private_interfaces)]
pub enum ConsensusAuthority {
    WithTonic(Option<AuthorityNode<TonicManager>>),
}

impl ConsensusAuthority {
    #[allow(clippy::too_many_arguments)]
    pub async fn start(
        network_type: NetworkType,
        epoch_start_timestamp_ms: u64,
        epoch_base_index: u64,
        own_index: AuthorityIndex,
        committee: Committee,
        parameters: Parameters,
        protocol_config: ProtocolConfig,
        protocol_keypair: ProtocolKeyPair,
        network_keypair: NetworkKeyPair,
        clock: Arc<Clock>,
        transaction_verifier: Arc<dyn TransactionVerifier>,
        commit_consumer: CommitConsumerArgs,
        registry: Registry,
        // A counter that keeps track of how many times the authority node has been booted while the binary
        // or the component that is calling the `ConsensusAuthority` has been running. It's mostly useful to
        // make decisions on whether amnesia recovery should run or not. When `boot_counter` is 0, then `ConsensusAuthority`
        // will initiate the process of amnesia recovery if that's enabled in the parameters.
        boot_counter: u64,
        system_transaction_provider: Option<Arc<dyn SystemTransactionProvider>>,
        // Legacy store manager from ConsensusNode, to avoid re-opening locked RocksDB files
        legacy_store_manager: Option<Arc<LegacyEpochStoreManager>>,
        coordination_hub: crate::coordination_hub::ConsensusCoordinationHub,
    ) -> Self {
        match network_type {
            NetworkType::Tonic => {
                let authority = AuthorityNode::start(
                    epoch_start_timestamp_ms,
                    epoch_base_index,
                    own_index,
                    committee,
                    parameters,
                    protocol_config,
                    protocol_keypair,
                    network_keypair,
                    clock,
                    transaction_verifier,
                    commit_consumer,
                    registry,
                    boot_counter,
                    system_transaction_provider,
                    legacy_store_manager,
                    coordination_hub,
                )
                .await;
                Self::WithTonic(Some(authority))
            }
        }
    }

    pub async fn stop(mut self) {
        match &mut self {
            Self::WithTonic(authority_opt) => {
                if let Some(authority) = authority_opt.take() {
                    authority.stop().await;
                }
            }
        }
    }

    pub fn transaction_client(&self) -> Arc<TransactionClient> {
        match self {
            Self::WithTonic(Some(authority)) => authority.transaction_client(),
            Self::WithTonic(None) => {
                panic!("transaction_client() called after authority was stopped — caller must check lifecycle before access")
            }
        }
    }

    #[cfg(test)]
    fn context(&self) -> &Arc<Context> {
        match self {
            Self::WithTonic(Some(authority)) => &authority.context,
            Self::WithTonic(None) => {
                panic!("context() called after authority was stopped — caller must check lifecycle before access")
            }
        }
    }

    /// Extract the store for use in LegacyEpochStoreManager.
    /// This should be called before stop() to preserve the store for legacy sync.
    pub fn take_store(&self) -> Arc<dyn crate::storage::Store> {
        match self {
            Self::WithTonic(Some(authority)) => authority.store.clone(),
            Self::WithTonic(None) => {
                panic!("take_store() called after authority was stopped — caller must check lifecycle before access")
            }
        }
    }

    pub fn is_alive(&self) -> bool {
        match self {
            Self::WithTonic(Some(authority)) => authority.is_alive(),
            Self::WithTonic(None) => false,
        }
    }
}

// ============================================================================
// DIAGNOSTIC: Drop implementation to track unexpected authority drops
// ============================================================================
impl Drop for ConsensusAuthority {
    fn drop(&mut self) {
        // Only log if authority is still present (unexpected drop)
        // If stop() was called, it will be None
        match self {
            Self::WithTonic(Some(authority)) => {
                let epoch = authority.context.committee.epoch();
                tracing::warn!(
                    "🔴 [CONSENSUS AUTHORITY DROP] Authority being dropped unexpectedly! epoch={}",
                    epoch
                );
                // Capture backtrace to identify the source of the drop
                let backtrace = std::backtrace::Backtrace::capture();
                if backtrace.status() == std::backtrace::BacktraceStatus::Captured {
                    tracing::warn!("🔴 [CONSENSUS AUTHORITY DROP] Backtrace:\n{}", backtrace);
                }
            }
            Self::WithTonic(None) => {
                // Normal case - stop() was called
            }
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum NetworkType {
    Tonic,
}

pub(crate) struct AuthorityNode<N>
where
    N: NetworkManager<AuthorityService<ChannelCoreThreadDispatcher>>,
{
    context: Arc<Context>,
    start_time: Instant,
    transaction_client: Arc<TransactionClient>,
    synchronizer: Arc<SynchronizerHandle>,
    /// Store reference for potential extraction during epoch transition.
    /// This allows the store to be kept open for legacy sync even after authority stops.
    store: Arc<dyn crate::storage::Store>,

    commit_syncer_handle: CommitSyncerHandle,
    round_prober_handle: RoundProberHandle,
    proposed_block_handler: JoinHandle<()>,
    leader_timeout_handle: LeaderTimeoutTaskHandle,
    core_thread_handle: CoreThreadHandle,
    subscriber: Subscriber<N::Client, AuthorityService<ChannelCoreThreadDispatcher>>,
    network_manager: N,
    /// Keeper for broadcast sender to prevent channel from closing during async spawning.
    /// This ensures the broadcast channel stays open until all components are fully initialized.
    /// Without this, race conditions can cause ProposedBlockHandler to receive "channel closed" immediately.
    #[allow(dead_code)]
    broadcast_sender_keeper: broadcast::Sender<crate::block::ExtendedBlock>,
}

impl<N> AuthorityNode<N>
where
    N: NetworkManager<AuthorityService<ChannelCoreThreadDispatcher>>,
{
    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn start(
        epoch_start_timestamp_ms: u64,
        epoch_base_index: u64,
        own_index: AuthorityIndex,
        committee: Committee,
        parameters: Parameters,
        protocol_config: ProtocolConfig,
        // To avoid accidentally leaking the private key, the protocol key pair should only be
        // kept in Core.
        protocol_keypair: ProtocolKeyPair,
        network_keypair: NetworkKeyPair,
        clock: Arc<Clock>,
        transaction_verifier: Arc<dyn TransactionVerifier>,
        commit_consumer: CommitConsumerArgs,
        registry: Registry,
        boot_counter: u64,
        system_transaction_provider: Option<Arc<dyn SystemTransactionProvider>>,
        // Legacy store manager passed from ConsensusNode to avoid RocksDB lock conflicts
        // during epoch transitions. If None, no legacy stores will be available.
        existing_legacy_store_manager: Option<Arc<LegacyEpochStoreManager>>,
        coordination_hub: crate::coordination_hub::ConsensusCoordinationHub,
    ) -> Self {
        assert!(
            committee.is_valid_index(own_index),
            "Invalid own index {}",
            own_index
        );
        let own_hostname = committee.authority(own_index).hostname.clone();
        info!(
            "Starting consensus authority {} {}, {:?}, epoch start timestamp {}, boot counter {}, replaying after commit index {}, consumer last processed commit index {}",
            own_index,
            own_hostname,
            protocol_config.version,
            epoch_start_timestamp_ms,
            boot_counter,
            commit_consumer.replay_after_commit_index,
            commit_consumer.consumer_last_processed_commit_index
        );
        info!(
            "Consensus authorities: {}",
            committee
                .authorities()
                .map(|(i, a)| format!("{}: {}", i, a.hostname))
                .join(", ")
        );
        info!("Consensus parameters: {:?}", parameters);
        info!("Consensus committee: {:?}", committee);

        // Save min_round_delay before moving parameters into Context
        let min_round_delay_ms = parameters.min_round_delay.as_millis() as u64;
        let adaptive_delay_enabled = parameters.adaptive_delay_enabled;

        let context = Arc::new(Context::new(
            epoch_start_timestamp_ms,
            own_index,
            committee,
            parameters,
            protocol_config,
            initialise_metrics(registry),
            clock,
        ));
        let start_time = Instant::now();

        context
            .metrics
            .node_metrics
            .authority_index
            .with_label_values(&[&own_hostname])
            .set(context.own_index.value() as i64);
        context
            .metrics
            .node_metrics
            .protocol_version
            .set(context.protocol_config.version.as_u64() as i64);

        let (tx_client, tx_receiver) = TransactionClient::new(context.clone());
        let tx_consumer = TransactionConsumer::new(tx_receiver, context.clone());

        let (core_signals, signals_receivers) = CoreSignals::new(context.clone());

        // CRITICAL FIX: Get the broadcast sender keeper IMMEDIATELY after CoreSignals creation
        // and BEFORE any tasks are spawned. This prevents the broadcast channel from closing
        // if Core is dropped before CoreThread starts, which would cause ProposedBlockHandler
        // to exit immediately with "Broadcast channel CLOSED" error.
        // The keeper must be obtained here because:
        // 1. ProposedBlockHandler is spawned at line ~247 with signals_receivers.block_broadcast_receiver()
        // 2. The receiver subscribes to the broadcast channel
        // 3. If all strong senders are dropped before the task runs, the channel closes
        // 4. Holding the keeper here ensures at least one strong sender survives
        let broadcast_sender_keeper = signals_receivers.broadcast_sender_keeper();
        info!(
            "📡 [AUTHORITY NODE] Broadcast sender keeper obtained, receiver_count={}, total_senders_should_be=3",
            broadcast_sender_keeper.receiver_count()
        );

        let mut network_manager = N::new(context.clone(), network_keypair);
        let network_client = network_manager.client();

        let store_path = context.parameters.db_path.as_path().to_str().expect(
            "consensus db_path must be valid UTF-8 — check Parameters::db_path configuration",
        );
        let store = Arc::new(RocksDBStore::new(store_path));
        let dag_state = DagState::new(context.clone(), store.clone());
        // REMOVED: dag_state.set_last_commit_timestamp_ms(commit_consumer.last_block_timestamp_ms);
        // This was overwriting the accurate ms-precision timestamp from Rust's DagState 
        // with Go's second-precision timestamp, causing fork divergence.

        // CRITICAL FIX: Align the CommitConsumerMonitor with the Go execution progress.
        // On snapshot restart: DAG might be at 1005, but Go has processed up to replay_after (e.g. 1000).
        // We MUST set highest_handled_commit = go_handled so that CommitObserver replays 1001-1005 to Go!
        // If we used max(), Go would permanently miss those commits, causing state divergence.
        let go_handled = commit_consumer.replay_after_commit_index;
        let dag_handled = dag_state.last_commit_index();
        let effective_handled = go_handled.max(dag_handled); // kept for logging/logic
        commit_consumer.monitor().set_highest_handled_commit(go_handled);
        info!(
            "📊 [STARTUP] CommitConsumerMonitor aligned: go_handled={}, dag_handled={}, effective={}",
            go_handled, dag_handled, effective_handled
        );

        // ═══════════════════════════════════════════════════════════════════
        // UNIFIED RECOVERY-GUARD POLICY (DAG-State-Aware & Go-State-Aware):
        //
        // The RECOVERY-GUARD prevents the local committer from evaluating a
        // sparse DAG that might produce divergent commits (fork).
        //
        // Decision rule:
        //   dag_commit == 0 && go_handled == 0 → TRUE GENESIS / EPOCH START
        //     → Auto-unlock (safe to participate).
        //   dag_commit > 0 OR go_handled > 0   → RECOVERY / RESTART
        //     → Keep locked until CommitSyncer verifies we are caught up.
        //
        // Scenarios:
        //   - Genesis: dag=0, go=0 → unlocked → no genesis deadlock
        //   - Epoch transition: dag=0, go=0 → unlocked → no epoch deadlock
        //   - Cold restart: dag>0, go>0 → locked → density proof required
        //   - Snapshot restore: dag=0, go>0 → locked → density proof required
        // ═══════════════════════════════════════════════════════════════════
        let dag_commit_index = dag_state.last_commit_index();
        if dag_commit_index == 0 && go_handled == 0 {
            info!(
                "🟢 [GUARD-POLICY] True Genesis (dag=0, go=0): RECOVERY-GUARD disabled. \
                 All nodes start synchronized — no fork risk."
            );
        } else {
            info!(
                "🔴 [GUARD-POLICY] Recovery Mode (dag={}, go={}): RECOVERY-GUARD active. \
                 Waiting for 5 network CertifiedCommits to prove density before local evaluation.",
                dag_commit_index, go_handled
            );
        }

        // NOTE: Commit index alignment is now handled by ConsensusCoordinationHub
        // during the FastForwarding phase (see commit_syncer.rs). 
        // We intentionally do NOT align here because we need real network data
        // (commits from peers) to determine the correct baseline.
        let dag_state = Arc::new(RwLock::new(dag_state));

        // Spawn the DagState single-writer actor.
        // All critical writes (baseline injection, network reset) go through this channel
        // instead of calling dag_state.write() directly — eliminating write-side deadlocks.
        let dag_state_writer = DagStateActor::spawn(dag_state.clone());

        let block_verifier = Arc::new(SignedBlockVerifier::new(
            context.clone(),
            transaction_verifier,
        ));

        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            commit_consumer.block_sender.clone(),
        );

        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));

        let proposed_block_handler = ProposedBlockHandler::new(
            context.clone(),
            signals_receivers.block_broadcast_receiver(),
            transaction_certifier.clone(),
            coordination_hub.clone(),
            commit_vote_monitor.clone(),
        );

        info!(
            "📡 [AUTHORITY NODE] About to spawn ProposedBlockHandler, keeper receiver_count={}",
            broadcast_sender_keeper.receiver_count()
        );
        let proposed_block_handler =
            tokio::spawn(async move { 
                let mut handler = proposed_block_handler;
                handler.run().await 
            });

        let sync_last_known_own_block = boot_counter == 0
            && dag_state.read().highest_accepted_round() == 0
            && !context
                .parameters
                .sync_last_known_own_block_timeout
                .is_zero();
        info!("Sync last known own block: {sync_last_known_own_block}");

        let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());

        let leader_schedule = Arc::new(LeaderSchedule::from_store(
            context.clone(),
            dag_state.clone(),
        ));

        let commit_consumer_monitor = commit_consumer.monitor();
        let commit_observer = CommitObserver::new(
            context.clone(),
            commit_consumer,
            dag_state.clone(),
            dag_state_writer.clone(),
            transaction_certifier.clone(),
            leader_schedule.clone(),
            epoch_base_index,
        )
        .await;

        let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));

        let adaptive_delay_state = Arc::new(AdaptiveDelayState::new(min_round_delay_ms, adaptive_delay_enabled));
        info!("Adaptive delay enabled: base_delay={}ms", min_round_delay_ms);

        let core = Core::new(
            context.clone(),
            leader_schedule,
            tx_consumer,
            transaction_certifier.clone(),
            block_manager,
            commit_observer,
            core_signals,
            protocol_keypair,
            dag_state.clone(),
            dag_state_writer.clone(),
            sync_last_known_own_block,
            round_tracker.clone(),
            Some(adaptive_delay_state.clone()),
            system_transaction_provider, // System transaction provider for Sui-style epoch transition
            coordination_hub.clone(),
        );

        let (core_dispatcher, core_thread_handle) =
            ChannelCoreThreadDispatcher::start(context.clone(), &dag_state, core);
        let core_dispatcher = Arc::new(core_dispatcher);
        let leader_timeout_handle =
            LeaderTimeoutTask::start(core_dispatcher.clone(), &signals_receivers, context.clone());

        // DIGEST-GATE: Wire CommitVoteMonitor.quorum_commit_digest() into CoordinationHub
        // so CommitProcessor can verify local commit digests against network quorum.
        {
            let monitor_ref = commit_vote_monitor.clone();
            coordination_hub.set_digest_verifier(move |index: u32| {
                monitor_ref
                    .quorum_commit_digest(index)
                    .map(|d| d.into_inner())
            });
        }

        // COLD-START-FIX (May 2026): Wire CommitVoteMonitor.has_any_digest_data() into
        // CoordinationHub so CommitProcessor can detect true cold-start conditions.
        // This is the definitive fix for the epoch transition deadlock where CommitSyncer
        // sets quorum_commit_index > 0 from peer queries BEFORE CommitVoteMonitor has
        // received any actual digest votes, permanently disabling COLD-START-BYPASS.
        {
            let monitor_ref = commit_vote_monitor.clone();
            coordination_hub.set_digest_data_checker(move || {
                monitor_ref.has_any_digest_data()
            });
        }

        // ZERO-TIMEOUT PEER ATTESTATION (May 2026):
        // Wire CommitVoteMonitor into CoordinationHub as the peer attestation callback.
        // This replaces ALL timeout-based bypass mechanisms (COLD-START-BYPASS 10s,
        // SUSTAINED-LOAD-BYPASS 5s) with a data-driven check.
        //
        // Logic:
        //   1. No digest data at all + no votes for this index → TRUE cold-start
        //      → all nodes have identical empty DAGs → deterministic → Ok
        //   2. No digest data + some votes exist → peers are starting to vote → Insufficient
        //   3. Quorum agrees with local digest → Ok
        //   4. Quorum disagrees → Conflict (local commit is wrong)
        //   5. No quorum yet → Insufficient (wait for more votes)
        {
            let monitor_ref = commit_vote_monitor.clone();
            let ctx_ref = context.clone();
            coordination_hub.set_peer_commit_attestation(move |index: u32, local_digest: [u8; 32]| {
                use crate::coordination_hub::PeerAttestResult;

                // First check: does quorum_commit_digest have a definitive answer?
                if let Some(quorum_digest) = monitor_ref.quorum_commit_digest(index) {
                    if quorum_digest.into_inner() == local_digest {
                        return PeerAttestResult::Ok; // 2f+1 agree with us
                    } else {
                        return PeerAttestResult::Conflict; // 2f+1 disagree
                    }
                }

                // No quorum digest yet. Check vote counts for this index.
                let (total_stake, best_entry) = monitor_ref.vote_count_for_index(index);

                if total_stake == 0 {
                    // No peer has voted for this index at all.
                    // Check if this is a TRUE cold-start (no digest data anywhere)
                    if !monitor_ref.has_any_digest_data() {
                        // TRUE COLD-START: No digest votes exist in the entire monitor.
                        // This means ALL nodes are in the same state — fresh epoch.
                        // The local commit is deterministic (same DAG → same commits).
                        // Safe to dispatch without timeout.
                        PeerAttestResult::Ok
                    } else {
                        // Digest data exists for OTHER indices but not this one.
                        // This could mean: GC'd (too old) or peers haven't voted yet.
                        // Stay pending until peers catch up.
                        PeerAttestResult::Insufficient
                    }
                } else if let Some((best_digest, best_stake)) = best_entry {
                    // Some peers have voted. Check if majority matches local digest.
                    let quorum_threshold = ctx_ref.committee.quorum_threshold();
                    if best_digest.into_inner() == local_digest {
                        // Majority matches us but hasn't reached quorum yet.
                        // If we have validity threshold (f+1) agreement, it's very likely safe,
                        // but we still wait for full quorum to be absolutely certain.
                        if best_stake >= quorum_threshold {
                            PeerAttestResult::Ok // Should have been caught above, but defensive
                        } else {
                            PeerAttestResult::Insufficient // Wait for full quorum
                        }
                    } else {
                        // Majority of votes so far disagree with us.
                        // If the disagreeing stake is already >= quorum, it's definitive.
                        if best_stake >= quorum_threshold {
                            PeerAttestResult::Conflict
                        } else {
                            // Sub-quorum disagreement — could flip. Wait.
                            PeerAttestResult::Insufficient
                        }
                    }
                } else {
                    PeerAttestResult::Insufficient
                }
            });
        }

        let synchronizer = Synchronizer::start(
            network_client.clone(),
            context.clone(),
            core_dispatcher.clone(),
            commit_vote_monitor.clone(),
            block_verifier.clone(),
            transaction_certifier.clone(),
            dag_state.clone(),
            sync_last_known_own_block,
        );

        let commit_syncer_handle = crate::commit_syncer::CommitSyncerSupervisor::start(
            context.clone(),
            core_dispatcher.clone(),
            commit_vote_monitor.clone(),
            commit_consumer_monitor.clone(),
            block_verifier.clone(),
            transaction_certifier.clone(),
            network_client.clone(),
            dag_state.clone(),
            coordination_hub.clone(),
            Some(adaptive_delay_state.clone()),
            dag_state_writer,
        );

        let round_prober_handle = RoundProber::new(
            context.clone(),
            core_dispatcher.clone(),
            round_tracker.clone(),
            dag_state.clone(),
            network_client.clone(),
        )
        .start();

        // Use existing LegacyEpochStoreManager if passed, otherwise None.
        // CRITICAL: Do NOT create new LegacyEpochStoreManager here during epoch transitions!
        // The old epoch's RocksDB may still be locked by the same process.
        // Instead, the LegacyEpochStoreManager is managed by ConsensusNode and stores are
        // added during epoch transitions after the old authority is stopped.
        let legacy_store_manager = if let Some(mgr) = existing_legacy_store_manager {
            info!(
                "✅ [AUTHORITY START] Using existing LegacyEpochStoreManager with {} epochs",
                mgr.store_count()
            );
            Some(mgr)
        } else {
            info!("ℹ️ [AUTHORITY START] No existing LegacyEpochStoreManager provided");
            None
        };

        let network_service = Arc::new(AuthorityService::new(
            context.clone(),
            block_verifier,
            commit_vote_monitor,
            round_tracker.clone(),
            synchronizer.clone(),
            core_dispatcher.clone(),
            signals_receivers.block_broadcast_receiver(),
            transaction_certifier,
            dag_state.clone(),
            store.clone(),
            None,                 // epoch_change_processor
            legacy_store_manager, // Pass initialized manager
            epoch_base_index,     // CRITICAL: Pass epoch_base for cold-start fallback
        ));

        let subscriber = {
            let s = Subscriber::new(
                context.clone(),
                network_client,
                network_service.clone(),
                dag_state,
                Some(coordination_hub),
            );
            for (peer, _) in context.committee.authorities() {
                if peer != context.own_index {
                    s.subscribe(peer);
                }
            }
            s
        };

        network_manager.install_service(network_service).await;


        info!(
            "✅ [AUTHORITY NODE] Consensus authority started, took {:?}",
            start_time.elapsed()
        );

        Self {
            context,
            start_time,
            transaction_client: Arc::new(tx_client),
            synchronizer,
            store,
            commit_syncer_handle,
            round_prober_handle,
            proposed_block_handler,
            leader_timeout_handle,
            core_thread_handle,
            subscriber,
            network_manager,
            broadcast_sender_keeper,
        }
    }

    pub(crate) async fn stop(mut self) {
        info!(
            "Stopping authority. Total run time: {:?}",
            self.start_time.elapsed()
        );

        // First shutdown components calling into Core.
        if let Err(e) = self.synchronizer.stop().await {
            if e.is_panic() {
                tracing::error!(
                    "🚨 [AUTHORITY STOP] Synchronizer panicked during shutdown: {:?}",
                    e
                );
                // Do NOT resume_unwind — that would propagate an abort() across FFI
            }
            // Cancellation can happen during an epoch transition where we intentionally abort in-flight tasks.
            // Keep it at DEBUG to avoid alarming operators.
            tracing::debug!("Synchronizer stop returned error during shutdown: {:?}", e);
        };
        self.commit_syncer_handle.stop().await;
        self.round_prober_handle.stop().await;
        self.proposed_block_handler.abort();
        self.leader_timeout_handle.stop().await;
        // Stop block subscriptions before stopping Core to prevent sending blocks to closed channel.
        self.subscriber.stop();
        // Shutdown Core to stop block productions and broadcast.
        self.core_thread_handle.stop().await;
        self.network_manager.stop().await;

        self.context
            .metrics
            .node_metrics
            .uptime
            .observe(self.start_time.elapsed().as_secs_f64());
    }

    pub(crate) fn is_alive(&self) -> bool {
        let syncer_alive = self.commit_syncer_handle.is_alive();
        let core_alive = self.core_thread_handle.is_alive();
        
        if !syncer_alive || !core_alive {
            tracing::warn!(
                "🔴 [AUTHORITY LIVENESS] Node internal task crashed! CommitSyncer alive: {}, CoreThread alive: {}",
                syncer_alive, core_alive
            );
            false
        } else {
            true
        }
    }

    pub(crate) fn transaction_client(&self) -> Arc<TransactionClient> {
        self.transaction_client.clone()
    }
}


#[cfg(test)]
mod tests;
