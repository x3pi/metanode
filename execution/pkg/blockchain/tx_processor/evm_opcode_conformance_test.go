package tx_processor

import (
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
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Opcode-level differential test: random straight-line programs over the pure opcodes (arithmetic, bit
// operations, comparisons, memory, hashing, calldata/code access, logs, storage, stack shuffling, jumps and
// invalid instructions) run on go-ethereum and on the MVM and are compared on success/failure, return data
// (which holds every computed result and a window of memory) and logs.
// EVM_OPCODE_N sets the number of programs (default 60), EVM_OPCODE_SEED the first seed.

type confOp struct {
	name   string
	code   byte
	pops   int
	pushes int
	kind   string // "" arithmetic operands, "mem" (offset, length ...) operands
}

var confOps = []confOp{
	{"ADD", 0x01, 2, 1, ""}, {"MUL", 0x02, 2, 1, ""}, {"SUB", 0x03, 2, 1, ""}, {"DIV", 0x04, 2, 1, ""},
	{"SDIV", 0x05, 2, 1, ""}, {"MOD", 0x06, 2, 1, ""}, {"SMOD", 0x07, 2, 1, ""}, {"ADDMOD", 0x08, 3, 1, ""},
	{"MULMOD", 0x09, 3, 1, ""}, {"EXP", 0x0a, 2, 1, ""}, {"SIGNEXTEND", 0x0b, 2, 1, ""},
	{"LT", 0x10, 2, 1, ""}, {"GT", 0x11, 2, 1, ""}, {"SLT", 0x12, 2, 1, ""}, {"SGT", 0x13, 2, 1, ""},
	{"EQ", 0x14, 2, 1, ""}, {"ISZERO", 0x15, 1, 1, ""}, {"AND", 0x16, 2, 1, ""}, {"OR", 0x17, 2, 1, ""},
	{"XOR", 0x18, 2, 1, ""}, {"NOT", 0x19, 1, 1, ""}, {"BYTE", 0x1a, 2, 1, ""}, {"SHL", 0x1b, 2, 1, ""},
	{"SHR", 0x1c, 2, 1, ""}, {"SAR", 0x1d, 2, 1, ""},
	{"KECCAK256", 0x20, 2, 1, "mem"}, {"CALLDATALOAD", 0x35, 1, 1, "mem"}, {"CALLDATASIZE", 0x36, 0, 1, ""},
	{"CALLDATACOPY", 0x37, 3, 0, "mem"}, {"CODESIZE", 0x38, 0, 1, ""}, {"CODECOPY", 0x39, 3, 0, "mem"},
	{"MLOAD", 0x51, 1, 1, "mem"}, {"MSTORE", 0x52, 2, 0, "memval"}, {"MSTORE8", 0x53, 2, 0, "memval"},
	{"MSIZE", 0x59, 0, 1, ""}, {"MCOPY", 0x5e, 3, 0, "mem"}, {"ADDRESS", 0x30, 0, 1, ""}, {"CALLER", 0x33, 0, 1, ""},
	{"ORIGIN", 0x32, 0, 1, ""}, {"CALLVALUE", 0x34, 0, 1, ""}, {"SELFBALANCE", 0x47, 0, 1, ""},
}

var confEdgeWords = func() []*big.Int {
	one := big.NewInt(1)
	max := new(big.Int).Sub(new(big.Int).Lsh(one, 256), one)
	return []*big.Int{
		big.NewInt(0), big.NewInt(1), big.NewInt(2), big.NewInt(3), big.NewInt(7), big.NewInt(31), big.NewInt(32), big.NewInt(255), big.NewInt(256),
		new(big.Int).Lsh(one, 128), new(big.Int).Lsh(one, 255), new(big.Int).Sub(new(big.Int).Lsh(one, 255), one), max,
		new(big.Int).Sub(max, one), new(big.Int).Lsh(one, 64),
	}
}()

func confPush32(a *confAsm, v *big.Int) {
	b := make([]byte, 32)
	v.FillBytes(b)
	a.op(0x7f)
	a.op(b...)
}

// confOpcodeProgram builds one random program; the returned description lists the instructions used.
func confOpcodeProgram(seed int64, skip map[int]bool) (code []byte, desc string, steps int) {
	r := rand.New(rand.NewSource(seed))
	a := &confAsm{}
	a.op(0x5b) // keep the first byte valid: runtime code starting with 0xEF is rejected at deploy (EIP-3541)
	var names []string
	smallOffsets := []int64{0, 1, 31, 32, 33, 64, 100, 0x100, 0x1ff, 0x2f0}
	smallLens := []int64{0, 1, 2, 31, 32, 33, 64, 96}
	slotBase := 0x2000
	slots := 0
	store := func() {
		a.push(uint64(slotBase + 32*slots))
		a.op(0x52)
		slots++
	}
	n := 1 + r.Intn(14)
	for i := 0; i < n; i++ {
		// Each step draws from its own RNG so removing a step (minimisation) leaves the others unchanged.
		r := rand.New(rand.NewSource(seed*7919 + int64(i)))
		randWord := func() *big.Int {
			if r.Intn(3) == 0 {
				b := make([]byte, 32)
				r.Read(b)
				return new(big.Int).SetBytes(b)
			}
			return confEdgeWords[r.Intn(len(confEdgeWords))]
		}
		c0 := r.Intn(12)
		if skip[i] {
			continue
		}
		switch c := c0; {
		case c < 7: // pure / memory op
			op := confOps[r.Intn(len(confOps))]
			names = append(names, op.name)
			// push operands, last argument first so that the first argument ends on top
			vals := make([]*big.Int, op.pops)
			for k := range vals {
				switch op.kind {
				case "mem":
					if op.name == "KECCAK256" || op.name == "MCOPY" || strings.HasSuffix(op.name, "COPY") {
						// (a, b, len) or (offset, len): the last argument is a length
						if k == op.pops-1 || (op.pops == 2 && k == 1) {
							vals[k] = big.NewInt(smallLens[r.Intn(len(smallLens))])
						} else if k == 1 && (op.name == "CALLDATACOPY" || op.name == "CODECOPY") && r.Intn(2) == 0 {
							vals[k] = randWord() // the source offset may be any 256-bit value: it just reads zeros
						} else {
							vals[k] = big.NewInt(smallOffsets[r.Intn(len(smallOffsets))])
						}
					} else if op.name == "CALLDATALOAD" && r.Intn(2) == 0 {
						vals[k] = randWord()
					} else {
						vals[k] = big.NewInt(smallOffsets[r.Intn(len(smallOffsets))])
					}
				case "memval":
					if k == 0 {
						vals[k] = big.NewInt(smallOffsets[r.Intn(len(smallOffsets))])
					} else {
						vals[k] = randWord()
					}
				default:
					vals[k] = randWord()
				}
			}
			// A zero-length range ignores its offset, which may then be any 256-bit value.
			if op.kind == "mem" && op.pops >= 2 && vals[op.pops-1].Sign() == 0 && r.Intn(2) == 0 &&
				(op.name == "KECCAK256" || op.name == "MCOPY" || strings.HasSuffix(op.name, "COPY")) {
				for k := 0; k < op.pops-1; k++ {
					vals[k] = randWord()
				}
			}
			for k := op.pops - 1; k >= 0; k-- {
				confPush32(a, vals[k])
			}
			a.op(op.code)
			if op.pushes == 1 {
				store()
			}
		case c == 7: // DUPn / SWAPn after k pushes
			names = append(names, "DUP/SWAP")
			k := 1 + r.Intn(17)
			for j := 0; j < k; j++ {
				confPush32(a, randWord())
			}
			if r.Intn(2) == 0 {
				a.op(byte(0x80 + r.Intn(16))) // DUP1..16
			} else {
				a.op(byte(0x90 + r.Intn(16))) // SWAP1..16
			}
			// store the top, then drop everything we pushed so later steps start clean
			store()
			depth := k
			if a.code[len(a.code)-1] != 0x52 {
				depth = k
			}
			for j := 0; j < depth; j++ {
				a.op(0x50)
			}
			a.names = nil
		case c == 8: // LOG0..LOG4
			t := r.Intn(5)
			names = append(names, "LOG"+strconv.Itoa(t))
			for j := t - 1; j >= 0; j-- {
				confPush32(a, randWord())
			}
			logLen := smallLens[r.Intn(len(smallLens))]
			a.push(uint64(logLen))
			if logLen == 0 && r.Intn(2) == 0 {
				confPush32(a, randWord()) // zero-length data: any offset is fine
			} else {
				a.push(uint64(smallOffsets[r.Intn(len(smallOffsets))]))
			}
			a.op(byte(0xa0 + t))
		case c == 9: // SSTORE / SLOAD with arbitrary keys
			names = append(names, "SSTORE/SLOAD")
			key := randWord()
			confPush32(a, randWord())
			confPush32(a, key)
			a.op(0x55)
			confPush32(a, key)
			a.op(0x54)
			store()
			confPush32(a, randWord())
			a.op(0x54) // SLOAD of a (probably) unset key
			store()
		case c == 10: // jumps
			switch r.Intn(4) {
			case 0: // valid JUMP over INVALID
				names = append(names, "JUMP-valid")
				dest := len(a.code) + 3 + 1 + 1
				a.op(0x61, byte(dest>>8), byte(dest), 0x56, 0xfe, 0x5b)
			case 1: // JUMPI taken
				names = append(names, "JUMPI-taken")
				dest := len(a.code) + 3 + 3 + 1 + 1 + 1
				a.op(0x60, 1, 0x61, byte(dest>>8), byte(dest), 0x57, 0xfe, 0x5b)
			case 2: // JUMPI not taken
				names = append(names, "JUMPI-not-taken")
				dest := len(a.code) + 3 + 3 + 1 + 1 + 1
				a.op(0x60, 0, 0x61, byte(dest>>8), byte(dest), 0x57, 0x5b, 0x5b)
			default: // JUMP into PUSH data (invalid destination) or to a non-JUMPDEST
				names = append(names, "JUMP-invalid")
				if r.Intn(2) == 0 {
					dest := len(a.code) + 3 + 1 + 1 + 1 // inside a PUSH1 0x5b operand
					a.op(0x61, byte(dest>>8), byte(dest), 0x56, 0x60, 0x5b)
				} else {
					dest := len(a.code) + 3 + 1
					a.op(0x61, byte(dest>>8), byte(dest), 0x56, 0x00)
				}
			}
		default: // undefined opcode, or a stack underflow
			if r.Intn(2) == 0 {
				bad := []byte{0x0c, 0x0d, 0x0e, 0x0f, 0x1e, 0x1f, 0x21, 0x2f, 0x4b, 0x4f, 0xa5, 0xb0, 0xef, 0xf6, 0xfb, 0xfc}
				names = append(names, "UNDEFINED-OPCODE")
				a.op(bad[r.Intn(len(bad))])
			} else {
				names = append(names, "STACK-UNDERFLOW")
				a.op(confOps[r.Intn(10)].code) // arithmetic op on an empty stack
			}
		}
	}
	a.push(uint64(slotBase + 32*slots))
	a.push(0)
	a.op(0xf3) // RETURN(0, slotBase+32*slots): memory window plus every stored result
	return a.code, strings.Join(names, ","), n
}

func TestEVMOpcodeConformanceAgainstGeth(t *testing.T) {
	n := 60
	if v := os.Getenv("EVM_OPCODE_N"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	seed0 := int64(1)
	if v := os.Getenv("EVM_OPCODE_SEED"); v != "" {
		seed0, _ = strconv.ParseInt(v, 10, 64)
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

	classes := map[string][]string{}
	mism := 0
	type outcome struct {
		g, m     confResult
		diffs    []string
		desc     string
		calldata []byte
		value    int64
		prog     []byte
		steps    int
	}
	eval := func(s int64, skip map[int]bool) outcome {
		prog, desc, steps := confOpcodeProgram(s, skip)
		rr := rand.New(rand.NewSource(s ^ 0x5bd1))
		calldata := [][]byte{nil, {1, 2, 3}, make([]byte, 40)}[rr.Intn(3)]
		value := []int64{0, 5}[rr.Intn(2)]
		runtimes := [][]byte{prog, {0}, {0}, {0}}
		g := confRunGeth(w, runtimes, []int64{50, 0, 0, 0}, value, calldata)
		m := confRunMVMReal(t, w, key, runtimes, []int64{50, 0, 0, 0}, value, calldata)
		return outcome{g: g, m: m, diffs: confCompare(w, g, m), desc: desc, calldata: calldata, value: value, prog: prog, steps: steps}
	}
	classify := func(o outcome) string {
		first := o.diffs[0]
		if strings.HasPrefix(first, "return-data") {
			first = confFirstWordDiff(o.g.ret, o.m.ret)
		}
		if strings.HasPrefix(first, "status") {
			first = fmt.Sprintf("status geth=%v mvm=%v (%s)", o.g.ok, o.m.ok, regexp.MustCompile(`0x[0-9a-f]+`).ReplaceAllString(o.m.failKind, "0x.."))
		}
		return first
	}
	for s := seed0; s < seed0+int64(n); s++ {
		o := eval(s, nil)
		if len(o.diffs) == 0 {
			continue
		}
		mism++
		skip := map[int]bool{}
		want := strings.Fields(classify(o))[0]
		if os.Getenv("EVM_OPCODE_NOMIN") == "" {
			for i := 0; i < o.steps; i++ {
				skip[i] = true
				if e := eval(s, skip); len(e.diffs) == 0 || strings.Fields(classify(e))[0] != want {
					delete(skip, i)
				}
			}
			o = eval(s, skip)
		}
		sig := classify(o)
		classes[sig] = append(classes[sig], fmt.Sprintf("seed=%d geth{ok=%v %s} mvm{ok=%v %s} calldata=%x value=%d [%s] code=%x", s, o.g.ok, o.g.failKind, o.m.ok, o.m.failKind, o.calldata, o.value, o.desc, o.prog))
	}
	keys := make([]string, 0, len(classes))
	for k := range classes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ex := classes[k][0]
		if os.Getenv("EVM_OPCODE_ALL") != "" {
			ex = strings.Join(classes[k], "\n  e.g. ")
		}
		t.Logf("CLASS %q count=%d\n  e.g. %s", k, len(classes[k]), ex)
	}
	t.Logf("SUMMARY programs=%d mismatching=%d classes=%d", n, mism, len(keys))
	if os.Getenv("EVM_OPCODE_REPORT_ONLY") == "" {
		require.Zero(t, mism, "MVM diverges from go-ethereum on %d/%d opcode programs", mism, n)
	}
}

// confFirstWordDiff names the first 32-byte word where two return buffers differ (or the length mismatch).
func confFirstWordDiff(g, m []byte) string {
	if len(g) != len(m) {
		return fmt.Sprintf("return length geth=%d mvm=%d", len(g), len(m))
	}
	for i := 0; i < len(g); i += 32 {
		end := i + 32
		if end > len(g) {
			end = len(g)
		}
		if string(g[i:end]) != string(m[i:end]) {
			return fmt.Sprintf("return word %d differs", i/32)
		}
	}
	return "return equal"
}
