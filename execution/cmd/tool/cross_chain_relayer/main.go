package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net/http"
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
	// RelayerKey (2026-09-04) is the relayer daemon's OWN signing key -- deliberately a SEPARATE
	// field from SubmitterKey, never falling back to it (see the doc comment on
	// devnetDefaultRelayerKeyHex below for why that fallback was a real, live bug).
	RelayerKey string `json:"relayer_key"`
	// MetricsAddr (2026-09-05) lets a per-instance config file pin a distinct metrics/health
	// port -- needed because running N relayer instances on one host (see
	// deploy/ansible_private_chains/run_relayer_tmux.sh's multi-instance support) means each
	// instance's -metrics-addr MUST differ, or the 2nd+ instance's listener simply fails to bind.
	// Only used when -metrics-addr was NOT explicitly passed on the command line (see main()'s
	// flag.Visit check) -- an explicit CLI flag always wins over the config file.
	MetricsAddr string `json:"metrics_addr,omitempty"`
	Chains      []struct {
		ChainID uint64 `json:"chain_id"`
		RPCURL  string `json:"rpc_url"`
	} `json:"chains"`

	// Legacy format fields
	RootAnchor string            `json:"root_anchor"`
	Nodes      map[string]string `json:"nodes"`
}

// devnetDefaultRelayerKeyHex is the SAME shared cross-chain relayer devnet identity
// gen_single_chain.py/gen_root_anchor_chain.py/gen_validator_entry.py already pre-register (BLS
// pubkey) and fund/gas-exempt on every chain (address 0x7d8bfbaba9268b59bab9ef8ff3f314d3f5747366)
// -- used here ONLY as a last-resort default when neither -key nor the config file's relayer_key
// sets one, purely for devnet convenience (matches DEVNET_GATEWAY_BLS_KEY's own pattern).
//
// SECURITY FIX (2026-09-04, found via a real run_full_pipeline.sh run): this used to fall back to
// the config file's submitter_key instead -- the SAME account register_chains uses for every
// governance/registration transaction on Root Anchor. Two independent processes (a short-lived
// CLI tool and this long-running daemon) sharing one account, each tracking its own nonce
// completely independently, is a real nonce-collision hazard: when the daemon starts moments
// after register_chains finishes (run_full_pipeline.sh's Step 2 -> Step 3, only a 3s gap), both
// can race to use the same nonce, silently orphaning whichever transaction loses -- confirmed
// live: a relayer attestCommit transaction to Root Anchor got permanently dropped this way,
// verified via eth_getTransactionReceipt returning null minutes later. Giving the relayer its own
// dedicated identity closes this at the root, not just papers over the symptom.
const devnetDefaultRelayerKeyHex = "d3ae7482f46f11cee2447bc711e9eb0fb79d4f2549781554cb962f54604e50f8"

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
		relayerKeyHex         string
		rootAnchorRPC         string
		chainRPCsFlag         string
		configFileFlag        string
		pollIntervalMs        int
		reserveChainID        uint64
		metricsAddr           string
		balanceCheckIntervalS int
		gasPriceBumpPercent   uint64
		maxGasPriceGwei       uint64
		maxPollBackoffS       int
	)

	flag.StringVar(&relayerKeyHex, "key", "", "Relayer ECDSA private key hex (with or without 0x prefix)")
	flag.StringVar(&rootAnchorRPC, "root-anchor", "", "Root Anchor JSON-RPC endpoint")
	flag.StringVar(&chainRPCsFlag, "chains", "", "Comma-separated chainID=URL mapping (e.g. 101=http://127.0.0.1:8545,202=http://127.0.0.1:8546)")
	flag.StringVar(&configFileFlag, "config-file", defaultConfigFile, "Path to json config / topology file (default: auto-detected gateway_register.json)")
	flag.StringVar(&configFileFlag, "config", defaultConfigFile, "Alias for -config-file")
	flag.IntVar(&pollIntervalMs, "poll-interval-ms", 500, "Polling interval in milliseconds")
	flag.Uint64Var(&reserveChainID, "reserve-chain-id", 0, "Chain ID of the system's Reserve")
	// Observability + gas pricing flags (2026-09-05 production-readiness pass -- see
	// note/cross_chain_relayer_production_readiness.md for the full rationale).
	flag.StringVar(&metricsAddr, "metrics-addr", ":9090", "Address to serve /metrics (Prometheus) and /health on. Empty string disables the server. "+
		"Running multiple relayer instances on one host REQUIRES a distinct value per instance.")
	flag.IntVar(&balanceCheckIntervalS, "balance-check-interval-s", 30, "How often (seconds) to refresh the relayer's own wallet balance per chain for the relayer_wallet_balance_wei metric")
	flag.Uint64Var(&gasPriceBumpPercent, "gas-price-bump-percent", 0, "Inflate each chain's suggested gas price by this percent (e.g. 110 = +10%) to reduce stuck-transaction risk during fee spikes. 0/unset = no bump")
	flag.Uint64Var(&maxGasPriceGwei, "max-gas-price-gwei", 0, "Hard ceiling (in Gwei) on the gas price this daemon will ever pay, regardless of what a chain's RPC suggests. 0/unset = no cap")
	flag.IntVar(&maxPollBackoffS, "max-poll-backoff-s", 30, "Ceiling (seconds) for WatchChainPair's exponential backoff after consecutive errors on one chain pair")
	flag.Parse()

	// Tracks which flags were explicitly passed on the command line, so a config file's
	// metrics_addr can fill in a default without ever overriding an explicit -metrics-addr.
	explicitFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	chainRPCs := make(map[uint64]string)

	// Đọc cấu hình từ file JSON (gateway_register.json hoặc topology json)
	if configFileFlag != "" {
		if cfg, err := parseConfigFile(configFileFlag); err == nil {
			logger.Info("📖 Đã nạp cấu hình Relayer trực tiếp từ file: %s", configFileFlag)
			if relayerKeyHex == "" && cfg.RelayerKey != "" {
				relayerKeyHex = cfg.RelayerKey
			}
			if !explicitFlags["metrics-addr"] && cfg.MetricsAddr != "" {
				metricsAddr = cfg.MetricsAddr
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
		relayerKeyHex = devnetDefaultRelayerKeyHex
		logger.Info("ℹ️ No -key or config relayer_key given -- using the shared devnet relayer identity (0x7d8bfbaba9268b59bab9ef8ff3f314d3f5747366). Set relayer_key in config (or -key) for any real deployment.")
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

	var maxGasPriceWei *big.Int
	if maxGasPriceGwei > 0 {
		maxGasPriceWei = new(big.Int).Mul(new(big.Int).SetUint64(maxGasPriceGwei), big.NewInt(1_000_000_000))
	}

	cfg := relayer_daemon.DaemonConfig{
		RelayerKeyHex:       relayerKeyHex,
		RootAnchorURLs:      []string{rootAnchorRPC},
		ChainRPCURLs:        chainRPCs,
		PollInterval:        time.Duration(pollIntervalMs) * time.Millisecond,
		MaxPollIterations:   200,
		ReserveChainID:      reserveChainID,
		GasPriceBumpPercent: gasPriceBumpPercent,
		MaxGasPriceWei:      maxGasPriceWei,
		MaxPollBackoff:      time.Duration(maxPollBackoffS) * time.Second,
	}

	daemon, err := relayer_daemon.NewRelayerDaemon(cfg)
	if err != nil {
		logger.Error("Failed to initialize RelayerDaemon: %v", err)
		os.Exit(1)
	}

	logger.Info("🚀 [CROSS-CHAIN RELAYER] started for relayer address %s", daemon.Address().Hex())
	logger.Info("Connected to Root Anchor @ %s with %d chains configured (ReserveChainID: %d)", rootAnchorRPC, len(chainRPCs), reserveChainID)

	ctx, cancel := context.WithCancel(context.Background())

	var metricsServer *http.Server
	if metricsAddr != "" {
		metricsServer = daemon.StartMetricsServer(metricsAddr)
	} else {
		logger.Info("ℹ️ [CROSS-CHAIN RELAYER] metrics/health server disabled (-metrics-addr is empty)")
	}
	daemon.StartBalanceMonitor(ctx, time.Duration(balanceCheckIntervalS)*time.Second)

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
	if metricsServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = metricsServer.Shutdown(shutdownCtx)
		shutdownCancel()
	}
	logger.Info("✅ [CROSS-CHAIN RELAYER] gracefully stopped")
}
