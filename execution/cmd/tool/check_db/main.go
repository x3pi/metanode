package main

import (
	"fmt"
	"log"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

func main() {
	dbPath := "/home/abc/chain-n/metanode/execution/cmd/simple_chain/sample/node3/back_up/backup_db"
	
	// Open ShardedDB with the exact same parameters as app.go
	db, err := storage.NewShardelDB(dbPath, 16, 2, storage.TypePebbleDB, dbPath)
	if err != nil {
		log.Fatalf("Failed to create ShardedDB: %v", err)
	}
	if err := db.Open(); err != nil {
		log.Fatalf("Failed to open ShardedDB: %v", err)
	}
	defer db.Close()

	key := []byte("block_data_topic-3")
	val, err := db.Get(key)
	if err != nil {
		fmt.Printf("Get error for key %s: %v\n", key, err)
	} else {
		fmt.Printf("Found key %s with %d bytes!\n", key, len(val))
	}
}
