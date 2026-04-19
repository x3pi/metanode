package processor

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

// TransactionPendingStatus giữ nguyên
type TransactionPendingStatus string

const (
	StatusInPool     TransactionPendingStatus = "InPool"
	StatusProcessing TransactionPendingStatus = "Processing"
	StatusExcluded   TransactionPendingStatus = "Excluded"
	StatusConfirmed  TransactionPendingStatus = "Confirmed"
	StatusFailed     TransactionPendingStatus = "Failed"
)

// TransactionPending giữ nguyên
type TransactionPending struct {
	Tx        types.Transaction
	Status    TransactionPendingStatus
	Timestamp time.Time
}

// nonceIndexKey is used for O(1) nonce conflict detection.
type nonceIndexKey struct {
	addr  common.Address
	nonce uint64
}

// PendingTransactionManager được tái cấu trúc.
// Dùng hai index song song:
//   - pendingTxs: sync.Map[txHash → TransactionPending]  (primary store)
//   - nonceIndex: sync.Map[addr+nonce → txHash]          (O(1) conflict check)
type PendingTransactionManager struct {
	// Primary store: txHash → TransactionPending
	pendingTxs sync.Map
	count      atomic.Int64

	// Secondary index for O(1) nonce conflict detection: addr+nonce → txHash
	nonceIndex sync.Map
}

// NewPendingTransactionManager tạo instance mới
func NewPendingTransactionManager() *PendingTransactionManager {
	return &PendingTransactionManager{}
}

// Add adds a transaction to the pool.
// Cập nhật cả pendingTxs và nonceIndex cùng lúc.
func (ptm *PendingTransactionManager) Add(tx types.Transaction, initialStatus TransactionPendingStatus) error {
	if tx == nil {
		return fmt.Errorf("transaction cannot be nil")
	}
	txHash := tx.Hash()

	newItem := TransactionPending{
		Tx:        tx,
		Status:    initialStatus,
		Timestamp: time.Now(),
	}

	_, loaded := ptm.pendingTxs.LoadOrStore(txHash, newItem)
	if !loaded {
		// New entry — register in nonceIndex too
		key := nonceIndexKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
		ptm.nonceIndex.Store(key, txHash)
		ptm.count.Add(1)
	}

	return nil
}

// AddBatch adds a slice of transactions to the pool at once
func (ptm *PendingTransactionManager) AddBatch(txs []types.Transaction, initialStatus TransactionPendingStatus) error {
	for _, tx := range txs {
		if tx == nil {
			continue // Skip nil to avoid panics
		}

		txHash := tx.Hash()
		newItem := TransactionPending{
			Tx:        tx,
			Status:    initialStatus,
			Timestamp: time.Now(),
		}

		_, loaded := ptm.pendingTxs.LoadOrStore(txHash, newItem)
		if !loaded {
			// New entry — register in nonceIndex too
			key := nonceIndexKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
			ptm.nonceIndex.Store(key, txHash)
			ptm.count.Add(1)
		}
	}
	return nil
}

// Get sử dụng Load của sync.Map
func (ptm *PendingTransactionManager) Get(txHash common.Hash) (TransactionPending, bool) {
	value, exists := ptm.pendingTxs.Load(txHash)
	if !exists {
		return TransactionPending{}, false
	}
	return value.(TransactionPending), true
}

// UpdateStatus cập nhật trạng thái
func (ptm *PendingTransactionManager) UpdateStatus(txHash common.Hash, newStatus TransactionPendingStatus) bool {
	value, exists := ptm.pendingTxs.Load(txHash)
	if !exists {
		return false
	}

	pendingTx := value.(TransactionPending)
	pendingTx.Status = newStatus
	pendingTx.Timestamp = time.Now()
	ptm.pendingTxs.Store(txHash, pendingTx)
	return true
}

// Remove xóa transaction khỏi pool và nonceIndex.
func (ptm *PendingTransactionManager) Remove(txHash common.Hash) bool {
	oldVal, loaded := ptm.pendingTxs.LoadAndDelete(txHash)
	if loaded {
		ptm.count.Add(-1)
		// Xóa khỏi nonceIndex nếu vẫn trỏ về hash này
		old := oldVal.(TransactionPending)
		key := nonceIndexKey{addr: old.Tx.FromAddress(), nonce: old.Tx.GetNonce()}
		if existing, ok := ptm.nonceIndex.Load(key); ok {
			if existingHash, ok2 := existing.(common.Hash); ok2 && existingHash == txHash {
				ptm.nonceIndex.Delete(key)
			}
		}
	}
	return loaded
}

// RemoveTransactions duyệt và xóa
func (ptm *PendingTransactionManager) RemoveTransactions(txs []TransactionPending) {
	for _, pendingTx := range txs {
		ptm.Remove(pendingTx.Tx.Hash())
	}
}

// Count trả về số lượng pending tx (O(1) via atomic)
func (ptm *PendingTransactionManager) Count() int {
	return int(ptm.count.Load())
}

// GetAll sử dụng Range của sync.Map
func (ptm *PendingTransactionManager) GetAll() []TransactionPending {
	var allTxs []TransactionPending
	ptm.pendingTxs.Range(func(key, value interface{}) bool {
		allTxs = append(allTxs, value.(TransactionPending))
		return true
	})
	return allTxs
}

// RemoveAll reset lại sync.Map và biến đếm
func (ptm *PendingTransactionManager) RemoveAll() {
	ptm.pendingTxs = sync.Map{}
	ptm.nonceIndex = sync.Map{}
	ptm.count.Store(0)
}

// HasNonceConflict kiểm tra O(1) thông qua nonceIndex.
// If a new TX arrives with same (addr, nonce) but different hash,
// the old TX is EVICTED (replaced) — matching Ethereum nonce replacement semantics.
// This prevents stuck nonces when clients retry with the same nonce.
func (ptm *PendingTransactionManager) HasNonceConflict(tx types.Transaction) bool {
	key := nonceIndexKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
	inputHash := tx.Hash()

	existingRaw, exists := ptm.nonceIndex.Load(key)
	if !exists {
		return false
	}

	existingHash, ok := existingRaw.(common.Hash)
	if !ok {
		ptm.nonceIndex.Delete(key)
		return false
	}

	if existingHash == inputHash {
		return false // Same tx — not a conflict
	}

	// Check if existing pending entry still exists
	_, ok2 := ptm.pendingTxs.Load(existingHash)
	if !ok2 {
		// Stale nonceIndex entry — clean up
		ptm.nonceIndex.Delete(key)
		return false
	}

	// NONCE REPLACEMENT (Mar 2026): Allow new TX to replace old TX with same nonce.
	// This is standard behavior (like Ethereum tx repricing/replacement).
	// The sender re-submitted with same nonce → old TX is no longer valid.
	logger.Info("🔄 [TX FLOW] Nonce replacement: evicting old TX %s for new TX %s (addr=%s, nonce=%d)",
		existingHash.Hex()[:16]+"...",
		inputHash.Hex()[:16]+"...",
		tx.FromAddress().Hex()[:10]+"...",
		tx.GetNonce())
	ptm.Remove(existingHash)
	return false
}

// StartCleanupLoop starts a background goroutine that periodically sweeps
// and removes expired entries from pendingTxs and nonceIndex.
// This prevents memory leaks when entries are added but never checked for conflicts.
func (ptm *PendingTransactionManager) StartCleanupLoop(stopCh <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				now := time.Now()
				var removed, total int
				ptm.pendingTxs.Range(func(key, value interface{}) bool {
					total++
					pending := value.(TransactionPending)
					if now.Sub(pending.Timestamp) > PendingTimeout {
						txHash := key.(common.Hash)
						ptm.pendingTxs.Delete(txHash)
						ptm.count.Add(-1)
						// Clean nonceIndex
						nKey := nonceIndexKey{addr: pending.Tx.FromAddress(), nonce: pending.Tx.GetNonce()}
						if existing, ok := ptm.nonceIndex.Load(nKey); ok {
							if existingHash, ok2 := existing.(common.Hash); ok2 && existingHash == txHash {
								ptm.nonceIndex.Delete(nKey)
							}
						}
						removed++
					}
					return true
				})
				if removed > 0 {
					logger.Info("[PENDING-CLEANUP] Swept %d expired entries (remaining: %d)", removed, total-removed)
				}
			}
		}
	}()
}

// GetOldTransactionsForRemoval giữ nguyên logic duyệt
func (ptm *PendingTransactionManager) GetOldTransactionsForRemoval(duration time.Duration) []TransactionPending {
	var oldTxs []TransactionPending
	now := time.Now()

	ptm.pendingTxs.Range(func(key, value interface{}) bool {
		pendingTx := value.(TransactionPending)
		age := now.Sub(pendingTx.Timestamp)
		if age > duration {
			oldTxs = append(oldTxs, pendingTx)
			logger.Debug("[TX FLOW] Found old pending transaction: txHash=%s, age=%v (threshold=%v), status=%s",
				pendingTx.Tx.Hash().Hex(),
				age,
				duration,
				pendingTx.Status)
		}
		return true
	})

	return oldTxs
}
