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

func TestFullDbLogsLatestKey_NormalizesEIP55AndLowercaseHex(t *testing.T) {
	db := newTestMemStorage()
	eip55Addr := "0xAbCdEf0123456789AbCdEf0123456789AbCdEf01"
	lowerNoPrefix := "abcdef0123456789abcdef0123456789abcdef01"
	upperWithPrefix := "0XABCDEF0123456789ABCDEF0123456789ABCDEF01"

	if !bytes.Equal(FullDbLogsLatestKey(eip55Addr), FullDbLogsLatestKey(lowerNoPrefix)) {
		t.Fatalf("EIP-55 key %q != lowercase key %q", FullDbLogsLatestKey(eip55Addr), FullDbLogsLatestKey(lowerNoPrefix))
	}
	if !bytes.Equal(FullDbLogsLatestKey(upperWithPrefix), FullDbLogsLatestKey(lowerNoPrefix)) {
		t.Fatalf("0X uppercase key %q != lowercase key %q", FullDbLogsLatestKey(upperWithPrefix), FullDbLogsLatestKey(lowerNoPrefix))
	}

	payload := []byte("eip55-exported-xapian-frame")
	if err := PutLatestFullDbLogsForAddress(db, eip55Addr, payload); err != nil {
		t.Fatalf("PutLatestFullDbLogsForAddress: %v", err)
	}

	got, ok := GetLatestFullDbLogsForAddress(db, lowerNoPrefix)
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("GetLatestFullDbLogsForAddress(lowerNoPrefix) = (%q, %v), want (%q, true)", got, ok, payload)
	}
	gotUpper, okUpper := GetLatestFullDbLogsForAddress(db, upperWithPrefix)
	if !okUpper || !bytes.Equal(gotUpper, payload) {
		t.Fatalf("GetLatestFullDbLogsForAddress(upperWithPrefix) = (%q, %v), want (%q, true)", gotUpper, okUpper, payload)
	}
}

func TestPersistLatestFullDbLogsBatch_MergesMixedCaseAndMultiBatchDeterministically(t *testing.T) {
	db := newTestMemStorage()
	addr1EIP55 := "0xAaBbCcDdEe0011223344556677889900AaBbCc01"
	addr1Lower := "aabbccddee0011223344556677889900aabbcc01"
	addr2EIP55 := "0xFfEeDdCcBbAa99887766554433221100FfEeDd02"
	addr2Lower := "ffeeddccbbaa99887766554433221100ffeedd02"

	batches := []map[string][]byte{
		{
			addr1Lower: []byte("frameA|"),
			addr2EIP55: []byte("contract2_only"),
		},
		{
			addr1EIP55: []byte("frameB"),
		},
		{
			addr1Lower: nil, // empty slice must be ignored, not overwrite
		},
	}

	if err := PersistLatestFullDbLogsBatch(db, batches); err != nil {
		t.Fatalf("PersistLatestFullDbLogsBatch: %v", err)
	}

	got1, ok1 := GetLatestFullDbLogsForAddress(db, addr1Lower)
	if !ok1 || !bytes.Equal(got1, []byte("frameA|frameB")) {
		t.Fatalf("addr1 merged logs = (%q, %v), want (%q, true)", got1, ok1, "frameA|frameB")
	}

	got2, ok2 := GetLatestFullDbLogsForAddress(db, addr2Lower)
	if !ok2 || !bytes.Equal(got2, []byte("contract2_only")) {
		t.Fatalf("addr2 logs = (%q, %v), want (%q, true)", got2, ok2, "contract2_only")
	}
}
