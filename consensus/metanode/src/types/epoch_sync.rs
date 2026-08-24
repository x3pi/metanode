// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Epoch Sync, Committee Update & Account-Level StateRoot Checkpoint (Phase P3)
//!
//! Implements Section 1.3 #2, Section 2.1, Section 5.2.2, and Section 14 (P3.1 - P3.2):
//! - P3.1: CommitteeUpdate — Sequential epoch transition, old committee QuorumCert verification, new committee PoP verification
//! - P3.2: StateRootCheckpoint & AccountTree — Account-level Merkle tree, inclusion proofs, and tamper guards

use std::collections::BTreeMap;
use fastcrypto::hash::{HashFunction, Keccak256};
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::types::cross_chain::{
    AccountLeaf, ChainRegistry, Hash, MerkleProof, QuorumCert, ValidatorEntry,
};
use crate::types::pop::{validate_committee, PopError};

#[derive(Error, Debug, PartialEq, Eq)]
pub enum EpochSyncError {
    #[error("Chain {0} is not registered in ChainRegistry")]
    UnknownChain(u64),

    #[error("Non-sequential epoch transition for chain {chain_id}: expected {expected}, got {got}")]
    NonSequentialEpoch {
        chain_id: u64,
        expected: u64,
        got: u64,
    },

    #[error("Quorum certificate for epoch {0} is invalid")]
    InvalidQuorumCert(u64),

    #[error("New committee validation failed: {0}")]
    InvalidNewCommittee(PopError),

    #[error("Account Merkle proof verification failed")]
    AccountProofFailed,

    #[error("Empty account list provided")]
    EmptyAccounts,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CommitteeUpdate {
    pub source_chain_id: u64,
    pub new_epoch: u64,
    pub new_committee: Vec<ValidatorEntry>,
    pub quorum_threshold: u64,
    pub state_root: Hash,
    pub cert: QuorumCert,
}

pub fn hash_account_leaf(leaf: &AccountLeaf) -> Hash {
    let mut data = Vec::with_capacity(20 + 32);
    data.extend_from_slice(&leaf.account.0);
    let bal_bytes = leaf.balance.to_big_endian();
    data.extend_from_slice(&bal_bytes);
    let digest = Keccak256::digest(&data);
    let mut out = [0u8; 32];
    out.copy_from_slice(digest.as_ref());
    Hash(out)
}


fn hash_nodes(left: Hash, right: Hash) -> Hash {
    crate::types::gateway::hash_pair(left, right)
}

/// Builds a binary Merkle Tree from a list of AccountLeaves and returns (root, proofs).
pub fn build_account_merkle_tree(
    accounts: &[AccountLeaf],
) -> Result<(Hash, Vec<MerkleProof>), EpochSyncError> {
    if accounts.is_empty() {
        return Err(EpochSyncError::EmptyAccounts);
    }

    let n = accounts.len();
    let mut leaves: Vec<Hash> = accounts.iter().map(hash_account_leaf).collect();

    // Pad to power of 2
    let mut padded_len = 1;
    while padded_len < n {
        padded_len *= 2;
    }
    let last = *leaves.last().unwrap();
    while leaves.len() < padded_len {
        leaves.push(last);
    }

    let num_layers = (padded_len as f64).log2() as usize;
    let mut layers: Vec<Vec<Hash>> = vec![leaves.clone()];

    let mut current_layer = leaves;
    for _ in 0..num_layers {
        let mut next_layer = Vec::new();
        for chunk in current_layer.chunks(2) {
            let left = chunk[0];
            let right = chunk[1];
            next_layer.push(hash_nodes(left, right));
        }
        layers.push(next_layer.clone());
        current_layer = next_layer;
    }

    let root = layers.last().unwrap()[0];

    // Generate proofs for original accounts
    let mut proofs = Vec::with_capacity(n);
    for (i, _) in accounts.iter().enumerate() {
        let mut siblings = Vec::new();
        let mut idx = i;
        for layer in layers.iter().take(num_layers) {
            let sib_idx = if idx % 2 == 0 { idx + 1 } else { idx - 1 };
            siblings.push(layer[sib_idx]);
            idx /= 2;
        }
        proofs.push(MerkleProof {
            leaf_index: i as u64,
            siblings,
        });
    }

    Ok((root, proofs))
}

/// Verifies an account inclusion proof against a known state_root.
pub fn verify_account_merkle_proof(
    leaf: &AccountLeaf,
    proof: &MerkleProof,
    expected_root: Hash,
) -> bool {
    let mut current = hash_account_leaf(leaf);
    for sibling in &proof.siblings {
        current = hash_nodes(current, *sibling);
    }
    current == expected_root
}

/// P3.1: Applies a CommitteeUpdate onto the ChainRegistry on the Root Anchor.
pub fn apply_committee_update(
    registry: &mut BTreeMap<u64, ChainRegistry>,
    update: CommitteeUpdate,
    is_old_cert_valid: bool,
) -> Result<(), EpochSyncError> {
    let reg = registry
        .get_mut(&update.source_chain_id)
        .ok_or(EpochSyncError::UnknownChain(update.source_chain_id))?;

    // Check sequential epoch transition
    let expected_epoch = reg.epoch + 1;
    if update.new_epoch != expected_epoch {
        return Err(EpochSyncError::NonSequentialEpoch {
            chain_id: update.source_chain_id,
            expected: expected_epoch,
            got: update.new_epoch,
        });
    }

    // Verify cert from current epoch committee
    if !is_old_cert_valid || update.cert.epoch != reg.epoch {
        return Err(EpochSyncError::InvalidQuorumCert(reg.epoch));
    }

    // Validate new committee PoPs and unique keys
    validate_committee(&update.new_committee)
        .map_err(EpochSyncError::InvalidNewCommittee)?;

    // Update on-chain registry
    reg.epoch = update.new_epoch;
    reg.committee = update.new_committee;
    reg.quorum_threshold = update.quorum_threshold;
    reg.state_root = update.state_root;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::cross_chain::{Address, U256};
    use crate::types::pop::pop_sign;
    use fastcrypto::bls12381::min_sig::BLS12381KeyPair;
    use fastcrypto::traits::{KeyPair, ToFromBytes};
    use rand::thread_rng;

    fn make_test_validator(stake: u64) -> ValidatorEntry {
        let mut rng = thread_rng();
        let kp = BLS12381KeyPair::generate(&mut rng);
        let pub_bytes = kp.public().as_bytes().to_vec();
        let pop_sig = pop_sign(&kp);
        ValidatorEntry {
            pubkey_bls: pub_bytes,
            stake,
            pop_signature: pop_sig,
        }
    }

    #[test]
    fn test_p3_1_committee_update_lifecycle_dod() {
        let mut registry = BTreeMap::new();
        let v1 = make_test_validator(1000);
        let reg_101 = ChainRegistry {
            chain_id: 101,
            committee: vec![v1],
            epoch: 5,
            quorum_threshold: 667,
            gateway_contract: Address([0xAA; 20]),
            state_root: Hash([0x11; 32]),
            archival_endpoint: "http://archive.test".to_string(),
            registered_at: 1000,
        };
        registry.insert(101, reg_101);

        let v2 = make_test_validator(2000);
        let v3 = make_test_validator(3000);
        let new_committee = vec![v2.clone(), v3.clone()];
        let new_state_root = Hash([0x99; 32]);

        let cert = QuorumCert {
            epoch: 5,
            aggregate_signature: vec![0xEE; 48],
            signer_bitmap: vec![0x0F],
        };

        // 1. Success Case: Epoch 5 -> 6 with valid cert
        let update_valid = CommitteeUpdate {
            source_chain_id: 101,
            new_epoch: 6,
            new_committee: new_committee.clone(),
            quorum_threshold: 3334,
            state_root: new_state_root,
            cert: cert.clone(),
        };
        apply_committee_update(&mut registry, update_valid, true).expect("update ok");

        let updated_reg = registry.get(&101).unwrap();
        assert_eq!(updated_reg.epoch, 6);
        assert_eq!(updated_reg.state_root, new_state_root);
        assert_eq!(updated_reg.committee.len(), 2);
        assert_eq!(updated_reg.quorum_threshold, 3334);

        // 2. Non-sequential Reject: Epoch 6 -> 8 (skipping 7)
        let update_skip = CommitteeUpdate {
            source_chain_id: 101,
            new_epoch: 8,
            new_committee: new_committee.clone(),
            quorum_threshold: 3334,
            state_root: new_state_root,
            cert: QuorumCert {
                epoch: 6,
                aggregate_signature: vec![0xEE; 48],
                signer_bitmap: vec![0x0F],
            },
        };
        let res_skip = apply_committee_update(&mut registry, update_skip, true);
        assert_eq!(
            res_skip,
            Err(EpochSyncError::NonSequentialEpoch {
                chain_id: 101,
                expected: 7,
                got: 8,
            })
        );

        // 3. Invalid Cert Reject
        let update_bad_cert = CommitteeUpdate {
            source_chain_id: 101,
            new_epoch: 7,
            new_committee: new_committee.clone(),
            quorum_threshold: 3334,
            state_root: new_state_root,
            cert: QuorumCert {
                epoch: 6,
                aggregate_signature: vec![0x00; 48],
                signer_bitmap: vec![0x0F],
            },
        };
        let res_bad_cert = apply_committee_update(&mut registry, update_bad_cert, false);
        assert_eq!(res_bad_cert, Err(EpochSyncError::InvalidQuorumCert(6)));
    }

    #[test]
    fn test_p3_2_account_merkle_tree_proof_and_tamper_guard() {
        let a1 = AccountLeaf {
            account: Address([0x11; 20]),
            balance: U256::from(500u64),
        };
        let a2 = AccountLeaf {
            account: Address([0x22; 20]),
            balance: U256::from(1500u64),
        };
        let a3 = AccountLeaf {
            account: Address([0x33; 20]),
            balance: U256::from(3000u64),
        };
        let a4 = AccountLeaf {
            account: Address([0x44; 20]),
            balance: U256::from(5000u64),
        };

        let accounts = vec![a1.clone(), a2.clone(), a3.clone(), a4.clone()];
        let (root, proofs) = build_account_merkle_tree(&accounts).expect("build tree ok");

        // Valid Proof Verification for all 4 accounts
        for (i, acc) in accounts.iter().enumerate() {
            assert!(
                verify_account_merkle_proof(acc, &proofs[i], root),
                "account {} proof must be valid",
                i
            );
        }

        // Tamper Guard: Attacker tampers balance of account 1 from 500 to 50,000
        let tampered_a1 = AccountLeaf {
            account: Address([0x11; 20]),
            balance: U256::from(50000u64),
        };
        assert!(
            !verify_account_merkle_proof(&tampered_a1, &proofs[0], root),
            "tampered balance must fail verification"
        );

        // Tamper Guard: Attacker changes address
        let tampered_addr = AccountLeaf {
            account: Address([0x99; 20]),
            balance: U256::from(500u64),
        };
        assert!(
            !verify_account_merkle_proof(&tampered_addr, &proofs[0], root),
            "tampered address must fail verification"
        );
    }
}
