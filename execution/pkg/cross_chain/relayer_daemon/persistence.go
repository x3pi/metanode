package relayer_daemon

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// snapshotUnrelayedBatchesLocked returns a shallow copy of d.unrelayedBatches. Callers MUST
// already hold d.mu (Lock or RLock) -- this does no locking of its own, by design: it exists so
// callers can copy the map while holding the lock, then release it BEFORE doing disk I/O in
// persistUnrelayedBatches, matching this file's existing convention of never doing I/O/RPC calls
// while holding d.mu.
func (d *RelayerDaemon) snapshotUnrelayedBatchesLocked() map[string]*unrelayedBatch {
	snapshot := make(map[string]*unrelayedBatch, len(d.unrelayedBatches))
	for k, v := range d.unrelayedBatches {
		snapshot[k] = v
	}
	return snapshot
}

// persistUnrelayedBatches writes snapshot to config.UnrelayedBatchesPersistPath as JSON, replacing
// the file atomically (write to a temp file + rename, so a crash mid-write never leaves a
// truncated/corrupt file that loadUnrelayedBatches would choke on at the next startup). A no-op
// when the path is unset (default) -- preserves PR #102's original in-memory-only behavior exactly
// unless an operator opts in.
//
// PRODUCTION-READINESS FIX (2026-09-05, review of PR #102): unrelayedBatches previously lived only
// in RAM. That closes "destination chain restarts while the relayer PROCESS keeps running" (PR
// #102's actual reported bug) but NOT "the relayer's own process restarts while a batch is stuck
// unrelayed" -- at that point PendingOutboundMessages was already drained by the batchOutboundCommit
// that produced the stuck batch, so a fresh process's normal flow (which only ever looks at
// getPendingOutboundCount) can never rediscover it; the batch would sit committed-but-unclaimed
// forever with no automatic recovery. Persisting + reloading this small queue closes that gap too.
// Errors are logged, never fatal or returned -- persistence is a resiliency nice-to-have, not a
// correctness dependency (RelayBatch/ClaimMessage's on-chain write-once guards are what actually
// keep this system safe; see note/cross_chain_relayer_production_readiness.md), so a disk hiccup
// here must never block message relaying itself.
func (d *RelayerDaemon) persistUnrelayedBatches(snapshot map[string]*unrelayedBatch) {
	path := d.config.UnrelayedBatchesPersistPath
	if path == "" {
		return
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		logger.Warn("⚠️ [RELAYER DAEMON] marshal unrelayed batches for persistence: %v", err)
		return
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".unrelayed-batches-*.tmp")
	if err != nil {
		logger.Warn("⚠️ [RELAYER DAEMON] create temp file for unrelayed-batches persistence in %s: %v", dir, err)
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		logger.Warn("⚠️ [RELAYER DAEMON] write unrelayed-batches persistence file: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		logger.Warn("⚠️ [RELAYER DAEMON] close unrelayed-batches persistence temp file: %v", err)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		logger.Warn("⚠️ [RELAYER DAEMON] rename unrelayed-batches persistence file into place: %v", err)
	}
}

// loadUnrelayedBatches reads a previously-persisted unrelayedBatches snapshot from
// config.UnrelayedBatchesPersistPath, if the path is set and the file exists. Returns an empty,
// non-nil map (never an error) on any failure to read/parse -- a corrupt or missing persistence
// file must never prevent the daemon from starting; it just starts with no pending retries to
// resume, exactly like PR #102's original in-memory-only behavior did on every restart.
func loadUnrelayedBatches(path string) map[string]*unrelayedBatch {
	result := make(map[string]*unrelayedBatch)
	if path == "" {
		return result
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("⚠️ [RELAYER DAEMON] read unrelayed-batches persistence file %s: %v", path, err)
		}
		return result
	}
	if len(data) == 0 {
		return result
	}
	if err := json.Unmarshal(data, &result); err != nil {
		logger.Warn("⚠️ [RELAYER DAEMON] parse unrelayed-batches persistence file %s (starting with none, treating as first run): %v", path, err)
		return make(map[string]*unrelayedBatch)
	}
	if result == nil {
		result = make(map[string]*unrelayedBatch)
	}
	if len(result) > 0 {
		logger.Info("♻️ [RELAYER DAEMON] resumed %d unrelayed batch(es) from %s", len(result), path)
	}
	return result
}
