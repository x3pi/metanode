package trie

// EvictableStateTrie is an optional capability interface for state tries
// that can prove whether their in-memory data has been safely made durable on disk.
// Tries implementing this interface will only be evicted from cache if CanEvict() returns true.
type EvictableStateTrie interface {
	CanEvict() bool
}
