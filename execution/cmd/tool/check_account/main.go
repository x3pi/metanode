package main

import (
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func main() {
	logger.SetConfig(&logger.LoggerConfig{
		Flag:    logger.FLAG_ERROR,
		Outputs: []*os.File{os.Stdout},
	})

	if len(os.Args) < 3 {
		fmt.Println("Usage: check_account <config.json> <address>")
		os.Exit(1)
	}

	configPath := os.Args[1]
	address := common.HexToAddress(os.Args[2])

	configIface, err := c_config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("❌ Config error: %v\n", err)
		os.Exit(1)
	}
	config := configIface.(*c_config.ClientConfig)

	c, err := client.NewClient(config)
	if err != nil {
		fmt.Printf("❌ Connect error: %v\n", err)
		os.Exit(1)
	}

	as, err := c.AccountState(address)
	if err != nil {
		fmt.Printf("❌ AccountState error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("══════ Account State ══════\n")
	fmt.Printf("  Address:        %s\n", as.Address().Hex())
	fmt.Printf("  Nonce:          %d\n", as.Nonce())
	fmt.Printf("  Balance:        %s\n", as.Balance().String())
	fmt.Printf("  PendingBalance: %s\n", as.PendingBalance().String())
	fmt.Printf("  LastHash:       %s\n", as.LastHash().Hex())
	fmt.Printf("  AccountType:    %v\n", as.AccountType())
	fmt.Printf("  PublicKeyBls:   %x\n", as.PublicKeyBls())

	sc := as.SmartContractState()
	if sc != nil {
		fmt.Printf("  SC State:       ✅ EXISTS\n")
		fmt.Printf("  SC Creator:     %s\n", sc.CreatorAddress().Hex())
		fmt.Printf("  SC CodeHash:    %s\n", sc.CodeHash().Hex())
		fmt.Printf("  SC StorageRoot: %s\n", sc.StorageRoot().Hex())
		fmt.Printf("  SC StorageAddr: %s\n", sc.StorageAddress().Hex())
		fmt.Printf("  SC LogsHash:    %s\n", sc.LogsHash().Hex())
	} else {
		fmt.Printf("  SC State:       ❌ nil (not a smart contract)\n")
	}
	fmt.Printf("═══════════════════════════\n")
}
