package trie

import (
	"sync"
	"testing"
	"time"
)

// A discarded speculative trie that already finished its NOMT session must not keep the shared handle busy:
// the sequential re-execution of the same block (a copy of the real tip) has to be able to begin its own
// session, and the discarded state must NOT be persisted.
func TestNomtStateTrie_AbortPendingReleasesHandleWithoutPersisting(t *testing.T) {
	h := openTestNomtHandle(t)
	const ns = "account_state_abort_pending"

	base := NewNomtStateTrie(h, true, ns)
	if err := base.Update(slotKey(1), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := base.Commit(true); err != nil {
		t.Fatal(err)
	}
	if err := base.CommitPayload(); err != nil {
		t.Fatal(err)
	}

	// The speculative execution: copy of the tip, writes, IntermediateRoot -> Commit leaves a pending session.
	spec := base.Copy().(*NomtStateTrie)
	if err := spec.Update(slotKey(2), []byte{2}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := spec.Commit(true); err != nil {
		t.Fatal(err)
	}

	// The re-execution on the real tip.
	redo := base.Copy().(*NomtStateTrie)
	if err := redo.Update(slotKey(3), []byte{3}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, _, err := redo.Commit(true); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("re-execution began a session while the discarded speculative one was still pending (err=%v): the precondition of the bug is not reproduced", err)
	case <-time.After(300 * time.Millisecond):
	}

	spec.AbortPending()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-execution is still blocked after the discarded speculative state was aborted")
	}
	if err := redo.CommitPayload(); err != nil {
		t.Fatal(err)
	}

	// Only the real chain's data is on disk.
	fresh := NewNomtStateTrie(h, true, ns)
	if v, _ := fresh.Get(slotKey(2)); len(v) != 0 {
		t.Fatalf("discarded speculative write was persisted: %x", v)
	}
	if v, _ := fresh.Get(slotKey(3)); len(v) != 1 || v[0] != 3 {
		t.Fatalf("re-execution's write missing: %x", v)
	}
	if v, _ := fresh.Get(slotKey(1)); len(v) != 1 || v[0] != 1 {
		t.Fatalf("baseline missing: %x", v)
	}
}

// AbortPending on a trie with nothing pending is a harmless no-op (the committer may call it on states that
// never reached IntermediateRoot).
func TestNomtStateTrie_AbortPendingWithNothingPending(t *testing.T) {
	h := openTestNomtHandle(t)
	tr := NewNomtStateTrie(h, true, "account_state_abort_none")
	tr.AbortPending()
	tr.AbortPending()
	if err := tr.Update(slotKey(1), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := tr.Commit(true); err != nil {
		t.Fatal(err)
	}
	if err := tr.CommitPayload(); err != nil {
		t.Fatal(err)
	}
}

// TestNomtPayload_CommitAsyncAndDiscard_ConcurrentRace tests that CommitAsync and Discard
// can be safely called concurrently without causing a data race or double-commit/double-abort on FFI.
func TestNomtPayload_CommitAsyncAndDiscard_ConcurrentRace(t *testing.T) {
	h := openTestNomtHandle(t)
	for i := 0; i < 20; i++ {
		tr := NewNomtStateTrie(h, true, "account_state_payload_race")
		if err := tr.Update(slotKey(byte(i+1)), []byte{byte(i + 1)}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := tr.Commit(true); err != nil {
			t.Fatal(err)
		}

		payload := tr.ExtractPendingPayload()
		if payload == nil {
			t.Fatal("expected non-nil payload")
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			payload.CommitAsync()
		}()

		go func() {
			defer wg.Done()
			<-start
			payload.Discard()
		}()

		close(start)
		wg.Wait()

		// Wait for whichever branch executed to call commitWg.Done()
		tr.commitWg.Wait()
	}
}

// TestNomtPayload_CommitAsyncThenDiscard_SynchronousOwnership verifies that
// CommitAsync claims ownership synchronously so a subsequent Discard is a no-op,
// leaving the ticket Durable and the session committed.
func TestNomtPayload_CommitAsyncThenDiscard_SynchronousOwnership(t *testing.T) {
	h := openTestNomtHandle(t)
	tr := NewNomtStateTrie(h, true, "account_state_sync_ownership")
	if err := tr.Update(slotKey(1), []byte{42}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := tr.Commit(true); err != nil {
		t.Fatal(err)
	}

	ticket := NewNomtPersistenceTicket()
	tr.SetPendingCommitTicket(ticket)

	payload := tr.ExtractPendingPayload()
	if payload == nil {
		t.Fatal("expected non-nil payload")
	}

	// Call CommitAsync and immediately call Discard on same thread
	payload.CommitAsync()
	payload.Discard() // Must be a no-op because CommitAsync claimed ownership synchronously

	tr.commitWg.Wait()

	if ticket.State() != NomtPersistenceDurable {
		t.Fatalf("expected ticket state Durable, got %v", ticket.State())
	}
}

