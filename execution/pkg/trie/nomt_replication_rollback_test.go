package trie

import (
	"bytes"
	"encoding/hex"
	"path/filepath"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

func setupIsolatedNomtBackend(t *testing.T) string {
	t.Helper()
	prevBackend := GetStateBackend()
	prevConfig := globalNomtConfig

	CloseNomtDB()

	nomtDir := filepath.Join(t.TempDir(), "nomt_db")
	if err := InitNomtDB(nomtDir, 1, 16, 16); err != nil {
		t.Fatalf("InitNomtDB: %v", err)
	}
	SetStateBackend(BackendNOMT)
	storage.ForceSetLastNomtCommittedBlock(0)

	t.Cleanup(func() {
		CloseNomtDB()
		globalNomtHandlesMu.Lock()
		globalNomtConfig = prevConfig
		globalNomtHandlesMu.Unlock()
		SetStateBackend(prevBackend)
		storage.ForceSetLastNomtCommittedBlock(0)
	})
	return nomtDir
}

func makeNomtKey(raw []byte) []byte {
	out := make([]byte, 5+len(raw))
	copy(out[:5], "nomt:")
	copy(out[5:], raw)
	return out
}

func makeNomtSCKey(addr [20]byte, slot byte) []byte {
	s := slotKey(slot)
	out := make([]byte, 20+5+32)
	copy(out[:20], addr[:])
	copy(out[20:25], "nomt:")
	copy(out[25:], s)
	return out
}

// TestApplyNomtReplicationBatchesWithRollback_RestoresAllNamespacesAndAllowsDeterministicContinuation
// verifies that rolling back a failed block's NOMT replication batch restores:
//  1. Exact C++ NOMT Merkle roots across all 5 namespaces (account_state, stake_db, smart_contract_storage, receipts, transaction_state)
//  2. Overwritten, deleted, and newly-created keys (including cleaning newly-created keys out of the file-backed registry)
//  3. StateChangelogDB entries, highestBlock, and recorded SC roots
//  4. LastNomtCommittedBlock counter
//  5. 100% deterministic Merkle root parity with a clean reference node when canonical Block #2 is applied afterwards.
func TestApplyNomtReplicationBatchesWithRollback_RestoresAllNamespacesAndAllowsDeterministicContinuation(t *testing.T) {
	setupIsolatedNomtBackend(t)

	clDir := t.TempDir()
	accCl, err := state_changelog.NewStateChangelogDB(filepath.Join(clDir, "acc"), "account_state")
	if err != nil {
		t.Fatal(err)
	}
	defer accCl.Close()
	stakeCl, err := state_changelog.NewStateChangelogDB(filepath.Join(clDir, "stake"), "stake_db")
	if err != nil {
		t.Fatal(err)
	}
	defer stakeCl.Close()
	scCl, err := state_changelog.NewStateChangelogDB(filepath.Join(clDir, "sc"), SharedContractStorageNamespace)
	if err != nil {
		t.Fatal(err)
	}
	defer scCl.Close()

	acct1 := bytes.Repeat([]byte{0x11}, 20)
	acct2 := bytes.Repeat([]byte{0x22}, 20)
	acct3 := bytes.Repeat([]byte{0x33}, 20)
	stake1 := bytes.Repeat([]byte{0x44}, 20)
	stake2 := bytes.Repeat([]byte{0x55}, 20)
	scA := [20]byte{0xA1, 0x01}
	scB := [20]byte{0xB2, 0x02}
	scC := [20]byte{0xC3, 0x03}
	rcpt1 := bytes.Repeat([]byte{0x71}, 32)
	rcpt2 := bytes.Repeat([]byte{0x72}, 32)
	tx1 := bytes.Repeat([]byte{0x81}, 32)
	tx2 := bytes.Repeat([]byte{0x82}, 32)

	buildBlock1Batches := func() map[string][][2][]byte {
		return map[string][][2][]byte{
			"Account": {
				{makeNomtKey(acct1), []byte("acct1_b1")},
				{makeNomtKey(acct2), []byte("acct2_b1")},
				{[]byte("plain_kv_keep"), []byte("kv1")},
			},
			"StakeState": {
				{makeNomtKey(stake1), []byte("stake1_b1")},
			},
			"SC Storage": {
				{makeNomtSCKey(scA, 1), []byte{0x10}},
				{makeNomtSCKey(scA, 2), []byte{0x20}},
				{makeNomtSCKey(scB, 5), []byte{0x50}},
			},
			"Receipt": {
				{makeNomtKey(rcpt1), []byte("rcpt_b1")},
			},
			"Transaction": {
				{makeNomtKey(tx1), []byte("tx_b1")},
			},
		}
	}

	b1 := buildBlock1Batches()
	_, _, err = ApplyNomtReplicationBatchesWithRollback(b1, accCl, stakeCl, scCl, 1)
	if err != nil {
		t.Fatalf("Block 1 ApplyNomtReplicationBatchesWithRollback: %v", err)
	}
	if len(b1["Account"]) != 1 || string(b1["Account"][0][0]) != "plain_kv_keep" {
		t.Fatalf("expected non-nomt key to remain in Account batch, got %v", b1["Account"])
	}
	if got := storage.GetLastNomtCommittedBlock(); got != 1 {
		t.Fatalf("LastNomtCommittedBlock after B1 = %d, want 1", got)
	}

	accRoot1, ok := GetNomtHandleRoot("account_state")
	if !ok || accRoot1 == (e_common.Hash{}) {
		t.Fatalf("invalid accRoot1: %x (ok=%v)", accRoot1, ok)
	}
	stakeRoot1, _ := GetNomtHandleRoot("stake_db")
	scRoot1, _ := GetNomtHandleRoot("smart_contract_storage")
	rcptRoot1, _ := GetNomtHandleRoot("receipts")
	txRoot1, _ := GetNomtHandleRoot("transaction_state")

	// Apply corrupt/aborted Block 2 that mutates existing keys, deletes acct2, and creates brand-new keys across all 5 namespaces
	b2Bad := map[string][][2][]byte{
		"Account": {
			{makeNomtKey(acct1), []byte("acct1_bad_b2")},
			{makeNomtKey(acct2), nil}, // delete existing account
			{makeNomtKey(acct3), []byte("acct3_new_bad_b2")},
		},
		"StakeState": {
			{makeNomtKey(stake1), []byte("stake1_bad_b2")},
			{makeNomtKey(stake2), []byte("stake2_new_bad_b2")},
		},
		"SC Storage": {
			{makeNomtSCKey(scA, 1), []byte{0x99}},
			{makeNomtSCKey(scA, 9), []byte{0x90}},
			{makeNomtSCKey(scC, 7), []byte{0x77}},
		},
		"Receipt": {
			{makeNomtKey(rcpt2), []byte("rcpt_bad_b2")},
		},
		"Transaction": {
			{makeNomtKey(tx2), []byte("tx_bad_b2")},
		},
	}

	_, rollbackB2, err := ApplyNomtReplicationBatchesWithRollback(b2Bad, accCl, stakeCl, scCl, 2)
	if err != nil {
		t.Fatalf("Block 2 (bad) ApplyNomtReplicationBatchesWithRollback: %v", err)
	}

	// Confirm state actually mutated before rollback
	accRootBad, _ := GetNomtHandleRoot("account_state")
	scRootBad, _ := GetNomtHandleRoot("smart_contract_storage")
	if accRootBad == accRoot1 || scRootBad == scRoot1 {
		t.Fatalf("expected Block 2 mutations to change NOMT roots before rollback")
	}
	if storage.GetLastNomtCommittedBlock() != 2 || accCl.GetHighestBlock() != 2 || scCl.GetHighestBlock() != 2 {
		t.Fatalf("expected B2 counters/changelogs at 2 before rollback")
	}

	// Execute rollback!
	if err := rollbackB2(); err != nil {
		t.Fatalf("rollbackB2 failed: %v", err)
	}

	// 1. All 5 C++ NOMT roots must match Block 1 byte-for-byte
	for _, check := range []struct {
		ns   string
		want e_common.Hash
	}{
		{"account_state", accRoot1},
		{"stake_db", stakeRoot1},
		{"smart_contract_storage", scRoot1},
		{"receipts", rcptRoot1},
		{"transaction_state", txRoot1},
	} {
		got, ok := GetNomtHandleRoot(check.ns)
		if !ok || got != check.want {
			t.Fatalf("namespace %s root after rollback = %x (ok=%v), want %x", check.ns, got, ok, check.want)
		}
	}

	// 2. Point reads & registries must reflect exact Block 1 state (acct2 restored, acct3 & scC gone)
	accHandle, _ := GetOrInitNomtHandle("account_state")
	accTrie := NewNomtStateTrie(accHandle, false, "account_state")
	if v, err := accTrie.Get(acct1); err != nil || string(v) != "acct1_b1" {
		t.Fatalf("acct1 after rollback = (%q, %v), want \"acct1_b1\"", v, err)
	}
	if v, err := accTrie.Get(acct2); err != nil || string(v) != "acct2_b1" {
		t.Fatalf("acct2 (deleted in bad B2) after rollback = (%q, %v), want \"acct2_b1\"", v, err)
	}
	if v, err := accTrie.Get(acct3); err != nil || len(v) != 0 {
		t.Fatalf("acct3 (created in bad B2) after rollback = (%q, %v), want empty", v, err)
	}
	accTrie.registry.mu.RLock()
	_, acct3StillInRegistry := accTrie.registry.keys[hex.EncodeToString(acct3)]
	accTrie.registry.mu.RUnlock()
	if acct3StillInRegistry {
		t.Fatalf("newly-created acct3 must be purged from account_state registry on rollback")
	}

	scHandle, _ := GetOrInitNomtHandle("smart_contract_storage")
	scTrieA := NewNomtStateTrie(scHandle, false, "smart_contract_storage_"+hex.EncodeToString(scA[:]))
	if v, err := scTrieA.Get(slotKey(1)); err != nil || !bytes.Equal(v, []byte{0x10}) {
		t.Fatalf("scA slot 1 after rollback = %x (%v), want 0x10", v, err)
	}
	if v, err := scTrieA.Get(slotKey(9)); err != nil || len(v) != 0 {
		t.Fatalf("scA slot 9 after rollback = %x (%v), want empty", v, err)
	}
	scTrieC := NewNomtStateTrie(scHandle, false, "smart_contract_storage_"+hex.EncodeToString(scC[:]))
	if v, err := scTrieC.Get(slotKey(7)); err != nil || len(v) != 0 {
		t.Fatalf("scC slot 7 after rollback = %x (%v), want empty", v, err)
	}

	// 3. Changelogs & LastNomtCommittedBlock must be back at Block 1
	if got := storage.GetLastNomtCommittedBlock(); got != 1 {
		t.Fatalf("LastNomtCommittedBlock after rollback = %d, want 1", got)
	}
	for name, cl := range map[string]*state_changelog.StateChangelogDB{
		"account": accCl,
		"stake":   stakeCl,
		"sc":      scCl,
	} {
		if got := cl.GetHighestBlock(); got != 1 {
			t.Fatalf("%s changelog highestBlock after rollback = %d, want 1", name, got)
		}
	}
	if rootBytes, blk, found, err := scCl.GetRootAtOrBefore(10); err != nil || !found || blk != 1 || e_common.BytesToHash(rootBytes) != scRoot1 {
		t.Fatalf("scCl recorded root after rollback = (%x, %d, %v, %v), want (%x, 1, true, nil)", rootBytes, blk, found, err, scRoot1)
	}

	// 4. Canonical Block 2 applied after rollback must produce 100% identical Merkle roots to a clean reference node!
	buildCanonBlock2Batches := func() map[string][][2][]byte {
		return map[string][][2][]byte{
			"Account": {
				{makeNomtKey(acct1), []byte("acct1_canon_b2")},
				{makeNomtKey(acct3), []byte("acct3_canon_b2")},
			},
			"StakeState": {
				{makeNomtKey(stake1), []byte("stake1_canon_b2")},
			},
			"SC Storage": {
				{makeNomtSCKey(scA, 1), []byte{0x42}},
				{makeNomtSCKey(scB, 6), []byte{0x66}},
			},
			"Receipt": {
				{makeNomtKey(rcpt2), []byte("rcpt_canon_b2")},
			},
			"Transaction": {
				{makeNomtKey(tx2), []byte("tx_canon_b2")},
			},
		}
	}

	if _, _, err := ApplyNomtReplicationBatchesWithRollback(buildCanonBlock2Batches(), accCl, stakeCl, scCl, 2); err != nil {
		t.Fatalf("Canonical Block 2 after rollback failed: %v", err)
	}
	rolledBackAccRoot2, _ := GetNomtHandleRoot("account_state")
	rolledBackStakeRoot2, _ := GetNomtHandleRoot("stake_db")
	rolledBackScRoot2, _ := GetNomtHandleRoot("smart_contract_storage")

	// Switch to a completely fresh NOMT directory and apply ONLY [Block 1, Canonical Block 2]
	CloseNomtDB()
	if err := InitNomtDB(filepath.Join(t.TempDir(), "nomt_clean_ref"), 1, 16, 16); err != nil {
		t.Fatal(err)
	}
	storage.ForceSetLastNomtCommittedBlock(0)

	if _, _, err := ApplyNomtReplicationBatchesWithRollback(buildBlock1Batches(), nil, nil, nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyNomtReplicationBatchesWithRollback(buildCanonBlock2Batches(), nil, nil, nil, 2); err != nil {
		t.Fatal(err)
	}
	cleanAccRoot2, _ := GetNomtHandleRoot("account_state")
	cleanStakeRoot2, _ := GetNomtHandleRoot("stake_db")
	cleanScRoot2, _ := GetNomtHandleRoot("smart_contract_storage")

	if rolledBackAccRoot2 != cleanAccRoot2 {
		t.Fatalf("account_state root diverged vs clean node: rolledBack=%x clean=%x", rolledBackAccRoot2, cleanAccRoot2)
	}
	if rolledBackStakeRoot2 != cleanStakeRoot2 {
		t.Fatalf("stake_db root diverged vs clean node: rolledBack=%x clean=%x", rolledBackStakeRoot2, cleanStakeRoot2)
	}
	if rolledBackScRoot2 != cleanScRoot2 {
		t.Fatalf("smart_contract_storage root diverged vs clean node: rolledBack=%x clean=%x", rolledBackScRoot2, cleanScRoot2)
	}
}

// TestSharedContractStorage_FirstContractBlockRollbackToEmptyBaseline verifies that when blocks #1..#3
// have NO contract storage writes and block #4 is the VERY FIRST block to write contract storage
// (via either CommitBatchRaw or ApplyNomtReplicationBatchesWithRollback), rolling back to block #3
// via AlignWithExpectedRoot succeeds and verifies against the baseline empty root [32]byte{}.
func TestSharedContractStorage_FirstContractBlockRollbackToEmptyBaseline(t *testing.T) {
	t.Run("CommitBatchRaw", func(t *testing.T) {
		h := openTestNomtHandle(t)
		cl := newContractChangelog(t)

		// Block 4 is the very first block in history to touch contract storage
		root4, _ := commitBlockBatched(t, h, cl, 4, []contractWrite{
			{addr: [20]byte{0xD4}, slots: map[byte]byte{1: 0xAA, 2: 0xBB}},
		})
		if root4 == (e_common.Hash{}) {
			t.Fatal("expected non-zero root at block 4")
		}

		// Baseline empty root must have been recorded at block 0 so GetRootAtOrBefore(3) succeeds
		baseRoot, blk, found, err := cl.GetRootAtOrBefore(3)
		if err != nil || !found || blk != 0 || e_common.BytesToHash(baseRoot) != (e_common.Hash{}) {
			t.Fatalf("GetRootAtOrBefore(3) = (%x, %d, %v, %v), want ([0..0], 0, true, nil)", baseRoot, blk, found, err)
		}

		rbTrie := NewNomtStateTrie(h, true, SharedContractStorageNamespace)
		rbTrie.SetChangelogDB(cl)
		if err := rbTrie.AlignWithExpectedRoot(nil, e_common.BytesToHash(baseRoot), 3); err != nil {
			t.Fatalf("AlignWithExpectedRoot to block 3 failed: %v", err)
		}
		curRoot, err := h.Root()
		if err != nil || e_common.BytesToHash(curRoot[:]) != (e_common.Hash{}) {
			t.Fatalf("handle root after rollback to empty block 3 = %x (%v), want 0x00..00", curRoot, err)
		}
	})

	t.Run("ApplyNomtReplicationBatchesWithRollback", func(t *testing.T) {
		setupIsolatedNomtBackend(t)
		cl := newContractChangelog(t)

		addr := [20]byte{0xE5}
		batches := map[string][][2][]byte{
			"SC Storage": {
				{makeNomtSCKey(addr, 3), []byte{0xCC}},
			},
		}
		if _, _, err := ApplyNomtReplicationBatchesWithRollback(batches, nil, nil, cl, 4); err != nil {
			t.Fatalf("ApplyNomtReplicationBatchesWithRollback(block 4): %v", err)
		}

		baseRoot, blk, found, err := cl.GetRootAtOrBefore(3)
		if err != nil || !found || blk != 0 || e_common.BytesToHash(baseRoot) != (e_common.Hash{}) {
			t.Fatalf("GetRootAtOrBefore(3) = (%x, %d, %v, %v), want ([0..0], 0, true, nil)", baseRoot, blk, found, err)
		}

		h, err := GetOrInitNomtHandle(SharedContractStorageNamespace)
		if err != nil {
			t.Fatal(err)
		}
		rbTrie := NewNomtStateTrie(h, true, SharedContractStorageNamespace)
		rbTrie.SetChangelogDB(cl)
		if err := rbTrie.AlignWithExpectedRoot(nil, e_common.BytesToHash(baseRoot), 3); err != nil {
			t.Fatalf("AlignWithExpectedRoot to block 3 failed: %v", err)
		}
		curRoot, err := h.Root()
		if err != nil || e_common.BytesToHash(curRoot[:]) != (e_common.Hash{}) {
			t.Fatalf("handle root after rollback to empty block 3 = %x (%v), want 0x00..00", curRoot, err)
		}
	})
}

// TestSharedContractStorage_NonEmptySnapshotBaselineRecordedBeforeFirstChangelogWrite verifies that
// when a node boots from a non-empty snapshot (empty SC changelog) and later commits block #5 as the
// first block recorded in scChangelogDB, the pre-block non-empty snapshot root is recorded at block #4
// so AlignWithExpectedRoot back to block #4 succeeds and preserves the non-empty snapshot root.
func TestSharedContractStorage_NonEmptySnapshotBaselineRecordedBeforeFirstChangelogWrite(t *testing.T) {
	t.Run("CommitBatchRaw", func(t *testing.T) {
		h := openTestNomtHandle(t)

		// Snapshot baseline written at block 2 without changelogDB.
		snapRoot, _ := commitBlockBatched(t, h, nil, 2, []contractWrite{
			{addr: [20]byte{0xA1}, slots: map[byte]byte{1: 0x11}},
		})
		if snapRoot == (e_common.Hash{}) {
			t.Fatal("expected non-zero snapshot root")
		}

		// Boot with a fresh empty changelogDB, then write block 5.
		cl := newContractChangelog(t)
		root5, _ := commitBlockBatched(t, h, cl, 5, []contractWrite{
			{addr: [20]byte{0xB2}, slots: map[byte]byte{2: 0x22}},
		})
		if root5 == snapRoot {
			t.Fatal("expected block 5 root to differ from snapshot root")
		}

		baseRoot, blk, found, err := cl.GetRootAtOrBefore(4)
		if err != nil || !found || blk != 4 || e_common.BytesToHash(baseRoot) != snapRoot {
			t.Fatalf("GetRootAtOrBefore(4) = (%x, %d, %v, %v), want (%x, 4, true, nil)",
				baseRoot, blk, found, err, snapRoot[:])
		}

		rbTrie := NewNomtStateTrie(h, true, SharedContractStorageNamespace)
		rbTrie.SetChangelogDB(cl)
		if err := rbTrie.AlignWithExpectedRoot(nil, snapRoot, 4); err != nil {
			t.Fatalf("AlignWithExpectedRoot to snapshot block 4 failed: %v", err)
		}
		curRoot, err := h.Root()
		if err != nil || e_common.BytesToHash(curRoot[:]) != snapRoot {
			t.Fatalf("handle root after rollback = %x (%v), want %x", curRoot, err, snapRoot[:])
		}
	})

	t.Run("ApplyNomtReplicationBatchesWithRollback", func(t *testing.T) {
		setupIsolatedNomtBackend(t)

		addrA1 := [20]byte{0xA1}
		snapBatches := map[string][][2][]byte{
			"SC Storage": {{makeNomtSCKey(addrA1, 1), []byte{0x11}}},
		}
		if _, _, err := ApplyNomtReplicationBatchesWithRollback(snapBatches, nil, nil, nil, 2); err != nil {
			t.Fatalf("ApplyNomtReplicationBatchesWithRollback(snapshot block 2): %v", err)
		}
		snapRoot, ok := GetNomtHandleRoot(SharedContractStorageNamespace)
		if !ok || snapRoot == (e_common.Hash{}) {
			t.Fatalf("expected non-zero snapshot root, got (%x, %v)", snapRoot, ok)
		}

		cl := newContractChangelog(t)
		addrB2 := [20]byte{0xB2}
		b5Batches := map[string][][2][]byte{
			"SC Storage": {{makeNomtSCKey(addrB2, 2), []byte{0x22}}},
		}
		if _, _, err := ApplyNomtReplicationBatchesWithRollback(b5Batches, nil, nil, cl, 5); err != nil {
			t.Fatalf("ApplyNomtReplicationBatchesWithRollback(block 5): %v", err)
		}

		baseRoot, blk, found, err := cl.GetRootAtOrBefore(4)
		if err != nil || !found || blk != 4 || e_common.BytesToHash(baseRoot) != snapRoot {
			t.Fatalf("GetRootAtOrBefore(4) = (%x, %d, %v, %v), want (%x, 4, true, nil)",
				baseRoot, blk, found, err, snapRoot[:])
		}

		h, err := GetOrInitNomtHandle(SharedContractStorageNamespace)
		if err != nil {
			t.Fatal(err)
		}
		rbTrie := NewNomtStateTrie(h, true, SharedContractStorageNamespace)
		rbTrie.SetChangelogDB(cl)
		if err := rbTrie.AlignWithExpectedRoot(nil, snapRoot, 4); err != nil {
			t.Fatalf("AlignWithExpectedRoot to snapshot block 4 failed: %v", err)
		}
		curRoot, err := h.Root()
		if err != nil || e_common.BytesToHash(curRoot[:]) != snapRoot {
			t.Fatalf("handle root after rollback = %x (%v), want %x", curRoot, err, snapRoot[:])
		}
	})
}
