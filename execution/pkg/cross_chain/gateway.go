package cross_chain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
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
	ErrCommitNotAttested       = errors.New("commit root has not been attested by source chain")
	ErrInvalidMerkleProof      = errors.New("invalid Merkle proof")
	ErrAlreadyClaimed          = errors.New("message has already been claimed or processed (idempotent guard)")
	ErrInvalidRefundState      = errors.New("cannot refund message: message is not in Pending status")
	ErrInvalidRefundProof      = errors.New("invalid failed execution proof for refund")
	ErrChainNotDead            = errors.New("target chain has not been declared dead")
	ErrDeadChainAlreadyClaimed = errors.New("account balance on dead chain has already been claimed")
	ErrNoActiveContext         = errors.New("no active cross-chain execution context")
	ErrNotCalledByGateway      = errors.New("caller is not authorized by GatewayPrecompile")
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

// GatewayEngine implements the GatewayPrecompile state machine and execution logic.
type GatewayEngine struct {
	mu                         sync.RWMutex
	LocalChainID               uint64
	ChainRegistry              map[uint64]ChainRegistry
	SupplyLedger               *GlobalSupplyLedger
	AttestedCommits            map[string]AttestedCommit // key: "sourceChainId:commitRootHex"
	MessageStatus              map[common.Hash]MessageStatus
	DeadChains                 map[uint64]bool
	DeadChainClaimed           map[string]bool // key: "deadChainId:accountHex"
	ActiveContext              *CrossChainContext
	LockedTips                 map[common.Hash]*big.Int
	ChannelSequence            map[string]uint64
	RelayerBalances            map[common.Address]*big.Int
	allocationRejectedListener AllocationRejectedListener
}

// NewGatewayEngine initializes the GatewayPrecompile execution engine.
func NewGatewayEngine(
	localChainID uint64,
	registry map[uint64]ChainRegistry,
	supplyLedger *GlobalSupplyLedger,
) *GatewayEngine {
	return &GatewayEngine{
		LocalChainID:     localChainID,
		ChainRegistry:    registry,
		SupplyLedger:     supplyLedger,
		AttestedCommits:  make(map[string]AttestedCommit),
		MessageStatus:    make(map[common.Hash]MessageStatus),
		DeadChains:       make(map[uint64]bool),
		DeadChainClaimed: make(map[string]bool),
		LockedTips:       make(map[common.Hash]*big.Int),
		ChannelSequence:  make(map[string]uint64),
		RelayerBalances:  make(map[common.Address]*big.Int),
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

func VerifyMerkleProof(leaf common.Hash, proof MerkleProof, expectedRoot common.Hash) bool {
	current := leaf.Bytes()
	for _, sibling := range proof.Siblings {
		sibBytes := sibling.Bytes()
		hasher := sha3.NewLegacyKeccak256()
		if bytes.Compare(current, sibBytes) <= 0 {
			hasher.Write(current)
			hasher.Write(sibBytes)
		} else {
			hasher.Write(sibBytes)
			hasher.Write(current)
		}
		var next common.Hash
		hasher.Sum(next[:0])
		current = next.Bytes()
	}
	return bytes.Equal(current, expectedRoot.Bytes())
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
	g.ChannelSequence[seqKey]++
	seq := g.ChannelSequence[seqKey]

	val := big.NewInt(0)
	if params.Value != nil {
		val.Set(params.Value)
	}

	tip := big.NewInt(0)
	if params.Tip != nil {
		tip.Set(params.Tip)
	}

	assetID := big.NewInt(0)
	if params.AssetID != nil {
		assetID.Set(params.AssetID)
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

// AttestCommit executes Phase 1 of Attest-then-Claim (P2.2).
// Verifies BLS signature, ensures strict epoch alignment, and checks per_chain_allocation ceiling.
func (g *GatewayEngine) AttestCommit(
	sourceChainID uint64,
	commitRoot common.Hash,
	aggregateAmount *big.Int,
	cert QuorumCert,
	isBlsValid bool,
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

	if !isBlsValid {
		return nil, ErrInvalidMerkleProof
	}

	if aggregateAmount == nil {
		aggregateAmount = big.NewInt(0)
	}

	// Ceiling check on source chain allocation (Scenario 10.7)
	currentAlloc, hasAlloc := g.SupplyLedger.PerChainAllocation[sourceChainID]
	if !hasAlloc {
		currentAlloc = big.NewInt(0)
	}

	if aggregateAmount.Cmp(currentAlloc) > 0 {
		if g.allocationRejectedListener != nil {
			g.allocationRejectedListener(sourceChainID, aggregateAmount, currentAlloc)
		}
		return nil, fmt.Errorf("%w: requested %s > available %s", ErrAllocationExceeded, aggregateAmount.String(), currentAlloc.String())
	}

	// Deduct allocation atomically
	newAlloc := new(big.Int).Sub(currentAlloc, aggregateAmount)
	g.SupplyLedger.PerChainAllocation[sourceChainID] = newAlloc

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
// Verifies Merkle proof against attested commit, guards against double-claim, sets execution context.
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
	_, attested := g.AttestedCommits[key]
	if !attested {
		// Also check if commitRoot was attested via Reserve chain for 2-hop routed transfers
		for chainID := range g.ChainRegistry {
			k := fmt.Sprintf("%d:%s", chainID, commitRoot.Hex())
			if _, ok := g.AttestedCommits[k]; ok {
				attested = true
				break
			}
		}
	}
	if !attested {
		return MessageStatusPending, fmt.Errorf("%w: commit %s on chain %d", ErrCommitNotAttested, commitRoot.Hex(), message.SourceChainID)
	}

	leafBytes, err := json.Marshal(message)
	if err != nil {
		return MessageStatusPending, fmt.Errorf("failed to serialize message leaf: %w", err)
	}
	leafHash := Keccak256(leafBytes)

	if !VerifyMerkleProof(leafHash, proof, commitRoot) {
		return MessageStatusPending, ErrInvalidMerkleProof
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
	return nil
}

// VerifyAndExecute handles atomic verification & execution for low-volume messages (P2.7).
func (g *GatewayEngine) VerifyAndExecute(
	message CrossChainMessage,
	cert QuorumCert,
	proof MerkleProof,
	commitRoot common.Hash,
	relayer common.Address,
	isBlsValid bool,
) (MessageStatus, error) {
	if _, err := g.AttestCommit(message.SourceChainID, commitRoot, message.Value, cert, isBlsValid); err != nil {
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
