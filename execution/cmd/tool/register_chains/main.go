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

func findDefaultChainsDir() string {
	if env := os.Getenv("CHAINS_DIR"); env != "" {
		return env
	}
	cwd, err := os.Getwd()
	if err == nil {
		dir := cwd
		for i := 0; i < 6; i++ {
			candidate := filepath.Join(dir, "deploy", "ansible_private_chains", "data")
			if stat, err := os.Stat(candidate); err == nil && stat.IsDir() {
				return candidate
			}
			candidateNested := filepath.Join(dir, "metanode", "deploy", "ansible_private_chains", "data")
			if stat, err := os.Stat(candidateNested); err == nil && stat.IsDir() {
				return candidateNested
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if stat, err := os.Stat("/opt/metanode"); err == nil && stat.IsDir() {
		return "/opt/metanode"
	}
	return "deploy/ansible_private_chains/data"
}

func findDefaultTargetRPCs() string {
	if data, err := os.ReadFile("/tmp/private_chains.json"); err == nil {
		var topology struct {
			Nodes map[string]string `json:"nodes"`
		}
		if err := json.Unmarshal(data, &topology); err == nil && len(topology.Nodes) > 0 {
			var pairs []string
			for cid, url := range topology.Nodes {
				pairs = append(pairs, fmt.Sprintf("%s=%s", cid, url))
			}
			return strings.Join(pairs, ",")
		}
	}
	return ""
}

func main() {
	defaultRootAnchor := findDefaultRootAnchor()
	defaultChainsDir := findDefaultChainsDir()
	defaultTargetRPCs := findDefaultTargetRPCs()

	var (
		actionFlag          string
		submitterKeyHex     string
		rootAnchorRPC       string
		chainsDir           string
		chainIDsFlag        string
		targetRPCsFlag      string
		fundGenesisFlag     bool
		genesisSupplyFlag   string
		perChainAllocFlag   string
		timelockWaitSeconds int

		// Transfer alloc flags
		fromChainFlag uint64
		toChainFlag   uint64
		amountMTNFlag float64
		amountWeiFlag string
	)

	flag.StringVar(&actionFlag, "action", "register", "Action to perform: register | transfer-alloc | query-alloc | query-registry")
	flag.StringVar(&submitterKeyHex, "key", "0xd3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9", "Sender ECDSA private key hex")
	flag.StringVar(&rootAnchorRPC, "root-anchor", defaultRootAnchor, "Root Anchor JSON-RPC endpoint (auto-detected)")
	flag.StringVar(&chainsDir, "chains-dir", defaultChainsDir, "Directory containing private chain data (auto-detected)")
	flag.StringVar(&chainIDsFlag, "chains", "101,102,103,104", "Comma-separated list of chain IDs")
	flag.StringVar(&targetRPCsFlag, "target-rpcs", defaultTargetRPCs, "Comma-separated chainID=rpcURL pairs for cross-chain mesh seeding (auto-detected)")
	flag.BoolVar(&fundGenesisFlag, "fund-genesis", false, "After bootstrapping, also mint and distribute genesis supply")
	flag.StringVar(&genesisSupplyFlag, "genesis-supply", "", "Total genesis supply to mint on Root Anchor (base-10 wei)")
	flag.StringVar(&perChainAllocFlag, "per-chain-allocation", "", "Amount transferred to each founding chain (base-10 wei)")
	flag.IntVar(&timelockWaitSeconds, "timelock-wait", 12, "Seconds to wait for devnet governance timelock")

	flag.Uint64Var(&fromChainFlag, "from-chain", 101, "Source Chain ID for transfer-alloc (e.g. 101 or 991)")
	flag.Uint64Var(&toChainFlag, "to-chain", 103, "Destination Chain ID for transfer-alloc")
	flag.Float64Var(&amountMTNFlag, "amount-mtn", 20000000, "Amount of MTN tokens to transfer (e.g. 20000000 for 20M MTN)")
	flag.StringVar(&amountWeiFlag, "amount-wei", "", "Exact amount in base-10 wei for transfer-alloc")
	flag.Parse()

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
	case "query-registry":
		handleQueryRegistry(ctx, rootAnchorRPC, chainIDsFlag, parsedABI)
	case "transfer-alloc", "transfer-allocation", "allocate-supply":
		handleTransferAllocation(ctx, privKey, fromAddress, rootAnchorRPC, chainsDir, parsedABI, fromChainFlag, toChainFlag, amountMTNFlag, amountWeiFlag, timelockWaitSeconds)
	default: // "register"
		handleRegisterChains(ctx, privKey, fromAddress, rootAnchorRPC, chainsDir, chainIDsFlag, targetRPCsFlag, fundGenesisFlag, genesisSupplyFlag, perChainAllocFlag, timelockWaitSeconds, parsedABI)
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
		fmt.Printf("   ├─ Chain %-4d: ✅ Đã đăng ký (Epoch: %d, Committee: %d validator(s), Quorum: %d%%)\n", cid, epoch, len(pubkeys), threshold/100)
		for i, pk := range pubkeys {
			fmt.Printf("   │  └─ Val[%d] BLS Pubkey: 0x%s\n", i, common.Bytes2Hex(pk))
		}
	}
	fmt.Println("═══════════════════════════════════════════════════════════════")
}

func handleTransferAllocation(
	ctx context.Context,
	privKey *ecdsa.PrivateKey,
	fromAddress common.Address,
	rootAnchorRPC, chainsDir string,
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

	committee := loadAllCommitteeMembers(chainsDir)
	if len(committee) == 0 {
		fmt.Printf("❌ No committee members found in %s\n", chainsDir)
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

func handleRegisterChains(
	ctx context.Context,
	privKey *ecdsa.PrivateKey,
	fromAddress common.Address,
	rootAnchorRPC, chainsDir, chainIDsFlag, targetRPCsFlag string,
	fundGenesisFlag bool,
	genesisSupplyFlag, perChainAllocFlag string,
	timelockWaitSeconds int,
	parsedABI abi.ABI,
) {
	logger.Info("Using submitter address: %s", fromAddress.Hex())

	var payloads [][]byte
	var committee []committeeMember
	var chainIDs []uint64
	for _, cidStr := range strings.Split(chainIDsFlag, ",") {
		cidStr = strings.TrimSpace(cidStr)
		if cidStr == "" {
			continue
		}
		var targetChainID uint64
		if _, err := fmt.Sscanf(cidStr, "%d", &targetChainID); err != nil {
			logger.Error("Invalid chain ID %q: %v", cidStr, err)
			os.Exit(1)
		}

		nodeCfg, err := loadChainNodeConfig(chainsDir, cidStr)
		if err != nil {
			logger.Error("Failed to load node config for chain %s: %v", cidStr, err)
			os.Exit(1)
		}
		if nodeCfg.Databases.BLSPrivateKey == "" {
			logger.Error("Chain %s has no Databases.BLSPrivateKey configured", cidStr)
			os.Exit(1)
		}

		blsPriv, blsPub, _ := bls.GenerateKeyPairFromSecretKey(nodeCfg.Databases.BLSPrivateKey)
		popSig := cross_chain.PopSign(blsPriv, blsPub)

		registry := cross_chain.ChainRegistry{
			ChainID: targetChainID,
			Epoch:   0,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: blsPub.Bytes(), Stake: 1000, PopSignature: popSig.Bytes()},
			},
			QuorumThreshold: 6667,
			GatewayContract: p_common.GATEWAY_CONTRACT_ADDRESS,
		}
		payloadBytes, err := json.Marshal(registry)
		if err != nil {
			logger.Error("Failed to marshal registry for chain %s: %v", cidStr, err)
			os.Exit(1)
		}
		payloads = append(payloads, payloadBytes)
		committee = append(committee, committeeMember{ChainID: targetChainID, PrivHex: nodeCfg.Databases.BLSPrivateKey})
		chainIDs = append(chainIDs, targetChainID)
		logger.Info("Prepared founding entry for chain %s (real BLS pubkey from Databases.BLSPrivateKey, real PoP)", cidStr)
	}

	if len(payloads) == 0 {
		logger.Error("No chains to register (-chains was empty)")
		os.Exit(1)
	}

	var registerCalldatas [][]byte
	for i, payloadBytes := range payloads {
		calldata, err := parsedABI.Pack("registerChainViaStake", payloadBytes)
		if err != nil {
			logger.Error("Failed to pack registerChainViaStake call for chain %d: %v", chainIDs[i], err)
			os.Exit(1)
		}
		registerCalldatas = append(registerCalldatas, calldata)
	}

	registerChains(ctx, privKey, fromAddress, rootAnchorRPC, "Root Anchor", chainIDs, registerCalldatas)

	if targetRPCsFlag != "" {
		for _, pair := range strings.Split(targetRPCsFlag, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) != 2 {
				logger.Error("Invalid -target-rpcs entry %q (expected chainID=url)", pair)
				os.Exit(1)
			}
			label, rpcURL := "chain "+parts[0], parts[1]
			registerChains(ctx, privKey, fromAddress, rpcURL, label, chainIDs, registerCalldatas)
		}
	}

	if fundGenesisFlag {
		fundGenesis(ctx, privKey, fromAddress, rootAnchorRPC, committee, chainIDs, genesisSupplyFlag, perChainAllocFlag, timelockWaitSeconds)
	}
}

func loadAllCommitteeMembers(chainsDir string) []committeeMember {
	var committee []committeeMember
	entries, err := os.ReadDir(chainsDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var cid uint64
		if strings.HasPrefix(entry.Name(), "chain_") {
			_, _ = fmt.Sscanf(entry.Name(), "chain_%d", &cid)
		} else if strings.HasPrefix(entry.Name(), "chain-") {
			_, _ = fmt.Sscanf(entry.Name(), "chain-%d", &cid)
		} else {
			continue
		}
		if cid == 0 {
			continue
		}
		cfg, err := loadChainNodeConfig(chainsDir, fmt.Sprintf("%d", cid))
		if err == nil && cfg.Databases.BLSPrivateKey != "" {
			committee = append(committee, committeeMember{
				ChainID: cid,
				PrivHex: cfg.Databases.BLSPrivateKey,
			})
		}
	}
	return committee
}

type chainConfig struct {
	Databases struct {
		BLSPrivateKey string
	}
}

func loadChainNodeConfig(chainsDir, cidStr string) (*chainConfig, error) {
	candidates := []string{
		filepath.Join(chainsDir, fmt.Sprintf("chain_%s", cidStr), "node-0", "config.json"),
		filepath.Join(chainsDir, fmt.Sprintf("chain-%s", cidStr), "config", "execution.json"),
		filepath.Join(chainsDir, fmt.Sprintf("chain_%s", cidStr), "config.json"),
	}
	for _, path := range candidates {
		if data, err := os.ReadFile(path); err == nil {
			var app chainConfig
			if err := json.Unmarshal(data, &app); err == nil && app.Databases.BLSPrivateKey != "" {
				return &app, nil
			}
		}
	}
	return nil, fmt.Errorf("could not find valid config for chain %s in %s", cidStr, chainsDir)
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

	for _, cid := range chainIDs {
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
			if strings.Contains(err.Error(), "insufficient allocation") {
				logger.Info("ℹ️ fundGenesis: allocation transfer to chain %d skipped (Reserve supply already distributed: %v)", cid, err)
				continue
			}
			logger.Error("❌ fundGenesis: allocation transfer to chain %d failed: %v", cid, err)
			os.Exit(1)
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
