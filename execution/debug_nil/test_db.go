//go:build ignore

package main

import (
	"fmt"
	"github.com/cockroachdb/pebble"
)

func main() {
	db, _ := pebble.Open("/home/abc/nhat/con-chain-v2/metanode/execution/cmd/simple_chain/sample/node-0/changelog", nil)
	iter, _ := db.NewIter(nil)
	count := 0
	for iter.First(); iter.Valid() && count < 10; iter.Next() {
		fmt.Printf("Key: %x\n", iter.Key())
		count++
	}
}
