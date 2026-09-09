use crate::consensus::tx_recycler::TxRecycler;
use crate::node::tx_submitter::TransactionSubmitter;
use crate::node::ConsensusNode;
use anyhow::Result;
use consensus_core;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};
use tokio::sync::RwLock;
use tracing::{debug, error, info, warn};

/// Bounds how long a single FFI batch may wait for its turn to actually
/// submit to consensus (see SubmitOrderGate below) before proceeding out
/// of order anyway. This is a pure liveness safety valve, not a
/// performance tuning knob -- per this fix's "thà pending chứ không fork"
/// mandate, waiting longer only ever slows the pipeline down (the bounded
/// pipeline_semaphore and ffi_tx_receiver channel naturally propagate that
/// as backpressure all the way back to Go's own SubmitTransactionBatch
/// caller -- a safe, correct outcome), whereas timing out too early risks
/// the exact reordering this fix exists to prevent.
///
/// CONFIRMED LIVE (2026-09-03): an earlier, much shorter value (5s) was
/// tried first and looked perfect at 1,000,000-tx scale (0 nonce-rejects,
/// 384 timeouts fired but none happened to collide with a shared sender)
/// -- but at 4,000,000-tx scale, sustained load caused some predecessor
/// batch to genuinely take longer than 5s, and this time the timeout's
/// "proceed out of order anyway" fallback DID collide with a shared
/// sender, reproducing 5,331 FAST-PATH-NONCE-REJECT events (far fewer
/// than the ~178-direct-plus-cascade seen before this fix entirely, but
/// not zero). Raised to comfortably exceed process_ffi_batch's own
/// internal epoch-transition retry cap (60s, `attempt >= 1200` at 50ms
/// each, a few dozen lines below) so this gate is never the shorter
/// timeout in that race -- a batch legitimately taking that long to clear
/// its own retry loop should still get to submit in order once it does,
/// not have this gate give up on it first.
const SUBMIT_ORDER_TIMEOUT: Duration = Duration::from_secs(90);

/// CONFIRMED ROOT CAUSE (2026-09-03): `TxSocketServer::start()`'s receive
/// loop `tokio::spawn`s an independent, concurrently-scheduled task for
/// every incoming FFI batch (see the 512-permit `pipeline_semaphore` a few
/// lines below -- added for throughput, to avoid a *different*,
/// already-fixed livelock). Go's own FFI calls arrive at `ffi_tx_receiver`
/// in strict order, but nothing then constrains which spawned task's
/// `submit_no_wait()` call actually reaches Rust's consensus submission
/// channel first -- tokio task scheduling under load is not required to
/// preserve spawn order. When a single sender's sequential nonces get
/// split across two separate Go-side batches (very common under
/// sustained load: a batch-size cap, a per-tick backlog cap, or simply
/// the sender's transactions arriving to Go's mempool at different real
/// times), the *later* nonces' batch can win this race and reach Rust's
/// TransactionConsumer before the *earlier* nonces' batch -- Rust just
/// includes what it received into blocks in received order, since it has
/// no way to know two unrelated FFI calls actually belonged to the same
/// logical sequence.
///
/// This surfaced downstream as Go's own execution engine
/// (native_fast_path.go) permanently, silently dropping the "ahead of
/// state" transaction and everything after it for that sender in that
/// block (confirmed live: ~178 "FAST-PATH-NONCE-REJECT" log lines in a
/// single 1,000,000-tx run, with gaps up to 25 between tx.Nonce() and
/// state.Nonce()) -- but the true defect is here: two validators could,
/// under different real-time scheduling/load conditions, order the SAME
/// two batches differently and compute genuinely different results for
/// the same GEI. That is a real correctness/determinism hazard, not just
/// a throughput one -- explicitly treated as fork-risk-adjacent per this
/// project's Zero-Fork invariant, even though this specific devnet only
/// ever ran a single validator so no divergence could be *observed* here.
///
/// Fix: assign each batch a ticket in strict arrival order (in the single
/// receive loop, before spawning), and have its task wait for that ticket
/// to become current immediately before its own submission phase -- so
/// concurrent PARSING/validation/epoch-transition-waiting is unaffected
/// (preserving the throughput this concurrency exists for), but the
/// actual handoff to consensus always happens in the same order Go
/// originally sent it in. Bounded by SUBMIT_ORDER_TIMEOUT so a stuck or
/// slow batch can only ever delay, never permanently block, everything
/// behind it: "thà pending chứ không fork" -- prefer waiting (or, in the
/// worst case, a late fallback) to correctness risk, but never a
/// node-wide hang. SubmitTicket's Drop impl advances the gate on every
/// exit path (early return, error, or normal completion) so a batch that
/// never reaches wait_for_turn at all (e.g. a parse error, or being
/// queued for the next epoch) can never starve tickets behind it either.
struct SubmitOrderGate {
    next_ticket: AtomicU64,
    notify: tokio::sync::Notify,
}

impl SubmitOrderGate {
    fn new() -> Self {
        Self {
            next_ticket: AtomicU64::new(0),
            notify: tokio::sync::Notify::new(),
        }
    }

    async fn wait_for_turn(&self, my_ticket: u64, timeout: Duration) {
        let deadline = Instant::now() + timeout;
        loop {
            if self.next_ticket.load(Ordering::Acquire) >= my_ticket {
                return;
            }
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                warn!(
                    "⏰ [FFI-ORDER] Timed out after {:?} waiting for submit ticket {} (current={}) -- proceeding out of order rather than stalling the pipeline.",
                    timeout,
                    my_ticket,
                    self.next_ticket.load(Ordering::Acquire)
                );
                return;
            }
            // A spurious/unrelated wakeup just re-checks the condition above
            // and goes back to waiting for the remaining time -- never a
            // correctness issue, at most a few redundant loop iterations.
            let _ = tokio::time::timeout(remaining, self.notify.notified()).await;
        }
    }

    /// Advances the gate to at least `ticket + 1` (never backwards, safe to
    /// call redundantly or out of order) and wakes every waiter to
    /// re-check its own turn.
    fn advance_past(&self, ticket: u64) {
        let mut current = self.next_ticket.load(Ordering::Acquire);
        loop {
            let target = current.max(ticket + 1);
            if target == current {
                break;
            }
            match self.next_ticket.compare_exchange_weak(
                current,
                target,
                Ordering::AcqRel,
                Ordering::Acquire,
            ) {
                Ok(_) => break,
                Err(actual) => current = actual,
            }
        }
        self.notify.notify_waiters();
    }
}

/// RAII ticket: guarantees `SubmitOrderGate::advance_past` is called
/// exactly once when this value is dropped, on ANY exit path from the
/// scope holding it -- see SubmitOrderGate's doc comment for why this
/// matters (a missed advance on one of process_ffi_batch's many early
/// returns would otherwise starve every ticket behind it).
struct SubmitTicket {
    gate: Arc<SubmitOrderGate>,
    ticket: u64,
    released: bool,
}

impl SubmitTicket {
    fn new(gate: Arc<SubmitOrderGate>, ticket: u64) -> Self {
        Self { gate, ticket, released: false }
    }

    async fn wait_for_turn(&self, timeout: Duration) {
        self.gate.wait_for_turn(self.ticket, timeout).await;
    }

    /// Releases this ticket NOW, letting the next ticket proceed
    /// immediately, instead of waiting for this value to be dropped at the
    /// end of its enclosing scope.
    ///
    /// LATENCY FIX (2026-09-03): must be called right after submit_no_wait
    /// returns -- the actual, only order-determining moment (the handoff
    /// to consensus's own submission channel) -- not left to fire on
    /// scope exit. process_ffi_batch's submission phase, AFTER
    /// submit_no_wait returns, awaits its own block-inclusion confirmation
    /// for up to 30s (a per-caller backpressure mechanism, unrelated to
    /// ordering) via `included_in_block_rx`. Before this fix, that 30s
    /// wait ran INSIDE this ticket's still-held scope, so every later
    /// ticket's wait_for_turn blocked on it too -- turning what used to be
    /// up to 512 (pipeline_semaphore) *concurrent* backpressure waits into
    /// a fully serial chain. Confirmed live: sustained 800 tx/s showed a
    /// tight, suspicious ~65-75s latency band -- 512 * ~130ms lines up
    /// almost exactly with a full pipeline_semaphore's worth of batches
    /// each serialized through roughly a consensus-round's cost. Calling
    /// release() here restores the original parallel backpressure-waiting
    /// behavior while still keeping the actual fix (ordered handoff to
    /// consensus) intact. Idempotent -- Drop only acts if this was never
    /// called (e.g. an early return before ever reaching submission).
    fn release(&mut self) {
        if !self.released {
            self.released = true;
            self.gate.advance_past(self.ticket);
        }
    }
}

impl Drop for SubmitTicket {
    fn drop(&mut self) {
        self.release();
    }
}

// TEMPORARY DIAGNOSTIC (2026-09-02): counts block-inclusion outcomes for
// submit_no_wait, bypassing the tracing subscriber entirely (eprintln!
// writes straight to stderr) to settle whether the tracing-level warn!s a
// few lines below are actually firing under extreme load or are themselves
// being dropped by the tracing subscriber the same way Go's own logger was
// found silently dropping lines under load earlier the same day (see
// execution/pkg/logger's async queue fix). Remove once that's settled.
static DIAG_GC_COUNT: AtomicU64 = AtomicU64::new(0);
static DIAG_TIMEOUT_COUNT: AtomicU64 = AtomicU64::new(0);
static DIAG_OK_COUNT: AtomicU64 = AtomicU64::new(0);
static DIAG_LAST_PRINT_SECS: AtomicU64 = AtomicU64::new(0);

fn diag_maybe_print() {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let last = DIAG_LAST_PRINT_SECS.load(Ordering::Relaxed);
    if now >= last + 2
        && DIAG_LAST_PRINT_SECS
            .compare_exchange(last, now, Ordering::Relaxed, Ordering::Relaxed)
            .is_ok()
    {
        eprintln!(
            "[DIAG submit_no_wait] ok={} gc={} timeout={}",
            DIAG_OK_COUNT.load(Ordering::Relaxed),
            DIAG_GC_COUNT.load(Ordering::Relaxed),
            DIAG_TIMEOUT_COUNT.load(Ordering::Relaxed)
        );
    }
}

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

        // BOUNDED PIPELINE BACKPRESSURE: Allow up to 512 concurrent batches in flight.
        // This prevents the livelock caused by unbounded spawning during epoch transitions,
        // but solves the sequential bottleneck that was starving the DAG consensus and
        // reducing End-to-End TPS. FFI channel will block when 512 batches are pending.
        let pipeline_semaphore = Arc::new(tokio::sync::Semaphore::new(512));

        // See SubmitOrderGate's doc comment: preserves Go's original FFI
        // call order at the point of actual consensus submission, without
        // giving up the concurrency pipeline_semaphore exists for.
        let submit_order_gate = Arc::new(SubmitOrderGate::new());
        let mut next_ticket: u64 = 0;

        while let Some(tx_data) = ffi_tx_receiver.recv().await {
            let permit = pipeline_semaphore.clone().acquire_owned().await.unwrap();

            let client_ref = client.clone();
            let node_ref = node.clone();
            let is_transitioning_ref = is_transitioning.clone();
            let peer_rpc_addresses_ref = peer_rpc_addresses.clone();
            let peer_discovery_addresses_ref = peer_discovery_addresses.clone();
            let tx_recycler_ref = tx_recycler.clone();
            let my_ticket = SubmitTicket::new(submit_order_gate.clone(), next_ticket);
            next_ticket += 1;

            tokio::spawn(async move {
                Self::process_ffi_batch(
                    tx_data,
                    client_ref,
                    node_ref,
                    is_transitioning_ref,
                    peer_rpc_addresses_ref,
                    peer_discovery_addresses_ref,
                    tx_recycler_ref,
                    my_ticket,
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
        mut submit_ticket: SubmitTicket,
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
                                    let tx_hex_list: Vec<String> = transactions_to_submit.iter().map(hex::encode).collect();
                                    // Delegated submission: this SyncOnly node cannot propose,
                                    // so the receiving validator MUST submit to its consensus.
                                    let req = crate::network::peer_rpc::SubmitTransactionRequest {
                                        transactions_hex: tx_hex_list,
                                        cache_only: false,
                                    };
                                    let body_arc_opt = match serde_json::to_string(&req) {
                                        Ok(body) => Some(std::sync::Arc::new(body)),
                                        Err(e) => {
                                            error!("❌ [FFI TX FLOW] Failed to serialize SyncOnly transactions: {}", e);
                                            None
                                        }
                                    };

                                    if let Some(body_arc) = body_arc_opt {
                                        for peer_addr in &targets {
                                            match crate::network::peer_rpc::forward_serialized_transactions_to_peer(
                                                peer_addr,
                                                body_arc.clone(),
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

            // ORDER GATE: wait for this batch's turn before actually handing
            // anything to consensus (see SubmitOrderGate's doc comment for
            // the full incident this closes). Everything above this point
            // (parsing, epoch-transition/node-acceptance waiting, SyncOnly
            // peer-forwarding) still runs fully concurrently across batches
            // -- only the final submission is ordered.
            submit_ticket.wait_for_turn(SUBMIT_ORDER_TIMEOUT).await;

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
                // Background mempool pre-propagation to peer validators to populate their
                // compact-block (BlockV3) reconstruction caches and avoid missing
                // transaction fetch stalls during consensus voting.
                // cache_only=true: this node submits the TXs to its OWN proposer below;
                // peers must NOT also propose them. Before this flag, every peer
                // re-submitted the same TXs into its own consensus → each TX proposed by
                // up to `committee_size` validators → Go execution rejected the duplicate
                // copies one-by-one via nonce checks (262K rejects per 150K real TXs),
                // which dominated end-to-end latency.
                if !broadcast_peers.is_empty() {
                    let tx_hex_list: Vec<String> = chunk_vec.iter().map(hex::encode).collect();
                    let req = crate::network::peer_rpc::SubmitTransactionRequest {
                        transactions_hex: tx_hex_list,
                        cache_only: true,
                    };
                    match serde_json::to_string(&req) {
                        Ok(body) => {
                            let body_arc = std::sync::Arc::new(body);
                            for peer_addr in broadcast_peers.clone() {
                                let body_clone = body_arc.clone();
                                tokio::spawn(async move {
                                    let _ = crate::network::peer_rpc::forward_serialized_transactions_to_peer(
                                        &peer_addr,
                                        body_clone,
                                    )
                                    .await;
                                });
                            }
                        }
                        Err(e) => {
                            error!("❌ [FFI TX FLOW] Failed to serialize pre-propagation transactions: {}", e);
                        }
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
                let submit_result = current_client.submit_no_wait(chunk_vec).await;
                // ORDER GATE: release now. The handoff to consensus's own
                // submission channel just happened (or definitively
                // failed) -- the only moment that actually determines
                // cross-batch order. See SubmitTicket::release's doc
                // comment: everything after this point, including the
                // up-to-30s block-inclusion backpressure wait a few lines
                // below, is this task's own concern and must not
                // serialize every later ticket behind it too.
                submit_ticket.release();
                match submit_result {
                    Ok(included_in_block_rx) => {
                        debug!("✅ [TX-FLOW-TRACE] ▶ PHASE 2: Submitted batch of {} txs to consensus Proposer", chunk_len);
                        // total_submitted += chunk_len;
                        // STABILITY FIX: We await block inclusion to provide backpressure!
                        // Fire-and-forget causes unbounded mempool growth during blast tests,
                        // leading to SyncOnly states. By awaiting here, we propagate backpressure
                        // up to the FFI channel -> Go mempool -> TCP sockets.
                        // [DIAGNOSTIC — temporary, not the final fix] Raised from 2s to 30s to test
                        // whether this ack-wait timing out under heavy load correlates with the
                        // ~3-22% permanent tx loss observed at 500k-tx scale (batches whose ack
                        // times out are never retried or tracked past this point — see the warn!
                        // below). If loss disappears with a longer timeout, that's strong evidence
                        // this is where it happens, even though the tx should still be sitting in
                        // consensus's mempool at timeout time; if it doesn't, the loss is elsewhere
                        // and this revert is a one-line diff.
                        match tokio::time::timeout(std::time::Duration::from_secs(30), included_in_block_rx).await {
                            Ok(Ok((_block_ref, _indices, status_receiver))) => {
                                DIAG_OK_COUNT.fetch_add(1, Ordering::Relaxed);
                                diag_maybe_print();
                                tokio::spawn(async move {
                                    if let Ok(consensus_core::BlockStatus::GarbageCollected(gc_block)) = status_receiver.await {
                                        DIAG_GC_COUNT.fetch_add(1, Ordering::Relaxed);
                                        warn!("♻️ [FFI TX STATUS] Block {:?} Garbage Collected.", gc_block);
                                        // FOLLOW-UP (found 2026-09-02, continuing the "DIAGNOSTIC"
                                        // comment a few lines above this one from an earlier
                                        // investigation into the same tx-loss symptom): after
                                        // exhausting every possible Go-side cause of transaction
                                        // loss (see execution/pkg/transaction_pool and
                                        // tx_validator_pool_core.go's localNonceFloor -- confirmed
                                        // via live tracing that the Go mempool now validates and
                                        // forwards 100% of what it receives, 0 evictions, 0
                                        // duplicate-key rejections, 0 stuck future-nonce
                                        // transactions, at every injection rate tested including
                                        // 45,000+ tx/s sustained), this branch was suspected as the
                                        // source of the REMAINING end-to-end loss at that same
                                        // extreme scale (garbage collection dropping already-
                                        // forwarded transactions with no resubmit). RULED OUT by
                                        // DIAG_OK_COUNT/DIAG_GC_COUNT/DIAG_TIMEOUT_COUNT (added
                                        // specifically for this, eprintln! bypassing tracing
                                        // entirely so a dropped-log explanation couldn't hide the
                                        // answer): during a live 1,000,000-tx run that still lost
                                        // ~51% end-to-end, the "[DIAG submit_no_wait]" line never
                                        // printed even once -- meaning Ok(Ok(..)) here, this GC
                                        // check, Ok(Err(..)) just below, AND the 30s timeout arm all
                                        // stayed at zero for the entire run, despite ~487,000
                                        // transactions genuinely landing on-chain in that same run.
                                        // That can only mean this specific call site
                                        // (current_client.submit_no_wait(chunk_vec) a few lines up,
                                        // reached via TxSocketServer::start()'s ffi_tx_receiver loop)
                                        // was NOT the path those on-chain transactions -- or the lost
                                        // ones -- actually took. tx_submitter.rs defines FOUR
                                        // different TransactionSubmitter implementations; which one
                                        // `current_client` resolves to for a single-validator/
                                        // block-signer node (this devnet's role) was not confirmed
                                        // before this session ended, and is the actual next question
                                        // -- not garbage collection, and not this file, unless that
                                        // confirms this IS the active implementation and something
                                        // upstream of this match (e.g. the node.read() acceptance
                                        // check a few dozen lines above, or the loop in
                                        // TxSocketServer::start() never being reached at all for
                                        // this node's config) is what actually needs the next
                                        // diagnostic pass. Do not assume GC is the cause without
                                        // first re-confirming DIAG_OK_COUNT actually increments for
                                        // this node's real submission path.
                                    }
                                });
                            }
                            Ok(Err(e)) => {
                                warn!("⚠️ [FFI TX FLOW] Failed to get inclusion confirmation: {}", e);
                            }
                            Err(_) => {
                                DIAG_TIMEOUT_COUNT.fetch_add(1, Ordering::Relaxed);
                                diag_maybe_print();
                                warn!("⏰ [FFI TX FLOW] Timeout waiting for block inclusion ({} txs). Consensus might be congested.", chunk_len);
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

#[cfg(test)]
mod submit_order_gate_tests {
    use super::*;

    /// Regression test for the CONFIRMED root cause: without SubmitOrderGate,
    /// two concurrently-spawned batches' actual submission order depends on
    /// which one finishes its own prep work first, not the order they
    /// arrived in. Ticket 2 here finishes prep fastest, ticket 0 slowest --
    /// a naive concurrent implementation would submit in completion order
    /// [2, 1, 0]; SubmitOrderGate must enforce arrival order [0, 1, 2]
    /// regardless.
    #[tokio::test]
    async fn tickets_enter_submission_in_ticket_order_not_completion_order() {
        let gate = Arc::new(SubmitOrderGate::new());
        let order = Arc::new(tokio::sync::Mutex::new(Vec::new()));

        let mut handles = Vec::new();
        for (ticket, prep_delay_ms) in [(0u64, 30u64), (1, 15), (2, 0)] {
            let gate = gate.clone();
            let order = order.clone();
            handles.push(tokio::spawn(async move {
                // Simulates each batch's own variable-length prep work
                // (parsing, node-acceptance checks, etc.) finishing at
                // different real times, independent of arrival order.
                tokio::time::sleep(Duration::from_millis(prep_delay_ms)).await;
                let t = SubmitTicket::new(gate.clone(), ticket);
                t.wait_for_turn(Duration::from_secs(5)).await;
                order.lock().await.push(ticket);
                // t drops here, advancing the gate for the next ticket.
            }));
        }
        for h in handles {
            h.await.unwrap();
        }
        assert_eq!(
            *order.lock().await,
            vec![0, 1, 2],
            "submission must happen in ticket (arrival) order despite prep work finishing in the opposite order"
        );
    }

    /// Regression test for the CONFIRMED latency incident (2026-09-03): a
    /// ticket that calls `release()` explicitly must unblock the next
    /// ticket IMMEDIATELY, without waiting for its own scope to end. Before
    /// this fix, only Drop released a ticket -- so a task that kept doing
    /// unrelated work (in production: a real, up-to-30s block-inclusion
    /// backpressure wait) after its own order-critical section serialized
    /// every later ticket behind that unrelated work too. Confirmed live:
    /// a sustained-load benchmark showed per-tx latency clustering tightly
    /// around ~65-75s, matching pipeline_semaphore's 512-permit depth times
    /// roughly one consensus round each -- i.e. batches were being forced
    /// through that 30s wait sequentially instead of in parallel.
    #[tokio::test]
    async fn explicit_release_unblocks_next_ticket_before_scope_ends() {
        let gate = Arc::new(SubmitOrderGate::new());

        let mut t0 = SubmitTicket::new(gate.clone(), 0);
        t0.wait_for_turn(Duration::from_secs(5)).await; // ticket 0 is already current
        t0.release(); // release explicitly -- t0 itself stays alive (not dropped) below

        let t1 = SubmitTicket::new(gate.clone(), 1);
        let start = std::time::Instant::now();
        t1.wait_for_turn(Duration::from_secs(5)).await;
        assert!(
            start.elapsed() < Duration::from_millis(200),
            "ticket 1 should proceed immediately once ticket 0 is explicitly released, even though ticket 0's own SubmitTicket value is still alive (not yet dropped): {:?}",
            start.elapsed()
        );

        // t0 is still in scope here, simulating the caller doing more
        // unrelated work (e.g. the real 30s backpressure wait) after
        // release() -- this must not affect anything, and Drop firing
        // later must not double-advance the gate incorrectly.
        drop(t0);
    }

    /// A ticket that's dropped WITHOUT ever calling wait_for_turn (simulating
    /// process_ffi_batch returning early -- a parse error, being queued for
    /// the next epoch, a SyncOnly forward, etc. -- before ever reaching its
    /// submission phase) must still release later tickets immediately, not
    /// starve them.
    #[tokio::test]
    async fn ticket_dropped_without_waiting_still_releases_later_tickets() {
        let gate = Arc::new(SubmitOrderGate::new());
        {
            let _t0 = SubmitTicket::new(gate.clone(), 0);
        } // dropped here, never having called wait_for_turn

        let t1 = SubmitTicket::new(gate.clone(), 1);
        let start = std::time::Instant::now();
        t1.wait_for_turn(Duration::from_secs(5)).await;
        assert!(
            start.elapsed() < Duration::from_secs(1),
            "ticket 1 should proceed almost immediately once ticket 0 is dropped, not wait out the full timeout: {:?}",
            start.elapsed()
        );
    }

    /// A ticket that's alive but never advances (simulating a genuinely
    /// stuck batch, e.g. an epoch-transition wait that never resolves) must
    /// only ever DELAY later tickets by the timeout, never block them
    /// forever -- this is the liveness safety valve that keeps this fix
    /// from introducing the same class of hang already fixed in TxRecycler.
    #[tokio::test]
    async fn stuck_ticket_times_out_instead_of_blocking_forever() {
        let gate = Arc::new(SubmitOrderGate::new());
        let _t0 = SubmitTicket::new(gate.clone(), 0); // held alive for the whole test

        let t1 = SubmitTicket::new(gate.clone(), 1);
        let start = std::time::Instant::now();
        t1.wait_for_turn(Duration::from_millis(200)).await;
        let elapsed = start.elapsed();
        assert!(
            elapsed >= Duration::from_millis(200),
            "should wait out the timeout, not skip immediately: {:?}",
            elapsed
        );
        assert!(
            elapsed < Duration::from_millis(700),
            "should proceed promptly once the timeout fires, not hang further: {:?}",
            elapsed
        );
    }
}
