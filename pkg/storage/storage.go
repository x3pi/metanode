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
