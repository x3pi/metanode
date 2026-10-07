package tx_processor

import (
	"math/big"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func benchFilterSecp(b *testing.B, warm bool) {
	cs := setupTestChainState(&testing.T{})
	cs.GetConfig().TxSignatureMode = config.TxSignatureModeSecp
	const n = 512
	txs := make([]types.Transaction, 0, n)
	for i := 0; i < n; i++ {
		tx, _, from := createSignedEIP1559Tx(&testing.T{}, 1, 0)
		as := state.NewAccountState(from)
		as.AddBalance(big.NewInt(1_000_000_000_000_000))
		cs.GetAccountStateDB().SetState(as)
		txs = append(txs, tx)
	}
	groups := groupsOf(txs...)
	if warm {
		FilterInvalidSignatures(cs, groups)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !warm {
			rotateVerifiedSignatures()
			rotateVerifiedSignatures()
			for _, tx := range txs {
				tx.(*transaction.Transaction).ClearCacheHash() // cold: also forget memoized decode/sender
			}
		}
		if out := FilterInvalidSignatures(cs, groups); len(out) != n {
			b.Fatalf("got %d", len(out))
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/tx")
}

func BenchmarkFilterInvalidSignatures_Secp1559_Cold(b *testing.B) { benchFilterSecp(b, false) }
func BenchmarkFilterInvalidSignatures_Secp1559_Warm(b *testing.B) { benchFilterSecp(b, true) }
