// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Shared types for the Peer RPC module.

use serde::{Deserialize, Serialize};

/// Response for /peer_info endpoint
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PeerInfoResponse {
    /// Node identifier
    pub node_id: usize,
    /// Current epoch number
    pub epoch: u64,
    /// Last executed block number
    pub last_block: u64,
    /// Last global execution index (Rust consensus index)
    #[serde(default)]
    pub last_global_exec_index: u64,
    /// Network address of this node
    pub network_address: String,
    /// Timestamp of response (Unix ms)
    pub timestamp_ms: u64,
    /// Account StateDB Merkle root hash
    #[serde(default)]
    pub state_root: String,
}

/// Request for /get_blocks endpoint
#[derive(Debug, Clone, Serialize, Deserialize)]
#[allow(dead_code)]
pub struct GetBlocksRequest {
    /// Start block number (inclusive)
    pub from_block: u64,
    /// End block number (inclusive)  
    pub to_block: u64,
}

/// Response for /get_blocks endpoint
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GetBlocksResponse {
    /// Node ID
    pub node_id: usize,
    /// Blocks data (block_number -> hex-encoded block data)
    pub blocks: std::collections::HashMap<u64, String>,
    /// Number of blocks returned
    pub count: usize,
    /// Error message if any
    pub error: Option<String>,
}

/// Simplified validator info for JSON transport
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ValidatorInfoSimple {
    pub name: String,
    pub address: String,
    pub stake: u64,
    pub protocol_key: Vec<u8>,
    pub network_key: Vec<u8>,
    pub authority_key: Vec<u8>,
    /// P2P address (Multiaddr format) — needed for committee building
    #[serde(default)]
    pub p2p_address: String,
}

/// Response for /get_epoch_boundary_data endpoint
/// This allows late-joining validators to fetch epoch boundary data from peers
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EpochBoundaryDataResponse {
    /// Target epoch number
    pub epoch: u64,
    /// Epoch start timestamp in milliseconds
    pub timestamp_ms: u64,
    /// Boundary block (last block of previous epoch)
    pub boundary_block: u64,
    /// Validators for this epoch
    pub validators: Vec<ValidatorInfoSimple>,
    /// Authoritative GEI for the epoch boundary
    #[serde(default)]
    pub boundary_gei: u64,
    /// Error message if any
    pub error: Option<String>,
}

/// Request for /submit_transaction endpoint (POST)
/// Used by SyncOnly nodes to forward transactions to validators, and by
/// validators to pre-propagate TX payloads to peers (cache_only mode).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SubmitTransactionRequest {
    /// Hex-encoded transactions data (batch)
    pub transactions_hex: Vec<String>,
    /// When true, the receiver only inserts the TX payloads into its
    /// compact-block (BlockV3) reconstruction cache and does NOT submit them
    /// to its local consensus proposer. Used for validator→validator
    /// pre-propagation: the origin validator already proposes these TXs
    /// itself; having every peer also propose them multiplied each TX into
    /// up to `committee_size` duplicate proposals, which the Go execution
    /// layer then had to reject one-by-one via nonce checks (measured: 262K
    /// rejects for 150K real TXs — the dominant E2E TPS bottleneck).
    /// Defaults to false so requests from older senders (and SyncOnly
    /// delegated submission, which MUST reach the proposer) keep the old
    /// submit behavior.
    #[serde(default)]
    pub cache_only: bool,
}

/// Response for /submit_transaction endpoint
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SubmitTransactionResponse {
    /// Whether the transaction was accepted
    pub success: bool,
    /// Number of transactions forwarded
    pub count: usize,
    /// Error message if any
    pub error: Option<String>,
}
