package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// GetAddressSelector chuyển đổi 4 byte đầu của hash method thành một common.Address
func GetAddressSelector(methodSignature string) common.Address {
	// 1. Tính Keccak256 hash của chuỗi signature (vd: "transfer(address,uint256)")
	hash := crypto.Keccak256([]byte(methodSignature))

	// 2. Lấy 4 byte đầu tiên (Method Selector)
	// 3. Chuyển 4 byte đó thành kiểu Address (20 bytes)
	// go-ethereum sẽ padding các byte 0 ở phía trước
	return common.BytesToAddress(hash[:4])
}

func main() {
	method := "letuannhat"
	addr := GetAddressSelector(method)

	fmt.Printf("Method Signature: %s\n", method)
	fmt.Printf("Full Hash:        %x\n", crypto.Keccak256([]byte(method)))
	fmt.Printf("4 Bytes Selector: %x\n", crypto.Keccak256([]byte(method))[:4])
	fmt.Printf("Generated Addr:   %s\n", addr.Hex())
}
