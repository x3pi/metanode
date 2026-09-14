// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

pub mod mem_store;
pub mod rocksdb_store;

#[cfg(test)]
mod store_tests;

use std::collections::BTreeMap;

use consensus_config::AuthorityIndex;
use consensus_types::block::{BlockRef, Round, TransactionIndex};

use crate::{
    block::VerifiedBlock,
    commit::{CommitInfo, CommitRange, CommitRef, TrustedCommit},
    error::ConsensusResult,
    CommitDigest, CommitIndex,
};

/// A common interface for consensus storage.
pub trait Store: Send + Sync {
    /// Writes blocks, consensus commits and other data to store atomically.
    fn write(&self, write_batch: WriteBatch) -> ConsensusResult<()>;

    /// Same as `write`, but durable: the write must be fsync'd to disk before returning,
    /// not just handed to the OS page cache. Default implementation just calls `write` --
    /// safe for any backend without a meaningful sync/no-sync distinction (e.g. `MemStore`
    /// in tests). `RocksDBStore` overrides this to actually request `sync(true)`.
    ///
    /// Added 2026-09-12 (note/... power-loss durability review, per explicit user request
    /// to review "if the system loses power and RAM/recent state is destroyed, does it
    /// still come back up correctly"): every write in this codebase is async by design for
    /// throughput (`ReadWriteOptions::default().sync_writes == false`, never overridden) --
    /// a genuine power loss (not a process crash/panic/abort, which the OS page cache
    /// survives fine) can lose the most recently written data that was never fsync'd. That
    /// is an acceptable trade-off for most writes (a node that "forgets" a few seconds of
    /// its own history on restart looks exactly like a node that fell behind, and the
    /// existing halt-not-guess / quorum-verification machinery already handles that safely
    /// -- see project memory "Phuong an A"). The ONE place this actually matters for BFT
    /// safety is the write that must complete BEFORE a validator broadcasts its own new
    /// block/vote to peers (`core/proposer.rs`'s flush-before-broadcast) -- a validator
    /// must not tell peers about something it cannot durably prove it did. This method
    /// exists so that ONE call site can opt into durability without changing the default
    /// for every other write in the system.
    fn write_durable(&self, write_batch: WriteBatch) -> ConsensusResult<()> {
        self.write(write_batch)
    }

    /// Reads blocks for the given refs.
    fn read_blocks(&self, refs: &[BlockRef]) -> ConsensusResult<Vec<Option<VerifiedBlock>>>;

    /// Checks if blocks exist in the store.
    fn contains_blocks(&self, refs: &[BlockRef]) -> ConsensusResult<Vec<bool>>;

    /// Reads blocks for an authority, from start_round.
    fn scan_blocks_by_author(
        &self,
        authority: AuthorityIndex,
        start_round: Round,
    ) -> ConsensusResult<Vec<VerifiedBlock>>;

    // The method returns the last `num_of_rounds` rounds blocks by author in round ascending order.
    // When a `before_round` is defined then the blocks of round `<=before_round` are returned. If not
    // then the max value for round will be used as cut off.
    fn scan_last_blocks_by_author(
        &self,
        author: AuthorityIndex,
        num_of_rounds: u64,
        before_round: Option<Round>,
    ) -> ConsensusResult<Vec<VerifiedBlock>>;

    /// Reads the last commit.
    fn read_last_commit(&self) -> ConsensusResult<Option<TrustedCommit>>;

    /// Reads all commits from start (inclusive) until end (inclusive).
    fn scan_commits(&self, range: CommitRange) -> ConsensusResult<Vec<TrustedCommit>>;

    /// Reads all blocks voting on a particular commit.
    fn read_commit_votes(
        &self,
        commit_index: CommitIndex,
        commit_digest: CommitDigest,
    ) -> ConsensusResult<Vec<BlockRef>>;

    /// Reads the last commit info, written atomically with the last commit.
    fn read_last_commit_info(&self) -> ConsensusResult<Option<(CommitRef, CommitInfo)>>;

    /// Reads commit info for a specific commit.
    fn read_commit_info(
        &self,
        commit_index: CommitIndex,
        commit_digest: CommitDigest,
    ) -> ConsensusResult<Option<CommitInfo>>;

    /// Reads the last finalized commit.
    fn read_last_finalized_commit(&self) -> ConsensusResult<Option<CommitRef>>;

    // Reads rejected transactions by block for a given commit.
    fn read_rejected_transactions(
        &self,
        commit_ref: CommitRef,
    ) -> ConsensusResult<Option<BTreeMap<BlockRef, Vec<TransactionIndex>>>>;

    /// Reads genesis block refs for the current epoch.
    fn read_genesis_blocks(&self, epoch: u64) -> ConsensusResult<Option<Vec<BlockRef>>>;

    /// Writes genesis block refs for a specific epoch.
    fn write_genesis_blocks(
        &self,
        epoch: u64,
        genesis_blocks: Vec<BlockRef>,
    ) -> ConsensusResult<()>;
}

/// Represents data to be written to the store together atomically.
#[derive(Debug, Default)]
pub struct WriteBatch {
    pub blocks: Vec<VerifiedBlock>,
    pub commits: Vec<TrustedCommit>,
    pub commits_to_delete: Vec<(CommitIndex, CommitDigest)>,
    pub commit_info: Vec<(CommitRef, CommitInfo)>,
    pub finalized_commits: Vec<(CommitRef, BTreeMap<BlockRef, Vec<TransactionIndex>>)>,
}

impl WriteBatch {
    pub fn new(
        blocks: Vec<VerifiedBlock>,
        commits: Vec<TrustedCommit>,
        commits_to_delete: Vec<(CommitIndex, CommitDigest)>,
        commit_info: Vec<(CommitRef, CommitInfo)>,
        finalized_commits: Vec<(CommitRef, BTreeMap<BlockRef, Vec<TransactionIndex>>)>,
    ) -> Self {
        WriteBatch {
            blocks,
            commits,
            commits_to_delete,
            commit_info,
            finalized_commits,
        }
    }

    // Test setters.

    #[cfg(test)]
    pub fn blocks(mut self, blocks: Vec<VerifiedBlock>) -> Self {
        self.blocks = blocks;
        self
    }

    #[cfg(test)]
    pub fn commits(mut self, commits: Vec<TrustedCommit>) -> Self {
        self.commits = commits;
        self
    }

    #[cfg(test)]
    pub fn commit_info(mut self, commit_info: Vec<(CommitRef, CommitInfo)>) -> Self {
        self.commit_info = commit_info;
        self
    }
}
