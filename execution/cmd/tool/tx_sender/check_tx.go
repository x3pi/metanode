package main
import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"log"
	"math/big"
)
func main() {
	client, err := ethclient.Dial("http://127.0.0.1:8757")
	if err != nil {
		log.Fatal(err)
	}
	block, err := client.BlockByNumber(context.Background(), big.NewInt(167))
	if err != nil {
		log.Fatal(err)
	}
	target := common.HexToAddress("0xa7B6b3C927f4c0a632Ea54942DA756e55c3fC98b")
	fmt.Printf("Block 167 has %d txs\n", len(block.Transactions()))
	found := false
	for i, tx := range block.Transactions() {
		if tx.To() != nil && *tx.To() == target {
			fmt.Printf("Found tx %d with to = %s, hash: %s\n", i, target.Hex(), tx.Hash().Hex())
			found = true
		}
	}
	if !found {
		fmt.Println("Not found in any tx.To()")
	}
}
