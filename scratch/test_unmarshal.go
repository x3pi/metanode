package main

import (
	"encoding/json"
	"fmt"
	"github.com/ethereum/go-ethereum/core/types"
)

func main() {
	var r types.Receipt
	bloom := "0x"
	for i := 0; i < 512; i++ {
		bloom += "0"
	}
	// Try unmarshalling with a log containing txHash without 0x prefix
	data := []byte(fmt.Sprintf(`{
		"status": "0x1",
		"cumulativeGasUsed": "0x5",
		"logsBloom": "%s",
		"logs": [
			{
				"address": "0x0000000000000000000000000000000000000000",
				"topics": [],
				"data": "0x00",
				"transactionHash": "4c01ca6dfd6852b2d4f1df32183ec5c42da9cd33972845749065a4951052fe6e",
				"blockHash": "0x0000000000000000000000000000000000000000000000000000000000000000"
			}
		]
	}`, bloom))
	err := json.Unmarshal(data, &r)
	fmt.Printf("Error: %v\n", err)
}
