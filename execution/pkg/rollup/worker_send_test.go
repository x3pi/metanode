package rollup

import (
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type mockParentChainClient struct {
	sentTransfers    []common.Hash
	inboundTransfers []*parentchain.TransferEvent
	failNext         bool
	claimedOutcome   parentchain.FloatOutcome
	// found=true, matching this mock's original behavior before the field existed).
	transferNotFound bool
	failError        error

	submitStateRootCount int
	shouldFailSubmit     bool
	mu                   sync.Mutex
}

func (m *mockParentChainClient) SendDepositToFloat(
	pubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount *big.Int,
) (common.Hash, error) {
	return common.Hash{}, nil
}

func (m *mockParentChainClient) SendTransferFloat(
	pubKey, destPubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount, gasFee *big.Int,
	nonce uint64,
	cert []byte,
	isRefund bool,
) (common.Hash, error) {
	if m.failNext {
		m.failNext = false
		if m.failError != nil {
			err := m.failError
			m.failError = nil
			return common.Hash{}, err
		}
		return common.Hash{}, errors.New("transient network error")
	}
	hash := common.HexToHash("0xabc")
	m.sentTransfers = append(m.sentTransfers, hash)
	return hash, nil
}

func (m *mockParentChainClient) SendMarkClaimed(msgID common.Hash, outcome parentchain.FloatOutcome, cert []byte) (common.Hash, error) {
	if m.failNext {
		m.failNext = false
		if m.failError != nil {
			err := m.failError
			m.failError = nil
			return common.Hash{}, err
		}
		return common.Hash{}, errors.New("transient network error")
	}
	return common.Hash{}, nil
}

func (m *mockParentChainClient) SendReclaimFloat(msgID common.Hash, cert []byte) (common.Hash, error) {
	if m.failNext {
		m.failNext = false
		if m.failError != nil {
			err := m.failError
			m.failError = nil
			return common.Hash{}, err
		}
		return common.Hash{}, errors.New("transient network error")
	}
	return common.Hash{}, nil
}

func (m *mockParentChainClient) GetInboundTransfers(pubKey cm.PublicKey, cursor uint64) ([]*parentchain.TransferEvent, uint64, error) {
	if len(m.inboundTransfers) > 0 {
		txs := m.inboundTransfers
		m.inboundTransfers = nil
		return txs, cursor + uint64(len(txs)), nil
	}
	return nil, cursor, nil
}

func (m *mockParentChainClient) GetTransferRecord(msgID common.Hash) (parentchain.FloatTransferRecord, bool, error) {
	if m.transferNotFound {
		return parentchain.FloatTransferRecord{}, false, nil
	}
	// For testing timeout/reclaim logic, say it's old enough
	rec := parentchain.FloatTransferRecord{
		ConfirmedAtBlockTime: 10,
	}
	return rec, true, nil
}

func (m *mockParentChainClient) GetClaimed(msgID common.Hash) (parentchain.FloatOutcome, error) {
	return m.claimedOutcome, nil
}

func (m *mockParentChainClient) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	return cm.PublicKey{}, true, nil
}

func (m *mockParentChainClient) SendRegisterAccount(userAddress common.Address, pubKey cm.PublicKey, signBytes []byte, sign cm.Sign) (common.Hash, error) {
	return common.Hash{}, nil
}

func (m *mockParentChainClient) GetFloatSeq(pubKey cm.PublicKey) (uint64, error) {
	return 0, nil
}

func (m *mockParentChainClient) SendSubmitStateRoot(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) (common.Hash, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submitStateRootCount++
	if m.shouldFailSubmit {
		return common.Hash{}, errors.New("transient network error")
	}
	return common.Hash{}, nil
}

func (m *mockParentChainClient) GetStateRoot(clusterPubKey cm.PublicKey, epoch uint64) (common.Hash, bool, error) {
	return common.Hash{}, false, nil
}

func (m *mockParentChainClient) GetBlockByNumber(number uint64) (parentchain.BlockRecord, bool, error) {
	return parentchain.BlockRecord{}, false, nil
}

func (m *mockParentChainClient) GetBlockByHash(hash common.Hash) (parentchain.BlockRecord, bool, error) {
	return parentchain.BlockRecord{}, false, nil
}

func (m *mockParentChainClient) GetTransaction(txHash common.Hash) (uint64, uint32, bool, error) {
	return 0, 0, false, nil
}

func (m *mockParentChainClient) GetReceipt(txHash common.Hash) (*parentchain.Receipt, bool, error) {
	return nil, false, nil
}

func (m *mockParentChainClient) GetStatus() (parentchain.ChainStatus, error) {
	return parentchain.ChainStatus{}, nil
}

func (m *mockParentChainClient) GetProof(key [32]byte) (parentchain.ProofResult, error) {
	return parentchain.ProofResult{}, nil
}

func (m *mockParentChainClient) SendRawTransaction(rawTx []byte) (common.Hash, error) {
	return common.Hash{}, nil
}



func TestSendWorker(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	client := &mockParentChainClient{}

	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()

	worker := NewSendWorker(store, client, kp1, kp2.PublicKey(), 102)

	// Create a record in PENDING
	msgID := common.HexToHash("0x123")
	record := &MessageRecord{
		MessageID: msgID,
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Sender:    common.HexToAddress("0xaaa"),
		Target:    common.HexToAddress("0xbbb"),
		Value:     big.NewInt(100),
		SourceSeq: 1,
	}
	_ = store.Put(record)

	// Test 1: Transient error doesn't advance state
	client.failNext = true
	worker.processPending()
	
	rec, _, _ := store.Get(msgID)
	if rec.State != StateLocalAppliedPendingSend {
		t.Errorf("Expected state to remain pending, got %v", rec.State)
	}
	if len(client.sentTransfers) != 0 {
		t.Errorf("Expected 0 sent, got %d", len(client.sentTransfers))
	}

	// Test 2: Success submits the tx (step 1 of 2: submit, not yet confirmed)
	worker.processPending()

	rec, _, _ = store.Get(msgID)
	if rec.State != StateSendSubmitted {
		t.Errorf("Expected state SEND_SUBMITTED after submit, got %v", rec.State)
	}
	if len(client.sentTransfers) != 1 {
		t.Errorf("Expected 1 sent, got %d", len(client.sentTransfers))
	}

	// Test 2b: next cycle polls GetTransferRecord and confirms (step 2 of 2)
	worker.processPending()

	rec, _, _ = store.Get(msgID)
	if rec.State != StateSentConfirmed {
		t.Errorf("Expected state SENT_CONFIRMED after poll-confirm, got %v", rec.State)
	}
	if len(client.sentTransfers) != 1 {
		t.Errorf("Expected still 1 sent (poll must not resend), got %d", len(client.sentTransfers))
	}

	// Test 3: Idempotent processing (won't send again because it's no longer PENDING)
	worker.processPending()
	if len(client.sentTransfers) != 1 {
		t.Errorf("Expected 1 sent, got %d", len(client.sentTransfers))
	}

	// Test 4: "wrong nonce" error should advance state
	msgID2 := common.HexToHash("0xdef")
	record2 := &MessageRecord{
		MessageID: msgID2,
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Sender:    common.HexToAddress("0x111"),
		Target:    common.HexToAddress("0x222"),
		Value:     big.NewInt(100),
		SourceSeq: 2,
	}
	_ = store.Put(record2)

	client.failNext = true
	client.failError = errors.New("float account: wrong nonce: got 2, want 3")
	worker.processPending()

	rec2, _, _ := store.Get(msgID2)
	if rec2.State != StateSendSubmitted {
		t.Errorf("Expected state to advance to SEND_SUBMITTED on wrong nonce error, got %v", rec2.State)
	}

	// Start and Stop test (non-blocking channel)
	worker.Start()
	worker.WakeUp()
	worker.WakeUp() // Channel full shouldn't block
	time.Sleep(10 * time.Millisecond)
	worker.Stop()
}

func TestSendWorker_EventDrivenWakeUp(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	client := &mockParentChainClient{}

	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()

	worker := NewSendWorker(store, client, kp1, kp2.PublicKey(), 102)
	// Set an abnormally long ticker (1 hour) to prove processing happens via WakeUp, NOT the ticker!
	worker.SetInterval(1 * time.Hour)

	msgID := common.HexToHash("0xaaaa1111")
	record := &MessageRecord{
		MessageID: msgID,
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
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

	// Verify state advances to SEND_SUBMITTED almost instantly (within 100ms) without waiting for ticker
	deadline := time.Now().Add(500 * time.Millisecond)
	var rec *MessageRecord
	for time.Now().Before(deadline) {
		r, found, _ := store.Get(msgID)
		if found && r.State == StateSendSubmitted {
			rec = r
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if rec == nil || rec.State != StateSendSubmitted {
		t.Fatalf("Expected record to advance via WakeUp without waiting for ticker, got: %v", rec)
	}
}

func TestSendWorker_SetInterval(t *testing.T) {
	worker := NewSendWorker(nil, nil, nil, cm.PublicKey{}, 1)
	if worker.interval != 1*time.Second {
		t.Errorf("Expected default interval 1s, got %v", worker.interval)
	}
	worker.SetInterval(500 * time.Millisecond)
	if worker.interval != 500*time.Millisecond {
		t.Errorf("Expected interval 500ms, got %v", worker.interval)
	}
	// Invalid value ignored
	worker.SetInterval(0)
	if worker.interval != 500*time.Millisecond {
		t.Errorf("Expected interval to remain 500ms, got %v", worker.interval)
	}
}

func TestSendWorker_ProgressDrivenPolling(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	client := &mockParentChainClient{}

	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	worker := NewSendWorker(store, client, kp1, kp2.PublicKey(), 102)

	var proposedEvents []Event
	worker.EventProposer = func(event Event, msgID common.Hash, sourceSeq uint64, sourcePubKey, destPubKey cm.PublicKey, payloadHash common.Hash) error {
		proposedEvents = append(proposedEvents, event)
		return nil
	}

	// Case 1: Empty store: no records -> hasProgress must be false (idle)
	if worker.processPending() {
		t.Errorf("Expected false for empty store, got true")
	}

	// Case 2: Add a pending send record
	msgID := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	record := &MessageRecord{
		MessageID: msgID,
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Sender:    common.HexToAddress("0xaaa"),
		Target:    common.HexToAddress("0xbbb"),
		Value:     big.NewInt(100),
		SourceSeq: 1,
	}
	_ = store.Put(record)

	// Sub-case 2a: SendTransferFloat fails with transient error -> stuck, must return false (no 100ms RPC spam)
	client.failNext = true
	client.failError = errors.New("network timeout")
	if worker.processPending() {
		t.Errorf("Expected false on send failure, got true")
	}

	// Sub-case 2b: Successful SendTransferFloat -> makes progress, must return true
	if !worker.processPending() {
		t.Errorf("Expected true on successful SendTransferFloat, got false")
	}
	if len(proposedEvents) != 1 || proposedEvents[0].Type != EventRPCSubmitted {
		t.Fatalf("Expected EventRPCSubmitted proposed, got %v", proposedEvents)
	}

	// Sub-case 2c: In-flight check: while record is waiting for local consensus commit,
	// store still has StateLocalAppliedPendingSend, but inFlightSubmits prevents re-submission
	// and returns false (no new progress, no duplicate RPC call).
	if worker.processPending() {
		t.Errorf("Expected false while record is inFlight waiting for local commit, got true")
	}

	// Sub-case 2d: Consensus commits -> record transitions to StateSendSubmitted.
	// But ParentChain has NOT yet mined/confirmed the transfer (transferNotFound = true).
	// Must return false (waiting for parent chain, no RPC spam).
	record.State = StateSendSubmitted
	_ = store.Put(record)
	client.transferNotFound = true
	if worker.processPending() {
		t.Errorf("Expected false when waiting for parent chain confirmation, got true")
	}

	// Sub-case 2e: ParentChain mines the transfer (transferNotFound = false).
	// Must return true (progress: EventParentConfirmed proposed).
	client.transferNotFound = false
	if !worker.processPending() {
		t.Errorf("Expected true when parent chain confirms transfer, got false")
	}
	if len(proposedEvents) != 2 || proposedEvents[1].Type != EventParentConfirmed {
		t.Fatalf("Expected EventParentConfirmed proposed, got %v", proposedEvents)
	}

	// Case 3: "wrong nonce" where record is NOT found on parent chain -> pauses to let nonce settle, must return false
	msgID2 := common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	record2 := &MessageRecord{
		MessageID: msgID2,
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Sender:    common.HexToAddress("0xaaa"),
		Target:    common.HexToAddress("0xbbb"),
		Value:     big.NewInt(50),
		SourceSeq: 2,
	}
	_ = store.Put(record2)
	client.failNext = true
	client.failError = errors.New("float account: wrong nonce: got 2, want 3")
	client.transferNotFound = true
	if worker.processPending() {
		t.Errorf("Expected false on wrong nonce settling pause, got true")
	}
}

