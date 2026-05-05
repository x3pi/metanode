//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"log"
	"github.com/cockroachdb/pebble"
)

func main() {
	db, err := pebble.Open("sample/node0/data/data/xapian_node/pebble_db", &pebble.Options{})
	if err != nil {
		log.Fatalf("Error opening db: %v", err)
	}
	defer db.Close()

	iter := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("system_txs_"),
		UpperBound: []byte("system_txs_`"),
	})
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		fmt.Printf("Found Key: %s\n", iter.Key())
		count++
	}
	fmt.Printf("Total system_txs keys found: %d\n", count)
}
