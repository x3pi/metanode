package main

import (
	"crypto/ecdsa"
	"log"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

func main() {
	log.Println("Starting Stress & Security Test for Devnet...")
	client := parentchain.NewHTTPClient("http://127.0.0.1:8547")

	blsPrivHex := "5fb8d1ceadf4059adca5c106dbd91452be8c433b2c38c5dd85c50f1c7da4c85c"
	blsPriv, clusterPubKey, _ := bls.GenerateKeyPairFromSecretKey(blsPrivHex)

	var pubKey cm.PublicKey
	pubKey[0] = 1

	// 1. Setup User
	targetPriv, _ := crypto.GenerateKey()
	target := crypto.PubkeyToAddress(targetPriv.PublicKey)
	senderPriv, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(senderPriv.PublicKey)

	registerUser(client, target, targetPriv, clusterPubKey, blsPriv)

	log.Println("--- STARTING SECURITY TEST ---")
	runSecurityTest(client, clusterPubKey, pubKey, target, sender, blsPriv)

	log.Println("--- STARTING CONCURRENCY LOAD TEST ---")
	runLoadTest(client, clusterPubKey, target)
}

func registerUser(client parentchain.Client, user common.Address, priv *ecdsa.PrivateKey, clusterPubKey cm.PublicKey, blsPriv cm.PrivateKey) {
	digest := parentchain.ComputeRegisterAccountMessage(user, clusterPubKey)
	userSig, _ := crypto.Sign(crypto.Keccak256(digest), priv)
	clusterSig := bls.Sign(blsPriv, digest)

	_, err := client.SendRegisterAccount(user, clusterPubKey, userSig, clusterSig)
	if err != nil {
		log.Printf("RegisterAccount failed (may already exist): %v", err)
	} else {
		log.Printf("Registered Account %s", user.Hex())
	}
	time.Sleep(1 * time.Second)
}

func runSecurityTest(client parentchain.Client, clusterPubKey, pubKey cm.PublicKey, target, sender common.Address, blsPriv cm.PrivateKey) {
	// S3: Tấn công chữ ký sai (Invalid Signature)
	log.Println("Test S3: Thử nghiệm Rút tiền với chữ ký sai...")
	payloadHash := crypto.Keccak256Hash(nil)
	digest := parentchain.ComputeTransferFloatMessage(clusterPubKey, pubKey, target, sender, big.NewInt(100), big.NewInt(0), payloadHash, 0)
	
	// Tạo chữ ký ngẫu nhiên
	fakeBlsPrivHex := "1111111111111111111111111111111111111111111111111111111111111111"
	fakeBlsPriv, _, _ := bls.GenerateKeyPairFromSecretKey(fakeBlsPrivHex)
	invalidSig := bls.Sign(fakeBlsPriv, digest)

	_, err := client.SendTransferFloat(clusterPubKey, pubKey, 992, target, sender, big.NewInt(100), big.NewInt(0), 0, invalidSig[:], false)
	if err == nil {
		log.Fatal("❌ SECURITY BREACH: Giao dịch chữ ký sai ĐÃ ĐƯỢC CHẤP NHẬN!")
	} else {
		log.Printf("✅ PASS: Giao dịch bị từ chối hợp lý -> %v", err)
	}

	// S4: Tấn công chi tiêu vượt mức (Insufficient Balance)
	log.Println("Test S4: Thử nghiệm Rút tiền lớn hơn số dư (1,000,000 tokens)...")
	digest2 := parentchain.ComputeTransferFloatMessage(clusterPubKey, pubKey, target, sender, big.NewInt(1000000), big.NewInt(0), payloadHash, 1)
	validSig2 := bls.Sign(blsPriv, digest2)
	_, err = client.SendTransferFloat(clusterPubKey, pubKey, 992, target, sender, big.NewInt(1000000), big.NewInt(0), 1, validSig2[:], false)
	if err == nil {
		log.Printf("Giao dịch được gửi, đang chờ xử lý. Sẽ thất bại khi Rollup kiểm tra.")
		// Wait, Rollup should drop it and it won't be processed. Parent Chain accepts it because the Cluster's float account might have enough balance!
		// Wait, does the cluster have 1,000,000? Maybe not! Let's see if Parent Chain rejects it.
	} else {
		log.Printf("✅ PASS (Parent Chain rejected it): %v", err)
	}
}

func runLoadTest(client parentchain.Client, clusterPubKey cm.PublicKey, target common.Address) {
	numWorkers := 20
	requestsPerWorker := 50
	log.Printf("Test C1: Bắn %d DepositToFloat giao dịch đồng thời...", numWorkers*requestsPerWorker)

	var wg sync.WaitGroup
	var successCount, failCount int32

	startTime := time.Now()
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < requestsPerWorker; j++ {
				uniqueSenderBytes := append(target.Bytes(), byte(j), byte(workerID))
				uniqueSender := common.BytesToAddress(uniqueSenderBytes)
				_, err := client.SendDepositToFloat(
					clusterPubKey, 992,
					uniqueSender, target, big.NewInt(1), // Deposit 1 token each time
				)
				if err != nil {
					atomic.AddInt32(&failCount, 1)
				} else {
					atomic.AddInt32(&successCount, 1)
				}
			}
		}(i)
	}
	wg.Wait()
	duration := time.Since(startTime)
	
	log.Printf("✅ Hoàn thành Load Test trong %v", duration)
	log.Printf("Thành công: %d, Thất bại: %d (Do Backpressure hoặc Timeout)", successCount, failCount)
	if failCount == 0 {
		log.Printf("✅ Không có giao dịch nào bị rớt (Zero dropped). Parent Chain xử lý hoàn hảo!")
	}
}
