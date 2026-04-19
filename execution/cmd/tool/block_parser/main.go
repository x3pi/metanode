package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
)

func main() {
	printBlock("Node 0", "/tmp/node0_block.hex")
	printBlock("Node 1", "/tmp/node1_block.hex")
}

func printBlock(name, file string) {
	bData, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatalf("ReadFile %s: %v", file, err)
	}

	bData = bytes.TrimSpace(bData)
	if string(bData) == "null" || string(bData) == "" {
		fmt.Printf("--- Block from %s --- (Empty/Null)\n", name)
		return
	}

	decodedBytes, err := hexutil.Decode(string(bData))
	if err != nil {
		log.Fatalf("Decode %s: %v", file, err)
	}

	b := &block.Block{}
	if err := b.Unmarshal(decodedBytes); err != nil {
		log.Fatalf("Unmarshal %s: %v", file, err)
	}

	h := b.Header()
	fmt.Printf("--- Block from %s ---\n", name)
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
