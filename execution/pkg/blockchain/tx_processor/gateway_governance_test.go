package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
)

// signGovernanceVote produces the (signerPubkeyBls, signature) pair a member of voterChainID's
// committee must supply to vote() so gateway_handler.go's BLS-membership check passes (Milestone
// G security fix — GovernanceEngine.Vote itself trusts whatever voterChainID its caller names, so
// the handler requires proof the caller actually speaks for that chain).
func signGovernanceVote(kp *bls.KeyPair, proposalID common.Hash, voterChainID uint64) ([]byte, []byte) {
	msg := cross_chain.ComputeGovernanceVoteMessage(proposalID, voterChainID)
	sig := bls.Sign(kp.PrivateKey(), msg)
	return kp.PublicKey().Bytes(), sig.Bytes()
}

// TestGatewayHandler_Vote_RejectsUnauthenticatedImpersonation is the regression test for the
// Milestone G security fix: GovernanceEngine.Vote itself trusts whatever voterChainID its caller
// names, with no notion of who is actually calling — the ORIGINAL Milestone G wiring called it
// straight from the public "vote" ABI method with no authentication at all, so any caller could
// cast any registered chain's single governance vote just by naming its ID. This must now be
// rejected: only a valid BLS signature from a member of that chain's own CURRENT committee may
// cast its vote.
func TestGatewayHandler_Vote_RejectsUnauthenticatedImpersonation(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp101 := bls.GenerateKeyPair()
	kpRogue := bls.GenerateKeyPair() // NOT a member of chain 101's committee

	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.PublicKey().Bytes(), Stake: 100}}},
		102: {ChainID: 102, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: bls.GenerateKeyPair().PublicKey().Bytes(), Stake: 100}}},
	}
	engine.Governance = cross_chain.NewGovernanceEngine([]uint64{101, 102})
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	payload, err := json.Marshal(uint64(999))
	require.NoError(t, err)
	proposeCalldata, err := h.abi.Pack("propose", uint8(cross_chain.ProposalDeclareChainDead), payload, uint64(100))
	require.NoError(t, err)
	proposeFee := big.NewInt(100_000_000_000_000_000) // 0.1 MTN anti-spam fee
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, proposeFee, marshalCallData(t, proposeCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 100)
	require.False(t, failed)
	out, err := h.abi.Unpack("propose", rcp.Return())
	require.NoError(t, err)
	proposalID := common.Hash(out[0].([32]byte))

	// Attack 1: rogue key not on chain 101's committee at all -> REJECTED
	pubRogue, sigRogue := signGovernanceVote(kpRogue, proposalID, 101)
	voteRogue, err := h.abi.Pack("vote", proposalID, big.NewInt(101), uint64(101), pubRogue, sigRogue)
	require.NoError(t, err)
	_, _, failedRogue := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, voteRogue)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	assert.True(t, failedRogue, "vote from a non-committee-member key must be rejected")

	// Attack 2: a REAL committee member's key, but for the WRONG chain (101's own valid key/sig
	// replayed to cast chain 102's vote) -> REJECTED, since kp101 is not a member of 102's committee.
	pub101As102, sig101As102 := signGovernanceVote(kp101, proposalID, 102)
	voteWrongChain, err := h.abi.Pack("vote", proposalID, big.NewInt(102), uint64(101), pub101As102, sig101As102)
	require.NoError(t, err)
	_, _, failedWrongChain := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, voteWrongChain)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	assert.True(t, failedWrongChain, "a chain's own committee key must not be able to cast a DIFFERENT chain's vote")

	// Attack 3: real committee member, but signature is over a DIFFERENT proposalId (replay) -> REJECTED
	otherProposalID := common.HexToHash("0xDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF")
	_, sigWrongProposal := signGovernanceVote(kp101, otherProposalID, 101)
	voteReplayed, err := h.abi.Pack("vote", proposalID, big.NewInt(101), uint64(101), kp101.PublicKey().Bytes(), sigWrongProposal)
	require.NoError(t, err)
	_, _, failedReplay := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, voteReplayed)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	assert.True(t, failedReplay, "a signature over a different proposalId must not authenticate this vote")

	// Sanity: the REAL vote, correctly signed, must succeed.
	pub101, sig101 := signGovernanceVote(kp101, proposalID, 101)
	voteValid, err := h.abi.Pack("vote", proposalID, big.NewInt(101), uint64(101), pub101, sig101)
	require.NoError(t, err)
	_, _, failedValid := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, voteValid)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	assert.False(t, failedValid, "correctly-authenticated vote must succeed")
}

func TestGatewayHandler_Governance_OnboardNewChainLifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	// Step 1: Seed Root Anchor with 3 active chains: 101, 102, 103 (Quorum: (2*3+2)/3 = 2 votes).
	// Each needs a real committee member so vote() can verify a BLS-authenticated vote as that
	// chain (Milestone G security fix).
	kp101 := bls.GenerateKeyPair()
	kp102 := bls.GenerateKeyPair()
	kp103 := bls.GenerateKeyPair()
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.PublicKey().Bytes(), Stake: 100}}},
		102: {ChainID: 102, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp102.PublicKey().Bytes(), Stake: 100}}},
		103: {ChainID: 103, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp103.PublicKey().Bytes(), Stake: 100}}},
	}
	engine.Governance = cross_chain.NewGovernanceEngine([]uint64{101, 102, 103})
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")

	// Step 2: Propose onboarding new chain 104
	newChainReg := cross_chain.ChainRegistry{
		ChainID:          104,
		Epoch:            1,
		QuorumThreshold:  6667,
		GatewayContract:  common.HexToAddress("0x9999999999999999999999999999999999999999"),
		ArchivalEndpoint: "https://rpc.chain104.io",
	}
	payload, err := json.Marshal(newChainReg)
	require.NoError(t, err)

	const proposedAt = uint64(1000)
	proposeCalldata, err := h.abi.Pack("propose", uint8(cross_chain.ProposalRegisterChain), payload, proposedAt)
	require.NoError(t, err)

	proposeFee := big.NewInt(100_000_000_000_000_000) // 0.1 MTN anti-spam fee
	proposeTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, proposeFee, marshalCallData(t, proposeCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, proposeTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, proposedAt)
	require.False(t, failed)
	require.NotNil(t, rcp)

	outValues, err := h.abi.Unpack("propose", rcp.Return())
	require.NoError(t, err)
	proposalID := common.Hash(outValues[0].([32]byte))

	// Verify proposal status via view call
	propCalldata, err := h.abi.Pack("getProposal", proposalID)
	require.NoError(t, err)
	propViewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, propCalldata))
	propRes, err := h.HandleOffChainQuery(cs, propViewTx)
	require.NoError(t, err)
	propFields, err := h.abi.Unpack("getProposal", propRes)
	require.NoError(t, err)
	assert.True(t, propFields[0].(bool), "Proposal must exist")
	assert.Equal(t, uint8(cross_chain.ProposalStatusActive), propFields[7].(uint8), "Status must be Active")
	assert.Equal(t, uint64(0), propFields[3].(uint64), "Votes must be 0")

	// Step 3: Vote 1 from Chain 101 (1/2 votes)
	pub101, sig101 := signGovernanceVote(kp101, proposalID, 101)
	vote1Calldata, err := h.abi.Pack("vote", proposalID, big.NewInt(101), proposedAt+10, pub101, sig101)
	require.NoError(t, err)
	vote1Tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote1Calldata))
	_, _, failed = h.HandleTransaction(context.Background(), cs, vote1Tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, proposedAt+10)
	require.False(t, failed)

	// Step 4: Vote 2 from Chain 102 (2/2 votes -> reaches quorum -> Timelocked)
	const vote2Time = proposedAt + 20
	pub102, sig102 := signGovernanceVote(kp102, proposalID, 102)
	vote2Calldata, err := h.abi.Pack("vote", proposalID, big.NewInt(102), vote2Time, pub102, sig102)
	require.NoError(t, err)
	vote2Tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote2Calldata))
	_, _, failed = h.HandleTransaction(context.Background(), cs, vote2Tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, vote2Time)
	require.False(t, failed)

	// Verify status is Timelocked
	propRes, err = h.HandleOffChainQuery(cs, propViewTx)
	require.NoError(t, err)
	propFields, err = h.abi.Unpack("getProposal", propRes)
	require.NoError(t, err)
	assert.Equal(t, uint8(cross_chain.ProposalStatusTimelocked), propFields[7].(uint8), "Status must be Timelocked")
	effectiveAt := propFields[5].(uint64)
	assert.Equal(t, vote2Time+cross_chain.DefaultGovernanceTimelockSeconds, effectiveAt)

	// Step 5: Premature execution before 72h timelock -> REJECTED
	execPrematureCalldata, err := h.abi.Pack("executeProposal", proposalID, effectiveAt-1)
	require.NoError(t, err)
	execPrematureTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, execPrematureCalldata))
	_, _, failed = h.HandleTransaction(context.Background(), cs, execPrematureTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, effectiveAt-1)
	assert.True(t, failed, "Execution before timelock expiry must revert")

	// Step 6: Valid execution after 72h timelock -> SUCCESS
	execCalldata, err := h.abi.Pack("executeProposal", proposalID, effectiveAt+1)
	require.NoError(t, err)
	execTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, execCalldata))
	_, _, failed = h.HandleTransaction(context.Background(), cs, execTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, effectiveAt+1)
	require.False(t, failed, "Execution after timelock expiry must succeed")

	// Step 7: Verify chain 104 is now registered in ChainRegistry & ActiveChains
	engineAfter, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	reg104, exists := engineAfter.ChainRegistry[104]
	assert.True(t, exists, "Chain 104 must be registered in GatewayEngine.ChainRegistry")
	assert.Equal(t, uint64(104), reg104.ChainID)
	assert.Equal(t, "https://rpc.chain104.io", reg104.ArchivalEndpoint)
	assert.True(t, engineAfter.Governance.ActiveChains[104], "Chain 104 must be active in Governance voter pool")

	// Step 8: Duplicate execution -> REJECTED (write-once / idempotent)
	_, _, failed = h.HandleTransaction(context.Background(), cs, execTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, effectiveAt+2)
	assert.True(t, failed, "Second execution of already executed proposal must revert")
}

func TestGatewayHandler_Governance_AssetRegistrationLifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	// Seed with chain 101 and 102, each with a real committee member (Milestone G security fix).
	kp101 := bls.GenerateKeyPair()
	kp102 := bls.GenerateKeyPair()
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.PublicKey().Bytes(), Stake: 100}}},
		102: {ChainID: 102, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp102.PublicKey().Bytes(), Stake: 100}}},
	}
	engine.Governance = cross_chain.NewGovernanceEngineWithTimelock([]uint64{101, 102}, 10) // 10s timelock for test
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")

	assetEntry := cross_chain.AssetEntry{
		AssetID:           big.NewInt(777),
		HomeChainID:       101,
		CanonicalContract: common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
	}
	payload, err := json.Marshal(assetEntry)
	require.NoError(t, err)

	// Propose asset
	proposeCalldata, err := h.abi.Pack("propose", uint8(cross_chain.ProposalRegisterAsset), payload, uint64(100))
	require.NoError(t, err)
	proposeFee := big.NewInt(100_000_000_000_000_000) // 0.1 MTN anti-spam fee
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, proposeFee, marshalCallData(t, proposeCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 100)
	require.False(t, failed)
	out, err := h.abi.Unpack("propose", rcp.Return())
	require.NoError(t, err)
	proposalID := common.Hash(out[0].([32]byte))

	// Vote from 101 and 102
	pub101, sig101 := signGovernanceVote(kp101, proposalID, 101)
	vote1, _ := h.abi.Pack("vote", proposalID, big.NewInt(101), uint64(101), pub101, sig101)
	_, _, failVote1 := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote1)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	require.False(t, failVote1)

	pub102, sig102 := signGovernanceVote(kp102, proposalID, 102)
	vote2, _ := h.abi.Pack("vote", proposalID, big.NewInt(102), uint64(102), pub102, sig102)
	_, _, failVote2 := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote2)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 102)
	require.False(t, failVote2)

	// Execute proposal at t=120
	execCalldata, _ := h.abi.Pack("executeProposal", proposalID, uint64(120))
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, execCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 120)
	require.False(t, failed)

	// Register Asset on Root Anchor with total supply 1,000,000
	regAssetCalldata, err := h.abi.Pack("registerAsset", proposalID, big.NewInt(1_000_000))
	require.NoError(t, err)
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, regAssetCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 121)
	require.False(t, failed)

	// Query Asset via getAsset
	getAssetCalldata, _ := h.abi.Pack("getAsset", big.NewInt(777))
	assetRes, err := h.HandleOffChainQuery(cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, getAssetCalldata)))
	require.NoError(t, err)
	assetFields, err := h.abi.Unpack("getAsset", assetRes)
	require.NoError(t, err)
	assert.True(t, assetFields[0].(bool), "Asset 777 must exist")
	assert.Equal(t, big.NewInt(101), assetFields[1].(*big.Int), "HomeChainID must be 101")
	assert.Equal(t, common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), assetFields[2].(common.Address))
	assert.True(t, assetFields[3].(bool), "Asset must be active")

	// Negative Test: Unapproved / Fake proposal cannot register asset
	fakeProposalID := common.HexToHash("0xDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF")
	fakeRegCalldata, _ := h.abi.Pack("registerAsset", fakeProposalID, big.NewInt(100))
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, fakeRegCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 122)
	assert.True(t, failed, "Fake proposal asset registration must revert")
}

func TestGatewayHandler_Governance_UpdateCommitteeLifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	kp101 := bls.GenerateKeyPair()
	kp102 := bls.GenerateKeyPair()
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.PublicKey().Bytes(), Stake: 100}}},
		102: {ChainID: 102, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp102.PublicKey().Bytes(), Stake: 100}}},
	}
	engine.Governance = cross_chain.NewGovernanceEngineWithTimelock([]uint64{101, 102}, 10) // 10s timelock
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x3333333333333333333333333333333333333333")

	kpNew1 := bls.GenerateKeyPair()
	kpNew2 := bls.GenerateKeyPair()
	popSigNew1 := cross_chain.PopSign(kpNew1.PrivateKey(), kpNew1.PublicKey())
	popSigNew2 := cross_chain.PopSign(kpNew2.PrivateKey(), kpNew2.PublicKey())

	newCommittee := []cross_chain.ValidatorEntry{
		{PubkeyBLS: kpNew1.BytesPublicKey(), Stake: 5000, PopSignature: popSigNew1.Bytes()},
		{PubkeyBLS: kpNew2.BytesPublicKey(), Stake: 5000, PopSignature: popSigNew2.Bytes()},
	}

	payloadObj := cross_chain.UpdateCommitteePayload{
		ChainID:         101,
		NewEpoch:        2,
		NewCommittee:    newCommittee,
		QuorumThreshold: 6700,
	}
	payload, err := json.Marshal(payloadObj)
	require.NoError(t, err)

	// Step 1: Propose UpdateCommittee
	proposeCalldata, err := h.abi.Pack("propose", uint8(cross_chain.ProposalUpdateCommittee), payload, uint64(100))
	require.NoError(t, err)
	proposeFee := big.NewInt(100_000_000_000_000_000) // 0.1 MTN
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, proposeFee, marshalCallData(t, proposeCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 100)
	require.False(t, failed)
	out, err := h.abi.Unpack("propose", rcp.Return())
	require.NoError(t, err)
	proposalID := common.Hash(out[0].([32]byte))

	// Step 2: Vote from 101 and 102
	pub101, sig101 := signGovernanceVote(kp101, proposalID, 101)
	vote1, _ := h.abi.Pack("vote", proposalID, big.NewInt(101), uint64(101), pub101, sig101)
	_, _, failVote1 := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote1)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	require.False(t, failVote1)

	pub102, sig102 := signGovernanceVote(kp102, proposalID, 102)
	vote2, _ := h.abi.Pack("vote", proposalID, big.NewInt(102), uint64(102), pub102, sig102)
	_, _, failVote2 := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote2)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 102)
	require.False(t, failVote2)

	// Step 3: Premature execution before timelock -> REJECTED
	execPrematureCalldata, _ := h.abi.Pack("executeProposal", proposalID, uint64(105))
	_, _, failedPremature := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, execPrematureCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 105)
	assert.True(t, failedPremature, "Premature execution must revert")

	// Step 4: Execute proposal after timelock (t=120)
	execCalldata, _ := h.abi.Pack("executeProposal", proposalID, uint64(120))
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, execCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 120)
	require.False(t, failed)

	// Step 5: Verify updated ChainRegistry state
	engineAfter, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	reg101 := engineAfter.ChainRegistry[101]
	assert.Equal(t, uint64(2), reg101.Epoch)
	assert.Equal(t, uint64(6700), reg101.QuorumThreshold)
	assert.Equal(t, 2, len(reg101.Committee))
	assert.Equal(t, kpNew1.BytesPublicKey(), reg101.Committee[0].PubkeyBLS)
	assert.Equal(t, kpNew2.BytesPublicKey(), reg101.Committee[1].PubkeyBLS)
}

// TestGatewayHandler_Propose_IsPermissionlessAndTracksGrowthViaMetric is the decision test for
// all_remaining_fixes_plan.md's Mục 2 ("propose() không có gate — xác nhận chủ ý hay thiếu
// sót"). Decision: permissionless propose(), gated only at vote()/quorum, is intentional --
// (a) it costs a real, non-refundable 0.1 native token fee per proposal with zero effect
// unless real quorum later votes yes, a genuine economic disincentive against spam; (b) it is
// what lets a brand-new chain self-nominate via ProposalRegisterChain without an existing
// active chain sponsoring it on its behalf, consistent with how RegisterChainViaStake's
// vote-free path already works. This test proves BOTH halves: an address with no relation
// to any registered chain can propose successfully (permissionless holds), and
// Proposals' unbounded growth (no TTL/cleanup exists, and none is added by this decision) is
// made observable via metrics.GovernanceProposalCount instead of guessing at a rate-limit
// design with no real production proposal-volume data behind it.
func TestGatewayHandler_Propose_IsPermissionlessAndTracksGrowthViaMetric(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	// A totally unrelated address -- not a registered chain, not a committee member, not
	// anyone with standing in ChainRegistry/ActiveChains -- exercising the permissionless half
	// of the decision.
	outsider := common.HexToAddress("0x9999888877776666555544443333222211110000")
	proposeFee := big.NewInt(100_000_000_000_000_000)

	pack := func(kind uint8, payload []byte, proposedAt uint64) []byte {
		calldata, err := h.abi.Pack("propose", kind, payload, proposedAt)
		require.NoError(t, err)
		return marshalCallData(t, calldata)
	}

	rcp1, _, failed1 := h.HandleTransaction(context.Background(), cs, newTx(outsider, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, proposeFee, pack(0, []byte("candidate chain A"), 100)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 100)
	require.False(t, failed1, "an outsider address must be able to propose -- permissionless is intentional")
	require.NotNil(t, rcp1)

	rcp2, _, failed2 := h.HandleTransaction(context.Background(), cs, newTx(outsider, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, proposeFee, pack(0, []byte("candidate chain B"), 101)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	require.False(t, failed2)
	require.NotNil(t, rcp2)

	// GovernanceProposalCount is Set() (not Inc()) to the calling engine's own current
	// Proposals map size each time propose() succeeds -- an absolute snapshot of THIS engine,
	// not a running total across every engine/test in the process. This engine started empty
	// and just recorded exactly 2 distinct proposals, so the metric must read exactly 2
	// regardless of what any other test's engine set it to before or after.
	assert.Equal(t, float64(2), testutil.ToFloat64(metrics.GovernanceProposalCount), "GovernanceProposalCount must reflect the real, unbounded growth of Proposals")
}

// TestGatewayHandler_RegisterChainViaStake_TracksChainCountViaMetric is the regression test for
// note/cross_chain_attack_scenario_catalog.md item C6: with BootstrapFoundingChains retired
// (2026-08-28), registerChainViaStake is now gated by a REAL native-coin deposit from the
// caller's own wallet instead (MinNativeStakeToRegister, checked+burned in gateway_handler.go),
// not a vote -- so a metric on ChainRegistry's growth stays just as important: a slowly-
// accumulating colluding coalition would look exactly like unremarkable steady growth without
// one. This proves the metric reflects reality across 4 real, individually-funded
// registerChainViaStake calls, mirroring the propose() test's own style above.
func TestGatewayHandler_RegisterChainViaStake_TracksChainCountViaMetric(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	minStake := big.NewInt(1000)
	engine := cross_chain.NewGatewayEngine(9099, map[uint64]cross_chain.ChainRegistry{}, nil)
	engine.MinNativeStakeToRegister = minStake
	require.NoError(t, saveGatewayEngine(cs, engine))

	callers := []common.Address{
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD01"),
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD02"),
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD03"),
		common.HexToAddress("0xDD00DD00DD00DD00DD00DD00DD00DD00DD00DD04"),
	}
	for i, id := range []uint64{101, 102, 103, 104} {
		caller := callers[i]
		require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))
		calldata, err := h.abi.Pack("registerChainViaStake", makeFoundingChainPayload(t, id))
		require.NoError(t, err)
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.False(t, failed, "registerChainViaStake for chain %d with a real, sufficient deposit must succeed", id)
	}

	assert.Equal(t, float64(4), testutil.ToFloat64(metrics.RegisteredChainCount), "RegisteredChainCount must reflect the real ChainRegistry size after 4 individual registrations")
}

// TestGatewayHandler_RegisterChainViaStake_NoVoteRequired is the end-to-end (real calldata,
// real dispatch) regression test for the vote-free registration path: a candidate must be
// admitted into ChainRegistry via a SINGLE registerChainViaStake transaction backed by a REAL
// native-coin deposit from the caller's own wallet, with no propose()/vote()/executeProposal()
// transaction anywhere in this test -- that absence is the behavior under test, not an
// oversight.
func TestGatewayHandler_RegisterChainViaStake_NoVoteRequired(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	minStake := big.NewInt(1000)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{}, nil)
	engine.MinNativeStakeToRegister = minStake
	require.NoError(t, saveGatewayEngine(cs, engine))

	caller := common.HexToAddress("0xCC00CC00CC00CC00CC00CC00CC00CC00CC00CC00")
	// Fund the caller's REAL wallet -- not any PerChainAllocation/SupplyLedger primitive -- with
	// exactly the required deposit (see gateway_test.go's TestGateway_RegisterChainViaStake for
	// the unit-level proof that RegisterChainViaStake itself performs no stake check at all).
	require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))

	payload := makeFoundingChainPayload(t, 104)
	calldata, err := h.abi.Pack("registerChainViaStake", payload)
	require.NoError(t, err)

	tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "registerChainViaStake with a sufficiently-funded real wallet must succeed with zero votes cast")

	reloaded, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	_, exists := reloaded.ChainRegistry[104]
	assert.True(t, exists, "chain 104 must be registered")
	assert.Contains(t, reloaded.Governance.ActiveChains, uint64(104))
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.RegisteredChainCount))
}

// TestGatewayHandler_SetGenesisDigest_UsesRealCallerNotCalldata is the end-to-end regression test
// proving the "setGenesisDigest" case forces tx.FromAddress() as the caller identity (never
// something read out of calldata) -- the exact property GatewayEngine.SetGenesisDigest's own doc
// comment says closes the front-running race. An attacker crafting a transaction FROM their own
// address can never successfully impersonate the real genesis wallet, no matter what the calldata
// itself claims.
func TestGatewayHandler_SetGenesisDigest_UsesRealCallerNotCalldata(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	genesisWallet := common.HexToAddress("0xfeedfeedfeedfeedfeedfeedfeedfeedfeedfeed")
	attacker := common.HexToAddress("0xbadbadbadbadbadbadbadbadbadbadbadbadbad")

	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		104: {ChainID: 104, Epoch: 1, QuorumThreshold: 6667, GenesisWallet: genesisWallet},
	}, nil)
	require.NoError(t, saveGatewayEngine(cs, engine))

	digest := common.HexToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	calldata, err := h.abi.Pack("setGenesisDigest", new(big.Int).SetUint64(104), digest)
	require.NoError(t, err)

	t.Run("attacker's own transaction cannot publish the digest even though calldata targets chain 104", func(t *testing.T) {
		tx := newTx(attacker, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "setGenesisDigest from a non-genesis-wallet address must fail")

		reloaded, err := loadGatewayEngine(cs)
		require.NoError(t, err)
		assert.Equal(t, common.Hash{}, reloaded.ChainRegistry[104].GenesisDigest, "a rejected attempt must never move the digest")
	})

	t.Run("the real genesis wallet's own transaction succeeds", func(t *testing.T) {
		tx := newTx(genesisWallet, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.False(t, failed, "setGenesisDigest from the real genesis wallet must succeed")

		reloaded, err := loadGatewayEngine(cs)
		require.NoError(t, err)
		assert.Equal(t, digest, reloaded.ChainRegistry[104].GenesisDigest)
	})
}
