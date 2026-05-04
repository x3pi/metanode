// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use consensus_core::{BlockAPI, CommittedSubDag};
use mysten_metrics::monitored_mpsc::UnboundedReceiver;
use std::collections::BTreeMap;
use std::sync::atomic::AtomicBool;
use std::sync::Arc;
use tracing::{error, info, trace, warn};

use crate::consensus::tx_recycler::TxRecycler;

use crate::node::executor_client::ExecutorClient;

/// Commit processor that ensures commits are executed in order
pub struct CommitProcessor {
    receiver: UnboundedReceiver<CommittedSubDag>,
    pub next_expected_index: u32, // CommitIndex is u32
    pending_commits: BTreeMap<u32, CommittedSubDag>,
    /// The last commit index that Go has already processed. Used to fast-forward replay.
    pub go_last_commit_index: u32,
    /// Optional callback to notify commit index updates (for epoch transition)
    commit_index_callback: Option<Arc<dyn Fn(u32) + Send + Sync>>,
    /// Optional callback to update global execution index after successful commit
    global_exec_index_callback: Option<Arc<dyn Fn(u64) + Send + Sync>>,
    /// Current epoch (for deterministic global_exec_index calculation)
    current_epoch: u64,

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
    epoch_transition_callback: Option<Arc<dyn Fn(u64, u64, u64, u64) -> Result<()> + Send + Sync>>,
    /// Multi-epoch committee cache: ETH addresses keyed by epoch
    /// Supports looking up leaders from previous epochs during transitions
    /// RS-1: Uses RwLock instead of Mutex — reads (every commit) don't block each other,
    /// only writes (epoch transition) take exclusive lock.
    epoch_eth_addresses: Arc<tokio::sync::RwLock<std::collections::HashMap<u64, Vec<Vec<u8>>>>>,
    /// TX recycler for confirming committed TXs
    tx_recycler: Option<Arc<TxRecycler>>,

    /// RS-2: Storage path for persistence
    storage_path: Option<std::path::PathBuf>,
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
            go_last_commit_index: 0,
            // PHASE-B: No GEI tracking in Rust. Go assigns via GEIAuthority.
            commit_index_callback: None,
            global_exec_index_callback: None,
            shared_last_global_exec_index: None,
            current_epoch: 0,

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

    /// Set epoch number.
    /// PHASE-B: epoch_base_index is no longer tracked. Go assigns GEI exclusively.
    pub fn with_epoch_info(mut self, epoch: u64) -> Self {
        self.current_epoch = epoch;
        self
    }

    /// Update the current epoch (used after STARTUP-SYNC if epoch advanced)
    pub fn update_epoch(&mut self, epoch: u64) {
        self.current_epoch = epoch;
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

    /// Set the last commit index already handled by Go.
    pub fn with_go_last_commit_index(mut self, go_last_commit_index: u32) -> Self {
        self.go_last_commit_index = go_last_commit_index;
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
        F: Fn(u64, u64, u64, u64) -> Result<()> + Send + Sync + 'static,
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

    /// Resolve leader ETH address from committee cache and embed into subdag.
    /// Called once per commit — same immutability pattern as global_exec_index.
    /// After this call, subdag.leader_address is set and MUST NOT be recalculated.
    ///
    /// FORK-SAFETY (May 2026): If leader_address is already set (from stored/synced commit),
    /// skip re-resolution to prevent divergence on nodes with corrupted DAG state.
    async fn resolve_leader_address(
        epoch_eth_addresses: &tokio::sync::RwLock<std::collections::HashMap<u64, Vec<Vec<u8>>>>,
        subdag: &mut CommittedSubDag,
        epoch: u64,
    ) {
        // FORK-SAFETY: If leader_address was pre-populated from stored commit data,
        // trust it and skip local resolution. This ensures recovering nodes use the
        // same address as the original producing node.
        if subdag.leader_address.len() == 20 {
            trace!(
                "✅ [LEADER] Using pre-embedded leader_address from commit (commit={}, epoch={}, addr=0x{})",
                subdag.commit_ref.index, epoch, hex::encode(&subdag.leader_address)
            );
            return;
        }

        let leader_author_index = subdag.leader.author.value();

        loop {
            {
                let addrs_guard = epoch_eth_addresses.read().await;
                if let Some(addrs) = addrs_guard.get(&epoch) {
                    if leader_author_index < addrs.len() {
                        let addr = &addrs[leader_author_index];
                        if addr.len() == 20 {
                            subdag.leader_address = addr.clone();
                            return;
                        }
                    }
                }
            }

            // Cache not ready yet — wait and retry
            warn!(
                "⏳ [LEADER] Waiting for epoch_eth_addresses (epoch={}, index={})...",
                epoch, leader_author_index
            );
            tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        }
    }

    /// Process commits in order
    pub async fn run(self) -> Result<()> {
        // Validate required dependencies upfront to avoid bare .unwrap() in hot loop
        let shared_gei = self.shared_last_global_exec_index.clone()
            .ok_or_else(|| anyhow::anyhow!("BUG: shared_last_global_exec_index must be set before CommitProcessor::run()"))?;

        let mut receiver = self.receiver;
        let mut next_expected_index = self.next_expected_index;
        let mut pending_commits = self.pending_commits;
        let commit_index_callback = self.commit_index_callback;
        let current_epoch = self.current_epoch;
        let executor_client = self.executor_client;
        let delivery_sender = self.delivery_sender;
        let _pending_transactions_queue = self.pending_transactions_queue;
        let epoch_transition_callback = self.epoch_transition_callback;
        let go_last_commit_index = self.go_last_commit_index;
        let epoch_eth_addresses = self.epoch_eth_addresses;

        // PHASE-B: Go is the sole authority for GEI. Rust sends commit_index +
        // transactions to Go. Go assigns GEI via GEIAuthority singleton.
        info!("🚀 [COMMIT PROCESSOR] PHASE-B: Go-Authoritative GEI. epoch={}, next_expected_index={}",
            current_epoch, next_expected_index);

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

            // Fix 3 Revert: Use direct indefinite block (no 120s timeout) to enforce backpressure
            match receiver.recv().await {
                Some(mut subdag) => {
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

                    // FORK-SAFETY FIX: We NO LONGER auto-jump on startup!
                    // If the node starts with expected=1, and the network is at commit 173,
                    // we MUST WAIT for CommitSyncer to fetch commits 1..172.
                    // Auto-jumping completely breaks state determinism.

                    // FORK-SAFETY: Removed DAG-RESET DETECTION heuristic.
                    // We DO NOT jump `next_expected_index` downwards based on arbitrary gap heuristics.
                    // If a node restarts and the DAG was wiped but Go state was not, that is an invalid
                    // operational state and the node MUST NOT silently reset its commit index to 1,
                    // as doing so would duplicate execution and cause a hard fork.

                    // CRITICAL FORK-SAFETY FIX: Fast-forward historical commits that Go has already executed.
                    // If we don't do this, CommitProcessor will replay historical commits and
                    // incorrectly increment `current_hint_gei` on top of its `go_last_gei` initial value.
                    if commit_index <= go_last_commit_index {
                        info!("⏭️  [FAST-FORWARD] Skipping historical commit {} (Go already at commit {})", commit_index, go_last_commit_index);
                        if commit_index == next_expected_index {
                            next_expected_index += 1;
                        }
                        continue;
                    }

                    if commit_index == next_expected_index {
                        // ═══════════════════════════════════════════════════════
                        // PHASE-B: GO-AUTHORITATIVE GEI
                        //
                        // Rust sends commit_index + transactions. Go assigns GEI.
                        // No hint_gei tracking in Rust.
                        // ═══════════════════════════════════════════════════════
                        let batch_id =
                            format!("E{}C{}", current_epoch, commit_index);

                        let total_txs_in_commit: usize = subdag
                            .blocks
                            .iter()
                            .map(|b| b.transactions().len())
                            .sum();

                        trace!(
                            "[batch_id={}] 📊 PHASE-B: Dispatching commit (txs={})",
                            batch_id, total_txs_in_commit
                        );

                        let gei = {
                            let gei_guard = shared_gei.lock().await;
                            *gei_guard + 1
                        };

                        // Resolve leader ETH address into subdag (immutable after this)
                        Self::resolve_leader_address(&epoch_eth_addresses, &mut subdag, current_epoch).await;

                        // Process commit — send accurate GEI to Go
                        let geis_consumed = super::executor::dispatch_commit(
                            &subdag,
                            gei, 
                            current_epoch,
                            executor_client.clone(),
                            delivery_sender.clone(),
                        )
                        .await?;

                        // Accumulate exact number of fragments consumed by this commit
                        {
                            let mut gei_guard = shared_gei.lock().await;
                            *gei_guard += geis_consumed;
                        }
                        
                        // ♻️ TX RECYCLER: Confirm committed TXs
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

                        if let Some(ref callback) = commit_index_callback {
                            callback(commit_index);
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
                                        "🚀 [EPOCH TRANSITION] Triggering epoch transition AFTER commit sent to Go: commit_index={}, new_epoch={}",
                                        commit_index, new_epoch
                                    );

                                    let current_gei = {
                                        let gei_guard = shared_gei.lock().await;
                                        *gei_guard
                                    };
                                    if let Err(e) = callback(
                                        new_epoch,
                                        subdag.timestamp_ms,
                                        boundary_block,
                                        current_gei,
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
                        while let Some(mut pending) = pending_commits.remove(&next_expected_index) {
                            let pending_commit_index = next_expected_index;

                            let pending_gei = {
                                let gei_guard = shared_gei.lock().await;
                                *gei_guard + 1
                            };

                            // Resolve leader ETH address into pending subdag
                            Self::resolve_leader_address(&epoch_eth_addresses, &mut pending, current_epoch).await;

                            let geis_consumed = super::executor::dispatch_commit(
                                &pending,
                                pending_gei, 
                                current_epoch,
                                executor_client.clone(),
                                delivery_sender.clone(),
                            )
                            .await?;

                            {
                                let mut gei_guard = shared_gei.lock().await;
                                *gei_guard += geis_consumed;
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
                                    let current_gei = {
                                        let gei_guard = shared_gei.lock().await;
                                        *gei_guard
                                    };
                                    if let Some(ref callback) = epoch_transition_callback {
                                        if let Err(e) =
                                            callback(new_epoch, pending.timestamp_ms, boundary_block, current_gei)
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

                        // FORK-SAFETY FIX: We NO LONGER jump forward heuristically!
                        // If the node is catching up and pending_commits grows large,
                        // we MUST wait for the CommitSyncer to deliver the missing commits.
                        // Skipping commits based on `gap > 20` caused fatal DAG divergence
                        // where different nodes assigned different commit_indices to the same GEI.
                        // We strictly buffer until `commit_index == next_expected_index`.
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
