// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet};
use consensus_types::block::{BlockRef, TransactionIndex};
use crate::{block::BlockAPI, CommittedSubDag, VerifiedBlock, CommitIndex};

pub(crate) struct CommitState {
    pub(crate) commit: CommittedSubDag,
    // Blocks pending finalization, mapped to the number of transactions in the block.
    // This field is populated by all blocks in the commit, before direct finalization.
    // After direct finalization, this field becomes empty.
    pub(crate) pending_blocks: BTreeMap<BlockRef, usize>,
    // Transactions pending indirect finalization.
    // This field is populated after direct finalization, if pending transactions exist.
    // Values in this field are removed as transactions are indirectly finalized or directly rejected.
    // When both pending_blocks and pending_transactions are empty, the commit is finalized.
    pub(crate) pending_transactions: BTreeMap<BlockRef, BTreeSet<TransactionIndex>>,
    // Transactions rejected by a quorum or indirectly, per block.
    pub(crate) rejected_transactions: BTreeMap<BlockRef, BTreeSet<TransactionIndex>>,
}

impl CommitState {
    pub(crate) fn new(commit: CommittedSubDag) -> Self {
        let pending_blocks: BTreeMap<_, _> = commit
            .blocks
            .iter()
            .map(|b| (b.reference(), b.transactions().len()))
            .collect();
        assert!(!pending_blocks.is_empty());
        Self {
            commit,
            pending_blocks,
            pending_transactions: BTreeMap::new(),
            rejected_transactions: BTreeMap::new(),
        }
    }

    pub(crate) fn remove_pending_transactions(
        &mut self,
        block_ref: &BlockRef,
        transactions: &[TransactionIndex],
    ) {
        let Some(block_pending_txns) = self.pending_transactions.get_mut(block_ref) else {
            return;
        };
        for t in transactions {
            block_pending_txns.remove(t);
        }
        if block_pending_txns.is_empty() {
            self.pending_transactions.remove(block_ref);
        }
    }
}

pub(crate) struct BlockState {
    // Content of the block.
    pub(crate) block: VerifiedBlock,
    // Blocks which has an explicit ancestor linking to this block.
    pub(crate) children: BTreeSet<BlockRef>,
    // Reject votes casted by this block, and by linked ancestors from the same authority.
    pub(crate) reject_votes: BTreeMap<BlockRef, BTreeSet<TransactionIndex>>,
    // Other committed blocks that are origin descendants of this block.
    // See the comment above append_origin_descendants_from_last_commit() for more details.
    pub(crate) origin_descendants: Vec<BlockRef>,
    // Commit which contains this block.
    pub(crate) commit_index: CommitIndex,
}

impl BlockState {
    pub(crate) fn new(block: VerifiedBlock, commit_index: CommitIndex) -> Self {
        let reject_votes: BTreeMap<_, _> = block
            .transaction_votes()
            .iter()
            .map(|v| (v.block_ref, v.rejects.clone().into_iter().collect()))
            .collect();
        // With at most 4 pending commits and assume 2 origin descendants per commit,
        // there will be at most 8 origin descendants.
        let origin_descendants = Vec::with_capacity(8);
        Self {
            block,
            children: BTreeSet::new(),
            reject_votes,
            origin_descendants,
            commit_index,
        }
    }
}
