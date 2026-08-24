// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Relayer Reference Engine & Scenario Test Suite (Phase P4)
//!
//! Implements Section 14 (P4.1 & P4.2) and Section 10 (8 User Scenarios):
//! - P4.1: Permissionless relayer scanner, multi-hop routing, Merkle proofs, commit certification
//! - P4.2: Relay tip claiming & competition ("First come, first served", zero double-spending)
//! - 8 User Scenarios (10.1 - 10.8) as official Definition of Done (DoD)

use std::collections::{BTreeMap, BTreeSet};
use fastcrypto::bls12381::min_sig::{
    BLS12381AggregateSignature, BLS12381KeyPair, BLS12381Signature,
};
use fastcrypto::traits::{AggregateAuthenticator, Signer, ToFromBytes};
use serde::{Deserialize, Serialize};

use crate::types::cross_chain::{
    Address, CrossChainMessage, Hash, MerkleProof, MessageStatus, QuorumCert, U256,
};
use crate::types::gateway::{
    GatewayEngine, GatewayError, OutboundParams,
};

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct RelayerConfig {
    pub relayer_address: Address,
    pub reserve_chain_id: u64,
    pub batch_size: usize,
    pub max_retries: usize,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct CertifiedCommitData {
    pub source_chain_id: u64,
    pub commit_root: Hash,
    pub epoch: u64,
    pub cert: QuorumCert,
    pub messages: Vec<CrossChainMessage>,
    pub merkle_layers: Vec<Vec<Hash>>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct RelayReceipt {
    pub message_id: Hash,
    pub source_chain_id: u64,
    pub dest_chain_id: u64,
    pub status: MessageStatus,
    pub relayer: Address,
    pub tip_collected: U256,
    pub routes: Vec<String>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct RelayerStats {
    pub total_relayed: u64,
    pub total_tips_collected: U256,
    pub failed_relays: u64,
    pub direct_message_count: u64,
    pub reserve_routed_count: u64,
}

pub struct RelayerEngine {
    pub config: RelayerConfig,
    pub chains: BTreeMap<u64, GatewayEngine>,
    pub signers: BTreeMap<u64, Vec<BLS12381KeyPair>>,
    pub pending_outbounds: BTreeMap<u64, Vec<CrossChainMessage>>,
    pub certified_commits: BTreeMap<(u64, Hash), CertifiedCommitData>,
    pub offline_chains: BTreeSet<u64>,
    pub stats: RelayerStats,
}

impl RelayerEngine {
    pub fn new(config: RelayerConfig, chains: BTreeMap<u64, GatewayEngine>) -> Self {
        Self {
            config,
            chains,
            signers: BTreeMap::new(),
            pending_outbounds: BTreeMap::new(),
            certified_commits: BTreeMap::new(),
            offline_chains: BTreeSet::new(),
            stats: RelayerStats::default(),
        }
    }

    pub fn set_signers(&mut self, chain_id: u64, signers: Vec<BLS12381KeyPair>) {
        self.signers.insert(chain_id, signers);
    }

    pub fn sign_commit(&self, chain_id: u64, commit_root: Hash, signer_bitmap: &[u8]) -> Vec<u8> {
        let keypairs = match self.signers.get(&chain_id) {
            Some(kps) if !kps.is_empty() => kps,
            _ => return vec![0u8; 48],
        };

        let mut commit_msg = b"COMMIT_ROOT_ATTEST_V1:".to_vec();
        commit_msg.extend_from_slice(&commit_root.0);

        let mut sigs: Vec<BLS12381Signature> = Vec::new();
        for (i, kp) in keypairs.iter().enumerate() {
            let mut is_signer = false;
            if !signer_bitmap.is_empty() {
                let byte_idx = i / 8;
                let bit_idx = i % 8;
                if byte_idx < signer_bitmap.len() && (signer_bitmap[byte_idx] & (1 << bit_idx)) != 0 {
                    is_signer = true;
                }
            } else if keypairs.len() == 1 {
                is_signer = true;
            }

            if is_signer {
                sigs.push(kp.sign(&commit_msg));
            }
        }

        if sigs.is_empty() {
            return vec![0u8; 48];
        }

        if sigs.len() == 1 {
            sigs[0].as_bytes().to_vec()
        } else {
            let sig_refs: Vec<&BLS12381Signature> = sigs.iter().collect();
            match BLS12381AggregateSignature::aggregate(&sig_refs) {
                Ok(agg) => agg.as_bytes().to_vec(),
                Err(_) => vec![0u8; 48],
            }
        }
    }

    pub fn set_chain_offline(&mut self, chain_id: u64, offline: bool) {
        if offline {
            self.offline_chains.insert(chain_id);
        } else {
            self.offline_chains.remove(&chain_id);
        }
    }

    pub fn is_chain_online(&self, chain_id: u64) -> bool {
        !self.offline_chains.contains(&chain_id)
    }

    pub fn submit_outbound(
        &mut self,
        source_chain_id: u64,
        sender: Address,
        params: OutboundParams,
        tx_hash: Hash,
    ) -> Result<CrossChainMessage, GatewayError> {
        let engine = self
            .chains
            .get_mut(&source_chain_id)
            .ok_or(GatewayError::UnknownSourceChain(source_chain_id))?;

        let msg = engine.outbound(sender, params, tx_hash)?;
        self.pending_outbounds
            .entry(source_chain_id)
            .or_default()
            .push(msg.clone());

        Ok(msg)
    }

    pub fn certify_commit(
        &mut self,
        source_chain_id: u64,
        epoch: u64,
        msgs: Vec<CrossChainMessage>,
        signer_bitmap: Vec<u8>,
    ) -> Result<CertifiedCommitData, String> {
        if msgs.is_empty() {
            return Err("Cannot certify empty commit".to_string());
        }

        let (root, layers) = build_merkle_tree_from_messages(&msgs)?;
        let bitmap = if signer_bitmap.is_empty() {
            vec![0xFF]
        } else {
            signer_bitmap
        };

        let aggregate_signature = self.sign_commit(source_chain_id, root, &bitmap);

        let cert = QuorumCert {
            epoch,
            aggregate_signature,
            signer_bitmap: bitmap,
        };

        let data = CertifiedCommitData {
            source_chain_id,
            commit_root: root,
            epoch,
            cert,
            messages: msgs.clone(),
            merkle_layers: layers,
        };

        self.certified_commits
            .insert((source_chain_id, root), data.clone());

        // Remove from pending outbounds
        if let Some(pending) = self.pending_outbounds.get_mut(&source_chain_id) {
            let processed_ids: BTreeSet<Hash> = msgs.iter().map(|m| m.message_id).collect();
            pending.retain(|m| !processed_ids.contains(&m.message_id));
        }

        Ok(data)
    }

    pub fn relay_message(
        &mut self,
        msg: CrossChainMessage,
        proof: MerkleProof,
        commit_root: Hash,
        cert: QuorumCert,
        relayer_addr: Address,
    ) -> Result<RelayReceipt, GatewayError> {
        let relayer = if relayer_addr == Address::ZERO {
            self.config.relayer_address
        } else {
            relayer_addr
        };

        // Scenario 10.4: If destination is offline, reject with pending error (Zero-Fork)
        if self.offline_chains.contains(&msg.dest_chain_id) {
            return Err(GatewayError::NoActiveContext); // offline marker
        }

        let mut routes = Vec::new();
        let tip_collected = msg.tip;

        // Route A: Value > 0 or AssetID != 0 -> 2-hop via Reserve Chain (Section 2.2(b), 2.3 & 10.1)
        if msg.value > U256::zero() || msg.asset_id > U256::zero() {
            let reserve_chain_id = self.config.reserve_chain_id;

            if self.offline_chains.contains(&reserve_chain_id) {
                return Err(GatewayError::NoActiveContext);
            }

            // Step 1: Attest commit on Reserve (checking allocation ceiling)
            {
                let reserve_engine = self
                    .chains
                    .get_mut(&reserve_chain_id)
                    .ok_or(GatewayError::UnknownSourceChain(reserve_chain_id))?;

                reserve_engine.attest_commit(
                    msg.source_chain_id,
                    commit_root,
                    msg.value,
                    cert,
                )?;
                routes.push(format!(
                    "{} -> {} (Reserve Attest)",
                    msg.source_chain_id, reserve_chain_id
                ));
            }

            // Step 2: Forward to Destination Chain (verified via Reserve's attestation)
            let reserve_epoch = self
                .chains
                .get(&msg.dest_chain_id)
                .and_then(|d| d.chain_registry.get(&reserve_chain_id))
                .map(|r| r.epoch)
                .unwrap_or(1);

            let reserve_sig = self.sign_commit(reserve_chain_id, commit_root, &[0xFF]);
            let reserve_cert = QuorumCert {
                epoch: reserve_epoch,
                aggregate_signature: reserve_sig,
                signer_bitmap: vec![0xFF],
            };

            let dest_engine = self
                .chains
                .get_mut(&msg.dest_chain_id)
                .ok_or(GatewayError::UnknownSourceChain(msg.dest_chain_id))?;

            // Authentication only (Reserve is the unconditional issuer, no ceiling to debit here
            // — Section 2.3.1 fix). claim_message below is what actually credits
            // dest_chain_id's allocation.
            dest_engine.attest_reserve_issued_commit(
                reserve_chain_id,
                commit_root,
                msg.value,
                reserve_cert,
            )?;

            let status = dest_engine.claim_message(msg.clone(), proof, commit_root, relayer)?;
            routes.push(format!(
                "{} -> {} (Mint & Execute)",
                reserve_chain_id, msg.dest_chain_id
            ));

            // Section 2.3.1 fix, Rust-specific note: each simulated chain in `self.chains` owns
            // an independently CLONED GlobalSupplyLedger (see setup_test_network / any real
            // caller constructing GatewayEngine per chain) rather than a single shared instance —
            // so the credit claim_message() just applied only landed on dest_engine's own clone,
            // invisible to Reserve's. Reserve is the sole authoritative ledger (Section 2.1: "CHỈ
            // Root Anchor mới được ghi"), so mirror the credit onto Reserve's own copy here too —
            // this is what every other caller (dashboards, ProcessRefund-equivalent, tests) reads.
            if msg.value > U256::zero() {
                let reserve_engine = self
                    .chains
                    .get_mut(&reserve_chain_id)
                    .ok_or(GatewayError::UnknownSourceChain(reserve_chain_id))?;
                let current = reserve_engine
                    .supply_ledger
                    .per_chain_allocation
                    .get(&msg.dest_chain_id)
                    .copied()
                    .unwrap_or(U256::zero());
                reserve_engine
                    .supply_ledger
                    .per_chain_allocation
                    .insert(msg.dest_chain_id, current + msg.value);
            }

            self.stats.total_relayed += 1;
            self.stats.reserve_routed_count += 1;
            self.stats.total_tips_collected += tip_collected;

            return Ok(RelayReceipt {
                message_id: msg.message_id,
                source_chain_id: msg.source_chain_id,
                dest_chain_id: msg.dest_chain_id,
                status,
                relayer,
                tip_collected,
                routes,
            });
        }

        // Route B: Value == 0 (Pure Message / Contract Call) -> Direct 1-hop
        let dest_engine = self
            .chains
            .get_mut(&msg.dest_chain_id)
            .ok_or(GatewayError::UnknownSourceChain(msg.dest_chain_id))?;

        dest_engine.attest_commit(msg.source_chain_id, commit_root, U256::zero(), cert)?;
        let status = dest_engine.claim_message(msg.clone(), proof, commit_root, relayer)?;
        routes.push(format!(
            "{} -> {} (Direct Call)",
            msg.source_chain_id, msg.dest_chain_id
        ));

        self.stats.total_relayed += 1;
        self.stats.direct_message_count += 1;
        self.stats.total_tips_collected += tip_collected;

        Ok(RelayReceipt {
            message_id: msg.message_id,
            source_chain_id: msg.source_chain_id,
            dest_chain_id: msg.dest_chain_id,
            status,
            relayer,
            tip_collected,
            routes,
        })
    }

    pub fn relay_commit(
        &mut self,
        source_chain_id: u64,
        commit_root: Hash,
        relayer_addr: Address,
    ) -> Result<Vec<RelayReceipt>, GatewayError> {
        let commit_data = self
            .certified_commits
            .get(&(source_chain_id, commit_root))
            .cloned()
            .ok_or(GatewayError::CommitNotAttested(
                commit_root,
                source_chain_id,
            ))?;

        let mut receipts = Vec::new();
        for (i, msg) in commit_data.messages.iter().enumerate() {
            let proof = get_merkle_proof(&commit_data.merkle_layers, i);
            let rcpt = self.relay_message(
                msg.clone(),
                proof,
                commit_data.commit_root,
                commit_data.cert.clone(),
                relayer_addr,
            )?;
            receipts.push(rcpt);
        }
        Ok(receipts)
    }

    pub fn compete_relayers(
        &mut self,
        msg: CrossChainMessage,
        proof: MerkleProof,
        commit_root: Hash,
        cert: QuorumCert,
        relayers: Vec<Address>,
    ) -> (
        Address,
        Result<RelayReceipt, GatewayError>,
        Vec<(Address, Result<RelayReceipt, GatewayError>)>,
    ) {
        if relayers.is_empty() {
            return (
                Address::ZERO,
                Err(GatewayError::NoActiveContext),
                Vec::new(),
            );
        }

        let winner = relayers[0];
        let winner_rcpt = self.relay_message(
            msg.clone(),
            proof.clone(),
            commit_root,
            cert.clone(),
            winner,
        );

        let mut losers = Vec::new();
        for &competing in &relayers[1..] {
            let loser_res = self.relay_message(
                msg.clone(),
                proof.clone(),
                commit_root,
                cert.clone(),
                competing,
            );
            losers.push((competing, loser_res));
        }

        (winner, winner_rcpt, losers)
    }

    pub fn process_refund(
        &mut self,
        source_chain_id: u64,
        _dest_chain_id: u64,
        message_id: Hash,
        sender: Address,
        amount: U256,
        is_failed_proof_valid: bool,
    ) -> Result<(), GatewayError> {
        let source_engine = self
            .chains
            .get_mut(&source_chain_id)
            .ok_or(GatewayError::UnknownSourceChain(source_chain_id))?;

        source_engine.refund(message_id, source_chain_id, sender, amount, is_failed_proof_valid)?;

        // Restore Reserve allocation if value was transferred
        if amount > U256::zero() {
            let reserve_chain_id = self.config.reserve_chain_id;
            if let Some(reserve_engine) = self.chains.get_mut(&reserve_chain_id) {
                let curr_alloc = reserve_engine
                    .supply_ledger
                    .per_chain_allocation
                    .get(&source_chain_id)
                    .copied()
                    .unwrap_or(U256::zero());
                reserve_engine
                    .supply_ledger
                    .per_chain_allocation
                    .insert(source_chain_id, curr_alloc + amount);
            }
        }

        Ok(())
    }
}

pub fn build_merkle_tree(leaves: &[Hash]) -> (Hash, Vec<Vec<Hash>>) {
    if leaves.is_empty() {
        return (Hash::ZERO, Vec::new());
    }

    let mut layers = vec![leaves.to_vec()];
    let mut current = leaves.to_vec();

    while current.len() > 1 {
        let mut next_layer = Vec::new();
        for chunk in current.chunks(2) {
            if chunk.len() == 2 {
                next_layer.push(hash_pair(chunk[0], chunk[1]));
            } else {
                next_layer.push(chunk[0]);
            }
        }
        layers.push(next_layer.clone());
        current = next_layer;
    }

    (current[0], layers)
}

fn hash_pair(a: Hash, b: Hash) -> Hash {
    crate::types::gateway::hash_pair(a, b)
}

pub fn get_merkle_proof(layers: &[Vec<Hash>], leaf_index: usize) -> MerkleProof {
    let mut siblings = Vec::new();
    let mut idx = leaf_index;

    for layer in layers.iter().take(layers.len() - 1) {
        let sibling_index = if idx % 2 == 0 { idx + 1 } else { idx - 1 };
        if sibling_index < layer.len() {
            siblings.push(layer[sibling_index]);
        }
        idx /= 2;
    }

    MerkleProof {
        leaf_index: leaf_index as u64,
        siblings,
    }
}

pub fn build_merkle_tree_from_messages(
    msgs: &[CrossChainMessage],
) -> Result<(Hash, Vec<Vec<Hash>>), String> {
    if msgs.is_empty() {
        return Err("Empty messages".to_string());
    }

    let mut leaves = Vec::new();
    for m in msgs {
        leaves.push(crate::types::gateway::compute_message_leaf_hash(m));
    }

    Ok(build_merkle_tree(&leaves))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::cross_chain::{AccountLeaf, ChainRegistry, GlobalSupplyLedger, GovernanceProposalKind, ValidatorEntry};
    use crate::types::gateway::keccak256;
    use crate::types::governance::GovernanceEngine;
    use fastcrypto::traits::KeyPair;

    fn setup_test_network() -> (RelayerEngine, BTreeMap<u64, GatewayEngine>) {
        let mut rng = rand::thread_rng();
        let kp1000 = BLS12381KeyPair::generate(&mut rng);
        let kp101 = BLS12381KeyPair::generate(&mut rng);
        let kp102 = BLS12381KeyPair::generate(&mut rng);
        let kp103 = BLS12381KeyPair::generate(&mut rng);

        let val1000 = ValidatorEntry {
            pubkey_bls: kp1000.public().as_bytes().to_vec(),
            stake: 100,
            pop_signature: vec![],
        };
        let val101 = ValidatorEntry {
            pubkey_bls: kp101.public().as_bytes().to_vec(),
            stake: 100,
            pop_signature: vec![],
        };
        let val102 = ValidatorEntry {
            pubkey_bls: kp102.public().as_bytes().to_vec(),
            stake: 100,
            pop_signature: vec![],
        };
        let val103 = ValidatorEntry {
            pubkey_bls: kp103.public().as_bytes().to_vec(),
            stake: 100,
            pop_signature: vec![],
        };

        let mut registry = BTreeMap::new();
        registry.insert(
            1000,
            ChainRegistry {
                chain_id: 1000,
                committee: vec![val1000],
                epoch: 1,
                quorum_threshold: 6667,
                gateway_contract: Address::ZERO,
                state_root: Hash([0x10; 32]),
                archival_endpoint: "http://archive.test".to_string(),
                registered_at: 1000,
            },
        );
        registry.insert(
            101,
            ChainRegistry {
                chain_id: 101,
                committee: vec![val101],
                epoch: 1,
                quorum_threshold: 6667,
                gateway_contract: Address::ZERO,
                state_root: Hash([0x11; 32]),
                archival_endpoint: "http://archive.test".to_string(),
                registered_at: 1000,
            },
        );
        registry.insert(
            102,
            ChainRegistry {
                chain_id: 102,
                committee: vec![val102],
                epoch: 1,
                quorum_threshold: 6667,
                gateway_contract: Address::ZERO,
                state_root: Hash([0x12; 32]),
                archival_endpoint: "http://archive.test".to_string(),
                registered_at: 1000,
            },
        );
        registry.insert(
            103,
            ChainRegistry {
                chain_id: 103,
                committee: vec![val103],
                epoch: 1,
                quorum_threshold: 6667,
                gateway_contract: Address::ZERO,
                state_root: Hash([0x13; 32]),
                archival_endpoint: "http://archive.test".to_string(),
                registered_at: 1000,
            },
        );

        let mut allocs = BTreeMap::new();
        allocs.insert(1000, U256::from(100_000u64));
        allocs.insert(101, U256::from(5_000u64));
        allocs.insert(102, U256::from(5_000u64));
        allocs.insert(103, U256::from(500u64)); // 500 max allocation for chain 103

        let ledger = GlobalSupplyLedger::new(U256::from(110_500u64), allocs).unwrap();

        let mut chains = BTreeMap::new();
        chains.insert(1000, GatewayEngine::new(1000, registry.clone(), ledger.clone()));
        chains.insert(101, GatewayEngine::new(101, registry.clone(), ledger.clone()));
        chains.insert(102, GatewayEngine::new(102, registry.clone(), ledger.clone()));
        chains.insert(103, GatewayEngine::new(103, registry, ledger));

        let cfg = RelayerConfig {
            relayer_address: Address([0x77; 20]),
            reserve_chain_id: 1000,
            batch_size: 2000,
            max_retries: 3,
        };

        let mut engine = RelayerEngine::new(cfg, chains.clone());
        engine.set_signers(1000, vec![kp1000]);
        engine.set_signers(101, vec![kp101]);
        engine.set_signers(102, vec![kp102]);
        engine.set_signers(103, vec![kp103]);

        (engine, chains)
    }

    #[test]
    fn test_p4_2_relayer_competition_and_tip_claiming() {
        let (mut relayer_engine, _) = setup_test_network();

        let sender = Address([0x11; 20]);
        let target = Address([0x22; 20]);
        let tx_hash = Hash([0xAA; 32]);
        let tip_amount = U256::from(50u64);

        let params = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: vec![1, 2, 3],
            asset_id: U256::zero(),
            value: U256::zero(),
            tip: tip_amount,
            hop_count: 1,
            ordered: false,
        };

        let msg = relayer_engine.submit_outbound(101, sender, params, tx_hash).unwrap();
        let commit = relayer_engine.certify_commit(101, 1, vec![msg.clone()], vec![]).unwrap();
        let proof = get_merkle_proof(&commit.merkle_layers, 0);

        let relayer1 = Address([0xA1; 20]);
        let relayer2 = Address([0xB2; 20]);

        let (winner, winner_rcpt, losers) = relayer_engine.compete_relayers(
            msg, proof, commit.commit_root, commit.cert, vec![relayer1, relayer2]
        );

        assert_eq!(winner, relayer1);
        let rcpt = winner_rcpt.unwrap();
        assert_eq!(rcpt.status, MessageStatus::Success);
        assert_eq!(rcpt.tip_collected, tip_amount);

        // Loser relayer2 rejected
        assert_eq!(losers.len(), 1);
        assert_eq!(losers[0].0, relayer2);
        assert_eq!(losers[0].1, Err(GatewayError::AlreadyClaimed(tx_hash)));
    }

    #[test]
    fn test_scenario_10_1_native_transfer_via_reserve() {
        let (mut relayer_engine, _) = setup_test_network();

        let sender = Address([0x11; 20]);
        let recipient = Address([0x22; 20]);
        let tx_hash = Hash([0x11; 32]);
        let transfer_amount = U256::from(100u64);
        let tip_amount = U256::from(5u64);

        let params = OutboundParams {
            dest_chain_id: 102,
            target: recipient,
            payload: vec![],
            asset_id: U256::zero(),
            value: transfer_amount,
            tip: tip_amount,
            hop_count: 1,
            ordered: false,
        };

        let msg = relayer_engine.submit_outbound(101, sender, params, tx_hash).unwrap();
        let commit = relayer_engine.certify_commit(101, 1, vec![msg], vec![]).unwrap();

        let receipts = relayer_engine.relay_commit(101, commit.commit_root, Address([0x77; 20])).unwrap();
        assert_eq!(receipts.len(), 1);
        assert_eq!(receipts[0].status, MessageStatus::Success);
        assert_eq!(receipts[0].routes.len(), 2); // 101 -> 1000 -> 102

        let reserve = relayer_engine.chains.get(&1000).unwrap();
        assert_eq!(
            reserve.supply_ledger.per_chain_allocation.get(&101).copied().unwrap(),
            U256::from(4900u64)
        );

        // Section 2.3.1 regression guard: chain 102 (destination) MUST be credited by the same
        // 100, and Reserve's OWN allocation entry (chain 1000) must be untouched — without this,
        // value silently evaporates from the ledger on every successful transfer (was: 100
        // transferred -> 200 destroyed, invariant broken) even with no attacker or failure
        // involved.
        assert_eq!(
            reserve.supply_ledger.per_chain_allocation.get(&102).copied().unwrap(),
            U256::from(5100u64)
        );
        assert_eq!(
            reserve.supply_ledger.per_chain_allocation.get(&1000).copied().unwrap(),
            U256::from(100_000u64)
        );
        assert!(
            reserve.supply_ledger.verify_invariant(),
            "Σ per_chain_allocation must still equal genesis_total_supply after a successful transfer"
        );
    }

    /// Regression guard for the "double debit, zero credit" supply invariant bug found during
    /// review: attest_commit used to unconditionally debit whatever source_chain_id it was called
    /// with — including the Reserve->dest leg, where it was wrongly called with Reserve as the
    /// "source", debiting Reserve's own bucket a second time while never crediting the true
    /// destination. Runs several successful, non-adversarial transfers end-to-end through
    /// relay_commit (the real relay path, not just the isolated ledger unit) and asserts
    /// Σ per_chain_allocation == genesis_total_supply after every single one.
    #[test]
    fn test_invariant_holds_across_multiple_reserve_routed_transfers() {
        let (mut relayer_engine, _) = setup_test_network();
        let recipient = Address([0x22; 20]);

        let transfers: [(u64, u64, u64); 4] = [(101, 102, 100), (102, 101, 50), (101, 102, 1), (102, 101, 25)];

        for (i, (from_chain, to_chain, value)) in transfers.iter().enumerate() {
            let sender = Address([0x11; 20]);
            let mut tx_hash_bytes = [0x99u8; 32];
            tx_hash_bytes[31] = i as u8;
            let tx_hash = Hash(tx_hash_bytes);

            let before = relayer_engine
                .chains
                .get(&1000)
                .unwrap()
                .supply_ledger
                .per_chain_allocation
                .get(to_chain)
                .copied()
                .unwrap_or(U256::zero());

            let params = OutboundParams {
                dest_chain_id: *to_chain,
                target: recipient,
                payload: vec![],
                asset_id: U256::zero(),
                value: U256::from(*value),
                tip: U256::zero(),
                hop_count: 1,
                ordered: false,
            };

            let msg = relayer_engine
                .submit_outbound(*from_chain, sender, params, tx_hash)
                .unwrap();
            let commit = relayer_engine
                .certify_commit(*from_chain, 1, vec![msg], vec![])
                .unwrap();
            relayer_engine
                .relay_commit(*from_chain, commit.commit_root, Address([0x77; 20]))
                .unwrap();

            let ledger = &relayer_engine.chains.get(&1000).unwrap().supply_ledger;
            let after = ledger.per_chain_allocation.get(to_chain).copied().unwrap_or(U256::zero());
            assert_eq!(
                after,
                before + U256::from(*value),
                "transfer #{i}: destination chain {to_chain} must be credited exactly by the transferred value"
            );
            assert!(
                ledger.verify_invariant(),
                "transfer #{i} ({from_chain} -> {to_chain}, value {value}): Σ per_chain_allocation must equal genesis_total_supply"
            );
        }
    }

    #[test]
    fn test_scenario_10_2_contract_call_with_value() {
        let (mut relayer_engine, _) = setup_test_network();

        let sender = Address([0x11; 20]);
        let target = Address([0x22; 20]);
        let tx_hash = Hash([0x22; 32]);

        let params = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: b"buyItem(id=42)".to_vec(),
            asset_id: U256::zero(),
            value: U256::from(250u64),
            tip: U256::from(2u64),
            hop_count: 1,
            ordered: false,
        };

        let msg = relayer_engine.submit_outbound(101, sender, params, tx_hash).unwrap();
        let commit = relayer_engine.certify_commit(101, 1, vec![msg], vec![]).unwrap();

        let receipts = relayer_engine.relay_commit(101, commit.commit_root, Address([0x77; 20])).unwrap();
        assert_eq!(receipts[0].status, MessageStatus::Success);
    }

    #[test]
    fn test_scenario_10_3_contract_failed_and_automated_refund() {
        let (mut relayer_engine, _) = setup_test_network();

        let sender = Address([0x11; 20]);
        let target = Address([0x22; 20]);
        let tx_hash = Hash([0x33; 32]);
        let refund_amount = U256::from(300u64);

        let params = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: b"buy(sold_out)".to_vec(),
            asset_id: U256::zero(),
            value: refund_amount,
            tip: U256::from(1u64),
            hop_count: 1,
            ordered: false,
        };

        let msg = relayer_engine.submit_outbound(101, sender, params, tx_hash).unwrap();

        // Automated refund
        relayer_engine.process_refund(101, 102, msg.message_id, sender, refund_amount, true).unwrap();

        let source = relayer_engine.chains.get(&101).unwrap();
        assert_eq!(source.message_status.get(&msg.message_id).copied().unwrap(), MessageStatus::Refunded);
    }

    #[test]
    fn test_scenario_10_4_destination_offline_pending_zero_fork() {
        let (mut relayer_engine, _) = setup_test_network();

        let sender = Address([0x11; 20]);
        let target = Address([0x22; 20]);
        let tx_hash = Hash([0x44; 32]);

        let params = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: vec![],
            asset_id: U256::zero(),
            value: U256::from(50u64),
            tip: U256::from(1u64),
            hop_count: 1,
            ordered: false,
        };

        let msg = relayer_engine.submit_outbound(101, sender, params, tx_hash).unwrap();
        let commit = relayer_engine.certify_commit(101, 1, vec![msg], vec![]).unwrap();

        // Simulate Chain 102 offline
        relayer_engine.set_chain_offline(102, true);
        assert!(relayer_engine.relay_commit(101, commit.commit_root, Address([0x77; 20])).is_err());

        // Chain 102 recovers
        relayer_engine.set_chain_offline(102, false);
        let receipts = relayer_engine.relay_commit(101, commit.commit_root, Address([0x77; 20])).unwrap();
        assert_eq!(receipts[0].status, MessageStatus::Success);
    }

    #[test]
    fn test_scenario_10_5_two_way_hop_count_guard() {
        let (mut relayer_engine, _) = setup_test_network();

        let sender = Address([0x11; 20]);
        let target = Address([0x22; 20]);

        // Hop 6 -> ok
        let params6 = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: b"ping".to_vec(),
            asset_id: U256::zero(),
            value: U256::zero(),
            tip: U256::from(1u64),
            hop_count: 6,
            ordered: false,
        };
        assert!(relayer_engine.submit_outbound(101, sender, params6, Hash([0x66; 32])).is_ok());

        // Hop 7 -> rejected
        let params7 = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: b"loop".to_vec(),
            asset_id: U256::zero(),
            value: U256::zero(),
            tip: U256::from(1u64),
            hop_count: 7,
            ordered: false,
        };
        assert_eq!(
            relayer_engine.submit_outbound(101, sender, params7, Hash([0x77; 32])),
            Err(GatewayError::HopCountExceeded(7))
        );
    }

    #[test]
    fn test_scenario_10_6_onboard_new_chain_via_governance() {
        let active_chains: BTreeSet<u64> = [1000, 101, 102, 103].into_iter().collect();
        let mut gov = GovernanceEngine::with_timelock_delay(active_chains, 72 * 3600);

        let prop_id = gov.propose(GovernanceProposalKind::RegisterChain, b"chain_D".to_vec(), 1000).unwrap();
        gov.vote(prop_id, 1000, 1050).unwrap();
        gov.vote(prop_id, 101, 1060).unwrap();
        gov.vote(prop_id, 102, 1070).unwrap();

        // 72h timelock check
        assert!(gov.execute(prop_id, 1070 + 100).is_err());
        assert!(gov.execute(prop_id, 1070 + 72 * 3600 + 1).is_ok());
    }

    #[test]
    fn test_scenario_10_7_adversarial_overdraw_blocked() {
        let (mut relayer_engine, _) = setup_test_network();

        let attacker = Address([0x99; 20]);
        let target = Address([0x22; 20]);
        let tx_hash = Hash([0x88; 32]);

        // Chain 103 only has 500 allocation. Attacker attempts to withdraw 1,000,000
        let params = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: vec![],
            asset_id: U256::zero(),
            value: U256::from(1_000_000u64),
            tip: U256::from(10u64),
            hop_count: 1,
            ordered: false,
        };

        let msg = relayer_engine.submit_outbound(103, attacker, params, tx_hash).unwrap();
        let commit = relayer_engine.certify_commit(103, 1, vec![msg], vec![]).unwrap();

        // Reserve rejects overdraw
        let res = relayer_engine.relay_commit(103, commit.commit_root, Address([0x77; 20]));
        assert_eq!(
            res,
            Err(GatewayError::AllocationExceeded {
                chain_id: 103,
                requested: U256::from(1_000_000u64),
                available: U256::from(500u64),
            })
        );
    }

    #[test]
    fn test_scenario_10_8_dead_chain_recovery() {
        let (_, mut chains) = setup_test_network();
        let reserve = chains.get_mut(&1000).unwrap();
        let dead_chain_id = 103;

        reserve.dead_chains.insert(dead_chain_id);

        let victim = Address([0xDE; 20]);
        let victim_balance = U256::from(250u64);

        let leaf = AccountLeaf {
            account: victim,
            balance: victim_balance,
        };
        let leaf_bytes = serde_json::to_vec(&leaf).unwrap();
        let leaf_hash = keccak256(&leaf_bytes);

        let reg = reserve.chain_registry.get_mut(&dead_chain_id).unwrap();
        reg.state_root = leaf_hash;

        let proof = MerkleProof {
            leaf_index: 0,
            siblings: vec![],
        };

        assert!(reserve.claim_dead_chain_balance(dead_chain_id, victim, victim_balance, proof.clone(), leaf_hash).is_ok());
        // Double claim fails
        assert_eq!(
            reserve.claim_dead_chain_balance(dead_chain_id, victim, victim_balance, proof, leaf_hash),
            Err(GatewayError::DeadChainAlreadyClaimed {
                chain_id: dead_chain_id,
                account: victim,
            })
        );
    }
}
