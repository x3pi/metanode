//! mtn-nomt-ffi: C-FFI bridge for NOMT (Nearly Optimal Merkle Trie)
//!
//! This crate provides a C-compatible API for Go (via CGo) to interact with NOMT.
//! The API is designed around batch operations to minimize FFI call overhead.
//!
//! Thread Safety:
//! - A single `NomtHandle` is created per node and lives for the entire epoch.
//! - Multiple concurrent readers are supported via `nomt_read`.
//! - Writes are batched: `nomt_session_begin` → N × `nomt_session_write` → `nomt_session_commit`.
//! - Only one write session may be active at a time (enforced by NOMT internally).

use libc::{c_char, c_int, size_t};
use nomt::hasher::Blake3Hasher;
use nomt::{KeyReadWrite, Nomt, Options};
use std::ffi::CStr;
use std::ptr;
use std::slice;


/// Opaque handle to the NOMT database instance.
/// Wrapped in Mutex to safely share between Go goroutines.
pub struct NomtHandle {
    db: Nomt<Blake3Hasher>,
}

/// Opaque handle to a write session.
/// Accumulates writes and commits them atomically.
pub struct SessionHandle {
    /// The true NOMT session, bound to a 'static lifetime because Go guarantees
    /// the NomtHandle outlives the SessionHandle.
    session: nomt::Session<Blake3Hasher>,
    /// Accumulated writes: (key_path, value). Sorted before commit.
    writes: Vec<([u8; 32], Option<Vec<u8>>)>,
    /// Accumulated reads: (key_path, value). Used for ReadThenWrite semantics.
    reads: Vec<([u8; 32], Option<Vec<u8>>)>,
}

/// Opaque handle to a finished session ready to be committed to disk.
pub struct FinishedSessionHandle {
    finished: nomt::FinishedSession,
}

// ═══════════════════════════════════════════════════════════════════════════════
// DATABASE LIFECYCLE
// ═══════════════════════════════════════════════════════════════════════════════

/// Open a NOMT database at the given path.
///
/// Returns a pointer to NomtHandle on success, null on failure.
/// The caller must eventually call `nomt_close` to free resources.
///
/// # Parameters
/// - `path`: null-terminated C string for the database directory path
/// - `commit_concurrency`: number of concurrent commit workers (1-64)
/// - `page_cache_mb`: page cache size in MiB (default: 256)
/// - `leaf_cache_mb`: leaf cache size in MiB (default: 256)
#[no_mangle]
pub extern "C" fn nomt_open(
    path: *const c_char,
    commit_concurrency: c_int,
    page_cache_mb: c_int,
    leaf_cache_mb: c_int,
) -> *mut NomtHandle {
    if path.is_null() {
        eprintln!("[nomt_ffi] nomt_open: null path");
        return ptr::null_mut();
    }

    let c_str = unsafe { CStr::from_ptr(path) };
    let path_str = match c_str.to_str() {
        Ok(s) => s,
        Err(e) => {
            eprintln!("[nomt_ffi] nomt_open: invalid UTF-8 path: {}", e);
            return ptr::null_mut();
        }
    };

    let mut opts = Options::new();
    opts.path(path_str);

    let concurrency = if commit_concurrency > 0 {
        commit_concurrency as usize
    } else {
        4
    };
    opts.commit_concurrency(concurrency);

    if page_cache_mb > 0 {
        opts.page_cache_size(page_cache_mb as usize);
    }
    if leaf_cache_mb > 0 {
        opts.leaf_cache_size(leaf_cache_mb as usize);
    }

    // Enable page cache prepopulation for predictable worst-case performance
    opts.prepopulate_page_cache(true);

    match Nomt::<Blake3Hasher>::open(opts) {
        Ok(db) => {
            let handle = Box::new(NomtHandle { db });
            Box::into_raw(handle)
        }
        Err(e) => {
            eprintln!("[nomt_ffi] nomt_open failed: {}", e);
            ptr::null_mut()
        }
    }
}

/// Close a NOMT database and free all resources.
///
/// # Safety
/// The handle must have been created by `nomt_open` and not yet closed.
#[no_mangle]
pub extern "C" fn nomt_close(handle: *mut NomtHandle) {
    if !handle.is_null() {
        unsafe {
            let _ = Box::from_raw(handle);
        }
    }
}

/// Get the current root hash of the trie.
///
/// # Parameters
/// - `handle`: NOMT handle
/// - `root_out`: pointer to a 32-byte buffer to receive the root hash
///
/// # Returns
/// 0 on success, -1 on failure.
#[no_mangle]
pub extern "C" fn nomt_root(handle: *const NomtHandle, root_out: *mut u8) -> c_int {
    if handle.is_null() || root_out.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let root = handle.db.root();
    let root_bytes = root.into_inner();

    unsafe {
        ptr::copy_nonoverlapping(root_bytes.as_ptr(), root_out, 32);
    }
    0
}

// ═══════════════════════════════════════════════════════════════════════════════
// READ OPERATIONS (can be called concurrently)
// ═══════════════════════════════════════════════════════════════════════════════

/// Read a value from the database.
///
/// # Parameters
/// - `handle`: NOMT handle
/// - `key`: pointer to a 32-byte key (KeyPath)
/// - `val_out`: pointer to a buffer to receive the value
/// - `val_max_len`: maximum length of the output buffer
/// - `val_actual_len`: pointer to receive the actual value length
///
/// # Returns
/// - 0: key found, value written to val_out
/// - 1: key not found (no value)
/// - -1: error
#[no_mangle]
pub extern "C" fn nomt_read(
    handle: *const NomtHandle,
    key: *const u8,
    val_out: *mut u8,
    val_max_len: size_t,
    val_actual_len: *mut size_t,
) -> c_int {
    if handle.is_null() || key.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let key_path: [u8; 32] = unsafe {
        let mut kp = [0u8; 32];
        ptr::copy_nonoverlapping(key, kp.as_mut_ptr(), 32);
        kp
    };

    match handle.db.read(key_path) {
        Ok(Some(value)) => {
            if val_out.is_null() || val_actual_len.is_null() {
                return -1;
            }
            let len = value.len().min(val_max_len);
            unsafe {
                ptr::copy_nonoverlapping(value.as_ptr(), val_out, len);
                *val_actual_len = value.len();
            }
            0
        }
        Ok(None) => {
            if !val_actual_len.is_null() {
                unsafe { *val_actual_len = 0; }
            }
            1 // not found
        }
        Err(e) => {
            eprintln!("[nomt_ffi] nomt_read error: {}", e);
            -1
        }
    }
}

// ═══════════════════════════════════════════════════════════════════════════════
// WRITE SESSION (single-writer, batch commit)
// ═══════════════════════════════════════════════════════════════════════════════

/// Begin a new write session.
///
/// The returned handle accumulates writes. Call `nomt_session_commit`
/// to atomically apply all writes and compute the new root hash.
///
/// # Returns
/// Pointer to SessionHandle, or null on failure.
#[no_mangle]
pub extern "C" fn nomt_session_begin(handle: *mut NomtHandle) -> *mut SessionHandle {
    if handle.is_null() {
        return ptr::null_mut();
    }
    
    let handle_ref = unsafe { &*handle };
    // Transmute to 'static: this is safe because the Go garbage collector and FFI bridge
    // strictly guarantee that the NomtHandle lives longer than any SessionHandle.
    let handle_static: &'static NomtHandle = unsafe { std::mem::transmute(handle_ref) };
    
    let session = handle_static.db.begin_session(nomt::SessionParams::default());
    
    let session_handle = Box::new(SessionHandle {
        session,
        writes: Vec::with_capacity(64 * 1024), // pre-allocate for ~64k writes
        reads: Vec::new(),
    });
    Box::into_raw(session_handle)
}

/// Dispatches a background asynchronous fetch to the NOMT threadpool to load 
/// the Merkle authentication branch for this key.
#[no_mangle]
pub extern "C" fn nomt_session_warm_up(
    session: *mut SessionHandle,
    key: *const u8,
) -> c_int {
    if session.is_null() || key.is_null() {
        return -1;
    }

    let session_ref = unsafe { &*session };
    let key_path: [u8; 32] = unsafe {
        let mut kp = [0u8; 32];
        ptr::copy_nonoverlapping(key, kp.as_mut_ptr(), 32);
        kp
    };

    session_ref.session.warm_up(key_path);
    0
}

/// Add a read record to the session (for ReadThenWrite tracking).
///
/// This records the value that was read for a key BEFORE it gets written.
/// During commit, keys that have both a read and a write will use
/// ReadThenWrite semantics (needed by NOMT for rollback delta computation).
///
/// # Parameters
/// - `session`: session handle
/// - `key`: pointer to a 32-byte key
/// - `val`: pointer to previous value bytes (null if key didn't exist)
/// - `val_len`: length of the previous value (0 if key didn't exist)
#[no_mangle]
pub extern "C" fn nomt_session_record_read(
    session: *mut SessionHandle,
    key: *const u8,
    val: *const u8,
    val_len: size_t,
) -> c_int {
    if session.is_null() || key.is_null() {
        return -1;
    }

    let session = unsafe { &mut *session };
    let key_path: [u8; 32] = unsafe {
        let mut kp = [0u8; 32];
        ptr::copy_nonoverlapping(key, kp.as_mut_ptr(), 32);
        kp
    };

    let value = if val.is_null() || val_len == 0 {
        None
    } else {
        Some(unsafe { slice::from_raw_parts(val, val_len) }.to_vec())
    };

    session.reads.push((key_path, value));
    0
}

/// Add a write to the session.
///
/// # Parameters
/// - `session`: session handle
/// - `key`: pointer to a 32-byte key
/// - `val`: pointer to value bytes (null to delete the key)
/// - `val_len`: length of the value (0 to delete)
///
/// # Returns
/// 0 on success, -1 on failure.
#[no_mangle]
pub extern "C" fn nomt_session_write(
    session: *mut SessionHandle,
    key: *const u8,
    val: *const u8,
    val_len: size_t,
) -> c_int {
    if session.is_null() || key.is_null() {
        return -1;
    }

    let session = unsafe { &mut *session };
    let key_path: [u8; 32] = unsafe {
        let mut kp = [0u8; 32];
        ptr::copy_nonoverlapping(key, kp.as_mut_ptr(), 32);
        kp
    };

    let value = if val.is_null() || val_len == 0 {
        None
    } else {
        Some(unsafe { slice::from_raw_parts(val, val_len) }.to_vec())
    };

    session.writes.push((key_path, value));
    0
}

/// Batch-add multiple writes to the session at once.
///
/// This is the high-performance path: Go collects all dirty accounts,
/// marshals them, and passes them in a single FFI call to avoid
/// per-account FFI overhead.
///
/// # Parameters
/// - `session`: session handle
/// - `keys`: pointer to N × 32-byte keys (contiguous)
/// - `vals`: pointer to flattened values
/// - `val_lens`: pointer to array of N value lengths
/// - `count`: number of key-value pairs
///
/// # Returns
/// 0 on success, -1 on failure.
#[no_mangle]
pub extern "C" fn nomt_session_batch_write(
    session: *mut SessionHandle,
    keys: *const u8,
    vals: *const u8,
    val_lens: *const size_t,
    count: size_t,
) -> c_int {
    if session.is_null() || keys.is_null() || val_lens.is_null() {
        return -1;
    }
    if count == 0 {
        return 0;
    }

    let session = unsafe { &mut *session };
    session.writes.reserve(count);

    unsafe {
        let mut val_offset = 0;
        for i in 0..count {
            let mut kp = [0u8; 32];
            ptr::copy_nonoverlapping(keys.add(i * 32), kp.as_mut_ptr(), 32);

            let val_len = *val_lens.add(i);

            let value = if vals.is_null() || val_len == 0 {
                None
            } else {
                let v = Some(slice::from_raw_parts(vals.add(val_offset), val_len).to_vec());
                val_offset += val_len;
                v
            };

            session.writes.push((kp, value));
        }
    }
    0
}

/// Commit the session: apply all accumulated writes to the database
/// and compute the new Merkle root.
///
/// After commit, the session handle is consumed and freed.
///
/// # Parameters
/// - `handle`: NOMT database handle
/// - `session`: session handle (consumed after this call)
/// - `new_root_out`: pointer to a 32-byte buffer to receive the new root hash
///
/// # Returns
/// 0 on success, -1 on failure.
#[no_mangle]
pub extern "C" fn nomt_session_commit(
    handle: *mut NomtHandle,
    session: *mut SessionHandle,
    new_root_out: *mut u8,
) -> c_int {
    if handle.is_null() || session.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let session = unsafe { Box::from_raw(session) };

    // Build a lookup map for reads (for ReadThenWrite semantics)
    let mut read_map = std::collections::HashMap::with_capacity(session.reads.len());
    for (key, val) in &session.reads {
        read_map.insert(*key, val.clone());
    }

    // Build the actuals vector: sorted by KeyPath as required by NOMT
    let mut actuals: Vec<([u8; 32], KeyReadWrite)> = Vec::with_capacity(session.writes.len());

    for (key, new_val) in session.writes {
        if let Some(old_val) = read_map.get(&key) {
            // Key was read before being written → ReadThenWrite
            actuals.push((key, KeyReadWrite::ReadThenWrite(old_val.clone(), new_val)));
        } else {
            // Key was only written (new key or blind overwrite)
            actuals.push((key, KeyReadWrite::Write(new_val)));
        }
    }

    // CRITICAL: NOMT requires actuals sorted by KeyPath
    actuals.sort_by(|a, b| a.0.cmp(&b.0));

    // Deduplicate: if the same key appears multiple times, keep only the last write
    actuals.dedup_by(|a, b| {
        if a.0 == b.0 {
            // Keep `b` (which comes earlier in sorted order after dedup_by semantics)
            // Actually dedup_by removes `a` if closure returns true, keeping `b`.
            // We want to keep the LAST write. Since we haven't reversed, and dedup_by
            // keeps the first of duplicates, let's handle this by overwriting.
            std::mem::swap(a, b);
            true
        } else {
            false
        }
    });

    // Finish the session and commit
    match session.session.finish(actuals) {
        Ok(finished) => {
            let root = finished.root();
            let root_bytes = root.into_inner();

            match finished.commit(&handle.db) {
                Ok(()) => {
                    if !new_root_out.is_null() {
                        unsafe {
                            ptr::copy_nonoverlapping(
                                root_bytes.as_ptr(),
                                new_root_out,
                                32,
                            );
                        }
                    }
                    0
                }
                Err(e) => {
                    eprintln!("[nomt_ffi] nomt_session_commit: commit failed: {}", e);
                    -1
                }
            }
        }
        Err(e) => {
            eprintln!("[nomt_ffi] nomt_session_commit: finish failed: {}", e);
            -1
        }
    }
}

/// Finish the session: compute the Merkle root but DO NOT write to disk yet.
/// This is fast and CPU-bound.
///
/// # Returns
/// Pointer to FinishedSessionHandle, or null on failure.
#[no_mangle]
pub extern "C" fn nomt_session_finish(
    handle: *mut NomtHandle,
    session: *mut SessionHandle,
    new_root_out: *mut u8,
) -> *mut FinishedSessionHandle {
    if handle.is_null() || session.is_null() {
        return ptr::null_mut();
    }

    let _handle = unsafe { &*handle };
    let session = unsafe { Box::from_raw(session) };

    let mut read_map = std::collections::HashMap::with_capacity(session.reads.len());
    for (key, val) in &session.reads {
        read_map.insert(*key, val.clone());
    }

    let mut actuals: Vec<([u8; 32], KeyReadWrite)> = Vec::with_capacity(session.writes.len());

    for (key, new_val) in session.writes {
        if let Some(old_val) = read_map.get(&key) {
            actuals.push((key, KeyReadWrite::ReadThenWrite(old_val.clone(), new_val)));
        } else {
            actuals.push((key, KeyReadWrite::Write(new_val)));
        }
    }

    actuals.sort_by(|a, b| a.0.cmp(&b.0));
    actuals.dedup_by(|a, b| {
        if a.0 == b.0 {
            std::mem::swap(a, b);
            true
        } else {
            false
        }
    });

    match session.session.finish(actuals) {
        Ok(finished) => {
            let root = finished.root();
            let root_bytes = root.into_inner();
            if !new_root_out.is_null() {
                unsafe {
                    ptr::copy_nonoverlapping(root_bytes.as_ptr(), new_root_out, 32);
                }
            }
            
            let finished_handle = Box::new(FinishedSessionHandle { finished });
            Box::into_raw(finished_handle)
        }
        Err(e) => {
            eprintln!("[nomt_ffi] nomt_session_finish failed: {}", e);
            ptr::null_mut()
        }
    }
}

/// Commit a finished session to disk (Disk I/O bound).
///
/// # Returns
/// 0 on success, -1 on test failure.
#[no_mangle]
pub extern "C" fn nomt_commit_payload(
    handle: *mut NomtHandle,
    finished_session: *mut FinishedSessionHandle,
) -> c_int {
    if handle.is_null() || finished_session.is_null() {
        return -1;
    }

    let handle = unsafe { &*handle };
    let finished_session = unsafe { Box::from_raw(finished_session) };

    match finished_session.finished.commit(&handle.db) {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("[nomt_ffi] nomt_commit_payload failed: {}", e);
            -1
        }
    }
}

/// Abort an uncommitted finished session.
#[no_mangle]
pub extern "C" fn nomt_finished_session_abort(finished_session: *mut FinishedSessionHandle) {
    if !finished_session.is_null() {
        unsafe {
            let _ = Box::from_raw(finished_session);
        }
    }
}

/// Abort an uncommitted session and free its resources.
///
/// # Safety
/// The session handle must have been created by `nomt_session_begin` and not yet committed.
#[no_mangle]
pub extern "C" fn nomt_session_abort(session: *mut SessionHandle) {
    if !session.is_null() {
        unsafe {
            let _ = Box::from_raw(session);
        }
    }
}
