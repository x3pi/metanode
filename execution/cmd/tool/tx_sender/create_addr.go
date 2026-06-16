//go:build ignore

package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	privateKeyHex := "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	addr := crypto.PubkeyToAddress(privateKey.PublicKey)
	fmt.Println("Private key address:", addr.Hex())
}
