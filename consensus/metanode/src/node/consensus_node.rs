// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! ConsensusNode constructors and orchestrator.
//!
//! Contains the main `new*` constructors for ConsensusNode, which orchestrate the initialization phases:
//! - Phase 1 (`setup_storage()`) — defined in `setup_storage.rs`
//! - Phase 2 (`setup_consensus()`) — defined in `setup_consensus.rs`
//! - Phase 3 (`setup_networking()`) — clock and NTP sync
//! - Phase 4 (`setup_epoch_management()`) — final assembly and epoch transition management

use crate::consensus::clock_sync::ClockSyncManager;
use anyhow::Result;
use consensus_core::ReconfigState;
use prometheus::Registry;
use std::sync::Arc;
use tokio::sync::RwLock;
use tracing::info;

use crate::config::NodeConfig;
use crate::node::epoch_store::load_legacy_epoch_stores;

use super::{ConsensusNode, NodeMode, StorageSetup, ConsensusSetup};
use super::{epoch_monitor, epoch_transition_manager, recovery};

impl ConsensusNode {
    #[allow(dead_code)]
    pub async fn new(config: NodeConfig) -> Result<Self> {
        Self::new_with_registry(config, Registry::new()).await
    }

    #[allow(dead_code)]
    pub async fn new_with_registry(config: NodeConfig, registry: Registry) -> Result<Self> {
        Self::new_with_registry_and_service(config, registry).await
    }

    /// Main constructor — delegates to setup helpers.
    pub async fn new_with_registry_and_service(
        config: NodeConfig,
        registry: Registry,
    ) -> Result<Self> {
        info!("Initializing consensus node {}...", config.node_id);

        // Phase 1: Storage, epoch discovery, committee, execution index, identity (defined in setup_storage.rs)
        let mut storage = Self::setup_storage(&config).await?;

        let coordination_hub = consensus_core::coordination_hub::ConsensusCoordinationHub::new();

        // Phase 2: Consensus params, commit processor, authority start (defined in setup_consensus.rs)
        let consensus = Self::setup_consensus(&config, &mut storage, &registry, coordination_hub.clone()).await?;

        // Phase 3: Clock/NTP sync
        let clock_sync_manager = Self::setup_networking(&config);

        // Phase 4: Assemble node + epoch management
        let node =
            Self::setup_epoch_management(config, storage, consensus, clock_sync_manager, &registry, coordination_hub)
                .await?;

        Ok(node)
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

        let mut node = ConsensusNode {
            authority: consensus.authority,
            legacy_store_manager: Arc::new(consensus_core::LegacyEpochStoreManager::new(
                config.epochs_to_keep,
            )),
            coordination_hub: coordination_hub.clone(),
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
            epoch_pending_transactions: Arc::new(tokio::sync::Mutex::new(std::collections::HashMap::new())),
            committed_transaction_hashes: consensus.committed_transaction_hashes,
            pending_epoch_transitions: Arc::new(tokio::sync::Mutex::new(Vec::new())),
            _commit_consumer_holder: consensus.commit_consumer_holder,
            epoch_eth_addresses: consensus.epoch_eth_addresses_arc.clone(),

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
        load_legacy_epoch_stores(
            &node.legacy_store_manager,
            &config.storage_path,
            storage.current_epoch,
            config.epochs_to_keep,
        );

        crate::consensus::epoch_transition::start_epoch_transition_handler(
            consensus.epoch_tx_receiver,
            config.clone(),
        );

        node.check_and_update_node_mode(&storage.committee, &config, false)
            .await?;

        // Start sync task for SyncOnly nodes
        if matches!(node.node_mode, NodeMode::SyncOnly) {
            let _ = node.start_sync_task(&config).await;
        }

        // UNIFIED EPOCH MONITOR
        epoch_monitor::stop_epoch_monitor(node.epoch_monitor_handle.take()).await;
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
