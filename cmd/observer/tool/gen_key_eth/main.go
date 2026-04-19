package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

type KeyInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Address    string `json:"address"`
}

func main() {
	count := flag.Int("count", 1, "Number of key pairs to generate")
	output := flag.String("output", "", "Output JSON file path (default: print to console)")
	flag.Parse()

	if *count <= 0 {
		fmt.Println("❌ Count must be greater than 0")
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  🔑 KEY GENERATOR — Private Key, Public Key, Address")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  Generating %d key pair(s)...\n\n", *count)

	start := time.Now()
	keys := make([]KeyInfo, 0, *count)

	for i := 0; i < *count; i++ {
		privateKey, err := crypto.GenerateKey()
		if err != nil {
			fmt.Printf("  ❌ Error generating key #%d: %v\n", i+1, err)
			continue
		}

		privKeyBytes := crypto.FromECDSA(privateKey)
		pubKeyBytes := crypto.CompressPubkey(&privateKey.PublicKey)
		address := crypto.PubkeyToAddress(privateKey.PublicKey)

		info := KeyInfo{
			Index:      i,
			PrivateKey: hex.EncodeToString(privKeyBytes),
			PublicKey:  hex.EncodeToString(pubKeyBytes),
			Address:    address.Hex(),
		}
		keys = append(keys, info)

		// Print to console
		fmt.Printf("  ─── Key #%d ───────────────────────────────────\n", i+1)
		fmt.Printf("  🔐 Private Key: %s\n", info.PrivateKey)
		fmt.Printf("  🔓 Public Key:  %s\n", info.PublicKey)
		fmt.Printf("  📍 Address:     %s\n\n", info.Address)
	}

	elapsed := time.Since(start)

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  ✅ Generated %d key pair(s) in %s\n", len(keys), elapsed.Round(time.Millisecond))
	fmt.Println("═══════════════════════════════════════════════════")

	// Save to file if output path is specified
	if *output != "" {
		data, err := json.MarshalIndent(keys, "", "  ")
		if err != nil {
			fmt.Printf("  ❌ Error marshaling JSON: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*output, data, 0644); err != nil {
			fmt.Printf("  ❌ Error writing file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  💾 Saved to %s\n", *output)
	}
}
