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
// FORK-SAFETY: Also persists the current epoch to enable epoch-aware restart validation.
// On restart, if the persisted epoch doesn't match current_epoch, the commit_index
// belongs to a previous epoch and must be treated as 0.
func (bp *BlockProcessor) updateAndPersistLastHandledCommitIndex(index uint32) {
	if index == 0 {
		return
	}
	storage.UpdateLastHandledCommitIndex(index)

	// Determine current epoch from last block
	var currentEpoch uint64
	lastBlock := bp.GetLastBlock()
	if lastBlock != nil {
		currentEpoch = lastBlock.Header().Epoch()
	}

	storage.UpdateLastHandledCommitEpoch(currentEpoch)

	geiAuthority := GetGEIAuthority()
	if geiAuthority != nil {
		geiAuthority.RecordCommitIndexWithEpoch(index, currentEpoch)
	}
	if bp.storageManager != nil && bp.storageManager.GetStorageBackupDb() != nil {
		bp.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitIndexHashKey.Bytes(), utils.Uint32ToBytes(index))
		bp.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitEpochHashKey.Bytes(), utils.Uint64ToBytes(currentEpoch))
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
	for update := range bp.geiUpdateChan {
		latestGEI := update.GlobalExecIndex
		latestCommit := update.CommitIndex
		drained := 0
	DRAIN_LOOP:
		for {
			select {
			case u := <-bp.geiUpdateChan:
				if u.GlobalExecIndex > latestGEI {
					latestGEI = u.GlobalExecIndex
					latestCommit = u.CommitIndex
				}
				drained++
			default:
				break DRAIN_LOOP
			}
		}

		job := CommitJob{
			Block:           nil,
			GlobalExecIndex: latestGEI,
			CommitIndex:     latestCommit,
		}
		select {
		case bp.commitChannel <- job:
		default:
			bp.commitChannel <- job
		}
	}
}

// PushAsyncGEIUpdate pushes an empty commit update to the commitChannel.
// This ensures that the global_exec_index is persisted to DB *strictly after*
// any pending blocks with transactions. Prevents GEI from racing ahead of lost async blocks.
func (bp *BlockProcessor) PushAsyncGEIUpdate(index uint64, hash []byte, commitIndex uint32) {
	if index == 0 {
		return
	}
	// GO-AUTHORITATIVE FIX: Keep GEIAuthority counter in sync with every GEI
	// advancement. Without this, the full-path and BATCH-DRAIN could have
	// different GEIAuthority.lastAssignedGEI values, causing +1 offset.
	geiAuth := GetGEIAuthority()
	if geiAuth.IsEnabled() {
		geiAuth.AdvanceGEITo(index)
	}
	bp.updateAndPersistLastExecutedCommitHash(hash)
	
	update := AsyncGEIUpdate{
		GlobalExecIndex: index,
		CommitIndex:     commitIndex,
	}

	select {
	case bp.geiUpdateChan <- update:
		// Sent successfully
	default:
		// Queue full, replace oldest item
		select {
		case <-bp.geiUpdateChan:
		default:
		}
		select {
		case bp.geiUpdateChan <- update:
		default:
		}
	}
}

// isEmptyCommit returns true if this committed epoch data contains zero transactions.
// Used by batch-drain to fast-track consecutive empty commits during catch-up sync.
func (bp *BlockProcessor) isEmptyCommit(epochData *pb.ExecutableBlock) bool {
	return len(epochData.Transactions) == 0
}
