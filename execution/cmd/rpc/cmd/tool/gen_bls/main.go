package main

import (
	"encoding/hex"
	"fmt"

	"github.com/herumi/bls-eth-go-binary/bls"
)

func main() {
	// Init BLS (BLS12-381 curve)
	err := bls.Init(bls.BLS12_381)
	if err != nil {
		panic(err)
	}

	// Ethereum mode (nếu dùng cho ETH / consensus)
	bls.SetETHmode(bls.EthModeLatest)

	// ===== tạo private key =====
	var sk bls.SecretKey
	sk.SetByCSPRNG() // random secure

	// ===== lấy public key =====
	pk := sk.GetPublicKey()

	// ===== serialize ra byte =====
	skBytes := sk.Serialize()
	pkBytes := pk.Serialize()

	fmt.Println("Private Key:", hex.EncodeToString(skBytes))
	fmt.Println("Public Key :", hex.EncodeToString(pkBytes))
}