package raftfeed

import (
	"bytes"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/hashicorp/raft"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	rpb "github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed/pb"
)

var seqAddr = common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")

type fsmRig struct {
	f     *fsm
	sink  chan *pb.ExecutableBlock
	fatal chan error
	dur   atomic.Uint64
}

func newRig(t *testing.T) *fsmRig {
	t.Helper()
	r := &fsmRig{sink: make(chan *pb.ExecutableBlock, 64), fatal: make(chan error, 4)}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	r.f = newFSM(stamper{leader: seqAddr, nextIndex: 1, nextBlock: 1}, r.sink, r.dur.Load, stop,
		func(err error) { r.fatal <- err })
	return r
}

// next receives the next delivered block and marks it durable, like a pipeline that commits promptly.
func (r *fsmRig) next() *pb.ExecutableBlock {
	b := <-r.sink
	r.dur.Store(b.BlockNumber)
	return b
}

func entry(t *testing.T, idx, ts uint64, batch []byte, count uint32) *raft.Log {
	t.Helper()
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(&rpb.BatchRecord{
		SchemaVersion: schemaVersion, TimestampMs: ts, Txs: batch, TxCount: count, ProposerId: "diag-only"})
	if err != nil {
		t.Fatal(err)
	}
	return &raft.Log{Index: idx, Type: raft.LogCommand, Data: data}
}

func marshalBlock(t *testing.T, b *pb.ExecutableBlock) []byte {
	t.Helper()
	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// T-AP-01: every mapped field, against the table of SEQUENCER_SCHEMAS §1.3.
func TestFSM_MapsBatchToBlockFields(t *testing.T) {
	r := newRig(t)
	b := testBatch(t, 1, 2, 3)
	if res := r.f.Apply(entry(t, 7, 5000, b, 3)); res.(applyResult).BlockNumber != 1 {
		t.Fatalf("unexpected result %+v", res)
	}
	blk := <-r.sink
	if len(blk.Transactions) != 3 || blk.BlockNumber != 1 || blk.GlobalExecIndex != 1 || blk.CommitIndex != 1 ||
		blk.Epoch != 0 || blk.CommitTimestampMs != 5000 || !bytes.Equal(blk.LeaderAddress, seqAddr.Bytes()) ||
		len(blk.CommitHash) != 32 || len(blk.SystemTransactions) != 0 {
		t.Fatalf("wrong block: %+v", blk)
	}
	for i, tx := range blk.Transactions {
		if len(tx.Digest) == 0 || tx.WorkerId != 0 {
			t.Fatalf("tx %d not a full tx in its own digest: %+v", i, tx)
		}
	}
}

// T-DET-04: two independent replicas applying the same entries produce byte-identical blocks, including the
// timestamp bump for a leader whose clock went backwards.
func TestFSM_TwoReplicasProduceIdenticalBlocks(t *testing.T) {
	a, b := newRig(t), newRig(t)
	entries := []*raft.Log{
		entry(t, 2, 1000, testBatch(t, 1), 1),
		entry(t, 3, 900, testBatch(t, 2, 3), 2), // clock went backwards (new leader)
		entry(t, 5, 900, testBatch(t, 4), 1),
	}
	var last uint64
	for _, e := range entries {
		a.f.Apply(e)
		b.f.Apply(e)
		x, y := a.next(), b.next()
		if !bytes.Equal(marshalBlock(t, x), marshalBlock(t, y)) {
			t.Fatalf("replicas diverged at index %d", e.Index)
		}
		if x.CommitTimestampMs <= last {
			t.Fatalf("timestamp not strictly increasing: %d after %d", x.CommitTimestampMs, last)
		}
		last = x.CommitTimestampMs
	}
}

// T-AP-02: numbering that no longer fits uint32 stops the replica; nothing is truncated or delivered.
func TestFSM_StopsWhenCommitIndexOverflows(t *testing.T) {
	r := newRig(t)
	r.f.st.nextIndex = math.MaxUint32 + 1
	if err, ok := r.f.Apply(entry(t, 9, 1, testBatch(t, 1), 1)).(error); !ok || err == nil {
		t.Fatal("expected an error result")
	}
	select {
	case <-r.fatal:
	case <-time.After(2 * time.Second):
		t.Fatal("replica was not stopped")
	}
	if len(r.sink) != 0 {
		t.Fatal("a block was delivered despite the overflow")
	}
}

// T-AP-03 / T-AP-05: a tx_count mismatch or an empty batch is never skipped: the replica stops, and after that
// it applies nothing at all (a later entry must not create a hole).
func TestFSM_RejectsBadEntriesAndStaysStopped(t *testing.T) {
	cases := map[string]*raft.Log{
		"tx_count mismatch": nil,
		"empty batch":       nil,
		"bad schema":        nil,
		"garbage":           {Index: 4, Type: raft.LogCommand, Data: []byte{0xff, 0xff, 0xff}},
	}
	good := testBatch(t, 1)
	cases["tx_count mismatch"] = entry(t, 4, 1, good, 2)
	cases["empty batch"] = entry(t, 4, 1, nil, 0)
	bad, _ := proto.Marshal(&rpb.BatchRecord{SchemaVersion: 99, Txs: good, TxCount: 1})
	cases["bad schema"] = &raft.Log{Index: 4, Type: raft.LogCommand, Data: bad}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if _, ok := r.f.Apply(l).(error); !ok {
				t.Fatal("expected an error result")
			}
			select {
			case <-r.fatal:
			case <-time.After(2 * time.Second):
				t.Fatal("replica was not stopped")
			}
			if _, ok := r.f.Apply(entry(t, 5, 2, good, 1)).(error); !ok {
				t.Fatal("a stopped replica applied a later entry")
			}
			if len(r.sink) != 0 {
				t.Fatal("a stopped replica delivered a block")
			}
		})
	}
}

// T-AP-04: Raft replays entries after a restart. Blocks already durable are not delivered again, but the
// counters and hash chain advance exactly as on a replica that did not restart.
func TestFSM_ReplaySkipsDurableBlocksButKeepsTheChain(t *testing.T) {
	entries := []*raft.Log{
		entry(t, 2, 100, testBatch(t, 1), 1),
		entry(t, 3, 200, testBatch(t, 2), 1),
		entry(t, 4, 300, testBatch(t, 3), 1),
	}
	ref := newRig(t)
	var want []*pb.ExecutableBlock
	for _, e := range entries {
		ref.f.Apply(e)
		want = append(want, ref.next())
	}

	re := newRig(t)
	re.dur.Store(2) // blocks 1 and 2 are already in the DB
	for i, e := range entries {
		res := re.f.Apply(e).(applyResult)
		if res.Skipped != (i < 2) {
			t.Fatalf("entry %d: skipped=%v", i, res.Skipped)
		}
	}
	if len(re.sink) != 1 {
		t.Fatalf("delivered %d blocks after replay, want only block 3", len(re.sink))
	}
	if got := <-re.sink; !bytes.Equal(marshalBlock(t, got), marshalBlock(t, want[2])) {
		t.Fatal("block 3 after a replay differs from the never-restarted replica: the chain was not advanced through skipped blocks")
	}
}

type memSink struct {
	bytes.Buffer
	cancelled bool
}

func (m *memSink) ID() string    { return "mem" }
func (m *memSink) Cancel() error { m.cancelled = true; return nil }
func (m *memSink) Close() error  { return nil }

func snapshotBytes(t *testing.T, s raft.FSMSnapshot) []byte {
	t.Helper()
	sink := &memSink{}
	if err := s.Persist(sink); err != nil {
		t.Fatal(err)
	}
	return sink.Bytes()
}

// T-AP-07 / T-AP-08: Snapshot must not return (so Raft cannot compact its log) while a delivered block is not
// yet durable; the meta it then carries describes exactly the durable state.
func TestFSM_SnapshotWaitsForDurableBlocks(t *testing.T) {
	r := newRig(t)
	for i := uint64(1); i <= 3; i++ {
		r.f.Apply(entry(t, i+1, i*10, testBatch(t, i), 1))
		r.next()
	}
	r.dur.Store(1) // pipeline has only committed block 1 of 3 (e.g. the DB lost its tail)

	type out struct {
		s   raft.FSMSnapshot
		err error
	}
	done := make(chan out, 1)
	go func() { s, err := r.f.Snapshot(); done <- out{s, err} }()
	select {
	case <-done:
		t.Fatal("Snapshot returned while delivered blocks were not durable: Raft could compact entries that are lost on a crash")
	case <-time.After(200 * time.Millisecond):
	}
	r.dur.Store(3)
	var o out
	select {
	case o = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Snapshot did not return once everything was durable")
	}
	if o.err != nil {
		t.Fatal(o.err)
	}
	var meta rpb.FsmSnapshotMeta
	if err := proto.Unmarshal(snapshotBytes(t, o.s), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.LastBlockNumber != 3 || meta.AppliedIndex != 4 || meta.LastGlobalExecIndex != 3 || meta.LastTimestampMs != 30 || len(meta.LastCommitHash) != 32 {
		t.Fatalf("unexpected meta: %+v", &meta)
	}
	if meta.LastBlockNumber > r.dur.Load() {
		t.Fatal("snapshot claims more than is durable")
	}
}

func TestFSM_SnapshotWithNothingAppliedIsNotTaken(t *testing.T) {
	r := newRig(t)
	if _, err := r.f.Snapshot(); err != raft.ErrNothingNewToSnapshot {
		t.Fatalf("got %v", err)
	}
}

// A replica restored from a snapshot continues the chain exactly like one that applied everything itself.
func TestFSM_RestoreContinuesTheChain(t *testing.T) {
	full := newRig(t)
	src := newRig(t)
	for i := uint64(1); i <= 3; i++ {
		e := entry(t, i+1, i*10, testBatch(t, i), 1)
		full.f.Apply(e)
		src.f.Apply(e)
		full.next()
		src.next()
	}
	src.dur.Store(3)
	snap, err := src.f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	data := snapshotBytes(t, snap)

	rest := newRig(t)
	rest.dur.Store(3)
	if err := rest.f.Restore(nopCloser{bytes.NewReader(data)}); err != nil {
		t.Fatal(err)
	}
	next := entry(t, 9, 5, testBatch(t, 99), 1) // ts 5 < last 30: must still be bumped identically
	full.f.Apply(next)
	rest.f.Apply(next)
	if !bytes.Equal(marshalBlock(t, full.next()), marshalBlock(t, <-rest.sink)) {
		t.Fatal("restored replica diverged from the one that applied every entry")
	}
}

// Restore must fail closed when the DB has not got what the snapshot claims (replica rebuilt empty).
func TestFSM_RestoreFailsWhenDBIsBehind(t *testing.T) {
	src := newRig(t)
	src.f.Apply(entry(t, 2, 1, testBatch(t, 1), 1))
	src.next()
	snap, _ := src.f.Snapshot()
	data := snapshotBytes(t, snap)

	rest := newRig(t) // durable = 0
	if err := rest.f.Restore(nopCloser{bytes.NewReader(data)}); err == nil {
		t.Fatal("restore succeeded although the DB is behind the snapshot")
	}
	select {
	case <-rest.fatal:
	case <-time.After(2 * time.Second):
		t.Fatal("replica not stopped")
	}
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

// T-AP-06: Apply's inputs are the log entry and restored counters only: no clock, no randomness in the code
// that builds blocks.
func TestApplyPathReadsNoClockOrRandomness(t *testing.T) {
	for _, file := range []string{"fsm.go", "stamper.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"time.Now", "math/rand", "crypto/rand"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s uses %s: block content would depend on the local machine", file, banned)
			}
		}
	}
}
