package main
import (
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)
func main() {
	addr := common.HexToAddress("0x824fef8A3cE4b93C546209CC254D97E5Fee804e0")
	for i := uint64(0); i < 300; i++ {
		created := crypto.CreateAddress(addr, i)
		if created.Hex() == "0xa7B6b3C927f4c0a632Ea54942DA756e55c3fC98b" {
			fmt.Println("Found at nonce:", i)
		}
	}
}
