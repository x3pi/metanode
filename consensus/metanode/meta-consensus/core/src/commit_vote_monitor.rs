// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use parking_lot::Mutex;

use crate::{
    block::{BlockAPI as _, VerifiedBlock},
    commit::{CommitDigest, GENESIS_COMMIT_INDEX},
    context::Context,
    CommitIndex,
};

struct VoteState {
    // Highest commit index voted by each authority.
    highest_voted_commits: Vec<CommitIndex>,
    // FORK-FIX (May 2026): Full digest history per commit index.
    // GC: entries below quorum_commit_index - DIGEST_HISTORY_RETAIN are pruned
    digest_history: BTreeMap<CommitIndex, HashMap<CommitDigest, u64>>,
    // Tracks which authority has already voted for which commit index.
    authority_voted_indices: Vec<std::collections::HashSet<CommitIndex>>,
}

/// Monitors the progress of consensus commits across the network.
pub struct CommitVoteMonitor {
    context: Arc<Context>,
    state: Mutex<VoteState>,
    // Notifier for when quorum index might have advanced
    pub quorum_advanced_notify: tokio::sync::Notify,
}

/// Number of commit indices to retain in digest_history below the current
/// quorum_commit_index. Prevents unbounded memory growth while ensuring
/// recently-buffered local commits can still be verified.
const DIGEST_HISTORY_RETAIN: u32 = 50;

impl CommitVoteMonitor {
    pub(crate) fn new(context: Arc<Context>) -> Self {
        let size = context.committee.size();
        let state = VoteState {
            highest_voted_commits: vec![0; size],
            digest_history: BTreeMap::new(),
            authority_voted_indices: (0..size).map(|_| std::collections::HashSet::new()).collect(),
        };
        Self {
            context,
            state: Mutex::new(state),
            quorum_advanced_notify: tokio::sync::Notify::new(),
        }
    }

    /// Keeps track of the highest commit voted by each authority.
    /// Also accumulates digest stake votes per commit index for DIGEST-GATE.
    pub(crate) fn observe_block(&self, block: &VerifiedBlock) {
        let mut updated = false;
        let author = block.author();
        {
            let mut state = self.state.lock();

            for vote in block.commit_votes() {
                // Update highest voted commit (for quorum_commit_index calculation)
                if vote.index > state.highest_voted_commits[author] {
                    state.highest_voted_commits[author] = vote.index;
                    updated = true;
                }

                // Accumulate digest vote for this specific commit index.
                // Only count each (authority, commit_index) pair ONCE to prevent
                // double-counting from duplicate blocks during catch-up.
                if state.authority_voted_indices[author].insert(vote.index) {
                    let authority_stake = self.context.committee.authority(author).stake;
                    let entry = state.digest_history
                        .entry(vote.index)
                        .or_insert_with(HashMap::new);
                    *entry.entry(vote.digest).or_insert(0) += authority_stake;
                }
            }

            // GC: Prune old digest history entries to prevent unbounded growth.
            // Keep entries from (quorum - RETAIN) onwards.
            let gc_below = self.compute_quorum_index_inner(&state.highest_voted_commits)
                .saturating_sub(DIGEST_HISTORY_RETAIN);
            if gc_below > 0 {
                // Split off entries below gc_below
                let to_keep = state.digest_history.split_off(&gc_below);
                state.digest_history = to_keep;
                // Also clean authority_voted for pruned indices
                for voted_set in state.authority_voted_indices.iter_mut() {
                    voted_set.retain(|idx| *idx >= gc_below);
                }
            }
        }
        if updated {
            self.quorum_advanced_notify.notify_waiters();
        }
    }

    /// Internal helper: compute quorum commit index from a locked highest_voted_commits vec.
    fn compute_quorum_index_inner(&self, highest_voted_commits: &[CommitIndex]) -> CommitIndex {
        let mut votes: Vec<(CommitIndex, u64)> = highest_voted_commits
            .iter()
            .zip(self.context.committee.authorities())
            .map(|(commit_index, (_, a))| (*commit_index, a.stake))
            .collect();
        votes.sort_by(|a, b| a.cmp(b).reverse());
        let mut total_stake = 0;
        for (commit_index, stake) in votes {
            total_stake += stake;
            if total_stake >= self.context.committee.quorum_threshold() {
                return commit_index;
            }
        }
        GENESIS_COMMIT_INDEX
    }

    // Finds the highest commit index certified by a quorum.
    // When an authority votes for commit index S, it is also voting for all commit indices 1 <= i < S.
    // So the quorum commit index is the smallest index S such that the sum of stakes of authorities
    // voting for commit indices >= S passes the quorum threshold.
    pub(crate) fn quorum_commit_index(&self) -> CommitIndex {
        let state = self.state.lock();
        self.compute_quorum_index_inner(&state.highest_voted_commits)
    }

    /// Returns the quorum-agreed digest for a specific commit index.
    /// If 2f+1 authorities voted for the same digest at this index, returns Some(digest).
    /// If there's no quorum agreement on digest (divergent DAG), returns None.
    ///
    /// FORK-FIX (May 2026): Now queries from persistent `digest_history` instead of
    /// `highest_voted_digests`. This means digest votes are NOT lost when authorities
    /// advance to higher commit indices, fixing the deadlock where DIGEST-GATE
    /// could never verify any commit because votes were always overwritten.
    pub fn quorum_commit_digest(&self, target_index: CommitIndex) -> Option<CommitDigest> {
        let state = self.state.lock();

        let digest_stakes = match state.digest_history.get(&target_index) {
            Some(stakes) => stakes,
            None => return None, // No votes recorded for this index (GC'd or never seen)
        };

        // Find the digest that reached quorum
        if let Some((best_digest, best_stake)) = digest_stakes.iter().max_by_key(|&(_, s)| *s) {
            if *best_stake >= self.context.committee.quorum_threshold() {
                return Some(*best_digest);
            }
        }

        // No single digest reached quorum at this index
        None
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
        let mut state = self.state.lock();

        // Calculate the current quorum directly from the locked state
        let current_quorum = self.compute_quorum_index_inner(&state.highest_voted_commits);

        // Only seed if current quorum is 0. If it's > 0, we already have real network agreement.
        if current_quorum > 0 {
            return false;
        }

        let mut updated = false;
        for slot in state.highest_voted_commits.iter_mut() {
            if *slot < commit_index {
                *slot = commit_index;
                updated = true;
            }
        }
        drop(state);
        if updated {
            self.quorum_advanced_notify.notify_waiters();
        }
        updated
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
