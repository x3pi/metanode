package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// Struct đơn giản để parse dữ liệu thô từ sự kiện newHeads
type RawHeader struct {
	ParentHash  common.Hash      `json:"parentHash"`
	Hash        common.Hash      `json:"hash"`
	UncleHash   common.Hash      `json:"sha3Uncles"`
	Coinbase    common.Address   `json:"miner"`
	Root        common.Hash      `json:"stateRoot"`
	TxHash      common.Hash      `json:"transactionsRoot"`
	ReceiptHash common.Hash      `json:"receiptsRoot"`
	Bloom       types.Bloom      `json:"logsBloom"`
	Difficulty  json.Number      `json:"difficulty"` // Có thể là string hoặc number
	Number      json.Number      `json:"number"`
	GasLimit    json.Number      `json:"gasLimit"`
	GasUsed     json.Number      `json:"gasUsed"`
	Time        json.Number      `json:"timestamp"`
	Extra       []byte           `json:"extraData"`
	MixDigest   common.Hash      `json:"mixHash"`
	Nonce       types.BlockNonce `json:"nonce"`
	BaseFee     *json.Number     `json:"baseFeePerGas"`
}

func main() {
	// Kết nối tới WebSocket node Ethereum
	client, err := rpc.Dial("ws://localhost:8545")
	if err != nil {
		log.Fatalf("❌ Lỗi kết nối WebSocket: %v", err)
	}
	defer client.Close()

	// Kênh nhận raw JSON block header
	headers := make(chan json.RawMessage)

	// Đăng ký sự kiện newHeads
	sub, err := client.EthSubscribe(context.Background(), headers, "newHeads")
	if err != nil {
		log.Fatalf("❌ Lỗi khi subscribe: %v", err)
	}

	fmt.Println("⏳ Đang lắng nghe block mới...")

	for {
		select {
		case err := <-sub.Err():
			log.Fatalf("❌ Lỗi trong quá trình lắng nghe: %v", err)

		case raw := <-headers:
			var hdr RawHeader
			if err := json.Unmarshal(raw, &hdr); err != nil {
				log.Printf("❌ Lỗi parse header: %v", err)
				fmt.Printf("📦 Raw: %s\n", raw)
				continue
			}
			fmt.Printf("📦 Raw: %s\n", raw)

			// Chuyển các giá trị sang big.Int
			num := jsonNumberToBigInt(hdr.Number)
			diff := jsonNumberToBigInt(hdr.Difficulty)
			gasLimit := jsonNumberToBigInt(hdr.GasLimit)
			timestamp := jsonNumberToBigInt(hdr.Time)

			fmt.Printf("🧱 Block #%v | Hash: %s | Difficulty: %s | GasLimit: %v | Timestamp: %v\n",
				num, hdr.Hash.Hex(), diff.String(), gasLimit, timestamp)
		}
	}
}

// Hàm tiện ích để chuyển json.Number → *big.Int
func jsonNumberToBigInt(n json.Number) *big.Int {
	b := new(big.Int)
	if str := n.String(); str != "" {
		b.SetString(str, 10)
	}
	return b
}
