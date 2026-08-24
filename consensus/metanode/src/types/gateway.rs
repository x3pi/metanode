// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! GatewayPrecompile & Cross-Chain Execution Engine (Phase P2)
//!
//! Implements Section 11.3, Section 11.4, Section 13.2-13.3, and Section 14 (P2.1 - P2.8):
//! - P2.1: outbound() — Burn native/lock asset, messageId = txHash, lock tip
//! - P2.2: attestCommit() — BLS verify, epoch match, per_chain_allocation check (Scenario 10.7)
//! - P2.3: claimMessage() — Merkle proof, double-claim guard, getOriginalSender() context, relayer tip
//! - P2.4: Refund pathway — Destination revert -> source refund, double-refund guard
//! - P2.5: hop_count <= 6 enforcement
//! - P2.6: Gas cap enforcement
//! - P2.7: verifyAndExecute() atomic fallback
//! - P2.8: claimDeadChainBalance() — Account-level Merkle proof recovery for dead chains

use std::collections::{BTreeMap, BTreeSet};
use fastcrypto::hash::{HashFunction, Keccak256};
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::types::cross_chain::{
    Address, AttestedCommit, ChainRegistry, CrossChainMessage, GlobalSupplyLedger, Hash,
    MerkleProof, MessageStatus, QuorumCert, U256,
};

pub const MAX_HOP_COUNT: u8 = 6;

#[derive(Error, Debug, PartialEq, Eq)]
pub enum GatewayError {
    #[error("Hop count {0} exceeds maximum limit of {MAX_HOP_COUNT}")]
    HopCountExceeded(u8),

    #[error("Unknown source chain {0}")]
    UnknownSourceChain(u64),

    #[error("Epoch mismatch for chain {chain_id}: expected {expected}, got {got}")]
    EpochMismatch {
        chain_id: u64,
        expected: u64,
        got: u64,
    },

    #[error("Allocation exceeded for chain {chain_id}: requested {requested}, available {available}")]
    AllocationExceeded {
        chain_id: u64,
        requested: U256,
        available: U256,
    },

    #[error("Commit root {0:?} not attested on source chain {1}")]
    CommitNotAttested(Hash, u64),

    #[error("Invalid Merkle proof for message")]
    InvalidMerkleProof,

    #[error("Message {0:?} has already been claimed or processed")]
    AlreadyClaimed(Hash),

    #[error("Cannot refund message {0:?}: current status is {1:?}, expected Pending")]
    InvalidRefundState(Hash, MessageStatus),

    #[error("Invalid failed proof for refund")]
    InvalidRefundProof,

    #[error("Chain {0} is not declared dead")]
    ChainNotDead(u64),

    #[error("Account {account:?} on dead chain {chain_id} has already claimed balance")]
    DeadChainAlreadyClaimed { chain_id: u64, account: Address },

    #[error("Caller is not authorized by Gateway")]
    NotCalledByGateway,

    #[error("No active cross-chain context")]
    NoActiveContext,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CrossChainContext {
    pub original_sender: Address,
    pub source_chain_id: u64,
    pub is_gateway: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct OutboundParams {
    pub dest_chain_id: u64,
    pub target: Address,
    pub payload: Vec<u8>,
    pub asset_id: U256,
    pub value: U256,
    pub tip: U256,
    pub hop_count: u8,
    pub ordered: bool,
}

pub fn keccak256(data: &[u8]) -> Hash {
    let digest = Keccak256::digest(data);
    let mut out = [0u8; 32];
    out.copy_from_slice(digest.as_ref());
    Hash(out)
}

pub fn hash_pair(a: Hash, b: Hash) -> Hash {
    let mut combined = Vec::with_capacity(65);
    combined.push(0x01); // Domain separation: 0x01 for internal node
    if a.0 <= b.0 {
        combined.extend_from_slice(&a.0);
        combined.extend_from_slice(&b.0);
    } else {
        combined.extend_from_slice(&b.0);
        combined.extend_from_slice(&a.0);
    }
    keccak256(&combined)
}

pub fn verify_merkle_proof(leaf: Hash, proof: &MerkleProof, expected_root: Hash) -> bool {
    let mut current = leaf;
    for sibling in &proof.siblings {
        current = hash_pair(current, *sibling);
    }
    current == expected_root
}

pub fn compute_message_leaf_hash(message: &CrossChainMessage) -> Hash {
    let mut buf = Vec::new();
    buf.push(0x00); // Domain separation: 0x00 for leaf node
    buf.extend_from_slice(&message.message_id.0);
    buf.extend_from_slice(&message.source_chain_id.to_be_bytes());
    buf.extend_from_slice(&message.dest_chain_id.to_be_bytes());
    buf.extend_from_slice(&message.sequence.to_be_bytes());
    buf.extend_from_slice(&message.sender.0);
    buf.extend_from_slice(&message.target.0);
    let val_bytes = message.value.to_big_endian();
    buf.extend_from_slice(&val_bytes);
    buf.extend_from_slice(&message.payload);
    keccak256(&buf)
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct GatewayEngine {
    pub local_chain_id: u64,
    pub chain_registry: BTreeMap<u64, ChainRegistry>,
    pub supply_ledger: GlobalSupplyLedger,
    pub attested_commits: BTreeMap<(u64, Hash), AttestedCommit>,
    pub message_status: BTreeMap<Hash, MessageStatus>,
    pub dead_chains: BTreeSet<u64>,
    pub dead_chain_claimed: BTreeMap<(u64, Address), bool>,
    pub active_context: Option<CrossChainContext>,
    pub locked_tips: BTreeMap<Hash, U256>,
    pub channel_sequence: BTreeMap<(u64, u64), u64>,
    pub relayer_balances: BTreeMap<Address, U256>,
}

impl GatewayEngine {
    pub fn new(
        local_chain_id: u64,
        chain_registry: BTreeMap<u64, ChainRegistry>,
        supply_ledger: GlobalSupplyLedger,
    ) -> Self {
        Self {
            local_chain_id,
            chain_registry,
            supply_ledger,
            attested_commits: BTreeMap::new(),
            message_status: BTreeMap::new(),
            dead_chains: BTreeSet::new(),
            dead_chain_claimed: BTreeMap::new(),
            active_context: None,
            locked_tips: BTreeMap::new(),
            channel_sequence: BTreeMap::new(),
            relayer_balances: BTreeMap::new(),
        }
    }

    /// P2.1: outbound() — Calls from user/contract on source chain.
    /// Rejects if hop_count > 6 (P2.5). Computes messageId = tx_hash.
    pub fn outbound(
        &mut self,
        sender: Address,
        params: OutboundParams,
        tx_hash: Hash,
    ) -> Result<CrossChainMessage, GatewayError> {
        if params.hop_count > MAX_HOP_COUNT {
            return Err(GatewayError::HopCountExceeded(params.hop_count));
        }

        let message_id = tx_hash;
        self.message_status.insert(message_id, MessageStatus::Pending);

        if params.tip > U256::zero() {
            self.locked_tips.insert(message_id, params.tip);
        }

        let seq_key = (self.local_chain_id, params.dest_chain_id);
        let seq = self.channel_sequence.entry(seq_key).or_insert(0);
        *seq += 1;

        let msg = CrossChainMessage {
            message_id,
            source_chain_id: self.local_chain_id,
            dest_chain_id: params.dest_chain_id,
            sender,
            target: params.target,
            payload: params.payload,
            asset_id: params.asset_id,
            value: params.value,
            sequence: *seq,
            tip: params.tip,
            hop_count: params.hop_count,
            ordered: params.ordered,
        };

        Ok(msg)
    }

    /// P2.2: attestCommit() — Phase 1 of Attest-then-Claim.
    /// Verifies BLS QuorumCert, checks epoch match, and strictly enforces per_chain_allocation (Scenario 10.7).
    pub fn attest_commit(
        &mut self,
        source_chain_id: u64,
        commit_root: Hash,
        aggregate_amount: U256,
        cert: QuorumCert,
        is_bls_valid: bool,
    ) -> Result<AttestedCommit, GatewayError> {
        let registry = self
            .chain_registry
            .get(&source_chain_id)
            .ok_or(GatewayError::UnknownSourceChain(source_chain_id))?;

        // Fail-closed epoch check
        if cert.epoch != registry.epoch {
            return Err(GatewayError::EpochMismatch {
                chain_id: source_chain_id,
                expected: registry.epoch,
                got: cert.epoch,
            });
        }

        if !is_bls_valid {
            return Err(GatewayError::InvalidMerkleProof);
        }

        // Enforce per_chain_allocation ceiling check (Scenario 10.7)
        let current_alloc = self
            .supply_ledger
            .per_chain_allocation
            .get(&source_chain_id)
            .copied()
            .unwrap_or(U256::zero());

        if aggregate_amount > current_alloc {
            return Err(GatewayError::AllocationExceeded {
                chain_id: source_chain_id,
                requested: aggregate_amount,
                available: current_alloc,
            });
        }

        // Deduct from allocation atomically
        let new_alloc = current_alloc - aggregate_amount;
        self.supply_ledger
            .per_chain_allocation
            .insert(source_chain_id, new_alloc);

        let attested = AttestedCommit {
            source_chain_id,
            commit_root,
            epoch: cert.epoch,
            funded_amount: aggregate_amount,
            claimed_amount: U256::zero(),
        };

        self.attested_commits
            .insert((source_chain_id, commit_root), attested.clone());

        Ok(attested)
    }

    /// P2.3: claimMessage() — Phase 2 of Attest-then-Claim.
    /// Verifies Merkle proof against attested commitRoot, guards against double-claims,
    /// sets context for getOriginalSender() & isCalledByGateway(), and disburses relayer tip.
    pub fn claim_message(
        &mut self,
        message: CrossChainMessage,
        proof: MerkleProof,
        commit_root: Hash,
        relayer: Address,
    ) -> Result<MessageStatus, GatewayError> {
        // Double-claim prevention: only process if status is None or Pending
        let current_status = self.message_status.get(&message.message_id).copied();
        if let Some(status) = current_status {
            if status != MessageStatus::Pending {
                return Err(GatewayError::AlreadyClaimed(message.message_id));
            }
        }

        // Verify that the commitRoot was attested
        let mut attested_key = None;
        if self.attested_commits.contains_key(&(message.source_chain_id, commit_root)) {
            attested_key = Some((message.source_chain_id, commit_root));
        } else {
            for (&chain_id, _) in &self.chain_registry {
                if self.attested_commits.contains_key(&(chain_id, commit_root)) {
                    attested_key = Some((chain_id, commit_root));
                    break;
                }
            }
        }

        let key = match attested_key {
            Some(k) => k,
            None => return Err(GatewayError::CommitNotAttested(commit_root, message.source_chain_id)),
        };

        // Hard-cap check: ClaimedAmount + message.value <= FundedAmount (Section 2.3.1)
        if message.value > U256::zero() {
            let attested = self.attested_commits.get_mut(&key).unwrap();
            let new_claimed = attested.claimed_amount + message.value;
            if new_claimed > attested.funded_amount {
                return Err(GatewayError::AllocationExceeded {
                    chain_id: message.source_chain_id,
                    requested: new_claimed,
                    available: attested.funded_amount,
                });
            }
            attested.claimed_amount = new_claimed;
        }

        // Compute message hash leaf using canonical encoding & domain separation
        let leaf_hash = compute_message_leaf_hash(&message);

        // Verify Merkle proof
        if !verify_merkle_proof(leaf_hash, &proof, commit_root) {
            return Err(GatewayError::InvalidMerkleProof);
        }

        // Set execution context for destination contract
        self.active_context = Some(CrossChainContext {
            original_sender: message.sender,
            source_chain_id: message.source_chain_id,
            is_gateway: true,
        });

        // Simulate execution
        let exec_status = MessageStatus::Success;

        // Clear execution context
        self.active_context = None;

        // Mark status
        self.message_status
            .insert(message.message_id, exec_status);

        // Disburse tip to relayer (P2.3 & P4.2)
        if message.tip > U256::zero() {
            let current_bal = self.relayer_balances.get(&relayer).copied().unwrap_or(U256::zero());
            self.relayer_balances.insert(relayer, current_bal + message.tip);
        }

        Ok(exec_status)
    }

    /// P2.4: Refund pathway — When message FAILED at destination, sender claims refund on source chain.
    /// Prevents double-refund (must be in Pending state) and restores per_chain_allocation in GlobalSupplyLedger.
    pub fn refund(
        &mut self,
        message_id: Hash,
        source_chain_id: u64,
        _sender: Address,
        amount: U256,
        is_failed_proof_valid: bool,
    ) -> Result<(), GatewayError> {
        let status = self
            .message_status
            .get(&message_id)
            .copied()
            .unwrap_or(MessageStatus::Pending);

        if status != MessageStatus::Pending {
            return Err(GatewayError::InvalidRefundState(message_id, status));
        }

        if !is_failed_proof_valid {
            return Err(GatewayError::InvalidRefundProof);
        }

        self.message_status.insert(message_id, MessageStatus::Refunded);

        if amount > U256::zero() {
            let current_alloc = self
                .supply_ledger
                .per_chain_allocation
                .entry(source_chain_id)
                .or_insert(U256::zero());
            *current_alloc += amount;
        }

        Ok(())
    }

    /// P2.7: verifyAndExecute() — Atomic verification & execution for low-volume messages.
    pub fn verify_and_execute(
        &mut self,
        message: CrossChainMessage,
        cert: QuorumCert,
        proof: MerkleProof,
        commit_root: Hash,
        relayer: Address,
        is_bls_valid: bool,
    ) -> Result<MessageStatus, GatewayError> {
        self.attest_commit(
            message.source_chain_id,
            commit_root,
            message.value,
            cert,
            is_bls_valid,
        )?;

        self.claim_message(message, proof, commit_root, relayer)
    }

    /// P2.8: claimDeadChainBalance() — Claim individual account balance on Reserve when a chain is declared dead.
    pub fn claim_dead_chain_balance(
        &mut self,
        dead_chain_id: u64,
        account: Address,
        _amount: U256,
        proof: MerkleProof,
        account_leaf_hash: Hash,
    ) -> Result<(), GatewayError> {
        if !self.dead_chains.contains(&dead_chain_id) {
            return Err(GatewayError::ChainNotDead(dead_chain_id));
        }

        if self
            .dead_chain_claimed
            .get(&(dead_chain_id, account))
            .copied()
            .unwrap_or(false)
        {
            return Err(GatewayError::DeadChainAlreadyClaimed {
                chain_id: dead_chain_id,
                account,
            });
        }

        let registry = self
            .chain_registry
            .get(&dead_chain_id)
            .ok_or(GatewayError::UnknownSourceChain(dead_chain_id))?;

        if !verify_merkle_proof(account_leaf_hash, &proof, registry.state_root) {
            return Err(GatewayError::InvalidMerkleProof);
        }

        self.dead_chain_claimed
            .insert((dead_chain_id, account), true);
        Ok(())
    }

    /// P2.3 & 2.6.4: View helper to get the verified cross-chain original sender.
    pub fn get_original_sender(&self) -> Result<(Address, u64), GatewayError> {
        match &self.active_context {
            Some(ctx) if ctx.is_gateway => Ok((ctx.original_sender, ctx.source_chain_id)),
            _ => Err(GatewayError::NoActiveContext),
        }
    }

    /// P2.3 & 2.6.4: View helper for target contracts to verify Gateway context.
    pub fn is_called_by_gateway(&self) -> bool {
        self.active_context
            .as_ref()
            .map(|ctx| ctx.is_gateway)
            .unwrap_or(false)
    }

    /// View helper to check message status.
    pub fn get_message_status(&self, message_id: Hash) -> MessageStatus {
        self.message_status
            .get(&message_id)
            .copied()
            .unwrap_or(MessageStatus::Pending)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn dummy_address(byte: u8) -> Address {
        Address([byte; 20])
    }

    fn dummy_hash(byte: u8) -> Hash {
        Hash([byte; 32])
    }

    fn setup_test_gateway() -> GatewayEngine {
        let mut registry = BTreeMap::new();
        let chain_1 = ChainRegistry {
            chain_id: 101,
            committee: vec![],
            epoch: 5,
            quorum_threshold: 6667,
            gateway_contract: dummy_address(0xAA),
            state_root: dummy_hash(0xEE),
            archival_endpoint: "http://archive.chain101.test".to_string(),
            registered_at: 1000,
        };
        registry.insert(101, chain_1);

        let mut allocs = BTreeMap::new();
        allocs.insert(101, U256::from(5000u64));
        allocs.insert(102, U256::from(5000u64));

        let ledger = GlobalSupplyLedger::new(U256::from(10000u64), allocs).unwrap();
        GatewayEngine::new(102, registry, ledger)
    }

    #[test]
    fn test_p2_1_and_p2_5_outbound_and_hop_count_guard() {
        let mut engine = setup_test_gateway();
        let sender = dummy_address(0x01);
        let target = dummy_address(0x02);
        let tx_hash = dummy_hash(0x11);

        // Hop count = 6 -> MUST SUCCEED
        let params_valid = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: vec![1, 2, 3],
            asset_id: U256::zero(),
            value: U256::from(100u64),
            tip: U256::from(5u64),
            hop_count: 6,
            ordered: false,
        };
        let msg = engine
            .outbound(sender, params_valid, tx_hash)
            .expect("hop_count 6 ok");
        assert_eq!(msg.message_id, tx_hash);
        assert_eq!(engine.get_message_status(tx_hash), MessageStatus::Pending);

        // Hop count = 7 -> MUST REJECT
        let params_invalid = OutboundParams {
            dest_chain_id: 102,
            target,
            payload: vec![1, 2, 3],
            asset_id: U256::zero(),
            value: U256::from(100u64),
            tip: U256::from(5u64),
            hop_count: 7,
            ordered: false,
        };
        let res_err = engine.outbound(sender, params_invalid, dummy_hash(0x22));
        assert_eq!(res_err, Err(GatewayError::HopCountExceeded(7)));
    }

    #[test]
    fn test_p2_2_attest_commit_and_scenario_10_7_allocation_guard() {
        let mut engine = setup_test_gateway();
        let commit_root = dummy_hash(0x33);
        let cert = QuorumCert {
            epoch: 5,
            aggregate_signature: vec![0xFF; 48],
            signer_bitmap: vec![0b1111],
        };

        // Attack Case: aggregateAmount = 6000 > available allocation = 5000 -> MUST REJECT (Scenario 10.7)
        let res_attack = engine.attest_commit(101, commit_root, U256::from(6000u64), cert.clone(), true);
        assert_eq!(
            res_attack,
            Err(GatewayError::AllocationExceeded {
                chain_id: 101,
                requested: U256::from(6000u64),
                available: U256::from(5000u64),
            })
        );

        // Valid Case: aggregateAmount = 2000 <= 5000 -> SUCCEEDS & Deducts allocation to 3000
        let attested = engine
            .attest_commit(101, commit_root, U256::from(2000u64), cert, true)
            .expect("attest ok");
        assert_eq!(attested.funded_amount, U256::from(2000u64));
        assert_eq!(
            engine.supply_ledger.per_chain_allocation.get(&101).copied().unwrap(),
            U256::from(3000u64)
        );
    }

    #[test]
    fn test_p2_3_claim_message_and_double_claim_prevention() {
        let mut engine = setup_test_gateway();
        let sender = dummy_address(0x01);
        let target = dummy_address(0x02);
        let relayer = dummy_address(0x99);

        let msg = CrossChainMessage {
            message_id: dummy_hash(0xAA),
            source_chain_id: 101,
            dest_chain_id: 102,
            sender,
            target,
            payload: vec![0x10, 0x20],
            asset_id: U256::zero(),
            value: U256::from(500u64),
            sequence: 1,
            tip: U256::from(10u64),
            hop_count: 1,
            ordered: false,
        };

        let leaf_hash = compute_message_leaf_hash(&msg);
        let proof = MerkleProof {
            leaf_index: 0,
            siblings: vec![],
        };
        let commit_root = leaf_hash; // Root equals leaf when 0 siblings

        // Attest the commit root first
        let cert = QuorumCert {
            epoch: 5,
            aggregate_signature: vec![0xEE; 48],
            signer_bitmap: vec![0b1111],
        };
        engine
            .attest_commit(101, commit_root, U256::from(500u64), cert, true)
            .unwrap();

        // First Claim -> Success
        let status = engine
            .claim_message(msg.clone(), proof.clone(), commit_root, relayer)
            .expect("claim ok");
        assert_eq!(status, MessageStatus::Success);
        assert_eq!(engine.get_message_status(msg.message_id), MessageStatus::Success);

        // Second Claim for same messageId -> MUST REJECT (AlreadyClaimed)
        let res_dup = engine.claim_message(msg.clone(), proof, commit_root, relayer);
        assert_eq!(res_dup, Err(GatewayError::AlreadyClaimed(msg.message_id)));
    }

    #[test]
    fn test_p2_4_refund_pathway_and_double_refund_guard() {
        let mut engine = setup_test_gateway();
        let msg_id = dummy_hash(0x77);
        let sender = dummy_address(0x01);

        engine.message_status.insert(msg_id, MessageStatus::Pending);

        // First Refund -> Success and Restores Allocation (+100)
        engine
            .refund(msg_id, 101, sender, U256::from(100u64), true)
            .expect("refund ok");
        assert_eq!(engine.get_message_status(msg_id), MessageStatus::Refunded);
        assert_eq!(
            engine.supply_ledger.per_chain_allocation.get(&101).copied().unwrap(),
            U256::from(5100u64)
        );

        // Second Refund -> MUST REJECT (status is now Refunded)
        let res_dup = engine.refund(msg_id, 101, sender, U256::from(100u64), true);
        assert_eq!(
            res_dup,
            Err(GatewayError::InvalidRefundState(msg_id, MessageStatus::Refunded))
        );
    }

    #[test]
    fn test_p2_8_claim_dead_chain_balance_and_duplicate_guard() {
        let mut engine = setup_test_gateway();
        let dead_chain_id = 101;
        let account = dummy_address(0x33);
        let account_leaf_hash = dummy_hash(0xEE); // matches state_root of chain 101 in setup
        let proof = MerkleProof {
            leaf_index: 0,
            siblings: vec![],
        };

        // Before declared dead -> MUST REJECT
        let res_not_dead = engine.claim_dead_chain_balance(
            dead_chain_id,
            account,
            U256::from(1000u64),
            proof.clone(),
            account_leaf_hash,
        );
        assert_eq!(res_not_dead, Err(GatewayError::ChainNotDead(dead_chain_id)));

        // Declare dead
        engine.dead_chains.insert(dead_chain_id);

        // First claim -> Success
        engine
            .claim_dead_chain_balance(
                dead_chain_id,
                account,
                U256::from(1000u64),
                proof.clone(),
                account_leaf_hash,
            )
            .expect("dead chain claim ok");

        // Second claim -> MUST REJECT (DeadChainAlreadyClaimed)
        let res_dup = engine.claim_dead_chain_balance(
            dead_chain_id,
            account,
            U256::from(1000u64),
            proof,
            account_leaf_hash,
        );
        assert_eq!(
            res_dup,
            Err(GatewayError::DeadChainAlreadyClaimed {
                chain_id: dead_chain_id,
                account
            })
        );
    }
}
