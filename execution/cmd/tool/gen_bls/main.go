package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	if len(os.Args) > 1 {
		// Derive public key from existing private key
		privKeyHex := os.Args[1]
		privKey, pubKey, addr := bls.GenerateKeyPairFromSecretKey(privKeyHex)
		fmt.Println("=== BLS Derive from Private Key ===")
		fmt.Printf("BLS_PRIVATE_KEY: 0x%s\n", hex.EncodeToString(privKey.Bytes()))
		fmt.Printf("BLS_PUBLIC_KEY:  0x%s\n", hex.EncodeToString(pubKey.Bytes()))
		fmt.Printf("BLS_ADDRESS:     %s\n", addr.Hex())
	} else {
		// Generate new BLS key pair
		kp := bls.GenerateKeyPair()
		fmt.Println("=== BLS Generate New Key Pair ===")
		fmt.Printf("BLS_PRIVATE_KEY: 0x%s\n", hex.EncodeToString(kp.BytesPrivateKey()))
		fmt.Printf("BLS_PUBLIC_KEY:  0x%s\n", hex.EncodeToString(kp.BytesPublicKey()))
		fmt.Printf("BLS_ADDRESS:     %s\n", kp.Address().Hex())
	}
}
