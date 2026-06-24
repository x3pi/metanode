package tx_processor

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

// TxTraceStep represents a single step in the transaction lifecycle.
type TxTraceStep struct {
	Step      string `json:"step"`
	Timestamp int64  `json:"timestamp"`
	Details   string `json:"details,omitempty"`
}

// TxTrace represents the collection of steps for a transaction.
type TxTrace struct {
	Hash      string        `json:"hash"`
	Step      string        `json:"step"`
	Timestamp int64         `json:"timestamp"`
	Details   string        `json:"details,omitempty"`
	Steps     []TxTraceStep `json:"steps"`
}

// TxTraceStore keeps track of the last N transaction traces in memory.
type TxTraceStore struct {
	mu      sync.RWMutex
	traces  map[common.Hash]*TxTrace
	hashes  []common.Hash
	head    int
	maxSize int
}

var (
	// GlobalTxTraceStore handles all transaction tracing in-memory.
	GlobalTxTraceStore = NewTxTraceStore(20000)
)

// NewTxTraceStore creates a new trace store.
func NewTxTraceStore(maxSize int) *TxTraceStore {
	return &TxTraceStore{
		traces:  make(map[common.Hash]*TxTrace, maxSize),
		hashes:  make([]common.Hash, maxSize),
		maxSize: maxSize,
	}
}

// UpdateTrace updates the trace of a transaction with the given step and details.
func (s *TxTraceStore) UpdateTrace(hash common.Hash, step string, details string) {
	if config.ConfigApp == nil || !config.ConfigApp.TxTraceEnabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	t, exists := s.traces[hash]
	if !exists {
		// Evict the oldest key if we wrapped around
		oldest := s.hashes[s.head]
		if oldest != (common.Hash{}) {
			delete(s.traces, oldest)
		}
		s.hashes[s.head] = hash
		s.head = (s.head + 1) % s.maxSize
		t = &TxTrace{
			Hash:  hash.Hex(),
			Steps: make([]TxTraceStep, 0, 4),
		}
		s.traces[hash] = t
	}
	t.Step = step
	t.Timestamp = time.Now().UnixMilli()
	t.Details = details
	t.Steps = append(t.Steps, TxTraceStep{
		Step:      step,
		Timestamp: t.Timestamp,
		Details:   details,
	})
}

// GetTrace retrieves a copy of the transaction trace for the given hash.
func (s *TxTraceStore) GetTrace(hash common.Hash) (*TxTrace, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, exists := s.traces[hash]
	if !exists {
		return nil, false
	}
	stepsCopy := make([]TxTraceStep, len(t.Steps))
	copy(stepsCopy, t.Steps)
	return &TxTrace{
		Hash:      t.Hash,
		Step:      t.Step,
		Timestamp: t.Timestamp,
		Details:   t.Details,
		Steps:     stepsCopy,
	}, true
}

// GetLatestTraces retrieves the most recent transaction traces up to the specified limit.
func (s *TxTraceStore) GetLatestTraces(limit int) []*TxTrace {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var traces []*TxTrace
	// head is the index where the next new item will be inserted.
	// We want to go backwards from head-1 to get the most recent ones.
	for i := 1; i <= s.maxSize; i++ {
		idx := (s.head - i + s.maxSize) % s.maxSize
		hash := s.hashes[idx]
		if hash == (common.Hash{}) {
			break // No more traces
		}
		if t, exists := s.traces[hash]; exists {
			stepsCopy := make([]TxTraceStep, len(t.Steps))
			copy(stepsCopy, t.Steps)
			traces = append(traces, &TxTrace{
				Hash:      t.Hash,
				Step:      t.Step,
				Timestamp: t.Timestamp,
				Details:   t.Details,
				Steps:     stepsCopy,
			})
			if len(traces) >= limit {
				break
			}
		}
	}
	return traces
}

// Enabled returns true if transaction tracing is enabled in the configuration.
func (s *TxTraceStore) Enabled() bool {
	return config.ConfigApp != nil && config.ConfigApp.TxTraceEnabled
}
