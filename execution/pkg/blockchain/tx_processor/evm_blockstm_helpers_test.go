package tx_processor

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// signTx builds a signed legacy transaction (chain 991, 2M gas) from key k.
func signTx(t *testing.T, k *ecdsa.PrivateKey, nonce uint64, to *common.Address, data []byte) types.Transaction {
	t.Helper()
	s, err := eth.SignNewTx(k, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{Nonce: nonce, To: to, Value: big.NewInt(0), Gas: 2_000_000, GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: data})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(s)
	require.NoError(t, err)
	return tx
}
