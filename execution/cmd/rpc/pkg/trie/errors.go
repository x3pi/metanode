package trie

import (
	"errors"
	"fmt"

	e_common "github.com/ethereum/go-ethereum/common"
)

var ErrCommitted = errors.New("mtn trie is already committed")

// MissingNodeError is returned by the trie functions (Get, Update, Delete)
// in the case where a trie node is not present in the local database. It contains
// information necessary for retrieving the missing node.
type MissingNodeError struct {
	NodeHash e_common.Hash // hash of the missing node
	Path     []byte        // hex-encoded path to the missing node
	err      error         // concrete error for missing trie node
}

// Unwrap returns the concrete error for missing trie node which
// allows us for further analysis outside.
func (err *MissingNodeError) Unwrap() error {
	return err.err
}

func (err *MissingNodeError) Error() string {
	return fmt.Sprintf(
		"missing trie node %x (path %x) %v",
		err.NodeHash,
		err.Path,
		err.err,
	)
}
