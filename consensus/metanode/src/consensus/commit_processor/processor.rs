// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use consensus_core::{BlockAPI, CommittedSubDag};
use mysten_metrics::monitored_mpsc::UnboundedReceiver;
use std::collections::BTreeMap;
use std::sync::atomic::AtomicBool;
use std::sync::Arc;
// [Added] Import Duration
// [Added] Import sleep for retry mechanism
use tracing::{error, info, trace, warn};

use crate::consensus::tx_recycler::TxRecycler;

use crate::node::executor_client::ExecutorClient;
// calculate_transaction_hash_hex removed: was only used for dead logging code

/// Commit processor that ensures commits are executed in order
pub struct CommitProcessor {
    receiver: UnboundedReceiver<CommittedSubDag>,
    next_expected_index: u32, // CommitIndex is u32
    pending_commits: BTreeMap<u32, CommittedSubDag>,
    /// Optional callback to notify commit index updates (for epoch transition)
    commit_index_callback: Option<Arc<dyn Fn(u32) + Send + Sync>>,
    /// Optional callback to update global execution index after successful commit
    global_exec_index_callback: Option<Arc<dyn Fn(u64) + Send + Sync>>,
    /// Current epoch (for deterministic global_exec_index calculation)
    current_epoch: u64,
    /// [PHASE-A DEPRECATED] epoch_base_index_override is no longer used for GEI computation.
    /// Go is the Single Writer for GEI. Kept for `with_epoch_info()` API compatibility.
    epoch_base_index_override: Option<u64>,
    /// Shared last global exec index for direct updates
    shared_last_global_exec_index: Option<Arc<tokio::sync::Mutex<u64>>>,
    /// Optional executor client to send blocks to Go executor
    executor_client: Option<Arc<ExecutorClient>>,
    /// Flag indicating if epoch transition is in progress
    /// When true, we're transitioning to a new epoch
    is_transitioning: Option<Arc<AtomicBool>>,
    /// Channel to send validated commits to the BlockDeliveryManager
    delivery_sender: Option<tokio::sync::mpsc::Sender<crate::node::block_delivery::ValidatedCommit>>,
    /// Queue for transactions that must be retried in the next epoch
    pending_transactions_queue: Option<Arc<tokio::sync::Mutex<Vec<Vec<u8>>>>>,
    /// Optional callback to handle EndOfEpoch system transactions
    /// Called immediately when an EndOfEpoch system transaction is detected in a committed sub-dag
    /// Uses commit finalization approach (like Sui) - no buffer needed as commits are processed sequentially
    epoch_transition_callback: Option<Arc<dyn Fn(u64, u64, u64) -> Result<()> + Send + Sync>>, // CHANGED: u32 -> u64
    /// Multi-epoch committee cache: ETH addresses keyed by epoch
    /// Supports looking up leaders from previous epochs during transitions
    /// RS-1: Uses RwLock instead of Mutex — reads (every commit) don't block each other,
    /// only writes (epoch transition) take exclusive lock.
    epoch_eth_addresses: Arc<tokio::sync::RwLock<std::collections::HashMap<u64, Vec<Vec<u8>>>>>,
    /// TX recycler for confirming committed TXs
    tx_recycler: Option<Arc<TxRecycler>>,


    /// RS-2: Storage path for persistence
    storage_path: Option<std::path::PathBuf>,
    /// [PHASE-A DEPRECATED] recovered_fragment_offset is no longer used.
    /// Go is the Single Writer for GEI — Rust doesn't need to track fragment offsets.
    recovered_fragment_offset: Option<u64>,
    /// Channel sender for emitting lag alerts
    lag_alert_sender: Option<
        tokio::sync::mpsc::UnboundedSender<
            crate::consensus::commit_processor::lag_monitor::LagAlert,
        >,
    >,
}

impl CommitProcessor {
    pub fn new(receiver: UnboundedReceiver<CommittedSubDag>) -> Self {
        Self {
            receiver,
            next_expected_index: 1, // First commit has index 1 (consensus doesn't create commit with index 0)
            pending_commits: BTreeMap::new(),
            commit_index_callback: None,
            global_exec_index_callback: None,
            shared_last_global_exec_index: None,
            current_epoch: 0,
            epoch_base_index_override: None,
            executor_client: None,
            is_transitioning: None,
            delivery_sender: None,
            pending_transactions_queue: None,
            epoch_transition_callback: None,
            epoch_eth_addresses: Arc::new(tokio::sync::RwLock::new(
                std::collections::HashMap::new(),
            )),
            tx_recycler: None,
            storage_path: None,
            recovered_fragment_offset: None,
            lag_alert_sender: None,

        }
    }

    /// Set callback to notify commit index updates
    pub fn with_commit_index_callback<F>(mut self, callback: F) -> Self
    where
        F: Fn(u32) + Send + Sync + 'static,
    {
        self.commit_index_callback = Some(Arc::new(callback));
        self
    }

    /// Set callback to update global execution index after successful commit
    pub fn with_global_exec_index_callback<F>(mut self, callback: F) -> Self
    where
        F: Fn(u64) + Send + Sync + 'static,
    {
        self.global_exec_index_callback = Some(Arc::new(callback));
        self
    }

    /// Set shared last global exec index for direct updates
    pub fn with_shared_last_global_exec_index(
        mut self,
        shared_index: Arc<tokio::sync::Mutex<u64>>,
    ) -> Self {
        self.shared_last_global_exec_index = Some(shared_index);
        self
    }

    /// Set epoch and last_global_exec_index.
    /// [PHASE-A] epoch_base_index_override is kept for API compatibility but
    /// is only used as a Rust-side hint for buffer ordering. Go exclusively
    /// controls actual GEI assignment.
    pub fn with_epoch_info(mut self, epoch: u64, last_global_exec_index: u64) -> Self {
        self.current_epoch = epoch;
        self.epoch_base_index_override = Some(last_global_exec_index);
        self
    }

    /// Set the next expected commit index for ordered processing.
    /// CRITICAL: Must be called during initialization to match the node's actual progress
    /// after restart. If not set, CommitProcessor starts at 1, causing AUTO-JUMP behavior
    /// that can lead to GEI miscalculation and fork.
    pub fn with_next_expected_index(mut self, next_expected: u32) -> Self {
        self.next_expected_index = next_expected;
        self
    }

    /// Set executor client to send blocks to Go executor
    pub fn with_executor_client(mut self, executor_client: Arc<ExecutorClient>) -> Self {
        self.executor_client = Some(executor_client);
        self
    }

    /// Set is_transitioning flag to track epoch transition state
    pub fn with_is_transitioning(mut self, is_transitioning: Arc<AtomicBool>) -> Self {
        self.is_transitioning = Some(is_transitioning);
        self
    }

    /// Set BlockDeliveryManager sender
    pub fn with_delivery_sender(mut self, sender: tokio::sync::mpsc::Sender<crate::node::block_delivery::ValidatedCommit>) -> Self {
        self.delivery_sender = Some(sender);
        self
    }

    /// Provide a queue to store transactions that must be retried in the next epoch.
    pub fn with_pending_transactions_queue(
        mut self,
        pending_transactions_queue: Arc<tokio::sync::Mutex<Vec<Vec<u8>>>>,
    ) -> Self {
        self.pending_transactions_queue = Some(pending_transactions_queue);
        self
    }

    /// Set callback to handle EndOfEpoch system transactions
    pub fn with_epoch_transition_callback<F>(mut self, callback: F) -> Self
    where
        F: Fn(u64, u64, u64) -> Result<()> + Send + Sync + 'static, // CHANGED: u32 -> u64
    {
        self.epoch_transition_callback = Some(Arc::new(callback));
        self
    }

    /// Set epoch ETH addresses HashMap for multi-epoch leader lookup
    /// Accepts a shared reference to the node's epoch_eth_addresses
    pub fn with_epoch_eth_addresses(
        mut self,
        epoch_eth_addresses: Arc<tokio::sync::RwLock<std::collections::HashMap<u64, Vec<Vec<u8>>>>>,
    ) -> Self {
        self.epoch_eth_addresses = epoch_eth_addresses;
        self
    }

    /// Legacy method for backward compatibility - creates HashMap with epoch 0
    #[allow(dead_code)]
    pub fn with_validator_eth_addresses(mut self, eth_addresses: Vec<Vec<u8>>) -> Self {
        let mut map = std::collections::HashMap::new();
        map.insert(self.current_epoch, eth_addresses);
        self.epoch_eth_addresses = Arc::new(tokio::sync::RwLock::new(map));
        self
    }

    /// Get a clone of the Arc to epoch_eth_addresses for external updates
    #[allow(dead_code)]
    pub fn get_epoch_eth_addresses_arc(
        &self,
    ) -> Arc<tokio::sync::RwLock<std::collections::HashMap<u64, Vec<Vec<u8>>>>> {
        self.epoch_eth_addresses.clone()
    }

    /// Set TX recycler for confirming committed TXs
    pub fn with_tx_recycler(mut self, recycler: Arc<TxRecycler>) -> Self {
        self.tx_recycler = Some(recycler);
        self
    }

    /// RS-2: Set storage path for persisting cumulative_fragment_offset
    pub fn with_storage_path(mut self, path: std::path::PathBuf) -> Self {
        self.storage_path = Some(path);
        self
    }

    /// Set recovered fragment offset dynamically calculated at startup
    pub fn with_recovered_fragment_offset(mut self, offset: Option<u64>) -> Self {
        self.recovered_fragment_offset = offset;
        self
    }

    /// Set a sender for lag alerts
    pub fn with_lag_alert_sender(
        mut self,
        sender: tokio::sync::mpsc::UnboundedSender<
            crate::consensus::commit_processor::lag_monitor::LagAlert,
        >,
    ) -> Self {
        self.lag_alert_sender = Some(sender);
        self
    }

    /// Process commits in order
    pub async fn run(self) -> Result<()> {
        let mut receiver = self.receiver;
        let mut next_expected_index = self.next_expected_index;
        let mut pending_commits = self.pending_commits;
        let commit_index_callback = self.commit_index_callback;
        let current_epoch = self.current_epoch;
        let executor_client = self.executor_client;
        let delivery_sender = self.delivery_sender;
        let pending_transactions_queue = self.pending_transactions_queue;
        let epoch_transition_callback = self.epoch_transition_callback;

        // ═══════════════════════════════════════════════════════════════
        // PHASE-A: GO IS THE SINGLE WRITER FOR GEI
        //
        // current_hint_gei is only used as a Rust-side hint for buffer
        // ordering in ExecutorClient. Go exclusively computes and persists
        // the actual GEI via GEIAuthority.
        // ═══════════════════════════════════════════════════════════════
        let mut current_hint_gei: u64 = self.epoch_base_index_override.unwrap_or(0);
        info!("🚀 [COMMIT PROCESSOR] PHASE-A: True Deterministic GEI mode. epoch={}, initial_hint_gei={}, next_expected_index={}",
            current_epoch, current_hint_gei, next_expected_index);

        let mut last_heartbeat_commit = 0u32;
        let mut last_heartbeat_time = std::time::Instant::now();
        const HEARTBEAT_INTERVAL: u32 = 1000;
        const HEARTBEAT_TIMEOUT_SECS: u64 = 300;

        // Spawn LagMonitor if configured
        if let (Some(client), Some(shared_gei), Some(sender)) = (
            &executor_client,
            &self.shared_last_global_exec_index,
            self.lag_alert_sender,
        ) {
            let lag_monitor = crate::consensus::commit_processor::lag_monitor::LagMonitor::new(
                client.clone(),
                shared_gei.clone(),
                sender,
            );
            tokio::spawn(async move {
                lag_monitor.run().await;
            });
            info!("🛡️ LagMonitor spawned for CommitProcessor.");
        }

        info!("📡 [COMMIT PROCESSOR] Waiting for commits from consensus...");

        // Load the exact hint GEI for the current commit from persistence
        if next_expected_index > 1 {
            if let Some(ref sp) = self.storage_path {
                let map = crate::node::executor_client::persistence::load_recent_fragment_offsets_wipe_safe(sp);
                if let Some(gei) = map.get(&next_expected_index) {
                    current_hint_gei = *gei;
                    info!("📂 [PERSIST] Loaded precise current_hint_gei={} for next commit={} from JSON", gei, next_expected_index);
                } else if let Some(ref client) = executor_client {
                    if let Ok((_, go_gei, _, _, _, _)) = client.get_last_handled_commit_index().await {
                        info!("⚠️ [PERSIST] Precise hint GEI not found in JSON for {}. Initializing from Go's last_gei={}", next_expected_index, go_gei);
                        current_hint_gei = go_gei;
                    }
                }
            }
        }
        info!("📊 [PHASE-A] current_hint_gei={} (strictly incremented by transaction commits)", current_hint_gei);
        let _storage_path_for_persist = self.storage_path.clone();

        loop {
            // CRITICAL DEFENSE: Pause processing if epoch is transitioning.
            // This prevents CommitProcessor from pushing new execution state to Go Master
            // while Go is busy re-initializing for the next epoch.
            if let Some(ref is_transitioning) = self.is_transitioning {
                let mut logged = false;
                let transition_wait_start = tokio::time::Instant::now();
                while is_transitioning.load(std::sync::atomic::Ordering::Acquire) {
                    if !logged {
                        info!("⏳ [STATION 3: PROCESSOR] Pausing execution - epoch transition in progress...");
                        logged = true;
                    }
                    // SAFETY TIMEOUT: Prevent permanent deadlock if is_transitioning
                    // flag is never cleared (e.g., panic in transition code despite
                    // Drop guard, or silent task cancellation).
                    // Increased to 120s to allow for heavy state trie updates during Go epoch transitions
                    if transition_wait_start.elapsed() > tokio::time::Duration::from_secs(120) {
                        error!(
                            "🚨 [PROCESSOR] is_transitioning stuck for 120s! Force-clearing to prevent permanent deadlock."
                        );
                        is_transitioning.store(false, std::sync::atomic::Ordering::Release);
                        break;
                    }
                    tokio::time::sleep(tokio::time::Duration::from_millis(100)).await;
                }
                if logged {
                    info!("▶️ [COMMIT PROCESSOR] Resuming execution after epoch transition.");
                }
            }

            match receiver.recv().await {
                Some(subdag) => {
                    let commit_index: u32 = subdag.commit_ref.index;
                    trace!("📥 [COMMIT PROCESSOR] Received committed subdag: commit_index={}, leader={:?}, blocks={}",
                        commit_index, subdag.leader, subdag.blocks.len());

                    // Heartbeat logic
                    if commit_index >= last_heartbeat_commit + HEARTBEAT_INTERVAL {
                        let elapsed = last_heartbeat_time.elapsed().as_secs();
                        info!("💓 [COMMIT PROCESSOR HEARTBEAT] Processed {} commits (last {} commits in {}s, pending_ooo={})", 
                            commit_index, HEARTBEAT_INTERVAL, elapsed, pending_commits.len());
                        last_heartbeat_commit = commit_index;
                        last_heartbeat_time = std::time::Instant::now();
                    }

                    // Check for stuck processor
                    let time_since_last_heartbeat = last_heartbeat_time.elapsed().as_secs();
                    if time_since_last_heartbeat > HEARTBEAT_TIMEOUT_SECS
                        && commit_index == last_heartbeat_commit
                    {
                        warn!("⚠️  [COMMIT PROCESSOR] Possible stuck detected: No progress for {}s (last commit: {})", 
                            time_since_last_heartbeat, commit_index);
                    }

                    trace!(
                        "📊 [COMMIT CONDITION] Checking commit_index={}, next_expected_index={}",
                        commit_index,
                        next_expected_index
                    );

                    // --- [AUTO-JUMP ON STARTUP] ---
                    // If this is the VERY FIRST commit we receive after restart, and it is > expected,
                    // we assume we are resuming from a higher commit index (provided by reliable Consensus Core).
                    if next_expected_index == 1 && commit_index > 1 {
                        warn!("🚀 [AUTO-JUMP] Initial commit {} > expected 1. Auto-jumping to match stream.", commit_index);
                        next_expected_index = commit_index;
                    }

                    // --- [DAG-RESET DETECTION] ---
                    // After a DAG wipe, the new DAG starts from commit 1 but next_expected_index
                    // may be at the old DAG's last commit (e.g., 939). Detect this and jump DOWN.
                    if commit_index < next_expected_index && next_expected_index > 1 {
                        let gap = next_expected_index - commit_index;
                        if gap > 100 {
                            warn!(
                                "🔄 [DAG-RESET] Detected DAG wipe: received commit {} but expected {}. \
                                 Gap={} indicates new DAG instance. Resetting next_expected to {}.",
                                commit_index, next_expected_index, gap, commit_index
                            );
                            next_expected_index = commit_index;

                            // Reset current_hint_gei to Go's last_gei
                            if let Some(ref client) = executor_client {
                                if let Ok((_, go_gei, _, _, _, go_last_block_ts)) = client.get_last_handled_commit_index().await {
                                    if subdag.timestamp_ms <= go_last_block_ts {
                                        tracing::warn!("⏳ [DAG-RESET] Commit {} timestamp ({}) is OLDER than Go's last block ({}). Syncing old DAG.", 
                                            commit_index, subdag.timestamp_ms, go_last_block_ts);
                                    } else {
                                        tracing::warn!("🔄 [DAG-RESET] True cluster restart detected (fresh commit > go_ts). Updated current_hint_gei: {} → {} (Go GEI={})",
                                            current_hint_gei, go_gei, go_gei);
                                        current_hint_gei = go_gei;
                                    }
                                } else {
                                    tracing::warn!("⚠️ [DAG-RESET] Failed to query Go GEI — using stale current_hint_gei={}", current_hint_gei);
                                }
                            }
                        }
                    }

                    if commit_index == next_expected_index {
                        // ═══════════════════════════════════════════════════════
                        // TRUE DETERMINISTIC GEI
                        //
                        // hint_gei perfectly mirrors Go's transaction-based GEI.
                        // It does not increment for empty commits.
                        // ═══════════════════════════════════════════════════════
                        let hint_gei = current_hint_gei + 1;

                        // CC-1: Unified batch_id for end-to-end tracing (Rust → Go)
                        let batch_id =
                            format!("E{}C{}G{}", current_epoch, commit_index, hint_gei);

                        trace!(
                            "[batch_id={}] 📊 TRUE DETERMINISTIC GEI: current_hint_gei={}",
                            batch_id,
                            current_hint_gei
                        );

                        let total_txs_in_commit = subdag
                            .blocks
                            .iter()
                            .map(|b| b.transactions().len())
                            .sum::<usize>();

                        // PHASE-A: GEI validation removed.
                        // Go is the Single Writer — it validates internally.
                        // Rust-side validation was the #2 source of false-positive
                        // fork-prevention that dropped legitimate commits.

                        if let Some(ref storage_path) = _storage_path_for_persist {
                            if let Err(e) = crate::node::executor_client::persistence::persist_recent_fragment_offsets_wipe_safe(
                                storage_path,
                                commit_index,
                                current_hint_gei,
                            ).await {
                                tracing::error!("Failed to save recent fragment offsets: {}", e);
                            }
                        }

                        // Process commit — Go assigns the authoritative GEI
                        let geis_consumed = super::executor::dispatch_commit(
                            &subdag,
                            hint_gei,
                            current_epoch,
                            executor_client.clone(),
                            delivery_sender.clone(),
                            pending_transactions_queue.clone(),
                            self.epoch_eth_addresses.clone(),
                            self.tx_recycler.clone(),
                            self.shared_last_global_exec_index.clone(),
                        )
                        .await?;

                        // ♻️ TX RECYCLER: Confirm committed TXs so they aren't re-submitted
                        if let Some(ref recycler) = self.tx_recycler {
                            if total_txs_in_commit > 0 {
                                let committed_tx_data: Vec<Vec<u8>> = subdag
                                    .blocks
                                    .iter()
                                    .flat_map(|b| {
                                        b.transactions().iter().map(|tx| tx.data().to_vec())
                                    })
                                    .collect();
                                recycler.confirm_committed(&committed_tx_data).await;
                            }
                        }

                        // PHASE-A: Update hint_gei for callbacks and monitoring.
                        // Note: this is a HINT, not the authoritative value.
                        let last_hint_gei = hint_gei + geis_consumed - 1;
                        if let Some(ref callback) = self.global_exec_index_callback {
                            callback(last_hint_gei);
                        }

                        if let Some(ref callback) = commit_index_callback {
                            callback(commit_index);
                        }

                        // UPDATE current_hint_gei
                        // It increments strictly by the number of GEIs actually consumed (0 for empty commits).
                        current_hint_gei += geis_consumed;
                        if geis_consumed > 0 {
                            info!("🔪 [GEI] Commit {} consumed {} GEIs, current_hint_gei now {}",
                                commit_index, geis_consumed, current_hint_gei);
                        }

                        next_expected_index += 1;

                        // Check for EndOfEpoch system transactions AFTER commit is sent to Go
                        if let Some((_block_ref, system_tx)) =
                            subdag.extract_end_of_epoch_transaction()
                        {
                            // SIMPLIFIED: as_end_of_epoch now returns (new_epoch, boundary_block)
                            // Timestamp is derived from block header at boundary_block (by Go/Rust later)
                            if let Some((new_epoch, boundary_block)) = system_tx.as_end_of_epoch() {
                                info!(
                                    "🎯 [SYSTEM TX] EndOfEpoch transaction detected in commit {}: epoch {} -> {}, boundary_block={}, total_txs_in_commit={}",
                                    commit_index, current_epoch, new_epoch, boundary_block, total_txs_in_commit
                                );

                                if let Some(ref callback) = epoch_transition_callback {
                                    info!(
                                        "🚀 [EPOCH TRANSITION] Triggering epoch transition AFTER commit sent to Go: commit_index={}, new_epoch={}, hint_gei={}",
                                        commit_index, new_epoch, hint_gei
                                    );

                                    // CHANGED: Pass boundary_block instead of timestamp_ms
                                    // Timestamp will be derived from boundary_block's block header
                                    if let Err(e) = callback(
                                        new_epoch,
                                        boundary_block, // boundary_block (was timestamp_ms)
                                        hint_gei, // PHASE-A: hint GEI (Go is authoritative)
                                    ) {
                                        warn!("❌ Failed to trigger epoch transition from system transaction: {}", e);
                                    }
                                }

                                // We MUST break here! This epoch is over. Any remaining commits in the channel
                                // belong to the old epoch (empty trailing commits) and must NOT be sent to Go,
                                // otherwise Go will increment LastGlobalExecIndex and cause a hash mismatch for the new epoch.
                                info!("🛑 [STATION 3: PROCESSOR] Halting processing for current epoch after EndOfEpoch transaction.");
                                break;
                            }
                        }

                        // Process pending out-of-order commits
                        let mut should_break = false;
                        while let Some(pending) = pending_commits.remove(&next_expected_index) {
                            let pending_commit_index = next_expected_index;

                            // Hint GEI for pending commits
                            let pending_hint_gei = current_hint_gei + 1;

                            let pending_geis = super::executor::dispatch_commit(
                                &pending,
                                pending_hint_gei,
                                current_epoch,
                                executor_client.clone(),
                                delivery_sender.clone(),
                                pending_transactions_queue.clone(),
                                self.epoch_eth_addresses.clone(),
                                self.tx_recycler.clone(),
                                self.shared_last_global_exec_index.clone(),
                            )
                            .await?;

                            // UPDATE current_hint_gei for pending
                            current_hint_gei += pending_geis;
                            if pending_geis > 0 {
                                info!("🔪 [GEI] Pending commit {} consumed {} GEIs, current_hint_gei now {}",
                                    pending_commit_index, pending_geis, current_hint_gei);
                            }

                            if let Some(ref callback) = commit_index_callback {
                                callback(pending_commit_index);
                            }

                            next_expected_index += 1;

                            // Check for EndOfEpoch in pending commits
                            if let Some((_block_ref, system_tx)) =
                                pending.extract_end_of_epoch_transaction()
                            {
                                if let Some((new_epoch, boundary_block)) =
                                    system_tx.as_end_of_epoch()
                                {
                                    if let Some(ref callback) = epoch_transition_callback {
                                        if let Err(e) =
                                            callback(new_epoch, boundary_block, pending_hint_gei)
                                        {
                                            warn!("❌ Failed to trigger epoch transition from pending system transaction: {}", e);
                                        }
                                    }

                                    info!("🛑 [STATION 3: PROCESSOR] Halting processing for current epoch after EndOfEpoch transaction in PENDING commit.");
                                    should_break = true;
                                    break;
                                }
                            }
                        }

                        if should_break {
                            break;
                        }
                    } else if commit_index > next_expected_index {
                        // SAFETY: Limit pending_commits size to prevent OOM
                        const MAX_PENDING_COMMITS: usize = 5000;
                        if pending_commits.len() >= MAX_PENDING_COMMITS {
                            warn!(
                                "🚨 [STATION 3: PROCESSOR] pending_commits at capacity ({})! \
                                Dropping out-of-order commit {} (expected {}). \
                                This indicates severe downstream overload at Station 4.",
                                MAX_PENDING_COMMITS, commit_index, next_expected_index
                            );
                            continue;
                        }
                        warn!(
                            "Received out-of-order commit: index={}, expected={}, pending_count={}, storing for later",
                            commit_index, next_expected_index, pending_commits.len()
                        );
                        pending_commits.insert(commit_index, subdag);

                        // ═══════════════════════════════════════════════════════════════
                        // SNAPSHOT RESTORE FORWARD-JUMP (Batch-Optimized)
                        // After snapshot restore, the DAG fast-forwards past the
                        // CommitProcessor's expected index, creating an unbridgeable gap.
                        // Commits between next_expected and the DAG's current position
                        // will NEVER arrive — they were never produced by this DAG instance.
                        //
                        // Detection: If we have ≥50 pending commits AND the gap between
                        // next_expected and the smallest pending commit is >100, jump to
                        // the smallest pending to drain the queue.
                        //
                        // OPTIMIZATION: Empty commits (no TXs, ~90%+ during catch-up) are
                        // batch-skipped in bulk — single GEI update + executor advance,
                        // avoiding 1000x individual dispatch_commit() calls.
                        //
                        // FORK-SAFETY: The GEI formula uses epoch_base + commit_index.
                        // After jumping, commit_index is correct (from consensus), so
                        // GEI calculation remains deterministic across all nodes.
                        // ═══════════════════════════════════════════════════════════════
                        const FORWARD_JUMP_PENDING_THRESHOLD: usize = 10;
                        const FORWARD_JUMP_GAP_THRESHOLD: u32 = 20;
                        if pending_commits.len() >= FORWARD_JUMP_PENDING_THRESHOLD {
                            if let Some(&smallest_pending) = pending_commits.keys().next() {
                                let gap = smallest_pending.saturating_sub(next_expected_index);
                                if gap > FORWARD_JUMP_GAP_THRESHOLD {
                                    warn!(
                                        "🚀 [FORWARD-JUMP] Unbridgeable gap detected! \
                                         expected={}, smallest_pending={}, gap={}, pending_count={}. \
                                         Jumping to {} to drain queue (snapshot restore recovery).",
                                        next_expected_index, smallest_pending, gap,
                                        pending_commits.len(), smallest_pending
                                    );

                                    next_expected_index = smallest_pending;

                                    // ═════════════════════════════════════════════════
                                    // BATCH DRAIN: Process pending commits with fast-
                                    // path for empty commits (no TXs, no system TX).
                                    // PHASE-A: Same logic, using hint GEI.
                                    // ═════════════════════════════════════════════════
                                    let mut batch_empty_count: u64 = 0;
                                    let mut batch_nonempty_count: u64 = 0;
                                    let drain_start = std::time::Instant::now();

                                    while let Some(pending) = pending_commits.remove(&next_expected_index) {
                                        let pending_commit_index = next_expected_index;
                                        let pending_hint_gei = current_hint_gei + 1;

                                        // Check if this commit has any transactions
                                        let total_txs: usize = pending.blocks
                                            .iter()
                                            .map(|b| b.transactions().len())
                                            .sum();
                                        let has_system_tx = pending.extract_end_of_epoch_transaction().is_some();

                                        if total_txs == 0 && !has_system_tx {
                                            // ── BATCH FAST-SKIP: Empty commit ──
                                            batch_empty_count += 1;

                                            if let Some(ref cb) = commit_index_callback {
                                                cb(pending_commit_index);
                                            }
                                        } else {
                                            // ── NON-EMPTY: Must go through full dispatch ──
                                            if batch_empty_count > 0 {
                                                let prev_hint = current_hint_gei;
                                                if let Some(ref shared_idx) = self.shared_last_global_exec_index {
                                                    let mut idx_guard = shared_idx.lock().await;
                                                    if prev_hint > *idx_guard {
                                                        *idx_guard = prev_hint;
                                                    }
                                                }
                                                // skip_empty_commit is NO LONGER NEEDED as GEI doesn't advance.
                                                info!(
                                                    "⏭️ [FORWARD-JUMP] Batch-skipped {} empty commits (hint_gei up to {})",
                                                    batch_empty_count, prev_hint
                                                );
                                                batch_empty_count = 0;
                                            }

                                            batch_nonempty_count += 1;
                                            let geis_consumed = super::executor::dispatch_commit(
                                                &pending,
                                                pending_hint_gei,
                                                current_epoch,
                                                executor_client.clone(),
                                                delivery_sender.clone(),
                                                pending_transactions_queue.clone(),
                                                self.epoch_eth_addresses.clone(),
                                                self.tx_recycler.clone(),
                                                self.shared_last_global_exec_index.clone(),
                                            )
                                            .await?;

                                            current_hint_gei += geis_consumed;

                                            if let Some(ref cb) = commit_index_callback {
                                                cb(pending_commit_index);
                                            }
                                            if let Some(ref cb) = self.global_exec_index_callback {
                                                cb(pending_hint_gei + geis_consumed - 1);
                                            }
                                        }

                                        next_expected_index += 1;
                                    }

                                    // Flush remaining empty commits after loop
                                    if batch_empty_count > 0 {
                                        let last_hint = current_hint_gei;
                                        if let Some(ref shared_idx) = self.shared_last_global_exec_index {
                                            let mut idx_guard = shared_idx.lock().await;
                                            if last_hint > *idx_guard {
                                                *idx_guard = last_hint;
                                            }
                                        }
                                        // skip_empty_commit is NO LONGER NEEDED as GEI doesn't advance.
                                        if let Some(ref cb) = self.global_exec_index_callback {
                                            cb(last_hint);
                                        }
                                    }

                                    let drain_elapsed = drain_start.elapsed();
                                    let total_drained = batch_empty_count + batch_nonempty_count;
                                    info!(
                                        "✅ [FORWARD-JUMP] Drain complete in {:?}. \
                                         total_drained={}, nonempty_processed={}, \
                                         next_expected={}, remaining_pending={}",
                                        drain_elapsed, total_drained,
                                        batch_nonempty_count,
                                        next_expected_index, pending_commits.len()
                                    );
                                }
                            }
                        }
                    } else {
                        warn!(
                            "Received commit with index {} which is less than expected {}",
                            commit_index, next_expected_index
                        );
                    }
                }
                None => {
                    warn!("⚠️  [COMMIT PROCESSOR] Commit receiver closed (commit processor will exit).");
                    break;
                }
            }
        }
        Ok(())
    }
}
