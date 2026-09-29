//go:build ignore

package main

// stress_concurrent_transfers.go fires N cross-chain transfers CONCURRENTLY from exec1 to N
// distinct freshly-registered exec2 target addresses, each for a distinct amount (so a
// target/value mixup bug -- e.g. two concurrently-processed system txs stomping on the wrong
// MessageRecord -- would show up as a WRONG credited amount, not just a missing one). Exists to
// stress-test the cross-chain credit flow closed out on 2026-09-29 (commits 15761838, cb9f3499)
// under concurrent load, since everything verified so far was a single transfer at a time.
//
// Run against an already-running devnet (see run_devnet.sh) with:
//   go run stress_concurrent_transfers.go

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

const numConcurrent = 10

type sender struct {
	EcdsaKey string `json:"ecdsa_private_key"`
	BlsKey   string `json:"bls_private_key"`
}

func loadSenders() []sender {
	b, err := os.ReadFile("devnet_senders.json")
	if err != nil {
		panic(err)
	}
	var s []sender
	if err := json.Unmarshal(b, &s); err != nil || len(s) < numConcurrent {
		panic(fmt.Sprintf("need %d senders in devnet_senders.json: %v", numConcurrent, err))
	}
	return s
}

func rpcCallStress(url, method string, params []interface{}) (map[string]interface{}, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "method": method, "params": params, "id": 1,
	})
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var res map[string]interface{}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	if errResp, ok := res["error"]; ok {
		return nil, fmt.Errorf("rpc error: %v", errResp)
	}
	return res, nil
}

type result struct {
	idx        int
	targetAddr common.Address
	wantAmount int64
	gotAmount  *big.Int
	sendTries  int
	err        string
	elapsed    time.Duration
}

func main() {
	parentClient := parentchain.NewHTTPClient("http://127.0.0.1:8547")

	exec2Hex := "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
	exec2Priv, exec2PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec2Hex)

	exec1Hex := "0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c"
	_, exec1PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec1Hex)

	bootstrapAddr := common.HexToAddress("0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5")
	fmt.Println("0. Bootstrapping exec1's own ChainRegistry entry via DepositToFloat...")
	if _, err := parentClient.SendDepositToFloat(exec1PubKey, 1, bootstrapAddr, bootstrapAddr, big.NewInt(1_000_000_000_000_000_000)); err != nil {
		fmt.Printf("   DepositToFloat failed (ok if already bootstrapped): %v\n", err)
	}

	fmt.Printf("1. Registering %d fresh target addresses to exec2...\n", numConcurrent)
	targets := make([]common.Address, numConcurrent)
	for i := 0; i < numConcurrent; i++ {
		targetPriv, _ := crypto.GenerateKey()
		targetAddr := crypto.PubkeyToAddress(targetPriv.PublicKey)
		digest := parentchain.ComputeRegisterAccountMessage(targetAddr, exec2PubKey)
		userSig, _ := crypto.Sign(crypto.Keccak256(digest), targetPriv)
		clusterSig := bls.Sign(exec2Priv, digest)
		if _, err := parentClient.SendRegisterAccount(targetAddr, exec2PubKey, userSig, clusterSig); err != nil {
			fmt.Printf("   RegisterAccount[%d] failed: %v\n", i, err)
			return
		}
		targets[i] = targetAddr
	}
	// Give the registrations a moment to land before firing transfers that depend on them.
	time.Sleep(3 * time.Second)

	senders := loadSenders()
	fmt.Printf("2. Firing %d cross-chain transfers CONCURRENTLY (distinct senders, amounts, targets)...\n", numConcurrent)
	results := make([]result, numConcurrent)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			amount := int64(1000 + idx) // distinct per-transfer amount to catch a mixup bug
			r := result{idx: idx, targetAddr: targets[idx], wantAmount: amount}
			t0 := time.Now()

			// Sender nonce is read fresh per RPC call on the SAME shared devnet sender account
			// (mtn_api.go's devnetSenderPrivateKeyHex) -- concurrent goroutines racing on that
			// read is a KNOWN, pre-existing RPC-layer limitation (not part of the rollup credit
			// flow being stress-tested here), so retry on failure with a short backoff to let
			// the nonce settle, same as a real client would.
			var sendErr error
			for attempt := 0; attempt < 15; attempt++ {
				r.sendTries++
				amountHex := fmt.Sprintf("0x%x", amount)
				_, sendErr = rpcCallStress("http://localhost:8646", "mtn_sendCrossChainTransfer",
					[]interface{}{r.targetAddr.Hex(), amountHex, senders[idx].EcdsaKey, senders[idx].BlsKey})
				if sendErr == nil {
					break
				}
				time.Sleep(time.Duration(100+attempt*100) * time.Millisecond)
			}
			if sendErr != nil {
				r.err = fmt.Sprintf("send failed after %d tries: %v", r.sendTries, sendErr)
				r.elapsed = time.Since(t0)
				results[idx] = r
				return
			}

			deadline := time.Now().Add(90 * time.Second)
			for time.Now().Before(deadline) {
				res2, err := rpcCallStress("http://localhost:8647", "mtn_getAccountState", []interface{}{r.targetAddr.Hex(), "latest"})
				if err == nil {
					if m, ok := res2["result"].(map[string]interface{}); ok && m != nil {
						if balStr, ok := m["balance"].(string); ok {
							bal := new(big.Int)
							bal.SetString(balStr, 10)
							if bal.Sign() > 0 {
								r.gotAmount = bal
								r.elapsed = time.Since(t0)
								results[idx] = r
								return
							}
						}
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			r.err = "timeout waiting for credit"
			r.elapsed = time.Since(t0)
			results[idx] = r
		}(i)
	}
	wg.Wait()
	totalElapsed := time.Since(start)

	fmt.Printf("\n=== Results (total wall time %v) ===\n", totalElapsed)
	successCount := 0
	mismatchCount := 0
	failCount := 0
	for _, r := range results {
		switch {
		case r.err != "":
			failCount++
			fmt.Printf("  [%d] FAIL   target=%s want=%d tries=%d elapsed=%v err=%s\n",
				r.idx, r.targetAddr.Hex(), r.wantAmount, r.sendTries, r.elapsed, r.err)
		case r.gotAmount.Cmp(big.NewInt(r.wantAmount)) != 0:
			mismatchCount++
			fmt.Printf("  [%d] MISMATCH target=%s want=%d got=%s tries=%d elapsed=%v\n",
				r.idx, r.targetAddr.Hex(), r.wantAmount, r.gotAmount.String(), r.sendTries, r.elapsed)
		default:
			successCount++
			fmt.Printf("  [%d] OK     target=%s amount=%d tries=%d elapsed=%v\n",
				r.idx, r.targetAddr.Hex(), r.wantAmount, r.sendTries, r.elapsed)
		}
	}
	fmt.Printf("\nSummary: %d/%d OK, %d mismatch, %d fail\n", successCount, numConcurrent, mismatchCount, failCount)
}
