package cross_chain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"

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
	AccountTreeRoot common.Hash      `json:"account_tree_root"`
	Cert            QuorumCert       `json:"cert"`
}

// CommitteeUpdateDomainTag domain-separates the payload every validator signs when attesting to
// a CommitteeUpdate (Milestone C of the wiring plan), distinct from attestCommit's
// "COMMIT_ROOT_ATTEST_V1:" tag so a signature over one can never be replayed as the other.
var CommitteeUpdateDomainTag = []byte("COMMITTEE_UPDATE_V1:")

// CommitRootAttestDomainTag domain-separates the payload every validator signs when attesting to
// a commit root (Milestone F of the wiring plan), matching attestCommit's verification convention
// in gateway.go.
var CommitRootAttestDomainTag = []byte("COMMIT_ROOT_ATTEST_V1:")

// ComputeCommitRootAttestMessage computes the domain-separated message payload every validator of
// the committee signs to attest to a commit root.
func ComputeCommitRootAttestMessage(commitRoot common.Hash) []byte {
	var buf []byte
	buf = append(buf, CommitRootAttestDomainTag...)
	buf = append(buf, commitRoot.Bytes()...)
	return buf
}

// GovernanceVoteDomainTag domain-separates the payload a committee member signs to cast their
// chain's single governance vote (Milestone G security fix), distinct from every other signed
// payload in this package so a signature over one can never be replayed as another.
var GovernanceVoteDomainTag = []byte("GOVERNANCE_VOTE_V1:")

// ComputeGovernanceVoteMessage computes the domain-separated payload a member of voterChainID's
// CURRENT committee (per Root Anchor's own ChainRegistry) must sign to cast that chain's one vote
// for a proposal. This is what gateway_handler.go's "vote" case verifies before ever calling
// GovernanceEngine.Vote — without it, any unauthenticated caller could cast a vote "as" any
// registered chain merely by naming its ID, since GovernanceEngine.Vote itself trusts its caller.
func ComputeGovernanceVoteMessage(proposalID common.Hash, voterChainID uint64) []byte {
	var buf []byte
	buf = append(buf, GovernanceVoteDomainTag...)
	buf = append(buf, proposalID.Bytes()...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], voterChainID)
	buf = append(buf, idBuf[:]...)
	return buf
}

// BuildSignerBitmap constructs a deterministic bit vector for a committee indicating which
// members have signed, where bit i represents committee[i].
func BuildSignerBitmap(committee []ValidatorEntry, votingPubkeys [][]byte) []byte {
	if len(committee) == 0 || len(votingPubkeys) == 0 {
		return []byte{}
	}
	numBytes := (len(committee) + 7) / 8
	bitmap := make([]byte, numBytes)
	for _, pk := range votingPubkeys {
		for i, member := range committee {
			if bytes.Equal(member.PubkeyBLS, pk) {
				byteIdx := i / 8
				bitIdx := uint(i % 8)
				bitmap[byteIdx] |= (1 << bitIdx)
				break
			}
		}
	}
	return bitmap
}

// ComputeCommitteeUpdateDigest computes the domain-separated digest every validator of the OLD
// committee signs to attest to a new one. newCommittee is sorted by PubkeyBLS bytes before
// hashing so the digest is independent of slice order — every validator must derive the exact
// same digest from the exact same (sourceChainID, newEpoch, newCommittee, stateRoot, accountTreeRoot) tuple for
// their individual signatures to aggregate into a single valid QuorumCert (Section 11.2).
func ComputeCommitteeUpdateDigest(sourceChainID, newEpoch uint64, newCommittee []ValidatorEntry, stateRoot common.Hash, accountTreeRoot common.Hash) common.Hash {
	sorted := make([]ValidatorEntry, len(newCommittee))
	copy(sorted, newCommittee)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].PubkeyBLS, sorted[j].PubkeyBLS) < 0
	})

	var committeeBuf []byte
	for _, v := range sorted {
		committeeBuf = append(committeeBuf, v.PubkeyBLS...)
		var stakeBuf [8]byte
		binary.BigEndian.PutUint64(stakeBuf[:], v.Stake)
		committeeBuf = append(committeeBuf, stakeBuf[:]...)
		committeeBuf = append(committeeBuf, v.PopSignature...)
	}
	committeeHash := Keccak256(committeeBuf)

	var buf []byte
	buf = append(buf, CommitteeUpdateDomainTag...)
	var chainIDBuf, epochBuf [8]byte
	binary.BigEndian.PutUint64(chainIDBuf[:], sourceChainID)
	binary.BigEndian.PutUint64(epochBuf[:], newEpoch)
	buf = append(buf, chainIDBuf[:]...)
	buf = append(buf, epochBuf[:]...)
	buf = append(buf, committeeHash.Bytes()...)
	buf = append(buf, stateRoot.Bytes()...)
	buf = append(buf, accountTreeRoot.Bytes()...)

	return Keccak256(buf)
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

// HashAggregateValueLeaf computes the 32-byte Keccak-256 leaf hash for an AggregateValueLeaf,
// domain-separated with 0x02 (distinct from 0x00 message leaves and 0x01 internal nodes —
// gateway.go's ComputeMessageLeafHash / hashPair) so it can never collide with a real message
// leaf inside the same commit tree (Section 2.3.1/11.2). Deliberately excludes sourceChainId and
// commitRoot: this leaf is scoped to one specific commit purely by being verified with a Merkle
// proof against that commit's own commitRoot (see BuildCommitTree in relayer.go and
// attestCommitInternal in gateway.go), matching the design doc's minimal
// AggregateValueLeaf{assetId, totalValue} exactly.
func HashAggregateValueLeaf(leaf AggregateValueLeaf) common.Hash {
	var data []byte
	data = append(data, 0x02) // Domain separation: 0x02 for AggregateValueLeaf

	assetBytes := make([]byte, 32)
	if leaf.AssetID != nil {
		raw := leaf.AssetID.Bytes()
		if len(raw) <= 32 {
			copy(assetBytes[32-len(raw):], raw)
		}
	}
	data = append(data, assetBytes...)

	amountBytes := make([]byte, 32)
	if leaf.AggregateAmount != nil {
		raw := leaf.AggregateAmount.Bytes()
		if len(raw) <= 32 {
			copy(amountBytes[32-len(raw):], raw)
		}
	}
	data = append(data, amountBytes...)

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

// BuildAccountSnapshot constructs a deterministic binary Merkle tree over a slice of AccountLeaf entries
// (sorting them by account address bytes for canonical ordering) and returns the root hash and a map
// of address -> MerkleProof for easy client-side recovery.
func BuildAccountSnapshot(accounts []AccountLeaf) (common.Hash, map[common.Address]MerkleProof, error) {
	if len(accounts) == 0 {
		return common.Hash{}, nil, ErrEmptyAccounts
	}
	sorted := make([]AccountLeaf, len(accounts))
	copy(sorted, accounts)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].Account.Bytes(), sorted[j].Account.Bytes()) < 0
	})

	root, proofs, err := BuildAccountMerkleTree(sorted)
	if err != nil {
		return common.Hash{}, nil, err
	}

	proofMap := make(map[common.Address]MerkleProof, len(sorted))
	for i, acc := range sorted {
		proofMap[acc.Account] = proofs[i]
	}
	return root, proofMap, nil
}

// VerifyAccountMerkleProof checks whether an AccountLeaf belongs to a committed account_tree_root.
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
	reg.AccountTreeRoot = update.AccountTreeRoot

	return nil
}
