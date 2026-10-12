package processor

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	sharedmemory "github.com/meta-node-blockchain/meta-node/pkg/shared_memory"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

var (
	firstUdsMetricLogged int32

	txListPool = sync.Pool{
		New: func() interface{} {
			s := make([]types.Transaction, 0, 1000)
			return &s
		},
	}
	errListPool = sync.Pool{
		New: func() interface{} {
			s := make([]error, 0, 1000)
			return &s
		},
	}
)

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
	rawEth  bool   // If true, rawBody is an EIP-2718 raw Ethereum envelope
	msgID   string // Header ID từ request gốc, dùng để gửi response thành công
}

// RawEthTxConverter converts an Ethereum raw binary envelope into an internal MetaTx
// and returns the decoded go-ethereum transaction.
type RawEthTxConverter func(rawEth []byte) (types.Transaction, *e_types.Transaction, error)

type TransactionProcessor struct {
	// admitMu makes "wait for mempool room" + "add to mempool" atomic across workers on the TCP submission paths.
	// Without it, N workers all observe room and all add, overshooting MaxMempoolSize and hitting evict-or-reject.
	admitMu       sync.Mutex
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

	rawEthConverter RawEthTxConverter

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

	// FORK-SAFETY: Shared RWMutex between virtual execution (RLock) and real
	// block processing (Lock). This prevents concurrent cgo calls to C++ MVM
	// that cause non-deterministic stateRoot divergence across nodes.
	blockProcessingLock := &sync.RWMutex{}

	// Initialize the embedded TxVirtualExecutor
	tp.TxVirtualExecutor = NewTxVirtualExecutor(
		nil, // env is set via SetEnvironment later
		messageSender,
		chainState,
		storageManager,
		blockProcessingLock,
	)

	tp.TxValidatorPool = NewTxValidatorPool(
		nil, // env is set via SetEnvironment later
		tp,  // offChainProcessor (TransactionProcessor implements it)
		chainState,
		storageManager,
		eventSystem,
		transactionPool,
		blockProcessingLock,
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

	// MEMORY LEAK FIX: Start background cleanup for verifiedSignaturesCache

	// MEMORY LEAK FIX: Start background cleanup for verifiedSignaturesCache
	// Prevents unbounded growth of signature verification cache (~2.3GB/hour at 10K TPS)
	tx_processor.StartSignatureCacheCleanup(make(chan struct{}))

	return tp
}

// startInjectionWorkers spawns workers that consume from injectionQueue
// and process transactions asynchronously (Component C).
func (tp *TransactionProcessor) startInjectionWorkers(n int) {
	for i := 0; i < n; i++ {
		go func(workerID int) {
			// logger.Info("Injection worker %d started", workerID)
			for req := range tp.injectionQueue {
				tp.executeAndAddTx(req)
			}
			// logger.Info("Injection worker %d stopped", workerID)
		}(i)
	}
}

// SetRawEthConverter sets the custom raw Ethereum envelope converter.
func (tp *TransactionProcessor) SetRawEthConverter(converter RawEthTxConverter) {
	tp.rawEthConverter = converter
}

// defaultRawEthConverter is used as fallback when no custom converter is injected.
func (tp *TransactionProcessor) defaultRawEthConverter(rawEth []byte) (types.Transaction, *e_types.Transaction, error) {
	ethTx := new(e_types.Transaction)
	if err := ethTx.UnmarshalBinary(rawEth); err != nil {
		return nil, nil, fmt.Errorf("failed to decode Ethereum transaction: %w", err)
	}
	var expectedChainId *big.Int
	if tp.chainState != nil && tp.chainState.GetConfig() != nil {
		expectedChainId = tp.chainState.GetConfig().ChainId
	}
	if err := transaction.ValidateEthTxEnvelope(ethTx, expectedChainId); err != nil {
		return nil, nil, err
	}
	metaTx, err := transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build MetaTx from EthTx: %w", err)
	}
	return metaTx, ethTx, nil
}

func (tp *TransactionProcessor) convertRawEth(rawEth []byte) (types.Transaction, *e_types.Transaction, error) {
	if tp.rawEthConverter != nil {
		return tp.rawEthConverter(rawEth)
	}
	return tp.defaultRawEthConverter(rawEth)
}

// executeAndAddTx handles the full lifecycle of a transaction injection:
// virtual execution and pool addition.
// If rawBody is set, unmarshal here (deferred from readLoop for throughput).
func (tp *TransactionProcessor) executeAndAddTx(req injectionRequest) {
	tx := req.tx
	var ethTxHash common.Hash
	if req.rawEth {
		startConv := time.Now()
		metaTx, ethTx, err := tp.convertRawEth(req.rawBody)
		metrics.RawEthConversionDuration.Observe(time.Since(startConv).Seconds())
		if err != nil {
			txErr := transaction.ClassifyEthTxError(err)
			metrics.RawEthTxsRejectedTotal.WithLabelValues(fmt.Sprintf("%d", txErr.Code)).Inc()
			logger.Warn("executeAndAddTx: rawEth conversion failed (code %d): %v", txErr.Code, err)
			tp.sendTransactionError(req.conn, common.Hash{}, int64(txErr.Code), err.Error(), nil, req.msgID)
			return
		}
		ethTxHash = ethTx.Hash()
		tx = metaTx
	} else if tx == nil && len(req.rawBody) > 0 {
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

	tx_processor.GlobalTxTraceStore.UpdateTrace(tx.Hash(), "INJECTION_UNMARSHALED", "Transaction successfully unmarshaled from client request")

	err := tp.processTransactionFromClient(req.conn, tx, req.msgID, ethTxHash)
	if err != nil {
		logger.Debug("Async injection failed for tx %s: %v", tx.Hash().Hex(), err)
	} else if req.rawEth && len(req.rawBody) > 0 {
		if bc := blockchain.GetBlockChainInstance(); bc != nil {
			bc.AddTxToCache(ethTxHash, append([]byte(nil), req.rawBody...))
		}
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

// ProcessTransactionOnChainWithDeviceKeyAndHash is the original method with lastHash parameter
func (tp *TransactionProcessor) ProcessTransactionOnChainWithDeviceKey(
	tx types.Transaction,
	rawNewDeviceKey []byte,
) error {
	if len(rawNewDeviceKey) > 0 && tp.storageManager != nil {
		tp.storageManager.SavePendingDeviceKey(tx.Hash(), rawNewDeviceKey)
	}

	output, err := tp.ProcessTransactionFromRpc(tx)
	if err != nil {
		logger.Error("Error ProcessTransactionOnChainWithDeviceKey err: %v , output %v", err, output)
		return fmt.Errorf("error: processTransactionFromClient not set: %v", err)
	}

	return nil
}

func (tp *TransactionProcessor) processTransactionFromClient(
	conn network.Connection,
	tx types.Transaction,
	msgID string,
	respHash common.Hash,
) error {
	tx_processor.GlobalTxTraceStore.UpdateTrace(tx.Hash(), "INJECTION_RECEIVED", fmt.Sprintf("Received from connection: %s", conn.RemoteAddrSafe()))

	tx_processor.GlobalTxTraceStore.UpdateTrace(tx.Hash(), "MEMPOOL_ADD_START", "Adding transaction to mempool")
	tp.admitMu.Lock()
	tp.waitForPoolRoom(1)
	code, err := tp.AddAdmittedTransactionToPool(tx)
	tp.admitMu.Unlock()
	if err != nil {
		tx_processor.GlobalTxTraceStore.UpdateTrace(tx.Hash(), "MEMPOOL_ADD_FAILED", err.Error())
		logger.Warn("⚠️ [TX REJECTED] AddTransactionToPool failed: txHash=%s, msg=%s", tx.Hash().Hex(), err.Error())
		errHash := tx.Hash()
		if respHash != (common.Hash{}) {
			errHash = respHash
		}
		tp.sendTransactionError(conn, errHash, code, err.Error(), nil, msgID)
		return err
	}

	// Always save txHash → connection mapping for txHash-based receipt delivery AFTER pool admission
	if tp.env != nil {
		tp.env.StoreTxHashConnEntry(tx.Hash(), TxHashConnEntry{
			Conn:      conn,
			MsgID:     msgID,
			CreatedAt: time.Now(),
		})
		if respHash != (common.Hash{}) && respHash != tx.Hash() {
			tp.env.StoreTxHashConnEntry(respHash, TxHashConnEntry{
				Conn:      conn,
				MsgID:     msgID,
				CreatedAt: time.Now(),
			})
		}
	}

	if respHash != (common.Hash{}) && respHash != tx.Hash() {
		if bc := blockchain.GetBlockChainInstance(); bc != nil {
			_ = bc.SetEthHashMapblsHash(respHash, tx.Hash())
		}
	}

	tx_processor.GlobalTxTraceStore.UpdateTrace(tx.Hash(), "MEMPOOL_ADD_SUCCESS", "Transaction is pending in mempool")
	// Gửi phản hồi thành công với respHash (nếu có, ví dụ ethTxHash), code 0 và msgID
	finalRespHash := tx.Hash()
	if respHash != (common.Hash{}) {
		finalRespHash = respHash
	}
	tp.sendTransactionResult(conn, finalRespHash, msgID)
	return nil
}

// ProcessRawTransactionFromClient receives a single raw Ethereum EIP-2718 envelope via TCP.
func (tp *TransactionProcessor) ProcessRawTransactionFromClient(
	request network.Request,
) error {

	// Overload is handled upstream by backpressure (network.SetTxAdmissionGate): the connection reader waits before the
	// request is enqueued. Rejecting or disconnecting here would silently drop the batch of a fire-and-forget client
	// and leave a nonce gap for every sender in it.

	body := request.Message().Body()
	if len(body) == 0 {
		err := fmt.Errorf("empty raw transaction body")
		tp.sendTransactionError(request.Connection(), common.Hash{}, int64(transaction.ErrDecodeRawEth.Code), err.Error(), nil, request.Message().ID())
		return err
	}
	if len(body) > transaction.MaxRawEthTxEnvelopeSize {
		err := fmt.Errorf("transaction envelope size %d exceeds maximum limit (%d)", len(body), transaction.MaxRawEthTxEnvelopeSize)
		tp.sendTransactionError(request.Connection(), common.Hash{}, int64(transaction.ErrExceedsMaxEnvelopeSize.Code), err.Error(), nil, request.Message().ID())
		return err
	}

	rawBody := make([]byte, len(body))
	copy(rawBody, body)

	// ZERO-COPY READLOOP: Enqueue raw bytes to async worker pool — non-blocking
	select {
	case tp.injectionQueue <- injectionRequest{
		conn:    request.Connection(),
		rawBody: rawBody,
		rawEth:  true,
		msgID:   request.Message().ID(),
	}:
		// Successfully enqueued
	default:
		logger.Warn("injectionQueue is full, dropping raw transaction (queue_size=%d)", InjectionQueueSize)
		err := fmt.Errorf("injection queue full, system overloaded")
		tp.sendTransactionError(request.Connection(), common.Hash{}, -1, err.Error(), nil, request.Message().ID())
		return err
	}

	metrics.TxsReceivedTotal.Inc()
	metrics.RawEthTxsReceivedTotal.Inc()
	metrics.InjectionQueueDepth.Set(float64(len(tp.injectionQueue)))
	return nil
}

// poolAdmissionHeadroom is kept free below MaxMempoolSize when admitting from TCP. Every forwarder tick takes up to
// 40,000 transactions out of the pool (maxPoolDrainPerTick in StartForwardingLoop) and puts back everything it did not
// forward (future nonces and the part above the block cap), so the pool size swings by that much. Admitting right up
// to MaxMempoolSize lets such a swing push the pool over the hard cap and trigger evict-or-reject.
const poolAdmissionHeadroom = 40000

// waitForPoolRoom is admission backpressure for the TCP submission paths. Raw-tx clients are fire-and-forget, so when
// the mempool is full the pool's evict-or-reject path drops transactions the client never learns about; each dropped
// tx is a permanent nonce gap and every later tx of that sender sits in the pool as "future" forever. Waiting here
// (the worker blocks, the central queue fills, the connection reader blocks, TCP flow control slows the client)
// keeps the stream lossless. It waits regardless of the connection state: the batch was received in full, so it is not
// discarded because the client already left, and giving up would fall back to the lossy path. The pool drains as soon
// as the forwarder makes progress (futures are dropped after FutureTxTimeout). Callers hold admitMu.
func (tp *TransactionProcessor) waitForPoolRoom(n int) {
	if tp.TxValidatorPool == nil || tp.transactionPool == nil { // only unit-test fixtures lack a pool; nothing to wait for
		return
	}
	start := time.Now()
	warned := false
	for tp.transactionPool.CountTransactions()+n >= MaxMempoolSize-poolAdmissionHeadroom {
		if !warned && time.Since(start) > 5*time.Second {
			warned = true
			logger.Warn("⏳ [ADMISSION] waiting for mempool room (pool=%d, limit=%d) for %v", tp.transactionPool.CountTransactions(), MaxMempoolSize-poolAdmissionHeadroom, time.Since(start).Round(time.Millisecond))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ProcessRawTransactionsFromClient receives a batch of raw Ethereum EIP-2718 envelopes via TCP,
// encoded as RLP [][]byte.
func (tp *TransactionProcessor) ProcessRawTransactionsFromClient(request network.Request) error {

	// Overload is handled upstream by backpressure (network.SetTxAdmissionGate): the connection reader waits before the
	// request is enqueued. Rejecting or disconnecting here would silently drop the batch of a fire-and-forget client
	// and leave a nonce gap for every sender in it.

	startTime := time.Now()
	body := request.Message().Body()
	if len(body) == 0 {
		err := fmt.Errorf("empty raw transaction batch")
		tp.sendTransactionError(request.Connection(), common.Hash{}, int64(transaction.ErrDecodeRawEth.Code), err.Error(), nil, request.Message().ID())
		return err
	}

	var rawEnvelopes [][]byte
	if err := rlp.DecodeBytes(body, &rawEnvelopes); err != nil {
		logger.Error("❌ ProcessRawTransactionsFromClient: rlp decode failed: %v", err)
		tp.sendTransactionError(request.Connection(), common.Hash{}, int64(transaction.ErrDecodeRawEth.Code), fmt.Sprintf("invalid batch RLP format: %v", err), nil, request.Message().ID())
		return fmt.Errorf("invalid batch RLP format: %w", err)
	}

	if len(rawEnvelopes) == 0 {
		err := fmt.Errorf("empty raw transaction batch")
		tp.sendTransactionError(request.Connection(), common.Hash{}, int64(transaction.ErrDecodeRawEth.Code), err.Error(), nil, request.Message().ID())
		return err
	}

	if len(rawEnvelopes) > transaction.MaxBatchTxCount {
		err := fmt.Errorf("batch transaction count %d exceeds maximum limit (%d)", len(rawEnvelopes), transaction.MaxBatchTxCount)
		logger.Error("❌ [TX BATCH REJECTED] %v", err)
		tp.sendTransactionError(request.Connection(), common.Hash{}, int64(transaction.ErrExceedsMaxBatchSize.Code), err.Error(), nil, request.Message().ID())
		return err
	}

	metrics.RawEthBatchSize.Observe(float64(len(rawEnvelopes)))
	metrics.RawEthTxsReceivedTotal.Add(float64(len(rawEnvelopes)))
	metrics.InjectionQueueDepth.Set(float64(len(tp.injectionQueue)))

	logger.Info("🔥 ProcessRawTransactionsFromClient: Received batch of %d raw Ethereum transactions", len(rawEnvelopes))

	type batchItemError struct {
		Index int
		Hash  common.Hash
		Code  int64
		Msg   string
	}
	var batchErrors []batchItemError

	type convertedItem struct {
		idx    int
		rawEnv []byte
		metaTx types.Transaction
		ethTx  *e_types.Transaction
		err    error
	}

	// Bounded worker pool to parallelize batch decoding and ecrecover across CPU cores
	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > 16 {
		numWorkers = 16
	}
	if numWorkers > len(rawEnvelopes) {
		numWorkers = len(rawEnvelopes)
	}
	if numWorkers <= 0 {
		numWorkers = 1
	}

	converted := make([]convertedItem, len(rawEnvelopes))
	var wg sync.WaitGroup
	chunkSize := (len(rawEnvelopes) + numWorkers - 1) / numWorkers

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(rawEnvelopes) {
			end = len(rawEnvelopes)
		}
		if start >= len(rawEnvelopes) {
			break
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for idx := s; idx < e; idx++ {
				rawEnv := rawEnvelopes[idx]
				if len(rawEnv) > transaction.MaxRawEthTxEnvelopeSize {
					converted[idx] = convertedItem{
						idx:    idx,
						rawEnv: rawEnv,
						err:    fmt.Errorf("batch item [%d] envelope size %d exceeds maximum limit (%d)", idx, len(rawEnv), transaction.MaxRawEthTxEnvelopeSize),
					}
					continue
				}
				startConv := time.Now()
				metaTx, ethTx, err := tp.convertRawEth(rawEnv)
				metrics.RawEthConversionDuration.Observe(time.Since(startConv).Seconds())
				converted[idx] = convertedItem{
					idx:    idx,
					rawEnv: rawEnv,
					metaTx: metaTx,
					ethTx:  ethTx,
					err:    err,
				}
			}
		}(start, end)
	}
	wg.Wait()

	bc := blockchain.GetBlockChainInstance()
	processedTxs := make([]types.Transaction, 0, len(rawEnvelopes))
	processedEthHashes := make([]common.Hash, 0, len(rawEnvelopes))
	processedRawEnvs := make([][]byte, 0, len(rawEnvelopes))

	for idx, item := range converted {
		if item.err != nil {
			txErr := transaction.ClassifyEthTxError(item.err)
			metrics.RawEthTxsRejectedTotal.WithLabelValues(fmt.Sprintf("%d", txErr.Code)).Inc()
			var failedHash common.Hash
			if item.ethTx != nil {
				failedHash = item.ethTx.Hash()
			}
			logger.Warn("ProcessRawTransactionsFromClient: item [%d] convert failed (code %d): %v", idx, txErr.Code, item.err)
			batchErrors = append(batchErrors, batchItemError{
				Index: idx,
				Hash:  failedHash,
				Code:  int64(txErr.Code),
				Msg:   fmt.Sprintf("batch item [%d]: %v", idx, item.err),
			})
			continue
		}

		processedTxs = append(processedTxs, item.metaTx)
		processedEthHashes = append(processedEthHashes, item.ethTx.Hash())
		processedRawEnvs = append(processedRawEnvs, item.rawEnv)

		tx_processor.GlobalTxTraceStore.UpdateTrace(item.metaTx.Hash(), "BATCH_UNMARSHALED", "Raw Ethereum transaction received in batch from client")
	}

	if len(processedTxs) == 0 {
		firstErr := batchErrors[0]
		err := fmt.Errorf("all %d transactions in batch failed decoding/validation: %s", len(rawEnvelopes), firstErr.Msg)
		logger.Warn("ProcessRawTransactionsFromClient: %v", err)
		tp.sendTransactionError(request.Connection(), firstErr.Hash, firstErr.Code, err.Error(), nil, request.Message().ID())
		return err
	}

	maxChunkSize := 0
	if tp.chainState != nil && tp.chainState.GetConfig() != nil {
		maxChunkSize = tp.chainState.GetConfig().TxVerificationChunkSize
	}
	if maxChunkSize <= 0 {
		maxChunkSize = runtime.GOMAXPROCS(0) * 50
		if maxChunkSize < 1000 {
			maxChunkSize = 1000
		} else if maxChunkSize > 5000 {
			maxChunkSize = 5000
		}
	}

	var allErrors = make([]error, 0, len(processedTxs))
	for i := 0; i < len(processedTxs); i += maxChunkSize {
		end := i + maxChunkSize
		if end > len(processedTxs) {
			end = len(processedTxs)
		}
		chunkTxs := processedTxs[i:end]
		tp.admitMu.Lock()
		tp.waitForPoolRoom(len(chunkTxs))
		chunkErrs := tp.AddAdmittedTransactionsToPool(chunkTxs)
		tp.admitMu.Unlock()
		allErrors = append(allErrors, chunkErrs...)
	}

	queueFullErrs := 0
	var successfulEthHashes []common.Hash
	for i, err := range allErrors {
		if err != nil {
			tx_processor.GlobalTxTraceStore.UpdateTrace(processedTxs[i].Hash(), "MEMPOOL_ADD_FAILED", err.Error())
			queueFullErrs++
			txErr := transaction.ClassifyEthTxError(err)
			errCode := int64(txErr.Code)
			errMsg := err.Error()
			if strings.HasPrefix(errMsg, "[code:") {
				if idx := strings.Index(errMsg, "] "); idx > 0 {
					if code, parseErr := strconv.ParseInt(errMsg[6:idx], 10, 64); parseErr == nil {
						errCode = code
						errMsg = errMsg[idx+2:]
					}
				}
			}
			logger.Warn("⚠️ [TX REJECTED] Batch AddTransactionToPool failed: ethHash=%s, metaHash=%s, code=%d, msg=%s",
				processedEthHashes[i].Hex(), processedTxs[i].Hash().Hex(), errCode, errMsg)
			batchErrors = append(batchErrors, batchItemError{
				Index: i,
				Hash:  processedEthHashes[i],
				Code:  errCode,
				Msg:   errMsg,
			})
		} else {
			successfulEthHashes = append(successfulEthHashes, processedEthHashes[i])
			tx_processor.GlobalTxTraceStore.UpdateTrace(processedTxs[i].Hash(), "MEMPOOL_ADD_SUCCESS", "Transaction is pending in mempool")

			if bc != nil {
				_ = bc.SetEthHashMapblsHash(processedEthHashes[i], processedTxs[i].Hash())
				bc.AddTxToCache(processedEthHashes[i], append([]byte(nil), processedRawEnvs[i]...))
			}

			if tp.env != nil {
				tp.env.StoreTxHashConnEntry(processedTxs[i].Hash(), TxHashConnEntry{
					Conn:      request.Connection(),
					MsgID:     request.Message().ID(),
					CreatedAt: time.Now(),
				})
				tp.env.StoreTxHashConnEntry(processedEthHashes[i], TxHashConnEntry{
					Conn:      request.Connection(),
					MsgID:     request.Message().ID(),
					CreatedAt: time.Now(),
				})
			}
		}
	}

	logger.Info("🔥 ProcessRawTransactionsFromClient: Added batch to pool. Total errors: %d, accepted: %d", queueFullErrs, len(successfulEthHashes))
	if len(successfulEthHashes) > 0 {
		hashBytesList := make([][]byte, len(successfulEthHashes))
		for i, h := range successfulEthHashes {
			hashBytesList[i] = h.Bytes()
		}
		respBody, errRlp := rlp.EncodeToBytes(hashBytesList)
		if errRlp != nil {
			tp.sendTransactionResult(request.Connection(), successfulEthHashes[0], request.Message().ID())
		} else {
			tp.sendTransactionSuccessBytes(request.Connection(), respBody, request.Message().ID())
		}
	} else if len(batchErrors) > 0 {
		firstErr := batchErrors[0]
		tp.sendTransactionError(request.Connection(), firstErr.Hash, firstErr.Code, firstErr.Msg, nil, request.Message().ID())
	}
	elapsed := time.Since(startTime)
	if elapsed > 10*time.Millisecond {
		logger.Warn("⏱️  [PERF-CLIENT-BATCH] ProcessRawTransactionsFromClient took %v", elapsed)
	}
	return nil
}

func (tp *TransactionProcessor) ProcessTransactionFromRpc(tx types.Transaction) ([]byte, error) {
	var output []byte

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
