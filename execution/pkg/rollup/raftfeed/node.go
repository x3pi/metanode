package raftfeed

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	"github.com/meta-node-blockchain/meta-node/types"
)

// submitCommitTimeout is how long Submit waits for its batch to be COMMITTED (replicated to a quorum) before it
// tells the caller to retry. It only decides what the caller is told: the FSM executes committed entries only, so
// a timeout can at worst make the caller send a batch twice (Go dedups by tx hash), never lose or fork anything.
const submitCommitTimeout = 5 * time.Second

// ClusterConfig starts a replicated Raft node (plan C2).
type ClusterConfig struct {
	Raft config.RaftConfig
	// Sink is the BlockProcessor's bounded ingestion queue.
	Sink chan<- *pb.ExecutableBlock
	// Durable returns the last block number durably committed in the DB (storage.GetLastBlockNumber).
	Durable func() uint64
	// BlockHash returns the header hash of a durable block for the cross-replica state check (nil disables it).
	BlockHash BlockHashFunc
	// Now supplies the timestamp the LEADER stamps on a batch (defaults to time.Now).
	Now func() time.Time
	// OnFatal is called once, from a goroutine, when this replica must stop (cannot apply an entry exactly).
	OnFatal func(error)

	// StateSource + StateTransferDir enable serving a consistent snapshot of this node to a new replica (C4).
	// StateTransferDir must be on the same filesystem as the database root.
	StateSource      StateSource
	StateTransferDir string
	// ForwardAddrOf is a test hook resolving members that are in neither peers[] nor derivable by offset.
	ForwardAddrOf func(raft.ServerID) (string, bool)

	// Test hooks: an injected transport (in-memory, partitionable), an already bound forward listener, a secret.
	WrapLogStore    func(raft.LogStore) raft.LogStore // test hook: inject log-store faults
	Tune            func(*raft.Config)                // last word on the library config (e.g. a short snapshot interval)
	Transport       raft.Transport
	ForwardListener net.Listener
	Secret          []byte
}

// proposal is one batch on its way into the log; done receives the outcome of its commit exactly once.
type proposal struct {
	batch      []byte
	done       chan error
	receivedAt time.Time
}

func (p proposal) finish(err error) {
	select {
	case p.done <- err:
	default:
	}
}

type inflight struct {
	future     raft.ApplyFuture
	p          proposal
	applyStart time.Time
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
	metrics  *Metrics

	forwardAddr   map[raft.ServerID]string
	forwardAddrOf func(raft.ServerID) (string, bool) // test hook (members the static list does not know)
	member        membership
	state         stateTransfer
	logStore      raft.LogStore
	snapMu        sync.Mutex
	snapHold      atomic.Bool
	snapTimer     *time.Timer
	reloadBase    raft.ReloadableConfig // the configured values a released hold goes back to
	draining      atomic.Bool           // leader is being drained for a leadership transfer: accept no new batches
	client        *http.Client
	httpSrv       *http.Server

	stop     chan struct{}
	ready    chan struct{} // closed once n.raft is assigned
	stopOnce sync.Once
	wg       sync.WaitGroup
	stores   []interface{ Close() error }
	failed   atomic.Bool

	blockHash  BlockHashFunc
	durable    func() uint64
	fatal      func(error) // fail-closed exit for this replica (set in start)
	mismatches atomic.Uint64
	attested   atomic.Uint64

	dropped atomic.Uint64 // batches that can never become a block (malformed / oversize)
}

var cluster atomic.Pointer[Node]

// GetNode returns the global Node instance, if any.
func GetNode() *Node {
	return cluster.Load()
}

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
		proposeQ:      make(chan proposal, rc.ProposeQueueSize),
		inflight:      make(chan inflight, rc.ProposeQueueSize),
		metrics:       &Metrics{},
		forwardAddr:   map[raft.ServerID]string{},
		forwardAddrOf: cc.ForwardAddrOf,
		state:         stateTransfer{base: cc.StateTransferDir, source: cc.StateSource},
		client: &http.Client{
			Timeout:       forwardTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		stop:  make(chan struct{}),
		ready: make(chan struct{}),
	}
	for _, p := range rc.Peers {
		if p.ForwardAddress != "" {
			n.forwardAddr[raft.ServerID(p.ID)] = p.ForwardAddress
		}
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

	n.cleanStaging()
	hasState, err := raft.HasExistingState(logs, stable, snaps)
	if err != nil {
		return fmt.Errorf("raft state check: %w", err)
	}
	if !hasState && cc.Durable() > 0 && !rc.JoinExistingChain {
		// Log positions and block numbers are only aligned for a chain that started with this cluster, or for a
		// replica that joins with a DB copied from a peer (join_existing_chain): then log/snapshot replay skips the
		// blocks the DB already has and the FSM counters come from the log/snapshot, not from the DB.
		return fmt.Errorf("raft state is empty but the chain already has block %d: set raft.join_existing_chain only if this data directory was copied from a stopped peer (rollup-cluster prepare-replica)", cc.Durable())
	}

	trans := cc.Transport
	if trans == nil {
		adv, err := net.ResolveTCPAddr("tcp", rc.AdvertiseAddress)
		if err != nil {
			return fmt.Errorf("raft advertise_address: %w", err)
		}
		streamLayer, err := newAuthTCPStreamLayer(rc.BindAddress, adv, n.secret, rc.Peers)
		if err != nil {
			return fmt.Errorf("raft transport: %w", err)
		}
		trans = raft.NewNetworkTransportWithLogger(streamLayer, 3, 10*time.Second, hcl)
	}

	// One fail-closed exit for everything that must take this replica out: an entry the FSM cannot apply, or a
	// log store that stopped accepting writes.
	onFatal := func(err error) {
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
	}
	n.fatal = onFatal
	n.blockHash, n.durable = wrapBlockHash(cc.BlockHash), cc.Durable
	n.fsm = newFSM(
		stamper{epoch: 0, leader: common.HexToAddress(rc.SequencerAddress), nextIndex: 1, nextBlock: 1},
		cc.Sink, cc.Durable, n.stop, onFatal,
	)
	n.fsm.metrics = n.metrics

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
	n.reloadBase = raft.ReloadableConfig{
		TrailingLogs: rcfg.TrailingLogs, SnapshotInterval: rcfg.SnapshotInterval, SnapshotThreshold: rcfg.SnapshotThreshold,
		HeartbeatTimeout: rcfg.HeartbeatTimeout, ElectionTimeout: rcfg.ElectionTimeout,
	}

	var logStore raft.LogStore = logs
	if cc.WrapLogStore != nil {
		logStore = cc.WrapLogStore(logs)
	}
	// A log write that fails may or may not have reached the disk: continuing (as a leader that keeps sending
	// heartbeats, or a follower that acks) could acknowledge entries that are not durable. Leave the cluster.
	logStore = &failClosedLogStore{LogStore: logStore, metrics: n.metrics, onFatal: func(err error) { go onFatal(err) }}
	n.logStore = logStore
	if n.raft, err = raft.NewRaft(rcfg, n.fsm, logStore, stable, snaps, trans); err != nil {
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
	mux.HandleFunc(hashPath, n.handleBlockHash)
	mux.HandleFunc("/raft/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !n.verifyForwardAuth(r, nil) && !isRaftLoopbackAddr(r.RemoteAddr) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, n.metrics.Snapshot())
	})
	n.registerAdmin(mux)
	n.registerState(mux)
	n.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second}
	n.wg.Add(4)
	go func() { defer n.wg.Done(); n.attestLoop() }()
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

// Submit is the C2 backend of raftfeed.Submit: true only when the batch has been COMMITTED by a quorum (locally
// on the leader, or reported so by the leader over the forward channel), so a batch the forwarder has been told
// "accepted" for survives the death of any single node. False (not leader / no leader / queue full / not
// committed within submitCommitTimeout) means "keep the batch and retry": the caller may then send a batch that
// did commit a second time, which Go's tx dedup absorbs.
func (n *Node) Submit(batch []byte) bool {
	return n.SubmitStatus(batch) == statusAccepted
}

// SubmitStatus returns the detailed submitStatus (statusAccepted, statusTooLarge, statusNoLeader, etc.)
// for both local proposal (on leader) and forwarded proposal (on follower).
func (n *Node) SubmitStatus(batch []byte) submitStatus {
	if n.failed.Load() || n.draining.Load() {
		return statusNoLeader
	}
	if n.raft.State() == raft.Leader {
		return n.submitLocal(batch)
	}
	return n.forwardToLeader(batch)
}

// MaxBatchBytes returns the usable limit shared by admission and HTTP forwarding.
func (n *Node) MaxBatchBytes() int {
	if n == nil {
		return defaultMaxBatchBytes
	}
	return effectiveBatchLimit(n.cfg.MaxBatchBytes)
}

// submitLocal validates a batch, splits it if it exceeds max_batch_bytes and queues the pieces for proposal.
// A malformed or empty batch can never become a block and would halt every replica if proposed, so it is
// dropped here (counted), exactly like the C1 feeder.
func (n *Node) submitLocal(batch []byte) submitStatus {
	if n.failed.Load() || n.draining.Load() {
		return statusNoLeader
	}
	if n.raft.State() != raft.Leader {
		if _, id := n.raft.LeaderWithID(); id == "" {
			return statusNoLeader
		}
		return statusRedirect
	}
	pieces, err := splitBatch(batch, n.MaxBatchBytes())
	if err != nil {
		n.dropped.Add(1)
		if errors.Is(err, ErrTxTooLarge) {
			logger.Warn("❌ [RAFT] rejecting batch containing oversized transaction exceeding max_batch_bytes %d: %v", n.MaxBatchBytes(), err)
			return statusTooLarge
		}
		logger.Error("❌ [RAFT] dropping malformed batch that cannot be unmarshaled: %v", err)
		return statusAccepted
	}
	if len(pieces) > 1 {
		n.metrics.RecordSplit(len(pieces))
	}

	timer := time.NewTimer(submitCommitTimeout)
	defer timer.Stop()

	chunkSize := cap(n.proposeQ)
	if chunkSize <= 0 {
		chunkSize = 1
	}

	for i := 0; i < len(pieces); i += chunkSize {
		end := i + chunkSize
		if end > len(pieces) {
			end = len(pieces)
		}
		chunk := pieces[i:end]

		if cap(n.proposeQ)-len(n.proposeQ) < len(chunk) {
			n.metrics.RecordStatusFull()
			return statusFull
		}

		dones := make([]chan error, 0, len(chunk))
		for _, p := range chunk {
			pr := proposal{batch: p, done: make(chan error, 1), receivedAt: time.Now()}
			n.metrics.RecordEnqueue(len(n.proposeQ))
			select {
			case n.proposeQ <- pr:
				dones = append(dones, pr.done)
			default:
				n.metrics.RecordStatusFull()
				return statusFull // pieces already queued still commit; a retry only duplicates them
			}
		}

		// Reset the timer for each chunk: as long as each chunk commits within submitCommitTimeout,
		// multi-chunk batches are actively making progress and must not be aborted prematurely.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(submitCommitTimeout)

		for _, d := range dones {
			select {
			case err := <-d:
				if err != nil {
					return statusUncommitted
				}
			case <-timer.C:
				return statusUncommitted
			case <-n.stop:
				return statusNoLeader
			}
		}
	}
	return statusAccepted
}

// EncodedTxSize is what one marshalled transaction of rawLen bytes adds to a Transactions batch: field tag, length prefix
// and the body. Admission, the forwarder and the batch splitter all size against this one definition.
func EncodedTxSize(rawLen int) int { return rawLen + protoTxOverhead(rawLen) }

func protoTxOverhead(rawLen int) int {
	tagSize := 1 // repeated Transaction Transactions = 1 (tag 1, wire type 2 -> 1 byte)
	varintSize := 1
	for v := uint64(rawLen); v >= 128; v >>= 7 {
		varintSize++
	}
	return tagSize + varintSize
}

// splitBatch returns the batch as-is when it fits one chunk, else greedily regroups its txs into pieces of at most
// chunkLimit bytes. It is all-or-nothing: a tx above max returns ErrTxTooLarge and a tx that cannot be re-encoded returns
// an error, so a batch is never accepted with some of its transactions silently dropped.
func splitBatch(batch []byte, max int) ([][]byte, error) {
	txs, err := transaction.UnmarshalTransactions(batch)
	if err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}
	if len(txs) == 0 {
		return nil, ErrEmptyBatch
	}

	chunkLimit := defaultMaxBatchBytes
	if max < chunkLimit {
		chunkLimit = max
	}

	// Fast path, the common case on the leader's serial submit path: the whole batch fits one chunk, so no single tx can
	// exceed max (an encoded tx is never longer than the batch that holds it) and nothing needs sizing or re-encoding.
	if len(batch) <= chunkLimit {
		return [][]byte{batch}, nil
	}

	// Size every tx once. One above max cannot be proposed at all.
	sizes := make([]int, len(txs))
	for i, tx := range txs {
		raw, err := tx.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal transaction %d: %w", i, err)
		}
		sizes[i] = EncodedTxSize(len(raw))
		if sizes[i] > max {
			logger.Warn("❌ [RAFT] rejecting batch: transaction %d (%d bytes exceeds max_batch_bytes %d)", i, len(raw), max)
			return nil, ErrTxTooLarge
		}
	}

	var out [][]byte
	var current []types.Transaction
	currentSize := 0

	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		b, err := transaction.MarshalTransactions(current)
		if err != nil {
			return err
		}
		out = append(out, b)
		current = nil
		currentSize = 0
		return nil
	}

	for i, tx := range txs {
		if currentSize+sizes[i] > chunkLimit && len(current) > 0 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		current = append(current, tx)
		currentSize += sizes[i]
	}

	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// proposeLoop is the only caller of raft.Apply on this node.
func (n *Node) proposeLoop() {
	for {
		select {
		case <-n.stop:
			return
		case p := <-n.proposeQ:
			n.metrics.RecordQueueWait(time.Since(p.receivedAt))
			txs, err := transaction.UnmarshalTransactions(p.batch)
			if err != nil || len(txs) == 0 {
				n.dropped.Add(1)
				p.finish(nil) // nothing can ever be committed for it; do not leave the caller waiting
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
				p.finish(nil)
				continue
			}
			applyStart := time.Now()
			f := n.raft.Apply(data, 0)
			select {
			case n.inflight <- inflight{future: f, p: p, applyStart: applyStart}:
			case <-n.stop:
				return
			}
		}
	}
}

// resultLoop waits for each proposal in order and reports its outcome to the Submit call waiting for it.
func (n *Node) resultLoop() {
	for {
		select {
		case <-n.stop:
			return
		case in := <-n.inflight:
			err := in.future.Error()
			n.metrics.RecordRaftApply(time.Since(in.applyStart))
			if err != nil {
				in.p.finish(err) // leadership lost / not leader / shutting down: the caller retries
				if errors.Is(err, raft.ErrRaftShutdown) {
					return
				}
				continue
			}
			if e, ok := in.future.Response().(error); ok {
				logger.Error("❌ [RAFT] entry applied with error: %v", e)
				in.p.finish(e)
				continue
			}
			in.p.finish(nil)
		}
	}
}

// Ready reports whether this node may accept transactions: a leader is known, the replica has not failed and,
// on the leader, the propose queue has room.
func (n *Node) Ready() bool {
	if n.failed.Load() || n.draining.Load() {
		return false
	}
	if n.raft.State() == raft.Leader {
		return len(n.proposeQ) < cap(n.proposeQ)
	}
	_, id := n.raft.LeaderWithID()
	return id != ""
}

// IsLeader reports whether this replica currently leads.
func (n *Node) IsLeader() bool {
	if n.raft == nil {
		return false
	}
	return n.raft.State() == raft.Leader
}

// State returns the current Raft state string ("Leader", "Follower", "Candidate", etc.).
func (n *Node) State() string {
	if n.raft == nil {
		return "unknown"
	}
	return n.raft.State().String()
}

// LeaderID returns the ServerID of the current cluster leader.
func (n *Node) LeaderID() string {
	if n.raft == nil {
		return ""
	}
	_, id := n.raft.LeaderWithID()
	return string(id)
}

// AppliedIndex is the last Raft index applied to the FSM.
func (n *Node) AppliedIndex() uint64 { return n.raft.AppliedIndex() }

// Metrics returns a snapshot of this replica's micro-benchmarks.
func (n *Node) Metrics() MetricsSnapshot {
	if n == nil || n.metrics == nil {
		return MetricsSnapshot{}
	}
	return n.metrics.Snapshot()
}

// Dropped / Skipped / Failed expose the counters used by tests and diagnostics.
func (n *Node) Dropped() uint64    { return n.dropped.Load() }
func (n *Node) Skipped() uint64    { return n.fsm.skipped.Load() }
func (n *Node) Failed() bool       { return n.failed.Load() }
func (n *Node) Mismatches() uint64 { return n.mismatches.Load() }
func (n *Node) Attested() uint64   { return n.attested.Load() }

// logWriter routes hashicorp's logger into the project logger.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	logger.Warn("%s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// failClosedLogStore turns the first failed log write into a fatal error for this replica.
type failClosedLogStore struct {
	raft.LogStore
	metrics *Metrics
	onFatal func(error)
	once    sync.Once
}

func (f *failClosedLogStore) StoreLog(l *raft.Log) error { return f.StoreLogs([]*raft.Log{l}) }

func (f *failClosedLogStore) StoreLogs(ls []*raft.Log) error {
	t0 := time.Now()
	err := f.LogStore.StoreLogs(ls)
	if f.metrics != nil {
		f.metrics.RecordBoltStore(time.Since(t0), len(ls))
	}
	if err != nil {
		f.once.Do(func() { f.onFatal(fmt.Errorf("raft log store write failed: %w", err)) })
	}
	return err
}

func isRaftLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (n *Node) verifyForwardAuth(r *http.Request, body []byte) bool {
	if n == nil || len(n.secret) == 0 {
		return false
	}
	sender := r.Header.Get(hdrNode)
	ts, err := strconv.ParseInt(r.Header.Get(hdrTs), 10, 64)
	if err != nil || !n.knownNode(sender) {
		return false
	}
	skew := n.now().Sub(time.UnixMilli(ts))
	if skew > maxForwardSkew || skew < -maxForwardSkew {
		return false
	}
	mac, err := hex.DecodeString(r.Header.Get(hdrMac))
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(forwardMAC(n.secret, sender, ts, r.URL.Path, body))
	return hmac.Equal(mac, want)
}

type authTCPStreamLayer struct {
	ln           net.Listener
	advertise    net.Addr
	secret       []byte
	allowedHosts map[string]bool
}

func newAuthTCPStreamLayer(bindAddr string, advertise net.Addr, secret []byte, peers []config.RaftPeer) (*authTCPStreamLayer, error) {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool)
	for _, p := range peers {
		if h, _, err := net.SplitHostPort(p.Address); err == nil && h != "" {
			allowed[h] = true
		}
		if h, _, err := net.SplitHostPort(p.ForwardAddress); err == nil && h != "" {
			allowed[h] = true
		}
	}
	if advertise != nil {
		if h, _, err := net.SplitHostPort(advertise.String()); err == nil && h != "" {
			allowed[h] = true
		}
	}
	return &authTCPStreamLayer{
		ln:           ln,
		advertise:    advertise,
		secret:       secret,
		allowedHosts: allowed,
	}, nil
}

func (s *authTCPStreamLayer) isAllowedPeer(remoteAddr net.Addr) bool {
	if remoteAddr == nil {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr.String())
	if err != nil {
		host = remoteAddr.String()
	}
	if s.allowedHosts[host] {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func computeRaftStreamMAC(secret []byte, role string, nonce []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(role))
	_, _ = mac.Write(nonce)
	return mac.Sum(nil)
}

func (s *authTCPStreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	conn, err := net.DialTimeout("tcp", string(address), timeout)
	if err != nil {
		return nil, err
	}
	if len(s.secret) > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
		var nonce [32]byte
		if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
			_ = conn.Close()
			return nil, err
		}
		clientMAC := computeRaftStreamMAC(s.secret, "raft-tcp-client-v1:", nonce[:])
		var handshake [64]byte
		copy(handshake[:32], nonce[:])
		copy(handshake[32:], clientMAC)
		if _, err := conn.Write(handshake[:]); err != nil {
			_ = conn.Close()
			return nil, err
		}
		var serverMAC [32]byte
		if _, err := io.ReadFull(conn, serverMAC[:]); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("raft tcp auth response failed: %w", err)
		}
		expectedServerMAC := computeRaftStreamMAC(s.secret, "raft-tcp-server-v1:", nonce[:])
		if !hmac.Equal(serverMAC[:], expectedServerMAC) {
			_ = conn.Close()
			return nil, errors.New("raft tcp server authentication failed")
		}
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, nil
}

func (s *authTCPStreamLayer) Accept() (net.Conn, error) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return nil, err
		}
		if len(s.allowedHosts) > 0 && !s.isAllowedPeer(conn.RemoteAddr()) {
			logger.Warn("🚨 [RAFT-TCP] Rejected unallowed peer IP %v", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}
		if len(s.secret) > 0 {
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			var handshake [64]byte
			if _, err := io.ReadFull(conn, handshake[:]); err != nil {
				_ = conn.Close()
				continue
			}
			nonce := handshake[:32]
			gotMAC := handshake[32:]
			expectedClientMAC := computeRaftStreamMAC(s.secret, "raft-tcp-client-v1:", nonce)
			if !hmac.Equal(gotMAC, expectedClientMAC) {
				logger.Warn("🚨 [RAFT-TCP] Rejected unauthenticated connection from %v", conn.RemoteAddr())
				_ = conn.Close()
				continue
			}
			serverMAC := computeRaftStreamMAC(s.secret, "raft-tcp-server-v1:", nonce)
			if _, err := conn.Write(serverMAC); err != nil {
				_ = conn.Close()
				continue
			}
			_ = conn.SetDeadline(time.Time{})
		}
		return conn, nil
	}
}

func (s *authTCPStreamLayer) Close() error { return s.ln.Close() }

func (s *authTCPStreamLayer) Addr() net.Addr {
	if s.advertise != nil {
		return s.advertise
	}
	return s.ln.Addr()
}
