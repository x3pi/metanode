// tps_measure — Distributed TPS measurement tool for multi-node blockchain clusters.
//
// This tool measures system TPS by polling JSON-RPC endpoints of multiple nodes
// running on different machines. It does NOT send transactions — use alongside
// tps_blast, tps_benchmark_e2e, or any TX load generator.
//
// Modes:
//
//	--watch    Live monitoring: polls nodes continuously, shows realtime TPS
//	--range    Post-hoc: analyzes blocks from --from to --to
//
// Usage:
//
//	tps_measure --nodes "http://192.168.1.231:8757,http://192.168.1.232:10749" --watch
//	tps_measure --nodes "http://192.168.1.231:8757" --from 100 --to 500
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Configuration
// ═══════════════════════════════════════════════════════════════════════════════

type Config struct {
	Nodes        []string      `json:"nodes"`
	Watch        bool          `json:"watch"`
	FromBlock    uint64        `json:"from_block"`
	ToBlock      uint64        `json:"to_block"`
	PollInterval time.Duration `json:"poll_interval_ms"`
	WindowSecs   int           `json:"window_seconds"`
	VerifyForks  bool          `json:"verify_forks"`
	OutFile      string        `json:"out_file"`
}

// ═══════════════════════════════════════════════════════════════════════════════
// Block Tracking
// ═══════════════════════════════════════════════════════════════════════════════

type BlockRecord struct {
	Number    uint64 `json:"number"`
	TxCount   int    `json:"tx_count"`
	Hash      string `json:"hash"`
	Timestamp uint64 `json:"timestamp"`
}

type TPSMetrics struct {
	InstantTPS    float64 `json:"instant_tps"`    // TXs in latest block / time since previous block
	WindowTPS     float64 `json:"window_tps"`     // TXs in rolling window / window duration
	CumulativeTPS float64 `json:"cumulative_tps"` // Total TXs / total elapsed time
	TotalTxs      int     `json:"total_txs"`
	TotalBlocks   int     `json:"total_blocks"`
	EmptyBlocks   int     `json:"empty_blocks"`
	MaxTxInBlock  int     `json:"max_tx_in_block"`
	AvgTxPerBlock float64 `json:"avg_tx_per_block"`
}

// ═══════════════════════════════════════════════════════════════════════════════
// Fork Detection
// ═══════════════════════════════════════════════════════════════════════════════

type ForkMismatch struct {
	BlockNumber uint64            `json:"block_number"`
	Field       string            `json:"field"`
	NodeHashes  map[string]string `json:"node_hashes"`
}

type ForkResult struct {
	Passed        bool           `json:"passed"`
	BlocksChecked int            `json:"blocks_checked"`
	Mismatches    int            `json:"mismatches"`
	NodesCompared int            `json:"nodes_compared"`
	Details       []ForkMismatch `json:"mismatch_details,omitempty"`
}

// ═══════════════════════════════════════════════════════════════════════════════
// Report
// ═══════════════════════════════════════════════════════════════════════════════

type Report struct {
	Config     Config        `json:"config"`
	StartBlock uint64        `json:"start_block"`
	EndBlock   uint64        `json:"end_block"`
	Duration   string        `json:"duration"`
	Metrics    TPSMetrics    `json:"metrics"`
	ForkCheck  *ForkResult   `json:"fork_check,omitempty"`
	Timestamp  string        `json:"timestamp"`
	Blocks     []BlockRecord `json:"blocks,omitempty"`

	// Per-block detail for range mode
	BlockDetails []BlockDetailRow `json:"block_details,omitempty"`
}

type BlockDetailRow struct {
	Number     uint64  `json:"number"`
	TxCount    int     `json:"tx_count"`
	Hash       string  `json:"hash"`
	InstantTPS float64 `json:"instant_tps"`
}

// ═══════════════════════════════════════════════════════════════════════════════
// Watch Mode — Live TPS monitoring
// ═══════════════════════════════════════════════════════════════════════════════

func watchMode(cfg Config) Report {
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║           🔍 TPS MEASURE — Live Watch Mode              ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Printf("Nodes:         %v\n", cfg.Nodes)
	fmt.Printf("Poll interval: %s\n", cfg.PollInterval)
	fmt.Printf("TPS window:    %ds\n", cfg.WindowSecs)
	fmt.Printf("Fork verify:   %v\n", cfg.VerifyForks)
	fmt.Println()

	// Use first node as primary for block data
	primary := NewRPCClient(cfg.Nodes[0])

	// Get starting block
	startBlock, err := primary.GetBlockNumber()
	if err != nil {
		fmt.Printf("❌ Failed to connect to primary node %s: %v\n", cfg.Nodes[0], err)
		os.Exit(1)
	}
	fmt.Printf("🏁 Starting block: %d\n", startBlock)
	fmt.Printf("⏳ Watching for new blocks... (Ctrl+C to stop)\n\n")

	// Setup signal handler for graceful exit
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	lastBlockNum := startBlock
	var blocks []BlockRecord
	totalTxs := 0
	emptyBlocks := 0
	maxTxInBlock := 0
	watchStart := time.Now()

	// Rolling window tracking
	type windowEntry struct {
		ts      time.Time
		txCount int
	}
	var windowEntries []windowEntry

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Table header
	fmt.Printf("  %-8s  %8s  %12s  %12s  %12s  %s\n",
		"Block", "TXs", "Instant", "Window", "Cumulative", "Hash")
	fmt.Printf("  %-8s  %8s  %12s  %12s  %12s  %s\n",
		"────────", "────────", "────────────", "────────────", "────────────", "──────────────")

loop:
	for {
		select {
		case <-sigCh:
			fmt.Println("\n\n🛑 Received interrupt signal, generating report...")
			break loop
		case <-ticker.C:
			currentBlock, err := primary.GetBlockNumber()
			if err != nil {
				fmt.Printf("\r  ⚠️  RPC error: %v   ", err)
				continue
			}

			if currentBlock <= lastBlockNum {
				continue
			}

			now := time.Now()

			for bn := lastBlockNum + 1; bn <= currentBlock; bn++ {
				blk, err := primary.GetBlockByNumber(bn)
				if err != nil || blk == nil {
					continue
				}

				txCount := len(blk.Transactions)
				totalTxs += txCount

				rec := BlockRecord{
					Number:    bn,
					TxCount:   txCount,
					Hash:      blk.Hash,
					Timestamp: blk.Timestamp,
				}
				blocks = append(blocks, rec)

				if txCount == 0 {
					emptyBlocks++
				}
				if txCount > maxTxInBlock {
					maxTxInBlock = txCount
				}

				// Add to window
				windowEntries = append(windowEntries, windowEntry{ts: now, txCount: txCount})

				// Compute instant TPS
				instantTPS := float64(0)
				if len(blocks) >= 2 {
					prev := blocks[len(blocks)-2]
					if blk.Timestamp > prev.Timestamp && prev.Timestamp > 0 {
						dt := float64(blk.Timestamp - prev.Timestamp)
						if dt > 0 {
							instantTPS = float64(txCount) / dt
						}
					}
				}

				// Compute window TPS
				windowCutoff := now.Add(-time.Duration(cfg.WindowSecs) * time.Second)
				windowTxs := 0
				firstWindowIdx := 0
				for i, e := range windowEntries {
					if e.ts.After(windowCutoff) {
						firstWindowIdx = i
						break
					}
				}
				windowEntries = windowEntries[firstWindowIdx:]
				for _, e := range windowEntries {
					windowTxs += e.txCount
				}
				windowDur := now.Sub(windowEntries[0].ts).Seconds()
				windowTPS := float64(0)
				if windowDur > 0 {
					windowTPS = float64(windowTxs) / windowDur
				}

				// Compute cumulative TPS
				elapsed := now.Sub(watchStart).Seconds()
				cumulativeTPS := float64(0)
				if elapsed > 0 {
					cumulativeTPS = float64(totalTxs) / elapsed
				}

				// Truncate hash for display
				shortHash := blk.Hash
				if len(shortHash) > 14 {
					shortHash = shortHash[:14] + "…"
				}

				fmt.Printf("  %-8d  %8d  %10.0f/s  %10.0f/s  %10.0f/s  %s\n",
					bn, txCount, instantTPS, windowTPS, cumulativeTPS, shortHash)
			}
			lastBlockNum = currentBlock
		}
	}

	// Compute final metrics
	elapsed := time.Since(watchStart)
	avgTx := float64(0)
	if len(blocks) > 0 {
		avgTx = float64(totalTxs) / float64(len(blocks))
	}
	cumulativeTPS := float64(0)
	if elapsed.Seconds() > 0 {
		cumulativeTPS = float64(totalTxs) / elapsed.Seconds()
	}

	endBlock := startBlock
	if len(blocks) > 0 {
		endBlock = blocks[len(blocks)-1].Number
	}

	metrics := TPSMetrics{
		CumulativeTPS: cumulativeTPS,
		TotalTxs:      totalTxs,
		TotalBlocks:   len(blocks),
		EmptyBlocks:   emptyBlocks,
		MaxTxInBlock:  maxTxInBlock,
		AvgTxPerBlock: avgTx,
	}

	// Fork check
	var forkResult *ForkResult
	if cfg.VerifyForks && len(cfg.Nodes) >= 2 && endBlock > startBlock {
		fmt.Println("\n═══ Fork Safety Verification ═══")
		fr := checkForksSafe(cfg.Nodes, startBlock, endBlock)
		forkResult = &fr
	}

	// Print summary
	printWatchSummary(startBlock, endBlock, elapsed, metrics, forkResult)

	return Report{
		Config:     cfg,
		StartBlock: startBlock,
		EndBlock:   endBlock,
		Duration:   elapsed.Round(time.Millisecond).String(),
		Metrics:    metrics,
		ForkCheck:  forkResult,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Blocks:     blocks,
	}
}

func printWatchSummary(startBlock, endBlock uint64, elapsed time.Duration, m TPSMetrics, fr *ForkResult) {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║              📊 TPS MEASUREMENT RESULTS                 ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════╣")
	fmt.Printf("  📦 Block range:      %d → %d (%d blocks)\n", startBlock, endBlock, m.TotalBlocks)
	fmt.Printf("  ⏱  Duration:         %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  📥 Total TXs:        %d\n", m.TotalTxs)
	fmt.Printf("  🚀 Cumulative TPS:   %.0f tx/s\n", m.CumulativeTPS)
	fmt.Printf("  📈 Max TXs/block:    %d\n", m.MaxTxInBlock)
	fmt.Printf("  📉 Avg TXs/block:    %.1f\n", m.AvgTxPerBlock)
	fmt.Printf("  👻 Empty blocks:     %d\n", m.EmptyBlocks)

	if fr != nil {
		fmt.Println("  ─────────────────────────────────────────────────────────")
		if fr.Passed {
			fmt.Printf("  🛡  Fork check:      ✅ PASSED (%d blocks, %d nodes)\n",
				fr.BlocksChecked, fr.NodesCompared)
		} else {
			fmt.Printf("  🛡  Fork check:      ❌ FAILED (%d mismatches in %d blocks)\n",
				fr.Mismatches, fr.BlocksChecked)
		}
	}
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Range Mode — Post-hoc block analysis
// ═══════════════════════════════════════════════════════════════════════════════

func rangeMode(cfg Config) Report {
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║           📊 TPS MEASURE — Range Analysis Mode          ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Printf("Nodes:         %v\n", cfg.Nodes)
	fmt.Printf("Block range:   %d → %d\n", cfg.FromBlock, cfg.ToBlock)
	fmt.Printf("Fork verify:   %v\n", cfg.VerifyForks)
	fmt.Println()

	primary := NewRPCClient(cfg.Nodes[0])

	// If ToBlock is 0, use latest
	if cfg.ToBlock == 0 {
		latest, err := primary.GetBlockNumber()
		if err != nil {
			fmt.Printf("❌ Failed to get latest block: %v\n", err)
			os.Exit(1)
		}
		cfg.ToBlock = latest
		fmt.Printf("📡 Latest block: %d\n", cfg.ToBlock)
	}

	totalBlocks := int(cfg.ToBlock - cfg.FromBlock)
	fmt.Printf("⏳ Fetching %d blocks...\n\n", totalBlocks)

	var blocks []BlockRecord
	var details []BlockDetailRow
	totalTxs := 0
	emptyBlocks := 0
	maxTxInBlock := 0

	var prevTimestamp uint64

	// Table header
	fmt.Printf("  %-8s  %8s  %12s  %s\n",
		"Block", "TXs", "Instant TPS", "Hash")
	fmt.Printf("  %-8s  %8s  %12s  %s\n",
		"────────", "────────", "────────────", "──────────────")

	for bn := cfg.FromBlock; bn <= cfg.ToBlock; bn++ {
		blk, err := primary.GetBlockByNumber(bn)
		if err != nil || blk == nil {
			if err != nil {
				fmt.Printf("  ⚠️  Block %d: %v\n", bn, err)
			}
			continue
		}

		txCount := len(blk.Transactions)
		totalTxs += txCount

		rec := BlockRecord{
			Number:    bn,
			TxCount:   txCount,
			Hash:      blk.Hash,
			Timestamp: blk.Timestamp,
		}
		blocks = append(blocks, rec)

		if txCount == 0 {
			emptyBlocks++
		}
		if txCount > maxTxInBlock {
			maxTxInBlock = txCount
		}

		// Compute instant TPS from block timestamps
		instantTPS := float64(0)
		if prevTimestamp > 0 && blk.Timestamp > prevTimestamp {
			dt := float64(blk.Timestamp - prevTimestamp)
			if dt > 0 {
				instantTPS = float64(txCount) / dt
			}
		}
		prevTimestamp = blk.Timestamp

		detail := BlockDetailRow{
			Number:     bn,
			TxCount:    txCount,
			Hash:       blk.Hash,
			InstantTPS: instantTPS,
		}
		details = append(details, detail)

		// Print every block or every 10th if range is large
		if totalBlocks <= 200 || bn%10 == 0 || bn == cfg.ToBlock {
			shortHash := blk.Hash
			if len(shortHash) > 14 {
				shortHash = shortHash[:14] + "…"
			}
			if instantTPS > 0 {
				fmt.Printf("  %-8d  %8d  %10.0f/s  %s\n", bn, txCount, instantTPS, shortHash)
			} else {
				fmt.Printf("  %-8d  %8d  %12s  %s\n", bn, txCount, "—", shortHash)
			}
		}

		// Progress for large ranges
		if totalBlocks > 200 && (bn-cfg.FromBlock)%100 == 0 {
			pct := float64(bn-cfg.FromBlock) / float64(totalBlocks) * 100
			fmt.Printf("\r  ⏳ Progress: %.0f%% (%d/%d blocks)   \n", pct, bn-cfg.FromBlock, totalBlocks)
		}
	}

	// Compute overall metrics
	avgTx := float64(0)
	if len(blocks) > 0 {
		avgTx = float64(totalTxs) / float64(len(blocks))
	}

	// Compute TPS from block timestamps
	var overallTPS float64
	if len(blocks) >= 2 {
		firstTs := blocks[0].Timestamp
		lastTs := blocks[len(blocks)-1].Timestamp
		if lastTs > firstTs {
			dt := float64(lastTs - firstTs)
			// Sum TXs excluding the first block (which has no interval before it)
			sumTxs := 0
			for i := 1; i < len(blocks); i++ {
				sumTxs += blocks[i].TxCount
			}
			overallTPS = float64(sumTxs) / dt
		}
	}

	// Also compute peak window TPS (best consecutive N blocks)
	peakWindowTPS := computePeakWindowTPS(blocks, cfg.WindowSecs)

	metrics := TPSMetrics{
		CumulativeTPS: overallTPS,
		WindowTPS:     peakWindowTPS,
		TotalTxs:      totalTxs,
		TotalBlocks:   len(blocks),
		EmptyBlocks:   emptyBlocks,
		MaxTxInBlock:  maxTxInBlock,
		AvgTxPerBlock: avgTx,
	}

	// Fork check
	var forkResult *ForkResult
	if cfg.VerifyForks && len(cfg.Nodes) >= 2 {
		fmt.Println("\n═══ Fork Safety Verification ═══")
		fr := checkForksSafe(cfg.Nodes, cfg.FromBlock, cfg.ToBlock)
		forkResult = &fr
	}

	// Print summary
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║              📊 RANGE ANALYSIS RESULTS                  ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════╣")
	fmt.Printf("  📦 Block range:      %d → %d (%d blocks)\n", cfg.FromBlock, cfg.ToBlock, len(blocks))
	fmt.Printf("  📥 Total TXs:        %d\n", totalTxs)
	fmt.Printf("  🚀 Overall TPS:      %.0f tx/s (from block timestamps)\n", overallTPS)
	if peakWindowTPS > 0 {
		fmt.Printf("  🔥 Peak %ds TPS:     %.0f tx/s\n", cfg.WindowSecs, peakWindowTPS)
	}
	fmt.Printf("  📈 Max TXs/block:    %d\n", maxTxInBlock)
	fmt.Printf("  📉 Avg TXs/block:    %.1f\n", avgTx)
	fmt.Printf("  👻 Empty blocks:     %d\n", emptyBlocks)

	if forkResult != nil {
		fmt.Println("  ─────────────────────────────────────────────────────────")
		if forkResult.Passed {
			fmt.Printf("  🛡  Fork check:      ✅ PASSED (%d blocks, %d nodes)\n",
				forkResult.BlocksChecked, forkResult.NodesCompared)
		} else {
			fmt.Printf("  🛡  Fork check:      ❌ FAILED (%d mismatches in %d blocks)\n",
				forkResult.Mismatches, forkResult.BlocksChecked)
		}
	}
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")

	return Report{
		Config:       cfg,
		StartBlock:   cfg.FromBlock,
		EndBlock:     cfg.ToBlock,
		Metrics:      metrics,
		ForkCheck:    forkResult,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		BlockDetails: details,
	}
}

// computePeakWindowTPS finds the highest TPS within any window of windowSecs seconds.
func computePeakWindowTPS(blocks []BlockRecord, windowSecs int) float64 {
	if len(blocks) < 2 {
		return 0
	}

	peakTPS := float64(0)

	for i := 0; i < len(blocks); i++ {
		windowTxs := 0
		endTs := blocks[i].Timestamp + uint64(windowSecs)

		for j := i; j < len(blocks); j++ {
			if blocks[j].Timestamp > endTs {
				break
			}
			windowTxs += blocks[j].TxCount
		}

		dt := float64(windowSecs)
		tps := float64(windowTxs) / dt
		if tps > peakTPS {
			peakTPS = tps
		}
	}

	return math.Round(peakTPS)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Fork Checking — compare block hashes across multiple nodes
// ═══════════════════════════════════════════════════════════════════════════════

func checkForksSafe(nodes []string, startBlock, endBlock uint64) ForkResult {
	result := ForkResult{
		Passed:        true,
		NodesCompared: len(nodes),
	}

	if len(nodes) < 2 {
		fmt.Println("  ⏭️  Fork check skipped (need 2+ nodes)")
		return result
	}

	clients := make([]*RPCClient, len(nodes))
	for i, nodeURL := range nodes {
		clients[i] = NewRPCClient(nodeURL)
	}

	fmt.Printf("  🔍 Checking forks: blocks %d → %d across %d nodes...\n",
		startBlock, endBlock, len(nodes))

	// Limit fork check to reasonable number of blocks
	maxCheck := uint64(1000)
	checkFrom := startBlock + 1
	checkTo := endBlock
	if checkTo-checkFrom > maxCheck {
		checkFrom = checkTo - maxCheck
		fmt.Printf("  ℹ️  Limiting fork check to last %d blocks (%d → %d)\n", maxCheck, checkFrom, checkTo)
	}

	var mu sync.Mutex

	for bn := checkFrom; bn <= checkTo; bn++ {
		result.BlocksChecked++

		blocksByNode := make(map[string]*BlockFull)
		var wg sync.WaitGroup
		var fetchMu sync.Mutex

		for i, client := range clients {
			wg.Add(1)
			go func(nodeIdx int, c *RPCClient) {
				defer wg.Done()
				blk, err := c.GetBlockFull(bn)
				if err != nil || blk == nil {
					return
				}
				fetchMu.Lock()
				blocksByNode[nodes[nodeIdx]] = blk
				fetchMu.Unlock()
			}(i, client)
		}
		wg.Wait()

		if len(blocksByNode) < 2 {
			continue
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
				mu.Lock()
				result.Passed = false
				result.Mismatches++
				result.Details = append(result.Details, ForkMismatch{
					BlockNumber: bn,
					Field:       field.name,
					NodeHashes:  values,
				})
				mu.Unlock()

				fmt.Printf("  ❌ FORK at block %d: %s mismatch!\n", bn, field.name)
				for node, hash := range values {
					fmt.Printf("     %s: %s\n", node, hash)
				}
			}
		}

		// Progress
		checked := bn - checkFrom + 1
		total := checkTo - checkFrom + 1
		if total > 50 && checked%50 == 0 {
			fmt.Printf("\r  🔍 Fork check: %d/%d blocks...   ", checked, total)
		}
	}

	if result.Passed {
		fmt.Printf("  ✅ Fork check PASSED: %d blocks, 0 mismatches across %d nodes\n",
			result.BlocksChecked, result.NodesCompared)
	} else {
		fmt.Printf("  ❌ Fork check FAILED: %d mismatches in %d blocks\n",
			result.Mismatches, result.BlocksChecked)
	}

	return result
}

// ═══════════════════════════════════════════════════════════════════════════════
// Report Saving
// ═══════════════════════════════════════════════════════════════════════════════

func saveReport(report Report, outFile string) {
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
// Connectivity Check
// ═══════════════════════════════════════════════════════════════════════════════

// checkConnectivity tests all node RPC endpoints and returns only reachable ones.
func checkConnectivity(nodes []string) []string {
	fmt.Println("🔌 Checking node connectivity...")

	type checkResult struct {
		node    string
		block   uint64
		err     error
		latency time.Duration
	}

	results := make([]checkResult, len(nodes))
	var wg sync.WaitGroup

	for i, node := range nodes {
		wg.Add(1)
		go func(idx int, nodeURL string) {
			defer wg.Done()
			client := NewRPCClient(nodeURL)
			start := time.Now()
			block, err := client.GetBlockNumber()
			results[idx] = checkResult{
				node:    nodeURL,
				block:   block,
				err:     err,
				latency: time.Since(start),
			}
		}(i, node)
	}
	wg.Wait()

	var reachable []string
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("  ❌ %s — UNREACHABLE: %v\n", r.node, r.err)
		} else {
			fmt.Printf("  ✅ %s — block: %d (latency: %s)\n", r.node, r.block, r.latency.Round(time.Millisecond))
			reachable = append(reachable, r.node)
		}
	}

	if len(reachable) == 0 {
		fmt.Println("\n❌ No reachable nodes! Check:")
		fmt.Println("   1. Are the RPC endpoints correct?")
		fmt.Println("   2. RPC may be bound to localhost only — use SSH tunnel:")
		fmt.Println("      ssh -L 8757:127.0.0.1:8757 user@remote-server")
		fmt.Println("   3. Or run this tool directly on the node's server")
		os.Exit(1)
	}

	fmt.Printf("  📡 %d/%d nodes reachable\n\n", len(reachable), len(nodes))
	return reachable
}

// ═══════════════════════════════════════════════════════════════════════════════
// Main
// ═══════════════════════════════════════════════════════════════════════════════

func main() {
	nodesFlag := flag.String("nodes", "http://localhost:8757", "Comma-separated RPC endpoints")
	watch := flag.Bool("watch", false, "Live monitoring mode")
	fromBlock := flag.Uint64("from", 0, "Start block for range analysis")
	toBlock := flag.Uint64("to", 0, "End block for range analysis (0 = latest)")
	pollInterval := flag.Duration("poll", 2*time.Second, "Polling interval for watch mode")
	windowSecs := flag.Int("window", 30, "Rolling TPS window in seconds")
	verifyForks := flag.Bool("verify-forks", true, "Enable multi-node fork checking")
	outFile := flag.String("out", "", "Output JSON report file (default: auto-generated)")
	flag.Parse()

	cfg := Config{
		Nodes:        strings.Split(*nodesFlag, ","),
		Watch:        *watch,
		FromBlock:    *fromBlock,
		ToBlock:      *toBlock,
		PollInterval: *pollInterval,
		WindowSecs:   *windowSecs,
		VerifyForks:  *verifyForks,
		OutFile:      *outFile,
	}

	// Check mode
	if !cfg.Watch && cfg.FromBlock == 0 && cfg.ToBlock == 0 {
		fmt.Println("Usage:")
		fmt.Println("  tps_measure --nodes <endpoints> --watch              # Live monitoring")
		fmt.Println("  tps_measure --nodes <endpoints> --from 100 --to 500  # Range analysis")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  tps_measure --nodes \"http://192.168.1.231:8757,http://192.168.1.232:10749\" --watch")
		fmt.Println("  tps_measure --nodes \"http://192.168.1.231:8757\" --from 1 --to 100")
		fmt.Println()
		flag.PrintDefaults()
		os.Exit(0)
	}

	// Pre-check connectivity (fail fast instead of hanging)
	cfg.Nodes = checkConnectivity(cfg.Nodes)

	// Determine mode
	var report Report
	if cfg.Watch {
		report = watchMode(cfg)
	} else {
		report = rangeMode(cfg)
	}

	// Save report
	if cfg.OutFile == "" {
		cfg.OutFile = fmt.Sprintf("tps_measure_%s.json", time.Now().Format("20060102_150405"))
	}
	saveReport(report, cfg.OutFile)
	fmt.Printf("\n📄 Report saved: %s\n", cfg.OutFile)
}
