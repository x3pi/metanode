package node

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	e_common "github.com/ethereum/go-ethereum/common"
)

// Node is a wrapper which contains the encoded blob of the trie node and its
// node hash. It is general enough that can be used to represent trie node
// corresponding to different trie implementations.
type NodeWrapper struct {
	Hash e_common.Hash // Node hash, empty for deleted node
	Blob []byte        // Encoded node blob, nil for the deleted node
}

func (n *NodeWrapper) String() string {
	return fmt.Sprintf("{%v, %x}", n.Hash, n.Blob)
}

// Size returns the total memory size used by this node.
func (n *NodeWrapper) Size() int {
	return len(n.Blob) + e_common.HashLength
}

// IsDeleted returns the indicator if the node is marked as deleted.
func (n *NodeWrapper) IsDeleted() bool {
	return len(n.Blob) == 0
}

// New constructs a node with provided node information.
func New(hash e_common.Hash, blob []byte) *NodeWrapper {
	return &NodeWrapper{Hash: hash, Blob: blob}
}

// NewDeleted constructs a node which is deleted.
func NewDeleted() *NodeWrapper { return New(e_common.Hash{}, nil) }

// leaf represents a trie leaf node
type leaf struct {
	Blob   []byte        // raw blob of leaf
	Parent e_common.Hash // the hash of parent node
}

// NodeSet contains a set of nodes collected during the commit operation.
// Each node is keyed by path. It's not thread-safe to use.
type NodeSet struct {
	Leaves  []*leaf
	Nodes   map[string]*NodeWrapper
	updates int // the count of updated and inserted nodes
	deletes int // the count of deleted nodes
}

// NewNodeSet initializes a node set. The owner is zero for the account trie and
// the owning account address hash for storage tries.
func NewNodeSet() *NodeSet {
	return &NodeSet{
		Nodes: make(map[string]*NodeWrapper),
	}
}

// ForEachWithOrder iterates the nodes with the order from bottom to top,
// right to left, nodes with the longest path will be iterated first.
func (set *NodeSet) ForEachWithOrder(callback func(path string, n *NodeWrapper)) {
	var paths []string
	for path := range set.Nodes {
		paths = append(paths, path)
	}
	// Bottom-up, the longest path first
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, path := range paths {
		callback(path, set.Nodes[path])
	}
}

// AddNode adds the provided node into set.
func (set *NodeSet) AddNode(path []byte, n *NodeWrapper) {
	if n.IsDeleted() {
		set.deletes += 1
	} else {
		set.updates += 1
	}
	set.Nodes[hex.EncodeToString(path)] = n
}

// Merge adds a set of nodes into the set.
func (set *NodeSet) Merge(nodes map[string]*NodeWrapper) error {
	for path, node := range nodes {
		prev, ok := set.Nodes[path]
		if ok {
			// overwrite happens, revoke the counter
			if prev.IsDeleted() {
				set.deletes -= 1
			} else {
				set.updates -= 1
			}
		}
		set.AddNode([]byte(path), node)
	}
	return nil
}

// AddLeaf adds the provided leaf node into set. TODO(rjl493456442) how can
// we get rid of it?
func (set *NodeSet) AddLeaf(parent e_common.Hash, blob []byte) {
	set.Leaves = append(set.Leaves, &leaf{Blob: blob, Parent: parent})
}

// Size returns the number of dirty nodes in set.
func (set *NodeSet) Size() (int, int) {
	return set.updates, set.deletes
}

// Hashes returns the hashes of all updated nodes. TODO(rjl493456442) how can
// we get rid of it?
func (set *NodeSet) Hashes() []e_common.Hash {
	var ret []e_common.Hash
	for _, node := range set.Nodes {
		ret = append(ret, node.Hash)
	}
	return ret
}

// Summary returns a string-representation of the NodeSet.
func (set *NodeSet) Summary() string {
	out := new(strings.Builder)
	if set.Nodes != nil {
		for path, n := range set.Nodes {
			// Deletion
			if n.IsDeleted() {
				fmt.Fprintf(out, "  [-]: %x\n", path)
				continue
			}
			// Insertion or update
			fmt.Fprintf(out, "  [+/*]: %x -> %v \n", path, n.Hash)
		}
	}
	for _, n := range set.Leaves {
		fmt.Fprintf(out, "[leaf]: %v\n", n)
	}
	return out.String()
}
