package main

import (
	"crypto/ecdsa"
	"fmt"
	"log"

	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	key := "6c8489f6f86fea58b26e34c8c37e13e5993651f09f5f96739d9febf65aded718"
	privateKey, err := crypto.HexToECDSA(key)
	if err != nil {
		log.Fatal(err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, _ := publicKey.(*ecdsa.PublicKey)
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	fmt.Printf("Private Key: %s\n", key)
	fmt.Printf("Address: %s\n", fromAddress.Hex())
}
