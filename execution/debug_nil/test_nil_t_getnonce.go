package main

import "fmt"

type Proto struct {
	Nonce []byte
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

func main() {
	var t *Transaction = nil
	fmt.Println("Calling GetNonce on nil t...")
	fmt.Println("Nonce:", t.GetNonce())
}
