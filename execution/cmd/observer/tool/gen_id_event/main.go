package main

import (
	"fmt"

	"golang.org/x/crypto/sha3"
)

func main() {
	// 1. Định nghĩa signature (Lưu ý: không có khoảng trắng, tên biến)
	signature := "MessageSent(uint256,uint256,bytes32,bool,address,address,uint256,bytes,uint256)"

	// 2. Hash Keccak-256
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(signature))
	t0 := hash.Sum(nil)

	// 3. In ra định dạng uint8_t cho C++
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
}
