package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type NodeStatus struct {
	LastBlock    uint64 `json:"last_block"`
	LastHash     string `json:"last_hash"`
	StateRoot    string `json:"state_root"`
	Syncing      bool   `json:"syncing"`
	ForkDetected bool   `json:"fork_detected"`
}

type NodeResult struct {
	URL          string
	Online       bool
	Status       NodeStatus
	Error        string
	ResponseTime time.Duration
}

func queryNode(url string, timeout time.Duration) NodeResult {
	start := time.Now()
	client := http.Client{Timeout: timeout}
	resp, err := client.Get(strings.TrimRight(url, "/") + "/status")
	duration := time.Since(start)

	if err != nil {
		return NodeResult{URL: url, Online: false, Error: err.Error(), ResponseTime: duration}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return NodeResult{
			URL:          url,
			Online:       false,
			Error:        fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
			ResponseTime: duration,
		}
	}

	var status NodeStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return NodeResult{URL: url, Online: false, Error: "decode error: " + err.Error(), ResponseTime: duration}
	}

	return NodeResult{
		URL:          url,
		Online:       true,
		Status:       status,
		ResponseTime: duration,
	}
}

func checkCluster(nodeURLs []string, timeout time.Duration) (hasFork bool, hasMismatch bool, allHealthy bool) {
	results := make([]NodeResult, len(nodeURLs))
	allHealthy = true

	for i, url := range nodeURLs {
		results[i] = queryNode(url, timeout)
		if !results[i].Online {
			allHealthy = false
		}
	}

	fmt.Println("---------------------------------------------------------------------------------------------------------------------------------")
	fmt.Printf("%-24s %-8s %-12s %-20s %-20s %-8s %-8s %-8s\n",
		"NODE URL", "STATUS", "LAST BLOCK", "STATE ROOT", "BLOCK HASH", "SYNCING", "FORK", "LATENCY")
	fmt.Println("---------------------------------------------------------------------------------------------------------------------------------")

	var refBlock uint64
	var refHash, refStateRoot string
	firstOnline := true

	for _, res := range results {
		if !res.Online {
			fmt.Printf("%-24s %-8s %-12s %-20s %-20s %-8s %-8s %-8s\n",
				res.URL, "OFFLINE", "N/A", "N/A", "N/A", "N/A", "N/A", res.ResponseTime.Round(time.Millisecond))
			continue
		}

		st := res.Status
		rootShort := st.StateRoot
		if len(rootShort) > 18 {
			rootShort = rootShort[:18] + "..."
		}
		hashShort := st.LastHash
		if len(hashShort) > 18 {
			hashShort = hashShort[:18] + "..."
		}

		forkStr := "OK"
		if st.ForkDetected {
			forkStr = "FORK!"
			hasFork = true
		}

		fmt.Printf("%-24s %-8s %-12d %-20s %-20s %-8t %-8s %-8s\n",
			res.URL, "ONLINE", st.LastBlock, rootShort, hashShort, st.Syncing, forkStr, res.ResponseTime.Round(time.Millisecond))

		if firstOnline {
			refBlock = st.LastBlock
			refHash = st.LastHash
			refStateRoot = st.StateRoot
			firstOnline = false
		} else {
			if st.LastBlock == refBlock {
				if st.LastHash != refHash || st.StateRoot != refStateRoot {
					hasMismatch = true
				}
			}
		}
	}
	fmt.Println("---------------------------------------------------------------------------------------------------------------------------------")

	if hasFork {
		fmt.Println("🚨 CRITICAL: fork_detected is TRUE on one or more nodes!")
	}
	if hasMismatch {
		fmt.Println("🚨 CRITICAL: StateRoot or BlockHash divergence detected among nodes at same height!")
	}
	if !hasFork && !hasMismatch && allHealthy {
		fmt.Println("✅ Parity verified: All online nodes are synchronized with identical block hash and state root.")
	}

	return hasFork, hasMismatch, allHealthy
}

func main() {
	defaultNodes := "http://127.0.0.1:18601,http://127.0.0.1:18602,http://127.0.0.1:18603,http://127.0.0.1:18604"
	if envNodes := os.Getenv("PARENT_CHAIN_URLS"); envNodes != "" {
		defaultNodes = envNodes
	}

	nodesFlag := flag.String("nodes", defaultNodes, "Comma-separated list of parent chain node URLs")
	onceFlag := flag.Bool("once", false, "Run check once and exit with return code (0 = healthy, 1 = error/divergence)")
	intervalFlag := flag.Duration("interval", 3*time.Second, "Monitoring interval in watch mode")
	timeoutFlag := flag.Duration("timeout", 2*time.Second, "HTTP timeout per node request")
	flag.Parse()

	nodes := strings.Split(*nodesFlag, ",")
	for i := range nodes {
		nodes[i] = strings.TrimSpace(nodes[i])
	}

	fmt.Println("=================================================================================================================================")
	fmt.Println("📡 METANODE PARENT CHAIN MULTI-NODE CONSENSUS & STATE MONITOR")
	fmt.Printf("Targets: %v\n", nodes)
	fmt.Println("=================================================================================================================================")

	if *onceFlag {
		hasFork, hasMismatch, allHealthy := checkCluster(nodes, *timeoutFlag)
		if hasFork || hasMismatch || !allHealthy {
			os.Exit(1)
		}
		os.Exit(0)
	}

	for {
		fmt.Printf("\n[%s] Checking node status...\n", time.Now().Format("2006-01-02 15:04:05"))
		checkCluster(nodes, *timeoutFlag)
		time.Sleep(*intervalFlag)
	}
}
