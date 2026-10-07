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

	// RollupSignaturesRejectedTotal counts rejected rollup system attestations, labeled by reason.
	// Valid reasons: "non_committee", "invalid_signature", "invalid_length", "duplicate".
	RollupSignaturesRejectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "master_rollup_signatures_rejected_total",
		Help: "Total number of rollup system attestations rejected, labeled by reason",
	}, []string{"reason"})

	// RollupCommitteeReadErrorsTotal counts errors encountered when reading active committee validator keys.
	RollupCommitteeReadErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_rollup_committee_read_errors_total",
		Help: "Total errors when reading active committee validator keys for rollup system attestations",
	})

	// RawEthTxsReceivedTotal counts raw EIP-2718 transactions received (TCP & RPC).
	RawEthTxsReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_raw_eth_txs_received_total",
		Help: "Total raw Ethereum EIP-2718 transactions received",
	})

	// RawEthTxsRejectedTotal counts rejected raw transactions, labeled by error code.
	RawEthTxsRejectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "master_raw_eth_txs_rejected_total",
		Help: "Total rejected raw Ethereum transactions, labeled by error code",
	}, []string{"code"})

	// BlockGasCacheHitsTotal counts cache hits in getBlockGasInfo.
	BlockGasCacheHitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_rpc_block_gas_cache_hits_total",
		Help: "Total number of cache hits in RPC block gas calculation",
	})

	// BlockGasCacheMissesTotal counts cache misses in getBlockGasInfo.
	BlockGasCacheMissesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_rpc_block_gas_cache_misses_total",
		Help: "Total number of cache misses in RPC block gas calculation",
	})
)

// ─── Gauges ──────────────────────────────────────────────────────────────────

var (
	// InjectionQueueDepth tracks the current depth of the async transaction injection queue.
	InjectionQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_injection_queue_depth",
		Help: "Current depth of the transaction injection queue",
	})

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

	// RegisteredChainCount tracks the current size of GatewayEngine.ChainRegistry, i.e. how
	// many chains are currently recognized (registerChainViaStake against a real native-coin
	// deposit is now the ONLY registration path -- the old vote-gated ProposalRegisterChain was
	// retired 2026-09-04). Same "measure instead of guessing" rationale as GovernanceProposalCount
	// (note/cross_chain_attack_scenario_catalog.md item C6): registerChainViaStake grants no vote
	// gate at all -- every newly-registered chain gains full, unweighted Governance.ActiveChains
	// voting rights for the price of one MinNativeStakeToRegister deposit (a real, confirmed
	// Sybil-governance-vote risk, note/eurozone_unified_native_coin_plan.md mục 2.6, still open) --
	// so a large/anomalous influx of newly registered chains in a short window is exactly the
	// pattern a colluding coalition buying up majority governance control would produce, and there
	// was no visibility into that pattern at all before this metric. No hard rate-limit is imposed
	// (mirrors all_remaining_fixes_plan.md Mục 2's decision not to guess a cap without real
	// production data) -- if real data later shows abuse, add a cap as a follow-up backed by
	// evidence, not speculation.
	RegisteredChainCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_gateway_registered_chain_count",
		Help: "Current number of chains recognized in this GatewayEngine's own ChainRegistry",
	})

	// ValidatorCommitteeKeyValid indicates whether this node's attestation key matches its on-chain validator identity.
	// 1 = valid/active committee, 0 = mismatch with on-chain PublicKeyBls, -1 = not in validator committee.
	ValidatorCommitteeKeyValid = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_validator_committee_key_valid",
		Help: "Validator committee attestation key status: 1=valid, 0=mismatch, -1=not validator",
	})

	// AccountRegistrationPendingTotal tracks the number of account registrations currently pending in the relay queue.
	AccountRegistrationPendingTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_account_registration_pending_total",
		Help: "Current number of pending account registration requests in relay queue",
	})

	// AccountRegistrationPendingMaxAgeSeconds tracks the age in seconds of the oldest pending registration in relay.
	AccountRegistrationPendingMaxAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_account_registration_pending_max_age_seconds",
		Help: "Age in seconds of the oldest pending registration request in relay queue",
	})

	// RollupAttestationStoredTotal / RollupAttestationCompletedTotal count attestation sets entering and leaving the
	// pending store (contract storage). They are monotonic counters on purpose: they are bumped from the deterministic
	// execution path, which may run the same tx more than once (speculative execution) and starts from zero after a
	// restart, so an up/down gauge would drift. Use rate() / increase(); "pending" is approximately stored - completed.
	RollupAttestationStoredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_rollup_attestation_sets_stored_total",
		Help: "Attestation sets written to the pending store (waiting for committee quorum); approximate under speculative re-execution",
	})
	RollupAttestationCompletedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "master_rollup_attestation_sets_completed_total",
		Help: "Attestation sets removed from the pending store after quorum; approximate under speculative re-execution",
	})

	// ParentChainIDMismatch indicates whether the Parent Chain's reported chain ID differs from the local execution configuration.
	// 0 = matches or not yet checked / parent disabled, 1 = mismatch detected.
	ParentChainIDMismatch = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "master_parent_chain_id_mismatch",
		Help: "Status of Parent Chain ID match: 1=mismatch with local config, 0=matches",
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

	// RawEthBatchSize observes incoming TCP batch sizes for raw transactions.
	RawEthBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_raw_eth_batch_size",
		Help:    "Size of incoming raw transaction batches",
		Buckets: []float64{1, 5, 10, 50, 100, 250, 500, 1000},
	})

	// RawEthConversionDuration observes latency of decoding and validating raw Ethereum envelopes.
	RawEthConversionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "master_raw_eth_conversion_seconds",
		Help:    "Latency of decoding and validating raw Ethereum envelopes in seconds",
		Buckets: []float64{0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.005, 0.01},
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
