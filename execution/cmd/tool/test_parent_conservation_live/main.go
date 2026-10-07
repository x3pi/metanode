package main

import (
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
)

var parentURLs = []string{
	"http://127.0.0.1:18601",
	"http://127.0.0.1:18602",
	"http://127.0.0.1:18603",
	"http://127.0.0.1:18604",
}

// MemoryStore mock for holding in-flight message records
type memStore struct {
	records map[common.Hash]*rollup.MessageRecord
	seq     uint64
}

func newMemStore() *memStore {
	return &memStore{records: make(map[common.Hash]*rollup.MessageRecord)}
}

func (m *memStore) Put(r *rollup.MessageRecord) error {
	m.records[r.MessageID] = r
	return nil
}

func (m *memStore) Get(id common.Hash) (*rollup.MessageRecord, bool, error) {
	r, ok := m.records[id]
	return r, ok, nil
}

func (m *memStore) ScanNonTerminal() ([]*rollup.MessageRecord, error) {
	var out []*rollup.MessageRecord
	for _, r := range m.records {
		if r.State != rollup.StateConfirmedSuccess &&
			r.State != rollup.StateConfirmedRefunded &&
			r.State != rollup.StateRefunded &&
			r.State != rollup.StateSkippedDup {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) GetNextFloatSeq() (uint64, error) {
	return m.seq, nil
}

func (m *memStore) IncrementFloatSeq() error {
	m.seq++
	return nil
}

func (m *memStore) SetFloatSeq(seq uint64) error {
	m.seq = seq
	return nil
}

func main() {
	fmt.Println("═════════════════════════════════════════════════════════════")
	fmt.Println("🛡️  MetaNode Live Rollup & Parent Chain Conservation Test")
	fmt.Println("═════════════════════════════════════════════════════════════")

	// 1. Quorum Client against live Parent Chain nodes (18601-18604)
	fmt.Println("\n🔍 [STEP 1] Testing Live Parent Chain QuorumClient (18601-18604)...")
	testPrivKey, testPubKey, testAddr := bls.GenerateKeyPairFromSecretKey("1111111111111111111111111111111111111111111111111111111111111111")
	fmt.Printf("   Node BLS Address: %s\n", testAddr.Hex())

	qClient := parentchain.NewQuorumClient(parentURLs, testPrivKey, testPubKey)
	bal, block, err := qClient.GetFloat(testPubKey)
	if err != nil {
		fmt.Printf("❌ Failed to query live Parent Chain quorum: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   ✅ Quorum achieved across 4 Parent Chain nodes!\n")
	fmt.Printf("   Parent Chain Block: #%d\n", block)
	fmt.Printf("   Attested BLS Float: %s wei\n", bal.String())

	// 2. Test Conservation Invariant At Rest
	fmt.Println("\n─────────────────────────────────────────────────────────────")
	fmt.Println("🧪 [STEP 2] Testing Conservation Invariant At Rest (Float == Accounts)")
	fmt.Println("─────────────────────────────────────────────────────────────")

	store := newMemStore()
	initialSupply := new(big.Int).Set(bal) // At rest, supply exactly matches float
	consInputs := rollup.ConservationInputs{
		Store:       store,
		Client:      qClient,
		Float:       qClient,
		BLS:         testPubKey,
		TotalSupply: func() (*big.Int, error) { return initialSupply, nil },
		RecvCursor:  func() uint64 { return 0 },
	}

	resAtRest, err := rollup.CheckConservation(consInputs)
	if err != nil {
		fmt.Printf("❌ CheckConservation error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Result: %s\n", resAtRest.String())
	if !resAtRest.Stable || !resAtRest.OK {
		fmt.Printf("❌ Expected OK at rest, got: %+v\n", resAtRest)
		os.Exit(1)
	}
	fmt.Println("   ✅ Conservation at rest: 100% OK!")

	// 3. Test Conservation with In-Flight Outbound Transfer
	fmt.Println("\n─────────────────────────────────────────────────────────────")
	fmt.Println("🧪 [STEP 3] Testing Conservation with In-Flight Outbound Transfer")
	fmt.Println("─────────────────────────────────────────────────────────────")

	// Sender debited 500 value + 100 fee on execution cluster:
	// Supply becomes initialSupply - 600, while Float is still initialSupply.
	transferVal := big.NewInt(500)
	transferFee := big.NewInt(100)
	totalDebit := new(big.Int).Add(transferVal, transferFee)

	inFlightSupply := new(big.Int).Sub(initialSupply, totalDebit)
	msgID := crypto.Keccak256Hash([]byte("live_test_transfer_1"))

	store.Put(&rollup.MessageRecord{
		MessageID: msgID,
		Role:      rollup.RoleSender,
		State:     rollup.StateLocalAppliedPendingSend,
		Value:     transferVal,
		GasFee:    transferFee,
	})

	consInputsInFlight := rollup.ConservationInputs{
		Store:       store,
		Client:      qClient,
		Float:       qClient,
		BLS:         testPubKey,
		TotalSupply: func() (*big.Int, error) { return inFlightSupply, nil },
		RecvCursor:  func() uint64 { return 0 },
	}

	resInFlight, err := rollup.CheckConservation(consInputsInFlight)
	if err != nil {
		fmt.Printf("❌ CheckConservation in-flight error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   In-flight Diff (Float - Supply): %s (Pending Bound: %s)\n",
		resInFlight.Diff.String(), resInFlight.Pending.String())
	if !resInFlight.OK || resInFlight.Diff.Cmp(totalDebit) != 0 {
		fmt.Printf("❌ Expected OK with diff matching totalDebit, got: %+v\n", resInFlight)
		os.Exit(1)
	}
	fmt.Println("   ✅ Conservation in-flight bounded: 100% OK!")

	// 4. Test Underflow Violation (Coins minted without Parent Chain backing)
	fmt.Println("\n─────────────────────────────────────────────────────────────")
	fmt.Println("🧪 [STEP 4] Testing Underflow Violation Detection (Float < Supply)")
	fmt.Println("─────────────────────────────────────────────────────────────")

	// Cluster accounts have 1000 wei more than Parent Chain Float backing
	counterfeitSupply := new(big.Int).Add(initialSupply, big.NewInt(1000))
	emptyStore := newMemStore()

	consInputsUnderflow := rollup.ConservationInputs{
		Store:       emptyStore,
		Client:      qClient,
		Float:       qClient,
		BLS:         testPubKey,
		TotalSupply: func() (*big.Int, error) { return counterfeitSupply, nil },
		RecvCursor:  func() uint64 { return 0 },
	}

	resUnderflow, _ := rollup.CheckConservation(consInputsUnderflow)
	fmt.Printf("   Underflow Verdict: OK=%v, Reason=%s\n", resUnderflow.OK, resUnderflow.Reason)
	if resUnderflow.OK {
		fmt.Println("❌ Underflow (Float < Supply) MUST be flagged as violation!")
		os.Exit(1)
	}
	fmt.Println("   ✅ Underflow detected & flagged as security violation!")

	// 5. Test ConservationGuard Fail-Closed Enforcement
	fmt.Println("\n─────────────────────────────────────────────────────────────")
	fmt.Println("🧪 [STEP 5] Testing ConservationGuard Fail-Closed Security")
	fmt.Println("─────────────────────────────────────────────────────────────")

	guard := rollup.NewConservationGuard(rollup.ConservationEnforce)

	// Before any verification, Allow() must refuse cross-chain (fail-closed)
	if err := guard.Allow(); err == nil {
		fmt.Println("❌ Unverified guard must NOT allow cross-chain transfers!")
		os.Exit(1)
	} else {
		fmt.Printf("   ✅ Unverified guard correctly denies transfer: %v\n", err)
	}

	// Observe OK at-rest measurement
	guard.Observe(resAtRest, nil)
	if err := guard.Allow(); err != nil {
		fmt.Printf("❌ Verified guard must allow cross-chain transfers, got: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("   ✅ Conclusive OK measurement permits cross-chain transfers.")

	// Observe 3 consecutive violations -> guard must block
	guard.Observe(resUnderflow, nil)
	guard.Observe(resUnderflow, nil)
	guard.Observe(resUnderflow, nil)

	if err := guard.Allow(); err == nil {
		fmt.Println("❌ Guard with confirmed violations MUST block cross-chain transfers!")
		os.Exit(1)
	} else {
		fmt.Printf("   ✅ Confirmed violation HALTED cross-chain transfers: %v\n", err)
	}

	// 6. Test Conservation Modes (warn vs off vs enforce)
	fmt.Println("\n─────────────────────────────────────────────────────────────")
	fmt.Println("🧪 [STEP 6] Testing Conservation Modes (Enforce vs Warn vs Off)")
	fmt.Println("─────────────────────────────────────────────────────────────")

	warnGuard := rollup.NewConservationGuard(rollup.ConservationWarn)
	warnGuard.Observe(resUnderflow, nil)
	warnGuard.Observe(resUnderflow, nil)
	warnGuard.Observe(resUnderflow, nil)
	if err := warnGuard.Allow(); err != nil {
		fmt.Println("❌ Warn mode should log but not block!")
		os.Exit(1)
	}
	fmt.Println("   ✅ Warn mode: Non-blocking as designed for devnet debugging.")

	offGuard := rollup.NewConservationGuard(rollup.ConservationOff)
	if err := offGuard.Allow(); err != nil {
		fmt.Println("❌ Off mode should allow unconditionally!")
		os.Exit(1)
	}
	fmt.Println("   ✅ Off mode: Bypasses guard as designed.")

	fmt.Println("\n═════════════════════════════════════════════════════════════")
	fmt.Println("🎉 ALL ROLLUP & PARENT CHAIN CONSERVATION TESTS PASSED! (100%)")
	fmt.Println("═════════════════════════════════════════════════════════════")
}
