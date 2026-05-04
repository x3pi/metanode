// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use consensus_core::{BlockAPI, CommittedSubDag};
use std::sync::Arc;
use tokio::time::Duration;
use tracing::{error, info, trace, warn};

/// T2-5: Bounded semaphore for deferred TX tracking and persistence tasks.
/// Prevents unbounded tokio::spawn accumulation under extreme commit rates
/// (e.g., 10K+ commits/sec during epoch transitions or burst load).
/// 64 permits = practical upper bound; exceeding this drops the task with a warning.
static DEFERRED_TASK_SEMAPHORE: std::sync::LazyLock<Arc<tokio::sync::Semaphore>> =
    std::sync::LazyLock::new(|| Arc::new(tokio::sync::Semaphore::new(64)));

use crate::node::executor_client::ExecutorClient;

pub async fn dispatch_commit(
    subdag: &CommittedSubDag,
    global_exec_index: u64,
    epoch: u64,
    executor_client: Option<Arc<ExecutorClient>>,
    delivery_sender: Option<tokio::sync::mpsc::Sender<crate::node::block_delivery::ValidatedCommit>>,
) -> Result<u64> {
    let commit_index = subdag.commit_ref.index;
    let mut total_transactions = 0;

    for block in subdag.blocks.iter() {
        total_transactions += block.transactions().len();
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
    if total_transactions == 0 && !has_system_tx {
        tracing::trace!(
            "⏭️ [FAST-SKIP] Empty commit #{} (GEI expected={}) skipped — no transactions",
            commit_index, global_exec_index
        );
        return Ok(0); // GEI DOES NOT ADVANCE FOR EMPTY COMMITS! This ensures mathematical determinism based purely on transactions.
    }

    // LEADER ADDRESS: Pre-resolved by CommitProcessor and embedded in subdag.
    // Same immutability pattern as global_exec_index — set once, never recalculated.
    let leader_address = subdag.leader_address.clone();

    if total_transactions > 0 || has_system_tx {
        trace!(
                "🔷 [batch_id={}] [Global Index: {}] Executing commit #{} (epoch={}): {} blocks, {} txs, has_system_tx={}",
                batch_id, global_exec_index, commit_index, epoch, subdag.blocks.len(), total_transactions, has_system_tx
            );
    } else {
        // Still log empty commits but as trace/debug to avoid spam
        tracing::trace!(
                "⏭️ [batch_id={}] [TX FLOW] Forwarding empty commit to Go Master (for sequence sync): global_exec_index={}, commit_index={}",
                batch_id, global_exec_index, commit_index
            );
    }

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
        if go_current_gei >= global_exec_index && global_exec_index > 0 {
            let has_end_of_epoch = subdag.extract_end_of_epoch_transaction().is_some();
            if !has_end_of_epoch {
                trace!(
                    "⏭️ [GEI GUARD] Skipping commit #{}: Go GEI={} >= commit GEI={}.",
                    commit_index, go_current_gei, global_exec_index
                );
                // CRITICAL FORK-SAFETY v5: Do NOT return Ok(1) blindly!
                // If a commit had 50001 TXs, it consumed 2 GEIs. If we skip it and return 1, 
                // this node will permanently lose 1 GEI from its cumulative_fragment_offset!
                let expected_fragments = if total_transactions > crate::node::executor_client::block_sending::MAX_TXS_PER_GO_BLOCK {
                    total_transactions.div_ceil(crate::node::executor_client::block_sending::MAX_TXS_PER_GO_BLOCK) as u64
                } else {
                    1
                };
                return Ok(expected_fragments);
            } else {
                info!(
                    "⚠️ [GEI GUARD] Go GEI={} >= commit GEI={}, but commit #{} \
                         contains EndOfEpoch — processing for epoch transition safety.",
                    go_current_gei, global_exec_index, commit_index
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

                    // Fix 4 Revert: Use direct indefinite wait (no 90s timeout) to enforce backpressure
                    let geis_consumed = match response_rx.await {
                        Ok(c) => c,
                        Err(_) => {
                            error!("🚨 [FATAL] DeliveryManager closed response channel without replying.");
                            anyhow::bail!("DeliveryManager response channel closed.");
                        }
                    };

                    trace!("✅ [batch_id={}] [TX FLOW] Successfully sent committed subdag: global_exec_index={}, commit_index={}, geis_consumed={}",
                                batch_id, global_exec_index, commit_index, geis_consumed);

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
                    // FIX C1: When try_lock() fails (transition handler holds node lock),
                    // spawn a deferred task to retry tracking after a short backoff.
                    // Previously, we simply skipped tracking, which could cause tx_recycler
                    // to resubmit already-committed TXs in the new epoch.
                    if let Some(node_arc) = crate::node::get_transition_handler_node().await {
                        // Use try_lock() instead of lock().await to avoid blocking
                        let node_guard = match node_arc.try_lock() {
                            Ok(guard) => guard,
                            Err(_) => {
                                // Lock held by transition handler — defer tracking to a background task
                                trace!("⏭️ [TX TRACKING] Deferring tracking for commit {} — node lock held by transition handler", commit_index);
                                let node_arc_clone = node_arc.clone();
                                let subdag_blocks: Vec<Vec<Vec<u8>>> = subdag
                                    .blocks
                                    .iter()
                                    .map(|b| {
                                        b.transactions()
                                            .iter()
                                            .map(|tx| tx.data().to_vec())
                                            .collect()
                                    })
                                    .collect();
                                let deferred_commit_index = commit_index;
                                // T2-5: Bounded deferred task — acquire semaphore permit before spawning
                                let sem = DEFERRED_TASK_SEMAPHORE.clone();
                                match sem.try_acquire_owned() {
                                    Ok(permit) => {
                                        tokio::spawn(async move {
                                            let _permit = permit; // held until task completes
                                                                  // Wait for transition handler to release lock
                                            tokio::time::sleep(Duration::from_millis(500)).await;
                                            if let Ok(guard) = node_arc_clone.try_lock() {
                                                let mut hashes_guard =
                                                    guard.committed_transaction_hashes.lock().await;
                                                let mut count = 0;
                                                for block_txs in &subdag_blocks {
                                                    for tx_data in block_txs {
                                                        let tx_hash = crate::types::tx_hash::calculate_transaction_hash_single(tx_data);
                                                        hashes_guard.insert(tx_hash);
                                                        count += 1;
                                                    }
                                                }
                                                if count > 0 {
                                                    info!("💾 [TX TRACKING DEFERRED] Successfully tracked {} hashes for commit #{} after backoff", count, deferred_commit_index);
                                                }
                                            } else {
                                                warn!("⚠️ [TX TRACKING DEFERRED] Still cannot acquire lock for commit #{}. TX tracking skipped.", deferred_commit_index);
                                            }
                                        });
                                    }
                                    Err(_) => {
                                        warn!("⚠️ [TX TRACKING DEFERRED] Semaphore full (64 tasks in-flight). Dropping deferred tracking for commit #{}.", deferred_commit_index);
                                    }
                                }
                                return Ok(geis_consumed);
                            }
                        };
                        let mut hashes_guard = node_guard.committed_transaction_hashes.lock().await;

                        let mut tracked_count = 0;
                        let mut batch_hashes = Vec::new();
                        for block in &subdag.blocks {
                            for tx in block.transactions() {
                                let tx_hash =
                                    crate::types::tx_hash::calculate_transaction_hash_single(
                                        tx.data(),
                                    );
                                hashes_guard.insert(tx_hash.clone());
                                batch_hashes.push(tx_hash);
                                tracked_count += 1;
                            }
                        }

                        // TPS OPT: Defer disk persist to background — TX hashes are only used for
                        // epoch transition recovery, not state computation. Async persist is fork-safe.
                        if !batch_hashes.is_empty() {
                            let storage_path = node_guard.storage_path.clone();
                            let hashes_count = batch_hashes.len();
                            let persist_epoch = epoch;
                            // T2-5: Bounded persistence task — acquire semaphore permit
                            let sem = DEFERRED_TASK_SEMAPHORE.clone();
                            match sem.try_acquire_owned() {
                                Ok(permit) => {
                                    tokio::spawn(async move {
                                        let _permit = permit; // held until task completes
                                        if let Err(e) = crate::node::transition::save_committed_transaction_hashes_batch(
                                                    &storage_path, persist_epoch, &batch_hashes
                                                ).await {
                                                warn!("⚠️ [TX TRACKING] Failed to persist committed hashes after commit: {}", e);
                                            } else {
                                                trace!("💾 [TX TRACKING] Persisted {} committed hashes for epoch {}", hashes_count, persist_epoch);
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
