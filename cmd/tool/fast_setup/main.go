package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"google.golang.org/protobuf/proto"
)

type AccountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
	Registered bool   `json:"registered"`
	Funded     bool   `json:"funded"`
}

func main() {
	_ = logger.Info
	configPath := os.Args[1]
	accountsPath := os.Args[2]
	action := os.Args[3] // "register" or "fund"

	configIface, _ := c_config.LoadConfig(configPath)
	config := configIface.(*c_config.ClientConfig)

	data, _ := os.ReadFile(accountsPath)
	var accounts []*AccountInfo
	json.Unmarshal(data, &accounts)
	fmt.Printf("Loaded %d accounts\n\n", len(accounts))

	if action == "register" {
		fireAndForgetRegister(config, accounts)
	} else if action == "fund" {
		fireAndForgetFund(config, accounts)
	}
}

func fireAndForgetRegister(config *c_config.ClientConfig, accounts []*AccountInfo) {
	// Create ONE connection
	c, err := client.NewClient(config)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	ctx := c.GetClientContext()
	parentConn := ctx.ConnectionsManager.ParentConnection()

	blsKeyPair := bls.NewKeyPair(config.PrivateKey())
	blsPubKey := blsKeyPair.PublicKey().String()
	chainId := new(big.Int).SetUint64(config.ChainId)

	fmt.Printf("  🚀 Fire-and-forget BLS registration for %d accounts...\n", len(accounts))
	startTime := time.Now()
	sent := 0

	for i, acc := range accounts {
		// Create the BLS registration TX (nonce=0 for new account registration)
		ethTx, err := client.CreateSignedSetBLSPublicKeyTx(acc.PrivateKey, blsPubKey, chainId, 0)
		if err != nil {
			fmt.Printf("  ❌ [%d] CreateTx error: %v\n", i, err)
			continue
		}

		// Convert to internal transaction format
		tx, err := mt_transaction.NewTransactionFromEth(ethTx)
		if err != nil {
			fmt.Printf("  ❌ [%d] NewTransactionFromEth error: %v\n", i, err)
			continue
		}

		// Set device key (using simple hash of address)
		newDeviceKey := common.Hash{}
		rawNewDeviceKeyBytes := []byte(fmt.Sprintf("bls-reg-%s-%d", acc.Address, time.Now().UnixNano()))
		rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
		deviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

		tx.UpdateRelatedAddresses([][]byte{})
		tx.UpdateDeriver(deviceKey, newDeviceKey)
		tx.SetSign(blsKeyPair.PrivateKey())

		// Marshal and send fire-and-forget
		txWithDeviceKey := &pb.TransactionWithDeviceKey{
			Transaction: tx.Proto().(*pb.Transaction),
			DeviceKey:   rawNewDeviceKey,
		}
		bTx, err := proto.Marshal(txWithDeviceKey)
		if err != nil {
			continue
		}

		err = ctx.MessageSender.SendBytes(parentConn, command.SendTransactionWithDeviceKey, bTx)
		if err != nil {
			fmt.Printf("  ❌ [%d] Send error: %v\n", i, err)
			break // Connection dead
		}

		sent++
		if sent%50 == 0 || sent == len(accounts) {
			elapsed := time.Since(startTime).Seconds()
			rate := float64(sent) / elapsed
			fmt.Printf("\r  📊 Sent: %d/%d | Elapsed: %.1fs | Rate: %.0f/s    ",
				sent, len(accounts), elapsed, rate)
		}

		// Tiny delay to avoid overwhelming
		time.Sleep(5 * time.Millisecond)
	}

	fmt.Println()
	elapsed := time.Since(startTime)
	fmt.Printf("  ✅ Sent %d registration TXs in %.1fs (%.0f TX/s)\n", sent, elapsed.Seconds(), float64(sent)/elapsed.Seconds())
	fmt.Println("  ⏳ Waiting 30s for chain to process all registrations...")
	time.Sleep(30 * time.Second)

	// Quick check: sample first, middle, last (with timeout)
	fmt.Println("  Spot check:")
	for _, idx := range []int{0, len(accounts) / 2, len(accounts) - 1} {
		vc, err := client.NewClient(config)
		if err != nil {
			fmt.Printf("    [%d] ❌ connect error: %v\n", idx, err)
			continue
		}
		addr := common.HexToAddress(accounts[idx].Address)
		as, err := vc.AccountState(addr)
		if err != nil {
			fmt.Printf("    [%d] %s ❌ error: %v\n", idx, accounts[idx].Address[:10], err)
			continue
		}
		if as != nil {
			fmt.Printf("    [%d] %s nonce=%d\n", idx, accounts[idx].Address[:10], as.Nonce())
		}
	}
}

func fireAndForgetFund(config *c_config.ClientConfig, accounts []*AccountInfo) {
	amountStr := "1000000000000000000" // 1 ETH
	amount, _ := new(big.Int).SetString(amountStr, 10)

	blsKeyPair := bls.NewKeyPair(config.PrivateKey())
	ecdsaKey, _ := crypto.ToECDSA(config.PrivateKey())
	funderAddress := crypto.PubkeyToAddress(ecdsaKey.PublicKey)
	relatedAddresses := [][]byte{blsKeyPair.Address().Bytes()}

	// Get funder nonce
	fc, _ := client.NewClient(config)
	fas, _ := fc.AccountState(funderAddress)
	nonce := fas.Nonce()
	fmt.Printf("  Funder %s nonce=%d\n", funderAddress.Hex()[:10], nonce)

	// Create single connection
	sendClient, _ := client.NewClient(config)
	ctx := sendClient.GetClientContext()
	parentConn := ctx.ConnectionsManager.ParentConnection()

	maxGas := uint64(10000000)
	maxGasPrice := uint64(p_common.MINIMUM_BASE_FEE)

	fmt.Printf("  🚀 Funding %d accounts with 1 ETH each...\n", len(accounts))
	startTime := time.Now()
	sent := 0

	for i, acc := range accounts {
		toAddr := common.HexToAddress(acc.Address)

		lastDeviceKey := common.Hash{}
		rawNewDeviceKeyBytes := []byte(fmt.Sprintf("fund-%d-%d", i, time.Now().UnixNano()))
		rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
		newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

		tx := mt_transaction.NewTransaction(
			funderAddress, toAddr, amount,
			maxGas, maxGasPrice, 0, nil,
			relatedAddresses, lastDeviceKey, newDeviceKey,
			nonce, config.ChainId,
		)
		tx.SetSign(blsKeyPair.PrivateKey())

		txWithDeviceKey := &pb.TransactionWithDeviceKey{
			Transaction: tx.Proto().(*pb.Transaction),
			DeviceKey:   rawNewDeviceKey,
		}
		bTx, _ := proto.Marshal(txWithDeviceKey)

		err := ctx.MessageSender.SendBytes(parentConn, command.SendTransactionWithDeviceKey, bTx)
		if err != nil {
			fmt.Printf("\n  ❌ [%d] Send error: %v\n", i, err)
			break
		}

		sent++
		nonce++

		if sent%50 == 0 || sent == len(accounts) {
			elapsed := time.Since(startTime).Seconds()
			rate := float64(sent) / elapsed
			fmt.Printf("\r  📊 Sent: %d/%d | Elapsed: %.1fs | Rate: %.0f/s    ",
				sent, len(accounts), elapsed, rate)
		}

		// Delay to let chain process nonces sequentially
		// Chain TPS ~40-50, so 25ms = ~40 TX/s should be safe
		time.Sleep(25 * time.Millisecond)
	}

	fmt.Println()
	elapsed := time.Since(startTime)
	fmt.Printf("  ✅ Sent %d funding TXs in %.1fs\n", sent, elapsed.Seconds())
	fmt.Println("  ⏳ Waiting 30s for chain...")
	time.Sleep(30 * time.Second)

	// Spot check
	fmt.Println("  Spot check:")
	for _, idx := range []int{0, len(accounts) / 4, len(accounts) / 2, 3 * len(accounts) / 4, len(accounts) - 1} {
		vc, _ := client.NewClient(config)
		addr := common.HexToAddress(accounts[idx].Address)
		as, _ := vc.AccountState(addr)
		if as != nil {
			fmt.Printf("    [%d] %s nonce=%d balance=%s\n", idx, accounts[idx].Address[:10], as.Nonce(), as.PendingBalance().String())
		}
	}

	_ = hex.EncodeToString
}
