// Package syncorder provides stateless helper functions for ordering
// incoming blocks by GlobalExecIndex (GEI). This logic was extracted
// from the monolithic block_processor_sync.go to improve testability
// and decrease cognitive complexity.
//
// All functions in this package are PURE — they take explicit inputs
// and return explicit outputs with no side effects. This makes them
// trivially unit-testable without needing a running BlockProcessor.
package syncorder

import (
	"sort"
)

// Decision describes the action the caller should take for an incoming block.
type Decision int

const (
	// Process means the block is sequential and should be executed immediately.
	Process Decision = iota
	// Skip means the block is a duplicate or stale; discard silently.
	Skip
	// Buffer means the block arrived out-of-order; store in pendingBlocks for later.
	Buffer
	// GapSkipFreshStart means a small gap on an empty DB — safe to jump ahead.
	GapSkipFreshStart
	// GapSkipRestore means a large gap with many buffered blocks (post-restore transition).
	GapSkipRestore
	// SyncFromDB means DB is ahead of in-memory tracker, caller should re-sync.
	SyncFromDB
)

// String returns a human-readable label for the Decision.
func (d Decision) String() string {
	switch d {
	case Process:
		return "Process"
	case Skip:
		return "Skip"
	case Buffer:
		return "Buffer"
	case GapSkipFreshStart:
		return "GapSkipFreshStart"
	case GapSkipRestore:
		return "GapSkipRestore"
	case SyncFromDB:
		return "SyncFromDB"
	default:
		return "Unknown"
	}
}

// OrderingResult holds the outcome of EvaluateBlockOrder.
type OrderingResult struct {
	// Decision is the action the caller should take.
	Decision Decision
	// NewNextExpected is the updated value for nextExpectedGlobalExecIndex.
	// The caller should set *nextExpected = NewNextExpected when Decision != Buffer.
	NewNextExpected uint64
	// Reason provides a human-readable explanation for logging.
	Reason string
}

// EvaluateBlockOrder determines what to do with a block at the given
// globalExecIndex, relative to the caller's current tracking state.
//
// Parameters:
//   - gei:             the incoming block's GlobalExecIndex
//   - nextExpected:    the caller's current nextExpectedGlobalExecIndex
//   - dbLastBlock:     the highest block number currently in the database
//   - pendingCount:    number of blocks currently buffered in pendingBlocks
//   - hasTxs:          whether the incoming block carries transactions
//
// This function is PURE and has no side effects.
func EvaluateBlockOrder(
	gei uint64,
	nextExpected uint64,
	dbLastBlock uint64,
	pendingCount int,
	hasTxs bool,
) OrderingResult {
	// ─── Case 1: Duplicate / old block ───────────────────────────────────
	if gei < nextExpected {
		return OrderingResult{
			Decision:        Skip,
			NewNextExpected: nextExpected,
			Reason:          "block GEI is behind nextExpected (duplicate/old)",
		}
	}

	// ─── Case 2: Sequential (happy path) ────────────────────────────────
	if gei == nextExpected {
		return OrderingResult{
			Decision:        Process,
			NewNextExpected: gei + 1,
			Reason:          "sequential block",
		}
	}

	// ─── Case 3: Future block (gei > nextExpected) ───────────────────────
	gap := gei - nextExpected

	// Sub-case 3a: DB is ahead — caller should sync from DB first.
	if dbLastBlock > 0 && dbLastBlock >= nextExpected {
		newNext := dbLastBlock + 1
		if gei < newNext {
			return OrderingResult{
				Decision:        Skip,
				NewNextExpected: newNext,
				Reason:          "block is old after DB sync",
			}
		}
		if gei == newNext {
			return OrderingResult{
				Decision:        SyncFromDB,
				NewNextExpected: newNext,
				Reason:          "block is sequential after DB sync",
			}
		}
		// Still a gap even after DB sync — buffer.
		return OrderingResult{
			Decision:        Buffer,
			NewNextExpected: newNext,
			Reason:          "still a gap after DB sync",
		}
	}

	// Sub-case 3b: Fresh-start gap skip (DB empty, small gap ≤ 16).
	if gap <= 16 && dbLastBlock == 0 {
		return OrderingResult{
			Decision:        GapSkipFreshStart,
			NewNextExpected: gei,
			Reason:          "fresh start gap skip (DB=0, gap≤16)",
		}
	}

	// Sub-case 3c: Restore transition gap skip (large gap + many pending blocks).
	if gap > 200 && pendingCount > 50 {
		return OrderingResult{
			Decision:        GapSkipRestore,
			NewNextExpected: nextExpected, // caller will compute from pending map
			Reason:          "restore transition gap skip (gap>200, pending>50)",
		}
	}

	// Sub-case 3d: Normal out-of-order — buffer.
	return OrderingResult{
		Decision:        Buffer,
		NewNextExpected: nextExpected,
		Reason:          "out-of-order block, buffering",
	}
}

// SortedPendingGEIs returns the GEIs from the pending map sorted in ascending
// order. This ensures deterministic processing of buffered blocks (critical for
// fork safety — random map iteration would cause different nodes to process
// blocks in different orders).
// Uses Go generics to accept any map value type.
func SortedPendingGEIs[V any](pending map[uint64]V) []uint64 {
	geis := make([]uint64, 0, len(pending))
	for gei := range pending {
		geis = append(geis, gei)
	}
	sort.Slice(geis, func(i, j int) bool { return geis[i] < geis[j] })
	return geis
}

// FindMinBufferedGEI returns the smallest GEI in the pending map.
// Returns 0 if the map is empty.
// Uses Go generics to accept any map value type.
func FindMinBufferedGEI[V any](pending map[uint64]V) uint64 {
	if len(pending) == 0 {
		return 0
	}
	min := ^uint64(0) // max uint64
	for gei := range pending {
		if gei < min {
			min = gei
		}
	}
	return min
}
