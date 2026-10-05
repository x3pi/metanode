package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

type NodeState struct {
	URL         string
	BlockNumber uint64
	BlockHash   string
	StateRoot   string
	Latency     time.Duration
	Alive       bool
	Error       string
}

// RunMultiNodeSuite executes zero-fork invariant and multi-node determinism checks
func RunMultiNodeSuite(nodeURLs []string, report *SecurityReport) {
	fmt.Println("\n═══════════════════════════════════════════════════════════════")
	fmt.Println("🌐 [SUITE 4] Multi-Node Zero-Fork Determinism & Invariant Checks")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	if len(nodeURLs) < 2 {
		fmt.Println("  ⚠️  Less than 2 nodes specified. Multi-node cross checks require >= 2 nodes.")
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}

	queryNode := func(url string) NodeState {
		start := time.Now()
		st := NodeState{URL: url, Alive: false}

		// 1. Query eth_blockNumber
		reqBody := `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`
		resp, err := client.Post(url, "application/json", strings.NewReader(reqBody))
		if err != nil {
			st.Error = err.Error()
			return st
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		var resNum struct {
			Result string `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &resNum); err != nil || resNum.Result == "" {
			st.Error = "invalid blockNumber response"
			return st
		}

		num, _ := hexutil.DecodeUint64(resNum.Result)
		st.BlockNumber = num

		// 2. Query eth_getBlockByNumber to get hash & stateRoot
		reqBlock := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["%s",false],"id":2}`, resNum.Result)
		respB, errB := client.Post(url, "application/json", strings.NewReader(reqBlock))
		if errB == nil {
			defer respB.Body.Close()
			bodyB, _ := io.ReadAll(respB.Body)
			var resBlk struct {
				Result struct {
					Hash      string `json:"hash"`
					StateRoot string `json:"stateRoot"`
				} `json:"result"`
			}
			_ = json.Unmarshal(bodyB, &resBlk)
			st.BlockHash = resBlk.Result.Hash
			st.StateRoot = resBlk.Result.StateRoot
		}

		st.Latency = time.Since(start)
		st.Alive = true
		return st
	}

	// Gather states concurrently
	var wg sync.WaitGroup
	states := make([]NodeState, len(nodeURLs))
	for i, u := range nodeURLs {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			states[idx] = queryNode(url)
		}(i, u)
	}
	wg.Wait()

	for _, s := range states {
		if s.Alive {
			fmt.Printf("  • Node %s: Block #%d | Hash: %s | Latency: %v\n",
				s.URL, s.BlockNumber, truncate(s.BlockHash, 18), s.Latency.Round(time.Millisecond))
		} else {
			fmt.Printf("  • Node %s: ❌ OFFLINE (%s)\n", s.URL, s.Error)
		}
	}

	// -------------------------------------------------------------
	// SEC-NODE-001: All Nodes Active & Responsive
	// -------------------------------------------------------------
	{
		start := time.Now()
		offlineCount := 0
		for _, s := range states {
			if !s.Alive {
				offlineCount++
			}
		}
		dur := time.Since(start)

		status := StatusPass
		if offlineCount > 0 {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-NODE-001", Category: "MultiNode", Name: "Cluster Node Liveness Verification",
			Status: status, Duration: dur,
			Details: fmt.Sprintf("%d/%d nodes active and online", len(states)-offlineCount, len(states)),
		})
	}

	// -------------------------------------------------------------
	// SEC-NODE-002: Height Divergence / Lagging Guard (< 3 blocks)
	// -------------------------------------------------------------
	{
		start := time.Now()
		var minHeight, maxHeight uint64
		first := true
		for _, s := range states {
			if !s.Alive {
				continue
			}
			if first {
				minHeight = s.BlockNumber
				maxHeight = s.BlockNumber
				first = false
			} else {
				if s.BlockNumber < minHeight {
					minHeight = s.BlockNumber
				}
				if s.BlockNumber > maxHeight {
					maxHeight = s.BlockNumber
				}
			}
		}
		gap := maxHeight - minHeight
		dur := time.Since(start)

		status := StatusPass
		if gap > 2 {
			status = StatusFail
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-NODE-002", Category: "MultiNode", Name: "Height Divergence & Lagging Guard",
			Status: status, Duration: dur,
			Details: fmt.Sprintf("Cluster gap: %d blocks (min=%d, max=%d, threshold <= 2)", gap, minHeight, maxHeight),
		})
	}

	// -------------------------------------------------------------
	// SEC-NODE-003: Common Height Determinism (Identical Hash)
	// -------------------------------------------------------------
	{
		start := time.Now()
		// Determine lowest common block number
		var commonHeight uint64
		first := true
		for _, s := range states {
			if !s.Alive {
				continue
			}
			if first || s.BlockNumber < commonHeight {
				commonHeight = s.BlockNumber
				first = false
			}
		}

		// Query common height across all nodes
		commonHex := hexutil.EncodeUint64(commonHeight)
		hashes := make(map[string]string)
		for _, s := range states {
			if !s.Alive {
				continue
			}
			req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["%s",false],"id":1}`, commonHex)
			resp, err := client.Post(s.URL, "application/json", strings.NewReader(req))
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				var res struct {
					Result struct {
						Hash string `json:"hash"`
					} `json:"result"`
				}
				_ = json.Unmarshal(b, &res)
				hashes[s.URL] = res.Result.Hash
			}
		}

		// Check if all hashes match
		uniqueHashes := make(map[string]bool)
		for _, h := range hashes {
			if h != "" {
				uniqueHashes[h] = true
			}
		}
		dur := time.Since(start)

		status := StatusPass
		if len(uniqueHashes) > 1 {
			status = StatusFail // FORK DETECTED!
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-NODE-003", Category: "MultiNode", Name: "Zero-Fork Common Block Hash Determinism",
			Status: status, Duration: dur,
			Details: fmt.Sprintf("Block #%d hash parity across all nodes: %d distinct hashes (expected 1)", commonHeight, len(uniqueHashes)),
		})
	}

	// -------------------------------------------------------------
	// SEC-NODE-004: Concurrent Query Resilience
	// -------------------------------------------------------------
	{
		start := time.Now()
		concurrency := 20
		var subWg sync.WaitGroup
		successCount := 0
		var mu sync.Mutex

		for i := 0; i < concurrency; i++ {
			subWg.Add(1)
			go func(idx int) {
				defer subWg.Done()
				targetURL := nodeURLs[idx%len(nodeURLs)]
				req := `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`
				resp, err := client.Post(targetURL, "application/json", strings.NewReader(req))
				if err == nil && resp.StatusCode == http.StatusOK {
					resp.Body.Close()
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}(i)
		}
		subWg.Wait()
		dur := time.Since(start)

		status := StatusPass
		if successCount < concurrency {
			status = StatusWarn
		}
		report.AddResult(TestCaseResult{
			ID: "SEC-NODE-004", Category: "MultiNode", Name: "Concurrent Multi-Node Query Resilience",
			Status: status, Duration: dur,
			Details: fmt.Sprintf("%d/%d concurrent requests succeeded in %v", successCount, concurrency, dur.Round(time.Millisecond)),
		})
	}
}
