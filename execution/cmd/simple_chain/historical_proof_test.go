package main

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

func TestLockHistoricalRebuild_RefusesInsteadOfQueueing(t *testing.T) {
	verifyCacheMu.Lock() // a rebuild is in progress
	if err := lockHistoricalRebuild(); !errors.Is(err, errHistoricalProofBusy) {
		verifyCacheMu.Unlock()
		t.Fatalf("want errHistoricalProofBusy while a rebuild runs, got %v", err)
	}
	verifyCacheMu.Unlock()
	if err := lockHistoricalRebuild(); err != nil {
		t.Fatalf("free lock must be taken, got %v", err)
	}
	verifyCacheMu.Unlock()
}

func TestCheckRecoveredHistoricalRoot(t *testing.T) {
	a, b := common.HexToHash("0x01"), common.HexToHash("0x02")
	if err := checkRecoveredHistoricalRoot(a, a); err != nil {
		t.Fatalf("equal roots must pass, got %v", err)
	}
	if err := checkRecoveredHistoricalRoot(a, b); err == nil {
		t.Fatal("different roots must be refused")
	}
}

func TestCachedHistoricalTrieHit_OnlyForTheVerifiedSameBlock(t *testing.T) {
	handle, err := nomt_ffi.Open(t.TempDir(), 1, 64, 64, 0, true)
	if err != nil {
		t.Skipf("nomt ffi unavailable: %v", err)
	}
	defer handle.Close()
	tr := mt_trie.NewNomtStateTrie(handle, false, "account_state")
	tr.Update([]byte("addr-1"), []byte("v1"))
	if _, _, _, err := tr.Commit(true); err != nil {
		t.Fatal(err)
	}
	if err := tr.CommitPayload(); err != nil {
		t.Fatal(err)
	}
	root := tr.Hash()

	verifyCacheMu.Lock()
	defer verifyCacheMu.Unlock()
	oldT, oldB, oldOK := verifyCachedTrie, verifyCachedBlock, verifyCachedOK
	defer func() { verifyCachedTrie, verifyCachedBlock, verifyCachedOK = oldT, oldB, oldOK }()
	verifyCachedTrie, verifyCachedBlock, verifyCachedOK = tr, 7, true

	if !cachedHistoricalTrieHit(7, root) {
		t.Fatal("verified cached trie of the same block must hit")
	}
	if cachedHistoricalTrieHit(8, root) {
		t.Fatal("a different block must not hit")
	}
	if cachedHistoricalTrieHit(7, common.HexToHash("0xdead")) {
		t.Fatal("a different expected root must not hit")
	}
	verifyCachedOK = false
	if cachedHistoricalTrieHit(7, root) {
		t.Fatal("an unverified cached trie must never hit")
	}
}
