package smart_contract_db

import (
	"bytes"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// PrefixedStorage wraps a storage.Storage and transparently prepends a fixed prefix
// (typically a contract address) to every key. This ensures that multiple tries
// sharing the same backing DB have isolated key namespaces.
//
// Without this, flat/verkle backends store keys as "fs:<slot>" or "vk:<slot>" with
// no per-contract disambiguation, causing cross-contract collisions when two
// contracts use the same storage slot (e.g. slot 0 for "owner").
//
// With PrefixedStorage, keys become "<address>fs:<slot>" / "<address>vk:<slot>",
// making collisions impossible.
type PrefixedStorage struct {
	inner  storage.Storage
	prefix []byte // typically 20-byte contract address
}

// NewPrefixedStorage creates a PrefixedStorage that wraps inner and prepends `address` bytes to all keys.
func NewPrefixedStorage(inner storage.Storage, address common.Address) *PrefixedStorage {
	return &PrefixedStorage{
		inner:  inner,
		prefix: address.Bytes(),
	}
}

// prefixBufPool reuses concat buffers for prefixKey to reduce GC pressure.
// Typical key: 20 (address) + 3 (fs:) + 32 (slot) = 55 bytes.
var prefixBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 64)
		return &buf
	},
}

// prefixKey prepends the stored prefix to the given key.
// Uses a pooled buffer for concatenation to reduce allocations.
func (ps *PrefixedStorage) prefixKey(key []byte) []byte {
	bufPtr := prefixBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	buf = append(buf, ps.prefix...)
	buf = append(buf, key...)
	result := make([]byte, len(buf))
	copy(result, buf)
	*bufPtr = buf
	prefixBufPool.Put(bufPtr)
	return result
}

// stripPrefix removes the stored prefix from a key.
// Returns nil if the key doesn't start with the prefix.
func (ps *PrefixedStorage) stripPrefix(key []byte) []byte {
	if len(key) < len(ps.prefix) || !bytes.HasPrefix(key, ps.prefix) {
		return nil
	}
	return key[len(ps.prefix):]
}

// Get retrieves a value by its prefixed key.
func (ps *PrefixedStorage) Get(key []byte) ([]byte, error) {
	return ps.inner.Get(ps.prefixKey(key))
}

// Put stores a value under its prefixed key.
func (ps *PrefixedStorage) Put(key, value []byte) error {
	return ps.inner.Put(ps.prefixKey(key), value)
}

// Delete removes a value by its prefixed key.
func (ps *PrefixedStorage) Delete(key []byte) error {
	return ps.inner.Delete(ps.prefixKey(key))
}

// BatchPut applies a batch of key-value pairs, each with prefixed keys.
func (ps *PrefixedStorage) BatchPut(pairs [][2][]byte) error {
	prefixed := make([][2][]byte, len(pairs))
	for i, kv := range pairs {
		prefixed[i] = [2][]byte{ps.prefixKey(kv[0]), kv[1]}
	}
	return ps.inner.BatchPut(prefixed)
}

// PrefixScan scans entries with `prefix + scanPrefix` and strips the prefix from returned keys.
func (ps *PrefixedStorage) PrefixScan(scanPrefix []byte) ([][2][]byte, error) {
	fullPrefix := ps.prefixKey(scanPrefix)
	pairs, err := ps.inner.PrefixScan(fullPrefix)
	if err != nil {
		return nil, err
	}
	// Strip the contract-address prefix from returned keys so callers see original keys
	for i, kv := range pairs {
		stripped := ps.stripPrefix(kv[0])
		if stripped != nil {
			pairs[i][0] = stripped
		}
	}
	return pairs, nil
}

// BatchDelete removes multiple entries by their prefixed keys.
func (ps *PrefixedStorage) BatchDelete(keys [][]byte) error {
	prefixed := make([][]byte, len(keys))
	for i, key := range keys {
		prefixed[i] = ps.prefixKey(key)
	}
	return ps.inner.BatchDelete(prefixed)
}

// Close is a passthrough — the underlying DB lifecycle is managed by StorageManager.
func (ps *PrefixedStorage) Close() error { return nil }

// Open is a passthrough — the underlying DB is already open.
func (ps *PrefixedStorage) Open() error { return nil }

// GetBackupPath delegates to the inner storage.
func (ps *PrefixedStorage) GetBackupPath() string { return ps.inner.GetBackupPath() }

// GetPrefix returns the prefix used by this storage.
func (ps *PrefixedStorage) GetPrefix() []byte { return ps.prefix }

// Flush delegates to the inner storage.
func (ps *PrefixedStorage) Flush() error { return ps.inner.Flush() }

// Compile-time check: PrefixedStorage must implement storage.Storage.
var _ storage.Storage = (*PrefixedStorage)(nil)
