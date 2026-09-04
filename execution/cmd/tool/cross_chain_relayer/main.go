package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/relayer_daemon"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type GatewayConfig struct {
	RootAnchorRPC string `json:"root_anchor_rpc"`
	SubmitterKey  string `json:"submitter_key"`
	Chains        []struct {
		ChainID uint64 `json:"chain_id"`
		RPCURL  string `json:"rpc_url"`
	} `json:"chains"`

	// Legacy format fields
	RootAnchor string            `json:"root_anchor"`
	Nodes      map[string]string `json:"nodes"`
}

func findDefaultConfigFile() string {
	if env := os.Getenv("GATEWAY_CONFIG"); env != "" {
		return env
	}

	// 1. Tìm từ thư mục làm việc hiện tại đi ngược lên
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 6; i++ {
			c := filepath.Join(dir, "deploy", "ansible_private_chains", "gateway_register.json")
			if stat, err := os.Stat(c); err == nil && !stat.IsDir() {
				return c
			}
			cDirect := filepath.Join(dir, "gateway_register.json")
			if stat, err := os.Stat(cDirect); err == nil && !stat.IsDir() {
				return cDirect
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// 2. Tìm từ vị trí file thực thi binary đi ngược lên
	if execPath, err := os.Executable(); err == nil {
		dir := filepath.Dir(execPath)
		for i := 0; i < 6; i++ {
			c := filepath.Join(dir, "deploy", "ansible_private_chains", "gateway_register.json")
			if stat, err := os.Stat(c); err == nil && !stat.IsDir() {
				return c
			}
			cDirect := filepath.Join(dir, "gateway_register.json")
			if stat, err := os.Stat(cDirect); err == nil && !stat.IsDir() {
				return cDirect
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// 3. Fallback cho production
	optPath := "/opt/metanode/deploy/ansible_private_chains/gateway_register.json"
	if _, err := os.Stat(optPath); err == nil {
		return optPath
	}

	if _, err := os.Stat("/tmp/private_chains.json"); err == nil {
		return "/tmp/private_chains.json"
	}

	return "deploy/ansible_private_chains/gateway_register.json"
}

func parseConfigFile(path string) (*GatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg GatewayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	defaultConfigFile := findDefaultConfigFile()

	var (
		relayerKeyHex  string
		rootAnchorRPC  string
		chainRPCsFlag  string
		configFileFlag string
		pollIntervalMs int
		reserveChainID uint64
	)

	flag.StringVar(&relayerKeyHex, "key", "", "Relayer ECDSA private key hex (with or without 0x prefix)")
	flag.StringVar(&rootAnchorRPC, "root-anchor", "", "Root Anchor JSON-RPC endpoint")
	flag.StringVar(&chainRPCsFlag, "chains", "", "Comma-separated chainID=URL mapping (e.g. 101=http://127.0.0.1:8545,202=http://127.0.0.1:8546)")
	flag.StringVar(&configFileFlag, "config-file", defaultConfigFile, "Path to json config / topology file (default: auto-detected gateway_register.json)")
	flag.StringVar(&configFileFlag, "config", defaultConfigFile, "Alias for -config-file")
	flag.IntVar(&pollIntervalMs, "poll-interval-ms", 500, "Polling interval in milliseconds")
	flag.Uint64Var(&reserveChainID, "reserve-chain-id", 0, "Chain ID of the system's Reserve")
	flag.Parse()

	chainRPCs := make(map[uint64]string)

	// Đọc cấu hình từ file JSON (gateway_register.json hoặc topology json)
	if configFileFlag != "" {
		if cfg, err := parseConfigFile(configFileFlag); err == nil {
			logger.Info("📖 Đã nạp cấu hình Relayer trực tiếp từ file: %s", configFileFlag)
			if relayerKeyHex == "" && cfg.SubmitterKey != "" {
				relayerKeyHex = cfg.SubmitterKey
			}
			if rootAnchorRPC == "" {
				if cfg.RootAnchorRPC != "" {
					rootAnchorRPC = cfg.RootAnchorRPC
				} else if cfg.RootAnchor != "" {
					rootAnchorRPC = cfg.RootAnchor
				}
			}

			// Nạp danh sách chains từ mảng chains
			for _, c := range cfg.Chains {
				if c.ChainID > 0 && c.RPCURL != "" {
					chainRPCs[c.ChainID] = c.RPCURL
				}
			}
			// Nạp danh sách chains từ map nodes (legacy format)
			for cidStr, url := range cfg.Nodes {
				if id, err := strconv.ParseUint(cidStr, 10, 64); err == nil && url != "" {
					if _, exists := chainRPCs[id]; !exists {
						chainRPCs[id] = url
					}
				}
			}
		}
	}

	if rootAnchorRPC == "" {
		rootAnchorRPC = "http://127.0.0.1:10746"
	}

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

	if relayerKeyHex == "" {
		fmt.Println("❌ Error: -key (relayer private key hex) is required or must be present in config file")
		flag.Usage()
		os.Exit(1)
	}

	// Tự động truy vấn ReserveChainID từ Root Anchor nếu chưa set
	if reserveChainID == 0 && rootAnchorRPC != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client, err := ethclient.DialContext(ctx, rootAnchorRPC)
		if err == nil {
			if onChainID, err := client.ChainID(ctx); err == nil {
				reserveChainID = onChainID.Uint64()
				logger.Info("ℹ️ [AUTO RESERVE] Detected Root Anchor ChainID = %d as Reserve Chain", reserveChainID)
			}
			client.Close()
		}
		cancel()
	}

	// Đảm bảo Reserve Chain được cấu hình trong danh sách RPC
	if reserveChainID != 0 && rootAnchorRPC != "" {
		if _, exists := chainRPCs[reserveChainID]; !exists {
			chainRPCs[reserveChainID] = rootAnchorRPC
		}
	}

	if len(chainRPCs) < 2 {
		fmt.Println("❌ Error: configure at least 2 chain IDs (need both a source and a destination to relay anything)")
		flag.Usage()
		os.Exit(1)
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
	logger.Info("Connected to Root Anchor @ %s with %d chains configured (ReserveChainID: %d)", rootAnchorRPC, len(chainRPCs), reserveChainID)

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

	// Dynamic Chain Discovery: tự động quét config file theo thời gian thực để cập nhật chain mới
	if configFileFlag != "" {
		logger.Info("🔍 [DYNAMIC AUTO-DISCOVERY] Enabled watching config file at %s", configFileFlag)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					cfg, err := parseConfigFile(configFileFlag)
					if err != nil {
						continue
					}
					// Quét mảng chains
					for _, c := range cfg.Chains {
						if c.ChainID > 0 && c.RPCURL != "" {
							if _, exists := daemon.GetChainClient(c.ChainID); !exists {
								if err := daemon.AddChain(ctx, c.ChainID, c.RPCURL); err != nil {
									logger.Warn("⚠️ [DYNAMIC DISCOVERY] Failed to add discovered chain %d: %v", c.ChainID, err)
								}
							}
						}
					}
					// Quét map nodes
					for cidStr, url := range cfg.Nodes {
						cid, err := strconv.ParseUint(cidStr, 10, 64)
						if err != nil || url == "" {
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

