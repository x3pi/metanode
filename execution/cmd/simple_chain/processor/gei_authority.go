// @title processor/gei_authority.go
// @markdown processor/gei_authority.go - Centralized Authoritative GEI Assignment
//
// GO-AUTHORITATIVE GEI: This module is the Single Source of Truth for
// Global Execution Index (GEI) assignment. When authoritative mode is
// enabled, Go internally assigns GEI values instead of trusting Rust.
//
// This eliminates the root cause of all fork bugs: Rust computed GEI from
// 3 fragile sources (epoch_base, commit_index, fragment_offset) that could
// diverge across nodes after restarts. Now Go atomically increments GEI
// and persists it — no reconstruction needed.
package processor

import (
	"sync"
	"sync/atomic"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// GEIAuthority is the centralized, thread-safe GEI assignment module.
// It maintains the authoritative GEI counter and provides atomic assignment.
type GEIAuthority struct {
	// lastAssignedGEI is the last GEI that was assigned to a block.
	// Atomically incremented for each new commit.
	lastAssignedGEI atomic.Uint64

	// lastHandledCommitIndex is the last Rust commit_index that Go processed.
	// Used for recovery: Rust asks "what commit_index did you last handle?"
	lastHandledCommitIndex atomic.Uint32

	// lastHandledCommitEpoch is the epoch when lastHandledCommitIndex was recorded.
	// CRITICAL FOR FORK PREVENTION: On restart, if current_epoch != lastHandledCommitEpoch,
	// the commit_index belongs to a previous epoch and MUST be treated as 0.
	lastHandledCommitEpoch atomic.Uint64

	// enabled controls whether Go-authoritative mode is active.
	// When false, falls back to Rust-computed GEI (legacy mode).
	enabled atomic.Bool

	// mu protects complex multi-step operations (e.g., AssignGEIRange)
	mu sync.Mutex
}

// Global singleton
var geiAuthority *GEIAuthority
var geiAuthorityOnce sync.Once

// GetGEIAuthority returns the global GEIAuthority singleton.
// Thread-safe, initialized once.
func GetGEIAuthority() *GEIAuthority {
	geiAuthorityOnce.Do(func() {
		geiAuthority = &GEIAuthority{}
		// Initialize from persistent storage
		lastGEI := storage.GetLastGlobalExecIndex()
		geiAuthority.lastAssignedGEI.Store(lastGEI)
		lastCommit := storage.GetLastHandledCommitIndex()
		geiAuthority.lastHandledCommitIndex.Store(lastCommit)
		lastEpoch := storage.GetLastHandledCommitEpoch()
		geiAuthority.lastHandledCommitEpoch.Store(lastEpoch)
		logger.Info("🔑 [GEI-AUTHORITY] Initialized: lastGEI=%d, lastCommit=%d, lastCommitEpoch=%d, mode=AUTHORITATIVE", lastGEI, lastCommit, lastEpoch)
	})
	return geiAuthority
}

// Enable activates Go-authoritative GEI mode.
func (ga *GEIAuthority) Enable() {
	ga.enabled.Store(true)
	logger.Info("🔑 [GEI-AUTHORITY] Go-Authoritative GEI mode ENABLED")
}

// Disable deactivates Go-authoritative GEI mode (falls back to Rust-computed).
func (ga *GEIAuthority) Disable() {
	ga.enabled.Store(false)
	logger.Info("🔑 [GEI-AUTHORITY] Go-Authoritative GEI mode DISABLED (legacy mode)")
}

// IsEnabled returns whether Go-authoritative GEI mode is active.
func (ga *GEIAuthority) IsEnabled() bool {
	return ga.enabled.Load()
}

// AssignGEI atomically assigns the next GEI for a single-block commit.
// Returns the assigned GEI.
//
// Thread-safe: multiple goroutines can call this concurrently.
// Monotonic: GEI only increases, never decreases.
func (ga *GEIAuthority) AssignGEI() uint64 {
	newGEI := ga.lastAssignedGEI.Add(1)
	return newGEI
}

// AssignGEIRange atomically assigns N consecutive GEIs for fragmented commits.
// Returns (startGEI, endGEI) inclusive range.
//
// Example: If lastAssignedGEI=100 and count=3, returns (101, 103)
// and lastAssignedGEI is updated to 103.
func (ga *GEIAuthority) AssignGEIRange(count uint64) (startGEI, endGEI uint64) {
	if count == 0 {
		lastGEI := ga.lastAssignedGEI.Load()
		return lastGEI, lastGEI
	}

	ga.mu.Lock()
	defer ga.mu.Unlock()

	currentGEI := ga.lastAssignedGEI.Load()
	startGEI = currentGEI + 1
	endGEI = currentGEI + count
	ga.lastAssignedGEI.Store(endGEI)

	return startGEI, endGEI
}

// AdvanceGEITo advances the GEI counter to at least the specified value.
// Used during initialization when Go's persisted GEI needs to catch up.
// No-op if current GEI is already >= target.
func (ga *GEIAuthority) AdvanceGEITo(target uint64) {
	for {
		current := ga.lastAssignedGEI.Load()
		if current >= target {
			return
		}
		if ga.lastAssignedGEI.CompareAndSwap(current, target) {
			logger.Info("🔑 [GEI-AUTHORITY] Advanced GEI: %d → %d", current, target)
			return
		}
	}
}

// RecordCommitIndex records the last handled Rust commit_index and its epoch.
// Called after successfully processing a commit from Rust.
func (ga *GEIAuthority) RecordCommitIndex(commitIndex uint32) {
	ga.lastHandledCommitIndex.Store(commitIndex)
}

// RecordCommitIndexWithEpoch records commit index paired with its epoch.
// FORK-SAFETY: Epoch pairing ensures restart validation can detect cross-epoch staleness.
func (ga *GEIAuthority) RecordCommitIndexWithEpoch(commitIndex uint32, epoch uint64) {
	ga.lastHandledCommitIndex.Store(commitIndex)
	ga.lastHandledCommitEpoch.Store(epoch)
}

// ResetCommitIndex forces the commit index back to 0 for a new epoch.
// Used exclusively during epoch transitions to prevent deduplication
// from incorrectly skipping commits in the new epoch.
func (ga *GEIAuthority) ResetCommitIndex() {
	ga.lastHandledCommitIndex.Store(0)
	logger.Info("🔑 [GEI-AUTHORITY] Reset lastHandledCommitIndex to 0 for new epoch")
}

// ResetCommitIndexForEpoch resets commit index and records the new epoch.
// FORK-SAFETY: This ensures the persisted epoch is always in sync with the commit index.
func (ga *GEIAuthority) ResetCommitIndexForEpoch(newEpoch uint64) {
	ga.lastHandledCommitIndex.Store(0)
	ga.lastHandledCommitEpoch.Store(newEpoch)
	logger.Info("🔑 [GEI-AUTHORITY] Reset lastHandledCommitIndex to 0 for epoch %d", newEpoch)
}

// GetLastAssignedGEI returns the last GEI that was assigned.
func (ga *GEIAuthority) GetLastAssignedGEI() uint64 {
	return ga.lastAssignedGEI.Load()
}

// GetLastHandledCommitIndex returns the last Rust commit_index that Go processed.
// Used for recovery: Rust asks Go "what's the last commit you handled?"
func (ga *GEIAuthority) GetLastHandledCommitIndex() uint32 {
	return ga.lastHandledCommitIndex.Load()
}

// GetLastHandledCommitEpoch returns the epoch when the last commit_index was recorded.
func (ga *GEIAuthority) GetLastHandledCommitEpoch() uint64 {
	return ga.lastHandledCommitEpoch.Load()
}

// GetCheckpointState returns (lastGEI, lastCommitIndex, lastCommitEpoch) as an atomic unit.
// FORK-SAFETY: These three values must be read together for deterministic restart.
func (ga *GEIAuthority) GetCheckpointState() (uint64, uint32, uint64) {
	ga.mu.Lock()
	defer ga.mu.Unlock()
	return ga.lastAssignedGEI.Load(), ga.lastHandledCommitIndex.Load(), ga.lastHandledCommitEpoch.Load()
}

// ReinitializeFromStorage re-reads all values from persistent storage.
// FORK-SAFETY: Call this after ForceSetLastGlobalExecIndex (e.g., snapshot verification)
// to ensure the singleton reflects the corrected values.
func (ga *GEIAuthority) ReinitializeFromStorage() {
	ga.mu.Lock()
	defer ga.mu.Unlock()
	lastGEI := storage.GetLastGlobalExecIndex()
	ga.lastAssignedGEI.Store(lastGEI)
	lastCommit := storage.GetLastHandledCommitIndex()
	ga.lastHandledCommitIndex.Store(lastCommit)
	lastEpoch := storage.GetLastHandledCommitEpoch()
	ga.lastHandledCommitEpoch.Store(lastEpoch)
	logger.Info("🔑 [GEI-AUTHORITY] Reinitialized from storage: lastGEI=%d, lastCommit=%d, lastCommitEpoch=%d", lastGEI, lastCommit, lastEpoch)
}

// PersistState persists the current GEI and commit_index to storage.
// Called periodically (not every commit) for crash recovery.
func (ga *GEIAuthority) PersistState() {
	gei := ga.lastAssignedGEI.Load()
	storage.UpdateLastGlobalExecIndex(gei)
}

