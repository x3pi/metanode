package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// ============================================================
// Configuration - THAY ĐỔI THEO CẤU HÌNH CỦA BẠN
// ============================================================

const (
	RPC_HTTP_URL = "http://localhost:8545" // RPC proxy endpoint
	RPC_WS_URL   = "http://localhost:8545" // Query endpoint
	CHAIN_ID     = 991
)

// Node 4 default configuration
var defaultNode4 = struct {
	PrivateKey string
	Name       string
}{
	// Private key for account 0xa87c6FD018Da82a52158B0328D61BAc29b556e86 (Node 4)
	PrivateKey: "6c8489f6f86fea58b26e34c8c37e13e5993651f09f5f96739d9febf65aded718",
	Name:       "node-4",
}

// ============================================================
// ABI Definition
// ============================================================

const ValidationABI = `[
	{
		"name": "deregisterValidator",
		"type": "function",
		"inputs": []
	},
	{
		"name": "undelegate",
		"type": "function", 
		"inputs": [
			{"name": "_validatorAddress", "type": "address"},
			{"name": "_amount", "type": "uint256"}
		]
	},
	{
		"inputs": [{"internalType": "address", "name": "", "type": "address"}],
		"name": "validators",
		"outputs": [
			{"internalType": "address", "name": "owner", "type": "address"},
			{"internalType": "string", "name": "primaryAddress", "type": "string"},
			{"internalType": "string", "name": "workerAddress", "type": "string"},
			{"internalType": "string", "name": "p2pAddress", "type": "string"},
			{"internalType": "string", "name": "name", "type": "string"},
			{"internalType": "string", "name": "description", "type": "string"},
			{"internalType": "string", "name": "website", "type": "string"},
			{"internalType": "string", "name": "image", "type": "string"},
			{"internalType": "uint256", "name": "commissionRate", "type": "uint256"},
			{"internalType": "uint256", "name": "minSelfDelegation", "type": "uint256"},
			{"internalType": "uint256", "name": "totalStakedAmount", "type": "uint256"},
			{"internalType": "uint256", "name": "accumulatedRewardsPerShare", "type": "uint256"},
			{"internalType": "string", "name": "hostname", "type": "string"},
			{"internalType": "string", "name": "authority_key", "type": "string"},
			{"internalType": "string", "name": "protocol_key", "type": "string"},
			{"internalType": "string", "name": "network_key", "type": "string"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getValidatorCount",
		"outputs": [{"internalType": "uint256", "name": "", "type": "uint256"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "uint256", "name": "", "type": "uint256"}],
		"name": "validatorAddresses",
		"outputs": [{"internalType": "address", "name": "", "type": "address"}],
		"stateMutability": "view",
		"type": "function"
	}
]`

// ============================================================
// Structs
// ============================================================

type RPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	Id      int           `json:"id"`
}

type RPCResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
	Id      int             `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ============================================================
// Main
// ============================================================

func main() {
	// Command line flags
	privateKey := flag.String("key", defaultNode4.PrivateKey, "Private key (hex, without 0x)")
	dryRun := flag.Bool("dry-run", false, "Only encode calldata, don't send transaction")
	skipUndelegate := flag.Bool("skip-undelegate", false, "Skip undelegate step (if you already undelegated)")

	flag.Parse()

	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║        DEREGISTER VALIDATOR TOOL (Node 4)                 ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()

	// 1. Load private key and get address
	privKey, err := crypto.HexToECDSA(*privateKey)
	if err != nil {
		fmt.Printf("❌ Lỗi load private key: %v\n", err)
		return
	}
	publicKey := privKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		fmt.Println("❌ Không thể cast public key to ECDSA")
		return
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	fmt.Printf("📍 Wallet Address: %s\n", fromAddress.Hex())
	fmt.Printf("📍 Contract:       %s\n", mt_common.VALIDATOR_CONTRACT_ADDRESS.Hex())
	fmt.Println()

	// 2. Check if validator is registered before deregistering
	fmt.Println("🔍 Kiểm tra trạng thái validator trước khi hủy đăng ký...")
	isRegistered := checkValidatorRegistration(fromAddress)
	if !isRegistered {
		fmt.Println()
		fmt.Println("⚠️  Validator chưa được đăng ký hoặc đã bị hủy trước đó.")
		fmt.Println("   Không cần thực hiện hủy đăng ký.")
		return
	}

	// 3. Check validator stake
	fmt.Println()
	fmt.Println("🔍 Kiểm tra stake của validator...")
	stakeAmount := getValidatorStake(fromAddress)
	if stakeAmount != nil && stakeAmount.Sign() > 0 {
		fmt.Printf("   💰 Stake hiện tại: %s wei\n", stakeAmount.String())
		fmt.Println()
		fmt.Println("⚠️  QUAN TRỌNG: Validator vẫn còn stake tokens!")
		fmt.Println("   Theo smart contract, phải rút hết stake trước khi hủy đăng ký.")
		fmt.Println()

		if *skipUndelegate {
			fmt.Println("   ⏭️  Bỏ qua bước undelegate (--skip-undelegate)")
		} else if *dryRun {
			fmt.Println("   📦 DRY-RUN: Sẽ cần undelegate trước khi deregister")
		} else {
			// Step 3a: Undelegate all stake
			fmt.Println("═══════════════════════════════════════════════════════════")
			fmt.Println("📌 BƯỚC 1: Rút hết stake (undelegate)...")
			fmt.Println("═══════════════════════════════════════════════════════════")
			fmt.Println()

			txHash, err := sendUndelegateTransaction(*privateKey, fromAddress, stakeAmount)
			if err != nil {
				fmt.Printf("❌ Lỗi undelegate: %v\n", err)
				return
			}
			fmt.Printf("   ✅ Undelegate TX Hash: %s\n", txHash)

			fmt.Println()
			fmt.Println("⏳ Đợi 5 giây để undelegate transaction được xác nhận...")
			time.Sleep(5 * time.Second)

			// Verify stake is now 0
			newStake := getValidatorStake(fromAddress)
			if newStake != nil && newStake.Sign() > 0 {
				fmt.Printf("⚠️  Vẫn còn stake: %s wei. Có thể cần đợi thêm.\n", newStake.String())
				fmt.Println("   Chạy lại tool sau vài block hoặc sử dụng --skip-undelegate")
				return
			}
			fmt.Println("   ✅ Đã rút hết stake thành công!")
			fmt.Println()
		}
	} else {
		fmt.Println("   ✅ Validator không còn stake, có thể hủy đăng ký.")
	}

	// 4. Encode calldata for deregisterValidator()
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("📌 BƯỚC 2: Hủy đăng ký validator (deregister)...")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()

	inputData, err := encodeDeregisterValidator()
	if err != nil {
		fmt.Printf("❌ Lỗi encode calldata: %v\n", err)
		return
	}

	fmt.Println("🔧 Calldata đã encode:")
	fmt.Printf("   %s\n", hexutil.Encode(inputData))
	fmt.Printf("   (Total: %d bytes)\n", len(inputData))
	fmt.Println()

	if *dryRun {
		fmt.Println("⚠️  DRY-RUN mode: Không gửi transaction")
		fmt.Printf("📦 Full Calldata:\n%s\n", hexutil.Encode(inputData))
		return
	}

	// 5. Send deregistration transaction
	fmt.Println("📤 Đang gửi transaction hủy đăng ký validator...")
	txHash, err := sendDeregisterTransaction(*privateKey, inputData)
	if err != nil {
		fmt.Printf("❌ Lỗi gửi transaction: %v\n", err)
		return
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("✅ DEREGISTRATION TRANSACTION ĐÃ GỬI THÀNH CÔNG!\n")
	fmt.Printf("   TX Hash: %s\n", txHash)
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()

	// 6. Wait for deregistration to be confirmed
	fmt.Println("⏳ Đợi 3 giây để transaction hủy đăng ký được xác nhận...")
	time.Sleep(3 * time.Second)

	fmt.Println("\n🔍 Kiểm tra danh sách validators sau khi hủy đăng ký...")
	verifyDeregistration(fromAddress)
}

// ============================================================
// Undelegate Functions
// ============================================================

func getValidatorStake(validatorAddr common.Address) *big.Int {
	client, err := rpc.Dial(RPC_WS_URL)
	if err != nil {
		fmt.Printf("❌ Lỗi kết nối RPC: %v\n", err)
		return nil
	}
	defer client.Close()

	parsedABI, _ := abi.JSON(strings.NewReader(ValidationABI))
	callData, _ := parsedABI.Pack("validators", validatorAddr)

	callObject := map[string]interface{}{
		"from": validatorAddr.Hex(),
		"to":   mt_common.VALIDATOR_CONTRACT_ADDRESS.Hex(),
		"data": hexutil.Encode(callData),
	}

	var result hexutil.Bytes
	err = client.Call(&result, "eth_call", callObject, "latest")
	if err != nil {
		fmt.Printf("❌ Lỗi lấy validator info: %v\n", err)
		return nil
	}

	// Decode result - totalStakedAmount is at index 10 in the outputs:
	// [0] owner, [1] primaryAddress, [2] workerAddress, [3] p2pAddress,
	// [4] name, [5] description, [6] website, [7] image,
	// [8] commissionRate, [9] minSelfDelegation, [10] totalStakedAmount, ...
	outputs, err := parsedABI.Methods["validators"].Outputs.Unpack(result)
	if err != nil {
		fmt.Printf("❌ Lỗi decode validator info: %v\n", err)
		return nil
	}

	if len(outputs) >= 11 {
		totalStaked, ok := outputs[10].(*big.Int)
		if ok {
			return totalStaked
		}
	}

	return nil
}

func encodeUndelegate(validatorAddr common.Address, amount *big.Int) ([]byte, error) {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return nil, fmt.Errorf("parsing ABI: %v", err)
	}
	return parsedABI.Pack("undelegate", validatorAddr, amount)
}

func sendUndelegateTransaction(privateKeyHex string, validatorAddr common.Address, amount *big.Int) (string, error) {
	fmt.Printf("   📤 Đang gửi undelegate transaction...\n")
	fmt.Printf("      Validator: %s\n", validatorAddr.Hex())
	fmt.Printf("      Amount:    %s wei\n", amount.String())

	// 1. Encode undelegate calldata
	inputData, err := encodeUndelegate(validatorAddr, amount)
	if err != nil {
		return "", fmt.Errorf("encode undelegate call: %v", err)
	}

	// 2. Load private key
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("cannot cast public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	// 3. Get nonce
	nonce, err := getTransactionCount(fromAddress.Hex())
	if err != nil {
		return "", fmt.Errorf("getting nonce: %v", err)
	}
	fmt.Printf("      Nonce: %d\n", nonce)

	// 4. Create transaction (no value)
	contractAddress := mt_common.VALIDATOR_CONTRACT_ADDRESS
	gasLimit := uint64(500000)
	gasPrice := big.NewInt(20000000000) // 20 Gwei

	tx := types.NewTransaction(nonce, contractAddress, big.NewInt(0), gasLimit, gasPrice, inputData)

	// 5. Sign transaction
	chainID := big.NewInt(CHAIN_ID)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return "", fmt.Errorf("signing transaction: %v", err)
	}

	// 6. Send transaction
	txHash, err := sendRawTransaction(signedTx)
	if err != nil {
		return "", fmt.Errorf("sending transaction: %v", err)
	}

	return txHash, nil
}

// ============================================================
// Helper Functions
// ============================================================

func encodeDeregisterValidator() ([]byte, error) {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return nil, fmt.Errorf("parsing ABI: %v", err)
	}

	// deregisterValidator() không có tham số
	return parsedABI.Pack("deregisterValidator")
}

func sendDeregisterTransaction(privateKeyHex string, inputData []byte) (string, error) {
	// 1. Load private key
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("cannot cast public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	// 2. Get nonce
	nonce, err := getTransactionCount(fromAddress.Hex())
	if err != nil {
		return "", fmt.Errorf("getting nonce: %v", err)
	}
	fmt.Printf("   Nonce: %d\n", nonce)

	// 3. Create transaction (no value needed)
	contractAddress := mt_common.VALIDATOR_CONTRACT_ADDRESS
	gasLimit := uint64(300000)
	gasPrice := big.NewInt(20000000000) // 20 Gwei

	tx := types.NewTransaction(nonce, contractAddress, big.NewInt(0), gasLimit, gasPrice, inputData)

	// 4. Sign transaction
	chainID := big.NewInt(CHAIN_ID)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return "", fmt.Errorf("signing transaction: %v", err)
	}

	// 5. Send transaction
	txHash, err := sendRawTransaction(signedTx)
	if err != nil {
		return "", fmt.Errorf("sending transaction: %v", err)
	}

	return txHash, nil
}

func getTransactionCount(address string) (uint64, error) {
	req := RPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_getTransactionCount",
		Params:  []interface{}{address, "latest"},
		Id:      1,
	}
	res, err := sendRPC(req)
	if err != nil {
		return 0, err
	}

	rawResult := string(res.Result)
	if rawResult == "null" || rawResult == "" {
		return 0, nil
	}

	var hexNonce string
	if err := json.Unmarshal(res.Result, &hexNonce); err == nil {
		return hexutil.DecodeUint64(hexNonce)
	}

	var intNonce uint64
	if err := json.Unmarshal(res.Result, &intNonce); err == nil {
		return intNonce, nil
	}

	return 0, fmt.Errorf("could not unmarshal nonce result: %s", rawResult)
}

func sendRawTransaction(tx *types.Transaction) (string, error) {
	rawTxBytes, err := tx.MarshalBinary()
	if err != nil {
		return "", err
	}
	rawTxHex := hexutil.Encode(rawTxBytes)

	req := RPCRequest{
		Jsonrpc: "2.0",
		Method:  "eth_sendRawTransaction",
		Params:  []interface{}{rawTxHex, nil, nil},
		Id:      2,
	}
	res, err := sendRPC(req)
	if err != nil {
		return "", err
	}

	var txHash string
	if err := json.Unmarshal(res.Result, &txHash); err != nil {
		return "", fmt.Errorf("failed to unmarshal tx hash: %v. Response: %s", err, string(res.Result))
	}
	return txHash, nil
}

func sendRPC(req RPCRequest) (*RPCResponse, error) {
	body, _ := json.Marshal(req)
	httpResp, err := http.Post(RPC_HTTP_URL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("http error: %v", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %v", err)
	}

	var res RPCResponse
	if err := json.Unmarshal(respBody, &res); err != nil {
		return nil, fmt.Errorf("json unmarshal error: %v", err)
	}

	if res.Error != nil {
		return nil, fmt.Errorf("RPC Error: %s (code %d)", res.Error.Message, res.Error.Code)
	}
	return &res, nil
}

func checkValidatorRegistration(expectedAddress common.Address) bool {
	client, err := rpc.Dial(RPC_WS_URL)
	if err != nil {
		fmt.Printf("❌ Lỗi kết nối RPC: %v\n", err)
		return false
	}
	defer client.Close()

	contractAddr := mt_common.VALIDATOR_CONTRACT_ADDRESS

	// Get validator count
	callData := common.FromHex("0x7071688a") // getValidatorCount()
	callObject := map[string]interface{}{
		"from": expectedAddress.Hex(),
		"to":   contractAddr.Hex(),
		"data": hexutil.Encode(callData),
	}

	var result hexutil.Bytes
	err = client.Call(&result, "eth_call", callObject, "latest")
	if err != nil {
		fmt.Printf("❌ Lỗi lấy validator count: %v\n", err)
		return false
	}

	count := new(big.Int).SetBytes(result)
	fmt.Printf("   Tổng số Validators: %d\n", count.Int64())

	// Get validator addresses and check if expectedAddress is in the list
	parsedABI, _ := abi.JSON(strings.NewReader(ValidationABI))

	for i := int64(0); i < count.Int64(); i++ {
		indexData, _ := parsedABI.Pack("validatorAddresses", big.NewInt(i))
		callObject["data"] = hexutil.Encode(indexData)

		var addrResult hexutil.Bytes
		err = client.Call(&addrResult, "eth_call", callObject, "latest")
		if err != nil {
			continue
		}

		addr := common.BytesToAddress(addrResult)

		if addr == expectedAddress {
			fmt.Printf("   ✅ Validator %s đã được đăng ký tại index [%d]\n", expectedAddress.Hex(), i)
			return true
		}
	}

	return false
}

func verifyDeregistration(expectedAddress common.Address) {
	client, err := rpc.Dial(RPC_WS_URL)
	if err != nil {
		fmt.Printf("❌ Lỗi kết nối RPC: %v\n", err)
		return
	}
	defer client.Close()

	contractAddr := mt_common.VALIDATOR_CONTRACT_ADDRESS

	// Get validator count
	callData := common.FromHex("0x7071688a") // getValidatorCount()
	callObject := map[string]interface{}{
		"from": expectedAddress.Hex(),
		"to":   contractAddr.Hex(),
		"data": hexutil.Encode(callData),
	}

	var result hexutil.Bytes
	err = client.Call(&result, "eth_call", callObject, "latest")
	if err != nil {
		fmt.Printf("❌ Lỗi lấy validator count: %v\n", err)
		return
	}

	count := new(big.Int).SetBytes(result)
	fmt.Printf("\n📊 Tổng số Validators sau khi hủy: %d\n", count.Int64())

	// Get validator addresses
	parsedABI, _ := abi.JSON(strings.NewReader(ValidationABI))
	found := false

	for i := int64(0); i < count.Int64(); i++ {
		indexData, _ := parsedABI.Pack("validatorAddresses", big.NewInt(i))
		callObject["data"] = hexutil.Encode(indexData)

		var addrResult hexutil.Bytes
		err = client.Call(&addrResult, "eth_call", callObject, "latest")
		if err != nil {
			continue
		}

		addr := common.BytesToAddress(addrResult)

		if addr == expectedAddress {
			fmt.Printf("  ⚠️ [%d] %s (VẪN CÒN TRONG DANH SÁCH!)\n", i, addr.Hex())
			found = true
		} else {
			fmt.Printf("  [%d] %s\n", i, addr.Hex())
		}
	}

	fmt.Println()
	if !found {
		fmt.Println("🎉 ════════════════════════════════════════════════════════")
		fmt.Println("🎉  VALIDATOR ĐÃ ĐƯỢC HỦY ĐĂNG KÝ THÀNH CÔNG!")
		fmt.Println("🎉  Node 4 đã bị xóa khỏi danh sách validators.")
		fmt.Println("🎉 ════════════════════════════════════════════════════════")
	} else {
		fmt.Println("⚠️  Validator vẫn còn trong danh sách.")
		fmt.Println("   Có thể transaction chưa được xử lý hoặc cần thêm thời gian.")
		fmt.Println("   Hoặc deregistration có thể có thời gian unbonding period.")
	}
}
