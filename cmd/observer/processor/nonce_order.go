package processor

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// ScanConfig stores the information needed for periodic GetLogs scanning.
// This is stored inside NonceOrderState so the scan job knows what to query.
type ScanConfig struct {
	EventName   string          // "MessageSent" or "MessageReceived"
	SourceIdStr string          // sourceNationId as string (e.g. "1", "2")
	Topic0      common.Hash     // Event signature hash
	Topics      [][]common.Hash // Full topic filters for GetLogs
	Address     common.Address  // Contract address to scan (1 contract per channel)
}

// PendingEntry stores an event waiting to be processed in nonce order.
// T is the concrete event type (e.g. *cross_chain_contract.MessageSent).
type PendingEntry[T any] struct {
	Event       T
	TxHash      common.Hash
	Nonce       *big.Int
	Timestamp   time.Time
	BlockNumber *big.Int
}

// NonceOrderState manages nonce-ordered event processing with a pending pool.
// It is not safe for concurrent use without external locking.
type NonceOrderState[T any] struct {
	nextExpectedNonce *big.Int
	lastBlockNumber   *big.Int
	pendingPool       map[string]*PendingEntry[T]
	scanConfig        *ScanConfig // Thông tin scan cho periodic GetLogs
}

// NewNonceOrderState creates a NonceOrderState with the given initial nonce and block number.
func NewNonceOrderState[T any](nextNonce, lastBlock *big.Int) *NonceOrderState[T] {
	return &NonceOrderState[T]{
		nextExpectedNonce: nextNonce,
		lastBlockNumber:   lastBlock,
		pendingPool:       make(map[string]*PendingEntry[T]),
	}
}

// SetScanConfig sets the scan configuration for periodic GetLogs scanning.
// Should be called right after NewNonceOrderState.
func (s *NonceOrderState[T]) SetScanConfig(config *ScanConfig) {
	s.scanConfig = config
}

// GetScanConfig returns the scan configuration.
func (s *NonceOrderState[T]) GetScanConfig() *ScanConfig {
	return s.scanConfig
}

// NextExpectedNonce returns the current next expected nonce.
func (s *NonceOrderState[T]) NextExpectedNonce() *big.Int { return s.nextExpectedNonce }

// LastBlockNumber returns the last processed block number.
func (s *NonceOrderState[T]) LastBlockNumber() *big.Int { return s.lastBlockNumber }

// PendingPoolSize returns the number of events waiting in the pending pool.
func (s *NonceOrderState[T]) PendingPoolSize() int { return len(s.pendingPool) }

// ShouldSkip returns true when the event nonce is below the next expected (already processed).
func (s *NonceOrderState[T]) ShouldSkip(nonce *big.Int) bool {
	return nonce.Cmp(s.nextExpectedNonce) < 0
}

// IsInOrder returns true when the event nonce exactly matches the next expected nonce.
func (s *NonceOrderState[T]) IsInOrder(nonce *big.Int) bool {
	return nonce.Cmp(s.nextExpectedNonce) == 0
}

// Advance increments nextExpectedNonce by 1 and updates lastBlockNumber.
func (s *NonceOrderState[T]) Advance(blockNumber *big.Int) {
	s.nextExpectedNonce = new(big.Int).Add(s.nextExpectedNonce, big.NewInt(1))
	s.lastBlockNumber = blockNumber
}

// Reset sets a new nextExpectedNonce and optionally a new lastBlockNumber.
// The pending pool is cleared because pending nonces are no longer valid
// after an admin channel state reset (adminSetChannelState).
// Pass nil for blockNumber to keep the existing value.
func (s *NonceOrderState[T]) Reset(newNextNonce *big.Int, newBlockNumber *big.Int) {
	s.nextExpectedNonce = new(big.Int).Set(newNextNonce)
	if newBlockNumber != nil {
		s.lastBlockNumber = new(big.Int).Set(newBlockNumber)
	}
	// Xóa pending pool vì các nonce cũ không còn hợp lệ sau khi admin reset
	s.pendingPool = make(map[string]*PendingEntry[T])
}

// AddToPending stores an event in the pending pool keyed by its nonce.
func (s *NonceOrderState[T]) AddToPending(nonce *big.Int, event T, txHash common.Hash, blockNumber *big.Int) {
	s.pendingPool[nonce.String()] = &PendingEntry[T]{
		Event:       event,
		TxHash:      txHash,
		Nonce:       nonce,
		Timestamp:   time.Now(),
		BlockNumber: blockNumber,
	}
}

// AddToPendingIfMissing adds the event to the pending pool when its nonce is in missingNonces
// and not already present. Returns true if an entry was added.
func (s *NonceOrderState[T]) AddToPendingIfMissing(
	nonce *big.Int, event T, txHash common.Hash, blockNumber *big.Int,
	missingNonces []*big.Int,
) bool {
	for _, missing := range missingNonces {
		if nonce.Cmp(missing) == 0 {
			nonceStr := nonce.String()
			if _, exists := s.pendingPool[nonceStr]; !exists {
				s.AddToPending(nonce, event, txHash, blockNumber)
				logger.Info("📥 Added missing event from GetLogs: Nonce=%v", nonce)
				return true
			}
			return false
		}
	}
	return false
}

// AddToPendingIfNeeded adds the event to pending pool if nonce >= nextExpectedNonce
// and not already present. Used by periodic scan — no missingNonces array needed.
// Returns true if an entry was added.
func (s *NonceOrderState[T]) AddToPendingIfNeeded(
	nonce *big.Int, event T, txHash common.Hash, blockNumber *big.Int,
) bool {
	// Nonce đã xử lý rồi → bỏ qua
	if nonce.Cmp(s.nextExpectedNonce) < 0 {
		return false
	}
	nonceStr := nonce.String()
	if _, exists := s.pendingPool[nonceStr]; !exists {
		s.AddToPending(nonce, event, txHash, blockNumber)
		logger.Info("📥 [Scan] Added event: Nonce=%v (nextExpected=%v)", nonce, s.nextExpectedNonce)
		return true
	}
	return false
}

// GetSampleTxHash returns the TxHash of any event currently in the pending pool.
// Used to determine the upper block boundary when querying missing events.
func (s *NonceOrderState[T]) GetSampleTxHash() (common.Hash, bool) {
	for _, e := range s.pendingPool {
		return e.TxHash, true
	}
	return common.Hash{}, false
}

// GetMissingNonces returns nonces in the range [nextExpected, upToNonce] that are
// not yet present in the pending pool.
func (s *NonceOrderState[T]) GetMissingNonces(upToNonce *big.Int) []*big.Int {
	missing := make([]*big.Int, 0)
	current := new(big.Int).Set(s.nextExpectedNonce)
	for current.Cmp(upToNonce) <= 0 {
		if _, exists := s.pendingPool[current.String()]; !exists {
			missing = append(missing, new(big.Int).Set(current))
		}
		current.Add(current, big.NewInt(1))
	}
	return missing
}

// AllMissingFound returns true when every nonce in missingNonces exists in the pending pool.
func (s *NonceOrderState[T]) AllMissingFound(missingNonces []*big.Int) bool {
	for _, n := range missingNonces {
		if _, exists := s.pendingPool[n.String()]; !exists {
			return false
		}
	}
	return true
}

// CountFoundInMissing returns how many of the missingNonces are now in the pending pool.
func (s *NonceOrderState[T]) CountFoundInMissing(missingNonces []*big.Int) int {
	count := 0
	for _, n := range missingNonces {
		if _, exists := s.pendingPool[n.String()]; exists {
			count++
		}
	}
	return count
}

// HasPending checks if a nonce exists in the pending pool.
func (s *NonceOrderState[T]) HasPending(nonceStr string) bool {
	_, exists := s.pendingPool[nonceStr]
	return exists
}

// UpdateLastBlockNumber updates the last processed block number in-memory.
// Used by periodic scan to avoid re-scanning old blocks.
func (s *NonceOrderState[T]) UpdateLastBlockNumber(blockNumber *big.Int) {
	logger.Info("UpdateLastBlockNumber: blockNumber=%v, lastBlockNumber=%v", blockNumber, s.lastBlockNumber)
	if blockNumber != nil && blockNumber.Cmp(s.lastBlockNumber) > 0 {
		s.lastBlockNumber = new(big.Int).Set(blockNumber)
	}
}

// ProcessPending drains the pending pool in strict nonce order, calling executeFunc for each
// consecutive event starting at nextExpectedNonce. Stops on the first gap or error.
func (s *NonceOrderState[T]) ProcessPending(executeFunc func(event T, blockNumber *big.Int) error) {
	processedCount := 0
	for {
		nonceStr := s.nextExpectedNonce.String()
		pending, exists := s.pendingPool[nonceStr]
		if !exists {
			logger.Info("🛑 No more consecutive events in pending pool (NextExpected=%v, Pool size=%d, Processed=%d)",
				s.nextExpectedNonce, len(s.pendingPool), processedCount)
			break
		}
		if err := executeFunc(pending.Event, pending.BlockNumber); err != nil {
			// if strings.Contains(err.Error(), "Message not found") {
			// 	logger.Error("❌ Message not found for pending event (Nonce=%v): %v", pending.Nonce, err)
			// 	delete(s.pendingPool, nonceStr)
			// 	continue
			// }
			logger.Warn("⚠️ Transient error executing pending event (Nonce=%v), will retry: %v", pending.Nonce, err)
			break
		}
		delete(s.pendingPool, nonceStr)
		s.Advance(pending.BlockNumber)
		processedCount++
		logger.Info("✅ Processed event: Nonce=%v, NextExpected=%v", pending.Nonce, s.nextExpectedNonce)
	}
}
