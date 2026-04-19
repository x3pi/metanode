package trie_database

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// TrieDatabaseManager quản lý nhiều TrieDatabase
type TrieDatabaseManager struct {
	trieDatabases    map[common.Hash]*TrieDatabase
	accountStateDB   *account_state_db.AccountStateDB
	collectedBatches map[string][]byte
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
		}
	})
	return instance
}
func GetTrieDatabaseManager() *TrieDatabaseManager {
	return instance
}

// CommitAllTrieDatabases duyệt qua tất cả các TrieDatabase và commit chúng.
func (manager *TrieDatabaseManager) CommitAllTrieDatabases() error {

	trieIDs := manager.ListAllIDs()
	slices.SortFunc(trieIDs, func(a, b common.Hash) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, id := range trieIDs {
		trieDB := manager.trieDatabases[id]
		switch trieDB.GetStatus() {
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
			key := trieDB.GetSubPath()
			value := trieDB.backUpDb
			// Thêm key-value vào map mới này
			manager.collectedBatches[key] = value
			if _, err := trieDB.Commit(); err != nil {
				return err // Trả về lỗi nếu bất kỳ commit nào không thành công
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
			return err
		}
		logger.Info("Closed TrieDatabase", "id", id)
	}
	return nil
}

func (manager *TrieDatabaseManager) DeleteTrieDatabase(id common.Hash) error {
	trieDB, exists := manager.trieDatabases[id]
	if !exists {
		return nil // Không có gì để xóa nếu không tồn tại
	}

	// Đóng database trước khi xóa
	err := trieDB.db.Close()
	if err != nil {
		logger.Error("Failed to close TrieDatabase", "id", id, "error", err)
		return err
	}

	dbNameHash := crypto.Keccak256Hash([]byte(trieDB.dbName)).Hex()
	databasePath := filepath.Join(config.ConfigApp.Databases.Trie.Path, trieDB.address.String(), dbNameHash)

	err = os.RemoveAll(databasePath)
	if err != nil {
		logger.Error("Failed to delete TrieDatabase folder", "id", id, "path", databasePath, "error", err)
		return err
	}

	delete(manager.trieDatabases, id)
	logger.Info("Deleted TrieDatabase and folder", "id", id, "path", databasePath)
	return nil
}

// GetTrieDatabase lấy một TrieDatabase theo ID của nó.
func (manager *TrieDatabaseManager) GetOrCrateTrieDatabase(id common.Hash, hash common.Hash, mvmId common.Address, address common.Address, dbName string) (*TrieDatabase, bool) {
	trieDB, exists := manager.trieDatabases[id]
	if !exists {
		dbNameHash := crypto.Keccak256Hash([]byte(dbName)).Hex()

		subPath := filepath.Join(address.String(), dbNameHash)

		databasePath := filepath.Join(config.ConfigApp.Databases.RootPath+config.ConfigApp.Databases.Trie.Path, subPath)

		database, err := storage.NewShardelDB(
			databasePath,
			1, 2,
			config.ConfigApp.DBType,
			databasePath,
		)
		database.Open()
		if err != nil {
			logger.Error(err)
			return nil, false
		}
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

// CheckpointAll creates atomic PebbleDB checkpoints for all active TrieDatabases in the manager.
func (manager *TrieDatabaseManager) CheckpointAll(destBaseDir string) error {
	trieIDs := manager.ListAllIDs()

	for _, id := range trieIDs {
		trieDB, exists := manager.trieDatabases[id]
		if !exists {
			continue
		}
		if trieDB.GetStatus() == Deleted || trieDB.GetStatus() == Reverted {
			continue // Skip deleted or reverted databases
		}

		// Calculate destination subdirectory corresponding to the contract
		destDir := filepath.Join(destBaseDir, trieDB.GetSubPath())

		// Ensure the parent directory (usually destBaseDir/address) exists before calling Checkpoint
		parentDir := filepath.Dir(destDir)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			logger.Error("Failed to create parent directory for TrieDatabase checkpoint", "id", id, "parent", parentDir, "error", err)
			return err
		}

		if err := trieDB.db.Checkpoint(destDir); err != nil {
			logger.Error("Failed to checkpoint TrieDatabase", "id", id, "dest", destDir, "error", err)
			return err
		}
		logger.Info("✅ [SNAPSHOT] Checkpointed TrieDatabase", "id", id, "dest", destDir)
	}
	return nil
}
