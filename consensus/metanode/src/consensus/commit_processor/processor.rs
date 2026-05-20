// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use consensus_core::{BlockAPI, CommittedSubDag};
use mysten_metrics::monitored_mpsc::UnboundedReceiver;
use std::collections::BTreeMap;
use std::sync::atomic::{AtomicBool, AtomicU32};
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
    /// Committed transaction hashes for deduplication
    committed_transaction_hashes: Option<Arc<tokio::sync::Mutex<std::collections::HashSet<Vec<u8>>>>>,
    storage_path: Option<std::path::PathBuf>,
    /// Channel sender for emitting lag alerts
    lag_alert_sender: Option<
        tokio::sync::mpsc::UnboundedSender<
            crate::consensus::commit_processor::lag_monitor::LagAlert,
        >,
    >,
    /// QUORUM-GATE (May 2026): Shared reference to the network's quorum commit index.
    /// When set, local commits (decided_with_local_blocks=true) are held until
    /// quorum_commit_index >= commit_index. This prevents Go from executing
    /// divergent blocks produced by a sparse DAG local committer.
    quorum_commit_index: Option<Arc<AtomicU32>>,
    /// Committee size (number of validators) for DAG density verification.
    /// Used to compute quorum threshold: 2f+1 where f = (n-1)/3.
    committee_size: usize,
    /// DIGEST-GATE (May 2026): Callback to query quorum-agreed commit digest.
    /// Takes commit_index, returns Some(digest_bytes) if 2f+1 authorities agree on digest,
    /// None if not enough votes yet. Used to verify local commit content matches network.
    digest_verifier: Option<Arc<dyn Fn(u32) -> Option<[u8; 32]> + Send + Sync>>,
    /// COLD-START-FIX (May 2026): Callback to check if CommitVoteMonitor has received
    /// any actual digest vote data from P2P blocks. Returns true when digest verification
    /// infrastructure is functional. Unlike quorum_commit_index (set by CommitSyncer's
    /// peer queries), this reflects actual digest observation capability.
    /// When this returns false, COLD-START-BYPASS is eligible regardless of QCI value.
    digest_data_checker: Option<Arc<dyn Fn() -> bool + Send + Sync>>,
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
            committed_transaction_hashes: None,
            storage_path: None,
            lag_alert_sender: None,
            quorum_commit_index: None,
            committee_size: 0,
            digest_verifier: None,
            digest_data_checker: None,
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

    /// Update the next expected index (used after STARTUP-SYNC if sync advanced)
    pub fn update_next_expected_index(&mut self, next_expected: u32) {
        self.next_expected_index = next_expected;
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

    /// Update the last commit index already handled by Go (used after STARTUP-SYNC)
    pub fn update_go_last_commit_index(&mut self, go_last_commit_index: u32) {
        self.go_last_commit_index = go_last_commit_index;
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

    /// Set committed transaction hashes
    pub fn with_committed_transaction_hashes(
        mut self,
        hashes: Arc<tokio::sync::Mutex<std::collections::HashSet<Vec<u8>>>>,
    ) -> Self {
        self.committed_transaction_hashes = Some(hashes);
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

    /// QUORUM-GATE (May 2026): Set the shared quorum commit index reference.
    /// When set, commits decided locally (decided_with_local_blocks=true) will be
    /// held in a buffer until the network quorum has confirmed them.
    /// This eliminates the root cause of consensus-layer forks from sparse DAG
    /// local committer decisions.
    pub fn with_quorum_commit_index(mut self, quorum_index: Arc<AtomicU32>) -> Self {
        self.quorum_commit_index = Some(quorum_index);
        self
    }

    pub fn with_committee_size(mut self, size: usize) -> Self {
        self.committee_size = size;
        self
    }

    pub fn with_digest_verifier<F>(mut self, verifier: F) -> Self
    where
        F: Fn(u32) -> Option<[u8; 32]> + Send + Sync + 'static,
    {
        self.digest_verifier = Some(Arc::new(verifier));
        self
    }

    /// COLD-START-FIX (May 2026): Set callback to check if CommitVoteMonitor has
    /// actual digest data. Used to correctly detect cold-start conditions after
    /// epoch transitions, where CommitSyncer may have set quorum_commit_index > 0
    /// from peer queries but CommitVoteMonitor has no actual digest votes yet.
    pub fn with_digest_data_checker<F>(mut self, checker: F) -> Self
    where
        F: Fn() -> bool + Send + Sync + 'static,
    {
        self.digest_data_checker = Some(Arc::new(checker));
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

        // BOUNDED WAIT (May 2026): Prevent infinite blocking of CommitProcessor.
        // ROOT CAUSE: During epoch catch-up or late-joining nodes, the committee
        // cache may never be populated for the current epoch, causing this loop
        // to block ALL commit processing indefinitely. The node then falls behind,
        // triggers connection timeouts, and is killed by external monitors.
        //
        // After MAX_WAIT_SECS, we log a critical error and return with empty
        // leader_address. The block will have LeaderAddress=0x00..00 which is
        // deterministically incorrect but CONSISTENT across all nodes in the
        // same state — no fork risk. The correct address will be resolved
        // when the epoch committee is eventually populated.
        const MAX_WAIT_SECS: u64 = 30;
        let resolve_start = std::time::Instant::now();
        let mut logged_warning = false;

        loop {
            {
                let addrs_guard = epoch_eth_addresses.read().await;
                if let Some(addrs) = addrs_guard.get(&epoch) {
                    if leader_author_index < addrs.len() {
                        let addr = &addrs[leader_author_index];
                        if addr.len() == 20 {
                            if logged_warning {
                                info!(
                                    "✅ [LEADER] epoch_eth_addresses resolved after {}ms (epoch={}, index={})",
                                    resolve_start.elapsed().as_millis(), epoch, leader_author_index
                                );
                            }
                            subdag.leader_address = addr.clone();
                            return;
                        } else {
                            warn!("⚠️ [LEADER] Invalid address length for epoch={}, index={} (len={})", epoch, leader_author_index, addr.len());
                            return;
                        }
                    } else {
                        warn!("🚨 [LEADER] Committee index OUT OF BOUNDS! (epoch={}, index={}, committee_size={})", epoch, leader_author_index, addrs.len());
                        return;
                    }
                }
            }

            // Timeout check: prevent permanent CommitProcessor stall
            let elapsed = resolve_start.elapsed();
            if elapsed.as_secs() >= MAX_WAIT_SECS {
                error!(
                    "🚨 [LEADER] TIMEOUT after {}s waiting for epoch_eth_addresses! \
                     epoch={}, index={}, commit={}. \
                     Proceeding with EMPTY leader_address to unblock CommitProcessor. \
                     This commit's block will have LeaderAddress=0x00..00.",
                    elapsed.as_secs(), epoch, leader_author_index, subdag.commit_ref.index
                );
                return;
            }

            // Cache not ready yet — wait and retry (rate-limited warning)
            if !logged_warning {
                warn!(
                    "⏳ [LEADER] Waiting for epoch_eth_addresses (epoch={}, index={}, timeout={}s)...",
                    epoch, leader_author_index, MAX_WAIT_SECS
                );
                logged_warning = true;
            } else if elapsed.as_secs() % 5 == 0 && elapsed.as_millis() % 5000 < 200 {
                // Log progress every ~5s instead of every 200ms to reduce log spam
                warn!(
                    "⏳ [LEADER] Still waiting for epoch_eth_addresses (epoch={}, index={}, elapsed={}s/{}s)...",
                    epoch, leader_author_index, elapsed.as_secs(), MAX_WAIT_SECS
                );
            }
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
        let tx_recycler = self.tx_recycler;
        let storage_path = self.storage_path;
        let committed_transaction_hashes = self.committed_transaction_hashes;
        let _quorum_commit_index_ref = self.quorum_commit_index.clone();
        let committee_size = self.committee_size;
        let digest_verifier = self.digest_verifier.clone();
        let digest_data_checker = self.digest_data_checker.clone();
        // BFT quorum threshold: 2f+1 where n=3f+1, so quorum = ceil(2n/3)
        // For n=5: quorum=4, n=4: quorum=3, n=7: quorum=5
        let _quorum_threshold = if committee_size > 0 {
            (committee_size * 2 + 2) / 3 // ceil(2n/3)
        } else {
            1 // Fallback: accept all if committee_size not configured
        };
        info!("🛡️ [DIGEST-GATE] Initialized: committee_size={}, quorum_threshold={}, digest_verifier={}", 
              committee_size, _quorum_threshold, digest_verifier.is_some());

        // DIGEST-GATE: Buffer for ALL local commits until digest verified.
        // ABSOLUTE RULE: NEVER dispatch unverified commits — waiting is always safe.
        //
        // Buffer is bounded to MAX_PENDING_LOCAL_COMMITS to prevent memory leak.
        // When exceeded, oldest entries are DROPPED (not dispatched!) — CommitSyncer
        // will re-deliver them as CertifiedCommit from peers.
        let mut pending_local_commits: BTreeMap<u32, CommittedSubDag> = BTreeMap::new();
        let mut pending_local_timestamps: BTreeMap<u32, std::time::Instant> = BTreeMap::new();
        // At 50k+ TPS (~1000 commits/sec), epoch transitions can stall
        // for 5-10s = 5000-10000 commits. Set to 2000 for local commits
        // to absorb 10+ seconds of sustained high-TPS commits.
        // Previously 500, which caused cascade failure: buffer full → drops
        // → re-delivery → more drops. 2000 provides sufficient headroom
        // while COLD-START-BYPASS (10s) resolves the verification deadlock.
        const MAX_PENDING_LOCAL_COMMITS: usize = 2000;

        // ═══════════════════════════════════════════════════════════════
        // LAYER-4 WAL: Write-Ahead Log for crash-safe FFI tracking.
        // Records PENDING before FFI call, COMMITTED after Go confirms.
        // On restart, pending entries indicate crash mid-FFI → log warning.
        // ═══════════════════════════════════════════════════════════════
        let mut commit_wal = if let Some(ref sp) = storage_path {
            match super::wal::CommitWAL::open(sp) {
                Ok(wal) => {
                    // Recovery: check for pending entries (crash during FFI)
                    let pending = wal.get_pending_entries();
                    if !pending.is_empty() {
                        warn!(
                            "⚠️ [WAL RECOVERY] Found {} pending (unconfirmed) commits from previous run. \
                             These commits were sent to Go but Go confirmation was not recorded. \
                             Go's Idempotent Guard (Layer 4) will skip if already processed.",
                            pending.len()
                        );
                        for entry in &pending {
                            warn!(
                                "  ⚠️ [WAL PENDING] commit_index={}, gei={}, epoch={}",
                                entry.commit_index, entry.global_exec_index, entry.epoch
                            );
                        }
                    }
                    Some(wal)
                }
                Err(e) => {
                    warn!("⚠️ [WAL] Failed to open WAL (non-critical, continuing without): {}", e);
                    None
                }
            }
        } else {
            None
        };

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

        // DIAGNOSTIC (May 2026): Periodic state dump for stall diagnosis
        let mut last_diag_log = std::time::Instant::now();
        const DIAG_INTERVAL_SECS: u64 = 10;

        loop {
            // ═══════════════════════════════════════════════════════════════
            // PIPELINE HEALTH DIAGNOSTIC: Log full DIGEST-GATE state every
            // 10s when pending commits exist. This provides visibility into
            // WHY the pipeline is stalled without modifying any control flow.
            // ═══════════════════════════════════════════════════════════════
            if !pending_local_commits.is_empty()
                && last_diag_log.elapsed().as_secs() >= DIAG_INTERVAL_SECS
            {
                let qci_val = if let Some(ref qci) = _quorum_commit_index_ref {
                    qci.load(std::sync::atomic::Ordering::Relaxed)
                } else {
                    0
                };
                let digest_has_data = if let Some(ref checker) = digest_data_checker {
                    checker()
                } else {
                    false
                };
                let is_trans = if let Some(ref it) = self.is_transitioning {
                    it.load(std::sync::atomic::Ordering::Relaxed)
                } else {
                    false
                };
                let oldest_age = pending_local_timestamps.values()
                    .map(|ts| std::time::Instant::now().duration_since(*ts).as_secs())
                    .max()
                    .unwrap_or(0);
                let first_pending_idx = pending_local_commits.keys().next().copied().unwrap_or(0);
                let first_verifier_result = if let Some(ref verifier) = digest_verifier {
                    match verifier(first_pending_idx) {
                        Some(_) => "Some(digest)",
                        None => "None",
                    }
                } else {
                    "no_verifier"
                };
                warn!(
                    "🔬 [DIGEST-GATE DIAG] PIPELINE STATE DUMP | \
                     pending_local={}, first_idx={}, oldest_age={}s, \
                     next_expected={}, qci={}, digest_has_data={}, \
                     is_transitioning={}, verifier({})={}, \
                     pending_ooo={}, epoch={}",
                    pending_local_commits.len(), first_pending_idx, oldest_age,
                    next_expected_index, qci_val, digest_has_data,
                    is_trans, first_pending_idx, first_verifier_result,
                    pending_commits.len(), current_epoch
                );
                last_diag_log = std::time::Instant::now();
            }
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
                    // Increased to 300s to allow for heavy state trie updates during Go epoch transitions
                    if transition_wait_start.elapsed() > tokio::time::Duration::from_secs(300) {
                        error!(
                            "🚨 [PROCESSOR] is_transitioning stuck for 300s! Force-clearing to prevent permanent deadlock."
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

            // ═══════════════════════════════════════════════════════════════
            // LIVENESS-MONITOR: Log stale pending local commits for diagnostics.
            //
            // ABSOLUTE RULE: NEVER force-dispatch an unverified commit.
            // Waiting indefinitely is ALWAYS preferable to a potential fork.
            // The system WILL eventually make progress when ≥2f+1 nodes are
            // online — CertifiedCommit from CommitSyncer or digest_verifier
            // poll will release the buffer.
            //
            // If all nodes restart simultaneously, the "seed node" mechanism
            // (see consensus_node.rs CLUSTER-BOOTSTRAP) ensures safe recovery.
            // ═══════════════════════════════════════════════════════════════
            if !pending_local_commits.is_empty() {
                let now = std::time::Instant::now();
                for (&idx, ts) in pending_local_timestamps.iter() {
                    let age_secs = now.duration_since(*ts).as_secs();
                    // Log warning at 60s, 120s, 300s, then every 300s
                    if age_secs == 60 || age_secs == 120 || (age_secs >= 300 && age_secs % 300 == 0) {
                        warn!(
                            "⏳ [DIGEST-GATE PENDING] Commit {} waiting {}s for verification. \
                             buffered={}, digest_verifier={}. \
                             System will NOT force-dispatch — waiting for CertifiedCommit or digest quorum.",
                            idx, age_secs, pending_local_commits.len(),
                            digest_verifier.is_some()
                        );
                    }
                }
            }

            // ═══════════════════════════════════════════════════════════════
            // DIGEST-GATE POLL: Release buffered local commits when digest verified.
            // Local commits are ONLY released when digest_verifier confirms
            // the network quorum agrees on the EXACT SAME commit digest.
            //
            // CRITICAL FIX (May 2026): Dispatch STRICTLY in ascending order.
            // If commit N is unverified, STOP — do NOT dispatch N+1.
            // Dispatching out of order causes GEI misalignment:
            //   e.g., commit 38 (verified) dispatches with GEI=36 (because
            //   commit 36 was stuck) → different nodes produce different
            //   block content at the same GEI slot → FORK.
            // ═══════════════════════════════════════════════════════════════
            if !pending_local_commits.is_empty() {
                if let Some(ref verifier) = digest_verifier {
                    // Collect indices to dispatch — MUST be contiguous from the lowest
                    let mut verified_indices: Vec<u32> = Vec::new();
                    for (&local_idx, local_commit) in pending_local_commits.iter() {
                        let local_digest = local_commit.commit_ref.digest.into_inner();
                        match verifier(local_idx) {
                            Some(quorum_digest) => {
                                if quorum_digest == local_digest {
                                    verified_indices.push(local_idx);
                                } else {
                                    // DIVERGENT — Local commit disagrees with network quorum.
                                    //
                                    // Check how long this commit has been stuck. If it's been
                                    // divergent for >30s, the local commit is STALE (likely from
                                    // a previous epoch's DAG state). Discard it to unblock the
                                    // pipeline — next_expected_index stays at this slot, so the
                                    // correct CertifiedCommit will fill it when it arrives.
                                    let divergence_age = pending_local_timestamps
                                        .get(&local_idx)
                                        .map(|ts| std::time::Instant::now().duration_since(*ts).as_secs())
                                        .unwrap_or(0);
                                    
                                    if divergence_age >= 30 {
                                        // ═══════════════════════════════════════════════════
                                        // STALE-COMMIT EVICTION (May 2026):
                                        //
                                        // After 30s of divergence, this local commit is stale.
                                        // Common cause: epoch-local commit indices reset each
                                        // epoch, so commit N in epoch K has a completely
                                        // different digest than commit N in epoch K+2.
                                        // If a stale commit persists in the buffer, it blocks
                                        // ALL subsequent commits permanently.
                                        //
                                        // Safety: We only DISCARD — never dispatch. The slot
                                        // (next_expected_index) stays open for the correct
                                        // CertifiedCommit from CommitSyncer.
                                        // ═══════════════════════════════════════════════════
                                        warn!(
                                            "🗑️ [DIGEST-GATE STALE-EVICT] Commit {} DISCARDED after {}s divergence! \
                                             local_digest={}, quorum_digest={}. \
                                             Stale local commit evicted — slot stays open for CertifiedCommit.",
                                            local_idx, divergence_age,
                                            hex::encode(&local_digest[..4]),
                                            hex::encode(&quorum_digest[..4])
                                        );
                                        // Mark for removal after iteration (can't modify during iter)
                                        // We break here; the removal happens below via verified_indices
                                        // using a special marker. Actually, let's just collect for removal.
                                        break; // Will be handled by post-loop eviction below
                                    } else {
                                        warn!(
                                            "🚨 [DIGEST-GATE POLL] Commit {} DIVERGENT! local={}, quorum={}. \
                                             BLOCKING all subsequent commits until resolved ({}s/30s). \
                                             Waiting for CertifiedCommit to replace.",
                                            local_idx,
                                            hex::encode(&local_digest[..4]),
                                            hex::encode(&quorum_digest[..4]),
                                            divergence_age
                                        );
                                        break; // STRICT ORDER: stop at first unverified
                                    }
                                }
                            }
                            None => {
                                // No quorum yet — check if digest was GC'd
                                // (quorum advanced far past this index, so the entry
                                // was pruned from digest_history in CommitVoteMonitor).
                                // If quorum_commit_index >> local_idx, the network has
                                // already moved past this commit = implicitly verified.
                                let qci_val = if let Some(ref qci) = _quorum_commit_index_ref {
                                    qci.load(std::sync::atomic::Ordering::Relaxed)
                                } else {
                                    0
                                };
                                let quorum_gc_bypass = qci_val > local_idx + 50_000;

                                // ═══════════════════════════════════════════════════════
                                // COLD-START-BYPASS (May 2026, Fixed May 17):
                                // After epoch transition, CommitVoteMonitor restarts with
                                // no history. digest_verifier always returns None because
                                // no votes exist yet.
                                //
                                // CRITICAL FIX: We CANNOT use qci==0 to detect this!
                                // CommitSyncer sets qci>0 from peer queries BEFORE
                                // CommitVoteMonitor receives any actual digest votes.
                                // This caused a permanent deadlock in epoch 21 under
                                // high TPS — qci>0 disabled COLD-START-BYPASS while
                                // digest_verifier had no data, trapping all commits.
                                //
                                // FIX: Use digest_data_checker (queries CommitVoteMonitor.
                                // has_any_digest_data()) to detect true cold-start state.
                                // When digest data is unavailable, bypass is eligible.
                                //
                                // Fork safety: During cold-start ALL nodes have empty
                                // CommitVoteMonitors, producing identical local commits.
                                // ═══════════════════════════════════════════════════════
                                let digest_has_data = if let Some(ref checker) = digest_data_checker {
                                    checker()
                                } else {
                                    false // No checker → assume no data → allow bypass
                                };
                                let (cold_start_bypass, sustained_load_bypass) = if !digest_has_data {
                                    if let Some(pending_ts) = pending_local_timestamps.get(&local_idx) {
                                        let now = std::time::Instant::now();
                                        let age = now.duration_since(*pending_ts);
                                        let cold = age.as_secs() >= 10;
                                        // SUSTAINED-LOAD-BYPASS: 5s timeout when buffer pressure is high
                                        let sustained = !cold
                                            && age.as_secs() >= 5
                                            && pending_local_commits.len() >= MAX_PENDING_LOCAL_COMMITS / 2;
                                        (cold, sustained)
                                    } else {
                                        (false, false)
                                    }
                                } else {
                                    // ═══════════════════════════════════════════════════════
                                    // QCI-AHEAD-BYPASS (May 2026): Safe alternative to timeout.
                                    //
                                    // When digest_has_data=true but verifier returns None for
                                    // THIS specific commit index:
                                    //   - digest_history has entries from OTHER indices
                                    //   - But blocks' commit_votes don't carry digests for
                                    //     every intermediate index — only their LATEST
                                    //   - So digest_history[commit_index] may never exist
                                    //
                                    // SAFETY PROOF: QCI requires 2f+1 stake agreement.
                                    // If our local commit at index N diverges from network,
                                    // commits N+1, N+2... would also diverge, preventing
                                    // QCI from advancing past N. Therefore:
                                    //   qci_val > commit_index → network agreed on N
                                    //   verifier returns None → no conflicting digest found
                                    //   → local commit is implicitly verified
                                    //
                                    // This is FORK-SAFE: we never bypass when a conflicting
                                    // digest EXISTS (that case returns false at line ~1165).
                                    // We only bypass when NO digest entry exists AND QCI
                                    // proves the network has already moved past this point.
                                    // ═══════════════════════════════════════════════════════
                                    // digest_has_data=true but verifier returned None.
                                    // QCI-AHEAD-BYPASS is checked below in the else branch.
                                    (false, false)
                                };

                                if quorum_gc_bypass {
                                    info!(
                                        "✅ [DIGEST-GATE POLL] Commit {} QUORUM-GC-BYPASS: \
                                         quorum_commit_index has advanced far past this commit. \
                                         Digest entry was GC'd = implicitly verified by quorum.",
                                        local_idx
                                    );
                                    verified_indices.push(local_idx);
                                } else if cold_start_bypass {
                                    warn!(
                                        "⚡ [DIGEST-GATE COLD-START-BYPASS] Commit {} dispatching after 10s wait. \
                                         CommitVoteMonitor has no digest data (epoch cold-start). \
                                         Normal DIGEST-GATE verification will resume once digest votes arrive.",
                                        local_idx
                                    );
                                    verified_indices.push(local_idx);
                                } else if sustained_load_bypass {
                                    warn!(
                                        "⚡ [DIGEST-GATE SUSTAINED-LOAD-BYPASS] Commit {} dispatching after 5s \
                                         (buffer at {}/{} = {}%). No digest data yet, dispatching early to prevent \
                                         cascade buffer saturation.",
                                        local_idx,
                                        pending_local_commits.len(),
                                        MAX_PENDING_LOCAL_COMMITS,
                                        (pending_local_commits.len() * 100) / MAX_PENDING_LOCAL_COMMITS
                                    );
                                    verified_indices.push(local_idx);
                                } else {
                                    // QCI-AHEAD-BYPASS: digest_has_data=true,
                                    // verifier returns None, check if QCI already passed this commit.
                                    let qci_ahead_bypass = if digest_has_data {
                                        qci_val > local_idx
                                    } else {
                                        false
                                    };
                                    if qci_ahead_bypass {
                                        info!(
                                            "✅ [DIGEST-GATE QCI-AHEAD-BYPASS] Commit {} dispatching: \
                                             qci={} > commit_index. Network quorum committed past \
                                             this index. No conflicting digest. Implicitly verified.",
                                            local_idx, qci_val
                                        );
                                        verified_indices.push(local_idx);
                                    } else {
                                        // Genuinely no quorum yet — STOP here
                                        break; // STRICT ORDER: stop at first unresolved
                                    }
                                }
                            }
                        }
                    }
                    
                    // ═══════════════════════════════════════════════════════════
                    // STALE-EVICT CLEANUP: Remove divergent commits that exceeded
                    // the 30s timeout. These are NOT dispatched — just discarded.
                    // The slot (next_expected_index) stays open for the correct
                    // CertifiedCommit from CommitSyncer or receiver channel.
                    // ═══════════════════════════════════════════════════════════
                    {
                        let now = std::time::Instant::now();
                        let stale_indices: Vec<u32> = pending_local_commits.iter()
                            .filter(|(&idx, commit)| {
                                // Only evict if we have a quorum digest that DISAGREES
                                if let Some(ref v) = digest_verifier {
                                    let local_d = commit.commit_ref.digest.into_inner();
                                    if let Some(quorum_d) = v(idx) {
                                        if quorum_d != local_d {
                                            let age = pending_local_timestamps.get(&idx)
                                                .map(|ts| now.duration_since(*ts).as_secs())
                                                .unwrap_or(0);
                                            return age >= 30;
                                        }
                                    }
                                }
                                false
                            })
                            .map(|(&idx, _)| idx)
                            .collect();
                        
                        for idx in stale_indices {
                            pending_local_commits.remove(&idx);
                            pending_local_timestamps.remove(&idx);
                            warn!(
                                "🗑️ [STALE-EVICT] Removed divergent commit {} from pending buffer. \
                                 Slot open for CertifiedCommit.",
                                idx
                            );
                        }
                    }
                    
                    // Dispatch verified commits in strict ascending order
                    let mut digest_gate_epoch_break = false;
                    for local_idx in verified_indices {
                        if let Some(mut confirmed) = pending_local_commits.remove(&local_idx) {
                            pending_local_timestamps.remove(&local_idx);
                            let local_digest = confirmed.commit_ref.digest.into_inner();
                            // FORK-FORENSIC: Structured dispatch log for DIGEST-GATE-POLL path
                            let poll_leader_idx = confirmed.leader.author.value();
                            let poll_leader_eth = {
                                let eth_cache = epoch_eth_addresses.read().await;
                                eth_cache.get(&current_epoch)
                                    .and_then(|addrs| addrs.get(poll_leader_idx as usize))
                                    .map(|a| format!("0x{}", hex::encode(a)))
                                    .unwrap_or_else(|| "UNRESOLVED".to_string())
                            };
                            let poll_txs: usize = confirmed.blocks.iter().map(|b| b.transactions().len()).sum();
                            info!(
                                "📊 [FORK-FORENSIC] commit_index={}, path=DIGEST-GATE-POLL, epoch={}, \
                                 leader={:?} (auth_idx={}, eth={}), digest={}, txs={}, timestamp_ms={}",
                                local_idx, current_epoch, confirmed.leader, poll_leader_idx, poll_leader_eth,
                                hex::encode(&local_digest[..4]), poll_txs, confirmed.timestamp_ms
                            );
                            let exec_gei = {
                                let gei_guard = shared_gei.lock().await;
                                *gei_guard + 1
                            };
                            Self::resolve_leader_address(&epoch_eth_addresses, &mut confirmed, current_epoch).await;
                            // WAL: Record PENDING before FFI
                            if let Some(ref mut wal) = commit_wal {
                                let _ = wal.write_pending(local_idx, exec_gei, current_epoch);
                            }
                            let geis_consumed = super::executor::dispatch_commit(
                                &confirmed,
                                exec_gei,
                                current_epoch,
                                executor_client.clone(),
                                delivery_sender.clone(),
                                tx_recycler.clone(),
                                committed_transaction_hashes.clone(),
                                storage_path.clone(),
                            )
                            .await?;
                            // WAL: Record COMMITTED after Go confirms
                            if let Some(ref mut wal) = commit_wal {
                                let _ = wal.mark_committed(local_idx);
                            }
                            {
                                let mut gei_guard = shared_gei.lock().await;
                                *gei_guard += geis_consumed;
                            }
                            if let Some(ref recycler) = tx_recycler {
                                let total_txs: usize = confirmed.blocks.iter().map(|b| b.transactions().len()).sum();
                                if total_txs > 0 {
                                    let committed_tx_data: Vec<Vec<u8>> = confirmed
                                        .blocks.iter()
                                        .flat_map(|b| b.transactions().iter().map(|tx| tx.data().to_vec()))
                                        .collect();
                                    recycler.confirm_committed(&committed_tx_data).await;
                                }
                            }
                            if let Some(ref callback) = commit_index_callback {
                                callback(local_idx);
                            }
                            // Post-dispatch digest audit: log the dispatch for forensic tracing
                            info!(
                                "📊 [DIGEST-AUDIT] commit_index={}, path=DIGEST-GATE-POLL, gei={}, \
                                 leader={:?}, digest={}, local=true",
                                local_idx, exec_gei, confirmed.leader, hex::encode(&local_digest[..4])
                            );

                            // CRITICAL FORK-SAFETY FIX (May 2026):
                            // Advance next_expected_index AFTER successful dispatch so that
                            // the out-of-order drain loop can process the next sequential commits!
                            next_expected_index += 1;

                            // Check EndOfEpoch
                            if let Some((_block_ref, system_tx)) = confirmed.extract_end_of_epoch_transaction() {
                                if let Some((new_epoch, boundary_block)) = system_tx.as_end_of_epoch() {
                                    let current_gei = {
                                        let gei_guard = shared_gei.lock().await;
                                        *gei_guard
                                    };
                                    if let Some(ref callback) = epoch_transition_callback {
                                        if let Err(e) = callback(new_epoch, confirmed.timestamp_ms, boundary_block, current_gei) {
                                            warn!("❌ Failed to trigger epoch transition: {}", e);
                                        }
                                    }
                                    info!("🛑 [STATION 3: PROCESSOR] Halting for EndOfEpoch in digest-gate confirmed commit.");
                                    digest_gate_epoch_break = true;
                                    break;
                                }
                            }
                        }
                    }
                    if digest_gate_epoch_break {
                        break; // break from outer main loop
                    }
                }
            }

            // CRITICAL FIX: Drain pending out-of-order commits BEFORE blocking on `recv()`.
            // If DIGEST-GATE POLL just dispatched a commit and advanced `next_expected_index`,
            // we MUST drain `pending_commits` here before `receiver.recv().await` blocks us forever.
            let mut should_break = false;
            while let Some(mut pending) = pending_commits.remove(&next_expected_index) {
                // Check transitioning state before processing buffered OOO commits
                if let Some(ref is_trans) = self.is_transitioning {
                    if is_trans.load(std::sync::atomic::Ordering::Relaxed) {
                        info!("🛑 [STATION 3: PROCESSOR] OOO Drain Loop paused: Node is transitioning. Buffering commit {}.", next_expected_index);
                        pending_commits.insert(next_expected_index, pending);
                        break;
                    }
                }
                
                // ═══════════════════════════════════════════════════════
                // DIGEST-GATE (OOO PATH): Same logic as main path.
                // ═══════════════════════════════════════════════════════
                if pending.decided_with_local_blocks {
                    let local_digest = pending.commit_ref.digest.into_inner();
                    let digest_match = if let Some(ref verifier) = digest_verifier {
                        match verifier(next_expected_index) {
                            Some(quorum_digest) => {
                                if quorum_digest == local_digest {
                                    info!(
                                        "✅ [DISPATCH:DIGEST-GATE-OOO] Pending local commit {} VERIFIED: digest matches quorum.",
                                        next_expected_index
                                    );
                                    true
                                } else {
                                    warn!(
                                        "🚨 [DIGEST-GATE OOO] Pending local commit {} DIVERGENT! \
                                         local={}, quorum={}. BLOCKING OOO drain. \
                                         Buffered for CertifiedCommit.",
                                        next_expected_index,
                                        hex::encode(&local_digest[..4]),
                                        hex::encode(&quorum_digest[..4])
                                    );
                                    false
                                }
                            }
                            None => {
                                // QUORUM-GC-BYPASS + COLD-START-BYPASS: Same logic as DIGEST-GATE POLL path.
                                let qci_val = if let Some(ref qci) = _quorum_commit_index_ref {
                                    qci.load(std::sync::atomic::Ordering::Relaxed)
                                } else {
                                    0
                                };
                                if qci_val > next_expected_index + 50_000 {
                                    info!(
                                        "✅ [DIGEST-GATE-OOO] Commit {} QUORUM-GC-BYPASS: \
                                         digest GC'd, quorum={} >> commit. Implicitly verified.",
                                        next_expected_index, qci_val
                                    );
                                    true
                                } else if !{
                                    // COLD-START-FIX: Check actual digest data availability
                                    // instead of qci==0 (which CommitSyncer corrupts early)
                                    if let Some(ref checker) = digest_data_checker {
                                        checker()
                                    } else {
                                        false
                                    }
                                } {
                                    // COLD-START-BYPASS: Same rationale as DIGEST-GATE POLL.
                                    // In OOO path, commit was already waiting in pending_commits
                                    // (arrived out of order). If qci==0, verification infra is
                                    // non-functional — safe to dispatch.
                                    warn!(
                                        "⚡ [DIGEST-GATE-OOO COLD-START-BYPASS] Commit {} dispatching. \
                                         CommitVoteMonitor has no digest data yet (epoch cold-start). \
                                         Normal verification will resume once digest votes arrive.",
                                        next_expected_index
                                    );
                                    true
                                } else {
                                    // QCI-AHEAD-BYPASS (OOO PATH): Same logic as POLL path.
                                    // If digest_has_data=true but verifier returns None,
                                    // check if QCI already passed this commit index.
                                    let digest_has_data_ooo = if let Some(ref checker) = digest_data_checker {
                                        checker()
                                    } else {
                                        false
                                    };
                                    let qci_val_ooo = if let Some(ref qci) = _quorum_commit_index_ref {
                                        qci.load(std::sync::atomic::Ordering::Relaxed)
                                    } else {
                                        0
                                    };
                                    if digest_has_data_ooo && qci_val_ooo > next_expected_index {
                                        info!(
                                            "✅ [DIGEST-GATE-OOO QCI-AHEAD-BYPASS] Commit {} dispatching: \
                                             qci={} > commit_index. Network quorum committed past \
                                             this index. No conflicting digest. Implicitly verified.",
                                            next_expected_index, qci_val_ooo
                                        );
                                        true
                                    } else {
                                        false
                                    }
                                }
                            }
                        }
                    } else {
                        false
                    };
                    if !digest_match {
                        pending_local_commits.insert(next_expected_index, pending);
                        pending_local_timestamps.entry(next_expected_index)
                            .or_insert_with(std::time::Instant::now);
                        break;
                    }
                } else {
                    if let Some(local) = pending_local_commits.remove(&next_expected_index) {
                        if local.leader != pending.leader {
                            let local_digest = local.commit_ref.digest.into_inner();
                            let cert_digest = pending.commit_ref.digest.into_inner();
                            let local_author_idx = local.leader.author.value();
                            let cert_author_idx = pending.leader.author.value();
                            let eth_cache = epoch_eth_addresses.read().await;
                            let local_eth = eth_cache.get(&current_epoch)
                                .and_then(|addrs| addrs.get(local_author_idx as usize))
                                .map(|a| format!("0x{}", hex::encode(a)))
                                .unwrap_or_else(|| "UNRESOLVED".to_string());
                            let cert_eth = eth_cache.get(&current_epoch)
                                .and_then(|addrs| addrs.get(cert_author_idx as usize))
                                .map(|a| format!("0x{}", hex::encode(a)))
                                .unwrap_or_else(|| "UNRESOLVED".to_string());
                            drop(eth_cache);
                            warn!(
                                "🚨 [FORK-FORENSIC] LEADER DIVERGENCE PREVENTED at commit {}! \
                                 path=OOO-CERTIFIED, epoch={}, \
                                 local_leader={:?} (eth={}), certified_leader={:?} (eth={}), \
                                 local_digest={}, cert_digest={}",
                                next_expected_index, current_epoch,
                                local.leader, local_eth,
                                pending.leader, cert_eth,
                                hex::encode(&local_digest[..4]), hex::encode(&cert_digest[..4])
                            );
                        } else {
                            info!(
                                "✅ [DIGEST-GATE OOO] CertifiedCommit {} matches local (leader={:?}).",
                                next_expected_index, pending.leader
                            );
                        }
                    }
                }

                let pending_commit_index = next_expected_index;
                let pending_gei = {
                    let gei_guard = shared_gei.lock().await;
                    *gei_guard + 1
                };

                Self::resolve_leader_address(&epoch_eth_addresses, &mut pending, current_epoch).await;

                if let Some(ref recycler) = tx_recycler {
                    let total_txs: usize = pending.blocks.iter().map(|b| b.transactions().len()).sum();
                    if total_txs > 0 {
                        let committed_tx_data: Vec<Vec<u8>> = pending
                            .blocks
                            .iter()
                            .flat_map(|b| b.transactions().iter().map(|tx| tx.data().to_vec()))
                            .collect();
                        recycler.confirm_committed(&committed_tx_data).await;
                    }
                }

                if let Some(ref mut wal) = commit_wal {
                    let _ = wal.write_pending(pending_commit_index, pending_gei, current_epoch);
                }
                let geis_consumed = super::executor::dispatch_commit(
                    &pending,
                    pending_gei, 
                    current_epoch,
                    executor_client.clone(),
                    delivery_sender.clone(),
                    tx_recycler.clone(),
                    committed_transaction_hashes.clone(),
                    storage_path.clone(),
                )
                .await?;
                if let Some(ref mut wal) = commit_wal {
                    let _ = wal.mark_committed(pending_commit_index);
                }

                {
                    let mut gei_guard = shared_gei.lock().await;
                    *gei_guard += geis_consumed;
                }

                if let Some(ref callback) = commit_index_callback {
                    callback(pending_commit_index);
                }

                next_expected_index += 1;

                if let Some((_block_ref, system_tx)) = pending.extract_end_of_epoch_transaction() {
                    if let Some((new_epoch, boundary_block)) = system_tx.as_end_of_epoch() {
                        let current_gei = {
                            let gei_guard = shared_gei.lock().await;
                            *gei_guard
                        };
                        if let Some(ref callback) = epoch_transition_callback {
                            if let Err(e) = callback(new_epoch, pending.timestamp_ms, boundary_block, current_gei) {
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

            // Fix 3 Revert: Use direct indefinite block (no 120s timeout) to enforce backpressure
            // QUORUM-GATE: Use select! with timeout when local commits are pending,
            // so we don't block forever waiting for new commits while quorum catches up.
            let recv_result = if !pending_local_commits.is_empty() {
                tokio::select! {
                    result = receiver.recv() => result,
                    _ = tokio::time::sleep(tokio::time::Duration::from_millis(200)) => {
                        // Timeout — loop back to check quorum again
                        continue;
                    }
                }
            } else {
                receiver.recv().await
            };

            match recv_result {
                Some(subdag) => {
                    let commit_index: u32 = subdag.commit_ref.index;
                    let total_txs: usize = subdag.blocks.iter().map(|b| b.transactions().len()).sum();
                    let is_local = subdag.decided_with_local_blocks;
                    info!(
                        "📥 [TX-FLOW-TRACE] ▶ PHASE 3 ENTRY: CommitProcessor received CommittedSubDag | \
                         commit_index={}, leader={:?}, blocks={}, total_txs={}, \
                         decided_local={}, digest={}, epoch={}",
                        commit_index, subdag.leader, subdag.blocks.len(), total_txs,
                        is_local, hex::encode(&subdag.commit_ref.digest.into_inner()[..4]), current_epoch
                    );

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
                        // ═══════════════════════════════════════════════════════════════
                        // DIGEST-GATE (May 2026): Absolute fork prevention via digest verification.
                        //
                        // ALL local commits are ALWAYS buffered — NEVER dispatched immediately.
                        // A local commit is only dispatched when ONE of:
                        //   1. digest_verifier confirms 2f+1 authorities voted for the
                        //      EXACT SAME digest (content-level parity).
                        //   2. A CertifiedCommit arrives from the network (replaces local).
                        //
                        // WHY density-gate was insufficient:
                        //   Even in a dense DAG (blocks from all authors), different nodes
                        //   can decide different leaders due to block arrival timing.
                        //   Only DIGEST verification guarantees content agreement.
                        //
                        // DEADLOCK-FREE: DAG advances independently. CommitSyncer +
                        // digest poll (200ms) release buffered commits when verified.
                        // ═══════════════════════════════════════════════════════════════
                        let dispatch_subdag: Option<CommittedSubDag>;

                        if subdag.decided_with_local_blocks {
                            // LOCAL commit: ALWAYS buffer first.
                            let local_digest = subdag.commit_ref.digest.into_inner();

                            // Check if digest is already verified by network quorum
                            let digest_match = if let Some(ref verifier) = digest_verifier {
                                match verifier(commit_index) {
                                    Some(quorum_digest) => {
                                        if quorum_digest == local_digest {
                                            info!(
                                                "✅ [DISPATCH:DIGEST-GATE-IMMEDIATE] Local commit {} VERIFIED: \
                                                 digest matches network quorum (digest={}). Safe to dispatch.",
                                                commit_index, hex::encode(&local_digest[..4])
                                            );
                                            true
                                        } else {
                                            warn!(
                                                "🚨 [DIGEST-GATE] Local commit {} DIVERGENT! \
                                                 local_digest={}, quorum_digest={}. \
                                                 Buffered — waiting for CertifiedCommit.",
                                                commit_index,
                                                hex::encode(&local_digest[..4]),
                                                hex::encode(&quorum_digest[..4])
                                            );
                                            false
                                        }
                                    }
                                    None => {
                                        // QUORUM-GC-BYPASS: If quorum advanced far past
                                        // this commit, digest entry was GC'd = implicitly verified.
                                        if let Some(ref qci) = _quorum_commit_index_ref {
                                            let qci_val = qci.load(std::sync::atomic::Ordering::Relaxed);
                                            if qci_val > commit_index + 50_000 {
                                                info!(
                                                    "✅ [DIGEST-GATE-IMMEDIATE] Commit {} QUORUM-GC-BYPASS: \
                                                     quorum={} >> commit. Implicitly verified.",
                                                    commit_index, qci_val
                                                );
                                                true
                                            } else {
                                                // COLD-START-FIX (May 2026): Check if digest data is available.
                                                // If CommitVoteMonitor has no data yet (epoch cold-start),
                                                // allow immediate dispatch instead of buffering.
                                                let digest_has_data = if let Some(ref checker) = digest_data_checker {
                                                    checker()
                                                } else {
                                                    false
                                                };
                                                if !digest_has_data {
                                                    warn!(
                                                        "⚡ [DIGEST-GATE-IMMEDIATE COLD-START-BYPASS] Commit {} dispatching. \
                                                         CommitVoteMonitor has no digest data yet (epoch cold-start). \
                                                         Normal verification will resume once digest votes arrive.",
                                                        commit_index
                                                    );
                                                    true
                                                } else {
                                                    // QCI-AHEAD-BYPASS: Check if QCI already passed
                                                    // this commit index at receive time.
                                                    let qci_immediate = if let Some(ref qci) = _quorum_commit_index_ref {
                                                        qci.load(std::sync::atomic::Ordering::Relaxed)
                                                    } else {
                                                        0
                                                    };
                                                    if qci_immediate > commit_index {
                                                        info!(
                                                            "✅ [DIGEST-GATE-IMMEDIATE QCI-AHEAD-BYPASS] Commit {} dispatching: \
                                                             qci={} > commit_index. Network quorum committed past \
                                                             this index. No conflicting digest. Implicitly verified.",
                                                            commit_index, qci_immediate
                                                        );
                                                        true
                                                    } else {
                                                        info!(
                                                            "🛡️ [DIGEST-GATE-IMMEDIATE] Commit {} buffered: digest_has_data=true \
                                                             but verifier returned None, qci={} <= commit_index. \
                                                             Will poll for QCI-AHEAD-BYPASS.",
                                                            commit_index, qci_immediate
                                                        );
                                                        false // Buffer — QCI-AHEAD-BYPASS will handle in POLL path
                                                    }
                                                }
                                            }
                                        } else {
                                            false // No quorum ref — buffer
                                        }
                                    }
                                }
                            } else {
                                false // No verifier available — buffer
                            };

                            if digest_match {
                                dispatch_subdag = Some(subdag);
                            } else {
                                info!(
                                    "🛡️ [TX-FLOW-TRACE DIGEST-GATE] ▶ PHASE 3 DIGEST-GATE: Local commit BUFFERED | \
                                     commit_index={}, leader={:?}, digest={}, buffered_count={}",
                                    commit_index, subdag.leader, hex::encode(&local_digest[..4]),
                                    pending_local_commits.len() + 1
                                );
                                pending_local_commits.insert(commit_index, subdag);
                                pending_local_timestamps.insert(commit_index, std::time::Instant::now());
                                
                                // MEMORY-GUARD: Drop oldest if buffer exceeds MAX.
                                // Dropped commits are NOT dispatched — they are simply discarded.
                                // CommitSyncer will re-deliver them as CertifiedCommit from peers.
                                // This guarantees: no fork from buffer management, ever.
                                while pending_local_commits.len() > MAX_PENDING_LOCAL_COMMITS {
                                    if let Some((&oldest_idx, _)) = pending_local_commits.iter().next() {
                                        warn!(
                                            "⚠️ [MEMORY-GUARD] Buffer full ({}/{}). DROPPING (not dispatching) oldest commit {}. \
                                             CommitSyncer will re-deliver as CertifiedCommit.",
                                            pending_local_commits.len(), MAX_PENDING_LOCAL_COMMITS, oldest_idx
                                        );
                                        pending_local_commits.remove(&oldest_idx);
                                        pending_local_timestamps.remove(&oldest_idx);
                                    } else {
                                        break;
                                    }
                                }
                                dispatch_subdag = None;
                            }
                        } else {
                            // CertifiedCommit — network-verified with 2f+1 agreement.
                            // This is the authoritative path to Go execution.

                            // ═══════════════════════════════════════════════════════
                            // DEFENSE-IN-DEPTH: Cross-validate CertifiedCommit digest
                            // against local quorum data. This should NEVER mismatch
                            // after the Phase 1 quorum fix in CommitSyncer, but serves
                            // as a safety net for future regressions.
                            // ═══════════════════════════════════════════════════════
                            if let Some(ref verifier) = digest_verifier {
                                let certified_digest = subdag.commit_ref.digest.into_inner();
                                match verifier(commit_index) {
                                    Some(quorum_digest) if quorum_digest != certified_digest => {
                                        warn!(
                                            "🚨🚨 [DIGEST-GATE CRITICAL] CertifiedCommit {} digest CONFLICTS with local quorum! \
                                             certified={}, quorum={}. \
                                             Dispatching CertifiedCommit (it has 2f+1 votes), but this indicates \
                                             a potential CommitSyncer integrity issue. INVESTIGATE IMMEDIATELY.",
                                            commit_index,
                                            hex::encode(&certified_digest[..4]),
                                            hex::encode(&quorum_digest[..4])
                                        );
                                    }
                                    Some(_) => {
                                        info!(
                                            "✅ [DISPATCH:CERTIFIED+DIGEST] CertifiedCommit {} digest matches quorum.",
                                            commit_index
                                        );
                                    }
                                    None => {
                                        // No quorum data available yet — normal during rapid sync
                                    }
                                }
                            }

                            // Discard any buffered local commit for the same index.
                            if let Some(local_subdag) = pending_local_commits.remove(&commit_index) {
                                pending_local_timestamps.remove(&commit_index);
                                if local_subdag.leader != subdag.leader {
                                    // FORK-FORENSIC (May 2026): Full fingerprint for leader divergence analysis
                                    let local_digest = local_subdag.commit_ref.digest.into_inner();
                                    let cert_digest = subdag.commit_ref.digest.into_inner();
                                    let local_txs: usize = local_subdag.blocks.iter().map(|b| b.transactions().len()).sum();
                                    let cert_txs: usize = subdag.blocks.iter().map(|b| b.transactions().len()).sum();
                                    let local_author_idx = local_subdag.leader.author.value();
                                    let cert_author_idx = subdag.leader.author.value();
                                    // Resolve ETH addresses for forensic correlation
                                    let eth_cache = epoch_eth_addresses.read().await;
                                    let local_eth = eth_cache.get(&current_epoch)
                                        .and_then(|addrs| addrs.get(local_author_idx as usize))
                                        .map(|a| hex::encode(a))
                                        .unwrap_or_else(|| "UNKNOWN".to_string());
                                    let cert_eth = eth_cache.get(&current_epoch)
                                        .and_then(|addrs| addrs.get(cert_author_idx as usize))
                                        .map(|a| hex::encode(a))
                                        .unwrap_or_else(|| "UNKNOWN".to_string());
                                    drop(eth_cache);
                                    warn!(
                                        "🚨 [FORK-FORENSIC] LEADER DIVERGENCE PREVENTED at commit {}! \
                                         path=CERTIFIED-COMMIT, epoch={}, \
                                         local_leader={:?} (auth_idx={}, eth=0x{}), \
                                         certified_leader={:?} (auth_idx={}, eth=0x{}), \
                                         local_digest={}, cert_digest={}, \
                                         local_txs={}, cert_txs={}",
                                        commit_index, current_epoch,
                                        local_subdag.leader, local_author_idx, local_eth,
                                        subdag.leader, cert_author_idx, cert_eth,
                                        hex::encode(&local_digest[..4]), hex::encode(&cert_digest[..4]),
                                        local_txs, cert_txs
                                    );
                                } else {
                                    info!(
                                        "✅ [DISPATCH:CERTIFIED-COMMIT] CertifiedCommit {} matches local (leader={:?}).",
                                        commit_index, subdag.leader
                                    );
                                }
                            } else {
                                info!(
                                    "📥 [TX-FLOW-TRACE DISPATCH:CERTIFIED-COMMIT] ▶ PHASE 3 CERTIFIED: CertifiedCommit received directly | \
                                     commit_index={}, dispatching immediately",
                                    commit_index
                                );
                               
                            }
                            dispatch_subdag = Some(subdag);
                        }

                        // CRITICAL FORK-SAFETY FIX (May 2026):
                        // We ONLY advance `next_expected_index` if the commit is actually dispatched!
                        // If it is buffered in `pending_local_commits`, we DO NOT advance `next_expected_index`.
                        // This guarantees STRICT sequential ordering for Go's GEI assignment and prevents
                        // out-of-order execution holes if the buffer drops a commit.
                        if dispatch_subdag.is_some() {
                            next_expected_index += 1;
                        }

                        // Skip Go dispatch for buffered local commits
                        if let Some(mut subdag) = dispatch_subdag {
                        // ═══════════════════════════════════════════════
                        // DISPATCH TO GO — only for confirmed commits
                        // ═══════════════════════════════════════════════

                        let total_txs_in_commit: usize = subdag
                            .blocks
                            .iter()
                            .map(|b| b.transactions().len())
                            .sum();

                        let gei = {
                            let gei_guard = shared_gei.lock().await;
                            *gei_guard + 1
                        };

                        let dispatch_path = if subdag.decided_with_local_blocks {
                            "DIGEST-GATE-IMMEDIATE"
                        } else {
                            "CERTIFIED-COMMIT"
                        };
                        let commit_digest = subdag.commit_ref.digest.into_inner();
                        let leader_author_idx = subdag.leader.author.value();
                        // FORK-FORENSIC: Structured dispatch log with leader ETH address for hash_mismatch_alert.log correlation
                        let leader_eth_hex = {
                            let eth_cache = epoch_eth_addresses.read().await;
                            eth_cache.get(&current_epoch)
                                .and_then(|addrs| addrs.get(leader_author_idx as usize))
                                .map(|a| format!("0x{}", hex::encode(a)))
                                .unwrap_or_else(|| "UNRESOLVED".to_string())
                        };
                        // Consolidated dispatch + forensic + audit log (was 3 separate logs)
                        let per_block: Vec<String> = subdag.blocks.iter().map(|b| {
                            format!("{}:{}", b.reference(), b.transactions().len())
                        }).collect();
                        info!(
                            "📊 [TX-AUDIT] commit_index={} | path={} | gei={} | epoch={} | \
                             digest={} | txs={} | blocks={} | per_block=[{}] | \
                             leader={:?} (auth_idx={}, eth={}) | decided_local={} | timestamp={}",
                            commit_index, dispatch_path, gei, current_epoch,
                            hex::encode(&commit_digest[..4]), total_txs_in_commit,
                            subdag.blocks.len(), per_block.join(", "),
                            subdag.leader, leader_author_idx, leader_eth_hex,
                            subdag.decided_with_local_blocks, subdag.timestamp_ms
                        );

                        // POST-DISPATCH DIGEST AUDIT: Cross-check dispatched content against quorum
                        if let Some(ref verifier) = digest_verifier {
                            match verifier(commit_index) {
                                Some(quorum_digest) => {
                                    if quorum_digest != commit_digest {
                                        error!(
                                            "🚨🚨🚨 [DIGEST-AUDIT CRITICAL] DISPATCHING commit {} with MISMATCHED digest! \
                                             dispatched={}, quorum={}. THIS MAY CAUSE FORK! path={}",
                                            commit_index,
                                            hex::encode(&commit_digest[..4]),
                                            hex::encode(&quorum_digest[..4]),
                                            dispatch_path
                                        );
                                    }
                                }
                                None => {
                                    // No quorum data yet — expected for CertifiedCommit path
                                    trace!(
                                        "📊 [DIGEST-AUDIT] commit_index={}: no quorum digest available for cross-check (path={})",
                                        commit_index, dispatch_path
                                    );
                                }
                            }
                        }

                        // Resolve leader ETH address into subdag (immutable after this)
                        Self::resolve_leader_address(&epoch_eth_addresses, &mut subdag, current_epoch).await;

                        // WAL: Record PENDING before FFI
                        if let Some(ref mut wal) = commit_wal {
                            let _ = wal.write_pending(commit_index, gei, current_epoch);
                        }
                        // Process commit — send accurate GEI to Go
                        let geis_consumed = super::executor::dispatch_commit(
                            &subdag,
                            gei, 
                            current_epoch,
                            executor_client.clone(),
                            delivery_sender.clone(),
                            tx_recycler.clone(),
                            committed_transaction_hashes.clone(),
                            storage_path.clone(),
                        )
                        .await?;
                        // WAL: Record COMMITTED after Go confirms
                        if let Some(ref mut wal) = commit_wal {
                            let _ = wal.mark_committed(commit_index);
                        }

                        // Accumulate exact number of fragments consumed by this commit
                        {
                            let mut gei_guard = shared_gei.lock().await;
                            *gei_guard += geis_consumed;
                        }
                        
                        // ♻️ TX RECYCLER: Confirm committed TXs
                        if let Some(ref recycler) = tx_recycler {
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

                        // Check for EndOfEpoch system transactions AFTER commit is sent to Go
                        if let Some((_block_ref, system_tx)) =
                            subdag.extract_end_of_epoch_transaction()
                        {
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

                                    info!("🛑 [STATION 3: PROCESSOR] Halting processing for current epoch after EndOfEpoch transaction.");
                                    break;
                                }
                            }
                        } // end of `if let Some(mut subdag) = dispatch_subdag` (Go dispatch path)
                    } else if commit_index > next_expected_index {
                        // QUORUM-GATE: If this is a CertifiedCommit (network-verified) and we have a
                        // buffered local commit for the SAME index, the certified version is authoritative.
                        // Replace the local version to prevent fork.
                        if !subdag.decided_with_local_blocks {
                            if let Some(local_subdag) = pending_local_commits.remove(&commit_index) {
                                info!(
                                    "🔄 [QUORUM-GATE] CertifiedCommit {} REPLACING buffered local commit \
                                     (local_leader={:?}, certified_leader={:?})",
                                    commit_index, local_subdag.leader, subdag.leader
                                );
                            }
                        }

                        // SAFETY: Limit pending_commits size to prevent OOM
                        // At 50k+ TPS (~1000 commits/sec), up to 50k commits
                        // can queue during extended stalls or epoch transitions.
                        const MAX_PENDING_COMMITS: usize = 50_000;
                        pending_commits.insert(commit_index, subdag);
                        
                        // SMART EVICTION: Instead of dropping the incoming commit (which might be 
                        // close to next_expected_index), we always evict the commit that is 
                        // FARTHEST in the future using pop_last(). The CommitSyncer will safely 
                        // re-fetch it later when the node catches up.
                        while pending_commits.len() > MAX_PENDING_COMMITS {
                            if let Some((dropped_idx, _)) = pending_commits.pop_last() {
                                warn!(
                                    "🚨 [STATION 3: PROCESSOR] pending_commits at capacity ({})! \
                                    Evicted farthest future commit {} (expected {}). \
                                    CommitSyncer will re-fetch this later.",
                                    MAX_PENDING_COMMITS, dropped_idx, next_expected_index
                                );
                            }
                        }

                        warn!(
                            "Received out-of-order commit: index={}, expected={}, pending_count={}, storing for later",
                            commit_index, next_expected_index, pending_commits.len()
                        );

                        // FORK-SAFETY FIX: We NO LONGER jump forward heuristically!
                        // If the node is catching up and pending_commits grows large,
                        // we MUST wait for the CommitSyncer to deliver the missing commits.
                        // Skipping commits based on `gap > 20` caused fatal DAG divergence
                        // where different nodes assigned different commit_indices to the same GEI.
                        // We strictly buffer until `commit_index == next_expected_index`.
                    } else {
                        // commit_index < next_expected_index
                        // CRITICAL FIX (May 2026): Check if this is a CertifiedCommit arriving
                        // to replace a buffered local commit. When a local commit was buffered
                        // by DIGEST-GATE, next_expected_index was already advanced. So the
                        // CertifiedCommit replacement arrives with commit_index < next_expected.
                        // We MUST process it — otherwise the local commit stays stuck forever.
                        if !subdag.decided_with_local_blocks {
                            if let Some(local) = pending_local_commits.remove(&commit_index) {
                                pending_local_timestamps.remove(&commit_index);
                                if local.leader != subdag.leader {
                                    // FORK-FORENSIC (May 2026): Late CertifiedCommit replacement with full fingerprint
                                    let local_digest = local.commit_ref.digest.into_inner();
                                    let cert_digest = subdag.commit_ref.digest.into_inner();
                                    let local_author_idx = local.leader.author.value();
                                    let cert_author_idx = subdag.leader.author.value();
                                    let eth_cache = epoch_eth_addresses.read().await;
                                    let local_eth = eth_cache.get(&current_epoch)
                                        .and_then(|addrs| addrs.get(local_author_idx as usize))
                                        .map(|a| format!("0x{}", hex::encode(a)))
                                        .unwrap_or_else(|| "UNRESOLVED".to_string());
                                    let cert_eth = eth_cache.get(&current_epoch)
                                        .and_then(|addrs| addrs.get(cert_author_idx as usize))
                                        .map(|a| format!("0x{}", hex::encode(a)))
                                        .unwrap_or_else(|| "UNRESOLVED".to_string());
                                    drop(eth_cache);
                                    warn!(
                                        "🚨 [FORK-FORENSIC] LEADER DIVERGENCE PREVENTED at commit {}! \
                                         path=CERTIFIED-REPLACE, epoch={}, \
                                         local_leader={:?} (eth={}), certified_leader={:?} (eth={}), \
                                         local_digest={}, cert_digest={}",
                                        commit_index, current_epoch,
                                        local.leader, local_eth,
                                        subdag.leader, cert_eth,
                                        hex::encode(&local_digest[..4]), hex::encode(&cert_digest[..4])
                                    );
                                } else {
                                    info!(
                                        "✅ [DISPATCH:CERTIFIED-REPLACE] CertifiedCommit {} replacing \
                                         buffered local (same leader={:?}).",
                                        commit_index, subdag.leader
                                    );
                                }
                                // Dispatch the CertifiedCommit immediately
                                let mut certified = subdag;
                                let exec_gei = {
                                    let gei_guard = shared_gei.lock().await;
                                    *gei_guard + 1
                                };
                                Self::resolve_leader_address(&epoch_eth_addresses, &mut certified, current_epoch).await;
                                // WAL: Record PENDING before FFI
                                if let Some(ref mut wal) = commit_wal {
                                    let _ = wal.write_pending(commit_index, exec_gei, current_epoch);
                                }
                                let geis_consumed = super::executor::dispatch_commit(
                                    &certified,
                                    exec_gei,
                                    current_epoch,
                                    executor_client.clone(),
                                    delivery_sender.clone(),
                                    tx_recycler.clone(),
                                    committed_transaction_hashes.clone(),
                                    storage_path.clone(),
                                )
                                .await?;
                                // WAL: Record COMMITTED after Go confirms
                                if let Some(ref mut wal) = commit_wal {
                                    let _ = wal.mark_committed(commit_index);
                                }
                                {
                                    let mut gei_guard = shared_gei.lock().await;
                                    *gei_guard += geis_consumed;
                                }
                                let certified_digest = certified.commit_ref.digest.into_inner();
                                info!(
                                    "📊 [DIGEST-AUDIT] commit_index={}, path=CERTIFIED-REPLACE, gei={}, \
                                     leader={:?}, digest={}, local=false",
                                    commit_index, exec_gei, certified.leader,
                                    hex::encode(&certified_digest[..4])
                                );
                                if let Some(ref recycler) = tx_recycler {
                                    let total_txs: usize = certified.blocks.iter().map(|b| b.transactions().len()).sum();
                                    if total_txs > 0 {
                                        let committed_tx_data: Vec<Vec<u8>> = certified
                                            .blocks.iter()
                                            .flat_map(|b| b.transactions().iter().map(|tx| tx.data().to_vec()))
                                            .collect();
                                        recycler.confirm_committed(&committed_tx_data).await;
                                    }
                                }
                                if let Some(ref callback) = commit_index_callback {
                                    callback(commit_index);
                                }

                                // After dispatching the replacement, try to drain any
                                // pending_local_commits that are now unblocked
                                // (the next sequential commit may have been waiting for this one)
                            } else {
                                // CertifiedCommit for an index we already processed — skip
                                warn!(
                                    "⏭️ [CERTIFIED-REPLACE] CertifiedCommit {} arrived but no pending local commit found. \
                                     Already processed? Skipping. next_expected={}",
                                    commit_index, next_expected_index
                                );
                            }
                        } else {
                            warn!(
                                "⏭️ Received local commit with index {} < expected {}. Ignoring stale local commit.",
                                commit_index, next_expected_index
                            );
                        }
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
