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
	type Transaction struct {
		ToAddress []byte
	}
	var t = &Transaction{} // t is NOT nil
	fmt.Println("Testing valid struct with nil slice")
	addr := common.BytesToAddress(t.ToAddress)
	fmt.Println("Success:", addr.Hex())
}
