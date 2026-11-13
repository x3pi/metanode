package trie

import (
	"bytes"
	"encoding/hex"
	"path/filepath"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
)

func TestNomtStateTrie_PreImageCapture_UpdateDeleteMultiCommitAndDiscard(t *testing.T) {
	h := openTestNomtHandle(t)

	for _, ns := range []string{"account_state", "stake_db"} {
		t.Run(ns, func(t *testing.T) {
			tr := NewNomtStateTrie(h, true, ns)
			kExisting := append([]byte(ns[:3]), slotKey(1)...)
			kNew := append([]byte(ns[:3]), slotKey(2)...)
			kDel := append([]byte(ns[:3]), slotKey(3)...)

			if err := tr.BatchUpdate([][]byte{kExisting, kDel}, [][]byte{[]byte("v1"), []byte("del_orig")}); err != nil {
				t.Fatalf("seed BatchUpdate: %v", err)
			}
			if _, _, _, err := tr.Commit(true); err != nil {
				t.Fatalf("seed Commit: %v", err)
			}
			if err := tr.CommitPayload(); err != nil {
				t.Fatalf("seed CommitPayload: %v", err)
			}

			// Mutate kExisting twice and insert+update kNew inside the same pending block before Commit(true),
			// then call Commit(true) again with empty wDirty (matching IntermediateRoot -> CommitPipeline).
			tr.SetCurrentCommitBlock(2)
			if err := tr.Update(kExisting, []byte("v1_b2")); err != nil {
				t.Fatalf("Update kExisting 1: %v", err)
			}
			if err := tr.Update(kNew, []byte("vNew_b2")); err != nil {
				t.Fatalf("Update kNew 1: %v", err)
			}
			if err := tr.Update(kExisting, []byte("v1_b2_second")); err != nil {
				t.Fatalf("Update kExisting 2: %v", err)
			}
			if err := tr.Update(kNew, []byte("vNew_b2_final")); err != nil {
				t.Fatalf("Update kNew 2: %v", err)
			}
			if _, _, _, err := tr.Commit(true); err != nil {
				t.Fatalf("Commit 1: %v", err)
			}
			if _, _, _, err := tr.Commit(true); err != nil {
				t.Fatalf("Commit 2 (empty wDirty): %v", err)
			}

			payload := tr.ExtractPendingPayload()
			if payload == nil {
				t.Fatalf("expected non-nil NomtPayload")
			}
			pre := payload.PreImageBatch()
			byKey := make(map[string][]byte, len(pre))
			for _, kv := range pre {
				byKey[string(kv[0])] = kv[1]
			}
			if !bytes.Equal(byKey[string(kExisting)], []byte("v1")) {
				t.Fatalf("multi-commit must preserve earliest pre-image v1 for kExisting, got %q", byKey[string(kExisting)])
			}
			if val, exists := byKey[string(kNew)]; !exists || val != nil {
				t.Fatalf("multi-commit must preserve nil pre-image for newly inserted kNew, exists=%v val=%v", exists, val)
			}

			payload.Discard()
			if got := payload.PreImageBatch(); got != nil {
				t.Fatalf("Discard must clear PreImageBatch, got %v", got)
			}
			tr.commitWg.Wait()

			// Re-verify persisted baseline on handle (since payload was discarded), then test delete capture.
			if err := tr.Update(kDel, nil); err != nil {
				t.Fatalf("Update delete kDel: %v", err)
			}
			if _, _, _, err := tr.Commit(true); err != nil {
				t.Fatalf("Commit delete: %v", err)
			}
			delPayload := tr.ExtractPendingPayload()
			if delPayload == nil {
				t.Fatalf("expected non-nil delete payload")
			}
			delPre := delPayload.PreImageBatch()
			if len(delPre) != 1 || !bytes.Equal(delPre[0][0], kDel) || !bytes.Equal(delPre[0][1], []byte("del_orig")) {
				t.Fatalf("delete must record pre-image del_orig, got %v", delPre)
			}
			delPayload.Discard()
			tr.commitWg.Wait()
		})
	}

	t.Run("skipped_when_sc_or_changelog_enabled", func(t *testing.T) {
		scTrie := NewNomtStateTrie(h, true, "smart_contract_storage")
		if err := scTrie.Update(slotKey(10), []byte("sc_val")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := scTrie.Commit(true); err != nil {
			t.Fatal(err)
		}
		scPayload := scTrie.ExtractPendingPayload()
		if scPayload == nil {
			t.Fatalf("expected payload for scTrie")
		}
		if len(scPayload.PreImageBatch()) != 0 {
			t.Fatalf("smart_contract_storage must not capture validator preImages, got %d", len(scPayload.PreImageBatch()))
		}
		scPayload.Discard()
		scTrie.commitWg.Wait()

		cl, err := state_changelog.NewStateChangelogDB(filepath.Join(t.TempDir(), "cl"), "account_state")
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		rpcTrie := NewNomtStateTrie(h, true, "account_state")
		rpcTrie.SetChangelogDB(cl)
		rpcTrie.SetCurrentCommitBlock(1)
		if err := rpcTrie.Update(slotKey(11), []byte("rpc_val")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := rpcTrie.Commit(true); err != nil {
			t.Fatal(err)
		}
		rpcPayload := rpcTrie.ExtractPendingPayload()
		if rpcPayload == nil {
			t.Fatalf("expected payload for rpcTrie")
		}
		if len(rpcPayload.PreImageBatch()) != 0 {
			t.Fatalf("trie with non-nil changelogDB must not populate PreImageBatch, got %d", len(rpcPayload.PreImageBatch()))
		}
		rpcPayload.Discard()
		rpcTrie.commitWg.Wait()
	})
}

func TestNomtStateTrie_AbortPending_ClearsDirtyCommittingAndOldValues(t *testing.T) {
	t.Run("UncommittedDirty", func(t *testing.T) {
		h := openTestNomtHandle(t)
		tr := NewNomtStateTrie(h, true, "account_state")

		k1 := slotKey(1)
		k2 := slotKey(2)
		if err := tr.Update(k1, []byte("v1")); err != nil {
			t.Fatal(err)
		}
		baseRoot, _, _, err := tr.Commit(true)
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.CommitPayload(); err != nil {
			t.Fatal(err)
		}

		if err := tr.Update(k1, []byte("aborted_v1")); err != nil {
			t.Fatal(err)
		}
		if err := tr.Update(k2, []byte("aborted_v2")); err != nil {
			t.Fatal(err)
		}
		tr.AbortPending()

		if tr.HasUnbatchedChanges() {
			t.Fatalf("HasUnbatchedChanges must be false after AbortPending")
		}
		if tr.HasUncommittedChanges() {
			t.Fatalf("HasUncommittedChanges must be false after AbortPending")
		}
		got1, err := tr.Get(k1)
		if err != nil || !bytes.Equal(got1, []byte("v1")) {
			t.Fatalf("Get(k1) = (%q, %v), want v1", got1, err)
		}
		got2, err := tr.Get(k2)
		if err != nil || len(got2) != 0 {
			t.Fatalf("Get(k2) = (%x, %v), want empty", got2, err)
		}
		if tr.Hash() != baseRoot {
			t.Fatalf("Hash() = %s, want baseline %s", tr.Hash().Hex(), baseRoot.Hex())
		}

		// Subsequent canonical update must still capture v1 (not aborted_v1) as pre-image.
		tr.SetCurrentCommitBlock(2)
		if err := tr.Update(k1, []byte("canon_v1")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := tr.Commit(true); err != nil {
			t.Fatal(err)
		}
		p := tr.ExtractPendingPayload()
		if p == nil {
			t.Fatalf("expected non-nil payload on canonical update")
		}
		pre := p.PreImageBatch()
		if len(pre) != 1 || !bytes.Equal(pre[0][0], k1) || !bytes.Equal(pre[0][1], []byte("v1")) {
			t.Fatalf("canonical update after AbortPending must capture oldVal=v1, got %v", pre)
		}
		p.Discard()
		tr.commitWg.Wait()
	})

	t.Run("StagedFinishedSession", func(t *testing.T) {
		h := openTestNomtHandle(t)
		tr := NewNomtStateTrie(h, true, "account_state")

		k1 := slotKey(5)
		if err := tr.Update(k1, []byte("v1")); err != nil {
			t.Fatal(err)
		}
		baseRoot, _, _, err := tr.Commit(true)
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.CommitPayload(); err != nil {
			t.Fatal(err)
		}

		if err := tr.Update(k1, []byte("staged_aborted")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := tr.Commit(true); err != nil {
			t.Fatal(err)
		}
		// Abort before CommitPayload() is called.
		tr.AbortPending()

		if tr.HasUncommittedChanges() {
			t.Fatalf("HasUncommittedChanges must be false after AbortPending on staged session")
		}
		got1, err := tr.Get(k1)
		if err != nil || !bytes.Equal(got1, []byte("v1")) {
			t.Fatalf("Get(k1) after AbortPending = (%q, %v), want v1", got1, err)
		}
		if tr.Hash() != baseRoot {
			t.Fatalf("Hash() after AbortPending = %s, want %s", tr.Hash().Hex(), baseRoot.Hex())
		}
		if err := tr.CommitPayload(); err != nil {
			t.Fatalf("CommitPayload after AbortPending failed: %v", err)
		}
		tr.Close()
	})
}

func TestRollbackNomtNamespaceKeys_RestoresValuesDeletesNewKeysAndRewritesRegistry(t *testing.T) {
	nomtDir := setupIsolatedNomtBackend(t)

	kGen := e_common.HexToAddress("0x1000000000000000000000000000000000000001").Bytes()
	kB1 := e_common.HexToAddress("0x1000000000000000000000000000000000000002").Bytes()
	kForkOnly := e_common.HexToAddress("0x1000000000000000000000000000000000000003").Bytes()
	kMissing := e_common.HexToAddress("0x1000000000000000000000000000000000000099").Bytes()

	// Block 0 (Genesis)
	_, pre0, _, err := ApplyNomtReplicationBatchesWithPreImages(map[string][][2][]byte{
		"Account": {{makeNomtKey(kGen), []byte("gen_val")}},
	}, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("Apply block 0: %v", err)
	}
	if len(pre0["account_state"]) != 1 || pre0["account_state"][0][1] != nil {
		t.Fatalf("expected nil pre-image for block 0 seed, got %v", pre0["account_state"])
	}

	// Block 1
	_, _, _, err = ApplyNomtReplicationBatchesWithPreImages(map[string][][2][]byte{
		"Account": {{makeNomtKey(kB1), []byte("b1_val")}},
	}, nil, nil, nil, 1)
	if err != nil {
		t.Fatalf("Apply block 1: %v", err)
	}
	b1Root, ok := GetNomtHandleRoot("account_state")
	if !ok {
		t.Fatalf("expected account_state handle root at block 1")
	}

	// Block 2 (fork) mutates kGen and inserts kForkOnly.
	_, pre2, _, err := ApplyNomtReplicationBatchesWithPreImages(map[string][][2][]byte{
		"Account": {
			{makeNomtKey(kGen), []byte("fork_gen")},
			{makeNomtKey(kForkOnly), []byte("fork_only")},
		},
	}, nil, nil, nil, 2)
	if err != nil {
		t.Fatalf("Apply block 2: %v", err)
	}
	byKey2 := make(map[string][]byte)
	for _, kv := range pre2["account_state"] {
		byKey2[string(kv[0])] = kv[1]
	}
	if !bytes.Equal(byKey2[string(kGen)], []byte("gen_val")) {
		t.Fatalf("expected pre2[kGen]=gen_val, got %q", byKey2[string(kGen)])
	}
	if v, exists := byKey2[string(kForkOnly)]; !exists || v != nil {
		t.Fatalf("expected pre2[kForkOnly]=nil, exists=%v v=%v", exists, v)
	}

	vals, err := GetNomtKeyValues("account_state", [][]byte{kGen, kB1, kForkOnly, kMissing})
	if err != nil {
		t.Fatalf("GetNomtKeyValues before rollback: %v", err)
	}
	if !bytes.Equal(vals[0], []byte("fork_gen")) || !bytes.Equal(vals[1], []byte("b1_val")) || !bytes.Equal(vals[2], []byte("fork_only")) || vals[3] != nil {
		t.Fatalf("unexpected GetNomtKeyValues before rollback: %q %q %q %v", vals[0], vals[1], vals[2], vals[3])
	}

	if err := RollbackNomtNamespaceKeys("account_state", [][]byte{kGen, kForkOnly}, [][]byte{[]byte("gen_val"), nil}); err != nil {
		t.Fatalf("RollbackNomtNamespaceKeys: %v", err)
	}

	afterRoot, _ := GetNomtHandleRoot("account_state")
	if afterRoot != b1Root {
		t.Fatalf("post-rollback root = %s, want block 1 root %s", afterRoot.Hex(), b1Root.Hex())
	}

	valsAfter, err := GetNomtKeyValues("account_state", [][]byte{kGen, kB1, kForkOnly})
	if err != nil {
		t.Fatalf("GetNomtKeyValues after rollback: %v", err)
	}
	if !bytes.Equal(valsAfter[0], []byte("gen_val")) || !bytes.Equal(valsAfter[1], []byte("b1_val")) || valsAfter[2] != nil {
		t.Fatalf("unexpected values after rollback: kGen=%q kB1=%q kForkOnly=%v", valsAfter[0], valsAfter[1], valsAfter[2])
	}

	// Reload the shared registry strictly from disk to verify deleted fork keys were permanently rewritten out.
	clearAllNomtRegistryCache()
	h, err := GetOrInitNomtHandle("account_state")
	if err != nil {
		t.Fatalf("GetOrInitNomtHandle: %v", err)
	}
	reloaded := getSharedRegistry(h.GetPath(), "account_state")
	reloaded.mu.RLock()
	_, hasGen := reloaded.keys[hex.EncodeToString(kGen)]
	_, hasB1 := reloaded.keys[hex.EncodeToString(kB1)]
	_, hasFork := reloaded.keys[hex.EncodeToString(kForkOnly)]
	reloaded.mu.RUnlock()
	if !hasGen || !hasB1 {
		t.Fatalf("reloaded registry (%s) missing kGen=%v or kB1=%v", nomtDir, hasGen, hasB1)
	}
	if hasFork {
		t.Fatalf("deleted kForkOnly must not remain in on-disk registry after RollbackNomtNamespaceKeys")
	}
}
