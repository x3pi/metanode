// E2E TPS Benchmark — Automated end-to-end TPS measurement with fork verification.
//
// This tool:
//  1. Connects to one or more nodes via JSON-RPC HTTP
//  2. Prepares signed transactions offline
//  3. Sends them via eth_sendRawTransaction concurrently
//  4. Polls block height for settlement (empty block streak detection)
//  5. Verifies fork safety: compare block hashes across all nodes
//  6. Produces a detailed JSON report with block statistics
//
// Usage:
//
//	go run . [flags]
//
// Flags:
//
//	-nodes      comma-separated RPC endpoints (default: http://localhost:8757)
//	-accounts   number of sending accounts (default: 100)
//	-txs        total transactions per round (default: 1000)
//	-workers    concurrent send workers per node (default: 10)
//	-settle     max seconds to wait for settlement (default: 30)
//	-rounds     number of benchmark rounds (default: 1)
//	-json       output results as JSON only (default: false)
//	-chain-id   chain ID (default: 9876)
//	-verify-forks  enable multi-node fork checking (default: true)
//	-out        output JSON file name (default: auto-generated timestamp)
//	-cooldown   seconds between rounds (default: 5)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Configuration
// ═══════════════════════════════════════════════════════════════════════════════

type BenchConfig struct {
	Nodes       []string `json:"nodes"`
	NumAccounts int      `json:"num_accounts"`
	NumTxs      int      `json:"num_txs"`
	Workers     int      `json:"workers"`
	SettleSecs  int      `json:"settle_seconds"`
	Rounds      int      `json:"rounds"`
	JSONOutput  bool     `json:"-"`
	ChainID     uint64   `json:"chain_id"`
	VerifyForks bool     `json:"verify_forks"`
	CooldownSec int      `json:"cooldown_seconds"`
}

// ═══════════════════════════════════════════════════════════════════════════════
// Results
// ═══════════════════════════════════════════════════════════════════════════════

type BlockStats struct {
	StartBlock    uint64  `json:"start_block"`
	EndBlock      uint64  `json:"end_block"`
	TotalBlocks   int     `json:"total_blocks"`
	EmptyBlocks   int     `json:"empty_blocks"`
	MaxTxInBlock  int     `json:"max_txs_in_block"`
	TotalTxBlocks int     `json:"total_txs_in_blocks"`
	AvgTxPerBlock float64 `json:"avg_txs_per_block"`
}

type RoundResult struct {
	Round        int        `json:"round"`
	TxsSent      int        `json:"txs_sent"`
	TxsConfirmed int        `json:"txs_confirmed"`
	InjectTimeMs int64      `json:"inject_time_ms"`
	SettleTimeMs int64      `json:"settle_time_ms"`
	InjectTPS    float64    `json:"inject_tps"`
	SettleTPS    float64    `json:"settle_tps"`
	SendErrors   int64      `json:"send_errors"`
	BlockStats   BlockStats `json:"block_stats"`
}

type ForkCheckResult struct {
	Passed          bool           `json:"passed"`
	BlocksChecked   int            `json:"blocks_checked"`
	Mismatches      int            `json:"mismatches"`
	NodesCompared   int            `json:"nodes_compared"`
	MismatchDetails []ForkMismatch `json:"mismatch_details,omitempty"`
}

type ForkMismatch struct {
	BlockNumber uint64            `json:"block_number"`
	NodeHashes  map[string]string `json:"node_hashes"`
	Field       string            `json:"field"` // "hash", "stateRoot", "transactionsRoot", "receiptsRoot"
}

type BenchReport struct {
	Config    BenchConfig      `json:"config"`
	Rounds    []RoundResult    `json:"rounds"`
	ForkCheck *ForkCheckResult `json:"fork_check,omitempty"`
	AvgTPS    float64          `json:"avg_settle_tps"`
	MaxTPS    float64          `json:"max_settle_tps"`
	MinTPS    float64          `json:"min_settle_tps"`
	Timestamp string           `json:"timestamp"`
}

// ═══════════════════════════════════════════════════════════════════════════════
// Account Generation — deterministic from seed
// ═══════════════════════════════════════════════════════════════════════════════

type TestAccount struct {
	PrivateKey []byte
	Address    common.Address
	Nonce      uint64
}

func generateAccounts(n int) []TestAccount {
	accounts := make([]TestAccount, n)
	for i := 0; i < n; i++ {
		seed := make([]byte, 32)
		seed[0] = byte(i >> 24)
		seed[1] = byte(i >> 16)
		seed[2] = byte(i >> 8)
		seed[3] = byte(i)
		key := crypto.Keccak256(append([]byte("tps-bench-e2e-"), seed...))
		ecdsaKey, err := crypto.ToECDSA(key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to generate key %d: %v\n", i, err)
			os.Exit(1)
		}
		accounts[i] = TestAccount{
			PrivateKey: key,
			Address:    crypto.PubkeyToAddress(ecdsaKey.PublicKey),
		}
	}
	return accounts
}

// ═══════════════════════════════════════════════════════════════════════════════
// Transaction Building
// ═══════════════════════════════════════════════════════════════════════════════

func buildTransactions(accounts []TestAccount, numTxs int, chainID uint64) [][]byte {
	txPayloads := make([][]byte, 0, numTxs)
	amount := big.NewInt(0)
	relBytes := make([][]byte, 1)
	relBytes[0] = common.Hex2Bytes("1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5")

	for i := 0; i < numTxs; i++ {
		acc := accounts[i%len(accounts)]
		ecdsaKey, _ := crypto.ToECDSA(acc.PrivateKey)
		fromAddr := crypto.PubkeyToAddress(ecdsaKey.PublicKey)
		destAddr := common.HexToAddress(fmt.Sprintf("0x000000000000000000000000000000000000%04x", i))

		nonce := acc.Nonce + uint64(i/len(accounts))

		tx := transaction.NewTransaction(
			fromAddr,
			destAddr,
			amount,
			10000000,
			1000000,
			0,
			nil,
			relBytes,
			common.Hash{},
			common.Hash{},
			nonce,
			chainID,
		)

		var pKey p_common.PrivateKey
		copy(pKey[:], acc.PrivateKey)
		tx.SetSign(pKey)

		bTx, err := tx.Marshal()
		if err != nil {
			continue
		}
		txPayloads = append(txPayloads, bTx)
	}
	return txPayloads
}

// ═══════════════════════════════════════════════════════════════════════════════
// Transaction Sending — concurrent HTTP JSON-RPC via eth_sendRawTransaction
// ═══════════════════════════════════════════════════════════════════════════════

func sendTransactions(nodes []string, payloads [][]byte, workers int, quiet bool) (sent int64, errors int64, elapsed time.Duration) {
	start := time.Now()

	var wg sync.WaitGroup
	workCh := make(chan []byte, len(payloads))

	for _, p := range payloads {
		if p != nil {
			workCh <- p
		}
	}
	close(workCh)

	// Create RPC clients for each node
	rpcClients := make([]*RPCClient, len(nodes))
	for i, nodeURL := range nodes {
		rpcClients[i] = NewRPCClient(nodeURL)
	}

	totalWorkers := workers * len(nodes)
	wg.Add(totalWorkers)

	for nodeIdx := range nodes {
		for w := 0; w < workers; w++ {
			go func(client *RPCClient) {
				defer wg.Done()
				for payload := range workCh {
					_, err := client.SendRawTransaction(payload)
					if err != nil {
						atomic.AddInt64(&errors, 1)
					} else {
						atomic.AddInt64(&sent, 1)
					}

					if !quiet {
						s := atomic.LoadInt64(&sent)
						e := atomic.LoadInt64(&errors)
						total := s + e
						if total%200 == 0 {
							elapsed := time.Since(start)
							rate := float64(s) / elapsed.Seconds()
							fmt.Printf("\r  📤 [%d/%d] %.0f tx/s | ⚠️ %d errors | %s   ",
								total, len(payloads), rate, e, elapsed.Round(time.Millisecond))
						}
					}
				}
			}(rpcClients[nodeIdx])
		}
	}

	wg.Wait()
	return sent, errors, time.Since(start)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Settlement Tracking — poll block height and count TXs
// ═══════════════════════════════════════════════════════════════════════════════

func waitForSettlement(rpcClient *RPCClient, startBlock uint64, maxWait time.Duration, expectedTxs int, quiet bool) (BlockStats, time.Duration) {
	pollInterval := 2 * time.Second
	processStart := time.Now()

	emptyBlockStreak := 0
	rpcErrorStreak := 0
	lastBlockNum := startBlock
	totalTxsInBlocks := 0
	requiredEmptyStreak := 6 // 6 × 2s = 12s idle
	maxRpcErrorStreak := 5

	stats := BlockStats{StartBlock: startBlock}

	for time.Since(processStart) < maxWait {
		time.Sleep(pollInterval)

		currentBlockNum, err := rpcClient.GetBlockNumber()
		if err != nil {
			rpcErrorStreak++
			if !quiet {
				fmt.Printf("\r  📡 RPC error (%d/%d): %v   ",
					rpcErrorStreak, maxRpcErrorStreak, err)
			}
			if rpcErrorStreak >= maxRpcErrorStreak {
				if !quiet {
					fmt.Printf("\n  ⚠️  RPC unreachable — treating as chain idle\n")
				}
				break
			}
			continue
		}
		rpcErrorStreak = 0

		newTxs := 0
		for bn := lastBlockNum + 1; bn <= currentBlockNum; bn++ {
			blk, err := rpcClient.GetBlockByNumber(bn)
			if err != nil || blk == nil {
				break
			}
			txCount := len(blk.Transactions)
			newTxs += txCount
			totalTxsInBlocks += txCount
			stats.TotalBlocks++

			if txCount == 0 {
				stats.EmptyBlocks++
			}
			if txCount > stats.MaxTxInBlock {
				stats.MaxTxInBlock = txCount
			}

			lastBlockNum = bn
		}

		if !quiet {
			pct := float64(totalTxsInBlocks) / float64(expectedTxs) * 100
			if pct > 100 {
				pct = 100
			}
			fmt.Printf("\r  📡 [%s] Block: %d | TXs: %d/%d (%.0f%%) | +%d new   ",
				time.Since(processStart).Round(time.Millisecond),
				currentBlockNum, totalTxsInBlocks, expectedTxs, pct, newTxs)
		}

		if newTxs == 0 {
			emptyBlockStreak++
			if emptyBlockStreak >= requiredEmptyStreak {
				if !quiet {
					fmt.Printf("\n  ✅ Chain idle for %ds — %d TXs in blocks\n",
						emptyBlockStreak*int(pollInterval.Seconds()), totalTxsInBlocks)
				}
				break
			}
		} else {
			emptyBlockStreak = 0
		}
	}

	stats.EndBlock = lastBlockNum
	stats.TotalTxBlocks = totalTxsInBlocks
	if stats.TotalBlocks > 0 {
		stats.AvgTxPerBlock = float64(totalTxsInBlocks) / float64(stats.TotalBlocks)
	}

	return stats, time.Since(processStart)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Fork Checking — compare block hashes across multiple nodes
// ═══════════════════════════════════════════════════════════════════════════════

func checkForks(nodes []string, startBlock, endBlock uint64, quiet bool) ForkCheckResult {
	result := ForkCheckResult{
		Passed:        true,
		NodesCompared: len(nodes),
	}

	if len(nodes) < 2 {
		if !quiet {
			fmt.Println("  ⏭️  Fork check skipped (need 2+ nodes)")
		}
		return result
	}

	clients := make([]*RPCClient, len(nodes))
	for i, nodeURL := range nodes {
		clients[i] = NewRPCClient(nodeURL)
	}

	if !quiet {
		fmt.Printf("  🔍 Checking forks: blocks %d → %d across %d nodes...\n",
			startBlock, endBlock, len(nodes))
	}

	for bn := startBlock + 1; bn <= endBlock; bn++ {
		result.BlocksChecked++

		blocksByNode := make(map[string]*BlockFull)
		for i, client := range clients {
			blk, err := client.GetBlockFull(bn)
			if err != nil || blk == nil {
				continue
			}
			blocksByNode[nodes[i]] = blk
		}

		if len(blocksByNode) < 2 {
			continue // Not enough nodes responded for this block
		}

		// Compare hashes across all nodes
		fields := []struct {
			name string
			get  func(*BlockFull) string
		}{
			{"hash", func(b *BlockFull) string { return b.Hash }},
			{"stateRoot", func(b *BlockFull) string { return b.StateRoot }},
			{"transactionsRoot", func(b *BlockFull) string { return b.TransactionsRoot }},
			{"receiptsRoot", func(b *BlockFull) string { return b.ReceiptsRoot }},
		}

		for _, field := range fields {
			values := make(map[string]string)
			var firstValue string
			mismatch := false

			for node, blk := range blocksByNode {
				val := field.get(blk)
				values[node] = val
				if firstValue == "" {
					firstValue = val
				} else if val != firstValue {
					mismatch = true
				}
			}

			if mismatch {
				result.Passed = false
				result.Mismatches++
				result.MismatchDetails = append(result.MismatchDetails, ForkMismatch{
					BlockNumber: bn,
					NodeHashes:  values,
					Field:       field.name,
				})
				if !quiet {
					fmt.Printf("  ❌ FORK at block %d: %s mismatch!\n", bn, field.name)
					for node, hash := range values {
						fmt.Printf("     %s: %s\n", node, hash)
					}
				}
			}
		}
	}

	if !quiet {
		if result.Passed {
			fmt.Printf("  ✅ Fork check PASSED: %d blocks, 0 mismatches\n", result.BlocksChecked)
		} else {
			fmt.Printf("  ❌ Fork check FAILED: %d mismatches in %d blocks\n",
				result.Mismatches, result.BlocksChecked)
		}
	}

	return result
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark Runner
// ═══════════════════════════════════════════════════════════════════════════════

func runBenchmark(cfg BenchConfig) BenchReport {
	report := BenchReport{
		Config:    cfg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		MinTPS:    1e18,
	}

	quiet := cfg.JSONOutput

	accounts := generateAccounts(cfg.NumAccounts)
	if !quiet {
		fmt.Printf("🔑 Generated %d test accounts\n", len(accounts))
	}

	// Use first node for polling
	rpcClient := NewRPCClient(cfg.Nodes[0])

	var globalStartBlock, globalEndBlock uint64

	for round := 1; round <= cfg.Rounds; round++ {
		if !quiet {
			fmt.Printf("\n═══ Round %d/%d ═══\n", round, cfg.Rounds)
		}

		// Record start block
		startBlock, err := rpcClient.GetBlockNumber()
		if err != nil {
			if !quiet {
				fmt.Printf("  ⚠️  Failed to get start block: %v\n", err)
			}
			startBlock = 0
		}
		if round == 1 {
			globalStartBlock = startBlock
		}

		if !quiet {
			fmt.Printf("  🏁 Start block: %d\n", startBlock)
		}

		// Build transactions
		if !quiet {
			fmt.Printf("  📦 Building %d transactions...\n", cfg.NumTxs)
		}
		payloads := buildTransactions(accounts, cfg.NumTxs, cfg.ChainID)
		if !quiet {
			fmt.Printf("  ✅ Built %d transactions\n", len(payloads))
		}

		// Send transactions
		if !quiet {
			fmt.Printf("  🚀 Sending %d txs across %d nodes (%d workers each)...\n",
				len(payloads), len(cfg.Nodes), cfg.Workers)
		}
		sent, sendErrors, injectTime := sendTransactions(cfg.Nodes, payloads, cfg.Workers, quiet)

		injectTPS := float64(0)
		if injectTime.Seconds() > 0 {
			injectTPS = float64(sent) / injectTime.Seconds()
		}

		if !quiet {
			fmt.Printf("\n  📤 Injected %d txs in %s (%.0f tx/s, %d errors)\n",
				sent, injectTime.Round(time.Millisecond), injectTPS, sendErrors)
		}

		// Wait for settlement
		if !quiet {
			fmt.Printf("  ⏳ Waiting for settlement (max %ds)...\n", cfg.SettleSecs)
		}
		maxWait := time.Duration(cfg.SettleSecs) * time.Second
		stats, settleTime := waitForSettlement(rpcClient, startBlock, maxWait, int(sent), quiet)

		// Calculate settle TPS from block stats
		settleTPS := float64(0)
		if settleTime.Seconds() > 0 {
			settleTPS = float64(stats.TotalTxBlocks) / settleTime.Seconds()
		}

		globalEndBlock = stats.EndBlock

		result := RoundResult{
			Round:        round,
			TxsSent:      int(sent),
			TxsConfirmed: stats.TotalTxBlocks,
			InjectTimeMs: injectTime.Milliseconds(),
			SettleTimeMs: settleTime.Milliseconds(),
			InjectTPS:    injectTPS,
			SettleTPS:    settleTPS,
			SendErrors:   sendErrors,
			BlockStats:   stats,
		}
		report.Rounds = append(report.Rounds, result)

		if settleTPS > report.MaxTPS {
			report.MaxTPS = settleTPS
		}
		if settleTPS < report.MinTPS {
			report.MinTPS = settleTPS
		}

		if !quiet {
			fmt.Printf("  📊 Round %d: %d TXs confirmed | %.0f inject TPS | %.0f settle TPS\n",
				round, stats.TotalTxBlocks, injectTPS, settleTPS)
		}

		// Cooldown between rounds
		if round < cfg.Rounds {
			if !quiet {
				fmt.Printf("  💤 Cooling down %ds...\n", cfg.CooldownSec)
			}
			time.Sleep(time.Duration(cfg.CooldownSec) * time.Second)
		}
	}

	// Compute average
	totalTPS := 0.0
	for _, r := range report.Rounds {
		totalTPS += r.SettleTPS
	}
	if len(report.Rounds) > 0 {
		report.AvgTPS = totalTPS / float64(len(report.Rounds))
	}

	// Fork check across all nodes
	if cfg.VerifyForks && len(cfg.Nodes) >= 2 && globalEndBlock > globalStartBlock {
		if !quiet {
			fmt.Println("\n═══ Fork Safety Verification ═══")
		}
		forkResult := checkForks(cfg.Nodes, globalStartBlock, globalEndBlock, quiet)
		report.ForkCheck = &forkResult
	}

	return report
}

// ═══════════════════════════════════════════════════════════════════════════════
// Output
// ═══════════════════════════════════════════════════════════════════════════════

func printReport(report BenchReport) {
	fmt.Println("\n╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║              E2E TPS BENCHMARK RESULTS                    ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════╣")

	for _, r := range report.Rounds {
		fmt.Printf("║ Round %d:                                                  ║\n", r.Round)
		fmt.Printf("║   TXs: %d sent → %d confirmed                       \n", r.TxsSent, r.TxsConfirmed)
		fmt.Printf("║   Inject: %.0f tx/s (%dms) | Settle: %.0f tx/s (%dms)\n",
			r.InjectTPS, r.InjectTimeMs, r.SettleTPS, r.SettleTimeMs)
		fmt.Printf("║   Blocks: %d (empty: %d, max: %d tx/blk, avg: %.1f)\n",
			r.BlockStats.TotalBlocks, r.BlockStats.EmptyBlocks,
			r.BlockStats.MaxTxInBlock, r.BlockStats.AvgTxPerBlock)
		if r.SendErrors > 0 {
			fmt.Printf("║   Send errors: %d                                        \n", r.SendErrors)
		}
	}

	fmt.Println("╠═══════════════════════════════════════════════════════════╣")
	fmt.Printf("║ Average TPS: %.0f  |  Max: %.0f  |  Min: %.0f\n",
		report.AvgTPS, report.MaxTPS, report.MinTPS)

	if report.ForkCheck != nil {
		fmt.Println("╠═══════════════════════════════════════════════════════════╣")
		if report.ForkCheck.Passed {
			fmt.Printf("║ Fork Check: ✅ PASSED (%d blocks, %d nodes)\n",
				report.ForkCheck.BlocksChecked, report.ForkCheck.NodesCompared)
		} else {
			fmt.Printf("║ Fork Check: ❌ FAILED (%d mismatches in %d blocks)\n",
				report.ForkCheck.Mismatches, report.ForkCheck.BlocksChecked)
		}
	}

	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
}

func saveReport(report BenchReport, outFile string) {
	f, err := os.Create(outFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create report file: %v\n", err)
		return
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to encode report: %v\n", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Main
// ═══════════════════════════════════════════════════════════════════════════════

func main() {
	logger.SetConfig(&logger.LoggerConfig{Flag: logger.FLAG_WARN, Outputs: []*os.File{os.Stdout}})

	nodesFlag := flag.String("nodes", "http://localhost:8757", "Comma-separated RPC endpoints")
	numAccounts := flag.Int("accounts", 100, "Number of sending accounts")
	numTxs := flag.Int("txs", 1000, "Total transactions per round")
	workers := flag.Int("workers", 10, "Concurrent workers per node")
	settleSecs := flag.Int("settle", 30, "Max seconds to wait for settlement")
	rounds := flag.Int("rounds", 1, "Number of benchmark rounds")
	jsonOutput := flag.Bool("json", false, "Output results as JSON only")
	chainID := flag.Uint64("chain-id", 9876, "Chain ID")
	verifyForks := flag.Bool("verify-forks", true, "Enable multi-node fork checking")
	outFile := flag.String("out", "", "Output JSON file (default: tps_report_<timestamp>.json)")
	cooldown := flag.Int("cooldown", 5, "Seconds between rounds")
	flag.Parse()

	cfg := BenchConfig{
		Nodes:       strings.Split(*nodesFlag, ","),
		NumAccounts: *numAccounts,
		NumTxs:      *numTxs,
		Workers:     *workers,
		SettleSecs:  *settleSecs,
		Rounds:      *rounds,
		JSONOutput:  *jsonOutput,
		ChainID:     *chainID,
		VerifyForks: *verifyForks,
		CooldownSec: *cooldown,
	}

	if !cfg.JSONOutput {
		fmt.Println("╔═══════════════════════════════════════════════════════════╗")
		fmt.Println("║           E2E TPS Benchmark                              ║")
		fmt.Println("╚═══════════════════════════════════════════════════════════╝")
		fmt.Printf("Nodes:    %v\n", cfg.Nodes)
		fmt.Printf("Accounts: %d  |  Txs/round: %d  |  Rounds: %d\n", cfg.NumAccounts, cfg.NumTxs, cfg.Rounds)
		fmt.Printf("Workers:  %d/node  |  Settle: %ds  |  ChainID: %d\n", cfg.Workers, cfg.SettleSecs, cfg.ChainID)
		fmt.Printf("Forks:    verify=%v  |  Cooldown: %ds\n", cfg.VerifyForks, cfg.CooldownSec)
	}

	report := runBenchmark(cfg)

	// Output
	if cfg.JSONOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(report)
	} else {
		printReport(report)

		// Also save JSON report
		if *outFile == "" {
			*outFile = fmt.Sprintf("tps_report_%s.json", time.Now().Format("20060102_150405"))
		}
		saveReport(report, *outFile)
		fmt.Printf("\n📄 Report saved: %s\n", *outFile)
	}
}
