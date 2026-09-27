package raftfeed

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// anySubmit offers a batch to the started replicas in turn until one acknowledges it (a client that can reach
// any RPC node).
func (h *harness) anySubmit(batch []byte, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		var nodes []*Node
		for _, m := range h.m {
			if m.node != nil {
				nodes = append(nodes, m.node)
			}
		}
		h.mu.Unlock()
		for _, n := range nodes {
			if n.Submit(batch) {
				return true
			}
		}
		time.Sleep(3 * time.Millisecond)
	}
	return false
}

// load sends unique txs continuously until stopped and reports how many were acknowledged.
type load struct {
	wg      sync.WaitGroup
	stop    atomic.Bool
	next    atomic.Uint64
	acked   atomic.Int64
	failed  atomic.Int64
	started uint64
}

func (h *harness) startLoad(t *testing.T, workers int, firstNonce uint64) *load {
	l := &load{started: firstNonce}
	l.next.Store(firstNonce)
	for w := 0; w < workers; w++ {
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			for !l.stop.Load() {
				n := l.next.Add(1) - 1
				if h.anySubmit(testBatch(t, n), 30*time.Second) {
					l.acked.Add(1)
				} else {
					l.failed.Add(1)
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	return l
}

func (l *load) finish() { l.stop.Store(true); l.wg.Wait() }

// every acknowledged nonce in [first, first+n) must have been executed (the tx of an un-acked submit may or
// may not have been).
func assertAllExecuted(t *testing.T, m *member, first, n uint64) {
	t.Helper()
	seen := map[uint64]bool{}
	for _, b := range m.disk.blocks() {
		seen[nonceOf(t, b)] = true
	}
	for i := first; i < first+n; i++ {
		if !seen[i] {
			t.Fatalf("%s never executed nonce %d", m.id, i)
		}
	}
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// T-CL-01 / T-CL-02: `check` on a healthy cluster.
func TestC4_CheckHealthyCluster(t *testing.T) {
	old := attestInterval
	attestInterval = 50 * time.Millisecond
	t.Cleanup(func() { attestInterval = old })
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 25; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(25, h.m...)
	rep := h.admin().Check(h.members())
	if rep.Leader != l.id || !rep.QuorumOK || !rep.KeyConsistent || len(rep.Problems) != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.StateRootConsistent == nil || !*rep.StateRootConsistent || rep.CheckpointBlock != 20 {
		t.Fatalf("state consistency at the checkpoint not confirmed: %+v", rep)
	}
	for _, r := range rep.Replicas {
		if !r.Reachable || !r.Voter || r.State == "" {
			t.Fatalf("replica line wrong: %+v", r)
		}
	}
}

// T-CL-02: replicas that would sign as different addresses are flagged.
func TestC4_CheckFlagsDifferentSigningKeys(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(rc *config.RaftConfig) {
		if rc.NodeID == "n2" {
			rc.SequencerAddress = "0x00000000000000000000000000000000000000aa"
		}
	}
	h.startAll()
	rep := h.admin().Check(h.members())
	if rep.KeyConsistent {
		t.Fatalf("different sequencer addresses went unnoticed: %+v", rep)
	}
}

// T-LD-01: leadership moves to the chosen follower while transactions keep flowing; none is lost.
func TestC4_TransferLeaderUnderLoad(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	old := h.leader()
	var target *member
	for _, m := range h.m {
		if m != old {
			target = m
		}
	}
	ld := h.startLoad(t, 4, 0)
	time.Sleep(300 * time.Millisecond)
	steps, err := h.admin().TransferLeader(h.members(), target.id, false)
	if err != nil {
		t.Fatalf("transfer: %v (steps %v)", err, steps)
	}
	time.Sleep(300 * time.Millisecond)
	ld.finish()
	if !target.node.IsLeader() || old.node.IsLeader() {
		t.Fatal("leadership did not move")
	}
	if ld.failed.Load() != 0 {
		t.Fatalf("%d submits were never acknowledged", ld.failed.Load())
	}
	acked := uint64(ld.acked.Load())
	h.waitBlocks(int(acked), h.m...)
	time.Sleep(300 * time.Millisecond)
	h.assertIdentical(h.m...)
	for _, m := range h.m {
		assertAllExecuted(t, m, 0, acked)
	}
	if h.m[0].node.Status().Draining || target.node.Status().Draining || old.node.Status().Draining {
		t.Fatal("a replica stayed in draining mode")
	}
}

// T-LD-02: a target that is behind is refused.
func TestC4_TransferRefusedForLaggingTarget(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	var target *member
	for _, m := range h.m {
		if m != l {
			target = m
		}
	}
	h.isolate(target) // its HTTP endpoint still answers, its Raft transport does not
	for i := 0; i < 120; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	_, err := h.admin().TransferLeader(h.members(), target.id, false)
	if err == nil || !strings.Contains(err.Error(), "behind") {
		t.Fatalf("expected a refusal for a lagging target, got %v", err)
	}
	if !l.node.IsLeader() {
		t.Fatal("leadership moved although the tool refused")
	}
}

// T-LD-03: removing a replica that would cost the quorum is refused; removing the dead one is allowed.
func TestC4_RemoveRefusedWhenQuorumWouldBeLost(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	var dead, live *member
	for _, m := range h.m {
		if m == l {
			continue
		}
		if dead == nil {
			dead = m
		} else {
			live = m
		}
	}
	h.stop(dead)
	_, err := h.admin().RemoveReplica(h.members(), live.id, false, false)
	if err == nil || !strings.Contains(err.Error(), "quorum") {
		t.Fatalf("removing a live replica of a 3-voter cluster with one dead must be refused, got %v", err)
	}
	// the node itself refuses too (a client that skips the pre-check)
	if err := l.node.RemoveMember(live.id); err == nil {
		t.Fatal("the leader accepted a removal that loses the quorum")
	}
	if _, err := h.admin().RemoveReplica(h.members(), l.id, false, false); err == nil {
		t.Fatal("removing the leader without --transfer-first must be refused")
	}
	if _, err := h.admin().RemoveReplica(append(h.members(), Member{ID: dead.id, RaftAddr: string(dead.addr), AdminAddr: dead.fwdAddr}), dead.id, false, false); err != nil {
		t.Fatalf("removing the dead replica must be allowed: %v", err)
	}
}

// T-LD-06: --dry-run prints the steps and changes nothing.
func TestC4_DryRunChangesNothing(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	var target *member
	for _, m := range h.m {
		if m != l {
			target = m
		}
	}
	steps, err := h.admin().TransferLeader(h.members(), target.id, true)
	if err != nil || len(steps) < 3 {
		t.Fatalf("dry run: %v %v", steps, err)
	}
	time.Sleep(200 * time.Millisecond)
	if !l.node.IsLeader() {
		t.Fatal("a dry run moved the leader")
	}
	n3 := h.newMember("n3")
	h.join = map[string]bool{"n3": true}
	h.start(n3)
	if _, err := h.admin().AddReplica(h.members()[:3], Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr}, true); err != nil {
		t.Fatal(err)
	}
	for _, s := range l.node.Status().Servers {
		if s.ID == "n3" {
			t.Fatal("a dry run added the replica")
		}
	}
}

// T-LD-04: a replica whose DB was copied from a stopped peer is added, catches up, is checked, promoted; from then
// on it holds exactly the same chain and counts toward the majority.
func TestC4_AddReplicaWithCopiedState(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 30; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(30, h.m...)
	var src *member
	for _, m := range h.m {
		if m != l {
			src = m
		}
	}
	h.stop(src) // copy only from a STOPPED peer
	n3 := h.newMember("n3")
	copyState(src, n3)
	h.join = map[string]bool{"n3": true}
	h.start(src)
	for i := 30; i < 50; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.start(n3)
	base := h.members()[:3]
	steps, err := h.admin().AddReplica(base, Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr}, false)
	if err != nil {
		t.Fatalf("add-replica: %v (steps %v)", err, steps)
	}
	h.waitBlocks(50, h.m...)
	h.assertIdentical(h.m...)
	voter := false
	for _, s := range l.node.Status().Servers {
		if s.ID == "n3" && s.Voter {
			voter = true
		}
	}
	if !voter {
		t.Fatal("n3 was not promoted to voter")
	}
	// it counts: with two of the four voters gone... a 4-voter cluster needs 3; stop one old replica, still fine
	h.stop(src)
	for i := 50; i < 60; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(60, l, n3)
}

// A replica with an EMPTY state can join when the leader's log is complete: it replays everything.
func TestC4_AddEmptyReplicaReplaysCompleteLog(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 25; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(25, h.m...)
	n3 := h.newMember("n3")
	h.join = map[string]bool{"n3": true}
	h.start(n3)
	if _, err := h.admin().AddReplica(h.members()[:3], Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr}, false); err != nil {
		t.Fatal(err)
	}
	h.waitBlocks(25, n3)
	h.assertIdentical(h.m...)
}

// With a compacted log an empty replica cannot be served: the tool refuses up front; if the operator forces the
// membership change anyway, the replica stops itself (fail closed) and the cluster is unaffected.
func TestC4_EmptyReplicaRefusedWhenLogCompacted(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(rc *config.RaftConfig) { rc.TrailingLogs = 5; rc.SnapshotThreshold = 1 << 30 }
	h.startAll()
	l := h.leader()
	for i := 0; i < 40; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(40, h.m...)
	if err := l.node.raft.Snapshot().Error(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "log compaction", 10*time.Second, func() bool { return l.node.Status().FirstLogIndex > 1 })

	n3 := h.newMember("n3")
	h.join = map[string]bool{"n3": true}
	h.start(n3)
	_, err := h.admin().AddReplica(h.members()[:3], Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr}, false)
	if err == nil || !strings.Contains(err.Error(), "compacted") {
		t.Fatalf("expected a refusal about the compacted log, got %v", err)
	}
	// force it at the node level
	if err := l.node.AddMember("n3", "n3", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the empty replica to stop itself", 15*time.Second, func() bool { return n3.node.Failed() })
	for i := 40; i < 50; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(50, h.m[:3]...)
	h.assertIdentical(h.m[:3]...)
}

// A copied state that is not this chain's is refused before it can vote.
func TestC4_AddReplicaRefusedForForeignState(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 20; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(20, h.m...)
	n3 := h.newMember("n3")
	copyState(h.m[0], n3)
	n3.disk.mu.Lock()
	n3.disk.received[len(n3.disk.received)-1].CommitHash[0] ^= 0xff // a corrupted / foreign tip
	n3.disk.mu.Unlock()
	h.join = map[string]bool{"n3": true}
	h.start(n3)
	_, err := h.admin().AddReplica(h.members()[:3], Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr}, false)
	if err == nil || !strings.Contains(err.Error(), "not a copy of this chain") {
		t.Fatalf("expected a refusal for a foreign state, got %v", err)
	}
}

// Node-level guards hold even when a client skips the tool's checks.
func TestC4_NodeRefusesUnsafeMembershipCalls(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	if err := l.node.AddMember("n9", "n9", true); err == nil {
		t.Fatal("a brand-new replica was added straight as a voter")
	}
	if err := l.node.RemoveMember(l.id); err == nil {
		t.Fatal("the leader removed itself")
	}
	if err := l.node.TransferLeadership(l.id); err == nil {
		t.Fatal("transfer to self accepted")
	}
	for _, m := range h.m {
		if m != l {
			if err := m.node.RemoveMember(l.id); err == nil {
				t.Fatal("a follower executed a leader-only operation")
			}
		}
	}
	if err := l.node.AddMember("n0x", "n0x", false); err != nil {
		t.Fatal(err)
	}
	// a non-voter that does not exist as a process cannot be promoted
	if err := l.node.AddMember("n0x", "n0x", true); err == nil {
		t.Fatal("an unreachable non-voter was promoted")
	}
}

// T-LD-05: a replica that died for good is replaced: a new one joins from a copied state, the dead one is removed,
// and transactions in flight are not interrupted.
func TestC4_ReplaceDeadReplica(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	var dead, src *member
	for _, m := range h.m {
		if m == l {
			continue
		}
		if dead == nil {
			dead = m
		} else {
			src = m
		}
	}
	for i := 0; i < 20; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(20, h.m...)
	h.stop(dead) // gone for good
	ld := h.startLoad(t, 3, 100)
	time.Sleep(200 * time.Millisecond)

	h.stop(src) // briefly stopped so its state can be copied (2 of 3 voters gone: the load stalls, nothing is lost)
	n3 := h.newMember("n3")
	copyState(src, n3)
	h.join = map[string]bool{"n3": true}
	h.start(src)
	h.start(n3)
	waitFor(t, "a leader", 10*time.Second, func() bool {
		for _, m := range h.m {
			if m.node != nil && m.node.IsLeader() {
				return true
			}
		}
		return false
	})
	live := []Member{}
	for _, m := range h.m {
		if m.node != nil && m.id != "n3" {
			live = append(live, Member{ID: m.id, RaftAddr: string(m.addr), AdminAddr: m.fwdAddr})
		}
	}
	waitFor(t, "a quorum of the two survivors", 15*time.Second, func() bool { return h.admin().Check(live).QuorumOK })
	var addErr error
	for try := 0; try < 5; try++ { // the survivors just re-formed: a transient "leadership lost" is retried like an operator would
		if _, addErr = h.admin().AddReplica(live, Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr}, false); addErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if addErr != nil {
		t.Fatalf("add: %v", addErr)
	}
	all := append(live, Member{ID: dead.id, RaftAddr: string(dead.addr), AdminAddr: dead.fwdAddr}, Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr})
	if _, err := h.admin().RemoveReplica(all, dead.id, false, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	ld.finish()
	if ld.failed.Load() != 0 {
		t.Fatalf("%d submits were never acknowledged", ld.failed.Load())
	}
	rep := h.admin().Check(append(append([]Member(nil), live...), Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr})) // the removed one is no longer part of the cluster
	voters := 0
	for _, r := range rep.Replicas {
		if r.Voter {
			voters++
		}
	}
	if voters != 3 || !rep.QuorumOK {
		t.Fatalf("the cluster is not back to 3 voters: %+v", rep)
	}
	var alive []*member
	for _, m := range h.m {
		if m.node != nil {
			alive = append(alive, m)
		}
	}
	h.waitBlocks(20+int(ld.acked.Load()), alive...)
	time.Sleep(300 * time.Millisecond)
	h.assertIdentical(alive...)
}

// Starting with empty Raft state on a DB that already has blocks is refused unless the operator says the state was
// copied from a peer.
func TestC4_ExistingChainNeedsJoinFlag(t *testing.T) {
	h := newHarness(t, 3)
	n3 := h.newMember("n3")
	n3.disk.last.Store(7)
	n3.disk.received = nil
	_, tr := raft.NewInmemTransport(n3.addr)
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	defer ln.Close()
	_, err := startNode(ClusterConfig{Raft: h.rcfg(n3), Sink: make(chan *pb.ExecutableBlock, 1), Durable: n3.disk.last.Load, Secret: h.secret, Transport: tr, ForwardListener: ln})
	if err == nil || !strings.Contains(err.Error(), "join_existing_chain") {
		t.Fatalf("expected a refusal mentioning join_existing_chain, got %v", err)
	}
}

func TestForwardPortOffset(t *testing.T) {
	if a, ok := derivedForwardAddr("10.0.0.5:7100", 100); !ok || a != "10.0.0.5:7200" {
		t.Fatalf("got %q %v", a, ok)
	}
	good := config.RaftConfig{
		NodeID: "a", BindAddress: "0.0.0.0:7100", AdvertiseAddress: "10.0.0.1:7100", DataDir: "/d", ForwardBindAddress: "0.0.0.0:7200", ForwardSecretFile: "/s",
		SequencerAddress: seqAddr.Hex(), ForwardPortOffset: 100,
		Peers: []config.RaftPeer{{ID: "a", Address: "10.0.0.1:7100"}},
	}
	if _, err := effectiveRaftConfig(&good); err != nil {
		t.Fatalf("valid offset config rejected: %v", err)
	}
	bad := good
	bad.ForwardBindAddress = "0.0.0.0:7300"
	if _, err := effectiveRaftConfig(&bad); err == nil {
		t.Fatal("a forward port that does not match the offset was accepted")
	}
	both := good
	both.Bootstrap, both.JoinExistingChain = true, true
	if _, err := effectiveRaftConfig(&both); err == nil {
		t.Fatal("bootstrap + join_existing_chain accepted")
	}
}

// A non-voter that is behind must not be promoted, even by a client that skips the tool's checks.
func TestC4_NodeRefusesPromotingALaggingReplica(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	n3 := h.newMember("n3")
	h.join = map[string]bool{"n3": true}
	h.start(n3)
	if err := l.node.AddMember("n3", "n3", false); err != nil {
		t.Fatal(err)
	}
	h.isolate(n3) // its HTTP endpoint answers, its Raft transport does not: it cannot catch up
	for i := 0; i < 150; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	err := l.node.AddMember("n3", "n3", true)
	if err == nil || !strings.Contains(err.Error(), "behind") {
		t.Fatalf("a lagging non-voter was promoted (or wrong error): %v", err)
	}
}

// A draining leader accepts nothing new and reports itself not ready, so the forwarder keeps its batches.
func TestC4_DrainingLeaderAcceptsNothing(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	l.node.draining.Store(true)
	if l.node.Submit(testBatch(t, 1)) || l.node.Ready() {
		t.Fatal("a draining leader accepted a batch or claimed to be ready")
	}
	if st := l.node.submitLocal(testBatch(t, 2)); st == statusAccepted {
		t.Fatal("the forward endpoint accepted a batch while draining")
	}
	l.node.draining.Store(false)
	h.submit(l, testBatch(t, 3))
}

// A replica that applied the log but has not EXECUTED it yet (long replay) must not be promoted: it would count
// toward the majority while far behind in state.
func TestC4_NodeRefusesPromotingAReplicaThatHasNotExecuted(t *testing.T) {
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for i := 0; i < 150; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(150, h.m...)
	n3 := h.newMember("n3")
	n3.disk.mu.Lock()
	n3.disk.hold = true // the pipeline receives blocks but never makes them durable: applied yes, executed no
	n3.disk.mu.Unlock()
	h.join = map[string]bool{"n3": true}
	h.start(n3)
	if err := l.node.AddMember("n3", "n3", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "n3 to apply the log", 15*time.Second, func() bool { return n3.node.Status().AppliedIndex+promoteMaxLag >= l.node.Status().CommitIndex })
	err := l.node.AddMember("n3", "n3", true)
	if err == nil || !strings.Contains(err.Error(), "executed") {
		t.Fatalf("a replica that has not executed the log was promoted (or wrong error): %v", err)
	}
	rep := h.admin().Check(h.members())
	if r := rep.replica("n3"); r == nil || r.LagBlocks < promoteMaxLag {
		t.Fatalf("check does not show the execution lag: %+v", r)
	}
}

// While snapshots are held no replica compacts its log (so a state copied meanwhile stays newer than the leader's
// latest snapshot); releasing resumes it; a forgotten hold releases itself.
func TestC4_HoldSnapshotsStopsCompactionAndReleases(t *testing.T) {
	h := newHarness(t, 3)
	h.tune = func(c *raft.Config) {
		c.SnapshotInterval = 50 * time.Millisecond
		c.SnapshotThreshold = 10
		c.TrailingLogs = 5
	}
	h.startAll()
	l := h.leader()
	if errs := h.admin().HoldSnapshots(h.members(), true); len(errs) > 0 {
		t.Fatal(errs)
	}
	for i := 0; i < 80; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(80, h.m...)
	time.Sleep(600 * time.Millisecond) // many snapshot intervals
	for _, m := range h.m {
		st := m.node.Status()
		if !st.SnapshotsHeld || st.LastSnapshotIndex != 0 || st.FirstLogIndex > 1 {
			t.Fatalf("%s compacted while snapshots were held: %+v", m.id, st)
		}
	}
	if errs := h.admin().HoldSnapshots(h.members(), false); len(errs) > 0 {
		t.Fatal(errs)
	}
	waitFor(t, "compaction after the release", 10*time.Second, func() bool { return l.node.Status().LastSnapshotIndex > 0 })
}

func TestC4_ForgottenSnapshotHoldReleasesItself(t *testing.T) {
	old := snapshotHoldMax
	snapshotHoldMax = 150 * time.Millisecond
	t.Cleanup(func() { snapshotHoldMax = old })
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	if err := l.node.HoldSnapshots(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the hold to expire", 5*time.Second, func() bool { return !l.node.Status().SnapshotsHeld })
}
