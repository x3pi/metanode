// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::config::NodeConfig;
use crate::node::startup::{InitializedNode, StartupConfig};
use std::ffi::CStr;
use std::os::raw::c_char;
use std::sync::OnceLock;
use tracing::{debug, error, info, warn};

// The global callbacks registry configured from Go
pub static GO_CALLBACKS: OnceLock<GoCallbacks> = OnceLock::new();

use std::sync::atomic::{AtomicBool, Ordering};
pub static TX_TRACE_ENABLED: AtomicBool = AtomicBool::new(false);

// The global channel sender for zero-copy FFI transaction submission
pub static FFI_TX_SENDER: std::sync::RwLock<Option<tokio::sync::mpsc::Sender<Vec<u8>>>> = std::sync::RwLock::new(None);

/// READINESS SIGNAL (2026-09-09): backs the `eth_syncing` RPC method (Go side: MetaAPI.Syncing()
/// in rpc_state.go, wired via metanode_is_ready_for_transactions() below). Found live during
/// today's chaos-restart CI test: a node that just restarted answers `eth_blockNumber` (plain RPC
/// liveness) within seconds, well before ConsensusCoordinationHub reaches a phase that actually
/// accepts proposals (Healthy + RecoveryBarrier Ready/Inactive -- see
/// coordination_hub.rs's `should_skip_proposal()`, the existing authoritative check this reuses).
/// A client/test-script that only checks "does RPC answer" sends a transaction into that window
/// and gets an unexplained 45s timeout with no diagnostic -- exactly what this exists to prevent.
///
/// Set once per consensus (re)start in consensus_node.rs, right where ConsensusCoordinationHub
/// itself is constructed, via `set_global_coordination_hub` below -- so a restart (whether the
/// systemd-visible kind or the internal ffi.rs restart loop) always publishes the freshest
/// instance. Read synchronously from `metanode_is_ready_for_transactions`, an FFI call Go can make
/// directly on every `eth_syncing` request without an FFI round-trip through the async runtime.
pub static GLOBAL_COORDINATION_HUB: std::sync::RwLock<Option<consensus_core::coordination_hub::ConsensusCoordinationHub>> = std::sync::RwLock::new(None);

/// Publishes the current node's CoordinationHub for `metanode_is_ready_for_transactions` to read.
/// Called once per (re)construction -- see GLOBAL_COORDINATION_HUB's doc comment for why every
/// restart path needs this, not just the very first startup.
pub fn set_global_coordination_hub(hub: consensus_core::coordination_hub::ConsensusCoordinationHub) {
    match GLOBAL_COORDINATION_HUB.write() {
        Ok(mut guard) => *guard = Some(hub),
        Err(poisoned) => {
            // A prior panic while holding this lock (extremely unlikely -- the critical section
            // is a single pointer assignment) would poison it. Recover rather than propagate:
            // losing readiness-tracking is far preferable to this becoming a new panic source
            // in a hot restart path.
            *poisoned.into_inner() = Some(hub);
        }
    }
}

/// QUORUM-CERTIFIED PAYLOAD-LOSS SKIP (2026-09-11): a Handle to the running consensus Tokio
/// runtime, so a plain synchronous FFI call from Go's own thread (no ambient tokio context of
/// its own) can `Handle::block_on` an async task on it -- see `metanode_attest_payload_loss`.
/// Re-stashed on every (re)build of the runtime (same rationale as GLOBAL_COORDINATION_HUB's
/// doc comment) so this never points at a stale, already-shut-down runtime after a restart.
pub static GLOBAL_TOKIO_HANDLE: std::sync::RwLock<Option<tokio::runtime::Handle>> =
    std::sync::RwLock::new(None);

pub fn set_global_tokio_handle(handle: tokio::runtime::Handle) {
    match GLOBAL_TOKIO_HANDLE.write() {
        Ok(mut guard) => *guard = Some(handle),
        Err(poisoned) => *poisoned.into_inner() = Some(handle),
    }
}

/// RESUBMIT-DON'T-DISCARD (2026-09-11, per user request): a handle to the live
/// `TransactionClientProxy` (already epoch-transition-safe -- see tx_submitter.rs's own doc
/// comment, it swaps its inner client under the hood so this reference never goes stale across
/// an epoch boundary), so `block_sending.rs`'s certified-skip path can resubmit a transaction
/// this node still happens to have cached, when a quorum certificate has excluded it from ONE
/// specific commit. Kept as a bare global here (not threaded through `ExecutorClient`'s
/// constructor) for the same reason GLOBAL_COORDINATION_HUB/GLOBAL_TOKIO_HANDLE are: the
/// alternative is a new constructor parameter at every one of ExecutorClient's existing call
/// sites for a rarely-used, best-effort path. Re-stashed every time setup_consensus (first boot)
/// or mode_transition (epoch/mode change) (re)creates the proxy, same rationale as the other
/// globals here.
pub static GLOBAL_TX_RESUBMIT_CLIENT: std::sync::RwLock<
    Option<std::sync::Arc<crate::node::tx_submitter::TransactionClientProxy>>,
> = std::sync::RwLock::new(None);

pub fn set_global_tx_resubmit_client(
    client: std::sync::Arc<crate::node::tx_submitter::TransactionClientProxy>,
) {
    match GLOBAL_TX_RESUBMIT_CLIENT.write() {
        Ok(mut guard) => *guard = Some(client),
        Err(poisoned) => *poisoned.into_inner() = Some(client),
    }
}

pub fn get_global_tx_resubmit_client(
) -> Option<std::sync::Arc<crate::node::tx_submitter::TransactionClientProxy>> {
    match GLOBAL_TX_RESUBMIT_CLIENT.read() {
        Ok(guard) => guard.clone(),
        Err(poisoned) => poisoned.into_inner().clone(),
    }
}

/// PEER-BLOCK RECOVERY (2026-09-11): this node's configured `peer_rpc_addresses` (the
/// lightweight custom HTTP peer-RPC protocol used by network::peer_rpc -- a different,
/// separate transport from the tonic/gRPC NetworkClient used for DAG-level peer calls like
/// fetch_transactions/attest_payload_loss), stashed once at startup so
/// executor_client/block_sending.rs's payload-loss recovery path can reach
/// network::peer_rpc::fetch_executable_blocks_from_peer without needing this threaded through
/// every one of ExecutorClient::new's ~16 call sites. Source of truth is still
/// NodeConfig::peer_rpc_addresses (config.rs) -- this is just a process-wide read-only copy of
/// it, set once where that config is first in scope (setup_storage.rs), not re-derived.
pub static GLOBAL_PEER_RPC_ADDRESSES: std::sync::OnceLock<Vec<String>> = std::sync::OnceLock::new();

/// Idempotent: only the first call actually sets it (matches the "static config for the
/// process's lifetime" nature of peer_rpc_addresses -- unlike GLOBAL_COORDINATION_HUB/
/// GLOBAL_TOKIO_HANDLE, this never needs to change across an epoch transition or internal FFI
/// restart, so a plain OnceLock -- not a RwLock<Option<...>> -- is the right, simpler fit).
pub fn set_global_peer_rpc_addresses(addresses: Vec<String>) {
    let _ = GLOBAL_PEER_RPC_ADDRESSES.set(addresses);
}

pub fn get_global_peer_rpc_addresses() -> Vec<String> {
    GLOBAL_PEER_RPC_ADDRESSES.get().cloned().unwrap_or_default()
}

/// FFI entry point for Go's `eth_syncing` handler (MetaAPI.Syncing() in rpc_state.go).
/// Returns true when this node's consensus layer would actually accept/propose a transaction
/// right now, false otherwise (still initializing/bootstrapping/catching-up/state-syncing, or no
/// consensus instance published yet at all -- e.g. very early in process startup).
#[no_mangle]
pub extern "C" fn metanode_is_ready_for_transactions() -> bool {
    let guard = match GLOBAL_COORDINATION_HUB.read() {
        Ok(g) => g,
        Err(poisoned) => poisoned.into_inner(),
    };
    match guard.as_ref() {
        Some(hub) => !hub.should_skip_proposal(),
        None => false,
    }
}

/// Retrieves the current node's peer tx-fetcher, if one has been wired (see TxFetcherFn's doc
/// comment in coordination_hub.rs). Used by build_sorted_transactions's callers
/// (executor_client/block_sending.rs) to attempt recovering a TxPayloadCache miss from peers
/// before giving up. None before authority_node has started, or for a SyncOnly node that never
/// wires one -- callers must treat that exactly like "tried and found nothing".
pub fn get_global_tx_fetcher() -> Option<consensus_core::coordination_hub::TxFetcherFn> {
    let guard = match GLOBAL_COORDINATION_HUB.read() {
        Ok(g) => g,
        Err(poisoned) => poisoned.into_inner(),
    };
    guard.as_ref().and_then(|hub| hub.get_tx_fetcher())
}


// DIAGNOSTIC (May 2026): FFI TX submission metrics for stall diagnosis
use std::sync::atomic::{AtomicU64, Ordering as AtomicOrdering};
static FFI_TX_SUBMIT_COUNT: AtomicU64 = AtomicU64::new(0);
static FFI_TX_SUBMIT_BYTES: AtomicU64 = AtomicU64::new(0);
static FFI_TX_FULL_COUNT: AtomicU64 = AtomicU64::new(0);
static FFI_TX_LAST_LOG_SECS: AtomicU64 = AtomicU64::new(0);

pub static mut PAUSE_GUARD: Option<std::sync::RwLockWriteGuard<'static, ()>> = None;

/// Tracks whether pause is active
static PAUSE_ACTIVE: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

// Test-only fault injection: lets the regression test below deterministically trigger a real
// `panic!` from *inside* `metanode_submit_transaction_batch`'s catch_unwind closure, to prove the
// wrapper actually converts it into a safe `false` return instead of unwinding across the C-ABI
// boundary (which would abort the whole process). Compiled out entirely in non-test builds.
#[cfg(test)]
static FORCE_PANIC_FOR_TEST: AtomicBool = AtomicBool::new(false);

// Same test-only fault-injection pattern as FORCE_PANIC_FOR_TEST above, but for
// `metanode_register_callbacks` specifically -- kept as a separate flag (rather than reusing
// FORCE_PANIC_FOR_TEST) so this test can't race with
// `metanode_submit_transaction_batch_survives_internal_panic` when `cargo test` runs both
// concurrently on separate threads.
#[cfg(test)]
static FORCE_REGISTER_CALLBACKS_PANIC_FOR_TEST: AtomicBool = AtomicBool::new(false);

#[repr(C)]
pub struct GoCallbacks {
    /// Send an executable block to Go for execution.
    pub execute_block: Option<
        extern "C" fn(
            payload: *const u8,
            len: usize,
            out_payload: *mut *mut u8,
            out_len: *mut usize,
        ) -> bool,
    >,
    /// Process a generic RPC request. Takes Protobuf request bytes, returns allocated Protobuf response bytes.
    pub process_rpc_request: Option<
        extern "C" fn(
            req_payload: *const u8,
            req_len: usize,
            out_payload: *mut *mut u8,
            out_len: *mut usize,
        ) -> bool,
    >,
    /// Free a buffer previously allocated by Go (e.g., returned via out_payload).
    pub free_go_buffer: Option<extern "C" fn(ptr: *mut u8)>,
    /// Get the current state root from Go AccountStateDB
    pub get_state_root: Option<extern "C" fn() -> *mut c_char>,
    /// Update transaction trace in Go's memory
    pub update_tx_trace: Option<
        extern "C" fn(
            hash_ptr: *const u8,
            step_ptr: *const c_char,
            details_ptr: *const c_char,
        ),
    >,
    /// Log message to Go logger
    pub log_message: Option<extern "C" fn(level: std::os::raw::c_int, msg_ptr: *const c_char, msg_len: usize)>,
}

/// Call into Go to update transaction trace
pub fn update_go_tx_trace(hash: &[u8], step: &str, details: &str) {
    if !TX_TRACE_ENABLED.load(Ordering::Relaxed) {
        return;
    }
    if let Some(callbacks) = GO_CALLBACKS.get() {
        if let Some(func) = callbacks.update_tx_trace {
            let step_c = std::ffi::CString::new(step).unwrap_or_default();
            let details_c = std::ffi::CString::new(details).unwrap_or_default();
            func(hash.as_ptr(), step_c.as_ptr(), details_c.as_ptr());
        }
    }
}


/// Register the CGo callbacks.
#[no_mangle]
pub extern "C" fn metanode_register_callbacks(callbacks: GoCallbacks) {
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        #[cfg(test)]
        if FORCE_REGISTER_CALLBACKS_PANIC_FOR_TEST.load(Ordering::SeqCst) {
            panic!("forced panic for catch_unwind regression test");
        }
        if GO_CALLBACKS.set(callbacks).is_err() {
            eprintln!("Warning: metanode_register_callbacks called multiple times");
        }
    }));
    if let Err(e) = result {
        eprintln!("🚨 [RUST FFI PANIC] in metanode_register_callbacks: {:?}", e);
    }
}

/// Pulse pause to Rust consensus
/// SAFETY: Waits indefinitely until Go calls metanode_resume_consensus.
/// This prevents State Corruption if Go takes a long time to snapshot.
#[no_mangle]
pub extern "C" fn metanode_pause_consensus() {
    let result = std::panic::catch_unwind(|| {
        info!(
            "⏸️ [FFI] metanode_pause_consensus called - acquiring write lock on RUST_EXECUTION_LOCK..."
        );
        let guard = match consensus_core::storage::rocksdb_store::RUST_EXECUTION_LOCK.write() {
            Ok(g) => g,
            Err(poisoned) => {
                warn!("⚠️ [FFI] RUST_EXECUTION_LOCK was poisoned! Recovering lock to pause consensus.");
                poisoned.into_inner()
            }
        };
        unsafe {
            PAUSE_GUARD = Some(std::mem::transmute(guard));
        }
        PAUSE_ACTIVE.store(true, std::sync::atomic::Ordering::SeqCst);
        
        info!(
            "⏸️ [FFI] metanode_pause_consensus: RocksDB writes are now PAUSED indefinitely until Go resumes."
        );
    });

    if let Err(e) = result {
        eprintln!("🚨 [RUST FFI PANIC] in metanode_pause_consensus: {:?}", e);
    }
}

/// Resume Rust consensus
#[no_mangle]
pub extern "C" fn metanode_resume_consensus() {
    let result = std::panic::catch_unwind(|| {
        info!("▶️ [FFI] metanode_resume_consensus called - dropping write lock...");
        PAUSE_ACTIVE.store(false, std::sync::atomic::Ordering::SeqCst);
        unsafe {
            PAUSE_GUARD = None;
        }
        info!("▶️ [FFI] metanode_resume_consensus: RocksDB writes RESUMED.");
    });

    if let Err(e) = result {
        eprintln!("🚨 [RUST FFI PANIC] in metanode_resume_consensus: {:?}", e);
    }
}

/// Call into Go to get the exact final StateRoot
pub fn get_go_state_root() -> String {
    if let Some(callbacks) = GO_CALLBACKS.get() {
        if let Some(func) = callbacks.get_state_root {
            let ptr = func();
            if !ptr.is_null() {
                let s = unsafe { CStr::from_ptr(ptr).to_string_lossy().into_owned() };
                if let Some(free_func) = callbacks.free_go_buffer {
                    free_func(ptr as *mut u8);
                }
                return s;
            }
        }
    }
    String::new()
}

/// QUORUM-CERTIFIED PAYLOAD-LOSS SKIP (2026-09-11): operator-triggered entry point. NOT called
/// automatically from anywhere -- an operator invokes this (via whatever thin CLI/admin wrapper
/// calls into this FFI boundary) only after `CONSENSUS-HALT-TX-PAYLOAD-LOST` (block_delivery.rs
/// mục 10) has been showing for this exact commit for a genuinely long time, per mục 11.2
/// point 6 of note/consensus_local_dag_trust_gap_design_2026-09.md -- never on a short
/// automatic timeout, since that risks treating a transient network partition as confirmed
/// permanent loss.
///
/// `tx_digest_hex` must be exactly `2 * DIGEST_LENGTH` hex characters (no `0x` prefix).
/// Returns:
///   0 = quorum-certified as permanently lost; the certified-skip list has been updated, and
///       the stuck delivery retry loop (block_delivery.rs) will pick this up and proceed on
///       its next attempt (within ~10s) without needing anything else from the operator.
///   1 = recovered instead -- a peer (or this node itself) actually still had the payload; it
///       has been inserted into the local TxPayloadCache, no skip was needed or applied.
///   2 = insufficient stake attested so far to reach quorum -- see the log line this prints
///       for exactly how much stake responded "confirmed missing" vs. how much is needed; the
///       operator may re-run this later once more peers are reachable, or investigate why they
///       aren't.
///  -1 = could not run at all (bad input, or this node's consensus isn't up yet -- e.g. no
///       committee/network client wired, same precondition TxFetcherFn already documents).
#[no_mangle]
pub extern "C" fn metanode_attest_payload_loss(
    commit_index: u32,
    tx_digest_hex: *const std::os::raw::c_char,
) -> i32 {
    if tx_digest_hex.is_null() {
        error!("🛑 [PAYLOAD-LOSS-SKIP] metanode_attest_payload_loss: null tx_digest_hex");
        return -1;
    }
    let hex_str = match unsafe { std::ffi::CStr::from_ptr(tx_digest_hex) }.to_str() {
        Ok(s) => s,
        Err(_) => {
            error!("🛑 [PAYLOAD-LOSS-SKIP] tx_digest_hex is not valid UTF-8");
            return -1;
        }
    };
    let digest_bytes = match hex::decode(hex_str) {
        Ok(b) if b.len() == consensus_config::DIGEST_LENGTH => b,
        Ok(b) => {
            error!(
                "🛑 [PAYLOAD-LOSS-SKIP] tx_digest_hex has {} bytes, expected {}",
                b.len(), consensus_config::DIGEST_LENGTH
            );
            return -1;
        }
        Err(e) => {
            error!("🛑 [PAYLOAD-LOSS-SKIP] tx_digest_hex is not valid hex: {}", e);
            return -1;
        }
    };
    let mut digest_arr = [0u8; consensus_config::DIGEST_LENGTH];
    digest_arr.copy_from_slice(&digest_bytes);
    let tx_digest = consensus_types::block::TxDigest(digest_arr);

    let collector = {
        let guard = match GLOBAL_COORDINATION_HUB.read() {
            Ok(g) => g,
            Err(poisoned) => poisoned.into_inner(),
        };
        guard.as_ref().and_then(|hub| hub.get_payload_loss_collector())
    };
    let Some(collector) = collector else {
        error!(
            "🛑 [PAYLOAD-LOSS-SKIP] No payload-loss collector wired yet -- consensus may not be \
             fully started. Try again shortly."
        );
        return -1;
    };
    let committee = {
        let guard = match GLOBAL_COORDINATION_HUB.read() {
            Ok(g) => g,
            Err(poisoned) => poisoned.into_inner(),
        };
        guard
            .as_ref()
            .and_then(|hub| hub.get_committee_for_payload_loss())
    };
    let Some(committee) = committee else {
        error!("🛑 [PAYLOAD-LOSS-SKIP] No committee available yet -- consensus may not be fully started.");
        return -1;
    };

    let handle = {
        let guard = match GLOBAL_TOKIO_HANDLE.read() {
            Ok(g) => g,
            Err(poisoned) => poisoned.into_inner(),
        };
        guard.clone()
    };
    let Some(handle) = handle else {
        error!("🛑 [PAYLOAD-LOSS-SKIP] No tokio runtime handle available yet.");
        return -1;
    };

    let claim = consensus_core::payload_loss_attestation::PayloadLossClaim {
        commit_index,
        tx_digest,
    };
    info!(
        "🔎 [PAYLOAD-LOSS-SKIP] Operator-triggered attestation collection starting for \
         commit_index={} tx_digest=0x{}...",
        commit_index, hex_str
    );
    let result = handle.block_on(collector(claim.clone(), std::time::Duration::from_secs(10)));
    apply_collection_result(result, &committee, commit_index, hex_str)
}

/// Shared by `metanode_attest_payload_loss` and `metanode_attest_payload_loss_for_commit`:
/// turns one already-awaited `PayloadLossCollectionResult` into the documented 0/1/2/-1 return
/// code, doing the certificate re-verification + recording + logging either function needs.
fn apply_collection_result(
    result: consensus_core::payload_loss_attestation::PayloadLossCollectionResult,
    committee: &consensus_config::Committee,
    commit_index: u32,
    hex_str: &str,
) -> i32 {
    use consensus_core::payload_loss_attestation::PayloadLossCollectionResult;
    match result {
        PayloadLossCollectionResult::Recovered => {
            info!(
                "✅ [PAYLOAD-LOSS-SKIP] Recovered -- a peer (or this node) actually had the \
                 payload for commit_index={} tx_digest=0x{}, no skip needed.",
                commit_index, hex_str
            );
            1
        }
        PayloadLossCollectionResult::Certified(certificate) => {
            if let Err(e) = certificate.verify(committee) {
                error!(
                    "🛑 [PAYLOAD-LOSS-SKIP] BUG: collector returned a certificate that fails its \
                     own re-verification: {}. NOT applying it.",
                    e
                );
                return -1;
            }
            consensus_core::payload_loss_attestation::record_certified_skip(certificate);
            info!(
                "🛑✅ [PAYLOAD-LOSS-SKIP-CERTIFIED] Quorum-certified permanent loss for \
                 commit_index={} tx_digest=0x{} -- recorded. The stuck delivery retry loop will \
                 pick this up and skip this transaction within ~10s.",
                commit_index, hex_str
            );
            0
        }
        PayloadLossCollectionResult::Insufficient { attested_missing_stake, quorum_needed } => {
            error!(
                "⏳ [PAYLOAD-LOSS-SKIP] Not enough stake attested yet for commit_index={} \
                 tx_digest=0x{}: {} of {} needed. Re-run later once more peers are reachable.",
                commit_index, hex_str, attested_missing_stake, quorum_needed
            );
            2
        }
    }
}

/// QUORUM-CERTIFIED PAYLOAD-LOSS SKIP, whole-commit convenience wrapper (2026-09-11): a single
/// commit's subdag can reference blocks from SEVERAL different authors, each carrying its own
/// transactions -- so more than one digest can be missing at once for the same halted commit
/// (reproduced live: a commit spanning 2 authors' blocks had 8 distinct missing digests). Before
/// this, an operator had to grep CONSENSUS-HALT-TX-PAYLOAD-LOST's log line for the ONE digest
/// name it prints (the first missing one `build_sorted_transactions` happens to hit), call
/// `metanode_attest_payload_loss` for it, then discover from the retry loop STILL not resuming
/// that there were more, repeating one at a time -- exactly the tedious, error-prone manual hunt
/// the user asked to have handled automatically instead.
///
/// This reuses `STUCK_CLAIMS` (payload_loss_attestation.rs) -- the SAME registry
/// `deliver_with_halt_retry` already populates with every digest it has found missing for a
/// commit, and that peer attestation itself already relies on for fork-safety -- to discover the
/// full set in one call, then certifies each one exactly as `metanode_attest_payload_loss` would.
/// No new tracking was needed; this only adds a way to enumerate and drive what the system
/// already knows.
///
/// Returns:
///   0  = every claim currently stuck on this node for this commit is now resolved (certified
///        skip and/or recovered) -- the retry loop should proceed within ~10s.
///   1  = nothing was stuck for this commit on this node right now (already resolved by the
///        time this ran, or this node was never stuck on it in the first place).
///   2  = at least one claim still has insufficient attested stake -- re-run once more peers are
///        reachable; claims that DID succeed this run are still recorded, only the remaining
///        ones need a retry.
///  -1  = could not run at all (same preconditions as `metanode_attest_payload_loss`), or at
///        least one claim hit a hard error (e.g. a certificate that failed its own
///        re-verification -- see the log for which).
#[no_mangle]
pub extern "C" fn metanode_attest_payload_loss_for_commit(commit_index: u32) -> i32 {
    let collector = {
        let guard = match GLOBAL_COORDINATION_HUB.read() {
            Ok(g) => g,
            Err(poisoned) => poisoned.into_inner(),
        };
        guard.as_ref().and_then(|hub| hub.get_payload_loss_collector())
    };
    let Some(collector) = collector else {
        error!(
            "🛑 [PAYLOAD-LOSS-SKIP] No payload-loss collector wired yet -- consensus may not be \
             fully started. Try again shortly."
        );
        return -1;
    };
    let committee = {
        let guard = match GLOBAL_COORDINATION_HUB.read() {
            Ok(g) => g,
            Err(poisoned) => poisoned.into_inner(),
        };
        guard
            .as_ref()
            .and_then(|hub| hub.get_committee_for_payload_loss())
    };
    let Some(committee) = committee else {
        error!("🛑 [PAYLOAD-LOSS-SKIP] No committee available yet -- consensus may not be fully started.");
        return -1;
    };
    let handle = {
        let guard = match GLOBAL_TOKIO_HANDLE.read() {
            Ok(g) => g,
            Err(poisoned) => poisoned.into_inner(),
        };
        guard.clone()
    };
    let Some(handle) = handle else {
        error!("🛑 [PAYLOAD-LOSS-SKIP] No tokio runtime handle available yet.");
        return -1;
    };

    let claims = consensus_core::payload_loss_attestation::stuck_claims_for_commit(commit_index);
    if claims.is_empty() {
        info!(
            "✅ [PAYLOAD-LOSS-SKIP] No claims currently stuck for commit_index={} on this node \
             -- already resolved, or this node was never stuck on it.",
            commit_index
        );
        return 1;
    }
    info!(
        "🔎 [PAYLOAD-LOSS-SKIP] Found {} claim(s) currently stuck for commit_index={} -- \
         attesting every one in this single admin action.",
        claims.len(), commit_index
    );

    let codes: Vec<i32> = handle.block_on(async {
        let mut codes = Vec::with_capacity(claims.len());
        for claim in claims {
            let hex_str = hex::encode(claim.tx_digest.0);
            let result = collector(claim.clone(), std::time::Duration::from_secs(10)).await;
            codes.push(apply_collection_result(result, &committee, commit_index, &hex_str));
        }
        codes
    });

    if codes.iter().any(|&c| c == -1) {
        error!(
            "🛑 [PAYLOAD-LOSS-SKIP] commit_index={}: at least one claim hit a hard error -- see \
             the log lines above for which digest(s). Re-run once resolved.",
            commit_index
        );
        -1
    } else if codes.iter().any(|&c| c == 2) {
        error!(
            "⏳ [PAYLOAD-LOSS-SKIP] commit_index={}: {} of {} claim(s) still need more attested \
             stake -- the rest were resolved and recorded. Re-run once more peers are reachable.",
            commit_index,
            codes.iter().filter(|&&c| c == 2).count(),
            codes.len()
        );
        2
    } else {
        info!(
            "✅ [PAYLOAD-LOSS-SKIP] commit_index={}: all {} claim(s) resolved. The retry loop \
             should proceed within ~10s.",
            commit_index, codes.len()
        );
        0
    }
}

/// Returns the current number of items in the FFI TX queue.
pub fn get_ffi_tx_queue_depth() -> usize {
    if let Ok(guard) = FFI_TX_SENDER.read() {
        if let Some(sender) = guard.as_ref() {
            return sender.max_capacity() - sender.capacity();
        }
    }
    0
}

/// Initialize RocksDB C++ static variables safely.
/// This should be called from Go's main thread on startup.
#[no_mangle]
pub extern "C" fn metanode_init_rocksdb(data_dir: *const std::os::raw::c_char) {
    if data_dir.is_null() { return; }
    let c_str = unsafe { std::ffi::CStr::from_ptr(data_dir) };
    if let Ok(dir) = c_str.to_str() {
        let path = format!("{}/rocksdb_dummy_init", dir);
        let _ = std::panic::catch_unwind(|| {
            let _ = consensus_core::storage::rocksdb_store::RocksDBStore::new(&path);
        });
    }
}

pub fn setup_ffi_transaction_channel(sender: tokio::sync::mpsc::Sender<Vec<u8>>) {
    // Acquire the lock and update the channel
    if let Ok(mut guard) = FFI_TX_SENDER.write() {
        *guard = Some(sender);
    } else {
        error!("❌ [FFI SETUP] Failed to acquire FFI_TX_SENDER lock for setup!");
        return;
    }
}

/// Directly submit a transaction batch from Go mempool to Rust consensus over FFI
#[no_mangle]
pub unsafe extern "C" fn metanode_submit_transaction_batch(payload: *const u8, len: usize) -> bool {
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        if payload.is_null() || len == 0 {
            return true; // Ignore empty payload safely
        }

        let tx_data = std::slice::from_raw_parts(payload, len).to_vec();

        #[cfg(test)]
        if FORCE_PANIC_FOR_TEST.load(Ordering::SeqCst) {
            panic!("forced panic for FFI panic-safety regression test");
        }

        // Wait for the channel to be initialized (blocks Go caller until Rust is ready, with 5s timeout)
        let mut sender_opt = None;
        for _ in 0..100 {
            if let Ok(guard) = FFI_TX_SENDER.read() {
                if let Some(ref s) = *guard {
                    sender_opt = Some(s.clone());
                    break;
                }
            }
            std::thread::sleep(std::time::Duration::from_millis(50));
        }
        
        let sender = match sender_opt {
            Some(s) => s,
            None => {
                tracing::warn!("⚠️ [FFI TX FLOW] Timeout waiting for FFI_TX_SENDER to be initialized! Returning false.");
                return false;
            }
        };

        // Instrumentation: Queue Saturation metrics
        let remaining_capacity = sender.capacity();
        if remaining_capacity < 1000 {
            tracing::warn!(
                "⚠️ [FFI TX FLOW] Rust FFI channel is highly saturated! Capacity remaining: {}/10000",
                remaining_capacity
            );
        }

        // try_send is non-blocking and synchronous
        let batch_size = tx_data.len();
        match sender.try_send(tx_data) {
            Ok(_) => {
                FFI_TX_SUBMIT_COUNT.fetch_add(1, AtomicOrdering::Relaxed);
                FFI_TX_SUBMIT_BYTES.fetch_add(batch_size as u64, AtomicOrdering::Relaxed);

                // DIAGNOSTIC: Periodic summary every 5s
                let current_secs = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs();
                let last_log = FFI_TX_LAST_LOG_SECS.load(AtomicOrdering::Relaxed);
                let should_log = if current_secs >= last_log + 5 {
                    if FFI_TX_LAST_LOG_SECS.compare_exchange(
                        last_log,
                        current_secs,
                        AtomicOrdering::Relaxed,
                        AtomicOrdering::Relaxed
                    ).is_ok() {
                        true
                    } else {
                        false
                    }
                } else {
                    false
                };
                if should_log {
                    let total_batches = FFI_TX_SUBMIT_COUNT.load(AtomicOrdering::Relaxed);
                    let total_bytes = FFI_TX_SUBMIT_BYTES.load(AtomicOrdering::Relaxed);
                    let full_count = FFI_TX_FULL_COUNT.load(AtomicOrdering::Relaxed);
                    info!(
                        "📊 [FFI TX METRICS] total_batches={}, total_bytes={}, channel_full_events={}, \
                         capacity_remaining={}/10000",
                        total_batches, total_bytes, full_count, remaining_capacity
                    );
                }

                debug!("📨 [TX-FLOW-TRACE] ▶ PHASE 1: Go→Rust FFI entry | batch_size={} bytes | channel_status=accepted", batch_size);
                true
            }
            Err(tokio::sync::mpsc::error::TrySendError::Full(_)) => {
                // Channel is full. Go side will see `false` and automatically sleep/retry.
                FFI_TX_FULL_COUNT.fetch_add(1, AtomicOrdering::Relaxed);
                warn!(
                    "⚠️ [FFI TX FLOW] Channel FULL! Go will retry. full_events_total={}",
                    FFI_TX_FULL_COUNT.load(AtomicOrdering::Relaxed)
                );
                false
            }
            Err(_) => {
                error!("❌ [FFI TX FLOW] Failed to send to FFI channel (channel may be closed due to restart)");

                // Channel closed. Reset sender to None so the next call blocks on the spin loop until a new channel is registered.
                if let Ok(mut guard) = FFI_TX_SENDER.write() {
                    *guard = None;
                }
                false
            }
        }
    }));

    match result {
        Ok(v) => v,
        Err(e) => {
            eprintln!("🚨 [RUST FFI PANIC] in metanode_submit_transaction_batch: {:?}", e);
            false
        }
    }
}

/// Start the Rust consensus engine in a background thread.
#[no_mangle]
pub unsafe extern "C" fn metanode_start_consensus(
    config_path_ptr: *const c_char,
    data_dir_ptr: *const c_char,
) {
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let config_path_str = if config_path_ptr.is_null() {
            eprintln!("Error: config_path_ptr is null");
            return;
        } else {
            CStr::from_ptr(config_path_ptr)
                .to_string_lossy()
                .into_owned()
        };

        let data_dir_str = if data_dir_ptr.is_null() {
            "".to_string()
        } else {
            CStr::from_ptr(data_dir_ptr).to_string_lossy().into_owned()
        };

        println!(
            "Starting MetaNode Consensus Engine via CGo FFI. Config: {}",
            config_path_str
        );

        // We must spawn a new OS thread to run Tokio, because the caller is Go's C-thread
        std::thread::spawn(move || {
        // Install panic hook for diagnostic output BEFORE any Rust code runs
        std::panic::set_hook(Box::new(|info| {
            eprintln!("🚨 [RUST PANIC] {}", info);
            eprintln!(
                "Backtrace:\n{:?}",
                std::backtrace::Backtrace::force_capture()
            );
        }));


/// Custom writer that forwards Rust tracing logs to Go logger via CGo callback with dynamic log levels
struct GoLogWriter {
    level: i32,
}

impl std::io::Write for GoLogWriter {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        let msg = String::from_utf8_lossy(buf);
        let trimmed = msg.trim_end();
        if !trimmed.is_empty() {
            if let Some(callbacks) = GO_CALLBACKS.get() {
                if let Some(log_func) = callbacks.log_message {
                    if let Ok(c_msg) = std::ffi::CString::new(trimmed) {
                        log_func(self.level, c_msg.as_ptr(), trimmed.len());
                    }
                }
            }
        }
        Ok(buf.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

struct GoLogMakeWriter;

impl<'a> tracing_subscriber::fmt::MakeWriter<'a> for GoLogMakeWriter {
    type Writer = GoLogWriter;

    fn make_writer(&'a self) -> Self::Writer {
        GoLogWriter { level: 1 } // Default to Info
    }

    fn make_writer_for(&'a self, meta: &tracing::Metadata<'_>) -> Self::Writer {
        let level = match *meta.level() {
            tracing::Level::ERROR => 3,
            tracing::Level::WARN => 2,
            tracing::Level::INFO => 1,
            tracing::Level::DEBUG | tracing::Level::TRACE => 0,
        };
        GoLogWriter { level }
    }
}
        // Initialize tracing for FFI thread (captured into execution.log via Go callback)
        let _ = tracing_subscriber::fmt()
            .with_ansi(false)
            .with_writer(GoLogMakeWriter)
            .with_env_filter(
                tracing_subscriber::EnvFilter::try_from_default_env()
                    .unwrap_or_else(|_| "info".into()),
            )
            .try_init();

        info!("Starting MetaNode Consensus Engine (FFI Thread)...");

        // OUTER RESILIENCE LOOP (2026-09-09): found live while verifying the
        // PERMANENT-GAP-RECOVERY amnesty fix on a real 4-node cluster restart.
        //
        // The catch_unwind below only ever ran ONCE. Its handler (see the bottom of this
        // loop body) just eprintln!'d and returned -- it did not retry. That is fine ONLY for
        // panics that originate from inside the async `loop` further down AND are already
        // turned into a `Result::Err` that loop explicitly matches on (NodeConfig::load,
        // InitializedNode::initialize, run_main_loop) -- those three sites already have their
        // own restart_count/backoff and `continue`. But a raw, UNCAUGHT panic anywhere else in
        // the entire initialize()/run_main_loop() call graph -- for example the typed-store-
        // derive retry loop's own `.expect("Cannot open DB at {:?}")` once it exhausts its 60
        // attempts (see typed-store-derive/src/lib.rs), which is exactly what was observed
        // live here -- unwinds straight past all of that inner handling and is caught only by
        // this OUTER catch_unwind, which used to just log and give up. Confirmed live: node-0
        // sat with Rust consensus permanently dead (Go kept running, RPC kept answering reads,
        // block height frozen) for 4+ minutes with zero further restart attempts, while
        // systemd saw one continuous, never-crashed OS process the whole time -- so nothing
        // external ever notices either. This is the same class of gap as the FFI-internal
        // restart loop already being invisible to systemd (see the backoff comment inside the
        // loop below), just one level further out: ANY panic anywhere in this whole subsystem
        // used to be a one-way trip to a silent, permanent, human-restart-required outage --
        // precisely the scenario a full-cluster restart needs to NOT be true.
        //
        // Fix: move the retry loop out here too, with the same growing backoff. A panic now
        // rebuilds a fresh Tokio runtime (cheap, and already what every iteration of the inner
        // loop implicitly relies on via "fresh Registry each loop") and tries again, instead of
        // ending the process's Rust consensus life for good.
        let mut outer_restart_count: u32 = 0;
        loop {
        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            // Build the Tokio multi-threaded runtime.
            //
            // CPU co-location: when multiple node processes share one machine
            // (e.g. this repo's single-box test rigs), Tokio's default of
            // spawning num_cpus::get() worker threads per process means N
            // co-located nodes collectively oversubscribe the shared cores up
            // to Nx — independently of and in addition to Go's GOMAXPROCS,
            // since this runtime is a separate thread pool from the Go
            // scheduler even though both live in the same OS process via FFI.
            // Reuse the GOMAXPROCS env var (already set per-node in the
            // systemd unit for exactly this reason) as the worker-thread cap
            // here too, so operators only need to tune one knob per node.
            // Unset (typical single-node-per-machine deployments): Tokio's
            // own num_cpus() default is used, unchanged.
            let mut builder = tokio::runtime::Builder::new_multi_thread();
            if let Some(n) = std::env::var("GOMAXPROCS")
                .ok()
                .and_then(|v| v.parse::<usize>().ok())
                .filter(|&n| n > 0)
            {
                builder.worker_threads(n);
                // Tokio's spawn_blocking pool (used by dag_state/write.rs,
                // commit_observer.rs, commit_finalizer, etc. — 11 call sites
                // in this codebase) is a SEPARATE pool from worker_threads
                // and defaults to up to 512 threads regardless of the cap
                // above. Under a co-located-node burst this pool can spin up
                // hundreds of short-lived OS threads on top of the already
                // fair-shared worker threads, which is consistent with the
                // 560 total OS threads observed at idle on this box (vs. the
                // ~40 expected from worker_threads=20 + Go's GOMAXPROCS=20).
                // Cap it to the same fair share.
                builder.max_blocking_threads(n);
            }
            let rt = match builder.enable_all().build() {
                Ok(rt) => rt,
                Err(e) => {
                    error!("Failed to create tokio runtime: {}", e);
                    return;
                }
            };
            // QUORUM-CERTIFIED PAYLOAD-LOSS SKIP (2026-09-11): stash a Handle to this runtime
            // so metanode_attest_payload_loss (a plain sync FFI call from Go's own thread, no
            // ambient tokio context of its own) can `Handle::block_on` the async collector.
            // Re-stashed on every (re)build of this runtime, same rationale as
            // set_global_coordination_hub's doc comment.
            crate::ffi::set_global_tokio_handle(rt.handle().clone());

            rt.block_on(async {
                let mut restart_count = 0u32;

                loop {
                    // Create a fresh Registry each loop to avoid Prometheus AlreadyReg panics
                    let registry = prometheus::Registry::new();

                    let config_path = std::path::PathBuf::from(config_path_str.clone());
                    let mut node_config = match NodeConfig::load(&config_path) {
                        Ok(c) => c,
                        Err(e) => {
                             error!("Failed to load configuration from {:?}: {}", config_path, e);
                             tokio::time::sleep(tokio::time::Duration::from_secs(5)).await;
                             continue;
                        }
                    };

                    TX_TRACE_ENABLED.store(node_config.tx_trace_enabled, Ordering::SeqCst);

                    // Override storage path to live inside Go's data directory if provided
                    if !data_dir_str.is_empty() {
                        // Override storage path for MystiCeti DAG storage
                        node_config.storage_path = std::path::PathBuf::from(&data_dir_str)
                            .join("consensus")
                            .join("rust_consensus");
                        info!(
                            "Storage path unified to Go data dir: {:?}",
                            node_config.storage_path
                        );
                    }

                    info!("Node ID: {}", node_config.node_id);
                    info!("Network address: {}", node_config.network_address);

                    if restart_count > 0 {
                        // BACKOFF (2026-09-09): was a flat 10s. Root-caused live (4-node
                        // cluster, all restarting simultaneously after each hit the
                        // TxPayloadCache-miss panic added in c915437d) a real
                        // "IO error: lock hold by current process ... LOCK: No locks
                        // available" panic (typed_store's macro-generated RocksDBStore::new,
                        // rocksdb_store.rs:31) on the FIRST 3-4 restart attempts, every time --
                        // the previous iteration's Tokio runtime (and whatever RocksDB
                        // background compaction/flush threads it owned) hadn't finished
                        // releasing the on-disk LOCK file by the time this same OS process
                        // tried to reopen the same path again. A flat 10s wait was
                        // consistently NOT enough; it took ~4 attempts (this same 10s+5s
                        // "FFI RESTART COOLDOWN" pair below, back to back) for the cluster to
                        // self-clear, which is a real ~60s+ of wasted, guaranteed-to-fail
                        // retries every single time this path triggers. Growing the wait with
                        // each consecutive failed attempt (capped, not unbounded) gives the
                        // still-draining resources more realistic time instead of hammering
                        // the same conflict on a fixed cadence.
                        let backoff_secs = (10 * restart_count.min(6)).min(60) as u64;
                        info!(
                            "🔄 [FFI RESTART] Attempt #{} — previous instance crashed. \
                             Waiting {}s for old connections/tasks (and any still-draining \
                             RocksDB handles) to fully release...",
                            restart_count, backoff_secs
                        );
                        // Extended delay: old TCP connections (consensus P2P, gRPC) need
                        // TIME_WAIT to expire. 5s was too aggressive — peers still had
                        // open connections to old ports, causing bind/connect failures.
                        tokio::time::sleep(tokio::time::Duration::from_secs(backoff_secs)).await;
                    }
                    let startup_config = StartupConfig::new(node_config, registry, None);

                    let initialized_node = match InitializedNode::initialize(startup_config).await {
                        Ok(node) => node,
                        Err(e) => {
                            error!("Failed to initialize node: {}", e);
                            restart_count += 1;
                            tokio::time::sleep(tokio::time::Duration::from_secs(5)).await;
                            continue;
                        }
                    };

                    let run_result = initialized_node.run_main_loop().await;
                    if let Err(e) = &run_result {
                        error!("Consensus main loop exited with error: {}", e);
                    }

                    // FFI RESTART COOLDOWN: Wait for old thread/tasks to release database locks
                    tokio::time::sleep(tokio::time::Duration::from_secs(5)).await;
                    restart_count += 1;
                    tracing::warn!(
                        "🔄 [FFI RESTART] Consensus Node crashed (restart #{}). \
                         All authority tasks will be dropped. Restarting...",
                        restart_count
                    );
                }
            });
        }));

        match result {
            Ok(()) => {
                // The async `loop` above has no `break` in it (every path either `continue`s
                // or falls through to loop again after a restart_count bump), so reaching here
                // without a panic is not expected in practice. Break rather than tight-looping
                // forever with no backoff if it ever does happen.
                eprintln!(
                    "🚨 [RUST FFI] Consensus engine's inner loop exited without panicking \
                     (unexpected -- it has no break). Not restarting again to avoid a tight \
                     loop; this OS thread is now idle."
                );
                break;
            }
            Err(e) => {
                outer_restart_count += 1;

                // GIVE-UP-AND-LET-SYSTEMD-RESTART (2026-09-10): found live, same cluster, same
                // day, verifying the DIGEST-GATE/PERMANENT-GAP-RECOVERY work above -- node-0 hit
                // the exact RocksDB "lock hold by current process" panic this loop already knows
                // about, but this time the SAME-PROCESS in-place recovery (rebuild Tokio runtime,
                // retry) never worked at all: 6 outer attempts, ~6+ minutes, growing backoff
                // maxed out at 60s each -- zero progress. Only an actual `systemctl restart`
                // (killing the whole OS process, not just this Tokio runtime) fixed it. Root
                // cause not fully confirmed (plausible: a raw OS thread from an earlier attempt,
                // outside Tokio's own tracked pool, stuck holding the on-disk LOCK file forever,
                // which rebuilding the runtime here has no way to reach or terminate) -- but the
                // empirical fix is unambiguous: rebuilding-in-place has a real, observed ceiling
                // past which it never recovers, while a real process restart reliably does.
                //
                // Fix: past a bounded number of in-place attempts, stop trying to self-heal
                // inside this process and exit cleanly instead, letting systemd's own
                // `Restart=on-failure` (RestartSec=15s, see metanode-execution.service.j2) do a
                // real process restart -- which this session confirmed actually works, unlike
                // continuing to loop here. `std::process::exit`, not a panic/abort: a controlled,
                // intentional exit, not an uncontrolled unwind through Go's side of this same OS
                // process. If the SAME problem recurs across multiple real restarts in a row,
                // systemd's own circuit breaker (StartLimitBurst=5 / StartLimitIntervalSec=300)
                // takes over and leaves the unit stopped rather than restart-looping forever --
                // at that point this needs a human, which is the correct outcome for a problem
                // that survives an actual process restart, not something this loop should keep
                // guessing at indefinitely.
                const MAX_IN_PROCESS_RESTARTS: u32 = 3;
                if outer_restart_count >= MAX_IN_PROCESS_RESTARTS {
                    eprintln!(
                        "🚨🚨🚨 [RUST FFI] Consensus engine panicked {} times in this process \
                         with zero progress (latest: {:?}). Giving up on in-process recovery -- \
                         exiting so systemd restarts the whole process fresh (Restart=on-failure, \
                         15s). If this keeps recurring across real restarts, systemd's own \
                         StartLimitBurst will stop the unit and this needs an operator.",
                        outer_restart_count, e
                    );
                    std::process::exit(1);
                }

                // Same growing-backoff shape as the inner loop's own FFI RESTART backoff (see
                // its comment) -- reusing the reasoning: a flat/short wait was consistently not
                // enough for a same-process RocksDB LOCK file (and whatever else was mid-
                // teardown) to actually finish releasing before the next attempt.
                let backoff_secs = (10 * outer_restart_count.min(6)).min(60) as u64;
                eprintln!(
                    "🚨 [RUST FFI] Consensus engine panicked (outer restart #{}/{}): {:?}. \
                     Rebuilding the Tokio runtime and retrying in {}s instead of leaving Rust \
                     consensus permanently dead for the rest of this process's life.",
                    outer_restart_count, MAX_IN_PROCESS_RESTARTS, e, backoff_secs
                );
                // DO NOT re-panic — that would abort() the Go process.
                // Blocking sleep is fine here: the Tokio runtime that panicked has already been
                // torn down (catch_unwind unwound out of it), so this OS thread has nothing
                // else to service right now.
                std::thread::sleep(std::time::Duration::from_secs(backoff_secs));
            }
        }
        }
    });
    }));

    if let Err(e) = result {
        eprintln!("🚨 [RUST FFI PANIC] in metanode_start_consensus: {:?}", e);
    }
}

fn copy_dir_all(
    src: impl AsRef<std::path::Path>,
    dst: impl AsRef<std::path::Path>,
) -> std::io::Result<()> {
    std::fs::create_dir_all(&dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let ty = entry.file_type()?;
        if ty.is_dir() {
            copy_dir_all(entry.path(), dst.as_ref().join(entry.file_name()))?;
        } else {
            std::fs::copy(entry.path(), dst.as_ref().join(entry.file_name()))?;
        }
    }
    Ok(())
}

/// Restore Rust consensus state from a snapshot directory.
/// Purges data_dir/consensus/rust_consensus and copies snapshot_dir/consensus/rust_consensus into it safely.
#[no_mangle]
pub unsafe extern "C" fn metanode_restore_from_snapshot(
    data_dir_ptr: *const c_char,
    snapshot_dir_ptr: *const c_char,
) -> bool {
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let data_dir_str = unsafe {
            if data_dir_ptr.is_null() {
                return false;
            }
            CStr::from_ptr(data_dir_ptr).to_string_lossy().into_owned()
        };

        let snapshot_dir_str = unsafe {
            if snapshot_dir_ptr.is_null() {
                return false;
            }
            CStr::from_ptr(snapshot_dir_ptr)
                .to_string_lossy()
                .into_owned()
        };

        // Target directory (the working consensus directory that needs to be replaced)
        let target_dir = std::path::PathBuf::from(&data_dir_str)
            .join("consensus")
            .join("rust_consensus");
        let source_dir = std::path::PathBuf::from(&snapshot_dir_str)
            .join("consensus")
            .join("rust_consensus");

        if !source_dir.exists() {
            error!(
                "[FFI Restore] Snapshot source dir not found: {:?}",
                source_dir
            );
            return false;
        }

        info!(
            "[FFI Restore] Restoring DAG from snapshot: {:?} -> {:?}",
            source_dir, target_dir
        );

        if target_dir.exists() {
            if let Err(e) = std::fs::remove_dir_all(&target_dir) {
                error!(
                    "[FFI Restore] Failed to remove old target dir {:?}: {}",
                    target_dir, e
                );
                return false;
            }
        }

        if let Err(e) = copy_dir_all(&source_dir, &target_dir) {
            error!(
                "[FFI Restore] Failed to copy snapshot files to target: {}",
                e
            );
            return false;
        }

        info!("[FFI Restore] Successfully restored rust_consensus!");
        true
    }));

    match result {
        Ok(v) => v,
        Err(e) => {
            eprintln!("🚨 [RUST FFI PANIC] in metanode_restore_from_snapshot: {:?}", e);
            false
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // Null/zero-length payload must stay a safe no-op (returns true) regardless of the
    // forced-panic flag below -- it's checked before that flag is ever read, so this test is
    // safe to run in parallel with `metanode_submit_transaction_batch_survives_internal_panic`.
    #[test]
    fn metanode_submit_transaction_batch_null_payload_is_noop() {
        let result = unsafe { metanode_submit_transaction_batch(std::ptr::null(), 0) };
        assert!(result, "null payload must be treated as a safe no-op");

        let payload = [0u8; 4];
        let result = unsafe { metanode_submit_transaction_batch(payload.as_ptr(), 0) };
        assert!(result, "zero-length payload must be treated as a safe no-op");
    }

    // Regression test for the catch_unwind wrapping added around
    // `metanode_submit_transaction_batch`: a real Rust panic triggered from inside the FFI call
    // must be caught and surfaced as the documented safe fallback (`false`), never unwind across
    // the C-ABI boundary. Since this function is `extern "C"` (non-unwind ABI), an uncaught panic
    // here would abort the whole test process rather than fail this assertion -- so a passing
    // `cargo test` run on this test is itself part of the proof that the wrapper works.
    #[test]
    fn metanode_submit_transaction_batch_survives_internal_panic() {
        FORCE_PANIC_FOR_TEST.store(true, Ordering::SeqCst);
        let payload = [0u8; 4];
        let result = unsafe { metanode_submit_transaction_batch(payload.as_ptr(), payload.len()) };
        FORCE_PANIC_FOR_TEST.store(false, Ordering::SeqCst);

        assert!(
            !result,
            "a panic inside the wrapped FFI fn must be caught and reported as `false`"
        );
    }

    // Regression test (FFI-03 residual, 2026-08-27, see
    // note/threat_matrix_verified_fixes_execution_plan.md Task 5): `metanode_register_callbacks`
    // was previously the one exported FFI function NOT wrapped in `catch_unwind`. Same rationale
    // as `metanode_submit_transaction_batch_survives_internal_panic` above: this function is
    // `extern "C"` (non-unwind ABI), so an uncaught panic here would abort the whole test process
    // rather than fail an assertion -- a passing `cargo test` run on this test is itself part of
    // the proof that the wrapper works. `metanode_register_callbacks` has no natural
    // input-driven panic path (unlike the batch function above), so this uses its own dedicated
    // test-only fault-injection flag instead.
    #[test]
    fn metanode_register_callbacks_survives_internal_panic() {
        let callbacks = GoCallbacks {
            execute_block: None,
            process_rpc_request: None,
            free_go_buffer: None,
            get_state_root: None,
            update_tx_trace: None,
            log_message: None,
        };

        FORCE_REGISTER_CALLBACKS_PANIC_FOR_TEST.store(true, Ordering::SeqCst);
        metanode_register_callbacks(callbacks);
        FORCE_REGISTER_CALLBACKS_PANIC_FOR_TEST.store(false, Ordering::SeqCst);

        // Reaching this line at all (instead of the test process aborting) is the proof.
    }
}
