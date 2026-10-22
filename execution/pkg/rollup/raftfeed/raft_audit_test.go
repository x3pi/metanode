package raftfeed

import (
	"testing"
	"time"

	"github.com/hashicorp/raft"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

func TestAuditOversizeTxMustNotAcknowledgeLostBatch(t *testing.T) {
	small := testBatch(t, 1)
	txs, err := transaction.UnmarshalTransactions(small)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := txs[0].Marshal()
	if err != nil {
		t.Fatal(err)
	}
	max := len(raw) + 3
	h := newHarness(t, 1)
	h.mutate = func(c *config.RaftConfig) { c.MaxBatchBytes = max }
	h.startAll()
	leader := h.leader()
	var input pb.Transactions
	if err := proto.Unmarshal(testBatch(t, 1, 2), &input); err != nil {
		t.Fatal(err)
	}
	input.Transactions[1].Data = make([]byte, max+1)
	batch, err := proto.Marshal(&input)
	if err != nil {
		t.Fatal(err)
	}
	if leader.node.Submit(batch) {
		t.Fatal("Submit returned true: batch containing oversized transaction must not be accepted")
	}
	if st := leader.node.SubmitStatus(batch); st != statusTooLarge {
		t.Fatalf("expected statusTooLarge, got %d", st)
	}
	if len(leader.disk.blocks()) != 0 {
		t.Fatalf("expected 0 blocks committed for rejected batch, got %d", len(leader.disk.blocks()))
	}
}

func TestAuditSplitMustRespectSerializedLimit(t *testing.T) {
	txs, err := transaction.UnmarshalTransactions(testBatch(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := txs[0].Marshal()
	if err != nil {
		t.Fatal(err)
	}
	max := len(raw) * 2
	pieces, err := splitBatch(testBatch(t, 1, 1, 1), max)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pieces {
		if len(p) > max {
			t.Errorf("piece %d serialized size %d exceeds max_batch_bytes %d", i, len(p), max)
		}
	}
}

func TestReviewAllOversizeMustNotReturnCommitSuccess(t *testing.T) {
	h := newHarness(t, 1)
	h.mutate = func(c *config.RaftConfig) { c.MaxBatchBytes = 1 }
	h.startAll()
	l := h.leader()
	if l.node.Submit(testBatch(t, 1)) {
		t.Fatalf("Submit=true although all transactions were dropped; dropped=%d blocks=%d", l.node.Dropped(), len(l.disk.blocks()))
	}
}

func TestMergeReviewFollowerMustNotAcknowledgeOversizeDrop(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(c *config.RaftConfig) { c.MaxBatchBytes = 1 }
	h.startAll()
	leader := h.leader()
	batch := testBatch(t, 1)
	for i := 0; i < 3; i++ {
		t.Logf("leader Submit attempt %d = %v", i+1, leader.node.Submit(batch))
	}
	for _, m := range h.m {
		if m == leader {
			continue
		}
		accepted := m.node.Submit(batch)
		if accepted {
			t.Fatalf("follower Submit=true for permanently rejected batch; leader blocks=%d follower drops=%d", len(leader.disk.blocks()), m.node.Dropped())
		}
		break
	}
}

func TestReviewConfiguredLimitBeforeClusterStart(t *testing.T) {
	prev := config.ConfigApp
	config.ConfigApp = &config.SimpleChainConfig{ConsensusMode: "raft", Raft: &config.RaftConfig{MaxBatchBytes: 256}}
	defer func() { config.ConfigApp = prev }()
	if got := MaxBatchBytes(); got != 256 {
		t.Fatalf("configured max_batch_bytes=256 but admission helper reports %d before StartCluster", got)
	}
}

func TestReviewSplitLargerThanQueueMustProgress(t *testing.T) {
	h := newHarness(t, 1)
	one := testBatch(t, 1)
	h.mutate = func(c *config.RaftConfig) { c.MaxBatchBytes = len(one); c.ProposeQueueSize = 1 }
	h.startAll()
	l := h.leader()
	batch := testBatch(t, 1, 2, 3)
	pieces, err := splitBatch(batch, len(one))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		st := l.node.SubmitStatus(batch)
		t.Logf("attempt=%d pieces=%d queueLen=%d queueCap=%d status=%d", i+1, len(pieces), len(l.node.proposeQ), cap(l.node.proposeQ), st)
		if st == statusAccepted {
			return
		}
	}
	t.Fatal("valid batch cannot enter an empty queue: split piece count exceeds total queue capacity")
}

func TestReviewForwardBodyLimitMustNotRejectSplittableBatch(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = calmElections
	h.startAll()
	leader := h.leader()
	nonces := make([]uint64, 1024)
	for i := range nonces {
		nonces[i] = uint64(i)
	}
	var input pb.Transactions
	if err := proto.Unmarshal(testBatch(t, nonces...), &input); err != nil {
		t.Fatal(err)
	}
	for _, tx := range input.Transactions {
		tx.Data = make([]byte, 64<<10)
	}
	batch, err := proto.Marshal(&input)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) <= maxForwardBody {
		t.Fatal("fixture does not exceed HTTP body limit")
	}
	pieces, err := splitBatch(batch, leader.node.MaxBatchBytes())
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, p := range pieces {
		txs, e := transaction.UnmarshalTransactions(p)
		if e != nil {
			t.Fatal(e)
		}
		total += len(txs)
	}
	if total != len(nonces) {
		t.Fatalf("fixture not fully splittable: txs=%d", total)
	}
	for _, m := range h.m {
		if m == leader {
			continue
		}
		st := m.node.SubmitStatus(batch)
		if st == statusTooLarge {
			t.Fatalf("HTTP forward rejected %d-byte batch with %d individually fitting txs / %d valid Raft pieces; status=%d", len(batch), total, len(pieces), st)
		}
		if st != statusAccepted {
			t.Fatalf("unexpected status %d", st)
		}
		break
	}
}

// calmElections gives the election/heartbeat timers production-like headroom. Tests that push tens of MB through three
// in-process nodes would otherwise starve heartbeats under the race detector: the leader "fails to contact quorum, stepping
// down" mid-commit and the submit comes back 504 (leadership lost), which says nothing about batch sizing.
func calmElections(c *config.RaftConfig) {
	c.HeartbeatTimeoutMs, c.ElectionTimeoutMs, c.LeaderLeaseTimeoutMs = 3000, 3000, 3000
}

func TestReviewHighBatchLimitStillRespectsHTTP(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(c *config.RaftConfig) { c.MaxBatchBytes = 128 << 20; calmElections(c) }
	h.startAll()
	leader := h.leader()
	nonces := make([]uint64, 1024)
	for i := range nonces {
		nonces[i] = uint64(i)
	}
	var input pb.Transactions
	if err := proto.Unmarshal(testBatch(t, nonces...), &input); err != nil {
		t.Fatal(err)
	}
	for _, tx := range input.Transactions {
		tx.Data = make([]byte, 64<<10)
	}
	batch, err := proto.Marshal(&input)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) <= maxForwardBody {
		t.Fatal("fixture does not exceed HTTP body limit")
	}
	pieces, err := splitBatch(batch, leader.node.MaxBatchBytes())
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, p := range pieces {
		txs, e := transaction.UnmarshalTransactions(p)
		if e != nil {
			t.Fatal(e)
		}
		total += len(txs)
	}
	if total != len(nonces) {
		t.Fatalf("fixture not fully splittable: txs=%d", total)
	}
	for _, m := range h.m {
		if m == leader {
			continue
		}
		st := m.node.SubmitStatus(batch)
		if st == statusTooLarge {
			t.Fatalf("HTTP forward rejected %d-byte batch with %d individually fitting txs / %d valid Raft pieces; status=%d", len(batch), total, len(pieces), st)
		}
		if st != statusAccepted {
			t.Fatalf("unexpected status %d", st)
		}
		break
	}
}

// Delay only application records to model slow durable storage without delaying elections.
type reviewSlowStore struct{ raft.LogStore }

func (s *reviewSlowStore) StoreLog(l *raft.Log) error { return s.StoreLogs([]*raft.Log{l}) }
func (s *reviewSlowStore) StoreLogs(ls []*raft.Log) error {
	for _, l := range ls {
		if l.Type == raft.LogCommand {
			time.Sleep(1100 * time.Millisecond)
			break
		}
	}
	return s.LogStore.StoreLogs(ls)
}

func TestReviewChunkRetryMustReachTail(t *testing.T) {
	h := newHarness(t, 1)
	one := testBatch(t, 1)
	h.mutate = func(c *config.RaftConfig) { c.MaxBatchBytes = len(one); c.ProposeQueueSize = 1 }
	h.wrap = map[string]func(raft.LogStore) raft.LogStore{"n0": func(s raft.LogStore) raft.LogStore { return &reviewSlowStore{s} }}
	h.startAll()
	l := h.leader()
	batch := testBatch(t, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	// ten single-tx chunks at ~1.1s each take longer than submitCommitTimeout in total, yet every chunk makes progress:
	// ONE call must finish with every nonce committed exactly once, with no retry and so no duplicated prefix.
	started := time.Now()
	st := l.node.SubmitStatus(batch)
	t.Logf("status=%d after %v (submitCommitTimeout=%v)", st, time.Since(started).Round(100*time.Millisecond), submitCommitTimeout)
	if st != statusAccepted {
		t.Fatalf("a batch that keeps making progress must be accepted by one call, got status %d", st)
	}
	counts := map[uint64]int{}
	for _, b := range l.disk.blocks() {
		for _, raw := range b.Transactions {
			tx, err := transaction.UnmarshalTransaction(raw.Digest)
			if err != nil {
				t.Fatal(err)
			}
			counts[tx.GetNonce()]++
		}
	}
	for nonce := uint64(1); nonce <= 10; nonce++ {
		if counts[nonce] != 1 {
			t.Fatalf("nonce %d was committed %d times, want exactly once: %v", nonce, counts[nonce], counts)
		}
	}
}

func TestForwardPreservesTransactionAboveDefaultLimit(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(c *config.RaftConfig) { c.MaxBatchBytes = 8 << 20; calmElections(c) }
	h.startAll()
	leader := h.leader()
	var input pb.Transactions
	if err := proto.Unmarshal(testBatch(t, 1, 2), &input); err != nil {
		t.Fatal(err)
	}
	input.Transactions[1].Data = make([]byte, 5<<20)
	batch, err := proto.Marshal(&input)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range h.m {
		if m == leader {
			continue
		}
		if st := m.node.SubmitStatus(batch); st != statusAccepted {
			t.Fatalf("status=%d", st)
		}
		break
	}
	h.waitBlocks(1, leader)
	total := 0
	for _, b := range leader.disk.blocks() {
		total += len(b.Transactions)
	}
	if total != 2 {
		t.Fatalf("forwarding acknowledged batch but retained only %d of 2 transactions", total)
	}
}

func TestAdmissionLimitIncludesTransportCap(t *testing.T) {
	prev := config.ConfigApp
	defer func() { config.ConfigApp = prev }()
	for _, limit := range []int{256, 8 << 20, 128 << 20} {
		config.ConfigApp = &config.SimpleChainConfig{ConsensusMode: "raft", Raft: &config.RaftConfig{MaxBatchBytes: limit}}
		want := limit
		if want > maxForwardBody {
			want = maxForwardBody
		}
		before := MaxBatchBytes()
		running := (&Node{cfg: config.RaftConfig{MaxBatchBytes: limit}}).MaxBatchBytes()
		if before != want || running != want {
			t.Fatalf("limit=%d before=%d running=%d want=%d", limit, before, running, want)
		}
	}
}

// EncodedTxSize must equal what protobuf really adds for a repeated bytes field number 1, at every varint width boundary.
func TestEncodedTxSizeMatchesProtobufWireFormat(t *testing.T) {
	for _, n := range []int{0, 1, 126, 127, 128, 129, 16383, 16384, 16385, 2<<20 - 1, 2 << 20, 4 << 20, 64 << 20, 268435455, 268435456} {
		if got, want := EncodedTxSize(n), 1+protowire.SizeBytes(n); got != want {
			t.Fatalf("EncodedTxSize(%d)=%d, protobuf encodes %d", n, got, want)
		}
	}
}

func TestReviewDifferentNodeLimitsMustNotAcknowledgeMissingTx(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(c *config.RaftConfig) {
		c.MaxBatchBytes = 8 << 10
		if c.NodeID == "n0" {
			c.MaxBatchBytes = 4 << 10
		}
		c.HeartbeatTimeoutMs = 2000
		c.ElectionTimeoutMs = 2000
		c.LeaderLeaseTimeoutMs = 1000
	}
	h.startAll()
	leader := h.leader()
	if leader.id != "n0" {
		t.Skip("fixture requires n0 leader")
	}
	var input pb.Transactions
	if err := proto.Unmarshal(testBatch(t, 1, 2), &input); err != nil {
		t.Fatal(err)
	}
	input.Transactions[1].Data = make([]byte, 5<<10)
	batch, err := proto.Marshal(&input)
	if err != nil {
		t.Fatal(err)
	}
	st := h.m[1].node.SubmitStatus(batch)
	if st != statusAccepted {
		return
	}
	h.waitBlocks(1, leader)
	total := 0
	for _, b := range leader.disk.blocks() {
		total += len(b.Transactions)
	}
	if total != 2 {
		t.Fatalf("status=200, submitted=2, delivered=%d; follower limit=%d leader limit=%d", total, h.m[1].node.MaxBatchBytes(), leader.node.MaxBatchBytes())
	}
}
