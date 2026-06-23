// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0
use std::{
    collections::{BTreeMap, BTreeSet, HashMap},
    sync::{
        atomic::{AtomicU32, Ordering},
        Arc,
    },
    time::Duration,
};

use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::{BlockRef, Round};
use parking_lot::{Mutex, RwLock};

use tokio::{
    sync::{mpsc::error::TrySendError, oneshot},
    task::{JoinError, JoinSet},
    time::{sleep_until, Instant},
};
use tracing::{debug, error, info, trace};

use crate::{
    authority_service::COMMIT_LAG_MULTIPLIER, core_thread::CoreThreadDispatcher,
};
use crate::{
    block::{SignedBlock, VerifiedBlock},
    block_verifier::BlockVerifier,
    commit_vote_monitor::CommitVoteMonitor,
    context::Context,
    dag_state::DagState,
    error::{ConsensusError, ConsensusResult},
    network::NetworkClient,
    transaction_certifier::TransactionCertifier,
    BlockAPI,
};

/// The number of concurrent fetch blocks requests per authority
const FETCH_BLOCKS_CONCURRENCY: usize = 15;

/// Timeouts when fetching blocks.
const FETCH_REQUEST_TIMEOUT: Duration = Duration::from_millis(2_000);
const FETCH_FROM_PEERS_TIMEOUT: Duration = Duration::from_millis(4_000);

const MAX_AUTHORITIES_TO_FETCH_PER_BLOCK: usize = 2;

// Max number of peers to request missing blocks concurrently in periodic sync.
const MAX_PERIODIC_SYNC_PEERS: usize = 3;

struct BlocksGuard {
    map: Arc<InflightBlocksMap>,
    block_refs: BTreeSet<BlockRef>,
    peer: AuthorityIndex,
}

impl Drop for BlocksGuard {
    fn drop(&mut self) {
        self.map.unlock_blocks(&self.block_refs, self.peer);
    }
}

// Keeps a mapping between the missing blocks that have been instructed to be fetched and the authorities
// that are currently fetching them. For a block ref there is a maximum number of authorities that can
// concurrently fetch it. The authority ids that are currently fetching a block are set on the corresponding
// `BTreeSet` and basically they act as "locks".
struct InflightBlocksMap {
    inner: Mutex<HashMap<BlockRef, BTreeSet<AuthorityIndex>>>,
}

impl InflightBlocksMap {
    fn new() -> Arc<Self> {
        Arc::new(Self {
            inner: Mutex::new(HashMap::new()),
        })
    }

    /// Locks the blocks to be fetched for the assigned `peer_index`. We want to avoid re-fetching the
    /// missing blocks from too many authorities at the same time, thus we limit the concurrency
    /// per block by attempting to lock per block. If a block is already fetched by the maximum allowed
    /// number of authorities, then the block ref will not be included in the returned set. The method
    /// returns all the block refs that have been successfully locked and allowed to be fetched.
    fn lock_blocks(
        self: &Arc<Self>,
        missing_block_refs: BTreeSet<BlockRef>,
        peer: AuthorityIndex,
    ) -> Option<BlocksGuard> {
        let mut blocks = BTreeSet::new();
        let mut inner = self.inner.lock();

        for block_ref in missing_block_refs {
            // check that the number of authorities that are already instructed to fetch the block is not
            // higher than the allowed and the `peer_index` has not already been instructed to do that.
            let authorities = inner.entry(block_ref).or_default();
            if authorities.len() < MAX_AUTHORITIES_TO_FETCH_PER_BLOCK
                && authorities.get(&peer).is_none()
            {
                assert!(authorities.insert(peer));
                blocks.insert(block_ref);
            }
        }

        if blocks.is_empty() {
            None
        } else {
            Some(BlocksGuard {
                map: self.clone(),
                block_refs: blocks,
                peer,
            })
        }
    }

    /// Unlocks the provided block references for the given `peer`. The unlocking is strict, meaning that
    /// if this method is called for a specific block ref and peer more times than the corresponding lock
    /// has been called, it will panic.
    fn unlock_blocks(self: &Arc<Self>, block_refs: &BTreeSet<BlockRef>, peer: AuthorityIndex) {
        // Now mark all the blocks as fetched from the map
        let mut blocks_to_fetch = self.inner.lock();
        for block_ref in block_refs {
            let authorities = blocks_to_fetch
                .get_mut(block_ref)
                .expect("Should have found a non empty map");

            assert!(authorities.remove(&peer), "Peer index should be present!");

            // if the last one then just clean up
            if authorities.is_empty() {
                blocks_to_fetch.remove(block_ref);
            }
        }
    }

    /// Drops the provided `blocks_guard` which will force to unlock the blocks, and lock now again the
    /// referenced block refs. The swap is best effort and there is no guarantee that the `peer` will
    /// be able to acquire the new locks.
    fn swap_locks(
        self: &Arc<Self>,
        blocks_guard: BlocksGuard,
        peer: AuthorityIndex,
    ) -> Option<BlocksGuard> {
        let block_refs = blocks_guard.block_refs.clone();

        // Explicitly drop the guard
        drop(blocks_guard);

        // Now create new guard
        self.lock_blocks(block_refs, peer)
    }

    #[cfg(test)]
    fn num_of_locked_blocks(self: &Arc<Self>) -> usize {
        let inner = self.inner.lock();
        inner.len()
    }
}

enum Command {
    FetchBlocks {
        missing_block_refs: BTreeSet<BlockRef>,
        peer_index: AuthorityIndex,
        result: oneshot::Sender<Result<(), ConsensusError>>,
    },
    FetchOwnLastBlock,
    KickOffScheduler,
}

use tokio::sync::mpsc::{Sender, Receiver, channel};

pub(crate) struct SynchronizerHandle {
    commands_sender: Sender<Command>,
    tasks: tokio::sync::Mutex<JoinSet<()>>,
}

impl SynchronizerHandle {
    /// Explicitly asks from the synchronizer to fetch the blocks - provided the block_refs set - from
    /// the peer authority.
    pub(crate) async fn fetch_blocks(
        &self,
        missing_block_refs: BTreeSet<BlockRef>,
        peer_index: AuthorityIndex,
    ) -> ConsensusResult<()> {
        let (sender, receiver) = oneshot::channel();
        self.commands_sender
            .send(Command::FetchBlocks {
                missing_block_refs,
                peer_index,
                result: sender,
            })
            .await
            .map_err(|_err| ConsensusError::Shutdown)?;
        receiver.await.map_err(|_err| ConsensusError::Shutdown)?
    }

    pub(crate) async fn stop(&self) -> Result<(), JoinError> {
        let mut tasks = self.tasks.lock().await;
        tasks.abort_all();
        while let Some(result) = tasks.join_next().await {
            result?
        }
        Ok(())
    }
}

/// `Synchronizer` oversees live block synchronization, crucial for node progress. Live synchronization
/// refers to the process of retrieving missing blocks, particularly those essential for advancing a node
/// when data from only a few rounds is absent. If a node significantly lags behind the network,
/// `commit_syncer` handles fetching missing blocks via a more efficient approach. `Synchronizer`
/// aims for swift catch-up employing two mechanisms:
///
/// 1. Explicitly requesting missing blocks from designated authorities via the "block send" path.
///    This includes attempting to fetch any missing ancestors necessary for processing a received block.
///    Such requests prioritize the block author, maximizing the chance of prompt retrieval.
///    A locking mechanism allows concurrent requests for missing blocks from up to two authorities
///    simultaneously, enhancing the chances of timely retrieval. Notably, if additional missing blocks
///    arise during block processing, requests to the same authority are deferred to the scheduler.
///
/// 2. Periodically requesting missing blocks via a scheduler. This primarily serves to retrieve
///    missing blocks that were not ancestors of a received block via the "block send" path.
///    The scheduler operates on either a fixed periodic basis or is triggered immediately
///    after explicit fetches described in (1), ensuring continued block retrieval if gaps persist.
///
/// Additionally to the above, the synchronizer can synchronize and fetch the last own proposed block
/// from the network peers as best effort approach to recover node from amnesia and avoid making the
/// node equivocate.
pub(crate) struct Synchronizer<C: NetworkClient, V: BlockVerifier, D: CoreThreadDispatcher> {
    context: Arc<Context>,
    commands_receiver: Receiver<Command>,
    fetch_block_senders: BTreeMap<AuthorityIndex, Sender<BlocksGuard>>,
    core_dispatcher: Arc<D>,
    commit_vote_monitor: Arc<CommitVoteMonitor>,
    dag_state: Arc<RwLock<DagState>>,
    fetch_blocks_scheduler_task: JoinSet<()>,
    fetch_own_last_block_task: JoinSet<()>,
    network_client: Arc<C>,
    block_verifier: Arc<V>,
    transaction_certifier: TransactionCertifier,
    inflight_blocks_map: Arc<InflightBlocksMap>,
    commands_sender: Sender<Command>,
    /// Tracks consecutive periodic sync failures for exponential backoff.
    /// Resets to 0 when any periodic sync succeeds.
    consecutive_sync_failures: Arc<AtomicU32>,
}


pub mod fetcher;
pub mod scheduler;

impl<C: NetworkClient, V: BlockVerifier, D: CoreThreadDispatcher> Synchronizer<C, V, D> {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn start(
        network_client: Arc<C>,
        context: Arc<Context>,
        core_dispatcher: Arc<D>,
        commit_vote_monitor: Arc<CommitVoteMonitor>,
        block_verifier: Arc<V>,
        transaction_certifier: TransactionCertifier,
        dag_state: Arc<RwLock<DagState>>,
        sync_last_known_own_block: bool,
    ) -> Arc<SynchronizerHandle> {
        let (commands_sender, commands_receiver) =
            channel(1_000);
        let inflight_blocks_map = InflightBlocksMap::new();

        // Spawn the tasks to fetch the blocks from the others
        let mut fetch_block_senders = BTreeMap::new();
        let mut tasks = JoinSet::new();
        for (index, _) in context.committee.authorities() {
            if index == context.own_index {
                continue;
            }
            let (sender, receiver) =
                channel(FETCH_BLOCKS_CONCURRENCY);
            let fetch_blocks_from_authority_async = Self::fetch_blocks_from_authority(
                index,
                network_client.clone(),
                block_verifier.clone(),
                transaction_certifier.clone(),
                commit_vote_monitor.clone(),
                context.clone(),
                core_dispatcher.clone(),
                dag_state.clone(),
                receiver,
                commands_sender.clone(),
            );
            tasks.spawn(fetch_blocks_from_authority_async);
            fetch_block_senders.insert(index, sender);
        }

        let commands_sender_clone = commands_sender.clone();

        if sync_last_known_own_block {
            commands_sender
                .try_send(Command::FetchOwnLastBlock)
                .expect("Failed to sync our last block");
        }

        // Spawn the task to listen to the requests & periodic runs
        tasks.spawn(async move {
            let mut s = Self {
                context,
                commands_receiver,
                fetch_block_senders,
                core_dispatcher,
                commit_vote_monitor,
                fetch_blocks_scheduler_task: JoinSet::new(),
                fetch_own_last_block_task: JoinSet::new(),
                network_client,
                block_verifier,
                transaction_certifier,
                inflight_blocks_map,
                commands_sender: commands_sender_clone,
                dag_state,
                consecutive_sync_failures: Arc::new(AtomicU32::new(0)),
            };
            s.run().await;
        });

        Arc::new(SynchronizerHandle {
            commands_sender,
            tasks: tokio::sync::Mutex::new(tasks),
        })
    }


    // The main loop to listen for the submitted commands.
    async fn run(&mut self) {
        // Base interval for periodic sync. Actual interval may be scaled up
        // by exponential backoff when sync consistently fails (e.g., epoch mismatch).
        const PERIODIC_FETCH_INTERVAL: Duration = Duration::from_millis(200);
        const MAX_BACKOFF_INTERVAL: Duration = Duration::from_secs(10);
        let scheduler_timeout = sleep_until(Instant::now() + PERIODIC_FETCH_INTERVAL);

        tokio::pin!(scheduler_timeout);

        loop {
            tokio::select! {
                Some(command) = self.commands_receiver.recv() => {
                    match command {
                        Command::FetchBlocks{ missing_block_refs, peer_index, result } => {
                            if peer_index == self.context.own_index {
                                error!("We should never attempt to fetch blocks from our own node");
                                continue;
                            }

                            // Keep only the max allowed blocks to request. Additional missing blocks
                            // will be fetched via periodic sync.
                            // Fetch from the lowest to highest round, to ensure progress.
                            let missing_block_refs = missing_block_refs
                                .into_iter()
                                .take(self.context.parameters.max_blocks_per_sync)
                                .collect();

                            let blocks_guard = self.inflight_blocks_map.lock_blocks(missing_block_refs, peer_index);
                            let Some(blocks_guard) = blocks_guard else {
                                result.send(Ok(())).ok();
                                continue;
                            };

                            // We don't block if the corresponding peer task is saturated - but we rather drop the request. That's ok as the periodic
                            // synchronization task will handle any still missing blocks in next run.
                            let r = self
                                .fetch_block_senders
                                .get(&peer_index)
                                .expect("Fatal error, sender should be present")
                                .try_send(blocks_guard)
                                .map_err(|err| {
                                    match err {
                                        TrySendError::Full(_) => {
                                            let peer_hostname = &self.context.committee.authority(peer_index).hostname;
                                            self.context
                                                .metrics
                                                .node_metrics
                                                .synchronizer_skipped_fetch_requests
                                                .with_label_values(&[peer_hostname])
                                                .inc();
                                            ConsensusError::SynchronizerSaturated(peer_index)
                                        },
                                        TrySendError::Closed(_) => ConsensusError::Shutdown
                                    }
                                });

                            result.send(r).ok();
                        }
                        Command::FetchOwnLastBlock => {
                            if self.fetch_own_last_block_task.is_empty() {
                                self.start_fetch_own_last_block_task();
                            }
                        }
                        Command::KickOffScheduler => {
                            // just reset the scheduler timeout timer to run immediately if not already running.
                            // If the scheduler is already running then just reduce the remaining time to run.
                            let timeout = if self.fetch_blocks_scheduler_task.is_empty() {
                                Instant::now()
                            } else {
                                Instant::now() + PERIODIC_FETCH_INTERVAL.checked_div(2).expect("dividing Duration by 2 should never fail")
                            };

                            // only reset if it is earlier than the next deadline
                            if timeout < scheduler_timeout.deadline() {
                                scheduler_timeout.as_mut().reset(timeout);
                            }
                        }
                    }
                },
                Some(result) = self.fetch_own_last_block_task.join_next(), if !self.fetch_own_last_block_task.is_empty() => {
                    match result {
                        Ok(()) => {},
                        Err(e) => {
                            if e.is_cancelled() {
                            } else if e.is_panic() {
                                std::panic::resume_unwind(e.into_panic());
                            } else {
                                error!("fetch our last block task failed (non-panic, non-cancel JoinError): {e}");
                            }
                        },
                    };
                },
                Some(result) = self.fetch_blocks_scheduler_task.join_next(), if !self.fetch_blocks_scheduler_task.is_empty() => {
                    match result {
                        Ok(()) => {},
                        Err(e) => {
                            if e.is_cancelled() {
                            } else if e.is_panic() {
                                std::panic::resume_unwind(e.into_panic());
                            } else {
                                error!("fetch blocks scheduler task failed (non-panic, non-cancel JoinError): {e}");
                            }
                        },
                    };
                },
                () = &mut scheduler_timeout => {
                    // we want to start a new task only if the previous one has already finished.
                    // TODO: consider starting backup fetches in parallel, when a fetch takes too long?
                    if self.fetch_blocks_scheduler_task.is_empty() {
                        if let Err(err) = self.start_fetch_missing_blocks_task().await {
                            debug!("Core is shutting down, synchronizer is shutting down: {err:?}");
                            return;
                        }

                        // Apply exponential backoff when sync keeps failing (e.g., lagging node
                        // fetching blocks from peers in a different epoch). This prevents a
                        // spam loop of ~5 futile requests/second per peer.
                        let failures = self.consecutive_sync_failures.load(Ordering::Relaxed);
                        
                        // Detect if we are in sync mode (lagging)
                        let local_commit = self.dag_state.read().last_commit_index();
                        let quorum_commit = self.commit_vote_monitor.quorum_commit_index();
                        let lag = quorum_commit.saturating_sub(local_commit);
                        let is_sync_mode = lag > 50 || (quorum_commit > 0 && (lag as f64 / quorum_commit as f64) > 0.05);

                        let next_interval = if failures > 0 {
                            let backoff = PERIODIC_FETCH_INTERVAL * 2u32.saturating_pow(failures.min(6));
                            let max_backoff = if is_sync_mode { Duration::from_secs(2) } else { MAX_BACKOFF_INTERVAL };
                            backoff.min(max_backoff)
                        } else {
                            PERIODIC_FETCH_INTERVAL
                        };
                        scheduler_timeout
                            .as_mut()
                            .reset(Instant::now() + next_interval);
                    }
                },
            }
        }
    }


    fn get_highest_accepted_rounds(
        dag_state: Arc<RwLock<DagState>>,
        context: &Arc<Context>,
    ) -> Vec<Round> {
        let blocks = dag_state
            .read()
            .get_last_cached_block_per_authority(Round::MAX);
        assert_eq!(blocks.len(), context.committee.size());

        blocks
            .into_iter()
            .map(|(block, _)| block.round())
            .collect::<Vec<_>>()
    }


    fn verify_blocks(
        serialized_blocks: Vec<Bytes>,
        block_verifier: Arc<V>,
        transaction_certifier: TransactionCertifier,
        context: &Context,
        peer_index: AuthorityIndex,
        dag_state: Arc<RwLock<DagState>>,
        network_client: Arc<C>,
    ) -> ConsensusResult<Vec<VerifiedBlock>> {
        let mut verified_blocks = Vec::new();
        let mut voted_blocks = Vec::new();
        for serialized_block in serialized_blocks {
            let signed_block: SignedBlock =
                bcs::from_bytes(&serialized_block).map_err(ConsensusError::MalformedBlock)?;

            // Dedup: skip verification if block is already accepted in dag_state.
            // This avoids redundant signature verification for blocks received via both
            // broadcast and fetch paths.
            {
                let block_digest = VerifiedBlock::compute_digest(&serialized_block);
                let block_ref =
                    BlockRef::new(signed_block.round(), signed_block.author(), block_digest);
                if context.committee.is_valid_index(signed_block.author())
                    && dag_state.read().contains_block(&block_ref)
                {
                    trace!(
                        "Skipping already-verified block {} in synchronizer",
                        block_ref
                    );
                    context
                        .metrics
                        .node_metrics
                        .synchronizer_fetched_blocks_by_peer
                        .with_label_values(&[
                            &context.committee.authority(peer_index).hostname,
                            "dedup_skip",
                        ])
                        .inc();
                    continue;
                }
            }

            let (verified_block, reject_txn_votes) = match block_verifier
                .verify_and_vote(signed_block.clone(), serialized_block.clone())
            {
                Ok(result) => result,
                Err(ConsensusError::WrongEpoch { expected, actual }) => {
                    // CROSS-EPOCH FIX: Ancestors from previous epochs are required
                    // to complete the causal history of the current epoch's first blocks.
                    // We must verify them without the epoch check using verify_for_commit_sync.
                    if actual < expected {
                        debug!(
                            "Verifying cross-epoch ancestor block from peer {} (expected epoch {}, got {})",
                            peer_index, expected, actual
                        );
                        match block_verifier.verify_for_commit_sync(signed_block, serialized_block) {
                            Ok(result) => result,
                            Err(e) => {
                                let hostname = context.committee.authority(peer_index).hostname.clone();
                                context
                                    .metrics
                                    .node_metrics
                                    .invalid_blocks
                                    .with_label_values(&[&hostname, "synchronizer_cross_epoch", e.clone().name()])
                                    .inc();
                                info!("Invalid cross-epoch block received from {}: {}", peer_index, e);
                                return Err(e);
                            }
                        }
                    } else {
                        let hostname = context.committee.authority(peer_index).hostname.clone();
                        context
                            .metrics
                            .node_metrics
                            .invalid_blocks
                            .with_label_values(&[&hostname, "synchronizer", "WrongEpoch"])
                            .inc();
                        info!("Invalid block received from {}: Block has wrong epoch: expected {}, actual {}", peer_index, expected, actual);
                        return Err(ConsensusError::WrongEpoch { expected, actual });
                    }
                }
                Err(ConsensusError::MissingTransactions(missing)) => {
                    // Fetch the missing transactions synchronously using block_on
                    let rt = tokio::runtime::Handle::current();
                    let fetch_res = rt.block_on(network_client.fetch_transactions(peer_index, missing, FETCH_REQUEST_TIMEOUT));
                    match fetch_res {
                        Ok(txs_bytes) => {
                            {
                                let mut cache = crate::transaction::get_global_tx_cache().write();
                                for tx_bytes in txs_bytes {
                                    let tx = crate::block::Transaction::new(tx_bytes.to_vec());
                                    cache.insert(tx.digest(), tx);
                                }
                            }
                            // Re-verify after inserting missing transactions into cache
                            block_verifier.verify_and_vote(signed_block, serialized_block)?
                        }
                        Err(err) => return Err(err),
                    }
                }
                Err(e) => {
                    let hostname = context.committee.authority(peer_index).hostname.clone();
                    context
                        .metrics
                        .node_metrics
                        .invalid_blocks
                        .with_label_values(&[&hostname, "synchronizer", e.clone().name()])
                        .inc();
                    info!("Invalid block received from {}: {}", peer_index, e);
                    return Err(e);
                }
            };

            // TODO: improve efficiency, maybe suspend and continue processing the block asynchronously.
            let now = context.clock.timestamp_utc_ms();
            let drift = verified_block.timestamp_ms().saturating_sub(now);
            if drift > 0 {
                let peer_hostname = &context
                    .committee
                    .authority(verified_block.author())
                    .hostname;
                context
                    .metrics
                    .node_metrics
                    .block_timestamp_drift_ms
                    .with_label_values(&[peer_hostname, "synchronizer"])
                    .inc_by(drift);

                trace!(
                    "Synced block {} timestamp {} is in the future (now={}).",
                    verified_block.reference(),
                    verified_block.timestamp_ms(),
                    now
                );
            }

            verified_blocks.push(verified_block.clone());
            voted_blocks.push((verified_block, reject_txn_votes));
        }

        if context.protocol_config.mysticeti_fastpath() {
            transaction_certifier.add_voted_blocks(voted_blocks);
        }

        Ok(verified_blocks)
    }


    fn is_commit_lagging(&self) -> bool {
        if self.commit_vote_monitor.highest_seen_epoch() > self.context.committee.epoch() {
            return true;
        }
        let last_commit_index = self.dag_state.read().last_commit_index();
        let quorum_commit_index = self.commit_vote_monitor.quorum_commit_index();
        let commit_threshold = last_commit_index
            + self.context.parameters.commit_sync_batch_size * COMMIT_LAG_MULTIPLIER;
        commit_threshold < quorum_commit_index
    }


}


#[cfg(test)]
mod tests {
    use std::{
        collections::{BTreeMap, BTreeSet},
        sync::Arc,
        time::Duration,
    };

    use async_trait::async_trait;
    use bytes::Bytes;
    use consensus_config::{AuthorityIndex, Parameters};
    use consensus_types::block::{BlockDigest, BlockRef, Round};
    use tokio::sync::mpsc;
    use parking_lot::RwLock;
    use tokio::{sync::Mutex, time::sleep};

    use crate::commit::{CommitVote, TrustedCommit};
    use crate::{
        authority_service::COMMIT_LAG_MULTIPLIER, core_thread::MockCoreThreadDispatcher,
        transaction_certifier::TransactionCertifier,
    };
    use crate::{
        block::{TestBlock, VerifiedBlock},
        block_verifier::NoopBlockVerifier,
        commit::CommitRange,
        commit_vote_monitor::CommitVoteMonitor,
        context::Context,
        core_thread::CoreThreadDispatcher,
        dag_state::DagState,
        error::{ConsensusError, ConsensusResult},
        network::{BlockStream, NetworkClient},
        storage::mem_store::MemStore,
        synchronizer::{
            InflightBlocksMap, Synchronizer, FETCH_BLOCKS_CONCURRENCY, FETCH_REQUEST_TIMEOUT,
        },
        CommitDigest, CommitIndex,
    };

    type FetchRequestKey = (Vec<BlockRef>, AuthorityIndex);
    type FetchRequestResponse = (Vec<VerifiedBlock>, Option<Duration>);
    type FetchLatestBlockKey = (AuthorityIndex, Vec<AuthorityIndex>);
    type FetchLatestBlockResponse = (Vec<VerifiedBlock>, Option<Duration>);

    #[derive(Default)]
    struct MockNetworkClient {
        fetch_blocks_requests: Mutex<BTreeMap<FetchRequestKey, FetchRequestResponse>>,
        fetch_latest_blocks_requests:
            Mutex<BTreeMap<FetchLatestBlockKey, Vec<FetchLatestBlockResponse>>>,
    }

    impl MockNetworkClient {
        async fn stub_fetch_blocks(
            &self,
            blocks: Vec<VerifiedBlock>,
            peer: AuthorityIndex,
            latency: Option<Duration>,
        ) {
            let mut lock = self.fetch_blocks_requests.lock().await;
            let block_refs = blocks
                .iter()
                .map(|block| block.reference())
                .collect::<Vec<_>>();
            lock.insert((block_refs, peer), (blocks, latency));
        }

        async fn stub_fetch_latest_blocks(
            &self,
            blocks: Vec<VerifiedBlock>,
            peer: AuthorityIndex,
            authorities: Vec<AuthorityIndex>,
            latency: Option<Duration>,
        ) {
            let mut lock = self.fetch_latest_blocks_requests.lock().await;
            lock.entry((peer, authorities))
                .or_default()
                .push((blocks, latency));
        }

        async fn fetch_latest_blocks_pending_calls(&self) -> usize {
            let lock = self.fetch_latest_blocks_requests.lock().await;
            lock.len()
        }
    }

    #[async_trait]
    impl NetworkClient for MockNetworkClient {
        async fn send_block(
            &self,
            _peer: AuthorityIndex,
            _serialized_block: &VerifiedBlock,
            _timeout: Duration,
        ) -> ConsensusResult<()> {
            unimplemented!("Unimplemented")
        }

        async fn subscribe_blocks(
            &self,
            _peer: AuthorityIndex,
            _last_received: Round,
            _timeout: Duration,
        ) -> ConsensusResult<BlockStream> {
            unimplemented!("Unimplemented")
        }

        async fn fetch_blocks(
            &self,
            peer: AuthorityIndex,
            block_refs: Vec<BlockRef>,
            _highest_accepted_rounds: Vec<Round>,
            _breadth_first: bool,
            _timeout: Duration,
        ) -> ConsensusResult<Vec<Bytes>> {
            let mut lock = self.fetch_blocks_requests.lock().await;
            let response = lock.remove(&(block_refs.clone(), peer)).unwrap_or_else(|| {
                panic!(
                    "Unexpected fetch blocks request made: {:?} {}. Current lock: {:?}",
                    block_refs, peer, lock
                );
            });

            let serialised = response
                .0
                .into_iter()
                .map(|block| block.serialized().clone())
                .collect::<Vec<_>>();

            drop(lock);

            if let Some(latency) = response.1 {
                sleep(latency).await;
            }

            Ok(serialised)
        }

        async fn fetch_commits(
            &self,
            _peer: AuthorityIndex,
            _commit_range: CommitRange,
            _timeout: Duration,
        ) -> ConsensusResult<(Vec<Bytes>, Vec<Bytes>, Vec<Bytes>)> {
            unimplemented!("Unimplemented")
        }

        async fn fetch_commits_by_global_range(
            &self,
            _peer: AuthorityIndex,
            _start_global_index: u64,
            _max_global_index: u64,
            _timeout: Duration,
        ) -> ConsensusResult<Vec<crate::network::tonic_network::GlobalCommitInfo>> {
            unimplemented!("Unimplemented")
        }

        async fn send_epoch_change_proposal(
            &self,
            _peer: AuthorityIndex,
            _proposal: &crate::epoch_change::EpochChangeProposal,
            _timeout: Duration,
        ) -> ConsensusResult<()> {
            unimplemented!("Unimplemented")
        }

        async fn send_epoch_change_vote(
            &self,
            _peer: AuthorityIndex,
            _vote: &crate::epoch_change::EpochChangeVote,
            _timeout: Duration,
        ) -> ConsensusResult<()> {
            unimplemented!("Unimplemented")
        }

        async fn fetch_latest_blocks(
            &self,
            peer: AuthorityIndex,
            authorities: Vec<AuthorityIndex>,
            _timeout: Duration,
        ) -> ConsensusResult<Vec<Bytes>> {
            let mut lock = self.fetch_latest_blocks_requests.lock().await;
            let mut responses = lock
                .remove(&(peer, authorities.clone()))
                .expect("Unexpected fetch blocks request made");

            let response = responses.remove(0);
            let serialised = response
                .0
                .into_iter()
                .map(|block| block.serialized().clone())
                .collect::<Vec<_>>();

            if !responses.is_empty() {
                lock.insert((peer, authorities), responses);
            }

            drop(lock);

            if let Some(latency) = response.1 {
                sleep(latency).await;
            }

            Ok(serialised)
        }

        async fn get_latest_rounds(
            &self,
            _peer: AuthorityIndex,
            _timeout: Duration,
        ) -> ConsensusResult<(Vec<Round>, Vec<Round>)> {
            unimplemented!("Unimplemented")
        }
    }

    #[test]
    fn test_inflight_blocks_map() {
        // GIVEN
        let map = InflightBlocksMap::new();
        let some_block_refs = [
            BlockRef::new(1, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
            BlockRef::new(10, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
            BlockRef::new(12, AuthorityIndex::new_for_test(3), BlockDigest::MIN),
            BlockRef::new(15, AuthorityIndex::new_for_test(2), BlockDigest::MIN),
        ];
        let missing_block_refs = some_block_refs.iter().cloned().collect::<BTreeSet<_>>();

        // Lock & unlock blocks
        {
            let mut all_guards = Vec::new();

            // Try to acquire the block locks for authorities 1 & 2
            for i in 1..=2 {
                let authority = AuthorityIndex::new_for_test(i);

                let guard = map.lock_blocks(missing_block_refs.clone(), authority);
                let guard = guard.expect("Guard should be created");
                assert_eq!(guard.block_refs.len(), 4);

                all_guards.push(guard);

                // trying to acquire any of them again will not succeed
                let guard = map.lock_blocks(missing_block_refs.clone(), authority);
                assert!(guard.is_none());
            }

            // Trying to acquire for authority 3 it will fail - as we have maxed out the number of allowed peers
            let authority_3 = AuthorityIndex::new_for_test(3);

            let guard = map.lock_blocks(missing_block_refs.clone(), authority_3);
            assert!(guard.is_none());

            // Explicitly drop the guard of authority 1 and try for authority 3 again - it will now succeed
            drop(all_guards.remove(0));

            let guard = map.lock_blocks(missing_block_refs.clone(), authority_3);
            let guard = guard.expect("Guard should be successfully acquired");

            assert_eq!(guard.block_refs, missing_block_refs);

            // Dropping all guards should unlock on the block refs
            drop(guard);
            drop(all_guards);

            assert_eq!(map.num_of_locked_blocks(), 0);
        }

        // Swap locks
        {
            // acquire a lock for authority 1
            let authority_1 = AuthorityIndex::new_for_test(1);
            let guard = map
                .lock_blocks(missing_block_refs.clone(), authority_1)
                .unwrap();

            // Now swap the locks for authority 2
            let authority_2 = AuthorityIndex::new_for_test(2);
            let guard = map.swap_locks(guard, authority_2);

            assert_eq!(guard.unwrap().block_refs, missing_block_refs);
        }
    }

    #[tokio::test]
    async fn successful_fetch_blocks_from_peer() {
        // GIVEN
        let (context, _) = Context::new_for_test(4);
        let context = Arc::new(context);
        let block_verifier = Arc::new(NoopBlockVerifier {});
        let core_dispatcher = Arc::new(MockCoreThreadDispatcher::default());
        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
        let network_client = Arc::new(MockNetworkClient::default());
        let (blocks_sender, _blocks_receiver) =
            tokio::sync::mpsc::unbounded_channel();
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store)));
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            blocks_sender,
        );

        let handle = Synchronizer::start(
            network_client.clone(),
            context,
            core_dispatcher.clone(),
            commit_vote_monitor,
            block_verifier,
            transaction_certifier,
            dag_state,
            false,
        );

        // Create some test blocks
        let expected_blocks = (1..10)
            .map(|round| VerifiedBlock::new_for_test(TestBlock::new(round, 0).build()))
            .collect::<Vec<_>>();
        let missing_blocks = expected_blocks
            .iter()
            .map(|block| block.reference())
            .collect::<BTreeSet<_>>();

        // AND stub the fetch_blocks request from peer 1
        let peer = AuthorityIndex::new_for_test(1);
        network_client
            .stub_fetch_blocks(expected_blocks.clone(), peer, None)
            .await;

        // WHEN request missing blocks from peer 1
        assert!(handle.fetch_blocks(missing_blocks, peer).await.is_ok());

        // Wait a little bit until those have been added in core
        sleep(Duration::from_millis(1_000)).await;

        // THEN ensure those ended up in Core
        let added_blocks = core_dispatcher.get_add_blocks().await;
        assert_eq!(added_blocks, expected_blocks);
    }

    #[tokio::test]
    async fn saturate_fetch_blocks_from_peer() {
        // GIVEN
        let (context, _) = Context::new_for_test(4);
        let context = Arc::new(context);
        let block_verifier = Arc::new(NoopBlockVerifier {});
        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
        let core_dispatcher = Arc::new(MockCoreThreadDispatcher::default());
        let network_client = Arc::new(MockNetworkClient::default());
        let (blocks_sender, _blocks_receiver) =
            tokio::sync::mpsc::unbounded_channel();
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store)));
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            blocks_sender,
        );

        let handle = Synchronizer::start(
            network_client.clone(),
            context,
            core_dispatcher.clone(),
            commit_vote_monitor,
            block_verifier,
            transaction_certifier,
            dag_state,
            false,
        );

        // Create some test blocks
        let expected_blocks = (0..=2 * FETCH_BLOCKS_CONCURRENCY)
            .map(|round| VerifiedBlock::new_for_test(TestBlock::new(round as Round, 0).build()))
            .collect::<Vec<_>>();

        // Now start sending requests to fetch blocks by trying to saturate peer 1 task
        let peer = AuthorityIndex::new_for_test(1);
        let mut iter = expected_blocks.iter().peekable();
        while let Some(block) = iter.next() {
            // stub the fetch_blocks request from peer 1 and give some high response latency so requests
            // can start blocking the peer task.
            network_client
                .stub_fetch_blocks(
                    vec![block.clone()],
                    peer,
                    Some(Duration::from_millis(5_000)),
                )
                .await;

            let mut missing_blocks = BTreeSet::new();
            missing_blocks.insert(block.reference());

            // WHEN requesting to fetch the blocks, it should not succeed for the last request and get
            // an error with "saturated" synchronizer
            if iter.peek().is_none() {
                match handle.fetch_blocks(missing_blocks, peer).await {
                    Err(ConsensusError::SynchronizerSaturated(index)) => {
                        assert_eq!(index, peer);
                    }
                    _ => panic!("A saturated synchronizer error was expected"),
                }
            } else {
                assert!(handle.fetch_blocks(missing_blocks, peer).await.is_ok());
            }
        }
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    #[ignore]
    async fn synchronizer_periodic_task_fetch_blocks() {
        // GIVEN
        let (context, _) = Context::new_for_test(4);
        let context = Arc::new(context);
        let block_verifier = Arc::new(NoopBlockVerifier {});
        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
        let core_dispatcher = Arc::new(MockCoreThreadDispatcher::default());
        let network_client = Arc::new(MockNetworkClient::default());
        let (blocks_sender, _blocks_receiver) =
            tokio::sync::mpsc::unbounded_channel();
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store)));
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            blocks_sender,
        );

        // Create some test blocks
        let expected_blocks = (0..10)
            .map(|round| VerifiedBlock::new_for_test(TestBlock::new(round, 0).build()))
            .collect::<Vec<_>>();
        let missing_blocks = expected_blocks
            .iter()
            .map(|block| block.reference())
            .collect::<BTreeSet<_>>();

        // AND stub the missing blocks
        core_dispatcher
            .stub_missing_blocks(missing_blocks.clone())
            .await;

        // AND stub the requests for authority 1 & 2
        // Make the first authority timeout, so the second will be called. "We" are authority = 0, so
        // we are skipped anyways.
        network_client
            .stub_fetch_blocks(
                expected_blocks.clone(),
                AuthorityIndex::new_for_test(1),
                Some(FETCH_REQUEST_TIMEOUT),
            )
            .await;
        network_client
            .stub_fetch_blocks(
                expected_blocks.clone(),
                AuthorityIndex::new_for_test(2),
                None,
            )
            .await;

        // WHEN start the synchronizer and wait for a couple of seconds
        let _handle = Synchronizer::start(
            network_client.clone(),
            context,
            core_dispatcher.clone(),
            commit_vote_monitor,
            block_verifier,
            transaction_certifier,
            dag_state,
            false,
        );

        sleep(2 * FETCH_REQUEST_TIMEOUT).await;

        // THEN the missing blocks should now be fetched and added to core
        let added_blocks = core_dispatcher.get_add_blocks().await;
        assert_eq!(added_blocks, expected_blocks);

        // AND missing blocks should have been consumed by the stub
        assert!(core_dispatcher
            .get_missing_blocks()
            .await
            .unwrap()
            .is_empty());
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    #[ignore]
    async fn synchronizer_periodic_task_when_commit_lagging_gets_disabled() {
        // GIVEN
        let (context, _) = Context::new_for_test(4);
        let context = Arc::new(context);
        let block_verifier = Arc::new(NoopBlockVerifier {});
        let core_dispatcher = Arc::new(MockCoreThreadDispatcher::default());
        let network_client = Arc::new(MockNetworkClient::default());
        let (blocks_sender, _blocks_receiver) =
            tokio::sync::mpsc::unbounded_channel();
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store)));
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            blocks_sender,
        );
        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));

        // AND stub some missing blocks. The highest accepted round is 0. Create blocks that are above the sync threshold.
        let sync_missing_block_round_threshold = context.parameters.commit_sync_batch_size;
        let stub_blocks = (sync_missing_block_round_threshold * 2
            ..sync_missing_block_round_threshold * 3)
            .map(|round| VerifiedBlock::new_for_test(TestBlock::new(round, 0).build()))
            .collect::<Vec<_>>();
        let missing_blocks = stub_blocks
            .iter()
            .map(|block| block.reference())
            .collect::<BTreeSet<_>>();
        core_dispatcher
            .stub_missing_blocks(missing_blocks.clone())
            .await;

        // AND stub the requests for authority 1 & 2
        // Make the first authority timeout, so the second will be called. "We" are authority = 0, so
        // we are skipped anyways.
        let mut expected_blocks = stub_blocks
            .iter()
            .take(context.parameters.max_blocks_per_sync)
            .cloned()
            .collect::<Vec<_>>();
        network_client
            .stub_fetch_blocks(
                expected_blocks.clone(),
                AuthorityIndex::new_for_test(1),
                Some(FETCH_REQUEST_TIMEOUT),
            )
            .await;
        network_client
            .stub_fetch_blocks(
                expected_blocks.clone(),
                AuthorityIndex::new_for_test(2),
                None,
            )
            .await;

        // Now create some blocks to simulate a commit lag
        let round = context.parameters.commit_sync_batch_size * COMMIT_LAG_MULTIPLIER * 2;
        let commit_index: CommitIndex = round - 1;
        let blocks = (0..4)
            .map(|authority| {
                let commit_votes = vec![CommitVote::new(commit_index, CommitDigest::MIN)];
                let block = TestBlock::new(round, authority)
                    .set_commit_votes(commit_votes)
                    .build();

                VerifiedBlock::new_for_test(block)
            })
            .collect::<Vec<_>>();

        // Pass them through the commit vote monitor - so now there will be a big commit lag to prevent
        // the scheduled synchronizer from running
        for block in blocks {
            commit_vote_monitor.observe_block(&block);
        }

        // WHEN start the synchronizer and wait for a couple of seconds where normally the synchronizer should have kicked in.
        let _handle = Synchronizer::start(
            network_client.clone(),
            context.clone(),
            core_dispatcher.clone(),
            commit_vote_monitor.clone(),
            block_verifier,
            transaction_certifier,
            dag_state.clone(),
            false,
        );

        sleep(4 * FETCH_REQUEST_TIMEOUT).await;

        // Since we should be in commit lag mode none of the missed blocks should have been fetched - hence nothing should be
        // sent to core for processing.
        let added_blocks = core_dispatcher.get_add_blocks().await;
        assert_eq!(added_blocks, vec![]);

        // AND advance now the local commit index by adding a new commit that matches the commit index
        // of quorum
        {
            let mut d = dag_state.write();
            for index in 1..=commit_index {
                let commit = TrustedCommit::new_for_test(
                    index,
                    CommitDigest::MIN,
                    0,
                    BlockRef::MIN,
                    vec![],
                    index as u64,
                );

                d.add_commit(commit);
            }

            assert_eq!(
                d.last_commit_index(),
                commit_vote_monitor.quorum_commit_index()
            );
        }

        // Now stub again the missing blocks to fetch the exact same ones.
        core_dispatcher
            .stub_missing_blocks(missing_blocks.clone())
            .await;

        sleep(2 * FETCH_REQUEST_TIMEOUT).await;

        // THEN the missing blocks should now be fetched and added to core
        let mut added_blocks = core_dispatcher.get_add_blocks().await;

        added_blocks.sort_by_key(|block| block.reference());
        expected_blocks.sort_by_key(|block| block.reference());

        assert_eq!(added_blocks, expected_blocks);
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    async fn synchronizer_fetch_own_last_block() {
        // GIVEN
        let (context, _) = Context::new_for_test(4);
        let context = Arc::new(context.with_parameters(Parameters {
            sync_last_known_own_block_timeout: Duration::from_millis(2_000),
            ..Default::default()
        }));
        let block_verifier = Arc::new(NoopBlockVerifier {});
        let core_dispatcher = Arc::new(MockCoreThreadDispatcher::default());
        let network_client = Arc::new(MockNetworkClient::default());
        let (blocks_sender, _blocks_receiver) =
            tokio::sync::mpsc::unbounded_channel();
        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store)));
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            blocks_sender,
        );
        let our_index = AuthorityIndex::new_for_test(0);

        // Create some test blocks
        let mut expected_blocks = (9..=10)
            .map(|round| VerifiedBlock::new_for_test(TestBlock::new(round, 0).build()))
            .collect::<Vec<_>>();

        // Now set different latest blocks for the peers
        // For peer 1 we give the block of round 10 (highest)
        let block_1 = expected_blocks.pop().unwrap();
        network_client
            .stub_fetch_latest_blocks(
                vec![block_1.clone()],
                AuthorityIndex::new_for_test(1),
                vec![our_index],
                None,
            )
            .await;
        network_client
            .stub_fetch_latest_blocks(
                vec![block_1],
                AuthorityIndex::new_for_test(1),
                vec![our_index],
                None,
            )
            .await;

        // For peer 2 we give the block of round 9
        let block_2 = expected_blocks.pop().unwrap();
        network_client
            .stub_fetch_latest_blocks(
                vec![block_2.clone()],
                AuthorityIndex::new_for_test(2),
                vec![our_index],
                Some(Duration::from_secs(10)),
            )
            .await;
        network_client
            .stub_fetch_latest_blocks(
                vec![block_2],
                AuthorityIndex::new_for_test(2),
                vec![our_index],
                None,
            )
            .await;

        // For peer 3 we don't give any block - and it should return an empty vector
        network_client
            .stub_fetch_latest_blocks(
                vec![],
                AuthorityIndex::new_for_test(3),
                vec![our_index],
                Some(Duration::from_secs(10)),
            )
            .await;
        network_client
            .stub_fetch_latest_blocks(
                vec![],
                AuthorityIndex::new_for_test(3),
                vec![our_index],
                None,
            )
            .await;

        // WHEN start the synchronizer and wait for a couple of seconds
        let handle = Synchronizer::start(
            network_client.clone(),
            context.clone(),
            core_dispatcher.clone(),
            commit_vote_monitor,
            block_verifier,
            transaction_certifier,
            dag_state,
            true,
        );

        // Wait at least for the timeout time
        sleep(context.parameters.sync_last_known_own_block_timeout * 2).await;

        // Assert that core has been called to set the min propose round
        assert_eq!(
            core_dispatcher.get_last_own_proposed_round().await,
            vec![10]
        );

        // Ensure that all the requests have been called
        assert_eq!(network_client.fetch_latest_blocks_pending_calls().await, 0);

        // And we got one retry
        assert_eq!(
            context
                .metrics
                .node_metrics
                .sync_last_known_own_block_retries
                .get(),
            1
        );

        // Ensure that no panic occurred
        if let Err(err) = handle.stop().await {
            if err.is_panic() {
                std::panic::resume_unwind(err.into_panic());
            }
        }
    }

    #[tokio::test]
    async fn test_process_fetched_blocks() {
        // GIVEN
        let (context, _) = Context::new_for_test(4);
        let context = Arc::new(context);
        let block_verifier = Arc::new(NoopBlockVerifier {});
        let core_dispatcher = Arc::new(MockCoreThreadDispatcher::default());
        let (blocks_sender, _blocks_receiver) =
            tokio::sync::mpsc::unbounded_channel();
        let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store)));
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            block_verifier.clone(),
            dag_state.clone(),
            blocks_sender,
        );
        let (commands_sender, _commands_receiver) =
            tokio::sync::mpsc::channel(1000);

        // Create input test blocks:
        // - Authority 0 block at round 60.
        // - Authority 1 blocks from round 30 to 60.
        let mut expected_blocks = vec![VerifiedBlock::new_for_test(TestBlock::new(60, 0).build())];
        expected_blocks.extend(
            (30..=60).map(|round| VerifiedBlock::new_for_test(TestBlock::new(round, 1).build())),
        );
        assert_eq!(
            expected_blocks.len(),
            context.parameters.max_blocks_per_sync
        );

        let expected_serialized_blocks = expected_blocks
            .iter()
            .map(|b| b.serialized().clone())
            .collect::<Vec<_>>();

        let expected_block_refs = expected_blocks
            .iter()
            .map(|b| b.reference())
            .collect::<BTreeSet<_>>();

        // GIVEN peer to fetch blocks from
        let peer_index = AuthorityIndex::new_for_test(2);

        // Create blocks_guard
        let inflight_blocks_map = InflightBlocksMap::new();
        let blocks_guard = inflight_blocks_map
            .lock_blocks(expected_block_refs.clone(), peer_index)
            .expect("Failed to lock blocks");

        assert_eq!(
            inflight_blocks_map.num_of_locked_blocks(),
            expected_block_refs.len()
        );

        // Create a Synchronizer
        let network_client = Arc::new(MockNetworkClient::default());
        let result = Synchronizer::<
            MockNetworkClient,
            NoopBlockVerifier,
            MockCoreThreadDispatcher,
        >::process_fetched_blocks(
            expected_serialized_blocks,
            peer_index,
            blocks_guard, // The guard is consumed here
            core_dispatcher.clone(),
            block_verifier,
            transaction_certifier,
            commit_vote_monitor,
            context.clone(),
            commands_sender,
            dag_state.clone(),
            "test",
            network_client,
        )
        .await;

        // THEN
        assert!(result.is_ok());

        // Check blocks were sent to core
        let added_blocks = core_dispatcher.get_add_blocks().await;
        assert_eq!(
            added_blocks
                .iter()
                .map(|b| b.reference())
                .collect::<BTreeSet<_>>(),
            expected_block_refs,
        );

        // Check blocks were unlocked
        assert_eq!(inflight_blocks_map.num_of_locked_blocks(), 0);
    }
}
