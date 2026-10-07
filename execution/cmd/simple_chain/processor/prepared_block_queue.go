package processor

import (
	"context"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// PreparedBlock represents an ExecutableBlock whose transactions have been
// unmarshaled, envelope-verified, deduplicated, sorted, and pre-indexed for grouping.
//
// ZERO-FORK INVARIANT:
// A PreparedBlock contains 100% PURE, in-memory computational state derived solely
// from the block's own protobuf bytes and transaction signatures.
// It does NOT touch ChainState, NOMT, or the database.
//
// This enables CPU-intensive transaction unmarshaling, EIP-1559/Secp256k1 signature
// verification (utilizing the boundSigKey cache), deduplication, and sorting to run
// concurrently across in-flight blocks BEFORE predecessor commits complete, without
// any risk of blockchain state fork.
type PreparedBlock struct {
	EpochData    *pb.ExecutableBlock
	GEI          uint64
	BlockNum     uint64
	CommitIndex  uint32
	Epoch        uint64
	Txs          []types.Transaction
	Items        []grouptxns.Item
	PrepDuration time.Duration
}

// PreparedBlockQueue coordinates bounded parallel transaction preparation for in-flight blocks.
type PreparedBlockQueue struct {
	prepSem  chan struct{} // Bounded concurrency for parallel preparation workers (default: 4)
	prepared sync.Map      // GEI (uint64) -> *PreparedBlock
}

// NewPreparedBlockQueue creates a new PreparedBlockQueue with bounded concurrency.
func NewPreparedBlockQueue(maxConcurrentPrep int) *PreparedBlockQueue {
	if maxConcurrentPrep <= 0 {
		maxConcurrentPrep = 4
	}
	return &PreparedBlockQueue{
		prepSem: make(chan struct{}, maxConcurrentPrep),
	}
}

// PrepareBlock executes parallel unmarshaling, signature & envelope binding verification,
// deduplication, sorting, and Union-Find grouping address extraction for an ExecutableBlock.
func (q *PreparedBlockQueue) PrepareBlock(
	ctx context.Context,
	epochData *pb.ExecutableBlock,
	blockNum uint64,
) (*PreparedBlock, error) {
	if epochData == nil {
		return nil, nil
	}

	gei := epochData.GetGlobalExecIndex()

	// 1. Fast-path: Deduplicate if already prepared
	if val, ok := q.prepared.Load(gei); ok {
		if pb, ok := val.(*PreparedBlock); ok && pb != nil {
			return pb, nil
		}
	}

	// 2. Bounded concurrency slot acquisition with cancellation awareness
	select {
	case q.prepSem <- struct{}{}:
		defer func() { <-q.prepSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Double-check after acquiring semaphore slot
	if val, ok := q.prepared.Load(gei); ok {
		if pb, ok := val.(*PreparedBlock); ok && pb != nil {
			return pb, nil
		}
	}

	start := time.Now()

	// 3. Parallel unmarshal, envelope binding check (zero-alloc), dedup, and sort
	allTxs := PrepareTransactions(epochData)

	// 4. Pre-extract group addresses for deterministic execution grouping
	items := make([]grouptxns.Item, 0, len(allTxs))
	for i, tx := range allTxs {
		items = append(items, grouptxns.Item{
			ID:    i,
			Array: grouptxns.BuildDeterministicGroupAddrs(tx),
			Tx:    tx,
		})
	}

	prepDuration := time.Since(start)

	prepBlock := &PreparedBlock{
		EpochData:    epochData,
		GEI:          gei,
		BlockNum:     blockNum,
		CommitIndex:  epochData.GetCommitIndex(),
		Epoch:        epochData.GetEpoch(),
		Txs:          allTxs,
		Items:        items,
		PrepDuration: prepDuration,
	}

	q.prepared.Store(gei, prepBlock)

	logger.Debug("⚡ [PREPARED-QUEUE] Pre-verified block GEI=%d (block #%d) with %d txs in %v",
		gei, blockNum, len(allTxs), prepDuration)

	return prepBlock, nil
}

// GetPreparedBlock retrieves an already-prepared block by GEI if available.
func (q *PreparedBlockQueue) GetPreparedBlock(gei uint64) (*PreparedBlock, bool) {
	val, ok := q.prepared.Load(gei)
	if !ok {
		return nil, false
	}
	pb, ok := val.(*PreparedBlock)
	return pb, ok
}

// DeletePreparedBlock removes a single prepared block by GEI.
func (q *PreparedBlockQueue) DeletePreparedBlock(gei uint64) {
	q.prepared.Delete(gei)
}

// CleanThrough removes all prepared blocks with GEI <= throughGEI.
func (q *PreparedBlockQueue) CleanThrough(throughGEI uint64) {
	q.prepared.Range(func(key, value interface{}) bool {
		gei, ok := key.(uint64)
		if ok && gei <= throughGEI {
			q.prepared.Delete(gei)
		}
		return true
	})
}

// Clear removes all prepared blocks from memory.
func (q *PreparedBlockQueue) Clear() {
	q.prepared.Range(func(key, value interface{}) bool {
		q.prepared.Delete(key)
		return true
	})
}
