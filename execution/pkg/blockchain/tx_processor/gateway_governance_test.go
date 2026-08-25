package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

func TestGatewayHandler_Governance_OnboardNewChainLifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	// Step 1: Seed Root Anchor with 3 active chains: 101, 102, 103 (Quorum: (2*3+2)/3 = 2 votes)
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1},
		102: {ChainID: 102, Epoch: 1},
		103: {ChainID: 103, Epoch: 1},
	}
	engine.Governance = cross_chain.NewGovernanceEngine([]uint64{101, 102, 103})
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")

	// Step 2: Propose onboarding new chain 104
	newChainReg := cross_chain.ChainRegistry{
		ChainID:         104,
		Epoch:           1,
		QuorumThreshold: 6667,
		GatewayContract: common.HexToAddress("0x9999999999999999999999999999999999999999"),
		ArchivalEndpoint: "https://rpc.chain104.io",
	}
	payload, err := json.Marshal(newChainReg)
	require.NoError(t, err)

	const proposedAt = uint64(1000)
	proposeCalldata, err := h.abi.Pack("propose", uint8(cross_chain.ProposalRegisterChain), payload, proposedAt)
	require.NoError(t, err)

	proposeTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, proposeCalldata))
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
	vote1Calldata, err := h.abi.Pack("vote", proposalID, big.NewInt(101), proposedAt+10)
	require.NoError(t, err)
	vote1Tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote1Calldata))
	_, _, failed = h.HandleTransaction(context.Background(), cs, vote1Tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, proposedAt+10)
	require.False(t, failed)

	// Step 4: Vote 2 from Chain 102 (2/2 votes -> reaches quorum -> Timelocked)
	const vote2Time = proposedAt + 20
	vote2Calldata, err := h.abi.Pack("vote", proposalID, big.NewInt(102), vote2Time)
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

	// Seed with chain 101 and 102
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		101: {ChainID: 101, Epoch: 1},
		102: {ChainID: 102, Epoch: 1},
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
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, proposeCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 100)
	require.False(t, failed)
	out, err := h.abi.Unpack("propose", rcp.Return())
	require.NoError(t, err)
	proposalID := common.Hash(out[0].([32]byte))

	// Vote from 101 and 102
	vote1, _ := h.abi.Pack("vote", proposalID, big.NewInt(101), uint64(101))
	_, _, _ = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote1)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)

	vote2, _ := h.abi.Pack("vote", proposalID, big.NewInt(102), uint64(102))
	_, _, _ = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote2)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 102)

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
