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

// mockEvictableTrie implements EvictableStateTrie for eviction tests
type mockEvictableTrie struct {
	trie.StateTrie
	canEvict bool
}

func (m *mockEvictableTrie) CanEvict() bool {
	return m.canEvict
}

func TestDirtyContractsTracking_OnlyProcessesDirtyOnCommit(t *testing.T) {
	origBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	defer trie.SetStateBackend(origBackend)

	accStorage := newTestDB()
	accTrie, err := trie.New(common.Hash{}, accStorage, false)
	require.NoError(t, err)
	asDB := account_state_db.NewAccountStateDB(accTrie, accStorage)

	contractA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	contractB := common.HexToAddress("0x2222222222222222222222222222222222222222")

	accA := state.NewAccountState(contractA)
	accA.SetSmartContractState(state.NewEmptySmartContractState())
	asDB.SetState(accA)

	accB := state.NewAccountState(contractB)
	accB.SetSmartContractState(state.NewEmptySmartContractState())
	asDB.SetState(accB)

	scStorage := newTestDB()
	db := NewSmartContractDB(newTestDB(), scStorage, asDB, nil)

	// Pre-load both contracts into cache
	_, err = db.loadStorageTrie(contractA)
	require.NoError(t, err)
	_, err = db.loadStorageTrie(contractB)
	require.NoError(t, err)

	// Neither is dirty yet
	var dirtyCount int
	db.dirtyStorageContracts.Range(func(key, value any) bool {
		dirtyCount++
		return true
	})
	require.Equal(t, 0, dirtyCount, "Initially no contracts should be dirty")

	// Mutate only contract A
	slotKey := []byte("slot_a")
	slotVal := []byte("val_a")
	require.NoError(t, db.SetStorageValue(contractA, slotKey, slotVal))

	// Only contract A should be dirty
	dirtyCount = 0
	db.dirtyStorageContracts.Range(func(key, value any) bool {
		dirtyCount++
		require.Equal(t, contractA, key.(common.Address))
		return true
	})
	require.Equal(t, 1, dirtyCount, "Only modified contract should be marked dirty")

	// LateBindRoots & CommitAllStorage
	require.NoError(t, db.LateBindRoots())
	_, err = db.CommitAllStorage()
	require.NoError(t, err)

	// After commit, dirtyStorageContracts should be empty
	dirtyCount = 0
	db.dirtyStorageContracts.Range(func(key, value any) bool {
		dirtyCount++
		return true
	})
	require.Equal(t, 0, dirtyCount, "Dirty markers should be cleared after successful commit")

	// Both contracts still in cache
	_, hasA := db.smartContractStorageTries.Load(contractA)
	_, hasB := db.smartContractStorageTries.Load(contractB)
	require.True(t, hasA)
	require.True(t, hasB)
}

func TestEvictCleanTriesIfNeeded_EvictsOnlyDurableTries(t *testing.T) {
	origBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	defer trie.SetStateBackend(origBackend)

	db := NewSmartContractDB(newTestDB(), newTestDB(), nil, nil)

	// Populate 550 dummy clean tries (limit is 512)
	totalTries := 550
	for i := 0; i < totalTries; i++ {
		var addr common.Address
		addr[18] = byte(i >> 8)
		addr[19] = byte(i)

		// Some tries can evict, some cannot (e.g. pending durability)
		canEvict := (i%2 == 0)
		mockTrie := &mockEvictableTrie{canEvict: canEvict}
		db.smartContractStorageTries.Store(addr, mockTrie)
		db.lastAccessSeq.Store(addr, uint64(i+1))
	}

	// Also mark address 0 as dirty
	var dirtyAddr common.Address
	dirtyAddr[18] = 0
	dirtyAddr[19] = 0
	db.dirtyStorageContracts.Store(dirtyAddr, true)

	db.evictCleanTriesIfNeeded()

	// dirtyAddr must NOT be evicted even though it has the lowest seq (0) and canEvict is true
	_, hasDirty := db.smartContractStorageTries.Load(dirtyAddr)
	require.True(t, hasDirty, "Dirty contract must never be evicted")

	// Tries that had canEvict == false must NOT be evicted
	for i := 1; i < 20; i += 2 {
		var addr common.Address
		addr[18] = byte(i >> 8)
		addr[19] = byte(i)
		_, exists := db.smartContractStorageTries.Load(addr)
		require.True(t, exists, "Trie with canEvict == false must never be evicted")
	}

	// Some clean, evictable tries must have been evicted to reduce cache size
	var finalCount int
	db.smartContractStorageTries.Range(func(_, _ any) bool {
		finalCount++
		return true
	})
	require.Less(t, finalCount, totalTries, "Cache size must be reduced by evicting clean tries")
}

func TestNomtPersistenceTicket_AsyncDurabilityAndEvictionSafe(t *testing.T) {
	origBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendNOMT)
	defer func() {
		trie.SetStateBackend(origBackend)
		trie.CloseNomtDB()
	}()

	tempDir := t.TempDir()
	err := trie.InitNomtDB(tempDir, 1, 16, 16)
	require.NoError(t, err)

	accStorage := newTestDB()
	accTrie, err := trie.NewStateTrie(common.Hash{}, accStorage, false)
	require.NoError(t, err)
	asDB := account_state_db.NewAccountStateDB(accTrie, accStorage)

	contractAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")
	accState := state.NewAccountState(contractAddr)
	accState.SetSmartContractState(state.NewEmptySmartContractState())
	asDB.SetState(accState)

	scStorage := newTestDB()
	db1 := NewSmartContractDB(newTestDB(), scStorage, asDB, nil)

	slotKey := []byte("nomt_slot_key")
	slotVal := []byte("nomt_slot_val_42")

	// Step 1: Write slot
	require.NoError(t, db1.SetStorageValue(contractAddr, slotKey, slotVal))

	// Step 2: LateBindRoots creates batchTicket and sets pending session
	require.NoError(t, db1.LateBindRoots())

	// Step 3: CommitAllStorage extracts payload with pending ticket
	payloadIface, err := db1.CommitAllStorage()
	require.NoError(t, err)
	require.NotNil(t, payloadIface)

	payload, ok := payloadIface.(*trie.NomtPayload)
	require.True(t, ok)

	// Verify that CanEvict() is FALSE before CommitAsync finishes
	cachedTrieIface, ok := db1.smartContractStorageTries.Load(contractAddr)
	require.True(t, ok)
	evictable, ok := cachedTrieIface.(trie.EvictableStateTrie)
	require.True(t, ok)
	require.False(t, evictable.CanEvict(), "Trie cannot be evicted while persistence ticket is pending")

	// Step 4: Clone DB (simulating Block N+1 starting concurrently)
	dbClone := db1.Copy(asDB)

	// Step 5: Concurrently execute Block N+1 reading storage and Block N persisting to disk
	// Block N+1 reads from clone: must see committing overlay value
	readVal, readOk := dbClone.StorageValue(contractAddr, slotKey)
	require.True(t, readOk, "Cloned DB must be able to read slot")
	require.Equal(t, slotVal, readVal, "Cloned DB must read correct value even before CommitAsync finishes")

	// Block N commits asynchronously
	payload.CommitAsync()
	nomtTrie, isNomt := cachedTrieIface.(*trie.NomtStateTrie)
	require.True(t, isNomt)
	require.NoError(t, nomtTrie.WaitCommitPayload())

	// Step 6: After CommitAsync finishes, persistence ticket is Durable
	require.True(t, evictable.CanEvict(), "Trie can now be evicted after persistence ticket is durable")

	// Step 7: Simulate cache eviction on db1
	db1.smartContractStorageTries.Delete(contractAddr)

	// Step 8: Cold read from disk on db1
	coldVal, coldOk := db1.StorageValue(contractAddr, slotKey)
	require.True(t, coldOk, "Cold read from disk must succeed after eviction")
	require.Equal(t, slotVal, coldVal, "Cold read must return exact committed value from disk")
}

func TestNomtStateTrie_AbortedSessionNeverBecomesDurable(t *testing.T) {
	origBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendNOMT)
	defer func() {
		trie.SetStateBackend(origBackend)
		trie.CloseNomtDB()
	}()

	tempDir := t.TempDir()
	require.NoError(t, trie.InitNomtDB(tempDir, 1, 16, 16))

	accStorage := newTestDB()
	accTrie, err := trie.NewStateTrie(common.Hash{}, accStorage, false)
	require.NoError(t, err)
	asDB := account_state_db.NewAccountStateDB(accTrie, accStorage)

	contractAddr := common.HexToAddress("0x7777777777777777777777777777777777777777")
	accState := state.NewAccountState(contractAddr)
	accState.SetSmartContractState(state.NewEmptySmartContractState())
	asDB.SetState(accState)

	db := NewSmartContractDB(newTestDB(), newTestDB(), asDB, nil)

	slotKey := []byte("aborted_slot")
	slotVal := []byte("aborted_val")
	require.NoError(t, db.SetStorageValue(contractAddr, slotKey, slotVal))
	require.NoError(t, db.LateBindRoots())

	// Grab the cached trie before abort
	cachedTrieIface, ok := db.smartContractStorageTries.Load(contractAddr)
	require.True(t, ok)
	nomtTrie := cachedTrieIface.(*trie.NomtStateTrie)

	// Now abort the pending session (e.g. speculative conflict)
	db.AbortPending()

	// Verify that ticket is marked Failed and CanEvict is FALSE
	require.False(t, nomtTrie.CanEvict(), "Aborted trie must never be evictable")

	// Call Close() / CommitPayload() on the aborted trie
	err = nomtTrie.CommitPayload()
	require.Error(t, err, "CommitPayload on aborted session with committing data must return error")

	// Verify ticket is STILL NOT durable
	require.False(t, nomtTrie.CanEvict(), "Aborted session must never become Durable after Close/CommitPayload")
}

func TestCommitAllStorage_MissingDirtyTrieDoesNotClearMarkers(t *testing.T) {
	origBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	defer trie.SetStateBackend(origBackend)

	db := NewSmartContractDB(newTestDB(), newTestDB(), nil, nil)

	addrMissing := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addrOther := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Mark both dirty
	db.dirtyStorageContracts.Store(addrMissing, true)
	db.dirtyStorageContracts.Store(addrOther, true)

	// Leave addrMissing absent from smartContractStorageTries
	// CommitAllStorage should fail because addrMissing is missing
	_, err := db.CommitAllStorage()
	require.Error(t, err)
	require.Contains(t, err.Error(), "dirty storage trie missing")

	// FAIL-CLOSED VERIFICATION: dirty markers must NOT have been cleared!
	_, dirtyMissing := db.dirtyStorageContracts.Load(addrMissing)
	_, dirtyOther := db.dirtyStorageContracts.Load(addrOther)
	require.True(t, dirtyMissing, "Dirty marker for missing contract must be retained on failure")
	require.True(t, dirtyOther, "Dirty marker for other contract must be retained on failure")
}
