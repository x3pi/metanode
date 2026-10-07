package transaction

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Fields that an EIP-2718 envelope cannot carry (MaxTimeUse, device keys, ReadOnly) are still read by execution,
// so they must be bound to their canonical zero values for every envelope type (P0-9 follow-up).
func TestEnvelopeBinding_NonEnvelopeFieldsMustStayCanonical(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")

	envelopes := map[string]e_types.TxData{
		"legacy": &e_types.LegacyTx{Nonce: 0, GasPrice: big.NewInt(200000), Gas: 21000, To: &to, Value: big.NewInt(1)},
		"2930":   &e_types.AccessListTx{ChainID: chainID, Nonce: 0, GasPrice: big.NewInt(200000), Gas: 21000, To: &to, Value: big.NewInt(1)},
		"1559": &e_types.DynamicFeeTx{ChainID: chainID, Nonce: 0, GasTipCap: big.NewInt(0), GasFeeCap: big.NewInt(200000),
			Gas: 21000, To: &to, Value: big.NewInt(1)},
	}
	mutations := map[string]func(*Transaction){
		"MaxTimeUse":    func(x *Transaction) { x.proto.MaxTimeUse = 777 },
		"LastDeviceKey": func(x *Transaction) { x.proto.LastDeviceKey = []byte{1, 2, 3} },
		"NewDeviceKey":  func(x *Transaction) { x.proto.NewDeviceKey = []byte{4, 5, 6} },
		"ReadOnly":      func(x *Transaction) { x.proto.ReadOnly = true },
	}

	for name, data := range envelopes {
		signed, err := e_types.SignTx(e_types.NewTx(data), signer, key)
		require.NoError(t, err)

		build := func() *Transaction {
			tx, err := NewTransactionFromEth(signed)
			require.NoError(t, err)
			return tx.(*Transaction)
		}

		honest := build()
		require.NoError(t, ValidateProtoEnvelopeBinding(honest.proto), "%s: honest tx must bind", name)
		require.True(t, honest.ValidEthSign(), "%s: honest tx must verify", name)

		for field, mutate := range mutations {
			forged := build()
			mutate(forged)
			forged.ClearCacheHash()
			require.ErrorIs(t, ValidateProtoEnvelopeBinding(forged.proto), ErrEnvelopeBindingMismatch, "%s/%s", name, field)
			require.False(t, forged.ValidEthSign(), "%s/%s: forged copy must not verify", name, field)
			require.Equal(t, honest.Hash(), forged.Hash(), "%s/%s: same envelope => same hash (why binding is needed)", name, field)
		}
	}
}
