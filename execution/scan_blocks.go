//go:build ignore

package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

var (
	lastBlockHashKey common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastBlockHashKey")))
)

func main() {
	dbPath := "/tmp/node1_db_copy/blocks"
	db, err := storage.NewShardelDB(dbPath, 1, 1, 2, "")
	if err != nil {
		fmt.Printf("Error creating blocks DB: %v\n", err)
		return
	}
	if err := db.Open(); err != nil {
		fmt.Printf("Error opening blocks DB: %v\n", err)
		return
	}
	defer db.Close()

	backupDbPath := "/tmp/node1_db_copy/back_up"
	backupDb, err := storage.NewShardelDB(backupDbPath, 1, 1, 2, "")
	if err != nil {
		fmt.Printf("Error creating backup DB: %v\n", err)
		return
	}
	if err := backupDb.Open(); err != nil {
		fmt.Printf("Error opening backup DB: %v\n", err)
		return
	}
	defer backupDb.Close()

	// Get last block info
	val, err := db.Get(lastBlockHashKey.Bytes())
	if err == nil && len(val) > 0 {
		blk := &block.Block{}
		if err := blk.Unmarshal(val); err == nil {
			fmt.Printf("Authoritative Node 1 Last Block: %d, Hash: %s, StateRoot: %s\n\n",
				blk.Header().BlockNumber(), blk.Header().Hash().Hex(), blk.Header().AccountStatesRoot().Hex())
		}
	}

	fmt.Printf("%-8s | %-16s | %-16s | %-12s | %-12s | %-12s\n", "Block #", "Hash", "StateRoot", "AccBatch", "SCBatch", "TrieDB")
	fmt.Println("---------------------------------------------------------------------------------------------")

	// Scan blocks from 1 onwards
	for i := uint64(1); i <= 400; i++ {
		// Try to read block mapping to get hash
		mappingKey := []byte(fmt.Sprintf("block_num_to_hash-%d", i))
		hashBytes, err := db.Get(mappingKey)
		if err != nil || len(hashBytes) == 0 {
			// fallback mapping
			mappingKey = []byte(fmt.Sprintf("mapping_%d", i))
			hashBytes, err = db.Get(mappingKey)
		}
		if err != nil || len(hashBytes) == 0 {
			continue
		}
		blkHash := common.BytesToHash(hashBytes)

		// Get block header
		blkVal, err := db.Get(blkHash.Bytes())
		if err != nil || len(blkVal) == 0 {
			fmt.Printf("Block %d: hash mapped to %s but raw block is missing\n", i, blkHash.Hex())
			continue
		}

		blk := &block.Block{}
		if err := blk.Unmarshal(blkVal); err != nil {
			fmt.Printf("Block %d: failed to unmarshal: %v\n", i, err)
			continue
		}

		// Read backup data
		backupKey := []byte(fmt.Sprintf("block_data_topic-%d", i))
		backupBytes, err := backupDb.Get(backupKey)
		if err != nil || len(backupBytes) == 0 {
			backupKey = []byte(fmt.Sprintf("backup_%d", i))
			backupBytes, err = backupDb.Get(backupKey)
		}

		accSize := 0
		scSize := 0
		trieDbSize := 0

		if len(backupBytes) > 0 {
			deser, err := storage.DeserializeBackupDb(backupBytes)
			if err == nil {
				accSize = len(deser.AccountBatch)
				scSize = len(deser.SmartContractStorageBatch)
				trieDbSize = len(deser.TrieDatabaseBatchPut)
			}
		}

		stateRoot := blk.Header().AccountStatesRoot().Hex()
		fmt.Printf("%-8d | %-16s | %-16s | %-12d | %-12d | %-12d\n",
			i, blkHash.Hex()[:16], stateRoot[:16], accSize, scSize, trieDbSize)
	}
}
