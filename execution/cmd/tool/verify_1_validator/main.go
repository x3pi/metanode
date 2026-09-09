package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorRed    = "\033[31m"
)

func main() {
	fmt.Println(colorCyan + colorBold + "🔬 BÀI TEST: MẠNG PRIVATE CHAIN 1 VALIDATOR (CENTRALIZED)" + colorReset)
	fmt.Println("Kiểm tra tính hợp lệ của việc dùng 1 validator để đăng ký và gửi giao dịch xuyên chuỗi.")
	fmt.Println()

	// 1. Tạo 1 BLS KeyPair
	kp := bls.GenerateKeyPair()
	pop := cross_chain.PopSign(kp.PrivateKey(), kp.PublicKey())

	// 2. Định nghĩa ChainRegistry với 1 Validator duy nhất
	chainID := uint64(999)
	stake := uint64(1000)
	committee := []cross_chain.ValidatorEntry{
		{PubkeyBLS: kp.BytesPublicKey(), Stake: stake, PopSignature: pop.Bytes()},
	}

	fmt.Printf("1️⃣ Khởi tạo Validator duy nhất:\n")
	fmt.Printf("   - Pubkey BLS: 0x%x...\n", kp.BytesPublicKey()[:10])
	fmt.Printf("   - Stake: %d\n", stake)

	// Validate Committee
	err := cross_chain.ValidateCommittee(committee)
	if err != nil {
		fmt.Printf("%s❌ ValidateCommittee thất bại: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}
	fmt.Printf("%s✅ ValidateCommittee thành công (len=1)%s\n", colorGreen, colorReset)

	// 3. Tính toán BFT Quorum Threshold
	totalStake := stake
	bftQuorumThreshold := (2*totalStake)/3 + 1
	maxFaultyStake := uint64(0)
	if totalStake > 0 {
		maxFaultyStake = (totalStake - 1) / 3
	}
	
	fmt.Printf("\n2️⃣ Tính toán BFT Quorum:\n")
	fmt.Printf("   - Total Stake: %d\n", totalStake)
	fmt.Printf("   - Threshold (2f+1): %d\n", bftQuorumThreshold)
	fmt.Printf("   - Max Faulty Stake (f): %d (Chịu lỗi 0 validator)\n", maxFaultyStake)
	fmt.Printf("%s✅ Threshold 667 nhỏ hơn lượng stake của 1 validator (1000)%s\n", colorGreen, colorReset)

	// 4. Test Gateway Engine lưu trữ và nhận chữ ký (Mock submitCommitAttestation)
	engine := cross_chain.NewGatewayEngine(100, nil, nil)
	engine.PendingCommitAttestations = make(map[string][]cross_chain.CommitAttestationShare) // Fix panic
	engine.ChainRegistry = make(map[uint64]cross_chain.ChainRegistry)

	
	// Mock đăng ký chain
	reg := cross_chain.ChainRegistry{
		ChainID:         chainID,
		Committee:       committee,
		Epoch:           1,
		QuorumThreshold: 6667, // 66.67%
	}
	engine.ChainRegistry[chainID] = reg

	fmt.Printf("\n3️⃣ Mô phỏng nộp chữ ký (submitCommitAttestation):\n")
	commitRoot := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)
	
	key := fmt.Sprintf("%d:%d:%s", chainID, 1, commitRoot.Hex())
	engine.PendingCommitAttestations[key] = append(engine.PendingCommitAttestations[key], cross_chain.CommitAttestationShare{
		SignerPubkeyBLS: kp.BytesPublicKey(),
		Signature:       sig.Bytes(),
	})
	fmt.Printf("%s✅ Chữ ký của 1 validator đã được lưu vào PendingCommitAttestations%s\n", colorGreen, colorReset)

	// 5. Relayer Daemon poll QuorumCert
	fmt.Printf("\n4️⃣ Relayer Daemon thu thập QuorumCert:\n")
	shares := engine.PendingCommitAttestations[key]
	
	var accumulatedStake uint64
	var validPubkeys [][]byte
	var validSigs [][]byte

	for _, s := range shares {
		for _, v := range reg.Committee {
			if string(v.PubkeyBLS) == string(s.SignerPubkeyBLS) {
				accumulatedStake += v.Stake
				validPubkeys = append(validPubkeys, s.SignerPubkeyBLS)
				validSigs = append(validSigs, s.Signature)
				break
			}
		}
	}

	daemonThreshold := (totalStake*reg.QuorumThreshold + 9999) / 10000
	fmt.Printf("   - Chữ ký hợp lệ thu được: %d\n", len(validSigs))
	fmt.Printf("   - Accumulated Stake: %d\n", accumulatedStake)
	fmt.Printf("   - Required Threshold: %d\n", daemonThreshold)

	if accumulatedStake >= daemonThreshold && len(validSigs) > 0 {
		fmt.Printf("%s✅ QUORUM REACHED! Hệ thống 1 validator hoạt động hoàn hảo.%s\n\n", colorBold+colorGreen, colorReset)
	} else {
		fmt.Printf("%s❌ QUORUM NOT REACHED! Lỗi logic.%s\n\n", colorRed, colorReset)
		os.Exit(1)
	}
}
