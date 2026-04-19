package processor

import (
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// BlockBuffers manages out-of-order blocks received from consensus or network.
// It was extracted from the monolithic BlockProcessor struct to cleanly separate
// memory management of pending blocks from core execution logic.
type BlockBuffers struct {
	stateCommitBlockBuffer map[uint64]*block.Block // Buffer for out-of-order blocks
	stateCommitBufferMutex sync.Mutex              // Mutex for stateCommitBlockBuffer

	// Buffer cho các block nhận được từ mạng (dành cho sub-node trong TxsProcessor)
	subNodeBlockBuffer map[uint64]*storage.BackUpDb
	bufferMutex        sync.Mutex
}

// NewBlockBuffers creates a new instance of BlockBuffers.
func NewBlockBuffers() *BlockBuffers {
	return &BlockBuffers{
		stateCommitBlockBuffer: make(map[uint64]*block.Block),
		subNodeBlockBuffer:     make(map[uint64]*storage.BackUpDb),
	}
}

// StartCleanupWorkers starts background goroutines to prevent memory leaks from old buffered blocks.
func (bb *BlockBuffers) StartCleanupWorkers(getNextBlockNum func() uint64) {
	go bb.cleanupStateCommitBlockBuffer(getNextBlockNum)
	go bb.cleanupSubNodeBlockBuffer(getNextBlockNum)
}

// cleanupStateCommitBlockBuffer cleans up old blocks in buffer to prevent memory leaks.
// Removes blocks older than 1000 blocks from expected block number.
func (bb *BlockBuffers) cleanupStateCommitBlockBuffer(getNextBlockNum func() uint64) {
	ticker := time.NewTicker(1 * time.Minute) // Run every minute
	defer ticker.Stop()

	for range ticker.C {
		expectedBlockNum := getNextBlockNum()
		// Remove blocks older than 1000 blocks from expected (equivalent to ~5 minutes if 1 block/second)
		cutoffBlockNum := expectedBlockNum - 1000
		if cutoffBlockNum > expectedBlockNum { // Prevent underflow
			cutoffBlockNum = 0
		}

		// Iterate and remove old blocks
		bb.stateCommitBufferMutex.Lock()
		removedCount := 0
		for blockNum := range bb.stateCommitBlockBuffer {
			if blockNum < cutoffBlockNum {
				delete(bb.stateCommitBlockBuffer, blockNum)
				removedCount++
			}
		}
		bufferSize := len(bb.stateCommitBlockBuffer)
		bb.stateCommitBufferMutex.Unlock()

		if removedCount > 0 {
			logger.Warn("cleanupStateCommitBlockBuffer: Removed %d old blocks from buffer (buffer size: %d)", removedCount, bufferSize)
		}
		if bufferSize > 100 {
			logger.Warn("cleanupStateCommitBlockBuffer: Buffer size large (%d), possible block ordering issue", bufferSize)
		}
	}
}

// cleanupSubNodeBlockBuffer cleans up old blocks in subNodeBlockBuffer to prevent memory leaks.
// Removes blocks older than 1000 blocks from expected block number.
func (bb *BlockBuffers) cleanupSubNodeBlockBuffer(getNextBlockNum func() uint64) {
	ticker := time.NewTicker(1 * time.Minute) // Run every minute
	defer ticker.Stop()

	for range ticker.C {
		expectedBlockNum := getNextBlockNum()
		// Remove blocks older than 1000 blocks from expected
		cutoffBlockNum := expectedBlockNum - 1000
		if cutoffBlockNum > expectedBlockNum { // Prevent underflow
			cutoffBlockNum = 0
		}

		// Iterate and remove old blocks
		bb.bufferMutex.Lock()
		removedCount := 0
		for blockNum := range bb.subNodeBlockBuffer {
			if blockNum < cutoffBlockNum {
				delete(bb.subNodeBlockBuffer, blockNum)
				removedCount++
			}
		}
		bufferSize := len(bb.subNodeBlockBuffer)
		bb.bufferMutex.Unlock()

		if removedCount > 0 {
			logger.Warn("cleanupSubNodeBlockBuffer: Removed %d old blocks from buffer (buffer size: %d)", removedCount, bufferSize)
		}
		if bufferSize > 100 {
			logger.Warn("cleanupSubNodeBlockBuffer: Buffer size large (%d), possible block ordering issue", bufferSize)
		}
	}
}
