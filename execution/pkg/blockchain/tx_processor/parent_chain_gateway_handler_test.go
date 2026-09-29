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
