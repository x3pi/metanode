// @title processor/block_processor_monitoring.go
// @markdown processor/block_processor_monitoring.go - Resource monitoring and cleanup functionality
package processor

import (
	"fmt"
	"runtime"
	"time"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/types"
)

// startResourceMonitoring monitors resource usage to detect memory leaks and resource exhaustion
func (bp *BlockProcessor) startResourceMonitoring() {
	ticker := time.NewTicker(30 * time.Second) // Run every 30 seconds
	defer ticker.Stop()

	for range ticker.C {
		// Monitor channel lengths
		processedVirtualTxLen := len(bp.ProcessedVirtualTransactionChain)
		processedVirtualTxCap := cap(bp.ProcessedVirtualTransactionChain)
		commitChannelLen := len(bp.commitChannel)
		commitChannelCap := cap(bp.commitChannel)
		createdBlocksChanLen := len(bp.createdBlocksChan)
		createdBlocksChanCap := cap(bp.createdBlocksChan)

		// Warn if channels are nearly full (>80%)
		if processedVirtualTxLen > processedVirtualTxCap*80/100 {
			logger.Warn("RESOURCE_MONITOR: ProcessedVirtualTransactionChain nearly full: %d/%d (%.1f%%)",
				processedVirtualTxLen, processedVirtualTxCap, float64(processedVirtualTxLen)/float64(processedVirtualTxCap)*100)
		}
		if commitChannelLen > commitChannelCap*80/100 {
			logger.Warn("RESOURCE_MONITOR: commitChannel nearly full: %d/%d (%.1f%%)",
				commitChannelLen, commitChannelCap, float64(commitChannelLen)/float64(commitChannelCap)*100)
		}
		if createdBlocksChanLen > createdBlocksChanCap*80/100 {
			logger.Warn("RESOURCE_MONITOR: createdBlocksChan nearly full: %d/%d (%.1f%%)",
				createdBlocksChanLen, createdBlocksChanCap, float64(createdBlocksChanLen)/float64(createdBlocksChanCap)*100)
		}

		// Monitor map sizes
		bp.stateCommitBufferMutex.Lock()
		stateCommitBufferSize := len(bp.stateCommitBlockBuffer)
		bp.stateCommitBufferMutex.Unlock()

		bp.bufferMutex.Lock()
		subNodeBlockBufferSize := len(bp.subNodeBlockBuffer)
		bp.bufferMutex.Unlock()

		if stateCommitBufferSize > 100 {
			logger.Warn("RESOURCE_MONITOR: stateCommitBlockBuffer size large: %d", stateCommitBufferSize)
		}
		if subNodeBlockBufferSize > 100 {
			logger.Warn("RESOURCE_MONITOR: subNodeBlockBuffer size large: %d", subNodeBlockBufferSize)
		}

		// Monitor goroutines
		goroutineCount := runtime.NumGoroutine()
		if goroutineCount > 1000 {
			logger.Warn("RESOURCE_MONITOR: High goroutine count: %d", goroutineCount)
		}
		if goroutineCount > 10000 {
			logger.Error("RESOURCE_MONITOR: 🚨 Very high goroutine count: %d (possible goroutine leak!)", goroutineCount)
		}

		// Monitor memory
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		allocMB := m.Alloc / 1024 / 1024
		sysMB := m.Sys / 1024 / 1024
		if allocMB > 2048 { // > 2GB
			logger.Warn("RESOURCE_MONITOR: High memory allocation: %d MB (Sys: %d MB)", allocMB, sysMB)
		}
		if sysMB > 4096 { // > 4GB
			logger.Error("RESOURCE_MONITOR: 🚨 Very high memory system usage: %d MB (possible memory leak!)", sysMB)
		}

		// Fix 5: Pipeline health monitoring
		persistChannelLen := len(bp.persistChannel)
		persistChannelCap := cap(bp.persistChannel)
		backupDbLen := len(bp.backupDbChannel)
		forceCommitLen := len(bp.forceCommitChan)

		// Detect stuck pipeline: commitChannel has items + persistChannel full = likely deadlock
		if commitChannelLen > 0 && persistChannelLen >= persistChannelCap {
			logger.Error("🚨 PIPELINE_STALL_DETECTED: commitChannel=%d AND persistChannel FULL (%d/%d). "+
				"commitWorker may be blocked on persistence. Check NOMT/PebbleDB I/O.",
				commitChannelLen, persistChannelLen, persistChannelCap)
		}

		// Log summary every 5 minutes (10 times)
		if time.Now().Unix()%300 < 30 { // Log in first 30 seconds of each 5 minutes
			logger.Info("RESOURCE_MONITOR: Channels[ProcessedVirtualTx:%d/%d, Commit:%d/%d, CreatedBlocks:%d/%d], "+
				"Maps[StateCommit:%d, SubNode:%d], Goroutines:%d, Memory[Alloc:%dMB, Sys:%dMB]",
				processedVirtualTxLen, processedVirtualTxCap,
				commitChannelLen, commitChannelCap,
				createdBlocksChanLen, createdBlocksChanCap,
				stateCommitBufferSize, subNodeBlockBufferSize,
				goroutineCount, allocMB, sysMB)
			logger.Info("PIPELINE_MONITOR: Channels[Commit:%d/%d, Persist:%d/%d, Backup:%d/%d, ForceCommit:%d/%d]",
				commitChannelLen, commitChannelCap,
				persistChannelLen, persistChannelCap,
				backupDbLen, cap(bp.backupDbChannel),
				forceCommitLen, cap(bp.forceCommitChan))
		}
	}
}

// CleanupOldPendingTransactions cleans up old pending transactions
// IMPORTANT: Reduce timeout from 50s to 30s for faster cleanup
// Transactions pending > 30s will be removed and error receipts sent
func (bp *BlockProcessor) CleanupOldPendingTransactions() {
	// IMPORTANT: Reduce timeout from 50s to 30s for faster cleanup
	const timeoutDuration = PendingTimeout

	// Log before cleanup for debugging
	totalPendingCount := bp.transactionProcessor.pendingTxManager.Count()
	if totalPendingCount > 0 {
		logger.Debug("[TX FLOW] CleanupOldPendingTransactions: checking %d pending transactions (timeout=%v)",
			totalPendingCount, timeoutDuration)
	}

	allOldPendingTxs := bp.transactionProcessor.pendingTxManager.GetOldTransactionsForRemoval(timeoutDuration)
	var receiptErrors []types.Receipt

	// Log details of each timed-out transaction
	for _, tx := range allOldPendingTxs {
		age := time.Since(tx.Timestamp)
		// Create more detailed error message
		errorMessage := fmt.Sprintf("Transaction timeout: pending for more than 30 seconds. txHash: %s, from: %s, nonce: %d",
			tx.Tx.Hash().Hex(),
			tx.Tx.FromAddress().Hex()[:10]+"...",
			tx.Tx.GetNonce())
		rcpErr := receipt.NewReceipt(tx.Tx.Hash(), tx.Tx.FromAddress(), tx.Tx.ToAddress(), tx.Tx.Amount(), pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(errorMessage), pb.EXCEPTION_NONE, p_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, uint64(0), common.Hash{}, 0)
		receiptErrors = append(receiptErrors, rcpErr)

		// Log detailed warning about timeout transaction
		logger.Warn("⏰ [TX FLOW] Pending transaction timeout: txHash=%s, from=%s, to=%s, nonce=%d, status=%s, age=%v (added at %v)",
			tx.Tx.Hash().Hex(),
			tx.Tx.FromAddress().Hex()[:10]+"...",
			tx.Tx.ToAddress().Hex()[:10]+"...",
			tx.Tx.GetNonce(),
			tx.Status,
			age,
			tx.Timestamp.Format("15:04:05.000"))
	}

	removedCount := len(allOldPendingTxs)
	if removedCount > 0 {
		bp.transactionProcessor.pendingTxManager.RemoveTransactions(allOldPendingTxs)
		logger.Info("✅ [TX FLOW] CleanupOldPendingTransactions: removed %d timeout transactions (remaining pending: %d)",
			removedCount, totalPendingCount-removedCount)
	} else if totalPendingCount > 0 {
		// Log when no transactions were timed out (to know mechanism is running)
		logger.Debug("✅ [TX FLOW] CleanupOldPendingTransactions: no timeout transactions (all %d pending transactions are still within timeout)",
			totalPendingCount)
	}

	if len(receiptErrors) > 0 {
		logger.Info("📤 [TX FLOW] Broadcasting %d error receipts for timeout transactions", len(receiptErrors))
		go bp.BroadCastReceipts(receiptErrors)
	}
}
