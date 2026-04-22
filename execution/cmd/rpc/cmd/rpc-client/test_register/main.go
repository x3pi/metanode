package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type RegisterBlsKeyParams struct {
	Address       string `json:"address"`
	BlsPrivateKey string `json:"blsPrivateKey"`
	Timestamp     string `json:"timestamp"`
	Signature     string `json:"signature"`
}

type JSONRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

func main() {
	var (
		ethPrivHex  = flag.String("eth-key", "", "Ethereum Private Key (hex without 0x) dùng để ký gửi")
		blsPrivHex  = flag.String("bls-key", "", "BLS Private Key (phải có 0x) của bạn")
		rpcEndpoint = flag.String("rpc", "http://localhost:8545", "RPC Endpoint")
	)
	flag.Parse()

	if *ethPrivHex == "" || *blsPrivHex == "" {
		log.Println("Cú pháp: go run main.go -eth-key <eth_private_key> -bls-key <bls_private_key>")
		log.Fatal("Lỗi: Yêu cầu cung cấp đủ -eth-key và -bls-key")
	}

	ethPrivKey, err := crypto.HexToECDSA(*ethPrivHex)
	if err != nil {
		log.Fatalf("Private key Ethereum không hợp lệ: %v", err)
	}

	ethAddress := crypto.PubkeyToAddress(ethPrivKey.PublicKey).Hex()
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)

	// Tạo thông điệp theo format của register_bls.go
	messageToVerify := fmt.Sprintf("BLS Data: %s\nTimestamp: %s", *blsPrivHex, timestamp)
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(messageToVerify), messageToVerify)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	// Ký message bằng Ethereum private key
	signatureBytes, err := crypto.Sign(messageHash.Bytes(), ethPrivKey)
	if err != nil {
		log.Fatalf("Lỗi khi ký: %v", err)
	}
	// Chuyển recovery ID (V) sang chuẩn Ethereum (27 hoặc 28)
	if len(signatureBytes) == 65 && signatureBytes[64] < 27 {
		signatureBytes[64] += 27
	}

	signatureHex := hexutil.Encode(signatureBytes)

	// Đóng gói data gửi đi
	params := RegisterBlsKeyParams{
		Address:       ethAddress,
		BlsPrivateKey: *blsPrivHex,
		Timestamp:     timestamp,
		Signature:     signatureHex,
	}

	reqBody := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "rpc_registerBlsKeyWithSignature",
		Params:  []interface{}{params},
		ID:      1,
	}

	reqBytes, _ := json.MarshalIndent(reqBody, "", "  ")

	fmt.Printf("Gửi request liên kết ví %s với BLS Key tới %s...\n", ethAddress, *rpcEndpoint)
	fmt.Printf("Payload:\n%s\n", string(reqBytes))

	// Thực hiện gọi RPC POST HTTP
	resp, err := http.Post(*rpcEndpoint, "application/json", bytes.NewBuffer(reqBytes))
	if err != nil {
		log.Fatalf("Gọi RPC thất bại: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := ioutil.ReadAll(resp.Body)
	fmt.Println("\n================= KẾT QUẢ ===================")
	fmt.Printf("HTTP Status: %s\n", resp.Status)
	fmt.Printf("Response Body: %s\n", string(bodyBytes))
}
