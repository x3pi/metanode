package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// This file closes the Task 1.3 test-coverage gap: every existing claimMessage/verifyAndExecute
// test (TestGatewayHandler_ClaimMessageMintsRealValue, TestGatewayHandler_VerifyAndExecute_Lifecycle)
// sets msg.Target to a plain EOA address with no code, so isContractCall() always returns false
// and executeContractCallForGateway() -- the real mvm.ExecutionEngine.Execute call that forwards
// a cross-chain message's Payload into a destination-chain contract call -- is never actually
// invoked by any existing test. The wiring itself has been live in production since Task 1
// (commit 489421c1), but had zero proof it actually works against a real deployed contract,
// exactly the kind of gap that already hid 2 real bugs in the analogous Task 1.2 custom-asset
// path (see gateway_handler_custom_asset_real_test.go's own comments). Both tests below deploy a
// real contract via the same mvm.ExecutionEngine.Deploy path production code uses, send a
// message whose Payload is a real ABI-encoded call into it, and prove the call actually executed
// by reading the contract's real state back afterwards -- not by trusting claimMessage's/
// verifyAndExecute's own success/MessageStatus bookkeeping.

func TestGatewayHandler_ClaimMessagePayload_ExecutesRealContractCall(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	deployer := common.HexToAddress("0x4444444444444444444444444444444444444444")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	// Real deployed contract used as the cross-chain message's Target -- isContractCall() only
	// returns true against real code, which is the entire point of this test.
	targetContract := deployTestWrappedAsset(t, cs, deployer, big.NewInt(0))

	// Payload is a real ABI-encoded mint(recipient, 42) call -- exactly what a real dApp would
	// put in a cross-chain message's Payload to have the destination chain execute a function
	// call on its behalf.
	parsedABI := testWrappedAssetABI(t)
	mintPayload, err := parsedABI.Pack("mint", recipient, big.NewInt(42))
	require.NoError(t, err)

	kp := bls.GenerateKeyPair()
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(10000), map[uint64]*big.Int{101: big.NewInt(5000), 102: big.NewInt(5000)})
	require.NoError(t, err)
	engine := cross_chain.NewGatewayEngine(102, map[uint64]cross_chain.ChainRegistry{
		101: {
			ChainID: 101,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		},
	}, ledger)
	require.NoError(t, saveGatewayEngine(cs, engine))

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xC0FFEE00000000000000000000000000000000000000000000000000000001"),
		SourceChainID: 101,
		DestChainID:   102,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        targetContract,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(0), // pure message: no native value, payload-only
		Payload:       mintPayload,
		Tip:           big.NewInt(0),
		Ordered:       false,
	}

	commitRoot, layers, aggAmounts, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, err)
	messageProof := cross_chain.GetMerkleProof(layers, 0)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	attestCalldata, err := h.abi.Pack("attestCommit",
		big.NewInt(101), commitRoot, aggAmounts["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof.LeafIndex), hashesToBytes32(aggregateProof.Siblings),
		uint64(1), sig.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)
	attestTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata))
	_, _, failed := h.HandleTransaction(context.Background(), cs, attestTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "attestCommit should succeed")

	recipientBalBefore := realTokenBalanceOf(t, cs, targetContract, recipient)
	require.Zero(t, recipientBalBefore.Sign(), "sanity: recipient should start with 0 real balance")

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.Ordered,
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
	)
	require.NoError(t, err)
	claimTx := newHighGasTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "claimMessage with a real contract-call payload should succeed, got: %+v", rcp)

	// The real, defining assertion: read the real deployed contract's state back via a real EVM
	// call -- proving executeContractCallForGateway() actually executed the payload, not just
	// that claimMessage()'s own bookkeeping (MessageStatus) reported success.
	recipientBalAfter := realTokenBalanceOf(t, cs, targetContract, recipient)
	require.Equal(t, 0, recipientBalAfter.Cmp(big.NewInt(42)),
		"expected real mint(recipient, 42) call from the message Payload to have executed, got balance %s", recipientBalAfter)
}

func TestGatewayHandler_VerifyAndExecutePayload_ExecutesRealContractCall(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	const sourceChainID = 909
	const epoch = 1

	deployer := common.HexToAddress("0x5555555555555555555555555555555555555555")
	sender := common.HexToAddress("0x6666666666666666666666666666666666666666")
	recipient := common.HexToAddress("0x7777777777777777777777777777777777777777")
	relayer := common.HexToAddress("0x8888888888888888888888888888888888888888")

	targetContract := deployTestWrappedAsset(t, cs, deployer, big.NewInt(0))

	parsedABI := testWrappedAssetABI(t)
	mintPayload, err := parsedABI.Pack("mint", recipient, big.NewInt(77))
	require.NoError(t, err)

	kp := bls.GenerateKeyPair()
	entry := cross_chain.ValidatorEntry{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 1000}

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xFEED000000000000000000000000000000000000000000000000000000002"),
		SourceChainID: sourceChainID,
		DestChainID:   0,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        targetContract,
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(0),
		Payload:       mintPayload,
		Tip:           big.NewInt(0),
		Ordered:       false,
	}
	// Real 2-leaf commit tree (message leaf + AggregateValueLeaf) -- same real BLS/Merkle
	// apparatus as TestGatewayHandler_VerifyAndExecute_Lifecycle, just with a real deployed
	// contract as Target instead of a plain EOA.
	commitRoot, commitLayers, _, aggIndex, errTree := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, errTree)
	messageProof := cross_chain.GetMerkleProof(commitLayers, 0)
	aggregateProof := cross_chain.GetMerkleProof(commitLayers, aggIndex["0"])

	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.ChainRegistry[sourceChainID] = cross_chain.ChainRegistry{
		ChainID:         sourceChainID,
		Committee:       []cross_chain.ValidatorEntry{entry},
		Epoch:           epoch,
		QuorumThreshold: 6667,
	}
	require.NoError(t, saveGatewayEngine(cs, engine))

	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	calldata, err := h.abi.Pack("verifyAndExecute",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.Ordered,
		new(big.Int).SetUint64(aggregateProof.LeafIndex), hashesToBytes32(aggregateProof.Siblings),
		new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings),
		commitRoot, uint64(epoch), sig.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)

	recipientBalBefore := realTokenBalanceOf(t, cs, targetContract, recipient)
	require.Zero(t, recipientBalBefore.Sign(), "sanity: recipient should start with 0 real balance")

	tx := newHighGasTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "verifyAndExecute with a real contract-call payload should succeed, got: %+v", rcp)

	// The real, defining assertion: read the real deployed contract's state back via a real EVM
	// call -- proving executeContractCallForGateway() actually executed the payload.
	recipientBalAfter := realTokenBalanceOf(t, cs, targetContract, recipient)
	require.Equal(t, 0, recipientBalAfter.Cmp(big.NewInt(77)),
		"expected real mint(recipient, 77) call from the message Payload to have executed, got balance %s", recipientBalAfter)
}
