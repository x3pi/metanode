// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0
use std::{
    sync::Arc,
    time::Duration,
};

use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::Round;
use futures::{stream::FuturesUnordered, StreamExt as _};
use itertools::Itertools as _;
use meta_macros::fail_point_async;
use mysten_metrics::monitored_mpsc::{Receiver, Sender};
use parking_lot::RwLock;

use tokio::{
    sync::mpsc::error::TrySendError,
    time::{sleep_until, timeout, Instant},
};
use tracing::{debug, info, warn};

use crate::core_thread::CoreThreadDispatcher;
use crate::{
    block::BlockAPI,
    block_verifier::BlockVerifier,
    commit_vote_monitor::CommitVoteMonitor,
    context::Context,
    dag_state::DagState,
    error::{ConsensusError, ConsensusResult},
    network::NetworkClient,
    transaction_certifier::TransactionCertifier,
};


use super::{
    Synchronizer, BlocksGuard, Command, FETCH_REQUEST_TIMEOUT,
    FETCH_BLOCKS_CONCURRENCY,
};

impl<C: NetworkClient, V: BlockVerifier, D: CoreThreadDispatcher> Synchronizer<C, V, D> {
    #[allow(clippy::too_many_arguments)]
    pub(super) async fn fetch_blocks_from_authority(
        peer_index: AuthorityIndex,
        network_client: Arc<C>,
        block_verifier: Arc<V>,
        transaction_certifier: TransactionCertifier,
        commit_vote_monitor: Arc<CommitVoteMonitor>,
        context: Arc<Context>,
        core_dispatcher: Arc<D>,
        dag_state: Arc<RwLock<DagState>>,
        mut receiver: Receiver<BlocksGuard>,
        commands_sender: Sender<Command>,
    ) {
        const MAX_RETRIES: u32 = 3;
        let peer_hostname = &context.committee.authority(peer_index).hostname;
        let mut requests = FuturesUnordered::new();

        loop {
            tokio::select! {
                Some(blocks_guard) = receiver.recv(), if requests.len() < FETCH_BLOCKS_CONCURRENCY => {
                    // get the highest accepted rounds
                    let highest_rounds = Self::get_highest_accepted_rounds(dag_state.clone(), &context);

                    requests.push(Self::fetch_blocks_request(network_client.clone(), peer_index, blocks_guard, highest_rounds, true, FETCH_REQUEST_TIMEOUT, 1))
                },
                Some((response, blocks_guard, retries, _peer, highest_rounds)) = requests.next() => {
                    match response {
                        Ok(blocks) => {
                            if let Err(err) = Self::process_fetched_blocks(blocks,
                                peer_index,
                                blocks_guard,
                                core_dispatcher.clone(),
                                block_verifier.clone(),
                                transaction_certifier.clone(),
                                commit_vote_monitor.clone(),
                                context.clone(),
                                commands_sender.clone(),
                                dag_state.clone(),
                                "live"
                            ).await {
                                warn!("Error while processing fetched blocks from peer {peer_index} {peer_hostname}: {err}");
                                context.metrics.node_metrics.synchronizer_process_fetched_failures.with_label_values(&[peer_hostname, "live"]).inc();
                            }
                        },
                        Err(_) => {
                            context.metrics.node_metrics.synchronizer_fetch_failures.with_label_values(&[peer_hostname, "live"]).inc();
                            if retries <= MAX_RETRIES {
                                requests.push(Self::fetch_blocks_request(network_client.clone(), peer_index, blocks_guard, highest_rounds, true, FETCH_REQUEST_TIMEOUT, retries))
                            } else {
                                warn!("Max retries {retries} reached while trying to fetch blocks from peer {peer_index} {peer_hostname}.");
                                // we don't necessarily need to do, but dropping the guard here to unlock the blocks
                                drop(blocks_guard);
                            }
                        }
                    }
                },
                else => {
                    info!("Fetching blocks from authority {peer_index} task will now abort.");
                    break;
                }
            }
        }
    }

    /// Processes the requested raw fetched blocks from peer `peer_index`. If no error is returned then
    /// the verified blocks are immediately sent to Core for processing.

    #[allow(clippy::too_many_arguments)]
    pub(super) async fn process_fetched_blocks(
        mut serialized_blocks: Vec<Bytes>,
        peer_index: AuthorityIndex,
        requested_blocks_guard: BlocksGuard,
        core_dispatcher: Arc<D>,
        block_verifier: Arc<V>,
        transaction_certifier: TransactionCertifier,
        commit_vote_monitor: Arc<CommitVoteMonitor>,
        context: Arc<Context>,
        commands_sender: Sender<Command>,
        dag_state: Arc<RwLock<DagState>>,
        sync_method: &str,
    ) -> ConsensusResult<()> {
        if serialized_blocks.is_empty() {
            return Ok(());
        }

        // Limit the number of the returned blocks processed.
        serialized_blocks.truncate(context.parameters.max_blocks_per_sync);

        // Verify all the fetched blocks in parallel chunks
        let chunk_size = std::cmp::max(1, context.parameters.max_blocks_per_sync / 4.max(1));
        let mut verification_tasks = Vec::new();
        for chunk in serialized_blocks.chunks(chunk_size) {
            let chunk = chunk.to_vec();
            let block_verifier = block_verifier.clone();
            let context = context.clone();
            let dag_state = dag_state.clone();
            let transaction_certifier = transaction_certifier.clone();
            
            verification_tasks.push(tokio::task::spawn_blocking(move || {
                Self::verify_blocks(
                    chunk,
                    block_verifier,
                    transaction_certifier,
                    &context,
                    peer_index,
                    dag_state,
                )
            }));
        }

        let mut blocks = Vec::new();
        for result in futures::future::join_all(verification_tasks).await {
            let verified_chunk = match result.expect("Spawn blocking should not fail") {
                Ok(chunk) => chunk,
                Err(ConsensusError::WrongEpoch { expected, actual }) => {
                    if actual > expected {
                        commit_vote_monitor.observe_highest_seen_epoch(actual);
                    }
                    return Err(ConsensusError::WrongEpoch { expected, actual });
                }
                Err(e) => return Err(e),
            };
            blocks.extend(verified_chunk);
        }

        // Record commit votes from the verified blocks.
        for block in &blocks {
            commit_vote_monitor.observe_block(block);
        }

        let metrics = &context.metrics.node_metrics;
        let peer_hostname = &context.committee.authority(peer_index).hostname;
        metrics
            .synchronizer_fetched_blocks_by_peer
            .with_label_values(&[peer_hostname, sync_method])
            .inc_by(blocks.len() as u64);
        for block in &blocks {
            let block_hostname = &context.committee.authority(block.author()).hostname;
            metrics
                .synchronizer_fetched_blocks_by_authority
                .with_label_values(&[block_hostname, sync_method])
                .inc();
        }

        debug!(
            "Synced {} missing blocks from peer {peer_index} {peer_hostname}: {}",
            blocks.len(),
            blocks.iter().map(|b| b.reference().to_string()).join(", "),
        );

        // Now send them to core for processing. Ignore the returned missing blocks as we don't want
        // this mechanism to keep feedback looping on fetching more blocks. The periodic synchronization
        // will take care of that.
        let missing_blocks = core_dispatcher
            .add_blocks(blocks)
            .await
            .map_err(|_| ConsensusError::Shutdown)?;

        // now release all the locked blocks as they have been fetched, verified & processed
        drop(requested_blocks_guard);

        // kick off immediately the scheduled synchronizer
        if !missing_blocks.is_empty() {
            // do not block here, so we avoid any possible cycles.
            if let Err(TrySendError::Full(_)) = commands_sender.try_send(Command::KickOffScheduler)
            {
                warn!("Commands channel is full")
            }
        }

        context
            .metrics
            .node_metrics
            .missing_blocks_after_fetch_total
            .inc_by(missing_blocks.len() as u64);

        Ok(())
    }


    pub(super) async fn fetch_blocks_request(
        network_client: Arc<C>,
        peer: AuthorityIndex,
        blocks_guard: BlocksGuard,
        highest_rounds: Vec<Round>,
        breadth_first: bool,
        request_timeout: Duration,
        mut retries: u32,
    ) -> (
        ConsensusResult<Vec<Bytes>>,
        BlocksGuard,
        u32,
        AuthorityIndex,
        Vec<Round>,
    ) {
        let start = Instant::now();
        let resp = timeout(
            request_timeout,
            network_client.fetch_blocks(
                peer,
                blocks_guard
                    .block_refs
                    .clone()
                    .into_iter()
                    .collect::<Vec<_>>(),
                highest_rounds.clone().into_iter().collect::<Vec<_>>(),
                breadth_first,
                request_timeout,
            ),
        )
        .await;

        fail_point_async!("consensus-delay");

        let resp = match resp {
            Ok(Err(err)) => {
                // Add a delay before retrying - if that is needed. If request has timed out then eventually
                // this will be a no-op.
                sleep_until(start + request_timeout).await;
                retries += 1;
                Err(err)
            } // network error
            Err(err) => {
                // timeout
                sleep_until(start + request_timeout).await;
                retries += 1;
                Err(ConsensusError::NetworkRequestTimeout(err.to_string()))
            }
            Ok(result) => result,
        };
        (resp, blocks_guard, retries, peer, highest_rounds)
    }

}
