// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Cross-Chain Root Anchor & Execution Types (Task P0.1)
//!
//! Implements canonical Rust data structures for Root Anchor state, cross-chain messaging,
//! light-client verification, and global supply invariant management.

use std::collections::{BTreeMap, BTreeSet};
use std::fmt;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use thiserror::Error;

uint::construct_uint! {
    /// 256-bit unsigned integer type for balances, allocations, and asset IDs.
    #[derive(Serialize, Deserialize)]
    pub struct U256(4);
}

/// 20-byte Ethereum-compatible account address
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Default)]
pub struct Address(pub [u8; 20]);

impl Address {
    pub const ZERO: Address = Address([0u8; 20]);

    pub fn from_slice(slice: &[u8]) -> Result<Self, &'static str> {
        if slice.len() != 20 {
            return Err("Address must be 20 bytes");
        }
        let mut bytes = [0u8; 20];
        bytes.copy_from_slice(slice);
        Ok(Address(bytes))
    }

    pub fn as_bytes(&self) -> &[u8; 20] {
        &self.0
    }

    pub fn to_hex(&self) -> String {
        format!("0x{}", hex::encode(self.0))
    }

    pub fn from_hex(s: &str) -> Result<Self, hex::FromHexError> {
        let cleaned = s.strip_prefix("0x").unwrap_or(s);
        let mut bytes = [0u8; 20];
        hex::decode_to_slice(cleaned, &mut bytes)?;
        Ok(Address(bytes))
    }
}

impl fmt::Display for Address {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_hex())
    }
}

impl fmt::Debug for Address {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Address({})", self.to_hex())
    }
}

impl Serialize for Address {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        if serializer.is_human_readable() {
            serializer.serialize_str(&self.to_hex())
        } else {
            serializer.serialize_bytes(&self.0)
        }
    }
}

impl<'de> Deserialize<'de> for Address {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        if deserializer.is_human_readable() {
            let s = String::deserialize(deserializer)?;
            Address::from_hex(&s).map_err(serde::de::Error::custom)
        } else {
            let bytes = <[u8; 20]>::deserialize(deserializer)?;
            Ok(Address(bytes))
        }
    }
}

/// 32-byte Cryptographic Hash (Keccak256 / SHA256 / Commit Root / Tx Hash)
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Default)]
pub struct Hash(pub [u8; 32]);

impl Hash {
    pub const ZERO: Hash = Hash([0u8; 32]);

    pub fn from_slice(slice: &[u8]) -> Result<Self, &'static str> {
        if slice.len() != 32 {
            return Err("Hash must be 32 bytes");
        }
        let mut bytes = [0u8; 32];
        bytes.copy_from_slice(slice);
        Ok(Hash(bytes))
    }

    pub fn as_bytes(&self) -> &[u8; 32] {
        &self.0
    }

    pub fn to_hex(&self) -> String {
        format!("0x{}", hex::encode(self.0))
    }

    pub fn from_hex(s: &str) -> Result<Self, hex::FromHexError> {
        let cleaned = s.strip_prefix("0x").unwrap_or(s);
        let mut bytes = [0u8; 32];
        hex::decode_to_slice(cleaned, &mut bytes)?;
        Ok(Hash(bytes))
    }
}

impl fmt::Display for Hash {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_hex())
    }
}

impl fmt::Debug for Hash {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Hash({})", self.to_hex())
    }
}

impl Serialize for Hash {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        if serializer.is_human_readable() {
            serializer.serialize_str(&self.to_hex())
        } else {
            serializer.serialize_bytes(&self.0)
        }
    }
}

impl<'de> Deserialize<'de> for Hash {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        if deserializer.is_human_readable() {
            let s = String::deserialize(deserializer)?;
            Hash::from_hex(&s).map_err(serde::de::Error::custom)
        } else {
            let bytes = <[u8; 32]>::deserialize(deserializer)?;
            Ok(Hash(bytes))
        }
    }
}

/// Validator committee member with BLS public key and Proof-of-Possession signature (Section 11.6 & 1.3 #4).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ValidatorEntry {
    pub pubkey_bls: Vec<u8>,
    pub stake: u64,
    pub pop_signature: Vec<u8>,
}

/// Metadata and committee snapshot for a registered private chain (Section 2.1).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ChainRegistry {
    pub chain_id: u64,
    pub committee: Vec<ValidatorEntry>,
    pub epoch: u64,
    pub quorum_threshold: u64,
    pub gateway_contract: Address,
    pub state_root: Hash,
    pub archival_endpoint: String,
    pub registered_at: u64,
}

#[derive(Error, Debug, PartialEq, Eq)]
pub enum SupplyLedgerError {
    #[error("Sum of per-chain allocations ({actual}) does not match genesis total supply ({expected})")]
    InvariantViolation { expected: U256, actual: U256 },

    #[error("Source chain {chain_id} has insufficient allocation: available {available}, requested {requested}")]
    InsufficientAllocation {
        chain_id: u64,
        available: U256,
        requested: U256,
    },

    #[error("Arithmetic overflow during allocation update")]
    ArithmeticOverflow,

    #[error("Source and destination chain IDs must be distinct")]
    SameChainTransfer,
}

/// Global Supply Ledger maintained exclusively on the Root Anchor / Reserve Chain (Section 2.1 & 2.3).
/// Enforces the active custodial ceiling: sum(per_chain_allocation) == genesis_total_supply.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct GlobalSupplyLedger {
    pub genesis_total_supply: U256,
    pub per_chain_allocation: BTreeMap<u64, U256>,
}

impl GlobalSupplyLedger {
    /// Creates a new `GlobalSupplyLedger` while strictly enforcing the initial invariant.
    pub fn new(
        genesis_total_supply: U256,
        per_chain_allocation: BTreeMap<u64, U256>,
    ) -> Result<Self, SupplyLedgerError> {
        let ledger = Self {
            genesis_total_supply,
            per_chain_allocation,
        };

        if !ledger.verify_invariant() {
            let actual = ledger.sum_allocations();
            return Err(SupplyLedgerError::InvariantViolation {
                expected: genesis_total_supply,
                actual,
            });
        }

        Ok(ledger)
    }

    /// Sum of all per-chain allocations.
    pub fn sum_allocations(&self) -> U256 {
        self.per_chain_allocation
            .values()
            .copied()
            .fold(U256::zero(), |acc, v| acc.saturating_add(v))
    }

    /// Verifies the Zero-Fork total supply invariant: Σ per_chain_allocation == genesis_total_supply.
    pub fn verify_invariant(&self) -> bool {
        let mut total = U256::zero();
        for &amount in self.per_chain_allocation.values() {
            let (next_total, overflow) = total.overflowing_add(amount);
            if overflow {
                return false;
            }
            total = next_total;
        }
        total == self.genesis_total_supply
    }

    /// Returns the current active ceiling for a given chain ID (returns 0 if not allocated).
    pub fn get_allocation(&self, chain_id: u64) -> U256 {
        self.per_chain_allocation
            .get(&chain_id)
            .copied()
            .unwrap_or_else(U256::zero)
    }

    /// Safely sets an initial allocation for a newly onboarded chain from an unallocated reserve balance.
    pub fn set_initial_allocation(
        &mut self,
        reserve_chain_id: u64,
        new_chain_id: u64,
        amount: U256,
    ) -> Result<(), SupplyLedgerError> {
        self.transfer_allocation(reserve_chain_id, new_chain_id, amount)
    }

    /// Transfers allocation from `from_chain` to `to_chain`, actively enforcing the custodial ceiling.
    pub fn transfer_allocation(
        &mut self,
        from_chain: u64,
        to_chain: u64,
        amount: U256,
    ) -> Result<(), SupplyLedgerError> {
        if from_chain == to_chain {
            return Err(SupplyLedgerError::SameChainTransfer);
        }

        let from_current = self.get_allocation(from_chain);
        if from_current < amount {
            return Err(SupplyLedgerError::InsufficientAllocation {
                chain_id: from_chain,
                available: from_current,
                requested: amount,
            });
        }

        let to_current = self.get_allocation(to_chain);
        let (new_to, overflow) = to_current.overflowing_add(amount);
        if overflow {
            return Err(SupplyLedgerError::ArithmeticOverflow);
        }

        let new_from = from_current - amount;

        // Apply mutations
        self.per_chain_allocation.insert(from_chain, new_from);
        self.per_chain_allocation.insert(to_chain, new_to);

        // Sanity invariant assertion
        debug_assert!(self.verify_invariant(), "Invariant must hold after valid transfer");

        Ok(())
    }
}

/// Canonical Cross-Chain Envelope (Section 11.2)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CrossChainMessage {
    pub message_id: Hash,
    pub source_chain_id: u64,
    pub dest_chain_id: u64,
    pub sequence: u64,
    pub hop_count: u8,
    pub sender: Address,
    pub target: Address,
    pub asset_id: U256,
    pub value: U256,
    pub payload: Vec<u8>,
    pub tip: U256,
    pub ordered: bool,
}

/// Quorum Certificate issued by source chain's Mysticeti DAG-BFT committee (Section 11.2)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct QuorumCert {
    pub epoch: u64,
    pub aggregate_signature: Vec<u8>,
    pub signer_bitmap: Vec<u8>,
}

/// Merkle Inclusion Proof for a message in a certified commit root (Section 11.2)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct MerkleProof {
    pub leaf_index: u64,
    pub siblings: Vec<Hash>,
}

/// Asset registry entry on Root Anchor for cross-chain token mapping (Section 11.6 & 2.5)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct AssetEntry {
    pub asset_id: U256,
    pub home_chain_id: u64,
    pub canonical_contract: Address,
    pub wrapped_contracts: BTreeMap<u64, Address>,
    pub active: bool,
}

/// Status of a cross-chain message on the channel state (Section 11.6)
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[repr(u8)]
pub enum MessageStatus {
    Pending = 0,
    Success = 1,
    Failed = 2,
    Refunded = 3,
}

/// Channel tracking ordered / unordered state between chain pairs (Section 11.6)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Channel {
    pub source_chain_id: u64,
    pub dest_chain_id: u64,
    pub ordered: bool,
    pub next_sequence: u64,
    pub last_processed_sequence: u64,
    pub status_by_message_id: BTreeMap<Hash, MessageStatus>,
}

/// Attested Commit state produced during Phase 1 of Attest-then-Claim (Section 11.6 & 13.3)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct AttestedCommit {
    pub source_chain_id: u64,
    pub commit_root: Hash,
    pub epoch: u64,
    pub funded_amount: U256,
    pub claimed_amount: U256,
}

/// Governance proposal kinds on Root Anchor (Section 11.6 & 1.3 #3)
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[repr(u8)]
pub enum GovernanceProposalKind {
    RegisterChain = 0,
    UnregisterChain = 1,
    RegisterAsset = 2,
    UpdateCommittee = 3,
    DeclareChainDead = 4,
}

/// On-chain governance proposal voted by registered active chains (Section 11.6 & 1.3 #3)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct GovernanceProposal {
    pub proposal_id: Hash,
    pub kind: GovernanceProposalKind,
    pub payload: Vec<u8>,
    pub votes_for: u64,
    pub voted_chains: BTreeSet<u64>,
    pub proposed_at: u64,
    pub effective_at: u64,
    pub executed: bool,
}

/// Merkle leaf representing account balance for Chain-Death Recovery (Section 11.6 & 5.2.2)
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct AccountLeaf {
    pub account: Address,
    pub balance: U256,
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::Rng;

    #[test]
    fn test_address_hash_serde_and_hex() {
        let addr_hex = "0x1122334455667788990011223344556677889900";
        let addr = Address::from_hex(addr_hex).expect("valid hex address");
        assert_eq!(addr.to_hex(), addr_hex);

        let json = serde_json::to_string(&addr).expect("serialize address json");
        assert_eq!(json, format!("\"{}\"", addr_hex));
        let deserialized: Address = serde_json::from_str(&json).expect("deserialize address json");
        assert_eq!(deserialized, addr);

        let hash_hex = "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789";
        let hash = Hash::from_hex(hash_hex).expect("valid hex hash");
        assert_eq!(hash.to_hex(), hash_hex);

        let hash_json = serde_json::to_string(&hash).expect("serialize hash json");
        let hash_deser: Hash = serde_json::from_str(&hash_json).expect("deserialize hash json");
        assert_eq!(hash_deser, hash);
    }

    #[test]
    fn test_all_structs_roundtrip_serde() {
        let addr = Address::from_hex("0x1111111111111111111111111111111111111111").unwrap();
        let hash = Hash::from_hex("0x2222222222222222222222222222222222222222222222222222222222222222").unwrap();

        // 1. ValidatorEntry
        let val = ValidatorEntry {
            pubkey_bls: vec![1, 2, 3, 4],
            stake: 1000,
            pop_signature: vec![5, 6, 7, 8],
        };
        let val_json = serde_json::to_string(&val).unwrap();
        let val_deser: ValidatorEntry = serde_json::from_str(&val_json).unwrap();
        assert_eq!(val, val_deser);

        // 2. ChainRegistry
        let reg = ChainRegistry {
            chain_id: 101,
            committee: vec![val.clone()],
            epoch: 5,
            quorum_threshold: 667,
            gateway_contract: addr,
            state_root: hash,
            archival_endpoint: "https://archive.metanode.network".to_string(),
            registered_at: 1700000000,
        };
        let reg_json = serde_json::to_string(&reg).unwrap();
        let reg_deser: ChainRegistry = serde_json::from_str(&reg_json).unwrap();
        assert_eq!(reg, reg_deser);

        // 3. CrossChainMessage
        let msg = CrossChainMessage {
            message_id: hash,
            source_chain_id: 101,
            dest_chain_id: 102,
            sequence: 42,
            hop_count: 1,
            sender: addr,
            target: addr,
            asset_id: U256::zero(),
            value: U256::from(1_000_000u64),
            payload: vec![0xaa, 0xbb, 0xcc],
            tip: U256::from(100u64),
            ordered: false,
        };
        let msg_json = serde_json::to_string(&msg).unwrap();
        let msg_deser: CrossChainMessage = serde_json::from_str(&msg_json).unwrap();
        assert_eq!(msg, msg_deser);

        // 4. QuorumCert
        let cert = QuorumCert {
            epoch: 5,
            aggregate_signature: vec![0x12, 0x34],
            signer_bitmap: vec![0xff, 0x01],
        };
        let cert_json = serde_json::to_string(&cert).unwrap();
        let cert_deser: QuorumCert = serde_json::from_str(&cert_json).unwrap();
        assert_eq!(cert, cert_deser);

        // 5. MerkleProof
        let proof = MerkleProof {
            leaf_index: 3,
            siblings: vec![hash, hash],
        };
        let proof_json = serde_json::to_string(&proof).unwrap();
        let proof_deser: MerkleProof = serde_json::from_str(&proof_json).unwrap();
        assert_eq!(proof, proof_deser);

        // 6. AssetEntry
        let mut wrapped = BTreeMap::new();
        wrapped.insert(102, addr);
        let asset = AssetEntry {
            asset_id: U256::from(1u64),
            home_chain_id: 101,
            canonical_contract: addr,
            wrapped_contracts: wrapped,
            active: true,
        };
        let asset_json = serde_json::to_string(&asset).unwrap();
        let asset_deser: AssetEntry = serde_json::from_str(&asset_json).unwrap();
        assert_eq!(asset, asset_deser);

        // 7. Channel
        let mut status_map = BTreeMap::new();
        status_map.insert(hash, MessageStatus::Success);
        let channel = Channel {
            source_chain_id: 101,
            dest_chain_id: 102,
            ordered: true,
            next_sequence: 10,
            last_processed_sequence: 9,
            status_by_message_id: status_map,
        };
        let channel_json = serde_json::to_string(&channel).unwrap();
        let channel_deser: Channel = serde_json::from_str(&channel_json).unwrap();
        assert_eq!(channel, channel_deser);

        // 8. AttestedCommit
        let commit = AttestedCommit {
            source_chain_id: 101,
            commit_root: hash,
            epoch: 5,
            funded_amount: U256::from(5000u64),
            claimed_amount: U256::from(2000u64),
        };
        let commit_json = serde_json::to_string(&commit).unwrap();
        let commit_deser: AttestedCommit = serde_json::from_str(&commit_json).unwrap();
        assert_eq!(commit, commit_deser);

        // 9. GovernanceProposal
        let mut voters = BTreeSet::new();
        voters.insert(101);
        voters.insert(102);
        let proposal = GovernanceProposal {
            proposal_id: hash,
            kind: GovernanceProposalKind::RegisterChain,
            payload: vec![1, 2, 3],
            votes_for: 2,
            voted_chains: voters,
            proposed_at: 1700000000,
            effective_at: 1700000000 + 72 * 3600,
            executed: false,
        };
        let prop_json = serde_json::to_string(&proposal).unwrap();
        let prop_deser: GovernanceProposal = serde_json::from_str(&prop_json).unwrap();
        assert_eq!(proposal, prop_deser);

        // 10. AccountLeaf
        let leaf = AccountLeaf {
            account: addr,
            balance: U256::from(100_000u64),
        };
        let leaf_json = serde_json::to_string(&leaf).unwrap();
        let leaf_deser: AccountLeaf = serde_json::from_str(&leaf_json).unwrap();
        assert_eq!(leaf, leaf_deser);
    }

    #[test]
    fn test_global_supply_ledger_basic_operations() {
        let total_supply = U256::from(1_000_000_000u64);
        let mut allocations = BTreeMap::new();
        allocations.insert(0, U256::from(700_000_000u64)); // Reserve Chain
        allocations.insert(1, U256::from(200_000_000u64)); // Chain 1
        allocations.insert(2, U256::from(100_000_000u64)); // Chain 2

        let mut ledger = GlobalSupplyLedger::new(total_supply, allocations).unwrap();
        assert!(ledger.verify_invariant());

        // Valid transfer from 1 to 2
        ledger.transfer_allocation(1, 2, U256::from(50_000_000u64)).unwrap();
        assert_eq!(ledger.get_allocation(1), U256::from(150_000_000u64));
        assert_eq!(ledger.get_allocation(2), U256::from(150_000_000u64));
        assert!(ledger.verify_invariant());

        // Over-allocation attempt: try to transfer 200M from chain 1 (which only has 150M)
        let err = ledger.transfer_allocation(1, 2, U256::from(200_000_000u64)).unwrap_err();
        assert_eq!(
            err,
            SupplyLedgerError::InsufficientAllocation {
                chain_id: 1,
                available: U256::from(150_000_000u64),
                requested: U256::from(200_000_000u64)
            }
        );
        assert!(ledger.verify_invariant());

        // Same chain transfer
        assert_eq!(
            ledger.transfer_allocation(1, 1, U256::from(1000u64)).unwrap_err(),
            SupplyLedgerError::SameChainTransfer
        );
    }

    #[test]
    fn test_global_supply_ledger_fuzz_property_based_invariant() {
        let mut rng = rand::thread_rng();

        let initial_supply = U256::from(10_000_000_000u64);
        let num_chains = 5u64;

        let mut initial_allocs = BTreeMap::new();
        // Initial distribution: 6B on chain 0, 1B on each of 1, 2, 3, 4
        initial_allocs.insert(0, U256::from(6_000_000_000u64));
        for i in 1..num_chains {
            initial_allocs.insert(i, U256::from(1_000_000_000u64));
        }

        let mut ledger = GlobalSupplyLedger::new(initial_supply, initial_allocs).unwrap();
        assert!(ledger.verify_invariant());

        // Run 10,000 randomized operations
        for _ in 0..10_000 {
            let from_chain = rng.gen_range(0..num_chains);
            let to_chain = rng.gen_range(0..num_chains);

            let current_from_alloc = ledger.get_allocation(from_chain);

            // Generate a random transfer amount: 50% within available balance, 50% potentially exceeding
            let transfer_amount = if rng.gen_bool(0.7) && current_from_alloc > U256::zero() {
                // Pick an amount between 0 and current_from_alloc
                let max_u64 = current_from_alloc.low_u64();
                if max_u64 > 0 {
                    U256::from(rng.gen_range(1..=max_u64))
                } else {
                    U256::from(1u64)
                }
            } else {
                // Potential over-allocation attempt
                current_from_alloc.saturating_add(U256::from(rng.gen_range(1..100_000u64)))
            };

            let res = ledger.transfer_allocation(from_chain, to_chain, transfer_amount);

            if from_chain == to_chain {
                assert!(res.is_err());
            } else if transfer_amount > current_from_alloc {
                assert!(res.is_err());
            } else {
                assert!(res.is_ok());
            }

            // CRITICAL INVARIANT: Total supply MUST remain identical under all outcomes
            assert!(
                ledger.verify_invariant(),
                "CRITICAL: Supply ledger invariant violated during fuzzing!"
            );
            assert_eq!(ledger.sum_allocations(), initial_supply);
        }
    }
}
