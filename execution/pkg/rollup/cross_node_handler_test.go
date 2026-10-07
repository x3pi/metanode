package rollup

import (
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

type mockAccountStateDB struct {
	mu        sync.Mutex
	balances  map[common.Address]*big.Int
	nonces    map[common.Address]uint64
	parentReg map[common.Address]bool
}

func newMockAccountStateDB() *mockAccountStateDB {
	return &mockAccountStateDB{
		balances:  make(map[common.Address]*big.Int),
		nonces:    make(map[common.Address]uint64),
		parentReg: make(map[common.Address]bool),
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

func (m *mockAccountStateDB) AddBalance(addr common.Address, amount *big.Int) error {
	bal := m.GetBalance(addr) // locks inside GetBalance

	m.mu.Lock()
	defer m.mu.Unlock()
	m.balances[addr] = new(big.Int).Add(bal, amount)
	return nil
}

func (m *mockAccountStateDB) SubBalance(addr common.Address, amount *big.Int) {
	// Mirrors the real AccountStateDB.SubBalance's guard (pkg/account_state_db/
	// account_state_db_mutations.go): a non-positive amount is a no-op. Without this, the mock
	// silently accepted `SubBalance(addr, negativeAmount)` as a way to credit balance, which is
	// exactly the real bug ActionCreditLocal handling had (see cross_node_handler.go) -- masking
	// it here would let that regression slip back in unnoticed.
	if amount == nil || amount.Sign() <= 0 {
		return
	}
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

func (m *mockAccountStateDB) GetParentRegistered(addr common.Address) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parentReg == nil {
		return false
	}
	return m.parentReg[addr]
}

func (m *mockAccountStateDB) SetParentRegistered(addr common.Address, registered bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parentReg == nil {
		m.parentReg = make(map[common.Address]bool)
	}
	m.parentReg[addr] = registered
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

	// Verify balance deducted: value (500) AND the transfer fee (100), the same total the Parent Chain removes from the
	// cluster's float.
	if stateDB.GetBalance(sender).Cmp(big.NewInt(400)) != 0 {
		t.Errorf("Expected balance 400 (1000 - 500 value - 100 fee), got %v", stateDB.GetBalance(sender))
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

type mockNilBalanceAccountStateDB struct {
	mockAccountStateDB
}

func (m *mockNilBalanceAccountStateDB) GetBalance(addr common.Address) *big.Int {
	// Explicitly returns nil to simulate accounts with uninitialized/nil balance
	return nil
}

func TestCrossNodeHandler_NilBalanceHandling(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := &mockNilBalanceAccountStateDB{
		mockAccountStateDB: *newMockAccountStateDB(),
	}

	handler := NewCrossNodeHandler(cm.PublicKey{})
	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	value := big.NewInt(500)
	payloadHash := common.HexToHash("0x111")

	// Must NOT panic with nil pointer dereference, and must return insufficient balance error
	msgID, err := handler.HandleTransfer(store, stateDB, cm.PublicKey{}, sender, target, value, payloadHash)
	if err == nil {
		t.Fatalf("Expected error when balance is nil/zero, got success with msgID: %v", msgID)
	}
	if err.Error() != "insufficient balance: have 0, need 600 (value 500 + fee 100)" {
		t.Errorf("Unexpected error message: %v", err)
	}
}

func TestCrossNodeHandler_NilGuards(t *testing.T) {
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	stateDB := newMockAccountStateDB()
	handler := NewCrossNodeHandler(cm.PublicKey{})

	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	value := big.NewInt(100)
	payloadHash := common.HexToHash("0x111")

	// 1. Nil store
	if _, err := handler.HandleTransfer(nil, stateDB, cm.PublicKey{}, sender, target, value, payloadHash); err == nil || err.Error() != "store is nil" {
		t.Errorf("Expected 'store is nil', got: %v", err)
	}

	// 2. Nil stateDB
	if _, err := handler.HandleTransfer(store, nil, cm.PublicKey{}, sender, target, value, payloadHash); err == nil || err.Error() != "stateDB is nil" {
		t.Errorf("Expected 'stateDB is nil', got: %v", err)
	}

	// 3. Nil value
	if _, err := handler.HandleTransfer(store, stateDB, cm.PublicKey{}, sender, target, nil, payloadHash); err == nil || err.Error() != "invalid value" {
		t.Errorf("Expected 'invalid value', got: %v", err)
	}

	// 4. Zero or negative value
	if _, err := handler.HandleTransfer(store, stateDB, cm.PublicKey{}, sender, target, big.NewInt(0), payloadHash); err == nil || err.Error() != "invalid value" {
		t.Errorf("Expected 'invalid value', got: %v", err)
	}

	// 5. HandleSystemEvent nil guards
	event := Event{Type: EventCreditObserved}
	if err := handler.HandleSystemEvent(nil, store, stateDB, event, common.Hash{}, 0, cm.PublicKey{}, cm.PublicKey{}, common.Hash{}); err == nil || err.Error() != "readStore is nil" {
		t.Errorf("Expected 'readStore is nil', got: %v", err)
	}
	if err := handler.HandleSystemEvent(store, nil, stateDB, event, common.Hash{}, 0, cm.PublicKey{}, cm.PublicKey{}, common.Hash{}); err == nil || err.Error() != "writeStore is nil" {
		t.Errorf("Expected 'writeStore is nil', got: %v", err)
	}
	if err := handler.HandleSystemEvent(store, store, nil, event, common.Hash{}, 0, cm.PublicKey{}, cm.PublicKey{}, common.Hash{}); err == nil || err.Error() != "stateDB is nil" {
		t.Errorf("Expected 'stateDB is nil', got: %v", err)
	}
}

