package main

import (
"fmt"
"github.com/ethereum/go-ethereum/common"
"github.com/meta-node-blockchain/meta-node/pkg/transaction"
"github.com/meta-node-blockchain/meta-node/pkg/types"
)

func main() {
tx := &transaction.Transaction{}
tx.SetToAddress(common.HexToAddress("0x1111111111111111111111111111111111111111"))
tx.SetAmount(100)

bytesTx, _ := tx.Marshal()
fmt.Printf("Single TX size: %d\n", len(bytesTx))

batchTxs := []types.Transaction{tx}
bTransaction, _ := transaction.MarshalTransactions(batchTxs)

fmt.Printf("pb.Transactions size: %d\n", len(bTransaction))

// Dump first 10 bytes
for i := 0; i < 10 && i < len(bTransaction); i++ {
tf("%02x ", bTransaction[i])
}
fmt.Println()
}
