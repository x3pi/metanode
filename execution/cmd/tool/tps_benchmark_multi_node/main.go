package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

type AccountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
	Funded     bool   `json:"funded"`
}

func main() {
	logger.SetConfig(&logger.LoggerConfig{Flag: logger.FLAG_WARN, Outputs: []*os.File{os.Stdout}})

	aData, err := os.ReadFile("../batch_account_setup/accounts.json")
	if err != nil {
		log.Fatalf("Failed to read accounts file: %v", err)
	}
	var allAccounts []AccountInfo
	json.Unmarshal(aData, &allAccounts)

	var accounts []AccountInfo
	for _, acc := range allAccounts {
		if acc.Funded {
			accounts = append(accounts, acc)
		}
	}
	fmt.Printf("✅ Loaded %d verified funded accounts for benchmarking.\n", len(accounts))

	if len(accounts) == 0 {
		log.Fatal("No funded accounts found in accounts.json")
	}

	baseConfig, _ := config.LoadConfig("../batch_account_setup/config.json")
	cConfig := baseConfig.(*config.ClientConfig)

	ports := []string{"4200", "6200", "6210", "6220", "6240"}
	var clients []*client.Client
	for _, p := range ports {
		cConfig.ParentConnectionAddress = "192.168.1.232:" + p
		cli, err := client.NewClient(cConfig)
		if err == nil {
			clients = append(clients, cli)
		} else {
			fmt.Printf("⚠️ Failed to connect to port %s: %v\n", p, err)
		}
	}
	if len(clients) == 0 {
		log.Fatal("No nodes available")
	}
	fmt.Printf("🔌 Connected to %d active nodes\n", len(clients))

	fmt.Printf("🔍 Fetching accurate nonces for %d accounts concurrently...\n", len(accounts))
	var wg sync.WaitGroup
	nonces := make([]uint64, len(accounts))
	for i, acc := range accounts {
		wg.Add(1)
		go func(idx int, addr string) {
			defer wg.Done()
			cli := clients[idx%len(clients)]
			as, err := cli.AccountState(common.HexToAddress(addr))
			if err == nil {
				nonces[idx] = as.Nonce()
			}
		}(i, acc.Address)
	}
	wg.Wait()

	fmt.Println("📦 Preparing offline Native Transfer payloads...")
	var rawTxs [][]byte

	// A random valid related address
	relBytes := make([][]byte, 1)
	relBytes[0], _ = hex.DecodeString("1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5")
	amount := big.NewInt(0)

	for i, acc := range accounts {
		privBytes, _ := hex.DecodeString(acc.PrivateKey)
		ecdsaKey, _ := crypto.ToECDSA(privBytes)
		fromAddr := crypto.PubkeyToAddress(ecdsaKey.PublicKey)

		nonce := nonces[i]

		destAddr := common.HexToAddress(fmt.Sprintf("0x000000000000000000000000000000000000%04x", i))

		tx := transaction.NewTransaction(
			fromAddr,
			destAddr, // Send to a unique dummy address
			amount,
			10000000,
			1000000,
			0,   // maxTimeUse
			nil, // raw data for normal transfer
			relBytes,
			common.Hash{},
			common.Hash{},
			nonce,
			cConfig.ChainId,
		)
		var pKey p_common.PrivateKey
		copy(pKey[:], cConfig.PrivateKey())
		tx.SetSign(pKey)

		bTx, _ := tx.Marshal()
		rawTxs = append(rawTxs, bTx)
	}

	fmt.Printf("\n🚀 BLASTING %d TRANSACTIONS CONCURRENTLY ACROSS %d NODES...\n", len(rawTxs), len(clients))
	startTime := time.Now()

	var wgBlast sync.WaitGroup
	for i, txBytes := range rawTxs {
		wgBlast.Add(1)
		go func(idx int, data []byte) {
			defer wgBlast.Done()
			cli := clients[idx%len(clients)]
			pConn := cli.GetClientContext().ConnectionsManager.ParentConnection()
			if pConn != nil {
				cli.GetClientContext().MessageSender.SendBytes(
					pConn,
					command.SendTransaction,
					data,
				)
			}
		}(i, txBytes)
	}
	wgBlast.Wait()

	injectTime := time.Since(startTime)
	fmt.Printf("📤 All %d queries dispatched in %s!\n", len(rawTxs), injectTime)

	fmt.Println("⏳ Holding 15 seconds to allow consensus ordering/execution...")
	time.Sleep(15 * time.Second)

	fmt.Println("🔍 Verifying new nonces across all accounts...")
	var confirmed int
	var mu sync.Mutex

	for i, acc := range accounts {
		wg.Add(1)
		go func(idx int, addr string) {
			defer wg.Done()
			cli := clients[idx%len(clients)]
			as, err := cli.AccountState(common.HexToAddress(addr))
			if err == nil {
				if as.Nonce() > nonces[idx] {
					mu.Lock()
					confirmed++
					mu.Unlock()
				}
			}
		}(i, acc.Address)
	}
	wg.Wait()

	fmt.Println("\n=======================================================")
	fmt.Println("                  TPS BENCHMARK RESULTS")
	fmt.Println("=======================================================")
	fmt.Printf("📊 Payload Executed: %d / %d\n", confirmed, len(rawTxs))

	tps := float64(confirmed) / 15.0
	fmt.Printf("🚀 Sustained TPS:   ~%.2f tx/sec (15s boundary)\n", tps)
	fmt.Println("=======================================================")
}
