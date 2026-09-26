package raftfeed

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	rpb "github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed/pb"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// A batch whose proposal failed (leadership change) is re-routed up to maxRerouteAttempts times, rerouteInterval
// apart, before it is counted as lost.
const (
	maxRerouteAttempts = 40
	rerouteInterval    = 250 * time.Millisecond
)

// ClusterConfig starts a replicated Raft node (plan C2).
type ClusterConfig struct {
	Raft config.RaftConfig
	// Sink is the BlockProcessor's bounded ingestion queue.
	Sink chan<- *pb.ExecutableBlock
	// Durable returns the last block number durably committed in the DB (storage.GetLastBlockNumber).
	Durable func() uint64
	// Now supplies the timestamp the LEADER stamps on a batch (defaults to time.Now).
	Now func() time.Time
	// OnFatal is called once, from a goroutine, when this replica must stop (cannot apply an entry exactly).
	OnFatal func(error)

	// Test hooks: an injected transport (in-memory, partitionable), an already bound forward listener, a secret.
	Tune            func(*raft.Config) // last word on the library config (e.g. a short snapshot interval)
	Transport       raft.Transport
	ForwardListener net.Listener
	Secret          []byte
}

type proposal struct {
	batch    []byte
	attempts int
}

type inflight struct {
	future raft.ApplyFuture
	p      proposal
}

// Node is one Raft replica. Leader: batches enter a bounded queue and are proposed. Follower: batches are
// forwarded to the leader over the internal HTTP channel. Every replica builds blocks in fsm.Apply.
type Node struct {
	cfg    config.RaftConfig
	raft   *raft.Raft
	fsm    *fsm
	secret []byte
	now    func() time.Time

	proposeQ chan proposal
	inflight chan inflight

	forwardAddr map[raft.ServerID]string
	client      *http.Client
	httpSrv     *http.Server

	stop     chan struct{}
	ready    chan struct{} // closed once n.raft is assigned
	stopOnce sync.Once
	wg       sync.WaitGroup
	stores   []interface{ Close() error }
	failed   atomic.Bool

	dropped atomic.Uint64 // batches that can never become a block (malformed / oversize)
	lost    atomic.Uint64 // batches whose proposal kept failing (reported, sender must resend)
}

var cluster atomic.Pointer[Node]

// StartCluster starts this replica and registers it as the target of Submit/Ready.
func StartCluster(cc ClusterConfig) (*Node, error) {
	if cluster.Load() != nil {
		return nil, errors.New("raftfeed: a cluster node is already started")
	}
	n, err := startNode(cc)
	if err != nil {
		return nil, err
	}
	if !cluster.CompareAndSwap(nil, n) { // only visible to Submit/Ready once fully started
		n.Stop()
		return nil, errors.New("raftfeed: a cluster node is already started")
	}
	return n, nil
}

// startNode builds and starts a replica without registering it globally (tests run several in one process).
func startNode(cc ClusterConfig) (*Node, error) {
	rc, err := effectiveRaftConfig(&cc.Raft)
	if err != nil {
		return nil, err
	}
	if cc.Sink == nil || cc.Durable == nil {
		return nil, errors.New("raftfeed: cluster needs a sink and a durable-block source")
	}
	if cc.Now == nil {
		cc.Now = time.Now
	}
	secret := cc.Secret
	if secret == nil {
		if secret, err = readForwardSecret(rc.ForwardSecretFile); err != nil {
			return nil, err
		}
	}
	n := &Node{
		cfg: rc, secret: secret, now: cc.Now,
		proposeQ:    make(chan proposal, rc.ProposeQueueSize),
		inflight:    make(chan inflight, rc.ProposeQueueSize),
		forwardAddr: map[raft.ServerID]string{},
		client: &http.Client{
			Timeout:       forwardTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		stop:  make(chan struct{}),
		ready: make(chan struct{}),
	}
	for _, p := range rc.Peers {
		n.forwardAddr[raft.ServerID(p.ID)] = p.ForwardAddress
	}
	if err := n.start(cc); err != nil {
		n.Stop()
		return nil, err
	}
	return n, nil
}

func (n *Node) start(cc ClusterConfig) error {
	rc := n.cfg
	if err := os.MkdirAll(rc.DataDir, 0o700); err != nil {
		return fmt.Errorf("raft data_dir: %w", err)
	}
	// Bolt stores fsync on every write (no NoSync): the power-loss incident showed what skipping that costs.
	logs, err := raftboltdb.New(raftboltdb.Options{Path: filepath.Join(rc.DataDir, "logs.db")})
	if err != nil {
		return fmt.Errorf("raft log store: %w", err)
	}
	n.stores = append(n.stores, logs)
	stable, err := raftboltdb.New(raftboltdb.Options{Path: filepath.Join(rc.DataDir, "stable.db")})
	if err != nil {
		return fmt.Errorf("raft stable store: %w", err)
	}
	n.stores = append(n.stores, stable)
	hcl := hclog.New(&hclog.LoggerOptions{Name: "raft", Level: hclog.Warn, Output: logWriter{}})
	snaps, err := raft.NewFileSnapshotStoreWithLogger(rc.DataDir, 2, hcl)
	if err != nil {
		return fmt.Errorf("raft snapshot store: %w", err)
	}

	hasState, err := raft.HasExistingState(logs, stable, snaps)
	if err != nil {
		return fmt.Errorf("raft state check: %w", err)
	}
	if !hasState && cc.Durable() > 0 {
		// Log positions and block numbers are only aligned for a chain that started with this cluster.
		return fmt.Errorf("raft state is empty but the chain already has block %d: cannot align (fresh chain required, plan C4)", cc.Durable())
	}

	trans := cc.Transport
	if trans == nil {
		adv, err := net.ResolveTCPAddr("tcp", rc.AdvertiseAddress)
		if err != nil {
			return fmt.Errorf("raft advertise_address: %w", err)
		}
		trans, err = raft.NewTCPTransportWithLogger(rc.BindAddress, adv, 3, 10*time.Second, hcl)
		if err != nil {
			return fmt.Errorf("raft transport: %w", err)
		}
	}

	n.fsm = newFSM(
		stamper{epoch: 0, leader: common.HexToAddress(rc.SequencerAddress), nextIndex: 1, nextBlock: 1},
		cc.Sink, cc.Durable, n.stop,
		func(err error) {
			n.failed.Store(true)
			if cc.OnFatal != nil {
				cc.OnFatal(err)
			}
			// Stop voting/applying; the rest of the cluster continues if it still has a quorum. n.raft is only
			// safe to read once start() has assigned it, hence the wait.
			select {
			case <-n.ready:
				n.raft.Shutdown()
			case <-n.stop:
			}
		},
	)

	rcfg := raft.DefaultConfig()
	rcfg.LocalID = raft.ServerID(rc.NodeID)
	rcfg.HeartbeatTimeout = time.Duration(rc.HeartbeatTimeoutMs) * time.Millisecond
	rcfg.ElectionTimeout = time.Duration(rc.ElectionTimeoutMs) * time.Millisecond
	rcfg.LeaderLeaseTimeout = time.Duration(rc.LeaderLeaseTimeoutMs) * time.Millisecond
	rcfg.CommitTimeout = time.Duration(rc.CommitTimeoutMs) * time.Millisecond
	rcfg.SnapshotInterval = time.Duration(rc.SnapshotIntervalS) * time.Second
	rcfg.SnapshotThreshold = uint64(rc.SnapshotThreshold)
	rcfg.TrailingLogs = uint64(rc.TrailingLogs)
	rcfg.Logger = hcl
	if cc.Tune != nil {
		cc.Tune(rcfg)
	}

	if n.raft, err = raft.NewRaft(rcfg, n.fsm, logs, stable, snaps, trans); err != nil {
		return fmt.Errorf("raft start: %w", err)
	}
	close(n.ready)
	if rc.Bootstrap && !hasState {
		var servers []raft.Server
		for _, p := range rc.Peers {
			servers = append(servers, raft.Server{Suffrage: raft.Voter, ID: raft.ServerID(p.ID), Address: raft.ServerAddress(p.Address)})
		}
		if err := n.raft.BootstrapCluster(raft.Configuration{Servers: servers}).Error(); err != nil {
			return fmt.Errorf("raft bootstrap: %w", err)
		}
	}

	ln := cc.ForwardListener
	if ln == nil {
		if ln, err = net.Listen("tcp", rc.ForwardBindAddress); err != nil {
			return fmt.Errorf("raft forward listener: %w", err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(submitPath, n.handleSubmit)
	n.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second}
	n.wg.Add(3)
	go func() { defer n.wg.Done(); _ = n.httpSrv.Serve(ln) }()
	go func() { defer n.wg.Done(); n.proposeLoop() }()
	go func() { defer n.wg.Done(); n.resultLoop() }()
	return nil
}

// Stop shuts the replica down. Batches still queued were only accepted, not replicated, and are dropped.
func (n *Node) Stop() {
	n.stopOnce.Do(func() {
		close(n.stop)
		if n.httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = n.httpSrv.Shutdown(ctx)
			cancel()
		}
		if n.raft != nil {
			_ = n.raft.Shutdown().Error()
		}
		n.wg.Wait()
		for _, s := range n.stores {
			_ = s.Close()
		}
		cluster.CompareAndSwap(n, nil)
	})
}

// Submit is the C2 backend of raftfeed.Submit: true only when the batch is accepted by the leader's bounded
// queue (locally or over the forward channel). It never blocks beyond the forward call's own transport timeout.
func (n *Node) Submit(batch []byte) bool {
	if n.failed.Load() {
		return false
	}
	if n.raft.State() == raft.Leader {
		return n.submitLocal(batch) == statusAccepted
	}
	return n.forwardToLeader(batch)
}

// submitLocal validates a batch, splits it if it exceeds max_batch_bytes and queues the pieces for proposal.
// A malformed or empty batch can never become a block and would halt every replica if proposed, so it is
// dropped here (counted), exactly like the C1 feeder.
func (n *Node) submitLocal(batch []byte) submitStatus {
	if n.failed.Load() {
		return statusNoLeader
	}
	if n.raft.State() != raft.Leader {
		if _, id := n.raft.LeaderWithID(); id == "" {
			return statusNoLeader
		}
		return statusRedirect
	}
	pieces, err := splitBatch(batch, n.cfg.MaxBatchBytes)
	if err != nil {
		n.dropped.Add(1)
		logger.Error("❌ [RAFT] dropping a batch that cannot become a block: %v", err)
		return statusAccepted
	}
	if cap(n.proposeQ)-len(n.proposeQ) < len(pieces) {
		return statusFull
	}
	for _, p := range pieces {
		select {
		case n.proposeQ <- proposal{batch: p}:
		default:
			return statusFull // partial acceptance only ever duplicates on retry; Go dedups by tx hash
		}
	}
	return statusAccepted
}

// splitBatch returns the batch as-is when it fits, else greedily regroups its txs into batches of at most max
// bytes. A single tx above max is an error (it could never be proposed).
func splitBatch(batch []byte, max int) ([][]byte, error) {
	txs, err := transaction.UnmarshalTransactions(batch)
	if err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}
	if len(txs) == 0 {
		return nil, errors.New("empty batch")
	}
	if len(batch) <= max {
		return [][]byte{batch}, nil
	}
	var out [][]byte
	start, size := 0, 0
	flush := func(end int) error {
		if end == start {
			return nil
		}
		b, err := transaction.MarshalTransactions(txs[start:end])
		if err != nil {
			return err
		}
		out = append(out, b)
		start, size = end, 0
		return nil
	}
	for i, tx := range txs {
		raw, err := tx.Marshal()
		if err != nil {
			return nil, err
		}
		if len(raw) > max {
			return nil, fmt.Errorf("tx %d is %d bytes, above max_batch_bytes %d", i, len(raw), max)
		}
		if size+len(raw) > max {
			if err := flush(i); err != nil {
				return nil, err
			}
		}
		size += len(raw)
	}
	return out, flush(len(txs))
}

// proposeLoop is the only caller of raft.Apply on this node.
func (n *Node) proposeLoop() {
	for {
		select {
		case <-n.stop:
			return
		case p := <-n.proposeQ:
			txs, err := transaction.UnmarshalTransactions(p.batch)
			if err != nil || len(txs) == 0 {
				n.dropped.Add(1)
				continue
			}
			data, err := proto.MarshalOptions{Deterministic: true}.Marshal(&rpb.BatchRecord{
				SchemaVersion: schemaVersion,
				TimestampMs:   uint64(n.now().UnixMilli()),
				Txs:           p.batch,
				TxCount:       uint32(len(txs)),
				ProposerId:    n.cfg.NodeID,
			})
			if err != nil {
				n.dropped.Add(1)
				continue
			}
			f := n.raft.Apply(data, 0)
			select {
			case n.inflight <- inflight{future: f, p: p}:
			case <-n.stop:
				return
			}
		}
	}
}

// resultLoop waits for each proposal in order. A failed proposal (leadership lost before commit) is routed
// again to whoever leads now, a bounded number of times; the batch is never silently forgotten.
func (n *Node) resultLoop() {
	for {
		select {
		case <-n.stop:
			return
		case in := <-n.inflight:
			if err := in.future.Error(); err != nil {
				if errors.Is(err, raft.ErrRaftShutdown) {
					return
				}
				n.reroute(in.p, err)
				continue
			}
			if e, ok := in.future.Response().(error); ok {
				logger.Error("❌ [RAFT] entry applied with error: %v", e)
			}
		}
	}
}

// reroute hands a batch whose proposal failed to whoever leads now. It retries while no leader is reachable
// (a cut-off old leader only learns of the new one after the partition heals), for a bounded number of tries.
// The interval only paces re-submission; it never decides whether anything is executed. Re-proposing can
// duplicate a batch that did commit; Go dedups by tx hash (T-SUB-05).
func (n *Node) reroute(p proposal, cause error) {
	for p.attempts = 0; p.attempts < maxRerouteAttempts; p.attempts++ {
		if n.raft.State() == raft.Leader {
			select {
			case n.proposeQ <- p:
				return
			default:
			}
		} else if n.forwardToLeader(p.batch) {
			return
		}
		select {
		case <-n.stop:
			return
		case <-time.After(rerouteInterval):
		}
	}
	n.lost.Add(1)
	logger.Error("🚨 [RAFT] batch lost after %d re-route attempts (proposal failed: %v): its transactions must be resent", maxRerouteAttempts, cause)
}

// Ready reports whether this node may accept transactions: a leader is known, the replica has not failed and,
// on the leader, the propose queue has room.
func (n *Node) Ready() bool {
	if n.failed.Load() {
		return false
	}
	if n.raft.State() == raft.Leader {
		return len(n.proposeQ) < cap(n.proposeQ)
	}
	_, id := n.raft.LeaderWithID()
	return id != ""
}

// IsLeader reports whether this replica currently leads.
func (n *Node) IsLeader() bool { return n.raft.State() == raft.Leader }

// AppliedIndex is the last Raft index applied to the FSM.
func (n *Node) AppliedIndex() uint64 { return n.raft.AppliedIndex() }

// Dropped / Lost / Skipped / Failed expose the counters used by tests and diagnostics.
func (n *Node) Dropped() uint64 { return n.dropped.Load() }
func (n *Node) Lost() uint64    { return n.lost.Load() }
func (n *Node) Skipped() uint64 { return n.fsm.skipped.Load() }
func (n *Node) Failed() bool    { return n.failed.Load() }

// logWriter routes hashicorp's logger into the project logger.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	logger.Warn("%s", strings.TrimSpace(string(p)))
	return len(p), nil
}
