package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
)

// MockCrossChainDispatcher implements CrossChainTransferDispatcher for testing.
type MockCrossChainDispatcher struct {
	HandleTransferCalled bool
	LastToKey            mt_common.PublicKey
	LastSender           common.Address
	LastTarget           common.Address
	LastValue            *big.Int
	LastPayloadHash      common.Hash
	MockError            error
	MockHash             common.Hash
}

func (m *MockCrossChainDispatcher) HandleTransfer(
	store rollup.Store,
	stateDB AccountStateAccessor,
	toKey mt_common.PublicKey,
	sender common.Address,
	target common.Address,
	value *big.Int,
	payloadHash common.Hash,
) (common.Hash, error) {
	m.HandleTransferCalled = true
	m.LastToKey = toKey
	m.LastSender = sender
	m.LastTarget = target
	m.LastValue = value
	m.LastPayloadHash = payloadHash
	return m.MockHash, m.MockError
}

func TestParentChainGatewayHandler_HandleTransaction(t *testing.T) {
	// 1. Setup mock dispatcher
	mockDispatcher := &MockCrossChainDispatcher{
		MockHash: common.HexToHash("0x1234"),
	}
	InitParentChainGatewayHandler(mockDispatcher)
	handler := GetParentChainGatewayHandler()
	assert.NotNil(t, handler)

	// 2. Setup mock state using test helper
	chainState, _, _, _ := newPersistentTestChainState(t)
	senderAddr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	stateDB := chainState.GetAccountStateDB()
	stateDB.AddBalance(senderAddr, big.NewInt(1000000000000000000)) // 1 ETH

	// 3. Create mock transaction
	destPubKeyHex := "010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101"
	destPubKeyBytes := common.FromHex(destPubKeyHex)
	targetAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Layout: destPubKey (48 bytes) || payloadHash (32 bytes) || targetAddr (20 bytes), matching
	// what mtn_api.go's SendCrossChainTransfer actually builds.
	rawPayload := make([]byte, 0, 100)
	rawPayload = append(rawPayload, destPubKeyBytes...)
	rawPayload = append(rawPayload, make([]byte, 32)...) // zero payloadHash for this test
	rawPayload = append(rawPayload, targetAddr.Bytes()...)

	// HandleTransaction reads tx.CallData().Input(), not tx.Data() directly — a real tx arriving
	// via SendRawEthTransaction is always wrapped in this CallData protobuf envelope (see
	// rpc_transaction.go), so the test must wrap it the same way or CallData().Input() returns
	// garbage from a failed protobuf-unmarshal of the raw bytes.
	callData := transaction.NewCallData(rawPayload)
	data, err := callData.Marshal()
	assert.NoError(t, err)

	txAmount := big.NewInt(5000)
	tx := transaction.NewTransaction(
		senderAddr,
		mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS,
		txAmount,
		21000,
		1,
		0,
		data,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		1,
	)

	// 4. Test HandleTransaction
	receipt, _, err := handler.HandleTransaction(context.Background(), chainState, tx, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.NotNil(t, receipt)
	
	// Ensure the receipt status is RETURNED (equivalent to success for barrier TX)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
	
	// Verify that the dispatcher was called with correct parameters
	assert.True(t, mockDispatcher.HandleTransferCalled)
	assert.Equal(t, destPubKeyBytes, mockDispatcher.LastToKey[:])
	assert.Equal(t, senderAddr, mockDispatcher.LastSender)
	assert.Equal(t, targetAddr, mockDispatcher.LastTarget)
	assert.Equal(t, txAmount, mockDispatcher.LastValue)

	// Check if gas fee was deducted (transfer fee is handled by dispatcher, gas is handled by handler)
	accountState, _ := stateDB.AccountState(senderAddr)
	newBalance := accountState.Balance()
	gasFee := new(big.Int).Mul(big.NewInt(int64(mt_common.TRANSFER_GAS_COST)), big.NewInt(1))
	expectedBalance := new(big.Int).Sub(big.NewInt(1000000000000000000), gasFee) // since HandleTransfer doesn't deduct from stateDB in the mock
	
	assert.Equal(t, expectedBalance.String(), newBalance.String())
}

func TestParentChainGatewayHandler_ErrorCases(t *testing.T) {
	mockDispatcher := &MockCrossChainDispatcher{
		MockHash: common.HexToHash("0x1234"),
	}
	InitParentChainGatewayHandler(mockDispatcher)
	handler := GetParentChainGatewayHandler()
	assert.NotNil(t, handler)

	chainState, _, _, _ := newPersistentTestChainState(t)
	stateDB := chainState.GetAccountStateDB()
	senderAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	stateDB.AddBalance(senderAddr, big.NewInt(1000)) // Low balance
	stateDB.SetNonce(senderAddr, 5)

	// Case 1: Short calldata (< 100 bytes)
	callDataShort := transaction.NewCallData([]byte("too short"))
	dataShort, err := callDataShort.Marshal()
	assert.NoError(t, err)

	txShort := transaction.NewTransaction(
		senderAddr,
		mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS,
		big.NewInt(500),
		21000,
		1,
		0,
		dataShort,
		nil,
		common.Hash{},
		common.Hash{},
		5,
		1,
	)

	receipt1, _, err := handler.HandleTransaction(context.Background(), chainState, txShort, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.NotNil(t, receipt1)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, receipt1.Status())
	// Nonce must advance to 6
	as1, err := stateDB.AccountState(senderAddr)
	assert.NoError(t, err)
	assert.Equal(t, uint64(6), as1.Nonce())

	// Case 2: Insufficient balance for transfer + gas
	rawPayload := make([]byte, 100)
	copy(rawPayload[0:48], make([]byte, 48))
	copy(rawPayload[80:100], common.HexToAddress("0x4444444444444444444444444444444444444444").Bytes())
	callDataFull := transaction.NewCallData(rawPayload)
	dataFull, err := callDataFull.Marshal()
	assert.NoError(t, err)

	// User has balance 1000, tries to send 1000000
	txExcessive := transaction.NewTransaction(
		senderAddr,
		mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS,
		big.NewInt(1000000),
		21000,
		1,
		0,
		dataFull,
		nil,
		common.Hash{},
		common.Hash{},
		6,
		1,
	)

	receipt2, _, err := handler.HandleTransaction(context.Background(), chainState, txExcessive, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.NotNil(t, receipt2)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, receipt2.Status())
	// Nonce must advance to 7
	as2, err := stateDB.AccountState(senderAddr)
	assert.NoError(t, err)
	assert.Equal(t, uint64(7), as2.Nonce())
	// Balance should not have changed
	assert.Equal(t, big.NewInt(1000), as2.Balance())
}

type mockTxWithAmount struct {
	types.Transaction
	amt *big.Int
}

func (m *mockTxWithAmount) Amount() *big.Int {
	return m.amt
}

func TestParentChainGatewayHandler_SecurityAdversarialInputs(t *testing.T) {
	mockDispatcher := &MockCrossChainDispatcher{
		MockHash: common.HexToHash("0x5678"),
	}
	InitParentChainGatewayHandler(mockDispatcher)
	handler := GetParentChainGatewayHandler()
	assert.NotNil(t, handler)

	chainState, _, _, _ := newPersistentTestChainState(t)
	stateDB := chainState.GetAccountStateDB()
	sender := common.HexToAddress("0x9999999999999999999999999999999999999999")
	stateDB.AddBalance(sender, big.NewInt(100_000))
	stateDB.SetNonce(sender, 10)

	// Valid 100-byte payload with valid target address
	destKeyBytes := make([]byte, 48)
	destKeyBytes[0] = 0x02
	targetAddr := common.HexToAddress("0x8888888888888888888888888888888888888888")
	validPayload := make([]byte, 0, 100)
	validPayload = append(validPayload, destKeyBytes...)
	validPayload = append(validPayload, make([]byte, 32)...)
	validPayload = append(validPayload, targetAddr.Bytes()...)

	callData := transaction.NewCallData(validPayload)
	dataBytes, err := callData.Marshal()
	assert.NoError(t, err)

	// Security Case 1: Zero amount
	txZero := transaction.NewTransaction(
		sender, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS,
		big.NewInt(0), 21000, 1, 0, dataBytes, nil, common.Hash{}, common.Hash{}, 10, 1,
	)
	rcpZero, _, err := handler.HandleTransaction(context.Background(), chainState, txZero, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcpZero.Status(), "Zero amount must be rejected")
	assert.Contains(t, string(rcpZero.Return()), "transfer amount must be positive")

	// Security Case 2: Negative and Nil amounts
	baseTx := transaction.NewTransaction(
		sender, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS,
		big.NewInt(500), 21000, 1, 0, dataBytes, nil, common.Hash{}, common.Hash{}, 11, 1,
	)
	txNeg := &mockTxWithAmount{Transaction: baseTx, amt: big.NewInt(-500)}
	rcpNeg, _, err := handler.HandleTransaction(context.Background(), chainState, txNeg, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcpNeg.Status(), "Negative amount must be rejected")
	assert.Contains(t, string(rcpNeg.Return()), "transfer amount must be positive")

	txNil := &mockTxWithAmount{Transaction: baseTx, amt: nil}
	rcpNil, _, err := handler.HandleTransaction(context.Background(), chainState, txNil, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcpNil.Status(), "Nil amount must be rejected")
	assert.Contains(t, string(rcpNil.Return()), "transfer amount must be positive")

	// Security Case 3: Zero target address
	zeroTargetPayload := make([]byte, 0, 100)
	zeroTargetPayload = append(zeroTargetPayload, destKeyBytes...)
	zeroTargetPayload = append(zeroTargetPayload, make([]byte, 32)...)
	zeroTargetPayload = append(zeroTargetPayload, (common.Address{}).Bytes()...)
	cdZeroTarget := transaction.NewCallData(zeroTargetPayload)
	dataZeroTarget, _ := cdZeroTarget.Marshal()

	txZeroTarget := transaction.NewTransaction(
		sender, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS,
		big.NewInt(500), 21000, 1, 0, dataZeroTarget, nil, common.Hash{}, common.Hash{}, 12, 1,
	)
	rcpZeroTarget, _, err := handler.HandleTransaction(context.Background(), chainState, txZeroTarget, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcpZeroTarget.Status(), "Zero target address must be rejected")
	assert.Contains(t, string(rcpZeroTarget.Return()), "target address cannot be zero")

	// Security Case 4: Insufficient balance edge case:
	// TRANSFER_GAS_COST is 20,000. Gas fee is 20,000 * 1 = 20,000. Transfer amount is 500.
	// Total required is: 500 (amount) + 20,000 (gas fee) + 100 (cross-chain fee) = 20,600.
	// Sender has 20,550 (enough for transfer + gas, but 50 wei short of the cross-chain fee).
	victim := common.HexToAddress("0x7777777777777777777777777777777777777777")
	stateDB.AddBalance(victim, big.NewInt(20550))
	stateDB.SetNonce(victim, 1)

	txEdgeFee := transaction.NewTransaction(
		victim, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS,
		big.NewInt(500), 21000, 1, 0, dataBytes, nil, common.Hash{}, common.Hash{}, 1, 1,
	)
	rcpEdge, _, err := handler.HandleTransaction(context.Background(), chainState, txEdgeFee, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcpEdge.Status(), "Must reject when lacking the 100 wei cross-chain fee")
	assert.Contains(t, string(rcpEdge.Return()), "insufficient balance")

	// Verify balance is completely untouched
	asVictim, err := stateDB.AccountState(victim)
	assert.NoError(t, err)
	assert.Equal(t, big.NewInt(20550), asVictim.Balance(), "Balance must remain intact after failed validation")
}


// realDispatcher adapts the production *rollup.CrossNodeHandler (same wiring as cmd/simple_chain's adapter).
type realDispatcher struct{ h *rollup.CrossNodeHandler }

func (d realDispatcher) HandleTransfer(store rollup.Store, stateDB AccountStateAccessor, toKey mt_common.PublicKey,
	sender, target common.Address, value *big.Int, payloadHash common.Hash) (common.Hash, error) {
	return d.h.HandleTransfer(store, stateDB, toKey, sender, target, value, payloadHash)
}

// Balance conservation with the REAL dispatcher: a successful transfer debits exactly amount + transfer fee + gas
// fee, a sender that is one unit short is rejected with its balance untouched, and a balance can never go negative
// (before the up-front check covered all three, the gas SubBalance ran after the dispatcher had already taken
// amount+fee).
func TestParentChainGatewayHandler_BalanceConservationAtBoundary(t *testing.T) {
	var fromKey mt_common.PublicKey
	fromKey[0] = 0x01
	// Build the handler directly: InitParentChainGatewayHandler is a sync.Once singleton already claimed by the
	// mock-based tests above, so it cannot be re-pointed at the real dispatcher.
	handler := &ParentChainGatewayHandler{dispatcher: realDispatcher{h: rollup.NewCrossNodeHandler(fromKey)}}

	chainState, _, _, _ := newPersistentTestChainState(t)
	stateDB := chainState.GetAccountStateDB()

	destKey := make([]byte, 48)
	destKey[0] = 0x02
	target := common.HexToAddress("0x8888888888888888888888888888888888888888")
	payload := append(append(append([]byte{}, destKey...), make([]byte, 32)...), target.Bytes()...)
	cd, err := transaction.NewCallData(payload).Marshal()
	assert.NoError(t, err)

	amount := big.NewInt(500)
	mk := func(from common.Address, nonce uint64) *transaction.Transaction {
		return transaction.NewTransaction(from, mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, amount, 21000, 1, 0,
			cd, nil, common.Hash{}, common.Hash{}, nonce, 1).(*transaction.Transaction)
	}
	probe := mk(common.Address{}, 0)
	gasFee := new(big.Int).Mul(new(big.Int).SetUint64(uint64(mt_common.TRANSFER_GAS_COST)), probe.EffectiveGasPrice())
	need := new(big.Int).Add(new(big.Int).Add(amount, gasFee), big.NewInt(rollup.CrossNodeTransferFee))

	// One unit short: rejected, balance untouched.
	short := common.HexToAddress("0x1111111111111111111111111111111111110001")
	shortBal := new(big.Int).Sub(need, big.NewInt(1))
	stateDB.AddBalance(short, shortBal)
	rcp, _, err := handler.HandleTransaction(context.Background(), chainState, mk(short, 0), mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status(), "one unit short of amount+fee+gas must be rejected")
	asShort, _ := stateDB.AccountState(short)
	assert.Equal(t, shortBal.String(), asShort.Balance().String(), "rejected tx must not move funds")

	// Exactly enough: succeeds and ends at exactly zero (never negative).
	exact := common.HexToAddress("0x1111111111111111111111111111111111110002")
	stateDB.AddBalance(exact, need)
	rcp, _, err = handler.HandleTransaction(context.Background(), chainState, mk(exact, 0), mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status(), "exactly amount+fee+gas must succeed: %s", string(rcp.Return()))
	asExact, _ := stateDB.AccountState(exact)
	assert.Equal(t, int64(0), asExact.Balance().Int64(), "debit must be exactly amount + transfer fee + gas fee")
}
