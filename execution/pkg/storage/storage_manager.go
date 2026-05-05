package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/explorer"
	"github.com/meta-node-blockchain/meta-node/pkg/mining"
)

var (
	LastBlockNumberHashKey     common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastBlockNumberHashKey")))
	LastGlobalExecIndexHashKey common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastGlobalExecIndexHashKey")))
	LastExecutedCommitHashKey  common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastExecutedCommitHashKey")))
	LastHandledCommitIndexHashKey common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastHandledCommitIndexHashKey")))
	LastHandledCommitEpochHashKey common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastHandledCommitEpochHashKey")))
)

// StorageType sử dụng enum (iota) để định danh loại Storage
type StorageType int

const (
	STORAGE_ACCOUNT StorageType = iota
	STORAGE_BACKUP_DEVICE_KEY
	STORAGE_RECEIPTS
	STORAGE_SMART_CONTRACT
	STORAGE_CODE
	STORAGE_DATABASE_TRIE
	STORAGE_BLOCK
	STORAGE_BACKUP_DB
	STORAGE_MAPPING_DB
	STORAGE_TRANSACTION
	STORAGE_STAKE
)

// String() giúp in giá trị enum dễ đọc hơn
func (s StorageType) String() string {
	switch s {
	case STORAGE_ACCOUNT:
		return "ACCOUNT_DB"
	case STORAGE_BACKUP_DEVICE_KEY:
		return "STORAGE_BACKUP_DEVICE_KEY"
	case STORAGE_RECEIPTS:
		return "STORAGE_RECEIPTS"
	case STORAGE_SMART_CONTRACT:
		return "STORAGE_SMART_CONTRACT"
	case STORAGE_CODE:
		return "STORAGE_CODE"
	case STORAGE_DATABASE_TRIE:
		return "STORAGE_DATABASE_TRIE"
	case STORAGE_BLOCK:
		return "STORAGE_BLOCK"
	case STORAGE_BACKUP_DB:
		return "STORAGE_BACKUP_DB"
	case STORAGE_MAPPING_DB:
		return "STORAGE_MAPPING_DB"
	case STORAGE_TRANSACTION:
		return "STORAGE_TRANSACTION"
	case STORAGE_STAKE:
		return "STORAGE_STAKE"
	default:
		return "UNKNOWN"
	}
}

// Danh sách hợp lệ các loại storage
var validStorageTypes = map[StorageType]bool{
	STORAGE_ACCOUNT:           true,
	STORAGE_BACKUP_DEVICE_KEY: true,
	STORAGE_RECEIPTS:          true,
	STORAGE_SMART_CONTRACT:    true,
	STORAGE_CODE:              true,
	STORAGE_DATABASE_TRIE:     true,
	STORAGE_BLOCK:             true,
	STORAGE_BACKUP_DB:         true,
	STORAGE_MAPPING_DB:        true,
	STORAGE_TRANSACTION:       true,
	STORAGE_STAKE:             true,
}

// StorageManager quản lý nhiều Storage với enum key
type StorageManager struct {
	storages               map[StorageType]Storage
	sharedDB               *ShardelDB // the single underlying database for the node
	explorerSearch         *explorer.ExplorerSearchService
	explorerSearchReadOnly *explorer.ExplorerSearchService

	miningService *mining.MiningService

	mu sync.RWMutex
}

// Khởi tạo StorageManager
func NewStorageManager() *StorageManager {
	return &StorageManager{
		storages: make(map[StorageType]Storage),
	}
}

// InitSharedDatabase initializes a single database instance and creates PrefixStorage wrappers for all domains
func (sm *StorageManager) InitSharedDatabase(rootPath string, dbType DBType) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	dbPath := filepath.Join(rootPath, "chaindata")
	
	// Create ShardelDB to use the specified DBType with 1 shard
	db, err := NewShardelDB(dbPath, 1, 4, dbType, "")
	if err != nil {
		return fmt.Errorf("failed to create shared DB: %w", err)
	}
	
	if err := db.Open(); err != nil {
		return fmt.Errorf("failed to open shared DB: %w", err)
	}
	
	sm.sharedDB = db

	prefixMap := map[StorageType]string{
		STORAGE_ACCOUNT:           "ac:",
		STORAGE_BLOCK:             "bl:",
		STORAGE_RECEIPTS:          "rc:",
		STORAGE_TRANSACTION:       "tx:",
		STORAGE_SMART_CONTRACT:    "sc:",
		STORAGE_CODE:              "cd:",
		STORAGE_DATABASE_TRIE:     "tr:",
		STORAGE_BACKUP_DEVICE_KEY: "bk:",
		STORAGE_MAPPING_DB:        "mp:",
		STORAGE_STAKE:             "st:",
		STORAGE_BACKUP_DB:         "bu:",
	}

	for sType, prefix := range prefixMap {
		sm.storages[sType] = NewPrefixStorage(sm.sharedDB, prefix)
	}

	return nil
}

func (sm *StorageManager) IsExplorer() bool {
	return sm.explorerSearch != nil
}

func (sm *StorageManager) SetExplorerSearchService(s *explorer.ExplorerSearchService) {
	sm.explorerSearch = s
}

func (sm *StorageManager) GetExplorerSearchService() *explorer.ExplorerSearchService {
	return sm.explorerSearch
}

func (sm *StorageManager) SetExplorerSearchServiceReadOnly(s *explorer.ExplorerSearchService) {
	sm.explorerSearchReadOnly = s
}

func (sm *StorageManager) GetExplorerSearchServiceReadOnly() *explorer.ExplorerSearchService {
	return sm.explorerSearchReadOnly
}

func (sm *StorageManager) IsMining() bool {
	return sm.miningService != nil
}

func (sm *StorageManager) SetMiningService(s *mining.MiningService) {
	sm.miningService = s
}

func (sm *StorageManager) GetMiningService() *mining.MiningService {
	return sm.miningService
}

// Thêm Storage vào manager (đảm bảo chỉ thêm 1 lần và hợp lệ)
func (sm *StorageManager) AddStorage(dbType StorageType, storage Storage) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Kiểm tra loại storage có hợp lệ không
	if _, valid := validStorageTypes[dbType]; !valid {
		return errors.New("invalid storage type")
	}

	// Kiểm tra nếu storage đã tồn tại
	if _, exists := sm.storages[dbType]; exists {
		return errors.New("storage type already exists")
	}

	sm.storages[dbType] = storage
	return nil
}

// Lấy Storage theo loại (enum key)
func (sm *StorageManager) GetStorage(dbType StorageType) Storage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	storage, exists := sm.storages[dbType]
	if !exists {
		return nil
	}
	return storage
}

// Các hàm AddStorage riêng cho từng loại
func (sm *StorageManager) AddStorageAccount(storage Storage) error {
	return sm.AddStorage(STORAGE_ACCOUNT, storage)
}

func (sm *StorageManager) GetStorageAccount() Storage {
	return sm.GetStorage(STORAGE_ACCOUNT)
}

func (sm *StorageManager) AddStorageTransaction(storage Storage) error {
	return sm.AddStorage(STORAGE_TRANSACTION, storage)
}

func (sm *StorageManager) GetStorageTransaction() Storage {
	return sm.GetStorage(STORAGE_TRANSACTION)
}

func (sm *StorageManager) AddStorageBackupDeviceKey(storage Storage) error {
	return sm.AddStorage(STORAGE_BACKUP_DEVICE_KEY, storage)
}

func (sm *StorageManager) GetStorageBackupDeviceKey() Storage {
	return sm.GetStorage(STORAGE_BACKUP_DEVICE_KEY)
}

func (sm *StorageManager) AddStorageReceipt(storage Storage) error {
	return sm.AddStorage(STORAGE_RECEIPTS, storage)
}

func (sm *StorageManager) GetStorageReceipt() Storage {
	return sm.GetStorage(STORAGE_RECEIPTS)
}

func (sm *StorageManager) AddStorageSmartContract(storage Storage) error {
	return sm.AddStorage(STORAGE_SMART_CONTRACT, storage)
}

func (sm *StorageManager) GetStorageSmartContract() Storage {
	return sm.GetStorage(STORAGE_SMART_CONTRACT)
}

func (sm *StorageManager) AddStorageCode(storage Storage) error {
	return sm.AddStorage(STORAGE_CODE, storage)
}

func (sm *StorageManager) GetStorageCode() Storage {
	return sm.GetStorage(STORAGE_CODE)
}

func (sm *StorageManager) AddStorageDatabaseTrie(storage Storage) error {
	return sm.AddStorage(STORAGE_DATABASE_TRIE, storage)
}

func (sm *StorageManager) GetStorageDatabaseTrie() Storage {
	return sm.GetStorage(STORAGE_DATABASE_TRIE)

}

func (sm *StorageManager) AddStorageBlock(storage Storage) error {
	return sm.AddStorage(STORAGE_BLOCK, storage)
}

func (sm *StorageManager) GetStorageBlock() Storage {
	return sm.GetStorage(STORAGE_BLOCK)
}

func (sm *StorageManager) AddStorageBackupDb(storage Storage) error {
	return sm.AddStorage(STORAGE_BACKUP_DB, storage)
}

func (sm *StorageManager) GetStorageBackupDb() Storage {
	return sm.GetStorage(STORAGE_BACKUP_DB)
}

func (sm *StorageManager) AddStorageMapping(storage Storage) error {
	return sm.AddStorage(STORAGE_MAPPING_DB, storage)
}

func (sm *StorageManager) GetStorageMapping() Storage {
	return sm.GetStorage(STORAGE_MAPPING_DB)

}

func (sm *StorageManager) AddStorageStake(storage Storage) error {
	return sm.AddStorage(STORAGE_STAKE, storage)
}

func (sm *StorageManager) GetStorageStake() Storage {
	return sm.GetStorage(STORAGE_STAKE)

}

// CloseAll đóng tất cả các database trong StorageManager
func (sm *StorageManager) CloseAll() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.sharedDB != nil {
		if err := sm.sharedDB.Close(); err != nil {
			return fmt.Errorf("failed to close shared DB: %w", err)
		}
		sm.sharedDB = nil
		// Clear storages map
		sm.storages = make(map[StorageType]Storage)
		return nil
	}

	var closeErrs []error

	for dbType, storage := range sm.storages {
		if err := storage.Close(); err != nil {
			closeErrs = append(closeErrs, errors.New(dbType.String()+": "+err.Error()))
		}
		delete(sm.storages, dbType) // Xóa khỏi danh sách sau khi đóng
	}

	// Nếu có lỗi khi đóng, gộp tất cả lại và trả về
	if len(closeErrs) > 0 {
		return errors.New("failed to close some storages: " + combineErrors(closeErrs))
	}

	return nil
}

// FlushAll flush tất cả memory buffers của tất cả các database trong StorageManager xuống đĩa
func (sm *StorageManager) FlushAll() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.sharedDB != nil {
		return sm.sharedDB.Flush()
	}

	var flushErrs []error

	for dbType, storage := range sm.storages {
		if err := storage.Flush(); err != nil {
			flushErrs = append(flushErrs, errors.New(dbType.String()+": "+err.Error()))
		}
	}

	if len(flushErrs) > 0 {
		return errors.New("failed to flush some storages: " + combineErrors(flushErrs))
	}

	return nil
}

// storageTypeToDirName maps StorageType enum to the directory name used in snapshots.
// These MUST match the directory names used by restore_node.sh and snapshot_manager.go.
var storageTypeToDirName = map[StorageType]string{
	STORAGE_ACCOUNT:           "account_state",
	STORAGE_BLOCK:             "blocks",
	STORAGE_RECEIPTS:          "receipts",
	STORAGE_TRANSACTION:       "transaction_state",
	STORAGE_MAPPING_DB:        "mapping",
	STORAGE_CODE:              "smart_contract_code",
	STORAGE_SMART_CONTRACT:    "smart_contract_storage",
	STORAGE_STAKE:             "stake_db",
	STORAGE_DATABASE_TRIE:     "trie_database",
	STORAGE_BACKUP_DEVICE_KEY: "backup_device_key_storage",
	STORAGE_BACKUP_DB:         "back_up/backup_db",
}

// CheckpointAll creates atomic PebbleDB checkpoints for all databases to destBaseDir.
// Each database is checkpointed to destBaseDir/<dir_name> (e.g. destBaseDir/account_state).
// This uses PebbleDB's native Checkpoint which includes memtable + WAL data,
// ensuring a fully consistent point-in-time snapshot (unlike file copy which misses unflushed data).
func (sm *StorageManager) CheckpointAll(destBaseDir string) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.sharedDB != nil {
		// Create a single unified checkpoint at <destBaseDir>/chaindata
		destDir := filepath.Join(destBaseDir, "chaindata")
		if err := sm.sharedDB.Checkpoint(destDir); err != nil {
			return fmt.Errorf("unified shared DB checkpoint failed: %w", err)
		}
		return nil
	}

	var checkpointErrs []error

	for dbType, storage := range sm.storages {
		dirName, ok := storageTypeToDirName[dbType]
		if !ok {
			continue // Skip storage types not mapped to a snapshot directory
		}

		// Type-assert to *ShardelDB which has the Checkpoint method
		shardelDB, ok := storage.(*ShardelDB)
		if !ok {
			continue // Skip non-ShardelDB storages (e.g. RemoteStorage)
		}

		destDir := destBaseDir + "/" + dirName
		if err := shardelDB.Checkpoint(destDir); err != nil {
			checkpointErrs = append(checkpointErrs, errors.New(dbType.String()+": "+err.Error()))
		}
	}

	if len(checkpointErrs) > 0 {
		return errors.New("failed to checkpoint some storages: " + combineErrors(checkpointErrs))
	}

	return nil
}

// combineErrors nối lỗi thành một chuỗi duy nhất
func combineErrors(errs []error) string {
	var result string
	for _, err := range errs {
		if result != "" {
			result += "; "
		}
		result += err.Error()
	}
	return result
}
