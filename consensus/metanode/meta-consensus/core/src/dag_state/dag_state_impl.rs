// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    cmp::max,
    collections::{BTreeMap, BTreeSet, VecDeque},
    panic,
    sync::Arc,
    vec,
};

use consensus_config::AuthorityIndex;
use consensus_types::block::{BlockRef, Round, TransactionIndex};
use tracing::{debug, info};

use crate::{
    block::{genesis_blocks, BlockAPI, VerifiedBlock, GENESIS_ROUND},
    commit::{
        load_committed_subdag_from_store, CommitAPI as _, CommitInfo, CommitRef, CommitVote,
        TrustedCommit, GENESIS_COMMIT_INDEX, CommitIndex, CommitDigest,
    },
    context::Context,
    dag_state::types::BlockInfo,
    leader_scoring::ScoringSubdag,
    storage::Store,
    threshold_clock::ThresholdClock,
};

/// DagState provides the API to write and read accepted blocks from the DAG.
/// Only uncommitted and last committed blocks are cached in memory.
/// The rest of blocks are stored on disk.
/// Refs to cached blocks and additional refs are cached as well, to speed up existence checks.
///
/// Note: DagState should be wrapped with Arc<parking_lot::RwLock<_>>, to allow
/// concurrent access from multiple components.
pub struct DagState {
    pub(crate) context: Arc<Context>,

    // The genesis blocks
    pub(crate) genesis: BTreeMap<BlockRef, VerifiedBlock>,

    // Contains recent blocks within CACHED_ROUNDS from the last committed round per authority.
    // Note: all uncommitted blocks are kept in memory.
    //
    // When GC is enabled, this map has a different semantic. It holds all the recent data for each authority making sure that it always have available
    // CACHED_ROUNDS worth of data. The entries are evicted based on the latest GC round, however the eviction process will respect the CACHED_ROUNDS.
    // For each authority, blocks are only evicted when their round is less than or equal to both `gc_round`, and `highest authority round - cached rounds`.
    // This ensures that the GC requirements are respected (we never clean up any block above `gc_round`), and there are enough blocks cached.
    pub(crate) recent_blocks: BTreeMap<BlockRef, BlockInfo>,

    // Indexes recent block refs by their authorities.
    // Vec position corresponds to the authority index.
    pub(crate) recent_refs_by_authority: Vec<BTreeSet<BlockRef>>,

    // Keeps track of the threshold clock for proposing blocks.
    pub(crate) threshold_clock: ThresholdClock,

    // Keeps track of the highest round that has been evicted for each authority. Any blocks that are of round <= evict_round
    // should be considered evicted, and if any exist we should not consider the causauly complete in the order they appear.
    // The `evicted_rounds` size should be the same as the committee size.
    pub(crate) evicted_rounds: Vec<Round>,

    // Highest round of blocks accepted.
    pub(crate) highest_accepted_round: Round,

    // Last consensus commit of the dag.
    pub(crate) last_commit: Option<TrustedCommit>,

    // Last wall time when commit round advanced. Does not persist across restarts.
    pub(crate) last_commit_round_advancement_time: Option<std::time::Instant>,

    // Last committed rounds per authority.
    pub(crate) last_committed_rounds: Vec<Round>,

    /// The committed subdags that have been scored but scores have not been used
    /// for leader schedule yet.
    pub(crate) scoring_subdag: ScoringSubdag,

    // Commit votes pending to be included in new blocks.
    // Multi-leader: dedup is enforced in take_commit_votes() — only
    // the first commit vote per index is kept when draining.
    // Note: pending votes are not recovered on restart — they will be
    // re-created from new commits after the node catches up.
    pub(crate) pending_commit_votes: VecDeque<CommitVote>,

    // Blocks and commits must be buffered for persistence before they can be
    // inserted into the local DAG or sent to output.
    pub(crate) blocks_to_write: Vec<VerifiedBlock>,
    pub(crate) commits_to_write: Vec<TrustedCommit>,
    pub(crate) commits_to_delete: Vec<(CommitIndex, CommitDigest)>,

    // Buffers the reputation scores & last_committed_rounds to be flushed with the
    // next dag state flush. Not writing eagerly is okay because we can recover reputation scores
    // & last_committed_rounds from the commits as needed.
    pub(crate) commit_info_to_write: Vec<(CommitRef, CommitInfo)>,

    // Buffers finalized commits and their rejected transactions to be written to storage.
    pub(crate) finalized_commits_to_write:
        Vec<(CommitRef, BTreeMap<BlockRef, Vec<TransactionIndex>>)>,

    // Persistent storage for blocks, commits and other consensus data.
    pub(crate) store: Arc<dyn Store>,

    // The number of cached rounds
    pub(crate) cached_rounds: Round,

    // Fallback timestamp for when last_commit is None (e.g., after DAG wipe)
    pub(crate) fallback_last_commit_timestamp_ms: u64,

    // Stores reputation scores fetched during a cold-start baseline reset
    pub(crate) baseline_reputation_scores: Option<Vec<(AuthorityIndex, u64)>>,
}

impl DagState {
    /// Get genesis block references for block verification.
    pub fn get_genesis_block_refs(&self) -> std::collections::BTreeSet<BlockRef> {
        self.genesis.keys().cloned().collect()
    }

    pub fn take_baseline_reputation_scores(&mut self) -> Option<Vec<(AuthorityIndex, u64)>> {
        self.baseline_reputation_scores.take()
    }

    pub fn has_baseline_reputation_scores(&self) -> bool {
        self.baseline_reputation_scores.is_some()
    }

    /// Initializes DagState from storage.
    pub fn new(context: Arc<Context>, store: Arc<dyn Store>) -> Self {
        let cached_rounds = context.parameters.dag_state_cached_rounds as Round;
        let num_authorities = context.committee.size();

        // Try to load persisted genesis block refs first, fallback to generating
        let genesis = if let Some(stored_genesis_refs) = store
            .read_genesis_blocks(context.committee.epoch())
            .unwrap_or_else(|e| {
                tracing::warn!("Failed to read genesis block refs from storage: {:?}", e);
                None
            }) {
            tracing::info!(
                "✅ Loaded {} genesis block refs from storage for epoch {}",
                stored_genesis_refs.len(),
                context.committee.epoch()
            );

            // Load actual blocks from storage using the refs
            let full_blocks = store.read_blocks(&stored_genesis_refs).unwrap_or_else(|e| {
                tracing::warn!("Failed to read full genesis blocks from storage: {:?}", e);
                vec![]
            });

            // Create map from refs to blocks, using available blocks
            let mut genesis_map = BTreeMap::new();
            for (i, block_ref) in stored_genesis_refs.into_iter().enumerate() {
                if let Some(Some(block)) = full_blocks.get(i) {
                    genesis_map.insert(block_ref, block.clone());
                } else {
                    tracing::warn!("Missing genesis block for ref {:?}", block_ref);
                }
            }

            // If we have incomplete genesis blocks, regenerate them
            if genesis_map.len() != context.committee.size() {
                tracing::warn!(
                    "Incomplete genesis blocks in storage ({} vs {}), regenerating",
                    genesis_map.len(),
                    context.committee.size()
                );
                let generated_genesis = genesis_blocks(context.as_ref());
                generated_genesis
                    .into_iter()
                    .map(|block| (block.reference(), block))
                    .collect()
            } else {
                genesis_map
            }
        } else {
            // Generate and persist genesis blocks
            let generated_genesis = genesis_blocks(context.as_ref());
            tracing::info!(
                "🔄 Generated {} genesis blocks for epoch {} - persisting to storage",
                generated_genesis.len(),
                context.committee.epoch()
            );

            // Persist block refs, not full blocks (to avoid serialization issues)
            let genesis_refs: Vec<BlockRef> =
                generated_genesis.iter().map(|b| b.reference()).collect();
            if let Err(e) = store.write_genesis_blocks(context.committee.epoch(), genesis_refs) {
                tracing::warn!("Failed to persist genesis block refs: {:?}", e);
            }

            generated_genesis
                .into_iter()
                .map(|block| (block.reference(), block))
                .collect()
        };

        let threshold_clock = ThresholdClock::new(1, context.clone());

        let last_commit = store
            .read_last_commit()
            .unwrap_or_else(|e| panic!("Failed to read_last_commit from storage: {:?}", e));

        let commit_info = store
            .read_last_commit_info()
            .unwrap_or_else(|e| panic!("Failed to read_last_commit_info from storage: {:?}", e));
        let (mut last_committed_rounds, commit_recovery_start_index) =
            if let Some((commit_ref, commit_info)) = commit_info {
                tracing::info!("Recovering committed state from {commit_ref} {commit_info:?}");
                (commit_info.committed_rounds, commit_ref.index + 1)
            } else {
                tracing::info!("Found no stored CommitInfo to recover from");
                (vec![0; num_authorities], GENESIS_COMMIT_INDEX + 1)
            };

        let mut unscored_committed_subdags = Vec::new();
        let mut scoring_subdag = ScoringSubdag::new(context.clone());

        if let Some(last_commit) = last_commit.as_ref() {
            let commits_per_schedule = crate::leader_schedule::LeaderSchedule::commits_per_schedule() as u32;
            let scoring_window_start = (last_commit.index() / commits_per_schedule) * commits_per_schedule + 1;
            let scan_start = std::cmp::min(scoring_window_start, commit_recovery_start_index);
            
            let commits = store
                .scan_commits((scan_start..=last_commit.index()).into())
                .unwrap_or_else(|e| {
                    panic!("Failed to scan_commits for scoring subdag recovery: {:?}", e)
                });
                
            let mut scoring_subdags_to_add = Vec::new();
            
            for commit in commits {
                if commit.index() >= commit_recovery_start_index {
                    for block_ref in commit.blocks() {
                        last_committed_rounds[block_ref.author] =
                            max(last_committed_rounds[block_ref.author], block_ref.round);
                    }
                    let committed_subdag =
                        load_committed_subdag_from_store(store.as_ref(), commit.clone(), vec![]);
                    unscored_committed_subdags.push(committed_subdag);
                }
                
                if commit.index() >= scoring_window_start {
                    let committed_subdag =
                        load_committed_subdag_from_store(store.as_ref(), commit.clone(), vec![]);
                    scoring_subdags_to_add.push(committed_subdag);
                }
            }
            
            scoring_subdag.add_subdags(scoring_subdags_to_add);
        }

        tracing::info!(
            "DagState was initialized with the following state: \
            {last_commit:?}; {last_committed_rounds:?}; {} unscored committed subdags; {} scored subdags",
            unscored_committed_subdags.len(),
            scoring_subdag.scored_subdags_count()
        );

        let mut last_commit_timestamp_ms = 0;
        if let Some(commit) = last_commit.as_ref() {
            last_commit_timestamp_ms = commit.timestamp_ms();
        }

        let mut state = Self {
            context: context.clone(),
            genesis,
            recent_blocks: BTreeMap::new(),
            recent_refs_by_authority: vec![BTreeSet::new(); num_authorities],
            threshold_clock,
            highest_accepted_round: 0,
            last_commit: last_commit.clone(),
            last_commit_round_advancement_time: None,
            last_committed_rounds: last_committed_rounds.clone(),
            pending_commit_votes: VecDeque::new(),
            blocks_to_write: vec![],
            commits_to_write: vec![],
            commits_to_delete: vec![],
            commit_info_to_write: vec![],
            finalized_commits_to_write: vec![],
            scoring_subdag,
            store: store.clone(),
            cached_rounds,
            evicted_rounds: vec![0; num_authorities],
            fallback_last_commit_timestamp_ms: last_commit_timestamp_ms,
            baseline_reputation_scores: None,
        };

        for (authority_index, _) in context.committee.authorities() {
            let (blocks, eviction_round) = {
                // Find the latest block for the authority to calculate the eviction round. Then we want to scan and load the blocks from the eviction round and onwards only.
                // As reminder, the eviction round is taking into account the gc_round.
                let last_block = state
                    .store
                    .scan_last_blocks_by_author(authority_index, 1, None)
                    .expect("Database error");
                let last_block_round = last_block
                    .last()
                    .map(|b| b.round())
                    .unwrap_or(GENESIS_ROUND);

                let eviction_round =
                    Self::eviction_round(last_block_round, state.gc_round(), state.cached_rounds);
                let blocks = state
                    .store
                    .scan_blocks_by_author(authority_index, eviction_round + 1)
                    .expect("Database error");

                (blocks, eviction_round)
            };

            state.evicted_rounds[authority_index] = eviction_round;

            // Update the block metadata for the authority.
            for block in &blocks {
                state.update_block_metadata(block);
            }

            debug!(
                "Recovered blocks {}: {:?}",
                authority_index,
                blocks
                    .iter()
                    .map(|b| b.reference())
                    .collect::<Vec<BlockRef>>()
            );
        }

        if let Some(last_commit) = last_commit {
            let mut index = last_commit.index();
            let gc_round = state.gc_round();
            info!(
                "Recovering block commit statuses from commit index {} and backwards until leader of round <= gc_round {:?}",
                index, gc_round
            );

            loop {
                let commits = store
                    .scan_commits((index..=index).into())
                    .unwrap_or_else(|e| panic!("Failed to scan_commits from storage during commit status recovery: {:?}", e));
                let Some(commit) = commits.first() else {
                    info!("Recovering finished up to index {index}, no more commits to recover");
                    break;
                };

                // Check the commit leader round to see if it is within the gc_round. If it is not then we can stop the recovery process.
                if gc_round > 0 && commit.leader().round <= gc_round {
                    info!(
                        "Recovering finished, reached commit leader round {} <= gc_round {}",
                        commit.leader().round,
                        gc_round
                    );
                    break;
                }

                commit.blocks().iter().filter(|b| b.round > gc_round).for_each(|block_ref|{
                    debug!(
                        "Setting block {:?} as committed based on commit {:?}",
                        block_ref,
                        commit.index()
                    );
                    assert!(state.set_committed(block_ref), "Attempted to set again a block {:?} as committed when recovering commit {:?}", block_ref, commit);
                });

                // All commits are indexed starting from 1, so one reach zero exit.
                index = index.saturating_sub(1);
                if index == 0 {
                    break;
                }
            }
        }

        // Recover hard linked statuses for blocks within GC round.
        let proposed_blocks = store
            .scan_blocks_by_author(context.own_index, state.gc_round() + 1)
            .expect("Database error");
        for block in proposed_blocks {
            state.link_causal_history(block.reference());
        }

        state
    }

    /// The last round that should get evicted after a cache clean up operation. After this round we are
    /// guaranteed to have all the produced blocks from that authority. For any round that is
    /// <= `last_evicted_round` we don't have such guarantees as out of order blocks might exist.
    pub(crate) fn calculate_authority_eviction_round(
        &self,
        authority_index: AuthorityIndex,
    ) -> Round {
        let last_round = self.recent_refs_by_authority[authority_index]
            .last()
            .map(|block_ref| block_ref.round)
            .unwrap_or(GENESIS_ROUND);

        Self::eviction_round(last_round, self.gc_round(), self.cached_rounds)
    }

    /// Calculates the eviction round for the given authority. The goal is to keep at least `cached_rounds`
    /// of the latest blocks in the cache (if enough data is available), while evicting blocks with rounds <= `gc_round` when possible.
    fn eviction_round(last_round: Round, gc_round: Round, cached_rounds: u32) -> Round {
        gc_round.min(last_round.saturating_sub(cached_rounds))
    }

    /// Returns the underlying store.
    pub(crate) fn store(&self) -> Arc<dyn Store> {
        self.store.clone()
    }

    /// Detects and returns the blocks of the round that forms the last quorum. The method will return
    /// the quorum even if that's genesis.
    #[cfg(test)]
    pub(crate) fn last_quorum(&self) -> Vec<VerifiedBlock> {
        // the quorum should exist either on the highest accepted round or the one before. If we fail to detect
        // a quorum then it means that our DAG has advanced with missing causal history.
        for round in
            (self.highest_accepted_round.saturating_sub(1)..=self.highest_accepted_round).rev()
        {
            if round == GENESIS_ROUND {
                return self.genesis_blocks();
            }
            use crate::stake_aggregator::{QuorumThreshold, StakeAggregator};
            let mut quorum = StakeAggregator::<QuorumThreshold>::new();

            // Since the minimum wave length is 3 we expect to find a quorum in the uncommitted rounds.
            let blocks = self.get_uncommitted_blocks_at_round(round);
            for block in &blocks {
                if quorum.add(block.author(), &self.context.committee) {
                    return blocks;
                }
            }
        }

        panic!("Fatal error, no quorum has been detected in our DAG on the last two rounds.");
    }

    #[cfg(test)]
    pub(crate) fn genesis_blocks(&self) -> Vec<VerifiedBlock> {
        self.genesis.values().cloned().collect()
    }

    #[cfg(test)]
    pub(crate) fn set_last_commit(&mut self, commit: TrustedCommit) {
        self.last_commit = Some(commit);
    }

    /// ═══════════════════════════════════════════════════════════════════
    /// DAG NETWORK BASELINE RESET: Unifies cold-start orchestration.
    /// Injects a synthetic commit at `target_round` so that `gc_round` is correctly
    /// aligned, blocks below `gc_round` are GC'd instead of deadlocking the node,
    /// and the global commit index maps to Go's state perfectly.
    ///
    /// The Coordination Hub calls this during the FastForwarding phase.
    /// SAFETY: Must only be called when DAG is empty / local_commit=0.
    /// ═══════════════════════════════════════════════════════════════════
    pub fn reset_to_network_baseline(
        &mut self,
        target_round: Round,
        synced_commit_index: crate::commit::CommitIndex,
        real_digest: crate::commit::CommitDigest,
        timestamp_ms: consensus_types::block::BlockTimestampMs,
        reputation_scores: Option<Vec<(consensus_config::AuthorityIndex, u64)>>,
    ) {
        let gc_depth = self.context.protocol_config.gc_depth();
        let target_index = synced_commit_index.max(1);
        
        let synthetic_commit = TrustedCommit::new_for_test(
            target_index,
            real_digest,
            timestamp_ms, // CRITICAL FORK-SAFETY: Must match the network's timestamp for monotonic guarantees
            BlockRef::new(
                target_round,
                consensus_config::AuthorityIndex::ZERO,
                consensus_types::block::BlockDigest::MIN,
            ),
            vec![],
            0,
        );

        tracing::warn!(
            "🧹 [DAG-RESET] Baseline injected: index={}, gc_round={}, digest_patched={}, reputation_scores_fetched={}",
            target_index,
            target_round.saturating_sub(gc_depth),
            real_digest != crate::commit::CommitDigest::MIN,
            reputation_scores.is_some()
        );

        self.last_commit = Some(synthetic_commit);
        self.baseline_reputation_scores = reputation_scores;

        // Update last_committed_rounds to the target round targeting GC efficiency
        for round in self.last_committed_rounds.iter_mut() {
            *round = std::cmp::max(*round, target_round);
        }
    }
}

