// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::BTreeSet,
    time::Instant,
};

use consensus_types::block::BlockRef;
use crate::VerifiedBlock;

pub(crate) struct SuspendedBlock {
    pub(crate) block: VerifiedBlock,
    pub(crate) missing_ancestors: BTreeSet<BlockRef>,
    pub(crate) timestamp: Instant,
}

impl SuspendedBlock {
    pub(crate) fn new(block: VerifiedBlock, missing_ancestors: BTreeSet<BlockRef>) -> Self {
        Self {
            block,
            missing_ancestors,
            timestamp: Instant::now(),
        }
    }
}

// Result of trying to accept one block.
pub(crate) enum TryAcceptResult {
    // The block is accepted. Wraps the block itself.
    Accepted(VerifiedBlock),
    // The block is suspended. Wraps ancestors to be fetched.
    Suspended(BTreeSet<BlockRef>),
    // The block has been processed before and already exists in BlockManager (and is suspended) or
    // in DagState (so has been already accepted). No further processing has been done at this point.
    Processed,
    // When a received block is <= gc_round, then we simply skip its processing as there is no meaning
    // do any action on it or even store it.
    Skipped,
}
