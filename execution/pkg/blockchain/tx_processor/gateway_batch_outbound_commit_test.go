package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// TestGatewayHandler_BatchOutboundCommit_EndToEnd closes the real P4 relayer-automation gap:
// CommitAttestationWorker (Milestone F) has always existed to BLS-sign a commit root, but
// nothing in production ever decided "these pending outbound() messages now form a committed
// batch" -- its OnCommitFinalized trigger was never called from anywhere (confirmed by grepping
// the whole module before writing this). This test proves the new batchOutboundCommit() ABI
// method closes that gap for real: 2 real outbound() messages get batched via a real
// transaction, the resulting commitRoot/message list survive a round trip through
// getCommitBatch() (exactly how a real relayer -- or this chain's own restarted worker -- would
// retrieve them), CommitFinalizedCallback fires with the right arguments, and the retrieved
// batch is enough to independently rebuild real Merkle proofs and successfully attestCommit() +
// claimMessage() both messages on a separate destination chain's GatewayHandler instance.
func TestGatewayHandler_BatchOutboundCommit_EndToEnd(t *testing.T) {
	csSource, _, _, _ := newPersistentTestChainState(t)
	csDest, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	const sourceChainID = 401
	const destChainID = 402

	// --- Give the source chain its own real LocalChainID (defaults to 0 otherwise) ---
	// destChainID must be a known registered chain on the SOURCE too -- outbound() now rejects an
	// unregistered destination before it ever locks/burns funds (2026-09-05 security fix, see
	// that case's own comment in gateway_handler.go).
	sourceEngine := cross_chain.NewGatewayEngine(sourceChainID, map[uint64]cross_chain.ChainRegistry{
		destChainID: {
			ChainID: destChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, nil)
	require.NoError(t, saveGatewayEngine(csSource, sourceEngine))

	// --- Set up chain 402's local view of chain 401's committee (needed for attestCommit there) ---
	kp := bls.GenerateKeyPair()
	popSig := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())
	destEngine, err := loadGatewayEngine(csDest)
	require.NoError(t, err)
	destEngine.ChainRegistry[sourceChainID] = cross_chain.ChainRegistry{
		ChainID: sourceChainID,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000, PopSignature: popSig.Bytes()},
		},
		Epoch:           0,
		QuorumThreshold: 6667,
	}
	require.NoError(t, saveGatewayEngine(csDest, destEngine))

	// --- Hook CommitFinalizedCallback to prove it fires with the right arguments ---
	var gotSourceChainID, gotEpoch uint64
	var gotCommitRoot common.Hash
	callbackFired := false
	prevCallback := CommitFinalizedCallback
	CommitFinalizedCallback = func(sourceChainID, epoch uint64, commitRoot common.Hash) {
		callbackFired = true
		gotSourceChainID = sourceChainID
		gotEpoch = epoch
		gotCommitRoot = commitRoot
	}
	defer func() { CommitFinalizedCallback = prevCallback }()

	// --- 2 real outbound() transactions on the source chain ---
	for i, payloadByte := range []byte{0x01, 0x02} {
		calldata, err := h.abi.Pack("outbound",
			big.NewInt(destChainID), target, []byte{payloadByte}, big.NewInt(0), big.NewInt(0),
			big.NewInt(0), big.NewInt(0), uint8(1), false,
		)
		require.NoError(t, err)
		tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, uint64(i), big.NewInt(0), marshalCallData(t, calldata))
		rcp, _, failed := h.HandleTransaction(context.Background(), csSource, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.False(t, failed, "outbound() should succeed, got: %+v", rcp)
	}

	pendingCalldata, err := h.abi.Pack("getPendingOutboundCount", big.NewInt(destChainID))
	require.NoError(t, err)
	pendingResult, err := h.HandleOffChainQuery(csSource, newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, pendingCalldata)))
	require.NoError(t, err)
	pendingOut, err := h.abi.Unpack("getPendingOutboundCount", pendingResult)
	require.NoError(t, err)
	require.Equal(t, 0, pendingOut[0].(*big.Int).Cmp(big.NewInt(2)), "expected 2 real pending outbound messages before batching")

	// --- Real batchOutboundCommit() transaction ---
	batchCalldata, err := h.abi.Pack("batchOutboundCommit", big.NewInt(destChainID))
	require.NoError(t, err)
	batchTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, batchCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), csSource, batchTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed, "batchOutboundCommit() should succeed, got: %+v", rcp)

	batchOut, err := h.abi.Unpack("batchOutboundCommit", rcp.Return())
	require.NoError(t, err)
	commitRoot := common.Hash(batchOut[0].([32]byte))
	require.Equal(t, 0, batchOut[1].(*big.Int).Cmp(big.NewInt(2)))

	require.True(t, callbackFired, "CommitFinalizedCallback must fire when this node processes its own batchOutboundCommit() tx")
	require.Equal(t, uint64(sourceChainID), gotSourceChainID)
	require.Equal(t, uint64(0), gotEpoch)
	require.Equal(t, commitRoot, gotCommitRoot)

	// --- Real getCommitBatch() retrieval, exactly how a relayer (or a restarted worker) would ---
	getBatchCalldata, err := h.abi.Pack("getCommitBatch", commitRoot)
	require.NoError(t, err)
	getBatchResult, err := h.HandleOffChainQuery(csSource, newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, getBatchCalldata)))
	require.NoError(t, err)
	getBatchOut, err := h.abi.Unpack("getCommitBatch", getBatchResult)
	require.NoError(t, err)
	require.True(t, getBatchOut[0].(bool), "commit batch must exist")
	require.Equal(t, uint64(0), getBatchOut[1].(uint64))
	var retrievedMessages []cross_chain.CrossChainMessage
	require.NoError(t, json.Unmarshal(getBatchOut[2].([]byte), &retrievedMessages))
	require.Len(t, retrievedMessages, 2)

	// --- Independently rebuild real Merkle proofs from the retrieved messages, exactly like a
	// real relayer would, and finish the real attestCommit()+claimMessage() round trip on the
	// destination chain. ---
	rebuiltRoot, layers, aggAmounts, aggIndex, err := cross_chain.BuildCommitTree(retrievedMessages)
	require.NoError(t, err)
	require.Equal(t, commitRoot, rebuiltRoot, "relayer-rebuilt tree must match the on-chain commitRoot exactly")

	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	attestCalldata, err := h.abi.Pack("attestCommit",
		big.NewInt(sourceChainID), commitRoot, aggAmounts["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof.LeafIndex), hashesToBytes32(aggregateProof.Siblings),
		uint64(0), sig.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)
	attestTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, attestCalldata))
	rcp2, _, failed2 := h.HandleTransaction(context.Background(), csDest, attestTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed2, "attestCommit() on destination chain should succeed, got: %+v", rcp2)

	for i, msg := range retrievedMessages {
		messageProof := cross_chain.GetMerkleProof(layers, i)
		claimCalldata, err := h.abi.Pack("claimMessage",
			msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
			big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
			msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
			new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
		)
		require.NoError(t, err)
		claimTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, uint64(2+i), big.NewInt(0), marshalCallData(t, claimCalldata))
		rcp3, _, failed3 := h.HandleTransaction(context.Background(), csDest, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.False(t, failed3, "claimMessage() for message %d should succeed, got: %+v", i, rcp3)
	}
}
