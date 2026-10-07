package transaction

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// A standard Ethereum account starts at nonce 0, so a contract creation signed with nonce 0 must be classified as a
// deployment and still pass signature + envelope binding (PR #156 removed the "nonce != 0" condition).
func TestEthContractCreationAtNonce0(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)
	initCode := []byte{0x60, 0x80, 0x60, 0x40, 0x52}

	datas := map[string]e_types.TxData{
		"legacy": &e_types.LegacyTx{Nonce: 0, GasPrice: big.NewInt(200000), Gas: 100000, Data: initCode},
		"1559": &e_types.DynamicFeeTx{ChainID: chainID, Nonce: 0, GasTipCap: big.NewInt(0), GasFeeCap: big.NewInt(200000),
			Gas: 100000, Data: initCode},
	}
	for name, data := range datas {
		signed, err := e_types.SignTx(e_types.NewTx(data), signer, key)
		require.NoError(t, err)
		tx, err := NewTransactionFromEth(signed)
		require.NoError(t, err)
		mt := tx.(*Transaction)

		require.Equal(t, uint64(0), mt.GetNonce(), name)
		require.True(t, mt.IsDeployContract(), "%s: nonce-0 creation must be a deployment", name)
		require.False(t, mt.IsCallContract(), name)
		require.True(t, mt.ValidEthSign(), name)
		require.NoError(t, ValidateProtoEnvelopeBinding(mt.proto), name)
		require.Equal(t, signed.Hash(), mt.Hash(), name)
		require.Equal(t, common.Address{}, mt.ToAddress(), name)
	}
}
