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

    // --- stall detection ---
    /// Tracks the last time quorum_commit_index changed.
    /// Used to detect permanent stalls where quorum stays at 0.
    last_quorum_change_at: tokio::time::Instant,
    last_known_quorum: CommitIndex,

    // --- liveness stall detection ---
    /// Tracks the last time local_commit_index changed.
    /// Used to detect liveness stalls where ALL nodes stop advancing simultaneously
    /// (quorum > 0 but frozen). The existing quorum==0 detector misses this case.
    last_local_commit_change_at: tokio::time::Instant,
    last_known_local_commit: CommitIndex,

    // --- adaptive delay ---
    adaptive_delay_state: Option<Arc<AdaptiveDelayState>>,

    // --- active sync recovery ---
    /// Counter for Case B retries (schedule_recovery_pending + peers at same level).
    /// After N retries without network progress, escalates to Case C (genuine deadlock).
    /// This prevents permanent stalls when ALL nodes restore from snapshot simultaneously.
    active_sync_retry_count: u32,

    // --- FORK-SAFETY: network-validated commit gate (May 2026) ---
    /// Counts the number of certified commits actually fetched and processed from
    /// the network since the last time startup_sync_active was set. This prevents
    /// a premature CatchingUp→Healthy transition where synced_commit_index is
    /// seeded from baseline (matching quorum_commit on first check) but no actual
    /// network sync has occurred. The node MUST have processed at least 1 real
    /// commit from peers before it can clear startup_sync_active.
    network_synced_commits: u64,
}

// ═══════════════════════════════════════════════════════════════════════
// STATE MACHINE TYPES (May 2026 Refactor)
//
// These types formalize the phase determination logic:
//   - PhaseStateInput: immutable snapshot of all state needed for decisions
//   - PhaseTransitionDecision: describes WHAT to do (no side effects)
//   - CATCHING_UP_ENTER_THRESHOLD: hysteresis constant
// ═══════════════════════════════════════════════════════════════════════

/// Hysteresis threshold for entering CatchingUp from Healthy.
/// Prevents rapid CatchingUp↔Healthy oscillation when the node is
/// perpetually 1-2 commits behind:
///   - Enter CatchingUp: lag > CATCHING_UP_ENTER_THRESHOLD
///   - Stay in CatchingUp: lag > 0
///   - Enter Healthy: lag == 0
const CATCHING_UP_ENTER_THRESHOLD: u32 = 5;

/// All inputs needed for phase determination — gathered once, used immutably.
/// This struct intentionally captures a snapshot of all state so that
/// `determine_phase()` is a pure function with no side effects.
#[derive(Debug, Clone, Copy)]
struct PhaseStateInput {
    /// Current phase of the node.
    current_phase: crate::coordination_hub::NodeConsensusPhase,
    /// How far behind the network quorum (quorum_commit - synced_commit_index).
    lag: u32,
    /// Highest commit index confirmed by network quorum.
    quorum_commit: u32,
    /// Highest commit index the Go execution layer has processed.
    highest_handled: u32,
    /// Highest commit index the CommitSyncer has synced to.
    synced_commit_index: u32,
    /// Number of certified commits actually fetched from network peers.
    /// Zero means only baseline-seeded, not real network data.
    network_synced_commits: u64,
    /// Whether STARTUP-SYNC is active (post-snapshot recovery in progress).
    startup_sync_active: bool,
    /// Whether Go has completed its startup peer-sync.
    go_sync_completed: bool,
    /// Whether the LeaderSchedule needs re-confirmation after snapshot recovery.
    schedule_recovery_pending: bool,
}

/// Result of phase determination — describes WHAT should happen, not HOW.
/// The `apply_transition()` method handles all side effects.
#[derive(Debug)]
enum PhaseTransitionDecision {
    /// Stay in current phase, no action needed.
    Hold { reason: &'static str },
    /// Transition to a new phase.
    Transition { to: crate::coordination_hub::NodeConsensusPhase },
    /// Transition to new phase AND clear startup_sync_active first.
    /// Used when all startup recovery gates have been passed.
    TransitionAndUnlock { to: crate::coordination_hub::NodeConsensusPhase },
    /// Seed the CommitVoteMonitor with execution state to break bootstrap deadlock.
    /// Used when Go has state but Rust DAG is empty and quorum == 0.
    SeedQuorum { commit_index: u32 },
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
        let dag_commit = inner.dag_state.read().last_commit_index();
        // FORK-SAFETY: DO NOT initialize with `handled_commit`!
        // If DAG was wiped (dag_commit = 0) but Go has state (handled_commit = 400),
        // we MUST start fetching from 0 to reconstruct the LeaderSchedule!
        // CommitProcessor will safely skip executing these already-handled commits.
        let synced_commit_index = dag_commit;
        
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
            last_quorum_change_at: tokio::time::Instant::now(),
            last_known_quorum: 0,
            last_local_commit_change_at: tokio::time::Instant::now(),
            last_known_local_commit: synced_commit_index,
            adaptive_delay_state,
            active_sync_retry_count: 0,
            network_synced_commits: 0,
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
        
        let supervisor_task = tokio::task::spawn(
            async move {
                let mut restart_delay = Duration::from_secs(1);
                loop {
                    tracing::info!("🛡️ [SUPERVISOR] Starting CommitSyncer task...");

                    // LIVENESS FIX: Before starting the new CommitSyncer, ensure
                    // CommitVoteMonitor has a non-zero quorum if Go has already
                    // processed blocks. Without this, the restarted CommitSyncer
                    // sees quorum=0 → bootstrap genesis timeout → Healthy with
                    // no proposals → permanent liveness stall after snapshot recovery.
                    let highest_handled = commit_consumer_monitor.highest_handled_commit();
                    if highest_handled > 0 {
                        commit_vote_monitor.seed_from_execution_state(highest_handled);
                        tracing::info!(
                            "🛡️ [SUPERVISOR] Pre-seeded CommitVoteMonitor with highest_handled={} before restart",
                            highest_handled
                        );
                    }

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
            }
        );

        CommitSyncerHandle {
            schedule_task: supervisor_task,
            tx_shutdown,
        }
    }
}

impl<C: NetworkClient> CommitSyncer<C> {
    // Derived interval 
    fn poll_interval(&self) -> Duration {
        match self.coordination_hub.get_phase() {
            crate::coordination_hub::NodeConsensusPhase::Initializing => Duration::from_secs(1),
            crate::coordination_hub::NodeConsensusPhase::CatchingUp => Duration::from_millis(150),
            crate::coordination_hub::NodeConsensusPhase::Bootstrapping => Duration::from_millis(200),
            crate::coordination_hub::NodeConsensusPhase::Aligning => Duration::from_millis(200),
            crate::coordination_hub::NodeConsensusPhase::Healthy => Duration::from_secs(2),
            crate::coordination_hub::NodeConsensusPhase::StateSyncing => Duration::from_secs(5),
        }
    }

    fn transition_phase_and_kick(&mut self, next_phase: crate::coordination_hub::NodeConsensusPhase) {
        let current_phase = self.coordination_hub.get_phase();
        if current_phase != next_phase {
            self.coordination_hub.set_phase(next_phase);
            if next_phase == crate::coordination_hub::NodeConsensusPhase::Healthy {
                let core_dispatcher = self.inner.core_thread_dispatcher.clone();
                tokio::spawn(async move {
                    tracing::info!("🏃 [LIVENESS] Kicking Core to resume proposals after transitioning to Healthy...");
                    if let Err(e) = core_dispatcher.new_block(consensus_types::block::Round::MAX, true).await {
                        tracing::warn!("Failed to kick Core thread to resume proposals: {:?}", e);
                    }
                });
            }
        }
    }

    // ═══════════════════════════════════════════════════════════════════════
    // STATE MACHINE: Refactored May 2026
    //
    // The phase determination logic is structured as a clean state machine:
    //
    //   update_state()                    — orchestrator
    //     ├── gather_state_input()        — snapshot all state (pure, no side effects)
    //     ├── determine_phase()           — match-based logic → TransitionDecision
    //     │     ├── bootstrap_exit()      — Initializing/Bootstrapping exit rules
    //     │     └── startup_sync_exit()   — CatchingUp exit during startup recovery
    //     └── apply_transition()          — execute side effects
    //
    // Design principles:
    //   1. gather → decide → apply (no side effects during decision)
    //   2. Every path is a named match arm (auditable, no hidden fallthrough)
    //   3. TransitionDecision describes WHAT, apply_transition() does HOW
    // ═══════════════════════════════════════════════════════════════════════

    /// All inputs needed for phase determination — gathered once, used immutably.
    fn gather_state_input(&self) -> PhaseStateInput {
        let highest_handled = self.inner.commit_consumer_monitor.highest_handled_commit();
        let quorum_commit = std::cmp::max(
            self.inner.commit_vote_monitor.quorum_commit_index(),
            self.coordination_hub.get_quorum_commit_index(),
        );
        let current_phase = self.coordination_hub.get_phase();
        let lag = quorum_commit.saturating_sub(self.synced_commit_index);

        PhaseStateInput {
            current_phase,
            lag,
            quorum_commit,
            highest_handled,
            synced_commit_index: self.synced_commit_index,
            network_synced_commits: self.network_synced_commits,
            startup_sync_active: self.coordination_hub.is_startup_sync_active(),
            go_sync_completed: self.coordination_hub.is_startup_go_sync_completed(),
            schedule_recovery_pending: self.coordination_hub.is_schedule_recovery_pending(),
        }
    }

    /// Pure logic: determine what phase transition should happen.
    /// Returns a TransitionDecision describing the action, with NO side effects.
    fn determine_phase(input: &PhaseStateInput) -> PhaseTransitionDecision {
        use crate::coordination_hub::NodeConsensusPhase::*;

        // ═══ INITIALIZING / BOOTSTRAPPING: Special exit logic ═══
        if matches!(input.current_phase, Initializing | Bootstrapping) {
            return Self::determine_bootstrap_exit(input);
        }

        // ═══ ALIGNING: Managed externally, don't touch ═══
        if matches!(input.current_phase, Aligning) {
            return PhaseTransitionDecision::Hold {
                reason: "Phase managed externally (Aligning)",
            };
        }

        // ═══ STATE SYNCING: Deep lag detected ═══
        if input.lag > 50_000 {
            return PhaseTransitionDecision::Transition { to: StateSyncing };
        }

        match input.current_phase {
            // ─── CATCHING UP during startup recovery ───
            CatchingUp if input.startup_sync_active => {
                Self::determine_startup_sync_exit(input)
            }

            // ─── CATCHING UP (normal): Stay until lag=0 ───
            CatchingUp if input.lag > 0 => PhaseTransitionDecision::Hold {
                reason: "CatchingUp — lag > 0, still syncing",
            },

            // ─── CATCHING UP (normal): Lag resolved → Healthy ───
            CatchingUp => PhaseTransitionDecision::Transition { to: Healthy },

            // ─── HEALTHY: Enter CatchingUp if significant lag ───
            Healthy if input.lag > CATCHING_UP_ENTER_THRESHOLD => {
                PhaseTransitionDecision::Transition { to: CatchingUp }
            }

            // ─── HEALTHY: Stay healthy (lag within tolerance) ───
            Healthy => PhaseTransitionDecision::Hold {
                reason: "Healthy — lag within threshold",
            },

            // ─── Catch-all: StateSyncing stays (managed by external trigger) ───
            _ => PhaseTransitionDecision::Hold {
                reason: "Phase managed externally",
            },
        }
    }

    /// Bootstrap exit logic — determines when to leave Initializing/Bootstrapping.
    ///
    /// Two distinct scenarios:
    ///   1. GENESIS START: Go has no state, DAG empty → propose block 1
    ///   2. SNAPSHOT RESTART: Go has state, DAG wiped → wait for quorum then catch up
    fn determine_bootstrap_exit(input: &PhaseStateInput) -> PhaseTransitionDecision {
        use crate::coordination_hub::NodeConsensusPhase::*;

        let next_phase_for_lag = if input.lag > 0 { CatchingUp } else { Healthy };

        match (input.highest_handled, input.quorum_commit, input.go_sync_completed) {
            // ── Case 1: No local state, quorum exists → NOT genesis, DAG wipe ──
            (0, quorum, _) if quorum > 0 => {
                tracing::info!(
                    "🚀 [BOOTSTRAP] highest_handled=0 but quorum={} found. \
                     NOT genesis — DAG wipe detected. Transitioning to {:?}.",
                    quorum, next_phase_for_lag
                );
                PhaseTransitionDecision::Transition { to: next_phase_for_lag }
            }

            // ── Case 2: No local state, no quorum, network polled → GENESIS ──
            (0, 0, true) => {
                tracing::info!(
                    "🚀 [BOOTSTRAP] Genesis detected (highest_handled=0, no quorum after network poll). \
                     Transitioning to {:?} to allow block 1 proposal.",
                    next_phase_for_lag
                );
                PhaseTransitionDecision::TransitionAndUnlock { to: next_phase_for_lag }
            }

            // ── Case 3: No local state, no quorum, still polling → WAIT ──
            (0, 0, false) => PhaseTransitionDecision::Hold {
                reason: "Bootstrap: highest_handled=0, quorum=0, waiting for network poll",
            },

            // ── Case 4: Has local state, quorum exists → SNAPSHOT RESTART ──
            (_, quorum, _) if quorum > 0 => {
                tracing::info!(
                    "🚀 [BOOTSTRAP] Snapshot restore complete. quorum={}, transitioning to {:?}.",
                    quorum, next_phase_for_lag
                );
                PhaseTransitionDecision::Transition { to: next_phase_for_lag }
            }

            // ── Case 5: Has local state, no quorum, network polled → SEED ──
            (handled, 0, true) => PhaseTransitionDecision::SeedQuorum {
                commit_index: handled,
            },

            // ── Case 6: Has local state, no quorum, still polling → WAIT ──
            (_, 0, false) => PhaseTransitionDecision::Hold {
                reason: "Bootstrap: has state but quorum=0, waiting for network poll",
            },

            // ── Unreachable: quorum can't be negative ──
            _ => PhaseTransitionDecision::Hold {
                reason: "Bootstrap: unexpected state",
            },
        }
    }

    /// Startup sync exit logic — determines when CatchingUp can transition to Healthy
    /// during post-snapshot recovery (startup_sync_active=true).
    ///
    /// Guards (all must pass):
    ///   1. lag == 0 (mathematical parity with quorum)
    ///   2. quorum_commit > 0 (real quorum, not stale)
    ///   3. quorum_commit >= highest_handled (quorum not behind execution)
    ///   4. network_synced_commits > 0 (real commits fetched, not baseline-seeded)
    ///   5. schedule_recovery_pending == false (LeaderSchedule confirmed)
    fn determine_startup_sync_exit(input: &PhaseStateInput) -> PhaseTransitionDecision {
        // Gate 1: Mathematical parity
        let has_parity = input.lag == 0
            && input.quorum_commit > 0
            && input.quorum_commit >= input.highest_handled;

        if !has_parity {
            return PhaseTransitionDecision::Hold {
                reason: "Startup sync: parity not reached (lag > 0 or quorum stale)",
            };
        }

        // Gate 2: Network-validated commits (prevent baseline-only false parity)
        if input.network_synced_commits == 0 {
            tracing::warn!(
                "⚠️ [COMMIT-SYNCER] Mathematical parity reached (synced={} >= quorum={}), \
                 but network_synced_commits=0 — no actual commits fetched from peers yet. \
                 Blocking CatchingUp→Healthy to prevent baseline-only false parity.",
                input.synced_commit_index, input.quorum_commit
            );
            return PhaseTransitionDecision::Hold {
                reason: "Startup sync: no network-validated commits yet",
            };
        }

        // Gate 3: LeaderSchedule confirmed
        if input.schedule_recovery_pending {
            tracing::warn!(
                "⚠️ [COMMIT-SYNCER] Mathematical parity reached (synced={} >= quorum={}), \
                 but LeaderSchedule recovery is still pending! \
                 Node MUST NOT exit CatchingUp yet to prevent fork.",
                input.synced_commit_index, input.quorum_commit
            );
            return PhaseTransitionDecision::Hold {
                reason: "Startup sync: LeaderSchedule recovery pending",
            };
        }

        // All gates passed — safe to unlock
        tracing::info!(
            "✅ [COMMIT-SYNCER] Mathematical parity reached (synced={} >= quorum={}, \
             network_synced={}) and LeaderSchedule is fully recovered. \
             Explicitly unlocking node.",
            input.synced_commit_index, input.quorum_commit, input.network_synced_commits
        );
        PhaseTransitionDecision::TransitionAndUnlock {
            to: crate::coordination_hub::NodeConsensusPhase::Healthy,
        }
    }

    /// Execute the side effects of a transition decision.
    /// This is the ONLY place where state is mutated during update_state().
    fn apply_transition(&mut self, decision: PhaseTransitionDecision, input: &PhaseStateInput) {
        match decision {
            PhaseTransitionDecision::Hold { reason } => {
                // No action needed — stay in current phase.
                tracing::trace!("🛡️ [STATE-MACHINE] PhaseTransitionDecision::Hold - reason: {}", reason);
            }

            PhaseTransitionDecision::Transition { to } => {
                if input.current_phase != to {
                    self.transition_phase_and_kick(to);
                }
            }

            PhaseTransitionDecision::TransitionAndUnlock { to } => {
                // Clear startup_sync BEFORE transitioning (so choke-point guard allows it)
                if self.coordination_hub.is_startup_sync_active() {
                    self.coordination_hub.set_startup_sync_active(false);
                }
                if input.current_phase != to {
                    self.transition_phase_and_kick(to);
                }
            }

            PhaseTransitionDecision::SeedQuorum { commit_index } => {
                let seeded = self.inner.commit_vote_monitor.seed_from_execution_state(commit_index);
                if seeded {
                    tracing::warn!(
                        "🌱 [QUORUM-SEED] Seeded CommitVoteMonitor with Go execution state \
                         (commit_index={}) after network poll confirmed to break bootstrap deadlock.",
                        commit_index
                    );
                }
            }
        }
    }

    /// Main state update — orchestrates the gather → decide → apply cycle.
    fn update_state(&mut self) {
        let input = self.gather_state_input();
        let decision = Self::determine_phase(&input);
        self.apply_transition(decision, &input);
    }

    async fn schedule_loop(mut self, mut rx_shutdown: oneshot::Receiver<()>) {
        eprintln!(
            "🔧 [COMMIT-SYNCER-LOOP] ENTERED! phase={:?}, synced={}, interval={}ms",
            self.coordination_hub.get_phase(),
            self.synced_commit_index,
            self.poll_interval().as_millis()
        );

        // CRITICAL RECOVERY GUARD:
        // Synchronously patch the baseline (fetching digest + reputation_scores) BEFORE
        // entering the loop and allowing any fetches! If we don't block here, `try_schedule_once`
        // might fetch commits and send them to `commit_manager` before the scores are restored,
        // causing `commit_manager` to evaluate synced commits with an empty default schedule.
        self.patch_baseline_if_needed().await;

        // CRITICAL FIX FOR DAG-GATE RACE CONDITION:
        // Passively waiting for CommitVoteMonitor to receive votes fails if the cluster
        // is idle (no new commits being produced). This causes local `quorum_commit` to stay 0,
        // tricking the node into transitioning to `Healthy` prematurely and causing forks.
        // We must ACTIVELY discover the true quorum commit from peers before starting.
        self.discover_quorum_commit().await;

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
                        self.patch_baseline_if_needed().await;
                        let new_state = self.coordination_hub.get_phase();
                        
                        let local_commit = self.inner.dag_state.read().last_commit_index();
                        let quorum_commit = self.inner.commit_vote_monitor.quorum_commit_index();
                        let lag = quorum_commit.saturating_sub(local_commit);

                        // ════════════════════════════════════════════════════════
                        // STALL DETECTOR 1: Detect permanent quorum stall and
                        // auto-recover by re-seeding from Go execution state.
                        // Triggers when quorum is stuck at 0 (bootstrap deadlock).
                        // ════════════════════════════════════════════════════════
                        if quorum_commit != self.last_known_quorum {
                            self.last_quorum_change_at = now;
                            self.last_known_quorum = quorum_commit;
                        }
                        let highest_handled = self.inner.commit_consumer_monitor.highest_handled_commit();
                        let stall_duration = now.duration_since(self.last_quorum_change_at);
                        if stall_duration >= Duration::from_secs(30)
                            && quorum_commit == 0
                            && highest_handled > 0
                            && self.coordination_hub.is_healthy()
                        {
                            tracing::error!(
                                "🚨 [STALL-DETECTOR] Quorum stuck at 0 for {:.0}s while Go has block state \
                                 (highest_handled={}). Re-seeding quorum and transitioning to Bootstrapping.",
                                stall_duration.as_secs_f64(),
                                highest_handled
                            );
                            let seeded = self.inner.commit_vote_monitor.seed_from_execution_state(highest_handled);
                            if seeded {
                                self.coordination_hub.set_phase(
                                    crate::coordination_hub::NodeConsensusPhase::Bootstrapping
                                );
                                self.last_quorum_change_at = now; // reset to avoid re-triggering immediately
                            }
                        }

                        // ════════════════════════════════════════════════════════
                        // STALL DETECTOR 2: Detect liveness stall where ALL nodes
                        // stop advancing simultaneously (quorum > 0 but frozen).
                        //
                        // The quorum==0 detector above misses this case because
                        // quorum is non-zero (all nodes agreed on the same commit).
                        // This can happen when:
                        //   - All nodes' threshold_clock_round stops advancing
                        //   - Broadcaster/Synchronizer tasks silently exit
                        //   - Leader timeout fires but can't advance round
                        //
                        // Recovery: Temporarily transition to Bootstrapping to
                        // kick the consensus pipeline. update_state() will
                        // re-evaluate on the next tick and restore Healthy if
                        // local_commit == quorum_commit.
                        // ════════════════════════════════════════════════════════
                        if local_commit != self.last_known_local_commit {
                            self.last_local_commit_change_at = now;
                            self.last_known_local_commit = local_commit;
                        }
                        let liveness_stall_duration = now.duration_since(self.last_local_commit_change_at);
                        if liveness_stall_duration >= Duration::from_secs(60)
                            && local_commit > 0
                            && self.coordination_hub.is_healthy()
                        {
                            tracing::error!(
                                "🚨 [LIVENESS-STALL] DAG commit frozen at {} for {:.0}s (quorum={}). \
                                 All nodes may have stalled simultaneously. \
                                 Transitioning to Bootstrapping to kick consensus pipeline.",
                                local_commit,
                                liveness_stall_duration.as_secs_f64(),
                                quorum_commit
                            );
                            self.coordination_hub.set_phase(
                                crate::coordination_hub::NodeConsensusPhase::Bootstrapping
                            );
                            // Re-seed quorum from local state so bootstrap exit logic works
                            self.inner.commit_vote_monitor.seed_from_execution_state(
                                std::cmp::max(highest_handled, local_commit)
                            );
                            self.last_local_commit_change_at = now; // reset to avoid rapid re-triggering
                            self.last_quorum_change_at = now;
                        }

                        // ════════════════════════════════════════════════════════
                        // STALL DETECTOR 3: All-zero deadlock after bootstrap.
                        //
                        // After DAG wipe + epoch transition + Supervisor restart,
                        // ALL nodes can end up in Healthy with local_commit=0,
                        // quorum=0. Neither Detector 1 (needs highest_handled>0)
                        // nor Detector 2 (needs local_commit>0) fires.
                        // The bootstrap timeout assumed genesis and transitioned
                        // to Healthy, but the one-shot kick failed to achieve
                        // quorum. No node proposes → permanent stall.
                        //
                        // Recovery: periodically kick Core to force proposals.
                        // Once any proposal achieves quorum, normal consensus
                        // resumes and this detector stops firing.
                        // ════════════════════════════════════════════════════════
                        let is_dag_empty = self.inner.dag_state.read().last_commit.is_none();
                        if is_dag_empty
                            && quorum_commit == 0
                            && highest_handled == 0
                            && self.coordination_hub.is_healthy()
                            && liveness_stall_duration >= Duration::from_secs(30)
                        {
                            tracing::error!(
                                "🚨 [ZERO-DEADLOCK] All-zero state for {:.0}s (local=0, quorum=0, highest_handled=0, phase=Healthy). \
                                 Kicking Core to force block proposal.",
                                liveness_stall_duration.as_secs_f64()
                            );
                            let core_dispatcher = self.inner.core_thread_dispatcher.clone();
                            tokio::spawn(async move {
                                if let Err(e) = core_dispatcher.new_block(
                                    consensus_types::block::Round::MAX, true
                                ).await {
                                    tracing::warn!("Failed to kick Core for zero-deadlock recovery: {:?}", e);
                                }
                            });
                            self.last_local_commit_change_at = now; // prevent rapid re-triggers
                        }

                        // ════════════════════════════════════════════════════════
                        // ACTIVE PEER SYNC RECOVERY (replaces Stall Detector 6)
                        //
                        // Design philosophy: Blockchain MUST NEVER fork. Instead of
                        // using timeouts to decide when to unlock the local committer,
                        // the system always asks peers for their state and uses DATA
                        // to determine the correct action.
                        //
                        // 5 Cases (all data-driven, no timeout-based unlock):
                        //
                        // Case A: Peers AHEAD → update quorum → CommitSyncer fetches
                        //         → add_certified_commits → unlock naturally
                        // Case B: Peers SAME + schedule NOT confirmed → trigger
                        //         active commit fetch to rebuild LeaderSwapTable
                        //         → schedule confirmed → unlock via data
                        // Case C: Peers SAME + schedule confirmed → genuine deadlock
                        //         → safe to unlock (all nodes have verified state)
                        // Case D: Genesis (local=0, quorum=0) → handled by Stall
                        //         Detector 3 (Core kick)
                        // Case E: No peers reachable → wait, retry next cycle
                        //         → NEVER act without data
                        //
                        // History: Block #1184 fork was caused by timeout-based unlock
                        // (5s → poll → unlock) during snapshot recovery. The node
                        // unlocked with a stale LeaderSwapTable, producing divergent
                        // leader elections and timestamps.
                        // ════════════════════════════════════════════════════════
                        if self.coordination_hub.is_healthy()
                            && !self.coordination_hub.is_local_commit_unlocked()
                            && now.duration_since(self.last_quorum_change_at) >= Duration::from_secs(5)
                        {
                            let inner = self.inner.clone();
                            let hub = self.coordination_hub.clone();
                            let my_commit = local_commit;
                            let is_schedule_pending = self.coordination_hub.is_schedule_recovery_pending();
                            let retry_count = self.active_sync_retry_count;

                            // Increment retry counter for Case B tracking BEFORE spawn.
                            // Reset happens when we exit this branch (node unlocked, or not healthy).
                            if is_schedule_pending {
                                self.active_sync_retry_count += 1;
                            } else {
                                self.active_sync_retry_count = 0;
                            }

                            tokio::spawn(async move {
                                tracing::info!(
                                    "🔍 [ACTIVE-SYNC-RECOVERY] Node is Healthy but locked. \
                                     Polling peers to determine recovery action (my_commit={}, schedule_pending={}, retry={})...",
                                    my_commit, is_schedule_pending, retry_count
                                );
                                let mut max_peer_commit: u32 = 0;
                                let mut polled_peers: u32 = 0;
                                let timeout = Duration::from_secs(2);
                                
                                for authority in inner.context.committee.authorities().map(|(i, _)| i) {
                                    if authority == inner.context.own_index {
                                        continue;
                                    }
                                    if let Ok(status) = inner.network_client.get_epoch_status(authority, timeout).await {
                                        max_peer_commit = std::cmp::max(max_peer_commit, status.last_commit_index);
                                        polled_peers += 1;
                                    }
                                }

                                // ── Case E: No peers reachable ──
                                // Never act without data from the network.
                                if polled_peers == 0 {
                                    tracing::warn!(
                                        "⚠️ [ACTIVE-SYNC-RECOVERY] Case E: Failed to reach any peers. \
                                         Cannot determine network state. Will retry next cycle. \
                                         NEVER acting without peer data."
                                    );
                                    return;
                                }

                                // ── Case A: Peers are AHEAD ──
                                // The node is lagging. Update quorum so CommitSyncer
                                // transitions to CatchingUp and fetches CertifiedCommits.
                                // The commits will flow through add_certified_commits()
                                // which unlocks the local committer naturally (data-driven).
                                if max_peer_commit > my_commit {
                                    tracing::info!(
                                        "📥 [ACTIVE-SYNC-RECOVERY] Case A: Peer has commit {} > {} (ours). \
                                         Updating quorum to trigger CatchingUp → fetch → unlock via data.",
                                        max_peer_commit, my_commit
                                    );
                                    hub.update_quorum_commit_index(max_peer_commit);
                                    return;
                                }

                                // Peers are at SAME commit level — potential deadlock.
                                // But we must verify safety before unlocking.
                                if hub.is_startup_sync_active() {
                                    tracing::info!(
                                        "🔒 [ACTIVE-SYNC-RECOVERY] STARTUP-SYNC still active. \
                                         Waiting for sync completion before any recovery action."
                                    );
                                    return;
                                }

                                // ── Case B: Peers SAME + schedule NOT confirmed ──
                                // After snapshot recovery, the LeaderSwapTable is stale.
                                // We CANNOT unlock — local commits would use wrong leader
                                // elections → fork. Instead, ACTIVELY fetch CertifiedCommits
                                // from peers to rebuild the schedule.
                                //
                                // We NEVER unlock based on timeout here. The system MUST wait 
                                // until it successfully receives the commits to reconstruct the DAG.
                                if is_schedule_pending {
                                    let num_commits: u32 = 300;
                                    let last_boundary = (my_commit / num_commits) * num_commits;
                                    let fetch_from = last_boundary.saturating_sub(num_commits);
                                    tracing::warn!(
                                        "📥 [ACTIVE-SYNC-RECOVERY] Case B (retry {}): Peers at same commit ({}) \
                                         but schedule_recovery_pending=true. LeaderSwapTable is stale. \
                                         NOT unlocking (would cause fork). \
                                         Triggering active fetch of commits {}→{} to rebuild schedule.",
                                        retry_count + 1, my_commit, fetch_from, my_commit
                                    );
                                    // Trigger CommitSyncer to re-fetch the scoring range.
                                    // By updating quorum slightly above my_commit, the
                                    // CommitSyncer's try_schedule_once will detect lag and
                                    // schedule fetches. The fetched CertifiedCommits will
                                    // flow through the normal pipeline, rebuilding the
                                    // LeaderSwapTable via update_leader_schedule_v2().
                                    hub.update_quorum_commit_index(my_commit.saturating_add(1));
                                    return;
                                }

                                // ── Case C: Peers SAME + schedule IS confirmed ──
                                // (or Case B exhausted → escalated to Case C)
                                // This is a GENUINE cluster-wide deadlock:
                                // - All nodes are at the same commit
                                // - No node is progressing
                                // - The LeaderSwapTable is verified/confirmed (or exhaustion-cleared)
                                // - The local committer is locked (all nodes waiting)
                                //
                                // This is the ONLY case where direct unlock is safe,
                                // because all nodes have identical verified state.
                                // Typically occurs during cluster-wide cold start.
                                tracing::warn!(
                                    "🚨 [ACTIVE-SYNC-RECOVERY] Case C: Genuine deadlock confirmed. \
                                     Polled {} peers, max_peer={}, my_commit={}. \
                                     Schedule is confirmed. All nodes have verified state. \
                                     Unlocking local committer to break deadlock.",
                                    polled_peers, max_peer_commit, my_commit
                                );
                                hub.unlock_local_commit();

                                // Kick the core to evaluate the committer immediately
                                let core_dispatcher = inner.core_thread_dispatcher.clone();
                                if let Err(e) = core_dispatcher.new_block(
                                    consensus_types::block::Round::MAX, true
                                ).await {
                                    tracing::warn!("Failed to kick Core for deadlock recovery: {:?}", e);
                                }
                            });
                            self.last_quorum_change_at = now; // reset to avoid rapid re-trigger
                        } else {
                            // Reset retry counter when not in Active Peer Sync trigger
                            // (node is unlocked, not healthy, or hasn't been stuck 5s)
                            self.active_sync_retry_count = 0;
                        }

                        // ════════════════════════════════════════════════════════
                        // STALL DETECTOR 4: Cluster Cold Start Deadlock
                        // If local_commit == 0 (DAG wiped) and we are stuck in CatchingUp
                        // for 5s without fetching, it means the ENTIRE cluster wiped
                        // its DAG. No one has the past commits. Force fast-forward.
                        // ════════════════════════════════════════════════════════
                        let is_dag_empty = self.inner.dag_state.read().last_commit.is_none();
                        let catching_up_stall = now.duration_since(self.last_quorum_change_at);
                        let is_fetching = !self.inflight_fetches.is_empty();
                        
                        if self.coordination_hub.is_catching_up()
                            && is_dag_empty
                            && highest_handled > 0
                            && catching_up_stall >= Duration::from_secs(5)
                            && !is_fetching
                        {
                            tracing::error!(
                                "🚨 [STALL-DETECTOR] Node stuck in CatchingUp for {:.0}s and NOT fetching. \
                                 No peers have the past commits. Forcing fast-forward to highest_handled={}.",
                                catching_up_stall.as_secs_f64(),
                                highest_handled
                            );
                            self.inner.dag_state.write().reset_to_network_baseline(0, highest_handled, crate::commit::CommitDigest::MIN, 0, None);
                            self.synced_commit_index = highest_handled;
                            self.last_quorum_change_at = now; // reset to avoid rapid re-trigger
                        }

                        // ════════════════════════════════════════════════════════
                        // STALL DETECTOR 5: Post-Epoch-Transition Recovery
                        //
                        // After epoch transition, the new AuthorityNode creates a
                        // fresh CommitConsumerMonitor with highest_handled=0 and
                        // a fresh DAG with local_commit=0. The node discovers
                        // quorum from peers (e.g. 1101) and enters CatchingUp.
                        //
                        // Stall Detector 4 requires highest_handled > 0, which
                        // is FALSE here because the new epoch starts from commit 0.
                        // Result: the node is permanently stuck in CatchingUp.
                        //
                        // Recovery: When CatchingUp with empty DAG, quorum > 0,
                        // and highest_handled == 0, fast-forward the DAG baseline
                        // to quorum_commit. This lets the node transition to
                        // Healthy and start proposing/committing new blocks.
                        // ════════════════════════════════════════════════════════
                        if self.coordination_hub.is_catching_up()
                            && is_dag_empty
                            && highest_handled == 0
                            && quorum_commit > 0
                            && catching_up_stall >= Duration::from_secs(5)
                            && !is_fetching
                        {
                            tracing::error!(
                                "🚨 [STALL-DETECTOR-5] Post-epoch-transition stall: CatchingUp for {:.0}s \
                                 with empty DAG, highest_handled=0, quorum={}, and NOT fetching. \
                                 New epoch has no local state. Fast-forwarding to quorum.",
                                catching_up_stall.as_secs_f64(),
                                quorum_commit
                            );
                            self.inner.dag_state.write().reset_to_network_baseline(
                                0, quorum_commit,
                                crate::commit::CommitDigest::MIN,
                                0, None
                            );
                            self.synced_commit_index = quorum_commit;
                            self.last_quorum_change_at = now;
                        }

                        info!(
                            "🛡️ [UNIFIED STATE] Phase: {:?} | Local DAG Commit: {} | Network Quorum: {} | Lag: {} | Block Source: {}",
                            new_state, local_commit, quorum_commit, lag,
                            match new_state {
                                crate::coordination_hub::NodeConsensusPhase::Initializing => "INITIALIZING",
                                crate::coordination_hub::NodeConsensusPhase::CatchingUp => "SYNC_ONLY (CatchingUp)",
                                crate::coordination_hub::NodeConsensusPhase::Bootstrapping => "BOOTSTRAPPING",
                                crate::coordination_hub::NodeConsensusPhase::Aligning => "ALIGNING (Go↔Rust)",
                                crate::coordination_hub::NodeConsensusPhase::Healthy => "CONSENSUS (Healthy)",
                                crate::coordination_hub::NodeConsensusPhase::StateSyncing => "CONSENSUS (StateSyncing)",
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
        if self.coordination_hub.is_state_syncing() {
            tracing::info!("⏳ [COMMIT-SYNCER] Node is in StateSyncing phase. Pausing batch block downloads until state snapshot is applied.");
            return;
        }

        let quorum_commit_index = std::cmp::max(
            self.inner.commit_vote_monitor.quorum_commit_index(),
            self.coordination_hub.get_quorum_commit_index(),
        );
        let local_commit_index = self.inner.dag_state.read().last_commit_index();
        
        // Update CoordinationHub so Core can read it to prevent divergent local commits
        self.coordination_hub.update_quorum_commit_index(quorum_commit_index);

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

        // Track synced commits: use the max of local DAG commit and current synced.
        // FORK-SAFETY: During startup sync OR catch-up, don't advance from DAG state alone —
        // the DAG can be reconstructed/populated from peers before Go processes the commits.
        // Only advance when Healthy (all commits already delivered to Core).
        if !self.coordination_hub.is_startup_sync_active() && !self.coordination_hub.is_catching_up() {
            self.synced_commit_index = self.synced_commit_index.max(local_commit_index);
        }

        // If synced_commit_index was forcibly lowered, ensure highest_scheduled doesn't block it
        if let Some(scheduled) = self.highest_scheduled_index {
            if scheduled > self.synced_commit_index && local_commit_index > self.inner.commit_consumer_monitor.highest_handled_commit() + 10 {
                self.highest_scheduled_index = Some(self.synced_commit_index);
            }
        }


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

    /// Fetches the real digest and timestamp from the network for a synthetic baseline commit.
    /// This ensures timestamp monotonicity calculations work correctly for subsequent commits
    /// after a Node fast-forwards its DAG from an empty state to match Go's catchup sync progress.
    async fn patch_baseline_if_needed(&mut self) {
        if self.synced_commit_index == 0 { return; }
        
        // A synthetic baseline needs patching if it was created by reset_to_network_baseline
        // with CommitDigest::MIN as previous_digest and BlockDigest::MIN as leader block digest.
        // We check previous_digest (the field we explicitly set to MIN) rather than
        // reference().digest (which is a computed hash of the serialized commit — never MIN).
        let is_synthetic_baseline = {
            let dag = self.inner.dag_state.read();
            if let Some(ref last_commit) = dag.last_commit {
                use crate::commit::CommitAPI;
                last_commit.index() == self.synced_commit_index
                    && last_commit.previous_digest() == crate::commit::CommitDigest::MIN
                    && last_commit.leader().digest == consensus_types::block::BlockDigest::MIN
            } else {
                false
            }
        };

        if !is_synthetic_baseline { return; }
        
        let prev_index = self.synced_commit_index;
        let interval = crate::leader_schedule::LeaderSchedule::commits_per_schedule() as u32;
        let last_schedule_change_index = (prev_index / interval) * interval;
        
        tracing::info!("🔗 Fetching network digest and timestamp for baseline commit #{}", prev_index);
        
        loop {
            let mut target_authorities = self.inner.context.committee.authorities()
                .filter_map(|(i, _)| if i != self.inner.context.own_index { Some(i) } else { None })
                .collect::<Vec<_>>();
                
            use rand::seq::SliceRandom;
            target_authorities.shuffle(&mut rand::thread_rng());
            
            let range: crate::commit::CommitRange = (prev_index..=prev_index).into();
            
            for authority in target_authorities.clone() {
                if let Ok(Ok((serialized_commits, _, commit_infos))) = tokio::time::timeout(
                    Duration::from_secs(5),
                    self.inner.network_client.fetch_commits(authority, range.clone(), Duration::from_secs(4))
                ).await {
                    if let Some(serialized) = serialized_commits.first() {
                        if let Ok(commit) = bcs::from_bytes::<crate::commit::Commit>(serialized) {
                            use crate::commit::CommitAPI; // Import the trait for .timestamp_ms()
                            let timestamp_ms = commit.timestamp_ms(); 
                            let leader_round = commit.leader().round;
                            let digest = crate::commit::TrustedCommit::compute_digest(serialized);
                            
                            // Extract reputation scores from commit_infos if present
                            let mut reputation_scores: Option<Vec<(AuthorityIndex, u64)>> = None;
                            if let Some(info_bytes) = commit_infos.first() {
                                if let Ok(info) = bcs::from_bytes::<crate::commit::CommitInfo>(info_bytes) {
                                    if !info.reputation_scores.scores_per_authority.is_empty() {
                                        let context_arc = self.inner.context.clone();
                                        reputation_scores = Some(info.reputation_scores.authorities_by_score(context_arc));
                                    }
                                }
                            }

                            if last_schedule_change_index > 0 && reputation_scores.is_none() {
                                tracing::info!(
                                    "ℹ️ [BASELINE] Schedule boundary at commit #{} noted, but reputation scores \
                                     cannot be extracted from serialized Commit. COLD-START-GUARD will prevent \
                                     local committer from using stale leader schedule.",
                                    last_schedule_change_index
                                );
                            } else if reputation_scores.is_some() {
                                tracing::info!(
                                    "ℹ️ [BASELINE] Schedule boundary at commit #{} noted, and reputation scores \
                                     were successfully extracted from network. Node can compute LeaderSchedule.",
                                    last_schedule_change_index
                                );
                            }
                            
                            self.inner.dag_state.write().reset_to_network_baseline(
                                leader_round,
                                prev_index,
                                digest,
                                timestamp_ms,
                                reputation_scores,
                            );
                            tracing::info!("✅ Baseline commit #{} successfully patched with digest {} and timestamp {}", prev_index, digest, timestamp_ms);
                            return;
                        }
                    }
                }
            }
            tracing::warn!("⚠️ Failed to fetch baseline commit #{} from network. Retrying in 1s...", prev_index);
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
    }

    /// Actively polls peers to establish the true quorum commit index before starting.
    /// This prevents the node from prematurely transitioning to Healthy phase when the cluster
    /// is idle and no P2P votes are being broadcasted.
    async fn discover_quorum_commit(&self) {
        tracing::info!("🔍 [BOOTSTRAP] Actively polling peers to discover true quorum_commit_index...");
        let mut max_peer_commit = 0;
        let mut polled_peers = 0;
        let timeout = Duration::from_secs(3);
        
        // Try up to 3 times to get responses from peers
        for attempt in 1..=3 {
            for authority in self.inner.context.committee.authorities().map(|(i, _)| i) {
                if authority == self.inner.context.own_index {
                    continue;
                }
                if let Ok(status) = self.inner.network_client.get_epoch_status(authority, timeout).await {
                    max_peer_commit = std::cmp::max(max_peer_commit, status.last_commit_index);
                    polled_peers += 1;
                }
            }
            
            if polled_peers > 0 {
                break;
            }
            tracing::warn!("⚠️ [BOOTSTRAP] Attempt {}/3: Failed to reach peers for quorum discovery. Retrying...", attempt);
            tokio::time::sleep(Duration::from_millis(500)).await;
        }
        
        if polled_peers > 0 {
            tracing::info!("✅ [BOOTSTRAP] Discovered max peer commit: {}. Updating quorum_commit_index.", max_peer_commit);
            self.coordination_hub.update_quorum_commit_index(max_peer_commit as u32);
        } else {
            tracing::warn!("⚠️ [BOOTSTRAP] Could not reach any peers. Proceeding with initial quorum=0.");
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
            let highest_handled = self.inner.commit_consumer_monitor.highest_handled_commit();
            let local_handled_gap = local_commit.saturating_sub(highest_handled);

            if local_commit > self.synced_commit_index {
                // ═══════════════════════════════════════════════════════════
                // FORK-SAFETY (May 2026): The DAG can be populated by peer
                // sync MUCH faster than Go can process commits. If we advance
                // synced_commit_index from DAG state, lag becomes 0, the node
                // transitions to Healthy, and starts proposing blocks BEFORE
                // Go has processed those commits → PERMANENT FORK.
                //
                // This happens in TWO scenarios:
                // 1. startup_sync_active: DAG reconstructed from 0 → 1300+
                // 2. CatchingUp: DAG fetches 51 commits in <100ms via peers
                //
                // Fix: Only advance synced_commit_index from DAG state when
                // the node is Healthy (not catching up or startup syncing).
                // During catch-up, synced_commit_index is only advanced via
                // the try_send_commits path (line ~1597) after commits are
                // actually delivered to Core.
                // ═══════════════════════════════════════════════════════════
                let is_catching_up = self.coordination_hub.is_catching_up();
                let is_startup = self.coordination_hub.is_startup_sync_active();
                
                if is_startup || is_catching_up {
                    tracing::debug!(
                        "[COMMIT-SYNCER] BLOCKED synced_commit_index advance {} → {} \
                         (startup_sync={}, catching_up={}, waiting for commits to be delivered to Core)",
                        self.synced_commit_index, local_commit, is_startup, is_catching_up
                    );
                } else if local_handled_gap > 10 {
                    tracing::info!(
                        "[COMMIT-SYNCER] Advancing synced_commit_index {} → {} despite handled gap {} \
                         (Go Master will catch up via batch-drain, CommitProcessor handles ordering)",
                        self.synced_commit_index, local_commit, local_handled_gap
                    );
                    self.synced_commit_index = local_commit;
                } else {
                    tracing::info!(
                        "[COMMIT-SYNCER] Advancing synced_commit_index {} → {} (from local DAG, phase={:?})",
                        self.synced_commit_index,
                        local_commit,
                        self.coordination_hub.get_phase()
                    );
                    self.synced_commit_index = local_commit;
                }
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
            // FORK-SAFETY: Track that we fetched REAL commits from the network.
            // This counter gates the CatchingUp→Healthy transition to prevent
            // false parity from baseline-only synced_commit_index.
            let commits_in_range = fetched_commit_range.end().saturating_sub(fetched_commit_range.start()) + 1;
            self.network_synced_commits += commits_in_range as u64;
            info!(
                "[NODE4-DEBUG] synced_commit_index advanced to {} (network_synced_commits={})",
                self.synced_commit_index, self.network_synced_commits
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
        mut commit_range: CommitRange,
        timeout: Duration,
        is_severe_lag: bool,
    ) -> ConsensusResult<CertifiedCommits> {
        let _timer = inner
            .context
            .metrics
            .node_metrics
            .commit_sync_fetch_once_latency
            .start_timer();

        // 1. Query peer epoch status to proactively truncate cross-epoch fetches.
        // This prevents the syncer from requesting a range that straddles the peer's
        // current epoch boundary, which would otherwise result in 'UnexpectedStartCommit'
        // or 'WrongEpoch' rejections from historical stores.
        match inner
            .network_client
            .get_epoch_status(target_authority, timeout)
            .await
        {
            Ok(status) => {
                let peer_start = status.current_epoch_start_commit;
                if peer_start > 0 && commit_range.start() < peer_start && commit_range.end() >= peer_start {
                    tracing::info!(
                        "[COMMIT-SYNCER] Truncating fetch range {:?} from {} to end at {} (peer's epoch {} starts at {})",
                        commit_range,
                        target_authority,
                        peer_start - 1,
                        status.epoch,
                        peer_start
                    );
                    commit_range = CommitRange::new(commit_range.start()..=peer_start - 1);
                } else if peer_start > 0 && commit_range.start() >= peer_start && commit_range.end() > status.last_commit_index {
                    // Also useful: don't fetch past the peer's last commit index if we know it.
                    let max_end = status.last_commit_index.max(commit_range.start());
                    if max_end < commit_range.end() {
                         commit_range = CommitRange::new(commit_range.start()..=max_end);
                    }
                }
            }
            Err(e) => {
                tracing::debug!("Failed to query epoch status from {}: {}", target_authority, e);
                // Continue with original range if query fails; legacy nodes might not support it.
            }
        }

        // 2. Fetch commits in the commit range from the target authority.
        let (serialized_commits, serialized_blocks, _commit_infos) = inner
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
                        let serialized: Bytes = serialized.into();
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
            // CROSS-EPOCH FIX: Use verify_for_commit_sync instead of verify_and_vote.
            // During commit sync, vote blocks may belong to a previous epoch when the
            // commit range spans epoch boundaries (e.g., snapshot restore). These blocks
            // are already quorum-certified, so re-checking epoch is incorrect and causes
            // permanent sync stalls (the "Block has wrong epoch" deadlock).
            let (block, reject_transaction_votes) =
                self.block_verifier.verify_for_commit_sync(block, serialized)?;
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

        // Bypass quorum check for FETCH-COMMITS to fix deadlock.
        // The master node's GEI determinism guarantees the commit sequence is correct.
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
