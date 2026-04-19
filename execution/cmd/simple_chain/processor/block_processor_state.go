// @title processor/block_processor_state.go
// @markdown processor/block_processor_state.go - State management and committer functionality
package processor

import (
	"encoding/binary"
	"time"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// stateCommitter is the single goroutine responsible for updating lastBlock and chainState sequentially
func (bp *BlockProcessor) stateCommitter() {
	logger.Info("✅ State Committer Worker đã khởi động")
	expectedBlockNum := bp.nextBlockNumber.Load()

	for newBlock := range bp.createdBlocksChan {
		blockNum := newBlock.Header().BlockNumber()

		if blockNum == expectedBlockNum {
			bp.applyBlockToState(newBlock)
			expectedBlockNum++

			// Vòng lặp để xử lý các block liền kề đã có trong buffer
			for {
				bp.stateCommitBufferMutex.Lock()
				bufferedBlock, ok := bp.stateCommitBlockBuffer[expectedBlockNum]
				if ok {
					delete(bp.stateCommitBlockBuffer, expectedBlockNum)
				}
				bp.stateCommitBufferMutex.Unlock()
				if ok {
					bp.applyBlockToState(bufferedBlock)
					expectedBlockNum++
				} else {
					break // Block tiếp theo chưa có trong buffer, quay lại chờ
				}
			}
		} else if blockNum > expectedBlockNum {
			// Nếu block đến sớm, đưa vào buffer để chờ
			bp.stateCommitBufferMutex.Lock()
			bp.stateCommitBlockBuffer[blockNum] = newBlock
			bp.stateCommitBufferMutex.Unlock()
		}
		// Bỏ qua các block có số hiệu cũ hơn (không nên xảy ra)
	}
}

// cleanupStateCommitBlockBuffer cleans up old blocks in buffer to prevent memory leaks
// Removes blocks older than 5 minutes from expected block number
func (bp *BlockProcessor) cleanupStateCommitBlockBuffer() {
	ticker := time.NewTicker(1 * time.Minute) // Run every minute
	defer ticker.Stop()

	for range ticker.C {
		expectedBlockNum := bp.nextBlockNumber.Load()
		// Remove blocks older than 1000 blocks from expected (equivalent to ~5 minutes if 1 block/second)
		cutoffBlockNum := expectedBlockNum - 1000
		if cutoffBlockNum > expectedBlockNum { // Prevent underflow
			cutoffBlockNum = 0
		}

		// Iterate and remove old blocks
		bp.stateCommitBufferMutex.Lock()
		removedCount := 0
		for blockNum := range bp.stateCommitBlockBuffer {
			if blockNum < cutoffBlockNum {
				delete(bp.stateCommitBlockBuffer, blockNum)
				removedCount++
			}
		}
		bufferSize := len(bp.stateCommitBlockBuffer)
		bp.stateCommitBufferMutex.Unlock()

		if removedCount > 0 {
			logger.Warn("cleanupStateCommitBlockBuffer: Removed %d old blocks from buffer (buffer size: %d)", removedCount, bufferSize)
		}
		if bufferSize > 100 {
			logger.Warn("cleanupStateCommitBlockBuffer: Buffer size large (%d), possible block ordering issue", bufferSize)
		}
	}
}

// cleanupSubNodeBlockBuffer cleans up old blocks in subNodeBlockBuffer to prevent memory leaks
// Removes blocks older than 1000 blocks from expected block number
func (bp *BlockProcessor) cleanupSubNodeBlockBuffer() {
	ticker := time.NewTicker(1 * time.Minute) // Run every minute
	defer ticker.Stop()

	for range ticker.C {
		expectedBlockNum := bp.nextBlockNumber.Load()
		// Remove blocks older than 1000 blocks from expected
		cutoffBlockNum := expectedBlockNum - 1000
		if cutoffBlockNum > expectedBlockNum { // Prevent underflow
			cutoffBlockNum = 0
		}

		// Iterate and remove old blocks
		bp.bufferMutex.Lock()
		removedCount := 0
		for blockNum := range bp.subNodeBlockBuffer {
			if blockNum < cutoffBlockNum {
				delete(bp.subNodeBlockBuffer, blockNum)
				removedCount++
			}
		}
		bufferSize := len(bp.subNodeBlockBuffer)
		bp.bufferMutex.Unlock()

		if removedCount > 0 {
			logger.Warn("cleanupSubNodeBlockBuffer: Removed %d old blocks from buffer (buffer size: %d)", removedCount, bufferSize)
		}
		if bufferSize > 100 {
			logger.Warn("cleanupSubNodeBlockBuffer: Buffer size large (%d), possible block ordering issue", bufferSize)
		}
	}
}

// applyBlockToState applies block to state in a thread-safe manner
func (bp *BlockProcessor) applyBlockToState(b *block.Block) {
	bp.lastBlockMutex.Lock()
	defer bp.lastBlockMutex.Unlock()

	if bp.GetLastBlock() == nil || b.Header().BlockNumber() > bp.GetLastBlock().Header().BlockNumber() {
		bp.SetLastBlock(b)
		headerCopy := b.Header()
		bp.chainState.SetcurrentBlockHeader(&headerCopy)
	}
}
func (bp *BlockProcessor) GetChainId(request network.Request) error {
	id := request.Message().ID()

	var chainId uint64
	if bp.config != nil && bp.config.ChainId != nil {
		chainId = bp.config.ChainId.Uint64()
	}

	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, chainId)

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.ChainId,
			ID:      id,
		},
		Body: buf,
	})
	return request.Connection().SendMessage(respMsg)
}

// inputTPSWorker monitors input TPS
func (bp *BlockProcessor) inputTPSWorker() {
	logger.Info("✅ Worker monitoring Input TPS initiated")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		count := bp.inputTxCounter.Swap(0)
		// Only log when TPS > 1000 to avoid spam
		if count > 1000 {
			logger.Info("INPUT_TPS: %d tx/s", count)
		}
	}
}
