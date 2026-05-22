// mapping_rebuild.go — Startup mapping integrity verification and rebuild.
//
// On startup, block number → hash mappings may be incomplete if the node
// was terminated before dirtyStorage was flushed to disk. This file provides
// a method to walk backwards from a given block through the parentHash chain
// and rebuild all missing mappings.
package blockchain

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	mtn_types "github.com/meta-node-blockchain/meta-node/types"
)

// RebuildMappingsFromBlock walks backwards from the given block through the
// parentHash chain and rebuilds any missing blockNumber→hash mappings.
// This should be called once at startup after InitBlockChain to ensure
// all historical mappings are present.
//
// Parameters:
//   - startBlock: the block to start walking from (typically startLastBlock)
//   - maxBlocks: maximum number of blocks to walk (0 = unlimited, walk to genesis)
//
// Returns the number of mappings rebuilt.
func (bc *BlockChain) RebuildMappingsFromBlock(startBlock mtn_types.Block, maxBlocks int) int {
	if startBlock == nil || bc.blockDatabase == nil || bc.storageManager == nil {
		return 0
	}

	startTime := time.Now()
	blk := startBlock
	rebuiltCount := 0
	checkedCount := 0
	consecutiveExisting := 0
	// After finding 50 consecutive existing mappings, we can assume the rest are intact
	const earlyExitThreshold = 50

	for blk != nil {
		bNum := blk.Header().BlockNumber()
		bHash := blk.Header().Hash()
		checkedCount++

		if maxBlocks > 0 && checkedCount > maxBlocks {
			break
		}

		// Check if mapping exists in DB (skip cache — we want to verify persistent storage)
		dbKey := []byte(fmt.Sprintf("%s%d", blockNumberPrefix, bNum))
		dbData, dbErr := bc.storageManager.GetStorageMapping().Get(dbKey)

		if dbErr != nil || dbData == nil || len(dbData) != common.HashLength {
			// Missing mapping — rebuild
			bc.storeToDirty(fmt.Sprintf("%s%d", blockNumberPrefix, bNum), bHash.Bytes())
			bc.blockNumberToHashCache.Store(bNum, cachedHash{
				hash:    bHash,
				addedAt: time.Now(),
			})
			rebuiltCount++
			consecutiveExisting = 0 // Reset counter
		} else {
			// Mapping exists — also verify hash correctness
			existingHash := common.BytesToHash(dbData)
			if existingHash != bHash {
				// Hash mismatch — update with correct hash from block chain
				logger.Warn("⚠️ [STARTUP-REBUILD] Block #%d mapping hash mismatch: DB=%s, chain=%s. Correcting.",
					bNum, existingHash.Hex()[:18]+"...", bHash.Hex()[:18]+"...")
				bc.storeToDirty(fmt.Sprintf("%s%d", blockNumberPrefix, bNum), bHash.Bytes())
				bc.blockNumberToHashCache.Store(bNum, cachedHash{
					hash:    bHash,
					addedAt: time.Now(),
				})
				rebuiltCount++
				consecutiveExisting = 0
			} else {
				// Correct mapping — cache it
				bc.blockNumberToHashCache.Store(bNum, cachedHash{
					hash:    existingHash,
					addedAt: time.Now(),
				})
				consecutiveExisting++
			}

			// Early exit: if we've found enough consecutive correct mappings,
			// the rest are very likely intact (they were committed together).
			if consecutiveExisting >= earlyExitThreshold {
				break
			}
		}

		if bNum == 0 {
			break
		}

		// Walk to parent
		parentHash := blk.Header().LastBlockHash()
		if parentHash == (common.Hash{}) {
			break
		}
		parentBlk, pErr := bc.blockDatabase.GetBlockByHash(parentHash)
		if pErr != nil || parentBlk == nil {
			logger.Warn("⚠️ [STARTUP-REBUILD] Cannot walk to parent of block #%d (parentHash=%s): %v",
				bNum, parentHash.Hex()[:18]+"...", pErr)
			break
		}
		blk = parentBlk
	}

	// Commit and flush rebuilt mappings
	if rebuiltCount > 0 {
		if commitErr := bc.Commit(); commitErr != nil {
			logger.Error("❌ [STARTUP-REBUILD] Failed to commit rebuilt mappings: %v", commitErr)
		} else if bc.storageManager != nil {
			if flushErr := bc.storageManager.GetStorageMapping().Flush(); flushErr != nil {
				logger.Error("❌ [STARTUP-REBUILD] Failed to flush mapping DB: %v", flushErr)
			}
		}
		logger.Info("🔄 [STARTUP-REBUILD] Rebuilt %d missing block→hash mappings (checked %d blocks, took %v)",
			rebuiltCount, checkedCount, time.Since(startTime))
	} else {
		logger.Info("✅ [STARTUP-REBUILD] All %d checked block→hash mappings are intact (took %v)",
			checkedCount, time.Since(startTime))
	}

	return rebuiltCount
}
