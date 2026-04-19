package processor

import (
	"sync"

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
}

// NewReceiptTracker creates and initializes a new ReceiptTracker.
func NewReceiptTracker() *ReceiptTracker {
	return &ReceiptTracker{}
}

// StoreTxHashConnEntry stores connection metadata allowing the RPC client
// to match an asynchronously delivered receipt to its pending request.
func (rt *ReceiptTracker) StoreTxHashConnEntry(txHash common.Hash, entry TxHashConnEntry) {
	rt.txHashConnectionMap.Store(txHash, entry)
}
