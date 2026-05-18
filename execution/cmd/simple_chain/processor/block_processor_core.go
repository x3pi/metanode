// @title processor/block_processor_core.go
// @markdown processor/block_processor_core.go - Core block processor structure and basic functionality
package processor

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/block_signer"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/txsender"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// State represents the processor state
type State int

const (
	StateNotLook State = iota
	StatePendingLook
	StateLook
)

// CommitJob represents a job for committing a block
type CommitJob struct {
	Block          *block.Block
	ProcessResults *tx_processor.ProcessResult
	Receipts       types.Receipts
	TxDB           *transaction_state_db.TransactionStateDB
	DoneChan       chan struct{}
	// MappingWg is waited on before broadcasting receipts.
	// Ensures async SetTxHashMapBlockNumber goroutine finishes before clients can query TXs.
	MappingWg *sync.WaitGroup
	// TrieBatchSnapshot captures TrieDB collected batches BEFORE async send.
	// Only TrieDB needs snapshotting because it's explicitly reset via ResetCollectedBatches().
	// Without this, next block's CommitAllTrieDatabases() overwrites collectedBatches
	// before commitWorker reads them → Go Sub gets incomplete trie data → "missing trie node".
	TrieBatchSnapshot map[string][]byte

	// Phase 6 FIX: Synchronously captured state DB batches to prevent race condition
	// where next block overwrites singleton caches before commitWorker runs.
	AccountBatch              []byte
	SmartContractBatch        []byte
	SmartContractStorageBatch []byte
	CodeBatchPut              []byte
	MappingBatch              []byte
	StakeBatch                []byte
	BlockBatch                []byte

	// Snapshot Fix: Track the rust consensus commit index
	GlobalExecIndex uint64
	CommitIndex     uint32

	// Crash-Safety Fix: Synchronously prepared backup data so it can be written to disk
	// before we unblock Rust via DoneChan.
	SerializedBackup []byte
}

// PersistJob REMOVED (May 2026): Was a no-op fence struct. PersistAsync runs
// inline in commitToMemoryParallel. WaitForPersistence now drains via
// commitChannel fence + backupDbWg.Wait() directly.

// AsyncGEIUpdate bundles GEI and CommitIndex updates together to ensure they are persisted atomically
type AsyncGEIUpdate struct {
	GlobalExecIndex uint64
	CommitIndex     uint32
}

// BlockProcessor handles block processing operations
type BlockProcessor struct {
	lastBlock atomic.Value

	transactionProcessor *TransactionProcessor
	subscribeProcessor   *SubscribeProcessor

	validatorAddress common.Address

	connectionsManager network.ConnectionsManager
	messageSender      network.MessageSender

	eventSystem *mt_filters.EventSystem
	state       State
	mu          sync.RWMutex
	serviceType p_common.ServiceType
	node        *node.HostNode

	storageManager                   *storage.StorageManager
	chainState                       *blockchain.ChainState
	genesisPath                      string
	config                           *config.SimpleChainConfig
	isSyncCompleted                  atomic.Bool
	ProcessedVirtualTransactionChain chan []byte

	commitChannel  chan CommitJob
	lastBlockMutex sync.Mutex

	indexingChannel chan uint64
	indexingLocks   sync.Map
	inputTxCounter  atomic.Int64
	nextBlockNumber atomic.Uint64

	ProcessedVirtualTxCount atomic.Uint64
	ProcessedInputTxCount   atomic.Uint64
	ProcessedIndexTxCount   atomic.Uint64

	// Kênh cho kiến trúc committer để xử lý block được tạo song song
	createdBlocksChan chan *block.Block // Kênh để nhận các block đã tạo từ các batch

	// Embedded Components (Phase 4 Refactoring)
	*CacheManager
	*BlockBuffers
	*ReceiptTracker
	*ConsensusContext

	txClientMutex sync.RWMutex     // G-H4 FIX: Protects txClient and txSender from data races
	txClient      *txsender.Client // Legacy TCP client (for backward compatibility)

	// Phase 7 Decomposition
	txBatchForwarder *TxBatchForwarder

	// Channel để đảm bảo chỉ một ProcessTransactionsInPool chạy tại một thời điểm
	processingLockChan chan struct{}

	// Backup DB Coalescing
	backupDbChannel chan CommitJob
	backupDbWg      sync.WaitGroup // Track active backupDbWorker tasks
	// GEI Coalescing
	geiUpdateChan chan AsyncGEIUpdate

	forceCommitChan chan struct{}

	// Self-monitoring fields
	processedBlockCount uint64
	lastRateCheckTime   time.Time
	lastRateCheckCount  uint64
	lastLazyRefreshTime time.Time

	// ExecutionMutex ensures atomic snapshots by pausing the dataChan loop
	ExecutionMutex sync.RWMutex

	// ═══════════════════════════════════════════════════════════════
	// SNAPSHOT GATE: Channel-based gate pattern (May 2026 optimization)
	//
	// Replaced sync.RWMutex with atomic.Bool + broadcast channel.
	// The RWMutex was used as a pure gate (RLock→RUnlock immediately
	// with NO critical section), causing unnecessary cache-line
	// contention (~2 atomic ops per call) on the hot path.
	//
	// New pattern:
	//   Hot path:  atomic.Bool check (zero cost when gate is open)
	//   Cold path: channel wait (only during snapshot)
	//   Close:     set flag + swap channel (snapshot goroutine)
	//   Open:      set flag + close old channel (broadcast wakeup)
	// ═══════════════════════════════════════════════════════════════
	snapshotGateOpen atomic.Bool   // true = open (normal), false = closed (snapshot)
	snapshotGateCh   chan struct{} // closed = gate re-opened (broadcast wakeup)
	snapshotGateMu   sync.Mutex    // serializes close/open transitions

	// FORK-SAFETY: Track Rust FFI session GEI baseline.
	// After DAG-wipe + restart, Rust sends commits with GEIs lower than Go's existing
	// lastBlockGEI. The GEI-REGRESSION-GUARD must be bypassed for these legitimate
	// new-session commits. Set to true when a GEI backward jump is detected in
	// processRustEpochData (new Rust session), reset when GEI catches up.
	rustSessionRestarted atomic.Bool

	// ═══════════════════════════════════════════════════════════════
	// LAYER-8: DB Write Lock Isolation
	// Serializes all block write operations (createBlockFromResults)
	// to prevent concurrent writes from processRustEpochData and
	// ProcessBlockData (network sync). Without this, two goroutines
	// could simultaneously modify state DBs causing corrupted roots.
	// ═══════════════════════════════════════════════════════════════
	blockWriteMutex sync.Mutex

	// ═══════════════════════════════════════════════════════════════
	// LAYER-9: Leader Address Persistence
	// Caches the last known leader address per GEI in BackupDB.
	// When Rust DAG-wipe occurs, the leader address is lost since
	// it's computed from DAG state. This cache allows recovery by
	// reading from Go's persisted storage.
	// Key: "leader_addr:<gei>" → 20-byte address
	// ═══════════════════════════════════════════════════════════════
	leaderAddrCache sync.Map // GEI → common.Address (in-memory LRU)
}

// PauseExecution acquires the exclusive execution lock to pause block processing (used for atomic snapshots)
func (bp *BlockProcessor) PauseExecution() {
	logger.Info("🔒 [PAUSE] PauseExecution: ENTER — commitChannel=%d/%d, snapshotGate=%v",
		len(bp.commitChannel), cap(bp.commitChannel), bp.snapshotGateOpen.Load())

	// 1. Gate all NOMT-writing goroutines (ProcessorPool, GenerateBlock).
	//    This MUST come first — it blocks new NOMT sessions from starting,
	//    which is required for CloseForSnapshot() to complete without deadlock.
	bp.closeSnapshotGate()
	logger.Info("🔒 [PAUSE] PauseExecution: snapshotGate CLOSED, waiting for ExecutionMutex.Lock()...")

	// 2. Lock execution mutex (gates network handlers)
	// FORK-SAFETY (May 2026): ALWAYS block until lock is acquired.
	// Proceeding without the lock could cause snapshot to capture inconsistent
	// state → fork on restore. Prefer pending over fork.
	//
	// Diagnostic goroutine logs warnings every 10s if the lock is held for
	// unusually long, providing visibility without breaking safety invariants.
	lockAcquired := make(chan struct{})
	go func() {
		bp.ExecutionMutex.Lock()
		close(lockAcquired)
	}()
	// Diagnostic: log warnings while waiting, but NEVER proceed without lock
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	waitStart := time.Now()
	for {
		select {
		case <-lockAcquired:
			// Normal path — lock acquired
			if d := time.Since(waitStart); d > 1*time.Second {
				logger.Warn("⚠️ [PAUSE-DIAG] PauseExecution: ExecutionMutex.Lock() took %v (slow but safe)", d)
			}
			goto LOCK_ACQUIRED
		case <-ticker.C:
			logger.Warn("🔒 [PAUSE-DIAG] PauseExecution: still waiting for ExecutionMutex.Lock() after %v. "+
				"commitChannel=%d/%d, snapshotGateOpen=%v. "+
				"Blocking until acquired (thà pending không fork).",
				time.Since(waitStart),
				len(bp.commitChannel), cap(bp.commitChannel),
				bp.snapshotGateOpen.Load())
		}
	}
LOCK_ACQUIRED:
	logger.Info("🔒 [PAUSE] PauseExecution: ExecutionMutex.Lock() ACQUIRED")

	// 3. CRITICAL FIX: Wait for all background persistence to complete before pausing.
	// This ensures both PebbleDB and NOMT have fully written the in-memory state out to disk,
	// preventing truncated/partial snapshots where metadata.json has a newer StateRoot than the disk.
	logger.Info("🔒 [PAUSE] PauseExecution: calling WaitForPersistence...")
	bp.WaitForPersistence()
	logger.Info("🔒 [PAUSE] PauseExecution: WaitForPersistence DONE — system fully paused")
}

// ResumeExecution releases the exclusive execution lock
func (bp *BlockProcessor) ResumeExecution() {
	bp.ExecutionMutex.Unlock()
	// Release snapshotGate AFTER ExecutionMutex to maintain lock ordering
	bp.openSnapshotGate()
}

// ═══════════════════════════════════════════════════════════════════════════
// SNAPSHOT GATE METHODS
// ═══════════════════════════════════════════════════════════════════════════

// initSnapshotGate initializes the gate in the open state.
// Must be called once during BlockProcessor construction.
func (bp *BlockProcessor) initSnapshotGate() {
	bp.snapshotGateOpen.Store(true)
	bp.snapshotGateCh = make(chan struct{})
}

// waitSnapshotGate blocks the caller if a snapshot is in progress.
// On the hot path (gate open), this is a single atomic.Bool load — zero contention.
// On the cold path (gate closed), blocks until the snapshot completes.
func (bp *BlockProcessor) waitSnapshotGate() {
	if bp.snapshotGateOpen.Load() {
		return // Fast path: gate open, no contention
	}
	// Slow path: snapshot in progress.
	// Read current channel under lock to avoid race with openSnapshotGate.
	bp.snapshotGateMu.Lock()
	waitCh := bp.snapshotGateCh
	bp.snapshotGateMu.Unlock()

	// Double-check: gate may have reopened between atomic check and lock
	if bp.snapshotGateOpen.Load() {
		return
	}

	logger.Debug("⏳ [SNAPSHOT-GATE] Waiting for snapshot to complete...")
	<-waitCh // Blocks until openSnapshotGate closes this channel
	logger.Debug("✅ [SNAPSHOT-GATE] Gate reopened, resuming")
}

// closeSnapshotGate closes the gate, blocking all hot-path callers.
// Called by PauseExecution before acquiring ExecutionMutex.
func (bp *BlockProcessor) closeSnapshotGate() {
	bp.snapshotGateMu.Lock()
	defer bp.snapshotGateMu.Unlock()

	if !bp.snapshotGateOpen.Load() {
		logger.Warn("⚠️  [SNAPSHOT-GATE] Gate already closed (double-close?)")
		return
	}

	// Create a NEW wait channel for this snapshot cycle.
	// Any goroutine that enters waitSnapshotGate after this point
	// will block on this channel.
	bp.snapshotGateCh = make(chan struct{})
	bp.snapshotGateOpen.Store(false)
	logger.Debug("🔒 [SNAPSHOT-GATE] Gate closed")
}

// openSnapshotGate reopens the gate, unblocking all waiting goroutines.
// Called by ResumeExecution after releasing ExecutionMutex.
func (bp *BlockProcessor) openSnapshotGate() {
	bp.snapshotGateMu.Lock()
	defer bp.snapshotGateMu.Unlock()

	if bp.snapshotGateOpen.Load() {
		logger.Warn("⚠️  [SNAPSHOT-GATE] Gate already open (double-open?)")
		return
	}

	bp.snapshotGateOpen.Store(true)
	// Close the channel → broadcast wakeup to ALL blocked goroutines.
	// This is the Go idiom for a one-shot broadcast: close(ch) unblocks
	// all <-ch receivers simultaneously.
	close(bp.snapshotGateCh)
	logger.Debug("🔓 [SNAPSHOT-GATE] Gate opened")
}

// GetTxClient returns the txClient for transaction forwarding (used by Go Sub to forward transactions to Rust)
func (bp *BlockProcessor) GetTxClient() *txsender.Client {
	bp.txClientMutex.RLock()
	defer bp.txClientMutex.RUnlock()
	return bp.txClient
}

// ConnectionByTypeAndAddress implements the IConnectionManager interface for TransactionProcessor.
func (bp *BlockProcessor) ConnectionByTypeAndAddress(connType int, addr common.Address) network.Connection {
	if bp.connectionsManager != nil {
		return bp.connectionsManager.ConnectionByTypeAndAddress(connType, addr)
	}
	return nil
}

// ConnectionsByType implements the IConnectionManager interface for TransactionProcessor.
func (bp *BlockProcessor) ConnectionsByType(connType int) map[common.Address]network.Connection {
	if bp.connectionsManager != nil {
		return bp.connectionsManager.ConnectionsByType(connType)
	}
	return nil
}

// GetRustTxSocketPath implements the ISystemConfig interface for TransactionProcessor.
func (bp *BlockProcessor) GetRustTxSocketPath() string {
	if bp.config != nil {
		return bp.config.RustTxSocketPath
	}
	return ""
}

// NewBlockProcessor creates a new block processor
func NewBlockProcessor(
	lastBlock types.Block,
	transactionProcessor *TransactionProcessor,
	subscribeProcessor *SubscribeProcessor,
	validatorAddress common.Address,
	connectionsManager network.ConnectionsManager,
	messageSender network.MessageSender,
	eventSystem *mt_filters.EventSystem,
	serviceType p_common.ServiceType,
	node *node.HostNode,
	storageManager *storage.StorageManager,
	chainState *blockchain.ChainState,
	genesisPath string,
	config *config.SimpleChainConfig,
) *BlockProcessor {

	// Thông tin cấu hình
	// MetaNode RPC server chạy trên port = metrics_port + 1000
	// Lấy port từ config hoặc hardcode fallback cho backward compatibility
	var rpcAddress string
	// Thử đoán metrics port từ config nếu có thể (đây là logic heuristic)
	// Hoặc cho phép config thêm RpcMetricsPort.
	// Hiện tại hack tạm: nếu ListenPort là 10000 (Node 0) -> 10100.
	// Nếu ListenPort là 10001 (Node 1) -> Metrics 9101 -> RPC 10101?
	// Node 1 separate port config:
	// Rust Node 1 separate port is 9011 (consensus).
	// Metrics port usually consensus + 100 = 9111.
	// MetaNode RPC port usually metrics + 1000 = 10111.

	// Better approach: Allow config to specify MetaNodeRPCAddress
	// Fallback to default 127.0.0.1:10100
	rpcAddress = "127.0.0.1:10100" // Default for standard system

	// Override if we can detect Node 1 separate config
	if config.MetaNodeRPCAddress != "" {
		rpcAddress = config.MetaNodeRPCAddress
	} else {
		// Simple heuristic based on known ports:
		// Node 1 Separate: Rust 9011 -> Metrics 9111 -> RPC 10111
		// Node 0 Standard: Rust 9000 -> Metrics 9100 -> RPC 10100
		// But Go config doesn't know about Rust ports directly.
		// We will add "meta_node_rpc_address" to config.json.
	}

	poolSize := 10 // Kích thước connection pool

	// Tạo một client để gửi giao dịch đến MetaNode
	// Client sẽ ưu tiên dùng Unix Domain Socket (nhanh hơn), fallback về HTTP nếu UDS không available
	txClient, err := txsender.NewClient(rpcAddress, poolSize)
	if err != nil {
		logger.Error("Không thể tạo transaction client: %v (sẽ retry trong background)", err)
		// LAZY RETRY: Khởi động goroutine để retry kết nối sau khi Rust khởi động
		// Điều này cho phép Go chạy trước Rust
	}

	bp := &BlockProcessor{
		transactionProcessor: transactionProcessor,
		subscribeProcessor:   subscribeProcessor,
		validatorAddress:     validatorAddress,
		connectionsManager:   connectionsManager,
		messageSender:        messageSender,
		eventSystem:          eventSystem,
		state:                StateNotLook,
		serviceType:          serviceType,
		storageManager:       storageManager,
		chainState:           chainState,
		genesisPath:          genesisPath,
		config:               config,
		node:                 node,
		// Giảm buffer size để tránh rò rỉ bộ nhớ
		// Nếu channels đầy, producer sẽ bị block thay vì tích lũy memory
		ProcessedVirtualTransactionChain: make(chan []byte, 10000),    // Giảm từ 100k xuống 10k để giảm memory
		commitChannel:                    make(chan CommitJob, 10000), // Tăng buffer để absorb burst empty blocks
		indexingChannel:                  make(chan uint64, 50000),    // Giữ nguyên 50k (chỉ 8 bytes mỗi entry)
		// Khởi tạo các trường mới cho kiến trúc committer và sub-node buffering
		createdBlocksChan: make(chan *block.Block, 200),
		CacheManager:      NewCacheManager(),
		BlockBuffers:      NewBlockBuffers(),
		ReceiptTracker:    NewReceiptTracker(),
		ConsensusContext:  NewConsensusContext(),
		txClient:          txClient,
		// Khởi tạo channel khóa với buffer size là 1
		processingLockChan: make(chan struct{}, 1),
		backupDbChannel:    make(chan CommitJob, 1000),
		geiUpdateChan:      make(chan AsyncGEIUpdate, 100),

		forceCommitChan:     make(chan struct{}, 64),
		lastRateCheckTime:   time.Now(),
		lastLazyRefreshTime: time.Now(),
	}

	// Initialize snapshot gate (must be done before any goroutine starts)
	bp.initSnapshotGate()

	// Phase 7: Initialize decoupled components
	bp.txBatchForwarder = NewTxBatchForwarder(
		string(serviceType),
		transactionProcessor,
		config,
		chainState,
		connectionsManager,
		messageSender,
	)

	// Initialize BLS block signer for Master nodes
	if serviceType == p_common.ServiceTypeMaster && config.Databases.BLSPrivateKey != "" {
		signer, err := block_signer.NewBlockSigner(config.Databases.BLSPrivateKey)
		if err != nil {
			logger.Warn("⚠️  [BLOCK SIGNER] Failed to initialize block signer: %v (blocks will not be signed)", err)
		} else {
			bp.blockSigner = signer
			logger.Info("🔏 [BLOCK SIGNER] Block signing enabled for Master node")
		}
	}

	// Initialize Sub-node signature verification
	if config.MasterBLSPubKey != "" {
		pubKeyBytes := common.FromHex(config.MasterBLSPubKey)
		if len(pubKeyBytes) > 0 {
			bp.masterBLSPubKey = pubKeyBytes
			logger.Info("🔏 [BLOCK VERIFY] Master BLS public key loaded for signature verification")
		}
	}
	bp.skipSigVerification = config.SkipSignatureVerification

	// Initialize attestation collector for fork detection (persisted in block DB alongside block data)
	bp.attestationCollector = newAttestationCollector(storageManager.GetStorageBlock())

	bp.SetLastBlock(lastBlock)

	if lastBlock != nil {
		bp.nextBlockNumber.Store(lastBlock.Header().BlockNumber() + 1)
	} else {
		bp.nextBlockNumber.Store(1)
	}

	// Khởi chạy goroutine committer để cập nhật state tuần tự
	go bp.stateCommitter()
	// Khởi chạy goroutine cleanup buffer để tránh rò rỉ bộ nhớ
	bp.BlockBuffers.StartCleanupWorkers(func() uint64 {
		return bp.nextBlockNumber.Load()
	})
	// Khởi chạy goroutine monitoring để theo dõi resource usage
	go bp.startResourceMonitoring()
	// Cleanup stale pending receipts to prevent memory leak from disconnected clients
	go bp.cleanupPendingReceipts()
	if bp.storageManager.IsExplorer() {
		go bp.startIndexingProcess()
	}
	if serviceType == p_common.ServiceTypeMaster {
		// go bp.commitWorker()
		go bp.backupDbWorker() // Coalesced BackupDb builder
		go bp.geiWorker()      // Coalesced GEI updates
	}
	go bp.inputTPSWorker()
	go bp.runUnixSocket() // FFI Bridge: Khởi chạy Rust Consensus Engine nhúng via CGo FFI

	// 📸 SNAPSHOT SYSTEM + LOG ROTATION: Luôn khởi tạo
	// InitSnapshotSystem đăng ký block commit callback cho LOG ROTATION (luôn cần)
	// và snapshot (chỉ khi enabled). Nếu snapshot tắt, log rotation vẫn hoạt động.
	logger.Info("📸 [SNAPSHOT-INIT] snapshot_enabled=%v, snapshot_server_port=%d, snapshot_blocks_delay=%d",
		config.SnapshotEnabled, config.SnapshotServerPort, config.SnapshotBlocksDelay)
	snapshotManager := executor.InitSnapshotSystem(config, bp.chainState)
	if snapshotManager != nil {
		logger.Info("📸 [SNAPSHOT-INIT] ✅ Block commit callback registered (log rotation + snapshot)")

		// Wire up the pause and resume callbacks for atomic database snapshots
		snapshotManager.SetPauseCallback(func() { bp.PauseExecution() })
		snapshotManager.SetResumeCallback(func() { bp.ResumeExecution() })

		// Set callback to fetch atomic StateRoot during snapshot
		snapshotManager.SetStateRootCallback(func() string {
			if root, ok := mt_trie.GetNomtHandleRoot("account_state"); ok {
				return root.Hex()
			}
			// Fallback to flat trie if NOMT isn't active
			if bp.chainState != nil && bp.chainState.GetAccountStateDB() != nil {
				return bp.chainState.GetAccountStateDB().Trie().Hash().Hex()
			}
			return ""
		})

		// Set callback to fetch atomic StakeStatesRoot during snapshot
		snapshotManager.SetStakeRootCallback(func() string {
			if root, ok := mt_trie.GetNomtHandleRoot("stake_db"); ok {
				return root.Hex()
			}
			return ""
		})

		// Register Rust Consensus pause/resume callbacks
		snapshotManager.SetRustPauseCallback(func() {
			executor.PauseRustConsensus()
		})
		snapshotManager.SetRustResumeCallback(func() {
			executor.ResumeRustConsensus()
		})

		// Fix: Synchronize snapshot triggering with the asynchronous commit pipeline
		// This guarantees that pebbleDB and fully flush to memory tables and NOMT
		// has synced the current block before snapshot logic begins closing DB handlers
		snapshotManager.SetWaitPersistenceCallback(func() {
			bp.WaitForPersistence()
		})

		// Wrap the force flush callback
		if storageMgr := bp.chainState.GetStorageManager(); storageMgr != nil {
			snapshotManager.SetForceFlushCallback(func() error {
				logger.Info("💾 [SNAPSHOT] Flushing memory tables to disk...")
				return storageMgr.FlushAll()
			})

			// Override checkpoint callback
			snapshotManager.SetCheckpointCallback(func(destPath string) error {
				logger.Info("💾 [SNAPSHOT] Creating PebbleDB checkpoints...")
				if err := storageMgr.CheckpointAll(destPath); err != nil {
					return err
				}

				return nil
			})
		}
	}

	// PEER DISCOVERY: Disabled to prevent port conflict with Rust PeerRpcServer
	// which now listens on config.PeerRPCPort (e.g. 1920x) for HTTP JSON-RPC.
	if config.PeerRPCPort > 0 {
		// go bp.runPeerDiscoverySocket(config.PeerRPCPort)
		logger.Info("🌐 [PEER DISCOVERY] Go Master TCP socket listener is intentionally disabled to avoid port conflict with Rust PeerRpcServer")
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// SYNC-ONLY BLOCK REFRESHER: For SyncOnly Master nodes, Rust sync_node
	// writes blocks to DB via HandleSyncBlocksRequest but never sends commits
	// to the executor socket. So processRustEpochData never runs and
	// bp.lastBlock stays at genesis → RPC eth_blockNumber returns 0.
	// This goroutine periodically syncs bp.lastBlock from DB.
	// ═══════════════════════════════════════════════════════════════════════════
	if bp.serviceType == p_common.ServiceTypeMaster {
		go bp.syncLastBlockFromDB()
	}

	return bp
}

// GetLastBlock returns the last processed block
func (bp *BlockProcessor) GetLastBlock() types.Block {
	value := bp.lastBlock.Load()
	if value == nil {
		return nil
	}
	return value.(types.Block)
}

// GetLastBlockMutex is DEPRECATED — no external callers exist.
// Kept as a no-op to avoid breaking any future code that may reference it.
// TODO: Remove in next major cleanup.
func (bp *BlockProcessor) GetLastBlockMutex() *sync.Mutex {
	return &bp.lastBlockMutex
}

// UpdateLastBlockAndHeader atomically updates both last block and chain state header
func (bp *BlockProcessor) UpdateLastBlockAndHeader(blk types.Block) {
	bp.lastBlockMutex.Lock()
	defer bp.lastBlockMutex.Unlock()

	if blk != nil {
		bp.lastBlock.Store(blk)
		// Update nextBlockNumber
		bp.nextBlockNumber.Store(blk.Header().BlockNumber() + 1)

		// Update header atomically
		headerCopy := blk.Header()
		bp.chainState.SetcurrentBlockHeader(&headerCopy)
	}
}

// SetLastBlock sets the last processed block
func (bp *BlockProcessor) SetLastBlock(lastBlock types.Block) {
	if lastBlock != nil {
		bp.lastBlockMutex.Lock()
		bp.lastBlock.Store(lastBlock)
		bp.lastBlockMutex.Unlock()
	}
}

// GetState returns the current processor state
func (bp *BlockProcessor) GetState() State { bp.mu.RLock(); defer bp.mu.RUnlock(); return bp.state }

// SetState sets the processor state
func (bp *BlockProcessor) SetState(newState State) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bp.state = newState
}

// IsSyncCompleted returns whether sync is completed
func (bp *BlockProcessor) IsSyncCompleted() bool { return bp.isSyncCompleted.Load() }

// SetNode sets the host node
func (bp *BlockProcessor) SetNode(node *node.HostNode) { bp.node = node }

// GetBlockNumber handles block number requests
func (bp *BlockProcessor) GetBlockNumber(request network.Request) error {
	id := request.Message().ID()
	blockNumber := bp.GetLastBlock().Header().BlockNumber()

	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, blockNumber)

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.BlockNumber,
			ID:      id,
		},
		Body: buf,
	})
	return request.Connection().SendMessage(respMsg)
}

// ProcessBlockNumber processes the response to GetBlockNumber requests
func (bp *BlockProcessor) ProcessBlockNumber(request network.Request) error {
	b := request.Message().Body()
	if len(b) != 8 {
		return fmt.Errorf("invalid block number length: expected 8, got %d", len(b))
	}
	masterBlock := binary.BigEndian.Uint64(b)
	storage.UpdateLastBlockNumberFromMaster(masterBlock)
	logger.Debug("✅ [ProcessBlockNumber] Updated master block height to %d", masterBlock)
	return nil
}

// GetLastBlockHeader handles last block header requests
func (bp *BlockProcessor) GetLastBlockHeader(request network.Request) error {
	// 1. Lấy đối tượng proto BlockHeader
	lastBlockHeaderProto := bp.GetLastBlock().Header().Proto()

	// 2. Tự tay mã hóa (marshal) nó thành một mảng byte
	bodyBytes, err := proto.Marshal(lastBlockHeaderProto)
	if err != nil {
		logger.Error("GetLastBlockHeader: Lỗi khi marshal BlockHeader: %v", err)
		return err
	}

	// 3. Gửi mảng byte này đi bằng cách sử dụng `SendBytes`
	// Đây là cách trực tiếp và ít rủi ro nhất
	err = bp.messageSender.SendBytes(request.Connection(), command.GetLastBlockHeader, bodyBytes)
	if err != nil {
		logger.Error("GetLastBlockHeader SendBytes error: %v", err)
	}

	return err
}

// ProcessedVirtualTransaction handles virtual transaction processing
func (bp *BlockProcessor) ProcessedVirtualTransaction(request network.Request) error {
	bp.ProcessedVirtualTransactionChain <- request.Message().Body()
	return nil
}

// StartCleanupOldPendingTransactions starts the cleanup routine for old pending transactions
func (bp *BlockProcessor) StartCleanupOldPendingTransactions() {
	// logger.Info("🔄 [TX FLOW] Starting CleanupOldPendingTransactions goroutine (runs every 1 second, timeout=30s)")
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			bp.CleanupOldPendingTransactions()
		}
	}()
}

// SyncData represents sync data structure
type SyncData struct{ StartingBlock, CurrentBlock, HighestBlock, KnownStates, PulledStates int64 }

// PrepareSyncData prepares sync data from block
func PrepareSyncData(bl types.Block) *SyncData {
	blockNum := int64(bl.Header().BlockNumber())
	return &SyncData{StartingBlock: blockNum, CurrentBlock: blockNum, HighestBlock: blockNum, KnownStates: blockNum, PulledStates: blockNum}
}

// calculateReceiptsRoot calculates receipts root hash
func (bp *BlockProcessor) calculateReceiptsRoot(receiptList []types.Receipt) (types.Receipts, common.Hash) {
	receipts, err := receipt.NewReceipts(bp.storageManager.GetStorageReceipt())
	if err != nil {
		logger.Error("❌ [RECEIPT TRIE] Failed to create receipts trie: %v", err)
		return nil, common.Hash{}
	}
	if len(receiptList) > 0 {
		receipts.AddReceipts(receiptList)
	}
	rcpRoot, err := receipts.IntermediateRoot()
	if err != nil {
		logger.Error("❌ [RECEIPT TRIE] Failed to calculate receipts root: %v", err)
	} else {
		// logger.Info("✅ [RECEIPT TRIE] Receipts root calculated: %s", rcpRoot.Hex())
	}
	return receipts, rcpRoot
}

// GetLeaderAddressByIndex looks up the validator address for the given authority index
// CRITICAL FORK-SAFETY: This ensures all nodes use the same leader address for the same commit
// Validators are sorted by AuthorityKey (matching Rust committee ordering)
// Returns the validator's Ethereum address, or falls back to bp.validatorAddress if lookup fails
func (bp *BlockProcessor) GetLeaderAddress(leaderAddress []byte, leaderAuthorIndex uint32) common.Address {
	// ═══════════════════════════════════════════════════════════════════════════════
	// FORK-SAFETY INVARIANT #1: Immutable Leader
	// Go MUST NEVER calculate the leader address. It must strictly use the address
	// determined by the Rust consensus layer to prevent forks.
	// ═══════════════════════════════════════════════════════════════════════════════
	if len(leaderAddress) != 20 {
		// ═══════════════════════════════════════════════════════════════
		// AVAILABILITY FIX: System transactions (EndOfEpoch) do not carry
		// a leader address because they are consensus-internal messages,
		// not user blocks with a real leader. Crashing here kills the node
		// at every epoch boundary.
		//
		// Fall back to ZERO ADDRESS (deterministic). This is safe because:
		//   1. System TX blocks only contain EndOfEpoch markers
		//   2. All nodes use the same zero address → identical block hash
		//   3. Zero address is never a valid validator, so it's unambiguous
		//
		// CRITICAL: Do NOT use bp.validatorAddress here — each node has a
		// different validator address, which would produce different block
		// hashes → FORK.
		//
		// For non-system-TX blocks, this would indicate a genuine FFI error
		// and should be investigated (hence Error, not Warn).
		// ═══════════════════════════════════════════════════════════════
		logger.Error("⚠️ [LEADER-FALLBACK] Rust sent leader_address with length %d (expected 20). "+
			"Using zero address for deterministic fallback. "+
			"This is expected for EndOfEpoch system transactions.",
			len(leaderAddress))
		return common.Address{}
	}

	addr := common.BytesToAddress(leaderAddress)
	logger.Debug("✅ [LEADER] Using deterministic address from Rust Consensus: %s", addr.Hex())
	return addr
}

// WaitForPersistence blocks until all pending async persistence jobs are processed.
// This ensures that memory buffers and trie nodes are fully written out before snapshots.
//
// FORK-SAFETY (May 2026): Blocks indefinitely — NEVER returns early.
// If commitWorker is slow/stuck, we wait and log diagnostics.
// Returning early would allow snapshot to capture incomplete state → FORK on restore.
// Principle: thà pending không fork.
//
// SIMPLIFICATION (May 2026): Removed persistChannel fence — persistWorker was
// a no-op (PersistAsync runs inline since May 2026). Now only 2 steps:
//  1. Drain commitWorker via fence job
//  2. Wait for background persistence (FlushAll + BackupDb) via backupDbWg
func (bp *BlockProcessor) WaitForPersistence() {
	logger.Info("⏳ [PERSIST] WaitForPersistence: ENTER — commitChannel=%d/%d", len(bp.commitChannel), cap(bp.commitChannel))
	done := make(chan struct{})

	go func() {
		defer close(done)

		// 1. Drain Commit Worker (this also implicitly drains any pending GEI updates
		// that were forwarded to commitChannel before this call)
		logger.Info("⏳ [PERSIST] WaitForPersistence: sending commit fence...")
		commitDone := make(chan struct{})
		bp.commitChannel <- CommitJob{DoneChan: commitDone}
		logger.Info("⏳ [PERSIST] WaitForPersistence: commit fence sent, waiting for commitWorker to process...")
		<-commitDone
		logger.Info("⏳ [PERSIST] WaitForPersistence: commit fence DONE. Starting backupDbWg.Wait()...")

		// 2. Wait for background persistence (FlushAll + BackupDb)
		bp.backupDbWg.Wait()
		logger.Info("⏳ [PERSIST] WaitForPersistence: backupDbWg.Wait() DONE — all persistence complete")
	}()

	// Block indefinitely with diagnostic logging — NEVER return early
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	waitStart := time.Now()
	for {
		select {
		case <-done:
			// All workers drained successfully
			if d := time.Since(waitStart); d > 1*time.Second {
				logger.Warn("⚠️ [PERSIST-DIAG] WaitForPersistence took %v (slow but complete)", d)
			}
			return
		case <-ticker.C:
			logger.Warn("🔒 [PERSIST-DIAG] WaitForPersistence: still draining after %v. "+
				"commitChannel=%d/%d. "+
				"Blocking until complete (thà pending không fork).",
				time.Since(waitStart),
				len(bp.commitChannel), cap(bp.commitChannel))
		}
	}
}

// StopWait safely drains all background worker channels before shutting down.
//
// SIMPLIFICATION (May 2026): Previously sent a redundant commit fence then called
// WaitForPersistence (which sent ANOTHER commit fence). Now just calls
// WaitForPersistence directly — it already drains commitWorker + backupDb.
//
// FORK-SAFETY: Blocks indefinitely — NEVER returns early.
// Returning before drain completes risks data loss → fork on restart.
// Principle: thà pending không fork.
func (bp *BlockProcessor) StopWait() {
	logger.Info("🛑 [SHUTDOWN] Draining BlockProcessor pipeline...")
	waitStart := time.Now()
	bp.WaitForPersistence()
	logger.Info("✅ [SHUTDOWN] BlockProcessor pipeline drained. Safe to flush. (took %v)", time.Since(waitStart))
}

// StartBackgroundWorkers initializes the essential background goroutines that persist
// blocks to disk, create backups for syncing nodes, and finalize state.
// These MUST be running regardless of Single/Multi node mode.
func (bp *BlockProcessor) StartBackgroundWorkers() {
	go bp.commitWorker()
	logger.Info("✅ Started background persistence worker (commit)")
}

// TxsProcessor2 is an adapter to start the TxBatchForwarder (Phase 7 Refactoring)
func (bp *BlockProcessor) TxsProcessor2() {
	if bp.txBatchForwarder != nil {
		bp.txBatchForwarder.StartForwardingLoop()
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// LAYER-9: Leader Address Persistence
// Persist leader addresses to BackupDB so they survive DAG-wipe + restart.
// Without this, Rust must recompute leader addresses from DAG state,
// which is lost after DAG-wipe, causing leader address divergence.
// ═══════════════════════════════════════════════════════════════════════════

// PersistLeaderAddress saves the leader address for a given GEI to BackupDB.
// Called after successful block creation to ensure crash-safe persistence.
func (bp *BlockProcessor) PersistLeaderAddress(gei uint64, addr common.Address) {
	if addr == (common.Address{}) {
		return // Don't persist zero addresses
	}
	// In-memory cache
	bp.leaderAddrCache.Store(gei, addr)

	// Persist to BackupDB
	if bp.storageManager != nil {
		backupDB := bp.storageManager.GetStorageBackupDb()
		if backupDB != nil {
			key := fmt.Sprintf("leader_addr:%d", gei)
			backupDB.Put(common.BytesToHash([]byte(key)).Bytes(), addr.Bytes())
		}
	}
}

// GetPersistedLeaderAddress retrieves a persisted leader address for DAG-wipe recovery.
// Returns (address, true) if found, (zero, false) if not persisted.
func (bp *BlockProcessor) GetPersistedLeaderAddress(gei uint64) (common.Address, bool) {
	// Check in-memory cache first
	if cached, ok := bp.leaderAddrCache.Load(gei); ok {
		return cached.(common.Address), true
	}

	// Fallback to BackupDB
	if bp.storageManager != nil {
		backupDB := bp.storageManager.GetStorageBackupDb()
		if backupDB != nil {
			key := fmt.Sprintf("leader_addr:%d", gei)
			data, err := backupDB.Get(common.BytesToHash([]byte(key)).Bytes())
			if err == nil && len(data) == 20 {
				addr := common.BytesToAddress(data)
				bp.leaderAddrCache.Store(gei, addr) // Promote to cache
				return addr, true
			}
		}
	}
	return common.Address{}, false
}
