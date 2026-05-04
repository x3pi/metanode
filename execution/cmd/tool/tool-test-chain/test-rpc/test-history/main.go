package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type Config struct {
	RPCUrl     string `json:"rpc_url"`
	PrivateKey string `json:"private_key"`
	ChainID    int64  `json:"chain_id"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// sendTx thực hiện gửi 1 giao dịch và chờ mạng mining, sau đó trả về block number chứa giao dịch đó
func sendTxAndWait(ethCli *ethclient.Client, privateKey *ecdsa.PrivateKey, chainId int64, fromAddress common.Address, toAddress common.Address) (uint64, error) {
	ctx := context.Background()
	nonce, err := ethCli.PendingNonceAt(ctx, fromAddress)
	if err != nil {
		return 0, err
	}
	gasPrice, err := ethCli.SuggestGasPrice(ctx)
	if err != nil {
		return 0, err
	}

	tx := types.NewTransaction(nonce, toAddress, big.NewInt(100), 21000, gasPrice, nil)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainId)), privateKey)
	if err != nil {
		return 0, err
	}

	err = ethCli.SendTransaction(ctx, signedTx)
	if err != nil {
		return 0, err
	}
	fmt.Printf("   🚀 Đã gửi Tx. Hash: %s\n", signedTx.Hash().Hex())

	for {
		receipt, err := ethCli.TransactionReceipt(ctx, signedTx.Hash())
		if err == nil {
			if receipt.Status == 1 {
				return receipt.BlockNumber.Uint64(), nil
			}
			return 0, fmt.Errorf("Tx Reverted")
		} else if err != ethereum.NotFound {
			return 0, err
		}
		time.Sleep(1 * time.Second)
	}
}

func main() {
	configFlag := flag.String("config", "config-local.json", "Đường dẫn file cấu hình (ví dụ: config-server.json)")
	waitBlocksFlag := flag.Uint64("wait", 0, "Số block cần đợi mạng lưới sinh ra trước khi test mốc B (để test Pruning)")
	flag.Parse()

	cfg, err := loadConfig(*configFlag)
	if err != nil {
		log.Fatalf("Failed to load config %s: %v", *configFlag, err)
	}

	rpcClient, err := rpc.Dial(cfg.RPCUrl)
	if err != nil {
		log.Fatalf("Failed to connect to RPC %s: %v", cfg.RPCUrl, err)
	}
	defer rpcClient.Close()
	ethCli := ethclient.NewClient(rpcClient)

	// Chuẩn bị ví
	privateKey, err := crypto.HexToECDSA(cfg.PrivateKey)
	if err != nil {
		log.Fatalf("Lỗi parse private key: %v", err)
	}
	publicKey := privateKey.Public()
	fromAddress := crypto.PubkeyToAddress(*publicKey.(*ecdsa.PublicKey))
	toAddress := common.HexToAddress("0x0000000000000000000000000000000000001002")

	fmt.Println("=====================================================")
	fmt.Printf("BẮT ĐẦU TEST LỊCH SỬ STATE (Từ ví: %s)\n", fromAddress.Hex())
	fmt.Printf("Sử dụng cấu hình: %s\n", *configFlag)
	fmt.Println("=====================================================")

	ctx := context.Background()
	var blockA, blockB uint64

	// Luôn tự động gửi Tx 1 để chốt mốc Block A
	fmt.Println("\n1. Đang tạo giao dịch (Tx) để chốt trạng thái vào Block A...")
	bA, err := sendTxAndWait(ethCli, privateKey, cfg.ChainID, fromAddress, toAddress)
	if err != nil {
		log.Fatalf("Gửi Tx thất bại: %v", err)
	}
	blockA = bA
	fmt.Printf("✅ Giao dịch đã được mine tại Block A: %d\n", blockA)

	if *waitBlocksFlag > 0 {
		blockB = blockA + *waitBlocksFlag
		fmt.Printf("\n⏳ Chế độ Test Pruning: Đang đợi mạng lưới chạy tới Block B (%d)...\n", blockB)
		for {
			var latestBlockHex string
			err = rpcClient.CallContext(ctx, &latestBlockHex, "eth_blockNumber")
			if err == nil {
				latestBlock, _ := hexutil.DecodeUint64(latestBlockHex)
				if latestBlock >= blockB {
					fmt.Printf("✅ Mạng lưới đã đạt Block %d!\n", latestBlock)
					break
				}
				fmt.Printf("   Đang ở block %d, cần đợi tới %d (Đang bắn Tx để kích block)...\n", latestBlock, blockB)
				
				// Bắn giao dịch để ép mạng lưới tạo block mới
				_, errTx := sendTxAndWait(ethCli, privateKey, cfg.ChainID, fromAddress, toAddress)
				if errTx != nil {
					fmt.Printf("   ⚠️ Lỗi khi bắn Tx kích block: %v\n", errTx)
					time.Sleep(2 * time.Second)
				}
			} else {
				time.Sleep(2 * time.Second)
			}
		}
	} else {
		// Test nhanh: Gửi thêm Tx 2 để lấy Block B ngay lập tức
		fmt.Println("\n2. Đang tạo giao dịch (Tx 2) để làm thay đổi số dư so với Block A...")
		bB, err := sendTxAndWait(ethCli, privateKey, cfg.ChainID, fromAddress, toAddress)
		if err != nil {
			log.Fatalf("Gửi Tx 2 thất bại: %v", err)
		}
		blockB = bB
		fmt.Printf("✅ Tx 2 đã được mine tại Block B: %d\n", blockB)
	}

	fmt.Println("\n=====================================================")
	fmt.Println("BẮT ĐẦU KIỂM TRA LỊCH SỬ BẰNG RPC TẠI 2 MỐC BLOCK KHÁC NHAU")
	fmt.Println("=====================================================")

	blockAHex := hexutil.EncodeUint64(blockA)
	blockBHex := hexutil.EncodeUint64(blockB)

	// Lấy Balance ở Block A
	var balanceAHex string
	err = rpcClient.CallContext(ctx, &balanceAHex, "eth_getBalance", fromAddress, blockAHex)
	balanceA, _ := hexutil.DecodeBig(balanceAHex)

	// Lấy Balance ở Block B
	var balanceBHex string
	err = rpcClient.CallContext(ctx, &balanceBHex, "eth_getBalance", fromAddress, blockBHex)
	balanceB, _ := hexutil.DecodeBig(balanceBHex)

	fmt.Printf("💰 eth_getBalance tại Block A (%d): %v\n", blockA, balanceA)
	fmt.Printf("💰 eth_getBalance tại Block B (%d): %v\n", blockB, balanceB)

	if balanceA.Cmp(balanceB) == 0 {
		fmt.Println("⚠️  LỖI: Số dư ở Block A và Block B GIỐNG HỆT NHAU. Chứng tỏ rpc_state.go đang load nhầm state hiện tại thay vì lịch sử!")
	} else {
		fmt.Println("✅ Số dư có sự khác biệt rõ ràng -> Lấy đúng state quá khứ!")
	}

	// Kiểm tra Nonce (TransactionCount)
	var nonceAHex string
	rpcClient.CallContext(ctx, &nonceAHex, "eth_getTransactionCount", fromAddress, blockAHex)
	nonceA, _ := hexutil.DecodeUint64(nonceAHex)

	var nonceBHex string
	rpcClient.CallContext(ctx, &nonceBHex, "eth_getTransactionCount", fromAddress, blockBHex)
	nonceB, _ := hexutil.DecodeUint64(nonceBHex)

	fmt.Printf("\n🔢 eth_getTransactionCount tại Block A (%d): %d\n", blockA, nonceA)
	fmt.Printf("🔢 eth_getTransactionCount tại Block B (%d): %d\n", blockB, nonceB)

	if nonceA == nonceB {
		fmt.Println("⚠️  LỖI: Nonce ở Block A và Block B GIỐNG HỆT NHAU.")
	} else {
		fmt.Println("✅ Nonce có sự khác biệt rõ ràng -> State Root historical hoạt động tốt!")
	}
}