package trie_database

import (
	"bytes"
	"fmt"
	"slices"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

type TrieDatabaseSnapshot struct {
	TrieCopy p_trie.StateTrie
	BackUpDb []byte
	SubPath  string
	Status   TrieDatabaseStatus
}

// TrieDatabaseManager quản lý nhiều TrieDatabase
type TrieDatabaseManager struct {
	trieDatabases    map[common.Hash]*TrieDatabase
	accountStateDB   *account_state_db.AccountStateDB
	collectedBatches map[string][]byte
	sharedDB         storage.Storage
}

var (
	instance *TrieDatabaseManager
	once     sync.Once
)

func CreateTrieDatabaseManager(db storage.Storage, accountStateDB *account_state_db.AccountStateDB) *TrieDatabaseManager {
	once.Do(func() {
		instance = &TrieDatabaseManager{
			trieDatabases:    make(map[common.Hash]*TrieDatabase),
			accountStateDB:   accountStateDB,
			collectedBatches: make(map[string][]byte),
			sharedDB:         db,
		}
	})
	return instance
}
func GetTrieDatabaseManager() *TrieDatabaseManager {
	return instance
}

func (manager *TrieDatabaseManager) CommitSnapshots(snapshots map[common.Hash]*TrieDatabaseSnapshot) error {
	trieIDs := make([]common.Hash, 0, len(snapshots))
	for id := range snapshots {
		trieIDs = append(trieIDs, id)
	}
	slices.SortFunc(trieIDs, func(a, b common.Hash) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, id := range trieIDs {
		snapshot := snapshots[id]
		trieDB, exists := manager.trieDatabases[id]
		if !exists {
			continue // Should not happen in normal flow, but safety check
		}
		
		switch snapshot.Status {
		case Deleted:
			if err := manager.DeleteTrieDatabase(id); err != nil {
				logger.Error("Failed to delete TrieDatabase", "id", id, "error", err)
				return err
			}
		case Reverted:
			if err := trieDB.Discard(); err != nil {
				logger.Error("Failed to discard TrieDatabase", "id", id, "error", err)
				return err
			}
		case Committed:
			key := snapshot.SubPath
			value := snapshot.BackUpDb
			// Thêm key-value vào map mới này
			manager.collectedBatches[key] = value
			
			// Commit directly from the snapshot copy
			if snapshot.TrieCopy != nil {
				root, nodeSet, _, err := snapshot.TrieCopy.Commit(true)
				if err != nil {
					return err
				}
				if nodeSet != nil && len(nodeSet.Nodes) > 0 {
					batch := make([][2][]byte, 0, len(nodeSet.Nodes))
					for _, node := range nodeSet.Nodes {
						if node.Hash == (common.Hash{}) {
							continue
						}
						batch = append(batch, [2][]byte{node.Hash.Bytes(), node.Blob})
					}
					if len(batch) > 0 {
						if err := trieDB.db.BatchPut(batch); err != nil {
							return fmt.Errorf("DB BatchPut failed: %w", err)
						}
					}
				}
				// ─── SAFE UPDATE OF LIVE TRIE ──────────────────────────────
				// Update the origin root for future discards
				trieDB.originRootHash = root
				// Create a new readonly trie based on the committed root
				newTrie, err := p_trie.NewStateTrie(root, trieDB.db, false)
				if err != nil {
					logger.Error("Error creating new trie after commit: %v", err)
					return err
				}
				// Safely bind the new trie. Pending dirty data in dirtyData is NOT lost
				// because dirtyData is separate from trieR.
				trieDB.trieR = newTrie
			}
		}
	}
	return nil
}

func (manager *TrieDatabaseManager) GetCollectedBatches() map[string][]byte {
	result := manager.collectedBatches
	// Zero-copy swap: return the current map and initialize a new one for the next block
	manager.collectedBatches = make(map[string][]byte)
	return result
}

// ResetCollectedBatches xoa toan bo du lieu (hien tai duoc tich hop vao GetCollectedBatches nhung giu lại de backward compatibility)
func (manager *TrieDatabaseManager) ResetCollectedBatches() {
	manager.collectedBatches = make(map[string][]byte)
}

func (manager *TrieDatabaseManager) SnapshotAllTrieDatabases() map[common.Hash]*TrieDatabaseSnapshot {
	snapshots := make(map[common.Hash]*TrieDatabaseSnapshot)
	for id, trieDB := range manager.trieDatabases {
		var trieCopy p_trie.StateTrie
		if trieDB.trieR != nil {
			trieCopy = trieDB.trieR.Copy()
		}
		
		var backUpDb []byte
		if trieDB.backUpDb != nil {
			backUpDb = make([]byte, len(trieDB.backUpDb))
			copy(backUpDb, trieDB.backUpDb)
		}
		
		snapshots[id] = &TrieDatabaseSnapshot{
			TrieCopy: trieCopy,
			BackUpDb: backUpDb,
			SubPath:  trieDB.subPath,
			Status:   trieDB.status,
		}
	}
	return snapshots
}

func (manager *TrieDatabaseManager) IntermediateRoot() error {
	trieIDs := manager.ListAllIDs()
	slices.SortFunc(trieIDs, func(a, b common.Hash) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, id := range trieIDs {
		trieDB := manager.trieDatabases[id]
		switch trieDB.GetStatus() {
		case Deleted:
			as, err := manager.accountStateDB.AccountState(trieDB.address)
			if err != nil {
				logger.Error("Failed to get AccountState", "id", id, "error", err)
				return err
			}
			as.SmartContractState().DeleteTrieDatabaseMapValue(trieDB.dbName)
			manager.accountStateDB.PublicSetDirtyAccountState(as)
		case Reverted:
			trieDB.Discard()
		default: // Bao gồm cả trạng thái Committed và các trạng thái khác
			root, err := trieDB.IntermediateRoot()
			if err != nil {
				logger.Error("Failed to get IntermediateRoot TrieDatabase", "id", id, "error", err)
				return err
			}
			as, err := manager.accountStateDB.AccountState(trieDB.address)
			if err != nil {
				logger.Error("Failed to get AccountState", "id", id, "error", err)
				return err
			}
			as.SmartContractState().SetTrieDatabaseMapValue(trieDB.dbName, root.Bytes())
			manager.accountStateDB.PublicSetDirtyAccountState(as)
			logger.Info("Updated IntermediateRoot for TrieDatabase", "id", id, "root", root)
		}
	}
	// Xóa các ID Deleted
	for _, id := range trieIDs {
		trieDB := manager.trieDatabases[id]
		if trieDB.GetStatus() == Deleted {
			manager.RemoveTrieDatabase(id)
		}
	}
	return nil
}

func (manager *TrieDatabaseManager) FindTrieDatabasesByMvmID(mvmId common.Address) []*TrieDatabase {
	var result []*TrieDatabase
	for _, trieDB := range manager.trieDatabases {
		if trieDB.mvmId == mvmId {
			result = append(result, trieDB)
		}
	}
	return result
}
func (manager *TrieDatabaseManager) FindAndSetTrieDatabasesByMvmID(mvmId common.Address, status TrieDatabaseStatus) {
	for _, trieDB := range manager.trieDatabases {
		if trieDB.mvmId == mvmId {
			trieDB.SetStatus(status)
		}
	}
}

// DiscardAllTrieDatabases loại bỏ tất cả các thay đổi đang chờ xử lý trong tất cả các TrieDatabase và xóa sạch bộ nhớ.
func (manager *TrieDatabaseManager) DiscardAllTrieDatabases() {
	for id, trieDB := range manager.trieDatabases {
		trieDB.Discard()
		logger.Info("Discarded TrieDatabase", "id", id)
	}
	manager.trieDatabases = make(map[common.Hash]*TrieDatabase)
}

// ClearAllTrieDatabases xóa sạch bộ nhớ cache của TrieDatabases (dùng cho Sub-node khi nhận block mới)
func (manager *TrieDatabaseManager) ClearAllTrieDatabases() {
	manager.trieDatabases = make(map[common.Hash]*TrieDatabase)
	logger.Info("✅ [TRIE MANAGER] Cleared all TrieDatabase caches from memory")
}

func (manager *TrieDatabaseManager) CloseAllTrieDatabases() error {
	for id, trieDB := range manager.trieDatabases {
		err := trieDB.db.Close()
		if err != nil {
			logger.Error("Failed to close TrieDatabase", "id", id, "error", err)
			// Return here or continue? Previous code returns on first error.
			return err
		}
		logger.Info("Closed TrieDatabase (NO-OP on PrefixStorage)", "id", id)
	}
	return nil
}

func (manager *TrieDatabaseManager) DeleteTrieDatabase(id common.Hash) error {
	trieDB, exists := manager.trieDatabases[id]
	if !exists {
		return nil // Không có gì để xóa nếu không tồn tại
	}

	// Xóa tất cả các keys thuộc prefix này (tương đương xóa folder cũ)
	results, err := trieDB.db.PrefixScan([]byte{})
	if err == nil && len(results) > 0 {
		var keysToDelete [][]byte
		for _, kv := range results {
			keysToDelete = append(keysToDelete, kv[0]) // kv[0] is the key
		}
		// Batch delete all keys
		_ = trieDB.db.BatchDelete(keysToDelete)
	}

	delete(manager.trieDatabases, id)
	logger.Info("Deleted TrieDatabase logic keys", "id", id, "address", trieDB.address.Hex(), "dbName", trieDB.dbName)
	return nil
}

// GetOrCrateTrieDatabase lấy một TrieDatabase theo ID của nó.
func (manager *TrieDatabaseManager) GetOrCrateTrieDatabase(id common.Hash, hash common.Hash, mvmId common.Address, address common.Address, dbName string) (*TrieDatabase, bool) {
	trieDB, exists := manager.trieDatabases[id]
	if !exists {
		dbNameHash := crypto.Keccak256Hash([]byte(dbName)).Hex()
		
		// Map the single TrieDatabase to a PrefixStorage slice on the sharedDB
		prefixStr := fmt.Sprintf("%s:%s:", address.Hex(), dbNameHash)
		database := storage.NewPrefixStorage(manager.sharedDB, prefixStr)

		trieDB = NewTrieDatabase(hash, database, mvmId, address, dbName, manager.accountStateDB)
		if trieDB == nil {
			return nil, false
		}
		manager.trieDatabases[id] = trieDB
	}
	return trieDB, true // trả về true nếu nó đã tồn tại, false nếu nó vừa được tạo
}

// RemoveTrieDatabase xóa một TrieDatabase khỏi danh sách quản lý
func (manager *TrieDatabaseManager) RemoveTrieDatabase(id common.Hash) {
	delete(manager.trieDatabases, id)
}

// ListAllIDs lấy danh sách tất cả các ID của TrieDatabase
func (manager *TrieDatabaseManager) ListAllIDs() []common.Hash {
	ids := make([]common.Hash, 0, len(manager.trieDatabases))
	for id := range manager.trieDatabases {
		ids = append(ids, id)
	}
	return ids
}


