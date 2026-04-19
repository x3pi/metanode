package trie

import (
	"log"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// committer is the tool used for the trie Commit operation. The committer will
// capture all dirty nodes during the commit process and keep them cached in
// insertion order.
type committer struct {
	nodes       *node.NodeSet
	tracer      *Tracer
	collectLeaf bool
}

// newCommitter creates a new committer or picks one from the pool.
func newCommitter(nodeset *node.NodeSet, tracer *Tracer, collectLeaf bool) *committer {
	return &committer{
		nodes:       nodeset,
		tracer:      tracer,
		collectLeaf: collectLeaf,
	}
}

// Commit collapses a node down into a hash node.
func (c *committer) Commit(n node.Node) node.HashNode {
	return c.commit(nil, n).(node.HashNode)
}

// commit collapses a node down into a hash node and returns it.
func (c *committer) commit(path []byte, n node.Node) node.Node {
	// if this path is clean, use available cached data
	hash, dirty := n.Cache()
	if hash != nil && !dirty {
		return hash
	}
	// Commit children, then parent, and remove the dirty flag.
	switch cn := n.(type) {
	case *node.ShortNode:
		// Commit child
		collapsed := cn.Copy()

		// If the child is fullNode, recursively commit,
		// otherwise it can only be hashNode or valueNode.
		if _, ok := cn.Val.(*node.FullNode); ok {
			collapsed.Val = c.commit(append(path, cn.Key...), cn.Val)
		}
		// The key needs to be copied, since we're adding it to the
		// modified nodeset.
		collapsed.Key = node.HexToCompact(cn.Key)
		hashedNode := c.store(path, collapsed)
		if hn, ok := hashedNode.(node.HashNode); ok {
			return hn
		}
		return collapsed
	case *node.FullNode:
		hashedKids := c.commitChildren(path, cn)
		collapsed := cn.Copy()
		collapsed.Children = hashedKids

		hashedNode := c.store(path, collapsed)
		if hn, ok := hashedNode.(node.HashNode); ok {
			return hn
		}
		return collapsed
	case node.HashNode:
		return cn
	default:
		// nil, valuenode shouldn't be committed
		log.Fatalf("committer.commit: %T: invalid node: %v", n, n)
		return nil // unreachable
	}
}

// commitChildren commits the children of the given fullnode
func (c *committer) commitChildren(path []byte, n *node.FullNode) [17]node.Node {
	var children [17]node.Node
	for i := 0; i < 16; i++ {
		child := n.Children[i]
		if child == nil {
			continue
		}
		// If it's the hashed child, save the hash value directly.
		// Note: it's impossible that the child in range [0, 15]
		// is a valueNode.
		if hn, ok := child.(node.HashNode); ok {
			children[i] = hn
			continue
		}
		// Commit the child recursively and store the "hashed" value.
		// Note the returned node can be some embedded nodes, so it's
		// possible the type is not hashNode.
		children[i] = c.commit(append(path, byte(i)), child)
	}
	// For the 17th child, it's possible the type is valuenode.
	if n.Children[16] != nil {
		children[16] = n.Children[16]
	}
	return children
}

// store hashes the node n and adds it to the modified nodeset. If leaf collection
// is enabled, leaf nodes will be tracked in the modified nodeset as well.
func (c *committer) store(path []byte, n node.Node) node.Node {
	// Larger nodes are replaced by their hash and stored in the database.
	hash, _ := n.Cache()

	// This was not generated - must be a small node stored in the parent.
	// In theory, we should check if the node is leaf here (embedded node
	// usually is leaf node). But small value (less than 32bytes) is not
	// our target (leaves in account trie only).
	if hash == nil {
		// The node is embedded in its parent, in other words, this node
		// will not be stored in the database independently, mark it as
		// deleted only if the node was existent in database before.
		_, ok := c.tracer.accessList.Load(string(path))
		if ok {
			c.nodes.AddNode(path, node.NewDeleted())
		}
		return n
	}
	// Collect the dirty node to nodeset for return.
	nhash := common.BytesToHash(hash)
	c.nodes.AddNode(path, node.New(nhash, node.NodeToBytes(n)))

	// Collect the corresponding leaf node if it's required. We don't check
	// full node since it's impossible to store value in fullNode. The key
	// length of leaves should be exactly same.
	if c.collectLeaf {
		if sn, ok := n.(*node.ShortNode); ok {
			if val, ok := sn.Val.(node.ValueNode); ok {
				c.nodes.AddLeaf(nhash, val)
			}
		}
	}
	return hash
}

// mptResolver the children resolver in merkle-patricia-tree.
type mptResolver struct{}

// ForEach implements childResolver, decodes the provided node and
// traverses the children inside.
func (resolver mptResolver) ForEach(n []byte, onChild func(common.Hash)) {
	decodednode, _ := node.DecodeNode(nil, n)
	forGatherChildren(decodednode, onChild)
}

// forGatherChildren traverses the node hierarchy and invokes the callback
// for all the hashnode children.
func forGatherChildren(n node.Node, onChild func(hash common.Hash)) {
	switch n := n.(type) {
	case *node.ShortNode:
		forGatherChildren(n.Val, onChild)
	case *node.FullNode:
		for i := 0; i < 16; i++ {
			forGatherChildren(n.Children[i], onChild)
		}
	case node.HashNode:
		onChild(common.BytesToHash(n))
	case node.ValueNode, nil:
	default:
		log.Fatalf("forGatherChildren: unknown node type: %T", n)
	}
}
