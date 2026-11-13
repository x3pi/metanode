package storage

import (
	"bytes"
	"encoding/binary"
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

func TestNomtUndoIndex_EmptyBatchDeletesStaleEntryAndStillPrunesOldWindow(t *testing.T) {
	db := newTestMemStorage()
	entries := [][2][]byte{
		{[]byte("acct_a"), []byte("val_a")},
	}

	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, 5, entries); err != nil {
		t.Fatalf("PersistNomtUndoBatch(5): %v", err)
	}
	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, 10, entries); err != nil {
		t.Fatalf("PersistNomtUndoBatch(10): %v", err)
	}

	// Re-writing block 10 with an empty batch (no changes) must remove stale undo data.
	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, 10, nil); err != nil {
		t.Fatalf("PersistNomtUndoBatch(10, nil): %v", err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 10); ok {
		t.Fatalf("stale undo entry for block 10 must be deleted when overwritten with empty batch")
	}

	// Persisting an empty batch at NomtUndoRetentionBlocks + 5 must still prune block 5.
	pruneTrigger := NomtUndoRetentionBlocks + 5
	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, pruneTrigger, [][2][]byte{}); err != nil {
		t.Fatalf("PersistNomtUndoBatch(%d, empty): %v", pruneTrigger, err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 5); ok {
		t.Fatalf("block 5 must be pruned even when block %d has an empty batch", pruneTrigger)
	}
}

func TestNomtUndoIndex_DecodeCorruptedPayloadsFailClosed(t *testing.T) {
	validEncoded := EncodeNomtUndoBatch([][2][]byte{
		{[]byte("k1"), []byte("v1")},
	})

	cases := []struct {
		name string
		raw  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"invalid_varint_count", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}},
		{"count_exceeds_max", binary.AppendUvarint(nil, maxNomtUndoEntries+1)},
		{"invalid_keylen_varint", append(binary.AppendUvarint(nil, 1), 0x80)},
		{"truncated_key", append(binary.AppendUvarint(binary.AppendUvarint(nil, 1), 10), []byte("abc")...)},
		{
			"invalid_vallen_varint",
			func() []byte {
				b := binary.AppendUvarint(nil, 1)
				b = binary.AppendUvarint(b, 3)
				b = append(b, []byte("abc")...)
				return append(b, 0x80)
			}(),
		},
		{
			"truncated_val",
			func() []byte {
				b := binary.AppendUvarint(nil, 1)
				b = binary.AppendUvarint(b, 3)
				b = append(b, []byte("abc")...)
				b = binary.AppendUvarint(b, 10)
				return append(b, []byte("xy")...)
			}(),
		},
		{"trailing_bytes", append(append([]byte(nil), validEncoded...), 0xde, 0xad)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeNomtUndoBatch(tc.raw); err == nil {
				t.Fatalf("DecodeNomtUndoBatch(%s) expected error, got nil", tc.name)
			}
		})
	}

	if _, ok := LoadNomtUndoBatch(nil, NomtUndoKindAccount, 1); ok {
		t.Fatalf("LoadNomtUndoBatch(nil) must return false")
	}

	db := newTestMemStorage()
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 999); ok {
		t.Fatalf("LoadNomtUndoBatch on missing key must return false")
	}
	if err := db.Put(NomtUndoKey(NomtUndoKindAccount, 77), []byte{0x01, 0x05}); err != nil {
		t.Fatalf("db.Put corrupted payload: %v", err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 77); ok {
		t.Fatalf("LoadNomtUndoBatch with corrupted stored payload must return false")
	}
}

func TestNomtUndoIndex_EdgeValuesZeroByteVsNilAndLargePayloads(t *testing.T) {
	db := newTestMemStorage()
	sample := [][2][]byte{{[]byte("k"), []byte("v")}}

	// blockNum == 0 must be a no-op.
	if err := PersistNomtUndoBatch(db, NomtUndoKindAccount, 0, sample); err != nil {
		t.Fatalf("PersistNomtUndoBatch(0): %v", err)
	}
	if _, ok := LoadNomtUndoBatch(db, NomtUndoKindAccount, 0); ok {
		t.Fatalf("block 0 must not persist an undo record")
	}

	largeKey := bytes.Repeat([]byte{0xab}, 300)
	largeVal := bytes.Repeat([]byte{0xcd}, 1024)
	origEntries := [][2][]byte{
		{[]byte("inserted_nil"), nil},
		{[]byte("inserted_empty_slice"), []byte{}},
		{[]byte("existing_single_zero"), []byte{0x00}},
		{append([]byte(nil), largeKey...), append([]byte(nil), largeVal...)},
	}

	if err := PersistNomtUndoBatch(db, NomtUndoKindStake, 12, origEntries); err != nil {
		t.Fatalf("PersistNomtUndoBatch(12): %v", err)
	}
	got, ok := LoadNomtUndoBatch(db, NomtUndoKindStake, 12)
	if !ok || len(got) != 4 {
		t.Fatalf("LoadNomtUndoBatch(12) ok=%v len=%d, want true, 4", ok, len(got))
	}
	if got[0][1] != nil {
		t.Fatalf("entry 0 expected nil, got %x", got[0][1])
	}
	if got[1][1] != nil {
		t.Fatalf("entry 1 expected nil, got %x", got[1][1])
	}
	if got[2][1] == nil || len(got[2][1]) != 1 || got[2][1][0] != 0x00 {
		t.Fatalf("entry 2 expected single 0x00 byte (non-nil), got %v", got[2][1])
	}
	if !bytes.Equal(got[3][0], largeKey) || !bytes.Equal(got[3][1], largeVal) {
		t.Fatalf("entry 3 large key/val mismatch")
	}
	// Mutating decoded slice must not alias caller's original slice.
	got[3][1][0] ^= 0xff
	if bytes.Equal(got[3][1], origEntries[3][1]) {
		t.Fatalf("mutated decoded payload unexpectedly aliased origEntries")
	}
}
