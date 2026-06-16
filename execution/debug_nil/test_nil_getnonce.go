//go:build ignore

package main

import (
	"fmt"
)

type Proto struct {
	Padding1  [24]byte
	Nonce     []byte
	ToAddress []byte
}

type Transaction struct {
	proto *Proto
}

func (t *Transaction) GetNonce() uint64 {
	if t.proto != nil && len(t.proto.Nonce) == 8 {
		return 1
	}
	return 0
}

func (t *Transaction) IsDeployContract() bool {
	return t.GetNonce() != 0 && len(t.proto.ToAddress) == 0
}

func main() {
	var t *Transaction = nil
	fmt.Println("Testing nil struct pointer")
	t.IsDeployContract()
}
