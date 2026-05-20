// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::node::executor_client::block_sending::MAX_TXS_PER_GO_BLOCK;
use crate::node::executor_client::ExecutorClient;
use anyhow::Result;
use consensus_core::{storage::rocksdb_store::RocksDBStore, storage::Store, BlockAPI, CommitAPI};
use std::sync::Arc;
use tracing::{error, info, warn};

pub async fn perform_block_recovery_check(
    executor_client: &Arc<ExecutorClient>,
    go_last_block: u64,
    epoch_base_exec_index: u64,
    current_epoch: u64,
    db_path: &std::path::PathBuf,
    node_id: u32,
    max_allowed_gei: u64,
) -> Result<()> {
    if node_id != 0 {
        // info!("ℹ️ [RECOVERY] Node ID is {}, enabling recovery for all validators.", node_id);
    }
    info!(
        "🔍 [RECOVERY] Checking for missing blocks from index {} (epoch_base={})...",
        go_last_block + 1,
        epoch_base_exec_index
    );

    // The recovery store is now passed in as an argument to avoid RocksDB lock conflicts.
    let start_global = go_last_block + 1;
    if start_global <= epoch_base_exec_index {
        info!(
            "⚠️ [RECOVERY] Go Master (GEI {}) is behind the current epoch base (GEI {}). \
            Deferring recovery to Go's network sync mechanism.",
            go_last_block, epoch_base_exec_index
        );
        return Ok(());
    }

    // Ensure db exists before attempting to read it
    if !db_path.exists() {
        info!("✅ [RECOVERY] Local DB path does not exist. No commits to recover.");
        return Ok(());
    }

    let recovery_store = RocksDBStore::new(db_path.to_str().unwrap());
    
    // We MUST scan from the beginning of the epoch to reconstruct the fragment offset.
    // Assuming commit_index starts at 1 for the first commit after genesis/epoch base.
    let range = consensus_core::CommitRange::new(1..=u32::MAX);
    let commits = recovery_store.scan_commits(range)?;

    if commits.is_empty() {
        info!("✅ [RECOVERY] No missing commits found in local DB.");
        return Ok(());
    }

    let mut next_required_global = go_last_block + 1;
    let mut cumulative_fragment_offset: u64 = 0;
    let mut missing_commits_found = false;

    info!(
        "🔍 [RECOVERY] Scanning {} commits to reconstruct GEI fragmentation offset...",
        commits.len()
    );

    // ═══════════════════════════════════════════════════════════════════
    // FORK-SAFETY FIX (C2): Track cumulative fragment offset during recovery.
    //
    // When a commit has >MAX_TXS_PER_GO_BLOCK TXs, send_committed_subdag
    // fragments it into N blocks, each consuming 1 GEI. So a fragmented
    // commit consumes N GEIs instead of 1.
    // We must accumulate `cumulative_fragment_offset` from commit 1 to ensure
    // that `commit_start_gei` is correctly aligned with the original GEI stream.
    // ═══════════════════════════════════════════════════════════════════

    for commit in commits {
        let commit_index = commit.index();
        if commit_index == 0 {
            continue; // Genesis/Epoch start is handled locally
        }

        let commit_start_gei = epoch_base_exec_index + commit_index as u64 + cumulative_fragment_offset;

        // Reconstruct CommittedSubDag
        // Note: reputation_scores are not critical for execution replay, passing empty
        let subdag = match consensus_core::try_load_committed_subdag_from_store(
            &recovery_store,
            commit,
            vec![],
        ) {
            Ok(s) => s,
            Err(e) => {
                warn!("⚠️ [RECOVERY] Critical failure loading commit {}: {}. Recovery cannot proceed sequentially.", commit_index, e);
                return Err(anyhow::anyhow!("Missing block data for commit {}. Deferring to network sync.", commit_index));
            }
        };

        // Count total TXs to determine how many GEIs this commit will consume
        let total_txs: usize = subdag.blocks.iter().map(|b| b.transactions().len()).sum();
        let geis_consumed: u64 = if total_txs > MAX_TXS_PER_GO_BLOCK {
            let fragments = total_txs.div_ceil(MAX_TXS_PER_GO_BLOCK);
            fragments as u64
        } else {
            1
        };

        let commit_end_gei = commit_start_gei + geis_consumed;

        // Update the offset for the NEXT commit
        if geis_consumed > 1 {
            cumulative_fragment_offset += geis_consumed - 1;
        }

        // If this commit is completely older than what we need, skip it
        if commit_end_gei <= next_required_global {
            continue;
        }
        
        // Strict Height Enforcement: Do not replay ghost commits beyond the authoritative network state
        if commit_start_gei > max_allowed_gei {
            warn!(
                "🛑 [RECOVERY] Strict Height Enforcement: Commit starts at GEI {} which exceeds network max_allowed_gei {}. \
                 Stopping recovery to prevent phantom GEI increments / fork.",
                commit_start_gei, max_allowed_gei
            );
            break;
        }

        // GAP DETECTED!
        // This is critical: if we skip a block, Go Master will buffer forever waiting for it.
        if commit_start_gei > next_required_global {
            let error_msg = format!(
                "🚨 [RECOVERY CRITICAL] Gap detected in block sequence! Expected global_exec_index={}, but commit {} starts at {}. Missing {} blocks. Recovery cannot proceed sequentially.",
                next_required_global, commit_index, commit_start_gei, commit_start_gei - next_required_global
             );
            error!("{}", error_msg);
            return Err(anyhow::anyhow!(error_msg));
        }

        missing_commits_found = true;

        if geis_consumed > 1 {
            info!(
                "🔪 [RECOVERY-FRAGMENT] Commit #{} has {} TXs → will consume {} GEIs ({} to {})",
                commit_index, total_txs, geis_consumed, commit_start_gei, commit_end_gei - 1
            );
        } else {
            info!(
                "🔄 [RECOVERY] Replaying commit #{} (global_exec_index={}, txs={})",
                commit_index, commit_start_gei, total_txs
            );
        }

        // Send to executor — send_committed_subdag handles fragmentation internally
        // We MUST pass commit_start_gei, and it will skip fragments < next_expected_index.
        let actual_geis = executor_client
            .send_committed_subdag(&subdag, current_epoch, commit_start_gei, subdag.leader_address.clone())
            .await?;

        // Advance expected index to the end of this commit
        next_required_global = std::cmp::max(next_required_global, commit_start_gei + actual_geis);

        // Small delay to prevent overwhelming the socket/executor
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }

    if !missing_commits_found {
        info!("✅ [RECOVERY] No missing commits found in local DB that need replaying.");
    } else {
        info!("✅ [RECOVERY] Replay completed successfully.");
    }
    Ok(())
}

pub async fn perform_fork_detection_check(node: &crate::node::ConsensusNode) -> Result<()> {
    info!(
        "🔍 [FORK DETECTION] Checking state (Epoch: {}, LastCommit: {})",
        node.current_epoch, node.last_global_exec_index
    );
    // Real implementation would query peers.
    Ok(())
}
