// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::node::executor_client::ExecutorClient;
use anyhow::Result;
use consensus_core::CommitAPI;
use std::sync::Arc;
use tracing::{error, info, warn};

pub async fn perform_block_recovery_check(
    executor_client: &Arc<ExecutorClient>,
    go_last_block: u64,
    go_last_commit_index: u32,
    epoch_base_exec_index: u64,
    current_epoch: u64,
    recovery_store: &Arc<dyn consensus_core::storage::Store>,
    context: &Arc<consensus_core::Context>,
    node_id: u32,
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
    // ROOT-CAUSE FIX (G11, found live on a 4-node cluster): the GEI of a commit used to be derived by ARITHMETIC over the
    // local commit store: `epoch_base + commit_index + cumulative_fragment_offset`, starting from commit 1. That is only
    // right if the store holds EVERY commit index from 1 up (each empty commit decrements the offset). A store that is
    // sparse (commits learned from peers during catch-up, an earlier wipe, ...) inflates every later GEI, so commits Go
    // had ALREADY executed were replayed as brand-new blocks (the same transactions became a second block and the
    // node's chain diverged from its peers).
    //
    // Go is the authority on what it executed. So: replay only commits ABOVE the last commit Go reports, number their
    // GEIs by counting forward from Go's own last GEI (exactly what the live path does: one GEI per non-empty commit,
    // N for a fragmented one), and refuse to guess when the local store has a hole right after Go's position.
    let first_commit = go_last_commit_index.saturating_add(1);
    let range = consensus_core::CommitRange::new(first_commit..=u32::MAX);
    let commits = recovery_store.scan_commits(range)?;

    if commits.is_empty() {
        info!("✅ [RECOVERY] No missing commits found in local DB.");
        return Ok(());
    }

    let mut next_required_global = go_last_block + 1;
    let mut expected_commit_index = first_commit;
    let mut missing_commits_found = false;

    info!(
        "🔍 [RECOVERY] Scanning {} commit(s) above Go's last commit {} (next GEI {})...",
        commits.len(),
        go_last_commit_index,
        next_required_global
    );

    for commit in commits {
        let commit_index = commit.index();
        if commit_index == 0 {
            continue; // Genesis/Epoch start is handled locally
        }

        // Halt rather than guess: a hole in the local commit sequence could hide a commit with transactions, and
        // numbering GEIs across it would silently shift every later one.
        if commit_index != expected_commit_index {
            warn!(
                "⚠️ [RECOVERY] Local commit store has a hole: expected commit {}, found {}. Not replaying from local \
                 data; deferring to network catch-up (certified commits).",
                expected_commit_index, commit_index
            );
            return Err(anyhow::anyhow!(
                "Local commit store not contiguous after Go's position (expected {}, found {}). Deferring to network sync.",
                expected_commit_index, commit_index
            ));
        }
        expected_commit_index += 1;

        let commit_start_gei = next_required_global;

        // Reconstruct CommittedSubDag
        // Note: reputation_scores are not critical for execution replay, passing empty
        let subdag = match consensus_core::try_load_committed_subdag_from_store(
            recovery_store.as_ref(),
            commit,
            vec![],
        ) {
            Ok(s) => s,
            Err(e) => {
                warn!("⚠️ [RECOVERY] Critical failure loading commit {}: {}. Recovery cannot proceed sequentially.", commit_index, e);
                return Err(anyhow::anyhow!("Missing block data for commit {}. Deferring to network sync.", commit_index));
            }
        };

        // DISK-REPLAY TRUST GAP FIX (2026-09-17): local RocksDB data just loaded above was only
        // self-checked (digest computed from the very same bytes just read -- corrupted-but-
        // internally-consistent data would still "match"), unlike a block received fresh over
        // the network, which always goes through a real signature check first. Re-verify every
        // block's signature here, in the one place this codebase replays old commits straight
        // from local disk, before trusting any of it enough to even count GEIs from it. On
        // failure: halt rather than guess (same principle already applied elsewhere in this
        // codebase for suspected local-data corruption) -- do NOT attempt to replay this commit
        // locally; bail so the caller falls through to the network catch-up path instead, which
        // re-verifies signatures unconditionally via `block_verifier.rs` and has no such gap.
        if let Err(e) = consensus_core::verify_subdag_block_signatures(&subdag, context) {
            error!(
                "🚨 [RECOVERY] Local disk data for commit {} failed signature verification: {}. \
                 Refusing to trust it for replay -- deferring to network catch-up instead.",
                commit_index, e
            );
            return Err(anyhow::anyhow!(
                "Untrusted local block data for commit {}. Deferring to network sync.",
                commit_index
            ));
        }

        // FORK FIX (2026-09-17): warm the local TxPayloadCache via peer recovery BEFORE the
        // GEI count below. Right after a fresh restart this cache starts empty, so a cold count
        // can silently disagree with the count `send_committed_subdag` computes moments later
        // once peer-recovery (called again internally there) has filled it in.
        executor_client.ensure_tx_payloads_cached(&subdag).await;

        // FORK FIX (2026-09-17, part 2): use STRICT mode (fail_on_missing_payload=true) here,
        // not the `false` "best-effort" mode this used to call. `false`'s fallback path can't
        // tell a genuinely-missing tx apart from the special all-zero placeholder (no bytes to
        // inspect), so it always counts it as 1 real tx -- a GUESS that silently disagrees with
        // the real count `send_committed_subdag` computes below once/if the payload actually
        // becomes available. That guess used to permanently desync `cumulative_fragment_offset`
        // from this commit onward: confirmed LIVE TWICE as a real divergent-history fork after a
        // node restarted mid chaos-test (global_exec_index N ended up holding completely
        // different commit content -- 0 tx vs 1 tx, ~2x different commitIndex -- than the rest
        // of the cluster), the second time even with the peer-cache pre-warm above in place
        // (the digest had already aged out of every OTHER node's RAM TxPayloadCache too, by the
        // time a late-restarting node needed it to replay old history -- the exact "evicted from
        // peer RAM, only recoverable via peer's on-disk executable_blocks/{gei}.bin" scenario
        // `try_recover_via_peer_executable_block` exists for, which this recovery-scan counting
        // pass never invokes). Rather than keep guessing, follow this project's established
        // halt-rather-than-guess rule: if the count still can't be determined with certainty
        // after the pre-warm attempt, bail out (existing Err handling below already returns
        // "Deferring to network sync", which the outer restart loop retries with backoff --
        // giving time for the payload to become fetchable, or for normal P2P block-sync to take
        // over instead of this from-scratch GEI recomputation).
        let (geis_consumed, _, total_txs) = match crate::consensus::commit_processor::executor::compute_commit_gei_and_valid_txs(
            &subdag, true
        ) {
            Ok(result) => result,
            Err(e) => {
                warn!("⚠️ [RECOVERY] Missing payload computing GEI for commit {}: {}. Deferring to network sync so this can be retried once recoverable.", commit_index, e);
                return Err(anyhow::anyhow!("Missing block data for commit {}. Deferring to network sync.", commit_index));
            }
        };

        // Empty commits never reach Go and consume no GEI (same rule as the live path).
        if geis_consumed == 0 {
            tracing::trace!("⏭️ [RECOVERY-SKIP] Skipping empty commit #{}", commit_index);
            continue;
        }

        let commit_end_gei = commit_start_gei + geis_consumed;

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

        // The next commit starts where this one actually ended: `actual_geis` is the authoritative count of GEIs this
        // send delivered (it can legitimately differ from the speculative `geis_consumed` above if a payload arrived
        // via gossip in between).
        next_required_global = commit_start_gei + actual_geis;

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
