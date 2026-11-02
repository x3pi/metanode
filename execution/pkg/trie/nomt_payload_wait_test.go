package trie

import (
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// TestNomtPayload_Wait verifies that NomtPayload.Wait() synchronously blocks until
// background CommitAsync finishes, durable state is marked, and lastNomtCommittedBlock is updated.
func TestNomtPayload_Wait(t *testing.T) {
	h := openTestNomtHandle(t)
	const ns = "account_state"

	tr := NewNomtStateTrie(h, true, ns)
	if err := tr.Update(slotKey(1), []byte{99}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, _, _, err := tr.Commit(true); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	ticket := NewNomtPersistenceTicket()
	tr.SetPendingCommitTicket(ticket)

	payload := tr.ExtractPendingPayload()
	if payload == nil {
		t.Fatal("expected non-nil NomtPayload from ExtractPendingPayload")
	}

	const testBlockNum = 12345
	payload.SetBlockNumber(testBlockNum)

	// Trigger async commit
	payload.CommitAsync()

	// Wait must block until completion and return no error
	if err := payload.Wait(); err != nil {
		t.Fatalf("payload.Wait() returned error: %v", err)
	}

	// Read view must have cleared committing overlay and set durable ticket
	view := tr.loadReadView()
	if view == nil {
		t.Fatal("expected non-nil readView")
	}
	if len(view.committing) != 0 {
		t.Fatalf("expected committing overlay to be cleared, got len=%d", len(view.committing))
	}
	if view.commitTicket == nil || view.commitTicket.State() != NomtPersistenceDurable {
		t.Fatalf("expected commitTicket state to be Durable, got %v", view.commitTicket)
	}

	// Verify storage block number was updated
	if lastCommitted := storage.GetLastNomtCommittedBlock(); lastCommitted < testBlockNum {
		t.Fatalf("expected GetLastNomtCommittedBlock >= %d, got %d", testBlockNum, lastCommitted)
	}
}

// TestNomtStateTrie_CommitPayload_SiblingBatchTicket verifies that sibling contract tries
// sharing a commit ticket with the primary trie cleanly succeed and clear the committing overlay
// once the shared ticket is durable.
func TestNomtStateTrie_CommitPayload_SiblingBatchTicket(t *testing.T) {
	h := openTestNomtHandle(t)
	tr := NewNomtStateTrie(h, true, "contract_sibling_test")

	ticket := NewNomtPersistenceTicket()
	ticket.SetState(NomtPersistencePending)

	// Simulate committing overlay from a shared batch
	committingMap := map[string]*nomtDirtyEntry{
		"slot": {originalKey: []byte("slot"), value: []byte{1}},
	}
	tr.writerMu.Lock()
	tr.publishReadView(nil, committingMap, [32]byte{1}, ticket)
	tr.writerMu.Unlock()

	// 1. While ticket is pending (still in flight), CommitPayload should return nil without error
	if err := tr.CommitPayload(); err != nil {
		t.Fatalf("CommitPayload with pending shared ticket should not error, got: %v", err)
	}
	if len(tr.loadReadView().committing) == 0 {
		t.Fatal("committing overlay should remain while ticket is still Pending")
	}

	// 2. Once shared ticket becomes Durable, CommitPayload should clear the committing overlay
	ticket.SetState(NomtPersistenceDurable)
	if err := tr.CommitPayload(); err != nil {
		t.Fatalf("CommitPayload with durable shared ticket should succeed, got: %v", err)
	}
	if len(tr.loadReadView().committing) != 0 {
		t.Fatalf("committing overlay should be cleared once Durable, got len=%d", len(tr.loadReadView().committing))
	}

	// 3. If ticket failed, CommitPayload should return an error
	ticketFailed := NewNomtPersistenceTicket()
	ticketFailed.SetState(NomtPersistenceFailed)
	committingMapFailed := map[string]*nomtDirtyEntry{
		"slot": {originalKey: []byte("slot"), value: []byte{2}},
	}
	tr.writerMu.Lock()
	tr.publishReadView(nil, committingMapFailed, [32]byte{2}, ticketFailed)
	tr.writerMu.Unlock()

	if err := tr.CommitPayload(); err == nil {
		t.Fatal("CommitPayload with failed shared ticket must return error")
	}
}
