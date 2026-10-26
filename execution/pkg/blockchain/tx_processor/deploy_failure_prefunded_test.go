package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// A failed top-level deployment must discard the half-created contract but must not
// destroy ether that was sent to the (predictable) contract address before the deployment.
func TestFailedDeployKeepsPrefundedBalance(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	target := crypto.CreateAddress(sender, 1)
	seedAccount(t, cs, target, big.NewInt(500), 0)

	execute := func(nonce uint64, value int64, data []byte) types.Receipt {
		signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
			Nonce: nonce, Value: big.NewInt(value), Gas: 300000,
			GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: data,
		})
		require.NoError(t, err)
		tx, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		st, err := cs.GetAccountStateDB().AccountState(sender)
		require.NoError(t, err)
		require.Nil(t, VerifyTransaction(tx, cs, st))
		items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
		_, receipts, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
		require.Len(t, receipts, 1)
		return receipts[0]
	}

	// initcode: PUSH1 0 PUSH1 0 REVERT
	r := execute(1, 100, common.FromHex("60006000fd"))
	require.NotEqual(t, pb.RECEIPT_STATUS_RETURNED, r.Status())

	st, err := cs.GetAccountStateDB().AccountState(target)
	require.NoError(t, err)
	require.Equal(t, "500", st.TotalBalance().String(), "pre-funded ether must survive a failed deployment")
	require.Zero(t, st.Nonce(), "failed deployment must not leave a contract nonce")
}
