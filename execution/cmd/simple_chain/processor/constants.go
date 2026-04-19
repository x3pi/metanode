// Package processor defines shared constants for block processing, transaction batching,
// concurrency limits, and network parameters. Centralizing these values makes tuning
// and auditing easier.
package processor

import "time"

// ─── Transaction Batching ────────────────────────────────────────────────────

const (
	// MinTxsForImmediateBlock is the minimum number of transactions to trigger
	// immediate block creation without waiting for the ticker.
	// TPS OPTIMIZATION: Lowered to 1000 so blocks are created IMMEDIATELY when
	// transactions arrive from Rust consensus, instead of waiting for the 100ms ticker.
	// This eliminates idle time between Rust commit delivery and Go block creation.
	// FORK-SAFETY: All nodes use the same compiled constant → identical behavior.
	MinTxsForImmediateBlock = 1000

	// MaxTxsInAccumulatedResults caps accumulated transactions to prevent memory leaks.
	// TPS OPTIMIZATION: 200K → 500K to allow larger blocks when TX rate is high.
	MaxTxsInAccumulatedResults = 500000

	// MaxTimeForAccumulatedResults forces a flush after this duration even if
	// MinTxsForImmediateBlock has not been reached.
	MaxTimeForAccumulatedResults = 5 * time.Second

	// TxBatchSize is the number of transactions per batch in GenerateBlocksInBatch.
	// TPS OPTIMIZATION: 200K → 500K to match MaxTxsInAccumulatedResults.
	TxBatchSize = 500000

	// BlockInBatch is the number of blocks to create from a single batch.
	BlockInBatch = 10

	// MaxWaitTime is the maximum wait before flushing a partial batch.
	MaxWaitTime = 50 * time.Millisecond
)

// ─── Concurrency Limits ─────────────────────────────────────────────────────

const (
	// NumSubTxWorkers is the number of parallel workers processing incoming
	// transactions from go-sub on go-master (ProcessTransactionsFromSub).
	// TPS OPTIMIZATION: 32 → 64 to match higher TX ingestion rate with min_round_delay=200ms-300ms.
	// FORK-SAFETY: Does not affect block content — only controls client TX ingestion parallelism.
	NumSubTxWorkers = 64

	// MaxConcurrentWorkers limits parallel block-creation goroutines.
	MaxConcurrentWorkers = 100

	// MaxConcurrentSends limits parallel transaction sends to Rust (UDS).
	MaxConcurrentSends = 200

	// MaxTransactionsPerBatch is the maximum number of transactions sent in a single batch over UDS.
	// TPS OPTIMIZATION: 50K → 100K to reduce UDS send overhead (fewer calls, larger payloads).
	// FORK-SAFETY: Does not affect block content — Rust consensus groups TXs independently.
	MaxTransactionsPerBatch = 60000

	// MaxConcurrentReadTx limits parallel read-transaction goroutines.
	MaxConcurrentReadTx = 10000

	// MaxConcurrentOffChainExec limits parallel off-chain execution goroutines.
	MaxConcurrentOffChainExec = 500

	// MaxConcurrentDeviceKeySend limits parallel device-key backup goroutines.
	MaxConcurrentDeviceKeySend = 500

	// NumInjectionWorkers is the number of parallel workers processing
	// incoming transactions from clients on go-sub.
	NumInjectionWorkers = 300

	// NumReadTxWorkers is the number of parallel workers processing
	// read transactions.
	NumReadTxWorkers = 200

	// InjectionQueueSize is the buffer size for the injection queue.
	InjectionQueueSize = 500000
)

// ─── Backpressure ───────────────────────────────────────────────────────────

const (
	// HighWatermark is the transaction-pool size at which backpressure kicks in.
	HighWatermark = 8_000_000

	// LowWatermark is the transaction-pool size at which normal flow resumes.
	LowWatermark = 5_000_000
)

// ─── Network & Retry ────────────────────────────────────────────────────────

const (
	// MissingBlockFetchMinInterval is the minimum interval between attempts to
	// fetch a missing block from peers.
	MissingBlockFetchMinInterval = 5 * time.Second

	// MaxSkippedCommitsRetention is how many skipped commits to keep in memory.
	MaxSkippedCommitsRetention uint64 = 100

	// MonitoringTimeout is the timeout for monitoring operations.
	MonitoringTimeout = 30 * time.Second

	// PendingTimeout is the timeout for pending transaction operations.
	// With high TX volume (10K+), processing can take 2+ minutes.
	PendingTimeout = 10 * time.Minute

	// CommitMaxRetries is the number of retries for commit operations.
	CommitMaxRetries = 3

	// CommitRetryDelay is the delay between commit retries.
	CommitRetryDelay = 1 * time.Second

	// CommitSendTimeout is the timeout for sending commit data.
	CommitSendTimeout = 5 * time.Second
)

// ─── Sub Node Recovery ──────────────────────────────────────────────────────

const (
	// BatchBlockFetchSize is the max blocks to request per batch during recovery.
	BatchBlockFetchSize = 50

	// LargeGapThreshold triggers accelerated batch recovery mode.
	LargeGapThreshold = 100

	// CriticalGapThreshold triggers full resync via file-transfer if batch fetch fails.
	CriticalGapThreshold = 100000

	// MaxConsecutiveFetchFailures triggers resync after this many consecutive failures.
	// Increased to 60 (300 seconds total with 5s interval) to tolerate slow master nodes during load tests.
	MaxConsecutiveFetchFailures = 60

	// BatchFetchInterval is the interval between batch fetch attempts.
	BatchFetchInterval = 500 * time.Millisecond
)
