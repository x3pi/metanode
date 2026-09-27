package processor

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// errSpeculativeParentStale is returned by the IR gate when the block this speculative execution was built on is
// no longer the chain tip. The execution is thrown away BEFORE any NOMT session is opened; the committer then
// re-executes the block sequentially (ExecuteErr path).
var errSpeculativeParentStale = errors.New("speculative execution built on a stale parent")

// gateRecheckInterval re-reads the DB's committed GEI while waiting: a P2P sync can commit blocks without the
// committer noticing until its next result. It only paces a re-check; it never decides anything by time.
const gateRecheckInterval = 5 * time.Millisecond

// MarkCommitted records that the committer has finished every GEI up to and including gei.
func (se *SpeculativeExecutor) MarkCommitted(gei uint64) {
	se.commitMu.Lock()
	defer se.commitMu.Unlock()
	if gei <= se.committedThrough {
		return
	}
	se.committedThrough = gei
	close(se.commitWake)
	se.commitWake = make(chan struct{})
}

func (se *SpeculativeExecutor) committedAtLeast(need uint64) (bool, chan struct{}) {
	se.commitMu.Lock()
	defer se.commitMu.Unlock()
	return se.committedThrough >= need, se.commitWake
}

// waitCommitted blocks until GEI need is committed (by the committer, or already in the DB) or ctx ends.
func (se *SpeculativeExecutor) waitCommitted(ctx context.Context, need uint64) error {
	for {
		done, wake := se.committedAtLeast(need)
		if done || storage.GetLastGlobalExecIndex() >= need {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-time.After(gateRecheckInterval):
		}
	}
}

// newIRGate builds the tx_processor.IRGate of the speculative execution of block gei that was cloned from a tip
// whose header hash is specParent: it waits until gei-1 is committed, then refuses to continue if the tip moved.
// Result: speculative executions reach the NOMT handle strictly in GEI order, and only when they are still valid.
func (se *SpeculativeExecutor) newIRGate(gei uint64, specParent common.Hash, tipHash func() (common.Hash, bool)) func(context.Context) error {
	return func(ctx context.Context) error {
		if gei > 1 {
			if err := se.waitCommitted(ctx, gei-1); err != nil {
				return err
			}
		}
		if tip, ok := tipHash(); ok && tip != specParent {
			return errSpeculativeParentStale
		}
		return nil
	}
}
