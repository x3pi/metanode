package mvcc

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/types"
)

// acctState builds a minimal AccountState whose Nonce we use as a version
// marker, so tests can verify Read() returned the value written at the
// expected floor version without depending on AccountState internals.
func acctState(marker uint64) types.AccountState {
	s := mt_state.NewAccountState(common.Address{})
	s.SetNonce(marker)
	return s
}

// ─── VersionedAccountState: binary-search floor lookup (B2 regression) ───

func TestVersionedAccountState_EmptyReadReturnsBaseVersion(t *testing.T) {
	v := NewVersionedAccountState()
	state, ver, wid, _ := v.Read(100)
	if state != nil || ver != BaseVersion || wid != BaseWriteID {
		t.Fatalf("empty read: got state=%v ver=%v wid=%v, want nil/BaseVersion/BaseWriteID", state, ver, wid)
	}
}

func TestVersionedAccountState_FloorReadOutOfOrderInserts(t *testing.T) {
	v := NewVersionedAccountState()
	// Insert out of order to make sure the sorted-slice maintenance in Write
	// (not just Read) is correct.
	v.Write(5, acctState(5))
	v.Write(2, acctState(2))
	v.Write(8, acctState(8))

	cases := []struct {
		request Version
		wantVer Version
	}{
		{1, BaseVersion}, // below everything written
		{2, 2},
		{3, 2}, // floor(3) == 2
		{4, 2},
		{5, 5},
		{7, 5}, // floor(7) == 5 (8 not yet visible to a version-7 reader)
		{8, 8},
		{100, 8},
	}
	for _, c := range cases {
		state, ver, _, _ := v.Read(c.request)
		if ver != c.wantVer {
			t.Errorf("Read(%d): got version %d, want %d", c.request, ver, c.wantVer)
			continue
		}
		if c.wantVer == BaseVersion {
			if state != nil {
				t.Errorf("Read(%d): expected nil state for BaseVersion", c.request)
			}
			continue
		}
		if state == nil || state.Nonce() != uint64(c.wantVer) {
			t.Errorf("Read(%d): got wrong state (nonce marker), want marker %d", c.request, c.wantVer)
		}
	}
}

func TestVersionedAccountState_DeleteFallsThroughToLowerVersion(t *testing.T) {
	v := NewVersionedAccountState()
	v.Write(3, acctState(3))
	v.Write(7, acctState(7))

	if _, ver, _, _ := v.Read(10); ver != 7 {
		t.Fatalf("expected floor version 7 before delete, got %d", ver)
	}

	v.Delete(7)

	if _, ver, _, _ := v.Read(10); ver != 3 {
		t.Fatalf("expected floor version 3 after deleting 7, got %d", ver)
	}

	v.Delete(3)
	if _, ver, _, _ := v.Read(10); ver != BaseVersion {
		t.Fatalf("expected BaseVersion after deleting everything, got %d", ver)
	}
}

func TestVersionedAccountState_DeleteThenReinsertMaintainsSortedOrder(t *testing.T) {
	v := NewVersionedAccountState()
	v.Write(1, acctState(1))
	v.Write(2, acctState(2))
	v.Write(3, acctState(3))
	v.Delete(2)
	v.Write(2, acctState(20))

	state, ver, _, _ := v.Read(2)
	if ver != 2 || state == nil || state.Nonce() != 20 {
		t.Fatalf("expected version 2 with marker 20 after delete+reinsert, got ver=%d state=%v", ver, state)
	}
	if _, ver3, _, _ := v.Read(3); ver3 != 3 {
		t.Fatalf("sorted order broken after delete+reinsert: floor(3) = %d, want 3", ver3)
	}
}

func TestVersionedAccountState_WriteIDChangesOnRewrite_ButNotOnUntouchedRead(t *testing.T) {
	v := NewVersionedAccountState()
	v.Write(1, acctState(1))
	_, _, wid1, _ := v.Read(5)
	_, _, wid1Again, _ := v.Read(5)
	if wid1 != wid1Again {
		t.Fatalf("reading twice without a write must return the same WriteID: %d != %d", wid1, wid1Again)
	}

	v.Write(1, acctState(1)) // rewrite same version, same value
	_, _, wid2, _ := v.Read(5)
	if wid1 == wid2 {
		t.Fatal("expected WriteID to change after rewriting the same version (used to detect conflicts)")
	}
}

// ─── VersionedStorage: same floor-lookup contract, []byte values ───

func TestVersionedStorage_FloorReadOutOfOrderInserts(t *testing.T) {
	v := NewVersionedStorage()
	if val, ver, _, _ := v.Read(100); val != nil || ver != BaseVersion {
		t.Fatalf("empty read: got val=%v ver=%v, want nil/BaseVersion", val, ver)
	}

	v.Write(5, []byte("five"))
	v.Write(2, []byte("two"))
	v.Write(8, []byte("eight"))

	cases := []struct {
		request Version
		want    string
		wantVer Version
	}{
		{1, "", BaseVersion},
		{2, "two", 2},
		{4, "two", 2},
		{5, "five", 5},
		{7, "five", 5},
		{8, "eight", 8},
		{50, "eight", 8},
	}
	for _, c := range cases {
		val, ver, _, _ := v.Read(c.request)
		if ver != c.wantVer {
			t.Errorf("Read(%d): got version %d, want %d", c.request, ver, c.wantVer)
		}
		if string(val) != c.want {
			t.Errorf("Read(%d): got value %q, want %q", c.request, val, c.want)
		}
	}
}

func TestVersionedStorage_Delete(t *testing.T) {
	v := NewVersionedStorage()
	v.Write(4, []byte("a"))
	v.Delete(4)
	if val, ver, _, _ := v.Read(10); val != nil || ver != BaseVersion {
		t.Fatalf("expected empty after delete, got val=%v ver=%v", val, ver)
	}
}

// ─── MVCCAccountMap: "strictly less than requestVersion" self-read exclusion ───
//
// This is the invariant the whole engine's correctness depends on: a TX must
// never observe its own not-yet-committed write when it re-reads the same
// address mid-execution via the map (it reads its own write through
// mvccDB.localState instead). If this regresses, TXs would start seeing
// impossible in-progress states.

func TestMVCCAccountMap_ReadExcludesOwnVersion(t *testing.T) {
	m := NewMVCCAccountMap()
	addr := common.Address{0xAA}
	m.Write(addr, 3, acctState(3))

	if _, ver, _, _ := m.Read(addr, 3); ver != BaseVersion {
		t.Fatalf("tx index 3 must not see its own write via Read(3), got version %d", ver)
	}

	state, ver, _, _ := m.Read(addr, 4)
	if ver != 3 || state == nil || state.Nonce() != 3 {
		t.Fatalf("tx index 4 should see version 3's write, got ver=%d state=%v", ver, state)
	}

	if _, ver0, _, _ := m.Read(addr, 0); ver0 != BaseVersion {
		t.Fatalf("Read with requestVersion=0 must return BaseVersion, got %d", ver0)
	}
}

func TestMVCCAccountMap_ExportLatestReturnsHighestVersion(t *testing.T) {
	m := NewMVCCAccountMap()
	addrA := common.Address{0xAA}
	addrB := common.Address{0xBB}
	m.Write(addrA, 1, acctState(1))
	m.Write(addrA, 5, acctState(5))
	m.Write(addrB, 2, acctState(2))

	latest := m.ExportLatest()
	if got := latest[addrA]; got == nil || got.Nonce() != 5 {
		t.Errorf("expected addrA's latest marker=5, got %v", got)
	}
	if got := latest[addrB]; got == nil || got.Nonce() != 2 {
		t.Errorf("expected addrB's latest marker=2, got %v", got)
	}
}

func TestMVCCStorageMap_ReadExcludesOwnVersion(t *testing.T) {
	m := NewMVCCStorageMap()
	addr := common.Address{0xCC}
	m.Write(addr, "slot", 3, []byte("v3"))

	if _, ver, _, _ := m.Read(addr, "slot", 3); ver != BaseVersion {
		t.Fatalf("tx index 3 must not see its own write via Read(3), got version %d", ver)
	}
	if val, ver, _, _ := m.Read(addr, "slot", 4); ver != 3 || string(val) != "v3" {
		t.Fatalf("tx index 4 should see version 3's write, got ver=%d val=%s", ver, val)
	}
}
