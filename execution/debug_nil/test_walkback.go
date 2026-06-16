//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

func main() {
	// 1. Initialize NOMT database if backend is "nomt"
	nomtPath := "../cmd/simple_chain/sample/node0/data/data/consensus/nomt_db"
	trie.SetStateBackend(trie.BackendNOMT)
	if err := trie.InitNomtDB(nomtPath, 4, 128, 128); err != nil {
		fmt.Printf("Failed to initialize NOMT database: %v\n", err)
		os.Exit(1)
	}

	// 2. Open databases
	rootPath := "../cmd/simple_chain/sample/node0/data/data"

	blockDBStorage, err := storage.NewShardelDB(
		rootPath+"/history/blocks/",
		1, 1,
		storage.DBType(2),
		"history/blocks",
	)
	if err != nil {
		fmt.Printf("Failed to create blocks DB: %v\n", err)
		os.Exit(1)
	}
	if err := blockDBStorage.Open(); err != nil {
		fmt.Printf("Failed to open blocks DB: %v\n", err)
		os.Exit(1)
	}
	defer blockDBStorage.Close()

	txDBStorage, err := storage.NewShardelDB(
		rootPath+"/history/transaction_state/",
		1, 1,
		storage.DBType(2),
		"history/transaction_state",
	)
	if err != nil {
		fmt.Printf("Failed to create tx DB: %v\n", err)
		os.Exit(1)
	}
	if err := txDBStorage.Open(); err != nil {
		fmt.Printf("Failed to open tx DB: %v\n", err)
		os.Exit(1)
	}
	defer txDBStorage.Close()

	blockDatabase := block.NewBlockDatabase(blockDBStorage)

	lastBlock, err := blockDatabase.GetLastBlock()
	if err != nil {
		fmt.Printf("Failed to get last block: %v\n", err)
		os.Exit(1)
	}
	if lastBlock == nil {
		fmt.Printf("Last block is nil\n")
		os.Exit(1)
	}

	fmt.Printf("Last block number: %d, hash: %s\n", lastBlock.Header().BlockNumber(), lastBlock.Header().Hash().Hex())

	targetEthHash := common.HexToHash("0x62b34f1e2752db65ea03555d8cf14a0fff249d04bcc4175d250e0dce58f25d9f")
	targetBlsHash := common.HexToHash("0xe8accb5593fc914cc6dffa2f0753d32786ce82ed6fd02ce41201eece768658a8")

	blk := lastBlock
	var depth int
	found := false

	for blk != nil && depth < 2000 {
		bNum := blk.Header().BlockNumber()
		txHashes := blk.Transactions()

		if bNum%100 == 0 {
			fmt.Printf("Walking back... Block #%d\n", bNum)
		}

		if len(txHashes) > 0 {
			txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blk.Header().TransactionsRoot(), txDBStorage)
			if err != nil {
				fmt.Printf("Block #%d: NewTransactionStateDBFromRoot failed: %v\n", bNum, err)
			} else if txDB != nil {
				for _, tHash := range txHashes {
					tx, err := txDB.GetTransaction(tHash)
					if err == nil && tx != nil {
						if tx.Hash() == targetBlsHash || tx.EthHash() == targetEthHash {
							fmt.Printf("🎉 🎉 🎉 FOUND TARGET TRANSACTION IN BLOCK #%d!\n", bNum)
							fmt.Printf("  BLS Hash: %s\n", tx.Hash().Hex())
							fmt.Printf("  Eth Hash: %s\n", tx.EthHash().Hex())
							fmt.Printf("  Block Hash: %s\n", blk.Header().Hash().Hex())
							found = true
							break
						}
					}
				}
			}
		}

		if found {
			break
		}

		if bNum == 0 {
			break
		}

		parentHash := blk.Header().LastBlockHash()
		if parentHash == (common.Hash{}) {
			break
		}
		parentBlk, pErr := blockDatabase.GetBlockByHash(parentHash)
		if pErr != nil || parentBlk == nil {
			fmt.Printf("Failed to get parent block of #%d (parentHash: %s): %v\n", bNum, parentHash.Hex(), pErr)
			break
		}
		blk = parentBlk
		depth++
	}

	if found {
		fmt.Println("Result: Transaction FOUND!")
	} else {
		fmt.Println("Result: Transaction NOT found in walkback!")
	}
}
