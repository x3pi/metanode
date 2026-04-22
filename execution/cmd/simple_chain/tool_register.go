package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/tcp_trans"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// ===== CONFIGURATION - COPIED FROM add_validator_node4/main.go =====

// Validation contract address
var VALIDATION_CONTRACT = mt_common.VALIDATOR_CONTRACT_ADDRESS

// Node4 info từ committee.json (Updated by script)
var node4Info = struct {
	Address      string // P2P address
	Hostname     string
	AuthorityKey string // BLS key (authority_key)
	ProtocolKey  string // Ed25519 (protocol_key)
	NetworkKey   string // Ed25519 (network_key)
}{
	Address:      "/ip4/127.0.0.1/tcp/9004",
	Hostname:     "node-4",
	AuthorityKey: "iILbjttIJ8ITwpm1gPEuJqCyRc5ygkwneJLCE37WL1Xv7dV5i7nCrxvF/4CcUpmWBjFuV0UKepBaW8SKBwQsF28u6+dSOUfZgmUHWHyqNnd6F688jOrQ7wGgv2LXH6GP",
	ProtocolKey:  "y4J4NBRQWmDkjz8YTZaovkwzwhMiUtktnqNARUJFcVA=",
	NetworkKey:   "h/FSKuXfXLlWjUNhq8jxGDgZTgRsCJKbfdTs+/Dyg2Y=",
}

// Validation ABI cho registerValidator
// Validation ABI cho registerValidator và view functions
const ValidationABI = `[
	{
		"name": "registerValidator",
		"type": "function",
		"inputs": [
			{"name": "primaryAddress", "type": "string"},
			{"name": "workerAddress", "type": "string"},
			{"name": "p2pAddress", "type": "string"},
			{"name": "name", "type": "string"},
			{"name": "description", "type": "string"},
			{"name": "website", "type": "string"},
			{"name": "image", "type": "string"},
			{"name": "commissionRate", "type": "uint64"},
			{"name": "minSelfDelegation", "type": "uint256"}
		]
	},
    {
        "name": "getValidatorCount",
        "type": "function",
        "inputs": [],
        "outputs": [{"name": "", "type": "uint256"}],
        "stateMutability": "view"
    },
    {
        "name": "validatorAddresses",
        "type": "function",
        "inputs": [{"name": "", "type": "uint256"}],
        "outputs": [{"name": "", "type": "address"}],
        "stateMutability": "view"
    },
    {
        "name": "validators",
        "type": "function",
        "inputs": [{"name": "", "type": "address"}],
        "outputs": [
            {"name": "owner", "type": "address"},
            {"name": "primaryAddress", "type": "string"},
            {"name": "workerAddress", "type": "string"},
            {"name": "p2pAddress", "type": "string"},
            {"name": "name", "type": "string"},
            {"name": "description", "type": "string"},
            {"name": "website", "type": "string"},
            {"name": "image", "type": "string"},
            {"name": "commissionRate", "type": "uint64"},
            {"name": "minSelfDelegation", "type": "uint256"},
            {"name": "totalStakedAmount", "type": "uint256"},
            {"name": "accumulatedRewardsPerShare", "type": "uint256"}
        ],
        "stateMutability": "view"
    }
]`

// runRegisterValidator executes the registration logic
func runRegisterValidator(configPath string) {
	fmt.Println("=== Register Validator Node-4 (Integrated Mode) ===")
	fmt.Println()

	// 1. Load config
	cfg, err := loadClientConfig(configPath)
	if err != nil {
		log.Fatalf("❌ Không thể load config: %v", err)
	}
	fmt.Printf("✅ Loaded config from: %s\n", configPath)

	// 2. Tạo TCP client kết nối đến Go Sub node
	tcpClient, err := client.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌ Không thể tạo client: %v", err)
	}
	defer tcpClient.Close()
	fmt.Printf("✅ Connected to parent node at: %s\n", cfg.ParentConnectionAddress)

	// 3. Lấy from address từ keypair trong config (BLS private key)
	fromAddress := cfg.Address()
	fmt.Printf("✅ From address: %s\n", fromAddress.Hex())

	// Init ABI
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		log.Fatalf("❌ Lỗi parse ABI: %v", err)
	}

	// --- PRINT VALIDATORS BEFORE REGISTRATION ---
	printValidators(tcpClient, fromAddress, parsedABI)

	// 4. Encode calldata cho registerValidator
	// Chuẩn bị tham số
	primaryAddress := node4Info.Address
	workerAddress := fromAddress.Hex()
	p2pAddress := node4Info.Address
	name := node4Info.Hostname
	description := "Node 4 - Full Sync Validator"
	website := ""
	image := ""
	commissionRate := uint64(1000)     // 10%
	minSelfDelegation := big.NewInt(0) // Không yêu cầu self-delegation ban đầu

	inputData, err := parsedABI.Pack("registerValidator",
		primaryAddress,
		workerAddress,
		p2pAddress,
		name,
		description,
		website,
		image,
		commissionRate,
		minSelfDelegation,
	)
	if err != nil {
		log.Fatalf("❌ Lỗi encode calldata: %v", err)
	}

	// Tạo CallData struct
	callData := transaction.NewCallData(inputData)
	bData, err := callData.Marshal()
	if err != nil {
		log.Fatalf("❌ Lỗi marshal calldata: %v", err)
	}
	fmt.Printf("✅ Calldata prepared\n")

	// 5. Hiển thị thông tin
	fmt.Println()
	fmt.Println("=== Transaction Details ===")
	fmt.Printf("From:     %s\n", fromAddress.Hex())
	fmt.Printf("To:       %s (Validation Contract)\n", VALIDATION_CONTRACT.Hex())
	fmt.Println()
	fmt.Println("Validator Info:")
	fmt.Printf("  Name:        %s\n", name)
	fmt.Printf("  P2P Address: %s\n", primaryAddress)
	fmt.Printf("  Commission:  %.2f%%\n", float64(commissionRate)/100)
	fmt.Println()

	// 6. Gửi transaction qua TCP client
	maxGas := uint64(300000)
	maxGasPrice := uint64(10000000)
	maxTimeUse := uint64(120) // INCREASED TIMEOUT to 120s

	fmt.Println("📤 Đang gửi transaction lên chain...")
	receipt, err := tcp_trans.SendTransactionWithDeviceKey(
		tcpClient,
		fromAddress,
		VALIDATION_CONTRACT,
		big.NewInt(0), // amount = 0
		bData,
		[]common.Address{}, // related addresses
		maxGas,
		maxGasPrice,
		maxTimeUse,
	)
	if err != nil {
		// Log error but try to print post-state anyway
		log.Printf("❌ Lỗi gửi transaction: %v", err)
		fmt.Println("⚠️  Kiểm tra lại danh sách validator để xem transaction đã được xử lý chưa...")
	} else {
		fmt.Println()
		fmt.Printf("✅ Transaction đã gửi thành công!\n")
		fmt.Printf("   TX Hash: %s\n", receipt.TransactionHash().Hex())
		fmt.Printf("   Status: %v\n", receipt.Status())
		fmt.Println()
		fmt.Println("✅ Node-4 đã được đăng ký làm validator!")
	}

	// --- PRINT VALIDATORS AFTER REGISTRATION ---
	printValidators(tcpClient, fromAddress, parsedABI)
}

func printValidators(tcpClient *client.Client, fromAddress common.Address, parsedABI abi.ABI) {
	fmt.Println("\n🔍 Calling Contract to get Validator List...")

	// 1. Get Count using ReadTransaction
	// Call getValidatorCount
	data, err := parsedABI.Pack("getValidatorCount")
	if err != nil {
		fmt.Printf("⚠️  Failed to pack getValidatorCount: %v\n", err)
		return
	}

	callData := transaction.NewCallData(data)
	bData, err := callData.Marshal()
	if err != nil {
		fmt.Printf("⚠️  Failed to marshal callData: %v\n", err)
		return
	}

	// ReadTransaction params: from, to, amount, data, relatedAddress, gas, gasPrice, timeUse
	maxGas := uint64(5000000)
	maxGasPrice := uint64(10000000)

	// Note: ReadTransaction returns a Receipt in the current client implementations typically
	// Check if ReadTransaction is exported in client.Client
	receipt, err := tcpClient.ReadTransaction(
		fromAddress,
		VALIDATION_CONTRACT,
		big.NewInt(0),
		bData,
		[]common.Address{},
		maxGas,
		maxGasPrice,
		60,
	)

	if err != nil {
		fmt.Printf("⚠️  Failed to call getValidatorCount: %v\n", err)
		return
	}

	// Unpack result
	returnData := receipt.Return()
	if len(returnData) == 0 {
		fmt.Println("⚠️  Return data empty from getValidatorCount")
		return
	}

	var count *big.Int
	// Try flexible unpacking
	res, err2 := parsedABI.Unpack("getValidatorCount", returnData)
	if err2 != nil {
		fmt.Printf("⚠️  Failed to unpack result: %v\n", err2)
		return
	}
	if len(res) > 0 {
		count = res[0].(*big.Int)
	}

	if count == nil {
		fmt.Println("⚠️  Could not retrieve validator count")
		return
	}

	fmt.Printf("📊 Current Validator Count: %d\n", count.Int64())
	fmt.Println("---------------------------------------------------")
	fmt.Printf("%-5s | %-20s | %-42s\n", "Index", "Name", "Address")
	fmt.Println("---------------------------------------------------")

	// Loop to get addresses
	cnt := int(count.Int64())
	for i := 0; i < cnt; i++ {
		// Call validatorAddresses(i)
		data, _ := parsedABI.Pack("validatorAddresses", big.NewInt(int64(i)))
		callData := transaction.NewCallData(data)
		bData, _ := callData.Marshal()

		receipt, err := tcpClient.ReadTransaction(fromAddress, VALIDATION_CONTRACT, big.NewInt(0), bData, []common.Address{}, maxGas, maxGasPrice, 60)
		if err != nil {
			fmt.Printf("⚠️  Failed to get address at index %d\n", i)
			continue
		}

		var valAddr common.Address
		res, err := parsedABI.Unpack("validatorAddresses", receipt.Return())
		if err == nil && len(res) > 0 {
			valAddr = res[0].(common.Address)
		} else {
			continue
		}

		// Call validators(valAddr) to get detail
		data2, _ := parsedABI.Pack("validators", valAddr)
		callData2 := transaction.NewCallData(data2)
		bData2, _ := callData2.Marshal()

		receipt2, err := tcpClient.ReadTransaction(fromAddress, VALIDATION_CONTRACT, big.NewInt(0), bData2, []common.Address{}, maxGas, maxGasPrice, 60)
		if err != nil {
			fmt.Printf("%-5d | %-20s | %s\n", i, "Error fetching info", valAddr.Hex())
			continue
		}

		// Output structure matches ABI: owner, primary, worker, p2p, name...
		res2, err := parsedABI.Unpack("validators", receipt2.Return())
		if err == nil && len(res2) >= 5 {
			name := res2[4].(string)
			fmt.Printf("%-5d | %-20s | %s\n", i, name, valAddr.Hex())
		} else {
			fmt.Printf("%-5d | %-20s | %s\n", i, "Unknown", valAddr.Hex())
		}
	}
	fmt.Println("---------------------------------------------------")
}

// loadClientConfig loads the client config directly to *ClientConfig type
func loadClientConfig(configPath string) (*tcp_config.ClientConfig, error) {
	config := &tcp_config.ClientConfig{}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, config)
	if err != nil {
		return nil, err
	}
	return config, nil
}

func runGetAddress(privateKeyHex string) {
	bytes := common.FromHex(privateKeyHex)
	kp := bls.NewKeyPair(bytes)
	fmt.Printf("%s", kp.Address().Hex())
}
