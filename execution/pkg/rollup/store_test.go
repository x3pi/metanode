package rollup

import (
	"math/big"
	"testing"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

type mockDB struct {
	mu   sync.Mutex
	data map[common.Address]map[common.Hash][]byte
}

func (m *mockDB) StorageValue(address common.Address, key common.Hash) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[address] == nil {
		return nil, false
	}
	val, ok := m.data[address][key]
	return val, ok
}

func (m *mockDB) SetStorageValue(address common.Address, key common.Hash, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[address] == nil {
		m.data[address] = make(map[common.Hash][]byte)
	}
	m.data[address][key] = value
}

func TestDBStore(t *testing.T) {
	mock := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(mock)

	msgID1 := common.HexToHash("0x123")
	record1 := &MessageRecord{
		MessageID: msgID1,
		Role:      RoleSender,
		State:     StateLocalAppliedPendingSend,
		Sender:    common.HexToAddress("0xaaa"),
		Target:    common.HexToAddress("0xbbb"),
		Value:     big.NewInt(100),
	}

	err := store.Put(record1)
	if err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	rec, found, err := store.Get(msgID1)
	if err != nil || !found {
		t.Fatalf("Failed to get: %v", err)
	}
	if rec.State != StateLocalAppliedPendingSend {
		t.Errorf("Expected StateLocalAppliedPendingSend, got %v", rec.State)
	}

	nonTerminal, err := store.ScanNonTerminal()
	if err != nil {
		t.Fatalf("ScanNonTerminal err: %v", err)
	}
	if len(nonTerminal) != 1 {
		t.Errorf("Expected 1 non-terminal record, got %d", len(nonTerminal))
	}

	// Update to terminal
	record1.State = StateConfirmedSuccess
	_ = store.Put(record1)

	nonTerminal, _ = store.ScanNonTerminal()
	if len(nonTerminal) != 0 {
		t.Errorf("Expected 0 non-terminal record, got %d", len(nonTerminal))
	}
}
