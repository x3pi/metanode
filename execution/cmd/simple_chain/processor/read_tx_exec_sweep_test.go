package processor

import (
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// eth_call / estimateGas run an EVM (C++) on a transaction the client builds completely: sender, target, value, gas and
// code are all its choice, and the worker that executes it has no recover(). These tests drive executeAndRespondReadTx
// (the exact worker entry point) with seeded random programs and calls, in both modes. A Go panic is reported; a crash of
// the C++ side would end the process, so the current case is written to stderr before it runs.

func newReadTxExecutor(t *testing.T) *TxVirtualExecutor {
	t.Helper()
	bp, _ := readRouteHarness(t)
	return &TxVirtualExecutor{
		chainState: bp.chainState, storageManager: bp.storageManager, messageSender: NewMockMessageSender(),
		readTxLimiter: make(chan struct{}, 1000), offChainExecutionLimiter: make(chan struct{}, 100),
		blockProcessingLock: &sync.RWMutex{},
	}
}

func deployTx(from common.Address, code []byte, value *big.Int, gas uint64) types.Transaction {
	data, _ := transaction.NewDeployData(code, common.Address{}).Marshal()
	return transaction.NewTransaction(from, common.Address{}, value, gas, 1, 0, data, nil, common.Hash{}, common.Hash{}, 0, 991)
}

func callTx(from, to common.Address, input []byte, value *big.Int, gas uint64) types.Transaction {
	data, _ := transaction.NewCallData(input).Marshal()
	return transaction.NewTransaction(from, to, value, gas, 1, 0, data, nil, common.Hash{}, common.Hash{}, 0, 991)
}

// randomProgram builds EVM bytecode: mostly valid opcodes with random PUSH data, some raw noise.
func randomProgram(r *rand.Rand) []byte {
	var code []byte
	for i, n := 0, 1+r.Intn(60); i < n; i++ {
		switch k := r.Intn(10); {
		case k < 3: // PUSHn with random data (often huge offsets/sizes for memory ops)
			size := 1 + r.Intn(32)
			code = append(code, byte(0x5f+size))
			d := make([]byte, size)
			r.Read(d)
			if r.Intn(3) == 0 {
				for j := range d {
					d[j] = 0xff
				}
			}
			code = append(code, d...)
		case k < 5: // arithmetic / stack / memory / storage / env
			code = append(code, byte(r.Intn(0x5c)))
		case k == 5: // DUP / SWAP / LOG
			code = append(code, byte(0x80+r.Intn(0x25)))
		case k == 6: // system opcodes: CREATE, CALL*, RETURN, REVERT, CREATE2, STATICCALL, SELFDESTRUCT...
			code = append(code, []byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xfa, 0xfd, 0xfe, 0xff}[r.Intn(10)])
		case k == 7: // a JUMPDEST loop head: JUMPDEST PUSH1 0 JUMP
			code = append(code, 0x5b, 0x60, 0x00, 0x56)
		default:
			b := make([]byte, 1+r.Intn(8))
			r.Read(b)
			code = append(code, b...)
		}
	}
	return code
}

func extremeValue(r *rand.Rand) *big.Int {
	switch r.Intn(5) {
	case 0:
		return big.NewInt(0)
	case 1:
		return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)) // 2^256-1
	case 2:
		return new(big.Int).Lsh(big.NewInt(1), 255)
	case 3:
		return new(big.Int).Lsh(big.NewInt(1), 64)
	default:
		return big.NewInt(r.Int63())
	}
}

var sweepGasLimits = []uint64{0, 1, 21000, 53000, 100_000, 1_000_000, 3_000_000}

func precompileTargets() []common.Address {
	var out []common.Address
	for i := 1; i <= 0x14; i++ {
		out = append(out, common.BigToAddress(big.NewInt(int64(i))))
	}
	return append(out, mt_common.GATEWAY_CONTRACT_ADDRESS, mt_common.VALIDATOR_CONTRACT_ADDRESS, common.HexToAddress("0x1003"), common.HexToAddress("0x1004"),
		common.HexToAddress("0xFD00000000000000000000000000000000000001"), common.Address{0xff})
}

type readTxCase struct {
	desc string
	tx   types.Transaction
}

func readTxCases(seed int64, deploys, calls int) []readTxCase {
	r := rand.New(rand.NewSource(seed))
	from := func() common.Address {
		if r.Intn(3) == 0 {
			var a common.Address
			r.Read(a[:])
			return a
		}
		return common.HexToAddress("0x1111111111111111111111111111111111111111")
	}
	var out []readTxCase
	for i := 0; i < deploys; i++ {
		code := randomProgram(r)
		gas := sweepGasLimits[r.Intn(len(sweepGasLimits))]
		out = append(out, readTxCase{fmt.Sprintf("deploy#%d gas=%d code=%x", i, gas, code), deployTx(from(), code, extremeValue(r), gas)})
	}
	targets := precompileTargets()
	for i := 0; i < calls; i++ {
		to := targets[r.Intn(len(targets))]
		input := make([]byte, r.Intn(300))
		r.Read(input)
		if r.Intn(4) == 0 && len(input) >= 4 {
			input = append(input[:4], make([]byte, 32*(1+r.Intn(8)))...) // selector + zeroed words
		}
		gas := sweepGasLimits[r.Intn(len(sweepGasLimits))]
		out = append(out, readTxCase{fmt.Sprintf("call#%d to=%s gas=%d input=%x", i, to.Hex(), gas, input), callTx(from(), to, input, extremeValue(r), gas)})
	}
	return out
}

func runReadTxCase(t *testing.T, v *TxVirtualExecutor, c readTxCase, estimate bool) {
	t.Helper()
	fmt.Fprintf(os.Stderr, "READTX-CASE estimate=%v %s\n", estimate, c.desc) // last line printed identifies a process-killing case
	done := make(chan interface{}, 1)
	go func() {
		defer func() { done <- recover() }()
		v.executeAndRespondReadTx(readTxRequest{conn: NewMockConnection(common.HexToAddress("0xabc")), tx: c.tx, msgID: "m", isEstimateGas: estimate})
	}()
	select {
	case p := <-done:
		if p != nil {
			t.Errorf("executeAndRespondReadTx panicked (estimate=%v) on %s: %v", estimate, c.desc, p)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("executeAndRespondReadTx still running after 60s (estimate=%v) on %s", estimate, c.desc)
	}
}

// Control: the harness really executes bytecode (otherwise a quiet sweep would prove nothing).
func TestReadTxExec_HarnessExecutesBytecode(t *testing.T) {
	v := newReadTxExecutor(t)
	// initcode: PUSH1 1 PUSH1 0 MSTORE8 PUSH1 1 PUSH1 0 RETURN -> deploys one byte of runtime code
	ok := deployTx(common.HexToAddress("0x1111"), []byte{0x60, 0x01, 0x60, 0x00, 0x53, 0x60, 0x01, 0x60, 0x00, 0xf3}, big.NewInt(0), 1_000_000)
	rs, err := v.ProcessTransactionOffChain(ok)
	if err != nil || rs == nil {
		t.Fatalf("a valid deployment did not execute: rs=%v err=%v", rs, err)
	}
	if rs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("valid deployment status = %v (exception %v), want RETURNED", rs.ReceiptStatus(), rs.Exception())
	}
	// and an invalid opcode must be reported as a failure, not crash and not succeed
	bad := deployTx(common.HexToAddress("0x1111"), []byte{0xfe}, big.NewInt(0), 1_000_000)
	rs, err = v.ProcessTransactionOffChain(bad)
	if err == nil && rs != nil && rs.ReceiptStatus() == pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("INVALID opcode reported as success")
	}
}

func TestReadTxExecSweep_NoPanicOnRandomProgramsAndPrecompileCalls(t *testing.T) {
	v := newReadTxExecutor(t)
	deploys, calls, seed := 250, 250, int64(20261021)
	if testing.Short() {
		deploys, calls = 40, 40
	}
	// deeper runs: READTX_SWEEP_N=5000 READTX_SWEEP_SEED=7 go test -run TestReadTxExecSweep ./cmd/simple_chain/processor/
	if n, err := strconv.Atoi(os.Getenv("READTX_SWEEP_N")); err == nil && n > 0 {
		deploys, calls = n, n
	}
	if sd, err := strconv.ParseInt(os.Getenv("READTX_SWEEP_SEED"), 10, 64); err == nil {
		seed = sd
	}
	for _, c := range readTxCases(seed, deploys, calls) {
		runReadTxCase(t, v, c, false)
		runReadTxCase(t, v, c, true)
	}
}

// Many clients issue the SAME historical eth_call at the same moment. Every execution must get its own MVM instance, so
// the results of identical calls must be identical and none may fail because another one finished first.
func TestReadTxExec_ConcurrentIdenticalHistoricalCalls_AllSucceedIdentically(t *testing.T) {
	v := newReadTxExecutor(t)
	headerPtr := v.chainState.GetcurrentBlockHeader()
	if headerPtr == nil {
		t.Fatal("no current header")
	}
	header := *headerPtr
	// initcode that keeps reading state through the MVM instance (many BALANCE lookups, each a Go callback), so concurrent
	// executions overlap for long enough that a shared or prematurely cleared instance would show; then returns empty.
	var code []byte
	for i := 0; i < 300; i++ {
		code = append(code, 0x73)
		code = append(code, common.HexToAddress("0x1111111111111111111111111111111111111111").Bytes()...)
		code = append(code, 0x31, 0x50) // BALANCE, POP
	}
	code = append(code, 0x60, 0x00, 0x60, 0x00, 0xf3) // PUSH1 0 PUSH1 0 RETURN
	tx := deployTx(common.HexToAddress("0x1111"), code, big.NewInt(0), 5_000_000)

	const workers, rounds = 100, 30
	var mu sync.Mutex
	outcomes := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				key := func() (k string) {
					defer func() {
						if r := recover(); r != nil {
							k = fmt.Sprintf("PANIC: %v", r)
						}
					}()
					rs, err := v.ProcessTransactionOffChainWithState(tx, header.AccountStatesRoot(), header)
					switch {
					case err != nil:
						return "error: " + err.Error()
					case rs == nil:
						return "nil result"
					default:
						return fmt.Sprintf("status=%v exception=%v", rs.ReceiptStatus(), rs.Exception())
					}
				}()
				mu.Lock()
				outcomes[key]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(outcomes) != 1 {
		t.Fatalf("%d identical concurrent historical calls produced different outcomes: %v", workers*rounds, outcomes)
	}
	for k := range outcomes {
		if k != fmt.Sprintf("status=%v exception=%v", pb.RECEIPT_STATUS_RETURNED, pb.EXCEPTION_NONE) {
			t.Fatalf("every identical call failed the same way: %s", k)
		}
	}
}
