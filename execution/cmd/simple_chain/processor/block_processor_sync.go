// @title processor/block_processor_sync.go
// @markdown processor/block_processor_sync.go - GEI ordering, fork-safety, and block sync processing
package processor

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// processSingleEpochData processes a single epoch data with full implementation
func (bp *BlockProcessor) processSingleEpochData(
	epochData *pb.ExecutableBlock,
	nextExpectedGlobalExecIndex *uint64,
	currentBlockNumber *uint64,
	pendingBlocks map[uint64]*pb.ExecutableBlock,
	skippedCommitsWithTxs map[uint64]*pb.ExecutableBlock,
	fileLogger *loggerfile.FileLogger,
) {
PROCESS_SINGLE_EPOCH_DATA_START:
	fmt.Printf("⚙️ [PROCESSOR] Called processSingleEpochData for GEI=%d\n", epochData.GetGlobalExecIndex())
	globalExecIndex := epochData.GetGlobalExecIndex()
	commitIndex := epochData.GetCommitIndex()

	// Use commitIndex to avoid unused variable warning
	_ = commitIndex

	// ═══════════════════════════════════════════════════════════════════════════
	// FAST-PATH: Skip expensive DB operations for zero-transaction commits
	// During sync, ~95%+ of commits are empty consensus rounds (0 transactions).
	// The full processing path does LAZY REFRESH (DB queries), epoch detection,
	// Case 1/2/3 ordering, block creation — all unnecessary for empty commits.
	// This fast-path: validates ordering → updates GEI → checks epoch → returns.
	// ═══════════════════════════════════════════════════════════════════════════
	totalTxsQuick := len(epochData.Transactions)
	if totalTxsQuick == 0 {
		// ORDERING: Must still validate sequential order
		if globalExecIndex < *nextExpectedGlobalExecIndex {
			// Old/duplicate — skip silently
			return
		}
		if globalExecIndex > *nextExpectedGlobalExecIndex {
			// Apply epoch boundary gap skip for fresh start
			gapSize := globalExecIndex - *nextExpectedGlobalExecIndex
			actualLastBlockDB := storage.GetLastBlockNumber()
			persistedGEI := storage.GetLastGlobalExecIndex()
			if gapSize <= 16 && actualLastBlockDB == 0 {
				*nextExpectedGlobalExecIndex = globalExecIndex
				logger.Info("📋 [FAST-EMPTY-GAP-SKIP] Fresh start gap skip: nextExpected → %d (gap=%d)", globalExecIndex, gapSize)
			} else if persistedGEI > 0 && persistedGEI >= *nextExpectedGlobalExecIndex {
				// Sync GEI from persisted state (GEI tracks ALL commits including empty)
				*nextExpectedGlobalExecIndex = persistedGEI + 1
				*currentBlockNumber = actualLastBlockDB
				if globalExecIndex < *nextExpectedGlobalExecIndex {
					return
				} else if globalExecIndex > *nextExpectedGlobalExecIndex {
					pendingBlocks[globalExecIndex] = epochData
					return
				}
			} else if gapSize > 200 && actualLastBlockDB > 0 && persistedGEI > 0 && persistedGEI < globalExecIndex {
				// SNAPSHOT-RESTORE GAP BRIDGE (same as full-path, see line ~420)
				*nextExpectedGlobalExecIndex = globalExecIndex
				*currentBlockNumber = actualLastBlockDB
				logger.Warn("🔗 [FAST-EMPTY GAP BRIDGE] Snapshot restore gap: nextExpected → %d (gap=%d, persistedGEI=%d, DB=%d)",
					globalExecIndex, gapSize, persistedGEI, actualLastBlockDB)
			} else {
				pendingBlocks[globalExecIndex] = epochData
				return
			}
		}

		// Sequential empty commit — update GEI and advance
		bp.pushAsyncGEIUpdate(globalExecIndex)
		*nextExpectedGlobalExecIndex = globalExecIndex + 1

		// ═══════════════════════════════════════════════════════════════
		// LAZY REFRESH for SyncOnly empty-commit fast-path:
		// HandleSyncBlocksRequest writes blocks to LevelDB in a separate
		// goroutine (Rust UDS handler). For SyncOnly nodes that ONLY
		// receive empty commits, the full LAZY REFRESH (line ~449) never
		// runs because it's after the fast-path return. Without this,
		// bp.lastBlock stays at genesis → RPC eth_blockNumber returns 0.
		// This lightweight check syncs bp.lastBlock from DB whenever
		// storage advances, keeping RPC responses accurate.
		// ═══════════════════════════════════════════════════════════════
		{
			storageLastBlockNum := storage.GetLastBlockNumber()
			bpLastBlock := bp.GetLastBlock()
			bpLastBlockNum := uint64(0)
			if bpLastBlock != nil {
				bpLastBlockNum = bpLastBlock.Header().BlockNumber()
			}
			if storageLastBlockNum > bpLastBlockNum {
				blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(storageLastBlockNum)
				if ok {
					freshBlock, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
					if err == nil && freshBlock != nil {
						bp.SetLastBlock(freshBlock)
						*currentBlockNumber = storageLastBlockNum
						bp.nextBlockNumber.Store(storageLastBlockNum + 1)
						headerCopy := freshBlock.Header()
						bp.chainState.SetcurrentBlockHeader(&headerCopy)
						logger.Info("🔄 [SYNC-ONLY REFRESH] Updated in-memory state from DB: block #%d → #%d",
							bpLastBlockNum, storageLastBlockNum)
					}
				}
			}
		}

		// EPOCH AUTO-ADVANCE: Check epoch from block data (cheap — only field access)
		epochNum := epochData.GetEpoch()
		if epochNum > 0 {
			commitTimestampMs := epochData.GetCommitTimestampMs()
			bp.chainState.CheckAndUpdateEpochFromBlock(epochNum, commitTimestampMs)

			// ═══════════════════════════════════════════════════════════════
			// EPOCH BOUNDARY BLOCK: When epoch changes, even with 0 txs we must
			// create a block marking the epoch boundary. Falls through to the full
			// processing path instead of returning early. This block ensures:
			//   1. Chain always has a block at epoch boundary (for restore)
			//   2. OnBlockCommitted() fires → snapshot trigger works correctly
			//   3. State consistency — new epoch has corresponding block number
			// ═══════════════════════════════════════════════════════════════
			lastBlock := bp.GetLastBlock()
			lastBlockEpoch := uint64(0)
			if lastBlock != nil {
				lastBlockEpoch = lastBlock.Header().Epoch()
			}
			if epochNum > lastBlockEpoch && lastBlock != nil {
				logger.Info("🔄 [EPOCH-BOUNDARY] Epoch %d→%d detected in 0-tx commit (GEI=%d). Creating boundary block.",
					lastBlockEpoch, epochNum, globalExecIndex)
				// Don't return — fall through to full processing path below
				// The full path will create a block with 0 transactions
				goto EPOCH_BOUNDARY_FALLTHROUGH
			}
		}

		// Drain pending blocks
		if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
			delete(pendingBlocks, *nextExpectedGlobalExecIndex)
			epochData = pendingBlock
			goto PROCESS_SINGLE_EPOCH_DATA_START
		}
		return
	}
EPOCH_BOUNDARY_FALLTHROUGH:
	// ═══════════════════════════════════════════════════════════════════════════
	// END FAST-PATH — blocks with transactions (or epoch boundary) follow full path
	// ═══════════════════════════════════════════════════════════════════════════

	// LAZY REFRESH: Ensure bp.lastBlock is up-to-date with storage
	// After SyncBlocks, storage may be ahead of bp.lastBlock. This check detects
	// staleness and refreshes from DB, preventing fork from stale parent hash.
	// This replaces the old syncCompletionCallback mechanism with a simpler approach.
	storageLastBlockNum := storage.GetLastBlockNumber()
	bpLastBlock := bp.GetLastBlock()
	bpLastBlockNum := uint64(0)
	if bpLastBlock != nil {
		bpLastBlockNum = bpLastBlock.Header().BlockNumber()
	}
	if storageLastBlockNum > bpLastBlockNum {
		// bp.lastBlock is stale (likely post-sync), refresh from storage
		// Use centralized CommitBlockState with WithRebuildTries to ensure ALL state
		// components (header, tries, mappings, counter) are updated atomically.
		// This fixes the SyncOnly→Validator fork where stale tries caused different
		// execution results.
		blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(storageLastBlockNum)
		if ok {
			freshBlock, err := bp.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
			if err == nil && freshBlock != nil {
				// ═══════════════════════════════════════════════════════════════
				// STATE-AWARENESS GUARD (LEGACY — defense-in-depth):
				// Since Phase 1 (sync_and_execute_blocks), synced blocks are
				// always executed via NOMT, so trie roots should always match.
				// This guard is kept as a safety net for unforeseen edge cases.
				//
				// Original purpose: After snapshot restore, P2P sync stored
				// blocks in LevelDB but NOMT state stayed at snapshot point.
				// Advancing bp.lastBlock caused new blocks to use stale trie.
				// ═══════════════════════════════════════════════════════════════
				currentTrieRoot := bp.chainState.GetAccountStateDB().Trie().Hash()
				targetStateRoot := freshBlock.Header().AccountStatesRoot()

				if currentTrieRoot != targetStateRoot && currentTrieRoot != (common.Hash{}) && targetStateRoot != (common.Hash{}) {
					logger.Warn("🛡️ [LAZY REFRESH] State mismatch! trie_root=%s ≠ target block #%d stateRoot=%s. "+
						"NOT advancing (P2P-synced blocks not executed by NOMT — snapshot restore?). "+
						"Staying at block #%d until Rust sends commits for sequential execution.",
						currentTrieRoot.Hex()[:18]+"...", storageLastBlockNum, targetStateRoot.Hex()[:18]+"...", bpLastBlockNum)
				} else {
					bp.SetLastBlock(freshBlock)
					newNextBlock := storageLastBlockNum + 1
					bp.nextBlockNumber.Store(newNextBlock)

					// Centralized state update: header + mappings + counter + tries
					if _, err := bp.chainState.CommitBlockState(freshBlock,
						blockchain.WithRebuildTries(),
					); err != nil {
						logger.Error("🔄 [LAZY REFRESH] Failed to rebuild state from fresh block #%d: %v",
							storageLastBlockNum, err)
					} else {
						logger.Debug("🔄 [LAZY REFRESH] ✅ Updated stale state: %d → %d (header + tries + mappings refreshed)",
							bpLastBlockNum, storageLastBlockNum)

						// CRITICAL (Feb 2026): Clear ALL cached state after sync→validator transition.
						// Two cache layers must be cleared:
						// 1. Go-side MVMApi instances (prevent stale accountStateDb references)
						// 2. C++ State::instances (prevent EVM from using stale nonce/balance
						//    from its internal concurrent_hash_map instead of calling GlobalStateGet)
						mvm.ClearAllMVMApi()
						mvm.CallClearAllStateInstances()
						logger.Debug("🔄 [LAZY REFRESH] 🗑️ Cleared Go MVMApi cache + C++ State instances (EVM will re-read fresh state)")
					}
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PHASE 3: HASH-MISMATCH-GUARD — Final defense against wrong parentHash fork.
	//
	// ROOT CAUSE: LAZY REFRESH uses strict inequality (storageLastBlockNum > bpLastBlockNum).
	// When both equal (e.g., both = 235), LAZY REFRESH does NOT fire. But Go P2P sync
	// (HandleSyncBlocksRequest) may have OVERWRITTEN LevelDB block #235 with the network's
	// authoritative version AFTER Rust already created its own block #235 in memory.
	// Result: bp.lastBlock.Hash() ≠ LevelDB hash → next block gets wrong parentHash → FORK.
	//
	// FIX: After LAZY REFRESH, when block numbers are equal (the "missed" case), compare
	// actual hashes. If they differ, force-refresh bp.lastBlock from LevelDB — the P2P-synced
	// (network-authoritative) version is always correct.
	// ═══════════════════════════════════════════════════════════════════════════
	if storageLastBlockNum == bpLastBlockNum && storageLastBlockNum > 0 && bpLastBlock != nil {
		storageHash, hashOk := blockchain.GetBlockChainInstance().GetBlockHashByNumber(storageLastBlockNum)
		if hashOk && storageHash != (common.Hash{}) {
			bpHash := bpLastBlock.Header().Hash()
			if bpHash != storageHash {
				freshBlock, err := bp.chainState.GetBlockDatabase().GetBlockByHash(storageHash)
				if err == nil && freshBlock != nil {
					bp.SetLastBlock(freshBlock)
					bp.nextBlockNumber.Store(storageLastBlockNum + 1)
					if _, err := bp.chainState.CommitBlockState(freshBlock,
						blockchain.WithRebuildTries(),
					); err != nil {
						logger.Error("🛡️ [HASH-MISMATCH-GUARD] Failed to rebuild state for block #%d: %v",
							storageLastBlockNum, err)
					} else {
						mvm.ClearAllMVMApi()
						mvm.CallClearAllStateInstances()
						logger.Warn("🛡️ [HASH-MISMATCH-GUARD] Block #%d hash mismatch detected! "+
							"bp.lastBlock=%s, storage=%s. Refreshed to P2P-synced authoritative version.",
							storageLastBlockNum, bpHash.Hex()[:18]+"...", storageHash.Hex()[:18]+"...")
					}
				}
			}
		}
	}

	// CRITICAL FORK-SAFETY: Extract consensus timestamp from Rust epochData
	// This timestamp is calculated by Rust Linearizer::calculate_commit_timestamp()
	// using median stake-weighted algorithm - deterministic across all nodes
	commitTimestampMs := epochData.GetCommitTimestampMs()
	if commitTimestampMs > 0 {
		logger.Debug("📅 [FORK-SAFETY] Using consensus timestamp from Rust: commit_timestamp_ms=%d (global_exec_index=%d)",
			commitTimestampMs, globalExecIndex)
	} else {
		logger.Warn("⚠️  [FORK-SAFETY] No consensus timestamp from Rust (commit_timestamp_ms=0), will use time.Now() as fallback (global_exec_index=%d)",
			globalExecIndex)
	}

	// Get epoch from first block if available
	epochNum := epochData.GetEpoch()

	// 🔍 DIAGNOSTIC: Detect epoch transition by comparing with last block and chain state
	lastBlock := bp.GetLastBlock()
	lastBlockEpoch := uint64(0)
	lastBlockNumber := uint64(0)

	if lastBlock != nil {
		lastBlockEpoch = lastBlock.Header().Epoch()
		lastBlockNumber = lastBlock.Header().BlockNumber()
	}
	// Log epoch boundary detection
	if epochNum != lastBlockEpoch && lastBlock != nil {
		logger.Debug("EPOCH BOUNDARY: epoch %d -> %d at gei=%d (last_block=#%d)", lastBlockEpoch, epochNum, globalExecIndex, lastBlockNumber)

		// ── Prometheus: epoch transition ───────────────────────────────────
		metrics.EpochTransitionsTotal.Inc()
		metrics.CurrentEpoch.Set(float64(epochNum))
	}

	// CRITICAL: Auto-advance Go epoch when incoming blocks have higher epoch
	// For SyncOnly nodes, blocks arrive via Rust P2P sync, and the epoch field
	// in each block indicates which epoch it belongs to. Without this check,
	// Go epoch stays at 0 forever because no EndOfEpoch system transaction
	// flows through the normal commit path.
	if epochNum > 0 {
		bp.chainState.CheckAndUpdateEpochFromBlock(epochNum, commitTimestampMs)
	}

	// 4. Process received data
	// Monitoring update removed per user request

	// CRITICAL FORK-SAFETY: Validate block order
	// All nodes must execute blocks strictly in order

	// Case 1: Duplicate/Old block using known GlobalExecIndex (likely retransmission)
	// CRITICAL FIX for epoch transition: If this duplicate block has transactions but we
	// processed an empty block with the same index (from epoch transition race), save it
	// for potential replacement when processing next sequential block.
	if globalExecIndex < *nextExpectedGlobalExecIndex {
		// Count transactions in this duplicate block
		duplicateTxCount := len(epochData.Transactions)

		if duplicateTxCount > 0 {
			// This duplicate has transactions - save for potential empty block replacement
			// Check if we just processed an empty block with this index (race condition in epoch transition)
			lastBlock := bp.GetLastBlock()
			if lastBlock != nil && lastBlock.Header().BlockNumber() == globalExecIndex && len(lastBlock.Transactions()) == 0 {
				logger.Warn("🔄 [EPOCH-RACE-FIX] Received duplicate block global_exec_index=%d with %d txs AFTER processing empty block! Replacing empty block.",
					globalExecIndex, duplicateTxCount)
				// Store for replacement - will be processed when we need to commit
				skippedCommitsWithTxs[globalExecIndex] = epochData
				// Don't return - let the normal processing replace the empty block
				// Rewind nextExpectedGlobalExecIndex to allow re-processing
				*nextExpectedGlobalExecIndex = globalExecIndex
				goto PROCESS_BLOCK
			} else {
				// Save for later potential recovery (edge case)
				logger.Warn("⚠️ [FORK-SAFETY] Received old block global_exec_index=%d with %d txs, saving for potential recovery (expected %d)",
					globalExecIndex, duplicateTxCount, *nextExpectedGlobalExecIndex)
				skippedCommitsWithTxs[globalExecIndex] = epochData
			}
		} else {
			logger.Warn("⚠️ [FORK-SAFETY] Skipping old/duplicate empty block global_exec_index=%d (expected %d)",
				globalExecIndex, *nextExpectedGlobalExecIndex)
		}
		return
	}

	// Case 2: Future block (out-of-order)
	// Buffer it for later processing once we receive the gap blocks
	// CRITICAL FORK-SAFETY: NEVER skip blocks! Always buffer and wait for sequential processing.
	if globalExecIndex > *nextExpectedGlobalExecIndex {
		gapSize := globalExecIndex - *nextExpectedGlobalExecIndex

		// SAFE FIX: Try to sync nextExpectedGlobalExecIndex from DB
		// This handles case where TxsProcessor (Network Sync) updated DB but we didn't track it
		// IMPORTANT: Only sync if DB is ahead, never jump ahead of DB!
		actualLastBlockDB := storage.GetLastBlockNumber()
		persistedGEI := storage.GetLastGlobalExecIndex()
		// Sync from persisted GEI (tracks ALL commits), not blockNumber (tracks only non-empty)
		if persistedGEI > 0 && persistedGEI >= *nextExpectedGlobalExecIndex {
			oldExpected := *nextExpectedGlobalExecIndex
			*nextExpectedGlobalExecIndex = persistedGEI + 1
			*currentBlockNumber = actualLastBlockDB
			logger.Info("🔄 [DB-SYNC] Synced nextExpectedGlobalExecIndex from persisted GEI: old=%d, new=%d (persisted_gei=%d, DB last_block=%d)",
				oldExpected, *nextExpectedGlobalExecIndex, persistedGEI, actualLastBlockDB)

			// Re-evaluate after sync
			if globalExecIndex < *nextExpectedGlobalExecIndex {
				logger.Warn("⚠️ [DB-SYNC] Block %d is now old after sync (expected %d), skipping",
					globalExecIndex, *nextExpectedGlobalExecIndex)
				return
			} else if globalExecIndex == *nextExpectedGlobalExecIndex {
				logger.Info("✅ [DB-SYNC] After sync, block %d is now sequential! Processing...", globalExecIndex)
				// Fall through to process the block
			} else {
				// Still a gap - buffer and wait (NO JUMPING!)
				newGap := globalExecIndex - *nextExpectedGlobalExecIndex
				logger.Warn("⏳ [FORK-SAFETY] After DB sync, still gap=%d, buffering block %d (expected %d). Waiting for missing blocks...",
					newGap, globalExecIndex, *nextExpectedGlobalExecIndex)
				pendingBlocks[globalExecIndex] = epochData
				return
			}
		} else if gapSize <= 16 && actualLastBlockDB == 0 {
			// ═══════════════════════════════════════════════════════════════
			// EPOCH BOUNDARY GAP SKIP (SyncOnly fresh start):
			// Go has processed NOTHING (DB block=0) and first blocks from
			// Rust arrive at GEI > 1 (e.g. GEI=9). The gap represents epoch 0
			// empty consensus blocks that peers no longer store.
			// Safe: Go has processed ZERO blocks, so no data is skipped.
			// Max gap 16 prevents accidentally skipping real missing data.
			// ═══════════════════════════════════════════════════════════════
			oldExpected := *nextExpectedGlobalExecIndex
			*nextExpectedGlobalExecIndex = globalExecIndex
			logger.Info("📋 [EPOCH-GAP-SKIP] Fresh start gap skip: nextExpected=%d → %d (DB=0, gap=%d, first block at GEI=%d)",
				oldExpected, globalExecIndex, gapSize, globalExecIndex)
			// Fall through to process the block
		} else if gapSize > 200 && actualLastBlockDB > 0 && persistedGEI > 0 && persistedGEI < globalExecIndex {
			// ═══════════════════════════════════════════════════════════════
			// SNAPSHOT-RESTORE GAP BRIDGE (Apr 2026):
			//
			// After snapshot restore, Go starts with persistedGEI from
			// the snapshot (e.g., 1265). Meanwhile, Rust's cold-start DAG
			// replays and its GEI GUARD skips all commits with GEI ≤ Go's
			// reported GEI. The first commit Rust actually SENDS to Go has
			// a much higher GEI (e.g., 4802) because the network has
			// advanced significantly since the snapshot.
			//
			// At this point:
			//   - persistedGEI = 1265 (from snapshot)
			//   - nextExpected = 1266
			//   - incoming GEI = 4802
			//   - actualLastBlockDB > 0 (has blocks from snapshot + peer sync)
			//   - gap = 3536 (>> 200)
			//
			// The missing GEIs (1266-4801) will NEVER arrive because Rust's
			// GEI GUARD already skipped them. Buffering and waiting = deadlock.
			//
			// SAFETY: This is safe because:
			// 1. Rust's GEI GUARD verified Go already has the state for those GEIs
			// 2. The blocks from peer sync + snapshot cover the execution state
			// 3. The GEI-REGRESSION guard (line ~637) will catch stale commits
			// 4. The ANTI-INFLATION guard (line ~670) will adopt synced blocks
			// 5. We only bridge when gap > 200 (normal ordering gaps are < 50)
			// ═══════════════════════════════════════════════════════════════
			oldExpected := *nextExpectedGlobalExecIndex
			*nextExpectedGlobalExecIndex = globalExecIndex
			*currentBlockNumber = actualLastBlockDB
			logger.Warn("🔗 [SNAPSHOT-RESTORE GAP BRIDGE] Large gap detected after snapshot restore! "+
				"Jumping nextExpected=%d → %d (gap=%d, persistedGEI=%d, DB_block=%d). "+
				"Rust GEI GUARD already skipped commits %d-%d.",
				oldExpected, globalExecIndex, gapSize, persistedGEI, actualLastBlockDB,
				oldExpected, globalExecIndex-1)
			// Fall through to process the block
		} else {
			// FORK-SAFETY CRITICAL: Still a gap - buffer and wait (NO JUMPING!)
			newGap := globalExecIndex - *nextExpectedGlobalExecIndex
			logger.Warn("⏳ [FORK-SAFETY] Out-of-order block. Gap=%d, buffering block %d (expected %d). Waiting for missing blocks...",
				newGap, globalExecIndex, *nextExpectedGlobalExecIndex)
			pendingBlocks[globalExecIndex] = epochData
			return
		}
	}

	// Case 3: Sequential block (globalExecIndex == *nextExpectedGlobalExecIndex)
	// Proceed to PROCESS_BLOCK
	logger.Debug("✅ [FORK-SAFETY] Processing sequential block global_exec_index=%d", globalExecIndex)

PROCESS_BLOCK:
	// ═══════════════════════════════════════════════════════════════════════════
	// SYNC DEDUP GUARD: If sync handler already wrote this block to LevelDB
	// while we were preparing to create it, skip creation and import the synced block.
	// This closes the race window between LAZY REFRESH (checked earlier) and now.
	// ═══════════════════════════════════════════════════════════════════════════
	{
		syncedLastBlock := storage.GetLastBlockNumber()
		// NOTE: Use globalExecIndex (the block we're about to create), NOT *currentBlockNumber
		// which hasn't been updated yet at this point and still holds the PREVIOUS block number.
		if syncedLastBlock >= globalExecIndex && globalExecIndex > 0 {
			// Sync already wrote this block — import it instead of creating a new one
			syncedHash, hashOk := blockchain.GetBlockChainInstance().GetBlockHashByNumber(globalExecIndex)
			if hashOk {
				syncedBlock, syncErr := bp.chainState.GetBlockDatabase().GetBlockByHash(syncedHash)
				if syncErr == nil && syncedBlock != nil {
					logger.Info("🔄 [SYNC DEDUP] Block #%d already in storage from sync (hash=%s), importing instead of creating",
						globalExecIndex, syncedHash.Hex()[:18]+"...")

					// Update in-memory state from the synced block
					bp.SetLastBlock(syncedBlock)
					bp.nextBlockNumber.Store(globalExecIndex + 1)
					if _, err := bp.chainState.CommitBlockState(syncedBlock,
						blockchain.WithRebuildTries(),
					); err != nil {
						logger.Error("🔄 [SYNC DEDUP] Failed to rebuild state for synced block #%d: %v", globalExecIndex, err)
					}

					// Advance to next block and check pending
					*nextExpectedGlobalExecIndex = globalExecIndex + 1
					if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
						logger.Info("✅ [SYNC DEDUP] Found pending block %d after importing synced block", *nextExpectedGlobalExecIndex)
						delete(pendingBlocks, *nextExpectedGlobalExecIndex)
						epochData = pendingBlock
						globalExecIndex = epochData.GetGlobalExecIndex()
						*currentBlockNumber = globalExecIndex
						goto PROCESS_BLOCK
					}
					return
				}
			}
		}
	}

	// ================= BLOCK CREATION FROM RUST COMMIT =================
	// EACH COMMIT FROM RUST = EXACTLY ONE BLOCK IN GO
	// Merge all transactions from all blocks in commit into one Go block

	// STEP 1: Block number will be assigned AFTER the zero-tx check below
	// This prevents premature assignment for commits that get skipped
	lastBlock = bp.GetLastBlock() // Re-fetch lastBlock

	// CRITICAL FIX: Handle empty commit (commit past barrier) - SKIP block creation entirely
	// Empty commits don't change state, so creating blocks for them wastes CPU/IO
	// All nodes receive the same commits from Rust → all skip the same empties → no fork
	// EXCEPTION: If this empty commit triggers an epoch transition, we MUST create an empty boundary block!
	isEpochBoundary := false
	if lastBlock != nil && epochNum > lastBlock.Header().Epoch() {
		isEpochBoundary = true
		logger.Info("🔄 [EPOCH-BOUNDARY] Processing empty commit at GEI=%d as boundary block for epoch %d→%d",
			globalExecIndex, lastBlock.Header().Epoch(), epochNum)
	}

	if len(epochData.Transactions) == 0 && !isEpochBoundary {
		logger.Debug("⏭️  [SKIP-EMPTY] Skipping empty commit: global_exec_index=%d (no state change)", globalExecIndex)

		// Update GlobalExecIndex tracking (persistent)
		bp.pushAsyncGEIUpdate(globalExecIndex)

		// CRITICAL FORK-SAFETY: Update next expected global_exec_index and process pending blocks
		if globalExecIndex > 0 {
			*nextExpectedGlobalExecIndex = globalExecIndex + 1

			// Check pending blocks
			if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
				logger.Info("✅ [FORK-SAFETY] Found pending block global_exec_index=%d after skipped empty commit", *nextExpectedGlobalExecIndex)
				delete(pendingBlocks, *nextExpectedGlobalExecIndex)
				epochData = pendingBlock
				globalExecIndex = epochData.GetGlobalExecIndex()
				commitIndex = epochData.GetCommitIndex()
				commitTimestampMs = epochData.GetCommitTimestampMs()
				epochNum = epochData.GetEpoch()
				goto PROCESS_BLOCK
			} else if skippedBlock, exists := skippedCommitsWithTxs[*nextExpectedGlobalExecIndex]; exists {
				skippedTotalTxs := len(skippedBlock.Transactions)
				logger.Info("✅ [LAG-HANDLING] Found skipped commit global_exec_index=%d after skipped empty (txs=%d)", *nextExpectedGlobalExecIndex, skippedTotalTxs)
				delete(skippedCommitsWithTxs, *nextExpectedGlobalExecIndex)
				epochData = skippedBlock
				globalExecIndex = epochData.GetGlobalExecIndex()
				commitIndex = epochData.GetCommitIndex()
				commitTimestampMs = epochData.GetCommitTimestampMs()
				epochNum = epochData.GetEpoch()
				goto PROCESS_BLOCK
			}
			return
		}
		return
	}

	logger.Debug("📦 [TX FLOW] Processing %d transactions straight from ExecutableBlock payload", len(epochData.Transactions))
	// fileLogger is passed as parameter from caller (MEMORY FIX: avoid per-call os.File leak)

	// CRITICAL FORK-SAFETY: Commit any pending empty block before processing new block with transactions
	// This ensures empty block is committed to DB if no replacement arrived
	// All nodes will commit empty block at the same point (when processing next block) → fork-safe
	lastBlockPending := bp.GetLastBlock()
	lastCommittedBlockNumber := storage.GetLastBlockNumber()
	if lastBlockPending != nil && lastBlockPending.Header().BlockNumber() > lastCommittedBlockNumber && len(lastBlockPending.Transactions()) == 0 {
		// There's an empty block that hasn't been committed yet - commit it now before processing new block
		emptyBlockNum := lastBlockPending.Header().BlockNumber()
		logger.Info("💾 [FORK-SAFETY] Committing pending empty block #%d before processing new block (global_exec_index=%d)",
			emptyBlockNum, globalExecIndex)

		// Centralized commit (fork-safe: all nodes will do this at the same point)
		if _, err := bp.chainState.CommitBlockState(lastBlockPending,
			blockchain.WithPersistToDB(),
		); err != nil {
			logger.Error("❌ [FORK-SAFETY] Failed to commit pending empty block #%d: %v", emptyBlockNum, err)
		} else {
			logger.Debug("✅ [FORK-SAFETY] Empty block #%d committed to database successfully", emptyBlockNum)
		}
	}

	// STEP 2: Process direct transactions from payload
	// OPTIMIZATION: Pre-allocate slice to prevent GC allocations during deserialization
	allTransactions := make([]types.Transaction, 0, len(epochData.Transactions))
	totalTxsFromRust := 0

	for txIdx, ms := range epochData.Transactions {
		// Skip empty transaction data
		if len(ms.Digest) == 0 {
			logger.Warn("⚠️  [TX FLOW] Empty transaction data in Rust block, transaction[%d], skipping", txIdx)
			continue
		}

		// Unmarshal as single Transaction first
		singleTx, err := transaction.UnmarshalTransaction(ms.Digest)
		if err == nil {
			logger.Info("✅ [TX FLOW] UnmarshalTransaction SUCCESS for tx[%d]", txIdx)
			allTransactions = append(allTransactions, singleTx)
			totalTxsFromRust++
			continue
		}
		logger.Warn("⚠️ [TX FLOW] UnmarshalTransaction FAILED for tx[%d]: %v", txIdx, err)

		// Fallback: try unmarshal as Transactions message (backward compatibility)
		transactions, err := transaction.UnmarshalTransactions(ms.Digest)
		if err != nil {
			logger.Error("❌ [TX FLOW] Failed to unmarshal transaction[%d] in Rust block: %v (size=%d bytes)", txIdx, err, len(ms.Digest))
			continue
		}
		logger.Info("✅ [TX FLOW] UnmarshalTransactions SUCCESS for tx[%d]: returned %d txs", txIdx, len(transactions))
		allTransactions = append(allTransactions, transactions...)
		totalTxsFromRust += len(transactions)
	}

	// If no transactions after unmarshal, skip (same as empty commit)
	if len(allTransactions) == 0 {
		logger.Info("⏭️  [SKIP-EMPTY] SILENT DROP: len(allTransactions) is 0 after unmarshal: global_exec_index=%d. totalTxsFromRust=%d", globalExecIndex, totalTxsFromRust)
		bp.pushAsyncGEIUpdate(globalExecIndex)

		// CRITICAL FORK-SAFETY: Update next expected global_exec_index and process pending blocks
		if globalExecIndex > 0 {
			*nextExpectedGlobalExecIndex = globalExecIndex + 1

			if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
				logger.Info("✅ [FORK-SAFETY] Found next pending block in buffer: global_exec_index=%d", *nextExpectedGlobalExecIndex)
				delete(pendingBlocks, *nextExpectedGlobalExecIndex)
				epochData = pendingBlock
				globalExecIndex = epochData.GetGlobalExecIndex()
				commitIndex = epochData.GetCommitIndex()
				epochNum = epochData.GetEpoch()
				goto PROCESS_BLOCK
			}

			if skippedBlock, exists := skippedCommitsWithTxs[*nextExpectedGlobalExecIndex]; exists {
				logger.Info("✅ [LAG-HANDLING] Found skipped commit: global_exec_index=%d", *nextExpectedGlobalExecIndex)
				delete(skippedCommitsWithTxs, *nextExpectedGlobalExecIndex)
				epochData = skippedBlock
				globalExecIndex = epochData.GetGlobalExecIndex()
				commitIndex = epochData.GetCommitIndex()
				epochNum = epochData.GetEpoch()
				goto PROCESS_BLOCK
			}
		}
		return
	}

	fileLogger.Info("block: --------------------------------%v txs=%d", *currentBlockNumber, len(allTransactions))

	// 🚀 SPEEDUP: Trigger background I/O preloading IMMEDIATELY after assembling TX slice.
	// This allows LevelDB reads to overlap with CPU-bound guards (GEI regression, anti-inflation)
	preloadChan := bp.transactionProcessor.StartPreloadAccounts(allTransactions)

	// STEP 3: Process all transactions together into a single block
	logger.Debug("⚙️  [TX FLOW] Processing %d transactions (from ExecutableBlock) for Go block #%d",
		len(allTransactions), *currentBlockNumber)

	// NOW assign sequential block number (AFTER zero-tx check, so only non-empty commits get a number)
	// CRITICAL FIX: We completely shift the block number sequence tracking to Rust.
	// Rust sends explicit `block_number` via ExecutableBlock, removing any risk of
	// local sequence inflation between Go Nodes.
	*currentBlockNumber = epochData.GetBlockNumber()
	logger.Debug("📊 [BLOCK-NUM] Assigning block #%d directly from Rust payload for global_exec_index=%d (txs=%d)",
		*currentBlockNumber, globalExecIndex, len(allTransactions))

	// ═══════════════════════════════════════════════════════════════════════════
	// BLOCK NUMBER REGRESSION GUARD: Prevent executing old blocks from DAG replay.
	// ═══════════════════════════════════════════════════════════════════════════
	{
		actualLastBlockDB := storage.GetLastBlockNumber()
		if actualLastBlockDB > 0 && *currentBlockNumber <= actualLastBlockDB {
			logger.Warn("🛡️ [BLOCK-NUM-REGRESSION] Skipping stale block: incoming block_number=%d ≤ last block DB=%d (GEI=%d). "+
				"This commit is from a replayed DAG or lag — not executing.",
				*currentBlockNumber, actualLastBlockDB, globalExecIndex)

			// Still update GEI counter so the processor advances past this commit
			bp.pushAsyncGEIUpdate(globalExecIndex)
			*nextExpectedGlobalExecIndex = globalExecIndex + 1

			// Check pending blocks
			if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
				delete(pendingBlocks, *nextExpectedGlobalExecIndex)
				epochData = pendingBlock
				goto PROCESS_SINGLE_EPOCH_DATA_START
			}
			return
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// GEI REGRESSION GUARD: Prevent creating blocks from stale DAG replay.
	//
	// ROOT CAUSE: After snapshot restore, Rust's cold-start guard clears when it
	// detects a "live" commit (recent timestamp + go_gei < commit_gei). However,
	// the replayed DAG uses a wrong epoch_base_index, producing commits with GEI
	// LOWER than what P2P sync already wrote. For example:
	//   - P2P sync wrote blocks up to block 4239 with GEI=18022
	//   - Rust's first post-cold-start commit has GEI=17344 (from wrong epoch_base)
	//   - Go would create block 4240 with GEI=17344 → hash differs from network
	//
	// FIX: If the incoming commit's GEI is LESS THAN the last block's GEI (from
	// P2P sync), the commit is STALE and must be skipped. The last block's GEI
	// represents the network's authoritative state at that height. Any commit with
	// a lower GEI is from a replayed DAG with incorrect epoch_base_index.
	//
	// This guard only triggers during post-restore catch-up, not during normal
	// consensus (where GEI always monotonically increases).
	// ═══════════════════════════════════════════════════════════════════════════
	{
		lastBlockGEI := uint64(0)
		if lastBlock != nil {
			lastBlockGEI = lastBlock.Header().GlobalExecIndex()
		}
		if lastBlockGEI > 0 && globalExecIndex <= lastBlockGEI {
			logger.Info("🛡️ [GEI-REGRESSION] Skipping stale commit: incoming GEI=%d ≤ last block GEI=%d (block #%d). "+
				"This commit is from a replayed DAG with wrong epoch_base_index — not creating block.",
				globalExecIndex, lastBlockGEI, *currentBlockNumber)

			// Still update GEI counter so the processor advances past this commit
			bp.pushAsyncGEIUpdate(globalExecIndex)
			*nextExpectedGlobalExecIndex = globalExecIndex + 1

			// Check pending blocks
			if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
				delete(pendingBlocks, *nextExpectedGlobalExecIndex)
				epochData = pendingBlock
				goto PROCESS_SINGLE_EPOCH_DATA_START
			}
			return
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// ANTI-INFLATION GUARD: Prevent block inflation after snapshot restore.
	//
	// ROOT CAUSE: After restore, Go receives blocks from TWO sources simultaneously:
	//   1. P2P sync (HandleSyncBlocksRequest): stores network blocks at specific block numbers
	//   2. Rust consensus (this path): creates NEW blocks at lastBlock+1
	//
	// When both run concurrently, Rust commits create blocks faster than the network,
	// inflating the block count past the network (e.g., 1519 vs 1003).
	//
	// FIX: Before creating a block, check if P2P sync already stored a block at
	// this block number. If it does, ADOPT the synced block (which is authoritative
	// from the network majority) and skip local creation.
	// ═══════════════════════════════════════════════════════════════════════════
	{
		existingHash, existsInMap := blockchain.GetBlockChainInstance().GetBlockHashByNumber(*currentBlockNumber)
		if existsInMap && existingHash != (common.Hash{}) {
			existingBlock, fetchErr := bp.chainState.GetBlockDatabase().GetBlockByHash(existingHash)
			if fetchErr == nil && existingBlock != nil {
				logger.Info("🛡️ [ANTI-INFLATION] Block #%d already exists from P2P sync (hash=%s). Adopting synced block instead of creating new one (gei=%d)",
					*currentBlockNumber, existingHash.Hex()[:18]+"...", globalExecIndex)

				// Adopt the synced block as our last block
				bp.SetLastBlock(existingBlock)
				bp.nextBlockNumber.Store(*currentBlockNumber + 1)
				headerCopy := existingBlock.Header()
				bp.chainState.SetcurrentBlockHeader(&headerCopy)

				// Rebuild tries from synced block to ensure state consistency
				if _, err := bp.chainState.CommitBlockState(existingBlock,
					blockchain.WithRebuildTries(),
				); err != nil {
					logger.Error("🛡️ [ANTI-INFLATION] Failed to rebuild state from synced block #%d: %v", *currentBlockNumber, err)
				} else {
					// Clear cached state to prevent stale EVM reads
					mvm.ClearAllMVMApi()
					mvm.CallClearAllStateInstances()
					logger.Info("🛡️ [ANTI-INFLATION] ✅ Adopted synced block #%d, rebuilt tries, cleared caches", *currentBlockNumber)
				}

				// Update GEI tracking
				bp.pushAsyncGEIUpdate(globalExecIndex)

				// Advance and check pending blocks
				*nextExpectedGlobalExecIndex = globalExecIndex + 1
				if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
					delete(pendingBlocks, *nextExpectedGlobalExecIndex)
					epochData = pendingBlock
					goto PROCESS_SINGLE_EPOCH_DATA_START
				}
				return
			}
		}
	}

	// FORK-SAFETY: Deduplication and sorting are now handled consistently by Rust in block_sending.rs

	blockTimeSec := commitTimestampMs / 1000 // Convert ms→s for deterministic EVM block.timestamp
	processTxStart := time.Now()
	// bp.transactionProcessor.logBackendStartMs()
	accumulatedResults, err := bp.transactionProcessor.ProcessTransactions(allTransactions, blockTimeSec, preloadChan)
	processTxDuration := time.Since(processTxStart)
	if err != nil {
		logger.Error("❌ [TX FLOW] Failed to process transactions for block #%d: %v", *currentBlockNumber, err)
		return // Skip this commit, wait for next one
	}
	logger.Debug("[PERF] ProcessTransactions: %d txs in %v (%.0f tx/s) for block #%d",
		len(allTransactions), processTxDuration, float64(len(allTransactions))/processTxDuration.Seconds(), *currentBlockNumber)

	// ⚠️ VALIDATION: Check if any transaction is missing its receipt
	if len(accumulatedResults.Receipts) != len(allTransactions) {
		logger.Error("❌ [RECEIPT VALIDATION] MISMATCH: block #%d has %d transactions but only %d receipts!",
			*currentBlockNumber, len(allTransactions), len(accumulatedResults.Receipts))

		// Build map of existing receipts
		receiptMap := make(map[common.Hash]bool)
		for _, rcp := range accumulatedResults.Receipts {
			receiptMap[rcp.TransactionHash()] = true
		}

		// Find transactions without receipt
		missingReceipts := []string{}
		for _, tx := range allTransactions {
			txHash := tx.Hash()
			if !receiptMap[txHash] {
				missingReceipts = append(missingReceipts, txHash.Hex())
				logger.Error("   ❌ [MISSING RECEIPT] Transaction without receipt: hash=%s, from=%s, to=%s, nonce=%d",
					txHash.Hex(), tx.FromAddress().Hex(), tx.ToAddress().Hex(), tx.GetNonce())
			}
		}
		logger.Error("   📋 [MISSING RECEIPT] Total missing receipts: %d. Missing tx hashes: %v",
			len(missingReceipts), missingReceipts)
	} else {
		logger.Debug("✅ [RECEIPT VALIDATION] All %d transactions have receipts for block #%d",
			len(allTransactions), *currentBlockNumber)
	}

	// Receipt detail logging removed for performance

	// STEP 4: Create a single block from all processed transactions
	logger.Debug("🔨 [TX FLOW] Creating Go block #%d from merged transactions", *currentBlockNumber)
	// CRITICAL FORK-SAFETY: Get leader address from Rust consensus for deterministic block hash
	leaderAddr := bp.GetLeaderAddress(epochData.GetLeaderAddress(), epochData.GetLeaderAuthorIndex())
	createBlockStart := time.Now()

	// CC-1: Construct standard batch_id for end-to-end tracing
	batchID := fmt.Sprintf("E%dC%dG%d", epochNum, commitIndex, globalExecIndex)

	newBlock := bp.createBlockFromResults(accumulatedResults, *currentBlockNumber, epochNum, true, batchID, commitTimestampMs, globalExecIndex, leaderAddr)
	createBlockDuration := time.Since(createBlockStart)

	blockHash := newBlock.Header().Hash().Hex()
	logger.Info("⏱️  [PERF] createBlockFromResults: %d txs in %v for block #%d (hash=%s, gei=%d)",
		len(newBlock.Transactions()), createBlockDuration, *currentBlockNumber, blockHash[:16]+"...", globalExecIndex)

	// Update GlobalExecIndex tracking (persistent)
	bp.pushAsyncGEIUpdate(globalExecIndex)

	logger.Debug("Lastblock header: %v", newBlock.Header())
	logger.Debug("Transactions in block: %d TXs", len(newBlock.Transactions()))
	logger.Debug("📦 [SNAPSHOT] Block #%d created in memory (%d transactions). Will be committed to DB by commitWorker (Rust will wait for commit before snapshot)",
		*currentBlockNumber, len(allTransactions))

	// Removed targeted explicit GC: It caused enormous Stop-The-World (STW) latency
	// spikes as block sizes scaled. Memory is now managed dynamically via GOMEMLIMIT
	// to allow generational, concurrent sweeping.
	if len(allTransactions) > 5000 {
		logger.Debug("🧹 [MEMORY] High TX count block #%d (%d txs), memory managed by GOMEMLIMIT natively", *currentBlockNumber, len(allTransactions))
	}

	// CRITICAL FORK-SAFETY: Update next expected global_exec_index and process pending blocks
	if globalExecIndex > 0 {
		*nextExpectedGlobalExecIndex = globalExecIndex + 1
		logger.Debug("🔄 [FORK-SAFETY] Updated nextExpectedGlobalExecIndex to %d after processing block #%d (global_exec_index=%d).",
			*nextExpectedGlobalExecIndex, *currentBlockNumber, globalExecIndex)

		// Process any pending blocks that are now in order
		if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
			logger.Info("✅ [FORK-SAFETY] Processing pending block with global_exec_index=%d", *nextExpectedGlobalExecIndex)
			delete(pendingBlocks, *nextExpectedGlobalExecIndex)
			epochData = pendingBlock
			goto PROCESS_SINGLE_EPOCH_DATA_START
		} else if skippedBlock, exists := skippedCommitsWithTxs[*nextExpectedGlobalExecIndex]; exists {
			logger.Info("✅ [LAG-HANDLING] Processing skipped commit: global_exec_index=%d", *nextExpectedGlobalExecIndex)
			delete(skippedCommitsWithTxs, *nextExpectedGlobalExecIndex)
			epochData = skippedBlock
			goto PROCESS_SINGLE_EPOCH_DATA_START
		}
		return
	} else {
		// No global_exec_index - increment block number normally
		*currentBlockNumber++
	}
}
