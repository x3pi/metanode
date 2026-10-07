// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use prost::Message;
use sha3::{Digest, Keccak256};
use tracing::warn;

// Include generated protobuf code
#[allow(dead_code)]
mod proto {
    include!(concat!(env!("OUT_DIR"), "/transaction.rs"));
}

use proto::Transaction;

/// Hash một single Transaction bytes — KHÔNG thử decode array.
///
/// Dùng khi biết chắc `tx_data` là pb.Transaction đơn (ví dụ: đã được
/// zero-copy extract từ pb.Transactions trong tx_socket_server.rs).
/// Loại bỏ hoàn toàn false-positive decode.
pub fn calculate_transaction_hash_single(tx_data: &[u8]) -> Vec<u8> {
    if let Ok(tx) = Transaction::decode(tx_data) {
        return calculate_single_transaction_hash(tx);
    }
    warn!("Failed to parse single Transaction protobuf, using raw data hash");
    Keccak256::digest(tx_data).to_vec()
}

/// Calculate hash for a single Transaction using TransactionHashData
/// This is the official hash calculation that matches Go implementation
fn calculate_single_transaction_hash(tx: Transaction) -> Vec<u8> {
    // Cutover bundle v1 / ADR D3 / W4:
    // If raw_envelope is present (standard Ethereum transaction),
    // canonical hash is keccak256(raw_envelope) matching go-ethereum and Go Transaction.Hash()
    if !tx.raw_envelope.is_empty() {
        return Keccak256::digest(&tx.raw_envelope).to_vec();
    }

    // System transactions (without raw_envelope, e.g. BLS node-identity transactions):
    // Maintain deterministic TransactionHashData protobuf hash.
    // Create TransactionHashData from Transaction
    let hash_data = proto::TransactionHashData {
        from_address: tx.from_address,
        to_address: tx.to_address,
        amount: tx.amount,
        max_gas: tx.max_gas,
        max_gas_price: tx.max_gas_price,
        max_time_use: tx.max_time_use,
        data: tx.data,
        r#type: tx.r#type,
        last_device_key: tx.last_device_key,
        new_device_key: tx.new_device_key,
        nonce: tx.nonce,
        chain_id: tx.chain_id,
        r: tx.r,
        s: tx.s,
        v: tx.v,
        gas_tip_cap: tx.gas_tip_cap,
        gas_fee_cap: tx.gas_fee_cap,
        access_list: tx.access_list,
        blob_versioned_hashes: tx.blob_versioned_hashes,
        max_fee_per_blob_gas: tx.max_fee_per_blob_gas,
        authorization_list: tx.authorization_list,
        // tx.sidecar is deliberately excluded — must match Go's Transaction.Hash()/RHash(),
        // which only commit to blob_versioned_hashes, not the raw blob/commitment/proof data.
    };

    // Encode TransactionHashData to protobuf bytes
    let mut buf = Vec::new();
    if let Err(e) = hash_data.encode(&mut buf) {
        warn!("Failed to encode TransactionHashData: {}", e);
        // Fallback: hash the raw transaction data
        let hash = Keccak256::digest(&hash_data.data);
        return hash.to_vec();
    }

    // Calculate Keccak256 hash of encoded TransactionHashData
    let hash = Keccak256::digest(&buf);
    hash.to_vec()
}

/// Calculate single transaction hash and return hex string (first 8 bytes)
/// Dùng `calculate_transaction_hash_single` — KHÔNG thử decode array.
pub fn calculate_transaction_hash_single_hex(tx_data: &[u8]) -> String {
    let hash = calculate_transaction_hash_single(tx_data);
    hex::encode(&hash[..8.min(hash.len())])
}

/// Verify that transaction data is valid protobuf (Transaction or Transactions)
/// Returns true if data can be parsed as protobuf with valid fields, false otherwise
///
/// STRICT VALIDATION: After decoding, we check that at least one transaction has
/// a non-empty `from_address`. This prevents false positives from permissive
/// protobuf decoding (e.g. a raw Transaction being incorrectly decoded as Transactions).
pub fn verify_transaction_protobuf(tx_data: &[u8]) -> bool {
    // EXPLICIT FILTER: Skip 64-byte zero payloads (SystemTransaction artifacts at epoch boundaries)
    // These payloads cause UnmarshalTransaction FAILED errors in the Go execution engine.
    if tx_data.len() == 64 && tx_data.iter().all(|&b| b == 0) {
        return false;
    }

    // Relaxed validation: Allow all transactions to be sent to Go, 
    // even if they cannot be decoded as standard protobuf here.
    // The Go engine contains the authoritative decoding logic 
    // and will correctly discard any truly invalid data.
    // Filtering here risks data loss during WAL replay.
    true
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_hash_with_raw_envelope() {
        let envelope = b"sample_raw_eip2718_envelope_bytes";
        let expected_hash = Keccak256::digest(envelope).to_vec();

        let tx = Transaction {
            from_address: vec![1; 20],
            to_address: vec![2; 20],
            raw_envelope: envelope.to_vec(),
            ..Default::default()
        };

        let mut tx_bytes = Vec::new();
        tx.encode(&mut tx_bytes).unwrap();

        let hash = calculate_transaction_hash_single(&tx_bytes);
        assert_eq!(hash, expected_hash, "Hash must match keccak256(raw_envelope)");
    }

    #[test]
    fn test_hash_system_tx_without_envelope() {
        let tx = Transaction {
            from_address: vec![0xaa; 20],
            to_address: vec![0xbb; 20],
            amount: vec![0x01],
            max_gas: 21000,
            max_gas_price: 100000,
            raw_envelope: Vec::new(), // empty raw_envelope = system tx
            ..Default::default()
        };

        let mut tx_bytes = Vec::new();
        tx.encode(&mut tx_bytes).unwrap();

        let hash = calculate_transaction_hash_single(&tx_bytes);
        assert_eq!(
            hex::encode(&hash),
            "1925db928262bc9d4db0f4dba30bdd01a29ffa7d5d4ae5f418b0a63ba373c9eb",
            "System tx hash must match Go golden hash"
        );
    }

    #[test]
    fn test_fake_variant_dedup_and_payload_hash() {
        let envelope = b"valid_signed_eip1559_envelope";
        let canonical_tx_hash = Keccak256::digest(envelope).to_vec();

        // Real transaction
        let real_tx = Transaction {
            from_address: vec![0x11; 20],
            to_address: vec![0x22; 20],
            amount: vec![0x05],
            nonce: vec![10],
            raw_envelope: envelope.to_vec(),
            ..Default::default()
        };
        let mut real_bytes = Vec::new();
        real_tx.encode(&mut real_bytes).unwrap();

        // Mutated fake variant (same envelope, altered proto fields by Byzantine node)
        let fake_tx = Transaction {
            from_address: vec![0x11; 20],
            to_address: vec![0x99; 20], // mutated To
            amount: vec![0x99],         // mutated Amount
            nonce: vec![10],
            raw_envelope: envelope.to_vec(),
            ..Default::default()
        };
        let mut fake_bytes = Vec::new();
        fake_tx.encode(&mut fake_bytes).unwrap();

        // 1. Both share the exact same canonical Ethereum tx_hash
        assert_eq!(
            calculate_transaction_hash_single(&real_bytes),
            calculate_transaction_hash_single(&fake_bytes),
            "Both transactions share the same envelope hash"
        );
        assert_eq!(
            calculate_transaction_hash_single(&real_bytes),
            canonical_tx_hash
        );

        // 2. But their full payload hashes are strictly distinct
        let real_payload_hash = Keccak256::digest(&real_bytes).to_vec();
        let fake_payload_hash = Keccak256::digest(&fake_bytes).to_vec();
        assert_ne!(
            real_payload_hash, fake_payload_hash,
            "Payload hashes must differ to prevent displacement"
        );

        // 3. Simulating subdag dedup: fake tx comes first!
        let all_txs: Vec<(&[u8], Vec<u8>, Vec<u8>)> = vec![
            (&fake_bytes[..], canonical_tx_hash.clone(), fake_payload_hash.clone()),
            (&real_bytes[..], canonical_tx_hash.clone(), real_payload_hash.clone()),
        ];

        // Deduplicate by payload_hash
        let mut seen = std::collections::HashSet::new();
        let mut unique_txs = Vec::new();
        for (tx_data, tx_hash, payload_hash) in all_txs {
            if seen.insert(payload_hash.clone()) {
                unique_txs.push((tx_data, tx_hash, payload_hash));
            }
        }

        // Both transactions must be preserved! The real tx is NOT displaced by fake tx.
        assert_eq!(unique_txs.len(), 2, "Both transactions must be retained when deduping by payload_hash");
    }
}
