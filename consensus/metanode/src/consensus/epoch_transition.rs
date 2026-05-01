// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use tokio::sync::mpsc::UnboundedReceiver;
use tracing::{error, info, warn};

use crate::config::NodeConfig;

/// Starts the epoch transition handler task
/// This task processes epoch transition requests from system transactions
pub fn start_epoch_transition_handler(
    mut receiver: UnboundedReceiver<(u64, u64, u64, u64)>,
    config: NodeConfig,
) {
    tokio::spawn(async move {
        while let Some((new_epoch, boundary_timestamp_ms, boundary_block, synced_global_exec_index)) =
            receiver.recv().await
        {
            info!(
                "🚀 [EPOCH TRANSITION HANDLER] Processing transition request (source=system_tx): epoch={}, boundary_timestamp_ms={}, boundary_block={}, synced_global_exec_index={}",
                new_epoch, boundary_timestamp_ms, boundary_block, synced_global_exec_index
            );

            // Check with EpochTransitionManager before proceeding
            // This prevents race conditions with epoch_monitor
            let epoch_manager = match crate::node::epoch_transition_manager::get_epoch_manager() {
                Some(m) => m,
                None => {
                    // Manager not initialized yet, skip this message
                    warn!("⚠️ [EPOCH TRANSITION HANDLER] Epoch manager not initialized yet, will retry");
                    continue;
                }
            };

            // Try to acquire transition lock
            if let Err(e) = epoch_manager
                .try_start_epoch_transition(new_epoch, "system_tx")
                .await
            {
                // ===========================================================================
                // CRITICAL FIX: EpochAlreadyCurrent does NOT mean we should skip!
                //
                // SCENARIO: DEFERRED EPOCH for SyncOnly nodes
                // 1. Sync catches up, calls advance_epoch(3)
                // 2. epoch_manager.current_epoch is set to 3
                // 3. Signal sent to epoch_transition_handler for epoch 3
                // 4. try_start_epoch_transition(3) returns EpochAlreadyCurrent
                // 5. OLD CODE: `continue` → SKIP! → SyncOnly never upgrades to Validator!
                //
                // FIX: When EpochAlreadyCurrent, check if this is a same-epoch mode upgrade
                // (SyncOnly → Validator). If so, still call transition_to_epoch_from_system_tx
                // which will handle the mode-only transition.
                // ===========================================================================
                let is_epoch_current = matches!(
                    e,
                    crate::node::epoch_transition_manager::TransitionError::EpochAlreadyCurrent { .. }
                );

                if is_epoch_current {
                    // Check if node is SyncOnly - might need mode upgrade
                    if let Some(node_arc) = crate::node::get_transition_handler_node().await {
                        let node_guard = node_arc.lock().await;
                        let is_sync_only =
                            matches!(node_guard.node_mode, crate::node::NodeMode::SyncOnly);
                        drop(node_guard);

                        if is_sync_only {
                            info!(
                                "🔄 [EPOCH TRANSITION HANDLER] Epoch {} already current, but node is SyncOnly. Checking for mode upgrade...",
                                new_epoch
                            );
                            // Don't continue - let the transition function handle mode-only transition
                            // Fall through to the transition code below
                        } else {
                            info!(
                                "⏳ [EPOCH TRANSITION HANDLER] Epoch {} already current and already Validator. Skipping.",
                                new_epoch
                            );
                            continue;
                        }
                    } else {
                        continue;
                    }
                } else {
                    info!(
                        "⏳ [EPOCH TRANSITION HANDLER] Cannot start transition: {} (another source may be handling it)",
                        e
                    );
                    continue;
                }
            }

            // [FIX CRITICAL]: Không update provider ở đây.
            // Nếu update trước, đồng hồ đếm giờ của Provider sẽ bị reset.
            // Nếu sau đó Node chuyển đổi thất bại, hệ thống sẽ bị kẹt vì Provider nghĩ rằng đã sang epoch mới.

            // Try to get node from global registry and call transition function
            if let Some(node_arc) = crate::node::get_transition_handler_node().await {
                let mut node_guard = node_arc.lock().await;

                // Thực hiện chuyển đổi trên Node trước
                if let Err(e) = node_guard
                    .transition_to_epoch_from_system_tx(
                        new_epoch,
                        boundary_timestamp_ms,
                        boundary_block,
                        synced_global_exec_index,
                        &config,
                    )
                    .await
                {
                    // Mark transition as failed in manager
                    epoch_manager.fail_transition(&e.to_string()).await;

                    error!(
                        "❌ [EPOCH TRANSITION HANDLER] Failed to transition epoch: {}",
                        e
                    );
                    // Nếu thất bại: Provider KHÔNG được update.
                    // Provider sẽ tiếp tục thấy epoch cũ -> tiếp tục bắn System Transaction -> Hệ thống sẽ thử lại (retry).
                } else {
                    // Mark transition as complete in manager
                    epoch_manager.complete_epoch_transition(new_epoch).await;

                    info!(
                        "✅ [EPOCH TRANSITION HANDLER] Successfully transitioned to epoch {}",
                        new_epoch
                    );

                    // [FIX DONE]: Chỉ update Provider khi Node đã chuyển đổi thành công.
                    // Lúc này mới an toàn để reset đồng hồ cho epoch tiếp theo.
                    // NOTE: The actual timestamp is now derived inside transition function from Go's boundary block header.
                    // The transition function `transition_to_epoch_from_system_tx` ALREADY called 
                    // `update_epoch` with the correct derived timestamp.
                    // DO NOT call it here again, because `boundary_timestamp_ms` is actually the `boundary_block` 
                    // (e.g. 522), and passing it here would set the epoch start time to 1970, causing 
                    // an immediate unintended EndOfEpoch trigger.
                }
            } else {
                // No node available, fail the transition
                epoch_manager.fail_transition("Node not registered").await;

                warn!("⚠️ [EPOCH TRANSITION HANDLER] Node not registered in global registry yet - transition will be handled when node is available");
                // Không update provider -> Hệ thống sẽ tiếp tục thử lại ở lần check tiếp theo
            }
        }
    });
}
