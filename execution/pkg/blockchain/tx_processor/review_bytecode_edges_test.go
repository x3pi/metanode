package tx_processor

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestReviewBytecodeTerminationAndUntakenJump(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	w := &confWorld{sender: crypto.PubkeyToAddress(key.PublicKey)}
	for i := 0; i < confContracts; i++ {
		w.p = append(w.p, crypto.CreateAddress(w.sender, uint64(1+i)))
	}
	w.eoas = []common.Address{
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		common.HexToAddress("0x2222222222222222222222222222222222222222"),
	}
	w.inits = confChildInits(w.p)

	// untaken JUMPI: condition is 0, destination is huge (> 64-bit)
	// stack: [huge, 0] -> JUMPI -> pops dest, pops cond -> cond==0 -> no jump
	hugeJumpUntaken := append([]byte{0x60, 0, 0x7f, 1}, make([]byte, 31)...)
	hugeJumpUntaken = append(hugeJumpUntaken, 0x57, 0x00)

	cases := []struct {
		name string
		tail []byte
	}{
		{"full_PUSH2_control", []byte{0x61, 0x01, 0x00}},
		{"truncated_PUSH1_empty", []byte{0x60}},
		{"truncated_PUSH2", []byte{0x61, 0x01}},
		{"truncated_PUSH4", []byte{0x63, 0x12, 0x34}},
		{"truncated_PUSH32", []byte{0x7f, 0xab}},
		{"PUSH0_at_end", []byte{0x5f}},
		{"untaken_JUMPI_small_control", []byte{0x60, 0, 0x60, 0xff, 0x57, 0}},
		{"untaken_JUMPI_large", hugeJumpUntaken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// First transfer 1 wei, then execute the edge-case tail.
			// Legal EVM programs must preserve the earlier successful transfer.
			code := confScript(confJoin(confCallSeq("CALL", w.eoas[0], 1, 0), []any{"POP"})...)
			code = append(code, tc.tail...)
			runtimes := [][]byte{code, {0}, {0}, {0}}
			confMVMGas = 2_000_000
			g := confRunGeth(w, runtimes, []int64{10, 0, 0, 0}, 0, nil)
			m := confRunMVMReal(t, w, key, runtimes, []int64{10, 0, 0, 0}, 0, nil)
			t.Logf("geth success=%v recipient=%s; MVM success=%v recipient=%s error=%s", g.ok, g.balance[w.eoas[0]], m.ok, m.balance[w.eoas[0]], m.failKind)
			require.True(t, g.ok, g.failKind)
			require.Empty(t, confCompare(w, g, m))
		})
	}
}

func TestTakenJumpDestinationHugeParity(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	w := &confWorld{sender: crypto.PubkeyToAddress(key.PublicKey)}
	for i := 0; i < confContracts; i++ {
		w.p = append(w.p, crypto.CreateAddress(w.sender, uint64(1+i)))
	}
	w.eoas = []common.Address{
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
	}
	w.inits = confChildInits(w.p)

	// JUMP to huge destination (>64-bit) -> must fail on both geth and MVM
	hugeJumpUnconditional := append([]byte{0x7f, 1}, make([]byte, 31)...)
	hugeJumpUnconditional = append(hugeJumpUnconditional, 0x56) // JUMP

	// JUMPI to huge destination (>64-bit) with condition=1 -> must fail on both geth and MVM
	hugeJUMPI := append([]byte{0x60, 1, 0x7f, 1}, make([]byte, 31)...) // PUSH1 1, PUSH32 0x0100...
	hugeJUMPI = append(hugeJUMPI, 0x57)                                  // JUMPI

	cases := []struct {
		name string
		tail []byte
	}{
		{"taken_JUMP_large", hugeJumpUnconditional},
		{"taken_JUMPI_large", hugeJUMPI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := confScript(confJoin(confCallSeq("CALL", w.eoas[0], 1, 0), []any{"POP"})...)
			code = append(code, tc.tail...)
			runtimes := [][]byte{code, {0}, {0}, {0}}
			confMVMGas = 2_000_000
			g := confRunGeth(w, runtimes, []int64{10, 0, 0, 0}, 0, nil)
			m := confRunMVMReal(t, w, key, runtimes, []int64{10, 0, 0, 0}, 0, nil)
			t.Logf("geth success=%v err=%s; MVM success=%v err=%s", g.ok, g.failKind, m.ok, m.failKind)
			require.False(t, g.ok, "geth should fail on invalid jump destination")
			require.False(t, m.ok, "MVM should fail on invalid jump destination")
			require.Empty(t, confCompare(w, g, m))
		})
	}
}
