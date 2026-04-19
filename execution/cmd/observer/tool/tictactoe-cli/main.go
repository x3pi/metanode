package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/joho/godotenv"
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func deployRawTx(client *ethclient.Client, privateKey *ecdsa.PrivateKey, chainID *big.Int, from common.Address, deployData []byte) (common.Hash, common.Address, error) {
	ctx := context.Background()
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, common.Address{}, err
	}
	gasPrice, _ := client.SuggestGasPrice(ctx)
	if gasPrice == nil {
		gasPrice = big.NewInt(0)
	}
	gasLimit := uint64(5_000_000)
	tx := types.NewContractCreation(nonce, big.NewInt(0), gasLimit, gasPrice, deployData)
	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, privateKey)
	if err != nil {
		return common.Hash{}, common.Address{}, err
	}
	err = client.SendTransaction(ctx, signedTx)
	if err != nil {
		return common.Hash{}, common.Address{}, err
	}
	contractAddr := crypto.CreateAddress(from, nonce)
	return signedTx.Hash(), contractAddr, nil
}

func sendRawTx(client *ethclient.Client, privateKey *ecdsa.PrivateKey, chainID *big.Int, from common.Address, to common.Address, data []byte) (common.Hash, error) {
	ctx := context.Background()
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, err
	}
	gasPrice, _ := client.SuggestGasPrice(ctx)
	if gasPrice == nil {
		gasPrice = big.NewInt(0)
	}
	gasLimit := uint64(500_000)
	tx := types.NewTransaction(nonce, to, big.NewInt(0), gasLimit, gasPrice, data)
	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, privateKey)
	if err != nil {
		return common.Hash{}, err
	}
	err = client.SendTransaction(ctx, signedTx)
	if err != nil {
		return common.Hash{}, err
	}
	return signedTx.Hash(), nil
}

func waitForReceipt(client *ethclient.Client, txHash common.Hash) *types.Receipt {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		receipt, err := client.TransactionReceipt(ctx, txHash)
		if err == nil && receipt != nil {
			return receipt
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(1 * time.Second):
		}
	}
}

func callView(client *ethclient.Client, contractAddr common.Address, parsedABI abi.ABI, method string, args ...interface{}) ([]interface{}, error) {
	callData, err := parsedABI.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	msg := ethereum.CallMsg{To: &contractAddr, Data: callData}
	res, err := client.CallContract(context.Background(), msg, nil)
	if err != nil {
		return nil, err
	}
	return parsedABI.Unpack(method, res)
}

func main() {
	godotenv.Load()
	httpURL := getEnv("HTTP_URL", "http://192.168.1.234:8545")
	privateKeyHex := getEnv("PRIVATE_KEY", "")
	if privateKeyHex == "" {
		log.Fatal("❌ PRIVATE_KEY is required in .env")
	}

	client, err := ethclient.Dial(httpURL)
	if err != nil {
		log.Fatalf("❌ Connect failed: %v", err)
	}

	chainID, _ := client.ChainID(context.Background())
	privateKey, _ := crypto.HexToECDSA(strings.TrimPrefix(privateKeyHex, "0x"))
	publicKeyECDSA := privateKey.Public().(*ecdsa.PublicKey)
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	abiFile, err := os.ReadFile("abi/tic_tac_toe.json")
	if err != nil {
		log.Fatal("Cannot read ABI:", err)
	}
	parsedABI, err := abi.JSON(strings.NewReader(string(abiFile)))
	if err != nil {
		log.Fatal("Parse ABI error:", err)
	}

	fmt.Printf("🎯 Tic-Tac-Toe CrossChain CLI\n")
	fmt.Printf("🔗 Chain ID: %s | 👤 Wallet: %s\n", chainID.String(), fromAddress.Hex())

	var contractAddr common.Address
	savedConfig, _ := os.ReadFile("tcc_address.txt")
	if len(savedConfig) > 0 {
		contractAddr = common.HexToAddress(strings.TrimSpace(string(savedConfig)))
	}

	for {
		fmt.Println("\n═════════════════════════════════")
		fmt.Println("1. 🚀 Deploy mới TicTacToe contract")
		fmt.Printf("2. 🎯 Tùy chỉnh địa chỉ (Hiện tại: %s)\n", contractAddr.Hex())
		fmt.Println("3. 🏁 Khởi tạo game [startGame]")
		fmt.Println("4. 🕹️ Đánh cờ [playMove]")
		fmt.Println("5. 👁️  Xem bàn cờ [Board & Status]")
		fmt.Println("6. 🤖 Bật Bot Auto Play (Tự động đánh khi tới lượt)")
		fmt.Println("0. Thoát")
		fmt.Print("Chọn: ")
		var choice string
		fmt.Scanln(&choice)

		switch choice {
		case "1":
			bcFile, err := os.ReadFile("byteCode/byteCode.json")
			if err != nil {
				fmt.Println("❌ Không đọc được byteCode.json:", err)
				continue
			}
			var bcData struct {
				TicTacToe string `json:"tic_tac_toe"`
			}
			json.Unmarshal(bcFile, &bcData)
			bytecodeHex := strings.TrimPrefix(bcData.TicTacToe, "0x")
			deployData := common.FromHex(bytecodeHex)

			fmt.Println("🔄 Đang gửi lệnh Deploy...")
			hash, addr, err := deployRawTx(client, privateKey, chainID, fromAddress, deployData)
			if err != nil {
				fmt.Println("❌ Deploy lỗi:", err)
				continue
			}
			fmt.Printf("📤 Tx Deploy: %s\n", hash.Hex())
			rcp := waitForReceipt(client, hash)
			if rcp != nil && rcp.Status == 1 {
				fmt.Printf("✅ Đã deploy thành công: %s\n", addr.Hex())
				contractAddr = addr
				os.WriteFile("tcc_address.txt", []byte(addr.Hex()), 0644)
			} else {
				fmt.Println("❌ Lỗi Deploy (hết gas hoặc revert)")
			}

		case "2":
			fmt.Print("Nhập địa chỉ TicTacToe TẠI CHAIN NÀY: ")
			var addrStr string
			fmt.Scanln(&addrStr)
			contractAddr = common.HexToAddress(addrStr)
			os.WriteFile("tcc_address.txt", []byte(contractAddr.Hex()), 0644)
			fmt.Println("✅ Đã lưu.")

		case "3":
			if contractAddr == (common.Address{}) {
				fmt.Println("❌ Vui lòng deploy hoặc set địa chỉ trước!")
				continue
			}
			fmt.Print("Nhập Chain ID đối thủ (vd: 2): ")
			var oppChainStr string
			fmt.Scanln(&oppChainStr)
			oppChain, ok := new(big.Int).SetString(oppChainStr, 10)
			if !ok {
				fmt.Println("❌ Sai định dạng số")
				continue
			}

			fmt.Print("Nhập Địa chỉ Contract tại chain đối thủ: ")
			var oppAddrStr string
			fmt.Scanln(&oppAddrStr)
			oppAddr := common.HexToAddress(oppAddrStr)

			fmt.Print("Nhập Địa chỉ Ví của đối thủ (Player O): ")
			var oppWalletStr string
			fmt.Scanln(&oppWalletStr)
			oppWallet := common.HexToAddress(oppWalletStr)

			callData, _ := parsedABI.Pack("startGame", oppChain, oppAddr, oppWallet)
			fmt.Println("🔄 Đang gửi sự kiện startGame...")
			hash, err := sendRawTx(client, privateKey, chainID, fromAddress, contractAddr, callData)
			if err != nil {
				fmt.Println("❌ Lỗi gửi:", err)
				continue
			}
			fmt.Printf("📤 Tx: %s\n", hash.Hex())
			rcp := waitForReceipt(client, hash)
			if rcp != nil && rcp.Status == 1 {
				fmt.Println("✅ Bắt đầu Game thành công! Chờ Observer xử lý...")
			} else {
				fmt.Println("❌ Revert! (Lỗi logic contract or Not Started)")
			}

		case "4":
			if contractAddr == (common.Address{}) {
				fmt.Println("❌ Chưa set contract!")
				continue
			}
			fmt.Print("Nhập vị trí nước cờ (0 đến 8): ")
			var posStr string
			fmt.Scanln(&posStr)
			var pos int
			if _, err := fmt.Sscanf(posStr, "%d", &pos); err != nil {
				fmt.Println("❌ Nhập số không hợp lệ.")
				continue
			}

			callData, _ := parsedABI.Pack("playMove", uint8(pos))
			hash, err := sendRawTx(client, privateKey, chainID, fromAddress, contractAddr, callData)
			if err != nil {
				fmt.Println("❌ Lỗi gửi:", err)
				continue
			}
			fmt.Printf("📤 Tx: %s\n", hash.Hex())
			rcp := waitForReceipt(client, hash)
			if rcp != nil && rcp.Status == 1 {
				fmt.Println("✅ Đi cờ thành công! Đang bắn sang Chain của đối thủ...")
			} else {
				fmt.Println("❌ Lỗi đi cờ (Có thể không phải lượt bạn, hoặc ô đã đánh)")
			}

		case "5":
			if contractAddr == (common.Address{}) {
				fmt.Println("❌ Chưa set contract!")
				continue
			}
			symbols := []string{"■", "X", "O"}
			b := make([]string, 9)
			for i := 0; i < 9; i++ {
				res, err := callView(client, contractAddr, parsedABI, "board", big.NewInt(int64(i)))
				if err == nil && len(res) > 0 {
					b[i] = symbols[res[0].(uint8)]
				} else {
					b[i] = "?"
				}
			}
			fmt.Printf("\n==== BÀN CỜ TẠI CHAIN %s ====\n", chainID.String())
			fmt.Printf(" %s  |  %s  |  %s \n", b[0], b[1], b[2])
			fmt.Printf("---------------\n")
			fmt.Printf(" %s  |  %s  |  %s \n", b[3], b[4], b[5])
			fmt.Printf("---------------\n")
			fmt.Printf(" %s  |  %s  |  %s \n", b[6], b[7], b[8])

			turnRes, _ := callView(client, contractAddr, parsedABI, "currentTurn")
			if turnRes != nil && len(turnRes) > 0 {
				fmt.Printf("\n🕹️ Lượt người số: %s\n", symbols[turnRes[0].(uint8)])
			}

			oppRes, _ := callView(client, contractAddr, parsedABI, "opponentContract")
			if oppRes != nil && len(oppRes) > 0 {
				fmt.Printf("📍 Khóa đích       : %s\n", oppRes[0].(common.Address).Hex())
			}

			pxRes, _ := callView(client, contractAddr, parsedABI, "playerX")
			if pxRes != nil && len(pxRes) > 0 {
				fmt.Printf("👤 Người chơi X    : %s\n", pxRes[0].(common.Address).Hex())
			}
			poRes, _ := callView(client, contractAddr, parsedABI, "playerO")
			if poRes != nil && len(poRes) > 0 {
				fmt.Printf("👤 Người chơi O    : %s\n", poRes[0].(common.Address).Hex())
			}

			winRes, _ := callView(client, contractAddr, parsedABI, "winner")
			if winRes != nil && len(winRes) > 0 {
				w := winRes[0].(uint8)
				if w == 1 {
					fmt.Println("🏆 TRẠNG THÁI: X Thắng Ván!")
				} else if w == 2 {
					fmt.Println("🏆 TRẠNG THÁI: O Thắng Ván!")
				} else if w == 3 {
					fmt.Println("🤝 TRẠNG THÁI: Hòa Cờ!")
				} else {
					fmt.Println("⚔️ TRẠNG THÁI: Đang Chiến...")
				}
			}

		case "6":
			if contractAddr == (common.Address{}) {
				fmt.Println("❌ Chưa set contract!")
				continue
			}
			fmt.Println("🤖 Bot Event-Driven đã được bật! (Nhấn Ctrl+C để thoát)")
			pxRes, _ := callView(client, contractAddr, parsedABI, "playerX")
			poRes, _ := callView(client, contractAddr, parsedABI, "playerO")
			var myRole uint8 = 0
			if pxRes != nil && len(pxRes) > 0 && pxRes[0].(common.Address) == fromAddress {
				myRole = 1
			} else if poRes != nil && len(poRes) > 0 && poRes[0].(common.Address) == fromAddress {
				myRole = 2
			}
			if myRole == 0 {
				fmt.Println("❌ Bạn không phải là Player X hay Player O trong ván cờ này!")
				continue
			}
			fmt.Printf("🎯 Vai trò của bạn: %d (1=X, 2=O). Đang subscribe MovePlayed...\n", myRole)

			// ── Kết nối WebSocket để subscribe events ──
			wsURL := strings.Replace(httpURL, "http://", "ws://", 1)
			wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
			wsClient, errWs := ethclient.Dial(wsURL)
			if errWs != nil {
				fmt.Printf("⚠️  Không kết nối WebSocket được (%v), fallback về polling...\n", errWs)
				// --- Fallback: polling ---
				for {
					time.Sleep(2 * time.Second)
					winRes, _ := callView(client, contractAddr, parsedABI, "winner")
					if winRes != nil && len(winRes) > 0 && winRes[0].(uint8) != 0 {
						fmt.Printf("\n🏆 Trò chơi kết thúc! Winner = %d\n", winRes[0].(uint8))
						break
					}
					turnRes, _ := callView(client, contractAddr, parsedABI, "currentTurn")
					if turnRes == nil || len(turnRes) == 0 {
						continue
					}
					if turnRes[0].(uint8) == myRole {
						botPlayFirstEmpty(client, privateKey, chainID, fromAddress, contractAddr, parsedABI)
					}
				}
				continue
			}
			defer wsClient.Close()

			// Subscribe MovePlayed event
			movePlayedSig := parsedABI.Events["MovePlayed"].ID
			query := ethereum.FilterQuery{
				Addresses: []common.Address{contractAddr},
				Topics:    [][]common.Hash{{movePlayedSig}},
			}
			logsCh := make(chan types.Log)
			sub, errSub := wsClient.SubscribeFilterLogs(context.Background(), query, logsCh)
			if errSub != nil {
				fmt.Printf("❌ Subscribe thất bại: %v\n", errSub)
				continue
			}
			fmt.Println("✅ Đã subscribe MovePlayed! Đang chờ lượt...")

			// Nếu ván đã bắt đầu và đang là lượt của mình → đánh ngay
			turnRes, _ := callView(client, contractAddr, parsedABI, "currentTurn")
			if turnRes != nil && len(turnRes) > 0 && turnRes[0].(uint8) == myRole {
				fmt.Println("⚡ Đang là lượt bot, đánh ngay!")
				botPlayFirstEmpty(client, privateKey, chainID, fromAddress, contractAddr, parsedABI)
			}

			gameOver := false
			for !gameOver {
				select {
				case err := <-sub.Err():
					fmt.Printf("❌ Subscription error: %v\n", err)
					gameOver = true
				case vlog := <-logsCh:
					// Unpack MovePlayed(uint8 player, uint8 position) — ABI-encoded, 32 bytes each
					if len(vlog.Data) < 64 {
						continue
					}
					player := vlog.Data[31]
					position := vlog.Data[63]
					fmt.Printf("📢 Event: Player %d vừa đánh ô %d\n", player, position)

					// Kiểm tra game đã kết thúc chưa
					winRes, _ := callView(client, contractAddr, parsedABI, "winner")
					if winRes != nil && len(winRes) > 0 && winRes[0].(uint8) != 0 {
						fmt.Printf("\n🏆 Game over! Winner = %d\n", winRes[0].(uint8))
						gameOver = true
						continue
					}
					// Nếu đối thủ vừa đánh → đến lượt mình
					if player != myRole {
						fmt.Println("⚡ ĐẾN LƯỢT BOT!")
						botPlayFirstEmpty(client, privateKey, chainID, fromAddress, contractAddr, parsedABI)
					}
				}
			}
			sub.Unsubscribe()

		case "0":
			return
		}
	}
}

// botPlayFirstEmpty tìm ô trống đầu tiên và gửi playMove.
func botPlayFirstEmpty(
	client *ethclient.Client,
	privateKey *ecdsa.PrivateKey,
	chainID *big.Int,
	from common.Address,
	contractAddr common.Address,
	parsedABI abi.ABI,
) {
	for i := 0; i < 9; i++ {
		res, err := callView(client, contractAddr, parsedABI, "board", big.NewInt(int64(i)))
		if err == nil && len(res) > 0 && res[0].(uint8) == 0 {
			fmt.Printf("🤖 Bot đánh ô: %d\n", i)
			callData, _ := parsedABI.Pack("playMove", uint8(i))
			hash, err := sendRawTx(client, privateKey, chainID, from, contractAddr, callData)
			if err != nil {
				fmt.Println("❌ Lỗi gửi Tx:", err)
				return
			}
			fmt.Printf("📤 Tx Bot: %s (chờ receipt...)\n", hash.Hex())
			waitForReceipt(client, hash)
			fmt.Println("✅ Đi cờ xong! Đợi đối thủ...")
			return
		}
	}
	fmt.Println("⚠️  Không tìm thấy ô trống!")
}
