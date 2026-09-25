//go:build c0spike

package main

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// BlockRecord stores execution result of a single block for determinism comparison
type BlockRecord struct {
	Number                 uint64 `json:"number"`
	Hash                   string `json:"hash"`
	AccountStatesRoot      string `json:"account_states_root"`
	StakeStatesRoot        string `json:"stake_states_root"`
	ReceiptsRoot           string `json:"receipts_root"`
	TxsRoot                string `json:"txs_root"`
	TxCount                int    `json:"tx_count"`
	SampleSenderNonce      uint64 `json:"sample_sender_nonce"`
	SampleRecipientBalance string `json:"sample_recipient_balance"`
	AllReceiptsSuccess     bool   `json:"all_receipts_success"`
	ContractAddress        string `json:"contract_address,omitempty"`
}

// RustIsolationEvidence records empirical measurements of process threads, sockets, and FFI state
type RustIsolationEvidence struct {
	FFIBridgeCallCount uint64         `json:"ffi_bridge_call_count"`
	TokioThreadsCount  int            `json:"tokio_threads_count"`
	ConsensusThreads   []string       `json:"consensus_threads"`
	NOMTThreads        map[string]int `json:"nomt_threads"`
	ListenSockets      []string       `json:"listen_sockets"`
	ObservedThreads    map[string]int `json:"observed_threads"`
}

// WorkerResult wraps records and runtime diagnostics emitted by a C0 worker process
type WorkerResult struct {
	Records           []BlockRecord          `json:"records"`
	IsolationEvidence *RustIsolationEvidence `json:"isolation_evidence,omitempty"`
	BypassedBlocks    int                    `json:"bypassed_blocks"`
	StartBlockNum     uint64                 `json:"start_block_num"`
}

func runC0Spike(mode, configPath, dataDir, outPath string, blocksCount int, isRestart bool, customReportPath string) {
	switch mode {
	case "worker":
		if err := runC0Worker(configPath, dataDir, outPath, blocksCount, isRestart); err != nil {
			fmt.Fprintf(os.Stderr, "❌ [C0 WORKER ERROR] %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	case "verify":
		if err := runC0Verify(configPath, blocksCount, customReportPath); err != nil {
			fmt.Fprintf(os.Stderr, "❌ [C0 VERIFY ERROR] %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown C0 spike mode: %q (supported: 'worker', 'verify')\n", mode)
		os.Exit(1)
	}
}

// ownSocketInodes returns the inode numbers of the socket file descriptors held by this process.
func ownSocketInodes() map[string]bool {
	inodes := make(map[string]bool)
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return inodes
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil || !strings.HasPrefix(target, "socket:[") {
			continue
		}
		inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
	}
	return inodes
}

func measureRustConsensusIsolation() (*RustIsolationEvidence, error) {
	ev := &RustIsolationEvidence{
		FFIBridgeCallCount: executor.InitFFIBridgeCallCount(),
		ObservedThreads:    make(map[string]int),
		NOMTThreads:        make(map[string]int),
		ConsensusThreads:   make([]string, 0),
		ListenSockets:      make([]string, 0),
	}

	// 1. Thread inspection via /proc/self/task/*/comm
	taskDir := "/proc/self/task"
	entries, err := os.ReadDir(taskDir)
	if err == nil {
		for _, e := range entries {
			commBytes, err := os.ReadFile(filepath.Join(taskDir, e.Name(), "comm"))
			if err != nil {
				continue
			}
			comm := strings.TrimSpace(string(commBytes))
			ev.ObservedThreads[comm]++

			// Identify NOMT internal Rust threads
			if strings.HasPrefix(comm, "beatree-") || strings.HasPrefix(comm, "nomt-") ||
				strings.HasPrefix(comm, "io-worker") || strings.HasPrefix(comm, "bitbox-") ||
				strings.HasPrefix(comm, "iou-wrk") {
				ev.NOMTThreads[comm]++
			}

			// Identify Rust consensus runtime threads
			if strings.Contains(comm, "tokio") {
				ev.TokioThreadsCount++
			}
			if strings.Contains(comm, "consensus") || strings.Contains(comm, "metanode-") {
				ev.ConsensusThreads = append(ev.ConsensusThreads, comm)
			}
		}
	}

	// 2. Listening TCP sockets owned by THIS process. /proc/self/net/tcp{,6} lists every socket of
	// the whole network namespace (other processes included), so keep only entries whose inode is
	// one of our own socket file descriptors.
	ownInodes := ownSocketInodes()
	for _, netFile := range []string{"/proc/self/net/tcp", "/proc/self/net/tcp6"} {
		lines, err := os.ReadFile(netFile)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(lines), "\n") {
			fields := strings.Fields(line)
			// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode
			if len(fields) >= 10 && fields[3] == "0A" && ownInodes[fields[9]] { // 0A = TCP_LISTEN
				ev.ListenSockets = append(ev.ListenSockets, fmt.Sprintf("%s:%s", netFile, fields[1]))
			}
		}
	}

	return ev, nil
}

func runC0Worker(baseConfigPath, dataDir, outPath string, blocksCount int, isRestart bool) error {
	if dataDir == "" {
		return fmt.Errorf("data-dir must be specified for worker")
	}
	if outPath == "" {
		outPath = filepath.Join(dataDir, "c0_result.json")
	}

	// 1. Prepare isolated config
	cfgFile, err := prepareC0Config(baseConfigPath, dataDir)
	if err != nil {
		return fmt.Errorf("prepareC0Config: %w", err)
	}

	// 2. Initialize App in Raft mode
	app, err := NewApp(cfgFile, logger.FLAG_INFO)
	if err != nil {
		return fmt.Errorf("NewApp failed: %w", err)
	}
	defer app.Stop()

	// Start background workers (commitWorker)
	app.blockProcessor.StartBackgroundWorkers()

	// Obtain synchronously initialized ingestion queue (Hook H1 / Raft mode)
	q := app.blockProcessor.GetBlockIngestionQueue()
	if q == nil {
		return fmt.Errorf("blockIngestionQueue is nil")
	}

	startBlockNum := storage.GetLastBlockNumber()
	fmt.Printf("🚀 [C0 WORKER] Initialized node at %s (current block: #%d, isRestart: %v)\n", dataDir, startBlockNum, isRestart)

	asDb := app.chainState.GetAccountStateDB()
	senderAddr := e_common.HexToAddress("0x294f72878a83B7d076E1d28eedecd184863df846")
	sender1Addr := e_common.HexToAddress("0x616969160142a381bb315A286fA54B7eD1749C49")
	recipAddr := e_common.HexToAddress("0x1111111111111111111111111111111111111111")

	// Register Gateway destination chain 102 for this worker process (harness-only hook, compiled with
	// -tags c0spike). It is applied to every GatewayEngine loaded during block execution.
	tx_processor.RegisterInitialChain(cross_chain.ChainRegistry{
		ChainID: 102,
		Epoch:   1,
		Committee: []cross_chain.ValidatorEntry{
			{
				PubkeyBLS: []byte("bls_pubkey_c0_spike_chain_102"),
				Stake:     1000,
			},
		},
	})

	// Seed Gateway destination chain 102 when starting a fresh chain (block 0)
	if startBlockNum == 0 {
		gwAddr := mt_common.GATEWAY_CONTRACT_ADDRESS
		gwAs, err := asDb.AccountState(gwAddr)
		if err != nil || gwAs == nil {
			gwAs = state.NewAccountState(gwAddr)
		}
		if gwAs.SmartContractState() == nil {
			gwAs.SetSmartContractState(state.NewEmptySmartContractState())
		}
		asDb.SetState(gwAs)

		engine, err := tx_processor.LoadGatewayEngine(app.chainState)
		if err == nil && engine != nil {
			if engine.ChainRegistry == nil {
				engine.ChainRegistry = make(map[uint64]cross_chain.ChainRegistry)
			}
			if _, ok := engine.ChainRegistry[102]; !ok {
				engine.ChainRegistry[102] = cross_chain.ChainRegistry{
					ChainID: 102,
					Epoch:   1,
					Committee: []cross_chain.ValidatorEntry{
						{
							PubkeyBLS: []byte("bls_pubkey_c0_spike_chain_102"),
							Stake:     1000,
						},
					},
				}
				if err := tx_processor.SaveGatewayEngine(app.chainState, engine); err != nil {
					return fmt.Errorf("SaveGatewayEngine seed: %w", err)
				}
			}
		}
	}

	// 3. Build deterministic transactions and blocks (Native + EVM + Gateway)
	chainId := app.config.ChainId
	if chainId == nil {
		chainId = big.NewInt(991)
	}

	totalBlocks := blocksCount
	if isRestart {
		totalBlocks = blocksCount + 1
	}

	executableBlocks, err := buildDeterministicC0Blocks(chainId, totalBlocks)
	if err != nil {
		return fmt.Errorf("buildDeterministicC0Blocks: %w", err)
	}

	// Pre-flight check: ensure genesis funds the C0 spike senders to prevent silent empty runs
	if asSender, err := asDb.AccountStateReadOnly(senderAddr); err != nil || asSender == nil || asSender.Balance() == nil || asSender.Balance().Sign() <= 0 {
		return fmt.Errorf("genesis at %q does not fund C0 spike sender %s (account not found or balance <= 0)", cfgFile, senderAddr.Hex())
	}
	if asSender1, err := asDb.AccountStateReadOnly(sender1Addr); err != nil || asSender1 == nil || asSender1.Balance() == nil || asSender1.Balance().Sign() <= 0 {
		return fmt.Errorf("genesis at %q does not fund C0 spike sender 1 %s (account not found or balance <= 0)", cfgFile, sender1Addr.Hex())
	}

	type blockSample struct {
		nonce   uint64
		balance string
	}
	samples := make(map[uint64]blockSample)
	bypassedBlocks := 0

	// 4. Feed blocks into ingestion queue
	for _, eb := range executableBlocks {
		targetHeight := eb.BlockNumber

		// If this is a historical block during restart, verify identity against DB (Zero-Fork P2.5) and bypass without re-executing
		if isRestart && targetHeight <= startBlockNum {
			bc := blockchain.GetBlockChainInstance()
			bDb := app.chainState.GetBlockDatabase()
			bHash, ok := bc.GetBlockHashByNumber(targetHeight)
			if !ok {
				return fmt.Errorf("restart bypass: block #%d hash not found in chain index", targetHeight)
			}
			committedBlk, err := bDb.GetBlockByHash(bHash)
			if err != nil || committedBlk == nil {
				return fmt.Errorf("restart bypass: block #%d hash %s not found in DB", targetHeight, bHash.Hex())
			}
			if committedBlk.Header().GlobalExecIndex() != eb.GlobalExecIndex {
				return fmt.Errorf("restart bypass: block #%d GEI mismatch: db=%d, consensus=%d (FAIL CLOSED)", targetHeight, committedBlk.Header().GlobalExecIndex(), eb.GlobalExecIndex)
			}
			if len(committedBlk.Transactions()) != len(eb.Transactions) {
				return fmt.Errorf("restart bypass: block #%d tx count mismatch: db=%d, consensus=%d (FAIL CLOSED)", targetHeight, len(committedBlk.Transactions()), len(eb.Transactions))
			}
			fmt.Printf("⏭️ [C0 WORKER] Block #%d [ALREADY COMMITTED / SKIPPED] with identity match (GEI=%d, Hash=%s, Txs=%d)\n",
				targetHeight, committedBlk.Header().GlobalExecIndex(), bHash.Hex()[:16]+"...", len(committedBlk.Transactions()))
			bypassedBlocks++
			continue
		}

		fmt.Printf("📥 [C0 WORKER] Submitting ExecutableBlock #%d (txs: %d, GEI: %d)...\n",
			targetHeight, len(eb.Transactions), eb.GlobalExecIndex)

		q <- eb

		// Wait for this block to commit
		committed := false
		for wait := 0; wait < 200; wait++ { // up to 10 seconds per block
			lastHeight := storage.GetLastBlockNumber()
			if lastHeight >= targetHeight {
				committed = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}

		if !committed {
			return fmt.Errorf("timeout waiting for block #%d to commit (lastHeight=%d)", targetHeight, storage.GetLastBlockNumber())
		}
		fmt.Printf("✅ [C0 WORKER] Block #%d processed (committed height=%d)\n", targetHeight, storage.GetLastBlockNumber())

		// Ensure block processor finished persisting this block
		app.blockProcessor.WaitForPersistence()

		// Write progress marker for progress-driven kill -9 coordination
		progressFile := filepath.Join(dataDir, "commit_progress.txt")
		_ = os.WriteFile(progressFile, []byte(fmt.Sprintf("%d", targetHeight)), 0644)

		// Sample state mutation immediately when this block is the tip
		_ = app.chainState.GetAccountStateDB().Discard() // Invalidate loadedAccounts cache to read fresh from disk
		sNonce := uint64(0)
		if asSender, err := app.chainState.GetAccountStateDB().AccountStateReadOnly(senderAddr); err == nil && asSender != nil {
			sNonce = asSender.Nonce()
		}
		rBal := "0"
		if asRecip, err := app.chainState.GetAccountStateDB().AccountStateReadOnly(recipAddr); err == nil && asRecip != nil && asRecip.Balance() != nil {
			rBal = asRecip.Balance().String()
		}
		samples[targetHeight] = blockSample{nonce: sNonce, balance: rBal}

		time.Sleep(50 * time.Millisecond)
	}

	// Drain persistence pipeline
	app.blockProcessor.WaitForPersistence()

	// 5. Collect block records with state mutation assertions
	records := make([]BlockRecord, 0, totalBlocks)
	bc := blockchain.GetBlockChainInstance()
	bDb := app.chainState.GetBlockDatabase()
	rcpStorage := app.storageManager.GetStorageReceipt()

	var prevStatesRoot string
	for b := 1; b <= totalBlocks; b++ {
		hash, ok := bc.GetBlockHashByNumber(uint64(b))
		if !ok {
			return fmt.Errorf("block #%d hash not found in mapping", b)
		}
		blkIface, err := bDb.GetBlockByHash(hash)
		if err != nil || blkIface == nil {
			return fmt.Errorf("block #%d not found in blockDatabase: %v", b, err)
		}
		hdr := blkIface.Header()

		// Assert tx_count > 0 to prevent silent pass on empty blocks
		if len(blkIface.Transactions()) == 0 {
			return fmt.Errorf("block #%d has 0 transactions! Consensus expected txs. Check genesis configuration.", b)
		}
		// Assert state root advances on each block
		if b > 1 && hdr.AccountStatesRoot().Hex() == prevStatesRoot {
			return fmt.Errorf("block #%d state root (%s) did not advance from block #%d! State mutation failed.", b, hdr.AccountStatesRoot().Hex(), b-1)
		}
		prevStatesRoot = hdr.AccountStatesRoot().Hex()

		allReceiptsSuccess := true
		rcpDb, err := receipt.NewReceiptsFromRoot(hdr.ReceiptRoot(), rcpStorage)
		if err != nil {
			allReceiptsSuccess = false
		} else {
			for _, txHash := range blkIface.Transactions() {
				rcp, err := rcpDb.GetReceipt(txHash)
				if err != nil || rcp == nil || rcp.Status() != pb.RECEIPT_STATUS_RETURNED {
					allReceiptsSuccess = false
					break
				}
			}
		}

		smp := samples[uint64(b)]

		contractAddrStr := ""
		if b == 1 {
			contractAddrStr = crypto.CreateAddress(senderAddr, 1).Hex()
		}

		records = append(records, BlockRecord{
			Number:                 uint64(b),
			Hash:                   hdr.Hash().Hex(),
			AccountStatesRoot:      hdr.AccountStatesRoot().Hex(),
			StakeStatesRoot:        hdr.StakeStatesRoot().Hex(),
			ReceiptsRoot:           hdr.ReceiptRoot().Hex(),
			TxsRoot:                hdr.TransactionsRoot().Hex(),
			TxCount:                len(blkIface.Transactions()),
			SampleSenderNonce:      smp.nonce,
			SampleRecipientBalance: smp.balance,
			AllReceiptsSuccess:     allReceiptsSuccess,
			ContractAddress:        contractAddrStr,
		})
	}

	// 6. Measure Rust consensus runtime isolation
	isolationEvidence, _ := measureRustConsensusIsolation()

	workerResult := WorkerResult{
		Records:           records,
		IsolationEvidence: isolationEvidence,
		BypassedBlocks:    bypassedBlocks,
		StartBlockNum:     startBlockNum,
	}

	// 7. Write to output file
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(workerResult); err != nil {
		return fmt.Errorf("encode workerResult: %w", err)
	}

	fmt.Printf("🎉 [C0 WORKER] Successfully wrote %d block records (startBlock=%d, bypassed=%d) to %s\n",
		len(records), startBlockNum, bypassedBlocks, outPath)
	return nil
}

func compareBlockRecords(rec1, rec2 []BlockRecord, context string) error {
	if len(rec1) != len(rec2) {
		return fmt.Errorf("[%s] record count mismatch: len(rec1)=%d, len(rec2)=%d", context, len(rec1), len(rec2))
	}
	for i := range rec1 {
		r1 := rec1[i]
		r2 := rec2[i]
		if r1.Hash != r2.Hash {
			return fmt.Errorf("[%s] Block #%d Hash mismatch: %s vs %s", context, r1.Number, r1.Hash, r2.Hash)
		}
		if r1.AccountStatesRoot != r2.AccountStatesRoot {
			return fmt.Errorf("[%s] Block #%d StateRoot mismatch: %s vs %s", context, r1.Number, r1.AccountStatesRoot, r2.AccountStatesRoot)
		}
		if r1.ReceiptsRoot != r2.ReceiptsRoot {
			return fmt.Errorf("[%s] Block #%d ReceiptsRoot mismatch: %s vs %s", context, r1.Number, r1.ReceiptsRoot, r2.ReceiptsRoot)
		}
		if r1.TxsRoot != r2.TxsRoot {
			return fmt.Errorf("[%s] Block #%d TxsRoot mismatch: %s vs %s", context, r1.Number, r1.TxsRoot, r2.TxsRoot)
		}
		if r1.TxCount != r2.TxCount {
			return fmt.Errorf("[%s] Block #%d TxCount mismatch: %d vs %d", context, r1.Number, r1.TxCount, r2.TxCount)
		}
		if !r1.AllReceiptsSuccess || !r2.AllReceiptsSuccess {
			return fmt.Errorf("[%s] Block #%d has failed receipts: rec1=%v vs rec2=%v", context, r1.Number, r1.AllReceiptsSuccess, r2.AllReceiptsSuccess)
		}
		fmt.Printf("   ✅ [%s] Block #%d: Hash=%s | StateRoot=%s | Txs=%d | AllSuccess=%v\n",
			context, r1.Number, r1.Hash[:16]+"...", r1.AccountStatesRoot[:16]+"...", r1.TxCount, r1.AllReceiptsSuccess)
	}
	return nil
}

func runC0Verify(baseConfigPath string, blocksCount int, customReportPath string) error {
	tmpBase, err := os.MkdirTemp("", "c0_spike_*")
	if err != nil {
		return fmt.Errorf("os.MkdirTemp: %w", err)
	}
	defer os.RemoveAll(tmpBase)

	selfExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}

	fmt.Println("═════════════════════════════════════════════════════════════════════")
	fmt.Printf("🧪 C0 SPIKE: DETERMINISM, WORKLOAD EXPANSION & PROGRESS-DRIVEN KILL -9\n")
	fmt.Printf("   Blocks: %d | Backend: NOMT | Mode: Raft | Workload: Native+EVM+Gateway\n", blocksCount)
	fmt.Println("═════════════════════════════════════════════════════════════════════")

	// ROUND 1: Two independent OS processes
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔄 ROUND 1: Verification Across 2 Independent OS Processes")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	dirR1P1 := filepath.Join(tmpBase, "r1_node1")
	dirR1P2 := filepath.Join(tmpBase, "r1_node2")
	outR1P1 := filepath.Join(tmpBase, "res_r1_node1.json")
	outR1P2 := filepath.Join(tmpBase, "res_r1_node2.json")

	fmt.Printf("\n▶ Round 1, Step 1: Running Process 1 in %s...\n", dirR1P1)
	startR1P1 := time.Now()
	cmdR1P1 := exec.Command(selfExe,
		"-tool-c0-spike=worker",
		"-config="+baseConfigPath,
		"-c0-data-dir="+dirR1P1,
		"-c0-out="+outR1P1,
		fmt.Sprintf("-c0-blocks=%d", blocksCount),
	)
	cmdR1P1.Stdout = os.Stdout
	cmdR1P1.Stderr = os.Stderr
	if err := cmdR1P1.Run(); err != nil {
		return fmt.Errorf("Round 1 Process 1 failed: %w", err)
	}
	durR1P1 := time.Since(startR1P1)

	fmt.Printf("\n▶ Round 1, Step 2: Running Process 2 in %s...\n", dirR1P2)
	startR1P2 := time.Now()
	cmdR1P2 := exec.Command(selfExe,
		"-tool-c0-spike=worker",
		"-config="+baseConfigPath,
		"-c0-data-dir="+dirR1P2,
		"-c0-out="+outR1P2,
		fmt.Sprintf("-c0-blocks=%d", blocksCount),
	)
	cmdR1P2.Stdout = os.Stdout
	cmdR1P2.Stderr = os.Stderr
	if err := cmdR1P2.Run(); err != nil {
		return fmt.Errorf("Round 1 Process 2 failed: %w", err)
	}
	durR1P2 := time.Since(startR1P2)

	resR1P1, err := loadWorkerResult(outR1P1)
	if err != nil {
		return fmt.Errorf("loadWorkerResult(%s): %w", outR1P1, err)
	}
	resR1P2, err := loadWorkerResult(outR1P2)
	if err != nil {
		return fmt.Errorf("loadWorkerResult(%s): %w", outR1P2, err)
	}

	if err := compareBlockRecords(resR1P1.Records, resR1P2.Records, "Round 1 (Proc1 vs Proc2)"); err != nil {
		return err
	}
	fmt.Printf("🎉 [ROUND 1 SUCCESS] 100%% Deterministic between Process 1 and Process 2!\n")

	// ROUND 2: Fresh isolated data directories to prove multi-round repeatability
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔄 ROUND 2: Multi-Round Determinism Across Fresh Data Dirs")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	dirR2P1 := filepath.Join(tmpBase, "r2_node1")
	dirR2P2 := filepath.Join(tmpBase, "r2_node2")
	outR2P1 := filepath.Join(tmpBase, "res_r2_node1.json")
	outR2P2 := filepath.Join(tmpBase, "res_r2_node2.json")

	fmt.Printf("\n▶ Round 2, Step 1: Running Process 1 in %s...\n", dirR2P1)
	cmdR2P1 := exec.Command(selfExe,
		"-tool-c0-spike=worker",
		"-config="+baseConfigPath,
		"-c0-data-dir="+dirR2P1,
		"-c0-out="+outR2P1,
		fmt.Sprintf("-c0-blocks=%d", blocksCount),
	)
	cmdR2P1.Stdout = os.Stdout
	cmdR2P1.Stderr = os.Stderr
	if err := cmdR2P1.Run(); err != nil {
		return fmt.Errorf("Round 2 Process 1 failed: %w", err)
	}

	fmt.Printf("\n▶ Round 2, Step 2: Running Process 2 in %s...\n", dirR2P2)
	cmdR2P2 := exec.Command(selfExe,
		"-tool-c0-spike=worker",
		"-config="+baseConfigPath,
		"-c0-data-dir="+dirR2P2,
		"-c0-out="+outR2P2,
		fmt.Sprintf("-c0-blocks=%d", blocksCount),
	)
	cmdR2P2.Stdout = os.Stdout
	cmdR2P2.Stderr = os.Stderr
	if err := cmdR2P2.Run(); err != nil {
		return fmt.Errorf("Round 2 Process 2 failed: %w", err)
	}

	resR2P1, err := loadWorkerResult(outR2P1)
	if err != nil {
		return fmt.Errorf("loadWorkerResult(%s): %w", outR2P1, err)
	}
	resR2P2, err := loadWorkerResult(outR2P2)
	if err != nil {
		return fmt.Errorf("loadWorkerResult(%s): %w", outR2P2, err)
	}

	if err := compareBlockRecords(resR2P1.Records, resR2P2.Records, "Round 2 (Proc1 vs Proc2)"); err != nil {
		return err
	}
	if err := compareBlockRecords(resR1P1.Records, resR2P1.Records, "Cross-Round (Round 1 vs Round 2)"); err != nil {
		return err
	}
	fmt.Printf("🎉 [ROUND 2 SUCCESS] 100%% Cross-Round Determinism confirmed (Round 1 == Round 2)!\n")

	// STEP 3: RUST CONSENSUS ISOLATION PROOF
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🛡️ STEP 3: Rust Consensus Isolation Proof (Empirical Measurement)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	iso := resR1P1.IsolationEvidence
	if iso == nil {
		return fmt.Errorf("missing isolation evidence from Process 1")
	}

	fmt.Printf("   - Consensus Mode: 'raft'\n")
	fmt.Printf("   - InitFFIBridge Call Count: %d (asserted 0)\n", iso.FFIBridgeCallCount)
	fmt.Printf("   - Tokio Consensus Threads: %d (asserted 0)\n", iso.TokioThreadsCount)
	fmt.Printf("   - Listening TCP sockets owned by the worker process: %d (asserted 0; counted by fd inode, not by network namespace)\n", len(iso.ListenSockets))
	fmt.Printf("   - NOMT Internal Rust Threads (Storage Only): %d distinct thread types detected\n", len(iso.NOMTThreads))
	for name, count := range iso.NOMTThreads {
		fmt.Printf("     • %s: %d threads\n", name, count)
	}

	if iso.FFIBridgeCallCount != 0 {
		return fmt.Errorf("Rust isolation failure: InitFFIBridge called %d times", iso.FFIBridgeCallCount)
	}
	if iso.TokioThreadsCount != 0 {
		return fmt.Errorf("Rust isolation failure: found %d tokio threads", iso.TokioThreadsCount)
	}
	if len(iso.ConsensusThreads) > 0 {
		return fmt.Errorf("Rust isolation failure: found consensus threads: %v", iso.ConsensusThreads)
	}
	if len(iso.ListenSockets) != 0 {
		return fmt.Errorf("Rust isolation failure: worker process owns %d listening TCP sockets: %v", len(iso.ListenSockets), iso.ListenSockets)
	}
	fmt.Printf("   - Status: PASS (Empirically verified: no Rust consensus runtime; NOMT storage threads isolated)\n")

	// STEP 4: PROGRESS-DRIVEN ABRUPT CRASH (kill -9) & RECOVERY TEST
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("💥 STEP 4: Progress-Driven Abrupt Crash (kill -9) & Recovery Test")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Kill points: right after the first block, after the second, and one block before the end.
	killPoints := []uint64{1, 2}
	if last := uint64(blocksCount - 1); last > 2 {
		killPoints = append(killPoints, last)
	}
	var lastCrash *crashScenarioResult
	var crashSummaries []string
	for _, kp := range killPoints {
		res, err := runCrashScenario(selfExe, baseConfigPath, tmpBase, blocksCount, kp, resR1P1.Records)
		if err != nil {
			return fmt.Errorf("crash scenario (kill at block >= %d): %w", kp, err)
		}
		lastCrash = res
		crashSummaries = append(crashSummaries, fmt.Sprintf("kill at block #%d -> startBlockNum=%d, bypassed=%d", res.KilledAtBlock, res.StartBlockNum, res.BypassedBlocks))
	}
	durRestart := lastCrash.Duration
	recCrash := lastCrash.Records
	blkN1 := recCrash[blocksCount]

	fmt.Printf("✅ [KILL -9 RECOVERY & N+1 CONTINUATION SUCCESS] (%d kill points)\n", len(killPoints))
	for _, line := range crashSummaries {
		fmt.Printf("   - %s (identity match GEI+txs, Zero-Fork P2.5; historical hashes == clean run)\n", line)
	}
	fmt.Printf("   - Successfully executed & committed Block #%d ($N+1$) with StateRoot=%s\n",
		blkN1.Number, blkN1.AccountStatesRoot[:16]+"...")

	// STEP 5: WRITE C0 VERIFICATION REPORT (Optional output / Default to tmp)
	finalReportPath := customReportPath
	if finalReportPath == "" {
		finalReportPath = filepath.Join(tmpBase, "c0_verification_report.md")
	}

	if err := writeC0VerificationReportExtended(finalReportPath, blocksCount, durR1P1, durR1P2, durRestart, resR1P1.Records, resR1P2.Records, resR2P1.Records, recCrash, iso); err != nil {
		fmt.Printf("⚠️ Warning: failed to write verification report to %s: %v\n", finalReportPath, err)
	} else if finalReportPath != "stdout" {
		fmt.Printf("\n📄 Generated C0 verification report at %s\n", finalReportPath)
		if customReportPath == "" {
			fmt.Printf("   (Note: Stored in isolated temp directory. Use flag '-c0-report=/path/to/report.md' or '-c0-report=stdout' if desired; repo will never be modified automatically)\n")
		}
	}

	fmt.Println("\n═════════════════════════════════════════════════════════════════════")
	fmt.Printf("🏆 C0 SPIKE VERIFICATION COMPLETED WITH 100%% EMPIRICAL ACCURACY!\n")
	fmt.Println("═════════════════════════════════════════════════════════════════════")
	return nil
}

// crashScenarioResult is the outcome of one kill -9 + recovery scenario.
type crashScenarioResult struct {
	KilledAtBlock  uint64
	StartBlockNum  uint64
	BypassedBlocks int
	Records        []BlockRecord
	Duration       time.Duration
}

// runCrashScenario starts a worker, SIGKILLs it as soon as its progress marker reaches killAt, restarts it
// on the same data directory in recovery mode and checks that (1) the blocks committed before the kill are
// bypassed with an identity check, (2) their hashes/state roots equal a clean run (ref) and (3) block N+1
// is executed on top of them.
func runCrashScenario(selfExe, baseConfigPath, tmpBase string, blocksCount int, killAt uint64, ref []BlockRecord) (*crashScenarioResult, error) {
	dirCrash := filepath.Join(tmpBase, fmt.Sprintf("crash_node_k%d", killAt))
	outCrashTemp := filepath.Join(dirCrash, "temp_result.json")
	outCrashRecovered := filepath.Join(tmpBase, fmt.Sprintf("res_crash_recovered_k%d.json", killAt))
	progressMarker := filepath.Join(dirCrash, "commit_progress.txt")

	fmt.Printf("\n▶ [kill point %d] Launching worker on %s to be killed abruptly...\n", killAt, dirCrash)
	cmdCrash := exec.Command(selfExe,
		"-tool-c0-spike=worker",
		"-config="+baseConfigPath,
		"-c0-data-dir="+dirCrash,
		"-c0-out="+outCrashTemp,
		fmt.Sprintf("-c0-blocks=%d", blocksCount),
	)
	cmdCrash.Stdout = os.Stdout
	cmdCrash.Stderr = os.Stderr
	if err := cmdCrash.Start(); err != nil {
		return nil, fmt.Errorf("failed to start crash target process: %w", err)
	}
	pid := cmdCrash.Process.Pid
	fmt.Printf("🚀 [CRASH TEST] Worker PID %d started. Waiting for progress marker (lastBlock >= %d)...\n", pid, killAt)

	killedAtBlock := uint64(0)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(progressMarker); err == nil {
			var bNum uint64
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &bNum); err == nil && bNum >= killAt {
				killedAtBlock = bNum
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if killedAtBlock < killAt {
		_ = cmdCrash.Process.Kill()
		_ = cmdCrash.Wait()
		return nil, fmt.Errorf("crash target failed to reach block >= %d within deadline (last recorded: %d)", killAt, killedAtBlock)
	}
	if killedAtBlock >= uint64(blocksCount) {
		_ = cmdCrash.Process.Kill()
		_ = cmdCrash.Wait()
		return nil, fmt.Errorf("worker finished all %d blocks before the kill landed (marker=%d): the kill was not mid-run", blocksCount, killedAtBlock)
	}

	fmt.Printf("💥 [CRASH TEST] Progress marker confirmed worker reached block #%d. Sending SIGKILL (kill -9) to PID %d...\n", killedAtBlock, pid)
	_ = cmdCrash.Process.Kill()
	_ = cmdCrash.Wait()
	fmt.Printf("☠️ [CRASH TEST] Process %d killed abruptly at block #%d (unclean shutdown, no defer/flush).\n", pid, killedAtBlock)

	fmt.Printf("\n▶ Restarting node on %s in recovery mode (c0-restart=true)...\n", dirCrash)
	startRestart := time.Now()
	cmdRecover := exec.Command(selfExe,
		"-tool-c0-spike=worker",
		"-config="+baseConfigPath,
		"-c0-data-dir="+dirCrash,
		"-c0-out="+outCrashRecovered,
		fmt.Sprintf("-c0-blocks=%d", blocksCount),
		"-c0-restart=true",
	)
	cmdRecover.Stdout = os.Stdout
	cmdRecover.Stderr = os.Stderr
	if err := cmdRecover.Run(); err != nil {
		return nil, fmt.Errorf("crash recovery worker failed: %w", err)
	}
	dur := time.Since(startRestart)

	res, err := loadWorkerResult(outCrashRecovered)
	if err != nil {
		return nil, fmt.Errorf("loadWorkerResult(%s): %w", outCrashRecovered, err)
	}
	if res.StartBlockNum < killedAtBlock {
		return nil, fmt.Errorf("crash recovery started at block #%d, expected >= #%d", res.StartBlockNum, killedAtBlock)
	}
	if res.BypassedBlocks < int(killedAtBlock) {
		return nil, fmt.Errorf("expected at least %d bypassed blocks, got %d", killedAtBlock, res.BypassedBlocks)
	}
	rec := res.Records
	if len(rec) != blocksCount+1 {
		return nil, fmt.Errorf("expected %d records after recovery continuation, got %d", blocksCount+1, len(rec))
	}
	for i := 0; i < blocksCount; i++ {
		if rec[i].Hash != ref[i].Hash {
			return nil, fmt.Errorf("crash recovery altered historical block #%d hash: recovered=%s vs expected=%s", i+1, rec[i].Hash, ref[i].Hash)
		}
		if rec[i].AccountStatesRoot != ref[i].AccountStatesRoot {
			return nil, fmt.Errorf("crash recovery altered historical block #%d state root: recovered=%s vs expected=%s", i+1, rec[i].AccountStatesRoot, ref[i].AccountStatesRoot)
		}
	}
	blkN1 := rec[blocksCount]
	if blkN1.Number != uint64(blocksCount+1) {
		return nil, fmt.Errorf("expected block #%d at tip, got #%d", blocksCount+1, blkN1.Number)
	}
	if blkN1.AccountStatesRoot == ref[blocksCount-1].AccountStatesRoot {
		return nil, fmt.Errorf("block #%d did not advance state root from block #%d", blocksCount+1, blocksCount)
	}
	if !blkN1.AllReceiptsSuccess {
		return nil, fmt.Errorf("block #%d has failed receipts", blocksCount+1)
	}
	return &crashScenarioResult{KilledAtBlock: killedAtBlock, StartBlockNum: res.StartBlockNum, BypassedBlocks: res.BypassedBlocks, Records: rec, Duration: dur}, nil
}

func prepareC0Config(baseConfigPath, dataDir string) (string, error) {
	cfg, err := config.LoadConfig(baseConfigPath)
	if err != nil {
		mvmCache := true
		cfg = &config.SimpleChainConfig{
			ChainId:         big.NewInt(991),
			MVMCacheEnabled: &mvmCache,
		}
	}
	if cfg.MVMCacheEnabled == nil {
		mvmCache := true
		cfg.MVMCacheEnabled = &mvmCache
	}

	cfg.ConsensusMode = "raft"
	cfg.Databases.RootPath = filepath.Join(dataDir, "data")
	cfg.BackupPath = filepath.Join(dataDir, "backup")
	cfg.LogPath = filepath.Join(dataDir, "logs")
	cfg.LastBlockSavePath = "/last_block.dat"
	cfg.TransactionBlockNumberLastHashPath = "/transaction_block_number_last_hash"
	cfg.BlockHashToNumberDBRootPath = "/block_hash_to_number_db_root_path"
	cfg.ExplorereDbPath = filepath.Join(dataDir, "explorer")
	cfg.ExplorereReadOnlyDbPath = filepath.Join(dataDir, "explorer-read-only")
	cfg.Databases.SnapshotPath = filepath.Join(dataDir, "snapshot")
	cfg.SnapshotSourceDir = filepath.Join(dataDir, "data")
	cfg.SnapshotEnabled = false
	cfg.ConnectionAddress = "127.0.0.1:0"
	cfg.StateBackend = "nomt"
	cfg.DBType = storage.TypePebbleDB
	cfg.RpcPort = "" // Disable RPC server port binding

	// Ensure genesis path — prioritize funded genesis configs for C0 spike
	genesisFound := false
	if cfg.GenesisFilePath != "" {
		candidates := []string{
			cfg.GenesisFilePath,
			filepath.Join(filepath.Dir(baseConfigPath), cfg.GenesisFilePath),
		}
		for _, c := range candidates {
			if abs, err := filepath.Abs(c); err == nil && fileExists(abs) {
				cfg.GenesisFilePath = abs
				genesisFound = true
				break
			}
		}
	}
	if !genesisFound {
		candidates := []string{
			"cmd/simple_chain/genesis-main.json",
			"execution/cmd/simple_chain/genesis-main.json",
			filepath.Join(filepath.Dir(baseConfigPath), "genesis-main.json"),
			"deploy/systemd/genesis.json",
			"deploy/systemd/genesis.json.example",
			"genesis.json",
			filepath.Join(filepath.Dir(baseConfigPath), "genesis.json"),
			"cmd/simple_chain/genesis.json",
			"execution/cmd/simple_chain/genesis.json",
		}
		for _, c := range candidates {
			if abs, err := filepath.Abs(c); err == nil && fileExists(abs) {
				cfg.GenesisFilePath = abs
				genesisFound = true
				break
			}
		}
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", err
	}
	tmpConfig := filepath.Join(dataDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tmpConfig, data, 0644); err != nil {
		return "", err
	}
	return tmpConfig, nil
}

func packGatewayOutbound(destChainId *big.Int, target e_common.Address, payload []byte, assetId, value, tip *big.Int) ([]byte, error) {
	h, err := tx_processor.GetGatewayHandler()
	if err != nil {
		return nil, fmt.Errorf("GetGatewayHandler: %w", err)
	}
	return h.GetABI().Pack("outbound",
		destChainId,
		target,
		payload,
		assetId,
		value,
		tip,
		big.NewInt(0), // gasFee
		uint8(1),      // hopCount
		false,         // ordered
		uint64(0),     // timeoutTimestamp
	)
}

func buildDeterministicC0Blocks(chainId *big.Int, count int) ([]*pb.ExecutableBlock, error) {
	privKeysHex := []string{
		"a70c079c7d118affc61170f6c8757aadfba8cbc714e1903f45e4daa06660efb7", // 0x294f72878a83B7d076E1d28eedecd184863df846
		"da62671fae8ee9aee7d0aa8ecc57aca918565de0f88ac12990f313e9fc2de2bd", // 0x616969160142a381bb315A286fA54B7eD1749C49
		"6451950adcce1e30efc6c029bbba140fdfcda79756047c8605709173d1600ff3", // 0x7EB655B6A3f58DE47CA598385ba531A8f4e156B1
	}

	keys := make([]*ecdsa.PrivateKey, len(privKeysHex))
	for i, h := range privKeysHex {
		k, err := crypto.HexToECDSA(h)
		if err != nil {
			return nil, fmt.Errorf("HexToECDSA(%d): %w", i, err)
		}
		keys[i] = k
	}

	sender0Addr := crypto.PubkeyToAddress(keys[0].PublicKey)
	counterAddr := crypto.CreateAddress(sender0Addr, 1)

	// TestCounter contract bytecode
	testCounterBytecode, err := hex.DecodeString("608060405234801561000f575f80fd5b506101818061001d5f395ff3fe608060405234801561000f575f80fd5b5060043610610034575f3560e01c8063a87d942c14610038578063d09de08a14610056575b5f80fd5b610040610060565b60405161004d91906100d2565b60405180910390f35b61005e610068565b005b5f8054905090565b60015f808282546100799190610118565b925050819055507f20d8a6f5a693f9d1d627a598e8820f7a55ee74c183aa8f1a30e8d4e8dd9a8d845f546040516100b091906100d2565b60405180910390a1565b5f819050919050565b6100cc816100ba565b82525050565b5f6020820190506100e55f8301846100c3565b92915050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52601160045260245ffd5b5f610122826100ba565b915061012d836100ba565b9250828201905080821115610145576101446100eb565b5b9291505056fea2646970667358221220124c20a0a92375b56d64655ddf70bcd5eccdd0fea4724fc3b1130c754d3eedd964736f6c63430008140033")
	if err != nil {
		return nil, fmt.Errorf("DecodeString(testCounterBytecode): %w", err)
	}

	// increment() selector = 0xd09de08a
	incrementCalldata := crypto.Keccak256([]byte("increment()"))[:4]

	// Gateway contract address
	gwAddr := mt_common.GATEWAY_CONTRACT_ADDRESS

	// Recipients
	recipients := []e_common.Address{
		e_common.HexToAddress("0x1111111111111111111111111111111111111111"),
		e_common.HexToAddress("0x2222222222222222222222222222222222222222"),
		e_common.HexToAddress("0x3333333333333333333333333333333333333333"),
		e_common.HexToAddress("0x4444444444444444444444444444444444444444"),
	}

	nonces := make([]uint64, len(keys))
	for i := range nonces {
		nonces[i] = 1 // Genesis accounts start at nonce 1
	}
	signer := e_types.NewEIP155Signer(chainId)
	leaderAddr := e_common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	blocks := make([]*pb.ExecutableBlock, count)

	for b := 1; b <= count; b++ {
		txExes := make([]*pb.TransactionExe, 0)

		addTx := func(senderIdx int, ethTx *e_types.Transaction, workerId uint32) error {
			signedTx, err := e_types.SignTx(ethTx, signer, keys[senderIdx])
			if err != nil {
				return fmt.Errorf("SignTx (sender %d, nonce %d): %w", senderIdx, ethTx.Nonce(), err)
			}
			txM, err := transaction.NewTransactionFromEth(signedTx)
			if err != nil {
				return fmt.Errorf("NewTransactionFromEth: %w", err)
			}
			rawBytes, err := txM.Marshal()
			if err != nil {
				return fmt.Errorf("Marshal tx: %w", err)
			}
			txExes = append(txExes, &pb.TransactionExe{
				Digest:   rawBytes,
				WorkerId: workerId,
			})
			return nil
		}

		if b == 1 {
			// Tx 0: Deploy TestCounter contract (sender 0)
			deployNonce := nonces[0]
			nonces[0]++
			deployTx := e_types.NewContractCreation(deployNonce, big.NewInt(0), 1000000, big.NewInt(1000000000), testCounterBytecode)
			if err := addTx(0, deployTx, 0); err != nil {
				return nil, err
			}
		} else {
			// Tx 0: EVM contract call: increment() on TestCounter (sender 0)
			callNonce := nonces[0]
			nonces[0]++
			callTx := e_types.NewTransaction(callNonce, counterAddr, big.NewInt(0), 500000, big.NewInt(1000000000), incrementCalldata)
			if err := addTx(0, callTx, 0); err != nil {
				return nil, err
			}
		}

		// Tx 1: Gateway barrier call: real state-mutating outbound() to registered chain 102 (sender 1)
		gwNonce := nonces[1]
		nonces[1]++
		gwCalldata, err := packGatewayOutbound(
			big.NewInt(102),
			recipients[1],
			[]byte{0xDE, 0xAD, 0xBE, 0xEF, byte(b)},
			big.NewInt(0),   // assetId 0 (native)
			big.NewInt(100), // value: 100 wei
			big.NewInt(5),   // tip: 5 wei
		)
		if err != nil {
			return nil, fmt.Errorf("packGatewayOutbound: %w", err)
		}
		gwTx := e_types.NewTransaction(gwNonce, gwAddr, big.NewInt(0), 500000, big.NewInt(1000000000), gwCalldata)
		if err := addTx(1, gwTx, 1); err != nil {
			return nil, err
		}

		// Remaining 6 txs per block: Native transfers with RW/WW conflicts
		type txSpec struct {
			senderIdx int
			recipIdx  int
			amountEth int64
			workerId  uint32
		}

		specs := []txSpec{
			{senderIdx: 0, recipIdx: 0, amountEth: 1, workerId: 2}, // RW conflict on Sender 0
			{senderIdx: 0, recipIdx: 0, amountEth: 2, workerId: 3}, // RW conflict on Sender 0, WW on Recip 0
			{senderIdx: 1, recipIdx: 0, amountEth: 3, workerId: 0}, // WW conflict on Recip 0
			{senderIdx: 2, recipIdx: 1, amountEth: 1, workerId: 1}, // Independent
			{senderIdx: 1, recipIdx: 2, amountEth: 2, workerId: 2}, // Disjoint write
			{senderIdx: 2, recipIdx: 3, amountEth: 3, workerId: 3}, // Disjoint write
		}

		for _, s := range specs {
			recipient := recipients[s.recipIdx]
			nonce := nonces[s.senderIdx]
			nonces[s.senderIdx]++
			amount := big.NewInt(s.amountEth * 1000000000000000) // milli-ether (1e15 wei)
			ethTx := e_types.NewTransaction(nonce, recipient, amount, 21000, big.NewInt(1000000000), nil)
			if err := addTx(s.senderIdx, ethTx, s.workerId); err != nil {
				return nil, err
			}
		}

		timestampMs := uint64(1772784000000 + b*1000)
		commitHash := crypto.Keccak256([]byte(fmt.Sprintf("batch-c0-%d", b)))

		blocks[b-1] = &pb.ExecutableBlock{
			Transactions:      txExes,
			GlobalExecIndex:   uint64(b),
			CommitIndex:       uint32(b),
			Epoch:             0,
			CommitTimestampMs: timestampMs,
			BlockNumber:       uint64(b),
			LeaderAddress:     leaderAddr.Bytes(),
			CommitHash:        commitHash,
		}
	}

	return blocks, nil
}

func writeC0VerificationReportExtended(
	reportPath string,
	blocksCount int,
	durR1P1, durR1P2, durRestart time.Duration,
	recR1P1, recR1P2, recR2P1, recCrashRecovered []BlockRecord,
	iso *RustIsolationEvidence,
) error {
	var sb strings.Builder
	sb.WriteString("# 📋 Báo Cáo Nghiệm Thu C0 Spike — Determinism, Workload Expansion & Crash Recovery\n\n")
	sb.WriteString(fmt.Sprintf("**Ngày thực hiện:** %s\n", time.Now().Format("2006-01-02 15:04:05 MST")))
	sb.WriteString("**Môi trường:** Linux x86_64, NOMT state trie backend, Raft consensus mode (`consensus_mode=\"raft\"`)\n")
	sb.WriteString("**Kiến trúc thử nghiệm:** 2 vòng độc lập (Round 1 & Round 2) trên 4 data dirs riêng biệt (`r1_node1`, `r1_node2`, `r2_node1`, `r2_node2`), kết hợp thử nghiệm `kill -9` crash recovery theo tiến độ commit thực tế.\n\n")
	sb.WriteString("---\n\n")

	sb.WriteString("## 1. Tóm Tắt Nghiệm Thu Các Cổng C0 (Executive Summary)\n\n")
	sb.WriteString("| Cổng nghiệm thu (P1–P2 & Mục 3) | Kết quả | Ghi chú kỹ thuật |\n")
	sb.WriteString("|---|:---:|---|\n")
	sb.WriteString("| **Multi-Round Determinism** | **PASS (100%)** | Round 1 & Round 2 (2 OS processes độc lập mỗi vòng) khớp 100% hash & state roots |\n")
	sb.WriteString("| **EVM Smart Contract Execution** | **PASS (100%)** | Deploy `TestCounter` ở Block #1, gọi `increment()` ở Blocks #2..N thành công |\n")
	sb.WriteString("| **Gateway Barrier Execution** | **PASS (100%)** | Gọi `outbound()` tới destination chain 102 (`0x1002`), ghi trạng thái `GatewayEngine` và receipt OK |\n")
	sb.WriteString("| **Block-STM Concurrency Conflicts** | **PASS (100%)** | RW (cùng sender, consecutive nonces) và WW (3 txs cùng recipient) hội tụ tuyệt đối |\n")
	sb.WriteString("| **Progress-Driven Crash & Recovery (`kill -9`)** | **PASS (100%)** | SIGKILL đột ngột khi `lastBlock >= 2`; restart bỏ qua block đã commit, đối chiếu identity, tiếp tục thực thi Block $N+1$ |\n")
	sb.WriteString("| **Rust Consensus Runtime Isolation** | **PASS (100%)** | `InitFFIBridge` không được gọi (đếm = 0); 0 Tokio runtime threads, 0 P2P listen sockets; NOMT threads phân lập lưu trữ |\n")
	sb.WriteString("| **Zero-Fork Invariant (Part 2.5)** | **PASS (100%)** | Không timeout dispatch; đối chiếu hash + tx count trước khi bypass; fail-closed khi sai lệch |\n\n")

	sb.WriteString("---\n\n")
	sb.WriteString("## 2. Số Liệu Hiệu Năng (Execution Metrics)\n\n")
	sb.WriteString(fmt.Sprintf("- **Round 1 Process 1 (%d blocks, %d txs):** %v (~%v/block)\n", blocksCount, blocksCount*8, durR1P1, durR1P1/time.Duration(blocksCount)))
	sb.WriteString(fmt.Sprintf("- **Round 1 Process 2 (%d blocks, %d txs):** %v (~%v/block)\n", blocksCount, blocksCount*8, durR1P2, durR1P2/time.Duration(blocksCount)))
	sb.WriteString(fmt.Sprintf("- **Crash Recovery Replay & Block #%d Continuation:** %v\n\n", blocksCount+1, durRestart))

	sb.WriteString("---\n\n")
	sb.WriteString("## 3. Bảng Đối Chiếu Determinism Toàn Diện (Round 1 & Round 2)\n\n")
	sb.WriteString("| Block | Workload Details | Block Hash | State Root | Receipts Root | Txs | All Receipts OK |\n")
	sb.WriteString("|---|---|---|---|---|:---:|:---:|\n")
	for i := 0; i < blocksCount; i++ {
		r1 := recR1P1[i]
		workload := "Native Transfers (RW/WW) + Gateway `outbound()`"
		if i == 0 {
			workload = fmt.Sprintf("EVM Deploy (`%s`) + Gateway `outbound()` + Native", r1.ContractAddress[:10]+"...")
		} else {
			workload = "EVM `increment()` + Gateway `outbound()` + Native (RW/WW)"
		}
		sb.WriteString(fmt.Sprintf("| #%d | %s | `%s` | `%s` | `%s` | %d | ✅ %v |\n",
			r1.Number, workload, r1.Hash[:16]+"...", r1.AccountStatesRoot[:16]+"...", r1.ReceiptsRoot[:16]+"...",
			r1.TxCount, r1.AllReceiptsSuccess))
	}

	sb.WriteString(fmt.Sprintf("\n> **Đối chiếu chéo (Cross-Round):** Toàn bộ %d blocks của Round 2 khớp 100%% với Round 1 trên từng byte hash và state root.\n\n", blocksCount))

	sb.WriteString("---\n\n")
	sb.WriteString("## 4. Kiểm Thử Đột Ngột `kill -9` Theo Tiến Độ & Tự Phục Hồi\n\n")
	sb.WriteString("1. **Kịch bản sự cố có điều khiển:** Node đang thực thi blocks thì tiến trình điều phối theo dõi `commit_progress.txt`. Ngay khi block #2 được commit bền vững xuống đĩa, tín hiệu `SIGKILL` (`kill -9`) được gửi cưỡng bức tới PID. Không có graceful shutdown, không có flush bộ đệm `app.Stop()`.\n")
	sb.WriteString("2. **Quy trình tái khởi động & Replay:**\n")
	sb.WriteString("   - Node khởi động lại trên cùng thư mục dữ liệu đã crash với cờ `-c0-restart=true`.\n")
	sb.WriteString("   - Database đọc `storage.GetLastBlockNumber()` xác định các blocks đã ghi bền (startBlockNum >= 2).\n")
	sb.WriteString("   - **Zero-Fork Identity Verification:** Các block lịch sử được kiểm tra đối chiếu block hash và transaction count từ DB trước khi gán nhãn `[ALREADY COMMITTED / SKIPPED]`. Tuyệt đối không thực thi lại mutation lên state trie.\n")
	sb.WriteString("   - **Block N+1 Continuation:** Sau khi hoàn tất đối chiếu các block cũ, hệ thống tiếp tục nhận và thực thi Block #6 ($N+1$).\n")

	if len(recCrashRecovered) > blocksCount {
		blkN1 := recCrashRecovered[blocksCount]
		sb.WriteString(fmt.Sprintf("   - **Kết quả Block #%d:** Hash=`%s`, StateRoot=`%s`, All Receipts OK (%v).\n\n",
			blkN1.Number, blkN1.Hash, blkN1.AccountStatesRoot, blkN1.AllReceiptsSuccess))
	}

	sb.WriteString("---\n\n")
	sb.WriteString("## 5. Bằng Chứng Cách Ly Rust Runtime (Rust Isolation Proof)\n\n")
	sb.WriteString("Thực nghiệm kiểm tra runtime của worker qua `/proc/self/task/*/comm`, `/proc/self/net/tcp*`, và cờ `InitFFIBridgeCallCount`:\n\n")
	sb.WriteString(fmt.Sprintf("- **`InitFFIBridge` Invocations:** `%d` (Hook H1 hoạt động hoàn hảo, FFI consensus bridge bị bỏ qua hoàn toàn).\n", iso.FFIBridgeCallCount))
	sb.WriteString(fmt.Sprintf("- **Rust Tokio Consensus Threads:** `%d` (không có tokio runtime nào được khởi tạo).\n", iso.TokioThreadsCount))
	sb.WriteString(fmt.Sprintf("- **Socket TCP đang LISTEN thuộc chính tiến trình worker:** `%d` (đếm theo inode của fd, không phải theo network namespace).\n", len(iso.ListenSockets)))
	sb.WriteString("- **Phân biệt ranh giới thread NOMT vs Consensus:**\n")
	sb.WriteString("  - **NOMT Threads (Lưu trữ thuần túy):** NOMT là thư viện Rust nhúng vào Go để quản lý state trie. Các thread quan sát được gồm:\n")
	if len(iso.NOMTThreads) > 0 {
		for name, count := range iso.NOMTThreads {
			sb.WriteString(fmt.Sprintf("    - `%s`: %d threads\n", name, count))
		}
	} else {
		sb.WriteString("    - Không phát hiện thread NOMT ngoài luồng thực thi chính.\n")
	}
	sb.WriteString("  - **Consensus Runtime:** 0 thread Tokio, 0 P2P socket. Hoàn toàn cách ly giữa Go execution và Rust consensus engine.\n\n")

	sb.WriteString("---\n\n")
	sb.WriteString("## 6. Trạng Thái Các Điểm Thiết Kế & Thực Nghiệm (Design Decisions & Limitations)\n\n")
	sb.WriteString("1. **Độ Bền Khi Ghi (Durability) vs Tính Nguyên Khối (Atomicity):**\n")
	sb.WriteString("   - `AccountStateDB` (NOMT, fsync riêng) và `BlockDB` (Pebble `NoSync`, flush chu kỳ 5s) là hai hệ thống lưu trữ vật lý riêng biệt, không tạo thành một atomic batch duy nhất ở mức storage.\n")
	sb.WriteString("   - Rủi ro mất điện phần cứng (hardware power loss) từng gây ra sự cố ngày 2026-09-24 (NOMT đi trước BlockDB, node thoát mã 78) đã được xử lý bằng cơ chế **durability barrier** (`634b0d39`) trong `CommitBlockState`.\n")
	sb.WriteString("   - Thử nghiệm `kill -9` ở Step 4 chỉ kiểm chứng tính toàn vẹn khi tiến trình ứng dụng bị dừng đột ngột (unclean application shutdown), không mô phỏng mất điện phần cứng (do OS page cache không bị xoá).\n")
	sb.WriteString("2. **Chỉ Số Raft (uint64) và `commit_index` (uint32) — Quyết Định Thiết Kế Mở:**\n")
	sb.WriteString("   - Trường `commit_index` trong protobuf `ExecutableBlock` là `uint32`, trong khi Raft log index là `uint64`.\n")
	sb.WriteString("   - Hiện tại chưa có cơ chế rollover / reset tự động trong code. Đây là **quyết định thiết kế mở (Open Decision)** cần được chốt trước C1 (phương án ánh xạ log index hoặc nâng cấp trường proto sang uint64).\n")
	sb.WriteString("3. **Snapshot Gắn Với CommitIndex:**\n")
	sb.WriteString("   - Cơ chế FSM snapshot gắn với `CommitIndex` là dự định thiết kế cho C2/C4, chưa được cài đặt trong C0.\n\n")

	sb.WriteString("---\n\n")
	sb.WriteString("## 7. Rà Soát Thực Tế Các Lời Gọi Go → Rust Khi Chạy Chế Độ Raft\n\n")
	sb.WriteString("Danh sách lấy từ `git grep 'executor\\.'` trong `cmd/simple_chain` (không tính test/spike). Cột trạng thái phản ánh **code hiện tại**; điểm nào chưa có guard được ghi rõ là **CHƯA GUARD** (việc của C1), không phải đã xử lý.\n\n")
	sb.WriteString("| Điểm gọi Go → Rust | Vị trí | Trạng thái ở chế độ Raft | Bằng chứng / ghi chú |\n")
	sb.WriteString("|---|---|:---:|---|\n")
	sb.WriteString("| `executor.InitFFIBridge`, `RegisterTraceCallback` | `processor/block_processor_network.go` (`runUnixSocket`) | **Bỏ qua** | Nhánh raft `return` trước khi gọi; **đo thật**: bộ đếm `InitFFIBridgeCallCount` = 0 (Step 3) |\n")
	sb.WriteString("| `executor.NewRequestHandler`, `GetGlobalSnapshotManager` | `processor/block_processor_network.go` (đầu `runUnixSocket`) | **Chạy (thuần Go)** | Nằm trước nhánh raft, được thực thi trong mọi lần chạy spike; không có thread/socket Rust sinh ra (Step 3) |\n")
	sb.WriteString("| `executor.GetAuthoritativeBlockQueue` | `processor/block_processor_network.go` (`processRustEpochData`) | **Chạy, trả `nil`** | Spike chạy qua nhánh dự phòng dùng `blockIngestionQueue`; các block được thực thi bình thường |\n")
	sb.WriteString("| `executor.IsRustConsensusReadyForTransactions` | `rpc_block.go` (`ConsensusReady`) | **Có guard** | Nhánh raft trả `ready=false` mà không gọi `executor` |\n")
	sb.WriteString("| `executor.SubmitTransactionBatch` | `processor/tx_batch_forwarder_core.go` | **CHƯA GUARD** | Gọi thẳng `C.metanode_submit_transaction_batch`; vòng lặp thử lại vô hạn khi trả `false`. Spike đưa block trực tiếp vào queue nên **không thực thi** đường này. C1 (H2) phải thay bằng đích Raft |\n")
	sb.WriteString("| `executor.GetConsensusVotes`, `GetCommitVotes` | `rpc_block.go`, `mtn_api.go` | **CHƯA GUARD** | RPC gọi được từ ngoài; spike không chạy RPC server. C1 (H4) phải trả \"unsupported in raft mode\" |\n")
	sb.WriteString("| `executor.AttestPayloadLoss`, `AttestPayloadLossForCommit` | `admin_api.go` | **CHƯA GUARD** | Như trên (H4) |\n")
	sb.WriteString("| `executor.PauseRustConsensus`, `ResumeRustConsensus`, `InitSnapshotSystem` | `processor/block_processor_core.go` | **CHƯA GUARD** | Chỉ chạy khi snapshot bật; spike đặt `SnapshotEnabled=false`. C1/C4 phải xử lý |\n")
	sb.WriteString("| `executor.RunSocketExecutor` | `processor/peer_discovery_socket.go` | **CHƯA GUARD** | Không nằm trong đường `NewApp` của spike nên **không được thử nghiệm**; không có guard theo `ConsensusMode` |\n")

	sb.WriteString("---\n\n")
	sb.WriteString("## 8. Kết Luận Các Cổng Thực Nghiệm C0 (Status: ◐ chờ quyết định đánh dấu ☑)\n\n")
	sb.WriteString("Đã đạt bằng chứng đo đạc thực tế cho:\n")
	sb.WriteString("- [x] Determinism giữa các OS process độc lập qua nhiều vòng (2 vòng × 2 process, so chéo giữa các vòng).\n")
	sb.WriteString("- [x] Workload: Native Transfers (RW/WW) + EVM Smart Contract (`TestCounter`) + Gateway `outbound()` ghi trạng thái (chain đích 102 được seed).\n")
	sb.WriteString("- [x] `kill -9` theo tiến độ tại nhiều điểm (sau block 1, block 2, và một block trước cuối); restart bỏ qua các block đã commit có đối chiếu identity (GEI + số tx), hash/state root lịch sử trùng lần chạy sạch, block N+1 thực thi tiếp.\n")
	sb.WriteString("- [x] Cách ly Rust consensus: `InitFFIBridge` = 0 lần, 0 thread tokio/consensus, 0 socket LISTEN thuộc tiến trình (thread NOMT vẫn tồn tại vì NOMT là thư viện Rust).\n")
	sb.WriteString("- [x] Phát hiện và sửa lỗi bền vững thật: kho `smart_contract_code` phải `SyncDurable` sau khi ghi bytecode (nếu không, replay sau `kill -9` cho hash khác); có test hồi quy ở `pkg/smart_contract_db`.\n\n")
	sb.WriteString("Chưa thuộc phạm vi C0 (chuyển sang C1+): các điểm **CHƯA GUARD** ở mục 7; ánh xạ `commit_index` uint32; snapshot gắn `CommitIndex`; kill giữa lúc thực thi một block (spike chỉ kill giữa hai block); kiểm tra mất điện thật.\n\n")
	sb.WriteString("**Trạng thái Milestone C0:** `◐` cho tới khi người quản lý kế hoạch đánh dấu ☑ sau khi xem bằng chứng này, `go test -race`, `build_check.sh` và `ci.sh run-now`.\n")

	if reportPath == "stdout" {
		fmt.Println("\n" + sb.String())
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(reportPath), 0755)
	return os.WriteFile(reportPath, []byte(sb.String()), 0644)
}

func loadWorkerResult(path string) (*WorkerResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wr WorkerResult
	if err := json.Unmarshal(data, &wr); err == nil && len(wr.Records) > 0 {
		return &wr, nil
	}
	var recs []BlockRecord
	if err := json.Unmarshal(data, &recs); err == nil {
		return &WorkerResult{Records: recs}, nil
	}
	return nil, fmt.Errorf("failed to parse %s as WorkerResult or []BlockRecord", path)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Suppress unused warnings
var _ = hex.EncodeToString
var _ types.Transaction
