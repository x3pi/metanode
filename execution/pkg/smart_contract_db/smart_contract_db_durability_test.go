package smart_contract_db

import (
	"errors"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// durableRecordingDB records the order of BatchPut and SyncDurable calls so a test can prove that
// contract bytecode is made durable right after it is written (a buffered store would otherwise lose
// it on a crash).
type durableRecordingDB struct {
	*testDB
	mu      sync.Mutex
	events  []string
	syncErr error
}

func newDurableRecordingDB() *durableRecordingDB {
	return &durableRecordingDB{testDB: newTestDB()}
}

func (d *durableRecordingDB) BatchPut(pairs [][2][]byte) error {
	d.mu.Lock()
	d.events = append(d.events, "batchput")
	d.mu.Unlock()
	return d.testDB.BatchPut(pairs)
}

func (d *durableRecordingDB) SyncDurable() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, "sync")
	return d.syncErr
}

func (d *durableRecordingDB) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.events...)
}

func TestCommit_SyncsCodeStorageAfterWritingBytecode(t *testing.T) {
	code := newDurableRecordingDB()
	db := NewSmartContractDB(code, newTestDB(), nil, nil)

	addr := common.HexToAddress("0x1000000000000000000000000000000000000001")
	codeHash := common.HexToHash("0xc0de")
	db.SetCode(addr, codeHash, []byte{0x60, 0x80, 0x60, 0x40})

	_, commitErr := db.Commit()
	require.NoError(t, commitErr)
	require.Equal(t, []string{"batchput", "sync"}, code.recorded(),
		"bytecode must be written and then made durable, in that order, exactly once")

	stored, err := code.Get(codeHash.Bytes())
	require.NoError(t, err)
	require.Equal(t, []byte{0x60, 0x80, 0x60, 0x40}, stored)
}

func TestCommit_PropagatesCodeStorageSyncError(t *testing.T) {
	code := newDurableRecordingDB()
	code.syncErr = errors.New("fsync failed")
	db := NewSmartContractDB(code, newTestDB(), nil, nil)
	db.SetCode(common.HexToAddress("0x1000000000000000000000000000000000000002"), common.HexToHash("0xc0de02"), []byte{0x01})

	_, err := db.Commit()
	require.Error(t, err, "a failed sync must fail the commit rather than report a durable block")
	require.Contains(t, err.Error(), "fsync failed")
}

func TestCommit_DoesNotSyncCodeStorageWhenNoNewCode(t *testing.T) {
	code := newDurableRecordingDB()
	db := NewSmartContractDB(code, newTestDB(), nil, nil)

	_, commitErr := db.Commit()
	require.NoError(t, commitErr)
	require.Empty(t, code.recorded(), "blocks without new bytecode must not pay for an fsync")
}

func TestCommit_SyncsEventLogStorageAfterWritingLogs(t *testing.T) {
	code := newDurableRecordingDB()
	eventStorage := newDurableRecordingDB()
	db := NewSmartContractDB(code, eventStorage, nil, nil)

	addr := common.HexToAddress("0x2000000000000000000000000000000000000001")
	txHash := common.HexToHash("0xaaaa")
	log := smart_contract.NewEventLog(txHash, addr, []byte("deposit"), [][]byte{[]byte("topic1")})
	db.AddEventLogs([]types.EventLog{log})

	_, commitErr := db.Commit()
	require.NoError(t, commitErr)
	require.Equal(t, []string{"batchput", "sync"}, eventStorage.recorded(),
		"event logs must be written and then made durable via SyncDurable, in that order")
}

func TestCommit_PropagatesEventLogStorageSyncError(t *testing.T) {
	code := newDurableRecordingDB()
	eventStorage := newDurableRecordingDB()
	eventStorage.syncErr = errors.New("event log fsync failed")
	db := NewSmartContractDB(code, eventStorage, nil, nil)

	addr := common.HexToAddress("0x2000000000000000000000000000000000000002")
	txHash := common.HexToHash("0xbbbb")
	log := smart_contract.NewEventLog(txHash, addr, []byte("transfer"), [][]byte{[]byte("topic1")})
	db.AddEventLogs([]types.EventLog{log})

	_, err := db.Commit()
	require.Error(t, err, "a failed event log sync must fail the commit")
	require.Contains(t, err.Error(), "event log fsync failed")
}

func TestCommit_DoesNotSyncEventLogStorageWhenNoLogs(t *testing.T) {
	code := newDurableRecordingDB()
	eventStorage := newDurableRecordingDB()
	db := NewSmartContractDB(code, eventStorage, nil, nil)

	_, commitErr := db.Commit()
	require.NoError(t, commitErr)
	require.Empty(t, eventStorage.recorded(), "blocks without event logs must not pay for an fsync")
}

// TestCommitAllStorage_CrashRecoveryAndCacheDurability verifies:
// 1. Storage mutations are kept in RAM (`smartContractStorageTries`) after CommitAllStorage for fast pipeline/speculative execution.
// 2. An abrupt crash and restart (clearing all RAM) preserves 100% of committed storage data loaded from disk.
// 3. New writes after restart correctly update the storage state without data corruption.
func TestCommitAllStorage_CrashRecoveryAndCacheDurability(t *testing.T) {
	origBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	defer trie.SetStateBackend(origBackend)

	accStorage := newTestDB()
	accTrie, err := trie.New(common.Hash{}, accStorage, false)
	require.NoError(t, err)
	asDB := account_state_db.NewAccountStateDB(accTrie, accStorage)

	contractAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	accState := state.NewAccountState(contractAddr)
	accState.SetSmartContractState(state.NewEmptySmartContractState())
	asDB.SetState(accState)

	// Step 1: Simulate active node running Block 1
	scStorage := newTestDB()
	db1 := NewSmartContractDB(newTestDB(), scStorage, asDB, nil)

	// Set contract storage slot
	slotKey := []byte("balance_user_alice")
	slotVal := []byte("1000000000000000000") // 1 ETH
	require.NoError(t, db1.SetStorageValue(contractAddr, slotKey, slotVal))

	// Compute storage roots and late-bind into account state
	require.NoError(t, db1.LateBindRoots())

	// Commit Block 1 storage
	_, commitErr := db1.CommitAllStorage()
	require.NoError(t, commitErr)

	// In MPT backend, late-bind roots or CommitAllStorage updates the root
	updatedAcc, err := asDB.AccountState(contractAddr)
	require.NoError(t, err)
	storageRootBlock1 := updatedAcc.SmartContractState().StorageRoot()
	require.NotEqual(t, common.Hash{}, storageRootBlock1, "StorageRoot must be non-zero after commit")

	// Verify in-memory cache hit (Block 1 RAM cache has the trie stored)
	cachedTrieIface, inCache := db1.smartContractStorageTries.Load(contractAddr)
	require.True(t, inCache, "Committed trie must remain in RAM cache for subsequent pipeline blocks")
	require.NotNil(t, cachedTrieIface)

	readFromCache, ok := db1.StorageValue(contractAddr, slotKey)
	require.True(t, ok)
	require.Equal(t, slotVal, readFromCache, "In-memory cache read must return committed value")

	// Step 2: SIMULATE ABRUPT CRASH & RESTART
	// Entire RAM of db1 is destroyed (discarded).
	// A new node process starts up with empty RAM cache.
	dbRestarted := NewSmartContractDB(newTestDB(), scStorage, asDB, nil)

	// Verify that RAM cache is empty on fresh start
	_, inCacheRestart := dbRestarted.smartContractStorageTries.Load(contractAddr)
	require.False(t, inCacheRestart, "On node restart, in-memory trie cache must start empty")

	// Read storage slot on restarted node: must load from disk via StorageRoot
	readAfterRestart, ok := dbRestarted.StorageValue(contractAddr, slotKey)
	require.True(t, ok, "Storage must be successfully read after crash and restart")
	require.Equal(t, slotVal, readAfterRestart, "Data read after restart must match exact committed value (Zero Data Loss)")

	// Now that it was read, verify that it was cached into RAM for subsequent reads
	_, inCacheAfterRead := dbRestarted.smartContractStorageTries.Load(contractAddr)
	require.True(t, inCacheAfterRead, "Storage trie should now be cached in RAM on the restarted node")

	// Step 3: Write second transaction after restart (Block 2)
	slotKey2 := []byte("balance_user_bob")
	slotVal2 := []byte("2500000000000000000") // 2.5 ETH
	require.NoError(t, dbRestarted.SetStorageValue(contractAddr, slotKey2, slotVal2))

	require.NoError(t, dbRestarted.LateBindRoots())
	_, commitErr2 := dbRestarted.CommitAllStorage()
	require.NoError(t, commitErr2)

	// Verify both slots are preserved after second commit
	val1, ok1 := dbRestarted.StorageValue(contractAddr, slotKey)
	require.True(t, ok1)
	require.Equal(t, slotVal, val1)

	val2, ok2 := dbRestarted.StorageValue(contractAddr, slotKey2)
	require.True(t, ok2)
	require.Equal(t, slotVal2, val2)
}
