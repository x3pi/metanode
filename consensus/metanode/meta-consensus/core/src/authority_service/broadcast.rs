// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::{BTreeMap, BTreeSet},
    pin::Pin,
    sync::Arc,
    time::Duration,
};

use async_trait::async_trait;
use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::{BlockRef, Round};
use futures::{ready, stream, task, Stream, StreamExt};
use meta_macros::fail_point_async;
use mysten_metrics::spawn_monitored_task;
use parking_lot::RwLock;
use rand::seq::SliceRandom as _;
use tap::TapFallible;
use tokio::sync::broadcast;
use tokio_util::sync::ReusableBoxFuture;
use tracing::{debug, info, warn};

use crate::{
    block::{BlockAPI as _, ExtendedBlock, SignedBlock, VerifiedBlock, GENESIS_ROUND},
    block_verifier::BlockVerifier,
    commit::{CommitAPI as _, CommitRange, TrustedCommit},
    commit_vote_monitor::CommitVoteMonitor,
    context::Context,
    core_thread::CoreThreadDispatcher,
    dag_state::DagState,
    epoch_change::{EpochChangeProposal, EpochChangeVote},
    epoch_change_provider::EpochChangeProcessor,
    error::{ConsensusError, ConsensusResult},
    legacy_store::LegacyEpochStoreManager,
    network::{BlockStream, ExtendedSerializedBlock, NetworkService},
    round_tracker::PeerRoundTracker,
    storage::Store,
    synchronizer::SynchronizerHandle,
    transaction_certifier::TransactionCertifier,
    CommitIndex,
};

struct Counter {
    count: usize,
    subscriptions_by_authority: Vec<usize>,
}

/// Atomically counts the number of active subscriptions to the block broadcast stream.
pub(crate) struct SubscriptionCounter {
    context: Arc<Context>,
    counter: parking_lot::Mutex<Counter>,
}

impl SubscriptionCounter {
    pub(crate) fn new(context: Arc<Context>) -> Self {
        // Set the subscribed peers by default to 0
        for (_, authority) in context.committee.authorities() {
            context
                .metrics
                .node_metrics
                .subscribed_by
                .with_label_values(&[authority.hostname.as_str()])
                .set(0);
        }

        Self {
            counter: parking_lot::Mutex::new(Counter {
                count: 0,
                subscriptions_by_authority: vec![0; context.committee.size()],
            }),
            context,
        }
    }

    pub(crate) fn increment(&self, peer: AuthorityIndex) -> Result<(), ConsensusError> {
        let mut counter = self.counter.lock();
        counter.count += 1;
        counter.subscriptions_by_authority[peer] += 1;

        let peer_hostname = &self.context.committee.authority(peer).hostname;
        self.context
            .metrics
            .node_metrics
            .subscribed_by
            .with_label_values(&[peer_hostname])
            .set(1);

        Ok(())
    }

    pub(crate) fn decrement(&self, peer: AuthorityIndex) -> Result<(), ConsensusError> {
        let mut counter = self.counter.lock();
        counter.count -= 1;
        counter.subscriptions_by_authority[peer] -= 1;

        if counter.subscriptions_by_authority[peer] == 0 {
            let peer_hostname = &self.context.committee.authority(peer).hostname;
            self.context
                .metrics
                .node_metrics
                .subscribed_by
                .with_label_values(&[peer_hostname])
                .set(0);
        }

        Ok(())
    }
}

/// Each broadcasted block stream wraps a broadcast receiver for blocks.
/// It yields blocks that are broadcasted after the stream is created.
pub(crate) type BroadcastedBlockStream = BroadcastStream<ExtendedBlock>;

/// Adapted from `tokio_stream::wrappers::BroadcastStream`. The main difference is that
/// this tolerates lags with only logging, without yielding errors.
pub(crate) struct BroadcastStream<T> {
    peer: AuthorityIndex,
    // Stores the receiver across poll_next() calls.
    inner: ReusableBoxFuture<
        'static,
        (
            Result<T, broadcast::error::RecvError>,
            broadcast::Receiver<T>,
        ),
    >,
    // Counts total subscriptions / active BroadcastStreams.
    subscription_counter: Arc<SubscriptionCounter>,
}

impl<T: 'static + Clone + Send> BroadcastStream<T> {
    pub(crate) fn new(
        peer: AuthorityIndex,
        rx: broadcast::Receiver<T>,
        subscription_counter: Arc<SubscriptionCounter>,
    ) -> Self {
        if let Err(err) = subscription_counter.increment(peer) {
            match err {
                ConsensusError::Shutdown => {}
                _ => panic!("Unexpected error: {err}"),
            }
        }
        Self {
            peer,
            inner: ReusableBoxFuture::new(make_recv_future(rx)),
            subscription_counter,
        }
    }
}

impl<T: 'static + Clone + Send> Stream for BroadcastStream<T> {
    type Item = T;

    fn poll_next(
        mut self: Pin<&mut Self>,
        cx: &mut task::Context<'_>,
    ) -> task::Poll<Option<Self::Item>> {
        let peer = self.peer;
        let maybe_item = loop {
            let (result, rx) = ready!(self.inner.poll(cx));
            self.inner.set(make_recv_future(rx));

            match result {
                Ok(item) => break Some(item),
                Err(broadcast::error::RecvError::Closed) => {
                    info!("Block BroadcastedBlockStream {} closed", peer);
                    break None;
                }
                Err(broadcast::error::RecvError::Lagged(n)) => {
                    warn!(
                        "Block BroadcastedBlockStream {} lagged by {} messages",
                        peer, n
                    );
                    continue;
                }
            }
        };
        task::Poll::Ready(maybe_item)
    }
}

impl<T> Drop for BroadcastStream<T> {
    fn drop(&mut self) {
        if let Err(err) = self.subscription_counter.decrement(self.peer) {
            match err {
                ConsensusError::Shutdown => {}
                _ => panic!("Unexpected error: {err}"),
            }
        }
    }
}

async fn make_recv_future<T: Clone>(
    mut rx: broadcast::Receiver<T>,
) -> (
    Result<T, broadcast::error::RecvError>,
    broadcast::Receiver<T>,
) {
    let result = rx.recv().await;
    (result, rx)
}

