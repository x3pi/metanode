package main

import (
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/config"
	tx_helper "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/utils/tx_helper"
	"github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/pkg/models/tx_models"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func main() {
	logger.SetConfig(&logger.LoggerConfig{
		Flag:    logger.FLAG_INFO,
		Outputs: []*os.File{os.Stdout},
	})

	// Sử dụng flag để load config linh hoạt
	config1Path := flag.String("c1", "config-local-1.json", "Đường dẫn file config cho Client 1")
	config2Path := flag.String("c2", "config-local-2.json", "Đường dẫn file config cho Client 2")
	flag.Parse()

	// 1. Load config
	cfg1Raw, err := tcp_config.LoadConfig(*config1Path)
	if err != nil {
		log.Fatalf("Lỗi đọc config 1 (%s): %v", *config1Path, err)
	}
	cfg1 := cfg1Raw.(*tcp_config.ClientConfig)

	cfg2Raw, err := tcp_config.LoadConfig(*config2Path)
	if err != nil {
		log.Fatalf("Lỗi đọc config 2 (%s): %v", *config2Path, err)
	}
	cfg2 := cfg2Raw.(*tcp_config.ClientConfig)

	// 2. Tạo 2 clients
	fmt.Println("🚀 Khởi tạo Client 1 (Sender)...")
	client1, err := client_tcp.NewClient(cfg1)
	if err != nil {
		log.Fatalf("Lỗi khởi tạo Client 1: %v", err)
	}
	fmt.Println("🚀 Khởi tạo Client 2 (Receiver)...")
	client2, err := client_tcp.NewClient(cfg2)
	if err != nil {
		log.Fatalf("Lỗi khởi tạo Client 2: %v", err)
	}

	addr1 := common.HexToAddress(cfg1.ParentAddress)
	addr2 := common.HexToAddress(cfg2.ParentAddress)

	fmt.Printf("Sender (Client 1): %s\n", addr1.Hex())
	fmt.Printf("Receiver (Client 2): %s\n", addr2.Hex())

	amount := big.NewInt(1000000)

	// 3. Client 1 gửi Native Transfer
	fmt.Println("⏳ Client 1 đang gửi Native Transfer...")

	// Gọi hàm tx_helper.SendTransaction từ Client 1
	receipt1, err := tx_helper.SendTransaction(
		"NativeTransfer",
		client1,
		cfg1,
		addr2, // to
		addr1, // from
		nil,   // data
		&tx_models.TxOptions{Amount: amount},
	)
	if err != nil {
		log.Fatalf("❌ Client 1 gửi giao dịch thất bại: %v", err)
	}

	txHash := receipt1.TransactionHash()
	fmt.Printf("✅ Client 1 đã nhận được Receipt! Hash: %s, Status: %s\n", txHash.Hex(), receipt1.Status().String())

	// 4. Client 2 lấy Receipt (vì receipt đã được broadcast và lưu trong buffer của Client 2)
	fmt.Println("⏳ Client 2 đang kiểm tra Receipt...")

	// Gọi FindReceiptByHash để lấy receipt từ pendingReceipts
	receipt2, err := client2.FindReceiptByHash(txHash)
	if err != nil {
		log.Fatalf("❌ Client 2 KHÔNG nhận được Receipt: %v", err)
	}

	fmt.Printf("✅ Client 2 ĐÃ NHẬN ĐƯỢC Receipt! Hash: %s, Status: %s\n", receipt2.TransactionHash().Hex(), receipt2.Status().String())
	fmt.Println("🎉 TEST HOÀN TẤT THÀNH CÔNG! CẢ SENDER VÀ RECEIVER ĐỀU NHẬN ĐƯỢC RECEIPT.")
}
