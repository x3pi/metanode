package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
)

type AccountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
	Registered bool   `json:"registered"`
	Funded     bool   `json:"funded"`
	Nonce      uint64 `json:"nonce"`
}

func main() {
	b, err := os.ReadFile("../blast_accounts.json")
	if err != nil {
		fmt.Println("Error reading accounts:", err)
		return
	}

	var accounts []*AccountInfo
	if err := json.Unmarshal(b, &accounts); err != nil {
		fmt.Println("Error unmarshaling:", err)
		return
	}

	config, err := c_config.LoadConfig("../config.json")
	if err != nil {
		fmt.Println("Error loading config:", err)
		return
	}

	c, err := client.NewClient(config.(*c_config.ClientConfig))
	if err != nil {
		fmt.Println("Error creating client:", err)
		return
	}

	fmt.Println("Checking unregistered accounts...")
	for _, acc := range accounts {
		if !acc.Registered {
			as, err := c.AccountState(common.HexToAddress(acc.Address))
			if err != nil {
				fmt.Printf("Account %s: error %v\n", acc.Address, err)
				continue
			}
			if as != nil {
				blsKey := as.PublicKeyBls()
				fmt.Printf("Account %s: On-chain Nonce=%d, BLS Key length=%d\n", acc.Address, as.Nonce(), len(blsKey))
				break
			} else {
				fmt.Printf("Account %s: state is nil\n", acc.Address)
				break
			}
		}
	}
}
