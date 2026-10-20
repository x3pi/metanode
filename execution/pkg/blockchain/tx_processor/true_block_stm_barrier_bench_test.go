package tx_processor

import (
	"context"
	"math/big"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Manual measurement of what resetting the MVCC maps after each barrier tx costs: a block of 2000 native transfers with a
// gateway barrier tx every N txs (N = 0: none). Run with BARRIER_BENCH=1; the numbers are wall-clock medians of 7 runs on a
// shared machine, so only large differences mean anything.
func TestTrueBlockSTM_BarrierEveryTiming(t *testing.T) {
	if os.Getenv("BARRIER_BENCH") == "" {
		t.Skip("set BARRIER_BENCH=1 to run")
	}
	gh, err := GetGatewayHandler()
	if err != nil {
		t.Fatal(err)
	}
	for _, every := range []int{0, 500, 100, 20} {
		var runs []time.Duration
		for it := 0; it < 7; it++ {
			_, cs := barrierSetup(t)
			var txs []mt_types.Transaction
			nonces := map[common.Address]uint64{}
			const senders = 400
			for i := 0; i < senders; i++ {
				seedAccount(t, cs, common.BigToAddress(big.NewInt(int64(0xB000+i))), big.NewInt(1_000_000_000), 0)
			}
			for i := 0; i < 2000; i++ {
				from := common.BigToAddress(big.NewInt(int64(0xB000 + i%senders)))
				to := common.BigToAddress(big.NewInt(int64(0xC000 + i%97)))
				n := nonces[from]
				nonces[from]++
				if every > 0 && i%every == every-1 {
					in, _ := gh.abi.Pack("withdrawRelayerTip")
					d, _ := transaction.NewCallData(in).Marshal()
					txs = append(txs, transaction.NewTransaction(from, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 100_000, 1, uint64(i), d, nil, common.Hash{}, common.Hash{}, n, 991))
					continue
				}
				txs = append(txs, transaction.NewTransaction(from, to, big.NewInt(1), 21000, 1, uint64(i), nil, nil, common.Hash{}, common.Hash{}, n, 991))
			}
			start := time.Now()
			NewTrueBlockSTM(txs).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
			runs = append(runs, time.Since(start))
		}
		sort.Slice(runs, func(a, b int) bool { return runs[a] < runs[b] })
		t.Logf("barrier every %4d txs: median %v (min %v, max %v)", every, runs[len(runs)/2], runs[0], runs[len(runs)-1])
	}
}
