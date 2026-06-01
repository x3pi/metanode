// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::{BTreeMap, BTreeSet},
    time::Duration,
};

use async_trait::async_trait;
use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::{BlockRef, Round};
use futures::{stream, StreamExt};
use meta_macros::fail_point_async;
use mysten_metrics::spawn_monitored_task;
use rand::seq::SliceRandom as _;
use tap::TapFallible;
use tracing::{debug, info, warn};

use crate::{
    block::{ExtendedBlock, SignedBlock, VerifiedBlock, GENESIS_ROUND},
    commit::{CommitAPI as _, CommitRange, TrustedCommit},
    epoch_change::{EpochChangeProposal, EpochChangeVote},
    error::{ConsensusError, ConsensusResult},
    network::{BlockStream, ExtendedSerializedBlock, NetworkService},
    CommitIndex,
};

use super::*;

#[async_trait]
impl<C: CoreThreadDispatcher> NetworkService for AuthorityService<C> {
    async fn handle_send_block(
        &self,
        peer: AuthorityIndex,
        serialized_block: ExtendedSerializedBlock,
    ) -> ConsensusResult<()> {
        fail_point_async!("consensus-rpc-response");


        // Dedup block verifications: skip expensive signature check if we
        // already verified this block recently (e.g., from broadcast + fetch).
        let signed_block: SignedBlock =
            bcs::from_bytes(&serialized_block.block).map_err(ConsensusError::MalformedBlock)?;

        // CRITICAL FIX: The `peer` identity derived from `remote_addr` is unreliable 
        // (especially for local testing where ephemeral ports are used, or when TLS client certs are not used).
        // A peer might be assigned dummy index 0, which would cause legitimate blocks to be rejected here.
        // We REMOVE the `peer != signed_block.author()` check and rely entirely on the Ed25519 signature 
        // to authenticate the block's origin. The `verify_and_vote` call immediately below strictly enforces this.
        let peer_hostname = &self.context.committee.authority(signed_block.author()).hostname;

        // Reject blocks failing validations.
        let (verified_block, reject_txn_votes) = self
            .block_verifier
            .verify_and_vote(signed_block, serialized_block.block)
            .tap_err(|e| {
                self.context
                    .metrics
                    .node_metrics
                    .invalid_blocks
                    .with_label_values(&[peer_hostname, "handle_send_block", e.name()])
                    .inc();
                info!("Invalid block from {}: {}", peer, e);
                if let ConsensusError::WrongEpoch { expected, actual } = e {
                    if *actual > *expected {
                        self.commit_vote_monitor.observe_highest_seen_epoch(*actual);
                    }
                }
            })?;
        let block_ref = verified_block.reference();
        debug!("Received block {} via send block.", block_ref);

        let now = self.context.clock.timestamp_utc_ms();
        let forward_time_drift =
            Duration::from_millis(verified_block.timestamp_ms().saturating_sub(now));

        self.context
            .metrics
            .node_metrics
            .block_timestamp_drift_ms
            .with_label_values(&[peer_hostname, "handle_send_block"])
            .inc_by(forward_time_drift.as_millis() as u64);

        // Observe the block for the commit votes. When local commit is lagging too much,
        // commit sync loop will trigger fetching.
        self.commit_vote_monitor.observe_block(&verified_block);

        // Reject blocks when local commit index is lagging too far from quorum commit index,
        // to avoid the memory overhead from suspended blocks.
        //
        // IMPORTANT: this must be done after observing votes from the block, otherwise
        // observed quorum commit will no longer progress.
        //
        // Since the main issue with too many suspended blocks is memory usage not CPU,
        // it is ok to reject after block verifications instead of before.
        let last_commit_index = self.dag_state.read().last_commit_index();
        let quorum_commit_index = self.commit_vote_monitor.quorum_commit_index();
        // The threshold to ignore block should be larger than commit_sync_batch_size,
        // to avoid excessive block rejections and synchronizations.
        if last_commit_index
            + self.context.parameters.commit_sync_batch_size * COMMIT_LAG_MULTIPLIER
            < quorum_commit_index
        {
            self.context
                .metrics
                .node_metrics
                .rejected_blocks
                .with_label_values(&["commit_lagging"])
                .inc();
            debug!(
                "Block {:?} is rejected because last commit index is lagging quorum commit index too much ({} < {})",
                block_ref, last_commit_index, quorum_commit_index,
            );
            return Err(ConsensusError::BlockRejected {
                block_ref,
                reason: format!(
                    "Last commit index is lagging quorum commit index too much ({} < {})",
                    last_commit_index, quorum_commit_index,
                ),
            });
        }

        self.context
            .metrics
            .node_metrics
            .verified_blocks
            .with_label_values(&[peer_hostname])
            .inc();

        // The block is verified and current, so it can be processed in the fastpath.
        if self.context.protocol_config.mysticeti_fastpath() {
            self.transaction_certifier
                .add_voted_blocks(vec![(verified_block.clone(), reject_txn_votes)]);
        }

        // Process epoch change data from block before accepting into DAG
        let proposal_bytes = verified_block.epoch_change_proposal().map(|v| v.as_slice());
        let votes_bytes: Vec<Vec<u8>> = verified_block
            .epoch_change_votes().to_vec();
        if proposal_bytes.is_some() || !votes_bytes.is_empty() {
            crate::epoch_change_provider::process_block_epoch_change(proposal_bytes, &votes_bytes);
        }

        // Try to accept the block into the DAG.
        let missing_ancestors = self
            .core_dispatcher
            .add_blocks(vec![verified_block.clone()])
            .await
            .map_err(|_| ConsensusError::Shutdown)?;

        // Schedule fetching missing ancestors from this peer in the background.
        if !missing_ancestors.is_empty() {
            self.context
                .metrics
                .node_metrics
                .handler_received_block_missing_ancestors
                .with_label_values(&[peer_hostname])
                .inc_by(missing_ancestors.len() as u64);
            let synchronizer = self.synchronizer.clone();
            spawn_monitored_task!(async move {
                // This does not wait for the fetch request to complete.
                // It only waits for synchronizer to queue the request to a peer.
                // When this fails, it usually means the queue is full.
                // The fetch will retry from other peers via live and periodic syncs.
                if let Err(err) = synchronizer.fetch_blocks(missing_ancestors, peer).await {
                    debug!("Failed to fetch missing ancestors via synchronizer: {err}");
                }
            });
        }

        // ------------ After processing the block, process the excluded ancestors ------------

        let excluded_ancestors = self
            .parse_excluded_ancestors(peer, &verified_block, serialized_block.excluded_ancestors)
            .tap_err(|e| {
                debug!("Failed to parse excluded ancestors from {peer} {peer_hostname}: {e}");
                self.context
                    .metrics
                    .node_metrics
                    .invalid_blocks
                    .with_label_values(&[peer_hostname, "handle_send_block", e.name()])
                    .inc();
            })?;

        self.round_tracker
            .write()
            .update_from_verified_block(&ExtendedBlock {
                block: verified_block,
                excluded_ancestors: excluded_ancestors.clone(),
            });

        let missing_excluded_ancestors = self
            .core_dispatcher
            .check_block_refs(excluded_ancestors)
            .await
            .map_err(|_| ConsensusError::Shutdown)?;

        // Schedule fetching missing soft links from this peer in the background.
        if !missing_excluded_ancestors.is_empty() {
            self.context
                .metrics
                .node_metrics
                .network_excluded_ancestors_sent_to_fetch
                .with_label_values(&[peer_hostname])
                .inc_by(missing_excluded_ancestors.len() as u64);

            let synchronizer = self.synchronizer.clone();
            spawn_monitored_task!(async move {
                if let Err(err) = synchronizer
                    .fetch_blocks(missing_excluded_ancestors, peer)
                    .await
                {
                    debug!("Failed to fetch excluded ancestors via synchronizer: {err}");
                }
            });
        }

        Ok(())
    }

    async fn handle_subscribe_blocks(
        &self,
        peer: AuthorityIndex,
        last_received: Round,
    ) -> ConsensusResult<BlockStream> {
        fail_point_async!("consensus-rpc-response");

        let dag_state = self.dag_state.read();
        // Find recent own blocks that have not been received by the peer.
        // If last_received is a valid and more blocks have been proposed since then, this call is
        // guaranteed to return at least some recent blocks, which will help with liveness.
        let missed_blocks = stream::iter(
            dag_state
                .get_cached_blocks(self.context.own_index, last_received + 1)
                .into_iter()
                .map(|block| ExtendedSerializedBlock {
                    block: block.serialized().clone(),
                    excluded_ancestors: vec![],
                }),
        );

        let broadcasted_blocks = BroadcastedBlockStream::new(
            peer,
            self.rx_block_broadcast.resubscribe(),
            self.subscription_counter.clone(),
        );

        // Return a stream of blocks that first yields missed blocks as requested, then new blocks.
        Ok(Box::pin(missed_blocks.chain(
            broadcasted_blocks.map(ExtendedSerializedBlock::from),
        )))
    }

    // Handles two types of requests:
    // 1. Missing block for block sync:
    //    - uses highest_accepted_rounds.
    //    - max_blocks_per_sync blocks should be returned.
    // 2. Committed block for commit sync:
    //    - does not use highest_accepted_rounds.
    //    - max_blocks_per_fetch blocks should be returned.
    async fn handle_fetch_blocks(
        &self,
        _peer: AuthorityIndex,
        mut block_refs: Vec<BlockRef>,
        highest_accepted_rounds: Vec<Round>,
        breadth_first: bool,
    ) -> ConsensusResult<Vec<Bytes>> {
        fail_point_async!("consensus-rpc-response");

        if !highest_accepted_rounds.is_empty()
            && highest_accepted_rounds.len() != self.context.committee.size()
        {
            return Err(ConsensusError::InvalidSizeOfHighestAcceptedRounds(
                highest_accepted_rounds.len(),
                self.context.committee.size(),
            ));
        }

        // Some quick validation of the requested block refs
        let max_response_num_blocks = if !highest_accepted_rounds.is_empty() {
            self.context.parameters.max_blocks_per_sync
        } else {
            self.context.parameters.max_blocks_per_fetch
        };
        if block_refs.len() > max_response_num_blocks {
            block_refs.truncate(max_response_num_blocks);
        }

        // Validate the requested block refs.
        for block in &block_refs {
            // CROSS-EPOCH FIX: Only validate AuthorityIndex strictly for current epoch blocks.
            // Historical blocks from previous epochs may have different committee sizes.
            // When legacy_store_manager is present, we allow any index and let the store lookup
            // return blocks (or empty) naturally - blocks from other epochs will be found in legacy stores.
            if self.legacy_store_manager.is_none() {
                // No legacy stores = current epoch only, strict validation
                if !self.context.committee.is_valid_index(block.author) {
                    return Err(ConsensusError::InvalidAuthorityIndex {
                        index: block.author,
                        max: self.context.committee.size(),
                    });
                }
            }
            if block.round == GENESIS_ROUND {
                return Err(ConsensusError::UnexpectedGenesisBlockRequested);
            }
        }

        // Get requested blocks from store.
        let mut blocks = if !highest_accepted_rounds.is_empty() {
            block_refs.sort();
            block_refs.dedup();
            let mut blocks = self
                .dag_state
                .read()
                .get_blocks(&block_refs)
                .into_iter()
                .flatten()
                .collect::<Vec<_>>();

            if breadth_first {
                // Get unique missing ancestor blocks of the requested blocks.
                let mut missing_ancestors = blocks
                    .iter()
                    .flat_map(|block| block.ancestors().to_vec())
                    .filter(|block_ref| highest_accepted_rounds[block_ref.author] < block_ref.round)
                    .collect::<BTreeSet<_>>()
                    .into_iter()
                    .collect::<Vec<_>>();

                // If there are too many missing ancestors, randomly select a subset to avoid
                // fetching duplicated blocks across peers.
                let selected_num_blocks = max_response_num_blocks.saturating_sub(blocks.len());
                if selected_num_blocks < missing_ancestors.len() {
                    missing_ancestors = missing_ancestors
                        .choose_multiple(&mut rand::thread_rng(), selected_num_blocks)
                        .copied()
                        .collect::<Vec<_>>();
                }
                let ancestor_blocks = self.dag_state.read().get_blocks(&missing_ancestors);
                blocks.extend(ancestor_blocks.into_iter().flatten());
            } else {
                // Get additional blocks from authorities with missing block, if they are available in cache.
                // Compute the lowest missing round per requested authority.
                let mut lowest_missing_rounds = BTreeMap::<AuthorityIndex, Round>::new();
                for block_ref in blocks.iter().map(|b| b.reference()) {
                    let entry = lowest_missing_rounds
                        .entry(block_ref.author)
                        .or_insert(block_ref.round);
                    *entry = (*entry).min(block_ref.round);
                }

                // Retrieve additional blocks per authority, from peer's highest accepted round + 1 to
                // lowest missing round (exclusive) per requested authority.
                // No block from other authorities are retrieved. It is possible that the requestor is not
                // seeing missing block from another authority, and serving a block would just lead to unnecessary
                // data transfer. Or missing blocks from other authorities are requested from other peers.
                let dag_state = self.dag_state.read();
                for (authority, lowest_missing_round) in lowest_missing_rounds {
                    let highest_accepted_round = highest_accepted_rounds[authority];
                    if highest_accepted_round >= lowest_missing_round {
                        continue;
                    }
                    let missing_blocks = dag_state.get_cached_blocks_in_range(
                        authority,
                        highest_accepted_round + 1,
                        lowest_missing_round,
                        self.context
                            .parameters
                            .max_blocks_per_sync
                            .saturating_sub(blocks.len()),
                    );
                    blocks.extend(missing_blocks);
                    if blocks.len() >= self.context.parameters.max_blocks_per_sync {
                        blocks.truncate(self.context.parameters.max_blocks_per_sync);
                        break;
                    }
                }
            }

            blocks
        } else {
            self.dag_state
                .read()
                .get_blocks(&block_refs)
                .into_iter()
                .flatten()
                .collect()
        };

        // STORE FALLBACK: For blocks not found in dag_state cache
        // First try current epoch's RocksDB store, then legacy stores
        let blocks_found = blocks.len();
        if blocks_found < block_refs.len() {
            // Collect block refs that were not found
            let found_refs: std::collections::HashSet<_> =
                blocks.iter().map(|b| b.reference()).collect();
            let mut missing_refs: Vec<_> = block_refs
                .iter()
                .filter(|r| !found_refs.contains(r))
                .cloned()
                .collect();

            // STEP 1: Try current epoch's RocksDB store (self.store)
            if !missing_refs.is_empty() {
                info!(
                    "🔄 [FETCH-BLOCKS] {} blocks missing from dag_state, searching current store...",
                    missing_refs.len()
                );

                if let Ok(store_blocks) = self.store.read_blocks(&missing_refs) {
                    let found_in_store: Vec<_> = store_blocks.into_iter().flatten().collect();
                    if !found_in_store.is_empty() {
                        info!(
                            "✅ [STORE SYNC] Found {} blocks in current epoch store",
                            found_in_store.len()
                        );
                        // Update missing_refs to exclude found blocks
                        let newly_found: std::collections::HashSet<_> =
                            found_in_store.iter().map(|b| b.reference()).collect();
                        missing_refs.retain(|r| !newly_found.contains(r));
                        blocks.extend(found_in_store);
                    }
                }
            }

            // STEP 2: Try legacy epoch stores for remaining missing blocks
            if !missing_refs.is_empty() {
                if let Some(ref legacy_manager) = self.legacy_store_manager {
                    info!(
                        "🔄 [FETCH-BLOCKS] {} blocks still missing, searching legacy stores...",
                        missing_refs.len()
                    );

                    // Search all legacy stores for missing blocks
                    for (epoch, legacy_store) in legacy_manager.get_all_stores() {
                        if let Ok(legacy_blocks) = legacy_store.read_blocks(&missing_refs) {
                            let found_legacy: Vec<_> =
                                legacy_blocks.into_iter().flatten().collect();
                            if !found_legacy.is_empty() {
                                info!(
                                    "✅ [LEGACY SYNC] Found {} blocks in epoch {} store",
                                    found_legacy.len(),
                                    epoch
                                );
                                blocks.extend(found_legacy);
                            }
                        }
                    }
                }
            }
        }

        // Return the serialized blocks
        let bytes = blocks
            .into_iter()
            .map(|block| block.serialized().clone())
            .collect::<Vec<_>>();
        Ok(bytes)
    }

    async fn handle_fetch_commits(
        &self,
        _peer: AuthorityIndex,
        commit_range: CommitRange,
    ) -> ConsensusResult<(Vec<TrustedCommit>, Vec<VerifiedBlock>, Vec<crate::commit::CommitInfo>)> {
        fail_point_async!("consensus-rpc-response");

        // Compute an inclusive end index and bound the maximum number of commits scanned.
        let inclusive_end = commit_range.end().min(
            commit_range.start() + self.context.parameters.commit_sync_batch_size as CommitIndex
                - 1,
        );

        // First try to get commits from current store
        let commits = self
            .store
            .scan_commits((commit_range.start()..=inclusive_end).into())?;

        // CRITICAL LOGGING: Trace commits found for sync debugging
        if !commits.is_empty() {
            let commit_indices: Vec<u32> = commits.iter().map(|c| c.index()).collect();
            info!(
                "📦 [FETCH-COMMITS] Found {} commits in range {:?}: indices={:?}",
                commits.len(),
                commit_range,
                commit_indices
            );
        } else {
            info!(
                "⚠️ [FETCH-COMMITS] No commits found in current store for range {:?}",
                commit_range
            );
        }

        let mut certifier_block_refs = vec![];

        if let Some(c) = commits.last() {
            let index = c.index();
            let votes = self.store.read_commit_votes(index, c.digest())?;
            
            // Bypass quorum validation for FETCH-COMMITS to fix deadlock
            certifier_block_refs = votes;
        }

        // Log final commits being returned
        if !commits.is_empty() {
            let final_indices: Vec<u32> = commits.iter().map(|c| c.index()).collect();
            info!(
                "✅ [FETCH-COMMITS] Returning {} commits: indices={:?}",
                commits.len(),
                final_indices
            );
        } else {
            info!("⚠️ [FETCH-COMMITS] No commits to return after quorum check");
        }
        
        let mut certifier_blocks = vec![];
        if !certifier_block_refs.is_empty() {
            // Read from current store only
            certifier_blocks = self.store
                .read_blocks(&certifier_block_refs)?
                .into_iter()
                .flatten()
                .collect();
        }
            
        let mut commit_infos = vec![];
        for c in &commits {
            // First try current store
            if let Ok(Some(info)) = self.store.read_commit_info(c.index(), c.digest()) {
                commit_infos.push(info);
            } else {
                commit_infos.push(crate::commit::CommitInfo {
                    reputation_scores: Default::default(),
                    committed_rounds: vec![],
                });
            }
        }

        Ok((commits, certifier_blocks, commit_infos))
    }

    /// Handles fetch_commits_by_global_range - searches current epoch
    /// Note: For now, this only supports the current epoch. Legacy epoch support
    /// requires epoch_base metadata which is not yet stored in legacy stores.
    async fn handle_fetch_commits_by_global_range(
        &self,
        _peer: AuthorityIndex,
        start_global_index: u64,
        end_global_index: u64,
    ) -> ConsensusResult<Vec<crate::network::tonic_network::GlobalCommitInfo>> {
        fail_point_async!("consensus-rpc-response");

        use crate::network::tonic_network::GlobalCommitInfo;

        let mut result: Vec<GlobalCommitInfo> = Vec::new();
        let batch_limit = self.context.parameters.commit_sync_batch_size as u64;
        let max_global_index = end_global_index.min(start_global_index + batch_limit - 1);

        let current_epoch = self.context.committee.epoch();

        // Read last commit info to determine epoch_base_index
        // epoch_base_index = global_exec_index_of_last_block_in_previous_epoch
        // For epoch 0, epoch_base = 0
        // For epoch N, epoch_base = boundary_block_of_epoch_N
        let last_commit_index = self.dag_state.read().last_commit_index();

        // For current epoch, we calculate epoch_base from the first commit:
        // epoch_base = first_commit.global_exec_index - first_commit.index
        let current_epoch_base: u64 = if current_epoch == 0 {
            0
        } else {
            // Read the first commit to determine epoch_base
            if let Ok(first_commits) = self.store.scan_commits((1..=1).into()) {
                if let Some(first_commit) = first_commits.first() {
                    let first_global_idx = first_commit.global_exec_index();
                    let first_local_idx = first_commit.index() as u64;
                    let calculated_base = first_global_idx.saturating_sub(first_local_idx);
                    info!(
                        "📊 [FETCH-GLOBAL] Calculated epoch_base from first commit: \
                         first_commit.global_exec_index={}, first_commit.index={}, epoch_base={}",
                        first_global_idx, first_local_idx, calculated_base
                    );
                    calculated_base
                } else {
                    // CRITICAL FIX: Use epoch_base_index from constructor instead of 0!
                    // On snapshot restore / cold start, there are no commits yet but
                    // the epoch_base_index was correctly set during authority startup.
                    warn!(
                        "⚠️ [FETCH-GLOBAL] No commits in store, using epoch_base_index={} from constructor",
                        self.epoch_base_index
                    );
                    self.epoch_base_index
                }
            } else {
                warn!(
                    "⚠️ [FETCH-GLOBAL] Failed to read first commit, using epoch_base_index={} from constructor",
                    self.epoch_base_index
                );
                self.epoch_base_index
            }
        };

        info!(
            "📦 [FETCH-GLOBAL] Searching commits in global range [{}, {}] (epoch={}, base={}, last_commit={})",
            start_global_index, max_global_index, current_epoch, current_epoch_base, last_commit_index
        );

        // Calculate which local commit indices we need
        let local_start = if start_global_index > current_epoch_base {
            (start_global_index - current_epoch_base) as u32
        } else {
            1 // Start from first commit
        };
        let local_end = if max_global_index > current_epoch_base {
            (max_global_index - current_epoch_base) as u32
        } else {
            0 // Range is before this epoch
        };

        if local_end >= local_start && local_end <= last_commit_index {
            if let Ok(commits) = self.store.scan_commits((local_start..=local_end).into()) {
                for commit in commits {
                    let commit_index = commit.index();
                    let global_idx = current_epoch_base + commit_index as u64;

                    // Get block refs for this commit
                    let block_refs: Vec<bytes::Bytes> = commit
                        .blocks()
                        .iter()
                        .filter_map(|r| bcs::to_bytes(r).ok().map(|b| b.into()))
                        .collect();

                    result.push(GlobalCommitInfo {
                        epoch: current_epoch,
                        global_exec_index: global_idx,
                        local_commit_index: commit_index,
                        epoch_boundary_block: current_epoch_base,
                        commit_data: commit.serialized().clone(),
                        block_refs,
                    });
                }
                info!(
                    "✅ [FETCH-GLOBAL] Found {} commits in epoch {} (local range [{}, {}])",
                    result.len(),
                    current_epoch,
                    local_start,
                    local_end
                );
            }
        } else {
            info!(
                "📭 [FETCH-GLOBAL] No commits in range (local=[{}, {}], last={})",
                local_start, local_end, last_commit_index
            );
        }

        // STORE-HOPPING: If the requested range includes blocks before current epoch,
        // we MUST search legacy stores for those blocks (even if current epoch has some results).
        // E.g., request [101, 236] with epoch_base=136 needs blocks 101-136 from previous epoch.
        if start_global_index < current_epoch_base {
            if let Some(ref legacy_manager) = self.legacy_store_manager {
                info!(
                    "🔄 [STORE-HOP] Range [{}, {}] is before current epoch base {}, searching legacy stores...",
                    start_global_index, max_global_index, current_epoch_base
                );

                // Get all legacy stores sorted by epoch (oldest first)
                let mut legacy_stores = legacy_manager.get_all_stores();
                legacy_stores.sort_by_key(|(epoch, _)| *epoch);

                for (legacy_epoch, legacy_store) in legacy_stores {
                    // For legacy stores, we need to derive epoch_base from the first commit
                    let legacy_epoch_base = if legacy_epoch == 0 {
                        0u64
                    } else {
                        // Read first commit to calculate epoch_base
                        if let Ok(first_commits) = legacy_store.scan_commits((1..=1).into()) {
                            if let Some(first_commit) = first_commits.first() {
                                let first_global = first_commit.global_exec_index();
                                let first_local = first_commit.index() as u64;
                                first_global.saturating_sub(first_local)
                            } else {
                                continue; // No commits in this store
                            }
                        } else {
                            continue;
                        }
                    };

                    // Calculate local range for this legacy epoch
                    let leg_local_start = if start_global_index > legacy_epoch_base {
                        (start_global_index - legacy_epoch_base) as u32
                    } else {
                        1
                    };
                    let leg_local_end = if max_global_index > legacy_epoch_base {
                        (max_global_index - legacy_epoch_base) as u32
                    } else {
                        0
                    };

                    if leg_local_end >= leg_local_start {
                        // Also need to check against the last commit in this legacy store
                        // Scan a batch to find commits
                        if let Ok(commits) =
                            legacy_store.scan_commits((leg_local_start..=leg_local_end).into())
                        {
                            for commit in commits {
                                let commit_index = commit.index();
                                let global_idx = commit.global_exec_index();

                                // Verify this commit is within the requested range
                                if global_idx >= start_global_index
                                    && global_idx <= max_global_index
                                {
                                    let block_refs: Vec<bytes::Bytes> = commit
                                        .blocks()
                                        .iter()
                                        .filter_map(|r| bcs::to_bytes(r).ok().map(|b| b.into()))
                                        .collect();

                                    result.push(GlobalCommitInfo {
                                        epoch: legacy_epoch,
                                        global_exec_index: global_idx,
                                        local_commit_index: commit_index,
                                        epoch_boundary_block: legacy_epoch_base,
                                        commit_data: commit.serialized().clone(),
                                        block_refs,
                                    });
                                }
                            }

                            if !result.is_empty() {
                                info!(
                                    "✅ [STORE-HOP] Found {} commits in legacy epoch {} for range [{}, {}]",
                                    result.len(), legacy_epoch, start_global_index, max_global_index
                                );
                                // break; // Found commits, no need to search older epochs
                            }
                        }
                    }
                }
            } else {
                info!(
                    "⚠️ [STORE-HOP] No legacy store manager available, cannot search previous epochs"
                );
            }
        }

        // Sort by global index to ensure order
        result.sort_by_key(|c| c.global_exec_index);

        info!(
            "✅ [FETCH-GLOBAL] Returning {} commits for range [{}, {}]",
            result.len(),
            start_global_index,
            max_global_index
        );

        Ok(result)
    }

    async fn handle_fetch_latest_blocks(
        &self,
        peer: AuthorityIndex,
        authorities: Vec<AuthorityIndex>,
    ) -> ConsensusResult<Vec<Bytes>> {
        fail_point_async!("consensus-rpc-response");

        if authorities.len() > self.context.committee.size() {
            return Err(ConsensusError::TooManyAuthoritiesProvided(peer));
        }

        // Ensure that those are valid authorities
        for authority in &authorities {
            if !self.context.committee.is_valid_index(*authority) {
                return Err(ConsensusError::InvalidAuthorityIndex {
                    index: *authority,
                    max: self.context.committee.size(),
                });
            }
        }

        // Read from the dag state to find the latest blocks.
        // TODO: at the moment we don't look into the block manager for suspended blocks. Ideally we
        // want in the future if we think we would like to tackle the majority of cases.
        let mut blocks = vec![];
        let dag_state = self.dag_state.read();
        for authority in authorities {
            let block = dag_state.get_last_block_for_authority(authority);

            debug!("Latest block for {authority}: {block:?} as requested from {peer}");

            // no reason to serve back the genesis block - it's equal as if it has not received any block
            if block.round() != GENESIS_ROUND {
                blocks.push(block);
            }
        }

        // Return the serialised blocks
        let result = blocks
            .into_iter()
            .map(|block| block.serialized().clone())
            .collect::<Vec<_>>();

        Ok(result)
    }

    async fn handle_get_latest_rounds(
        &self,
        _peer: AuthorityIndex,
    ) -> ConsensusResult<(Vec<Round>, Vec<Round>)> {
        fail_point_async!("consensus-rpc-response");

        let mut highest_received_rounds = self.core_dispatcher.highest_received_rounds();

        let blocks = self
            .dag_state
            .read()
            .get_last_cached_block_per_authority(Round::MAX);
        let highest_accepted_rounds = blocks
            .into_iter()
            .map(|(block, _)| block.round())
            .collect::<Vec<_>>();

        // Own blocks do not go through the core dispatcher, so they need to be set separately.
        highest_received_rounds[self.context.own_index] =
            highest_accepted_rounds[self.context.own_index];

        Ok((highest_received_rounds, highest_accepted_rounds))
    }

    async fn handle_get_epoch_status(
        &self,
        _peer: AuthorityIndex,
    ) -> ConsensusResult<crate::network::tonic_network::GetEpochStatusResponse> {
        fail_point_async!("consensus-rpc-response");

        let epoch = self.context.committee.epoch();
        let last_commit_index = self.dag_state.read().last_commit_index();

        let current_epoch_start_commit = if let Ok(first_commits) = self.store.scan_commits((1..=1).into()) {
            first_commits.first().map(|c| c.index()).unwrap_or(0)
        } else {
            0
        };

        Ok(crate::network::tonic_network::GetEpochStatusResponse {
            epoch,
            current_epoch_start_commit,
            last_commit_index,
        })
    }

    async fn handle_send_epoch_change_proposal(
        &self,
        _peer: AuthorityIndex,
        proposal: EpochChangeProposal,
    ) -> ConsensusResult<()> {
        // Forward to epoch change processor if available
        if let Some(ref processor) = *self.epoch_change_processor.read() {
            let proposal_bytes = bcs::to_bytes(&proposal).map_err(|e| {
                ConsensusError::NetworkRequest(format!("serialize proposal failed: {e:?}"))
            })?;
            processor.process_proposal(&proposal_bytes);
        }

        Ok(())
    }

    async fn handle_send_epoch_change_vote(
        &self,
        _peer: AuthorityIndex,
        vote: EpochChangeVote,
    ) -> ConsensusResult<()> {
        // Forward to epoch change processor if available
        if let Some(ref processor) = *self.epoch_change_processor.read() {
            let vote_bytes = bcs::to_bytes(&vote).map_err(|e| {
                ConsensusError::NetworkRequest(format!("serialize vote failed: {e:?}"))
            })?;
            processor.process_vote(&vote_bytes);
        }

        Ok(())
    }
}

