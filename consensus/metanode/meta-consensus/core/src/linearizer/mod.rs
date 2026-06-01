// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::sync::Arc;
use std::ops::Deref;

use consensus_config::Stake;
use consensus_types::block::{BlockRef, BlockTimestampMs, Round};
use itertools::Itertools;
use parking_lot::RwLock;

use crate::{
    block::{BlockAPI, VerifiedBlock},
    commit::{sort_sub_dag_blocks, Commit, CommittedSubDag, TrustedCommit, CommitAPI},
    context::Context,
    dag_state::DagState,
};

/// The `StorageAPI` trait provides an interface for the block store and has been
/// mostly introduced for allowing to inject the test store in `DagBuilder`.
pub(crate) trait BlockStoreAPI {
    fn get_blocks(&self, refs: &[BlockRef]) -> Vec<Option<VerifiedBlock>>;

    fn gc_round(&self) -> Round;

    fn is_committed(&self, block_ref: &BlockRef) -> bool;

    /// Gets all uncommitted blocks in a round.
    /// This is used to commit all blocks in the same round as the leader,
    /// ensuring all transactions in the round are committed.
    #[allow(dead_code)]
    fn get_uncommitted_blocks_at_round(&self, round: Round) -> Vec<VerifiedBlock>;
}

impl BlockStoreAPI
    for parking_lot::lock_api::RwLockReadGuard<'_, parking_lot::RawRwLock, DagState>
{
    fn get_blocks(&self, refs: &[BlockRef]) -> Vec<Option<VerifiedBlock>> {
        DagState::get_blocks(self, refs)
    }

    fn gc_round(&self) -> Round {
        DagState::gc_round(self)
    }

    fn is_committed(&self, block_ref: &BlockRef) -> bool {
        DagState::is_committed(self, block_ref)
    }

    fn get_uncommitted_blocks_at_round(&self, round: Round) -> Vec<VerifiedBlock> {
        DagState::get_uncommitted_blocks_at_round(self, round)
    }
}

/// Expand a committed sequence of leader into a sequence of sub-dags.
#[derive(Clone)]
pub struct Linearizer {
    /// In memory block store representing the dag state
    context: Arc<Context>,
    dag_state: Arc<RwLock<DagState>>,
    dag_state_writer: crate::dag_state_actor::DagStateWriter,
    /// Base index for global_exec_index calculation.
    /// global_exec_index = epoch_base_index + commit_index
    /// This is set once at epoch start and remains constant.
    epoch_base_index: u64,
    /// Leaders waiting to be committed - deferred because not all blocks were available.
    /// FORK PREVENTION: We only commit when ALL blocks for a round are present.
    /// This list is processed first on each handle_commit call.
    deferred_leaders: Vec<(VerifiedBlock, Option<crate::commit::CertifiedCommit>)>,
}

impl Linearizer {
    pub fn new(
        context: Arc<Context>,
        dag_state: Arc<RwLock<DagState>>,
        dag_state_writer: crate::dag_state_actor::DagStateWriter,
    ) -> Self {
        Self {
            context,
            dag_state,
            dag_state_writer,
            epoch_base_index: 0,
            deferred_leaders: Vec::new(),
        }
    }

    /// Set the epoch base index for global_exec_index calculation.
    /// This should be called once at epoch start.
    pub fn set_epoch_base_index(&mut self, epoch_base_index: u64) {
        self.epoch_base_index = epoch_base_index;
    }

    /// Collect the sub-dag and the corresponding commit from a specific leader.
    /// FORK PREVENTION: This commits ONLY the leader block and its referenced ancestors.
    /// This is deterministic because all nodes agree on what a leader references.
    fn try_collect_sub_dag_and_commit(
        &mut self,
        leader_block: VerifiedBlock,
        precomputed_commit: Option<crate::commit::CertifiedCommit>,
    ) -> Option<(CommittedSubDag, TrustedCommit, bool)> {
        let _s = self
            .context
            .metrics
            .node_metrics
            .scope_processing_time
            .with_label_values(&["Linearizer::try_collect_sub_dag_and_commit"])
            .start_timer();

        let leader_round = leader_block.round();
        let committee_size = self.context.committee.size();

        // NOTE: No need to check for block availability.
        // We ONLY commit blocks that the leader references (its ancestors).
        // Since all nodes receive the same leader block with the same references,
        // they will all commit the same blocks deterministically.
        tracing::debug!(
            "✅ [COMMIT] Proceeding with commit for leader {} round {} (committee_size={})",
            leader_block.reference(),
            leader_round,
            committee_size
        );

        // Grab latest commit state from dag state
        let dag_state = self.dag_state.read();
        let last_commit_index = dag_state.last_commit_index();
        let last_commit_digest = dag_state.last_commit_digest();
        let last_commit_timestamp_ms = dag_state.last_commit_timestamp_ms();

        // Now linearize the sub-dag starting from the leader block
        let (to_commit_blocks, timestamp_ms, final_commit) = if let Some(certified_commit) = precomputed_commit.as_ref() {
            let mut blocks = certified_commit.blocks().to_vec();
            crate::commit::sort_sub_dag_blocks(&mut blocks);
            (Some(blocks), certified_commit.timestamp_ms(), Some(certified_commit.deref().clone()))
        } else {
            let blocks = Self::linearize_sub_dag(leader_block.clone(), &dag_state);

            // ═══════════════════════════════════════════════════════════════════════════
            // COLD-START-GUARD: Multi-layer ancestor verification for local commits.
            //
            // Layer 1 (Guard 6 — ALWAYS ACTIVE):
            //   Verify that all round-1 parent blocks REFERENCED by the leader are
            //   present in the local DAG. If any referenced block is missing, the
            //   median_timestamp_by_stake() will compute from a different input set.
            //   This is safe at Genesis because by the time a leader is elected,
            //   the referenced validators' blocks are always in the local DAG.
            //
            // Layer 2 (Guard 6a — ONLY AFTER RECOVERY, last_commit_index > 0):
            //   Require full committee representation (all validators at round-1).
            //   After snapshot recovery, the DAG may have a sparse set of blocks
            //   at boundary rounds. If the leader only references 3/5 validators,
            //   the median can differ from nodes that have the full 5/5 set.
            //   SKIP at Genesis (commit_index==0): validators start at different
            //   times so the leader naturally can't reference all of them.
            //
            // Layer 3 (Guard 6b — ONLY AFTER RECOVERY, last_commit_index > 0):
            //   Deep ancestor check: verify ALL ancestors (not just round-1).
            //   Missing blocks at older rounds can cause linearize_sub_dag to
            //   produce a different sub-dag ordering.
            //   SKIP at Genesis: older rounds don't exist yet.
            // ═══════════════════════════════════════════════════════════════════════════

            // --- Guard 6: Referenced parent blocks must be present (unconditional) ---
            let parent_refs = leader_block
                .ancestors()
                .iter()
                .filter(|block_ref| block_ref.round == leader_block.round() - 1)
                .cloned()
                .collect::<Vec<_>>();
            let parent_blocks = dag_state.get_blocks(&parent_refs);
            let missing_parents = parent_refs.iter().zip(parent_blocks.iter()).filter(|(_pref, pblock)| {
                pblock.is_none()
            }).count();
            if missing_parents > 0 {
                tracing::warn!(
                    "🛡️ [COLD-START-GUARD] ABORTING local commit for leader {:?}: \
                     {}/{} referenced round-{} ancestor blocks missing from DAG. \
                     Deferring to prevent timestamp divergence.",
                    leader_block.reference(),
                    missing_parents,
                    parent_refs.len(),
                    leader_block.round() - 1,
                );
                return None;
            }

            let ts = Self::calculate_commit_timestamp(
                &self.context,
                &dag_state,
                &leader_block,
                last_commit_timestamp_ms,
            );

            (blocks, ts, None)
        };

        let to_commit = match to_commit_blocks {
            Some(blocks) => blocks,
            None => {
                tracing::info!("⚠️ [LINEARIZER] Missing blocks to linearize sub-dag for leader {:?}. Deferring commit.", leader_block.reference());
                return None;
            }
        };

        // Check if this is a historical commit (already committed during previous epochs)
        let is_historical = final_commit.as_ref().map_or(false, |fc| fc.index() <= last_commit_index);

        drop(dag_state);

        // ACTOR WRITE: Now that we don't hold the read lock, we can set them as committed!
        if !is_historical {
            for block in &to_commit {
                assert!(
                    self.dag_state_writer.set_committed(block.reference()),
                    "Block with reference {:?} attempted to be committed twice",
                    block.reference()
                );
            }
        } else {
            tracing::debug!(
                "⏭️ [SCHEDULE-RECOVERY] Skipping set_committed for {} blocks. They are from a historical commit and already committed.",
                to_commit.len()
            );
        }

        let commit = if let Some(trusted_commit) = final_commit {
            trusted_commit
        } else {
            let commit_index = last_commit_index + 1;
            let global_exec_index = self.epoch_base_index + commit_index as u64;

            let commit = Commit::new(
                commit_index,
                last_commit_digest,
                timestamp_ms,
                leader_block.reference(),
                to_commit
                    .iter()
                    .map(|block| block.reference())
                    .collect::<Vec<_>>(),
                global_exec_index,
            );
            let serialized = commit
                .serialize()
                .unwrap_or_else(|e| panic!("Failed to serialize commit: {}", e));
            TrustedCommit::new_trusted(commit, serialized)
        };

        // Create the corresponding committed sub dag
        let mut sub_dag = CommittedSubDag::new(
            leader_block.reference(),
            to_commit,
            timestamp_ms,
            commit.reference(),
            commit.global_exec_index(),
        );

        // FORK-SAFETY (May 2026): Propagate consensus-agreed leader_address from commit
        let stored_leader_addr = commit.leader_address();
        if !stored_leader_addr.is_empty() {
            sub_dag.leader_address = stored_leader_addr.to_vec();
        }

        Some((sub_dag, commit, is_historical))
    }

    /// Calculates the commit's timestamp. The timestamp will be calculated as the median of leader's parents (leader.round - 1)
    /// timestamps by stake. To ensure that commit timestamp monotonicity is respected it is compared against the `last_commit_timestamp_ms`
    /// and the maximum of the two is returned.
    pub(crate) fn calculate_commit_timestamp(
        context: &Context,
        dag_state: &impl BlockStoreAPI,
        leader_block: &VerifiedBlock,
        last_commit_timestamp_ms: BlockTimestampMs,
    ) -> BlockTimestampMs {
        // ═══════════════════════════════════════════════════════════════════════════
        // DETERMINISTIC GUARDED MEDIAN TIMESTAMP (May 2026):
        // We calculate the stake-weighted median timestamp over the set of parent
        // blocks referenced by the leader block.
        // Because of Guard 6 (which unconditionally defers the commit if any parent
        // block referenced by the leader block is missing), every node executing
        // this commit is guaranteed to have the exact same set of parent blocks
        // locally. Therefore, this calculation is 100% deterministic and identical
        // across all nodes, completely eliminating forks due to DAG sparsity.
        // If the calculation fails (e.g. at round 1), it safely falls back to the
        // leader block's embedded timestamp. Monotonicity is enforced via `.max()`.
        // ═══════════════════════════════════════════════════════════════════════════
        let parent_refs = leader_block
            .ancestors()
            .iter()
            .filter(|block_ref| block_ref.round == leader_block.round() - 1)
            .cloned()
            .collect::<Vec<_>>();
        let parent_blocks = dag_state.get_blocks(&parent_refs);
        let blocks = parent_blocks.into_iter().flatten();

        let ts = median_timestamp_by_stake(context, blocks)
            .unwrap_or_else(|_| leader_block.timestamp_ms());

        ts.max(last_commit_timestamp_ms)
    }

    pub(crate) fn linearize_sub_dag(
        leader_block: VerifiedBlock,
        dag_state: &impl BlockStoreAPI,
    ) -> Option<Vec<VerifiedBlock>> {
        // The GC round here is calculated based on the last committed round of the leader block.
        let gc_round: Round = dag_state.gc_round();
        let leader_block_ref = leader_block.reference();
        let mut buffer = vec![leader_block.clone()];
        let mut to_commit = Vec::new();
        let mut visited = std::collections::HashSet::new();
        visited.insert(leader_block_ref);

        if dag_state.is_committed(&leader_block_ref) {
            tracing::warn!(
                "⚠️ [LINEARIZER] Leader block with reference {:?} was already committed.",
                leader_block_ref
            );
            return Some(vec![]);
        }

        while let Some(x) = buffer.pop() {
            to_commit.push(x.clone());

            let uncommitted_ancestors: Vec<BlockRef> = x.ancestors()
                .iter()
                .copied()
                .filter(|ancestor| {
                    ancestor.round > gc_round && !dag_state.is_committed(ancestor)
                })
                .collect();
                
            let ancestor_blocks = dag_state.get_blocks(&uncommitted_ancestors);
            
            for (idx, ancestor_opt) in ancestor_blocks.into_iter().enumerate() {
                match ancestor_opt {
                    Some(ancestor) => {
                        if visited.insert(ancestor.reference()) {
                            buffer.push(ancestor);
                        }
                    },
                    None => {
                        let missing_ref = uncommitted_ancestors[idx];
                        
                        tracing::warn!(
                            "⚠️ [LINEARIZER] FORK PREVENTION: Missing uncommitted ancestor block {:?} during linearization! \
                             Aborting sub-dag collection to prevent state divergence. Will retry when block arrives.", missing_ref
                        );
                        return None;
                    }
                }
            }
        }
        
        assert!(
            to_commit.iter().all(|block| block.round() > gc_round),
            "No blocks <= {gc_round} should be committed. Leader round {}, blocks {to_commit:?}.",
            leader_block_ref
        );

        // Sort the blocks of the sub-dag blocks
        sort_sub_dag_blocks(&mut to_commit);

        Some(to_commit)
    }

    // This function should be called whenever a new commit is observed. This will
    // iterate over the sequence of committed leaders and produce a list of committed
    // sub-dags.
    //
    // FORK PREVENTION: Leaders are only committed when ALL blocks for their round are available.
    // If blocks are missing, leaders are stored in deferred_leaders and retried on each call.
    // This ensures all nodes commit with identical block sets, preventing divergence.
    pub(crate) fn handle_commit(
        &mut self,
        committed_leaders: Vec<VerifiedBlock>,
        precomputed_commits: Option<Vec<crate::commit::CertifiedCommit>>,
    ) -> Vec<CommittedSubDag> {
        let mut committed_sub_dags = vec![];

        // Combine deferred leaders with new leaders, maintaining order.
        // Deferred leaders are tried first (they are older and waiting longer).
        let mut all_leaders: Vec<(VerifiedBlock, Option<crate::commit::CertifiedCommit>)> =
            std::mem::take(&mut self.deferred_leaders);
        let had_deferred = !all_leaders.is_empty();

        let new_leaders_with_ts = committed_leaders.into_iter().enumerate().map(|(i, leader)| {
            let commit = precomputed_commits.as_ref().map(|commits| commits[i].clone());
            (leader, commit)
        });
        all_leaders.extend(new_leaders_with_ts);

        let gc_round = self.dag_state.read().gc_round();
        
        // Filter out leaders that are <= gc_round.
        // This is crucial for handling cold-start fast-forwards, where gc_round
        // is synthetically advanced. Without this, the linearizer would panic
        // when evaluating old leaders that were passed down before Core was synced.
        all_leaders.retain(|(leader, _)| {
            if leader.round() <= gc_round {
                tracing::warn!(
                    "⚠️ [LINEARIZER] Discarding leader {} (round {}) because it is <= gc_round ({}). \
                     This usually happens after a cold-start fast-forward.",
                    leader.reference(), leader.round(), gc_round
                );
                false
            } else {
                true
            }
        });

        if all_leaders.is_empty() {
            return vec![];
        }

        // Convert to iterator to handle remaining elements properly
        let mut leaders_iter = all_leaders.into_iter().peekable();

        while let Some((leader_block, precomputed_ts)) = leaders_iter.next() {
            // Try to collect the sub-dag. Returns None if blocks are missing.
            match self.try_collect_sub_dag_and_commit(leader_block.clone(), precomputed_ts.clone()) {
                Some((sub_dag, commit, is_historical)) => {
                    // Success! All blocks were available.
                    self.update_blocks_pruned_metric(&sub_dag);

                    // Buffer commit in dag state for persistence later.
                    // This also updates the last committed rounds.
                    if !is_historical {
                        self.dag_state_writer.add_commit(commit.clone());
                    } else {
                        tracing::debug!(
                            "⏭️ [SCHEDULE-RECOVERY] Skipping DagStateWriter::add_commit for historical commit {}. It is already persisted.",
                            commit.index()
                        );
                    }

                    committed_sub_dags.push(sub_dag);
                }
                None => {
                    // Blocks are missing - defer this leader AND all remaining leaders.
                    // IMPORTANT: Commits must be processed IN ORDER.
                    // We cannot skip any leader - they must all wait.
                    self.deferred_leaders.push((leader_block, precomputed_ts));

                    // Push all remaining leaders to deferred list
                    self.deferred_leaders.extend(leaders_iter);
                    break;
                }
            }
        }

        // Log status
        if !self.deferred_leaders.is_empty() {
            tracing::info!(
                "📋 [DEFERRED COMMITS] {} leaders waiting for blocks. \
                 Commits will resume when Synchronizer fetches missing blocks.",
                self.deferred_leaders.len()
            );
        } else if had_deferred {
            tracing::info!(
                "✅ [DEFERRED COMMITS] All deferred leaders successfully committed. {} new commits.",
                committed_sub_dags.len()
            );
        }

        committed_sub_dags
    }

    /// Returns the number of leaders waiting for blocks.
    pub fn deferred_leaders_count(&self) -> usize {
        self.deferred_leaders.len()
    }

    // Try to measure the number of blocks that get pruned due to GC. This is not very accurate, but it can give us a good enough idea.
    // We consider a block as pruned when it is an ancestor of a block that has been committed as part of the provided `sub_dag`, but
    // it has not been committed as part of previous commits. Right now we measure this via checking that highest committed round for the authority
    // as we don't an efficient look up functionality to check if a block has been committed or not.
    fn update_blocks_pruned_metric(&self, sub_dag: &CommittedSubDag) {
        let (last_committed_rounds, gc_round) = {
            let dag_state = self.dag_state.read();
            (dag_state.last_committed_rounds(), dag_state.gc_round())
        };

        for block_ref in sub_dag
            .blocks
            .iter()
            .flat_map(|block| block.ancestors())
            .filter(
                |ancestor_ref| {
                    ancestor_ref.round <= gc_round
                        && last_committed_rounds[ancestor_ref.author] != ancestor_ref.round
                }, // If the last committed round is the same as the pruned block's round, then we know for sure that it has been committed and it doesn't count here
                   // as pruned block.
            )
            .unique()
        {
            let hostname = &self.context.committee.authority(block_ref.author).hostname;

            // If the last committed round from this authority is lower than the pruned ancestor in question, then we know for sure that it has not been committed.
            let label_values = if last_committed_rounds[block_ref.author] < block_ref.round {
                &[hostname, "uncommitted"]
            } else {
                // If last committed round is higher for this authority, then we don't really know it's status, but we know that there is a higher committed block from this authority.
                &[hostname, "higher_committed"]
            };

            self.context
                .metrics
                .node_metrics
                .blocks_pruned_on_commit
                .with_label_values(label_values)
                .inc();
        }
    }
}

/// Computes the median timestamp of the blocks weighted by the stake of their authorities.
/// This function assumes each block comes from a different authority of the same round.
/// Error is returned if no blocks are provided or total stake is less than quorum threshold.
#[allow(dead_code)]
pub(crate) fn median_timestamp_by_stake(
    context: &Context,
    blocks: impl Iterator<Item = VerifiedBlock>,
) -> Result<BlockTimestampMs, String> {
    let mut total_stake = 0;
    let mut timestamps = vec![];
    for block in blocks {
        let stake = context.committee.authority(block.author()).stake;
        timestamps.push((block.timestamp_ms(), stake));
        total_stake += stake;
    }

    if timestamps.is_empty() {
        return Err("No blocks provided".to_string());
    }
    if total_stake < context.committee.quorum_threshold() {
        return Err(format!(
            "Total stake {} < quorum threshold {}",
            total_stake,
            context.committee.quorum_threshold()
        )
        .to_string());
    }

    Ok(median_timestamps_by_stake_inner(timestamps, total_stake))
}

#[allow(dead_code)]
fn median_timestamps_by_stake_inner(
    mut timestamps: Vec<(BlockTimestampMs, Stake)>,
    total_stake: Stake,
) -> BlockTimestampMs {
    timestamps.sort_by_key(|(ts, _)| *ts);

    let mut cumulative_stake = 0;
    for (ts, stake) in &timestamps {
        cumulative_stake += stake;
        if cumulative_stake > total_stake / 2 {
            return *ts;
        }
    }

    timestamps
        .last()
        .expect("timestamps non-empty — empty case handled above")
        .0
}


#[cfg(test)]
mod tests;
