package rollup

import (
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
