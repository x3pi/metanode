package raftfeed

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/raft"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	rpb "github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed/pb"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

const schemaVersion = 1

// durablePollInterval is only how often Snapshot re-checks a LOCAL durability counter; it never decides
// whether a batch is applied or dispatched.
const durablePollInterval = 5 * time.Millisecond

// applyResult is what Apply returns to the proposing leader's future (used by tests and diagnostics).
type applyResult struct {
	BlockNumber uint64
	Skipped     bool // block was already durable locally (replay after restart)
}

// fsm is the Raft state machine of consensus_mode="raft". Apply runs on every replica for every committed
// entry, in the same order, and builds the same ExecutableBlock from it (SEQUENCER_SCHEMAS §1.3).
//
// Determinism: Apply reads no clock and no randomness; everything comes from the BatchRecord and the counters
// restored from the snapshot. Zero-fork: an entry that cannot be applied exactly (bad schema, tx_count mismatch,
// empty batch, numbering exhausted) makes the replica STOP (onFatal) instead of skipping it — skipping would make
// this replica's chain differ from the others.
//
// The real state lives in NOMT/DB, so the Raft snapshot only carries counters, and Snapshot() waits until every
// block handed to the pipeline is durable before returning (see Snapshot).
type fsm struct {
	sink    chan<- *pb.ExecutableBlock
	durable func() uint64 // last block number durable in the DB
	stop    <-chan struct{}
	onFatal func(error)

	mu           sync.Mutex // guards the fields below; Apply/Snapshot/Restore are serial, readers are diagnostics
	st           stamper
	appliedIndex uint64
	delivered    uint64 // last block number handed to the sink

	failed  atomic.Bool
	skipped atomic.Uint64
	metrics *Metrics

	// snapshotBlock is the last block number covered by the newest snapshot this replica took or restored: a
	// replica joining with a DB older than that cannot be served from the leader's compacted log.
	snapshotBlock atomic.Uint64
}

func newFSM(st stamper, sink chan<- *pb.ExecutableBlock, durable func() uint64, stop <-chan struct{}, onFatal func(error)) *fsm {
	return &fsm{st: st, sink: sink, durable: durable, stop: stop, onFatal: onFatal}
}

// fatal marks the replica failed exactly once and tells the owner to stop participating.
func (f *fsm) fatal(err error) error {
	if f.failed.CompareAndSwap(false, true) {
		logger.Error("🚨 [RAFT-FSM] %v — this replica stops applying (fail closed, no guessing)", err)
		if f.onFatal != nil {
			go f.onFatal(err)
		}
	}
	return err
}

// Apply implements raft.FSM.
func (f *fsm) Apply(l *raft.Log) interface{} {
	if f.failed.Load() {
		return errors.New("raftfeed: replica failed, not applying")
	}
	t0 := time.Now()
	var rec rpb.BatchRecord
	if err := proto.Unmarshal(l.Data, &rec); err != nil {
		return f.fatal(fmt.Errorf("index %d: undecodable BatchRecord: %w", l.Index, err))
	}
	if rec.SchemaVersion != schemaVersion {
		return f.fatal(fmt.Errorf("index %d: unsupported BatchRecord schema %d", l.Index, rec.SchemaVersion))
	}
	txs, err := transaction.UnmarshalTransactions(rec.Txs)
	if err != nil {
		return f.fatal(fmt.Errorf("index %d: undecodable txs: %w", l.Index, err))
	}
	if len(txs) == 0 || uint32(len(txs)) != rec.TxCount {
		return f.fatal(fmt.Errorf("index %d: tx_count %d does not match %d decoded txs", l.Index, rec.TxCount, len(txs)))
	}
	tDecode := time.Since(t0)

	f.mu.Lock()
	defer f.mu.Unlock()
	t1 := time.Now()
	blk, err := f.st.build(rec.Txs, rec.TimestampMs)
	if err != nil {
		return f.fatal(fmt.Errorf("index %d: %w", l.Index, err))
	}
	tBuild := time.Since(t1)

	var tSink time.Duration
	res := applyResult{BlockNumber: blk.BlockNumber}
	if blk.BlockNumber <= f.durable() {
		// Raft replays entries after a restart; the block is already in the DB. Counters still advance so the
		// hash chain and numbering stay identical to the replicas that did not restart.
		res.Skipped = true
		f.skipped.Add(1)
	} else {
		t2 := time.Now()
		select {
		case f.sink <- blk: // bounded queue: blocks here (backpressure into Raft) when the pipeline is behind
			f.delivered = blk.BlockNumber
		case <-f.stop:
			return errors.New("raftfeed: stopping")
		}
		tSink = time.Since(t2)
	}
	f.st.advance(blk)
	f.appliedIndex = l.Index

	if f.metrics != nil {
		f.metrics.RecordFsmApply(tDecode, tBuild, tSink, len(txs))
		cnt := f.metrics.EntriesApplied.Load()
		if cnt > 0 && cnt%50 == 0 {
			snap := f.metrics.Snapshot()
			logger.Info("⏱️ [RAFT-METRICS] applied=%d txs=%d qDepthMax=%d qWaitAvg=%.2fms applyAvg=%.2fms fsmBuildAvg=%.2fms sinkWaitAvg=%.2fms boltFsyncAvg=%.2fms",
				snap.EntriesApplied, snap.TotalTxs, snap.ProposeQDepthMax, snap.AvgProposeQWaitMs, snap.AvgRaftApplyMs, snap.AvgFsmBuildMs, snap.AvgFsmSinkWaitMs, snap.AvgBoltStoreMs)
		}
	}
	return res
}

// Snapshot implements raft.FSM.
//
// Raft compacts its log up to the index this call is made at, so every block delivered so far MUST already be
// durable in the DB, otherwise a crash after compaction would lose entries for good (§1.4). Snapshot therefore
// waits (on a local counter, not on peers) until the pipeline has committed everything Apply delivered. Apply is
// blocked meanwhile, so the pipeline can only drain; if it never does, no snapshot is taken and no log is lost.
func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appliedIndex == 0 {
		return nil, raft.ErrNothingNewToSnapshot
	}
	for f.durable() < f.delivered {
		select {
		case <-f.stop:
			return nil, errors.New("raftfeed: stopping")
		case <-time.After(durablePollInterval):
		}
	}
	meta := &rpb.FsmSnapshotMeta{
		SchemaVersion:       schemaVersion,
		AppliedIndex:        f.appliedIndex,
		LastBlockNumber:     f.st.nextBlock - 1,
		LastGlobalExecIndex: f.st.nextIndex - 1,
		LastCommitHash:      append([]byte(nil), f.st.prevHash...),
		LastTimestampMs:     f.st.lastTs,
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(meta)
	if err != nil {
		return nil, err
	}
	f.snapshotBlock.Store(meta.LastBlockNumber)
	return &metaSnapshot{data: data}, nil
}

// Restore implements raft.FSM. It only restores counters. If the DB is behind the snapshot (a replica that was
// rebuilt empty, plan C4) it fails closed: this node cannot continue from log replay.
func (f *fsm) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	var meta rpb.FsmSnapshotMeta
	if err := proto.Unmarshal(data, &meta); err != nil {
		return f.fatal(fmt.Errorf("undecodable FsmSnapshotMeta: %w", err))
	}
	if meta.SchemaVersion != schemaVersion {
		return f.fatal(fmt.Errorf("unsupported FsmSnapshotMeta schema %d", meta.SchemaVersion))
	}
	if d := f.durable(); d < meta.LastBlockNumber {
		return f.fatal(fmt.Errorf("snapshot covers block %d but the DB only has block %d: state must be rebuilt first (plan C4)", meta.LastBlockNumber, d))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.nextBlock = meta.LastBlockNumber + 1
	f.st.nextIndex = meta.LastGlobalExecIndex + 1
	f.st.prevHash = append([]byte(nil), meta.LastCommitHash...)
	f.st.lastTs = meta.LastTimestampMs
	f.appliedIndex = meta.AppliedIndex
	f.delivered = meta.LastBlockNumber
	f.snapshotBlock.Store(meta.LastBlockNumber)
	return nil
}

type metaSnapshot struct{ data []byte }

func (s *metaSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *metaSnapshot) Release() {}
