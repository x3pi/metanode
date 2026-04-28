package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/joho/godotenv"

	crosschainabi "github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler/abi"
)

// BytecodeData holds bytecode from JSON file
type BytecodeData struct {
	ConfigsCC       string `json:"configs_cc"`
	Pingpong        string `json:"pingpong"`
	PingpongFactory string `json:"pingpong_factory"`
}

type PingPongConfig struct {
	PingPong string `json:"pingpong_address"`
	Factory  string `json:"factory_address"`
}

func loadPingPongConfig() PingPongConfig {
	var cfg PingPongConfig
	data, err := os.ReadFile("contract_pingpong.json")
	if err == nil {
		json.Unmarshal(data, &cfg)
	}
	return cfg
}

func savePingPongConfig(cfg PingPongConfig) {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile("contract_pingpong.json", data, 0644)
}

func main() {
	// Load .env
	if err := godotenv.Load(); err != nil {
		log.Printf("⚠️  .env not found, using environment variables")
	}

	httpURL := getEnv("HTTP_URL", "http://192.168.1.234:8545")
	privateKeyHex := getEnv("PRIVATE_KEY", "")
	ccContractStr := getEnv("CC_CONTRACT", "")
	payloadCCHex := getEnv("PAYLOAD_CC", "")
	addressLockBalanceStr := getEnv("ADDRESS_LOCK_BALANCE", "")
	envChainIdStr := getEnv("CHAIN_ID", "")
	envRegisteredId := getEnv("REGISTERED_ID", "")
	envPubEmbassies := getEnv("PUB_EMBASSIES", "")
	envEthAddressEmbassies := getEnv("ETH_ADDRESS_EMBASSIES", "")

	if privateKeyHex == "" {
		log.Fatal("❌ PRIVATE_KEY is required in .env")
	}
	if ccContractStr == "" {
		log.Fatal("❌ CC_CONTRACT is required in .env")
	}

	ccContract := common.HexToAddress(ccContractStr)
	addressLockBalance := common.HexToAddress(addressLockBalanceStr)

	// Connect to node
	client, err := ethclient.Dial(httpURL)
	if err != nil {
		log.Fatalf("❌ Failed to connect to %s: %v", httpURL, err)
	}
	defer client.Close()

	// Parse private key
	privateKeyHex = strings.TrimPrefix(privateKeyHex, "0x")
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		log.Fatalf("❌ Invalid private key: %v", err)
	}
	publicKeyECDSA, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		log.Fatal("❌ Failed to cast public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	// Get chainID
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		log.Fatalf("❌ Failed to get chainID: %v", err)
	}

	// Parse cross-chain ABI
	parsedABI, err := abi.JSON(strings.NewReader(crosschainabi.CCGatewayABI))
	if err != nil {
		log.Fatalf("❌ Failed to parse CrossChain ABI: %v", err)
	}

	log.Println("═══════════════════════════════════════════")
	log.Println("🧪 Test Fake Contract (No Bytecode)")
	log.Println("═══════════════════════════════════════════")
	log.Printf("👤 From          : %s", fromAddress.Hex())
	log.Printf("📍 CC_CONTRACT   : %s", ccContract.Hex())
	log.Printf("🔐 LOCK_BALANCE  : %s", addressLockBalance.Hex())
	log.Printf("📦 PAYLOAD_CC    : %s", payloadCCHex)
	log.Printf("🔗 EVM Chain ID  : %s", chainID.String())
	log.Printf("🆔 CHAIN_ID (env): %s", envChainIdStr)
	log.Printf("📋 REGISTERED_ID : %s", envRegisteredId)
	log.Println("═══════════════════════════════════════════")

	for {
		fmt.Println("\n═════════════════════════════════════════")
		fmt.Println("📋 Test Fake Contract Menu")
		fmt.Println("═════════════════════════════════════════")
		fmt.Println("  1. Deploy configs_cc  (CHAIN_ID, REGISTERED_ID, v.v...)")
		fmt.Println("  2. lockAndBridge      (recipient=ADDRESS_LOCK_BALANCE, nhập amount + destId)")
		fmt.Println("  3. sendMessage        (target, payload=PAYLOAD_CC, nhập amount + destId)")
		fmt.Println("  4. Check Balance      (wallet + ADDRESS_LOCK_BALANCE)")
		fmt.Println("  5. 🔥 Spam lockAndBridge (đọc generated_keys.json, đo TPS)")
		fmt.Println("  6. 🌉 Cross-Chain TPS (spam lockAndBridge → check balance chain đích)")
		fmt.Println("  7. 🏓 Ping Pong Demo (deploy + serve + hit cross-chain)")
		fmt.Println("  8. 🛠️  [CREATE2] Deploy 2 Contract giống nhau trên 2 Chain")
		fmt.Println("  0. Exit")
		fmt.Print("\nNhập lựa chọn: ")

		var choice string
		fmt.Scanln(&choice)

		switch choice {
		case "1":
			deployConfigsCC(client, privateKey, chainID, fromAddress, envChainIdStr, envRegisteredId, envPubEmbassies, envEthAddressEmbassies)
		case "2":
			callLockAndBridge(client, privateKey, chainID, fromAddress, ccContract, parsedABI, addressLockBalance)
		case "3":
			callSendMessage(client, privateKey, chainID, fromAddress, ccContract, parsedABI, payloadCCHex)
		case "4":
			checkBalances(client, fromAddress, addressLockBalance)
		case "5":
			spamLockAndBridge(client, chainID, ccContract, parsedABI, addressLockBalance)
		case "6":
			spamCrossChainTPS(client, chainID, ccContract, parsedABI, addressLockBalance)
		case "7":
			pingPongDemo(client, privateKey, chainID, fromAddress, ccContract, chainID)
		case "8":
			pingPongDemoCreate2(client, privateKey, chainID, fromAddress, ccContract)
		case "0":
			fmt.Println("👋 Bye!")
			return
		default:
			fmt.Printf("❌ Lựa chọn không hợp lệ: %s\n", choice)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// callLockAndBridge – gọi lockAndBridge(recipient) trên CC_CONTRACT (fake, không có bytecode)
// Sử dụng raw transaction để bypass bytecode check của go-ethereum bind
// ─────────────────────────────────────────────────────────────────────────────
func callLockAndBridge(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	ccContract common.Address,
	parsedABI abi.ABI,
	recipient common.Address,
) {
	if recipient == (common.Address{}) {
		fmt.Println("❌ ADDRESS_LOCK_BALANCE chưa được set trong .env")
		return
	}

	fmt.Print("Nhập amount ETH (vd: 1, 0.5): ")
	var amountStr string
	fmt.Scanln(&amountStr)
	if amountStr == "" {
		amountStr = "1"
	}

	amountETH, ok := new(big.Float).SetString(amountStr)
	if !ok {
		fmt.Println("❌ Amount không hợp lệ")
		return
	}
	weiMul := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	amountWei, _ := new(big.Float).Mul(amountETH, weiMul).Int(nil)

	fmt.Print("Nhập destinationId (vd: 2): ")
	var destIdStr string
	fmt.Scanln(&destIdStr)
	destIdStr = strings.TrimSpace(destIdStr)
	if destIdStr == "" {
		fmt.Println("❌ destinationId is required")
		return
	}
	destinationId, ok := new(big.Int).SetString(destIdStr, 10)
	if !ok {
		fmt.Println("❌ destinationId không hợp lệ")
		return
	}

	ethF := new(big.Float).Quo(new(big.Float).SetInt(amountWei), big.NewFloat(1e18))
	fmt.Printf("\n🔒 lockAndBridge()\n")
	fmt.Printf("   Contract      : %s\n", ccContract.Hex())
	fmt.Printf("   Recipient     : %s\n", recipient.Hex())
	fmt.Printf("   DestinationId : %s\n", destinationId.String())
	fmt.Printf("   Amount        : %s ETH (%s wei)\n", ethF.Text('f', 8), amountWei.String())

	// ABI encode: lockAndBridge(address recipient, uint256 destinationId)
	inputData, err := parsedABI.Pack("lockAndBridge", recipient, destinationId)
	if err != nil {
		fmt.Printf("❌ ABI pack error: %v\n", err)
		return
	}

	txHash, err := sendRawTx(client, privateKey, chainID, from, ccContract, amountWei, inputData)
	if err != nil {
		fmt.Printf("❌ Send tx failed: %v\n", err)
		return
	}

	fmt.Printf("📤 Tx: %s\n", txHash.Hex())
	receipt, err := waitForReceipt(client, txHash, "lockAndBridge")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	fmt.Printf("✅ lockAndBridge confirmed! Block=%d  Gas=%d  Status=%d\n",
		receipt.BlockNumber, receipt.GasUsed, receipt.Status)
}

// ─────────────────────────────────────────────────────────────────────────────
// callSendMessage – gọi sendMessage(target, payload, destinationId) trên CC_CONTRACT
// target = CC_CONTRACT (vì contract giả), payload = PAYLOAD_CC từ .env
// ─────────────────────────────────────────────────────────────────────────────
func callSendMessage(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	ccContract common.Address,
	parsedABI abi.ABI,
	payloadCCHex string,
) {
	fmt.Print("Nhập target address (Enter = CC_CONTRACT): ")
	var targetStr string
	fmt.Scanln(&targetStr)
	targetStr = strings.TrimSpace(targetStr)

	target := ccContract // default target = CC_CONTRACT
	if targetStr != "" && common.IsHexAddress(targetStr) {
		target = common.HexToAddress(targetStr)
	}

	fmt.Print("Nhập amount ETH (vd: 0, 0.5, Enter = 0): ")
	var amountStr string
	fmt.Scanln(&amountStr)
	if amountStr == "" {
		amountStr = "0"
	}

	amountETH, ok := new(big.Float).SetString(amountStr)
	if !ok {
		fmt.Println("❌ Amount không hợp lệ")
		return
	}
	weiMul := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	amountWei, _ := new(big.Float).Mul(amountETH, weiMul).Int(nil)

	fmt.Print("Nhập destinationId (vd: 2): ")
	var destIdStr string
	fmt.Scanln(&destIdStr)
	destIdStr = strings.TrimSpace(destIdStr)
	if destIdStr == "" {
		fmt.Println("❌ destinationId is required")
		return
	}
	destinationId, ok := new(big.Int).SetString(destIdStr, 10)
	if !ok {
		fmt.Println("❌ destinationId không hợp lệ")
		return
	}

	// Decode payload
	var payloadBytes []byte
	if payloadCCHex != "" {
		var decErr error
		payloadBytes, decErr = hex.DecodeString(strings.TrimPrefix(payloadCCHex, "0x"))
		if decErr != nil {
			fmt.Printf("❌ Invalid PAYLOAD_CC hex: %v\n", decErr)
			return
		}
	}

	ethF := new(big.Float).Quo(new(big.Float).SetInt(amountWei), big.NewFloat(1e18))
	fmt.Printf("\n📨 sendMessage()\n")
	fmt.Printf("   Contract      : %s\n", ccContract.Hex())
	fmt.Printf("   Target        : %s\n", target.Hex())
	fmt.Printf("   DestinationId : %s\n", destinationId.String())
	fmt.Printf("   Amount        : %s ETH (%s wei)\n", ethF.Text('f', 8), amountWei.String())
	fmt.Printf("   Payload       : %s (%d bytes)\n", payloadCCHex, len(payloadBytes))

	// ABI encode: sendMessage(address target, bytes payload, uint256 destinationId)
	inputData, err := parsedABI.Pack("sendMessage", target, payloadBytes, destinationId)
	if err != nil {
		fmt.Printf("❌ ABI pack error: %v\n", err)
		return
	}

	txHash, err := sendRawTx(client, privateKey, chainID, from, ccContract, amountWei, inputData)
	if err != nil {
		fmt.Printf("❌ Send tx failed: %v\n", err)
		return
	}

	fmt.Printf("📤 Tx: %s\n", txHash.Hex())
	receipt, err := waitForReceipt(client, txHash, "sendMessage")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	fmt.Printf("✅ sendMessage confirmed! Block=%d  Gas=%d  Status=%d\n",
		receipt.BlockNumber, receipt.GasUsed, receipt.Status)
}

// ─────────────────────────────────────────────────────────────────────────────
// checkBalances – Kiểm tra số dư ví deployer + ADDRESS_LOCK_BALANCE
// ─────────────────────────────────────────────────────────────────────────────
func checkBalances(client *ethclient.Client, wallet common.Address, lockBalanceAddr common.Address) {
	fmt.Println("\n💰 Checking Balances...")
	ctx := context.Background()

	printBalance := func(label string, addr common.Address) {
		if addr == (common.Address{}) {
			fmt.Printf("   %-25s: (chưa set)\n", label)
			return
		}
		b, err := client.BalanceAt(ctx, addr, nil)
		if err != nil {
			fmt.Printf("   %-25s: ERROR %v\n", label, err)
			return
		}
		eth := new(big.Float).Quo(new(big.Float).SetInt(b), big.NewFloat(1e18))
		fmt.Printf("   %-25s: %s\n", label, addr.Hex())
		fmt.Printf("   %-25s  %s ETH\n", "", eth.Text('f', 8))
		fmt.Printf("   %-25s  %s wei\n", "", b.String())
	}

	printBalance("👤 Wallet (deployer)", wallet)
	printBalance("🔐 ADDRESS_LOCK_BALANCE", lockBalanceAddr)

	// Hardcoded check: pk 1bc735d...
	hardcodedAddr := common.HexToAddress("0xbF2b4B9b9dFB6d23F7F0FC46981c2eC89f94A9F2")
	if hardcodedAddr != wallet && hardcodedAddr != lockBalanceAddr {
		printBalance("📋 0xbF2b...9F2", hardcodedAddr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// deployConfigsCC – Deploy CrossChainConfigRegistry contract (configs_cc)
// Constructor: constructor(uint256 _chainId)
// Tự động đọc CHAIN_ID, REGISTERED_ID, PUB_EMBASSIES, ETH_ADDRESS_EMBASSIES từ .env
// ─────────────────────────────────────────────────────────────────────────────
func deployConfigsCC(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	envChainIdStr string,
	envRegisteredId string,
	envPubEmbassies string,
	envEthAddressEmbassies string,
) {
	fmt.Println("\n🚀 Deploy CrossChainConfigRegistry (configs_cc)")

	// ── Validate CHAIN_ID from .env ──
	if envChainIdStr == "" {
		fmt.Println("❌ CHAIN_ID not set in .env")
		return
	}
	constructorChainId, ok := new(big.Int).SetString(envChainIdStr, 10)
	if !ok {
		fmt.Printf("❌ CHAIN_ID '%s' không hợp lệ\n", envChainIdStr)
		return
	}

	// ── Parse REGISTERED_ID from .env (comma-separated) ──
	var registeredIds []*big.Int
	if envRegisteredId != "" {
		parts := strings.Split(envRegisteredId, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			id, ok := new(big.Int).SetString(p, 10)
			if !ok {
				fmt.Printf("❌ REGISTERED_ID '%s' không hợp lệ\n", p)
				return
			}
			registeredIds = append(registeredIds, id)
		}
	}

	// ── Parse PUB_EMBASSIES từ .env (comma-separated hex) ──
	var embassyBLSKeys [][]byte
	if envPubEmbassies != "" {
		parts := strings.Split(envPubEmbassies, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			pkBytes, err := hex.DecodeString(strings.TrimPrefix(p, "0x"))
			if err != nil {
				fmt.Printf("❌ PUB_EMBASSIES key '%s' hex không hợp lệ: %v\n", p, err)
				return
			}
			embassyBLSKeys = append(embassyBLSKeys, pkBytes)
		}
	}

	// ── Parse ETH_ADDRESS_EMBASSIES từ .env (comma-separated address) ──
	var embassyEthAddrs []common.Address
	if envEthAddressEmbassies != "" {
		parts := strings.Split(envEthAddressEmbassies, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !common.IsHexAddress(p) {
				fmt.Printf("❌ ETH_ADDRESS_EMBASSIES '%s' không phải address hợp lệ\n", p)
				return
			}
			embassyEthAddrs = append(embassyEthAddrs, common.HexToAddress(p))
		}
	}

	// Validate: phải có cùng số lượng BLS keys và ETH addresses
	if len(embassyBLSKeys) > 0 && len(embassyEthAddrs) > 0 && len(embassyBLSKeys) != len(embassyEthAddrs) {
		fmt.Printf("❌ PUB_EMBASSIES (%d) và ETH_ADDRESS_EMBASSIES (%d) phải cùng số lượng\n",
			len(embassyBLSKeys), len(embassyEthAddrs))
		return
	}

	fmt.Printf("   CHAIN_ID             : %s\n", constructorChainId.String())
	fmt.Printf("   REGISTERED_ID        : %v (%d chains)\n", registeredIds, len(registeredIds))
	fmt.Printf("   PUB_EMBASSIES        : %d keys\n", len(embassyBLSKeys))
	fmt.Printf("   ETH_ADDRESS_EMBASSIES: %d addrs\n", len(embassyEthAddrs))

	// ── Load bytecode ──
	bytecodeFile, err := os.ReadFile("byteCode/byteCode.json")
	if err != nil {
		fmt.Printf("❌ Failed to read byteCode/byteCode.json: %v\n", err)
		return
	}

	var bcData BytecodeData
	if err := json.Unmarshal(bytecodeFile, &bcData); err != nil {
		fmt.Printf("❌ Failed to parse byteCode JSON: %v\n", err)
		return
	}

	if bcData.ConfigsCC == "" {
		fmt.Println("❌ configs_cc bytecode is empty in byteCode.json")
		return
	}

	bytecode := common.FromHex(strings.TrimPrefix(bcData.ConfigsCC, "0x"))
	if len(bytecode) == 0 {
		fmt.Println("❌ configs_cc bytecode is invalid")
		return
	}

	fmt.Printf("   Bytecode size  : %d bytes\n", len(bytecode))

	// ── ABI encode constructor: constructor(uint256 _chainId) ──
	uint256Type, _ := abi.NewType("uint256", "", nil)
	constructorArgs, err := abi.Arguments{{Type: uint256Type}}.Pack(constructorChainId)
	if err != nil {
		fmt.Printf("❌ Pack constructor args error: %v\n", err)
		return
	}

	deployData := append(bytecode, constructorArgs...)

	// ── Deploy ──
	txHash, contractAddr, err := deployRawTx(client, privateKey, chainID, from, deployData)
	if err != nil {
		fmt.Printf("❌ Deploy failed: %v\n", err)
		return
	}

	fmt.Printf("📤 Deploy Tx: %s\n", txHash.Hex())
	receipt, err := waitForReceipt(client, txHash, "deploy configs_cc")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	if receipt.ContractAddress != (common.Address{}) {
		contractAddr = receipt.ContractAddress
	}

	fmt.Printf("✅ configs_cc deployed! Block=%d  Gas=%d  Status=%d\n",
		receipt.BlockNumber, receipt.GasUsed, receipt.Status)
	fmt.Printf("📍 Contract Address: %s\n", contractAddr.Hex())

	// ── Post-deploy: auto add embassy keys from PUB_EMBASSIES + ETH_ADDRESS_EMBASSIES ──
	if len(embassyBLSKeys) > 0 {
		fmt.Printf("\n🔑 Auto adding %d embassies (BLS key + ETH address)...\n", len(embassyBLSKeys))
		autoAddEmbassyKeys(client, privateKey, chainID, from, contractAddr, embassyBLSKeys, embassyEthAddrs)
	}

	// ── Post-deploy: auto register chains from REGISTERED_ID ──
	if len(registeredIds) > 0 {
		fmt.Printf("\n📋 Auto registering %d chains...\n", len(registeredIds))
		autoRegisterChains(client, privateKey, chainID, from, contractAddr, registeredIds)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// autoAddEmbassyKeys – Tự động gọi addEmbassy(bytes _blsPublicKey, address _ethAddress)
// Dùng CrossChainConfigABI đã parse để đảm bảo 4-byte selector đúng
// ─────────────────────────────────────────────────────────────────────────────
func autoAddEmbassyKeys(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	contractAddr common.Address,
	embassyBLSKeys [][]byte,
	embassyEthAddrs []common.Address,
) {
	// Parse ABI từ CrossChainConfigABI để lấy đúng 4-byte selector
	parsedConfigABI, err := abi.JSON(strings.NewReader(crosschainabi.CrossChainConfigABI))
	if err != nil {
		fmt.Printf("   ❌ Failed to parse CrossChainConfigABI: %v\n", err)
		return
	}

	// Debug: kiểm tra quyền owner của caller
	isOwnerData, _ := parsedConfigABI.Pack("isOwner", from)
	var isOwnerResultHex string
	callErr := client.Client().CallContext(context.Background(), &isOwnerResultHex, "eth_call",
		map[string]interface{}{
			"from": from.Hex(),
			"to":   contractAddr.Hex(),
			"data": "0x" + hex.EncodeToString(isOwnerData),
		}, "latest")
	
	if callErr != nil {
		fmt.Printf("   ⚠️  isOwner() eth_call error: %v\n", callErr)
	} else {
		isOwnerResultHex = strings.TrimPrefix(isOwnerResultHex, "0x")
		// Trả về bool (32 bytes), true là 0x...1
		if len(isOwnerResultHex) >= 64 && isOwnerResultHex[63] == '1' {
			fmt.Printf("   ✅ Caller %s là owner hợp lệ\n", from.Hex())
		} else {
			fmt.Printf("   ❌ FATAL: Caller %s KHÔNG phải là owner → các thao tác admin sẽ revert\n", from.Hex())
			fmt.Printf("      Hãy dùng đúng PRIVATE_KEY của owner trong .env\n")
			return
		}
	}

	for i, pkBytes := range embassyBLSKeys {
		// Nếu chưa có ETH address tương ứng → bỏ qua
		var ethAddr common.Address
		if i < len(embassyEthAddrs) {
			ethAddr = embassyEthAddrs[i]
		} else {
			fmt.Printf("   [⚠️] Embassy[%d]: no ETH address provided, skipping\n", i+1)
			continue
		}

		fmt.Printf("   [%d/%d] addEmbassy: BLS=%d bytes, ETH=%s\n",
			i+1, len(embassyBLSKeys), len(pkBytes), ethAddr.Hex())

		// Dùng parsed ABI → đảm bảo selector đúng
		callData, err := parsedConfigABI.Pack("addEmbassy", pkBytes, ethAddr)
		if err != nil {
			fmt.Printf("   ❌ ABI pack error: %v\n", err)
			continue
		}
		// Debug: in 4-byte selector để verify
		fmt.Printf("   🔍 Selector: 0x%s  Calldata size: %d bytes\n",
			hex.EncodeToString(callData[:4]), len(callData))

		// ── eth_call simulation để lấy revert reason trước khi gửi TX thật ──
		var simResult string
		simErr := client.Client().CallContext(context.Background(), &simResult, "eth_call",
			map[string]interface{}{
				"from": from.Hex(),
				"to":   contractAddr.Hex(),
				"data": "0x" + hex.EncodeToString(callData),
			}, "latest")
		if simErr != nil {
			fmt.Printf("   ⚠️  eth_call simulate: REVERT — %v\n", simErr)
			fmt.Printf("      → Có thể bytecode của contract là bản cũ (addEmbassy chỉ có 1 param)\n")
			fmt.Printf("      → Hãy recompile contract và update byteCode/byteCode.json\n")
			// Vẫn thử gửi TX để xem chain trả ra gì
		} else {
			fmt.Printf("   ✅ eth_call simulate OK (result: %s)\n", simResult)
		}

		txHash, err := sendRawTx(client, privateKey, chainID, from, contractAddr, big.NewInt(0), callData)
		if err != nil {
			fmt.Printf("   ❌ Send tx failed: %v\n", err)
			continue
		}

		fmt.Printf("   📤 addEmbassy Tx: %s\n", txHash.Hex())
		receipt, err := waitForReceipt(client, txHash, fmt.Sprintf("addEmbassy[%d]", i+1))
		if err != nil {
			fmt.Printf("   ❌ %v\n", err)
			continue
		}
		fmt.Printf("   ✅ addEmbassy[%d] confirmed! Block=%d  Status=%d\n",
			i+1, receipt.BlockNumber, receipt.Status)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// autoRegisterChains – Tự động gọi registerChain cho mỗi ID từ REGISTERED_ID
// nationId = chainId = registeredId, name = "Chain-{id}", gateway = zero address
// ─────────────────────────────────────────────────────────────────────────────
func autoRegisterChains(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	contractAddr common.Address,
	registeredIds []*big.Int,
) {
	uint256Type, _ := abi.NewType("uint256", "", nil)
	stringType, _ := abi.NewType("string", "", nil)
	addressType, _ := abi.NewType("address", "", nil)

	registerMethod := abi.NewMethod("registerChain", "registerChain", abi.Function, "", false, true,
		abi.Arguments{
			{Name: "_nationId", Type: uint256Type},
			{Name: "_chainId", Type: uint256Type},
			{Name: "_name", Type: stringType},
			{Name: "_gateway", Type: addressType},
		},
		abi.Arguments{},
	)

	for i, regId := range registeredIds {
		chainName := fmt.Sprintf("Chain-%s", regId.String())
		gateway := common.Address{} // zero address

		fmt.Printf("   [%d/%d] Registering nationId=%s chainId=%s name=%s\n",
			i+1, len(registeredIds), regId.String(), regId.String(), chainName)

		inputData, err := registerMethod.Inputs.Pack(regId, regId, chainName, gateway)
		if err != nil {
			fmt.Printf("   ❌ ABI pack error: %v\n", err)
			continue
		}
		callData := append(registerMethod.ID, inputData...)

		txHash, err := sendRawTx(client, privateKey, chainID, from, contractAddr, big.NewInt(0), callData)
		if err != nil {
			fmt.Printf("   ❌ Send tx failed: %v\n", err)
			continue
		}

		fmt.Printf("   📤 registerChain Tx: %s\n", txHash.Hex())
		receipt, err := waitForReceipt(client, txHash, fmt.Sprintf("registerChain[%d]", i+1))
		if err != nil {
			fmt.Printf("   ❌ %v\n", err)
			continue
		}
		fmt.Printf("   ✅ registerChain[%d] confirmed! Block=%d  Status=%d\n",
			i+1, receipt.BlockNumber, receipt.Status)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sendRawTx – Gửi raw transaction trực tiếp (bypass bytecode check)
// Dùng cho contract giả CC_CONTRACT không có bytecode trên chain
// ─────────────────────────────────────────────────────────────────────────────
func sendRawTx(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	to common.Address,
	value *big.Int,
	data []byte,
) (common.Hash, error) {
	ctx := context.Background()

	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, fmt.Errorf("get nonce: %w", err)
	}

	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		gasPrice = big.NewInt(0)
	}

	gasLimit := uint64(500_000)

	tx := types.NewTransaction(nonce, to, value, gasLimit, gasPrice, data)

	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, privateKey)
	if err != nil {
		return common.Hash{}, fmt.Errorf("sign tx: %w", err)
	}

	err = client.SendTransaction(ctx, signedTx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("send tx: %w", err)
	}

	return signedTx.Hash(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// deployRawTx – Deploy contract bằng raw transaction (to = nil)
// ─────────────────────────────────────────────────────────────────────────────
func deployRawTx(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	deployData []byte,
) (common.Hash, common.Address, error) {
	ctx := context.Background()

	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, common.Address{}, fmt.Errorf("get nonce: %w", err)
	}

	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		gasPrice = big.NewInt(0)
	}

	gasLimit := uint64(5_000_000) // higher gas for deploy

	tx := types.NewContractCreation(nonce, big.NewInt(0), gasLimit, gasPrice, deployData)

	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, privateKey)
	if err != nil {
		return common.Hash{}, common.Address{}, fmt.Errorf("sign tx: %w", err)
	}

	err = client.SendTransaction(ctx, signedTx)
	if err != nil {
		return common.Hash{}, common.Address{}, fmt.Errorf("send tx: %w", err)
	}

	// Predict contract address
	contractAddr := crypto.CreateAddress(from, nonce)

	return signedTx.Hash(), contractAddr, nil
}

// RawReceipt chứa receipt data từ raw RPC (bao gồm cả revertReason custom)
type RawReceipt struct {
	Status          uint64
	GasUsed         uint64
	BlockNumber     uint64
	TransactionHash common.Hash
	ContractAddress common.Address
	Return          string
	Logs            []*types.Log
}

func waitForReceipt(client *ethclient.Client, txHash common.Hash, name string) (*RawReceipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for {
		var raw map[string]interface{}
		err := client.Client().CallContext(ctx, &raw, "eth_getTransactionReceipt", txHash)
		if err == nil && raw != nil {
			rcp := parseRawReceipt(raw, txHash)

			if rcp.Status == 1 {
				log.Printf("✅ [%s] status=1 block=%d", name, rcp.BlockNumber)
				return rcp, nil
			}

			revertMsg := fmt.Sprintf("tx reverted (status=%d, gasUsed=%d, txHash=%s)",
				rcp.Status, rcp.GasUsed, txHash.Hex())

			return rcp, fmt.Errorf("%s | reason: %s", revertMsg, rcp.Return)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout 30s waiting for tx %s", txHash.Hex())
		case <-time.After(10 * time.Millisecond):
			// poll every 500ms
		}
	}
}

// parseRawReceipt parse raw map từ eth_getTransactionReceipt thành RawReceipt
func parseRawReceipt(raw map[string]interface{}, txHash common.Hash) *RawReceipt {
	rcp := &RawReceipt{TransactionHash: txHash}

	if s, ok := raw["status"].(string); ok {
		rcp.Status = hexToUint64(s)
	}
	if g, ok := raw["gasUsed"].(string); ok {
		rcp.GasUsed = hexToUint64(g)
	}
	if bn, ok := raw["blockNumber"].(string); ok {
		rcp.BlockNumber = hexToUint64(bn)
	}
	if ca, ok := raw["contractAddress"].(string); ok && ca != "" {
		rcp.ContractAddress = common.HexToAddress(ca)
	}

	// Node của Meta trả về return data trong field "return" (hex string)
	// Đọc từ "return" trước, fallback sang "revertReason" nếu không có
	returnHex := ""
	if rv, ok := raw["return"].(string); ok && rv != "" && rv != "0x" {
		returnHex = rv
	}
	if returnHex != "" {
		returnHex = strings.TrimPrefix(returnHex, "0x")
		if decoded, err := hex.DecodeString(returnHex); err == nil && len(decoded) > 0 {
			rcp.Return = string(decoded)
		}
	}

	return rcp
}

func hexToUint64(s string) uint64 {
	s = strings.TrimPrefix(s, "0x")
	val, _ := new(big.Int).SetString(s, 16)
	if val == nil {
		return 0
	}
	return val.Uint64()
}

// ─────────────────────────────────────────────────────────────────────────────
// printLogs – in ra event logs từ receipt
// ─────────────────────────────────────────────────────────────────────────────
func printLogs(receipt *types.Receipt) {
	if len(receipt.Logs) > 0 {
		fmt.Printf("📜 Events: %d\n", len(receipt.Logs))
		for i, vlog := range receipt.Logs {
			fmt.Printf("   [%d] Address: %s\n", i+1, vlog.Address.Hex())
			for j, topic := range vlog.Topics {
				fmt.Printf("       topic[%d]: %s\n", j, topic.Hex())
			}
			if len(vlog.Data) > 0 {
				fmt.Printf("       data (%d bytes): 0x%s\n", len(vlog.Data), hex.EncodeToString(vlog.Data))
			}
		}
	} else {
		fmt.Println("📜 No events emitted")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// getEnv – helper đọc env var
// ─────────────────────────────────────────────────────────────────────────────
func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// ─────────────────────────────────────────────────────────────────────────────
// pingPongDemo – Deploy CrossChainPingPong, serve + hit ball cross-chain
//
// Bước 1: Deploy contract lên chain hiện tại (với gateway = ccContract, homeChainId)
// Bước 2: Gọi serveBall() → contract giữ ball, hasBall = true
// Bước 3: Gọi hitBallTo(destChainId) → contract gọi gateway.sendMessage(...)
//
//	→ Observer scan MessageSent event → batchSubmit lên chain đích
//	→ Go node chain đích gọi contract.receiveBall(ball)
//	→ hasBall trên chain đích = true, chain nguồn = false
//
// ─────────────────────────────────────────────────────────────────────────────
func pingPongDemo(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	ccContract common.Address, // CrossChainGateway address
	_ *big.Int, // unused homeChainId param (đọc từ user input)
) {
	// Lấy khóa PK_DEPLOY_CC để deploy
	pkHex := getEnv("PK_DEPLOY_CC", "")
	var deployPk *ecdsa.PrivateKey
	var deployAddr common.Address
	if pkHex != "" {
		pkHex = strings.TrimPrefix(pkHex, "0x")
		deployPk, _ = crypto.HexToECDSA(pkHex)
		if deployPk != nil {
			pubECDSA, _ := deployPk.Public().(*ecdsa.PublicKey)
			deployAddr = crypto.PubkeyToAddress(*pubECDSA)
		}
	}
	if deployPk == nil {
		fmt.Println("⚠️  Không tìm thấy PK_DEPLOY_CC hơp lệ, dùng PRIVATE_KEY mặc định.")
		deployPk = privateKey
		deployAddr = from
	}

	fmt.Println("\n🏓 ═══════════════════════════════════════════════")
	fmt.Println("   Cross-Chain Ping Pong Demo")
	fmt.Println("   ═══════════════════════════════════════════════")
	fmt.Println("   Contract: CrossChainPingPong")
	fmt.Printf("   Ví Deploy: %s\n", deployAddr.Hex())
	fmt.Printf("   Gateway : %s\n", ccContract.Hex())
	fmt.Println()

	ccBiBytes, err := os.ReadFile("abi/pingpong_cc.json")
	if err != nil {
		fmt.Printf("❌ Không tìm thấy abi/pingpong_cc.json: %v\n", err)
		return
	}
	ccABI, err := abi.JSON(strings.NewReader(string(ccBiBytes)))
	if err != nil {
		fmt.Printf("❌ Lỗi parse ABI: %v\n", err)
		return
	}

	fmt.Println("\n📋 Chọn hành động:")
	fmt.Println("  1. Deploy PingPong contract (lần đầu) [Lấy từ byteCode.json]")
	fmt.Println("  2. Serve Ball (tạo bóng trên chain này)")
	fmt.Println("  3. Hit Ball To (đập bóng sang chain khác)")
	fmt.Println("  4. Check State (hasBall, rallyCount, lastHitter)")
	fmt.Print("\nChọn: ")

	var action string
	fmt.Scanln(&action)

	switch action {
	case "1":
		// ── Deploy CrossChainPingPong ──
		fmt.Println("\n🚀 Deploying CrossChainPingPong...")

		// Đọc bytecode từ file byteCode.json
		bcFile, err := os.ReadFile("byteCode/byteCode.json")
		if err != nil {
			fmt.Printf("❌ Không tìm thấy byteCode/byteCode.json\n")
			return
		}
		var bcData BytecodeData
		json.Unmarshal(bcFile, &bcData)
		bytecode := common.FromHex(strings.TrimPrefix(bcData.Pingpong, "0x"))
		if len(bytecode) == 0 {
			fmt.Println("❌ Bytecode 'pingpong' rỗng hoặc không hợp lệ")
			return
		}

		// ABI encode constructor: constructor() => Không có tham số
		ctorArgs, err := abi.Arguments{}.Pack()
		if err != nil {
			fmt.Printf("❌ Pack constructor error: %v\n", err)
			return
		}

		deployData := append(bytecode, ctorArgs...)
		txHash, contractAddr, err := deployRawTx(client, deployPk, chainID, deployAddr, deployData)
		if err != nil {
			fmt.Printf("❌ Deploy failed: %v\n", err)
			return
		}

		fmt.Printf("📤 Deploy Tx: %s\n", txHash.Hex())
		rcp, err := waitForReceipt(client, txHash, "deploy PingPong")
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}
		if rcp.ContractAddress != (common.Address{}) {
			contractAddr = rcp.ContractAddress
		}
		fmt.Printf("✅ CrossChainPingPong deployed!\n")
		fmt.Printf("📍 Contract Address: %s\n", contractAddr.Hex())

		cfg := loadPingPongConfig()
		cfg.PingPong = contractAddr.Hex()
		savePingPongConfig(cfg)
		fmt.Println("💾 Đã lưu địa chỉ vào contract_pingpong.json")

		fmt.Printf("   ⚠️  Phải deploy cùng địa chỉ (cùng nonce) trên chain đích!\n")

	case "2":
		// ── Serve Ball ──
		cfg := loadPingPongConfig()
		defAddr := cfg.PingPong
		fmt.Printf("Nhập địa chỉ PingPong contract [%s]: ", defAddr)
		var ppAddrStr string
		fmt.Scanln(&ppAddrStr)
		ppAddrStr = strings.TrimSpace(ppAddrStr)
		if ppAddrStr == "" {
			ppAddrStr = defAddr
		}
		ppContract := common.HexToAddress(ppAddrStr)

		// Dùng ABI Pack
		serveData, err := ccABI.Pack("serveBall")
		if err != nil {
			fmt.Printf("❌ Pack lỗi: %v\n", err)
			return
		}
		txHash, err := sendRawTx(client, deployPk, chainID, deployAddr, ppContract, big.NewInt(0), serveData)
		if err != nil {
			fmt.Printf("❌ serveBall tx failed: %v\n", err)
			return
		}
		fmt.Printf("📤 serveBall Tx: %s\n", txHash.Hex())
		rcp, err := waitForReceipt(client, txHash, "serveBall")
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}
		fmt.Printf("✅ Ball served! Block=%d\n", rcp.BlockNumber)
		fmt.Println("   🏓 Chain này đang giữ bóng. Dùng option 3 để hit sang chain khác!")

	case "3":
		// ── Hit Ball To ──
		cfg := loadPingPongConfig()
		defAddr := cfg.PingPong
		fmt.Printf("Nhập địa chỉ PingPong contract [%s]: ", defAddr)
		var ppAddrStr string
		fmt.Scanln(&ppAddrStr)
		ppAddrStr = strings.TrimSpace(ppAddrStr)
		if ppAddrStr == "" {
			ppAddrStr = defAddr
		}
		ppContract := common.HexToAddress(ppAddrStr)

		fmt.Print("Nhập destChainId (Nation ID của chain ĐÍCH, vd: 2): ")
		var destStr string
		fmt.Scanln(&destStr)
		destChainId, ok := new(big.Int).SetString(strings.TrimSpace(destStr), 10)
		if !ok || destChainId.Sign() <= 0 {
			fmt.Println("❌ destChainId không hợp lệ")
			return
		}

		// Dùng ABI Pack
		hitData, err := ccABI.Pack("hitBallTo", destChainId)
		if err != nil {
			fmt.Printf("❌ Pack lỗi: %v\n", err)
			return
		}

		txHash, err := sendRawTx(client, deployPk, chainID, deployAddr, ppContract, big.NewInt(0), hitData)
		if err != nil {
			fmt.Printf("❌ hitBallTo tx failed txHash %s err: %v\n", txHash.Hex(), err)
			return
		}
		fmt.Printf("📤 hitBallTo(%s) Tx: %s\n", destChainId.String(), txHash.Hex())
		rcp, err := waitForReceipt(client, txHash, "hitBallTo")
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			fmt.Println("   → Nếu lỗi 'No ball here!' nghĩa là chain này không giữ bóng")
			return
		}
		fmt.Printf("✅ Ball hit! Block=%d, Gas=%d\n", rcp.BlockNumber, rcp.GasUsed)
		fmt.Printf("   🏓 Bóng đã bay sang chain %s!\n", destChainId.String())
		fmt.Printf("   ⏳ Đợi Observer scan + relay → kiểm tra chain %s receiveBall\n", destChainId.String())
		fmt.Printf("   💡 Trên chain đích: dùng option 4 để check hasBall = true\n")

	case "4":
		// ── Check State: hasBall + ball via eth_call ──
		cfg := loadPingPongConfig()
		defAddr := cfg.PingPong
		fmt.Printf("Nhập địa chỉ PingPong contract [%s]: ", defAddr)
		var ppAddrStr string
		fmt.Scanln(&ppAddrStr)
		ppAddrStr = strings.TrimSpace(ppAddrStr)
		if ppAddrStr == "" {
			ppAddrStr = defAddr
		}
		ppContract := common.HexToAddress(ppAddrStr)

		// hasBall() selector
		hasBallCall, _ := ccABI.Pack("hasBall")
		var hasBallHex string
		ctx := context.Background()
		client.Client().CallContext(ctx, &hasBallHex, "eth_call", map[string]interface{}{
			"from": deployAddr.Hex(),
			"to":   ppContract.Hex(),
			"data": fmt.Sprintf("0x%x", hasBallCall),
		}, "latest")

		hasBall := false
		if len(hasBallHex) > 2 {
			hasData, _ := hex.DecodeString(strings.TrimPrefix(hasBallHex, "0x"))
			res, err := ccABI.Unpack("hasBall", hasData)
			if err == nil && len(res) > 0 {
				hasBall = res[0].(bool)
			}
		}
		fmt.Printf("\n📊 Chain State (contract %s):\n", ppContract.Hex())
		if hasBall {
			fmt.Println("   🏓 hasBall = TRUE  ← Bóng đang ở chain này!")
		} else {
			fmt.Println("   ⚪  hasBall = FALSE ← Bóng không ở chain này")
		}

		// ball() struct getter
		ballCall, _ := ccABI.Pack("ball")
		var ballHex string
		client.Client().CallContext(ctx, &ballHex, "eth_call", map[string]interface{}{
			"from": deployAddr.Hex(),
			"to":   ppContract.Hex(),
			"data": fmt.Sprintf("0x%x", ballCall),
		}, "latest")

		if len(ballHex) > 2 {
			bData, _ := hex.DecodeString(strings.TrimPrefix(ballHex, "0x"))
			res, err := ccABI.Unpack("ball", bData)
			if err == nil && len(res) >= 3 {
				rallyCount := res[0].(*big.Int)
				lastChainId := res[1].(*big.Int)
				lastHitter := res[2].(common.Address)
				fmt.Printf("   🔢 rallyCount  = %s\n", rallyCount.String())
				fmt.Printf("   🔗 lastChainId = %s\n", lastChainId.String())
				fmt.Printf("   👤 lastHitter  = %s\n", lastHitter.Hex())
			}
		}

	default:
		fmt.Println("❌ Lựa chọn không hợp lệ")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// pingPongDemoCreate2 – Tự động Deploy Factory & CrossChainPingPong lên 2 Chain
// ─────────────────────────────────────────────────────────────────────────────
func pingPongDemoCreate2(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey, // Default private key if PK_DEPLOY_CC missing
	srcChainID *big.Int,
	from common.Address,
	ccContract common.Address,
) {
	fmt.Println("\n🛠️  ═══════════════════════════════════════════════")
	fmt.Println("   [CREATE2] TỰ ĐỘNG DEPLOY 2 CONTRACT GIỐNG NHAU")
	fmt.Println("     - Tự tìm NONCE của ví Triển Khai trên 2 Chain")
	fmt.Println("     - Tự đúc Factory trên 2 Chain (nếu chưa có)")
	fmt.Println("     - Kích hoạt CREATE2 ra địa chỉ y hệt nhau")
	fmt.Println("   ═══════════════════════════════════════════════")

	// 1. Get Destination RPC
	destRPC := getEnv("REMOTE_HTTP", "")
	if destRPC == "" {
		fmt.Print("Nhập RPC Chain 2 (vd: http://192.168.1.233:8545): ")
		fmt.Scanln(&destRPC)
		destRPC = strings.TrimSpace(destRPC)
	}
	destClient, err := ethclient.Dial(destRPC)
	if err != nil {
		fmt.Printf("❌ Failed to connect Chain 2: %v\n", err)
		return
	}
	defer destClient.Close()
	destChainID, err := destClient.ChainID(context.Background())
	if err != nil {
		fmt.Printf("❌ Failed to get Chain 2 ID: %v\n", err)
		return
	}

	// 2. Private Key for Deploy (use PK_DEPLOY_CC or default)
	pkHex := getEnv("PK_DEPLOY_CC", "")
	var deployPk *ecdsa.PrivateKey
	var deployAddr common.Address
	if pkHex != "" {
		pkHex = strings.TrimPrefix(pkHex, "0x")
		deployPk, err = crypto.HexToECDSA(pkHex)
		if err == nil {
			pubECDSA, _ := deployPk.Public().(*ecdsa.PublicKey)
			deployAddr = crypto.PubkeyToAddress(*pubECDSA)
		} else {
			fmt.Printf("❌ Lỗi Hex PK_DEPLOY_CC: %v\n", err)
		}
	}
	if deployPk == nil {
		fmt.Println("⚠️  Sử dụng PRIVATE_KEY gốc làm Ví Deploy")
		deployPk = privateKey
		deployAddr = from
	}

	facBiBytes, errAbi := os.ReadFile("abi/pingpong_factory.json")
	if errAbi != nil {
		fmt.Printf("❌ Không tìm thấy abi/pingpong_factory.json: %v\n", errAbi)
		return
	}
	facABI, errAbi := abi.JSON(strings.NewReader(string(facBiBytes)))
	if errAbi != nil {
		fmt.Printf("❌ Lỗi parse ABI pingpong_factory: %v\n", errAbi)
		return
	}

	fmt.Printf("\n🔗 Chain 1 (Src) : %s\n", srcChainID.String())
	fmt.Printf("🔗 Chain 2 (Dest): %s\n", destChainID.String())
	fmt.Printf("👤 Đang dùng ví  : %s\n", deployAddr.Hex())

	ctx := context.Background()
	nonce1, _ := client.PendingNonceAt(ctx, deployAddr)
	nonce2, _ := destClient.PendingNonceAt(ctx, deployAddr)

	fmt.Printf("🔢 Nonce Chain 1 : %d\n", nonce1)
	fmt.Printf("🔢 Nonce Chain 2 : %d\n", nonce2)

	// 3. Prompt for Factory
	fmt.Print("\n❓ Nhập địa chỉ PingPongFactory (Nhấn [Enter] để tự động Deploy Mới trên CẢ 2 mạng): ")
	var factoryStr string
	fmt.Scanln(&factoryStr)
	factoryStr = strings.TrimSpace(factoryStr)

	var factoryAddr common.Address
	if factoryStr == "" {
		// Deploy
		if nonce1 != nonce2 {
			fmt.Println("⚠️  CẢNH BÁO MẠNH: Nonce 2 mạng đang KHÁC NHAU!")
			fmt.Println("    Điều này sẽ dẫn đến 2 địa chỉ Factory sinh ra bị LỆCH NHAU.")
			fmt.Println("    Tốt nhất là hủy (n), tạo 1 ví mới tinh nạp sẵn phí gas rồi điền vào PK_DEPLOY_CC.")
			fmt.Print("   Tiếp tục ép Deploy? (y/n): ")
			var conf string
			fmt.Scanln(&conf)
			if conf != "y" {
				return
			}
		}

		// Read Factory Bytecode
		bcFile, err := os.ReadFile("byteCode/byteCode.json")
		if err != nil {
			fmt.Println("❌ byteCode/byteCode.json not found")
			return
		}
		var bcData BytecodeData
		json.Unmarshal(bcFile, &bcData)
		if bcData.PingpongFactory == "" {
			fmt.Println("❌ Không đọc được key 'pingpong_factory' JSON (Dành cho Factory) trong byteCode.json.")
			return
		}

		facCode := common.FromHex(strings.TrimPrefix(bcData.PingpongFactory, "0x"))

		fmt.Println("\n🚀 Triển khai Factory Móng lên Chain 1...")
		tx1, addr1, err1 := deployRawTx(client, deployPk, srcChainID, deployAddr, facCode)
		if err1 != nil {
			fmt.Printf("❌ %v\n", err1)
			return
		}
		waitForReceipt(client, tx1, "Deploy Factory Chain 1")

		fmt.Println("\n🚀 Triển khai Factory Móng lên Chain 2...")
		tx2, addr2, err2 := deployRawTx(destClient, deployPk, destChainID, deployAddr, facCode)
		if err2 != nil {
			fmt.Printf("❌ %v\n", err2)
			return
		}
		waitForReceipt(destClient, tx2, "Deploy Factory Chain 2")

		fmt.Printf("\n✅ Factory Chain 1: %s\n", addr1.Hex())
		fmt.Printf("✅ Factory Chain 2: %s\n", addr2.Hex())

		factoryAddr = addr1

		// Lưu factory vào JSON
		cfg := loadPingPongConfig()
		cfg.Factory = addr1.Hex()
		savePingPongConfig(cfg)
		fmt.Println("💾 Đã lưu địa chỉ Factory vào contract_pingpong.json")
	} else {
		factoryAddr = common.HexToAddress(factoryStr)
	}

	// 4. Deploy PingPong via CREATE2
	fmt.Printf("\n📌 Địa chỉ Factory đang dùng: %s\n", factoryAddr.Hex())
	fmt.Print("\n🏷️  Nhập mã Salt dạng uint (VD: 9999, mặc định 1234): ")
	var saltStr string
	fmt.Scanln(&saltStr)
	saltStr = strings.TrimSpace(saltStr)
	if saltStr == "" {
		saltStr = "1234"
	}
	saltInt, ok := new(big.Int).SetString(saltStr, 10)
	if !ok {
		fmt.Println("❌ Mã Salt không hợp lệ")
		return
	}
	var saltBytes32 [32]byte
	saltInt.FillBytes(saltBytes32[:])

	// build calldata: deployPingPong(bytes32)
	callData, err := facABI.Pack("deployPingPong", saltBytes32)
	if err != nil {
		fmt.Printf("❌ Pack deployPingPong lỗi: %v\n", err)
		return
	}

	fmt.Println("\n🔥 Bắn lệnh CREATE2 PingPong lên CẢ 2 MẠNG cùng lúc...")

	// Launch Chain 1
	txHash1, err := sendRawTx(client, deployPk, srcChainID, deployAddr, factoryAddr, big.NewInt(0), callData)
	if err != nil {
		fmt.Printf("❌ Chain 1 fail: %v\n", err)
	} else {
		fmt.Printf("📤 Chain 1 TX: %s\n", txHash1.Hex())
		rcp1, errRcp := waitForReceipt(client, txHash1, "CREATE2 PingPong C1")
		if errRcp == nil {
			found := false
			var createdAddr common.Address
			for _, log := range rcp1.Logs {
				if len(log.Topics) > 0 && log.Topics[0] == facABI.Events["Deployed"].ID {
					values, errData := facABI.Unpack("Deployed", log.Data)
					if errData == nil && len(values) >= 1 {
						if addr, ok := values[0].(common.Address); ok {
							fmt.Printf("   🎉 PingPong Chain 1: %s\n", addr.Hex())
							createdAddr = addr
							found = true
						}
					}
				}
			}
			if !found {
				fmt.Println("   ❓ Giao dịch thành công nhưng không thấy event Deployed.")
			} else {
				// Lưu địa chỉ PingPong
				cfg := loadPingPongConfig()
				cfg.PingPong = createdAddr.Hex()
				savePingPongConfig(cfg)
				fmt.Println("   💾 Đã lưu địa chỉ PingPong vào contract_pingpong.json")
			}
		}
	}

	// Launch Chain 2
	txHash2, err2 := sendRawTx(destClient, deployPk, destChainID, deployAddr, factoryAddr, big.NewInt(0), callData)
	if err2 != nil {
		fmt.Printf("❌ Chain 2 fail: %v\n", err2)
	} else {
		fmt.Printf("📤 Chain 2 TX: %s\n", txHash2.Hex())
		rcp2, errRcp := waitForReceipt(destClient, txHash2, "CREATE2 PingPong C2")
		if errRcp == nil {
			found := false
			for _, log := range rcp2.Logs {
				if len(log.Topics) > 0 && log.Topics[0] == facABI.Events["Deployed"].ID {
					values, errData := facABI.Unpack("Deployed", log.Data)
					if errData == nil && len(values) >= 1 {
						if addr, ok := values[0].(common.Address); ok {
							fmt.Printf("   🎉 PingPong Chain 2: %s\n", addr.Hex())
							found = true
						}
					}
				}
			}
			if !found {
				fmt.Println("   ❓ Giao dịch thành công nhưng không thấy event Deployed.")
			}
		}
	}

	fmt.Println("\n✅ Hoàn tất luồng CREATE2! Giờ bạn có thể qua Option 7 nhập địa chỉ này để bắt đầu đập bóng!")
}

// ─────────────────────────────────────────────────────────────────────────────
// SpamKeyInfo – struct để đọc generated_keys.json
// ─────────────────────────────────────────────────────────────────────────────
type SpamKeyInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
}

// ─────────────────────────────────────────────────────────────────────────────
// spamLockAndBridge – Đọc generated_keys.json, mỗi ví spam N giao dịch lockAndBridge
// Gửi concurrent, đợi receipt, tính TPS
// ─────────────────────────────────────────────────────────────────────────────
func spamLockAndBridge(
	client *ethclient.Client,
	chainID *big.Int,
	ccContract common.Address,
	parsedABI abi.ABI,
	recipient common.Address,
) {
	fmt.Println("\n🔥 SPAM lockAndBridge")
	fmt.Println("═════════════════════════════════════════")

	// Đọc keys file từ env PATH_KEY_SPAM
	keysPath := getEnv("PATH_KEY_SPAM", "../../../tool/test_tps/gen_spam_keys/generated_keys.json")
	// Nếu PATH_KEY_SPAM là thư mục, nối thêm generated_keys.json
	if strings.HasSuffix(keysPath, "/") {
		keysPath = keysPath + "generated_keys.json"
	}
	// Nếu file không tồn tại, thử thêm /generated_keys.json
	if _, err := os.Stat(keysPath); os.IsNotExist(err) {
		alt := keysPath + "/generated_keys.json"
		if _, err2 := os.Stat(alt); err2 == nil {
			keysPath = alt
		}
	}
	fmt.Printf("  📂 Keys file: %s\n", keysPath)

	keysData, err := os.ReadFile(keysPath)
	if err != nil {
		fmt.Printf("❌ Cannot read %s: %v\n", keysPath, err)
		return
	}

	var allKeys []SpamKeyInfo
	if err := json.Unmarshal(keysData, &allKeys); err != nil {
		fmt.Printf("❌ Cannot parse keys JSON: %v\n", err)
		return
	}
	fmt.Printf("  📋 Loaded %d keys from %s\n", len(allKeys), keysPath)

	// Số ví sử dụng
	fmt.Print("Số ví muốn dùng (Enter = all): ")
	var numWalletsStr string
	fmt.Scanln(&numWalletsStr)
	numWallets := len(allKeys)
	if numWalletsStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(numWalletsStr), 10); ok {
			if int(n.Int64()) < numWallets {
				numWallets = int(n.Int64())
			}
		}
	}

	// Số TX per ví
	fmt.Print("Số TX per ví (Enter = 1): ")
	var txPerWalletStr string
	fmt.Scanln(&txPerWalletStr)
	txPerWallet := 1
	if txPerWalletStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(txPerWalletStr), 10); ok {
			txPerWallet = int(n.Int64())
		}
	}

	// Amount
	fmt.Print("Amount ETH per TX (Enter = 0.001): ")
	var amountStr string
	fmt.Scanln(&amountStr)
	if amountStr == "" {
		amountStr = "0.001"
	}
	amountETH, ok := new(big.Float).SetString(amountStr)
	if !ok {
		fmt.Println("❌ Amount không hợp lệ")
		return
	}
	weiMul := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	amountWei, _ := new(big.Float).Mul(amountETH, weiMul).Int(nil)

	// Destination ID
	fmt.Print("DestinationId (Enter = 2): ")
	var destIdStr string
	fmt.Scanln(&destIdStr)
	if destIdStr == "" {
		destIdStr = "2"
	}
	destinationId, ok := new(big.Int).SetString(strings.TrimSpace(destIdStr), 10)
	if !ok {
		fmt.Println("❌ destinationId không hợp lệ")
		return
	}

	// Concurrency
	fmt.Print("Concurrency (goroutines, Enter = 10): ")
	var concStr string
	fmt.Scanln(&concStr)
	concurrency := 10
	if concStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(concStr), 10); ok {
			concurrency = int(n.Int64())
		}
	}

	// Number of rounds
	fmt.Print("Số lần đo (Enter = 1): ")
	var roundsStr string
	fmt.Scanln(&roundsStr)
	numRounds := 1
	if roundsStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(roundsStr), 10); ok && n.Int64() > 0 {
			numRounds = int(n.Int64())
		}
	}

	totalTx := numWallets * txPerWallet
	fmt.Printf("\n  📊 Plan: %d wallets × %d tx = %d total transactions × %d rounds\n", numWallets, txPerWallet, totalTx, numRounds)
	fmt.Printf("  💰 Amount: %s ETH per tx\n", amountStr)
	fmt.Printf("  🎯 Destination: %s\n", destinationId.String())
	fmt.Printf("  🔀 Concurrency: %d\n", concurrency)
	fmt.Printf("  📍 Contract: %s\n", ccContract.Hex())
	fmt.Printf("  📬 Recipient: %s\n", recipient.Hex())
	fmt.Print("\n  ⚡ Bắt đầu? (Enter = yes, n = no): ")
	var confirm string
	fmt.Scanln(&confirm)
	if strings.TrimSpace(confirm) == "n" {
		fmt.Println("  ❌ Cancelled")
		return
	}

	// ABI encode lockAndBridge(address recipient, uint256 destinationId)
	inputData, err := parsedABI.Pack("lockAndBridge", recipient, destinationId)
	if err != nil {
		fmt.Printf("❌ ABI pack error: %v\n", err)
		return
	}

	// Parse all private keys
	type WalletInfo struct {
		PrivKey *ecdsa.PrivateKey
		From    common.Address
	}
	wallets := make([]WalletInfo, 0, numWallets)
	for i := 0; i < numWallets; i++ {
		pk, err := crypto.HexToECDSA(allKeys[i].PrivateKey)
		if err != nil {
			fmt.Printf("  ⚠️ Skip key #%d: %v\n", i, err)
			continue
		}
		addr := crypto.PubkeyToAddress(pk.PublicKey)
		wallets = append(wallets, WalletInfo{PrivKey: pk, From: addr})
	}

	fmt.Printf("\n  🚀 Starting spam with %d wallets, %d rounds...\n\n", len(wallets), numRounds)

	var allRoundTPS []float64

	for round := 1; round <= numRounds; round++ {
		if numRounds > 1 {
			fmt.Printf("\n╔═══════════════════════════════════════════════════╗\n")
			fmt.Printf("║  🔄 ROUND %d / %d\n", round, numRounds)
			fmt.Printf("╚═══════════════════════════════════════════════════╝\n")
		}

		// ── Phase 1: Send all TXs ──────────────────────────
		type TxResult struct {
			From   common.Address
			TxHash common.Hash
			TxIdx  int
			Err    error
		}

		results := make([]TxResult, 0, totalTx)
		var resultsMu sync.Mutex
		var sendWg sync.WaitGroup
		sendSem := make(chan struct{}, concurrency)

		fmt.Println("  📤 Phase 1: Sending all transactions...")
		sendStart := time.Now()

		for _, w := range wallets {
			for txIdx := 0; txIdx < txPerWallet; txIdx++ {
				sendWg.Add(1)
				sendSem <- struct{}{}

				go func(wallet WalletInfo, txNum int) {
					defer sendWg.Done()
					defer func() { <-sendSem }()

					txHash, err := sendRawTx(client, wallet.PrivKey, chainID, wallet.From, ccContract, amountWei, inputData)
					resultsMu.Lock()
					results = append(results, TxResult{
						From:   wallet.From,
						TxHash: txHash,
						TxIdx:  txNum,
						Err:    err,
					})
					resultsMu.Unlock()

					if err != nil {
						log.Printf("❌ [%s tx#%d] send failed: %v", wallet.From.Hex()[:10], txNum, err)
					}
				}(w, txIdx)
			}
		}
		sendWg.Wait()
		sendDuration := time.Since(sendStart)

		sentOK := 0
		sentFail := 0
		for _, r := range results {
			if r.Err != nil {
				sentFail++
			} else {
				sentOK++
			}
		}
		fmt.Printf("\n  ✅ Phase 1 done: %d sent, %d failed in %s\n", sentOK, sentFail, sendDuration.Round(time.Millisecond))

		if sentOK == 0 {
			fmt.Println("  ❌ No transactions sent successfully, skipping confirm phase")
			return
		}

		// ── Phase 2: Wait for ALL receipts ──────────────────
		fmt.Printf("\n  📥 Phase 2: Waiting for %d receipts...\n", sentOK)

		var (
			confirmedCount int64
			failedCount    int64
			confirmWg      sync.WaitGroup
			confirmSem     = make(chan struct{}, concurrency*2) // more concurrency for polling
		)

		// Progress reporter
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					c := atomic.LoadInt64(&confirmedCount)
					f := atomic.LoadInt64(&failedCount)
					elapsed := time.Since(sendStart).Seconds()
					tps := float64(0)
					if elapsed > 0 {
						tps = float64(c) / elapsed
					}
					fmt.Printf("  📊 Progress: confirmed=%d failed=%d elapsed=%.1fs TPS=%.1f\n",
						c, f, elapsed, tps)
				}
			}
		}()

		for _, r := range results {
			if r.Err != nil {
				continue // skip failed sends
			}
			confirmWg.Add(1)
			confirmSem <- struct{}{}

			go func(res TxResult) {
				defer confirmWg.Done()
				defer func() { <-confirmSem }()

				receipt, err := waitForReceipt(client, res.TxHash, fmt.Sprintf("%s-tx%d", res.From.Hex()[:10], res.TxIdx))
				if err != nil {
					atomic.AddInt64(&failedCount, 1)
					log.Printf("❌ [%s tx#%d] receipt failed: %v", res.From.Hex()[:10], res.TxIdx, err)
					return
				}

				if receipt.Status == 1 {
					atomic.AddInt64(&confirmedCount, 1)
				} else {
					atomic.AddInt64(&failedCount, 1)
					log.Printf("⚠️ [%s tx#%d] reverted (status=%d, reason=%s)",
						res.From.Hex()[:10], res.TxIdx, receipt.Status, receipt.Return)
				}
			}(r)
		}

		confirmWg.Wait()
		close(done)
		totalDuration := time.Since(sendStart)

		// ── Round Report ────────────────────────────────────
		roundTPS := float64(0)
		if totalDuration.Seconds() > 0 {
			roundTPS = float64(confirmedCount) / totalDuration.Seconds()
		}
		sendTPS := float64(0)
		if sendDuration.Seconds() > 0 {
			sendTPS = float64(sentOK) / sendDuration.Seconds()
		}

		allRoundTPS = append(allRoundTPS, roundTPS)

		fmt.Println("\n═══════════════════════════════════════════════════")
		fmt.Printf("  📊 ROUND %d REPORT\n", round)
		fmt.Println("═══════════════════════════════════════════════════")
		fmt.Printf("  📤 Send phase     : %d sent in %s (%.1f tx/s)\n", sentOK, sendDuration.Round(time.Millisecond), sendTPS)
		fmt.Printf("  📥 Confirm phase  : %d confirmed, %d failed\n", confirmedCount, failedCount)
		fmt.Printf("  ─────────────────────────────────────────────────\n")
		fmt.Printf("  ⏱️  Total duration : %s (send → last confirm)\n", totalDuration.Round(time.Millisecond))
		fmt.Printf("  🔥 End-to-End TPS : %.2f tx/s\n", roundTPS)
		fmt.Printf("  📋 Wallets used   : %d\n", len(wallets))
		fmt.Printf("  📦 TX per wallet  : %d\n", txPerWallet)
		fmt.Println("═══════════════════════════════════════════════════")

	} // end round loop

	// ── Summary across all rounds ──────────────────────
	if numRounds > 1 {
		var minTPS, maxTPS, sumTPS float64
		minTPS = allRoundTPS[0]
		maxTPS = allRoundTPS[0]
		for _, t := range allRoundTPS {
			sumTPS += t
			if t < minTPS {
				minTPS = t
			}
			if t > maxTPS {
				maxTPS = t
			}
		}
		avgTPS := sumTPS / float64(len(allRoundTPS))

		fmt.Println("\n╔═══════════════════════════════════════════════════╗")
		fmt.Println("║  📊 BENCHMARK SUMMARY")
		fmt.Println("╠═══════════════════════════════════════════════════╣")
		fmt.Printf("║  🔄 Rounds         : %d\n", numRounds)
		fmt.Printf("║  📤 TXs per round  : %d\n", totalTx)
		fmt.Println("║  ─────────────────────────────────────────────────")
		for i, t := range allRoundTPS {
			fmt.Printf("║  Round %-2d TPS      : %.2f tx/s\n", i+1, t)
		}
		fmt.Println("║  ─────────────────────────────────────────────────")
		fmt.Printf("║  📉 Min TPS        : %.2f tx/s\n", minTPS)
		fmt.Printf("║  📈 Max TPS        : %.2f tx/s\n", maxTPS)
		fmt.Printf("║  📊 Avg TPS        : %.2f tx/s\n", avgTPS)
		fmt.Println("╚═══════════════════════════════════════════════════╝")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// spamCrossChainTPS – Spam lockAndBridge trên chain nguồn, check balance chain đích
// Flow: gửi N tx lockAndBridge → chờ receipt chain nguồn → poll balance chain đích
// ─────────────────────────────────────────────────────────────────────────────
func spamCrossChainTPS(
	client *ethclient.Client,
	chainID *big.Int,
	ccContract common.Address,
	parsedABI abi.ABI,
	recipient common.Address,
) {
	fmt.Println("\n🌉 CROSS-CHAIN TPS TEST")
	fmt.Println("═════════════════════════════════════════")

	// ── Destination chain RPC ────────────────────────
	destRPC := getEnv("REMOTE_HTTP", "")
	if destRPC == "" {
		fmt.Print("Nhập RPC URL chain đích (vd: http://192.168.1.233:8545): ")
		fmt.Scanln(&destRPC)
		destRPC = strings.TrimSpace(destRPC)
	}
	if destRPC == "" {
		fmt.Println("❌ REMOTE_HTTP is required")
		return
	}

	destClient, err := ethclient.Dial(destRPC)
	if err != nil {
		fmt.Printf("❌ Failed to connect to dest chain %s: %v\n", destRPC, err)
		return
	}
	defer destClient.Close()
	fmt.Printf("  ✅ Connected to dest chain: %s\n", destRPC)

	// Recipient sẽ là chính các ví trong danh sách.

	// ── Load keys ────────────────────────────────────
	keysPath := getEnv("PATH_KEY_SPAM", "../../../tool/test_tps/gen_spam_keys/generated_keys.json")
	if strings.HasSuffix(keysPath, "/") {
		keysPath = keysPath + "generated_keys.json"
	}
	if _, err := os.Stat(keysPath); os.IsNotExist(err) {
		alt := keysPath + "/generated_keys.json"
		if _, err2 := os.Stat(alt); err2 == nil {
			keysPath = alt
		}
	}
	keysData, err := os.ReadFile(keysPath)
	if err != nil {
		fmt.Printf("❌ Cannot read %s: %v\n", keysPath, err)
		return
	}
	var allKeys []SpamKeyInfo
	if err := json.Unmarshal(keysData, &allKeys); err != nil {
		fmt.Printf("❌ Cannot parse keys JSON: %v\n", err)
		return
	}
	fmt.Printf("  📋 Loaded %d keys\n", len(allKeys))

	// ── Parameters ───────────────────────────────────
	fmt.Print("Số ví muốn dùng (Enter = all): ")
	var numWalletsStr string
	fmt.Scanln(&numWalletsStr)
	numWallets := len(allKeys)
	if numWalletsStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(numWalletsStr), 10); ok {
			if int(n.Int64()) < numWallets {
				numWallets = int(n.Int64())
			}
		}
	}

	fmt.Print("Số TX per ví (Enter = 1): ")
	var txPerWalletStr string
	fmt.Scanln(&txPerWalletStr)
	txPerWallet := 1
	if txPerWalletStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(txPerWalletStr), 10); ok {
			txPerWallet = int(n.Int64())
		}
	}

	fmt.Print("Amount ETH per TX (Enter = 0.001): ")
	var amountStr string
	fmt.Scanln(&amountStr)
	if amountStr == "" {
		amountStr = "0.001"
	}
	amountETH, ok := new(big.Float).SetString(amountStr)
	if !ok {
		fmt.Println("❌ Amount không hợp lệ")
		return
	}
	weiMul := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	amountWei, _ := new(big.Float).Mul(amountETH, weiMul).Int(nil)

	fmt.Print("DestinationId (Enter = 2): ")
	var destIdStr string
	fmt.Scanln(&destIdStr)
	if destIdStr == "" {
		destIdStr = "2"
	}
	destinationId, ok := new(big.Int).SetString(strings.TrimSpace(destIdStr), 10)
	if !ok {
		fmt.Println("❌ destinationId không hợp lệ")
		return
	}

	fmt.Print("Concurrency (Enter = 10): ")
	var concStr string
	fmt.Scanln(&concStr)
	concurrency := 10
	if concStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(concStr), 10); ok {
			concurrency = int(n.Int64())
		}
	}

	fmt.Print("Timeout chờ balance chain đích (giây, Enter = 120): ")
	var timeoutStr string
	fmt.Scanln(&timeoutStr)
	balanceTimeout := 120 * time.Second
	if timeoutStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(timeoutStr), 10); ok {
			balanceTimeout = time.Duration(n.Int64()) * time.Second
		}
	}

	// Number of rounds
	fmt.Print("Số lần đo (Enter = 1): ")
	var roundsStr string
	fmt.Scanln(&roundsStr)
	numRounds := 1
	if roundsStr != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(roundsStr), 10); ok && n.Int64() > 0 {
			numRounds = int(n.Int64())
		}
	}

	totalTx := numWallets * txPerWallet
	fmt.Printf("\n  📊 Plan: %d wallets × %d tx = %d total transactions × %d rounds\n", numWallets, txPerWallet, totalTx, numRounds)
	fmt.Printf("  💰 Amount: %s ETH per tx\n", amountStr)
	fmt.Printf("  🎯 Destination ID: %s\n", destinationId.String())
	fmt.Printf("  ⏱️  Balance timeout: %s\n", balanceTimeout)

	fmt.Print("\n  ⚡ Bắt đầu? (Enter = yes, n = no): ")
	var confirm string
	fmt.Scanln(&confirm)
	if strings.TrimSpace(confirm) == "n" {
		fmt.Println("  ❌ Cancelled")
		return
	}

	// ── Parse keys ───────────────────────────────────
	type WalletInfo struct {
		PrivKey        *ecdsa.PrivateKey
		From           common.Address
		InitialBalance *big.Int
	}
	wallets := make([]WalletInfo, 0, numWallets)
	for i := 0; i < numWallets; i++ {
		pk, err := crypto.HexToECDSA(allKeys[i].PrivateKey)
		if err != nil {
			continue
		}
		addr := crypto.PubkeyToAddress(pk.PublicKey)
		wallets = append(wallets, WalletInfo{
			PrivKey:        pk,
			From:           addr,
			InitialBalance: big.NewInt(0),
		})
	}

	var allSourceTPS []float64
	var allE2ETPS []float64

	for round := 1; round <= numRounds; round++ {
		if numRounds > 1 {
			fmt.Printf("\n╔═══════════════════════════════════════════════════╗\n")
			fmt.Printf("║  🔄 ROUND %d / %d\n", round, numRounds)
			fmt.Printf("╚═══════════════════════════════════════════════════╝\n")
		}

		// ── Fetch Initial Balances ───────────────────────
		fmt.Println("\n  📥 Fetching initial balance on dest chain for wallets...")
		var initBalWg sync.WaitGroup
		initBalSem := make(chan struct{}, 50)
		for i := range wallets {
			initBalWg.Add(1)
			initBalSem <- struct{}{}
			go func(w *WalletInfo) {
				defer initBalWg.Done()
				defer func() { <-initBalSem }()
				bal, err := destClient.BalanceAt(context.Background(), w.From, nil)
				if err == nil {
					w.InitialBalance = bal
				}
			}(&wallets[i])
		}
		initBalWg.Wait()
		fmt.Println("  ✅ Initial balances fetched.")

		// ═══════════════════════════════════════════════════
		// Phase 1: Send all lockAndBridge TXs on source chain
		// ═══════════════════════════════════════════════════
		type TxResult struct {
			TxHash common.Hash
			Err    error
		}
		results := make([]TxResult, 0, totalTx)
		var resultsMu sync.Mutex
		var sendWg sync.WaitGroup
		sendSem := make(chan struct{}, concurrency)

		fmt.Println("\n  📤 Phase 1: Sending lockAndBridge on source chain...")
		sendStart := time.Now()

		for i := range wallets {
			for txIdx := 0; txIdx < txPerWallet; txIdx++ {
				sendWg.Add(1)
				sendSem <- struct{}{}

				go func(wallet *WalletInfo) {
					defer sendWg.Done()
					defer func() { <-sendSem }()

					inputData, err := parsedABI.Pack("lockAndBridge", wallet.From, destinationId)
					if err != nil {
						log.Printf("❌ [%s] ABI pack error: %v\n", wallet.From.Hex()[:10], err)
						return
					}

					txHash, err := sendRawTx(client, wallet.PrivKey, chainID, wallet.From, ccContract, amountWei, inputData)
					resultsMu.Lock()
					results = append(results, TxResult{TxHash: txHash, Err: err})
					resultsMu.Unlock()
					if err != nil {
						log.Printf("❌ [%s] send failed: %v", wallet.From.Hex()[:10], err)
					}
				}(&wallets[i])
			}
		}
		sendWg.Wait()
		sendDuration := time.Since(sendStart)

		sentOK := 0
		for _, r := range results {
			if r.Err == nil {
				sentOK++
			}
		}
		fmt.Printf("  ✅ Phase 1: %d sent in %s\n", sentOK, sendDuration.Round(time.Millisecond))

		if sentOK == 0 {
			fmt.Println("  ❌ No transactions sent")
			return
		}

		// ═══════════════════════════════════════════════════
		// Phase 2: Wait for receipts on source chain
		// ═══════════════════════════════════════════════════
		fmt.Printf("\n  📥 Phase 2: Waiting for %d receipts on source chain...\n", sentOK)
		var confirmedCount int64
		var confirmWg sync.WaitGroup
		confirmSem := make(chan struct{}, concurrency*2)

		for _, r := range results {
			if r.Err != nil {
				continue
			}
			confirmWg.Add(1)
			confirmSem <- struct{}{}

			go func(txHash common.Hash) {
				defer confirmWg.Done()
				defer func() { <-confirmSem }()

				receipt, err := waitForReceipt(client, txHash, "lockAndBridge")
				if err == nil && receipt.Status == 1 {
					atomic.AddInt64(&confirmedCount, 1)
				}
			}(r.TxHash)
		}
		confirmWg.Wait()
		sourceConfirmDuration := time.Since(sendStart)
		fmt.Printf("  ✅ Phase 2: %d confirmed on source in %s\n",
			confirmedCount, sourceConfirmDuration.Round(time.Millisecond))

		// ═══════════════════════════════════════════════════
		// Phase 3: Poll balance on dest chain
		// ═══════════════════════════════════════════════════
		fmt.Printf("\n  🌉 Phase 3: Polling balance on dest chain (timeout=%s, interval=5s)...\n", balanceTimeout)

		expectedPerWallet := new(big.Int).Mul(amountWei, big.NewInt(int64(txPerWallet)))
		fmt.Printf("  🎯 Waiting for each wallet to receive %s ETH\n",
			new(big.Float).Quo(new(big.Float).SetInt(expectedPerWallet), big.NewFloat(1e18)).Text('f', 8))

		pollStart := time.Now()
		deadline := time.After(balanceTimeout)
		ticker := time.NewTicker(1 * time.Second) // Giãn cách 1 giây để tránh DDoS RPC
		defer ticker.Stop()

		// Theo dõi danh sách các ví CHƯA nhận được tiền để chỉ check những ví này
		unreachedWallets := make([]*WalletInfo, len(wallets))
		for i := range wallets {
			unreachedWallets[i] = &wallets[i]
		}
		reachedCount := 0

		crossChainDone := false
		var finalReached walletsReachedInfo // for report
		finalReached.total = numWallets

		for {
			select {
			case <-deadline:
				fmt.Printf("\n  ⏱️  TIMEOUT! Chưa đủ %d ví nhận được tiền\n", numWallets)
				filename := "failed_wallets.txt"
				f, err := os.Create(filename)
				if err != nil {
					fmt.Printf("❌ Không thể tạo file %s: %v\n", filename, err)
				} else {
					defer f.Close()
					fmt.Fprintf(f, "📋 Danh sách ví chưa đủ tiền (Timeout tại %s):\n", time.Now().Format("2006-01-02 15:04:05"))
					for _, w := range unreachedWallets {
						currentBal, _ := destClient.BalanceAt(context.Background(), w.From, nil)
						targetBal := new(big.Int).Add(w.InitialBalance, expectedPerWallet)
						fmt.Fprintf(f, "Wallet: %s | Hiện tại: %s ETH | Mong muốn: %s ETH\n",
							w.From.Hex(),
							new(big.Float).Quo(new(big.Float).SetInt(currentBal), big.NewFloat(1e18)).Text('f', 8),
							new(big.Float).Quo(new(big.Float).SetInt(targetBal), big.NewFloat(1e18)).Text('f', 8))
					}
					fmt.Printf("  📄 Danh sách ví lỗi đã được ghi vào file: %s\n", filename)
				}
				fmt.Println("\n  ❌ Dừng test do timeout.")
				return
			case <-ticker.C:
				var checkWg sync.WaitGroup
				checkSem := make(chan struct{}, 50) // 50 worker
				var mu sync.Mutex
				var stillUnreached []*WalletInfo

				for _, w := range unreachedWallets {
					checkWg.Add(1)
					checkSem <- struct{}{}
					go func(w *WalletInfo) {
						defer checkWg.Done()
						defer func() { <-checkSem }()
						bal, err := destClient.BalanceAt(context.Background(), w.From, nil)
						isReached := false
						if err == nil {
							diff := new(big.Int).Sub(bal, w.InitialBalance)
							if diff.Cmp(expectedPerWallet) >= 0 {
								isReached = true
							}
						}

						mu.Lock()
						if isReached {
							reachedCount++
						} else {
							stillUnreached = append(stillUnreached, w)
						}
						mu.Unlock()
					}(w)
				}
				checkWg.Wait()
				unreachedWallets = stillUnreached

				finalReached.reached = reachedCount
				progress := float64(reachedCount) * 100 / float64(numWallets)
				elapsed := time.Since(pollStart)
				fmt.Printf("  📊 Reached wallets: %d/%d (%.1f%%) elapsed=%s\n",
					reachedCount, numWallets, progress, elapsed.Round(time.Millisecond))

				if reachedCount >= numWallets {
					crossChainDone = true
				} else {
					continue
				}
			}
			break
		}

		totalDuration := time.Since(sendStart)
		crossChainDuration := time.Since(pollStart)

		// ═══════════════════════════════════════════════════
		// Report
		// ═══════════════════════════════════════════════════
		e2eTPS := float64(0)
		if totalDuration.Seconds() > 0 {
			e2eTPS = float64(confirmedCount) / totalDuration.Seconds()
		}
		sourceTPS := float64(0)
		if sourceConfirmDuration.Seconds() > 0 {
			sourceTPS = float64(confirmedCount) / sourceConfirmDuration.Seconds()
		}

		fmt.Println("\n╔═══════════════════════════════════════════════════╗")
		fmt.Println("║  🌉 CROSS-CHAIN TPS REPORT")
		fmt.Println("╠═══════════════════════════════════════════════════╣")
		fmt.Printf("║  📤 Source chain     : %d tx sent, %d confirmed\n", sentOK, confirmedCount)
		fmt.Printf("║  ⏱️  Send duration    : %s\n", sendDuration.Round(time.Millisecond))
		fmt.Printf("║  ⏱️  Source confirm   : %s\n", sourceConfirmDuration.Round(time.Millisecond))
		fmt.Printf("║  🔥 Source TPS       : %.2f tx/s\n", sourceTPS)
		fmt.Println("║  ─────────────────────────────────────────────────")
		fmt.Printf("║  💰 Reached Wallets  : %d / %d (%.1f%%)\n", finalReached.reached, finalReached.total, float64(finalReached.reached)*100/float64(finalReached.total))
		if crossChainDone {
			fmt.Printf("║  ✅ Cross-chain      : COMPLETE in %s\n", crossChainDuration.Round(time.Millisecond))
		} else {
			fmt.Printf("║  ❌ Cross-chain      : INCOMPLETE (timeout)\n")
		}
		fmt.Println("║  ─────────────────────────────────────────────────")
		fmt.Printf("║  ⏱️  Total E2E        : %s\n", totalDuration.Round(time.Millisecond))
		fmt.Printf("║  🌉 E2E TPS          : %.2f tx/s\n", e2eTPS)
		fmt.Println("╚═══════════════════════════════════════════════════╝")

		allSourceTPS = append(allSourceTPS, sourceTPS)
		allE2ETPS = append(allE2ETPS, e2eTPS)
	}

	// ── Summary across all rounds ──────────────────────
	if numRounds > 1 {
		var minSTPS, maxSTPS, sumSTPS float64
		var minE2E, maxE2E, sumE2E float64

		minSTPS = allSourceTPS[0]
		maxSTPS = allSourceTPS[0]
		minE2E = allE2ETPS[0]
		maxE2E = allE2ETPS[0]

		for i := 0; i < numRounds; i++ {
			s := allSourceTPS[i]
			sumSTPS += s
			if s < minSTPS {
				minSTPS = s
			}
			if s > maxSTPS {
				maxSTPS = s
			}

			e := allE2ETPS[i]
			sumE2E += e
			if e < minE2E {
				minE2E = e
			}
			if e > maxE2E {
				maxE2E = e
			}
		}

		avgSTPS := sumSTPS / float64(numRounds)
		avgE2E := sumE2E / float64(numRounds)

		fmt.Println("\n╔═══════════════════════════════════════════════════╗")
		fmt.Println("║  📊 CROSS-CHAIN BENCHMARK SUMMARY")
		fmt.Println("╠═══════════════════════════════════════════════════╣")
		fmt.Printf("║  🔄 Rounds         : %d\n", numRounds)
		fmt.Printf("║  📤 TXs per round  : %d\n", totalTx)
		fmt.Println("║  ─────────────────────────────────────────────────")
		for i := 0; i < numRounds; i++ {
			fmt.Printf("║  Round %-2d Source TPS : %7.2f tx/s | E2E TPS: %7.2f tx/s\n", i+1, allSourceTPS[i], allE2ETPS[i])
		}
		fmt.Println("║  ─────────────────────────────────────────────────")
		fmt.Printf("║  📉 Min Source TPS : %.2f tx/s\n", minSTPS)
		fmt.Printf("║  📈 Max Source TPS : %.2f tx/s\n", maxSTPS)
		fmt.Printf("║  📊 Avg Source TPS : %.2f tx/s\n", avgSTPS)
		fmt.Println("║  ─────────────────────────────────────────────────")
		fmt.Printf("║  📉 Min E2E TPS    : %.2f tx/s\n", minE2E)
		fmt.Printf("║  📈 Max E2E TPS    : %.2f tx/s\n", maxE2E)
		fmt.Printf("║  📊 Avg E2E TPS    : %.2f tx/s\n", avgE2E)
		fmt.Println("╚═══════════════════════════════════════════════════╝")
	}
}

type walletsReachedInfo struct {
	total   int
	reached int
}
