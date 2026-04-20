package trie

import (
	e_common "github.com/ethereum/go-ethereum/common"

	trie_db "github.com/meta-node-blockchain/meta-node/pkg/trie/db"
)

type TrieReader struct {
	db trie_db.DB
}

// newTrieReader initializes the trie reader with the given node reader.
func newTrieReader(db trie_db.DB) (*TrieReader, error) {
	return &TrieReader{
		db: db,
	}, nil
}

// node retrieves the rlp-encoded trie node with the provided trie node
// information. An MissingNodeError will be returned in case the node is
// not found or any error is encountered.
func (r *TrieReader) node(path []byte, hash e_common.Hash) ([]byte, error) {
	blob, err := r.db.Get(hash.Bytes())
	if err != nil || len(blob) == 0 {
		return nil, &MissingNodeError{
			NodeHash: hash, Path: path, err: err,
		}
	}
	return blob, nil
}
