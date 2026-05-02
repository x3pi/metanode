package nomt_ffi

/*
#cgo CFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -L${SRCDIR}/rust_lib/target/release -lmtn_nomt -lm -ldl -lpthread
#include "nomt_ffi.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// maxValueSize is the maximum expected value size for account state data.
// MetaNode AccountState is typically ~200-500 bytes. Using 64KB as safe upper bound.
const maxValueSize = 64 * 1024

// Handle wraps the opaque NOMT database pointer.
type Handle struct {
	ptr               *C.NomtHandle
	mu                sync.RWMutex // protects ptr lifecycle (open/close)
	path              string       // stores the path for snapshotting
	commitConcurrency int
	pageCacheMB       int
	leafCacheMB       int
}

// Session wraps the opaque NOMT write session pointer.
type Session struct {
	ptr    *C.SessionHandle
	handle *Handle
}

// FinishedSession wraps an opaque pointer to a session that has finished
// computing its Merkle root but has NOT yet written to disk.
type FinishedSession struct {
	ptr    *C.FinishedSessionHandle
	handle *Handle
}

// Open creates a new NOMT database at the given path.
// Parameters:
//   - path: filesystem path for the NOMT database directory
//   - commitConcurrency: number of concurrent commit workers (1-64)
//   - pageCacheMB: page cache size in MiB (0 = default 256)
//   - leafCacheMB: leaf cache size in MiB (0 = default 256)
func Open(path string, commitConcurrency, pageCacheMB, leafCacheMB int) (*Handle, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	ptr := C.nomt_open(cPath, C.int(commitConcurrency), C.int(pageCacheMB), C.int(leafCacheMB))
	if ptr == nil {
		return nil, fmt.Errorf("nomt_ffi: failed to open database at %s", path)
	}

	return &Handle{
		ptr:               ptr,
		path:              path,
		commitConcurrency: commitConcurrency,
		pageCacheMB:       pageCacheMB,
		leafCacheMB:       leafCacheMB,
	}, nil
}

// Close frees all resources associated with the database.
func (h *Handle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ptr != nil {
		C.nomt_close(h.ptr)
		h.ptr = nil
	}
}

// GetPath returns the filesystem path of the database.
func (h *Handle) GetPath() string {
	return h.path
}

// CloseForSnapshot acquires the lock and fully closes the underlying NOMT/RocksDB
// database instance. This guarantees that all background compaction and flush
// threads are stopped, making it 100% safe to `cp -a` the database directory.
func (h *Handle) CloseForSnapshot() {
	h.mu.Lock()
	if h.ptr != nil {
		C.nomt_close(h.ptr)
		h.ptr = nil
	}
}

// ReopenAfterSnapshot reopens the database using the previously saved path and config,
// and releases the lock acquired by `CloseForSnapshot()`.
// Includes retry logic because the OS may not have fully released the NOMT directory
// lock file immediately after nomt_close() returns (e.g., background compaction threads
// still cleaning up).
func (h *Handle) ReopenAfterSnapshot() error {
	cPath := C.CString(h.path)
	defer C.free(unsafe.Pointer(cPath))

	// Remove stale lock file left by the previous instance.
	// nomt_close() should clean this up, but on some filesystems the OS-level
	// flock may linger for a few milliseconds after the fd is closed.
	lockFile := h.path + "/.lock"
	_ = os.Remove(lockFile)

	const maxRetries = 5
	var ptr *C.NomtHandle
	for attempt := 0; attempt < maxRetries; attempt++ {
		ptr = C.nomt_open(cPath, C.int(h.commitConcurrency), C.int(h.pageCacheMB), C.int(h.leafCacheMB))
		if ptr != nil {
			break
		}
		// Wait with exponential backoff: 50ms, 100ms, 200ms, 400ms, 800ms
		waitMs := 50 << attempt
		logger.Warn("⚠️ [TRIE] nomt_ffi ReopenAfterSnapshot: attempt %d/%d failed, retrying in %dms (path=%s)",
			attempt+1, maxRetries, waitMs, h.path)
		time.Sleep(time.Duration(waitMs) * time.Millisecond)
		// Try removing lock file again in case it was recreated
		_ = os.Remove(lockFile)
	}

	if ptr == nil {
		h.mu.Unlock() // avoid deadlock on failure
		return fmt.Errorf("nomt_ffi: failed to reopen database after snapshot at %s (after %d retries)", h.path, maxRetries)
	}
	h.ptr = ptr
	h.mu.Unlock()
	return nil
}

// AcquireExclusive acquires the exclusive lock on the database.
// This blocks all reads and commits. Use it only for critical operations like snapshotting.
func (h *Handle) AcquireExclusive() {
	h.mu.Lock()
}

// ReleaseExclusive releases the exclusive lock on the database.
func (h *Handle) ReleaseExclusive() {
	h.mu.Unlock()
}

// Root returns the current 32-byte Merkle root hash.
func (h *Handle) Root() ([32]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var root [32]byte
	ret := C.nomt_root(h.ptr, (*C.uint8_t)(&root[0]))
	if ret != 0 {
		return root, fmt.Errorf("nomt_ffi: failed to get root")
	}
	return root, nil
}

// global pool to prevent memory churn on hot path reads
var readBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, maxValueSize)
		return &buf
	},
}

// Read retrieves the value for a 32-byte key from the database.
// Returns (value, true) if found, (nil, false) if not found.
// This method is safe to call concurrently from multiple goroutines.
func (h *Handle) Read(key [32]byte) ([]byte, bool, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	bufPtr := readBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer readBufPool.Put(bufPtr)

	var actualLen C.size_t

	ret := C.nomt_read(
		h.ptr,
		(*C.uint8_t)(&key[0]),
		(*C.uint8_t)(&buf[0]),
		C.size_t(len(buf)),
		&actualLen,
	)

	// If the buffer was too small, Rust's nomt_read will write up to val_max_len
	// but it sets val_actual_len to the full length of the value!
	if ret == 0 && int(actualLen) > len(buf) {
		// Reallocate and read again
		buf = make([]byte, int(actualLen))
		ret = C.nomt_read(
			h.ptr,
			(*C.uint8_t)(&key[0]),
			(*C.uint8_t)(&buf[0]),
			C.size_t(len(buf)),
			&actualLen,
		)
	}

	switch ret {
	case 0:
		if int(actualLen) > len(buf) {
			return nil, false, fmt.Errorf("nomt_ffi: buffer still too small after reallocation (len=%d, actual=%d)", len(buf), actualLen)
		}
		// Return a copy so the caller doesn't hold onto the large buffer if actualLen is small
		res := make([]byte, actualLen)
		copy(res, buf[:actualLen])
		return res, true, nil
	case 1:
		return nil, false, nil // not found
	default:
		return nil, false, fmt.Errorf("nomt_ffi: read error for key %x", key[:8])
	}

}

// BeginSession creates a new write session.
// Add writes via Session.Write() or Session.BatchWrite(),
// then call Session.Commit() to apply atomically.
func BeginSession(h *Handle) *Session {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.ptr == nil {
		return nil
	}
	ptr := C.nomt_session_begin(h.ptr)
	if ptr == nil {
		return nil
	}
	return &Session{ptr: ptr, handle: h}
}

// WarmUp sends an asynchronous prefetch request to the NOMT threadpool to load
// the Merkle branch nodes from disk.
func (s *Session) WarmUp(key [32]byte) error {
	if s.ptr == nil || s.handle == nil {
		return fmt.Errorf("nomt_ffi: invalid session")
	}
	s.handle.mu.RLock()
	defer s.handle.mu.RUnlock()
	if s.handle.ptr == nil {
		return fmt.Errorf("nomt_ffi: handle closed")
	}

	ret := C.nomt_session_warm_up(
		s.ptr,
		(*C.uint8_t)(&key[0]),
	)
	if ret != 0 {
		return fmt.Errorf("nomt_ffi: warm_up error")
	}
	return nil
}

// RecordRead records a previous read value for a key (for ReadThenWrite semantics).
// This should be called for keys where the old value is known before writing.
func (s *Session) RecordRead(key [32]byte, oldValue []byte) error {
	if s.ptr == nil || s.handle == nil {
		return fmt.Errorf("nomt_ffi: invalid session")
	}
	s.handle.mu.RLock()
	defer s.handle.mu.RUnlock()
	if s.handle.ptr == nil {
		return fmt.Errorf("nomt_ffi: handle closed")
	}

	var valPtr *C.uint8_t
	valLen := C.size_t(0)
	if len(oldValue) > 0 {
		valPtr = (*C.uint8_t)(&oldValue[0])
		valLen = C.size_t(len(oldValue))
	}

	ret := C.nomt_session_record_read(
		s.ptr,
		(*C.uint8_t)(&key[0]),
		valPtr,
		valLen,
	)
	if ret != 0 {
		return fmt.Errorf("nomt_ffi: record_read error")
	}
	return nil
}

// Write adds a single key-value write to the session.
// Pass nil value to delete the key.
func (s *Session) Write(key [32]byte, value []byte) error {
	if s.ptr == nil || s.handle == nil {
		return fmt.Errorf("nomt_ffi: invalid session")
	}
	s.handle.mu.RLock()
	defer s.handle.mu.RUnlock()
	if s.handle.ptr == nil {
		return fmt.Errorf("nomt_ffi: handle closed")
	}

	var valPtr *C.uint8_t
	valLen := C.size_t(0)
	if len(value) > 0 {
		valPtr = (*C.uint8_t)(&value[0])
		valLen = C.size_t(len(value))
	}

	ret := C.nomt_session_write(
		s.ptr,
		(*C.uint8_t)(&key[0]),
		valPtr,
		valLen,
	)
	if ret != 0 {
		return fmt.Errorf("nomt_ffi: write error")
	}
	return nil
}

// BatchWrite adds multiple key-value pairs to the session in a single FFI call.
// This is the high-performance path for block commits.
// Keys must be 32 bytes each. Values can be nil to delete.
//
// Implementation note: CGo forbids passing Go pointers that contain other Go pointers
// into C. We flatten all values into a single contiguous byte array here.
func (s *Session) BatchWrite(keys [][32]byte, values [][]byte) error {
	if s.ptr == nil || s.handle == nil {
		return fmt.Errorf("nomt_ffi: invalid session")
	}
	if len(keys) != len(values) {
		return fmt.Errorf("nomt_ffi: BatchWrite keys/values length mismatch (%d vs %d)", len(keys), len(values))
	}
	n := len(keys)
	if n == 0 {
		return nil
	}

	s.handle.mu.RLock()
	defer s.handle.mu.RUnlock()
	if s.handle.ptr == nil {
		return fmt.Errorf("nomt_ffi: handle closed")
	}

	// Flatten keys into contiguous byte array (N × 32 bytes) — single Go buffer, no nested pointers
	flatKeys := make([]byte, n*32)
	for i, k := range keys {
		copy(flatKeys[i*32:], k[:])
	}

	// Calculate total values size
	totalValsLen := 0
	for _, v := range values {
		totalValsLen += len(v)
	}

	// Flatten values and collect lengths
	var flatValsPtr *C.uint8_t
	var flatValues []byte

	if totalValsLen > 0 {
		flatValues = make([]byte, totalValsLen)
		flatValsPtr = (*C.uint8_t)(&flatValues[0])
	}

	valLens := make([]C.size_t, n)
	offset := 0
	for i, v := range values {
		l := len(v)
		if l > 0 {
			copy(flatValues[offset:], v)
			offset += l
		}
		valLens[i] = C.size_t(l)
	}

	ret := C.nomt_session_batch_write(
		s.ptr,
		(*C.uint8_t)(&flatKeys[0]),
		flatValsPtr,
		(*C.size_t)(&valLens[0]),
		C.size_t(n),
	)
	if ret != 0 {
		return fmt.Errorf("nomt_ffi: batch_write error for %d entries", n)
	}
	return nil
}

// Commit atomically applies all accumulated writes, computes the new Merkle root,
// and returns the 32-byte root hash. The session is consumed and cannot be reused.
// Note: This method blocks until disk I/O completes. For high-performance async
// commits, use Finish() and CommitPayload() instead.
func (s *Session) Commit(h *Handle) ([32]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var newRoot [32]byte

	ret := C.nomt_session_commit(
		h.ptr,
		s.ptr,
		(*C.uint8_t)(&newRoot[0]),
	)
	s.ptr = nil // session consumed

	if ret != 0 {
		return newRoot, fmt.Errorf("nomt_ffi: commit failed")
	}
	return newRoot, nil
}

// Finish computes the new Merkle root in-memory but DOES NOT write to disk yet.
// This is fast and CPU-bound, ideal for the critical path.
// The session is consumed. Returns the new root hash and a FinishedSession payload
// which can be passed to CommitPayload() later in a background thread.
func (s *Session) Finish(h *Handle) ([32]byte, *FinishedSession, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.ptr == nil {
		return [32]byte{}, nil, fmt.Errorf("nomt_ffi: handle closed")
	}

	var newRoot [32]byte

	ptr := C.nomt_session_finish(
		h.ptr,
		s.ptr,
		(*C.uint8_t)(&newRoot[0]),
	)
	s.ptr = nil // session consumed

	if ptr == nil {
		return newRoot, nil, fmt.Errorf("nomt_ffi: session finish failed")
	}
	return newRoot, &FinishedSession{ptr: ptr, handle: h}, nil
}

// CommitPayload performs the actual disk I/O to persist a FinishedSession.
// This is typically called from a background worker thread.
func (fs *FinishedSession) CommitPayload(h *Handle) error {
	if fs.ptr == nil {
		return fmt.Errorf("nomt_ffi: finished session is already committed or aborted")
	}

	// Fast lock to ensure Handle isn't closed during commit
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.ptr == nil {
		return fmt.Errorf("nomt_ffi: handle closed")
	}

	ret := C.nomt_commit_payload(h.ptr, fs.ptr)
	fs.ptr = nil // consumed

	if ret != 0 {
		return fmt.Errorf("nomt_ffi: commit_payload failed")
	}
	return nil
}

// Abort discards an uncommitted session.
func (s *Session) Abort() {
	if s.ptr != nil && s.handle != nil {
		s.handle.mu.RLock()
		defer s.handle.mu.RUnlock()
		if s.handle.ptr != nil {
			C.nomt_session_abort(s.ptr)
		}
		s.ptr = nil
	}
}

// Abort discards an uncommitted finished session.
func (fs *FinishedSession) Abort() {
	if fs.ptr != nil && fs.handle != nil {
		fs.handle.mu.RLock()
		defer fs.handle.mu.RUnlock()
		if fs.handle.ptr != nil {
			C.nomt_finished_session_abort(fs.ptr)
		}
		fs.ptr = nil
	}
}
