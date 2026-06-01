// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::BTreeMap,
    sync::Arc,
    time::Duration,
};

use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::BlockRef;
use futures::{stream::FuturesOrdered, StreamExt as _};
use itertools::Itertools as _;
use rand::{prelude::SliceRandom as _, rngs::ThreadRng};
use tokio::{runtime::Handle, time::sleep};
use tracing::{info, warn};

use super::{CommitSyncer, Inner};
use crate::{
    block::{BlockAPI, SignedBlock, VerifiedBlock},
    commit::{CertifiedCommit, CertifiedCommits, CommitAPI as _, CommitRange},
    error::{ConsensusError, ConsensusResult},
    network::NetworkClient,
    CommitIndex,
};

impl<C: NetworkClient> CommitSyncer<C> {
    pub(super) fn try_start_fetches(&mut self) {
        // Cap parallel fetches based on configured limit and committee size, to avoid overloading the network.
        // Also when there are too many fetched blocks that cannot be sent to Core before an earlier fetch
        // has not finished, reduce parallelism so the earlier fetch can retry on a better host and succeed.
        // STATE MACHINE: Adjust parallelism based on sync state
        let base_parallel_fetches = self.inner.context.parameters.commit_sync_parallel_fetches;
        let effective_parallel_fetches = if self.coordination_hub.is_catching_up() {
            // Turbo: 3x parallel fetches for catching up
            (base_parallel_fetches * 3)
                .min(self.inner.context.committee.size())
        } else {
            base_parallel_fetches
        };

        let effective_batches_ahead = if self.coordination_hub.is_catching_up() {
            self.inner.context.parameters.commit_sync_batches_ahead * 3
        } else {
            self.inner.context.parameters.commit_sync_batches_ahead
        };

        // In turbo mode, allow fetching from all peers (not just 2/3)
        let committee_cap = if self.coordination_hub.is_catching_up() {
            self.inner.context.committee.size()
        } else {
            self.inner.context.committee.size() * 2 / 3
        };

        let target_parallel_fetches = effective_parallel_fetches
            .min(committee_cap)
            .min(
                effective_batches_ahead
                    .saturating_sub(self.fetched_ranges.len()),
            );
        // Start new fetches if there are pending batches and available slots.
        loop {
            if self.inflight_fetches.len() >= target_parallel_fetches {
                break;
            }
            let Some(commit_range) = self.pending_fetches.pop_first() else {
                break;
            };
            self.inflight_fetches
                .spawn(Self::fetch_loop(
                    self.inner.clone(),
                    commit_range,
                    self.coordination_hub.is_catching_up(), // is_severe_lag
                    self.coordination_hub.is_catching_up(), // is_sync_mode
                ));
        }

        let metrics = &self.inner.context.metrics.node_metrics;
        metrics
            .commit_sync_inflight_fetches
            .set(self.inflight_fetches.len() as i64);
        metrics
            .commit_sync_pending_fetches
            .set(self.pending_fetches.len() as i64);
        metrics
            .commit_sync_highest_synced_index
            .set(self.synced_commit_index as i64);
    }

    // Retries fetching commits and blocks from available authorities, until a request succeeds
    // where at least a prefix of the commit range is fetched.
    // Returns the fetched commits and blocks referenced by the commits.
    async fn fetch_loop(
        inner: Arc<Inner<C>>,
        commit_range: CommitRange,
        is_severe_lag: bool,
        is_sync_mode: bool,
    ) -> (CommitIndex, CertifiedCommits) {
        // Individual request base timeout.
        const TIMEOUT: Duration = Duration::from_secs(5);
        // Max per-request timeout will be base timeout times a multiplier.
        // At the extreme, this means there will be 120s timeout to fetch max_blocks_per_fetch blocks.
        const MAX_TIMEOUT_MULTIPLIER: u32 = 12;
        // timeout * max number of targets should be reasonably small, so the
        // system can adjust to slow network or large data sizes quickly.
        const MAX_NUM_TARGETS: usize = 24;
        let mut timeout_multiplier = 0;
        let _timer = inner
            .context
            .metrics
            .node_metrics
            .commit_sync_fetch_loop_latency
            .start_timer();
        info!("Starting to fetch commits in {commit_range:?} ...",);
        loop {
            // Attempt to fetch commits and blocks through min(committee size, MAX_NUM_TARGETS) peers.
            let mut target_authorities = inner
                .context
                .committee
                .authorities()
                .filter_map(|(i, _)| {
                    if i != inner.context.own_index {
                        Some(i)
                    } else {
                        None
                    }
                })
                .collect_vec();
            target_authorities.shuffle(&mut ThreadRng::default());
            target_authorities.truncate(MAX_NUM_TARGETS);
            // Increase timeout multiplier for each loop until MAX_TIMEOUT_MULTIPLIER.
            timeout_multiplier = (timeout_multiplier + 1).min(MAX_TIMEOUT_MULTIPLIER);
            let request_timeout = TIMEOUT * timeout_multiplier;
            // Give enough overall timeout for fetching commits and blocks.
            // - Timeout for fetching commits and commit certifying blocks.
            // - Timeout for fetching blocks referenced by the commits.
            // - Time spent on pipelining requests to fetch blocks.
            // - Another headroom to allow fetch_once() to timeout gracefully if possible.
            let fetch_timeout = request_timeout * 4;
            // Try fetching from selected target authority.
            for authority in target_authorities {
                match tokio::time::timeout(
                    fetch_timeout,
                    Self::fetch_once(
                        inner.clone(),
                        authority,
                        commit_range.clone(),
                        request_timeout,
                        is_severe_lag,
                    ),
                )
                .await
                {
                    Ok(Ok(commits)) => {
                        info!("Finished fetching commits in {commit_range:?}");
                        return (commit_range.end(), commits);
                    }
                    Ok(Err(e)) => {
                        let hostname = inner
                            .context
                            .committee
                            .authority(authority)
                            .hostname
                            .clone();
                        warn!("Failed to fetch {commit_range:?} from {hostname}: {}", e);
                        inner
                            .context
                            .metrics
                            .node_metrics
                            .commit_sync_fetch_once_errors
                            .with_label_values(&[&hostname, e.name()])
                            .inc();
                    }
                    Err(_) => {
                        let hostname = inner
                            .context
                            .committee
                            .authority(authority)
                            .hostname
                            .clone();
                        warn!("Timed out fetching {commit_range:?} from {authority}",);
                        inner
                            .context
                            .metrics
                            .node_metrics
                            .commit_sync_fetch_once_errors
                            .with_label_values(&[&hostname, "FetchTimeout"])
                            .inc();
                    }
                }
            }
            // Avoid busy looping, by waiting briefly before retrying (reduced for faster catch-up).
            let retry_delay = if is_severe_lag {
                Duration::from_millis(500)
            } else if is_sync_mode {
                Duration::from_millis(1000)
            } else {
                Duration::from_secs(2)
            };
            sleep(retry_delay).await;
        }
    }

    // Fetches commits and blocks from a single authority. At a high level, first the commits are
    // fetched and verified. After that, blocks referenced in the certified commits are fetched
    // and sent to Core for processing.
    async fn fetch_once(
        inner: Arc<Inner<C>>,
        target_authority: AuthorityIndex,
        mut commit_range: CommitRange,
        timeout: Duration,
        is_severe_lag: bool,
    ) -> ConsensusResult<CertifiedCommits> {
        let _timer = inner
            .context
            .metrics
            .node_metrics
            .commit_sync_fetch_once_latency
            .start_timer();

        // 1. Query peer epoch status to proactively truncate cross-epoch fetches.
        // This prevents the syncer from requesting a range that straddles the peer's
        // current epoch boundary, which would otherwise result in 'UnexpectedStartCommit'
        // or 'WrongEpoch' rejections from historical stores.
        let mut is_epoch_boundary = false;
        let mut is_historical = false;
        match inner
            .network_client
            .get_epoch_status(target_authority, timeout)
            .await
        {
            Ok(status) => {
                if status.epoch > inner.context.committee.epoch() {
                    is_epoch_boundary = true;
                }
                let peer_start = status.current_epoch_start_commit;
                if peer_start > 0 && commit_range.start() < peer_start && commit_range.end() >= peer_start {
                    tracing::info!(
                        "[COMMIT-SYNCER] Truncating fetch range {:?} from {} to end at {} (peer's epoch {} starts at {})",
                        commit_range,
                        target_authority,
                        peer_start - 1,
                        status.epoch,
                        peer_start
                    );
                    commit_range = CommitRange::new(commit_range.start()..=peer_start - 1);
                    is_epoch_boundary = true;
                } else if peer_start > 0 && commit_range.start() >= peer_start && commit_range.end() > status.last_commit_index {
                    // Also useful: don't fetch past the peer's last commit index if we know it.
                    let max_end = status.last_commit_index.max(commit_range.start());
                    if max_end < commit_range.end() {
                         commit_range = CommitRange::new(commit_range.start()..=max_end);
                    }
                }
                if peer_start > 0 && commit_range.end() == peer_start - 1 {
                    is_epoch_boundary = true;
                }
                // If the peer's last commit index is strictly past our requested range end,
                // this range is historical for the peer, so they might have pruned vote blocks.
                if commit_range.end() < status.last_commit_index {
                    is_historical = true;
                }
            }
            Err(e) => {
                tracing::debug!("Failed to query epoch status from {}: {}", target_authority, e);
                // Continue with original range if query fails; legacy nodes might not support it.
            }
        }

        // 2. Fetch commits in the commit range from the target authority.
        let (serialized_commits, serialized_blocks, commit_infos) = inner
            .network_client
            .fetch_commits(target_authority, commit_range.clone(), timeout)
            .await?;

        // 2. Verify the response contains blocks that can certify the last returned commit,
        // and the returned commits are chained by digests, so earlier commits are certified
        // as well.
        let (commits, vote_blocks) = Handle::current()
            .spawn_blocking({
                let inner = inner.clone();
                move || {
                    inner.verify_commits(
                        target_authority,
                        commit_range,
                        serialized_commits,
                        serialized_blocks,
                        is_epoch_boundary,
                        is_severe_lag,
                        is_historical,
                    )
                }
            })
            .await
            .expect("Spawn blocking should not fail")?;

        // Parse and persist CommitInfo records if they are non-empty and valid.
        let mut commit_infos_to_write = Vec::new();
        for (i, commit) in commits.iter().enumerate() {
            if let Some(info_bytes) = commit_infos.get(i) {
                if let Ok(info) = bcs::from_bytes::<crate::commit::CommitInfo>(info_bytes) {
                    if !info.reputation_scores.scores_per_authority.is_empty() {
                        commit_infos_to_write.push((commit.reference(), info));
                    }
                }
            }
        }

        if !commit_infos_to_write.is_empty() {
            tracing::info!(
                "[COMMIT-SYNCER] Persisting {} CommitInfo records to RocksDB from peer sync",
                commit_infos_to_write.len()
            );
            let store_write_result = Handle::current()
                .spawn_blocking({
                    let inner = inner.clone();
                    let commit_infos_to_write = commit_infos_to_write.clone();
                    move || {
                        let write_batch = crate::storage::WriteBatch {
                            commit_info: commit_infos_to_write,
                            ..Default::default()
                        };
                        inner.dag_state.read().store().write(write_batch)
                    }
                })
                .await
                .expect("Spawn blocking should not fail");

            if let Err(e) = store_write_result {
                tracing::error!("Failed to write CommitInfo to storage: {:?}", e);
            }
        }

        // 3. Fetch blocks referenced by the commits, from the same peer where commits are fetched.
        let mut block_refs: Vec<_> = commits.iter().flat_map(|c| c.blocks()).cloned().collect();
        block_refs.sort();
        let num_chunks = block_refs
            .len()
            .div_ceil(inner.context.parameters.max_blocks_per_fetch)
            as u32;
        let mut requests: FuturesOrdered<_> = block_refs
            .chunks(inner.context.parameters.max_blocks_per_fetch)
            .enumerate()
            .map(|(i, request_block_refs)| {
                let inner = inner.clone();
                async move {
                    // 4. Send out pipelined fetch requests to avoid overloading the target authority.
                    // In turbo mode (severe lag), we blast requests to catch up as fast as possible.
                    if !is_severe_lag {
                        let delay = timeout * i as u32 / num_chunks / 4; // reduced delay for normal
                        sleep(delay).await;
                    }
                    // Retry block fetches up to 3 times with backoff before propagating the error.
                    const MAX_BLOCK_FETCH_RETRIES: u32 = 3;
                    let serialized_blocks = {
                        let mut last_err = None;
                        let mut result = None;
                        for attempt in 0..MAX_BLOCK_FETCH_RETRIES {
                            match inner
                                .network_client
                                .fetch_blocks(
                                    target_authority,
                                    request_block_refs.to_vec(),
                                    vec![],
                                    false,
                                    timeout,
                                )
                                .await
                            {
                                Ok(blocks) => {
                                    result = Some(blocks);
                                    break;
                                }
                                Err(e) => {
                                    let hostname = &inner.context.committee.authority(target_authority).hostname;
                                    warn!(
                                        "Commit sync: retry {}/{} fetching blocks from {hostname}: {e}",
                                        attempt + 1,
                                        MAX_BLOCK_FETCH_RETRIES
                                    );
                                    last_err = Some(e);
                                    if attempt + 1 < MAX_BLOCK_FETCH_RETRIES {
                                        sleep(Duration::from_millis(500 * (attempt as u64 + 1))).await;
                                    }
                                }
                            }
                        }
                        match result {
                            Some(blocks) => blocks,
                            None => return Err(last_err.expect("last_err must be set after failed retries")),
                        }
                    };
                    // 5. Verify the same number of blocks are returned as requested.
                    if request_block_refs.len() != serialized_blocks.len() {
                        return Err(ConsensusError::UnexpectedNumberOfBlocksFetched {
                            authority: target_authority,
                            requested: request_block_refs.len(),
                            received: serialized_blocks.len(),
                        });
                    }
                    // 6. Verify returned blocks have valid formats.
                    let signed_blocks = serialized_blocks
                        .iter()
                        .map(|serialized| {
                            let block: SignedBlock = bcs::from_bytes(serialized)
                                .map_err(ConsensusError::MalformedBlock)?;
                            Ok(block)
                        })
                        .collect::<ConsensusResult<Vec<_>>>()?;
                    // 7. Verify the returned blocks match the requested block refs.
                    // If they do match, the returned blocks can be considered verified as well.
                    let mut blocks = Vec::new();
                    for ((requested_block_ref, signed_block), serialized) in request_block_refs
                        .iter()
                        .zip(signed_blocks.into_iter())
                        .zip(serialized_blocks.into_iter())
                    {
                        let serialized: Bytes = serialized.into();
                        let signed_block_digest = VerifiedBlock::compute_digest(&serialized);
                        let received_block_ref = BlockRef::new(
                            signed_block.round(),
                            signed_block.author(),
                            signed_block_digest,
                        );
                        if *requested_block_ref != received_block_ref {
                            return Err(ConsensusError::UnexpectedBlockForCommit {
                                peer: target_authority,
                                requested: *requested_block_ref,
                                received: received_block_ref,
                            });
                        }
                        blocks.push(VerifiedBlock::new_verified(signed_block, serialized));
                    }
                    Ok(blocks)
                }
            })
            .collect();

        let mut fetched_blocks = BTreeMap::new();
        while let Some(result) = requests.next().await {
            for block in result? {
                fetched_blocks.insert(block.reference(), block);
            }
        }

        // 8. Check if the block timestamps are lower than current time - this is for metrics only.
        for block in fetched_blocks.values().chain(vote_blocks.iter()) {
            let now_ms = inner.context.clock.timestamp_utc_ms();
            let forward_drift = block.timestamp_ms().saturating_sub(now_ms);
            if forward_drift == 0 {
                continue;
            };
            let peer_hostname = &inner.context.committee.authority(target_authority).hostname;
            inner
                .context
                .metrics
                .node_metrics
                .block_timestamp_drift_ms
                .with_label_values(&[peer_hostname, "commit_syncer"])
                .inc_by(forward_drift);
        }

        // 9. Now create certified commits by assigning the blocks to each commit.
        let mut certified_commits = Vec::new();
        for commit in &commits {
            let blocks = commit
                .blocks()
                .iter()
                .map(|block_ref| {
                    fetched_blocks
                        .remove(block_ref)
                        .expect("Block should exist")
                })
                .collect::<Vec<_>>();
            certified_commits.push(CertifiedCommit::new_certified(commit.clone(), blocks));
        }

        // 10. Add blocks in certified commits to the transaction certifier.
        for commit in &certified_commits {
            for block in commit.blocks() {
                // Only account for reject votes in the block, since they may vote on uncommitted
                // blocks or transactions. It is unnecessary to vote on the committed blocks
                // themselves.
                if inner.context.protocol_config.mysticeti_fastpath() {
                    inner
                        .transaction_certifier
                        .add_voted_blocks(vec![(block.clone(), vec![])]);
                }
            }
        }

        Ok(CertifiedCommits::new(certified_commits, vote_blocks))
    }
}
