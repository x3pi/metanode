package processor

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingPrefixBackupStorage struct {
	*storage.MemoryDB
	mu           sync.Mutex
	failPrefixes [][]byte
	failErr      error
}

func newFailingPrefixBackupStorage(failErr error, failPrefixes ...[]byte) *failingPrefixBackupStorage {
	copied := make([][]byte, len(failPrefixes))
	for i, p := range failPrefixes {
		copied[i] = append([]byte(nil), p...)
	}
	return &failingPrefixBackupStorage{
		MemoryDB:     storage.NewMemoryDb(),
		failPrefixes: copied,
		failErr:      failErr,
	}
}

func (s *failingPrefixBackupStorage) GetBackupPath() string { return "" }

func (s *failingPrefixBackupStorage) BatchDelete(keys [][]byte) error {
	for _, k := range keys {
		if err := s.MemoryDB.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *failingPrefixBackupStorage) Put(key, value []byte) error {
	s.mu.Lock()
	for _, pref := range s.failPrefixes {
		if bytes.HasPrefix(key, pref) {
			s.mu.Unlock()
			return s.failErr
		}
	}
	s.mu.Unlock()
	return s.MemoryDB.Put(key, value)
}

type preImageDiscardPayload struct {
	preImages    [][2][]byte
	changelogErr error
	discarded    *atomic.Bool
}

func (p preImageDiscardPayload) PreImageBatch() [][2][]byte { return p.preImages }
func (p preImageDiscardPayload) WriteChangelog() error      { return p.changelogErr }
func (p preImageDiscardPayload) Discard()                   { p.discarded.Store(true) }

func TestCommitWorker_ValidatorNomtUndoFailureIsFatalAndCleansPartialUndo(t *testing.T) {
	t.Run("account_undo_write_fails", func(t *testing.T) {
		backupStore := newFailingPrefixBackupStorage(errors.New("disk full"), []byte(storage.NomtUndoKindAccount))
		sm := storage.NewStorageManager()
		require.NoError(t, sm.AddStorageBackupDb(backupStore))

		bp := &BlockProcessor{
			storageManager: sm,
			commitChannel:  make(chan CommitJob, 4),
		}
		go bp.commitWorker()
		defer close(bp.commitChannel)

		var accDisc, stakeDisc, scDisc atomic.Bool
		errCh := make(chan error, 1)
		doneCh := make(chan struct{})
		bp.commitChannel <- CommitJob{
			Block: newFailClosedTestBlock(),
			AccountNomtPayload: preImageDiscardPayload{
				preImages: [][2][]byte{{[]byte("acct1"), []byte("gen1")}},
				discarded: &accDisc,
			},
			StakeNomtPayload: preImageDiscardPayload{
				preImages: [][2][]byte{{[]byte("stake1"), []byte("gen_s1")}},
				discarded: &stakeDisc,
			},
			SmartContractNomtPayload: trackingDiscardPayload{discarded: &scDisc},
			DoneChan:                 doneCh,
			ErrChan:                  errCh,
		}

		select {
		case err := <-errCh:
			require.Error(t, err)
			assert.Contains(t, err.Error(), "account NOMT undo durability failed")
			assert.Contains(t, err.Error(), "disk full")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for account undo failure")
		}
		select {
		case <-doneCh:
			t.Fatal("DoneChan must not be closed on undo failure")
		case <-time.After(100 * time.Millisecond):
		}

		require.Error(t, bp.GetLastCommitErr())
		assert.True(t, accDisc.Load(), "account payload must be discarded")
		assert.True(t, stakeDisc.Load(), "stake payload must be discarded")
		assert.True(t, scDisc.Load(), "SC payload must be discarded")
	})

	t.Run("stake_undo_write_fails_cleans_account_undo", func(t *testing.T) {
		backupStore := newFailingPrefixBackupStorage(errors.New("disk full"), []byte(storage.NomtUndoKindStake))
		sm := storage.NewStorageManager()
		require.NoError(t, sm.AddStorageBackupDb(backupStore))

		bp := &BlockProcessor{
			storageManager: sm,
			commitChannel:  make(chan CommitJob, 4),
		}
		go bp.commitWorker()
		defer close(bp.commitChannel)

		var accDisc, stakeDisc, scDisc atomic.Bool
		errCh := make(chan error, 1)
		doneCh := make(chan struct{})
		bp.commitChannel <- CommitJob{
			Block: newFailClosedTestBlock(),
			AccountNomtPayload: preImageDiscardPayload{
				preImages: [][2][]byte{{[]byte("acct1"), []byte("gen1")}},
				discarded: &accDisc,
			},
			StakeNomtPayload: preImageDiscardPayload{
				preImages: [][2][]byte{{[]byte("stake1"), []byte("gen_s1")}},
				discarded: &stakeDisc,
			},
			SmartContractNomtPayload: trackingDiscardPayload{discarded: &scDisc},
			DoneChan:                 doneCh,
			ErrChan:                  errCh,
		}

		select {
		case err := <-errCh:
			require.Error(t, err)
			assert.Contains(t, err.Error(), "stake NOMT undo durability failed")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for stake undo failure")
		}

		require.Error(t, bp.GetLastCommitErr())
		assert.True(t, accDisc.Load())
		assert.True(t, stakeDisc.Load())
		assert.True(t, scDisc.Load())

		_, hasPartialAcc := storage.LoadNomtUndoBatch(backupStore, storage.NomtUndoKindAccount, 10)
		assert.False(t, hasPartialAcc, "partial account undo at block 10 must be deleted when stake undo write fails")
	})
}

func TestCommitWorker_DownstreamSmartContractChangelogFailureCleansValidatorUndo(t *testing.T) {
	backupStore := newFailingPrefixBackupStorage(errors.New("unused"))
	sm := storage.NewStorageManager()
	require.NoError(t, sm.AddStorageBackupDb(backupStore))

	bp := &BlockProcessor{
		storageManager: sm,
		commitChannel:  make(chan CommitJob, 4),
	}
	go bp.commitWorker()
	defer close(bp.commitChannel)

	var accDisc, stakeDisc, scDisc atomic.Bool
	errCh := make(chan error, 1)
	bp.commitChannel <- CommitJob{
		Block: newFailClosedTestBlock(),
		AccountNomtPayload: preImageDiscardPayload{
			preImages: [][2][]byte{{[]byte("acct1"), []byte("gen1")}},
			discarded: &accDisc,
		},
		StakeNomtPayload: preImageDiscardPayload{
			preImages: [][2][]byte{{[]byte("stake1"), []byte("gen_s1")}},
			discarded: &stakeDisc,
		},
		SmartContractNomtPayload: trackingDiscardPayload{
			err:       errors.New("sc changelog write error"),
			discarded: &scDisc,
		},
		ErrChan: errCh,
	}

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "smart contract changelog durability failed")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for smart contract changelog failure")
	}

	assert.True(t, accDisc.Load())
	assert.True(t, stakeDisc.Load())
	assert.True(t, scDisc.Load())

	_, hasAcc := storage.LoadNomtUndoBatch(backupStore, storage.NomtUndoKindAccount, 10)
	_, hasStake := storage.LoadNomtUndoBatch(backupStore, storage.NomtUndoKindStake, 10)
	assert.False(t, hasAcc, "account undo for failed block 10 must be deleted on downstream changelog failure")
	assert.False(t, hasStake, "stake undo for failed block 10 must be deleted on downstream changelog failure")
}
