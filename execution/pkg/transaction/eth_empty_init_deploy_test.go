package transaction

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestEthContractCreationWithEmptyInitCode(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)

	testCases := map[string]e_types.TxData{
		"legacy_nil_data": &e_types.LegacyTx{
			Nonce:    0,
			GasPrice: big.NewInt(200000),
			Gas:      100000,
			To:       nil,
			Value:    big.NewInt(123),
			Data:     nil,
		},
		"legacy_empty_data": &e_types.LegacyTx{
			Nonce:    1,
			GasPrice: big.NewInt(200000),
			Gas:      100000,
			To:       nil,
			Value:    big.NewInt(456),
			Data:     []byte{},
		},
		"eip2930_nil_data": &e_types.AccessListTx{
			ChainID:  chainID,
			Nonce:    0,
			GasPrice: big.NewInt(200000),
			Gas:      100000,
			To:       nil,
			Value:    big.NewInt(789),
			Data:     nil,
		},
		"eip2930_empty_data": &e_types.AccessListTx{
			ChainID:  chainID,
			Nonce:    1,
			GasPrice: big.NewInt(200000),
			Gas:      100000,
			To:       nil,
			Value:    big.NewInt(789),
			Data:     []byte{},
		},
		"1559_nil_data": &e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     0,
			GasTipCap: big.NewInt(0),
			GasFeeCap: big.NewInt(200000),
			Gas:       100000,
			To:        nil,
			Value:     big.NewInt(123),
			Data:      nil,
		},
		"1559_empty_data": &e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     1,
			GasTipCap: big.NewInt(0),
			GasFeeCap: big.NewInt(200000),
			Gas:       100000,
			To:        nil,
			Value:     big.NewInt(456),
			Data:      []byte{},
		},
	}

	for name, txData := range testCases {
		t.Run(name, func(t *testing.T) {
			signed, err := e_types.SignTx(e_types.NewTx(txData), signer, key)
			require.NoError(t, err)

			tx, err := NewTransactionFromEth(signed)
			require.NoError(t, err)
			mt, ok := tx.(*Transaction)
			require.True(t, ok)

			require.True(t, mt.IsDeployContract(), "To==nil must be classified as deploy contract even with empty initcode")
			require.False(t, mt.IsCallContract())
			require.False(t, mt.IsRegularTransaction())
			require.True(t, mt.ValidDeployData(), "Empty initcode deploy data must be valid")
			require.True(t, mt.ValidEthSign())
			require.NoError(t, ValidateProtoEnvelopeBinding(mt.proto))
			require.Equal(t, signed.Hash(), mt.Hash())
			require.Equal(t, common.Address{}, mt.ToAddress())
			require.Equal(t, 0, len(mt.DeployData().Code()))

			// Verify ToEthTransaction reconstruction preserves To == nil
			ethReconstructed := mt.ToEthTransaction()
			require.NotNil(t, ethReconstructed)
			require.Nil(t, ethReconstructed.To(), "Reconstructed Ethereum transaction must have To == nil for contract creation")
		})
	}
}

func TestEthZeroAddressTransferVsDeploy(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)
	zeroAddr := common.Address{}

	// Case 1: Transfer to zero address (To != nil, pointing to 0x0)
	transferTx := &e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     0,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &zeroAddr,
		Value:     big.NewInt(100),
		Data:      nil,
	}
	signedTransfer, err := e_types.SignTx(e_types.NewTx(transferTx), signer, key)
	require.NoError(t, err)

	txTransfer, err := NewTransactionFromEth(signedTransfer)
	require.NoError(t, err)
	require.False(t, txTransfer.IsDeployContract(), "Transfer to zero address is NOT deploy")
	require.True(t, txTransfer.IsRegularTransaction(), "Transfer to zero address without data is regular tx")
	require.False(t, txTransfer.IsCallContract())

	// Case 2: Call to zero address (To != nil, pointing to 0x0, with data)
	callTx := &e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(200000),
		Gas:       50000,
		To:        &zeroAddr,
		Value:     big.NewInt(0),
		Data:      []byte{0x12, 0x34},
	}
	signedCall, err := e_types.SignTx(e_types.NewTx(callTx), signer, key)
	require.NoError(t, err)

	txCall, err := NewTransactionFromEth(signedCall)
	require.NoError(t, err)
	require.False(t, txCall.IsDeployContract())
	require.False(t, txCall.IsRegularTransaction())
	require.True(t, txCall.IsCallContract(), "Call to zero address with data is call contract")
}
