package raftfeed

import (
	"bytes"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func withConsensusMode(t *testing.T, cfg *config.SimpleChainConfig) {
	t.Helper()
	prev := config.ConfigApp
	config.ConfigApp = cfg
	t.Cleanup(func() { config.ConfigApp = prev })
}

// The raft feed must be OFF unless consensus_mode is exactly "raft" (default-off, T-OFF-*).
func TestEnabled_OnlyForExactRaftMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.SimpleChainConfig
		want bool
	}{
		{"nil config", nil, false},
		{"empty mode (default Rust consensus)", &config.SimpleChainConfig{}, false},
		{"raft", &config.SimpleChainConfig{ConsensusMode: "raft"}, true},
		{"unknown mode", &config.SimpleChainConfig{ConsensusMode: "rust"}, false},
		{"case differs", &config.SimpleChainConfig{ConsensusMode: "Raft"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withConsensusMode(t, tc.cfg)
			if got := Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func testBatch(t *testing.T, nonces ...uint64) []byte {
	t.Helper()
	var txs []types.Transaction
	for _, n := range nonces {
		txs = append(txs, transaction.NewTransaction(
			common.HexToAddress("0x1111111111111111111111111111111111111111"),
			common.HexToAddress("0x2222222222222222222222222222222222222222"),
			big.NewInt(1), 21000, 1, 1000, nil, nil, common.Hash{}, common.Hash{}, n, 1))
	}
	b, err := transaction.MarshalTransactions(txs)
	if err != nil {
		t.Fatalf("MarshalTransactions: %v", err)
	}
	return b
}

// fixedClock always returns the same instant, so the feeder's "timestamps never repeat" rule is what
// separates block timestamps.
func fixedClock() func() time.Time {
	t0 := time.UnixMilli(1_700_000_000_000)
	return func() time.Time { return t0 }
}

func startFeeder(t *testing.T, sinkCap int, mutate func(*StartConfig)) (*Feeder, chan *pb.ExecutableBlock) {
	t.Helper()
	sink := make(chan *pb.ExecutableBlock, sinkCap)
	cfg := StartConfig{
		Sink: sink, NextIndex: 10, NextBlock: 4, Epoch: 2,
		LeaderAddress: common.HexToAddress("0xAAAA000000000000000000000000000000000001"),
		Now:           fixedClock(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	f, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)
	return f, sink
}

func recv(t *testing.T, sink <-chan *pb.ExecutableBlock) *pb.ExecutableBlock {
	t.Helper()
	select {
	case b := <-sink:
		return b
	case <-time.After(5 * time.Second): // test guard only, never used by the feeder itself
		t.Fatal("no block delivered")
		return nil
	}
}

// Until a feeder runs, nothing may be accepted: the forwarder keeps the batch instead of losing it.
func TestSubmitAndReady_FailClosedWithoutFeeder(t *testing.T) {
	withConsensusMode(t, &config.SimpleChainConfig{ConsensusMode: "raft"})
	if Ready() {
		t.Fatal("Ready() must be false with no feeder running")
	}
	if Submit(testBatch(t, 1)) {
		t.Fatal("Submit() must not accept a batch with no feeder running")
	}
}

func TestStart_RejectsUnusableConfig(t *testing.T) {
	sink := make(chan *pb.ExecutableBlock, 1)
	for name, cfg := range map[string]StartConfig{
		"nil sink":    {NextIndex: 1, NextBlock: 1},
		"zero index":  {Sink: sink, NextIndex: 0, NextBlock: 1},
		"zero blocks": {Sink: sink, NextIndex: 1, NextBlock: 0},
	} {
		if f, err := Start(cfg); err == nil {
			f.Stop()
			t.Errorf("%s: Start must fail", name)
		}
	}
}

func TestStart_OnlyOneFeederAtATime(t *testing.T) {
	startFeeder(t, 1, nil)
	if f, err := Start(StartConfig{Sink: make(chan *pb.ExecutableBlock, 1), NextIndex: 1, NextBlock: 1}); err == nil {
		f.Stop()
		t.Fatal("a second Start while one is running must fail")
	}
}

// A batch becomes one block that carries exactly its transactions, with consecutive numbering.
func TestFeeder_BuildsConsecutiveBlocksFromBatches(t *testing.T) {
	_, sink := startFeeder(t, 8, nil)
	if !Ready() {
		t.Fatal("Ready() must be true once a feeder is running")
	}
	batches := [][]byte{testBatch(t, 1, 2, 3), testBatch(t, 4), testBatch(t, 5, 6)}
	for _, b := range batches {
		if !Submit(b) {
			t.Fatal("Submit must accept a batch while the queue has room")
		}
	}
	wantTxs := []int{3, 1, 2}
	var prevTs uint64
	var prevHash []byte
	for i := 0; i < 3; i++ {
		b := recv(t, sink)
		if b.GlobalExecIndex != uint64(10+i) || b.BlockNumber != uint64(4+i) || b.CommitIndex != uint32(10+i) {
			t.Errorf("block %d numbering = (gei %d, block %d, commit %d), want (%d, %d, %d)",
				i, b.GlobalExecIndex, b.BlockNumber, b.CommitIndex, 10+i, 4+i, 10+i)
		}
		if len(b.Transactions) != wantTxs[i] {
			t.Errorf("block %d has %d txs, want %d", i, len(b.Transactions), wantTxs[i])
		}
		if b.Epoch != 2 || !bytes.Equal(b.LeaderAddress, common.HexToAddress("0xAAAA000000000000000000000000000000000001").Bytes()) {
			t.Errorf("block %d epoch/leader not stamped from config", i)
		}
		if b.CommitTimestampMs <= prevTs {
			t.Errorf("block %d timestamp %d did not increase past %d", i, b.CommitTimestampMs, prevTs)
		}
		if len(b.CommitHash) != 32 || bytes.Equal(b.CommitHash, prevHash) {
			t.Errorf("block %d commit hash must be a fresh 32-byte value chained to the previous block", i)
		}
		prevTs, prevHash = b.CommitTimestampMs, b.CommitHash
	}
}

// Same batches, same start and same clock => byte-identical blocks (the property C2's replicas will rely on).
func TestFeeder_SameInputsGiveIdenticalBlocks(t *testing.T) {
	run := func() []*pb.ExecutableBlock {
		f, sink := startFeeder(t, 8, nil)
		for _, b := range [][]byte{testBatch(t, 1, 2), testBatch(t, 3)} {
			if !Submit(b) {
				t.Fatal("Submit rejected")
			}
		}
		out := []*pb.ExecutableBlock{recv(t, sink), recv(t, sink)}
		f.Stop()
		return out
	}
	a, b := run(), run()
	for i := range a {
		if a[i].GlobalExecIndex != b[i].GlobalExecIndex || a[i].CommitTimestampMs != b[i].CommitTimestampMs ||
			!bytes.Equal(a[i].CommitHash, b[i].CommitHash) || len(a[i].Transactions) != len(b[i].Transactions) {
			t.Fatalf("block %d differs between two identical runs", i)
		}
		for j := range a[i].Transactions {
			if !bytes.Equal(a[i].Transactions[j].Digest, b[i].Transactions[j].Digest) {
				t.Fatalf("block %d tx %d differs between two identical runs", i, j)
			}
		}
	}
}

// The submit queue is bounded: with a stalled consumer Submit must start returning false (never grow, never drop).
func TestFeeder_SubmitIsBoundedAndBackpressures(t *testing.T) {
	startFeeder(t, 1, nil) // sink holds 1 block and nobody reads it
	accepted := 0
	loopDone := make(chan struct{})
	go func() { // a blocking Submit must fail this test fast instead of hanging it
		defer close(loopDone)
		for i := 0; i < SubmitQueueCap+50; i++ {
			if Submit(testBatch(t, uint64(i+1))) {
				accepted++
			}
		}
	}()
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Submit blocked instead of returning false when the queue is full")
	}
	if accepted >= SubmitQueueCap+50 {
		t.Fatalf("all %d submits were accepted: the queue is not bounded", accepted)
	}
	if accepted > SubmitQueueCap+2 { // queue + the block being delivered + the one held by the sink
		t.Fatalf("accepted %d batches, more than the bound %d (+ in-flight)", accepted, SubmitQueueCap)
	}
	if Submit(testBatch(t, 9999)) {
		t.Fatal("Submit must keep returning false while the queue stays saturated")
	}
	if Ready() {
		t.Fatal("Ready() must be false while the submit queue is saturated")
	}
}

// A batch that can never become a block must not stop the feed (retrying it would block the pipeline forever).
func TestFeeder_DropsUnusableBatchesAndKeepsGoing(t *testing.T) {
	f, sink := startFeeder(t, 4, nil)
	empty, _ := transaction.MarshalTransactions(nil)
	for _, bad := range [][]byte{{0xff, 0xff, 0xff}, empty} {
		if !Submit(bad) {
			t.Fatal("Submit accepts the bytes; the feeder decides they are unusable")
		}
	}
	if !Submit(testBatch(t, 1)) {
		t.Fatal("Submit rejected a good batch")
	}
	b := recv(t, sink)
	if b.GlobalExecIndex != 10 {
		t.Fatalf("dropped batches must not consume numbers: first good block has GEI %d, want 10", b.GlobalExecIndex)
	}
	if f.Dropped() != 2 {
		t.Fatalf("Dropped() = %d, want 2", f.Dropped())
	}
}

// If numbering can no longer be represented in ExecutableBlock.CommitIndex (uint32) the feed stops itself
// instead of wrapping around and producing a duplicate index.
func TestFeeder_FailsClosedWhenCommitIndexWouldOverflow(t *testing.T) {
	f, sink := startFeeder(t, 4, func(c *StartConfig) { c.NextIndex = math.MaxUint32 })
	if !Submit(testBatch(t, 1)) {
		t.Fatal("Submit rejected")
	}
	if b := recv(t, sink); b.GlobalExecIndex != math.MaxUint32 || b.CommitIndex != math.MaxUint32 {
		t.Fatalf("the last representable index must still be delivered, got gei=%d commit=%d", b.GlobalExecIndex, b.CommitIndex)
	}
	if !Submit(testBatch(t, 2)) {
		t.Fatal("Submit rejected")
	}
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the feeder did not stop itself when numbering ran out")
	}
	if !f.Failed() {
		t.Fatal("the feeder must report Failed() after numbering ran out")
	}
	if Ready() || Submit(testBatch(t, 3)) {
		t.Fatal("a failed feeder must be neither ready nor accept batches")
	}
	select {
	case b := <-sink:
		t.Fatalf("no block may be produced after numbering ran out, got GEI %d", b.GlobalExecIndex)
	default:
	}
}

// Stop halts the feeder; nothing is accepted afterwards and a new one can be started.
func TestStop_HaltsAndAllowsRestart(t *testing.T) {
	f, _ := startFeeder(t, 2, nil)
	f.Stop()
	if Ready() || Submit(testBatch(t, 1)) {
		t.Fatal("a stopped feeder must be neither ready nor accept batches")
	}
	f2, err := Start(StartConfig{Sink: make(chan *pb.ExecutableBlock, 1), NextIndex: 1, NextBlock: 1})
	if err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	f2.Stop()
}

// Raft mode refuses configurations it cannot honour; every other mode is untouched (default-off).
func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.SimpleChainConfig
		wantErr bool
	}{
		{"nil", nil, false},
		{"default mode with snapshots", &config.SimpleChainConfig{SnapshotEnabled: true}, false},
		{"raft without snapshots", &config.SimpleChainConfig{ConsensusMode: "raft"}, false},
		{"raft with snapshots", &config.SimpleChainConfig{ConsensusMode: "raft", SnapshotEnabled: true}, true},
		{"unknown mode with snapshots", &config.SimpleChainConfig{ConsensusMode: "rust", SnapshotEnabled: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateConfig(tc.cfg); (err != nil) != tc.wantErr {
				t.Fatalf("ValidateConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
