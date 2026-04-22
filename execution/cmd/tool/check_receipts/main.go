package main

import (
	"fmt"
	"log"

	"github.com/meta-node-blockchain/meta-node/cmd/tool/tps_blast/rpc"
)

func main() {
	rpcClient := rpc.NewRPCClient("http://127.0.0.1:8747")

	startBlock, err := rpcClient.GetBlockNumber()
	if err != nil {
		log.Fatalf("Failed to get block number: %v", err)
	}

	fmt.Printf("Current block: %d\n", startBlock)

	failedTxs := 0
	totalTxs := 0

	for bn := uint64(1); bn <= startBlock; bn++ {
		blk, err := rpcClient.GetBlockByNumber(bn)
		if err != nil {
			log.Fatalf("Error getting block %d: %v", bn, err)
		}

		if len(blk.Transactions) > 0 {
			for i, txHex := range blk.Transactions {
				totalTxs++
				receipt, err := rpcClient.GetReceipt(txHex)
				if err != nil {
					log.Printf("Error getting receipt for tx %s: %v", txHex, err)
					continue
				}
				if receipt == nil {
					continue
				}

				fromAddr := receipt["from"].(string)

				statusHex, ok := receipt["status"].(string)
				if !ok || (statusHex != "0x1" && statusHex != "1") {
					failedTxs++
					if failedTxs <= 5 {
						fmt.Printf("TX Failed: %s | Status: %v | From: %s | ReturnData: %v (Block: %d, Index: %d)\n", txHex, receipt["status"], fromAddr, receipt["returnData"], bn, i)
					}
				}
			}
		}
	}

	fmt.Printf("\nTotal TXs: %d\n", totalTxs)
	fmt.Printf("Failed TXs: %d\n", failedTxs)
}
