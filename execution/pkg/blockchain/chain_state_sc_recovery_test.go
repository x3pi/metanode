package blockchain

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
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

// When contract storage is ahead of the canonical block and recorded roots exist,
// alignSmartContractStorage must roll back contract storage to the canonical block and verify the root.
func TestAlignSmartContractStorage_RollbackSuccess(t *testing.T) {
	tempDir := t.TempDir()
	_ = trie.InitNomtDB(tempDir, 1, 16, 16)
	t.Cleanup(trie.CloseNomtDB)

	handle, err := trie.GetOrInitNomtHandle(trie.SharedContractStorageNamespace)
	if err != nil {
		t.Fatalf("GetOrInitNomtHandle: %v", err)
	}

	cl := newScChangelog(t)

	slotKey := func(b byte) []byte {
		k := make([]byte, 32)
		k[31] = b
		return k
	}

	addr1 := [20]byte{0x01}
	tr1 := trie.NewNomtStateTrie(handle, true, "smart_contract_storage_"+hex.EncodeToString(addr1[:]))
	if err := tr1.Update(slotKey(1), []byte{10}); err != nil {
		t.Fatal(err)
	}
	st1 := tr1.ExportDirty()
	st1.Prefix = addr1[:]
	root1, fs1, _, err := trie.CommitBatchRaw(handle, []trie.NomtDirtyState{st1}, cl, 1)
	if err != nil {
		t.Fatal(err)
	}
	tr1.ClearDirty(root1)
	if err := fs1.CommitPayload(handle); err != nil {
		t.Fatal(err)
	}

	// Block 2 writes (dirty forward state)
	addr2 := [20]byte{0x02}
	tr2 := trie.NewNomtStateTrie(handle, true, "smart_contract_storage_"+hex.EncodeToString(addr2[:]))
	if err := tr2.Update(slotKey(2), []byte{20}); err != nil {
		t.Fatal(err)
	}
	st2 := tr2.ExportDirty()
	st2.Prefix = addr2[:]
	root2, fs2, _, err := trie.CommitBatchRaw(handle, []trie.NomtDirtyState{st2}, cl, 2)
	if err != nil {
		t.Fatal(err)
	}
	tr2.ClearDirty(root2)
	if err := fs2.CommitPayload(handle); err != nil {
		t.Fatal(err)
	}

	// Confirm highest block is 2
	if cl.GetHighestBlock() != 2 {
		t.Fatalf("expected highestBlock=2, got %d", cl.GetHighestBlock())
	}

	// Simulate crash: canonical block is 1, but smart_contract_storage has block 2
	if err := alignSmartContractStorage(cl, 1); err != nil {
		t.Fatalf("alignSmartContractStorage(cl, 1) failed: %v", err)
	}

	// Verify root after rollback is back to root1
	curRoot, err := handle.Root()
	if err != nil {
		t.Fatal(err)
	}
	if curRoot != root1 {
		t.Fatalf("handle root after rollback = %x, want root1 %x", curRoot, root1)
	}
}
