// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Block Delivery Manager
//!
//! Centralized block delivery manager that handles sending commits to the Go Master
//! execution engine via an MPSC channel, decoupling the consensus processing thread
//! from execution engine backpressure and IO overhead.

use crate::node::executor_client::ExecutorClient;
use consensus_core::CommittedSubDag;
use std::sync::Arc;
use tokio::sync::mpsc;
use tracing::{debug, error, info};

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
