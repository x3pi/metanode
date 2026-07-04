// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! RPC query methods for ExecutorClient — state queries.
//!
//! These methods query Go Master state via the request/response socket:
//! - `get_validators_at_block`
//! - `get_last_block_number`
//!
//! Epoch-related queries are in `rpc_queries_epoch.rs`.

use anyhow::Result;
use prost::Message;

use tracing::{info, trace, warn};

use super::block_store;
use super::proto::{self, GetValidatorsAtBlockRequest, Request, Response, ValidatorInfo};
use super::ExecutorClient;

impl ExecutorClient {
    /// Helper to load validators locally from committee.json for pure Rust execution mode
    pub(crate) async fn load_local_validators(&self) -> Result<(Vec<ValidatorInfo>, u64, u64)> {
        // Load committee.json
        let paths = vec![
            std::path::PathBuf::from("config/committee.json"),
            std::path::PathBuf::from("consensus/metanode/config/committee.json"),
            std::path::PathBuf::from("../config/committee.json"),
            std::path::PathBuf::from("../../config/committee.json"),
            std::path::PathBuf::from("/home/abc/chain-n/metanode/consensus/metanode/config/committee.json"),
        ];
        
        let mut content = None;
        for p in paths {
            if p.exists() {
                if let Ok(c) = std::fs::read_to_string(&p) {
                    info!("📖 [RUST-EXEC] Loaded local committee from {:?}", p);
                    content = Some(c);
                    break;
                }
            }
        }
        
        let json_str = match content {
            Some(c) => c,
            None => {
                info!("⚠️ [RUST-EXEC] committee.json not found in paths, dynamically building from /opt/metanode/node-*/keys");
                let mut authorities = Vec::new();
                for i in 0..10 {
                    let base_path = format!("/opt/metanode/node-{}/keys", i);
                    if let (Ok(proto), Ok(net), Ok(auth)) = (
                        std::fs::read_to_string(format!("{}/protocol_key.json", base_path)),
                        std::fs::read_to_string(format!("{}/network_key.json", base_path)),
                        std::fs::read_to_string(format!("{}/authority_key.json", base_path)),
                    ) {
                        let eth_addr = std::fs::read_to_string(format!("{}/eth_key.json", base_path))
                            .ok()
                            .and_then(|content| {
                                serde_json::from_str::<serde_json::Value>(&content)
                                    .ok()
                                    .and_then(|v| v.get("ETH_ADDRESS").and_then(|s| s.as_str()).map(|s| s.to_string()))
                            })
                            .unwrap_or_else(|| "0x0000000000000000000000000000000000000000".to_string());

                        let auth_str = auth.trim();
                        // Handle if authority_key is a JSON object with public_key_base64
                        let auth_b64 = if let Ok(parsed) = serde_json::from_str::<serde_json::Value>(auth_str) {
                            if let Some(pub_b64) = parsed.get("public_key_base64").and_then(|v| v.as_str()) {
                                pub_b64.to_string()
                            } else {
                                auth_str.to_string()
                            }
                        } else {
                            auth_str.trim_matches('"').to_string()
                        };

                        authorities.push(format!(r#"
    {{
      "stake": 1000,
      "address": "{}",
      "p2p_address": "/ip4/192.168.1.232/tcp/900{}",
      "hostname": "node-{}",
      "authority_key": "{}",
      "protocol_key": "{}",
      "network_key": "{}"
    }}"#, eth_addr, i, i, auth_b64, proto.trim(), net.trim()));
                    }
                }
                
                if authorities.is_empty() {
                    warn!("❌ [RUST-EXEC] No local keys found in /opt/metanode/node-*/keys. Node will likely fail to start.");
                }

                let total_stake = authorities.len() * 1000;
                let quorum = total_stake * 2 / 3;
                let validity = total_stake / 3;
                
                let authorities_str = authorities.join(",\n");
                let json_str = format!(r#"{{
  "epoch": 0,
  "total_stake": {},
  "quorum_threshold": {},
  "validity_threshold": {},
  "authorities": [
{}
  ],
  "epoch_timestamp_ms": 1772265024018,
  "last_global_exec_index": 0
}}"#, total_stake, quorum, validity, authorities_str);
                json_str
            }
        };
        
        let parsed: serde_json::Value = serde_json::from_str(&json_str)?;
        let authorities = parsed.get("authorities").and_then(|v| v.as_array()).ok_or_else(|| anyhow::anyhow!("authorities field missing"))?;
        
        use base64::{engine::general_purpose, Engine as _};
        let mut validators = Vec::new();
        for auth in authorities {
            let address = auth.get("address").and_then(|v| v.as_str()).unwrap_or_default().to_string();
            let p2p_address = auth.get("p2p_address").and_then(|v| v.as_str()).unwrap_or_default().to_string();
            let stake = auth.get("stake").and_then(|v| v.as_u64()).unwrap_or(0);
            let name = auth.get("hostname").and_then(|v| v.as_str()).unwrap_or_default().to_string();
            
            let auth_key_str = auth.get("authority_key").and_then(|v| v.as_str()).unwrap_or_default();
            let proto_key_str = auth.get("protocol_key").and_then(|v| v.as_str()).unwrap_or_default();
            let net_key_str = auth.get("network_key").and_then(|v| v.as_str()).unwrap_or_default();
            
            let authority_key = general_purpose::STANDARD.decode(auth_key_str.trim()).unwrap_or_default();
            let mut protocol_key = general_purpose::STANDARD.decode(proto_key_str.trim()).unwrap_or_default();
            let mut network_key = general_purpose::STANDARD.decode(net_key_str.trim()).unwrap_or_default();
            
            if protocol_key.len() == 64 {
                protocol_key = protocol_key[32..64].to_vec();
            }
            if network_key.len() == 64 {
                network_key = network_key[32..64].to_vec();
            }
            
            validators.push(ValidatorInfo {
                address,
                stake: stake.to_string(),
                name,
                authority_key,
                protocol_key,
                network_key,
                description: String::new(),
                website: String::new(),
                image: String::new(),
                commission_rate: 0,
                min_self_delegation: "0".to_string(),
                accumulated_rewards_per_share: "0".to_string(),
                p2p_address,
            });
        }
        
        let epoch_timestamp_ms = parsed.get("epoch_timestamp_ms").and_then(|v| v.as_u64()).unwrap_or(0);
        let last_global_exec_index = parsed.get("last_global_exec_index").and_then(|v| v.as_u64()).unwrap_or(0);
        
        Ok((validators, epoch_timestamp_ms, last_global_exec_index))
    }

    /// Get validators at a specific block number from Go state
    /// Used for startup (block 0) and epoch transition (last_global_exec_index)
    pub async fn get_validators_at_block(
        &self,
        block_number: u64,
    ) -> Result<(Vec<ValidatorInfo>, u64, u64)> {
        if self.rust_execution_enabled {
            return self.load_local_validators().await;
        }
        if !self.is_enabled() {
            return Err(anyhow::anyhow!("Executor client is not enabled"));
        }

        // Circuit breaker check
        if let Err(reason) = self.rpc_circuit_breaker.check("get_validators_at_block") {
            return Err(anyhow::anyhow!("Circuit breaker: {}", reason));
        }

        // Create GetValidatorsAtBlockRequest
        let request = Request {
            payload: Some(proto::request::Payload::GetValidatorsAtBlockRequest(
                GetValidatorsAtBlockRequest { block_number },
            )),
        };

        // Encode request to protobuf bytes
        let mut request_buf = Vec::new();
        request.encode(&mut request_buf)?;

        // FFI INTEGRATION: Send request directly via CGo callback
        let response_buf = self.execute_rpc_request(&request_buf).await?;

        trace!(
            "📥 [EXECUTOR-REQ] Received {} bytes from Go FFI, decoding...",
            response_buf.len()
        );

        // Decode response
        let response = Response::decode(&response_buf[..]).map_err(|e| {
            anyhow::anyhow!(
                "Failed to decode response from Go: {}. Response length: {} bytes.",
                e,
                response_buf.len()
            )
        })?;

        trace!("🔍 [EXECUTOR-REQ] Decoded response successfully");
        trace!(
            "🔍 [EXECUTOR-REQ] Response payload type: {:?}",
            response.payload
        );

        // Debug: Check all possible payload types
        match &response.payload {
            Some(proto::response::Payload::NotifyEpochChangeResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is NotifyEpochChangeResponse (ignored in debug match)");
            }
            Some(proto::response::Payload::ValidatorInfoList(v)) => {
                info!(
                    "🔍 [EXECUTOR-REQ] Payload is ValidatorInfoList with {} validators",
                    v.validators.len()
                );
                // CRITICAL: Log each ValidatorInfo exactly as received from Go
                for (idx, validator) in v.validators.iter().enumerate() {
                    let auth_hex = hex::encode(&validator.authority_key);
                    let auth_key_preview = if auth_hex.len() > 50 {
                        format!("{}...", &auth_hex[..50])
                    } else {
                        auth_hex
                    };
                    info!("📥 [RUST←GO] ValidatorInfo[{}]: address={}, stake={}, name={}, authority_key={}, protocol_key={}, network_key={}",
                            idx, validator.address, validator.stake, validator.name,
                            auth_key_preview, hex::encode(&validator.protocol_key), hex::encode(&validator.network_key));
                }
            }
            Some(proto::response::Payload::Error(e)) => {
                info!("🔍 [EXECUTOR-REQ] Payload is Error: {}", e);
            }
            Some(proto::response::Payload::ValidatorList(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is ValidatorList (not expected for this request)");
            }
            Some(proto::response::Payload::ServerStatus(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is ServerStatus (not expected for this request)");
            }
            Some(proto::response::Payload::LastBlockNumberResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is LastBlockNumberResponse (not expected for GetValidatorsAtBlockRequest)");
            }
            Some(proto::response::Payload::GetCurrentEpochResponse(_)) => {
                info!("🔍 [EXECUTOR-REQ] Payload is GetCurrentEpochResponse (handled below)");
            }
            Some(proto::response::Payload::GetEpochStartTimestampResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is GetEpochStartTimestampResponse (not expected for this request)");
            }
            Some(proto::response::Payload::AdvanceEpochResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is AdvanceEpochResponse (not expected for this request)");
            }
            Some(proto::response::Payload::EpochBoundaryData(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is EpochBoundaryData (not expected for this request)");
            }
            Some(proto::response::Payload::SetConsensusStartBlockResponse(_))
            | Some(proto::response::Payload::SetSyncStartBlockResponse(_))
            | Some(proto::response::Payload::WaitForSyncToBlockResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is Transition Handoff response (not expected for this request)");
            }
            Some(proto::response::Payload::ForceCommitResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is ForceCommitResponse (not expected for this request)");
            }
            Some(proto::response::Payload::GetBlocksRangeResponse(_))
            | Some(proto::response::Payload::SyncBlocksResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is Block Sync response (not expected for this request)");
            }
            Some(proto::response::Payload::GetLastHandledCommitIndexResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is GetLastHandledCommitIndexResponse (not expected for this request)");
            }
            Some(proto::response::Payload::SetLastExecutedCommitHashResponse(_)) => {
                warn!("🔍 [EXECUTOR-REQ] Payload is SetLastExecutedCommitHashResponse (not expected for this request)");
            }
            None => {
                warn!("🔍 [EXECUTOR-REQ] Payload is None - response structure may be incorrect");
                warn!("🔍 [EXECUTOR-REQ] Full response debug: {:?}", response);
            }
        }

        match response.payload {
                Some(proto::response::Payload::NotifyEpochChangeResponse(_)) => {
                    Err(anyhow::anyhow!("Unexpected NotifyEpochChangeResponse"))
                }
                Some(proto::response::Payload::ValidatorInfoList(validator_info_list)) => {
                    info!("✅ [EXECUTOR-REQ] Received ValidatorInfoList from Go at block {} with {} validators, epoch_timestamp_ms={}, last_global_exec_index={}",
                        block_number, validator_info_list.validators.len(),
                        validator_info_list.epoch_timestamp_ms,
                        validator_info_list.last_global_exec_index);

                    // CRITICAL: Log each ValidatorInfo exactly as received from Go
                    for (idx, validator) in validator_info_list.validators.iter().enumerate() {
                        let auth_hex = hex::encode(&validator.authority_key);
                        let auth_key_preview = if auth_hex.len() > 50 {
                            format!("{}...", &auth_hex[..50])
                        } else {
                            auth_hex
                        };
                        info!("📥 [RUST←GO] ValidatorInfo[{}]: address={}, stake={}, name={}, authority_key={}, protocol_key={}, network_key={}",
                            idx, validator.address, validator.stake, validator.name,
                            auth_key_preview, hex::encode(&validator.protocol_key), hex::encode(&validator.network_key));
                    }

                    Ok((
                        validator_info_list.validators,
                        validator_info_list.epoch_timestamp_ms,
                        validator_info_list.last_global_exec_index,
                    ))
                }
                Some(proto::response::Payload::Error(error_msg)) => {
                    Err(anyhow::anyhow!("Go returned error: {}", error_msg))
                }
                Some(proto::response::Payload::ValidatorList(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected ValidatorList response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::ServerStatus(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected ServerStatus response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::LastBlockNumberResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected LastBlockNumberResponse response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::GetCurrentEpochResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected GetCurrentEpochResponse response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::GetEpochStartTimestampResponse(_)) => {
                    Err(anyhow::anyhow!("Unexpected GetEpochStartTimestampResponse response (expected ValidatorInfoList)"))
                }
                Some(proto::response::Payload::AdvanceEpochResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected AdvanceEpochResponse response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::EpochBoundaryData(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected EpochBoundaryData response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::SetConsensusStartBlockResponse(_))
                | Some(proto::response::Payload::SetSyncStartBlockResponse(_))
                | Some(proto::response::Payload::WaitForSyncToBlockResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected Transition Handoff response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::ForceCommitResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected ForceCommitResponse response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::GetBlocksRangeResponse(_))
                | Some(proto::response::Payload::SyncBlocksResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected Block Sync response (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::GetLastHandledCommitIndexResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected GetLastHandledCommitIndexResponse (expected ValidatorInfoList)"
                    ))
                }
                Some(proto::response::Payload::SetLastExecutedCommitHashResponse(_)) => {
                    Err(anyhow::anyhow!(
                        "Unexpected SetLastExecutedCommitHashResponse (expected ValidatorInfoList)"
                    ))
                }
                None => {
                    Err(anyhow::anyhow!("Unexpected response type from Go. Response payload: None"))
                }
            }
    }

    pub(crate) async fn recover_from_disk_if_fresh(&self) {
        if !self.rust_execution_enabled {
            return;
        }
        let mut expected_guard = self.next_expected_index.lock().await;
        let next_expected = *expected_guard;
        let mut bn_guard = self.next_block_number.lock().await;
        let next_bn = *bn_guard;
        
        if next_expected <= 1 {
            if let Some(storage_path) = &self.storage_path {
                if let Ok(Some(max_gei)) = block_store::get_max_stored_gei(storage_path).await {
                    if let Ok(data) = block_store::load_executable_block(storage_path, max_gei).await {
                        if let Ok(bdata) = super::proto::ExecutableBlock::decode(&data[..]) {
                            let bn = bdata.block_number;
                            *expected_guard = max_gei + 1;
                            *bn_guard = bn + 1;
                            let mut ep_guard = self.last_processed_epoch.lock().await;
                            *ep_guard = bdata.epoch;
                            info!("🔋 [STARTUP-RECOVERY] Recovered executor client state from disk: next_expected={}, next_block_number={}, epoch={}", max_gei + 1, bn + 1, bdata.epoch);
                        }
                    }
                }
            }
        } else if next_bn == 0 {
            if let Some(storage_path) = &self.storage_path {
                let last_gei = next_expected.saturating_sub(1);
                if let Ok(data) = block_store::load_executable_block(storage_path, last_gei).await {
                    if let Ok(bdata) = super::proto::ExecutableBlock::decode(&data[..]) {
                        *bn_guard = bdata.block_number + 1;
                        let mut ep_guard = self.last_processed_epoch.lock().await;
                        *ep_guard = bdata.epoch;
                        info!("🔋 [STARTUP-RECOVERY] Recovered block number and epoch from disk for next_expected={}: next_block_number={}, epoch={}", next_expected, bdata.block_number + 1, bdata.epoch);
                    }
                }
            }
        }
    }

    /// Get last block number AND last global exec index from Go Master
    /// Returns (last_block_number, last_global_exec_index, is_ready, last_executed_commit_hash, last_epoch)
    /// CRITICAL: last_block_number counts only non-empty commits (actual blocks)
    ///           last_global_exec_index counts ALL commits (including empty ones)
    ///           Use last_global_exec_index for epoch transition SYNC WAIT comparison
    pub async fn get_last_block_number(&self) -> Result<(u64, u64, bool, [u8; 32], u64)> {
        if self.rust_execution_enabled {
            self.recover_from_disk_if_fresh().await;
            let next_bn = *self.next_block_number.lock().await;
            let next_expected = *self.next_expected_index.lock().await;
            let last_ep = *self.last_processed_epoch.lock().await;
            let bn = next_bn.saturating_sub(1);
            let gei = next_expected.saturating_sub(1);
            return Ok((bn, gei, true, [0u8; 32], last_ep));
        }
        if !self.is_enabled() {
            return Err(anyhow::anyhow!("Executor client is not enabled"));
        }

        // Circuit breaker check
        if let Err(reason) = self.rpc_circuit_breaker.check("get_last_block_number") {
            return Err(anyhow::anyhow!("Circuit breaker: {}", reason));
        }

        // Create GetLastBlockNumberRequest
        let request = Request {
            payload: Some(proto::request::Payload::GetLastBlockNumberRequest(
                proto::GetLastBlockNumberRequest {},
            )),
        };

        // Encode request to protobuf bytes
        let mut request_buf = Vec::new();
        request.encode(&mut request_buf)?;

        // FFI INTEGRATION: Send request directly via CGo callback
        let response_buf = self.execute_rpc_request(&request_buf).await?;

        // HEX DUMP: Log raw proto bytes to diagnose gei=0 decode bug
        let hex_preview: String = response_buf
            .iter()
            .take(64)
            .map(|b| format!("{:02x}", b))
            .collect::<Vec<_>>()
            .join(" ");
        trace!(
            "📥 [EXECUTOR-REQ] Received {} bytes from Go FFI, hex={}, decoding...",
            response_buf.len(),
            hex_preview
        );

        // Decode response
        let response = Response::decode(&response_buf[..])
            .map_err(|e| anyhow::anyhow!("Failed to decode response from Go: {}", e))?;

        match response.payload {
            Some(proto::response::Payload::NotifyEpochChangeResponse(_)) => {
                Err(anyhow::anyhow!("Unexpected NotifyEpochChangeResponse"))
            }
            Some(proto::response::Payload::LastBlockNumberResponse(r)) => {
                trace!("✅ [EXECUTOR-REQ] Received LastBlockNumber: {}, GEI: {}, Epoch: {}, IsReady: {}", 
                    r.last_block_number, r.last_global_exec_index, r.last_epoch, r.is_ready);
                let mut hash = [0u8; 32];
                if r.last_executed_commit_hash.len() == 32 {
                    hash.copy_from_slice(&r.last_executed_commit_hash);
                } else if !r.last_executed_commit_hash.is_empty() {
                    warn!("⚠️ [EXECUTOR-REQ] Received invalid last_executed_commit_hash length: {}, expected 32. Using zeroes.", r.last_executed_commit_hash.len());
                }
                Ok((
                    r.last_block_number,
                    r.last_global_exec_index,
                    r.is_ready,
                    hash,
                    r.last_epoch,
                ))
            }
            Some(proto::response::Payload::Error(error_msg)) => {
                Err(anyhow::anyhow!("Go returned error: {}", error_msg))
            }
            _ => Err(anyhow::anyhow!(
                "Unexpected response type from Go (expected LastBlockNumberResponse)"
            )),
        }
    }

    /// Get last global exec index from Go Master
    /// CRITICAL: This returns the global_exec_index (tracks ALL commits including empty)
    /// Use this for epoch transition SYNC WAIT — NOT get_last_block_number!
    pub async fn get_last_global_exec_index(&self) -> Result<u64> {
        if self.rust_execution_enabled {
            self.recover_from_disk_if_fresh().await;
            let next_expected = *self.next_expected_index.lock().await;
            return Ok(next_expected.saturating_sub(1));
        }
        if !self.is_enabled() {
            return Err(anyhow::anyhow!("Executor client is not enabled"));
        }

        // Circuit breaker check
        if let Err(reason) = self.rpc_circuit_breaker.check("get_last_global_exec_index") {
            return Err(anyhow::anyhow!("Circuit breaker: {}", reason));
        }

        // Reuse GetLastBlockNumberRequest — Go response now includes last_global_exec_index
        let request = Request {
            payload: Some(proto::request::Payload::GetLastBlockNumberRequest(
                proto::GetLastBlockNumberRequest {},
            )),
        };

        let mut request_buf = Vec::new();
        request.encode(&mut request_buf)?;

        // FFI INTEGRATION: Send request directly via CGo callback
        let response_buf = self.execute_rpc_request(&request_buf).await?;

        // HEX DUMP: Log raw proto bytes to diagnose gei=0 decode bug
        let hex_preview: String = response_buf
            .iter()
            .take(64)
            .map(|b| format!("{:02x}", b))
            .collect::<Vec<_>>()
            .join(" ");
        trace!(
            "📥 [EXECUTOR-REQ-GEI] Received {} bytes from Go FFI, hex={}",
            response_buf.len(),
            hex_preview
        );

        let response = Response::decode(&response_buf[..])
            .map_err(|e| anyhow::anyhow!("Failed to decode response from Go: {}", e))?;

        match response.payload {
            Some(proto::response::Payload::LastBlockNumberResponse(res)) => {
                let last_gei = res.last_global_exec_index;
                trace!(
                    "✅ [EXECUTOR-REQ] Go last_global_exec_index={} (block={})",
                    last_gei, res.last_block_number
                );
                Ok(last_gei)
            }
            Some(proto::response::Payload::Error(error_msg)) => {
                Err(anyhow::anyhow!("Go returned error: {}", error_msg))
            }
            _ => Err(anyhow::anyhow!(
                "Unexpected response type from Go (expected LastBlockNumberResponse)"
            )),
        }
    }

    /// Trigger ForceCommit in Go to flush transactions immediately and generate a block
    pub async fn send_force_commit(&self, reason: String) -> Result<bool> {
        if self.rust_execution_enabled {
            info!("Local Rust execution skipped send_force_commit (reason: {})", reason);
            return Ok(true);
        }
        if !self.is_enabled() {
            return Ok(false);
        }

        // Circuit breaker check
        if let Err(reason_cb) = self.rpc_circuit_breaker.check("send_force_commit") {
            return Err(anyhow::anyhow!("Circuit breaker: {}", reason_cb));
        }

        let request = Request {
            payload: Some(proto::request::Payload::ForceCommitRequest(
                proto::ForceCommitRequest { reason },
            )),
        };

        let mut request_buf = Vec::new();
        request.encode(&mut request_buf)?;

        // FFI INTEGRATION: Send request directly via CGo callback
        let response_buf = self.execute_rpc_request(&request_buf).await?;

        let response = Response::decode(&response_buf[..])
            .map_err(|e| anyhow::anyhow!("Failed to decode response from Go: {}", e))?;

        match response.payload {
            Some(proto::response::Payload::ForceCommitResponse(res)) => {
                info!("✅ [EXECUTOR-REQ] ForceCommit successful: {}", res.message);
                Ok(res.success)
            }
            Some(proto::response::Payload::Error(error_msg)) => {
                Err(anyhow::anyhow!("Go returned error: {}", error_msg))
            }
            _ => Err(anyhow::anyhow!("Unexpected response type for ForceCommit")),
        }
    }

    // ========================================================================
    // GO-AUTHORITATIVE GEI: Recovery Query
    // ========================================================================

    /// Query Go's last handled commit state for recovery after restart.
    /// This replaces the fragile fragment_offset reconstruction logic.
    ///
    /// Returns (last_commit_index, last_gei, last_block_number, epoch, is_authoritative, last_block_timestamp_ms, state_root)
    pub async fn get_last_handled_commit_index(&self) -> Result<(u32, u64, u64, u64, bool, u64, Vec<u8>)> {
        if self.rust_execution_enabled {
            self.recover_from_disk_if_fresh().await;
            let next_bn = *self.next_block_number.lock().await;
            let next_expected = *self.next_expected_index.lock().await;
            let last_ep = *self.last_processed_epoch.lock().await;
            let bn = next_bn.saturating_sub(1);
            let gei = next_expected.saturating_sub(1);
            info!("Local Rust execution get_last_handled_commit_index returned last_gei={}", gei);
            return Ok((0, gei, bn, last_ep, true, 0, vec![]));
        }
        if !self.is_enabled() {
            return Err(anyhow::anyhow!("Executor client is not enabled"));
        }

        // Circuit breaker check
        if let Err(reason) = self.rpc_circuit_breaker.check("get_last_handled_commit_index") {
            return Err(anyhow::anyhow!("Circuit breaker: {}", reason));
        }

        let request = Request {
            payload: Some(proto::request::Payload::GetLastHandledCommitIndexRequest(
                proto::GetLastHandledCommitIndexRequest {},
            )),
        };

        let mut request_buf = Vec::new();
        request.encode(&mut request_buf)?;

        // FFI INTEGRATION: Send request directly via CGo callback
        let response_buf = self.execute_rpc_request(&request_buf).await?;

        let response = Response::decode(&response_buf[..])
            .map_err(|e| anyhow::anyhow!("Failed to decode response from Go: {}", e))?;

        match response.payload {
            Some(proto::response::Payload::GetLastHandledCommitIndexResponse(res)) => {
                info!(
                    "🔑 [GO-AUTH GEI] Recovery state received: last_commit={}, last_gei={}, block={}, epoch={}, authoritative={}, timestamp={}, state_root={} bytes",
                    res.last_commit_index, res.last_gei, res.last_block_number, res.epoch, res.is_authoritative, res.last_block_timestamp_ms, res.state_root.len()
                );
                Ok((
                    res.last_commit_index,
                    res.last_gei,
                    res.last_block_number,
                    res.epoch,
                    res.is_authoritative,
                    res.last_block_timestamp_ms,
                    res.state_root,
                ))
            }
            Some(proto::response::Payload::Error(error_msg)) => {
                Err(anyhow::anyhow!("Go returned error for GetLastHandledCommitIndex: {}", error_msg))
            }
            _ => Err(anyhow::anyhow!("Unexpected response type for GetLastHandledCommitIndex")),
        }
    }

    /// Set last executed commit hash in Go to align startup consensus metadata
    pub async fn set_last_executed_commit_hash(&self, hash: [u8; 32]) -> Result<()> {
        if self.rust_execution_enabled {
            info!("Local Rust execution set_last_executed_commit_hash to: {}", hex::encode(hash));
            return Ok(());
        }
        if !self.is_enabled() {
            return Err(anyhow::anyhow!("Executor client is not enabled"));
        }

        // Circuit breaker check
        if let Err(reason) = self.rpc_circuit_breaker.check("set_last_executed_commit_hash") {
            return Err(anyhow::anyhow!("Circuit breaker: {}", reason));
        }

        let request = Request {
            payload: Some(proto::request::Payload::SetLastExecutedCommitHashRequest(
                proto::SetLastExecutedCommitHashRequest {
                    last_executed_commit_hash: hash.to_vec(),
                },
            )),
        };

        let mut request_buf = Vec::new();
        request.encode(&mut request_buf)?;

        let response_buf = self.execute_rpc_request(&request_buf).await?;

        let response = Response::decode(&response_buf[..])
            .map_err(|e| anyhow::anyhow!("Failed to decode response from Go: {}", e))?;

        match response.payload {
            Some(proto::response::Payload::SetLastExecutedCommitHashResponse(res)) => {
                if res.success {
                    info!("✅ [EXECUTOR-REQ] SetLastExecutedCommitHash successful");
                    Ok(())
                } else {
                    Err(anyhow::anyhow!("Go returned failure to set hash"))
                }
            }
            Some(proto::response::Payload::Error(error_msg)) => {
                Err(anyhow::anyhow!("Go returned error: {}", error_msg))
            }
            _ => Err(anyhow::anyhow!("Unexpected response type for SetLastExecutedCommitHash")),
        }
    }
}
