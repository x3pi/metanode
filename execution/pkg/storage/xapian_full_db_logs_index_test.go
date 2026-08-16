package storage

// See xapian_full_db_logs_index.go's doc comment / note/
// tee_dual_mode_execution_plan.md §5b (2026-08-16) for the "why".

import (
	"bytes"
	"testing"
)

// testMemStorage adapts *MemoryDB to the full Storage interface for this
// test file — MemoryDB predates GetBackupPath/BatchDelete being added to
// the interface (see pkg/mvm/ta_boundary_harness_test.go's identical
// harnessMemoryStorage, same fix, same reason) and was never updated for
// them; not worth changing production code just for a test double.
type testMemStorage struct{ *MemoryDB }

func (testMemStorage) GetBackupPath() string { return "" }
func (s testMemStorage) BatchDelete(keys [][]byte) error {
	for _, k := range keys {
		if err := s.MemoryDB.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func newTestMemStorage() testMemStorage { return testMemStorage{NewMemoryDb()} }

func TestFullDbLogsLatestKey_IsPrefixedAndAddressSpecific(t *testing.T) {
	k1 := FullDbLogsLatestKey("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	k2 := FullDbLogsLatestKey("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if bytes.Equal(k1, k2) {
		t.Fatalf("keys for different addresses must differ, got the same key %q for both", k1)
	}
	if !bytes.HasPrefix(k1, []byte(fullDbLogsLatestKeyPrefix)) {
		t.Errorf("key %q does not carry the expected prefix %q", k1, fullDbLogsLatestKeyPrefix)
	}
}

func TestPutGetLatestFullDbLogsForAddress_RoundTrip(t *testing.T) {
	db := newTestMemStorage()
	addr := "cccccccccccccccccccccccccccccccccccccc"
	want := []byte("serialized XapianLog::ComprehensiveLog bytes")

	if err := PutLatestFullDbLogsForAddress(db, addr, want); err != nil {
		t.Fatalf("PutLatestFullDbLogsForAddress: %v", err)
	}

	got, ok := GetLatestFullDbLogsForAddress(db, addr)
	if !ok {
		t.Fatalf("GetLatestFullDbLogsForAddress: ok=false right after a successful Put")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("GetLatestFullDbLogsForAddress = %q, want %q", got, want)
	}
}

func TestGetLatestFullDbLogsForAddress_MissIsNotAnError(t *testing.T) {
	db := newTestMemStorage()

	got, ok := GetLatestFullDbLogsForAddress(db, "dddddddddddddddddddddddddddddddddddddd")
	if ok {
		t.Fatalf("GetLatestFullDbLogsForAddress: ok=true for an address that was never written, got %q", got)
	}
	if got != nil {
		t.Errorf("GetLatestFullDbLogsForAddress: expected nil bytes on a miss, got %q", got)
	}
}

func TestPutLatestFullDbLogsForAddress_LastWriteWins(t *testing.T) {
	db := newTestMemStorage()
	addr := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	if err := PutLatestFullDbLogsForAddress(db, addr, []byte("block N")); err != nil {
		t.Fatalf("PutLatestFullDbLogsForAddress (first write): %v", err)
	}
	if err := PutLatestFullDbLogsForAddress(db, addr, []byte("block N+1")); err != nil {
		t.Fatalf("PutLatestFullDbLogsForAddress (second write): %v", err)
	}

	got, ok := GetLatestFullDbLogsForAddress(db, addr)
	if !ok {
		t.Fatalf("GetLatestFullDbLogsForAddress: ok=false after 2 writes")
	}
	if !bytes.Equal(got, []byte("block N+1")) {
		t.Errorf("expected the second (latest) write to win, got %q", got)
	}
}
