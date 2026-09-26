package processor

import (
	"errors"
	"sync"
	"testing"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBlockProcessor_SetAndClearLastCommitErr verifies that the first commit error is recorded
// and retained (fail-closed) and that ClearLastCommitErr resets it safely.
func TestBlockProcessor_SetAndClearLastCommitErr(t *testing.T) {
	bp := &BlockProcessor{}

	assert.NoError(t, bp.GetLastCommitErr())

	err1 := errors.New("first commit error: I/O timeout")
	err2 := errors.New("second commit error: out of space")

	bp.SetLastCommitErr(err1)
	assert.Equal(t, err1, bp.GetLastCommitErr(), "first error must be recorded")

	// Subsequent error must not overwrite the root error
	bp.SetLastCommitErr(err2)
	assert.Equal(t, err1, bp.GetLastCommitErr(), "root error must not be overwritten")

	bp.ClearLastCommitErr()
	assert.NoError(t, bp.GetLastCommitErr(), "error should be cleared")
}

// TestCommitWorker_FencePropagatesPriorError verifies that fence jobs propagate prior commit errors
// via ErrChan and DO NOT close DoneChan (preventing false success).
func TestCommitWorker_FencePropagatesPriorError(t *testing.T) {
	bp := &BlockProcessor{
		commitChannel: make(chan CommitJob, 4),
	}

	rootErr := errors.New("underlying PebbleDB corrupted")
	bp.SetLastCommitErr(rootErr)

	go bp.commitWorker()

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)

	// Send fence job (Block: nil)
	bp.commitChannel <- CommitJob{
		Block:    nil,
		DoneChan: doneCh,
		ErrChan:  errCh,
	}

	// 1. ErrChan must receive the prior root error
	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Equal(t, rootErr, err)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ErrChan")
	}

	// 2. DoneChan must NOT be closed (no false success)
	select {
	case <-doneCh:
		t.Fatal("DoneChan was signaled/closed despite commit error! (false-success invariant violated)")
	case <-time.After(100 * time.Millisecond):
		// Expected: DoneChan remains open and unsignaled
	}
}

// TestCommitWorker_SubsequentBlocksSkippedAfterError verifies that if a prior block failed to commit,
// subsequent block commits are skipped to avoid state drift/corruption, and return the error to caller.
func TestCommitWorker_SubsequentBlocksSkippedAfterError(t *testing.T) {
	bp := &BlockProcessor{
		commitChannel: make(chan CommitJob, 4),
	}

	rootErr := errors.New("fatal disk write failure")
	bp.SetLastCommitErr(rootErr)

	go bp.commitWorker()

	header := block.NewBlockHeader(
		e_common.HexToHash("0x1"), 10,
		e_common.HexToHash("0x2"), e_common.HexToHash("0x3"),
		e_common.HexToHash("0x4"), e_common.HexToAddress("0x5"),
		1000, e_common.HexToHash("0x6"), 1,
	)
	bl := block.NewBlock(header, nil, nil)

	errCh := make(chan error, 1)
	doneCh := make(chan struct{})

	bp.commitChannel <- CommitJob{
		Block:    bl,
		DoneChan: doneCh,
		ErrChan:  errCh,
	}

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prior commit error")
		assert.Contains(t, err.Error(), "fatal disk write failure")
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ErrChan on skipped block")
	}

	// DoneChan must NOT be signaled
	select {
	case <-doneCh:
		t.Fatal("DoneChan signaled for skipped block!")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

// TestWaitForPersistence_ReturnsPriorCommitError verifies that WaitForPersistence
// returns the prior commit error rather than hanging or silently succeeding.
func TestWaitForPersistence_ReturnsPriorCommitError(t *testing.T) {
	bp := &BlockProcessor{
		commitChannel: make(chan CommitJob, 4),
	}

	rootErr := errors.New("NOMT flush error")
	bp.SetLastCommitErr(rootErr)

	go bp.commitWorker()

	err := bp.WaitForPersistence()
	require.Error(t, err)
	assert.Equal(t, rootErr, err)
}

// TestCommitWorker_SuccessSignaling verifies that on success:
// - ErrChan receives nil
// - DoneChan receives struct{}{}
func TestCommitWorker_FenceSuccessSignaling(t *testing.T) {
	bp := &BlockProcessor{
		commitChannel: make(chan CommitJob, 4),
	}

	go bp.commitWorker()

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)

	// Clean state fence
	bp.commitChannel <- CommitJob{
		Block:    nil,
		DoneChan: doneCh,
		ErrChan:  errCh,
	}

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ErrChan")
	}

	select {
	case <-doneCh:
		// Succeeded as expected
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for DoneChan on clean fence")
	}
}

// TestCommitWorker_ConcurrentFenceDrain verifies concurrent drain calls do not race.
func TestCommitWorker_ConcurrentFenceDrain(t *testing.T) {
	bp := &BlockProcessor{
		commitChannel: make(chan CommitJob, 16),
	}

	go bp.commitWorker()

	const concurrent = 8
	var wg sync.WaitGroup
	wg.Add(concurrent)

	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			err := bp.WaitForPersistence()
			assert.NoError(t, err)
		}()
	}

	wg.Wait()
}

// failingChangelogPayload stands in for a NOMT payload whose changelog cannot be made durable.
type failingChangelogPayload struct{ err error }

func (p failingChangelogPayload) WriteChangelog() error { return p.err }

func newFailClosedTestBlock() *block.Block {
	header := block.NewBlockHeader(
		e_common.HexToHash("0x1"), 20,
		e_common.HexToHash("0x2"), e_common.HexToHash("0x3"),
		e_common.HexToHash("0x4"), e_common.HexToAddress("0x5"),
		1000, e_common.HexToHash("0x6"), 1,
	)
	return block.NewBlock(header, nil, nil)
}

// A changelog that cannot be made durable must stop the commit: the error is reported, the block is not
// acknowledged (DoneChan stays open) and the root error is recorded so later blocks and fences are refused.
// Without this, a block could be published with no NOMT recovery data (a fork after a crash).
func TestCommitWorker_ChangelogFailureIsFatal(t *testing.T) {
	cases := []struct {
		name    string
		account interface{}
		stake   interface{}
		want    string
	}{
		{"account", failingChangelogPayload{errors.New("disk full")}, nil, "account changelog durability failed"},
		{"stake", nil, failingChangelogPayload{errors.New("disk full")}, "stake changelog durability failed"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			bp := &BlockProcessor{commitChannel: make(chan CommitJob, 4)}
			go bp.commitWorker()
			defer close(bp.commitChannel)

			errCh := make(chan error, 1)
			doneCh := make(chan struct{})
			bp.commitChannel <- CommitJob{
				Block:              newFailClosedTestBlock(),
				AccountNomtPayload: tc.account,
				StakeNomtPayload:   tc.stake,
				DoneChan:           doneCh,
				ErrChan:            errCh,
			}

			select {
			case err := <-errCh:
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
				assert.Contains(t, err.Error(), "disk full")
			case <-time.After(2 * time.Second):
				t.Fatal("timed out: a failed changelog write was not reported")
			}
			select {
			case <-doneCh:
				t.Fatal("DoneChan signaled although the changelog was not durable")
			case <-time.After(100 * time.Millisecond):
			}
			require.Error(t, bp.GetLastCommitErr(), "root error must be recorded so later work is refused")
		})
	}
}
