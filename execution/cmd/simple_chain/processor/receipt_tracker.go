package processor

import (
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
)

// ReceiptTracker manages the mapping of transactions to specific client connections
// to ensure transaction receipts are properly routed back to the submitters asynchronously.
// It also tracks pending receipts if connections are temporarily unavailable.
// It was extracted from the monolithic BlockProcessor struct.
type ReceiptTracker struct {
	// When BroadCastReceipts can't find a client connection, receipts are saved here.
	// When client reconnects (ProcessInitConnection), pending receipts are flushed.
	pendingReceipts sync.Map

	// consumed (deleted) in BroadCastReceipts after sending the receipt.
	txHashConnectionMap sync.Map

	// inFlightCount tracks the number of transactions currently awaiting receipt broadcast
	// (admitted by this validator into mempool/consensus/execution but not yet committed).
	inFlightCount atomic.Int64
}

// NewReceiptTracker creates and initializes a new ReceiptTracker.
func NewReceiptTracker() *ReceiptTracker {
	return &ReceiptTracker{}
}

// StoreTxHashConnEntry stores connection metadata allowing the RPC client
// to match an asynchronously delivered receipt to its pending request.
func (rt *ReceiptTracker) StoreTxHashConnEntry(txHash common.Hash, entry TxHashConnEntry) {
	if _, loaded := rt.txHashConnectionMap.Swap(txHash, entry); !loaded {
		rt.inFlightCount.Add(1)
	}
}

// GetTxHashConnEntry retrieves the connection metadata for a transaction hash.
func (rt *ReceiptTracker) GetTxHashConnEntry(txHash common.Hash) (TxHashConnEntry, bool) {
	val, ok := rt.txHashConnectionMap.Load(txHash)
	if !ok {
		return TxHashConnEntry{}, false
	}
	return val.(TxHashConnEntry), true
}

// TakeTxHashConnEntry atomically removes and returns the connection metadata for txHash,
// decrementing the in-flight transaction gauge when an entry was present.
func (rt *ReceiptTracker) TakeTxHashConnEntry(txHash common.Hash) (TxHashConnEntry, bool) {
	val, ok := rt.txHashConnectionMap.LoadAndDelete(txHash)
	if !ok {
		return TxHashConnEntry{}, false
	}
	if rt.inFlightCount.Add(-1) < 0 {
		rt.inFlightCount.Store(0)
	}
	entry, castOk := val.(TxHashConnEntry)
	return entry, castOk
}

// DeleteTxHashConnEntry atomically deletes a key from txHashConnectionMap and decrements inFlightCount.
func (rt *ReceiptTracker) DeleteTxHashConnEntry(key interface{}) bool {
	if _, ok := rt.txHashConnectionMap.LoadAndDelete(key); ok {
		if rt.inFlightCount.Add(-1) < 0 {
			rt.inFlightCount.Store(0)
		}
		return true
	}
	return false
}

// InFlightTxCount returns the current number of admitted transactions awaiting commit/receipt on this validator.
func (rt *ReceiptTracker) InFlightTxCount() int {
	if rt == nil {
		return 0
	}
	n := rt.inFlightCount.Load()
	if n < 0 {
		return 0
	}
	return int(n)
}

// ReconcileInFlightTxCount recounts live entries in txHashConnectionMap to self-heal any potential counter drift.
func (rt *ReceiptTracker) ReconcileInFlightTxCount() int {
	if rt == nil {
		return 0
	}
	var count int64
	rt.txHashConnectionMap.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	rt.inFlightCount.Store(count)
	return int(count)
}
