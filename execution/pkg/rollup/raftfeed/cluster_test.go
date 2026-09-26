package raftfeed

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// disk stands in for a node's block DB: it outlives node restarts. hold=true means the "pipeline" received
// blocks but has not committed them durably yet.
type disk struct {
	mu       sync.Mutex
	received []*pb.ExecutableBlock
	last     atomic.Uint64
	hold     bool
}

func (d *disk) run(sink <-chan *pb.ExecutableBlock, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case b := <-sink:
			d.mu.Lock()
			d.received = append(d.received, b)
			if !d.hold {
				d.last.Store(b.BlockNumber)
			}
			d.mu.Unlock()
		}
	}
}

func (d *disk) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hold = false
	if n := len(d.received); n > 0 {
		d.last.Store(d.received[n-1].BlockNumber)
	}
}

func (d *disk) blocks() []*pb.ExecutableBlock {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*pb.ExecutableBlock(nil), d.received...)
}

type member struct {
	id       string
	addr     raft.ServerAddress
	dir      string
	fwdAddr  string
	disk     *disk
	trans    *raft.InmemTransport
	node     *Node
	stopPipe chan struct{}
}

type harness struct {
	t       *testing.T
	m       []*member
	secret  []byte
	mutate  func(*config.RaftConfig)
	tune    func(*raft.Config)
	fatalMu sync.Mutex
	fatals  map[string]error
}

func newHarness(t *testing.T, n int) *harness {
	t.Helper()
	h := &harness{t: t, secret: bytes.Repeat([]byte("k"), 32), fatals: map[string]error{}}
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		fa := ln.Addr().String()
		ln.Close() // reserve the port; each (re)start re-listens on it
		h.m = append(h.m, &member{
			id: fmt.Sprintf("n%d", i), addr: raft.ServerAddress(fmt.Sprintf("n%d", i)),
			dir: filepath.Join(t.TempDir(), "raft"), fwdAddr: fa, disk: &disk{},
		})
	}
	t.Cleanup(func() {
		for _, m := range h.m {
			h.stop(m)
		}
	})
	return h
}

func (h *harness) rcfg(m *member) config.RaftConfig {
	var peers []config.RaftPeer
	for _, p := range h.m {
		peers = append(peers, config.RaftPeer{ID: p.id, Address: string(p.addr), ForwardAddress: p.fwdAddr})
	}
	rc := config.RaftConfig{
		NodeID: m.id, BindAddress: string(m.addr), DataDir: m.dir, Peers: peers, Bootstrap: m.id == "n0",
		HeartbeatTimeoutMs: 100, ElectionTimeoutMs: 100, LeaderLeaseTimeoutMs: 50, CommitTimeoutMs: 5,
		ForwardBindAddress: m.fwdAddr, ForwardSecretFile: "unused-in-tests", SequencerAddress: seqAddr.Hex(),
	}
	if h.mutate != nil {
		h.mutate(&rc)
	}
	return rc
}

func (h *harness) start(m *member) {
	h.t.Helper()
	_, tr := raft.NewInmemTransport(m.addr)
	m.trans = tr
	ln, err := net.Listen("tcp", m.fwdAddr)
	if err != nil {
		h.t.Fatal(err)
	}
	sink := make(chan *pb.ExecutableBlock, 5000)
	m.stopPipe = make(chan struct{})
	go m.disk.run(sink, m.stopPipe)
	m.node, err = startNode(ClusterConfig{
		Raft: h.rcfg(m), Sink: sink, Durable: m.disk.last.Load, Secret: h.secret,
		Transport: tr, ForwardListener: ln, Tune: h.tune,
		OnFatal: func(e error) { h.fatalMu.Lock(); h.fatals[m.id] = e; h.fatalMu.Unlock() },
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.connect()
}

func (h *harness) stop(m *member) {
	if m.node != nil {
		m.node.Stop()
		m.node = nil
		close(m.stopPipe)
	}
}

func (h *harness) startAll() {
	for _, m := range h.m {
		h.start(m)
	}
}

// connect wires every live transport to every other (a heal), except members currently isolated.
func (h *harness) connect() {
	for _, a := range h.m {
		for _, b := range h.m {
			if a != b && a.node != nil && b.node != nil {
				a.trans.Connect(b.addr, b.trans)
			}
		}
	}
}

func (h *harness) isolate(m *member) {
	m.trans.DisconnectAll()
	for _, o := range h.m {
		if o != m && o.trans != nil {
			o.trans.Disconnect(m.addr)
		}
	}
}

func (h *harness) leader(exclude ...*member) *member {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
	next:
		for _, m := range h.m {
			for _, x := range exclude {
				if m == x {
					continue next
				}
			}
			if m.node != nil && m.node.IsLeader() {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("no leader elected")
	return nil
}

// submit hands a batch to the current leader, retrying like the tx forwarder does on false.
func (h *harness) submit(from *member, batch []byte) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if from.node.Submit(batch) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatal("Submit never accepted the batch")
}

func (h *harness) waitBlocks(n int, members ...*member) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, m := range members {
			if len(m.disk.blocks()) < n {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, m := range members {
		h.t.Logf("%s has %d blocks", m.id, len(m.disk.blocks()))
	}
	h.t.Fatalf("replicas did not reach %d blocks", n)
}

func nonceOf(t *testing.T, blk *pb.ExecutableBlock) uint64 {
	t.Helper()
	tx, err := transaction.UnmarshalTransaction(blk.Transactions[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	return tx.GetNonce()
}

// assertIdentical: every member received exactly the same block stream, byte for byte.
func (h *harness) assertIdentical(members ...*member) {
	h.t.Helper()
	ref := members[0].disk.blocks()
	for _, m := range members[1:] {
		got := m.disk.blocks()
		if len(got) != len(ref) {
			h.t.Fatalf("%s has %d blocks, %s has %d", m.id, len(got), members[0].id, len(ref))
		}
		for i := range ref {
			if !bytes.Equal(marshalBlock(h.t, ref[i]), marshalBlock(h.t, got[i])) {
				h.t.Fatalf("block %d differs between %s and %s: FORK", i+1, members[0].id, m.id)
			}
		}
	}
	for i, b := range ref {
		if b.BlockNumber != uint64(i+1) || uint64(b.CommitIndex) != uint64(i+1) {
			h.t.Fatalf("numbering hole/dup at position %d: block %d commit %d", i, b.BlockNumber, b.CommitIndex)
		}
	}
}

// T-RF-01 (+T-DET-04 across real replicas): 1000 batches through the leader reach every replica in the same
// order and byte for byte.
func TestCluster_ThousandBatchesIdenticalOnAllReplicas(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	const N = 1000
	for i := 0; i < N; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(N, h.m...)
	h.assertIdentical(h.m...)
	for i, b := range h.m[0].disk.blocks() {
		if nonceOf(t, b) != uint64(i) {
			t.Fatalf("block %d holds nonce %d: order not preserved", i+1, nonceOf(t, b))
		}
	}
}

// T-SUB-01: a batch submitted on a follower reaches the log exactly once, via the leader.
func TestCluster_FollowerSubmitReachesLeaderExactlyOnce(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	var f *member
	for _, m := range h.m {
		if m != l {
			f = m
		}
	}
	for i := 0; i < 20; i++ {
		h.submit(f, testBatch(t, uint64(i)))
	}
	h.waitBlocks(20, h.m...)
	time.Sleep(200 * time.Millisecond) // a duplicate would show up by now
	h.assertIdentical(h.m...)
	if got := len(h.m[0].disk.blocks()); got != 20 {
		t.Fatalf("%d blocks, want exactly 20", got)
	}
}

// T-SUB-02: wrong MAC, unknown node and stale timestamp are all refused with 401 and nothing is proposed.
func TestCluster_ForwardChannelRejectsBadAuth(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	url := "http://" + l.fwdAddr + submitPath
	body := testBatch(t, 1)
	now := time.Now().UnixMilli()
	send := func(node string, ts int64, mac string) int {
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set(hdrNode, node)
		req.Header.Set(hdrTs, strconv.FormatInt(ts, 10))
		req.Header.Set(hdrMac, mac)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for name, code := range map[string]int{
		"wrong mac":     send("n1", now, forwardMAC([]byte("another-secret-another-secret-xxxx"), "n1", now, body)),
		"unknown node":  send("evil", now, forwardMAC(h.secret, "evil", now, body)),
		"stale ts":      send("n1", now-int64(2*maxForwardSkew/time.Millisecond), forwardMAC(h.secret, "n1", now-int64(2*maxForwardSkew/time.Millisecond), body)),
		"mac for other": send("n1", now, forwardMAC(h.secret, "n1", now, []byte("other body"))),
	} {
		if code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, code)
		}
	}
	if code := send("n1", now, forwardMAC(h.secret, "n1", now, body)); code != http.StatusOK {
		t.Fatalf("a correctly signed submit got %d", code)
	}
	h.waitBlocks(1, h.m...)
	time.Sleep(200 * time.Millisecond)
	if got := len(l.disk.blocks()); got != 1 {
		t.Fatalf("%d blocks: a rejected submit reached the log", got)
	}
}

// T-SUB-03: a batch above max_batch_bytes is split at tx boundaries, not cut and not lost.
func TestCluster_OversizeBatchIsSplitNotTruncated(t *testing.T) {
	h := newHarness(t, 3)
	one := testBatch(t, 0)
	h.mutate = func(rc *config.RaftConfig) { rc.MaxBatchBytes = len(one)*3 + 10 } // ~3 txs per piece
	h.startAll()
	l := h.leader()
	var nonces []uint64
	for i := 0; i < 10; i++ {
		nonces = append(nonces, uint64(i))
	}
	h.submit(l, testBatch(t, nonces...))
	h.waitBlocks(4, h.m...) // 10 txs, <=3 per piece => 4 blocks
	h.assertIdentical(h.m...)
	seen := map[uint64]bool{}
	for _, b := range l.disk.blocks() {
		for _, x := range b.Transactions {
			tx, _ := transaction.UnmarshalTransaction(x.Digest)
			seen[tx.GetNonce()] = true
		}
	}
	if len(seen) != 10 {
		t.Fatalf("only %d of 10 txs survived the split", len(seen))
	}
}

// Malformed and empty batches must never be proposed (they would halt every replica).
func TestCluster_MalformedBatchIsDroppedNotProposed(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	if !l.node.Submit([]byte{0xff, 0xff, 0xff, 0xff}) {
		t.Fatal("a batch that can never work must be accepted-and-dropped, or the forwarder retries it forever")
	}
	h.submit(l, testBatch(t, 7))
	h.waitBlocks(1, h.m...)
	if l.node.Dropped() != 1 || l.node.Failed() {
		t.Fatalf("dropped=%d failed=%v", l.node.Dropped(), l.node.Failed())
	}
	for _, m := range h.m {
		if len(m.disk.blocks()) != 1 {
			t.Fatalf("%s has %d blocks", m.id, len(m.disk.blocks()))
		}
	}
}

// T-RF-02 + T-RF-11: after the leader is killed, every batch it had committed is still executed by the others,
// a new leader takes over and the chain continues. The failover time is reported (no hard threshold).
func TestCluster_KillLeaderAfterCommit(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 50; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(50, h.m...)
	killed := time.Now()
	h.stop(l)
	nl := h.leader(l)
	t.Logf("T-RF-11: leader lost -> new leader in %v (election_timeout_ms=100)", time.Since(killed))
	for i := 50; i < 60; i++ {
		h.submit(nl, testBatch(t, uint64(i)))
	}
	var alive []*member
	for _, m := range h.m {
		if m != l {
			alive = append(alive, m)
		}
	}
	h.waitBlocks(60, alive...)
	h.assertIdentical(alive...)
	if got := len(l.disk.blocks()); got > 60 {
		t.Fatalf("killed node has %d blocks", got)
	}
}

// T-RF-04: one isolated follower does not stop the cluster; after healing it catches up to an identical chain.
func TestCluster_IsolatedFollowerDoesNotStopCommitAndCatchesUp(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	var f *member
	for _, m := range h.m {
		if m != l {
			f = m
		}
	}
	h.isolate(f)
	for i := 0; i < 30; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	var rest []*member
	for _, m := range h.m {
		if m != f {
			rest = append(rest, m)
		}
	}
	h.waitBlocks(30, rest...)
	if len(f.disk.blocks()) != 0 {
		t.Fatal("isolated follower received blocks")
	}
	h.connect()
	h.waitBlocks(30, f)
	h.assertIdentical(h.m...)
}

// distinctNonces counts the distinct txs executed by a member and how many blocks re-executed an earlier tx.
func distinctNonces(t *testing.T, m *member) (distinct int, duplicateBlocks int) {
	seen := map[uint64]bool{}
	for _, b := range m.disk.blocks() {
		n := nonceOf(t, b)
		if seen[n] {
			duplicateBlocks++
		}
		seen[n] = true
	}
	return len(seen), duplicateBlocks
}

// T-RF-05: with both followers cut off the leader cannot commit, so nothing new is executed anywhere; earlier
// committed data is intact. After healing every queued tx is executed. A batch whose proposal outcome was unknown
// (leadership lost) may be executed twice as two blocks — the same tx in two batches; Go dedups by tx hash — but
// never zero times, and all replicas stay byte-identical.
func TestCluster_LeaderWithoutQuorumExecutesNothing(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 5; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(5, h.m...)

	for _, m := range h.m {
		if m != l {
			h.isolate(m)
		}
	}
	for i := 5; i < 10; i++ {
		l.node.Submit(testBatch(t, uint64(i)))
	}
	time.Sleep(600 * time.Millisecond)
	for _, m := range h.m {
		if got := len(m.disk.blocks()); got != 5 {
			t.Fatalf("%s executed %d blocks without a quorum, want 5", m.id, got)
		}
	}
	h.connect()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if d, _ := distinctNonces(t, l); d == 10 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	for _, m := range h.m {
		d, dup := distinctNonces(t, m)
		if d != 10 {
			t.Fatalf("%s executed %d distinct txs after healing, want 10 (lost=%d)", m.id, d, l.node.Lost())
		}
		t.Logf("%s: %d blocks, %d re-executed batches (dedup'd by Go)", m.id, len(m.disk.blocks()), dup)
	}
	h.waitBlocks(len(l.disk.blocks()), h.m...)
	h.assertIdentical(h.m...)
}

// T-RF-03 / T-RF-06 / T-SUB-04: the leader is cut off but alive and accepts batches it can never commit. The
// others elect a new leader; nothing uncommitted is executed while cut off; after healing the old leader's log is
// reconciled (no fork), and the batches it had accepted are re-routed to the new leader and executed once each.
func TestCluster_IsolatedLeaderNoForkNoLossNoDuplicate(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	old := h.leader()
	for i := 0; i < 5; i++ {
		h.submit(old, testBatch(t, uint64(i)))
	}
	h.waitBlocks(5, h.m...)

	h.isolate(old)
	for i := 100; i < 105; i++ { // accepted by the cut-off leader, cannot commit
		old.node.Submit(testBatch(t, uint64(i)))
	}
	nl := h.leader(old)
	for i := 200; i < 205; i++ { // committed by the majority
		h.submit(nl, testBatch(t, uint64(i)))
	}
	var majority []*member
	for _, m := range h.m {
		if m != old {
			majority = append(majority, m)
		}
	}
	h.waitBlocks(10, majority...)
	if got := len(old.disk.blocks()); got != 5 {
		t.Fatalf("cut-off leader executed %d blocks, want 5", got)
	}

	h.connect()
	h.waitBlocks(15, h.m...) // 5 + 5 (majority) + 5 re-routed
	time.Sleep(500 * time.Millisecond)
	h.assertIdentical(h.m...)
	seen := map[uint64]int{}
	for _, b := range old.disk.blocks() {
		seen[nonceOf(t, b)]++
	}
	for n := range seen {
		if seen[n] != 1 {
			t.Fatalf("nonce %d executed %d times", n, seen[n])
		}
	}
	for _, want := range []uint64{0, 4, 100, 104, 200, 204} {
		if seen[want] != 1 {
			t.Fatalf("nonce %d missing after the leader change (lost=%d)", want, old.node.Lost())
		}
	}
	if len(seen) != 15 {
		t.Fatalf("%d distinct txs, want 15", len(seen))
	}
}

// T-RF-07: the whole cluster restarts; Raft replays, executed blocks are not executed again, numbering continues.
func TestCluster_FullRestartContinuesWithoutReexecution(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 40; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(40, h.m...)
	for _, m := range h.m {
		h.stop(m)
	}
	h.startAll()
	l = h.leader()
	for i := 40; i < 50; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	// Each disk kept its 40 blocks; the restarted FSM must deliver only the 10 new ones.
	h.waitBlocks(50, h.m...)
	time.Sleep(300 * time.Millisecond)
	h.assertIdentical(h.m...)
	if got := len(l.disk.blocks()); got != 50 {
		t.Fatalf("%d blocks after restart, want 50 (a replay must not execute twice)", got)
	}
	var skipped uint64
	for _, m := range h.m {
		skipped += m.node.Skipped()
	}
	if skipped == 0 {
		t.Fatal("expected the replay to be skipped block by block, saw no skips (was anything replayed?)")
	}
}

// T-AP-07 end to end: while the pipeline has not made the delivered blocks durable, a forced Raft snapshot must
// not finish, so no log entry can be compacted away; once they are durable it completes, and a crash + restart
// from that snapshot loses nothing. (The delivery gate keeps at most one block ahead of the DB, so what is held
// back here is Apply itself — the FSM-level test covers Snapshot's own wait.)
func TestCluster_SnapshotNeverCompactsAheadOfDurability(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(rc *config.RaftConfig) { rc.TrailingLogs = 5; rc.SnapshotThreshold = 1 << 30 }
	h.startAll()
	l := h.leader()
	l.disk.mu.Lock()
	l.disk.hold = true
	l.disk.mu.Unlock()
	for i := 0; i < 30; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(1, l)
	time.Sleep(300 * time.Millisecond)
	if got := len(l.disk.blocks()); got != 1 {
		t.Fatalf("%d blocks delivered although none is durable: the gate lets the pipeline run more than one block ahead", got)
	}
	done := make(chan error, 1)
	go func() { done <- l.node.raft.Snapshot().Error() }()
	select {
	case err := <-done:
		t.Fatalf("snapshot finished (%v) while a delivered block was not durable: log could be compacted ahead of the DB", err)
	case <-time.After(400 * time.Millisecond):
	}
	l.disk.release()
	h.waitBlocks(30, h.m...)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not complete once the blocks were durable")
	}

	// Crash the leader after compaction, keep its DB; restart from snapshot + remaining log.
	h.stop(l)
	h.start(l)
	nl := h.leader()
	for i := 30; i < 40; i++ {
		h.submit(nl, testBatch(t, uint64(i)))
	}
	h.waitBlocks(40, h.m...)
	time.Sleep(300 * time.Millisecond)
	h.assertIdentical(h.m...)
}

// A replica whose FSM cannot apply an entry stops; the rest of the cluster keeps going (T-RF-09 shape, using a
// hostile entry instead of a state-root mismatch, which needs the execution layer).
func TestCluster_FailedReplicaStopsWhileQuorumContinues(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	var victim *member
	for _, m := range h.m {
		if m != l {
			victim = m
		}
	}
	h.submit(l, testBatch(t, 0))
	h.waitBlocks(1, h.m...)
	victim.node.fsm.fatal(fmt.Errorf("injected divergence"))
	deadline := time.Now().Add(5 * time.Second)
	for !victim.node.Failed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !victim.node.Failed() || victim.node.Ready() {
		t.Fatal("victim did not stop accepting work")
	}
	for i := 1; i < 6; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	var rest []*member
	for _, m := range h.m {
		if m != victim {
			rest = append(rest, m)
		}
	}
	h.waitBlocks(6, rest...)
	h.assertIdentical(rest...)
	if got := len(victim.disk.blocks()); got > 1 {
		t.Fatalf("stopped replica kept delivering: %d blocks", got)
	}
}

func TestClusterConfigValidation(t *testing.T) {
	good := config.RaftConfig{
		NodeID: "a", BindAddress: "x:1", DataDir: "/d", ForwardBindAddress: "x:2", ForwardSecretFile: "/s",
		SequencerAddress: seqAddr.Hex(),
		Peers:            []config.RaftPeer{{ID: "a", Address: "x:1", ForwardAddress: "x:2"}},
	}
	if _, err := effectiveRaftConfig(&good); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
	bad := map[string]func(*config.RaftConfig){
		"node not in peers":    func(c *config.RaftConfig) { c.NodeID = "zzz" },
		"no data dir":          func(c *config.RaftConfig) { c.DataDir = "" },
		"dup peer":             func(c *config.RaftConfig) { c.Peers = append(c.Peers, c.Peers[0]) },
		"election < heartbeat": func(c *config.RaftConfig) { c.HeartbeatTimeoutMs, c.ElectionTimeoutMs = 500, 100 },
		"lease > heartbeat": func(c *config.RaftConfig) {
			c.LeaderLeaseTimeoutMs, c.HeartbeatTimeoutMs = 900, 500
			c.ElectionTimeoutMs = 900
		},
		"negative batch":         func(c *config.RaftConfig) { c.MaxBatchBytes = -1 },
		"bad sequencer":          func(c *config.RaftConfig) { c.SequencerAddress = "0x12" },
		"peer without forwarder": func(c *config.RaftConfig) { c.Peers[0].ForwardAddress = "" },
	}
	for name, mut := range bad {
		c := good
		c.Peers = append([]config.RaftPeer(nil), good.Peers...)
		mut(&c)
		if _, err := effectiveRaftConfig(&c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// The package-level Submit/Ready reach a node started through StartCluster (real TCP transport, real forward
// listener, secret read from the configured file), and stop reaching it after Stop.
func TestStartCluster_SingleNodeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "fwd.key")
	if err := os.WriteFile(secretFile, bytes.Repeat([]byte("s"), 40), 0o600); err != nil {
		t.Fatal(err)
	}
	raftAddr, fwdAddr := freeAddr(t), freeAddr(t)
	rc := config.RaftConfig{
		NodeID: "solo", BindAddress: raftAddr, DataDir: filepath.Join(dir, "raft"), Bootstrap: true,
		Peers:              []config.RaftPeer{{ID: "solo", Address: raftAddr, ForwardAddress: fwdAddr}},
		HeartbeatTimeoutMs: 100, ElectionTimeoutMs: 100, LeaderLeaseTimeoutMs: 50, CommitTimeoutMs: 5,
		ForwardBindAddress: fwdAddr, ForwardSecretFile: secretFile, SequencerAddress: seqAddr.Hex(),
	}
	if err := ValidateConfig(&config.SimpleChainConfig{ConsensusMode: "raft", Raft: &rc}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	d := &disk{}
	sink := make(chan *pb.ExecutableBlock, 16)
	stopPipe := make(chan struct{})
	defer close(stopPipe)
	go d.run(sink, stopPipe)

	if Ready() || Submit(testBatch(t, 1)) {
		t.Fatal("Ready/Submit must be false before a node is started")
	}
	n, err := StartCluster(ClusterConfig{Raft: rc, Sink: sink, Durable: d.last.Load})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartCluster(ClusterConfig{Raft: rc, Sink: sink, Durable: d.last.Load}); err == nil {
		t.Fatal("a second cluster node was started in the same process")
	}
	deadline := time.Now().Add(10 * time.Second)
	for !Ready() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !Ready() {
		t.Fatal("node never became ready")
	}
	for i := 0; i < 5; i++ {
		for !Submit(testBatch(t, uint64(i))) {
			time.Sleep(2 * time.Millisecond)
		}
	}
	dl := time.Now().Add(10 * time.Second)
	for len(d.blocks()) < 5 && time.Now().Before(dl) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(d.blocks()); got != 5 {
		t.Fatalf("%d blocks, want 5", got)
	}
	n.Stop()
	if Ready() || Submit(testBatch(t, 9)) {
		t.Fatal("Ready/Submit must be false after Stop")
	}
}

func TestValidateConfig_Raft(t *testing.T) {
	if err := ValidateConfig(&config.SimpleChainConfig{ConsensusMode: "raft", Raft: &config.RaftConfig{}}); err == nil {
		t.Fatal("an empty raft block was accepted")
	}
	// A raft block is ignored unless consensus_mode is "raft" (default-off).
	if err := ValidateConfig(&config.SimpleChainConfig{Raft: &config.RaftConfig{}}); err != nil {
		t.Fatalf("raft block validated although consensus_mode is not raft: %v", err)
	}
	if err := ValidateConfig(&config.SimpleChainConfig{ConsensusMode: "raft"}); err != nil {
		t.Fatalf("raft mode without a raft block (single-node C1 feed) rejected: %v", err)
	}
}
