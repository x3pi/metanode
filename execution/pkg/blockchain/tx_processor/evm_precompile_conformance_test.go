package tx_processor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Precompile conformance: go-ethereum's own precompile test vectors (core/vm/testdata/precompiles) are fed to
// the MVM and to go-ethereum through a contract that STATICCALLs the precompile with the vector as calldata.
// Compared: success flag, return data size and return data.

type precompileVector struct {
	Input    string
	Expected string
	Name     string
}

func TestEVMPrecompileConformanceAgainstGeth(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/ethereum/go-ethereum").Output()
	if err != nil {
		t.Skipf("cannot locate the go-ethereum module: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "core", "vm", "testdata", "precompiles")
	files := []struct {
		file string
		addr int64
	}{
		{"ecRecover.json", 1}, {"modexp.json", 5}, {"bn256Add.json", 6}, {"bn256ScalarMul.json", 7},
		{"bn256Pairing.json", 8}, {"blake2F.json", 9}, {"fail-blake2f.json", 9}, {"pointEvaluation.json", 10},
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

	// The vectors above plus a few synthetic inputs for sha256 / ripemd160 / identity.
	type job struct {
		name  string
		addr  int64
		input []byte
	}
	var jobs []job
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(dir, f.file))
		if err != nil {
			t.Logf("skip %s: %v", f.file, err)
			continue
		}
		var vs []precompileVector
		require.NoError(t, json.Unmarshal(raw, &vs))
		for _, v := range vs {
			in, err := hex.DecodeString(v.Input)
			require.NoError(t, err)
			jobs = append(jobs, job{f.file + "/" + v.Name, f.addr, in})
		}
	}
	// invalid inputs of the elliptic-curve precompiles must fail the call (0 on the stack, all the gas given is burned)
	offCurve := make([]byte, 128)
	offCurve[31], offCurve[63] = 1, 1 // the point (1, 1) is not on alt_bn128
	jobs = append(jobs,
		job{"invalid/bn256Add_off_curve", 6, offCurve},
		job{"invalid/bn256Mul_off_curve", 7, offCurve[:96]},
		job{"invalid/bn256Pairing_length_191", 8, make([]byte, 191)},
		job{"invalid/bn256Pairing_off_curve", 8, append(append([]byte{}, offCurve...), make([]byte, 64)...)},
		job{"invalid/pointEvaluation_random", 10, bytes.Repeat([]byte{0x11}, 192)},
		job{"invalid/pointEvaluation_short", 10, []byte{1, 2, 3}},
		job{"invalid/ecrecover_short", 1, []byte{1, 2, 3}},
		job{"invalid/blake2f_short", 9, []byte{0, 0, 0, 1}},
	)
	for _, in := range [][]byte{nil, {1}, bytes.Repeat([]byte{0xab}, 55), bytes.Repeat([]byte{0xcd}, 200)} {
		for _, a := range []int64{2, 3, 4} {
			jobs = append(jobs, job{"synthetic/" + string(rune('0'+a)), a, in})
		}
	}

	stop := confScript("STOP")
	bad, total, skipped := 0, 0, 0
	for _, j := range jobs {
		code := confScript(confJoin(
			[]any{"CALLDATASIZE", 0, 0, "CALLDATACOPY", 0, 0x1000, "CALLDATASIZE", 0, common.BigToAddress(big.NewInt(j.addr)), "GAS", "STATICCALL", 0x1000, "MSTORE",
				"RETURNDATASIZE", 0x1020, "MSTORE", "RETURNDATASIZE", 0, 0x1040, "RETURNDATACOPY", "RETURNDATASIZE", 0x40, "ADD", 0x1000, "RETURN"})...)
		runtimes := [][]byte{code, stop, stop, stop}
		g := confRunGeth(w, runtimes, []int64{0, 0, 0, 0}, 0, j.input)
		m := confRunMVMReal(t, w, key, runtimes, []int64{0, 0, 0, 0}, 0, j.input)
		total++
		if !g.ok || !m.ok {
			// An outer failure (for example gas exhaustion in the MVM tx limit) is reported, not compared.
			if g.ok != m.ok {
				bad++
				t.Logf("MISMATCH %-45s outer status geth=%v (%s) mvm=%v (%s)", j.name, g.ok, g.failKind, m.ok, m.failKind)
			} else {
				skipped++
			}
			continue
		}
		if !bytes.Equal(g.ret, m.ret) {
			bad++
			t.Logf("MISMATCH %-45s geth=%x mvm=%x", j.name, g.ret, m.ret)
		} else if want := confGethTotalGas(g, j.input); want != m.gas {
			bad++
			t.Logf("MISMATCH %-45s gas geth=%d mvm=%d", j.name, want, m.gas)
		}
	}
	t.Logf("SUMMARY precompile vectors=%d mismatching=%d both-failed=%d", total, bad, skipped)
	if os.Getenv("EVM_CONFORMANCE_REPORT_ONLY") == "" {
		require.Zero(t, bad, "%d precompile vectors diverge from go-ethereum", bad)
	}
}
