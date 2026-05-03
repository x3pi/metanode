package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		return
	}
	info, err := os.Lstat(os.Args[1])
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("File: %s\n", os.Args[1])
	fmt.Printf("Size: %d bytes\n", info.Size())
	fmt.Printf("IsDir: %v\n", info.IsDir())
	fmt.Printf("Mode: %v\n", info.Mode())
}
