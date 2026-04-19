//go:build ignore

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run getHashFunc.go <signature>")
		return
	}
	signature := os.Args[1]
	
	// Tự động bổ sung "()" nếu user quên gõ
	if !strings.Contains(signature, "(") {
		signature = signature + "()"
	}

	hash := crypto.Keccak256Hash([]byte(signature))
	fmt.Printf("0x%x\n", hash[:4])
}
