package trie

import (
	"bytes"
	"encoding/hex"
	"fmt"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	trie_db "github.com/meta-node-blockchain/meta-node/pkg/trie/db"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// EmptyRootHash is the known root hash of an empty merkle trie.
var EmptyRootHash = e_common.HexToHash(
	"56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
)

// TrieData chứa dữ liệu cần thiết để tái tạo lại Trie.
type TrieData struct {
	RootHash  e_common.Hash
	KeyValues map[string][]byte
}
type MerklePatriciaTrie struct {
	root      node.Node
	committed bool
	reader    *TrieReader
	//
	tracer     *Tracer
	fastTracer *FastTracer // optional: used by BatchUpdate subtrees for performance
	isHash     bool

	// lastCommitBatch stores the node hash→blob pairs from the last Commit().
	// Used by AccountStateDB to replicate trie nodes to Sub nodes via AccountBatch.
	lastCommitBatch [][2][]byte
}

// tracerOnRead dispatches to FastTracer or Tracer.
func (t *MerklePatriciaTrie) tracerOnRead(path []byte, val []byte) {
	if t.fastTracer != nil {
		t.fastTracer.onRead(path, val)
	} else {
		t.tracer.onRead(path, val)
	}
}

// tracerOnInsert dispatches to FastTracer or Tracer.
func (t *MerklePatriciaTrie) tracerOnInsert(path []byte) {
	if t.fastTracer != nil {
		t.fastTracer.onInsert(path)
	} else {
		t.tracer.onInsert(path)
	}
}

// tracerOnDelete dispatches to FastTracer or Tracer.
func (t *MerklePatriciaTrie) tracerOnDelete(path []byte) {
	if t.fastTracer != nil {
		t.fastTracer.onDelete(path)
	} else {
		t.tracer.onDelete(path)
	}
}

// tracerAppendOldKey appends an old key to the tracer.
func (t *MerklePatriciaTrie) tracerAppendOldKey(hash node.HashNode) {
	if t.fastTracer != nil {
		t.fastTracer.oldKeys = append(t.fastTracer.oldKeys, hash)
	} else {
		t.tracer.oldKeys = append(t.tracer.oldKeys, hash)
	}
}

func (t *MerklePatriciaTrie) hashKey(key []byte) []byte {
	if len(key) == 32 || !t.isHash {
		return key
	}
	return crypto.Keccak256(key)
}

func New(root e_common.Hash, db trie_db.DB, isHash bool) (*MerklePatriciaTrie, error) {
	reader, err := newTrieReader(db)
	if err != nil {
		return nil, err
	}
	trie := &MerklePatriciaTrie{
		reader: reader,
		tracer: newTracer(),
		isHash: isHash,
	}
	if root != (e_common.Hash{}) && root != EmptyRootHash {
		rootnode, err := trie.resolveAndTrack(root[:], nil)
		if err != nil {
			return nil, err
		}
		trie.root = rootnode
	}
	return trie, nil
}

// copyInternal returns a concrete *MerklePatriciaTrie copy for internal use
// (e.g., Commit needs access to .root field which is not in the interface).
func (t *MerklePatriciaTrie) copyInternal() *MerklePatriciaTrie {
	return &MerklePatriciaTrie{
		root:   t.root,
		reader: t.reader,
		isHash: t.isHash,
		tracer: newTracer(),
	}
}

// Copy returns a shallow copy as StateTrie interface.
// Satisfies the StateTrie interface.
func (t *MerklePatriciaTrie) Copy() StateTrie {
	return t.copyInternal()
}

func (t *MerklePatriciaTrie) NodeIterator(start []byte) (NodeIterator, error) {
	return nil, nil
}

func (t *MerklePatriciaTrie) Get(key []byte) ([]byte, error) {
	if t.committed {
		return nil, ErrCommitted
	}

	hashedKey := t.hashKey(key)
	value, _, _, err := t.get(t.root, node.KeybytesToHex(hashedKey), 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get key: %w", err)
	}

	return value, nil
}

func (t *MerklePatriciaTrie) ParallelGet(keys [][]byte) ([][]byte, error) {
	if t.committed {
		return nil, ErrCommitted
	}

	// CRITICAL FORK-SAFETY: This function MUST be sequential, not parallel.
	// t.get() calls resolveAndTrack() which writes to t.tracer (shared state).
	// Additionally, get() resolves HashNode→actual nodes but the resolved nodes
	// were discarded by the old parallel version, causing non-deterministic
	// internal trie state across nodes depending on goroutine scheduling.
	// This was the ROOT CAUSE of forks under sustained high load.
	results := make([][]byte, len(keys))
	for i, key := range keys {
		hashedKey := t.hashKey(key)
		value, newnode, didResolve, err := t.get(t.root, node.KeybytesToHex(hashedKey), 0)
		if err != nil {
			return nil, fmt.Errorf("error getting key %s: %v", hex.EncodeToString(key), err)
		}
		// IMPORTANT: Update the root when a hash node was resolved.
		// This ensures the trie's internal state stays consistent
		// and subsequent gets don't re-resolve the same nodes.
		if didResolve {
			t.root = newnode
		}
		results[i] = value
	}

	return results, nil
}
func (t *MerklePatriciaTrie) get(
	origNode node.Node,
	key []byte,
	pos int,
) (value []byte, newnode node.Node, didResolve bool, err error) {
	switch n := (origNode).(type) {
	case nil:
		return nil, nil, false, nil
	case node.ValueNode:
		return n, n, false, nil
	case *node.ShortNode:
		if len(key)-pos < len(n.Key) || !bytes.Equal(n.Key, key[pos:pos+len(n.Key)]) {
			// key not found in trie
			return nil, n, false, nil
		}
		value, newnode, didResolve, err = t.get(n.Val, key, pos+len(n.Key))
		if err == nil && didResolve {
			n = n.Copy()
			n.Val = newnode
		}
		return value, n, didResolve, err
	case *node.FullNode:
		value, newnode, didResolve, err = t.get(n.Children[key[pos]], key, pos+1)
		if err == nil && didResolve {
			n = n.Copy()
			n.Children[key[pos]] = newnode
		}
		return value, n, didResolve, err
	case node.HashNode:
		child, err := t.resolveAndTrack(n, key[:pos])
		if err != nil {
			return nil, n, true, err
		}
		value, newnode, _, err := t.get(child, key, pos)
		return value, newnode, true, err
	default:
		return nil, nil, false, fmt.Errorf("%T: invalid node: %v", origNode, origNode)
	}
}

func (t *MerklePatriciaTrie) insert(
	n node.Node,
	prefix, key []byte,
	value node.Node,
) (bool, node.Node, error) {
	if len(key) == 0 {
		if v, ok := n.(node.ValueNode); ok {
			return !bytes.Equal(v, value.(node.ValueNode)), value, nil
		}
		return true, value, nil
	}
	switch n := n.(type) {
	case *node.ShortNode:
		matchlen := node.PrefixLen(key, n.Key)
		// If the whole key matches, keep this short node as is
		// and only update the value.
		if matchlen == len(n.Key) {
			dirty, nn, err := t.insert(n.Val, append(prefix, key[:matchlen]...), key[matchlen:], value)
			if !dirty || err != nil {
				if err != nil {
					logger.Error("error when insert trie node 1", err)
				}
				return false, n, err
			}
			t.tracerAppendOldKey(n.Flags.Hash)
			return true, &node.ShortNode{
				Key:   n.Key,
				Val:   nn,
				Flags: node.NewFlag(),
			}, nil
		}
		// Otherwise branch out at the index where they differ.
		branch := &node.FullNode{Flags: node.NewFlag()}
		var err error
		_, branch.Children[n.Key[matchlen]], err = t.insert(nil, append(prefix, n.Key[:matchlen+1]...), n.Key[matchlen+1:], n.Val)
		if err != nil {
			logger.Error("error when insert trie node 2", err)
			return false, nil, err
		}
		_, branch.Children[key[matchlen]], err = t.insert(nil, append(prefix, key[:matchlen+1]...), key[matchlen+1:], value)
		if err != nil {
			logger.Error("error when insert trie node 3", err)
			return false, nil, err
		}
		// Replace this shortNode with the branch if it occurs at index 0.
		if matchlen == 0 {
			t.tracerAppendOldKey(n.Flags.Hash)
			return true, branch, nil
		}
		// New branch node is created as a child of the original short node.
		// Track the newly inserted node in the tracer. The node identifier
		// passed is the path from the root node.
		t.tracerOnInsert(append(prefix, key[:matchlen]...))

		// Replace it with a short node leading up to the branch.
		t.tracerAppendOldKey(n.Flags.Hash)
		return true, &node.ShortNode{
			Key:   key[:matchlen],
			Val:   branch,
			Flags: node.NewFlag(),
		}, nil

	case *node.FullNode:
		dirty, nn, err := t.insert(n.Children[key[0]], append(prefix, key[0]), key[1:], value)
		if !dirty || err != nil {
			if err != nil {
				logger.Error("error when insert trie node 4", err)
				logger.DebugP("node", n.FString("@@@>"))
				logger.DebugP("prefix", hex.EncodeToString(prefix))
				logger.DebugP("key", hex.EncodeToString(key))
				logger.DebugP("value", value.FString("@@@!!>"))
			}
			return false, n, err
		}
		t.tracerAppendOldKey(n.Flags.Hash)
		// OPTIMIZATION: Skip Copy() if node is already dirty (exclusively owned
		// by current update path). Only copy clean nodes shared with committed state.
		// FORK-SAFETY: Dirty nodes are never shared — safe to mutate in-place.
		if !n.Flags.Dirty {
			n = n.Copy()
		}
		n.Flags = node.NewFlag()
		n.Children[key[0]] = nn
		return true, n, nil

	case nil:
		// New short node is created and track it in the tracer. The node identifier
		// passed is the path from the root node. Note the valueNode won't be tracked
		// since it's always embedded in its parent.
		t.tracerOnInsert(prefix)
		return true, &node.ShortNode{
			Key:   key,
			Val:   value,
			Flags: node.NewFlag(),
		}, nil

	case node.HashNode:
		// We've hit a part of the trie that isn't loaded yet. Load
		// the node and insert into it. This leaves all child nodes on
		// the path to the value in the trie.
		rn, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			logger.Error("error when insert trie node 5", err)
			logger.DebugP("prefix", hex.EncodeToString(prefix))
			logger.DebugP("node", hex.EncodeToString(n))
			// temporary create new short node
			logger.Warn("temporary create new short node, need more investigation if this effect to the result of the trie.")
			return t.insert(nil, prefix, key, value)
		}
		dirty, nn, err := t.insert(rn, prefix, key, value)
		if !dirty || err != nil {
			if err != nil {
				logger.Error("error when insert trie node 6", err)
				logger.DebugP("prefix", hex.EncodeToString(prefix))
				logger.DebugP("node", hex.EncodeToString(n))
			}
			return false, rn, err
		}
		return true, nn, nil

	default:
		return false, nil, fmt.Errorf("%T: invalid node: %v", n, n)
	}
}

func (t *MerklePatriciaTrie) Update(key, value []byte) error {

	if t.committed {
		logger.Error("Udpate error committed")

		return ErrCommitted
	}
	hashedKey := t.hashKey(key)
	err := t.update(hashedKey, value)
	return err
}

func (t *MerklePatriciaTrie) update(key, value []byte) error {
	k := node.KeybytesToHex(key)
	// k := key
	if len(value) != 0 {
		_, n, err := t.insert(t.root, nil, k, node.ValueNode(value))
		if err != nil {
			logger.Error("error when insert trie node", err)
			return err
		}
		t.root = n
	} else {
		_, n, err := t.delete(t.root, nil, k)
		if err != nil {
			return err
		}
		t.root = n
	}
	return nil
}
// delete returns the new root of the trie with key deleted.
// It reduces the trie to minimal form by simplifying
// nodes on the way up after deleting recursively.
func (t *MerklePatriciaTrie) delete(n node.Node, prefix, key []byte) (bool, node.Node, error) {
	switch n := n.(type) {
	case *node.ShortNode:
		matchlen := node.PrefixLen(key, n.Key)
		if matchlen < len(n.Key) {
			return false, n, nil // don't replace n on mismatch
		}
		if matchlen == len(key) {
			// The matched short node is deleted entirely and track
			// it in the deletion set. The same the valueNode doesn't
			// need to be tracked at all since it's always embedded.
			t.tracerOnDelete(prefix)

			return true, nil, nil // remove n entirely for whole matches
		}
		// The key is longer than n.Key. Remove the remaining suffix
		// from the subtrie. Child can never be nil here since the
		// subtrie must contain at least two other values with keys
		// longer than n.Key.
		dirty, child, err := t.delete(n.Val, append(prefix, key[:len(n.Key)]...), key[len(n.Key):])
		if !dirty || err != nil {
			return false, n, err
		}
		switch child := child.(type) {
		case *node.ShortNode:
			// The child shortNode is merged into its parent, track
			// is deleted as well.
			t.tracerOnDelete(append(prefix, n.Key...))

			// Deleting from the subtrie reduced it to another
			// short node. Merge the nodes to avoid creating a
			// shortNode{..., shortNode{...}}. Use concat (which
			// always creates a new slice) instead of append to
			// avoid modifying n.Key since it might be shared with
			// other nodes.
			return true, &node.ShortNode{
				Key:   concat(n.Key, child.Key...),
				Val:   child.Val,
				Flags: node.NewFlag(),
			}, nil
		default:
			return true, &node.ShortNode{
				Key:   n.Key,
				Val:   child,
				Flags: node.NewFlag(),
			}, nil
		}

	case *node.FullNode:
		dirty, nn, err := t.delete(n.Children[key[0]], append(prefix, key[0]), key[1:])
		if !dirty || err != nil {
			return false, n, err
		}
		n = n.Copy()
		n.Flags = node.NewFlag()
		n.Children[key[0]] = nn

		// Because n is a full node, it must've contained at least two children
		// before the delete operation. If the new child value is non-nil, n still
		// has at least two children after the deletion, and cannot be reduced to
		// a short node.
		if nn != nil {
			return true, n, nil
		}
		// Reduction:
		// Check how many non-nil entries are left after deleting and
		// reduce the full node to a short node if only one entry is
		// left. Since n must've contained at least two children
		// before deletion (otherwise it would not be a full node) n
		// can never be reduced to nil.
		//
		// When the loop is done, pos contains the index of the single
		// value that is left in n or -2 if n contains at least two
		// values.
		pos := -1
		for i, cld := range &n.Children {
			if cld != nil {
				if pos == -1 {
					pos = i
				} else {
					pos = -2
					break
				}
			}
		}
		if pos >= 0 {
			if pos != 16 {
				// If the remaining entry is a short node, it replaces
				// n and its key gets the missing nibble tacked to the
				// front. This avoids creating an invalid
				// shortNode{..., shortNode{...}}.  Since the entry
				// might not be loaded yet, resolve it just for this
				// check.
				cnode, err := t.resolve(n.Children[pos], append(prefix, byte(pos)))
				if err != nil {
					return false, nil, err
				}
				if cnode, ok := cnode.(*node.ShortNode); ok {
					// Replace the entire full node with the short node.
					// Mark the original short node as deleted since the
					// value is embedded into the parent now.
					t.tracerOnDelete(append(prefix, byte(pos)))

					k := append([]byte{byte(pos)}, cnode.Key...)
					return true, &node.ShortNode{
						Key:   k,
						Val:   cnode.Val,
						Flags: node.NewFlag(),
					}, nil
				}
			}
			// Otherwise, n is replaced by a one-nibble short node
			// containing the child.
			return true, &node.ShortNode{
				Key:   []byte{byte(pos)},
				Val:   n.Children[pos],
				Flags: node.NewFlag(),
			}, nil
		}
		// n still contains at least two values and cannot be reduced.
		return true, n, nil

	case node.ValueNode:
		return true, nil, nil

	case nil:
		return false, nil, nil

	case node.HashNode:
		// We've hit a part of the trie that isn't loaded yet. Load
		// the node and delete from it. This leaves all child nodes on
		// the path to the value in the trie.
		rn, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			return false, nil, err
		}
		dirty, nn, err := t.delete(rn, prefix, key)
		if !dirty || err != nil {
			return false, rn, err
		}
		return true, nn, nil

	default:
		return false, nil, fmt.Errorf("%T: invalid node: %v (%v)", n, n, key)
	}
}

func (t *MerklePatriciaTrie) Hash() e_common.Hash {
	hash, cached := t.hashRoot()
	t.root = cached
	return e_common.BytesToHash(hash.(node.HashNode))
}
// resolveAndTrack loads node from the underlying store with the given node hash
// and path prefix and also tracks the loaded node blob in tracer treated as the
// node's original value. The rlp-encoded blob is preferred to be loaded from
// database because it's easy to decode node while complex to encode node to blob.
func (t *MerklePatriciaTrie) resolveAndTrack(n node.HashNode, prefix []byte) (node.Node, error) {
	blob, err := t.reader.node(prefix, e_common.BytesToHash(n))
	if err != nil {
		return nil, err
	}
	t.tracerOnRead(prefix, blob)
	return node.DecodeNode(n, blob)
}

func (t *MerklePatriciaTrie) resolve(n node.Node, prefix []byte) (node.Node, error) {
	if n, ok := n.(node.HashNode); ok {
		return t.resolveAndTrack(n, prefix)
	}
	return n, nil
}

// hashRoot calculates the root hash of the given trie
func (t *MerklePatriciaTrie) hashRoot() (node.Node, node.Node) {
	if t.root == nil {
		return node.HashNode(EmptyRootHash.Bytes()), nil
	}
	// If the number of changes is below 100, we let one thread handle it
	h := newHasher(true)
	defer func() {
		returnHasherToPool(h)
	}()
	hashed, cached := h.hash(t.root, true)
	return hashed, cached
}

func concat(s1 []byte, s2 ...byte) []byte {
	r := make([]byte, len(s1)+len(s2))
	copy(r, s1)
	copy(r[len(s1):], s2)
	return r
}

// ClearDirty recursively traverses the Trie and resets the Dirty flag on all nodes.
// This is used to clear the dirty state after a successful commit so that future
// commits on the same Trie instance do not re-process unchanged nodes.
func (t *MerklePatriciaTrie) ClearDirty() {
	if t.root != nil {
		clearDirty(t.root)
	}
}

func clearDirty(n node.Node) {
	if n == nil {
		return
	}
	switch cn := n.(type) {
	case *node.ShortNode:
		if cn.Flags.Dirty {
			cn.Flags.Dirty = false
			clearDirty(cn.Val)
		}
	case *node.FullNode:
		if cn.Flags.Dirty {
			cn.Flags.Dirty = false
			for i := 0; i < len(cn.Children); i++ {
				if cn.Children[i] != nil {
					clearDirty(cn.Children[i])
				}
			}
		}
	case node.ValueNode, node.HashNode:
		// Leaf values and external hashes do not maintain dirty flags or children
	}
}
