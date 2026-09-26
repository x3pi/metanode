package raftfeed

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

var errNumberingExhausted = errors.New("raftfeed: commit index no longer fits in uint32")

// stamper turns a batch into the next ExecutableBlock. It is the ONE place that decides numbering, timestamp
// monotonicity and the commit-hash chain, shared by the C1 feeder and the C2 Raft FSM, so both produce the same
// block for the same (batch, timestamp) stream. It reads no clock and no randomness: the caller supplies the
// timestamp. Not safe for concurrent use (each owner drives it from one goroutine).
type stamper struct {
	epoch  uint64
	leader common.Address

	nextIndex uint64
	nextBlock uint64
	lastTs    uint64
	prevHash  []byte
}

// build decodes a batch and stamps a block WITHOUT consuming any numbers: the caller calls advance only after
// the block was delivered (or deliberately skipped), so a shutdown between build and delivery never leaves a hole.
func (s *stamper) build(batch []byte, tsMs uint64) (*pb.ExecutableBlock, error) {
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
	if s.nextIndex > math.MaxUint32 {
		return nil, errNumberingExhausted
	}

	ts := tsMs
	if ts <= s.lastTs { // block timestamps never go backwards or repeat
		ts = s.lastTs + 1
	}
	return &pb.ExecutableBlock{
		Transactions:      exes,
		GlobalExecIndex:   s.nextIndex,
		CommitIndex:       uint32(s.nextIndex),
		Epoch:             s.epoch,
		CommitTimestampMs: ts,
		LeaderAddress:     s.leader.Bytes(),
		BlockNumber:       s.nextBlock,
		CommitHash:        s.chainHash(s.nextIndex, ts, batch),
	}, nil
}

func (s *stamper) advance(blk *pb.ExecutableBlock) {
	s.nextIndex = blk.GlobalExecIndex + 1
	s.nextBlock = blk.BlockNumber + 1
	s.lastTs = blk.CommitTimestampMs
	s.prevHash = blk.CommitHash
}

// chainHash = keccak256(prev || index || timestamp || batch): deterministic for a given batch stream.
func (s *stamper) chainHash(index, ts uint64, batch []byte) []byte {
	var num [16]byte
	binary.BigEndian.PutUint64(num[:8], index)
	binary.BigEndian.PutUint64(num[8:], ts)
	return crypto.Keccak256(s.prevHash, num[:], batch)
}
