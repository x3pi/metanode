package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/common"
)

func main() {
	type Proto struct {
		Padding1 [24]byte
		ToAddress []byte
	}
	type Transaction struct {
		proto *Proto
	}
	
	t := &Transaction{proto: nil}
	fmt.Println("Testing nil inner struct")
	addr := common.BytesToAddress(t.proto.ToAddress)
	fmt.Println("Success:", addr.Hex())
}
