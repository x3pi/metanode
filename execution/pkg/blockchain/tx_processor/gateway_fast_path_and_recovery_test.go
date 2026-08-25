package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

func TestGatewayHandler_VerifyAndExecute_Lifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	const sourceChainID = 808
	const epoch = 1

	kp := bls.GenerateKeyPair()
	entry := cross_chain.ValidatorEntry{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 1000}

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF"),
		SourceChainID: sourceChainID,
		DestChainID:   0,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        target,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(0),
		Payload:       []byte{0xDE, 0xAD, 0xBE, 0xEF},
		Tip:           big.NewInt(0),
		Ordered:       false,
	}
	// Real 2-leaf commit tree (message leaf + AggregateValueLeaf, Section 2.3.1) — attestCommit()
	// verifies aggregateProof against commitRoot itself, not a separately-declared StateRoot.
	commitRoot, commitLayers, _, aggIndex, errTree := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, errTree)
	messageProof := cross_chain.GetMerkleProof(commitLayers, 0)
	aggregateProof := cross_chain.GetMerkleProof(commitLayers, aggIndex["0"])

	// Seed engine
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry[sourceChainID] = cross_chain.ChainRegistry{
		ChainID:         sourceChainID,
		Committee:       []cross_chain.ValidatorEntry{entry},
		Epoch:           epoch,
		QuorumThreshold: 6667,
	}
	require.NoError(t, saveGatewayEngine(cs, engine))

	// Sign commitRoot
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	// Call verifyAndExecute(msg..., aggProof, msgProof, commitRoot, cert)
	calldata, err := h.abi.Pack("verifyAndExecute",
		msg.MessageID,
		big.NewInt(int64(msg.SourceChainID)),
		big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)),
		msg.HopCount,
		msg.Sender,
		msg.Target,
		msg.AssetID,
		msg.Value,
		msg.Payload,
		msg.Tip,
		msg.Ordered,
		new(big.Int).SetUint64(aggregateProof.LeafIndex), hashesToBytes32(aggregateProof.Siblings),
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings),
		commitRoot,
		uint64(epoch),
		sig.Bytes(),
		[]byte{0x01},
	)
	require.NoError(t, err)

	tx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed)
	require.NotNil(t, rcp)

	// Verify MessageStatus is Success (1)
	statusCalldata, err := h.abi.Pack("getMessageStatus", msg.MessageID)
	require.NoError(t, err)
	statusRes, err := h.HandleOffChainQuery(cs, newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, statusCalldata)))
	require.NoError(t, err)
	outStatus, err := h.abi.Unpack("getMessageStatus", statusRes)
	require.NoError(t, err)
	assert.Equal(t, uint8(cross_chain.MessageStatusSuccess), outStatus[0].(uint8))

	// Idempotent double execution of same message -> REJECTED
	_, _, failed = h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.True(t, failed, "Second execution of already claimed message must revert")
}

func TestGatewayHandler_ClaimDeadChainBalance_Lifecycle(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	const deadChainID = 404
	const localChainID = 9099

	alice := cross_chain.AccountLeaf{Account: common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), Balance: big.NewInt(1500)}
	bob := cross_chain.AccountLeaf{Account: common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"), Balance: big.NewInt(2500)}
	stateRoot, proofs, err := cross_chain.BuildAccountMerkleTree([]cross_chain.AccountLeaf{alice, bob})
	require.NoError(t, err)

	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10_000_000), map[uint64]*big.Int{
		deadChainID:  big.NewInt(5_000_000),
		localChainID: big.NewInt(5_000_000),
	})
	require.NoError(t, err)

	// Chains 101/102 each need a real committee member to cast a BLS-authenticated governance
	// vote (Milestone G security fix).
	kp101 := bls.GenerateKeyPair()
	kp102 := bls.GenerateKeyPair()

	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.LocalChainID = localChainID
	engine.SupplyLedger = ledger
	engine.ChainRegistry = map[uint64]cross_chain.ChainRegistry{
		deadChainID: {ChainID: deadChainID, StateRoot: common.HexToHash("0x11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"), AccountTreeRoot: stateRoot, Epoch: 1},
		101:         {ChainID: 101, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp101.PublicKey().Bytes(), Stake: 100}}},
		102:         {ChainID: 102, Epoch: 1, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kp102.PublicKey().Bytes(), Stake: 100}}},
	}
	engine.Governance = cross_chain.NewGovernanceEngineWithTimelock([]uint64{101, 102}, 10)
	require.NoError(t, saveGatewayEngine(cs, engine))

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")

	// Negative Test 1: Alice attempts to claim balance before chain is declared dead -> REJECTED
	leafAliceHash := cross_chain.HashAccountLeaf(alice)
	claimAliceCalldata, err := h.abi.Pack("claimDeadChainBalance",
		big.NewInt(deadChainID),
		alice.Account,
		alice.Balance,
		big.NewInt(int64(proofs[0].LeafIndex)),
		proofs[0].Siblings,
		leafAliceHash,
	)
	require.NoError(t, err)

	_, _, failed := h.HandleTransaction(context.Background(), cs, newTx(alice.Account, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimAliceCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 50)
	assert.True(t, failed, "Claiming balance on live chain must revert")

	// Governance: Declare Chain 404 Dead
	deadPayload, _ := json.Marshal(uint64(deadChainID))
	proposeDead, _ := h.abi.Pack("propose", uint8(cross_chain.ProposalDeclareChainDead), deadPayload, uint64(100))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, proposeDead)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 100)
	require.False(t, failed)
	out, err := h.abi.Unpack("propose", rcp.Return())
	require.NoError(t, err)
	propID := common.Hash(out[0].([32]byte))

	// Vote from 101 and 102
	pub101, sig101 := signGovernanceVote(kp101, propID, 101)
	vote1, _ := h.abi.Pack("vote", propID, big.NewInt(101), uint64(101), pub101, sig101)
	_, _, failVote1 := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote1)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 101)
	require.False(t, failVote1)
	pub102, sig102 := signGovernanceVote(kp102, propID, 102)
	vote2, _ := h.abi.Pack("vote", propID, big.NewInt(102), uint64(102), pub102, sig102)
	_, _, failVote2 := h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, vote2)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 102)
	require.False(t, failVote2)

	// Execute proposal at t=120
	execDead, _ := h.abi.Pack("executeProposal", propID, uint64(120))
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, execDead)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 120)
	require.False(t, failed)

	// Alice claims 1500 -> SUCCESS
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(alice.Account, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimAliceCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 121)
	require.False(t, failed, "Alice must successfully claim dead chain balance")

	// Negative Test 2: Alice double-claims -> REJECTED
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(alice.Account, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimAliceCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 122)
	assert.True(t, failed, "Double claiming dead chain balance must revert")

	// Negative Test 3: Tampered balance (15,000 instead of 1500) -> REJECTED
	tamperedAlice := cross_chain.AccountLeaf{Account: alice.Account, Balance: big.NewInt(15_000)}
	tamperedCalldata, _ := h.abi.Pack("claimDeadChainBalance",
		big.NewInt(deadChainID),
		alice.Account,
		tamperedAlice.Balance,
		big.NewInt(int64(proofs[0].LeafIndex)),
		proofs[0].Siblings,
		cross_chain.HashAccountLeaf(tamperedAlice),
	)
	_, _, failed = h.HandleTransaction(context.Background(), cs, newTx(alice.Account, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, tamperedCalldata)), mt_common.GATEWAY_CONTRACT_ADDRESS, false, 123)
	assert.True(t, failed, "Tampered balance claim must revert")

	// Verify Ledger after Alice claim: 5,000,000 - 1500 = 4,998,500
	engineAfter, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(4_998_500), engineAfter.SupplyLedger.GetAllocation(deadChainID))
	assert.True(t, engineAfter.SupplyLedger.VerifyInvariant())
}
