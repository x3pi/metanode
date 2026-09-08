// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use consensus_core::{BlockAPI, CommittedSubDag};
use std::sync::Arc;

use tracing::{debug, error, info, trace, warn};

/// T2-5: Bounded semaphore for deferred TX tracking and persistence tasks.
/// Prevents unbounded tokio::spawn accumulation under extreme commit rates
/// (e.g., 10K+ commits/sec during epoch transitions or burst load).
/// 64 permits = practical upper bound; exceeding this drops the task with a warning.
static DEFERRED_TASK_SEMAPHORE: std::sync::LazyLock<Arc<tokio::sync::Semaphore>> =
    std::sync::LazyLock::new(|| Arc::new(tokio::sync::Semaphore::new(64)));

static LAST_FORCE_COMMIT: std::sync::LazyLock<std::sync::atomic::AtomicU64> =
    std::sync::LazyLock::new(|| std::sync::atomic::AtomicU64::new(0));

// TEMPORARY DIAGNOSTIC (2026-09-03): counts commits skipped by the GEI GUARD
// below (Rust believes Go's already-reported GEI is at or past this commit's
// end, so it never dispatches it -- returning Ok() as if it had) and, of
// those, how many actually carried real transactions. Same investigation as
// tx_socket_server.rs's DIAG_* and commit_processor/processor.rs's
// DIAG_DIGEST_* counters (both ruled out their respective hypotheses); this
// is the third lead. eprintln! bypasses tracing entirely for the same
// reason as those two. Remove once settled.
static DIAG_GEI_GUARD_SKIPS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static DIAG_GEI_GUARD_SKIPPED_TXS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static DIAG_FAST_SKIP_EMPTY: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static DIAG_DISPATCHED_TXS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static DIAG_EXEC_LAST_PRINT_SECS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

fn diag_exec_maybe_print() {
    use std::sync::atomic::Ordering;
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let last = DIAG_EXEC_LAST_PRINT_SECS.load(Ordering::Relaxed);
    if now >= last + 2
        && DIAG_EXEC_LAST_PRINT_SECS
            .compare_exchange(last, now, Ordering::Relaxed, Ordering::Relaxed)
            .is_ok()
    {
        eprintln!(
            "[DIAG dispatch_commit] dispatched_txs={} gei_guard_skips={} gei_guard_skipped_txs={} fast_skip_empty={}",
            DIAG_DISPATCHED_TXS.load(Ordering::Relaxed),
            DIAG_GEI_GUARD_SKIPS.load(Ordering::Relaxed),
            DIAG_GEI_GUARD_SKIPPED_TXS.load(Ordering::Relaxed),
            DIAG_FAST_SKIP_EMPTY.load(Ordering::Relaxed)
        );
    }
}

use crate::node::executor_client::ExecutorClient;

/// Extracts the raw byte data of every real (non-system, non-empty-payload)
/// transaction actually contained in a committed sub-DAG. Shared by every
/// caller of `TxRecycler::confirm_committed` (this file's own call below,
/// plus four in `commit_processor/processor.rs`).
///
/// BUG FIX (2026-09-03): every one of those 5 call sites previously
/// extracted transactions via `block.transactions()` alone. `BlockV3`
/// (compact block) stores only `tx_digests()` and unconditionally returns
/// `&[]` from `transactions()` -- the exact same class of bug already
/// found and fixed for this function's own FAST-SKIP counting a few dozen
/// lines below (see that comment for the full BlockV3 explanation), and
/// for the real dispatch path in `build_sorted_transactions`
/// (block_sending.rs). So for any commit made of compact blocks, every
/// `confirm_committed` call site saw zero (or an incomplete list of)
/// transactions and skipped or under-reported confirmation, even though
/// those transactions were genuinely committed and correctly dispatched to
/// Go via this same function's separate, already-digest-aware path a few
/// lines above.
///
/// Consequence: TxRecycler's `pending` map never learned these
/// already-successful transactions had confirmed, leaving them stuck until
/// RECYCLE_TIMEOUT elapsed -- at which point `collect_stale()` resubmitted
/// an already-committed transaction with an already-used nonce. This is
/// exactly the "stale TX re-submission ... nonce conflicts ... chain
/// stall" failure mode the comment on this file's own confirm_committed
/// call site already describes as a past, supposedly-fixed incident: it
/// recurred because that fix used the wrong (digest-blind) extraction
/// method, not because confirm_committed was never called at all.
pub(crate) fn extract_committed_tx_data(subdag: &CommittedSubDag) -> Vec<Vec<u8>> {
    let cache = consensus_core::get_global_tx_cache().read();
    let mut out = Vec::new();
    for block in &subdag.blocks {
        let tx_digests = block.tx_digests();
        if !tx_digests.is_empty() {
            for digest in &tx_digests {
                if let Some(tx) = cache.get(digest) {
                    let data = tx.data();
                    if data.len() == 64 && data.iter().all(|&b| b == 0) {
                        continue;
                    }
                    out.push(data.to_vec());
                }
                // Not yet in cache: nothing to confirm with here.
                // build_sorted_transactions() will warn/skip it at actual
                // send time if it's truly missing -- this function is
                // best-effort recycler bookkeeping, not the dispatch path.
            }
        } else {
            for tx in block.transactions() {
                let data = tx.data();
                if data.len() == 64 && data.iter().all(|&b| b == 0) {
                    continue;
                }
                out.push(data.to_vec());
            }
        }
    }
    out
}

/// Whether a commit's transactions are considered "empty" for GEI-consumption purposes, i.e.
/// whether it fast-skips (consumes 0 GEIs) instead of consuming 1+ GEIs.
///
/// EXEMPTION (2026-04-something, FAST PATH comment below): `commit_index == 1` (an epoch's very
/// first commit) is NEVER fast-skipped, even with zero transactions and no system tx -- it
/// always consumes exactly 1 GEI. This function is the SINGLE source of truth for that rule,
/// specifically so `dispatch_commit()` (the live execution path, below) and
/// `recovery.rs::perform_block_recovery_check()` (the replay-from-storage path, used when
/// reconstructing GEI history for a node that's catching up) can never silently diverge on it
/// again. They already had -- found live 2026-09-05: recovery.rs was missing this
/// `commit_index > 1` exemption entirely, so every epoch whose first commit happened to be
/// empty made its replay under-count GEI by exactly 1 relative to what live execution actually
/// assigned. Accumulated over many epochs (common on a quiet/idle devnet), this became a real,
/// stable GlobalExecIndex/CommitIndex divergence between a node that replayed its own history
/// through that buggy path and one that didn't -- which fork_guard.rs's LAYER-6 correctly (if
/// confusingly, since AccountStatesRoot still matched -- the actual executed state was never
/// wrong) flagged as a "CONFIRMED FORK".
pub(crate) fn commit_is_empty_for_gei(
    total_transactions: usize,
    has_system_tx: bool,
    commit_index: u32,
) -> bool {
    total_transactions == 0 && !has_system_tx && commit_index > 1
}

pub async fn dispatch_commit(
    subdag: &CommittedSubDag,
    global_exec_index: u64,
    epoch: u64,
    executor_client: Option<Arc<ExecutorClient>>,
    delivery_sender: Option<tokio::sync::mpsc::Sender<crate::node::block_delivery::ValidatedCommit>>,
    tx_recycler: Option<Arc<crate::consensus::tx_recycler::TxRecycler>>,
    committed_transaction_hashes: Option<Arc<dashmap::DashSet<Vec<u8>>>>,
    storage_path: Option<std::path::PathBuf>,
) -> Result<u64> {
    let commit_index = subdag.commit_ref.index;
    let mut total_transactions = 0;

    // BUG FIX: BlockV3 (compact block) stores only tx_digests() — its transactions()
    // unconditionally returns &[] (see BlockAPI impl for BlockV3 in block.rs). Counting
    // via transactions() alone therefore misclassifies every BlockV3 commit as empty,
    // which triggers the FAST-SKIP branch below (`return Ok(0)`) and silently drops the
    // commit's real, already-quorum-committed transactions without ever delivering them
    // to Go — GEI never advances for that commit. Mirror the same tx_digests()-first
    // lookup that build_sorted_transactions() (block_sending.rs) already uses on the
    // actual send path, so the count here matches what will really be sent.
    {
        let cache = consensus_core::get_global_tx_cache().read();
        for block in subdag.blocks.iter() {
            let tx_digests = block.tx_digests();
            if !tx_digests.is_empty() {
                for digest in &tx_digests {
                    match cache.get(digest) {
                        Some(tx) => {
                            let tx_data = tx.data();
                            // Skip 64-byte zero payloads (SystemTransaction artifacts at epoch boundaries)
                            if tx_data.len() == 64 && tx_data.iter().all(|&b| b == 0) {
                                continue;
                            }
                            total_transactions += 1;
                        }
                        None => {
                            // Not in cache yet — still count it as real so this commit
                            // isn't wrongly fast-skipped. build_sorted_transactions()
                            // will warn/skip it at actual send time if truly missing.
                            total_transactions += 1;
                        }
                    }
                }
            } else {
                for tx in block.transactions().iter() {
                    let tx_data = tx.data();
                    // Skip 64-byte zero payloads (SystemTransaction artifacts at epoch boundaries)
                    if tx_data.len() == 64 && tx_data.iter().all(|&b| b == 0) {
                        continue;
                    }
                    total_transactions += 1;
                }
            }
        }
    }

    let has_system_tx = subdag.extract_end_of_epoch_transaction().is_some();

    // CC-1: Unified batch_id for end-to-end tracing
    let batch_id = format!("E{}C{}G{}", epoch, commit_index, global_exec_index);

    // ═══════════════════════════════════════════════════════════════════
    // FAST PATH: Skip empty commits entirely during catch-up.
    //
    // Empty DAG rounds (no transactions, no system TX) make up 90%+ of
    // commits during catch-up. Each one was going through:
    //   1. Leader resolution (RwLock + HashMap + retries) → ~ms
    //   2. Protobuf encode → ~μs
    //   3. BlockDeliveryManager channel (oneshot await) → ~μs
    //   4. Buffer + FFI call to Go CGo → ~ms
    //   5. TX tracking + ForceCommit → ~μs
    //
    // With 4000+ empty commits, this adds ~4-8 seconds of unnecessary
    // latency during catch-up. Go doesn't create blocks for empty commits
    // anyway (block_number=0), so we can skip the entire pipeline.
    //
    // We still update:
    //   - shared_last_global_exec_index → for GEI tracking
    //   - executor_client.next_expected_index → to prevent gap detection
    // ═══════════════════════════════════════════════════════════════════
    if commit_is_empty_for_gei(total_transactions, has_system_tx, commit_index) {
        DIAG_FAST_SKIP_EMPTY.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        diag_exec_maybe_print();
        tracing::trace!(
            "⏭️ [FAST-SKIP] Empty commit #{} (GEI expected={}) skipped — no transactions",
            commit_index, global_exec_index
        );
        return Ok(0); // GEI DOES NOT ADVANCE FOR EMPTY COMMITS! This ensures mathematical determinism based purely on transactions.
    }

    // LEADER ADDRESS: Pre-resolved by CommitProcessor and embedded in subdag.
    // Same immutability pattern as global_exec_index — set once, never recalculated.
    let leader_address = subdag.leader_address.clone();


    // ═══════════════════════════════════════════════════════════════════
    // GEI GUARD: Skip commits that Go has already executed.
    //
    // Since Phase 1 (sync_and_execute_blocks), Go's GEI is ALWAYS accurate
    // (reflects actually-executed state, never inflated). This single path
    // handles all deduplication correctly.
    //
    // EndOfEpoch commits always pass through for epoch transition safety.
    // ═══════════════════════════════════════════════════════════════════
    if let Some(ref client) = executor_client {
        // CRITICAL FIX (2026-04-26): Only use Go's ACTUAL GEI for dedup, not the
        // inflated shared_last_global_exec_index.
        //
        // BUG: After cold-start sync, shared_last_global_exec_index is set to the
        // network tip (~2361) but new epoch commits start with GEI=1. The old code
        // used shared_last_global_exec_index as the fast-path filter between Go RPC
        // checks, silently skipping ALL new-epoch commits (GEI < 2361).
        //
        // FIX: Use 0 as fallback between Go RPC checks. Real deduplication is
        // handled by send_committed_subdag's REPLAY PROTECTION (next_expected_index).
        let go_current_gei = if commit_index % 200 == 0 {
            client.get_last_global_exec_index().await.unwrap_or(0)
        } else {
            0 // Don't filter between Go RPC checks — let REPLAY PROTECTION handle it
        };

        let expected_fragments = if total_transactions > crate::node::executor_client::block_sending::MAX_TXS_PER_GO_BLOCK {
            total_transactions.div_ceil(crate::node::executor_client::block_sending::MAX_TXS_PER_GO_BLOCK) as u64
        } else {
            1
        };

        if go_current_gei > 0 && global_exec_index > 0 && go_current_gei >= (global_exec_index + expected_fragments - 1) {
            let has_end_of_epoch = subdag.extract_end_of_epoch_transaction().is_some();
            if !has_end_of_epoch {
                DIAG_GEI_GUARD_SKIPS.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                DIAG_GEI_GUARD_SKIPPED_TXS.fetch_add(total_transactions as u64, std::sync::atomic::Ordering::Relaxed);
                diag_exec_maybe_print();
                trace!(
                    "⏭️ [GEI GUARD] Skipping commit #{}: Go GEI={} >= commit end GEI={}.",
                    commit_index, go_current_gei, global_exec_index + expected_fragments - 1
                );
                return Ok(expected_fragments);
            } else {
                info!(
                    "⚠️ [GEI GUARD] Go GEI={} >= commit end GEI={}, but commit #{} \
                         contains EndOfEpoch — processing for epoch transition safety.",
                    go_current_gei, global_exec_index + expected_fragments - 1, commit_index
                );
            }
        }

        if let Some(ref sender) = delivery_sender {
                    let (response_tx, response_rx) = tokio::sync::oneshot::channel();
                    let validated = crate::node::block_delivery::ValidatedCommit {
                        subdag: subdag.clone(),
                        global_exec_index,
                        epoch,
                        leader_address: leader_address.clone(),
                        response_tx,
                    };

                    if let Err(e) = sender.send(validated).await {
                        error!("🚨 [FATAL] Failed to send commit to DeliveryManager: {}", e);
                        anyhow::bail!("DeliveryManager channel closed.");
                    }
                    DIAG_DISPATCHED_TXS.fetch_add(total_transactions as u64, std::sync::atomic::Ordering::Relaxed);

                    // PIPELINE FIX: We return expected_fragments immediately to unblock CommitProcessor.
                    // This eliminates the IPC serialization bottleneck. Backpressure is now handled
                    // natively by the bounded capacity of `delivery_sender` (100 commits).
                    // With FIX-1A (honest FFI wait), BlockDeliveryManager blocks per block,
                    // so this buffer fills when Go is ~100 blocks behind → natural throttle.
                    let geis_consumed = expected_fragments;

                    tokio::spawn(async move {
                        if let Err(_) = response_rx.await {
                            error!("🚨 [FATAL] DeliveryManager closed response channel without replying.");
                        }
                    });

                    debug!(
                        "📤 [TX-FLOW-TRACE] ▶ PHASE 3.2→3.3: Commit sent to BlockDeliveryManager | \
                         batch_id={}, commit_index={}, gei={}, txs={}, fragments={}, \
                         leader_addr_len={}, has_system_tx={}",
                        batch_id, commit_index, global_exec_index, total_transactions,
                        geis_consumed, leader_address.len(), has_system_tx
                    );

                    // CommitProcessor handles updating shared_last_global_exec_index using the returned geis_consumed.

                    // Track lag every 500 commits (reduced from 100 to minimize Go RPC during sync)
                    if commit_index % 500 == 0 {
                        if let Ok(go_gei) = client.get_last_global_exec_index().await {
                            let lag = global_exec_index.saturating_sub(go_gei);
                            if lag > 500 {
                                tracing::warn!(
                                    "⚠️ [EXEC-LAG] Rust GEI={} vs Go GEI={} — gap={} blocks",
                                    global_exec_index,
                                    go_gei,
                                    lag
                                );
                            }
                        }
                    }

                    // Track committed transaction hashes to prevent duplicates during epoch transitions
                    // CRITICAL: Only track when commit is actually processed, not just submitted
                    //
                    // We now pass committed_transaction_hashes and tx_recycler directly to avoid acquiring
                    // the Node lock, which under high TPS is heavily contended (e.g. by UdsServer), causing
                    // tracking to fail and dropping transactions into a re-submission loop.
                    if let Some(hashes_arc) = &committed_transaction_hashes {

                        let mut tracked_count = 0;
                        let mut batch_hashes = Vec::new();
                        // Collect committed TX data for TxRecycler confirmation. Uses the
                        // digest-aware extractor (see its doc comment) instead of a bare
                        // block.transactions() loop, which silently sees zero transactions
                        // for BlockV3 (compact) blocks.
                        let committed_tx_data = extract_committed_tx_data(subdag);
                        for tx_data in &committed_tx_data {
                            let tx_hash =
                                crate::types::tx_hash::calculate_transaction_hash_single(
                                    tx_data,
                                );
                            hashes_arc.insert(tx_hash.clone());
                            batch_hashes.push(tx_hash);
                            tracked_count += 1;
                        }

                        // STABILITY FIX: Confirm committed TXs in TxRecycler.
                        // Previously, TxRecycler.confirm_committed() was NEVER called from the
                        // commit path. This caused pending TXs to accumulate indefinitely (up to
                        // MAX_PENDING_TXS=100K), triggering stale TX re-submission every 15s
                        // via collect_stale() → nonce conflicts → chain stall.
                        if !committed_tx_data.is_empty() {
                            if let Some(ref recycler) = tx_recycler {
                                recycler.confirm_committed(&committed_tx_data).await;
                            }
                        }

                        // TPS OPT: Defer disk persist to background — TX hashes are only used for
                        // epoch transition recovery, not state computation. Async persist is fork-safe.
                        if !batch_hashes.is_empty() {
                            let storage_path_clone = storage_path.clone();
                            let hashes_count = batch_hashes.len();
                            let persist_epoch = epoch;
                            // T2-5: Bounded persistence task — acquire semaphore permit
                            let sem = DEFERRED_TASK_SEMAPHORE.clone();
                            match sem.try_acquire_owned() {
                                Ok(permit) => {
                                    tokio::spawn(async move {
                                        let _permit = permit; // held until task completes
                                        if let Some(ref p) = storage_path_clone {
                                            if let Err(e) = crate::node::transition::save_committed_transaction_hashes_batch(
                                                        p, persist_epoch, &batch_hashes
                                                    ).await {
                                                    warn!("⚠️ [TX TRACKING] Failed to persist committed hashes after commit: {}", e);
                                                } else {
                                                    trace!("💾 [TX TRACKING] Persisted {} committed hashes for epoch {}", hashes_count, persist_epoch);
                                                }
                                        }
                                    });
                                }
                                Err(_) => {
                                    warn!("⚠️ [TX TRACKING] Semaphore full (64 tasks). Skipping async persist for {} hashes (epoch {}). Will re-persist on next commit.", hashes_count, persist_epoch);
                                }
                            }
                        }

                        if tracked_count > 0 {
                            trace!("💾 [TX TRACKING] Tracked {} committed transaction hashes after processing commit #{} (global_exec_index={})",
                                          tracked_count, commit_index, global_exec_index);
                        }
                    }

                    // NEW: Send ForceCommit request to Go via isolated deferred task
                    // This triggers Event-Driven Block Generation in the Go execution engine
                    // RATE-LIMIT: Only trigger ForceCommit on EndOfEpoch OR once every 20ms
                    // This prevents TCP socket saturation under high TPS.
                    let now_ms = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap_or_default().as_millis() as u64;
                    let last_ms = LAST_FORCE_COMMIT.load(std::sync::atomic::Ordering::Relaxed);
                    let should_force_commit = has_system_tx || (now_ms.saturating_sub(last_ms) >= 20);

                    if should_force_commit {
                        LAST_FORCE_COMMIT.store(now_ms, std::sync::atomic::Ordering::Relaxed);
                        let client_clone = client.clone();
                        let reason = format!("commit_g{}_e{}", global_exec_index, epoch);
                        let sem = DEFERRED_TASK_SEMAPHORE.clone();
                        match sem.try_acquire_owned() {
                            Ok(permit) => {
                                tokio::spawn(async move {
                                    let _permit = permit;
                                    if let Err(e) = client_clone.send_force_commit(reason).await {
                                        trace!("📝 [FORCE COMMIT] Failed to send ForceCommit (non-critical): {}", e);
                                    }
                                });
                            }
                            Err(_) => {
                                trace!("📝 [FORCE COMMIT] Semaphore full (64 tasks), skipping force commit trigger");
                            }
                        }
                    } else {
                        trace!("📝 [FORCE COMMIT] Rate-limited force commit trigger for commit_g{}_e{}", global_exec_index, epoch);
                    }

                    return Ok(geis_consumed);
                } else {
                    tracing::error!("🚨 [FATAL] delivery_sender is None in dispatch_commit. Cannot process commit.");
                    anyhow::bail!("delivery_sender missing.");
                }
    } else {
        info!("ℹ️  [TX FLOW] Executor client not enabled, skipping send");
    }

    Ok(1)
}

#[cfg(test)]
mod commit_is_empty_for_gei_tests {
    use super::commit_is_empty_for_gei;

    // Regression test for the 2026-09-05 GEI-drift bug: recovery.rs::perform_block_recovery_check
    // used to inline its own copy of this exact decision without the `commit_index > 1`
    // exemption, silently under-counting GEI by 1 for every epoch whose first commit was empty.
    // Pins down the real rule dispatch_commit() relies on so the two call sites can never
    // silently re-diverge on it again.

    #[test]
    fn epoch_first_commit_never_empty_even_with_zero_txs_and_no_system_tx() {
        assert!(!commit_is_empty_for_gei(0, false, 1));
    }

    #[test]
    fn later_commit_with_zero_txs_and_no_system_tx_is_empty() {
        assert!(commit_is_empty_for_gei(0, false, 2));
        assert!(commit_is_empty_for_gei(0, false, 12345));
    }

    #[test]
    fn any_commit_with_transactions_is_never_empty() {
        assert!(!commit_is_empty_for_gei(1, false, 1));
        assert!(!commit_is_empty_for_gei(1, false, 2));
        assert!(!commit_is_empty_for_gei(500, false, 2));
    }

    #[test]
    fn any_commit_with_a_system_tx_is_never_empty_regardless_of_commit_index() {
        assert!(!commit_is_empty_for_gei(0, true, 1));
        assert!(!commit_is_empty_for_gei(0, true, 2));
    }
}
