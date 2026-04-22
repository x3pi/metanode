package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// ============================================================
// Configuration
// ============================================================

const CHAIN_ID = 991

// Node 4 default configuration
var defaultNode4Config = Node4Config{
	PrivateKey:     "6c8489f6f86fea58b26e34c8c37e13e5993651f09f5f96739d9febf65aded718",
	PrimaryAddress: "/ip4/127.0.0.1/tcp/9004",
	WorkerAddress:  "127.0.0.1:9004",
	P2PAddress:     "/ip4/127.0.0.1/tcp/9004",
	Name:           "node-4",
	Description:    "Node 4 - SyncOnly Validator (ready for epoch transition)",
	CommissionRate: 1000,
	ProtocolKey:    "qnTBK30Gui4kBqR0UFwLq34DNa/GwXqGubpJbx+87kQ=",
	NetworkKey:     "5g5vIzco8ydGZ6BXTSSVt2dhO1hI31KS4rXSFdwbpgs=",
	Hostname:       "node-4",
	AuthorityKey:   "q2rdagN+1z8x6ozdCBj1l8P4aYm0Di5b2Wa1ojyBY9XtBj31dortoL2Q4h4bhRzQDZHRhSPJQImRUIABBemflBZ6dbrleOtZSrBrgMNEi2l0h54q36CrNPQdNLKREYem",
}

type Node4Config struct {
	PrivateKey     string
	PrimaryAddress string
	WorkerAddress  string
	P2PAddress     string
	Name           string
	Description    string
	CommissionRate uint64
	ProtocolKey    string
	NetworkKey     string
	Hostname       string
	AuthorityKey   string
}

// ============================================================
// ABI Definition
// ============================================================

const ValidationABI = `[
	{
		"name": "registerValidator",
		"type": "function",
		"inputs": [
			{"name": "primaryAddress", "type": "string"},
			{"name": "workerAddress", "type": "string"},
			{"name": "p2pAddress", "type": "string"},
			{"name": "name", "type": "string"},
			{"name": "description", "type": "string"},
			{"name": "website", "type": "string"},
			{"name": "image", "type": "string"},
			{"name": "commissionRate", "type": "uint64"},
			{"name": "minSelfDelegation", "type": "uint256"},
			{"name": "networkKey", "type": "string"},
			{"name": "hostname", "type": "string"},
			{"name": "authorityKey", "type": "string"},
			{"name": "protocolKey", "type": "string"}
		]
	},
	{
		"name": "delegate",
		"type": "function",
		"inputs": [
			{"name": "_validatorAddress", "type": "address"}
		]
	},
	{
		"name": "undelegate",
		"type": "function", 
		"inputs": [
			{"name": "_validatorAddress", "type": "address"},
			{"name": "_amount", "type": "uint256"}
		]
	},
	{
		"name": "deregisterValidator",
		"type": "function",
		"inputs": []
	},
	{
		"inputs": [],
		"name": "getValidatorCount",
		"outputs": [{"internalType": "uint256", "name": "", "type": "uint256"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "uint256", "name": "", "type": "uint256"}],
		"name": "validatorAddresses",
		"outputs": [{"internalType": "address", "name": "", "type": "address"}],
		"stateMutability": "view",
		"type": "function"
	}
]`

// ============================================================
// Structs
// ============================================================

type RPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	Id      int           `json:"id"`
}

type RPCResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
	Id      int             `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type PhaseResult struct {
	Name     string
	Passed   bool
	Forks    int64
	Checks   int64
	Duration time.Duration
	Details  string
}

type RoundResult struct {
	RoundNum    int
	StartTime   time.Time
	Duration    time.Duration
	Phases      []PhaseResult
	AllPassed   bool
	TotalForks  int64
	TotalChecks int64
}

type HashWatcher struct {
	masterRPC   string
	node4RPC    string
	interval    time.Duration
	checkLast   int
	totalForks  atomic.Int64
	totalChecks atomic.Int64
	phaseForks  atomic.Int64
	phaseChecks atomic.Int64
	stopCh      chan struct{}
	stopped     atomic.Bool
	mu          sync.Mutex
	lastErr     string
}

// ============================================================
// Main
// ============================================================

func main() {
	masterRPC := flag.String("master-rpc", "http://localhost:8747", "Master node RPC URL")
	node4RPC := flag.String("node4-rpc", "http://localhost:10748", "Node 4 RPC URL")
	scriptsDir := flag.String("scripts-dir", "", "Path to consensus/metanode/scripts/node/ (auto-detect if empty)")
	privateKey := flag.String("private-key", defaultNode4Config.PrivateKey, "Node 4 private key (hex)")
	checkInterval := flag.Duration("check-interval", 5*time.Second, "Hash check interval")
	stakeAmount := flag.String("stake", "1000000000000000000000", "Stake amount in wei (default: 1000 ETH)")
	skipPhases := flag.String("skip-phase", "", "Comma-separated phase numbers to skip (e.g. '4,5')")
	txRPC := flag.String("tx-rpc", "http://localhost:8545", "RPC endpoint for sending transactions")
	loopCount := flag.Int("loop", 1, "Number of rounds to run (0 = infinite until Ctrl+C)")
	loopInterval := flag.Duration("loop-interval", 5*time.Minute, "Wait time between rounds (default: 5m ≈ 5 epochs)")

	flag.Parse()

	// Auto-detect scripts dir
	if *scriptsDir == "" {
		candidates := []string{
			"../../../consensus/metanode/scripts/node",
			"../../../../consensus/metanode/scripts/node",
			"/home/abc/chain-n/consensus/metanode/scripts/node",
		}
		for _, c := range candidates {
			abs, _ := filepath.Abs(c)
			if _, err := os.Stat(filepath.Join(abs, "stop_node.sh")); err == nil {
				*scriptsDir = abs
				break
			}
		}
		if *scriptsDir == "" {
			fmt.Println("❌ Không tìm thấy scripts dir. Sử dụng --scripts-dir")
			os.Exit(1)
		}
	}

	// Parse skip phases
	skipMap := make(map[int]bool)
	if *skipPhases != "" {
		for _, s := range strings.Split(*skipPhases, ",") {
			s = strings.TrimSpace(s)
			var n int
			fmt.Sscanf(s, "%d", &n)
			skipMap[n] = true
		}
	}

	// Parse stake amount
	stakeAmountBig, ok := new(big.Int).SetString(*stakeAmount, 10)
	if !ok {
		stakeAmountBig = new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18))
	}

	printBanner()

	suite := &TestSuite{
		masterRPC:    *masterRPC,
		node4RPC:     *node4RPC,
		scriptsDir:   *scriptsDir,
		privateKey:   *privateKey,
		txRPC:        *txRPC,
		stakeAmount:  stakeAmountBig,
		skipPhases:   skipMap,
		results:      make([]PhaseResult, 0),
		roundResults: make([]RoundResult, 0),
	}

	// Create and start hash watcher
	suite.watcher = &HashWatcher{
		masterRPC: *masterRPC,
		node4RPC:  *node4RPC,
		interval:  *checkInterval,
		checkLast: 20,
		stopCh:    make(chan struct{}),
	}

	loopLabel := "1 lần"
	if *loopCount == 0 {
		loopLabel = "∞ (Ctrl+C để dừng)"
	} else if *loopCount > 1 {
		loopLabel = fmt.Sprintf("%d rounds", *loopCount)
	}

	fmt.Printf("📋 Config:\n")
	fmt.Printf("   Master RPC:    %s\n", *masterRPC)
	fmt.Printf("   Node4 RPC:     %s\n", *node4RPC)
	fmt.Printf("   TX RPC:        %s\n", *txRPC)
	fmt.Printf("   Scripts Dir:   %s\n", *scriptsDir)
	fmt.Printf("   Check Every:   %s\n", *checkInterval)
	fmt.Printf("   Loop:          %s\n", loopLabel)
	if *loopCount != 1 {
		fmt.Printf("   Loop Interval: %s\n", *loopInterval)
	}
	fmt.Println()

	// Verify connectivity
	fmt.Println("🔌 Kiểm tra kết nối...")
	masterHeight, err := getBlockHeight(*masterRPC)
	if err != nil {
		fmt.Printf("❌ Không kết nối được Master: %v\n", err)
		os.Exit(1)
	}
	node4Height, err := getBlockHeight(*node4RPC)
	if err != nil {
		fmt.Printf("❌ Không kết nối được Node 4: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   ✅ Master: block %d\n", masterHeight)
	fmt.Printf("   ✅ Node 4: block %d\n", node4Height)
	fmt.Println()

	// Setup signal handler for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	startTime := time.Now()

	// Start background hash watcher
	go suite.watcher.Run()

	// Run loop
	round := 0
	for {
		round++
		if *loopCount > 0 && round > *loopCount {
			break
		}

		if *loopCount != 1 {
			fmt.Printf("\n🔄══════════════════════════════════════════════════════════\n")
			if *loopCount == 0 {
				fmt.Printf("🔄  ROUND %d (infinite mode)\n", round)
			} else {
				fmt.Printf("🔄  ROUND %d / %d\n", round, *loopCount)
			}
			fmt.Printf("🔄══════════════════════════════════════════════════════════\n")
		}

		// Reset per-round state
		suite.results = make([]PhaseResult, 0)

		// Run all phases
		suite.runAllPhases()

		// Collect round result
		rr := suite.collectRoundResult(round)
		suite.roundResults = append(suite.roundResults, rr)

		// Print round summary
		if *loopCount != 1 {
			suite.printRoundSummary(rr)
		}

		// Check if this was the last round
		if *loopCount > 0 && round >= *loopCount {
			break
		}

		// Wait for next round (with signal check)
		fmt.Printf("\n⏳ Đợi %s trước round tiếp theo... (Ctrl+C để dừng)\n", *loopInterval)
		select {
		case <-sigCh:
			fmt.Printf("\n🛑 Nhận tín hiệu dừng. Kết thúc sau round %d.\n", round)
			goto done
		case <-time.After(*loopInterval):
			// Continue to next round
		}
	}

done:
	// Stop watcher
	suite.watcher.Stop()

	// Print final report
	if *loopCount == 1 {
		suite.printReport(time.Since(startTime))
	} else {
		suite.printLoopReport(time.Since(startTime))
	}
}

// ============================================================
// Test Suite
// ============================================================

type TestSuite struct {
	masterRPC    string
	node4RPC     string
	scriptsDir   string
	privateKey   string
	txRPC        string
	stakeAmount  *big.Int
	skipPhases   map[int]bool
	watcher      *HashWatcher
	results      []PhaseResult
	roundResults []RoundResult
}

func (s *TestSuite) runAllPhases() {
	if !s.skipPhases[1] {
		s.runPhase1_Baseline()
	}
	if !s.skipPhases[2] {
		s.runPhase2_Deregister()
	}
	if !s.skipPhases[3] {
		s.runPhase3_RestartNode4()
	}
	if !s.skipPhases[4] {
		s.runPhase4_Register()
	}
	if !s.skipPhases[5] {
		s.runPhase5_RestartNode1()
	}
}

func (s *TestSuite) collectRoundResult(roundNum int) RoundResult {
	allPassed := true
	var totalForks, totalChecks int64
	var totalDuration time.Duration

	for _, r := range s.results {
		if !r.Passed {
			allPassed = false
		}
		totalForks += r.Forks
		totalChecks += r.Checks
		totalDuration += r.Duration
	}

	return RoundResult{
		RoundNum:    roundNum,
		StartTime:   time.Now().Add(-totalDuration),
		Duration:    totalDuration,
		Phases:      append([]PhaseResult{}, s.results...),
		AllPassed:   allPassed,
		TotalForks:  totalForks,
		TotalChecks: totalChecks,
	}
}

func (s *TestSuite) printRoundSummary(rr RoundResult) {
	symbol := "✅"
	status := "PASS"
	if !rr.AllPassed {
		symbol = "❌"
		status = "FAIL"
	}
	fmt.Printf("\n%s Round %d: %s | %d checks, %d forks | %s\n",
		symbol, rr.RoundNum, status, rr.TotalChecks, rr.TotalForks, rr.Duration.Round(time.Second))

	// Cumulative stats
	totalRounds := len(s.roundResults)
	passedRounds := 0
	var cumForks int64
	for _, r := range s.roundResults {
		if r.AllPassed {
			passedRounds++
		}
		cumForks += r.TotalForks
	}
	fmt.Printf("📊 Tổng cộng: %d/%d rounds passed, %d forks tích lũy\n",
		passedRounds, totalRounds, cumForks)
}

func (s *TestSuite) runPhase1_Baseline() {
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("📌 PHASE 1: Baseline — Kiểm tra hệ thống ổn định")
	fmt.Println("═══════════════════════════════════════════════════════════")

	s.watcher.ResetPhaseCounters()
	start := time.Now()

	// Watch for 30 seconds
	s.waitWithProgress(30*time.Second, "Đang theo dõi")

	forks := s.watcher.phaseForks.Load()
	checks := s.watcher.phaseChecks.Load()

	result := PhaseResult{
		Name:     "Baseline",
		Passed:   forks == 0 && checks > 0,
		Forks:    forks,
		Checks:   checks,
		Duration: time.Since(start),
	}
	if checks == 0 {
		result.Details = "Không có check nào chạy"
		result.Passed = false
	}
	s.results = append(s.results, result)
	s.printPhaseResult(1, result)
}

func (s *TestSuite) runPhase2_Deregister() {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("📌 PHASE 2: Deregister Node 4 — Hủy đăng ký validator")
	fmt.Println("═══════════════════════════════════════════════════════════")

	s.watcher.ResetPhaseCounters()
	start := time.Now()

	// Check current validator count
	countBefore := s.getValidatorCount()
	fmt.Printf("   📊 Validator count trước: %d\n", countBefore)

	// Check if node 4 is registered
	node4Addr := s.getNode4Address()
	isRegistered := s.isValidatorRegistered(node4Addr)

	if !isRegistered {
		fmt.Println("   ⏭️  Node 4 chưa đăng ký — bỏ qua phase 2")
		s.results = append(s.results, PhaseResult{
			Name:     "Deregister Node 4",
			Passed:   true,
			Duration: time.Since(start),
			Details:  "Skipped — Node 4 not registered",
		})
		return
	}

	// Send undelegate TX first (if has stake)
	fmt.Println("   📤 Gửi undelegate TX...")
	err := s.sendUndelegateTx(node4Addr)
	if err != nil {
		fmt.Printf("   ⚠️  Undelegate lỗi (có thể không có stake): %v\n", err)
	} else {
		fmt.Println("   ✅ Undelegate TX gửi thành công")
		fmt.Println("   ⏳ Đợi TX xử lý (10s)...")
		time.Sleep(10 * time.Second)
	}

	// Send deregister TX
	fmt.Println("   📤 Gửi deregister TX...")
	txHash, err := s.sendDeregisterTx()
	if err != nil {
		fmt.Printf("   ❌ Deregister lỗi: %v\n", err)
		s.results = append(s.results, PhaseResult{
			Name:     "Deregister Node 4",
			Passed:   false,
			Duration: time.Since(start),
			Details:  fmt.Sprintf("TX error: %v", err),
		})
		return
	}
	fmt.Printf("   ✅ TX Hash: %s\n", txHash)

	// Wait 2 epochs for committee change
	fmt.Println("   ⏳ Đợi 2 epoch (120s) để committee thay đổi...")
	s.waitWithProgress(120*time.Second, "Đợi epoch transition")

	// Verify
	countAfter := s.getValidatorCount()
	fmt.Printf("   📊 Validator count sau: %d\n", countAfter)

	forks := s.watcher.phaseForks.Load()
	checks := s.watcher.phaseChecks.Load()

	result := PhaseResult{
		Name:     "Deregister Node 4",
		Passed:   forks == 0 && countAfter < countBefore,
		Forks:    forks,
		Checks:   checks,
		Duration: time.Since(start),
		Details:  fmt.Sprintf("validators: %d → %d", countBefore, countAfter),
	}
	s.results = append(s.results, result)
	s.printPhaseResult(2, result)
}

func (s *TestSuite) runPhase3_RestartNode4() {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("📌 PHASE 3: Stop & Resume Node 4")
	fmt.Println("═══════════════════════════════════════════════════════════")

	s.watcher.ResetPhaseCounters()
	start := time.Now()

	// Record height before stop
	heightBefore, _ := getBlockHeight(s.node4RPC)
	fmt.Printf("   📊 Node 4 height trước stop: %d\n", heightBefore)

	// Stop Node 4
	fmt.Println("   🛑 Stopping Node 4...")
	err := s.execScript("stop_node.sh", "4")
	if err != nil {
		fmt.Printf("   ❌ Stop lỗi: %v\n", err)
	}

	// Wait 30s for gap
	s.waitWithProgress(30*time.Second, "Node 4 đang tắt")

	// Resume Node 4
	fmt.Println("   🔄 Resuming Node 4...")
	err = s.execScript("resume_node.sh", "4")
	if err != nil {
		fmt.Printf("   ❌ Resume lỗi: %v\n", err)
	}

	// Wait for catch-up
	fmt.Println("   ⏳ Đợi catch-up (60s)...")
	s.waitWithProgress(60*time.Second, "Đợi Node 4 catch-up")

	// Check heights
	masterHeight, _ := getBlockHeight(s.masterRPC)
	node4Height, _ := getBlockHeight(s.node4RPC)
	heightDiff := int64(masterHeight) - int64(node4Height)
	fmt.Printf("   📊 Heights: Master=%d, Node4=%d (chênh %d)\n", masterHeight, node4Height, heightDiff)

	forks := s.watcher.phaseForks.Load()
	checks := s.watcher.phaseChecks.Load()

	result := PhaseResult{
		Name:     "Stop/Resume Node 4",
		Passed:   forks == 0 && heightDiff < 50,
		Forks:    forks,
		Checks:   checks,
		Duration: time.Since(start),
		Details:  fmt.Sprintf("height diff: %d blocks", heightDiff),
	}
	s.results = append(s.results, result)
	s.printPhaseResult(3, result)
}

func (s *TestSuite) runPhase4_Register() {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("📌 PHASE 4: Re-register Node 4 — Đăng ký lại validator")
	fmt.Println("═══════════════════════════════════════════════════════════")

	s.watcher.ResetPhaseCounters()
	start := time.Now()

	countBefore := s.getValidatorCount()
	fmt.Printf("   📊 Validator count trước: %d\n", countBefore)

	// Check if already registered
	node4Addr := s.getNode4Address()
	if s.isValidatorRegistered(node4Addr) {
		fmt.Println("   ⏭️  Node 4 đã đăng ký — bỏ qua phase 4")
		s.results = append(s.results, PhaseResult{
			Name:     "Re-register Node 4",
			Passed:   true,
			Duration: time.Since(start),
			Details:  "Skipped — already registered",
		})
		return
	}

	// Send register TX
	fmt.Println("   📤 Gửi register TX...")
	txHash, err := s.sendRegisterTx()
	if err != nil {
		fmt.Printf("   ❌ Register lỗi: %v\n", err)
		s.results = append(s.results, PhaseResult{
			Name:     "Re-register Node 4",
			Passed:   false,
			Duration: time.Since(start),
			Details:  fmt.Sprintf("TX error: %v", err),
		})
		return
	}
	fmt.Printf("   ✅ Register TX Hash: %s\n", txHash)

	// Wait for TX confirmation
	time.Sleep(5 * time.Second)

	// Send delegate TX
	fmt.Println("   📤 Gửi delegate TX...")
	err = s.sendDelegateTx(node4Addr)
	if err != nil {
		fmt.Printf("   ⚠️  Delegate lỗi: %v\n", err)
	} else {
		fmt.Println("   ✅ Delegate TX gửi thành công")
	}

	// Wait 2 epochs for committee change
	fmt.Println("   ⏳ Đợi 2 epoch (120s) để committee thay đổi...")
	s.waitWithProgress(120*time.Second, "Đợi epoch transition")

	countAfter := s.getValidatorCount()
	fmt.Printf("   📊 Validator count sau: %d\n", countAfter)

	forks := s.watcher.phaseForks.Load()
	checks := s.watcher.phaseChecks.Load()

	result := PhaseResult{
		Name:     "Re-register Node 4",
		Passed:   forks == 0 && countAfter > countBefore,
		Forks:    forks,
		Checks:   checks,
		Duration: time.Since(start),
		Details:  fmt.Sprintf("validators: %d → %d", countBefore, countAfter),
	}
	s.results = append(s.results, result)
	s.printPhaseResult(4, result)
}

func (s *TestSuite) runPhase5_RestartNode1() {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("📌 PHASE 5: Stop & Resume Node 1")
	fmt.Println("═══════════════════════════════════════════════════════════")

	s.watcher.ResetPhaseCounters()
	start := time.Now()

	// Stop Node 1
	fmt.Println("   🛑 Stopping Node 1...")
	err := s.execScript("stop_node.sh", "1")
	if err != nil {
		fmt.Printf("   ❌ Stop lỗi: %v\n", err)
	}

	// Wait 30s for gap
	s.waitWithProgress(30*time.Second, "Node 1 đang tắt")

	// Resume Node 1
	fmt.Println("   🔄 Resuming Node 1...")
	err = s.execScript("resume_node.sh", "1")
	if err != nil {
		fmt.Printf("   ❌ Resume lỗi: %v\n", err)
	}

	// Wait for catch-up
	fmt.Println("   ⏳ Đợi catch-up (60s)...")
	s.waitWithProgress(60*time.Second, "Đợi Node 1 catch-up")

	// Check heights (Master vs Node 4 — both should still be in sync)
	masterHeight, _ := getBlockHeight(s.masterRPC)
	node4Height, _ := getBlockHeight(s.node4RPC)
	heightDiff := int64(masterHeight) - int64(node4Height)
	if heightDiff < 0 {
		heightDiff = -heightDiff
	}
	fmt.Printf("   📊 Heights: Master=%d, Node4=%d (chênh %d)\n", masterHeight, node4Height, heightDiff)

	forks := s.watcher.phaseForks.Load()
	checks := s.watcher.phaseChecks.Load()

	result := PhaseResult{
		Name:     "Stop/Resume Node 1",
		Passed:   forks == 0,
		Forks:    forks,
		Checks:   checks,
		Duration: time.Since(start),
		Details:  fmt.Sprintf("height diff: %d blocks", heightDiff),
	}
	s.results = append(s.results, result)
	s.printPhaseResult(5, result)
}

// ============================================================
// Hash Watcher
// ============================================================

func (w *HashWatcher) Run() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.checkOnce()
		}
	}
}

func (w *HashWatcher) Stop() {
	if w.stopped.CompareAndSwap(false, true) {
		close(w.stopCh)
	}
}

func (w *HashWatcher) ResetPhaseCounters() {
	w.phaseForks.Store(0)
	w.phaseChecks.Store(0)
}

func (w *HashWatcher) checkOnce() {
	masterHeight, err := getBlockHeight(w.masterRPC)
	if err != nil {
		w.mu.Lock()
		w.lastErr = fmt.Sprintf("master RPC error: %v", err)
		w.mu.Unlock()
		return
	}

	node4Height, err := getBlockHeight(w.node4RPC)
	if err != nil {
		// Node might be down (phase 3), that's OK
		return
	}

	// Compare last N blocks on the shorter chain
	minHeight := masterHeight
	if node4Height < minHeight {
		minHeight = node4Height
	}

	startBlock := minHeight - uint64(w.checkLast)
	if int64(startBlock) < 1 {
		startBlock = 1
	}

	forkFound := false
	for blockNum := startBlock; blockNum <= minHeight; blockNum++ {
		h1, err1 := getBlockHash(w.masterRPC, blockNum)
		h2, err2 := getBlockHash(w.node4RPC, blockNum)
		if err1 != nil || err2 != nil {
			continue
		}
		if h1 != h2 {
			forkFound = true
			fmt.Printf("\n   ❌ FORK at block %d: master=%s node4=%s\n", blockNum, h1[:18], h2[:18])
			break
		}
	}

	w.phaseChecks.Add(1)
	w.totalChecks.Add(1)

	if forkFound {
		w.phaseForks.Add(1)
		w.totalForks.Add(1)
	}
}

// ============================================================
// Validator Transaction Functions
// ============================================================

func (s *TestSuite) getNode4Address() common.Address {
	privKey, _ := crypto.HexToECDSA(s.privateKey)
	publicKey := privKey.Public()
	publicKeyECDSA := publicKey.(*ecdsa.PublicKey)
	return crypto.PubkeyToAddress(*publicKeyECDSA)
}

func (s *TestSuite) sendRegisterTx() (string, error) {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return "", fmt.Errorf("parsing ABI: %v", err)
	}

	inputData, err := parsedABI.Pack("registerValidator",
		defaultNode4Config.PrimaryAddress,
		defaultNode4Config.WorkerAddress,
		defaultNode4Config.P2PAddress,
		defaultNode4Config.Name,
		defaultNode4Config.Description,
		"", // website
		"", // image
		defaultNode4Config.CommissionRate,
		big.NewInt(0), // minSelfDelegation
		defaultNode4Config.NetworkKey,
		defaultNode4Config.Hostname,
		defaultNode4Config.AuthorityKey,
		defaultNode4Config.ProtocolKey,
	)
	if err != nil {
		return "", fmt.Errorf("encoding calldata: %v", err)
	}

	return s.sendContractTx(inputData, big.NewInt(0))
}

func (s *TestSuite) sendDeregisterTx() (string, error) {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return "", fmt.Errorf("parsing ABI: %v", err)
	}

	inputData, err := parsedABI.Pack("deregisterValidator")
	if err != nil {
		return "", fmt.Errorf("encoding calldata: %v", err)
	}

	return s.sendContractTx(inputData, big.NewInt(0))
}

func (s *TestSuite) sendDelegateTx(validatorAddr common.Address) error {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return fmt.Errorf("parsing ABI: %v", err)
	}

	inputData, err := parsedABI.Pack("delegate", validatorAddr)
	if err != nil {
		return fmt.Errorf("encoding calldata: %v", err)
	}

	_, err = s.sendContractTx(inputData, s.stakeAmount)
	return err
}

func (s *TestSuite) sendUndelegateTx(validatorAddr common.Address) error {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return fmt.Errorf("parsing ABI: %v", err)
	}

	// Undelegate the exact stake amount
	amount := s.stakeAmount
	fmt.Printf("   📊 Undelegate amount: %s wei\n", amount.String())
	inputData, err := parsedABI.Pack("undelegate", validatorAddr, amount)
	if err != nil {
		return fmt.Errorf("encoding calldata: %v", err)
	}

	_, err = s.sendContractTx(inputData, big.NewInt(0))
	return err
}

func (s *TestSuite) sendContractTx(inputData []byte, value *big.Int) (string, error) {
	privateKey, err := crypto.HexToECDSA(s.privateKey)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("cannot cast public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	nonce, err := getTransactionCount(s.txRPC, fromAddress.Hex())
	if err != nil {
		return "", fmt.Errorf("getting nonce: %v", err)
	}

	contractAddress := mt_common.VALIDATOR_CONTRACT_ADDRESS
	gasLimit := uint64(500000)
	gasPrice := big.NewInt(20000000000) // 20 Gwei

	tx := types.NewTransaction(nonce, contractAddress, value, gasLimit, gasPrice, inputData)

	chainID := big.NewInt(CHAIN_ID)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return "", fmt.Errorf("signing transaction: %v", err)
	}

	txHash, err := sendRawTransaction(s.txRPC, signedTx)
	if err != nil {
		return "", fmt.Errorf("sending transaction: %v", err)
	}

	return txHash, nil
}

func (s *TestSuite) getValidatorCount() int64 {
	callData := common.FromHex("0x7071688a") // getValidatorCount()
	result, err := ethCall(s.txRPC, mt_common.VALIDATOR_CONTRACT_ADDRESS.Hex(), hexutil.Encode(callData))
	if err != nil {
		fmt.Printf("   ⚠️  getValidatorCount error: %v\n", err)
		return -1
	}
	count := new(big.Int).SetBytes(result)
	return count.Int64()
}

func (s *TestSuite) isValidatorRegistered(addr common.Address) bool {
	parsedABI, _ := abi.JSON(strings.NewReader(ValidationABI))
	count := s.getValidatorCount()

	for i := int64(0); i < count; i++ {
		indexData, _ := parsedABI.Pack("validatorAddresses", big.NewInt(i))
		result, err := ethCall(s.txRPC, mt_common.VALIDATOR_CONTRACT_ADDRESS.Hex(), hexutil.Encode(indexData))
		if err != nil {
			continue
		}
		validatorAddr := common.BytesToAddress(result)
		if validatorAddr == addr {
			return true
		}
	}
	return false
}

// ============================================================
// Shell Script Execution
// ============================================================

func (s *TestSuite) execScript(script string, args ...string) error {
	scriptPath := filepath.Join(s.scriptsDir, script)
	cmd := exec.Command("bash", append([]string{scriptPath}, args...)...)
	cmd.Dir = s.scriptsDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v\n%s", script, err, string(output))
	}
	return nil
}

// ============================================================
// RPC Helpers
// ============================================================

func getBlockHeight(rpcURL string) (uint64, error) {
	req := RPCRequest{Jsonrpc: "2.0", Method: "eth_blockNumber", Params: []interface{}{}, Id: 1}
	res, err := sendRPC(rpcURL, req)
	if err != nil {
		return 0, err
	}
	var hexNum string
	if err := json.Unmarshal(res.Result, &hexNum); err != nil {
		return 0, err
	}
	return hexutil.DecodeUint64(hexNum)
}

func getBlockHash(rpcURL string, blockNum uint64) (string, error) {
	hexNum := fmt.Sprintf("0x%x", blockNum)
	req := RPCRequest{Jsonrpc: "2.0", Method: "eth_getBlockByNumber", Params: []interface{}{hexNum, false}, Id: 1}
	res, err := sendRPC(rpcURL, req)
	if err != nil {
		return "", err
	}

	var block map[string]interface{}
	if err := json.Unmarshal(res.Result, &block); err != nil {
		return "", err
	}
	if block == nil {
		return "", fmt.Errorf("block %d not found", blockNum)
	}

	hash, ok := block["hash"].(string)
	if !ok {
		return "", fmt.Errorf("no hash field")
	}
	return hash, nil
}

func getTransactionCount(rpcURL string, address string) (uint64, error) {
	req := RPCRequest{Jsonrpc: "2.0", Method: "eth_getTransactionCount", Params: []interface{}{address, "latest"}, Id: 1}
	res, err := sendRPC(rpcURL, req)
	if err != nil {
		return 0, err
	}

	rawResult := string(res.Result)
	if rawResult == "null" || rawResult == "" {
		return 0, nil
	}

	var hexNonce string
	if err := json.Unmarshal(res.Result, &hexNonce); err == nil {
		return hexutil.DecodeUint64(hexNonce)
	}

	var intNonce uint64
	if err := json.Unmarshal(res.Result, &intNonce); err == nil {
		return intNonce, nil
	}

	return 0, fmt.Errorf("could not unmarshal nonce result: %s", rawResult)
}

func sendRawTransaction(rpcURL string, tx *types.Transaction) (string, error) {
	rawTxBytes, err := tx.MarshalBinary()
	if err != nil {
		return "", err
	}
	rawTxHex := hexutil.Encode(rawTxBytes)

	req := RPCRequest{Jsonrpc: "2.0", Method: "eth_sendRawTransaction", Params: []interface{}{rawTxHex, nil, nil}, Id: 2}
	res, err := sendRPC(rpcURL, req)
	if err != nil {
		return "", err
	}

	var txHash string
	if err := json.Unmarshal(res.Result, &txHash); err != nil {
		return "", fmt.Errorf("failed to unmarshal tx hash: %v. Response: %s", err, string(res.Result))
	}
	return txHash, nil
}

func ethCall(rpcURL string, to string, data string) ([]byte, error) {
	callObject := map[string]interface{}{
		"to":   to,
		"data": data,
	}
	req := RPCRequest{Jsonrpc: "2.0", Method: "eth_call", Params: []interface{}{callObject, "latest"}, Id: 1}
	res, err := sendRPC(rpcURL, req)
	if err != nil {
		return nil, err
	}

	var hexResult string
	if err := json.Unmarshal(res.Result, &hexResult); err != nil {
		return nil, err
	}
	return hexutil.Decode(hexResult)
}

func sendRPC(rpcURL string, req RPCRequest) (*RPCResponse, error) {
	body, _ := json.Marshal(req)
	httpResp, err := http.Post(rpcURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("http error: %v", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %v", err)
	}

	var res RPCResponse
	if err := json.Unmarshal(respBody, &res); err != nil {
		return nil, fmt.Errorf("json unmarshal error: %v", err)
	}

	if res.Error != nil {
		return nil, fmt.Errorf("RPC Error: %s (code %d)", res.Error.Message, res.Error.Code)
	}
	return &res, nil
}

// ============================================================
// UI Helpers
// ============================================================

func printBanner() {
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║       VALIDATOR TRANSITION TEST SUITE                     ║")
	fmt.Println("║       Automated fork detection during transitions         ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()
}

func (s *TestSuite) waitWithProgress(duration time.Duration, label string) {
	endTime := time.Now().Add(duration)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			remaining := time.Until(endTime)
			if remaining <= 0 {
				return
			}
			forks := s.watcher.phaseForks.Load()
			checks := s.watcher.phaseChecks.Load()
			symbol := "✅"
			if forks > 0 {
				symbol = "❌"
			}
			fmt.Printf("   ⏱️  %s — còn %ds | %s %d checks, %d forks\n",
				label, int(remaining.Seconds()), symbol, checks, forks)
		default:
			if time.Now().After(endTime) {
				return
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func (s *TestSuite) printPhaseResult(num int, r PhaseResult) {
	symbol := "✅"
	status := "PASS"
	if !r.Passed {
		symbol = "❌"
		status = "FAIL"
	}
	fmt.Printf("\n   %s Phase %d [%s]: %s — %d checks, %d forks (%s) %s\n\n",
		symbol, num, status, r.Name, r.Checks, r.Forks, r.Duration.Round(time.Second), r.Details)
}

func (s *TestSuite) printReport(totalDuration time.Duration) {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║            VALIDATOR TRANSITION TEST REPORT               ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════╣")

	allPassed := true
	totalPhaseChecks := int64(0)
	totalPhaseForks := int64(0)

	for i, r := range s.results {
		symbol := "✅"
		status := "PASS"
		if !r.Passed {
			symbol = "❌"
			status = "FAIL"
			allPassed = false
		}
		totalPhaseChecks += r.Checks
		totalPhaseForks += r.Forks

		details := ""
		if r.Details != "" {
			details = " (" + r.Details + ")"
		}
		fmt.Printf("║ Phase %d: %-25s %s %s%s\n", i+1, r.Name, symbol, status, details)
	}

	fmt.Println("╠═══════════════════════════════════════════════════════════╣")
	fmt.Printf("║ Total checks: %-6d Forks: %-6d Duration: %-12s ║\n",
		s.watcher.totalChecks.Load(), s.watcher.totalForks.Load(),
		totalDuration.Round(time.Second))

	if allPassed {
		fmt.Println("║ Result: ✅ ALL PHASES PASSED                              ║")
	} else {
		fmt.Println("║ Result: ❌ SOME PHASES FAILED                             ║")
	}
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
}

func (s *TestSuite) printLoopReport(totalDuration time.Duration) {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║         VALIDATOR TRANSITION LOOP TEST REPORT            ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════╣")

	allPassed := true
	var totalForks, totalChecks int64

	for _, rr := range s.roundResults {
		symbol := "✅"
		status := "PASS"
		if !rr.AllPassed {
			symbol = "❌"
			status = "FAIL"
			allPassed = false
		}
		totalForks += rr.TotalForks
		totalChecks += rr.TotalChecks
		fmt.Printf("║ Round %3d: %s %s | %4d checks, %3d forks | %s\n",
			rr.RoundNum, symbol, status, rr.TotalChecks, rr.TotalForks, rr.Duration.Round(time.Second))
	}

	fmt.Println("╠═══════════════════════════════════════════════════════════╣")
	fmt.Printf("║ Rounds: %-5d Checks: %-6d Forks: %-6d              ║\n",
		len(s.roundResults), totalChecks, totalForks)
	fmt.Printf("║ Duration: %-49s║\n", totalDuration.Round(time.Second))

	if allPassed {
		fmt.Println("║ Result: ✅ ALL ROUNDS PASSED                             ║")
	} else {
		failCount := 0
		for _, rr := range s.roundResults {
			if !rr.AllPassed {
				failCount++
			}
		}
		fmt.Printf("║ Result: ❌ %d/%d ROUNDS FAILED                            ║\n",
			failCount, len(s.roundResults))
	}
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
}
