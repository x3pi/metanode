package processor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMapSnapshotLayout_PlacesEveryDirectory(t *testing.T) {
	snap, root := t.TempDir(), filepath.Join(t.TempDir(), "root")
	for _, p := range []string{
		"account_state/MANIFEST", "stake_db/MANIFEST", "nomt_db/account_state/ht", "xapian/db/x",
		"history/blocks/CURRENT", "history/changelog_db_sc/CURRENT", "history/blob_store/CURRENT",
		"back_up/consensus/backup_db/CURRENT", // not part of the root
	} {
		touch(t, filepath.Join(snap, p))
	}
	if err := mapSnapshotLayout(snap, root); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"consensus/account_state/MANIFEST", "consensus/stake_db/MANIFEST", "consensus/nomt_db/account_state/ht",
		"consensus/xapian/db/x", "history/blocks/CURRENT", "history/changelog_db_sc/CURRENT", "history/blob_store/CURRENT",
	} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Fatalf("%s missing after mapping: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "back_up")); err == nil {
		t.Fatal("back_up must not be part of the database root")
	}
}

func TestMapSnapshotLayout_RefusesUnknownEntries(t *testing.T) {
	snap := t.TempDir()
	touch(t, filepath.Join(snap, "chaindata", "CURRENT")) // the unified shared-DB layout
	if err := mapSnapshotLayout(snap, filepath.Join(t.TempDir(), "root")); err == nil || !strings.Contains(err.Error(), "chaindata") {
		t.Fatalf("an unknown snapshot entry was silently accepted: %v", err)
	}
}

func TestVerifyCoverage_FlagsMissingDirectories(t *testing.T) {
	live, snap := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(live, "consensus/account_state/a"))
	touch(t, filepath.Join(live, "history/blocks/a"))
	touch(t, filepath.Join(live, "history/changelog_db_sc/a"))
	touch(t, filepath.Join(snap, "consensus/account_state/a"))
	touch(t, filepath.Join(snap, "history/blocks/a"))
	err := verifyCoverage(live, snap)
	if err == nil || !strings.Contains(err.Error(), "history/changelog_db_sc") {
		t.Fatalf("a snapshot missing a live database directory was accepted: %v", err)
	}
	touch(t, filepath.Join(snap, "history/changelog_db_sc/a"))
	if err := verifyCoverage(live, snap); err != nil {
		t.Fatal(err)
	}
}
