package parentchain

import (
	"crypto/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T-U10: Test binary Merkle tree: root, member proof, 0/1/odd/even leaves.
func TestMerkleTree_RootsAndProofs(t *testing.T) {
	// 1. 0 leaves
	emptyRoot := ComputeMerkleRoot(nil)
	assert.Equal(t, common.Hash{}, emptyRoot)

	// 2. 1 leaf
	leaf1 := crypto.Keccak256Hash([]byte("tx1"))
	root1 := ComputeMerkleRoot([]common.Hash{leaf1})
	assert.Equal(t, leaf1, root1)

	rootGen1, proof1, err := GenerateMerkleProof([]common.Hash{leaf1}, 0)
	require.NoError(t, err)
	assert.Equal(t, root1, rootGen1)
	assert.Empty(t, proof1)
	assert.True(t, VerifyMerkleProof(root1, leaf1, 0, proof1))

	// 3. Even leaves (2, 4, 8)
	for _, count := range []int{2, 4, 8, 16} {
		leaves := make([]common.Hash, count)
		for i := 0; i < count; i++ {
			b := make([]byte, 32)
			_, _ = rand.Read(b)
			leaves[i] = crypto.Keccak256Hash(b)
		}
		root := ComputeMerkleRoot(leaves)
		assert.NotEqual(t, common.Hash{}, root)

		for i := 0; i < count; i++ {
			r, proof, err := GenerateMerkleProof(leaves, i)
			require.NoError(t, err)
			assert.Equal(t, root, r)
			assert.True(t, VerifyMerkleProof(root, leaves[i], i, proof), "proof should verify for leaf %d", i)

			// Tampered leaf should fail
			fakeLeaf := crypto.Keccak256Hash([]byte("fake"))
			assert.False(t, VerifyMerkleProof(root, fakeLeaf, i, proof))

			// Tampered index should fail
			if count > 1 {
				wrongIdx := (i + 1) % count
				assert.False(t, VerifyMerkleProof(root, leaves[i], wrongIdx, proof))
			}
		}
	}

	// 4. Odd leaves (3, 5, 7, 13)
	for _, count := range []int{3, 5, 7, 13, 27} {
		leaves := make([]common.Hash, count)
		for i := 0; i < count; i++ {
			b := make([]byte, 32)
			_, _ = rand.Read(b)
			leaves[i] = crypto.Keccak256Hash(b)
		}
		root := ComputeMerkleRoot(leaves)
		assert.NotEqual(t, common.Hash{}, root)

		for i := 0; i < count; i++ {
			r, proof, err := GenerateMerkleProof(leaves, i)
			require.NoError(t, err)
			assert.Equal(t, root, r)
			assert.True(t, VerifyMerkleProof(root, leaves[i], i, proof), "proof should verify for leaf %d of %d", i, count)
		}
	}

	// 5. Deterministic test vector
	fixedLeaves := []common.Hash{
		crypto.Keccak256Hash([]byte("leaf0")),
		crypto.Keccak256Hash([]byte("leaf1")),
		crypto.Keccak256Hash([]byte("leaf2")),
	}
	fixedRoot := ComputeMerkleRoot(fixedLeaves)
	// Compute expected by hand:
	// Level 0: [L0, L1, L2, L2 (duplicated)]
	// Level 1: [H(L0||L1), H(L2||L2)]
	// Level 2: H(Level 1[0] || Level 1[1])
	h01 := crypto.Keccak256Hash(append(fixedLeaves[0].Bytes(), fixedLeaves[1].Bytes()...))
	h22 := crypto.Keccak256Hash(append(fixedLeaves[2].Bytes(), fixedLeaves[2].Bytes()...))
	expectedRoot := crypto.Keccak256Hash(append(h01.Bytes(), h22.Bytes()...))
	assert.Equal(t, expectedRoot, fixedRoot)

	// Check proof for leaf 2
	_, proof2, err := GenerateMerkleProof(fixedLeaves, 2)
	require.NoError(t, err)
	// Sibling at level 0 is duplicated L2; sibling at level 1 is h01
	assert.Equal(t, []common.Hash{fixedLeaves[2], h01}, proof2)
	assert.True(t, VerifyMerkleProof(fixedRoot, fixedLeaves[2], 2, proof2))
}

func TestTxsRoot_ReceiptsRoot(t *testing.T) {
	txs := [][]byte{[]byte("txA"), []byte("txB")}
	rootA := TxsRoot(txs)
	rootB := ReceiptsRoot(txs)
	assert.Equal(t, rootA, rootB)
	assert.NotEqual(t, common.Hash{}, rootA)
}
