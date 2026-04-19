// keygen tool: derives Go BLS pubkey from given private keys
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	// Rust authority key private keys (BLS12-381 scalars)
	privKeys := []string{
		"0109751113666a88b1d335c312a6fb49a0981c2147489f5ec264e91177b42777",
		"36c2664ab4b4a972643e6808a5eb1fadc9ad9a5b4cb7983a292b21cbf53f5d0d",
		"39cebcf1b9a3e78ffc959db97c106b6da609f19b98cfb36d2c40c3d0c793ce33",
		"54299679cc61c8930e92b8c9b3c9ff0bd84425a3270419f4407a27f608f3ee97",
	}

	if len(os.Args) > 1 {
		// Accept private keys as arguments
		privKeys = os.Args[1:]
	}

	for i, privHex := range privKeys {
		privBytes, err := hex.DecodeString(privHex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error decoding key %d: %v\n", i, err)
			continue
		}
		kp := bls.NewKeyPair(privBytes)

		pubBytes := kp.PublicKey()
		pubHex := "0x" + hex.EncodeToString(pubBytes[:])
		addr := kp.Address().Hex()

		fmt.Printf("=== Node %d ===\n", i)
		fmt.Printf("  PrivateKey (hex):  %s\n", privHex)
		fmt.Printf("  PublicKey (hex):   %s\n", pubHex)
		fmt.Printf("  Address:           %s\n", addr)
		fmt.Println()
	}
}
