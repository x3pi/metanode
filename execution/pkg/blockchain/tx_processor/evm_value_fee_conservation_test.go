package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// TestReviewEVMValueAndFeeConservation verifies that when a transaction has enough balance
// for its value before gas deduction but not after gas fee is deducted, the EVM execution does
// not credit the contract while failing to debit the sender (preventing unbacked token creation).
func TestReviewEVMValueAndFeeConservation(t *testing.T) { runReviewValue(t, 0) }

func TestReviewEVMValueAndFeeControl(t *testing.T) { runReviewValue(t, 100_000_000_000) }

func runReviewValue(t *testing.T, extra int64) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	initial := big.NewInt(1_000_000_000_000)
	seedAccount(t, cs, sender, new(big.Int).Add(initial, big.NewInt(extra)), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Payable initcode returning a one-byte STOP runtime.
	code := common.FromHex("0x6001600c60003960016000f300")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	seedAccount(t, cs, recipient, big.NewInt(1), 1)
	price := uint64(mt_common.MINIMUM_BASE_FEE)
	makeTx := func(nonce uint64, to *common.Address, value *big.Int, gas uint64, data []byte) mt_types.Transaction {
		signed, err := eth_types.SignNewTx(key, eth_types.NewEIP155Signer(big.NewInt(991)), &eth_types.LegacyTx{
			Nonce:    nonce,
			To:       to,
			Value:    value,
			Gas:      gas,
			GasPrice: new(big.Int).SetUint64(price),
			Data:     data,
		})
		require.NoError(t, err)
		result, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		return result
	}

	first := makeTx(1, &recipient, big.NewInt(100_000_000_000), 21000, nil)
	amount := new(big.Int).Sub(initial, big.NewInt(100_000_000_000))
	amount.Sub(amount, new(big.Int).Mul(new(big.Int).SetUint64(mt_common.TRANSFER_GAS_COST), new(big.Int).SetUint64(price)))
	tx := makeTx(2, nil, amount, 100000, code)

	before, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.True(t, first.ValidMaxFee(before))
	require.True(t, tx.ValidMaxFee(before))
	require.True(t, first.ValidEthSign())
	require.True(t, tx.ValidEthSign())
	require.Nil(t, VerifyTransaction(first, cs, before))
	require.Nil(t, VerifyTransaction(tx, cs, before))
	t.Logf("both signed transactions pass VerifyTransaction against initial balance; deploy value=%s", amount)

	txs := []mt_types.Transaction{first, tx}
	items := make([]grouptxns.Item, len(txs))
	for i, item := range txs {
		items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(item), Tx: item}
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	_, rcps, scrs, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
	require.Len(t, rcps, 2)

	sum := new(big.Int)
	for _, addr := range []common.Address{sender, leader, recipient} {
		st, err := cs.GetAccountStateDB().AccountState(addr)
		require.NoError(t, err)
		t.Logf("account=%s balance=%s", addr, st.TotalBalance())
		sum.Add(sum, st.TotalBalance())
	}
	for _, rs := range scrs {
		if rs != nil {
			for addr := range rs.MapCodeHash() {
				st, err := cs.GetAccountStateDB().AccountState(common.HexToAddress(addr))
				require.NoError(t, err)
				t.Logf("contract=%s balance=%s", addr, st.TotalBalance())
				sum.Add(sum, st.TotalBalance())
			}
		}
	}
	t.Logf("status=%s gas=%d initial=%s final=%s return=%s", rcps[1].Status(), rcps[1].GasUsed(), new(big.Int).Add(initial, big.NewInt(1+extra)), sum, rcps[1].Return())
	require.Equal(t, 0, sum.Cmp(new(big.Int).Add(initial, big.NewInt(1+extra))), "balance conservation")
}
