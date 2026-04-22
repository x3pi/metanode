package trie

import (
	"bytes"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"
	"sync"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
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

// ExportTrie xuất Trie vào file.
func ExportTrie(t *MerklePatriciaTrie, filePath string) error {
	data := TrieData{
		RootHash:  t.Hash(),
		KeyValues: make(map[string][]byte),
	}

	// Lấy tất cả key-value từ Trie
	allData, err := t.GetAll()
	if err != nil {
		return fmt.Errorf("không thể lấy tất cả dữ liệu từ Trie: %v", err)
	}

	data.KeyValues = allData

	// Mở file để ghi
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("không thể tạo file: %v", err)
	}
	defer file.Close()

	// Mã hóa và ghi dữ liệu vào file
	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("lỗi mã hóa dữ liệu vào file: %v", err)
	}

	fmt.Printf("✅ Đã xuất Trie vào file: %s\n", filePath)
	return nil
}

// ImportTrie khôi phục Trie từ file.
func ImportTrie(filePath string) (*MerklePatriciaTrie, error) {
	// Mở file để đọc
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("không thể mở file: %v", err)
	}
	defer file.Close()

	// Giải mã dữ liệu từ file
	var data TrieData
	decoder := gob.NewDecoder(file)
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("lỗi giải mã dữ liệu từ file: %v", err)
	}

	// Tạo một MerklePatriciaTrie mới với cơ sở dữ liệu memory
	db := storage.NewMemoryDb()
	t, err := New(data.RootHash, db, true)
	if err != nil {
		return nil, fmt.Errorf("không thể tạo Trie mới: %v", err)
	}

	// Cập nhật dữ liệu vào Trie
	for key, value := range data.KeyValues {
		err := t.Update([]byte(key), value)
		if err != nil {
			return nil, fmt.Errorf("lỗi cập nhật key %s: %v", key, err)
		}
	}

	fmt.Printf("✅ Đã nhập Trie từ file: %s\n", filePath)
	return t, nil
}

type MerklePatriciaTrie struct {
	root      node.Node
	committed bool
	reader    *TrieReader
	//
	tracer *Tracer
	isHash bool
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

func (t *MerklePatriciaTrie) Copy() *MerklePatriciaTrie {
	return &MerklePatriciaTrie{
		root:   t.root,
		reader: t.reader,
		isHash: t.isHash,
		tracer: newTracer(),
	}
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

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make([][]byte, len(keys))
		errors  = make([]error, len(keys))
	)

	for i, key := range keys {
		wg.Add(1)
		go func(idx int, k []byte) {
			defer wg.Done()

			hashedKey := t.hashKey(k)
			value, _, _, err := t.get(t.root, node.KeybytesToHex(hashedKey), 0)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errors[idx] = err
				results[idx] = nil
			} else {
				results[idx] = value
			}
		}(i, key)
	}

	wg.Wait()

	// Check for errors
	for _, err := range errors {
		if err != nil {
			return nil, fmt.Errorf("error occurred during parallel get: %v", errors)
		}
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
		panic(fmt.Sprintf("%T: invalid node: %v", origNode, origNode))
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
			t.tracer.oldKeys = append(t.tracer.oldKeys, n.Flags.Hash)
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
			t.tracer.oldKeys = append(t.tracer.oldKeys, n.Flags.Hash)
			return true, branch, nil
		}
		// New branch node is created as a child of the original short node.
		// Track the newly inserted node in the tracer. The node identifier
		// passed is the path from the root node.
		t.tracer.onInsert(append(prefix, key[:matchlen]...))

		// Replace it with a short node leading up to the branch.
		t.tracer.oldKeys = append(t.tracer.oldKeys, n.Flags.Hash)
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
		t.tracer.oldKeys = append(t.tracer.oldKeys, n.Flags.Hash)
		n = n.Copy()
		n.Flags = node.NewFlag()
		n.Children[key[0]] = nn
		return true, n, nil

	case nil:
		// New short node is created and track it in the tracer. The node identifier
		// passed is the path from the root node. Note the valueNode won't be tracked
		// since it's always embedded in its parent.
		t.tracer.onInsert(prefix)
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
		panic(fmt.Sprintf("%T: invalid node: %v", n, n))
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

// func (t *MerklePatriciaTrie) Delete(key []byte) error {
// 	return nil
// }

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
			t.tracer.onDelete(prefix)

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
			t.tracer.onDelete(append(prefix, n.Key...))

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
					t.tracer.onDelete(append(prefix, byte(pos)))

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
		panic(fmt.Sprintf("%T: invalid node: %v (%v)", n, n, key))
	}
}

func (t *MerklePatriciaTrie) Hash() e_common.Hash {
	hash, cached := t.hashRoot()
	t.root = cached
	return e_common.BytesToHash(hash.(node.HashNode))
}

// func (t *MerklePatriciaTrie) Commit(
// 	collectLeaf bool,
// ) (hash e_common.Hash, nodeSet *node.NodeSet, oldKeys [][]byte, err error) {
// 	defer t.tracer.reset()
// 	defer func() {
// 		t.committed = true
// 	}()
// 	// Trie is empty and can be classified into two types of situations:
// 	// (a) The trie was empty and no update happens => return nil
// 	// (b) The trie was non-empty and all nodes are dropped => return
// 	//     the node set includes all deleted nodes
// 	if t.root == nil {
// 		paths := t.tracer.deletedNodes()
// 		if len(paths) == 0 {
// 			return EmptyRootHash, nil, t.tracer.oldKeys, nil // case (a)
// 		}
// 		nodes := node.NewNodeSet()
// 		for _, path := range paths {
// 			nodes.AddNode([]byte(path), node.NewDeleted())
// 		}
// 		return EmptyRootHash, nodes, t.tracer.oldKeys, nil // case (b)
// 	}
// 	// Derive the hash for all dirty nodes first. We hold the assumption
// 	// in the following procedure that all nodes are hashed.
// 	rootHash := t.Hash()

// 	// Do a quick check if we really need to commit. This can happen e.g.
// 	// if we load a trie for reading storage values, but don't write to it.
// 	if hashedNode, dirty := t.root.Cache(); !dirty {
// 		// Replace the root node with the origin hash in order to
// 		// ensure all resolved nodes are dropped after the commit.
// 		t.root = hashedNode
// 		return rootHash, nil, t.tracer.oldKeys, nil
// 	}
// 	nodes := node.NewNodeSet()
// 	for _, path := range t.tracer.deletedNodes() {
// 		nodes.AddNode([]byte(path), node.NewDeleted())
// 	}
// 	t.root = newCommitter(nodes, t.tracer, collectLeaf).Commit(t.root)
// 	return rootHash, nodes, t.tracer.oldKeys, nil
// }

// func (t *MerklePatriciaTrie) Commit(
// 	collectLeaf bool,
// ) (hash e_common.Hash, nodeSet *node.NodeSet, oldKeys [][]byte, err error) {
// 	commitTrie := t.Copy()

// 	defer t.tracer.reset()
// 	defer func() {
// 		commitTrie.committed = true
// 	}()

// 	// Tạo bản sao của trie cho commit
// 	// commitTrie := t.Copy()

// 	// Trie is empty and can be classified into two types of situations:
// 	// (a) The trie was empty and no update happens => return nil
// 	// (b) The trie was non-empty and all nodes are dropped => return
// 	//     the node set includes all deleted nodes
// 	if commitTrie.root == nil { // Sử dụng commitTrie.root thay vì t.root
// 		paths := commitTrie.tracer.deletedNodes() // Sử dụng commitTrie.tracer
// 		if len(paths) == 0 {
// 			return EmptyRootHash, nil, commitTrie.tracer.oldKeys, nil // case (a), sử dụng commitTrie.tracer.oldKeys
// 		}
// 		nodes := node.NewNodeSet()
// 		for _, path := range paths {
// 			nodes.AddNode([]byte(path), node.NewDeleted())
// 		}
// 		return EmptyRootHash, nodes, commitTrie.tracer.oldKeys, nil // case (b), sử dụng commitTrie.tracer.oldKeys
// 	}
// 	// Derive the hash for all dirty nodes first. We hold the assumption
// 	// in the following procedure that all nodes are hashed.
// 	rootHash := commitTrie.Hash() // Sử dụng commitTrie.Hash()

// 	// Do a quick check if we really need to commit. This can happen e.g.
// 	// if we load a trie for reading storage values, but don't write to it.
// 	if hashedNode, dirty := commitTrie.root.Cache(); !dirty { // Sử dụng commitTrie.root
// 		// Replace the root node with the origin hash in order to
// 		// ensure all resolved nodes are dropped after the commit.
// 		commitTrie.root = hashedNode                         // Sử dụng commitTrie.root
// 		t.root = commitTrie.root                             // Cập nhật root của trie gốc sau khi commit trên bản sao
// 		return rootHash, nil, commitTrie.tracer.oldKeys, nil // Sử dụng commitTrie.tracer.oldKeys
// 	}
// 	nodes := node.NewNodeSet()
// 	for _, path := range commitTrie.tracer.deletedNodes() { // Sử dụng commitTrie.tracer
// 		nodes.AddNode([]byte(path), node.NewDeleted())
// 	}
// 	commitTrie.root = newCommitter(nodes, commitTrie.tracer, collectLeaf).Commit(commitTrie.root) // Sử dụng commitTrie.root và commitTrie.tracer
// 	rootHash = commitTrie.Hash()                                                                  // Cập nhật rootHash sau commit trên bản sao
// 	t.root = commitTrie.root                                                                      // Cập nhật root của trie gốc sau khi commit trên bản sao
// 	return rootHash, nodes, commitTrie.tracer.oldKeys, nil                                        // Sử dụng commitTrie.tracer.oldKeys
// }

func (t *MerklePatriciaTrie) Commit(
	collectLeaf bool,
) (hash e_common.Hash, nodeSet *node.NodeSet, oldKeys [][]byte, err error) {
	// Use the original trie's tracer for tracking changes up to the commit.
	// Reset it *after* we've used its data (deletedNodes, oldKeys).
	defer t.tracer.reset() // Reset the original trie's tracer

	// If the original trie is empty (no modifications ever, or deleted back to empty)
	if t.root == nil {
		paths := t.tracer.deletedNodes() // Use original tracer to check for deletions
		// If no deletions occurred, it was always empty.
		if len(paths) == 0 {
			return EmptyRootHash, nil, t.tracer.oldKeys, nil // case (a)
		}
		// If deletions occurred, it means it became empty.
		nodes := node.NewNodeSet()
		for _, path := range paths {
			nodes.AddNode([]byte(path), node.NewDeleted())
		}
		// The original tracer's oldKeys are returned.
		return EmptyRootHash, nodes, t.tracer.oldKeys, nil // case (b)
	}

	// Create a temporary copy to perform hashing and commit logic without
	// modifying the original trie's node structure (cache).
	commitTrie := t.Copy()
	// Mark the temporary copy as committed conceptually after this function.
	// This doesn't affect the original trie 't'.
	defer func() {
		commitTrie.committed = true // Mark the temporary copy
	}()

	// Calculate the root hash of the *current state* using the temporary copy.
	// The Hash() method on the copy might update the cache state within commitTrie's nodes.
	rootHash := commitTrie.Hash() // This updates commitTrie.root to its HashNode representation

	// Check if the root node *in the copy* became dirty (or was already dirty).
	// The Hash() call above ensures the cache state reflects the hash calculation result.
	_, dirty := commitTrie.root.Cache() // Check the copy's root cache state

	if !dirty {
		// If not dirty (no changes since the last hash calculation on a similar structure),
		// no new nodes need to be written.
		// Return the calculated hash and the original tracer's oldKeys.
		// Do *not* modify t.root.
		return rootHash, nil, t.tracer.oldKeys, nil // Use original tracer's oldKeys
	}

	// If dirty, we need to generate the nodeSet containing changes.
	nodes := node.NewNodeSet()
	// Collect deleted node paths from the original trie's tracer.
	for _, path := range t.tracer.deletedNodes() { // Use original tracer
		nodes.AddNode([]byte(path), node.NewDeleted())
	}

	// Run the committer on the *copy's* root node structure (which might now be a HashNode
	// after the commitTrie.Hash() call, or the full structure if it was dirty).
	// Use the *original* tracer to provide metadata about insertions/deletions to the committer.
	// The committer populates the 'nodes' NodeSet with the RLP data of changed nodes.
	// We don't need the return value (the final hash node) from the Commit call here,
	// as we already have the correct rootHash calculated earlier.
	_ = newCommitter(nodes, t.tracer, collectLeaf).Commit(commitTrie.root) // Use original tracer, run on copy's root

	// Return the rootHash calculated before running the committer, the populated nodeSet,
	// and the original tracer's oldKeys.
	// Do *not* modify t.root.
	return rootHash, nodes, t.tracer.oldKeys, nil // Use original tracer's oldKeys
}

func (t *MerklePatriciaTrie) Reset() {
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
	t.tracer.onRead(prefix, blob)
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

func (t *MerklePatriciaTrie) GetStorageKeys() []e_common.Hash {
	rootHashNode, _ := t.root.Cache()
	root := e_common.BytesToHash(rootHashNode)
	// copy to new trie with root hash
	trie := &MerklePatriciaTrie{
		reader: t.reader,
		isHash: t.isHash,
		tracer: newTracer(),
	}
	if root != (e_common.Hash{}) && root != EmptyRootHash {
		rootnode, err := trie.resolveAndTrack(root[:], nil)
		if err != nil {
			logger.Error("error when resolve and track root node", err)
			return nil
		}
		trie.root = rootnode
	}
	// not yet process nodes: which nodes can have hash node in it
	unprocessedNodes := []node.Node{trie.root}
	storageKeys := []e_common.Hash{root}
	for len(unprocessedNodes) > 0 {
		uNode := unprocessedNodes[0]
		unprocessedNodes = unprocessedNodes[1:]
		switch n := uNode.(type) {
		case *node.ShortNode:
			switch n.Val.(type) {
			case node.HashNode:
				storageKeys = append(storageKeys, e_common.BytesToHash(n.Val.(node.HashNode)))
				rn, err := t.resolveAndTrack(n.Val.(node.HashNode), nil)
				if err != nil {
					logger.Error("error when resolve and track short node", err)
				}
				n.Val = rn
			default:
			}
			unprocessedNodes = append(unprocessedNodes, n.Val)
		case *node.FullNode:
			for i, child := range n.Children {
				if child != nil {
					switch child.(type) {
					case node.HashNode:
						storageKeys = append(storageKeys, e_common.BytesToHash(child.(node.HashNode)))
						var err error
						n.Children[i], err = t.resolveAndTrack(child.(node.HashNode), nil)
						if err != nil {
							hash, _ := n.Cache()
							logger.Error("error when resolve and track full node", err, n, hex.EncodeToString(hash))
							logger.DebugP("index", i)
						}
					default:
					}
					unprocessedNodes = append(unprocessedNodes, n.Children[i])
				}
			}
		case node.HashNode:
			logger.Warn("String function revice hashNode:", n)
			storageKeys = append(storageKeys, e_common.BytesToHash(n))
			rn, err := t.resolveAndTrack(n, nil) // nil prefix because we dont need it to able to solve hash node
			if err != nil {
				logger.Error("error when resolve and track hash node", err)
			}
			unprocessedNodes = append(unprocessedNodes, rn)
		case node.ValueNode:
		}
	}
	return storageKeys
}

func (t *MerklePatriciaTrie) String() string {
	rootHashNode, _ := t.root.Cache()
	root := e_common.BytesToHash(rootHashNode)
	// copy to new trie with root hash
	trie := &MerklePatriciaTrie{
		reader: t.reader,
		isHash: t.isHash,
		tracer: newTracer(),
	}

	if root != (e_common.Hash{}) && root != EmptyRootHash {
		rootnode, err := trie.resolveAndTrack(root[:], nil)
		if err != nil {
			logger.Error("error when resolve and track root node", err)
			return ""
		}
		trie.root = rootnode
	}
	// not yet process nodes: which nodes can have hash node in it
	unprocessedNodes := []node.Node{trie.root}
	for len(unprocessedNodes) > 0 {
		uNode := unprocessedNodes[0]
		unprocessedNodes = unprocessedNodes[1:]
		switch n := uNode.(type) {
		case *node.ShortNode:
			switch n.Val.(type) {
			case node.HashNode:
				rn, err := t.resolveAndTrack(n.Val.(node.HashNode), nil)
				if err != nil {
					logger.Error("error when resolve and track short node", err)
					continue
				}
				n.Val = rn
			default:
			}
			unprocessedNodes = append(unprocessedNodes, n.Val)
		case *node.FullNode:
			for i, child := range n.Children {
				if child != nil {
					switch child.(type) {
					case node.HashNode:
						var err error
						n.Children[i], err = t.resolveAndTrack(child.(node.HashNode), nil)
						if err != nil {
							hash, _ := n.Cache()
							logger.Error("error when resolve and track full node", err, n, hex.EncodeToString(hash))
							logger.DebugP("index", i)
						}
					default:
					}
					unprocessedNodes = append(unprocessedNodes, n.Children[i])
				}
			}
		case node.HashNode:
			logger.Warn("String function revice hashNode:", n)
			rn, err := t.resolveAndTrack(n, nil) // nil prefix because we dont need it to able to solve hash node
			if err != nil {
				logger.Error("error when resolve and track hash node", err)
			}
			unprocessedNodes = append(unprocessedNodes, rn)

		case node.ValueNode:
			logger.Debug("value node:", hex.EncodeToString(n))
		}
	}
	return trie.root.FString("==>")
}

func GetRootHash(
	data map[string][]byte,
) (e_common.Hash, error) {
	// create mem storage
	memStorage := storage.NewMemoryDb()
	trie, err := New(e_common.Hash{}, memStorage, false)
	if err != nil {
		logger.Error("error when create new trie", err)
		return e_common.Hash{}, err
	}
	for k, v := range data {
		trie.Update(e_common.FromHex(k), v)
	}
	hash := trie.Hash()
	memStorage = nil
	trie = nil
	return hash, nil
}

func (t *MerklePatriciaTrie) GetKeyValue() (map[string][]byte, error) {
	if t.committed {
		return nil, ErrCommitted
	}

	data := make(map[string][]byte)
	err := t.getKeyValue(t.root, nil, data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (t *MerklePatriciaTrie) getKeyValue(
	origNode node.Node,
	prefix []byte,
	data map[string][]byte,
) error {
	switch n := (origNode).(type) {
	case nil:
		return nil
	case node.ValueNode:
		// Key is the path from the root to this node
		key := append(prefix, node.KeybytesToHex(crypto.Keccak256(prefix))...)
		data[hex.EncodeToString(key)] = n
		return nil
	case *node.ShortNode:
		// Key is the path from the root to this node
		key := append(prefix, n.Key...)
		// Recursively traverse the value node
		err := t.getKeyValue(n.Val, key, data)
		if err != nil {
			fmt.Print("🟡")
			return err
		}
		return nil
	case *node.FullNode:
		for i, child := range n.Children {
			if child != nil {
				// Recursively traverse each child node
				err := t.getKeyValue(child, append(prefix, byte(i)), data)
				if err != nil {
					fmt.Print("🟢")
					return err
				}
			}
		}
		return nil
	case node.HashNode:
		// Resolve the hash node and recursively traverse it
		child, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			fmt.Print("🔴")
			return err
		}
		return t.getKeyValue(child, prefix, data)
	default:
		panic(fmt.Sprintf("%T: invalid node: %v", origNode, origNode))
	}
}
func (t *MerklePatriciaTrie) GetAll() (map[string][]byte, error) {
	if t.committed {
		return nil, ErrCommitted
	}

	data := make(map[string][]byte)
	err := t.getAll(t.root, nil, data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (t *MerklePatriciaTrie) getAll(
	origNode node.Node,
	prefix []byte,
	data map[string][]byte,
) error {
	switch n := (origNode).(type) {
	case nil:
		return nil
	case node.ValueNode:
		key := hex.EncodeToString(append(prefix, node.KeybytesToHex(crypto.Keccak256(prefix))...))
		data[key] = n
		return nil
	case *node.ShortNode:
		key := append(prefix, n.Key...)
		err := t.getAll(n.Val, key, data)
		if err != nil {
			return err
		}
		return nil
	case *node.FullNode:
		for i, child := range n.Children {
			if child != nil {
				err := t.getAll(child, append(prefix, byte(i)), data)
				if err != nil {
					return err
				}
			}
		}
		return nil
	case node.HashNode:
		child, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			return err
		}
		return t.getAll(child, prefix, data)
	default:
		panic(fmt.Sprintf("%T: invalid node: %v", origNode, origNode))
	}
}

// Count trả về tổng số cặp key-value trong trie.
func (t *MerklePatriciaTrie) Count() (int, error) {
	if t.committed {
		return 0, ErrCommitted
	}

	count := 0
	// Sử dụng một con trỏ tới count để hàm đệ quy có thể cập nhật nó
	err := t.countRecursive(t.root, nil, &count)
	if err != nil {
		return 0, fmt.Errorf("lỗi khi đếm phần tử: %w", err)
	}

	return count, nil
}

// countRecursive là hàm đệ quy giúp đếm số lượng phần tử.
func (t *MerklePatriciaTrie) countRecursive(
	origNode node.Node,
	prefix []byte,
	count *int, // Sử dụng con trỏ để cập nhật giá trị count
) error {
	switch n := (origNode).(type) {
	case nil:
		return nil // Nút rỗng, không làm gì cả
	case node.ValueNode:
		*count++ // Gặp nút giá trị (lá), tăng bộ đếm
		return nil
	case *node.ShortNode:
		// Tiếp tục duyệt xuống nút con (Val) với prefix được cập nhật
		key := append(prefix, n.Key...)
		return t.countRecursive(n.Val, key, count)
	case *node.FullNode:
		// Duyệt qua tất cả các nút con không rỗng
		for i, child := range n.Children {
			if child != nil {
				err := t.countRecursive(child, append(prefix, byte(i)), count)
				if err != nil {
					return err // Nếu có lỗi ở nhánh con, trả về lỗi
				}
			}
		}
		return nil
	case node.HashNode:
		// Giải quyết nút hash và tiếp tục duyệt
		child, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			logger.Error("Lỗi khi resolve hash node trong lúc đếm:", err, "Prefix:", hex.EncodeToString(prefix))
			return fmt.Errorf("không thể resolve hash node %s: %w", hex.EncodeToString(n), err)
		}
		return t.countRecursive(child, prefix, count)
	default:
		// Trường hợp không mong muốn
		panic(fmt.Sprintf("%T: kiểu node không hợp lệ khi đếm: %v", origNode, origNode))
	}
}
