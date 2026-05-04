// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Post-transition verification, readiness checks, and timestamp synchronization.

use crate::node::executor_client::ExecutorClient;
use crate::node::tx_submitter::TransactionSubmitter;
use crate::node::ConsensusNode;
use anyhow::Result;
use std::time::Duration;
use tokio::time::sleep;
use tracing::{info, trace, warn};

/// Post-transition verification: ensure Go and Rust epochs match, sync timestamps.
pub(super) async fn verify_epoch_consistency(
    node: &mut ConsensusNode,
    new_epoch: u64,
    epoch_timestamp: u64,
    executor_client: &ExecutorClient,
) -> Result<()> {
    // FORK-SAFETY: Verify Go and Rust epochs match
    match executor_client.get_current_epoch().await {
        Ok(go_epoch) => {
            if go_epoch != new_epoch {
                warn!(
                    "⚠️ [EPOCH VERIFY] Go-Rust epoch mismatch! Rust: {}, Go: {}. \
                     This could indicate a fork risk. Consider investigating.",
                    new_epoch, go_epoch
                );
            } else {
                info!("✅ [EPOCH VERIFY] Go-Rust epoch consistent: {}", new_epoch);
            }
        }
        Err(e) => {
            warn!("⚠️ [EPOCH VERIFY] Failed to verify epoch with Go: {}", e);
        }
    }

    // FORK-SAFETY: Sync timestamp from Go to ensure consistency
    let go_epoch_timestamp_ms = match sync_epoch_timestamp_from_go(
        executor_client,
        new_epoch,
        epoch_timestamp,
    )
    .await
    {
        Ok(timestamp) => {
            if timestamp != epoch_timestamp {
                warn!(
                    "⚠️ [EPOCH TIMESTAMP SYNC] Timestamp mismatch: Local={}ms, Go={}ms, diff={}ms. Using Go's.",
                    epoch_timestamp, timestamp,
                    (timestamp as i64 - epoch_timestamp as i64).abs()
                );
            } else {
                info!(
                    "✅ [EPOCH TIMESTAMP SYNC] Timestamp consistent: {}ms",
                    epoch_timestamp
                );
            }
            timestamp
        }
        Err(e) => {
            if e.to_string().contains("not found") || e.to_string().contains("Unexpected response")
            {
                info!(
                    "ℹ️ [EPOCH TIMESTAMP SYNC] Go endpoint not implemented yet, using local: {}ms",
                    epoch_timestamp
                );
            } else {
                warn!(
                    "⚠️ [EPOCH TIMESTAMP SYNC] Failed to sync from Go: {}. Using local.",
                    e
                );
            }
            epoch_timestamp
        }
    };

    // Update SystemTransactionProvider with verified timestamp
    node.system_transaction_provider
        .update_epoch(new_epoch, go_epoch_timestamp_ms)
        .await;

    // Cross-epoch transition summary
    info!(
        "✅ [EPOCH TRANSITION COMPLETE] epoch={}, mode={:?}, last_global_exec_index={}, go_sync_complete={}",
        node.current_epoch,
        node.node_mode,
        node.last_global_exec_index,
        executor_client.get_last_block_number().await.map(|(b, _, _, _, _)| b).unwrap_or(0) >= node.last_global_exec_index
    );

    Ok(())
}



/// Wait for consensus to become ready with retries instead of fixed sleep
/// This replaces the unreliable 1000ms sleep with proper synchronization
pub(super) async fn wait_for_consensus_ready(node: &ConsensusNode) -> bool {
    let max_attempts = 20; // Up to 2 seconds with 100ms intervals
    let retry_delay = Duration::from_millis(100);

    for attempt in 1..=max_attempts {
        if test_consensus_readiness(node).await {
            return true;
        }

        if attempt < max_attempts {
            trace!(
                "⏳ Consensus not ready yet (attempt {}/{}), waiting...",
                attempt,
                max_attempts
            );
            sleep(retry_delay).await;
        }
    }

    warn!(
        "⚠️ Consensus failed to become ready after {} attempts",
        max_attempts
    );
    false
}

async fn test_consensus_readiness(node: &ConsensusNode) -> bool {
    if let Some(proxy) = &node.transaction_client_proxy {
        match tokio::time::timeout(
            std::time::Duration::from_millis(200),
            proxy.submit(vec![vec![0u8; 64]])
        ).await {
            Ok(res) => res.is_ok(),
            Err(_) => false, // Timeout
        }
    } else {
        false
    }
}

/// Sync epoch timestamp from Go with retry logic for epoch mismatch.
/// CRITICAL FIX: Timestamp difference NO LONGER causes retries.
/// Previously, a large timestamp_diff triggered retry loops that contributed
/// to cluster stalls. The root cause of wrong timestamps is on the Go side
/// (missing seconds→milliseconds conversion), and retrying doesn't fix it.
/// We now accept Go's timestamp immediately and log a warning.
pub(super) async fn sync_epoch_timestamp_from_go(
    executor_client: &ExecutorClient,
    expected_epoch: u64,
    expected_timestamp: u64,
) -> Result<u64> {
    const MAX_RETRIES: u32 = 5;
    const RETRY_DELAY_MS: u64 = 200;

    for attempt in 1..=MAX_RETRIES {
        // First check if Go has transitioned to expected epoch
        match executor_client.get_current_epoch().await {
            Ok(go_current_epoch) => {
                if go_current_epoch != expected_epoch {
                    if attempt == MAX_RETRIES {
                        return Err(anyhow::anyhow!(
                            "Go still in epoch {} after {} attempts, expected epoch {}",
                            go_current_epoch,
                            MAX_RETRIES,
                            expected_epoch
                        ));
                    }
                    warn!(
                        "⚠️ [EPOCH SYNC] Go still in epoch {} (attempt {}/{}), expected {}. Retrying...",
                        go_current_epoch, attempt, MAX_RETRIES, expected_epoch
                    );
                    sleep(Duration::from_millis(RETRY_DELAY_MS)).await;
                    continue;
                }
            }
            Err(e) => {
                warn!(
                    "⚠️ [EPOCH SYNC] Failed to get current epoch from Go (attempt {}/{}): {}",
                    attempt, MAX_RETRIES, e
                );
                if attempt == MAX_RETRIES {
                    return Err(anyhow::anyhow!(
                        "Failed to verify Go epoch after transition: {}",
                        e
                    ));
                }
                sleep(Duration::from_millis(RETRY_DELAY_MS)).await;
                continue;
            }
        }

        // Now get timestamp — ACCEPT IMMEDIATELY regardless of difference
        match executor_client
            .get_epoch_start_timestamp(expected_epoch)
            .await
        {
            Ok(go_timestamp) => {
                let timestamp_diff =
                    (go_timestamp as i64 - expected_timestamp as i64).unsigned_abs();

                if timestamp_diff > 10000 {
                    // Log warning but DO NOT retry — accept Go's value
                    // The timestamp mismatch is likely a seconds-vs-milliseconds conversion
                    // bug on the Go side. Retrying won't fix it and just delays consensus.
                    warn!(
                        "⚠️ [EPOCH SYNC] Go timestamp {}ms differs from expected {}ms by {}ms. \
                         ACCEPTING Go's value to avoid blocking consensus. \
                         Root cause: likely missing seconds→ms conversion on Go side.",
                        go_timestamp, expected_timestamp, timestamp_diff
                    );
                } else {
                    info!(
                        "✅ [EPOCH SYNC] Successfully synced timestamp from Go: {}ms (diff: {}ms)",
                        go_timestamp, timestamp_diff
                    );
                }
                return Ok(go_timestamp);
            }
            Err(e) => {
                warn!(
                    "⚠️ [EPOCH SYNC] Failed to get timestamp from Go (attempt {}/{}): {}",
                    attempt, MAX_RETRIES, e
                );
                if attempt == MAX_RETRIES {
                    return Err(anyhow::anyhow!(
                        "Failed to get timestamp from Go after transition: {}",
                        e
                    ));
                }
                sleep(Duration::from_millis(RETRY_DELAY_MS)).await;
                continue;
            }
        }
    }

    Err(anyhow::anyhow!(
        "Failed to sync epoch timestamp from Go after {} attempts",
        MAX_RETRIES
    ))
}
