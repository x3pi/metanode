// Package pipeline — PipelineStats tracks atomic counters at each stage of the TX pipeline.
// Extracted from processor/pipeline_stats.go.
package pipeline

import (
	"encoding/json"
	"sync/atomic"
	"time"
)

// PipelineStats tracks atomic counters at each stage of the TX pipeline.
// Thread-safe via sync/atomic — no locks needed.
type PipelineStats struct {
	// Stage 1: TXs received by this node (from clients or network)
	TxsReceived atomic.Int64

	// Stage 2: TXs currently in transaction pool (set, not incremented)
	PoolSize atomic.Int64

	// Stage 3: TXs in pendingTxManager (set, not incremented)
	PendingSize atomic.Int64

	// Stage 4: TXs forwarded to Rust via UDS (cumulative)
	TxsForwarded atomic.Int64

	// Stage 5: Blocks received from Rust (cumulative)
	BlocksReceived atomic.Int64

	// Stage 6: TXs committed in blocks (cumulative)
	TxsCommitted atomic.Int64

	// Stage 7: Latest block number
	LastBlock atomic.Int64

	// Stage 8: Last block commit time in microseconds
	LastCommitTimeUs atomic.Int64

	// Timing
	StartTime time.Time

	// Node role (sub/master)
	NodeRole string
}

// PipelineSnapshot is the JSON-serializable snapshot of pipeline stats.
type PipelineSnapshot struct {
	NodeRole       string  `json:"node_role"`
	UptimeSeconds  float64 `json:"uptime_seconds"`
	TxsReceived    int64   `json:"txs_received"`
	PoolSize       int64   `json:"pool_size"`
	PendingSize    int64   `json:"pending_size"`
	TxsForwarded   int64   `json:"txs_forwarded"`
	BlocksReceived int64   `json:"blocks_received"`
	TxsCommitted   int64   `json:"txs_committed"`
	LastBlock      int64   `json:"last_block"`
	LastCommitUs   int64   `json:"last_commit_us"`
	Timestamp      string  `json:"timestamp"`
}

// GlobalPipelineStats is the singleton instance used across the node.
var GlobalPipelineStats = &PipelineStats{
	StartTime: time.Now(),
}

// LastSendBatchTimeNano stores the Unix nanosecond timestamp of the last time
// TxsProcessor2 successfully queued a batch to Rust via SendBatch.
var LastSendBatchTimeNano atomic.Int64

// LastSendBatchTxCount stores the total TX count of the last batch sent to Rust.
var LastSendBatchTxCount atomic.Int64

// SetNodeRole sets the node role (should be called once during initialization).
func (ps *PipelineStats) SetNodeRole(role string) {
	ps.NodeRole = role
}

// Snapshot returns a JSON-serializable snapshot of the current pipeline state.
func (ps *PipelineStats) Snapshot() PipelineSnapshot {
	return PipelineSnapshot{
		NodeRole:       ps.NodeRole,
		UptimeSeconds:  time.Since(ps.StartTime).Seconds(),
		TxsReceived:    ps.TxsReceived.Load(),
		PoolSize:       ps.PoolSize.Load(),
		PendingSize:    ps.PendingSize.Load(),
		TxsForwarded:   ps.TxsForwarded.Load(),
		BlocksReceived: ps.BlocksReceived.Load(),
		TxsCommitted:   ps.TxsCommitted.Load(),
		LastBlock:      ps.LastBlock.Load(),
		LastCommitUs:   ps.LastCommitTimeUs.Load(),
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// SnapshotJSON returns the pipeline snapshot as JSON bytes.
func (ps *PipelineStats) SnapshotJSON() ([]byte, error) {
	snap := ps.Snapshot()
	return json.Marshal(snap)
}

// Reset clears all counters (useful between test runs).
func (ps *PipelineStats) Reset() {
	ps.TxsReceived.Store(0)
	ps.PoolSize.Store(0)
	ps.PendingSize.Store(0)
	ps.TxsForwarded.Store(0)
	ps.BlocksReceived.Store(0)
	ps.TxsCommitted.Store(0)
	ps.LastBlock.Store(0)
	ps.LastCommitTimeUs.Store(0)
	ps.StartTime = time.Now()
}

// --- Convenience increment methods ---

func (ps *PipelineStats) IncrTxsReceived(n int64)      { ps.TxsReceived.Add(n) }
func (ps *PipelineStats) IncrTxsForwarded(n int64)     { ps.TxsForwarded.Add(n) }
func (ps *PipelineStats) IncrBlocksReceived(n int64)   { ps.BlocksReceived.Add(n) }
func (ps *PipelineStats) IncrTxsCommitted(n int64)     { ps.TxsCommitted.Add(n) }
func (ps *PipelineStats) SetPoolSize(n int64)          { ps.PoolSize.Store(n) }
func (ps *PipelineStats) SetPendingSize(n int64)       { ps.PendingSize.Store(n) }
func (ps *PipelineStats) SetLastBlock(n int64)         { ps.LastBlock.Store(n) }
func (ps *PipelineStats) SetLastCommitTimeUs(us int64) { ps.LastCommitTimeUs.Store(us) }
