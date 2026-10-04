package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func newSecpProtoTx(
	t *testing.T,
	privKey *ecdsa.PrivateKey,
	to common.Address,
	nonce uint64,
	amount *big.Int,
	chainID uint64,
) *transaction.Transaction {
	t.Helper()
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	tx := &transaction.Transaction{}
	tx.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   to.Bytes(),
		Amount:      amount.Bytes(),
		MaxGas:      mt_common.TRANSFER_GAS_COST,
		MaxGasPrice: mt_common.MINIMUM_BASE_FEE,
		MaxTimeUse:  1000,
		ChainID:     chainID,
		Type:        0xFF,
	})
	tx.SetNonce(nonce)
	err := tx.SignSecpProto(privKey)
	require.NoError(t, err)
	return tx
}

func TestSecpProto_FullExecution_BalanceTransferAndNonce(t *testing.T) {
	cs := newTestChainState(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	senderAddr := crypto.PubkeyToAddress(senderKey.PublicKey)

	recipientKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	recipientAddr := crypto.PubkeyToAddress(recipientKey.PublicKey)

	initialSenderBalance := big.NewInt(10_000_000_000_000)
	transferAmount := big.NewInt(1_000_000_000_000)

	// Seed sender account (pure secp, no BLS key)
	seedAccount(t, cs, senderAddr, initialSenderBalance, 0)

	// Create and sign Type 0xFF transaction
	tx := newSecpProtoTx(t, senderKey, recipientAddr, 0, transferAmount, 1)
	require.Equal(t, uint64(0xFF), tx.Type())
	require.True(t, tx.ValidSecpProtoSign())

	// Step 1: Pass through FilterInvalidSignatures (consensus filter)
	filteredGroups := FilterInvalidSignatures(cs, groupsOf(tx))
	require.Len(t, filteredGroups, 1, "valid Secp Proto tx must pass FilterInvalidSignatures")
	require.Len(t, filteredGroups[0].Items, 1)

	// Step 2: Execute transaction via TrueBlockSTM
	leaderAddr := common.HexToAddress("0xEE000000000000000000000000000000000000EE")
	stm := NewTrueBlockSTM([]types.Transaction{tx})
	execTxs, execRcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 1)

	require.Len(t, execTxs, 1)
	require.Len(t, execRcps, 1)
	require.NotNil(t, execRcps[0], "receipt must be generated")
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, execRcps[0].Status(), "tx execution status must be RETURNED")
	assert.Equal(t, tx.Hash(), execRcps[0].TransactionHash(), "receipt must link to tx.Hash()")

	// Step 3: Verify State updates in AccountStateDB
	// Recipient should receive exact transfer amount
	recipientState, err := cs.GetAccountStateDB().AccountStateReadOnly(recipientAddr)
	require.NoError(t, err)
	assert.Equal(t, transferAmount, recipientState.TotalBalance(), "recipient balance must match transfer amount")

	// Sender balance must be decremented by transfer amount + gas fee
	senderState, err := cs.GetAccountStateDB().AccountStateReadOnly(senderAddr)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), senderState.Nonce(), "sender nonce must increment from 0 to 1")

	expectedMaxSenderBalance := new(big.Int).Sub(initialSenderBalance, transferAmount)
	assert.True(t, senderState.TotalBalance().Cmp(expectedMaxSenderBalance) <= 0,
		"sender balance must be at most (initial - transferAmount)")
}

func TestSecpProto_SequentialTransactions_NonceChaining(t *testing.T) {
	cs := newTestChainState(t)

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	senderAddr := crypto.PubkeyToAddress(senderKey.PublicKey)
	recipientAddr := common.HexToAddress("0xCAFE00000000000000000000000000000000CAFE")

	initialBalance := big.NewInt(50_000_000_000_000)
	seedAccount(t, cs, senderAddr, initialBalance, 0)

	amountPerTx := big.NewInt(2_000_000_000_000)

	// Build 3 sequential transactions with nonces 0, 1, 2
	tx0 := newSecpProtoTx(t, senderKey, recipientAddr, 0, amountPerTx, 1)
	tx1 := newSecpProtoTx(t, senderKey, recipientAddr, 1, amountPerTx, 1)
	tx2 := newSecpProtoTx(t, senderKey, recipientAddr, 2, amountPerTx, 1)

	allTxs := []types.Transaction{tx0, tx1, tx2}

	// Filter signatures
	filtered := FilterInvalidSignatures(cs, groupsOf(allTxs...))
	require.Len(t, filtered, 3, "all 3 valid transactions must pass signature filter")

	// Execute block with all 3 transactions
	leaderAddr := common.HexToAddress("0xEE000000000000000000000000000000000000EE")
	stm := NewTrueBlockSTM(allTxs)
	execTxs, execRcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 2)

	require.Len(t, execTxs, 3)
	require.Len(t, execRcps, 3)

	for i, rcp := range execRcps {
		require.NotNil(t, rcp, "receipt %d must not be nil", i)
		assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status(), "tx %d must succeed", i)
	}

	// Verify recipient received 3 * amountPerTx
	expectedRecipientBalance := new(big.Int).Mul(amountPerTx, big.NewInt(3))
	recipientState, err := cs.GetAccountStateDB().AccountStateReadOnly(recipientAddr)
	require.NoError(t, err)
	assert.Equal(t, expectedRecipientBalance, recipientState.TotalBalance())

	// Verify sender nonce is 3
	senderState, err := cs.GetAccountStateDB().AccountStateReadOnly(senderAddr)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), senderState.Nonce(), "sender nonce must be 3 after 3 txs")
}

func TestSecpProto_FilterDropsForgedBeforeExecution(t *testing.T) {
	cs := newTestChainState(t)

	realKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	realAddr := crypto.PubkeyToAddress(realKey.PublicKey)

	attackerKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	recipientAddr := common.HexToAddress("0x1234000000000000000000000000000000001234")

	seedAccount(t, cs, realAddr, big.NewInt(10_000_000_000_000), 0)

	// Legitimate tx signed by realKey
	goodTx := newSecpProtoTx(t, realKey, recipientAddr, 0, big.NewInt(1_000_000), 1)

	// Forged tx: claim FromAddress = realAddr, but signed by attackerKey
	forgedTx := &transaction.Transaction{}
	forgedTx.FromProto(&pb.Transaction{
		FromAddress: realAddr.Bytes(),
		ToAddress:   recipientAddr.Bytes(),
		Amount:      big.NewInt(5_000_000).Bytes(),
		Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 0},
		MaxGas:      mt_common.TRANSFER_GAS_COST,
		MaxGasPrice: mt_common.MINIMUM_BASE_FEE,
		MaxTimeUse:  1000,
		ChainID:     1,
		Type:        0xFF,
	})
	err = forgedTx.SignSecpProto(attackerKey)
	require.NoError(t, err)

	// Filter signatures: only goodTx should survive
	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	filtered := FilterInvalidSignatures(cs, groupsOf(goodTx, forgedTx))
	require.Len(t, filtered, 1, "forged transaction must be dropped by FilterInvalidSignatures")
	assert.Equal(t, goodTx.Hash(), filtered[0].Items[0].Tx.Hash())
}
