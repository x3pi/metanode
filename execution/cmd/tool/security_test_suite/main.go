package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultRPC      = "http://127.0.0.1:10746"
	defaultAllNodes = "http://127.0.0.1:10746,http://127.0.0.1:10747,http://127.0.0.1:10748,http://127.0.0.1:10749,http://127.0.0.1:10750"
	// Use registered genesis test account: 0x0D94C0D19eb6FeD26F5fa2493eb3F25605Cbd336
	defaultKey      = "4828f88ed7a8dc1b6450b3479ce17370c238fd9ed3b9df828b06ead16126db0b"
	defaultChainID  = uint64(991)
)

func main() {
	rpcURL := flag.String("rpc", defaultRPC, "Target primary RPC endpoint URL")
	allNodesFlag := flag.String("all-nodes", defaultAllNodes, "Comma-separated URLs of all cluster nodes for multi-node checks")
	keyHex := flag.String("key", defaultKey, "Funder private key hex")
	chainID := flag.Uint64("chainid", defaultChainID, "Chain ID")
	suiteName := flag.String("suite", "all", "Suite to execute: 'all', 'rpc', 'crypto', 'evm', 'multinode'")
	outReport := flag.String("out", "note/security_test_suite_report.md", "Path to export markdown security report")
	flag.Parse()

	fmt.Println("╔═════════════════════════════════════════════════════════════╗")
	fmt.Println("║     🛡️  METANODE COMPREHENSIVE SECURITY TEST SUITE         ║")
	fmt.Println("╚═════════════════════════════════════════════════════════════╝")
	fmt.Printf("Primary Target:  %s\n", *rpcURL)
	fmt.Printf("Chain ID:        %d\n", *chainID)
	fmt.Printf("Selected Suite:  %s\n", *suiteName)

	report := &SecurityReport{
		Timestamp:   time.Now(),
		TargetRPC:   *rpcURL,
		TargetChain: *chainID,
	}

	startAll := time.Now()
	nodesList := strings.Split(*allNodesFlag, ",")
	for i := range nodesList {
		nodesList[i] = strings.TrimSpace(nodesList[i])
	}

	// Execute Suites
	if *suiteName == "all" || *suiteName == "rpc" {
		RunRPCSuite(*rpcURL, report)
	}
	if *suiteName == "all" || *suiteName == "crypto" {
		RunCryptoSuite(*rpcURL, *chainID, *keyHex, report)
	}
	if *suiteName == "all" || *suiteName == "evm" {
		RunEVMSuite(*rpcURL, report)
	}
	if *suiteName == "all" || *suiteName == "multinode" {
		RunMultiNodeSuite(nodesList, report)
	}

	report.Duration = time.Since(startAll)

	// Print Summary
	PrintSummary(report)

	// Export Report
	if *outReport != "" {
		if err := ExportMarkdownReport(report, *outReport); err != nil {
			fmt.Printf("⚠️ Failed to write report to %s: %v\n", *outReport, err)
		} else {
			fmt.Printf("📄 Security audit report exported to: %s\n", *outReport)
		}
	}

	if report.Failed > 0 {
		os.Exit(1)
	}
}
