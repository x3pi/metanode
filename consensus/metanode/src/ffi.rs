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


// DIAGNOSTIC (May 2026): FFI TX submission metrics for stall diagnosis
use std::sync::atomic::{AtomicU64, Ordering as AtomicOrdering};
static FFI_TX_SUBMIT_COUNT: AtomicU64 = AtomicU64::new(0);
static FFI_TX_SUBMIT_BYTES: AtomicU64 = AtomicU64::new(0);
static FFI_TX_FULL_COUNT: AtomicU64 = AtomicU64::new(0);
static FFI_TX_LAST_LOG_SECS: AtomicU64 = AtomicU64::new(0);

pub static mut PAUSE_GUARD: Option<std::sync::RwLockWriteGuard<'static, ()>> = None;

/// Tracks whether pause is active
static PAUSE_ACTIVE: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

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
    if GO_CALLBACKS.set(callbacks).is_err() {
        eprintln!("Warning: metanode_register_callbacks called multiple times");
    }
}

/// Pulse pause to Rust consensus
/// SAFETY: Waits indefinitely until Go calls metanode_resume_consensus.
/// This prevents State Corruption if Go takes a long time to snapshot.
#[no_mangle]
pub extern "C" fn metanode_pause_consensus() {
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
}

/// Resume Rust consensus
#[no_mangle]
pub extern "C" fn metanode_resume_consensus() {
    info!("▶️ [FFI] metanode_resume_consensus called - dropping write lock...");
    PAUSE_ACTIVE.store(false, std::sync::atomic::Ordering::SeqCst);
    unsafe {
        PAUSE_GUARD = None;
    }
    info!("▶️ [FFI] metanode_resume_consensus: RocksDB writes RESUMED.");
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

/// Returns the current number of items in the FFI TX queue.
pub fn get_ffi_tx_queue_depth() -> usize {
    if let Ok(guard) = FFI_TX_SENDER.read() {
        if let Some(sender) = guard.as_ref() {
            return sender.max_capacity() - sender.capacity();
        }
    }
    0
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
    if payload.is_null() || len == 0 {
        return true; // Ignore empty payload safely
    }

    let tx_data = unsafe { std::slice::from_raw_parts(payload, len) }.to_vec();

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
}

/// Start the Rust consensus engine in a background thread.
#[no_mangle]
pub unsafe extern "C" fn metanode_start_consensus(
    config_path_ptr: *const c_char,
    data_dir_ptr: *const c_char,
) {
    let config_path_str = unsafe {
        if config_path_ptr.is_null() {
            eprintln!("Error: config_path_ptr is null");
            return;
        }
        CStr::from_ptr(config_path_ptr)
            .to_string_lossy()
            .into_owned()
    };

    let data_dir_str = unsafe {
        if data_dir_ptr.is_null() {
            "".to_string()
        } else {
            CStr::from_ptr(data_dir_ptr).to_string_lossy().into_owned()
        }
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


/// Custom writer that forwards Rust tracing logs to Go logger via CGo callback
struct GoLogWriter;

impl std::io::Write for GoLogWriter {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        let msg = String::from_utf8_lossy(buf);
        let trimmed = msg.trim_end();
        if !trimmed.is_empty() {
            if let Some(callbacks) = GO_CALLBACKS.get() {
                if let Some(log_func) = callbacks.log_message {
                    if let Ok(c_msg) = std::ffi::CString::new(trimmed) {
                        log_func(1, c_msg.as_ptr(), trimmed.len());
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

impl<'a> tracing_subscriber::fmt::MakeWriter<'a> for GoLogWriter {
    type Writer = GoLogWriter;

    fn make_writer(&'a self) -> Self::Writer {
        GoLogWriter
    }
}
        // Initialize tracing for FFI thread (captured into execution.log via Go callback)
        let _ = tracing_subscriber::fmt()
            .with_ansi(false)
            .with_writer(GoLogWriter)
            .with_env_filter(
                tracing_subscriber::EnvFilter::try_from_default_env()
                    .unwrap_or_else(|_| "info".into()),
            )
            .try_init();

        info!("Starting MetaNode Consensus Engine (FFI Thread)...");

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
                        info!(
                            "🔄 [FFI RESTART] Attempt #{} — previous instance crashed. \
                             Waiting 10s for old connections/tasks to drain...",
                            restart_count
                        );
                        // Extended delay: old TCP connections (consensus P2P, gRPC) need
                        // TIME_WAIT to expire. 5s was too aggressive — peers still had
                        // open connections to old ports, causing bind/connect failures.
                        tokio::time::sleep(tokio::time::Duration::from_secs(10)).await;
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

        if let Err(e) = result {
            eprintln!("🚨 [RUST FFI] Consensus engine panicked: {:?}", e);
            // DO NOT re-panic — that would abort() the Go process
        }
    });
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
}
