package tx_processor

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestFailedLogCannotLeakIntoReceipt(t *testing.T) {
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

	// LOG0 (64 bytes): PUSH1 0x40, PUSH1 0x00, LOG0, STOP
	log0Code := common.FromHex("60406000a000")

	// LOG1 (32 bytes): PUSH1 0xaa (topic), PUSH1 0x20, PUSH1 0x00, LOG1, STOP
	log1Code := common.FromHex("60aa60206000a100")

	// LOG4 (32 bytes, 4 topics): PUSH1 0x44, PUSH1 0x33, PUSH1 0x22, PUSH1 0x11, PUSH1 0x20, PUSH1 0x00, LOG4, STOP
	log4Code := common.FromHex("604460336022601160206000a400")

	cases := []struct {
		name         string
		gas          int
		calleeCode   []byte
		expectCallOk bool
		expectLogs   int
	}{
		{"LOG0_enough_gas_control", 2000, log0Code, true, 1},
		{"LOG0_out_of_gas", 600, log0Code, false, 0},
		{"LOG1_enough_gas", 3000, log1Code, true, 1},
		{"LOG1_out_of_gas", 500, log1Code, false, 0},
		{"LOG4_enough_gas", 5000, log4Code, true, 1},
		{"LOG4_out_of_gas", 1000, log4Code, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A catches B's failure and returns CALL's success bit.
			code := confScript(confJoin([]any{0, 0, 0, 0, 0, w.p[1], tc.gas, "CALL"}, confReturnWords(1))...)
			runtimes := [][]byte{code, tc.calleeCode, {0}, {0}}
			confMVMGas = 2_000_000
			g := confRunGeth(w, runtimes, []int64{0, 0, 0, 0}, 0, nil)
			m := confRunMVMReal(t, w, key, runtimes, []int64{0, 0, 0, 0}, 0, nil)
			require.True(t, g.ok, g.failKind)
			require.True(t, m.ok, m.failKind)
			require.Equal(t, tc.expectLogs, len(g.logs), "geth log count mismatch")
			require.Equal(t, tc.expectLogs, len(m.logs), "mvm log count mismatch")
			t.Logf("gas=%d geth CALL=%x logs=%d; mvm CALL=%x logs=%d", tc.gas, g.ret, len(g.logs), m.ret, len(m.logs))
			require.Empty(t, confCompare(w, g, m))
		})
	}
}

func TestNestedCallLogRollbackOnRevert(t *testing.T) {
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

	// Contract C (w.p[2]): Emits LOG1 (topic 0xcc), then REVERTS
	// PUSH1 0xcc, PUSH1 0x20, PUSH1 0x00, LOG1, PUSH1 0x00, PUSH1 0x00, REVERT
	cCode := common.FromHex("60cc60206000a160006000fd")

	// Contract B (w.p[1]): Emits LOG1 (topic 0xbb), calls C with 2000 gas, ignores return, finishes
	// 1. PUSH1 0xbb, PUSH1 0x20, PUSH1 0x00, LOG1 (0x60bb60206000a1)
	// 2. CALL C with 2,000 gas
	// 3. POP result, STOP
	bScript := append(common.FromHex("60bb60206000a1"), confScript(
		0, 0, 0, 0, 0, w.p[2], 2000, "CALL", "POP", 0, 0, "RETURN",
	)...)

	// Contract A (w.p[0]): Calls B with 30,000 gas, returns CALL status
	aScript := confScript(confJoin(
		[]any{0, 0, 0, 0, 0, w.p[1], 30000, "CALL"},
		confReturnWords(1),
	)...)

	runtimes := [][]byte{aScript, bScript, cCode, {0}}
	confMVMGas = 2_000_000
	g := confRunGeth(w, runtimes, []int64{0, 0, 0, 0}, 0, nil)
	m := confRunMVMReal(t, w, key, runtimes, []int64{0, 0, 0, 0}, 0, nil)

	require.True(t, g.ok, g.failKind)
	require.True(t, m.ok, m.failKind)
	// B's log must be preserved, C's log must be rolled back!
	require.Equal(t, 1, len(g.logs), "geth should have exactly 1 log from B")
	require.Equal(t, 1, len(m.logs), "MVM should have exactly 1 log from B")
	t.Logf("geth logs=%d, MVM logs=%d", len(g.logs), len(m.logs))
	require.Empty(t, confCompare(w, g, m))
}

func TestStaticCallCannotEmitLog(t *testing.T) {
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

	// Contract B (w.p[1]): attempts to emit LOG0
	bCode := common.FromHex("60406000a000")

	// Contract A (w.p[0]): calls B via STATICCALL
	// STATICCALL: inOffset, inSize, retOffset, retSize, target, gas -> ret
	// stack: [retSize, retOffset, inSize, inOffset, target, gas]
	aScript := confScript(confJoin(
		[]any{0, 0, 0, 0, w.p[1], 30000, "STATICCALL"},
		confReturnWords(1),
	)...)

	runtimes := [][]byte{aScript, bCode, {0}, {0}}
	confMVMGas = 2_000_000
	g := confRunGeth(w, runtimes, []int64{0, 0, 0, 0}, 0, nil)
	m := confRunMVMReal(t, w, key, runtimes, []int64{0, 0, 0, 0}, 0, nil)

	require.True(t, g.ok, g.failKind)
	require.True(t, m.ok, m.failKind)
	require.Equal(t, 0, len(g.logs), "geth should have 0 logs")
	require.Equal(t, 0, len(m.logs), "MVM should have 0 logs")
	require.Empty(t, confCompare(w, g, m))
}
