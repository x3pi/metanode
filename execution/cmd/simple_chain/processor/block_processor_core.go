// @title processor/block_processor_core.go
// @markdown processor/block_processor_core.go - Core block processor structure and basic functionality
package processor

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/block_signer"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	stake_state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/txsender"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"

	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"path/filepath"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
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

	// Crash-Safety Fix: Synchronously prepared backup data so it can be written to disk
	// before we unblock Rust via DoneChan.
	SerializedBackup []byte
}

// PersistJob holds pipeline commit results for async LevelDB persistence.
// Sent to persistWorker via persistChannel after CommitPipeline() completes.
type PersistJob struct {
	BlockNum      uint64
	AccountResult *account_state_db.PipelineCommitResult
	StakeResult   *stake_state_db.StakePipelineCommitResult
	ReceiptResult *types.ReceiptPipelineResult
	DoneSignal    chan struct{}
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
	txBatchForwarder     *TxBatchForwarder

	// Channel để đảm bảo chỉ một ProcessTransactionsInPool chạy tại một thời điểm
	processingLockChan chan struct{}

	// Pipeline commit: async persistence of trie nodes to LevelDB
	persistChannel chan PersistJob

	// Backup DB Coalescing
	backupDbChannel chan CommitJob
	// GEI Coalescing
	geiUpdateChan chan uint64
    
	forceCommitChan chan struct{}

	// Self-monitoring fields
	processedBlockCount  uint64
	lastRateCheckTime    time.Time
	lastRateCheckCount   uint64
	lastLazyRefreshTime  time.Time

	// ExecutionMutex ensures atomic snapshots by pausing the dataChan loop
	ExecutionMutex sync.RWMutex
}

// PauseExecution acquires the exclusive execution lock to pause block processing (used for atomic snapshots)
func (bp *BlockProcessor) PauseExecution() {
	bp.ExecutionMutex.Lock()
	// CRITICAL FIX: Wait for all background persistence to complete before pausing.
	// This ensures both PebbleDB and NOMT have fully written the in-memory state out to disk,
	// preventing truncated/partial snapshots where metadata.json has a newer StateRoot than the disk.
	bp.WaitForPersistence()
}

// ResumeExecution releases the exclusive execution lock
func (bp *BlockProcessor) ResumeExecution() {
	bp.ExecutionMutex.Unlock()
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
		// Pipeline commit: async persistence channel
		persistChannel: make(chan PersistJob, 100),
		backupDbChannel: make(chan CommitJob, 1),
		geiUpdateChan: make(chan uint64, 1),

		forceCommitChan:  make(chan struct{}, 1),
		lastRateCheckTime: time.Now(),
		lastLazyRefreshTime: time.Now(),
	}

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
		go bp.commitWorker()
		go bp.persistWorker()   // Pipeline commit: async LevelDB persistence
		go bp.backupDbWorker()  // Coalesced BackupDb builder
		go bp.geiWorker()       // Coalesced GEI updates
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
			if bp.chainState != nil && bp.chainState.GetAccountStateDB() != nil {
				return bp.chainState.GetAccountStateDB().Trie().Hash().Hex()
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

		// Wrap the force flush callback to also wait for persistence
		if storageMgr := bp.chainState.GetStorageManager(); storageMgr != nil {
			snapshotManager.SetForceFlushCallback(func() error {
				logger.Info("💾 [SNAPSHOT] Waiting for background persistence queue to drain...")
				bp.WaitForPersistence()
				logger.Info("💾 [SNAPSHOT] Background persistence finished. Flushing to disk...")
				return storageMgr.FlushAll()
			})

			// Override checkpoint callback to also wait for persistence
			snapshotManager.SetCheckpointCallback(func(destPath string) error {
				logger.Info("💾 [SNAPSHOT] Waiting for background persistence before checkpoint...")
				bp.WaitForPersistence()
				logger.Info("💾 [SNAPSHOT] Creating PebbleDB checkpoints...")
				if err := storageMgr.CheckpointAll(destPath); err != nil {
					return err
				}
				
				// CRITICAL FIX: Checkpoint dynamically created MVM Smart Contract storage tries.
				// These are created directly by TrieDatabaseManager and not registered in storageMgr.
				trieDestPath := filepath.Join(destPath, "trie_database")
				if err := trie_database.GetTrieDatabaseManager().CheckpointAll(trieDestPath); err != nil {
					logger.Error("💾 [SNAPSHOT] Failed to checkpoint TrieDatabaseManager: %v", err)
					return fmt.Errorf("failed to checkpoint TrieDatabaseManager: %w", err)
				}
				
				return nil
			})
		}
	}

	// PEER DISCOVERY: Start TCP listener for remote Rust nodes to query this Go Master
	// This enables distributed deployment (nodes on different machines)
	if config.PeerRPCPort > 0 {
		go bp.runPeerDiscoverySocket(config.PeerRPCPort)
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
	bp.lastBlockMutex.Lock()
	defer bp.lastBlockMutex.Unlock()
	value := bp.lastBlock.Load()
	if value == nil {
		return nil
	}
	return value.(types.Block)
}

// SetLastBlock sets the last processed block
func (bp *BlockProcessor) SetLastBlock(lastBlock types.Block) {
	bp.lastBlockMutex.Lock()
	defer bp.lastBlockMutex.Unlock()
	if lastBlock != nil {
		bp.lastBlock.Store(lastBlock)
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
func (bp *BlockProcessor) GetLeaderAddressByIndex(leaderAuthorIndex uint32) common.Address {
	if bp.chainState == nil {
		logger.Warn("⚠️ [LEADER LOOKUP] chainState is nil, falling back to validatorAddress")
		return bp.validatorAddress
	}

	// Get all validators from stake state
	validators, err := bp.chainState.GetStakeStateDB().GetAllValidators()
	if err != nil {
		logger.Warn("⚠️ [LEADER LOOKUP] Failed to get validators: %v, falling back to validatorAddress", err)
		return bp.validatorAddress
	}

	if len(validators) == 0 {
		logger.Warn("⚠️ [LEADER LOOKUP] No validators found, falling back to validatorAddress")
		return bp.validatorAddress
	}

	// CRITICAL: Sort validators by AuthorityKey to match Rust committee ordering
	// Rust uses: sorted_validators.sort_by(|a, b| a.authority_key.cmp(&b.authority_key))
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].AuthorityKey() < validators[j].AuthorityKey()
	})

	// Filter only active validators (not jailed, has stake > 0)
	var activeValidators []common.Address
	for _, v := range validators {
		if v.IsJailed() {
			continue
		}
		stake := v.TotalStakedAmount()
		if stake == nil || stake.Sign() <= 0 {
			continue
		}
		activeValidators = append(activeValidators, v.Address())
	}

	if len(activeValidators) == 0 {
		// CRITICAL: No active validators is fatal
		logger.Error("🚨 [FATAL] No active validators found! Cannot determine leader.")
		logger.Error("🚨 [FATAL] This indicates consensus corruption. System cannot continue safely.")
		logger.Fatal("FORK-SAFETY: No active validators - cannot determine leader")
	}

	// Lookup by index with DETERMINISTIC FALLBACK (not node-specific address)
	if int(leaderAuthorIndex) >= len(activeValidators) {
		// DETERMINISTIC FALLBACK: Use modulo to wrap index
		// This ensures ALL nodes pick the SAME fallback leader
		safeIndex := int(leaderAuthorIndex) % len(activeValidators)
		logger.Warn("⚠️ [LEADER LOOKUP] leaderAuthorIndex=%d out of range (active=%d), using deterministic fallback index=%d",
			leaderAuthorIndex, len(activeValidators), safeIndex)
		return activeValidators[safeIndex]
	}

	leaderAddr := activeValidators[leaderAuthorIndex]
	logger.Debug("✅ [LEADER LOOKUP] Found leader address for index %d: %s", leaderAuthorIndex, leaderAddr.Hex())
	return leaderAddr
}

// ═══════════════════════════════════════════════════════════════════════════════
// GetLeaderAddress - PRIMARY ENTRY POINT FOR LEADER LOOKUP
// ═══════════════════════════════════════════════════════════════════════════════
// RUST-DRIVEN: Rust MUST provide valid 20-byte leader_address
// Go only falls back to index lookup as SAFETY NET, not primary mechanism
// ═══════════════════════════════════════════════════════════════════════════════
func (bp *BlockProcessor) GetLeaderAddress(leaderAddress []byte, leaderAuthorIndex uint32) common.Address {
	// PREFERRED PATH: Rust provided valid 20-byte Ethereum address
	if len(leaderAddress) == 20 {
		addr := common.BytesToAddress(leaderAddress)
		logger.Debug("✅ [LEADER] Using direct address from Rust: %s", addr.Hex())
		return addr
	}

	// SAFETY NET: Rust did not provide valid address
	// This should be RARE with fixed Rust code
	// Use deterministic index lookup as fallback
	if len(leaderAddress) > 0 {
		logger.Warn("⚠️ [LEADER] Rust sent leader_address with invalid length %d (expected 20). Using index fallback.", len(leaderAddress))
	} else {
		logger.Warn("⚠️ [LEADER] Rust sent EMPTY leader_address. Using index fallback. This may indicate Rust bug!")
	}

	// Fallback to deterministic index lookup
	// GetLeaderAddressByIndex now uses modulo for deterministic fallback
	return bp.GetLeaderAddressByIndex(leaderAuthorIndex)
}

// WaitForPersistence blocks until all pending async persistence jobs are processed.
// This ensures that memory buffers and trie nodes are fully written out before snapshots.
func (bp *BlockProcessor) WaitForPersistence() {
	doneChan := make(chan struct{})
	bp.persistChannel <- PersistJob{
		DoneSignal: doneChan,
	}
	<-doneChan
}

// StopWait safely drains all background worker channels before shutting down.
// It sequences flush signals through the pipeline (commit -> broadcast -> persist)
// to guarantee all in-flight blocks are fully processed and saved to disk.
func (bp *BlockProcessor) StopWait() {
	logger.Info("🛑 [SHUTDOWN] Draining BlockProcessor pipeline channels sequentially...")

	timeout := time.After(12 * time.Second)
	done := make(chan struct{})

	go func() {
		// 1. Drain Commit Worker
		commitDone := make(chan struct{})
		bp.commitChannel <- CommitJob{DoneChan: commitDone}
		<-commitDone
		logger.Debug("✅ [SHUTDOWN] commitWorker drained.")



		// 3. Drain Persist Worker
		bp.WaitForPersistence()
		logger.Debug("✅ [SHUTDOWN] persistWorker drained.")

		close(done)
	}()

	select {
	case <-done:
		logger.Info("✅ [SHUTDOWN] BlockProcessor channels perfectly drained. Safe to flush.")
	case <-timeout:
		logger.Warn("⚠️  [SHUTDOWN] Timed out waiting for BlockProcessor channels to drain. Data loss may occur.")
	}
}

// StartBackgroundWorkers initializes the essential background goroutines that persist
// blocks to disk, create backups for syncing nodes, and finalize state.
// These MUST be running regardless of Single/Multi node mode.
func (bp *BlockProcessor) StartBackgroundWorkers() {
	go bp.commitWorker()
	go bp.persistWorker()
	logger.Info("✅ Started background persistence workers (commit, persist)")
}

// TxsProcessor2 is an adapter to start the TxBatchForwarder (Phase 7 Refactoring)
func (bp *BlockProcessor) TxsProcessor2() {
	if bp.txBatchForwarder != nil {
		bp.txBatchForwarder.StartForwardingLoop()
	}
}


