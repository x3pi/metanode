// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::{BTreeMap, BTreeSet},
    sync::Arc,
    time::Instant,
};

use consensus_types::block::{BlockRef, Round};
use itertools::Itertools as _;
use parking_lot::RwLock;
use tracing::{debug, trace, warn};

use crate::{
    block::{BlockAPI, VerifiedBlock, GENESIS_ROUND},
    context::Context,
    dag_state::DagState,
};

pub mod types;
#[cfg(test)]
mod tests;

use types::{SuspendedBlock, TryAcceptResult};

/// Maximum number of suspended blocks allowed per authority. When the total suspended blocks
/// across all authorities exceeds `MAX_SUSPENDED_BLOCKS_PER_AUTHORITY * committee_size`,
/// new blocks will be skipped to prevent OOM from Byzantine validators sending blocks
const MAX_SUSPENDED_BLOCKS_PER_AUTHORITY: usize = 5000;

/// Block manager suspends incoming blocks until they are connected to the existing graph,
/// returning newly connected blocks.
/// Byzantine OOM protection: total suspended blocks are bounded by
/// `MAX_SUSPENDED_BLOCKS_PER_AUTHORITY * committee_size`.
pub(crate) struct BlockManager {
    context: Arc<Context>,
    dag_state: Arc<RwLock<DagState>>,
    dag_state_writer: crate::dag_state_actor::DagStateWriter,

    /// Keeps all the suspended blocks. A suspended block is a block that is missing part of its causal history and thus
    /// can't be immediately processed. A block will remain in this map until all its causal history has been successfully
    /// processed.
    suspended_blocks: BTreeMap<BlockRef, SuspendedBlock>,
    /// A map that keeps all the blocks that we are missing (keys) and the corresponding blocks that reference the missing blocks
    /// as ancestors and need them to get unsuspended. It is possible for a missing dependency (key) to be a suspended block, so
    /// the block has been already fetched but it self is still missing some of its ancestors to be processed.
    missing_ancestors: BTreeMap<BlockRef, BTreeSet<BlockRef>>,
    /// Keeps all the blocks that we actually miss and haven't fetched them yet. That set will basically contain all the
    /// keys from the `missing_ancestors` minus any keys that exist in `suspended_blocks`.
    missing_blocks: BTreeSet<BlockRef>,
    /// A vector that holds a tuple of (lowest_round, highest_round) of received blocks per authority.
    /// This is used for metrics reporting purposes and resets during restarts.
    received_block_rounds: Vec<Option<(Round, Round)>>,
}

impl BlockManager {
    pub(crate) fn new(context: Arc<Context>, dag_state: Arc<RwLock<DagState>>, dag_state_writer: crate::dag_state_actor::DagStateWriter) -> Self {
        let committee_size = context.committee.size();
        Self {
            context,
            dag_state,
            dag_state_writer,
            suspended_blocks: BTreeMap::new(),
            missing_ancestors: BTreeMap::new(),
            missing_blocks: BTreeSet::new(),
            received_block_rounds: vec![None; committee_size],
        }
    }

    /// Tries to accept the provided blocks assuming that all their causal history exists. The method
    /// returns all the blocks that have been successfully processed in round ascending order, that includes also previously
    /// suspended blocks that have now been able to get accepted. Method also returns a set with the missing ancestor blocks.
    #[tracing::instrument(skip_all)]
    pub(crate) fn try_accept_blocks(
        &mut self,
        blocks: Vec<VerifiedBlock>,
    ) -> (Vec<VerifiedBlock>, BTreeSet<BlockRef>) {
        /* let _s = tracing::info_span!("BlockManager::try_accept_blocks").entered(); */
        self.try_accept_blocks_internal(blocks, false)
    }

    // Tries to accept blocks that have been committed. Returns all the blocks that have been accepted, both from the ones
    // provided and any children blocks.
    #[tracing::instrument(skip_all)]
    pub(crate) fn try_accept_committed_blocks(
        &mut self,
        blocks: Vec<VerifiedBlock>,
    ) -> Vec<VerifiedBlock> {
        // Just accept the blocks
        /* let _s = tracing::info_span!("BlockManager::try_accept_committed_blocks").entered(); */
        let (accepted_blocks, missing_blocks) = self.try_accept_blocks_internal(blocks, true);
        assert!(
            missing_blocks.is_empty(),
            "No missing blocks should be returned for committed blocks"
        );

        accepted_blocks
    }

    /// Attempts to accept the provided blocks. When `committed = true` then the blocks are considered to be committed via certified commits and
    /// are handled differently.
    fn try_accept_blocks_internal(
        &mut self,
        mut blocks: Vec<VerifiedBlock>,
        committed: bool,
    ) -> (Vec<VerifiedBlock>, BTreeSet<BlockRef>) {
        /* let _s = tracing::info_span!("BlockManager::try_accept_blocks_internal").entered(); */
        let start = std::time::Instant::now();
        let mut db_total = std::time::Duration::ZERO;

        blocks.sort_by_key(|b| b.round());
        if !blocks.is_empty() {
            debug!(
                "Trying to accept blocks: {}",
                blocks.iter().map(|b| b.reference().to_string()).join(",")
            );
        }

        let mut accepted_blocks = vec![];
        let mut missing_blocks = BTreeSet::new();

        for block in blocks.clone() {
            self.update_block_received_metrics(&block);

            // Try to accept the input block.
            let block_ref = block.reference();

            let mut blocks_to_accept = vec![];
            if committed {
                match self.try_accept_one_committed_block(block) {
                    TryAcceptResult::Accepted(block) => {
                        // As this is a committed block, then it's already accepted and there is no need to verify its timestamps.
                        // Just add it to the accepted blocks list.
                        accepted_blocks.push(block);
                    }
                    TryAcceptResult::Processed => continue,
                    TryAcceptResult::Suspended(_) | TryAcceptResult::Skipped => panic!(
                        "Did not expect to suspend or skip a committed block: {:?}",
                        block_ref
                    ),
                };
            } else {
                match self.try_accept_one_block(block) {
                    TryAcceptResult::Accepted(block) => {
                        blocks_to_accept.push(block);
                    }
                    TryAcceptResult::Suspended(ancestors_to_fetch) => {
                        debug!(
                            "Missing ancestors to fetch for block {block_ref}: {}",
                            ancestors_to_fetch.iter().map(|b| b.to_string()).join(",")
                        );
                        missing_blocks.extend(ancestors_to_fetch);
                        continue;
                    }
                    TryAcceptResult::Processed | TryAcceptResult::Skipped => continue,
                };
            };

            // If the block is accepted, try to unsuspend its children blocks if any.
            let unsuspended_blocks = self.try_unsuspend_children_blocks(block_ref);
            blocks_to_accept.extend(unsuspended_blocks);

            // Insert the accepted blocks into DAG state so future blocks including them as
            // ancestors do not get suspended.
            let db_start = std::time::Instant::now();
            self.dag_state_writer
                .accept_blocks(blocks_to_accept.clone());
            db_total += db_start.elapsed();

            accepted_blocks.extend(blocks_to_accept);
        }

        self.update_stats(missing_blocks.len() as u64);

        let elapsed = start.elapsed();
        if !blocks.is_empty() {
            let refs = blocks.iter().map(|b| format!("r{}/a{}", b.round(), b.author())).collect::<Vec<_>>().join(",");
            tracing::warn!(
                "⏱️ [PERF-RUST] try_accept_blocks_internal (committed: {}, blocks: [{}], accepted: {}): total={:?}, db_write={:?}",
                committed,
                refs,
                accepted_blocks.len(),
                elapsed,
                db_total
            );
        }

        // Figure out the new missing blocks
        (accepted_blocks, missing_blocks)
    }

    fn try_accept_one_committed_block(&mut self, block: VerifiedBlock) -> TryAcceptResult {
        if self.dag_state.read().contains_block(&block.reference()) {
            return TryAcceptResult::Processed;
        }

        // Remove the block from missing and suspended blocks
        self.missing_blocks.remove(&block.reference());

        // If the block has been already fetched and parked as suspended block, then remove it. Also find all the references of missing
        // ancestors to remove those as well. If we don't do that then it's possible once the missing ancestor is fetched to cause a panic
        // when trying to unsuspend this children as it won't be found in the suspended blocks map.
        if let Some(suspended_block) = self.suspended_blocks.remove(&block.reference()) {
            suspended_block
                .missing_ancestors
                .iter()
                .for_each(|ancestor| {
                    if let Some(references) = self.missing_ancestors.get_mut(ancestor) {
                        references.remove(&block.reference());
                    }
                });
        }

        // Accept this block before any unsuspended children blocks
        self.dag_state_writer.accept_blocks(vec![block.clone()]);

        TryAcceptResult::Accepted(block)
    }

    /// Tries to find the provided block_refs in DagState and BlockManager,
    /// and returns missing block refs.
    pub(crate) fn try_find_blocks(&mut self, block_refs: Vec<BlockRef>) -> BTreeSet<BlockRef> {
        /* let _s = tracing::info_span!("BlockManager::try_find_blocks").entered(); */
        let gc_round = self.dag_state.read().gc_round();

        // No need to fetch blocks that are <= gc_round as they won't get processed anyways and they'll get skipped.
        // So keep only the ones above.
        let mut block_refs = block_refs
            .into_iter()
            .filter(|block_ref| block_ref.round > gc_round)
            .collect::<Vec<_>>();

        if block_refs.is_empty() {
            return BTreeSet::new();
        }

        block_refs.sort_by_key(|b| b.round);

        trace!(
            "Trying to find blocks: {}",
            block_refs.iter().map(|b| b.to_string()).join(",")
        );

        let mut missing_blocks = BTreeSet::new();

        for (found, block_ref) in self
            .dag_state
            .read()
            .contains_blocks(block_refs.clone())
            .into_iter()
            .zip(block_refs.iter())
        {
            if found || self.suspended_blocks.contains_key(block_ref) {
                continue;
            }
            // Fetches the block if it is not in dag state or suspended.
            missing_blocks.insert(*block_ref);
            if self.missing_blocks.insert(*block_ref) {
                // We want to report this as a missing ancestor even if there is no block that is actually references it right now. That will allow us
                // to seamlessly GC the block later if needed.
                self.missing_ancestors.entry(*block_ref).or_default();

                let block_ref_hostname =
                    &self.context.committee.authority(block_ref.author).hostname;
                self.context
                    .metrics
                    .node_metrics
                    .block_manager_missing_blocks_by_authority
                    .with_label_values(&[block_ref_hostname])
                    .inc();
            }
        }

        let metrics = &self.context.metrics.node_metrics;
        metrics
            .missing_blocks_total
            .inc_by(missing_blocks.len() as u64);
        metrics
            .block_manager_missing_blocks
            .set(self.missing_blocks.len() as i64);

        missing_blocks
    }

    /// Tries to accept the provided block. To accept a block its ancestors must have been already successfully accepted. If
    /// block is accepted then Some result is returned. None is returned when either the block is suspended or the block
    /// has been already accepted before.
    fn try_accept_one_block(&mut self, block: VerifiedBlock) -> TryAcceptResult {
        let block_ref = block.reference();
        let mut missing_ancestors = BTreeSet::new();
        let mut ancestors_to_fetch = BTreeSet::new();
        let dag_state = self.dag_state.read();
        let gc_round = dag_state.gc_round();

        // OOM guard: reject blocks when suspended blocks limit is reached to prevent Byzantine
        // validators from causing unbounded memory growth.
        let max_suspended = MAX_SUSPENDED_BLOCKS_PER_AUTHORITY * self.context.committee.size();
        if self.suspended_blocks.len() >= max_suspended {
            // ═══════════════════════════════════════════════════════════════════
            // COLD-START EVICTION: After snapshot restore, the DAG is empty and
            // blocks from round 1..N all have missing ancestors → they fill the
            // entire suspended buffer. Meanwhile, HEAD blocks (round N+1000..)
            // arrive and get skipped → threshold clock stuck → DEADLOCK.
            //
            // Fix: when a new block is ≥100 rounds above the oldest suspended
            // block, evict the oldest 50% of suspended blocks by round. Those
            // old blocks will never get their ancestors (too far behind GC on
            // peers) so evicting them is safe and frees space for HEAD blocks.
            // ═══════════════════════════════════════════════════════════════════
            let oldest_round = self
                .suspended_blocks
                .keys()
                .next()
                .map(|r| r.round)
                .unwrap_or(0);
            let incoming_round = block.round();

            if incoming_round > oldest_round + 50 {
                // Evict oldest 50% of suspended blocks
                let evict_count = self.suspended_blocks.len() / 2;
                let mut evict_refs: Vec<BlockRef> = self
                    .suspended_blocks
                    .keys()
                    .take(evict_count)
                    .cloned()
                    .collect();
                // Sort by round ascending (BTreeMap is already sorted by BlockRef which starts with round)
                evict_refs.sort_by_key(|r| r.round);

                let evict_cutoff_round = evict_refs
                    .last()
                    .map(|r| r.round)
                    .unwrap_or(0);

                for evict_ref in &evict_refs {
                    if let Some(suspended) = self.suspended_blocks.remove(evict_ref) {
                        // Clean up missing_ancestors references
                        for ancestor in &suspended.missing_ancestors {
                            if let Some(children) = self.missing_ancestors.get_mut(ancestor) {
                                children.remove(evict_ref);
                                if children.is_empty() {
                                    self.missing_ancestors.remove(ancestor);
                                    self.missing_blocks.remove(ancestor);
                                }
                            }
                        }
                    }
                }

                warn!(
                    "🧹 [COLD-START-EVICT] Evicted {} suspended blocks (rounds ≤{}) to make room \
                     for block {} at round {} (oldest_suspended_round={}, buffer was {}/{})",
                    evict_refs.len(),
                    evict_cutoff_round,
                    block_ref,
                    incoming_round,
                    oldest_round,
                    max_suspended,
                    max_suspended,
                );
                // Fall through — buffer now has space for the incoming block
            } else {
                let hostname = self
                    .context
                    .committee
                    .authority(block.author())
                    .hostname
                    .as_str();
                warn!(
                    "Suspended blocks limit reached ({}/{}), skipping block {} from {}",
                    self.suspended_blocks.len(),
                    max_suspended,
                    block_ref,
                    hostname
                );
                self.context
                    .metrics
                    .node_metrics
                    .block_manager_skipped_blocks
                    .with_label_values(&[hostname])
                    .inc();
                return TryAcceptResult::Skipped;
            }
        }

        // If block has been already received and suspended, or already processed and stored, or is a genesis block, then skip it.
        if self.suspended_blocks.contains_key(&block_ref) || dag_state.contains_block(&block_ref) {
            return TryAcceptResult::Processed;
        }

        // If the block is <= gc_round, then we simply skip its processing as there is no meaning do any action on it or even store it.
        if block.round() <= gc_round {
            let hostname = self
                .context
                .committee
                .authority(block.author())
                .hostname
                .as_str();
            self.context
                .metrics
                .node_metrics
                .block_manager_skipped_blocks
                .with_label_values(&[hostname])
                .inc();
            return TryAcceptResult::Skipped;
        }

        // Keep only the ancestors that are greater than the GC round to check for their existence.
        let ancestors = block
            .ancestors()
            .iter()
            .filter(|ancestor| ancestor.round == GENESIS_ROUND || ancestor.round > gc_round)
            .cloned()
            .collect::<Vec<_>>();

        // make sure that we have all the required ancestors in store
        for (found, ancestor) in dag_state
            .contains_blocks(ancestors.clone())
            .into_iter()
            .zip(ancestors.iter())
        {
            if !found {
                missing_ancestors.insert(*ancestor);

                // mark the block as having missing ancestors
                self.missing_ancestors
                    .entry(*ancestor)
                    .or_default()
                    .insert(block_ref);

                let ancestor_hostname = &self.context.committee.authority(ancestor.author).hostname;
                self.context
                    .metrics
                    .node_metrics
                    .block_manager_missing_ancestors_by_authority
                    .with_label_values(&[ancestor_hostname])
                    .inc();

                // Add the ancestor to the missing blocks set only if it doesn't already exist in the suspended blocks - meaning
                // that we already have its payload.
                if !self.suspended_blocks.contains_key(ancestor) {
                    // Fetches the block if it is not in dag state or suspended.
                    ancestors_to_fetch.insert(*ancestor);
                    if self.missing_blocks.insert(*ancestor) {
                        self.context
                            .metrics
                            .node_metrics
                            .block_manager_missing_blocks_by_authority
                            .with_label_values(&[ancestor_hostname])
                            .inc();
                    }
                }
            }
        }

        // Remove the block ref from the `missing_blocks` - if exists - since we now have received the block. The block
        // might still get suspended, but we won't report it as missing in order to not re-fetch.
        self.missing_blocks.remove(&block.reference());

        if !missing_ancestors.is_empty() {
            let hostname = self
                .context
                .committee
                .authority(block.author())
                .hostname
                .as_str();
            self.context
                .metrics
                .node_metrics
                .block_suspensions
                .with_label_values(&[hostname])
                .inc();
            self.suspended_blocks
                .insert(block_ref, SuspendedBlock::new(block, missing_ancestors));
            return TryAcceptResult::Suspended(ancestors_to_fetch);
        }

        TryAcceptResult::Accepted(block)
    }

    /// Given an accepted block `accepted_block` it attempts to accept all the suspended children blocks assuming such exist.
    /// All the unsuspended / accepted blocks are returned as a vector in causal order.
    fn try_unsuspend_children_blocks(&mut self, accepted_block: BlockRef) -> Vec<VerifiedBlock> {
        let mut unsuspended_blocks = vec![];
        let mut to_process_blocks = vec![accepted_block];

        while let Some(block_ref) = to_process_blocks.pop() {
            // And try to check if its direct children can be unsuspended
            if let Some(block_refs_with_missing_deps) = self.missing_ancestors.remove(&block_ref) {
                for r in block_refs_with_missing_deps {
                    // For each dependency try to unsuspend it. If that's successful then we add it to the queue so
                    // we can recursively try to unsuspend its children.
                    if let Some(block) = self.try_unsuspend_block(&r, &block_ref) {
                        to_process_blocks.push(block.block.reference());
                        unsuspended_blocks.push(block);
                    }
                }
            }
        }

        let now = Instant::now();

        // Report the unsuspended blocks
        for block in &unsuspended_blocks {
            let hostname = self
                .context
                .committee
                .authority(block.block.author())
                .hostname
                .as_str();
            self.context
                .metrics
                .node_metrics
                .block_unsuspensions
                .with_label_values(&[hostname])
                .inc();
            self.context
                .metrics
                .node_metrics
                .suspended_block_time
                .with_label_values(&[hostname])
                .observe(now.saturating_duration_since(block.timestamp).as_secs_f64());
        }

        unsuspended_blocks
            .into_iter()
            .map(|block| block.block)
            .collect()
    }

    /// Attempts to unsuspend a block by checking its ancestors and removing the `accepted_dependency` by its local set.
    /// If there is no missing dependency then this block can be unsuspended immediately and is removed from the `suspended_blocks` map.
    fn try_unsuspend_block(
        &mut self,
        block_ref: &BlockRef,
        accepted_dependency: &BlockRef,
    ) -> Option<SuspendedBlock> {
        let block = self
            .suspended_blocks
            .get_mut(block_ref)
            .expect("Block should be in suspended map");

        assert!(
            block.missing_ancestors.remove(accepted_dependency),
            "Block reference {} should be present in missing dependencies of {:?}",
            block_ref,
            block.block
        );

        if block.missing_ancestors.is_empty() {
            // we have no missing dependency, so we unsuspend the block and return it
            return self.suspended_blocks.remove(block_ref);
        }
        None
    }

    /// Tries to unsuspend any blocks for the latest gc round. If gc round hasn't changed then no blocks will be unsuspended due to
    /// this action.
    pub(crate) fn try_unsuspend_blocks_for_latest_gc_round(&mut self) {
        /* let _s = tracing::info_span!("BlockManager::try_unsuspend_blocks_for_latest_gc_round").entered(); */
        let gc_round = self.dag_state.read().gc_round();
        let mut blocks_unsuspended_below_gc_round = 0;
        let mut blocks_gc_ed = 0;

        while let Some((block_ref, _children_refs)) = self.missing_ancestors.first_key_value() {
            // If the first block in the missing ancestors is higher than the gc_round, then we can't unsuspend it yet. So we just put it back
            // and we terminate the iteration as any next entry will be of equal or higher round anyways.
            if block_ref.round > gc_round {
                return;
            }

            blocks_gc_ed += 1;

            let hostname = self
                .context
                .committee
                .authority(block_ref.author)
                .hostname
                .as_str();
            self.context
                .metrics
                .node_metrics
                .block_manager_gced_blocks
                .with_label_values(&[hostname])
                .inc();

            assert!(
                !self.suspended_blocks.contains_key(block_ref),
                "Block should not be suspended, as we are causally GC'ing and no suspended block should exist for a missing ancestor."
            );

            // Also remove it from the missing list - we don't want to keep looking for it.
            self.missing_blocks.remove(block_ref);

            // Find all the children blocks that have a dependency on this one and try to unsuspend them
            let unsuspended_blocks = self.try_unsuspend_children_blocks(*block_ref);

            unsuspended_blocks.iter().for_each(|block| {
                if block.round() <= gc_round {
                    blocks_unsuspended_below_gc_round += 1;
                }
            });

            // Now accept the unsuspended blocks
            self.dag_state_writer
                .accept_blocks(unsuspended_blocks.clone());

            for block in unsuspended_blocks {
                let hostname = self
                    .context
                    .committee
                    .authority(block.author())
                    .hostname
                    .as_str();
                self.context
                    .metrics
                    .node_metrics
                    .block_manager_gc_unsuspended_blocks
                    .with_label_values(&[hostname])
                    .inc();
            }
        }

        debug!(
            "Total {} blocks unsuspended and total blocks {} gc'ed <= gc_round {}",
            blocks_unsuspended_below_gc_round, blocks_gc_ed, gc_round
        );
    }

    /// Returns all the blocks that are currently missing and needed in order to accept suspended
    /// blocks.
    pub(crate) fn missing_blocks(&self) -> BTreeSet<BlockRef> {
        self.missing_blocks.clone()
    }

    fn update_stats(&mut self, missing_blocks: u64) {
        let metrics = &self.context.metrics.node_metrics;
        metrics.missing_blocks_total.inc_by(missing_blocks);
        metrics
            .block_manager_suspended_blocks
            .set(self.suspended_blocks.len() as i64);
        metrics
            .block_manager_missing_ancestors
            .set(self.missing_ancestors.len() as i64);
        metrics
            .block_manager_missing_blocks
            .set(self.missing_blocks.len() as i64);
    }

    fn update_block_received_metrics(&mut self, block: &VerifiedBlock) {
        let (min_round, max_round) =
            if let Some((curr_min, curr_max)) = self.received_block_rounds[block.author()] {
                (curr_min.min(block.round()), curr_max.max(block.round()))
            } else {
                (block.round(), block.round())
            };
        self.received_block_rounds[block.author()] = Some((min_round, max_round));

        let hostname = &self.context.committee.authority(block.author()).hostname;
        self.context
            .metrics
            .node_metrics
            .lowest_verified_authority_round
            .with_label_values(&[hostname])
            .set(min_round.into());
        self.context
            .metrics
            .node_metrics
            .highest_verified_authority_round
            .with_label_values(&[hostname])
            .set(max_round.into());
    }

    /// Checks if block manager is empty.
    #[cfg(test)]
    pub(crate) fn is_empty(&self) -> bool {
        self.suspended_blocks.is_empty()
            && self.missing_ancestors.is_empty()
            && self.missing_blocks.is_empty()
    }

    /// Returns all the suspended blocks whose causal history we miss hence we can't accept them yet.
    #[cfg(test)]
    fn suspended_blocks(&self) -> Vec<BlockRef> {
        self.suspended_blocks.keys().cloned().collect()
    }
}
