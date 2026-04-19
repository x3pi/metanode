// send_dual_tx.go
//
// Gửi N TX đồng thời vào CÙNG 1 contract (cùng mvmId) sử dụng nhiều key khác nhau.
// Mỗi key chạy trong 1 goroutine riêng → gửi song song, tối đa cơ hội land cùng block.
//
// Mục đích: Kiểm chứng vấn đề trong block_processor_commit.go:
//   - Khi 2+ TX có cùng ToAddress (mvmId) trong 1 block, chỉ TX đầu được CommitFullDb()
//   - Câu hỏi: Data của TX2, TX3... có bị mất không?
//
// Keys dùng: generated_keys.json (index 0..N-1)
//   Key 0: 546bb8b8... → 0x39671a3610DA65b94F098511f5Cb8d4577F7e030
//   Key 1: 994cb756... → 0x27A01FbB4C80CE65C8bb54e45A13dAEB099d76C6
//   Key 2: cc00cd10... → 0xE9521FBaD2286af75Bea8EBFeb5C72b8272f5Db7
//   Key 3: 350131522... → 0xf77e4B0ab90c0d81C540C5e2749BD14Ff396e53A
//   Key 4: 2263ec54... → 0xD7895C33138E6654d0DDd380164d4893134bCC88
//   ...
//
// Cách dùng:
//   go run send_dual_tx.go -contract 0xADDRESS
//   go run send_dual_tx.go -contract 0xADDRESS -workers 5 -rpc http://192.168.1.234:8545 -chain 991
//
// QUAN TRỌNG: Mỗi address phải có đủ ETH để trả gas.

//go:build ignore

package main

import (
	"context"
	"crypto/ecdsa"
	"flag"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Keys từ generated_keys.json — bóc đại 10 key đầu
var spamKeys = []struct {
	PrivateKey string
	Address    string
}{
	{"546bb8b88d5db8c1be24fd04ccaec199c4ebfc7f6363cf6d92d65b69379fcb96", "0x39671a3610DA65b94F098511f5Cb8d4577F7e030"},
	{"994cb75638c787ef1d3a7bf1952f23036026dfe0f234511ee755c71448f37279", "0x27A01FbB4C80CE65C8bb54e45A13dAEB099d76C6"},
	{"cc00cd10cc23d7e7d9d2666c9aaa8ef8d1e61e4d7b67f348206b1f88eed6860d", "0xE9521FBaD2286af75Bea8EBFeb5C72b8272f5Db7"},
	{"350131522baa6dc507808fd69644f2cf538ff3a1bef81aed9406684afe42d942", "0xf77e4B0ab90c0d81C540C5e2749BD14Ff396e53A"},
	{"2263ec543b1476f8b8dd99cffb07c108c65d116639027755f17fddefd8bd78f6", "0xD7895C33138E6654d0DDd380164d4893134bCC88"},
	{"b90fe2e7de84dd2ffb05a717a1ff62f90ff8b2c530ced98e1d011eabe9eddeab", "0x118A853AD555544AfA4b577548da83aFff3Cd86d"},
	{"484674a9ff76fec2ab8ad748476b9d8cb7be103982baa134e76eb214205d683b", "0xbeF7B39bA9C39EB95845Dbd45aDbfE043946CF2C"},
	{"6d6b9fdedfbfa28e2cc2efdd46b6fc25af18fd8431bdd90a544515ca8d9f1c16", "0xc40148Be56CcE77937D9EBFa3FD4a8e278468Cf4"},
	{"2f02cf949c678852400b734f19d5a6cf830e362eadcba68e09f7a66b7b54eee4", "0x4EE6A2d4f9c51CD3A1df7bDa486c3689224C5f68"},
	{"e1405c8edf22ef9079f796edac9d4bd3937c84e869e26e711b98df480e7258d3", "0x1dCAde22F0e6E13f0Ba84D3c621F10ceCA46dC10"},
}

// ABI của DualTxXapianTest — chỉ dùng hàm insertDoc + events
const contractABI = `[
  {
    "inputs": [
      {"internalType": "uint256", "name": "slot", "type": "uint256"},
      {"internalType": "string",  "name": "data", "type": "string"}
    ],
    "name": "insertDoc",
    "outputs": [],
    "stateMutability": "nonpayable",
    "type": "function"
  },
  {
    "inputs": [
      {"internalType": "uint256", "name": "slot", "type": "uint256"}
    ],
    "name": "readDoc",
    "outputs": [
      {"internalType": "string", "name": "data", "type": "string"}
    ],
    "stateMutability": "nonpayable",
    "type": "function"
  },
  {
    "inputs": [
      {"internalType": "uint256", "name": "", "type": "uint256"}
    ],
    "name": "realDocIds",
    "outputs": [
      {"internalType": "uint256", "name": "", "type": "uint256"}
    ],
    "stateMutability": "view",
    "type": "function"
  },
  {
    "anonymous": false,
    "inputs": [
      {"indexed": false, "internalType": "uint256", "name": "inputSlot",  "type": "uint256"},
      {"indexed": false, "internalType": "uint256", "name": "realDocId",  "type": "uint256"},
      {"indexed": false, "internalType": "string",  "name": "data",       "type": "string"}
    ],
    "name": "DocInserted",
    "type": "event"
  },
  {
    "anonymous": false,
    "inputs": [
      {"indexed": false, "internalType": "uint256", "name": "realDocId", "type": "uint256"},
      {"indexed": false, "internalType": "string",  "name": "data",      "type": "string"},
      {"indexed": false, "internalType": "bool",    "name": "isEmpty",   "type": "bool"}
    ],
    "name": "DocRead",
    "type": "event"
  }
]`

type txResult struct {
	slot        int
	txHash      common.Hash
	blockNumber uint64
	status      uint64
	gasUsed     uint64
	err         error
}

func main() {
	contractFlag := flag.String("contract", "", "Contract address của DualTxXapianTest (bắt buộc)")
	rpcFlag := flag.String("rpc", "http://192.168.1.234:8545", "RPC URL")
	chainFlag := flag.Int64("chain", 991, "Chain ID")
	workersFlag := flag.Int("workers", 5, "Số luồng song song (1-10, dùng key index 0..N-1)")
	flag.Parse()

	if *contractFlag == "" {
		log.Fatal("❌ Bắt buộc truyền -contract 0xADDRESS")
	}
	if *workersFlag < 1 || *workersFlag > len(spamKeys) {
		log.Fatalf("❌ -workers phải từ 1 đến %d", len(spamKeys))
	}

	nWorkers := *workersFlag
	contractAddr := common.HexToAddress(*contractFlag)

	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println("🧪 MULTI-KEY PARALLEL TX XAPIAN COMMIT TEST")
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Printf("📡 RPC:      %s\n", *rpcFlag)
	fmt.Printf("📄 Contract: %s\n", contractAddr.Hex())
	fmt.Printf("⛓️  Chain ID: %d\n", *chainFlag)
	fmt.Printf("👥 Workers:  %d goroutines, mỗi key gửi 1 TX\n", nWorkers)
	fmt.Println()

	// Parse ABI
	parsedABI, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		log.Fatalf("Lỗi parse ABI: %v", err)
	}

	// In danh sách keys sẽ dùng
	fmt.Println("🔑 Keys sử dụng:")
	for i := 0; i < nWorkers; i++ {
		fmt.Printf("   [%d] %s\n", i, spamKeys[i].Address)
	}
	fmt.Println()

	// Channel thu kết quả
	results := make(chan txResult, nWorkers)
	var wg sync.WaitGroup

	// Barrier: chờ tất cả goroutine sẵn sàng rồi gửi đồng loạt
	startGun := make(chan struct{})

	// ══════════════════════════════════════════════════════════════
	// Khởi động N goroutine, mỗi goroutine = 1 key = 1 TX
	// ══════════════════════════════════════════════════════════════
	for i := 0; i < nWorkers; i++ {
		wg.Add(1)
		go func(slot int, keyInfo struct{ PrivateKey, Address string }) {
			defer wg.Done()

			// Kết nối riêng cho mỗi goroutine (tránh race)
			client, err := ethclient.Dial(*rpcFlag)
			if err != nil {
				results <- txResult{slot: slot, err: fmt.Errorf("dial: %v", err)}
				return
			}
			defer client.Close()

			// Parse key
			privateKey, err := crypto.HexToECDSA(keyInfo.PrivateKey)
			if err != nil {
				results <- txResult{slot: slot, err: fmt.Errorf("parse key: %v", err)}
				return
			}
			fromAddr := crypto.PubkeyToAddress(*privateKey.Public().(*ecdsa.PublicKey))

			// Lấy nonce + gasPrice
			nonce, err := client.PendingNonceAt(context.Background(), fromAddr)
			if err != nil {
				results <- txResult{slot: slot, err: fmt.Errorf("nonce: %v", err)}
				return
			}
			gasPrice, err := client.SuggestGasPrice(context.Background())
			if err != nil {
				results <- txResult{slot: slot, err: fmt.Errorf("gasPrice: %v", err)}
				return
			}

			// Build payload: insertDoc(slot, jsonData)
			docData := fmt.Sprintf(
				`{"title":"TX%d_Document","source":"worker_%d","slot":%d,"key":"%s","ts":"%s"}`,
				slot, slot, slot, keyInfo.Address, time.Now().Format(time.RFC3339Nano),
			)
			payload, err := parsedABI.Pack("insertDoc", big.NewInt(int64(slot)), docData)
			if err != nil {
				results <- txResult{slot: slot, err: fmt.Errorf("pack: %v", err)}
				return
			}

			// Estimate gas
			msg := ethereum.CallMsg{From: fromAddr, To: &contractAddr, GasPrice: gasPrice, Data: payload}
			gasLimit, err := client.EstimateGas(context.Background(), msg)
			if err != nil {
				fmt.Printf("   ⚠️ [slot=%d] EstimateGas fail: %v, dùng 3000000\n", slot, err)
				gasLimit = 3000000
			} else {
				gasLimit += 30000
			}

			// Ký TX
			tx := types.NewTransaction(nonce, contractAddr, big.NewInt(0), gasLimit, gasPrice, payload)
			signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(*chainFlag)), privateKey)
			if err != nil {
				results <- txResult{slot: slot, err: fmt.Errorf("sign: %v", err)}
				return
			}

			fmt.Printf("   ✅ [slot=%d] Sẵn sàng! from=%s nonce=%d hash=%s\n",
				slot, fromAddr.Hex(), nonce, signedTx.Hash().Hex())

			// Chờ lệnh bắn đồng loạt
			<-startGun

			// GỬI TX
			if err := client.SendTransaction(context.Background(), signedTx); err != nil {
				results <- txResult{slot: slot, txHash: signedTx.Hash(), err: fmt.Errorf("send: %v", err)}
				return
			}
			fmt.Printf("   🚀 [slot=%d] SENT! hash=%s\n", slot, signedTx.Hash().Hex())

			// Chờ receipt
			hash := signedTx.Hash()
			for {
				receipt, err := client.TransactionReceipt(context.Background(), hash)
				if err == nil {
					results <- txResult{
						slot:        slot,
						txHash:      hash,
						blockNumber: receipt.BlockNumber.Uint64(),
						status:      receipt.Status,
						gasUsed:     receipt.GasUsed,
					}
					return
				}
				time.Sleep(300 * time.Millisecond)
			}
		}(i, spamKeys[i])
	}

	// Chờ tất cả goroutine sẵn sàng (đã lấy nonce, ký tx)
	// Đơn giản: sleep 2s cho tất cả worker setup xong
	fmt.Println()
	fmt.Printf("⏳ Đang chuẩn bị %d TX song song...\n", nWorkers)
	time.Sleep(2 * time.Second)

	// BẮN!
	fmt.Printf("\n🔫 FIRE! Gửi đồng loạt %d TX...\n\n", nWorkers)
	close(startGun)

	// Thu kết quả theo goroutine kết thúc
	go func() {
		wg.Wait()
		close(results)
	}()

	// ══════════════════════════════════════════════════════════════
	// Tổng hợp kết quả
	// ══════════════════════════════════════════════════════════════
	var allResults []txResult
	for r := range results {
		allResults = append(allResults, r)
		if r.err != nil {
			fmt.Printf("   ❌ [slot=%d] LỖI: %v\n", r.slot, r.err)
		} else {
			fmt.Printf("   📦 [slot=%d] Mined! Block=#%d Status=%d GasUsed=%d\n",
				r.slot, r.blockNumber, r.status, r.gasUsed)
		}
	}

	// ══════════════════════════════════════════════════════════════
	// Phân tích: bao nhiêu TX vào cùng block?
	// ══════════════════════════════════════════════════════════════
	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println("📊 PHÂN TÍCH KẾT QUẢ:")
	fmt.Println("══════════════════════════════════════════════════════════════")

	blockGroups := map[uint64][]int{} // blockNumber → []slot
	for _, r := range allResults {
		if r.err == nil {
			blockGroups[r.blockNumber] = append(blockGroups[r.blockNumber], r.slot)
		}
	}

	maxGroupSize := 0
	var bestBlock uint64
	for bn, slots := range blockGroups {
		fmt.Printf("   Block #%d → %d TX: slots %v\n", bn, len(slots), slots)
		if len(slots) > maxGroupSize {
			maxGroupSize = len(slots)
			bestBlock = bn
		}
	}

	fmt.Println()
	if maxGroupSize >= 2 {
		fmt.Printf("✅ CÙNG BLOCK #%d có %d TX → Test case HỢP LỆ!\n", bestBlock, maxGroupSize)
		fmt.Println("   Các TX này cùng mvmId trong 1 block commit round.")
		fmt.Println()
		fmt.Println("📋 BƯỚC TIẾP THEO: Đọc lại data để verify")
		fmt.Printf("   go run ../main.go -config ../config.json -data step4_verify.json\n")
		fmt.Println()
		fmt.Println("   → Nếu data của slot > 0 bị mất → BUG: CommitFullDb() skip TX2+")
		fmt.Println("   → Nếu data tất cả slot đều có → OK: buffer tích lũy đủ")
	} else {
		fmt.Println("⚠️  Mỗi TX nằm ở block khác nhau → Test chưa hợp lệ.")
		fmt.Println("   → Thử tăng -workers hoặc chờ cluster busy hơn.")
	}
}
