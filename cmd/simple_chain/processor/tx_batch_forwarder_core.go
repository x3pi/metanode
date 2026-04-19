// @title processor/block_processor_txs.go
// @markdown processor/block_processor_txs.go - Transaction processing for sub-nodes and channel-based architecture
package processor

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	mt_config "github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"

	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// TxsProcessor2 handles transaction processing with channel-based architecture

type TxBatchForwarder struct {
	serviceType          string
	transactionProcessor *TransactionProcessor
	config               *mt_config.SimpleChainConfig
	chainState           *blockchain.ChainState
	connectionsManager   network.ConnectionsManager
	messageSender        network.MessageSender
}

func NewTxBatchForwarder(
	serviceType string,
	transactionProcessor *TransactionProcessor,
	config *mt_config.SimpleChainConfig,
	chainState *blockchain.ChainState,
	connectionsManager network.ConnectionsManager,
	messageSender network.MessageSender,
) *TxBatchForwarder {
	return &TxBatchForwarder{
		serviceType:          serviceType,
		transactionProcessor: transactionProcessor,
		config:               config,
		chainState:           chainState,
		connectionsManager:   connectionsManager,
		messageSender:        messageSender,
	}
}

func (bf *TxBatchForwarder) StartForwardingLoop() {
	fmt.Printf("🚀 [TX FLOW] Starting TxsProcessor2 goroutine\n")
	ticker := time.NewTicker(1 * time.Millisecond) // 1ms poll for faster TX processing
	defer ticker.Stop()

	// LOCALHOST OPTIMIZATION: Max transactions per batch limit
	const maxTransactionsPerBatch = MaxTransactionsPerBatch
	
	// LOCALHOST OPTIMIZATION: Rate limitter
	rateLimiter := time.NewTicker(1 * time.Millisecond)
	defer rateLimiter.Stop()

	// Control concurrency for sending TCP fallback
	const maxConcurrentSends = MaxConcurrentSends
	semaphore := make(chan struct{}, maxConcurrentSends)

	for {
		<-ticker.C

		// Write (SUB-WRITE): sends TXs to local Rust, which forwards to validators
		if bf.serviceType == string(p_common.ServiceTypeMaster) || bf.serviceType == string(p_common.ServiceTypeWrite) {
			// FFI integration handles forwarding globally. No UDS socket connection needed.
		}

		setEmptyBlock := false
		poolSizeBefore := bf.transactionProcessor.transactionPool.CountTransactions()

		// Kiểm tra pool size trước khi lấy transactions
		if poolSizeBefore == 0 {
			time.Sleep(1 * time.Millisecond)
			continue
		}

		// BATCH ACCUMULATION: Wait for pool to fill up before draining.
		const batchAccumulationTimeout = 50 * time.Millisecond      
		const batchAccumulationCheckInterval = 5 * time.Millisecond 
		const minBatchSize = MaxTransactionsPerBatch                

		accumulationStart := time.Now()
		lastPoolSize := poolSizeBefore
		stagnantCycles := 0
		for {
			elapsed := time.Since(accumulationStart)
			if elapsed >= batchAccumulationTimeout {
				break // Timeout — drain whatever we have
			}

			currentPoolSize := bf.transactionProcessor.transactionPool.CountTransactions()

			if currentPoolSize >= minBatchSize {
				break
			}

			if currentPoolSize == lastPoolSize {
				stagnantCycles++
				if stagnantCycles >= 3 { 
					break
				}
			} else {
				stagnantCycles = 0
			}

			lastPoolSize = currentPoolSize
			time.Sleep(batchAccumulationCheckInterval)
		}

		poolSizeBefore = bf.transactionProcessor.transactionPool.CountTransactions()
		pendingPoolSizeBefore := bf.transactionProcessor.pendingTxManager.Count()
		txs := bf.transactionProcessor.ProcessTransactionsInPoolSub(setEmptyBlock)
		poolSizeAfter := bf.transactionProcessor.transactionPool.CountTransactions()
		pendingPoolSizeAfter := bf.transactionProcessor.pendingTxManager.Count()

		if len(txs) == 0 {
			if poolSizeBefore > 0 {
				logger.Warn("⚠️  [TX FLOW] TxsProcessor2: Race condition detected! pool_size=%d->%d, pending_pool_size=%d->%d, but retrieved 0 transactions",
					poolSizeBefore, poolSizeAfter, pendingPoolSizeBefore, pendingPoolSizeAfter)
			}
			continue
		}

		// Chia nhỏ batch nếu có quá nhiều transaction để tránh quá tải
		totalTxs := len(txs)
		if totalTxs > maxTransactionsPerBatch {
			logger.Info("📦 [TX FLOW] TxsProcessor2: Retrieved %d transactions, splitting into batches of %d",
				totalTxs, maxTransactionsPerBatch)
		}

		logger.Debug("📦 [TX FLOW] TxsProcessor2: Retrieved %d transactions from pool (pool_size: %d->%d, pending_pool_size: %d->%d)",
			totalTxs, poolSizeBefore, poolSizeAfter, pendingPoolSizeBefore, pendingPoolSizeAfter)

		// Chia nhỏ thành các batch nhỏ hơn để gửi
		for batchStart := 0; batchStart < totalTxs; batchStart += maxTransactionsPerBatch {
			<-rateLimiter.C

			batchEnd := batchStart + maxTransactionsPerBatch
			if batchEnd > totalTxs {
				batchEnd = totalTxs
			}

			batchTxs := txs[batchStart:batchEnd]
			batchNum := (batchStart / maxTransactionsPerBatch) + 1
			totalBatches := (totalTxs + maxTransactionsPerBatch - 1) / maxTransactionsPerBatch

			shouldLogBatch := totalTxs > maxTransactionsPerBatch && (batchNum == 1 || batchNum == totalBatches || batchNum%10 == 0)
			if shouldLogBatch {
				fmt.Printf("   📋 Batch [%d/%d]: Processing %d transactions (indices %d-%d)\n",
					batchNum, totalBatches, len(batchTxs), batchStart, batchEnd-1)
			}

			bTransaction, err := transaction.MarshalTransactions(batchTxs)
			if err != nil {
				logger.Error("MarshalTransactions for batch %d: %v", batchNum, err)
				continue
			}

			// FFI Path (Master and Write nodes forward directly to embedded Rust process)
			if bf.serviceType == string(p_common.ServiceTypeMaster) || bf.serviceType == string(p_common.ServiceTypeWrite) {
				shouldLogSend := batchNum == 1 || batchNum == totalBatches
				if shouldLogSend {
					logger.Info("📤 [TX FLOW] Sending batch [%d/%d]: %d transactions to Rust (size=%d bytes)",
						batchNum, totalBatches, len(batchTxs), len(bTransaction))
					logger.Info("🔥 [PROFILING] GoSub: Sent batch of %d TXs to Rust FFI at UnixMilli: %d (batch %d/%d)",
						len(batchTxs), time.Now().UnixMilli(), batchNum, totalBatches)
				}

				// Gửi batch qua FFI (synchronous zero-copy injection)
				success := executor.SubmitTransactionBatch(bTransaction)
				if !success {
					logger.Warn("⚠️  [TX FLOW] Failed to inject batch [%d/%d] (%d txs) to FFI channel (pool full? will retry)",
						batchNum, totalBatches, len(batchTxs))
					// Re-add to transaction pool for retry in the next tick
					bf.transactionProcessor.transactionPool.AddTransactions(batchTxs)
					for _, tx := range batchTxs {
						bf.transactionProcessor.pendingTxManager.UpdateStatus(tx.Hash(), StatusInPool)
					}
					// Slow down slightly on backpressure
					time.Sleep(50 * time.Millisecond)
					continue
				} else {
					if shouldLogSend {
						logger.Info("✅ [TX FLOW] Injected batch [%d/%d]: %d txs via FFI (Zero-Copy)",
							batchNum, totalBatches, len(batchTxs))
					}
					// Pipeline stats: track TXs forwarded to Rust
					GlobalPipelineStats.IncrTxsForwarded(int64(len(batchTxs)))
					// ── RUST CONSENSUS TIMER: stamp when last batch enters Rust ──────
					LastSendBatchTimeNano.Store(time.Now().UnixNano())
					LastSendBatchTxCount.Store(int64(totalTxs))
					// ─────────────────────────────────────────────────────────────────
				}
			}
		}

		// TCP Path (Fallback: Sub Nodes forward to Master or Single node)
		if bf.serviceType == string(p_common.ServiceTypeReadonly) || bf.chainState.GetConfig().Mode == p_common.MODE_SINGLE {
			// Marshal tất cả transactions một lần cho mode SINGLE
			bTransaction, err := transaction.MarshalTransactions(txs)
			if err != nil {
				logger.Error("MarshalTransactions: %v", err)
				continue
			}

			// Add transactions to pending manager
			for _, tx := range txs {
				bf.transactionProcessor.pendingTxManager.Add(tx, StatusProcessing)
			}

			var wg sync.WaitGroup
			masterConnections := bf.connectionsManager.ConnectionsByType(p_common.MapConnectionTypeToIndex(p_common.MASTER_CONNECTION_TYPE))

			// Retry logic nếu không có master connections
			if len(masterConnections) == 0 {
				logger.Warn("TxsProcessor2: không tìm thấy master connection, txCount=%d, retry sau 100ms",
					len(txs))
				time.Sleep(100 * time.Millisecond)
				masterConnections = bf.connectionsManager.ConnectionsByType(p_common.MapConnectionTypeToIndex(p_common.MASTER_CONNECTION_TYPE))
				if len(masterConnections) == 0 {
					logger.Warn("TxsProcessor2: vẫn không tìm thấy master connection sau retry")
					continue
				}
			}

			// Copy bTransaction một lần để tránh race condition và tối ưu memory
			bTransactionCopy := make([]byte, len(bTransaction))
			copy(bTransactionCopy, bTransaction)

			// Gửi đến tất cả master connections
			for address, conn := range masterConnections {
				if conn == nil {
					logger.Warn("TxsProcessor2: connection là nil, address=%s", address.Hex())
					continue
				}

				// Kiểm tra connection sẵn sàng (tối ưu: check trước khi tạo goroutine)
				if !conn.IsConnect() {
					// Retry logic để đợi connection sẵn sàng
					maxRetries := 10
					retryDelay := 50 * time.Millisecond
					connected := false

					for retry := 0; retry < maxRetries; retry++ {
						if conn.IsConnect() {
							connected = true
							break
						}
						if retry < maxRetries-1 {
							time.Sleep(retryDelay)
						}
					}

					if !connected {
						logger.Warn("TxsProcessor2: connection không connected sau %d retries, bỏ qua, address=%s",
							maxRetries, address.Hex())
						continue
					}
				}

				// Sử dụng semaphore để giới hạn số lượng goroutines đồng thời
				wg.Add(1)
				go func(c network.Connection, addr common.Address, txData []byte) {
					defer wg.Done()

					// Acquire semaphore
					semaphore <- struct{}{}
					defer func() { <-semaphore }()

					// Thêm timeout để tránh goroutine treo
					done := make(chan error, 1)
					go func() {
						done <- bf.messageSender.SendBytes(c, p_common.TransactionsFromSubTopic, txData)
					}()

					select {
					case sendErr := <-done:
						if sendErr != nil {
							logger.Error("TxsProcessor2: lỗi khi gửi transaction đến %s: %v", addr.Hex(), sendErr)
						}
					case <-time.After(5 * time.Second):
						logger.Error("TxsProcessor2: timeout khi gửi transaction đến %s", addr.Hex())
					}
				}(conn, address, bTransactionCopy)
			}

			// Đợi tất cả goroutines hoàn tất với timeout
			waitDone := make(chan struct{})
			go func() {
				wg.Wait()
				close(waitDone)
			}()

			select {
			case <-waitDone:
				// Thành công, không cần log
			case <-time.After(30 * time.Second):
				logger.Warn("TxsProcessor2: timeout khi đợi goroutines hoàn tất (30s)")
			}
		}
	}
}
