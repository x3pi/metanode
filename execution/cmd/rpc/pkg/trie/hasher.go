package trie

import (
	"sync"

	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"

	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// hasher is a type used for the trie Hash operation. A hasher has some
// internal preallocated temp space
type hasher struct {
	sha      crypto.KeccakState
	tmp      []byte
	parallel bool // Whether to use parallel threads when hashing
}

// hasherPool holds pureHashers
var hasherPool = sync.Pool{
	New: func() interface{} {
		return &hasher{
			tmp: make([]byte, 0, 550), // cap is as large as a full fullNode.
			sha: sha3.NewLegacyKeccak256().(crypto.KeccakState),
		}
	},
}

func newHasher(parallel bool) *hasher {
	h := hasherPool.Get().(*hasher)
	h.parallel = parallel
	return h
}

func returnHasherToPool(h *hasher) {
	hasherPool.Put(h)
}

// hash collapses a node down into a hash node, also returning a copy of the
// original node initialized with the computed hash to replace the original one.
func (h *hasher) hash(n node.Node, force bool) (hashed node.Node, cached node.Node) {
	// Return the cached hash if it's available
	if hash, _ := n.Cache(); hash != nil {
		return hash, n
	}
	// Trie not processed yet, walk the children
	switch n := n.(type) {
	case *node.ShortNode:
		collapsed, cached := h.hashShortNodeChildren(n)
		hashed := h.shortnodeToHash(collapsed, force)
		// We need to retain the possibly _not_ hashed node, in case it was too
		// small to be hashed
		if hn, ok := hashed.(node.HashNode); ok {
			cached.Flags.Hash = hn
		} else {
			cached.Flags.Hash = nil
		}
		return hashed, cached
	case *node.FullNode:
		collapsed, cached := h.hashFullNodeChildren(n)
		hashed = h.fullnodeToHash(collapsed, force)
		if hn, ok := hashed.(node.HashNode); ok {
			cached.Flags.Hash = hn
		} else {
			cached.Flags.Hash = nil
		}
		return hashed, cached
	default:
		// Value and hash nodes don't have children, so they're left as were
		return n, n
	}
}

// hashShortNodeChildren collapses the short node. The returned collapsed node
// holds a live reference to the Key, and must not be modified.
func (h *hasher) hashShortNodeChildren(n *node.ShortNode) (collapsed, cached *node.ShortNode) {
	// Hash the short node's child, caching the newly hashed subtree
	collapsed, cached = n.Copy(), n.Copy()
	// Previously, we did copy this one. We don't seem to need to actually
	// do that, since we don't overwrite/reuse keys
	// cached.Key = common.CopyBytes(n.Key)
	collapsed.Key = node.HexToCompact(n.Key)
	// Unless the child is a valuenode or hashnode, hash it
	switch n.Val.(type) {
	case *node.FullNode, *node.ShortNode:
		collapsed.Val, cached.Val = h.hash(n.Val, false)
	}
	return collapsed, cached
}

func (h *hasher) hashFullNodeChildren(
	n *node.FullNode,
) (collapsed *node.FullNode, cached *node.FullNode) {
	// Hash the full node's children, caching the newly hashed subtrees
	cached = n.Copy()
	collapsed = n.Copy()
	if h.parallel {
		var wg sync.WaitGroup
		wg.Add(16)
		for i := 0; i < 16; i++ {
			go func(i int) {
				hasher := newHasher(false)
				if child := n.Children[i]; child != nil {
					collapsed.Children[i], cached.Children[i] = hasher.hash(child, false)
				} else {
					collapsed.Children[i] = node.NilValueNode
				}
				returnHasherToPool(hasher)
				wg.Done()
			}(i)
		}
		wg.Wait()
	} else {
		for i := 0; i < 16; i++ {
			if child := n.Children[i]; child != nil {
				collapsed.Children[i], cached.Children[i] = h.hash(child, false)
			} else {
				collapsed.Children[i] = node.NilValueNode
			}
		}
	}
	return collapsed, cached
}

func (h *hasher) shortnodeToHash(n *node.ShortNode, force bool) node.Node {
	b, _ := n.Marshal()
	return h.hashData(b)
}

// fullnodeToHash is used to create a hashNode from a fullNode, (which
// may contain nil values)
func (h *hasher) fullnodeToHash(n *node.FullNode, force bool) node.Node {
	b, _ := n.Marshal()
	return h.hashData(b)
}

// hashData hashes the provided data
func (h *hasher) hashData(data []byte) node.HashNode {
	n := make(node.HashNode, 32)
	h.sha.Reset()
	h.sha.Write(data)
	h.sha.Read(n)
	return n
}

func (h *hasher) proofHash(original node.Node) (collapsed, hashed node.Node) {
	switch n := original.(type) {
	case *node.ShortNode:
		sn, _ := h.hashShortNodeChildren(n)
		return sn, h.shortnodeToHash(sn, false)
	case *node.FullNode:
		fn, _ := h.hashFullNodeChildren(n)
		return fn, h.fullnodeToHash(fn, false)
	default:
		// Value and hash nodes don't have children, so they're left as were
		return n, n
	}
}
