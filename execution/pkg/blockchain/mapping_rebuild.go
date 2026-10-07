// mapping_rebuild.go — Startup mapping integrity verification and rebuild.
//
// On startup, block number → hash and tx hash → block number mappings may be
// incomplete if the node was terminated before dirtyStorage was flushed to disk.
// This file provides a method to walk backwards from a given block through the
// parentHash chain and rebuild all missing mappings BEFORE serving queries.
package blockchain

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	mtn_types "github.com/meta-node-blockchain/meta-node/types"
)

// RebuildMappingsFromBlock walks backwards from the given block through the
// parentHash chain and rebuilds any missing blockNumber→hash, txHash→blockNumber,
// and ethHash→blsHash mappings. This is called once at startup after InitBlockChain
// to ensure all historical mappings are present before opening RPC endpoints.
//
// Parameters:
//   - startBlock: the block to start walking from (typically startLastBlock)
//   - maxBlocks: maximum number of blocks to walk (0 = unlimited, walk to genesis or last pruned block)
//
// Returns the number of mappings rebuilt and an error if recovery failed.
func (bc *BlockChain) RebuildMappingsFromBlock(startBlock mtn_types.Block, maxBlocks int) (int, error) {
	if startBlock == nil || bc.blockDatabase == nil || bc.storageManager == nil {
		return 0, nil
	}

	startTime := time.Now()
	blk := startBlock
	rebuiltCount := 0
	checkedCount := 0
	unflushedCount := 0
	const flushBatchSize = 5000
	lastPruned := bc.GetLastPrunedBlockNumber()

	flush := func() error {
		if unflushedCount == 0 {
			return nil
		}
		if commitErr := bc.Commit(); commitErr != nil {
			logger.Error("❌ [STARTUP-REBUILD] Failed to commit rebuilt mappings: %v", commitErr)
			return fmt.Errorf("failed to commit rebuilt mappings: %w", commitErr)
		}
		if bc.storageManager != nil {
			if flushErr := bc.storageManager.GetStorageMapping().Flush(); flushErr != nil {
				logger.Error("❌ [STARTUP-REBUILD] Failed to flush mapping DB: %v", flushErr)
				return fmt.Errorf("failed to flush mapping DB: %w", flushErr)
			}
		}
		unflushedCount = 0
		return nil
	}

	for blk != nil {
		bNum := blk.Header().BlockNumber()
		bHash := blk.Header().Hash()
		checkedCount++

		if maxBlocks > 0 && checkedCount > maxBlocks {
			break
		}

		if lastPruned > 0 && bNum <= lastPruned {
			break
		}

		// 1. Check if blockNumber -> hash mapping exists and is correct in DB
		dbKey := []byte(fmt.Sprintf("%s%d", blockNumberPrefix, bNum))
		dbData, dbErr := bc.storageManager.GetStorageMapping().Get(dbKey)

		if dbErr != nil || dbData == nil || len(dbData) != common.HashLength {
			// Missing mapping — rebuild
			bc.storeToDirty(fmt.Sprintf("%s%d", blockNumberPrefix, bNum), bHash.Bytes())
			rebuiltCount++
			unflushedCount++
			if unflushedCount >= flushBatchSize {
				if err := flush(); err != nil {
					return rebuiltCount, err
				}
			}
		} else {
			existingHash := common.BytesToHash(dbData)
			if existingHash != bHash {
				logger.Warn("⚠️ [STARTUP-REBUILD] Block #%d mapping hash mismatch: DB=%s, chain=%s. Correcting.",
					bNum, existingHash.Hex()[:18]+"...", bHash.Hex()[:18]+"...")
				bc.storeToDirty(fmt.Sprintf("%s%d", blockNumberPrefix, bNum), bHash.Bytes())
				rebuiltCount++
				unflushedCount++
				if unflushedCount >= flushBatchSize {
					if err := flush(); err != nil {
						return rebuiltCount, err
					}
				}
			}
		}

		// Keep only recent blocks in cache (<= 100 blocks) to prevent RAM bloat
		if checkedCount <= 100 {
			bc.blockNumberToHashCache.Store(bNum, cachedHash{
				hash:    bHash,
				addedAt: time.Now(),
			})
		}

		// 2. Check and rebuild all txHash -> blockNumber and ethHash -> blsHash mappings
		// Note: Do NOT flood in-memory tx caches (txHashToBlockNumber / ethHashMapBlsHash)
		// during recovery. Queries naturally lazy-load and cache entries upon miss.
		txHashes := blk.Transactions()
		if len(txHashes) > 0 {
			blockNumberBytes := make([]byte, 8)
			binary.BigEndian.PutUint64(blockNumberBytes, bNum)

			var txDB *transaction_state_db.TransactionStateDB
			var txDBInitErr error
			getTxDB := func() (*transaction_state_db.TransactionStateDB, error) {
				if txDB == nil && txDBInitErr == nil && bc.storageManager != nil {
					txDB, txDBInitErr = transaction_state_db.NewTransactionStateDBFromRoot(blk.Header().TransactionsRoot(), bc.storageManager.GetStorageTransaction())
				}
				return txDB, txDBInitErr
			}

			for _, tHash := range txHashes {
				// 2a. Verify txHash -> blockNumber
				txKey := []byte(txHashPrefix + tHash.Hex())
				txData, txErr := bc.storageManager.GetStorageMapping().Get(txKey)
				if txErr != nil || txData == nil || len(txData) != 8 || binary.BigEndian.Uint64(txData) != bNum {
					bc.storeToDirty(string(txKey), blockNumberBytes)
					rebuiltCount++
					unflushedCount++
					if unflushedCount >= flushBatchSize {
						if err := flush(); err != nil {
							return rebuiltCount, err
						}
					}
				}

				// 2b. Verify ethHash -> blsHash
				db, err := getTxDB()
				if err != nil {
					logger.Error("❌ [STARTUP-REBUILD] Failed to open txDB for block #%d root %s: %v",
						bNum, blk.Header().TransactionsRoot().Hex(), err)
					return rebuiltCount, fmt.Errorf("failed to open txDB for block #%d: %w", bNum, err)
				}
				if db != nil {
					tx, txErr := db.GetTransaction(tHash)
					if txErr != nil {
						logger.Error("❌ [STARTUP-REBUILD] Failed to read transaction %s for block #%d: %v",
							tHash.Hex(), bNum, txErr)
						return rebuiltCount, fmt.Errorf("failed to read transaction %s for block #%d: %w", tHash.Hex(), bNum, txErr)
					}
					if tx != nil {
						ethHash := tx.EthHash()
						if ethHash != (common.Hash{}) {
							ethKey := []byte(ethHashMapBlsHashPrefix + ethHash.Hex())
							ethData, ethErr := bc.storageManager.GetStorageMapping().Get(ethKey)
							if ethErr != nil || ethData == nil || len(ethData) != common.HashLength || common.BytesToHash(ethData) != tHash {
								bc.storeToDirty(string(ethKey), tHash.Bytes())
								rebuiltCount++
								unflushedCount++
								if unflushedCount >= flushBatchSize {
									if err := flush(); err != nil {
										return rebuiltCount, err
									}
								}
							}
						}
					}
				}
			}
		}

		if bNum == 0 {
			break
		}

		// Boundary check 1: Stop BEFORE reading pruned parent.
		// If lastPruned > 0 and bNum <= lastPruned + 1, the parent of bNum is <= lastPruned,
		// which has already been deleted by pruning. Attempting to load it would fail.
		if lastPruned > 0 && bNum <= lastPruned+1 {
			break
		}

		// Boundary check 2: Stop once maxBlocks blocks have been processed.
		// Avoid loading the parent of the last block in the requested range.
		if maxBlocks > 0 && checkedCount >= maxBlocks {
			break
		}

		// Walk to parent
		parentHash := blk.Header().LastBlockHash()
		if parentHash == (common.Hash{}) {
			break
		}
		parentBlk, pErr := bc.blockDatabase.GetBlockByHash(parentHash)
		if pErr != nil || parentBlk == nil {
			logger.Error("❌ [STARTUP-REBUILD] Cannot walk to parent of block #%d (parentHash=%s): %v",
				bNum, parentHash.Hex()[:18]+"...", pErr)
			return rebuiltCount, fmt.Errorf("cannot walk to parent of block #%d: %v", bNum, pErr)
		}
		blk = parentBlk
	}

	// Commit and flush any remaining rebuilt mappings
	if err := flush(); err != nil {
		return rebuiltCount, err
	}

	if rebuiltCount > 0 {
		logger.Info("🔄 [STARTUP-REBUILD] Rebuilt %d missing block/tx mappings (checked %d blocks, took %v)",
			rebuiltCount, checkedCount, time.Since(startTime))
	} else {
		logger.Info("✅ [STARTUP-REBUILD] All %d checked block & tx mappings are intact (took %v)",
			checkedCount, time.Since(startTime))
	}

	return rebuiltCount, nil
}
