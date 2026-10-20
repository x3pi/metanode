package trie

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
)

// A failed background NOMT commit must reach onAsyncPersistenceFailure (fail-stop in production) and leave the
// persistence ticket Failed (the commit error is injected through commitFinishedSession), so the committing overlay is never treated as durable.
func TestNomtPayload_CommitAsyncFailureTriggersFailStop(t *testing.T) {
	var calls atomic.Int32
	var gotNS atomic.Value
	prev := onAsyncPersistenceFailure
	onAsyncPersistenceFailure = func(ns string, _ uint64, err error) {
		if err == nil {
			t.Error("handler called without an error")
		}
		gotNS.Store(ns)
		calls.Add(1)
	}
	t.Cleanup(func() { onAsyncPersistenceFailure = prev })

	prevCommit := commitFinishedSession
	commitFinishedSession = func(*nomt_ffi.FinishedSession, *nomt_ffi.Handle) error {
		return errors.New("injected: No space left on device")
	}
	t.Cleanup(func() { commitFinishedSession = prevCommit })

	h := openTestNomtHandle(t)
	tr := NewNomtStateTrie(h, true, "account_state")
	if err := tr.Update(slotKey(1), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := tr.Commit(true); err != nil {
		t.Fatal(err)
	}
	payload := tr.ExtractPendingPayload()
	if payload == nil {
		t.Fatal("expected non-nil payload")
	}
	ticket := payload.commitTicket

	payload.CommitAsync()
	tr.commitWg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("fail-stop handler called %d times, want 1", calls.Load())
	}
	if ns, _ := gotNS.Load().(string); ns != "account_state" {
		t.Fatalf("handler namespace = %q", ns)
	}
	if ticket != nil && ticket.State() != NomtPersistenceFailed {
		t.Fatalf("ticket state = %v, want Failed", ticket.State())
	}
}
