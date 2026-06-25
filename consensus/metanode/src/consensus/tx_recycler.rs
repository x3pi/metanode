// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! TX Recycler: Reclaims transactions from uncommitted DAG proposals.
//!
//! Problem: TransactionConsumer.next() POPs TXs from the mpsc channel permanently.
//! If the proposed block containing those TXs is never committed (GC'd), TXs are lost.
//!
//! Solution: Track submitted TXs by hash. When committed sub-DAGs arrive, mark their
//! TXs as confirmed. Periodically re-submit unconfirmed TXs back to consensus.



use sha3::{Digest, Keccak256};
use dashmap::DashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};
use std::sync::atomic::{AtomicU64, Ordering};
use tracing::{info, warn};

/// How long to wait before recycling unconfirmed TXs
const RECYCLE_TIMEOUT: Duration = Duration::from_secs(15);

/// Maximum number of pending TXs to track (memory safety)
const MAX_PENDING_TXS: usize = 100_000;

/// A pending TX waiting to be confirmed
struct PendingTx {
    /// Raw TX data for re-submission
    data: Vec<u8>,
    /// When this TX was submitted
    submitted_at: Instant,
    /// How many times this TX has been recycled
    recycle_count: u32,
}

/// Shared TX recycler between tx_socket_server and commit_processor
pub struct TxRecycler {
    /// TXs submitted but not yet confirmed: tx_hash -> PendingTx
    pending: DashMap<[u8; 32], PendingTx>,
    /// Stats
    total_submitted: AtomicU64,
    total_confirmed: AtomicU64,
    total_recycled: AtomicU64,
}

impl TxRecycler {
    pub fn new() -> Self {
        Self {
            pending: DashMap::new(),
            total_submitted: AtomicU64::new(0),
            total_confirmed: AtomicU64::new(0),
            total_recycled: AtomicU64::new(0),
        }
    }

    /// Clear all pending TXs during epoch transitions to prevent unbounded memory growth.
    /// TXs from the old epoch are no longer relevant — they were either committed (and
    /// confirm_committed missed them due to CommitProcessor halting) or will be recovered
    /// via `recover_epoch_pending_transactions` which tracks them separately.
    pub async fn clear_pending(&self) {
        let count = self.pending.len();
        self.pending.clear();
        self.pending.shrink_to_fit();
        if count > 0 {
            info!(
                "🧹 [TX RECYCLER] Epoch transition: cleared {} pending TXs to prevent memory leak",
                count
            );
        }
    }

    /// Drain all pending TXs and return their raw data for migration to the new epoch.
    /// Unlike clear_pending(), this preserves the TX data so unconfirmed TXs can be
    /// re-submitted in the new epoch's consensus, preventing TX loss during transitions.
    ///
    /// STABILITY FIX: Previously, clear_pending() dropped all pending TXs including
    /// those that were submitted but not yet committed. If confirm_committed() was never
    /// called (BUG 2), ALL pending TXs were lost at every epoch boundary.
    pub async fn drain_all_pending(&self) -> Vec<Vec<u8>> {
        let count = self.pending.len();
        let txs: Vec<Vec<u8>> = self.pending.iter().map(|ptx| ptx.data.clone()).collect();
        self.pending.clear();
        self.pending.shrink_to_fit();
        if count > 0 {
            info!(
                "♻️ [TX RECYCLER] Epoch transition: drained {} pending TXs for migration to new epoch",
                count
            );
        }
        txs
    }

    /// Hash TX data using SHA-256 (same as Rust consensus TX identity)
    pub fn hash_tx(data: &[u8]) -> [u8; 32] {
        let mut hasher = Keccak256::new();
        hasher.update(data);
        hasher.finalize().into()
    }

    /// Track submitted TXs. Called by tx_socket_server after client.submit() succeeds.
    pub async fn track_submitted(&self, tx_data_list: &[Vec<u8>]) {
        // PERF: Pre-compute hashes concurrently outside the Mutex to prevent
        // blocking the async executor during high-throughput submission.
        use rayon::prelude::*;
        let hashes: Vec<[u8; 32]> = tx_data_list
            .par_iter()
            .map(|tx_data| Self::hash_tx(tx_data))
            .collect();

        for (tx_data, hash) in tx_data_list.iter().zip(hashes) {
            // Don't overwrite if already pending (might be a re-submission)
            if !self.pending.contains_key(&hash) {
                // Memory safety: evict pseudo-random element if too many.
                if self.pending.len() >= MAX_PENDING_TXS {
                    // In DashMap, we can just grab an arbitrary key to evict
                    let key_to_remove = self.pending.iter().next().map(|r| *r.key());
                    if let Some(k) = key_to_remove {
                        self.pending.remove(&k);
                    }
                }

                self.pending.insert(
                    hash,
                    PendingTx {
                        data: tx_data.clone(),
                        submitted_at: Instant::now(),
                        recycle_count: 0,
                    },
                );
            }

            self.total_submitted.fetch_add(1, Ordering::Relaxed);
        }
    }

    /// Mark TXs as confirmed (committed). Called by commit_processor when processing sub-DAGs.
    /// `committed_tx_data` is the raw TX bytes from committed blocks.
    pub async fn confirm_committed<T: AsRef<[u8]> + Sync>(&self, committed_tx_data: &[T]) {
        // Pre-compute hashes concurrently to minimize the Mutex lock duration.
        // Hashing 50k transactions sequentially takes significant time.
        use rayon::prelude::*;
        let hashes: Vec<[u8; 32]> = committed_tx_data
            .par_iter()
            .map(|tx_data| Self::hash_tx(tx_data.as_ref()))
            .collect();

        let before = self.pending.len();
        for hash in hashes {
            if self.pending.remove(&hash).is_some() {
                self.total_confirmed.fetch_add(1, Ordering::Relaxed);
            }
        }
        let removed = before - self.pending.len();

        if removed > 0 {
            info!(
                "♻️ [TX RECYCLER] Confirmed {} TXs from committed sub-DAG ({} still pending)",
                removed,
                self.pending.len()
            );
        }
    }

    /// Collect TXs that have been pending too long and need re-submission.
    /// Returns the raw TX data for re-submission. Max 3 recycle attempts per TX.
    pub async fn collect_stale(&self) -> Vec<Vec<u8>> {
        let now = Instant::now();
        let mut stale_txs = Vec::new();
        let mut keys_to_update = Vec::new();

        for entry in self.pending.iter() {
            let ptx = entry.value();
            if now.duration_since(ptx.submitted_at) > RECYCLE_TIMEOUT && ptx.recycle_count < 3 {
                stale_txs.push(ptx.data.clone());
                keys_to_update.push(*entry.key());
            }
        }

        // Update recycle count and reset timer for re-submitted TXs
        for hash in &keys_to_update {
            if let Some(mut ptx) = self.pending.get_mut(hash) {
                ptx.recycle_count += 1;
                ptx.submitted_at = now; // Reset timer for next recycle attempt
                self.total_recycled.fetch_add(1, Ordering::Relaxed);
            }
        }

        // Remove TXs that exceeded max retries
        let mut expired_count = 0;
        self.pending.retain(|_, ptx| {
            if ptx.recycle_count >= 3 && now.duration_since(ptx.submitted_at) > RECYCLE_TIMEOUT {
                expired_count += 1;
                false
            } else {
                true
            }
        });

        if !stale_txs.is_empty() || expired_count > 0 {
            warn!(
                "♻️ [TX RECYCLER] Recycling {} stale TXs, expired {} (pending: {})",
                stale_txs.len(),
                expired_count,
                self.pending.len()
            );
        }

        stale_txs
    }

    // Get current stats
    pub async fn stats(&self) -> (usize, u64, u64, u64) {
        let submitted = self.total_submitted.load(Ordering::Relaxed);
        let confirmed = self.total_confirmed.load(Ordering::Relaxed);
        let recycled = self.total_recycled.load(Ordering::Relaxed);
        (self.pending.len(), submitted, confirmed, recycled)
    }

    /// Load pending TXs from disk on startup
    pub async fn load_from_disk(&self, storage_path: &str) {
        let file_path = format!("{}/tx_recycler_pending.dat", storage_path);
        if let Ok(data) = std::fs::read(&file_path) {
            let mut offset = 0;
            let mut loaded_count = 0;
            while offset + 32 < data.len() {
                let mut hash = [0u8; 32];
                hash.copy_from_slice(&data[offset..offset + 32]);
                offset += 32;

                if offset + 4 > data.len() {
                    break;
                }
                let tx_len = u32::from_le_bytes([
                    data[offset],
                    data[offset + 1],
                    data[offset + 2],
                    data[offset + 3],
                ]) as usize;
                offset += 4;

                if offset + tx_len > data.len() {
                    break;
                }
                let tx_data = data[offset..offset + tx_len].to_vec();
                offset += tx_len;

                self.pending.insert(
                    hash,
                    PendingTx {
                        data: tx_data,
                        submitted_at: Instant::now(), // Reset timer on startup
                        recycle_count: 0,
                    },
                );
                loaded_count += 1;
            }
            if loaded_count > 0 {
                info!(
                    "💾 [TX RECYCLER] Hydrated {} pending transactions from disk",
                    loaded_count
                );
            }
        }
    }

    /// Save pending TXs to disk
    pub async fn save_to_disk(&self, storage_path: &str) {
        // Don't save if empty to save I/O
        if self.pending.is_empty() {
            return;
        }

        let mut data = Vec::new();
        for entry in self.pending.iter() {
            data.extend_from_slice(entry.key());
            data.extend_from_slice(&(entry.value().data.len() as u32).to_le_bytes());
            data.extend_from_slice(&entry.value().data);
        }

        let file_path = format!("{}/tx_recycler_pending.dat", storage_path);
        let temp_path = format!("{}.tmp", file_path);
        if std::fs::write(&temp_path, data).is_ok() {
            let _ = std::fs::rename(temp_path, file_path);
        }
    }
}

/// Start background recycler task that periodically re-submits stale TXs
pub async fn start_recycler_background(
    recycler: Arc<TxRecycler>,
    transaction_client: Arc<dyn crate::node::tx_submitter::TransactionSubmitter>,
    storage_path: String,
) {
    info!(
        "♻️ [TX RECYCLER] Background recycler started (timeout={}s, max_retries=3)",
        RECYCLE_TIMEOUT.as_secs()
    );

    // Hydrate state from disk on startup
    recycler.load_from_disk(&storage_path).await;

    let mut interval = tokio::time::interval(Duration::from_secs(5));
    let mut last_stats_log = Instant::now();
    let mut last_save = Instant::now();

    loop {
        interval.tick().await;

        // Collect stale TXs
        let stale_txs = recycler.collect_stale().await;

        for tx in stale_txs {
            let single_tx_vec = vec![tx];
            match transaction_client.submit(single_tx_vec).await {
                Ok((block_ref, _indices, _)) => {
                    info!(
                        "♻️ [TX RECYCLER] Successfully recycled stale TX into block {:?}",
                        block_ref
                    );
                }
                Err(e) => {
                    warn!(
                        "♻️ [TX RECYCLER] Failed to re-submit stale TX: {}",
                        e
                    );
                }
            }
        }

        // Persist state to disk periodically (every 5s)
        if last_save.elapsed() >= Duration::from_secs(5) {
            recycler.save_to_disk(&storage_path).await;
            last_save = Instant::now();
        }

        // Log stats periodically (every 60s)
        if last_stats_log.elapsed() > Duration::from_secs(60) {
            let (pending, submitted, confirmed, recycled) = recycler.stats().await;
            if pending > 0 || recycled > 0 {
                info!(
                    "♻️ [TX RECYCLER STATS] pending={}, submitted={}, confirmed={}, recycled={}",
                    pending, submitted, confirmed, recycled
                );
            }
            last_stats_log = Instant::now();
        }
    }
}
