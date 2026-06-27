use crate::consensus::tx_recycler::TxRecycler;
use crate::node::tx_submitter::TransactionSubmitter;
use crate::node::ConsensusNode;
use anyhow::Result;
use consensus_core;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use tokio::sync::RwLock;
use tracing::{debug, error, info, warn};

pub struct TxSocketServer {
    transaction_client: Arc<dyn TransactionSubmitter>,
    node: Option<Arc<RwLock<ConsensusNode>>>,
    is_transitioning: Option<Arc<AtomicBool>>,
    peer_rpc_addresses: Vec<String>,
    peer_discovery_addresses: Option<Arc<RwLock<Vec<String>>>>,
    tx_recycler: Option<Arc<TxRecycler>>,
}

impl TxSocketServer {
    pub fn with_node(
        transaction_client: Arc<dyn TransactionSubmitter>,
        node: Option<Arc<RwLock<ConsensusNode>>>,
        is_transitioning: Option<Arc<AtomicBool>>,
        peer_rpc_addresses: Vec<String>,
    ) -> Self {
        Self {
            transaction_client,
            node,
            is_transitioning,
            peer_rpc_addresses,
            peer_discovery_addresses: None,
            tx_recycler: None,
        }
    }

    pub fn with_peer_discovery(mut self, addresses: Arc<RwLock<Vec<String>>>) -> Self {
        self.peer_discovery_addresses = Some(addresses);
        self
    }

    pub fn with_tx_recycler(mut self, recycler: Arc<TxRecycler>) -> Self {
        self.tx_recycler = Some(recycler);
        self
    }

    pub async fn start(self, mut ffi_tx_receiver: tokio::sync::mpsc::Receiver<Vec<u8>>) -> Result<()> {
        let client = self.transaction_client;
        let node = self.node;
        let is_transitioning = self.is_transitioning;
        let peer_rpc_addresses = self.peer_rpc_addresses;
        let peer_discovery_addresses = self.peer_discovery_addresses;
        let tx_recycler = self.tx_recycler;

        // BOUNDED PIPELINE BACKPRESSURE: Allow up to 128 concurrent batches in flight.
        // This prevents the livelock caused by unbounded spawning during epoch transitions,
        // but solves the sequential bottleneck that was starving the DAG consensus and
        // reducing End-to-End TPS. FFI channel will block when 128 batches are pending.
        let pipeline_semaphore = Arc::new(tokio::sync::Semaphore::new(128));

        while let Some(tx_data) = ffi_tx_receiver.recv().await {
            let permit = pipeline_semaphore.clone().acquire_owned().await.unwrap();

            let client_ref = client.clone();
            let node_ref = node.clone();
            let is_transitioning_ref = is_transitioning.clone();
            let peer_rpc_addresses_ref = peer_rpc_addresses.clone();
            let peer_discovery_addresses_ref = peer_discovery_addresses.clone();
            let tx_recycler_ref = tx_recycler.clone();

            tokio::spawn(async move {
                Self::process_ffi_batch(
                    tx_data,
                    client_ref,
                    node_ref,
                    is_transitioning_ref,
                    peer_rpc_addresses_ref,
                    peer_discovery_addresses_ref,
                    tx_recycler_ref,
                )
                .await;
                drop(permit);
            });
        }
        Ok(())
    }

    async fn process_ffi_batch(
        tx_data: Vec<u8>,
        client: Arc<dyn TransactionSubmitter>,
        node: Option<Arc<RwLock<ConsensusNode>>>,
        is_transitioning: Option<Arc<AtomicBool>>,
        peer_rpc_addresses: Vec<String>,
        peer_discovery_addresses: Option<Arc<RwLock<Vec<String>>>>,
        tx_recycler: Option<Arc<TxRecycler>>,
    ) {
        use prost::bytes::Buf;
        let mut individual_txs = Vec::new();
        let mut offset = 0;
        let data_len = tx_data.len();
        let mut parse_error = false;

        // Zero-copy extraction
        while offset < data_len {
            let mut buf = &tx_data[offset..];
            let initial_remaining = buf.remaining();

            let tag = match prost::encoding::decode_varint(&mut buf) {
                Ok(t) => t,
                Err(_) => {
                    parse_error = true;
                    break;
                }
            };

            let tag_len = initial_remaining - buf.remaining();
            if tag_len == 0 {
                parse_error = true;
                break;
            }
            offset += tag_len;

            let field_number = tag >> 3;
            let wire_type = tag & 0x07;

            if field_number == 1 && wire_type == 2 {
                let mut buf_val = &tx_data[offset..];
                let init_rem = buf_val.remaining();
                let length = match prost::encoding::decode_varint(&mut buf_val) {
                    Ok(l) => l as usize,
                    Err(_) => {
                        parse_error = true;
                        break;
                    }
                };
                let length_varint_size = init_rem - buf_val.remaining();
                offset += length_varint_size;

                if offset + length <= data_len {
                    individual_txs.push(tx_data[offset..offset + length].to_vec());
                } else {
                    parse_error = true;
                    break;
                }
                offset += length;
            } else {
                match wire_type {
                    0 => {
                        let mut buf_varint = &tx_data[offset..];
                        let init_rem = buf_varint.remaining();
                        let _ = prost::encoding::decode_varint(&mut buf_varint).unwrap_or(0);
                        offset += init_rem - buf_varint.remaining();
                    }
                    1 => offset += 8,
                    2 => {
                        let mut buf_len = &tx_data[offset..];
                        let init_rem = buf_len.remaining();
                        let skip_len = match prost::encoding::decode_varint(&mut buf_len) {
                            Ok(l) => l as usize,
                            Err(_) => {
                                parse_error = true;
                                break;
                            }
                        };
                        offset += (init_rem - buf_len.remaining()) + skip_len;
                    }
                    5 => offset += 4,
                    _ => {
                        parse_error = true;
                        break;
                    }
                }
            }
        }

        if parse_error || individual_txs.is_empty() {
            error!("❌ [FFI TX FLOW] Failed to decode Transactions message");
            return;
        }

        debug!("📦 [TX-FLOW-TRACE] ▶ PHASE 1.5: Rust TxSocketServer decoded batch | tx_count={} | raw_batch_size={} bytes",
            individual_txs.len(), data_len);
        if crate::ffi::TX_TRACE_ENABLED.load(std::sync::atomic::Ordering::Relaxed) {
            for tx_bytes in &individual_txs {
                let tx_hash = crate::types::tx_hash::calculate_transaction_hash_single(tx_bytes);
                crate::ffi::update_go_tx_trace(&tx_hash, "RUST_RECEIVED", "Transaction received and decoded by Rust consensus socket server");
            }
        }
        let transactions_to_submit = individual_txs;

        // RETRY LOOP FOR EPOCH TRANSITIONS
        let mut attempt = 0;
        let mut current_client = client;

        loop {
            // Lock-free transitioning check
            if let Some(ref transitioning) = is_transitioning {
                if transitioning.load(Ordering::SeqCst) {
                    warn!("⚡ [FFI TX FLOW] Epoch transition in progress. Delaying {} TXs internally.", transactions_to_submit.len());
                    attempt += 1;
                    if attempt % 20 == 0 {
                        warn!("⏳ [FFI TX FLOW] Epoch transition still in progress. Waited {}s for {} TXs.", attempt / 20, transactions_to_submit.len());
                    }
                    // SAFETY TIMEOUT: Prevent permanent deadlock if is_transitioning
                    // flag is never cleared (same pattern as CommitProcessor).
                    // After 60s (1200 attempts at 50ms), force-clear and proceed to submission.
                    if attempt >= 1200 {
                        error!(
                            "🚨 [FFI TX FLOW] is_transitioning stuck for {}s! Force-clearing to prevent permanent TX deadlock.",
                            attempt / 20
                        );
                        transitioning.store(false, Ordering::SeqCst);
                        // Fall through to submission
                    } else {
                        tokio::time::sleep(std::time::Duration::from_millis(50)).await;
                        continue;
                    }
                }
            }

            // Node acceptance check (takes node lock momentarily)
            if let Some(ref node_arc) = node {
                let lock_result = tokio::time::timeout(std::time::Duration::from_millis(200), node_arc.read()).await;
                match lock_result {
                    Ok(node_guard) => {
                        let (should_accept, should_queue, reason) = node_guard.check_transaction_acceptance().await;
                        
                        // Update current_client just in case we transitioned recently
                        if let Some(fresh_submitter) = node_guard.transaction_submitter() {
                            current_client = fresh_submitter;
                        }

                        if should_queue {
                            debug!("📨 [FFI TX FLOW] Queueing {} transactions for next epoch: {}", transactions_to_submit.len(), reason);
                            let _ = node_guard.queue_transactions_for_next_epoch(transactions_to_submit.clone()).await;
                            return; // Enqueued successfully
                        }

                        if !should_accept {
                            let is_sync_only = reason.contains("Node is still initializing");
                            if is_sync_only {
                                // Fallback to discovery addresses if peer_rpc_addresses is empty
                                let mut targets = peer_rpc_addresses.clone();
                                if targets.is_empty() {
                                    if let Some(ref discovery_lock) = peer_discovery_addresses {
                                        targets = discovery_lock.read().await.clone();
                                    }
                                }

                                if !targets.is_empty() {
                                    info!(
                                        "📡 [FFI TX FLOW] Node is running in SyncOnly. Attempting to forward {} TXs to active validators...",
                                        transactions_to_submit.len()
                                    );
                                    let mut forwarded = false;
                                    let mut explicitly_rejected = false;
                                    for peer_addr in &targets {
                                        match crate::network::peer_rpc::forward_transactions_to_peer(
                                            peer_addr,
                                            transactions_to_submit.clone(),
                                        )
                                        .await
                                        {
                                            Ok(resp) => {
                                                if resp.success {
                                                    info!(
                                                        "📡 [FFI TX FLOW] Successfully forwarded {} TXs to validator {}",
                                                        transactions_to_submit.len(),
                                                        peer_addr
                                                    );
                                                    forwarded = true;
                                                    break;
                                                } else {
                                                    warn!(
                                                        "📡 [FFI TX FLOW] Validator {} rejected forwarded transactions: {:?}",
                                                        peer_addr, resp.error
                                                    );
                                                    explicitly_rejected = true;
                                                }
                                            }
                                            Err(e) => {
                                                warn!(
                                                    "📡 [FFI TX FLOW] Failed to forward transactions to validator {}: {}",
                                                    peer_addr, e
                                                );
                                            }
                                        }
                                    }
                                    if forwarded || explicitly_rejected {
                                        return; // Exit thread. If rejected, drop it permanently so client can retry/fail.
                                    }
                                }

                                warn!("⏳ [FFI TX FLOW] Node is catching up. Delaying {} TXs internally (attempt {}/20).", transactions_to_submit.len(), attempt + 1);
                                drop(node_guard);
                                attempt += 1;
                                if attempt >= 20 {
                                    error!("🚨 [FFI TX FLOW] Dropping {} TXs after 20 failed attempts to forward. Preventing FFI channel deadlock.", transactions_to_submit.len());
                                    return;
                                }
                                tokio::time::sleep(std::time::Duration::from_millis(50)).await;
                                continue;
                            }

                            warn!("🚫 [FFI TX FLOW] Rejecting {} TXs: {}", transactions_to_submit.len(), reason);
                            return; // Permanent failure
                        }
                    }
                    Err(_) => {
                        // Lock timeout. If transitioning, sleep and retry. Else proceed.
                        let is_epoch_transition = is_transitioning
                            .as_ref()
                            .is_some_and(|flag| flag.load(Ordering::SeqCst));

                        if is_epoch_transition {
                            attempt += 1;
                            tokio::time::sleep(std::time::Duration::from_millis(50)).await;
                            continue;
                        }
                    }
                }
            }

            // Submission phase
            const MAX_BUNDLE_SIZE: usize = 50000;
            let total_tx_count = transactions_to_submit.len();
            // let mut total_submitted = 0usize;

            let mut broadcast_peers = peer_rpc_addresses.clone();
            if let Some(ref discovery_lock) = peer_discovery_addresses {
                let discovered = discovery_lock.read().await.clone();
                if !discovered.is_empty() {
                    broadcast_peers = discovered;
                }
            }

            let chunks_list: Vec<Vec<Vec<u8>>> = if total_tx_count <= MAX_BUNDLE_SIZE {
                vec![transactions_to_submit.clone()]
            } else {
                transactions_to_submit.chunks(MAX_BUNDLE_SIZE).map(|c| c.to_vec()).collect()
            };

            let mut all_succeeded = true;
            for (_chunk_idx, chunk_vec) in chunks_list.into_iter().enumerate() {
                // Background mempool pre-propagation to peer validators to populate their caches
                // and completely avoid missing transaction sync stalls during consensus voting.
                if !broadcast_peers.is_empty() {
                    let chunk_clone = chunk_vec.clone();
                    for peer_addr in broadcast_peers.clone() {
                        let chunk = chunk_clone.clone();
                        tokio::spawn(async move {
                            let _ = crate::network::peer_rpc::forward_transactions_to_peer(
                                &peer_addr,
                                chunk,
                            )
                            .await;
                        });
                    }
                }

                // STABILITY FIX: epoch_pending_transactions tracking REMOVED from hot path.
                //
                // ROOT CAUSE OF TX LOSS: Previously, every TX was inserted into 
                // epoch_pending_transactions HashMap here. After 8 rounds × 20K TXs = 160K entries,
                // the HashMap caused severe mutex lock contention. When the 200ms lock timeout
                // fired, TXs were submitted to consensus but NOT tracked in epoch_pending.
                // During epoch transition, recover_epoch_pending_transactions would only recover
                // tracked TXs → untracked TXs lost forever.
                //
                // FIX: TxRecycler already tracks ALL submitted TXs with bounded capacity (100K)
                // and persists them to disk. Epoch transition now drains TxRecycler pending
                // instead of epoch_pending_transactions. This eliminates:
                // 1. Unbounded HashMap growth (160K+ entries)
                // 2. Mutex lock contention on submission hot path
                // 3. Lock timeout causing untracked TXs that can't be recovered

                if let Some(ref recycler) = tx_recycler {
                    recycler.track_submitted(&chunk_vec).await;
                }

                let chunk_len = chunk_vec.len();
                // RUST_SUBMITTED trace
                if crate::ffi::TX_TRACE_ENABLED.load(std::sync::atomic::Ordering::Relaxed) {
                    for tx_bytes in &chunk_vec {
                        let tx_hash = crate::types::tx_hash::calculate_transaction_hash_single(tx_bytes);
                        crate::ffi::update_go_tx_trace(&tx_hash, "RUST_SUBMITTED", "Transaction submitted to Rust consensus DAG proposer");
                    }
                }
                match current_client.submit_no_wait(chunk_vec).await {
                    Ok(included_in_block_rx) => {
                        debug!("✅ [TX-FLOW-TRACE] ▶ PHASE 2: Submitted batch of {} txs to consensus Proposer", chunk_len);
                        // total_submitted += chunk_len;
                        // STABILITY FIX: We await block inclusion to provide backpressure!
                        // Fire-and-forget causes unbounded mempool growth during blast tests,
                        // leading to SyncOnly states. By awaiting here, we propagate backpressure
                        // up to the FFI channel -> Go mempool -> TCP sockets.
                        // Timeout lowered to 2s to prevent holding FFI semaphore permits for too long.
                        match tokio::time::timeout(std::time::Duration::from_secs(2), included_in_block_rx).await {
                            Ok(Ok((_block_ref, _indices, status_receiver))) => {
                                tokio::spawn(async move {
                                    if let Ok(consensus_core::BlockStatus::GarbageCollected(gc_block)) = status_receiver.await {
                                        warn!("♻️ [FFI TX STATUS] Block {:?} Garbage Collected.", gc_block);
                                    }
                                });
                            }
                            Ok(Err(e)) => {
                                warn!("⚠️ [FFI TX FLOW] Failed to get inclusion confirmation: {}", e);
                            }
                            Err(_) => {
                                warn!("⏰ [FFI TX FLOW] Timeout waiting for block inclusion. Consensus might be congested.");
                            }
                        }
                    }
                    Err(e) => {
                        let err_str = e.to_string();
                        if err_str.contains("SyncOnly") || err_str.contains("shutting down") || err_str.contains("channel closed") {
                            warn!("♻️ [FFI TX FLOW] Transition context loss. Delaying internally. Error: {}", err_str);
                            all_succeeded = false;
                            break;
                        } else {
                            error!("❌ [FFI TX FLOW] Submission failed fatally: {}", e);
                            return; // Fatal failure: stop retrying and discard batch
                        }
                    }
                }
            }

            if all_succeeded {
                debug!("✅ [TX-FLOW-TRACE] ▶ PHASE 1.5 DONE: All {} TXs submitted to consensus DAG core", total_tx_count);
                return; // Everything submitted cleanly
            }

            // If we broke out early due to transient transition error, sleep and retry
            attempt += 1;
            if attempt % 20 == 0 {
                warn!("⏳ [FFI TX FLOW] Delayed TXs for {}s due to submission failure.", attempt / 20);
            }
            tokio::time::sleep(std::time::Duration::from_millis(50)).await;
        }
    }
}
