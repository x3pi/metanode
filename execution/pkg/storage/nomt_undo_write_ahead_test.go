package storage

import (
	"bytes"
	"errors"
	"testing"
)

// failPutStorage fails every Put whose key starts with one of failPrefixes.
type failPutStorage struct {
	testMemStorage
	failPrefixes [][]byte
	err          error
}

func (s failPutStorage) Put(key, value []byte) error {
	for _, p := range s.failPrefixes {
		if bytes.HasPrefix(key, p) {
			return s.err
		}
	}
	return s.testMemStorage.Put(key, value)
}

var (
	undoAccPre   = [][2][]byte{{[]byte("acc_key"), []byte("acc_old")}, {[]byte("acc_new"), nil}}
	undoStakePre = [][2][]byte{{[]byte("stake_key"), []byte("stake_old")}}
)

func mustLoadUndo(t *testing.T, db Storage, kind string, bn uint64) [][2][]byte {
	t.Helper()
	got, ok := LoadNomtUndoBatch(db, kind, bn)
	if !ok {
		t.Fatalf("LoadNomtUndoBatch(%s, %d): record missing", kind, bn)
	}
	return got
}

func TestPersistNomtUndoForBlock_WritesBothKinds(t *testing.T) {
	db := newTestMemStorage()
	if err := PersistNomtUndoForBlock(db, 7, true, undoAccPre, true, undoStakePre); err != nil {
		t.Fatalf("PersistNomtUndoForBlock: %v", err)
	}
	acc := mustLoadUndo(t, db, NomtUndoKindAccount, 7)
	if len(acc) != 2 || !bytes.Equal(acc[0][1], []byte("acc_old")) || acc[1][1] != nil {
		t.Fatalf("account undo = %q, want pre-images preserved (incl. nil for inserted key)", acc)
	}
	stake := mustLoadUndo(t, db, NomtUndoKindStake, 7)
	if len(stake) != 1 || !bytes.Equal(stake[0][1], []byte("stake_old")) {
		t.Fatalf("stake undo = %q, want stake_old", stake)
	}
}

func TestPersistNomtUndoForBlock_FlagsSelectKinds(t *testing.T) {
	db := newTestMemStorage()
	if err := PersistNomtUndoForBlock(db, 7, false, undoAccPre, true, undoStakePre); err != nil {
		t.Fatalf("PersistNomtUndoForBlock: %v", err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 7); ok {
		t.Fatalf("account undo written although writeAccount=false (changelog DB owns account rollback)")
	}
	mustLoadUndo(t, db, NomtUndoKindStake, 7)
}

func TestPersistNomtUndoForBlock_NilDBAndGenesisAreNoop(t *testing.T) {
	if err := PersistNomtUndoForBlock(nil, 7, true, undoAccPre, true, undoStakePre); err != nil {
		t.Fatalf("nil backup DB must be a no-op, got %v", err)
	}
	db := newTestMemStorage()
	if err := PersistNomtUndoForBlock(db, 0, true, undoAccPre, true, undoStakePre); err != nil {
		t.Fatalf("block 0 must be a no-op, got %v", err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 0); ok {
		t.Fatalf("block 0 must never get an undo record")
	}
}

// Account write fails: a stale stake record left at this height by an earlier (forked) block must be
// removed too, otherwise a rollback would combine it with a missing account record.
func TestPersistNomtUndoForBlock_AccountFailureRemovesStaleStakeRecord(t *testing.T) {
	base := newTestMemStorage()
	if err := PersistNomtUndoBatch(base, NomtUndoKindStake, 7, [][2][]byte{{[]byte("stale"), []byte("fork")}}); err != nil {
		t.Fatalf("seed stale stake record: %v", err)
	}
	injected := errors.New("disk full")
	db := failPutStorage{testMemStorage: base, failPrefixes: [][]byte{[]byte(NomtUndoKindAccount)}, err: injected}

	err := PersistNomtUndoForBlock(db, 7, true, undoAccPre, true, undoStakePre)
	if !errors.Is(err, injected) {
		t.Fatalf("err = %v, want wrapped %v", err, injected)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 7); ok {
		t.Fatalf("account record must not exist after failed write")
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindStake, 7); ok {
		t.Fatalf("stale stake record must be removed after account write failure")
	}
}

// Stake write fails after the account record was written: the account record must be removed so no
// half-written pair survives.
func TestPersistNomtUndoForBlock_StakeFailureRemovesWrittenAccountRecord(t *testing.T) {
	injected := errors.New("io error")
	db := failPutStorage{testMemStorage: newTestMemStorage(), failPrefixes: [][]byte{[]byte(NomtUndoKindStake)}, err: injected}

	err := PersistNomtUndoForBlock(db, 7, true, undoAccPre, true, undoStakePre)
	if !errors.Is(err, injected) {
		t.Fatalf("err = %v, want wrapped %v", err, injected)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 7); ok {
		t.Fatalf("account record must be removed when the stake write fails")
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindStake, 7); ok {
		t.Fatalf("stake record must not exist after failed write")
	}
}

// Failures at height N must not touch the undo records of other heights.
func TestPersistNomtUndoForBlock_FailureLeavesOtherBlocksIntact(t *testing.T) {
	base := newTestMemStorage()
	if err := PersistNomtUndoForBlock(base, 6, true, undoAccPre, true, undoStakePre); err != nil {
		t.Fatalf("seed block 6: %v", err)
	}
	db := failPutStorage{testMemStorage: base, failPrefixes: [][]byte{[]byte(NomtUndoKindStake)}, err: errors.New("io")}
	if err := PersistNomtUndoForBlock(db, 7, true, undoAccPre, true, undoStakePre); err == nil {
		t.Fatalf("expected failure at block 7")
	}
	mustLoadUndo(t, db, NomtUndoKindAccount, 6)
	mustLoadUndo(t, db, NomtUndoKindStake, 6)
}

// Empty pre-image batch (block touched no key) must clear a stale record from an earlier block at the
// same height instead of leaving it to be applied by a later rollback.
func TestPersistNomtUndoForBlock_EmptyBatchClearsStaleRecord(t *testing.T) {
	db := newTestMemStorage()
	if err := PersistNomtUndoForBlock(db, 7, true, undoAccPre, true, undoStakePre); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := PersistNomtUndoForBlock(db, 7, true, nil, true, nil); err != nil {
		t.Fatalf("PersistNomtUndoForBlock(empty): %v", err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 7); ok {
		t.Fatalf("stale account record must be cleared by an empty batch")
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindStake, 7); ok {
		t.Fatalf("stale stake record must be cleared by an empty batch")
	}
}

// failDeleteStorage fails every Delete whose key starts with one of failPrefixes.
type failDeleteStorage struct {
	testMemStorage
	failPrefixes [][]byte
	err          error
}

func (s failDeleteStorage) Delete(key []byte) error {
	for _, p := range s.failPrefixes {
		if bytes.HasPrefix(key, p) {
			return s.err
		}
	}
	return s.testMemStorage.Delete(key)
}

// When a batch is empty, PersistNomtUndoBatch attempts to delete any stale entry for that height.
// If the underlying delete fails, the error must be surfaced fail-closed instead of ignored.
func TestPersistNomtUndoBatch_EmptyBatchDeleteErrorSurfaced(t *testing.T) {
	injected := errors.New("delete error")
	db := failDeleteStorage{testMemStorage: newTestMemStorage(), failPrefixes: [][]byte{[]byte(NomtUndoKindAccount)}, err: injected}
	err := PersistNomtUndoBatch(db, NomtUndoKindAccount, 7, nil)
	if !errors.Is(err, injected) {
		t.Fatalf("err = %v, want wrapped %v", err, injected)
	}
}
