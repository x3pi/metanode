// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::sync::Arc;

use parking_lot::Mutex;

use crate::{
    block::{BlockAPI as _, VerifiedBlock},
    commit::GENESIS_COMMIT_INDEX,
    context::Context,
    CommitIndex,
};

/// Monitors the progress of consensus commits across the network.
pub struct CommitVoteMonitor {
    context: Arc<Context>,
    // Highest commit index voted by each authority.
    highest_voted_commits: Mutex<Vec<CommitIndex>>,
    // Notifier for when quorum index might have advanced
    pub quorum_advanced_notify: tokio::sync::Notify,
}

impl CommitVoteMonitor {
    pub(crate) fn new(context: Arc<Context>) -> Self {
        let highest_voted_commits = Mutex::new(vec![0; context.committee.size()]);
        Self {
            context,
            highest_voted_commits,
            quorum_advanced_notify: tokio::sync::Notify::new(),
        }
    }

    /// Keeps track of the highest commit voted by each authority.
    pub(crate) fn observe_block(&self, block: &VerifiedBlock) {
        let mut updated = false;
        {
            let mut highest_voted_commits = self.highest_voted_commits.lock();
            for vote in block.commit_votes() {
                if vote.index > highest_voted_commits[block.author()] {
                    highest_voted_commits[block.author()] = vote.index;
                    updated = true;
                }
            }
        }
        if updated {
            self.quorum_advanced_notify.notify_waiters();
        }
    }

    // Finds the highest commit index certified by a quorum.
    // When an authority votes for commit index S, it is also voting for all commit indices 1 <= i < S.
    // So the quorum commit index is the smallest index S such that the sum of stakes of authorities
    // voting for commit indices >= S passes the quorum threshold.
    pub(crate) fn quorum_commit_index(&self) -> CommitIndex {
        let highest_voted_commits = self.highest_voted_commits.lock();
        let mut highest_voted_commits = highest_voted_commits
            .iter()
            .zip(self.context.committee.authorities())
            .map(|(commit_index, (_, a))| (*commit_index, a.stake))
            .collect::<Vec<_>>();
        // Sort by commit index then stake, in descending order.
        highest_voted_commits.sort_by(|a, b| a.cmp(b).reverse());
        let mut total_stake = 0;
        for (commit_index, stake) in highest_voted_commits {
            total_stake += stake;
            if total_stake >= self.context.committee.quorum_threshold() {
                return commit_index;
            }
        }
        GENESIS_COMMIT_INDEX
    }

    /// Seeds the quorum from Go execution state to break the chicken-and-egg
    /// deadlock where blocks need quorum to be produced, but quorum needs blocks
    /// to be computed via observe_block().
    ///
    /// This is only applied when ALL authority vote slots are at 0 (i.e. no real
    /// vote data exists yet). Once real blocks arrive with commit_votes, those
    /// will naturally supersede this seed because observe_block() only advances
    /// votes forward.
    ///
    /// Returns true if seeding was applied, false if skipped (already has votes).
    pub(crate) fn seed_from_execution_state(&self, commit_index: CommitIndex) -> bool {
        if commit_index == 0 {
            return false;
        }
        let mut highest_voted_commits = self.highest_voted_commits.lock();
        // Only seed if ALL slots are at 0 — never overwrite real vote data
        let all_zero = highest_voted_commits.iter().all(|&v| v == 0);
        if !all_zero {
            return false;
        }
        for slot in highest_voted_commits.iter_mut() {
            *slot = commit_index;
        }
        drop(highest_voted_commits);
        self.quorum_advanced_notify.notify_waiters();
        true
    }
}

#[cfg(test)]
mod test {
    use std::sync::Arc;

    use super::CommitVoteMonitor;
    use crate::{
        block::{TestBlock, VerifiedBlock},
        commit::{CommitDigest, CommitRef},
        context::Context,
    };

    #[tokio::test]
    async fn test_commit_vote_monitor() {
        let context = Arc::new(Context::new_for_test(4).0);
        let monitor = CommitVoteMonitor::new(context.clone());

        // Observe commit votes for indices 5, 6, 7, 8 from blocks.
        let blocks = (0..4)
            .map(|i| {
                VerifiedBlock::new_for_test(
                    TestBlock::new(10, i)
                        .set_commit_votes(vec![CommitRef::new(5 + i, CommitDigest::MIN)])
                        .build(),
                )
            })
            .collect::<Vec<_>>();
        for b in blocks {
            monitor.observe_block(&b);
        }

        // CommitIndex 6 is the highest index supported by a quorum.
        assert_eq!(monitor.quorum_commit_index(), 6);

        // Observe new blocks with new votes from authority 0 and 1.
        let blocks = (0..2)
            .map(|i| {
                VerifiedBlock::new_for_test(
                    TestBlock::new(11, i)
                        .set_commit_votes(vec![
                            CommitRef::new(6 + i, CommitDigest::MIN),
                            CommitRef::new(7 + i, CommitDigest::MIN),
                        ])
                        .build(),
                )
            })
            .collect::<Vec<_>>();
        for b in blocks {
            monitor.observe_block(&b);
        }

        // Highest commit index per authority should be 7, 8, 7, 8 now.
        assert_eq!(monitor.quorum_commit_index(), 7);
    }
}
