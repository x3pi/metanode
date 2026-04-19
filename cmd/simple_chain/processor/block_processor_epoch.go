// @title processor/block_processor_epoch.go
// @markdown processor/block_processor_epoch.go - Epoch transition helpers and GEI tracking
package processor

import (
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

// updateAndPersistLastGlobalExecIndex updates GEI in memory and persists it to BackupDB
// SHOULD NEVER BE CALLED DIRECTLY DURING BLOCK PROCESSING, USE pushAsyncGEIUpdate INSTEAD!
func (bp *BlockProcessor) updateAndPersistLastGlobalExecIndex(index uint64) {
	storage.UpdateLastGlobalExecIndex(index)
	value := utils.Uint64ToBytes(index)
	if bp.storageManager != nil && bp.storageManager.GetStorageBackupDb() != nil {
		bp.storageManager.GetStorageBackupDb().Put(storage.LastGlobalExecIndexHashKey.Bytes(), value)
	}
}

// pushAsyncGEIUpdate pushes an empty commit update to the commitChannel.
// This ensures that the global_exec_index is persisted to DB *strictly after*
// any pending blocks with transactions. Prevents GEI from racing ahead of lost async blocks.
func (bp *BlockProcessor) pushAsyncGEIUpdate(index uint64) {
	if index == 0 {
		return
	}
	job := CommitJob{
		Block:           nil, // Empty commit, just update GEI
		GlobalExecIndex: index,
	}
	select {
	case bp.commitChannel <- job:
		// Sent successfully
	default:
		// Fallback blocking send if channel is full
		bp.commitChannel <- job
	}
}

// isEmptyCommit returns true if this committed epoch data contains zero transactions.
// Used by batch-drain to fast-track consecutive empty commits during catch-up sync.
func (bp *BlockProcessor) isEmptyCommit(epochData *pb.ExecutableBlock) bool {
	return len(epochData.Transactions) == 0
}
