// pipeline_stats.go — Thin wrapper that delegates to processor/pipeline sub-package.
// All logic lives in the pipeline package; this file exists only for backward
// compatibility so existing callers can continue using processor.GlobalPipelineStats.
package processor

import (
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor/pipeline"
)

// Re-export types from pipeline sub-package for backward compatibility.
type PipelineStats = pipeline.PipelineStats
type PipelineSnapshot = pipeline.PipelineSnapshot

// GlobalPipelineStats is the singleton pipeline stats instance.
// Delegates to pipeline.GlobalPipelineStats.
var GlobalPipelineStats = pipeline.GlobalPipelineStats

// LastSendBatchTimeNano — re-exported from pipeline sub-package.
var LastSendBatchTimeNano = &pipeline.LastSendBatchTimeNano

// LastSendBatchTxCount — re-exported from pipeline sub-package.
var LastSendBatchTxCount = &pipeline.LastSendBatchTxCount

// --- BlockProcessor integration methods ---

// GetPipelineStatsJSON returns pipeline stats as JSON bytes (called from HTTP handler)
func (bp *BlockProcessor) GetPipelineStatsJSON() ([]byte, error) {
	// Update live pool/pending sizes before snapshot
	GlobalPipelineStats.SetPoolSize(int64(bp.transactionProcessor.transactionPool.CountTransactions()))
	GlobalPipelineStats.SetPendingSize(int64(bp.transactionProcessor.pendingTxManager.Count()))
	return GlobalPipelineStats.SnapshotJSON()
}

// ResetPipelineStats resets all pipeline counters
func (bp *BlockProcessor) ResetPipelineStats() {
	GlobalPipelineStats.Reset()
}
