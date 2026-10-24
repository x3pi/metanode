package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// Two transactions of one block that CREATE2 the same address must serialise: the first deploys, the second hits the
// EIP-684 collision and gets 0. The block (Block-STM, parallel) and one-transaction-per-block runs must agree.
func TestBlockSTMCreate2CollisionAcrossTransactionsSerialises(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	keys := make([]*ecdsa.PrivateKey, 3)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}
	w := &confWorld{sender: crypto.PubkeyToAddress(keys[0].PublicKey)}
	for i := 0; i < confContracts; i++ {
		w.p = append(w.p, crypto.CreateAddress(w.sender, uint64(1+i)))
	}
	w.eoas = []common.Address{common.HexToAddress("0x1111111111111111111111111111111111111111"), common.HexToAddress("0x2222222222222222222222222222222222222222")}
	w.inits = confChildInits(w.p)

	okChild := common.FromHex("0x600060005360016000f3")
	stop := []byte{0x00}
	// endowment 100 changes the child's balance; endowment 0 creates it with only code, which must be tracked too
	for _, endowment := range []int{100, 0} {
		for _, create2 := range []string{"CREATE2", "CREATE"} {
			var args []any
			if create2 == "CREATE2" {
				args = []any{0, len(okChild), 0, endowment, "CREATE2"}
			} else {
				args = []any{len(okChild), 0, endowment, "CREATE"}
			}
			factory := confScript(confJoin(confMemWrite(0, okChild), args, confReturnWords(1))...)
			runtimes := [][]byte{factory, stop, stop, stop}
			balances := []int64{1000, 0, 0, 0}
			calls := []stmCall{{sender: 1, target: 0}, {sender: 2, target: 0}, {sender: 1, target: 0}}

			seq := stmRun(t, keys, w, runtimes, balances, calls, false)
			blk := stmRun(t, keys, w, runtimes, balances, calls, true)
			name := create2 + "/endowment=" + strconv.Itoa(endowment)
			require.Equal(t, strings.Join(seq.receipts, "\n"), strings.Join(blk.receipts, "\n"), name+" receipts")
			require.Equal(t, strings.Join(seq.accounts, "\n"), strings.Join(blk.accounts, "\n"), name+" accounts")
		}
	}
}

// A contract created earlier in a block must be callable by the later transactions of the same block. Its code hash is
// visible through MVCC the moment the creating transaction finishes, so the code bytes must be too: otherwise a call into
// it silently runs as a call to an address without code.
func TestBlockSTMContractCreatedInBlockIsCallableInSameBlock(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	k, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(k.PublicKey)
	answer := confScript(0x2a, 0, "MSTORE", 32, 0, "RETURN") // returns the word 0x2a

	newState := func() (types.Transaction, func(txs ...types.Transaction) []types.Receipt) {
		cs := newTestChainState(t)
		cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
		leader := common.HexToAddress(auditLeader)
		seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
		seedAccount(t, cs, leader, big.NewInt(0), 0)
		return nil, func(txs ...types.Transaction) []types.Receipt {
			items := make([]grouptxns.Item, len(txs))
			for i, tx := range txs {
				items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
			}
			groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
			_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
			return r
		}
	}

	t.Run("deployment transaction", func(t *testing.T) {
		_, run := newState()
		x := crypto.CreateAddress(sender, 1)
		r := run(signTx(t, k, 1, nil, confDeployInit(answer)), signTx(t, k, 2, &x, []byte{1}))
		require.Len(t, r, 2)
		require.Equal(t, "000000000000000000000000000000000000000000000000000000000000002a", common.Bytes2Hex(r[1].Return()))
	})

	t.Run("CREATE from a contract", func(t *testing.T) {
		_, run := newState()
		// factory: CREATE the contract above and return its address
		init := confDeployInit(answer)
		factoryCode := confScript(confJoin(confMemWrite(0, init), []any{len(init), 0, 0, "CREATE"}, confReturnWords(1))...)
		run(signTx(t, k, 1, nil, confDeployInit(factoryCode))) // its own block
		factory := crypto.CreateAddress(sender, 1)
		child := crypto.CreateAddress(factory, 1)
		r := run(signTx(t, k, 2, &factory, []byte{1}), signTx(t, k, 3, &child, []byte{1}))
		require.Len(t, r, 2)
		require.Equal(t, "000000000000000000000000000000000000000000000000000000000000002a", common.Bytes2Hex(r[1].Return()))
	})
}
