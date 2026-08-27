package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTransferTx(from, to common.Address, nonce uint64, amount *big.Int) types.Transaction {
	return transaction.NewTransaction(
		from, to, amount,
		100000, // maxGas
		1,      // maxGasPrice
		1000,   // maxTimeUse
		nil,    // data (nil = regular transfer)
		nil,
		common.Hash{},
		common.Hash{},
		nonce,
		1,
	)
}

func TestGas_DustAccountCreation_TrueBlockSTM(t *testing.T) {
	cs := newTestChainState(t)

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	existingReceiver := common.HexToAddress("0x2222222222222222222222222222222222222222")
	newReceiver := common.HexToAddress("0x3333333333333333333333333333333333333333")
	leaderAddr := common.HexToAddress("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE")

	seedAccount(t, cs, sender, big.NewInt(10_000_000_000), 0)
	seedAccount(t, cs, existingReceiver, big.NewInt(100), 0)

	// Tx 0: Transfer to existing account -> 20,000 gas
	txExisting := newTransferTx(sender, existingReceiver, 0, big.NewInt(1000))
	// Tx 1: Transfer to new account -> 20,000 + 25,000 = 45,000 gas
	txNew := newTransferTx(sender, newReceiver, 1, big.NewInt(1000))

	txs := []types.Transaction{txExisting, txNew}
	stm := NewTrueBlockSTM(txs)
	processedTxs, rcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps, 2)
	require.Len(t, processedTxs, 2)

	// Verify status
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[1].Status())

	// Verify gas used parity
	assert.Equal(t, uint64(mt_common.TRANSFER_GAS_COST), rcps[0].GasUsed(), "Existing account should cost standard 20,000 gas")
	assert.Equal(t, uint64(mt_common.TRANSFER_GAS_COST+params.CallNewAccountGas), rcps[1].GasUsed(), "New account should cost 20,000 + 25,000 = 45,000 gas")
}

func TestGas_DustAccountCreation_NativeFastPath(t *testing.T) {
	cs := newTestChainState(t)

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	existingReceiver := common.HexToAddress("0x2222222222222222222222222222222222222222")
	newReceiver := common.HexToAddress("0x3333333333333333333333333333333333333333")
	leaderAddr := common.HexToAddress("0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE")

	seedAccount(t, cs, sender, big.NewInt(10_000_000_000), 0)
	seedAccount(t, cs, existingReceiver, big.NewInt(100), 0)

	// Tx 0: Transfer to existing account -> 20,000 gas
	txExisting := newTransferTx(sender, existingReceiver, 0, big.NewInt(1000))
	// Tx 1: Transfer to new account -> 20,000 + 25,000 = 45,000 gas
	txNew := newTransferTx(sender, newReceiver, 1, big.NewInt(1000))

	groups := []grouptxns.RelativeGroup{
		{
			GroupID: 0,
			Items: []grouptxns.Item{
				{Tx: txExisting},
				{Tx: txNew},
			},
		},
	}

	processedTxs, rcps, exRss, _ := processNativeTransfersFastPath(
		context.Background(), cs, groups, 2, false, leaderAddr, true, blankHeader(), 12345, nil,
	)
	require.Len(t, rcps, 2)
	require.Len(t, processedTxs, 2)
	require.Len(t, exRss, 2)

	// Verify status
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[1].Status())

	// Verify gas used parity between fast-path and STM
	assert.Equal(t, uint64(mt_common.TRANSFER_GAS_COST), rcps[0].GasUsed(), "Fast path: Existing account should cost standard 20,000 gas")
	assert.Equal(t, uint64(mt_common.TRANSFER_GAS_COST+params.CallNewAccountGas), rcps[1].GasUsed(), "Fast path: New account should cost 20,000 + 25,000 = 45,000 gas")

	assert.Equal(t, uint64(mt_common.TRANSFER_GAS_COST), exRss[0].GasUsed())
	assert.Equal(t, uint64(mt_common.TRANSFER_GAS_COST+params.CallNewAccountGas), exRss[1].GasUsed())
}
