// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! ConsensusNode Phase 1 storage and epoch discovery initialization.

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use crate::node::epoch_store::detect_local_epoch;
use crate::node::ConsensusNode;
use crate::node::StorageSetup;
use anyhow::Result;
use consensus_config::AuthorityIndex;
use std::sync::Arc;
use tracing::{info, warn};

impl ConsensusNode {
    /// Initializes executor client, discovers epoch from peers/Go, loads committee,
    /// calculates execution index, finds own identity in the committee.
    pub(crate) async fn setup_storage(config: &NodeConfig) -> Result<StorageSetup> {
        info!("🚀 [STARTUP] Loading latest block, epoch and committee from Go state...");

        let executor_client = Arc::new(ExecutorClient::new(
            true,
            false,
            config.executor_send_socket_path.clone(),
            config.executor_receive_socket_path.clone(),
            Some(config.storage_path.clone()),
        ));

        // SNAPSHOT RESTORE FIX: Go Master needs time to load DB after snapshot restore.
        // Go now has an explicit blockchainInitDone flag. is_ready=true means the block
        // number is the FINAL authoritative value — no transient metadata.json values.
        let latest_block_number = {
            let retry_interval = std::time::Duration::from_millis(500);
            let mut attempt = 1;
            let block_num = loop {
                match executor_client.get_last_block_number().await {
                    Ok((n, _, true, _, _)) => {
                        info!(
                            "✅ [STARTUP] Got block number {} from Go (is_ready=true) (attempt {})",
                            n, attempt
                        );
                        break n;
                    }
                    Ok((n, _, false, _, _)) => {
                        if attempt % 10 == 0 {
                            info!(
                                "⏳ [STARTUP] Go not ready (block={}) (attempt {}). Retrying indefinitely to guarantee state parity...",
                                n, attempt
                            );
                        }
                        tokio::time::sleep(retry_interval).await;
                    }
                    Err(e) => {
                        if attempt % 10 == 0 {
                            warn!(
                                "⚠️ [STARTUP] Failed to fetch block from Go (attempt {}): {}. Retrying indefinitely to prevent starting with stale data...",
                                attempt, e
                            );
                        }
                        tokio::time::sleep(retry_interval).await;
                    }
                }
                attempt += 1;
            };

            if block_num == 0 {
                warn!("⚠️ [STARTUP] Go reporting block=0 natively (is_ready=true). This is a fresh node.");
            }

            block_num
        };

        // ═══════════════════════════════════════════════════════════════════════
        // CRITICAL FIX: ALL nodes MUST use local Go epoch, NOT peer epoch!
        // Using peer epoch causes DEADLOCK for nodes recovering from snapshot:
        //   1. Peer says epoch=100 → Rust advances internal state to epoch 100
        //   2. Deferred epoch transition waits for Go GEI >= boundary
        //   3. But Go GEI=0 (snapshot restore) → 120s timeout → DEADLOCK
        // All nodes must sync blocks sequentially: epoch transitions happen naturally
        // when Go processes blocks up to the epoch boundary.
        // ═══════════════════════════════════════════════════════════════════════
        let (go_epoch, peer_last_block, best_socket) = {
            // Get epoch from Go Master. Epoch 0 is valid for genesis-era chains.
            // No retry needed — Go has already loaded blockchain state by this point,
            // as evidenced by latest_block_number being available.
            let epoch = match executor_client.get_current_epoch().await {
                Ok(e) => {
                    info!(
                        "✅ [STARTUP] Got epoch {} from Go (block={})",
                        e, latest_block_number
                    );
                    e
                }
                Err(e) => {
                    // Retry indefinitely
                    let retry_interval = std::time::Duration::from_millis(500);
                    let mut attempt = 2;
                    let mut last_err = e;

                    loop {
                        if attempt % 10 == 0 {
                            warn!(
                                "⚠️ [STARTUP] Failed to get epoch (attempt {}): {}. Retrying indefinitely...",
                                attempt, last_err
                            );
                        }
                        tokio::time::sleep(retry_interval).await;
                        match executor_client.get_current_epoch().await {
                            Ok(e) => {
                                info!(
                                    "✅ [STARTUP] Got epoch {} from Go (attempt {}, block={})",
                                    e, attempt, latest_block_number
                                );
                                break e;
                            }
                            Err(e) => {
                                last_err = e;
                                attempt += 1;
                            }
                        }
                    }
                }
            };

            info!(
                "📋 [STARTUP] Using LOCAL Go epoch {} (skipping peer discovery to prevent deadlock)",
                epoch
            );
            (
                epoch,
                latest_block_number,
                config.executor_receive_socket_path.clone(),
            )
        };

        info!(
            "📊 [STARTUP] Go State: Block {}, Epoch {} (peer_block={})",
            latest_block_number, go_epoch, peer_last_block
        );

        // CATCHUP: Check if we need to sync epoch from local storage
        let storage_path = config.storage_path.clone();
        let local_epoch = detect_local_epoch(&storage_path);

        let current_epoch = if local_epoch < go_epoch {
            warn!(
                "🔄 [CATCHUP] Epoch mismatch detected: local={}, go={}. Syncing to epoch {}.",
                local_epoch, go_epoch, go_epoch
            );
            if config.epochs_to_keep > 0 {
                // Smart cleanup: only delete epochs older than epochs_to_keep
                // Keep recent epochs so THIS node can serve historical data to lagging peers
                let keep_from = go_epoch.saturating_sub(config.epochs_to_keep as u64);
                let epochs_dir = storage_path.join("epochs");
                if epochs_dir.exists() {
                    if let Ok(entries) = std::fs::read_dir(&epochs_dir) {
                        for entry in entries.flatten() {
                            if let Some(name) = entry.file_name().to_str() {
                                if let Some(epoch_str) = name.strip_prefix("epoch_") {
                                    if let Ok(epoch) = epoch_str.parse::<u64>() {
                                        if epoch < keep_from {
                                            info!("🗑️ [CATCHUP] Removing old epoch {} data (older than keep_from={})", epoch, keep_from);
                                            let _ = std::fs::remove_dir_all(entry.path());
                                        } else {
                                            info!("📦 [CATCHUP] Keeping epoch {} data for sync support", epoch);
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            } else {
                info!("📦 [CATCHUP] Archive mode (epochs_to_keep=0): keeping all epoch data");
            }
            go_epoch
        } else if local_epoch > go_epoch {
            warn!(
                "🚨 [CATCHUP] Local epoch {} is AHEAD of network epoch {}! Detect stale chain.",
                local_epoch, go_epoch
            );
            warn!("🗑️ [CATCHUP] Clearing ALL local epochs to resync with network.");
            if let Ok(entries) = std::fs::read_dir(storage_path.join("epochs")) {
                for entry in entries.flatten() {
                    if let Ok(path) = entry.path().canonicalize() {
                        info!("🗑️ [CATCHUP] Removing {:?}", path);
                        let _ = std::fs::remove_dir_all(path);
                    }
                }
            }
            go_epoch
        } else {
            go_epoch
        };

        info!(
            "📊 [STARTUP] Using epoch {} (synced with Go)",
            current_epoch
        );

        // Fetch committee from the best Go Master source
        let peer_executor_client = if best_socket != config.executor_receive_socket_path {
            info!(
                "🔄 [PEER SYNC] Using peer Go Master {} for validators (has correct epoch {})",
                best_socket, go_epoch
            );
            Arc::new(ExecutorClient::new(
                true,
                false,
                String::new(),
                best_socket.clone(),
                None,
            ))
        } else {
            executor_client.clone()
        };

        let (
            current_epoch,
            epoch_timestamp_ms,
            boundary_block,
            validators,
            epoch_duration_from_go,
            boundary_gei,
        ) = match peer_executor_client
            .get_epoch_boundary_data(current_epoch)
            .await
        {
            Ok((epoch, timestamp, boundary_blk, vals, epoch_dur, boundary_gei_val)) => {
                info!(
                    "✅ [STARTUP] Got epoch boundary data for epoch {} from Go (epoch_duration={}s, boundary_gei={})",
                    epoch, epoch_dur, boundary_gei_val
                );
                (
                    epoch,
                    timestamp,
                    boundary_blk,
                    vals,
                    epoch_dur,
                    boundary_gei_val,
                )
            }
            Err(e) => {
                warn!(
                    "⚠️ [STARTUP] Failed to get epoch boundary for epoch {}: {}. Trying fallbacks...",
                    current_epoch, e
                );

                // SNAPSHOT RESTORE FIX (2026-03-19):
                // After snapshot restore, Go may have stale epoch data (epoch=0) while
                // peers are at epoch N. Instead of falling back to local Go's stale epoch,
                // query peers FIRST for epoch boundary data.
                let local_epoch = executor_client.get_current_epoch().await.unwrap_or(0);

                if local_epoch < current_epoch && !config.peer_rpc_addresses.is_empty() {
                    warn!(
                        "🔄 [STARTUP] Local Go epoch {} < peer epoch {}. Go may have stale data (snapshot restore?). Querying peers for epoch boundary...",
                        local_epoch, current_epoch
                    );

                    // Try each peer for epoch boundary data at the CORRECT (peer) epoch
                    let mut peer_boundary = None;
                    for peer_addr in &config.peer_rpc_addresses {
                        match crate::network::peer_rpc::query_peer_epoch_boundary_data(
                            peer_addr,
                            current_epoch,
                        )
                        .await
                        {
                            Ok(boundary) => {
                                info!(
                                    "✅ [STARTUP] Got epoch {} boundary from peer {}: {} validators, boundary_block={}, boundary_gei={}",
                                    current_epoch, peer_addr, boundary.validators.len(), boundary.boundary_block, boundary.boundary_gei
                                );
                                peer_boundary = Some(boundary);
                                break;
                            }
                            Err(pe) => {
                                warn!(
                                    "⚠️ [STARTUP] Peer {} epoch {} boundary failed: {}",
                                    peer_addr, current_epoch, pe
                                );
                            }
                        }
                    }

                    if let Some(boundary) = peer_boundary {
                        use super::executor_client::proto::ValidatorInfo as ProtoVI;
                        let validators: Vec<ProtoVI> = boundary
                            .validators
                            .into_iter()
                            .map(|v| ProtoVI {
                                address: v.address,
                                stake: v.stake.to_string(),
                                name: v.name,
                                authority_key: v.authority_key,
                                protocol_key: v.protocol_key,
                                network_key: v.network_key,
                                description: String::new(),
                                website: String::new(),
                                image: String::new(),
                                commission_rate: 0,
                                min_self_delegation: String::new(),
                                accumulated_rewards_per_share: String::new(),
                                p2p_address: String::new(),
                            })
                            .collect();
                        (
                            current_epoch,
                            boundary.timestamp_ms,
                            boundary.boundary_block,
                            validators,
                            900u64,
                            boundary.boundary_gei,
                        )
                    } else {
                        warn!("⚠️ [STARTUP] No peers returned epoch {} boundary. Falling back to local Go epoch {}.", current_epoch, local_epoch);
                        // Fall through to local Go fallback below
                        match executor_client.get_epoch_boundary_data(local_epoch).await {
                            Ok((
                                epoch,
                                timestamp,
                                boundary_blk,
                                vals,
                                epoch_dur,
                                boundary_gei_val,
                            )) => (
                                epoch,
                                timestamp,
                                boundary_blk,
                                vals,
                                epoch_dur,
                                boundary_gei_val,
                            ),
                            Err(e2) => {
                                return Err(anyhow::anyhow!(
                                    "Failed to get epoch boundary from peers AND local Go. Peer epoch={} error: {}, Local epoch={} error: {}",
                                    current_epoch, e, local_epoch, e2
                                ));
                            }
                        }
                    }
                } else {
                    // Local epoch matches or no peers — use local Go
                    info!(
                        "📊 [STARTUP] Using local Go epoch {} for boundary data",
                        local_epoch
                    );

                    match executor_client.get_epoch_boundary_data(local_epoch).await {
                        Ok((epoch, timestamp, boundary_blk, vals, epoch_dur, boundary_gei_val)) => {
                            info!(
                                "✅ [STARTUP] Got epoch boundary data for local epoch {} (epoch_duration={}s, boundary_gei={})",
                                epoch, epoch_dur, boundary_gei_val
                            );
                            (
                                epoch,
                                timestamp,
                                boundary_blk,
                                vals,
                                epoch_dur,
                                boundary_gei_val,
                            )
                        }
                        Err(e2) => {
                            warn!(
                                "⚠️ [STARTUP] No epoch boundary available (local epoch {} error: {}). Trying genesis validators...",
                                local_epoch, e2
                            );
                            // Try local Go first
                            match executor_client.get_validators_at_block(0).await {
                                Ok((genesis_validators, _genesis_epoch, _)) => {
                                    (0u64, 0u64, 0u64, genesis_validators, 900u64, 0u64)
                                }
                                Err(e3) => {
                                    // LOCAL GO FAILED — query peers for epoch 0
                                    warn!(
                                        "⚠️ [STARTUP] Local Go genesis validators failed: {}. Querying peers...",
                                        e3
                                    );
                                    if !config.peer_rpc_addresses.is_empty() {
                                        let mut peer_validators = None;
                                        for peer_addr in &config.peer_rpc_addresses {
                                            match crate::network::peer_rpc::query_peer_epoch_boundary_data(
                                                peer_addr, 0,
                                            ).await {
                                                Ok(boundary) => {
                                                    info!(
                                                        "✅ [STARTUP] Got epoch 0 boundary from peer {}: {} validators",
                                                        peer_addr, boundary.validators.len()
                                                    );
                                                    peer_validators = Some(boundary);
                                                    break;
                                                }
                                                Err(pe) => {
                                                    warn!("⚠️ [STARTUP] Peer {} epoch 0 boundary failed: {}", peer_addr, pe);
                                                }
                                            }
                                        }
                                        if let Some(boundary) = peer_validators {
                                            use super::executor_client::proto::ValidatorInfo as ProtoVI;
                                            let validators: Vec<ProtoVI> = boundary
                                                .validators
                                                .into_iter()
                                                .map(|v| ProtoVI {
                                                    address: v.address,
                                                    stake: v.stake.to_string(),
                                                    name: v.name,
                                                    authority_key: v.authority_key,
                                                    protocol_key: v.protocol_key,
                                                    network_key: v.network_key,
                                                    description: String::new(),
                                                    website: String::new(),
                                                    image: String::new(),
                                                    commission_rate: 0,
                                                    min_self_delegation: String::new(),
                                                    accumulated_rewards_per_share: String::new(),
                                                    p2p_address: String::new(),
                                                })
                                                .collect();
                                            (
                                                0u64,
                                                boundary.timestamp_ms,
                                                boundary.boundary_block,
                                                validators,
                                                900u64,
                                                boundary.boundary_gei,
                                            )
                                        } else {
                                            return Err(anyhow::anyhow!(
                                                "Failed to fetch genesis validators from both local Go and peers. Local: {}, No peers returned data.",
                                                e3
                                            ));
                                        }
                                    } else {
                                        return Err(anyhow::anyhow!(
                                            "Failed to fetch genesis validators: {} (no peers configured for fallback)",
                                            e3
                                        ));
                                    }
                                }
                            }
                        }
                    }
                }
            }
        };

        info!(
            "📊 [STARTUP] Using epoch boundary data: epoch={}, boundary_block={}, epoch_timestamp={}ms, validators={}, boundary_gei={}",
            current_epoch, boundary_block, epoch_timestamp_ms, validators.len(), boundary_gei
        );

        if validators.is_empty() {
            anyhow::bail!("Go state returned empty validators list at epoch boundary");
        }

        // Filter validators for single node debug if needed
        let validators_to_use = if std::env::var("SINGLE_NODE_DEBUG").is_ok() {
            info!("🔧 SINGLE_NODE_DEBUG: Using only node 0");
            validators
                .into_iter()
                .filter(|v| v.name == "node-0")
                .collect::<Vec<_>>()
        } else {
            validators
        };

        let (committee, validator_eth_addresses) =
            super::committee::build_committee_with_eth_addresses(validators_to_use, current_epoch)?;

        // ═══════════════════════════════════════════════════════════════════════════
        // COMMITTEE VERIFICATION: Compute deterministic hash and validate with peers.
        //
        // The committee determines leader election → LeaderAddress in block header.
        // If ANY node uses a DIFFERENT committee, it will produce different leader
        // addresses → different block hashes → FORK.
        //
        // CRITICAL: After snapshot restore, local Go may return stale validators
        // from a previous epoch. The committee hash catches this early.
        // ═══════════════════════════════════════════════════════════════════════════
        let committee_hash = super::committee_source::calculate_committee_hash(&committee);
        let committee_hash_hex = hex::encode(&committee_hash[..8]);
        info!(
            "✅ Loaded committee with {} authorities and {} eth_addresses (from epoch boundary). \
             Committee hash={}... (epoch {})",
            committee.size(),
            validator_eth_addresses.len(),
            committee_hash_hex,
            current_epoch
        );

        // Log each validator's ETH address for forensic verification across nodes
        for (idx, eth_addr) in validator_eth_addresses.iter().enumerate() {
            if eth_addr.len() == 20 {
                info!(
                    "  📋 [COMMITTEE] Validator {}: ETH=0x{}",
                    idx, hex::encode(eth_addr)
                );
            } else {
                warn!(
                    "  ⚠️ [COMMITTEE] Validator {}: INVALID ETH address ({} bytes)",
                    idx, eth_addr.len()
                );
            }
        }

        // Cross-validate committee with peers during cold-start
        // (empty DAG indicates restore — committee may be stale from local Go)
        let epoch_db_path_for_committee_check = config
            .storage_path
            .join("epochs")
            .join(format!("epoch_{}", current_epoch))
            .join("consensus_db");
        let dag_has_history_for_committee = epoch_db_path_for_committee_check.exists()
            && std::fs::read_dir(&epoch_db_path_for_committee_check)
                .map(|mut entries| entries.next().is_some())
                .unwrap_or(false);

        if !dag_has_history_for_committee && !config.peer_rpc_addresses.is_empty() && current_epoch > 1 {
            // NOTE: Skipped for epoch ≤ 1 (genesis). On fresh start, ALL nodes
            // build committee from genesis.json — guaranteed identical. Querying
            // peers for epoch_boundary_data would livelock because no peer has
            // processed any epoch transitions yet.
            info!(
                "🔍 [LAYER-7 COMMITTEE VERIFY] Cold-start detected (epoch {}). Cross-validating committee hash with ALL peers...",
                current_epoch
            );

            // ═══════════════════════════════════════════════════════════════
            // ABSOLUTE FORK-FREE COMMITTEE VERIFICATION
            //
            // RULES:
            // 1. Mismatch with ANY peer → HALT (local state is wrong).
            // 2. ≥1 peer matches, 0 mismatches → ACCEPT (confirmed).
            // 3. 0 matches, 0 mismatches (all unreachable) → RETRY forever.
            //    System waits indefinitely. This is intentional: waiting is
            //    ALWAYS safer than starting with an unverified committee.
            //
            // CLUSTER COLD-START (Seed):
            // When ALL nodes restart simultaneously, the FIRST node to start
            // will loop here until at least 1 peer comes online. Once any
            // peer is reachable, they cross-verify and both proceed.
            // This acts as a natural "seed" — no special bootstrap needed.
            //
            // LIVENESS GUARANTEE: With ≥2f+1 nodes online, at least 1 peer
            // will eventually respond, breaking the retry loop.
            // ═══════════════════════════════════════════════════════════════
            let total_peers = config.peer_rpc_addresses.len();
            const PER_PEER_TIMEOUT_SECS: u64 = 5;
            const RETRY_INTERVAL_SECS: u64 = 10;
            let mut attempt = 0u32;

            loop {
                attempt += 1;
                let mut matching_peers = 0u32;
                let mut mismatching_peers = 0u32;
                let mut unreachable_peers = 0u32;

                let round_start = std::time::Instant::now();

                for peer_addr in &config.peer_rpc_addresses {
                    // Per-peer timeout using tokio::time::timeout
                    let peer_result = tokio::time::timeout(
                        std::time::Duration::from_secs(PER_PEER_TIMEOUT_SECS),
                        crate::network::peer_rpc::query_peer_epoch_boundary_data(
                            peer_addr,
                            current_epoch,
                        )
                    ).await;

                    match peer_result {
                        Ok(Ok(boundary)) if !boundary.validators.is_empty() => {
                            // Build peer committee to compute hash
                            use super::executor_client::proto::ValidatorInfo as ProtoVI;
                            let peer_validators: Vec<ProtoVI> = boundary
                                .validators
                                .into_iter()
                                .map(|v| ProtoVI {
                                    address: v.address,
                                    stake: v.stake.to_string(),
                                    name: v.name,
                                    authority_key: v.authority_key,
                                    protocol_key: v.protocol_key,
                                    network_key: v.network_key,
                                    description: String::new(),
                                    website: String::new(),
                                    image: String::new(),
                                    commission_rate: 0,
                                    min_self_delegation: String::new(),
                                    accumulated_rewards_per_share: String::new(),
                                    p2p_address: String::new(),
                                })
                                .collect();

                            match super::committee::build_committee_from_validator_list(
                                peer_validators,
                                current_epoch,
                            ) {
                                Ok(peer_committee) => {
                                    let peer_hash =
                                        super::committee_source::calculate_committee_hash(
                                            &peer_committee,
                                        );
                                    let peer_hash_hex = hex::encode(&peer_hash[..8]);

                                    if committee_hash == peer_hash {
                                        matching_peers += 1;
                                        info!(
                                            "Arc::new: Peer {} committee MATCH (hash={}...) [{}/{}] attempt={}",
                                            peer_addr, peer_hash_hex, matching_peers, total_peers, attempt
                                        );
                                    } else {
                                        mismatching_peers += 1;
                                        warn!(
                                            "🚨 [LAYER-7] Peer {} committee MISMATCH! local={}... ≠ peer={}... attempt={}",
                                            peer_addr, committee_hash_hex, peer_hash_hex, attempt
                                        );
                                    }
                                }
                                Err(e) => {
                                    unreachable_peers += 1;
                                    warn!(
                                        "⚠️ [LAYER-7] Failed to build peer committee from {}: {}",
                                        peer_addr, e
                                    );
                                }
                            }
                        }
                        Ok(Ok(_)) => {
                            unreachable_peers += 1;
                            // Don't log every attempt for empty validators — reduce noise
                            if attempt <= 3 || attempt % 10 == 0 {
                                warn!("⚠️ [LAYER-7] Peer {} returned empty validators (attempt={})", peer_addr, attempt);
                            }
                        }
                        Ok(Err(e)) => {
                            unreachable_peers += 1;
                            if attempt <= 3 || attempt % 10 == 0 {
                                warn!("⚠️ [LAYER-7] Failed to query peer {} (attempt={}): {}", peer_addr, attempt, e);
                            }
                        }
                        Err(_) => {
                            unreachable_peers += 1;
                            if attempt <= 3 || attempt % 10 == 0 {
                                warn!(
                                    "⚠️ [LAYER-7] Peer {} timed out after {}s (attempt={})",
                                    peer_addr, PER_PEER_TIMEOUT_SECS, attempt
                                );
                            }
                        }
                    }

                    // Early exit on mismatch — no point checking remaining peers
                    if mismatching_peers > 0 {
                        break;
                    }
                }

                let elapsed = round_start.elapsed().as_secs();

                if mismatching_peers > 0 {
                    // HALT IMMEDIATELY — local committee is wrong
                    let err_msg = format!(
                        "🚨 [LAYER-7] Committee hash MISMATCH with {} peer(s)! \
                         match={}, mismatch={}, unreachable={} (epoch {}, attempt={}, {}s). \
                         HALTING NODE to prevent fork. Wipe local DB or verify snapshot.",
                        mismatching_peers, matching_peers, mismatching_peers,
                        unreachable_peers, current_epoch, attempt, elapsed
                    );
                    tracing::error!("{}", err_msg);
                    anyhow::bail!(err_msg);
                }

                if matching_peers > 0 {
                    // ≥1 peer confirmed, 0 mismatches → SAFE to proceed
                    info!(
                        "✅ [LAYER-7] Committee verified: {}/{} peers match, 0 mismatch. \
                         ACCEPTED after {} attempt(s). ({}s)",
                        matching_peers, total_peers, attempt, elapsed
                    );
                    break; // Exit retry loop
                }

                // 0 matches, 0 mismatches — ALL peers unreachable
                // RETRY: Wait and try again. Do NOT accept without verification.
                warn!(
                    "⏳ [LAYER-7] No peers reachable for committee verification (attempt={}). \
                     unreachable={}/{} (epoch {}). Retrying in {}s... \
                     System will wait here until at least 1 peer confirms committee hash.",
                    attempt, unreachable_peers, total_peers, current_epoch, RETRY_INTERVAL_SECS
                );

                tokio::time::sleep(std::time::Duration::from_secs(RETRY_INTERVAL_SECS)).await;
            }
        } else if !dag_has_history_for_committee && current_epoch <= 1 {
            info!(
                "✅ [LAYER-7 COMMITTEE VERIFY] Genesis epoch {} — committee from genesis.json, no peer verification needed.",
                current_epoch
            );
        }

        // EXECUTION INDEX SYNC
        let (_local_go_block, last_global_exec_index, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms) = Self::calculate_last_global_exec_index(
            config,
            &executor_client,
            &best_socket,
            peer_last_block,
        )
        .await;

        if last_global_exec_index > 100000 {
            warn!(
                "⚠️ [STARTUP] Very high last_global_exec_index={} - this is normal for long-running chains. Trusting Go's value.",
                last_global_exec_index
            );
        }

        // ═══════════════════════════════════════════════════════════════════════════
        // FORK-SAFETY: Validate boundary_gei before using it as epoch_base_index.
        // After snapshot restore, Go's epoch_data_backup.json may have boundary_gei=0
        // for epoch>0 because the epoch transition wasn't captured in the snapshot.
        // Using boundary_gei=0 causes wrong GEI calculation → block hash divergence.
        //
        // CRITICAL FIX (2026-04-15): Also validate when DAG storage is empty.
        // After snapshot restore, Go may have non-zero but STALE boundary_gei that doesn't
        // match the network. This causes epoch_base_index to be wrong → global_exec_index
        // calculation diverges → FORK.
        //
        // Fix: Validate from peers if:
        //   1. boundary_gei == 0 && epoch > 0 (original fix), OR
        //   2. DAG storage is empty - indicates snapshot restore
        // ═══════════════════════════════════════════════════════════════════════════
        let epoch_db_path = config
            .storage_path
            .join("epochs")
            .join(format!("epoch_{}", current_epoch))
            .join("consensus_db");
        let dag_has_history = epoch_db_path.exists()
            && std::fs::read_dir(&epoch_db_path)
                .map(|mut entries| entries.next().is_some())
                .unwrap_or(false);

        let epoch_base_exec_index = if (boundary_gei == 0 && current_epoch > 0)
            || (!dag_has_history && current_epoch > 0)
        {
            let force_peer_check = !dag_has_history && current_epoch > 0;
            if force_peer_check {
                warn!(
                    "⚠️ [FORK-SAFETY] Cold-start detected (empty DAG). Validating boundary_gei={} from peers for epoch {}...",
                    boundary_gei, current_epoch
                );
            }
            let (_, _, _, _, _, safe_gei) = executor_client
                .get_safe_epoch_boundary_data_with_force(
                    current_epoch,
                    &config.peer_rpc_addresses,
                    force_peer_check,
                )
                .await?;
            if safe_gei != boundary_gei {
                warn!(
                    "🔄 [FORK-SAFETY] Corrected boundary_gei: {} → {} (from peers)",
                    boundary_gei, safe_gei
                );
            }
            safe_gei
        } else {
            boundary_gei
        };
        info!(
            "✅ [STARTUP] Using epoch_base={} from Go boundary_gei (epoch={}, boundary_block={})",
            epoch_base_exec_index, current_epoch, boundary_block
        );

        // Initialize executor client memory state from Go Master before WAL recovery replay
        if config.executor_read_enabled {
            info!("📊 [STARTUP] Synchronizing executor client memory state from Go Master prior to recovery check...");
            executor_client.initialize_from_go().await;
            info!("✅ [STARTUP] Executor client memory state synchronized successfully (block/GEI guards updated).");
        }

        // Recovery check
        if config.executor_read_enabled && last_global_exec_index > 0 {
            if let Err(e) = super::recovery::perform_block_recovery_check(
                &executor_client,
                last_global_exec_index,
                epoch_base_exec_index,
                current_epoch,
                &epoch_db_path,
                config.node_id as u32,
            )
            .await {
                warn!("⚠️ [STARTUP MINOR] Block recovery check paused (this is normal during cold-start or snapshot restore): {}", e);
            }
        }

        let protocol_keypair = config.load_protocol_keypair()?;
        let network_keypair = config.load_network_keypair()?;

        // Identity: find own index in committee
        let own_protocol_pubkey = protocol_keypair.public();
        let own_index_opt = committee.authorities().find_map(|(idx, auth)| {
            if auth.protocol_key == own_protocol_pubkey {
                Some(idx)
            } else {
                None
            }
        });

        let is_in_committee = own_index_opt.is_some();
        let own_index = own_index_opt.unwrap_or(AuthorityIndex::ZERO);

        if is_in_committee {
            info!(
                "✅ [IDENTITY] Found self in committee at index {} using protocol_key match",
                own_index
            );
        } else {
            info!(
                "ℹ️ [IDENTITY] Not in committee (protocol_key not found in {} authorities)",
                committee.size()
            );
        }

        std::fs::create_dir_all(&config.storage_path)?;

        Ok(StorageSetup {
            current_epoch,
            epoch_timestamp_ms,
            committee,
            validator_eth_addresses,
            own_index,
            is_in_committee,
            last_global_exec_index,
            epoch_base_exec_index,
            storage_path,
            protocol_keypair,
            network_keypair,
            epoch_duration_from_go,
            last_executed_commit_hash,
            latest_block_number,
            last_handled_commit_index,
            last_block_timestamp_ms,
        })
    }

    /// Determines the effective last global execution index and commit hash from local Go, peers, and persisted state.
    async fn calculate_last_global_exec_index(
        config: &NodeConfig,
        executor_client: &Arc<ExecutorClient>,
        best_socket: &str,
        peer_last_block: u64,
    ) -> (u64, u64, [u8; 32], Option<u32>, u64) {
        if !config.executor_read_enabled {
            return (0, 0, [0; 32], None, 0);
        }

        let (local_go_block, local_go_gei, _go_ready, last_executed_commit_hash) = loop {
            match executor_client.get_last_block_number().await {
                Ok((block, gei, true, hash, _)) => break (block, gei, true, hash),
                Ok((block, gei, false, _hash, _)) => {
                    warn!(
                        "⏳ [STARTUP] Go Master not ready (block={}, gei={}). Retrying in 1s...",
                        block, gei
                    );
                    tokio::time::sleep(tokio::time::Duration::from_secs(1)).await;
                }
                Err(e) => {
                    warn!(
                        "⚠️ [STARTUP] Failed to get last block from Go: {}. Using defaults.",
                        e
                    );
                    break (0, 0, false, [0; 32]);
                }
            }
        };

        let (last_handled_commit_index, last_block_timestamp_ms) = match executor_client.get_last_handled_commit_index().await {
            Ok((commit_index, _, _, _, _, ts, state_root)) => {
                let state_root_hex = hex::encode(&state_root);
                tracing::info!("🔑 [GO-AUTH GEI] Post-init query: state_root=0x{}", state_root_hex);
                (Some(commit_index), ts)
            },
            Err(e) => {
                warn!("⚠️ [STARTUP] Failed to get last_handled_commit_index from Go: {}", e);
                (None, 0)
            }
        };

        let storage_path = &config.storage_path;

        let (persisted_index, persisted_commit) =
            super::executor_client::load_persisted_last_index(storage_path).unwrap_or((0, 0));

        let peer_last_block =
            if best_socket != config.executor_receive_socket_path && peer_last_block > 0 {
                peer_last_block
            } else {
                0
            };

        if peer_last_block > 0 {
            info!(
                "📊 [STARTUP] Sync Check: LocalGoBlock={}, PeerBlock={}, PersistedGEI=({}, commit={}) (from {})",
                local_go_block, peer_last_block, persisted_index, persisted_commit, best_socket
            );

            let sources_match =
                local_go_block == peer_last_block || local_go_block.abs_diff(peer_last_block) <= 5;
            if !sources_match {
                warn!("⚠️ [STARTUP] INDEX DISCREPANCY DETECTED:");
                warn!(
                    "   LocalGoBlock={}, PeerBlock={}, PersistedGEI={}, LocalGEI={}",
                    local_go_block, peer_last_block, persisted_index, local_go_gei
                );
                warn!("   This may indicate network partition or stale data.");
            }

            if local_go_block > peer_last_block + 5 {
                warn!("🚨 [STARTUP] STALE CHAIN DETECTED: Local ({}) is ahead of Peer ({})! Forcing resync from Peer.", 
                       local_go_block, peer_last_block);
                // In recovery we just use the local GEI anyway because Go Master blocks handles actual rollback if needed
                (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
            } else if local_go_block < peer_last_block.saturating_sub(5) {
                let lag = peer_last_block - local_go_block;
                info!(
                    "ℹ️ [STARTUP] Local Go Master ({}) is behind Peer ({}) by {} blocks. Using Local {} to trigger recovery/backfill.",
                    local_go_block, peer_last_block, lag, local_go_block
                );
                // Flag as lagging if behind by more than 50 blocks
                (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
            } else {
                info!(
                    "✅ [STARTUP] Local and Peer are in sync (LocalBlock={}, PeerBlock={}). Using Local Go GEI: {} as authoritative.",
                    local_go_block, peer_last_block, local_go_gei
                );
                (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
            }
        } else {
            if persisted_index > local_go_gei {
                warn!("⚠️ [STARTUP] Persisted Index (GEI) {} > Local Go GEI {}. Go is behind (possible rollback/crash). Using Local Go GEI {} to force resync/replay.", 
                    persisted_index, local_go_gei, local_go_gei);
            }
            info!(
                "📊 [STARTUP] No peer reference, using Local Go Last GEI: {} (Block: {})",
                local_go_gei, local_go_block
            );
            (local_go_block, local_go_gei, last_executed_commit_hash, last_handled_commit_index, last_block_timestamp_ms)
        }
    }
}
