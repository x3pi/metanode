// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Block Delivery Manager
//!
//! Centralized block delivery manager that handles sending commits to the Go Master
//! execution engine via an MPSC channel, decoupling the consensus processing thread
//! from execution engine backpressure and IO overhead.

use crate::node::executor_client::ExecutorClient;
use consensus_core::{BlockAPI, CommittedSubDag};
use std::sync::Arc;
use tokio::sync::mpsc;
use tracing::{debug, error, info};

// TEMPORARY DIAGNOSTIC (2026-09-03): counts commits actually reaching this
// "STATION 4" delivery loop and calling send_committed_subdag, split by
// outcome. Fourth counter set in the same investigation (see executor.rs's
// DIAG_* for the third, GEI GUARD / FAST-SKIP -- both still pending
// results as of this addition). eprintln! bypasses tracing for the same
// reason as the others. Remove once settled.
static DIAG_DELIVERY_OK: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static DIAG_DELIVERY_OK_TXS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static DIAG_DELIVERY_LAST_PRINT_SECS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

fn diag_delivery_maybe_print() {
    use std::sync::atomic::Ordering;
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let last = DIAG_DELIVERY_LAST_PRINT_SECS.load(Ordering::Relaxed);
    if now >= last + 2
        && DIAG_DELIVERY_LAST_PRINT_SECS
            .compare_exchange(last, now, Ordering::Relaxed, Ordering::Relaxed)
            .is_ok()
    {
        eprintln!(
            "[DIAG block_delivery] ok_commits={} ok_txs={}",
            DIAG_DELIVERY_OK.load(Ordering::Relaxed),
            DIAG_DELIVERY_OK_TXS.load(Ordering::Relaxed)
        );
    }
}

/// A sanitized commit that has been verified, has the proper GEI assigned,
/// and has the leader address resolved.
pub struct ValidatedCommit {
    pub subdag: CommittedSubDag,
    pub global_exec_index: u64,
    pub epoch: u64,
    pub leader_address: Vec<u8>,
    /// Channel for the Delivery Manager to reply with the number of GEIs consumed
    /// by this commit (e.g. fragmentation offset).
    pub response_tx: tokio::sync::oneshot::Sender<u64>,
}

pub struct BlockDeliveryManager {
    executor_client: Arc<ExecutorClient>,
    receiver: mpsc::Receiver<ValidatedCommit>,
    metrics: Arc<crate::node::sync_metrics::SyncMetrics>,
}

impl BlockDeliveryManager {
    pub fn new(
        executor_client: Arc<ExecutorClient>,
        receiver: mpsc::Receiver<ValidatedCommit>,
        _peer_addrs: Vec<String>,
        metrics: Arc<crate::node::sync_metrics::SyncMetrics>,
    ) -> Self {
        Self {
            executor_client,
            receiver,
            metrics,
        }
    }

    pub async fn run(mut self) {
        info!("🚚 [STATION 4: DELIVERY] Started BlockDeliveryManager loop. Conveyor belt active.");
        while let Some(msg) = self.receiver.recv().await {
            let commit_index = msg.subdag.commit_ref.index;

            let start_delivery = std::time::Instant::now();
            let result = self
                .executor_client
                .send_committed_subdag(
                    &msg.subdag,
                    msg.epoch,
                    msg.global_exec_index,
                    msg.leader_address,
                )
                .await;
            let elapsed = start_delivery.elapsed();
            
            // Record Prometheus metrics
            self.metrics.go_send_per_commit_seconds.observe(elapsed.as_secs_f64());
            self.metrics.blocks_sent_to_go_total.inc();

            if elapsed.as_millis() > 50 {
                tracing::warn!(
                    "🐌 [PERF-WARN] send_committed_subdag took {:?} for commit {} (GEI={}). Go Execution is lagging!",
                    elapsed,
                    commit_index,
                    msg.global_exec_index
                );
            }

            match result {
                Ok(geis_consumed) => {
                    let tx_count: usize = msg.subdag.blocks.iter().map(|b| {
                        let d = b.tx_digests();
                        if !d.is_empty() { d.len() } else { b.transactions().len() }
                    }).sum();
                    DIAG_DELIVERY_OK.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    DIAG_DELIVERY_OK_TXS.fetch_add(tx_count as u64, std::sync::atomic::Ordering::Relaxed);
                    diag_delivery_maybe_print();
                    debug!(
                        "✅ [TX-FLOW-TRACE] ▶ PHASE 3.3→3.4 DONE: send_committed_subdag completed | \
                         commit_index={}, gei={}, geis_consumed={}",
                        commit_index, msg.global_exec_index, geis_consumed
                    );
                    if let Err(_) = msg.response_tx.send(geis_consumed) {
                        error!("🚨 [STATION 4: DELIVERY] Processor dropped response channel for commit {} before reply could be sent.", commit_index);
                    }
                }
                Err(e) => {
                    error!(
                        "🚨 [STATION 4: FATAL ERROR] Failed to send commit {} (GEI={}) to Executor: {}",
                        commit_index, msg.global_exec_index, e
                    );
                    panic!(
                        "Execution failure during block delivery. Cannot recover. Error: {}",
                        e
                    );
                }
            }
        }
        info!("🛑 [STATION 4: DELIVERY] BlockDeliveryManager closed (channel dropped).");
    }
}
