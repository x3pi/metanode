package smart_contract_db

import (
	"bytes"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

type SmartContractDB struct {
	codeStorage     storage.Storage
	dbSmartContract storage.Storage

	accountStateDB types.AccountStateDB

	smartContractStorageTries sync.Map // Replaces map[common.Address]trie.StateTrie

	pendingCode      sync.Map // Replaces map[common.Hash][]byte
	pendingEventLogs sync.Map // Replaces map[common.Address][]types.EventLog
	lastAccessTime   sync.Map // map[common.Address]time.Time

	smartContractStorageBatch []byte
	codeBatchPut              []byte
	smartContractBatch        []byte
}

func (db *SmartContractDB) CodeStorage() storage.Storage {
	return db.codeStorage
}

func (db *SmartContractDB) DbSmartContract() storage.Storage {
	return db.dbSmartContract
}

func (db *SmartContractDB) SetSmartContractStorageBatch(batch []byte) {
	db.smartContractStorageBatch = batch
}

func (db *SmartContractDB) GetSmartContractStorageBatch() []byte {
	batch := db.smartContractStorageBatch
	db.smartContractStorageBatch = nil
	return batch
}

func (db *SmartContractDB) SetCodeBatchPut(batch []byte) {
	db.codeBatchPut = batch
}

func (db *SmartContractDB) GetCodeBatchPut() []byte {
	batch := db.codeBatchPut
	db.codeBatchPut = nil
	return batch
}

func (db *SmartContractDB) SetSmartContractBatch(batch []byte) {
	db.smartContractBatch = batch
}

func (db *SmartContractDB) GetSmartContractBatch() []byte {
	batch := db.smartContractBatch
	db.smartContractBatch = nil
	return batch
}

func NewSmartContractDB(
	codeStorage storage.Storage,
	dbSmartContract storage.Storage,
	accountStateDB types.AccountStateDB,
) *SmartContractDB {
	db := &SmartContractDB{
		codeStorage:     codeStorage,
		accountStateDB:  accountStateDB,
		dbSmartContract: dbSmartContract,
	} // go db.cleanupLoop() // Start the cleanup goroutine
	return db
}

func (db *SmartContractDB) Code(address common.Address) []byte {
	account, err := db.accountStateDB.AccountState(address)
	if err != nil {
		logger.Error("Error getting account state")
		return nil
	}
	codeHash := account.SmartContractState().CodeHash()
	code, err := db.codeStorage.Get(codeHash.Bytes())
	if err != nil {
		logger.Error("Error getting code from storage")
		return nil
	}
	return code
}

func (db *SmartContractDB) GetCodeByCodeHash(address common.Address, codeHash common.Hash) []byte {
	code, err := db.codeStorage.Get(codeHash.Bytes())
	if err != nil {
		logger.Error("Error getting code from storage")
		return nil
	}
	return code
}

func (db *SmartContractDB) StorageValue(address common.Address, key []byte, customRoot ...*common.Hash) ([]byte, bool) {
	t, err := db.loadStorageTrie(address, customRoot...)
	if err != nil {
		logger.Error("failed to get storage value: error: ", err)
		// Ghi log thời gian trước khi return
		return nil, false
	}
	if t == nil {
		logger.Error("failed to get storage value: trie is nil")
		// Ghi log thời gian trước khi return
		return nil, false
	}
	value, err := t.Get(key)
	if err != nil {
		logger.Error("failed to get value from trie: error: ", err)
		// Ghi log thời gian trước khi return
		return nil, false
	}

	if len(value) == 0 {
		// Ghi log thời gian trước khi return
		return common.Hash{}.Bytes(), true
	}
	// Kết thúc đo thời gian và ghi log
	return value, true
}

func (db *SmartContractDB) SetCode(address common.Address, codeHash common.Hash, code []byte) {
	db.pendingCode.LoadOrStore(codeHash, code)
}

func (db *SmartContractDB) SetStorageValue(address common.Address, key []byte, value []byte) error {
	t, err := db.loadStorageTrie(address)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("failed to get StorageValue: db is nil")
	}
	return t.Update(key, value)
}

// BatchSetStorageValues performs optimized batch updates for multiple storage slots
// of a single contract address. Instead of calling SetStorageValue N times (each
// doing loadStorageTrie + mutex Lock + hex.EncodeToString + DB read), this method:
//  1. Loads the storage trie ONCE per address
//  2. Delegates to trie.BatchUpdate() which parallelizes DB reads (16 workers)
//     and acquires the trie mutex only once for the entire batch
//
// This is the primary slot-write optimization: reduces lock overhead from N to 1
// and exploits SSD parallel read bandwidth for old-value lookups.
func (db *SmartContractDB) BatchSetStorageValues(address common.Address, keys, values [][]byte) error {
	if len(keys) == 0 {
		return nil
	}
	t, err := db.loadStorageTrie(address)
	if err != nil {
		return fmt.Errorf("failed to load trie for batch set: %w", err)
	}
	if t == nil {
		return fmt.Errorf("failed to batch set storage: trie is nil for address %s", address.Hex())
	}
	return t.BatchUpdate(keys, values)
}

func (db *SmartContractDB) EventLogs() map[common.Address][]types.EventLog {
	result := make(map[common.Address][]types.EventLog)
	db.pendingEventLogs.Range(func(key, value interface{}) bool {
		address := key.(common.Address)
		logs := value.([]types.EventLog)
		result[address] = logs
		return true
	})
	return result
}

func (db *SmartContractDB) AddEventLogs(eventLogs []types.EventLog) {
	for _, eventLog := range eventLogs {
		address := eventLog.Address()
		eve, _ := eventLog.Marshal()
		pbLog := &pb.EventLog{}

		// Unmarshal bytes thành protobuf object
		err := proto.Unmarshal(eve, pbLog)
		if err != nil {
			logger.Warn(err)
		}
		sEventLog := &smart_contract.EventLog{}
		sEventLog.FromProto(pbLog)

		// Load logs hiện tại (nếu có)
		if logs, ok := db.pendingEventLogs.Load(address); ok {
			// Đã có logs, append event mới vào slice
			existingLogs := logs.([]types.EventLog)
			// Tạo slice mới và append event mới để cộng dồn
			newLogs := append(existingLogs, eventLog)
			// Store lại với slice mới đã có event mới
			db.pendingEventLogs.Store(address, newLogs)
		} else {
			// Chưa có logs, tạo slice mới với event đầu tiên
			db.pendingEventLogs.Store(address, []types.EventLog{eventLog})
		}
	}
}

func GroupEventLogsByAddress(eventLogs []types.EventLog) map[common.Address][]types.EventLog {
	groupedLogs := make(map[common.Address][]types.EventLog)

	for _, eventLog := range eventLogs {
		address := eventLog.Address()
		groupedLogs[address] = append(groupedLogs[address], eventLog)
	}

	return groupedLogs
}

func (db *SmartContractDB) CommitAllStorage() error {
	var allBatches [][2][]byte
	var finalErr error
	backend := trie.GetStateBackend()

	var addresses []common.Address
	db.smartContractStorageTries.Range(func(key, _ interface{}) bool {
		address := key.(common.Address)
		addresses = append(addresses, address)
		return true
	})

	// FIX: Sorting the addresses ensures deterministic commit sequences
	// across the cluster, preventing random divergence in NOMT intermediate roots
	slices.SortFunc(addresses, func(a, b common.Address) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, address := range addresses {
		// Load trie (wrapped with PrefixedStorage for key isolation)
		t_val, ok := db.smartContractStorageTries.Load(address)
		if !ok || t_val == nil {
			logger.Error("Failed to load storage trie for address:", address)
			continue
		}
		t := t_val.(trie.StateTrie)

		var root common.Hash
		var commitSource trie.StateTrie // the trie instance to collect batch from

		// ═══════════════════════════════════════════════════════════════════════
		// NOMT FAST PATH: If the trie was already committed by LateBindRoots
		// (detected by HasUncommittedChanges=false), skip the redundant
		// Copy→Commit→CommitPayload cycle. The root and replication batch
		// are already available on the original trie.
		// ═══════════════════════════════════════════════════════════════════════
		if nomtTrie, isNomt := t.(*trie.NomtStateTrie); isNomt && !nomtTrie.HasUncommittedChanges() {
			root = nomtTrie.Hash()
			commitSource = t
			// CommitPayload was already called in LateBindRoots — nothing to persist.
		} else {
			// Normal path: Create a snapshot copy and commit.
			// This handles MPT, Flat, Verkle, and NOMT tries that were NOT
			// processed by LateBindRoots (e.g. read-only tries kept in map).
			commitTrie := t.Copy()

			var err error
			root, _, _, err = commitTrie.Commit(true)
			if err != nil {
				logger.Error("Error committing storage trie for address:", address)
				finalErr = err
				continue
			}

			if nomtTrie, isNomt := commitTrie.(*trie.NomtStateTrie); isNomt {
				if err := nomtTrie.CommitPayload(); err != nil {
					logger.Error("Error committing NOMT payload for address:", address, "error:", err)
					finalErr = err
					continue
				}
			}
			commitSource = commitTrie
		}

		// Update account state with new storage root
		as, asErr := db.accountStateDB.AccountState(address)
		if asErr != nil || as.SmartContractState() == nil {
			logger.Error("Invalid account state for address:", address)
			finalErr = asErr
			continue
		}

		if as.SmartContractState().StorageRoot() != root {
			if trie.GetStateBackend() == trie.BackendNOMT {
				logger.Debug("[SmartContractDB] NOMT late storage root bind for %s: %s -> %s",
					address.Hex(), as.SmartContractState().StorageRoot().Hex(), root.Hex())
				as.SmartContractState().SetStorageRoot(root)
				db.accountStateDB.SetState(as)
			} else {
				logger.Error("Storage root mismatch for address:", address,
					"expected:", as.SmartContractState().StorageRoot(),
					"got:", root)
				finalErr = fmt.Errorf("storage root mismatch for %s: expected %s, got %s",
					address.Hex(), as.SmartContractState().StorageRoot().Hex(), root.Hex())
				continue
			}
		}

		// Collect commit batch from the source that performed the actual commit
		commitBatch := commitSource.GetCommitBatch()

		if len(commitBatch) > 0 {
			if backend == trie.BackendMPT {
				// MPT does NOT self-persist: we must write the NodeSet {hash→blob}
				// to the GLOBAL raw db, because MPT nodes are shared and looked up via NodeHash.
				if batchErr := db.dbSmartContract.BatchPut(commitBatch); batchErr != nil {
					logger.Error("CommitAllStorage MPT BatchPut error for address:", address, "error:", batchErr)
					finalErr = batchErr
					continue
				}
				// For network replication, MPT global nodes are sent as-is.
				allBatches = append(allBatches, commitBatch...)
			} else {
				// Flat/Verkle/NOMT: already persisted locally.
				// The commitBatch lacks the Address prefix — prepend it for replication.
				for i := range commitBatch {
					prefixedKey := make([]byte, 20+len(commitBatch[i][0])) // Address is 20 bytes
					copy(prefixedKey[:20], address.Bytes())
					copy(prefixedKey[20:], commitBatch[i][0])
					
					allBatches = append(allBatches, [2][]byte{prefixedKey, commitBatch[i][1]})
				}
			}
		}

		db.smartContractStorageTries.Delete(address)
	}

	// Network replication: serialize batches for Sub nodes (master only)
	if config.ConfigApp.ServiceType == p_common.ServiceTypeMaster && len(allBatches) > 0 {
		data, err := storage.SerializeBatch(allBatches)
		if err != nil {
			logger.Error("CommitAllStorage serialize error:", err)
			return err
		}
		db.SetSmartContractStorageBatch(data)
	}

	return finalErr
}


// LateBindRoots computes the definitive StorageRoot for all dirty smart contracts
// and late-binds them into the AccountState before AccountStateDB.IntermediateRoot runs.
// This ensures that the AccountRoot hash is deterministic and incorporates correct EVM storage roots.
func (db *SmartContractDB) LateBindRoots() error {
	var finalErr error
	var addresses []common.Address
	db.smartContractStorageTries.Range(func(key, _ interface{}) bool {
		addresses = append(addresses, key.(common.Address))
		return true
	})

	// FIX: Ensure LateBindRoots processes in deterministic order to prevent
	// non-deterministic assignment of StorageRoot hashes across nodes.
	slices.SortFunc(addresses, func(a, b common.Address) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, address := range addresses {
		t_val, ok := db.smartContractStorageTries.Load(address)
		if !ok || t_val == nil {
			logger.Error("LateBindRoots: Failed to load storage trie for address:", address)
			continue
		}
		t := t_val.(trie.StateTrie)

		// VIRTUAL EXECUTION FIX:
		// Virtual execution caches tries in `smartContractStorageTries`. If we late-bind
		// a read-only trie loaded prior to a state update, it will revert `StorageRoot` to a stale value.
		var hasChanges bool = true
		if nomtTrie, isNomt := t.(*trie.NomtStateTrie); isNomt {
			hasChanges = nomtTrie.HasUncommittedChanges()
		}
		if !hasChanges {
			continue
		}

		// ═══════════════════════════════════════════════════════════════════════
		// CRITICAL FORK-SAFETY FIX (Apr 2026):
		//
		// For NOMT: commit the ORIGINAL trie directly and persist immediately.
		// The previous Copy→Commit→Abort pattern corrupted NOMT's shared handle:
		//   1. Copy creates a new NomtStateTrie sharing the same NOMT Handle
		//   2. Commit calls session.Finish(handle) which modifies the handle's
		//      internal Merkle tree state
		//   3. Close/Abort calls nomt_finished_session_abort which frees the
		//      session but does NOT fully revert the handle's Beatree state
		//   4. The next session on this handle starts from corrupted state
		//
		// By committing the original and calling CommitPayload immediately,
		// the handle always transitions through a clean lifecycle:
		//   BeginSession → Write → Finish → CommitPayload (persist)
		// CommitAllStorage then detects the trie is already committed and
		// skips redundant work.
		// ═══════════════════════════════════════════════════════════════════════
		var root common.Hash
		if _, isNomt := t.(*trie.NomtStateTrie); isNomt {
			var err error
			root, _, _, err = t.Commit(true)
			if err != nil {
				logger.Error("LateBindRoots: Error committing NOMT storage trie for address:", address)
				finalErr = err
				continue
			}
			// Persist immediately so the NOMT handle transitions to a clean state
			// before the next contract's session begins.
			if nomtTrie, ok := t.(*trie.NomtStateTrie); ok {
				if err := nomtTrie.CommitPayload(); err != nil {
					logger.Error("LateBindRoots: Error persisting NOMT payload for address:", address, "error:", err)
					finalErr = err
					continue
				}
			}
		} else {
			// Non-NOMT: safe to use Copy→Commit→Close (no shared mutable handle)
			commitTrie := t.Copy()
			var err error
			root, _, _, err = commitTrie.Commit(true)
			if err != nil {
				logger.Error("LateBindRoots: Error committing storage trie for address:", address)
				finalErr = err
				continue
			}
			if closer, ok := commitTrie.(interface{ Close() }); ok {
				closer.Close()
			}
		}

		as, asErr := db.accountStateDB.AccountState(address)
		if asErr != nil || as.SmartContractState() == nil {
			continue
		}

		if as.SmartContractState().StorageRoot() != root {
			logger.Debug("[SmartContractDB] LateBindRoots: early root bind for %s: %s -> %s",
				address.Hex(), as.SmartContractState().StorageRoot().Hex(), root.Hex())
			as.SmartContractState().SetStorageRoot(root)
			db.accountStateDB.SetState(as)
		}
	}
	return finalErr
}


func (db *SmartContractDB) Commit() error {
	var batch [][2][]byte

	// Commit code
	var codeHashes []common.Hash
	db.pendingCode.Range(func(key, _ interface{}) bool {
		codeHashes = append(codeHashes, key.(common.Hash))
		return true
	})
	slices.SortFunc(codeHashes, func(a, b common.Hash) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, codeHash := range codeHashes {
		value, ok := db.pendingCode.Load(codeHash)
		if !ok {
			continue
		}
		code := value.([]byte)
		batch = append(batch, [2][]byte{codeHash.Bytes(), code})
		db.pendingCode.Delete(codeHash)
	}

	if len(batch) > 0 {
		if err := db.codeStorage.BatchPut(batch); err != nil {
			logger.Error("Error batch putting code:", err)
			return err
		}

		if config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
			data, err := storage.SerializeBatch(batch)
			if err != nil {
				logger.Error("Error serializing code batch:", err)
				return err
			}
			db.SetCodeBatchPut(data)
		}
	}

	// Commit smart contract storage
	if err := db.CommitAllStorage(); err != nil {
		logger.Error("Error committing smart contract storage:", err)
		return err
	}

	// Commit event logs
	var eventLogErr error
	var eventLogAddresses []common.Address
	db.pendingEventLogs.Range(func(key, _ interface{}) bool {
		eventLogAddresses = append(eventLogAddresses, key.(common.Address))
		return true
	})
	slices.SortFunc(eventLogAddresses, func(a, b common.Address) int {
		return bytes.Compare(a[:], b[:])
	})

	var globalEventLogBatch [][2][]byte

	for _, address := range eventLogAddresses {
		value, ok := db.pendingEventLogs.Load(address)
		if !ok {
			continue
		}
		logs := value.([]types.EventLog)

		for _, log := range logs {
			eventLogBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(log.Proto())
			if err != nil {
				logger.Error("Error marshaling event log:", err)
				eventLogErr = err
				break
			}
			globalEventLogBatch = append(globalEventLogBatch, [2][]byte{log.Hash().Bytes(), eventLogBytes})
		}
		
		if eventLogErr != nil {
			break
		}

		db.pendingEventLogs.Delete(address)
	}

	if eventLogErr != nil {
		return eventLogErr
	}

	if len(globalEventLogBatch) > 0 {
		if config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
			data, err := storage.SerializeBatch(globalEventLogBatch)
			if err != nil {
				logger.Error("Error serializing event log batch:", err)
				return err
			}
			db.SetSmartContractBatch(data)
		}

		if err := db.dbSmartContract.BatchPut(globalEventLogBatch); err != nil {
			logger.Error("Error batch putting event logs:", err)
			return err
		}
	}

	return nil
}

func (db *SmartContractDB) GetLogsByHash(hash common.Hash) (*smart_contract.EventLog, error) {
	eventLogBytes, err := db.dbSmartContract.Get(hash.Bytes())
	if err != nil {
		logger.Error("Error getting event log from storage:", err)
		return nil, err
	}
	if eventLogBytes == nil {
		return nil, fmt.Errorf("event log not found for hash: %s", hash.Hex())
	}

	pbLog := &pb.EventLog{}
	err = proto.Unmarshal(eventLogBytes, pbLog)
	if err != nil {
		logger.Error("Error unmarshaling event log:", err)
		return nil, err
	}

	sEventLog := &smart_contract.EventLog{}
	sEventLog.FromProto(pbLog)

	return sEventLog, nil
}

func (db *SmartContractDB) StorageRoot(address common.Address, customRoot ...*common.Hash) common.Hash {
	t, err := db.loadStorageTrie(address, customRoot...)
	if err != nil {
		logger.Error("failed to get storage root: error: ", err)
		return common.Hash{}
	}
	if t == nil {
		logger.Error("failed to get storage root: trie is nil")
		return common.Hash{}
	}
	return t.Hash()
}

func (db *SmartContractDB) loadStorageTrie(address common.Address, customRoot ...*common.Hash) (trie.StateTrie, error) {

	db.lastAccessTime.LoadOrStore(address, time.Now())
	if t, ok := db.smartContractStorageTries.Load(address); ok {

		if t == nil {
			return nil, fmt.Errorf("trie is nil for address: %s", address.Hex())
		}

		return t.(trie.StateTrie), nil
	}
	var root common.Hash
	if len(customRoot) > 0 && customRoot[0] != nil {
		root = *customRoot[0]
	} else {

		as, err := db.accountStateDB.AccountState(address)

		if err != nil || as.SmartContractState() == nil {
			root = common.Hash{}
		} else {

			root = as.SmartContractState().StorageRoot()

		}
	}

	// CRITICAL: MPT uses global node sharing via Keccak256(RLP) hashes.
	// We MUST NOT wrap MPT with PrefixedStorage, otherwise node reads will fail
	// because they will prepend the contract address to the globally shared node hash.
	// Flat/Verkle backends DO require PrefixedStorage to isolate their slot namespaces.
	var trieDB storage.Storage
	if trie.GetStateBackend() == trie.BackendMPT {
		trieDB = db.dbSmartContract
	} else {
		trieDB = NewPrefixedStorage(db.dbSmartContract, address)
	}

	t, err := trie.NewStateTrie(root, trieDB, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create trie for address: %s, root %s, error: %w", address.Hex(), root, err)
	}
	db.smartContractStorageTries.LoadOrStore(address, t) // Sử dụng LoadOrStore

	// Kết thúc đo thời gian và ghi log
	return t, nil
}

func (db *SmartContractDB) Discard() {
	db.pendingCode.Range(func(key, _ interface{}) bool {
		db.pendingCode.Delete(key)
		return true
	})
	db.smartContractStorageTries.Range(func(key, _ interface{}) bool {
		db.smartContractStorageTries.Delete(key)
		return true
	})
}

// InvalidateAllCaches clears all in-memory caches. This is CRITICAL for fork-safety
// after applying P2P synchronization batches, which write directly to PebbleDB/NOMT.
// Without this, cached pre-sync reads (from eth_call or virtual execution) will persist
// into live execution, causing Node 1 to execute with stale values and produce divergent state roots.
func (db *SmartContractDB) InvalidateAllCaches() {
	db.Discard()
	db.pendingEventLogs.Range(func(key, _ interface{}) bool {
		db.pendingEventLogs.Delete(key)
		return true
	})
	db.lastAccessTime.Range(func(key, _ interface{}) bool {
		db.lastAccessTime.Delete(key)
		return true
	})
}
