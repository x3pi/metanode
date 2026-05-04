package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

type Config struct {
	RPCUrl     string `json:"rpc_url"`
	PrivateKey string `json:"private_key"`
	ChainID    int64  `json:"chain_id"`
}

func loadConfig(path string) *Config {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("❌ Lỗi đọc file config (%s): %v", path, err)
	}
	var c Config
	err = json.Unmarshal(b, &c)
	if err != nil {
		log.Fatalf("❌ Lỗi parse file config: %v", err)
	}
	return &c
}

func main() {
	// 1. Tải cấu hình
	configPath := "../config-local.json"
	cfg := loadConfig(configPath)

	// 2. Kết nối tới Chain
	client, err := ethclient.Dial(cfg.RPCUrl)
	if err != nil {
		log.Fatalf("❌ Lỗi kết nối RPC %s: %v", cfg.RPCUrl, err)
	}
	fmt.Printf("✅ Đã kết nối tới mạng: %s\n", cfg.RPCUrl)

	// 3. Chuẩn bị Private Key & Address người gửi
	privateKey, err := crypto.HexToECDSA(cfg.PrivateKey)
	if err != nil {
		log.Fatalf("❌ Lỗi parse private key: %v", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		log.Fatal("❌ Lỗi parse public key")
	}

	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	// Khởi tạo một địa chỉ nhận ngẫu nhiên (hoặc bạn có thể hardcode)
	toAddress := common.HexToAddress("0x1111222233334444555566667777888899990000")

	// Số lượng gửi: 1 META (10^18 wei)
	amount := new(big.Int)
	amount.SetString("1000000000000000000", 10) // 1 ETH/META

	fmt.Println("\n==================================================")
	fmt.Println("🔍 KIỂM TRA SỐ DƯ TRƯỚC KHI CHUYỂN")
	fmt.Println("==================================================")
	
	balanceFromBefore, err := client.BalanceAt(context.Background(), fromAddress, nil)
	if err != nil {
		log.Fatalf("❌ Lỗi lấy số dư người gửi: %v", err)
	}
	
	balanceToBefore, err := client.BalanceAt(context.Background(), toAddress, nil)
	if err != nil {
		log.Fatalf("❌ Lỗi lấy số dư người nhận: %v", err)
	}

	fmt.Printf("📤 Người gửi (%s): %s wei\n", fromAddress.Hex(), balanceFromBefore.String())
	fmt.Printf("📥 Người nhận (%s): %s wei\n", toAddress.Hex(), balanceToBefore.String())

	// 4. Tạo giao dịch (Transaction)
	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		log.Fatalf("❌ Lỗi lấy nonce: %v", err)
	}

	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		log.Fatalf("❌ Lỗi lấy gas price: %v", err)
	}

	// Native transfer tốn chuẩn 21000 gas
	gasLimit := uint64(21000) 

	tx := types.NewTransaction(nonce, toAddress, amount, gasLimit, gasPrice, nil)

	// 5. Ký giao dịch
	chainID := big.NewInt(cfg.ChainID)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		log.Fatalf("❌ Lỗi ký transaction: %v", err)
	}

	fmt.Println("\n==================================================")
	fmt.Printf("🚀 ĐANG GỬI GIAO DỊCH (%s wei)...\n", amount.String())
	fmt.Println("==================================================")
	fmt.Printf("   - Tx Hash: %s\n", signedTx.Hash().Hex())
	fmt.Printf("   - Nonce: %d\n", nonce)
	fmt.Printf("   - Gas Price: %s\n", gasPrice.String())

	// 6. Gửi giao dịch lên mạng
	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		log.Fatalf("❌ Lỗi gửi transaction: %v", err)
	}

	// 7. Chờ giao dịch được đưa vào Block (Mining)
	fmt.Print("⏳ Đang chờ mạng lưới xác nhận ")
	var receipt *types.Receipt
	for {
		receipt, err = client.TransactionReceipt(context.Background(), signedTx.Hash())
		if err == nil {
			break
		} else if err != ethereum.NotFound {
			log.Fatalf("\n❌ Lỗi hệ thống khi check receipt: %v", err)
		}
		fmt.Print(".")
		time.Sleep(1 * time.Second)
	}
	fmt.Println()

	if receipt.Status == 1 {
		fmt.Printf("✅ GIAO DỊCH THÀNH CÔNG! (Block: %d, Gas used: %d)\n", receipt.BlockNumber.Uint64(), receipt.GasUsed)
	} else {
		log.Fatalf("❌ GIAO DỊCH THẤT BẠI! (Tx bị Revert)")
	}

	// 8. Tính tổng phí gas đã sử dụng
	gasUsedBig := new(big.Int).SetUint64(receipt.GasUsed)
	gasCost := new(big.Int).Mul(gasUsedBig, gasPrice)
	fmt.Printf("💸 Tổng phí Gas đã trả: %s wei\n", gasCost.String())

	fmt.Println("\n==================================================")
	fmt.Println("🔍 KIỂM TRA SỐ DƯ SAU KHI CHUYỂN")
	fmt.Println("==================================================")

	balanceFromAfter, err := client.BalanceAt(context.Background(), fromAddress, nil)
	if err != nil {
		log.Fatalf("❌ Lỗi lấy số dư người gửi: %v", err)
	}

	balanceToAfter, err := client.BalanceAt(context.Background(), toAddress, nil)
	if err != nil {
		log.Fatalf("❌ Lỗi lấy số dư người nhận: %v", err)
	}

	fmt.Printf("📤 Người gửi (%s): %s wei\n", fromAddress.Hex(), balanceFromAfter.String())
	fmt.Printf("📥 Người nhận (%s): %s wei\n", toAddress.Hex(), balanceToAfter.String())

	// 9. Đối chiếu sự thay đổi (Tăng giảm có chuẩn xác không?)
	fmt.Println("\n==================================================")
	fmt.Println("📊 ĐỐI CHIẾU SỰ THAY ĐỔI")
	fmt.Println("==================================================")
	
	// Thay đổi của người nhận = balanceToAfter - balanceToBefore
	diffTo := new(big.Int).Sub(balanceToAfter, balanceToBefore)
	
	// Thay đổi của người gửi = balanceFromBefore - balanceFromAfter
	diffFrom := new(big.Int).Sub(balanceFromBefore, balanceFromAfter)
	
	// Tổng tiêu hao dự kiến của người gửi = Amount + GasCost
	expectedDiffFrom := new(big.Int).Add(amount, gasCost)

	fmt.Printf("📈 Người nhận tăng: %s wei (Kỳ vọng: %s wei) -> ", diffTo.String(), amount.String())
	if diffTo.Cmp(amount) == 0 {
		fmt.Println("✅ ĐÚNG")
	} else {
		fmt.Println("❌ SAI")
	}

	fmt.Printf("📉 Người gửi giảm: %s wei (Kỳ vọng: %s wei) -> ", diffFrom.String(), expectedDiffFrom.String())
	if diffFrom.Cmp(expectedDiffFrom) == 0 {
		fmt.Println("✅ ĐÚNG")
	} else {
		fmt.Println("❌ SAI")
	}
}
