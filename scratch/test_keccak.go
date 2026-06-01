package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	fmt.Printf("runStep1_Setup(): %x\n", crypto.Keccak256([]byte("runStep1_Setup()"))[:4])
	fmt.Printf("runStep2_ReadBack(): %x\n", crypto.Keccak256([]byte("runStep2_ReadBack()"))[:4])
	fmt.Printf("runStep3_UpdateDoc(): %x\n", crypto.Keccak256([]byte("runStep3_UpdateDoc()"))[:4])
}
