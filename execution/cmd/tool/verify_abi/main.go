package main

import (
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
)

func main() {
	fmt.Println("Verifying GetValidatorHandler...")
	h, err := tx_processor.GetValidatorHandler()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("Handler initialized successfully: %v\n", h)
}
