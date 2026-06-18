package pipeline

import (
	"sync"
)

const MaxBlockTraces = 50

type BlockTrace struct {
	BlockNumber uint64 `json:"block_number"`
	TxCount     int    `json:"tx_count"`

	// Rust Consensus
	ConsensusDurationUs          int64 `json:"consensus_duration_ms"`
	RustMempoolProposeDurationUs int64 `json:"rust_mempool_propose_duration_ms"`
	RustDagConsensusDurationUs   int64 `json:"rust_dag_consensus_duration_ms"`
	RustDeliveryFFIDurationUs    int64 `json:"rust_delivery_ffi_duration_ms"`

	// Pre-mempool
	ClientBatchProcessingUs int64 `json:"client_batch_processing_ms"`
	WaitGoUs                int64 `json:"wait_go_us"`
	WaitRustUs              int64 `json:"wait_rust_us"`

	// Phase 1: Execution & Processing (Root Calc)
	ProcessTxsDurationUs   int64 `json:"process_txs_duration_ms"`
	ReceiptsRootDurationUs int64 `json:"receipts_root_duration_ms"`
	TxsRootDurationUs      int64 `json:"txs_root_duration_ms"`
	Phase1TotalDurationUs  int64 `json:"phase1_total_duration_ms"`

	// Phase 2: Create Block Data
	BlockDataDurationUs int64 `json:"block_data_duration_ms"`

	// Phase 3.1: Mapping Generate
	MappingDurationUs int64 `json:"mapping_duration_ms"`

	// Phase 3.2: Commit Memory (Trie Commit)
	CommitMemoryDurationUs int64 `json:"commit_memory_duration_ms"`

	// Phase 4: Job Prep & Snap
	JobPrepAndSnapDurationUs int64 `json:"job_prep_and_snap_duration_ms"`
	DispatchDurationUs       int64 `json:"dispatch_duration_ms"`

	// Phase 5: DB Persistence (Async)
	SaveDBDurationUs     int64 `json:"save_db_duration_ms"`
	TotalBlockDurationUs int64 `json:"total_block_duration_ms"`
	GCPauseUs            int64 `json:"gc_pause_us"` // Tracks GC pause duration during block processing
}

// BlockTraceStore holds traces for recent blocks
type BlockTraceStore struct {
	traces map[uint64]*BlockTrace
	mu     sync.RWMutex
}

var GlobalBlockTraceStore = &BlockTraceStore{
	traces: make(map[uint64]*BlockTrace),
}

func (s *BlockTraceStore) getOrCreateTrace(blockNum uint64) *BlockTrace {
	if trace, exists := s.traces[blockNum]; exists {
		return trace
	}
	trace := &BlockTrace{BlockNumber: blockNum}
	s.traces[blockNum] = trace

	// Evict old traces to prevent memory leak
	if len(s.traces) > MaxBlockTraces {
		var oldest uint64
		first := true
		for k := range s.traces {
			if first || k < oldest {
				oldest = k
				first = false
			}
		}
		if oldest > 0 {
			delete(s.traces, oldest)
		}
	}

	return trace
}

func (s *BlockTraceStore) AddConsensusAndExecTime(blockNum uint64, txCount int, consensusUs, execUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.TxCount = txCount
	t.ConsensusDurationUs = consensusUs
	t.ProcessTxsDurationUs = execUs
}

func (s *BlockTraceStore) SetRustConsensusDetailedTime(blockNum uint64, rustMempoolProposeUs, rustDagConsensusUs, rustDeliveryFFIUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.RustMempoolProposeDurationUs = rustMempoolProposeUs
	t.RustDagConsensusDurationUs = rustDagConsensusUs
	t.RustDeliveryFFIDurationUs = rustDeliveryFFIUs
}

func (s *BlockTraceStore) SetClientBatchProcessingTime(blockNum uint64, ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.ClientBatchProcessingUs = ms
}

func (s *BlockTraceStore) SetWaitTime(blockNum uint64, waitGoUs int64, waitRustUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.WaitGoUs = waitGoUs
	t.WaitRustUs = waitRustUs
}

func (s *BlockTraceStore) AddPhase1Time(blockNum uint64, processTxsUs, receiptsRootUs, txsRootUs, phase1TotalUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	// Only update ProcessTxs if not set already
	if t.ProcessTxsDurationUs == 0 {
		t.ProcessTxsDurationUs = processTxsUs
	}
	t.ReceiptsRootDurationUs = receiptsRootUs
	t.TxsRootDurationUs = txsRootUs
	t.Phase1TotalDurationUs = phase1TotalUs
}

func (s *BlockTraceStore) AddPhase2to4Time(blockNum uint64, phase2Us, phase31Us, phase32Us, phase4Us, dispatchUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.BlockDataDurationUs = phase2Us
	t.MappingDurationUs = phase31Us
	t.CommitMemoryDurationUs = phase32Us
	t.JobPrepAndSnapDurationUs = phase4Us
	t.DispatchDurationUs = dispatchUs
}

func (s *BlockTraceStore) UpdateCommitMemoryTime(blockNum uint64, durationUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.CommitMemoryDurationUs = durationUs
}

func (s *BlockTraceStore) UpdateSaveDBTime(blockNum uint64, durationUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.SaveDBDurationUs = durationUs
}

func (s *BlockTraceStore) UpdateTotalBlockTime(blockNum uint64, durationUs int64) BlockTrace {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.TotalBlockDurationUs += durationUs

	// Create a copy to return under lock
	traceCopy := *t
	return traceCopy
}

func (s *BlockTraceStore) AddGCPause(blockNum uint64, pauseUs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.GCPauseUs += pauseUs
}

func (s *BlockTraceStore) GetTraces(startBlock, endBlock uint64) []BlockTrace {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []BlockTrace
	for i := startBlock; i <= endBlock; i++ {
		if trace, exists := s.traces[i]; exists {
			result = append(result, *trace)
		}
	}
	return result
}
