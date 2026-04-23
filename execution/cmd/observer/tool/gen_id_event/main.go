package main

import (
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/sha3"
)

func main() {
	// 1. Định nghĩa signature (Lưu ý: Solidity Event signature chuẩn không có tên biến và khoảng trắng)
	signature := "MessageReceived(uint256,uint256,bytes32,uint8,uint8,bytes,address,uint256)"

	// 2. Hash Keccak-256
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(signature))
	t0 := hash.Sum(nil)

	// 3. In ra định dạng uint8_t cho C++
	fmt.Println("// C++ uint8_t array format:")
	fmt.Printf("uint8_t t0_buf[32] = {")
	for i, b := range t0 {
		fmt.Printf("0x%02x", b)
		if i < len(t0)-1 {
			fmt.Print(", ")
		}
		if (i+1)%8 == 0 && i < len(t0)-1 {
			fmt.Print("\n                          ")
		}
	}
	fmt.Println("};")

	fmt.Println("\n// Hex string format (for Go/Solidity):")
	// 4. In ra dạng Hex string thông thường
	fmt.Printf("string hex = \"0x%s\"\n", hex.EncodeToString(t0))
}
