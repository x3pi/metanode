package main

import (
	"fmt"
	"log"

	"github.com/syndtr/goleveldb/leveldb"
)

func main() {
	count := 0
	for i := 0; i < 16; i++ {
		dbPath := fmt.Sprintf("/home/abc/chain-n/metanode/execution/sample/node0/back_up/backup_db/db%d", i)
		db, err := leveldb.OpenFile(dbPath, nil)
		if err != nil {
			log.Fatalf("Failed to open DB %d: %v", i, err)
		}

		iter := db.NewIterator(nil, nil)
		for iter.Next() {
			fmt.Printf("Key: %s\n", string(iter.Key()))
			count++
			if count > 20 {
				fmt.Println("... (truncated)")
				db.Close()
				iter.Release()
				return
			}
		}
		iter.Release()
		db.Close()
	}

	if count == 0 {
		fmt.Println("All DB shards are empty!")
	}
}
