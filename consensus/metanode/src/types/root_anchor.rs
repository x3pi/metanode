// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Root Anchor / Reserve Chain Architecture & Founding Committee Aggregator (Phase P1)
//!
//! Implements Section 1.3 #5, Section 5.2, and Section 11.1:
//! - Root Anchor Genesis Configuration
//! - Mandatory >= 4 Founding Private Chains committee aggregation
//! - Maximum stake contribution cap per chain (default 33%) to prevent monopoly
//! - Stake-weighted BFT Quorum Threshold calculation: Quorum = 2f + 1 = floor(2 * TotalStake / 3) + 1
//! - Fault tolerance simulation: 1 founding chain offline (<= f) maintains liveness (Quorum OK)

use std::collections::{BTreeMap, BTreeSet};
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::types::cross_chain::{U256, ValidatorEntry};
use crate::types::pop::{validate_committee, PopError};

pub const MIN_FOUNDING_CHAINS: usize = 4;
pub const DEFAULT_MAX_STAKE_CAP_PERCENT: u8 = 33; // Max 33% stake per chain

#[derive(Error, Debug, PartialEq, Eq)]
pub enum RootAnchorError {
    #[error("Root Anchor requires at least {MIN_FOUNDING_CHAINS} founding chains, got {0}")]
    InsufficientFoundingChains(usize),

    #[error("Chain {chain_id} stake ({stake}) exceeds max cap of {cap_percent}% ({max_allowed})")]
    StakeCapExceeded {
        chain_id: u64,
        stake: u64,
        cap_percent: u8,
        max_allowed: u64,
    },

    #[error("Founding chain {0} has duplicate chain_id")]
    DuplicateChainId(u64),

    #[error("PoP validation failed for founding chain {0}: {1}")]
    PopValidationFailed(u64, PopError),

    #[error("Total stake cannot be zero")]
    ZeroTotalStake,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FoundingChainConfig {
    pub chain_id: u64,
    pub name: String,
    pub validators: Vec<ValidatorEntry>,
    pub total_stake: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct RootAnchorGenesisConfig {
    pub chain_id: u64,
    pub genesis_total_supply: U256,
    pub founding_chains: Vec<FoundingChainConfig>,
    pub initial_allocations: BTreeMap<u64, U256>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct RootAnchorCommittee {
    pub founding_chains: Vec<FoundingChainConfig>,
    pub max_stake_cap_percent: u8,
    pub all_validators: Vec<ValidatorEntry>,
    pub stake_by_chain: BTreeMap<u64, u64>,
    pub total_stake: u64,
}

impl RootAnchorCommittee {
    pub fn new(
        founding_chains: Vec<FoundingChainConfig>,
        max_stake_cap_percent: u8,
    ) -> Result<Self, RootAnchorError> {
        if founding_chains.len() < MIN_FOUNDING_CHAINS {
            return Err(RootAnchorError::InsufficientFoundingChains(
                founding_chains.len(),
            ));
        }

        let mut seen_chains = BTreeSet::new();
        let mut total_stake: u64 = 0;
        let mut all_validators = Vec::new();
        let mut stake_by_chain = BTreeMap::new();

        for chain in &founding_chains {
            if !seen_chains.insert(chain.chain_id) {
                return Err(RootAnchorError::DuplicateChainId(chain.chain_id));
            }

            // Validate all validators in the founding chain have valid PoP & stake > 0
            validate_committee(&chain.validators)
                .map_err(|e| RootAnchorError::PopValidationFailed(chain.chain_id, e))?;

            let chain_stake: u64 = chain.validators.iter().map(|v| v.stake).sum();
            total_stake = total_stake.saturating_add(chain_stake);
            stake_by_chain.insert(chain.chain_id, chain_stake);
            all_validators.extend(chain.validators.clone());
        }

        if total_stake == 0 {
            return Err(RootAnchorError::ZeroTotalStake);
        }

        // Verify no single chain exceeds max_stake_cap_percent
        for chain in &founding_chains {
            let chain_stake = stake_by_chain.get(&chain.chain_id).copied().unwrap_or(0);
            let max_allowed = (total_stake * (max_stake_cap_percent as u64)) / 100;
            if chain_stake > max_allowed {
                return Err(RootAnchorError::StakeCapExceeded {
                    chain_id: chain.chain_id,
                    stake: chain_stake,
                    cap_percent: max_stake_cap_percent,
                    max_allowed,
                });
            }
        }

        Ok(Self {
            founding_chains,
            max_stake_cap_percent,
            all_validators,
            stake_by_chain,
            total_stake,
        })
    }

    /// Computes the BFT quorum threshold: 2f + 1 = floor(2 * TotalStake / 3) + 1.
    pub fn bft_quorum_threshold(&self) -> u64 {
        (2 * self.total_stake) / 3 + 1
    }

    /// Computes the maximum Byzantine / faulty stake tolerance: f = floor((TotalStake - 1) / 3).
    pub fn max_faulty_stake(&self) -> u64 {
        if self.total_stake == 0 {
            0
        } else {
            (self.total_stake - 1) / 3
        }
    }

    /// Simulates an outage of a founding chain and determines if the remaining committee reaches BFT Quorum.
    /// Returns (can_reach_quorum, remaining_stake, quorum_threshold).
    pub fn simulate_chain_outage(&self, offline_chain_id: u64) -> (bool, u64, u64) {
        let offline_stake = self.stake_by_chain.get(&offline_chain_id).copied().unwrap_or(0);
        let remaining_stake = self.total_stake.saturating_sub(offline_stake);
        let threshold = self.bft_quorum_threshold();
        let can_reach_quorum = remaining_stake >= threshold;
        (can_reach_quorum, remaining_stake, threshold)
    }

    /// Verifies if a given set of voting validator public keys satisfies the BFT Quorum threshold.
    pub fn verify_quorum_votes(
        &self,
        voting_pubkeys: &[Vec<u8>],
    ) -> Result<(bool, u64, u64), RootAnchorError> {
        let threshold = self.bft_quorum_threshold();
        let mut accumulated_stake: u64 = 0;
        let mut counted_keys = BTreeSet::new();

        for entry in &self.all_validators {
            if voting_pubkeys.contains(&entry.pubkey_bls) && counted_keys.insert(entry.pubkey_bls.clone()) {
                accumulated_stake = accumulated_stake.saturating_add(entry.stake);
            }
        }

        let reached = accumulated_stake >= threshold;
        Ok((reached, accumulated_stake, threshold))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::pop::pop_sign;
    use fastcrypto::bls12381::min_sig::BLS12381KeyPair;
    use fastcrypto::traits::{KeyPair, ToFromBytes};
    use rand::thread_rng;

    fn make_test_chain(chain_id: u64, name: &str, stake: u64) -> FoundingChainConfig {
        let mut rng = thread_rng();
        let kp = BLS12381KeyPair::generate(&mut rng);
        let pub_bytes = kp.public().as_bytes().to_vec();
        let pop_sig = pop_sign(&kp);

        let entry = ValidatorEntry {
            pubkey_bls: pub_bytes,
            stake,
            pop_signature: pop_sig,
        };

        FoundingChainConfig {
            chain_id,
            name: name.to_string(),
            validators: vec![entry],
            total_stake: stake,
        }
    }

    #[test]
    fn test_root_anchor_committee_init_and_threshold_dod() {
        // Setup 4 founding chains with equal stake of 2500 each (total stake = 10,000)
        // 25% stake each <= 33% max cap
        let c1 = make_test_chain(101, "Chain-A", 2500);
        let c2 = make_test_chain(102, "Chain-B", 2500);
        let c3 = make_test_chain(103, "Chain-C", 2500);
        let c4 = make_test_chain(104, "Chain-D", 2500);

        let committee = RootAnchorCommittee::new(vec![c1.clone(), c2.clone(), c3.clone(), c4.clone()], 33)
            .expect("init committee ok");

        assert_eq!(committee.total_stake, 10000);
        // Quorum = floor(2 * 10000 / 3) + 1 = 6666 + 1 = 6667
        assert_eq!(committee.bft_quorum_threshold(), 6667);
        // Faulty tolerance f = floor(9999 / 3) = 3333
        assert_eq!(committee.max_faulty_stake(), 3333);

        // DoD Test: 1 of 4 founding chains goes offline (loss of 2500 stake <= 3333 max faulty stake)
        // Remaining stake = 7500 >= 6667 -> Quorum still reached!
        let (can_reach, remaining, threshold) = committee.simulate_chain_outage(101);
        assert!(can_reach);
        assert_eq!(remaining, 7500);
        assert_eq!(threshold, 6667);

        // Sub-test: Voting verification with 3 chains (c2, c3, c4)
        let voting_keys = vec![
            c2.validators[0].pubkey_bls.clone(),
            c3.validators[0].pubkey_bls.clone(),
            c4.validators[0].pubkey_bls.clone(),
        ];
        let (reached, accum, thresh) = committee.verify_quorum_votes(&voting_keys).unwrap();
        assert!(reached);
        assert_eq!(accum, 7500);
        assert_eq!(thresh, 6667);

        // Sub-test: Voting verification with only 2 chains (c2, c3) -> 5000 < 6667 -> Quorum not reached (Safe PENDING)
        let partial_keys = vec![
            c2.validators[0].pubkey_bls.clone(),
            c3.validators[0].pubkey_bls.clone(),
        ];
        let (reached2, accum2, _) = committee.verify_quorum_votes(&partial_keys).unwrap();
        assert!(!reached2);
        assert_eq!(accum2, 5000);
    }

    #[test]
    fn test_root_anchor_minimum_chains_and_cap_guards() {
        let c1 = make_test_chain(101, "Chain-A", 1000);
        let c2 = make_test_chain(102, "Chain-B", 1000);
        let c3 = make_test_chain(103, "Chain-C", 1000);

        // Only 3 founding chains -> MUST REJECT (minimum 4 required)
        let res_too_few = RootAnchorCommittee::new(vec![c1.clone(), c2.clone(), c3.clone()], 33);
        assert_eq!(
            res_too_few,
            Err(RootAnchorError::InsufficientFoundingChains(3))
        );

        // 4 chains, but 1 chain has 5000 / 8000 stake = 62.5% > 33% max cap -> MUST REJECT
        let c_monopoly = make_test_chain(104, "Chain-Monopoly", 5000);
        let res_cap = RootAnchorCommittee::new(vec![c1, c2, c3, c_monopoly], 33);
        assert!(matches!(res_cap, Err(RootAnchorError::StakeCapExceeded { .. })));
    }
}
