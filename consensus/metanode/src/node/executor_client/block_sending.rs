// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Block sending methods for ExecutorClient.
//!
//! These methods handle sending committed blocks to the Go executor:
//! - `send_committed_subdag` — buffered, sequential sending
//! - `flush_buffer` — flush buffered blocks in order
//! - `send_committed_subdag_direct` — bypass buffer (for SyncOnly)
//! - `send_block_data` — low-level socket send
//! - `convert_to_protobuf` — CommittedSubDag → protobuf bytes

use anyhow::Result;
use consensus_core::{BlockAPI, CommittedSubDag, SystemTransaction};
use prost::Message;

use tracing::{debug, error, info, trace, warn};

use super::persistence::persist_last_sent_index;
use super::proto::{ExecutableBlock, TransactionExe};
use super::ExecutorClient;
use super::{GO_VERIFICATION_INTERVAL, MAX_BUFFER_SIZE};

/// Wall-clock timestamp in nanoseconds since the Unix epoch. Used only for
/// [FFI-TRACE] diagnostics: Rust and Go share the same OS clock (CGo links
/// them into one process), so these timestamps can be directly correlated
/// with the `time.Now().UnixNano()` timestamps Go logs on its side of the
/// same FFI call, keyed by `gei`, to attribute the Rust<->Go round trip.
fn now_ns() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0)
}

/// Gates the [FFI-TRACE] diagnostics (see `now_ns` above) behind
/// `METANODE_FFI_TRACE=true`. They're `warn!`, which — like Go's
/// `logger.Warn` counterpart these are paired with — shows at any configured
/// log level and fires on every block, so they stay opt-in rather than an
/// always-on log-volume cost every production node would pay forever for a
/// diagnostic only needed when actively chasing a perf regression again.
fn ffi_trace_enabled() -> bool {
    static ENABLED: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
    *ENABLED.get_or_init(|| std::env::var("METANODE_FFI_TRACE").as_deref() == Ok("true"))
}

/// Maximum transactions per Go block.
/// When a DAG commit exceeds this threshold, Rust splits it into multiple
/// ExecutableBlock payloads with incrementing global_exec_index values.
/// Go EVM performs best with 5000-10000 TXs per block (balances parallelism
/// vs IntermediateRoot overhead). Splitting at 50000 reduces per-block overhead
/// while keeping GC pressure and EVM contention manageable.
/// FORK-SAFETY: All nodes use the same threshold → deterministic split.
pub const MAX_TXS_PER_GO_BLOCK: usize = 50_000;

impl ExecutorClient {
    /// Send committed sub-DAG to executor, with automatic fragmentation for large commits.
    ///
    /// BLOCK FRAGMENTATION: When a commit contains more than MAX_TXS_PER_GO_BLOCK
    /// transactions, it is split into N smaller ExecutableBlock payloads:
    ///   - Fragment 0: global_exec_index=GEI,   TXs[0..5000]
    ///   - Fragment 1: global_exec_index=GEI+1,  TXs[5000..10000]
    ///   - Fragment 2: global_exec_index=GEI+2,  TXs[10000..12479]
    /// Each fragment is sent as a separate block.
    ///
    /// Returns the number of GEI slots consumed (1 for normal, N for fragmented).
    /// The caller (CommitProcessor) must advance its global_exec_index tracking by this amount.
    ///
    /// CRITICAL FORK-SAFETY: global_exec_index and commit_index ensure deterministic execution order
    /// SEQUENTIAL BUFFERING: Blocks are buffered and sent in order to ensure Go receives them sequentially
    /// LEADER_ADDRESS: Optional 20-byte Ethereum address of leader validator
    /// When provided, Go uses this directly instead of looking up by index
    pub async fn send_committed_subdag(
        &self,
        subdag: &CommittedSubDag,
        epoch: u64,
        global_exec_index: u64,
        leader_address: Vec<u8>,
    ) -> Result<u64> {
        if !self.is_enabled() {
            return Ok(1); // Silently skip if not enabled
        }

        // ═══════════════════════════════════════════════════════════════
        // LAYER-1: Protobuf Strict Boundary — Validate FFI data BEFORE
        // any serialization or network calls. Reject malformed data at
        // the gate to prevent String vs Bytes sorting forks.
        // ═══════════════════════════════════════════════════════════════
        if !leader_address.is_empty() && leader_address.len() != 20 {
            anyhow::bail!(
                "🛡️ [LAYER-1] REJECT: leader_address must be exactly 20 bytes, got {} bytes (commit={}). \
                 This indicates a String/Bytes encoding mismatch in committee resolution.",
                leader_address.len(), subdag.commit_ref.index
            );
        }

        // Count total transactions BEFORE conversion (to detect if transactions are lost)
        let total_tx_before: usize = subdag
            .blocks
            .iter()
            .map(|b| {
                let tx_len = b.transactions().len();
                if tx_len > 0 {
                    tx_len
                } else {
                    b.tx_digests().len()
                }
            })
            .sum();

        // T2-6: Unified batch_id for cross-process tracing (matches Go format)

        // Determine expected fragments early so we can return it if we skip
        let expected_fragments = if total_tx_before > MAX_TXS_PER_GO_BLOCK {
            total_tx_before.div_ceil(MAX_TXS_PER_GO_BLOCK) as u64
        } else {
            1
        };

        // REPLAY PROTECTION: Discard blocks that are already processed
        // This is critical when Consensus replays old commits on restart
        // PHASE-B: When global_exec_index=0, Rust doesn't track GEI — skip this guard.
        // Go handles dedup internally via is_authoritative_gei + GEIAuthority.
        if global_exec_index > 0 {
            let next_expected = self.next_expected_index.lock().await;
            if global_exec_index + expected_fragments <= *next_expected {
                // Only log periodically or for non-empty blocks to avoid noise during replay
                if total_tx_before > 0 || global_exec_index.is_multiple_of(1000) {
                    info!(
                        "♻️ [REPLAY] Discarding already processed block: global={}..{}, expected={}",
                        global_exec_index, global_exec_index + expected_fragments - 1, *next_expected
                    );
                }
                return Ok(expected_fragments);
            }
        }

        // DUAL-STREAM DEDUP: Prevent duplicate sends from Consensus and Sync streams
        // PHASE-B: Skip when global_exec_index=0 — all commits would hash to 0.
        if global_exec_index > 0 {
            let sent = self.sent_indices.lock().await;
            if sent.contains(&global_exec_index) {
                info!(
                    "🔄 [DEDUP] Skipping already-sent block from dual-stream: global_exec_index={}",
                    global_exec_index
                );
                return Ok(expected_fragments); // Already sent, skip but return correct fragments
            }
            // Don't insert yet - insert only after successful send
        }

        // ═══════════════════════════════════════════════════════════════
        // PRE-PROCESS TRANSACTIONS: Filter (SystemTx, Invalid) and Dedup
        // ═══════════════════════════════════════════════════════════════
        let mut all_proto_txs = Vec::new();
        let mut all_system_txs = Vec::new();
        let mut total_after_dedup = 0;

        if total_tx_before > 0 {
            self.ensure_tx_payloads_cached(subdag).await;
            // PEER-BLOCK RECOVERY (2026-09-11): if ordinary peer-cache recovery above still
            // left digests missing, try adopting a peer's already-built whole block for this
            // exact commit BEFORE falling through to build_sorted_transactions's bail -- see
            // that method's doc comment for the full rationale (this is the real fix for a
            // real fork the quorum-certified-skip mechanism's first version caused live).
            // Only applicable to the common, non-fragmented case (global_exec_index maps 1:1
            // to this commit) -- fragmented commits (rare, only very large commits) fall
            // through to the ordinary bail/retry path unchanged, same as before this existed.
            if !Self::all_tx_payloads_cached(subdag)
                && total_tx_before <= MAX_TXS_PER_GO_BLOCK
                && self
                    .try_recover_via_peer_executable_block(
                        subdag,
                        global_exec_index,
                        epoch,
                        subdag.commit_ref.index,
                    )
                    .await
            {
                if global_exec_index > 0 {
                    self.sent_indices.lock().await.insert(global_exec_index);
                }
                return Ok(expected_fragments);
            }
            let (txs, sys_txs) = self.build_sorted_transactions(subdag)?;
            all_proto_txs = txs;
            all_system_txs = sys_txs;
            total_after_dedup = all_proto_txs.len();
        }

        // ═══════════════════════════════════════════════════════════════
        // BLOCK FRAGMENTATION: If commit has more TXs than MAX_TXS_PER_GO_BLOCK,
        // split into N smaller ExecutableBlock payloads.
        // CRITICAL FORK-SAFETY: All nodes use the same threshold and same
        // transaction order → deterministic split → identical GEI mapping.
        // ═══════════════════════════════════════════════════════════════
        if total_tx_before > MAX_TXS_PER_GO_BLOCK {
            let num_fragments = total_tx_before.div_ceil(MAX_TXS_PER_GO_BLOCK);
            info!("🔪 [FRAGMENT] Splitting large commit: {} TXs → {} fragments of ≤{} TXs each (global_exec_index={}, commit_index={}, epoch={})",
                total_tx_before, num_fragments, MAX_TXS_PER_GO_BLOCK, global_exec_index, subdag.commit_ref.index, epoch);

            // CRITICAL FORK-SAFETY v5: Recalculate fragments using total_tx_before
            // (from the consensus deterministic subdag) instead of total_after_dedup.
            // If we use total_after_dedup, a restarted node (empty tx_recycler) will
            // fragment differently than a continuous node (populated tx_recycler),
            // causing GEI divergence!
            let actual_fragments = total_tx_before.div_ceil(MAX_TXS_PER_GO_BLOCK);

            for frag_idx in 0..actual_fragments {
                let start = std::cmp::min(frag_idx * MAX_TXS_PER_GO_BLOCK, total_after_dedup);
                let end = std::cmp::min(start + MAX_TXS_PER_GO_BLOCK, total_after_dedup);
                let fragment_txs: Vec<TransactionExe> = all_proto_txs[start..end].to_vec();
                let fragment_gei = global_exec_index + frag_idx as u64;

                let next_expected = { *self.next_expected_index.lock().await };
                if fragment_gei < next_expected {
                    trace!("⏭️  [REPLAY PROTECTION] Fragment GEI={} is already processed (expected={}), skipping entirely.", fragment_gei, next_expected);
                    continue;
                }

                if self.send_buffer.lock().await.contains_key(&fragment_gei) {
                    trace!("⏭️  [SEQUENTIAL-BUFFER] Fragment GEI={} is already in buffer, skipping entirely to preserve its valid block_number", fragment_gei);
                    continue;
                }

                let block_number = {
                    let mut next_bn = self.next_block_number.lock().await;
                    let mut last_ep = self.last_processed_epoch.lock().await;

                        let is_epoch_boundary = epoch > *last_ep;
                        if is_epoch_boundary {
                            info!("🔄 [EPOCH BOUNDARY FRAGMENT] Sending Epoch block to Go! Epoch {} -> {}, SystemTxs: {}, UserTxs: {}, GEI: {}, Block: {}", 
                                *last_ep, epoch, all_system_txs.len(), all_proto_txs.len(), fragment_gei, *next_bn);
                            *last_ep = epoch;
                        }

                        // We ALWAYS increment block number for fragments to ensure block numbers
                        // stay perfectly synchronized between continuous nodes (whose TxRecycler
                        // may deduplicate many TXs making the fragment empty) and restarted nodes
                        // (whose empty TxRecycler will allow TXs through).
                        let bn = *next_bn;
                        *next_bn += 1;
                        info!("📊 [BLOCK-NUM-ASSIGN] Fragment: block_number={} for GEI={}, epoch={}, frag={}/{}, txs_user={}, txs_sys={}",
                            bn, fragment_gei, epoch, frag_idx+1, actual_fragments, all_proto_txs.len(), all_system_txs.len());
                        bn
                };

                let subdag_commit_idx = subdag.commit_ref.index;
                let is_last_frag = frag_idx == actual_fragments - 1;

                // CRITICAL FORK-SAFETY v6: Do not update Go's last_handled_commit_index until
                // the FINAL fragment of a commit is executed. If a node crashes mid-commit,
                // Go will stay at C-1, forcing the node to safely replay the entire commit C
                // on restart, using executor_client's next_expected_index to skip already-executed fragments.
                let commit_index_for_go = if is_last_frag {
                    subdag_commit_idx
                } else {
                    subdag_commit_idx.saturating_sub(1)
                };

                let epoch_data = ExecutableBlock {
                    transactions: fragment_txs.clone(),
                    global_exec_index: fragment_gei,
                    commit_index: commit_index_for_go,
                    epoch,
                    commit_timestamp_ms: subdag.timestamp_ms,
                    leader_author_index: subdag.leader.author.value() as u32,
                    leader_address: leader_address.clone(),
                    block_number,
                    commit_hash: subdag.commit_ref.digest.into_inner().to_vec(),
                    // GO-AUTHORITATIVE GEI: Disabled because Rust now passes the correct GEI explicitly
                    is_authoritative_gei: false,
                    system_transactions: if is_last_frag {
                        all_system_txs.clone()
                    } else {
                        Vec::new()
                    },
                    // Layer 1: Protobuf strict boundary fields
                    authority_key: vec![],
                    commit_digest: vec![],
                    rust_dispatch_timestamp_ms: std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_millis() as u64,
                    rust_ffi_delivery_timestamp_ms: std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_millis() as u64,
                };

                for tx_exe in &epoch_data.transactions {
                    let tx_hash = crate::types::tx_hash::calculate_transaction_hash_single(&tx_exe.digest);
                    crate::ffi::update_go_tx_trace(&tx_hash, "RUST_CONSENSUS_COMMITTED", &format!("Transaction packaged in fragmented ExecutableBlock sent to Go. GEI={}, Block={}", fragment_gei, block_number));
                }

                let tx_count = epoch_data.transactions.len();
                let mut buf = Vec::new();
                epoch_data.encode(&mut buf)?;

                info!(
                    "🔪 [FRAGMENT {}/{}] GEI={}, TXs={}, size={} bytes",
                    frag_idx + 1,
                    actual_fragments,
                    fragment_gei,
                    tx_count,
                    buf.len()
                );

                self.buffer_and_flush(fragment_gei, buf, epoch, subdag.commit_ref.index, tx_count)
                    .await?;
            }

            info!(
                "✅ [FRAGMENT] Completed fragmentation: {} TXs → {} blocks (GEI {}→{})",
                total_after_dedup,
                actual_fragments,
                global_exec_index,
                global_exec_index + actual_fragments as u64 - 1
            );

            return Ok(actual_fragments as u64);
        }

        // ═══════════════════════════════════════════════════════════════
        // NORMAL PATH: Commit fits within MAX_TXS_PER_GO_BLOCK
        // ═══════════════════════════════════════════════════════════════

        let _has_system_tx = subdag.blocks.iter().any(|b| {
            b.transactions()
                .iter()
                .any(|tx| SystemTransaction::from_bytes(tx.data()).is_ok())
        });

        let block_number = {
            let mut next_bn = self.next_block_number.lock().await;
            let mut last_ep = self.last_processed_epoch.lock().await;

            let is_epoch_boundary = epoch > *last_ep;
            if is_epoch_boundary {
                info!("🔄 [EPOCH BOUNDARY] Preparing to send Epoch Boundary Block to Go: Epoch {} -> {}, SystemTxs: {}, UserTxs: {}, GEI: {}, Block: {}", 
                    *last_ep, epoch, all_system_txs.len(), all_proto_txs.len(), global_exec_index, *next_bn);
                *last_ep = epoch;
            }

            // CRITICAL FORK-SAFETY v7: Always increment block number for every commit,
            // even if empty. This ensures Go receives a sequential block_number and can
            // explicitly create an empty block to prevent "Ghost block" sequence gaps.
            let bn = *next_bn;
            *next_bn += 1;
            info!("📊 [BLOCK-NUM-ASSIGN] Assigned block_number={} for GEI={}, epoch={}, commit_idx={}, txs_user={}, txs_sys={}",
                bn, global_exec_index, epoch, subdag.commit_ref.index, all_proto_txs.len(), all_system_txs.len());
            bn
        };

        // Construct ExecutableBlock directly using pre-processed transactions
        let epoch_data = ExecutableBlock {
            transactions: all_proto_txs,
            global_exec_index,
            commit_index: subdag.commit_ref.index,
            epoch,
            commit_timestamp_ms: subdag.timestamp_ms,
            leader_author_index: subdag.leader.author.value() as u32,
            leader_address,
            block_number,
            commit_hash: subdag.commit_ref.digest.into_inner().to_vec(),
            // GO-AUTHORITATIVE GEI: Disabled. Rust is the absolute authority for GEI.
            is_authoritative_gei: false,
            system_transactions: all_system_txs,
            // Layer 1: Protobuf strict boundary fields
            authority_key: vec![],
            commit_digest: vec![],
            rust_dispatch_timestamp_ms: std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_millis() as u64,
            rust_ffi_delivery_timestamp_ms: std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_millis() as u64,
        };

        for tx_exe in &epoch_data.transactions {
            let tx_hash = crate::types::tx_hash::calculate_transaction_hash_single(&tx_exe.digest);
            crate::ffi::update_go_tx_trace(&tx_hash, "RUST_CONSENSUS_COMMITTED", &format!("Transaction packaged in ExecutableBlock sent to Go. GEI={}, Block={}", global_exec_index, block_number));
        }
        let mut epoch_data_bytes = Vec::new();
        epoch_data.encode(&mut epoch_data_bytes)?;

        self.buffer_and_flush(
            global_exec_index,
            epoch_data_bytes,
            epoch,
            subdag.commit_ref.index,
            total_after_dedup,
        )
        .await?;

        Ok(1) // Normal commit consumes 1 GEI
    }

    /// Buffer an ExecutableBlock and flush.
    /// This is the shared logic extracted from send_committed_subdag to support fragmentation.
    async fn buffer_and_flush(
        &self,
        global_exec_index: u64,
        epoch_data_bytes: Vec<u8>,
        epoch: u64,
        commit_index: u32,
        total_tx: usize,
    ) -> Result<()> {
        // ═══════════════════════════════════════════════════════════════
        // PHASE-B DIRECT SEND: When global_exec_index=0, Rust doesn't
        // track GEI. The GEI-keyed buffer is incompatible (all commits
        // would get key=0, causing overwrites). CommitProcessor already
        // ensures sequential ordering, so bypass buffer and send directly.
        // ═══════════════════════════════════════════════════════════════
        if global_exec_index == 0 {
            // Ensure connection
            if let Err(e) = self.connect().await {
                warn!("⚠️  Executor connection failed: {}", e);
                return Ok(());
            }

            if let Some(c_fn) = crate::ffi::GO_CALLBACKS.get().and_then(|c| c.execute_block) {
                let ffi_start = std::time::Instant::now();
                
                let (success, response) = {
                    let mut out_payload: *mut u8 = std::ptr::null_mut();
                    let mut out_len = 0usize;
                    let success = c_fn(
                        epoch_data_bytes.as_ptr(),
                        epoch_data_bytes.len(),
                        &mut out_payload,
                        &mut out_len,
                    );
                    
                    let response = if !out_payload.is_null() && out_len > 0 {
                        let slice = unsafe { std::slice::from_raw_parts(out_payload, out_len) };
                        let resp = super::proto::ExecuteBlockResponse::decode(slice).ok();
                        if let Some(free_fn) = crate::ffi::GO_CALLBACKS.get().and_then(|c| c.free_go_buffer) {
                            free_fn(out_payload);
                        }
                        resp
                    } else {
                        None
                    };
                    (success, response)
                };

                let ffi_elapsed = ffi_start.elapsed();
                tracing::warn!(
                    "⏱️ [PERF-RUST] FFI direct execute_block for commit_index {} (size={} bytes): {:?}",
                    commit_index,
                    epoch_data_bytes.len(),
                    ffi_elapsed
                );

                if success {
                    self.record_send_success().await;
                    trace!(
                        "✅ [PHASE-B DIRECT] Sent commit_index={} to Go FFI (epoch={}, tx={})",
                        commit_index,
                        epoch,
                        total_tx
                    );
                    if let Some(resp) = response {
                        if !resp.state_root.is_empty() {
                            tracing::info!(
                                "💾 [BLOCK-SEND] Go computed StateRoot for commit_index={}: 0x{}",
                                commit_index,
                                hex::encode(&resp.state_root)
                            );
                        }
                    }
                } else {
                    warn!(
                        "⚠️  [PHASE-B DIRECT] FFI execute_block failed for commit_index={}",
                        commit_index
                    );
                    self.record_send_failure().await;
                }
            } else {
                // Socket fallback
                use tokio::io::AsyncWriteExt;
                let mut conn_guard = self.connection.lock().await;
                if let Some(ref mut stream) = *conn_guard {
                    let mut buf = Vec::new();
                    super::persistence::write_uvarint(&mut buf, epoch_data_bytes.len() as u64)?;
                    buf.extend_from_slice(&epoch_data_bytes);

                    let result = async {
                        stream.write_all(&buf).await?;
                        stream.flush().await?;
                        Ok::<(), anyhow::Error>(())
                    }.await;

                    if let Err(e) = result {
                        warn!("⚠️  [PHASE-B DIRECT] Socket write failed: {}, resetting connection", e);
                        *conn_guard = None;
                        self.record_send_failure().await;
                    } else {
                        self.record_send_success().await;
                        trace!(
                            "✅ [PHASE-B DIRECT] Sent commit_index={} to Go Socket (epoch={}, tx={})",
                            commit_index,
                            epoch,
                            total_tx
                        );
                    }
                } else {
                    warn!("⚠️  [PHASE-B DIRECT] Socket connection is None");
                    self.record_send_failure().await;
                }
            }
            return Ok(());
        }

        // LEGACY PATH: GEI-based buffer (for non-PHASE-B or transition)
        // SEQUENTIAL BUFFERING: Add to buffer and try to send in order
        {
            let mut buffer = self.send_buffer.lock().await;

            // PRODUCTION SAFETY: Buffer size limit to prevent memory exhaustion
            if buffer.len() >= MAX_BUFFER_SIZE {
                error!("🚨 [BUFFER LIMIT] Buffer is full ({} blocks). Rejecting block global_exec_index={}. This indicates severe sync issues.",
                    buffer.len(), global_exec_index);
                return Err(anyhow::anyhow!(
                    "Buffer full: {} blocks (max {})",
                    buffer.len(),
                    MAX_BUFFER_SIZE
                ));
            }
            if buffer.contains_key(&global_exec_index) {
                let (existing_epoch_data, existing_epoch, existing_commit) = buffer
                    .get(&global_exec_index)
                    .map(|(d, e, c)| (d.len(), *e, *c))
                    .unwrap_or((0, 0, 0));
                error!(
                    "🚨 [DUPLICATE GLOBAL_EXEC_INDEX] Duplicate global_exec_index={} detected!",
                    global_exec_index
                );
                error!(
                    "   📊 Existing: epoch={}, commit_index={}, data_size={} bytes",
                    existing_epoch, existing_commit, existing_epoch_data
                );
                error!(
                    "   📊 New:      epoch={}, commit_index={}, data_size={} bytes, total_tx={}",
                    epoch,
                    commit_index,
                    epoch_data_bytes.len(),
                    total_tx
                );

                let is_same_commit = existing_epoch == epoch && existing_commit == commit_index;

                if is_same_commit {
                    warn!("   ✅ Same commit detected (epoch={}, commit_index={}) - skipping duplicate, existing commit in buffer will be sent", epoch, commit_index);
                } else {
                    error!("   🚨 DIFFERENT commits with same global_exec_index! This is a BUG!");
                    error!("   🔍 Root cause analysis:");
                    error!("      - Epochs different ({} vs {}): global_exec_index calculation may be wrong", existing_epoch, epoch);
                    error!("      - Commit indexes different ({} vs {}): same global_exec_index calculated for different commits", existing_commit, commit_index);
                    error!("      - This indicates last_global_exec_index was not updated correctly or calculation is wrong");
                    warn!("   ⚠️  Keeping first-seen commit to ensure deterministic data");
                }
            }
            buffer
                .entry(global_exec_index)
                .or_insert((epoch_data_bytes, epoch, commit_index));
            trace!("[batch_id=E{}C{}G{}] 📦 [SEQUENTIAL-BUFFER] Added block: total_tx={}, buffer_size={}",
                epoch, commit_index, global_exec_index, total_tx, buffer.len());
        }

        // CRITICAL: Flush buffer iteratively after adding commit.
        // Loop ensures we drain all consecutive blocks without recursion (C5 fix).
        loop {
            if let Err(e) = self.flush_buffer().await {
                warn!(
                    "⚠️  [SEQUENTIAL-BUFFER] Failed to flush buffer after adding commit: {}",
                    e
                );
                break;
            }
            // Check if there are more consecutive blocks to flush
            let has_more = {
                let buffer = self.send_buffer.lock().await;
                let next_expected = self.next_expected_index.lock().await;
                buffer.contains_key(&*next_expected)
            };
            if !has_more {
                break;
            }
        }

        Ok(())
    }

    /// Flush buffered blocks in sequential order
    /// This ensures Go executor receives blocks in the correct order even if Rust sends them out-of-order
    /// CRITICAL: This function will send all consecutive commits starting from next_expected_index
    /// OPTIMIZATION: Batches consecutive socket writes and reduces lock contention
    pub async fn flush_buffer(&self) -> Result<()> {
        // Connect if needed
        if let Err(e) = self.connect().await {
            warn!("⚠️  Executor connection failed, cannot flush buffer: {}", e);
            return Ok(()); // Don't fail if executor is unavailable
        }

        // Log buffer status before flushing
        {
            let buffer = self.send_buffer.lock().await;
            let next_expected = self.next_expected_index.lock().await;
            if !buffer.is_empty() {
                let min_buffered = *buffer.keys().next().unwrap_or(&0);
                let max_buffered = *buffer.keys().last().unwrap_or(&0);
                let gap = min_buffered.saturating_sub(*next_expected);
                trace!("📊 [FLUSH BUFFER] Buffer status: size={}, range={}..{}, next_expected={}, gap={}", 
                    buffer.len(), min_buffered, max_buffered, *next_expected, gap);
            }
        }

        // CRITICAL: Do NOT skip blocks - ensure all blocks are sent sequentially
        {
            let buffer = self.send_buffer.lock().await;
            let next_expected = self.next_expected_index.lock().await;
            if !buffer.is_empty() {
                let min_buffered = *buffer.keys().next().unwrap_or(&0);
                let gap = min_buffered.saturating_sub(*next_expected);

                let now_ms = std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_millis() as u64;
                let last_query = self.last_gap_query_ms.load(std::sync::atomic::Ordering::Relaxed);
                let should_sync = gap > 100 || (gap > 0 && now_ms.saturating_sub(last_query) >= 1000);

                if should_sync {
                    let next_expected_val = *next_expected;
                    self.last_gap_query_ms.store(now_ms, std::sync::atomic::Ordering::Relaxed);
                    drop(buffer);
                    drop(next_expected);

                    warn!("⚠️  [SEQUENTIAL-BUFFER] Gap detected: min_buffered={}, next_expected={}, gap={}. Syncing with Go using fast 2-second timeout...", 
                        min_buffered, next_expected_val, gap);

                    // CRITICAL FIX: Use get_last_global_exec_index() instead of get_last_block_number()
                    // get_last_block_number() returns Go block NUMBER (counts only non-empty commits)
                    // but next_expected_index tracks GEI (counts ALL commits including empty ones)
                    // Using block number (e.g. 6) when GEI is ~9000 creates a permanent gap > 100,
                    // causing an infinite sync loop where TX blocks are buffered but never sent.
                    let sync_future = self.get_last_global_exec_index();
                    if let Ok(Ok(go_last_gei)) =
                        tokio::time::timeout(tokio::time::Duration::from_secs(2), sync_future).await
                    {
                        let go_next_expected = go_last_gei + 1;

                        let mut buffer = self.send_buffer.lock().await;
                        let mut next_expected_guard = self.next_expected_index.lock().await;
                        if go_next_expected > *next_expected_guard {
                            info!("📊 [SINGLE-SOURCE-TRUTH] Updating next_expected from {} to {} (from Go last_gei={})",
                                *next_expected_guard, go_next_expected, go_last_gei);
                            *next_expected_guard = go_next_expected;

                            let before_clear = buffer.len();
                            buffer.retain(|&k, _| k >= go_next_expected);
                            let after_clear = buffer.len();
                            if before_clear > after_clear {
                                info!(
                                    "🧹 [SINGLE-SOURCE-TRUTH] Cleared {} old blocks, kept {}",
                                    before_clear - after_clear,
                                    after_clear
                                );
                            }
                        } else {
                            // Go is BEHIND — likely after restore + SyncOnly→Validator transition.
                            // The buffer has consensus blocks far ahead of Go. The empty GEIs
                            // between Go and the buffer are consensus rounds that were never
                            // captured (sync stopped before consensus started).
                            // SAFE: advance next_expected to min_buffered so flush_buffer
                            // can start sending. Go will receive blocks in sequential GEI
                            // order from flush_buffer (BTreeMap + sequential iteration).
                            let min_buf = *buffer.keys().next().unwrap_or(&0);
                            if min_buf > *next_expected_guard {
                                warn!("🚀 [RESTORE-GAP-BRIDGE] Go is behind (gei={}), buffer starts at {}. Advancing next_expected {} → {} to bridge transition gap",
                                    go_last_gei, min_buf, *next_expected_guard, min_buf);
                                *next_expected_guard = min_buf;
                            }
                        }
                    } else {
                        warn!("⚠️  [SEQUENTIAL-BUFFER] get_last_global_exec_index timed out or failed. Continuing buffered sender...");
                    }
                } else if gap > 0 {
                    trace!("⏸️  [SEQUENTIAL-BUFFER] Small gap={} (normal during high throughput), waiting for blocks to arrive", gap);
                }
            }
        }

        // ═══════════════════════════════════════════════════════════════════
        // BATCHED FLUSH: Collect consecutive blocks and write them in a
        // single batched operation. This reduces socket flushes from N to 1
        // and batches lock operations for sent_indices and persistence.
        // ═══════════════════════════════════════════════════════════════════
        const BATCH_WRITE_LIMIT: usize = 500; // Max blocks per batch write
        const PERSIST_INTERVAL: u64 = 100; // Persist to disk every N commits

        // Phase 1: Collect consecutive blocks from buffer
        let mut batch: Vec<(u64, Vec<u8>, u64, u32)> = Vec::new(); // (global_exec_index, data, epoch, commit_index)
        {
            let mut buffer = self.send_buffer.lock().await;
            let next_expected = self.next_expected_index.lock().await;
            let mut idx = *next_expected;
            while batch.len() < BATCH_WRITE_LIMIT {
                if let Some((data, epoch, commit_index)) = buffer.remove(&idx) {
                    batch.push((idx, data, epoch, commit_index));
                    idx += 1;
                } else {
                    break; // Gap — stop collecting
                }
            }
        }

        if batch.is_empty() {
            // Nothing to send
            return Ok(());
        }

        let batch_size = batch.len();
        let first_idx = batch[0].0;
        let last_idx = batch[batch_size - 1].0;
        let last_commit_index = batch[batch_size - 1].3;

        // Phase 2: Write all blocks to FFI in a single batch (1 flush)
        {
            let mut sent_count = 0usize;
            if let Some(c_fn) = crate::ffi::GO_CALLBACKS.get().and_then(|c| c.execute_block) {
                for (idx, data, _, _) in &batch {
                    debug!(
                        "🚀 [TX-FLOW-TRACE] ▶ PHASE 4 FFI: cgo_execute_block() called | gei={}, payload_size={} bytes",
                        idx, data.len()
                    );
                    let ffi_start = std::time::Instant::now();
                    let sched_start_ns = now_ns();
                    let data_clone = data.clone();

                    // [PERF-TRACE] Split the FFI round trip into its 3 real components so a
                    // slow block can be attributed correctly instead of lumped into one
                    // "FFI execute_block" number:
                    //   1. sched_ns   — clone() + waiting for a spawn_blocking thread pool slot
                    //   2. cgo_ns     — time actually inside the CGo call (== all of Go's work)
                    //   3. decode_ns  — time to resolve the .await + protobuf-decode the response
                    let (success, response) = {
                        let ffi_res = tokio::task::spawn_blocking(move || {
                            let thread_enter_ns = now_ns();
                            let mut out_payload: *mut u8 = std::ptr::null_mut();
                            let mut out_len = 0usize;
                            let success = c_fn(
                                data_clone.as_ptr(),
                                data_clone.len(),
                                &mut out_payload,
                                &mut out_len,
                            );
                            let thread_exit_ns = now_ns();
                            (success, out_payload as usize, out_len, thread_enter_ns, thread_exit_ns)
                        })
                        .await
                        .unwrap_or((false, 0usize, 0, 0, 0));

                        let success = ffi_res.0;
                        let out_payload = ffi_res.1 as *mut u8;
                        let out_len = ffi_res.2;
                        let thread_enter_ns = ffi_res.3;
                        let thread_exit_ns = ffi_res.4;
                        if ffi_trace_enabled() {
                            let await_done_ns = now_ns();
                            tracing::warn!(
                                "⏱️ [FFI-TRACE] gei={} stage=RUST sched_ns={} cgo_ns={} decode_ns={}",
                                idx,
                                thread_enter_ns.saturating_sub(sched_start_ns),
                                thread_exit_ns.saturating_sub(thread_enter_ns),
                                await_done_ns.saturating_sub(thread_exit_ns),
                            );
                        }

                        let response = if !out_payload.is_null() && out_len > 0 {
                            let slice = unsafe { std::slice::from_raw_parts(out_payload, out_len) };
                            let resp = super::proto::ExecuteBlockResponse::decode(slice).ok();
                            if let Some(free_fn) = crate::ffi::GO_CALLBACKS.get().and_then(|c| c.free_go_buffer) {
                                free_fn(out_payload);
                            }
                            resp
                        } else {
                            None
                        };
                        (success, response)
                    };

                    let ffi_elapsed = ffi_start.elapsed();
                    tracing::warn!(
                        "⏱️ [PERF-RUST] FFI execute_block for gei {} (size={} bytes): {:?}",
                        idx,
                        data.len(),
                        ffi_elapsed
                    );

                    if success {
                        debug!(
                            "✅ [TX-FLOW-TRACE] ▶ PHASE 4 FFI DONE: Go accepted block | gei={}",
                            idx
                        );
                        if let Some(resp) = response {
                            if !resp.state_root.is_empty() {
                                tracing::info!(
                                    "💾 [BLOCK-SEND] Go computed StateRoot for GEI={}: 0x{}",
                                    idx,
                                    hex::encode(&resp.state_root)
                                );
                            }
                        }
                        sent_count += 1;
                    } else {
                        warn!("⚠️  [BLOCK-SEND] FFI execute_block failed at GEI={}", idx);
                        self.record_send_failure().await;
                        break;
                    }
                }
            } else {
                // Socket fallback
                use tokio::io::AsyncWriteExt;
                let mut conn_guard = self.connection.lock().await;
                if let Some(ref mut stream) = *conn_guard {
                    let mut write_err = false;
                    for (idx, data, _, _) in &batch {
                        debug!(
                            "🚀 [TX-FLOW-TRACE] ▶ PHASE 4 SOCKET: send block | gei={}, payload_size={} bytes",
                            idx, data.len()
                        );
                        let mut buf = Vec::new();
                        if let Err(e) = super::persistence::write_uvarint(&mut buf, data.len() as u64) {
                            warn!("⚠️  [BLOCK-SEND] Failed to write uvarint: {}", e);
                            write_err = true;
                            break;
                        }
                        buf.extend_from_slice(data);

                        let result = async {
                            stream.write_all(&buf).await?;
                            stream.flush().await?;
                            Ok::<(), anyhow::Error>(())
                        }.await;

                        if let Err(e) = result {
                            warn!("⚠️  [BLOCK-SEND] Socket write failed at GEI={}: {}, resetting connection", idx, e);
                            *conn_guard = None;
                            write_err = true;
                            break;
                        }

                        debug!(
                            "✅ [TX-FLOW-TRACE] ▶ PHASE 4 SOCKET DONE: Go accepted block | gei={}",
                            idx
                        );
                        sent_count += 1;
                    }
                    if write_err {
                        self.record_send_failure().await;
                    }
                } else {
                    warn!("⚠️  [BLOCK-SEND] Socket connection is None");
                    self.record_send_failure().await;
                }
            }

            if sent_count > 0 {
                self.record_send_success().await;
                if batch_size > 1 && sent_count == batch_size {
                    info!(
                        "[batch_id=G{}..{}] ⚡ [BATCH-SEND] Sent {} blocks sequentially to Go FFI",
                        first_idx, last_idx, batch_size
                    );
                }
            }

            // Re-add unsent blocks back to buffer
            if sent_count < batch_size {
                let mut buffer = self.send_buffer.lock().await;
                for (idx, data, epoch, ci) in batch.iter().skip(sent_count) {
                    buffer.insert(*idx, (data.clone(), *epoch, *ci));
                }
                warn!(
                    "🔄 [BLOCK-SEND] Re-buffered {} unsent blocks (sent {}/{})",
                    batch_size - sent_count,
                    sent_count,
                    batch_size
                );
                if sent_count == 0 {
                    return Ok(());
                }
            }
        }

        // Phase 3: Update tracking state (batched — 1 lock per collection)
        {
            let mut next_expected = self.next_expected_index.lock().await;
            *next_expected = last_idx + 1;
        }
        {
            let mut sent = self.sent_indices.lock().await;
            for idx in first_idx..=last_idx {
                sent.insert(idx);
            }
            // Limit memory: trim if too large
            // H2 FIX: Fast removal using BTreeSet pop_first() (O(log N))
            // instead of O(N) iteration in HashSet, which caused O(N^2) loop.
            while sent.len() > 10000 {
                sent.pop_first();
            }
        }

        // Phase 4: Persist — only at intervals or end of batch (not every commit)
        if let Some(ref storage_path) = self.storage_path {
            if last_idx.is_multiple_of(PERSIST_INTERVAL) || batch_size > 1 {
                if let Err(e) =
                    persist_last_sent_index(storage_path, last_idx, last_commit_index).await
                {
                    warn!(
                        "⚠️ [PERSIST] Failed to persist last_sent_index={}: {}",
                        last_idx, e
                    );
                }
            }
            // Wipe-safe persist more frequently (every 10 commits) because this is
            // the critical recovery path for DAG wipe scenarios. Regular persist
            // uses PERSIST_INTERVAL=100 which is too coarse for recovery accuracy.
            const WIPE_SAFE_PERSIST_INTERVAL: u64 = 10;
            if last_idx.is_multiple_of(WIPE_SAFE_PERSIST_INTERVAL) || batch_size > 1 {
                let _ = super::persistence::persist_last_sent_index_wipe_safe(
                    storage_path,
                    last_idx,
                    last_commit_index,
                )
                .await;
            }

            // Phase 4.5: Store ExecutableBlock bytes for sync peers
            // This allows sync nodes to fetch blocks directly from Rust RocksDB
            // without going through Go PebbleDB.
            {
                let block_refs: Vec<(u64, &[u8])> = batch
                    .iter()
                    .map(|(gei, data, _, _)| (*gei, data.as_slice()))
                    .collect();
                if let Err(e) =
                    super::block_store::store_executable_blocks_batch(storage_path, &block_refs)
                        .await
                {
                    warn!(
                        "⚠️ [BLOCK STORE] Failed to store executable blocks (GEI {}→{}): {}",
                        first_idx, last_idx, e
                    );
                }
            }
        }

        // T2-2: Immediate lag estimate from buffer size (no RPC needed)
        // This runs after every flush — provides near-real-time feedback to
        // SystemTransactionProvider via go_lag_handle, even between GO_VERIFICATION_INTERVAL checks.
        {
            let buffer = self.send_buffer.lock().await;
            let buffer_lag = buffer.len() as u64;
            if let Some(ref handle) = self.go_lag_handle {
                // Use buffer size as minimum lag estimate — actual lag may be higher
                // (Go may be further behind), but buffer_lag is available immediately
                let current_lag = handle.load(std::sync::atomic::Ordering::Relaxed);
                if buffer_lag > current_lag {
                    handle.store(buffer_lag, std::sync::atomic::Ordering::Relaxed);
                }
            }
        }

        // Phase 5: Go verification (periodic RPC check)
        if last_idx.is_multiple_of(GO_VERIFICATION_INTERVAL) {
            if let Ok((go_last_block, go_last_gei, _, _, _)) = self.get_last_block_number().await {
                let mut last_verified = self.last_verified_go_index.lock().await;
                if go_last_block < *last_verified {
                    error!("🚨 [FORK DETECTED] Go's block number DECREASED! last_verified={}, go_now={}. CRITICAL: Possible fork or Go state corruption!",
                        *last_verified, go_last_block);
                }
                *last_verified = go_last_block;
                let lag = last_idx.saturating_sub(go_last_gei);
                if let Some(ref handle) = self.go_lag_handle {
                    handle.store(lag, std::sync::atomic::Ordering::Relaxed);
                }
                if lag > 100 {
                    warn!(
                        "⚠️ [GO LAG] Go is {} GEIs behind Rust. sent_gei={}, go_gei={}",
                        lag, last_idx, go_last_gei
                    );
                } else {
                    trace!(
                        "✓ [GO VERIFY] Go verified at GEI {}. Rust sent_gei={}, lag={}",
                        go_last_gei,
                        last_idx,
                        lag
                    );
                }
            }
        }

        // ═══════════════════════════════════════════════════════════════════
        // STABILITY FIX (C5): Iterative flush instead of recursive Box::pin.
        // Previously: `Box::pin(self.flush_buffer()).await` — each recursion
        // allocates a new pinned Future on the heap. With 50K+ buffered blocks
        // (snapshot restore), this could exhaust memory or stack.
        // Now: signal the outer loop to continue flushing.
        // ═══════════════════════════════════════════════════════════════════
        {
            let buffer = self.send_buffer.lock().await;
            let next_expected = self.next_expected_index.lock().await;
            if buffer.contains_key(&*next_expected) {
                drop(buffer);
                drop(next_expected);
                // Signal caller to re-invoke flush_buffer (handled by flush_buffer_loop)
                return Ok(());
            }
        }

        // Log remaining buffer status
        {
            let buffer = self.send_buffer.lock().await;
            let next_expected = self.next_expected_index.lock().await;
            if !buffer.is_empty() {
                let min_buffered = *buffer.keys().next().unwrap_or(&0);
                let max_buffered = *buffer.keys().last().unwrap_or(&0);
                let gap = min_buffered.saturating_sub(*next_expected);
                if buffer.len() > 10 {
                    warn!("⚠️  [SEQUENTIAL-BUFFER] Buffer has {} blocks waiting (range: {}..{}, next_expected={}, gap={}). Some blocks may be missing or out-of-order.",
                        buffer.len(), min_buffered, max_buffered, *next_expected, gap);
                } else {
                    info!("📊 [SEQUENTIAL-BUFFER] Buffer has {} blocks waiting (range: {}..{}, next_expected={}, gap={})",
                        buffer.len(), min_buffered, max_buffered, *next_expected, gap);
                }
            }
        }

        Ok(())
    }

    /// Send committed sub-DAG directly to Go executor (BYPASS BUFFER)
    ///
    /// This is used by SyncOnly nodes to send blocks directly without using
    /// the sequential buffer. SyncOnly may receive blocks out-of-order or
    /// with gaps, so the buffer-based approach doesn't work.
    ///
    /// IMPORTANT: This does NOT update next_expected_index or sent_indices.
    /// Go is responsible for handling ordering when receiving synced blocks.
    #[allow(dead_code)]
    pub async fn send_committed_subdag_direct(
        &self,
        subdag: &CommittedSubDag,
        epoch: u64,
        global_exec_index: u64,
        leader_address: Vec<u8>,
    ) -> Result<()> {
        if !self.is_enabled() {
            return Ok(()); // Silently skip if not enabled
        }

        // LAYER-1: Protobuf Strict Boundary — same validation as send_committed_subdag
        if !leader_address.is_empty() && leader_address.len() != 20 {
            anyhow::bail!(
                "🛡️ [LAYER-1] REJECT (direct): leader_address must be 20 bytes, got {} (commit={})",
                leader_address.len(),
                subdag.commit_ref.index
            );
        }

        self.ensure_tx_payloads_cached(subdag).await;
        let (all_proto_txs, all_system_txs) = self.build_sorted_transactions(subdag)?;

        let epoch_data = ExecutableBlock {
            transactions: all_proto_txs,
            global_exec_index,
            commit_index: subdag.commit_ref.index,
            epoch,
            commit_timestamp_ms: subdag.timestamp_ms,
            leader_author_index: subdag.leader.author.value() as u32,
            leader_address,
            block_number: 0,
            commit_hash: subdag.commit_ref.digest.into_inner().to_vec(),
            // GO-AUTHORITATIVE GEI: Disabled
            is_authoritative_gei: false,
            system_transactions: all_system_txs,
            // Layer 1: Protobuf strict boundary fields
            authority_key: vec![],
            commit_digest: vec![],
            rust_dispatch_timestamp_ms: std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_millis() as u64,
            rust_ffi_delivery_timestamp_ms: std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_millis() as u64,
        };

        let mut epoch_data_bytes = Vec::new();
        epoch_data.encode(&mut epoch_data_bytes)?;
        info!("📤 [SYNC-DIRECT] Sending block directly: global_exec_index={}, epoch={}, size={} bytes",
            global_exec_index, epoch, epoch_data_bytes.len());

        // Send directly - bypass buffer
        self.send_block_data(
            &epoch_data_bytes,
            global_exec_index,
            epoch,
            subdag.commit_ref.index,
        )
        .await?;

        info!(
            "✅ [SYNC-DIRECT] Block sent successfully: global_exec_index={}",
            global_exec_index
        );

        Ok(())
    }

    /// Send block data via UDS/TCP (internal helper)
    #[allow(dead_code)]
    pub async fn send_block_data(
        &self,
        epoch_data_bytes: &[u8],
        global_exec_index: u64,
        epoch: u64,
        commit_index: u32,
    ) -> Result<()> {
        // 🛡️ CIRCUIT BREAKER: Check if we are in Fast-Fail mode
        self.check_send_circuit_breaker().await?;

        // FFI INTEGRATION: Send directly to Go via CGo callback
        if let Some(c_fn) = crate::ffi::GO_CALLBACKS.get().and_then(|c| c.execute_block) {
            let ffi_start = std::time::Instant::now();
            let data_vec = epoch_data_bytes.to_vec();
            
            let (success, response) = {
                let ffi_res = tokio::task::spawn_blocking(move || {
                    let mut out_payload: *mut u8 = std::ptr::null_mut();
                    let mut out_len = 0usize;
                    let success = c_fn(
                        data_vec.as_ptr(),
                        data_vec.len(),
                        &mut out_payload,
                        &mut out_len,
                    );
                    (success, out_payload as usize, out_len)
                })
                .await
                .unwrap_or((false, 0usize, 0));

                let success = ffi_res.0;
                let out_payload = ffi_res.1 as *mut u8;
                let out_len = ffi_res.2;

                let response = if !out_payload.is_null() && out_len > 0 {
                    let slice = unsafe { std::slice::from_raw_parts(out_payload, out_len) };
                    let resp = super::proto::ExecuteBlockResponse::decode(slice).ok();
                    if let Some(free_fn) = crate::ffi::GO_CALLBACKS.get().and_then(|c| c.free_go_buffer) {
                        free_fn(out_payload);
                    }
                    resp
                } else {
                    None
                };
                (success, response)
            };

            let ffi_elapsed = ffi_start.elapsed();
            tracing::warn!(
                "⏱️ [PERF-RUST] FFI direct send_block_data for gei {} (size={} bytes): {:?}",
                global_exec_index,
                epoch_data_bytes.len(),
                ffi_elapsed
            );

            if success {
                trace!("📤 [TX FLOW] Sent committed sub-DAG to Go executor via FFI: global_exec_index={}, commit_index={}, epoch={}, data_size={} bytes", 
                    global_exec_index, commit_index, epoch, epoch_data_bytes.len());
                if let Some(resp) = response {
                    if !resp.state_root.is_empty() {
                        tracing::info!(
                            "💾 [BLOCK-SEND] Go computed StateRoot for send_block_data gei={}: 0x{}",
                            global_exec_index,
                            hex::encode(&resp.state_root)
                        );
                    }
                }
                self.record_send_success().await; // ✅ Mark Success
                Ok(())
            } else {
                warn!(
                    "⚠️  [EXECUTOR] Go FFI execute_block failed for global_exec_index={}",
                    global_exec_index
                );
                self.record_send_failure().await; // 🚨 Mark Failure
                Err(anyhow::anyhow!("Go FFI execute_block returned false"))
            }
        } else {
            // Socket fallback
            use tokio::io::AsyncWriteExt;
            self.connect().await?;
            let mut conn_guard = self.connection.lock().await;
            if let Some(ref mut stream) = *conn_guard {
                let mut buf = Vec::new();
                super::persistence::write_uvarint(&mut buf, epoch_data_bytes.len() as u64)?;
                buf.extend_from_slice(epoch_data_bytes);

                let result = async {
                    stream.write_all(&buf).await?;
                    stream.flush().await?;
                    Ok::<(), anyhow::Error>(())
                }.await;

                if let Err(e) = result {
                    warn!("⚠️  [EXECUTOR] Socket write failed: {}, resetting connection", e);
                    *conn_guard = None;
                    self.record_send_failure().await;
                    return Err(e);
                }

                trace!("📤 [TX FLOW] Sent committed sub-DAG to Go executor via Socket: global_exec_index={}, commit_index={}, epoch={}, data_size={} bytes", 
                    global_exec_index, commit_index, epoch, epoch_data_bytes.len());
                self.record_send_success().await;
                Ok(())
            } else {
                self.record_send_failure().await;
                Err(anyhow::anyhow!("Socket connection is None"))
            }
        }
    }

    /// PEER TX-PAYLOAD RECOVERY (2026-09-09): scans `subdag` for tx digests missing from the
    /// local TxPayloadCache and, if any are found, asks peers for them (via the tx-fetcher wired
    /// in coordination_hub.rs — see TxFetcherFn's doc comment there for why this is safe to call
    /// speculatively on every call, not just after a first failure) before `build_sorted_
    /// transactions` below gets a chance to run. A hit here means that function's own bail!()
    /// on a still-missing digest (see its doc comment) simply won't trigger; a miss (no fetcher
    /// wired yet, no peer had it either — e.g. a full-cluster restart, where every peer lost the
    /// same in-memory entry at the same time) leaves that unchanged fork-safety net exactly as
    /// it was before this existed. Always call this before `build_sorted_transactions`, never as
    /// a substitute for it.
    async fn ensure_tx_payloads_cached(&self, subdag: &CommittedSubDag) {
        let missing: Vec<consensus_types::block::TxDigest> = {
            let cache = consensus_core::get_global_tx_cache().read();
            subdag
                .blocks
                .iter()
                .flat_map(|block| block.tx_digests())
                .filter(|digest| cache.get(digest).is_none())
                .collect()
        };
        if missing.is_empty() {
            return;
        }
        // DIAG (2026-09-11, temporary): the recovery mechanism below was never observed
        // firing in production despite hitting the fork-safety bail thousands of times
        // live -- info!() (not filtered at the default "info" level, unlike the debug!()
        // this replaces) to settle whether get_global_tx_fetcher() is actually None here,
        // or Some but the fetch itself silently fails to populate the cache. Remove once
        // settled -- see note/consensus_local_dag_trust_gap_design_2026-09.md mục 10.
        let Some(fetcher) = crate::ffi::get_global_tx_fetcher() else {
            info!(
                "🔧 [TX-PAYLOAD-RECOVERY-DIAG] {} tx digest(s) missing for commit {} but \
                 get_global_tx_fetcher() returned None -- cannot even attempt peer recovery.",
                missing.len(),
                subdag.commit_ref.index
            );
            return;
        };
        info!(
            "🔧 [TX-PAYLOAD-RECOVERY-DIAG] {} tx digest(s) missing from local cache for commit {} \
             — asking peers before falling back to the existing fork-safety bail.",
            missing.len(),
            subdag.commit_ref.index
        );
        fetcher(missing.clone(), std::time::Duration::from_secs(5)).await;
        let still_missing = {
            let cache = consensus_core::get_global_tx_cache().read();
            missing.iter().filter(|d| cache.get(d).is_none()).count()
        };
        info!(
            "🔧 [TX-PAYLOAD-RECOVERY-DIAG] after peer fetch for commit {}: {}/{} digest(s) still missing.",
            subdag.commit_ref.index,
            still_missing,
            missing.len()
        );
    }

    /// Returns true if every digest this subdag's blocks reference is currently in the local
    /// TxPayloadCache (i.e. `build_sorted_transactions` would not hit a missing-payload bail).
    fn all_tx_payloads_cached(subdag: &CommittedSubDag) -> bool {
        let cache = consensus_core::get_global_tx_cache().read();
        subdag
            .blocks
            .iter()
            .flat_map(|block| block.tx_digests())
            .all(|digest| cache.get(&digest).is_some())
    }

    /// PEER-BLOCK RECOVERY (2026-09-11, real fix for a real fork): the true, general answer to
    /// "what if the raw transaction bytes aren't in ANY peer's ephemeral TxPayloadCache anymore
    /// (e.g. they already applied it long ago and it aged out of RAM)" -- reuses ALREADY-LIVE,
    /// ALREADY-PROVEN production infrastructure (network::peer_rpc's executable-block sync,
    /// currently used by SyncOnly nodes catching up -- see rust_sync_node/sync_loop.rs) instead
    /// of inventing new persistent storage.
    ///
    /// WHY THIS WORKS: every node ALREADY durably persists the exact, byte-for-byte
    /// ExecutableBlock protobuf it sends to Go for every commit it successfully delivers (see
    /// block_store.rs's doc comment -- "storage_path/executable_blocks/{gei}.bin", written
    /// right after a successful send, by every validator, unconditionally, with no pruning
    /// found in this codebase as of 2026-09-11 -- effectively an unbounded recovery window,
    /// unlike the ephemeral TxPayloadCache this bail path already fell back on before). A peer
    /// that already applied this exact commit still has this file on disk even if its RAM
    /// TxPayloadCache entry for the underlying tx has since been evicted by later traffic --
    /// EXACTLY the case that caused the real fork this fix follows (mục 11's first version
    /// treated that peer's honest "not in my RAM cache" as "nobody ever had this," which was
    /// false). Fetching and adopting the WHOLE already-built block (not just the missing raw tx
    /// bytes) is also strictly safer than reconstructing locally: it's guaranteed byte-identical
    /// to what the rest of the network already has, since it never gets re-derived at all.
    ///
    /// Tried AFTER `ensure_tx_payloads_cached`'s ordinary peer-cache recovery and BEFORE ever
    /// falling through to `build_sorted_transactions`'s bail (which leads to the
    /// halt-and-retry-forever path, and eventually the operator-only, quorum-certified skip --
    /// see block_delivery.rs / payload_loss_attestation.rs). This step being tried first should
    /// make that last-resort mechanism rarely if ever actually necessary in practice: it only
    /// remains needed for the genuinely-lost-from-every-peer's-disk-too case.
    ///
    /// Returns true if this exact commit was successfully recovered and delivered directly to
    /// Go this way (caller should treat delivery as complete, skip `build_sorted_transactions`
    /// entirely) -- false if no peer had it (or none were reachable), in which case the caller
    /// falls through to the existing bail/retry path unchanged.
    async fn try_recover_via_peer_executable_block(
        &self,
        subdag: &CommittedSubDag,
        global_exec_index: u64,
        epoch: u64,
        commit_index: u32,
    ) -> bool {
        if Self::all_tx_payloads_cached(subdag) {
            // Ordinary peer-cache recovery (ensure_tx_payloads_cached) already resolved
            // everything -- nothing for this step to do.
            return true;
        }
        let peers = crate::ffi::get_global_peer_rpc_addresses();
        if peers.is_empty() {
            return false;
        }
        let fetched = match crate::network::peer_rpc::fetch_executable_blocks_from_peer(
            &peers,
            global_exec_index,
            global_exec_index,
        )
        .await
        {
            Ok(blocks) => blocks,
            Err(e) => {
                debug!(
                    "🔧 [PEER-BLOCK-RECOVERY] fetch_executable_blocks_from_peer failed for GEI={}: {}",
                    global_exec_index, e
                );
                return false;
            }
        };
        let Some((_, data)) = fetched.into_iter().find(|(gei, _)| *gei == global_exec_index) else {
            return false;
        };
        // CORRECTNESS (2026-09-11): decode the peer's block to learn ITS block_number/epoch
        // before forwarding it as-is -- these bytes came from a DIFFERENT node's own local
        // next_block_number/last_processed_epoch counters, not this node's. Forwarding the
        // bytes unchanged (correct -- content must stay byte-identical to what the rest of the
        // network has) without ALSO advancing this node's own counters to match would leave
        // them silently stale, causing the NEXT block this node builds normally to reuse or
        // gap a block_number. `ExecutableBlock` is a plain prost::Message, decodable here with
        // zero new dependencies.
        let peer_block_number = match <ExecutableBlock as prost::Message>::decode(data.as_slice()) {
            Ok(decoded) => decoded.block_number,
            Err(e) => {
                warn!(
                    "⚠️ [PEER-BLOCK-RECOVERY] Fetched peer block for GEI={} but failed to decode \
                     it to learn its block_number: {}. Refusing to adopt it blind.",
                    global_exec_index, e
                );
                return false;
            }
        };
        info!(
            "🔧 [PEER-BLOCK-RECOVERY] Found peer's already-built ExecutableBlock for GEI={} \
             (commit={}, block_number={}, {} bytes) -- adopting it directly instead of \
             rebuilding locally.",
            global_exec_index, commit_index, peer_block_number, data.len()
        );
        match self
            .send_block_data(&data, global_exec_index, epoch, commit_index)
            .await
        {
            Ok(()) => {
                // Sync this node's own block_number/epoch counters to match what was actually
                // just delivered (see this function's doc comment on why this is necessary) --
                // never regress them if, for some reason, they're already ahead.
                {
                    let mut next_bn = self.next_block_number.lock().await;
                    if peer_block_number + 1 > *next_bn {
                        *next_bn = peer_block_number + 1;
                    }
                }
                {
                    let mut last_ep = self.last_processed_epoch.lock().await;
                    if epoch > *last_ep {
                        *last_ep = epoch;
                    }
                }
                info!(
                    "✅ [PEER-BLOCK-RECOVERY] Successfully delivered peer-recovered block for \
                     GEI={} (commit={}, block_number={}) to Go -- local counters synced.",
                    global_exec_index, commit_index, peer_block_number
                );
                true
            }
            Err(e) => {
                warn!(
                    "⚠️ [PEER-BLOCK-RECOVERY] Fetched peer block for GEI={} but send_block_data \
                     failed: {}. Falling through to the ordinary bail/retry path.",
                    global_exec_index, e
                );
                false
            }
        }
    }

    /// Build sorted, deduplicated TransactionExe list from a CommittedSubDag.
    ///
    /// This extracts the filter → dedup → sort logic from convert_to_protobuf
    /// so it can be reused by the fragmentation path. Returns `(Vec<TransactionExe>, Vec<Vec<u8>>)`
    /// where the first is deterministic order (sorted by tx hash) user transactions,
    /// and the second is the list of BCS-encoded SystemTransactions.
    fn build_sorted_transactions(
        &self,
        subdag: &CommittedSubDag,
    ) -> Result<(Vec<TransactionExe>, Vec<Vec<u8>>)> {
        use crate::types::tx_hash::{
            calculate_transaction_hash_single, verify_transaction_protobuf,
        };

        // Reconstruct transactions from digests if it's BlockV3
        let mut all_txs_to_process = Vec::new();
        let cache = consensus_core::get_global_tx_cache().read();

        for block in &subdag.blocks {
            let tx_digests = block.tx_digests();
            if !tx_digests.is_empty() {
                for digest in &tx_digests {
                    // QUORUM-CERTIFIED PAYLOAD-LOSS SKIP (2026-09-11): checked BEFORE the cache
                    // lookup, not only in the cache-miss arm -- moved here 2026-09-11 after a
                    // real fork-safety gap was spotted in review: once a quorum has certified a
                    // digest permanently lost for this exact commit, EVERY honest node must
                    // exclude it from the block, INCLUDING a node that happens to still have the
                    // payload cached (e.g. the exact "slow node" case that made peer stake look
                    // unresponsive during collection, mục 13's own fork -- that node's cache
                    // entry does not un-happen the certificate; the rest of the network already
                    // computes the block without this transaction, so this node including it
                    // anyway would be a fork of its own, one certified-skip application at a
                    // time). Checking only in the `None` arm silently missed exactly this case.
                    // Deterministic on every node holding the same certificate (every honest node
                    // reaches or receives and independently re-verifies the identical
                    // certificate, so every node computes the identical resulting
                    // all_txs_to_process, hence identical downstream fragment/GEI math and block
                    // hash; see build.rs/coordination_hub.rs for how the certificate itself is
                    // only ever recorded via an operator-triggered, quorum-verified action, never
                    // silently/automatically).
                    let claim = consensus_core::payload_loss_attestation::PayloadLossClaim {
                        commit_index: subdag.commit_ref.index,
                        tx_digest: *digest,
                    };
                    if let Some(certificate) =
                        consensus_core::payload_loss_attestation::get_certified_skip(&claim)
                    {
                        // RESUBMIT-DON'T-DISCARD (2026-09-11, per user request): a certified skip
                        // means "excluded from THIS specific commit" -- it must stay excluded
                        // here no matter what (every honest node has to compute the identical
                        // block, see the doc comment above), but it does NOT mean "this
                        // transaction must never execute". If this node happens to still hold the
                        // payload (e.g. it was one of the slower-to-lose-it peers whose absence
                        // from attestation collection was what let the certificate reach quorum
                        // in the first place -- mục 13's own incident), feed it back through the
                        // SAME resubmission path tx_recycler.rs already uses for ordinary
                        // stale/requeued transactions (`resubmit_one_stale_tx`), via the global
                        // TransactionClientProxy handle (ffi.rs) so it gets a fresh, ordinary
                        // shot at inclusion in a LATER commit instead of vanishing silently.
                        // Fire-and-forget on a background task: this function is not async, and
                        // resubmission succeeding or failing must never affect (block, delay, or
                        // fail) dispatch of THIS commit either way -- worst case, exactly today's
                        // behavior (transaction is simply gone).
                        if let Some(tx) = cache.get(digest) {
                            let tx_data = tx.data().to_vec();
                            let digest_for_log = *digest;
                            let commit_for_log = subdag.commit_ref.index;
                            if let Some(client) = crate::ffi::get_global_tx_resubmit_client() {
                                tokio::spawn(async move {
                                    use crate::node::tx_submitter::TransactionSubmitter;
                                    match client.submit(vec![tx_data]).await {
                                        Ok((block_ref, _, _)) => tracing::info!(
                                            "♻️ [PAYLOAD-LOSS-RESUBMIT] Digest {:?} (excluded from \
                                             commit {} by a certified skip, still held locally) \
                                             resubmitted -- now targeting block {:?}.",
                                            digest_for_log, commit_for_log, block_ref
                                        ),
                                        Err(e) => tracing::warn!(
                                            "♻️ [PAYLOAD-LOSS-RESUBMIT] Digest {:?} (excluded from \
                                             commit {}) resubmission failed, transaction is lost: \
                                             {}",
                                            digest_for_log, commit_for_log, e
                                        ),
                                    }
                                });
                            } else {
                                tracing::warn!(
                                    "♻️ [PAYLOAD-LOSS-RESUBMIT] Digest {:?} (excluded from commit \
                                     {}) still held locally but no TransactionClientProxy is \
                                     wired yet -- cannot resubmit, transaction is lost.",
                                    digest, commit_for_log
                                );
                            }
                        }
                        tracing::error!(
                            "🛑✅ [PAYLOAD-LOSS-SKIP-APPLIED] Skipping certified-permanently-lost \
                             transaction digest {:?} in commit {} per quorum certificate \
                             ({} attesting signatures) -- treating as absent, NOT as an error.",
                            digest, subdag.commit_ref.index, certificate.attestations.len()
                        );
                        continue;
                    }
                    match cache.get(digest) {
                        Some(tx) => all_txs_to_process.push(tx),
                        None => {
                            // FORK-SAFETY (2026-09-08): a missing digest here means this
                            // commit is NOT empty — the digest count is real, it's counted as
                            // such by both commit_is_empty_for_gei (executor.rs) and
                            // recovery.rs's own tx counter, specifically so a non-empty commit
                            // is never misclassified as empty — but the actual transaction
                            // bytes are gone: TxPayloadCache is in-memory-only and does not
                            // survive a process restart (see transaction.rs's doc comment on
                            // TX_PAYLOAD_DIR). This branch used to just warn!() and silently
                            // drop the transaction, so the block still got built and
                            // dispatched anyway — with genuinely wrong (empty-where-real)
                            // content, GEI still consumed as if nothing were wrong. That is
                            // exactly how a validator silently diverges from the rest of the
                            // network at one specific GEI: reproduced and confirmed via a real
                            // chaos-restart test (2026-09-08), where a restarted node
                            // committed an empty block at a GEI where every other validator
                            // had a real 10-tx block — identical GEI, different hash/state
                            // root, only on the node that had just restarted.
                            //
                            // Zero-Fork Invariant: never let a node silently build and
                            // dispatch content it knows is wrong. Both callers of this
                            // function propagate this Err via `?` into paths that already
                            // exist and are already correct for exactly this situation — the
                            // live delivery path (block_delivery.rs's
                            // BlockDeliveryManager::run) panics on any Err from here (with an
                            // outer resilience loop in ffi.rs, since 08780279, that retries
                            // instead of leaving Rust consensus dead forever), and the
                            // startup-replay path (recovery.rs) treats an Err as "defer to
                            // network sync". Returning Err routes this into those existing
                            // failure paths instead of inventing a new one.
                            //
                            // CORRECTION (2026-09-09, same day, confirmed live): "defer to
                            // network sync" in recovery.rs does NOT actually fetch anything —
                            // it just skips that one startup fast-path and falls through to
                            // this same live-delivery bail. If every node in the cluster
                            // restarted together and all lost the same in-memory cache entry
                            // at the same time, NOTHING here recovers it, and this becomes a
                            // real infinite retry loop (confirmed live: 600+ repeats at the
                            // exact same commit). The actual fix is the peer fetch attempted
                            // BEFORE this loop even runs — see ensure_tx_payloads_cached's doc
                            // comment above build_sorted_transactions. That only helps when at
                            // least one peer didn't lose the same entry (the common case: one
                            // node restarting while others stay up); a genuine full-cluster-
                            // simultaneous loss of this exact digest is still unrecoverable by
                            // design, and this bail is the correct, final answer for it.
                            anyhow::bail!(
                                "Missing transaction payload for digest {:?} in block {} \
                                 (commit {}) — TxPayloadCache has no entry (most likely a \
                                 post-restart cache-miss). Refusing to build a block with a \
                                 silently-dropped transaction.",
                                digest, block.reference(), subdag.commit_ref.index
                            );
                        }
                    }
                }
            } else {
                for tx in block.transactions() {
                    all_txs_to_process.push(tx.clone());
                }
            }
        }

        let mut all_transactions_with_hash: Vec<(&[u8], Vec<u8>)> = Vec::new();
        let mut system_transactions: Vec<Vec<u8>> = Vec::new();
        let mut skipped_count = 0;

        for (tx_idx, tx) in all_txs_to_process.iter().enumerate() {
            let tx_data = tx.data();
            let tx_hash = calculate_transaction_hash_single(tx_data);

            // Filter: Separate SystemTransaction (BCS format)
            if SystemTransaction::from_bytes(tx_data).is_ok() {
                system_transactions.push(tx_data.to_vec());
                skipped_count += 1;
                continue;
            }

            // Filter: Skip non-protobuf transactions
            if !verify_transaction_protobuf(tx_data) {
                let tx_hash_hex = hex::encode(&tx_hash[..8.min(tx_hash.len())]);
                trace!("⚠️ [FRAGMENT-FILTER] Skipping non-protobuf tx at index {}: hash={}...", tx_idx, tx_hash_hex);
                skipped_count += 1;
                continue;
            }

            all_transactions_with_hash.push((tx_data, tx_hash));
        }

        // Dedup by txHash
        let original_len = all_transactions_with_hash.len();
        let mut unique_txs = Vec::new();
        let mut seen = std::collections::HashSet::new();
        for (tx_data, tx_hash) in all_transactions_with_hash {
            if seen.insert(tx_hash.clone()) {
                unique_txs.push((tx_data, tx_hash));
            }
        }
        let dedup_removed = original_len - unique_txs.len();

        // Sort by txHash for deterministic ordering
        unique_txs.sort_by(|(_, hash_a), (_, hash_b)| hash_a.cmp(hash_b));

        // Convert to TransactionExe
        let transactions: Vec<TransactionExe> = unique_txs
            .iter()
            .map(|(tx_data_ref, _)| TransactionExe {
                digest: tx_data_ref.to_vec(),
                worker_id: 0,
            })
            .collect();

        info!(
            "🔪 [FRAGMENT-BUILD] Built sorted TX list: input={}, filtered={}, deduped={}, final={}, system_txs={}",
            original_len + skipped_count,
            skipped_count,
            dedup_removed,
            transactions.len(),
            system_transactions.len()
        );

        Ok((transactions, system_transactions))
    }
}
