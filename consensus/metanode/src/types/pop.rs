// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! BLS12-381 Proof-of-Possession (PoP) & Rogue-Key Attack Prevention (Task P0.3)
//!
//! Implements Section 1.3 #4 and Section 5.4: Explicit `PopVerify` on every validator public key
//! registered with `ChainRegistry`. Verifies that the registering party possesses the private key
//! corresponding to `pubkey_bls`, preventing rogue-key aggregate signature manipulation.

use std::collections::BTreeSet;
use fastcrypto::bls12381::min_sig::{BLS12381KeyPair, BLS12381PublicKey, BLS12381Signature};
use fastcrypto::traits::{KeyPair, Signer, ToFromBytes, VerifyingKey};
use thiserror::Error;

use crate::types::cross_chain::ValidatorEntry;

/// Domain Separation Tag for BLS Proof-of-Possession (IETF RFC / draft alignment)
pub const BLS_POP_DOMAIN: &[u8] = b"BLS_POP_METANODE_ROOT_ANCHOR_V1:";

#[derive(Error, Debug, PartialEq, Eq)]
pub enum PopError {
    #[error("Invalid BLS public key format: {0}")]
    InvalidPublicKey(String),

    #[error("Invalid BLS signature format: {0}")]
    InvalidSignature(String),

    #[error("BLS Proof-of-Possession verification failed: {0}")]
    VerificationFailed(String),

    #[error("Validator stake cannot be zero")]
    ZeroStake,

    #[error("Committee cannot be empty")]
    EmptyCommittee,

    #[error("Duplicate validator public key detected in committee")]
    DuplicatePublicKey,
}

/// Generates a Proof-of-Possession signature for a given BLS keypair: PopSignature = Sign_sk(POP_DOMAIN || pk).
pub fn pop_sign(keypair: &BLS12381KeyPair) -> Vec<u8> {
    let mut msg = BLS_POP_DOMAIN.to_vec();
    msg.extend_from_slice(keypair.public().as_bytes());
    let sig = keypair.sign(&msg);
    sig.as_bytes().to_vec()
}

/// Verifies a BLS12-381 Proof-of-Possession signature for a public key.
pub fn pop_verify(pubkey_bytes: &[u8], pop_sig_bytes: &[u8]) -> Result<(), PopError> {
    let pubkey = BLS12381PublicKey::from_bytes(pubkey_bytes)
        .map_err(|e| PopError::InvalidPublicKey(e.to_string()))?;

    let sig = BLS12381Signature::from_bytes(pop_sig_bytes)
        .map_err(|e| PopError::InvalidSignature(e.to_string()))?;

    let mut msg = BLS_POP_DOMAIN.to_vec();
    msg.extend_from_slice(pubkey_bytes);

    pubkey
        .verify(&msg, &sig)
        .map_err(|e| PopError::VerificationFailed(e.to_string()))?;

    Ok(())
}

/// Validates a single validator entry: verifies stake > 0 and PoP signature.
pub fn validate_committee_entry(entry: &ValidatorEntry) -> Result<(), PopError> {
    if entry.stake == 0 {
        return Err(PopError::ZeroStake);
    }
    pop_verify(&entry.pubkey_bls, &entry.pop_signature)
}

/// Validates a full validator committee registration on `ChainRegistry`:
/// - Checks non-empty committee
/// - Verifies PoP signature for every validator entry
/// - Rejects duplicate public keys
pub fn validate_committee(committee: &[ValidatorEntry]) -> Result<(), PopError> {
    if committee.is_empty() {
        return Err(PopError::EmptyCommittee);
    }

    let mut seen_keys = BTreeSet::new();
    for entry in committee {
        validate_committee_entry(entry)?;
        if !seen_keys.insert(entry.pubkey_bls.clone()) {
            return Err(PopError::DuplicatePublicKey);
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use fastcrypto::traits::KeyPair;
    use rand::thread_rng;

    #[test]
    fn test_bls_pop_valid_keypair_verification() {
        let mut rng = thread_rng();
        let kp = BLS12381KeyPair::generate(&mut rng);
        let pub_bytes = kp.public().as_bytes().to_vec();
        let pop_sig = pop_sign(&kp);

        // Valid verify should succeed
        assert!(pop_verify(&pub_bytes, &pop_sig).is_ok());

        // ValidatorEntry validation should succeed
        let entry = ValidatorEntry {
            pubkey_bls: pub_bytes.clone(),
            stake: 1000,
            pop_signature: pop_sig.clone(),
        };
        assert!(validate_committee_entry(&entry).is_ok());

        // Zero stake should fail
        let zero_stake_entry = ValidatorEntry {
            pubkey_bls: pub_bytes,
            stake: 0,
            pop_signature: pop_sig,
        };
        assert_eq!(
            validate_committee_entry(&zero_stake_entry),
            Err(PopError::ZeroStake)
        );
    }

    #[test]
    fn test_bls_pop_rogue_key_attack_prevention() {
        let mut rng = thread_rng();

        // Legitimate victim validator
        let victim_kp = BLS12381KeyPair::generate(&mut rng);
        let victim_pub = victim_kp.public().as_bytes().to_vec();
        let victim_pop = pop_sign(&victim_kp);

        // Attacker creates their own keypair
        let attacker_kp = BLS12381KeyPair::generate(&mut rng);
        let _attacker_pub = attacker_kp.public().as_bytes().to_vec();

        // 1. Rogue Key Attack Case A: Attacker attempts to register victim's public key with attacker's signature
        let attacker_pop_for_victim_pub = attacker_kp.sign(&[BLS_POP_DOMAIN, &victim_pub].concat()).as_bytes().to_vec();
        let rogue_entry_a = ValidatorEntry {
            pubkey_bls: victim_pub.clone(),
            stake: 500,
            pop_signature: attacker_pop_for_victim_pub,
        };
        let res_a = validate_committee_entry(&rogue_entry_a);
        assert!(
            res_a.is_err(),
            "Rogue key registration must fail when attacker does not own the victim's private key"
        );

        // 2. Rogue Key Attack Case B: Attacker attempts to use victim's valid PoP for attacker's public key
        let rogue_entry_b = ValidatorEntry {
            pubkey_bls: attacker_kp.public().as_bytes().to_vec(),
            stake: 500,
            pop_signature: victim_pop.clone(),
        };
        let res_b = validate_committee_entry(&rogue_entry_b);
        assert!(
            res_b.is_err(),
            "Reusing another validator's PoP signature for a different public key must fail"
        );

        // 3. Rogue Key Attack Case C: Corrupted/Random PoP bytes
        let rogue_entry_c = ValidatorEntry {
            pubkey_bls: victim_pub,
            stake: 500,
            pop_signature: vec![0u8; 48], // Invalid signature length/data
        };
        assert!(validate_committee_entry(&rogue_entry_c).is_err());
    }

    #[test]
    fn test_validate_committee_full() {
        let mut rng = thread_rng();

        let kp1 = BLS12381KeyPair::generate(&mut rng);
        let kp2 = BLS12381KeyPair::generate(&mut rng);
        let kp3 = BLS12381KeyPair::generate(&mut rng);

        let entry1 = ValidatorEntry {
            pubkey_bls: kp1.public().as_bytes().to_vec(),
            stake: 1000,
            pop_signature: pop_sign(&kp1),
        };
        let entry2 = ValidatorEntry {
            pubkey_bls: kp2.public().as_bytes().to_vec(),
            stake: 2000,
            pop_signature: pop_sign(&kp2),
        };
        let entry3 = ValidatorEntry {
            pubkey_bls: kp3.public().as_bytes().to_vec(),
            stake: 3000,
            pop_signature: pop_sign(&kp3),
        };

        // Valid committee with 3 validators
        let committee = vec![entry1.clone(), entry2.clone(), entry3];
        assert!(validate_committee(&committee).is_ok());

        // Empty committee rejected
        assert_eq!(validate_committee(&[]), Err(PopError::EmptyCommittee));

        // Duplicate validator rejected
        let dup_committee = vec![entry1, entry2, entry1_dup(&kp1)];
        assert_eq!(
            validate_committee(&dup_committee),
            Err(PopError::DuplicatePublicKey)
        );
    }

    fn entry1_dup(kp: &BLS12381KeyPair) -> ValidatorEntry {
        ValidatorEntry {
            pubkey_bls: kp.public().as_bytes().to_vec(),
            stake: 500,
            pop_signature: pop_sign(kp),
        }
    }
}
