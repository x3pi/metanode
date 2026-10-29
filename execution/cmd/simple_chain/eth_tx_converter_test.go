package main

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/require"
)

func TestConvertRawEthTxToMetaTx_EnvelopeLimits(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)

	app := &App{
		config: &config.SimpleChainConfig{
			ChainId: chainID,
		},
	}

	t.Run("valid_large_tx_under_2MB_succeeds", func(t *testing.T) {
		// ~1 MB calldata -> total envelope ~1.01 MB (< 2 MB)
		largeData := make([]byte, 1024*1024)
		largeData[0] = 0x60
		tx := e_types.NewTx(&e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     1,
			GasTipCap: big.NewInt(10000),
			GasFeeCap: big.NewInt(200000),
			Gas:       10_000_000,
			To:        &common.Address{0x01},
			Value:     big.NewInt(100),
			Data:      largeData,
		})
		signed, err := e_types.SignTx(tx, signer, key)
		require.NoError(t, err)

		raw, err := signed.MarshalBinary()
		require.NoError(t, err)
		require.Less(t, len(raw), mt_transaction.MaxStandardTxEnvelopeSize)

		metaTx, parsedEthTx, err := app.ConvertRawEthTxToMetaTx(raw)
		require.NoError(t, err)
		require.NotNil(t, metaTx)
		require.NotNil(t, parsedEthTx)
	})

	t.Run("standard_tx_over_2MB_rejected", func(t *testing.T) {
		// Calldata large enough so envelope exceeds 2 MB
		oversizedData := make([]byte, mt_transaction.MaxStandardTxEnvelopeSize+100)
		tx := e_types.NewTx(&e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     2,
			GasTipCap: big.NewInt(10000),
			GasFeeCap: big.NewInt(200000),
			Gas:       20_000_000,
			To:        &common.Address{0x01},
			Value:     big.NewInt(100),
			Data:      oversizedData,
		})
		signed, err := e_types.SignTx(tx, signer, key)
		require.NoError(t, err)

		raw, err := signed.MarshalBinary()
		require.NoError(t, err)

		_, _, err = app.ConvertRawEthTxToMetaTx(raw)
		require.Error(t, err)
		require.ErrorIs(t, err, mt_transaction.ErrExceedsMaxEnvelopeSize)
	})
}
