package rollup

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

func TestReclaimWorker(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()
	client := &mockParentChainClient{claimedOutcome: parentchain.FloatOutcomeReclaimed}
	kp := bls.GenerateKeyPair()

	worker := NewReclaimWorker(store, stateDB, client, kp)

	msgID := common.HexToHash("0x222")
	record := &MessageRecord{
		MessageID: msgID,
		Role:      RoleSender,
		State:     StateReclaimSubmitted,
		Sender:    common.HexToAddress("0xaaa"),
		Value:     big.NewInt(100),
	}
	_ = store.Put(record)

	// Test 1: Test Reclaim Eligible (transient error doesn't advance)
	msgID3 := common.HexToHash("0x333")
	record3 := &MessageRecord{
		MessageID: msgID3,
		Role:      RoleSender,
		State:     StateSentConfirmed, // Eligible for reclaim
		Sender:    common.HexToAddress("0xbbb"),
		Value:     big.NewInt(100),
	}
	_ = store.Put(record3)
	
	client.failNext = true
	client.failError = errors.New("network dropped")
	worker.processReclaims()
	
	rec3, _, _ := store.Get(msgID3)
	if rec3.State != StateSentConfirmed {
		t.Errorf("Expected StateSentConfirmed on failure, got %v", rec3.State)
	}

	// Test 2: Success or already resolved advances
	client.failNext = false // success
	worker.processReclaims()
	rec3, _, _ = store.Get(msgID3)
	if rec3.State != StateReclaimSubmitted {
		t.Errorf("Expected StateReclaimSubmitted on success, got %v", rec3.State)
	}

	// Process reclaims
	worker.processReclaims()

	// Should transition from StateReclaimSubmitted to StateConfirmedRefunded (assuming won=true)
	rec, _, _ := store.Get(msgID)
	if rec.State != StateConfirmedRefunded {
		t.Errorf("Expected StateConfirmedRefunded, got %v", rec.State)
	}

	// Balance should be credited back (100)
	if stateDB.GetBalance(record.Sender).Cmp(big.NewInt(100)) != 0 {
		t.Errorf("Expected balance 100, got %v", stateDB.GetBalance(record.Sender))
	}

	// Test RefundInTransit
	msgID2 := common.HexToHash("0x333")
	record2 := &MessageRecord{
		MessageID: msgID2,
		Role:      RoleSender,
		State:     StateRefundInTransit,
		Sender:    common.HexToAddress("0xbbb"),
		Value:     big.NewInt(200),
	}
	_ = store.Put(record2)

	worker.processReclaims()

	rec2, _, _ := store.Get(msgID2)
	if rec2.State != StateConfirmedRefunded {
		t.Errorf("Expected StateConfirmedRefunded, got %v", rec2.State)
	}
}
