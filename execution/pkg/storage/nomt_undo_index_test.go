package storage

import (
	"bytes"
	"testing"
)

func TestNomtUndoIndex_CompactRoundTripAndRollingPrune(t *testing.T) {
	kAcc := NomtUndoKey(NomtUndoKindAccount, 42)
	kStake := NomtUndoKey(NomtUndoKindStake, 42)
	if len(kAcc) != 10 {
		t.Fatalf("NomtUndoKey account len = %d, want 10", len(kAcc))
	}
	if len(kStake) != 10 {
		t.Fatalf("NomtUndoKey stake len = %d, want 10", len(kStake))
	}
	if bytes.Equal(kAcc, kStake) {
		t.Fatalf("account and stake keys must differ")
	}

	db := newTestMemStorage()
	entries := [][2][]byte{
		{[]byte("new_fork_key"), nil},
		{[]byte("genesis_key_20_bytes"), []byte("genesis_bal")},
	}

	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, 5, entries); err != nil {
		t.Fatalf("PersistNomtUndoBatch(5): %v", err)
	}
	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, 6, entries); err != nil {
		t.Fatalf("PersistNomtUndoBatch(6): %v", err)
	}
	if err := PersistNomtUndoBatch(db, NomtUndoKindStake, 6, entries); err != nil {
		t.Fatalf("PersistNomtUndoBatch(stake, 6): %v", err)
	}

	got, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 5)
	if !ok || len(got) != 2 {
		t.Fatalf("LoadNomtUndoBatch(5) ok=%v len=%d, want ok=true len=2", ok, len(got))
	}
	if !bytes.Equal(got[0][0], []byte("new_fork_key")) || got[0][1] != nil {
		t.Fatalf("entry 0 = (%q, %v), want (new_fork_key, nil)", got[0][0], got[0][1])
	}
	if !bytes.Equal(got[1][0], []byte("genesis_key_20_bytes")) || !bytes.Equal(got[1][1], []byte("genesis_bal")) {
		t.Fatalf("entry 1 = (%q, %q), want (genesis_key_20_bytes, genesis_bal)", got[1][0], got[1][1])
	}

	// Writing at NomtUndoRetentionBlocks + 5 must prune block 5 while preserving block 6.
	pruneTriggerBlock := NomtUndoRetentionBlocks + 5
	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, pruneTriggerBlock, entries); err != nil {
		t.Fatalf("PersistNomtUndoBatch(%d): %v", pruneTriggerBlock, err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 5); ok {
		t.Fatalf("block 5 must be pruned when persisting block %d", pruneTriggerBlock)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 6); !ok {
		t.Fatalf("block 6 must remain intact after persisting block %d", pruneTriggerBlock)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, pruneTriggerBlock); !ok {
		t.Fatalf("block %d must be readable", pruneTriggerBlock)
	}

	DeleteNomtUndoForBlock(db, 6)
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 6); ok {
		t.Fatalf("account undo for block 6 must be deleted")
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindStake, 6); ok {
		t.Fatalf("stake undo for block 6 must be deleted")
	}
}
