package tx_processor

import (
	"context"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Native coin counterpart of the token clobber test: normal native transfers touch an account, then a barrier tx (gateway
// outbound, which burns native coin from the sender) changes the same account. The batched paths must end with the same
// balances and nonces as one-tx-per-block execution.
func TestTrueBlockSTM_BarrierTxChangesToNativeAccountsSurviveEarlierNormalWrites(t *testing.T) {
	run := func(mode string) string {
		env, cs := barrierSetup(t)
		gh, err := GetGatewayHandler()
		if err != nil {
			t.Fatal(err)
		}
		a, b := env.evm[0].addr, env.evm[1].addr
		wrap := func(in []byte, err error) []byte {
			if err != nil {
				t.Fatal(err)
			}
			d, _ := transaction.NewCallData(in).Marshal()
			return d
		}
		nonces := map[common.Address]uint64{}
		next := func(x common.Address) uint64 { n := nonces[x]; nonces[x]++; return n }
		var txs []mt_types.Transaction
		tx := func(from, to common.Address, value int64, data []byte) {
			txs = append(txs, transaction.NewTransaction(from, to, big.NewInt(value), 600_000, 1, uint64(9900+len(txs)), data, nil, common.Hash{}, common.Hash{}, next(from), 991))
		}
		out := func(v int64) []byte {
			return wrap(gh.abi.Pack("outbound", big.NewInt(102), env.users[0], []byte{0xAE}, big.NewInt(0), big.NewInt(v), big.NewInt(0), big.NewInt(0), uint8(1), false, uint64(0)))
		}
		tx(b, a, 700, nil)                                     // normal: a is credited
		tx(a, env.users[0], 50, nil)                           // normal: a pays
		tx(a, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, out(300)) // barrier: burns 300 from a
		tx(b, a, 11, nil)                                      // normal after the barrier: a credited again
		tx(a, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, out(200)) // second barrier
		items := make([]grouptxns.Item, len(txs))
		for i, x := range txs {
			items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(x), Tx: x}
		}
		groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		var flat []mt_types.Transaction
		for _, g := range groups {
			for _, it := range g.Items {
				flat = append(flat, it.Tx)
			}
		}
		switch mode {
		case "seq":
			for _, x := range flat {
				NewTrueBlockSTM([]mt_types.Transaction{x}).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
			}
		case "stm":
			NewTrueBlockSTM(flat).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
		default:
			ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, diffLeader, true)
		}
		s := ""
		for _, x := range []common.Address{a, b, env.users[0], mt_common.GATEWAY_CONTRACT_ADDRESS} {
			st, err := cs.GetAccountStateDB().AccountState(x)
			if err != nil {
				t.Fatal(err)
			}
			s += fmt.Sprintf("%s bal=%s nonce=%d; ", x.Hex()[34:], st.TotalBalance(), st.Nonce())
		}
		return s
	}
	want := run("seq")
	for _, mode := range []string{"stm", "opt"} {
		if got := run(mode); got != want {
			t.Errorf("%s:\n got  %s\n want %s", mode, got, want)
		}
	}
	t.Logf("sequential: %s", want)
}
