// @title processor/block_processor_monitoring.go
// @markdown processor/block_processor_monitoring.go - Resource monitoring and cleanup functionality
package processor

import (
	"runtime"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
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

		// Determine memLimitGB from config, fallback to default 8GB
		memLimitGB := 8
		if bp.config != nil && bp.config.GoMemLimitGB > 0 {
			memLimitGB = bp.config.GoMemLimitGB
		}
		memLimitMB := uint64(memLimitGB) * 1024

		// Warn at 80% of limit, Error at 95% of limit (since Sys can include large mapped memory)
		warnAllocMB := memLimitMB * 80 / 100
		errSysMB := memLimitMB * 95 / 100

		if allocMB > warnAllocMB {
			logger.Warn("RESOURCE_MONITOR: High memory allocation: %d MB (Sys: %d MB, Config Limit: %d GB)", allocMB, sysMB, memLimitGB)
		}
		if sysMB > errSysMB {
			logger.Error("RESOURCE_MONITOR: 🚨 Very high memory system usage: %d MB (possible memory leak! Limit: %d GB)", sysMB, memLimitGB)
		}

		// Pipeline health monitoring
		backupDbLen := len(bp.backupDbChannel)
		forceCommitLen := len(bp.forceCommitChan)

		// Log summary every 5 minutes (10 times)
		if time.Now().Unix()%300 < 30 { // Log in first 30 seconds of each 5 minutes
			logger.Info("RESOURCE_MONITOR: Channels[ProcessedVirtualTx:%d/%d, Commit:%d/%d, CreatedBlocks:%d/%d], "+
				"Maps[StateCommit:%d, SubNode:%d], Goroutines:%d, Memory[Alloc:%dMB, Sys:%dMB]",
				processedVirtualTxLen, processedVirtualTxCap,
				commitChannelLen, commitChannelCap,
				createdBlocksChanLen, createdBlocksChanCap,
				stateCommitBufferSize, subNodeBlockBufferSize,
				goroutineCount, allocMB, sysMB)
			logger.Info("PIPELINE_MONITOR: Channels[Commit:%d/%d, Backup:%d/%d, ForceCommit:%d/%d]",
				commitChannelLen, commitChannelCap,
				backupDbLen, cap(bp.backupDbChannel),
				forceCommitLen, cap(bp.forceCommitChan))
		}
	}
}

// CleanupOldPendingTransactions cleans up old pending transactions
// IMPORTANT: Reduce timeout from 50s to 30s for faster cleanup
// Transactions pending > 30s will be removed and error receipts sent
func (bp *BlockProcessor) CleanupOldPendingTransactions() {
	// PendingTransactionManager has been removed, so this function is intentionally left empty.
	// Transaction timeout handling should be implemented elsewhere or relies on transaction pool expiration.
}
