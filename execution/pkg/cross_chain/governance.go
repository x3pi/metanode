package cross_chain

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// DefaultGovernanceTimelockSeconds is the mandatory 72-hour timelock delay (Section 1.3 #3 & 5.4).
	DefaultGovernanceTimelockSeconds uint64 = 72 * 3600
)

type GovernanceProposalStatus uint8

const (
	ProposalStatusActive     GovernanceProposalStatus = 0
	ProposalStatusTimelocked GovernanceProposalStatus = 1
	ProposalStatusExecuted   GovernanceProposalStatus = 2
	ProposalStatusRejected   GovernanceProposalStatus = 3
)

var (
	ErrProposalNotFound      = errors.New("governance proposal not found")
	ErrChainNotRegistered    = errors.New("chain is not an active registered chain")
	ErrAlreadyVoted          = errors.New("chain has already voted for this proposal")
	ErrProposalNotActive     = errors.New("proposal is not in active voting status")
	ErrProposalNotTimelocked = errors.New("proposal is not in timelocked status")
	ErrTimelockNotExpired    = errors.New("mandatory 72-hour timelock has not expired")
	ErrAlreadyExecuted       = errors.New("proposal has already been executed")
	ErrNoActiveChains        = errors.New("cannot compute quorum: no active chains registered")
)

// GovernanceEngine manages the proposal lifecycle on Root Anchor:
// propose -> vote -> >=2/3 active chains -> 72h timelock -> executed.
// 1 chain = 1 vote (counted by number of active registered chains, not stake).
type GovernanceEngine struct {
	ActiveChains         map[uint64]bool                          `json:"active_chains"`
	Proposals            map[common.Hash]*GovernanceProposal      `json:"proposals"`
	ProposalStatus       map[common.Hash]GovernanceProposalStatus `json:"proposal_status"`
	TimelockDelaySeconds uint64                                   `json:"timelock_delay_seconds"`
}

// NewGovernanceEngine creates a new governance engine with default 72h timelock.
func NewGovernanceEngine(activeChains []uint64) *GovernanceEngine {
	return NewGovernanceEngineWithTimelock(activeChains, DefaultGovernanceTimelockSeconds)
}

// NewGovernanceEngineWithTimelock creates a governance engine with custom timelock (for testing).
func NewGovernanceEngineWithTimelock(activeChains []uint64, timelockDelaySeconds uint64) *GovernanceEngine {
	chainsMap := make(map[uint64]bool, len(activeChains))
	for _, c := range activeChains {
		chainsMap[c] = true
	}
	return &GovernanceEngine{
		ActiveChains:         chainsMap,
		Proposals:            make(map[common.Hash]*GovernanceProposal),
		ProposalStatus:       make(map[common.Hash]GovernanceProposalStatus),
		TimelockDelaySeconds: timelockDelaySeconds,
	}
}

// RegisterActiveChain adds a newly approved chain into the governance voter pool.
func (g *GovernanceEngine) RegisterActiveChain(chainID uint64) {
	g.ActiveChains[chainID] = true
}

// UnregisterActiveChain removes a chain from the governance voter pool.
func (g *GovernanceEngine) UnregisterActiveChain(chainID uint64) {
	delete(g.ActiveChains, chainID)
}

// QuorumThreshold computes the >= 2/3 active chains threshold: ceil(2N/3) = (2N + 2) / 3.
func (g *GovernanceEngine) QuorumThreshold() (uint64, error) {
	n := uint64(len(g.ActiveChains))
	if n == 0 {
		return 0, ErrNoActiveChains
	}
	return (2*n + 2) / 3, nil
}

// Propose submits a new governance proposal and returns the deterministic proposalID.
func (g *GovernanceEngine) Propose(kind GovernanceProposalKind, payload []byte, proposedAt uint64) (common.Hash, error) {
	var buf []byte
	buf = append(buf, byte(kind))
	var tsBytes [8]byte
	binary.BigEndian.PutUint64(tsBytes[:], proposedAt)
	buf = append(buf, tsBytes[:]...)
	buf = append(buf, payload...)

	proposalID := crypto.Keccak256Hash(buf)
	if _, exists := g.Proposals[proposalID]; exists {
		return proposalID, nil
	}

	proposal := &GovernanceProposal{
		ProposalID:  proposalID,
		Kind:        kind,
		Payload:     payload,
		VotesFor:    0,
		VotedChains: make(map[uint64]bool),
		ProposedAt:  proposedAt,
		EffectiveAt: 0,
		Executed:    false,
	}

	g.Proposals[proposalID] = proposal
	g.ProposalStatus[proposalID] = ProposalStatusActive

	return proposalID, nil
}

// Vote casts a vote from an active registered chain.
// If votes >= ceil(2N/3), transitions to Timelocked and sets effective_at = currentTimestamp + timelockDelay.
func (g *GovernanceEngine) Vote(proposalID common.Hash, voterChainID uint64, currentTimestamp uint64) (GovernanceProposalStatus, error) {
	if !g.ActiveChains[voterChainID] {
		return ProposalStatusActive, fmt.Errorf("%w: chain %d", ErrChainNotRegistered, voterChainID)
	}

	status, exists := g.ProposalStatus[proposalID]
	if !exists {
		return ProposalStatusActive, ErrProposalNotFound
	}
	if status != ProposalStatusActive {
		return status, fmt.Errorf("%w: current status %d", ErrProposalNotActive, status)
	}

	proposal := g.Proposals[proposalID]
	if proposal.VotedChains[voterChainID] {
		return status, fmt.Errorf("%w: chain %d", ErrAlreadyVoted, voterChainID)
	}

	proposal.VotedChains[voterChainID] = true
	proposal.VotesFor = uint64(len(proposal.VotedChains))

	threshold, err := g.QuorumThreshold()
	if err != nil {
		return status, err
	}

	if proposal.VotesFor >= threshold {
		proposal.EffectiveAt = currentTimestamp + g.TimelockDelaySeconds
		g.ProposalStatus[proposalID] = ProposalStatusTimelocked
		return ProposalStatusTimelocked, nil
	}

	return ProposalStatusActive, nil
}

// Execute executes an approved proposal after the mandatory 72h timelock.
// Strictly idempotent: second call fails with ErrAlreadyExecuted.
func (g *GovernanceEngine) Execute(proposalID common.Hash, currentTimestamp uint64) (*GovernanceProposal, error) {
	status, exists := g.ProposalStatus[proposalID]
	if !exists {
		return nil, ErrProposalNotFound
	}

	if status == ProposalStatusExecuted {
		return nil, ErrAlreadyExecuted
	}
	if status != ProposalStatusTimelocked {
		return nil, fmt.Errorf("%w: current status %d", ErrProposalNotTimelocked, status)
	}

	proposal := g.Proposals[proposalID]
	if currentTimestamp < proposal.EffectiveAt {
		return nil, fmt.Errorf("%w: current %d, effective_at %d", ErrTimelockNotExpired, currentTimestamp, proposal.EffectiveAt)
	}

	proposal.Executed = true
	g.ProposalStatus[proposalID] = ProposalStatusExecuted

	return proposal, nil
}

func (g *GovernanceEngine) GetProposal(proposalID common.Hash) *GovernanceProposal {
	return g.Proposals[proposalID]
}

func (g *GovernanceEngine) GetStatus(proposalID common.Hash) (GovernanceProposalStatus, bool) {
	status, exists := g.ProposalStatus[proposalID]
	return status, exists
}
