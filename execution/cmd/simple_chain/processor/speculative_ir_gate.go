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

// executionLock is the RWMutex the speculative worker holds (as a reader) while it runs. The gate must not wait
// for the committer while holding it: a writer that is pending (PauseExecution for a snapshot, P2P sync) blocks
// new readers, the committer needs the read lock to commit the predecessor, and the writer waits for us — a cycle.
type executionLock struct{ release, reacquire func() }

func (se *SpeculativeExecutor) isCommitted(need uint64) bool {
	done, _ := se.committedAtLeast(need)
	return done || storage.GetLastGlobalExecIndex() >= need
}

// newIRGate builds the tx_processor.IRGate of the speculative execution of block gei that was cloned from a tip
// whose header hash is specParent: it waits until gei-1 is committed (giving up the execution read lock while it
// waits), then refuses to continue if the tip moved. Result: speculative executions reach the NOMT handle strictly
// in GEI order, and only when they are still valid.
func (se *SpeculativeExecutor) newIRGate(gei uint64, specParent common.Hash, tipHash func() (common.Hash, bool), lock *executionLock) func(context.Context) error {
	return func(ctx context.Context) error {
		if gei > 1 && !se.isCommitted(gei-1) {
			if lock != nil {
				lock.release()
			}
			err := se.waitCommitted(ctx, gei-1)
			if lock != nil {
				lock.reacquire() // always: the worker's deferred RUnlock must stay balanced
			}
			if err != nil {
				return err
			}
		}
		if tip, ok := tipHash(); ok && tip != specParent {
			return errSpeculativeParentStale
		}
		return nil
	}
}
