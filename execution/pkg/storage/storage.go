package storage

const (
	STORAGE_TYPE_LEVEL_DB  = "level"
	STORAGE_TYPE_BADGER_DB = "badger"
	STORAGE_TYPE_MEMORY_DB = "memory"
)

type Storage interface {
	Get([]byte) ([]byte, error)
	Put([]byte, []byte) error
	// Has([]byte) bool
	Delete([]byte) error
	BatchPut([][2][]byte) error
	PrefixScan(prefix []byte) ([][2][]byte, error)
	Close() error
	Open() error
	GetBackupPath() string
	BatchDelete(keys [][]byte) error
	Flush() error
	// GetIterator() IIterator
	// GetSnapShot() SnapShot
}

// func LoadDb(dbPath string, dbType string) (Storage, error) {
// 	var db Storage
// 	var err error
// 	if dbType == STORAGE_TYPE_BADGER_DB {
// 		db, err = NewBadgerDB(
// 			dbPath,
// 		)
// 	} else {
// 		if dbType == STORAGE_TYPE_MEMORY_DB {
// 			db = NewMemoryDb()
// 		} else {
// 			db, err = NewLevelDB(
// 				dbPath,
// 			)
// 		}
// 	}
// 	return db, err
// }

// DurableSyncer is implemented by storages that can force everything written so far onto stable
// storage: in-memory write buffers are flushed and the write-ahead log is fsynced. Ordinary
// BatchPut/Put calls on these storages are only buffered (Pebble runs with NoSync), so a power loss
// drops the newest writes; callers that need "written before X" ordering across crashes
// (see BlockChain commit) must call SyncDurable.
type DurableSyncer interface {
	SyncDurable() error
}

// SyncDurable makes s durable if it supports it; storages that do not implement DurableSyncer have
// nothing to sync and are left untouched.
func SyncDurable(s Storage) error {
	if d, ok := s.(DurableSyncer); ok {
		return d.SyncDurable()
	}
	return nil
}
