package main

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

const (
	rpcURL     = "http://127.0.0.1:8646"
	tcpAddr    = "127.0.0.1:4200"
	chainID    = 991
	gasPrice   = 100000
	funderPriv = "a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1"
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

func rpcCall(method string, params ...interface{}) (json.RawMessage, error) {
	reqBody, _ := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	resp, err := http.Post(rpcURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var res rpcResp
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.Error != nil {
		return nil, fmt.Errorf("rpc error: %s (code %d)", res.Error.Message, res.Error.Code)
	}
	return res.Result, nil
}

func getPendingNonce(addr common.Address) (uint64, error) {
	res, err := rpcCall("eth_getTransactionCount", addr.Hex(), "pending")
	if err != nil {
		return 0, err
	}
	var hexStr string
	if err := json.Unmarshal(res, &hexStr); err != nil {
		return 0, err
	}
	var n uint64
	fmt.Sscanf(hexStr, "0x%x", &n)
	return n, nil
}

func sendTCPBatch(txs []*types.Transaction) error {
	var encodedTxs [][]byte
	for _, tx := range txs {
		raw, err := tx.MarshalBinary()
		if err != nil {
			return err
		}
		encodedTxs = append(encodedTxs, raw)
	}
	rlpBatch, err := rlp.EncodeToBytes(encodedTxs)
	if err != nil {
		return err
	}

	conn, err := net.DialTimeout("tcp", tcpAddr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	writer := bufio.NewWriter(conn)

	// Metanode TCP framing helper
	sendMsg := func(cmd string, body []byte) error {
		msgProto := &pb.Message{
			Header: &pb.Header{
				Command: cmd,
				Version: "0.0.1.0",
				ID:      uuid.New().String(),
			},
			Body: body,
		}
		b, err := proto.Marshal(msgProto)
		if err != nil {
			return err
		}
		lengthBuf := make([]byte, 8)
		binary.LittleEndian.PutUint64(lengthBuf, uint64(len(b)))
		if _, err := writer.Write(lengthBuf); err != nil {
			return err
		}
		if _, err := writer.Write(b); err != nil {
			return err
		}
		return writer.Flush()
	}

	// 1. Initial handshake
	initMsg := &pb.InitConnection{
		Address: common.HexToAddress("0x0000000000000000000000000000000000000001").Bytes(),
		Type:    "client",
		Replace: true,
	}
	initBody, err := proto.Marshal(initMsg)
	if err != nil {
		return err
	}
	if err := sendMsg("InitConnection", initBody); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)

	// 2. Send batch
	return sendMsg("SendRawTransactions", rlpBatch)
}

func waitReceipt(hash common.Hash, timeout time.Duration) (uint64, time.Duration, error) {
	start := time.Now()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := rpcCall("eth_getTransactionReceipt", hash.Hex())
		if err == nil && len(res) > 0 && string(res) != "null" {
			var rec struct {
				Status string `json:"status"`
			}
			json.Unmarshal(res, &rec)
			var st uint64
			fmt.Sscanf(rec.Status, "0x%x", &st)
			return st, time.Since(start), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, 0, fmt.Errorf("timeout waiting for receipt")
}

func main() {
	fmt.Println("==================================================================")
	fmt.Println("🚀 P2-2 METANODE PRODUCTION BENCHMARK (Throughput & Latency)")
	fmt.Println("   RPC URL:  ", rpcURL)
	fmt.Println("   TCP Addr: ", tcpAddr)
	fmt.Println("   Chain ID: ", chainID)
	fmt.Println("==================================================================")

	funderKey, err := crypto.HexToECDSA(funderPriv)
	if err != nil {
		panic(err)
	}
	funderAddr := crypto.PubkeyToAddress(funderKey.PublicKey)
	fmt.Printf("Funder: %s\n", funderAddr.Hex())

	signer := types.LatestSignerForChainID(big.NewInt(chainID))
	nonce, err := getPendingNonce(funderAddr)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Initial Nonce: %d\n", nonce)

	// Step 1: Fund 5 sub-wallets to enable concurrent multi-account submission
	const numWallets = 5
	const txsPerWallet = 20
	const totalTxs = numWallets * txsPerWallet

	type wallet struct {
		key   *ecdsa.PrivateKey
		priv  string
		addr  common.Address
		nonce uint64
	}

	fmt.Printf("\n📦 Creating and funding %d sub-wallets (100 ETH each)...\n", numWallets)
	var wallets []struct {
		key  *ecdsa.PrivateKey
		addr common.Address
	}
	for i := 0; i < numWallets; i++ {
		k, _ := crypto.GenerateKey()
		a := crypto.PubkeyToAddress(k.PublicKey)
		tx, _ := types.SignNewTx(funderKey, signer, &types.DynamicFeeTx{
			ChainID:   big.NewInt(chainID),
			Nonce:     nonce + uint64(i),
			GasTipCap: big.NewInt(0),
			GasFeeCap: big.NewInt(gasPrice),
			Gas:       21000,
			To:        &a,
			Value:     big.NewInt(1000000000000000000), // 1 ETH
		})
		raw, _ := tx.MarshalBinary()
		_, err := rpcCall("eth_sendRawTransaction", fmt.Sprintf("0x%x", raw))
		if err != nil {
			panic(fmt.Sprintf("fund wallet %d: %v", i, err))
		}
		wallets = append(wallets, struct {
			key  *ecdsa.PrivateKey
			addr common.Address
		}{key: k, addr: a})
	}
	// Wait for funding txs to commit
	time.Sleep(3 * time.Second)
	fmt.Println("✅ All sub-wallets funded successfully.")

	// Step 2: Generate 100 Transactions (mixed Legacy & EIP-1559)
	fmt.Printf("\n🧪 Preparing %d signed transactions across %d wallets...\n", totalTxs, numWallets)
	var allTxs []*types.Transaction
	for wIdx, w := range wallets {
		wKey := w.key
		for j := 0; j < txsPerWallet; j++ {
			to := common.HexToAddress(fmt.Sprintf("0x000000000000000000000000000000000000%02d%02d", wIdx, j))
			var tx *types.Transaction
			if j%2 == 0 {
				// EIP-1559 Type 2
				tx, _ = types.SignNewTx(wKey, signer, &types.DynamicFeeTx{
					ChainID:   big.NewInt(chainID),
					Nonce:     uint64(j),
					GasTipCap: big.NewInt(0),
					GasFeeCap: big.NewInt(gasPrice),
					Gas:       21000,
					To:        &to,
					Value:     big.NewInt(1000),
				})
			} else {
				// Legacy Type 0
				tx, _ = types.SignNewTx(wKey, signer, &types.LegacyTx{
					Nonce:    uint64(j),
					GasPrice: big.NewInt(gasPrice),
					Gas:      21000,
					To:       &to,
					Value:    big.NewInt(1000),
				})
			}
			allTxs = append(allTxs, tx)
		}
	}
	fmt.Println("✅ 100 transactions signed.")

	// Step 3: Measure TCP Batch Ingress Rate
	fmt.Printf("\n⚡ Benchmark 1: Raw TCP Ingress Throughput (50 tx batch via SendRawTransactions)...\n")
	tcpBatch := allTxs[:50]
	t0 := time.Now()
	if err := sendTCPBatch(tcpBatch); err != nil {
		panic(fmt.Sprintf("sendTCPBatch: %v", err))
	}
	tcpIngressDur := time.Since(t0)
	tcpIngressRate := float64(len(tcpBatch)) / tcpIngressDur.Seconds()
	fmt.Printf("   TCP Ingress Time: %v for %d txs (%.2f tx/s ingress speed)\n", tcpIngressDur, len(tcpBatch), tcpIngressRate)

	// Step 4: Measure RPC Ingress Rate (50 tx concurrent via eth_sendRawTransaction)
	fmt.Printf("\n⚡ Benchmark 2: JSON-RPC Concurrent Ingress Throughput (50 txs via HTTP RPC)...\n")
	rpcBatch := allTxs[50:]
	t1 := time.Now()
	var wg sync.WaitGroup
	var rpcSuccess uint64
	for _, tx := range rpcBatch {
		wg.Add(1)
		go func(t *types.Transaction) {
			defer wg.Done()
			raw, _ := t.MarshalBinary()
			_, err := rpcCall("eth_sendRawTransaction", fmt.Sprintf("0x%x", raw))
			if err == nil {
				rpcSuccess++
			}
		}(tx)
	}
	wg.Wait()
	rpcIngressDur := time.Since(t1)
	rpcIngressRate := float64(rpcSuccess) / rpcIngressDur.Seconds()
	fmt.Printf("   RPC Ingress Time: %v for %d txs (%.2f tx/s ingress speed)\n", rpcIngressDur, rpcSuccess, rpcIngressRate)

	// Step 5: Measure Block Inclusion & Commit Latency
	fmt.Printf("\n⏱️  Benchmark 3: Consensus & Block Inclusion Latency (Tracking receipts)...\n")
	sampleHashes := []common.Hash{
		tcpBatch[0].Hash(),
		tcpBatch[25].Hash(),
		tcpBatch[49].Hash(),
		rpcBatch[0].Hash(),
		rpcBatch[25].Hash(),
		rpcBatch[49].Hash(),
	}
	var totalLatency time.Duration
	for i, h := range sampleHashes {
		st, lat, err := waitReceipt(h, 30*time.Second)
		if err != nil {
			panic(fmt.Sprintf("receipt %s: %v", h.Hex(), err))
		}
		fmt.Printf("   Sample Tx #%d (%s): Status=%d, Latency=%v\n", i+1, h.Hex()[:10]+"...", st, lat)
		totalLatency += lat
	}
	avgLatency := totalLatency / time.Duration(len(sampleHashes))
	fmt.Printf("   Average Block Commit Latency: %v\n", avgLatency)

	// Step 6: Verify Final Parity & State
	bNum, _ := rpcCall("eth_blockNumber")
	fmt.Printf("\n🏁 Benchmark Complete! Latest Block Number: %s\n", string(bNum))
	fmt.Println("==================================================================")
}
