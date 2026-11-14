package main

import (
	"bytes"
	"errors"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

type catchupUndoMemStorage struct {
	*storage.MemoryDB
	failPrefix []byte
	failErr    error
}

func newCatchupUndoMemStorage() *catchupUndoMemStorage {
	return &catchupUndoMemStorage{MemoryDB: storage.NewMemoryDb()}
}

func (s *catchupUndoMemStorage) GetBackupPath() string { return "" }

func (s *catchupUndoMemStorage) BatchDelete(keys [][]byte) error {
	for _, k := range keys {
		if err := s.MemoryDB.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *catchupUndoMemStorage) Put(key, value []byte) error {
	if s.failPrefix != nil && bytes.HasPrefix(key, s.failPrefix) {
		return s.failErr
	}
	return s.MemoryDB.Put(key, value)
}

var (
	catchupAccPre   = [][2][]byte{{[]byte("acc"), []byte("old_acc")}}
	catchupStakePre = [][2][]byte{{[]byte("stk"), []byte("old_stk")}}
)

// The undo record must already be durable when commit starts touching NOMT.
func TestCatchupUndo_WrittenBeforeCommit(t *testing.T) {
	db := newCatchupUndoMemStorage()
	discarded := false
	commitSawUndo := false

	err := commitCatchupBlockWithUndo(db, 9, true, catchupAccPre, true, catchupStakePre,
		func() { discarded = true },
		func() error {
			_, accOK := storage.LoadNomtUndoBatch(db, storage.NomtUndoKindAccount, 9)
			_, stakeOK := storage.LoadNomtUndoBatch(db, storage.NomtUndoKindStake, 9)
			commitSawUndo = accOK && stakeOK
			return nil
		})

	require.NoError(t, err)
	assert.True(t, commitSawUndo, "account+stake undo must be persisted before commit runs")
	assert.False(t, discarded)
}

// Undo write fails: commit must never run, payloads are discarded and no partial record survives.
func TestCatchupUndo_UndoFailureSkipsCommitAndDiscards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{"account write fails", storage.NomtUndoKindAccount},
		{"stake write fails", storage.NomtUndoKindStake},
	} {
		t.Run(tc.name, func(t *testing.T) {
			injected := errors.New("disk full")
			db := newCatchupUndoMemStorage()
			db.failPrefix, db.failErr = []byte(tc.prefix), injected

			discarded, committed := 0, 0
			err := commitCatchupBlockWithUndo(db, 9, true, catchupAccPre, true, catchupStakePre,
				func() { discarded++ },
				func() error { committed++; return nil })

			require.ErrorIs(t, err, injected)
			assert.Equal(t, 0, committed, "NOMT commit must not run when undo is not durable")
			assert.Equal(t, 1, discarded, "payloads must be discarded exactly once")
			_, accOK := storage.LoadNomtUndoBatch(db, storage.NomtUndoKindAccount, 9)
			_, stakeOK := storage.LoadNomtUndoBatch(db, storage.NomtUndoKindStake, 9)
			assert.False(t, accOK, "no account undo record may survive a failed write-ahead")
			assert.False(t, stakeOK, "no stake undo record may survive a failed write-ahead")
		})
	}
}

// Commit fails after the undo is durable: the error is propagated and the undo is kept, because commit
// may already have flushed part of the block to NOMT and the record is the only way to roll it back.
func TestCatchupUndo_CommitFailureKeepsUndo(t *testing.T) {
	db := newCatchupUndoMemStorage()
	commitErr := errors.New("persist failed")
	discarded := false

	err := commitCatchupBlockWithUndo(db, 9, true, catchupAccPre, true, catchupStakePre,
		func() { discarded = true },
		func() error { return commitErr })

	require.ErrorIs(t, err, commitErr)
	assert.False(t, discarded, "commit owns discard on its own failures; the wrapper must not double-discard")
	got, ok := storage.LoadNomtUndoBatch(db, storage.NomtUndoKindAccount, 9)
	require.True(t, ok, "account undo must be kept after a commit failure")
	assert.Equal(t, []byte("old_acc"), got[0][1])
	_, ok = storage.LoadNomtUndoBatch(db, storage.NomtUndoKindStake, 9)
	assert.True(t, ok, "stake undo must be kept after a commit failure")
}

// End-to-end with the real commitVerifiedCatchupBlock: a root mismatch discards the payloads, never
// commits NOMT, and leaves only the (harmless) undo record behind.
func TestCatchupUndo_RootMismatchWithRealCommit(t *testing.T) {
	db := newCatchupUndoMemStorage()
	sc, acct, stake := &mockNomtCatchupPayload{}, &mockNomtCatchupPayload{}, &mockNomtCatchupPayload{}
	expected := e_common.HexToHash("0x1111")
	wrong := e_common.HexToHash("0x2222")

	err := commitCatchupBlockWithUndo(db, 9, true, catchupAccPre, true, catchupStakePre,
		func() { discardPayloads(sc, acct, stake) },
		func() error {
			return commitVerifiedCatchupBlock(9, expected, expected, sc, acct, wrong, stake, expected, nil, nil, nil, nil)
		})

	require.Error(t, err)
	assert.Equal(t, int32(0), acct.commitAsyncCount.Load(), "NOMT must not be committed on root mismatch")
	assert.Equal(t, int32(1), acct.discardCount.Load())
	assert.Equal(t, int32(1), stake.discardCount.Load())
	assert.Equal(t, int32(1), sc.discardCount.Load())
}

// Without a backup DB (e.g. MPT-only setup) the wrapper is a pass-through.
func TestCatchupUndo_NilBackupDBStillCommits(t *testing.T) {
	committed := false
	err := commitCatchupBlockWithUndo(nil, 9, true, catchupAccPre, true, catchupStakePre,
		func() { t.Fatal("discard must not be called") },
		func() error { committed = true; return nil })
	require.NoError(t, err)
	assert.True(t, committed)
}

func TestDiscardPayloads_SkipsNilAndNonDiscarders(t *testing.T) {
	p := &mockNomtCatchupPayload{}
	discardPayloads(nil, "not a payload", p)
	assert.Equal(t, int32(1), p.discardCount.Load())
}
