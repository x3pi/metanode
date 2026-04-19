package main

import (
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
)

func main() {
	fmt.Println("Checking Master DB:")
	checkDb("./cmd/simple_chain/sample/node0/data/data/nomt_db/account_state")

	fmt.Println("\nChecking Sub DB:")
	checkDb("./cmd/simple_chain/sample/node0/data-write/data/nomt_db/account_state")
}

func checkDb(path string) {
	fmt.Println("Opening:", path)
	handle, _ := nomt_ffi.Open(path, 1, 1, 1)
	if handle == nil {
		fmt.Println("Wait, handle is nil")
		return
	}
	defer handle.Close()
	// unfortunately nomt doesn't have a simple iterator exposed via ffi directly except maybe we can query the address
}
