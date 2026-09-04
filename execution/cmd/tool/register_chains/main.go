package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type committeeMember struct {
	ChainID uint64
	PrivHex string
}

func findDefaultRootAnchor() string {
	if env := os.Getenv("ROOT_ANCHOR_RPC"); env != "" {
		return env
	}
	if data, err := os.ReadFile("/tmp/private_chains.json"); err == nil {
		var topology struct {
			RootAnchor string `json:"root_anchor"`
		}
		if err := json.Unmarshal(data, &topology); err == nil && topology.RootAnchor != "" {
			return topology.RootAnchor
		}
	}
	return "http://127.0.0.1:10746"
}

func findDefaultConfigFile() string {
	if env := os.Getenv("GATEWAY_CONFIG"); env != "" {
		return env
	}

	// 1. Tìm từ thư mục làm việc hiện tại (working directory) đi ngược lên
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 6; i++ {
			candidate := filepath.Join(dir, "deploy", "ansible_private_chains", "gateway_register.json")
			if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
				return candidate
			}
			candidateDirect := filepath.Join(dir, "gateway_register.json")
			if stat, err := os.Stat(candidateDirect); err == nil && !stat.IsDir() {
				return candidateDirect
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// 2. Tìm từ vị trí file thực thi binary (executable directory) đi ngược lên
	if execPath, err := os.Executable(); err == nil {
		dir := filepath.Dir(execPath)
		for i := 0; i < 6; i++ {
			candidate := filepath.Join(dir, "deploy", "ansible_private_chains", "gateway_register.json")
			if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
				return candidate
			}
			candidateDirect := filepath.Join(dir, "gateway_register.json")
			if stat, err := os.Stat(candidateDirect); err == nil && !stat.IsDir() {
				return candidateDirect
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// 3. Fallback cho production cài đặt tại /opt/metanode
	optPath := "/opt/metanode/deploy/ansible_private_chains/gateway_register.json"
	if _, err := os.Stat(optPath); err == nil {
		return optPath
	}

	return "deploy/ansible_private_chains/gateway_register.json"
}

func main() {
	defaultRootAnchor := findDefaultRootAnchor()
	defaultConfigFile := findDefaultConfigFile()

	var (
		configFileFlag      string
		actionFlag          string
		submitterKeyHex     string
		rootAnchorRPC       string
		chainIDsFlag        string
		fundGenesisFlag     bool
		genesisSupplyFlag   string
		perChainAllocFlag   string
		timelockWaitSeconds int

		// Transfer alloc flags
		fromChainFlag uint64
		toChainFlag   uint64
		amountMTNFlag float64
		amountWeiFlag string

		// Deterministic-genesis flags (2026-09-04) -- see publish-genesis-digest/verify-genesis
		genesisFileFlag string
	)

	flag.StringVar(&configFileFlag, "config", defaultConfigFile, "Path to JSON configuration file (declarative gateway register config)")
	flag.StringVar(&actionFlag, "action", "register", "Action to perform: register | transfer-alloc | query-alloc | query-registry | publish-genesis-digest | verify-genesis")
	flag.StringVar(&submitterKeyHex, "key", "0xd3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9", "Sender ECDSA private key hex")
	flag.StringVar(&rootAnchorRPC, "root-anchor", defaultRootAnchor, "Root Anchor JSON-RPC endpoint (auto-detected)")
	flag.StringVar(&chainIDsFlag, "chains", "101,102,103,104", "Comma-separated list of chain IDs (for query actions)")
	flag.BoolVar(&fundGenesisFlag, "fund-genesis", false, "After bootstrapping, also mint and distribute genesis supply")
	flag.StringVar(&genesisSupplyFlag, "genesis-supply", "", "Total genesis supply to mint on Root Anchor (base-10 wei)")
	flag.StringVar(&perChainAllocFlag, "per-chain-allocation", "", "Amount transferred to each founding chain (base-10 wei)")
	flag.IntVar(&timelockWaitSeconds, "timelock-wait", 12, "Seconds to wait for devnet governance timelock")

	flag.Uint64Var(&fromChainFlag, "from-chain", 101, "Source Chain ID for transfer-alloc (e.g. 101 or 991)")
	flag.Uint64Var(&toChainFlag, "to-chain", 103, "Destination Chain ID for transfer-alloc")
	flag.Float64Var(&amountMTNFlag, "amount-mtn", 20000000, "Amount of MTN tokens to transfer (e.g. 20000000 for 20M MTN)")
	flag.StringVar(&amountWeiFlag, "amount-wei", "", "Exact amount in base-10 wei for transfer-alloc")
	flag.StringVar(&genesisFileFlag, "genesis-file", "", "Path to a chain's genesis.json (for publish-genesis-digest / verify-genesis)")
	flag.Parse()

	var fileConfig *GatewayConfigFile
	if configFileFlag != "" {
		cfg, err := loadGatewayConfigFile(configFileFlag)
		if err != nil {
			logger.Error("Failed to load config file %s: %v", configFileFlag, err)
			os.Exit(1)
		}
		fileConfig = cfg
		logger.Info("Loaded configuration directly from file: %s (%d chains configured)", configFileFlag, len(fileConfig.Chains))

		if fileConfig.RootAnchorRPC != "" {
			rootAnchorRPC = fileConfig.RootAnchorRPC
		}
		if fileConfig.SubmitterKey != "" {
			submitterKeyHex = fileConfig.SubmitterKey
		}
		if fileConfig.GenesisSupply != "" {
			genesisSupplyFlag = fileConfig.GenesisSupply
		}
		if fileConfig.PerChainAllocation != "" {
			perChainAllocFlag = fileConfig.PerChainAllocation
		}
		if fileConfig.FundGenesis != nil {
			fundGenesisFlag = *fileConfig.FundGenesis
		}
		if fileConfig.TimelockWaitSeconds != nil {
			timelockWaitSeconds = *fileConfig.TimelockWaitSeconds
		}
	}

	submitterKeyHex = strings.TrimPrefix(submitterKeyHex, "0x")
	privKey, err := crypto.HexToECDSA(submitterKeyHex)
	if err != nil {
		logger.Error("Invalid private key: %v", err)
		os.Exit(1)
	}
	fromAddress := crypto.PubkeyToAddress(privKey.PublicKey)

	ctx := context.Background()
	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		logger.Error("Failed to parse Gateway ABI: %v", err)
		os.Exit(1)
	}

	switch strings.ToLower(actionFlag) {
	case "query-alloc", "query-allocations":
		handleQueryAllocations(ctx, rootAnchorRPC, chainIDsFlag, parsedABI)
	case "query-alloc-raw":
		// Machine-readable counterpart to query-alloc: prints ONLY the decimal wei amount for
		// exactly one chain ID to stdout, nothing else -- no banner, no logging -- so scripts
		// (gen_single_chain.py's deterministic-genesis cross-check) can parse it directly instead
		// of scraping human-formatted text.
		handleQueryAllocationRaw(ctx, rootAnchorRPC, chainIDsFlag, parsedABI)
	case "query-genesis-wallet-raw":
		// Same idea as query-alloc-raw but for ChainRegistry.GenesisWallet -- prints just the
		// 0x-prefixed address (or the zero address if not yet set/registered), nothing else.
		handleQueryGenesisWalletRaw(ctx, rootAnchorRPC, chainIDsFlag, parsedABI)
	case "query-registry":
		handleQueryRegistry(ctx, rootAnchorRPC, chainIDsFlag, parsedABI)
	case "transfer-alloc", "transfer-allocation", "allocate-supply":
		handleTransferAllocation(ctx, privKey, fromAddress, rootAnchorRPC, fileConfig, parsedABI, fromChainFlag, toChainFlag, amountMTNFlag, amountWeiFlag, timelockWaitSeconds)
	case "publish-genesis-digest":
		handlePublishGenesisDigest(ctx, privKey, fromAddress, rootAnchorRPC, chainIDsFlag, genesisFileFlag, parsedABI)
	case "verify-genesis":
		handleVerifyGenesis(ctx, rootAnchorRPC, chainIDsFlag, genesisFileFlag, parsedABI)
	default: // "register"
		if fileConfig == nil || len(fileConfig.Chains) == 0 {
			logger.Error("No gateway register config file found. Please provide --config <gateway_register.json>")
			os.Exit(1)
		}
		handleRegisterChains(ctx, privKey, fromAddress, rootAnchorRPC, fileConfig, fundGenesisFlag, genesisSupplyFlag, perChainAllocFlag, timelockWaitSeconds, parsedABI)
	}
}

func handleQueryAllocations(ctx context.Context, rootAnchorRPC, chainIDsFlag string, parsedABI abi.ABI) {
	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		fmt.Printf("❌ Failed to connect to RPC %s: %v\n", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
	chainID, _ := client.ChainID(ctx)

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("📊 TRA CỨU HẠN MỨC CUNG TIỀN LIÊN CHUỖI (CUSTODIAL CEILING)\n")
	fmt.Printf("   - RPC Endpoint: %s (ChainID: %v)\n", rootAnchorRPC, chainID)
	fmt.Printf("   - Gateway Address: %s\n", gwAddr.Hex())
	fmt.Println("═══════════════════════════════════════════════════════════════")

	for _, cidStr := range strings.Split(chainIDsFlag, ",") {
		cidStr = strings.TrimSpace(cidStr)
		if cidStr == "" {
			continue
		}
		var cid uint64
		if _, err := fmt.Sscanf(cidStr, "%d", &cid); err != nil {
			continue
		}

		calldata, err := parsedABI.Pack("getAllocation", new(big.Int).SetUint64(cid))
		if err != nil {
			fmt.Printf("❌ Pack error for chain %d: %v\n", cid, err)
			continue
		}

		out, err := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil)
		if err != nil {
			fmt.Printf("   Chain %-4d: ⚠️  Chưa nạp method getAllocation trên node hiện tại\n", cid)
			continue
		}

		results, err := parsedABI.Unpack("getAllocation", out)
		if err != nil {
			fmt.Printf("❌ Unpack error for chain %d: %v\n", cid, err)
			continue
		}

		alloc := results[0].(*big.Int)
		allocFloat := new(big.Float).Quo(new(big.Float).SetInt(alloc), big.NewFloat(1e18))
		fmt.Printf("   ├─ Chain %-4d: %24s wei  ➔  %14s MTN\n", cid, alloc.String(), allocFloat.Text('f', 4))
	}
	fmt.Println("═══════════════════════════════════════════════════════════════")
}

func handleQueryAllocationRaw(ctx context.Context, rootAnchorRPC, chainIDsFlag string, parsedABI abi.ABI) {
	chainID, err := parseSingleChainID(chainIDsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query-alloc-raw requires exactly one chain ID via -chains: %v\n", err)
		os.Exit(1)
	}
	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect to %s: %v\n", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
	calldata, err := parsedABI.Pack("getAllocation", new(big.Int).SetUint64(chainID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack getAllocation: %v\n", err)
		os.Exit(1)
	}
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "call getAllocation: %v\n", err)
		os.Exit(1)
	}
	results, err := parsedABI.Unpack("getAllocation", out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unpack getAllocation: %v\n", err)
		os.Exit(1)
	}
	alloc, _ := results[0].(*big.Int)
	if alloc == nil {
		alloc = big.NewInt(0)
	}
	fmt.Println(alloc.String())
}

func handleQueryGenesisWalletRaw(ctx context.Context, rootAnchorRPC, chainIDsFlag string, parsedABI abi.ABI) {
	chainID, err := parseSingleChainID(chainIDsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query-genesis-wallet-raw requires exactly one chain ID via -chains: %v\n", err)
		os.Exit(1)
	}
	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect to %s: %v\n", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
	calldata, err := parsedABI.Pack("getChainRegistry", new(big.Int).SetUint64(chainID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack getChainRegistry: %v\n", err)
		os.Exit(1)
	}
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "call getChainRegistry: %v\n", err)
		os.Exit(1)
	}
	results, err := parsedABI.Unpack("getChainRegistry", out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unpack getChainRegistry: %v\n", err)
		os.Exit(1)
	}
	genesisWallet, _ := results[11].(common.Address)
	fmt.Println(genesisWallet.Hex())
}

func handleQueryRegistry(ctx context.Context, rootAnchorRPC, chainIDsFlag string, parsedABI abi.ABI) {
	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		fmt.Printf("❌ Failed to connect to RPC %s: %v\n", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
	chainID, _ := client.ChainID(ctx)

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("📋 TRA CỨU DANH BẠ LIÊN CHUỖI (CHAIN REGISTRY)\n")
	fmt.Printf("   - RPC Endpoint: %s (ChainID: %v)\n", rootAnchorRPC, chainID)
	fmt.Println("═══════════════════════════════════════════════════════════════")

	for _, cidStr := range strings.Split(chainIDsFlag, ",") {
		cidStr = strings.TrimSpace(cidStr)
		if cidStr == "" {
			continue
		}
		var cid uint64
		if _, err := fmt.Sscanf(cidStr, "%d", &cid); err != nil {
			continue
		}

		calldata, err := parsedABI.Pack("getChainRegistry", new(big.Int).SetUint64(cid))
		if err != nil {
			continue
		}
		out, err := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil)
		if err != nil {
			fmt.Printf("   Chain %-4d: ❌ Call error: %v\n", cid, err)
			continue
		}
		results, err := parsedABI.Unpack("getChainRegistry", out)
		if err != nil {
			continue
		}
		exists := results[0].(bool)
		if !exists {
			fmt.Printf("   ├─ Chain %-4d: ❌ Chưa đăng ký trong danh bạ\n", cid)
			continue
		}
		pubkeys := results[1].([][]byte)
		epoch := results[4].(uint64)
		threshold := results[5].(uint64)
		genesisWallet, _ := results[11].(common.Address)
		genesisDigestRaw, _ := results[12].([32]byte)
		genesisDigest := common.Hash(genesisDigestRaw)
		fmt.Printf("   ├─ Chain %-4d: ✅ Đã đăng ký (Epoch: %d, Committee: %d validator(s), Quorum: %d%%)\n", cid, epoch, len(pubkeys), threshold/100)
		for i, pk := range pubkeys {
			fmt.Printf("   │  └─ Val[%d] BLS Pubkey: 0x%s\n", i, common.Bytes2Hex(pk))
		}
		if genesisWallet != (common.Address{}) {
			fmt.Printf("   │  └─ Genesis wallet: %s\n", genesisWallet.Hex())
			if genesisDigest != (common.Hash{}) {
				fmt.Printf("   │  └─ Genesis digest: %s (published)\n", genesisDigest.Hex())
			} else {
				fmt.Printf("   │  └─ Genesis digest: chưa publish (dùng -action publish-genesis-digest)\n")
			}
		}
	}
	fmt.Println("═══════════════════════════════════════════════════════════════")
}

// handlePublishGenesisDigest computes the keccak256 digest of a chain's canonical genesis.json
// (raw file bytes -- same definition as pkg/cross_chain/ceremony.Digest, kept identical on
// purpose) and publishes it on Root Anchor via setGenesisDigest(), the second phase of the
// deterministic-genesis design (see GatewayEngine.SetGenesisDigest's own doc comment). Restricted
// server-side to the chain's own GenesisWallet -- this simply fails if the signing key here isn't
// that wallet, so no client-side check is needed to make this safe.
func handlePublishGenesisDigest(ctx context.Context, privKey *ecdsa.PrivateKey, fromAddress common.Address, rootAnchorRPC, chainIDsFlag, genesisFile string, parsedABI abi.ABI) {
	if genesisFile == "" {
		logger.Error("publish-genesis-digest requires -genesis-file <path to genesis.json>")
		os.Exit(1)
	}
	chainID, err := parseSingleChainID(chainIDsFlag)
	if err != nil {
		logger.Error("publish-genesis-digest requires exactly one chain ID via -chains: %v", err)
		os.Exit(1)
	}
	raw, err := os.ReadFile(genesisFile)
	if err != nil {
		logger.Error("Failed to read genesis file %s: %v", genesisFile, err)
		os.Exit(1)
	}
	digest := crypto.Keccak256Hash(raw)
	fmt.Printf("📐 Genesis digest cho chain %d (%s): %s\n", chainID, genesisFile, digest.Hex())

	calldata, err := parsedABI.Pack("setGenesisDigest", new(big.Int).SetUint64(chainID), digest)
	if err != nil {
		logger.Error("Failed to pack setGenesisDigest: %v", err)
		os.Exit(1)
	}
	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		logger.Error("Failed to connect to Root Anchor at %s: %v", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	if _, err := sendTxAndWait(ctx, client, privKey, fromAddress, rootAnchorRPC, nil, 200_000, calldata, fmt.Sprintf("setGenesisDigest(chain %d)", chainID)); err != nil {
		logger.Error("❌ setGenesisDigest(chain %d) failed: %v", chainID, err)
		os.Exit(1)
	}
	fmt.Printf("✅ Đã publish genesis digest cho chain %d: %s\n", chainID, digest.Hex())
}

// handleVerifyGenesis is the read-only counterpart: fetch the digest Root Anchor has on record for
// a chain and compare it against a local genesis.json's own recomputed digest -- exactly the same
// "recompute and compare, fail closed on mismatch" defense
// pkg/cross_chain/ceremony.VerifyGenesisFile already provides for the founding-chain ceremony
// path, just checking against an on-chain record instead of a hand-distributed genesis_digest.txt.
// Exits non-zero on any mismatch or on "not yet published" -- meant to gate node startup /
// operator trust decisions, so it fails loud rather than warn-and-continue.
func handleVerifyGenesis(ctx context.Context, rootAnchorRPC, chainIDsFlag, genesisFile string, parsedABI abi.ABI) {
	if genesisFile == "" {
		logger.Error("verify-genesis requires -genesis-file <path to genesis.json>")
		os.Exit(1)
	}
	chainID, err := parseSingleChainID(chainIDsFlag)
	if err != nil {
		logger.Error("verify-genesis requires exactly one chain ID via -chains: %v", err)
		os.Exit(1)
	}
	raw, err := os.ReadFile(genesisFile)
	if err != nil {
		logger.Error("Failed to read genesis file %s: %v", genesisFile, err)
		os.Exit(1)
	}
	localDigest := crypto.Keccak256Hash(raw)

	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		logger.Error("Failed to connect to Root Anchor at %s: %v", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
	calldata, err := parsedABI.Pack("getChainRegistry", new(big.Int).SetUint64(chainID))
	if err != nil {
		logger.Error("Pack getChainRegistry: %v", err)
		os.Exit(1)
	}
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil)
	if err != nil {
		logger.Error("Call getChainRegistry: %v", err)
		os.Exit(1)
	}
	results, err := parsedABI.Unpack("getChainRegistry", out)
	if err != nil {
		logger.Error("Unpack getChainRegistry: %v", err)
		os.Exit(1)
	}
	exists, _ := results[0].(bool)
	if !exists {
		fmt.Printf("❌ Chain %d chưa được đăng ký trên Root Anchor -- không có gì để đối chiếu.\n", chainID)
		os.Exit(1)
	}
	remoteDigestRaw, _ := results[12].([32]byte)
	remoteDigest := common.Hash(remoteDigestRaw)

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("🔍 XÁC MINH GENESIS -- chain %d\n", chainID)
	fmt.Printf("   - File local:              %s\n", genesisFile)
	fmt.Printf("   - Digest tính lại:         %s\n", localDigest.Hex())
	fmt.Printf("   - Digest trên Root Anchor: %s\n", remoteDigest.Hex())
	fmt.Println("═══════════════════════════════════════════════════════════════")

	if remoteDigest == (common.Hash{}) {
		fmt.Println("⚠️  Chưa có digest nào được publish cho chain này -- KHÔNG THỂ xác minh, đừng coi là an toàn.")
		os.Exit(1)
	}
	if localDigest != remoteDigest {
		fmt.Println("❌ LỆCH -- genesis.json cục bộ KHÔNG khớp bản đã công bố trên Root Anchor. KHÔNG chạy node với file này.")
		os.Exit(1)
	}
	fmt.Println("✅ KHỚP -- genesis.json cục bộ đúng với bản đã công bố trên Root Anchor.")
}

// parseSingleChainID reuses the shared -chains flag (comma-separated, for the query actions) but
// takes only the first entry -- publish-genesis-digest/verify-genesis operate on exactly one chain.
func parseSingleChainID(chainIDsFlag string) (uint64, error) {
	for _, p := range strings.Split(chainIDsFlag, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var cid uint64
		if _, err := fmt.Sscanf(p, "%d", &cid); err != nil {
			return 0, err
		}
		return cid, nil
	}
	return 0, fmt.Errorf("no chain ID found in %q", chainIDsFlag)
}

func handleTransferAllocation(
	ctx context.Context,
	privKey *ecdsa.PrivateKey,
	fromAddress common.Address,
	rootAnchorRPC string,
	fileConfig *GatewayConfigFile,
	parsedABI abi.ABI,
	fromChain, toChain uint64,
	amountMTN float64,
	amountWei string,
	timelockWaitSeconds int,
) {
	var amountBig *big.Int
	if amountWei != "" {
		var ok bool
		amountBig, ok = new(big.Int).SetString(amountWei, 10)
		if !ok {
			fmt.Printf("❌ Invalid -amount-wei: %s\n", amountWei)
			os.Exit(1)
		}
	} else {
		amountBig = new(big.Int)
		new(big.Float).Mul(big.NewFloat(amountMTN), big.NewFloat(1e18)).Int(amountBig)
	}

	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		fmt.Printf("❌ Failed to connect to Root Anchor (%s): %v\n", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	if fileConfig == nil || len(fileConfig.Chains) == 0 {
		fmt.Printf("❌ Config file required for committee signatures in transfer-alloc. Provide --config <path>\n")
		os.Exit(1)
	}

	var committee []committeeMember
	for _, c := range fileConfig.Chains {
		for _, v := range c.Validators {
			if v.BLSPrivateKey != "" {
				committee = append(committee, committeeMember{
					ChainID: c.ChainID,
					PrivHex: v.BLSPrivateKey,
				})
			}
		}
	}
	if len(committee) == 0 {
		fmt.Printf("❌ No validator BLSPrivateKeys found in config file\n")
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("🔄 CHUYỂN HẠN MỨC TIỀN CỌC LIÊN CHUỖI (TRANSFER ALLOCATION)\n")
	fmt.Printf("   ├─ Root Anchor RPC:  %s\n", rootAnchorRPC)
	fmt.Printf("   ├─ From Chain:       %d\n", fromChain)
	fmt.Printf("   ├─ To Chain:         %d\n", toChain)
	fmt.Printf("   ├─ Amount:           %s wei (%.4f MTN)\n", amountBig.String(), amountMTN)
	fmt.Printf("   ├─ Committee Size:   %d active members\n", len(committee))
	fmt.Printf("   └─ Submitter:        %s\n", fromAddress.Hex())
	fmt.Println("═══════════════════════════════════════════════════════════════")

	transferPayload, err := json.Marshal(cross_chain.AllocationTransferPayload{
		FromChainID: fromChain,
		ToChainID:   toChain,
		Amount:      amountBig,
	})
	if err != nil {
		fmt.Printf("❌ Failed to marshal AllocationTransferPayload: %v\n", err)
		os.Exit(1)
	}

	if err := proposeVoteExecute(ctx, client, privKey, fromAddress, rootAnchorRPC,
		6 /* ProposalTransferAllocation */, transferPayload, committee, timelockWaitSeconds,
		fmt.Sprintf("transfer allocation from chain %d to %d", fromChain, toChain)); err != nil {
		fmt.Printf("❌ Transfer allocation failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("🎉 THÀNH CÔNG! ĐÃ CHUYỂN HẠN MỨC %.4f MTN TỪ CHAIN %d SANG CHAIN %d!\n", amountMTN, fromChain, toChain)
	fmt.Println("═══════════════════════════════════════════════════════════════")
}

type GatewayConfigFile struct {
	RootAnchorRPC       string               `json:"root_anchor_rpc,omitempty"`
	SubmitterKey        string               `json:"submitter_key,omitempty"`
	GenesisSupply       string               `json:"genesis_supply,omitempty"`
	PerChainAllocation  string               `json:"per_chain_allocation,omitempty"`
	FundGenesis         *bool                `json:"fund_genesis,omitempty"`
	TimelockWaitSeconds *int                 `json:"timelock_wait_seconds,omitempty"`
	Chains              []ChainConfigEntry   `json:"chains"`
}

type ChainConfigEntry struct {
	ChainID         uint64                 `json:"chain_id"`
	RPCURL          string                 `json:"rpc_url"`
	QuorumThreshold uint64                 `json:"quorum_threshold,omitempty"`
	GatewayContract string                 `json:"gateway_contract,omitempty"`
	Validators      []ValidatorConfigEntry `json:"validators"`
	// StakeAmount (2026-09-04, wei as a base-10 decimal string, same convention as GenesisSupply/
	// PerChainAllocation): how much of the submitter's own real wallet balance to commit as THIS
	// chain's registerChainViaStake deposit / initial circulating allocation -- see
	// GatewayEngine.RegisterChainViaStake's doc comment for why this is now caller-chosen rather
	// than a fixed protocol constant. Empty/unset falls back to the live on-chain
	// getMinNativeStakeToRegister() floor (handleRegisterChains queries it once, lazily, only if
	// at least one chain entry needs it) -- the exact old fixed-amount behavior, unchanged for any
	// existing config that doesn't set this field.
	StakeAmount string `json:"stake_amount,omitempty"`
}

type ValidatorConfigEntry struct {
	Name          string `json:"name,omitempty"`
	NodeID        int    `json:"node_id,omitempty"`
	BLSPrivateKey string `json:"bls_private_key"`
	Stake         uint64 `json:"stake,omitempty"`
}

func loadGatewayConfigFile(filePath string) (*GatewayConfigFile, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var cfg GatewayConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config json: %w", err)
	}
	return &cfg, nil
}

func handleRegisterChains(
	ctx context.Context,
	privKey *ecdsa.PrivateKey,
	fromAddress common.Address,
	rootAnchorRPC string,
	cfg *GatewayConfigFile,
	fundGenesisFlag bool,
	genesisSupplyFlag, perChainAllocFlag string,
	timelockWaitSeconds int,
	parsedABI abi.ABI,
) {
	logger.Info("Using submitter address: %s", fromAddress.Hex())

	var payloads [][]byte
	var amounts []*big.Int
	var committee []committeeMember
	var chainIDs []uint64
	var targetRPCs []string

	// Lazily-fetched fallback for any chain entry that doesn't set its own StakeAmount -- the
	// live on-chain getMinNativeStakeToRegister() floor, i.e. exactly the old fixed-amount
	// behavior for any config that doesn't opt into a caller-chosen amount.
	var floorAmount *big.Int
	fetchFloorAmount := func() *big.Int {
		if floorAmount != nil {
			return floorAmount
		}
		client, err := ethclient.Dial(rootAnchorRPC)
		if err != nil {
			logger.Error("Failed to connect to %s to query getMinNativeStakeToRegister: %v", rootAnchorRPC, err)
			os.Exit(1)
		}
		defer client.Close()
		gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
		calldata, err := parsedABI.Pack("getMinNativeStakeToRegister")
		if err != nil {
			logger.Error("Failed to pack getMinNativeStakeToRegister: %v", err)
			os.Exit(1)
		}
		out, err := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil)
		if err != nil {
			logger.Error("Failed to call getMinNativeStakeToRegister: %v", err)
			os.Exit(1)
		}
		results, err := parsedABI.Unpack("getMinNativeStakeToRegister", out)
		if err != nil {
			logger.Error("Failed to unpack getMinNativeStakeToRegister: %v", err)
			os.Exit(1)
		}
		amt, _ := results[0].(*big.Int)
		if amt == nil || amt.Sign() <= 0 {
			logger.Error("getMinNativeStakeToRegister returned %v -- Root Anchor has no configured floor, set stake_amount explicitly in the config for every chain", amt)
			os.Exit(1)
		}
		floorAmount = amt
		return floorAmount
	}

	for _, c := range cfg.Chains {
		if c.ChainID == 0 {
			logger.Error("Encountered invalid ChainID 0 in config")
			os.Exit(1)
		}

		var committeeEntries []cross_chain.ValidatorEntry
		for _, v := range c.Validators {
			if v.BLSPrivateKey == "" {
				continue
			}
			blsPriv, blsPub, _ := bls.GenerateKeyPairFromSecretKey(v.BLSPrivateKey)
			popSig := cross_chain.PopSign(blsPriv, blsPub)

			stake := v.Stake
			if stake == 0 {
				stake = 1000
			}

			committeeEntries = append(committeeEntries, cross_chain.ValidatorEntry{
				PubkeyBLS:    blsPub.Bytes(),
				Stake:        stake,
				PopSignature: popSig.Bytes(),
			})
			committee = append(committee, committeeMember{
				ChainID: c.ChainID,
				PrivHex: v.BLSPrivateKey,
			})
		}

		if len(committeeEntries) == 0 {
			logger.Error("Chain %d has no valid validators with BLSPrivateKey in config", c.ChainID)
			os.Exit(1)
		}

		qThreshold := c.QuorumThreshold
		if qThreshold == 0 {
			qThreshold = 6667
		}
		gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
		if c.GatewayContract != "" {
			gwAddr = common.HexToAddress(c.GatewayContract)
		}

		registry := cross_chain.ChainRegistry{
			ChainID:         c.ChainID,
			Epoch:           0,
			Committee:       committeeEntries,
			QuorumThreshold: qThreshold,
			GatewayContract: gwAddr,
			// GenesisWallet (2026-09-04, deterministic-genesis design): the address that pays
			// for this registration also becomes the wallet that must hold the chain's initial
			// native-coin supply in its own genesis.json alloc -- see ChainRegistry.GenesisWallet
			// and RegisterChainViaStake's own doc comments. gateway_handler.go's
			// "registerChainViaStake" case forces this to tx.FromAddress() regardless of what
			// this payload says, so setting it to anything else here would be pointless, not
			// just wrong -- kept equal to fromAddress for clarity, not as a real security
			// boundary (the real boundary is enforced server-side).
			GenesisWallet: fromAddress,
		}
		payloadBytes, err := json.Marshal(registry)
		if err != nil {
			logger.Error("Failed to marshal registry for chain %d: %v", c.ChainID, err)
			os.Exit(1)
		}
		payloads = append(payloads, payloadBytes)
		chainIDs = append(chainIDs, c.ChainID)
		if c.StakeAmount != "" {
			amt, ok := new(big.Int).SetString(c.StakeAmount, 10)
			if !ok || amt.Sign() <= 0 {
				logger.Error("Chain %d: stake_amount %q is not a valid positive base-10 wei integer", c.ChainID, c.StakeAmount)
				os.Exit(1)
			}
			amounts = append(amounts, amt)
		} else {
			amounts = append(amounts, fetchFloorAmount())
		}
		if c.RPCURL != "" {
			targetRPCs = append(targetRPCs, fmt.Sprintf("%d=%s", c.ChainID, c.RPCURL))
		}
		logger.Info("Prepared founding entry for chain %d (%d validators, real BLS pubkeys, real PoP)", c.ChainID, len(committeeEntries))
	}

	if len(payloads) == 0 {
		logger.Error("No valid chains found in config file")
		os.Exit(1)
	}

	var registerCalldatas [][]byte
	for i, payloadBytes := range payloads {
		calldata, err := parsedABI.Pack("registerChainViaStake", payloadBytes, amounts[i])
		if err != nil {
			logger.Error("Failed to pack registerChainViaStake call for chain %d: %v", chainIDs[i], err)
			os.Exit(1)
		}
		registerCalldatas = append(registerCalldatas, calldata)
	}

	registerChains(ctx, privKey, fromAddress, rootAnchorRPC, "Root Anchor", chainIDs, registerCalldatas)

	// REVERTED 2026-09-04 (same day, found via a real run_full_pipeline.sh end-to-end run): a
	// prior version of this fix removed this second loop, reasoning it was pure self-registration
	// (redundant once genesis.json ships ChainRegistry directly). That reasoning was wrong -- this
	// submits the SAME registerChainViaStake payload to EVERY OTHER chain's own RPC too, which is
	// what makes chain 101's committee known on chain 102's OWN LOCAL ChainRegistry (and vice
	// versa) -- attestCommit/attestReserveIssuedCommit look up g.ChainRegistry[sourceChainID] on
	// whichever chain is actually processing the call, not Root Anchor's copy. Without this,
	// relaying breaks immediately with "unknown source chain ID" the moment any real cross-chain
	// message is attempted (confirmed live: relayer.log showed exactly that revert, the deploy
	// pipeline's cross-chain test timed out at 60s). GatewayRegistryMonitor exists to eventually
	// converge this same state via polling, but is not fast/reliable enough to substitute for this
	// synchronous step during a fresh deploy.
	for _, pair := range targetRPCs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			label, rpcURL := "chain "+parts[0], parts[1]
			registerChains(ctx, privKey, fromAddress, rpcURL, label, chainIDs, registerCalldatas)
		}
	}

	if fundGenesisFlag {
		fundGenesis(ctx, privKey, fromAddress, rootAnchorRPC, committee, chainIDs, genesisSupplyFlag, perChainAllocFlag, timelockWaitSeconds, parsedABI)
	}
}

func decodeRevertReason(returnBytes []byte) string {
	if len(returnBytes) == 0 {
		return ""
	}
	if bytes.Equal(returnBytes[:4], []byte{0x08, 0xc3, 0x79, 0xa0}) && len(returnBytes) >= 68 {
		strLen := binary.BigEndian.Uint64(returnBytes[60:68])
		if uint64(len(returnBytes)) >= 68+strLen {
			return string(returnBytes[68 : 68+strLen])
		}
	}
	return string(returnBytes)
}

func registerChains(ctx context.Context, privKey *ecdsa.PrivateKey, fromAddress common.Address, rpcURL, label string, chainIDs []uint64, registerCalldatas [][]byte) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		logger.Error("Failed to connect to %s at %s: %v", label, rpcURL, err)
		os.Exit(1)
	}
	defer client.Close()

	onChainID, err := client.ChainID(ctx)
	if err != nil {
		logger.Error("Failed to fetch %s ChainID: %v", label, err)
		os.Exit(1)
	}
	logger.Info("Connected to %s (ChainID: %d)", label, onChainID.Uint64())

	parsedABI, _ := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS

	for i, targetChainID := range chainIDs {
		// Pre-check if already registered to avoid reverting and paying unnecessary stake gas
		checkData, _ := parsedABI.Pack("getChainRegistry", new(big.Int).SetUint64(targetChainID))
		out, callErr := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: checkData}, nil)
		if callErr == nil {
			unpacked, unpErr := parsedABI.Unpack("getChainRegistry", out)
			if unpErr == nil && len(unpacked) > 0 {
				if exists, ok := unpacked[0].(bool); ok && exists {
					logger.Info("ℹ️ %s: chain %d is already registered in ChainRegistry -- skipping.", label, targetChainID)
					continue
				}
			}
		}

		receipt, err := sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, nil, 1_000_000,
			registerCalldatas[i], fmt.Sprintf("registerChainViaStake(chain %d)", targetChainID))
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "already registered") || strings.Contains(errStr, "already active") {
				logger.Info("ℹ️ %s: chain %d is already registered -- proceeding.", label, targetChainID)
				continue
			}
			logger.Error("❌ %s: registerChainViaStake(chain %d) failed: %v", label, targetChainID, err)
			os.Exit(1)
		}
		logger.Info("✅ registerChainViaStake(chain %d) succeeded on %s!", targetChainID, label)
		_ = receipt
	}
}

func fundGenesis(
	ctx context.Context,
	privKey *ecdsa.PrivateKey,
	fromAddress common.Address,
	rootAnchorRPC string,
	committee []committeeMember,
	chainIDs []uint64,
	genesisSupplyFlag, perChainAllocFlag string,
	timelockWaitSeconds int,
	parsedABI abi.ABI,
) {
	if genesisSupplyFlag == "" || perChainAllocFlag == "" {
		logger.Error("-fund-genesis requires both -genesis-supply and -per-chain-allocation flags to be set")
		os.Exit(1)
	}
	genesisSupply, ok := new(big.Int).SetString(genesisSupplyFlag, 10)
	if !ok || genesisSupply.Sign() <= 0 {
		logger.Error("Invalid -genesis-supply: %q", genesisSupplyFlag)
		os.Exit(1)
	}
	perChainAlloc, ok := new(big.Int).SetString(perChainAllocFlag, 10)
	if !ok || perChainAlloc.Sign() <= 0 {
		logger.Error("Invalid -per-chain-allocation: %q", perChainAllocFlag)
		os.Exit(1)
	}
	totalNeeded := new(big.Int).Mul(perChainAlloc, big.NewInt(int64(len(chainIDs))))
	if totalNeeded.Cmp(genesisSupply) > 0 {
		logger.Error("len(chains)*perChainAlloc (%s) exceeds genesisSupply (%s)", totalNeeded.String(), genesisSupply.String())
		os.Exit(1)
	}

	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		logger.Error("fundGenesis: dial Root Anchor: %v", err)
		os.Exit(1)
	}
	defer client.Close()

	rootChainIDBig, err := client.ChainID(ctx)
	if err != nil {
		logger.Error("fundGenesis: fetch Root Anchor ChainID: %v", err)
		os.Exit(1)
	}
	reserveChainID := rootChainIDBig.Uint64()
	logger.Info("fundGenesis: Root Anchor's real chain ID (used as ReserveChainID) is %d", reserveChainID)

	mintPayload, err := json.Marshal(cross_chain.AllocationGrantPayload{
		ChainID: reserveChainID,
		Amount:  genesisSupply,
	})
	if err != nil {
		logger.Error("fundGenesis: marshal mintPayload: %v", err)
		os.Exit(1)
	}

	if err := proposeVoteExecute(ctx, client, privKey, fromAddress, rootAnchorRPC,
		5 /* ProposalAllocateSupply */, mintPayload, committee, timelockWaitSeconds,
		"mint genesis supply"); err != nil {
		logger.Info("ℹ️ fundGenesis: mint genesis supply skipped (%v) -- proceeding to distribute.", err)
	} else {
		logger.Info("✅ fundGenesis: minted %s to Reserve (chain %d) on Root Anchor", genesisSupply.String(), reserveChainID)
	}

	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
	for _, cid := range chainIDs {
		// 1. Kiểm tra xem chain đã có allocation trước đó chưa
		calldata, packErr := parsedABI.Pack("getAllocation", new(big.Int).SetUint64(cid))
		if packErr == nil {
			if out, callErr := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil); callErr == nil {
				if results, unpackErr := parsedABI.Unpack("getAllocation", out); unpackErr == nil && len(results) > 0 {
					if existingAlloc, ok := results[0].(*big.Int); ok && existingAlloc.Sign() > 0 {
						logger.Info("ℹ️ fundGenesis: chain %d already has allocation (%s wei) -- skipping distribution.", cid, existingAlloc.String())
						continue
					}
				}
			}
		}

		transferPayload, err := json.Marshal(cross_chain.AllocationTransferPayload{
			FromChainID: reserveChainID,
			ToChainID:   cid,
			Amount:      perChainAlloc,
		})
		if err != nil {
			logger.Error("fundGenesis: marshal transferPayload for chain %d: %v", cid, err)
			os.Exit(1)
		}
		if err := proposeVoteExecute(ctx, client, privKey, fromAddress, rootAnchorRPC,
			6 /* ProposalTransferAllocation */, transferPayload, committee, timelockWaitSeconds,
			fmt.Sprintf("transfer allocation to chain %d", cid)); err != nil {
			logger.Info("ℹ️ fundGenesis: allocation transfer to chain %d skipped (%v)", cid, err)
			continue
		}
		logger.Info("✅ fundGenesis: transferred %s from Reserve (chain %d) to chain %d", perChainAlloc.String(), reserveChainID, cid)
	}
}

func proposeVoteExecute(
	ctx context.Context,
	client *ethclient.Client,
	privKey *ecdsa.PrivateKey,
	fromAddress common.Address,
	rpcURL string,
	kind uint8,
	payload []byte,
	committee []committeeMember,
	timelockWaitSeconds int,
	label string,
) error {
	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		return fmt.Errorf("parse GatewayABI: %w", err)
	}

	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("fetch block header for propose: %w", err)
	}

	proposeCalldata, err := parsedABI.Pack("propose", kind, payload, header.Time)
	if err != nil {
		return fmt.Errorf("pack propose(kind=%d): %w", kind, err)
	}
	receipt, err := sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, big.NewInt(100_000_000_000_000_000), 2_000_000,
		proposeCalldata, fmt.Sprintf("%s: propose", label))
	if err != nil {
		return fmt.Errorf("%s: propose: %w", label, err)
	}
	logger.Info("✅ %s: propose succeeded", label)

	block, err := client.BlockByNumber(ctx, receipt.BlockNumber)
	if err != nil {
		return fmt.Errorf("fetch propose block %v: %w", receipt.BlockNumber, err)
	}
	proposedAt := block.Time()

	var buf []byte
	buf = append(buf, kind)
	var tsBytes [8]byte
	binary.BigEndian.PutUint64(tsBytes[:], proposedAt)
	buf = append(buf, tsBytes[:]...)
	buf = append(buf, payload...)
	proposalID := crypto.Keccak256Hash(buf)
	logger.Info("%s: computed proposalID=%s (blockTime=%d)", label, proposalID.Hex(), proposedAt)

	voteNow := uint64(time.Now().Unix())
	for _, m := range committee {
		kp := bls.NewKeyPair(common.FromHex(m.PrivHex))
		voteMsg := cross_chain.ComputeGovernanceVoteMessage(proposalID, m.ChainID)
		sig := bls.Sign(kp.PrivateKey(), voteMsg)
		voteCalldata, err := parsedABI.Pack("vote", proposalID, new(big.Int).SetUint64(m.ChainID), voteNow, kp.BytesPublicKey(), sig.Bytes())
		if err != nil {
			return fmt.Errorf("pack vote(chain=%d): %w", m.ChainID, err)
		}
		if _, err := sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, nil, 1_000_000, voteCalldata,
			fmt.Sprintf("%s: vote(chain=%d)", label, m.ChainID)); err != nil {
			logger.Info("ℹ️ %s: vote(chain=%d) did not succeed (likely already past quorum/timelock): %v", label, m.ChainID, err)
		} else {
			logger.Info("✅ %s: vote(chain=%d) succeeded", label, m.ChainID)
		}
	}

	logger.Info("%s: waiting %ds for devnet timelock before executeProposal...", label, timelockWaitSeconds)
	time.Sleep(time.Duration(timelockWaitSeconds) * time.Second)
	execNow := uint64(time.Now().Unix())
	execCalldata, err := parsedABI.Pack("executeProposal", proposalID, execNow)
	if err != nil {
		return fmt.Errorf("pack executeProposal(kind=%d): %w", kind, err)
	}
	_, err = sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, nil, 1_000_000, execCalldata,
		fmt.Sprintf("%s: executeProposal", label))
	if err != nil {
		return fmt.Errorf("%s: executeProposal: %w", label, err)
	}
	logger.Info("✅ %s: executeProposal succeeded", label)
	return nil
}

func sendTxAndWait(ctx context.Context, client *ethclient.Client, privKey *ecdsa.PrivateKey, fromAddress common.Address, rpcURL string, value *big.Int, gasLimit uint64, calldata []byte, label string) (*types.Receipt, error) {
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: fetch ChainID: %w", label, err)
	}
	nonce, err := client.PendingNonceAt(ctx, fromAddress)
	if err != nil {
		return nil, fmt.Errorf("%s: get nonce: %w", label, err)
	}
	if value == nil {
		value = big.NewInt(0)
	}
	gasPrice := big.NewInt(1_000_000_000)
	gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
	tx := types.NewTransaction(nonce, gwAddr, value, gasLimit, gasPrice, calldata)
	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, privKey)
	if err != nil {
		return nil, fmt.Errorf("%s: sign tx: %w", label, err)
	}
	if err := client.SendTransaction(ctx, signedTx); err != nil {
		return nil, fmt.Errorf("%s: send tx: %w", label, err)
	}

	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		receipt, err := client.TransactionReceipt(ctx, signedTx.Hash())
		if err != nil {
			continue
		}
		if receipt.Status != 1 {
			reasonBytes, _ := client.CallContract(ctx, ethereum.CallMsg{
				From:  fromAddress,
				To:    &gwAddr,
				Gas:   gasLimit,
				Value: value,
				Data:  calldata,
			}, receipt.BlockNumber)
			reason := decodeRevertReason(reasonBytes)
			if reason == "" {
				reason = fmt.Sprintf("0x%s", common.Bytes2Hex(reasonBytes))
			}
			return receipt, fmt.Errorf("reverted (tx=%s): %s", signedTx.Hash().Hex(), reason)
		}
		return receipt, nil
	}
	return nil, fmt.Errorf("%s: timed out waiting for tx receipt (%s)", label, signedTx.Hash().Hex())
}
