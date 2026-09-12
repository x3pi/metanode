// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::node::ConsensusNode;
use crate::node::executor_client::ExecutorClient;
use std::sync::atomic::AtomicBool;
use std::sync::Arc;

/// Result of tallying every responding peer's answer for one block (see
/// `tally_peer_answers` and `note/startup_sync_commit_index_import_fork_design_2026-09.md`
/// mục 6.4). Deliberately has NO "single peer answered" case that's treated as
/// authoritative on its own -- with only 1 responding peer, `agree_count ==
/// total_responding == 1` still satisfies "strict majority" (1 > 1/2), which is
/// correct: a lone reachable peer's answer is the best information available, same as
/// the original design already assumed when only 1 peer is configured.
#[derive(Debug)]
enum PeerQuorumOutcome {
    /// No peer answered successfully at all.
    NoPeerData,
    /// The peers that answered do NOT have a strict majority behind any single
    /// (block_hash, state_root) pair -- e.g. 3 peers, 3 different answers, or a tie.
    /// `answers` is (hex-encoded block_hash, count) per distinct answer, for logging.
    Split { answers: Vec<(String, usize)> },
    /// A strict majority (> half) of responding peers agree on this exact answer.
    Majority {
        hash: Vec<u8>,
        state_root: Vec<u8>,
        agree_count: usize,
        total_responding: usize,
    },
}

/// Tally every peer's individual answer for one block into a `PeerQuorumOutcome`.
/// See that type's doc comment and the design doc (mục 6.4) for why this replaces
/// the old "ask one peer, rotate on retry" comparison.
fn tally_peer_answers(
    peer_results: &[(String, anyhow::Result<crate::node::executor_client::proto::BlockData>)],
) -> PeerQuorumOutcome {
    use std::collections::HashMap;

    // Group responding peers by (block_hash, state_root).
    let mut groups: HashMap<(Vec<u8>, Vec<u8>), usize> = HashMap::new();
    for (_peer_addr, result) in peer_results {
        if let Ok(block) = result {
            *groups
                .entry((block.block_hash.clone(), block.state_root.clone()))
                .or_insert(0) += 1;
        }
    }

    let total_responding: usize = groups.values().sum();
    if total_responding == 0 {
        return PeerQuorumOutcome::NoPeerData;
    }

    // Find the largest group. HashMap iteration order is unspecified, but ties are
    // exactly the case we want to report as Split anyway, so which tied group "wins"
    // here doesn't matter -- the strict-majority check below is what actually decides.
    let ((majority_hash, majority_root), &agree_count) = groups
        .iter()
        .max_by_key(|(_, &count)| count)
        .expect("groups is non-empty since total_responding > 0");

    if agree_count * 2 > total_responding {
        PeerQuorumOutcome::Majority {
            hash: majority_hash.clone(),
            state_root: majority_root.clone(),
            agree_count,
            total_responding,
        }
    } else {
        let answers = groups
            .into_iter()
            .map(|((hash, _root), count)| (hex::encode(&hash), count))
            .collect();
        PeerQuorumOutcome::Split { answers }
    }
}

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
    ///
    /// ═══ SECOND ROOT CAUSE FOUND + FIXED (2026-09-05, same day) ═══
    /// `block_hash` itself could STILL legitimately differ between two honest nodes for "the
    /// same" queried block number, live-reproduced twice on a node recovering from a large
    /// internal commit replay (`node/recovery.rs::perform_block_recovery_check`). A temporary
    /// field-level diagnostic (since removed) showed AccountStatesRoot always matched -- the
    /// actually-executed state was never wrong -- but LastBlockHash/TimeStamp/GlobalExecIndex/
    /// CommitIndex all differed, with GlobalExecIndex consistently off by the same amount
    /// (~73) and CommitIndex by another consistent amount (~370). Root cause:
    /// `perform_block_recovery_check`'s from-storage GEI reconstruction had its own inlined
    /// copy of `executor.rs::dispatch_commit()`'s "is this commit empty" decision, missing the
    /// `commit_index > 1` exemption (an epoch's first commit always consumes exactly 1 GEI even
    /// when empty, live) -- so every epoch whose first commit happened to be empty (common on a
    /// quiet devnet) made replay under-count GEI by 1, accumulating over many epochs into a
    /// real, stable divergence. Fixed at the source in `commit_processor::executor::
    /// commit_is_empty_for_gei` (now the single shared decision both paths call) -- see that
    /// function's doc comment for the full writeup. This fix only prevents the drift from being
    /// introduced on FUTURE replays; a node whose on-disk history was already built by a past
    /// buggy replay stays drifted until it re-syncs from a clean peer (STARTUP-SYNC block-copy),
    /// not by replaying its own already-wrong local history again.
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

            // ═══════════════════════════════════════════════════════════════════
            // QUORUM COMPARISON (2026-09-12, note/startup_sync_commit_index_import_
            // fork_design_2026-09.md mục 6.4): query EVERY configured peer
            // independently for this same block, instead of asking one peer at a
            // time (rotating on retry). Against a genuine PERSISTENT fork (not a
            // single flaky peer), single-peer rotation can land on either side of
            // the split on different attempts, oscillating between "MISMATCH" and
            // "NOW MATCHES" forever without ever reaching a firm conclusion —
            // observed live for 400+ blocks with no resolution. Sui/Aptos's real
            // designs both require quorum/majority agreement before trusting a
            // value; this applies the same principle here.
            // ═══════════════════════════════════════════════════════════════════
            let local_result = client.get_blocks_range(next_check_block, next_check_block).await;
            let peer_results = crate::network::peer_rpc::query_block_from_all_peers(
                &peers, next_check_block,
            ).await;

            match local_result {
                Ok(local_blocks) if !local_blocks.is_empty() => {
                    let local_block = &local_blocks[0];
                    match tally_peer_answers(&peer_results) {
                        PeerQuorumOutcome::NoPeerData => {
                            tracing::warn!(
                                "⚠️ [LAYER-6] Block #{}: no peer answered (queried {} peer(s)). Skipping this check.",
                                next_check_block, peers.len()
                            );
                            consecutive_failures += 1;
                        }
                        PeerQuorumOutcome::Split { answers } => {
                            // Peers disagree WITH EACH OTHER — no single extra query from
                            // this node can resolve that, and it is NOT safe to assume this
                            // node is the one at fault. Loud, distinct alert; no abort.
                            tracing::error!(
                                "🚨🚨 [LAYER-6] Block #{}: PEER QUORUM ITSELF IS SPLIT — {} distinct \
                                 answers across {} responding peer(s), no majority. This node's own \
                                 answer (hash=0x{}) may or may not be correct — cannot determine from \
                                 here. Requires operator investigation; NOT auto-halting since the \
                                 correct side is unknown. Detail: {:?}",
                                next_check_block, answers.len(), peer_results.len(),
                                hex::encode(&local_block.block_hash), answers
                            );
                            consecutive_failures = 0;
                        }
                        PeerQuorumOutcome::Majority { hash: majority_hash, state_root: majority_root, agree_count, total_responding } => {
                            if local_block.block_hash == majority_hash && local_block.state_root == majority_root {
                                if next_check_block % 100 == 0 {
                                    tracing::info!(
                                        "✅ [LAYER-6] Block #{} verified ({}/{} responding peers agree, matches local)",
                                        next_check_block, agree_count, total_responding
                                    );
                                }
                                consecutive_failures = 0;
                            } else {
                                tracing::error!(
                                    "🚨 [LAYER-6] Block #{} MISMATCH DETECTED! local_hash=0x{} local_root=0x{} \
                                     vs peer MAJORITY ({}/{} responding peers) hash=0x{} root=0x{}. \
                                     ENTERING PENDING MODE — will re-verify 3 times before action.",
                                    next_check_block,
                                    hex::encode(&local_block.block_hash), hex::encode(&local_block.state_root),
                                    agree_count, total_responding,
                                    hex::encode(majority_hash), hex::encode(majority_root)
                                );

                                let mut confirmed_mismatch = true;
                                for retry in 1..=3 {
                                    tracing::warn!(
                                        "⏳ [LAYER-6] Re-verify attempt {}/3 for block #{} in 5s...",
                                        retry, next_check_block
                                    );
                                    tokio::time::sleep(std::time::Duration::from_secs(5)).await;

                                    let retry_local = client.get_blocks_range(next_check_block, next_check_block).await;
                                    let retry_peer_results = crate::network::peer_rpc::query_block_from_all_peers(
                                        &peers, next_check_block,
                                    ).await;

                                    match (retry_local, tally_peer_answers(&retry_peer_results)) {
                                        (Ok(retry_local_blocks), PeerQuorumOutcome::Majority { hash, state_root, agree_count, total_responding })
                                            if !retry_local_blocks.is_empty() =>
                                        {
                                            if retry_local_blocks[0].block_hash == hash && retry_local_blocks[0].state_root == state_root {
                                                tracing::info!(
                                                    "✅ [LAYER-6] Re-verify {}/3: Block #{} NOW MATCHES peer majority \
                                                     ({}/{})! Was transient pipeline lag. Resuming.",
                                                    retry, next_check_block, agree_count, total_responding
                                                );
                                                confirmed_mismatch = false;
                                                break;
                                            } else {
                                                tracing::error!(
                                                    "🚨 [LAYER-6] Re-verify {}/3: Block #{} STILL MISMATCHES peer majority ({}/{})!",
                                                    retry, next_check_block, agree_count, total_responding
                                                );
                                            }
                                        }
                                        (Ok(_), PeerQuorumOutcome::Split { answers }) => {
                                            tracing::error!(
                                                "🚨 [LAYER-6] Re-verify {}/3: peer quorum split ({} distinct answers) — \
                                                 cannot confirm OR clear the mismatch this attempt.",
                                                retry, answers.len()
                                            );
                                        }
                                        _ => {
                                            tracing::warn!(
                                                "⚠️ [LAYER-6] Re-verify {}/3: could not get a comparable answer (local fetch or peer quorum failed) for block #{}",
                                                retry, next_check_block
                                            );
                                        }
                                    }
                                }

                                if confirmed_mismatch {
                                    tracing::error!(
                                        "🚨🚨🚨 [LAYER-6] CONFIRMED FORK at block #{}! \
                                         3/3 re-verifications failed against peer MAJORITY (not just 1 peer). \
                                         Setting is_terminally_failed and halting process.",
                                        next_check_block
                                    );
                                    is_terminally_failed.store(true, std::sync::atomic::Ordering::SeqCst);
                                    // FOUND LIVE (2026-09-05): std::process::exit() calls libc's
                                    // exit() -- which, unlike _exit()/abort(), runs every
                                    // atexit()-registered handler and every linked C++ library's
                                    // static-object destructor (via __cxa_atexit) before actually
                                    // terminating. This binary statically links several nontrivial
                                    // C/C++ libraries (Xapian, the custom MVM/EVM linker, NOMT's
                                    // FFI) -- reproduced live, twice, on two different builds (one
                                    // with an unrelated unrelated change, one on a clean revert of
                                    // it, ruling out that change as the cause): this exact log line
                                    // printed, "Calling std::process::exit(1)" logged immediately
                                    // after, and the OS process (verified by exact PID + `ps
                                    // -o lstart`, not a race) kept running for 46+ seconds
                                    // afterward -- i.e. it hung *inside* exit(), most likely stuck
                                    // in one of those handlers, never actually terminating. This
                                    // silently defeats the entire safety mechanism: a node that
                                    // detects a confirmed fork keeps running (and could keep
                                    // participating in consensus with state already judged
                                    // divergent) instead of halting.
                                    //
                                    // Fixed by calling abort() instead: it raises SIGABRT directly,
                                    // skipping atexit()/__cxa_atexit entirely -- verified in
                                    // isolation (a minimal thread::spawn + tokio::spawn + exit(1)
                                    // repro terminated correctly in under 1s, so the hang is
                                    // specific to this binary's real linked libraries, not to the
                                    // exit()-from-a-tokio-task pattern itself). A clean shutdown
                                    // doesn't matter here anyway -- state is already judged
                                    // divergent, so running MORE code (even cleanup code) before
                                    // dying is undesirable, not just unnecessary. Under systemd,
                                    // `Restart=on-failure` restarts on an abnormal signal
                                    // termination exactly the same as on a nonzero exit code, so
                                    // this doesn't change the "FFI restart loop" recovery story at
                                    // all -- only makes the halt itself actually happen.
                                    tracing::error!(
                                        "🛑 [LAYER-6] Calling std::process::abort() to halt node \
                                         (skips atexit handlers that can hang -- see comment above). \
                                         FFI restart loop will trigger STARTUP-SYNC resync."
                                    );
                                    std::process::abort();
                                } else {
                                    consecutive_failures = 0;
                                }
                            }
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
