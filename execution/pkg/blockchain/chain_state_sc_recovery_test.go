package blockchain

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
)

func newScChangelog(t *testing.T) *state_changelog.StateChangelogDB {
	t.Helper()
	db, err := state_changelog.NewStateChangelogDB(filepath.Join(t.TempDir(), "changelog_sc"), "smart_contract_storage")
	if err != nil {
		t.Fatalf("NewStateChangelogDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func writeScBlock(t *testing.T, db *state_changelog.StateChangelogDB, block uint64) {
	t.Helper()
	if err := db.WriteBlockChanges(block, []state_changelog.StateChange{{Key: []byte("slot"), NewValue: []byte{byte(block)}}}); err != nil {
		t.Fatalf("WriteBlockChanges(%d): %v", block, err)
	}
}

// No rollback is needed unless the changelog shows contract storage was written for a later block.
func TestAlignSmartContractStorage_NoOpWhenNotAhead(t *testing.T) {
	if err := alignSmartContractStorage(nil, 10); err != nil {
		t.Fatalf("nil changelog must be a no-op, got %v", err)
	}
	db := newScChangelog(t)
	if err := alignSmartContractStorage(db, 0); err != nil {
		t.Fatalf("empty changelog must be a no-op, got %v", err)
	}
	writeScBlock(t, db, 5)
	for _, canonical := range []uint64{5, 6, 100} {
		if err := alignSmartContractStorage(db, canonical); err != nil {
			t.Fatalf("canonical block %d >= highest written block 5 must be a no-op, got %v", canonical, err)
		}
	}
}

// If contract storage is ahead of the canonical block and there is no recorded root to verify the rollback
// against, the node must refuse to continue instead of silently running on unverified state.
func TestAlignSmartContractStorage_FailsClosedWithoutRecordedRoot(t *testing.T) {
	db := newScChangelog(t)
	writeScBlock(t, db, 5) // written for block 5, but the canonical block is 3, and no root was ever recorded
	err := alignSmartContractStorage(db, 3)
	if err == nil {
		t.Fatal("expected an error: the rollback cannot be verified without a recorded root")
	}
	if !strings.Contains(err.Error(), "no root recorded") {
		t.Fatalf("unexpected error: %v", err)
	}
}
