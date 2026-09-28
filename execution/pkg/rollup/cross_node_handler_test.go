package rollup

import (
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

type mockAccountStateDB struct {
	mu       sync.Mutex
	balances map[common.Address]*big.Int
	nonces   map[common.Address]uint64
}

func newMockAccountStateDB() *mockAccountStateDB {
	return &mockAccountStateDB{
		balances: make(map[common.Address]*big.Int),
		nonces:   make(map[common.Address]uint64),
	}
}

func (m *mockAccountStateDB) GetBalance(addr common.Address) *big.Int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bal, ok := m.balances[addr]; ok {
		return bal
	}
	return big.NewInt(0)
}

func (m *mockAccountStateDB) SubBalance(addr common.Address, amount *big.Int) {
	bal := m.GetBalance(addr) // locks inside GetBalance
	
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balances[addr] = new(big.Int).Sub(bal, amount)
}

func (m *mockAccountStateDB) GetNonce(addr common.Address) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nonces[addr]
}

func (m *mockAccountStateDB) SetNonce(addr common.Address, nonce uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nonces[addr] = nonce
}

func TestCrossNodeHandler(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()

	handler := NewCrossNodeHandler(cm.PublicKey{})

	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	value := big.NewInt(500)
	payloadHash := common.HexToHash("0x111")

	// 1. Insufficient balance
	_, err := handler.HandleTransfer(store, stateDB, cm.PublicKey{}, sender, target, value, payloadHash)
	if err == nil {
		t.Errorf("Expected insufficient balance error")
	}

	// Fund sender
	stateDB.balances[sender] = big.NewInt(1000)

	// 2. Successful transfer
	msgID, err := handler.HandleTransfer(store, stateDB, cm.PublicKey{}, sender, target, value, payloadHash)
	if err != nil {
		t.Fatalf("Expected success, got: %v", err)
	}

	// Verify balance deducted
	if stateDB.GetBalance(sender).Cmp(big.NewInt(500)) != 0 {
		t.Errorf("Expected balance 500, got %v", stateDB.GetBalance(sender))
	}

	// Verify nonce incremented
	if stateDB.GetNonce(sender) != 1 {
		t.Errorf("Expected nonce 1, got %d", stateDB.GetNonce(sender))
	}

	// Verify record saved
	record, found, err := store.Get(msgID)
	if err != nil || !found {
		t.Fatalf("Failed to find record: %v", err)
	}
	if record.State != StateLocalAppliedPendingSend {
		t.Errorf("Expected state LOCAL_APPLIED_PENDING_SEND, got %v", record.State)
	}

	// 3. Second transfer to exhaust balance
	_, err = handler.HandleTransfer(store, stateDB, cm.PublicKey{}, sender, target, big.NewInt(600), payloadHash)
	if err == nil {
		t.Errorf("Expected insufficient balance error for second transfer")
	}
	
	// Reload mock DB state (like a crash)
	store2 := NewDBStore(scDB)
	nonTerminal, err := store2.ScanNonTerminal()
	if err != nil {
		t.Fatalf("Scan err: %v", err)
	}
	if len(nonTerminal) != 1 {
		t.Errorf("Expected 1 non terminal record, got %d", len(nonTerminal))
	}
	if nonTerminal[0].MessageID != msgID {
		t.Errorf("Expected msgID match")
	}
}
