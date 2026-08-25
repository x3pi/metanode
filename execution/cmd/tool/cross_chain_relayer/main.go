package main

import (
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
		pollIntervalMs int
	)

	flag.StringVar(&relayerKeyHex, "key", "", "Relayer ECDSA private key hex (with or without 0x prefix)")
	flag.StringVar(&rootAnchorRPC, "root-anchor", "http://127.0.0.1:9099", "Root Anchor JSON-RPC endpoint")
	flag.StringVar(&chainRPCsFlag, "chains", "", "Comma-separated chainID=URL mapping (e.g. 101=http://127.0.0.1:8545,202=http://127.0.0.1:8546)")
	flag.IntVar(&pollIntervalMs, "poll-interval-ms", 500, "Polling interval in milliseconds")
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

	cfg := relayer_daemon.DaemonConfig{
		RelayerKeyHex:     relayerKeyHex,
		RootAnchorURLs:    []string{rootAnchorRPC},
		ChainRPCURLs:      chainRPCs,
		PollInterval:      time.Duration(pollIntervalMs) * time.Millisecond,
		MaxPollIterations: 30,
	}

	daemon, err := relayer_daemon.NewRelayerDaemon(cfg)
	if err != nil {
		logger.Error("Failed to initialize RelayerDaemon: %v", err)
		os.Exit(1)
	}

	logger.Info("🚀 [CROSS-CHAIN RELAYER] started for relayer address %s", daemon.Address().Hex())
	logger.Info("Connected to Root Anchor @ %s with %d private chains configured", rootAnchorRPC, len(chainRPCs))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	logger.Info("🛑 [CROSS-CHAIN RELAYER] received shutdown signal, stopping...")
	daemon.Stop()
	logger.Info("✅ [CROSS-CHAIN RELAYER] gracefully stopped")
}
