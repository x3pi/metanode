package cross_chain

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

const (
	// MinFoundingChains is the strict minimum number of founding private chains required (Section 1.3 #5 & 5.2).
	MinFoundingChains = 4

	// DefaultMaxStakeCapPercent is the maximum allowed stake contribution cap per chain (default 33%).
	DefaultMaxStakeCapPercent uint8 = 33
)

var (
	ErrInsufficientFoundingChains = errors.New("Root Anchor requires at least 4 founding chains")
	ErrStakeCapExceeded           = errors.New("founding chain stake exceeds maximum allowed cap percentage")
	ErrDuplicateChainID           = errors.New("duplicate founding chain ID detected")
	ErrZeroTotalStake             = errors.New("total committee stake cannot be zero")
)

// FoundingChainConfig represents a founding private chain contributing validators to Root Anchor.
type FoundingChainConfig struct {
	ChainID    uint64           `json:"chain_id"`
	Name       string           `json:"name"`
	Validators []ValidatorEntry `json:"validators"`
	TotalStake uint64           `json:"total_stake"`
}

// RootAnchorGenesisConfig specifies the genesis parameters for the Root Anchor chain.
type RootAnchorGenesisConfig struct {
	ChainID            uint64                `json:"chain_id"`
	GenesisTotalSupply *big.Int              `json:"genesis_total_supply"`
	FoundingChains     []FoundingChainConfig `json:"founding_chains"`
	InitialAllocations map[uint64]*big.Int   `json:"initial_allocations"`
}

// RootAnchorCommittee manages the aggregated validator committee on the Root Anchor chain.
type RootAnchorCommittee struct {
	FoundingChains     []FoundingChainConfig `json:"founding_chains"`
	MaxStakeCapPercent uint8                 `json:"max_stake_cap_percent"`
	AllValidators      []ValidatorEntry      `json:"all_validators"`
	StakeByChain       map[uint64]uint64     `json:"stake_by_chain"`
	TotalStake         uint64                `json:"total_stake"`
}

// NewRootAnchorCommittee aggregates >= 4 founding private chains into the Root Anchor committee.
func NewRootAnchorCommittee(foundingChains []FoundingChainConfig, maxStakeCapPercent uint8) (*RootAnchorCommittee, error) {
	if len(foundingChains) < MinFoundingChains {
		return nil, fmt.Errorf("%w: got %d, expected >= %d", ErrInsufficientFoundingChains, len(foundingChains), MinFoundingChains)
	}

	if maxStakeCapPercent == 0 {
		maxStakeCapPercent = DefaultMaxStakeCapPercent
	}

	seenChains := make(map[uint64]bool, len(foundingChains))
	var totalStake uint64
	allValidators := make([]ValidatorEntry, 0)
	stakeByChain := make(map[uint64]uint64, len(foundingChains))

	for _, chain := range foundingChains {
		if seenChains[chain.ChainID] {
			return nil, fmt.Errorf("%w: chain %d", ErrDuplicateChainID, chain.ChainID)
		}
		seenChains[chain.ChainID] = true

		// Validate PoP for all validators in the founding chain
		if err := ValidateCommittee(chain.Validators); err != nil {
			return nil, fmt.Errorf("PoP validation failed for founding chain %d: %w", chain.ChainID, err)
		}

		var chainStake uint64
		for _, v := range chain.Validators {
			chainStake += v.Stake
		}

		totalStake += chainStake
		stakeByChain[chain.ChainID] = chainStake
		allValidators = append(allValidators, chain.Validators...)
	}

	if totalStake == 0 {
		return nil, ErrZeroTotalStake
	}

	// Enforce max stake cap per chain
	for _, chain := range foundingChains {
		chainStake := stakeByChain[chain.ChainID]
		maxAllowed := (totalStake * uint64(maxStakeCapPercent)) / 100
		if chainStake > maxAllowed {
			return nil, fmt.Errorf("%w: chain %d has %d stake (%d%% > %d%% cap, max allowed %d)",
				ErrStakeCapExceeded, chain.ChainID, chainStake, (chainStake*100)/totalStake, maxStakeCapPercent, maxAllowed)
		}
	}

	return &RootAnchorCommittee{
		FoundingChains:     foundingChains,
		MaxStakeCapPercent: maxStakeCapPercent,
		AllValidators:      allValidators,
		StakeByChain:       stakeByChain,
		TotalStake:         totalStake,
	}, nil
}

// BftQuorumThreshold computes the BFT quorum threshold: 2f + 1 = floor(2 * TotalStake / 3) + 1.
func (c *RootAnchorCommittee) BftQuorumThreshold() uint64 {
	return (2*c.TotalStake)/3 + 1
}

// MaxFaultyStake computes the maximum Byzantine / faulty stake tolerance: f = floor((TotalStake - 1) / 3).
func (c *RootAnchorCommittee) MaxFaultyStake() uint64 {
	if c.TotalStake == 0 {
		return 0
	}
	return (c.TotalStake - 1) / 3
}

// SimulateChainOutage tests if remaining committee reaches BFT Quorum when a founding chain is offline.
// Returns (canReachQuorum, remainingStake, quorumThreshold).
func (c *RootAnchorCommittee) SimulateChainOutage(offlineChainID uint64) (bool, uint64, uint64) {
	offlineStake := c.StakeByChain[offlineChainID]
	var remainingStake uint64
	if c.TotalStake >= offlineStake {
		remainingStake = c.TotalStake - offlineStake
	}
	threshold := c.BftQuorumThreshold()
	canReach := remainingStake >= threshold
	return canReach, remainingStake, threshold
}

// VerifyQuorumVotes checks whether a set of voting validator public keys satisfies the BFT Quorum.
func (c *RootAnchorCommittee) VerifyQuorumVotes(votingPubkeys [][]byte) (bool, uint64, uint64) {
	threshold := c.BftQuorumThreshold()
	var accumulatedStake uint64
	seenKeys := make([][]byte, 0, len(votingPubkeys))

	for _, entry := range c.AllValidators {
		isVoting := false
		for _, vk := range votingPubkeys {
			if bytes.Equal(vk, entry.PubkeyBLS) {
				isVoting = true
				break
			}
		}

		if isVoting {
			alreadyCounted := false
			for _, sk := range seenKeys {
				if bytes.Equal(sk, entry.PubkeyBLS) {
					alreadyCounted = true
					break
				}
			}
			if !alreadyCounted {
				accumulatedStake += entry.Stake
				seenKeys = append(seenKeys, entry.PubkeyBLS)
			}
		}
	}

	return accumulatedStake >= threshold, accumulatedStake, threshold
}

// VerifyQuorumCert validates both cryptographic BLS aggregate signature and BFT stake quorum (Section 1.3 #2).
func (c *RootAnchorCommittee) VerifyQuorumCert(
	votingPubkeys [][]byte,
	cert QuorumCert,
	digest []byte,
) (bool, uint64, uint64, error) {
	if len(cert.AggregateSignature) == 0 {
		return false, 0, c.BftQuorumThreshold(), ErrInvalidBLSSignature
	}

	quorumMet, accumStake, threshold := c.VerifyQuorumVotes(votingPubkeys)
	if !quorumMet {
		return false, accumStake, threshold, fmt.Errorf("quorum not reached: stake %d < threshold %d", accumStake, threshold)
	}

	msgs := make([][]byte, len(votingPubkeys))
	for i := range msgs {
		msgs[i] = digest
	}

	if !bls.VerifyAggregateSign(votingPubkeys, cert.AggregateSignature, msgs) {
		return false, accumStake, threshold, ErrInvalidBLSSignature
	}

	return true, accumStake, threshold, nil
}
