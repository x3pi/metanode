package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// Block-STM versus sequential execution of random MVM blocks. Several senders call the same random contracts
// (the generator of the go-ethereum differential test) in ONE block, executed by the production pipeline
// (ProcessTransactionsOptimistic -> TrueBlockSTM, parallel with conflict detection and re-execution), and the same
// transactions are then executed one per block in the same order. Both must end in the same state with the same
// receipts: the block order is the serialisation order. EVM_STM_N sets the number of blocks (default 40).

type stmOutcome struct {
	accounts []string
	receipts []string
}

func stmRun(t *testing.T, keys []*ecdsa.PrivateKey, w *confWorld, runtimes [][]byte, balances []int64, calls []stmCall, oneBlock bool) stmOutcome {
	t.Helper()
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	leader := common.HexToAddress(auditLeader)
	for _, k := range keys {
		seedAccount(t, cs, crypto.PubkeyToAddress(k.PublicKey), big.NewInt(1_000_000_000_000_000), 1)
	}
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, w.eoas[0], big.NewInt(50), 0)

	mk := func(k *ecdsa.PrivateKey, nonce uint64, to *common.Address, val int64, data []byte) types.Transaction {
		s, err := eth.SignNewTx(k, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{Nonce: nonce, To: to, Value: big.NewInt(val), Gas: 2_000_000, GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: data})
		require.NoError(t, err)
		tx, err := transaction.NewTransactionFromEth(s)
		require.NoError(t, err)
		return tx
	}
	run := func(txs []types.Transaction) []types.Receipt {
		items := make([]grouptxns.Item, len(txs))
		for i, tx := range txs {
			items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
		}
		groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
		return r
	}
	// deploy the contracts from key 0 (nonces 1..4 so the addresses match the generator's world)
	for i := 0; i < confContracts; i++ {
		r := run([]types.Transaction{mk(keys[0], uint64(1+i), nil, balances[i], confDeployInit(runtimes[i]))})
		require.Len(t, r, 1)
		require.Equal(t, 0, int(r[0].Status()), "deploy of P%d: %v", i, r[0].Status())
	}
	nextNonce := make([]uint64, len(keys))
	for i := range nextNonce {
		nextNonce[i] = 1
	}
	nextNonce[0] = uint64(1 + confContracts)
	var txs []types.Transaction
	for _, c := range calls {
		txs = append(txs, mk(keys[c.sender], nextNonce[c.sender], &w.p[c.target], c.value, c.calldata))
		nextNonce[c.sender]++
	}
	var rcps []types.Receipt
	if oneBlock {
		rcps = run(txs)
	} else {
		// The serialisation order of a block is the order of its deterministic groups (transactions of one sender
		// by nonce), not the order in which the transactions were submitted: execute one per block in that order.
		items := make([]grouptxns.Item, len(txs))
		for i, tx := range txs {
			items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
		}
		for _, g := range grouptxns.GroupTransactionsDeterministic(items, cs.HasCode) {
			for _, it := range g.Items {
				rcps = append(rcps, run([]types.Transaction{it.Tx})...)
			}
		}
	}
	var out stmOutcome
	idx := map[common.Hash]int{}
	for i, tx := range txs {
		idx[tx.Hash()] = i
	}
	for _, r := range rcps {
		out.receipts = append(out.receipts, fmt.Sprintf("tx#%02d status=%v gas=%d ret=%x logs=%d", idx[r.TransactionHash()], r.Status(), r.GasUsed(), r.Return(), len(r.EventLogs())))
	}
	sort.Strings(out.receipts)
	watch := append([]common.Address{leader}, confCandidates(w)...)
	for _, k := range keys {
		watch = append(watch, crypto.PubkeyToAddress(k.PublicKey))
	}
	for _, a := range watch {
		st, err := cs.GetAccountStateDB().AccountState(a)
		require.NoError(t, err)
		sc := st.SmartContractState()
		hasCode := sc != nil && sc.CodeHash() != (common.Hash{}) && sc.CodeHash() != crypto.Keccak256Hash(nil)
		out.accounts = append(out.accounts, fmt.Sprintf("%s bal=%s nonce=%d code=%v", a.Hex(), st.TotalBalance(), st.Nonce(), hasCode))
	}
	sort.Strings(out.accounts)
	return out
}

type stmCall struct {
	sender, target int
	value          int64
	calldata       []byte
}

func TestEVMBlockSTMVersusSequential(t *testing.T) {
	n := 40
	if v := os.Getenv("EVM_STM_N"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	seed0 := int64(1)
	if v := os.Getenv("EVM_STM_SEED"); v != "" {
		seed0, _ = strconv.ParseInt(v, 10, 64)
	}
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")

	keys := make([]*ecdsa.PrivateKey, 4)
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

	build := func(s int64, excl map[[2]int]bool) (runtimes [][]byte, descs []string, balances []int64, calls []stmCall) {
		r := rand.New(rand.NewSource(s))
		balances = make([]int64, confContracts)
		for i := 0; i < confContracts; i++ {
			c, d := confGenContract(r, w, i, s, excl)
			runtimes = append(runtimes, c)
			descs = append(descs, d)
			balances[i] = int64(10 + r.Intn(90))
		}
		for j, k := 0, 3+r.Intn(6); j < k; j++ {
			calls = append(calls, stmCall{sender: r.Intn(len(keys)), target: r.Intn(2), value: []int64{0, 0, 1, 7}[r.Intn(4)], calldata: [][]byte{nil, {1}, {1, 2, 3}}[r.Intn(3)]})
		}
		return
	}
	differs := func(runtimes [][]byte, balances []int64, calls []stmCall) (bool, stmOutcome, stmOutcome) {
		seq := stmRun(t, keys, w, runtimes, balances, calls, false)
		blk := stmRun(t, keys, w, runtimes, balances, calls, true)
		return strings.Join(seq.receipts, "\n") != strings.Join(blk.receipts, "\n") || strings.Join(seq.accounts, "\n") != strings.Join(blk.accounts, "\n"), seq, blk
	}

	bad := 0
	for s := seed0; s < seed0+int64(n); s++ {
		runtimes, descs, balances, calls := build(s, nil)
		if d, _, _ := differs(runtimes, balances, calls); !d {
			continue
		}
		bad++
		// minimise: drop gadgets, then calls, while the divergence remains
		excl := map[[2]int]bool{}
		if os.Getenv("EVM_STM_NOMIN") == "" {
			for idx := 0; idx < confContracts; idx++ {
				for gi := 0; gi < 6; gi++ {
					excl[[2]int{idx, gi}] = true
					rt, _, bal, cl := build(s, excl)
					if d, _, _ := differs(rt, bal, cl); !d {
						delete(excl, [2]int{idx, gi})
					}
				}
			}
			runtimes, descs, balances, calls = build(s, excl)
			for i := len(calls) - 1; i >= 0; i-- {
				trial := append(append([]stmCall{}, calls[:i]...), calls[i+1:]...)
				if d, _, _ := differs(runtimes, balances, trial); d {
					calls = trial
				}
			}
		}
		_, seq, blk := differs(runtimes, balances, calls)
		for i, rt := range runtimes {
			t.Logf("  P%d runtime=%x balance=%d", i, rt, balances[i])
		}
		t.Logf("MISMATCH seed=%d %s balances=%v calls=%+v", s, strings.Join(descs, " "), balances, calls)
		for i := 0; i < len(seq.receipts) && i < len(blk.receipts); i++ {
			if seq.receipts[i] != blk.receipts[i] {
				t.Logf("  receipt differs:\n    seq=%.300s\n    blk=%.300s", seq.receipts[i], blk.receipts[i])
			}
		}
		for i := range seq.accounts {
			if seq.accounts[i] != blk.accounts[i] {
				t.Logf("  account differs: seq=%s | block=%s", seq.accounts[i], blk.accounts[i])
			}
		}
	}
	t.Logf("SUMMARY blocks=%d mismatching=%d", n, bad)
	if os.Getenv("EVM_STM_REPORT_ONLY") == "" {
		require.Zero(t, bad, "Block-STM diverges from sequential execution on %d/%d blocks", bad, n)
	}
}
