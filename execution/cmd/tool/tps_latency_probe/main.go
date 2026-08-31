// tps_latency_probe measures real per-transaction latency: wall-clock time
// from eth_sendRawTransaction returning a hash to eth_getTransactionReceipt
// first reporting that tx as mined. This is a different metric from
// tps_benchmark_e2e's bulk throughput numbers — it answers "how long does a
// single user's transaction take to confirm", not "how many tx/s can the
// chain sustain".
//
// Usage:
//
//	tps_latency_probe -node=http://127.0.0.1:19545 -chain-id=7777 -n=50 -gap=500ms
//	tps_latency_probe -node=http://127.0.0.1:19545 -chain-id=7777 -n=50 -gap=500ms -concurrent-load=20000
package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type rpcReq struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

func call(node, method string, params ...interface{}) (json.RawMessage, error) {
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	resp, err := httpClient.Post(node, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", r.Error.Code, r.Error.Message)
	}
	return r.Result, nil
}

func getNonce(node string, addr common.Address) (uint64, error) {
	res, err := call(node, "eth_getTransactionCount", addr.Hex(), "latest")
	if err != nil {
		return 0, err
	}
	var s string
	json.Unmarshal(res, &s)
	s = strings.TrimPrefix(s, "0x")
	n := new(big.Int)
	n.SetString(s, 16)
	return n.Uint64(), nil
}

func receiptExists(node, txHash string) (bool, error) {
	res, err := call(node, "eth_getTransactionReceipt", txHash)
	if err != nil {
		return false, err
	}
	return string(res) != "null" && len(res) > 0, nil
}

func buildAndSign(key *ecdsa.PrivateKey, nonce uint64, chainID uint64, to common.Address) ([]byte, error) {
	tx := types.NewTransaction(nonce, to, big.NewInt(0), 10_000_000, big.NewInt(1_000_000), nil)
	signer := types.LatestSignerForChainID(big.NewInt(int64(chainID)))
	signedTx, err := types.SignTx(tx, signer, key)
	if err != nil {
		return nil, err
	}
	return signedTx.MarshalBinary()
}

func sendRaw(node string, raw []byte) (string, error) {
	res, err := call(node, "eth_sendRawTransaction", "0x"+hex.EncodeToString(raw))
	if err != nil {
		return "", err
	}
	var s string
	json.Unmarshal(res, &s)
	return s, nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func main() {
	node := flag.String("node", "http://127.0.0.1:19545", "RPC endpoint")
	chainID := flag.Uint64("chain-id", 7777, "chain id")
	n := flag.Int("n", 50, "number of probe transactions")
	gap := flag.Duration("gap", 500*time.Millisecond, "gap between probe sends")
	pollEvery := flag.Duration("poll", 3*time.Millisecond, "receipt poll interval")
	maxWait := flag.Duration("max-wait", 10*time.Second, "max wait per probe tx")
	keyHex := flag.String("key", "", "hex private key of a funded account (required — a fresh random key has 0 balance)")
	flag.Parse()

	if *keyHex == "" {
		fmt.Fprintln(os.Stderr, "-key is required (hex private key of a funded dev account)")
		os.Exit(1)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(*keyHex, "0x"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "key parse:", err)
		os.Exit(1)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000009999")

	nonce, err := getNonce(*node, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "getNonce:", err)
		os.Exit(1)
	}

	var latencies []float64 // ms
	fmt.Printf("Probing %d transactions against %s (gap=%s)...\n", *n, *node, *gap)

	for i := 0; i < *n; i++ {
		raw, err := buildAndSign(key, nonce, *chainID, to)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sign:", err)
			continue
		}
		nonce++

		t0 := time.Now()
		txHash, err := sendRaw(*node, raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%d] send error: %v\n", i, err)
			continue
		}

		deadline := time.Now().Add(*maxWait)
		found := false
		for time.Now().Before(deadline) {
			ok, err := receiptExists(*node, txHash)
			if err == nil && ok {
				elapsed := time.Since(t0)
				latencies = append(latencies, float64(elapsed.Microseconds())/1000.0)
				fmt.Printf("[%3d] %s  latency=%8.2fms  sent_unix_ms=%d  confirmed_unix_ms=%d\n",
					i, txHash, float64(elapsed.Microseconds())/1000.0, t0.UnixMilli(), time.Now().UnixMilli())
				if trace, err := call(*node, "debug_getTransactionTrace", txHash); err == nil {
					fmt.Printf("      trace: %s\n", string(trace))
				} else {
					fmt.Printf("      trace: unavailable (%v)\n", err)
				}
				found = true
				break
			}
			time.Sleep(*pollEvery)
		}
		if !found {
			fmt.Fprintf(os.Stderr, "[%d] %s TIMEOUT after %s (sent_unix_ms=%d)\n", i, txHash, *maxWait, t0.UnixMilli())
		}

		time.Sleep(*gap)
	}

	if len(latencies) == 0 {
		fmt.Println("No successful probes.")
		return
	}
	sort.Float64s(latencies)
	sum := 0.0
	for _, l := range latencies {
		sum += l
	}
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("Samples: %d/%d confirmed\n", len(latencies), *n)
	fmt.Printf("Min:     %8.2f ms\n", latencies[0])
	fmt.Printf("p50:     %8.2f ms\n", percentile(latencies, 0.50))
	fmt.Printf("p90:     %8.2f ms\n", percentile(latencies, 0.90))
	fmt.Printf("p99:     %8.2f ms\n", percentile(latencies, 0.99))
	fmt.Printf("Max:     %8.2f ms\n", latencies[len(latencies)-1])
	fmt.Printf("Mean:    %8.2f ms\n", sum/float64(len(latencies)))
	fmt.Println("═══════════════════════════════════════════")
}
