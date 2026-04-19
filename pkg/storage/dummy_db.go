package storage

// DummyStorage is a no-op storage implementation used to skip creating directories
// for databases that are completely bypassed when using the NOMT state backend
// (e.g., account_state, stake_db, trie_database).
type DummyStorage struct {
	backupPath string
}

func NewDummyStorage(backupPath string) *DummyStorage {
	return &DummyStorage{backupPath: backupPath}
}

func (d *DummyStorage) Get(key []byte) ([]byte, error)                { return nil, nil }
func (d *DummyStorage) Put(key, value []byte) error                   { return nil }
func (d *DummyStorage) Delete(key []byte) error                       { return nil }
func (d *DummyStorage) BatchPut(keys [][2][]byte) error               { return nil }
func (d *DummyStorage) BatchDelete(keys [][]byte) error               { return nil }
func (d *DummyStorage) PrefixScan(prefix []byte) ([][2][]byte, error) { return nil, nil }
func (d *DummyStorage) Close() error                                  { return nil }
func (d *DummyStorage) Open() error                                   { return nil }
func (d *DummyStorage) GetBackupPath() string                         { return d.backupPath }
func (d *DummyStorage) Flush() error                                  { return nil }
