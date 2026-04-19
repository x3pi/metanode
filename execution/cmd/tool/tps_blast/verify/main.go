package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

type AccountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
	Registered bool   `json:"registered"`
	Funded     bool   `json:"funded"`
}

var (
	configPath   string
	accountsFile string
	fix          bool
	parallel     int
)

func main() {
	flag.StringVar(&configPath, "config", "../batch_account_setup/config.json", "Client config")
	flag.StringVar(&accountsFile, "accounts", "blast_accounts.json", "Accounts file to verify")
	flag.BoolVar(&fix, "fix", false, "Re-send BLS registration for failed accounts")
	flag.IntVar(&parallel, "parallel", 20, "Parallel verification workers")
	flag.Parse()

	logger.SetConfig(&logger.LoggerConfig{Flag: 0})

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  🔍 BLS Registration Verifier")
	fmt.Println("═══════════════════════════════════════════════════")

	// Load config
	configIface, _ := c_config.LoadConfig(configPath)
	config := configIface.(*c_config.ClientConfig)

	// Load accounts
	data, err := os.ReadFile(accountsFile)
	if err != nil {
		log.Fatalf("Cannot read %s: %v", accountsFile, err)
	}
	var accounts []*AccountInfo
	json.Unmarshal(data, &accounts)
	fmt.Printf("  📋 Loaded %d accounts from %s\n", len(accounts), accountsFile)

	// Connect
	fmt.Printf("  🔌 Connecting to %s...\n", config.ParentConnectionAddress)
	c, err := client.NewClient(config)
	if err != nil {
		log.Fatalf("Connect failed: %v", err)
	}
	fmt.Println("  ✅ Connected")

	// ─── VERIFY ALL ACCOUNTS ──────────────────────────────────
	fmt.Printf("\n🔍 Verifying %d accounts (parallel=%d)...\n", len(accounts), parallel)
	start := time.Now()

	var confirmed int64
	var failed int64
	var errors int64
	var failedAccounts []*AccountInfo
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)

	for _, acc := range accounts {
		wg.Add(1)
		sem <- struct{}{}
		go func(a *AccountInfo) {
			defer wg.Done()
			defer func() { <-sem }()

			addr := common.HexToAddress(a.Address)
			as, err := c.AccountState(addr)
			if err != nil {
				atomic.AddInt64(&errors, 1)
				mu.Lock()
				failedAccounts = append(failedAccounts, a)
				mu.Unlock()
				return
			}

			blsKey := as.PublicKeyBls()
			if blsKey != nil && len(blsKey) > 0 {
				atomic.AddInt64(&confirmed, 1)
				mu.Lock()
				a.Registered = true
				mu.Unlock()
			} else {
				atomic.AddInt64(&failed, 1)
				mu.Lock()
				failedAccounts = append(failedAccounts, a)
				mu.Unlock()
			}
		}(acc)
	}
	wg.Wait()
	elapsed := time.Since(start)

	conf := atomic.LoadInt64(&confirmed)
	fail := atomic.LoadInt64(&failed)
	errs := atomic.LoadInt64(&errors)

	fmt.Printf("\n═══════════════════════════════════════════════════\n")
	fmt.Printf("  📊 VERIFICATION RESULTS\n")
	fmt.Printf("═══════════════════════════════════════════════════\n")
	fmt.Printf("  ✅ BLS confirmed:  %d/%d\n", conf, len(accounts))
	fmt.Printf("  ❌ No BLS key:     %d\n", fail)
	fmt.Printf("  ⚠️  Query errors:  %d\n", errs)
	fmt.Printf("  ⏱️  Time:          %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("═══════════════════════════════════════════════════\n")

	// Save updated accounts
	updatedData, _ := json.MarshalIndent(accounts, "", "  ")
	os.WriteFile(accountsFile, updatedData, 0644)
	fmt.Printf("  💾 Updated %s with registration status\n", accountsFile)

	// ─── FIX: Re-send for failed accounts ─────────────────────
	if len(failedAccounts) > 0 && fix {
		fmt.Printf("\n🔧 Fixing %d failed accounts...\n", len(failedAccounts))

		blsPubKey := bls.NewKeyPair(config.PrivateKey()).PublicKey().String()
		var pKey p_common.PrivateKey
		copy(pKey[:], config.PrivateKey())
		bigChainId := new(big.Int).SetUint64(config.ChainId)

		pConn := c.GetClientContext().ConnectionsManager.ParentConnection()
		if pConn == nil {
			fmt.Println("  ❌ No connection for retry")
			return
		}

		var retrySent int64
		var retryFail int64

		for i, acc := range failedAccounts {
			ethTx, err := client.CreateSignedSetBLSPublicKeyTx(acc.PrivateKey, blsPubKey, bigChainId, 0)
			if err != nil {
				atomic.AddInt64(&retryFail, 1)
				continue
			}
			internalTx, err := transaction.NewTransactionFromEth(ethTx)
			if err != nil {
				atomic.AddInt64(&retryFail, 1)
				continue
			}
			internalTx.UpdateRelatedAddresses([][]byte{})
			internalTx.UpdateDeriver(common.Hash{}, common.Hash{})
			internalTx.SetSign(pKey)
			bTx, err := internalTx.Marshal()
			if err != nil {
				atomic.AddInt64(&retryFail, 1)
				continue
			}

			c.GetClientContext().MessageSender.SendBytes(pConn, command.SendTransaction, bTx)
			atomic.AddInt64(&retrySent, 1)

			if (i+1)%50 == 0 {
				fmt.Printf("\r  📤 Retry sent: %d/%d   ", i+1, len(failedAccounts))
				time.Sleep(100 * time.Millisecond)
			}
		}

		fmt.Printf("\n  📤 Retry sent: %d, Build errors: %d\n", retrySent, retryFail)
		fmt.Println("  ⏳ Wait 30s, then re-run without --fix to verify")
	} else if len(failedAccounts) > 0 {
		fmt.Printf("\n  💡 Run with --fix to re-send BLS for %d failed accounts\n", len(failedAccounts))
	} else {
		fmt.Println("\n  🎉 All accounts verified successfully!")
	}

	c.Close()
}
