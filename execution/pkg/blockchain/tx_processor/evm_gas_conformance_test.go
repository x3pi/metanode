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

// TestEVMGasAgainstGeth compares the gas used by the same programs on go-ethereum and the MVM. It is an
// information report (gas is a consensus rule here, but the MVM has a deliberately different gas model: no
// per-call gas limit, no 63/64 rule): it prints, per first gadget of the entry contract, how often the
// execution gas is identical and the typical difference. EVM_GAS_N sets the number of programs (default 150);
// EVM_GAS_STRICT=1 turns identical-gas into an assertion.
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

	// Calibration: gas of an empty program, which isolates the intrinsic gas charged on top by the tx pipeline.
	stop := [][]byte{{0x00}, {0x00}, {0x00}, {0x00}}
	calib := map[string]int64{}
	for _, cd := range [][]byte{nil, {1}} {
		g := confRunGeth(w, stop, []int64{10, 10, 10, 10}, 0, cd)
		m := confRunMVMReal(t, w, key, stop, []int64{10, 10, 10, 10}, 0, cd)
		calib[fmt.Sprint(len(cd))] = int64(m.gas) - int64(g.gas)
	}
	t.Logf("calibration (MVM - geth, empty program): no calldata=%d, 1 byte=%d", calib["0"], calib["1"])

	type stat struct {
		n, same int
		deltas  []int64
	}
	stats := map[string]*stat{}
	for s := int64(1); s <= int64(n); s++ {
		r := rand.New(rand.NewSource(s))
		var runtimes [][]byte
		var descs []string
		balances := make([]int64, confContracts)
		os.Setenv("EVM_CONFORMANCE_MAXGADGETS", "1")
		for i := 0; i < confContracts; i++ {
			c, d := confGenContract(r, w, i, s+777, nil)
			runtimes = append(runtimes, c)
			descs = append(descs, d)
			balances[i] = int64(10 + r.Intn(90))
		}
		cd := [][]byte{nil, {1}}[r.Intn(2)]
		g := confRunGeth(w, runtimes, balances, 0, cd)
		m := confRunMVMReal(t, w, key, runtimes, balances, 0, cd)
		if !g.ok || !m.ok {
			continue
		}
		name := descs[0][strings.Index(descs[0], "[")+1:]
		name = strings.SplitN(name, ";", 2)[0]
		name = strings.SplitN(name, "->", 2)[0]
		name = strings.SplitN(name, "(", 2)[0]
		st := stats[name]
		if st == nil {
			st = &stat{}
			stats[name] = st
		}
		delta := int64(m.gas) - int64(g.gas) - calib[fmt.Sprint(len(cd))]
		st.n++
		if delta == 0 {
			st.same++
		}
		st.deltas = append(st.deltas, delta)
	}
	os.Unsetenv("EVM_CONFORMANCE_MAXGADGETS")
	names := make([]string, 0, len(stats))
	for k := range stats {
		names = append(names, k)
	}
	sort.Strings(names)
	totalN, totalSame := 0, 0
	for _, k := range names {
		st := stats[k]
		sort.Slice(st.deltas, func(i, j int) bool { return st.deltas[i] < st.deltas[j] })
		t.Logf("GAS %-16s n=%-4d identical=%-4d delta(min/median/max)=%d/%d/%d", k, st.n, st.same, st.deltas[0], st.deltas[len(st.deltas)/2], st.deltas[len(st.deltas)-1])
		totalN += st.n
		totalSame += st.same
	}
	t.Logf("GAS SUMMARY comparable=%d identical=%d", totalN, totalSame)
	if os.Getenv("EVM_GAS_STRICT") != "" {
		require.Equal(t, totalN, totalSame, "gas differs from go-ethereum")
	}
}
