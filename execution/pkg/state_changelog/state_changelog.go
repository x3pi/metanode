package state_changelog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// StateChangelogDB manages historical state diffs per block
type StateChangelogDB struct {
	db        *pebble.DB
	namespace string
	mu        sync.RWMutex
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
	}

	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open changelog pebble db at %s: %w", path, err)
	}

	logger.Info("📜 [CHANGELOG] Initialized StateChangelogDB at %s for namespace %s", path, namespace)

	return &StateChangelogDB{
		db:        db,
		namespace: namespace,
	}, nil
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

// WriteBlockChanges records state diffs for a given block
func (c *StateChangelogDB) WriteBlockChanges(blockNumber uint64, changes []StateChange) error {
	if len(changes) == 0 {
		return nil // Nothing to record
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	batch := c.db.NewBatch()
	defer batch.Close()

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

	if err := batch.Commit(pebble.NoSync); err != nil {
		return fmt.Errorf("failed to commit changelog batch: %w", err)
	}

	logger.Debug("📜 [CHANGELOG] Recorded %d state changes for block %d in namespace %s", len(changes), blockNumber, c.namespace)
	return nil
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
