// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Full epoch transitions (epoch N → N+1).
//!
//! ARCHITECTURE (2026-04-24):
//! This module handles a SINGLE epoch transition triggered by an EndOfEpoch
//! system transaction from CommitProcessor (Validator) or epoch_monitor (SyncOnly).
//!
//! Multi-epoch catch-up is NOT handled here — epoch_monitor polls the network
//! and triggers sequential transitions (one per poll cycle) until caught up.
//!
//! Steps:
//! 1. Guard: duplicate/case check, is_transitioning lock
//! 2. Wait for commit processor to flush (Validator only)
//! 3. Stop old authority, flush blocks to Go, poll Go GEI
//! 4. Disk cleanup + state update
//! 5. Advance Go epoch
//! 6. Fetch committee → determine role
//! 7. Start authority (Validator) or sync task (SyncOnly)
//! 8. Verification + transaction recovery

use crate::config::NodeConfig;
use crate::node::executor_client::ExecutorClient;
use crate::node::{ConsensusNode, NodeMode};
use anyhow::Result;
use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::Duration;
use tracing::{error, info, warn};

use super::consensus_setup::{setup_synconly_sync, setup_validator_consensus};
use super::demotion::determine_role_and_check_transition;
use super::mode_transition::transition_mode_only;
use super::tx_recovery::recover_epoch_pending_transactions;
use super::verification::{
    verify_epoch_consistency, wait_for_consensus_ready,
};

pub async fn transition_to_epoch_from_system_tx(
    node: &mut ConsensusNode,
    new_epoch: u64,
    boundary_timestamp_ms: u64,
    boundary_block: u64,
    synced_global_exec_index: u64,
    config: &NodeConfig,
) -> Result<()> {
    // =========================================================================
    // STEP 1: Guard — duplicate/case check
    // =========================================================================
    let is_sync_only = matches!(node.node_mode, NodeMode::SyncOnly);
    let is_same_epoch = node.current_epoch == new_epoch;

    // CASE 1: Same epoch, SyncOnly → Validator upgrade
    if is_same_epoch && is_sync_only {
        return super::mode_transition::handle_synconly_upgrade_wait(
            node,
            new_epoch,
            boundary_block,
            synced_global_exec_index,
            config,
        )
        .await;
    }

    // CASE 2: Already at this epoch and Validator — skip
    if node.current_epoch >= new_epoch && !is_sync_only {
        info!(
            "ℹ️ [TRANSITION SKIP] Already at epoch {} (requested: {}) and Validator. Skipping.",
            node.current_epoch, new_epoch
        );
        return Ok(());
    }

    // CASE 3: Current epoch ahead — skip
    if node.current_epoch > new_epoch {
        info!(
            "ℹ️ [TRANSITION SKIP] Current epoch {} AHEAD of requested {}. Skipping.",
            node.current_epoch, new_epoch
        );
        return Ok(());
    }

    // CASE 4: Full epoch transition
    info!(
        "🔄 [FULL EPOCH TRANSITION] Processing: epoch {} -> {} (current_mode={:?})",
        node.current_epoch, new_epoch, node.node_mode
    );

    if node.coordination_hub.swap_epoch_transitioning(true) {
        warn!("⚠️ Full epoch transition already in progress, skipping.");
        node.coordination_hub.set_epoch_transitioning(false);
        return Ok(());
    }

    // Reset flag guard — ensures is_transitioning is cleared even on panic/error
    struct Guard(Arc<std::sync::atomic::AtomicBool>);
    impl Drop for Guard {
        fn drop(&mut self) {
            if self.0.load(Ordering::SeqCst) {
                self.0.store(false, Ordering::SeqCst);
            }
        }
    }
    let _guard = Guard(node.coordination_hub.get_is_transitioning_ref());

    // =========================================================================
    // STEP 2: Discover committee source + provisional timestamp
    // =========================================================================
    let committee_source = crate::node::committee_source::CommitteeSource::discover(config).await?;

    if !committee_source.validate_epoch(new_epoch) {
        warn!(
            "⚠️ [TRANSITION] Epoch mismatch: Expected={}, Source={}. Proceeding with source.",
            new_epoch, committee_source.epoch
        );
    }

    let executor_client =
        committee_source.create_executor_client(&config.executor_send_socket_path);

    // SINGLE SOURCE OF TRUTH: Rust provides the exact timestamp of the commit that contained
    // the EndOfEpoch system transaction. Go MUST NOT use time.Now() or header fallback.
    let provisional_timestamp = boundary_timestamp_ms;
    info!(
        "ℹ️ [EPOCH TRANSITION] Sending exact timestamp_ms={} to Go for epoch {}. \
         Go MUST use this timestamp as authoritative.",
        provisional_timestamp, new_epoch
    );

    node.system_transaction_provider
        .update_epoch(new_epoch, provisional_timestamp)
        .await;

    // =========================================================================
    // STEP 3: Commit processor wait removed
    // =========================================================================
    // We no longer wait for the commit processor here because:
    // 1. CommitProcessor breaks its loop immediately after dispatching the EndOfEpoch commit.
    // 2. Its `current_commit_index` will never increment further for this epoch.
    // 3. Waiting here previously caused a 5-10s timeout stall on every transition.
    // 4. Actual synchronization with Go execution is safely handled in STEP 4 (poll_go_until_synced).
    info!("⚡ [TRANSITION] Skipping commit_processor wait (handled by Go poll)");

    // =========================================================================
    // STEP 4: Stop old authority, flush blocks, poll Go
    // =========================================================================
    let synced_index =
        stop_authority_and_poll_go(node, new_epoch, &executor_client, &committee_source).await?;

    // =========================================================================
    // STEP 5: Disk cleanup + state update
    // =========================================================================
    if config.epochs_to_keep > 0 {
        cleanup_old_epochs(node, new_epoch, config.epochs_to_keep);
    }

    // Update state
    node.current_epoch = new_epoch;
    node.current_commit_index.store(0, Ordering::SeqCst);
    node.coordination_hub.reset_quorum_commit_index(0);

    let effective_synced = std::cmp::max(synced_index, synced_global_exec_index);
    if effective_synced > synced_index {
        info!(
            "📊 [SYNC FLOOR] Using catch-up boundary {} instead of Go-reported {}",
            synced_global_exec_index, synced_index
        );
    }
    node.coordination_hub.set_initial_global_exec_index(effective_synced).await;
    node.last_global_exec_index = effective_synced;

    // Memory cleanup
    {
        let mut hashes = node.committed_transaction_hashes.lock().await;
        let old_count = hashes.len();
        hashes.clear();
        hashes.shrink_to_fit();
        if old_count > 0 {
            info!(
                "🧹 [MEMORY] Cleared {} committed_transaction_hashes",
                old_count
            );
        }
    }
    node.update_execution_lock_epoch(new_epoch).await;

    info!(
        "📊 [STATE UPDATED] epoch={}, last_gei={}, commit_index={}, mode={:?}",
        node.current_epoch,
        node.last_global_exec_index,
        node.current_commit_index.load(Ordering::SeqCst),
        node.node_mode
    );

    // =========================================================================
    // STEP 6: Advance Go epoch
    // =========================================================================
    let go_boundary = match executor_client.get_last_block_number().await {
        Ok(bn) => {
            info!(
                "✅ [EPOCH ADVANCE] Go's last_block_number={} (GEI={})",
                bn.0, effective_synced
            );
            bn.0
        }
        Err(e) => {
            warn!(
                "⚠️ [EPOCH ADVANCE] Failed to get Go block: {}. Using effective_synced={}",
                e, effective_synced
            );
            effective_synced
        }
    };

    info!(
        "📤 [EPOCH ADVANCE] epoch {} (boundary: block={}, gei={})",
        new_epoch, go_boundary, effective_synced
    );
    if let Err(e) = executor_client
        .advance_epoch(
            new_epoch,
            provisional_timestamp,
            go_boundary,
            effective_synced,
        )
        .await
    {
        warn!(
            "⚠️ [EPOCH ADVANCE] Failed for epoch {}: {}. Continuing...",
            new_epoch, e
        );
    }

    // =========================================================================
    // STEP 7: Determine role + fetch committee + start components
    // =========================================================================
    let own_protocol_pubkey = node.protocol_keypair.public();
    let (target_role, _needs_mode_change) = determine_role_and_check_transition(
        new_epoch,
        &node.node_mode,
        &own_protocol_pubkey,
        config,
    )
    .await?;

    info!(
        "📋 [ROLE] Epoch {}: target_role={:?}, current_mode={:?}",
        new_epoch, target_role, node.node_mode
    );

    node.close_user_certs().await;

    // Verify boundary consistency
    match executor_client.get_epoch_boundary_data(new_epoch).await {
        Ok((stored_epoch, _ts, stored_boundary, _, _, _)) => {
            if stored_boundary != go_boundary {
                error!(
                    "🚨 [BOUNDARY MISMATCH] Go stored={} but we sent {}!",
                    stored_boundary, go_boundary
                );
            } else {
                info!(
                    "✅ [CONTINUITY] epoch {} boundary: block={}",
                    stored_epoch, stored_boundary
                );
            }
        }
        Err(e) => warn!("⚠️ [VALIDATION SKIP] Cannot verify boundary: {}", e),
    }

    // Create consensus DB for new epoch
    let db_path = node
        .storage_path
        .join("epochs")
        .join(format!("epoch_{}", new_epoch))
        .join("consensus_db");
    if db_path.exists() {
        let _ = std::fs::remove_dir_all(&db_path);
    }
    std::fs::create_dir_all(&db_path)?;

    // Reset fragment offset for the new epoch (both regular and wipe-safe)
    if let Err(e) =
        crate::node::executor_client::persistence::reset_fragment_offset(&node.storage_path).await
    {
        warn!("⚠️ [PERSIST] Failed to reset fragment offset: {}", e);
    }
    if let Err(e) =
        crate::node::executor_client::persistence::reset_fragment_offset_wipe_safe(&node.storage_path).await
    {
        warn!("⚠️ [PERSIST] Failed to reset wipe-safe fragment offset: {}", e);
    }

    // Fetch committee with unified timestamp
    info!(
        "📋 [COMMITTEE] Fetching for epoch {} from {}",
        new_epoch, committee_source.socket_path
    );
    let (committee, epoch_timestamp_to_use, eth_addresses) = committee_source
        .fetch_committee_with_timestamp(&config.executor_send_socket_path, new_epoch)
        .await?;

    info!(
        "✅ [UNIFIED TIMESTAMP] epoch {}: {}ms",
        new_epoch, epoch_timestamp_to_use
    );

    node.system_transaction_provider
        .update_epoch(new_epoch, epoch_timestamp_to_use)
        .await;

    let committee_for_priority_check = committee.clone();

    // Update epoch_eth_addresses cache atomically
    {
        let mut cache = node.epoch_eth_addresses.write().await;
        cache.insert(new_epoch, eth_addresses);
        
        // Keep only last 2 epochs to prevent unbounded growth
        if cache.len() > 2 {
            let min_keep = new_epoch.saturating_sub(1);
            cache.retain(|&epoch, _| epoch >= min_keep);
        }
    }

    node.check_and_update_node_mode(&committee, config, true)
        .await?;

    // Update own_index from new committee
    let own_protocol_pubkey = node.protocol_keypair.public();
    if let Some((idx, _)) = committee
        .authorities()
        .find(|(_, a)| a.protocol_key == own_protocol_pubkey)
    {
        node.own_index = idx;
        info!("✅ [TRANSITION] Self in committee at index {}", idx);
    } else {
        node.own_index = consensus_config::AuthorityIndex::ZERO;
        info!("ℹ️ [TRANSITION] Not in new committee");
    }

    // Start consensus components based on mode
    let epoch_boundary_block = go_boundary;
    if matches!(node.node_mode, NodeMode::Validator) {
        setup_validator_consensus(
            node,
            new_epoch,
            effective_synced,
            epoch_timestamp_to_use,
            db_path,
            committee,
            config,
        )
        .await?;
    } else {
        setup_synconly_sync(
            node,
            new_epoch,
            effective_synced,
            epoch_timestamp_to_use,
            committee,
            config,
        )
        .await?;
    }

    // =========================================================================
    // STEP 8: Post-transition checks
    // =========================================================================
    if wait_for_consensus_ready(node).await {
        info!("✅ Consensus ready.");
    }

    let _ = recover_epoch_pending_transactions(node).await;

    node.coordination_hub.set_epoch_transitioning(false);
    let _ = node.submit_queued_transactions().await;

    // VALIDATOR PRIORITY FIX: After SyncOnly setup, re-check committee membership
    if matches!(node.node_mode, NodeMode::SyncOnly) {
        let own_key = node.protocol_keypair.public();
        let is_now_in_committee = committee_for_priority_check
            .authorities()
            .any(|(_, authority)| authority.protocol_key == own_key);

        if is_now_in_committee {
            info!(
                "🚀 [VALIDATOR PRIORITY] SyncOnly IS in committee for epoch {}! Upgrading.",
                new_epoch
            );
            let _upgrade_result = transition_mode_only(
                node,
                new_epoch,
                epoch_boundary_block,
                effective_synced,
                config,
            )
            .await;
            info!(
                "✅ [VALIDATOR PRIORITY] Upgrade complete: now {:?}",
                node.node_mode
            );
        }
    }

    node.reset_reconfig_state().await;

    // STEP 9: Verification
    verify_epoch_consistency(node, new_epoch, epoch_timestamp_to_use, &executor_client).await?;

    Ok(())
}

// =============================================================================
// HELPER: Stop old authority, flush blocks to Go, poll GEI
// =============================================================================

/// Stop old authority, preserve store, and poll Go until it has all old-epoch blocks.
/// Returns the verified synced_index from Go.
///
/// Ordering:
/// 1. Pre-shutdown flush (blocks in buffer → Go)
/// 2. authority.take() → preserve store in LegacyEpochStoreManager
/// 3. auth.stop()
/// 4. Post-shutdown flush (any remaining blocks)
/// 5. Poll Go GEI until caught up (executor nodes only)
/// 6. Return max(go_gei, go_block, expected_floor, committee_floor)
async fn stop_authority_and_poll_go(
    node: &mut ConsensusNode,
    new_epoch: u64,
    executor_client: &ExecutorClient,
    committee_source: &crate::node::committee_source::CommitteeSource,
) -> Result<u64> {
    let expected_last_block = {
        let gei_arc = node.coordination_hub.get_global_exec_index_ref();
        let shared_index = gei_arc.lock().await;
        *shared_index
    };
    info!(
        "🛑 [TRANSITION] Stopping old authority (expected_gei={})",
        expected_last_block
    );

    // Pre-shutdown flush
    if let Some(ref exec_client) = node.executor_client {
        match exec_client.flush_buffer().await {
            Ok(_) => info!("✅ Pre-shutdown buffer flush completed"),
            Err(e) => warn!("⚠️ Pre-shutdown flush failed: {}. Will retry after stop.", e),
        }
    }

    // Extract store and stop old authority
    if let Some(auth) = node.authority.take() {
        let old_store = auth.take_store();
        let old_epoch = new_epoch.saturating_sub(1);
        node.legacy_store_manager.add_store(old_epoch, old_store);
        info!("📦 Extracted store from epoch {} for legacy sync", old_epoch);

        auth.stop().await;
        info!("✅ Old authority stopped. Store preserved.");
    }

    // Post-shutdown flush with retry
    if let Some(ref exec_client) = node.executor_client {
        for retry in 0..5 {
            match exec_client.flush_buffer().await {
                Ok(_) => {
                    info!("✅ Post-shutdown flush completed (attempt {})", retry + 1);
                    break;
                }
                Err(e) => {
                    if retry < 4 {
                        warn!(
                            "⚠️ Post-shutdown flush attempt {} failed: {}. Retrying...",
                            retry + 1, e
                        );
                        tokio::time::sleep(Duration::from_millis(200)).await;
                    } else {
                        error!("❌ Post-shutdown flush FAILED after 5 attempts: {}", e);
                    }
                }
            }
        }

        // Log remaining buffer state
        let buffer = exec_client.send_buffer.lock().await;
        if !buffer.is_empty() {
            error!(
                "🚨 Buffer still has {} blocks after flush! Keys: {:?}",
                buffer.len(),
                buffer.keys().take(10).collect::<Vec<_>>()
            );
        }
    }

    // Poll Go GEI (executor nodes only)
    let is_executor = node
        .executor_client
        .as_ref()
        .map(|ec| ec.can_commit())
        .unwrap_or(false);

    if !is_executor {
        info!(
            "⏩ [SYNC SKIP] Non-executor node — skipping Go GEI wait (expected={})",
            expected_last_block
        );
    } else {
        poll_go_until_synced(executor_client, expected_last_block, new_epoch).await;
    }

    // Fetch final synced_index from Go
    let raw_synced_gei = executor_client
        .get_last_global_exec_index()
        .await
        .unwrap_or(0);
    let raw_synced_block = executor_client
        .get_last_block_number()
        .await
        .map(|(b, _, _, _, _)| b)
        .unwrap_or(0);
    let raw_synced = std::cmp::max(raw_synced_gei, raw_synced_block);

    // Committee floor
    let committee_floor = if committee_source.last_block > 0 {
        committee_source.last_block
    } else {
        0
    };

    // SAFETY FLOOR: Never let synced_index go below expected
    let synced_index = *[raw_synced, expected_last_block, committee_floor]
        .iter()
        .max()
        .unwrap();
    if synced_index > raw_synced {
        warn!(
            "🚨 [SYNC SAFETY] Go returned max(gei,block)={} < floor={}. Using floor!",
            raw_synced, synced_index
        );
    }

    info!(
        "📊 Final synced: {} (gei={}, block={}, expected={}, committee={})",
        synced_index, raw_synced_gei, raw_synced_block, expected_last_block, committee_floor
    );
    Ok(synced_index)
}

/// Poll Go's GEI until it reaches the expected value, with timeout.
///
/// OPTIMIZATION (2026-04-25): Reduced timeout from 300s to 30s and added
/// active ForceCommit to flush Go's async commitChannel pipeline.
/// Previously, 3 GEI updates could sit in Go's coalescing geiUpdateChan
/// or commitChannel buffer indefinitely, causing a 5-minute stall at
/// every epoch transition. ForceCommit triggers Go to flush its pipeline.
///
/// Also accepts a small tolerance (5 GEIs) for in-flight empty commits
/// that don't affect state correctness.
async fn poll_go_until_synced(
    executor_client: &ExecutorClient,
    expected_last_block: u64,
    _new_epoch: u64,
) {
    let poll_interval = Duration::from_millis(100);
    let max_wait = Duration::from_secs(30);
    let wait_start = std::time::Instant::now();
    let mut attempt = 0u64;
    let mut last_force_commit = std::time::Instant::now();

    // Tolerance: Accept if Go is within this many GEIs of expected.
    // In-flight empty commits in Go's async pipeline don't affect state.
    const GEI_TOLERANCE: u64 = 5;

    // Immediately trigger a ForceCommit to flush Go's pipeline
    if let Err(e) = executor_client
        .send_force_commit("epoch_transition_pre_flush".to_string())
        .await
    {
        warn!("⚠️ [SYNC POLL] Pre-flush ForceCommit failed: {}", e);
    }

    loop {
        attempt += 1;

        if wait_start.elapsed() > max_wait {
            warn!(
                "⏱️ [SYNC TIMEOUT] Giving up after {:?}. expected_gei={}. Continuing...",
                wait_start.elapsed(),
                expected_last_block
            );
            break;
        }

        match executor_client.get_last_global_exec_index().await {
            Ok(go_last_gei) => {
                if go_last_gei >= expected_last_block {
                    info!(
                        "✅ [SYNC VERIFIED] Go confirmed: gei={} >= expected={} ({} attempts, {:?})",
                        go_last_gei, expected_last_block, attempt, wait_start.elapsed()
                    );
                    break;
                }

                // TOLERANCE: Accept if gap is small (in-flight empty commits)
                let remaining = expected_last_block.saturating_sub(go_last_gei);
                if remaining <= GEI_TOLERANCE && wait_start.elapsed() > Duration::from_secs(5) {
                    info!(
                        "✅ [SYNC CLOSE-ENOUGH] gei={}, expected={}, gap={} <= tolerance={}. Proceeding. ({:?})",
                        go_last_gei, expected_last_block, remaining, GEI_TOLERANCE, wait_start.elapsed()
                    );
                    break;
                }

                // Trigger ForceCommit every 3 seconds to flush Go's pipeline
                if last_force_commit.elapsed() > Duration::from_secs(3) {
                    last_force_commit = std::time::Instant::now();
                    if let Err(e) = executor_client
                        .send_force_commit("epoch_transition_poll_flush".to_string())
                        .await
                    {
                        warn!("⚠️ [SYNC POLL] ForceCommit failed: {}", e);
                    }
                }

                // Periodic progress logging
                if attempt.is_multiple_of(50) {
                    warn!(
                        "⏳ [SYNC WAIT] gei={}, expected={}, gap={} (waiting {:?})",
                        go_last_gei, expected_last_block, remaining, wait_start.elapsed()
                    );
                }
            }
            Err(e) => {
                if attempt.is_multiple_of(50) {
                    error!(
                        "❌ [SYNC POLL] Cannot reach Go (attempt {}): {}",
                        attempt, e
                    );
                }
            }
        }
        tokio::time::sleep(poll_interval).await;
    }
}

// =============================================================================
// HELPER: Disk cleanup
// =============================================================================

/// Remove old epoch directories beyond epochs_to_keep.
fn cleanup_old_epochs(node: &mut ConsensusNode, new_epoch: u64, epochs_to_keep: usize) {
    let keep_from = new_epoch.saturating_sub(epochs_to_keep as u64);
    let epochs_dir = node.storage_path.join("epochs");
    if !epochs_dir.exists() {
        return;
    }

    if let Ok(entries) = std::fs::read_dir(&epochs_dir) {
        for entry in entries.flatten() {
            if let Some(name) = entry.file_name().to_str() {
                if let Some(epoch_str) = name.strip_prefix("epoch_") {
                    if let Ok(epoch) = epoch_str.parse::<u64>() {
                        if epoch < keep_from {
                            info!(
                                "🗑️ [CLEANUP] Removing epoch {} (keep_from={})",
                                epoch, keep_from
                            );
                            if let Err(e) = std::fs::remove_dir_all(entry.path()) {
                                warn!("⚠️ [CLEANUP] Failed to remove epoch {}: {}", epoch, e);
                            }
                            node.legacy_store_manager.remove_store(epoch);
                        }
                    }
                }
            }
        }
    }
}
