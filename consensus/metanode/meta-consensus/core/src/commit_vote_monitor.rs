// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::sync::Arc;

use parking_lot::Mutex;
use serde::{Deserialize, Serialize};

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
    // Tracks what digest each authority has voted for per commit index: authority -> (CommitIndex -> CommitDigest)
    authority_voted_commits: Vec<HashMap<CommitIndex, CommitDigest>>,
    // Tracks commit indices that were injected via CertifiedCommits (catch-up / recovery)
    // where per-validator individual votes are not available.
    injected_commits: BTreeSet<CommitIndex>,
    // Highest seen epoch in future blocks or network packets (for catching up)
    highest_seen_epoch: u64,
}

/// Monitors the progress of consensus commits across the network.
pub struct CommitVoteMonitor {
    context: Arc<Context>,
    state: Mutex<VoteState>,
    // Notifier for when quorum index might have advanced
    pub quorum_advanced_notify: Arc<tokio::sync::Notify>,
}

/// Number of commit indices to retain in digest_history below the current
/// quorum_commit_index. Prevents unbounded memory growth while ensuring
/// recently-buffered local commits can still be verified.
///
/// Sizing: At 50k+ TPS (~1000 commits/sec), epoch transitions can stall
/// CommitProcessor for 5-10s = 5k-10k commits. Set to 50k for 50× headroom.
/// Memory: ~50k entries × ~100 bytes = ~5MB — negligible.
const DIGEST_HISTORY_RETAIN: u32 = 50_000;

impl CommitVoteMonitor {
    pub(crate) fn new(context: Arc<Context>) -> Self {
        let size = context.committee.size();
        let current_epoch = context.committee.epoch();
        let state = VoteState {
            highest_voted_commits: vec![0; size],
            digest_history: BTreeMap::new(),
            authority_voted_commits: (0..size).map(|_| HashMap::new()).collect(),
            injected_commits: BTreeSet::new(),
            highest_seen_epoch: current_epoch,
        };
        Self {
            context,
            state: Mutex::new(state),
            quorum_advanced_notify: Arc::new(tokio::sync::Notify::new()),
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
                if let std::collections::hash_map::Entry::Vacant(e) =
                    state.authority_voted_commits[author].entry(vote.index)
                {
                    e.insert(vote.digest);
                    let authority_stake = self.context.committee.authority(author).stake;
                    let entry = state
                        .digest_history
                        .entry(vote.index)
                        .or_insert_with(HashMap::new);
                    *entry.entry(vote.digest).or_insert(0) += authority_stake;
                }
            }

            // GC: Prune old digest history entries to prevent unbounded growth.
            // Keep entries from (quorum - RETAIN) onwards.
            let gc_below = self
                .compute_quorum_index_inner(&state.highest_voted_commits)
                .saturating_sub(DIGEST_HISTORY_RETAIN);
            if gc_below > 0 {
                // Split off entries below gc_below
                let to_keep = state.digest_history.split_off(&gc_below);
                state.digest_history = to_keep;
                // Also clean authority_voted for pruned indices
                for voted_map in state.authority_voted_commits.iter_mut() {
                    voted_map.retain(|idx, _| *idx >= gc_below);
                }
                state.injected_commits.retain(|idx| *idx >= gc_below);
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

    /// Returns true if the monitor has received ANY digest vote data from observe_block().
    /// Used by COLD-START-BYPASS to differentiate between:
    ///   - quorum_commit_index > 0 set by CommitSyncer (peer count, NOT digest verification)
    ///   - Actual digest verification capability from P2P block observation
    ///
    /// After epoch transition, CommitVoteMonitor is re-created empty. CommitSyncer
    /// may set quorum_commit_index > 0 from peer queries within seconds, but digest
    /// verification is NOT functional until observe_block() accumulates real votes.
    /// If this returns false, COLD-START-BYPASS should be active regardless of qci.
    pub fn has_any_digest_data(&self) -> bool {
        let state = self.state.lock();
        !state.digest_history.is_empty()
    }

    /// Returns (total_voted_stake, Option<(best_digest, best_stake)>) for a specific commit index.
    /// Used by peer attestation to distinguish:
    ///   - (0, None) → No peer has voted for this index yet (true cold-start)
    ///   - (>0, Some) → Some peers voted but haven't reached quorum yet
    ///
    /// ZERO-TIMEOUT (May 2026): This enables data-driven dispatch without timeouts.
    pub fn vote_count_for_index(
        &self,
        target_index: CommitIndex,
    ) -> (u64, Option<(CommitDigest, u64)>) {
        let state = self.state.lock();
        match state.digest_history.get(&target_index) {
            None => (0, None),
            Some(digest_stakes) => {
                let total_stake: u64 = digest_stakes.values().sum();
                let best = digest_stakes
                    .iter()
                    .max_by(|(d1, s1), (d2, s2)| s1.cmp(s2).then_with(|| d1.cmp(d2)))
                    .map(|(d, s)| (*d, *s));
                (total_stake, best)
            }
        }
    }

    /// Injects a certified commit directly into the vote monitor with full quorum weight.
    /// This is used during catch-up to ensure that commits fetched from peers as CertifiedCommits
    /// (which inherently have quorum support) instantly satisfy the DIGEST-GATE in the CommitProcessor.
    /// This prevents a deadlock when the local node already produced the commit locally but lost the
    /// votes after a restart, causing it to stall waiting for votes that will never arrive.
    pub(crate) fn inject_certified_commit(&self, commit_index: CommitIndex, digest: CommitDigest) {
        let mut state = self.state.lock();

        let authority_stake = self.context.committee.total_stake();
        let entry = state
            .digest_history
            .entry(commit_index)
            .or_insert_with(HashMap::new);

        // Inject sufficient weight to immediately pass the quorum threshold
        *entry.entry(digest).or_insert(0) += authority_stake as u64;

        // Mark this commit as injected from CertifiedCommit (voter attribution incomplete)
        state.injected_commits.insert(commit_index);
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

    /// Observes a seen epoch from block verification or other network messages.
    /// If it is higher than the current highest seen epoch, we update it.
    pub fn observe_highest_seen_epoch(&self, epoch: u64) {
        let mut state = self.state.lock();
        if epoch > state.highest_seen_epoch {
            state.highest_seen_epoch = epoch;
        }
    }

    /// Returns the highest seen epoch across blocks, verification failures, etc.
    pub fn highest_seen_epoch(&self) -> u64 {
        let state = self.state.lock();
        state.highest_seen_epoch
    }

    /// Returns a lightweight snapshot of consensus vote progress for external monitoring.
    /// Fast in-memory clone of scalar fields and vector with zero disk/network I/O.
    pub fn get_vote_snapshot(&self) -> ConsensusVoteSnapshot {
        let state = self.state.lock();
        let epoch = self.context.committee.epoch();
        let quorum_commit_index = self.compute_quorum_index_inner(&state.highest_voted_commits);
        let highest_seen_epoch = state.highest_seen_epoch;
        let own_index = self.context.own_index.value();

        let mut authorities = Vec::with_capacity(self.context.committee.size());
        for (idx, authority) in self.context.committee.authorities() {
            let highest_voted = state
                .highest_voted_commits
                .get(idx.value())
                .copied()
                .unwrap_or(0);
            authorities.push(AuthorityVoteInfo {
                authority_index: idx.value(),
                hostname: authority.hostname.clone(),
                address: authority.address.to_string(),
                stake: authority.stake,
                highest_voted_commit: highest_voted,
            });
        }

        // Populate recent 10 commits with exact per-authority voting data from memory
        let mut recent_commits = Vec::new();
        if quorum_commit_index > 0 {
            let start_commit = quorum_commit_index.saturating_sub(9).max(1);
            for c in start_commit..=quorum_commit_index {
                recent_commits.push(self.compute_commit_vote_details_inner(&state, c));
            }
        }

        ConsensusVoteSnapshot {
            epoch,
            quorum_commit_index,
            own_index,
            highest_seen_epoch,
            authorities,
            recent_commits,
        }
    }

    /// Computes the exact voting breakdown for a specific commit index from current state.
    /// Categorizes authorities into voters (voted leading_digest), conflicting_voters (voted other digest),
    /// and missing_voters (unvoted). Breaks ties between equal stake digests deterministically.
    fn compute_commit_vote_details_inner(
        &self,
        state: &VoteState,
        target_index: CommitIndex,
    ) -> CommitVoteDetails {
        let quorum_threshold = self.context.committee.quorum_threshold();

        // Deterministic tie-breaking: if stakes are equal, break ties using digest ordering
        let (leading_digest, leading_stake, total_voted_stake_history) = state
            .digest_history
            .get(&target_index)
            .map(|digest_stakes| {
                let total_voted: u64 = digest_stakes.values().sum();
                let best = digest_stakes
                    .iter()
                    .max_by(|(d1, s1), (d2, s2)| s1.cmp(s2).then_with(|| d1.cmp(d2)));
                let (best_d, best_s) = best.map(|(d, s)| (Some(*d), *s)).unwrap_or((None, 0));
                (best_d, best_s, total_voted)
            })
            .unwrap_or((None, 0, 0));

        let quorum_reached = leading_stake >= quorum_threshold && leading_digest.is_some();

        let leading_digest_str = leading_digest.as_ref().map(|d| {
            let hex_str: String = d
                .into_inner()
                .iter()
                .map(|b| format!("{:02x}", b))
                .collect();
            format!("0x{}", hex_str)
        });

        let quorum_digest = if quorum_reached {
            leading_digest_str.clone()
        } else {
            None
        };

        let mut voters = Vec::new();
        let mut conflicting_voters = Vec::new();
        let mut missing_voters = Vec::new();
        let mut total_stake = 0;
        let mut total_voted_stake = 0;

        for (idx, authority) in self.context.committee.authorities() {
            let voted_digest = state
                .authority_voted_commits
                .get(idx.value())
                .and_then(|m| m.get(&target_index));

            let name = if authority.hostname.is_empty() {
                format!("val-{}", idx.value())
            } else {
                authority.hostname.clone()
            };

            match voted_digest {
                Some(d) if leading_digest.as_ref() == Some(d) => {
                    total_stake += authority.stake;
                    total_voted_stake += authority.stake;
                    voters.push(name);
                }
                Some(_) => {
                    total_voted_stake += authority.stake;
                    conflicting_voters.push(name);
                }
                None => {
                    missing_voters.push(name);
                }
            }
        }

        let voters_stake = total_stake;
        // In case votes were injected via inject_certified_commit without per-authority records
        total_stake = total_stake.max(leading_stake);
        total_voted_stake = total_voted_stake.max(total_voted_stake_history);

        // Voter attribution completeness:
        // Live consensus observes individual validator blocks so voter attribution is complete.
        // Injected certified commits represent aggregate quorum proof from network recovery without per-validator votes.
        let is_injected = state.injected_commits.contains(&target_index);
        let voter_attribution_complete = !is_injected || (voters_stake >= quorum_threshold);

        CommitVoteDetails {
            commit_index: target_index,
            quorum_reached,
            voter_attribution_complete,
            leading_digest: leading_digest_str,
            quorum_digest,
            total_stake,
            total_voted_stake,
            quorum_threshold,
            voters,
            conflicting_voters,
            missing_voters,
        }
    }

    /// Returns the exact voting breakdown for a specific commit index.
    pub fn get_commit_vote_details(&self, target_index: CommitIndex) -> CommitVoteDetails {
        let state = self.state.lock();
        self.compute_commit_vote_details_inner(&state, target_index)
    }
}

/// Authority voting details for monitoring
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct AuthorityVoteInfo {
    pub authority_index: usize,
    pub hostname: String,
    pub address: String,
    pub stake: u64,
    pub highest_voted_commit: CommitIndex,
}

/// Detailed voting breakdown for a single commit
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct CommitVoteDetails {
    pub commit_index: CommitIndex,
    pub quorum_reached: bool,
    pub voter_attribution_complete: bool,
    pub leading_digest: Option<String>,
    pub quorum_digest: Option<String>,
    pub total_stake: u64,
    pub total_voted_stake: u64,
    pub quorum_threshold: u64,
    pub voters: Vec<String>,
    pub conflicting_voters: Vec<String>,
    pub missing_voters: Vec<String>,
}

/// Snapshot of consensus progress and per-authority votes
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ConsensusVoteSnapshot {
    pub epoch: u64,
    pub quorum_commit_index: CommitIndex,
    pub own_index: usize,
    pub highest_seen_epoch: u64,
    pub authorities: Vec<AuthorityVoteInfo>,
    pub recent_commits: Vec<CommitVoteDetails>,
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

    #[tokio::test]
    async fn test_commit_vote_details_quorum_and_conflict() {
        let context = Arc::new(Context::new_for_test(4).0);
        let monitor = CommitVoteMonitor::new(context.clone());

        // 1. Test empty state
        let empty_details = monitor.get_commit_vote_details(10);
        assert_eq!(empty_details.commit_index, 10);
        assert!(!empty_details.quorum_reached);
        assert!(empty_details.leading_digest.is_none());
        assert!(empty_details.quorum_digest.is_none());
        assert_eq!(empty_details.total_stake, 0);
        assert_eq!(empty_details.total_voted_stake, 0);
        assert_eq!(empty_details.voters.len(), 0);
        assert_eq!(empty_details.conflicting_voters.len(), 0);
        assert_eq!(empty_details.missing_voters.len(), 4);

        // 2. Test conflict: Auth 0 and Auth 1 vote MIN, Auth 2 votes MAX, Auth 3 unvoted. Quorum threshold is 3.
        let block_0 = VerifiedBlock::new_for_test(
            TestBlock::new(10, 0)
                .set_commit_votes(vec![CommitRef::new(10, CommitDigest::MIN)])
                .build(),
        );
        let block_1 = VerifiedBlock::new_for_test(
            TestBlock::new(10, 1)
                .set_commit_votes(vec![CommitRef::new(10, CommitDigest::MIN)])
                .build(),
        );
        let block_2 = VerifiedBlock::new_for_test(
            TestBlock::new(10, 2)
                .set_commit_votes(vec![CommitRef::new(10, CommitDigest::MAX)])
                .build(),
        );
        monitor.observe_block(&block_0);
        monitor.observe_block(&block_1);
        monitor.observe_block(&block_2);

        let conflict_details = monitor.get_commit_vote_details(10);
        assert!(!conflict_details.quorum_reached);
        assert!(conflict_details.leading_digest.is_some());
        assert!(conflict_details.quorum_digest.is_none());
        // Stake for MIN is 2 (Auth 0 and Auth 1). Auth 2 voted MAX.
        assert_eq!(conflict_details.total_stake, 2);
        assert_eq!(conflict_details.total_voted_stake, 3);
        // Verify each validator belongs to exactly one category:
        assert_eq!(
            conflict_details.voters,
            vec!["test_host_0".to_string(), "test_host_1".to_string()]
        );
        assert_eq!(
            conflict_details.conflicting_voters,
            vec!["test_host_2".to_string()]
        );
        assert_eq!(
            conflict_details.missing_voters,
            vec!["test_host_3".to_string()]
        );

        // 3. Test quorum reached: Auth 3 votes MIN -> reaches 3 votes >= quorum_threshold (3).
        let block_3 = VerifiedBlock::new_for_test(
            TestBlock::new(10, 3)
                .set_commit_votes(vec![CommitRef::new(10, CommitDigest::MIN)])
                .build(),
        );
        monitor.observe_block(&block_3);

        let quorum_details = monitor.get_commit_vote_details(10);
        assert!(quorum_details.quorum_reached);
        assert!(quorum_details.leading_digest.is_some());
        assert!(quorum_details.quorum_digest.is_some());
        assert_eq!(quorum_details.leading_digest, quorum_details.quorum_digest);
        assert_eq!(quorum_details.total_stake, 3);
        assert_eq!(quorum_details.total_voted_stake, 4);
        assert_eq!(
            quorum_details.voters,
            vec![
                "test_host_0".to_string(),
                "test_host_1".to_string(),
                "test_host_3".to_string()
            ]
        );
        assert_eq!(
            quorum_details.conflicting_voters,
            vec!["test_host_2".to_string()]
        );
        assert!(quorum_details.missing_voters.is_empty());

        // 4. Test deterministic tie-break: Auth 0 votes MIN, Auth 1 votes MAX (stakes 1 vs 1)
        let tie_block_0 = VerifiedBlock::new_for_test(
            TestBlock::new(20, 0)
                .set_commit_votes(vec![CommitRef::new(20, CommitDigest::MIN)])
                .build(),
        );
        let tie_block_1 = VerifiedBlock::new_for_test(
            TestBlock::new(20, 1)
                .set_commit_votes(vec![CommitRef::new(20, CommitDigest::MAX)])
                .build(),
        );
        monitor.observe_block(&tie_block_0);
        monitor.observe_block(&tie_block_1);

        let tie_details = monitor.get_commit_vote_details(20);
        assert_eq!(tie_details.total_stake, 1);
        assert_eq!(tie_details.total_voted_stake, 2);
        // MAX > MIN, so MAX deterministically wins tie-break:
        assert_eq!(tie_details.voters, vec!["test_host_1".to_string()]);
        assert_eq!(
            tie_details.conflicting_voters,
            vec!["test_host_0".to_string()]
        );
        assert_eq!(
            tie_details.missing_voters,
            vec!["test_host_2".to_string(), "test_host_3".to_string()]
        );

        // 5. Test snapshot contains the quorum commit details
        let snapshot = monitor.get_vote_snapshot();
        assert_eq!(snapshot.quorum_commit_index, 10);
        assert!(!snapshot.recent_commits.is_empty());
        let last_commit = snapshot.recent_commits.last().unwrap();
        assert_eq!(last_commit.commit_index, 10);
        assert!(last_commit.quorum_reached);
        assert!(last_commit.voter_attribution_complete);
        assert_eq!(last_commit.total_stake, 3);
        assert_eq!(last_commit.total_voted_stake, 4);
        assert_eq!(last_commit.voters.len(), 3);
        assert_eq!(last_commit.conflicting_voters.len(), 1);
        assert!(last_commit.missing_voters.is_empty());

        // 6. Test injected certified commit (recovery): quorum is reached but voter attribution is incomplete
        monitor.inject_certified_commit(30, CommitDigest::MIN);
        let injected_details = monitor.get_commit_vote_details(30);
        assert!(injected_details.quorum_reached);
        assert!(!injected_details.voter_attribution_complete);
        assert!(injected_details.total_stake >= injected_details.quorum_threshold);
        assert!(injected_details.quorum_digest.is_some());
        assert_eq!(injected_details.voters.len(), 0);
    }
}
