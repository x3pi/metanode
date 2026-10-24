package tx_processor

import (
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Per-opcode gas: every single instruction (with small operands) is run alone and its gas must equal go-ethereum's.
func TestEVMOpcodeGasTableAgainstGeth(t *testing.T) {
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

	// operand sets chosen to avoid out-of-range cases; memory operands stay small
	ops := append([]confOp{}, confOps...)
	ops = append(ops, confOp{"POP", 0x50, 1, 0, ""}, confOp{"PC", 0x58, 0, 1, ""}, confOp{"GAS", 0x5a, 0, 1, ""},
		confOp{"JUMPDEST", 0x5b, 0, 0, ""}, confOp{"PUSH0", 0x5f, 0, 1, ""}, confOp{"CODESIZE", 0x38, 0, 1, ""},
		confOp{"GASPRICE", 0x3a, 0, 1, ""}, confOp{"RETURNDATASIZE", 0x3d, 0, 1, ""}, confOp{"CHAINID", 0x46, 0, 1, ""},
		confOp{"BASEFEE", 0x48, 0, 1, ""}, confOp{"COINBASE", 0x41, 0, 1, ""}, confOp{"TIMESTAMP", 0x42, 0, 1, ""},
		confOp{"NUMBER", 0x43, 0, 1, ""}, confOp{"GASLIMIT", 0x45, 0, 1, ""}, confOp{"BLOCKHASH", 0x40, 1, 1, ""},
		confOp{"TLOAD", 0x5c, 1, 1, ""}, confOp{"SLOAD", 0x54, 1, 1, ""}, confOp{"BLOBBASEFEE", 0x4a, 0, 1, ""},
		confOp{"BLOBHASH", 0x49, 1, 1, ""})
	skip := map[string]bool{"GAS": true, "COINBASE": true, "TIMESTAMP": true, "NUMBER": true, "GASLIMIT": true,
		"CHAINID": true, "BASEFEE": true, "GASPRICE": true, "BLOCKHASH": true, "BLOBBASEFEE": true, "BLOBHASH": true}
	var bad []string
	for _, op := range ops {
		if skip[op.name] {
			continue
		}
		a := &confAsm{}
		for k := op.pops - 1; k >= 0; k-- {
			v := big.NewInt(int64(3 + k))
			if op.kind == "mem" || op.kind == "memval" {
				v = big.NewInt(int64([]int{32, 0, 64}[k%3]))
			}
			confPush32(a, v)
		}
		a.op(op.code)
		for i := 0; i < op.pushes; i++ {
			a.op(0x50) // POP the result: a constant 2 gas on both sides
		}
		a.op(0x00)
		runtimes := [][]byte{a.code, {0}, {0}, {0}}
		g := confRunGeth(w, runtimes, []int64{50, 0, 0, 0}, 0, nil)
		m := confRunMVMReal(t, w, key, runtimes, []int64{50, 0, 0, 0}, 0, nil)
		if g.ok != m.ok {
			bad = append(bad, fmt.Sprintf("%s: status geth=%v mvm=%v", op.name, g.ok, m.ok))
			continue
		}
		geth := confGethTotalGas(g, nil)
		if m.gas != geth {
			bad = append(bad, fmt.Sprintf("%s: geth=%d mvm=%d (delta %d)", op.name, geth, m.gas, int64(m.gas)-int64(geth)))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Logf("GAS TABLE MISMATCHES:\n  %s", strings.Join(bad, "\n  "))
	}
	if os.Getenv("EVM_GAS_REPORT_ONLY") == "" {
		require.Empty(t, bad, "opcode gas differs from go-ethereum")
	}
}
