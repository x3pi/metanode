package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	// Bắt buộc phải khởi tạo thư viện CGO của BLS trước khi dùng
	bls.Init()

	keyFlag := flag.String("key", "", "BLS private key (hex) để recover public key và address")
	flag.Parse()

	if *keyFlag != "" {
		// ── Chế độ RECOVER ──────────────────────────────────────────────
		privHex := strings.TrimPrefix(*keyFlag, "0x")

		// Kiểm tra hex hợp lệ
		if _, err := hex.DecodeString(privHex); err != nil {
			fmt.Fprintf(os.Stderr, "Lỗi: private key không hợp lệ (phải là hex): %v\n", err)
			os.Exit(1)
		}

		priv, pub, addr := bls.GenerateKeyPairFromSecretKey(privHex)

		fmt.Println("==================================================")
		fmt.Println("           BLS KEY RECOVER FROM PRIVATE           ")
		fmt.Println("==================================================")
		fmt.Printf("Private Key : 0x%v\n", hex.EncodeToString(priv.Bytes()))
		fmt.Printf("Public Key  : 0x%v\n", hex.EncodeToString(pub.Bytes()))
		fmt.Printf("Address     : %v\n", addr.Hex())
		fmt.Println("==================================================")
	} else {
		// ── Chế độ GENERATE (mặc định) ──────────────────────────────────
		kp := bls.GenerateKeyPair()

		fmt.Println("==================================================")
		fmt.Println("             BLS KEY PAIR GENERATOR               ")
		fmt.Println("==================================================")
		fmt.Printf("Private Key : 0x%v\n", hex.EncodeToString(kp.BytesPrivateKey()))
		fmt.Printf("Public Key  : 0x%v\n", hex.EncodeToString(kp.BytesPublicKey()))
		fmt.Printf("Address     : %v\n", kp.Address().Hex())
		fmt.Println("==================================================")
	}
}

