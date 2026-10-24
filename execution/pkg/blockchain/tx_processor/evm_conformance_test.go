package tx_processor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// Differential conformance test: the same random contract programs run on go-ethereum's EVM (the oracle)
// and on the MVM through the Block-STM transaction pipeline. Compared per program: success/failure of the
// top-level call, its return data on success, and the balance and code presence of every involved account.
//
// Known, intentional differences are normalised away (see evmConfNormalise) so a mismatch means a real
// divergence. EVM_CONFORMANCE_N sets the number of programs (default 40), EVM_CONFORMANCE_SEED the first seed.

// confMVMGas is the gas limit of the MVM transactions; scenarios that need to pay for very large code raise it.
var confMVMGas uint64 = 2_000_000

const (
	confContracts = 4
	confMemOut    = 0x100
)

// childInits are the init codes the generated contracts pass to CREATE / CREATE2.
func confChildInits(p []common.Address) [][]byte {
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	okRuntime := common.FromHex("0x600060005360016000f3") // returns the single runtime byte 0x00
	push20 := func(a common.Address) []byte { return append([]byte{0x73}, a.Bytes()...) }
	return [][]byte{
		okRuntime,                                // 0 ok
		common.FromHex("0x60006000fd"),           // 1 revert
		{0x00},                                   // 2 STOP (empty runtime code)
		{0xfe},                                   // 3 INVALID
		common.FromHex("0x60ef60005360016000f3"), // 4 returns code starting with 0xEF
		cat(push20(p[0]), []byte{0xff}),          // 5 selfdestruct to P0
		cat([]byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0}, push20(common.HexToAddress("0x2222222222222222222222222222222222222222")), []byte{0x5a, 0xf1, 0x50}, okRuntime), // 6 calls an EOA, ok
		cat([]byte{0x34, 0x60, 0, 0x55}, okRuntime),                   // 7 stores CALLVALUE in slot 0, ok
		cat([]byte{0x60, 0, 0x60, 0, 0x60, 0, 0xf0, 0x50}, okRuntime), // 8 creates an empty-init account, ok
		{}, // 9 empty init code
	}
}

type confAsm struct {
	code    []byte
	data    []byte   // appended after the body; offsets patched in finish()
	fixups  []int    // positions of PUSH2 operands that hold a data offset
	dataOff []int    // data offset each fixup refers to
	slots   int      // number of 32-byte result slots written
	names   []string // gadget names for diagnostics
}

func (a *confAsm) op(b ...byte) { a.code = append(a.code, b...) }
func (a *confAsm) push(v uint64) {
	a.code = append(a.code, 0x61, byte(v>>8), byte(v))
}
func (a *confAsm) pushAddr(x common.Address) {
	a.code = append(a.code, 0x73)
	a.code = append(a.code, x.Bytes()...)
}

// store pops the top of stack into the next result slot.
func (a *confAsm) store() {
	a.push(uint64(confMemOut + 32*a.slots))
	a.op(0x52)
	a.slots++
}

func (a *confAsm) addData(d []byte) int {
	off := len(a.data)
	a.data = append(a.data, d...)
	return off
}

func (a *confAsm) pushDataOffset(off int) {
	a.fixups = append(a.fixups, len(a.code)+1)
	a.dataOff = append(a.dataOff, off)
	a.push(0)
}

func (a *confAsm) finish() []byte {
	for i, pos := range a.fixups {
		v := len(a.code) + a.dataOff[i]
		a.code[pos], a.code[pos+1] = byte(v>>8), byte(v)
	}
	return append(append([]byte{}, a.code...), a.data...)
}

type confWorld struct {
	sender common.Address
	p      []common.Address // deployed contracts
	eoas   []common.Address // accounts without code (one funded in both engines)
	inits  [][]byte
}

// genContract builds contract number idx. Calls only go to contracts with a higher index (bounded depth).
func confGenContract(r *rand.Rand, w *confWorld, idx int, seed int64, excl map[[2]int]bool) (code []byte, desc string) {
	a := &confAsm{}
	targets := []common.Address{w.eoas[0], w.eoas[1], common.BigToAddress(big.NewInt(2)), common.BigToAddress(big.NewInt(3)), common.BigToAddress(big.NewInt(4))} // two EOAs, sha256 / ripemd160 / identity precompiles
	for j := idx + 1; j < confContracts; j++ {
		targets = append(targets, w.p[j])
	}
	values := []uint64{0, 0, 1, 3, 1000}
	nGadgets := 1 + r.Intn(6)
	if v := os.Getenv("EVM_CONFORMANCE_MAXGADGETS"); v != "" {
		mx, _ := strconv.Atoi(v)
		nGadgets = 1 + r.Intn(mx)
	}
	for g := 0; g < nGadgets; g++ {
		// Each gadget draws from its own RNG so that excluding one (for minimisation) leaves the others unchanged.
		r := rand.New(rand.NewSource(seed*1000003 + int64(idx)*1009 + int64(g)))
		pick := func() common.Address { return targets[r.Intn(len(targets))] }
		pickNoPrecompile := func() common.Address {
			for {
				if x := pick(); !confIsPrecompile(x) {
					return x
				}
			}
		}
		k0 := r.Intn(17)
		if excl[[2]int{idx, g}] {
			continue
		}
		switch k := k0; k {
		case 0, 1, 2, 3: // CALL / STATICCALL / DELEGATECALL / CALLCODE
			kind := []string{"CALL", "STATICCALL", "DELEGATECALL", "CALLCODE"}[k]
			if r.Intn(2) == 0 { // calldata byte so callees can branch on CALLDATASIZE
				a.push(1)
				a.push(0)
				a.op(0x53)
			}
			argSize := uint64(r.Intn(2))
			a.push(32)   // retSize
			a.push(0x80) // retOff
			a.push(argSize)
			a.push(0)
			tgt := pick()
			a.names = append(a.names, kind+"->"+confTargetLabel(w, tgt))
			if kind == "CALL" || kind == "CALLCODE" {
				v := values[r.Intn(len(values))]
				if confIsPrecompile(tgt) {
					v = 0 // known, intentional: value sent to a precompile is not credited to it (see runbook)
				}
				a.push(v)
			}
			a.pushAddr(tgt)
			a.op(0x5a) // GAS
			a.op([]byte{0xf1, 0xfa, 0xf4, 0xf2}[k])
			a.store()
			a.op(0x3d) // RETURNDATASIZE
			a.store()
			a.push(0x80)
			a.op(0x51) // MLOAD returned word
			a.store()
		case 4, 5: // CREATE / CREATE2
			ii := r.Intn(len(w.inits))
			init := w.inits[ii]
			off := a.addData(init)
			a.names = append(a.names, fmt.Sprintf("CREATE%d(init%d)", map[int]int{4: 1, 5: 2}[k], ii))
			if len(init) > 0 {
				a.push(uint64(len(init)))
				a.pushDataOffset(off)
				a.push(0)
				a.op(0x39) // CODECOPY
			}
			if k == 5 {
				a.push(uint64(r.Intn(2))) // salt
			}
			a.push(uint64(len(init)))
			a.push(0)
			a.push(values[r.Intn(len(values))])
			if k == 4 {
				a.op(0xf0)
			} else {
				a.op(0xf5)
			}
			a.store()
		case 6:
			a.names = append(a.names, "SSTORE")
			slot := uint64(r.Intn(3))
			a.push(uint64(1 + r.Intn(9)))
			a.push(slot)
			a.op(0x55)
			a.push(slot)
			a.op(0x54)
			a.store()
		case 7:
			a.names = append(a.names, "TSTORE")
			slot := uint64(r.Intn(3))
			a.push(uint64(1 + r.Intn(9)))
			a.push(slot)
			a.op(0x5d)
			a.push(slot)
			a.op(0x5c)
			a.store()
		case 8:
			a.names = append(a.names, "ENV")
			for _, o := range []byte{0x34, 0x33, 0x30, 0x32, 0x47, 0x36} { // CALLVALUE CALLER ADDRESS ORIGIN SELFBALANCE CALLDATASIZE
				a.op(o)
				a.store()
			}
		case 9:
			a.names = append(a.names, "BALANCE")
			a.pushAddr(w.p[r.Intn(confContracts)])
			a.op(0x31)
			a.store()
		case 10:
			a.names = append(a.names, "EXTCODESIZE")
			a.pushAddr(w.p[r.Intn(confContracts)])
			a.op(0x3b)
			a.store()
		case 11:
			a.names = append(a.names, "EXTCODEHASH")
			a.pushAddr(pickNoPrecompile())
			a.op(0x3f)
			a.store()
		case 12:
			a.names = append(a.names, "LOG1")
			a.push(7)
			a.push(0)
			a.push(0)
			a.op(0xa1)
		case 14: // RETURNDATACOPY of exactly RETURNDATASIZE bytes, then read the first word
			a.names = append(a.names, "RETURNDATACOPY")
			a.op(0x3d, 0x60, 0, 0x61, 0x02, 0x00, 0x3e) // RETURNDATASIZE PUSH1 0 PUSH2 0x200 RETURNDATACOPY
			a.push(0x200)
			a.op(0x51)
			a.store()
		case 15: // RETURNDATACOPY one byte past the end: exceptional halt of this frame
			a.names = append(a.names, "RETURNDATACOPY-OOB")
			a.op(0x3d, 0x60, 1, 0x01, 0x60, 0, 0x61, 0x02, 0x00, 0x3e) // RETURNDATASIZE+1 ...
		case 16: // EXTCODECOPY the first 32 bytes of a contract's code
			a.names = append(a.names, "EXTCODECOPY")
			a.push(32)
			a.push(0)
			a.push(0x200)
			a.pushAddr(w.p[r.Intn(confContracts)])
			a.op(0x3c)
			a.push(0x200)
			a.op(0x51)
			a.store()
		case 13:
			a.names = append(a.names, "SSTORE0")
			a.push(5)
			a.push(0)
			a.op(0x55)
		}
	}
	ending := r.Intn(7)
	endName := []string{"RETURN", "REVERT", "STOP", "FALLOFF", "INVALID", "SELFDESTRUCT", "RETURN"}[ending]
	n := uint64(32 * a.slots)
	switch ending {
	case 0, 6:
		a.push(n)
		a.push(confMemOut)
		a.op(0xf3)
	case 1:
		a.push(n)
		a.push(confMemOut)
		a.op(0xfd)
	case 2:
		a.op(0x00)
	case 3:
		a.op(0x60, 0x01) // PUSH1 1, then run off the end
	case 4:
		a.op(0xfe)
	case 5:
		a.pushAddr(append(append([]common.Address{}, w.p...), w.eoas...)[r.Intn(confContracts+2)])
		a.op(0xff)
	}
	return a.finish(), fmt.Sprintf("P%d[%s;%s]", idx, strings.Join(a.names, ","), endName)
}

type confResult struct {
	gas      uint64   // gas used by the execution (geth: limit - leftover; MVM: receipt gas used)
	logs     []string // emitted logs as "address|topic,topic|data" (successful executions only)
	ok       bool
	ret      []byte
	balance  map[common.Address]string
	hasCode  map[common.Address]bool
	failKind string
}

func confCandidates(w *confWorld) []common.Address {
	set := map[common.Address]bool{}
	for _, a := range w.p {
		set[a] = true
	}
	for _, a := range w.eoas {
		set[a] = true
	}
	for i := int64(1); i <= 10; i++ { // precompiles
		set[common.BigToAddress(big.NewInt(i))] = true
	}
	for _, creator := range w.p {
		for n := uint64(0); n < 4; n++ {
			c := crypto.CreateAddress(creator, n)
			set[c] = true
			set[crypto.CreateAddress(c, 0)] = true
			set[crypto.CreateAddress(c, 1)] = true
		}
		for _, init := range w.inits {
			for s := byte(0); s < 2; s++ {
				var salt [32]byte
				salt[31] = s
				c := crypto.CreateAddress2(creator, salt, crypto.Keccak256(init))
				set[c] = true
			}
		}
	}
	out := make([]common.Address, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

func confRunGeth(w *confWorld, runtimes [][]byte, balances []int64, entryValue int64, calldata []byte) confResult {
	sdb, _ := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	one := uint256.NewInt
	sdb.AddBalance(w.sender, one(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	for i, a := range w.p {
		sdb.SetCode(a, runtimes[i])
		sdb.AddBalance(a, one(uint64(balances[i])), tracing.BalanceChangeUnspecified)
		sdb.SetNonce(a, 1, tracing.NonceChangeUnspecified)
	}
	sdb.AddBalance(w.eoas[0], one(50), tracing.BalanceChangeUnspecified)
	cfg := &runtime.Config{Origin: w.sender, State: sdb, GasLimit: 1_000_000_000_000_000, Value: big.NewInt(entryValue), BlockNumber: big.NewInt(1)}
	ret, left, err := runtime.Call(w.p[0], calldata, cfg)
	var gethLogs []string
	if err == nil {
		for _, l := range sdb.Logs() {
			tp := make([]string, len(l.Topics))
			for i, x := range l.Topics {
				tp[i] = x.Hex()
			}
			gethLogs = append(gethLogs, fmt.Sprintf("%s|%s|%x", l.Address.Hex(), strings.Join(tp, ","), l.Data))
		}
	}
	sdb.Finalise(true) // end of transaction: EIP-6780 deletions take effect
	res := confResult{logs: gethLogs, gas: cfg.GasLimit - left, ok: err == nil, ret: ret, balance: map[common.Address]string{}, hasCode: map[common.Address]bool{}}
	if err != nil {
		res.failKind = err.Error()
	}
	for _, a := range confCandidates(w) {
		res.balance[a] = sdb.GetBalance(a).String()
		res.hasCode[a] = len(sdb.GetCode(a)) > 0
	}
	return res
}

func confDeployInit(runtime []byte) []byte {
	l := len(runtime)
	init := []byte{0x61, byte(l >> 8), byte(l), 0x60, 0x0e, 0x60, 0, 0x39, 0x61, byte(l >> 8), byte(l), 0x60, 0, 0xf3}
	return append(init, runtime...)
}

func TestEVMConformanceAgainstGeth(t *testing.T) {
	n := 40
	if v := os.Getenv("EVM_CONFORMANCE_N"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	seed0 := int64(1)
	if v := os.Getenv("EVM_CONFORMANCE_SEED"); v != "" {
		seed0, _ = strconv.ParseInt(v, 10, 64)
	}
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	w := &confWorld{sender: sender}
	for i := 0; i < confContracts; i++ {
		w.p = append(w.p, crypto.CreateAddress(sender, uint64(1+i)))
	}
	w.eoas = []common.Address{common.HexToAddress("0x1111111111111111111111111111111111111111"), common.HexToAddress("0x2222222222222222222222222222222222222222")}
	w.inits = confChildInits(w.p)

	type evalOut struct {
		descs      []string
		g, m       confResult
		diffs      []string
		entryValue int64
		calldata   []byte
		skipped    bool
	}
	eval := func(s int64, excl map[[2]int]bool) evalOut {
		r := rand.New(rand.NewSource(s))
		var runtimes [][]byte
		var descs []string
		balances := make([]int64, confContracts)
		for i := 0; i < confContracts; i++ {
			c, d := confGenContract(r, w, i, s, excl)
			runtimes = append(runtimes, c)
			descs = append(descs, d)
			balances[i] = int64(10 + r.Intn(90))
		}
		entryValue := []int64{0, 0, 7}[r.Intn(3)]
		calldata := [][]byte{nil, {1}}[r.Intn(2)]
		g := confRunGeth(w, runtimes, balances, entryValue, calldata)
		m := confRunMVMReal(t, w, key, runtimes, balances, entryValue, calldata)
		o := evalOut{descs: descs, g: g, m: m, entryValue: entryValue, calldata: calldata}
		if !g.ok && strings.Contains(g.failKind, "out of gas") && os.Getenv("EVM_CONFORMANCE_KEEP_OOG") == "" {
			// Known gap: MVM has no per-call gas limit / 63-64 rule, so an out-of-gas callee kills the
			// whole tx there but only the callee on Ethereum. These programs are counted and skipped.
			o.skipped = true
			return o
		}
		o.diffs = confCompare(w, g, m)
		return o
	}
	kind := func(d []string) string { return strings.Fields(d[0])[0] }

	mismatches := map[string][]string{}
	total, mism, skippedOOG := 0, 0, 0
	for s := seed0; s < seed0+int64(n); s++ {
		o := eval(s, nil)
		total++
		if o.skipped {
			skippedOOG++
			continue
		}
		if len(o.diffs) == 0 {
			continue
		}
		mism++
		// Greedy minimisation: drop gadgets while the same kind of divergence remains.
		excl := map[[2]int]bool{}
		if os.Getenv("EVM_CONFORMANCE_NOMIN") == "" {
			for idx := 0; idx < confContracts; idx++ {
				for gi := 0; gi < 6; gi++ {
					excl[[2]int{idx, gi}] = true
					if e := eval(s, excl); e.skipped || len(e.diffs) == 0 || kind(e.diffs) != kind(o.diffs) {
						delete(excl, [2]int{idx, gi})
					}
				}
			}
			o = eval(s, excl)
		}
		sig := kind(o.diffs)
		if sig == "status" {
			sig = "status geth=" + fmt.Sprint(o.g.ok) + " (" + o.g.failKind + ") mvm=" + fmt.Sprint(o.m.ok)
		}
		if os.Getenv("EVM_CONFORMANCE_BRIEF") == "" {
			sig += " <- " + regexp.MustCompile(`\(init[0-9]+\)`).ReplaceAllString(o.descs[0], "(initN)")
		}
		mismatches[sig] = append(mismatches[sig], fmt.Sprintf("seed=%d entryValue=%d calldata=%x geth{ok=%v %s} mvm{ok=%v %s} %s | %s", s, o.entryValue, o.calldata, o.g.ok, o.g.failKind, o.m.ok, o.m.failKind, strings.Join(o.descs, " "), strings.Join(o.diffs, "; ")))
	}
	keys := make([]string, 0, len(mismatches))
	for k := range mismatches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ex := mismatches[k][0]
		if os.Getenv("EVM_CONFORMANCE_ALL") != "" {
			ex = strings.Join(mismatches[k], "\n  e.g. ")
		}
		t.Logf("CLASS %q count=%d\n  e.g. %s", k, len(mismatches[k]), ex)
	}
	t.Logf("SUMMARY programs=%d skipped(geth out-of-gas, gas model)=%d mismatching=%d classes=%d", total, skippedOOG, mism, len(keys))
	if os.Getenv("EVM_CONFORMANCE_REPORT_ONLY") == "" {
		require.Zero(t, mism, "MVM diverges from go-ethereum on %d/%d programs", mism, total)
	}
}

func confCompare(w *confWorld, g, m confResult) []string {
	var d []string
	if g.ok != m.ok {
		return []string{fmt.Sprintf("status geth=%v mvm=%v", g.ok, m.ok)}
	}
	if g.ok && strings.Join(g.logs, ";") != strings.Join(m.logs, ";") {
		d = append(d, fmt.Sprintf("logs differ geth=%v mvm=%v", g.logs, m.logs))
	}
	if g.ok && !bytes.Equal(g.ret, m.ret) {
		d = append(d, fmt.Sprintf("return-data differs geth=%x mvm=%x", g.ret, m.ret))
	}
	for _, a := range confCandidates(w) {
		if g.balance[a] != m.balance[a] {
			d = append(d, fmt.Sprintf("balance %s geth=%s mvm=%s", confName(w, a), g.balance[a], m.balance[a]))
		}
		if g.hasCode[a] != m.hasCode[a] {
			d = append(d, fmt.Sprintf("code %s geth=%v mvm=%v", confName(w, a), g.hasCode[a], m.hasCode[a]))
		}
	}
	return d
}

func confIsPrecompile(a common.Address) bool {
	for i := int64(1); i <= 10; i++ {
		if a == common.BigToAddress(big.NewInt(i)) {
			return true
		}
	}
	return false
}

func confTargetLabel(w *confWorld, a common.Address) string {
	if confIsPrecompile(a) {
		return "precompile" + strconv.Itoa(int(a[19]))
	}
	return confName(w, a)
}

func confName(w *confWorld, a common.Address) string {
	for i, p := range w.p {
		if p == a {
			return fmt.Sprintf("P%d", i)
		}
	}
	for i, p := range w.eoas {
		if p == a {
			return fmt.Sprintf("EOA%d", i)
		}
	}
	for i, p := range w.p {
		for n := uint64(0); n < 4; n++ {
			if crypto.CreateAddress(p, n) == a {
				return fmt.Sprintf("P%d.create#%d", i, n)
			}
		}
	}
	return "child"
}

func confRunMVMReal(t *testing.T, w *confWorld, key *ecdsa.PrivateKey, runtimes [][]byte, balances []int64, entryValue int64, calldata []byte) confResult {
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, w.sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, w.eoas[0], big.NewInt(50), 0)
	mk := func(nonce uint64, to *common.Address, val int64, data []byte) types.Transaction {
		s, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{Nonce: nonce, To: to, Value: big.NewInt(val), Gas: confMVMGas, GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: data})
		require.NoError(t, err)
		tx, err := transaction.NewTransactionFromEth(s)
		require.NoError(t, err)
		return tx
	}
	ex := func(tx types.Transaction) types.Receipt {
		items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
		g := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, g, blankHeader(), false, false, 999, leader, false)
		require.Len(t, r, 1)
		return r[0]
	}
	for i := 0; i < confContracts; i++ {
		rc := ex(mk(uint64(1+i), nil, balances[i], confDeployInit(runtimes[i])))
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rc.Status(), "deploy of P%d must succeed (len=%d): %q", i, len(runtimes[i]), rc.Return())
	}
	call := ex(mk(uint64(1+confContracts), &w.p[0], entryValue, calldata))
	res := confResult{gas: call.GasUsed(), ok: call.Status() == pb.RECEIPT_STATUS_RETURNED, ret: call.Return(), balance: map[common.Address]string{}, hasCode: map[common.Address]bool{}}
	if res.ok {
		for _, l := range call.EventLogs() {
			tp := make([]string, len(l.Topics))
			for i, x := range l.Topics {
				tp[i] = common.BytesToHash(x).Hex()
			}
			res.logs = append(res.logs, fmt.Sprintf("%s|%s|%x", common.BytesToAddress(l.Address).Hex(), strings.Join(tp, ","), l.Data))
		}
	}
	if !res.ok {
		res.failKind = strings.Map(func(r rune) rune {
			if r >= 32 && r < 127 {
				return r
			}
			return -1
		}, string(call.Return()))
	}
	for _, a := range confCandidates(w) {
		st, err := cs.GetAccountStateDB().AccountState(a)
		require.NoError(t, err)
		res.balance[a] = st.TotalBalance().String()
		sc := st.SmartContractState()
		// An account whose code hash is the hash of empty code holds no code (Ethereum treats it as code-less).
		res.hasCode[a] = sc != nil && sc.CodeHash() != (common.Hash{}) && sc.CodeHash() != crypto.Keccak256Hash(nil)
	}
	return res
}
