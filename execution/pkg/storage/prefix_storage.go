package storage

import (

)

// PrefixStorage is a wrapper around a shared database (e.g., PebbleDB) that
// automatically prepends a unique domain prefix to all keys.
// This allows multiplexing multiple logical databases onto a single physical instance
// while preserving full isolation.
type PrefixStorage struct {
	db     Storage
	prefix []byte
}

// NewPrefixStorage creates a new PrefixStorage wrapper.
func NewPrefixStorage(db Storage, prefix string) *PrefixStorage {
	return &PrefixStorage{
		db:     db,
		prefix: []byte(prefix),
	}
}

// Get retrieves a value by prepending the prefix to the key.
func (ps *PrefixStorage) Get(key []byte) ([]byte, error) {
	prefixedKey := make([]byte, len(ps.prefix)+len(key))
	copy(prefixedKey, ps.prefix)
	copy(prefixedKey[len(ps.prefix):], key)
	return ps.db.Get(prefixedKey)
}

// Put stores a key-value pair after prepending the prefix.
func (ps *PrefixStorage) Put(key, value []byte) error {
	prefixedKey := make([]byte, len(ps.prefix)+len(key))
	copy(prefixedKey, ps.prefix)
	copy(prefixedKey[len(ps.prefix):], key)
	return ps.db.Put(prefixedKey, value)
}

// Delete removes a key-value pair after prepending the prefix.
func (ps *PrefixStorage) Delete(key []byte) error {
	prefixedKey := make([]byte, len(ps.prefix)+len(key))
	copy(prefixedKey, ps.prefix)
	copy(prefixedKey[len(ps.prefix):], key)
	return ps.db.Delete(prefixedKey)
}

// BatchPut stores multiple key-value pairs atomically after prepending the prefix.
func (ps *PrefixStorage) BatchPut(kvs [][2][]byte) error {
	prefixedKvs := make([][2][]byte, len(kvs))
	for i, kv := range kvs {
		prefixedKey := make([]byte, len(ps.prefix)+len(kv[0]))
		copy(prefixedKey, ps.prefix)
		copy(prefixedKey[len(ps.prefix):], kv[0])
		prefixedKvs[i] = [2][]byte{prefixedKey, kv[1]}
	}
	return ps.db.BatchPut(prefixedKvs)
}

// BatchDelete removes multiple keys atomically after prepending the prefix.
func (ps *PrefixStorage) BatchDelete(keys [][]byte) error {
	prefixedKeys := make([][]byte, len(keys))
	for i, key := range keys {
		prefixedKey := make([]byte, len(ps.prefix)+len(key))
		copy(prefixedKey, ps.prefix)
		copy(prefixedKey[len(ps.prefix):], key)
		prefixedKeys[i] = prefixedKey
	}
	return ps.db.BatchDelete(prefixedKeys)
}

// PrefixScan retrieves all key-value pairs matching a prefix within this logical database.
// It prepends the logical database prefix to the given prefix, scans the underlying physical database,
// and correctly strips the DB prefix before returning results back to the caller.
func (ps *PrefixStorage) PrefixScan(prefix []byte) ([][2][]byte, error) {
	// Construct the absolute prefix to search for in the underlying DB
	searchPrefix := make([]byte, len(ps.prefix)+len(prefix))
	copy(searchPrefix, ps.prefix)
	copy(searchPrefix[len(ps.prefix):], prefix)

	// Perform scan on the underlying physical database
	rawResults, err := ps.db.PrefixScan(searchPrefix)
	if err != nil {
		return nil, err
	}

	// Strip the prefix from the returned keys (the PrefixScan on the underlying DB might strip `searchPrefix`
	// entirely, or it might just give back raw keys. Since ShardelDB / PebbleDB stripped their argument `prefix`
	// from their returned list in existing code, let's just make sure it behaves normally)
	// Actually, wait: PebbleDB implementation in pkg/storage/pebble_db.go line 232 strips `prefix`.
	// So `rawResults` has `searchPrefix` completely stripped off.
	// But `PrefixScan` is supposed to strip the argument `prefix`.
	// Since `pebble_db.go` `PrefixScan(p)` strips `p`, here when we call `ps.db.PrefixScan(searchPrefix)`,
	// the result has `searchPrefix` stripped entirely.
	// This means it has both `ps.prefix` AND `prefix` stripped.
	// However, callers expect only `prefix` to be stripped.
	// Actually, looking at `pebble_db.go`:
	// key := make([]byte, len(iter.Key())-len(prefix))
	// copy(key, iter.Key()[len(prefix):])
	// Yes! `rawResults` keys are exactly what the user wants! They are missing `searchPrefix`, which includes `prefix`.
	
	return rawResults, nil
}

// Close is a NO-OP since the shared database lifecycle is managed globally.
func (ps *PrefixStorage) Close() error {
	return nil
}

// Open is a NO-OP since the shared database lifecycle is managed globally.
func (ps *PrefixStorage) Open() error {
	return nil
}

// Flush is a NO-OP but we could potentially invoke db.Flush() here if we want isolated triggers.
func (ps *PrefixStorage) Flush() error {
	return nil
}

// GetBackupPath returns an empty string as backups are handled uniformly at the chaindata directory level.
func (ps *PrefixStorage) GetBackupPath() string {
	return ""
}
