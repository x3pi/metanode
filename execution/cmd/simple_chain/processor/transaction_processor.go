package processor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	sharedmemory "github.com/meta-node-blockchain/meta-node/pkg/shared_memory"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

var firstUdsMetricLogged int32

// TransactionResponse represents an internal response struct used during transaction lifecycle.
type TransactionResponse struct {
	Data                interface{}
	Error               error
	Code                int64
	IsBroadCastReceipts bool
}

// Sử dụng sync.Map với key là common.Hash
type TransactionManagerSyncMap struct {
	pending sync.Map
}

// Constructor giữ nguyên
func NewTransactionManagerSyncMap() *TransactionManagerSyncMap {
	return &TransactionManagerSyncMap{}
}

var _ tx_processor.OffChainProcessor = (*TransactionProcessor)(nil)

// readTxRequest holds the context for an async read transaction execution.
type readTxRequest struct {
	conn          network.Connection
	tx            types.Transaction
	msgID         string // Header ID từ request gốc, dùng để match response
	isEstimateGas bool   // Phân biệt eth_call và eth_estimateGas
}

// injectionRequest holds the context for an async transaction injection.
// rawBody is used for zero-copy readLoop: the TCP handler enqueues raw bytes
// without unmarshaling, and the injection worker does the unmarshal.
type injectionRequest struct {
	conn    network.Connection
	tx      types.Transaction
	rawBody []byte // Raw bytes for deferred unmarshal (zero-copy readLoop)
	msgID   string // Header ID từ request gốc, dùng để gửi response thành công
}

type TransactionProcessor struct {
	env           ITransactionProcessorEnvironment
	txManagerMap  *TransactionManagerSyncMap
	messageSender network.MessageSender

	freeFeeAddress map[common.Address]struct{}

	eventSystem *mt_filters.EventSystem

	ProcessResultChan chan tx_processor.ProcessResult

	// SỬ DỤNG: Channel để giao tiếp giữa producer và consumer của giao dịch read-only
	readOnlyResultChan chan tx_processor.ProcessResult

	smartContractStorageDBPath string
	chainId                    string
	storageManager             *storage.StorageManager
	executedMvmIds             sync.Map
	chainState                 *blockchain.ChainState

	// Embedded struct for handling off-chain and read-only execution
	*TxVirtualExecutor

	// Embedded struct for handling mempool tracking and validator grouping
	*TxValidatorPool

	// --- Component Queues & State ---
	// Worker pool để giới hạn số goroutines đồng thời cho backupDeviceKey
	// Tránh rò rỉ bộ nhớ khi có quá nhiều goroutines chạy cùng lúc
	deviceKeySendPool chan struct{}

	// Metrics để theo dõi goroutines và memory
	deviceKeyGoroutineCount     int64 // Atomic counter (số goroutines đang chạy)
	deviceKeyGoroutineCompleted int64 // Atomic counter (số goroutines đã hoàn thành)
	deviceKeyGoroutineDuration  int64 // Atomic counter (tổng nanoseconds của các goroutines đã hoàn thành)

	// Note: readTxRequestChan is now inside TxVirtualExecutor

	// --- TX Injection Queue ---
	// Buffered channel queue for processing incoming TXs from clients.
	// Decouples network handler from slow virtual execution/pool addition.
	injectionQueue chan injectionRequest
}

// NewTransactionProcessor creates a new TransactionProcessor
func NewTransactionProcessor(
	messageSender network.MessageSender,
	transactionPool *transaction_pool.TransactionPool,
	freeFeeAddress map[common.Address]struct{},
	eventSystem *mt_filters.EventSystem,
	smartContractStorageDBPath string,
	chainId string,
	storageManager *storage.StorageManager,
	chainState *blockchain.ChainState,

) *TransactionProcessor {

	// Khởi động logger cho sendToAllConnectionsOfType TPS
	StartSendToAllConnectionsTpsLogger()

	// Đặt giới hạn an toàn cho số request đọc đồng thời
	const maxConcurrentReadTx = 10000
	const maxConcurrentOffChainExecution = 100
	const maxConcurrentDeviceKeySend = 100 // Giới hạn số goroutines đồng thời cho backupDeviceKey
	tp := &TransactionProcessor{
		messageSender:              messageSender,
		freeFeeAddress:             freeFeeAddress,
		chainState:                 chainState,
		eventSystem:                eventSystem,
		ProcessResultChan:          make(chan tx_processor.ProcessResult, 1000),
		smartContractStorageDBPath: smartContractStorageDBPath,
		chainId:                    chainId,
		storageManager:             storageManager,
		executedMvmIds:             sync.Map{},
		txManagerMap:               NewTransactionManagerSyncMap(),

		// Worker pool để giới hạn goroutines cho backupDeviceKey (tránh rò rỉ bộ nhớ)
		deviceKeySendPool:           make(chan struct{}, maxConcurrentDeviceKeySend),
		deviceKeyGoroutineCount:     0,
		deviceKeyGoroutineCompleted: 0,
		deviceKeyGoroutineDuration:  0,
		// TX injection queue: buffered channel (from constants) + workers
		injectionQueue: make(chan injectionRequest, InjectionQueueSize),
	}

	// Initialize the embedded TxVirtualExecutor
	tp.TxVirtualExecutor = NewTxVirtualExecutor(
		nil, // env is set via SetEnvironment later
		messageSender,
		chainState,
		storageManager,
	)

	pendingTxManager := NewPendingTransactionManager()

	tp.TxValidatorPool = NewTxValidatorPool(
		nil, // env is set via SetEnvironment later
		tp,  // offChainProcessor (TransactionProcessor implements it)
		chainState,
		storageManager,
		eventSystem,
		transactionPool,
		pendingTxManager,
	)

	tp.MonitorCacheSize()
	// Khởi chạy goroutine để dọn dẹp cache hash của giao dịch "chỉ đọc"
	go tp.cleanupReadTxHashes()
	// Khởi chạy goroutine để dọn dẹp executedMvmIds để tránh rò rỉ bộ nhớ
	go tp.cleanupExecutedMvmIds()
	// Khởi chạy monitoring cho device key goroutines và memory
	go tp.MonitorDeviceKeyGoroutines()
	// Start async read TX worker pool (Component A)
	tp.startReadTxWorkers(NumReadTxWorkers)
	// Start TX injection workers (Phase 6)
	tp.startInjectionWorkers(NumInjectionWorkers)

	// MEMORY LEAK FIX: Start background sweeper for PendingTransactionManager
	// Uses a never-closed channel (lifetime of TransactionProcessor)
	pendingTxManager.StartCleanupLoop(make(chan struct{}))

	// MEMORY LEAK FIX: Start background cleanup for verifiedSignaturesCache
	// Prevents unbounded growth of signature verification cache (~2.3GB/hour at 10K TPS)
	tx_processor.StartSignatureCacheCleanup(make(chan struct{}))

	// Set global OffChainProcessor cho cross-chain handler
	cross_chain_handler.SetOffChainProcessor(tp)

	return tp
}

// startInjectionWorkers spawns workers that consume from injectionQueue
// and process transactions asynchronously (Component C).
func (tp *TransactionProcessor) startInjectionWorkers(n int) {
	for i := 0; i < n; i++ {
		go func(workerID int) {
			logger.Info("Injection worker %d started", workerID)
			for req := range tp.injectionQueue {
				tp.executeAndAddTx(req)
			}
			logger.Info("Injection worker %d stopped", workerID)
		}(i)
	}
}

// executeAndAddTx handles the full lifecycle of a transaction injection:
// virtual execution and pool addition.
// If rawBody is set, unmarshal here (deferred from readLoop for throughput).
func (tp *TransactionProcessor) executeAndAddTx(req injectionRequest) {
	tx := req.tx
	if tx == nil && len(req.rawBody) > 0 {
		// Deferred unmarshal: readLoop skipped this for throughput
		parsedTx := &transaction.Transaction{}
		if err := parsedTx.Unmarshal(req.rawBody); err != nil {
			logger.Error("Deferred TX unmarshal failed: %v", err)
			return
		}
		tx = parsedTx
	}
	if tx == nil {
		logger.Error("executeAndAddTx: tx is nil and no rawBody")
		return
	}

	err := tp.processTransactionFromClient(req.conn, tx, req.msgID)
	if err != nil {
		logger.Debug("Async injection failed for tx %s: %v", tx.Hash().Hex(), err)
	}
}

func (tp *TransactionProcessor) SetEnvironment(
	env ITransactionProcessorEnvironment,
) {
	tp.env = env
	tp.TxVirtualExecutor.SetEnvironment(env)
	tp.TxValidatorPool.SetEnvironment(env)
}

func (tp *TransactionProcessor) SendRawTransaction(ctx context.Context, rawTx []byte) ([]byte, error) {

	var isExistOverloaded bool
	value, exists := sharedmemory.GlobalSharedMemory.Read("pendingOverloaded")

	if !exists {
		isExistOverloaded = false
	} else {
		var ok bool
		isExistOverloaded, ok = value.(bool) // Type assertion
		if !ok {
			err := fmt.Errorf("error: cannot convert 'pendingOverloaded' to bool")
			return nil, err
		}
	}
	if isExistOverloaded {
		err := fmt.Errorf("system overloaded. waiting")
		return nil, err
	}

	tx := &transaction.Transaction{}
	err := tx.Unmarshal(rawTx)
	if err != nil {
		return nil, err
	}

	_, err = tp.AddTransactionToPool(tx)
	if err != nil {
		return nil, err
	}

	return rawTx, nil
}

// checkConnectionInitialized — REMOVED (see ProcessTransactionFromClient comments).
// The spin-wait retry loop (50×100ms=5s) was blocking TX processing goroutines.
// Connections are now validated by signature/nonce, not connection manager state.

func (tp *TransactionProcessor) ProcessTransactionFromClient(
	request network.Request,
) error {
	// NOTE: Removed checkConnectionInitialized — gây race condition với ProcessInitConnection
	// trong worker pool. TX được validate bằng signature/nonce.

	var isExistOverloaded bool
	value, exists := sharedmemory.GlobalSharedMemory.Read("pendingOverloaded")

	if !exists {
		isExistOverloaded = false
	} else {
		var ok bool
		isExistOverloaded, ok = value.(bool) // Type assertion
		if !ok {
			err := fmt.Errorf("error: cannot convert 'pendingOverloaded' to bool")
			request.Connection().Disconnect()
			return err
		}
	}
	if isExistOverloaded {
		err := fmt.Errorf("system overloaded. waiting")
		request.Connection().Disconnect()
		return err
	}

	// ═══════════════════════════════════════════════════════════════════
	// ZERO-COPY READLOOP: Do NOT unmarshal here — it blocks the TCP
	// readLoop goroutine (~100μs per TX). Instead, just copy the raw
	// bytes into the injectionQueue and let the 300 injection workers
	// do the unmarshal in parallel. This allows the readLoop to drain
	// the TCP receive buffer at wire speed (75K+ msg/s).
	// ═══════════════════════════════════════════════════════════════════
	rawBody := make([]byte, len(request.Message().Body()))
	copy(rawBody, request.Message().Body())
	// Enqueue raw bytes to async worker pool — non-blocking
	select {
	case tp.injectionQueue <- injectionRequest{conn: request.Connection(), rawBody: rawBody, msgID: request.Message().ID()}:
		// Successfully enqueued
	default:
		logger.Warn("injectionQueue is full, dropping transaction (queue_size=%d)", InjectionQueueSize)
		return fmt.Errorf("injection queue full, system overloaded")
	}

	// ── Prometheus: count received transaction ──────────────────────────
	metrics.TxsReceivedTotal.Inc()

	return nil
}

func (tp *TransactionProcessor) ProcessTransactionFromClientWithDeviceKey(
	request network.Request,
) error {
	// NOTE: Removed checkConnectionInitialized — nó gây race condition.
	// ProcessInitConnection chưa kịp xử lý (worker pool) khi TX handler chạy,
	// dẫn đến connection address = zero → TX bị reject sai.
	// TX đã được validate bằng signature/nonce, không cần check connection init.
	connAddr := request.Connection().Address()
	logger.Debug("[TX-DK] ProcessTransactionFromClientWithDeviceKey called: connAddr=%s, remoteAddr=%s, bodySize=%d",
		connAddr.Hex(), request.Connection().RemoteAddrSafe(), len(request.Message().Body()))

	transactionWithDeviceKey := &pb.TransactionWithDeviceKey{}
	err := proto.Unmarshal(request.Message().Body(), transactionWithDeviceKey)
	if err != nil {
		return err
	}

	tx := &transaction.Transaction{}
	tx.FromProto(transactionWithDeviceKey.Transaction)

	// DEBUG: Log TX details
	logger.Debug("[TX-DK] TX parsed: txHash=%s, from=%s, to=%s, nonce=%d, connAddr=%s",
		tx.Hash().Hex(), tx.FromAddress().Hex(), tx.ToAddress().Hex(), tx.GetNonce(), connAddr.Hex())
	// Always save txHash → connection mapping for txHash-based receipt delivery
	if tp.env != nil {
		tp.env.StoreTxHashConnEntry(tx.Hash(), TxHashConnEntry{
			Conn:      request.Connection(),
			MsgID:     request.Message().ID(),
			CreatedAt: time.Now(),
		})
		logger.Info("📌 [TX-DK] Mapped txHash=%s → conn=%s (connAddr=%s)",
			tx.Hash().Hex()[:16], request.Connection().RemoteAddrSafe(),
			connAddr.Hex()[:10])
	}

	if tp.storageManager.GetStorageBackupDeviceKey() == nil {
		logger.Error("❌ [DEBUG TX-DK] backupDeviceKeyStorage is nil!")
		return fmt.Errorf("error: backupDeviceKeyStorage not set")
	}

	err = tp.backupDeviceKey(tp.storageManager.GetStorageBackupDeviceKey(), tx, transactionWithDeviceKey.DeviceKey)
	if err != nil {
		logger.Error("❌ [DEBUG TX-DK] backupDeviceKey failed: %v", err)
		return fmt.Errorf("error: backupDeviceKeyStorage not set: %v", err)
	}

	// Enqueue to async worker pool — non-blocking
	select {
	case tp.injectionQueue <- injectionRequest{conn: request.Connection(), tx: tx, msgID: request.Message().ID()}:
		// Successfully enqueued
	default:
		err := fmt.Errorf("injection queue full, system overloaded")
		logger.Warn("injectionQueue is full, dropping device key transaction")
		tp.sendTransactionError(request.Connection(), tx.Hash(), -1, err.Error(), nil, request.Message().ID())
		return err
	}

	return nil
}

// ProcessTransactionOnChainWithDeviceKeyAndHash is the original method with lastHash parameter
func (tp *TransactionProcessor) ProcessTransactionOnChainWithDeviceKey(
	tx types.Transaction,
	rawNewDeviceKey []byte,
) error {
	// log
	transactionWithDeviceKey := &pb.TransactionWithDeviceKey{
		Transaction: tx.Proto().(*pb.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	tx.FromProto(transactionWithDeviceKey.Transaction)
	if tp.storageManager.GetStorageBackupDeviceKey() == nil {
		return fmt.Errorf("error: backupDeviceKeyStorage not set")
	}

	err := tp.backupDeviceKey(tp.storageManager.GetStorageBackupDeviceKey(), tx, transactionWithDeviceKey.DeviceKey)
	if err != nil {
		return fmt.Errorf("error: backupDeviceKeyStorage not set: %v", err)
	}

	output, err := tp.ProcessTransactionFromRpc(tx)
	if err != nil {
		logger.Error("Error ProcessTransactionOnChainWithDeviceKey err: %v , output %v", err, output)
		return fmt.Errorf("error: processTransactionFromClient not set: %v", err)
	}

	return nil
}

func (tp *TransactionProcessor) ProcessTransactionFromRpcWithDeviceKey(
	transactionWithDeviceKey *pb.TransactionWithDeviceKey,
) ([]byte, error) {
	tx := &transaction.Transaction{}
	tx.FromProto(transactionWithDeviceKey.Transaction)
	if tp.storageManager.GetStorageBackupDeviceKey() == nil {
		return nil, fmt.Errorf("error: backupDeviceKeyStorage not set")
	}
	err := tp.backupDeviceKey(tp.storageManager.GetStorageBackupDeviceKey(), tx, transactionWithDeviceKey.DeviceKey)
	if err != nil {
		return nil, fmt.Errorf("error: backupDeviceKeyStorage not set: %v", err)
	}
	output, err := tp.ProcessTransactionFromRpc(tx)
	if err != nil {
		return output, fmt.Errorf("error: %v", err)
	}

	return nil, nil
}

func (tp *TransactionProcessor) ProcessTransactionsFromClient(request network.Request) error {
	logger.Info("🔥 ProcessTransactionsFromClient CALLED, cmd_length=%d, body_length=%d", len(request.Message().Command()), len(request.Message().Body()))
	transactions, err := transaction.UnmarshalTransactions(request.Message().Body())
	if err != nil {
		logger.Error("❌ ProcessTransactionsFromClient: UnmarshalTransactions failed: %v", err)
		return err
	}

	logger.Info("🔥 ProcessTransactionsFromClient: Received batch of %d transactions", len(transactions))

	queueFullErrs := 0

	// FORK-SAFETY AND PERFORMANCE: Bypass the injectionQueue worker pool entirely
	// for batched transactions from the TPS blast tool.
	// The `AddTransactionsToPool` method internally takes the lock ONCE and validates in bulk.
	if len(transactions) > 0 {
		errors := tp.AddTransactionsToPool(transactions)
		for i, err := range errors {
			if err != nil {
				queueFullErrs++
				errCode := int64(-1)
				errMsg := err.Error()
				// Extract code from "[code:30] invalid data" format
				if strings.HasPrefix(errMsg, "[code:") {
					if idx := strings.Index(errMsg, "] "); idx > 0 {
						if code, parseErr := strconv.ParseInt(errMsg[6:idx], 10, 64); parseErr == nil {
							errCode = code
							errMsg = errMsg[idx+2:]
						}
					}
				}
				logger.Error("❌ [TX REJECTED] Batch AddTransactionToPool failed: txHash=%s, code=%d, msg=%s",
					transactions[i].Hash().Hex(), errCode, errMsg)
				tp.sendTransactionError(request.Connection(), transactions[i].Hash(), errCode, errMsg, nil, "")
			}
		}
	}

	logger.Info("🔥 ProcessTransactionsFromClient: Added batch to pool. Total errors: %d", queueFullErrs)

	if queueFullErrs > 0 {
		logger.Warn("Dropped %d transactions from batch due to pool rejection", queueFullErrs)
		return fmt.Errorf("dropped %d txs", queueFullErrs)
	}

	return nil
}

// `ProcessReadTransactionsFromSub` (Producer) gửi kết quả vào channel
func (tp *TransactionProcessor) ProcessReadTransactionsFromSub() {
	// NOT USED ANYMORE IN ITransactionProcessorEnvironment
	// It will be separated. Currently disabled for Sub Node fast sync optimization.
}

func (tp *TransactionProcessor) processTransactionFromClient(
	conn network.Connection,
	tx types.Transaction,
	msgID string,
) error {
	// Always save txHash → connection mapping for txHash-based receipt delivery
	// This ensures `BroadCastReceipts` sends the receipt back to the sender
	if tp.env != nil {
		tp.env.StoreTxHashConnEntry(tx.Hash(), TxHashConnEntry{
			Conn:      conn,
			MsgID:     msgID,
			CreatedAt: time.Now(),
		})
	}

	// ═══════════════════════════════════════════════════════════════════
	// FAST PATH: Skip expensive virtual EVM execution for simple TXs.
	// ProcessSingleTransactionVirtual takes ~10ms per TX, which is the
	// main bottleneck for injection throughput. These TX types don't
	// need virtual execution:
	//   1. Account setting TXs (BLS registration, account type changes)
	//   2. Validator staking TXs
	//   3. Simple value transfers (no contract call, no deploy)
	// ═══════════════════════════════════════════════════════════════════
	needsVirtualExecution := true

	// Skip for account setting TXs (BLS key registration, etc.)
	accountSettingAddr := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	if tx.ToAddress() == accountSettingAddr {
		needsVirtualExecution = false
	}

	// Skip for validator staking TXs
	if tx.ToAddress() == mt_common.VALIDATOR_CONTRACT_ADDRESS {
		needsVirtualExecution = false
	}

	// Skip for simple value transfers (not a contract call, not a deploy)
	if !tx.IsCallContract() && !tx.IsDeployContract() {
		needsVirtualExecution = false
	}

	// Skip for file upload chunks (existing logic)
	fileAbi, _ := file_handler.GetFileAbi()
	name, _ := fileAbi.ParseMethodName(tx)
	if tx.ToAddress() == file_handler.PredictContractAddress(common.HexToAddress(tp.chainState.GetConfig().OwnerFileStorageAddress)) && name == "uploadChunk" {
		needsVirtualExecution = false
	}

	if needsVirtualExecution {
		startVirtual := time.Now()
		updatedTx, err, output := tp.ProcessSingleTransactionVirtual(tx)
		_ = time.Since(startVirtual)
		if err != nil {
			logger.Error("ProcessSingleTransactionVirtual failed: ", err)
			tp.sendTransactionError(conn, tx.Hash(), -1, err.Error(), output, msgID)
			return err
		}
		tx = updatedTx
	}
	code, err := tp.AddTransactionToPool(tx)
	if err != nil {
		logger.Error("❌ [TX REJECTED] AddTransactionToPool failed: txHash=%s, msg=%s", tx.Hash().Hex(), err.Error())
		tp.sendTransactionError(conn, tx.Hash(), code, err.Error(), nil, msgID)
		return err
	}

	// Gửi phản hồi thành công với txHash, code 0 và msgID
	tp.sendTransactionResult(conn, tx.Hash(), msgID)
	return nil
}
func (tp *TransactionProcessor) ProcessTransactionFromRpc(tx types.Transaction) ([]byte, error) {
	var output []byte
	fileAbi, _ := file_handler.GetFileAbi()
	name, _ := fileAbi.ParseMethodName(tx)
	if !(tx.ToAddress() == file_handler.PredictContractAddress(common.HexToAddress(tp.chainState.GetConfig().OwnerFileStorageAddress)) && name == "uploadChunk") {
		updatedTx, err, output := tp.ProcessSingleTransactionVirtual(tx)
		if err != nil {
			logger.Error("ProcessSingleTransactionVirtual failed: ", err)
			return output, err
		}
		tx = updatedTx
	}
	_, err := tp.AddTransactionToPool(tx)
	if err != nil {
		logger.Error("AddTransactionToPool failed: ", err)
		return output, err
	}
	return output, nil
}

func (tp *TransactionProcessor) logBackendStartMs() {
	if atomic.CompareAndSwapInt32(&firstUdsMetricLogged, 0, 1) {
		nowMs := time.Now().UnixMilli()
		f, err := os.OpenFile("/tmp/backend_start_ms.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString(fmt.Sprintf("%d\n", nowMs))
			f.Close()
		}
		go func() {
			time.Sleep(10 * time.Second)
			atomic.StoreInt32(&firstUdsMetricLogged, 0)
		}()
	}
}
