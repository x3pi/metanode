package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type AccountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
	Registered bool   `json:"registered"`
	Funded     bool   `json:"funded"`
}

func main() {
	logger.SetConfig(&logger.LoggerConfig{Flag: logger.FLAG_WARN, Outputs: []*os.File{os.Stdout}})

	configIface, err := c_config.LoadConfig("../tps_benchmark/config.json")
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}
	config := configIface.(*c_config.ClientConfig)

	c, err := client.NewClient(config)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Check funder
	ecdsaKey, _ := crypto.ToECDSA(config.PrivateKey())
	fromAddr := crypto.PubkeyToAddress(ecdsaKey.PublicKey)
	as, _ := c.AccountState(fromAddr)
	fmt.Printf("Funder %s: nonce=%d, balance=%s\n", fromAddr.Hex(), as.Nonce(), as.Balance().String())

	// Load accounts
	data, _ := os.ReadFile("../batch_account_setup/accounts.json")
	var accounts []*AccountInfo
	json.Unmarshal(data, &accounts)

	// Check first 10 accounts
	funded := 0
	for i := 0; i < 10 && i < len(accounts); i++ {
		acc := accounts[i]
		as, err := c.AccountState(common.HexToAddress(acc.Address))
		if err != nil {
			fmt.Printf("  [%d] %s: ERROR %v\n", i, acc.Address[:10], err)
			continue
		}
		bal := as.Balance()
		pendingBal := as.PendingBalance()
		totalBal := as.TotalBalance()
		hasFunds := (totalBal != nil && totalBal.Sign() > 0)
		if hasFunds {
			funded++
		}
		fmt.Printf("  [%d] %s: bal=%s, pending=%s, total=%s, nonce=%d, bls=%v\n",
			i, acc.Address[:10], bal.String(), pendingBal.String(), totalBal.String(), as.Nonce(), len(as.PublicKeyBls()) > 0)
	}
	fmt.Printf("\nFirst 10: %d funded\n", funded)
}
