package cross_chain

import (
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestChain(chainID uint64, name string, stake uint64) FoundingChainConfig {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	pri := kp.PrivateKey()
	popSig := PopSign(pri, pub)

	entry := ValidatorEntry{
		PubkeyBLS:    pub.Bytes(),
		Stake:        stake,
		PopSignature: popSig.Bytes(),
	}

	return FoundingChainConfig{
		ChainID:    chainID,
		Name:       name,
		Validators: []ValidatorEntry{entry},
		TotalStake: stake,
	}
}

func TestRootAnchorCommittee_InitAndThresholdDoD(t *testing.T) {
	// 4 founding chains with 2500 stake each (total = 10,000)
	c1 := makeTestChain(101, "Chain-A", 2500)
	c2 := makeTestChain(102, "Chain-B", 2500)
	c3 := makeTestChain(103, "Chain-C", 2500)
	c4 := makeTestChain(104, "Chain-D", 2500)

	committee, err := NewRootAnchorCommittee([]FoundingChainConfig{c1, c2, c3, c4}, 33)
	require.NoError(t, err)

	assert.Equal(t, uint64(10000), committee.TotalStake)
	// Quorum = floor(2 * 10000 / 3) + 1 = 6667
	assert.Equal(t, uint64(6667), committee.BftQuorumThreshold())
	// Max faulty stake f = floor(9999 / 3) = 3333
	assert.Equal(t, uint64(3333), committee.MaxFaultyStake())

	// DoD Test: 1 founding chain offline (loss of 2500 stake <= 3333) -> Quorum reached!
	canReach, remaining, threshold := committee.SimulateChainOutage(101)
	assert.True(t, canReach)
	assert.Equal(t, uint64(7500), remaining)
	assert.Equal(t, uint64(6667), threshold)

	// Voting with 3 chains (c2, c3, c4) -> 7500 stake >= 6667
	votingKeys := [][]byte{
		c2.Validators[0].PubkeyBLS,
		c3.Validators[0].PubkeyBLS,
		c4.Validators[0].PubkeyBLS,
	}
	reached, accum, thresh := committee.VerifyQuorumVotes(votingKeys)
	assert.True(t, reached)
	assert.Equal(t, uint64(7500), accum)
	assert.Equal(t, uint64(6667), thresh)

	// Voting with only 2 chains (c2, c3) -> 5000 stake < 6667 -> Quorum not reached (Pending state)
	partialKeys := [][]byte{
		c2.Validators[0].PubkeyBLS,
		c3.Validators[0].PubkeyBLS,
	}
	reached2, accum2, _ := committee.VerifyQuorumVotes(partialKeys)
	assert.False(t, reached2)
	assert.Equal(t, uint64(5000), accum2)
}

func TestRootAnchorCommittee_GuardRails(t *testing.T) {
	c1 := makeTestChain(101, "Chain-A", 1000)
	c2 := makeTestChain(102, "Chain-B", 1000)
	c3 := makeTestChain(103, "Chain-C", 1000)

	// Only 3 founding chains -> Reject (minimum 4 required)
	_, errFew := NewRootAnchorCommittee([]FoundingChainConfig{c1, c2, c3}, 33)
	assert.ErrorIs(t, errFew, ErrInsufficientFoundingChains)

	// 4 chains, but 1 chain has 5000 / 8000 = 62.5% stake > 33% cap -> Reject
	cMonopoly := makeTestChain(104, "Chain-Monopoly", 5000)
	_, errCap := NewRootAnchorCommittee([]FoundingChainConfig{c1, c2, c3, cMonopoly}, 33)
	assert.ErrorIs(t, errCap, ErrStakeCapExceeded)

	// Duplicate chain ID -> Reject
	c1Dup := makeTestChain(101, "Chain-A-Dup", 1000)
	c4 := makeTestChain(104, "Chain-D", 1000)
	_, errDup := NewRootAnchorCommittee([]FoundingChainConfig{c1, c2, c3, c1Dup, c4}, 33)
	assert.ErrorIs(t, errDup, ErrDuplicateChainID)
}
