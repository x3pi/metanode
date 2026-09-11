// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Quorum-Certified Payload-Loss Attestation (2026-09-11)
//!
//! See note/consensus_local_dag_trust_gap_design_2026-09.md mục 11 for the full design
//! writeup and rationale -- this module implements the core, self-contained piece: the
//! signed attestation message, its signing/verification, and quorum aggregation into a
//! `PayloadLossCertificate`. The network RPC to actually gossip these between peers, and
//! the wiring into `block_sending.rs`'s skip path, are separate, later increments -- this
//! module is deliberately usable and testable in isolation first.
//!
//! CONTEXT (why this exists): mục 10 fixed `BlockDeliveryManager` to halt-and-retry forever
//! (instead of panicking) when a commit's transaction payload is confirmed missing from a
//! node's local `TxPayloadCache` and from every peer it could individually reach. That is
//! safe but can never self-resolve if the payload is genuinely gone from *every* node in the
//! committee at once (e.g. a simultaneous full-cluster power loss, not just a routine
//! restart) -- since the transaction was never applied to any node's state before delivery
//! failed, skipping it is logically safe (nothing to fork over), but only if the decision to
//! skip is itself established via quorum agreement, not one node's unilateral local
//! conclusion (a node that is merely partitioned from the network, not genuinely missing the
//! data, must never be outvoted by nodes that skip without it). This mirrors Sui's own real
//! "Network Stall Resolution" precedent already cited in mục 4.5: halt, and only proceed
//! after a verified (here: quorum-certified) decision -- never a silent, unilateral guess.
//!
//! DELIBERATELY NOT wired to any automatic trigger yet (see mục 11.2 point 6): collecting
//! attestations must be operator-initiated after `CONSENSUS-HALT-TX-PAYLOAD-LOST` (mục 10)
//! has been showing for a genuinely long time, not something that fires on its own after a
//! short timeout -- an automatic trigger risks treating a transient network partition as
//! confirmed permanent loss.

use consensus_config::{AuthorityIndex, Committee, ProtocolKeyPair, ProtocolKeySignature, ProtocolPublicKey};
use consensus_types::block::TxDigest;
use serde::{Deserialize, Serialize};
use shared_crypto::intent::{Intent, IntentMessage, IntentScope};

use crate::{
    commit::CommitIndex,
    error::{ConsensusError, ConsensusResult},
    stake_aggregator::{QuorumThreshold, StakeAggregator},
};

/// The claim one authority is signing: "for this exact (commit_index, tx_digest), I have
/// checked my own TxPayloadCache and queried every peer I could reach, and none of us --
/// including me -- has this transaction's payload." Kept minimal and specific (bound to one
/// exact commit+digest pair) so a signature can never be replayed against a different claim.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Hash)]
pub struct PayloadLossClaim {
    pub commit_index: CommitIndex,
    pub tx_digest: TxDigest,
}

/// One authority's signed attestation of a `PayloadLossClaim`.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct PayloadLossAttestation {
    pub claim: PayloadLossClaim,
    pub authority: AuthorityIndex,
    /// Serialized `ProtocolKeySignature` over `to_intent_message(claim)`.
    pub signature: Vec<u8>,
}

fn to_intent_message(claim: &PayloadLossClaim) -> IntentMessage<PayloadLossClaim> {
    IntentMessage::new(
        Intent::consensus_app(IntentScope::PayloadLossAttestation),
        claim.clone(),
    )
}

impl PayloadLossAttestation {
    /// Signs a `PayloadLossClaim` as the given authority. The caller is responsible for
    /// having actually verified the claim is true locally (checked its own cache and queried
    /// reachable peers) before calling this -- signing is not itself a truth check.
    pub fn sign(
        claim: PayloadLossClaim,
        authority: AuthorityIndex,
        keypair: &ProtocolKeyPair,
    ) -> ConsensusResult<Self> {
        let message = bcs::to_bytes(&to_intent_message(&claim))
            .map_err(ConsensusError::SerializationFailure)?;
        let signature = keypair.sign(&message);
        Ok(Self {
            claim,
            authority,
            signature: signature.to_bytes().to_vec(),
        })
    }

    /// Verifies this attestation's signature was produced by `pubkey` over its own claim.
    /// Does NOT verify the claim is actually true -- only that the named authority really
    /// signed it (the authority's own honesty about having checked is a trust assumption, the
    /// same one the rest of BFT consensus already makes about honest-majority behavior).
    pub fn verify(&self, pubkey: &ProtocolPublicKey) -> ConsensusResult<()> {
        let message = bcs::to_bytes(&to_intent_message(&self.claim))
            .map_err(ConsensusError::SerializationFailure)?;
        let sig = ProtocolKeySignature::from_bytes(&self.signature)
            .map_err(ConsensusError::MalformedSignature)?;
        pubkey
            .verify(&message, &sig)
            .map_err(ConsensusError::SignatureVerificationFailure)
    }
}

/// Collects `PayloadLossAttestation`s for one exact `PayloadLossClaim` and reports whether
/// 2f+1 stake has confirmed it -- once true, every honest node that independently reaches (or
/// receives and verifies) the same conclusion is safe to treat the transaction as permanently
/// absent and skip it identically, per mục 11.2's fork-safety argument.
pub struct PayloadLossAggregator {
    claim: PayloadLossClaim,
    aggregator: StakeAggregator<QuorumThreshold>,
    attestations: Vec<PayloadLossAttestation>,
}

impl PayloadLossAggregator {
    pub fn new(claim: PayloadLossClaim) -> Self {
        Self {
            claim,
            aggregator: StakeAggregator::new(),
            attestations: Vec::new(),
        }
    }

    /// Verifies and adds one attestation. Rejects (returns Err, does not count) an
    /// attestation for a different claim or with an invalid signature -- a byzantine peer
    /// cannot contribute stake toward quorum without a genuinely valid signature over the
    /// exact claim this aggregator is collecting for.
    pub fn add(
        &mut self,
        attestation: PayloadLossAttestation,
        committee: &Committee,
    ) -> ConsensusResult<bool> {
        if attestation.claim != self.claim {
            return Err(ConsensusError::InvalidPayloadLossAttestation(format!(
                "attestation claim {:?} does not match aggregator claim {:?}",
                attestation.claim, self.claim
            )));
        }
        let pubkey = committee.authority(attestation.authority).protocol_key.clone();
        attestation.verify(&pubkey)?;
        let reached = self
            .aggregator
            .add_unique(attestation.authority, committee);
        self.attestations.push(attestation);
        Ok(reached && self.aggregator.reached_threshold(committee))
    }

    pub fn reached_quorum(&self, committee: &Committee) -> bool {
        self.aggregator.reached_threshold(committee)
    }

    /// Once quorum is reached, this is the portable certificate: the exact claim plus every
    /// verified attestation that contributed to it. Any node can re-verify this from scratch
    /// (re-check every signature, re-sum stake against its own view of the committee) without
    /// trusting whoever sent it -- see `PayloadLossCertificate::verify`.
    pub fn into_certificate(self, committee: &Committee) -> Option<PayloadLossCertificate> {
        if !self.reached_quorum(committee) {
            return None;
        }
        Some(PayloadLossCertificate {
            claim: self.claim,
            attestations: self.attestations,
        })
    }
}

/// A quorum-certified, self-verifying claim that a transaction's payload is permanently
/// unrecoverable. Safe to broadcast and trust once `verify()` passes -- the receiving node
/// does not need to have collected the attestations itself.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct PayloadLossCertificate {
    pub claim: PayloadLossClaim,
    pub attestations: Vec<PayloadLossAttestation>,
}

impl PayloadLossCertificate {
    /// Re-verifies every attestation's signature and re-sums stake against `committee`,
    /// independent of whatever aggregator (if any) originally produced this certificate.
    /// Deduplicates by authority (a byzantine sender padding the list with repeats of the
    /// same authority must not inflate the stake sum).
    pub fn verify(&self, committee: &Committee) -> ConsensusResult<()> {
        let mut aggregator = StakeAggregator::<QuorumThreshold>::new();
        for attestation in &self.attestations {
            if attestation.claim != self.claim {
                return Err(ConsensusError::InvalidPayloadLossAttestation(format!(
                    "certificate contains an attestation for a different claim: {:?} != {:?}",
                    attestation.claim, self.claim
                )));
            }
            let pubkey = committee.authority(attestation.authority).protocol_key.clone();
            attestation.verify(&pubkey)?;
            aggregator.add_unique(attestation.authority, committee);
        }
        if !aggregator.reached_threshold(committee) {
            return Err(ConsensusError::InvalidPayloadLossAttestation(format!(
                "certificate for claim {:?} has only {} stake, below quorum threshold {}",
                self.claim,
                aggregator.stake(),
                aggregator.threshold(committee)
            )));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_claim() -> PayloadLossClaim {
        PayloadLossClaim {
            commit_index: 42,
            tx_digest: TxDigest([7u8; consensus_config::DIGEST_LENGTH]),
        }
    }

    #[tokio::test]
    async fn sign_and_verify_roundtrip() {
        let (context, key_pairs) = crate::context::Context::new_for_test(4);
        let committee = &context.committee;
        let (authority, (network_keypair, protocol_keypair)) =
            (AuthorityIndex::new_for_test(0), key_pairs[0].clone());
        let _ = network_keypair;
        let attestation =
            PayloadLossAttestation::sign(test_claim(), authority, &protocol_keypair).unwrap();
        let pubkey = committee.authority(authority).protocol_key.clone();
        assert!(attestation.verify(&pubkey).is_ok());
    }

    #[tokio::test]
    async fn verify_rejects_wrong_signer_pubkey() {
        let (context, key_pairs) = crate::context::Context::new_for_test(4);
        let committee = &context.committee;
        let attestation = PayloadLossAttestation::sign(
            test_claim(),
            AuthorityIndex::new_for_test(0),
            &key_pairs[0].1,
        )
        .unwrap();
        // Verify against authority 1's pubkey instead of authority 0's -- must fail.
        let wrong_pubkey = committee
            .authority(AuthorityIndex::new_for_test(1))
            .protocol_key
            .clone();
        assert!(attestation.verify(&wrong_pubkey).is_err());
    }

    #[tokio::test]
    async fn aggregator_reaches_quorum_only_after_enough_stake() {
        let (context, key_pairs) = crate::context::Context::new_for_test(4);
        let committee = context.committee.clone();
        let claim = test_claim();
        let mut agg = PayloadLossAggregator::new(claim.clone());

        // 4 equal-stake authorities, quorum = 2f+1 = 3 of 4 -- one attestation must not be
        // enough.
        let att0 = PayloadLossAttestation::sign(
            claim.clone(),
            AuthorityIndex::new_for_test(0),
            &key_pairs[0].1,
        )
        .unwrap();
        let reached = agg.add(att0, &committee).unwrap();
        assert!(!reached, "1 of 4 must not reach quorum");
        assert!(!agg.reached_quorum(&committee));

        let att1 = PayloadLossAttestation::sign(
            claim.clone(),
            AuthorityIndex::new_for_test(1),
            &key_pairs[1].1,
        )
        .unwrap();
        let reached = agg.add(att1, &committee).unwrap();
        assert!(!reached, "2 of 4 must not reach quorum");

        let att2 = PayloadLossAttestation::sign(
            claim.clone(),
            AuthorityIndex::new_for_test(2),
            &key_pairs[2].1,
        )
        .unwrap();
        let reached = agg.add(att2, &committee).unwrap();
        assert!(reached, "3 of 4 must reach quorum (2f+1 with f=1)");
        assert!(agg.reached_quorum(&committee));

        let cert = agg.into_certificate(&committee).unwrap();
        assert!(cert.verify(&committee).is_ok());
    }

    #[tokio::test]
    async fn aggregator_rejects_attestation_for_a_different_claim() {
        let (context, key_pairs) = crate::context::Context::new_for_test(4);
        let committee = context.committee.clone();
        let mut agg = PayloadLossAggregator::new(test_claim());

        let mut other_claim = test_claim();
        other_claim.commit_index += 1;
        let mismatched = PayloadLossAttestation::sign(
            other_claim,
            AuthorityIndex::new_for_test(0),
            &key_pairs[0].1,
        )
        .unwrap();
        assert!(agg.add(mismatched, &committee).is_err());
    }

    #[tokio::test]
    async fn certificate_verify_rejects_duplicate_authority_padding() {
        // A byzantine sender cannot inflate stake by repeating the same authority's
        // attestation multiple times in the attestations list.
        let (context, key_pairs) = crate::context::Context::new_for_test(4);
        let committee = context.committee.clone();
        let claim = test_claim();
        let att0 = PayloadLossAttestation::sign(
            claim.clone(),
            AuthorityIndex::new_for_test(0),
            &key_pairs[0].1,
        )
        .unwrap();
        let cert = PayloadLossCertificate {
            claim,
            attestations: vec![att0.clone(), att0.clone(), att0],
        };
        // Only 1 unique authority's stake, well below quorum for a 4-node committee.
        assert!(cert.verify(&committee).is_err());
    }
}
