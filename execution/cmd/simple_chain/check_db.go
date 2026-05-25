package main

import (
"fmt"
"os"

"github.com/meta-node-blockchain/meta-node/pkg/storage"
"github.com/ethereum/go-ethereum/common"
)

func main() {
dbPath := "/home/abc/chain-n/metanode/execution/cmd/simple_chain/sample/node4/data-write/data/mapping_db/db_shard_0"
db, err := storage.NewPebbleDB(dbPath, false)
if err != nil {
tf("Error: %v\n", err)
()
key := []byte("block_number_hash_1502")
val, err := db.Get(key)
if err != nil {
tf("1502 Not found in db_shard_0: %v\n", err)
} else {
:= common.BytesToHash(val)
tf("1502 hash: %x\n", hash)
}
    
key1501 := []byte("block_number_hash_1501")
val1501, err := db.Get(key1501)
if err != nil {
tf("1501 Not found in db_shard_0: %v\n", err)
} else {
:= common.BytesToHash(val1501)
tf("1501 hash: %x\n", hash1501)
}
}
