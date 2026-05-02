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
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
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
	logger.Debug("⚙️ [PROCESSOR] Called processSingleEpochData for GEI=%d", epochData.GetGlobalExecIndex())
	globalExecIndex := epochData.GetGlobalExecIndex()
	commitIndex := epochData.GetCommitIndex()
	epochNum := epochData.GetEpoch()

	// Compute the next GEI from Go's persisted state
	lastPersistedGEI := storage.GetLastGlobalExecIndex()
	if lastPersistedGEI >= *nextExpectedGlobalExecIndex && lastPersistedGEI > 0 {
		// Go's storage is ahead — sync in-memory state
		*nextExpectedGlobalExecIndex = lastPersistedGEI + 1
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// RUST-AUTHORITATIVE GEI: Go completely trusts Rust's global_exec_index.
	// Go passively executes the block and persists the incomingGEI.
	// ═══════════════════════════════════════════════════════════════════════════
	incomingGEI := epochData.GetGlobalExecIndex()
	globalExecIndex = incomingGEI
	commitIndex = epochData.GetCommitIndex()
	epochNum = epochData.GetEpoch()

	currentEpoch := uint64(0)
	lastBlock := bp.GetLastBlock()
	if lastBlock != nil {
		currentEpoch = lastBlock.Header().Epoch()
	}

	// ═════════════════════════════════════════════════════════════════════
	// CRITICAL FORK-SAFETY FIX: Epoch transition commit_index reset
	// ═════════════════════════════════════════════════════════════════════
	if epochNum > currentEpoch && lastBlock != nil {
		logger.Info("🔄 [GEI-AUTHORITY] Epoch %d→%d detected. Resetting lastHandledCommitIndex.", currentEpoch, epochNum)
		geiAuth := GetGEIAuthority()
		geiAuth.ResetCommitIndex()
		storage.UpdateLastHandledCommitIndex(0)
		if bp.storageManager != nil && bp.storageManager.GetStorageBackupDb() != nil {
			bp.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitIndexHashKey.Bytes(), utils.Uint32ToBytes(0))
		}
	}

	// Record the commit index to persist progress
	geiAuth := GetGEIAuthority()
	geiAuth.RecordCommitIndex(commitIndex)

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
			} else if gapSize > 20 && actualLastBlockDB > 0 && persistedGEI > 0 && persistedGEI < globalExecIndex {
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
		bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex)
		*nextExpectedGlobalExecIndex = globalExecIndex + 1

		// ═══════════════════════════════════════════════════════════════
		// LAZY REFRESH: DISABLED (FORK-SAFETY FIX 2026-04-29)
		//
		// This block imported P2P-synced blocks from LevelDB into the
		// in-memory state (bp.lastBlock, currentBlockNumber, chainState).
		// processSingleEpochData only runs on Master nodes, which MUST NOT
		// adopt P2P blocks — their state is managed by consensus execution.
		// Importing P2P blocks here corrupts the trie state and causes
		// hash mismatches in subsequent blocks.
		//
		// Original purpose (SyncOnly nodes) is now handled by
		// syncLastBlockFromDB goroutine in block_processor_db_sync.go.
		// ═══════════════════════════════════════════════════════════════

		// EPOCH AUTO-ADVANCE: Check epoch from block data (cheap — only field access)
		epochNum = epochData.GetEpoch()
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
				// The full path will create a block with 0 transactions.
				// CRITICAL FIX: Undo the GEI advance from line 85 so the full path doesn't reject it as a duplicate.
				*nextExpectedGlobalExecIndex = globalExecIndex
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

	// ═══════════════════════════════════════════════════════════════════════════
	// LAZY REFRESH + HASH-MISMATCH-GUARD: DISABLED (FORK-SAFETY FIX 2026-04-29)
	//
	// These guards imported P2P-synced blocks from LevelDB and replaced the
	// in-memory state (bp.lastBlock, tries, header, chainState). This caused
	// hash mismatches because:
	//   1. P2P blocks are created by DIFFERENT leaders with different metadata
	//   2. CommitBlockState(WithRebuildTries()) rebuilds tries from foreign state
	//   3. The HASH-MISMATCH-GUARD adopted "P2P-authoritative" blocks, but P2P
	//      blocks are NOT authoritative for Master nodes — consensus is
	//
	// processSingleEpochData only runs on Master nodes, which derive ALL state
	// from consensus execution. P2P sync writes to LevelDB are for Sub/SyncOnly
	// nodes and must never override Master consensus state.
	//
	// DIAGNOSTIC: Log if LAZY REFRESH would have triggered, for analysis.
	// ═══════════════════════════════════════════════════════════════════════════
	if time.Since(bp.lastLazyRefreshTime) > 100*time.Millisecond {
		bp.lastLazyRefreshTime = time.Now()
		storageLastBlockNum := storage.GetLastBlockNumber()
		bpLastBlock := bp.GetLastBlock()
		bpLastBlockNum := uint64(0)
		if bpLastBlock != nil {
			bpLastBlockNum = bpLastBlock.Header().BlockNumber()
		}
		if storageLastBlockNum > bpLastBlockNum {
			logger.Debug("🔍 [LAZY-REFRESH-DISABLED] Storage block #%d > bp.lastBlock #%d, but Master will NOT import P2P blocks (GEI=%d). Guard disabled to prevent fork.",
				storageLastBlockNum, bpLastBlockNum, globalExecIndex)
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
	epochNum = epochData.GetEpoch()

	// 🔍 DIAGNOSTIC: Detect epoch transition by comparing with last block and chain state
	lastBlock = bp.GetLastBlock()
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
	// ═══════════════════════════════════════════════════════════════════════════
	// CRITICAL FIX: Since Go P2P sync is disabled and ALL blocks are delivered
	// strictly sequentially via Rust FFI (ExecuteBlock), ANY gap in globalExecIndex 
	// means Rust intentionally fast-skipped empty commits during catch-up.
	// We MUST NOT buffer it. We just adopt the new GEI and process it immediately.
	// ═══════════════════════════════════════════════════════════════════════════
	if globalExecIndex > *nextExpectedGlobalExecIndex {
		gapSize := globalExecIndex - *nextExpectedGlobalExecIndex
		
		oldExpected := *nextExpectedGlobalExecIndex
		*nextExpectedGlobalExecIndex = globalExecIndex


		logger.Info("🔗 [RUST-FAST-SKIP] GEI jumped from %d to %d (gap=%d). Adopting new GEI due to empty-commit fast-skip in Rust.",
			oldExpected, globalExecIndex, gapSize)
			
		// Fall through to process the block sequentially
	}

	// Case 3: Sequential block (globalExecIndex == *nextExpectedGlobalExecIndex)
	// Proceed to PROCESS_BLOCK
	logger.Debug("✅ [FORK-SAFETY] Processing sequential block global_exec_index=%d", globalExecIndex)

PROCESS_BLOCK:
	// ═══════════════════════════════════════════════════════════════════════════
	// NOMT RE-EXECUTION GUARD: Prevent EVM execution of already-committed blocks
	// If the DB already contains this block number (or higher), we must NEVER
	// pass it to the EVM. Doing so with NOMT corrupts the trie state because
	// NOMT only stores the latest state, not historic state roots.
	// ═══════════════════════════════════════════════════════════════════════════
	// The NEXT block to be created will be *currentBlockNumber + 1 (assigned at line ~585).
	// Compare that against the DB — if the DB already has it, this is a re-execution.
	nextBlockToCreate := *currentBlockNumber + 1
	if nextBlockToCreate <= storage.GetLastBlockNumber() && storage.GetLastBlockNumber() > 0 {
		if len(epochData.Transactions) > 0 {
			logger.Warn("🛡️ [NOMT-GUARD] Skipping EVM execution for block #%d (already in DB: #%d). "+
				"Re-executing historic blocks corrupts NOMT state.", nextBlockToCreate, storage.GetLastBlockNumber())
			return
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// SYNC DEDUP GUARD: DISABLED (FORK-SAFETY FIX 2026-04-29)
	//
	// This guard imported P2P-synced blocks from LevelDB instead of creating
	// them from consensus execution. This caused hash mismatches because:
	//   1. P2P blocks are created by DIFFERENT leaders with different metadata
	//   2. CommitBlockState(WithRebuildTries()) corrupts the local trie state
	//   3. Subsequent blocks inherit divergent state → permanent fork
	//
	// processSingleEpochData only runs on Master nodes (via processRustEpochData),
	// which MUST create blocks from consensus — never import from P2P sync.
	// P2P sync is for Sub/SyncOnly nodes that don't participate in consensus.
	//
	// DIAGNOSTIC: Log if we WOULD have triggered the old guard, for analysis.
	// ═══════════════════════════════════════════════════════════════════════════
	{
		incomingBlockNumber := epochData.GetBlockNumber()
		syncedLastBlock := storage.GetLastBlockNumber()
		if incomingBlockNumber > 0 && syncedLastBlock >= incomingBlockNumber {
			syncedHash, hashOk := blockchain.GetBlockChainInstance().GetBlockHashByNumber(incomingBlockNumber)
			if hashOk {
				logger.Info("🔍 [SYNC-DEDUP-DISABLED] Block #%d exists in storage (hash=%s) but Master will CREATE from consensus (GEI=%d). Guard disabled to prevent fork.",
					incomingBlockNumber, syncedHash.Hex()[:18]+"...", globalExecIndex)
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
	if lastBlock != nil && epochNum > lastBlock.Header().Epoch() {
		logger.Info("🔄 [EPOCH-BOUNDARY] Processing empty commit at GEI=%d as boundary block for epoch %d→%d",
			globalExecIndex, lastBlock.Header().Epoch(), epochNum)
	}

	if epochData.GetBlockNumber() == 0 {
		logger.Debug("⏭️  [SKIP-EMPTY] Skipping empty commit: global_exec_index=%d (no state change)", globalExecIndex)

		// Update GlobalExecIndex tracking (persistent)
		bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex)

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
			blockchain.WithCommitMappings(), // CRITICAL FIX: Ensure mapping batches from memory are flushed to DB
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
		allTransactions = append(allTransactions, transactions...)
		totalTxsFromRust += len(transactions)
	}

	// If block number is 0, it means Rust explicitly marked this commit to be skipped
	if epochData.GetBlockNumber() == 0 {
		logger.Info("⏭️  [SKIP-EMPTY] SILENT DROP: block number is 0 after unmarshal (marked as skip by Rust): global_exec_index=%d. totalTxsFromRust=%d", globalExecIndex, totalTxsFromRust)
		bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex)

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

	// RUST IS AUTHORITATIVE: Use the block_number exactly as provided by Rust.
	// We no longer assign sequential block numbers internally in Go.
	if epochData.GetBlockNumber() > 0 {
		*currentBlockNumber = epochData.GetBlockNumber()
	} else {
		// Fallback for genesis / phase-b if needed
		*currentBlockNumber++
	}
	
	logger.Debug("📊 [BLOCK-NUM] Using Rust's authoritative block #%d for global_exec_index=%d (txs=%d)",
		*currentBlockNumber, globalExecIndex, len(allTransactions))


	// ═══════════════════════════════════════════════════════════════════════════
	// GEI REGRESSION GUARD: Prevent creating blocks from stale DAG replay.
	//
	// In Phase-B architecture, Rust reads GEI from Go's authoritative state
	// and starts CommitProcessor from lastGEI+1. Therefore, legitimate
	// recovery commits should ALWAYS have GEI > lastBlockGEI. Any commit
	// with GEI <= lastBlockGEI is stale and must be skipped to prevent
	// duplicate block creation (which causes hash divergence and forks).
	// ═══════════════════════════════════════════════════════════════════════════
	{
		lastBlockGEI := uint64(0)
		if lastBlock != nil {
			lastBlockGEI = lastBlock.Header().GlobalExecIndex()
		}

		if lastBlockGEI > 0 && globalExecIndex <= lastBlockGEI {
			logger.Info("🛡️ [GEI-REGRESSION] Skipping stale commit: incoming GEI=%d ≤ last block GEI=%d (block #%d). "+
				"This commit was already executed — not creating duplicate block.",
				globalExecIndex, lastBlockGEI, *currentBlockNumber)

			// Still update GEI counter so the processor advances past this commit
			bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex)
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
	// ═══════════════════════════════════════════════════════════════════════════
	// ANTI-INFLATION GUARD: DISABLED (Fork-Safety Fix 3)
	//
	// This guard adopted P2P-synced blocks and called CommitBlockState(WithRebuildTries()),
	// which replaces trie state from a P2P block header. This is non-deterministic because:
	//   1. Different nodes may have different P2P-synced blocks at any given moment
	//   2. WithRebuildTries() modifies trie state that the execution pipeline depends on
	// Master nodes don't use P2P sync, so this guard should never fire in normal operation.
	// If it does fire, it means something is fundamentally broken and should NOT be masked.
	// ═══════════════════════════════════════════════════════════════════════════

	// FORK-SAFETY: Deduplication and sorting are now handled consistently by Rust in block_sending.rs

	// ═══════════════════════════════════════════════════════════════════════════
	// FORK-SAFETY FIX 2: Invalidate all in-memory state caches before executing.
	//
	// The async commit pipeline (CommitPipeline → PersistAsync) for the PREVIOUS
	// block may leave stale entries in loadedAccounts, SmartContractDB caches, etc.
	// Even with synchronous DoneChan (Fix 1), CommitBlockState does NOT clear the
	// in-memory caches — it only persists to DB. If a cached account state object
	// from Block N-1 is still in loadedAccounts, Block N's ProcessTransactions will
	// read the cached (pre-commit) version instead of the freshly-committed version.
	// This invalidation forces all reads to go through the committed trie.
	// ═══════════════════════════════════════════════════════════════════════════
	bp.chainState.InvalidateAllState()

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

	newBlock := bp.createBlockFromResults(accumulatedResults, *currentBlockNumber, epochNum, true, batchID, commitTimestampMs, globalExecIndex, commitIndex, leaderAddr)
	createBlockDuration := time.Since(createBlockStart)

	blockHash := newBlock.Header().Hash().Hex()
	logger.Debug("⏱️  [PERF] createBlockFromResults: %d txs in %v for block #%d (hash=%s, gei=%d)",
		len(newBlock.Transactions()), createBlockDuration, *currentBlockNumber, blockHash[:16]+"...", globalExecIndex)

	// ═══════════════════════════════════════════════════════════════════════════
	// PHASE 1 DIAGNOSTIC: Log hash-input fields for fork forensics.
	// Hash INCLUDES: BlockNumber, AccountStatesRoot, StakeStatesRoot, ReceiptRoot,
	//                LeaderAddress, TimeStamp, TransactionsRoot, Epoch, GlobalExecIndex
	// Hash EXCLUDES: LastBlockHash, AggregateSignature
	// ═══════════════════════════════════════════════════════════════════════════
	logger.Info("🔍 [FORK-DIAG] Block #%d hash=%s | leader=%s | ts=%d | epoch=%d | stateRoot=%s | stakeRoot=%s | rcptRoot=%s | txRoot=%s | GEI=%d",
		newBlock.Header().BlockNumber(),
		newBlock.Header().Hash().Hex(),
		newBlock.Header().LeaderAddress().Hex(),
		newBlock.Header().TimeStamp(),
		newBlock.Header().Epoch(),
		newBlock.Header().AccountStatesRoot().Hex(),
		newBlock.Header().StakeStatesRoot().Hex(),
		newBlock.Header().ReceiptRoot().Hex(),
		newBlock.Header().TransactionsRoot().Hex(),
		newBlock.Header().GlobalExecIndex(),
	)

	// ═══════════════════════════════════════════════════════════════════════════
	// PHASE 2: POST-CREATE FORK GUARD
	//
	// ROOT CAUSE: When a node cold-starts after restore, Rust's DAG replay may
	// produce commits with different metadata (leader, timestamp, GEI) than
	// the network's canonical commits. The locally-created block will have the
	// same stateRoot (same TXs executed) but a DIFFERENT hash.
	//
	// FIX: After creating a block, check if P2P sync has already written a
	// block at this number with a DIFFERENT hash. If so, adopt the P2P version
	// (which is network-authoritative) and discard our locally-created block.
	//
	// FORK-SAFETY: All nodes that are in-sync produce the same blocks, so they
	// never trigger this guard. Only nodes catching up after restart trigger it.
	// ═══════════════════════════════════════════════════════════════════════════
	// ═══════════════════════════════════════════════════════════════════════════
	// POST-CREATE FORK GUARD: DISABLED (Fork-Safety Fix 3)
	//
	// This guard compared the locally-created block hash with a P2P-synced block
	// and adopted the P2P version if different, calling CommitBlockState(WithRebuildTries()).
	// This is a PRIMARY CAUSE of fork because:
	//   1. It replaces trie state with state from a P2P-synced block header
	//   2. Different nodes may or may not trigger this guard depending on P2P timing
	//   3. WithRebuildTries() during active block processing corrupts state determinism
	// Master nodes should NEVER have P2P-synced blocks at the same height they're creating.
	// If they do, the root cause is elsewhere and masking it makes debugging harder.
	//
	// DIAGNOSTIC: If this block would have triggered the old guard, log it for analysis.
	// ═══════════════════════════════════════════════════════════════════════════
	{
		existingHash, existsInMap := blockchain.GetBlockChainInstance().GetBlockHashByNumber(*currentBlockNumber)
		if existsInMap && existingHash != (common.Hash{}) {
			localHash := newBlock.Header().Hash()
			if localHash != existingHash {
				logger.Warn("🔍 [FORK-DIAG-ONLY] Block #%d: local hash=%s ≠ existing hash=%s. "+
					"Guard DISABLED — NOT adopting existing block. "+
					"Local: leader=%s ts=%d GEI=%d",
					*currentBlockNumber,
					localHash.Hex()[:18]+"...", existingHash.Hex()[:18]+"...",
					newBlock.Header().LeaderAddress().Hex(),
					newBlock.Header().TimeStamp(),
					newBlock.Header().GlobalExecIndex(),
				)
			}
		}
	}

	// Update GlobalExecIndex tracking (persistent)
	bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex)

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
