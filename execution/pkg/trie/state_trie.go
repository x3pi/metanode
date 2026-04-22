package trie

import (
	e_common "github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// StateTrie defines the interface for state storage backends.
// Both MerklePatriciaTrie and future FlatStateTrie implement this interface.
// This abstraction allows consumers (account_state_db, stake_state_db, etc.)
// to operate on any trie implementation without code changes.
type StateTrie interface {
	// Get retrieves the value for a key from the trie.
	Get(key []byte) ([]byte, error)

	// GetAll returns all key-value pairs in the trie.
	GetAll() (map[string][]byte, error)

	// Update sets the value for a key in the trie.
	Update(key, value []byte) error

	// BatchUpdate performs parallel updates for multiple keys.
	// Keys and values must be the same length.
	BatchUpdate(keys, values [][]byte) error

	// PreWarm pre-loads trie paths for the given keys to warm caches.
	PreWarm(keys [][]byte)

	// Hash computes the current root hash without committing.
	Hash() e_common.Hash

	// Commit finalizes changes and returns the root hash, node set, old keys, and error.
	Commit(collectLeaf bool) (e_common.Hash, *node.NodeSet, [][]byte, error)

	// GetCommitBatch returns the key-value pairs written during the last Commit(),
	// formatted for network replication to Sub nodes via AccountBatch.
	// For MPT: returns {nodeHash → nodeBlob} pairs (trie internal nodes).
	// For Flat: returns {fs:address → accountData} + {fb:idx → bucketHash} pairs.
	// For Verkle: returns {vk:address → accountData} pairs.
	// This is a one-shot read: calling it clears the stored batch to free memory.
	GetCommitBatch() [][2][]byte

	// Copy creates a shallow copy for concurrent operations.
	Copy() StateTrie
}

// Compile-time check: all trie implementations must implement StateTrie.
var _ StateTrie = (*MerklePatriciaTrie)(nil)
var _ StateTrie = (*FlatStateTrie)(nil)
var _ StateTrie = (*VerkleStateTrie)(nil)
var _ StateTrie = (*NomtStateTrie)(nil)
