// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Unified Committee Source Module
//!
//! This module provides a fork-safe way to fetch committee information
//! that works consistently across both SyncOnly and Validator modes.
//!
//! ## Fork Prevention Principles
//!
//! 1. Always use Go Master with highest epoch (network consensus)
//! 2. Always use `get_epoch_start_timestamp()` for consistent genesis hash
//! 3. Committee and timestamp must come from the SAME source

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use anyhow::Result;
use consensus_config::Committee;
use sha3::{Digest, Keccak256};
use std::sync::Arc;
use tracing::{debug, info, warn};

/// Calculate a deterministic hash of the committee for verification/debugging
/// This hash can be compared across nodes to detect committee mismatches
pub fn calculate_committee_hash(committee: &Committee) -> [u8; 32] {
    let mut hasher = Keccak256::new();

    // Hash committee size first
    hasher.update((committee.size() as u64).to_le_bytes());

    // Collect authorities in index order (deterministic)
    for i in 0..committee.size() {
        let idx = consensus_config::AuthorityIndex::new_for_test(i as u32);
        let authority = committee.authority(idx);

        // Hash public key
        hasher.update(authority.protocol_key.to_bytes());
        // Hash stake
        hasher.update(authority.stake.to_le_bytes());
        // Hash hostname (network identity)
        hasher.update(authority.hostname.as_bytes());
    }

    hasher.finalize().into()
}

/// Unified committee source for both SyncOnly and Validator modes
/// Ensures fork-safe committee fetching by always using the best available source
#[derive(Debug, Clone)]
pub struct CommitteeSource {
    /// Best Go Master socket (either local or peer)
    pub socket_path: String,
    /// Epoch from the best source
    pub epoch: u64,
    /// Last committed block from best source
    pub last_block: u64,
    /// Whether this source is from a peer (not local)
    pub is_peer: bool,
    /// Peer RPC addresses for fallback when local is behind
    #[allow(dead_code)]
    pub peer_rpc_addresses: Vec<String>,
}

impl CommitteeSource {
    /// Discover the best committee source
    /// Priority: Peer with highest epoch > Local Go Master
    ///
    /// This ensures all nodes use the same committee source, preventing fork.
    pub async fn discover(config: &NodeConfig) -> Result<Self> {
        info!("🔍 [COMMITTEE SOURCE] Discovering best committee source...");

        // First, check local Go Master
        let local_client = ExecutorClient::new(
            true,
            false,
            config.executor_send_socket_path.clone(),
            config.executor_receive_socket_path.clone(),
            None,
        );

        let local_epoch = local_client.get_current_epoch().await.unwrap_or(0);
        let local_block = local_client
            .get_last_block_number()
            .await
            .map(|(b, _, _, _, _)| b)
            .unwrap_or(0);

        info!(
            "📊 [COMMITTEE SOURCE] Local Go Master: epoch={}, block={}",
            local_epoch, local_block
        );

        // If no TCP peer addresses configured, use local
        if config.peer_rpc_addresses.is_empty() {
            info!("ℹ️ [COMMITTEE SOURCE] No peer RPC addresses configured, using local Go Master");
            return Ok(Self {
                socket_path: config.executor_receive_socket_path.clone(),
                epoch: local_epoch,
                last_block: local_block,
                is_peer: false,
                peer_rpc_addresses: Vec::new(),
            });
        }

        // Query TCP peers to find the best source
        let mut best_epoch = local_epoch;
        let mut best_block = local_block;
        let best_socket = config.executor_receive_socket_path.clone();
        let mut is_peer = false;

        // Use TCP RPC to query peer nodes over network
        for peer_address in &config.peer_rpc_addresses {
            match crate::network::peer_rpc::query_peer_info(peer_address).await {
                Ok(peer_info) => {
                    debug!(
                        "📊 [COMMITTEE SOURCE] TCP Peer {}: epoch={}, block={}, timestamp={}",
                        peer_address, peer_info.epoch, peer_info.last_block, peer_info.timestamp_ms
                    );

                    // Use peer if:
                    // 1. Higher epoch (network has advanced)
                    // 2. Same epoch but higher block (more up-to-date)
                    if peer_info.epoch > best_epoch
                        || (peer_info.epoch == best_epoch && peer_info.last_block > best_block)
                    {
                        best_epoch = peer_info.epoch;
                        best_block = peer_info.last_block;
                        // For TCP peers, we still use local socket for actual data read
                        // The peer info just tells us who is ahead
                        // best_socket stays as local since we can't RPC read blocks over TCP (yet)
                        is_peer = true;

                        info!(
                            "✅ [COMMITTEE SOURCE] Found ahead peer: {} (epoch={}, block={}). Using local Go Master for data.",
                            peer_address, peer_info.epoch, peer_info.last_block
                        );
                    }
                }
                Err(e) => {
                    warn!(
                        "⚠️ [COMMITTEE SOURCE] Failed to query TCP peer {}: {}",
                        peer_address, e
                    );
                }
            }
        }

        info!(
            "✅ [COMMITTEE SOURCE] Selected source: {} (epoch={}, block={}, is_peer={})",
            best_socket, best_epoch, best_block, is_peer
        );

        Ok(Self {
            socket_path: best_socket.clone(),
            epoch: best_epoch,
            last_block: best_block,
            is_peer,
            peer_rpc_addresses: config.peer_rpc_addresses.clone(),
        })
    }

    /// Create an executor client connected to this source
    pub fn create_executor_client(&self, send_socket: &str) -> Arc<ExecutorClient> {
        Arc::new(ExecutorClient::new(
            true,
            false,
            send_socket.to_string(),
            self.socket_path.clone(),
            None,
        ))
    }

    /// Fetch committee from this source using EPOCH BOUNDARY DATA
    /// This ensures validators are fetched from the boundary block (last block of prev epoch)
    /// for consistent committee across all nodes
    ///
    /// NOTE: target_epoch is the epoch we're transitioning TO, not the current epoch.
    /// This is critical because during epoch transition, the Go Master may still report
    /// the old epoch while we need the new epoch's committee.
    ///
    /// CRITICAL FIX: Single Source of Truth for Committee
    ///
    /// To prevent forks, this function ONLY uses LOCAL Go as the data source.
    /// It will wait INDEFINITELY until local Go has synced to the boundary block
    /// and can provide validators for the target epoch.
    ///
    /// The sync process (rust_sync_node) must complete BEFORE this returns.
    /// This ensures all nodes derive committee from the same verified blockchain state.
    ///
    /// SIMPLIFIED: Go now returns data even when boundary block not fully synced.
    pub async fn fetch_committee(
        &self,
        send_socket: &str,
        target_epoch: u64,
    ) -> Result<(Committee, Vec<Vec<u8>>)> {
        let client = self.create_executor_client(send_socket);

        info!(
            "📋 [COMMITTEE SOURCE] Fetching committee for epoch {} from local Go",
            target_epoch
        );

        // Configuration: reasonable timeout since Go now handles unsynced state
        const MAX_ATTEMPTS: u32 = 60; // ~30 seconds
        const DELAY_MS: u64 = 500;

        for attempt in 1..=MAX_ATTEMPTS {
            match client.get_epoch_boundary_data(target_epoch).await {
                Ok((epoch, timestamp_ms, boundary_block, validators, _, _)) => {
                    if epoch == target_epoch && !validators.is_empty() {
                        info!(
                            "✅ [COMMITTEE SOURCE] Got epoch {} (ts={}, boundary={}, validators={})",
                            epoch, timestamp_ms, boundary_block, validators.len()
                        );

                        // Extract eth_addresses
                        let mut sorted_validators: Vec<_> = validators.clone().into_iter().collect();
                        sorted_validators.sort_by(|a, b| a.authority_key.cmp(&b.authority_key));

                        let mut eth_addresses = Vec::new();
                        for validator in &sorted_validators {
                            let eth_addr_bytes = if validator.address.starts_with("0x")
                                && validator.address.len() == 42
                            {
                                match hex::decode(&validator.address[2..]) {
                                    Ok(bytes) if bytes.len() == 20 => bytes,
                                    _ => {
                                        warn!(
                                            "⚠️ [EPOCH ETH ADDRESSES] Invalid eth address: {}",
                                            validator.address
                                        );
                                        vec![]
                                    }
                                }
                            } else {
                                warn!(
                                    "⚠️ [EPOCH ETH ADDRESSES] Missing eth address: {}",
                                    validator.address
                                );
                                vec![]
                            };
                            eth_addresses.push(eth_addr_bytes);
                        }

                        match crate::node::committee::build_committee_from_validator_info_list(
                            &validators,
                            target_epoch,
                        )
                        .await
                        {
                            Ok(committee) => {
                                info!(
                                    "✅ [COMMITTEE SOURCE] Built committee with {} authorities",
                                    committee.size()
                                );
                                return Ok((committee, eth_addresses));
                            }
                            Err(e) => {
                                warn!("⚠️ [COMMITTEE SOURCE] build_committee failed: {}", e);
                            }
                        }
                    } else if attempt % 10 == 0 {
                        info!(
                            "⏳ [COMMITTEE SOURCE] Waiting for epoch {} (got epoch={}, attempt {}/{})",
                            target_epoch, epoch, attempt, MAX_ATTEMPTS
                        );
                    }
                }
                Err(e) => {
                    if attempt == 1 || attempt % 10 == 0 {
                        info!(
                            "⏳ [COMMITTEE SOURCE] Local Go not ready: {} (attempt {}/{})",
                            e, attempt, MAX_ATTEMPTS
                        );
                    }
                }
            }
            tokio::time::sleep(tokio::time::Duration::from_millis(DELAY_MS)).await;
        }

        // ═══════════════════════════════════════════════════════════════════
        // PEER FALLBACK: Local Go failed after MAX_ATTEMPTS.
        // This typically happens after snapshot restore when:
        // 1. NOMT knownKeys is empty → GetAllValidators() returns 0
        // 2. Epoch validator cache doesn't exist (old snapshot)
        // Query healthy peers for the authoritative epoch boundary data.
        // ═══════════════════════════════════════════════════════════════════
        if !self.peer_rpc_addresses.is_empty() {
            warn!(
                "⚠️ [COMMITTEE SOURCE] Local Go timed out for epoch {}. Trying {} peer(s)...",
                target_epoch, self.peer_rpc_addresses.len()
            );

            for peer_addr in &self.peer_rpc_addresses {
                match crate::network::peer_rpc::query_peer_epoch_boundary_data(peer_addr, target_epoch).await {
                    Ok(pb) if pb.epoch == target_epoch && !pb.validators.is_empty() => {
                        info!(
                            "✅ [COMMITTEE SOURCE] PEER FALLBACK: Got {} validators for epoch {} from peer {}",
                            pb.validators.len(), target_epoch, peer_addr
                        );

                        // Convert ValidatorInfoSimple → proto::ValidatorInfo
                        let proto_validators: Vec<crate::node::executor_client::proto::ValidatorInfo> =
                            pb.validators.iter().map(|v| {
                                crate::node::executor_client::proto::ValidatorInfo {
                                    name: v.name.clone(),
                                    address: v.address.clone(),
                                    stake: v.stake.to_string(),
                                    protocol_key: v.protocol_key.clone(),
                                    network_key: v.network_key.clone(),
                                    authority_key: v.authority_key.clone(),
                                    p2p_address: v.p2p_address.clone(),
                                    ..Default::default()
                                }
                            }).collect();

                        // Extract eth_addresses
                        let mut sorted = proto_validators.clone();
                        sorted.sort_by(|a, b| a.authority_key.cmp(&b.authority_key));
                        let mut eth_addresses = Vec::new();
                        for validator in &sorted {
                            let eth_addr_bytes = if validator.address.starts_with("0x") && validator.address.len() == 42 {
                                hex::decode(&validator.address[2..]).unwrap_or_default()
                            } else {
                                vec![]
                            };
                            eth_addresses.push(eth_addr_bytes);
                        }

                        match crate::node::committee::build_committee_from_validator_info_list(
                            &proto_validators, target_epoch,
                        ).await {
                            Ok(committee) => {
                                info!(
                                    "✅ [COMMITTEE SOURCE] PEER FALLBACK: Built committee with {} authorities from peer {}",
                                    committee.size(), peer_addr
                                );
                                return Ok((committee, eth_addresses));
                            }
                            Err(e) => {
                                warn!("⚠️ [COMMITTEE SOURCE] PEER FALLBACK: build_committee failed from peer {}: {}", peer_addr, e);
                            }
                        }
                    }
                    Ok(pb) => {
                        warn!("⚠️ [COMMITTEE SOURCE] Peer {} returned epoch={} validators={} (expected epoch={})",
                            peer_addr, pb.epoch, pb.validators.len(), target_epoch);
                    }
                    Err(e) => {
                        warn!("⚠️ [COMMITTEE SOURCE] Peer {} query failed: {}", peer_addr, e);
                    }
                }
            }
        }

        Err(anyhow::anyhow!(
            "All sources failed for epoch {} committee: local Go timed out after {} attempts, {} peer(s) also failed",
            target_epoch,
            MAX_ATTEMPTS,
            self.peer_rpc_addresses.len()
        ))
    }



    /// Fetch committee AND timestamp from Go (UNIFIED SOURCE)
    ///
    /// CRITICAL: This returns the timestamp from Go's get_epoch_boundary_data response.
    /// Go derives this timestamp deterministically:
    /// - Epoch 0: Genesis timestamp from genesis.json
    /// - Epoch N: boundaryBlock.Header().TimeStamp() * 1000
    ///
    /// Using this timestamp ensures ALL nodes have identical genesis blocks = NO FORK!
    pub async fn fetch_committee_with_timestamp(
        &self,
        send_socket: &str,
        target_epoch: u64,
    ) -> Result<(Committee, u64, Vec<Vec<u8>>)> {
        let client = self.create_executor_client(send_socket);

        info!(
            "📋 [COMMITTEE SOURCE] Fetching committee+timestamp for epoch {} from LOCAL Go (unified source)",
            target_epoch
        );

        const INITIAL_DELAY_MS: u64 = 500;
        const MAX_DELAY_MS: u64 = 5000;
        const LOG_INTERVAL: u32 = 10;
        const MAX_ATTEMPTS: u32 = 60; // ~5 minutes with exponential backoff

        let mut attempt = 0u32;
        let mut delay_ms = INITIAL_DELAY_MS;

        loop {
            attempt += 1;

            // CRITICAL FIX: Prevent infinite loop when Go doesn't have epoch data
            // This can happen when transition_mode_only is called before Go syncs
            if attempt > MAX_ATTEMPTS {
                // ═══════════════════════════════════════════════════════════════════
                // PEER FALLBACK: Local Go failed after MAX_ATTEMPTS.
                // Query healthy peers for epoch boundary data + timestamp.
                // ═══════════════════════════════════════════════════════════════════
                if !self.peer_rpc_addresses.is_empty() {
                    warn!(
                        "⚠️ [UNIFIED TIMESTAMP] Local Go timed out for epoch {}. Trying {} peer(s)...",
                        target_epoch, self.peer_rpc_addresses.len()
                    );

                    for peer_addr in &self.peer_rpc_addresses {
                        match crate::network::peer_rpc::query_peer_epoch_boundary_data(peer_addr, target_epoch).await {
                            Ok(pb) if pb.epoch == target_epoch && !pb.validators.is_empty() => {
                                info!(
                                    "✅ [UNIFIED TIMESTAMP] PEER FALLBACK: Got {} validators, timestamp={} for epoch {} from {}",
                                    pb.validators.len(), pb.timestamp_ms, target_epoch, peer_addr
                                );

                                // Convert ValidatorInfoSimple → proto::ValidatorInfo
                                let proto_validators: Vec<crate::node::executor_client::proto::ValidatorInfo> =
                                    pb.validators.iter().map(|v| {
                                        crate::node::executor_client::proto::ValidatorInfo {
                                            name: v.name.clone(),
                                            address: v.address.clone(),
                                            stake: v.stake.to_string(),
                                            protocol_key: v.protocol_key.clone(),
                                            network_key: v.network_key.clone(),
                                            authority_key: v.authority_key.clone(),
                                            p2p_address: v.p2p_address.clone(),
                                            ..Default::default()
                                        }
                                    }).collect();

                                // Extract eth_addresses
                                let mut sorted = proto_validators.clone();
                                sorted.sort_by(|a, b| a.authority_key.cmp(&b.authority_key));
                                let mut eth_addresses = Vec::new();
                                for validator in &sorted {
                                    let eth_addr_bytes = if validator.address.starts_with("0x") && validator.address.len() == 42 {
                                        hex::decode(&validator.address[2..]).unwrap_or_default()
                                    } else {
                                        vec![]
                                    };
                                    eth_addresses.push(eth_addr_bytes);
                                }

                                match crate::node::committee::build_committee_from_validator_info_list(
                                    &proto_validators, target_epoch,
                                ).await {
                                    Ok(committee) => {
                                        info!(
                                            "✅ [UNIFIED TIMESTAMP] PEER FALLBACK: Built committee size={}, timestamp={} from peer {}",
                                            committee.size(), pb.timestamp_ms, peer_addr
                                        );
                                        return Ok((committee, pb.timestamp_ms, eth_addresses));
                                    }
                                    Err(e) => {
                                        warn!("⚠️ [UNIFIED TIMESTAMP] PEER FALLBACK: build_committee failed from peer {}: {}", peer_addr, e);
                                    }
                                }
                            }
                            Ok(pb) => {
                                warn!("⚠️ [UNIFIED TIMESTAMP] Peer {} returned epoch={} validators={} (expected epoch={})",
                                    peer_addr, pb.epoch, pb.validators.len(), target_epoch);
                            }
                            Err(e) => {
                                warn!("⚠️ [UNIFIED TIMESTAMP] Peer {} query failed: {}", peer_addr, e);
                            }
                        }
                    }
                }

                return Err(anyhow::anyhow!(
                    "All sources failed for epoch {} data after {} attempts, {} peer(s) also failed.",
                    target_epoch,
                    MAX_ATTEMPTS,
                    self.peer_rpc_addresses.len()
                ));
            }

            let should_log = attempt == 1 || attempt.is_multiple_of(LOG_INTERVAL);

            match client.get_epoch_boundary_data(target_epoch).await {
                Ok((epoch, timestamp_ms, boundary_block, validators, _, _)) => {
                    if epoch == target_epoch {
                        info!(
                            "✅ [UNIFIED TIMESTAMP] Got from Go: epoch={}, timestamp_ms={}, boundary_block={} (attempt {})",
                            epoch, timestamp_ms, boundary_block, attempt
                        );

                        // Extract eth_addresses atomically
                        let mut sorted_validators: Vec<_> = validators.clone().into_iter().collect();
                        sorted_validators.sort_by(|a, b| a.authority_key.cmp(&b.authority_key));

                        let mut eth_addresses = Vec::new();
                        for validator in &sorted_validators {
                            let eth_addr_bytes = if validator.address.starts_with("0x")
                                && validator.address.len() == 42
                            {
                                match hex::decode(&validator.address[2..]) {
                                    Ok(bytes) if bytes.len() == 20 => bytes,
                                    _ => {
                                        warn!(
                                            "⚠️ [EPOCH ETH ADDRESSES] Invalid eth address: {}",
                                            validator.address
                                        );
                                        vec![]
                                    }
                                }
                            } else {
                                warn!(
                                    "⚠️ [EPOCH ETH ADDRESSES] Missing eth address: {}",
                                    validator.address
                                );
                                vec![]
                            };
                            eth_addresses.push(eth_addr_bytes);
                        }

                        // Build committee from validators
                        match crate::node::committee::build_committee_from_validator_info_list(
                            &validators,
                            target_epoch,
                        )
                        .await
                        {
                            Ok(committee) => {
                                info!(
                                    "✅ [UNIFIED TIMESTAMP] Committee size={}, AUTHORITATIVE timestamp={} ms",
                                    committee.size(), timestamp_ms
                                );
                                return Ok((committee, timestamp_ms, eth_addresses));
                            }
                            Err(e) => {
                                if should_log {
                                    warn!(
                                        "⚠️ [UNIFIED TIMESTAMP] build_committee failed: {} (attempt {})",
                                        e, attempt
                                    );
                                }
                            }
                        }
                    } else if should_log {
                        info!(
                            "⏳ [UNIFIED TIMESTAMP] Local Go at epoch {}, waiting for epoch {} (attempt {})",
                            epoch, target_epoch, attempt
                        );
                    }
                }
                Err(e) => {
                    if should_log {
                        info!(
                            "⏳ [UNIFIED TIMESTAMP] Local Go not ready: {} (attempt {})",
                            e, attempt
                        );
                    }
                }
            }

            tokio::time::sleep(tokio::time::Duration::from_millis(delay_ms)).await;
            delay_ms = std::cmp::min(delay_ms * 2, MAX_DELAY_MS);
        }
    }

    /// Validate that this source matches expected epoch
    /// Returns true if matches, logs warning and returns false otherwise
    pub fn validate_epoch(&self, expected_epoch: u64) -> bool {
        if self.epoch != expected_epoch {
            warn!(
                "⚠️ [COMMITTEE SOURCE] Epoch mismatch! Expected={}, Source={}. \
                 This may indicate network partition or stale local state.",
                expected_epoch, self.epoch
            );
            false
        } else {
            true
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_source(epoch: u64, last_block: u64, is_peer: bool) -> CommitteeSource {
        CommitteeSource {
            socket_path: "/tmp/test_recv.sock".to_string(),
            epoch,
            last_block,
            is_peer,
            peer_rpc_addresses: Vec::new(),
        }
    }

    #[test]
    fn test_validate_epoch_matching() {
        let source = make_source(5, 100, false);
        assert!(source.validate_epoch(5));
    }

    #[test]
    fn test_validate_epoch_mismatch() {
        let source = make_source(5, 100, false);
        assert!(!source.validate_epoch(6));
        assert!(!source.validate_epoch(0));
    }

    #[test]
    fn test_committee_source_local() {
        let source = make_source(0, 0, false);
        assert!(!source.is_peer);
        assert_eq!(source.epoch, 0);
        assert_eq!(source.last_block, 0);
    }

    #[test]
    fn test_committee_source_peer() {
        let source = make_source(3, 500, true);
        assert!(source.is_peer);
        assert_eq!(source.epoch, 3);
        assert_eq!(source.last_block, 500);
    }

    #[test]
    fn test_create_executor_client() {
        let source = make_source(1, 50, false);
        let client = source.create_executor_client("/tmp/test_send.sock");
        assert!(client.is_enabled());
        assert!(!client.can_commit());
    }

    #[test]
    fn test_committee_source_with_peer_addresses() {
        let source = CommitteeSource {
            socket_path: "/tmp/test.sock".to_string(),
            epoch: 2,
            last_block: 200,
            is_peer: true,
            peer_rpc_addresses: vec!["127.0.0.1:9000".to_string(), "127.0.0.1:9001".to_string()],
        };
        assert_eq!(source.peer_rpc_addresses.len(), 2);
        assert!(source.validate_epoch(2));
    }
}
