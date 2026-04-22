// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use mysten_metrics::start_prometheus_server;
use prometheus::Registry;
use std::net::SocketAddr;
use std::sync::Arc;
use tokio::sync::Mutex;
use tracing::{error, info, warn};

use crate::config::NodeConfig;
use crate::network::peer_discovery::PeerDiscoveryService;
use crate::network::peer_rpc::PeerRpcServer;
use crate::network::rpc::RpcServer;
use crate::network::tx_socket_server::TxSocketServer;
use crate::node::ConsensusNode;

/// Startup configuration and initialization
pub struct StartupConfig {
    pub node_config: NodeConfig,
    pub registry: Registry,
    pub registry_service: Option<Arc<mysten_metrics::RegistryService>>,
}

impl StartupConfig {
    pub fn new(
        node_config: NodeConfig,
        registry: Registry,
        registry_service: Option<Arc<mysten_metrics::RegistryService>>,
    ) -> Self {
        Self {
            node_config,
            registry,
            registry_service,
        }
    }
}

/// Represents the initialized node and its servers
pub struct InitializedNode {
    pub node: Arc<Mutex<ConsensusNode>>,
    pub rpc_server_handle: Option<tokio::task::JoinHandle<()>>,
    pub uds_server_handle: Option<tokio::task::JoinHandle<()>>,
    #[allow(dead_code)]
    pub peer_rpc_server_handle: Option<tokio::task::JoinHandle<()>>,
    pub node_config: NodeConfig,
}

impl InitializedNode {
    /// Initialize and start all node components
    pub async fn initialize(config: StartupConfig) -> Result<Self> {
        let StartupConfig {
            node_config,
            registry,
            registry_service,
        } = config;

        // Start metrics server if enabled
        let _metrics_addr = if node_config.enable_metrics {
            let metrics_addr = SocketAddr::from(([127, 0, 0, 1], node_config.metrics_port));
            let _registry_service = start_prometheus_server(metrics_addr);
            info!(
                "Metrics server started at http://127.0.0.1:{}/metrics",
                node_config.metrics_port
            );
            Some(metrics_addr)
        } else {
            info!("Metrics server is disabled (enable_metrics = false)");
            None
        };

        // Get registry from RegistryService if metrics is enabled, otherwise create a new one
        let registry = if let Some(ref rs) = registry_service {
            rs.default_registry()
        } else {
            registry
        };

        // Create the ConsensusNode wrapped in a Mutex for safe concurrent access
        let node = Arc::new(Mutex::new(
            ConsensusNode::new_with_registry_and_service(node_config.clone(), registry).await?,
        ));

        // Register node in global registry for transition handler access
        crate::node::set_transition_handler_node(node.clone()).await;

        // Get transaction submitter for servers
        let tx_client = { node.lock().await.transaction_submitter() };

        let rpc_server_handle;
        let uds_server_handle;
        let mut peer_rpc_server_handle = None;

        // Start RPC server for client submissions (HTTP)
        // ALWAY start RPC Server. For SyncOnly nodes that are catching up, they still need the port open
        // so that clients can connect (the transactions will just be rejected/queued until caught up).
        let rpc_port = node_config.metrics_port + 1000;
        let node_for_rpc = node.clone();
        let rpc_server = RpcServer::with_node(rpc_port, node_for_rpc);
        rpc_server_handle = Some(tokio::spawn(async move {
            if let Err(e) = rpc_server.start().await {
                error!("RPC server error: {}", e);
            }
        }));
        info!("RPC server available at http://127.0.0.1:{}", rpc_port);

        // Start Unix Domain Socket server for ALL node types (validator + SyncOnly)
        // Validators submit directly to consensus; SyncOnly forwards to validators via peer RPC
        {
            let tx_client_for_uds: Arc<dyn crate::node::tx_submitter::TransactionSubmitter> =
                match tx_client {
                    Some(ref tc) => tc.clone(),
                    None => Arc::new(crate::node::tx_submitter::NoOpTransactionSubmitter),
                };

            let _socket_path = node_config
                .rust_tx_socket_path
                .clone()
                .unwrap_or_else(|| format!("/tmp/metanode-tx-{}.sock", node_config.node_id));
            let node_for_uds = node.clone();
            let (is_transitioning_for_uds, _pending_tx_queue, _storage_path) = {
                let node_guard = node.lock().await;
                (
                    node_guard.is_transitioning.clone(),
                    node_guard.pending_transactions_queue.clone(),
                    node_guard.storage_path.clone(),
                )
            };

            // Start PeerDiscoveryService if enabled
            let peer_discovery_addresses = if node_config.enable_peer_discovery {
                if let Some(ref go_rpc_url) = node_config.go_rpc_url {
                    let peer_port = node_config.peer_rpc_port.unwrap_or(6090);
                    let refresh_interval =
                        std::time::Duration::from_secs(node_config.peer_discovery_refresh_secs);
                    let service = Arc::new(
                        PeerDiscoveryService::new(go_rpc_url.clone(), peer_port)
                            .with_refresh_interval(refresh_interval),
                    );
                    let addresses_handle = service.get_addresses_handle();

                    // Start background refresh task
                    let _discovery_handle = service.start();
                    info!(
                        "🔍 [PEER DISCOVERY] Service started (refresh every {}s)",
                        node_config.peer_discovery_refresh_secs
                    );

                    Some(addresses_handle)
                } else {
                    warn!("⚠️ [PEER DISCOVERY] Enabled but go_rpc_url is not set, skipping");
                    None
                }
            } else {
                None
            };

            let mut uds_server = TxSocketServer::with_node(
                tx_client_for_uds,
                Some(node_for_uds),
                Some(is_transitioning_for_uds),
                node_config.peer_rpc_addresses.clone(),
            );

            // ♻️ TX RECYCLER: Inject into UDS server for tracking submitted TXs
            {
                let node_guard = node.lock().await;
                if let Some(ref recycler) = node_guard.tx_recycler {
                    uds_server = uds_server.with_tx_recycler(recycler.clone());
                    info!("♻️ [TX RECYCLER] Injected into UDS server");
                }
            }

            // Inject dynamic peer addresses if discovery is enabled
            if let Some(addrs) = peer_discovery_addresses {
                uds_server = uds_server.with_peer_discovery(addrs);
            }

            uds_server_handle = Some(tokio::spawn(async move {
                if let Err(e) = uds_server.start().await {
                    error!("FFI Transaction process error: {}", e);
                }
            }));
            info!("FFI transaction interface initialized");
        }

        if let Some(peer_port) = node_config.peer_rpc_port {
            if peer_port > 0 {
                let (executor_client_for_peer, shared_index_for_peer) = {
                    let node_guard = node.lock().await;
                    (
                        node_guard.executor_client.clone(),
                        Some(node_guard.shared_last_global_exec_index.clone()),
                    )
                };
                if let Some(exc) = executor_client_for_peer {
                    let mut peer_server = PeerRpcServer::new(
                        node_config.node_id,
                        peer_port,
                        node_config.network_address.clone(),
                        exc,
                        shared_index_for_peer
                            .unwrap_or_else(|| std::sync::Arc::new(tokio::sync::Mutex::new(0))),
                    );
                    // Inject dynamic node reference instead of static transaction submitter
                    peer_server = peer_server.with_node(node.clone());
                    info!("📡 [PEER RPC] Node reference injected for dynamic transaction routing");
                    peer_rpc_server_handle = Some(tokio::spawn(async move {
                        if let Err(e) = peer_server.start().await {
                            error!("Peer RPC server error: {}", e);
                        }
                    }));
                    info!(
                        "📡 [PEER RPC] Server started on 0.0.0.0:{} for WAN sync",
                        peer_port
                    );
                } else {
                    warn!("⚠️ [PEER RPC] No executor client available, skipping peer RPC server");
                }
            }
        }

        Ok(Self {
            node,
            rpc_server_handle,
            uds_server_handle,
            peer_rpc_server_handle,
            node_config,
        })
    }

    /// Run the main event loop
    pub async fn run_main_loop(self) -> Result<()> {
        // --- [SYNC-BEFORE-CONSENSUS] ---
        // Block startup until we catch up with the network
        // This prevents the node from proposing blocks on a fork or when behind
        let catchup_manager = {
            let node_guard = self.node.lock().await;
            if let Some(client) = node_guard.executor_client.clone() {
                Some(crate::node::catchup::CatchupManager::new(
                    client,
                    self.node_config.executor_receive_socket_path.clone(),
                    self.node_config.peer_rpc_addresses.clone(),
                ))
            } else {
                warn!("⚠️ [STARTUP] No executor client available, skipping catchup check");
                None
            }
        };

        // ═══════════════════════════════════════════════════════════════════════
        // SNAPSHOT RESTORE SUPPORT for SyncOnly nodes:
        // Previously, SyncOnly skipped catchup entirely, causing nodes restored
        // from Go snapshot to get stuck (commit_syncer at stale epoch = "Not
        // enough votes"). Now we run a LIGHTWEIGHT epoch-catchup: sync Go blocks
        // from peers until Go reaches the current network epoch. After Go catches
        // up, the existing epoch_monitor detects the epoch change and triggers
        // consensus restart at the correct epoch. Combined with the cold-start
        // fast-forward in commit_syncer, the node does NOT need Rust consensus
        // storage copied from another node.
        //
        // The old deadlock (can't match epoch without blocks, can't get blocks
        // without matching epoch) is broken by sync_go_to_current_epoch() which
        // fetches blocks from peer Go nodes regardless of epoch match.
        // ═══════════════════════════════════════════════════════════════════════
        let is_sync_only_mode = {
            let node_guard = self.node.lock().await;
            matches!(node_guard.node_mode, crate::node::NodeMode::SyncOnly)
        };

        if is_sync_only_mode {
            // SyncOnly: Run lightweight epoch-catchup only (no consensus join wait)
            if let Some(ref cm) = catchup_manager {
                info!("📋 [STARTUP] SyncOnly mode: running epoch-catchup before sync_loop...");
                let local_epoch = {
                    let node_guard = self.node.lock().await;
                    node_guard.current_epoch
                };
                match cm.check_sync_status(local_epoch, 0).await {
                    Ok(status) if !status.epoch_match => {
                        info!(
                            "🔄 [STARTUP] SyncOnly epoch mismatch: Local={}, Network={}. Syncing Go blocks...",
                            local_epoch, status.go_epoch
                        );
                        match cm.sync_go_to_current_epoch(status.go_epoch).await {
                            Ok(synced) => {
                                info!(
                                    "✅ [STARTUP] SyncOnly epoch-catchup complete: {} blocks synced. \
                                     epoch_monitor will handle consensus restart at correct epoch.",
                                    synced
                                );
                            }
                            Err(e) => {
                                warn!(
                                    "⚠️ [STARTUP] SyncOnly epoch-catchup failed: {}. \
                                     sync_loop will handle catchup at runtime.",
                                    e
                                );
                            }
                        }
                    }
                    Ok(_) => {
                        info!("✅ [STARTUP] SyncOnly: epoch matches network, no catchup needed.");
                    }
                    Err(e) => {
                        warn!(
                            "⚠️ [STARTUP] SyncOnly sync status check failed: {}. Proceeding.",
                            e
                        );
                    }
                }
            } else {
                info!("📋 [STARTUP] SyncOnly mode: no executor client, skipping catchup.");
            }
        } else if let Some(cm) = catchup_manager {
            let is_syncing_up_mode = {
                let node_guard = self.node.lock().await;
                matches!(node_guard.node_mode, crate::node::NodeMode::SyncingUp)
            };

            if is_syncing_up_mode {
                info!("⏳ [STARTUP] Verifying sync status before joining consensus...");
                let timeout = std::time::Duration::from_secs(600); // 10 minutes timeout
                let start = std::time::Instant::now();

                // Node is already in SyncingUp mode, we just wait.

                loop {
                    // Check timeout
                    if start.elapsed() > timeout {
                        warn!("⚠️ [STARTUP] Catchup timed out after 600s. Forcing start (risky).");
                        break;
                    }

                    // Get current local state
                    let (local_epoch, local_commit) = {
                        let node = self.node.lock().await;
                        // RocksDBStore read is expensive? No, we use in-memory counters if available?
                        // ConsensusNode has current_commit_index (AtomicU32) but we need u64 mapping?
                        // Let's use current_epoch.
                        // Commit index is trickier. Let's assume passed 0 for now as catchup checks Epoch primarily.
                        // But for Commit sync, we need local commit.
                        // Use commit_processor's tracked index?
                        // Node has `current_commit_index` (AtomicU32).
                        (
                            node.current_epoch,
                            node.current_commit_index
                                .load(std::sync::atomic::Ordering::Relaxed)
                                as u64,
                        )
                    };

                    match cm.check_sync_status(local_epoch, local_commit).await {
                        Ok(status) => {
                            if status.ready {
                                info!(
                                    "✅ [STARTUP] Node is synced (gap={}). Joining consensus!",
                                    status.commit_gap
                                );
                                // CRITICAL FIX: Update Rust's internal state to match the synced network state
                                // If Rust started with epoch 0 but Go and the Network are at epoch N,
                                // we must update Rust's state before joining consensus, otherwise it starts at epoch 0!
                                {
                                    let mut node = self.node.lock().await;
                                    if node.current_epoch != status.go_epoch {
                                        info!(
                                        "🔄 [STARTUP] Updating Rust internal epoch {} -> {} (Network Epoch)",
                                        node.current_epoch, status.go_epoch
                                    );
                                        node.current_epoch = status.go_epoch;
                                    }
                                    if node.last_global_exec_index < status.network_commit {
                                        info!(
                                        "🔄 [STARTUP] Updating Rust internal exec_index {} -> {} (Network Commit)",
                                        node.last_global_exec_index, status.network_commit
                                    );
                                        node.last_global_exec_index = status.network_commit;
                                    }
                                }
                                break;
                            }

                            if status.epoch_match {
                                info!(
                                    "🔄 [CATCHUP] Syncing blocks: LocalExec={}, Network={}, Gap={}",
                                    status.go_last_block,
                                    status.network_block_height,
                                    status.block_gap
                                );

                                // FAST SYNC: For large gaps, loop continuously fetching batches
                                if status.block_gap > 100 {
                                    let mut remaining = status.block_gap;
                                    let mut current_go_block = status.go_last_block;
                                    let max_blocks_per_cycle = 2000u64;
                                    let mut fetched_total = 0u64;

                                    while remaining > 0 && fetched_total < max_blocks_per_cycle {
                                        let fetch_to = std::cmp::min(
                                            current_go_block + 50,
                                            status.network_block_height,
                                        );
                                        match cm
                                            .sync_blocks_from_peers(current_go_block, fetch_to)
                                            .await
                                        {
                                            Ok(synced) => {
                                                if synced == 0 {
                                                    break;
                                                }
                                                current_go_block += synced;
                                                remaining = remaining.saturating_sub(synced);
                                                fetched_total += synced;
                                            }
                                            Err(e) => {
                                                warn!("⚠️ [CATCHUP] Fast sync batch failed: {}", e);
                                                break;
                                            }
                                        }
                                    }
                                    info!(
                                        "🚀 [CATCHUP] Fast sync cycle: fetched {} blocks total",
                                        fetched_total
                                    );
                                    continue; // No delay - immediately re-check
                                }

                                // Normal sync: small gap, single fetch
                                if let Err(e) = cm
                                    .sync_blocks_from_peers(
                                        status.go_last_block,
                                        status.network_block_height,
                                    )
                                    .await
                                {
                                    warn!("⚠️ [CATCHUP] Block sync from peers failed: {}", e);
                                }
                            } else {
                                // ═══════════════════════════════════════════════════
                                // CROSS-EPOCH BLOCK SYNC (Snapshot Restore Support)
                                // Go is at a different epoch than the network.
                                // Fetch blocks from peer Go nodes until Go catches up
                                // to the current network epoch. This enables nodes to
                                // start from only a Go snapshot without Rust data.
                                // ═══════════════════════════════════════════════════
                                info!(
                                "🔄 [CATCHUP] Epoch mismatch: GoLocal={}, Network={}. Syncing Go blocks to reach target epoch...",
                                local_epoch, status.go_epoch
                            );
                                match cm.sync_go_to_current_epoch(status.go_epoch).await {
                                    Ok(synced) => {
                                        info!(
                                        "✅ [CATCHUP] Cross-epoch sync complete: {} blocks synced. Go should now be at epoch {}.",
                                        synced, status.go_epoch
                                    );
                                        // Don't delay — immediately re-check sync status
                                        continue;
                                    }
                                    Err(e) => {
                                        warn!(
                                        "⚠️ [CATCHUP] Cross-epoch sync failed: {}. Will retry...",
                                        e
                                    );
                                    }
                                }
                            }
                        }
                        Err(e) => {
                            warn!("⚠️ [STARTUP] Failed to check sync status: {}", e);
                        }
                    }

                    // Dynamic delay: 200ms for near-caught-up (much faster than original 2s)
                    tokio::time::sleep(std::time::Duration::from_millis(200)).await;
                }
            } else {
                // ═══════════════════════════════════════════════════════════════
                // SNAPSHOT RESTORE: Even when "not lagging" (no peer reference),
                // the node may be behind by entire epochs after snapshot restore.
                // Check if Go epoch matches network epoch — if not, sync Go
                // blocks from peers until Go catches up to the current epoch.
                // Without this, the node joins consensus at a stale epoch and
                // commit_syncer fails with "wrong epoch" on every fetch.
                // ═══════════════════════════════════════════════════════════════
                info!("✅ [STARTUP] Node is starting directly as Validator (lag is below threshold). Checking epoch match before proceeding...");
                let local_epoch = {
                    let node_guard = self.node.lock().await;
                    node_guard.current_epoch
                };
                match cm.check_sync_status(local_epoch, 0).await {
                    Ok(status) if !status.epoch_match => {
                        warn!(
                            "🔄 [STARTUP] Epoch mismatch detected at Validator startup: Local epoch={}, Network epoch={}. Running cross-epoch sync...",
                            local_epoch, status.go_epoch
                        );
                        match cm.sync_go_to_current_epoch(status.go_epoch).await {
                            Ok(synced) => {
                                info!(
                                    "✅ [STARTUP] Cross-epoch sync complete: {} blocks synced. epoch_monitor will handle transition.",
                                    synced
                                );
                            }
                            Err(e) => {
                                warn!(
                                    "⚠️ [STARTUP] Cross-epoch sync failed: {}. Proceeding anyway — epoch_monitor may handle it later.",
                                    e
                                );
                            }
                        }
                    }
                    Ok(_status)
                        if matches!(
                            *cm.state.read().await,
                            crate::node::catchup::CatchupState::BehindRustLocal { .. }
                        ) =>
                    {
                        let state = cm.state.read().await.clone();
                        if let crate::node::catchup::CatchupState::BehindRustLocal {
                            target_block,
                            current_block,
                        } = state
                        {
                            warn!("🔄 [STARTUP] Go is behind local Rust storage! Fast-forwarding directly from local DB ({} -> {})...", current_block, target_block);
                            let storage_path = std::path::Path::new(&self.node_config.storage_path);
                            match cm
                                .sync_blocks_from_local_rust(
                                    storage_path,
                                    current_block,
                                    target_block,
                                )
                                .await
                            {
                                Ok(synced) => {
                                    info!("✅ [STARTUP] Local rust fast-forward complete: {} blocks synced directly.", synced);
                                }
                                Err(e) => {
                                    warn!("⚠️ [STARTUP] Local rust fast-forward failed: {}. Will retry or fallback...", e);
                                }
                            }
                        }
                    }
                    Ok(status) if status.block_gap > 10 => {
                        // ═══════════════════════════════════════════════════════
                        // SNAPSHOT RESTORE BLOCK GAP FIX: Epoch matches, but Go
                        // is behind network by >10 blocks (e.g., snapshot at 3050
                        // but network at 3255). Without syncing these blocks from
                        // peers FIRST, the Rust commit processor will ask Go for
                        // blocks that don't exist yet, and block sync keeps
                        // requesting from Go Master returning 0 blocks forever.
                        // ═══════════════════════════════════════════════════════
                        warn!(
                            "🔄 [STARTUP] Epoch matches but Go is behind by {} blocks (Go={}, Network={}). Syncing gap from peers...",
                            status.block_gap, status.go_last_block, status.network_block_height
                        );
                        let mut current_go_block = status.go_last_block;
                        let target = status.network_block_height;
                        let mut total_synced = 0u64;
                        while current_go_block < target {
                            let fetch_to = std::cmp::min(current_go_block + 50, target);
                            match cm.sync_blocks_from_peers(current_go_block, fetch_to).await {
                                Ok(synced) => {
                                    if synced == 0 {
                                        break;
                                    }
                                    current_go_block += synced;
                                    total_synced += synced;
                                }
                                Err(e) => {
                                    warn!("⚠️ [STARTUP] Block gap sync failed: {}. Proceeding — commit processor will retry.", e);
                                    break;
                                }
                            }
                        }
                        info!(
                            "✅ [STARTUP] Block gap sync complete: {} blocks synced. Go should be near block {}.",
                            total_synced, current_go_block
                        );
                    }
                    Ok(_) => {
                        info!("✅ [STARTUP] Epoch matches network and block gap is small. Consensus syncing will handle any minor lag.");
                    }
                    Err(e) => {
                        warn!("⚠️ [STARTUP] Epoch check failed: {}. Proceeding anyway.", e);
                    }
                }
            }

            // Restore Validator mode by triggering a mode-only transition
            let was_syncing_up = {
                let node_guard = self.node.lock().await;
                node_guard.node_mode == crate::node::NodeMode::SyncingUp
            };

            if was_syncing_up {

                // Phase 3: Start ConsensusAuthority
                info!(
                    "✅ [STARTUP] Catch-up complete. Starting ConsensusAuthority for Validator..."
                );
                let (new_epoch, new_exec_index, go_last_block, executor_client_opt, peer_rpc_addresses) = {
                    let node_guard = self.node.lock().await;
                    // CRITICAL: Get Go's actual block number for state verification.
                    // last_global_exec_index is a DAG commit counter (e.g. 4629),
                    // NOT a Go block number (e.g. 110). Using GEI as block number
                    // causes get_blocks_range to request non-existent blocks → FATAL.
                    let go_block = if let Some(ref client) = node_guard.executor_client {
                        match client.get_last_block_number().await {
                            Ok((block, _gei, _ready)) => block,
                            Err(e) => {
                                warn!("⚠️ [STATE VERIFY] Could not query Go block number: {}. Falling back to 0.", e);
                                0
                            }
                        }
                    } else {
                        0
                    };
                    (
                        node_guard.current_epoch,
                        node_guard.last_global_exec_index,
                        go_block,
                        node_guard.executor_client.clone(),
                        self.node_config.peer_rpc_addresses.clone(),
                    )
                };

                // ═══════════════════════════════════════════════════════════════
                // Phase 2.5: STATE VERIFICATION GATE
                // CRITICAL: Use go_last_block (actual Go block number), NOT
                // new_exec_index (global_exec_index / DAG commit counter).
                // These are different counters: block=110, gei=4629.
                // ═══════════════════════════════════════════════════════════════
                if go_last_block > 0 && !peer_rpc_addresses.is_empty() {
                    info!(
                        "🔍 [STATE VERIFY] Starting State Verification Gate for block {} (exec_index={})...",
                        go_last_block, new_exec_index
                    );
                    let mut state_verified = false;
                    let mut retry_count = 0;

                    while !state_verified && retry_count < 3 {
                        // 1. Get LOCAL state root (using actual Go block number, NOT exec_index)
                        let mut local_state_root = vec![];
                        if let Some(ref client) = executor_client_opt {
                            if let Ok(blocks) = client.get_blocks_range(go_last_block, go_last_block).await {
                                if let Some(local_block) = blocks.first() {
                                    local_state_root = local_block.state_root.clone();
                                }
                            }
                        }

                        if local_state_root.is_empty() {
                            warn!("⚠️ [STATE VERIFY] Local Go missing block {}. Attempting to fetch from peers and sync...", go_last_block);
                            // ═══════════════════════════════════════════════════════════════════
                            // CRITICAL FIX: Go may have last_block_number counter but missing
                            // actual block data (due to snapshot corruption or incomplete sync).
                            // Fetch the missing block from peers and sync to Go before retrying.
                            // ═══════════════════════════════════════════════════════════════════
                            let mut synced = false;
                            for peer in &peer_rpc_addresses {
                                match crate::network::peer_rpc::fetch_blocks_from_peer(&[peer.clone()], go_last_block, go_last_block).await {
                                    Ok(blocks) if !blocks.is_empty() => {
                                        if let Some(peer_block) = blocks.first() {
                                            info!("📥 [STATE VERIFY] Fetched block {} from peer {}, syncing to local Go...", go_last_block, peer);
                                            // Sync block to local Go
                                            if let Some(ref client) = executor_client_opt {
                                                match client.sync_blocks(blocks).await {
                                                    Ok((count, _last)) => {
                                                        info!("✅ [STATE VERIFY] Synced {} blocks to Go, retrying verification...", count);
                                                        synced = true;
                                                        break;
                                                    }
                                                    Err(e) => {
                                                        warn!("⚠️ [STATE VERIFY] Failed to sync block to Go: {}", e);
                                                    }
                                                }
                                            }
                                        }
                                    }
                                    Ok(_) => {}
                                    Err(e) => {
                                        warn!("⚠️ [STATE VERIFY] Failed to fetch block {} from peer {}: {}", go_last_block, peer, e);
                                    }
                                }
                            }
                            
                            if synced {
                                // Retry immediately after sync
                                continue;
                            }
                            
                            warn!("⚠️ [STATE VERIFY] Could not fetch local state root for block {} and peer sync failed. Retrying...", go_last_block);
                            retry_count += 1;
                            tokio::time::sleep(std::time::Duration::from_secs(2)).await;
                            continue;
                        }

                        let local_root_hex = hex::encode(&local_state_root);
                        let local_root_preview = if local_root_hex.len() > 16 {
                            format!("{}...", &local_root_hex[..16])
                        } else {
                            local_root_hex.clone()
                        };
                        info!("🔍 [STATE VERIFY] Local StateRoot at block {}: {}", go_last_block, local_root_preview);

                        // 2. Get NETWORK state root (same block number from peers)
                        let mut match_count = 0;
                        let mut peer_roots = std::collections::HashMap::new();

                        for peer in &peer_rpc_addresses {
                            if let Ok(blocks) = crate::network::peer_rpc::fetch_blocks_from_peer(&[peer.clone()], go_last_block, go_last_block).await {
                                if let Some(peer_block) = blocks.first() {
                                    if peer_block.state_root.is_empty() { continue; }
                                    let peer_root_hex = hex::encode(&peer_block.state_root);
                                    *peer_roots.entry(peer_root_hex.clone()).or_insert(0) += 1;

                                    if peer_block.state_root == local_state_root {
                                        match_count += 1;
                                    }
                                }
                            }
                        }

                        // 3. Compare: require at least 1 peer match (or 2f+1 for larger networks)
                        let f = peer_rpc_addresses.len() / 3;
                        let required_matches = std::cmp::max(1, f * 2 + 1);

                        if match_count >= required_matches {
                            info!("✅ [STATE VERIFY] PASS! Local stateRoot at block {} matches {} peers (required: {}).", go_last_block, match_count, required_matches);
                            state_verified = true;
                        } else if peer_roots.is_empty() {
                            warn!("⚠️ [STATE VERIFY] Could not fetch stateRoot from any peers for block {}.", go_last_block);
                            retry_count += 1;
                            tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                        } else {
                            warn!("🚨 [STATE VERIFY] FAIL! Local stateRoot {} at block {} matched only {} peers (required: {}).", local_root_preview, go_last_block, match_count, required_matches);
                            for (root, count) in peer_roots {
                                let root_preview = if root.len() > 16 { format!("{}...", &root[..16]) } else { root };
                                warn!("   Network Root: {} ({} peers)", root_preview, count);
                            }
                            retry_count += 1;
                            if retry_count < 3 {
                                warn!("🔄 [STATE VERIFY] Retrying... {}/3", retry_count);
                                tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                            }
                        }
                    }

                    if !state_verified {
                        error!("❌ [STATE VERIFY] FATAL ERROR: Node has diverged from network state at block {} (exec_index={})! Halting before joining consensus!", go_last_block, new_exec_index);
                        return Err(anyhow::anyhow!("State verification failed at block {} (exec_index={})", go_last_block, new_exec_index));
                    }
                } else if peer_rpc_addresses.is_empty() {
                    info!("⏭️ [STATE VERIFY] SKIPPING: No peers configured.");
                }

                let mut node_guard = self.node.lock().await;

                if let Err(e) = crate::node::transition::mode_transition::transition_mode_only(
                    &mut node_guard,
                    new_epoch,
                    0,
                    new_exec_index,
                    &self.node_config,
                )
                .await
                {
                    error!("❌ [STARTUP] Failed to transition to Validator mode: {}", e);
                    node_guard.node_mode = crate::node::NodeMode::Validator;
                } else {
                    info!(
                        "✅ [STARTUP] Node is now a Validator at epoch {}! exec_index={}",
                        new_epoch, new_exec_index
                    );
                }

                drop(node_guard);

                // ═══════════════════════════════════════════════════════════════
                // Phase 4: UNIFIED STATE ARCHITECTURE
                //
                // DualStreamController has been REMOVED.
                // Rust consensus (Core and CommitProcessor) is now the ONLY
                // component authorized to sync and deliver blocks to Go Master.
                // Go Master strictly executes whatever Rust provides.
                // ═══════════════════════════════════════════════════════════════
            }
        }

        info!("Press Ctrl+C to stop the node");
        tokio::signal::ctrl_c().await?;
        info!("Received Ctrl+C, initiating shutdown...");
        self.shutdown().await
    }

    /// Shutdown the node and all servers
    /// Thứ tự tắt được tối ưu để đảm bảo data integrity:
    /// 1. Shutdown consensus connections/tasks (để tránh new blocks)
    /// 2. Flush remaining blocks to Go Master (đảm bảo không mất blocks)
    /// 3. Shutdown servers (dừng accept new requests)
    pub async fn shutdown(self) -> Result<()> {
        info!("Shutting down node...");

        // 1. Shutdown servers FIRST (stop accepting new requests)
        if let Some(handle) = self.rpc_server_handle {
            handle.abort();
            // Optional: wait for it to finish
            let _ = handle.await;
        }
        if let Some(handle) = self.uds_server_handle {
            handle.abort();
            let _ = handle.await;
        }

        // 2. Lock node and perform shutdown sequence
        // We use lock() instead of try_unwrap() because the node is shared (e.g. global registry)
        let mut node = self.node.lock().await;

        // 3. Flush remaining blocks to Go Master
        if let Err(e) = node.flush_blocks_to_go_master().await {
            warn!("⚠️ [SHUTDOWN] Failed to flush blocks: {}", e);
        }

        // 4. Shutdown consensus connections/tasks
        if let Err(e) = node.shutdown().await {
            error!("❌ [SHUTDOWN] Error during consensus shutdown: {}", e);
        }

        info!("Node stopped gracefully");
        Ok(())
    }
}
