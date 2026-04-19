package mapping_db

import (
	"errors"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// mockStorage implements storage.Storage for testing
type mockStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMockStorage() *mockStorage {
	return &mockStorage{data: make(map[string][]byte)}
}

func (m *mockStorage) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.data[string(key)]
	if !ok {
		return nil, errors.New("not found")
	}
	return val, nil
}

func (m *mockStorage) Put(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[string(key)] = value
	return nil
}

func (m *mockStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *mockStorage) BatchPut(pairs [][2][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pair := range pairs {
		m.data[string(pair[0])] = pair[1]
	}
	return nil
}

func (m *mockStorage) Close() error          { return nil }
func (m *mockStorage) Flush() error          { return nil }
func (m *mockStorage) Open() error           { return nil }
func (m *mockStorage) GetBackupPath() string { return "" }
func (m *mockStorage) PrefixScan(prefix []byte) ([][2][]byte, error) { return nil, nil }
func (m *mockStorage) BatchDelete(keys [][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		delete(m.data, string(key))
	}
	return nil
}

func resetSingleton() {
	mappingDbInstance = nil
	once = sync.Once{}
}

func TestNewMappingDb(t *testing.T) {
	resetSingleton()
	store := newMockStorage()
	db := NewMappingDb(store)
	if db == nil {
		t.Fatal("NewMappingDb returned nil")
	}
}

func TestReturnDB(t *testing.T) {
	resetSingleton()
	store := newMockStorage()
	db := NewMappingDb(store)
	if db.ReturnDB() == nil {
		t.Fatal("ReturnDB returned nil")
	}
}

func TestSaveAndGetBlockHash(t *testing.T) {
	resetSingleton()
	store := newMockStorage()
	db := NewMappingDb(store)

	expectedHash := common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	err := db.SaveBlockNumberToHash(42, expectedHash)
	if err != nil {
		t.Fatalf("SaveBlockNumberToHash failed: %v", err)
	}

	gotHash, found := db.GetBlockHashByNumber(42)
	if !found {
		t.Fatal("GetBlockHashByNumber returned false for existing entry")
	}
	if gotHash != expectedHash {
		t.Fatalf("hash mismatch: got %s, expected %s", gotHash.Hex(), expectedHash.Hex())
	}
}

func TestGetBlockHash_NotFound(t *testing.T) {
	resetSingleton()
	store := newMockStorage()
	db := NewMappingDb(store)

	_, found := db.GetBlockHashByNumber(999)
	if found {
		t.Fatal("expected false for non-existent block number")
	}
}

func TestSaveOverwrite(t *testing.T) {
	resetSingleton()
	store := newMockStorage()
	db := NewMappingDb(store)

	hash1 := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	hash2 := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	_ = db.SaveBlockNumberToHash(1, hash1)
	_ = db.SaveBlockNumberToHash(1, hash2)

	got, found := db.GetBlockHashByNumber(1)
	if !found {
		t.Fatal("should find overwritten entry")
	}
	if got != hash2 {
		t.Fatalf("expected hash2 after overwrite, got %s", got.Hex())
	}
}

func TestMultipleBlocks(t *testing.T) {
	resetSingleton()
	store := newMockStorage()
	db := NewMappingDb(store)

	for i := uint64(0); i < 10; i++ {
		hash := common.BigToHash(common.Big0)
		hash[31] = byte(i)
		err := db.SaveBlockNumberToHash(i, hash)
		if err != nil {
			t.Fatalf("failed to save block %d: %v", i, err)
		}
	}

	for i := uint64(0); i < 10; i++ {
		_, found := db.GetBlockHashByNumber(i)
		if !found {
			t.Fatalf("block %d not found", i)
		}
	}
}
