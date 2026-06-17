package pipeline

import (
	"sync"
)

const MaxBlockTraces = 10000

type BlockTrace struct {
	BlockNumber uint64 `json:"block_number"`
	TxCount     int    `json:"tx_count"`

	// Rust Consensus
	ConsensusDurationMs int64 `json:"consensus_duration_ms"`

	// Phase 1: Execution & Processing
	ProcessTxsDurationMs   int64 `json:"process_txs_duration_ms"`
	ReceiptsRootDurationMs int64 `json:"receipts_root_duration_ms"`
	TxsRootDurationMs      int64 `json:"txs_root_duration_ms"`
	Phase1TotalDurationMs  int64 `json:"phase1_total_duration_ms"`

	// Phase 2: Commit
	CommitMemoryDurationMs int64 `json:"commit_memory_duration_ms"`
	SaveDBDurationMs       int64 `json:"save_db_duration_ms"`
	Phase2TotalDurationMs  int64 `json:"phase2_total_duration_ms"`

	// Total
	TotalBlockDurationMs int64 `json:"total_block_duration_ms"`
}

type BlockTraceStore struct {
	mu     sync.RWMutex
	traces map[uint64]*BlockTrace
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

func (s *BlockTraceStore) AddConsensusAndExecTime(blockNum uint64, txCount int, consensusMs, execMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.TxCount = txCount
	t.ConsensusDurationMs = consensusMs
	t.ProcessTxsDurationMs = execMs
}

func (s *BlockTraceStore) AddPhase1Time(blockNum uint64, processTxsMs, receiptsRootMs, txsRootMs, phase1TotalMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	// Only update ProcessTxs if not set already
	if t.ProcessTxsDurationMs == 0 {
		t.ProcessTxsDurationMs = processTxsMs
	}
	t.ReceiptsRootDurationMs = receiptsRootMs
	t.TxsRootDurationMs = txsRootMs
	t.Phase1TotalDurationMs = phase1TotalMs
}

func (s *BlockTraceStore) UpdateCommitMemoryTime(blockNum uint64, durationMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.CommitMemoryDurationMs = durationMs
}

func (s *BlockTraceStore) UpdateSaveDBTime(blockNum uint64, durationMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.SaveDBDurationMs = durationMs
}

func (s *BlockTraceStore) UpdateTotalBlockTime(blockNum uint64, durationMs int64) BlockTrace {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.getOrCreateTrace(blockNum)
	t.TotalBlockDurationMs = durationMs
	return *t
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
