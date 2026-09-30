package parentchain

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	ErrEmptyLeaves   = errors.New("merkle: cannot generate proof for empty leaves")
	ErrIndexOverflow = errors.New("merkle: index out of bounds")
)

// ComputeMerkleRoot computes the root of a binary Merkle tree using Keccak256.
// If leaves is empty, it returns common.Hash{} (all zeros).
// If leaves has 1 element, the root is that element.
// For any level with an odd number of nodes (>1), the last node is duplicated.
func ComputeMerkleRoot(leaves []common.Hash) common.Hash {
	if len(leaves) == 0 {
		return common.Hash{}
	}
	if len(leaves) == 1 {
		return leaves[0]
	}

	current := make([]common.Hash, len(leaves))
	copy(current, leaves)

	for len(current) > 1 {
		if len(current)%2 != 0 {
			current = append(current, current[len(current)-1])
		}
		next := make([]common.Hash, len(current)/2)
		for i := 0; i < len(current); i += 2 {
			combined := append(current[i].Bytes(), current[i+1].Bytes()...)
			next[i/2] = crypto.Keccak256Hash(combined)
		}
		current = next
	}

	return current[0]
}

// GenerateMerkleProof generates the inclusion proof (audit path) for a leaf at the given index.
// Returns (root, proof, error).
func GenerateMerkleProof(leaves []common.Hash, index int) (common.Hash, []common.Hash, error) {
	if len(leaves) == 0 {
		return common.Hash{}, nil, ErrEmptyLeaves
	}
	if index < 0 || index >= len(leaves) {
		return common.Hash{}, nil, ErrIndexOverflow
	}
	if len(leaves) == 1 {
		return leaves[0], []common.Hash{}, nil
	}

	current := make([]common.Hash, len(leaves))
	copy(current, leaves)
	var proof []common.Hash
	idx := index

	for len(current) > 1 {
		if len(current)%2 != 0 {
			current = append(current, current[len(current)-1])
		}

		var sibling common.Hash
		if idx%2 == 0 {
			sibling = current[idx+1]
		} else {
			sibling = current[idx-1]
		}
		proof = append(proof, sibling)

		next := make([]common.Hash, len(current)/2)
		for i := 0; i < len(current); i += 2 {
			combined := append(current[i].Bytes(), current[i+1].Bytes()...)
			next[i/2] = crypto.Keccak256Hash(combined)
		}
		current = next
		idx = idx / 2
	}

	return current[0], proof, nil
}

// VerifyMerkleProof verifies whether leaf is part of the Merkle tree with the given root at index.
func VerifyMerkleProof(root common.Hash, leaf common.Hash, index int, proof []common.Hash) bool {
	if len(proof) == 0 {
		return root == leaf && index == 0
	}

	curr := leaf
	idx := index
	for _, sibling := range proof {
		var combined []byte
		if idx%2 == 0 {
			combined = append(curr.Bytes(), sibling.Bytes()...)
		} else {
			combined = append(sibling.Bytes(), curr.Bytes()...)
		}
		curr = crypto.Keccak256Hash(combined)
		idx = idx / 2
	}

	return curr == root
}

// TxsRoot calculates the binary Merkle root over keccak256(tx_bytes) in order.
func TxsRoot(txBytesList [][]byte) common.Hash {
	leaves := make([]common.Hash, len(txBytesList))
	for i, b := range txBytesList {
		leaves[i] = crypto.Keccak256Hash(b)
	}
	return ComputeMerkleRoot(leaves)
}

// ReceiptsRoot calculates the binary Merkle root over keccak256(receipt_bytes) in order.
func ReceiptsRoot(receiptBytesList [][]byte) common.Hash {
	leaves := make([]common.Hash, len(receiptBytesList))
	for i, b := range receiptBytesList {
		leaves[i] = crypto.Keccak256Hash(b)
	}
	return ComputeMerkleRoot(leaves)
}
