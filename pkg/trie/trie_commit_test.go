package trie

import (
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// ══════════════════════════════════════════════════════════════════════════════
// Commit Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_Commit_Basic(t *testing.T) {
	tr := newTestMPT(t)

	_ = tr.Update(mptKey(0x01), mptValue(0x01))
	_ = tr.Update(mptKey(0x02), mptValue(0x02))

	preHash := tr.Hash()

	hash, nodeSet, oldKeys, err := tr.Commit(true)
	require.NoError(t, err)
	assert.Equal(t, preHash, hash, "committed hash should match pre-commit hash")
	assert.NotNil(t, nodeSet, "nodeSet should not be nil")
	_ = oldKeys // oldKeys may or may not be populated
}

func TestMPT_Commit_Persists(t *testing.T) {
	db := storage.NewMemoryDb()
	tr, err := New(e_common.Hash{}, db, true)
	require.NoError(t, err)

	_ = tr.Update(mptKey(0x01), mptValue(0x01))
	_ = tr.Update(mptKey(0x02), mptValue(0x02))

	hash, nodeSet, _, err := tr.Commit(true)
	require.NoError(t, err)

	// Write nodeSet to DB
	if nodeSet != nil {
		nodeSet.ForEachWithOrder(func(path string, n *node.NodeWrapper) {
			if !n.IsDeleted() {
				_ = db.Put(n.Hash[:], n.Blob)
			}
		})
	}

	// Create new trie from committed hash — should be able to read data
	tr2, err := New(hash, db, true)
	require.NoError(t, err)

	got, err := tr2.Get(mptKey(0x01))
	require.NoError(t, err)
	assert.Equal(t, mptValue(0x01), got)

	got, err = tr2.Get(mptKey(0x02))
	require.NoError(t, err)
	assert.Equal(t, mptValue(0x02), got)
}

// ══════════════════════════════════════════════════════════════════════════════
// BatchUpdate Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMPT_BatchUpdate_Basic(t *testing.T) {
	tr := newTestMPT(t)

	keys := make([][]byte, 20)
	values := make([][]byte, 20)
	for i := byte(0); i < 20; i++ {
		keys[i] = mptKey(i)
		values[i] = mptValue(i)
	}

	err := tr.BatchUpdate(keys, values)
	require.NoError(t, err)

	// Verify all keys
	for i := byte(0); i < 20; i++ {
		got, err := tr.Get(mptKey(i))
		require.NoError(t, err)
		assert.Equal(t, mptValue(i), got, "BatchUpdate key %d mismatch", i)
	}
}

func TestMPT_BatchUpdate_MismatchedLengths(t *testing.T) {
	tr := newTestMPT(t)

	keys := make([][]byte, 3)
	values := make([][]byte, 2)

	err := tr.BatchUpdate(keys, values)
	assert.Error(t, err, "mismatched keys/values should error")
}

func TestMPT_BatchUpdate_SameResult_AsSequential(t *testing.T) {
	// BatchUpdate and sequential Update should produce identical trie hash
	keys := make([][]byte, 30)
	values := make([][]byte, 30)
	for i := byte(0); i < 30; i++ {
		keys[i] = mptKey(i)
		values[i] = mptValue(i)
	}

	// Sequential
	trSeq := newTestMPT(t)
	for i := 0; i < 30; i++ {
		_ = trSeq.Update(keys[i], values[i])
	}
	seqHash := trSeq.Hash()

	// Batch
	trBatch := newTestMPT(t)
	err := trBatch.BatchUpdate(keys, values)
	require.NoError(t, err)
	batchHash := trBatch.Hash()

	assert.Equal(t, seqHash, batchHash, "BatchUpdate and sequential Update should produce same hash")
}

func TestMPT_BatchUpdate_Empty(t *testing.T) {
	tr := newTestMPT(t)
	hashBefore := tr.Hash()

	err := tr.BatchUpdate(nil, nil)
	require.NoError(t, err)

	assert.Equal(t, hashBefore, tr.Hash(), "empty batch should not change hash")
}
