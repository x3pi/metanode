package state

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
)

func TestSmartContractState_Unmarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name: "valid protobuf data",
			input: common.FromHex(
				"0a300000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001a20ae779dfa8e07968602d8ee78cf4b925aec2f1431b327e4ee98e995f4f41e6e772220c8805329615a3ad861c7fb08ea437800eb61a588e64f3e4e4a62d7fba41bdcad3a14da7284fac5e804f8b9d71aa39310f0f86776b51d",
			),
			wantErr: false,
		},
		{
			name:    "empty data succeeds (empty protobuf)",
			input:   []byte{},
			wantErr: false,
		},
		{
			name:    "invalid protobuf data",
			input:   []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss := &SmartContractState{}
			err := ss.Unmarshal(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("SmartContractState.Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSmartContractState_MarshalUnmarshalRoundTrip(t *testing.T) {
	pubKey := p_common.PubkeyFromBytes(make([]byte, 48))
	addr := common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d")
	codeHash := common.HexToHash("0xae779dfa8e07968602d8ee78cf4b925aec2f1431b327e4ee98e995f4f41e6e77")
	storageRoot := common.HexToHash("0xc8805329615a3ad861c7fb08ea437800eb61a588e64f3e4e4a62d7fba41bdcad")
	logsHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	original := NewSmartContractState(pubKey, addr, codeHash, storageRoot, logsHash)

	// Marshal
	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Unmarshal
	recovered := &SmartContractState{}
	err = recovered.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify fields
	if recovered.StorageAddress() != addr {
		t.Errorf("StorageAddress mismatch: got %v, want %v", recovered.StorageAddress(), addr)
	}
	if recovered.CodeHash() != codeHash {
		t.Errorf("CodeHash mismatch: got %v, want %v", recovered.CodeHash(), codeHash)
	}
	if recovered.StorageRoot() != storageRoot {
		t.Errorf("StorageRoot mismatch: got %v, want %v", recovered.StorageRoot(), storageRoot)
	}
	if recovered.LogsHash() != logsHash {
		t.Errorf("LogsHash mismatch: got %v, want %v", recovered.LogsHash(), logsHash)
	}
}

func TestSmartContractState_Getters_Setters(t *testing.T) {
	ss := &SmartContractState{}

	// Test SetCodeHash and CodeHash
	codeHash := common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	ss.SetCodeHash(codeHash)
	if ss.CodeHash() != codeHash {
		t.Errorf("CodeHash getter/setter mismatch")
	}

	// Test SetStorageRoot and StorageRoot
	storageRoot := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	ss.SetStorageRoot(storageRoot)
	if ss.StorageRoot() != storageRoot {
		t.Errorf("StorageRoot getter/setter mismatch")
	}

	// Test SetLogsHash and LogsHash
	logsHash := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	ss.SetLogsHash(logsHash)
	if ss.LogsHash() != logsHash {
		t.Errorf("LogsHash getter/setter mismatch")
	}

	// Test SetStorageAddress and StorageAddress
	addr := common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d")
	ss.SetStorageAddress(addr)
	if ss.StorageAddress() != addr {
		t.Errorf("StorageAddress getter/setter mismatch")
	}

	// Test SetMapFullDbHash and MapFullDbHash
	mapHash := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	ss.SetMapFullDbHash(mapHash)
	if ss.MapFullDbHash() != mapHash {
		t.Errorf("MapFullDbHash getter/setter mismatch")
	}

	// Test SetSimpleDbHash and SimpleDbHash
	simpleHash := common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444")
	ss.SetSimpleDbHash(simpleHash)
	if ss.SimpleDbHash() != simpleHash {
		t.Errorf("SimpleDbHash getter/setter mismatch")
	}
}

func TestSmartContractState_TrieDatabaseMap(t *testing.T) {
	ss := &SmartContractState{}

	// Initially nil
	if ss.TrieDatabaseMap() != nil {
		t.Error("TrieDatabaseMap should be nil initially")
	}

	// SetTrieDatabaseMapValue should create map if nil
	ss.SetTrieDatabaseMapValue("key1", []byte{0x01, 0x02})
	got := ss.GetTrieDatabaseMapValue("key1")
	if len(got) != 2 || got[0] != 0x01 || got[1] != 0x02 {
		t.Errorf("Get after Set failed: got %v", got)
	}

	// Add another key
	ss.SetTrieDatabaseMapValue("key2", []byte{0x03})
	if len(ss.TrieDatabaseMap()) != 2 {
		t.Errorf("TrieDatabaseMap should have 2 entries, got %d", len(ss.TrieDatabaseMap()))
	}

	// Delete key1
	ss.DeleteTrieDatabaseMapValue("key1")
	if ss.GetTrieDatabaseMapValue("key1") != nil {
		t.Error("key1 should be deleted")
	}

	// SetTrieDatabaseMap replaces entirely
	newMap := map[string][]byte{"a": {1}, "b": {2}}
	ss.SetTrieDatabaseMap(newMap)
	if len(ss.TrieDatabaseMap()) != 2 {
		t.Errorf("SetTrieDatabaseMap should replace map")
	}
}

func TestSmartContractState_Copy(t *testing.T) {
	pubKey := p_common.PubkeyFromBytes(make([]byte, 48))
	addr := common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d")
	codeHash := common.HexToHash("0xae779dfa8e07968602d8ee78cf4b925aec2f1431b327e4ee98e995f4f41e6e77")
	storageRoot := common.HexToHash("0xc8805329615a3ad861c7fb08ea437800eb61a588e64f3e4e4a62d7fba41bdcad")
	logsHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	original := NewSmartContractState(pubKey, addr, codeHash, storageRoot, logsHash)
	original.SetTrieDatabaseMapValue("test", []byte{1, 2, 3})

	copied := original.Copy()

	// Verify fields match
	if copied.StorageAddress() != original.StorageAddress() {
		t.Error("Copy: StorageAddress mismatch")
	}
	if copied.CodeHash() != original.CodeHash() {
		t.Error("Copy: CodeHash mismatch")
	}
	if copied.StorageRoot() != original.StorageRoot() {
		t.Error("Copy: StorageRoot mismatch")
	}
	if copied.LogsHash() != original.LogsHash() {
		t.Error("Copy: LogsHash mismatch")
	}

	// Verify trie database map is copied
	if len(copied.TrieDatabaseMap()) != 1 {
		t.Error("Copy: TrieDatabaseMap length mismatch")
	}

	// Mutating copy should not affect original
	copied.SetCodeHash(common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999"))
	if original.CodeHash() == copied.CodeHash() {
		t.Error("Mutating copy should not affect original")
	}
}

func TestSmartContractState_String(t *testing.T) {
	ss := NewEmptySmartContractState()
	s := ss.String()
	if len(s) == 0 {
		t.Error("String() should not be empty")
	}
}

func TestNewEmptySmartContractState(t *testing.T) {
	ss := NewEmptySmartContractState()
	if ss == nil {
		t.Fatal("NewEmptySmartContractState returned nil")
	}
	if ss.CodeHash() != (common.Hash{}) {
		t.Error("Empty state should have zero CodeHash")
	}
	if ss.StorageRoot() != (common.Hash{}) {
		t.Error("Empty state should have zero StorageRoot")
	}
}

func TestJsonSmartContractState_RoundTrip(t *testing.T) {
	pubKey := p_common.PubkeyFromBytes(make([]byte, 48))
	addr := common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d")
	codeHash := common.HexToHash("0xae779dfa8e07968602d8ee78cf4b925aec2f1431b327e4ee98e995f4f41e6e77")
	storageRoot := common.HexToHash("0xc8805329615a3ad861c7fb08ea437800eb61a588e64f3e4e4a62d7fba41bdcad")
	logsHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	original := NewSmartContractState(pubKey, addr, codeHash, storageRoot, logsHash)

	// Convert to JSON struct
	jsonSS := &JsonSmartContractState{}
	jsonSS.FromSmartContractState(original)

	// Convert back
	recovered := jsonSS.ToSmartContractState()

	if recovered.StorageAddress() != addr {
		t.Errorf("JSON round trip: StorageAddress mismatch: got %v, want %v", recovered.StorageAddress(), addr)
	}
	if recovered.CodeHash() != codeHash {
		t.Errorf("JSON round trip: CodeHash mismatch")
	}
	if recovered.StorageRoot() != storageRoot {
		t.Errorf("JSON round trip: StorageRoot mismatch")
	}
}
