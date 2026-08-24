// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! On-Chain Governance Module for Root Anchor (Task P0.2)
//!
//! Implements on-chain proposal lifecycle: propose -> vote -> >=2/3 active chains -> 72h timelock -> execute.
//! Adheres strictly to Section 1.3 #3 and Section 5.4: voting power is 1-chain-1-vote (counted by active chains,
//! NOT stake-weighted) to prevent single large chain dominance.

use std::collections::{BTreeMap, BTreeSet};
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::types::cross_chain::{GovernanceProposal, GovernanceProposalKind, Hash};

pub const DEFAULT_GOVERNANCE_TIMELOCK_SECONDS: u64 = 72 * 3600; // 72 hours

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum GovernanceProposalStatus {
    Active,
    Timelocked,
    Executed,
    Rejected,
}

#[derive(Error, Debug, PartialEq, Eq)]
pub enum GovernanceError {
    #[error("Proposal {0:?} not found")]
    ProposalNotFound(Hash),

    #[error("Chain {0} is not an active registered chain")]
    ChainNotRegistered(u64),

    #[error("Chain {0} has already voted for this proposal")]
    AlreadyVoted(u64),

    #[error("Proposal is not in Active status (current status: {0:?})")]
    ProposalNotActive(GovernanceProposalStatus),

    #[error("Proposal is not in Timelocked status (current status: {0:?})")]
    ProposalNotTimelocked(GovernanceProposalStatus),

    #[error("Timelock has not expired: current time {current}, effective at {effective_at}")]
    TimelockNotExpired { current: u64, effective_at: u64 },

    #[error("Proposal has already been executed")]
    AlreadyExecuted,

    #[error("Cannot compute quorum: no active chains registered")]
    NoActiveChains,
}

/// On-Chain Governance Engine running on the Root Anchor Chain (Section 1.3 #3 & Section 5.4).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct GovernanceEngine {
    pub active_chains: BTreeSet<u64>,
    pub proposals: BTreeMap<Hash, GovernanceProposal>,
    pub proposal_status: BTreeMap<Hash, GovernanceProposalStatus>,
    pub timelock_delay_seconds: u64,
}

impl GovernanceEngine {
    pub fn new(active_chains: BTreeSet<u64>) -> Self {
        Self {
            active_chains,
            proposals: BTreeMap::new(),
            proposal_status: BTreeMap::new(),
            timelock_delay_seconds: DEFAULT_GOVERNANCE_TIMELOCK_SECONDS,
        }
    }

    pub fn with_timelock_delay(active_chains: BTreeSet<u64>, timelock_delay_seconds: u64) -> Self {
        Self {
            active_chains,
            proposals: BTreeMap::new(),
            proposal_status: BTreeMap::new(),
            timelock_delay_seconds,
        }
    }

    /// Registers a newly approved active chain into the voting pool.
    pub fn register_active_chain(&mut self, chain_id: u64) {
        self.active_chains.insert(chain_id);
    }

    /// Unregisters an active chain from the voting pool.
    pub fn unregister_active_chain(&mut self, chain_id: u64) {
        self.active_chains.remove(&chain_id);
    }

    /// Computes the required >= 2/3 active chain quorum threshold (ceil(2N/3)).
    /// 1 chain -> 1, 2 chains -> 2, 3 chains -> 2, 4 chains -> 3, 5 chains -> 4, 6 chains -> 4, etc.
    pub fn quorum_threshold(&self) -> Result<u64, GovernanceError> {
        let n = self.active_chains.len() as u64;
        if n == 0 {
            return Err(GovernanceError::NoActiveChains);
        }
        // Formula: (2 * n + 2) / 3 gives ceil(2n / 3)
        Ok((2 * n + 2) / 3)
    }

    /// Submits a new governance proposal. Returns the calculated proposal_id hash.
    pub fn propose(
        &mut self,
        kind: GovernanceProposalKind,
        payload: Vec<u8>,
        proposed_at: u64,
    ) -> Result<Hash, GovernanceError> {
        // Calculate deterministic proposal_id from (kind, payload, proposed_at)
        let mut hasher_data = Vec::new();
        hasher_data.push(kind as u8);
        hasher_data.extend_from_slice(&proposed_at.to_be_bytes());
        hasher_data.extend_from_slice(&payload);

        let hash_bytes = sha3::keccak256_digest(&hasher_data);
        let proposal_id = Hash(hash_bytes);

        let proposal = GovernanceProposal {
            proposal_id,
            kind,
            payload,
            votes_for: 0,
            voted_chains: BTreeSet::new(),
            proposed_at,
            effective_at: 0,
            executed: false,
        };

        self.proposals.insert(proposal_id, proposal);
        self.proposal_status.insert(proposal_id, GovernanceProposalStatus::Active);

        Ok(proposal_id)
    }

    /// Casts a vote from an active registered chain (1 chain = 1 vote).
    /// If >= 2/3 threshold is reached, automatically transitions status to `Timelocked`
    /// and sets `effective_at = current_timestamp + timelock_delay_seconds`.
    pub fn vote(
        &mut self,
        proposal_id: Hash,
        voter_chain_id: u64,
        current_timestamp: u64,
    ) -> Result<GovernanceProposalStatus, GovernanceError> {
        if !self.active_chains.contains(&voter_chain_id) {
            return Err(GovernanceError::ChainNotRegistered(voter_chain_id));
        }

        let threshold = self.quorum_threshold()?;

        let status = self
            .proposal_status
            .get(&proposal_id)
            .copied()
            .ok_or(GovernanceError::ProposalNotFound(proposal_id))?;

        if status != GovernanceProposalStatus::Active {
            return Err(GovernanceError::ProposalNotActive(status));
        }

        let proposal = self
            .proposals
            .get_mut(&proposal_id)
            .ok_or(GovernanceError::ProposalNotFound(proposal_id))?;

        if proposal.voted_chains.contains(&voter_chain_id) {
            return Err(GovernanceError::AlreadyVoted(voter_chain_id));
        }

        proposal.voted_chains.insert(voter_chain_id);
        proposal.votes_for = proposal.voted_chains.len() as u64;

        if proposal.votes_for >= threshold {
            proposal.effective_at = current_timestamp + self.timelock_delay_seconds;
            self.proposal_status.insert(proposal_id, GovernanceProposalStatus::Timelocked);
            Ok(GovernanceProposalStatus::Timelocked)
        } else {
            Ok(GovernanceProposalStatus::Active)
        }
    }

    /// Executes an approved proposal after the mandatory 72-hour timelock window has elapsed.
    /// Strictly idempotent: calling a second time fails with `AlreadyExecuted`.
    pub fn execute(
        &mut self,
        proposal_id: Hash,
        current_timestamp: u64,
    ) -> Result<GovernanceProposal, GovernanceError> {
        let status = self
            .proposal_status
            .get(&proposal_id)
            .copied()
            .ok_or(GovernanceError::ProposalNotFound(proposal_id))?;

        if status == GovernanceProposalStatus::Executed {
            return Err(GovernanceError::AlreadyExecuted);
        }

        if status != GovernanceProposalStatus::Timelocked {
            return Err(GovernanceError::ProposalNotTimelocked(status));
        }

        let proposal = self
            .proposals
            .get_mut(&proposal_id)
            .ok_or(GovernanceError::ProposalNotFound(proposal_id))?;

        if current_timestamp < proposal.effective_at {
            return Err(GovernanceError::TimelockNotExpired {
                current: current_timestamp,
                effective_at: proposal.effective_at,
            });
        }

        proposal.executed = true;
        self.proposal_status.insert(proposal_id, GovernanceProposalStatus::Executed);

        Ok(proposal.clone())
    }

    pub fn get_proposal(&self, proposal_id: &Hash) -> Option<&GovernanceProposal> {
        self.proposals.get(proposal_id)
    }

    pub fn get_status(&self, proposal_id: &Hash) -> Option<GovernanceProposalStatus> {
        self.proposal_status.get(proposal_id).copied()
    }
}

mod sha3 {
    use sha3::{Digest, Keccak256};

    pub fn keccak256_digest(data: &[u8]) -> [u8; 32] {
        let mut hasher = Keccak256::new();
        hasher.update(data);
        let result = hasher.finalize();
        let mut output = [0u8; 32];
        output.copy_from_slice(&result);
        output
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_quorum_threshold_calculation() {
        let mut chains = BTreeSet::new();
        let engine = GovernanceEngine::new(chains.clone());
        assert_eq!(engine.quorum_threshold(), Err(GovernanceError::NoActiveChains));

        // N=1 -> 1
        chains.insert(1);
        assert_eq!(GovernanceEngine::new(chains.clone()).quorum_threshold().unwrap(), 1);

        // N=2 -> 2
        chains.insert(2);
        assert_eq!(GovernanceEngine::new(chains.clone()).quorum_threshold().unwrap(), 2);

        // N=3 -> 2
        chains.insert(3);
        assert_eq!(GovernanceEngine::new(chains.clone()).quorum_threshold().unwrap(), 2);

        // N=4 -> 3
        chains.insert(4);
        assert_eq!(GovernanceEngine::new(chains.clone()).quorum_threshold().unwrap(), 3);

        // N=5 -> 4
        chains.insert(5);
        assert_eq!(GovernanceEngine::new(chains.clone()).quorum_threshold().unwrap(), 4);

        // N=6 -> 4
        chains.insert(6);
        assert_eq!(GovernanceEngine::new(chains.clone()).quorum_threshold().unwrap(), 4);
    }

    #[test]
    fn test_governance_full_lifecycle_dod() {
        // Setup 4 active chains (quorum >= 3)
        let mut active = BTreeSet::new();
        active.insert(101);
        active.insert(102);
        active.insert(103);
        active.insert(104);

        let timelock = 72 * 3600; // 72h
        let mut engine = GovernanceEngine::with_timelock_delay(active, timelock);

        let t0 = 1_700_000_000u64;

        // Step 1: Propose
        let prop_id = engine
            .propose(
                GovernanceProposalKind::RegisterChain,
                vec![0x10, 0x20, 0x30],
                t0,
            )
            .expect("propose ok");

        assert_eq!(engine.get_status(&prop_id), Some(GovernanceProposalStatus::Active));

        // Step 2: Vote 1 (Chain 101) -> votes=1/4 (Threshold=3) -> Remains Active
        let s1 = engine.vote(prop_id, 101, t0 + 100).expect("vote 1 ok");
        assert_eq!(s1, GovernanceProposalStatus::Active);

        // Step 3: Vote 2 (Chain 102) -> votes=2/4 -> Remains Active
        let s2 = engine.vote(prop_id, 102, t0 + 200).expect("vote 2 ok");
        assert_eq!(s2, GovernanceProposalStatus::Active);

        // Sub-test (b): Vote not enough quorum -> Cannot execute
        let err_exec_early = engine.execute(prop_id, t0 + 300).unwrap_err();
        assert_eq!(
            err_exec_early,
            GovernanceError::ProposalNotTimelocked(GovernanceProposalStatus::Active)
        );

        // Step 4: Vote 3 (Chain 103) -> votes=3/4 (>=3) -> Transitions to Timelocked!
        let t_approved = t0 + 500;
        let s3 = engine.vote(prop_id, 103, t_approved).expect("vote 3 ok");
        assert_eq!(s3, GovernanceProposalStatus::Timelocked);

        let prop = engine.get_proposal(&prop_id).unwrap();
        assert_eq!(prop.effective_at, t_approved + timelock);

        // Sub-test (c): Quorum reached but 72h timelock NOT yet expired -> execute must revert
        let err_timelock = engine.execute(prop_id, t_approved + timelock - 1).unwrap_err();
        assert_eq!(
            err_timelock,
            GovernanceError::TimelockNotExpired {
                current: t_approved + timelock - 1,
                effective_at: t_approved + timelock
            }
        );

        // Sub-test (d): Exactly at or after 72h elapsed -> execute succeeds!
        let executed_prop = engine
            .execute(prop_id, t_approved + timelock)
            .expect("execute after 72h ok");
        assert!(executed_prop.executed);
        assert_eq!(engine.get_status(&prop_id), Some(GovernanceProposalStatus::Executed));

        // Sub-test (d-idempotent): Calling execute a 2nd time must revert with AlreadyExecuted
        let err_second_exec = engine.execute(prop_id, t_approved + timelock + 10).unwrap_err();
        assert_eq!(err_second_exec, GovernanceError::AlreadyExecuted);

        // Sub-test (e): Double voting from same chain must be rejected
        let prop2_id = engine
            .propose(GovernanceProposalKind::UpdateCommittee, vec![1], t0)
            .unwrap();
        engine.vote(prop2_id, 101, t0 + 10).unwrap();
        let err_double_vote = engine.vote(prop2_id, 101, t0 + 20).unwrap_err();
        assert_eq!(err_double_vote, GovernanceError::AlreadyVoted(101));

        // Sub-test (f): Voting from non-active chain must be rejected
        let err_unregistered = engine.vote(prop2_id, 999, t0 + 30).unwrap_err();
        assert_eq!(err_unregistered, GovernanceError::ChainNotRegistered(999));
    }
}
