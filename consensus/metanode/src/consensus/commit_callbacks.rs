// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;

use tracing::{info, warn};

/// Creates a commit index callback that updates the shared commit index
pub fn create_commit_index_callback(
    current_commit_index: Arc<AtomicU32>,
    commit_consumer_monitor: Arc<consensus_core::CommitConsumerMonitor>,
) -> impl Fn(u32) + Send + Sync + 'static {
    move |index| {
        current_commit_index.store(index, Ordering::SeqCst);
        commit_consumer_monitor.set_highest_handled_commit(index);
    }
}

/// Creates a global execution index callback that updates the shared global exec index
pub fn create_global_exec_index_callback(
    _shared_last_global_exec_index: Arc<tokio::sync::Mutex<u64>>,
) -> impl Fn(u64) + Send + Sync + 'static {
    move |global_exec_index| {
        // We no longer asynchronously overwrite the shared_last_global_exec_index here.
        // During rapid node recovery, spawning tasks to overwrite this state caused
        // severe out-of-order execution race conditions (shrinking the GEI lock value).
        // CommitProcessor already synchronously and authoritatively updates the state.
        
        // Log periodically to avoid spamming during catch-up
        if global_exec_index % 500 == 0 {
            info!(
                "✅ [GLOBAL_EXEC_INDEX] Advanced to {}",
                global_exec_index
            );
        }
    }
}

/// Creates an epoch transition callback that sends transition requests via channel
pub fn create_epoch_transition_callback(
    epoch_transition_sender: tokio::sync::mpsc::UnboundedSender<(u64, u64, u64, u64)>,
) -> impl Fn(u64, u64, u64, u64) -> Result<(), anyhow::Error> + Send + Sync + 'static {
    move |new_epoch, boundary_timestamp_ms, boundary_block, synced_global_exec_index| {
        info!("🎯 [SYSTEM TX CALLBACK] EndOfEpoch detected: epoch={}, boundary_timestamp_ms={}, boundary_block={}, synced_global_exec_index={}",
            new_epoch, boundary_timestamp_ms, boundary_block, synced_global_exec_index);

        if let Err(e) = epoch_transition_sender.send((
            new_epoch,
            boundary_timestamp_ms,
            boundary_block,
            synced_global_exec_index,
        )) {
            warn!(
                "❌ [SYSTEM TX CALLBACK] Failed to send epoch transition request: {}",
                e
            );
            return Err(anyhow::anyhow!(
                "Failed to send epoch transition request: {}",
                e
            ));
        }
        Ok(())
    }
}
