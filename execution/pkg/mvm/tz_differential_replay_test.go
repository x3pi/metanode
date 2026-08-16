package mvm_test

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// TestTABoundary_TrustzoneLoopback_DifferentialReplay is GĐ4's loopback-only
// portion (note/tee_dual_mode_execution_plan.md: "replay đối chiếu ngoại
// tuyến ... loopback trước, TA thật sau"): replay the SAME sequence of 6
// transactions — one per live forward command, each applied to state before
// the next runs, the way a real block's transactions are — once under cgo,
// once under trustzone-loopback, and compare not just each step's
// ExecuteResult but the final account-state snapshot
// (AccountStateDB.DeterministicDirtyHash — the same primitive
// true_block_stm.go itself uses for fork debugging) after the whole
// sequence.
//
// Both runs use the SAME account/contract addresses (unlike the single-op
// comparison tests above, which deliberately use per-run-unique addresses
// specifically to dodge this) because a meaningful DeterministicDirtyHash
// comparison requires the two ChainStates to end up with the same set of
// dirty addresses. That reintroduces the C++ State-singleton leak risk
// documented in the plan's section 3.4 (Execute/isCache=true keys state by
// contract address, process-globally) — mvm.ClearAllStateInstances()
// between the two sequences is what actually prevents it here; picking a
// fresh address (this file's other tests' fix) isn't an option when the two
// runs must land on identical addresses to be comparable at all.
func TestTABoundary_TrustzoneLoopback_DifferentialReplay(t *testing.T) {
	deployer := nextTestAddr()
	alice := nextTestAddr()
	bob := nextTestAddr()
	mintRecipient := nextTestAddr()
	nonceAcct := nextTestAddr()
	contractAddr := crypto.CreateAddress(deployer, 0)

	// Same SLOAD+SSTORE+RETURN runtime as the Call/Execute comparison tests
	// above — deliberately reused so Call (step 3) and Execute (step 4)
	// both mutate the same slot, in sequence, like 2 real block txs would.
	runtime := []byte{
		0x60, 0x00, 0x54, 0x60, 0x01, 0x01, 0x80, 0x60, 0x00, 0x55,
		0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3,
	}
	initCode := append([]byte{
		0x60, byte(len(runtime)),
		0x60, 0x0c,
		0x60, 0x00,
		0x39,
		0x60, byte(len(runtime)),
		0x60, 0x00,
		0xf3,
	}, runtime...)

	const startNonce = 7
	const mintAmount = 55_555
	const transferAmount = 4_321

	// runSequence executes all 6 steps under mode, applying each result to
	// cs before the next step runs (mirroring a real block's sequential tx
	// application), and returns the final ChainState plus each step's
	// result for cross-mode comparison.
	runSequence := func(mode string, mvmId common.Address) (*blockchain.ChainState, []*mvm.MVMExecuteResult) {
		t.Helper()
		prevMode := mvm.GetExecutionMode()
		mvm.SetExecutionMode(mode)
		t.Cleanup(func() { mvm.SetExecutionMode(prevMode) })

		cs, codeStorage := harnessChainStateWithCodeStorage(t)
		harnessSeedAccount(t, cs, deployer, big.NewInt(1_000_000_000), 0)
		harnessSeedAccount(t, cs, alice, big.NewInt(1_000_000_000), 0)

		engine := mvm.NewExecutionEngine(mvmId, cs.GetSmartContractDB(), cs.GetAccountStateDB(), false)
		t.Cleanup(func() { mvm.ClearMVMApi(mvmId) })

		var results []*mvm.MVMExecuteResult
		apply := func(step string, rs *mvm.MVMExecuteResult) {
			t.Helper()
			if rs == nil {
				t.Fatalf("[%s] mode=%s step=%s: nil result", t.Name(), mode, step)
			}
			if rs.Status != pb.RECEIPT_STATUS_RETURNED {
				t.Fatalf("[%s] mode=%s step=%s: status=%v exmsg=%q", t.Name(), mode, step, rs.Status, rs.Exmsg)
			}
			applyExecuteResultToChainState(t, cs, codeStorage, rs)
			results = append(results, rs)
		}

		// Step 1: Deploy.
		engine.SetRelatedAddresses([]common.Address{deployer})
		apply("Deploy", engine.Deploy(
			deployer.Bytes(), initCode, big.NewInt(0),
			1, 200_000,
			0, 30_000_000, 0, 0, 1, common.Address{},
			mvmId, common.HexToHash("0x01").Bytes(),
			false, false, false,
		))

		// Step 2: SendNative alice -> bob.
		engine.SetRelatedAddresses([]common.Address{alice, bob})
		apply("SendNative", engine.SendNative(
			alice.Bytes(), bob.Bytes(), big.NewInt(transferAmount),
			1, 21000,
			0, 30_000_000, 0, 0, 2, common.Address{},
			mvmId, false,
		))

		// Step 3: Call into the deployed contract (storage[0]: 0 -> 1).
		engine.SetRelatedAddresses([]common.Address{deployer, contractAddr})
		apply("Call", engine.Call(
			deployer.Bytes(), contractAddr.Bytes(), nil, big.NewInt(0),
			1, 200_000,
			0, 30_000_000, 0, 0, 3, common.Address{},
			mvmId, false, common.HexToHash("0x03").Bytes(), []common.Address{deployer, contractAddr},
			false, false,
		))

		// Step 4: Execute into the same contract (storage[0]: 1 -> 2) — the
		// isCache=true path whose process-global State-singleton caching
		// this whole test is designed to survive.
		apply("Execute", engine.Execute(
			deployer.Bytes(), contractAddr.Bytes(), nil, big.NewInt(0),
			1, 200_000,
			0, 30_000_000, 0, 0, 4, common.Address{},
			mvmId, common.HexToHash("0x04").Bytes(), []common.Address{deployer, contractAddr},
			false, true,
		))

		// Step 5: ProcessNativeMintBurn (mint) -> mintRecipient.
		systemAddr := common.HexToAddress("0x000000000000000000000000000000000000MINT")
		engine.SetRelatedAddresses([]common.Address{systemAddr, mintRecipient})
		apply("ProcessNativeMintBurn", engine.ProcessNativeMintBurn(
			systemAddr.Bytes(), mintRecipient.Bytes(), big.NewInt(mintAmount), 0,
			1, 21000,
			0, 30_000_000, 0, 0, 5, common.Address{},
			mvmId, false,
		))

		// Step 6: NoncePlusOne.
		harnessSeedAccount(t, cs, nonceAcct, big.NewInt(0), startNonce)
		engine.SetRelatedAddresses([]common.Address{nonceAcct})
		apply("NoncePlusOne", engine.NoncePlusOne(
			nonceAcct.Bytes(),
			1, 21000,
			0, 30_000_000, 0, 0, 6, common.Address{},
			mvmId, false,
		))

		return cs, results
	}

	csCgo, cgoResults := runSequence(mvm.ModeCgo, nextTestAddr())

	// See the doc comment above: reset the process-global C++ State
	// singleton before the trustzone-loopback sequence reuses the exact
	// same addresses (contractAddr in particular) that the cgo sequence
	// above just wrote to.
	mvm.ClearAllStateInstances()

	csTz, tzResults := runSequence(mvm.ModeTrustzone, nextTestAddr())

	if len(cgoResults) != len(tzResults) {
		t.Fatalf("step count mismatch: cgo=%d trustzone=%d", len(cgoResults), len(tzResults))
	}
	stepNames := []string{"Deploy", "SendNative", "Call", "Execute", "ProcessNativeMintBurn", "NoncePlusOne"}
	for i, name := range stepNames {
		cgoRs, tzRs := cgoResults[i], tzResults[i]
		if tzRs.Status != cgoRs.Status {
			t.Errorf("step %d (%s): status trustzone=%v cgo=%v", i, name, tzRs.Status, cgoRs.Status)
		}
		if tzRs.GasUsed != cgoRs.GasUsed {
			t.Errorf("step %d (%s): gas_used trustzone=%d cgo=%d", i, name, tzRs.GasUsed, cgoRs.GasUsed)
		}
		if string(tzRs.Return) != string(cgoRs.Return) {
			t.Errorf("step %d (%s): return trustzone=%x cgo=%x", i, name, tzRs.Return, cgoRs.Return)
		}
	}

	// Storage really did evolve across steps 3->4 (Call then Execute on the
	// same slot), not just individually matching: confirm the final Execute
	// step (index 3) actually returned 2, not 1 — otherwise a bug that
	// makes both runs silently skip step 3's effect would still "match".
	want2 := make([]byte, 32)
	want2[31] = 2
	if string(cgoResults[3].Return) != string(want2) {
		t.Fatalf("cgo step 3 (Execute) Return = %x, want %x (storage[0] after Call then Execute)", cgoResults[3].Return, want2)
	}

	cgoHash := csCgo.GetAccountStateDB().DeterministicDirtyHash()
	tzHash := csTz.GetAccountStateDB().DeterministicDirtyHash()
	if cgoHash == (common.Hash{}) {
		t.Fatalf("cgo DeterministicDirtyHash is zero — no dirty accounts recorded, replay applied nothing")
	}
	if tzHash != cgoHash {
		t.Errorf("final DeterministicDirtyHash mismatch: trustzone=%s cgo=%s\ncgo dirty accounts:\n%s\ntrustzone dirty accounts:\n%s",
			tzHash.Hex(), cgoHash.Hex(),
			csCgo.GetAccountStateDB().DirtyAccountsSummary(),
			csTz.GetAccountStateDB().DirtyAccountsSummary())
	}
}
