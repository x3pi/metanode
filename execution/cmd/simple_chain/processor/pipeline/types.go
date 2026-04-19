// Package pipeline defines the core types used by the block commit pipeline.
//
// These types (CommitJob, PersistJob, BroadcastJob, State) were originally
// defined inside the monolithic processor package. Moving them here allows
// other sub-packages to reference pipeline concepts without importing the
// entire processor package, breaking cyclic dependencies.
package pipeline

import (
	"sync"

	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	stake_state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
)

// State represents the processor state for the BlockProcessor state machine.
type State int

const (
	StateNotLook State = iota
	StatePendingLook
	StateLook
)

// CommitJob represents a job for committing a block through the commit pipeline.
// This is the primary unit of work passed through the commitChannel.
type CommitJob struct {
	Block          *block.Block
	ProcessResults *tx_processor.ProcessResult
	Receipts       types.Receipts
	TxDB           *transaction_state_db.TransactionStateDB
	DoneChan       chan struct{}
	// MappingWg is waited on before broadcasting receipts.
	// Ensures async SetTxHashMapBlockNumber goroutine finishes before clients can query TXs.
	MappingWg *sync.WaitGroup
	// TrieBatchSnapshot captures TrieDB collected batches BEFORE async send.
	// Only TrieDB needs snapshotting because it's explicitly reset via ResetCollectedBatches().
	// Without this, next block's CommitAllTrieDatabases() overwrites collectedBatches
	// before commitWorker reads them → Go Sub gets incomplete trie data → "missing trie node".
	TrieBatchSnapshot map[string][]byte

	// Phase 6 FIX: Synchronously captured state DB batches to prevent race condition
	// where next block overwrites singleton caches before commitWorker runs.
	AccountBatch              []byte
	SmartContractBatch        []byte
	SmartContractStorageBatch []byte
	CodeBatchPut              []byte
	MappingBatch              []byte
	StakeBatch                []byte
	BlockBatch                []byte

	// Snapshot Fix: Track the rust consensus commit index
	GlobalExecIndex uint64
}



// PersistJob holds pipeline commit results for async LevelDB persistence.
// Sent to persistWorker via persistChannel after CommitPipeline() completes.
type PersistJob struct {
	BlockNum      uint64
	AccountResult *account_state_db.PipelineCommitResult
	StakeResult   *stake_state_db.StakePipelineCommitResult
	ReceiptResult *types.ReceiptPipelineResult
	DoneSignal    chan struct{}
}


