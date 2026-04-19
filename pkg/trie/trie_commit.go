package trie

import (
"fmt"
"sync"

e_common "github.com/ethereum/go-ethereum/common"
"github.com/meta-node-blockchain/meta-node/pkg/logger"

"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// PreWarm resolves HashNodes along the trie paths for the given keys in PARALLEL.
// This warms the PebbleDB / LevelDB block cache so subsequent BatchUpdate operations
// hit memory instead of disk I/O, significantly reducing BatchUpdate latency.
//
// PARALLELISM: Keys are partitioned by the first nibble of their hashed form (0-F),
// giving 16 independent subtrees that can be warmed concurrently.
// Each goroutine gets its own sub-trie root slice to avoid shared-state mutations.
//
// FORK-SAFETY: This method only READS from the trie (via get → resolveAndTrack).
// It does not modify any trie structure used for hash computation.
// The resolved nodes replace HashNodes in the in-memory trie, which is the
// exact same thing that get() and insert() already do — just done upfront.
// The final trie.Hash() result is identical regardless of whether nodes
// were pre-resolved or resolved lazily during insert.
//
// This should be called on a trie Copy() to avoid holding muTrie lock.
func (t *MerklePatriciaTrie) PreWarm(keys [][]byte) {
	if t.committed || t.root == nil || len(keys) == 0 {
		return
	}

	// Hash all keys and group by first nibble
	type hashedKey struct {
		hexKey []byte
		raw    []byte
	}
	buckets := [16][]hashedKey{}
	for _, key := range keys {
		hk := t.hashKey(key)
		hexKey := node.KeybytesToHex(hk)
		if len(hexKey) < 1 {
			continue
		}
		nib := hexKey[0]
		if nib < 16 {
			buckets[nib] = append(buckets[nib], hashedKey{hexKey: hexKey, raw: hk})
		}
	}

	// If root is a HashNode, resolve it first
	if hn, ok := t.root.(node.HashNode); ok {
		resolved, err := t.resolveAndTrack(hn, nil)
		if err != nil {
			return
		}
		t.root = resolved
	}

	// If root is not a FullNode, fall back to sequential
	rootFull, isFullNode := t.root.(*node.FullNode)
	if !isFullNode {
		for _, key := range keys {
			hk := t.hashKey(key)
			_, newnode, didResolve, _ := t.get(t.root, node.KeybytesToHex(hk), 0)
			if didResolve {
				t.root = newnode
			}
		}
		return
	}

	// Parallel PreWarm: each goroutine warms one subtree independently
	var wg sync.WaitGroup
	for nib := byte(0); nib < 16; nib++ {
		if len(buckets[nib]) == 0 {
			continue
		}
		wg.Add(1)
		go func(nibble byte, bucket []hashedKey) {
			defer wg.Done()
			// Make a sub-trie rooted at this nibble's child
			subTrie := &MerklePatriciaTrie{
				root:   rootFull.Children[nibble],
				reader: t.reader,
				isHash: t.isHash,
				tracer: newTracer(),
			}
			for _, kh := range bucket {
				if len(kh.hexKey) <= 1 {
					continue
				}
				// Traverse the sub-key (skip first nibble already used for partitioning)
				_, newnode, didResolve, _ := subTrie.get(subTrie.root, kh.hexKey[1:], 0)
				if didResolve {
					subTrie.root = newnode
				}
			}
		}(nib, buckets[nib])
	}
	wg.Wait()
}
// BatchUpdate performs parallel trie updates by partitioning keys by first nibble.
//
// The MPT root is a FullNode with 16 children (nibbles 0-F). Updates to keys
// with different first nibbles are COMPLETELY INDEPENDENT — they touch different
// subtrees. This allows 16-way parallelism.
//
// FORK-SAFETY: The MPT structure is uniquely determined by the SET of key-value
// pairs, NOT the insertion order. Parallel subtree updates produce an identical
// trie to sequential updates.
//
// keys and values must be pre-hashed (raw 32-byte keys, NOT hex-encoded).
// They must be the same length.
func (t *MerklePatriciaTrie) BatchUpdate(keys, values [][]byte) error {
	if t.committed {
		return ErrCommitted
	}
	if len(keys) != len(values) {
		return fmt.Errorf("BatchUpdate: keys and values length mismatch (%d vs %d)", len(keys), len(values))
	}
	if len(keys) == 0 {
		return nil
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 1: Hash keys and convert to hex nibbles (PARALLEL)
	// Keccak256 is CPU-bound, so we parallelize across 8 workers.
	// FORK-SAFETY: hashKey + KeybytesToHex are pure functions.
	// ═══════════════════════════════════════════════════════════════
	type kvPair struct {
		hexKey []byte
		value  []byte
	}

	// Pre-hash all keys in parallel
	allKVs := make([]kvPair, len(keys))
	{
		const hashWorkers = 8
		var hashWg sync.WaitGroup
		chunkSize := (len(keys) + hashWorkers - 1) / hashWorkers
		for w := 0; w < hashWorkers; w++ {
			start := w * chunkSize
			end := start + chunkSize
			if end > len(keys) {
				end = len(keys)
			}
			if start >= len(keys) {
				break
			}
			hashWg.Add(1)
			go func(s, e int) {
				defer hashWg.Done()
				for i := s; i < e; i++ {
					hashedKey := t.hashKey(keys[i])
					allKVs[i] = kvPair{
						hexKey: node.KeybytesToHex(hashedKey),
						value:  values[i],
					}
				}
			}(start, end)
		}
		hashWg.Wait()
	}

	// Partition by first nibble (0-15) → 16 buckets
	buckets := [16][]kvPair{}
	for _, kv := range allKVs {
		if len(kv.hexKey) == 0 {
			continue
		}
		nibble := kv.hexKey[0]
		if nibble >= 16 {
			continue
		}
		buckets[nibble] = append(buckets[nibble], kv)
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 2: Ensure root is a FullNode (resolve if HashNode)
	// ═══════════════════════════════════════════════════════════════
	if t.root == nil {
		t.root = &node.FullNode{Flags: node.NewFlag()}
	}

	// If root is a HashNode, resolve it
	if hn, ok := t.root.(node.HashNode); ok {
		resolved, err := t.resolveAndTrack(hn, nil)
		if err != nil {
			return fmt.Errorf("BatchUpdate: failed to resolve root: %w", err)
		}
		t.root = resolved
	}

	// If root is a ShortNode, we need to handle it specially.
	// For a trie with many keys, root is almost always a FullNode.
	// For small tries with a ShortNode root, fall back to sequential.
	rootFull, isFullNode := t.root.(*node.FullNode)
	if !isFullNode {
		// Fallback: sequential update for non-FullNode roots
		for i, key := range keys {
			if err := t.Update(key, values[i]); err != nil {
				return err
			}
		}
		return nil
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 3: Parallel subtree updates (16 goroutines)
	// Each goroutine processes one nibble bucket independently.
	// ═══════════════════════════════════════════════════════════════
	type subtreeResult struct {
		nibble     byte
		child      node.Node
		fastTracer *FastTracer
		err        error
	}

	resultChan := make(chan subtreeResult, 16)
	activeCount := 0

	// Track the root's old hash for tracer
	rootDirty := false

	for nibble := byte(0); nibble < 16; nibble++ {
		if len(buckets[nibble]) == 0 {
			continue
		}
		activeCount++

		go func(nib byte, pairs []kvPair) {
			// Each goroutine gets its own FastTracer (regular maps, ~5x faster)
			// FORK-SAFETY: exclusive access per goroutine, no data races
			subFastTracer := newFastTracer()
			subTrie := &MerklePatriciaTrie{
				root:       rootFull.Children[nib],
				reader:     t.reader,
				isHash:     t.isHash,
				fastTracer: subFastTracer,
			}

			// Sequential updates within this subtree partition
			for _, kv := range pairs {
				// Skip first nibble (already used for partitioning)
				remainingKey := kv.hexKey[1:]
				if len(kv.value) != 0 {
					_, n, insertErr := subTrie.insert(
						subTrie.root,
						[]byte{nib}, // prefix = the nibble
						remainingKey,
						node.ValueNode(kv.value),
					)
					if insertErr != nil {
						resultChan <- subtreeResult{nibble: nib, err: insertErr}
						return
					}
					subTrie.root = n
				} else {
					_, n, deleteErr := subTrie.delete(
						subTrie.root,
						[]byte{nib},
						remainingKey,
					)
					if deleteErr != nil {
						resultChan <- subtreeResult{nibble: nib, err: deleteErr}
						return
					}
					subTrie.root = n
				}
			}

			resultChan <- subtreeResult{
				nibble:     nib,
				child:      subTrie.root,
				fastTracer: subFastTracer,
			}
		}(nibble, buckets[nibble])
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 4: Collect results and merge into root FullNode
	// ═══════════════════════════════════════════════════════════════
	newRoot := rootFull.Copy()
	newRoot.Flags = node.NewFlag()

	for i := 0; i < activeCount; i++ {
		result := <-resultChan
		if result.err != nil {
			return fmt.Errorf("BatchUpdate: subtree %x failed: %w", result.nibble, result.err)
		}
		newRoot.Children[result.nibble] = result.child
		rootDirty = true

		// Merge subtree FastTracer into main Tracer (regular map iteration, fast)
		t.tracer.mergeFast(result.fastTracer)
	}

	if rootDirty {
		// Track root's old hash
		t.tracer.oldKeys = append(t.tracer.oldKeys, rootFull.Flags.Hash)
		t.root = newRoot
	}

	return nil
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
	commitTrie := t.copyInternal()
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
		logger.Error("❌ [TRIE DEBUG] early return because !dirty=true. root type=%T", commitTrie.root)
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
	logger.Info("✅ [TRIE DEBUG] Calling committer.Commit! commitTrie.root is %T. nodes=%d", commitTrie.root, len(nodes.Nodes))
	_ = newCommitter(nodes, t.tracer, collectLeaf).Commit(commitTrie.root) // Use original tracer, run on copy's root
	logger.Info("✅ [TRIE DEBUG] After committer.Commit! nodes=%d", len(nodes.Nodes))

	// Clear the dirty flag on all nodes in the original trie so that future 
	// commits only process new modifications, keeping commit time constant.
	t.ClearDirty()

	// Return the rootHash calculated before running the committer, the populated nodeSet,
	// and the original tracer's oldKeys.
	// Do *not* modify t.root.

	// Store lastCommitBatch for network replication (AccountBatch → Sub nodes).
	// Build batch from nodeSet: {nodeHash → nodeBlob} pairs.
	batch := make([][2][]byte, 0, len(nodes.Nodes))
	for _, n := range nodes.Nodes {
		if n.IsDeleted() || n.Hash == (e_common.Hash{}) {
			continue
		}
		batch = append(batch, [2][]byte{n.Hash.Bytes(), n.Blob})
	}
	t.lastCommitBatch = batch

	return rootHash, nodes, t.tracer.oldKeys, nil // Use original tracer's oldKeys
}

// GetCommitBatch returns the node hash→blob pairs from the last Commit().
// Used by AccountStateDB to build AccountBatch for network replication to Sub nodes.
// This is a one-shot read: calling it clears the stored batch to free memory.
func (t *MerklePatriciaTrie) GetCommitBatch() [][2][]byte {
	batch := t.lastCommitBatch
	t.lastCommitBatch = nil
	return batch
}

func (t *MerklePatriciaTrie) Reset() {
}
