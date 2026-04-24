// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

//! CommitSyncer implements efficient synchronization of committed data.
//!
//! During the operation of a committee of authorities for consensus, one or more authorities
//! can fall behind the quorum in their received and accepted blocks. This can happen due to
//! network disruptions, host crash, or other reasons. Authorities fell behind need to catch up to
//! the quorum to be able to vote on the latest leaders. So efficient synchronization is necessary
//! to minimize the impact of temporary disruptions and maintain smooth operations of the network.
//!  
//! CommitSyncer achieves efficient synchronization by relying on the following: when blocks
//! are included in commits with >= 2f+1 certifiers by stake, these blocks must have passed
//! verifications on some honest validators, so re-verifying them is unnecessary. In fact, the
//! quorum certified commits themselves can be trusted to be sent to Sui directly, but for
//! simplicity this is not done. Blocks from trusted commits still go through Core and committer.
//!
//! Another way CommitSyncer improves the efficiency of synchronization is parallel fetching:
//! commits have a simple dependency graph (linear), so it is easy to fetch ranges of commits
//! in parallel.
//!
//! Commit synchronization is an expensive operation, involving transferring large amount of data via
//! the network. And it is not on the critical path of block processing. So the heuristics for
//! synchronization, including triggers and retries, should be chosen to favor throughput and
//! efficient resource usage, over faster reactions.

use std::{
    collections::{BTreeMap, BTreeSet},
    sync::Arc,
    time::Duration,
};

// ═══════════════════════════════════════════════════════════════════════════════
// STATE MACHINE: Explicit state management for synchronization lifecycle
// Replaces scattered boolean flags (is_sync_mode, is_severe_lag, bootstrap_*)
// with a single source of truth for node sync state.
// ═══════════════════════════════════════════════════════════════════════════════
// ═══════════════════════════════════════════════════════════════════════════════
// STATE MACHINE: Explicit state management mapped through ConsensusCoordinationHub
// ═══════════════════════════════════════════════════════════════════════════════

use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::BlockRef;
use futures::{stream::FuturesOrdered, StreamExt as _};
use itertools::Itertools as _;
use mysten_metrics::spawn_logged_monitored_task;
use parking_lot::RwLock;
use rand::{prelude::SliceRandom as _, rngs::ThreadRng};
use tokio::{
    runtime::Handle,
    sync::oneshot,
    task::{JoinHandle, JoinSet},
    time::{sleep, MissedTickBehavior},
};
use tracing::{debug, error, info, warn};

use crate::{
    adaptive_delay::AdaptiveDelayState,
    block::{BlockAPI, SignedBlock, VerifiedBlock},
    block_verifier::BlockVerifier,
    commit::{
        CertifiedCommit, CertifiedCommits, Commit, CommitAPI as _, CommitDigest, CommitRange,
        CommitRef, TrustedCommit,
    },
    commit_vote_monitor::CommitVoteMonitor,
    context::Context,
    core_thread::CoreThreadDispatcher,
    dag_state::DagState,
    error::{ConsensusError, ConsensusResult},
    network::NetworkClient,
    stake_aggregator::{QuorumThreshold, StakeAggregator},
    transaction_certifier::TransactionCertifier,
    CommitConsumerMonitor, CommitIndex,
};

// Handle to stop the CommitSyncer loop.
pub(crate) struct CommitSyncerHandle {
    schedule_task: JoinHandle<()>,
    tx_shutdown: oneshot::Sender<()>,
}

impl CommitSyncerHandle {
    pub(crate) async fn stop(self) {
        let _ = self.tx_shutdown.send(());
        // Do not abort schedule task, which waits for fetches to shut down.
        if let Err(e) = self.schedule_task.await {
            if e.is_panic() {
                std::panic::resume_unwind(e.into_panic());
            }
        }
    }

    pub(crate) fn is_alive(&self) -> bool {
        !self.schedule_task.is_finished()
    }
}

pub(crate) struct CommitSyncer<C: NetworkClient> {
    // States shared by scheduler and fetch tasks.

    // Shared components wrapper.
    inner: Arc<Inner<C>>,

    // States only used by the scheduler.

    // Inflight requests to fetch commits from different authorities.
    inflight_fetches: JoinSet<(u32, CertifiedCommits)>,
    // Additional ranges of commits to fetch.
    pending_fetches: BTreeSet<CommitRange>,
    // Fetched commits and blocks by commit range.
    fetched_ranges: BTreeMap<CommitRange, CertifiedCommits>,
    // Highest commit index among inflight and pending fetches.
    // Used to determine the start of new ranges to be fetched.
    highest_scheduled_index: Option<CommitIndex>,
    // Highest index among fetched commits, after commits and blocks are verified.
    // Used for metrics.
    highest_fetched_commit_index: CommitIndex,
    // The commit index that is the max of highest local commit index and commit index inflight to Core.
    // Used to determine if fetched blocks can be sent to Core without gaps.
    synced_commit_index: CommitIndex,

    // --- log throttling ---
    last_schedule_log_at: tokio::time::Instant,
    last_logged_quorum_commit_index: CommitIndex,
    last_logged_local_commit_index: CommitIndex,

    // --- state coordination ---
    coordination_hub: crate::coordination_hub::ConsensusCoordinationHub,
    last_state_log_at: tokio::time::Instant,

    // --- adaptive delay ---
    adaptive_delay_state: Option<Arc<AdaptiveDelayState>>,
}

impl<C: NetworkClient> CommitSyncer<C> {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        context: Arc<Context>,
        core_thread_dispatcher: Arc<dyn CoreThreadDispatcher>,
        commit_vote_monitor: Arc<CommitVoteMonitor>,
        commit_consumer_monitor: Arc<CommitConsumerMonitor>,
        block_verifier: Arc<dyn BlockVerifier>,
        transaction_certifier: TransactionCertifier,
        network_client: Arc<C>,
        dag_state: Arc<RwLock<DagState>>,
        coordination_hub: crate::coordination_hub::ConsensusCoordinationHub,
        adaptive_delay_state: Option<Arc<AdaptiveDelayState>>,
    ) -> Self {
        let inner = Arc::new(Inner {
            context,
            core_thread_dispatcher,
            commit_vote_monitor,
            commit_consumer_monitor,
            block_verifier,
            transaction_certifier,
            network_client,
            dag_state,
        });
        let synced_commit_index = inner.dag_state.read().last_commit_index();
        
        CommitSyncer {
            inner,
            inflight_fetches: JoinSet::new(),
            pending_fetches: BTreeSet::new(),
            fetched_ranges: BTreeMap::new(),
            highest_scheduled_index: None,
            highest_fetched_commit_index: 0,
            synced_commit_index,
            last_schedule_log_at: tokio::time::Instant::now() - Duration::from_secs(300),
            last_logged_quorum_commit_index: 0,
            last_logged_local_commit_index: 0,
            coordination_hub,
            last_state_log_at: tokio::time::Instant::now(),
            adaptive_delay_state,
        }
    }

    pub(crate) fn start(self) -> CommitSyncerHandle {
        let (tx_shutdown, rx_shutdown) = oneshot::channel();
        let schedule_task = spawn_logged_monitored_task!(self.schedule_loop(rx_shutdown), "commit_syncer_loop");
        CommitSyncerHandle {
            schedule_task,
            tx_shutdown,
        }
    }
}

/// A Supervisor that wraps CommitSyncer initialization and monitors its task.
/// If CommitSyncer crashes, it is automatically restarted with backoff to prevent node shutdown.
pub(crate) struct CommitSyncerSupervisor;

impl CommitSyncerSupervisor {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn start<C: NetworkClient + 'static>(
        context: Arc<Context>,
        core_thread_dispatcher: Arc<dyn CoreThreadDispatcher>,
        commit_vote_monitor: Arc<CommitVoteMonitor>,
        commit_consumer_monitor: Arc<CommitConsumerMonitor>,
        block_verifier: Arc<dyn BlockVerifier>,
        transaction_certifier: TransactionCertifier,
        network_client: Arc<C>,
        dag_state: Arc<RwLock<DagState>>,
        coordination_hub: crate::coordination_hub::ConsensusCoordinationHub,
        adaptive_delay_state: Option<Arc<AdaptiveDelayState>>,
    ) -> CommitSyncerHandle {
        let (tx_shutdown, mut rx_shutdown) = oneshot::channel();
        
        let supervisor_task = spawn_logged_monitored_task!(
            async move {
                let mut restart_delay = Duration::from_secs(1);
                loop {
                    tracing::info!("🛡️ [SUPERVISOR] Starting CommitSyncer task...");
                    let syncer = CommitSyncer::new(
                        context.clone(),
                        core_thread_dispatcher.clone(),
                        commit_vote_monitor.clone(),
                        commit_consumer_monitor.clone(),
                        block_verifier.clone(),
                        transaction_certifier.clone(),
                        network_client.clone(),
                        dag_state.clone(),
                        coordination_hub.clone(),
                        adaptive_delay_state.clone(),
                    );
                    
                    let mut handle = syncer.start();

                    tokio::select! {
                        res = &mut handle.schedule_task => {
                            if let Err(e) = res {
                                if e.is_panic() {
                                    tracing::error!("🔴 [SUPERVISOR] CommitSyncer panicked! Restarting in {:?}...", restart_delay);
                                } else {
                                    tracing::error!("🔴 [SUPERVISOR] CommitSyncer task cancelled! Restarting in {:?}...", restart_delay);
                                }
                            } else {
                                tracing::warn!("⚠️ [SUPERVISOR] CommitSyncer exited cleanly. Expected? Restarting in {:?}...", restart_delay);
                            }
                            
                            tokio::time::sleep(restart_delay).await;
                            // Exponential backoff capped at 10 seconds
                            restart_delay = std::cmp::min(restart_delay * 2, Duration::from_secs(10));
                        }
                        _ = &mut rx_shutdown => {
                            tracing::info!("🛡️ [SUPERVISOR] Shutdown signal received. Stopping CommitSyncer...");
                            handle.stop().await;
                            break;
                        }
                    }
                }
            },
            "commit_syncer_supervisor"
        );

        CommitSyncerHandle {
            schedule_task: supervisor_task,
            tx_shutdown,
        }
    }

    // Derived interval 
    fn poll_interval(&self) -> Duration {
        match self.coordination_hub.get_phase() {
            crate::coordination_hub::NodeConsensusPhase::CatchingUp => Duration::from_millis(150),
            crate::coordination_hub::NodeConsensusPhase::Bootstrapping => Duration::from_millis(200),
            crate::coordination_hub::NodeConsensusPhase::Healthy => Duration::from_secs(2),
        }
    }

    /// STATE MACHINE: Update sync state based on current metrics
    fn update_state(&mut self) {
        let highest_handled_index = self.inner.commit_consumer_monitor.highest_handled_commit();
        let quorum_commit = self.inner.commit_vote_monitor.quorum_commit_index();
        let mut local_commit = self.inner.dag_state.read().last_commit_index();

        // ════════════════════════════════════════════════════════════════════════
        // SNAPSHOT RESTORE FAST-FORWARD
        // If Rust DAG is empty (local_commit == 0) BUT Go executor has restored state
        // up to highest_handled_index > 0, we MUST fast-forward the baseline NOW.
        // Otherwise, `is_behind` will evaluate to `true`, node stays stuck in
        // CatchingUp trying to fetch blocks 1..highest_handled_index which are
        // irrelevant and will cause own-slot conflicts.
        // ════════════════════════════════════════════════════════════════════════
        if local_commit == 0 && highest_handled_index > 0 {
            tracing::info!(
                "🚀 [COLD-START/RESTORE] Node initialized with empty DAG but Go execution is at {}. Fast-forwarding DAG baseline NOW.",
                highest_handled_index
            );
            
            // Fast-forward DAG state to match Go Execution Engine's progress
            // We use highest_handled_index as the base target round for GC dropping
            self.inner.dag_state.write().reset_to_network_baseline(highest_handled_index as u32, highest_handled_index, crate::commit::CommitDigest::MIN);
            self.synced_commit_index = highest_handled_index;
            
            // Update local_commit for the phase evaluation below!
            local_commit = highest_handled_index;
        }

        let current_phase = self.coordination_hub.get_phase();

        // Phase is determined by DAG consensus progress, NOT Go execution speed.
        // local_commit = how far the DAG has committed
        // quorum_commit = network consensus progress
        // highest_handled_index = Go execution progress (independent of consensus)
        let is_behind = local_commit < quorum_commit;

        let next_phase = if is_behind {
            crate::coordination_hub::NodeConsensusPhase::CatchingUp
        } else {
            crate::coordination_hub::NodeConsensusPhase::Healthy
        };

        if current_phase != next_phase && current_phase != crate::coordination_hub::NodeConsensusPhase::Bootstrapping {
            self.coordination_hub.set_phase(next_phase);
        } else if current_phase == crate::coordination_hub::NodeConsensusPhase::Bootstrapping {
            // ════════════════════════════════════════════════════════════════
            // BOOTSTRAPPING EXIT LOGIC — two distinct scenarios:
            //
            // 1. GENESIS START (highest_handled == 0):
            //    Go has no state, DAG is empty. Node MUST propose block 1 to
            //    bootstrap consensus. → Immediately transition to Healthy.
            //
            // 2. SNAPSHOT RESTART (highest_handled > 0):
            //    Go has state at index N, DAG was wiped. Must wait for
            //    CommitSyncer to fast-forward baseline, then detect quorum
            //    before allowing proposals. → Only exit when quorum > 0.
            // ════════════════════════════════════════════════════════════════
            if highest_handled_index == 0 {
                // Genesis: no Go state → exit Bootstrapping immediately
                tracing::info!(
                    "🚀 [BOOTSTRAP] Genesis detected (highest_handled=0). \
                     Transitioning to {:?} to allow block 1 proposal.",
                    next_phase
                );
                self.coordination_hub.set_phase(next_phase);
            } else if quorum_commit > 0 {
                // Snapshot restart: Go has state, wait for quorum detection
                tracing::info!(
                    "🚀 [BOOTSTRAP] Snapshot restore complete. quorum={}, transitioning to {:?}.",
                    quorum_commit, next_phase
                );
                self.coordination_hub.set_phase(next_phase);
            }
        }
    }

    async fn schedule_loop(mut self, mut rx_shutdown: oneshot::Receiver<()>) {
        eprintln!(
            "🔧 [COMMIT-SYNCER-LOOP] ENTERED! phase={:?}, synced={}, interval={}ms",
            self.coordination_hub.get_phase(),
            self.synced_commit_index,
            self.poll_interval().as_millis()
        );
        // STATE MACHINE: Get initial interval from current state
        let initial_interval = self.poll_interval();
        let mut current_interval_duration = initial_interval;
        let mut interval = tokio::time::interval(initial_interval);
        interval.set_missed_tick_behavior(MissedTickBehavior::Skip);
        let mut last_state_check = tokio::time::Instant::now();

        info!(
            "🚀 [COMMIT-SYNCER] Starting with phase={:?}, interval={}ms",
            self.coordination_hub.get_phase(),
            initial_interval.as_millis()
        );

        loop {
            tokio::select! {
                // Standby Mode Wakeup: Wakes up instantly when the quorum advances while we're sleeping
                _ = self.inner.commit_vote_monitor.quorum_advanced_notify.notified() => {
                    let now = tokio::time::Instant::now();
                    // Throttle state checks slightly to avoid rapid toggling
                    if now.duration_since(last_state_check) >= Duration::from_millis(100) {
                        self.update_state();
                        last_state_check = now;
                    }
                    self.try_schedule_once();
                }
                // Periodically, schedule new fetches if the node is falling behind or still catching up.
                _ = interval.tick() => {
                    // STATE MACHINE: Check for state transitions dynamically
                    let now = tokio::time::Instant::now();
                    let check_interval = if self.coordination_hub.is_healthy() {
                        Duration::from_secs(2)
                    } else {
                        Duration::from_millis(500)
                    };
                    if now.duration_since(last_state_check) >= check_interval {
                        let old_state = self.coordination_hub.get_phase();
                        self.update_state();
                        let new_state = self.coordination_hub.get_phase();
                        
                        let local_commit = self.inner.dag_state.read().last_commit_index();
                        let quorum_commit = self.inner.commit_vote_monitor.quorum_commit_index();
                        let lag = quorum_commit.saturating_sub(local_commit);

                        info!(
                            "🛡️ [UNIFIED STATE] Phase: {:?} | Local DAG Commit: {} | Network Quorum: {} | Lag: {} | Block Source: {}",
                            new_state, local_commit, quorum_commit, lag,
                            match new_state {
                                crate::coordination_hub::NodeConsensusPhase::CatchingUp => "SYNC_ONLY (CatchingUp)",
                                crate::coordination_hub::NodeConsensusPhase::Bootstrapping => "BOOTSTRAPPING",
                                crate::coordination_hub::NodeConsensusPhase::Healthy => "CONSENSUS (Healthy)",
                            }
                        );

                        if old_state != new_state {
                            info!(
                                "🔄 [NODE4-DEBUG] STATE TRANSITION: {:?} → {:?}",
                                old_state, new_state
                            );
                        }
                        let target_interval = self.poll_interval();
                        if target_interval != current_interval_duration {
                            let old_ms = current_interval_duration.as_millis();
                            current_interval_duration = target_interval;
                            interval = tokio::time::interval(target_interval);
                            interval.set_missed_tick_behavior(MissedTickBehavior::Skip);
                            info!(
                                "⚡ [STATE-CHANGE] {:?} → {:?}, interval: {}ms → {}ms",
                                old_state, new_state, old_ms, target_interval.as_millis()
                            );
                            
                            if new_state == crate::coordination_hub::NodeConsensusPhase::Healthy {
                                info!("🛌 [STANDBY MODE] CommitSyncer has entered Standby Mode. Sleeping until quorum advances...");
                                // When switching to Healthy, the 2-second interval tick continues as a background heartbeat,
                                // but we rely primarily on `quorum_advanced_notify` to wake up instantly.
                            }
                        }
                        last_state_check = now;
                    }
                    // Only try to schedule if we're not fully healthy/standby, or if this is the heartbeat tick
                    self.try_schedule_once();
                }
                // Handles results from fetch tasks.
                Some(result) = self.inflight_fetches.join_next(), if !self.inflight_fetches.is_empty() => {
                    if let Err(e) = result {
                        if e.is_panic() {
                            std::panic::resume_unwind(e.into_panic());
                        }
                        warn!("Fetch cancelled. CommitSyncer shutting down: {}", e);
                        // If any fetch is cancelled or panicked, try to shutdown and exit the loop.
                        self.inflight_fetches.shutdown().await;
                        return;
                    }
                    let (target_end, commits) = result.expect("inflight fetch result should be Ok after error handling above");
                    let should_shutdown = self.handle_fetch_result(target_end, commits).await;
                    if should_shutdown {
                        warn!("🔴 [COMMIT-SYNCER] CoreThread is gone. Shutting down schedule_loop.");
                        self.inflight_fetches.shutdown().await;
                        return;
                    }
                }
                _ = &mut rx_shutdown => {
                    // Shutdown requested.
                    info!("CommitSyncer shutting down ...");
                    self.inflight_fetches.shutdown().await;
                    return;
                }
            }

            self.try_start_fetches();
        }
    }

    fn try_schedule_once(&mut self) {
        let quorum_commit_index = self.inner.commit_vote_monitor.quorum_commit_index();
        let local_commit_index = self.inner.dag_state.read().last_commit_index();
        let metrics = &self.inner.context.metrics.node_metrics;
        metrics
            .commit_sync_quorum_index
            .set(quorum_commit_index as i64);
        metrics
            .commit_sync_local_index
            .set(local_commit_index as i64);

        // Update quorum commit rate tracker for adaptive delay
        if let Some(adaptive_delay_state) = &self.adaptive_delay_state {
            adaptive_delay_state.update_quorum_commit(quorum_commit_index);
        }
        let highest_handled_index = self.inner.commit_consumer_monitor.highest_handled_commit();
        let _highest_scheduled_index = self.highest_scheduled_index.unwrap_or(0);

        // Track synced commits: ALWAYS use the max of local DAG commit and
        // Go execution progress, regardless of phase. Using only
        // highest_handled_index during CatchingUp caused synced to lag behind
        // local_commit, creating permanent gaps with fetched ranges.
        self.synced_commit_index = self
            .synced_commit_index
            .max(local_commit_index)
            .max(highest_handled_index);

        let unhandled_commits_threshold = self.unhandled_commits_threshold();
        // Throttle noisy logs:
        // - When healthy (no lag): log at most once per 120s.
        // - When lagging (quorum ahead of local): log at most once per 30s, or when lag jumps notably.
        let now = tokio::time::Instant::now();
        let lag = quorum_commit_index.saturating_sub(local_commit_index);
        let last_lag = self
            .last_logged_quorum_commit_index
            .saturating_sub(self.last_logged_local_commit_index);
        let min_interval = if lag == 0 {
            Duration::from_secs(120)
        } else {
            Duration::from_secs(30)
        };
        let lag_jump = lag > last_lag + (unhandled_commits_threshold / 2).max(1);

        // CRITICAL: Detect significant lag and enter sync mode
        // Thresholds for sync mode:
        // - MODERATE_LAG: 50 commits or 5% behind quorum -> enter sync mode
        // - SEVERE_LAG: 200 commits or 10% behind quorum -> aggressive sync mode
        const MODERATE_LAG_THRESHOLD: u32 = 50; // For logging only - thresholds defined in SyncState

        let lag_percentage = if quorum_commit_index > 0 {
            (lag as f64 / quorum_commit_index as f64) * 100.0
        } else {
            0.0
        };

        // STATE MACHINE: Update state before making decisions
        self.update_state();

        // Log significant lag warnings (throttled)
        if lag > MODERATE_LAG_THRESHOLD
            && now.duration_since(self.last_state_log_at) >= Duration::from_secs(10)
        {
            if self.coordination_hub.is_catching_up() {
                tracing::error!(
                    "🚨 [LAG-DETECTION] Phase={:?}, lag={} commits ({}% behind quorum), local_commit={}, quorum_commit={}, synced_commit={}",
                    self.coordination_hub.get_phase(), lag, lag_percentage, local_commit_index, quorum_commit_index, self.synced_commit_index
                );
            } else {
                tracing::warn!(
                    "⚠️ [LAG-DETECTION] Phase={:?}, lag={} commits ({}% behind quorum), local_commit={}, quorum_commit={}, synced_commit={}",
                    self.coordination_hub.get_phase(), lag, lag_percentage, local_commit_index, quorum_commit_index, self.synced_commit_index
                );
            }
            self.last_state_log_at = now;
        }


        if now.duration_since(self.last_schedule_log_at) >= min_interval
            || lag_jump
            || quorum_commit_index != self.last_logged_quorum_commit_index
                && lag > 0
                && now.duration_since(self.last_schedule_log_at) >= Duration::from_secs(10)
        {
            info!(
                "[NODE4-DEBUG] schedule: phase={:?}, synced={}, local={}, quorum={}, lag={}, scheduled={:?}",
                self.coordination_hub.get_phase(),
                self.synced_commit_index,
                local_commit_index,
                quorum_commit_index,
                lag,
                self.highest_scheduled_index,
            );
            self.last_schedule_log_at = now;
            self.last_logged_quorum_commit_index = quorum_commit_index;
            self.last_logged_local_commit_index = local_commit_index;
        }

        // Cleanup pending fetches whose entire range is already synced.
        self.pending_fetches
            .retain(|range| range.end() > self.synced_commit_index);
        let fetch_after_index = self
            .synced_commit_index
            .max(self.highest_scheduled_index.unwrap_or(0));

        // ADAPTIVE SYNC: Adjust batch size and scheduling based on lag severity
        // When in sync mode, use larger batches and more aggressive scheduling
        let base_batch_size = self.inner.context.parameters.commit_sync_batch_size;
        // STATE MACHINE: Use state to determine batch size and threshold
        let effective_batch_size = if self.coordination_hub.is_catching_up() { base_batch_size * 4 } else { base_batch_size };

        // Compute effective threshold based on state severity
        let effective_threshold = if self.coordination_hub.is_catching_up() {
            unhandled_commits_threshold * 4 // Turbo: allow 4x unhandled commits for severe lag
        } else {
            unhandled_commits_threshold
        };

        // When the node is falling behind, schedule pending fetches which will be executed on later.
        for prev_end in
            (fetch_after_index..=quorum_commit_index).step_by(effective_batch_size as usize)
        {
            // Create range with inclusive start and end.
            let range_start = prev_end + 1;
            let range_end = prev_end + effective_batch_size;
            // Commit range is not fetched when [range_start, range_end] contains less number of commits
            // than the target batch size. This is to avoid the cost of processing more and smaller batches.
            // Block broadcast, subscription and synchronization will help the node catchup.
            if quorum_commit_index < range_end {
                break;
            }
            // Pause scheduling new fetches when handling of commits is lagging.
            // STATE MACHINE: Healthy phase ONLY checks threshold. CatchingUp and other
            // phases ALWAYS schedule to prevent deadlock: if fetches are blocked due
            // to lag, the node can never catch up.
            let fetch_threshold_index = if self.coordination_hub.is_healthy() { highest_handled_index.max(local_commit_index) } else { highest_handled_index };
            if self.coordination_hub.is_healthy()
                && fetch_threshold_index + effective_threshold < range_end
            {
                warn!(
                    "Skip scheduling new commit fetches: consensus handler is lagging. phase={:?}, highest_handled_index={}, local_commit_index={}, effective_threshold={}, range_end={}",
                    self.coordination_hub.get_phase(), highest_handled_index, local_commit_index, effective_threshold, range_end
                );
                break;
            }
            let new_range: CommitRange = (range_start..=range_end).into();
            info!(
                "[NODE4-DEBUG] scheduling fetch: range={:?}, pending_count={}",
                new_range,
                self.pending_fetches.len()
            );
            self.pending_fetches.insert(new_range);
            // quorum_commit_index should be non-decreasing, so highest_scheduled_index should not
            // decrease either.
            self.highest_scheduled_index = Some(range_end);
        }

        // TURBO SYNC: In sync mode, schedule remaining partial batch so no commits are left behind.
        // Without this, the last partial batch (smaller than effective_batch_size) is always skipped,
        // leaving up to (effective_batch_size - 1) commits unscheduled until the next tick.
        // We ALWAYS run this (regardless of phase) to prevent the node from permanently stalling
        // if it misses a few broadcasted blocks and falls just short of a full batch.
        let scheduled_up_to = self.highest_scheduled_index.unwrap_or(fetch_after_index);
        let fetch_threshold_index = if self.coordination_hub.is_healthy() { highest_handled_index.max(local_commit_index) } else { highest_handled_index };
        if scheduled_up_to < quorum_commit_index
            && fetch_threshold_index + effective_threshold >= quorum_commit_index
        {
            let range_start = scheduled_up_to + 1;
            self.pending_fetches
                .insert((range_start..=quorum_commit_index).into());
            self.highest_scheduled_index = Some(quorum_commit_index);
        }
    }

    /// Processes fetched commits and sends them to Core.
    /// Returns `true` if the CoreThread has shut down and the schedule_loop should exit.
    async fn handle_fetch_result(
        &mut self,
        target_end: CommitIndex,
        certified_commits: CertifiedCommits,
    ) -> bool {
        assert!(!certified_commits.commits().is_empty());

        let (total_blocks_fetched, total_blocks_size_bytes) = certified_commits
            .commits()
            .iter()
            .fold((0, 0), |(blocks, bytes), c| {
                (
                    blocks + c.blocks().len(),
                    bytes
                        + c.blocks()
                            .iter()
                            .map(|b| b.serialized().len())
                            .sum::<usize>() as u64,
                )
            });

        let metrics = &self.inner.context.metrics.node_metrics;
        metrics
            .commit_sync_fetched_commits
            .inc_by(certified_commits.commits().len() as u64);
        metrics
            .commit_sync_fetched_blocks
            .inc_by(total_blocks_fetched as u64);
        metrics
            .commit_sync_total_fetched_blocks_size
            .inc_by(total_blocks_size_bytes);

        let (commit_start, commit_end) = (
            certified_commits
                .commits()
                .first()
                .expect("certified_commits checked non-empty above")
                .index(),
            certified_commits
                .commits()
                .last()
                .expect("certified_commits checked non-empty above")
                .index(),
        );

        // ═══════════════════════════════════════════════════════════════════════
        // COLD-START GC ADVANCE: Handled earlier in try_schedule_once
        // ═══════════════════════════════════════════════════════════════════════

        self.highest_fetched_commit_index = self.highest_fetched_commit_index.max(commit_end);
        metrics
            .commit_sync_highest_fetched_index
            .set(self.highest_fetched_commit_index as i64);

        // Allow returning partial results, and try fetching the rest separately.
        if commit_end < target_end {
            self.pending_fetches
                .insert((commit_end + 1..=target_end).into());
        }
        // ═══════════════════════════════════════════════════════════════════════
        // CRITICAL FIX: Always keep synced_commit_index >= local DAG commit.
        // Previously this was gated by is_healthy(), which caused a PERMANENT
        // STALL during CatchingUp: if a commit was already in the local DAG
        // (from a previous add_certified_commits call) but synced_commit_index
        // hadn't advanced, a gap formed between synced and the next fetched
        // range. Since fetched ranges require synced+1 >= range.start(), the
        // gap blocked ALL processing forever.
        //
        // Example: synced=4301, local_commit=4302, fetched starts at 4303
        // → needs 4302 to be fetched, but it's already local → gap forever
        // ═══════════════════════════════════════════════════════════════════════
        {
            let local_commit = self.inner.dag_state.read().last_commit_index();
            if local_commit > self.synced_commit_index {
                tracing::info!(
                    "[COMMIT-SYNCER] Advancing synced_commit_index {} → {} (from local DAG, phase={:?})",
                    self.synced_commit_index,
                    local_commit,
                    self.coordination_hub.get_phase()
                );
                self.synced_commit_index = local_commit;
            }
        }
        info!(
            "[NODE4-DEBUG] fetched result: range={}→{}, synced_commit={}, pending_ranges={}",
            commit_start, commit_end, self.synced_commit_index, self.fetched_ranges.len()
        );

        // Only add new blocks if at least some of them are not already synced.
        if self.synced_commit_index < commit_end {
            self.fetched_ranges
                .insert((commit_start..=commit_end).into(), certified_commits);
            info!(
                "[NODE4-DEBUG] inserted fetched range {}→{} into fetched_ranges (len={})",
                commit_start, commit_end, self.fetched_ranges.len()
            );
        }
        // Try to process as many fetched blocks as possible.
        while let Some((fetched_commit_range, _commits)) = self.fetched_ranges.first_key_value() {
            // Only pop fetched_ranges if there is no gap with blocks already synced.
            // Note: start, end and synced_commit_index are all inclusive.
            let (fetched_commit_range, commits) =
                if fetched_commit_range.start() <= self.synced_commit_index + 1 {
                    info!(
                        "[NODE4-DEBUG] processing range {}→{} (synced={})",
                        fetched_commit_range.start(),
                        fetched_commit_range.end(),
                        self.synced_commit_index
                    );
                    self.fetched_ranges
                        .pop_first()
                        .expect("checked first_key_value above")
                } else {
                    // Found gap between earliest fetched block and latest synced block.
                    // Schedule a fetch for the missing range to fill the gap.
                    let gap_start = self.synced_commit_index + 1;
                    let gap_end = fetched_commit_range.start() - 1;
                    tracing::warn!(
                        "[COMMIT-SYNCER] GAP DETECTED: fetched_range={}→{} but synced={}. Scheduling gap fill {}→{}",
                        fetched_commit_range.start(),
                        fetched_commit_range.end(),
                        self.synced_commit_index,
                        gap_start,
                        gap_end
                    );
                    if gap_start <= gap_end {
                        let gap_range: CommitRange = (gap_start..=gap_end).into();
                        // Only insert if not already inflight or pending
                        if !self.pending_fetches.iter().any(|r| r.start() <= gap_start && r.end() >= gap_end) {
                            tracing::info!(
                                "[COMMIT-SYNCER] Inserting gap-fill fetch: {:?}",
                                gap_range
                            );
                            self.pending_fetches.insert(gap_range);
                        }
                    }
                    metrics.commit_sync_gap_on_processing.inc();
                    break;
                };
            // Avoid sending to Core a whole batch of already synced blocks.
            if fetched_commit_range.end() <= self.synced_commit_index {
                continue;
            }

            debug!(
                "Fetched blocks for commit range {:?}: {}",
                fetched_commit_range,
                commits
                    .commits()
                    .iter()
                    .flat_map(|c| c.blocks())
                    .map(|b| b.reference().to_string())
                    .join(","),
            );

            // If core thread cannot handle the incoming blocks, it is ok to block here
            // to slow down the commit syncer.
            info!(
                "[NODE4-DEBUG] sending commits {}→{} to Core, commits_count={}",
                fetched_commit_range.start(),
                fetched_commit_range.end(),
                commits.commits().len()
            );
            match self
                .inner
                .core_thread_dispatcher
                .add_certified_commits(commits)
                .await
            {
                // Missing ancestors are possible from certification blocks, but
                // it is unnecessary to try to sync their causal history. If they are required
                // for the progress of the DAG, they will be included in a future commit.
                Ok(missing) => {
                    info!(
                        "[NODE4-DEBUG] Core accepted range {}→{}, missing_blocks={}",
                        fetched_commit_range.start(),
                        fetched_commit_range.end(),
                        missing.len()
                    );
                    if !missing.is_empty() {
                        info!(
                            "Certification blocks have missing ancestors: {} for commit range {:?}",
                            missing.iter().map(|b| b.to_string()).join(","),
                            fetched_commit_range,
                        );
                    }
                    for block_ref in missing {
                        let hostname = &self
                            .inner
                            .context
                            .committee
                            .authority(block_ref.author)
                            .hostname;
                        metrics
                            .commit_sync_fetch_missing_blocks
                            .with_label_values(&[hostname])
                            .inc();
                    }
                }
                Err(e) => {
                    error!(
                        "🔴 [COMMIT-SYNCER] Core FAILED to process range {}→{}: {}. CoreThread likely shut down.",
                        fetched_commit_range.start(),
                        fetched_commit_range.end(),
                        e
                    );
                    return true; // Signal schedule_loop to exit
                }
            };

            // Once commits and blocks are sent to Core, ratchet up synced_commit_index
            self.synced_commit_index = self.synced_commit_index.max(fetched_commit_range.end());
            info!(
                "[NODE4-DEBUG] synced_commit_index advanced to {}",
                self.synced_commit_index
            );
        }

        metrics
            .commit_sync_inflight_fetches
            .set(self.inflight_fetches.len() as i64);
        metrics
            .commit_sync_pending_fetches
            .set(self.pending_fetches.len() as i64);
        metrics
            .commit_sync_highest_synced_index
            .set(self.synced_commit_index as i64);

        false // No shutdown needed
    }

    fn try_start_fetches(&mut self) {
        // Cap parallel fetches based on configured limit and committee size, to avoid overloading the network.
        // Also when there are too many fetched blocks that cannot be sent to Core before an earlier fetch
        // has not finished, reduce parallelism so the earlier fetch can retry on a better host and succeed.
        // STATE MACHINE: Adjust parallelism based on sync state
        let base_parallel_fetches = self.inner.context.parameters.commit_sync_parallel_fetches;
        let effective_parallel_fetches = if self.coordination_hub.is_catching_up() {
            // Turbo: 3x parallel fetches for catching up
            (base_parallel_fetches * 3)
                .min(self.inner.context.committee.size())
        } else {
            base_parallel_fetches
        };

        let effective_batches_ahead = if self.coordination_hub.is_catching_up() {
            self.inner.context.parameters.commit_sync_batches_ahead * 3
        } else {
            self.inner.context.parameters.commit_sync_batches_ahead
        };

        // In turbo mode, allow fetching from all peers (not just 2/3)
        let committee_cap = if self.coordination_hub.is_catching_up() {
            self.inner.context.committee.size()
        } else {
            self.inner.context.committee.size() * 2 / 3
        };

        let target_parallel_fetches = effective_parallel_fetches
            .min(committee_cap)
            .min(
                effective_batches_ahead
                    .saturating_sub(self.fetched_ranges.len()),
            );
        // Start new fetches if there are pending batches and available slots.
        loop {
            if self.inflight_fetches.len() >= target_parallel_fetches {
                break;
            }
            let Some(commit_range) = self.pending_fetches.pop_first() else {
                break;
            };
            self.inflight_fetches
                .spawn(Self::fetch_loop(
                    self.inner.clone(),
                    commit_range,
                    self.coordination_hub.is_catching_up(), // is_severe_lag
                    self.coordination_hub.is_catching_up(), // is_sync_mode
                ));
        }

        let metrics = &self.inner.context.metrics.node_metrics;
        metrics
            .commit_sync_inflight_fetches
            .set(self.inflight_fetches.len() as i64);
        metrics
            .commit_sync_pending_fetches
            .set(self.pending_fetches.len() as i64);
        metrics
            .commit_sync_highest_synced_index
            .set(self.synced_commit_index as i64);
    }

    // Retries fetching commits and blocks from available authorities, until a request succeeds
    // where at least a prefix of the commit range is fetched.
    // Returns the fetched commits and blocks referenced by the commits.
    async fn fetch_loop(
        inner: Arc<Inner<C>>,
        commit_range: CommitRange,
        is_severe_lag: bool,
        is_sync_mode: bool,
    ) -> (CommitIndex, CertifiedCommits) {
        // Individual request base timeout.
        const TIMEOUT: Duration = Duration::from_secs(5);
        // Max per-request timeout will be base timeout times a multiplier.
        // At the extreme, this means there will be 120s timeout to fetch max_blocks_per_fetch blocks.
        const MAX_TIMEOUT_MULTIPLIER: u32 = 12;
        // timeout * max number of targets should be reasonably small, so the
        // system can adjust to slow network or large data sizes quickly.
        const MAX_NUM_TARGETS: usize = 24;
        let mut timeout_multiplier = 0;
        let _timer = inner
            .context
            .metrics
            .node_metrics
            .commit_sync_fetch_loop_latency
            .start_timer();
        info!("Starting to fetch commits in {commit_range:?} ...",);
        loop {
            // Attempt to fetch commits and blocks through min(committee size, MAX_NUM_TARGETS) peers.
            let mut target_authorities = inner
                .context
                .committee
                .authorities()
                .filter_map(|(i, _)| {
                    if i != inner.context.own_index {
                        Some(i)
                    } else {
                        None
                    }
                })
                .collect_vec();
            target_authorities.shuffle(&mut ThreadRng::default());
            target_authorities.truncate(MAX_NUM_TARGETS);
            // Increase timeout multiplier for each loop until MAX_TIMEOUT_MULTIPLIER.
            timeout_multiplier = (timeout_multiplier + 1).min(MAX_TIMEOUT_MULTIPLIER);
            let request_timeout = TIMEOUT * timeout_multiplier;
            // Give enough overall timeout for fetching commits and blocks.
            // - Timeout for fetching commits and commit certifying blocks.
            // - Timeout for fetching blocks referenced by the commits.
            // - Time spent on pipelining requests to fetch blocks.
            // - Another headroom to allow fetch_once() to timeout gracefully if possible.
            let fetch_timeout = request_timeout * 4;
            // Try fetching from selected target authority.
            for authority in target_authorities {
                match tokio::time::timeout(
                    fetch_timeout,
                    Self::fetch_once(
                        inner.clone(),
                        authority,
                        commit_range.clone(),
                        request_timeout,
                        is_severe_lag,
                    ),
                )
                .await
                {
                    Ok(Ok(commits)) => {
                        info!("Finished fetching commits in {commit_range:?}",);
                        return (commit_range.end(), commits);
                    }
                    Ok(Err(e)) => {
                        let hostname = inner
                            .context
                            .committee
                            .authority(authority)
                            .hostname
                            .clone();
                        warn!("Failed to fetch {commit_range:?} from {hostname}: {}", e);
                        inner
                            .context
                            .metrics
                            .node_metrics
                            .commit_sync_fetch_once_errors
                            .with_label_values(&[&hostname, e.name()])
                            .inc();
                    }
                    Err(_) => {
                        let hostname = inner
                            .context
                            .committee
                            .authority(authority)
                            .hostname
                            .clone();
                        warn!("Timed out fetching {commit_range:?} from {authority}",);
                        inner
                            .context
                            .metrics
                            .node_metrics
                            .commit_sync_fetch_once_errors
                            .with_label_values(&[&hostname, "FetchTimeout"])
                            .inc();
                    }
                }
            }
            // Avoid busy looping, by waiting briefly before retrying (reduced for faster catch-up).
            let retry_delay = if is_severe_lag {
                Duration::from_millis(500)
            } else if is_sync_mode {
                Duration::from_millis(1000)
            } else {
                Duration::from_secs(2)
            };
            sleep(retry_delay).await;
        }
    }

    // Fetches commits and blocks from a single authority. At a high level, first the commits are
    // fetched and verified. After that, blocks referenced in the certified commits are fetched
    // and sent to Core for processing.
    async fn fetch_once(
        inner: Arc<Inner<C>>,
        target_authority: AuthorityIndex,
        commit_range: CommitRange,
        timeout: Duration,
        is_severe_lag: bool,
    ) -> ConsensusResult<CertifiedCommits> {
        let _timer = inner
            .context
            .metrics
            .node_metrics
            .commit_sync_fetch_once_latency
            .start_timer();

        // 1. Fetch commits in the commit range from the target authority.
        let (serialized_commits, serialized_blocks) = inner
            .network_client
            .fetch_commits(target_authority, commit_range.clone(), timeout)
            .await?;

        // 2. Verify the response contains blocks that can certify the last returned commit,
        // and the returned commits are chained by digests, so earlier commits are certified
        // as well.
        let (commits, vote_blocks) = Handle::current()
            .spawn_blocking({
                let inner = inner.clone();
                move || {
                    inner.verify_commits(
                        target_authority,
                        commit_range,
                        serialized_commits,
                        serialized_blocks,
                    )
                }
            })
            .await
            .expect("Spawn blocking should not fail")?;

        // 3. Fetch blocks referenced by the commits, from the same peer where commits are fetched.
        let mut block_refs: Vec<_> = commits.iter().flat_map(|c| c.blocks()).cloned().collect();
        block_refs.sort();
        let num_chunks = block_refs
            .len()
            .div_ceil(inner.context.parameters.max_blocks_per_fetch)
            as u32;
        let mut requests: FuturesOrdered<_> = block_refs
            .chunks(inner.context.parameters.max_blocks_per_fetch)
            .enumerate()
            .map(|(i, request_block_refs)| {
                let inner = inner.clone();
                async move {
                    // 4. Send out pipelined fetch requests to avoid overloading the target authority.
                    // In turbo mode (severe lag), we blast requests to catch up as fast as possible.
                    if !is_severe_lag {
                        let delay = timeout * i as u32 / num_chunks / 4; // reduced delay for normal
                        sleep(delay).await;
                    }
                    // Retry block fetches up to 3 times with backoff before propagating the error.
                    const MAX_BLOCK_FETCH_RETRIES: u32 = 3;
                    let serialized_blocks = {
                        let mut last_err = None;
                        let mut result = None;
                        for attempt in 0..MAX_BLOCK_FETCH_RETRIES {
                            match inner
                                .network_client
                                .fetch_blocks(
                                    target_authority,
                                    request_block_refs.to_vec(),
                                    vec![],
                                    false,
                                    timeout,
                                )
                                .await
                            {
                                Ok(blocks) => {
                                    result = Some(blocks);
                                    break;
                                }
                                Err(e) => {
                                    let hostname = &inner.context.committee.authority(target_authority).hostname;
                                    warn!(
                                        "Commit sync: retry {}/{} fetching blocks from {hostname}: {e}",
                                        attempt + 1,
                                        MAX_BLOCK_FETCH_RETRIES
                                    );
                                    last_err = Some(e);
                                    if attempt + 1 < MAX_BLOCK_FETCH_RETRIES {
                                        sleep(Duration::from_millis(500 * (attempt as u64 + 1))).await;
                                    }
                                }
                            }
                        }
                        match result {
                            Some(blocks) => blocks,
                            None => return Err(last_err.expect("last_err must be set after failed retries")),
                        }
                    };
                    // 5. Verify the same number of blocks are returned as requested.
                    if request_block_refs.len() != serialized_blocks.len() {
                        return Err(ConsensusError::UnexpectedNumberOfBlocksFetched {
                            authority: target_authority,
                            requested: request_block_refs.len(),
                            received: serialized_blocks.len(),
                        });
                    }
                    // 6. Verify returned blocks have valid formats.
                    let signed_blocks = serialized_blocks
                        .iter()
                        .map(|serialized| {
                            let block: SignedBlock = bcs::from_bytes(serialized)
                                .map_err(ConsensusError::MalformedBlock)?;
                            Ok(block)
                        })
                        .collect::<ConsensusResult<Vec<_>>>()?;
                    // 7. Verify the returned blocks match the requested block refs.
                    // If they do match, the returned blocks can be considered verified as well.
                    let mut blocks = Vec::new();
                    for ((requested_block_ref, signed_block), serialized) in request_block_refs
                        .iter()
                        .zip(signed_blocks.into_iter())
                        .zip(serialized_blocks.into_iter())
                    {
                        let signed_block_digest = VerifiedBlock::compute_digest(&serialized);
                        let received_block_ref = BlockRef::new(
                            signed_block.round(),
                            signed_block.author(),
                            signed_block_digest,
                        );
                        if *requested_block_ref != received_block_ref {
                            return Err(ConsensusError::UnexpectedBlockForCommit {
                                peer: target_authority,
                                requested: *requested_block_ref,
                                received: received_block_ref,
                            });
                        }
                        blocks.push(VerifiedBlock::new_verified(signed_block, serialized));
                    }
                    Ok(blocks)
                }
            })
            .collect();

        let mut fetched_blocks = BTreeMap::new();
        while let Some(result) = requests.next().await {
            for block in result? {
                fetched_blocks.insert(block.reference(), block);
            }
        }

        // 8. Check if the block timestamps are lower than current time - this is for metrics only.
        for block in fetched_blocks.values().chain(vote_blocks.iter()) {
            let now_ms = inner.context.clock.timestamp_utc_ms();
            let forward_drift = block.timestamp_ms().saturating_sub(now_ms);
            if forward_drift == 0 {
                continue;
            };
            let peer_hostname = &inner.context.committee.authority(target_authority).hostname;
            inner
                .context
                .metrics
                .node_metrics
                .block_timestamp_drift_ms
                .with_label_values(&[peer_hostname, "commit_syncer"])
                .inc_by(forward_drift);
        }

        // 9. Now create certified commits by assigning the blocks to each commit.
        let mut certified_commits = Vec::new();
        for commit in &commits {
            let blocks = commit
                .blocks()
                .iter()
                .map(|block_ref| {
                    fetched_blocks
                        .remove(block_ref)
                        .expect("Block should exist")
                })
                .collect::<Vec<_>>();
            certified_commits.push(CertifiedCommit::new_certified(commit.clone(), blocks));
        }

        // 10. Add blocks in certified commits to the transaction certifier.
        for commit in &certified_commits {
            for block in commit.blocks() {
                // Only account for reject votes in the block, since they may vote on uncommitted
                // blocks or transactions. It is unnecessary to vote on the committed blocks
                // themselves.
                if inner.context.protocol_config.mysticeti_fastpath() {
                    inner
                        .transaction_certifier
                        .add_voted_blocks(vec![(block.clone(), vec![])]);
                }
            }
        }

        Ok(CertifiedCommits::new(certified_commits, vote_blocks))
    }

    fn unhandled_commits_threshold(&self) -> CommitIndex {
        self.inner.context.parameters.commit_sync_batch_size
            * (self.inner.context.parameters.commit_sync_batches_ahead as u32)
    }

    #[cfg(test)]
    fn pending_fetches(&self) -> BTreeSet<CommitRange> {
        self.pending_fetches.clone()
    }

    #[cfg(test)]
    fn fetched_ranges(&self) -> BTreeMap<CommitRange, CertifiedCommits> {
        self.fetched_ranges.clone()
    }

    #[cfg(test)]
    fn highest_scheduled_index(&self) -> Option<CommitIndex> {
        self.highest_scheduled_index
    }

    #[cfg(test)]
    fn highest_fetched_commit_index(&self) -> CommitIndex {
        self.highest_fetched_commit_index
    }

    #[cfg(test)]
    fn synced_commit_index(&self) -> CommitIndex {
        self.synced_commit_index
    }
}

struct Inner<C: NetworkClient> {
    context: Arc<Context>,
    core_thread_dispatcher: Arc<dyn CoreThreadDispatcher>,
    commit_vote_monitor: Arc<CommitVoteMonitor>,
    commit_consumer_monitor: Arc<CommitConsumerMonitor>,
    block_verifier: Arc<dyn BlockVerifier>,
    transaction_certifier: TransactionCertifier,
    network_client: Arc<C>,
    dag_state: Arc<RwLock<DagState>>,
}

impl<C: NetworkClient> Inner<C> {
    /// Verifies the commits and also certifies them using the provided vote blocks for the last commit. The
    /// method returns the trusted commits and the votes as verified blocks.
    fn verify_commits(
        &self,
        peer: AuthorityIndex,
        commit_range: CommitRange,
        serialized_commits: Vec<Bytes>,
        serialized_vote_blocks: Vec<Bytes>,
    ) -> ConsensusResult<(Vec<TrustedCommit>, Vec<VerifiedBlock>)> {
        // Parse and verify commits.
        let mut commits = Vec::new();
        for serialized in &serialized_commits {
            let commit: Commit =
                bcs::from_bytes(serialized).map_err(ConsensusError::MalformedCommit)?;
            let digest = TrustedCommit::compute_digest(serialized);
            if commits.is_empty() {
                // start is inclusive, so first commit must be at the start index.
                if commit.index() != commit_range.start() {
                    return Err(ConsensusError::UnexpectedStartCommit {
                        peer,
                        start: commit_range.start(),
                        commit: Box::new(commit),
                    });
                }
            } else {
                // Verify next commit increments index and references the previous digest.
                let (last_commit_digest, last_commit): &(CommitDigest, Commit) = commits
                    .last()
                    .expect("commits is non-empty: checked at loop entry");
                if commit.index() != last_commit.index() + 1
                    || &commit.previous_digest() != last_commit_digest
                {
                    return Err(ConsensusError::UnexpectedCommitSequence {
                        peer,
                        prev_commit: Box::new(last_commit.clone()),
                        curr_commit: Box::new(commit),
                    });
                }
            }
            // Do not process more commits past the end index.
            if commit.index() > commit_range.end() {
                break;
            }
            commits.push((digest, commit));
        }
        let Some((end_commit_digest, end_commit)) = commits.last() else {
            return Err(ConsensusError::NoCommitReceived { peer });
        };

        // Parse and verify blocks. Then accumulate votes on the end commit.
        let end_commit_ref = CommitRef::new_with_global(
            end_commit.index(),
            *end_commit_digest,
            end_commit.global_exec_index(),
            0, // Epoch will be ignored by PartialEq if we only compare index & digest, but wait, PartialEq compares all fields! Wait, the block's vote might have epoch = 0 or actual epoch.
        );
        let mut stake_aggregator = StakeAggregator::<QuorumThreshold>::new();
        let mut vote_blocks = Vec::new();
        for serialized in serialized_vote_blocks {
            let block: SignedBlock =
                bcs::from_bytes(&serialized).map_err(ConsensusError::MalformedBlock)?;
            // Only block signatures need to be verified, to verify commit votes.
            // But the blocks will be sent to Core, so they need to be fully verified.
            let (block, reject_transaction_votes) =
                self.block_verifier.verify_and_vote(block, serialized)?;
            if self.context.protocol_config.mysticeti_fastpath() {
                self.transaction_certifier
                    .add_voted_blocks(vec![(block.clone(), reject_transaction_votes)]);
            }
            for vote in block.commit_votes() {
                if *vote == end_commit_ref {
                    stake_aggregator.add(block.author(), &self.context.committee);
                }
            }
            vote_blocks.push(block);
        }

        // Check if the end commit has enough votes.
        if !stake_aggregator.reached_threshold(&self.context.committee) {
            return Err(ConsensusError::NotEnoughCommitVotes {
                stake: stake_aggregator.stake(),
                peer,
                commit: Box::new(end_commit.clone()),
            });
        }

        let trusted_commits = commits
            .into_iter()
            .zip(serialized_commits)
            .map(|((_d, c), s)| TrustedCommit::new_trusted(c, s))
            .collect();
        Ok((trusted_commits, vote_blocks))
    }
}

#[cfg(test)]
mod tests {
    use std::{sync::Arc, time::Duration};

    use bytes::Bytes;
    use consensus_config::{AuthorityIndex, Parameters};
    use consensus_types::block::{BlockRef, Round};
    use mysten_metrics::monitored_mpsc;
    use parking_lot::RwLock;

    use crate::{
        block::{TestBlock, VerifiedBlock},
        block_verifier::NoopBlockVerifier,
        commit::CommitRange,
        commit_syncer::CommitSyncer,
        commit_vote_monitor::CommitVoteMonitor,
        context::Context,
        core_thread::MockCoreThreadDispatcher,
        dag_state::DagState,
        error::ConsensusResult,
        network::{BlockStream, NetworkClient},
        storage::mem_store::MemStore,
        transaction_certifier::TransactionCertifier,
        CommitConsumerMonitor, CommitDigest, CommitRef,
    };

    #[derive(Default)]
    struct FakeNetworkClient {}

    #[async_trait::async_trait]
    impl NetworkClient for FakeNetworkClient {
        async fn send_block(
            &self,
            _peer: AuthorityIndex,
            _serialized_block: &VerifiedBlock,
            _timeout: Duration,
        ) -> ConsensusResult<()> {
            unimplemented!("Unimplemented")
        }

        async fn subscribe_blocks(
            &self,
            _peer: AuthorityIndex,
            _last_received: Round,
            _timeout: Duration,
        ) -> ConsensusResult<BlockStream> {
            unimplemented!("Unimplemented")
        }

        async fn fetch_blocks(
            &self,
            _peer: AuthorityIndex,
            _block_refs: Vec<BlockRef>,
            _highest_accepted_rounds: Vec<Round>,
            _breadth_first: bool,
            _timeout: Duration,
        ) -> ConsensusResult<Vec<Bytes>> {
            unimplemented!("Unimplemented")
        }

        async fn fetch_commits(
            &self,
            _peer: AuthorityIndex,
            _commit_range: CommitRange,
            _timeout: Duration,
        ) -> ConsensusResult<(Vec<Bytes>, Vec<Bytes>)> {
            unimplemented!("Unimplemented")
        }

        async fn fetch_commits_by_global_range(
            &self,
            _peer: AuthorityIndex,
            _start_global_index: u64,
            _max_global_index: u64,
            _timeout: Duration,
        ) -> ConsensusResult<Vec<crate::network::tonic_network::GlobalCommitInfo>> {
            unimplemented!("Unimplemented")
        }

        async fn send_epoch_change_proposal(
            &self,
            _peer: AuthorityIndex,
            _proposal: &crate::epoch_change::EpochChangeProposal,
            _timeout: Duration,
        ) -> ConsensusResult<()> {
            unimplemented!("Unimplemented")
        }

        async fn send_epoch_change_vote(
            &self,
            _peer: AuthorityIndex,
            _vote: &crate::epoch_change::EpochChangeVote,
            _timeout: Duration,
        ) -> ConsensusResult<()> {
            unimplemented!("Unimplemented")
        }

        async fn fetch_latest_blocks(
            &self,
            _peer: AuthorityIndex,
            _authorities: Vec<AuthorityIndex>,
            _timeout: Duration,
        ) -> ConsensusResult<Vec<Bytes>> {
            unimplemented!("Unimplemented")
        }

        async fn get_latest_rounds(
            &self,
            _peer: AuthorityIndex,
            _timeout: Duration,
        ) -> ConsensusResult<(Vec<Round>, Vec<Round>)> {
            unimplemented!("Unimplemented")
        }
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    async fn commit_syncer_start_and_pause_scheduling() {
        // SETUP
        let (context, _) = Context::new_for_test(4);
        // Use smaller batches and fetch limits for testing.
        let context = Context {
            own_index: AuthorityIndex::new_for_test(3),
            parameters: Parameters {
                commit_sync_batch_size: 5,
                commit_sync_batches_ahead: 5,
                commit_sync_parallel_fetches: 5,
                max_blocks_per_fetch: 5,
                ..context.parameters
            },
            ..context
        };
        let context = Arc::new(context);
        let block_verifier = Arc::new(NoopBlockVerifier {});
        let core_thread_dispatcher = Arc::new(MockCoreThreadDispatcher::default());
        let network_client = Arc::new(FakeNetworkClient::default());
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store)));
        let (blocks_sender, _blocks_receiver) =
            monitored_mpsc::unbounded_channel("consensus_block_output");
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            blocks_sender,
        );
        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
        let commit_consumer_monitor = Arc::new(CommitConsumerMonitor::new(0, 0));
        let mut commit_syncer = CommitSyncer::new(
            context,
            core_thread_dispatcher,
            commit_vote_monitor.clone(),
            commit_consumer_monitor.clone(),
            block_verifier,
            transaction_certifier,
            network_client,
            dag_state.clone(),
            None,
        );

        // Check initial state.
        assert!(commit_syncer.pending_fetches().is_empty());
        assert!(commit_syncer.fetched_ranges().is_empty());
        assert!(commit_syncer.highest_scheduled_index().is_none());
        assert_eq!(commit_syncer.highest_fetched_commit_index(), 0);
        assert_eq!(commit_syncer.synced_commit_index(), 0);

        // Force highest_accepted_round > 1 to bypass cold-start fast-forward logic,
        // which would otherwise skip scheduling fetches from index 1 to 10.
        let test_block = TestBlock::new(2, 0).build();
        dag_state.write().accept_block(VerifiedBlock::new_for_test(test_block));

        // Observe round 15 blocks voting for commit 10 from authorities 0 to 2 in CommitVoteMonitor
        for i in 0..3 {
            let test_block = TestBlock::new(15, i)
                .set_commit_votes(vec![CommitRef::new(10, CommitDigest::MIN)])
                .build();
            let block = VerifiedBlock::new_for_test(test_block);
            commit_vote_monitor.observe_block(&block);
        }

        // Fetches should be scheduled after seeing progress of other validators.
        commit_syncer.try_schedule_once();

        // Verify state.
        assert_eq!(commit_syncer.pending_fetches().len(), 1);
        assert!(commit_syncer.fetched_ranges().is_empty());
        assert_eq!(commit_syncer.highest_scheduled_index(), Some(10));
        assert_eq!(commit_syncer.highest_fetched_commit_index(), 0);
        assert_eq!(commit_syncer.synced_commit_index(), 0);

        // Observe round 40 blocks voting for commit 35 from authorities 0 to 2 in CommitVoteMonitor
        for i in 0..3 {
            let test_block = TestBlock::new(40, i)
                .set_commit_votes(vec![CommitRef::new(35, CommitDigest::MIN)])
                .build();
            let block = VerifiedBlock::new_for_test(test_block);
            commit_vote_monitor.observe_block(&block);
        }

        // Fetches should be scheduled until the unhandled commits threshold.
        commit_syncer.try_schedule_once();

        // Verify commit syncer is paused after scheduling 15 commits to index 25.
        assert_eq!(commit_syncer.unhandled_commits_threshold(), 25);
        assert_eq!(commit_syncer.highest_scheduled_index(), Some(30));
        let pending_fetches = commit_syncer.pending_fetches();
        assert_eq!(pending_fetches.len(), 3);

        // Indicate commit index 25 is consumed, and try to schedule again.
        commit_consumer_monitor.set_highest_handled_commit(25);
        commit_syncer.try_schedule_once();

        // Verify commit syncer schedules fetches up to index 35.
        assert_eq!(commit_syncer.highest_scheduled_index(), Some(30));
        let pending_fetches = commit_syncer.pending_fetches();
        assert_eq!(pending_fetches.len(), 3);

        // Verify contiguous ranges are scheduled.
        for (range, start) in pending_fetches.iter().zip((1..35).step_by(10)) {
            assert_eq!(range.start(), start);
            assert_eq!(range.end(), start + 9);
        }
    }
}
