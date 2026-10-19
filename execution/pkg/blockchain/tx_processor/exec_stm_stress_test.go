package tx_processor

import (
	"fmt"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Contention stress: few senders, many txs each, with duplicate nonces, nonce gaps, gas limits below intrinsic gas,
// underfunded transfers and hot recipients. The native fast path is the sequential oracle (same-sender txs run in nonce
// order inside one group); TrueBlockSTM is run several times on the SAME block because its bugs depend on goroutine
// scheduling and a single run proves nothing (the lost-update bug fixed in c96640f4 showed up in ~4% of runs).

func contentionBlock(r *rand.Rand, accts []diffAccount) []mt_types.Transaction {
	senders := 2 + r.Intn(2) // 2-3 senders
	prices := []uint64{mt_common.MINIMUM_BASE_FEE, mt_common.MINIMUM_BASE_FEE, 1, 0, mt_common.MINIMUM_BASE_FEE * 3}
	gasLimits := []uint64{21000, 21000, 21000, 20999, 50000}
	amounts := []int64{0, 1, 1000, 25_000_000, 1_000_000_000, 5_000_000_000_000}
	next := make([]uint64, len(accts))
	n := 8 + r.Intn(30)
	txs := make([]mt_types.Transaction, 0, n)
	for i := 0; i < n; i++ {
		fi := r.Intn(senders) + 3 // accounts a4..a6 carry the larger balances
		if fi >= len(accts) {
			fi = len(accts) - 1
		}
		var to common.Address
		switch r.Intn(5) {
		case 0:
			to = accts[fi].addr // self
		case 1:
			to = diffLeader
		case 2:
			to = common.BigToAddress(big.NewInt(int64(0xF000 + r.Intn(4)))) // brand-new recipient
		default:
			to = accts[3+r.Intn(3)].addr // hot recipient among the senders
		}
		nonce := next[fi]
		switch r.Intn(8) {
		case 0:
			nonce += 1 + uint64(r.Intn(2)) // gap
		case 1, 2:
			if nonce > 0 {
				nonce -= 1 + uint64(r.Intn(int(min64(nonce, 2)))) // replay of an older nonce
			}
		}
		if nonce == next[fi] {
			next[fi]++
		}
		txs = append(txs, transaction.NewTransaction(accts[fi].addr, to, big.NewInt(amounts[r.Intn(len(amounts))]),
			gasLimits[r.Intn(len(gasLimits))], prices[r.Intn(len(prices))], uint64(1000+i), nil, nil,
			common.Hash{}, common.Hash{}, nonce, parityChainID))
	}
	return txs
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func TestTrueBlockSTM_ContentionStress_MatchesSequentialOracle(t *testing.T) {
	const blocks, stmRuns = 100, 6
	accts := diffAccounts()
	bad := 0
	for seed := int64(1); seed <= blocks; seed++ {
		txs := contentionBlock(rand.New(rand.NewSource(seed*7919)), accts)
		oracle := diffRun(t, false, accts, txs)
		key := func(o diffOutcome) string {
			return strings.Join(o.accounts, "\n") + "\n" + strings.Join(o.receipts, "\n")
		}
		want := key(oracle)
		for run := 0; run < stmRuns; run++ {
			got := diffRun(t, true, accts, txs)
			if key(got) != want {
				bad++
				if bad <= 2 {
					t.Errorf("seed %d run %d: Block-STM differs from the sequential oracle\n%s oracle:\n  %s\n block-stm:\n  %s",
						seed, run, describeDiffBlock(txs), strings.ReplaceAll(want, "\n", "\n  "), strings.ReplaceAll(key(got), "\n", "\n  "))
				}
				break
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d contention blocks diverged from the oracle in at least one of %d Block-STM runs", bad, blocks, stmRuns)
	}
	_ = fmt.Sprint
}
