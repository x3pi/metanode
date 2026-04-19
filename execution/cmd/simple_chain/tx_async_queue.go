package main

// tx_async_queue.go — Asynchronous transaction processing queue.
//
// Separates the "send" stream (accept tx, return hash immediately) from the
// "receive" stream (process tx in background workers, receipts available via
// eth_getTransactionReceipt polling).

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// TxStatus represents the processing state of an async transaction.
type TxStatus int32

const (
	TxStatusPending    TxStatus = 0
	TxStatusProcessing TxStatus = 1
	TxStatusSuccess    TxStatus = 2
	TxStatusFailed     TxStatus = 3
)

// pendingTx holds all the data needed to process a transaction asynchronously.
type pendingTx struct {
	EthTx      *types.Transaction
	MetaTxData []byte
	MetaTxHash common.Hash
	EthTxHash  common.Hash
	EnqueuedAt time.Time
}

// txResult stores the outcome of an asynchronous transaction processing.
type txResult struct {
	Status    TxStatus
	Error     string
	Timestamp time.Time
}

// TxAsyncQueue manages asynchronous transaction processing with a fixed-size
// worker pool. Transactions are enqueued immediately (returning the hash to
// the caller), then processed by background workers.
type TxAsyncQueue struct {
	queue       chan *pendingTx
	results     sync.Map // map[common.Hash]*txResult — keyed by ethTxHash
	app         *App
	workerCount int
	wg          sync.WaitGroup
	cancel      context.CancelFunc
	ctx         context.Context

	// Metrics
	enqueued   atomic.Int64
	processed  atomic.Int64
	failed     atomic.Int64
	queueDepth atomic.Int64
}

// DefaultAsyncQueueSize is the default buffer size for the pending tx channel.
const DefaultAsyncQueueSize = 500000

// resultTTL is how long completed tx results are kept before cleanup.
// Clients have this window to poll eth_getTransactionReceipt.
const resultTTL = 5 * time.Minute

// resultCleanupInterval is how often the cleanup goroutine runs.
const resultCleanupInterval = 60 * time.Second

// NewTxAsyncQueue creates a new async queue with the given worker count.
// If workerCount <= 0, defaults to 2 * NumCPU.
func NewTxAsyncQueue(app *App, workerCount int) *TxAsyncQueue {
	if workerCount <= 0 {
		workerCount = runtime.NumCPU() * 2
		if workerCount > 32 {
			workerCount = 32
		}
		if workerCount < 2 {
			workerCount = 2
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &TxAsyncQueue{
		queue:       make(chan *pendingTx, DefaultAsyncQueueSize),
		app:         app,
		workerCount: workerCount,
		cancel:      cancel,
		ctx:         ctx,
	}
}

// Start launches the worker goroutines.
func (q *TxAsyncQueue) Start() {
	logger.Info("[TX_ASYNC] Starting async tx queue with %d workers, buffer=%d",
		q.workerCount, DefaultAsyncQueueSize)

	for i := 0; i < q.workerCount; i++ {
		q.wg.Add(1)
		go q.worker(i)
	}

	// Periodic stats logger
	q.wg.Add(1)
	go q.statsLogger()

	// MEMORY LEAK FIX: Periodic cleanup of old results
	q.wg.Add(1)
	go q.resultCleanupLoop()
}

// Stop gracefully shuts down the queue, processing remaining transactions.
func (q *TxAsyncQueue) Stop() {
	logger.Info("[TX_ASYNC] Shutting down async tx queue...")
	q.cancel()
	close(q.queue)
	q.wg.Wait()
	logger.Info("[TX_ASYNC] Async tx queue stopped. Processed=%d, Failed=%d",
		q.processed.Load(), q.failed.Load())
}

// EnqueueEthTransaction accepts a raw Ethereum transaction, performs fast
// validation (decode, BLS key lookup, MetaTx building), enqueues it for
// async processing, and returns the ETH tx hash immediately.
func (q *TxAsyncQueue) EnqueueEthTransaction(ctx context.Context, input hexutil.Bytes) (common.Hash, error) {
	// 1. Decode the Ethereum TX
	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(input); err != nil {
		return common.Hash{}, fmt.Errorf("failed to decode Ethereum transaction: %w", err)
	}

	// 2. Derive sender
	signer := types.LatestSignerForChainID(q.app.config.ChainId)
	fromAddress, err := types.Sender(signer, ethTx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to derive sender: %w", err)
	}

	// 3. Determine BLS private key
	var blsPrivateKey mt_common.PrivateKey
	if q.app.blsKeyStore != nil {
		exists, _ := q.app.blsKeyStore.HasPrivateKey(fromAddress)
		if exists {
			pkStr, err := q.app.blsKeyStore.GetPrivateKey(fromAddress)
			if err != nil {
				return common.Hash{}, fmt.Errorf("failed to retrieve BLS key for %s: %w", fromAddress.Hex(), err)
			}
			kp := bls.NewKeyPair(common.FromHex(pkStr))
			blsPrivateKey = kp.PrivateKey()
		} else {
			blsPrivateKey = q.app.keyPair.PrivateKey()
		}
	} else {
		blsPrivateKey = q.app.keyPair.PrivateKey()
	}

	// 4. Get latest state root
	stateRoot := q.app.blockProcessor.GetLastBlock().Header().AccountStatesRoot()

	// 5. Build MetaTx from EthTx (fast, in-memory)
	metaTxData, metaTx, err := buildMetaTxFromEthTx(
		ethTx,
		q.app.config.ChainId,
		blsPrivateKey,
		stateRoot,
		q.app,
	)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to build MetaTx: %w", err)
	}

	// 6. Mark as pending
	ethTxHash := ethTx.Hash()
	q.results.Store(ethTxHash, &txResult{
		Status:    TxStatusPending,
		Timestamp: time.Now(),
	})

	// 7. Enqueue for async processing
	ptx := &pendingTx{
		EthTx:      ethTx,
		MetaTxData: metaTxData,
		MetaTxHash: metaTx.Hash(),
		EthTxHash:  ethTxHash,
		EnqueuedAt: time.Now(),
	}

	select {
	case q.queue <- ptx:
		q.enqueued.Add(1)
		q.queueDepth.Add(1)
		logger.Info("[TX_ASYNC] Enqueued tx ethHash=%s metaHash=%s from=%s queueDepth=%d",
			ethTxHash.Hex(), metaTx.Hash().Hex(), fromAddress.Hex(), q.queueDepth.Load())
		return ethTxHash, nil
	default:
		// Queue full — reject
		q.results.Delete(ethTxHash)
		return common.Hash{}, fmt.Errorf("transaction queue is full (capacity=%d)", DefaultAsyncQueueSize)
	}
}

// GetTxStatus returns the current processing status for a given tx hash.
// Returns nil if the tx was never enqueued.
func (q *TxAsyncQueue) GetTxStatus(ethTxHash common.Hash) *txResult {
	if val, ok := q.results.Load(ethTxHash); ok {
		return val.(*txResult)
	}
	return nil
}

// worker is a goroutine that consumes from the queue and processes transactions.
func (q *TxAsyncQueue) worker(id int) {
	defer q.wg.Done()
	logger.Info("[TX_ASYNC] Worker %d started", id)

	for ptx := range q.queue {
		q.queueDepth.Add(-1)

		// Mark as processing
		q.results.Store(ptx.EthTxHash, &txResult{
			Status:    TxStatusProcessing,
			Timestamp: time.Now(),
		})

		// Process the transaction
		err := q.processTransaction(ptx)

		if err != nil {
			q.failed.Add(1)
			q.results.Store(ptx.EthTxHash, &txResult{
				Status:    TxStatusFailed,
				Error:     err.Error(),
				Timestamp: time.Now(),
			})
			logger.Error("[TX_ASYNC] Worker %d: tx %s failed: %v",
				id, ptx.EthTxHash.Hex(), err)
		} else {
			q.processed.Add(1)
			q.results.Store(ptx.EthTxHash, &txResult{
				Status:    TxStatusSuccess,
				Timestamp: time.Now(),
			})
		}

		elapsed := time.Since(ptx.EnqueuedAt)
		logger.Info("[TX_ASYNC] Worker %d: processed tx %s in %v (success=%v)",
			id, ptx.EthTxHash.Hex(), elapsed, err == nil)
	}

	logger.Info("[TX_ASYNC] Worker %d stopped", id)
}

// processTransaction handles the actual submission of a pre-built MetaTx.
func (q *TxAsyncQueue) processTransaction(ptx *pendingTx) error {
	// Unmarshal the TransactionWithDeviceKey
	txD := &mt_proto.TransactionWithDeviceKey{}
	if err := proto.Unmarshal(ptx.MetaTxData, txD); err != nil {
		return fmt.Errorf("failed to unmarshal TransactionWithDeviceKey: %w", err)
	}

	// Process through the transaction processor
	output, err := q.app.transactionProcessor.ProcessTransactionFromRpcWithDeviceKey(txD)
	if err != nil {
		return newError(err, output)
	}

	// Map ETH hash → BLS hash for receipt lookup
	if err := blockchain.GetBlockChainInstance().SetEthHashMapblsHash(ptx.EthTxHash, ptx.MetaTxHash); err != nil {
		logger.Warn("[TX_ASYNC] SetEthHashMapblsHash failed for %s: %v", ptx.EthTxHash.Hex(), err)
	}

	return nil
}

// statsLogger periodically logs queue metrics.
func (q *TxAsyncQueue) statsLogger() {
	defer q.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-q.ctx.Done():
			return
		case <-ticker.C:
			logger.Info("[TX_ASYNC] Stats: enqueued=%d processed=%d failed=%d queueDepth=%d",
				q.enqueued.Load(), q.processed.Load(), q.failed.Load(), q.queueDepth.Load())
		}
	}
}

// resultCleanupLoop periodically removes old entries from the results sync.Map
// to prevent unbounded memory growth. Completed results older than resultTTL
// are deleted — clients must poll within this window.
func (q *TxAsyncQueue) resultCleanupLoop() {
	defer q.wg.Done()
	ticker := time.NewTicker(resultCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-q.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			var cleaned, total int
			q.results.Range(func(key, value any) bool {
				total++
				result := value.(*txResult)
				// Only clean completed results (Success/Failed), keep Pending/Processing
				if (result.Status == TxStatusSuccess || result.Status == TxStatusFailed) &&
					now.Sub(result.Timestamp) > resultTTL {
					q.results.Delete(key)
					cleaned++
				}
				return true
			})
			if cleaned > 0 {
				logger.Info("[TX_ASYNC] Cleaned %d old results (remaining: %d)", cleaned, total-cleaned)
			}
		}
	}
}
