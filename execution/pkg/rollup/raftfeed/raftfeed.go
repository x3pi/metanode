// Package raftfeed is the switch and the block/batch source for consensus_mode = "raft".
//
// C1 (single node, no replication yet): the tx forwarder hands each batch to Submit; a single feeder
// goroutine stamps it (timestamp, next GlobalExecIndex, next block number, hash chained to the previous
// block) and pushes the resulting ExecutableBlock into the BlockProcessor's bounded ingestion queue.
// Everything that would later be replicated by Raft (C2) already goes through this one path, so C2 only has
// to move the stamping into the leader and let every replica build blocks from the committed batch.
//
// Zero-fork rules kept here: nothing is decided from wall-clock time except the block timestamp, and that is
// stamped once, before the block exists, never recomputed; every queue is bounded; when the feed cannot
// accept or deliver, Submit returns false (the forwarder keeps the batch and retries) instead of dropping.
package raftfeed

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// SubmitQueueCap bounds the batches accepted by Submit but not yet turned into blocks. A full queue makes
// Submit return false (backpressure to the forwarder), it never grows.
const SubmitQueueCap = 256

// Enabled reports whether consensus_mode is exactly "raft". With any other value (including empty)
// the node behaves as before: Rust consensus, no raft code path is taken.
func Enabled() bool {
	return config.ConfigApp != nil && config.ConfigApp.ConsensusMode == "raft"
}

// ValidateConfig rejects raft-mode configurations the feed cannot honour yet. It is a no-op for every other
// consensus_mode, so the default (Rust consensus) path is unchanged.
func ValidateConfig(cfg *config.SimpleChainConfig) error {
	if cfg == nil || cfg.ConsensusMode != "raft" {
		return nil
	}
	if cfg.SnapshotEnabled {
		// Snapshots must carry the Raft CommitIndex and a way to rebuild a replica (C4); until that is defined a
		// snapshot taken here would restore to a state the feed cannot continue from.
		return errors.New("consensus_mode=raft does not support snapshot_enabled yet (needs the Raft CommitIndex, plan C4): disable snapshots")
	}
	return nil
}

// StartConfig describes where the feeder delivers blocks and where its numbering starts.
type StartConfig struct {
	// Sink is the BlockProcessor's bounded ingestion queue. The feeder blocks (bounded backpressure) when it
	// is full and never drops a block it has already numbered.
	Sink chan<- *pb.ExecutableBlock
	// NextIndex is the first GlobalExecIndex to hand out (last executed GEI + 1); it must be >= 1.
	NextIndex uint64
	// NextBlock is the first block number to hand out (last committed block + 1); it must be >= 1.
	NextBlock uint64
	// PrevCommitHash seeds the commit-hash chain (nil for a fresh chain).
	PrevCommitHash []byte
	// Epoch is stamped on every block.
	Epoch uint64
	// LeaderAddress is the address stamped as the block's leader. Go must never derive a leader itself; in C1
	// it is this node's own validator address, in C2 it comes from the replicated batch record.
	LeaderAddress common.Address
	// Now supplies the timestamp source (defaults to time.Now); injectable so tests can prove determinism.
	Now func() time.Time
}

// Feeder turns submitted batches into ExecutableBlocks.
type Feeder struct {
	cfg   StartConfig
	queue chan []byte

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}

	running atomic.Bool
	failed  atomic.Bool
	dropped atomic.Uint64

	// Owned by the run goroutine only.
	nextIndex uint64
	nextBlock uint64
	lastTs    uint64
	prevHash  []byte
}

var current atomic.Pointer[Feeder]

// Start launches the feeder. It fails if one is already running or the configuration is unusable.
func Start(cfg StartConfig) (*Feeder, error) {
	if cfg.Sink == nil {
		return nil, errors.New("raftfeed: nil sink")
	}
	if cfg.NextIndex < 1 || cfg.NextBlock < 1 {
		return nil, fmt.Errorf("raftfeed: NextIndex (%d) and NextBlock (%d) must be >= 1", cfg.NextIndex, cfg.NextBlock)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	f := &Feeder{
		cfg:       cfg,
		queue:     make(chan []byte, SubmitQueueCap),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		nextIndex: cfg.NextIndex,
		nextBlock: cfg.NextBlock,
		prevHash:  append([]byte(nil), cfg.PrevCommitHash...),
	}
	if !current.CompareAndSwap(nil, f) {
		return nil, errors.New("raftfeed: already started")
	}
	f.running.Store(true)
	go f.run()
	return f, nil
}

// Stop halts the current feeder and waits for it to exit. Batches still queued are NOT executed: their
// senders were told "accepted", which is why C1 is not HA (C2 makes acceptance mean "replicated").
func Stop() {
	f := current.Load()
	if f == nil {
		return
	}
	f.Stop()
}

// Stop halts this feeder and waits for it to exit.
func (f *Feeder) Stop() {
	f.stopOnce.Do(func() {
		f.running.Store(false)
		close(f.stop)
	})
	<-f.done
	current.CompareAndSwap(f, nil)
}

// Dropped returns how many submitted batches were discarded because they could not become a block
// (malformed or empty). Such a batch can never be executed, so retrying it would block the pipeline forever.
func (f *Feeder) Dropped() uint64 { return f.dropped.Load() }

// Failed reports whether the feeder stopped itself because it could not keep numbering safely.
func (f *Feeder) Failed() bool { return f.failed.Load() }

// Submit hands a marshalled transaction batch (transaction.MarshalTransactions) to the feeder.
//
// It returns true only once the batch is accepted into the bounded queue. tx_batch_forwarder retries a false
// result forever, so false must mean "not accepted, keep the batch": no feeder running, or the queue is full.
func Submit(batch []byte) bool {
	f := current.Load()
	if f == nil || !f.running.Load() {
		return false
	}
	select {
	case f.queue <- append([]byte(nil), batch...):
		return true
	default:
		return false
	}
}

// Ready reports whether the node may accept transactions: a feeder is running, has not failed, and its
// submit queue is not saturated.
func Ready() bool {
	f := current.Load()
	return f != nil && f.running.Load() && !f.failed.Load() && len(f.queue) < cap(f.queue)
}

func (f *Feeder) run() {
	defer close(f.done)
	defer f.running.Store(false)
	for {
		select {
		case <-f.stop:
			return
		case batch := <-f.queue:
			blk, err := f.build(batch)
			if err != nil {
				if errors.Is(err, errNumberingExhausted) {
					logger.Error("🚨 [RAFT-FEED] %v — stopping the feed (fail closed)", err)
					f.failed.Store(true)
					return
				}
				f.dropped.Add(1)
				logger.Error("❌ [RAFT-FEED] dropping a batch that cannot become a block: %v", err)
				continue
			}
			select {
			case f.cfg.Sink <- blk:
				f.advance(blk)
			case <-f.stop:
				return
			}
		}
	}
}

var errNumberingExhausted = errors.New("raftfeed: commit index no longer fits in uint32")

// build decodes a batch and stamps a block WITHOUT consuming any numbers: they are advanced only after the
// block was delivered, so a shutdown between build and delivery never leaves a hole.
func (f *Feeder) build(batch []byte) (*pb.ExecutableBlock, error) {
	txs, err := transaction.UnmarshalTransactions(batch)
	if err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}
	if len(txs) == 0 {
		return nil, errors.New("empty batch")
	}
	exes := make([]*pb.TransactionExe, len(txs))
	for i, tx := range txs {
		raw, err := tx.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal tx %d: %w", i, err)
		}
		exes[i] = &pb.TransactionExe{Digest: raw}
	}
	// CommitIndex is uint32 in ExecutableBlock while indexes are uint64: refuse to wrap around silently.
	if f.nextIndex > math.MaxUint32 {
		return nil, errNumberingExhausted
	}

	ts := uint64(f.cfg.Now().UnixMilli())
	if ts <= f.lastTs { // block timestamps never go backwards or repeat
		ts = f.lastTs + 1
	}
	return &pb.ExecutableBlock{
		Transactions:      exes,
		GlobalExecIndex:   f.nextIndex,
		CommitIndex:       uint32(f.nextIndex),
		Epoch:             f.cfg.Epoch,
		CommitTimestampMs: ts,
		LeaderAddress:     f.cfg.LeaderAddress.Bytes(),
		BlockNumber:       f.nextBlock,
		CommitHash:        f.chainHash(f.nextIndex, ts, batch),
	}, nil
}

func (f *Feeder) advance(blk *pb.ExecutableBlock) {
	f.nextIndex = blk.GlobalExecIndex + 1
	f.nextBlock = blk.BlockNumber + 1
	f.lastTs = blk.CommitTimestampMs
	f.prevHash = blk.CommitHash
}

// chainHash = keccak256(prev || index || timestamp || batch): deterministic for a given batch stream.
func (f *Feeder) chainHash(index, ts uint64, batch []byte) []byte {
	var num [16]byte
	binary.BigEndian.PutUint64(num[:8], index)
	binary.BigEndian.PutUint64(num[8:], ts)
	return crypto.Keccak256(f.prevHash, num[:], batch)
}
