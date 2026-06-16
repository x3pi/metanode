//go:build ignore

package main

import (
	"fmt"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

func main() {
	var tx pb.Transaction
	// Create corrupted protobuf data: a bytes field with length 100 but no data
	// Field 2 (ToAddress): wire type 2 (length-delimited), tag = 2 << 3 | 2 = 18
	badData := []byte{18, 100}

	err := proto.Unmarshal(badData, &tx)
	fmt.Println("Error:", err)

	fmt.Printf("ToAddress len: %d, cap: %d, isNil: %v\n", len(tx.ToAddress), cap(tx.ToAddress), tx.ToAddress == nil)
}
