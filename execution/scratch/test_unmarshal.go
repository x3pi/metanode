package main

import (
	"fmt"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/ethereum/go-ethereum/common"
)

func main() {
	txList := []types.Transaction{}
	tx := transaction.NewTransaction(
		common.HexToAddress("0x824fef8A3cE4b93C546209CC254D97E5Fee804e0"),
		common.HexToAddress("0x0000000000000000000000000000000000000000"),
		nil,
		"0",
		0,
		0,
		nil,
	)
	txList = append(txList, tx)
	txList = append(txList, tx)

	bytes, err := transaction.MarshalTransactions(txList)
	if err != nil {
		fmt.Printf("Marshal failed: %v\n", err)
	}

	singleTx, err := transaction.UnmarshalTransaction(bytes)
	if err != nil {
		fmt.Printf("UnmarshalTransaction failed (EXPECTED): %v\n", err)
	} else {
		fmt.Printf("UnmarshalTransaction SUCCEEDED SILENTLY! (BUG)\n")
		fmt.Printf("ToAddress: %s\n", singleTx.ToAddress().Hex())
		if singleTx.FromAddress() == (common.Address{}) {
			fmt.Printf("FromAddress is EMPTY\n")
		}
	}
}
