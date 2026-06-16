// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0
use std::{
    collections::{BTreeMap, BTreeSet},
    sync::{
        atomic::Ordering,
        Arc,
    },
    time::Duration,
};

use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::BlockRef;
use futures::{stream::FuturesUnordered, StreamExt as _};
use meta_macros::fail_point_async;
use parking_lot::RwLock;
use rand::{prelude::SliceRandom as _, rngs::ThreadRng};

use tokio::time::{sleep, sleep_until, Instant};
use tracing::{debug, info, trace, warn};

use crate::core_thread::CoreThreadDispatcher;
use crate::{
    block::{BlockAPI, SignedBlock, VerifiedBlock},
    block_verifier::BlockVerifier,
    context::Context,
    dag_state::DagState,
    error::{ConsensusError, ConsensusResult},
    network::NetworkClient,
};


use super::{
    Synchronizer, BlocksGuard, InflightBlocksMap,
    FETCH_REQUEST_TIMEOUT, FETCH_FROM_PEERS_TIMEOUT, MAX_PERIODIC_SYNC_PEERS,
};

impl<C: NetworkClient, V: BlockVerifier, D: CoreThreadDispatcher> Synchronizer<C, V, D> {
    pub(super) fn start_fetch_own_last_block_task(&mut self) {
        const FETCH_OWN_BLOCK_RETRY_DELAY: Duration = Duration::from_millis(1_000);
        const MAX_RETRY_DELAY_STEP: Duration = Duration::from_millis(4_000);

        let context = self.context.clone();
        let dag_state = self.dag_state.clone();
        let network_client = self.network_client.clone();
        let block_verifier = self.block_verifier.clone();
        let core_dispatcher = self.core_dispatcher.clone();

        self.fetch_own_last_block_task
            .spawn(async move {
                /* let _scope = tracing::info_span!("FetchOwnLastBlockTask").entered(); */

                let fetch_own_block = |authority_index: AuthorityIndex, fetch_own_block_delay: Duration| {
                    let network_client_cloned = network_client.clone();
                    let own_index = context.own_index;
                    async move {
                        sleep(fetch_own_block_delay).await;
                        let r = network_client_cloned.fetch_latest_blocks(authority_index, vec![own_index], FETCH_REQUEST_TIMEOUT).await;
                        (r, authority_index)
                    }
                };

                let process_blocks = |blocks: Vec<Bytes>, authority_index: AuthorityIndex| -> ConsensusResult<Vec<VerifiedBlock>> {
                    let mut result = Vec::new();
                    for serialized_block in blocks {
                        let signed_block: SignedBlock = bcs::from_bytes(&serialized_block).map_err(ConsensusError::MalformedBlock)?;
                        let verified_block = match block_verifier.verify_and_vote(signed_block.clone(), serialized_block.clone()) {
                            Ok((block, _)) => block,
                            Err(ConsensusError::WrongEpoch { expected, actual }) if actual < expected => {
                                // CROSS-EPOCH FIX: After snapshot restore to epoch N, peers
                                // may return our last block from epoch N-1. We only care about
                                // the round number here, so cross-epoch verification is safe.
                                info!(
                                    "Cross-epoch own block from peer {} (expected epoch {}, got {}). \
                                     Using commit-sync verification.",
                                    authority_index, expected, actual
                                );
                                match block_verifier.verify_for_commit_sync(signed_block, serialized_block) {
                                    Ok((block, _)) => block,
                                    Err(e) => {
                                        let hostname = context.committee.authority(authority_index).hostname.clone();
                                        context
                                            .metrics
                                            .node_metrics
                                            .invalid_blocks
                                            .with_label_values(&[&hostname, "synchronizer_own_block_cross_epoch", e.clone().name()])
                                            .inc();
                                        warn!("Invalid cross-epoch own block from {}: {}", authority_index, e);
                                        return Err(e);
                                    }
                                }
                            }
                            Err(err) => {
                                let hostname = context.committee.authority(authority_index).hostname.clone();
                                context
                                    .metrics
                                    .node_metrics
                                    .invalid_blocks
                                    .with_label_values(&[&hostname, "synchronizer_own_block", err.clone().name()])
                                    .inc();
                                warn!("Invalid block received from {}: {}", authority_index, err);
                                return Err(err);
                            }
                        };

                        if verified_block.author() != context.own_index {
                            return Err(ConsensusError::UnexpectedLastOwnBlock { index: authority_index, block_ref: verified_block.reference()});
                        }
                        result.push(verified_block);
                    }
                    Ok(result)
                };

                // Get the highest of all the results. Retry until at least `f+1` results have been gathered.
                let mut highest_round;
                let mut retries = 0;
                let mut retry_delay_step = Duration::from_millis(500);
                'main:loop {
                    if context.committee.size() == 1 {
                        highest_round = dag_state.read().get_last_proposed_block().round();
                        info!("Only one node in the network, will not try fetching own last block from peers.");
                        break 'main;
                    }

                    let mut total_stake = 0;
                    highest_round = 0;

                    // Ask all the other peers about our last block
                    let mut results = FuturesUnordered::new();

                    for (authority_index, _authority) in context.committee.authorities() {
                        if authority_index != context.own_index {
                            results.push(fetch_own_block(authority_index, Duration::from_millis(0)));
                        }
                    }

                    // Gather the results but wait to timeout as well
                    let timer = sleep_until(Instant::now() + context.parameters.sync_last_known_own_block_timeout);
                    tokio::pin!(timer);

                    'inner: loop {
                        tokio::select! {
                            result = results.next() => {
                                let Some((result, authority_index)) = result else {
                                    break 'inner;
                                };
                                match result {
                                    Ok(result) => {
                                        match process_blocks(result, authority_index) {
                                            Ok(blocks) => {
                                                let max_round = blocks.into_iter().map(|b|b.round()).max().unwrap_or(0);
                                                highest_round = highest_round.max(max_round);

                                                total_stake += context.committee.stake(authority_index);
                                            },
                                            Err(err) => {
                                                warn!("Invalid result returned from {authority_index} while fetching last own block: {err}");
                                            }
                                        }
                                    },
                                    Err(err) => {
                                        // During startup (or right after epoch restart) peers may not have their network
                                        // service ready yet, which results in transient connect errors.
                                        // Keep this at DEBUG to avoid alarming operators; the retry loop will handle it.
                                        debug!(
                                            "Error {err} while fetching our own block from peer {authority_index}. Will retry."
                                        );
                                        results.push(fetch_own_block(authority_index, FETCH_OWN_BLOCK_RETRY_DELAY));
                                    }
                                }
                            },
                            () = &mut timer => {
                                info!("Timeout while trying to sync our own last block from peers");
                                break 'inner;
                            }
                        }
                    }

                    // Request at least f+1 stake to have replied back.
                    if context.committee.reached_validity(total_stake) {
                        info!("{} out of {} total stake returned acceptable results for our own last block with highest round {}, with {retries} retries.", total_stake, context.committee.total_stake(), highest_round);
                        break 'main;
                    }

                    retries += 1;
                    context.metrics.node_metrics.sync_last_known_own_block_retries.inc();
                    warn!("Not enough stake: {} out of {} total stake returned acceptable results for our own last block with highest round {}. Will now retry {retries}.", total_stake, context.committee.total_stake(), highest_round);

                    sleep(retry_delay_step).await;

                    retry_delay_step = Duration::from_secs_f64(retry_delay_step.as_secs_f64() * 1.5);
                    retry_delay_step = retry_delay_step.min(MAX_RETRY_DELAY_STEP);
                }

                // Update the Core with the highest detected round
                context.metrics.node_metrics.last_known_own_block_round.set(highest_round as i64);

                if let Err(err) = core_dispatcher.set_last_known_proposed_round(highest_round) {
                    warn!("Error received while calling dispatcher, probably dispatcher is shutting down, will now exit: {err:?}");
                }
            });
    }


    pub(super) async fn start_fetch_missing_blocks_task(&mut self) -> ConsensusResult<()> {
        if self.context.committee.size() == 1 {
            trace!(
                "Only one node in the network, will not try fetching missing blocks from peers."
            );
            return Ok(());
        }

        let missing_blocks = self
            .core_dispatcher
            .get_missing_blocks()
            .await
            .map_err(|_err| ConsensusError::Shutdown)?;

        // No reason to kick off the scheduler if there are no missing blocks to fetch
        if missing_blocks.is_empty() {
            return Ok(());
        }

        let context = self.context.clone();
        let network_client = self.network_client.clone();
        let block_verifier = self.block_verifier.clone();
        let transaction_certifier = self.transaction_certifier.clone();
        let commit_vote_monitor = self.commit_vote_monitor.clone();
        let core_dispatcher = self.core_dispatcher.clone();
        let blocks_to_fetch = self.inflight_blocks_map.clone();
        let commands_sender = self.commands_sender.clone();
        let dag_state = self.dag_state.clone();
        let consecutive_sync_failures = self.consecutive_sync_failures.clone();

        // If we are commit lagging, then we don't want to enable the scheduler. As the node is sycnhronizing via the commit syncer, the certified commits
        // will bring all the necessary blocks to run the commits. As the commits are certified, we are guaranteed that all the necessary causal history is present.
        if self.is_commit_lagging() {
            return Ok(());
        }

        self.fetch_blocks_scheduler_task
            .spawn(async move {
                /* let _scope = tracing::info_span!("FetchMissingBlocksScheduler").entered(); */
                context
                    .metrics
                    .node_metrics
                    .fetch_blocks_scheduler_inflight
                    .inc();
                let total_requested = missing_blocks.len();

                fail_point_async!("consensus-delay");
                
                let (tx, mut rx) = tokio::sync::mpsc::channel(100);
                let fetch_context = context.clone();
                let fetch_network_client = network_client.clone();
                let fetch_dag_state = dag_state.clone();
                let fetch_blocks_to_fetch = blocks_to_fetch.clone();
                
                // Fetch blocks from peers concurrently
                tokio::spawn(async move {
                    Self::fetch_blocks_from_authorities(
                        fetch_context,
                        fetch_blocks_to_fetch,
                        fetch_network_client,
                        missing_blocks,
                        fetch_dag_state,
                        tx,
                    )
                    .await;
                });

                // Now process the returned results immediately as they stream in
                let mut total_fetched = 0;
                let mut any_success = false;
                while let Some((blocks_guard, fetched_blocks, peer)) = rx.recv().await {
                    total_fetched += fetched_blocks.len();

                    if let Err(err) = Self::process_fetched_blocks(
                        fetched_blocks,
                        peer,
                        blocks_guard,
                        core_dispatcher.clone(),
                        block_verifier.clone(),
                        transaction_certifier.clone(),
                        commit_vote_monitor.clone(),
                        context.clone(),
                        commands_sender.clone(),
                        dag_state.clone(),
                        "periodic",
                    )
                    .await
                    {
                        warn!(
                            "Error occurred while processing fetched blocks from peer {peer}: {err}"
                        );
                        context
                            .metrics
                            .node_metrics
                            .synchronizer_process_fetched_failures
                            .with_label_values(&[
                                &context.committee.authority(peer).hostname,
                                "periodic",
                            ])
                            .inc();
                    } else {
                        any_success = true;
                    }
                }

                context
                    .metrics
                    .node_metrics
                    .fetch_blocks_scheduler_inflight
                    .dec();

                // Track consecutive failures for exponential backoff.
                // When all syncs fail (e.g., lagging node fetching wrong-epoch blocks),
                // increase backoff to avoid spamming peers.
                if any_success {
                    consecutive_sync_failures.store(0, Ordering::Relaxed);
                } else {
                    let prev = consecutive_sync_failures.fetch_add(1, Ordering::Relaxed);
                    if prev < 5 {
                        info!(
                            "Periodic sync: all peers returned errors ({} consecutive failures). \
                             Backing off to reduce load.",
                            prev + 1
                        );
                    }
                }

                debug!(
                    "Total blocks requested to fetch: {}, total fetched: {}",
                    total_requested, total_fetched
                );
            });
        Ok(())
    }


    /// Fetches the `missing_blocks` from peers. Requests the same number of authorities with missing blocks from each peer.
    /// Each response from peer can contain the requested blocks, and additional blocks from the last accepted round for
    /// authorities with missing blocks.
    /// Each element of the vector is a tuple which contains the requested missing block refs, the returned blocks and
    /// the peer authority index.
    pub(super) async fn fetch_blocks_from_authorities(
        context: Arc<Context>,
        inflight_blocks: Arc<InflightBlocksMap>,
        network_client: Arc<C>,
        missing_blocks: BTreeSet<BlockRef>,
        dag_state: Arc<RwLock<DagState>>,
        tx_process: tokio::sync::mpsc::Sender<(BlocksGuard, Vec<Bytes>, AuthorityIndex)>,
    ) {
        // Preliminary truncation of missing blocks to fetch. Since each peer can have different
        // number of missing blocks and the fetching is batched by peer, so keep more than max_blocks_per_sync
        // per peer on average.
        let missing_blocks = missing_blocks
            .into_iter()
            .take(2 * MAX_PERIODIC_SYNC_PEERS * context.parameters.max_blocks_per_sync)
            .collect::<Vec<_>>();

        // Maps authorities to the missing blocks they have.
        let mut authorities = BTreeMap::<AuthorityIndex, Vec<BlockRef>>::new();
        for block_ref in &missing_blocks {
            authorities
                .entry(block_ref.author)
                .or_default()
                .push(*block_ref);
        }
        // Distribute the same number of authorities into each peer to sync.
        // When running this function, context.committee.size() is always greater than 1.
        let num_authorities_per_peer = authorities
            .len()
            .div_ceil((context.committee.size() - 1).min(MAX_PERIODIC_SYNC_PEERS));

        // Update metrics related to missing blocks.
        let mut missing_blocks_per_authority = vec![0; context.committee.size()];
        for (authority, blocks) in &authorities {
            missing_blocks_per_authority[*authority] += blocks.len();
        }
        for (missing, (_, authority)) in missing_blocks_per_authority
            .into_iter()
            .zip(context.committee.authorities())
        {
            context
                .metrics
                .node_metrics
                .synchronizer_missing_blocks_by_authority
                .with_label_values(&[&authority.hostname])
                .inc_by(missing as u64);
            context
                .metrics
                .node_metrics
                .synchronizer_current_missing_blocks_by_authority
                .with_label_values(&[&authority.hostname])
                .set(missing as i64);
        }

        let mut peers = context
            .committee
            .authorities()
            .filter_map(|(peer_index, _)| (peer_index != context.own_index).then_some(peer_index))
            .collect::<Vec<_>>();

        // TODO: probably inject the RNG to allow unit testing - this is a work around for now.
        if cfg!(not(test)) {
            // Shuffle the peers
            peers.shuffle(&mut ThreadRng::default());
        }

        let mut peers = peers.into_iter();
        let mut request_futures = FuturesUnordered::new();

        let highest_rounds = Self::get_highest_accepted_rounds(dag_state, &context);

        // Shuffle the authorities for each request.
        let mut authorities = authorities.into_values().collect::<Vec<_>>();
        if cfg!(not(test)) {
            // Shuffle the authorities
            authorities.shuffle(&mut ThreadRng::default());
        }

        // Send the fetch requests
        for batch in authorities.chunks(num_authorities_per_peer) {
            let Some(peer) = peers.next() else {
                panic!("No more peers left to fetch blocks!");
            };
            let peer_hostname = &context.committee.authority(peer).hostname;
            // Fetch from the lowest round missing blocks to ensure progress.
            // This may reduce efficiency and increase the chance of duplicated data transfer in edge cases.
            let block_refs = batch
                .iter()
                .flatten()
                .cloned()
                .collect::<BTreeSet<_>>()
                .into_iter()
                .take(context.parameters.max_blocks_per_sync)
                .collect::<BTreeSet<_>>();

            // lock the blocks to be fetched. If no lock can be acquired for any of the blocks then don't bother
            if let Some(blocks_guard) = inflight_blocks.lock_blocks(block_refs.clone(), peer) {
                info!(
                    "Periodic sync of {} missing blocks from peer {} {}: {}",
                    block_refs.len(),
                    peer,
                    peer_hostname,
                    block_refs
                        .iter()
                        .map(|b| b.to_string())
                        .collect::<Vec<_>>()
                        .join(", ")
                );
                request_futures.push(Self::fetch_blocks_request(
                    network_client.clone(),
                    peer,
                    blocks_guard,
                    highest_rounds.clone(),
                    false,
                    FETCH_REQUEST_TIMEOUT,
                    1,
                ));
            }
        }

        let fetcher_timeout = sleep(FETCH_FROM_PEERS_TIMEOUT);

        tokio::pin!(fetcher_timeout);

        loop {
            tokio::select! {
                Some((response, blocks_guard, _retries, peer_index, highest_rounds)) = request_futures.next() => {
                    let peer_hostname = &context.committee.authority(peer_index).hostname;
                    match response {
                        Ok(fetched_blocks) => {
                            let _ = tx_process.send((blocks_guard, fetched_blocks, peer_index)).await;

                            // no more pending requests are left, just break the loop
                            if request_futures.is_empty() {
                                break;
                            }
                        },
                        Err(_) => {
                            context.metrics.node_metrics.synchronizer_fetch_failures.with_label_values(&[peer_hostname, "periodic"]).inc();
                            // try again if there is any peer left
                            if let Some(next_peer) = peers.next() {
                                // do best effort to lock guards. If we can't lock then don't bother at this run.
                                if let Some(blocks_guard) = inflight_blocks.swap_locks(blocks_guard, next_peer) {
                                    info!(
                                        "Retrying syncing {} missing blocks from peer {}: {}",
                                        blocks_guard.block_refs.len(),
                                        peer_hostname,
                                        blocks_guard.block_refs
                                            .iter()
                                            .map(|b| b.to_string())
                                            .collect::<Vec<_>>()
                                            .join(", ")
                                    );
                                    request_futures.push(Self::fetch_blocks_request(
                                        network_client.clone(),
                                        next_peer,
                                        blocks_guard,
                                        highest_rounds,
                                        false,
                                        FETCH_REQUEST_TIMEOUT,
                                        1,
                                    ));
                                } else {
                                    debug!("Couldn't acquire locks to fetch blocks from peer {next_peer}.")
                                }
                            } else {
                                debug!("No more peers left to fetch blocks");
                            }
                        }
                    }
                },
                _ = &mut fetcher_timeout => {
                    debug!("Timed out while fetching missing blocks");
                    break;
                }
            }
        }
    }

}
