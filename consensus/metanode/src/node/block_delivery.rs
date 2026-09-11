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

            let geis_consumed = self.deliver_with_halt_retry(&msg).await;

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
        info!("🛑 [STATION 4: DELIVERY] BlockDeliveryManager closed (channel dropped).");
    }

    /// Delivers one commit to the Go executor, retrying with backoff on the SAME commit
    /// instead of ever moving on, if `send_committed_subdag` returns an Err.
    ///
    /// FORK-SAFETY (2026-09-11): this used to `panic!()` on any Err, which -- confirmed live
    /// on both a real teammate cluster and this machine's own test cluster -- silently and
    /// PERMANENTLY killed this whole detached `tokio::spawn`'d task with nothing watching it:
    /// the outer FFI resilience loop (`08780279`, ffi.rs) only catches panics on the main
    /// consensus init/run call stack, not inside a task spawned off to the side like this one.
    /// The node kept running (RPC still answering, systemd never saw a crash) while block
    /// production silently stalled forever -- exactly the "Go kept running... zero further
    /// restart attempts" symptom `08780279` itself was written to fix, just reached through a
    /// different door.
    ///
    /// The Err case this hits in practice: `send_committed_subdag` (via
    /// `build_sorted_transactions`) refuses to build a block that's missing a transaction's
    /// payload, and the peer-recovery attempt (`ensure_tx_payloads_cached`) already tried and
    /// failed -- confirmed live (2026-09-11) via TX-PAYLOAD-RECOVERY-DIAG logging that this
    /// legitimately happens when every node in the cluster loses the same in-memory
    /// TxPayloadCache entry at once (e.g. a routine full-cluster code deploy that restarts
    /// every node together -- see coordination_hub.rs's TxFetcherFn doc comment, which already
    /// documents this exact scenario as "unrecoverable by design").
    ///
    /// Skipping the commit is not an option (would silently drop a real transaction and fork
    /// this node from the network -- the Zero-Fork Invariant). So this halts dispatch on this
    /// one commit exactly like Phuong an A halts dispatch on a suspected divergence
    /// (processor.rs) -- loud, greppable, distinctly-named alert marker (a different one:
    /// this is a CONFIRMED permanent loss, not a suspected fork, so the operator runbook is
    /// different) -- and retries forever with backoff, so it can still self-heal if a peer
    /// that was itself temporarily down comes back with the payload, or if new commits
    /// eventually queue up behind this being retried later once an operator resolves it,
    /// rather than closing that door immediately via panic.
    async fn deliver_with_halt_retry(&self, msg: &ValidatedCommit) -> u64 {
        let commit_index = msg.subdag.commit_ref.index;
        let mut attempt: u32 = 0;
        loop {
            let start_delivery = std::time::Instant::now();
            let result = self
                .executor_client
                .send_committed_subdag(
                    &msg.subdag,
                    msg.epoch,
                    msg.global_exec_index,
                    msg.leader_address.clone(),
                )
                .await;
            let elapsed = start_delivery.elapsed();
            self.metrics.go_send_per_commit_seconds.observe(elapsed.as_secs_f64());

            match result {
                Ok(geis_consumed) => {
                    self.metrics.blocks_sent_to_go_total.inc();
                    if elapsed.as_millis() > 50 {
                        tracing::warn!(
                            "🐌 [PERF-WARN] send_committed_subdag took {:?} for commit {} (GEI={}). Go Execution is lagging!",
                            elapsed, commit_index, msg.global_exec_index
                        );
                    }
                    if attempt > 0 {
                        error!(
                            "✅ [CONSENSUS-HALT-TX-PAYLOAD-LOST-RECOVERED] Commit {} (GEI={}) delivered \
                             successfully after {} failed attempt(s) -- resuming normal dispatch.",
                            commit_index, msg.global_exec_index, attempt
                        );
                    }
                    return geis_consumed;
                }
                Err(e) => {
                    attempt += 1;
                    // Re-alert on the 1st attempt, then every ~2min (12 * 10s), so a live tail
                    // of the last 200KB (start_monitors.sh's halt-check window) always has a
                    // recent occurrence without spamming the log every 10s forever.
                    if attempt == 1 || attempt % 12 == 0 {
                        error!(
                            "🛑🚨 [CONSENSUS-HALT-TX-PAYLOAD-LOST] Failed to deliver commit {} (GEI={}) \
                             to the executor after {} attempt(s): {}. Peer recovery already attempted \
                             and failed -- HALTING dispatch on this commit rather than risk a fork by \
                             skipping it (Zero-Fork Invariant), per Phuong an A's halt-rather-than-guess \
                             precedent. This node keeps running (RPC/reads still answer) and retries \
                             every 10s in case a peer regains the payload, but will NOT make further \
                             progress past this commit without one. THIS NEEDS AN OPERATOR: see \
                             note/consensus_local_dag_trust_gap_design_2026-09.md mục 10.",
                            commit_index, msg.global_exec_index, attempt, e
                        );
                    }
                    tokio::time::sleep(std::time::Duration::from_secs(10)).await;
                }
            }
        }
    }
}
