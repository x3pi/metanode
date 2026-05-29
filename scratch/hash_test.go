package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	fmt.Printf("Selector: %x\n", crypto.Keccak256([]byte("runStep1_Setup()"))[:4])
}
