package main

import (
	"bytes"
	"fmt"
	"github.com/cockroachdb/pebble"
)

func checkMapping(nodeName string, path string) {
	fmt.Printf("=== Checking %s mapping DB at %s ===\n", nodeName, path)
	db, err := pebble.Open(path, &pebble.Options{ReadOnly: true})
	if err != nil {
		fmt.Printf("Failed to open DB: %v\n", err)
		return
	}
	defer db.Close()

	iter, err := db.NewIter(nil)
	if err != nil {
		fmt.Printf("Failed to create iterator: %v\n", err)
		return
	}
	defer iter.Close()

	targetEthHash := "0x62b34f1e2752db65ea03555d8cf14a0fff249d04bcc4175d250e0dce58f25d9f"
	targetKey := "ethHashMapBlsHashPrefix" + targetEthHash

	found := false
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if bytes.Contains(iter.Key(), []byte("62b34f1e")) || bytes.Contains(iter.Key(), []byte(targetEthHash)) || key == targetKey {
			fmt.Printf("Found match: Key = %s, Value = 0x%x\n", key, iter.Value())
			found = true
		}
	}
	if !found {
		fmt.Println("Target mapping not found!")
	}
}

func main() {
	checkMapping("Node 0", "/home/abc/chain-n/metanode/execution/cmd/simple_chain/sample/node0/data/data/history/mapping/db_shard_0")
	checkMapping("Node 1", "/home/abc/chain-n/metanode/execution/cmd/simple_chain/sample/node1/data/data/history/mapping/db_shard_0")
}
