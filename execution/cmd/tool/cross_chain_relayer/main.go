package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/relayer_daemon"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func main() {
	var (
		relayerKeyHex  string
		rootAnchorRPC  string
		chainRPCsFlag  string
		configFileFlag string
		pollIntervalMs int
		reserveChainID uint64
	)

	flag.StringVar(&relayerKeyHex, "key", "", "Relayer ECDSA private key hex (with or without 0x prefix)")
	flag.StringVar(&rootAnchorRPC, "root-anchor", "http://127.0.0.1:9099", "Root Anchor JSON-RPC endpoint")
	flag.StringVar(&chainRPCsFlag, "chains", "", "Comma-separated chainID=URL mapping (e.g. 101=http://127.0.0.1:8545,202=http://127.0.0.1:8546)")
	flag.StringVar(&configFileFlag, "config-file", "/tmp/private_chains.json", "Path to json topology file for dynamic chain auto-discovery (default: /tmp/private_chains.json)")
	flag.IntVar(&pollIntervalMs, "poll-interval-ms", 500, "Polling interval in milliseconds")
	flag.Uint64Var(&reserveChainID, "reserve-chain-id", 0, "Chain ID of the system's Reserve -- MUST be included in -chains. When set, batches whose source is this chain use attestReserveIssuedCommit instead of attestCommit (see DaemonConfig.ReserveChainID's own doc comment), which is what makes a genuine A -> Reserve -> B 2-hop value transfer actually complete on the second (Reserve -> B) leg. Leave 0 to keep the pre-2026-08-28 behavior (every batch uses attestCommit; only [chain] <-> Reserve direct transfers work).")
	flag.Parse()

	if relayerKeyHex == "" {
		fmt.Println("Error: -key (relayer private key hex) is required")
		flag.Usage()
		os.Exit(1)
	}

	chainRPCs := make(map[uint64]string)
	if chainRPCsFlag != "" {
		pairs := strings.Split(chainRPCsFlag, ",")
		for _, pair := range pairs {
			parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
			if len(parts) == 2 {
				id, err := strconv.ParseUint(parts[0], 10, 64)
				if err == nil {
					chainRPCs[id] = parts[1]
				}
			}
		}
	}

	// Read initial chains from config file if provided and chains flag had fewer than 2 chains
	if configFileFlag != "" {
		if data, err := os.ReadFile(configFileFlag); err == nil {
			var topo struct {
				RootAnchor string            `json:"root_anchor"`
				Nodes      map[string]string `json:"nodes"`
			}
			if err := json.Unmarshal(data, &topo); err == nil {
				for cidStr, url := range topo.Nodes {
					if id, err := strconv.ParseUint(cidStr, 10, 64); err == nil {
						if _, exists := chainRPCs[id]; !exists {
							chainRPCs[id] = url
						}
					}
				}
			}
		}
	}

	if len(chainRPCs) < 2 {
		fmt.Println("Error: -chains must configure at least 2 chain IDs (need both a source and a destination to relay anything)")
		flag.Usage()
		os.Exit(1)
	}

	if reserveChainID != 0 {
		if _, ok := chainRPCs[reserveChainID]; !ok {
			fmt.Printf("Error: -reserve-chain-id %d is not one of the chain IDs in -chains\n", reserveChainID)
			os.Exit(1)
		}
	}

	cfg := relayer_daemon.DaemonConfig{
		RelayerKeyHex:     relayerKeyHex,
		RootAnchorURLs:    []string{rootAnchorRPC},
		ChainRPCURLs:      chainRPCs,
		PollInterval:      time.Duration(pollIntervalMs) * time.Millisecond,
		MaxPollIterations: 200,
		ReserveChainID:    reserveChainID,
	}

	daemon, err := relayer_daemon.NewRelayerDaemon(cfg)
	if err != nil {
		logger.Error("Failed to initialize RelayerDaemon: %v", err)
		os.Exit(1)
	}

	logger.Info("🚀 [CROSS-CHAIN RELAYER] started for relayer address %s", daemon.Address().Hex())
	logger.Info("Connected to Root Anchor @ %s with %d private chains configured", rootAnchorRPC, len(chainRPCs))

	// Watch every ordered (source, dest) pair among the configured chains -- any of them could
	// send an outbound() message to any other. Each pair gets its own independent
	// WatchChainPair loop (real polling: getPendingOutboundCount -> batchOutboundCommit ->
	// attestCommit -> claimMessage, see RelayerDaemon.WatchChainPair's doc comment), so one
	// pair's RPC trouble never blocks another pair's relaying.
	ctx, cancel := context.WithCancel(context.Background())
	chainIDs := make([]uint64, 0, len(chainRPCs))
	for id := range chainRPCs {
		chainIDs = append(chainIDs, id)
	}
	watchCount := 0
	for _, src := range chainIDs {
		for _, dst := range chainIDs {
			if src == dst {
				continue
			}
			go daemon.WatchChainPair(ctx, src, dst)
			watchCount++
		}
	}
	logger.Info("👀 [CROSS-CHAIN RELAYER] watching %d chain pair(s) for real outbound messages", watchCount)

	// Dynamic Chain Discovery: automatically poll config-file for newly registered chains at runtime
	if configFileFlag != "" {
		logger.Info("🔍 [DYNAMIC AUTO-DISCOVERY] Enabled watching config topology at %s", configFileFlag)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					data, err := os.ReadFile(configFileFlag)
					if err != nil {
						continue
					}
					var topo struct {
						RootAnchor string            `json:"root_anchor"`
						Nodes      map[string]string `json:"nodes"`
					}
					if err := json.Unmarshal(data, &topo); err != nil {
						continue
					}
					for cidStr, url := range topo.Nodes {
						cid, err := strconv.ParseUint(cidStr, 10, 64)
						if err != nil {
							continue
						}
						if _, exists := daemon.GetChainClient(cid); !exists {
							if err := daemon.AddChain(ctx, cid, url); err != nil {
								logger.Warn("⚠️ [DYNAMIC DISCOVERY] Failed to add discovered chain %d: %v", cid, err)
							}
						}
					}
				}
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	logger.Info("🛑 [CROSS-CHAIN RELAYER] received shutdown signal, stopping...")
	cancel()
	daemon.Stop()
	logger.Info("✅ [CROSS-CHAIN RELAYER] gracefully stopped")
}

