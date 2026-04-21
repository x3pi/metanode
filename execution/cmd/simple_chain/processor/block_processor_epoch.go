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
func (bp *BlockProcessor) pushAsyncGEIUpdate(index uint64) {
	if index == 0 {
		return
	}
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
