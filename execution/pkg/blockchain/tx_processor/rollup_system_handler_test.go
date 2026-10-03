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

type MockRollupSystemDispatcher struct {
	HandleSystemEventCalled bool
	LastData                []byte
	MockError               error
}

func (m *MockRollupSystemDispatcher) HandleSystemEvent(
	store rollup.Store,
	stateDB AccountStateAccessor,
	data []byte,
) error {
	m.HandleSystemEventCalled = true
	m.LastData = data
	return m.MockError
}

func TestRollupSystemHandler_HandleTransaction(t *testing.T) {
	mockDispatcher := &MockRollupSystemDispatcher{}
	InitRollupSystemHandler(mockDispatcher)
	handler := GetRollupSystemHandler()
	assert.NotNil(t, handler)

	chainState, _, _, _ := newPersistentTestChainState(t)
	senderAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	stateDB := chainState.GetAccountStateDB()
	initialBalance := big.NewInt(1000000000000000000) // 1 ETH
	stateDB.AddBalance(senderAddr, initialBalance)

	payload := []byte(`{"type":"test"}`)
	tx := transaction.NewTransaction(
		senderAddr,
		rollup.RollupSystemAddress,
		big.NewInt(0),
		21000,
		1000, // gasPrice
		0,
		payload,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1,
	)

	receipt, _, err := handler.HandleTransaction(context.Background(), chainState, tx, rollup.RollupSystemAddress, false, 0)
	assert.NoError(t, err)
	assert.NotNil(t, receipt)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
	assert.True(t, mockDispatcher.HandleSystemEventCalled)

	// Verify gas deduction
	accountState, _ := stateDB.AccountState(senderAddr)
	newBalance := accountState.Balance()
	gasFee := new(big.Int).Mul(big.NewInt(int64(mt_common.TRANSFER_GAS_COST)), big.NewInt(1000))
	expectedBalance := new(big.Int).Sub(initialBalance, gasFee)
	assert.Equal(t, expectedBalance.String(), newBalance.String())

	// Verify nonce advanced
	assert.Equal(t, uint64(1), accountState.Nonce())
}
