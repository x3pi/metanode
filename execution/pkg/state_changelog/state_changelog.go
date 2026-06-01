package state_changelog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// StateChangelogDB manages historical state diffs per block
type StateChangelogDB struct {
	db        *pebble.DB
	namespace string
	mu        sync.RWMutex

	// startBlock is the first block that was committed with changelog enabled.
	// Used for exact accuracy: if targetBlock < startBlock, we CANNOT serve
	// historical data because we don't know what happened before tracking started.
	// Persisted as a PebbleDB key: "__meta__:start_block" → 8-byte big-endian.
	startBlock atomic.Uint64 // 0 = not yet initialized
}

// StateChange represents a single state mutation
type StateChange struct {
	Key      []byte // original address
	OldValue []byte // value before this block (nil = newly created)
	NewValue []byte // value after this block (nil = deleted)
}

// NewStateChangelogDB initializes a new changelog database
func NewStateChangelogDB(path string, namespace string) (*StateChangelogDB, error) {
	opts := &pebble.Options{
		DisableWAL: false,
		Cache:      storage.GetSharedPebbleCache(),
		TableCache: storage.GetSharedPebbleTableCache(),
	}

	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open changelog pebble db at %s: %w", path, err)
	}

	c := &StateChangelogDB{
		db:        db,
		namespace: namespace,
	}

	// Load persisted startBlock from DB (if exists)
	metaKey := []byte("__meta__:" + namespace + ":start_block")
	val, closer, getErr := db.Get(metaKey)
	if getErr == nil && len(val) == 8 {
		block := binary.BigEndian.Uint64(val)
		c.startBlock.Store(block)
		closer.Close()
		logger.Info("📜 [CHANGELOG] Loaded startBlock=%d for namespace %s", block, namespace)
	}

	logger.Info("📜 [CHANGELOG] Initialized StateChangelogDB at %s for namespace %s (startBlock=%d)", path, namespace, c.startBlock.Load())

	return c, nil
}

// Close gracefully shuts down the database
func (c *StateChangelogDB) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// Checkpoint creates an atomic PebbleDB checkpoint of the changelog DB to destDir.
func (c *StateChangelogDB) Checkpoint(destDir string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.db == nil {
		return fmt.Errorf("changelog db is closed")
	}
	return c.db.Checkpoint(destDir)
}

// WriteBlockChanges records state diffs for a given block
func (c *StateChangelogDB) WriteBlockChanges(blockNumber uint64, changes []StateChange) error {
	if len(changes) == 0 {
		return nil // Nothing to record
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	batch := c.db.NewBatch()
	defer batch.Close()

	// Persist startBlock on first write (CAS: only if still 0)
	if c.startBlock.CompareAndSwap(0, blockNumber) {
		metaKey := []byte("__meta__:" + c.namespace + ":start_block")
		blockBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(blockBytes, blockNumber)
		if err := batch.Set(metaKey, blockBytes, pebble.Sync); err != nil {
			c.startBlock.Store(0) // Reset on failure
			return fmt.Errorf("failed to persist startBlock: %w", err)
		}
		logger.Info("📜 [CHANGELOG] startBlock set to %d for namespace %s (first write)", blockNumber, c.namespace)
	}

	for _, change := range changes {
		// Key format: namespace:address:blockNumber
		// We use big-endian for blockNumber so that Pebble iterator sorts blocks correctly (older first)
		key := c.encodeKey(change.Key, blockNumber)
		
		// If NewValue is nil, it means it was deleted. We still record it with an empty byte slice to track the deletion event.
		// For StateChangelog, we care about what the value WAS at this point.
		// Actually, wait, reverse diff is better. If we want to know what the state was AT block X,
		// we query the latest value and apply reverse diffs.
		// But the implementation plan says: `GetStateAt` binary search for `(address, targetBlock)` -> returns value at that block.
		// So we just append the `NewValue`! This means we store the state AFTER this block execution.
		// If someone asks for state AT block X, we find the entry <= X.
		
		val := change.NewValue
		if len(val) == 0 {
			// Marker for deletion
			val = []byte("DEL")
		}

		if err := batch.Set(key, val, pebble.Sync); err != nil {
			return fmt.Errorf("failed to set changelog key: %w", err)
		}
	}

	// Use pebble.Sync instead of NoSync to ensure durability of historical changelogs,
	// protecting against historical state loss during sudden node shutdowns/kills.
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit changelog batch: %w", err)
	}

	logger.Debug("📜 [CHANGELOG] Recorded %d state changes for block %d in namespace %s", len(changes), blockNumber, c.namespace)
	return nil
}

// GetStartBlock returns the first block that was committed with changelog enabled.
// Returns 0 if not yet initialized (no blocks committed yet).
func (c *StateChangelogDB) GetStartBlock() uint64 {
	return c.startBlock.Load()
}

// GetBlockChanges scans the changelog to find all state changes that occurred AT a specific block.
// Note: This performs a full namespace scan. It is intended for debugging APIs only.
func (c *StateChangelogDB) GetBlockChanges(targetBlock uint64) ([]StateChange, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var changes []StateChange
	prefix := []byte(fmt.Sprintf("%s:", c.namespace))
	
	opts := &pebble.IterOptions{
		LowerBound: prefix,
	}
	iter, err := c.db.NewIter(opts)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	blockBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockBytes, targetBlock)

	for iter.SeekGE(prefix); iter.Valid(); iter.Next() {
		key := iter.Key()
		if !bytes.HasPrefix(key, prefix) {
			break
		}
		
		// Key format: namespace:address:blockNumber
		// blockNumber is the last 8 bytes.
		if len(key) < len(prefix)+9 {
			continue
		}
		
		keyBlockBytes := key[len(key)-8:]
		if bytes.Equal(keyBlockBytes, blockBytes) {
			addr := key[len(prefix) : len(key)-9]
			
			val := iter.Value()
			var newValue []byte
			if !bytes.Equal(val, []byte("DEL")) {
				newValue = make([]byte, len(val))
				copy(newValue, val)
			}
			
			addrCopy := make([]byte, len(addr))
			copy(addrCopy, addr)
			
			changes = append(changes, StateChange{
				Key:      addrCopy,
				NewValue: newValue,
			})
		}
	}
	
	if err := iter.Error(); err != nil {
		return nil, err
	}
	
	return changes, nil
}

// GetStateAt returns the value of an address AT a specific block.
// It finds the most recent change for this address that happened AT OR BEFORE targetBlock.
func (c *StateChangelogDB) GetStateAt(address []byte, targetBlock uint64) ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// We want to find the largest blockNumber <= targetBlock
	// Key format: namespace:address:blockNumber
	// By creating a prefix iterator up to targetBlock, we can get the value.
	
	startKey := c.encodeKeyPrefix(address)
	// targetBlock + 1 because the iterator's upper bound is exclusive, but wait...
	// If we use seekGE, it finds the first key >= target. But we want <= target.
	// So we can use SeekLT on (targetBlock + 1).

	searchKey := c.encodeKey(address, targetBlock+1)
	
	opts := &pebble.IterOptions{
		LowerBound: startKey,
	}
	
	iter, err := c.db.NewIter(opts)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	if iter.SeekLT(searchKey) {
		// Found an entry < targetBlock+1.
		// Must verify it belongs to the same address prefix.
		if bytes.HasPrefix(iter.Key(), startKey) {
			val := iter.Value()
			if bytes.Equal(val, []byte("DEL")) {
				return nil, nil // Deleted
			}
			// Return a copy to avoid referencing memory from pebble
			ret := make([]byte, len(val))
			copy(ret, val)
			return ret, nil
		}
	}

	// No entry found before or at targetBlock.
	// This means the address didn't have any changes recorded <= targetBlock.
	// If this node has a complete changelog from genesis, it means the address didn't exist.
	// If the node started syncing at block N, and targetBlock < N, we don't have the state.
	return nil, fmt.Errorf("historical state not found in changelog for block %d", targetBlock)
}

// HasAnyEntry checks if the given address has ANY changelog entry (at any block).
// This is used for progressive accuracy: if an address has zero entries, it means
// the account was never modified since changelog tracking started, so the current
// NOMT state IS the correct historical state for all blocks within the tracking range.
//
// Performance: single Pebble SeekGE (~1µs) — negligible overhead.
func (c *StateChangelogDB) HasAnyEntry(address []byte) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	prefix := c.encodeKeyPrefix(address)

	opts := &pebble.IterOptions{
		LowerBound: prefix,
	}
	iter, err := c.db.NewIter(opts)
	if err != nil {
		return false
	}
	defer iter.Close()

	// SeekGE finds the first key >= prefix. If it starts with prefix,
	// it means there is at least one entry for this address.
	if iter.SeekGE(prefix) {
		return bytes.HasPrefix(iter.Key(), prefix)
	}
	return false
}

// encodeKeyPrefix returns: namespace:address:
func (c *StateChangelogDB) encodeKeyPrefix(address []byte) []byte {
	prefix := []byte(fmt.Sprintf("%s:", c.namespace))
	prefix = append(prefix, address...)
	prefix = append(prefix, ':')
	return prefix
}

// encodeKey returns: namespace:address:blockNumber
func (c *StateChangelogDB) encodeKey(address []byte, blockNumber uint64) []byte {
	key := c.encodeKeyPrefix(address)
	
	// Encode blockNumber as 8-byte big endian so it sorts numerically
	blockBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockBytes, blockNumber)
	
	key = append(key, blockBytes...)
	return key
}
