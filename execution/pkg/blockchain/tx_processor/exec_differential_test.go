package tx_processor

import (
	"context"
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Differential test: the same random block of plain native transfers is executed by the native fast path
// (ProcessTransactionsOptimistic on a pure-native block) and by TrueBlockSTM, each on an identical fresh state. Which
// pipeline runs a block depends only on the OTHER txs in it, so any difference in the final state or in the receipts is
// a consensus hazard (two validators picking different pipelines would fork). Seeds are fixed, so a failure reproduces.

type diffAccount struct {
	addr    common.Address
	balance int64
}

func diffAccounts() []diffAccount {
	// Balances straddle the fees used below so that some transfers succeed, some fail for lack of funds.
	bal := []int64{0, 50_000_000, 3_000_000_000, 2_100_000_000_000, 7, 900_000_000_000}
	out := make([]diffAccount, len(bal))
	for i, b := range bal {
		out[i] = diffAccount{addr: common.BigToAddress(big.NewInt(int64(0xA1 + i))), balance: b}
	}
	return out
}

var diffLeader = common.HexToAddress("0xAAAA000000000000000000000000000000000001")

// diffBlock builds a random block. Nonces are mostly the next expected one, sometimes a gap or a replay.
func diffBlock(r *rand.Rand, accts []diffAccount) []mt_types.Transaction {
	prices := []uint64{mt_common.MINIMUM_BASE_FEE, mt_common.MINIMUM_BASE_FEE, mt_common.MINIMUM_BASE_FEE * 3, 1, 0}
	gasLimits := []uint64{21000, 21000, 21000, 21000, 20999, 50000}
	next := make([]uint64, len(accts))
	n := 1 + r.Intn(12)
	txs := make([]mt_types.Transaction, 0, n)
	for i := 0; i < n; i++ {
		fi := r.Intn(len(accts))
		var to common.Address
		switch r.Intn(6) {
		case 0:
			to = accts[fi].addr // self transfer
		case 1:
			to = diffLeader
		case 2:
			to = common.BigToAddress(big.NewInt(int64(0xF000 + r.Intn(4)))) // brand-new recipient
		default:
			to = accts[r.Intn(len(accts))].addr
		}
		nonce := next[fi]
		switch r.Intn(10) {
		case 0:
			nonce += 1 + uint64(r.Intn(2)) // gap
		case 1:
			if nonce > 0 {
				nonce-- // replay
			}
		}
		if nonce == next[fi] {
			next[fi]++
		}
		amounts := []int64{0, 1, 1000, 25_000_000, 1_000_000_000, 5_000_000_000_000}
		tx := transaction.NewTransaction(accts[fi].addr, to, big.NewInt(amounts[r.Intn(len(amounts))]),
			gasLimits[r.Intn(len(gasLimits))], prices[r.Intn(len(prices))], uint64(1000+i), nil, nil,
			common.Hash{}, common.Hash{}, nonce, parityChainID)
		txs = append(txs, tx)
	}
	return txs
}

type diffOutcome struct {
	accounts []string // "addr balance nonce" sorted by address
	receipts []string // "txhash status gas" sorted
	supply   *big.Int
}

func diffRun(t *testing.T, useSTM bool, accts []diffAccount, txs []mt_types.Transaction) diffOutcome {
	idx := map[common.Hash]int{}
	for i, tx := range txs {
		idx[tx.Hash()] = i
	}
	t.Helper()
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(parityChainID)})
	for _, a := range accts {
		seedAccount(t, cs, a.addr, big.NewInt(a.balance), 0)
	}
	seedAccount(t, cs, diffLeader, big.NewInt(0), 0)

	// Both pipelines get the block the way production hands it over: grouped deterministically (within a sender sorted by
	// nonce), and for TrueBlockSTM flattened in group order (see runEvmPipeline).
	items := make([]grouptxns.Item, len(txs))
	for i, tx := range txs {
		items[i] = grouptxns.Item{ID: i, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	var rcps []mt_types.Receipt
	if useSTM {
		flat := make([]mt_types.Transaction, 0, len(txs))
		for _, g := range groups {
			for _, it := range g.Items {
				flat = append(flat, it.Tx)
			}
		}
		_, rcps, _, _ = NewTrueBlockSTM(flat).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
	} else {
		_, rcps, _, _ = ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, diffLeader, true)
	}

	var out diffOutcome
	out.supply = new(big.Int)
	watch := []common.Address{diffLeader}
	for _, a := range accts {
		watch = append(watch, a.addr)
	}
	for i := 0; i < 4; i++ {
		watch = append(watch, common.BigToAddress(big.NewInt(int64(0xF000+i))))
	}
	for _, a := range watch {
		st, err := cs.GetAccountStateDB().AccountState(a)
		if err != nil {
			t.Fatalf("account state %s: %v", a.Hex(), err)
		}
		out.accounts = append(out.accounts, fmt.Sprintf("%s bal=%s nonce=%d", a.Hex(), st.TotalBalance(), st.Nonce()))
		out.supply.Add(out.supply, st.TotalBalance())
	}
	sort.Strings(out.accounts)
	for _, r := range rcps {
		if r == nil || fmt.Sprintf("%v", r) == "<nil>" {
			continue // a tx rejected before execution (e.g. nonce mismatch) has no receipt
		}
		out.receipts = append(out.receipts, fmt.Sprintf("tx#%02d status=%v gas=%d", idx[r.TransactionHash()], r.Status(), r.GasUsed()))
	}
	sort.Strings(out.receipts)
	return out
}

func describeDiffBlock(txs []mt_types.Transaction) string {
	var b strings.Builder
	for i, tx := range txs {
		fmt.Fprintf(&b, "  #%d from=%s to=%s nonce=%d amount=%s gas=%d price=%d\n", i, tx.FromAddress().Hex()[38:], tx.ToAddress().Hex()[38:],
			tx.GetNonce(), tx.Amount(), tx.MaxGas(), tx.MaxGasPrice())
	}
	return b.String()
}

func TestExecDifferential_NativeFastPathVsTrueBlockSTM(t *testing.T) {
	const iterations = 300
	accts := diffAccounts()
	var supplyBefore = new(big.Int)
	for _, a := range accts {
		supplyBefore.Add(supplyBefore, big.NewInt(a.balance))
	}
	diverged := 0
	for seed := int64(1); seed <= iterations; seed++ {
		txs := diffBlock(rand.New(rand.NewSource(seed)), accts)
		fast := diffRun(t, false, accts, txs)
		stm := diffRun(t, true, accts, txs)

		for name, o := range map[string]diffOutcome{"fast-path": fast, "block-stm": stm} {
			if o.supply.Cmp(supplyBefore) != 0 {
				t.Errorf("seed %d %s: supply not conserved, before %s after %s\n%s", seed, name, supplyBefore, o.supply, describeDiffBlock(txs))
			}
		}
		if strings.Join(fast.accounts, "\n") != strings.Join(stm.accounts, "\n") || strings.Join(fast.receipts, "\n") != strings.Join(stm.receipts, "\n") {
			diverged++
			if diverged <= 3 {
				t.Errorf("seed %d: pipelines diverge\n%s fast-path accounts:\n  %s\n block-stm accounts:\n  %s\n fast-path receipts:\n  %s\n block-stm receipts:\n  %s",
					seed, describeDiffBlock(txs), strings.Join(fast.accounts, "\n  "), strings.Join(stm.accounts, "\n  "),
					strings.Join(fast.receipts, "\n  "), strings.Join(stm.receipts, "\n  "))
			}
		}
	}
	if diverged > 0 {
		t.Errorf("%d of %d random blocks produced different outcomes between the two pipelines", diverged, iterations)
	}
}
