package processor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

type commitFatalRecorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *commitFatalRecorder) handler(err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

func (r *commitFatalRecorder) calls() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func TestCommitFatalHandlerFor_OnlyRaftModeTerminates(t *testing.T) {
	assert.Nil(t, commitFatalHandlerFor(nil), "nil config must keep the existing pipeline")
	assert.Nil(t, commitFatalHandlerFor(&config.SimpleChainConfig{}), "Rust consensus mode (empty) must keep the existing pipeline")
	assert.Nil(t, commitFatalHandlerFor(&config.SimpleChainConfig{ConsensusMode: "mysticeti"}))
	assert.Nil(t, commitFatalHandlerFor(&config.SimpleChainConfig{ConsensusMode: "RAFT"}),
		"must match raftfeed.Enabled(), which requires exactly \"raft\"")
	assert.NotNil(t, commitFatalHandlerFor(&config.SimpleChainConfig{ConsensusMode: "raft"}))
}

func TestSetLastCommitErr_FatalHandlerCalledOnceWithFirstError(t *testing.T) {
	rec := &commitFatalRecorder{}
	bp := &BlockProcessor{onCommitFatal: rec.handler}

	bp.SetLastCommitErr(nil)
	assert.Empty(t, rec.calls(), "a nil error must not trigger the handler")

	first := errors.New("first")
	bp.SetLastCommitErr(first)
	bp.SetLastCommitErr(errors.New("second"))

	calls := rec.calls()
	require.Len(t, calls, 1, "handler must run exactly once")
	assert.Same(t, first, calls[0], "handler must receive the root (first) error")
	assert.Same(t, first, bp.GetLastCommitErr(), "first error stays the recorded root error")
}

func TestSetLastCommitErr_NoHandlerKeepsLegacyBehaviour(t *testing.T) {
	bp := &BlockProcessor{}
	bp.SetLastCommitErr(errors.New("boom"))
	require.EqualError(t, bp.GetLastCommitErr(), "boom")
	bp.ClearLastCommitErr()
	assert.NoError(t, bp.GetLastCommitErr())
}

// The handler must run after the error is visible and without holding lastCommitErrMu, so a handler that
// reads the error (as a real shutdown path does) cannot deadlock.
func TestSetLastCommitErr_HandlerRunsWithoutHoldingLock(t *testing.T) {
	bp := &BlockProcessor{}
	seen := make(chan error, 1)
	bp.onCommitFatal = func(error) { seen <- bp.GetLastCommitErr() }

	done := make(chan struct{})
	go func() {
		bp.SetLastCommitErr(errors.New("disk full"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetLastCommitErr deadlocked: handler was called while holding lastCommitErrMu")
	}
	select {
	case got := <-seen:
		assert.EqualError(t, got, "disk full")
	default:
		t.Fatal("handler was not called")
	}
}

func TestSetLastCommitErr_ConcurrentErrorsCallHandlerOnce(t *testing.T) {
	var calls atomic.Int32
	bp := &BlockProcessor{onCommitFatal: func(error) { calls.Add(1) }}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bp.SetLastCommitErr(errors.New("err"))
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), calls.Load())
}

// End-to-end through commitWorker: an undo durability failure (disk full) on a raft replica must reach the
// fatal handler instead of leaving the worker silently skipping every later block.
func TestCommitWorker_RaftUndoFailureTriggersFatal(t *testing.T) {
	backupStore := newFailingPrefixBackupStorage(errors.New("no space left on device"), []byte(storage.NomtUndoKindAccount))
	sm := storage.NewStorageManager()
	require.NoError(t, sm.AddStorageBackupDb(backupStore))

	rec := &commitFatalRecorder{}
	bp := &BlockProcessor{
		storageManager: sm,
		commitChannel:  make(chan CommitJob, 4),
		onCommitFatal:  rec.handler,
	}
	go bp.commitWorker()
	defer close(bp.commitChannel)

	var accDisc, stakeDisc, scDisc atomic.Bool
	errCh := make(chan error, 1)
	bp.commitChannel <- CommitJob{
		Block:                    newFailClosedTestBlock(),
		AccountNomtPayload:       preImageDiscardPayload{preImages: [][2][]byte{{[]byte("a"), []byte("v")}}, discarded: &accDisc},
		StakeNomtPayload:         preImageDiscardPayload{discarded: &stakeDisc},
		SmartContractNomtPayload: trackingDiscardPayload{discarded: &scDisc},
		ErrChan:                  errCh,
	}
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "no space left on device")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for commit failure")
	}

	calls := rec.calls()
	require.Len(t, calls, 1, "raft replica must hit the fatal handler on its first commit failure")
	assert.ErrorContains(t, calls[0], "account NOMT undo durability failed")
	assert.True(t, accDisc.Load() && stakeDisc.Load() && scDisc.Load(), "payloads are still discarded before exit")

	// A later block (the legacy "skip" path) must not call the handler a second time.
	errCh2 := make(chan error, 1)
	var d2 atomic.Bool
	bp.commitChannel <- CommitJob{Block: newFailClosedTestBlock(), AccountNomtPayload: preImageDiscardPayload{discarded: &d2}, ErrChan: errCh2}
	select {
	case err := <-errCh2:
		require.ErrorContains(t, err, "prior commit error")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for skipped block")
	}
	assert.Len(t, rec.calls(), 1)
}

const raftFatalChildEnv = "METANODE_TEST_RAFT_COMMIT_FATAL_CHILD"

// The real raft handler must terminate the process with exit code 1 and write crash.log: verified in a child
// process, since fatal.Exit calls os.Exit.
func TestRaftCommitFatal_ExitsProcessWithCrashDump(t *testing.T) {
	if os.Getenv(raftFatalChildEnv) == "1" {
		bp := &BlockProcessor{onCommitFatal: commitFatalHandlerFor(&config.SimpleChainConfig{ConsensusMode: "raft"})}
		bp.SetLastCommitErr(errors.New("no space left on device"))
		// Reaching this line means the replica kept running: report it with a distinct code.
		os.Exit(42)
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRaftCommitFatal_ExitsProcessWithCrashDump$")
	cmd.Env = append(os.Environ(), raftFatalChildEnv+"=1")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "child must exit non-zero, output:\n%s", out)
	require.Equal(t, 1, exitErr.ExitCode(), "raft replica must exit with code 1 (42 = kept running), output:\n%s", out)

	dump, readErr := os.ReadFile(filepath.Join(dir, "crash.log"))
	require.NoError(t, readErr, "fatal.Exit must write crash.log")
	assert.True(t, strings.Contains(string(dump), "no space left on device"), "crash.log must carry the root cause:\n%s", dump)
	assert.Contains(t, string(dump), "raft replica cannot persist a committed block")
}
