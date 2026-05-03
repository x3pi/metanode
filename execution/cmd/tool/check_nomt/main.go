package main

import (
	"fmt"
	"log"
	"os"

	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: check_nomt <path>")
	}
	path := os.Args[1]

	handle, err := nomt_ffi.Open(path, 4, 128, 128)
	if err != nil {
		log.Fatalf("Failed to open: %v", err)
	}
	defer handle.Close()

	root, err := handle.Root()
	if err != nil {
		log.Fatalf("Failed to get root: %v", err)
	}

	fmt.Printf("Root for %s is %x\n", path, root)
}
