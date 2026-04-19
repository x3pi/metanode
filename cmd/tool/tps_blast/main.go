package main

import (
	"bufio"
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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/tool/tps_blast/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
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
	count        int
	accountsFile string
	sleepMs      int
	batchSize    int
)

// rawWriter wraps a raw TCP connection for direct message sending.
// This avoids client.NewClient() which spawns many goroutines (Listen, KeepAlive,
// HandleConnection) causing RemoveConnection conflicts on the server.
type rawWriter struct {
	conn      net.Conn
	writer    *bufio.Writer
	addr      string
	version   string
	toAddrHex string
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

	// Bắt đầu background goroutine để đọc và log error từ server
	go func() {
		reader := bufio.NewReader(conn)
		for {
			lengthBuf := make([]byte, 8)
			if _, err := io.ReadFull(reader, lengthBuf); err != nil {
				return
			}
			msgLen := binary.LittleEndian.Uint64(lengthBuf)
			if msgLen > 10*1024*1024 { // max 10MB
				return
			}
			msgBuf := make([]byte, msgLen)
			if _, err := io.ReadFull(reader, msgBuf); err != nil {
				return
			}
			var msg pb.Message
			if err := proto.Unmarshal(msgBuf, &msg); err == nil && msg.Header != nil {
				// Print ALL messages from server
				if msg.Header.Command == "TransactionError" {
					var txErr pb.TransactionHashWithError
					if proto.Unmarshal(msg.Body, &txErr) == nil {
						fmt.Printf("\n❌ SERVER REJECTED TX: %s | Code: %d | Msg: %s\n",
							common.BytesToHash(txErr.Hash).Hex(),
							txErr.Code,
							txErr.Description)
					}
				} else if msg.Header.Command != "Receipt" {
					fmt.Printf("\n📩 SERVER RESPONDED WITH COMMAND: %s\n", msg.Header.Command)
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
	rw.conn.SetWriteDeadline(time.Now().Add(120 * time.Second))
	if _, err := rw.writer.Write(lengthBuf); err != nil {
		return err
	}
	if _, err := rw.writer.Write(b); err != nil {
		return err
	}
	return nil
}

func (rw *rawWriter) flush() error {
	return rw.writer.Flush()
}

func (rw *rawWriter) close() {
	if rw.conn != nil {
		rw.conn.Close()
	}
}

func main() {
	var nodeOverride string
	flag.StringVar(&nodeOverride, "node", "", "Override the ParentConnectionAddress directly (e.g., 127.0.0.1:6201)")
	flag.StringVar(&configPath, "config", "config.json", "Client config")
	flag.IntVar(&count, "count", 1000, "Number of BLS registrations to blast")
	flag.StringVar(&accountsFile, "accounts_file", "", "JSON file to save/load generated accounts (default: do not save)")
	flag.IntVar(&sleepMs, "sleep", 0, "Sleep between sends (or batches) in ms")
	flag.IntVar(&batchSize, "batch", 0, "Batch size for blasting (0 = sleep per TX, >0 = sleep per batch)")
	waitSecs := flag.Int("wait", 60, "Max seconds to wait for chain processing completion")
	skipVerify := flag.Bool("skip-verify", false, "Skip per-account verification (faster exit for blast scripts)")
	rpcAddr := flag.String("rpc", "", "Target RPC URL for verification (default: auto from config IP:8757)")
	waitFile := flag.String("wait-file", "", "Wait for this file to exist before starting blast (for syncing multi-clients)")
	flag.Parse()

	logger.SetConfig(&logger.LoggerConfig{Flag: 0})

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  🔥 TPS BLAST — Fire-and-Forget BLS Registration")
	fmt.Println("═══════════════════════════════════════════════════")

	// Load config
	configIface, err := c_config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}
	config := configIface.(*c_config.ClientConfig)

	// BLS key pair from config
	blsPubKey := bls.NewKeyPair(config.PrivateKey()).PublicKey().String()
	var pKey p_common.PrivateKey
	copy(pKey[:], config.PrivateKey())

	chainId := config.ChainId
	bigChainId := new(big.Int).SetUint64(chainId)

	// ─── Load or generate accounts ───────────────────────────
	var accounts []*AccountInfo

	if accountsFile != "" {
		data, err := os.ReadFile(accountsFile)
		if err != nil {
			fmt.Printf("  ⚠️  Cannot read %s, generating new accounts\n", accountsFile)
		} else {
			json.Unmarshal(data, &accounts)
			fmt.Printf("  📋 Loaded %d accounts from %s\n", len(accounts), accountsFile)
		}
	}

	if len(accounts) == 0 {
		fmt.Printf("  🔑 Generating %d accounts...\n", count)
		for i := 0; i < count; i++ {
			privateKey, _ := crypto.GenerateKey()
			privKeyBytes := crypto.FromECDSA(privateKey)
			address := crypto.PubkeyToAddress(privateKey.PublicKey)
			accounts = append(accounts, &AccountInfo{
				Index:      i,
				PrivateKey: hex.EncodeToString(privKeyBytes),
				Address:    address.Hex(),
			})
		}
		data, _ := json.MarshalIndent(accounts, "", "  ")
		os.WriteFile(accountsFile, data, 0644)
		fmt.Printf("  ✅ Generated %d, saved to %s\n", count, accountsFile)
	}

	// Filter up to count, ignoring Registered status to allow cache reuse after node wipes
	var toSend []*AccountInfo
	for _, acc := range accounts {
		if len(toSend) < count {
			toSend = append(toSend, acc)
		}
	}
	fmt.Printf("  📊 Accounts to register: %d\n", len(toSend))

	// ─── Pre-build all raw BLS TX bytes (TRƯỚC khi connect) ──────────────
	// Build TXs trước để không giữ connection idle 6.5s — tránh server disconnect
	fmt.Printf("\n📦 Pre-building %d BLS registration TXs...\n", len(toSend))
	buildStart := time.Now()

	type rawTx struct {
		bytes []byte
		addr  string
	}
	var allTxs []rawTx
	var buildErrors int

	for _, acc := range toSend {
		// 1. Create signed EthTx for setBlsPublicKey
		ethTx, err := client.CreateSignedSetBLSPublicKeyTx(acc.PrivateKey, blsPubKey, bigChainId, 0)
		if err != nil {
			buildErrors++
			continue
		}

		// 2. Convert to internal Transaction format
		internalTx, err := transaction.NewTransactionFromEth(ethTx)
		if err != nil {
			buildErrors++
			continue
		}

		// 3. Update related addresses and sign with BLS key
		internalTx.UpdateRelatedAddresses([][]byte{})
		internalTx.UpdateDeriver(common.Hash{}, common.Hash{})
		internalTx.SetSign(pKey)

		// 4. Marshal to raw bytes
		bTx, err := internalTx.Marshal()
		if err != nil {
			buildErrors++
			continue
		}

		allTxs = append(allTxs, rawTx{bytes: bTx, addr: acc.Address})
	}

	buildDuration := time.Since(buildStart)
	fmt.Printf("  ✅ Built %d TXs in %s (%.0f tx/s), %d errors\n",
		len(allTxs), buildDuration.Round(time.Millisecond),
		float64(len(allTxs))/buildDuration.Seconds(), buildErrors)

	// ─── Connect NGAY SAU KHI BUILD XONG dùng rawWriter (raw TCP) ────────────
	// rawWriter tránh client.NewClient() với Listen/KeepAlive/HandleConnection goroutines
	// → chỉ 1 TCP connection, không gây RemoveConnection conflict trên server
	// Lấy địa chỉ Client ngẫu nhiên thay vì dùng PrivateKey chung từ config để tránh conflict Replace connection
	randomPrivKey, _ := crypto.GenerateKey()
	clientAddress := crypto.PubkeyToAddress(randomPrivKey.PublicKey)
	toAddrHex := config.ParentAddress
	version := config.Version()

	maxConnectRetries := 20 // 20 * 500ms = 10s max

	targetAddress := config.ParentConnectionAddress
	if nodeOverride != "" {
		targetAddress = nodeOverride
	}

	reconnect := func() *rawWriter {
		for attempt := 1; attempt <= maxConnectRetries; attempt++ {
			if attempt <= 3 || attempt%10 == 0 {
				fmt.Printf("  🔌 Connecting to %s (attempt %d/%d)...\n", targetAddress, attempt, maxConnectRetries)
			}
			rw, err := newRawWriter(targetAddress, version, toAddrHex)
			if err != nil {
				if attempt <= 3 || attempt%10 == 0 {
					fmt.Printf("  ⚠️  Connect failed: %v, retrying in 500ms...\n", err)
				}
				time.Sleep(500 * time.Millisecond)
				continue
			}

			// Tạo message InitConnection hợp lệ với địa chỉ của client
			initMsg := &pb.InitConnection{
				Address: clientAddress.Bytes(),
				Type:    config.NodeType(), // Thường là "client"
				Replace: true,
			}
			initBody, err := proto.Marshal(initMsg)
			if err != nil {
				log.Fatalf("Failed to marshal InitConnection: %v", err)
			}

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
			fmt.Println("  ✅ Connected and InitConnection sent")
			return rw
		}
		fmt.Printf("  ❌ Failed to connect after %d attempts, exiting\n", maxConnectRetries)
		os.Exit(1)
		return nil
	}

	rw := reconnect()
	defer rw.close()

	// ─── INIT RPC CLIENT ───────────────────────────────────────────────
	// Auto-derive RPC address from config if not specified
	if *rpcAddr == "" {
		// Extract host IP from config's ParentConnectionAddress (e.g., "192.168.1.231:4201" → "192.168.1.231")
		configHost := config.ParentConnectionAddress
		if nodeOverride != "" {
			configHost = nodeOverride
		}
		host := configHost
		if idx := strings.LastIndex(configHost, ":"); idx >= 0 {
			host = configHost[:idx]
		}
		*rpcAddr = host + ":8757"
		fmt.Printf("  📡 Auto RPC: %s (from config)\n", *rpcAddr)
	}
	rpcUrl := *rpcAddr
	if !strings.HasPrefix(rpcUrl, "http") {
		rpcUrl = "http://" + rpcUrl
	}
	rpcClient := rpc.NewRPCClient(rpcUrl)

	startBlock, err := rpcClient.GetBlockNumber()
	if err != nil {
		fmt.Printf("  ⚠️  Failed to reach RPC node at %s to fetch starting block: %v\n", rpcUrl, err)
	} else {
		fmt.Printf("\n  🏁 Starting block: %d\n", startBlock)
	}

	// ─── SINGLE-PASS INJECTION WITH FLOW CONTROL ──────────────
	effectiveBatch := batchSize
	effectiveSleep := sleepMs
	if effectiveBatch <= 0 {
		effectiveBatch = 500
	}
	if effectiveSleep <= 0 {
		effectiveSleep = 10
	}

	fmt.Printf("\n🔥 BLASTING %d BLS TXs via batch SendTransactions...\n", len(allTxs))
	fmt.Printf("   Batch size: %d, Sleep between batches: %dms\n", effectiveBatch, effectiveSleep)

	if *waitFile != "" {
		fmt.Printf("  ⏳ Client Ready. Waiting for sync signal file %s...\n", *waitFile)
		readyFile := fmt.Sprintf("%s_ready_%d", *waitFile, os.Getpid())
		os.WriteFile(readyFile, []byte("ready"), 0644)
		for {
			if _, err := os.Stat(*waitFile); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	blastStart := time.Now()
	fmt.Printf("  [SYNC] START_INJECT_MS=%d\n", blastStart.UnixMilli())
	writeErrors := 0

	// Group TXs into batches
	var batchedMsgs [][]byte // Array of protobuf-encoded pb.Transactions
	for i := 0; i < len(allTxs); i += effectiveBatch {
		end := i + effectiveBatch
		if end > len(allTxs) {
			end = len(allTxs)
		}

		var pbTxs []*pb.Transaction
		for j := i; j < end; j++ {
			// Unmarshal the raw bytes back into protobuf to pack them into pb.Transactions
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

	for i, batchBytes := range batchedMsgs {
		err := rw.sendRaw(command.SendTransactions, batchBytes)
		if err != nil {
			writeErrors++
			fmt.Printf("\n  ⚠️  Write error at Batch %d: %v — reconnecting...\n", i, err)
			rw.close()
			rw = reconnect()
			// Retry this Batch
			if retryErr := rw.sendRaw(command.SendTransactions, batchBytes); retryErr != nil {
				fmt.Printf("  ❌ Batch %d failed after reconnect: %v\n", i, retryErr)
			}
		}

		// Calculate progress based on actual transactions sent
		sentTxs := (i + 1) * effectiveBatch
		if sentTxs > len(allTxs) {
			sentTxs = len(allTxs)
		}

		if (i+1)%10 == 0 || i == len(batchedMsgs)-1 {
			elapsed := time.Since(blastStart)
			rate := float64(sentTxs) / elapsed.Seconds()
			fmt.Printf("\r  📤 [%d/%d] %.0f tx/s | elapsed %s   ",
				sentTxs, len(allTxs), rate, elapsed.Round(time.Millisecond))
		}

		rw.flush()
		if i < len(batchedMsgs)-1 {
			time.Sleep(time.Duration(effectiveSleep) * time.Millisecond)
		}
	}
	rw.flush()

	blastDuration := time.Since(blastStart)
	injectionTPS := float64(len(allTxs)) / blastDuration.Seconds()
	if writeErrors > 0 {
		fmt.Printf("\n  ⚠️  %d reconnects during blast\n", writeErrors)
	}

	fmt.Printf("\n\n  📤 Injected: %d TXs in %s\n", len(allTxs), blastDuration.Round(time.Millisecond))
	fmt.Printf("  🚀 Injection TPS: %.0f tx/s\n", injectionTPS)

	// ─── Poll for completion using block-based monitoring ────
	// Wait until chain stops producing blocks with TXs (mempool empty)
	maxWait := time.Duration(*waitSecs) * time.Second
	pollInterval := 2 * time.Second

	fmt.Printf("\n⏳ Polling chain for completion (max %s, poll every %s)...\n", maxWait, pollInterval)
	processStart := time.Now()

	var processingDuration time.Duration
	emptyBlockStreak := 0
	rpcErrorStreak := 0
	lastBlockNum := startBlock
	totalTxsInBlocks := uint64(0)
	requiredEmptyStreak := 8 // Wait for 8 consecutive polls (16s) with no new blocks → chain truly idle
	maxRpcErrorStreak := 5   // Exit after 5 consecutive RPC errors (10s) — node is likely down

	for time.Since(processStart) < maxWait {
		time.Sleep(pollInterval)

		currentBlockNum, err := rpcClient.GetBlockNumber()
		if err != nil {
			rpcErrorStreak++
			fmt.Printf("\r  📡 [%s] RPC error (%d/%d): %v   ",
				time.Since(processStart).Round(time.Millisecond), rpcErrorStreak, maxRpcErrorStreak, err)
			if rpcErrorStreak >= maxRpcErrorStreak {
				fmt.Printf("\n  ⚠️  RPC unreachable after %d attempts — treating as chain idle\n", rpcErrorStreak)
				break
			}
			continue
		}
		rpcErrorStreak = 0 // Reset on success

		// Count new TXs since last poll
		newTxs := uint64(0)
		nextLastBlockNum := lastBlockNum

		for bn := lastBlockNum + 1; bn <= currentBlockNum; bn++ {
			var newBlockTxs int
			var fetchErr error
			for retry := 0; retry < 3; retry++ {
				blk, err := rpcClient.GetBlockByNumber(bn)
				if err == nil && blk != nil {
					newBlockTxs = len(blk.Transactions)
					fetchErr = nil
					break
				}
				fetchErr = err
				if fetchErr == nil {
					fetchErr = fmt.Errorf("block returned null")
				}
				time.Sleep(200 * time.Millisecond)
			}

			if fetchErr == nil {
				newTxs += uint64(newBlockTxs)
				nextLastBlockNum = bn
			} else {
				break
			}
		}

		totalTxsInBlocks += newTxs
		lastBlockNum = nextLastBlockNum

		pct := float64(totalTxsInBlocks) / float64(len(allTxs)) * 100
		if pct > 100 {
			pct = 100
		}

		fmt.Printf("\r  📡 [%s] Block: %d | TXs in blocks: %d (this client: %d/%d %.0f%%) | +%d new   ",
			time.Since(processStart).Round(time.Millisecond),
			currentBlockNum, totalTxsInBlocks, totalTxsInBlocks, len(allTxs), pct, newTxs)
		os.Stdout.Sync()

		// NOTE: Do NOT exit based on totalTxsInBlocks >= len(allTxs).
		// In multi-client mode, other clients' TXs also fill blocks.
		// We rely on emptyBlockStreak to detect when chain is truly idle.

		if newTxs == 0 {
			emptyBlockStreak++
			// Exit when chain is idle — in blast mode, many TXs are dropped
			// (dups, mempool overflow). Don't wait forever for them.
			if emptyBlockStreak >= requiredEmptyStreak {
				processingDuration = time.Since(processStart) - time.Duration(emptyBlockStreak)*pollInterval
				if processingDuration < 0 {
					processingDuration = 100 * time.Millisecond
				}
				fmt.Printf("\n  ✅ Chain idle for %ds — %d/%d TXs in blocks\n",
					emptyBlockStreak*int(pollInterval.Seconds()), totalTxsInBlocks, len(allTxs))
				break
			}
		} else {
			emptyBlockStreak = 0
		}
	}

	if processingDuration == 0 {
		processingDuration = time.Since(processStart)
		fmt.Printf("\n  ⚠️  Max wait reached (%s)\n", maxWait)
	}

	var totalConfirmed int32
	var totalFailed int32
	var totalErrors int32

	if !*skipVerify {
		// ─── FULL VERIFICATION (via HTTP RPC — concurrency safe) ─
		fmt.Printf("\n🔍 Verifying ALL %d accounts via HTTP RPC (%s)...\n", len(toSend), rpcUrl)

		// Add a cooldown for nodes to finish flushing batches to trie/LevelDB
		fmt.Println("  ⏳ Waiting 10s for go-sub state flush...")
		time.Sleep(10 * time.Second)

		verifyTimeout := 180 * time.Second
		verifyDeadline := time.Now().Add(verifyTimeout)
		workerCount := 10
		jobs := make(chan int, len(toSend))
		var wg sync.WaitGroup
		var verifyTimedOut int32

		for w := 0; w < workerCount; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobs {
					// Check global timeout
					if time.Now().After(verifyDeadline) {
						atomic.StoreInt32(&verifyTimedOut, 1)
						// Drain remaining jobs
						atomic.AddInt32(&totalErrors, 1)
						continue
					}

					var err error
					var isConfirmed bool
					var as *rpc.AccountStateResult
					maxRetries := 15 // 15 retries × 200ms = 3s max per account
					for retries := 0; retries < maxRetries; retries++ {
						// Check timeout each retry
						if time.Now().After(verifyDeadline) {
							err = fmt.Errorf("global verify timeout")
							break
						}
						asResult, retryErr := rpcClient.GetAccountState(toSend[idx].Address)
						as = asResult
						err = retryErr
						if err == nil {
							if as != nil && as.PublicKeyBls != "" {
								isConfirmed = true
								break
							}
						}
						time.Sleep(200 * time.Millisecond)
					}
					if err != nil {
						atomic.AddInt32(&totalErrors, 1)
					} else if isConfirmed {
						atomic.AddInt32(&totalConfirmed, 1)
						toSend[idx].Registered = true
					} else {
						atomic.AddInt32(&totalFailed, 1)
					}

					done := atomic.LoadInt32(&totalConfirmed) + atomic.LoadInt32(&totalFailed) + atomic.LoadInt32(&totalErrors)
					if done%500 == 0 || done == int32(len(toSend)) {
						fmt.Printf("\r   Verified %d/%d (✅ %d | ❌ %d | ⚠️ %d)   ",
							done, len(toSend), atomic.LoadInt32(&totalConfirmed), atomic.LoadInt32(&totalFailed), atomic.LoadInt32(&totalErrors))
						os.Stdout.Sync()
					}
				}
			}()
		}

		for si := 0; si < len(toSend); si++ {
			jobs <- si
		}
		close(jobs)
		wg.Wait()

		if atomic.LoadInt32(&verifyTimedOut) == 1 {
			fmt.Printf("\n  ⚠️  Verification timed out after %s\n", verifyTimeout)
		}
		fmt.Println()
	} else {
		fmt.Println("\n⚡ Skipping per-account verification (--skip-verify)")
	}

	endBlock, err := rpcClient.GetBlockNumber()
	if err != nil {
		fmt.Printf("  ⚠️  Failed to reach RPC node at %s to fetch ending block: %v\n", rpcUrl, err)
	} else {
		fmt.Printf("  🏁 Ending block:   %d\n", endBlock)
	}

	// ─── Processing TPS from actual measured time ────────────
	finalConfirmed := int(0)
	for _, acc := range toSend {
		if acc.Registered {
			finalConfirmed++
		}
	}
	successRate := float64(finalConfirmed) / float64(len(toSend)) * 100
	processingTPS := float64(finalConfirmed) / processingDuration.Seconds()

	finalFailed := len(toSend) - finalConfirmed

	fmt.Printf("\n\n═══════════════════════════════════════════════════\n")
	fmt.Printf("  📊 BLS REGISTRATION RESULTS\n")
	fmt.Printf("═══════════════════════════════════════════════════\n")
	fmt.Printf("  📤 Total TXs sent:       %d\n", len(allTxs))
	fmt.Printf("  🚀 Injection TPS:        %.0f tx/s\n", injectionTPS)
	fmt.Printf("  ⏱️  Injection time:       %s\n", blastDuration.Round(time.Millisecond))
	fmt.Printf("  ─────────────────────────────────────────────────\n")
	fmt.Printf("  🔍 Verified:             %d/%d (✅ %d | ❌ %d | ⚠️ %d)\n", len(toSend), len(toSend), totalConfirmed, totalFailed, totalErrors)
	fmt.Printf("  ✅ Success rate:         %.1f%%\n", successRate)
	totalRealSec := blastDuration.Seconds() + processingDuration.Seconds()
	fmt.Printf("  ─────────────────────────────────────────────────\n")
	fmt.Printf("  📊 Processing TPS:       ~%.0f tx/s\n", processingTPS)
	fmt.Printf("  ⏱️  Network confirmation: %s\n", processingDuration.Round(time.Millisecond))
	fmt.Printf("  🕛 Total real time:      %.3fs (inject + confirm)\n", totalRealSec)
	fmt.Printf("  ─────────────────────────────────────────────────\n")

	// Fetch block statistics
	blockCount := 0
	emptyBlocks := 0
	maxTxInBlock := 0
	totalTxInBlocks := 0

	for b := startBlock; b <= endBlock; b++ {
		blkInfo, err := rpcClient.GetBlockByNumber(b)
		if err == nil && blkInfo != nil {
			blockCount++
			txCount := len(blkInfo.Transactions)
			totalTxInBlocks += txCount
			if txCount == 0 {
				emptyBlocks++
			}
			if txCount > maxTxInBlock {
				maxTxInBlock = txCount
			}
		}
	}

	fmt.Printf("  📦 BLOCK STATISTICS (Blocks %d to %d)\n", startBlock, endBlock)
	fmt.Printf("  🧊 Total Blocks:         %d\n", blockCount)
	fmt.Printf("  📥 Total TXs in blocks:  %d\n", totalTxInBlocks)
	fmt.Printf("  📈 Max TXs in a block:   %d\n", maxTxInBlock)
	if blockCount > 0 {
		fmt.Printf("  📉 Avg TXs per block:    %.1f\n", float64(totalTxInBlocks)/float64(blockCount))
	}
	fmt.Printf("  👻 Empty Blocks:         %d\n", emptyBlocks)
	fmt.Printf("═══════════════════════════════════════════════════\n")

	// Save results
	results := map[string]interface{}{
		"txCount":       len(allTxs),
		"blastDuration": blastDuration.String(),
		"blsConfirmed":  finalConfirmed,
		"blsFailed":     finalFailed,
		"successRate":   fmt.Sprintf("%.1f%%", successRate),
		"processingTPS": processingTPS,
		"sleepMs":       sleepMs,
		"blockStats": map[string]interface{}{
			"startBlock":      startBlock,
			"endBlock":        endBlock,
			"totalBlocks":     blockCount,
			"emptyBlocks":     emptyBlocks,
			"maxTxInBlock":    maxTxInBlock,
			"totalTxInBlocks": totalTxInBlocks,
		},
	}

	jsonBytes, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile("blast_results.json", jsonBytes, 0644)
	fmt.Println("💾 Results saved to blast_results.json")

	// OVERWRITE ACCOUNTS JSON with updated Registered status
	if accountsFile != "" {
		accData, _ := json.MarshalIndent(accounts, "", "  ")
		os.WriteFile(accountsFile, accData, 0644)
		fmt.Printf("💾 Updated accounts saved to %s\n", accountsFile)
	} else {
		accData, _ := json.MarshalIndent(accounts, "", "  ")
		os.WriteFile("blast_accounts.json", accData, 0644)
		fmt.Println("💾 Updated accounts saved to blast_accounts.json")
	}
}
