package cross_chain

import (
	"errors"
	"fmt"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"
)

var (
	ErrUnknownChain        = errors.New("chain is not registered in ChainRegistry")
	ErrNonSequentialEpoch  = errors.New("non-sequential epoch transition")
	ErrInvalidQuorumCert   = errors.New("quorum certificate for current epoch is invalid")
	ErrInvalidNewCommittee = errors.New("new committee validation failed")
	ErrAccountProofFailed  = errors.New("account Merkle proof verification failed")
	ErrEmptyAccounts       = errors.New("empty account list provided")
)

// CommitteeUpdate represents the epoch transition payload sent from a private chain to Root Anchor (Section 14 P3.1).
type CommitteeUpdate struct {
	SourceChainID   uint64           `json:"source_chain_id"`
	NewEpoch        uint64           `json:"new_epoch"`
	NewCommittee    []ValidatorEntry `json:"new_committee"`
	QuorumThreshold uint64           `json:"quorum_threshold"`
	StateRoot       common.Hash      `json:"state_root"`
	Cert            QuorumCert       `json:"cert"`
}

// HashAccountLeaf computes the 32-byte Keccak-256 leaf hash for an AccountLeaf (Section 11.6 & 5.2.2).
func HashAccountLeaf(leaf AccountLeaf) common.Hash {
	var data []byte
	data = append(data, leaf.Account.Bytes()...)
	balBytes := leaf.Balance.Bytes()
	// Pad balance to 32 bytes
	padded := make([]byte, 32)
	copy(padded[32-len(balBytes):], balBytes)
	data = append(data, padded...)

	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(data)
	var out common.Hash
	hasher.Sum(out[:0])
	return out
}

func hashNodePair(left, right common.Hash) common.Hash {
	return hashPair(left, right)
}

// BuildAccountMerkleTree constructs a binary Merkle tree from account leaves and generates individual inclusion proofs (Section 14 P3.2).
func BuildAccountMerkleTree(accounts []AccountLeaf) (common.Hash, []MerkleProof, error) {
	if len(accounts) == 0 {
		return common.Hash{}, nil, ErrEmptyAccounts
	}

	n := len(accounts)
	leaves := make([]common.Hash, n)
	for i, acc := range accounts {
		leaves[i] = HashAccountLeaf(acc)
	}

	paddedLen := 1
	for paddedLen < n {
		paddedLen *= 2
	}
	last := leaves[len(leaves)-1]
	for len(leaves) < paddedLen {
		leaves = append(leaves, last)
	}

	numLayers := int(math.Log2(float64(paddedLen)))
	layers := [][]common.Hash{leaves}

	currentLayer := leaves
	for l := 0; l < numLayers; l++ {
		nextLayer := make([]common.Hash, 0, len(currentLayer)/2)
		for i := 0; i < len(currentLayer); i += 2 {
			nextLayer = append(nextLayer, hashNodePair(currentLayer[i], currentLayer[i+1]))
		}
		layers = append(layers, nextLayer)
		currentLayer = nextLayer
	}

	root := layers[len(layers)-1][0]

	proofs := make([]MerkleProof, n)
	for i := 0; i < n; i++ {
		siblings := make([]common.Hash, 0, numLayers)
		idx := i
		for l := 0; l < numLayers; l++ {
			sibIdx := idx ^ 1
			siblings = append(siblings, layers[l][sibIdx])
			idx /= 2
		}
		proofs[i] = MerkleProof{
			LeafIndex: uint64(i),
			Siblings:  siblings,
		}
	}

	return root, proofs, nil
}

// VerifyAccountMerkleProof checks whether an AccountLeaf belongs to a committed state_root.
func VerifyAccountMerkleProof(leaf AccountLeaf, proof MerkleProof, expectedRoot common.Hash) bool {
	current := HashAccountLeaf(leaf)
	for _, sibling := range proof.Siblings {
		current = hashNodePair(current, sibling)
	}
	return current == expectedRoot
}

// ApplyCommitteeUpdate updates the ChainRegistry on Root Anchor upon receiving a valid CommitteeUpdate (Section 14 P3.1).
func ApplyCommitteeUpdate(
	registry map[uint64]*ChainRegistry,
	update CommitteeUpdate,
	isOldCertValid bool,
) error {
	reg, exists := registry[update.SourceChainID]
	if !exists {
		return fmt.Errorf("%w: chain %d", ErrUnknownChain, update.SourceChainID)
	}

	expectedEpoch := reg.Epoch + 1
	if update.NewEpoch != expectedEpoch {
		return fmt.Errorf("%w: expected %d, got %d", ErrNonSequentialEpoch, expectedEpoch, update.NewEpoch)
	}

	if !isOldCertValid || update.Cert.Epoch != reg.Epoch {
		return fmt.Errorf("%w: for epoch %d", ErrInvalidQuorumCert, reg.Epoch)
	}

	if err := ValidateCommittee(update.NewCommittee); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidNewCommittee, err)
	}

	reg.Epoch = update.NewEpoch
	reg.Committee = update.NewCommittee
	reg.QuorumThreshold = update.QuorumThreshold
	reg.StateRoot = update.StateRoot

	return nil
}
