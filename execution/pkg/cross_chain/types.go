package cross_chain

import (
	"bytes"
	"encoding/binary"
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
	// ErrGenesisDigestAlreadySet occurs when SetGenesisDigest is called for a chain that already
	// has a non-zero GenesisDigest recorded -- settable exactly once, see ChainRegistry.GenesisDigest.
	ErrGenesisDigestAlreadySet = errors.New("genesis digest already published for this chain")
	// ErrNotGenesisWallet occurs when SetGenesisDigest's caller does not match the chain's
	// recorded GenesisWallet (the address that actually paid the registration stake).
	ErrNotGenesisWallet = errors.New("caller is not this chain's recorded genesis wallet")
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
	AccountTreeRoot  common.Hash      `json:"account_tree_root"`
	ArchivalEndpoint string           `json:"archival_endpoint"`
	RegisteredAt     uint64           `json:"registered_at"`

	// GenesisWallet and GenesisDigest (2026-09-04) support the stake-funded onboarding flow's
	// deterministic-genesis design: a chain registered via RegisterChainViaStake bakes its
	// initial allocation directly into its OWN genesis.json (no live post-registration bridge
	// transfer needed) rather than starting at 0 and waiting for a separate ClaimMessage. See
	// RegisterChainViaStake and SetGenesisDigest's own doc comments for the full mechanism.
	//
	// GenesisWallet is the address that must hold the chain's initial native-coin supply in its
	// own genesis.json alloc -- forced to equal the caller that actually paid the stake
	// (gateway_handler.go's "registerChainViaStake" case passes tx.FromAddress(), overwriting
	// whatever the submitted payload claimed), never operator-suppliable, so it can't be spoofed
	// to point at an unrelated wallet.
	GenesisWallet common.Address `json:"genesis_wallet,omitempty"`

	// GenesisDigest is keccak256 of the chain's canonical genesis.json bytes (same primitive as
	// pkg/cross_chain/ceremony.Digest -- kept identical on purpose so the two verification paths
	// share one well-tested definition of "digest"), published via SetGenesisDigest AFTER the
	// registrant has actually built genesis.json for every validator (chicken-and-egg: the
	// digest can only be computed once the file exists, so it can't be part of the original
	// registration payload). Zero means "not yet published" -- any observer bootstrapping a node
	// for this chain should treat an unpublished digest as "cannot verify yet", not "verified
	// empty". Settable exactly once (ErrGenesisDigestAlreadySet on a second attempt) and only by
	// GenesisWallet, so a would-be attacker can't front-run the real registrant with a wrong
	// digest that later locks out the honest genesis file.
	GenesisDigest common.Hash `json:"genesis_digest,omitempty"`
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

// VerifyInvariant checks if sum(per_chain_allocation) <= genesis_total_supply.
func (g *GlobalSupplyLedger) VerifyInvariant() bool {
	if g.GenesisTotalSupply == nil {
		return false
	}
	sum := g.SumAllocations()
	return sum.Cmp(g.GenesisTotalSupply) <= 0
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

// GrantAllocation increases chainID's custodial ceiling (per_chain_allocation) and the tracked
// genesis total supply together, keeping sum(per_chain_allocation) <= genesis_total_supply intact.
// Unlike TransferAllocation (redistributes EXISTING allocation between two already-funded chains),
// this is the only way new headroom enters the ledger at all: neither the retired
// BootstrapFoundingChains/ProposalRegisterChain paths nor RegisterChainViaStake's own registration
// step ever touch SupplyLedger this way (confirmed by direct code reading), and production always
// constructs the ledger with genesis_total_supply=0 and an empty allocation map
// (gateway_handler.go's loadGatewayEngine) — so without this method, EVERY attestCommit() ceiling
// check (Scenario 10.7) rejects with "available 0" forever, for every chain, native coin or custom
// asset alike. Callers are cert-gated (2026-09-04): AllocateSupplyWithCert requires the Reserve
// chain's own committee to self-sign a one-time genesis mint, and RegisterAssetOnRootAnchor
// requires the new asset's HomeChainID committee to self-sign its registration — so a captured
// single OTHER chain can never self-grant an allocation it doesn't own.
func (g *GlobalSupplyLedger) GrantAllocation(chainID uint64, amount *big.Int) error {
	if amount == nil || amount.Sign() <= 0 {
		return ErrNilAmount
	}
	current := g.GetAllocation(chainID)
	g.PerChainAllocation[chainID] = new(big.Int).Add(current, amount)
	g.GenesisTotalSupply = new(big.Int).Add(g.GenesisTotalSupply, amount)
	if !g.VerifyInvariant() {
		panic("CRITICAL: Invariant violation during GrantAllocation")
	}
	return nil
}

// TransferAllocation moves allocation between chains, enforcing the custodial ceiling.
func (g *GlobalSupplyLedger) TransferAllocation(fromChain, toChain uint64, amount *big.Int) error {
	if fromChain == toChain {
		return ErrSameChainTransfer
	}
	if amount == nil || amount.Sign() <= 0 {
		return ErrNilAmount
	}

	sumBefore := g.SumAllocations()

	fromAlloc := g.GetAllocation(fromChain)
	if fromAlloc.Cmp(amount) < 0 {
		return fmt.Errorf("%w: chain %d available %s, requested %s", ErrInsufficientAllocation, fromChain, fromAlloc.String(), amount.String())
	}

	toAlloc := g.GetAllocation(toChain)

	newFrom := new(big.Int).Sub(fromAlloc, amount)
	newTo := new(big.Int).Add(toAlloc, amount)

	g.PerChainAllocation[fromChain] = newFrom
	g.PerChainAllocation[toChain] = newTo

	sumAfter := g.SumAllocations()
	if sumBefore.Cmp(sumAfter) != 0 || !g.VerifyInvariant() {
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
	// GasFee is a native-coin amount locked at outbound() time to pay for CONTRACT_CALL
	// execution at the destination chain (mục 2.6.5) -- separate from Tip (relayer incentive,
	// paid regardless of whether the message calls a contract). isContractCall()==true with
	// GasFee==0 fails closed rather than executing for free (mục 5.3 risk #9). See
	// gateway_handler.go's executeContractCallForGateway call sites for the settlement logic.
	GasFee  *big.Int `json:"gas_fee"`
	Ordered bool     `json:"ordered"`
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
	GasFee        *hexutil.Big   `json:"gas_fee"`
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
		GasFee:        (*hexutil.Big)(m.GasFee),
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
	m.GasFee = (*big.Int)(aux.GasFee)
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

// UpdateCommitteePayload is the JSON payload for UpdateCommitteeWithRecoveryCert (RecoveryCommittee-
// authorized committee replacement -- see gateway.go). Was formerly also used by the deleted
// GovernanceEngine's ProposalUpdateCommittee proposal kind; that whole propose/vote/execute
// machinery (GovernanceProposalKind, GovernanceProposal, AllocationGrantPayload,
// AllocationTransferPayload and the numbered Proposal* kinds) was removed 2026-09-04 in favor of
// per-action cert-based self-authorization / RecoveryCommittee-authorization (see gateway.go's
// AllocateSupplyWithCert/TransferAllocationWithCert/DeclareChainDeadWithCert/
// UnregisterChainWithCert/UpdateCommitteeWithRecoveryCert). This struct is the only piece of that
// old payload family still live.
type UpdateCommitteePayload struct {
	ChainID         uint64           `json:"chain_id"`
	SourceChainID   uint64           `json:"source_chain_id,omitempty"`
	NewEpoch        uint64           `json:"new_epoch"`
	NewCommittee    []ValidatorEntry `json:"new_committee"`
	QuorumThreshold uint64           `json:"quorum_threshold,omitempty"`
	StateRoot       common.Hash      `json:"state_root,omitempty"`
	AccountTreeRoot common.Hash      `json:"account_tree_root,omitempty"`
}

// AccountLeaf represents account state snapshot for Chain-Death Recovery (Section 11.6 & 5.2.2).
type AccountLeaf struct {
	Account common.Address `json:"account"`
	Balance *big.Int       `json:"balance"`
}

// AggregateValueLeaf represents the real total value moved for a given asset in a commit
// (Section 11.2/2.3.1). It is a leaf of the SAME Merkle tree that already contains the commit's
// per-message leaves (BuildCommitTree in relayer.go) — not a separate tree, and it carries no
// sourceChainId/commitRoot fields of its own (matching the design doc's minimal
// AggregateValueLeaf{assetId, totalValue} exactly): scoping to one specific commit comes entirely
// from verifying its Merkle proof against that commit's own commitRoot, so the identical leaf is
// valid both for the originating chain's own attestCommit() and for Reserve's re-attestation of
// the same commit on the second hop (AttestReserveIssuedCommit) — both verify against the same
// commitRoot.
type AggregateValueLeaf struct {
	AssetID         *big.Int
	AggregateAmount *big.Int
}

// relayMarkerPrefix tags a CrossChainMessage.Payload as a "relay this value/call onward to
// another chain instead of settling it here" instruction, for the 2-hop A -> Reserve -> B value
// & CONTRACT_CALL routing added 2026-08-28/2026-08-29 (see
// note/cross_chain_stake_and_value_flow.md). Distinct, unlikely-to-collide tag rather than a
// single marker byte -- Payload is otherwise always empty for a plain native-value transfer with
// no payload, so this is safe to introduce without touching CrossChainMessage's wire format,
// Merkle leaf hashing (ComputeMessageLeafHash hashes Payload as opaque bytes either way), or any
// existing message that predates this feature (their Payload is empty and DecodeRelayPayload
// correctly returns ok=false for anything that isn't this exact tag).
var relayMarkerPrefix = []byte("MTNRELAY1:")

// relayMarkerHeaderLen is the fixed-size portion of an encoded relay payload: the tag plus an
// 8-byte big-endian finalDestChainID. Anything after that is the caller-supplied inner payload,
// forwarded verbatim to the FINAL destination chain's own claimMessage handling (CONTRACT_CALL if
// Target has code there, otherwise ignored exactly like any other pure-value message's payload).
var relayMarkerHeaderLen = len(relayMarkerPrefix) + 8

// EncodeRelayPayload builds a CrossChainMessage.Payload that instructs the RECEIVING chain (once
// it verifies and claims this message via ClaimMessage) to relay the value AND innerPayload
// onward to finalDestChainID instead of settling them here. Intended for the FIRST leg of an
// A -> Reserve -> B transfer: the message's own DestChainID is Reserve (the immediate hop,
// required so attestCommit's ceiling check -- C8 -- can actually pass), while this payload
// carries the TRUE final destination B plus whatever payload B's own claimMessage should act on
// (nil/empty for a plain value transfer; real ABI-encoded calldata for a cross-chain
// CONTRACT_CALL -- see settleGasCappedContractCall's own doc comment for how that executes once
// it reaches the real final destination).
func EncodeRelayPayload(finalDestChainID uint64, innerPayload []byte) []byte {
	buf := make([]byte, relayMarkerHeaderLen+len(innerPayload))
	copy(buf, relayMarkerPrefix)
	binary.BigEndian.PutUint64(buf[len(relayMarkerPrefix):relayMarkerHeaderLen], finalDestChainID)
	copy(buf[relayMarkerHeaderLen:], innerPayload)
	return buf
}

// DecodeRelayPayload returns (finalDestChainID, innerPayload, true) if payload was built by
// EncodeRelayPayload, or (0, nil, false) for anything else (including the empty payload every
// pre-existing message uses). innerPayload is nil (not just len-0) when EncodeRelayPayload was
// called with an empty/nil innerPayload, so callers can use it directly as a CrossChainMessage's
// own Payload field without an extra empty-vs-nil normalization step.
func DecodeRelayPayload(payload []byte) (finalDestChainID uint64, innerPayload []byte, ok bool) {
	if len(payload) < relayMarkerHeaderLen {
		return 0, nil, false
	}
	if !bytes.Equal(payload[:len(relayMarkerPrefix)], relayMarkerPrefix) {
		return 0, nil, false
	}
	finalDestChainID = binary.BigEndian.Uint64(payload[len(relayMarkerPrefix):relayMarkerHeaderLen])
	if len(payload) > relayMarkerHeaderLen {
		innerPayload = payload[relayMarkerHeaderLen:]
	}
	return finalDestChainID, innerPayload, true
}
