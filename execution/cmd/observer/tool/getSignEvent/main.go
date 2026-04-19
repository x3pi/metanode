package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	fmt.Printf("MessageSent: %x\n", crypto.Keccak256([]byte("MessageSent(uint256,uint256,bool,address,address,uint256,bytes,uint256)"))[:])
}
