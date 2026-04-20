package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// 1. CẬP NHẬT ABI: Thêm field vào dispatch
const contractABI = `[
    {"anonymous":false,"inputs":[{"indexed":false,"internalType":"bytes32","name":"sessionId","type":"bytes32"},{"indexed":false,"internalType":"bytes32","name":"actionId","type":"bytes32"},{"indexed":false,"internalType":"address","name":"operator","type":"address"},{"indexed":false,"internalType":"bytes","name":"data","type":"bytes"}],"name":"EmitSentence","type":"event"},
    {"inputs":[{"internalType":"bytes32","name":"sessionId","type":"bytes32"},{"internalType":"bytes32","name":"actionId","type":"bytes32"},{"internalType":"bytes","name":"data","type":"bytes"},{"internalType":"uint256","name":"time","type":"uint256"},{"internalType":"bytes","name":"sig","type":"bytes"}],"name":"dispatch","outputs":[],"stateMutability":"nonpayable","type":"function"}
]`

func BuildMessageForDispatch(sessionId [32]byte, actionId [32]byte, data []byte, timestamp *big.Int) []byte {
	timestampBytes := make([]byte, 32)
	timestamp.FillBytes(timestampBytes)

	message := make([]byte, 0, 32+32+len(data)+32)
	message = append(message, sessionId[:]...)
	message = append(message, actionId[:]...)
	message = append(message, data...)
	message = append(message, timestampBytes...)

	return message
}

func main() {
	// 1. KẾT NỐI
	client, err := ethclient.Dial("ws://192.168.1.234:8545/interceptor")
	if err != nil {
		log.Fatal("Lỗi kết nối:", err)
	}

	contractAddr := common.HexToAddress("0xE74A88071fdc26f6b0453fE2B8b1d3e805b314E5")
	parsedABI, _ := abi.JSON(strings.NewReader(contractABI))

	// Thông tin Robot
	mySessionID := [32]byte{}
	copy(mySessionID[:], common.HexToHash("0x53455353494f4e5f303031000000000000000000000000000000000000000000").Bytes())
	actionMove := [32]byte{}
	copy(actionMove[:], common.HexToHash("0x414354494f4e5f4d4f5645000000000000000000000000000000000000000000").Bytes())

	// -------------------------------------------------------------------------
	// PHẦN 1: LẮNG NGHE SỰ KIỆN (Giữ nguyên cấu trúc lọc của bạn)
	// -------------------------------------------------------------------------
	go func() {
		query := ethereum.FilterQuery{
			Addresses: []common.Address{contractAddr},
		}

		logs := make(chan types.Log)
		sub, err := client.SubscribeFilterLogs(context.Background(), query, logs)
		if err != nil {
			log.Fatal("Lỗi Subscribe:", err)
		}

		fmt.Println("📡 [LISTENER] Hệ thống đang nghe sự kiện EmitSentence...")

		for {
			select {
			case err := <-sub.Err():
				log.Println("Lỗi luồng nghe:", err)
			case vLog := <-logs:
				// Giải mã sự kiện EmitSentence
				var event struct {
					SessionId [32]byte
					ActionId  [32]byte
					Operator  common.Address
					Data      []byte
				}

				// Unpack data từ logs
				err := parsedABI.UnpackIntoInterface(&event, "EmitSentence", vLog.Data)
				if err != nil {
					continue
				}

				// LOGIC LỌC (Sử dụng [32]byte trực tiếp để so sánh)
				if event.SessionId == mySessionID {
					fmt.Printf("\n📩 [NHẬN ĐƯỢC] Phản hồi từ Blockchain:\n")
					fmt.Printf("   👉 Session: %s\n", "SESSION_001")
					fmt.Printf("   👉 Operator: %s\n", event.Operator.Hex())
					fmt.Printf("   👉 Dữ liệu: %s\n", string(event.Data))
					fmt.Printf("   👉 TxHash: %s\n", vLog.TxHash.Hex())
				}
			}
		}
	}()

	time.Sleep(1 * time.Second)

	// -------------------------------------------------------------------------
	// PHẦN 2: GỬI LỆNH DISPATCH (Bổ sung Time và Sig)
	// -------------------------------------------------------------------------
	fmt.Println("\n🚀 [SENDER] Chuẩn bị ký và gửi Dispatch...")

	privateKey, _ := crypto.HexToECDSA("3f425fa96b85f8ece78f2a10350fa7af4643a4cdee02f36369833f45b0e003a7")
	publicKey := privateKey.Public().(*ecdsa.PublicKey)
	fromAddress := crypto.PubkeyToAddress(*publicKey)

	// A. Chuẩn bị Data và Timestamp
	dataPayload := []byte("Tốc độ: 10m/s, Hướng: Đông Bắc")
	nowTime := big.NewInt(time.Now().Unix())

	message := BuildMessageForDispatch(mySessionID, actionMove, dataPayload, nowTime)
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	signature, err := crypto.Sign(messageHash.Bytes(), privateKey)
	if err != nil {
		log.Fatal("Lỗi ký:", err)
	}
	signature[64] += 27 // Chuẩn hóa V cho Ethereum

	// C. Thiết lập giao dịch
	nonce, _ := client.PendingNonceAt(context.Background(), fromAddress)
	gasPrice, _ := client.SuggestGasPrice(context.Background())
	chainID, _ := client.ChainID(context.Background())

	auth, _ := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	auth.Nonce = big.NewInt(int64(nonce))
	auth.GasLimit = uint64(500000)
	auth.GasPrice = gasPrice

	// D. Gửi Dispatch: bổ sung nowTime và signature vào tham số cuối
	input, err := parsedABI.Pack("dispatch", mySessionID, actionMove, dataPayload, nowTime, signature)
	if err != nil {
		log.Fatal("Lỗi Pack ABI:", err)
	}

	tx := types.NewTransaction(nonce, contractAddr, big.NewInt(0), auth.GasLimit, gasPrice, input)
	signedTx, _ := auth.Signer(fromAddress, tx)

	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		log.Fatal("Gửi Dispatch thất bại:", err)
	}

	fmt.Printf("✅ [SENDER] Đã gửi thành công! TxHash: %s\n", signedTx.Hash().Hex())
	fmt.Printf("📝 [INFO] Time: %d, Sig: 0x%x\n", nowTime.Uint64(), signature)

	select {}
}
