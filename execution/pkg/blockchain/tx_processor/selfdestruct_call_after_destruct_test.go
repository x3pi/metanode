package tx_processor

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSelfdestructCallAfterDestructSameTx(t *testing.T) {
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
	child := crypto.CreateAddress(w.p[0], 1)
	childInit := confDeployInit(confScript(w.eoas[0], "SELFDESTRUCT"))
	for _, again := range []bool{false, true} {
		name := "one_call_control"
		if again {
			name = "second_call_with_value"
		}
		t.Run(name, func(t *testing.T) {
			// Create a child endowed with 100, call it to selfdestruct, then call it
			// again with 50. Its code must remain executable until transaction end.
			body := confJoin(confMemWrite(0, childInit), []any{len(childInit), 0, 100, "CREATE", "POP"}, confCallSeq("CALL", child, 0, 0), []any{"POP"})
			if again {
				body = confJoin(body, confCallSeq("CALL", child, 50, 0), []any{"POP"})
			}
			body = append(body, "STOP")
			runtimes := [][]byte{confScript(body...), {0}, {0}, {0}}
			confMVMGas = 2_000_000
			g := confRunGeth(w, runtimes, []int64{200, 0, 0, 0}, 0, nil)
			m := confRunMVMReal(t, w, key, runtimes, []int64{200, 0, 0, 0}, 0, nil)
			require.True(t, g.ok, g.failKind)
			require.True(t, m.ok, m.failKind)
			t.Logf("geth recipient=%s child=%s; MVM recipient=%s child=%s", g.balance[w.eoas[0]], g.balance[child], m.balance[w.eoas[0]], m.balance[child])
			require.Empty(t, confCompare(w, g, m))
		})
	}
}
