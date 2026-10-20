package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refund() and verifyAndExecute() against a custom asset whose token address has no code: the VM now answers a call to an
// address without code with success (as the EVM does), so the gateway has to refuse to treat that as a token operation
// that happened. refund must be rejected and leave the message pending (measured: without the guard the refund goes
// through); verifyAndExecute must finalize the message as Failed, which keeps the refund path open. On the
// claim/verifyAndExecute path the message ends Failed even without the isContractCall guard (measured), so that test pins
// the outcome rather than the guard. (outbound and claimMessage are covered in gateway_handler_custom_asset_real_test.go.)

type noCodeFixture struct {
	env  barrierEnv
	cs   *blockchain.ChainState
	run  func(data []byte) mt_types.Receipt
	noCd common.Address
}

func newNoCodeFixture(t *testing.T) noCodeFixture {
	t.Helper()
	barrierContractStorage = true
	t.Cleanup(func() { barrierContractStorage = false })
	env, cs := barrierSetup(t)
	seedGatewayLedger(t, env, cs)
	noCode := common.BigToAddress(big.NewInt(0xDEAD0002))
	engine, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	engine.EnsureAssetRegistry()
	// asset 9: wrapped on the local chain by a token address without code; asset 12: home chain is the local chain, same
	engine.AssetRegistry.Assets["9"] = &cross_chain.AssetEntry{AssetID: big.NewInt(9), Active: true, HomeChainID: 103,
		CanonicalContract: common.BigToAddress(big.NewInt(0xCA90)), WrappedContracts: map[uint64]common.Address{reserveChainID: noCode}}
	engine.AssetRegistry.Assets["12"] = &cross_chain.AssetEntry{AssetID: big.NewInt(12), Active: true, HomeChainID: reserveChainID,
		CanonicalContract: noCode, WrappedContracts: map[uint64]common.Address{102: common.BigToAddress(big.NewInt(0xCA91))}}
	msg := noCodeRefundMessage(env)
	root, _, _, _, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, err)
	engine.CommittedBatches = map[common.Hash]cross_chain.CommittedOutboundBatch{root: {Messages: []cross_chain.CrossChainMessage{msg}, Epoch: 1}}
	require.NoError(t, saveGatewayEngine(cs, engine))

	from := env.evm[1].addr
	nonce := uint64(0)
	run := func(data []byte) mt_types.Receipt {
		d, err := transaction.NewCallData(data).Marshal()
		require.NoError(t, err)
		tx := transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 600_000, 1, 1, d, nil, common.Hash{}, common.Hash{}, nonce, 991)
		nonce++
		_, rcps, _, _ := NewTrueBlockSTM([]mt_types.Transaction{tx}).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
		require.Len(t, rcps, 1)
		require.NotNil(t, rcps[0])
		return rcps[0]
	}
	return noCodeFixture{env: env, cs: cs, run: run, noCd: noCode}
}

func noCodeRefundMessage(env barrierEnv) cross_chain.CrossChainMessage {
	return cross_chain.CrossChainMessage{MessageID: common.BigToHash(big.NewInt(0xF12)), SourceChainID: reserveChainID, DestChainID: 102, Sequence: 1, HopCount: 1,
		Sender: env.evm[0].addr, Target: env.users[0], AssetID: big.NewInt(12), Value: big.NewInt(100), Tip: big.NewInt(0), GasFee: big.NewInt(0)}
}

func TestGatewayHandler_CustomAsset_NoCodeToken_RefundRejected(t *testing.T) {
	f := newNoCodeFixture(t)
	gh, err := GetGatewayHandler()
	require.NoError(t, err)
	msg := noCodeRefundMessage(f.env)
	root, layers, _, _, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, err)
	mp := cross_chain.GetMerkleProof(layers, 0)
	sig := bls.Sign(f.env.nodeKP.PrivateKey(), cross_chain.ComputeMessageFailureAttestMessage(msg.MessageID, 102)).Bytes()
	in, err := gh.abi.Pack("refund", msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(102), big.NewInt(1), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered, new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root, uint64(1), sig, []byte{1})
	require.NoError(t, err)

	rcp := f.run(in)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status(), "a refund of tokens that cannot be moved must be rejected")
	assert.Contains(t, string(rcp.Return()), "has no code deployed")

	engine, err := loadGatewayEngine(f.cs)
	require.NoError(t, err)
	assert.Equal(t, cross_chain.MessageStatusPending, engine.GetMessageStatus(msg.MessageID), "a rejected refund must leave the message pending, not Refunded")
}

func TestGatewayHandler_CustomAsset_NoCodeToken_VerifyAndExecuteFinalizesFailed(t *testing.T) {
	f := newNoCodeFixture(t)
	gh, err := GetGatewayHandler()
	require.NoError(t, err)
	from := f.env.evm[1].addr
	msg := cross_chain.CrossChainMessage{MessageID: common.BigToHash(big.NewInt(0xA9)), SourceChainID: 103, DestChainID: reserveChainID, Sequence: 1, HopCount: 1, Sender: from,
		Target: f.noCd, AssetID: big.NewInt(9), Value: big.NewInt(250), Payload: f.env.users[0].Bytes(), Tip: big.NewInt(0), GasFee: big.NewInt(0)}
	root, layers, _, aggIdx, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, err)
	mp, ap := cross_chain.GetMerkleProof(layers, 0), cross_chain.GetMerkleProof(layers, aggIdx["9"])
	sig := bls.Sign(f.env.kp103.PrivateKey(), cross_chain.ComputeCommitRootAttestMessage(root)).Bytes()
	in, err := gh.abi.Pack("verifyAndExecute", msg.MessageID, big.NewInt(103), big.NewInt(int64(reserveChainID)), big.NewInt(1), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered, new(big.Int).SetUint64(ap.LeafIndex), hashesToBytes32(ap.Siblings),
		new(big.Int).SetUint64(mp.LeafIndex), hashesToBytes32(mp.Siblings), root, uint64(1), sig, []byte{1})
	require.NoError(t, err)

	rcp := f.run(in)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status(), "the tx itself does not revert: the message is finalized as Failed")
	engine, err := loadGatewayEngine(f.cs)
	require.NoError(t, err)
	assert.Equal(t, cross_chain.MessageStatusFailed, engine.GetMessageStatus(msg.MessageID),
		"minting into a token without code must finalize the message as Failed (refundable), not Success")
}
