package tx_processor

import (
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// confAsmOps maps the mnemonics used by the scripted conformance scenarios to opcodes.
var confAsmOps = map[string]byte{
	"STOP": 0x00, "RETURNDATACOPY": 0x3e, "JUMPI": 0x57, "POP": 0x50, "MLOAD": 0x51, "MSTORE": 0x52, "MSTORE8": 0x53, "SLOAD": 0x54, "SSTORE": 0x55,
	"CALLER": 0x33, "CALLVALUE": 0x34, "ADDRESS": 0x30, "BALANCE": 0x31, "SELFBALANCE": 0x47, "CODECOPY": 0x39,
	"EXTCODESIZE": 0x3b, "EXTCODEHASH": 0x3f, "RETURNDATASIZE": 0x3d, "GAS": 0x5a, "TLOAD": 0x5c, "TSTORE": 0x5d,
	"CREATE": 0xf0, "CALL": 0xf1, "CALLCODE": 0xf2, "RETURN": 0xf3, "DELEGATECALL": 0xf4, "CREATE2": 0xf5,
	"STATICCALL": 0xfa, "REVERT": 0xfd, "INVALID": 0xfe, "SELFDESTRUCT": 0xff,
}

// confScript assembles a program from mnemonics, uint64 pushes (PUSH2) and common.Address pushes (PUSH20).
func confScript(items ...any) []byte {
	var out []byte
	for _, it := range items {
		switch v := it.(type) {
		case string:
			op, ok := confAsmOps[v]
			if !ok {
				panic("unknown mnemonic " + v)
			}
			out = append(out, op)
		case int:
			out = append(out, 0x61, byte(v>>8), byte(v))
		case common.Address:
			out = append(out, 0x73)
			out = append(out, v.Bytes()...)
		case []byte:
			out = append(out, v...)
		default:
			panic(fmt.Sprintf("bad item %T", it))
		}
	}
	return out
}

// confCallSeq pushes the arguments of CALL / CALLCODE (withValue) or DELEGATECALL / STATICCALL and runs it,
// leaving the success flag on the stack. Return data goes to memory 0x80..0xa0.
func confCallSeq(op string, target common.Address, value, argSize int) []any {
	s := []any{32, 0x80, argSize, 0}
	if op == "CALL" || op == "CALLCODE" {
		s = append(s, value)
	}
	return append(s, target, "GAS", op)
}

// confMemWrite stores data at memory[offset..] one byte at a time.
func confMemWrite(offset int, data []byte) []any {
	var s []any
	for i, b := range data {
		s = append(s, int(b), offset+i, "MSTORE8")
	}
	return s
}

func confJoin(parts ...[]any) []any {
	var out []any
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// confReturnWords stores the stack-top values (top first) into memory 0x100.. and returns them.
func confReturnWords(n int) []any {
	var s []any
	for i := 0; i < n; i++ {
		s = append(s, 0x100+32*i, "MSTORE")
	}
	return append(s, 32*n, 0x100, "RETURN")
}

type confScenario struct {
	name     string
	build    func(p []common.Address, x common.Address) [4][]byte // contract runtimes; x = funded EOA0
	balances [4]int64
	value    int64
	calldata []byte
	// knownDivergence, when set, marks an intentional difference from Ethereum (documented in the runbook):
	// a mismatch is reported as KNOWN instead of failing, and an unexpected match is flagged.
	knownDivergence string
}

func confScenarios() []confScenario {
	return []confScenario{
		{
			name: "callcode_selfdestruct_then_selfdestruct",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{
					confScript(confJoin(confCallSeq("CALLCODE", p[1], 0, 0), []any{"POP", x, "SELFDESTRUCT"})...),
					confScript(x, "SELFDESTRUCT"),
				}
			},
			balances: [4]int64{50, 5, 0, 0}, value: 7,
		},
		{
			name:            "call_precompile_identity_with_value",
			knownDivergence: "value sent to a precompile is not credited to it (cross-chain precompile handles value itself)",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("CALL", common.BigToAddress(big.NewInt(4)), 3, 1), confReturnWords(1))...)}
			},
			balances: [4]int64{50, 0, 0, 0},
		},
		{
			name: "call_precompile_identity_returns_input",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{1, 0, "MSTORE8"}, confCallSeq("STATICCALL", common.BigToAddress(big.NewInt(4)), 0, 1), []any{"POP", 0x80, "MLOAD"}, confReturnWords(1))...)}
			},
			calldata: []byte{1},
		},
		{
			name: "extcodesize_extcodehash_of_eoa_and_missing",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{x, "EXTCODEHASH", x, "EXTCODESIZE", common.HexToAddress("0x2222222222222222222222222222222222222222"), "EXTCODEHASH"}, confReturnWords(3))...)}
			},
		},
		{
			name:            "extcodehash_of_precompile",
			knownDivergence: "precompiles report code size 1 so Solidity's extcodesize check passes for chain-specific precompiles",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{common.BigToAddress(big.NewInt(4)), "EXTCODEHASH", common.BigToAddress(big.NewInt(4)), "EXTCODESIZE"}, confReturnWords(2))...)}
			},
		},
		{
			name: "call_insufficient_balance_continues",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("CALL", x, 1000, 0), []any{"RETURNDATASIZE"}, confReturnWords(2))...)}
			},
			balances: [4]int64{10, 0, 0, 0},
		},
		{
			name: "create_insufficient_balance_keeps_nonce",
			build: func(p []common.Address, x common.Address) [4][]byte {
				// CREATE(1000) fails (0) and must not bump the nonce, so the next CREATE(0) lands at nonce 0.
				return [4][]byte{confScript(confJoin([]any{0, 0, 1000, "CREATE", 0, 0, 0, "CREATE"}, confReturnWords(2))...)}
			},
			balances: [4]int64{10, 0, 0, 0},
		},
		{
			name: "call_identity_zero_input_1byte",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("STATICCALL", common.BigToAddress(big.NewInt(4)), 0, 1), []any{"POP", 0x80, "MLOAD", "RETURNDATASIZE"}, confReturnWords(2))...)}
			},
		},
		{
			name: "call_identity_no_input",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("STATICCALL", common.BigToAddress(big.NewInt(4)), 0, 0), []any{"POP", 0x80, "MLOAD", "RETURNDATASIZE"}, confReturnWords(2))...)}
			},
		},
		{
			name: "call_eoa_keeps_return_memory",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{5, 0x80, "MSTORE"}, confCallSeq("CALL", x, 0, 0), []any{"POP", 0x80, "MLOAD", "RETURNDATASIZE"}, confReturnWords(2))...)}
			},
		},
		{
			name: "call_reverting_callee_returndata",
			build: func(p []common.Address, x common.Address) [4][]byte {
				callee := confScript(confJoin([]any{9, 0, "MSTORE", 32, 0, "REVERT"})...)
				return [4][]byte{confScript(confJoin(confCallSeq("CALL", p[1], 0, 0), []any{0x80, "MLOAD", "RETURNDATASIZE"}, confReturnWords(3))...), callee}
			},
		},
		{
			name: "delegatecall_selfdestruct_to_self_then_selfdestruct",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{
					confScript(confJoin(confCallSeq("DELEGATECALL", p[1], 0, 0), []any{"POP", x, "SELFDESTRUCT"})...),
					confScript("ADDRESS", "SELFDESTRUCT"),
				}
			},
			balances: [4]int64{50, 5, 0, 0}, value: 7,
		},
		{
			name: "selfdestruct_to_self_preexisting_contract_keeps_balance",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript("ADDRESS", "SELFDESTRUCT")}
			},
			balances: [4]int64{50, 0, 0, 0}, value: 7,
		},
		{
			name: "selfdestruct_to_self_created_same_tx_burns",
			build: func(p []common.Address, x common.Address) [4][]byte {
				init := confScript("ADDRESS", "SELFDESTRUCT")
				return [4][]byte{confScript(confJoin(confMemWrite(0, init), []any{len(init), 0, 100, "CREATE"}, confReturnWords(1))...)}
			},
			balances: [4]int64{200, 0, 0, 0},
		},
		{
			name: "call_returning_less_than_retsize_keeps_rest_of_memory",
			build: func(p []common.Address, x common.Address) [4][]byte {
				callee := confScript(confJoin(confMemWrite(0, []byte{0xAA}), []any{1, 0, "RETURN"})...) // returns 1 byte
				return [4][]byte{confScript(confJoin([]any{5, 0x80, "MSTORE"}, confCallSeq("CALL", p[1], 0, 0), []any{"POP", 0x80, "MLOAD", "RETURNDATASIZE"}, confReturnWords(2))...), callee}
			},
		},
		{
			name: "callee_runs_off_end_keeps_return_memory",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{5, 0x80, "MSTORE"}, confCallSeq("CALL", p[1], 0, 0), []any{"POP", 0x80, "MLOAD"}, confReturnWords(1))...), confScript(1)}
			},
		},
		{
			name: "failed_call_clears_returndata",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("CALL", p[1], 0, 0), []any{"POP", "RETURNDATASIZE"}, confReturnWords(1))...), confScript("INVALID")}
			},
		},
		{
			name: "call_self_with_value_moves_nothing",
			build: func(p []common.Address, x common.Address) [4][]byte {
				// Entered with value 0 it calls itself with value 3; entered with value != 0 it just stops.
				body := confScript(confJoin(confCallSeq("CALL", p[0], 3, 0), confReturnWords(1))...)
				head := confScript("CALLVALUE", 5+len(body), "JUMPI")
				code := append(append(head, body...), 0x5b, 0x00) // JUMPDEST STOP
				return [4][]byte{code}
			},
			balances: [4]int64{50, 0, 0, 0},
		},
		{
			name: "create_clears_returndata_and_exposes_ctor_revert_data",
			build: func(p []common.Address, x common.Address) [4][]byte {
				// ctor: MSTORE(0, 9); REVERT(0, 32)
				init := confScript(confJoin(confMemWrite(0, nil), []any{9, 0, "MSTORE", 32, 0, "REVERT"})...)
				return [4][]byte{confScript(confJoin(confMemWrite(0, init), []any{len(init), 0, 0, "CREATE", "RETURNDATASIZE", 0x100, "MSTORE", 0x120, "MSTORE", 0x40, 0x100, "RETURN"})...)}
			},
		},
		{
			name: "create_after_call_clears_returndata",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("STATICCALL", common.BigToAddress(big.NewInt(4)), 0, 1), []any{"POP", 0, 0, 0, "CREATE", "POP", "RETURNDATASIZE"}, confReturnWords(1))...)}
			},
		},
		{
			name: "returndatacopy_out_of_bounds_halts_frame",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("CALL", p[1], 0, 0), []any{"POP", 1, 0, 0x200, "RETURNDATACOPY"}, confReturnWords(0))...), confScript(confJoin(confMemWrite(0, nil), []any{0, 0, "RETURN"})...)}
			},
		},
		{
			name: "root_runs_off_end",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(1)}
			},
		},
		{
			name: "callee_runs_off_end",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin(confCallSeq("CALL", p[1], 0, 0), confReturnWords(1))...), confScript(1)}
			},
		},
		{
			name: "staticcall_callcode_with_value_allowed",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{
					confScript(confJoin(confCallSeq("STATICCALL", p[1], 0, 0), confReturnWords(1))...),
					confScript(confJoin(confCallSeq("CALLCODE", x, 1, 0), confReturnWords(1))...),
				}
			},
			balances: [4]int64{0, 10, 0, 0},
		},
		{
			name: "staticcall_call_with_value_fails",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{
					confScript(confJoin(confCallSeq("STATICCALL", p[1], 0, 0), confReturnWords(1))...),
					confScript(confJoin(confCallSeq("CALL", x, 1, 0), confReturnWords(1))...),
				}
			},
			balances: [4]int64{0, 10, 0, 0},
		},
		{
			name: "constructor_stop_leaves_no_code",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{1, 0, 100, "CREATE"}, confReturnWords(1))...)}
			},
			balances: [4]int64{200, 0, 0, 0},
		},
		{
			name: "selfdestruct_in_constructor_same_tx",
			build: func(p []common.Address, x common.Address) [4][]byte {
				init := confScript(x, "SELFDESTRUCT")
				return [4][]byte{confScript(confJoin(confMemWrite(0, init), []any{len(init), 0, 100, "CREATE"}, confReturnWords(1))...)}
			},
			balances: [4]int64{200, 0, 0, 0},
		},
	}
}

func TestEVMConformanceScripted(t *testing.T) {
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
	only := os.Getenv("EVM_CONFORMANCE_SCENARIO")
	bad := 0
	for _, sc := range confScenarios() {
		if only != "" && !strings.Contains(sc.name, only) {
			continue
		}
		rt := sc.build(w.p, w.eoas[0])
		runtimes := make([][]byte, confContracts)
		for i := range runtimes {
			if rt[i] == nil {
				rt[i] = []byte{0x00}
			}
			runtimes[i] = rt[i]
		}
		balances := sc.balances[:]
		g := confRunGeth(w, runtimes, balances, sc.value, sc.calldata)
		m := confRunMVMReal(t, w, key, runtimes, balances, sc.value, sc.calldata)
		diffs := confCompare(w, g, m)
		if len(diffs) == 0 {
			if sc.knownDivergence != "" {
				t.Logf("NOW-MATCHES %-45s (was a known divergence: %s) - update the scenario and the runbook", sc.name, sc.knownDivergence)
			} else {
				t.Logf("MATCH    %-48s ok=%v ret=%x", sc.name, g.ok, g.ret)
			}
			continue
		}
		if sc.knownDivergence != "" {
			t.Logf("KNOWN    %-48s %s", sc.name, sc.knownDivergence)
			continue
		}
		bad++
		t.Logf("MISMATCH %-48s geth{ok=%v %s} mvm{ok=%v %s} :: %s", sc.name, g.ok, g.failKind, m.ok, m.failKind, strings.Join(diffs, "; "))
	}
	if os.Getenv("EVM_CONFORMANCE_REPORT_ONLY") == "" {
		require.Zero(t, bad, "%d scripted scenarios diverge from go-ethereum", bad)
	}
}
