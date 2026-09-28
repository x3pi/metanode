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

type mockParentChainClient struct {
	sentTransfers  []common.Hash
	failNext       bool
	claimedOutcome parentchain.FloatOutcome
	// found=true, matching this mock's original behavior before the field existed).
	transferNotFound bool
	failError        error
}

func (m *mockParentChainClient) SendDepositToFloat(
	pubKey cm.PublicKey,
	destChainID uint64,
	sender, target common.Address,
	amount *big.Int,
) (common.Hash, error) {
	return common.Hash{}, nil
}

func (m *mockParentChainClient) SendTransferFloat(
	pubKey, destPubKey cm.PublicKey,
	destChainID uint64,
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
