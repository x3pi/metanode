package state_changelog

import (
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *StateChangelogDB {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "changelog")
	db, err := NewStateChangelogDB(dir, "test")
	if err != nil {
		t.Fatalf("NewStateChangelogDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestPruneBeforeBlock_PreservesFloorForQuietAddress reproduces the exact
// scenario the user described: an address that hasn't changed since before
// the prune cutoff must still resolve to its last known value for any block
// at or after the cutoff, not fall back to the genesis value.
func TestPruneBeforeBlock_PreservesFloorForQuietAddress(t *testing.T) {
	db := newTestDB(t)

	addrQuiet := []byte("quiet-address")
	addrActive := []byte("active-address")

	// addrQuiet: genesis -> changes once at block 50, then goes quiet forever.
	if err := db.WriteBlockChanges(50, []StateChange{
		{Key: addrQuiet, OldValue: []byte("genesis-value"), NewValue: []byte("value-at-50")},
	}); err != nil {
		t.Fatalf("WriteBlockChanges(50): %v", err)
	}

	// addrActive: changes at block 50 and again at block 150 (after the cutoff).
	if err := db.WriteBlockChanges(50, []StateChange{
		{Key: addrActive, OldValue: []byte("genesis-value"), NewValue: []byte("active-at-50")},
	}); err != nil {
		t.Fatalf("WriteBlockChanges(50) active: %v", err)
	}
	if err := db.WriteBlockChanges(150, []StateChange{
		{Key: addrActive, OldValue: []byte("active-at-50"), NewValue: []byte("active-at-150")},
	}); err != nil {
		t.Fatalf("WriteBlockChanges(150): %v", err)
	}

	// Prune everything strictly before block 100 (simulating an epoch boundary
	// two epochs back), the same call chain_state.go makes on every epoch transition.
	if err := db.PruneBeforeBlock(100); err != nil {
		t.Fatalf("PruneBeforeBlock: %v", err)
	}

	// The quiet address must still resolve correctly for any block >= 100,
	// even though its last write (block 50) is well before the cutoff.
	got, err := db.GetStateAt(addrQuiet, 120)
	if err != nil {
		t.Fatalf("GetStateAt(quiet, 120): %v", err)
	}
	if string(got) != "value-at-50" {
		t.Fatalf("GetStateAt(quiet, 120) = %q, want %q (compact-to-floor should have preserved the block-50 entry)", got, "value-at-50")
	}

	// The active address should resolve to its block-50 value for queries
	// between 100 and 150, and its block-150 value after.
	got, err = db.GetStateAt(addrActive, 120)
	if err != nil {
		t.Fatalf("GetStateAt(active, 120): %v", err)
	}
	if string(got) != "active-at-50" {
		t.Fatalf("GetStateAt(active, 120) = %q, want %q", got, "active-at-50")
	}

	got, err = db.GetStateAt(addrActive, 150)
	if err != nil {
		t.Fatalf("GetStateAt(active, 150): %v", err)
	}
	if string(got) != "active-at-150" {
		t.Fatalf("GetStateAt(active, 150) = %q, want %q", got, "active-at-150")
	}
}

// TestPruneBeforeBlock_MultipleStaleEntriesCollapseToOne verifies that older
// superseded entries below the floor are actually reclaimed, not just left alone.
func TestPruneBeforeBlock_MultipleStaleEntriesCollapseToOne(t *testing.T) {
	db := newTestDB(t)
	addr := []byte("addr")

	for _, b := range []uint64{10, 20, 30, 40} {
		if err := db.WriteBlockChanges(b, []StateChange{
			{Key: addr, OldValue: []byte("prev"), NewValue: []byte("v")},
		}); err != nil {
			t.Fatalf("WriteBlockChanges(%d): %v", b, err)
		}
	}

	if err := db.PruneBeforeBlock(100); err != nil {
		t.Fatalf("PruneBeforeBlock: %v", err)
	}

	// Only the floor entry (block 40) and genesis (block 0) should remain.
	changes, err := db.GetAllUniqueAddresses()
	if err != nil {
		t.Fatalf("GetAllUniqueAddresses: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 unique address, got %d", len(changes))
	}

	got, err := db.GetStateAt(addr, 200)
	if err != nil {
		t.Fatalf("GetStateAt(addr, 200): %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("GetStateAt(addr, 200) = %q, want %q", got, "v")
	}
}
