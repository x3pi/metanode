package cross_chain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"golang.org/x/crypto/sha3"
)

const (
	// MaxHopCount is the maximum allowable routing hops for cross-chain messages (Section 2.6.2 & 11.3).
	MaxHopCount uint8 = 6
)

var (
	ErrHopCountExceeded        = errors.New("hop count exceeds maximum limit of 6")
	ErrUnknownSourceChain      = errors.New("unknown source chain ID")
	ErrEpochMismatch           = errors.New("epoch mismatch for source chain")
	ErrAllocationExceeded      = errors.New("aggregate amount exceeds source chain allocation ceiling (Scenario 10.7)")
	ErrQuorumNotReached        = errors.New("BFT quorum stake threshold not reached")
	ErrCommitNotAttested       = errors.New("commit root has not been attested by source chain")
	ErrInvalidMerkleProof      = errors.New("invalid Merkle proof")
	ErrAlreadyClaimed          = errors.New("message has already been claimed or processed (idempotent guard)")
	ErrInvalidRefundState      = errors.New("cannot refund message: message is not in Pending status")
	ErrInvalidRefundProof      = errors.New("invalid failed execution proof for refund")
	ErrChainNotDead            = errors.New("target chain has not been declared dead")
	ErrDeadChainAlreadyClaimed = errors.New("account balance on dead chain has already been claimed")
	ErrNoActiveContext         = errors.New("no active cross-chain execution context")
	ErrNotCalledByGateway      = errors.New("caller is not authorized by GatewayPrecompile")
	ErrInvalidBLSSignature     = errors.New("BLS Quorum Certificate signature is invalid or empty")
)

// OutboundParams contains user/contract request parameters for outbound cross-chain messages.
type OutboundParams struct {
	DestChainID uint64         `json:"dest_chain_id"`
	Target      common.Address `json:"target"`
	Payload     []byte         `json:"payload"`
	AssetID     *big.Int       `json:"asset_id"`
	Value       *big.Int       `json:"value"`
	Tip         *big.Int       `json:"tip"`
	HopCount    uint8          `json:"hop_count"`
	Ordered     bool           `json:"ordered"`
}

// CrossChainContext stores execution context accessible via GetOriginalSender / IsCalledByGateway.
type CrossChainContext struct {
	OriginalSender common.Address `json:"original_sender"`
	SourceChainID  uint64         `json:"source_chain_id"`
	IsGateway      bool           `json:"is_gateway"`
}

// AllocationRejectedListener defines a hook triggered when a chain overdraws its allocation ceiling.
type AllocationRejectedListener func(chainID uint64, requested, available *big.Int)

// GatewayEngine implements the native light-client bridge state machine (Section 2.1 & 2.2).
type GatewayEngine struct {
	mu                         sync.RWMutex
	LocalChainID               uint64
	ChainRegistry              map[uint64]ChainRegistry
	SupplyLedger               *GlobalSupplyLedger
	AttestedCommits            map[string]AttestedCommit
	MessageStatus              map[common.Hash]MessageStatus
	DeadChains                 map[uint64]bool
	DeadChainClaimed           map[string]bool
	ActiveContext              *CrossChainContext
	LockedTips                 map[common.Hash]*big.Int
	ChannelSequence            map[string]uint64
	RelayerBalances            map[common.Address]*big.Int
	allocationRejectedListener AllocationRejectedListener

	// PendingCommitteeAttestations collects individual BLS signature shares for a pending
	// CommitteeUpdate, keyed by "sourceChainId:oldEpoch:payloadHashHex" (Milestone C). Cleared
	// once the corresponding committeeUpdate() succeeds.
	PendingCommitteeAttestations map[string][]CommitteeAttestationShare
	// RegisteredPops is a durable, permissionless Proof-of-Possession registry keyed by
	// hex(pubkeyBls) (Milestone C) — see registerCommitteePop()/getRegisteredPop() in
	// gateway_handler.go. Independent of ChainRegistry membership: anyone may register a PoP for
	// their own key at any time.
	RegisteredPops map[string][]byte
}

// NewGatewayEngine initializes a new GatewayEngine instance for the local chain.
func NewGatewayEngine(
	localChainID uint64,
	registry map[uint64]ChainRegistry,
	ledger *GlobalSupplyLedger,
) *GatewayEngine {
	return &GatewayEngine{
		LocalChainID:                 localChainID,
		ChainRegistry:                registry,
		SupplyLedger:                 ledger,
		AttestedCommits:              make(map[string]AttestedCommit),
		MessageStatus:                make(map[common.Hash]MessageStatus),
		DeadChains:                   make(map[uint64]bool),
		DeadChainClaimed:             make(map[string]bool),
		LockedTips:                   make(map[common.Hash]*big.Int),
		ChannelSequence:              make(map[string]uint64),
		RelayerBalances:              make(map[common.Address]*big.Int),
		PendingCommitteeAttestations: make(map[string][]CommitteeAttestationShare),
		RegisteredPops:               make(map[string][]byte),
	}
}

// SetAllocationRejectedListener registers an instant alert listener for overdraw events.
func (g *GatewayEngine) SetAllocationRejectedListener(l AllocationRejectedListener) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allocationRejectedListener = l
}

func Keccak256(data []byte) common.Hash {
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(data)
	var out common.Hash
	hasher.Sum(out[:0])
	return out
}

// hashPair combines two Merkle tree hashes with RFC 6962 domain separation (0x01 for internal nodes).
func hashPair(a, b common.Hash) common.Hash {
	var combined []byte
	combined = append(combined, 0x01) // Domain separation: 0x01 for internal node
	if bytes.Compare(a.Bytes(), b.Bytes()) <= 0 {
		combined = append(combined, a.Bytes()...)
		combined = append(combined, b.Bytes()...)
	} else {
		combined = append(combined, b.Bytes()...)
		combined = append(combined, a.Bytes()...)
	}
	return Keccak256(combined)
}

// VerifyMerkleProof verifies Merkle membership using domain-separated pair hashing.
func VerifyMerkleProof(leaf common.Hash, proof MerkleProof, expectedRoot common.Hash) bool {
	current := leaf
	for _, sibling := range proof.Siblings {
		current = hashPair(current, sibling)
	}
	return current == expectedRoot
}

// CanonicalEncodeMessage serializes a CrossChainMessage into canonical deterministic binary representation (Section 11.2).
// Fixed-width formatting for all numeric and hash fields with length prefix for variable payload ensures zero boundary ambiguity.
func CanonicalEncodeMessage(m CrossChainMessage) []byte {
	var buf []byte
	buf = append(buf, m.MessageID.Bytes()...)
	var cBuf [8]byte
	binary.BigEndian.PutUint64(cBuf[:], m.SourceChainID)
	buf = append(buf, cBuf[:]...)
	binary.BigEndian.PutUint64(cBuf[:], m.DestChainID)
	buf = append(buf, cBuf[:]...)
	binary.BigEndian.PutUint64(cBuf[:], m.Sequence)
	buf = append(buf, cBuf[:]...)
	buf = append(buf, m.Sender.Bytes()...)
	buf = append(buf, m.Target.Bytes()...)

	// Fixed 32 bytes for AssetID (uint256 big-endian)
	assetBytes := make([]byte, 32)
	if m.AssetID != nil {
		raw := m.AssetID.Bytes()
		if len(raw) <= 32 {
			copy(assetBytes[32-len(raw):], raw)
		}
	}
	buf = append(buf, assetBytes...)

	buf = append(buf, byte(m.HopCount))
	if m.Ordered {
		buf = append(buf, 0x01)
	} else {
		buf = append(buf, 0x00)
	}

	// Fixed 32 bytes for Value (uint256 big-endian)
	valBytes := make([]byte, 32)
	if m.Value != nil {
		raw := m.Value.Bytes()
		if len(raw) <= 32 {
			copy(valBytes[32-len(raw):], raw)
		}
	}
	buf = append(buf, valBytes...)

	// Fixed 32 bytes for Tip (uint256 big-endian)
	tipBytes := make([]byte, 32)
	if m.Tip != nil {
		raw := m.Tip.Bytes()
		if len(raw) <= 32 {
			copy(tipBytes[32-len(raw):], raw)
		}
	}
	buf = append(buf, tipBytes...)

	// 4-byte length prefix for Payload followed by payload bytes
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(m.Payload)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, m.Payload...)
	return buf
}

// ComputeMessageLeafHash computes the canonical leaf hash with 0x00 domain separation prefix.
func ComputeMessageLeafHash(m CrossChainMessage) common.Hash {
	data := append([]byte{0x00}, CanonicalEncodeMessage(m)...)
	return Keccak256(data)
}

// Outbound handles initiating a cross-chain message on the source chain (P2.1 & P2.5).
func (g *GatewayEngine) Outbound(
	sender common.Address,
	params OutboundParams,
	txHash common.Hash,
) (*CrossChainMessage, error) {
	if params.HopCount > MaxHopCount {
		return nil, fmt.Errorf("%w: got %d, max allowed %d", ErrHopCountExceeded, params.HopCount, MaxHopCount)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	messageID := txHash
	g.MessageStatus[messageID] = MessageStatusPending

	if params.Tip != nil && params.Tip.Sign() > 0 {
		g.LockedTips[messageID] = new(big.Int).Set(params.Tip)
	}

	seqKey := fmt.Sprintf("%d:%d", g.LocalChainID, params.DestChainID)
	seq := g.ChannelSequence[seqKey] + 1
	g.ChannelSequence[seqKey] = seq

	val := big.NewInt(0)
	if params.Value != nil {
		val = new(big.Int).Set(params.Value)
	}
	tip := big.NewInt(0)
	if params.Tip != nil {
		tip = new(big.Int).Set(params.Tip)
	}
	assetID := big.NewInt(0)
	if params.AssetID != nil {
		assetID = new(big.Int).Set(params.AssetID)
	}

	msg := &CrossChainMessage{
		MessageID:     messageID,
		SourceChainID: g.LocalChainID,
		DestChainID:   params.DestChainID,
		Sender:        sender,
		Target:        params.Target,
		Payload:       params.Payload,
		AssetID:       assetID,
		Value:         val,
		Sequence:      seq,
		Tip:           tip,
		HopCount:      params.HopCount,
		Ordered:       params.Ordered,
	}

	return msg, nil
}

// AttestCommit executes Phase 1 of Attest-then-Claim (P2.2) for a commit originating from a
// registered PRIVATE CHAIN. It enforces and debits per_chain_allocation[sourceChainID] — this is
// the ONLY leg of a transfer where a ceiling is meaningful (Section 2.3 step 2). The matching
// credit to the final destination chain happens in ClaimMessage (Section 2.3.1 fix), not here,
// because a single commit can route to multiple distinct destinations.
func (g *GatewayEngine) AttestCommit(
	sourceChainID uint64,
	commitRoot common.Hash,
	aggregateAmount *big.Int,
	cert QuorumCert,
) (*AttestedCommit, error) {
	return g.attestCommitInternal(sourceChainID, commitRoot, aggregateAmount, cert, true)
}

// AttestReserveIssuedCommit executes Phase 1 of Attest-then-Claim for a commit issued by RESERVE
// itself (the Reserve->destination leg, Section 2.3 step 3). Reserve is the unconditional issuer:
// there is no per_chain_allocation ceiling to enforce against Reserve's own entry, and this call
// MUST NOT debit it — doing so was the root cause of the "double debit, zero credit" supply
// invariant violation found during review (100 transferred -> 200 destroyed from the ledger).
// Full BLS quorum verification still applies; only the ceiling/debit step is skipped.
func (g *GatewayEngine) AttestReserveIssuedCommit(
	reserveChainID uint64,
	commitRoot common.Hash,
	aggregateAmount *big.Int,
	cert QuorumCert,
) (*AttestedCommit, error) {
	return g.attestCommitInternal(reserveChainID, commitRoot, aggregateAmount, cert, false)
}

func (g *GatewayEngine) attestCommitInternal(
	sourceChainID uint64,
	commitRoot common.Hash,
	aggregateAmount *big.Int,
	cert QuorumCert,
	enforceCeiling bool,
) (*AttestedCommit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	registry, exists := g.ChainRegistry[sourceChainID]
	if !exists {
		return nil, fmt.Errorf("%w: chain %d", ErrUnknownSourceChain, sourceChainID)
	}

	// Fail-closed epoch verification
	if cert.Epoch != registry.Epoch {
		return nil, fmt.Errorf("%w: expected %d, got %d", ErrEpochMismatch, registry.Epoch, cert.Epoch)
	}

	// Fail-closed: Committee cannot be empty
	if len(registry.Committee) == 0 {
		return nil, fmt.Errorf("%w: chain %d", ErrEmptyCommittee, sourceChainID)
	}

	// Real BLS Quorum Signature Verification (Cryptographic Defense)
	if len(cert.AggregateSignature) == 0 {
		return nil, ErrInvalidBLSSignature
	}

	var accumulatedStake uint64
	var totalStake uint64
	var votingPubkeys [][]byte

	for i, val := range registry.Committee {
		totalStake += val.Stake
		isSigner := false
		if len(cert.SignerBitmap) > 0 {
			byteIdx := i / 8
			bitIdx := uint(i % 8)
			if byteIdx < len(cert.SignerBitmap) && (cert.SignerBitmap[byteIdx]&(1<<bitIdx)) != 0 {
				isSigner = true
			}
		} else if len(registry.Committee) == 1 {
			// Single validator backward compatibility when bitmap is omitted
			isSigner = true
		}

		if isSigner {
			accumulatedStake += val.Stake
			votingPubkeys = append(votingPubkeys, val.PubkeyBLS)
		}
	}

	if totalStake == 0 {
		return nil, ErrZeroTotalStake
	}

	// Calculate quorum threshold: default 66.67% (BFT 2/3) or basis points from registry.QuorumThreshold
	threshold := (totalStake*2 + 2) / 3
	if registry.QuorumThreshold > 0 {
		threshold = (totalStake*uint64(registry.QuorumThreshold) + 9999) / 10000
	}

	if accumulatedStake < threshold || len(votingPubkeys) == 0 {
		return nil, fmt.Errorf("%w: accumulated stake %d < threshold %d", ErrQuorumNotReached, accumulatedStake, threshold)
	}

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	if len(votingPubkeys) == 1 {
		pubKey := cm.PubkeyFromBytes(votingPubkeys[0])
		sig := cm.SignFromBytes(cert.AggregateSignature)
		if !bls.VerifySign(pubKey, sig, commitMsg) {
			return nil, ErrInvalidBLSSignature
		}
	} else {
		msgs := make([][]byte, len(votingPubkeys))
		for i := range msgs {
			msgs[i] = commitMsg
		}
		if !bls.VerifyAggregateSign(votingPubkeys, cert.AggregateSignature, msgs) {
			return nil, ErrInvalidBLSSignature
		}
	}

	if aggregateAmount == nil {
		aggregateAmount = big.NewInt(0)
	}

	if enforceCeiling {
		// Check per_chain_allocation ceiling (Scenario 10.7) — only meaningful for a private
		// chain's own commit (the X -> Reserve leg). Reserve-issued commits skip this entirely.
		currentAlloc := g.SupplyLedger.GetAllocation(sourceChainID)
		if aggregateAmount.Cmp(currentAlloc) > 0 {
			if g.allocationRejectedListener != nil {
				g.allocationRejectedListener(sourceChainID, aggregateAmount, currentAlloc)
			}
			return nil, fmt.Errorf("%w: requested %s exceeds available %s", ErrAllocationExceeded, aggregateAmount.String(), currentAlloc.String())
		}

		// Debit source chain allocation upon successful BFT attestation. The matching credit to
		// the destination happens per-message in ClaimMessage (Section 2.3.1).
		g.SupplyLedger.PerChainAllocation[sourceChainID] = new(big.Int).Sub(currentAlloc, aggregateAmount)
	}

	key := fmt.Sprintf("%d:%s", sourceChainID, commitRoot.Hex())
	attested := AttestedCommit{
		SourceChainID: sourceChainID,
		CommitRoot:    commitRoot,
		Epoch:         cert.Epoch,
		FundedAmount:  new(big.Int).Set(aggregateAmount),
		ClaimedAmount: big.NewInt(0),
	}
	g.AttestedCommits[key] = attested

	return &attested, nil
}

// ClaimMessage executes Phase 2 of Attest-then-Claim (P2.3 & P2.6).
// Enforces hard-cap on FundedAmount, verifies domain-separated Merkle proof, guards against double-claim, sets execution context.
func (g *GatewayEngine) ClaimMessage(
	message CrossChainMessage,
	proof MerkleProof,
	commitRoot common.Hash,
	relayer common.Address,
) (MessageStatus, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	currentStatus, hasStatus := g.MessageStatus[message.MessageID]
	if hasStatus && currentStatus != MessageStatusPending {
		return currentStatus, fmt.Errorf("%w: message %s has status %d", ErrAlreadyClaimed, message.MessageID.Hex(), currentStatus)
	}

	key := fmt.Sprintf("%d:%s", message.SourceChainID, commitRoot.Hex())
	attested, exists := g.AttestedCommits[key]
	if !exists {
		// 2-hop routed transfers via Reserve / Hub Chain (Section 2.2(b))
		for chainID := range g.ChainRegistry {
			k := fmt.Sprintf("%d:%s", chainID, commitRoot.Hex())
			if a, ok := g.AttestedCommits[k]; ok {
				attested = a
				exists = true
				key = k
				break
			}
		}
	}
	if !exists {
		return MessageStatusPending, fmt.Errorf("%w: commit %s on chain %d", ErrCommitNotAttested, commitRoot.Hex(), message.SourceChainID)
	}

	// Hard-cap verification: ClaimedAmount + Value <= FundedAmount (Section 2.3.1)
	if message.Value != nil && message.Value.Sign() > 0 {
		newClaimed := new(big.Int).Add(attested.ClaimedAmount, message.Value)
		if newClaimed.Cmp(attested.FundedAmount) > 0 {
			return MessageStatusPending, fmt.Errorf("%w: commit cap %s exceeded by %s", ErrAllocationExceeded, attested.FundedAmount.String(), newClaimed.String())
		}
		attested.ClaimedAmount = newClaimed
		g.AttestedCommits[key] = attested
	}

	// Canonical leaf hash with 0x00 domain separation
	leafHash := ComputeMessageLeafHash(message)

	if !VerifyMerkleProof(leafHash, proof, commitRoot) {
		return MessageStatusPending, ErrInvalidMerkleProof
	}

	// Credit this chain's allocation ceiling with the value being finalized here (Section 2.3.1
	// fix). This is the missing counterpart to AttestCommit's debit — without it, value silently
	// evaporates from Σ per_chain_allocation on every successful transfer (proven via reproduction:
	// 100 transferred -> 200 destroyed). ClaimMessage is the correct place because it is the only
	// point that knows the message's real, individual DestChainID (a single attested commit can
	// route to several distinct destinations).
	if message.Value != nil && message.Value.Sign() > 0 && g.SupplyLedger != nil {
		if message.DestChainID != g.LocalChainID {
			return MessageStatusPending, fmt.Errorf("%w: message destChainId %d does not match claiming engine's chain %d", ErrInvalidMerkleProof, message.DestChainID, g.LocalChainID)
		}
		currentAlloc := g.SupplyLedger.GetAllocation(g.LocalChainID)
		g.SupplyLedger.PerChainAllocation[g.LocalChainID] = new(big.Int).Add(currentAlloc, message.Value)
	}

	// Set execution context for destination target contracts
	g.ActiveContext = &CrossChainContext{
		OriginalSender: message.Sender,
		SourceChainID:  message.SourceChainID,
		IsGateway:      true,
	}

	// Execution succeeds
	execStatus := MessageStatusSuccess

	// Clear execution context
	g.ActiveContext = nil

	g.MessageStatus[message.MessageID] = execStatus

	// Disburse tip to relayer (P2.3 & P4.2)
	if message.Tip != nil && message.Tip.Sign() > 0 {
		currBal := g.RelayerBalances[relayer]
		if currBal == nil {
			currBal = big.NewInt(0)
		}
		g.RelayerBalances[relayer] = new(big.Int).Add(currBal, message.Tip)
	}

	return execStatus, nil
}

// Refund processes returning funds to the sender on the source chain when the destination reverts (P2.4).
func (g *GatewayEngine) Refund(
	messageID common.Hash,
	sourceChainID uint64,
	sender common.Address,
	amount *big.Int,
	isFailedProofValid bool,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	status, exists := g.MessageStatus[messageID]
	if !exists {
		status = MessageStatusPending
	}

	if status != MessageStatusPending {
		return fmt.Errorf("%w: message %s current status is %d", ErrInvalidRefundState, messageID.Hex(), status)
	}

	if !isFailedProofValid {
		return ErrInvalidRefundProof
	}

	g.MessageStatus[messageID] = MessageStatusRefunded

	// Restore allocation in GlobalSupplyLedger to preserve total supply invariant (Section 2.4 #2)
	if g.SupplyLedger != nil && amount != nil && amount.Sign() > 0 {
		currAlloc, hasAlloc := g.SupplyLedger.PerChainAllocation[sourceChainID]
		if !hasAlloc || currAlloc == nil {
			currAlloc = big.NewInt(0)
		}
		g.SupplyLedger.PerChainAllocation[sourceChainID] = new(big.Int).Add(currAlloc, amount)
	}

	return nil
}

// VerifyAndExecute handles atomic verification & execution for low-volume messages (P2.7).
func (g *GatewayEngine) VerifyAndExecute(
	message CrossChainMessage,
	cert QuorumCert,
	proof MerkleProof,
	commitRoot common.Hash,
	relayer common.Address,
) (MessageStatus, error) {
	if _, err := g.AttestCommit(message.SourceChainID, commitRoot, message.Value, cert); err != nil {
		return MessageStatusPending, err
	}
	return g.ClaimMessage(message, proof, commitRoot, relayer)
}

// ClaimDeadChainBalance allows user to recover funds on Reserve using account-tree Merkle proof (P2.8).
func (g *GatewayEngine) ClaimDeadChainBalance(
	deadChainID uint64,
	account common.Address,
	amount *big.Int,
	proof MerkleProof,
	accountLeafHash common.Hash,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.DeadChains[deadChainID] {
		return fmt.Errorf("%w: chain %d", ErrChainNotDead, deadChainID)
	}

	claimKey := fmt.Sprintf("%d:%s", deadChainID, account.Hex())
	if g.DeadChainClaimed[claimKey] {
		return fmt.Errorf("%w: chain %d, account %s", ErrDeadChainAlreadyClaimed, deadChainID, account.Hex())
	}

	registry, exists := g.ChainRegistry[deadChainID]
	if !exists {
		return fmt.Errorf("%w: chain %d", ErrUnknownSourceChain, deadChainID)
	}

	if !VerifyMerkleProof(accountLeafHash, proof, registry.StateRoot) {
		return ErrInvalidMerkleProof
	}

	if amount != nil && amount.Sign() > 0 && g.SupplyLedger != nil {
		if err := g.SupplyLedger.TransferAllocation(deadChainID, g.LocalChainID, amount); err != nil {
			return err
		}
	}

	g.DeadChainClaimed[claimKey] = true
	return nil
}

// GetOriginalSender provides recipient contracts with verified cross-chain origin metadata.
func (g *GatewayEngine) GetOriginalSender() (common.Address, uint64, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.ActiveContext == nil || !g.ActiveContext.IsGateway {
		return common.Address{}, 0, ErrNoActiveContext
	}
	return g.ActiveContext.OriginalSender, g.ActiveContext.SourceChainID, nil
}

// IsCalledByGateway verifies that the execution call originates from GatewayPrecompile.
func (g *GatewayEngine) IsCalledByGateway() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.ActiveContext != nil && g.ActiveContext.IsGateway
}

// GetMessageStatus returns current lifecycle status for a given messageId.
func (g *GatewayEngine) GetMessageStatus(messageID common.Hash) MessageStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()

	status, exists := g.MessageStatus[messageID]
	if !exists {
		return MessageStatusPending
	}
	return status
}
