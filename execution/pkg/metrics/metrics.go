// Package metrics provides Prometheus instrumentation for the Go Master node.
// All metrics are registered globally and can be referenced from any package.
//
// Metric naming convention: master_<subsystem>_<name>_<unit>
// This aligns with Rust sync_* metrics for unified dashboarding.
package metrics

import (
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ─── Counters ────────────────────────────────────────────────────────────────

var (
	// RPCRequestsTotal counts all RPC requests, labeled by JSON-RPC method.
	RPCRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "master_rpc_requests_total",
		Help: "Total number of RPC requests received",
	}, []string{"method"})

	// RPCErrorsTotal counts all RPC errors, labeled by JSON-RPC method.
	RPCErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "master_rpc_errors_total",
		Help: "Total number of RPC errors",
	}, []string{"method"})

	// BlocksProcessedTotal counts all blocks that have been committed.
	BlocksProcessedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_blocks_processed_total",
		Help: "Total number of blocks committed to chain",
	})

	// TxsReceivedTotal counts all transactions received from clients/RPC.
	TxsReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_txs_received_total",
		Help: "Total transactions received from clients",
	})

	// TxsProcessedTotal counts transactions successfully processed into blocks.
	TxsProcessedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_txs_processed_total",
		Help: "Total transactions processed into blocks",
	})

	// EpochTransitionsTotal counts completed epoch transitions.
	EpochTransitionsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_epoch_transitions_total",
		Help: "Total epoch transitions completed",
	})

	// BlockStmConflictsTotal counts transactions/groups aborted during Block-STM parallel execution.
	BlockStmConflictsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_block_stm_conflicts_total",
		Help: "Total number of conflicts resolved by Block-STM Union-Find",
	})
)

// ─── Gauges ──────────────────────────────────────────────────────────────────

var (
	// CurrentBlock tracks the latest block number.
	CurrentBlock = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_current_block",
		Help: "Current block number",
	})

	// CurrentEpoch tracks the current epoch number.
	CurrentEpoch = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_current_epoch",
		Help: "Current epoch number",
	})

	// TxPoolSize tracks the current transaction pool depth.
	TxPoolSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_tx_pool_size",
		Help: "Current transaction pool size",
	})

	// Goroutines tracks the number of active goroutines.
	Goroutines = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_goroutines",
		Help: "Current number of goroutines",
	})

	// HeapAllocBytes tracks heap allocation in bytes.
	HeapAllocBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_heap_alloc_bytes",
		Help: "Current heap allocation in bytes",
	})

	// PeersConnected tracks the number of connected peers.
	PeersConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_peers_connected",
		Help: "Number of connected peers",
	})

	// GatewayRegistryDriftEpochs tracks how many epochs the local ChainRegistry is behind Root Anchor.
	GatewayRegistryDriftEpochs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "master_gateway_registry_drift_epochs",
		Help: "Number of epochs the local ChainRegistry is behind Root Anchor (by chain_id)",
	}, []string{"chain_id"})

	// GovernanceProposalCount tracks the total size of GatewayEngine.Governance.Proposals.
	// propose() is deliberately permissionless (gated only at vote()/quorum, see
	// all_remaining_fixes_plan.md Mục 2 for the full decision) -- the map has no TTL/cleanup,
	// so this exists to make its growth visible for real monitoring instead of guessing at a
	// rate-limit design with no production data behind it.
	GovernanceProposalCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_gateway_governance_proposal_count",
		Help: "Total number of governance proposals ever recorded (Propose() is permissionless; this map has no cleanup)",
	})
)

// ─── Histograms ──────────────────────────────────────────────────────────────

var (
	// BlockProcessingDuration observes block processing latency.
	// Buckets match Rust sync_round_duration_seconds for consistent dashboards.
	BlockProcessingDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_block_processing_seconds",
		Help:    "Block processing latency in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
	})

	// TxProcessingDuration observes transaction batch processing latency.
	TxProcessingDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_tx_processing_seconds",
		Help:    "Transaction batch processing latency in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
	})

	// RPCDuration observes RPC request handling duration, labeled by method.
	RPCDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "master_rpc_duration_seconds",
		Help:    "RPC request handling duration in seconds",
		Buckets: []float64{0.0001, 0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 5.0},
	}, []string{"method"})

	// EpochTransitionDuration observes epoch transition duration.
	// Buckets match Rust sync_epoch_transition_duration_seconds.
	EpochTransitionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_epoch_transition_seconds",
		Help:    "Epoch transition duration in seconds",
		Buckets: []float64{0.1, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0},
	})

	// BlockStmRounds observes the number of iterative rounds Block-STM required to finish.
	BlockStmRounds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_block_stm_rounds_total",
		Help:    "Number of iterative rounds to resolve a block",
		Buckets: []float64{1, 2, 3, 4, 5, 8, 10, 20},
	})

	// TrieIRDuration observes Trie Intermediate Root calculation latency.
	TrieIRDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_trie_ir_seconds",
		Help:    "Trie Intermediate Root calculation latency in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.0},
	})

	// TxMempoolDuration observes the latency of a transaction from reception to being forwarded to consensus.
	TxMempoolDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_tx_mempool_seconds",
		Help:    "Transaction latency in mempool before consensus in seconds",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0},
	})

	// TxConsensusDuration observes the latency of a transaction inside the consensus layer.
	TxConsensusDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_tx_consensus_seconds",
		Help:    "Transaction consensus latency in seconds",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0},
	})

	// TxExecutionDuration observes the execution latency of a transaction.
	TxExecutionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_tx_execution_seconds",
		Help:    "Transaction execution latency in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 5.0},
	})

	// TxEndToEndDuration observes the full end-to-end latency of a transaction.
	TxEndToEndDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_tx_end_to_end_seconds",
		Help:    "Full end-to-end latency from reception to execution in seconds",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0},
	})

	// BlockTimeDuration observes the time between block generations.
	BlockTimeDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_block_time_seconds",
		Help:    "Time between consecutive blocks in seconds",
		Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 15.0, 30.0, 60.0},
	})
)

// ─── System Metrics Collector ────────────────────────────────────────────────

// StartSystemMetricsCollector updates runtime gauges every interval.
// Call this once from main/app startup.
func StartSystemMetricsCollector(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			Goroutines.Set(float64(runtime.NumGoroutine()))
			HeapAllocBytes.Set(float64(m.HeapAlloc))
		}
	}()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// ObserveDuration is a convenience to observe elapsed time on a histogram.
// Usage: defer metrics.ObserveDuration(metrics.BlockProcessingDuration, time.Now())
func ObserveDuration(h prometheus.Observer, start time.Time) {
	h.Observe(time.Since(start).Seconds())
}
