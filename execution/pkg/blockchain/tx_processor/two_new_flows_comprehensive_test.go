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

// =========================================================================================
// FLOW 1: RegisterChainViaStake (Vote-Free, Stake-Gated Registration)
// =========================================================================================

func TestComprehensive_RegisterChainViaStake_FullLifecycle(t *testing.T) {
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	minStake := big.NewInt(10_000)

	t.Run("1.1 Fail-closed when MinNativeStakeToRegister is unconfigured (0)", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		engine := cross_chain.NewGatewayEngine(100, nil, nil)
		// engine.MinNativeStakeToRegister left nil.
		require.NoError(t, saveGatewayEngine(cs, engine))

		candidateKP := bls.GenerateKeyPair()
		pop := cross_chain.PopSign(candidateKP.PrivateKey(), candidateKP.PublicKey())
		candidateReg := cross_chain.ChainRegistry{
			ChainID: 201,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: pop.Bytes()},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		}

		payload, err := json.Marshal(candidateReg)
		require.NoError(t, err)

		calldata, err := h.abi.Pack("registerChainViaStake", payload, minStake)
		require.NoError(t, err)

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
		// Even a well-funded real wallet must not help -- unconfigured fails closed regardless.
		require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, big.NewInt(1_000_000)))
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "registerChainViaStake must fail when MinNativeStakeToRegister is 0")
	})

	t.Run("1.2 Fail-closed when caller's real wallet balance is insufficient (< MinNativeStakeToRegister)", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		engine := cross_chain.NewGatewayEngine(100, nil, nil)
		engine.MinNativeStakeToRegister = minStake
		require.NoError(t, saveGatewayEngine(cs, engine))

		candidateKP := bls.GenerateKeyPair()
		pop := cross_chain.PopSign(candidateKP.PrivateKey(), candidateKP.PublicKey())
		candidateReg := cross_chain.ChainRegistry{
			ChainID: 202,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: pop.Bytes()},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		}

		payload, err := json.Marshal(candidateReg)
		require.NoError(t, err)

		calldata, err := h.abi.Pack("registerChainViaStake", payload, minStake)
		require.NoError(t, err)

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
		// Caller only holds 5,000 real balance (< 10,000 minStake).
		require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, big.NewInt(5_000)))
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "registerChainViaStake must fail when caller's real balance is insufficient")

		as, err := cs.GetAccountStateDB().AccountState(caller)
		require.NoError(t, err)
		assert.Equal(t, 0, as.Balance().Cmp(big.NewInt(5_000)), "balance must remain untouched on failure")
	})

	t.Run("1.3 Success: candidate registered with ZERO votes cast, real deposit locked", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		engine := cross_chain.NewGatewayEngine(100, nil, nil)
		engine.MinNativeStakeToRegister = minStake
		require.NoError(t, saveGatewayEngine(cs, engine))

		candidateKP := bls.GenerateKeyPair()
		pop := cross_chain.PopSign(candidateKP.PrivateKey(), candidateKP.PublicKey())
		candidateReg := cross_chain.ChainRegistry{
			ChainID: 203,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: pop.Bytes()},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		}

		payload, err := json.Marshal(candidateReg)
		require.NoError(t, err)

		calldata, err := h.abi.Pack("registerChainViaStake", payload, minStake)
		require.NoError(t, err)

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
		// Caller holds exactly minStake as REAL native-coin balance.
		require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.False(t, failed, "registerChainViaStake with sufficient real stake must succeed: %+v", rcp)

		as, err := cs.GetAccountStateDB().AccountState(caller)
		require.NoError(t, err)
		assert.Equal(t, 0, as.Balance().Sign(), "caller's real balance must be fully debited (deposit locked)")

		gatewayAs, err := cs.GetAccountStateDB().AccountState(mt_common.GATEWAY_CONTRACT_ADDRESS)
		require.NoError(t, err)
		assert.Equal(t, 0, gatewayAs.Balance().Cmp(minStake), "the deposit must be locked into GATEWAY_CONTRACT_ADDRESS")

		// Verify state persistence
		reloaded, err := loadGatewayEngine(cs)
		require.NoError(t, err)
		reg, exists := reloaded.ChainRegistry[203]
		require.True(t, exists, "chain 203 must be present in ChainRegistry")
		assert.Equal(t, uint64(203), reg.ChainID)
		assert.Equal(t, uint64(6667), reg.QuorumThreshold)
	})

	t.Run("1.4 Rejection of already-registered chain ID", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		candidateKP := bls.GenerateKeyPair()
		pop := cross_chain.PopSign(candidateKP.PrivateKey(), candidateKP.PublicKey())

		engine := cross_chain.NewGatewayEngine(100, map[uint64]cross_chain.ChainRegistry{
			204: {ChainID: 204, Epoch: 1, QuorumThreshold: 6667},
		}, nil)
		engine.MinNativeStakeToRegister = minStake
		require.NoError(t, saveGatewayEngine(cs, engine))

		candidateReg := cross_chain.ChainRegistry{
			ChainID: 204,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: pop.Bytes()},
			},
			Epoch:           2,
			QuorumThreshold: 6667,
		}

		payload, err := json.Marshal(candidateReg)
		require.NoError(t, err)

		calldata, err := h.abi.Pack("registerChainViaStake", payload, minStake)
		require.NoError(t, err)

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
		require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "re-registering an existing chain ID must fail")
	})

	t.Run("1.5 Rejection when BLS Proof of Possession is invalid", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		engine := cross_chain.NewGatewayEngine(100, nil, nil)
		engine.MinNativeStakeToRegister = minStake
		require.NoError(t, saveGatewayEngine(cs, engine))

		candidateKP := bls.GenerateKeyPair()
		wrongKP := bls.GenerateKeyPair()
		badPop := cross_chain.PopSign(wrongKP.PrivateKey(), candidateKP.PublicKey())

		candidateReg := cross_chain.ChainRegistry{
			ChainID: 205,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: badPop.Bytes()},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		}

		payload, err := json.Marshal(candidateReg)
		require.NoError(t, err)

		calldata, err := h.abi.Pack("registerChainViaStake", payload, minStake)
		require.NoError(t, err)

		caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
		require.NoError(t, cs.GetAccountStateDB().AddBalance(caller, minStake))
		tx := newTx(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "registerChainViaStake with bad PoP must fail")
	})
}

// =========================================================================================
// FLOW 2: 2-Hop Routing (A -> Reserve -> B) Native Value & Contract Call
// =========================================================================================

func TestComprehensive_TwoHopValueTransfer_NativeCoins_A_Reserve_B(t *testing.T) {
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	chainAID := uint64(101)
	reserveChainID := uint64(102)
	chainBID := uint64(103)

	kpA := bls.GenerateKeyPair()
	kpReserve := bls.GenerateKeyPair()

	senderOnA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipientOnB := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	transferValue := big.NewInt(2500)

	// Step 1: Set up Reserve chain state (Chain 102)
	csReserve, _, _, _ := newPersistentTestChainState(t)
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
		chainAID:       big.NewInt(50_000),
		reserveChainID: big.NewInt(50_000),
	})
	require.NoError(t, err)

	engineReserve := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
		chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		chainBID: {ChainID: chainBID, Epoch: 0, QuorumThreshold: 6667},
	}, ledger)
	engineReserve.ReserveChainID = reserveChainID
	require.NoError(t, saveGatewayEngine(csReserve, engineReserve))

	// Step 2: Set up Chain B state (Chain 103)
	csB, _, _, _ := newPersistentTestChainState(t)
	engineB := cross_chain.NewGatewayEngine(chainBID, map[uint64]cross_chain.ChainRegistry{
		reserveChainID: {ChainID: reserveChainID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpReserve.BytesPublicKey(), Stake: 1000}}, Epoch: 0, QuorumThreshold: 6667},
	}, nil)
	engineB.ReserveChainID = reserveChainID
	require.NoError(t, saveGatewayEngine(csB, engineB))

	// Step 3: Chain A creates Leg 1 message (DestChainID = Reserve 102, Relay Marker = Chain 103)
	leg1Msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x1111AAAA1111AAAA1111AAAA1111AAAA1111AAAA1111AAAA1111AAAA1111AAAA"),
		SourceChainID: chainAID,
		DestChainID:   reserveChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        senderOnA,
		Target:        recipientOnB,
		AssetID:       big.NewInt(0),
		Value:         transferValue,
		Payload:       cross_chain.EncodeRelayPayload(chainBID, nil), // 2-hop marker
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
		Ordered:       false,
	}

	// Step 4: Attest Leg 1 on Reserve (signed with kpA)
	commitRoot1, messageProof1 := setupAndAttestRelayTestCommit(t, csReserve, h, leg1Msg, kpA)

	// Step 5: Claim Leg 1 on Reserve
	claimCalldata1, err := h.abi.Pack("claimMessage",
		leg1Msg.MessageID, big.NewInt(int64(leg1Msg.SourceChainID)), big.NewInt(int64(leg1Msg.DestChainID)),
		big.NewInt(int64(leg1Msg.Sequence)), leg1Msg.HopCount, leg1Msg.Sender, leg1Msg.Target,
		leg1Msg.AssetID, leg1Msg.Value, leg1Msg.Payload, leg1Msg.Tip, leg1Msg.GasFee, leg1Msg.Ordered,
		new(big.Int).SetUint64(messageProof1.LeafIndex), hashesToBytes32(messageProof1.Siblings), commitRoot1,
	)
	require.NoError(t, err)

	claimTx1 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata1))
	rcp1, _, failed1 := h.HandleTransaction(context.Background(), csReserve, claimTx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed1, "leg 1 claim on Reserve must succeed: %+v", rcp1)

	// Invariant A: recipientOnB must NOT receive direct balance credit on Reserve
	acctReserve, err := csReserve.GetAccountStateDB().AccountState(recipientOnB)
	if err == nil && acctReserve != nil {
		assert.Equal(t, 0, acctReserve.Balance().Sign(), "recipient must NOT be credited on intermediate Reserve node")
	}

	// Invariant B: Reserve must have queued a new Leg 2 message for Chain B
	reloadedReserve, err := loadGatewayEngine(csReserve)
	require.NoError(t, err)
	pending := reloadedReserve.PendingOutboundMessages[chainBID]
	require.Len(t, pending, 1, "Reserve must have queued exactly 1 message for Chain 103")

	leg2Msg := pending[0]
	assert.Equal(t, reserveChainID, leg2Msg.SourceChainID, "leg 2 source must be Reserve")
	assert.Equal(t, chainBID, leg2Msg.DestChainID, "leg 2 dest must be Chain B")
	assert.Equal(t, recipientOnB, leg2Msg.Target)
	assert.Equal(t, 0, leg2Msg.Value.Cmp(transferValue))
	assert.Equal(t, uint8(2), leg2Msg.HopCount)
	assert.Empty(t, leg2Msg.Payload, "inner payload was nil")

	// Step 6: Build Commit tree for Leg 2, sign with kpReserve
	commitRoot2, layers2, aggAmounts2, aggIndex2, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{leg2Msg})
	require.NoError(t, err)
	messageProof2 := cross_chain.GetMerkleProof(layers2, 0)
	aggregateProof2 := cross_chain.GetMerkleProof(layers2, aggIndex2["0"])
	commitMsg2 := cross_chain.ComputeCommitRootAttestMessage(commitRoot2)
	sig2 := bls.Sign(kpReserve.PrivateKey(), commitMsg2)

	// Step 7: Attest Leg 2 on Chain B using attestReserveIssuedCommit (exempt from ceiling check)
	attestCalldata2, err := h.abi.Pack("attestReserveIssuedCommit",
		big.NewInt(int64(reserveChainID)), commitRoot2, aggAmounts2["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof2.LeafIndex), hashesToBytes32(aggregateProof2.Siblings),
		uint64(0), sig2.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)

	attestTx2 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata2))
	_, _, attestFailed2 := h.HandleTransaction(context.Background(), csB, attestTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, attestFailed2, "leg 2 attestReserveIssuedCommit on Chain B must succeed")

	// Step 8: Claim Leg 2 on Chain B
	claimCalldata2, err := h.abi.Pack("claimMessage",
		leg2Msg.MessageID, big.NewInt(int64(leg2Msg.SourceChainID)), big.NewInt(int64(leg2Msg.DestChainID)),
		big.NewInt(int64(leg2Msg.Sequence)), leg2Msg.HopCount, leg2Msg.Sender, leg2Msg.Target,
		leg2Msg.AssetID, leg2Msg.Value, leg2Msg.Payload, leg2Msg.Tip, leg2Msg.GasFee, leg2Msg.Ordered,
		new(big.Int).SetUint64(messageProof2.LeafIndex), hashesToBytes32(messageProof2.Siblings), commitRoot2,
	)
	require.NoError(t, err)

	claimTx2 := newHighGasTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata2))
	rcp2, _, failed2 := h.HandleTransaction(context.Background(), csB, claimTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed2, "leg 2 claim on Chain B must succeed: %+v", rcp2)

	// Step 9: Final Assertion: recipientOnB received the exact transfer value on Chain B!
	acctB, err := csB.GetAccountStateDB().AccountState(recipientOnB)
	require.NoError(t, err)
	require.NotNil(t, acctB)
	assert.Equal(t, 0, acctB.Balance().Cmp(transferValue), "recipient on Chain B must have received exactly %s wei, got %s", transferValue, acctB.Balance())
}

func TestComprehensive_TwoHopContractCall_RealERC20_A_Reserve_B(t *testing.T) {
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	chainAID := uint64(101)
	reserveChainID := uint64(102)
	chainBID := uint64(103)

	kpA := bls.GenerateKeyPair()
	kpReserve := bls.GenerateKeyPair()

	deployer := common.HexToAddress("0x4444444444444444444444444444444444444444")
	senderOnA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipientOnB := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	gasFeeBudget := big.NewInt(600_000 * mt_common.MINIMUM_BASE_FEE)
	mintAmount := big.NewInt(777)

	// Step 1: Deploy real token contract on Chain B (103)
	csB, _, _, _ := newPersistentTestChainState(t)
	targetContractOnB := deployTestWrappedAsset(t, csB, deployer, big.NewInt(0))
	engineB := cross_chain.NewGatewayEngine(chainBID, map[uint64]cross_chain.ChainRegistry{
		reserveChainID: {ChainID: reserveChainID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpReserve.BytesPublicKey(), Stake: 1000}}, Epoch: 0, QuorumThreshold: 6667},
	}, nil)
	engineB.ReserveChainID = reserveChainID
	require.NoError(t, saveGatewayEngine(csB, engineB))

	// Step 2: Set up Reserve chain state (102)
	csReserve, _, _, _ := newPersistentTestChainState(t)
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
		chainAID:       big.NewInt(50_000),
		reserveChainID: big.NewInt(50_000),
	})
	require.NoError(t, err)

	engineReserve := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
		chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		chainBID: {ChainID: chainBID, Epoch: 0, QuorumThreshold: 6667},
	}, ledger)
	engineReserve.ReserveChainID = reserveChainID
	require.NoError(t, saveGatewayEngine(csReserve, engineReserve))

	// Step 3: Pack real ABI calldata for mint(recipientOnB, 777)
	parsedABI := testWrappedAssetABI(t)
	mintCalldata, err := parsedABI.Pack("mint", recipientOnB, mintAmount)
	require.NoError(t, err)

	// Step 4: Construct Leg 1 message
	leg1Msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x2222BBBB2222BBBB2222BBBB2222BBBB2222BBBB2222BBBB2222BBBB2222BBBB"),
		SourceChainID: chainAID,
		DestChainID:   reserveChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        senderOnA,
		Target:        targetContractOnB, // Target is the real contract on Chain B
		AssetID:       big.NewInt(0),
		Value:         big.NewInt(0),
		Payload:       cross_chain.EncodeRelayPayload(chainBID, mintCalldata),
		Tip:           big.NewInt(0),
		GasFee:        gasFeeBudget,
		Ordered:       false,
	}

	// Step 5: Attest + Claim Leg 1 on Reserve
	commitRoot1, messageProof1 := setupAndAttestRelayTestCommit(t, csReserve, h, leg1Msg, kpA)
	claimCalldata1, err := h.abi.Pack("claimMessage",
		leg1Msg.MessageID, big.NewInt(int64(leg1Msg.SourceChainID)), big.NewInt(int64(leg1Msg.DestChainID)),
		big.NewInt(int64(leg1Msg.Sequence)), leg1Msg.HopCount, leg1Msg.Sender, leg1Msg.Target,
		leg1Msg.AssetID, leg1Msg.Value, leg1Msg.Payload, leg1Msg.Tip, leg1Msg.GasFee, leg1Msg.Ordered,
		new(big.Int).SetUint64(messageProof1.LeafIndex), hashesToBytes32(messageProof1.Siblings), commitRoot1,
	)
	require.NoError(t, err)

	claimTx1 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata1))
	rcp1, _, failed1 := h.HandleTransaction(context.Background(), csReserve, claimTx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed1, "leg 1 claim on Reserve must succeed: %+v", rcp1)

	// Step 6: Verify Leg 2 message queued on Reserve has real calldata and intact gasFee
	reloadedReserve, err := loadGatewayEngine(csReserve)
	require.NoError(t, err)
	pending := reloadedReserve.PendingOutboundMessages[chainBID]
	require.Len(t, pending, 1)

	leg2Msg := pending[0]
	assert.Equal(t, mintCalldata, leg2Msg.Payload, "inner calldata must be forwarded unchanged")
	assert.Equal(t, 0, leg2Msg.GasFee.Cmp(gasFeeBudget), "gasFee budget must be preserved for settlement on Chain B")
	assert.Equal(t, targetContractOnB, leg2Msg.Target)

	// Step 7: Build Commit on Reserve, sign with kpReserve
	commitRoot2, layers2, aggAmounts2, aggIndex2, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{leg2Msg})
	require.NoError(t, err)
	messageProof2 := cross_chain.GetMerkleProof(layers2, 0)
	aggregateProof2 := cross_chain.GetMerkleProof(layers2, aggIndex2["0"])
	commitMsg2 := cross_chain.ComputeCommitRootAttestMessage(commitRoot2)
	sig2 := bls.Sign(kpReserve.PrivateKey(), commitMsg2)

	// Step 8: Attest Leg 2 on Chain B (attestReserveIssuedCommit)
	attestCalldata2, err := h.abi.Pack("attestReserveIssuedCommit",
		big.NewInt(int64(reserveChainID)), commitRoot2, aggAmounts2["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof2.LeafIndex), hashesToBytes32(aggregateProof2.Siblings),
		uint64(0), sig2.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)

	attestTx2 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata2))
	_, _, attestFailed2 := h.HandleTransaction(context.Background(), csB, attestTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, attestFailed2, "leg 2 attest on Chain B must succeed")

	// Step 9: Claim Leg 2 on Chain B (executes the contract call!)
	claimCalldata2, err := h.abi.Pack("claimMessage",
		leg2Msg.MessageID, big.NewInt(int64(leg2Msg.SourceChainID)), big.NewInt(int64(leg2Msg.DestChainID)),
		big.NewInt(int64(leg2Msg.Sequence)), leg2Msg.HopCount, leg2Msg.Sender, leg2Msg.Target,
		leg2Msg.AssetID, leg2Msg.Value, leg2Msg.Payload, leg2Msg.Tip, leg2Msg.GasFee, leg2Msg.Ordered,
		new(big.Int).SetUint64(messageProof2.LeafIndex), hashesToBytes32(messageProof2.Siblings), commitRoot2,
	)
	require.NoError(t, err)

	claimTx2 := newHighGasTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata2))
	rcp2, _, failed2 := h.HandleTransaction(context.Background(), csB, claimTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed2, "leg 2 contract call claim on Chain B must succeed: %+v", rcp2)

	// Step 10: Verify the smart contract state changed on Chain B!
	tokenBal := realTokenBalanceOf(t, csB, targetContractOnB, recipientOnB)
	assert.Equal(t, 0, tokenBal.Cmp(mintAmount), "contract call must have minted %s tokens on Chain B, got %s", mintAmount, tokenBal)

	// Step 11: Verify unused gasFee was refunded to sender on Chain B
	senderAcctB, err := csB.GetAccountStateDB().AccountState(senderOnA)
	require.NoError(t, err)
	require.NotNil(t, senderAcctB)
	assert.True(t, senderAcctB.Balance().Sign() > 0, "unused gas fee must be refunded to sender on Chain B")
}

// TestComprehensive_TwoHopContractCall_LegTwoFailsAndRefundsOnReserve is the regression test for
// the "MessageID preserved across relay hops" fix (2026-09-05,
// note/cross_chain/security_audit_findings.md finding #7): before this fix, the relay-onward step
// inside claimMessage (mechanism 2 -- the EncodeRelayPayload/DecodeRelayPayload 2-hop routing this
// whole file exercises) minted a BRAND NEW MessageID (tx.Hash() of the leg-1 claimMessage
// transaction) for the leg-2 message it queues from Reserve to B, instead of preserving leg 1's
// own MessageID. This test drives leg 2 all the way to a genuine payload revert on B (a real
// ERC-20-shaped transfer() call from a zero-balance sender) and refunds it on Reserve, then
// asserts Reserve's OWN bookkeeping for the ORIGINAL leg-1 MessageID (the only ID the sender on
// chain A ever actually saw) now reflects the refund -- proving the whole 2-hop journey stays
// traceable end-to-end under one ID, not silently forked into an unlinkable second one the moment
// it relays through Reserve.
func TestComprehensive_TwoHopContractCall_LegTwoFailsAndRefundsOnReserve(t *testing.T) {
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	chainAID := uint64(101)
	reserveChainID := uint64(102)
	chainBID := uint64(103)

	kpA := bls.GenerateKeyPair()
	kpReserve := bls.GenerateKeyPair()
	kpB := bls.GenerateKeyPair()

	deployer := common.HexToAddress("0x4444444444444444444444444444444444444444")
	senderOnA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipientOnB := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	transferValue := big.NewInt(500)
	gasFeeBudget := big.NewInt(200_000 * mt_common.MINIMUM_BASE_FEE)

	// Step 1: deploy a real token contract on Chain B with ZERO initial supply -- any transfer()
	// out of senderOnA's (empty) balance is a genuine, real business-logic revert.
	csB, _, _, _ := newPersistentTestChainState(t)
	targetContractOnB := deployTestWrappedAsset(t, csB, deployer, big.NewInt(0))
	engineB := cross_chain.NewGatewayEngine(chainBID, map[uint64]cross_chain.ChainRegistry{
		reserveChainID: {ChainID: reserveChainID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpReserve.BytesPublicKey(), Stake: 1000}}, Epoch: 0, QuorumThreshold: 6667},
	}, nil)
	engineB.ReserveChainID = reserveChainID
	require.NoError(t, saveGatewayEngine(csB, engineB))

	// Step 2: Reserve chain state -- ChainRegistry[chainBID] needs a REAL committee (kpB) this
	// time (unlike the other tests in this file, which never need to verify a cert FROM B), since
	// Refund() below verifies B's own failure cert against Reserve's copy of B's registry.
	csReserve, _, _, _ := newPersistentTestChainState(t)
	ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
		chainAID:       big.NewInt(50_000),
		reserveChainID: big.NewInt(50_000),
	})
	require.NoError(t, err)
	engineReserve := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
		chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		chainBID: {ChainID: chainBID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpB.BytesPublicKey(), Stake: 1000}}, Epoch: 0, QuorumThreshold: 6667},
	}, ledger)
	engineReserve.ReserveChainID = reserveChainID
	require.NoError(t, saveGatewayEngine(csReserve, engineReserve))

	// Step 3: leg 1 -- Value=500 AND a real ERC-20 transfer() call that WILL revert (senderOnA has
	// 0 tokens on the freshly-deployed contract).
	parsedABI := testWrappedAssetABI(t)
	revertingCalldata, err := parsedABI.Pack("transfer", recipientOnB, big.NewInt(1))
	require.NoError(t, err)

	leg1Msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x7777CCCC7777CCCC7777CCCC7777CCCC7777CCCC7777CCCC7777CCCC7777CCCC"),
		SourceChainID: chainAID,
		DestChainID:   reserveChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        senderOnA,
		Target:        targetContractOnB,
		AssetID:       big.NewInt(0),
		Value:         transferValue,
		Payload:       cross_chain.EncodeRelayPayload(chainBID, revertingCalldata),
		Tip:           big.NewInt(0),
		GasFee:        gasFeeBudget,
		Ordered:       false,
	}

	commitRoot1, messageProof1 := setupAndAttestRelayTestCommit(t, csReserve, h, leg1Msg, kpA)
	claimCalldata1, err := h.abi.Pack("claimMessage",
		leg1Msg.MessageID, big.NewInt(int64(leg1Msg.SourceChainID)), big.NewInt(int64(leg1Msg.DestChainID)),
		big.NewInt(int64(leg1Msg.Sequence)), leg1Msg.HopCount, leg1Msg.Sender, leg1Msg.Target,
		leg1Msg.AssetID, leg1Msg.Value, leg1Msg.Payload, leg1Msg.Tip, leg1Msg.GasFee, leg1Msg.Ordered,
		new(big.Int).SetUint64(messageProof1.LeafIndex), hashesToBytes32(messageProof1.Siblings), commitRoot1,
	)
	require.NoError(t, err)
	claimTx1 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata1))
	rcp1, _, failed1 := h.HandleTransaction(context.Background(), csReserve, claimTx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed1, "leg 1 claim on Reserve must succeed: %+v", rcp1)

	reloadedReserve, err := loadGatewayEngine(csReserve)
	require.NoError(t, err)
	pending := reloadedReserve.PendingOutboundMessages[chainBID]
	require.Len(t, pending, 1)
	leg2Msg := pending[0]

	// The defining assertion for the fix itself: leg 2 must carry leg 1's OWN MessageID forward,
	// not a fresh one derived from the leg-1 claim transaction's own hash.
	assert.Equal(t, leg1Msg.MessageID, leg2Msg.MessageID, "leg 2 must preserve leg 1's original MessageID across the relay hop")

	// Step 4: real batchOutboundCommit() on Reserve -- this is what populates CommittedBatches,
	// which Refund() (called on Reserve below) needs for its own commit-attestation check.
	batchCalldata, err := h.abi.Pack("batchOutboundCommit", big.NewInt(int64(chainBID)))
	require.NoError(t, err)
	batchTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, batchCalldata))
	rcpBatch, _, failedBatch := h.HandleTransaction(context.Background(), csReserve, batchTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failedBatch, "batchOutboundCommit on Reserve must succeed: %+v", rcpBatch)
	batchOut, err := h.abi.Unpack("batchOutboundCommit", rcpBatch.Return())
	require.NoError(t, err)
	commitRoot2 := common.Hash(batchOut[0].([32]byte))

	_, layers2, aggAmounts2, aggIndex2, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{leg2Msg})
	require.NoError(t, err)
	messageProof2 := cross_chain.GetMerkleProof(layers2, 0)
	aggregateProof2 := cross_chain.GetMerkleProof(layers2, aggIndex2["0"])
	commitMsg2 := cross_chain.ComputeCommitRootAttestMessage(commitRoot2)
	sig2 := bls.Sign(kpReserve.PrivateKey(), commitMsg2)

	attestCalldata2, err := h.abi.Pack("attestReserveIssuedCommit",
		big.NewInt(int64(reserveChainID)), commitRoot2, aggAmounts2["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggregateProof2.LeafIndex), hashesToBytes32(aggregateProof2.Siblings),
		uint64(0), sig2.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)
	attestTx2 := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, attestCalldata2))
	_, _, attestFailed2 := h.HandleTransaction(context.Background(), csB, attestTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, attestFailed2, "leg 2 attestReserveIssuedCommit on Chain B must succeed")

	// Step 5: claim leg 2 on Chain B -- the transfer() call genuinely reverts (0 balance).
	claimCalldata2, err := h.abi.Pack("claimMessage",
		leg2Msg.MessageID, big.NewInt(int64(leg2Msg.SourceChainID)), big.NewInt(int64(leg2Msg.DestChainID)),
		big.NewInt(int64(leg2Msg.Sequence)), leg2Msg.HopCount, leg2Msg.Sender, leg2Msg.Target,
		leg2Msg.AssetID, leg2Msg.Value, leg2Msg.Payload, leg2Msg.Tip, leg2Msg.GasFee, leg2Msg.Ordered,
		new(big.Int).SetUint64(messageProof2.LeafIndex), hashesToBytes32(messageProof2.Siblings), commitRoot2,
	)
	require.NoError(t, err)
	claimTx2 := newHighGasTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata2))
	rcp2, _, failed2 := h.HandleTransaction(context.Background(), csB, claimTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	// mục 2.4 point 1 / finding #1: the TRANSACTION itself succeeds even though the payload
	// reverted -- claimMessage finalizes Failed instead of hard-reverting.
	require.False(t, failed2, "leg 2 claimMessage transaction itself must succeed (finalizes Failed, does not hard-revert): %+v", rcp2)

	statusCalldata, err := h.abi.Pack("getMessageStatus", leg2Msg.MessageID)
	require.NoError(t, err)
	statusResult, err := h.HandleOffChainQuery(csB, newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, statusCalldata)))
	require.NoError(t, err)
	statusOut, err := h.abi.Unpack("getMessageStatus", statusResult)
	require.NoError(t, err)
	require.Equal(t, uint8(cross_chain.MessageStatusFailed), statusOut[0].(uint8), "leg 2 must be finalized Failed on Chain B")

	// Step 6: refund on Reserve, using a real failure cert signed by B's own committee.
	failDigest := cross_chain.ComputeMessageFailureAttestMessage(leg2Msg.MessageID, chainBID)
	failSig := bls.Sign(kpB.PrivateKey(), failDigest)
	refundCalldata, err := h.abi.Pack("refund",
		leg2Msg.MessageID, big.NewInt(int64(leg2Msg.SourceChainID)), big.NewInt(int64(leg2Msg.DestChainID)),
		big.NewInt(int64(leg2Msg.Sequence)), leg2Msg.HopCount, leg2Msg.Sender, leg2Msg.Target,
		leg2Msg.AssetID, leg2Msg.Value, leg2Msg.Payload, leg2Msg.Tip, leg2Msg.GasFee, leg2Msg.Ordered,
		new(big.Int).SetUint64(messageProof2.LeafIndex), hashesToBytes32(messageProof2.Siblings), commitRoot2,
		uint64(0), failSig.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)
	refundTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 2, big.NewInt(0), marshalCallData(t, refundCalldata))
	rcpRefund, _, failedRefund := h.HandleTransaction(context.Background(), csReserve, refundTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failedRefund, "refund() on Reserve must succeed once leg 2's own MessageID is used consistently throughout: %+v", rcpRefund)

	// The defining assertion: Reserve's bookkeeping for leg 1's ORIGINAL MessageID -- the only ID
	// chain A's sender ever actually saw -- now shows Refunded. Before this fix, this would query
	// a message Reserve never resolved (it stayed on the throwaway tx-hash-derived ID instead),
	// permanently reading back Pending.
	finalStatusCalldata, err := h.abi.Pack("getMessageStatus", leg1Msg.MessageID)
	require.NoError(t, err)
	finalStatusResult, err := h.HandleOffChainQuery(csReserve, newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, finalStatusCalldata)))
	require.NoError(t, err)
	finalStatusOut, err := h.abi.Unpack("getMessageStatus", finalStatusResult)
	require.NoError(t, err)
	assert.Equal(t, uint8(cross_chain.MessageStatusRefunded), finalStatusOut[0].(uint8), "Reserve's status for the ORIGINAL leg-1 MessageID must reflect the refund")
}

func TestComprehensive_TwoHop_SecurityGuards_SelfLoopAndUnregisteredTarget(t *testing.T) {
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	chainAID := uint64(101)
	reserveChainID := uint64(102)

	kpA := bls.GenerateKeyPair()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	t.Run("Self-loop relay destination (relaying back to Reserve itself) fails closed", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
			chainAID:       big.NewInt(50_000),
			reserveChainID: big.NewInt(50_000),
		})
		require.NoError(t, err)

		engine := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
			chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		}, ledger)
		engine.ReserveChainID = reserveChainID
		require.NoError(t, saveGatewayEngine(cs, engine))

		msg := cross_chain.CrossChainMessage{
			MessageID:     common.HexToHash("0x9999AAAA9999AAAA9999AAAA9999AAAA9999AAAA9999AAAA9999AAAA9999AAAA"),
			SourceChainID: chainAID,
			DestChainID:   reserveChainID,
			Sequence:      1,
			HopCount:      1,
			Sender:        sender,
			Target:        target,
			AssetID:       big.NewInt(0),
			Value:         big.NewInt(500),
			Payload:       cross_chain.EncodeRelayPayload(reserveChainID, nil), // Target is Reserve itself -> invalid!
			Tip:           big.NewInt(0),
			GasFee:        big.NewInt(0),
			Ordered:       false,
		}
		commitRoot, messageProof := setupAndAttestRelayTestCommit(t, cs, h, msg, kpA)

		claimCalldata, err := h.abi.Pack("claimMessage",
			msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
			big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
			msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
			new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
		)
		require.NoError(t, err)

		claimTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "claimMessage with self-loop relay marker must fail")
	})

	t.Run("Unregistered target chain ID fails closed", func(t *testing.T) {
		cs, _, _, _ := newPersistentTestChainState(t)
		ledger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
			chainAID:       big.NewInt(50_000),
			reserveChainID: big.NewInt(50_000),
		})
		require.NoError(t, err)

		engine := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
			chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		}, ledger)
		engine.ReserveChainID = reserveChainID
		require.NoError(t, saveGatewayEngine(cs, engine))

		msg := cross_chain.CrossChainMessage{
			MessageID:     common.HexToHash("0x8888BBBB8888BBBB8888BBBB8888BBBB8888BBBB8888BBBB8888BBBB8888BBBB"),
			SourceChainID: chainAID,
			DestChainID:   reserveChainID,
			Sequence:      1,
			HopCount:      1,
			Sender:        sender,
			Target:        target,
			AssetID:       big.NewInt(0),
			Value:         big.NewInt(500),
			Payload:       cross_chain.EncodeRelayPayload(99999, nil), // Unknown chain 99999
			Tip:           big.NewInt(0),
			GasFee:        big.NewInt(0),
			Ordered:       false,
		}
		commitRoot, messageProof := setupAndAttestRelayTestCommit(t, cs, h, msg, kpA)

		claimCalldata, err := h.abi.Pack("claimMessage",
			msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
			big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
			msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
			new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
		)
		require.NoError(t, err)

		claimTx := newTx(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
		_, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
		require.True(t, failed, "claimMessage with unknown destination chain ID must fail")
	})
}
