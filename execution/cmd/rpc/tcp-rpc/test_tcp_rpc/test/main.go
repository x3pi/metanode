package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	client_tcp "github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp/utils/tx_helper"
)

func main() {
	logger.SetConfig(&logger.LoggerConfig{
		Flag:    logger.FLAG_INFO,
		Outputs: []*os.File{os.Stdout},
	})

	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║  TCP Pure Test: setValue → increaseValue → getValue  ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")

	// ─── 1. Load config + ABI ───
	configPath := "./config-main.json"
	abiPath := "abi.json"

	cfgRaw, err := tcp_config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("  ❌ LoadConfig: %v\n", err)
		return
	}
	cfg := cfgRaw.(*tcp_config.ClientConfig)

	abiBytes, err := os.ReadFile(abiPath)
	if err != nil {
		fmt.Printf("  ❌ ReadFile ABI: %v\n", err)
		return
	}
	demoABI, err := abi.JSON(strings.NewReader(string(abiBytes)))
	if err != nil {
		fmt.Printf("  ❌ Parse ABI: %v\n", err)
		return
	}

	// ─── 2. Khởi tạo TCP client (kết nối trực tiếp chain) ───
	fmt.Printf("\n  Connecting to: %s\n", cfg.ConnectionAddress())
	tcpClient, err := client_tcp.NewClient(cfg)
	if err != nil {
		fmt.Printf("  ❌ NewClient: %v\n", err)
		return
	}
	time.Sleep(1 * time.Second)

	fromAddr := common.HexToAddress(cfg.ParentAddress)
	contractAddr := common.HexToAddress(cfg.DemoContractAddress)

	fmt.Printf("  From:     %s\n", fromAddr.Hex())
	fmt.Printf("  Contract: %s\n", contractAddr.Hex())

	// ─── 3. getValue (trước) ───
	fmt.Println("\n─── Step 1: getValue (trước) ───")
	valueBefore := callGetValue(tcpClient, cfg, demoABI, contractAddr, fromAddr)
	if valueBefore != nil {
		fmt.Printf("  ✅ getValue() = %s\n", valueBefore.String())
	}

	// ─── 3.5. estimateGas setValue(1000) ───
	fmt.Println("\n─── Step 1.5: estimateGas setValue(1000) ───")
	gas := callEstimateGas(tcpClient, cfg, demoABI, contractAddr, fromAddr, "setValue", big.NewInt(1000))
	if gas != 0 {
		fmt.Printf("  ✅ estimated gas = %d\n", gas)
	}

	// ─── 4. setValue(1000) ───
	fmt.Println("\n─── Step 2: setValue(1000) ───")
	receipt := sendWriteTx(tcpClient, cfg, demoABI, contractAddr, fromAddr, "setValue", big.NewInt(1000))
	printReceipt("setValue", receipt)

	// // ─── 5. getValue (sau setValue) ───
	// fmt.Println("\n─── Step 3: getValue (sau setValue) ───")
	// valueAfterSet := callGetValue(tcpClient, cfg, demoABI, contractAddr, fromAddr)
	// if valueAfterSet != nil {
	// 	fmt.Printf("  ✅ getValue() = %s\n", valueAfterSet.String())
	// 	if valueAfterSet.Int64() == 1000 {
	// 		fmt.Println("  ✅ Đúng! (1000)")
	// 	} else {
	// 		fmt.Printf("  ❌ Expected 1000, got %s\n", valueAfterSet.String())
	// 	}
	// }

	// // ─── 6. increaseValue(250) ───
	// fmt.Println("\n─── Step 4: increaseValue(250) ───")
	// receipt2 := sendWriteTx(tcpClient, cfg, demoABI, contractAddr, fromAddr, "increaseValue", big.NewInt(250))
	// printReceipt("increaseValue", receipt2)

	// // ─── 5. getValue (sau increaseValue) ───
	// fmt.Println("\n─── Step 5: getValue (sau increaseValue) ───")
	// valueAfterIncrease := callGetValue(tcpClient, cfg, demoABI, contractAddr, fromAddr)
	// if valueAfterIncrease != nil {
	// 	fmt.Printf("  ✅ getValue() = %s\n", valueAfterIncrease.String())
	// 	expected := big.NewInt(0).Add(valueAfterSet, big.NewInt(250))
	// 	if valueAfterIncrease.Cmp(expected) == 0 {
	// 		fmt.Printf("  ✅ Đúng! (%s + 250 = %s)\n", valueAfterSet.String(), expected.String())
	// 	} else {
	// 		fmt.Printf("  ❌ Expected %s, got %s\n", expected.String(), valueAfterIncrease.String())
	// 	}
	// }

	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Println("║       TCP Pure Test completed!                       ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")

	fmt.Println("\n  [System] Chương trình đang chạy. Nhấn Ctrl+C để thoát...")
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	fmt.Println("  [System] Đã bắt được tín hiệu dừng. Thoát chương trình.")
}

// ─── Helpers ───

// callGetValue đọc getValue() bằng ReadTransaction (không tốn gas, không cần nonce)
func callGetValue(
	cli *client_tcp.Client,
	cfg *tcp_config.ClientConfig,
	demoABI abi.ABI,
	contractAddr common.Address,
	fromAddr common.Address,
) *big.Int {
	inputData, err := demoABI.Pack("getValue")
	if err != nil {
		fmt.Printf("  ❌ Pack getValue: %v\n", err)
		return nil
	}

	receipt, err := tx_helper.SendReadTransaction(
		"getValue",
		cli,
		cfg,
		contractAddr,
		fromAddr,
		inputData,
		nil,
	)
	if err != nil {
		fmt.Printf("  ❌ getValue: %v\n", err)
		return nil
	}

	returnData := receipt.Return()
	if len(returnData) == 0 {
		fmt.Println("  ⚠️ No return data")
		return nil
	}

	results, err := demoABI.Unpack("getValue", returnData)
	if err != nil {
		fmt.Printf("  ❌ Unpack getValue: %v\n", err)
		return nil
	}
	if len(results) > 0 {
		if val, ok := results[0].(*big.Int); ok {
			return val
		}
	}
	return nil
}

// callEstimateGas gửi giao dịch mô phỏng (estimate gas)
func callEstimateGas(
	cli *client_tcp.Client,
	cfg *tcp_config.ClientConfig,
	demoABI abi.ABI,
	contractAddr common.Address,
	fromAddr common.Address,
	method string,
	args ...interface{},
) uint64 {
	inputData, err := demoABI.Pack(method, args...)
	if err != nil {
		fmt.Printf("  ❌ Pack %s for estimate gas: %v\n", method, err)
		return 0
	}

	receipt, err := tx_helper.SendEstimateGas(
		"estimateGas "+method,
		cli,
		cfg,
		contractAddr,
		fromAddr,
		inputData,
		nil,
	)
	if err != nil {
		fmt.Printf("  ❌ estimateGas: %v\n", err)
		return 0
	}

	if receipt == nil {
		fmt.Println("  ⚠️ No return data")
		return 0
	}

	return receipt.GasUsed()
}

// sendWriteTx gửi write transaction bằng SendTransaction (BLS, chờ receipt)
func sendWriteTx(
	cli *client_tcp.Client,
	cfg *tcp_config.ClientConfig,
	demoABI abi.ABI,
	contractAddr common.Address,
	fromAddr common.Address,
	method string,
	args ...interface{},
) *pb.Receipt {
	inputData, err := demoABI.Pack(method, args...)
	if err != nil {
		fmt.Printf("  ❌ Pack %s: %v\n", method, err)
		return nil
	}

	receipt, err := tx_helper.SendTransaction(
		method,
		cli,
		cfg,
		contractAddr,
		fromAddr,
		inputData,
		nil,
	)
	if err != nil {
		fmt.Printf("  ❌ %s TX error: %v\n", method, err)
		return nil
	}

	if receipt == nil {
		fmt.Printf("  ⚠️ %s: receipt nil\n", method)
		return nil
	}

	// Convert types.Receipt → pb.Receipt để hiển thị
	pbReceipt := &pb.Receipt{
		TransactionHash: receipt.TransactionHash().Bytes(),
		Status:          receipt.Status(),
		GasUsed:         receipt.GasUsed(),
	}
	return pbReceipt
}

// printReceipt hiển thị receipt đẹp
func printReceipt(method string, rcpt *pb.Receipt) {
	if rcpt == nil {
		fmt.Printf("  ⚠️ %s: no receipt\n", method)
		return
	}
	txHash := common.BytesToHash(rcpt.TransactionHash).Hex()
	status := "SUCCESS"
	if rcpt.Status == pb.RECEIPT_STATUS_THREW || rcpt.Status == pb.RECEIPT_STATUS_TRANSACTION_ERROR {
		status = "FAILED"
	}
	fmt.Printf("  ✅ %s receipt:\n", method)
	fmt.Printf("     TxHash:  %s\n", txHash)
	fmt.Printf("     Status:  %s (%d)\n", status, rcpt.Status)
	fmt.Printf("     GasUsed: %d\n", rcpt.GasUsed)

	// In receipt JSON cho debug
	rcptJSON, _ := json.MarshalIndent(map[string]interface{}{
		"txHash":  txHash,
		"status":  status,
		"gasUsed": rcpt.GasUsed,
	}, "     ", "  ")
	fmt.Printf("     JSON: %s\n", string(rcptJSON))
}
