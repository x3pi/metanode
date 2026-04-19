package smart_contract_db

import (
	"bytes"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// testDB — A simple in-memory DB that implements storage.Storage with
// variable-length keys (unlike MemoryDB which truncates to 32 bytes).
// ══════════════════════════════════════════════════════════════════════════════

type testDB struct {
	mu   sync.RWMutex
	data map[string][]byte // hex-encoded key → value
}

func newTestDB() *testDB {
	return &testDB{data: make(map[string][]byte)}
}

func (t *testDB) Get(key []byte) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	h := hex.EncodeToString(key)
	if v, ok := t.data[h]; ok {
		return v, nil
	}
	return nil, errors.New("key not found")
}

func (t *testDB) Put(key, value []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data[hex.EncodeToString(key)] = value
	return nil
}

func (t *testDB) Delete(key []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := hex.EncodeToString(key)
	if _, ok := t.data[h]; !ok {
		return errors.New("key not found")
	}
	delete(t.data, h)
	return nil
}

func (t *testDB) BatchPut(pairs [][2][]byte) error {
	for _, kv := range pairs {
		if err := t.Put(kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

func (t *testDB) BatchDelete(keys [][]byte) error {
	for _, k := range keys {
		if err := t.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func (t *testDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	hexPrefix := hex.EncodeToString(prefix)
	var results [][2][]byte
	// Collect and sort for deterministic output
	var keys []string
	for k := range t.data {
		if len(k) >= len(hexPrefix) && k[:len(hexPrefix)] == hexPrefix {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		rawKey, _ := hex.DecodeString(k)
		// Strip prefix from key (matching PebbleDB/ShardelDB behavior)
		strippedKey := rawKey[len(prefix):]
		results = append(results, [2][]byte{strippedKey, t.data[k]})
	}
	return results, nil
}

func (t *testDB) Close() error         { return nil }
func (t *testDB) Open() error          { return nil }
func (t *testDB) Flush() error         { return nil }
func (t *testDB) GetBackupPath() string { return "/tmp/testdb" }

// Compile-time check
var _ storage.Storage = (*testDB)(nil)

// ══════════════════════════════════════════════════════════════════════════════
// PrefixedStorage Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestPrefixedStorage_Isolation(t *testing.T) {
	// Two contracts sharing the same backing DB should NOT collide
	backingDB := newTestDB()

	addrA := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	addrB := common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	psA := NewPrefixedStorage(backingDB, addrA)
	psB := NewPrefixedStorage(backingDB, addrB)

	key := []byte("slot_0")
	valueA := []byte("contract_A_value")
	valueB := []byte("contract_B_value")

	// Write same key to both prefixed storages
	require.NoError(t, psA.Put(key, valueA))
	require.NoError(t, psB.Put(key, valueB))

	// Each should read its own value
	gotA, err := psA.Get(key)
	require.NoError(t, err)
	assert.Equal(t, valueA, gotA, "contract A should read its own value")

	gotB, err := psB.Get(key)
	require.NoError(t, err)
	assert.Equal(t, valueB, gotB, "contract B should read its own value")
}

func TestPrefixedStorage_Delete(t *testing.T) {
	backingDB := newTestDB()
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	ps := NewPrefixedStorage(backingDB, addr)

	key := []byte("to_delete")
	require.NoError(t, ps.Put(key, []byte("value")))

	// Verify exists
	val, err := ps.Get(key)
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)

	// Delete
	require.NoError(t, ps.Delete(key))

	// Should be gone
	_, err = ps.Get(key)
	assert.Error(t, err, "deleted key should not be found")
}

func TestPrefixedStorage_BatchPut(t *testing.T) {
	backingDB := newTestDB()
	addr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	ps := NewPrefixedStorage(backingDB, addr)

	pairs := [][2][]byte{
		{[]byte("k1"), []byte("v1")},
		{[]byte("k2"), []byte("v2")},
		{[]byte("k3"), []byte("v3")},
	}

	require.NoError(t, ps.BatchPut(pairs))

	for _, kv := range pairs {
		got, err := ps.Get(kv[0])
		require.NoError(t, err)
		assert.Equal(t, kv[1], got)
	}
}

func TestPrefixedStorage_PrefixScan(t *testing.T) {
	backingDB := newTestDB()
	addrA := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	addrB := common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	psA := NewPrefixedStorage(backingDB, addrA)
	psB := NewPrefixedStorage(backingDB, addrB)

	// Write entries with "fs:" prefix to both contracts
	require.NoError(t, psA.Put([]byte("fs:slot0"), []byte("A_val0")))
	require.NoError(t, psA.Put([]byte("fs:slot1"), []byte("A_val1")))
	require.NoError(t, psB.Put([]byte("fs:slot0"), []byte("B_val0")))
	require.NoError(t, psB.Put([]byte("fs:slot2"), []byte("B_val2")))

	// PrefixScan from contract A with "fs:" prefix
	pairsA, err := psA.PrefixScan([]byte("fs:"))
	require.NoError(t, err)
	assert.Equal(t, 2, len(pairsA), "contract A should have 2 'fs:' entries")

	// PrefixScan from contract B with "fs:" prefix
	pairsB, err := psB.PrefixScan([]byte("fs:"))
	require.NoError(t, err)
	assert.Equal(t, 2, len(pairsB), "contract B should have 2 'fs:' entries")

	// Verify that keys are properly stripped (returned keys should NOT contain contract address prefix)
	for _, kv := range pairsA {
		assert.True(t, bytes.HasPrefix(kv[0], []byte("slot")), "scanned key should start with 'slot' after stripping prefix: got %q", string(kv[0]))
	}
}

func TestPrefixedStorage_BatchDelete(t *testing.T) {
	backingDB := newTestDB()
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	ps := NewPrefixedStorage(backingDB, addr)

	pairs := [][2][]byte{
		{[]byte("k1"), []byte("v1")},
		{[]byte("k2"), []byte("v2")},
	}
	require.NoError(t, ps.BatchPut(pairs))

	// Delete k1
	require.NoError(t, ps.BatchDelete([][]byte{[]byte("k1")}))

	_, err := ps.Get([]byte("k1"))
	assert.Error(t, err, "k1 should be deleted")

	val, err := ps.Get([]byte("k2"))
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), val, "k2 should still exist")
}

func TestPrefixedStorage_Passthrough(t *testing.T) {
	backingDB := newTestDB()
	addr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	ps := NewPrefixedStorage(backingDB, addr)

	// Close/Open/Flush should not error
	assert.NoError(t, ps.Close())
	assert.NoError(t, ps.Open())
	assert.NoError(t, ps.Flush())

	// GetBackupPath delegates to inner
	assert.Equal(t, "/tmp/testdb", ps.GetBackupPath())
}

func TestPrefixedStorage_CrossContractIsolation_WithFlatKeys(t *testing.T) {
	// Simulates the exact scenario that would cause collision without the fix:
	// Two contracts writing fs:0000...0000 (flat key for storage slot 0)
	backingDB := newTestDB()

	addrA := common.HexToAddress("0x0000000000000000000000000000000000000001")
	addrB := common.HexToAddress("0x0000000000000000000000000000000000000002")

	psA := NewPrefixedStorage(backingDB, addrA)
	psB := NewPrefixedStorage(backingDB, addrB)

	// This simulates what FlatStateTrie.Update does: makeFlatKey(slot) = "fs:" + slot_bytes
	flatKey := append([]byte("fs:"), make([]byte, 32)...) // "fs:" + 32-byte slot 0

	require.NoError(t, psA.Put(flatKey, []byte("owner_A")))
	require.NoError(t, psB.Put(flatKey, []byte("owner_B")))

	// Without PrefixedStorage, these would COLLIDE.
	// With PrefixedStorage, they MUST be isolated.
	gotA, err := psA.Get(flatKey)
	require.NoError(t, err)
	assert.Equal(t, []byte("owner_A"), gotA, "CRITICAL: contract A must read its own owner")

	gotB, err := psB.Get(flatKey)
	require.NoError(t, err)
	assert.Equal(t, []byte("owner_B"), gotB, "CRITICAL: contract B must read its own owner")
}
