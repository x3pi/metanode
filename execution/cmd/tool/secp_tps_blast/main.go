package main

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// AccountInfo represents an account from generated_keys.json
type AccountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
}

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

type RpcNodesJSON struct {
	Nodes    map[string]string `json:"nodes"`
	TcpNodes map[string]string `json:"tcp_nodes"`
	Roles    map[string]string `json:"roles"`
}

// Config stores all benchmark parameters
type Config struct {
	Count        int
	BatchSize    int
	Rounds       int
	Mode         string // "tcp", "rpc", "both"
	TxType       string // "1559", "legacy"
	KeysFile     string
	RPCUrls      []string
	TCPAddrs     []string
	ChainID      int64
	GasPrice     int64
	VerifyParity bool
	ReportFile   string
	DurationSec  int
	RateLimit    int
}

// BenchmarkResult stores the summary of the benchmark run
type BenchmarkResult struct {
	Timestamp           string        `json:"timestamp"`
	ChainID             int64         `json:"chain_id"`
	Mode                string        `json:"mode"`
	TxType              string        `json:"tx_type"`
	TotalSubmitted      int           `json:"total_submitted"`
	TotalConfirmed      int           `json:"total_confirmed"`
	TotalReverted       int           `json:"total_reverted"`
	TotalDropped        int           `json:"total_dropped"`
	InjectionDuration   string        `json:"injection_duration"`
	InjectionTPS        float64       `json:"injection_tps"`
	CommitDuration      string        `json:"commit_duration"`
	EffectiveTPS        float64       `json:"effective_tps"`
	LatencyP50          time.Duration `json:"latency_p50"`
	LatencyP90          time.Duration `json:"latency_p90"`
	LatencyP95          time.Duration `json:"latency_p95"`
	LatencyP99          time.Duration `json:"latency_p99"`
	LatencyAvg          time.Duration `json:"latency_avg"`
	StartBlock          uint64        `json:"start_block"`
	EndBlock            uint64        `json:"end_block"`
	BlocksProduced      uint64        `json:"blocks_produced"`
	ZeroForkVerified    bool          `json:"zero_fork_verified"`
	NodeRootsConsistent bool          `json:"node_roots_consistent"`
	MaxCPU              string        `json:"max_cpu"`
	MaxRSSMB            int           `json:"max_rss_mb"`
}

type rawTCPClient struct {
	addr   string
	conn   net.Conn
	writer *bufio.Writer
	mu     sync.Mutex
	closed bool
}

func newRawTCPClient(addr string, clientAddr common.Address) (*rawTCPClient, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	client := &rawTCPClient{
		addr:   addr,
		conn:   conn,
		writer: bufio.NewWriterSize(conn, 4*1024*1024),
	}

	// Drain responses in background to prevent TCP receive window stalls
	go func() {
		buf := make([]byte, 64*1024)
		for {
			_, err := conn.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	initMsg := &pb.InitConnection{
		Address: clientAddr.Bytes(),
		Type:    "client",
		Replace: false,
	}
	body, err := proto.Marshal(initMsg)
	if err != nil {
		conn.Close()
		return nil, err
	}

	msgProto := &pb.Message{
		Header: &pb.Header{
			Command: "InitConnection",
			Version: "0.0.1.0",
			ID:      uuid.New().String(),
		},
		Body: body,
	}
	b, err := proto.Marshal(msgProto)
	if err != nil {
		conn.Close()
		return nil, err
	}
	lengthBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(lengthBuf, uint64(len(b)))
	if _, err := client.writer.Write(lengthBuf); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := client.writer.Write(b); err != nil {
		conn.Close()
		return nil, err
	}
	if err := client.writer.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	time.Sleep(150 * time.Millisecond)
	return client, nil
}

func (c *rawTCPClient) SendBatch(txs []*types.Transaction) error {
	c.mu.Lock()
	defer c.mu.Unlock()

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

	msgProto := &pb.Message{
		Header: &pb.Header{
			Command: "SendRawTransactions",
			Version: "0.0.1.0",
			ID:      uuid.New().String(),
		},
		Body: rlpBatch,
	}
	b, err := proto.Marshal(msgProto)
	if err != nil {
		return err
	}
	lengthBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(lengthBuf, uint64(len(b)))
	if _, err := c.writer.Write(lengthBuf); err != nil {
		return err
	}
	if _, err := c.writer.Write(b); err != nil {
		return err
	}
	return c.writer.Flush()
}

func (c *rawTCPClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.conn.Close()
	}
}

func rpcCallTo(url, method string, params ...interface{}) (json.RawMessage, error) {
	reqBody, err := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
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

func getChainID(rpcURL string) (int64, error) {
	res, err := rpcCallTo(rpcURL, "eth_chainId")
	if err != nil {
		return 0, err
	}
	var hexStr string
	if err := json.Unmarshal(res, &hexStr); err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(hexStr, "0x"), 16, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func getBlockNumber(rpcURL string) (uint64, error) {
	res, err := rpcCallTo(rpcURL, "eth_blockNumber")
	if err != nil {
		return 0, err
	}
	var hexStr string
	if err := json.Unmarshal(res, &hexStr); err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(hexStr, "0x"), 16, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func getPendingNonce(rpcURL string, addr common.Address) (uint64, error) {
	res, err := rpcCallTo(rpcURL, "eth_getTransactionCount", addr.Hex(), "pending")
	if err != nil {
		return 0, err
	}
	var hexStr string
	if err := json.Unmarshal(res, &hexStr); err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(hexStr, "0x"), 16, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func loadEndpointsFromJSON(path string) ([]string, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var r RpcNodesJSON
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, nil
	}
	var rpcs []string
	var tcps []string
	if len(r.Roles) > 0 {
		for name := range r.Roles {
			if u, ok := r.Nodes[name]; ok {
				rpcs = append(rpcs, u)
			}
			if t, ok := r.TcpNodes[name]; ok {
				tcps = append(tcps, t)
			}
		}
	} else {
		for _, u := range r.Nodes {
			rpcs = append(rpcs, u)
		}
		for _, t := range r.TcpNodes {
			tcps = append(tcps, t)
		}
	}
	sort.Strings(rpcs)
	sort.Strings(tcps)
	return rpcs, tcps
}

func main() {
	var (
		flagCount        = flag.Int("count", 1000, "Total number of transactions to blast per round")
		flagBatch        = flag.Int("batch", 500, "Batch size per TCP/RPC packet")
		flagRounds       = flag.Int("rounds", 1, "Number of test rounds to execute")
		flagMode         = flag.String("mode", "tcp", "Traffic mode: tcp, rpc, or both")
		flagTxType       = flag.String("type", "1559", "Transaction type: 1559 (DynamicFee) or legacy")
		flagKeys         = flag.String("keys", "", "Path to generated_keys.json (optional)")
		flagRPC          = flag.String("rpc", "", "Comma-separated RPC URLs (default auto-discover from node config)")
		flagTCP          = flag.String("tcp", "", "Comma-separated TCP endpoints (default auto-discover from node config)")
		flagNodesConfig  = flag.String("nodes-config", "", "Path to rpc_nodes.json config (optional)")
		flagChainID      = flag.Int64("chain-id", 0, "Chain ID (default: query from RPC)")
		flagGasPrice     = flag.Int64("gas-price", 100000, "Gas price in wei")
		flagVerifyParity = flag.Bool("verify-parity", true, "Verify 100% Zero-Fork parity across all online nodes")
		flagReport       = flag.String("report", "", "File path to save JSON benchmark report")
		flagDuration     = flag.Int("duration", 0, "Sustained blasting duration in seconds (if >0, ignores count)")
		flagRateLimit    = flag.Int("rate-limit", 0, "Target injection rate limit in tx/s (0 = unlimited)")
	)
	flag.Parse()

	fmt.Println("==================================================================")
	fmt.Println("🚀 SECP256K1 / EIP-1559 HIGH-PERFORMANCE WORKLOAD BENCHMARK")
	fmt.Println("   Zero-Fork Compliant (AGENTS.md Part 2.5) | Production SECP Mode")
	fmt.Println("==================================================================")

	var rpcURLs []string
	var tcpAddrs []string

	if *flagRPC != "" {
		for _, s := range strings.Split(*flagRPC, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				rpcURLs = append(rpcURLs, s)
			}
		}
	}
	if *flagTCP != "" {
		for _, s := range strings.Split(*flagTCP, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				tcpAddrs = append(tcpAddrs, s)
			}
		}
	}

	if len(rpcURLs) == 0 || len(tcpAddrs) == 0 {
		var candidates []string
		if *flagNodesConfig != "" {
			candidates = append(candidates, *flagNodesConfig)
		}
		candidates = append(candidates, "/tmp/rpc_nodes.json", "/tmp/rpc_nodes.exec1.json")
		for _, c := range candidates {
			discoveredRPC, discoveredTCP := loadEndpointsFromJSON(c)
			if len(discoveredRPC) > 0 && len(discoveredTCP) > 0 {
				if len(rpcURLs) == 0 {
					rpcURLs = discoveredRPC
				}
				if len(tcpAddrs) == 0 {
					tcpAddrs = discoveredTCP
				}
				break
			}
		}
	}

	if len(rpcURLs) == 0 {
		rpcURLs = []string{"http://127.0.0.1:10746"}
	}
	if len(tcpAddrs) == 0 {
		tcpAddrs = []string{"127.0.0.1:6200"}
	}

	fmt.Printf("🌐 RPC Pool (%d nodes): %s\n", len(rpcURLs), strings.Join(rpcURLs, ", "))
	fmt.Printf("⚡ TCP Pool (%d nodes): %s\n", len(tcpAddrs), strings.Join(tcpAddrs, ", "))

	chainID := *flagChainID
	if chainID == 0 {
		id, err := getChainID(rpcURLs[0])
		if err != nil {
			fmt.Printf("⚠️ Failed to query eth_chainId from %s: %v, defaulting to 991\n", rpcURLs[0], err)
			chainID = 991
		} else {
			chainID = id
		}
	}
	fmt.Printf("🆔 Chain ID: %d\n", chainID)

	keysPath := *flagKeys
	if keysPath == "" {
		candidates := []string{
			"/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json",
			"../metanode-suite/test_tps/gen_spam_keys/generated_keys.json",
			"../../metanode-suite/test_tps/gen_spam_keys/generated_keys.json",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				keysPath = c
				break
			}
		}
	}

	var accounts []AccountInfo
	if keysPath != "" {
		if data, err := os.ReadFile(keysPath); err == nil {
			var accs []AccountInfo
			if err := json.Unmarshal(data, &accs); err == nil && len(accs) > 0 {
				accounts = accs
				fmt.Printf("🔑 Loaded %d pre-allocated accounts from %s\n", len(accounts), filepath.Base(keysPath))
			}
		}
	}

	numTxs := *flagCount
	if numTxs > len(accounts) && len(accounts) > 0 {
		fmt.Printf("⚠️ Requested count %d > loaded accounts %d, clamping to %d\n", numTxs, len(accounts), len(accounts))
		numTxs = len(accounts)
	} else if len(accounts) == 0 {
		fmt.Printf("⚠️ No accounts file found. Generating and funding %d sub-wallets...\n", numTxs)
		funderPriv := "a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1"
		funderKey, err := crypto.HexToECDSA(funderPriv)
		if err != nil {
			panic(fmt.Sprintf("invalid funder key: %v", err))
		}
		funderAddr := crypto.PubkeyToAddress(funderKey.PublicKey)
		funderNonce, err := getPendingNonce(rpcURLs[0], funderAddr)
		if err != nil {
			panic(fmt.Sprintf("get funder nonce: %v", err))
		}

		signer := types.LatestSignerForChainID(big.NewInt(chainID))
		for i := 0; i < numTxs; i++ {
			k, _ := crypto.GenerateKey()
			a := crypto.PubkeyToAddress(k.PublicKey)
			tx, _ := types.SignNewTx(funderKey, signer, &types.DynamicFeeTx{
				ChainID:   big.NewInt(chainID),
				Nonce:     funderNonce + uint64(i),
				GasTipCap: big.NewInt(0),
				GasFeeCap: big.NewInt(*flagGasPrice),
				Gas:       21000,
				To:        &a,
				Value:     big.NewInt(1000000000000000000),
			})
			raw, _ := tx.MarshalBinary()
			_, _ = rpcCallTo(rpcURLs[0], "eth_sendRawTransaction", fmt.Sprintf("0x%x", raw))
			accounts = append(accounts, AccountInfo{
				Index:      i,
				PrivateKey: fmt.Sprintf("%x", crypto.FromECDSA(k)),
				Address:    a.Hex(),
			})
		}
		time.Sleep(3 * time.Second)
		fmt.Printf("✅ Funded %d temporary sub-wallets.\n", len(accounts))
	}

	cfg := Config{
		Count:        numTxs,
		BatchSize:    *flagBatch,
		Rounds:       *flagRounds,
		Mode:         *flagMode,
		TxType:       *flagTxType,
		KeysFile:     keysPath,
		RPCUrls:      rpcURLs,
		TCPAddrs:     tcpAddrs,
		ChainID:      chainID,
		GasPrice:     *flagGasPrice,
		VerifyParity: *flagVerifyParity,
		ReportFile:   *flagReport,
		DurationSec:  *flagDuration,
		RateLimit:    *flagRateLimit,
	}

	if cfg.DurationSec > 0 {
		runSustainedBenchmark(cfg, accounts)
	} else {
		for r := 1; r <= cfg.Rounds; r++ {
			fmt.Printf("\n▶️ RUNNING ROUND %d/%d\n", r, cfg.Rounds)
			runBenchmarkRound(r, cfg, accounts[:cfg.Count])
		}
	}
}

func runBenchmarkRound(round int, cfg Config, accounts []AccountInfo) {
	startBlock, err := getBlockNumber(cfg.RPCUrls[0])
	if err != nil {
		fmt.Printf("⚠️ Warning: unable to get start block: %v\n", err)
	}
	fmt.Printf("🏁 Start Block: #%d\n", startBlock)

	// Step 1: Fetch Nonces
	fmt.Printf("\n🔍 Fetching nonces for %d accounts across %d RPC nodes...\n", len(accounts), len(cfg.RPCUrls))
	tNonce0 := time.Now()
	type AccountWithNonce struct {
		Account AccountInfo
		Key     *ecdsa.PrivateKey
		Nonce   uint64
	}
	accsWithNonce := make([]AccountWithNonce, len(accounts))
	workerPool := make(chan struct{}, 64)
	var wg sync.WaitGroup
	var nonceErrCount uint64

	for i := range accounts {
		wg.Add(1)
		workerPool <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-workerPool }()

			acc := accounts[idx]
			key, err := crypto.HexToECDSA(acc.PrivateKey)
			if err != nil {
				atomic.AddUint64(&nonceErrCount, 1)
				return
			}
			addr := crypto.PubkeyToAddress(key.PublicKey)
			rpcEndpoint := cfg.RPCUrls[idx%len(cfg.RPCUrls)]
			nonce, err := getPendingNonce(rpcEndpoint, addr)
			if err != nil {
				atomic.AddUint64(&nonceErrCount, 1)
				return
			}
			accsWithNonce[idx] = AccountWithNonce{
				Account: acc,
				Key:     key,
				Nonce:   nonce,
			}
		}(i)
	}
	wg.Wait()
	nonceDur := time.Since(tNonce0)
	fmt.Printf("✅ Nonces fetched in %v (Errors: %d)\n", nonceDur, nonceErrCount)

	var validAccs []AccountWithNonce
	for _, a := range accsWithNonce {
		if a.Key != nil {
			validAccs = append(validAccs, a)
		}
	}
	if len(validAccs) == 0 {
		fmt.Println("❌ No valid accounts available to blast transactions.")
		os.Exit(1)
	}

	// Step 2: Pre-sign EIP-1559 Transactions
	fmt.Printf("\n✍️ Pre-signing %d %s transactions...\n", len(validAccs), cfg.TxType)
	tSign0 := time.Now()
	signer := types.LatestSignerForChainID(big.NewInt(cfg.ChainID))
	signedTxs := make([]*types.Transaction, len(validAccs))
	targetAddr := common.HexToAddress("0x000000000000000000000000000000000000dEaD")

	var signWg sync.WaitGroup
	signWorkers := make(chan struct{}, 64)
	for i := range validAccs {
		signWg.Add(1)
		signWorkers <- struct{}{}
		go func(idx int) {
			defer signWg.Done()
			defer func() { <-signWorkers }()

			a := validAccs[idx]
			var tx *types.Transaction
			var err error
			if cfg.TxType == "legacy" {
				tx, err = types.SignNewTx(a.Key, signer, &types.LegacyTx{
					Nonce:    a.Nonce,
					GasPrice: big.NewInt(cfg.GasPrice),
					Gas:      21000,
					To:       &targetAddr,
					Value:    big.NewInt(100),
				})
			} else {
				tx, err = types.SignNewTx(a.Key, signer, &types.DynamicFeeTx{
					ChainID:   big.NewInt(cfg.ChainID),
					Nonce:     a.Nonce,
					GasTipCap: big.NewInt(0),
					GasFeeCap: big.NewInt(cfg.GasPrice),
					Gas:       21000,
					To:        &targetAddr,
					Value:     big.NewInt(100),
				})
			}
			if err != nil {
				return
			}
			signedTxs[idx] = tx
		}(i)
	}
	signWg.Wait()
	signDur := time.Since(tSign0)
	fmt.Printf("✅ Pre-signed %d txs in %v (%.2f tx/s signing speed)\n", len(signedTxs), signDur, float64(len(signedTxs))/signDur.Seconds())

	// Step 3: Initialize TCP Connection Pool if TCP mode
	var tcpClients []*rawTCPClient
	if cfg.Mode == "tcp" || cfg.Mode == "both" {
		fmt.Printf("\n🔌 Initializing TCP connection pool (%d endpoints)...\n", len(cfg.TCPAddrs))
		clientCounter := 1
		for _, addr := range cfg.TCPAddrs {
			clientAddr := common.BigToAddress(big.NewInt(int64(clientCounter)))
			clientCounter++
			c, err := newRawTCPClient(addr, clientAddr)
			if err != nil {
				fmt.Printf("⚠️ Failed to connect TCP to %s: %v\n", addr, err)
				continue
			}
			tcpClients = append(tcpClients, c)
		}
		defer func() {
			for _, c := range tcpClients {
				c.Close()
			}
		}()
		fmt.Printf("✅ %d TCP connections established & registered.\n", len(tcpClients))
	}

	// Step 4: Inject Transactions
	fmt.Printf("\n🔥 BLASTING %d Transactions across cluster (Mode: %s)...\n", len(signedTxs), cfg.Mode)
	tInject0 := time.Now()

	var maxRecordedCPU float64
	var maxRecordedRSS uint64
	var resMu sync.Mutex
	stopCpuMon := make(chan struct{})
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopCpuMon:
				return
			case <-ticker.C:
				out, err := exec.Command("ps", "-o", "%cpu,rss", "--no-headers", "-C", "simple_chain").Output()
				if err == nil {
					lines := strings.Split(string(out), "\n")
					var curTotalCPU float64
					var curTotalRSS uint64
					for _, line := range lines {
						fields := strings.Fields(line)
						if len(fields) >= 2 {
							var c float64
							var r uint64
							fmt.Sscanf(fields[0], "%f", &c)
							fmt.Sscanf(fields[1], "%d", &r)
							curTotalCPU += c
							curTotalRSS += r
						}
					}
					resMu.Lock()
					if curTotalCPU > maxRecordedCPU {
						maxRecordedCPU = curTotalCPU
					}
					if curTotalRSS > maxRecordedRSS {
						maxRecordedRSS = curTotalRSS
					}
					resMu.Unlock()
				}
			}
		}
	}()

	var submittedTxs int
	var injectedHashes []common.Hash
	var hashesMu sync.Mutex

	if (cfg.Mode == "tcp" || cfg.Mode == "both") && len(tcpClients) > 0 {
		batchSize := cfg.BatchSize
		if batchSize <= 0 {
			batchSize = 500
		}
		totalBatches := (len(signedTxs) + batchSize - 1) / batchSize
		fmt.Printf("📦 Splitting into %d TCP batches (batch size: %d)...\n", totalBatches, batchSize)

		var batchWg sync.WaitGroup
		var tcpSuccess atomic.Uint64

		for bIdx := 0; bIdx < totalBatches; bIdx++ {
			start := bIdx * batchSize
			end := start + batchSize
			if end > len(signedTxs) {
				end = len(signedTxs)
			}
			chunk := signedTxs[start:end]
			client := tcpClients[bIdx%len(tcpClients)]

			batchWg.Add(1)
			go func(txsChunk []*types.Transaction, cl *rawTCPClient) {
				defer batchWg.Done()
				if err := cl.SendBatch(txsChunk); err != nil {
					fmt.Printf("⚠️ TCP batch error to %s: %v\n", cl.addr, err)
				} else {
					tcpSuccess.Add(uint64(len(txsChunk)))
					hashesMu.Lock()
					for _, t := range txsChunk {
						if t != nil {
							injectedHashes = append(injectedHashes, t.Hash())
						}
					}
					hashesMu.Unlock()
				}
			}(chunk, client)
		}
		batchWg.Wait()
		submittedTxs = int(tcpSuccess.Load())
	} else if cfg.Mode == "rpc" || len(tcpClients) == 0 {
		var rpcWg sync.WaitGroup
		rpcLimiter := make(chan struct{}, 64)
		var rpcSuccess atomic.Uint64

		for idx, tx := range signedTxs {
			if tx == nil {
				continue
			}
			targetRPC := cfg.RPCUrls[idx%len(cfg.RPCUrls)]
			rpcWg.Add(1)
			rpcLimiter <- struct{}{}
			go func(t *types.Transaction, rpcURL string) {
				defer rpcWg.Done()
				defer func() { <-rpcLimiter }()

				raw, err := t.MarshalBinary()
				if err != nil {
					return
				}
				_, err = rpcCallTo(rpcURL, "eth_sendRawTransaction", fmt.Sprintf("0x%x", raw))
				if err == nil {
					rpcSuccess.Add(1)
					hashesMu.Lock()
					injectedHashes = append(injectedHashes, t.Hash())
					hashesMu.Unlock()
				}
			}(tx, targetRPC)
		}
		rpcWg.Wait()
		submittedTxs = int(rpcSuccess.Load())
	}

	injectDur := time.Since(tInject0)
	injectTPS := float64(submittedTxs) / injectDur.Seconds()
	fmt.Printf("⚡ Injected %d TXs in %v (Injection TPS: %.2f tx/s)\n", submittedTxs, injectDur, injectTPS)

	// Step 5: Track Inclusion Receipts & Compute Latencies
	fmt.Printf("\n⏱️ Tracking Transaction Receipts for Consensus Inclusion & Latency...\n")
	sampleCount := 50
	if sampleCount > len(injectedHashes) {
		sampleCount = len(injectedHashes)
	}
	var sampleHashes []common.Hash
	if len(injectedHashes) > 0 {
		step := len(injectedHashes) / sampleCount
		if step == 0 {
			step = 1
		}
		for i := 0; i < len(injectedHashes); i += step {
			sampleHashes = append(sampleHashes, injectedHashes[i])
			if len(sampleHashes) >= sampleCount {
				break
			}
		}
	}

	tCommit0 := time.Now()
	var confirmedCount atomic.Uint64
	var revertedCount atomic.Uint64
	var latencies []time.Duration
	var latMu sync.Mutex

	var trackWg sync.WaitGroup
	trackWorkers := make(chan struct{}, 32)
	timeout := 20 * time.Second

	for i, h := range sampleHashes {
		trackWg.Add(1)
		trackWorkers <- struct{}{}
		go func(hash common.Hash, sIdx int) {
			defer trackWg.Done()
			defer func() { <-trackWorkers }()

			start := time.Now()
			deadline := start.Add(timeout)
			rpcURL := cfg.RPCUrls[sIdx%len(cfg.RPCUrls)]

			for time.Now().Before(deadline) {
				res, err := rpcCallTo(rpcURL, "eth_getTransactionReceipt", hash.Hex())
				if err == nil && len(res) > 0 && string(res) != "null" {
					var rec struct {
						Status string `json:"status"`
					}
					json.Unmarshal(res, &rec)
					var st uint64
					fmt.Sscanf(rec.Status, "0x%x", &st)

					elapsed := time.Since(tInject0)
					latMu.Lock()
					latencies = append(latencies, elapsed)
					latMu.Unlock()

					if st == 1 {
						confirmedCount.Add(1)
					} else {
						revertedCount.Add(1)
					}
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}(h, i)
	}
	trackWg.Wait()
	close(stopCpuMon)
	commitDur := time.Since(tCommit0)

	resMu.Lock()
	finalCPU := maxRecordedCPU
	finalRSS := int(maxRecordedRSS / 1024)
	resMu.Unlock()

	// Step 6: Statistics & Latency Percentiles
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var p50, p90, p95, p99, avgLat time.Duration
	if len(latencies) > 0 {
		p50 = latencies[len(latencies)*50/100]
		p90 = latencies[len(latencies)*90/100]
		p95 = latencies[len(latencies)*95/100]
		p99 = latencies[len(latencies)*99/100]
		var totalLat time.Duration
		for _, l := range latencies {
			totalLat += l
		}
		avgLat = totalLat / time.Duration(len(latencies))
	}

	endBlock, _ := getBlockNumber(cfg.RPCUrls[0])
	blocksProduced := uint64(0)
	if endBlock >= startBlock {
		blocksProduced = endBlock - startBlock
	}

	sampleRatio := 1.0
	if len(sampleHashes) > 0 {
		sampleRatio = float64(len(injectedHashes)) / float64(len(sampleHashes))
	}
	extrapolatedConfirmed := int(float64(confirmedCount.Load()) * sampleRatio)
	if extrapolatedConfirmed > submittedTxs {
		extrapolatedConfirmed = submittedTxs
	}
	effectiveTPS := float64(extrapolatedConfirmed) / commitDur.Seconds()

	fmt.Printf("\n📊 BENCHMARK METRICS (Round %d):\n", round)
	fmt.Printf("   • Total Submitted:    %d txs\n", submittedTxs)
	fmt.Printf("   • Sample Verified:    %d/%d (Success: %d, Reverted: %d)\n",
		confirmedCount.Load()+revertedCount.Load(), len(sampleHashes), confirmedCount.Load(), revertedCount.Load())
	fmt.Printf("   • Injection Speed:    %.2f tx/s (Duration: %v)\n", injectTPS, injectDur)
	fmt.Printf("   • Effective TPS:      %.2f tx/s (Duration: %v)\n", effectiveTPS, commitDur)
	fmt.Printf("   • Latency P50:        %v\n", p50)
	fmt.Printf("   • Latency P90:        %v\n", p90)
	fmt.Printf("   • Latency P95:        %v\n", p95)
	fmt.Printf("   • Latency P99:        %v\n", p99)
	fmt.Printf("   • Latency Avg:        %v\n", avgLat)
	fmt.Printf("   • Blocks Produced:    %d (#%d -> #%d)\n", blocksProduced, startBlock, endBlock)
	fmt.Printf("   • Peak CPU (Nodes):   %.1f%%\n", finalCPU)
	fmt.Printf("   • Peak RSS (Nodes):   %d MB\n", finalRSS)

	// Step 7: Zero-Fork Invariant Verification (AGENTS.md Part 2.5)
	zeroForkOk := true
	if cfg.VerifyParity && len(cfg.RPCUrls) > 1 {
		fmt.Printf("\n🛡️ VERIFYING ZERO-FORK INVARIANT ACROSS %d NODES...\n", len(cfg.RPCUrls))
		targetBlockNum := fmt.Sprintf("0x%x", endBlock)
		var blockHashes = make(map[string][]string)
		var stateRoots = make(map[string][]string)

		for _, u := range cfg.RPCUrls {
			res, err := rpcCallTo(u, "eth_getBlockByNumber", targetBlockNum, false)
			if err != nil || len(res) == 0 || string(res) == "null" {
				fmt.Printf("   ⚠️ Node %s did not return block %s\n", u, targetBlockNum)
				continue
			}
			var b struct {
				Hash      string        `json:"hash"`
				StateRoot string        `json:"stateRoot"`
				Txs       []interface{} `json:"transactions"`
			}
			if err := json.Unmarshal(res, &b); err == nil && b.Hash != "" {
				blockHashes[b.Hash] = append(blockHashes[b.Hash], u)
				stateRoots[b.StateRoot] = append(stateRoots[b.StateRoot], u)
				fmt.Printf("   • Node %s: Block %s | Hash: %s... | Root: %s... | Txs: %d\n",
					u, targetBlockNum, b.Hash[:12], b.StateRoot[:12], len(b.Txs))
			}
		}

		if len(blockHashes) > 1 || len(stateRoots) > 1 {
			zeroForkOk = false
			fmt.Printf("🚨 [CRITICAL FORK DETECTED] Multiple block hashes or state roots found across nodes!\n")
			for h, nodes := range blockHashes {
				fmt.Printf("   BlockHash %s: %s\n", h, strings.Join(nodes, ", "))
			}
			for r, nodes := range stateRoots {
				fmt.Printf("   StateRoot %s: %s\n", r, strings.Join(nodes, ", "))
			}
		} else {
			fmt.Println("✅ [ZERO-FORK VERIFIED] 100% agreement across all online nodes on Block Hash & State Root!")
		}
	}

	result := BenchmarkResult{
		Timestamp:           time.Now().UTC().Format(time.RFC3339),
		ChainID:             cfg.ChainID,
		Mode:                cfg.Mode,
		TxType:              cfg.TxType,
		TotalSubmitted:      submittedTxs,
		TotalConfirmed:      extrapolatedConfirmed,
		TotalReverted:       int(revertedCount.Load()),
		TotalDropped:        submittedTxs - extrapolatedConfirmed,
		InjectionDuration:   injectDur.String(),
		InjectionTPS:        injectTPS,
		CommitDuration:      commitDur.String(),
		EffectiveTPS:        effectiveTPS,
		LatencyP50:          p50,
		LatencyP90:          p90,
		LatencyP95:          p95,
		LatencyP99:          p99,
		LatencyAvg:          avgLat,
		StartBlock:          startBlock,
		EndBlock:            endBlock,
		BlocksProduced:      blocksProduced,
		ZeroForkVerified:    zeroForkOk,
		NodeRootsConsistent: zeroForkOk,
		MaxCPU:              fmt.Sprintf("%.1f%%", finalCPU),
		MaxRSSMB:            finalRSS,
	}

	if cfg.ReportFile != "" {
		if data, err := json.MarshalIndent(result, "", "  "); err == nil {
			_ = os.WriteFile(cfg.ReportFile, data, 0644)
			fmt.Printf("📄 JSON Benchmark report saved to: %s\n", cfg.ReportFile)
		}
	}
	fmt.Println("==================================================================")
}

func runSustainedBenchmark(cfg Config, accounts []AccountInfo) {
	fmt.Println("==================================================================")
	fmt.Printf("⏱️ SUSTAINED BENCHMARK: Target Duration: %d seconds\n", cfg.DurationSec)
	fmt.Printf("   Mode: %s | TxType: %s | Wallets: %d | BatchSize: %d\n", cfg.Mode, cfg.TxType, len(accounts), cfg.BatchSize)
	fmt.Println("==================================================================")

	startBlock, err := getBlockNumber(cfg.RPCUrls[0])
	if err != nil {
		fmt.Printf("⚠️ Warning: unable to get start block: %v\n", err)
	}
	fmt.Printf("🏁 Start Block: #%d\n", startBlock)

	// Step 1: Concurrently Fetch Starting Nonces
	fmt.Printf("\n🔍 Fetching starting nonces for %d accounts...\n", len(accounts))
	tNonce0 := time.Now()
	type SustainedAccount struct {
		Key     *ecdsa.PrivateKey
		Address common.Address
		Nonce   uint64
	}
	activeAccs := make([]*SustainedAccount, 0, len(accounts))
	var accMu sync.Mutex
	workerPool := make(chan struct{}, 64)
	var wg sync.WaitGroup

	for i := range accounts {
		wg.Add(1)
		workerPool <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-workerPool }()

			acc := accounts[idx]
			key, err := crypto.HexToECDSA(acc.PrivateKey)
			if err != nil {
				return
			}
			addr := crypto.PubkeyToAddress(key.PublicKey)
			rpcEndpoint := cfg.RPCUrls[idx%len(cfg.RPCUrls)]
			nonce, err := getPendingNonce(rpcEndpoint, addr)
			if err != nil {
				return
			}
			accMu.Lock()
			activeAccs = append(activeAccs, &SustainedAccount{
				Key:     key,
				Address: addr,
				Nonce:   nonce,
			})
			accMu.Unlock()
		}(i)
	}
	wg.Wait()
	fmt.Printf("✅ %d active wallets ready with starting nonces (fetched in %v)\n", len(activeAccs), time.Since(tNonce0))

	if len(activeAccs) == 0 {
		fmt.Println("❌ No valid accounts available for sustained blasting.")
		return
	}

	// Step 2: Establish TCP pool if TCP mode
	var tcpClients []*rawTCPClient
	if cfg.Mode == "tcp" || cfg.Mode == "both" {
		fmt.Printf("\n🔌 Establishing %d TCP connections for high-throughput pipeline...\n", len(cfg.TCPAddrs))
		clientCounter := 1
		for _, addr := range cfg.TCPAddrs {
			clientAddr := common.BigToAddress(big.NewInt(int64(clientCounter)))
			clientCounter++
			c, err := newRawTCPClient(addr, clientAddr)
			if err != nil {
				fmt.Printf("⚠️ TCP connect failed to %s: %v\n", addr, err)
				continue
			}
			tcpClients = append(tcpClients, c)
		}
		defer func() {
			for _, c := range tcpClients {
				c.Close()
			}
		}()
		fmt.Printf("✅ %d TCP connections connected.\n", len(tcpClients))
	}

	// Step 3: Sustained Injection Loop
	fmt.Printf("\n🔥 BLASTING CONTINUOUSLY FOR %d SECONDS...\n", cfg.DurationSec)
	tBlast0 := time.Now()
	deadline := tBlast0.Add(time.Duration(cfg.DurationSec) * time.Second)

	var totalSubmitted atomic.Uint64
	type SampleTx struct {
		Hash   common.Hash
		SentAt time.Time
	}
	var sampleMu sync.Mutex
	var sampleTxs []SampleTx

	signer := types.LatestSignerForChainID(big.NewInt(cfg.ChainID))
	targetAddr := common.HexToAddress("0x000000000000000000000000000000000000dEaD")

	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 500
	}

	var maxRecordedCPU float64
	var maxRecordedRSS uint64
	var resMu sync.Mutex

	sampleTicker := time.NewTicker(3 * time.Second)
	defer sampleTicker.Stop()
	go func() {
		for range sampleTicker.C {
			if time.Now().After(deadline) {
				return
			}
			out, err := exec.Command("ps", "-o", "%cpu,rss", "--no-headers", "-C", "simple_chain").Output()
			if err == nil {
				lines := strings.Split(string(out), "\n")
				var curTotalCPU float64
				var curTotalRSS uint64
				for _, line := range lines {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						var c float64
						var r uint64
						fmt.Sscanf(fields[0], "%f", &c)
						fmt.Sscanf(fields[1], "%d", &r)
						curTotalCPU += c
						curTotalRSS += r
					}
				}
				resMu.Lock()
				if curTotalCPU > maxRecordedCPU {
					maxRecordedCPU = curTotalCPU
				}
				if curTotalRSS > maxRecordedRSS {
					maxRecordedRSS = curTotalRSS
				}
				resMu.Unlock()
			}
		}
	}()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			if time.Now().After(deadline) {
				return
			}
			sub := totalSubmitted.Load()
			el := time.Since(tBlast0)
			resMu.Lock()
			cpuSnap := maxRecordedCPU
			rssSnap := maxRecordedRSS / 1024
			resMu.Unlock()
			fmt.Printf("   ⏳ [PROGRESS %s / %ds] Injected %d txs (Current Injection TPS: %.2f tx/s | Peak CPU: %.1f%% | Peak RSS: %d MB)\n",
				el.Truncate(time.Second), cfg.DurationSec, sub, float64(sub)/el.Seconds(), cpuSnap, rssSnap)
		}
	}()

	var blastWg sync.WaitGroup
	numWorkers := 16
	if len(tcpClients) > 0 && len(tcpClients) < numWorkers {
		numWorkers = len(tcpClients) * 4
	}

	var chunkInterval time.Duration
	if cfg.RateLimit > 0 && batchSize > 0 {
		chunkInterval = time.Duration(float64(numWorkers) * float64(batchSize) / float64(cfg.RateLimit) * float64(time.Second))
		fmt.Printf("⏱️ Throttling injection: Target %d tx/s across %d workers (~%v per batch)\n",
			cfg.RateLimit, numWorkers, chunkInterval)
	}

	for w := 0; w < numWorkers; w++ {
		blastWg.Add(1)
		go func(workerID int) {
			defer blastWg.Done()

			workerAccs := make([]*SustainedAccount, 0)
			for i := workerID; i < len(activeAccs); i += numWorkers {
				workerAccs = append(workerAccs, activeAccs[i])
			}
			if len(workerAccs) == 0 {
				return
			}

			accIdx := 0
			for time.Now().Before(deadline) {
				chunkStart := time.Now()
				txsChunk := make([]*types.Transaction, 0, batchSize)
				for len(txsChunk) < batchSize && time.Now().Before(deadline) {
					acc := workerAccs[accIdx%len(workerAccs)]
					accIdx++

					txNonce := acc.Nonce
					acc.Nonce++

					var tx *types.Transaction
					var err error
					if cfg.TxType == "legacy" {
						tx, err = types.SignNewTx(acc.Key, signer, &types.LegacyTx{
							Nonce:    txNonce,
							GasPrice: big.NewInt(cfg.GasPrice),
							Gas:      21000,
							To:       &targetAddr,
							Value:    big.NewInt(100),
						})
					} else {
						tx, err = types.SignNewTx(acc.Key, signer, &types.DynamicFeeTx{
							ChainID:   big.NewInt(cfg.ChainID),
							Nonce:     txNonce,
							GasTipCap: big.NewInt(0),
							GasFeeCap: big.NewInt(cfg.GasPrice),
							Gas:       21000,
							To:        &targetAddr,
							Value:     big.NewInt(100),
						})
					}
					if err == nil && tx != nil {
						txsChunk = append(txsChunk, tx)
					}
				}

				if len(txsChunk) == 0 {
					break
				}

				sampleMu.Lock()
				if len(sampleTxs) < 100 {
					sampleTxs = append(sampleTxs, SampleTx{
						Hash:   txsChunk[0].Hash(),
						SentAt: time.Now(),
					})
				}
				sampleMu.Unlock()

				if (cfg.Mode == "tcp" || cfg.Mode == "both") && len(tcpClients) > 0 {
					client := tcpClients[workerID%len(tcpClients)]
					if err := client.SendBatch(txsChunk); err != nil {
						time.Sleep(10 * time.Millisecond)
					} else {
						totalSubmitted.Add(uint64(len(txsChunk)))
					}
				} else {
					for _, tx := range txsChunk {
						raw, err := tx.MarshalBinary()
						if err == nil {
							rpcURL := cfg.RPCUrls[workerID%len(cfg.RPCUrls)]
							_, err = rpcCallTo(rpcURL, "eth_sendRawTransaction", fmt.Sprintf("0x%x", raw))
							if err == nil {
								totalSubmitted.Add(1)
							}
						}
					}
				}

				if chunkInterval > 0 {
					chunkElapsed := time.Since(chunkStart)
					if chunkElapsed < chunkInterval {
						time.Sleep(chunkInterval - chunkElapsed)
					}
				}
			}
		}(w)
	}

	blastWg.Wait()
	injectDur := time.Since(tBlast0)
	subTxs := int(totalSubmitted.Load())
	injectTPS := float64(subTxs) / injectDur.Seconds()
	fmt.Printf("\n⚡ Injected total %d TXs over %v (Injection TPS: %.2f tx/s)\n", subTxs, injectDur, injectTPS)

	// Step 4: Consensus stabilization & Receipt Confirmation
	fmt.Printf("\n⏱️ Waiting 10 seconds for consensus commit & tracking receipt latencies...\n")
	time.Sleep(10 * time.Second)

	var confirmedCount atomic.Uint64
	var revertedCount atomic.Uint64
	var latencies []time.Duration
	var latMu sync.Mutex

	var trackWg sync.WaitGroup
	trackWorkers := make(chan struct{}, 32)
	timeout := 30 * time.Second

	for i, s := range sampleTxs {
		trackWg.Add(1)
		trackWorkers <- struct{}{}
		go func(st SampleTx, sIdx int) {
			defer trackWg.Done()
			defer func() { <-trackWorkers }()

			start := time.Now()
			deadline := start.Add(timeout)
			rpcURL := cfg.RPCUrls[sIdx%len(cfg.RPCUrls)]

			for time.Now().Before(deadline) {
				res, err := rpcCallTo(rpcURL, "eth_getTransactionReceipt", st.Hash.Hex())
				if err == nil && len(res) > 0 && string(res) != "null" {
					var rec struct {
						Status string `json:"status"`
					}
					json.Unmarshal(res, &rec)
					var status uint64
					fmt.Sscanf(rec.Status, "0x%x", &status)

					elapsed := time.Since(st.SentAt)
					latMu.Lock()
					latencies = append(latencies, elapsed)
					latMu.Unlock()

					if status == 1 {
						confirmedCount.Add(1)
					} else {
						revertedCount.Add(1)
					}
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}(s, i)
	}
	trackWg.Wait()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var p50, p90, p95, p99, avgLat time.Duration
	if len(latencies) > 0 {
		p50 = latencies[len(latencies)*50/100]
		p90 = latencies[len(latencies)*90/100]
		p95 = latencies[len(latencies)*95/100]
		p99 = latencies[len(latencies)*99/100]
		var totalLat time.Duration
		for _, l := range latencies {
			totalLat += l
		}
		avgLat = totalLat / time.Duration(len(latencies))
	}

	endBlock, _ := getBlockNumber(cfg.RPCUrls[0])
	blocksProduced := uint64(0)
	if endBlock >= startBlock {
		blocksProduced = endBlock - startBlock
	}

	var totalOnChainTxs uint64
	for b := startBlock + 1; b <= endBlock; b++ {
		res, err := rpcCallTo(cfg.RPCUrls[0], "eth_getBlockByNumber", fmt.Sprintf("0x%x", b), false)
		if err == nil && len(res) > 0 && string(res) != "null" {
			var bObj struct {
				Transactions []interface{} `json:"transactions"`
			}
			if err := json.Unmarshal(res, &bObj); err == nil {
				totalOnChainTxs += uint64(len(bObj.Transactions))
			}
		}
	}

	effectiveTPS := float64(totalOnChainTxs) / injectDur.Seconds()

	fmt.Printf("\n📊 SUSTAINED BENCHMARK METRICS:\n")
	fmt.Printf("   • Duration:           %v (Target: %ds)\n", injectDur, cfg.DurationSec)
	fmt.Printf("   • Total Submitted:    %d txs\n", subTxs)
	fmt.Printf("   • Total Confirmed:    %d txs on-chain across %d blocks\n", totalOnChainTxs, blocksProduced)
	fmt.Printf("   • Injection Speed:    %.2f tx/s\n", injectTPS)
	fmt.Printf("   • Sustained TPS:      %.2f tx/s\n", effectiveTPS)
	fmt.Printf("   • Latency P50:        %v\n", p50)
	fmt.Printf("   • Latency P90:        %v\n", p90)
	fmt.Printf("   • Latency P95:        %v\n", p95)
	fmt.Printf("   • Latency P99:        %v\n", p99)
	fmt.Printf("   • Latency Avg:        %v\n", avgLat)
	fmt.Printf("   • Sample Verified:    %d/%d (Confirmed: %d, Reverted: %d)\n",
		confirmedCount.Load()+revertedCount.Load(), len(sampleTxs), confirmedCount.Load(), revertedCount.Load())
	fmt.Printf("   • Blocks Produced:    %d (#%d -> #%d)\n", blocksProduced, startBlock, endBlock)
	resMu.Lock()
	finalCPU := maxRecordedCPU
	finalRSS := int(maxRecordedRSS / 1024)
	resMu.Unlock()
	fmt.Printf("   • Peak CPU (Nodes):   %.1f%%\n", finalCPU)
	fmt.Printf("   • Peak RSS (Nodes):   %d MB\n", finalRSS)

	// Step 5: Zero-Fork Invariant Verification
	zeroForkOk := true
	if cfg.VerifyParity && len(cfg.RPCUrls) > 1 {
		fmt.Printf("\n🛡️ VERIFYING ZERO-FORK INVARIANT ACROSS %d NODES...\n", len(cfg.RPCUrls))
		targetBlockNum := fmt.Sprintf("0x%x", endBlock)
		var blockHashes = make(map[string][]string)
		var stateRoots = make(map[string][]string)

		for _, u := range cfg.RPCUrls {
			res, err := rpcCallTo(u, "eth_getBlockByNumber", targetBlockNum, false)
			if err != nil || len(res) == 0 || string(res) == "null" {
				fmt.Printf("   ⚠️ Node %s did not return block %s\n", u, targetBlockNum)
				continue
			}
			var b struct {
				Hash      string        `json:"hash"`
				StateRoot string        `json:"stateRoot"`
				Txs       []interface{} `json:"transactions"`
			}
			if err := json.Unmarshal(res, &b); err == nil && b.Hash != "" {
				blockHashes[b.Hash] = append(blockHashes[b.Hash], u)
				stateRoots[b.StateRoot] = append(stateRoots[b.StateRoot], u)
				fmt.Printf("   • Node %s: Block %s | Hash: %s... | Root: %s... | Txs: %d\n",
					u, targetBlockNum, b.Hash[:12], b.StateRoot[:12], len(b.Txs))
			}
		}

		if len(blockHashes) > 1 || len(stateRoots) > 1 {
			zeroForkOk = false
			fmt.Printf("🚨 [CRITICAL FORK DETECTED] Multiple block hashes or state roots found across nodes!\n")
		} else {
			fmt.Println("✅ [ZERO-FORK VERIFIED] 100% agreement across all online nodes on Block Hash & State Root!")
		}
	}

	result := BenchmarkResult{
		Timestamp:           time.Now().UTC().Format(time.RFC3339),
		ChainID:             cfg.ChainID,
		Mode:                cfg.Mode,
		TxType:              cfg.TxType,
		TotalSubmitted:      subTxs,
		TotalConfirmed:      int(totalOnChainTxs),
		TotalReverted:       int(revertedCount.Load()),
		TotalDropped:        subTxs - int(totalOnChainTxs),
		InjectionDuration:   injectDur.String(),
		InjectionTPS:        injectTPS,
		CommitDuration:      injectDur.String(),
		EffectiveTPS:        effectiveTPS,
		LatencyP50:          p50,
		LatencyP90:          p90,
		LatencyP95:          p95,
		LatencyP99:          p99,
		LatencyAvg:          avgLat,
		StartBlock:          startBlock,
		EndBlock:            endBlock,
		BlocksProduced:      blocksProduced,
		ZeroForkVerified:    zeroForkOk,
		NodeRootsConsistent: zeroForkOk,
		MaxCPU:              fmt.Sprintf("%.1f%%", finalCPU),
		MaxRSSMB:            finalRSS,
	}

	if cfg.ReportFile != "" {
		if data, err := json.MarshalIndent(result, "", "  "); err == nil {
			_ = os.WriteFile(cfg.ReportFile, data, 0644)
			fmt.Printf("📄 JSON Benchmark report saved to: %s\n", cfg.ReportFile)
		}
	}
	fmt.Println("==================================================================")
}
