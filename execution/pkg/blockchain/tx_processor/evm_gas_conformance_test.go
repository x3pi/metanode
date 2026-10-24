package tx_processor

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Gas conformance: the total gas of a transaction on the MVM must equal go-ethereum's (intrinsic gas + execution gas,
// minus the refund capped at a fifth of the total). go-ethereum starts with exactly the gas the MVM has after the
// intrinsic gas so that the 63/64 rule and out-of-gas cases behave the same. EVM_GAS_N sets the number of random
// programs (default 150); EVM_GAS_REPORT_ONLY=1 prints the differences without failing.

// confGethTotalGas is go-ethereum's gas used for the whole transaction, as the receipt would show it.
func confGethTotalGas(g confResult, calldata []byte) uint64 {
	intrinsic := confIntrinsicGas(calldata)
	total := intrinsic + g.gas
	refund := g.refund
	if cap := total / 5; refund > cap {
		refund = cap
	}
	return total - refund
}

func TestEVMGasAgainstGeth(t *testing.T) {
	n := 150
	if v := os.Getenv("EVM_GAS_N"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	w := &confWorld{sender: crypto.PubkeyToAddress(key.PublicKey)}
	for i := 0; i < confContracts; i++ {
		w.p = append(w.p, crypto.CreateAddress(w.sender, uint64(1+i)))
	}
	w.eoas = []common.Address{common.HexToAddress("0x1111111111111111111111111111111111111111"), common.HexToAddress("0x2222222222222222222222222222222222222222")}
	w.inits = confChildInits(w.p)

	type stat struct {
		n, same int
		deltas  []int64
		example string
	}
	stats := map[string]*stat{}
	record := func(name string, delta int64, example string) {
		st := stats[name]
		if st == nil {
			st = &stat{example: example}
			stats[name] = st
		}
		st.n++
		if delta == 0 {
			st.same++
		} else if st.example == "" || len(st.deltas) == st.same {
			st.example = example
		}
		st.deltas = append(st.deltas, delta)
	}

	maxGadgets := 1
	if v := os.Getenv("EVM_GAS_MAXGADGETS"); v != "" {
		maxGadgets, _ = strconv.Atoi(v)
	}
	os.Setenv("EVM_CONFORMANCE_MAXGADGETS", strconv.Itoa(maxGadgets))
	defer os.Unsetenv("EVM_CONFORMANCE_MAXGADGETS")

	type gasOutcome struct {
		comparable bool
		delta      int64
		descs      []string
		cd         []byte
		detail     string
	}
	evalGas := func(s int64, excl map[[2]int]bool) gasOutcome {
		r := rand.New(rand.NewSource(s))
		var runtimes [][]byte
		var descs []string
		balances := make([]int64, confContracts)
		for i := 0; i < confContracts; i++ {
			c, d := confGenContract(r, w, i, s+777, excl)
			runtimes = append(runtimes, c)
			descs = append(descs, d)
			balances[i] = int64(10 + r.Intn(90))
		}
		cd := [][]byte{nil, {1}}[r.Intn(2)]
		g := confRunGeth(w, runtimes, balances, 0, cd)
		m := confRunMVMReal(t, w, key, runtimes, balances, 0, cd)
		o := gasOutcome{descs: descs, cd: cd}
		p0 := fmt.Sprintf(" P0=%x cd=%x", runtimes[0], cd)
		if g.ok != m.ok {
			o.comparable, o.delta = true, 1<<40
			return o
		}
		o.comparable = true
		o.delta = int64(m.gas) - int64(confGethTotalGas(g, cd))
		o.detail = fmt.Sprintf("gethExec=%d gethRefund=%d gethTotal=%d mvmTotal=%d", g.gas, g.refund, confGethTotalGas(g, cd), m.gas) + p0
		return o
	}
	for s := int64(1); s <= int64(n); s++ {
		o := evalGas(s, nil)
		if !o.comparable {
			continue
		}
		descs := o.descs
		if o.delta != 0 && maxGadgets > 1 && os.Getenv("EVM_GAS_NOMIN") == "" {
			excl := map[[2]int]bool{}
			for idx := 0; idx < confContracts; idx++ {
				for gi := 0; gi < 6; gi++ {
					excl[[2]int{idx, gi}] = true
					if e := evalGas(s, excl); !e.comparable || e.delta == 0 {
						delete(excl, [2]int{idx, gi})
					}
				}
			}
			e := evalGas(s, excl)
			descs, o.delta, o.detail = e.descs, e.delta, e.detail
		}
		name := descs[0][strings.Index(descs[0], "[")+1:]
		name = strings.SplitN(name, ";", 2)[0]
		name = strings.SplitN(name, "(", 2)[0]
		record(name, o.delta, fmt.Sprintf("seed=%d delta=%d %s [%s]", s, o.delta, strings.Join(descs, " "), o.detail))
	}

	names := make([]string, 0, len(stats))
	for k := range stats {
		names = append(names, k)
	}
	sort.Strings(names)
	totalN, totalSame := 0, 0
	for _, k := range names {
		st := stats[k]
		ds := append([]int64{}, st.deltas...)
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		line := fmt.Sprintf("GAS %-34s n=%-4d identical=%-4d delta(min/median/max)=%d/%d/%d", k, st.n, st.same, ds[0], ds[len(ds)/2], ds[len(ds)-1])
		if st.same != st.n {
			line += "\n      e.g. " + st.example
		}
		t.Log(line)
		totalN += st.n
		totalSame += st.same
	}
	t.Logf("GAS SUMMARY comparable=%d identical=%d", totalN, totalSame)
	if os.Getenv("EVM_GAS_REPORT_ONLY") == "" {
		require.Equal(t, totalN, totalSame, "gas differs from go-ethereum")
	}
}
