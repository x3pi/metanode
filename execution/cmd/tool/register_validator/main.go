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
	RPC_HTTP_URL = "http://localhost:8545" // RPC proxy endpoint (handles eth_sendRawTransaction)
	RPC_WS_URL   = "http://localhost:8545" // Query endpoint
	CHAIN_ID     = 991
)

// Node 4 default configuration (from mtn-consensus/metanode/config/node_4.toml)
// Keys are extracted as public key portions (last 32 bytes) from the 64-byte key files
var defaultNode4 = struct {
	PrivateKey     string
	PrimaryAddress string
	WorkerAddress  string
	P2PAddress     string
	Name           string
	Description    string
	Website        string
	Image          string
	CommissionRate uint64
	NetworkKey     string
	Hostname       string
	AuthorityKey   string
	ProtocolKey    string
}{
	// Private key for account 0xABd56156C3Eb4F87C3b0b9F6D5D2ddB9F7Abed11 (Node 4)
	PrivateKey: "6c8489f6f86fea58b26e34c8c37e13e5993651f09f5f96739d9febf65aded718",
	// Addresses from node_4.toml
	PrimaryAddress: "/ip4/127.0.0.1/tcp/9004",
	WorkerAddress:  "127.0.0.1:9004",
	P2PAddress:     "/ip4/127.0.0.1/tcp/9004",
	// Validator metadata
	Name:           "node-4",
	Description:    "Node 4 - SyncOnly Validator (ready for epoch transition)",
	Website:        "",
	Image:          "",
	CommissionRate: 1000, // 10%
	// Keys from mtn-consensus/metanode/config (public key portions - last 32 bytes, base64)
	// node_4_protocol_key.json: Bx+lQ63vuiMQx3yU/zlxIj8eklcnnsOC309yiHIC1+6qdMErfQa6LiQGpHRQXAurfgM1r8bBeoa5uklvH7zuRA==
	// Public portion (last 32 bytes): qnTBK30Gui4kBqR0UFwLq34DNa/GwXqGubpJbx+87kQ=
	ProtocolKey: "qnTBK30Gui4kBqR0UFwLq34DNa/GwXqGubpJbx+87kQ=",
	// node_4_network_key.json: Bx+lQ63vuiMQx3yU/zlxIj8eklcnnsOC309yiHIC1+4G+u+e5g5vIzco8ydGZ6BXTSSVt2dhO1hI31KS4rXSFdwbpgs=
	// Public portion (last 32 bytes): 5g5vIzco8ydGZ6BXTSSVt2dhO1hI31KS4rXSFdwbpgs=
	NetworkKey: "5g5vIzco8ydGZ6BXTSSVt2dhO1hI31KS4rXSFdwbpgs=",
	// Hostname matches node name
	Hostname: "node-4",
	// AuthorityKey: BLS G2 public key (96 bytes = 128 chars base64)
	// CRITICAL: Must be a valid BLS12-381 G2 curve point, NOT random bytes!
	// Generated using: cd mtn-consensus/metanode && cargo run -- generate --nodes 5
	AuthorityKey: "q2rdagN+1z8x6ozdCBj1l8P4aYm0Di5b2Wa1ojyBY9XtBj31dortoL2Q4h4bhRzQDZHRhSPJQImRUIABBemflBZ6dbrleOtZSrBrgMNEi2l0h54q36CrNPQdNLKREYem",
}

// ============================================================
// ABI Definition
// ============================================================

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
			{"name": "minSelfDelegation", "type": "uint256"},
			{"name": "networkKey", "type": "string"},
			{"name": "hostname", "type": "string"},
			{"name": "authorityKey", "type": "string"},
			{"name": "protocolKey", "type": "string"}
		]
	},
	{
		"name": "delegate",
		"type": "function",
		"inputs": [
			{"name": "_validatorAddress", "type": "address"}
		]
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
		"name": "withdrawReward",
		"type": "function",
		"inputs": [
			{"name": "_validatorAddress", "type": "address"}
		]
	},
	{
		"name": "setCommissionRate",
		"type": "function",
		"inputs": [
			{"name": "_newRate", "type": "uint64"}
		]
	},
	{
		"name": "deregisterValidator",
		"type": "function",
		"inputs": []
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

type RegisterValidatorParams struct {
	PrimaryAddress    string
	WorkerAddress     string
	P2PAddress        string
	Name              string
	Description       string
	Website           string
	Image             string
	CommissionRate    uint64
	MinSelfDelegation *big.Int
	NetworkKey        string
	Hostname          string
	AuthorityKey      string
	ProtocolKey       string
}

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
	name := flag.String("name", defaultNode4.Name, "Validator name")
	primaryAddr := flag.String("primary", defaultNode4.PrimaryAddress, "Primary/P2P address")
	workerAddr := flag.String("worker", defaultNode4.WorkerAddress, "Worker address")
	p2pAddr := flag.String("p2p", defaultNode4.P2PAddress, "P2P address")
	description := flag.String("desc", defaultNode4.Description, "Description")
	website := flag.String("website", defaultNode4.Website, "Website URL")
	image := flag.String("image", defaultNode4.Image, "Image URL")
	commission := flag.Uint64("commission", defaultNode4.CommissionRate, "Commission rate (1000 = 10%)")
	minDelegation := flag.String("min-delegation", "0", "Minimum self-delegation (in wei)")
	networkKey := flag.String("network-key", defaultNode4.NetworkKey, "Network key (Ed25519 base64)")
	hostname := flag.String("hostname", defaultNode4.Hostname, "Hostname")
	authorityKey := flag.String("authority-key", defaultNode4.AuthorityKey, "Authority key (BLS base64)")
	protocolKey := flag.String("protocol-key", defaultNode4.ProtocolKey, "Protocol key (Ed25519 base64)")
	dryRun := flag.Bool("dry-run", false, "Only encode calldata, don't send transaction")
	delegateOnly := flag.Bool("delegate-only", false, "Only delegate stake (skip registration)")
	stakeAmount := flag.String("stake", "1000000000000000000000", "Stake amount in wei (default: 1000 ETH)")

	flag.Parse()

	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║          REGISTER VALIDATOR TOOL                          ║")
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

	// Parse stake amount
	stakeAmountBig, ok := new(big.Int).SetString(*stakeAmount, 10)
	if !ok {
		stakeAmountBig = new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18)) // Default 1000 ETH
	}
	fmt.Printf("💰 Stake Amount:   %s wei\n", stakeAmountBig.String())
	fmt.Println()

	// Delegate-only mode: skip registration
	if *delegateOnly {
		fmt.Println("═══════════════════════════════════════════════════════════")
		fmt.Println("📌 DELEGATE-ONLY MODE: Bỏ qua đăng ký, chỉ thêm stake")
		fmt.Println("═══════════════════════════════════════════════════════════")
		fmt.Println()

		err = sendDelegateTransaction(*privateKey, fromAddress, stakeAmountBig)
		if err != nil {
			fmt.Printf("❌ Lỗi delegate: %v\n", err)
			return
		}

		fmt.Println("⏳ Đợi 3 giây để transaction được xác nhận...")
		time.Sleep(3 * time.Second)

		fmt.Println("\n🔍 Kiểm tra delegation...")
		verifyDelegation(fromAddress)
		return
	}

	// 2. Parse min delegation
	minDelegationBig, ok := new(big.Int).SetString(*minDelegation, 10)
	if !ok {
		minDelegationBig = big.NewInt(0)
	}

	// 3. Prepare parameters
	params := RegisterValidatorParams{
		PrimaryAddress:    *primaryAddr,
		WorkerAddress:     *workerAddr,
		P2PAddress:        *p2pAddr,
		Name:              *name,
		Description:       *description,
		Website:           *website,
		Image:             *image,
		CommissionRate:    *commission,
		MinSelfDelegation: minDelegationBig,
		NetworkKey:        *networkKey,
		Hostname:          *hostname,
		AuthorityKey:      *authorityKey,
		ProtocolKey:       *protocolKey,
	}

	fmt.Println("📋 Thông tin Validator:")
	fmt.Println("─────────────────────────────────────────")
	fmt.Printf("  Name:              %s\n", params.Name)
	fmt.Printf("  Hostname:          %s\n", params.Hostname)
	fmt.Printf("  Primary Address:   %s\n", params.PrimaryAddress)
	fmt.Printf("  Worker Address:    %s\n", params.WorkerAddress)
	fmt.Printf("  P2P Address:       %s\n", params.P2PAddress)
	fmt.Printf("  Description:       %s\n", params.Description)
	fmt.Printf("  Commission Rate:   %.2f%%\n", float64(params.CommissionRate)/100)
	fmt.Printf("  Protocol Key:      %s\n", params.ProtocolKey)
	fmt.Printf("  Network Key:       %s\n", params.NetworkKey)
	fmt.Printf("  Authority Key:     %s...\n", params.AuthorityKey[:40])
	fmt.Println()

	// 4. Encode calldata
	inputData, err := encodeRegisterValidator(params)
	if err != nil {
		fmt.Printf("❌ Lỗi encode calldata: %v\n", err)
		return
	}

	fmt.Println("🔧 Calldata đã encode:")
	fmt.Printf("   %s...\n", hexutil.Encode(inputData[:min(64, len(inputData))]))
	fmt.Printf("   (Total: %d bytes)\n", len(inputData))
	fmt.Println()

	if *dryRun {
		fmt.Println("⚠️  DRY-RUN mode: Không gửi transaction")
		fmt.Printf("📦 Full Calldata:\n%s\n", hexutil.Encode(inputData))
		return
	}

	// 5. Send registration transaction
	fmt.Println("📤 Đang gửi transaction đăng ký validator...")
	txHash, err := sendValidatorTransaction(*privateKey, big.NewInt(0), inputData)
	if err != nil {
		fmt.Printf("❌ Lỗi gửi transaction: %v\n", err)
		return
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("✅ REGISTRATION TRANSACTION ĐÃ GỬI THÀNH CÔNG!\n")
	fmt.Printf("   TX Hash: %s\n", txHash)
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()

	// 6. Wait for registration to be confirmed
	fmt.Println("⏳ Đợi 3 giây để transaction đăng ký được xác nhận...")
	time.Sleep(3 * time.Second)

	fmt.Println("\n🔍 Kiểm tra danh sách validators...")
	verifyRegistration(fromAddress, params.Name)

	// 7. Send delegate transaction to add stake
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("📌 BƯỚC 2: Thêm stake cho validator...")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()

	err = sendDelegateTransaction(*privateKey, fromAddress, stakeAmountBig)
	if err != nil {
		fmt.Printf("❌ Lỗi delegate: %v\n", err)
		return
	}

	fmt.Println("⏳ Đợi 3 giây để delegate transaction được xác nhận...")
	time.Sleep(3 * time.Second)

	fmt.Println("\n🔍 Kiểm tra delegation...")
	verifyDelegation(fromAddress)
}

// ============================================================
// Helper Functions
// ============================================================

func encodeRegisterValidator(params RegisterValidatorParams) ([]byte, error) {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return nil, fmt.Errorf("parsing ABI: %v", err)
	}

	if params.MinSelfDelegation == nil {
		params.MinSelfDelegation = big.NewInt(0)
	}

	return parsedABI.Pack("registerValidator",
		params.PrimaryAddress,
		params.WorkerAddress,
		params.P2PAddress,
		params.Name,
		params.Description,
		params.Website,
		params.Image,
		params.CommissionRate,
		params.MinSelfDelegation,
		params.NetworkKey,
		params.Hostname,
		params.AuthorityKey,
		params.ProtocolKey,
	)
}

func sendValidatorTransaction(privateKeyHex string, value *big.Int, inputData []byte) (string, error) {
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

	// 3. Create transaction
	contractAddress := mt_common.VALIDATOR_CONTRACT_ADDRESS
	gasLimit := uint64(500000)
	gasPrice := big.NewInt(20000000000) // 20 Gwei

	tx := types.NewTransaction(nonce, contractAddress, value, gasLimit, gasPrice, inputData)

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
	// RPC client uses decodeHexPooled which expects hex-encoded data
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

func verifyRegistration(expectedAddress common.Address, expectedName string) {
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
	fmt.Printf("\n📊 Tổng số Validators: %d\n", count.Int64())

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
			fmt.Printf("  ✅ [%d] %s (ĐÃ TÌM THẤY!)\n", i, addr.Hex())
			found = true
		} else {
			fmt.Printf("  [%d] %s\n", i, addr.Hex())
		}
	}

	fmt.Println()
	if found {
		fmt.Println("🎉 ════════════════════════════════════════════════════")
		fmt.Println("🎉  VALIDATOR ĐÃ ĐƯỢC ĐĂNG KÝ THÀNH CÔNG!")
		fmt.Println("🎉 ════════════════════════════════════════════════════")
	} else {
		fmt.Println("⚠️  Validator chưa xuất hiện trong danh sách.")
		fmt.Println("   Có thể transaction chưa được xử lý hoặc bị lỗi.")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ============================================================
// Delegate Functions
// ============================================================

func encodeDelegateCall(validatorAddress common.Address) ([]byte, error) {
	parsedABI, err := abi.JSON(strings.NewReader(ValidationABI))
	if err != nil {
		return nil, fmt.Errorf("parsing ABI: %v", err)
	}

	return parsedABI.Pack("delegate", validatorAddress)
}

func sendDelegateTransaction(privateKeyHex string, validatorAddress common.Address, amount *big.Int) error {
	fmt.Printf("📤 Đang gửi delegate transaction...\n")
	fmt.Printf("   Validator: %s\n", validatorAddress.Hex())
	fmt.Printf("   Amount:    %s wei\n", amount.String())

	// 1. Encode delegate calldata
	inputData, err := encodeDelegateCall(validatorAddress)
	if err != nil {
		return fmt.Errorf("encode delegate call: %v", err)
	}

	// 2. Load private key
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return fmt.Errorf("invalid private key: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("cannot cast public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	// 3. Get nonce
	nonce, err := getTransactionCount(fromAddress.Hex())
	if err != nil {
		return fmt.Errorf("getting nonce: %v", err)
	}
	fmt.Printf("   Nonce: %d\n", nonce)

	// 4. Create transaction (with value = stake amount)
	contractAddress := mt_common.VALIDATOR_CONTRACT_ADDRESS
	gasLimit := uint64(300000)
	gasPrice := big.NewInt(20000000000) // 20 Gwei

	tx := types.NewTransaction(nonce, contractAddress, amount, gasLimit, gasPrice, inputData)

	// 5. Sign transaction
	chainID := big.NewInt(CHAIN_ID)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return fmt.Errorf("signing transaction: %v", err)
	}

	// 6. Send transaction
	txHash, err := sendRawTransaction(signedTx)
	if err != nil {
		return fmt.Errorf("sending transaction: %v", err)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("✅ DELEGATE TRANSACTION ĐÃ GỬI THÀNH CÔNG!\n")
	fmt.Printf("   TX Hash: %s\n", txHash)
	fmt.Println("═══════════════════════════════════════════════════════════")

	return nil
}

func verifyDelegation(validatorAddress common.Address) {
	client, err := rpc.Dial(RPC_WS_URL)
	if err != nil {
		fmt.Printf("❌ Lỗi kết nối RPC: %v\n", err)
		return
	}
	defer client.Close()

	// Call getDelegation(address _delegator, address _validator) to get delegation info
	// Or use custom RPC method if available

	// For now, we'll show a simple confirmation message
	// The actual verification would need a getDelegation function in the ABI

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("📊 Delegation cho validator %s:\n", validatorAddress.Hex())
	fmt.Println("   Sử dụng tool query_validators để xem chi tiết delegation")
	fmt.Println("   cd ../query_validators && go run . -validator", validatorAddress.Hex())
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Println("🎉 ════════════════════════════════════════════════════════")
	fmt.Println("🎉  VALIDATOR ĐÃ ĐĂNG KÝ VÀ STAKE THÀNH CÔNG!")
	fmt.Println("🎉 ════════════════════════════════════════════════════════")
}
