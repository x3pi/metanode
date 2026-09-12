// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::sync::Arc;

use tokio::sync::mpsc::{unbounded_channel, UnboundedReceiver, UnboundedSender};
use tokio::sync::watch;
use tracing::debug;

use crate::{block::CertifiedBlocksOutput, CommitIndex, CommittedSubDag};

/// Arguments from commit consumer to this consensus instance.
/// This includes both parameters and components for communications.
#[derive(Clone)]
pub struct CommitConsumerArgs {
    /// The consumer requests consensus to replay from commit replay_after_commit_index + 1.
    /// Set to 0 to replay from the start (as commit sequence starts at index = 1).
    pub(crate) replay_after_commit_index: CommitIndex,
    /// Index of the last commit that the consumer has processed.  This is useful during
    /// crash recovery when other components can wait for the consumer to finish processing
    /// up to this index.
    pub(crate) consumer_last_processed_commit_index: CommitIndex,
    /// The hash of the last executed commit from Go Master state, used to perform anti-fork check at startup.
    pub(crate) last_executed_commit_hash: [u8; 32],
    
    /// The timestamp of the last executed block from Go Master state, used for recovery.
    pub(crate) last_block_timestamp_ms: u64,

    /// Callback to align the last executed commit hash in Go state.
    pub(crate) align_executed_commit_hash: Option<Arc<dyn Fn([u8; 32]) + Send + Sync>>,

    /// A channel to output the committed sub dags.
    pub(crate) commit_sender: UnboundedSender<CommittedSubDag>,
    /// A channel to output blocks for processing, separated from consensus commits.
    /// In each block output, transactions that are not rejected are considered certified.
    pub(crate) block_sender: UnboundedSender<CertifiedBlocksOutput>,
    // Allows the commit consumer to report its progress.
    monitor: Arc<CommitConsumerMonitor>,
}

impl CommitConsumerArgs {
    pub fn new(
        replay_after_commit_index: CommitIndex,
        consumer_last_processed_commit_index: CommitIndex,
        last_executed_commit_hash: [u8; 32],
        last_block_timestamp_ms: u64,
    ) -> (
        Self,
        UnboundedReceiver<CommittedSubDag>,
        UnboundedReceiver<CertifiedBlocksOutput>,
    ) {
        let (commit_sender, commit_receiver) = unbounded_channel();
        let (block_sender, block_receiver) = unbounded_channel();

        let monitor = Arc::new(CommitConsumerMonitor::new(
            replay_after_commit_index,
            consumer_last_processed_commit_index,
        ));
        (
            Self {
                replay_after_commit_index,
                consumer_last_processed_commit_index,
                last_executed_commit_hash,
                last_block_timestamp_ms,
                align_executed_commit_hash: None,
                commit_sender,
                block_sender,
                monitor,
            },
            commit_receiver,
            block_receiver,
        )
    }

    pub fn with_align_executed_commit_hash<F>(mut self, cb: F) -> Self
    where
        F: Fn([u8; 32]) + Send + Sync + 'static,
    {
        self.align_executed_commit_hash = Some(Arc::new(cb));
        self
    }


    pub fn monitor(&self) -> Arc<CommitConsumerMonitor> {
        self.monitor.clone()
    }

    /// Update the replay_after_commit_index after STARTUP-SYNC completes.
    /// This ensures that authority_node.rs uses the post-sync value (from Go RPC)
    /// instead of the stale pre-sync value when computing `effective_handled`.
    /// Without this, the max(go_handled, dag_handled) in authority_node.rs can
    /// override our post-sync highest_handled_commit with a lower value from
    /// the surviving RocksDB DAG state.
    pub fn update_replay_after_commit_index(&mut self, new_index: CommitIndex) {
        self.replay_after_commit_index = new_index;
        self.consumer_last_processed_commit_index = new_index;
        self.monitor.set_highest_handled_commit(new_index);
    }

    pub fn update_last_block_timestamp_ms(&mut self, timestamp: u64) {
        self.last_block_timestamp_ms = timestamp;
    }
}

/// Helps monitor the progress of the consensus commit handler (consumer).
///
/// This component currently has two use usages:
/// 1. Checking the highest commit index processed by the consensus commit handler.
///    Consensus components can decide to wait for more commits to be processed before proceeding with
///    their work.
/// 2. Waiting for consensus commit handler to finish processing replayed commits.
///    Current usage is actually outside of consensus.
pub struct CommitConsumerMonitor {
    // highest commit that has been handed off for dispatch to the Go execution engine.
    // ROOT-CAUSE NOTE (2026-09-12, project memory mục 17 UPDATE #4): despite the name and
    // its doc comment above, this is NOT "processed" in the sense of "Go confirmed it
    // executed" -- executor.rs's dispatch_commit() sets this (via the commit_index_callback)
    // as soon as the commit is successfully enqueued onto BlockDeliveryManager's channel,
    // deliberately BEFORE awaiting that delivery's real completion (`response_rx`) --
    // see dispatch_commit's own "PIPELINE FIX" comment for why: waiting there was a real,
    // deliberate throughput optimization (avoids an IPC serialization bottleneck), not an
    // oversight. The result: this value climbs normally even while Go's own execution is
    // genuinely stuck, as long as the bounded delivery channel itself isn't full -- which
    // empty commits (the majority during low load) never touch at all, and even a handful
    // of stuck real commits rarely fill on their own. This is what made commit_syncer's
    // STALL-DETECTOR 4 ("Go execution / gap detector") blind to a live-reproduced,
    // multi-minute Go-side stall: it compares THIS value against synced_commit_index, and
    // both were advancing together throughout. See `go_confirmed_commit` below for the
    // value that actually reflects Go's own confirmed progress, added to close this gap.
    highest_handled_commit: watch::Sender<u32>,

    // The highest commit index Go has ACTUALLY confirmed executing, via
    // BlockDeliveryManager's real response_rx completion (not just successful enqueue).
    // Lags behind `highest_handled_commit` by design under normal load (that gap is exactly
    // the in-flight delivery queue depth) -- the signal to watch for is this value NOT
    // moving for an extended period while `highest_handled_commit` keeps climbing, which
    // is precisely what `highest_handled_commit` alone could never distinguish from Go
    // being merely a little behind.
    go_confirmed_commit: watch::Sender<u32>,

    // At node startup, the last consensus commit processed by the commit consumer from the previous run.
    // This can be 0 if starting a new epoch.
    consumer_last_processed_commit_index: CommitIndex,
}

impl CommitConsumerMonitor {
    pub(crate) fn new(
        replay_after_commit_index: CommitIndex,
        consumer_last_processed_commit_index: CommitIndex,
    ) -> Self {
        Self {
            highest_handled_commit: watch::Sender::new(replay_after_commit_index),
            go_confirmed_commit: watch::Sender::new(replay_after_commit_index),
            consumer_last_processed_commit_index,
        }
    }

    /// Gets the highest commit index processed by the consensus commit handler.
    pub fn highest_handled_commit(&self) -> CommitIndex {
        *self.highest_handled_commit.borrow()
    }

    /// Updates the highest commit index processed by the consensus commit handler.
    pub fn set_highest_handled_commit(&self, highest_handled_commit: CommitIndex) {
        debug!("Highest handled commit set to {}", highest_handled_commit);
        self.highest_handled_commit
            .send_replace(highest_handled_commit);
    }

    /// Gets the highest commit index Go has ACTUALLY confirmed executing (see the field's
    /// own doc comment for why this differs from `highest_handled_commit`). Only ever
    /// advances (see `set_go_confirmed_commit`).
    pub fn go_confirmed_commit(&self) -> CommitIndex {
        *self.go_confirmed_commit.borrow()
    }

    /// Records that Go has actually confirmed executing up to `commit_index`. Never
    /// regresses -- responses can theoretically arrive out of order across the bounded
    /// delivery channel's in-flight capacity, and this must stay monotonic to be a
    /// trustworthy "Go is alive and moving" signal for stall detection.
    pub fn set_go_confirmed_commit(&self, commit_index: CommitIndex) {
        if commit_index > *self.go_confirmed_commit.borrow() {
            debug!("Go-confirmed commit advanced to {}", commit_index);
            self.go_confirmed_commit.send_replace(commit_index);
        }
    }

    /// Waits for consensus to replay commits until the consumer last processed commit index.
    pub async fn replay_to_consumer_last_processed_commit_complete(&self) {
        let mut rx = self.highest_handled_commit.subscribe();
        loop {
            let highest_handled = *rx.borrow_and_update();
            if highest_handled >= self.consumer_last_processed_commit_index {
                return;
            }
            rx.changed()
                .await
                .expect("commit consumer watch channel closed unexpectedly — all senders dropped");
        }
    }
}

#[cfg(test)]
mod test {
    use crate::CommitConsumerMonitor;

    #[test]
    fn test_commit_consumer_monitor() {
        let monitor = CommitConsumerMonitor::new(0, 10);
        assert_eq!(monitor.highest_handled_commit(), 0);

        monitor.set_highest_handled_commit(100);
        assert_eq!(monitor.highest_handled_commit(), 100);
    }

    #[test]
    fn test_go_confirmed_commit_initial_value_matches_replay_after() {
        // go_confirmed_commit should start at replay_after_commit_index, same as
        // highest_handled_commit, so a freshly-started node doesn't immediately look
        // "stalled" relative to its own startup baseline.
        let monitor = CommitConsumerMonitor::new(42, 42);
        assert_eq!(monitor.go_confirmed_commit(), 42);
        assert_eq!(monitor.go_confirmed_commit(), monitor.highest_handled_commit());
    }

    #[test]
    fn test_go_confirmed_commit_advances() {
        let monitor = CommitConsumerMonitor::new(0, 0);
        monitor.set_go_confirmed_commit(5);
        assert_eq!(monitor.go_confirmed_commit(), 5);

        monitor.set_go_confirmed_commit(10);
        assert_eq!(monitor.go_confirmed_commit(), 10);
    }

    #[test]
    fn test_go_confirmed_commit_never_regresses() {
        // Responses can theoretically arrive out of order across the bounded delivery
        // channel's in-flight capacity -- set_go_confirmed_commit must ignore any
        // out-of-order call that would move the watermark backwards.
        let monitor = CommitConsumerMonitor::new(0, 0);
        monitor.set_go_confirmed_commit(10);
        monitor.set_go_confirmed_commit(3);
        assert_eq!(monitor.go_confirmed_commit(), 10);

        monitor.set_go_confirmed_commit(10);
        assert_eq!(monitor.go_confirmed_commit(), 10);
    }

    #[test]
    fn test_go_confirmed_commit_independent_of_highest_handled() {
        // The whole point of this field: it must be able to lag behind
        // highest_handled_commit (in-flight backlog) without being coupled to it.
        let monitor = CommitConsumerMonitor::new(0, 0);
        monitor.set_highest_handled_commit(50);
        assert_eq!(monitor.highest_handled_commit(), 50);
        assert_eq!(monitor.go_confirmed_commit(), 0);

        monitor.set_go_confirmed_commit(20);
        assert_eq!(monitor.highest_handled_commit(), 50);
        assert_eq!(monitor.go_confirmed_commit(), 20);
    }
}
