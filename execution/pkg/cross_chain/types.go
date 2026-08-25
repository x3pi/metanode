package cross_chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

var (
	// ErrInvariantViolation occurs when sum(per_chain_allocation) != genesis_total_supply.
	ErrInvariantViolation = errors.New("sum of per_chain_allocation does not equal genesis_total_supply")
	// ErrInsufficientAllocation occurs when a chain attempts to transfer more allocation than its ceiling.
	ErrInsufficientAllocation = errors.New("source chain has insufficient allocation")
	// ErrSameChainTransfer occurs when from_chain == to_chain.
	ErrSameChainTransfer = errors.New("source and destination chain IDs must be distinct")
	// ErrNilAmount occurs when an operation receives a nil or negative amount.
	ErrNilAmount = errors.New("allocation amount cannot be nil or negative")
)

// ValidatorEntry represents a committee validator with BLS pubkey and Proof-of-Possession signature.
type ValidatorEntry struct {
	PubkeyBLS    []byte `json:"pubkey_bls"`
	Stake        uint64 `json:"stake"`
	PopSignature []byte `json:"pop_signature"`
}

// CommitteeAttestationShare is one validator's individual BLS signature over a pending
// CommitteeUpdate's payload hash (Milestone C of the wiring plan — see
// execution/pkg/cross_chain/epoch_sync.go's ComputeCommitteeUpdateDigest). Collected on Root
// Anchor via GatewayEngine.PendingCommitteeAttestations until enough stake is reached to
// aggregate into a real QuorumCert for committeeUpdate().
type CommitteeAttestationShare struct {
	SignerPubkeyBLS []byte `json:"signer_pubkey_bls"`
	Signature       []byte `json:"signature"`
}

// CommitAttestationShare is one validator's individual BLS signature over a pending
// commit root (Milestone F of the wiring plan — see
// execution/pkg/cross_chain/epoch_sync.go's ComputeCommitRootAttestMessage). Collected on Root
// Anchor via GatewayEngine.PendingCommitAttestations until enough stake is reached to
// aggregate into a real QuorumCert for attestCommit().
type CommitAttestationShare struct {
	SignerPubkeyBLS []byte `json:"signer_pubkey_bls"`
	Signature       []byte `json:"signature"`
}

// ChainRegistry holds registered chain metadata and committee state on Root Anchor (Section 2.1).
type ChainRegistry struct {
	ChainID          uint64           `json:"chain_id"`
	Committee        []ValidatorEntry `json:"committee"`
	Epoch            uint64           `json:"epoch"`
	QuorumThreshold  uint64           `json:"quorum_threshold"`
	GatewayContract  common.Address   `json:"gateway_contract"`
	StateRoot        common.Hash      `json:"state_root"`
	ArchivalEndpoint string           `json:"archival_endpoint"`
	RegisteredAt     uint64           `json:"registered_at"`
}

// GlobalSupplyLedger is the active issuer and custodial ceiling ledger on Root Anchor (Section 2.1 & 2.3).
// Invariant: sum(per_chain_allocation) == genesis_total_supply.
type GlobalSupplyLedger struct {
	GenesisTotalSupply *big.Int            `json:"genesis_total_supply"`
	PerChainAllocation map[uint64]*big.Int `json:"per_chain_allocation"`
}

// NewGlobalSupplyLedger initializes a supply ledger and validates the initial invariant.
func NewGlobalSupplyLedger(genesisTotalSupply *big.Int, initialAllocations map[uint64]*big.Int) (*GlobalSupplyLedger, error) {
	if genesisTotalSupply == nil || genesisTotalSupply.Sign() < 0 {
		return nil, ErrNilAmount
	}

	allocCopy := make(map[uint64]*big.Int, len(initialAllocations))
	for k, v := range initialAllocations {
		if v == nil || v.Sign() < 0 {
			return nil, ErrNilAmount
		}
		allocCopy[k] = new(big.Int).Set(v)
	}

	ledger := &GlobalSupplyLedger{
		GenesisTotalSupply: new(big.Int).Set(genesisTotalSupply),
		PerChainAllocation: allocCopy,
	}

	if !ledger.VerifyInvariant() {
		return nil, fmt.Errorf("%w: expected %s, actual %s", ErrInvariantViolation, genesisTotalSupply.String(), ledger.SumAllocations().String())
	}

	return ledger, nil
}

// SumAllocations computes the sum of all per-chain allocations.
func (g *GlobalSupplyLedger) SumAllocations() *big.Int {
	sum := new(big.Int)
	for _, v := range g.PerChainAllocation {
		if v != nil {
			sum.Add(sum, v)
		}
	}
	return sum
}

// VerifyInvariant checks if sum(per_chain_allocation) == genesis_total_supply.
func (g *GlobalSupplyLedger) VerifyInvariant() bool {
	if g.GenesisTotalSupply == nil {
		return false
	}
	sum := g.SumAllocations()
	return sum.Cmp(g.GenesisTotalSupply) == 0
}

// GetAllocation returns the current active ceiling for the given chain ID.
func (g *GlobalSupplyLedger) GetAllocation(chainID uint64) *big.Int {
	if alloc, exists := g.PerChainAllocation[chainID]; exists && alloc != nil {
		return new(big.Int).Set(alloc)
	}
	return new(big.Int)
}

// SetInitialAllocation allocates funds to a new chain from an unallocated reserve balance.
func (g *GlobalSupplyLedger) SetInitialAllocation(reserveChainID, newChainID uint64, amount *big.Int) error {
	return g.TransferAllocation(reserveChainID, newChainID, amount)
}

// TransferAllocation moves allocation between chains, enforcing the custodial ceiling.
func (g *GlobalSupplyLedger) TransferAllocation(fromChain, toChain uint64, amount *big.Int) error {
	if fromChain == toChain {
		return ErrSameChainTransfer
	}
	if amount == nil || amount.Sign() <= 0 {
		return ErrNilAmount
	}

	fromAlloc := g.GetAllocation(fromChain)
	if fromAlloc.Cmp(amount) < 0 {
		return fmt.Errorf("%w: chain %d available %s, requested %s", ErrInsufficientAllocation, fromChain, fromAlloc.String(), amount.String())
	}

	toAlloc := g.GetAllocation(toChain)

	newFrom := new(big.Int).Sub(fromAlloc, amount)
	newTo := new(big.Int).Add(toAlloc, amount)

	g.PerChainAllocation[fromChain] = newFrom
	g.PerChainAllocation[toChain] = newTo

	if !g.VerifyInvariant() {
		panic("CRITICAL: Invariant violation during TransferAllocation")
	}

	return nil
}

// CrossChainMessage represents an outbound/inbound envelope across chains (Section 11.2).
type CrossChainMessage struct {
	MessageID     common.Hash    `json:"message_id"`
	SourceChainID uint64         `json:"source_chain_id"`
	DestChainID   uint64         `json:"dest_chain_id"`
	Sequence      uint64         `json:"sequence"`
	HopCount      uint8          `json:"hop_count"`
	Sender        common.Address `json:"sender"`
	Target        common.Address `json:"target"`
	AssetID       *big.Int       `json:"asset_id"`
	Value         *big.Int       `json:"value"`
	Payload       []byte         `json:"payload"`
	Tip           *big.Int       `json:"tip"`
	Ordered       bool           `json:"ordered"`
}

// Custom JSON marshaler for CrossChainMessage to handle big.Int fields cleanly
type crossChainMessageJSON struct {
	MessageID     common.Hash    `json:"message_id"`
	SourceChainID uint64         `json:"source_chain_id"`
	DestChainID   uint64         `json:"dest_chain_id"`
	Sequence      uint64         `json:"sequence"`
	HopCount      uint8          `json:"hop_count"`
	Sender        common.Address `json:"sender"`
	Target        common.Address `json:"target"`
	AssetID       *hexutil.Big   `json:"asset_id"`
	Value         *hexutil.Big   `json:"value"`
	Payload       hexutil.Bytes  `json:"payload"`
	Tip           *hexutil.Big   `json:"tip"`
	Ordered       bool           `json:"ordered"`
}

func (m CrossChainMessage) MarshalJSON() ([]byte, error) {
	return json.Marshal(crossChainMessageJSON{
		MessageID:     m.MessageID,
		SourceChainID: m.SourceChainID,
		DestChainID:   m.DestChainID,
		Sequence:      m.Sequence,
		HopCount:      m.HopCount,
		Sender:        m.Sender,
		Target:        m.Target,
		AssetID:       (*hexutil.Big)(m.AssetID),
		Value:         (*hexutil.Big)(m.Value),
		Payload:       m.Payload,
		Tip:           (*hexutil.Big)(m.Tip),
		Ordered:       m.Ordered,
	})
}

func (m *CrossChainMessage) UnmarshalJSON(data []byte) error {
	var aux crossChainMessageJSON
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.MessageID = aux.MessageID
	m.SourceChainID = aux.SourceChainID
	m.DestChainID = aux.DestChainID
	m.Sequence = aux.Sequence
	m.HopCount = aux.HopCount
	m.Sender = aux.Sender
	m.Target = aux.Target
	m.AssetID = (*big.Int)(aux.AssetID)
	m.Value = (*big.Int)(aux.Value)
	m.Payload = aux.Payload
	m.Tip = (*big.Int)(aux.Tip)
	m.Ordered = aux.Ordered
	return nil
}

// QuorumCert is a BFT consensus quorum certificate from the source chain (Section 11.2).
type QuorumCert struct {
	Epoch              uint64        `json:"epoch"`
	AggregateSignature hexutil.Bytes `json:"aggregate_signature"`
	SignerBitmap       hexutil.Bytes `json:"signer_bitmap"`
}

// MerkleProof represents an inclusion proof into a commit root (Section 11.2).
type MerkleProof struct {
	LeafIndex uint64        `json:"leaf_index"`
	Siblings  []common.Hash `json:"siblings"`
}

// AssetEntry maps an asset across private chains and Root Anchor (Section 11.6 & 2.5).
type AssetEntry struct {
	AssetID           *big.Int                  `json:"asset_id"`
	HomeChainID       uint64                    `json:"home_chain_id"`
	CanonicalContract common.Address            `json:"canonical_contract"`
	WrappedContracts  map[uint64]common.Address `json:"wrapped_contracts"`
	Active            bool                      `json:"active"`
}

// MessageStatus defines message settlement states (Section 11.6).
type MessageStatus uint8

const (
	MessageStatusPending  MessageStatus = 0
	MessageStatusSuccess  MessageStatus = 1
	MessageStatusFailed   MessageStatus = 2
	MessageStatusRefunded MessageStatus = 3
)

// Channel tracks message progress between source and destination chains (Section 11.6).
type Channel struct {
	SourceChainID         uint64                        `json:"source_chain_id"`
	DestChainID           uint64                        `json:"dest_chain_id"`
	Ordered               bool                          `json:"ordered"`
	NextSequence          uint64                        `json:"next_sequence"`
	LastProcessedSequence uint64                        `json:"last_processed_sequence"`
	StatusByMessageID     map[common.Hash]MessageStatus `json:"status_by_message_id"`
}

// AttestedCommit records certified commits during Phase 1 of Attest-then-Claim (Section 11.6 & 13.3).
type AttestedCommit struct {
	SourceChainID uint64      `json:"source_chain_id"`
	CommitRoot    common.Hash `json:"commit_root"`
	AssetID       *big.Int    `json:"asset_id"`
	Epoch         uint64      `json:"epoch"`
	FundedAmount  *big.Int    `json:"funded_amount"`
	ClaimedAmount *big.Int    `json:"claimed_amount"`
}

// GovernanceProposalKind represents governance action types on Root Anchor (Section 11.6 & 1.3 #3).
type GovernanceProposalKind uint8

const (
	ProposalRegisterChain    GovernanceProposalKind = 0
	ProposalUnregisterChain  GovernanceProposalKind = 1
	ProposalRegisterAsset    GovernanceProposalKind = 2
	ProposalUpdateCommittee  GovernanceProposalKind = 3
	ProposalDeclareChainDead GovernanceProposalKind = 4
)

// GovernanceProposal tracks on-chain voting across active chains (Section 11.6 & 1.3 #3).
type GovernanceProposal struct {
	ProposalID  common.Hash            `json:"proposal_id"`
	Kind        GovernanceProposalKind `json:"kind"`
	Payload     []byte                 `json:"payload"`
	VotesFor    uint64                 `json:"votes_for"`
	VotedChains map[uint64]bool        `json:"voted_chains"`
	ProposedAt  uint64                 `json:"proposed_at"`
	EffectiveAt uint64                 `json:"effective_at"`
	Executed    bool                   `json:"executed"`
}

// AccountLeaf represents account state snapshot for Chain-Death Recovery (Section 11.6 & 5.2.2).
type AccountLeaf struct {
	Account common.Address `json:"account"`
	Balance *big.Int       `json:"balance"`
}

// AggregateValueLeaf represents the real total value moved for a given asset in a commit root (Milestone E, Section 11.2).
type AggregateValueLeaf struct {
	SourceChainID   uint64
	CommitRoot      common.Hash
	AssetID         *big.Int
	AggregateAmount *big.Int
}
