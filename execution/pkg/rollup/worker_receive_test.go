package rollup

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

func TestReceiveWorker(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()
	client := &mockParentChainClient{}

	kp1 := bls.GenerateKeyPair()
	worker := NewReceiveWorker(store, stateDB, client, kp1)
	client.claimedOutcome = parentchain.FloatOutcomeCredited

	// Test 1: New inbound transfer
	msgID := common.HexToHash("0x789")
	tx := &parentchain.TransferEvent{
		MsgID:       msgID,
		SourcePubKey: cm.PublicKey{},
		DestPubKey:   cm.PublicKey{},
		SourceSeq:   1,
		Sender:      common.HexToAddress("0xaaa"),
		Target:      common.HexToAddress("0xbbb"),
		Amount:      big.NewInt(500),
	}

	worker.handleIncomingTransfer(tx)

	// Check record created
	rec, found, _ := store.Get(msgID)
	if !found {
		t.Fatalf("Expected record created")
	}
	if rec.State != StateMarkedClaimedPendingCredit {
		t.Errorf("Expected MARKED_CLAIMED_PENDING_CREDIT, got %v", rec.State)
	}

	// Test 2: processMarkClaimedPendingCredit (step 1: submit MarkClaimed)
	worker.pollAndProcess()

	rec, _, _ = store.Get(msgID)
	if rec.State != StateMarkClaimedSubmitted {
		t.Errorf("Expected MARK_CLAIMED_SUBMITTED after submit, got %v", rec.State)
	}

	// Test 2b: next cycle polls GetClaimed and confirms (step 2: credit applied)
	worker.pollAndProcess()

	rec, _, _ = store.Get(msgID)
	if rec.State != StateCredited {
		t.Errorf("Expected CREDITED after poll-confirm, got %v", rec.State)
	}
	
	// Target should have 500 more balance
	if stateDB.GetBalance(tx.Target).Cmp(big.NewInt(500)) != 0 {
		t.Errorf("Expected 500 balance, got %v", stateDB.GetBalance(tx.Target))
	}

	// Test 3: Duplicated transfer (cursor updated but record skipped)
	worker.handleIncomingTransfer(tx)
	// state should remain CREDITED, no new balance added
	rec, _, _ = store.Get(msgID)
	if rec.State != StateCredited {
		t.Errorf("Expected CREDITED after duplicate, got %v", rec.State)
	}
	if stateDB.GetBalance(tx.Target).Cmp(big.NewInt(500)) != 0 {
		t.Errorf("Expected balance still 500 after duplicate, got %v", stateDB.GetBalance(tx.Target))
	}
}

// TestReceiveWorker_RefundSentConfirmation drives a record through the Refund path and
// verifies the compensating Transfer this worker sends is itself confirmed (via
// GetTransferRecord) before the record is allowed to close, rather than being trusted the
// instant the RPC call to submit it returns successfully.
func TestReceiveWorker_RefundSentConfirmation(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()
	client := &mockParentChainClient{claimedOutcome: parentchain.FloatOutcomeRefund}

	kp1 := bls.GenerateKeyPair()
	worker := NewReceiveWorker(store, stateDB, client, kp1)

	msgID := common.HexToHash("0xaaa789")
	tx := &parentchain.TransferEvent{
		MsgID:        msgID,
		SourcePubKey: cm.PublicKey{},
		DestPubKey:   cm.PublicKey{},
		SourceSeq:    1,
		Sender:       common.HexToAddress("0xaaa"),
		Target:       common.HexToAddress("0xbbb"),
		Amount:       big.NewInt(500),
	}
	worker.Validator = func(addr common.Address) bool { return false } // force the refund path
	worker.handleIncomingTransfer(tx)

	// Step 1: submit MarkClaimed(Refund).
	worker.pollAndProcess()
	rec, _, _ := store.Get(msgID)
	if rec.State != StateMarkClaimedSubmitted {
		t.Fatalf("Expected MARK_CLAIMED_SUBMITTED after submit, got %v", rec.State)
	}

	// Step 2: poll-confirm MarkClaimed(Refund) -> sends the compensating Transfer, record
	// moves to StateRefundSent with RefundMsgID set, but is NOT yet StateRefunded.
	worker.pollAndProcess()
	rec, _, _ = store.Get(msgID)
	if rec.State != StateRefundSent {
		t.Fatalf("Expected REFUND_SENT after sending compensating transfer, got %v", rec.State)
	}
	if rec.RefundMsgID == (common.Hash{}) {
		t.Fatalf("Expected RefundMsgID to be set once the compensating transfer was submitted")
	}

	// Step 3: the compensating transfer has NOT yet been confirmed on Parent Chain (e.g. still
	// queued) -- must NOT close the record on the strength of "submit succeeded" alone.
	client.transferNotFound = true
	worker.pollAndProcess()
	rec, _, _ = store.Get(msgID)
	if rec.State != StateRefundSent {
		t.Fatalf("Expected record to stay REFUND_SENT while the compensating transfer is unconfirmed, got %v", rec.State)
	}

	// Step 4: Parent Chain now reports the compensating transfer landed -- only now may the
	// record close.
	client.transferNotFound = false
	worker.pollAndProcess()
	rec, _, _ = store.Get(msgID)
	if rec.State != StateRefunded {
		t.Fatalf("Expected REFUNDED once the compensating transfer is confirmed, got %v", rec.State)
	}
}
