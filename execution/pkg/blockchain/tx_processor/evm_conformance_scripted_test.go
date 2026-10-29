package tx_processor

import (
	"bytes"
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
	"STOP": 0x00, "ADD": 0x01, "CALLDATASIZE": 0x36, "CALLDATACOPY": 0x37, "RETURNDATACOPY": 0x3e, "JUMPI": 0x57, "POP": 0x50, "MLOAD": 0x51, "MSTORE": 0x52, "MSTORE8": 0x53, "SLOAD": 0x54, "SSTORE": 0x55,
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
	gas      uint64 // MVM tx gas limit (default 2M)
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
			name: "stack_1024_items_ok",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{append(bytes.Repeat([]byte{0x60, 0x00}, 1024), 0x00)}
			},
		},
		{
			name: "stack_1025_items_overflow",
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{append(bytes.Repeat([]byte{0x60, 0x00}, 1025), 0x00)}
			},
		},
		{
			name: "calldata_and_code_reads_at_huge_offsets_return_zero",
			build: func(p []common.Address, x common.Address) [4][]byte {
				huge := new(big.Int).Lsh(big.NewInt(1), 200)
				hb := make([]byte, 32)
				huge.FillBytes(hb)
				push32 := append([]byte{0x7f}, hb...)
				var c []byte
				c = append(c, 0x60, 0x07, 0x60, 0x00, 0x52) // mem[0..32) = 7 (to prove it gets overwritten)
				c = append(c, 0x60, 0x20)                   // size 32
				c = append(c, push32...)                    // src offset 2^200
				c = append(c, 0x60, 0x00, 0x37)             // CALLDATACOPY(0, 2^200, 32)
				c = append(c, push32...)                    // CALLDATALOAD(2^200)
				c = append(c, 0x35, 0x60, 0x20, 0x52)       // mem[32..64) = result
				c = append(c, 0x60, 0x20)                   // size 32
				c = append(c, push32...)                    // src offset 2^200
				c = append(c, 0x60, 0x40, 0x39)             // CODECOPY(0x40, 2^200, 32)
				c = append(c, 0x60, 0x60, 0x60, 0x00, 0xf3) // RETURN(0, 96)
				return [4][]byte{c}
			},
			calldata: []byte{1, 2, 3},
		},
		{
			name: "create_runtime_code_24576_bytes_ok",
			gas:  30_000_000,
			build: func(p []common.Address, x common.Address) [4][]byte {
				return confCreateReturningCode(24576)
			},
			balances: [4]int64{10, 0, 0, 0},
		},
		{
			name: "create_runtime_code_24577_bytes_fails_eip170",
			gas:  30_000_000,
			build: func(p []common.Address, x common.Address) [4][]byte {
				return confCreateReturningCode(24577)
			},
			balances:        [4]int64{10, 0, 0, 0},
			knownDivergence: "appchain supports up to 512KB runtime code (EIP-170 limit increased)",
		},
		{
			name: "create_initcode_49152_bytes_ok",
			gas:  30_000_000,
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{49152, 0, 0, "CREATE"}, confReturnWords(1))...)}
			},
		},
		{
			name: "create_initcode_49153_bytes_fails_eip3860",
			gas:  30_000_000,
			build: func(p []common.Address, x common.Address) [4][]byte {
				return [4][]byte{confScript(confJoin([]any{49153, 0, 0, "CREATE"}, confReturnWords(1))...)}
			},
			knownDivergence: "appchain supports up to 1MB initcode (EIP-3860 limit increased)",
		},
		{
			name: "call_depth_limit_1024_unwinds_cleanly",
			build: func(p []common.Address, x common.Address) [4][]byte {
				// P0 calls itself forever; at depth 1024 the CALL fails and every frame returns normally.
				return [4][]byte{confScript(confJoin(confCallSeq("CALL", p[0], 0, 0), []any{"POP", "STOP"})...)}
			},
			gas: 20_000_000,
		},
		{
			name: "zero_length_ranges_ignore_huge_offsets",
			build: func(p []common.Address, x common.Address) [4][]byte {
				huge := new(big.Int).Lsh(big.NewInt(1), 200)
				hb := make([]byte, 32)
				huge.FillBytes(hb)
				h := append([]byte{0x7f}, hb...)
				var c []byte
				c = append(c, 0x60, 0x00) // KECCAK256(2^200, 0): size first, then offset
				c = append(c, h...)
				c = append(c, 0x20, 0x60, 0x00, 0x52) // mem[0] = result
				c = append(c, 0x60, 0x00)             // LOG0(2^200, 0)
				c = append(c, h...)
				c = append(c, 0xa0)
				c = append(c, 0x60, 0x00) // MCOPY(2^200, 2^200, 0)
				c = append(c, h...)
				c = append(c, h...)
				c = append(c, 0x5e)
				c = append(c, 0x60, 0x00, 0x60, 0x00) // CALLDATACOPY(2^200, 0, 0)
				c = append(c, h...)
				c = append(c, 0x37)
				c = append(c, 0x60, 0x00)                   // RETURN(2^200, 0)... after proving the result
				c = append(c, 0x60, 0x20, 0x60, 0x00, 0xf3) // RETURN(0, 32)
				return [4][]byte{c}
			},
		},
		{
			name: "blockhash_of_huge_number_is_zero",
			build: func(p []common.Address, x common.Address) [4][]byte {
				// 2^32 + 5 must not be truncated into block 5
				return [4][]byte{confScript(confJoin([]any{[]byte{0x64, 0x01, 0x00, 0x00, 0x00, 0x05, 0x40}}, confReturnWords(1))...)}
			},
		},
		{
			name: "create2_constructor_stores_callvalue",
			build: func(p []common.Address, x common.Address) [4][]byte {
				init := confChildInits(p)[7] // SSTORE(0, CALLVALUE) then returns one byte of runtime code
				return [4][]byte{confScript(confJoin(confMemWrite(0, init), []any{1, len(init), 0, 3, "CREATE2"}, confReturnWords(1))...)}
			},
			balances: [4]int64{50, 0, 0, 0},
		},
		{
			name: "create_constructor_stores_callvalue",
			build: func(p []common.Address, x common.Address) [4][]byte {
				init := confChildInits(p)[7]
				return [4][]byte{confScript(confJoin(confMemWrite(0, init), []any{len(init), 0, 3, "CREATE"}, confReturnWords(1))...)}
			},
			balances: [4]int64{50, 0, 0, 0},
		},
		{
			name: "create2_twice_empty_runtime_second_collides_eip161",
			build: func(p []common.Address, x common.Address) [4][]byte {
				// The first CREATE2 deploys a contract with no code (its nonce is 1), so the second one collides.
				return [4][]byte{confScript(confJoin(confMemWrite(0, []byte{0x00}), []any{0, 1, 0, 0, "CREATE2", 0, 1, 0, 0, "CREATE2"}, confReturnWords(2))...)}
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
		confMVMGas = 2_000_000
		if sc.gas != 0 {
			confMVMGas = sc.gas
		}
		g := confRunGeth(w, runtimes, balances, sc.value, sc.calldata)
		m := confRunMVMReal(t, w, key, runtimes, balances, sc.value, sc.calldata)
		diffs := confCompare(w, g, m)
		if g.ok && m.ok {
			// the gas of the whole transaction must also match go-ethereum's
			if want := confGethTotalGas(g, sc.calldata); want != m.gas {
				diffs = append(diffs, fmt.Sprintf("gas geth=%d mvm=%d", want, m.gas))
			}
		}
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

// confCreateReturningCode builds a factory that CREATEs a child whose init code returns n zero bytes as its
// runtime code, and returns the CREATE result.
func confCreateReturningCode(n int) [4][]byte {
	init := []byte{0x62, byte(n >> 16), byte(n >> 8), byte(n), 0x60, 0x00, 0xf3} // PUSH3 n, PUSH1 0, RETURN
	return [4][]byte{confScript(confJoin(confMemWrite(0, init), []any{len(init), 0, 0, "CREATE"}, confReturnWords(1))...)}
}
