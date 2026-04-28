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

// updateAndPersistLastHandledCommitIndex updates the commit_index in memory and persists it
func (bp *BlockProcessor) updateAndPersistLastHandledCommitIndex(index uint32) {
	if index == 0 {
		return
	}
	storage.UpdateLastHandledCommitIndex(index)
	geiAuthority := GetGEIAuthority()
	if geiAuthority != nil {
		geiAuthority.RecordCommitIndex(index)
	}
	value := utils.Uint32ToBytes(index)
	if bp.storageManager != nil && bp.storageManager.GetStorageBackupDb() != nil {
		bp.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitIndexHashKey.Bytes(), value)
	}
}

// updateAndPersistLastExecutedCommitHash updates the commit hash in memory and persists it
func (bp *BlockProcessor) updateAndPersistLastExecutedCommitHash(hash []byte) {
	if len(hash) == 0 {
		return
	}
	storage.UpdateLastExecutedCommitHash(hash)
	if bp.storageManager != nil && bp.storageManager.GetStorageBackupDb() != nil {
		bp.storageManager.GetStorageBackupDb().Put(storage.LastExecutedCommitHashKey.Bytes(), hash)
	}
}

// geiWorker processes coalesced GEI updates, sending only the latest to commitChannel
func (bp *BlockProcessor) geiWorker() {
	for index := range bp.geiUpdateChan {
		latest := index
		drained := 0
	DRAIN_LOOP:
		for {
			select {
			case n := <-bp.geiUpdateChan:
				if n > latest {
					latest = n
				}
				drained++
			default:
				break DRAIN_LOOP
			}
		}

		job := CommitJob{
			Block:           nil,
			GlobalExecIndex: latest,
		}
		select {
		case bp.commitChannel <- job:
		default:
			bp.commitChannel <- job
		}
	}
}

// pushAsyncGEIUpdate pushes an empty commit update to the commitChannel.
// This ensures that the global_exec_index is persisted to DB *strictly after*
// any pending blocks with transactions. Prevents GEI from racing ahead of lost async blocks.
func (bp *BlockProcessor) pushAsyncGEIUpdate(index uint64, hash []byte) {
	if index == 0 {
		return
	}
	// We only use the channel to track the highest GEI. The hash will be stored immediately if batch-drain is not used.
	// Actually, batch-drain handles highestGEI itself, pushAsyncGEIUpdate is for normal processing.
	// Let's create an explicit CommitJob for the hash since geiUpdateChan only takes uint64.
	// But it's simpler to just persist it directly if we want.
	// Wait, geiUpdateChan coalesces GEI! We should just send a full CommitJob directly for empty commits!
	// No, geiUpdateChan is for coalescing. I will just update the memory state for now, and the CommitWorker will persist it when it gets the next job.
	// But empty blocks don't trigger CommitWorker unless they are large in number.
	// Actually, I can just persist it directly here!
	bp.updateAndPersistLastExecutedCommitHash(hash)
	select {
	case bp.geiUpdateChan <- index:
		// Sent successfully
	default:
		// Queue full, replace oldest item
		select {
		case <-bp.geiUpdateChan:
		default:
		}
		select {
		case bp.geiUpdateChan <- index:
		default:
		}
	}
}

// isEmptyCommit returns true if this committed epoch data contains zero transactions.
// Used by batch-drain to fast-track consecutive empty commits during catch-up sync.
func (bp *BlockProcessor) isEmptyCommit(epochData *pb.ExecutableBlock) bool {
	return len(epochData.Transactions) == 0
}
