package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

// Config holds deployment configuration
type Config struct {
	RPCUrl             string
	WSUrl              string // WS_URL in .env → WebSocket endpoint for event subscription
	RemoteRPCUrl       string // REMOTE_WS_URL in .env → RPC/WS endpoint for remote chain
	DeployerPrivateKey string
	SourceNationID     *big.Int
	DestNationID       *big.Int
	GatewayAddress     common.Address   // First CONTRACT_ADDRESS (backward compat)
	GatewayAddresses   []common.Address // All CONTRACT_ADDRESS entries (multi-channel)
	RemoteGateway      common.Address   // First SMC_REMOTE (backward compat)
	RemoteGateways     []common.Address // All SMC_REMOTE entries (multi-channel)
	PayloadCC          string
	AddressLockBalance common.Address // ADDRESS_LOCK_BALANCE in .env → recipient for lockAndBridge
	Relayers           []common.Address
	Relayers1          []common.Address
	Relayers2          []common.Address
	Relayers3          []common.Address
	Relayers4          []common.Address
	ClientPKs          []string
}

// BytecodeData holds bytecode from JSON file
type BytecodeData struct {
	CrossChain string `json:"cross_chain"` // CrossChainGateway bytecode
	ContractCC string `json:"contract_cc"` // contract_cc (test receiver) bytecode
}

var (
	globalClient         *ethclient.Client
	globalRemoteClient   *ethclient.Client // Client for remote chain (REMOTE_WS_URL)
	globalAuth           *bind.TransactOpts
	globalGatewayAddress common.Address // deployed CrossChainGateway address
	globalCCAddress      common.Address // deployed contract_cc address
	globalGatewayABI     abi.ABI
	globalCCABI          abi.ABI
	globalConfig         *Config
)

func main() {
	// Parse -env flag before loading env
	envFile := ".env.crosschain"
	for i, arg := range os.Args[1:] {
		if arg == "-env" && i+1 < len(os.Args[1:])-0 {
			envFile = os.Args[i+2]
		} else if strings.HasPrefix(arg, "-env=") {
			envFile = strings.TrimPrefix(arg, "-env=")
		}
	}

	// Load environment variables
	err := godotenv.Load(envFile)
	if err != nil {
		log.Printf("Warning: Error loading %s file: %v", envFile, err)
		godotenv.Load(".env")
	} else {
		log.Printf("✅ Loaded env file: %s", envFile)
	}

	sourceNationID := getEnvBigInt("SOURCE_NATION_ID", "1")
	destNationID := getEnvBigInt("DEST_NATION_ID", "2")

	// Parse RELAYERS list
	var relayers []common.Address
	relayersStr := getEnv("RELAYERS", "")
	if relayersStr != "" {
		for _, r := range strings.Split(relayersStr, ",") {
			r = strings.TrimSpace(r)
			if common.IsHexAddress(r) {
				relayers = append(relayers, common.HexToAddress(r))
			}
		}
	}

	// Parse RELAYERS_1 .. RELAYERS_4 lists
	parseRelayerList := func(envKey string) []common.Address {
		var list []common.Address
		s := getEnv(envKey, "")
		if s != "" {
			for _, r := range strings.Split(s, ",") {
				r = strings.TrimSpace(r)
				if common.IsHexAddress(r) {
					list = append(list, common.HexToAddress(r))
				}
			}
		}
		return list
	}
	relayers1 := parseRelayerList("RELAYERS_1")
	relayers2 := parseRelayerList("RELAYERS_2")
	relayers3 := parseRelayerList("RELAYERS_3")
	relayers4 := parseRelayerList("RELAYERS_4")

	// Parse CLIENT_PK list
	var clientPKs []string
	clientPKStr := getEnv("CLIENT_PK", "")
	if clientPKStr != "" {
		for _, pk := range strings.Split(clientPKStr, ",") {
			pk = strings.TrimSpace(pk)
			if pk != "" {
				clientPKs = append(clientPKs, pk)
			}
		}
	}

	// Parse multi-channel CONTRACT_ADDRESS and SMC_REMOTE
	var gatewayAddresses []common.Address
	contractAddrStr := getEnv("CONTRACT_ADDRESS", "")
	if contractAddrStr != "" {
		for _, a := range strings.Split(contractAddrStr, ",") {
			a = strings.TrimSpace(a)
			if common.IsHexAddress(a) {
				gatewayAddresses = append(gatewayAddresses, common.HexToAddress(a))
			}
		}
	}

	var remoteGateways []common.Address
	remoteStr := getEnv("SMC_REMOTE", "")
	if remoteStr != "" {
		for _, a := range strings.Split(remoteStr, ",") {
			a = strings.TrimSpace(a)
			if common.IsHexAddress(a) {
				remoteGateways = append(remoteGateways, common.HexToAddress(a))
			}
		}
	}

	var firstGateway, firstRemote common.Address
	if len(gatewayAddresses) > 0 {
		firstGateway = gatewayAddresses[0]
	}
	if len(remoteGateways) > 0 {
		firstRemote = remoteGateways[0]
	}

	config := &Config{
		RPCUrl:             getEnv("RPC_URL", "http://192.168.1.234:8545"),
		WSUrl:              getEnv("WS_URL", ""),
		RemoteRPCUrl:       getEnv("REMOTE_WS_URL", ""),
		DeployerPrivateKey: getEnv("PRIVATE_KEY", ""),
		SourceNationID:     sourceNationID,
		DestNationID:       destNationID,
		GatewayAddress:     firstGateway,
		GatewayAddresses:   gatewayAddresses,
		RemoteGateway:      firstRemote,
		RemoteGateways:     remoteGateways,
		PayloadCC:          getEnv("PAYLOAD_CC", ""),
		AddressLockBalance: common.HexToAddress(getEnv("ADDRESS_LOCK_BALANCE", "")),
		Relayers:           relayers,
		Relayers1:          relayers1,
		Relayers2:          relayers2,
		Relayers3:          relayers3,
		Relayers4:          relayers4,
		ClientPKs:          clientPKs,
	}
	globalConfig = config

	if config.DeployerPrivateKey == "" {
		log.Fatal("PRIVATE_KEY is required in .env file")
	}

	var rpcClient *rpc.Client
	ctx := context.Background()
	rpcUrl := config.RPCUrl
	parsedURL, err := url.Parse(rpcUrl)
	if err != nil {
		log.Fatalf("Failed to parse RPC URL: %v", err)
	}

	switch parsedURL.Scheme {
	case "https":
		insecureTLSConfig := &tls.Config{InsecureSkipVerify: true}
		transport := &http.Transport{TLSClientConfig: insecureTLSConfig}
		httpClient := &http.Client{Transport: transport}
		rpcClient, err = rpc.DialHTTPWithClient(rpcUrl, httpClient)
	case "wss":
		dialer := *websocket.DefaultDialer
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		rpcClient, err = rpc.DialWebsocketWithDialer(ctx, rpcUrl, "", dialer)
	default:
		log.Printf("Connecting to RPC URL with default settings (scheme: %s).", parsedURL.Scheme)
		rpcClient, err = rpc.DialContext(ctx, rpcUrl)
	}
	if err != nil {
		log.Fatalf("Failed to connect to RPC: %v", err)
	}
	client := ethclient.NewClient(rpcClient)
	globalClient = client

	// ── Create remote client if REMOTE_WS_URL is set ──
	remoteRPCUrl := config.RemoteRPCUrl
	if remoteRPCUrl != "" {
		var remoteRpcClient *rpc.Client
		parsedRemote, errR := url.Parse(remoteRPCUrl)
		if errR != nil {
			log.Printf("⚠️  Failed to parse REMOTE_WS_URL: %v", errR)
		} else {
			switch parsedRemote.Scheme {
			case "wss":
				dialer := *websocket.DefaultDialer
				dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				remoteRpcClient, errR = rpc.DialWebsocketWithDialer(ctx, remoteRPCUrl, "", dialer)
			default:
				remoteRpcClient, errR = rpc.DialContext(ctx, remoteRPCUrl)
			}
			if errR != nil {
				log.Printf("⚠️  Failed to connect to REMOTE_WS_URL (%s): %v", remoteRPCUrl, errR)
			} else {
				globalRemoteClient = ethclient.NewClient(remoteRpcClient)
				log.Printf("✅ Remote client connected: %s", remoteRPCUrl)
			}
		}
	}

	deployerAuth, err := getDeployerAuth(client, config.DeployerPrivateKey)
	if err != nil {
		log.Fatalf("Failed to get deployer auth: %v", err)
	}
	globalAuth = deployerAuth

	log.Printf("🚀 Cross-Chain Deployer Tool")
	log.Printf("📍 Deployer  : %s", deployerAuth.From.Hex())
	log.Printf("🌐 RPC URL   : %s", config.RPCUrl)
	log.Printf("🔗 Source ID : %s  |  Dest ID: %s", config.SourceNationID.String(), config.DestNationID.String())
	if len(config.GatewayAddresses) > 0 {
		log.Printf("🏛️  Gateways (env)  : %d channels", len(config.GatewayAddresses))
		for i, gw := range config.GatewayAddresses {
			log.Printf("   [%d] %s", i+1, gw.Hex())
		}
	}
	if len(config.RemoteGateways) > 0 {
		log.Printf("🌐 Remote Gateways : %d channels", len(config.RemoteGateways))
		for i, rg := range config.RemoteGateways {
			log.Printf("   [%d] %s", i+1, rg.Hex())
		}
	}
	if config.AddressLockBalance != (common.Address{}) {
		log.Printf("🔐 LockBalance addr: %s", config.AddressLockBalance.Hex())
	}
	if config.PayloadCC != "" {
		log.Printf("📦 PAYLOAD_CC      : %s", config.PayloadCC)
	}
	if len(config.Relayers) > 0 {
		log.Printf("🔑 RELAYERS        : %d addresses", len(config.Relayers))
		for i, r := range config.Relayers {
			log.Printf("   [%d] %s", i+1, r.Hex())
		}
	}
	if len(config.ClientPKs) > 0 {
		log.Printf("🧪 CLIENT_PK       : %d wallets for spam test", len(config.ClientPKs))
	}
	log.Println("=====================================")

	showInteractiveMenu(client, deployerAuth, config)
}

// ─────────────────────────────────────────────────────────────────────────────
// showInteractiveMenu
// ─────────────────────────────────────────────────────────────────────────────
func showInteractiveMenu(client *ethclient.Client, auth *bind.TransactOpts, config *Config) {
	// Load Gateway ABI
	gatewayABI, err := loadABI([]string{
		"../../pkg/abi/cross_chain_abi.json",
	})
	if err != nil {
		log.Fatalf("Cannot load gateway ABI: %v", err)
	}
	globalGatewayABI = gatewayABI

	// Load contract_cc ABI
	ccABI, err := loadABI([]string{"contract_cc_abi.json"})
	if err != nil {
		log.Printf("⚠️  Cannot load contract_cc ABI: %v", err)
	} else {
		globalCCABI = ccABI
	}

	scanner := bufio.NewScanner(os.Stdin)

	// Gateway luôn dùng CONTRACT_ADDRESS từ env; deploy mới không thay đổi
	resolveGateway := func() common.Address {
		return config.GatewayAddress
	}

	for {
		gwAddr := resolveGateway()
		ccAddr := globalCCAddress

		fmt.Println("\n=====================================")
		fmt.Println("📋  Cross-Chain Tool Menu")
		fmt.Println("=====================================")
		fmt.Println("1. Deploy CrossChainGateway (cross_chain)")
		fmt.Println("2. Deploy contract_cc        (receiver contract)")
		fmt.Println("─────────────────── Gateway calls ───────────────────")
		fmt.Println("3. getChannelInfo")
		fmt.Println("4. adminSetChannelState  (nhập _outboundNonce, _inboundNonce, _confirmationNonce)")
		fmt.Println("5. sendMessage           (nhập target, amount, dùng PAYLOAD_CC)")
		fmt.Println("6. lockAndBridge         (recipient=ADDRESS_LOCK_BALANCE, nhập amount)")
		fmt.Println("─────────────────── Balance checks ──────────────────")
		fmt.Println("7. getContractBalance    (CONTRACT_ADDRESS & ADDRESS_LOCK_BALANCE)")
		fmt.Println("8. Check Balances (Wallet & Gateway & contract_cc)")
		fmt.Println("─────────────────────────────────────────────────────")
		fmt.Println("9. Fund Contract         (gửi ETH thẳng vào contract, mặc định 100 ETH)")
		fmt.Println("─────────────────── Spam test ───────────────────────")
		fmt.Println("10. Spam Test            (dùng CLIENT_PK, chọn lockAndBridge / sendMessage)")
		fmt.Println("─────────────────── Approval check ──────────────────")
		fmt.Println("11. Check Approval Status (nhập messageId, xem relayer nào đã submit)")
		fmt.Println("12. Check Confirmation Approval (nhập messageId, eventNonce, isSuccess)")
		fmt.Println("─────────────────────────────────────────────────────")
		fmt.Println("0. Exit")

		if gwAddr != (common.Address{}) {
			fmt.Printf("\n🏛️  Gateway      : %s\n", gwAddr.Hex())
		} else {
			fmt.Println("\n⚠️  Gateway      : chưa có (deploy option 1 hoặc set CONTRACT_ADDRESS)")
		}
		if ccAddr != (common.Address{}) {
			fmt.Printf("📦 contract_cc   : %s\n", ccAddr.Hex())
		} else {
			fmt.Println("⚠️  contract_cc   : chưa deploy (option 2)")
		}
		if config.AddressLockBalance != (common.Address{}) {
			fmt.Printf("🔐 LockBalance   : %s\n", config.AddressLockBalance.Hex())
		}

		fmt.Print("\nNhập lựa chọn: ")
		if !scanner.Scan() {
			break
		}
		choice := strings.TrimSpace(scanner.Text())

		switch choice {

		// ── 1. Deploy CrossChainGateway ─────────────────────────────────────
		case "1":
			addr, err := deployFromBytecode(client, auth,
				"CrossChainGateway", "cross_chain",
				gatewayABI, config.SourceNationID, config.DestNationID)
			if err != nil {
				fmt.Printf("❌ Deploy failed: %v\n", err)
				break
			}
			globalGatewayAddress = addr
			fmt.Printf("\n✅ CrossChainGateway deployed!\n   📍 Address: %s\n", addr.Hex())
			fmt.Printf("   👤 Owner/Deployer: %s (NOT a relayer, chỉ quản lý contract)\n", auth.From.Hex())

			// ── Chọn danh sách relayer ──
			type relayerOption struct {
				name     string
				relayers []common.Address
			}
			relayerOptions := []relayerOption{}
			if len(config.Relayers) > 0 {
				relayerOptions = append(relayerOptions, relayerOption{"RELAYERS", config.Relayers})
			}
			if len(config.Relayers1) > 0 {
				relayerOptions = append(relayerOptions, relayerOption{"RELAYERS_1", config.Relayers1})
			}
			if len(config.Relayers2) > 0 {
				relayerOptions = append(relayerOptions, relayerOption{"RELAYERS_2", config.Relayers2})
			}
			if len(config.Relayers3) > 0 {
				relayerOptions = append(relayerOptions, relayerOption{"RELAYERS_3", config.Relayers3})
			}
			if len(config.Relayers4) > 0 {
				relayerOptions = append(relayerOptions, relayerOption{"RELAYERS_4", config.Relayers4})
			}

			var selectedRelayers []common.Address
			var selectedRelayerName string
			if len(relayerOptions) == 0 {
				fmt.Println("\n⚠️  Không có danh sách relayer nào (RELAYERS, RELAYERS_1 đều trống).")
				fmt.Println("   Set RELAYERS trong .env.crosschain để thêm relayer addresses.")
			} else if len(relayerOptions) == 1 {
				selectedRelayers = relayerOptions[0].relayers
				selectedRelayerName = relayerOptions[0].name
				fmt.Printf("\n🔑 Sử dụng %s (%d relayers)\n", selectedRelayerName, len(selectedRelayers))
			} else {
				fmt.Println("\n🔑 Chọn danh sách relayer:")
				for i, opt := range relayerOptions {
					fmt.Printf("   [%d] %s (%d relayers)\n", i+1, opt.name, len(opt.relayers))
					for j, r := range opt.relayers {
						fmt.Printf("        %d. %s\n", j+1, r.Hex())
					}
				}
				fmt.Printf("Chọn (Enter = 1 = RELAYERS): ")
				if scanner.Scan() {
					rInput := strings.TrimSpace(scanner.Text())
					if rInput == "" || rInput == "1" {
						selectedRelayers = relayerOptions[0].relayers
						selectedRelayerName = relayerOptions[0].name
					} else {
						idx, ok := new(big.Int).SetString(rInput, 10)
						if ok && idx.Int64() >= 1 && int(idx.Int64()) <= len(relayerOptions) {
							selectedRelayers = relayerOptions[int(idx.Int64())-1].relayers
							selectedRelayerName = relayerOptions[int(idx.Int64())-1].name
						} else {
							fmt.Printf("⚠️  Lựa chọn không hợp lệ, dùng mặc định RELAYERS\n")
							selectedRelayers = relayerOptions[0].relayers
							selectedRelayerName = relayerOptions[0].name
						}
					}
				}
				fmt.Printf("✅ Sử dụng %s (%d relayers)\n", selectedRelayerName, len(selectedRelayers))
			}

			// ── Post-deploy: addRelayer cho từng address ──
			if len(selectedRelayers) > 0 {
				fmt.Printf("\n🔑 Adding %d relayers from %s...\n", len(selectedRelayers), selectedRelayerName)
				for i, relayer := range selectedRelayers {
					fmt.Printf("   [%d] Adding relayer: %s\n", i+1, relayer.Hex())
					if err := callContractMethod(client, auth, addr, gatewayABI, "addRelayer", relayer); err != nil {
						fmt.Printf("   ❌ addRelayer failed: %v\n", err)
					} else {
						fmt.Printf("   ✅ addRelayer succeeded!\n")
					}
					refreshNonce(client, auth)
				}
			}

			// threshold tự động = ceil(relayerList.length * 2 / 3), không cần set thủ công

			// Collect relayer addresses for deployment info
			relayerHexList := make([]string, 0, len(selectedRelayers))
			for _, r := range selectedRelayers {
				relayerHexList = append(relayerHexList, r.Hex())
			}
			totalRelayers := len(relayerHexList)
			dynamicThreshold := 0
			if totalRelayers > 0 {
				dynamicThreshold = (totalRelayers*2 + 2) / 3
			}
			fmt.Printf("\n🔐 Threshold tự động: %d/%d (ceil 2/3)\n", dynamicThreshold, totalRelayers)

			saveDeploymentInfo(map[string]interface{}{
				"crossChainGateway":  addr.Hex(),
				"sourceNationID":     config.SourceNationID.String(),
				"destNationID":       config.DestNationID.String(),
				"deployer_owner":     auth.From.Hex(),
				"relayers":           relayerHexList,
				"signatureThreshold": fmt.Sprintf("dynamic: ceil(%d * 2/3) = %d", totalRelayers, dynamicThreshold),
				"rpcUrl":             config.RPCUrl,
				"timestamp":          time.Now().Format(time.RFC3339),
			})
			refreshNonce(client, auth)

		// ── 2. Deploy contract_cc ────────────────────────────────────────────
		case "2":
			addr, err := deployFromBytecode(client, auth,
				"contract_cc", "contract_cc",
				abi.ABI{}, // no constructor args
				nil, nil)
			if err != nil {
				fmt.Printf("❌ Deploy failed: %v\n", err)
				break
			}
			globalCCAddress = addr
			fmt.Printf("\n✅ contract_cc deployed!\n   📍 Address: %s\n", addr.Hex())

			saveDeploymentInfo(map[string]interface{}{
				"contract_cc": addr.Hex(),
				"deployer":    auth.From.Hex(),
				"rpcUrl":      config.RPCUrl,
				"timestamp":   time.Now().Format(time.RFC3339),
			})
			refreshNonce(client, auth)

		// ── 3. getChannelInfo ─────────────────────────────────────────
		case "3":
			if len(config.GatewayAddresses) == 0 {
				fmt.Println("❌ Chưa có gateway address (set CONTRACT_ADDRESS trong .env).")
				break
			}
			fmt.Println("\n📋 Danh sách contract (từ CONTRACT_ADDRESS):")
			for i, gw := range config.GatewayAddresses {
				fmt.Printf("   [%d] %s\n", i+1, gw.Hex())
			}
			fmt.Printf("Chọn contract (Enter = 1, 'all' = tất cả): ")
			var addrsToQuery []common.Address
			if scanner.Scan() {
				cInput := strings.TrimSpace(scanner.Text())
				if cInput == "" || cInput == "1" {
					addrsToQuery = []common.Address{config.GatewayAddresses[0]}
				} else if strings.ToLower(cInput) == "all" {
					addrsToQuery = config.GatewayAddresses
				} else {
					idx, ok := new(big.Int).SetString(cInput, 10)
					if !ok || idx.Int64() < 1 || int(idx.Int64()) > len(config.GatewayAddresses) {
						fmt.Printf("❌ Lựa chọn không hợp lệ: %s\n", cInput)
						break
					}
					addrsToQuery = []common.Address{config.GatewayAddresses[int(idx.Int64())-1]}
				}
			}
			for _, addr := range addrsToQuery {
				callGetChannelInfo(client, addr, gatewayABI)
			}

		// ── 4. adminSetChannelState ─────────────────────────────────────────
		case "4":
			addr := resolveGateway()
			if addr == (common.Address{}) {
				fmt.Println("❌ Chưa có gateway address.")
				break
			}
			fmt.Print("Nhập _outboundNonce: ")
			if !scanner.Scan() {
				break
			}
			outboundNonce, ok := new(big.Int).SetString(strings.TrimSpace(scanner.Text()), 10)
			if !ok {
				fmt.Println("❌ Giá trị không hợp lệ")
				break
			}
			fmt.Print("Nhập _inboundNonce: ")
			if !scanner.Scan() {
				break
			}
			inboundNonce, ok := new(big.Int).SetString(strings.TrimSpace(scanner.Text()), 10)
			if !ok {
				fmt.Println("❌ Giá trị không hợp lệ")
				break
			}
			fmt.Print("Nhập _confirmationNonce: ")
			if !scanner.Scan() {
				break
			}
			confirmNonce, ok := new(big.Int).SetString(strings.TrimSpace(scanner.Text()), 10)
			if !ok {
				fmt.Println("❌ Giá trị không hợp lệ")
				break
			}
			callAdminSetChannelState(client, auth, addr, gatewayABI, outboundNonce, inboundNonce, confirmNonce)
			refreshNonce(client, auth)

		// ── 5. sendMessage ──────────────────────────────────────────────────
		case "5":
			if len(config.GatewayAddresses) == 0 {
				fmt.Println("❌ Chưa có gateway address (set CONTRACT_ADDRESS trong .env).")
				break
			}
			// Chọn contract, mặc định contract 1
			fmt.Println("\n📋 Danh sách contract (từ CONTRACT_ADDRESS):")
			for i, gw := range config.GatewayAddresses {
				fmt.Printf("   [%d] %s\n", i+1, gw.Hex())
			}
			fmt.Printf("Chọn contract (Enter = 1): ")
			var addr common.Address
			if scanner.Scan() {
				cInput := strings.TrimSpace(scanner.Text())
				if cInput == "" || cInput == "1" {
					addr = config.GatewayAddresses[0]
				} else {
					idx, ok := new(big.Int).SetString(cInput, 10)
					if !ok || idx.Int64() < 1 || int(idx.Int64()) > len(config.GatewayAddresses) {
						fmt.Printf("❌ Lựa chọn không hợp lệ: %s\n", cInput)
						break
					}
					addr = config.GatewayAddresses[int(idx.Int64())-1]
				}
			}
			if addr == (common.Address{}) {
				break
			}
			fmt.Printf("✅ Sử dụng contract: %s\n", addr.Hex())

			fmt.Print("Nhập target contract address: ")
			if !scanner.Scan() {
				break
			}
			targetStr := strings.TrimSpace(scanner.Text())
			if !common.IsHexAddress(targetStr) {
				fmt.Printf("❌ Địa chỉ không hợp lệ: %s\n", targetStr)
				break
			}
			targetAddr := common.HexToAddress(targetStr)

			fmt.Print("Nhập amount (ETH, vd: 0.01): ")
			if !scanner.Scan() {
				break
			}
			amountETH, ok := new(big.Float).SetString(strings.TrimSpace(scanner.Text()))
			if !ok {
				fmt.Println("❌ Amount không hợp lệ")
				break
			}
			weiMul := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
			amountWei, _ := new(big.Float).Mul(amountETH, weiMul).Int(nil)

			// Nhập related addresses (optional)
			fmt.Print("Nhập related addresses (comma-separated, Enter = rỗng): ")
			var relatedAddrs []common.Address
			if scanner.Scan() {
				relInput := strings.TrimSpace(scanner.Text())
				if relInput != "" {
					for _, addrStr := range strings.Split(relInput, ",") {
						addrStr = strings.TrimSpace(addrStr)
						if common.IsHexAddress(addrStr) {
							relatedAddrs = append(relatedAddrs, common.HexToAddress(addrStr))
						} else {
							fmt.Printf("⚠️  Bỏ qua address không hợp lệ: %s\n", addrStr)
						}
					}
				}
			}

			callSendMessage(client, auth, addr, gatewayABI, targetAddr, amountWei, config.PayloadCC, relatedAddrs)
			refreshNonce(client, auth)

		// ── 6. lockAndBridge (1 ETH cố định) ───────────────────────────────────
		case "6":
			if len(config.GatewayAddresses) == 0 {
				fmt.Println("❌ Chưa có gateway address (set CONTRACT_ADDRESS trong .env).")
				break
			}
			// Chọn contract, mặc định contract 1
			fmt.Println("\n📋 Danh sách contract (từ CONTRACT_ADDRESS):")
			for i, gw := range config.GatewayAddresses {
				fmt.Printf("   [%d] %s\n", i+1, gw.Hex())
			}
			fmt.Printf("Chọn contract (Enter = 1): ")
			var addr common.Address
			if scanner.Scan() {
				cInput := strings.TrimSpace(scanner.Text())
				if cInput == "" || cInput == "1" {
					addr = config.GatewayAddresses[0]
				} else {
					idx, ok := new(big.Int).SetString(cInput, 10)
					if !ok || idx.Int64() < 1 || int(idx.Int64()) > len(config.GatewayAddresses) {
						fmt.Printf("❌ Lựa chọn không hợp lệ: %s\n", cInput)
						break
					}
					addr = config.GatewayAddresses[int(idx.Int64())-1]
				}
			}
			if addr == (common.Address{}) {
				break
			}
			fmt.Printf("✅ Sử dụng contract: %s\n", addr.Hex())

			recipient := config.AddressLockBalance
			if recipient == (common.Address{}) {
				fmt.Println("❌ ADDRESS_LOCK_BALANCE chưa được set trong .env.crosschain")
				break
			}
			// Cố định 1 ETH
			one := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
			callLockAndBridge(client, auth, addr, gatewayABI, recipient, one)
			refreshNonce(client, auth)

		// ── 7. getContractBalance (CONTRACT_ADDRESS & ADDRESS_LOCK_BALANCE) ──
		case "7":
			callGetBalances(client, config.GatewayAddress, config.AddressLockBalance)

		// ── 8. Check Balances ───────────────────────────────────────────────
		case "8":
			checkBalances(client, auth.From, resolveGateway(), globalCCAddress, config.AddressLockBalance)

		// ── 9. Fund Contract (raw ETH transfer) ──────────────────────────────
		case "9":
			if len(config.GatewayAddresses) == 0 {
				fmt.Println("❌ Chưa có gateway address (set CONTRACT_ADDRESS trong .env).")
				break
			}

			// Hiển thị danh sách contract từ env
			fmt.Println("\n💰 Danh sách contract (từ CONTRACT_ADDRESS):")
			for i, gw := range config.GatewayAddresses {
				b, err := client.BalanceAt(context.Background(), gw, nil)
				balStr := "?"
				if err == nil {
					eth := new(big.Float).Quo(new(big.Float).SetInt(b), big.NewFloat(1e18))
					balStr = eth.Text('f', 4) + " ETH"
				}
				fmt.Printf("   [%d] %s  (%s)\n", i+1, gw.Hex(), balStr)
			}
			fmt.Printf("Chọn contract để fund (Enter = 1, 'all' = tất cả, hoặc nhập số): ")
			if !scanner.Scan() {
				break
			}
			fundInput := strings.TrimSpace(scanner.Text())

			var fundTargets []common.Address
			if fundInput == "" || fundInput == "1" {
				fundTargets = []common.Address{config.GatewayAddresses[0]}
			} else if strings.ToLower(fundInput) == "all" {
				fundTargets = config.GatewayAddresses
			} else if common.IsHexAddress(fundInput) {
				fundTargets = []common.Address{common.HexToAddress(fundInput)}
			} else {
				idx, ok := new(big.Int).SetString(fundInput, 10)
				if !ok || idx.Int64() < 1 || int(idx.Int64()) > len(config.GatewayAddresses) {
					fmt.Printf("❌ Lựa chọn không hợp lệ: %s\n", fundInput)
					break
				}
				fundTargets = []common.Address{config.GatewayAddresses[int(idx.Int64())-1]}
			}

			// Amount – mặc định 100 ETH
			fmt.Print("Nhập amount ETH mỗi contract (Enter = 100): ")
			if !scanner.Scan() {
				break
			}
			amtStr := strings.TrimSpace(scanner.Text())
			if amtStr == "" {
				amtStr = "100"
			}
			amtETH, ok := new(big.Float).SetString(amtStr)
			if !ok {
				fmt.Println("❌ Amount không hợp lệ")
				break
			}
			weiMul := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
			amtWei, _ := new(big.Float).Mul(amtETH, weiMul).Int(nil)

			for i, target := range fundTargets {
				fmt.Printf("\n── Fund [%d/%d] %s ──\n", i+1, len(fundTargets), target.Hex())
				fundContract(client, auth, target, amtWei)
				refreshNonce(client, auth)
			}

		// ── 10. Spam Test ────────────────────────────────────────────────
		case "10":
			spamTest(client, config, scanner, gatewayABI)

		// ── 11. Check Approval Status ──────────────────────────────────────
		case "11":
			addr := resolveGateway()
			if addr == (common.Address{}) {
				fmt.Println("❌ Chưa có gateway address.")
				break
			}
			fmt.Print("Nhập messageId (bytes32 hex, vd: 0xabc...): ")
			if !scanner.Scan() {
				break
			}
			msgIdStr := strings.TrimSpace(scanner.Text())
			if len(msgIdStr) == 0 {
				fmt.Println("❌ messageId không được để trống")
				break
			}
			msgIdBytes := common.FromHex(msgIdStr)
			if len(msgIdBytes) != 32 {
				fmt.Printf("❌ messageId phải là 32 bytes (got %d bytes)\n", len(msgIdBytes))
				break
			}
			var messageId [32]byte
			copy(messageId[:], msgIdBytes)
			callCheckApprovalStatus(client, addr, gatewayABI, messageId)

		// ── 12. Check Confirmation Approval Status ─────────────────────────
		case "12":
			addr := resolveGateway()
			if addr == (common.Address{}) {
				fmt.Println("❌ Chưa có gateway address.")
				break
			}
			fmt.Print("Nhập messageId (bytes32 hex, vd: 0xabc...): ")
			if !scanner.Scan() {
				break
			}
			msgIdStr12 := strings.TrimSpace(scanner.Text())
			if len(msgIdStr12) == 0 {
				fmt.Println("❌ messageId không được để trống")
				break
			}
			msgIdBytes12 := common.FromHex(msgIdStr12)
			if len(msgIdBytes12) != 32 {
				fmt.Printf("❌ messageId phải là 32 bytes (got %d bytes)\n", len(msgIdBytes12))
				break
			}
			var messageId12 [32]byte
			copy(messageId12[:], msgIdBytes12)

			fmt.Print("Nhập eventNonce (uint256, vd: 0): ")
			if !scanner.Scan() {
				break
			}
			eventNonce, ok := new(big.Int).SetString(strings.TrimSpace(scanner.Text()), 10)
			if !ok {
				fmt.Println("❌ eventNonce không hợp lệ")
				break
			}

			fmt.Print("Nhập isSuccess (true/false): ")
			if !scanner.Scan() {
				break
			}
			isSuccessStr := strings.ToLower(strings.TrimSpace(scanner.Text()))
			var isSuccess bool
			if isSuccessStr == "true" || isSuccessStr == "1" {
				isSuccess = true
			} else if isSuccessStr == "false" || isSuccessStr == "0" {
				isSuccess = false
			} else {
				fmt.Println("❌ isSuccess phải là true/false")
				break
			}

			callCheckConfirmationApprovalStatus(client, addr, gatewayABI, messageId12, eventNonce, isSuccess)

		case "0":
			fmt.Println("\n👋 Goodbye!")
			return

		default:
			fmt.Println("❌ Lựa chọn không hợp lệ. Nhập 0-12.")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// fundContract – raw ETH transfer thẳng vào địa chỉ contract (không cần ABI)
// ─────────────────────────────────────────────────────────────────────────────
func fundContract(client *ethclient.Client, auth *bind.TransactOpts, target common.Address, amountWei *big.Int) {
	ethF := new(big.Float).Quo(new(big.Float).SetInt(amountWei), big.NewFloat(1e18))
	fmt.Printf("\n💸 Funding contract...\n")
	fmt.Printf("   Target  : %s\n", target.Hex())
	fmt.Printf("   Amount  : %s ETH (%s wei)\n", ethF.Text('f', 6), amountWei.String())

	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		fmt.Printf("❌ Failed to get nonce: %v\n", err)
		return
	}
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		gasPrice = big.NewInt(1e9)
	}

	// Gas limit 50000 đủ cho fallback/receive function;
	// nếu contract không có receive/fallback thì tăng lên hoặc dùng 21000 cho EOA
	tx := types.NewTransaction(nonce, target, amountWei, uint64(50_000), gasPrice, nil)

	signedTx, err := auth.Signer(auth.From, tx)
	if err != nil {
		fmt.Printf("❌ Failed to sign: %v\n", err)
		return
	}
	if err := client.SendTransaction(context.Background(), signedTx); err != nil {
		fmt.Printf("❌ Failed to send: %v\n", err)
		return
	}

	fmt.Printf("📤 Tx: %s\n", signedTx.Hash().Hex())
	receipt, err := waitForTransaction(client, signedTx.Hash(), "FundContract")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}
	fmt.Printf("✅ Funded! Block=%d  Gas=%d  Status=%d\n",
		receipt.BlockNumber.Uint64(), receipt.GasUsed, receipt.Status)

	// Show new balance
	b, err := client.BalanceAt(context.Background(), target, nil)
	if err == nil {
		eth := new(big.Float).Quo(new(big.Float).SetInt(b), big.NewFloat(1e18))
		fmt.Printf("   📊 Balance mới của contract: %s ETH\n", eth.Text('f', 8))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// deployFromBytecode – deploy contract from byteCode/byteCode.json
//
//	name        : tên hiển thị (CrossChainGateway / contract_cc)
//	field       : JSON field name (cross_chain / contract_cc)
//	parsedABI   : ABI để pack constructor args (abi.ABI{} nếu không có)
//	arg1, arg2  : constructor args (nil nếu không có)
//
// ─────────────────────────────────────────────────────────────────────────────
func deployFromBytecode(
	client *ethclient.Client,
	auth *bind.TransactOpts,
	name, field string,
	parsedABI abi.ABI,
	arg1, arg2 *big.Int,
) (common.Address, error) {
	log.Printf("🚀 Deploying %s...", name)

	// Load bytecode JSON
	bytecodeData, err := os.ReadFile("byteCode/byteCode.json")
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to read byteCode/byteCode.json: %w", err)
	}

	var bcFile BytecodeData
	if err := json.Unmarshal(bytecodeData, &bcFile); err != nil {
		return common.Address{}, fmt.Errorf("failed to parse bytecode JSON: %w", err)
	}

	var rawHex string
	switch field {
	case "cross_chain":
		rawHex = bcFile.CrossChain
	case "contract_cc":
		rawHex = bcFile.ContractCC
	default:
		return common.Address{}, fmt.Errorf("unknown bytecode field: %s", field)
	}

	if rawHex == "" {
		return common.Address{}, fmt.Errorf("bytecode field '%s' is empty", field)
	}

	bytecode := common.FromHex(strings.TrimPrefix(rawHex, "0x"))
	if len(bytecode) == 0 {
		return common.Address{}, fmt.Errorf("bytecode for '%s' is invalid", field)
	}
	log.Printf("   Bytecode size: %d bytes", len(bytecode))

	// Pack constructor args if provided
	if arg1 != nil && arg2 != nil {
		constructorArgs, err := parsedABI.Pack("", arg1, arg2)
		if err != nil {
			return common.Address{}, fmt.Errorf("failed to pack constructor args: %w", err)
		}
		bytecode = append(bytecode, constructorArgs...)
	}

	// Nonce & gas price
	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to get nonce: %w", err)
	}
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		gasPrice = big.NewInt(1e9)
		log.Printf("⚠️  Fallback gas price 1 gwei")
	}

	tx := types.NewContractCreation(nonce, big.NewInt(0), uint64(5_000_000), gasPrice, bytecode)

	signedTx, err := auth.Signer(auth.From, tx)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to sign: %w", err)
	}
	if err := client.SendTransaction(context.Background(), signedTx); err != nil {
		return common.Address{}, fmt.Errorf("failed to send: %w", err)
	}

	log.Printf("📤 Deploy tx: %s", signedTx.Hash().Hex())

	receipt, err := waitForTransaction(client, signedTx.Hash(), name)
	if err != nil {
		return common.Address{}, fmt.Errorf("tx failed: %w", err)
	}

	log.Printf("✅ %s deployed at: %s  (gas used: %d)", name, receipt.ContractAddress.Hex(), receipt.GasUsed)
	return receipt.ContractAddress, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// channelInfoData – struct chứa dữ liệu từ getChannelInfo()
// ─────────────────────────────────────────────────────────────────────────────
type channelInfoData struct {
	SourceNationId    *big.Int
	DestNationId      *big.Int
	OutboundNonce     *big.Int
	InboundNonce      *big.Int
	InboundLastBlock  *big.Int
	ConfirmationNonce *big.Int
	ConfirmLastBlock  *big.Int
}

// getChannelInfoData – gọi getChannelInfo() và trả về struct, không in ra console
func getChannelInfoData(client *ethclient.Client, contractAddress common.Address, parsedABI abi.ABI) (*channelInfoData, error) {
	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	var results []interface{}
	if err := contract.Call(&bind.CallOpts{Context: context.Background()}, &results, "getChannelInfo"); err != nil {
		return nil, fmt.Errorf("getChannelInfo failed: %w", err)
	}
	if len(results) < 7 {
		return nil, fmt.Errorf("unexpected results count: %d (want 7)", len(results))
	}
	return &channelInfoData{
		SourceNationId:    results[0].(*big.Int),
		DestNationId:      results[1].(*big.Int),
		OutboundNonce:     results[2].(*big.Int),
		InboundNonce:      results[3].(*big.Int),
		InboundLastBlock:  results[4].(*big.Int),
		ConfirmationNonce: results[5].(*big.Int),
		ConfirmLastBlock:  results[6].(*big.Int),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// callGetChannelInfo
// ─────────────────────────────────────────────────────────────────────────────
func callGetChannelInfo(client *ethclient.Client, contractAddress common.Address, parsedABI abi.ABI) {
	fmt.Println("\n🔍 Calling getChannelInfo()...")

	info, err := getChannelInfoData(client, contractAddress, parsedABI)
	if err != nil {
		fmt.Printf("❌ Error: %v\n", err)
		return
	}

	fmt.Printf("\n📊 Channel Info:\n")
	fmt.Printf("   sourceNationId        : %s\n", info.SourceNationId.String())
	fmt.Printf("   destNationId          : %s\n", info.DestNationId.String())
	fmt.Printf("   outboundNonce         : %s\n", info.OutboundNonce.String())
	fmt.Printf("   inboundNonce          : %s\n", info.InboundNonce.String())
	fmt.Printf("   inboundLastBlock      : %s\n", info.InboundLastBlock.String())
	fmt.Printf("   confirmationNonce     : %s\n", info.ConfirmationNonce.String())
	fmt.Printf("   confirmationLastBlock : %s\n", info.ConfirmLastBlock.String())
}

// ─────────────────────────────────────────────────────────────────────────────
// callAdminSetChannelState
// ─────────────────────────────────────────────────────────────────────────────
func callAdminSetChannelState(
	client *ethclient.Client,
	auth *bind.TransactOpts,
	contractAddress common.Address,
	parsedABI abi.ABI,
	outboundNonce *big.Int,
	inboundNonce *big.Int,
	confirmationNonce *big.Int,
) {
	fmt.Printf("\n⚙️  adminSetChannelState(outbound=%s, inbound=%s, confirmation=%s)...\n", outboundNonce.String(), inboundNonce.String(), confirmationNonce.String())

	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		fmt.Printf("❌ Failed to get nonce: %v\n", err)
		return
	}

	txAuth := &bind.TransactOpts{
		From:     auth.From,
		Nonce:    big.NewInt(int64(nonce)),
		Signer:   auth.Signer,
		Value:    big.NewInt(0),
		GasLimit: 300_000,
	}

	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	tx, err := contract.Transact(txAuth, "adminSetChannelState", outboundNonce, inboundNonce, confirmationNonce)
	if err != nil {
		fmt.Printf("❌ Failed: %v\n", err)
		return
	}

	fmt.Printf("📤 Tx: %s\n", tx.Hash().Hex())
	receipt, err := waitForTransaction(client, tx.Hash(), "adminSetChannelState")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}
	fmt.Printf("✅ Confirmed! Block=%d  Status=%d\n", receipt.BlockNumber.Uint64(), receipt.Status)

	// Auto verify
	fmt.Println()
	callGetChannelInfo(client, contractAddress, parsedABI)
}

// ─────────────────────────────────────────────────────────────────────────────
// callSendMessage
// ─────────────────────────────────────────────────────────────────────────────

func callSendMessage(
	client *ethclient.Client,
	auth *bind.TransactOpts,
	contractAddress common.Address,
	parsedABI abi.ABI,
	target common.Address,
	amountWei *big.Int,
	payloadHex string,
	relatedAddresses []common.Address,
) {
	fmt.Println("\n📨 Calling sendMessage()...")
	fmt.Printf("   Contract  : %s\n", contractAddress.Hex())
	fmt.Printf("   Target    : %s\n", target.Hex())

	ethF := new(big.Float).Quo(new(big.Float).SetInt(amountWei), big.NewFloat(1e18))
	fmt.Printf("   Amount    : %s ETH (%s wei)\n", ethF.Text('f', 6), amountWei.String())
	fmt.Printf("   Payload   : %s\n", payloadHex)

	var payloadBytes []byte
	if payloadHex != "" {
		var err error
		payloadBytes, err = hex.DecodeString(strings.TrimPrefix(payloadHex, "0x"))
		if err != nil {
			fmt.Printf("❌ Invalid payload hex: %v\n", err)
			return
		}
	}

	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		fmt.Printf("❌ Failed to get nonce: %v\n", err)
		return
	}

	txAuth := &bind.TransactOpts{
		From:     auth.From,
		Nonce:    big.NewInt(int64(nonce)),
		Signer:   auth.Signer,
		Value:    amountWei,
		GasLimit: 500_000,
	}

	if relatedAddresses == nil {
		relatedAddresses = []common.Address{}
	}
	if len(relatedAddresses) > 0 {
		fmt.Printf("   Related   : %d addresses\n", len(relatedAddresses))
		for i, addr := range relatedAddresses {
			fmt.Printf("              [%d] %s\n", i+1, addr.Hex())
		}
	}

	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	tx, err := contract.Transact(txAuth, "sendMessage", target, payloadBytes, relatedAddresses)
	if err != nil {
		fmt.Printf("❌ Failed: %v\n", err)
		return
	}

	fmt.Printf("📤 Tx: %s\n", tx.Hash().Hex())
	receipt, err := waitForTransaction(client, tx.Hash(), "sendMessage")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}
	fmt.Printf("✅ Confirmed! Block=%d  Gas=%d  Status=%d\n",
		receipt.BlockNumber.Uint64(), receipt.GasUsed, receipt.Status)

	if len(receipt.Logs) > 0 {
		fmt.Printf("📜 Events: %d\n", len(receipt.Logs))
		for i, vlog := range receipt.Logs {
			if len(vlog.Topics) > 0 {
				fmt.Printf("   [%d] topic[0]=%s\n", i+1, vlog.Topics[0].Hex())
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// callGetBalances – kiểm tra ETH balance của CONTRACT_ADDRESS và ADDRESS_LOCK_BALANCE
// Dùng client.BalanceAt trực tiếp (không cần ABI)
// ─────────────────────────────────────────────────────────────────────────────
func callGetBalances(client *ethclient.Client, gatewayAddr, lockBalanceAddr common.Address) {
	fmt.Println("\n💰 getContractBalance:")

	printETHBalance := func(label string, addr common.Address) {
		if addr == (common.Address{}) {
			fmt.Printf("   %-22s: (chưa set)\n", label)
			return
		}
		b, err := client.BalanceAt(context.Background(), addr, nil)
		if err != nil {
			fmt.Printf("   %-22s: ERROR %v\n", label, err)
			return
		}
		eth := new(big.Float).Quo(new(big.Float).SetInt(b), big.NewFloat(1e18))
		fmt.Printf("   %-22s: %s\n", label, addr.Hex())
		fmt.Printf("   %-22s  %s wei\n", "", b.String())
		fmt.Printf("   %-22s  %s ETH\n", "", eth.Text('f', 8))
	}

	printETHBalance("🏛️  CONTRACT_ADDRESS", gatewayAddr)
	printETHBalance("🔐 ADDRESS_LOCK_BALANCE", lockBalanceAddr)
}

// ─────────────────────────────────────────────────────────────────────────────
// callLockAndBridge – gọi lockAndBridge(recipient) với amount ETH
// ─────────────────────────────────────────────────────────────────────────────
func callLockAndBridge(
	client *ethclient.Client,
	auth *bind.TransactOpts,
	contractAddress common.Address,
	parsedABI abi.ABI,
	recipient common.Address,
	amountWei *big.Int,
) {
	ethF := new(big.Float).Quo(new(big.Float).SetInt(amountWei), big.NewFloat(1e18))
	fmt.Printf("\n🔒 Calling lockAndBridge()...\n")
	fmt.Printf("   Gateway   : %s\n", contractAddress.Hex())
	fmt.Printf("   Recipient : %s\n", recipient.Hex())
	fmt.Printf("   Amount    : %s ETH (%s wei)\n", ethF.Text('f', 8), amountWei.String())

	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		fmt.Printf("❌ Failed to get nonce: %v\n", err)
		return
	}

	txAuth := &bind.TransactOpts{
		From:     auth.From,
		Nonce:    big.NewInt(int64(nonce)),
		Signer:   auth.Signer,
		Value:    amountWei, // payable – gửi ETH
		GasLimit: 500_000,
	}

	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	tx, err := contract.Transact(txAuth, "lockAndBridge", recipient)
	if err != nil {
		fmt.Printf("❌ Failed: %v\n", err)
		return
	}

	fmt.Printf("📤 Tx: %s\n", tx.Hash().Hex())
	receipt, err := waitForTransaction(client, tx.Hash(), "lockAndBridge")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}
	fmt.Printf("✅ lockAndBridge confirmed! Block=%d  Gas=%d  Status=%d\n",
		receipt.BlockNumber.Uint64(), receipt.GasUsed, receipt.Status)

	if len(receipt.Logs) > 0 {
		fmt.Printf("📜 Events: %d\n", len(receipt.Logs))
		for i, vlog := range receipt.Logs {
			if len(vlog.Topics) > 0 {
				fmt.Printf("   [%d] topic[0]=%s\n", i+1, vlog.Topics[0].Hex())
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// checkBalances
// ─────────────────────────────────────────────────────────────────────────────
func checkBalances(client *ethclient.Client, wallet, gateway, ccAddr, lockBalance common.Address) {
	fmt.Println("\n💰 Checking Balances...")
	printBalance := func(label, addr string) {
		b, err := client.BalanceAt(context.Background(), common.HexToAddress(addr), nil)
		if err != nil {
			fmt.Printf("   %-22s: ERROR %v\n", label, err)
			return
		}
		eth := new(big.Float).Quo(new(big.Float).SetInt(b), big.NewFloat(1e18))
		fmt.Printf("   %-22s: %s\n   %-22s  %s ETH\n", label, addr, "", eth.Text('f', 8))
	}
	printBalance("👤 Wallet", wallet.Hex())
	if gateway != (common.Address{}) {
		printBalance("🏛️  Gateway (contract)", gateway.Hex())
	}
	if lockBalance != (common.Address{}) {
		printBalance("🔐 LockBalance", lockBalance.Hex())
	}
	if ccAddr != (common.Address{}) {
		printBalance("📦 contract_cc", ccAddr.Hex())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func loadABI(paths []string) (abi.ABI, error) {
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		parsed, err := abi.JSON(strings.NewReader(string(data)))
		if err != nil {
			return abi.ABI{}, fmt.Errorf("parse ABI %s: %w", p, err)
		}
		log.Printf("✅ Loaded ABI from: %s", p)
		return parsed, nil
	}
	return abi.ABI{}, fmt.Errorf("ABI not found in: %v", paths)
}

// callContractMethod is a generic helper to send a write transaction to a contract.
// It handles nonce, signing, sending, and waiting for the receipt.
func callContractMethod(
	client *ethclient.Client,
	auth *bind.TransactOpts,
	contractAddress common.Address,
	parsedABI abi.ABI,
	method string,
	args ...interface{},
) error {
	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		return fmt.Errorf("failed to get nonce: %w", err)
	}

	txAuth := &bind.TransactOpts{
		From:     auth.From,
		Nonce:    big.NewInt(int64(nonce)),
		Signer:   auth.Signer,
		Value:    big.NewInt(0),
		GasLimit: 300_000,
	}

	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	tx, err := contract.Transact(txAuth, method, args...)
	if err != nil {
		return fmt.Errorf("transact failed: %w", err)
	}

	fmt.Printf("📤 Tx (%s): %s\n", method, tx.Hash().Hex())
	receipt, err := waitForTransaction(client, tx.Hash(), method)
	if err != nil {
		return fmt.Errorf("tx failed: %w", err)
	}
	fmt.Printf("   Block=%d  Gas=%d  Status=%d\n", receipt.BlockNumber.Uint64(), receipt.GasUsed, receipt.Status)
	return nil
}

func refreshNonce(client *ethclient.Client, auth *bind.TransactOpts) {
	n, err := client.PendingNonceAt(context.Background(), auth.From)
	if err == nil {
		auth.Nonce = big.NewInt(int64(n))
	}
}

func getDeployerAuth(client *ethclient.Client, privateKeyHex string) (*bind.TransactOpts, error) {
	privateKeyHex = strings.TrimPrefix(privateKeyHex, "0x")
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("ECDSA cast error")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chainID: %w", err)
	}
	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create transactor: %w", err)
	}
	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = big.NewInt(0)
	auth.GasLimit = uint64(30_000_000)
	auth.GasPrice = nil
	return auth, nil
}

func waitForTransaction(client *ethclient.Client, txHash common.Hash, name string) (*types.Receipt, error) {
	log.Printf("⏳ Waiting for [%s] tx to be mined...", name)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var lastStatus uint64
	retryOnRevert := 3 // Số lần retry khi nhận status=0 (có thể do receipt chưa finalize)

	for {
		receipt, err := client.TransactionReceipt(ctx, txHash)
		if err == nil {
			if receipt.Status == 1 {
				return receipt, nil
			}
			// Status=0: có thể receipt chưa finalize, retry vài lần
			lastStatus = receipt.Status
			if retryOnRevert > 0 {
				retryOnRevert--
				log.Printf("⚠️  [%s] tx %s got status=%d, retrying (%d left)...",
					name, txHash.Hex()[:14]+"…", receipt.Status, retryOnRevert)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("tx reverted (status=%d), receipt: %v", lastStatus, receipt)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting for tx")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvBigInt(key, defaultValue string) *big.Int {
	v := getEnv(key, defaultValue)
	r := new(big.Int)
	r.SetString(v, 10)
	return r
}

func saveDeploymentInfo(info map[string]interface{}) {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		log.Printf("Warning: marshal failed: %v", err)
		return
	}
	filename := fmt.Sprintf("deployment_crosschain_%s.json", time.Now().Format("20060102_150405"))
	if err := os.WriteFile(filename, data, 0644); err != nil {
		log.Printf("Warning: write failed: %v", err)
		return
	}
	log.Printf("💾 Saved: %s", filename)
}

// ─────────────────────────────────────────────────────────────────────────────
// getAuthFromPK – tạo TransactOpts từ private key hex
// ─────────────────────────────────────────────────────────────────────────────
func getAuthFromPK(client *ethclient.Client, pkHex string) (*bind.TransactOpts, error) {
	pkHex = strings.TrimPrefix(pkHex, "0x")
	privateKey, err := crypto.HexToECDSA(pkHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("ECDSA cast error")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chainID: %w", err)
	}
	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create transactor: %w", err)
	}
	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = big.NewInt(0)
	auth.GasLimit = uint64(30_000_000)
	auth.GasPrice = nil
	return auth, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// spamTest – spam lockAndBridge hoặc sendMessage dùng CLIENT_PK wallets
//   - Hỗ trợ multi-channel: load-balance wallets trên nhiều gateway contracts
//   - Lắng nghe OutboundResult event qua WS để thống kê success/fail
//
// ─────────────────────────────────────────────────────────────────────────────
func spamTest(client *ethclient.Client, config *Config, scanner *bufio.Scanner, gatewayABI abi.ABI) {
	if len(config.ClientPKs) == 0 {
		fmt.Println("❌ CLIENT_PK chưa được set trong .env.crosschain")
		fmt.Println("   Cần ít nhất 1 private key, cách nhau bằng dấu phẩy.")
		return
	}

	if len(config.GatewayAddresses) == 0 {
		fmt.Println("❌ Chưa có gateway address (set CONTRACT_ADDRESS trong .env.crosschain).")
		return
	}

	// ── Chọn số kênh (channels) ──
	fmt.Printf("\n🔗 Có %d kênh (channels) khả dụng:\n", len(config.GatewayAddresses))
	for i, gw := range config.GatewayAddresses {
		fmt.Printf("   [%d] %s\n", i+1, gw.Hex())
	}
	fmt.Printf("Dùng bao nhiêu kênh? (Enter = 1, dùng kênh đầu tiên): ")
	if !scanner.Scan() {
		return
	}
	channelCountStr := strings.TrimSpace(scanner.Text())
	channelCount := 1
	if channelCountStr != "" {
		cc, ok := new(big.Int).SetString(channelCountStr, 10)
		if !ok || cc.Int64() <= 0 {
			fmt.Println("❌ Số kênh không hợp lệ")
			return
		}
		channelCount = int(cc.Int64())
		if channelCount > len(config.GatewayAddresses) {
			channelCount = len(config.GatewayAddresses)
		}
	}
	selectedGateways := config.GatewayAddresses[:channelCount]
	fmt.Printf("   ✅ Sử dụng %d kênh\n", channelCount)
	for i, gw := range selectedGateways {
		fmt.Printf("      [%d] %s\n", i+1, gw.Hex())
	}

	// Tạo auth cho từng CLIENT_PK
	type walletInfo struct {
		Auth    *bind.TransactOpts
		Address common.Address
		PKIndex int
	}
	var wallets []walletInfo
	for i, pk := range config.ClientPKs {
		auth, err := getAuthFromPK(client, pk)
		if err != nil {
			fmt.Printf("❌ CLIENT_PK[%d] invalid: %v\n", i+1, err)
			continue
		}
		wallets = append(wallets, walletInfo{Auth: auth, Address: auth.From, PKIndex: i + 1})
		fmt.Printf("   ✅ Wallet[%d]: %s\n", i+1, auth.From.Hex())
	}

	if len(wallets) == 0 {
		fmt.Println("❌ Không có wallet nào hợp lệ.")
		return
	}

	// Chọn số lượng wallets
	fmt.Printf("\n📊 Có %d wallets khả dụng. Dùng bao nhiêu? (Enter = %d): ", len(wallets), len(wallets))
	if !scanner.Scan() {
		return
	}
	walletCountStr := strings.TrimSpace(scanner.Text())
	if walletCountStr != "" {
		wc, ok := new(big.Int).SetString(walletCountStr, 10)
		if !ok || wc.Int64() <= 0 {
			fmt.Println("❌ Số lượng không hợp lệ")
			return
		}
		walletCount := int(wc.Int64())
		if walletCount > len(wallets) {
			walletCount = len(wallets)
		}
		wallets = wallets[:walletCount]
		fmt.Printf("   ✅ Sử dụng %d/%d wallets\n", walletCount, len(config.ClientPKs))
	}

	// Chọn kiểu spam
	fmt.Println("\n🔀 Chọn loại giao dịch để spam:")
	fmt.Println("   1. lockAndBridge  (gửi ETH qua bridge, cần ADDRESS_LOCK_BALANCE)")
	fmt.Println("   2. sendMessage    (gọi contract message, cần PAYLOAD_CC)")
	fmt.Print("Nhập lựa chọn (1 hoặc 2): ")
	if !scanner.Scan() {
		return
	}
	spamType := strings.TrimSpace(scanner.Text())
	if spamType != "1" && spamType != "2" {
		fmt.Println("❌ Chỉ nhận 1 hoặc 2.")
		return
	}

	// Số lần spam
	fmt.Print("Nhập số lần spam mỗi wallet (Enter = 5): ")
	if !scanner.Scan() {
		return
	}
	countStr := strings.TrimSpace(scanner.Text())
	if countStr == "" {
		countStr = "5"
	}
	count, ok := new(big.Int).SetString(countStr, 10)
	if !ok || count.Int64() <= 0 {
		fmt.Println("❌ Số lần không hợp lệ")
		return
	}
	spamCount := int(count.Int64())

	// Amount
	fmt.Print("Nhập amount ETH mỗi tx (Enter = 0.01): ")
	if !scanner.Scan() {
		return
	}
	amtStr := strings.TrimSpace(scanner.Text())
	if amtStr == "" {
		amtStr = "0.01"
	}
	amtETH, ok := new(big.Float).SetString(amtStr)
	if !ok {
		fmt.Println("❌ Amount không hợp lệ")
		return
	}
	weiMul := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	amtWei, _ := new(big.Float).Mul(amtETH, weiMul).Int(nil)

	// Xác nhận
	typeName := "lockAndBridge"
	if spamType == "2" {
		typeName = "sendMessage"
	}
	fmt.Printf("\n🚀 Spam config:\n")
	fmt.Printf("   Type     : %s\n", typeName)
	fmt.Printf("   Channels : %d\n", channelCount)
	for i, gw := range selectedGateways {
		fmt.Printf("      [%d] %s\n", i+1, gw.Hex())
	}
	fmt.Printf("   Wallets  : %d (chạy song song, load-balance trên %d kênh)\n", len(wallets), channelCount)
	fmt.Printf("   Count    : %d tx/wallet\n", spamCount)
	fmt.Printf("   Amount   : %s ETH/tx\n", amtStr)
	fmt.Printf("   Total tx : %d\n", spamCount*len(wallets))
	if config.WSUrl != "" {
		fmt.Printf("   📡 WS     : %s\n", config.WSUrl)
		fmt.Printf("   📡 Lắng nghe OutboundResult:\n")
		fmt.Printf("      Local  : %d gateways (CONTRACT_ADDRESS)\n", channelCount)
		remoteCount := len(config.RemoteGateways)
		if remoteCount > channelCount {
			remoteCount = channelCount
		}
		fmt.Printf("      Remote : %d gateways (SMC_REMOTE)\n", remoteCount)
	} else {
		fmt.Println("   ⚠️  WS_URL chưa set → không lắng nghe OutboundResult")
	}
	fmt.Print("\nBắt đầu spam? (y/N): ")
	if !scanner.Scan() {
		return
	}
	if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		fmt.Println("❌ Cancelled.")
		return
	}

	// ── Lưu channel info TRƯỚC khi spam ──
	type nonceSnapshot struct {
		OutboundNonce     *big.Int
		ConfirmationNonce *big.Int
	}
	preSpamNonces := make(map[common.Address]*nonceSnapshot)

	fmt.Println("\n📋 Lấy channel info TRƯỚC khi spam...")
	// Local gateways
	for _, gw := range selectedGateways {
		info, err := getChannelInfoData(client, gw, gatewayABI)
		if err != nil {
			fmt.Printf("   ⚠️  [Local] %s: %v\n", gw.Hex()[:12]+"…", err)
		} else {
			preSpamNonces[gw] = &nonceSnapshot{
				OutboundNonce:     new(big.Int).Set(info.OutboundNonce),
				ConfirmationNonce: new(big.Int).Set(info.ConfirmationNonce),
			}
			fmt.Printf("   ✅ [Local] %s: outbound=%s, confirm=%s\n",
				gw.Hex()[:12]+"…", info.OutboundNonce.String(), info.ConfirmationNonce.String())
		}
	}
	// Remote gateways
	selectedRemotesForNonce := config.RemoteGateways
	if len(selectedRemotesForNonce) > channelCount {
		selectedRemotesForNonce = selectedRemotesForNonce[:channelCount]
	}
	for _, rm := range selectedRemotesForNonce {
		remoteCliForInfo := globalRemoteClient
		if remoteCliForInfo == nil {
			remoteCliForInfo = client
		}
		info, err := getChannelInfoData(remoteCliForInfo, rm, gatewayABI)
		if err != nil {
			fmt.Printf("   ⚠️  [Remote] %s: %v\n", rm.Hex()[:12]+"…", err)
		} else {
			preSpamNonces[rm] = &nonceSnapshot{
				OutboundNonce:     new(big.Int).Set(info.OutboundNonce),
				ConfirmationNonce: new(big.Int).Set(info.ConfirmationNonce),
			}
			fmt.Printf("   ✅ [Remote] %s: outbound=%s, confirm=%s\n",
				rm.Hex()[:12]+"…", info.OutboundNonce.String(), info.ConfirmationNonce.String())
		}
	}

	// ── Bắt đầu spam ──
	fmt.Printf("\n🔥 Bắt đầu spam %s — %d wallet song song trên %d kênh ...\n\n", typeName, len(wallets), channelCount)
	var successCount int64
	var failCount int64

	// Per-contract counters
	type contractStats struct {
		success int64
		fail    int64
	}
	perContractStats := make(map[common.Address]*contractStats)
	var perContractMu sync.Mutex
	for _, gw := range selectedGateways {
		perContractStats[gw] = &contractStats{}
	}

	// ── Event listener counters (từ OutboundResult trên source chain) ──
	var evtSuccessCount int64
	var evtFailCount int64
	var evtTotalCount int64
	// ── Event file logger (ghi trực tiếp vào file để so sánh với observer log) ──
	evtFileName := fmt.Sprintf("spam_events_%s.log", time.Now().Format("20060102_150405"))
	evtFile, err := os.Create(evtFileName)
	var evtFileLogger *log.Logger
	if err != nil {
		log.Printf("⚠️  Cannot create events log file: %v", err)
	} else {
		defer evtFile.Close()
		evtFileLogger = log.New(evtFile, "", 0)
		evtFileLogger.Printf("# Spam Events — %s", time.Now().Format(time.RFC3339))
		evtFileLogger.Printf("# Format: nonce | contract | status | msgId | sender | block")
		log.Printf("💾 Events will be logged to: %s", evtFileName)
	}

	// ── Timestamps để tính TPS riêng biệt ──
	var firstEventTimeNano int64 // UnixNano của event đầu tiên (atomic)
	var lastEventTimeNano int64  // UnixNano của event cuối cùng (atomic)

	// ── Khởi động WebSocket event listener cho mỗi kênh + remote ──
	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	defer listenerCancel()

	var listenerWg sync.WaitGroup
	if config.WSUrl != "" {
		// Lắng nghe OutboundResult trên local gateways (CONTRACT_ADDRESS)
		for chIdx, gwAddr := range selectedGateways {
			listenerWg.Add(1)
			go func(chIdx int, gwAddr common.Address) {
				defer listenerWg.Done()
				log.Printf("📡 [Local Ch %d] Lắng nghe OutboundResult: %s", chIdx+1, gwAddr.Hex())
				spamEventListener(
					listenerCtx, config.WSUrl, gwAddr, gatewayABI,
					&evtSuccessCount, &evtFailCount, &evtTotalCount,
					&firstEventTimeNano, &lastEventTimeNano,
					evtFileLogger,
				)
			}(chIdx, gwAddr)
		}

		// Lắng nghe OutboundResult trên remote gateways (SMC_REMOTE)
		selectedRemotes := config.RemoteGateways
		if len(selectedRemotes) > channelCount {
			selectedRemotes = selectedRemotes[:channelCount]
		}
		for rmIdx, rmAddr := range selectedRemotes {
			listenerWg.Add(1)
			go func(rmIdx int, rmAddr common.Address) {
				defer listenerWg.Done()
				log.Printf("📡 [Remote Ch %d] Lắng nghe OutboundResult: %s", rmIdx+1, rmAddr.Hex())
				spamEventListener(
					listenerCtx, config.WSUrl, rmAddr, gatewayABI,
					&evtSuccessCount, &evtFailCount, &evtTotalCount,
					&firstEventTimeNano, &lastEventTimeNano,
					evtFileLogger,
				)
			}(rmIdx, rmAddr)
		}
	}

	startTime := time.Now()

	var wg sync.WaitGroup
	var failErrors []string
	var failErrorsMu sync.Mutex

	for walletIdx, w := range wallets {
		// Load-balance: wallet i → channel (i % channelCount)
		assignedGateway := selectedGateways[walletIdx%channelCount]
		wg.Add(1)
		go func(w walletInfo, gwAddr common.Address, chIdx int) {
			defer wg.Done()
			for round := 1; round <= spamCount; round++ {
				log.Printf("[Wallet %d → Ch %d] Round %d/%d │ %s → %s",
					w.PKIndex, chIdx+1, round, spamCount, w.Address.Hex(), gwAddr.Hex())

				refreshNonce(client, w.Auth)

				var err error
				switch spamType {
				case "1": // lockAndBridge
					recipient := config.AddressLockBalance
					if recipient == (common.Address{}) {
						recipient = w.Address
					}
					err = callLockAndBridgeWithErr(client, w.Auth, gwAddr, gatewayABI, recipient, amtWei)

				case "2": // sendMessage
					targetAddr := globalCCAddress
					if targetAddr == (common.Address{}) {
						targetAddr = gwAddr
					}
					err = callSendMessageWithErr(client, w.Auth, gwAddr, gatewayABI, targetAddr, amtWei, config.PayloadCC)
				}

				if err != nil {
					errMsg := fmt.Sprintf("[Wallet %d → Ch %d] Round %d: %v", w.PKIndex, chIdx+1, round, err)
					log.Printf("[Wallet %d → Ch %d] ❌ Round %d failed: %v", w.PKIndex, chIdx+1, round, err)
					failErrorsMu.Lock()
					failErrors = append(failErrors, errMsg)
					failErrorsMu.Unlock()
					atomic.AddInt64(&failCount, 1)
					perContractMu.Lock()
					if cs, ok := perContractStats[gwAddr]; ok {
						atomic.AddInt64(&cs.fail, 1)
					}
					perContractMu.Unlock()
				} else {
					log.Printf("[Wallet %d → Ch %d] ✅ Round %d done", w.PKIndex, chIdx+1, round)
					atomic.AddInt64(&successCount, 1)
					perContractMu.Lock()
					if cs, ok := perContractStats[gwAddr]; ok {
						atomic.AddInt64(&cs.success, 1)
					}
					perContractMu.Unlock()
				}
			}
			log.Printf("[Wallet %d → Ch %d] 🏁 Hoàn thành %d rounds", w.PKIndex, chIdx+1, spamCount)
		}(w, assignedGateway, walletIdx%channelCount)
	}

	wg.Wait()
	sendEndTime := time.Now() // Thời điểm gửi xong tất cả tx

	// Spam gửi xong, chờ cho event listener nhận nốt OutboundResult events
	if config.WSUrl != "" {
		totalSent := atomic.LoadInt64(&successCount) // Chỉ đếm tx thành công, tx lỗi không tạo event
		fmt.Printf("\n⏳ Chờ tối đa 10 phút để nhận đủ %d OutboundResult events (nhấn Enter để hoàn tất sớm)...\n", totalSent)
		waitStart := time.Now()
		maxWait := 10 * time.Minute

		// Goroutine lắng nghe Enter từ stdin
		enterCh := make(chan struct{}, 1)
		go func() {
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			enterCh <- struct{}{}
		}()

		for {
			received := atomic.LoadInt64(&evtTotalCount)
			if received >= totalSent {
				fmt.Printf("   ✅ Nhận đủ %d/%d events!\n", received, totalSent)
				break
			}
			if time.Since(waitStart) >= maxWait {
				fmt.Printf("   ⚠️  Timeout 10 phút! Nhận được %d/%d events\n", received, totalSent)
				break
			}
			// Kiểm tra Enter
			select {
			case <-enterCh:
				fmt.Printf("   ⏹️  Enter pressed! Hoàn tất với %d/%d events (dùng last event time để đo TPS)\n", received, totalSent)
				goto doneWaiting
			default:
			}
			fmt.Printf("   📡 Đang chờ: %d/%d events (elapsed: %s) [Enter=hoàn tất]\r",
				received, totalSent, time.Since(waitStart).Round(time.Second))
			time.Sleep(2 * time.Second)
		}
	doneWaiting:
	}

	// Dừng listener
	listenerCancel()
	listenerWg.Wait()

	totalElapsed := time.Since(startTime)
	sendElapsed := sendEndTime.Sub(startTime)
	totalSuccess := atomic.LoadInt64(&successCount)
	totalFail := atomic.LoadInt64(&failCount)
	totalSentAll := totalSuccess + totalFail
	sendTPS := float64(0)
	if sendElapsed.Seconds() > 0 {
		sendTPS = float64(totalSentAll) / sendElapsed.Seconds()
	}

	fmt.Printf("\n════════════════════════════════════════════\n")
	fmt.Printf("🏁 Spam hoàn thành!\n")
	fmt.Printf("──── 📤 Gửi (Send-side) ────\n")
	fmt.Printf("   ✅ Success : %d\n", totalSuccess)
	fmt.Printf("   ❌ Failed  : %d\n", totalFail)
	fmt.Printf("   📊 Send TPS: %.2f tx/s\n", sendTPS)
	fmt.Printf("   ⏱  Send time: %s\n", sendElapsed.Round(time.Millisecond))
	if len(selectedGateways) > 1 {
		fmt.Printf("   ── Per-contract breakdown ──\n")
		for i, gw := range selectedGateways {
			if cs, ok := perContractStats[gw]; ok {
				s := atomic.LoadInt64(&cs.success)
				f := atomic.LoadInt64(&cs.fail)
				fmt.Printf("   [Ch %d] %s → ✅ %d  ❌ %d\n", i+1, gw.Hex()[:12]+"…", s, f)
			}
		}
	}
	if config.WSUrl != "" {
		evtTotal := atomic.LoadInt64(&evtTotalCount)
		evtSuccess := atomic.LoadInt64(&evtSuccessCount)
		evtFail := atomic.LoadInt64(&evtFailCount)

		// Tính Confirm TPS từ khoảng thời gian giữa event đầu tiên và event cuối cùng
		firstNano := atomic.LoadInt64(&firstEventTimeNano)
		lastNano := atomic.LoadInt64(&lastEventTimeNano)
		confirmTPS := float64(0)
		var confirmDuration time.Duration
		if firstNano > 0 && lastNano > firstNano && evtTotal > 1 {
			// Nhiều events: đo từ event đầu → event cuối
			confirmDuration = time.Duration(lastNano - firstNano)
			confirmTPS = float64(evtTotal) / confirmDuration.Seconds()
		} else if evtTotal >= 1 && firstNano > 0 {
			// 1 event: đo từ startTime → event đó
			firstEventTime := time.Unix(0, firstNano)
			confirmDuration = firstEventTime.Sub(startTime)
			if confirmDuration.Seconds() > 0 {
				confirmTPS = float64(evtTotal) / confirmDuration.Seconds()
			}
		}

		// Tính latency trung bình: thời gian từ gửi xong → nhận event cuối
		var avgLatency time.Duration
		if lastNano > 0 {
			lastEventTime := time.Unix(0, lastNano)
			avgLatency = lastEventTime.Sub(sendEndTime)
		}

		fmt.Printf("──── 📡 Kết quả (OutboundResult events) ────\n")
		fmt.Printf("   📨 Total events : %d\n", evtTotal)
		fmt.Printf("   ✅ Confirmed    : %d (isSuccess=true)\n", evtSuccess)
		fmt.Printf("   ❌ Refunded     : %d (isSuccess=false)\n", evtFail)
		fmt.Printf("   📊 Confirm TPS  : %.2f tx/s\n", confirmTPS)
		if confirmDuration > 0 {
			fmt.Printf("   ⏱  Confirm time : %s (first→last event)\n", confirmDuration.Round(time.Millisecond))
		}
		if avgLatency > 0 {
			fmt.Printf("   ⏱  Cross-chain  : %s (send done→last confirm)\n", avgLatency.Round(time.Millisecond))
		}
		if totalSentAll > 0 {
			completionRate := float64(evtTotal) / float64(totalSentAll) * 100
			fmt.Printf("   📊 Completion   : %.1f%% (%d/%d)\n", completionRate, evtTotal, totalSentAll)
		}
	}
	fmt.Printf("   ⏱  Total elapsed: %s\n", totalElapsed.Round(time.Millisecond))

	// In chi tiết lỗi nếu có
	if len(failErrors) > 0 {
		fmt.Printf("──── ❌ Chi tiết lỗi (%d) ────\n", len(failErrors))
		for i, errMsg := range failErrors {
			fmt.Printf("   %d. %s\n", i+1, errMsg)
		}
		// Lưu ra file
		errFileName := fmt.Sprintf("spam_errors_%s.log", time.Now().Format("20060102_150405"))
		errContent := fmt.Sprintf("Spam Test Errors — %s\nType: %s | Wallets: %d | Count: %d/wallet\nTotal: %d sent, %d failed\nElapsed: %s\n\n",
			time.Now().Format(time.RFC3339), typeName, len(wallets), spamCount,
			totalSentAll, totalFail, totalElapsed.Round(time.Millisecond))
		for i, errMsg := range failErrors {
			errContent += fmt.Sprintf("%d. %s\n", i+1, errMsg)
		}
		if err := os.WriteFile(errFileName, []byte(errContent), 0644); err == nil {
			fmt.Printf("   💾 Lỗi đã lưu vào: %s\n", errFileName)
		}
	}
	// ── So sánh channel info SAU spam ──
	if len(preSpamNonces) > 0 {
		fmt.Printf("──── 📊 Channel Nonce Delta (trước → sau spam) ────\n")
		// Local gateways
		for i, gw := range selectedGateways {
			pre, hasPre := preSpamNonces[gw]
			postInfo, err := getChannelInfoData(client, gw, gatewayABI)
			if err != nil {
				fmt.Printf("   [Local Ch %d] %s: ❌ lỗi lấy info: %v\n", i+1, gw.Hex()[:12]+"…", err)
				continue
			}
			if hasPre {
				outDelta := new(big.Int).Sub(postInfo.OutboundNonce, pre.OutboundNonce)
				confDelta := new(big.Int).Sub(postInfo.ConfirmationNonce, pre.ConfirmationNonce)
				fmt.Printf("   [Local Ch %d] %s\n", i+1, gw.Hex()[:12]+"…")
				fmt.Printf("      outboundNonce     : %s → %s  (Δ +%s)\n", pre.OutboundNonce.String(), postInfo.OutboundNonce.String(), outDelta.String())
				fmt.Printf("      confirmationNonce : %s → %s  (Δ +%s)\n", pre.ConfirmationNonce.String(), postInfo.ConfirmationNonce.String(), confDelta.String())
				if outDelta.Cmp(confDelta) == 0 {
					fmt.Printf("      ✅ Tất cả %s giao dịch đã được confirm!\n", outDelta.String())
				} else {
					pending := new(big.Int).Sub(outDelta, confDelta)
					fmt.Printf("      ⚠️  Còn %s giao dịch chưa confirm (outΔ=%s, confΔ=%s)\n", pending.String(), outDelta.String(), confDelta.String())
				}
			} else {
				fmt.Printf("   [Local Ch %d] %s: outbound=%s, confirm=%s (không có dữ liệu trước)\n",
					i+1, gw.Hex()[:12]+"…", postInfo.OutboundNonce.String(), postInfo.ConfirmationNonce.String())
			}
		}
		// Remote gateways
		for i, rm := range selectedRemotesForNonce {
			pre, hasPre := preSpamNonces[rm]
			remoteCliForInfo := globalRemoteClient
			if remoteCliForInfo == nil {
				remoteCliForInfo = client
			}
			postInfo, err := getChannelInfoData(remoteCliForInfo, rm, gatewayABI)
			if err != nil {
				fmt.Printf("   [Remote Ch %d] %s: ❌ lỗi lấy info: %v\n", i+1, rm.Hex()[:12]+"…", err)
				continue
			}
			if hasPre {
				outDelta := new(big.Int).Sub(postInfo.OutboundNonce, pre.OutboundNonce)
				confDelta := new(big.Int).Sub(postInfo.ConfirmationNonce, pre.ConfirmationNonce)
				fmt.Printf("   [Remote Ch %d] %s\n", i+1, rm.Hex()[:12]+"…")
				fmt.Printf("      outboundNonce     : %s → %s  (Δ +%s)\n", pre.OutboundNonce.String(), postInfo.OutboundNonce.String(), outDelta.String())
				fmt.Printf("      confirmationNonce : %s → %s  (Δ +%s)\n", pre.ConfirmationNonce.String(), postInfo.ConfirmationNonce.String(), confDelta.String())
				if outDelta.Cmp(confDelta) == 0 {
					fmt.Printf("      ✅ Tất cả %s giao dịch đã được confirm!\n", outDelta.String())
				} else {
					pending := new(big.Int).Sub(outDelta, confDelta)
					fmt.Printf("      ⚠️  Còn %s giao dịch chưa confirm (outΔ=%s, confΔ=%s)\n", pending.String(), outDelta.String(), confDelta.String())
				}
			} else {
				fmt.Printf("   [Remote Ch %d] %s: outbound=%s, confirm=%s (không có dữ liệu trước)\n",
					i+1, rm.Hex()[:12]+"…", postInfo.OutboundNonce.String(), postInfo.ConfirmationNonce.String())
			}
		}
	}

	fmt.Printf("════════════════════════════════════════════\n")
	if evtFileLogger != nil {
		fmt.Printf("💾 Events log saved: %s\n", evtFileName)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// spamEventListener – kết nối WS đến source chain, subscribe OutboundResult,
//
//	ping 30s để giữ kết nối, thống kê confirmed/refunded
//
// OutboundResult(bytes32 indexed messageId, uint256 indexed nonce,
//
//	MessageType msgType, address indexed sender,
//	bool isSuccess, uint256 amount)
//
// ─────────────────────────────────────────────────────────────────────────────
func spamEventListener(
	ctx context.Context,
	wsURL string,
	gatewayAddr common.Address,
	gatewayABI abi.ABI,
	evtSuccess, evtFail, evtTotal *int64,
	firstEventTimeNano, lastEventTimeNano *int64,
	evtFileLogger *log.Logger,
) {
	log.Printf("📡 [EventListener] Kết nối WS: %s", wsURL)
	log.Printf("📡 [EventListener] Lắng nghe OutboundResult trên: %s", gatewayAddr.Hex())

	// Kết nối WS
	parsedURL, err := url.Parse(wsURL)
	if err != nil {
		log.Printf("📡 [EventListener] ❌ Parse WS URL failed: %v", err)
		return
	}

	var wsRPCClient *rpc.Client
	switch parsedURL.Scheme {
	case "wss":
		dialer := *websocket.DefaultDialer
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		wsRPCClient, err = rpc.DialWebsocketWithDialer(ctx, wsURL, "", dialer)
	default: // ws
		wsRPCClient, err = rpc.DialContext(ctx, wsURL)
	}
	if err != nil {
		log.Printf("📡 [EventListener] ❌ Kết nối WS failed: %v", err)
		return
	}
	wsClient := ethclient.NewClient(wsRPCClient)
	defer wsClient.Close()
	log.Printf("📡 [EventListener] ✅ Kết nối WS thành công!")

	// Lấy event signature cho OutboundResult
	outboundResultEvent, exists := gatewayABI.Events["OutboundResult"]
	if !exists {
		log.Printf("📡 [EventListener] ❌ ABI không có event OutboundResult")
		return
	}

	// Subscribe to logs
	query := ethereum.FilterQuery{
		Addresses: []common.Address{gatewayAddr},
		Topics:    [][]common.Hash{{outboundResultEvent.ID}},
	}

	logsCh := make(chan types.Log, 100)
	sub, err := wsClient.SubscribeFilterLogs(ctx, query, logsCh)
	if err != nil {
		log.Printf("📡 [EventListener] ❌ Subscribe failed: %v", err)
		return
	}
	defer sub.Unsubscribe()
	log.Printf("📡 [EventListener] ✅ Subscribed! Event topic: %s", outboundResultEvent.ID.Hex())

	// Ping ticker 30s
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("📡 [EventListener] 🛑 Stopped (context cancelled)")
			return

		case err := <-sub.Err():
			log.Printf("📡 [EventListener] ❌ Subscription error: %v", err)
			return

		case vLog := <-logsCh:
			// Parse non-indexed event data: msgType (uint8), isSuccess (bool), amount (uint256)
			eventData := make(map[string]interface{})
			if err := gatewayABI.UnpackIntoMap(eventData, "OutboundResult", vLog.Data); err != nil {
				log.Printf("📡 [EventListener] ⚠️  Unpack event failed: %v", err)
				continue
			}

			// Indexed topics: [0]=eventSig, [1]=messageId, [2]=nonce, [3]=sender
			var messageId common.Hash
			if len(vLog.Topics) > 1 {
				messageId = vLog.Topics[1]
			}

			var eventNonce *big.Int
			if len(vLog.Topics) > 2 {
				eventNonce = new(big.Int).SetBytes(vLog.Topics[2].Bytes())
			}

			var senderAddr common.Address
			if len(vLog.Topics) > 3 {
				senderAddr = common.HexToAddress(vLog.Topics[3].Hex())
			}

			// Parse isSuccess (bool)
			isSuccess := false
			if val, ok := eventData["isSuccess"]; ok {
				if b, ok := val.(bool); ok {
					isSuccess = b
				}
			}

			atomic.AddInt64(evtTotal, 1)

			// Ghi timestamp event đầu tiên và cuối cùng
			nowNano := time.Now().UnixNano()
			atomic.CompareAndSwapInt64(firstEventTimeNano, 0, nowNano) // Chỉ set lần đầu
			atomic.StoreInt64(lastEventTimeNano, nowNano)              // Luôn cập nhật

			nonceTxt := "?"
			if eventNonce != nil {
				nonceTxt = eventNonce.String()
			}

			contractShort := vLog.Address.Hex()[:10] + "…"

			if isSuccess {
				atomic.AddInt64(evtSuccess, 1)
				log.Printf("📡 [Event] ✅ CONFIRMED │ contract=%s msgId=%s nonce=%s sender=%s block=%d",
					contractShort, messageId.Hex()[:18]+"…", nonceTxt, senderAddr.Hex()[:10]+"…", vLog.BlockNumber)
			} else {
				atomic.AddInt64(evtFail, 1)
				log.Printf("📡 [Event] ❌ REFUNDED  │ contract=%s msgId=%s nonce=%s sender=%s block=%d",
					contractShort, messageId.Hex()[:18]+"…", nonceTxt, senderAddr.Hex()[:10]+"…", vLog.BlockNumber)
			}

			// Ghi vào file log
			if evtFileLogger != nil {
				status := "CONFIRMED"
				if !isSuccess {
					status = "REFUNDED"
				}
				evtFileLogger.Printf("%s | %s | %s | %s | %s | %d",
					nonceTxt, vLog.Address.Hex(), status, messageId.Hex(), senderAddr.Hex(), vLog.BlockNumber)
			}

		case <-pingTicker.C:
			// Ping: đọc block number mới nhất để giữ kết nối WS
			blockNum, err := wsClient.BlockNumber(ctx)
			if err != nil {
				log.Printf("📡 [EventListener] ⚠️  Ping failed: %v", err)
			} else {
				log.Printf("📡 [EventListener] 🏓 Ping OK │ latest block=%d │ events: total=%d ✅=%d ❌=%d",
					blockNum,
					atomic.LoadInt64(evtTotal),
					atomic.LoadInt64(evtSuccess),
					atomic.LoadInt64(evtFail))
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// callCheckApprovalStatus – kiểm tra trạng thái approval của một messageId
// Gọi getRelayerList, signatureThreshold, approvalCount, hasApproved cho từng relayer
// ─────────────────────────────────────────────────────────────────────────────
func callCheckApprovalStatus(client *ethclient.Client, contractAddress common.Address, parsedABI abi.ABI, messageId [32]byte) {
	fmt.Printf("\n🔍 Checking approval status for messageId: 0x%x\n", messageId)
	fmt.Println("─────────────────────────────────────────────────────")

	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	ctx := context.Background()

	// 1. Gọi getRelayerList() → address[]
	var relayerResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &relayerResult, "getRelayerList"); err != nil {
		fmt.Printf("❌ getRelayerList() failed: %v\n", err)
		return
	}
	relayers, ok := relayerResult[0].([]common.Address)
	if !ok {
		fmt.Println("❌ Cannot parse relayer list")
		return
	}
	fmt.Printf("👥 Total relayers: %d\n", len(relayers))

	// 2. Gọi getThreshold() → uint256 (dynamic: ceil(relayerList.length * 2 / 3))
	var thresholdResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &thresholdResult, "getThreshold"); err != nil {
		fmt.Printf("❌ getThreshold() failed: %v\n", err)
		return
	}
	threshold, _ := thresholdResult[0].(*big.Int)
	fmt.Printf("🔐 Threshold (dynamic 2/3): %s/%d\n", threshold.String(), len(relayers))

	// 3. Gọi approvalCount(messageId) → uint256
	var countResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &countResult, "approvalCount", messageId); err != nil {
		fmt.Printf("❌ approvalCount() failed: %v\n", err)
		return
	}
	count, _ := countResult[0].(*big.Int)
	fmt.Printf("📊 Approval count: %s / %s\n", count.String(), threshold.String())

	// 4. Gọi messageExecuted(messageId) → bool
	var executedResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &executedResult, "messageExecuted", messageId); err != nil {
		fmt.Printf("⚠️  messageExecuted() failed: %v\n", err)
	} else {
		executed, _ := executedResult[0].(bool)
		if executed {
			fmt.Printf("✅ Message đã được executed!\n")
		} else {
			fmt.Printf("⏳ Message chưa được executed\n")
		}
	}

	// 5. Duyệt từng relayer, gọi hasApproved(messageId, relayer) → bool
	fmt.Println("\n─────────────────────────────────────────────────────")
	fmt.Println("📋 Chi tiết approval từng relayer:")
	fmt.Println("─────────────────────────────────────────────────────")

	approvedCount := 0
	for i, relayer := range relayers {
		var approvedResult []interface{}
		if err := contract.Call(&bind.CallOpts{Context: ctx}, &approvedResult, "hasApproved", messageId, relayer); err != nil {
			fmt.Printf("   [%d] %s → ❌ ERROR: %v\n", i+1, relayer.Hex(), err)
			continue
		}
		approved, _ := approvedResult[0].(bool)
		if approved {
			approvedCount++
			fmt.Printf("   [%d] %s → ✅ ĐÃ SUBMIT\n", i+1, relayer.Hex())
		} else {
			fmt.Printf("   [%d] %s → ⏳ CHƯA SUBMIT\n", i+1, relayer.Hex())
		}
	}

	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("📊 Tổng kết: %d/%d relayer đã submit (threshold: %s)\n", approvedCount, len(relayers), threshold.String())
	if count.Cmp(threshold) >= 0 {
		fmt.Println("✅ ĐÃ ĐỦ THRESHOLD!")
	} else {
		remaining := new(big.Int).Sub(threshold, count)
		fmt.Printf("⏳ Còn thiếu %s approval nữa\n", remaining.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// callCheckConfirmationApprovalStatus – kiểm tra trạng thái approval của processConfirmation
// approvalId = keccak256(abi.encode("CONFIRM", messageId, eventNonce, isSuccess))
// ─────────────────────────────────────────────────────────────────────────────
func callCheckConfirmationApprovalStatus(
	client *ethclient.Client,
	contractAddress common.Address,
	parsedABI abi.ABI,
	messageId [32]byte,
	eventNonce *big.Int,
	isSuccess bool,
) {
	fmt.Printf("\n🔍 Checking processConfirmation approval status\n")
	fmt.Printf("   messageId  : 0x%x\n", messageId)
	fmt.Printf("   eventNonce : %s\n", eventNonce.String())
	fmt.Printf("   isSuccess  : %v\n", isSuccess)
	fmt.Println("─────────────────────────────────────────────────────")

	// Compute approvalId = keccak256(abi.encode("CONFIRM", messageId, eventNonce, isSuccess))
	// Solidity: abi.encode(string, bytes32, uint256, bool)
	abiArgs := abi.Arguments{
		{Type: mustABIType("string")},
		{Type: mustABIType("bytes32")},
		{Type: mustABIType("uint256")},
		{Type: mustABIType("bool")},
	}
	packed, err := abiArgs.Pack("CONFIRM", messageId, eventNonce, isSuccess)
	if err != nil {
		fmt.Printf("❌ Failed to abi.encode approvalId: %v\n", err)
		return
	}
	approvalId := crypto.Keccak256Hash(packed)
	fmt.Printf("🔑 approvalId : %s\n", approvalId.Hex())

	// Now check the same way as case 11, but using approvalId
	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	ctx := context.Background()

	// 1. getRelayerList
	var relayerResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &relayerResult, "getRelayerList"); err != nil {
		fmt.Printf("❌ getRelayerList() failed: %v\n", err)
		return
	}
	relayers, ok := relayerResult[0].([]common.Address)
	if !ok {
		fmt.Println("❌ Cannot parse relayer list")
		return
	}
	fmt.Printf("👥 Total relayers: %d\n", len(relayers))

	// 2. getThreshold (dynamic: ceil(relayerList.length * 2 / 3))
	var thresholdResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &thresholdResult, "getThreshold"); err != nil {
		fmt.Printf("❌ getThreshold() failed: %v\n", err)
		return
	}
	threshold, _ := thresholdResult[0].(*big.Int)
	fmt.Printf("🔐 Threshold (dynamic 2/3): %s/%d\n", threshold.String(), len(relayers))

	// 3. approvalCount(approvalId)
	var countResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &countResult, "approvalCount", approvalId); err != nil {
		fmt.Printf("❌ approvalCount() failed: %v\n", err)
		return
	}
	count, _ := countResult[0].(*big.Int)
	fmt.Printf("📊 Approval count: %s / %s\n", count.String(), threshold.String())

	// 4. messageExecuted(approvalId)
	var executedResult []interface{}
	if err := contract.Call(&bind.CallOpts{Context: ctx}, &executedResult, "messageExecuted", approvalId); err != nil {
		fmt.Printf("⚠️  messageExecuted() failed: %v\n", err)
	} else {
		executed, _ := executedResult[0].(bool)
		if executed {
			fmt.Printf("✅ Confirmation đã được executed!\n")
		} else {
			fmt.Printf("⏳ Confirmation chưa được executed\n")
		}
	}

	// 5. hasApproved(approvalId, relayer) cho từng relayer
	fmt.Println("\n─────────────────────────────────────────────────────")
	fmt.Println("📋 Chi tiết approval từng relayer:")
	fmt.Println("─────────────────────────────────────────────────────")

	approvedCount := 0
	for i, relayer := range relayers {
		var approvedResult []interface{}
		if err := contract.Call(&bind.CallOpts{Context: ctx}, &approvedResult, "hasApproved", approvalId, relayer); err != nil {
			fmt.Printf("   [%d] %s → ❌ ERROR: %v\n", i+1, relayer.Hex(), err)
			continue
		}
		approved, _ := approvedResult[0].(bool)
		if approved {
			approvedCount++
			fmt.Printf("   [%d] %s → ✅ ĐÃ SUBMIT\n", i+1, relayer.Hex())
		} else {
			fmt.Printf("   [%d] %s → ⏳ CHƯA SUBMIT\n", i+1, relayer.Hex())
		}
	}

	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("📊 Tổng kết: %d/%d relayer đã submit (threshold: %s)\n", approvedCount, len(relayers), threshold.String())
	if count.Cmp(threshold) >= 0 {
		fmt.Println("✅ ĐÃ ĐỦ THRESHOLD!")
	} else {
		remaining := new(big.Int).Sub(threshold, count)
		fmt.Printf("⏳ Còn thiếu %s approval nữa\n", remaining.String())
	}
}

// mustABIType – helper tạo abi.Type từ string, panic nếu lỗi
func mustABIType(t string) abi.Type {
	typ, err := abi.NewType(t, "", nil)
	if err != nil {
		panic(fmt.Sprintf("invalid abi type: %s: %v", t, err))
	}
	return typ
}

// ─────────────────────────────────────────────────────────────────────────────
// callLockAndBridgeWithErr – giống callLockAndBridge nhưng trả error để goroutine xử lý
// ─────────────────────────────────────────────────────────────────────────────
func callLockAndBridgeWithErr(
	client *ethclient.Client,
	auth *bind.TransactOpts,
	contractAddress common.Address,
	parsedABI abi.ABI,
	recipient common.Address,
	amountWei *big.Int,
) error {
	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		return fmt.Errorf("get nonce: %w", err)
	}

	txAuth := &bind.TransactOpts{
		From:     auth.From,
		Nonce:    big.NewInt(int64(nonce)),
		Signer:   auth.Signer,
		Value:    amountWei,
		GasLimit: 500_000,
	}

	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	tx, err := contract.Transact(txAuth, "lockAndBridge", recipient)
	if err != nil {
		return fmt.Errorf("transact: %w", err)
	}

	log.Printf("   📤 lockAndBridge tx: %s (from %s)", tx.Hash().Hex(), auth.From.Hex())
	receipt, err := waitForTransaction(client, tx.Hash(), "lockAndBridge")
	if err != nil {
		return fmt.Errorf("wait receipt (tx=%s): %w", tx.Hash().Hex(), err)
	}
	log.Printf("   ✅ lockAndBridge block=%d gas=%d status=%d",
		receipt.BlockNumber.Uint64(), receipt.GasUsed, receipt.Status)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// callSendMessageWithErr – giống callSendMessage nhưng trả error để goroutine xử lý
// ─────────────────────────────────────────────────────────────────────────────
func callSendMessageWithErr(
	client *ethclient.Client,
	auth *bind.TransactOpts,
	contractAddress common.Address,
	parsedABI abi.ABI,
	target common.Address,
	amountWei *big.Int,
	payloadHex string,
) error {
	var payloadBytes []byte
	if payloadHex != "" {
		var err error
		payloadBytes, err = hex.DecodeString(strings.TrimPrefix(payloadHex, "0x"))
		if err != nil {
			return fmt.Errorf("invalid payload: %w", err)
		}
	}

	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		return fmt.Errorf("get nonce: %w", err)
	}

	txAuth := &bind.TransactOpts{
		From:     auth.From,
		Nonce:    big.NewInt(int64(nonce)),
		Signer:   auth.Signer,
		Value:    amountWei,
		GasLimit: 500_000,
	}

	contract := bind.NewBoundContract(contractAddress, parsedABI, client, client, client)
	tx, err := contract.Transact(txAuth, "sendMessage", target, payloadBytes, []common.Address{})
	if err != nil {
		return fmt.Errorf("transact: %w", err)
	}

	log.Printf("   📤 sendMessage tx: %s (from %s)", tx.Hash().Hex(), auth.From.Hex())
	receipt, err := waitForTransaction(client, tx.Hash(), "sendMessage")
	if err != nil {
		return fmt.Errorf("wait receipt (tx=%s): %w", tx.Hash().Hex(), err)
	}
	log.Printf("   ✅ sendMessage block=%d gas=%d status=%d",
		receipt.BlockNumber.Uint64(), receipt.GasUsed, receipt.Status)
	return nil
}
