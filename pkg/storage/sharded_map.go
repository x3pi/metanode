package storage

import (
	"sync"
)

const shardCount = 256

// ShardedMap is a high-performance concurrent map, optimized for heavy writes.
// By partitioning the map into 256 independent shards, it eliminates global lock
// contention that impacts standard sync.Map under high-throughput conditions.
// It is specifically strongly typed for string -> []byte to avoid interface{} allocations.
type ShardedMap struct {
	shards [shardCount]*shard
}

type shard struct {
	sync.RWMutex
	m map[string][]byte
}

// NewShardedMap creates a new partitioned concurrent map.
func NewShardedMap() *ShardedMap {
	sm := &ShardedMap{}
	for i := 0; i < shardCount; i++ {
		sm.shards[i] = &shard{m: make(map[string][]byte)}
	}
	return sm
}

// fnv32 is a fast hashing algorithm (FNV-1a 32-bit).
func fnv32(key string) uint32 {
	hash := uint32(2166136261)
	const prime32 = uint32(16777619)
	for i := 0; i < len(key); i++ {
		hash *= prime32
		hash ^= uint32(key[i])
	}
	return hash
}

// getShard returns the specific partition lock for a key.
func (sm *ShardedMap) getShard(key string) *shard {
	return sm.shards[fnv32(key)%shardCount]
}

// Load retrieves an item from the map.
// `ok` is true if the key exists in the map.
// Note: A value of `nil` represents a tombstone (deleted record).
func (sm *ShardedMap) Load(key string) ([]byte, bool) {
	shard := sm.getShard(key)
	shard.RLock()
	val, ok := shard.m[key]
	shard.RUnlock()
	return val, ok
}

// Store saves a key-value pair to the map.
// A nil value is valid and is used as a tombstone for LazyDB.
func (sm *ShardedMap) Store(key string, value []byte) {
	shard := sm.getShard(key)
	shard.Lock()
	shard.m[key] = value
	shard.Unlock()
}

// Delete removes an item entirely from the map.
// (Not equivalent to storing a tombstone, it removes the key).
func (sm *ShardedMap) Delete(key string) {
	shard := sm.getShard(key)
	shard.Lock()
	delete(shard.m, key)
	shard.Unlock()
}

// Range iterates over all shards and calls `f` on every KV pair.
// Iteration stops if `f` returns false.
// This is safe for concurrent use, locking one shard at a time.
func (sm *ShardedMap) Range(f func(key string, value []byte) bool) {
	for i := 0; i < shardCount; i++ {
		shard := sm.shards[i]
		shard.RLock()
		for k, v := range shard.m {
			if !f(k, v) {
				shard.RUnlock()
				return
			}
		}
		shard.RUnlock()
	}
}
