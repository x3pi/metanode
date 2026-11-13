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

// Roots recorded per block are what crash recovery verifies a rollback against, so the "newest record at or
// before the target block" lookup has to be exact at every boundary.
func TestBlockRoot_NewestAtOrBefore(t *testing.T) {
	db := newTestDB(t)
	for block, root := range map[uint64]byte{3: 0xA3, 5: 0xA5, 9: 0xA9} {
		if err := db.SetBlockRoot(block, []byte{root, 0x01}); err != nil {
			t.Fatalf("SetBlockRoot(%d): %v", block, err)
		}
	}

	cases := []struct {
		target    uint64
		wantFound bool
		wantBlock uint64
		wantRoot  byte
	}{
		{0, false, 0, 0},
		{2, false, 0, 0},
		{3, true, 3, 0xA3},
		{4, true, 3, 0xA3},
		{5, true, 5, 0xA5},
		{8, true, 5, 0xA5},
		{9, true, 9, 0xA9},
		{1_000_000, true, 9, 0xA9},
	}
	for _, c := range cases {
		root, block, found, err := db.GetRootAtOrBefore(c.target)
		if err != nil {
			t.Fatalf("GetRootAtOrBefore(%d): %v", c.target, err)
		}
		if found != c.wantFound || block != c.wantBlock || (found && (len(root) != 2 || root[0] != c.wantRoot)) {
			t.Errorf("GetRootAtOrBefore(%d) = (%x, %d, %v), want block %d root %#x found %v",
				c.target, root, block, found, c.wantBlock, c.wantRoot, c.wantFound)
		}
	}
}

func TestBlockRoot_RejectsEmptyRoot(t *testing.T) {
	db := newTestDB(t)
	if err := db.SetBlockRoot(1, nil); err == nil {
		t.Fatal("an empty root must be rejected, not stored")
	}
}

// Root records live under a meta prefix. They must neither be reported as state keys nor be affected by
// (or affect) the real changelog entries of the same namespace.
func TestBlockRoot_DoesNotMixWithStateKeys(t *testing.T) {
	db := newTestDB(t)
	if err := db.WriteBlockChanges(4, []StateChange{{Key: []byte("k"), OldValue: nil, NewValue: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBlockRoot(4, []byte{0x04}); err != nil {
		t.Fatal(err)
	}
	addrs, err := db.GetAllUniqueAddresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 1 || string(addrs[0]) != "k" {
		t.Fatalf("GetAllUniqueAddresses = %q, want only the real state key", addrs)
	}
	got, err := db.GetStateAt([]byte("k"), 4)
	if err != nil || string(got) != "v" {
		t.Fatalf("GetStateAt = %q, %v, want %q", got, err, "v")
	}
}

// The crash-recovery trigger compares the highest block written to the changelog with the canonical block, so
// it must survive a restart and must never run ahead of what is durable.
func TestHighestBlock_PersistsAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "changelog")
	db, err := NewStateChangelogDB(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []uint64{2, 7, 5} { // out of order: the highest must not go down
		if err := db.WriteBlockChanges(b, []StateChange{{Key: []byte("k"), NewValue: []byte{byte(b)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := db.GetHighestBlock(); got != 7 {
		t.Fatalf("highest before reopen = %d, want 7", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := NewStateChangelogDB(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if got := db2.GetHighestBlock(); got != 7 {
		t.Fatalf("highest after reopen = %d, want 7", got)
	}
}

func TestStateChangelogDB_RollbackAboveBlock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "changelog_rb")
	db, err := NewStateChangelogDB(dir, "account_state")
	if err != nil {
		t.Fatal(err)
	}

	keyA := []byte("acct_alpha_000000001")
	keyB := []byte("acct_bravo_000000002")
	keyC := []byte("acct_charlie_0000003")

	// Block 1: create keyA = "a1"
	if err := db.WriteBlockChanges(1, []StateChange{
		{Key: keyA, OldValue: nil, NewValue: []byte("a1")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBlockRoot(1, []byte{0xB1}); err != nil {
		t.Fatal(err)
	}

	// Block 2: update keyA = "a2", create keyB = "b2"
	if err := db.WriteBlockChanges(2, []StateChange{
		{Key: keyA, OldValue: []byte("a1"), NewValue: []byte("a2")},
		{Key: keyB, OldValue: nil, NewValue: []byte("b2")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBlockRoot(2, []byte{0xB2}); err != nil {
		t.Fatal(err)
	}

	// Block 3: update keyA = "a3", update keyB = "b3", create keyC = "c3"
	if err := db.WriteBlockChanges(3, []StateChange{
		{Key: keyA, OldValue: []byte("a2"), NewValue: []byte("a3")},
		{Key: keyB, OldValue: []byte("b2"), NewValue: []byte("b3")},
		{Key: keyC, OldValue: nil, NewValue: []byte("c3")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBlockRoot(3, []byte{0xB3}); err != nil {
		t.Fatal(err)
	}

	// Roll back above block 2
	if err := db.RollbackAboveBlock(2); err != nil {
		t.Fatalf("RollbackAboveBlock(2): %v", err)
	}
	if got := db.GetHighestBlock(); got != 2 {
		t.Fatalf("highestBlock after RollbackAboveBlock(2) = %d, want 2", got)
	}
	if got, err := db.GetStateAt(keyA, 10); err != nil || string(got) != "a2" {
		t.Fatalf("keyA after rollback to 2 = (%q, %v), want \"a2\"", got, err)
	}
	if got, err := db.GetStateAt(keyB, 10); err != nil || string(got) != "b2" {
		t.Fatalf("keyB after rollback to 2 = (%q, %v), want \"b2\"", got, err)
	}
	if _, err := db.GetStateAt(keyC, 10); err == nil {
		t.Fatal("keyC created at block 3 must not exist after RollbackAboveBlock(2)")
	}
	addrs, err := db.GetAllUniqueAddresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 2 {
		t.Fatalf("unique addresses after rollback to 2 = %d, want 2 (keyC orphan DEL must be cleaned)", len(addrs))
	}
	if root, blk, found, err := db.GetRootAtOrBefore(10); err != nil || !found || blk != 2 || len(root) != 1 || root[0] != 0xB2 {
		t.Fatalf("root after rollback to 2 = (%x, %d, %v, %v), want (b2, 2, true, nil)", root, blk, found, err)
	}

	// Roll back further above block 1: keyB (created at block 2) must still know it did NOT exist at block 1
	// and have its genesis DEL marker purged cleanly.
	if err := db.RollbackAboveBlock(1); err != nil {
		t.Fatalf("RollbackAboveBlock(1): %v", err)
	}
	if got := db.GetHighestBlock(); got != 1 {
		t.Fatalf("highestBlock after RollbackAboveBlock(1) = %d, want 1", got)
	}
	if got, err := db.GetStateAt(keyA, 10); err != nil || string(got) != "a1" {
		t.Fatalf("keyA after rollback to 1 = (%q, %v), want \"a1\"", got, err)
	}
	if _, err := db.GetStateAt(keyB, 10); err == nil {
		t.Fatal("keyB created at block 2 must not exist after RollbackAboveBlock(1)")
	}
	addrs, err = db.GetAllUniqueAddresses()
	if err != nil || len(addrs) != 1 || string(addrs[0]) != string(keyA) {
		t.Fatalf("unique addresses after rollback to 1 = %q (%v), want only keyA", addrs, err)
	}

	// Verify persistence across reopen
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dbReopened, err := NewStateChangelogDB(dir, "account_state")
	if err != nil {
		t.Fatal(err)
	}
	defer dbReopened.Close()
	if got := dbReopened.GetHighestBlock(); got != 1 {
		t.Fatalf("highestBlock after reopen = %d, want 1", got)
	}
	if root, blk, found, err := dbReopened.GetRootAtOrBefore(10); err != nil || !found || blk != 1 || root[0] != 0xB1 {
		t.Fatalf("root after reopen = (%x, %d, %v, %v), want (b1, 1, true, nil)", root, blk, found, err)
	}

	// Roll back above block 0: completely empty state
	if err := dbReopened.RollbackAboveBlock(0); err != nil {
		t.Fatalf("RollbackAboveBlock(0): %v", err)
	}
	if dbReopened.GetHighestBlock() != 0 || dbReopened.GetStartBlock() != 0 {
		t.Fatalf("after RollbackAboveBlock(0): highest=%d start=%d, want 0, 0", dbReopened.GetHighestBlock(), dbReopened.GetStartBlock())
	}
	addrs, err = dbReopened.GetAllUniqueAddresses()
	if err != nil || len(addrs) != 0 {
		t.Fatalf("after RollbackAboveBlock(0): unique addresses = %q (%v), want empty", addrs, err)
	}
}
