package cross_chain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGovernance_ProposeIsIdempotent_DoesNotResetVotes is a regression test: proposalID is
// deterministic (keccak256 of kind+proposedAt+payload), so re-submitting an identical propose()
// call — e.g. a relayer retry, or a malicious replay timed right before quorum is reached — used
// to silently overwrite the existing *GovernanceProposal with a fresh zero-vote one, wiping out
// every vote already cast. Propose() must now return the existing proposal unchanged instead.
func TestGovernance_ProposeIsIdempotent_DoesNotResetVotes(t *testing.T) {
	engine := NewGovernanceEngine([]uint64{101, 102, 103})

	kind := ProposalRegisterChain
	payload := []byte("same-payload")
	const proposedAt = uint64(1000)

	propID1, err := engine.Propose(kind, payload, proposedAt)
	require.NoError(t, err)

	// Cast 1 vote (below the 2/3-of-3 threshold of 2, so status stays Active).
	status, err := engine.Vote(propID1, 101, proposedAt+1)
	require.NoError(t, err)
	assert.Equal(t, ProposalStatusActive, status)
	assert.Equal(t, uint64(1), engine.GetProposal(propID1).VotesFor)

	// Re-submit the IDENTICAL propose() call (same kind/proposedAt/payload -> same proposalID).
	propID2, err := engine.Propose(kind, payload, proposedAt)
	require.NoError(t, err)
	assert.Equal(t, propID1, propID2, "identical inputs must produce the identical deterministic proposalID")

	// The vote already cast must still be there — NOT reset to 0.
	assert.Equal(t, uint64(1), engine.GetProposal(propID1).VotesFor, "re-proposing an identical proposal must not wipe out votes already cast")
	assert.True(t, engine.GetProposal(propID1).VotedChains[101])
}

func TestGovernance_QuorumThreshold(t *testing.T) {
	engine := NewGovernanceEngine(nil)
	_, err := engine.QuorumThreshold()
	assert.ErrorIs(t, err, ErrNoActiveChains)

	// N=1 -> 1
	engine.RegisterActiveChain(1)
	q1, err := engine.QuorumThreshold()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), q1)

	// N=2 -> 2
	engine.RegisterActiveChain(2)
	q2, _ := engine.QuorumThreshold()
	assert.Equal(t, uint64(2), q2)

	// N=3 -> 2
	engine.RegisterActiveChain(3)
	q3, _ := engine.QuorumThreshold()
	assert.Equal(t, uint64(2), q3)

	// N=4 -> 3
	engine.RegisterActiveChain(4)
	q4, _ := engine.QuorumThreshold()
	assert.Equal(t, uint64(3), q4)

	// N=5 -> 4
	engine.RegisterActiveChain(5)
	q5, _ := engine.QuorumThreshold()
	assert.Equal(t, uint64(4), q5)
}

func TestGovernance_FullLifecycleDoD(t *testing.T) {
	// 4 active chains (quorum >= 3)
	activeChains := []uint64{101, 102, 103, 104}
	timelock := uint64(72 * 3600) // 72 hours
	engine := NewGovernanceEngineWithTimelock(activeChains, timelock)

	t0 := uint64(1700000000)

	// Step 1: Propose
	propID, err := engine.Propose(ProposalRegisterChain, []byte{0x10, 0x20, 0x30}, t0)
	require.NoError(t, err)

	status, exists := engine.GetStatus(propID)
	require.True(t, exists)
	assert.Equal(t, ProposalStatusActive, status)

	// Step 2: Vote 1 (Chain 101) -> votes=1/4 -> Remains Active
	s1, err := engine.Vote(propID, 101, t0+100)
	require.NoError(t, err)
	assert.Equal(t, ProposalStatusActive, s1)

	// Step 3: Vote 2 (Chain 102) -> votes=2/4 -> Remains Active
	s2, err := engine.Vote(propID, 102, t0+200)
	require.NoError(t, err)
	assert.Equal(t, ProposalStatusActive, s2)

	// Sub-test (b): Try to execute while still Active (< 2/3 quorum) -> must fail
	_, errEarlyExec := engine.Execute(propID, t0+300)
	require.Error(t, errEarlyExec)
	assert.ErrorIs(t, errEarlyExec, ErrProposalNotTimelocked)

	// Step 4: Vote 3 (Chain 103) -> votes=3/4 (>=3 threshold) -> Transitions to Timelocked!
	tApproved := t0 + 500
	s3, err := engine.Vote(propID, 103, tApproved)
	require.NoError(t, err)
	assert.Equal(t, ProposalStatusTimelocked, s3)

	prop := engine.GetProposal(propID)
	require.NotNil(t, prop)
	assert.Equal(t, tApproved+timelock, prop.EffectiveAt)

	// Sub-test (c): Try to execute before 72h expired -> must fail
	_, errTimelock := engine.Execute(propID, tApproved+timelock-1)
	require.Error(t, errTimelock)
	assert.ErrorIs(t, errTimelock, ErrTimelockNotExpired)

	// Sub-test (d): Execute at or after 72h -> succeeds!
	executedProp, err := engine.Execute(propID, tApproved+timelock)
	require.NoError(t, err)
	assert.True(t, executedProp.Executed)

	statusExecuted, _ := engine.GetStatus(propID)
	assert.Equal(t, ProposalStatusExecuted, statusExecuted)

	// Sub-test (d-idempotent): Calling execute second time must fail with ErrAlreadyExecuted
	_, errSecondExec := engine.Execute(propID, tApproved+timelock+10)
	require.ErrorIs(t, errSecondExec, ErrAlreadyExecuted)

	// Sub-test (e): Double vote from same chain rejected
	prop2ID, err := engine.Propose(ProposalUpdateCommittee, []byte{1}, t0)
	require.NoError(t, err)
	_, err = engine.Vote(prop2ID, 101, t0+10)
	require.NoError(t, err)
	_, errDoubleVote := engine.Vote(prop2ID, 101, t0+20)
	require.ErrorIs(t, errDoubleVote, ErrAlreadyVoted)

	// Sub-test (f): Unregistered chain vote rejected
	_, errUnregistered := engine.Vote(prop2ID, 999, t0+30)
	require.ErrorIs(t, errUnregistered, ErrChainNotRegistered)
}
