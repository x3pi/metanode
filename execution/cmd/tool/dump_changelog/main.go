package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"

	"github.com/cockroachdb/pebble"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
)

func main() {
	db0, err := pebble.Open("/tmp/node0_changelog", &pebble.Options{})
	if err != nil {
		log.Fatalf("Failed to open node0 changelog: %v", err)
	}
	defer db0.Close()

	db1, err := pebble.Open("/tmp/node1_changelog", &pebble.Options{})
	if err != nil {
		log.Fatalf("Failed to open node1 changelog: %v", err)
	}
	defer db1.Close()

	fmt.Println("=== Comparing changelogs for block 526 ===")

	// We want to scan the database and extract all keys modified at Block 526.
	// Key format: namespace:address:blockNumber
	// namespace is "account_state", blockNumber is uint64(526).

	targetBlock := uint64(526)
	targetBlockBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(targetBlockBytes, targetBlock)

	// Since Pebble keys are sorted lexicographically:
	// "account_state:<address>:<blockNumber>"
	// We can iterate over the entire DB and filter by block number at the end of the key.

	iter0, err := db0.NewIter(nil)
	if err != nil {
		log.Fatalf("Failed to create iter0: %v", err)
	}
	defer iter0.Close()

	changes0 := make(map[string][]byte)
	for iter0.First(); iter0.Valid(); iter0.Next() {
		k := iter0.Key()
		if len(k) < 8 {
			continue
		}
		// Check if it ends with targetBlockBytes
		blockPart := k[len(k)-8:]
		if bytes.Equal(blockPart, targetBlockBytes) {
			// Extract address. Key is "account_state:address:block"
			// Prefix is "account_state:" (14 bytes)
			if bytes.HasPrefix(k, []byte("account_state:")) && len(k) >= 14+8 {
				addr := k[14 : len(k)-9] // 9 is ':' + 8 bytes block
				changes0[string(addr)] = append([]byte(nil), iter0.Value()...)
			}
		}
	}

	iter1, err := db1.NewIter(nil)
	if err != nil {
		log.Fatalf("Failed to create iter1: %v", err)
	}
	defer iter1.Close()

	changes1 := make(map[string][]byte)
	for iter1.First(); iter1.Valid(); iter1.Next() {
		k := iter1.Key()
		if len(k) < 8 {
			continue
		}
		blockPart := k[len(k)-8:]
		if bytes.Equal(blockPart, targetBlockBytes) {
			if bytes.HasPrefix(k, []byte("account_state:")) && len(k) >= 14+8 {
				addr := k[14 : len(k)-9]
				changes1[string(addr)] = append([]byte(nil), iter1.Value()...)
			}
		}
	}

	fmt.Printf("Node 0 changes count: %d\n", len(changes0))
	fmt.Printf("Node 1 changes count: %d\n", len(changes1))

	// Find mismatches
	allAddresses := make(map[string]bool)
	for addr := range changes0 {
		allAddresses[addr] = true
	}
	for addr := range changes1 {
		allAddresses[addr] = true
	}

	mismatches := 0
	for addr := range allAddresses {
		v0, ok0 := changes0[addr]
		v1, ok1 := changes1[addr]

		addrHex := fmt.Sprintf("%x", []byte(addr))

		if !ok0 {
			mismatches++
			fmt.Printf("❌ Address %s modified on Node 1 but NOT on Node 0!\n", addrHex)
			as1 := &state.AccountState{}
			if err := as1.Unmarshal(v1); err == nil {
				fmt.Printf("   Node 1 state:\n%s\n", as1.String())
			} else {
				fmt.Printf("   Node 1 raw hex: %x\n", v1)
			}
		} else if !ok1 {
			mismatches++
			fmt.Printf("❌ Address %s modified on Node 0 but NOT on Node 1!\n", addrHex)
			as0 := &state.AccountState{}
			if err := as0.Unmarshal(v0); err == nil {
				fmt.Printf("   Node 0 state:\n%s\n", as0.String())
			} else {
				fmt.Printf("   Node 0 raw hex: %x\n", v0)
			}
		} else if !bytes.Equal(v0, v1) {
			mismatches++
			fmt.Printf("❌ Address %s value mismatch:\n", addrHex)
			as0 := &state.AccountState{}
			as1 := &state.AccountState{}
			_ = as0.Unmarshal(v0)
			_ = as1.Unmarshal(v1)
			fmt.Printf("   Node 0 state:\n%s\n", as0.String())
			fmt.Printf("   Node 1 state:\n%s\n", as1.String())
		}
	}

	if mismatches == 0 {
		fmt.Println("✅ Perfect match! All account changelog entries are identical at Block 526.")
	} else {
		fmt.Printf("❌ Found %d mismatches in changelog at Block 526.\n", mismatches)
	}
}
