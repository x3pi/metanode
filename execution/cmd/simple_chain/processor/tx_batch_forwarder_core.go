// @title processor/block_processor_txs.go
// @markdown processor/block_processor_txs.go - Transaction processing for unified block proposer node
package processor

import (
	"fmt"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_config "github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"

	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// TxBatchForwarder handles transaction processing and forwarding to Rust FFI
type TxBatchForwarder struct {
	transactionProcessor *TransactionProcessor
	config               *mt_config.SimpleChainConfig
	chainState           *blockchain.ChainState
	connectionsManager   network.ConnectionsManager
	messageSender        network.MessageSender
}

func NewTxBatchForwarder(
	transactionProcessor *TransactionProcessor,
	config *mt_config.SimpleChainConfig,
	chainState *blockchain.ChainState,
	connectionsManager network.ConnectionsManager,
	messageSender network.MessageSender,
) *TxBatchForwarder {
	return &TxBatchForwarder{
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

	// TBAB: Throughput-Based Adaptive Batching state
	emaTPS := 0.0
	lastBatchTime := time.Now()
	cycleCount := uint64(0)

	for {
		<-ticker.C

		setEmptyBlock := false
		poolSizeBefore := bf.transactionProcessor.transactionPool.CountTransactions()

		// Kiểm tra pool size trước khi lấy transactions
		if poolSizeBefore == 0 {
			time.Sleep(1 * time.Millisecond)
			continue
		}

		cycleCount++
		if cycleCount%1000 == 0 {
			logger.Debug("[TX FLOW] TxsProcessor2: poolSizeBefore=%d, emaTPS=%.2f", poolSizeBefore, emaTPS)
		}

		// BATCH ACCUMULATION: Throughput-Based Adaptive Batching (TBAB)
		// Dynamically adjust timeout based on real-time TPS
		var batchAccumulationTimeout time.Duration
		if emaTPS < 1000 {
			batchAccumulationTimeout = 5 * time.Millisecond // Low latency for low load
		} else if emaTPS < 10000 {
			batchAccumulationTimeout = 20 * time.Millisecond // Balanced
		} else {
			batchAccumulationTimeout = 100 * time.Millisecond // Max throughput for extreme load
		}

		const batchAccumulationCheckInterval = 1 * time.Millisecond
		const minBatchSize = 20000

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
				// ADAPTIVE BATCHING: If no new TXs arrive for 10ms, flush immediately
				if stagnantCycles >= 10 {
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

		// TBAB: Update TPS
		totalTxs := len(txs)
		now := time.Now()
		elapsedSecs := now.Sub(lastBatchTime).Seconds()
		if elapsedSecs > 0 {
			currentTPS := float64(totalTxs) / elapsedSecs
			emaTPS = (emaTPS * 0.8) + (currentTPS * 0.2) // EMA smoothing
		}
		lastBatchTime = now

		if len(txs) == 0 {
			if poolSizeBefore > 0 {
				logger.Warn("⚠️  [TX FLOW] TxsProcessor2: Race condition detected! pool_size=%d->%d, pending_pool_size=%d->%d, but retrieved 0 transactions. Sleeping 10ms to prevent spin.",
					poolSizeBefore, poolSizeAfter, pendingPoolSizeBefore, pendingPoolSizeAfter)
				// Backoff to prevent 100% CPU spin loop when pool contains only future nonces
				time.Sleep(10 * time.Millisecond)
			}
			continue
		}

		// Block size capping: Limit total transactions processed per block tick to ~50,000 txs.
		// Return any excess transactions back to the pool to be processed in the next blocks.
		const targetBlockSize = 50000
		if len(txs) > targetBlockSize {
			remainingTxs := txs[targetBlockSize:]
			bf.transactionProcessor.transactionPool.AddTransactions(remainingTxs)
			for _, tx := range remainingTxs {
				bf.transactionProcessor.pendingTxManager.UpdateStatus(tx.Hash(), StatusInPool)
			}
			txs = txs[:targetBlockSize]
			totalTxs = len(txs) // Update totalTxs to match the capped list
		}

		// Chia nhỏ batch nếu có quá nhiều transaction để tránh quá tải
		if totalTxs > maxTransactionsPerBatch {
			logger.Debug("📦 [TX FLOW] TxsProcessor2: Retrieved %d transactions, splitting into batches of %d",
				totalTxs, maxTransactionsPerBatch)
		}

		logger.Debug("📦 [TX FLOW] TxsProcessor2: Retrieved %d transactions from pool (pool_size: %d->%d, pending_pool_size: %d->%d)",
			totalTxs, poolSizeBefore, poolSizeAfter, pendingPoolSizeBefore, pendingPoolSizeAfter)

		// Chia nhỏ thành các batch nhỏ hơn để gửi
		for batchStart := 0; batchStart < totalTxs; batchStart += maxTransactionsPerBatch {
			batchEnd := batchStart + maxTransactionsPerBatch
			if batchEnd > totalTxs {
				batchEnd = totalTxs
			}

			batchTxs := txs[batchStart:batchEnd]
			batchNum := (batchStart / maxTransactionsPerBatch) + 1
			totalBatches := (totalTxs + maxTransactionsPerBatch - 1) / maxTransactionsPerBatch

			shouldLogBatch := totalTxs > maxTransactionsPerBatch && (batchNum == 1 || batchNum == totalBatches || batchNum%10 == 0)
			if shouldLogBatch {
				logger.Debug("   📋 Batch [%d/%d]: Processing %d transactions (indices %d-%d)",
					batchNum, totalBatches, len(batchTxs), batchStart, batchEnd-1)
			}

			bTransaction, err := transaction.MarshalTransactions(batchTxs)
			if err != nil {
				logger.Error("MarshalTransactions for batch %d: %v", batchNum, err)
				continue
			}

			// FFI Path (forward directly to embedded Rust process)
			shouldLogSend := batchNum == 1 || batchNum == totalBatches
			if shouldLogSend {
				logger.Debug("📤 [TX FLOW] Sending batch [%d/%d]: %d transactions to Rust (size=%d bytes)",
					batchNum, totalBatches, len(batchTxs), len(bTransaction))
				logger.Debug("🔥 [PROFILING] GoSub: Sent batch of %d TXs to Rust FFI at UnixMilli: %d (batch %d/%d)",
					len(batchTxs), time.Now().UnixMilli(), batchNum, totalBatches)
			}

			// Gửi batch qua FFI (synchronous zero-copy injection)
			success := executor.SubmitTransactionBatch(bTransaction)
			if !success {
				logger.Warn("⚠️  [TX FLOW] Failed to inject batch [%d/%d] (%d txs) to FFI channel (pool full? will retry)",
					batchNum, totalBatches, len(batchTxs))
				// Re-add CURRENT AND ALL REMAINING transactions to the transaction pool
				remainingTxs := txs[batchStart:]
				bf.transactionProcessor.transactionPool.AddTransactions(remainingTxs)
				for _, tx := range remainingTxs {
					bf.transactionProcessor.pendingTxManager.UpdateStatus(tx.Hash(), StatusInPool)
				}
				// Slow down slightly on backpressure
				time.Sleep(50 * time.Millisecond)
				break // Break out of the batch loop to wait for the next tick
			} else {
				if shouldLogSend {
					logger.Debug("✅ [TX FLOW] Injected batch [%d/%d]: %d txs via FFI (Zero-Copy)",
						batchNum, totalBatches, len(batchTxs))
				}
				for _, tx := range batchTxs {
					tx_processor.GlobalTxTraceStore.UpdateTrace(tx.Hash(), "FORWARDED_TO_RUST", "Transaction batch forwarded to Rust consensus engine via FFI")
					if entry, ok := bf.transactionProcessor.env.GetTxHashConnEntry(tx.Hash()); ok {
						entry.SentToRustAt = time.Now()
						bf.transactionProcessor.env.StoreTxHashConnEntry(tx.Hash(), entry)
					}
				}
				// Pipeline stats: track TXs forwarded to Rust
				GlobalPipelineStats.IncrTxsForwarded(int64(len(batchTxs)))
				// ── RUST CONSENSUS TIMER: stamp when last batch enters Rust ──────
				LastSendBatchTimeNano.Store(time.Now().UnixNano())
				LastSendBatchTxCount.Store(int64(totalTxs))
				// ─────────────────────────────────────────────────────────────────
				// PACING: Add an adaptive delay proportional to batch size to prevent overflowing Rust consensus.
				// For small batches, sleep less (proportional to size) to avoid artificially bottlenecking TPS.
				// Capped at 5ms to prevent excessive delays for large batches.
				pacingDelay := time.Duration(len(batchTxs)) * 2 * time.Microsecond
				if pacingDelay > 5*time.Millisecond {
					pacingDelay = 5 * time.Millisecond
				}
				if pacingDelay > 0 {
					time.Sleep(pacingDelay)
				}
			}
		}
	}
}
