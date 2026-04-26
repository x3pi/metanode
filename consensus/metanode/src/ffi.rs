// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use crate::config::NodeConfig;
use crate::node::startup::{InitializedNode, StartupConfig};
use std::ffi::CStr;
use std::os::raw::c_char;
use std::sync::OnceLock;
use tracing::{error, info};

// The global callbacks registry configured from Go
pub static GO_CALLBACKS: OnceLock<GoCallbacks> = OnceLock::new();

// The global channel sender for zero-copy FFI transaction submission
pub static FFI_TX_SENDER: std::sync::RwLock<Option<tokio::sync::mpsc::Sender<Vec<u8>>>> = std::sync::RwLock::new(None);

pub static mut PAUSE_GUARD: Option<std::sync::RwLockWriteGuard<'static, ()>> = None;


#[repr(C)]
pub struct GoCallbacks {
    /// Send an executable block to Go for execution.
    pub execute_block: Option<extern "C" fn(payload: *const u8, len: usize) -> bool>,
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
}

/// Register the CGo callbacks.
#[no_mangle]
pub extern "C" fn metanode_register_callbacks(callbacks: GoCallbacks) {
    if GO_CALLBACKS.set(callbacks).is_err() {
        eprintln!("Warning: metanode_register_callbacks called multiple times");
    }
}

/// Pulse pause to Rust consensus
#[no_mangle]
pub extern "C" fn metanode_pause_consensus() {
    info!("⏸️ [FFI] metanode_pause_consensus called - acquiring write lock on RUST_EXECUTION_LOCK...");
    let guard = consensus_core::storage::rocksdb_store::RUST_EXECUTION_LOCK.write().expect("Failed to acquire write lock for pausing consensus");
    unsafe {
        PAUSE_GUARD = Some(std::mem::transmute(guard));
    }
    info!("⏸️ [FFI] metanode_pause_consensus: RocksDB writes are now PAUSED.");
}

/// Resume Rust consensus
#[no_mangle]
pub extern "C" fn metanode_resume_consensus() {
    info!("▶️ [FFI] metanode_resume_consensus called - dropping write lock...");
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

/// Directly submit a transaction batch from Go mempool to Rust consensus over FFI
#[no_mangle]
pub extern "C" fn metanode_submit_transaction_batch(payload: *const u8, len: usize) -> bool {
    if payload.is_null() || len == 0 {
        return true; // Ignore empty payload safely
    }

    let tx_data = unsafe { std::slice::from_raw_parts(payload, len) }.to_vec();

    // Read the current channel sender and clone it locally
    let sender_opt = if let Ok(guard) = FFI_TX_SENDER.read() {
        guard.clone()
    } else {
        None
    };

    if let Some(sender) = sender_opt {
        // try_send is non-blocking and synchronous
        match sender.try_send(tx_data) {
            Ok(_) => true,
            Err(tokio::sync::mpsc::error::TrySendError::Full(_)) => {
                // Channel is full. Go side will see `false` and automatically sleep/retry.
                false
            }
            Err(_) => {
                error!("❌ [FFI TX FLOW] Failed to send to FFI channel (channel may be closed)");
                false
            }
        }
    } else {
        error!("❌ [FFI TX FLOW] FFI_TX_SENDER is not initialized but Go tried to send TX");
        false
    }
}

/// Start the Rust consensus engine in a background thread.
#[no_mangle]
pub extern "C" fn metanode_start_consensus(config_path_ptr: *const c_char, data_dir_ptr: *const c_char) {
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
            CStr::from_ptr(data_dir_ptr)
                .to_string_lossy()
                .into_owned()
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
            eprintln!("Backtrace:\n{:?}", std::backtrace::Backtrace::force_capture());
        }));

        // Initialize tracing
        tracing_subscriber::fmt()
            .with_env_filter(
                tracing_subscriber::EnvFilter::try_from_default_env()
                    .unwrap_or_else(|_| "metanode=info,consensus_core=info".into()),
            )
            .init();

        info!("Starting MetaNode Consensus Engine (FFI Thread)...");

        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            // Build the Tokio multi-threaded runtime
            let rt = match tokio::runtime::Builder::new_multi_thread()
                .enable_all()
                .build()
            {
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

                    // Override storage path to live inside Go's data directory if provided
                    if !data_dir_str.is_empty() {
                        node_config.storage_path = std::path::PathBuf::from(&data_dir_str).join("rust_consensus");
                        info!("Storage path unified to Go data dir: {:?}", node_config.storage_path);
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

                    let startup_config = StartupConfig::new(
                        node_config, registry, None
                    );

                    let initialized_node = match InitializedNode::initialize(startup_config).await {
                        Ok(node) => node,
                        Err(e) => {
                            error!("Failed to initialize node: {}", e);
                            restart_count += 1;
                            tokio::time::sleep(tokio::time::Duration::from_secs(5)).await;
                            continue;
                        }
                    };

                    if let Err(e) = initialized_node.run_main_loop().await {
                        error!("Consensus main loop exited with error: {}", e);
                    }

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

fn copy_dir_all(src: impl AsRef<std::path::Path>, dst: impl AsRef<std::path::Path>) -> std::io::Result<()> {
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
/// Purges data_dir/rust_consensus and copies snapshot_dir/rust_consensus into it safely.
#[no_mangle]
pub extern "C" fn metanode_restore_from_snapshot(data_dir_ptr: *const c_char, snapshot_dir_ptr: *const c_char) -> bool {
    let data_dir_str = unsafe {
        if data_dir_ptr.is_null() { return false; }
        CStr::from_ptr(data_dir_ptr).to_string_lossy().into_owned()
    };
    
    let snapshot_dir_str = unsafe {
        if snapshot_dir_ptr.is_null() { return false; }
        CStr::from_ptr(snapshot_dir_ptr).to_string_lossy().into_owned()
    };

    let target_dir = std::path::PathBuf::from(&data_dir_str).join("rust_consensus");
    let source_dir = std::path::PathBuf::from(&snapshot_dir_str).join("rust_consensus");

    if !source_dir.exists() {
        error!("[FFI Restore] Snapshot source dir not found: {:?}", source_dir);
        return false;
    }

    info!("[FFI Restore] Restoring DAG from snapshot: {:?} -> {:?}", source_dir, target_dir);

    if target_dir.exists() {
        if let Err(e) = std::fs::remove_dir_all(&target_dir) {
            error!("[FFI Restore] Failed to remove old target dir {:?}: {}", target_dir, e);
            return false;
        }
    }

    if let Err(e) = copy_dir_all(&source_dir, &target_dir) {
        error!("[FFI Restore] Failed to copy snapshot files to target: {}", e);
        return false;
    }

    info!("[FFI Restore] Successfully restored rust_consensus!");
    true
}
