package tx_processor

import (
	"context"
	"math/big"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/mvcc"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// stressIterations returns how many blocks to run. The default is small so the normal test run stays
// fast; set STM_STRESS_ITERS (for example 20000) to hunt for rare interleavings.
func stressIterations(def int) int {
	if v, err := strconv.Atoi(os.Getenv("STM_STRESS_ITERS")); err == nil && v > 0 {
		return v
	}
	return def
}

// TestTrueBlockSTM_HotRecipientNoLostUpdate_Stress executes the SAME shape of block that the C0 spike uses
// (three senders with consecutive nonces, all crediting a few shared "hot" recipients) through the real
// TrueBlockSTM many times and with different GOMAXPROCS, and checks the result against a plain arithmetic
// expectation: every recipient must hold exactly the sum of the amounts sent to it and every transaction
// must succeed. Native transfers commute, so ANY deviation is a lost or duplicated update, i.e. a
// non-deterministic state (a fork hazard), not a legitimate ordering effect.
func TestTrueBlockSTM_HotRecipientNoLostUpdate_Stress(t *testing.T) {
	iters := stressIterations(40)
	procsChoices := []int{1, 2, 4, 8, 16}
	if v := os.Getenv("STM_STRESS_PROCS"); v != "" { // e.g. "8" or "1,8"
		procsChoices = nil
		for _, f := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 {
				procsChoices = append(procsChoices, n)
			}
		}
	}
	maxWorkers, _ := strconv.Atoi(os.Getenv("STM_STRESS_MAXWORKERS")) // 0 = default (GOMAXPROCS-scaled)
	prevProcs := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(prevProcs) })

	senders := []common.Address{
		common.HexToAddress("0x294f72878a83B7d076E1d28eedecd184863df846"),
		common.HexToAddress("0x616969160142a381bb315A286fA54B7eD1749C49"),
		common.HexToAddress("0x7EB655B6A3f58DE47CA598385ba531A8f4e156B1"),
	}
	recipients := []common.Address{
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		common.HexToAddress("0x2222222222222222222222222222222222222222"),
		common.HexToAddress("0x3333333333333333333333333333333333333333"),
		common.HexToAddress("0x4444444444444444444444444444444444444444"),
	}
	type spec struct {
		sender, recip int
		amount        int64
	}
	specs := []spec{{0, 0, 1}, {0, 0, 2}, {1, 0, 3}, {2, 1, 1}, {1, 2, 2}, {2, 3, 3}}
	const rounds = 31 // 6 * 31 = 186 transactions, like the spike's large blocks
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	maxFail, _ := strconv.Atoi(os.Getenv("STM_STRESS_MAXFAIL")) // 0 = stop after 5 failing iterations
	if maxFail <= 0 {
		maxFail = 5
	}
	failures := 0
	for it := 0; it < iters && failures < maxFail; it++ {
		procs := procsChoices[it%len(procsChoices)]
		runtime.GOMAXPROCS(procs)
		rnd := rand.New(rand.NewSource(int64(it) + 1))

		cs := newTestChainState(t)
		for _, s := range senders {
			seedAccount(t, cs, s, new(big.Int).Lsh(big.NewInt(1), 100), 0)
		}

		// Build the block: keep each sender's nonces ascending but interleave senders randomly.
		nextNonce := make([]uint64, len(senders))
		var queue []spec
		for r := 0; r < rounds; r++ {
			queue = append(queue, specs...)
		}
		want := make([]*big.Int, len(recipients))
		for i := range want {
			want[i] = big.NewInt(0)
		}
		txs := make([]types.Transaction, 0, len(queue))
		for len(queue) > 0 {
			i := rnd.Intn(len(queue))
			s := queue[i]
			queue = append(queue[:i], queue[i+1:]...)
			amt := big.NewInt(s.amount * 1_000_000)
			txs = append(txs, newTx(senders[s.sender], recipients[s.recip], nextNonce[s.sender], amt, nil))
			nextNonce[s.sender]++
			want[s.recip].Add(want[s.recip], amt)
		}

		stm := NewTrueBlockSTM(txs)
		if maxWorkers > 0 {
			stm = stm.WithMaxExecWorkers(maxWorkers)
		}
		_, rcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)

		bad := false
		for i, rcp := range rcps {
			if rcp == nil || rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
				t.Errorf("iter %d (GOMAXPROCS=%d): tx %d did not succeed: %v", it, procs, i, rcp)
				bad = true
				break
			}
		}
		for i, r := range recipients {
			as, err := cs.GetAccountStateDB().AccountState(r)
			if err != nil || as == nil {
				t.Errorf("iter %d (GOMAXPROCS=%d): recipient %d missing: %v", it, procs, i, err)
				bad = true
				continue
			}
			if got := as.TotalBalance(); got.Cmp(want[i]) != 0 {
				t.Errorf("iter %d (GOMAXPROCS=%d): recipient %s balance = %s, want %s (diff %s): LOST/DUPLICATED UPDATE",
					it, procs, r.Hex(), got, want[i], new(big.Int).Sub(got, want[i]))
				bad = true
			}
		}
		for i, s := range senders {
			as, err := cs.GetAccountStateDB().AccountState(s)
			if err != nil || as == nil || as.Nonce() != nextNonce[i] {
				n := uint64(0)
				if as != nil {
					n = as.Nonce()
				}
				t.Errorf("iter %d (GOMAXPROCS=%d): sender %d nonce = %d, want %d", it, procs, i, n, nextNonce[i])
				bad = true
			}
		}
		if bad {
			failures++
			// Scheduler diagnostics: a tx that did not end in status 3 (Validated) was never confirmed.
			statusCount := map[int32]int{}
			for i := range stm.txState {
				_, st := unpackState(atomic.LoadUint64(&stm.txState[i]))
				statusCount[st]++
			}
			for i, r := range recipients {
				st, ver, _, blocking := stm.accountMap.Read(r, mvcc.MaxVersion)
				bal := "nil"
				if st != nil {
					bal = st.TotalBalance().String()
				}
				t.Logf("iter %d recipient %d MVCC tip: balance=%s writerVersion=%d blockingEstimateVersion=%d (BaseVersion=%d means none)",
					it, i, bal, ver, blocking, mvcc.BaseVersion)
			}
			// Walk the version chain of the hot recipient: each writer's balance must equal the previous
			// writer's balance plus its own amount. Report the first writer that does not.
			{
				hot := recipients[0]
				prevBal := big.NewInt(0)
				prevWriter := -1
				broken := 0
				for idx := 0; idx < len(txs); idx++ {
					if txs[idx].ToAddress() != hot {
						continue
					}
					st, ver, _, _ := stm.accountMap.Read(hot, mvcc.Version(idx+1)) // floor <= idx
					if st == nil || int(ver) != idx {
						t.Logf("iter %d hot recipient: tx %d has NO version entry (floor=%d)", it, idx, ver)
						broken++
						continue
					}
					bal := st.TotalBalance()
					delta := new(big.Int).Sub(bal, prevBal)
					if delta.Cmp(txs[idx].Amount()) != 0 && broken < 6 {
						t.Logf("iter %d hot recipient: writer tx %d (prev writer %d): balance delta=%s but its amount=%s (from %s nonce %d)",
							it, idx, prevWriter, delta, txs[idx].Amount(), txs[idx].FromAddress().Hex()[:8], txs[idx].GetNonce())
						broken++
					}
					prevBal = bal
					prevWriter = idx
				}
			}
			t.Logf("iter %d diagnostics: aborts=%d final tx status counts (0=pending 1=executed 2=validating 3=validated 4=aborted): %v",
				it, atomic.LoadInt32(&stm.abortCount), statusCount)
		}
	}
}
