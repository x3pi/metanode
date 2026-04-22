package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

func main() {
	// Option 1: Hardcoded fallback here
	hardcodedHex := "0ac401dcdbf380000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000002531302e3737383638393735313635303537352d3130362e3735313631333032303839363933000000000000000000000000000000000000000000000000000000"
	hexStr := hardcodedHex

	// Option 2: Provide via CLI arguments
	if len(os.Args) >= 2 {
		hexStr = os.Args[1]
	}

	// Remove 0x prefix if present
	if len(hexStr) >= 2 && hexStr[:2] == "0x" {
		hexStr = hexStr[2:]
	}

	protoData, err := hex.DecodeString(hexStr)
	if err != nil {
		fmt.Printf("Error decoding hex: %v\n", err)
		return
	}

	// Create an empty CallData
	cd := &transaction.CallData{}

	err = cd.Unmarshal(protoData)
	if err != nil {
		fmt.Printf("Error unmarshaling CallData proto: %v\n", err)
		return
	}

	input := cd.Input()
	fmt.Println("==================================================")
	fmt.Printf("Raw Hex Input: %s\n", hexStr)
	fmt.Printf("Parsed Input Data (Hex): %x\n", input)
	fmt.Println("==================================================")
}
