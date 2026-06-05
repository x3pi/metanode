package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/common"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Panicked:", r)
		}
	}()
	fmt.Println("Start")
	addr := common.BytesToAddress(nil)
	fmt.Println("Success:", addr.Hex())

	type Transaction struct {
		ToAddress []byte
	}
	var t *Transaction
	fmt.Println("Testing nil pointer dereference")
	addr2 := common.BytesToAddress(t.ToAddress)
	fmt.Println("Success 2:", addr2.Hex())
}
