package utils

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

const shardedAddressMapNumShards = 32

// ShardedAddressMap is a concurrent, lock-contention-free map optimized for common.Address keys.
// It acts as a drop-in replacement for sync.Map in high-write scenarios where sync.Map struggles.
type ShardedAddressMap[V any] struct {
	shards [shardedAddressMapNumShards]*addressMapShard[V]
}

type addressMapShard[V any] struct {
	mu sync.RWMutex
	m  map[common.Address]V
}

// NewShardedAddressMap creates a new initialized ShardedAddressMap.
func NewShardedAddressMap[V any]() *ShardedAddressMap[V] {
	sm := &ShardedAddressMap[V]{}
	for i := 0; i < shardedAddressMapNumShards; i++ {
		sm.shards[i] = &addressMapShard[V]{
			m: make(map[common.Address]V),
		}
	}
	return sm
}

// getShard determines which shard an address belongs to.
// We use the 20th byte (index 19) of common.Address as it is highly entropic in hashed addresses.
func (sm *ShardedAddressMap[V]) getShard(addr common.Address) *addressMapShard[V] {
	return sm.shards[addr[19]%shardedAddressMapNumShards]
}

// Load returns the value stored in the map for a key, or nil if no value is present.
func (sm *ShardedAddressMap[V]) Load(addr common.Address) (V, bool) {
	shard := sm.getShard(addr)
	shard.mu.RLock()
	val, ok := shard.m[addr]
	shard.mu.RUnlock()
	return val, ok
}

// Store sets the value for a key.
func (sm *ShardedAddressMap[V]) Store(addr common.Address, val V) {
	shard := sm.getShard(addr)
	shard.mu.Lock()
	shard.m[addr] = val
	shard.mu.Unlock()
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
func (sm *ShardedAddressMap[V]) LoadOrStore(addr common.Address, val V) (actual V, loaded bool) {
	shard := sm.getShard(addr)

	// Optimistic read
	shard.mu.RLock()
	if existing, ok := shard.m[addr]; ok {
		shard.mu.RUnlock()
		return existing, true
	}
	shard.mu.RUnlock()

	shard.mu.Lock()
	defer shard.mu.Unlock()
	if existing, ok := shard.m[addr]; ok {
		return existing, true
	}
	shard.m[addr] = val
	return val, false
}

// Delete deletes the value for a key.
func (sm *ShardedAddressMap[V]) Delete(addr common.Address) {
	shard := sm.getShard(addr)
	shard.mu.Lock()
	delete(shard.m, addr)
	shard.mu.Unlock()
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
func (sm *ShardedAddressMap[V]) Range(f func(key common.Address, value V) bool) {
	for i := 0; i < shardedAddressMapNumShards; i++ {
		shard := sm.shards[i]
		shard.mu.RLock()
		for k, v := range shard.m {
			if !f(k, v) {
				shard.mu.RUnlock()
				return
			}
		}
		shard.mu.RUnlock()
	}
}

// Clear removes all elements from the map safely by replacing the inner maps.
func (sm *ShardedAddressMap[V]) Clear() {
	for i := 0; i < shardedAddressMapNumShards; i++ {
		shard := sm.shards[i]
		shard.mu.Lock()
		shard.m = make(map[common.Address]V)
		shard.mu.Unlock()
	}
}

// Len returns the total number of items across all shards.
// Useful for debugging and monitoring.
func (sm *ShardedAddressMap[V]) Len() int {
	total := 0
	for i := 0; i < shardedAddressMapNumShards; i++ {
		shard := sm.shards[i]
		shard.mu.RLock()
		total += len(shard.m)
		shard.mu.RUnlock()
	}
	return total
}
