package tx_processor

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSelfdestructForceFundDestroyedAccount(t *testing.T) {
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
	for _, force := range []bool{false, true} {
		name := "no_force_funding_control"
		if force {
			name = "force_funded_after_destruction"
		}
		t.Run(name, func(t *testing.T) {
			// After the new child selfdestructs, another preexisting contract
			// force-sends its balance to that child using SELFDESTRUCT.
			body := confJoin(confMemWrite(0, childInit), []any{len(childInit), 0, 100, "CREATE", "POP"}, confCallSeq("CALL", child, 0, 0), []any{"POP"})
			if force {
				body = confJoin(body, confCallSeq("CALL", w.p[1], 0, 0), []any{"POP"})
			}
			body = append(body, "STOP")
			proxy := []any{child, "SELFDESTRUCT"}
			runtimes := [][]byte{confScript(body...), confScript(proxy...), {0}, {0}}
			confMVMGas = 2_000_000
			g := confRunGeth(w, runtimes, []int64{200, 50, 0, 0}, 0, nil)
			m := confRunMVMReal(t, w, key, runtimes, []int64{200, 50, 0, 0}, 0, nil)
			require.True(t, g.ok, g.failKind)
			require.True(t, m.ok, m.failKind)
			t.Logf("force_fund=%v geth child_balance=%s MVM child_balance=%s", force, g.balance[child], m.balance[child])
			require.Empty(t, confCompare(w, g, m))
		})
	}
}
