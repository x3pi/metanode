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

	// Test 2: processMarkClaimedPendingCredit
	worker.pollAndProcess() // This will process the pending record
	
	rec, _, _ = store.Get(msgID)
	if rec.State != StateCredited {
		t.Errorf("Expected CREDITED, got %v", rec.State)
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
