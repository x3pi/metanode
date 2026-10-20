package tx_processor

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Panic hunt on STATE-DEPENDENT paths: it takes the blocks of the barrier differential test (real BLS certs, Merkle proofs,
// registered chains, bonds, allocations) and randomly mangles the calldata of some txs, so the handlers reach their deep
// branches with hostile input. The random-calldata fuzz in exec_barrier_panic_fuzz_test.go almost never gets that far
// (it cannot forge a valid cert). A panic anywhere on this path takes down every validator that executes the block.
// Longer hunts: BARRIER_FUZZ_BLOCKS=20000 go test -run TestBarrierHandlers_NoPanicOnMutatedValidFlows ...
func TestBarrierHandlers_NoPanicOnMutatedValidFlows(t *testing.T) {
	blocks := 600
	if v := os.Getenv("BARRIER_FUZZ_BLOCKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			blocks = n
		}
	}
	sites := map[string]*panicSite{}
	mutated := 0
	for seed := int64(1); seed <= int64(blocks); seed++ {
		r := rand.New(rand.NewSource(seed * 7919))
		env, cs := barrierSetup(t)
		gen := barrierBlock
		switch seed % 3 {
		case 0: // the funded-ledger flows (ceiling attest, claim, credit/refund of the reserve allocation)
			seedGatewayLedger(t, env, cs)
			gen = ledgerBlock
		case 1: // attestations, checkpoints, PoPs, genesis digest, equivocation slash, batched outbound commit
			seedGatewayLedger(t, env, cs)
			miscTweak(t, env, cs)
			gen = miscBlock
		}
		txs := gen(t, r, env)
		for i, tx := range txs {
			if tx.ToAddress() == (common.Address{}) || r.Intn(3) != 0 {
				continue
			}
			in := tx.CallData().Input()
			if len(in) == 0 {
				continue
			}
			wrapped, err := transaction.NewCallData(mutateBytes(r, in)).Marshal()
			if err != nil {
				continue
			}
			txs[i] = transaction.NewTransaction(tx.FromAddress(), tx.ToAddress(), tx.Amount(), tx.MaxGas(), tx.MaxGasPrice(), uint64(9500+i), wrapped, nil,
				common.Hash{}, common.Hash{}, tx.GetNonce(), 991)
			mutated++
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					frame := repoFrame(string(debug.Stack()))
					key := fmt.Sprint(rec) + " | " + frame
					s := sites[key]
					if s == nil {
						var d strings.Builder
						for i, tx := range txs {
							fmt.Fprintf(&d, "  #%d from=%s to=%s nonce=%d amount=%s data=%dB\n", i, tx.FromAddress().Hex()[34:], tx.ToAddress().Hex()[34:], tx.GetNonce(), tx.Amount(), len(tx.Data()))
						}
						s = &panicSite{value: fmt.Sprint(rec), frame: frame, seed: seed, txs: d.String()}
						sites[key] = s
					}
					s.count++
				}
			}()
			var _ []mt_types.Transaction = txs
			NewTrueBlockSTM(txs).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
			_ = cs.GetSmartContractDB().LateBindRoots()
			_, _ = cs.GetAccountStateDB().IntermediateRoot(true)
			_, _ = cs.GetStakeStateDB().IntermediateRoot()
		}()
	}
	t.Logf("%d blocks, %d txs with mangled calldata", blocks, mutated)
	for _, s := range sites {
		t.Errorf("PANIC x%d (first at seed %d): %s\n  raised at %s\n  block:\n%s", s.count, s.seed, s.value, s.frame, s.txs)
	}
}
