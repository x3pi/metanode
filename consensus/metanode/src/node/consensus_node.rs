// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! ConsensusNode constructors.
//!
//! Contains the struct definition and all `new*` constructors for ConsensusNode.
//! The main constructor delegates to 4 helper functions:
//! - `setup_storage()` — executor, epoch discovery, committee, execution index, identity
//! - `setup_consensus()` — commit processor, params, authority start
//! - `setup_networking()` — NTP/clock sync
//! - `setup_epoch_management()` — transition manager, monitor, sync task

use crate::consensus::clock_sync::ClockSyncManager;
use crate::types::transaction::NoopTransactionVerifier;
use anyhow::Result;
use consensus_config::AuthorityIndex;
use consensus_core::{
    Clock, CommitConsumerArgs, ConsensusAuthority, DefaultSystemTransactionProvider, NetworkType,
    ReconfigState, SystemTransactionProvider,
};
use meta_protocol_config::ProtocolConfig;
use prometheus::Registry;
use std::sync::atomic::AtomicU32;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::RwLock;
use tracing::{info, warn};

use crate::config::NodeConfig;
use crate::node::epoch_store::load_legacy_epoch_stores;
use crate::node::executor_client::ExecutorClient;
use crate::node::tx_submitter::TransactionClientProxy;

use super::{detect_local_epoch, ConsensusNode, NodeMode};
use super::{epoch_monitor, epoch_transition_manager, recovery};

// ---------------------------------------------------------------------------
// Intermediate structs for passing data between setup phases
// ---------------------------------------------------------------------------

/// Results from storage/epoch initialization phase.
struct StorageSetup {
    current_epoch: u64,
    epoch_timestamp_ms: u64,
    committee: consensus_config::Committee,
    validator_eth_addresses: Vec<Vec<u8>>,
    own_index: AuthorityIndex,
    is_in_committee: bool,
    last_global_exec_index: u64,
    epoch_base_exec_index: u64,
    storage_path: std::path::PathBuf,
    protocol_keypair: consensus_config::ProtocolKeyPair,
    network_keypair: consensus_config::NetworkKeyPair,
    /// Epoch duration in seconds, loaded from Go via protobuf (from genesis.json)
    epoch_duration_from_go: u64,
    last_executed_commit_hash: [u8; 32],
    /// Last block number from Go at startup, verified with is_ready=true retry loop.
    /// Passed to setup_consensus to avoid re-query race where Go returns stale value.
    latest_block_number: u64,
}

/// Results from consensus setup phase.
struct ConsensusSetup {
    authority: Option<ConsensusAuthority>,
    /// Whether DAG storage has prior history. False after snapshot restore (DAG deleted).
    #[allow(dead_code)]
    dag_has_history: bool,
    /// Critical health flag. Set to true if any background Station crashes.
    is_terminally_failed: Arc<AtomicBool>,
    commit_consumer_holder: Option<CommitConsumerArgs>,
    transaction_client_proxy: Option<Arc<TransactionClientProxy>>,
    executor_client_for_proc: Arc<ExecutorClient>,
    current_commit_index: Arc<AtomicU32>,
    pending_transactions_queue: Arc<tokio::sync::Mutex<Vec<Vec<u8>>>>,
    committed_transaction_hashes: Arc<tokio::sync::Mutex<std::collections::HashSet<Vec<u8>>>>,
    epoch_tx_sender: tokio::sync::mpsc::UnboundedSender<(u64, u64, u64)>,
    epoch_tx_receiver: tokio::sync::mpsc::UnboundedReceiver<(u64, u64, u64)>,
    system_transaction_provider: Arc<DefaultSystemTransactionProvider>,
    protocol_config: ProtocolConfig,
    parameters: consensus_config::Parameters,
    clock: Arc<Clock>,
    transaction_verifier: Arc<NoopTransactionVerifier>,
    /// TX recycler for tracking and re-submitting uncommitted TXs
    tx_recycler: Arc<crate::consensus::tx_recycler::TxRecycler>,
}

// ---------------------------------------------------------------------------
// Constructor entry points
// ---------------------------------------------------------------------------

impl ConsensusNode {
    #[allow(dead_code)]
    pub async fn new(config: NodeConfig) -> Result<Self> {
        Self::new_with_registry(config, Registry::new()).await
    }

    #[allow(dead_code)]
    pub async fn new_with_registry(config: NodeConfig, registry: Registry) -> Result<Self> {
        Self::new_with_registry_and_service(config, registry).await
    }

    /// Main constructor — delegates to 4 setup helpers.
    pub async fn new_with_registry_and_service(
        config: NodeConfig,
        registry: Registry,
    ) -> Result<Self> {
        info!("Initializing consensus node {}...", config.node_id);

        // Phase 1: Storage, epoch discovery, committee, execution index, identity
        let storage = Self::setup_storage(&config).await?;

        let coordination_hub = consensus_core::coordination_hub::ConsensusCoordinationHub::new();

        // Phase 2: Consensus params, commit processor, authority start
        let consensus = Self::setup_consensus(&config, &storage, &registry, coordination_hub.clone()).await?;

        // Phase 3: Clock/NTP sync
        let clock_sync_manager = Self::setup_networking(&config);

        // Phase 4: Assemble node + epoch management (transition manager, monitor, sync)
        let node =
            Self::setup_epoch_management(config, storage, consensus, clock_sync_manager, &registry, coordination_hub)
                .await?;

        Ok(node)
    }

    // -----------------------------------------------------------------------
    // Phase 1: setup_storage
    // -----------------------------------------------------------------------

    /// Initializes executor client, discovers epoch from peers/Go, loads committee,
    /// calculates execution index, finds own identity in the committee.
    async fn setup_storage(config: &NodeConfig) -> Result<StorageSetup> {
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
            let max_retries = 30;
            let retry_interval = std::time::Duration::from_millis(500);
            let mut block_num = 0u64;

            for attempt in 1..=max_retries {
                match executor_client.get_last_block_number().await {
                    Ok((n, _, true, _, _)) => {
                        block_num = n;
                        info!(
                            "✅ [STARTUP] Got block number {} from Go (is_ready=true) (attempt {})",
                            n, attempt
                        );
                        break;
                    }
                    Ok((n, _, false, _, _)) => {
                        info!(
                            "⏳ [STARTUP] Go not ready (block={}) (attempt {}/{}). Waiting for blockchain init...",
                            n, attempt, max_retries
                        );
                        if attempt < max_retries {
                            tokio::time::sleep(retry_interval).await;
                        }
                    }
                    Err(e) => {
                        if attempt < max_retries {
                            warn!(
                                "⚠️ [STARTUP] Failed to fetch block from Go (attempt {}/{}): {}. Retrying...",
                                attempt, max_retries, e
                            );
                            tokio::time::sleep(retry_interval).await;
                        } else {
                            warn!("⚠️ [STARTUP] Failed to fetch latest block from Go after {} attempts: {}. Attempting to read persisted value.", max_retries, e);
                            block_num = super::executor_client::read_last_block_number(
                                &config.storage_path,
                            )
                            .await
                            .unwrap_or(0);
                        }
                    }
                }
            }

            if block_num == 0 {
                warn!("⚠️ [STARTUP] Go still reporting block=0 after {} retries. This may be a fresh node or Go failed to load snapshot data.", max_retries);
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
                    // Retry a few times for transient RPC errors only
                    let max_retries = 5;
                    let retry_interval = std::time::Duration::from_millis(500);
                    let mut final_epoch = 0u64;
                    let mut last_err = e;

                    for attempt in 2..=max_retries {
                        warn!(
                            "⚠️ [STARTUP] Failed to get epoch (attempt {}/{}): {}. Retrying...",
                            attempt, max_retries, last_err
                        );
                        tokio::time::sleep(retry_interval).await;
                        match executor_client.get_current_epoch().await {
                            Ok(e) => {
                                info!(
                                    "✅ [STARTUP] Got epoch {} from Go (attempt {}, block={})",
                                    e, attempt, latest_block_number
                                );
                                final_epoch = e;
                                last_err = anyhow::anyhow!("resolved");
                                break;
                            }
                            Err(e) => {
                                last_err = e;
                            }
                        }
                    }
                    if last_err.to_string() != "resolved" {
                        return Err(anyhow::anyhow!(
                            "Failed to fetch epoch from Go after {} attempts: {}",
                            max_retries,
                            last_err
                        ));
                    }
                    final_epoch
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

        if !dag_has_history_for_committee && !config.peer_rpc_addresses.is_empty() {
            info!(
                "🔍 [COMMITTEE VERIFY] Cold-start detected. Cross-validating committee with peers..."
            );
            for peer_addr in &config.peer_rpc_addresses {
                match crate::network::peer_rpc::query_peer_epoch_boundary_data(
                    peer_addr,
                    current_epoch,
                )
                .await
                {
                    Ok(boundary) => {
                        if !boundary.validators.is_empty() {
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
                                        info!(
                                            "✅ [COMMITTEE VERIFY] Peer {} committee matches! hash={}...",
                                            peer_addr, peer_hash_hex
                                        );
                                    } else {
                                        warn!(
                                            "🚨 [COMMITTEE VERIFY] Peer {} committee MISMATCH! \
                                             local={}... ≠ peer={}... (epoch {}). \
                                             This WILL cause block hash divergence!",
                                            peer_addr,
                                            committee_hash_hex,
                                            peer_hash_hex,
                                            current_epoch
                                        );
                                        warn!(
                                            "🚨 [COMMITTEE VERIFY] Local: {} authorities, Peer: {} authorities",
                                            committee.size(), peer_committee.size()
                                        );
                                    }
                                }
                                Err(e) => {
                                    warn!(
                                        "⚠️ [COMMITTEE VERIFY] Failed to build peer committee from {}: {}",
                                        peer_addr, e
                                    );
                                }
                            }
                            break; // Only need one peer to verify
                        }
                    }
                    Err(e) => {
                        warn!(
                            "⚠️ [COMMITTEE VERIFY] Failed to query peer {}: {}",
                            peer_addr, e
                        );
                    }
                }
            }
        }

        // EXECUTION INDEX SYNC
        let (last_global_exec_index, last_executed_commit_hash) = Self::calculate_last_global_exec_index(
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

        // Recovery check
        if config.executor_read_enabled && last_global_exec_index > 0 {
            if let Err(e) = recovery::perform_block_recovery_check(
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
        })
    }

    /// Determines the effective last global execution index and commit hash from local Go, peers, and persisted state.
    async fn calculate_last_global_exec_index(
        config: &NodeConfig,
        executor_client: &Arc<ExecutorClient>,
        best_socket: &str,
        peer_last_block: u64,
    ) -> (u64, [u8; 32]) {
        if !config.executor_read_enabled {
            return (0, [0; 32]);
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
                (local_go_gei, last_executed_commit_hash)
            } else if local_go_block < peer_last_block.saturating_sub(5) {
                let lag = peer_last_block - local_go_block;
                info!(
                    "ℹ️ [STARTUP] Local Go Master ({}) is behind Peer ({}) by {} blocks. Using Local {} to trigger recovery/backfill.",
                    local_go_block, peer_last_block, lag, local_go_block
                );
                // Flag as lagging if behind by more than 50 blocks
                (local_go_gei, last_executed_commit_hash)
            } else {
                info!(
                    "✅ [STARTUP] Local and Peer are in sync (LocalBlock={}, PeerBlock={}). Using Local Go GEI: {} as authoritative.",
                    local_go_block, peer_last_block, local_go_gei
                );
                (local_go_gei, last_executed_commit_hash)
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
            (local_go_gei, last_executed_commit_hash)
        }
    }

    // -----------------------------------------------------------------------
    // Phase 2: setup_consensus
    // -----------------------------------------------------------------------

    /// Builds the commit processor, consensus parameters, starts authority (or SyncOnly holder),
    /// and wires up all the shared state.
    async fn setup_consensus(
        config: &NodeConfig,
        storage: &StorageSetup,
        registry: &Registry,
        coordination_hub: consensus_core::coordination_hub::ConsensusCoordinationHub,
    ) -> Result<ConsensusSetup> {
        let clock = Arc::new(Clock::default());
        let transaction_verifier = Arc::new(NoopTransactionVerifier);
        // ═══════════════════════════════════════════════════════════════
        // FORK-SAFETY FIX v5: Use persisted commit_index, NOT GEI-derived value.
        // GEI includes fragment_offset, so (GEI - epoch_base) is HIGHER than actual commit_index.
        // Using inflated value causes CommitSyncer to PREVENT legitimate commits.
        //
        // CRITICAL: When DAG is wiped (snapshot restore), persistence files are also
        // gone. In that case we MUST set go_replay_after=0 to let the new DAG start
        // from commit 1. The CommitProcessor's AUTO-JUMP and executor's GEI guard
        // will handle skipping already-executed commits in Go.
        // ═══════════════════════════════════════════════════════════════
        // Detect empty DAG (snapshot restore) FIRST - needed for go_replay_after decision
        let dag_has_history = {
            let epoch_db = config
                .storage_path
                .join("epochs")
                .join(format!("epoch_{}", storage.current_epoch))
                .join("consensus_db");
            epoch_db.exists()
                && std::fs::read_dir(&epoch_db)
                    .map(|mut entries| entries.next().is_some())
                    .unwrap_or(false)
        };

        let go_replay_after = if config.executor_read_enabled {
            // Try regular persistence first (best case: normal restart)
            let persisted = crate::node::executor_client::persistence::load_persisted_last_index(&config.storage_path);
            match persisted {
                Some((_gei, commit_index)) if commit_index > 0 => commit_index,
                _ => {
                    // Regular persistence missing. Try WIPE-SAFE location (survives DAG wipes).
                    let wipe_safe = crate::node::executor_client::persistence::load_persisted_last_index_wipe_safe(&config.storage_path);
                    match wipe_safe {
                        Some((_gei, commit_index)) if commit_index > 0 => {
                            info!(
                                "📊 [FORK-SAFETY] Recovered commit_index={} from wipe-safe persistence (DAG wipe recovery)",
                                commit_index
                            );
                            commit_index
                        }
                        _ => {
                            // No persistence at all. If DAG also empty, this is first start.
                            if !dag_has_history {
                                info!(
                                    "📊 [FORK-SAFETY] DAG empty + no persistence = first start or full reset. \
                                     Setting go_replay_after=0 (new DAG starts from commit 1)"
                                );
                                0
                            } else if storage.last_global_exec_index > storage.epoch_base_exec_index {
                                // DAG exists but no persistence file (legacy/first epoch).
                                (storage.last_global_exec_index - storage.epoch_base_exec_index) as u32
                            } else {
                                0
                            }
                        }
                    }
                }
            }
        } else {
            0
        };
        info!(
            "📊 [STARTUP] CommitConsumerArgs: go_replay_after={} (from last_global_exec_index={})",
            go_replay_after, storage.last_global_exec_index
        );
        // Phase 1 Handshake - Retrieve last_executed_commit_hash from Go.
        info!(
            "🤝 [HANDSHAKE] Passing last_executed_commit_hash from Go to Rust DAG: {:?}",
            hex::encode(storage.last_executed_commit_hash)
        );

        let (commit_consumer, commit_receiver, mut block_receiver) =
            CommitConsumerArgs::new(go_replay_after, go_replay_after, storage.last_executed_commit_hash);
        let current_commit_index = Arc::new(AtomicU32::new(0));
        let is_transitioning = coordination_hub.get_is_transitioning_ref();

        // Load persisted transaction queue
        let persisted_queue = super::queue::load_transaction_queue_static(&storage.storage_path)
            .await
            .unwrap_or_default();
        if !persisted_queue.is_empty() {
            info!("💾 Loaded {} persisted transactions", persisted_queue.len());
        }
        let pending_transactions_queue = Arc::new(tokio::sync::Mutex::new(persisted_queue));

        // Load committed transaction hashes from current epoch for duplicate prevention
        let committed_hashes = crate::node::transition::load_committed_transaction_hashes(
            &storage.storage_path,
            storage.current_epoch,
        )
        .await;
        if !committed_hashes.is_empty() {
            info!(
                "💾 Loaded {} committed transaction hashes from epoch {}",
                committed_hashes.len(),
                storage.current_epoch
            );
        }
        let committed_transaction_hashes = Arc::new(tokio::sync::Mutex::new(committed_hashes));

        let (epoch_tx_sender, epoch_tx_receiver) =
            tokio::sync::mpsc::unbounded_channel::<(u64, u64, u64)>();
        let epoch_transition_callback =
            crate::consensus::commit_callbacks::create_epoch_transition_callback(
                epoch_tx_sender.clone(),
            );

        let shared_last_global_exec_index = coordination_hub.get_global_exec_index_ref();
        
        // Initialize GEI in the Hub
        coordination_hub.set_initial_global_exec_index(storage.epoch_base_exec_index).await;

        // ═══════════════════════════════════════════════════════════════════
        // Detect empty DAG (snapshot restore) for startup logging and
        // boundary GEI validation.
        // ═══════════════════════════════════════════════════════════════════
        // (dag_has_history already computed above)
        if !dag_has_history && storage.is_in_committee && storage.current_epoch > 0 {
            warn!(
                "⚠️ [FORK-SAFETY] DAG storage empty for epoch {} — snapshot restore detected. \
                 GEI guard in executor will skip commits Go has already executed.",
                storage.current_epoch
            );
        }

        // Stage 4 Conveyor Belt Buffer: Huge buffer to absorb transaction spikes without halting Core
        let (delivery_tx, delivery_rx) = tokio::sync::mpsc::channel(10000);

        // CRITICAL FORK-SAFETY: Calculate correct next_expected_commit_index from storage state.
        // If not set correctly, CommitProcessor defaults to 1 and AUTO-JUMPs on first commit,
        // causing GEI miscalculation and hash divergence between nodes after restart.
        let next_expected_commit_index = if config.executor_read_enabled && go_replay_after > 0 {
            go_replay_after + 1
        } else {
            1
        };
        info!(
            "📊 [COMMIT PROCESSOR INIT] Startup: next_expected_commit_index={}, from go_replay_after={}",
            next_expected_commit_index, go_replay_after
        );

        let mut commit_processor = crate::consensus::commit_processor::CommitProcessor::new(
            commit_receiver,
        )
        .with_delivery_sender(delivery_tx)
        .with_commit_index_callback(
            crate::consensus::commit_callbacks::create_commit_index_callback(
                current_commit_index.clone(),
            ),
        )
        .with_global_exec_index_callback(
            crate::consensus::commit_callbacks::create_global_exec_index_callback(
                shared_last_global_exec_index.clone(),
            ),
        )
        .with_get_last_global_exec_index({
            let shared_index = shared_last_global_exec_index.clone();
            move || {
                if let Ok(_rt) = tokio::runtime::Handle::try_current() {
                    warn!("⚠️ get_last_global_exec_index called from async context, returning 0.");
                    0
                } else {
                    let shared_index_clone = shared_index.clone();
                    futures::executor::block_on(async { *shared_index_clone.lock().await })
                }
            }
        })
        .with_shared_last_global_exec_index(shared_last_global_exec_index.clone())
        .with_epoch_info(storage.current_epoch, storage.epoch_base_exec_index)
        .with_next_expected_index(next_expected_commit_index)
        .with_is_transitioning(is_transitioning.clone())
        .with_pending_transactions_queue(pending_transactions_queue.clone())
        .with_epoch_transition_callback(epoch_transition_callback)
        .with_epoch_eth_addresses({
            let mut map = std::collections::HashMap::new();
            map.insert(
                storage.current_epoch,
                storage.validator_eth_addresses.clone(),
            );
            Arc::new(tokio::sync::RwLock::new(map))
        })
        .with_storage_path(config.storage_path.clone());

        // ExecutorClient for commit processing
        let initial_next_expected = if config.executor_read_enabled {
            storage.last_global_exec_index + 1
        } else {
            1
        };

        // MOVED UP: Create system_transaction_provider BEFORE executor_client_for_proc
        // so we can wire the go_lag_handle for backpressure
        let epoch_duration_seconds = storage.epoch_duration_from_go;
        let system_transaction_provider = Arc::new(DefaultSystemTransactionProvider::new(
            storage.current_epoch,
            epoch_duration_seconds,
            storage.epoch_timestamp_ms,
            config.time_based_epoch_change,
        ));

        let executor_client_for_proc = if config.executor_read_enabled {
            let mut client = ExecutorClient::new_with_initial_index(
                true,
                config.executor_commit_enabled,
                config.executor_send_socket_path.clone(),
                config.executor_receive_socket_path.clone(),
                initial_next_expected,
                Some(config.storage_path.clone()),
            );
            // BACKPRESSURE: Wire go_lag_handle from SystemTransactionProvider into ExecutorClient
            // This creates the feedback loop: flush_buffer() computes lag → updates go_lag
            // → SystemTransactionProvider checks go_lag before emitting EndOfEpoch
            client.set_go_lag_handle(system_transaction_provider.go_lag_handle());
            Arc::new(client)
        } else {
            Arc::new(ExecutorClient::new(
                false,
                false,
                "".to_string(),
                "".to_string(),
                None,
            ))
        };

        // ═══════════════════════════════════════════════════════════════════════════
        // STARTUP STATE SYNC BARRIER (2026-04-24)
        //
        // When a node restarts from a stale snapshot (or genesis) while the network
        // has advanced, its local Go state is behind peers. If we start consensus
        // immediately, the node will produce/verify blocks with a divergent state
        // root because its GEI and account state lag behind. This causes a PERMANENT
        // fork (hash mismatch on every block).
        //
        // Fix: Query peers for their latest block. If local Go is behind by more
        // than a small threshold, fetch missing blocks via P2P and execute them
        // through Go BEFORE allowing consensus to start.
        // ═══════════════════════════════════════════════════════════════════════════
        if config.executor_read_enabled {
            let barrier_client = executor_client_for_proc.clone();
            let barrier_peers = config.peer_rpc_addresses.clone();

            // Give Go a moment to finish its own initialization
            tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;

            // CRITICAL FIX: Use the verified latest_block_number from setup_storage()
            // instead of re-querying Go. Re-querying with a new ExecutorClient can race
            // with Go's internal state and return a stale/wrong value (e.g., 40 instead of 51).
            // This caused re-execution of already-existing blocks → permanent fork.
            let mut local_block = storage.latest_block_number;
            tracing::info!(
                "📊 [STARTUP-SYNC] Barrier using verified block={} from setup_storage()",
                local_block
            );

            // Sanity check: re-query Go once. If it reports a HIGHER number, use that
            // (Go may have processed blocks since setup_storage). If it reports LOWER,
            // trust the setup_storage value (Go gave us a stale value).
            match barrier_client.get_last_block_number().await {
                Ok((requery_block, _gei, true, _hash, _)) => {
                    if requery_block > local_block {
                        tracing::info!(
                            "📊 [STARTUP-SYNC] Go advanced since setup: {} -> {}. Using higher value.",
                            local_block, requery_block
                        );
                        local_block = requery_block;
                    } else if requery_block < local_block {
                        tracing::warn!(
                            "🚨 [STARTUP-SYNC] Go re-query returned STALE value ({} < {}). \
                             Ignoring — this prevents re-execution of existing blocks.",
                            requery_block, local_block
                        );
                    }
                }
                Ok((requery_block, _gei, false, _hash, _)) => {
                    tracing::warn!(
                        "⚠️ [STARTUP-SYNC] Go re-query not ready (block={}). Trusting setup_storage value={}.",
                        requery_block, local_block
                    );
                }
                Err(e) => {
                    tracing::warn!(
                        "⚠️ [STARTUP-SYNC] Go re-query failed ({}). Trusting setup_storage value={}.",
                        e, local_block
                    );
                }
            }

            // ═══════════════════════════════════════════════════════════════
            // FORK-SAFETY FIX (v8): Retry peer sync until gap is closed.
            //
            // ROOT CAUSE: Single-shot sync fetches blocks up to the peer's
            // current block at query time. But while we sync those blocks,
            // the cluster continues creating more. Example:
            //   - Query: peer at 231, local at 198 → sync 199-231
            //   - While syncing, cluster advances to 262
            //   - Gap: 231 vs 262 → 31 blocks of new commits
            //   - Node creates blocks 232+ from NEW commits (different content!)
            //   - Other nodes have blocks 232+ from EARLIER commits
            //   - Same block number, different content → hash mismatch
            //
            // FIX: Loop the sync until gap ≤ 2 blocks (network jitter).
            // Max 5 retries to prevent infinite loops.
            // ═══════════════════════════════════════════════════════════════
            const MAX_SYNC_RETRIES: u32 = 5;
            const ACCEPTABLE_GAP: u64 = 2;

            for sync_round in 0..MAX_SYNC_RETRIES {
                // Re-query peers for their latest block
                let mut max_peer_block = 0u64;
                let mut max_peer_gei = 0u64;
                for peer_addr in &barrier_peers {
                    match crate::network::peer_rpc::query_peer_info(peer_addr).await {
                        Ok(info) => {
                            if info.last_block > max_peer_block {
                                max_peer_block = info.last_block;
                                max_peer_gei = info.last_global_exec_index;
                            }
                        }
                        Err(e) => {
                            tracing::debug!("⚠️ [STARTUP-SYNC] Peer {} info query failed: {}", peer_addr, e);
                        }
                    }
                }

                if max_peer_block == 0 || local_block + ACCEPTABLE_GAP >= max_peer_block {
                    tracing::info!(
                        "✅ [STARTUP-SYNC] Local state in sync (local_block={}, peer_block={}, round={}). Starting consensus...",
                        local_block, max_peer_block, sync_round
                    );
                    break;
                }

                let gap = max_peer_block - local_block;
                tracing::warn!(
                    "🚨 [STARTUP-SYNC] Round {}: Local state BEHIND network! local_block={}, peer_block={}, gap={}, peer_gei={}. Syncing...",
                    sync_round, local_block, max_peer_block, gap, max_peer_gei
                );

                let from_block = local_block + 1;
                let to_block = max_peer_block;

                match crate::network::peer_rpc::fetch_blocks_from_peer(&barrier_peers, from_block, to_block).await {
                    Ok(blocks) if !blocks.is_empty() => {
                        tracing::info!(
                            "🔄 [STARTUP-SYNC] Round {}: Fetched {} blocks ({}-{}). Executing...",
                            sync_round,
                            blocks.len(),
                            blocks.first().map(|b| b.block_number).unwrap_or(0),
                            blocks.last().map(|b| b.block_number).unwrap_or(0)
                        );
                        match barrier_client.sync_and_execute_blocks(blocks).await {
                            Ok((synced, last_block, _gei)) => {
                                tracing::info!(
                                    "✅ [STARTUP-SYNC] Round {}: Synced {} blocks (last_block={})",
                                    sync_round, synced, last_block
                                );
                                local_block = last_block; // Update for next iteration
                                
                                // Re-query Go GEI after syncing to update shared state
                                if let Ok((_, new_gei, _, _, _)) = barrier_client.get_last_block_number().await {
                                    coordination_hub.set_initial_global_exec_index(new_gei).await;
                                    
                                    let new_handled = {
                                        let persisted = crate::node::executor_client::persistence::load_persisted_last_index(&config.storage_path);
                                        match persisted {
                                            Some((_gei, commit_idx)) if commit_idx > 0 => commit_idx,
                                            _ => {
                                                let wipe_safe = crate::node::executor_client::persistence::load_persisted_last_index_wipe_safe(&config.storage_path);
                                                match wipe_safe {
                                                    Some((_gei, commit_idx)) if commit_idx > 0 => commit_idx,
                                                    _ => {
                                                        go_replay_after
                                                    }
                                                }
                                            }
                                        }
                                    };
                                    commit_consumer.monitor().set_highest_handled_commit(new_handled);
                                    tracing::info!(
                                        "🔄 [STARTUP-SYNC] Round {}: Re-queried Go: gei={}, highest_handled_commit={}",
                                        sync_round, new_gei, new_handled
                                    );
                                }
                            }
                            Err(e) => {
                                tracing::error!("❌ [STARTUP-SYNC] Round {}: Failed to execute fetched blocks: {}", sync_round, e);
                                break;
                            }
                        }
                    }
                    Ok(_) => {
                        tracing::warn!("⚠️ [STARTUP-SYNC] Round {}: Fetched 0 blocks from peers. Retrying...", sync_round);
                    }
                    Err(e) => {
                        tracing::error!("❌ [STARTUP-SYNC] Round {}: Failed to fetch blocks from peers: {}", sync_round, e);
                        break;
                    }
                }

                // Small delay before retry to let the cluster produce more blocks
                if sync_round < MAX_SYNC_RETRIES - 1 {
                    tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;
                }
            }
        }

        let is_terminally_failed = Arc::new(AtomicBool::new(false));

        // FORK-SAFETY FIX v5: initialize_from_go() MUST be SYNCHRONOUS.
        // Previously this was spawned as tokio::spawn (async), creating a race
        // condition: the CommitProcessor could start processing replayed commits
        // BEFORE initialize_from_go() updated next_expected_index and next_block_number.
        //
        // After DAG wipe + startup sync, Go's state moves to GEI=1807 but the
        // executor client still has next_expected=1662 (pre-sync value). Without
        // synchronous init, old commits with GEI >= 1662 pass the replay guard
        // and produce DUPLICATE blocks on Go → GEI divergence → fork.
        //
        // This call is fast (<1ms, local UDS socket to Go) so blocking is safe.
        executor_client_for_proc.initialize_from_go().await;
        tracing::info!(
            "✅ [STARTUP] initialize_from_go() completed synchronously (block/GEI guards updated)"
        );

        // Tích hợp BlockDeliveryManager vào Phase khởi động
        let peer_addrs = config.peer_rpc_addresses.clone();
        let executor_client_for_manager = executor_client_for_proc.clone();
        let failed_delivery = is_terminally_failed.clone();
        tokio::spawn(async move {
            let manager = crate::node::block_delivery::BlockDeliveryManager::new(
                executor_client_for_manager,
                delivery_rx,
                peer_addrs,
            );
            
            // Explicitly unused since we no longer mark terminal failure on normal exit
            let _ = failed_delivery;

            manager.run().await;
            tracing::info!("🛑 [STATION 4: DELIVERY] BlockDeliveryManager gracefully exited (expected on Epoch Transition).");
        });

        // ═══════════════════════════════════════════════════════════════════════════
        // SYNC-FIRST BARRIER: REMOVED (2026-04-24)
        //
        // Previously this section created a CatchupManager and ran block/epoch sync
        // before starting consensus (~70 lines + 2s sleep). This is redundant because:
        //
        // 1. CommitSyncer::update_state() detects local_commit==0 && handled>0
        //    → calls reset_to_network_baseline() → fast-forwards DAG automatically
        // 2. RustSyncNode handles block gap for SyncOnly nodes
        // 3. epoch_monitor handles cross-epoch catch-up
        //
        // Removing this eliminates ~2s startup delay and ~70 lines of duplicate logic.
        // CommitSyncer is the single source of truth for DAG catch-up.
        // ═══════════════════════════════════════════════════════════════════════════

        // ♻️ TX Recycler: Create shared instance for tracking and recycling uncommitted TXs
        let tx_recycler = Arc::new(crate::consensus::tx_recycler::TxRecycler::new());
        info!("♻️ [TX RECYCLER] Created shared TxRecycler instance");

        commit_processor = commit_processor
            .with_executor_client(executor_client_for_proc.clone())
            .with_tx_recycler(tx_recycler.clone());

        let (lag_alert_sender, mut lag_alert_receiver) = tokio::sync::mpsc::unbounded_channel::<
            crate::consensus::commit_processor::lag_monitor::LagAlert,
        >();

        commit_processor = commit_processor.with_lag_alert_sender(lag_alert_sender);

        let lag_executor_client = executor_client_for_proc.clone();
        let lag_peer_addresses = config.peer_rpc_addresses.clone();

        tokio::spawn(async move {
            while let Some(alert) = lag_alert_receiver.recv().await {
                match alert {
                    crate::consensus::commit_processor::lag_monitor::LagAlert::ModerateLag {
                        gap,
                        go_rate,
                        go_block_number,
                        ..
                    } => {
                        tracing::warn!("⚠️ [LAG-MONITOR] Go is {} blocks behind Rust (rate: {:.1} blk/s), go_block_number={}. Monitoring...", gap, go_rate, go_block_number);
                    }
                    crate::consensus::commit_processor::lag_monitor::LagAlert::SevereLag {
                        rust_gei,
                        go_gei,
                        go_block_number,
                        gap,
                        go_rate,
                    } => {
                        tracing::error!("🚨 [LAG-MONITOR] SEVERE: Go is {} blocks behind Rust! (rust={}, go_gei={}, go_block={}, rate={:.1} blk/s).",
                            gap, rust_gei, go_gei, go_block_number, go_rate);

                        if lag_peer_addresses.is_empty() {
                            tracing::warn!("⚠️ [LAG-RECOVERY] No peer_rpc_addresses configured! Cannot fetch missing blocks from P2P.");
                            continue;
                        }

                        // Use go_block_number (actual block index in Go DB) rather than GEI.
                        // We safely ask for blocks up to go_block_number + gap (Go peer caps nicely to its own max).
                        let missing_from = go_block_number + 1;
                        let missing_to = go_block_number + gap;
                        tracing::info!("🔄 [LAG-RECOVERY] Triggering P2P block fetch: blocks {} -> {} (assuming worst-case dense gap)", missing_from, missing_to);

                        match crate::network::peer_rpc::fetch_blocks_from_peer(
                            &lag_peer_addresses,
                            missing_from,
                            missing_to,
                        )
                        .await
                        {
                            Ok(blocks) => {
                                if blocks.is_empty() {
                                    tracing::warn!(
                                        "⚠️ [LAG-RECOVERY] Fetched 0 blocks from peers."
                                    );
                                } else {
                                    tracing::info!("✅ [LAG-RECOVERY] Fetched {} blocks. Sending to Go for execution...", blocks.len());
                                    match lag_executor_client.sync_and_execute_blocks(blocks).await
                                    {
                                        Ok((synced, last_block, _gei)) => {
                                            tracing::info!("✅ [LAG-RECOVERY] Successfully executed {} P2P blocks (last_block={})", synced, last_block);
                                        }
                                        Err(e) => {
                                            tracing::error!("❌ [LAG-RECOVERY] Failed to execute blocks via UDS: {}", e);
                                        }
                                    }
                                }
                            }
                            Err(e) => {
                                tracing::error!("❌ [LAG-RECOVERY] P2P block fetch failed: {}", e);
                            }
                        }
                    }
                    crate::consensus::commit_processor::lag_monitor::LagAlert::Recovered {
                        ..
                    } => {
                        tracing::info!("✅ [LAG-MONITOR] Go has caught up with Rust. Normal operations resumed.");
                    }
                }
            }
        });

        // Spawn background recycler is done in setup_epoch_management where tx_client is accessible

        let failed_processor = is_terminally_failed.clone();
        tokio::spawn(async move {
            if let Err(e) = commit_processor.run().await {
                tracing::error!("❌ [STATION 3: PROCESSOR] Fatal Error: {}", e);
                failed_processor.store(true, Ordering::SeqCst);
            } else {
                tracing::info!("🛑 [STATION 3: PROCESSOR] Gracefully Exited (Expected upon EndOfEpoch).");
            }
        });

        tokio::spawn(async move {
            while let Some(output) = block_receiver.recv().await {
                tracing::debug!("Received {} certified blocks", output.blocks.len());
            }
        });

        // Consensus parameters
        let protocol_config = ProtocolConfig::get_for_max_version_UNSAFE();
        let mut parameters = consensus_config::Parameters::default();
        parameters.commit_sync_batch_size = config.commit_sync_batch_size;
        parameters.commit_sync_parallel_fetches = config.commit_sync_parallel_fetches;
        parameters.commit_sync_batches_ahead = config.commit_sync_batches_ahead;

        if let Some(ms) = config.min_round_delay_ms {
            parameters.min_round_delay = Duration::from_millis(ms);
        }

        parameters.adaptive_delay_enabled = config.adaptive_delay_enabled;

        if let Some(ms) = config.leader_timeout_ms {
            parameters.leader_timeout = Duration::from_millis(ms);
        } else if config.speed_multiplier != 1.0 {
            info!("Applying speed multiplier: {}x", config.speed_multiplier);
            parameters.leader_timeout =
                Duration::from_millis((200.0 / config.speed_multiplier) as u64);
        }

        let db_path = config
            .storage_path
            .join("epochs")
            .join(format!("epoch_{}", storage.current_epoch))
            .join("consensus_db");
        std::fs::create_dir_all(&db_path)?;
        parameters.db_path = db_path;

        // system_transaction_provider and epoch_duration_seconds are created earlier
        // (before executor_client_for_proc) for backpressure wiring

        // Simplified validator startup: if node is in committee, always start consensus.
        // Cold-start / restart catch-up is handled by CommitSyncer (reset_to_network_baseline).
        // CommitSyncer detects empty DAG → fast-forwards → fetches real commits from peers.
        let is_designated_validator = storage.is_in_committee;
        let start_as_validator = is_designated_validator;

        // CRITICAL: Transition from Initializing → Bootstrapping BEFORE starting authority.
        // This ensures Core::recover() sees should_skip_proposal()=true, preventing
        // assertion panics when the DAG is empty after snapshot restore.
        // CommitSyncer will later transition Bootstrapping → CatchingUp/Healthy.
        if start_as_validator {
            coordination_hub.set_phase(
                consensus_core::coordination_hub::NodeConsensusPhase::Bootstrapping,
            );
        }

        let (authority, commit_consumer_holder) = if start_as_validator {
            info!("🚀 Starting consensus authority node (phase=Bootstrapping)...");

            (
                Some(
                    ConsensusAuthority::start(
                        NetworkType::Tonic,
                        storage.epoch_timestamp_ms,
                        storage.epoch_base_exec_index,
                        storage.own_index,
                        storage.committee.clone(),
                        parameters.clone(),
                        protocol_config.clone(),
                        storage.protocol_keypair.clone(),
                        storage.network_keypair.clone(),
                        clock.clone(),
                        transaction_verifier.clone(),
                        commit_consumer,
                        registry.clone(),
                        0,
                        Some(system_transaction_provider.clone()
                            as Arc<dyn SystemTransactionProvider>),
                        None,
                        coordination_hub,
                    )
                    .await,
                ),
                None,
            )
        } else {
            info!("🔄 Starting as sync-only node (not in committee)");
            info!("📡 Keeping commit_consumer alive for SyncOnly mode to prevent channel close");
            (None, Some(commit_consumer))
        };

        let transaction_client_proxy = authority
            .as_ref()
            .map(|auth| Arc::new(TransactionClientProxy::new(auth.transaction_client())));

        Ok(ConsensusSetup {
            authority,
            dag_has_history,
            commit_consumer_holder,
            transaction_client_proxy,
            executor_client_for_proc,
            current_commit_index,
            pending_transactions_queue,
            committed_transaction_hashes,
            epoch_tx_sender,
            epoch_tx_receiver,
            system_transaction_provider,
            protocol_config,
            parameters,
            clock,
            transaction_verifier,
            tx_recycler,
            is_terminally_failed,
        })
    }

    // -----------------------------------------------------------------------
    // Phase 3: setup_networking
    // -----------------------------------------------------------------------

    /// Initializes clock sync manager and starts NTP sync tasks.
    fn setup_networking(config: &NodeConfig) -> Arc<RwLock<ClockSyncManager>> {
        let clock_sync_manager = Arc::new(RwLock::new(ClockSyncManager::new(
            config.ntp_servers.clone(),
            config.max_clock_drift_seconds * 1000,
            config.ntp_sync_interval_seconds,
            config.enable_ntp_sync,
        )));

        if config.enable_ntp_sync {
            let sync_manager_clone = clock_sync_manager.clone();
            let monitor_manager_clone = clock_sync_manager.clone();
            tokio::spawn(async move {
                let mut manager = sync_manager_clone.write().await;
                let _ = manager.sync_with_ntp().await;
            });
            ClockSyncManager::start_sync_task(clock_sync_manager.clone());
            ClockSyncManager::start_drift_monitor(monitor_manager_clone);
        }

        clock_sync_manager
    }

    // -----------------------------------------------------------------------
    // Phase 4: setup_epoch_management
    // -----------------------------------------------------------------------

    /// Assembles the ConsensusNode, initializes epoch management, starts sync tasks and monitors.
    async fn setup_epoch_management(
        config: NodeConfig,
        storage: StorageSetup,
        consensus: ConsensusSetup,
        clock_sync_manager: Arc<RwLock<ClockSyncManager>>,
        _registry: &Registry,
        coordination_hub: consensus_core::coordination_hub::ConsensusCoordinationHub,
    ) -> Result<ConsensusNode> {
        // Initialize no-op epoch change handlers (required by core)
        use consensus_core::epoch_change_provider::{EpochChangeProcessor, EpochChangeProvider};
        struct NoOpProvider;
        impl EpochChangeProvider for NoOpProvider {
            fn get_proposal(&self) -> Option<Vec<u8>> {
                None
            }
            fn get_votes(&self) -> Vec<Vec<u8>> {
                Vec::new()
            }
        }
        struct NoOpProcessor;
        impl EpochChangeProcessor for NoOpProcessor {
            fn process_proposal(&self, _: &[u8]) {}
            fn process_vote(&self, _: &[u8]) {}
        }
        consensus_core::epoch_change_provider::init_epoch_change_provider(Box::new(NoOpProvider));
        consensus_core::epoch_change_provider::init_epoch_change_processor(Box::new(NoOpProcessor));

        // ♻️ TX RECYCLER: Background recycler DISABLED PERMANENTLY.
        // ARCHITECTURAL REASON: Go broadcasts its mempool to all nodes via LibP2P.
        // Therefore, MULTIPLE Rust validators will propose the EXACT SAME transactions in their blocks.
        // Only one of those blocks might get sequenced quickly, while the others might be GarbageCollected.
        // If a losing validator auto-resubmits its dropped/GC'd transactions (either via timeout or GC events),
        // it introduces massive duplicate transaction bloat into the DAG, causing State Root forks or performance collapse.
        // Dropped transactions should only be retried by the original Client/Wallet, NOT the consensus layer.
        // Tracking + confirming still active solely for metrics and observability.

        let mut node = ConsensusNode {
            authority: consensus.authority,
            legacy_store_manager: Arc::new(consensus_core::LegacyEpochStoreManager::new(
                config.epochs_to_keep,
            )),
            coordination_hub: coordination_hub.clone(),
            // In-committee nodes are always Validator. Catch-up is handled by CommitSyncer
            // (cold-start path: highest_accepted_round <= 1).
            node_mode: if storage.is_in_committee {
                NodeMode::Validator
            } else {
                NodeMode::SyncOnly
            },
            execution_lock: Arc::new(tokio::sync::RwLock::new(storage.current_epoch)),
            reconfig_state: Arc::new(tokio::sync::RwLock::new(ReconfigState::default())),
            transaction_client_proxy: consensus.transaction_client_proxy,
            clock_sync_manager,
            current_commit_index: consensus.current_commit_index,
            storage_path: storage.storage_path,
            current_epoch: storage.current_epoch,
            last_global_exec_index: storage.last_global_exec_index,
            protocol_keypair: storage.protocol_keypair,
            network_keypair: storage.network_keypair,
            protocol_config: consensus.protocol_config,
            clock: consensus.clock,
            transaction_verifier: consensus.transaction_verifier,
            parameters: consensus.parameters,
            own_index: storage.own_index,
            boot_counter: 0,
            last_transition_hash: None,
            current_registry_id: None,
            executor_commit_enabled: config.executor_commit_enabled,
            pending_transactions_queue: consensus.pending_transactions_queue,
            system_transaction_provider: consensus.system_transaction_provider,
            epoch_transition_sender: consensus.epoch_tx_sender,
            sync_task_handle: None,
            sync_controller: Arc::new(crate::node::sync_controller::SyncController::new(coordination_hub.clone())),
            epoch_monitor_handle: None,
            notification_server_handle: None,
            executor_client: Some(consensus.executor_client_for_proc),
            epoch_pending_transactions: Arc::new(tokio::sync::Mutex::new(Vec::new())),
            committed_transaction_hashes: consensus.committed_transaction_hashes,
            pending_epoch_transitions: Arc::new(tokio::sync::Mutex::new(Vec::new())),
            _commit_consumer_holder: consensus.commit_consumer_holder,
            epoch_eth_addresses: {
                let mut map = std::collections::HashMap::new();
                map.insert(
                    storage.current_epoch,
                    storage.validator_eth_addresses.clone(),
                );
                Arc::new(tokio::sync::RwLock::new(map))
            },

            peer_rpc_addresses: config.peer_rpc_addresses.clone(),
            tx_recycler: Some(consensus.tx_recycler),
            is_terminally_failed: consensus.is_terminally_failed,
        };

        // Initialize the global StateTransitionManager
        epoch_transition_manager::init_state_manager(
            storage.current_epoch,
            storage.is_in_committee,
        )
        .await;
        info!(
            "🔧 [STARTUP] Initialized StateTransitionManager: epoch={}, mode={}",
            storage.current_epoch,
            if storage.is_in_committee {
                "Validator"
            } else {
                "SyncOnly"
            }
        );

        // Load previous epoch stores for cross-epoch sync support
        // This enables THIS node to serve historical commits to lagging peers
        load_legacy_epoch_stores(
            &node.legacy_store_manager,
            &config.storage_path,
            storage.current_epoch,
            config.epochs_to_keep,
        );

        crate::consensus::epoch_transition::start_epoch_transition_handler(
            consensus.epoch_tx_receiver,
            node.system_transaction_provider.clone(),
            config.clone(),
        );

        node.check_and_update_node_mode(&storage.committee, &config, false)
            .await?;

        // Start sync task for SyncOnly nodes
        if matches!(node.node_mode, NodeMode::SyncOnly) {
            let _ = node.start_sync_task(&config).await;
        }

        // UNIFIED EPOCH MONITOR
        if let Ok(Some(handle)) =
            epoch_monitor::start_unified_epoch_monitor(&node.executor_client, &config)
        {
            node.epoch_monitor_handle = Some(handle);
            info!(
                "🔄 Started unified epoch monitor for {:?} mode at epoch={}",
                node.node_mode, node.current_epoch
            );
        }

        recovery::perform_fork_detection_check(&node).await?;

        Ok(node)
    }

    pub(crate) fn is_alive(&self) -> bool {
        if self.is_terminally_failed.load(std::sync::atomic::Ordering::SeqCst) {
            return false;
        }
        if let Some(authority) = &self.authority {
            authority.is_alive()
        } else {
            true
        }
    }
}
