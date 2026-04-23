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

            info!("✅ [STARTUP] Validator started with consensus authority active. CommitSyncer will handle any catch-up.");

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
