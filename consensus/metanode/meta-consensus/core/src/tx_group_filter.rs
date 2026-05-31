// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use prost::Message;
use sha3::{Digest, Keccak256};
use std::collections::HashMap;

pub const MAX_TRANSACTION_GROUP_SIZE: usize = 2;

#[allow(dead_code)]
pub mod proto {
    include!(concat!(env!("OUT_DIR"), "/transaction.rs"));
}

use proto::Transaction as ProtoTx;

fn get_address_selector(signature: &str) -> Vec<u8> {
    let hash = Keccak256::digest(signature.as_bytes());
    let mut addr = vec![0u8; 20];
    addr[16..20].copy_from_slice(&hash[0..4]);
    addr
}

// Union-Find structure for grouping transactions
#[derive(Clone)]
struct UnionFind {
    parent: Vec<usize>,
}

impl UnionFind {
    fn new(n: usize) -> Self {
        Self {
            parent: (0..n).collect(),
        }
    }

    fn find(&mut self, i: usize) -> usize {
        let mut root = i;
        while self.parent[root] != root {
            root = self.parent[root];
        }
        let mut curr = i;
        while curr != root {
            let next = self.parent[curr];
            self.parent[curr] = root;
            curr = next;
        }
        root
    }

    fn union(&mut self, i: usize, j: usize) {
        let root_i = self.find(i);
        let root_j = self.find(j);
        if root_i != root_j {
            self.parent[root_i] = root_j;
        }
    }
}

pub fn get_group_addresses(tx_data: &[u8]) -> Vec<Vec<u8>> {
    let account_setting_addr = get_address_selector("account");
    let validator_contract_addr = vec![0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0x01];

    if let Ok(proto_tx) = ProtoTx::decode(tx_data) {
        let mut group_addrs = Vec::new();
        for addr in &proto_tx.related_addresses {
            if addr != &account_setting_addr && addr != &validator_contract_addr {
                group_addrs.push(addr.clone());
            }
        }
        if group_addrs.is_empty() {
            group_addrs.push(proto_tx.from_address.clone());
        }
        group_addrs
    } else {
        // Fallback for undecodable transactions
        let mut hasher = Keccak256::new();
        hasher.update(tx_data);
        vec![hasher.finalize().to_vec()]
    }
}

/// Verifies that no address-sharing group of transactions has more than `max_group_size` transactions.
/// A group is formed by transactions that share at least one address in `RelatedAddresses`.
pub fn verify_group_limit(txs: &[crate::block::Transaction], max_group_size: usize) -> bool {
    if txs.is_empty() {
        return true;
    }

    let n = txs.len();
    let mut uf = UnionFind::new(n);

    // Map address to list of transaction indices in this block
    let mut addr_to_indices: HashMap<Vec<u8>, Vec<usize>> = HashMap::new();
    for (i, tx) in txs.iter().enumerate() {
        let addrs = get_group_addresses(tx.data());
        for addr in addrs {
            addr_to_indices.entry(addr).or_default().push(i);
        }
    }

    // Union indices that share the same address
    for indices in addr_to_indices.values() {
        for i in 1..indices.len() {
            uf.union(indices[0], indices[i]);
        }
    }

    // Calculate component sizes
    let mut component_sizes = vec![0; n];
    for i in 0..n {
        let root = uf.find(i);
        component_sizes[root] += 1;
    }

    // If any group size exceeds the limit, verification fails
    for size in component_sizes {
        if size > max_group_size {
            return false;
        }
    }

    true
}

#[derive(Clone)]
pub struct IncrementalGroupVerifier {
    uf: UnionFind,
    addr_to_root: HashMap<Vec<u8>, usize>,
    component_sizes: Vec<usize>,
    max_group_size: usize,
    next_index: usize,
}

impl IncrementalGroupVerifier {
    pub fn new(max_group_size: usize, capacity: usize) -> Self {
        Self {
            uf: UnionFind::new(capacity),
            addr_to_root: HashMap::with_capacity(capacity),
            component_sizes: vec![1; capacity],
            max_group_size,
            next_index: 0,
        }
    }

    /// Adds a transaction and returns false if it violates the group limit.
    /// Note: If this returns false, the internal state might be partially updated.
    pub fn add_tx(&mut self, tx: &crate::block::Transaction) -> bool {
        let i = self.next_index;
        self.next_index += 1;
        
        // Ensure uf and component_sizes have enough capacity (should not happen if capacity is set correctly)
        if i >= self.uf.parent.len() {
            self.uf.parent.push(i);
            self.component_sizes.push(1);
        } else {
            self.component_sizes[i] = 1;
        }

        let addrs = get_group_addresses(tx.data());
        
        for addr in addrs {
            if let Some(&existing_root) = self.addr_to_root.get(&addr) {
                let root_i = self.uf.find(i);
                let root_j = self.uf.find(existing_root);
                
                if root_i != root_j {
                    let new_size = self.component_sizes[root_i] + self.component_sizes[root_j];
                    if new_size > self.max_group_size {
                        return false;
                    }
                    self.uf.parent[root_i] = root_j;
                    self.component_sizes[root_j] = new_size;
                }
            } else {
                self.addr_to_root.insert(addr, i);
            }
        }
        
        true
    }
}
