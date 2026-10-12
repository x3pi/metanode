package raftfeed

import (
	"sync/atomic"
	"time"
)

// Metrics collects internal performance counters and timings for a Raft replica.
type Metrics struct {
	// Counters
	StatusFullCount   atomic.Uint64
	SplitBatchCount   atomic.Uint64
	PiecesCreated     atomic.Uint64
	ProposalsEnqueued atomic.Uint64
	ProposalsDequeued atomic.Uint64
	EntriesApplied    atomic.Uint64
	TotalTxs          atomic.Uint64

	// Queue depth
	ProposeQDepthMax atomic.Uint64
	ProposeQDepthSum atomic.Uint64

	// Latencies (nanoseconds)
	ProposeQWaitNsTotal atomic.Uint64
	ProposeQWaitCount   atomic.Uint64
	ProposeQWaitMaxNs   atomic.Uint64

	RaftApplyNsTotal atomic.Uint64
	RaftApplyCount   atomic.Uint64
	RaftApplyMaxNs   atomic.Uint64

	FsmDecodeNsTotal   atomic.Uint64
	FsmBuildNsTotal    atomic.Uint64
	FsmSinkWaitNsTotal atomic.Uint64
	FsmApplyCount      atomic.Uint64

	BoltStoreNsTotal   atomic.Uint64
	BoltStoreCount     atomic.Uint64
	BoltStoreLogsTotal atomic.Uint64
	BoltStoreMaxNs     atomic.Uint64
}

// MetricsSnapshot is the JSON-serializable snapshot of Metrics.
type MetricsSnapshot struct {
	StatusFullCount    uint64  `json:"status_full_count"`
	SplitBatchCount    uint64  `json:"split_batch_count"`
	PiecesCreated      uint64  `json:"pieces_created"`
	ProposalsEnqueued  uint64  `json:"proposals_enqueued"`
	ProposalsDequeued  uint64  `json:"proposals_dequeued"`
	EntriesApplied     uint64  `json:"entries_applied"`
	TotalTxs           uint64  `json:"total_txs"`
	ProposeQDepthMax   uint64  `json:"propose_q_depth_max"`
	ProposeQDepthAvg   float64 `json:"propose_q_depth_avg"`
	AvgProposeQWaitMs  float64 `json:"avg_propose_q_wait_ms"`
	MaxProposeQWaitMs  float64 `json:"max_propose_q_wait_ms"`
	AvgRaftApplyMs     float64 `json:"avg_raft_apply_ms"`
	MaxRaftApplyMs     float64 `json:"max_raft_apply_ms"`
	AvgFsmDecodeMs     float64 `json:"avg_fsm_decode_ms"`
	AvgFsmBuildMs      float64 `json:"avg_fsm_build_ms"`
	AvgFsmSinkWaitMs   float64 `json:"avg_fsm_sink_wait_ms"`
	AvgBoltStoreMs     float64 `json:"avg_bolt_store_ms"`
	MaxBoltStoreMs     float64 `json:"max_bolt_store_ms"`
	TotalBoltStoreLogs uint64  `json:"total_bolt_store_logs"`
}

func (m *Metrics) RecordStatusFull() {
	if m == nil {
		return
	}
	m.StatusFullCount.Add(1)
}

func (m *Metrics) RecordSplit(pieces int) {
	if m == nil {
		return
	}
	m.SplitBatchCount.Add(1)
	m.PiecesCreated.Add(uint64(pieces))
}

func (m *Metrics) RecordEnqueue(depth int) {
	if m == nil {
		return
	}
	d := uint64(depth)
	m.ProposalsEnqueued.Add(1)
	m.ProposeQDepthSum.Add(d)
	for {
		cur := m.ProposeQDepthMax.Load()
		if d <= cur || m.ProposeQDepthMax.CompareAndSwap(cur, d) {
			break
		}
	}
}

func (m *Metrics) RecordQueueWait(dur time.Duration) {
	if m == nil {
		return
	}
	ns := uint64(dur.Nanoseconds())
	m.ProposalsDequeued.Add(1)
	m.ProposeQWaitNsTotal.Add(ns)
	m.ProposeQWaitCount.Add(1)
	for {
		cur := m.ProposeQWaitMaxNs.Load()
		if ns <= cur || m.ProposeQWaitMaxNs.CompareAndSwap(cur, ns) {
			break
		}
	}
}

func (m *Metrics) RecordRaftApply(dur time.Duration) {
	if m == nil {
		return
	}
	ns := uint64(dur.Nanoseconds())
	m.RaftApplyNsTotal.Add(ns)
	m.RaftApplyCount.Add(1)
	for {
		cur := m.RaftApplyMaxNs.Load()
		if ns <= cur || m.RaftApplyMaxNs.CompareAndSwap(cur, ns) {
			break
		}
	}
}

func (m *Metrics) RecordFsmApply(decodeDur, buildDur, sinkDur time.Duration, txs int) {
	if m == nil {
		return
	}
	m.EntriesApplied.Add(1)
	m.TotalTxs.Add(uint64(txs))
	m.FsmDecodeNsTotal.Add(uint64(decodeDur.Nanoseconds()))
	m.FsmBuildNsTotal.Add(uint64(buildDur.Nanoseconds()))
	m.FsmSinkWaitNsTotal.Add(uint64(sinkDur.Nanoseconds()))
	m.FsmApplyCount.Add(1)
}

func (m *Metrics) RecordBoltStore(dur time.Duration, logCount int) {
	if m == nil {
		return
	}
	ns := uint64(dur.Nanoseconds())
	m.BoltStoreNsTotal.Add(ns)
	m.BoltStoreCount.Add(1)
	m.BoltStoreLogsTotal.Add(uint64(logCount))
	for {
		cur := m.BoltStoreMaxNs.Load()
		if ns <= cur || m.BoltStoreMaxNs.CompareAndSwap(cur, ns) {
			break
		}
	}
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	enq := m.ProposalsEnqueued.Load()
	deq := m.ProposalsDequeued.Load()
	qWaitCnt := m.ProposeQWaitCount.Load()
	qWaitNs := m.ProposeQWaitNsTotal.Load()
	qMaxNs := m.ProposeQWaitMaxNs.Load()

	applyCnt := m.RaftApplyCount.Load()
	applyNs := m.RaftApplyNsTotal.Load()
	applyMaxNs := m.RaftApplyMaxNs.Load()

	fsmCnt := m.FsmApplyCount.Load()
	fsmDecodeNs := m.FsmDecodeNsTotal.Load()
	fsmBuildNs := m.FsmBuildNsTotal.Load()
	fsmSinkNs := m.FsmSinkWaitNsTotal.Load()

	boltCnt := m.BoltStoreCount.Load()
	boltNs := m.BoltStoreNsTotal.Load()
	boltMaxNs := m.BoltStoreMaxNs.Load()

	var avgDepth float64
	if enq > 0 {
		avgDepth = float64(m.ProposeQDepthSum.Load()) / float64(enq)
	}

	var avgQWaitMs float64
	if qWaitCnt > 0 {
		avgQWaitMs = float64(qWaitNs) / float64(qWaitCnt) / 1e6
	}

	var avgApplyMs float64
	if applyCnt > 0 {
		avgApplyMs = float64(applyNs) / float64(applyCnt) / 1e6
	}

	var avgFsmDecodeMs, avgFsmBuildMs, avgFsmSinkMs float64
	if fsmCnt > 0 {
		avgFsmDecodeMs = float64(fsmDecodeNs) / float64(fsmCnt) / 1e6
		avgFsmBuildMs = float64(fsmBuildNs) / float64(fsmCnt) / 1e6
		avgFsmSinkMs = float64(fsmSinkNs) / float64(fsmCnt) / 1e6
	}

	var avgBoltMs float64
	if boltCnt > 0 {
		avgBoltMs = float64(boltNs) / float64(boltCnt) / 1e6
	}

	return MetricsSnapshot{
		StatusFullCount:    m.StatusFullCount.Load(),
		SplitBatchCount:    m.SplitBatchCount.Load(),
		PiecesCreated:      m.PiecesCreated.Load(),
		ProposalsEnqueued:  enq,
		ProposalsDequeued:  deq,
		EntriesApplied:     m.EntriesApplied.Load(),
		TotalTxs:           m.TotalTxs.Load(),
		ProposeQDepthMax:   m.ProposeQDepthMax.Load(),
		ProposeQDepthAvg:   avgDepth,
		AvgProposeQWaitMs:  avgQWaitMs,
		MaxProposeQWaitMs:  float64(qMaxNs) / 1e6,
		AvgRaftApplyMs:     avgApplyMs,
		MaxRaftApplyMs:     float64(applyMaxNs) / 1e6,
		AvgFsmDecodeMs:     avgFsmDecodeMs,
		AvgFsmBuildMs:      avgFsmBuildMs,
		AvgFsmSinkWaitMs:   avgFsmSinkMs,
		AvgBoltStoreMs:     avgBoltMs,
		MaxBoltStoreMs:     float64(boltMaxNs) / 1e6,
		TotalBoltStoreLogs: m.BoltStoreLogsTotal.Load(),
	}
}
