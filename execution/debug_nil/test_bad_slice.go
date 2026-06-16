//go:build ignore

package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"unsafe"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Panicked:", r)
		}
	}()
	type SliceHeader struct {
		Data unsafe.Pointer
		Len  int
		Cap  int
	}

	var badSlice []byte
	hdr := (*SliceHeader)(unsafe.Pointer(&badSlice))
	hdr.Data = nil // Nil pointer
	hdr.Len = 5    // But length is 5
	hdr.Cap = 5

	fmt.Println("Testing bad slice")
	addr := common.BytesToAddress(badSlice)
	fmt.Println("Success:", addr.Hex())
}
