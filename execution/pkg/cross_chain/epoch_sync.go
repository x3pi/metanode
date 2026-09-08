package cross_chain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
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

// MessageFailureAttestDomainTag domain-separates the payload a destination chain committee signs
// to attest that a specific cross-chain message failed execution on the destination chain (P2.4).
var MessageFailureAttestDomainTag = []byte("MESSAGE_FAILURE_ATTEST_V1:")

// ComputeMessageFailureAttestMessage computes the domain-separated message payload a destination chain
// committee signs to attest execution failure for messageID on destChainID.
func ComputeMessageFailureAttestMessage(messageID common.Hash, destChainID uint64) []byte {
	var buf []byte
	buf = append(buf, MessageFailureAttestDomainTag...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], destChainID)
	buf = append(buf, idBuf[:]...)
	buf = append(buf, messageID.Bytes()...)
	return buf
}

// MessageSuccessAttestDomainTag domain-separates the payload a destination chain committee signs
// to attest that a specific cross-chain message SUCCEEDED (settled as MessageStatusSuccess) on the
// destination chain -- the mirror image of MessageFailureAttestDomainTag.
//
// SECURITY FIX (2026-09-05, "Cross-Chain Ledger Inflation via Missing Reserve Refund" finding,
// note/cross_chain/security_audit_findings.md): CreditReserveAllocation used to credit a
// destination chain's Reserve allocation on nothing more than a valid Merkle proof that a message
// was part of an already-attested commit -- that only proves the message was QUEUED for delivery,
// never that it actually succeeded. Anyone able to submit a transaction (not even destChainID's
// own validators) could call creditReserveAllocation for a message that genuinely failed (or was
// never even attempted), inflating that chain's allocation with no real value ever having landed
// there -- exactly the "print real tokens elsewhere" risk the finding describes, and strictly
// worse than the finding's own framing (this never required a malicious destination chain at all,
// just an uncooperative or malicious relayer). CreditReserveAllocation now requires a real
// QuorumCert over this digest, cryptographically signed by destChainID's OWN registered committee
// -- the same trust boundary MessageFailureAttestDomainTag/RefundReserveAllocation already rely on
// for the mirror-image "message failed" case.
var MessageSuccessAttestDomainTag = []byte("MESSAGE_SUCCESS_ATTEST_V1:")

// ComputeMessageSuccessAttestMessage computes the domain-separated message payload a destination
// chain committee signs to attest that messageID succeeded on destChainID.
func ComputeMessageSuccessAttestMessage(messageID common.Hash, destChainID uint64) []byte {
	var buf []byte
	buf = append(buf, MessageSuccessAttestDomainTag...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], destChainID)
	buf = append(buf, idBuf[:]...)
	buf = append(buf, messageID.Bytes()...)
	return buf
}

// The 5 domain tags and digest functions below (2026-09-04) replace GovernanceEngine's whole
// propose/vote/72h-timelock/execute machinery, removed the same day per explicit user request
// ("bỏ hoàn toàn vote... vì không có ai thao túng vote cả" -- if there is no vote mechanism, there
// is nothing to Sybil-manipulate). Vote from Governance.ActiveChains (grown for free by every
// RegisterChainViaStake call, see note/eurozone_unified_native_coin_plan.md mục 2.6) was itself the
// Sybil-exploitable primitive; replacing it with a real cryptographic QuorumCert -- from either the
// affected party's OWN committee (self-authorization) or a small, config-set, non-Sybil-able
// RecoveryCommittee (for actions a party genuinely cannot self-authorize) -- removes the
// vote-buying attack surface entirely rather than trying to patch it. Each of these mirrors
// ComputeCommitRootAttestMessage/ComputeGovernanceVoteMessage's own domain-separation pattern —
// a distinct tag per action so a signature over one can never be replayed as another.

// TransferAllocationDomainTag domain-separates GatewayEngine.TransferAllocationWithCert's payload:
// the SOURCE chain's own committee self-authorizes moving its own allocation elsewhere.
var TransferAllocationDomainTag = []byte("TRANSFER_ALLOCATION_V1:")

// ComputeTransferAllocationMessage computes the digest fromChainID's own committee signs to
// authorize moving `amount` of its own allocation to toChainID. nonce (2026-09-04, found in
// review: without it a captured valid cert could be replayed indefinitely to drain fromChainID's
// entire allocation -- see GatewayEngine.TransferAllocationNonce's own doc comment) must equal
// fromChainID's current TransferAllocationNonce for TransferAllocationWithCert to accept it, and
// is bumped by exactly 1 on every successful transfer, so the exact same signed digest can never
// verify against the live nonce a second time.
func ComputeTransferAllocationMessage(fromChainID, toChainID uint64, amount *big.Int, nonce uint64) []byte {
	var buf []byte
	buf = append(buf, TransferAllocationDomainTag...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], fromChainID)
	buf = append(buf, idBuf[:]...)
	binary.BigEndian.PutUint64(idBuf[:], toChainID)
	buf = append(buf, idBuf[:]...)
	buf = append(buf, padTo32(amount)...)
	binary.BigEndian.PutUint64(idBuf[:], nonce)
	buf = append(buf, idBuf[:]...)
	return buf
}

// AllocateSupplyDomainTag domain-separates GatewayEngine.AllocateSupplyWithCert's payload:
// Reserve's own committee self-authorizes the one-time genesis mint to itself.
var AllocateSupplyDomainTag = []byte("ALLOCATE_SUPPLY_V1:")

// ComputeAllocateSupplyMessage computes the digest Reserve's own committee signs to authorize the
// one-time genesis mint of `amount` to itself (chainID, always == g.ReserveChainID by the time
// this is checked, included here anyway so the signed payload is fully self-describing).
func ComputeAllocateSupplyMessage(chainID uint64, amount *big.Int) []byte {
	var buf []byte
	buf = append(buf, AllocateSupplyDomainTag...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], chainID)
	buf = append(buf, idBuf[:]...)
	buf = append(buf, padTo32(amount)...)
	return buf
}

// DeclareChainDeadDomainTag domain-separates GatewayEngine.DeclareChainDeadWithCert's payload:
// this is NOT self-authorizable (the whole point is the target chain is unresponsive), so it is
// signed by the config-set RecoveryCommittee instead.
var DeclareChainDeadDomainTag = []byte("DECLARE_CHAIN_DEAD_V1:")

// ComputeDeclareChainDeadMessage computes the digest the RecoveryCommittee signs to declare
// chainID dead (unlocking ClaimDeadChainBalance for its stranded account holders).
func ComputeDeclareChainDeadMessage(chainID uint64) []byte {
	var buf []byte
	buf = append(buf, DeclareChainDeadDomainTag...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], chainID)
	buf = append(buf, idBuf[:]...)
	return buf
}

// UnregisterChainDomainTag domain-separates GatewayEngine.UnregisterChainWithCert's payload —
// same non-self-authorizable rationale as DeclareChainDead, signed by RecoveryCommittee.
var UnregisterChainDomainTag = []byte("UNREGISTER_CHAIN_V1:")

// ComputeUnregisterChainMessage computes the digest the RecoveryCommittee signs to remove chainID
// from ChainRegistry entirely.
func ComputeUnregisterChainMessage(chainID uint64) []byte {
	var buf []byte
	buf = append(buf, UnregisterChainDomainTag...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], chainID)
	buf = append(buf, idBuf[:]...)
	return buf
}

// RecoveryUpdateCommitteeDomainTag domain-separates GatewayEngine.UpdateCommitteeWithRecoveryCert's
// payload — distinct from CommitteeUpdateDomainTag (ApplyCommitteeUpdate's OWN-committee-signs-its-
// successor path, epoch_sync.go above) on purpose: that path requires the chain's CURRENT/OLD
// committee to still be reachable to sign, which is exactly what is impossible in the recovery
// scenario this path exists for (old committee's keys lost/unreachable) — signed by
// RecoveryCommittee instead, and deliberately does NOT require sequential epoch progression the
// way ApplyCommitteeUpdate does, since a stuck chain's epoch counter may be arbitrarily far behind.
var RecoveryUpdateCommitteeDomainTag = []byte("RECOVERY_UPDATE_COMMITTEE_V1:")

// ComputeRecoveryUpdateCommitteeMessage computes the digest the RecoveryCommittee signs to install
// a brand new committee for chainID. newCommittee is sorted by PubkeyBLS first (same rationale as
// ComputeCommitteeUpdateDigest) so the digest is independent of slice order.
func ComputeRecoveryUpdateCommitteeMessage(chainID, newEpoch uint64, newCommittee []ValidatorEntry, quorumThreshold uint64, stateRoot, accountTreeRoot common.Hash) []byte {
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
	buf = append(buf, RecoveryUpdateCommitteeDomainTag...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], chainID)
	buf = append(buf, idBuf[:]...)
	binary.BigEndian.PutUint64(idBuf[:], newEpoch)
	buf = append(buf, idBuf[:]...)
	buf = append(buf, committeeHash.Bytes()...)
	var qtBuf [8]byte
	binary.BigEndian.PutUint64(qtBuf[:], quorumThreshold)
	buf = append(buf, qtBuf[:]...)
	buf = append(buf, stateRoot.Bytes()...)
	buf = append(buf, accountTreeRoot.Bytes()...)
	return buf
}

// RegisterAssetDomainTag domain-separates GatewayEngine.RegisterAssetOnRootAnchor's payload: the
// asset's own HomeChainID self-authorizes bridging it onto the shared registry.
var RegisterAssetDomainTag = []byte("REGISTER_ASSET_V1:")

// ComputeRegisterAssetMessage computes the digest an asset's HomeChainID own committee signs to
// authorize registering it.
func ComputeRegisterAssetMessage(assetID *big.Int, homeChainID uint64, canonicalContract common.Address) []byte {
	var buf []byte
	buf = append(buf, RegisterAssetDomainTag...)
	buf = append(buf, padTo32(assetID)...)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], homeChainID)
	buf = append(buf, idBuf[:]...)
	buf = append(buf, canonicalContract.Bytes()...)
	return buf
}

// padTo32 left-pads a big.Int's big-endian bytes to exactly 32 bytes (uint256 convention), same
// truncation-avoidance pattern HashAggregateValueLeaf already uses -- nil/negative treated as 0.
func padTo32(v *big.Int) []byte {
	out := make([]byte, 32)
	if v == nil {
		return out
	}
	raw := v.Bytes()
	if len(raw) <= 32 {
		copy(out[32-len(raw):], raw)
	}
	return out
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
	// Security fix: QuorumThreshold was applied with no bounds check — see
	// ValidateQuorumThreshold's doc comment (pop.go) for why a nonzero value below the 2/3 BFT
	// floor would let a minority of the new committee forge a "valid" QuorumCert afterward.
	if err := ValidateQuorumThreshold(update.QuorumThreshold); err != nil {
		return fmt.Errorf("chain %d: %w", update.SourceChainID, err)
	}

	reg.Epoch = update.NewEpoch
	reg.Committee = update.NewCommittee
	reg.QuorumThreshold = update.QuorumThreshold
	reg.StateRoot = update.StateRoot
	reg.AccountTreeRoot = update.AccountTreeRoot

	return nil
}
