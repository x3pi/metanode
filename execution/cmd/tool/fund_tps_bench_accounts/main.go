// Command fund_tps_bench_accounts pre-funds the exact deterministic accounts
// tps_benchmark_e2e (cmd/tool/tps_benchmark_e2e) signs its zero-value
// transfers from. That tool derives NumAccounts addresses from a fixed seed
// ("tps-bench-e2e-<index>") and needs each to cover its own gas fee
// (amount sent is always 0, but gasLimit*gasPrice still has to come from
// somewhere) -- on a devnet whose genesis wasn't specifically built to
// pre-fund that exact seed (e.g. deploy/cluster/local_devnet's fixture),
// every account starts at zero balance and every benchmark tx is rejected
// for insufficient balance. Run this once against a freshly-reset devnet,
// from any already-funded account, before running tps_benchmark_e2e.
//
// See deploy/cluster/local_devnet/run_load_test.sh for the full,
// one-command repeatable recipe (reset -> start -> fund -> benchmark) this
// tool is one step of.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// generateAccounts mirrors tps_benchmark_e2e's own deterministic account
// derivation exactly, so this funds precisely the addresses that tool will
// sign transactions from.
func generateAccounts(n int) []common.Address {
	out := make([]common.Address, n)
	for i := 0; i < n; i++ {
		seed := make([]byte, 32)
		seed[0] = byte(i >> 24)
		seed[1] = byte(i >> 16)
		seed[2] = byte(i >> 8)
		seed[3] = byte(i)
		key := crypto.Keccak256(append([]byte("tps-bench-e2e-"), seed...))
		ecdsaKey, err := crypto.ToECDSA(key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "derive key %d: %v\n", i, err)
			os.Exit(1)
		}
		out[i] = crypto.PubkeyToAddress(ecdsaKey.PublicKey)
	}
	return out
}

func main() {
	node := flag.String("node", "http://127.0.0.1:18545", "RPC endpoint")
	chainID := flag.Uint64("chain-id", 1337, "chain id")
	fromKeyHex := flag.String("key", "", "hex private key of a funded account")
	n := flag.Int("n", 100, "number of tps-bench-e2e accounts to fund")
	amountStr := flag.String("amount", "100000000000000000", "wei to send each account")
	waitConfirm := flag.Bool("wait", true, "poll until the last funded account's balance is visible before exiting")
	waitTimeout := flag.Duration("wait-timeout", 60*time.Second, "max time to wait for -wait")
	flag.Parse()

	if *fromKeyHex == "" {
		fmt.Fprintln(os.Stderr, "-key is required")
		os.Exit(1)
	}

	pk, err := crypto.HexToECDSA(*fromKeyHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid key: %v\n", err)
		os.Exit(1)
	}
	fromAddr := crypto.PubkeyToAddress(pk.PublicKey)

	amount, ok := new(big.Int).SetString(*amountStr, 10)
	if !ok {
		fmt.Fprintln(os.Stderr, "invalid -amount")
		os.Exit(1)
	}

	client, err := ethclient.Dial(*node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()

	nonce, err := client.PendingNonceAt(ctx, fromAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nonce: %v\n", err)
		os.Exit(1)
	}

	accounts := generateAccounts(*n)
	signer := types.LatestSignerForChainID(new(big.Int).SetUint64(*chainID))
	gasPrice := big.NewInt(1000000)

	sent := 0
	for i, addr := range accounts {
		tx := types.NewTransaction(nonce+uint64(i), addr, amount, 21000, gasPrice, nil)
		signedTx, err := types.SignTx(tx, signer, pk)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sign %d: %v\n", i, err)
			continue
		}
		if err := client.SendTransaction(ctx, signedTx); err != nil {
			fmt.Fprintf(os.Stderr, "send %d (%s): %v\n", i, addr.Hex(), err)
			continue
		}
		sent++
	}
	fmt.Printf("Sent %d/%d funding txs from %s (base nonce %d)\n", sent, *n, fromAddr.Hex(), nonce)

	if *waitConfirm && sent > 0 {
		last := accounts[sent-1]
		deadline := time.Now().Add(*waitTimeout)
		for {
			bal, err := client.BalanceAt(ctx, last, nil)
			if err == nil && bal != nil && bal.Sign() > 0 {
				fmt.Printf("✅ Funding confirmed (account[%d]=%s balance=%s)\n", sent-1, last.Hex(), bal.String())
				return
			}
			if time.Now().After(deadline) {
				fmt.Fprintf(os.Stderr, "⚠️ Timed out after %s waiting for funding to land -- check the node is producing blocks\n", waitTimeout.String())
				os.Exit(1)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}
