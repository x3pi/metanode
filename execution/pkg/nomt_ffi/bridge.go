package nomt_ffi

/*
#cgo CFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -L${SRCDIR}/../../../target/release -lmtn_nomt -lm -ldl -lpthread
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
)

var (
	keysBufPool = sync.Pool{
		New: func() interface{} {
			b := make([]byte, 0, 1024*32)
			return &b
		},
	}
	valsBufPool = sync.Pool{
		New: func() interface{} {
			b := make([]byte, 0, 1024*1024)
			return &b
		},
	}
	valLensPool = sync.Pool{
		New: func() interface{} {
			b := make([]C.size_t, 0, 1024)
			return &b
		},
	}
)

// maxValueSize is the maximum expected value size for account state data.
// MetaNode AccountState is typically ~200-500 bytes. Using 64KB as safe upper bound.
const maxValueSize = 64 * 1024

// Handle wraps the opaque NOMT database pointer.
type Handle struct {
	ptr               *C.NomtHandle
	stateDbPtr        *C.RustStateDBHandle // Added for simplified FFI
	mu                sync.RWMutex // protects ptr lifecycle (open/close)
	path              string       // stores the path for snapshotting
	commitConcurrency int
	pageCacheMB       int
	leafCacheMB       int
	hashtableBuckets  int
	preallocate       bool

	sessionsMu      sync.Mutex
	pendingSessions []*FinishedSession

	closing     bool
	activeCount int
	activeCond  *sync.Cond

	commitPayloadMu sync.Mutex
}

func (h *Handle) LockCommitPayload() {
	h.commitPayloadMu.Lock()
}

func (h *Handle) UnlockCommitPayload() {
	h.commitPayloadMu.Unlock()
}

// Session wraps the opaque NOMT write session pointer.
type Session struct {
	ptr    *C.SessionHandle
	handle *Handle
}

// FinishedSession wraps an opaque pointer to a session that has finished
// computing its Merkle root but has NOT yet written to disk.
type FinishedSession struct {
	mu     sync.Mutex
	ptr    *C.FinishedSessionHandle
	handle *Handle
}

// Open creates a new NOMT database at the given path.
// Parameters:
//   - path: filesystem path for the NOMT database directory
//   - commitConcurrency: number of concurrent commit workers (1-64)
//   - pageCacheMB: page cache size in MiB (0 = default 256)
//   - leafCacheMB: leaf cache size in MiB (0 = default 256)
func Open(path string, commitConcurrency, pageCacheMB, leafCacheMB, hashtableBuckets int, preallocate bool) (*Handle, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	preallocVal := 0
	if preallocate {
		preallocVal = 1
	}

	ptr := C.nomt_open(cPath, C.int(commitConcurrency), C.int(pageCacheMB), C.int(leafCacheMB), C.int(hashtableBuckets), C.int(preallocVal))
	if ptr == nil {
		return nil, fmt.Errorf("nomt_ffi: failed to open database at %s", path)
	}

	stateDbPtr := C.state_db_open_from_handle(ptr, cPath)
	if stateDbPtr == nil {
		C.nomt_close(ptr)
		return nil, fmt.Errorf("nomt_ffi: failed to open state_db at %s", path)
	}

	h := &Handle{
		ptr:               ptr,
		stateDbPtr:        stateDbPtr,
		path:              path,
		commitConcurrency: commitConcurrency,
		pageCacheMB:       pageCacheMB,
		leafCacheMB:       leafCacheMB,
		hashtableBuckets:  hashtableBuckets,
		preallocate:       preallocate,
	}
	h.activeCond = sync.NewCond(&h.sessionsMu)

	return h, nil
}

// Close frees all resources associated with the database.
// Close frees all resources associated with the database.
// It pauses new session creation, waits for all active sessions to finish or abort,
// aborts any pending finished sessions to release their memory, and then closes the handle.
func (h *Handle) Close() {
	diagDone := make(chan struct{})
	waitStart := time.Now()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-diagDone:
				return
			case <-ticker.C:
				h.sessionsMu.Lock()
				ac, ps := h.activeCount, len(h.pendingSessions)
				h.sessionsMu.Unlock()
				fmt.Printf("🔒 [NOMT-DIAG] Close WAITING at %s: activeCount=%d, pendingSessions=%d, elapsed=%v\n",
					h.path, ac, ps, time.Since(waitStart))
			}
		}
	}()

	// 1. Prevent new sessions from starting
	h.sessionsMu.Lock()
	h.closing = true

	// 2. Wait for all active sessions to either Abort() or turn into FinishedSessions
	for h.activeCount > len(h.pendingSessions) {
		h.activeCond.Wait()
	}
	close(diagDone)

	pending := h.pendingSessions
	h.pendingSessions = nil
	h.sessionsMu.Unlock()

	// 3. Abort all pending finished sessions to release memory safely
	h.LockCommitPayload()
	for _, fs := range pending {
		if fs != nil {
			fs.Abort()
		}
	}
	h.UnlockCommitPayload()

	// 4. Forcefully close NOMT. Since there are NO active Go Sessions holding
	// Arc<Core> references, nomt_close will fully drop the database synchronously.
	h.mu.Lock()
	if h.stateDbPtr != nil {
		C.state_db_close(h.stateDbPtr)
		h.stateDbPtr = nil
	}
	if h.ptr != nil {
		C.nomt_close(h.ptr)
		h.ptr = nil
	}
	h.mu.Unlock()
}

// GetPath returns the filesystem path of the database.
func (h *Handle) GetPath() string {
	return h.path
}

// CloseForSnapshot gracefully closes the database for snapshotting.
// It pauses new session creation and waits for all active sessions to finish
// mutating memory-mapped files before invoking nomt_close().
func (h *Handle) CloseForSnapshot() {
	diagDone := make(chan struct{})
	waitStart := time.Now()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-diagDone:
				return
			case <-ticker.C:
				h.sessionsMu.Lock()
				ac, ps := h.activeCount, len(h.pendingSessions)
				h.sessionsMu.Unlock()
				fmt.Printf("🔒 [NOMT-DIAG] CloseForSnapshot WAITING at %s: activeCount=%d, pendingSessions=%d, elapsed=%v\n",
					h.path, ac, ps, time.Since(waitStart))
			}
		}
	}()

	// 1. Prevent new sessions from starting
	h.sessionsMu.Lock()
	h.closing = true

	// 2. Wait for all active sessions to either Close() or turn into FinishedSessions
	for h.activeCount > len(h.pendingSessions) {
		h.activeCond.Wait()
	}
	close(diagDone)
	if d := time.Since(waitStart); d > 100*time.Millisecond {
		fmt.Printf("⚠️ [NOMT-DIAG] CloseForSnapshot session drain at %s took %v\n", h.path, d)
	}

	// At this point, ALL sessions are either fully closed or sitting in pendingSessions.
	// No new sessions can start. It is safe to grab pending sessions.
	pending := h.pendingSessions
	h.pendingSessions = nil
	h.sessionsMu.Unlock()

	// 3. Acquire exclusive lock to prevent any Reads while we close
	h.mu.Lock()

	h.LockCommitPayload()
	for _, fs := range pending {
		if fs != nil {
			fs.mu.Lock()
			ptr := fs.ptr
			if ptr != nil {
				fmt.Printf("nomt_ffi: forcefully flushing pending finished session at %s before snapshot\n", h.path)
				C.nomt_commit_payload(h.ptr, ptr)
				fs.ptr = nil
			}
			fs.mu.Unlock()
		}
	}
	h.UnlockCommitPayload()

	// 4. Forcefully close NOMT. Since there are NO active Go Sessions holding
	// Arc<Core> references, nomt_close will fully drop the database synchronously,
	// flushing all WALs and memory mapped files safely.
	if h.stateDbPtr != nil {
		C.state_db_close(h.stateDbPtr)
		h.stateDbPtr = nil
	}
	if h.ptr != nil {
		C.nomt_close(h.ptr)
		h.ptr = nil
	}
	h.mu.Unlock()
}

// ReopenAfterSnapshot reopens the database using the previously saved path and config,
// and releases the lock acquired by `CloseForSnapshot()`.
// Includes retry logic because the OS may not have fully released the NOMT directory
// lock file immediately after nomt_close() returns (e.g., background compaction threads
// still cleaning up).
func (h *Handle) ReopenAfterSnapshot() error {
	cPath := C.CString(h.path)
	defer C.free(unsafe.Pointer(cPath))

	// CRITICAL FIX: Remove stale lock file before reopening.
	// After nomt_close(), leaked Go Session objects may still hold Rust Arc<Core>
	// references that keep the OS-level flock() alive on the old file descriptor.
	// Since PauseExecution() + WaitForPersistence() guarantees exclusive single-process
	// access at this point, removing the lock file is safe — the new nomt_open() will
	// create a fresh lock file on a new inode, bypassing the stale flock entirely.
	lockFile := h.path + "/.lock"
	_ = os.Remove(lockFile)

	var ptr *C.NomtHandle
	var stateDbPtr *C.RustStateDBHandle
	preallocVal := 0
	if h.preallocate {
		preallocVal = 1
	}
	// Retry a few times as a safety net (filesystem flush delay, etc.)
	for i := 0; i < 10; i++ {
		ptr = C.nomt_open(cPath, C.int(h.commitConcurrency), C.int(h.pageCacheMB), C.int(h.leafCacheMB), C.int(h.hashtableBuckets), C.int(preallocVal))
		if ptr != nil {
			stateDbPtr = C.state_db_open_from_handle(ptr, cPath)
			if stateDbPtr != nil {
				if i > 0 {
					fmt.Printf("nomt_ffi: successfully reopened database at %s after %d retries\n", h.path, i)
				}
				break
			}
			C.nomt_close(ptr)
			ptr = nil
		}
		// Try removing lock file again in case it was recreated
		_ = os.Remove(lockFile)
		time.Sleep(100 * time.Millisecond)
	}

	if ptr == nil || stateDbPtr == nil {
		return fmt.Errorf("nomt_ffi: failed to reopen database after snapshot at %s even after retries", h.path)
	}
	h.ptr = ptr
	h.stateDbPtr = stateDbPtr

	// Reset closing state to allow new sessions
	h.sessionsMu.Lock()
	h.closing = false
	h.activeCount = 0 // pendingSessions were consumed
	h.activeCond.Broadcast()
	h.sessionsMu.Unlock()

	return nil
}

// Checkpoint creates a point-in-time copy of the NOMT database files to destPath
// WITHOUT closing or reopening the database. This is much faster than CloseForSnapshot
// + copy + ReopenAfterSnapshot (~700ms saved per call) and eliminates os error 11
// lock contention issues entirely.
//
// SAFETY: The caller MUST ensure:
//  1. PauseExecution() — no active sessions
//  2. WaitForPersistence() — all disk I/O flushed
//  3. No concurrent reads/writes (this method acquires h.mu.Lock internally)
func (h *Handle) Checkpoint(destPath string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.ptr == nil {
		return fmt.Errorf("nomt_ffi: handle closed, cannot checkpoint")
	}

	// Drain any pending finished sessions first to ensure all data is on disk
	h.sessionsMu.Lock()
	pending := h.pendingSessions
	h.pendingSessions = nil
	h.sessionsMu.Unlock()

	for _, fs := range pending {
		if fs != nil {
			fs.mu.Lock()
			ptr := fs.ptr
			if ptr != nil {
				fmt.Printf("nomt_ffi: flushing pending session before checkpoint at %s\n", h.path)
				C.nomt_commit_payload(h.ptr, ptr)
				fs.ptr = nil
			}
			fs.mu.Unlock()
		}
	}

	cSrc := C.CString(h.path)
	defer C.free(unsafe.Pointer(cSrc))
	cDest := C.CString(destPath)
	defer C.free(unsafe.Pointer(cDest))

	ret := C.nomt_checkpoint(h.ptr, cSrc, cDest)
	if ret != 0 {
		return fmt.Errorf("nomt_ffi: checkpoint failed for %s -> %s", h.path, destPath)
	}
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

// Stats holds NOMT's own internal diagnostics for a database instance:
// page cache hit/miss counters, average page/value fetch latency, and
// hashtable (bitbox) bucket occupancy. See nomt_get_stats in the Rust FFI.
type Stats struct {
	PageRequests     uint64
	PageCacheMisses  uint64
	PageFetchTimeNs  uint64
	ValueFetchTimeNs uint64
	HTCapacity       uint64
	HTOccupied       uint64
}

// PageCacheMissRate returns the fraction of page requests that missed NOMT's
// in-memory page cache, in [0,1]. Returns 0 if there were no requests yet.
func (s Stats) PageCacheMissRate() float64 {
	if s.PageRequests == 0 {
		return 0
	}
	return float64(s.PageCacheMisses) / float64(s.PageRequests)
}

// HTOccupancyRate returns the hashtable (bitbox) bucket occupancy in [0,1].
// NOMT's own docs note performance likely degrades beyond 0.9.
func (s Stats) HTOccupancyRate() float64 {
	if s.HTCapacity == 0 {
		return 0
	}
	return float64(s.HTOccupied) / float64(s.HTCapacity)
}

// Stats reads NOMT's own internal metrics/utilization counters for this
// database instance. Metrics collection is enabled unconditionally in
// nomt_open, so this is always populated (page/value fetch timers read 0
// until at least one request has been recorded).
func (h *Handle) Stats() (Stats, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var s Stats
	ret := C.nomt_get_stats(
		h.ptr,
		(*C.uint64_t)(&s.PageRequests),
		(*C.uint64_t)(&s.PageCacheMisses),
		(*C.uint64_t)(&s.PageFetchTimeNs),
		(*C.uint64_t)(&s.ValueFetchTimeNs),
		(*C.uint64_t)(&s.HTCapacity),
		(*C.uint64_t)(&s.HTOccupied),
	)
	if ret != 0 {
		return s, fmt.Errorf("nomt_ffi: failed to get stats")
	}
	return s, nil
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

	h.sessionsMu.Lock()
	for h.closing || h.activeCount > 0 {
		h.activeCond.Wait()
	}
	h.activeCount++
	h.sessionsMu.Unlock()

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.ptr == nil {
		// Rollback active count if ptr is nil
		h.sessionsMu.Lock()
		h.activeCount--
		h.activeCond.Broadcast()
		h.sessionsMu.Unlock()
		return nil
	}
	ptr := C.nomt_session_begin(h.ptr)
	if ptr == nil {
		h.sessionsMu.Lock()
		h.activeCount--
		h.activeCond.Broadcast()
		h.sessionsMu.Unlock()
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

// BatchRecordRead records multiple read records to the session in a single FFI call.
// This is the high-performance path.
func (s *Session) BatchRecordRead(keys [][32]byte, values [][]byte) error {
	if s.ptr == nil || s.handle == nil {
		return fmt.Errorf("nomt_ffi: invalid session")
	}
	if len(keys) != len(values) {
		return fmt.Errorf("nomt_ffi: BatchRecordRead keys/values length mismatch (%d vs %d)", len(keys), len(values))
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

	keysBufPtr := keysBufPool.Get().(*[]byte)
	keysBuf := *keysBufPtr
	if cap(keysBuf) < n*32 {
		keysBuf = make([]byte, n*32)
	}
	flatKeys := keysBuf[:n*32]
	for i, k := range keys {
		copy(flatKeys[i*32:], k[:])
	}

	totalValsLen := 0
	for _, v := range values {
		totalValsLen += len(v)
	}

	var flatValsPtr *C.uint8_t
	valsBufPtr := valsBufPool.Get().(*[]byte)
	valsBuf := *valsBufPtr
	if cap(valsBuf) < totalValsLen {
		valsBuf = make([]byte, totalValsLen)
	}
	flatValues := valsBuf[:totalValsLen]

	if totalValsLen > 0 {
		flatValsPtr = (*C.uint8_t)(&flatValues[0])
	}

	valLensPtr := valLensPool.Get().(*[]C.size_t)
	valLens := *valLensPtr
	if cap(valLens) < n {
		valLens = make([]C.size_t, n)
	}
	valLens = valLens[:n]

	offset := 0
	for i, v := range values {
		l := len(v)
		if l > 0 {
			copy(flatValues[offset:], v)
			offset += l
		}
		valLens[i] = C.size_t(l)
	}

	ret := C.nomt_session_batch_record_read(
		s.ptr,
		(*C.uint8_t)(&flatKeys[0]),
		flatValsPtr,
		(*C.size_t)(&valLens[0]),
		C.size_t(n),
	)

	*keysBufPtr = keysBuf
	keysBufPool.Put(keysBufPtr)
	*valsBufPtr = valsBuf
	valsBufPool.Put(valsBufPtr)
	*valLensPtr = valLens
	valLensPool.Put(valLensPtr)

	if ret != 0 {
		return fmt.Errorf("nomt_ffi: batch_record_read error for %d entries", n)
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

	keysBufPtr := keysBufPool.Get().(*[]byte)
	keysBuf := *keysBufPtr
	if cap(keysBuf) < n*32 {
		keysBuf = make([]byte, n*32)
	}
	flatKeys := keysBuf[:n*32]
	for i, k := range keys {
		copy(flatKeys[i*32:], k[:])
	}

	totalValsLen := 0
	for _, v := range values {
		totalValsLen += len(v)
	}

	var flatValsPtr *C.uint8_t
	valsBufPtr := valsBufPool.Get().(*[]byte)
	valsBuf := *valsBufPtr
	if cap(valsBuf) < totalValsLen {
		valsBuf = make([]byte, totalValsLen)
	}
	flatValues := valsBuf[:totalValsLen]

	if totalValsLen > 0 {
		flatValsPtr = (*C.uint8_t)(&flatValues[0])
	}

	valLensPtr := valLensPool.Get().(*[]C.size_t)
	valLens := *valLensPtr
	if cap(valLens) < n {
		valLens = make([]C.size_t, n)
	}
	valLens = valLens[:n]

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

	*keysBufPtr = keysBuf
	keysBufPool.Put(keysBufPtr)
	*valsBufPtr = valsBuf
	valsBufPool.Put(valsBufPtr)
	*valLensPtr = valLens
	valLensPool.Put(valLensPtr)

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

	// Active session finished
	h.sessionsMu.Lock()
	h.activeCount--
	h.activeCond.Broadcast()
	h.sessionsMu.Unlock()

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
		s.ptr = nil
		h.sessionsMu.Lock()
		h.activeCount--
		h.activeCond.Broadcast()
		h.sessionsMu.Unlock()
		return [32]byte{}, nil, fmt.Errorf("nomt_ffi: handle closed")
	}

	var newRoot [32]byte

	ptr := C.nomt_session_finish(
		h.ptr,
		s.ptr,
		(*C.uint8_t)(&newRoot[0]),
	)
	// NOTE: We DO NOT decrement activeCount here, because the returned
	// FinishedSession still holds an Arc<Core> reference and represents an active session.
	s.ptr = nil // session consumed

	if ptr == nil {
		// On error, the session is consumed but we failed to get a FinishedSession
		h.sessionsMu.Lock()
		h.activeCount--
		h.activeCond.Broadcast()
		h.sessionsMu.Unlock()
		return newRoot, nil, fmt.Errorf("nomt_ffi: failed to finish session")
	}

	fs := &FinishedSession{ptr: ptr, handle: h}
	h.sessionsMu.Lock()
	h.pendingSessions = append(h.pendingSessions, fs)
	h.activeCond.Broadcast()
	h.sessionsMu.Unlock()

	return newRoot, fs, nil
}

// CommitPayload performs the actual disk I/O to persist a FinishedSession.
// This is typically called from a background worker thread.
func (fs *FinishedSession) CommitPayload(h *Handle) error {
	// Fast lock to ensure Handle isn't closed during commit
	h.mu.RLock()
	defer h.mu.RUnlock()

	fs.mu.Lock()
	ptr := fs.ptr
	if ptr == nil {
		fs.mu.Unlock()
		return nil // already consumed by CloseForSnapshot or earlier commit
	}

	if h.ptr == nil {
		fs.ptr = nil
		fs.mu.Unlock()
		h.sessionsMu.Lock()
		for i, pfs := range h.pendingSessions {
			if pfs == fs {
				h.pendingSessions = append(h.pendingSessions[:i], h.pendingSessions[i+1:]...)
				break
			}
		}
		h.activeCount--
		h.activeCond.Broadcast()
		h.sessionsMu.Unlock()
		return fmt.Errorf("nomt_ffi: handle closed")
	}

	ret := C.nomt_commit_payload(h.ptr, ptr)
	fs.ptr = nil
	fs.mu.Unlock()

	h.sessionsMu.Lock()
	for i, pfs := range h.pendingSessions {
		if pfs == fs {
			h.pendingSessions = append(h.pendingSessions[:i], h.pendingSessions[i+1:]...)
			break
		}
	}

	if ret != 0 {
		h.activeCount--
		h.activeCond.Broadcast()
		h.sessionsMu.Unlock()
		return fmt.Errorf("nomt_ffi: failed to commit payload")
	}

	h.activeCount--
	h.activeCond.Broadcast()
	h.sessionsMu.Unlock()

	return nil
}

// Abort discards an uncommitted session.
func (s *Session) Abort() {
	if s.ptr != nil && s.handle != nil {
		s.handle.mu.RLock()
		if s.handle.ptr != nil {
			C.nomt_session_abort(s.ptr)
		}
		s.handle.mu.RUnlock()
		s.ptr = nil

		s.handle.sessionsMu.Lock()
		s.handle.activeCount--
		s.handle.activeCond.Broadcast()
		s.handle.sessionsMu.Unlock()
	}
}

// Abort discards an uncommitted finished session.
func (fs *FinishedSession) Abort() {
	if fs.ptr != nil && fs.handle != nil {
		fs.mu.Lock()
		ptr := fs.ptr
		if ptr == nil {
			fs.mu.Unlock()
			return
		}
		fs.ptr = nil
		h := fs.handle
		fs.mu.Unlock()

		h.mu.RLock()
		if h.ptr != nil {
			C.nomt_finished_session_abort(ptr)
		}
		h.mu.RUnlock()

		h.sessionsMu.Lock()
		for i, pfs := range h.pendingSessions {
			if pfs == fs {
				h.pendingSessions = append(h.pendingSessions[:i], h.pendingSessions[i+1:]...)
				break
			}
		}
		h.activeCount--
		h.activeCond.Broadcast()
		h.sessionsMu.Unlock()
	}
}

// GenerateProof generates a Merkle proof for a given key.
func (h *Handle) GenerateProof(key [32]byte) ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var proofPtr *C.uint8_t
	var proofLen C.size_t

	keyPtr := (*C.uint8_t)(unsafe.Pointer(&key[0]))

	res := C.nomt_generate_proof(h.ptr, keyPtr, &proofPtr, &proofLen)
	if res != 0 {
		return nil, fmt.Errorf("nomt_generate_proof failed")
	}

	if proofPtr == nil || proofLen == 0 {
		return nil, nil // Or return empty slice
	}

	defer C.nomt_free_proof(proofPtr, proofLen)

	return C.GoBytes(unsafe.Pointer(proofPtr), C.int(proofLen)), nil
}

// ─── UNIFIED STATE DB METHODS (SIMPLIFIED BRIDGE) ────────────────────────────

// StateDbRoot returns the current Merkle root of the state database.
func (h *Handle) StateDbRoot() ([32]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.stateDbPtr == nil {
		return [32]byte{}, fmt.Errorf("state_db not open")
	}
	var root [32]byte
	ret := C.state_db_root(h.stateDbPtr, (*C.uint8_t)(unsafe.Pointer(&root[0])))
	if ret != 0 {
		return root, fmt.Errorf("state_db_root failed")
	}
	return root, nil
}

// StateDbGet reads a value from the state database for a key.
func (h *Handle) StateDbGet(key [32]byte) ([]byte, bool, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.stateDbPtr == nil {
		return nil, false, fmt.Errorf("state_db not open")
	}
	bufPtr := readBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer readBufPool.Put(bufPtr)

	var actualLen C.size_t
	ret := C.state_db_get(
		h.stateDbPtr,
		(*C.uint8_t)(unsafe.Pointer(&key[0])),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.size_t(len(buf)),
		&actualLen,
	)

	if ret == 0 && int(actualLen) > len(buf) {
		buf = make([]byte, int(actualLen))
		ret = C.state_db_get(
			h.stateDbPtr,
			(*C.uint8_t)(unsafe.Pointer(&key[0])),
			(*C.uint8_t)(unsafe.Pointer(&buf[0])),
			C.size_t(len(buf)),
			&actualLen,
		)
	}

	switch ret {
	case 0:
		if int(actualLen) > len(buf) {
			return nil, false, fmt.Errorf("state_db_get: buffer still too small")
		}
		res := make([]byte, actualLen)
		copy(res, buf[:actualLen])
		return res, true, nil
	case 1:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("state_db_get error for key %x", key[:8])
	}
}

// StateDbCommit batch commits writes and reads to the trie, returning the new Merkle root.
func (h *Handle) StateDbCommit(writes [][32]byte, writeVals [][]byte, reads [][32]byte, readVals [][]byte) ([32]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stateDbPtr == nil {
		return [32]byte{}, fmt.Errorf("state_db not open")
	}

	// Flatten writes
	writeCount := len(writes)
	var flatWriteKeys []byte
	var flatWriteVals []byte
	var writeValLens []C.size_t

	if writeCount > 0 {
		flatWriteKeys = make([]byte, writeCount*32)
		for i, k := range writes {
			copy(flatWriteKeys[i*32:], k[:])
		}
		totalValsLen := 0
		for _, v := range writeVals {
			totalValsLen += len(v)
		}
		flatWriteVals = make([]byte, totalValsLen)
		writeValLens = make([]C.size_t, writeCount)
		offset := 0
		for i, v := range writeVals {
			l := len(v)
			if l > 0 {
				copy(flatWriteVals[offset:], v)
				offset += l
			}
			writeValLens[i] = C.size_t(l)
		}
	}

	// Flatten reads
	readCount := len(reads)
	var flatReadKeys []byte
	var flatReadVals []byte
	var readValLens []C.size_t

	if readCount > 0 {
		flatReadKeys = make([]byte, readCount*32)
		for i, k := range reads {
			copy(flatReadKeys[i*32:], k[:])
		}
		totalValsLen := 0
		for _, v := range readVals {
			totalValsLen += len(v)
		}
		flatReadVals = make([]byte, totalValsLen)
		readValLens = make([]C.size_t, readCount)
		offset := 0
		for i, v := range readVals {
			l := len(v)
			if l > 0 {
				copy(flatReadVals[offset:], v)
				offset += l
			}
			readValLens[i] = C.size_t(l)
		}
	}

	var root [32]byte
	var writeKeysPtr, writeValsPtr, readKeysPtr, readValsPtr *C.uint8_t
	var writeLensPtr, readLensPtr *C.size_t

	if writeCount > 0 {
		writeKeysPtr = (*C.uint8_t)(unsafe.Pointer(&flatWriteKeys[0]))
		if len(flatWriteVals) > 0 {
			writeValsPtr = (*C.uint8_t)(unsafe.Pointer(&flatWriteVals[0]))
		}
		writeLensPtr = (*C.size_t)(unsafe.Pointer(&writeValLens[0]))
	}
	if readCount > 0 {
		readKeysPtr = (*C.uint8_t)(unsafe.Pointer(&flatReadKeys[0]))
		if len(flatReadVals) > 0 {
			readValsPtr = (*C.uint8_t)(unsafe.Pointer(&flatReadVals[0]))
		}
		readLensPtr = (*C.size_t)(unsafe.Pointer(&readValLens[0]))
	}

	ret := C.state_db_commit(
		h.stateDbPtr,
		writeKeysPtr,
		writeValsPtr,
		writeLensPtr,
		C.size_t(writeCount),
		readKeysPtr,
		readValsPtr,
		readLensPtr,
		C.size_t(readCount),
		(*C.uint8_t)(unsafe.Pointer(&root[0])),
	)

	if ret != 0 {
		return root, fmt.Errorf("state_db_commit failed")
	}
	return root, nil
}

// StateDbCheckpoint checkpoints the database to a destination path.
func (h *Handle) StateDbCheckpoint(destPath string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stateDbPtr == nil {
		return fmt.Errorf("state_db not open")
	}
	cDest := C.CString(destPath)
	defer C.free(unsafe.Pointer(cDest))
	ret := C.state_db_checkpoint(h.stateDbPtr, cDest)
	if ret != 0 {
		return fmt.Errorf("state_db_checkpoint failed")
	}
	return nil
}

// StateDbPrune prunes database history older than epoch.
func (h *Handle) StateDbPrune(oldEpoch uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stateDbPtr == nil {
		return fmt.Errorf("state_db not open")
	}
	ret := C.state_db_prune(h.stateDbPtr, C.uint64_t(oldEpoch))
	if ret != 0 {
		return fmt.Errorf("state_db_prune failed")
	}
	return nil
}
