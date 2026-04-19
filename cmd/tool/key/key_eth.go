package main

import (
	"crypto/ecdsa"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	if len(os.Args) > 1 {
		// Recover public key and address from existing private key
		privKeyHex := os.Args[1]
		// Remove 0x prefix if present
		privKeyHex = strings.TrimPrefix(privKeyHex, "0x")

		privateKey, err := crypto.HexToECDSA(privKeyHex)
		if err != nil {
			log.Fatalf("Invalid private key: %v", err)
		}

		publicKey := privateKey.Public()
		publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
		if !ok {
			log.Fatal("Error casting public key to ECDSA")
		}

		address := crypto.PubkeyToAddress(*publicKeyECDSA)

		fmt.Println("=== ETH Recover from Private Key ===")
		fmt.Printf("ETH_PRIVATE_KEY: 0x%s\n", privKeyHex)
		fmt.Printf("ETH_ADDRESS:     %s\n", address.Hex())
	} else {
		// Generate new ETH key pair
		privateKey, err := crypto.GenerateKey()
		if err != nil {
			log.Fatalf("Error generating new key: %v", err)
		}

		publicKey := privateKey.Public()
		publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
		if !ok {
			log.Fatal("Error casting public key to ECDSA")
		}

		address := crypto.PubkeyToAddress(*publicKeyECDSA)

		fmt.Println("=== ETH Generate New Key Pair ===")
		fmt.Printf("ETH_PRIVATE_KEY: 0x%x\n", crypto.FromECDSA(privateKey))
		fmt.Printf("ETH_ADDRESS:     %s\n", address.Hex())
	}
}
