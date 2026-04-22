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

// KeyInfo represents a generated key pair
type KeyInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
}

// GenesisAlloc represents an account in genesis.json alloc
type GenesisAlloc struct {
	Address      string `json:"address"`
	Balance      string `json:"balance"`
	PendingBal   string `json:"pending_balance"`
	LastHash     string `json:"last_hash"`
	DeviceKey    string `json:"device_key"`
	PublicKeyBls string `json:"publicKeyBls"`
}

func main() {
	count := flag.Int("count", 50000, "Number of key pairs to generate")
	keysOutput := flag.String("keys-output", "generated_keys.json", "Output file for generated keys")
	genesisIn := flag.String("genesis-in", "../../../simple_chain/genesis-main.json", "Path to source genesis.json to read from")
	genesisOut := flag.String("genesis-out", "../../../simple_chain/genesis.json", "Path to generated genesis.json to write to")

	balance := flag.String("balance", "100000000000000000000", "Initial balance for each account (wei, default 100 ETH)")
	blsKey := flag.String("bls", "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184", "Default BLS public key")
	flag.Parse()

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  🔑 SPAM KEY GENERATOR — Gen + Inject Genesis")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  Count: %d\n", *count)
	fmt.Printf("  Keys output: %s\n", *keysOutput)
	fmt.Printf("  Genesis In: %s\n", *genesisIn)
	fmt.Printf("  Genesis Out: %s\n", *genesisOut)
	fmt.Printf("  Balance: %s wei\n\n", *balance)

	start := time.Now()
	keys := make([]KeyInfo, 0, *count)

	for i := 0; i < *count; i++ {
		privateKey, err := crypto.GenerateKey()
		if err != nil {
			fmt.Printf("  ❌ Error generating key #%d: %v\n", i+1, err)
			continue
		}

		privKeyBytes := crypto.FromECDSA(privateKey)
		address := crypto.PubkeyToAddress(privateKey.PublicKey)

		info := KeyInfo{
			Index:      i,
			PrivateKey: hex.EncodeToString(privKeyBytes),
			Address:    address.Hex(),
		}
		keys = append(keys, info)

		if (i+1)%100 == 0 {
			fmt.Printf("  ✅ Generated %d/%d keys...\n", i+1, *count)
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("\n  ✅ Generated %d keys in %s\n", len(keys), elapsed.Round(time.Millisecond))

	// Save keys to file
	keysData, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		fmt.Printf("  ❌ Error marshaling keys: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*keysOutput, keysData, 0644); err != nil {
		fmt.Printf("  ❌ Error writing keys file: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  💾 Keys saved to %s\n", *keysOutput)

	// Inject into genesis.json if path provided
	if *genesisIn != "" && *genesisOut != "" {
		fmt.Printf("\n  📝 Reading base genesis from %s...\n", *genesisIn)
		fmt.Printf("  📝 Injecting %d accounts into %s...\n", len(keys), *genesisOut)

		genesisRaw, err := os.ReadFile(*genesisIn)
		if err != nil {
			fmt.Printf("  ❌ Error reading genesis: %v\n", err)
			os.Exit(1)
		}

		var genesis map[string]interface{}
		if err := json.Unmarshal(genesisRaw, &genesis); err != nil {
			fmt.Printf("  ❌ Error parsing genesis: %v\n", err)
			os.Exit(1)
		}

		// Get existing alloc
		allocRaw, ok := genesis["alloc"]
		if !ok {
			fmt.Println("  ❌ genesis.json has no 'alloc' field")
			os.Exit(1)
		}

		// Convert to []interface{}
		existingAlloc, ok := allocRaw.([]interface{})
		if !ok {
			fmt.Println("  ❌ 'alloc' field is not an array")
			os.Exit(1)
		}

		existingCount := len(existingAlloc)

		// Build set of existing addresses (lowercase)
		existingAddrs := make(map[string]bool)
		for _, item := range existingAlloc {
			if m, ok := item.(map[string]interface{}); ok {
				if addr, ok := m["address"].(string); ok {
					existingAddrs[addr] = true
				}
			}
		}

		// Add new accounts
		added := 0
		for _, key := range keys {
			if existingAddrs[key.Address] {
				continue // skip duplicate
			}
			newAlloc := GenesisAlloc{
				Address:      key.Address,
				Balance:      *balance,
				PendingBal:   "0",
				LastHash:     "0x0000000000000000000000000000000000000000000000000000000000000000",
				DeviceKey:    "0x0000000000000000000000000000000000000000000000000000000000000000",
				PublicKeyBls: *blsKey,
			}
			existingAlloc = append(existingAlloc, newAlloc)
			added++
		}

		genesis["alloc"] = existingAlloc

		// Write back
		genesisOutData, err := json.MarshalIndent(genesis, "", "  ")
		if err != nil {
			fmt.Printf("  ❌ Error marshaling genesis: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*genesisOut, genesisOutData, 0644); err != nil {
			fmt.Printf("  ❌ Error writing genesis: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("  ✅ Injected %d new accounts (was %d, now %d)\n", added, existingCount, len(existingAlloc))
		fmt.Printf("  💾 Genesis saved to %s\n", *genesisOut)
	}

	fmt.Println("\n═══════════════════════════════════════════════════")
	fmt.Println("  ✅ Done!")
	fmt.Println("═══════════════════════════════════════════════════")
}
