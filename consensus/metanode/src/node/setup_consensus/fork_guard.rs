// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::node::ConsensusNode;
use crate::node::executor_client::ExecutorClient;
use std::sync::atomic::AtomicBool;
use std::sync::Arc;

impl ConsensusNode {

    /// Runtime Fork Guard — PERMANENT background block hash verification (Layer 6).
    ///
    /// ═══ FALSE-POSITIVE FIX (2026-09-05) ═══
    /// This used to gate "CONFIRMED FORK" on `local_raw_block_bytes == peer_raw_block_bytes`
    /// (the full `Block.Marshal()` output), in addition to `state_root`. That is too strict:
    /// `Marshal()`'s wire format (`Block.Proto()` -> `BlockHeader.Proto()`, execution/pkg/block/
    /// block.go + block_header.go) includes `CommitIndex` — the Rust-side commit/round counter
    /// that happened to trigger execution of this GEI on THIS node. `BlockHeader.Hash()`
    /// (execution/pkg/block/block_header.go) deliberately excludes `CommitIndex` from the
    /// header fields it hashes, with its own comment explaining `Hash()` is meant to be the
    /// block's true cryptographic identity — i.e. the codebase's own Go side already treats
    /// CommitIndex as node-local bookkeeping, not part of "is this the same block". Two honest
    /// validators can commit the identical GEI/state via different local commit-round numbers
    /// (e.g. one observes an extra empty/skipped round the other doesn't), so their raw bytes
    /// can legitimately differ while `state_root` (and `block_hash`) match exactly.
    ///
    /// This is exactly what happened live: a real `local_devnet` restart hit "CONFIRMED FORK"
    /// on 3 of 4 nodes (cascading — the crash of the first drove a peer_rpc request flood that
    /// then pressured the others), each calling `std::process::exit(1)` with no supervisor to
    /// restart them (that call is only safe under systemd's `Restart=on-failure`, per the
    /// deploy/systemd/ unit — this ad-hoc devnet has none), i.e. a raw-bytes false positive
    /// took down the whole cluster. The 3x/5s re-verify loop didn't catch it because it keeps
    /// re-querying the same first-available peer (see `fetch_blocks_from_peer`'s peer_idx
    /// selection) — 3 answers from one peer are not independent confirmation of anything.
    ///
    /// Fix: compare against `block_hash` (already `BlockHeader.Hash()`'s correct, canonical
    /// per-block identity, sent as its own field) instead of full raw-byte equality. Keep
    /// `state_root` as a redundant/explicit check for clearer diagnostics on mismatch. Raw-byte
    /// inequality is still logged (as a WARNING, not a fork) when hashes otherwise match, purely
    /// as a diagnostic breadcrumb — it should no longer be able to halt the process by itself.
    pub(crate) async fn runtime_fork_guard(
        client: Arc<ExecutorClient>,
        peers: Vec<String>,
        start_block: u64,
        is_terminally_failed: Arc<AtomicBool>,
    ) {
        const CHECK_INTERVAL: u64 = 10;
        let mut next_check_block = start_block + CHECK_INTERVAL;
        let mut consecutive_failures: u32 = 0;
        const MAX_CONSECUTIVE_FAILURES: u32 = 10;

        tracing::info!(
            "🛡️ [LAYER-6] Runtime Fork Guard started — PERMANENT monitoring from block {} (every {} blocks)",
            start_block, CHECK_INTERVAL
        );

        loop {
            loop {
                match client.get_last_block_number().await {
                    Ok((current, _, true, _, _)) if current >= next_check_block => break,
                    _ => {
                        tokio::time::sleep(std::time::Duration::from_secs(2)).await;
                    }
                }
            }

            match crate::network::peer_rpc::fetch_blocks_from_peer(
                &peers, next_check_block, next_check_block,
            ).await {
                Ok(peer_blocks) if !peer_blocks.is_empty() => {
                    match client.get_blocks_range(next_check_block, next_check_block).await {
                        Ok(local_blocks) if !local_blocks.is_empty() => {
                            let local_hash = &local_blocks[0].block_hash;
                            let peer_hash = &peer_blocks[0].block_hash;
                            let local_state_root = &local_blocks[0].state_root;
                            let peer_state_root = &peer_blocks[0].state_root;
                            let local_raw = &local_blocks[0].raw_block_bytes;
                            let peer_raw = &peer_blocks[0].raw_block_bytes;

                            // Raw-byte inequality alone is NOT a fork signal (see the doc
                            // comment above — it legitimately varies with local CommitIndex).
                            // Log it once as a breadcrumb, but never gate on it.
                            if local_raw != peer_raw && local_hash == peer_hash {
                                tracing::debug!(
                                    "ℹ️ [LAYER-6] Block #{} raw bytes differ ({} vs {} bytes) but \
                                     block_hash matches — expected CommitIndex-only divergence, not a fork.",
                                    next_check_block, local_raw.len(), peer_raw.len()
                                );
                            }

                            if local_hash == peer_hash && local_state_root == peer_state_root {
                                if next_check_block % 100 == 0 {
                                    tracing::info!(
                                        "✅ [LAYER-6] Block #{} verified (block_hash match, state_root match)",
                                        next_check_block
                                    );
                                }
                                consecutive_failures = 0;
                            } else {
                                tracing::error!(
                                    "🚨 [LAYER-6] Block #{} MISMATCH DETECTED! \
                                     local_hash=0x{} peer_hash=0x{}, local_root=0x{} peer_root=0x{} \
                                     (raw_bytes: local={} peer={} bytes). \
                                     ENTERING PENDING MODE — will re-verify 3 times before action.",
                                    next_check_block,
                                    hex::encode(local_hash), hex::encode(peer_hash),
                                    hex::encode(local_state_root), hex::encode(peer_state_root),
                                    local_raw.len(), peer_raw.len()
                                );

                                let mut confirmed_mismatch = true;
                                for retry in 1..=3 {
                                    tracing::warn!(
                                        "⏳ [LAYER-6] Re-verify attempt {}/3 for block #{} in 5s...",
                                        retry, next_check_block
                                    );
                                    tokio::time::sleep(std::time::Duration::from_secs(5)).await;

                                    // Rotate which peer goes first each retry (when more than
                                    // one is configured) so the 3 re-verifications are genuinely
                                    // independent corroboration, not 3 repeated asks to whichever
                                    // single peer answered first — `fetch_blocks_from_peer` always
                                    // tries its slice's first entry before falling back, so without
                                    // this a lone misbehaving/overloaded peers[0] could "confirm"
                                    // its own bad answer on every retry.
                                    let mut retry_peers = peers.clone();
                                    let n = retry_peers.len();
                                    if n > 1 {
                                        retry_peers.rotate_left(retry as usize % n);
                                    }

                                    match crate::network::peer_rpc::fetch_blocks_from_peer(
                                        &retry_peers, next_check_block, next_check_block,
                                    ).await {
                                        Ok(retry_peer_blocks) if !retry_peer_blocks.is_empty() => {
                                            match client.get_blocks_range(next_check_block, next_check_block).await {
                                                Ok(retry_local) if !retry_local.is_empty() => {
                                                    if retry_local[0].block_hash == retry_peer_blocks[0].block_hash
                                                        && retry_local[0].state_root == retry_peer_blocks[0].state_root
                                                    {
                                                        tracing::info!(
                                                            "✅ [LAYER-6] Re-verify {}/3: Block #{} NOW MATCHES! \
                                                             Was transient pipeline lag. Resuming.",
                                                            retry, next_check_block
                                                        );
                                                        confirmed_mismatch = false;
                                                        break;
                                                    } else {
                                                        tracing::error!(
                                                            "🚨 [LAYER-6] Re-verify {}/3: Block #{} STILL MISMATCHES!",
                                                            retry, next_check_block
                                                        );
                                                    }
                                                }
                                                _ => {
                                                    tracing::warn!(
                                                        "⚠️ [LAYER-6] Re-verify {}/3: Could not fetch local block #{}",
                                                        retry, next_check_block
                                                    );
                                                }
                                            }
                                        }
                                        _ => {
                                            tracing::warn!(
                                                "⚠️ [LAYER-6] Re-verify {}/3: Could not reach peer for block #{}",
                                                retry, next_check_block
                                            );
                                        }
                                    }
                                }

                                if confirmed_mismatch {
                                    tracing::error!(
                                        "🚨🚨🚨 [LAYER-6] CONFIRMED FORK at block #{}! \
                                         3/3 re-verifications failed. \
                                         Setting is_terminally_failed and halting process.",
                                        next_check_block
                                    );
                                    is_terminally_failed.store(true, std::sync::atomic::Ordering::SeqCst);
                                    tracing::error!(
                                        "🛑 [LAYER-6] Calling std::process::exit(1) to halt node. \
                                         FFI restart loop will trigger STARTUP-SYNC resync."
                                    );
                                    std::process::exit(1);
                                } else {
                                    consecutive_failures = 0;
                                }
                            }
                        }
                        _ => {
                            consecutive_failures += 1;
                        }
                    }
                }
                _ => {
                    consecutive_failures += 1;
                }
            }

            if consecutive_failures >= MAX_CONSECUTIVE_FAILURES {
                tracing::warn!(
                    "⚠️ [LAYER-6] {} consecutive peer failures. Backing off to 60s interval.",
                    consecutive_failures
                );
                tokio::time::sleep(std::time::Duration::from_secs(60)).await;
                consecutive_failures = 0;
            }

            next_check_block += CHECK_INTERVAL;
        }
    }
}
