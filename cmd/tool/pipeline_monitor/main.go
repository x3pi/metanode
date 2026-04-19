// pipeline_monitor — Real-time monitor for TX pipeline flow
// Polls Go Sub + Go Master /pipeline/stats endpoints and displays bottleneck analysis
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PipelineSnapshot mirrors processor.PipelineSnapshot
type PipelineSnapshot struct {
	NodeRole       string  `json:"node_role"`
	UptimeSeconds  float64 `json:"uptime_seconds"`
	TxsReceived    int64   `json:"txs_received"`
	PoolSize       int64   `json:"pool_size"`
	PendingSize    int64   `json:"pending_size"`
	TxsForwarded   int64   `json:"txs_forwarded"`
	BlocksReceived int64   `json:"blocks_received"`
	TxsCommitted   int64   `json:"txs_committed"`
	LastBlock      int64   `json:"last_block"`
	LastCommitUs   int64   `json:"last_commit_us"`
	Timestamp      string  `json:"timestamp"`
}

// NodeStats holds stats + metadata for a node
type NodeStats struct {
	Name    string
	URL     string
	Stats   *PipelineSnapshot
	Prev    *PipelineSnapshot
	Err     error
	IsGoSub bool
}

func fetchStats(url string) (*PipelineSnapshot, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/pipeline/stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var snap PipelineSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("invalid JSON: %s", string(body[:min(len(body), 100)]))
	}
	return &snap, nil
}

func resetStats(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(url+"/pipeline/reset", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
	clearScreen = "\033[H\033[2J"
)

func main() {
	goSubURL := flag.String("sub", "http://127.0.0.1:4201", "Go Sub HTTP URL (comma-separated for multiple)")
	goMasterURL := flag.String("master", "http://127.0.0.1:8747", "Go Master HTTP URL")
	interval := flag.Duration("interval", 1*time.Second, "Poll interval")
	reset := flag.Bool("reset", false, "Reset all counters and exit")
	flag.Parse()

	if *reset {
		subURLs := strings.Split(*goSubURL, ",")
		for _, url := range subURLs {
			url = strings.TrimSpace(url)
			if err := resetStats(url); err != nil {
				fmt.Printf("❌ Failed to reset %s: %v\n", url, err)
			} else {
				fmt.Printf("✅ Reset %s\n", url)
			}
		}
		if err := resetStats(*goMasterURL); err != nil {
			fmt.Printf("❌ Failed to reset %s: %v\n", *goMasterURL, err)
		} else {
			fmt.Printf("✅ Reset %s\n", *goMasterURL)
		}
		return
	}

	subURLs := strings.Split(*goSubURL, ",")
	nodes := make([]*NodeStats, 0, len(subURLs)+1)
	for i, url := range subURLs {
		url = strings.TrimSpace(url)
		nodes = append(nodes, &NodeStats{
			Name:    fmt.Sprintf("Go Sub %d", i),
			URL:     url,
			IsGoSub: true,
		})
	}
	nodes = append(nodes, &NodeStats{
		Name:    "Go Master",
		URL:     strings.TrimSpace(*goMasterURL),
		IsGoSub: false,
	})

	fmt.Printf("%s%s🔍 PIPELINE MONITOR started (poll: %v)%s\n", colorBold, colorCyan, *interval, colorReset)
	fmt.Printf("   Nodes: %s, %s\n", *goSubURL, *goMasterURL)
	fmt.Println("   Press Ctrl+C to stop")
	fmt.Println()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	poll := 0
	for {
		poll++
		// Fetch all stats
		for _, node := range nodes {
			node.Prev = node.Stats
			snap, err := fetchStats(node.URL)
			node.Stats = snap
			node.Err = err
		}

		// Clear screen and print
		fmt.Print(clearScreen)
		printDashboard(nodes, poll, *interval)

		<-ticker.C
	}
}

func printDashboard(nodes []*NodeStats, poll int, interval time.Duration) {
	now := time.Now().Format("15:04:05")
	fmt.Printf("%s%s═══════════════════════════════════════════════════════════════════════════%s\n", colorBold, colorCyan, colorReset)
	fmt.Printf("%s  🔍 PIPELINE MONITOR — TX Flow Tracker  [%s] Poll #%d%s\n", colorBold, now, poll, colorReset)
	fmt.Printf("%s═══════════════════════════════════════════════════════════════════════════%s\n", colorCyan, colorReset)
	fmt.Println()

	// Header
	fmt.Printf("  %-14s │ %8s │ %6s │ %7s │ %9s │ %9s │ %7s │ %8s │ %6s\n",
		"Node", "Received", "Pool", "Pending", "Forwarded", "Committed", "Block", "Commit", "Δ TPS")
	fmt.Printf("  %-14s─┼─%8s─┼─%6s─┼─%7s─┼─%9s─┼─%9s─┼─%7s─┼─%8s─┼─%6s\n",
		"──────────────", "────────", "──────", "───────", "─────────", "─────────", "───────", "────────", "──────")

	// Data rows
	for _, node := range nodes {
		if node.Err != nil {
			fmt.Printf("  %s%-14s%s │ %s%8s%s │\n", colorRed, node.Name, colorReset, colorRed, "OFFLINE", colorReset)
			continue
		}
		s := node.Stats
		if s == nil {
			continue
		}

		// Calculate delta TPS
		var deltaTPS string
		if node.Prev != nil {
			var delta int64
			if node.IsGoSub {
				delta = s.TxsForwarded - node.Prev.TxsForwarded
			} else {
				delta = s.TxsCommitted - node.Prev.TxsCommitted
			}
			tps := float64(delta) / interval.Seconds()
			if tps > 0 {
				deltaTPS = fmt.Sprintf("%s%.0f%s", colorGreen, tps, colorReset)
			} else {
				deltaTPS = fmt.Sprintf("%s%.0f%s", colorDim, tps, colorReset)
			}
		} else {
			deltaTPS = "—"
		}

		// Commit time formatting
		commitStr := "—"
		if s.LastCommitUs > 0 {
			commitMs := float64(s.LastCommitUs) / 1000.0
			if commitMs > 5000 {
				commitStr = fmt.Sprintf("%s%.0fms%s", colorRed, commitMs, colorReset)
			} else if commitMs > 1000 {
				commitStr = fmt.Sprintf("%s%.0fms%s", colorYellow, commitMs, colorReset)
			} else {
				commitStr = fmt.Sprintf("%.0fms", commitMs)
			}
		}

		// Pool/Pending coloring
		poolStr := fmt.Sprintf("%d", s.PoolSize)
		if s.PoolSize > 100 {
			poolStr = fmt.Sprintf("%s%d%s", colorYellow, s.PoolSize, colorReset)
		}

		pendingStr := fmt.Sprintf("%d", s.PendingSize)
		if s.PendingSize > 1000 {
			pendingStr = fmt.Sprintf("%s%d%s", colorRed, s.PendingSize, colorReset)
		} else if s.PendingSize > 100 {
			pendingStr = fmt.Sprintf("%s%d%s", colorYellow, s.PendingSize, colorReset)
		}

		// Print row (dash for non-applicable fields)
		if node.IsGoSub {
			fmt.Printf("  %-14s │ %8d │ %6s │ %7s │ %9d │ %9s │ %7s │ %8s │ %6s\n",
				node.Name, s.TxsReceived, poolStr, pendingStr, s.TxsForwarded, "—", "—", "—", deltaTPS)
		} else {
			fmt.Printf("  %-14s │ %8s │ %6s │ %7s │ %9s │ %9d │ %7d │ %8s │ %6s\n",
				node.Name, "—", poolStr, pendingStr, "—", s.TxsCommitted, s.LastBlock, commitStr, deltaTPS)
		}
	}

	fmt.Println()

	// Bottleneck analysis
	detectBottleneck(nodes)
}

func detectBottleneck(nodes []*NodeStats) {
	var goSub, goMaster *NodeStats
	for _, n := range nodes {
		if n.IsGoSub && n.Stats != nil && n.Err == nil {
			goSub = n
		}
		if !n.IsGoSub && n.Stats != nil && n.Err == nil {
			goMaster = n
		}
	}

	bottlenecks := []string{}

	if goSub != nil {
		s := goSub.Stats
		// Check pool buildup
		if s.PoolSize > 500 {
			bottlenecks = append(bottlenecks, fmt.Sprintf("%s🚧 Go Sub pool buildup: %d TXs stuck in pool (UDS send too slow)%s", colorYellow, s.PoolSize, colorReset))
		}
		// Check pending leak
		if s.PendingSize > 1000 {
			bottlenecks = append(bottlenecks, fmt.Sprintf("%s🚨 Go Sub pending leak: %d TXs stuck as 'pending' (not clearing)%s", colorRed, s.PendingSize, colorReset))
		}
		// Check forwarding gap
		if s.TxsReceived > 0 && s.TxsForwarded < s.TxsReceived {
			gap := s.TxsReceived - s.TxsForwarded
			if gap > 100 {
				bottlenecks = append(bottlenecks, fmt.Sprintf("%s⚠️  Go Sub forwarding gap: %d TXs received but not forwarded%s", colorYellow, gap, colorReset))
			}
		}
	}

	if goMaster != nil && goSub != nil {
		// Check Rust → Go Master pipeline
		if goSub.Stats.TxsForwarded > 0 && goMaster.Stats.TxsCommitted < goSub.Stats.TxsForwarded {
			gap := goSub.Stats.TxsForwarded - goMaster.Stats.TxsCommitted
			if gap > 500 {
				bottlenecks = append(bottlenecks, fmt.Sprintf("%s🚧 Rust→Master gap: %d TXs forwarded but not committed (Rust consensus slow)%s", colorYellow, gap, colorReset))
			}
		}

		// Check commit time
		if goMaster.Stats.LastCommitUs > 10_000_000 { // > 10s
			bottlenecks = append(bottlenecks, fmt.Sprintf("%s🚨 Go Master slow commit: last block took %.1fs%s", colorRed, float64(goMaster.Stats.LastCommitUs)/1_000_000, colorReset))
		}
	}

	if len(bottlenecks) == 0 {
		fmt.Printf("  %s✅ No bottlenecks detected — Pipeline healthy%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  %s%s🔥 BOTTLENECKS DETECTED:%s\n", colorBold, colorRed, colorReset)
		for _, b := range bottlenecks {
			fmt.Printf("    %s\n", b)
		}
	}

	fmt.Printf("%s═══════════════════════════════════════════════════════════════════════════%s\n", colorCyan, colorReset)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
