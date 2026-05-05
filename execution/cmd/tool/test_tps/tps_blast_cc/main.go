//go:build ignore

package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bufio"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/tool/tps_blast/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// AccountInfo from generated_keys.json
type AccountInfo struct {
	Index      int    `json:"index"`
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
}

// rawWriter wraps a raw TCP connection (same as tps_blast)
type rawWriter struct {
	conn         net.Conn
	writer       *bufio.Writer
	addr         string
	version      string
	toAddrHex    string
	rpcPool      []*rpc.RPCClient // injected for nonce divergence check
	nonceChecker func(addrHex string) // callback khi invalid nonce xảy ra
}

func newRawWriter(addr, version, toAddrHex string) (*rawWriter, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	rw := &rawWriter{
		conn:      conn,
		writer:    bufio.NewWriterSize(conn, 4*1024*1024),
		addr:      addr,
		version:   version,
		toAddrHex: toAddrHex,
	}

	go func() {
		reader := bufio.NewReader(conn)
		for {
			lengthBuf := make([]byte, 8)
			if _, err := io.ReadFull(reader, lengthBuf); err != nil {
				return
			}
			msgLen := binary.LittleEndian.Uint64(lengthBuf)
			if msgLen > 10*1024*1024 {
				return
			}
			msgBuf := make([]byte, msgLen)
			if _, err := io.ReadFull(reader, msgBuf); err != nil {
				return
			}
			var msg pb.Message
			if err := proto.Unmarshal(msgBuf, &msg); err == nil && msg.Header != nil {
				if msg.Header.Command == "TransactionError" {
					var txErr pb.TransactionHashWithError
					if proto.Unmarshal(msg.Body, &txErr) == nil {
						txHashHex := common.BytesToHash(txErr.Hash).Hex()
						fmt.Printf("\n❌ SERVER REJECTED TX: %s | Node: %s | Code: %d | Msg: %s\n",
							txHashHex, rw.addr, txErr.Code, txErr.Description)
						// Nếu lỗi invalid nonce → trigger cross-check nonce divergence
						if strings.Contains(strings.ToLower(txErr.Description), "invalid nonce") {
							if rw.nonceChecker != nil {
								// Không block goroutine đọc — chạy check bất đồng bộ
								go rw.nonceChecker(txHashHex)
							}
						}
					}
				} else if msg.Header.Command != "Receipt" {
					fmt.Printf("\n📩 SERVER: %s\n", msg.Header.Command)
				}
			}
		}
	}()

	return rw, nil
}

func (rw *rawWriter) sendRaw(cmd string, body []byte) error {
	toAddr := common.HexToAddress(rw.toAddrHex)
	msgProto := &pb.Message{
		Header: &pb.Header{
			Command:   cmd,
			Version:   rw.version,
			ToAddress: toAddr.Bytes(),
			ID:        uuid.New().String(),
		},
		Body: body,
	}
	b, err := proto.Marshal(msgProto)
	if err != nil {
		return err
	}
	lengthBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(lengthBuf, uint64(len(b)))
	rw.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := rw.writer.Write(lengthBuf); err != nil {
		return err
	}
	if _, err := rw.writer.Write(b); err != nil {
		return err
	}
	return nil
}

func (rw *rawWriter) flush() error { return rw.writer.Flush() }
func (rw *rawWriter) close() {
	if rw.conn != nil {
		rw.conn.Close()
	}
}

// checkNonceDivergence fetch nonce của 1 sample address từ tất cả RPC nodes
// để phát hiện node nào đang bị lệch state. Chỉ gọi khi xảy ra invalid nonce.
func checkNonceDivergence(rpcPool []*rpc.RPCClient, sampleAddr string, triggerInfo string) {
	if len(rpcPool) < 2 {
		// Chỉ 1 node → không có gì để so sánh
		return
	}

	type nodeNonce struct {
		endpoint string
		nonce    int64
		err      error
	}

	results := make([]nodeNonce, len(rpcPool))
	var wg sync.WaitGroup
	for i, rc := range rpcPool {
		wg.Add(1)
		go func(i int, rc *rpc.RPCClient) {
			defer wg.Done()
			as, err := rc.GetAccountState(sampleAddr)
			if err != nil {
				results[i] = nodeNonce{endpoint: rc.Endpoint, nonce: -1, err: err}
				return
			}
			if as == nil {
				results[i] = nodeNonce{endpoint: rc.Endpoint, nonce: -1, err: fmt.Errorf("nil state")}
				return
			}
			results[i] = nodeNonce{endpoint: rc.Endpoint, nonce: int64(as.Nonce)}
		}(i, rc)
	}
	wg.Wait()

	// Tìm nonce chuẩn (majority voting)
	nonceCount := make(map[int64]int)
	for _, r := range results {
		if r.err == nil {
			nonceCount[r.nonce]++
		}
	}
	majorityNonce := int64(-1)
	maxVotes := 0
	for n, cnt := range nonceCount {
		if cnt > maxVotes {
			maxVotes = cnt
			majorityNonce = n
		}
	}

	// In bảng so sánh
	fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  🔍 NONCE DIVERGENCE CHECK (triggered by: invalid nonce)\n")
	fmt.Printf("║  📋 Sample addr: %s\n", sampleAddr)
	fmt.Printf("║  ℹ️  Trigger: %s\n", triggerInfo)
	fmt.Printf("╠══════════════════════════════════════════════════════════════╣\n")

	hasDivergence := false
	for i, r := range results {
		if r.err != nil {
			fmt.Printf("║  Node[%d] %-35s  nonce=ERROR (%v)\n", i, r.endpoint, r.err)
			continue
		}
		status := "✅ OK"
		if r.nonce != majorityNonce {
			status = fmt.Sprintf("⚠️  LỆCH! (majority=%d)", majorityNonce)
			hasDivergence = true
		}
		fmt.Printf("║  Node[%d] %-35s  nonce=%-6d  %s\n", i, r.endpoint, r.nonce, status)
	}

	if hasDivergence {
		fmt.Printf("╠══════════════════════════════════════════════════════════════╣\n")
		fmt.Printf("║  ⚠️  PHÁT HIỆN LỆCH NONCE GIỮA CÁC NODE! Majority nonce=%d\n", majorityNonce)
		fmt.Printf("║     → Nguyên nhân: sub-node bị replication lag hoặc chết.\n")
		fmt.Printf("║     → Kiểm tra logs: /consensus/metanode/logs/node_*/\n")
	} else {
		fmt.Printf("║  ✅ Tất cả node đồng thuận nonce=%d. Có thể do race condition.\n", majorityNonce)
	}
	fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n")
}

func main() {
	var (
		configPath     string
		keysFile       string
		count          int
		batchSize      int
		sleepMs        int
		nodeAddr       string
		rpcAddr        string
		waitSecs       int
		recipient      string
		destId         int
		amountWei      string
		numRounds      int
		parallelNative bool
		loadBalance    bool
		verify         bool
	)

	flag.StringVar(&configPath, "config", "./config.json", "Client config")
	flag.StringVar(&keysFile, "keys", "../gen_spam_keys/generated_keys.json", "Generated keys JSON")
	flag.IntVar(&count, "count", 10000, "Number of lockAndBridge TXs")
	flag.IntVar(&batchSize, "batch", 500, "Batch size")
	flag.IntVar(&sleepMs, "sleep", 10, "Sleep between batches (ms)")
	flag.StringVar(&nodeAddr, "node", "", "Override node TCP address")
	flag.StringVar(&rpcAddr, "rpc", "", "RPC URL for verification")
	flag.IntVar(&waitSecs, "wait", 120, "Max seconds to wait for chain processing")
	flag.StringVar(&recipient, "recipient", "0xbF2b4B9b9dFB6d23F7F0FC46981c2eC89f94A9F2", "Recipient address")
	flag.IntVar(&destId, "dest", 2, "Destination chain ID")
	flag.StringVar(&amountWei, "amount", "1000000000000000000", "Amount in wei (default: 1 ETH)")
	flag.IntVar(&numRounds, "rounds", 1, "Number of benchmark rounds")
	flag.BoolVar(&parallelNative, "parallel_native", false, "Use native self-transfers for parallel execution benchmarking")
	flag.BoolVar(&loadBalance, "load_balance", false, "Round-robin transactions across all connection_node_* in config")
	flag.BoolVar(&verify, "verify", false, "After each round, check recipient balance to confirm TXs landed")
	flag.Parse()

	logger.SetConfig(&logger.LoggerConfig{Flag: 0})

	fmt.Println("═══════════════════════════════════════════════════")
	if parallelNative {
		fmt.Println("  🔥 TPS BLAST — Parallel Native Self-Transfers")
	} else {
		fmt.Println("  🔥 TPS BLAST — lockAndBridge Cross-Chain")
	}
	fmt.Println("═══════════════════════════════════════════════════")

	// Load config
	configIface, err := c_config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}
	config := configIface.(*c_config.ClientConfig)

	var pKey p_common.PrivateKey
	copy(pKey[:], config.PrivateKey())

	chainId := config.ChainId

	// Load keys
	keysData, err := os.ReadFile(keysFile)
	if err != nil {
		log.Fatalf("Cannot read keys file %s: %v", keysFile, err)
	}
	var accounts []AccountInfo
	if err := json.Unmarshal(keysData, &accounts); err != nil {
		log.Fatalf("Cannot parse keys file: %v", err)
	}
	fmt.Printf("  📋 Loaded %d accounts from %s\n", len(accounts), keysFile)

	if count > len(accounts) {
		count = len(accounts)
	}
	toSend := accounts[:count]

	fmt.Printf("  📊 TXs to send: %d\n", len(toSend))
	fmt.Printf("  📍 Recipient: %s\n", recipient)
	fmt.Printf("  🆔 DestinationId: %d\n", destId)
	fmt.Printf("  💰 Amount: %s wei\n", amountWei)

	// ── Build RPC pool: rpc_1, rpc_2, rpc_3 từ config (load balance nonce fetch) ──
	// Đọc raw config để lấy rpc_1/rpc_2/rpc_3
	var rpcPool []*rpc.RPCClient
	if raw, err := os.ReadFile(configPath); err == nil {
		var rawCfg map[string]interface{}
		if json.Unmarshal(raw, &rawCfg) == nil {
			// Thêm rpc_1, rpc_2, rpc_3, ... theo thứ tự
			for i := 1; i <= 10; i++ {
				// Nếu load_balance = false, chỉ sử dụng rpc_1 (node hiện tại)
				if !loadBalance && i > 1 {
					break
				}

				key := fmt.Sprintf("rpc_%d", i)
				if v, ok := rawCfg[key].(string); ok && v != "" {
					url := v
					if !strings.HasPrefix(url, "http") {
						url = "http://" + url
					}
					rpcPool = append(rpcPool, rpc.NewRPCClient(url))

					if !loadBalance {
						fmt.Printf("  🌐 Chế độ Single Node IP (RPC): %s\n", url)
					} else {
						fmt.Printf("  🌐 RPC pool [%d]: %s\n", i, url)
					}
				}
			}
		}
	}

	// Fallback: nếu không có rpc_* trong config, dùng rpcAddr như cũ
	if len(rpcPool) == 0 {
		if rpcAddr == "" {
			targetAddr := config.ParentConnectionAddress
			if nodeAddr != "" {
				targetAddr = nodeAddr
			}
			configHost := targetAddr
			if idx := strings.LastIndex(configHost, ":"); idx >= 0 {
				configHost = configHost[:idx]
			}
			rpcAddr = configHost + ":8757"
		}
		rpcUrl := rpcAddr
		if !strings.HasPrefix(rpcUrl, "http") {
			rpcUrl = "http://" + rpcUrl
		}
		rpcPool = append(rpcPool, rpc.NewRPCClient(rpcUrl))
		fmt.Printf("  🌐 RPC pool [fallback]: %s\n", rpcUrl)
	}

	// rpcClient dùng cho block polling (luôn dùng pool[0])
	rpcClient := rpcPool[0]

	// Round-robin counter cho nonce fetching
	var rpcPoolIdx int64

	// fetchNonce: luôn dùng rcPool[0] (tức là node master/local) để lấy nonce
	// Việc dùng round-robin rpcPool (poolSize > 1) sẽ gây ra lỗi "invalid nonce" vì
	// các sub-node thường bị lag 1 chút (replication lag). Khi lấy state từ sub-node bị lag,
	// nonce trả về sẽ là nonce cũ.
	fetchNonce := func(addr string) (uint64, error) {
		poolSize := len(rpcPool)
		if !parallelNative {
			poolSize = 1
		}
		idx := atomic.AddInt64(&rpcPoolIdx, 1) % int64(poolSize)
		rc := rpcPool[idx]

		as, err := rc.GetAccountState(addr)
		if err != nil {
			return 0, err
		}
		if as == nil {
			return 0, fmt.Errorf("node[%d] returned nil state", idx)
		}

		// Log 5 cái đầu để debug
		count := atomic.LoadInt64(&rpcPoolIdx)
		if count <= 5 {
			fmt.Printf("      DEBUG: Account %s => Nonce %d (from %s)\n", addr, as.Nonce, rc.Endpoint)
		}

		return uint64(as.Nonce), nil
	}

	// Fetch nonce for ALL accounts concurrently (load-balanced conditional)
	if parallelNative {
		fmt.Printf("  🔍 Fetching nonces for %d accounts (pool size: %d RPC nodes)...\n", len(toSend), len(rpcPool))
	} else {
		fmt.Printf("  🔍 Fetching nonces for %d accounts (using single RPC: %s)...\n", len(toSend), rpcPool[0].Endpoint)
	}
	nonceMap := make(map[string]uint64) // address -> nonce
	var nonceMu sync.Mutex
	var nonceWg sync.WaitGroup
	nonceCh := make(chan int, len(toSend))
	nonceWorkers := 50
	var nonceFetched int64
	var nonceErrors int64

	for w := 0; w < nonceWorkers; w++ {
		nonceWg.Add(1)
		go func() {
			defer nonceWg.Done()
			for idx := range nonceCh {
				acc := toSend[idx]
				nonce, err := fetchNonce(acc.Address)
				if err == nil {
					nonceMu.Lock()
					nonceMap[acc.Address] = nonce
					nonceMu.Unlock()
					atomic.AddInt64(&nonceFetched, 1)
				} else {
					logger.Info("Error fetching nonce for account %s: %v", acc.Address, err)
					atomic.AddInt64(&nonceErrors, 1)
				}
				done := atomic.LoadInt64(&nonceFetched) + atomic.LoadInt64(&nonceErrors)
				if done%100 == 0 || done == int64(len(toSend)) {
					fmt.Printf("\r    ⏳ Progress: %d/%d nonces fetched (errors: %d)... ", done, len(toSend), atomic.LoadInt64(&nonceErrors))
				}
			}
		}()
	}
	for i := range toSend {
		nonceCh <- i
	}
	close(nonceCh)
	nonceWg.Wait()
	fmt.Printf("\n  ✅ Nonces fetched: %d ok, %d errors\n", nonceFetched, nonceErrors)

	// Build lockAndBridge ABI
	addressType, _ := abi.NewType("address", "", nil)
	uint256Type, _ := abi.NewType("uint256", "", nil)
	lockMethod := abi.NewMethod("lockAndBridge", "lockAndBridge", abi.Function, "", false, true,
		abi.Arguments{
			{Name: "recipient", Type: addressType},
			{Name: "destinationId", Type: uint256Type},
		},
		abi.Arguments{},
	)

	destBig := big.NewInt(int64(destId))
	ccContract := common.HexToAddress("0x00000000000000000000000000000000B429C0B2")
	amount, _ := new(big.Int).SetString(amountWei, 10)

	// Pre-build all TXs
	txTypeName := "lockAndBridge"
	if parallelNative {
		txTypeName = "Native parallel"
	}
	fmt.Printf("\n📦 Pre-building %d %s TXs...\n", len(toSend), txTypeName)
	buildStart := time.Now()

	type rawTx struct {
		bytes  []byte
		addr   string
		txHash common.Hash
		target common.Address
		amount *big.Int
	}
	var allTxs []rawTx
	var buildErrors int
	for i, acc := range toSend {
		privKeyBytes, err := hex.DecodeString(acc.PrivateKey)
		if err != nil {
			buildErrors++
			continue
		}
		ecdsaKey, err := crypto.ToECDSA(privKeyBytes)
		if err != nil {
			buildErrors++
			continue
		}
		fromAddr := crypto.PubkeyToAddress(ecdsaKey.PublicKey)

		var targetContract common.Address
		var bCallData []byte

		if parallelNative {
			// Generate a unique dummy address so each sender sends to an untouched recipient
			// This makes verification perfectly isolated and guarantees the balance must equal txAmount.
			dummyKey, _ := crypto.GenerateKey()
			targetContract = crypto.PubkeyToAddress(dummyKey.PublicKey)
			bCallData = []byte{}
		} else {
			// lockAndBridge Cross-Chain
			counterpartIdx := (i + 1) % len(toSend)
			if len(toSend) == 1 {
				counterpartIdx = 0
			}
			counterpartAcc := toSend[counterpartIdx]
			counterpartAddr := common.HexToAddress(counterpartAcc.Address)

			targetContract = ccContract
			// Pack calldata: lockAndBridge(counterpartAddr, destinationId)
			inputData, err := lockMethod.Inputs.Pack(counterpartAddr, destBig)
			if err != nil {
				buildErrors++
				continue
			}
			callData := append(lockMethod.ID, inputData...)

			callDataObj := transaction.NewCallData(callData)
			bCallData, err = callDataObj.Marshal()
			if err != nil {
				buildErrors++
				continue
			}
		}

		// Get nonce for this account
		nonce, ok := nonceMap[acc.Address]
		if !ok {
			buildErrors++
			continue
		}

		txAmount := amount

		internalTx := transaction.NewTransaction(
			fromAddr,
			targetContract,
			txAmount,
			1000000, // maxGas
			1000000, // maxGasPrice
			0,       // maxPriorityFee
			bCallData,
			[][]byte{},
			common.Hash{},
			common.Hash{},
			nonce,
			chainId,
		)

		// Sign with BLS key
		internalTx.SetSign(pKey)

		bTx, err := internalTx.Marshal()
		if err != nil {
			buildErrors++
			continue
		}

		allTxs = append(allTxs, rawTx{
			bytes:  bTx,
			addr:   acc.Address,
			txHash: internalTx.Hash(),
			target: targetContract,
			amount: txAmount,
		})
	}

	buildDuration := time.Since(buildStart)
	fmt.Printf("  ✅ Built %d TXs in %s (%.0f tx/s), %d errors\n",
		len(allTxs), buildDuration.Round(time.Millisecond),
		float64(len(allTxs))/buildDuration.Seconds(), buildErrors)

	targetAddresses := []string{config.ParentConnectionAddress}
	if !loadBalance {
		fmt.Printf("\n  📡 Chế độ Single Node IP (TCP): %s\n", config.ParentConnectionAddress)
	}

	if nodeAddr != "" {
		targetAddresses = strings.Split(nodeAddr, ",")
	} else if loadBalance {
		// Read raw config for extra load-balancer nodes only if load_balance flag is true
		if raw, err := os.ReadFile(configPath); err == nil {
			var rawCfg map[string]interface{}
			if err := json.Unmarshal(raw, &rawCfg); err == nil {
				for k, v := range rawCfg {
					if strings.HasPrefix(k, "connection_node_") {
						if strV, ok := v.(string); ok {
							targetAddresses = append(targetAddresses, strV)
						}
					}
				}
			}
		}
	}

	toAddrHex := config.ParentAddress
	version := config.Version()

	randomPrivKey, _ := crypto.GenerateKey()
	clientAddress := crypto.PubkeyToAddress(randomPrivKey.PublicKey)
	_ = bls.NewKeyPair(config.PrivateKey()) // keep import

	// Throttle: chỉ check divergence tối đa 1 lần mỗi 5 giây
	var lastNonceCheckMs atomic.Int64

	// nonceCheckerFn được inject vào mỗi rawWriter.
	// triggerInfo = txHashHex của TX bị reject (để log)
	nonceCheckerFn := func(triggerInfo string) {
		if len(rpcPool) < 2 || len(toSend) == 0 {
			return
		}
		nowMs := time.Now().UnixMilli()
		last := lastNonceCheckMs.Load()
		// throttle 5000ms
		if nowMs-last < 5000 {
			return
		}
		if !lastNonceCheckMs.CompareAndSwap(last, nowMs) {
			return // goroutine khác đã giằng lấy quyền check
		}
		// lấy sample = addr đầu tiên trong toSend
		sampleAddr := toSend[0].Address
		checkNonceDivergence(rpcPool, sampleAddr, triggerInfo)
	}

	reconnectNode := func(targetAddr string) *rawWriter {
		for attempt := 1; attempt <= 20; attempt++ {
			fmt.Printf("  🔌 Connecting to %s (attempt %d)...\n", targetAddr, attempt)
			rw, err := newRawWriter(targetAddr, version, toAddrHex)
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			// Inject rpcPool + nonceChecker vào rawWriter
			rw.rpcPool = rpcPool
			rw.nonceChecker = nonceCheckerFn
			initMsg := &pb.InitConnection{
				Address: clientAddress.Bytes(),
				Type:    config.NodeType(),
				Replace: true,
			}
			initBody, _ := proto.Marshal(initMsg)
			if err := rw.sendRaw(command.InitConnection, initBody); err != nil {
				rw.close()
				time.Sleep(500 * time.Millisecond)
				continue
			}
			if err := rw.flush(); err != nil {
				rw.close()
				time.Sleep(500 * time.Millisecond)
				continue
			}
			fmt.Printf("  ✅ Connected to %s and InitConnection sent\n", targetAddr)
			return rw
		}
		fmt.Printf("  ❌ Failed to connect to %s after 20 attempts\n", targetAddr)
		return nil
	}

	type activeClient struct {
		addr string
		rw   *rawWriter
	}

	connectAll := func() []*activeClient {
		var clients []*activeClient
		for _, addr := range targetAddresses {
			if rw := reconnectNode(addr); rw != nil {
				clients = append(clients, &activeClient{addr: addr, rw: rw})
			}
		}
		if len(clients) == 0 {
			fmt.Println("  ❌ Failed to connect to any node.")
			os.Exit(1)
		}
		return clients
	}

	var allRoundTPS []float64

	type RoundSummary struct {
		Round         int     `json:"round"`
		StartBlock    uint64  `json:"startBlock"`
		EndBlock      uint64  `json:"endBlock"`
		BlockCount    int     `json:"blockCount"`
		TxCount       int     `json:"txCount"`
		MaxTxInBlock  int     `json:"maxTxInBlock"`
		TPS           float64 `json:"tps"`
		ProcessingSec float64 `json:"processingSec"`
	}
	var roundSummaries []RoundSummary

	clients := connectAll()
	defer func() {
		for _, c := range clients {
			if c.rw != nil {
				c.rw.close()
			}
		}
	}()

	for round := 1; round <= numRounds; round++ {
		if numRounds > 1 {
			fmt.Printf("\n╔═══════════════════════════════════════════════════╗\n")
			fmt.Printf("║  🔄 ROUND %d / %d\n", round, numRounds)
			fmt.Printf("╚═══════════════════════════════════════════════════╝\n")
		}

		// ── Re-fetch nonces + rebuild TXs for rounds > 1 ──
		if round > 1 {
			// Wait for chain to fully process previous round before re-fetching nonces
			fmt.Printf("  ⏳ Waiting 3s for chain to finalize previous round...\n")
			time.Sleep(3 * time.Second)
			fmt.Printf("  🔍 Re-fetching nonces for %d accounts (pool: %d nodes)...\n", len(toSend), len(rpcPool))
			nonceMap = make(map[string]uint64)
			var refetchOk, refetchErr int64
			var refetchMu sync.Mutex
			var refetchWg sync.WaitGroup
			refetchCh := make(chan int, len(toSend))
			for w := 0; w < 50; w++ {
				refetchWg.Add(1)
				go func() {
					defer refetchWg.Done()
					for idx := range refetchCh {
						acc := toSend[idx]
						nonce, err := fetchNonce(acc.Address)
						if err == nil {
							refetchMu.Lock()
							nonceMap[acc.Address] = nonce
							refetchMu.Unlock()
							atomic.AddInt64(&refetchOk, 1)
						} else {
							atomic.AddInt64(&refetchErr, 1)
						}
						done := atomic.LoadInt64(&refetchOk) + atomic.LoadInt64(&refetchErr)
						if done%2000 == 0 || done == int64(len(toSend)) {
							fmt.Printf("\r    Fetched %d/%d nonces (errors: %d)   ", done, len(toSend), atomic.LoadInt64(&refetchErr))
						}
					}
				}()
			}
			for i := range toSend {
				refetchCh <- i
			}
			close(refetchCh)
			refetchWg.Wait()
			fmt.Printf("\n  ✅ Nonces re-fetched: %d ok, %d errors\n", refetchOk, refetchErr)

			// Rebuild all TXs with new nonces
			fmt.Printf("\n📦 Re-building %d %s TXs...\n", len(toSend), txTypeName)
			rebuildStart := time.Now()
			allTxs = nil
			var rebuildErrors int
			for i, acc := range toSend {
				privKeyBytes, err := hex.DecodeString(acc.PrivateKey)
				if err != nil {
					rebuildErrors++
					continue
				}
				ecdsaKey, err := crypto.ToECDSA(privKeyBytes)
				if err != nil {
					rebuildErrors++
					continue
				}
				fromAddr := crypto.PubkeyToAddress(ecdsaKey.PublicKey)

				var targetContract common.Address
				var bCallData []byte

				if parallelNative {
					dummyKey, _ := crypto.GenerateKey()
					targetContract = crypto.PubkeyToAddress(dummyKey.PublicKey)
					bCallData = []byte{}
				} else {
					counterpartAcc := toSend[len(toSend)-1-i]
					counterpartAddr := common.HexToAddress(counterpartAcc.Address)

					targetContract = ccContract
					inputData, err := lockMethod.Inputs.Pack(counterpartAddr, destBig)
					if err != nil {
						rebuildErrors++
						continue
					}
					callData := append(lockMethod.ID, inputData...)
					callDataObj := transaction.NewCallData(callData)
					bCallData, err = callDataObj.Marshal()
					if err != nil {
						rebuildErrors++
						continue
					}
				}

				nonce, ok := nonceMap[acc.Address]
				if !ok {
					rebuildErrors++
					continue
				}

				txAmount := amount

				internalTx := transaction.NewTransaction(
					fromAddr, targetContract, txAmount,
					1000000, 1000000, 0,
					bCallData, [][]byte{},
					common.Hash{}, common.Hash{},
					nonce, chainId,
				)
				internalTx.SetSign(pKey)
				bTx, err := internalTx.Marshal()
				if err != nil {
					rebuildErrors++
					continue
				}
				allTxs = append(allTxs, rawTx{
					bytes:  bTx,
					addr:   acc.Address,
					txHash: internalTx.Hash(),
					target: targetContract,
					amount: txAmount,
				})
			}
			rebuildDuration := time.Since(rebuildStart)
			fmt.Printf("  ✅ Re-built %d TXs in %s (%.0f tx/s), %d errors\n",
				len(allTxs), rebuildDuration.Round(time.Millisecond),
				float64(len(allTxs))/rebuildDuration.Seconds(), rebuildErrors)
		}

		startBlock, _ := rpcClient.GetBlockNumber()
		fmt.Printf("\n  🏁 Starting block: %d\n", startBlock)

		// Batch and blast
		fmt.Printf("\n🔥 BLASTING %d %s TXs across %d nodes via SendTransactions...\n", len(allTxs), txTypeName, len(clients))
		fmt.Printf("   Batch size: %d, Sleep between batches: %dms\n", batchSize, sleepMs)

		var batchedMsgs [][]byte
		for i := 0; i < len(allTxs); i += batchSize {
			end := i + batchSize
			if end > len(allTxs) {
				end = len(allTxs)
			}
			var pbTxs []*pb.Transaction
			for j := i; j < end; j++ {
				txProto := &pb.Transaction{}
				if err := proto.Unmarshal(allTxs[j].bytes, txProto); err == nil {
					pbTxs = append(pbTxs, txProto)
				}
			}
			batchProto := &pb.Transactions{Transactions: pbTxs}
			batchBytes, err := proto.Marshal(batchProto)
			if err == nil {
				batchedMsgs = append(batchedMsgs, batchBytes)
			}
		}

		blastStart := time.Now()
		writeErrors := 0

		for i, batchBytes := range batchedMsgs {
			clientIdx := i % len(clients)
			c := clients[clientIdx]

			if c.rw == nil {
				c.rw = reconnectNode(c.addr)
				if c.rw == nil {
					fmt.Printf("\n  ❌ Skipping batch %d due to reconnect failure on %s\n", i, c.addr)
					continue
				}
			}

			err := c.rw.sendRaw(command.SendTransactions, batchBytes)
			if err != nil {
				writeErrors++
				fmt.Printf("\n  ⚠️  Write error on %s at Batch %d: %v — reconnecting...\n", c.addr, i, err)
				c.rw.close()
				c.rw = reconnectNode(c.addr)
				if c.rw != nil {
					c.rw.sendRaw(command.SendTransactions, batchBytes)
				} else {
					fmt.Printf("\n  ❌ Skipping batch %d due to reconnect failure on %s\n", i, c.addr)
					continue
				}
			}

			sentTxs := (i + 1) * batchSize
			if sentTxs > len(allTxs) {
				sentTxs = len(allTxs)
			}

			if (i+1)%10 == 0 || i == len(batchedMsgs)-1 {
				elapsed := time.Since(blastStart)
				rate := float64(sentTxs) / elapsed.Seconds()
				fmt.Printf("\r  📤 [%d/%d] %.0f tx/s | elapsed %s   ",
					sentTxs, len(allTxs), rate, elapsed.Round(time.Millisecond))
			}

			if c.rw != nil {
				c.rw.flush()
			}
			if i < len(batchedMsgs)-1 {
				time.Sleep(time.Duration(sleepMs) * time.Millisecond)
			}
		}

		for _, c := range clients {
			if c.rw != nil {
				c.rw.flush()
			}
		}

		blastDuration := time.Since(blastStart)
		injectionTPS := float64(len(allTxs)) / blastDuration.Seconds()
		fmt.Printf("\n\n  📤 Injected: %d TXs in %s\n", len(allTxs), blastDuration.Round(time.Millisecond))
		fmt.Printf("  🚀 Injection TPS: %.0f tx/s\n", injectionTPS)

		// Poll for completion
		maxWait := time.Duration(waitSecs) * time.Second
		pollInterval := 20 * time.Millisecond
		fmt.Printf("\n⏳ Polling chain for completion (max %s)...\n", maxWait)
		processStart := time.Now()

		var processingDuration time.Duration
		emptyBlockStreak := 0
		lastBlockNum := startBlock
		totalTxsInBlocks := uint64(0)
		seenAnyTx := false

		for time.Since(processStart) < maxWait {
			time.Sleep(pollInterval)

			currentBlockNum, err := rpcClient.GetBlockNumber()
			if err != nil {
				continue
			}

			newTxs := uint64(0)
			nextLastBlockNum := lastBlockNum
			for bn := lastBlockNum + 1; bn <= currentBlockNum; bn++ {
				blk, err := rpcClient.GetBlockByNumber(bn)
				if err == nil && blk != nil {
					newTxs += uint64(len(blk.Transactions))
					nextLastBlockNum = bn
				} else {
					// Stop the loop if fetching a block fails, to avoid skipping it permanently
					break
				}
			}

			totalTxsInBlocks += newTxs
			lastBlockNum = nextLastBlockNum

			if newTxs > 0 {
				seenAnyTx = true
			}

			pct := float64(totalTxsInBlocks) / float64(len(allTxs)) * 100
			if pct > 100 {
				pct = 100
			}

			fmt.Printf("\r  📡 [%s] Block: %d | TXs in blocks: %d/%d (%.0f%%) | +%d new   ",
				time.Since(processStart).Round(time.Millisecond),
				currentBlockNum, totalTxsInBlocks, len(allTxs), pct, newTxs)

			// Stop immediately when all TXs confirmed
			if totalTxsInBlocks >= uint64(len(allTxs)) {
				processingDuration = time.Since(processStart)
				fmt.Printf("\n  ✅ All %d/%d TXs confirmed in blocks\n", totalTxsInBlocks, len(allTxs))
				break
			}

			if newTxs == 0 && seenAnyTx {
				emptyBlockStreak++
				// With 10ms poll, need 6000 streaks = 60 seconds of no new blocks
				if emptyBlockStreak >= 6000 {
					processingDuration = time.Since(processStart)
					fmt.Printf("\n  ✅ Chain idle — %d/%d TXs in blocks (timeout after 60s)\n", totalTxsInBlocks, len(allTxs))
					break
				}
			} else {
				emptyBlockStreak = 0
			}
		}

		if processingDuration == 0 {
			processingDuration = time.Since(processStart)
		}

		if totalTxsInBlocks < uint64(len(allTxs)) {
			fmt.Printf("\n❌ ERROR: Not all transactions were processed! (%d/%d)\n", totalTxsInBlocks, len(allTxs))
			os.Exit(1)
		}

		endBlock, _ := rpcClient.GetBlockNumber()

		// Block statistics
		blockCount := 0
		maxTxInBlock := 0
		totalTxInBlocks := 0

		for b := startBlock + 1; b <= endBlock; b++ {
			blkInfo, err := rpcClient.GetBlockByNumber(b)
			if err == nil && blkInfo != nil {
				blockCount++
				txCount := len(blkInfo.Transactions)
				totalTxInBlocks += txCount
				if txCount > maxTxInBlock {
					maxTxInBlock = txCount
				}
			}
		}

		totalDuration := blastDuration + processingDuration
		processingTPS := float64(totalTxsInBlocks) / totalDuration.Seconds()
		allRoundTPS = append(allRoundTPS, processingTPS)

		roundSummaries = append(roundSummaries, RoundSummary{
			Round:         round,
			StartBlock:    startBlock,
			EndBlock:      endBlock,
			BlockCount:    blockCount,
			TxCount:       totalTxInBlocks,
			MaxTxInBlock:  maxTxInBlock,
			TPS:           processingTPS,
			ProcessingSec: totalDuration.Seconds(),
		})

		fmt.Printf("\n\n═══════════════════════════════════════════════════\n")
		fmt.Printf("  📊 ROUND %d RESULTS\n", round)
		fmt.Printf("═══════════════════════════════════════════════════\n")
		fmt.Printf("  📤 Total TXs sent:       %d\n", len(allTxs))
		fmt.Printf("  🚀 Injection TPS:        %.0f tx/s\n", injectionTPS)
		fmt.Printf("  ⏱️  Injection time:       %s\n", blastDuration.Round(time.Millisecond))
		fmt.Printf("  ─────────────────────────────────────────────────\n")
		fmt.Printf("  📥 TX in blocks:         %d\n", totalTxsInBlocks)
		fmt.Printf("  📊 End-to-End TPS:       ~%.0f tx/s\n", processingTPS)
		fmt.Printf("  ⏱️  End-to-End time:      %s\n", totalDuration.Round(time.Millisecond))
		fmt.Printf("  ─────────────────────────────────────────────────\n")
		fmt.Printf("  📦 BLOCK STATISTICS (Blocks %d to %d)\n", startBlock, endBlock)
		fmt.Printf("  🧊 Total Blocks:         %d\n", blockCount)
		fmt.Printf("  📥 Total TXs in blocks:  %d\n", totalTxInBlocks)
		fmt.Printf("  📈 Max TXs in a block:   %d\n", maxTxInBlock)
		if blockCount > 0 {
			fmt.Printf("  📉 Avg TXs per block:    %.1f\n", float64(totalTxInBlocks)/float64(blockCount))
		}

		// ── Verify: Kiểm tra cụ thể Balance và Receipt ──────────────────
		if verify {
			if parallelNative {
				fmt.Printf("\n  🔎 Chờ 2s để các node đồng bộ state...\n")
				time.Sleep(2 * time.Second)

				fmt.Printf("  🔎 Verifying 10000 transactions (Balance & Receipt)...\n")
				var verifiedCount int64
				var failedCount int64

				// BƯỚC 1: Quét nhanh Balance cho toàn bộ TX (Pass 1)
				var pass1Wg sync.WaitGroup
				pass1Ch := make(chan int, len(allTxs))

				// Hàng đợi cho những TX trượt bước 1
				var pass2Txs []int
				var pass2Mu sync.Mutex

				for w := 0; w < 100; w++ {
					pass1Wg.Add(1)
					go func() {
						defer pass1Wg.Done()
						for idx := range pass1Ch {
							tx := allTxs[idx]
							rc := rpcPool[idx%len(rpcPool)]

							as, err := rc.GetAccountState(tx.target.Hex())
							if err == nil && as != nil && as.Balance.Cmp(tx.amount) >= 0 {
								atomic.AddInt64(&verifiedCount, 1)
							} else {
								pass2Mu.Lock()
								pass2Txs = append(pass2Txs, idx)
								pass2Mu.Unlock()
							}

							done := atomic.LoadInt64(&verifiedCount) + int64(len(pass2Txs))
							if done%1000 == 0 || done == int64(len(allTxs)) {
								fmt.Printf("\r    [Pass 1] Checked Balance: %d/%d (Need Receipts: %d)   ", done, len(allTxs), len(pass2Txs))
							}
						}
					}()
				}

				for i := range allTxs {
					pass1Ch <- i
				}
				close(pass1Ch)
				pass1Wg.Wait()

				// BƯỚC 2: Nếu có TX chưa check được Balance, chờ 1 cục 5s rồi gặt mạng hỏi Receipt (Pass 2)
				if len(pass2Txs) > 0 {
					fmt.Printf("\n  ⏳ Mạng lag/Balance chưa lên, rớt lại %d TXs. Ngủ 5s trước khi quét Receipt...\n", len(pass2Txs))
					time.Sleep(5 * time.Second)

					var pass2Wg sync.WaitGroup
					pass2Ch := make(chan int, len(pass2Txs))

					for w := 0; w < 100; w++ {
						pass2Wg.Add(1)
						go func() {
							defer pass2Wg.Done()
							for idx := range pass2Ch {
								tx := allTxs[idx]
								rc := rpcPool[idx%len(rpcPool)]

								// Kiểm tra Balance lại một lần nữa sau khi đã ngủ 5s (chắc cú 100%)
								as, _ := rc.GetAccountState(tx.target.Hex())
								if as != nil && as.Balance.Cmp(tx.amount) >= 0 {
									// Tiền đã nổi sau 5s chờ
									atomic.AddInt64(&verifiedCount, 1)
								} else {
									// Nếu vẫn chưa up Balance (do RPC node bị delay cache), đành tin chuẩn xác vào Receipt
									receipt, rErr := rc.GetReceipt(tx.txHash.Hex())
									if rErr == nil && receipt != nil {
										status := ""
										if s, ok := receipt["status"].(string); ok {
											status = s
										} else if st, ok := receipt["Status"].(string); ok {
											status = st
										}

										if status != "" && status != "0x0" && status != "FAILED" { // Lọc bớt status thất bại nếu có
											atomic.AddInt64(&verifiedCount, 1)
										} else {
											atomic.AddInt64(&failedCount, 1)
										}
									} else {
										atomic.AddInt64(&failedCount, 1)
									}
								}

								done2 := atomic.LoadInt64(&verifiedCount) + atomic.LoadInt64(&failedCount)
								if done2%1000 == 0 || done2 == int64(len(allTxs)) {
									fmt.Printf("\r    [Pass 2] Fetching Receipts: %d/%d completed            ", done2, len(allTxs))
								}
							}
						}()
					}

					for _, idx := range pass2Txs {
						pass2Ch <- idx
					}
					close(pass2Ch)
					pass2Wg.Wait()
				}

				fmt.Printf("\n  ✅ Kết quả: %d TXs xác nhận OK, %d TXs Lỗi\n", verifiedCount, failedCount)

			} else {
				// lockAndBridge logic nguyên bản
				fmt.Printf("\n  🔎 Verifying recipient balance (lockAndBridge)...\n")
				fmt.Printf("  ℹ️  lockAndBridge: token lock nên balance recipient trên chain này không đổi\n")
			}
		}
	} // end round loop

	// ── Benchmark Summary ──────────────────────────────
	if numRounds > 1 {
		var minTPS, maxTPS, sumTPS float64
		minTPS = allRoundTPS[0]
		maxTPS = allRoundTPS[0]
		for _, t := range allRoundTPS {
			sumTPS += t
			if t < minTPS {
				minTPS = t
			}
			if t > maxTPS {
				maxTPS = t
			}
		}
		avgTPS := sumTPS / float64(len(allRoundTPS))

		fmt.Println("\n╔═══════════════════════════════════════════════════╗")
		fmt.Println("║  📊 BENCHMARK SUMMARY")
		fmt.Println("╠═══════════════════════════════════════════════════╣")
		fmt.Printf("║  🔄 Rounds         : %d\n", numRounds)
		fmt.Printf("║  📤 TXs per round  : %d\n", len(allTxs))
		fmt.Println("║  ─────────────────────────────────────────────────")
		for i, t := range allRoundTPS {
			fmt.Printf("║  Round %-2d TPS      : ~%.0f tx/s\n", i+1, t)
		}
		fmt.Println("║  ─────────────────────────────────────────────────")
		fmt.Printf("║  📉 Min TPS        : ~%.0f tx/s\n", minTPS)
		fmt.Printf("║  📈 Max TPS        : ~%.0f tx/s\n", maxTPS)
		fmt.Printf("║  📊 Avg TPS        : ~%.0f tx/s\n", avgTPS)
		fmt.Println("╚═══════════════════════════════════════════════════╝")
	}

	// Save results
	results := map[string]interface{}{
		"type":          txTypeName,
		"txCount":       len(allTxs),
		"recipient":     recipient,
		"destinationId": destId,
		"amount":        amountWei,
		"rounds":        numRounds,
		"roundTPS":      allRoundTPS,
		"summaries":     roundSummaries,
	}

	jsonBytes, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile("blast_cc_results.json", jsonBytes, 0644)
	fmt.Println("💾 Results saved to blast_cc_results.json")
}
