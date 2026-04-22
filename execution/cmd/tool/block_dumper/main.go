package main

import (
	"fmt"
	"log"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

func main() {
	dumpBlock("Node 0", "/home/abc/chain-n/metanode/sample/node0/data/data/master_n_0_0/blocks")
	dumpBlock("Node 1", "/home/abc/chain-n/metanode/sample/node1/data/data/master_n_1_0/blocks")
}

func dumpBlock(nodeName, dbPath string) {
	fmt.Printf("--- Dumping blocks 200-201 from %s (%s) ---\n", nodeName, dbPath)
	
	db, err := storage.NewShardelDB(dbPath, 16, 1, storage.TypePebbleDB, "")
	if err != nil {
		log.Fatalf("Failed to create ShardedDB for %s: %v", dbPath, err)
	}
	if err := db.Open(); err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	it := db.NewIterator(nil, nil)
	defer it.Release()

	for it.Next() {
		val := it.Value()
		b := &block.Block{}
		if err := b.Unmarshal(val); err != nil {
			continue // Not a block or parse error
		}
		
		h := b.Header()
		if h.BlockNumber() == 200 || h.BlockNumber() == 201 {
			fmt.Printf("Block Number: %d\n", h.BlockNumber())
			fmt.Printf("Hash: %s\n", h.Hash().Hex())
			fmt.Printf("AccountStatesRoot: %s\n", h.AccountStatesRoot().Hex())
			fmt.Printf("StakeStatesRoot: %s\n", h.StakeStatesRoot().Hex())
			fmt.Printf("ReceiptRoot: %s\n", h.ReceiptRoot().Hex())
			fmt.Printf("TransactionsRoot: %s\n", h.TransactionsRoot().Hex())
			fmt.Printf("LeaderAddress: %s\n", h.LeaderAddress().Hex())
			fmt.Printf("Epoch: %d\n", h.Epoch())
			fmt.Printf("GlobalExecIndex: %d\n", h.GlobalExecIndex())
			fmt.Printf("Timestamp: %d\n", h.TimeStamp())
			fmt.Println()
		}
	}
}
