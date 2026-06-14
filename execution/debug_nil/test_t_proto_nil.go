//go:build ignore

package main

import (
	"fmt"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type Transaction struct {
	proto *pb.Transaction
}

func (t *Transaction) GetNonce() uint64 {
	if t.proto != nil && len(t.proto.Nonce) == 8 {
		return 1
	} else {
		return 0
	}
}

func (t *Transaction) ToAddress() []byte {
	return t.proto.ToAddress
}

func main() {
	var pbTx *pb.Transaction = nil
	tx := &Transaction{proto: pbTx}
	
	fmt.Println("GetNonce:")
	nonce := tx.GetNonce()
	fmt.Println("Nonce:", nonce)
	
	fmt.Println("ToAddress:")
	addr := tx.ToAddress()
	fmt.Println("Addr:", addr)
}
