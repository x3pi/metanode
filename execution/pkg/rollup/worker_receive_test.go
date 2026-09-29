package rollup

import (
	"errors"
	"math/big"
	"testing"
	"time"

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

// TestReceiveWorker_RaftRefundDisconnectionRecovery verifies that when a record enters
// StateRefundSent without a RefundMsgID (the exact scenario produced by Raft FSM execution
// because HTTP requests cannot run deterministically inside Raft blocks), the background
// ReceiveWorker properly sends the HTTP SendTransferFloat refund, records the RefundMsgID,
// and proceeds to confirmation instead of silently dropping the refund.
func TestReceiveWorker_RaftRefundDisconnectionRecovery(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()
	client := &mockParentChainClient{claimedOutcome: parentchain.FloatOutcomeRefund}

	kp1 := bls.GenerateKeyPair()
	worker := NewReceiveWorker(store, stateDB, client, kp1)

	msgID := common.HexToHash("0xraft_refund_test")
	rec := &MessageRecord{
		MessageID:    msgID,
		Role:         RoleReceiver,
		State:        StateRefundSent,
		Sender:       common.HexToAddress("0x1111"),
		Target:       common.HexToAddress("0x2222"),
		Value:        big.NewInt(777),
		SourceSeq:    42,
		SourcePubKey: cm.PublicKey{},
		DestPubKey:   kp1.PublicKey(),
		RefundMsgID:  common.Hash{}, // EMPTY: simulated Raft state transition
	}
	if err := store.Put(rec); err != nil {
		t.Fatalf("Failed to put record: %v", err)
	}

	// First tick: worker detects missing RefundMsgID, calls SendTransferFloat, and records RefundMsgID.
	worker.pollAndProcess()

	updatedRec, found, err := store.Get(msgID)
	if err != nil || !found {
		t.Fatalf("Record not found: %v", err)
	}
	if updatedRec.RefundMsgID == (common.Hash{}) {
		t.Fatalf("Expected worker to send refund and record RefundMsgID, but got empty hash")
	}
	if updatedRec.State != StateRefundSent {
		t.Fatalf("Expected state to remain StateRefundSent while waiting for confirmation, got %v", updatedRec.State)
	}

	// Second tick: worker polls ParentChain for the refundMsgID and confirms it landed -> transitions to StateRefunded.
	client.transferNotFound = false
	worker.pollAndProcess()

	finalRec, found, err := store.Get(msgID)
	if err != nil || !found {
		t.Fatalf("Record not found: %v", err)
	}
	if finalRec.State != StateRefunded {
		t.Fatalf("Expected StateRefunded after confirmation, got %v", finalRec.State)
	}
}

func TestReceiveWorker_EventDrivenWakeUp(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()
	client := &mockParentChainClient{}

	kp := bls.GenerateKeyPair()
	worker := NewReceiveWorker(store, stateDB, client, kp)
	// Set 1 hour ticker to guarantee execution is purely event-driven
	worker.SetInterval(1 * time.Hour)

	msgID := common.HexToHash("0xbbbb2222")
	record := &MessageRecord{
		MessageID: msgID,
		Role:      RoleReceiver,
		State:     StateMarkedClaimedPendingCredit,
		Sender:    common.HexToAddress("0x111"),
		Target:    common.HexToAddress("0x222"),
		Value:     big.NewInt(100),
		SourceSeq: 1,
	}
	_ = store.Put(record)

	worker.Start()
	defer worker.Stop()

	// Trigger immediate event-driven wake up
	worker.WakeUp()

	// Verify state advances to MARK_CLAIMED_SUBMITTED without waiting for ticker
	deadline := time.Now().Add(500 * time.Millisecond)
	var rec *MessageRecord
	for time.Now().Before(deadline) {
		r, found, _ := store.Get(msgID)
		if found && r.State == StateMarkClaimedSubmitted {
			rec = r
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if rec == nil || rec.State != StateMarkClaimedSubmitted {
		t.Fatalf("Expected record to advance via WakeUp without waiting for ticker, got: %v", rec)
	}
}

func TestReceiveWorker_SetInterval(t *testing.T) {
	worker := NewReceiveWorker(nil, nil, nil, nil)
	if worker.interval != 1*time.Second {
		t.Errorf("Expected default interval 1s, got %v", worker.interval)
	}
	worker.SetInterval(200 * time.Millisecond)
	if worker.interval != 200*time.Millisecond {
		t.Errorf("Expected interval 200ms, got %v", worker.interval)
	}
	worker.SetInterval(0)
	if worker.interval != 200*time.Millisecond {
		t.Errorf("Expected interval to remain 200ms, got %v", worker.interval)
	}
}

func TestReceiveWorker_ProgressDrivenPolling(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()
	client := &mockParentChainClient{}

	kp := bls.GenerateKeyPair()
	worker := NewReceiveWorker(store, stateDB, client, kp)

	// Case 1: Idle state (no inbound transfers, no non-terminal records)
	// hasProgress must be false to avoid spinning 100ms ticker
	if worker.pollAndProcess() {
		t.Errorf("Expected false when idle, got true")
	}

	// Case 2: Inbound transfer arrives from parent chain, but SendMarkClaimed fails on initial try
	msgID := common.HexToHash("0x1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff")
	tx := &parentchain.TransferEvent{
		MsgID:        msgID,
		SourcePubKey: cm.PublicKey{},
		DestPubKey:   kp.PublicKey(),
		SourceSeq:    1,
		Sender:       common.HexToAddress("0xaaa"),
		Target:       common.HexToAddress("0xbbb"),
		Amount:       big.NewInt(300),
	}
	client.inboundTransfers = []*parentchain.TransferEvent{tx}
	client.failNext = true
	client.failError = errors.New("initial mark claimed failure")

	// Must return true because inbound transfer was fetched (len(transfers) > 0)
	if !worker.pollAndProcess() {
		t.Errorf("Expected true when inbound transfer arrives, got false")
	}

	// Record was created and remains in StateMarkedClaimedPendingCredit because SendMarkClaimed failed
	rec, found, _ := store.Get(msgID)
	if !found || rec.State != StateMarkedClaimedPendingCredit {
		t.Fatalf("Expected StateMarkedClaimedPendingCredit, got %v", rec)
	}

	// Case 3: Retrying while RPC is still failing (no new transfers, SendMarkClaimed fails)
	// Must return false to prevent spinning at 100ms
	client.failNext = true
	client.failError = errors.New("network timeout")
	if worker.pollAndProcess() {
		t.Errorf("Expected false when SendMarkClaimed fails, got true")
	}

	// Case 4: SendMarkClaimed succeeds -> advances to StateMarkClaimedSubmitted -> must return true
	client.failNext = false
	if !worker.pollAndProcess() {
		t.Errorf("Expected true when SendMarkClaimed succeeds, got false")
	}

	rec, _, _ = store.Get(msgID)
	if rec.State != StateMarkClaimedSubmitted {
		t.Fatalf("Expected StateMarkClaimedSubmitted, got %v", rec.State)
	}

	// Case 5: Waiting for Parent Chain confirmation (GetClaimed returns FloatOutcomeNone)
	// Must return false to prevent spamming GetClaimed every 100ms
	client.claimedOutcome = parentchain.FloatOutcomeNone
	if worker.pollAndProcess() {
		t.Errorf("Expected false when waiting for GetClaimed confirmation, got true")
	}

	// Case 6: Parent Chain confirms (GetClaimed returns FloatOutcomeCredited)
	// Must return true (progress: credit applied, transitions to StateCredited)
	client.claimedOutcome = parentchain.FloatOutcomeCredited
	if !worker.pollAndProcess() {
		t.Errorf("Expected true when GetClaimed confirms, got false")
	}

	rec, _, _ = store.Get(msgID)
	if rec.State != StateCredited {
		t.Fatalf("Expected StateCredited, got %v", rec.State)
	}

	// Case 7: Refund flow progress testing
	msgIDRefund := common.HexToHash("0xffffeeeeeeddddccccbbbbaaaa0000999988887777666655554444333322221111")
	refundRec := &MessageRecord{
		MessageID:    msgIDRefund,
		Role:         RoleReceiver,
		State:        StateRefundSent,
		Sender:       common.HexToAddress("0x1111"),
		Target:       common.HexToAddress("0x2222"),
		Value:        big.NewInt(100),
		SourceSeq:    5,
		SourcePubKey: cm.PublicKey{},
		DestPubKey:   kp.PublicKey(),
		RefundMsgID:  common.HexToHash("0x9999888877776666555544443333222211110000aaaabbbbccccddddeeeeffff"),
	}
	_ = store.Put(refundRec)

	// Sub-case 7a: Refund tx not yet found on Parent Chain -> must return false (waiting for block)
	client.transferNotFound = true
	if worker.pollAndProcess() {
		t.Errorf("Expected false when waiting for refund tx confirmation, got true")
	}

	// Sub-case 7b: Refund tx confirmed on Parent Chain -> must return true (transitions to StateRefunded)
	client.transferNotFound = false
	if !worker.pollAndProcess() {
		t.Errorf("Expected true when refund tx is confirmed, got false")
	}

	recRefund, _, _ := store.Get(msgIDRefund)
	if recRefund.State != StateRefunded {
		t.Fatalf("Expected StateRefunded, got %v", recRefund.State)
	}
}

