package trie

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
)

func openTestNomtHandle(t *testing.T) *nomt_ffi.Handle {
	t.Helper()
	h, err := nomt_ffi.Open(filepath.Join(t.TempDir(), "nomt"), 1, 16, 16, 64000, false)
	if err != nil {
		t.Fatalf("nomt_ffi.Open: %v", err)
	}
	t.Cleanup(h.Close)
	return h
}

func slotKey(b byte) []byte {
	k := make([]byte, 32)
	k[31] = b
	return k
}

// contractWrite is one contract's storage writes for a block.
type contractWrite struct {
	addr  [20]byte
	slots map[byte]byte // slot -> value (single byte, non-zero)
}

// commitBlockBatched mimics SmartContractDB.LateBindRoots for a block: every dirty contract goes into ONE
// session (CommitBatchRaw), the changelog and the block's root are recorded, and the session is persisted.
// It returns the shared-tree root after the block and the tries so tests can inspect them.
func commitBlockBatched(t *testing.T, h *nomt_ffi.Handle, cl *state_changelog.StateChangelogDB, block uint64, writes []contractWrite) (e_common.Hash, []*NomtStateTrie) {
	t.Helper()
	var tries []*NomtStateTrie
	var states []NomtDirtyState
	for _, w := range writes {
		tr := NewNomtStateTrie(h, true, "smart_contract_storage_"+hex.EncodeToString(w.addr[:]))
		for slot, val := range w.slots {
			if err := tr.Update(slotKey(slot), []byte{val}); err != nil {
				t.Fatalf("Update: %v", err)
			}
		}
		st := tr.ExportDirty()
		st.Prefix = w.addr[:]
		states = append(states, st)
		tries = append(tries, tr)
	}
	root, fs, _, err := CommitBatchRaw(h, states, cl, block)
	if err != nil {
		t.Fatalf("CommitBatchRaw(block %d): %v", block, err)
	}
	for _, tr := range tries {
		tr.ClearDirty(root)
	}
	// Persist like commitWorker does once the block is committed.
	if err := fs.CommitPayload(h); err != nil {
		t.Fatalf("CommitPayload(block %d): %v", block, err)
	}
	return root, tries
}

func newContractChangelog(t *testing.T) *state_changelog.StateChangelogDB {
	t.Helper()
	cl, err := state_changelog.NewStateChangelogDB(filepath.Join(t.TempDir(), "changelog_sc"), SharedContractStorageNamespace)
	if err != nil {
		t.Fatalf("NewStateChangelogDB: %v", err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

// SyncOnly nodes receive contract storage ONLY through the replication batch that CommitAllStorage reads with
// GetCommitBatch. The batched commit path must therefore leave the same "nomt:"-prefixed batch that Commit()
// does; it once did not, and a SyncOnly node silently kept an empty contract storage.
func TestCommitBatchRaw_LeavesReplicationBatchForEveryContract(t *testing.T) {
	h := openTestNomtHandle(t)
	a := contractWrite{addr: [20]byte{0xA1}, slots: map[byte]byte{1: 0x11, 2: 0x22}}
	b := contractWrite{addr: [20]byte{0xB2}, slots: map[byte]byte{1: 0x33}}
	_, tries := commitBlockBatched(t, h, nil, 1, []contractWrite{a, b})

	want := []map[string]string{
		{"nomt:" + string(slotKey(1)): "\x11", "nomt:" + string(slotKey(2)): "\x22"},
		{"nomt:" + string(slotKey(1)): "\x33"},
	}
	for i, tr := range tries {
		// The writes were handed to a session, so there is nothing left to batch, even though their data stays
		// visible until the session is persisted. CommitAllStorage relies on this to keep using the original
		// trie (whose replication batch and pending session it needs) instead of committing a copy.
		if tr.HasUnbatchedChanges() {
			t.Errorf("contract %d: HasUnbatchedChanges must be false once the writes were batched", i)
		}
		batch := tr.GetCommitBatch()
		if len(batch) != len(want[i]) {
			t.Fatalf("contract %d: replication batch has %d entries, want %d", i, len(batch), len(want[i]))
		}
		for _, kv := range batch {
			if w, ok := want[i][string(kv[0])]; !ok || w != string(kv[1]) {
				t.Errorf("contract %d: unexpected replication entry %q=%q", i, kv[0], kv[1])
			}
		}
		if again := tr.GetCommitBatch(); again != nil {
			t.Errorf("contract %d: GetCommitBatch must be one-shot, second call returned %d entries", i, len(again))
		}
	}
}

// After a crash between "the block executed (storage written)" and "the block became durable", the shared
// contract storage must roll back to exactly the state of the last canonical block, and that must be PROVEN
// against the root recorded for that block; an expected root the rollback does not reproduce must be rejected.
func TestSharedContractStorage_RollbackRestoresRecordedRoot(t *testing.T) {
	h := openTestNomtHandle(t)
	cl := newContractChangelog(t)

	root1, _ := commitBlockBatched(t, h, cl, 1, []contractWrite{
		{addr: [20]byte{0xA1}, slots: map[byte]byte{1: 0x11}},
	})
	// Block 2 dirties TWO contracts, changes an existing slot and creates new ones.
	root2, _ := commitBlockBatched(t, h, cl, 2, []contractWrite{
		{addr: [20]byte{0xA1}, slots: map[byte]byte{1: 0x99, 2: 0x22}},
		{addr: [20]byte{0xB2}, slots: map[byte]byte{7: 0x77}},
	})
	if root1 == root2 {
		t.Fatal("test setup: blocks 1 and 2 must produce different roots")
	}
	if cur, _ := h.Root(); e_common.BytesToHash(cur[:]) != root2 {
		t.Fatalf("handle root before crash = %x, want block 2 root %x", cur, root2)
	}

	// The changelog records each block's root, newest at-or-before lookup.
	if got, blk, found, err := cl.GetRootAtOrBefore(1); err != nil || !found || blk != 1 || e_common.BytesToHash(got) != root1 {
		t.Fatalf("recorded root for block 1 = (%x, %d, %v, %v), want %x", got, blk, found, err, root1)
	}
	if got, blk, found, err := cl.GetRootAtOrBefore(2); err != nil || !found || blk != 2 || e_common.BytesToHash(got) != root2 {
		t.Fatalf("recorded root for block 2 = (%x, %d, %v, %v), want %x", got, blk, found, err, root2)
	}

	// "Crash": the canonical tip is block 1 but storage holds block 2's writes. A WRONG expected root is rejected.
	rollback := NewNomtStateTrie(h, true, SharedContractStorageNamespace)
	rollback.SetChangelogDB(cl)
	bogus := e_common.Hash{0xEE}
	if err := rollback.AlignWithExpectedRoot(nil, bogus, 1); err == nil {
		t.Fatal("a rollback whose result does not match the expected root must fail verification")
	}

	// Redo the block-2 writes (the failed attempt above already rolled storage back to block 1) and roll back for real.
	root2b, _ := commitBlockBatched(t, h, cl, 2, []contractWrite{
		{addr: [20]byte{0xA1}, slots: map[byte]byte{1: 0x99, 2: 0x22}},
		{addr: [20]byte{0xB2}, slots: map[byte]byte{7: 0x77}},
	})
	if root2b != root2 {
		t.Fatalf("replaying block 2 produced root %x, want the original %x (execution must be deterministic)", root2b, root2)
	}
	rollback = NewNomtStateTrie(h, true, SharedContractStorageNamespace)
	rollback.SetChangelogDB(cl)
	if err := rollback.AlignWithExpectedRoot(nil, root1, 1); err != nil {
		t.Fatalf("rollback to block 1 with the recorded root must succeed: %v", err)
	}
	if cur, _ := h.Root(); e_common.BytesToHash(cur[:]) != root1 {
		t.Fatalf("handle root after rollback = %x, want block 1 root %x", cur, root1)
	}
}

func TestHasUnbatchedChanges_TracksLiveDirtyWrites(t *testing.T) {
	h := openTestNomtHandle(t)
	tr := NewNomtStateTrie(h, true, "smart_contract_storage_"+hex.EncodeToString([]byte{0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3}))
	if tr.HasUnbatchedChanges() {
		t.Fatal("a fresh trie has no unbatched changes")
	}
	if err := tr.Update(slotKey(1), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if !tr.HasUnbatchedChanges() {
		t.Fatal("a written slot must count as an unbatched change")
	}
}
