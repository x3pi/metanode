// @title processor/block_processor_sync.go
// @markdown processor/block_processor_sync.go - GEI ordering, fork-safety, and block sync processing
package processor

// Go build cache invalidation comment: Force relink of Rust FFI library
import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
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

	// ═══════════════════════════════════════════════════════════════════════════
	// LAYER-4: Idempotent Execution Guard
	// Check if this commit was already processed BEFORE any state mutation.
	// This prevents GEI drift when Rust retries the same commit after
	// timeout/crash. Must check before epoch transition logic to avoid
	// double-resetting commitIndex.
	// ═══════════════════════════════════════════════════════════════════════════
	geiAuthLayer4 := GetGEIAuthority()
	if geiAuthLayer4.ShouldSkipCommit(commitIndex, epochNum) {
		logger.Info("🛡️ [LAYER-4] Idempotent guard triggered: returning early for commit=%d epoch=%d GEI=%d",
			commitIndex, epochNum, globalExecIndex)
		return
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// LAYER-1: Protobuf Strict Boundary — Go-side validation
	// Reject blocks with invalid LeaderAddress. Valid values:
	//   - Exactly 20 bytes (valid Ethereum address from Rust)
	//   - Empty (0 bytes) — allowed for backward compatibility, uses index fallback
	// Any other length indicates corrupted FFI data → REJECT to prevent fork.
	// ═══════════════════════════════════════════════════════════════════════════
	if leaderAddrLen := len(epochData.LeaderAddress); leaderAddrLen != 20 && leaderAddrLen != 0 {
		logger.Error("🛡️ [LAYER-1] REJECT: LeaderAddress must be 20 bytes or empty, got %d bytes (commit=%d, GEI=%d, epoch=%d). Dropping block to prevent fork.",
			leaderAddrLen, commitIndex, globalExecIndex, epochNum)
		return
	}

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
	//
	// EPOCH-INFLATION GUARD (May 2026):
	// Large epoch jumps (>2 at once) indicate replayed/corrupt data, not a
	// real transition. Real transitions increment by exactly 1 epoch.
	// Block 918 fork: m1 replayed stale commits → phantom epoch transitions
	// → epoch 22 vs cluster epoch 19 → GEI inflation → permanent fork.
	// ═════════════════════════════════════════════════════════════════════
	if epochNum > currentEpoch && lastBlock != nil {
		epochJump := epochNum - currentEpoch
		if epochJump > 2 {
			logger.Error("🚨 [EPOCH-INFLATION-GUARD] Suspicious epoch jump %d→%d (delta=%d, GEI=%d). "+
				"This indicates replayed/corrupt commit data. BLOCKING epoch reset to prevent inflation.",
				currentEpoch, epochNum, epochJump, globalExecIndex)
			// DO NOT reset commitIndex — this would cause GEI inflation on forked nodes.
			// The node should continue with its current epoch until a legitimate
			// EndOfEpoch system transaction arrives via live consensus.
		} else {
			logger.Info("🔄 [GEI-AUTHORITY] Epoch %d→%d detected (jump=%d). Resetting lastHandledCommitIndex and persisting new epoch.", currentEpoch, epochNum, epochJump)
			geiAuth := GetGEIAuthority()
			geiAuth.ResetCommitIndexForEpoch(epochNum)
			storage.ForceSetLastHandledCommitIndex(0)
			storage.UpdateLastHandledCommitEpoch(epochNum)
			if bp.storageManager != nil && bp.storageManager.GetStorageBackupDb() != nil {
				bp.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitIndexHashKey.Bytes(), utils.Uint32ToBytes(0))
				bp.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitEpochHashKey.Bytes(), utils.Uint64ToBytes(epochNum))
			}
		}
	}

	// Record the commit index to persist progress
	geiAuth := GetGEIAuthority()
	geiAuth.RecordCommitIndex(commitIndex)

	// ═══════════════════════════════════════════════════════════════════════════
	// POST-MUTATION DIAGNOSTIC (May 2026):
	// After all state mutations above (epoch reset, commit index recording),
	// emit a one-time diagnostic showing the exact values that the
	// GEI-REGRESSION guard (line ~652) will use to decide whether to skip
	// this commit. This is critical for E2E test traceability
	// (test_snapshot_stability_loop.sh) — if the guard incorrectly skips
	// a commit, this log will show exactly why.
	//
	// Only log on significant GEI values (first commit, epoch boundary,
	// or every 100 commits) to avoid spam.
	// ═══════════════════════════════════════════════════════════════════════════
	if commitIndex <= 1 || epochNum != currentEpoch || commitIndex%100 == 0 {
		lastBlockGEIDiag := uint64(0)
		if lastBlock != nil {
			lastBlockGEIDiag = lastBlock.Header().GlobalExecIndex()
		}
		logger.Info("🔍 [SYNC-PARITY-DIAG] epoch=%d commitIndex=%d incomingGEI=%d lastBlockGEI=%d lastPersistedGEI=%d nextExpectedGEI=%d — "+
			"GEI-REGRESSION guard will %s this commit",
			epochNum, commitIndex, globalExecIndex, lastBlockGEIDiag, lastPersistedGEI, *nextExpectedGlobalExecIndex,
			func() string {
				if lastBlockGEIDiag > 0 && globalExecIndex <= lastBlockGEIDiag {
					return "SKIP"
				}
				return "PROCESS"
			}())
	}

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
	if totalTxsQuick == 0 && len(epochData.GetSystemTransactions()) == 0 {
		// ORDERING: Must still validate sequential order
		if globalExecIndex < *nextExpectedGlobalExecIndex {
			// Old/duplicate — skip silently
			return
		}
		if globalExecIndex > *nextExpectedGlobalExecIndex {
			gapSize := globalExecIndex - *nextExpectedGlobalExecIndex
			oldExpected := *nextExpectedGlobalExecIndex
			
			// Adopt the new GEI from Rust (100% deterministic)
			*nextExpectedGlobalExecIndex = globalExecIndex
			
			// Ensure block number is synced for System Txs if we just started/restored
			if *currentBlockNumber == 0 {
				*currentBlockNumber = storage.GetLastBlockNumber()
			}
			
			logger.Info("📡 [TELEMETRY] [RUST-FAST-SKIP] GEI jumped from %d to %d (gap=%d) in Fast-Path. Adopting new GEI.",
				oldExpected, globalExecIndex, gapSize)
		}

		// Sequential empty commit — update GEI and advance
		bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)
		*nextExpectedGlobalExecIndex = globalExecIndex + 1

		// ═══════════════════════════════════════════════════════════════
		// FAST-PATH SYSTEM-TX PERSISTENCE: Save SystemTransactions even
		// when there are 0 user transactions. Without this, EndOfEpoch
		// system txs attached to empty commits are silently dropped,
		// making them invisible to eth_getSystemTransactionsByBlockNumber.
		// ═══════════════════════════════════════════════════════════════
		fastPathSysTxs := epochData.GetSystemTransactions()
		if len(fastPathSysTxs) > 0 {
			// Use the current block number from the last persisted block
			sysTxBlockNum := *currentBlockNumber
			if lastBlock := bp.GetLastBlock(); lastBlock != nil {
				sysTxBlockNum = lastBlock.Header().BlockNumber()
			}
			err := bp.chainState.GetBlockDatabase().SaveSystemTransactions(sysTxBlockNum, fastPathSysTxs)
			if err != nil {
				logger.Error("❌ [FAST-PATH-SYSTEM-TX] Failed to save %d SystemTransactions at block #%d: %v", len(fastPathSysTxs), sysTxBlockNum, err)
			} else {
				logger.Info("📡 [TELEMETRY] [FAST-PATH-SYSTEM-TX] Saved %d SystemTransactions at block #%d (GEI=%d)", len(fastPathSysTxs), sysTxBlockNum, globalExecIndex)
			}
		}

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

		// ═══════════════════════════════════════════════════════════════
		// GHOST-BLOCK-GUARD: If Rust explicitly assigned a block number
		// for this 0-tx commit, we MUST create an empty block to prevent
		// sequence gaps in the Go chain.
		// ═══════════════════════════════════════════════════════════════
		if epochData.GetBlockNumber() > 0 {
			logger.Info("🛡️ [GHOST-BLOCK-GUARD] Rust assigned block_number=%d for 0-tx commit (GEI=%d). Creating empty block to prevent sequence gap.",
				epochData.GetBlockNumber(), globalExecIndex)
			*nextExpectedGlobalExecIndex = globalExecIndex
			goto EPOCH_BOUNDARY_FALLTHROUGH
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
		// ═══════════════════════════════════════════════════════════════════
		// CRITICAL FORK-SAFETY FIX (May 2026):
		// NEVER use time.Now() for consensus blocks! time.Now() is
		// non-deterministic — different nodes call it at different moments,
		// producing different timestamps → different block hashes → FORK.
		//
		// ROOT CAUSE: Block 918 fork in hash_mismatch_alert.log was caused
		// by m1 getting timestamp 0x69fa9331 while cluster got 0x69fa932f
		// (2-second difference from time.Now() divergence during replay).
		//
		// FIX: Derive a deterministic timestamp from lastBlock.timestamp + 1s.
		// All nodes have the same lastBlock after STARTUP-SYNC, so they all
		// produce the same derived timestamp.
		// ═══════════════════════════════════════════════════════════════════
		lastBlockForTs := bp.GetLastBlock()
		if lastBlockForTs != nil && lastBlockForTs.Header().TimeStamp() > 0 {
			commitTimestampMs = lastBlockForTs.Header().TimeStamp()*1000 + 1000
			logger.Warn("🛡️ [FORK-SAFETY] No consensus timestamp — using lastBlock.timestamp+1s = %d ms (GEI=%d, lastBlock=#%d)",
				commitTimestampMs, globalExecIndex, lastBlockForTs.Header().BlockNumber())
		} else {
			// Genesis fallback: use 1 second (epoch 0, block 0)
			commitTimestampMs = 1000
			logger.Warn("🛡️ [FORK-SAFETY] No consensus timestamp AND no lastBlock — using genesis fallback 1000ms (GEI=%d)",
				globalExecIndex)
		}
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

	// CRITICAL: Auto-advance Go epoch when incoming blocks have higher epoch.
	// For SyncOnly nodes, blocks arrive via Rust P2P sync, and the epoch field
	// in each block indicates which epoch it belongs to. Without this check,
	// Go epoch stays at 0 forever because no EndOfEpoch system transaction
	// flows through the normal commit path.
	//
	// EPOCH-INFLATION GUARD (May 2026): Only auto-advance epoch for LIVE blocks
	// (GEI > persisted GEI). Replayed blocks whose GEI is already persisted
	// should NOT trigger epoch advancement — their epoch metadata may be
	// from a stale fork. This prevents the cascading epoch inflation seen
	// at block 918 where m1 advanced from epoch 2 to epoch 22.
	if epochNum > 0 && globalExecIndex > lastPersistedGEI {
		bp.chainState.CheckAndUpdateEpochFromBlock(epochNum, commitTimestampMs)
	} else if epochNum > 0 && globalExecIndex <= lastPersistedGEI {
		logger.Debug("🛡️ [EPOCH-INFLATION-GUARD] Skipping CheckAndUpdateEpochFromBlock for replayed block (GEI=%d ≤ persisted=%d, epoch=%d)",
			globalExecIndex, lastPersistedGEI, epochNum)
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
			if lastBlock != nil && lastBlock.Header().GlobalExecIndex() == globalExecIndex && len(lastBlock.Transactions()) == 0 {
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

		if len(epochData.Transactions) > 0 {
			logger.Error("🚨 [CRITICAL-DIVERGENCE] GEI jumped from %d to %d (gap=%d). We missed blocks with transactions! This will cause a state root mismatch. Immediate debug required. Block epoch=%d", 
				oldExpected, globalExecIndex, gapSize, epochNum)
		} else {
			logger.Info("📡 [TELEMETRY] [RUST-FAST-SKIP] GEI jumped from %d to %d (gap=%d). Adopting new GEI due to empty-commit fast-skip in Rust.",
				oldExpected, globalExecIndex, gapSize)
		}
			
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
	isEpochBoundary := false
	if lastBlock != nil && epochNum > lastBlock.Header().Epoch() {
		isEpochBoundary = true
		logger.Info("🔄 [EPOCH-BOUNDARY] Processing empty commit at GEI=%d as boundary block for epoch %d→%d",
			globalExecIndex, lastBlock.Header().Epoch(), epochNum)
	}

	if len(epochData.Transactions) == 0 && len(epochData.GetSystemTransactions()) == 0 && !isEpochBoundary {
		if epochData.GetBlockNumber() > 0 {
			logger.Info("🛡️ [GHOST-BLOCK-GUARD] Empty payload (deduplicated), but Rust assigned block_number=%d. Creating empty block to prevent gap. GEI=%d", epochData.GetBlockNumber(), globalExecIndex)
		} else {
			logger.Debug("⏭️  [SKIP-EMPTY] Skipping empty commit: global_exec_index=%d (no state change)", globalExecIndex)

			// Update GlobalExecIndex tracking (persistent)
			bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)

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
			}
			return
		}
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

		// EXPLICIT FILTER: Skip 64-byte zero payloads (SystemTransaction artifact)
		// These artifacts appear at epoch boundaries and are not valid user transactions.
		if len(ms.Digest) == 64 {
			isZero := true
			for _, b := range ms.Digest {
				if b != 0 {
					isZero = false
					break
				}
			}
			if isZero {
				logger.Info("📡 [TELEMETRY] SystemTransaction artifact detected and filtered. GEI=%d, Epoch=%d, TxIdx=%d", globalExecIndex, epochNum, txIdx)
				continue
			}
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
			logger.Error("❌ [TX FLOW] Failed to unmarshal transaction[%d] in Rust block: %v (size=%d bytes). Hex: %x", txIdx, err, len(ms.Digest), ms.Digest)
			continue
		}
		allTransactions = append(allTransactions, transactions...)
		totalTxsFromRust += len(transactions)
	}

	// If no transactions after unmarshal, skip (same as empty commit)
	if len(allTransactions) == 0 && len(epochData.GetSystemTransactions()) == 0 && !isEpochBoundary {
		bNum := epochData.GetBlockNumber()
		if bNum == 0 {
			// FALLBACK: Auto-increment local block number if Rust doesn't provide one (e.g., during SyncOnly)
			lastCommittedBlockNumber := storage.GetLastAssignedBlockNumber()
			if lastCommittedBlockNumber == 0 {
				lastCommittedBlockNumber = storage.GetLastBlockNumber()
			}
			bNum = lastCommittedBlockNumber + 1
		}

		logger.Info("🛡️ [GHOST-BLOCK-GUARD] len(allTransactions) is 0 after unmarshal, creating empty block to prevent gap. Rust_block_number=%d, assigned_block_number=%d, GEI=%d", 
			epochData.GetBlockNumber(), bNum, globalExecIndex)
		
		emptyResult := tx_processor.ProcessResult{Transactions: nil, Receipts: nil}
		lastB := bp.GetLastBlock()
		if lastB != nil {
			emptyResult.Root = lastB.Header().AccountStatesRoot()
			emptyResult.StakeStatesRoot = lastB.Header().StakeStatesRoot()
		}
		leaderBytes := epochData.GetLeaderAddress()
		var leader common.Address
		if len(leaderBytes) == 20 {
			leader = common.BytesToAddress(leaderBytes)
		}
		batchID := fmt.Sprintf("SYNC-%d-%d", globalExecIndex, time.Now().UnixNano())
		
		*currentBlockNumber = bNum
		storage.UpdateLastAssignedBlockNumber(*currentBlockNumber)

		emptyBlock := bp.createBlockFromResults(emptyResult, *currentBlockNumber, epochNum, true, batchID, epochData.GetCommitTimestampMs(), globalExecIndex, commitIndex, leader)
		if emptyBlock != nil {
			// CRITICAL FIX: Must set last block so the NEXT block's ParentHash is correct
			bp.SetLastBlock(emptyBlock)
			bp.AddPendingCommitBlock(emptyBlock)

			// CRITICAL FIX: Must also dispatch to commitWorker to save to DB!
			job := CommitJob{
				Block:           emptyBlock,
				ProcessResults:  &emptyResult,
				MappingWg:       &sync.WaitGroup{},
				GlobalExecIndex: globalExecIndex,
				CommitIndex:     commitIndex,
			}
			select {
			case bp.commitChannel <- job:
			default:
				logger.Warn("WARNING: commitChannel full! Block commit goroutine will block.")
				bp.commitChannel <- job
			}

			select {
			case bp.createdBlocksChan <- emptyBlock:
			default:
				logger.Warn("WARNING: createdBlocksChan full! Block creation goroutine will block.")
				bp.createdBlocksChan <- emptyBlock
			}
		}
		bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)
		if globalExecIndex > 0 {
			*nextExpectedGlobalExecIndex = globalExecIndex + 1
			if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
				logger.Info("✅ [FORK-SAFETY] Found next pending block in buffer: global_exec_index=%d", *nextExpectedGlobalExecIndex)
				delete(pendingBlocks, *nextExpectedGlobalExecIndex)
				epochData = pendingBlock
				goto PROCESS_SINGLE_EPOCH_DATA_START
			}

			if skippedBlock, exists := skippedCommitsWithTxs[*nextExpectedGlobalExecIndex]; exists {
				logger.Info("✅ [LAG-HANDLING] Found skipped commit: global_exec_index=%d", *nextExpectedGlobalExecIndex)
				delete(skippedCommitsWithTxs, *nextExpectedGlobalExecIndex)
				epochData = skippedBlock
				goto PROCESS_SINGLE_EPOCH_DATA_START
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
		storage.UpdateLastAssignedBlockNumber(*currentBlockNumber)
	} else {
		// CRITICAL FORK-SAFETY FIX: Rust sets block_number = 0 for empty commits.
		// We MUST skip creating a Go block for them to prevent block inflation.
		logger.Info("⏭️  [BLOCK-NUM] Skipping empty commit from Rust (GEI=%d, commitIndex=%d) - PREVENTING INFLATION", globalExecIndex, commitIndex)
		
		// Still update GEI counter so the processor advances past this commit
		bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)
		*nextExpectedGlobalExecIndex = globalExecIndex + 1

		// Check pending blocks
		if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
			delete(pendingBlocks, *nextExpectedGlobalExecIndex)
			epochData = pendingBlock
			goto PROCESS_SINGLE_EPOCH_DATA_START
		}
		return
	}
	
	logger.Debug("📊 [BLOCK-NUM] Using Rust's authoritative block #%d for global_exec_index=%d (txs=%d)",
		*currentBlockNumber, globalExecIndex, len(allTransactions))

	// ═══════════════════════════════════════════════════════════════════════════
	// FORENSIC COMMIT FINGERPRINT (Phase 3 — Fork Diagnosis)
	//
	// Log the complete identity of every commit dispatched from Rust.
	// This enables immediate fork diagnosis by comparing logs across nodes
	// without needing to correlate Rust consensus logs.
	// ═══════════════════════════════════════════════════════════════════════════
	{
		leaderBytes := epochData.GetLeaderAddress()
		commitDigest := epochData.GetCommitDigest()
		leaderHex := fmt.Sprintf("0x%x", leaderBytes)
		if len(leaderBytes) == 20 {
			leaderHex = fmt.Sprintf("0x%X", leaderBytes)
		}
		digestHex := "nil"
		if len(commitDigest) > 0 {
			if len(commitDigest) >= 4 {
				digestHex = fmt.Sprintf("0x%x", commitDigest[:4])
			} else {
				digestHex = fmt.Sprintf("0x%x", commitDigest)
			}
		}
		logger.Info("🔍 [COMMIT-FINGERPRINT] block=#%d GEI=%d epoch=%d commitIdx=%d leader=%s digest=%s txs=%d",
			*currentBlockNumber, globalExecIndex, epochNum, commitIndex,
			leaderHex, digestHex, len(allTransactions))
	}


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

			bNum := epochData.GetBlockNumber()
			if bNum == 0 {
				lastCommittedBlockNumber := storage.GetLastAssignedBlockNumber()
				if lastCommittedBlockNumber == 0 {
					lastCommittedBlockNumber = storage.GetLastBlockNumber()
				}
				bNum = lastCommittedBlockNumber + 1
			}

			logger.Info("🛡️ [GHOST-BLOCK-GUARD] Creating empty block for GEI-REGRESSION to prevent gap. Rust_block_number=%d, assigned_block_number=%d, GEI=%d", epochData.GetBlockNumber(), bNum, globalExecIndex)
			emptyResult := tx_processor.ProcessResult{Transactions: nil, Receipts: nil}
			lastB := bp.GetLastBlock()
			if lastB != nil {
				emptyResult.Root = lastB.Header().AccountStatesRoot()
				emptyResult.StakeStatesRoot = lastB.Header().StakeStatesRoot()
			}
			leaderBytes := epochData.GetLeaderAddress()
			var leader common.Address
			if len(leaderBytes) == 20 {
				leader = common.BytesToAddress(leaderBytes)
			}
			batchID := fmt.Sprintf("SYNC-%d-%d", globalExecIndex, time.Now().UnixNano())
			*currentBlockNumber = bNum
			storage.UpdateLastAssignedBlockNumber(*currentBlockNumber)

			emptyBlock := bp.createBlockFromResults(emptyResult, *currentBlockNumber, epochNum, true, batchID, epochData.GetCommitTimestampMs(), globalExecIndex, commitIndex, leader)
			if emptyBlock != nil {
				// CRITICAL FIX: Must set last block so the NEXT block's ParentHash is correct
				bp.SetLastBlock(emptyBlock)
				bp.AddPendingCommitBlock(emptyBlock)

				// CRITICAL FIX: Must also dispatch to commitWorker to save to DB!
				job := CommitJob{
					Block:           emptyBlock,
					ProcessResults:  &emptyResult,
					MappingWg:       &sync.WaitGroup{},
					GlobalExecIndex: globalExecIndex,
					CommitIndex:     commitIndex,
				}
				select {
				case bp.commitChannel <- job:
				default:
					logger.Warn("WARNING: commitChannel full! Block commit goroutine will block.")
					bp.commitChannel <- job
				}

				select {
				case bp.createdBlocksChan <- emptyBlock:
				default:
					logger.Warn("WARNING: createdBlocksChan full! Block creation goroutine will block.")
					bp.createdBlocksChan <- emptyBlock
				}
			}

			// Still update GEI counter so the processor advances past this commit
			bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)
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
	// FORK-SAFETY FIX 2 REVISED (May 2026): Cache invalidation REMOVED.
	//
	// ORIGINAL PURPOSE: When PersistAsync ran asynchronously (pre-May 2026),
	// Block N+1 could start reading from loadedAccounts/lruCache while Block N's
	// trie swap hadn't completed — causing stale reads and state divergence.
	//
	// WHY SAFE TO REMOVE: Since May 2026, PersistAsync runs INLINE in
	// commitToMemoryParallel, and DoneChan blocks until PersistAsync completes.
	// This guarantees:
	//   1. Trie swap is done before createBlockFromResults returns
	//   2. persistReady is closed before DoneChan signals
	//   3. IntermediateRoot(true) already clears dirtyAccounts + loadedAccounts
	//      on every block (account_state_db_commit.go lines 1022-1038)
	//   4. IntermediateRoot(true) waits on persistReady before any trie access
	//
	// PERFORMANCE IMPACT OF REMOVAL: InvalidateAllState() was destroying the
	// 200K-entry lruCache on EVERY block, forcing ~20,000 NOMT FFI reads per
	// block (~100-200ms). Over 1500+ blocks, the repeated map alloc/dealloc
	// caused progressive GC pressure → throughput degradation → stall at ~1672.
	//
	// C++ State::instances is cleared by mvm.CallClearAllStateInstances() in
	// the appropriate places (LAZY REFRESH, epoch transition).
	// ═══════════════════════════════════════════════════════════════════════════

	// ═══════════════════════════════════════════════════════════════════════════
	// FORK-PREVENTION SAFETY NET (May 2026): Verify NOMT handle root matches
	// the in-memory trie root BEFORE executing any transactions.
	//
	// This catches the case where STARTUP-SYNC's trie re-alignment failed
	// or was incomplete. Without this check, a stale trie silently produces
	// wrong AccountStatesRoot → different block hash → permanent fork.
	//
	// Cost: 1 NOMT FFI root query per block (negligible).
	// ═══════════════════════════════════════════════════════════════════════════
	if globalExecIndex > 0 && trie.GetStateBackend() == trie.BackendNOMT {
		nomtRoot, hasNomtRoot := trie.GetNomtHandleRoot("account_state")
		trieRoot := bp.chainState.GetAccountStateDB().Trie().Hash()
		if hasNomtRoot && nomtRoot != trieRoot {
			logger.Error("🚨 [FORK-PREVENTION] NOMT handle root DIFFERS from trie root BEFORE processing block #%d! "+
				"nomtRoot=%s, trieRoot=%s. Forcing trie re-alignment...",
				*currentBlockNumber, nomtRoot.Hex()[:18]+"...", trieRoot.Hex()[:18]+"...")
			lastBlock := bp.GetLastBlock()
			if lastBlock != nil {
				if err := bp.chainState.UpdateStateForNewHeader(lastBlock.Header()); err != nil {
					logger.Error("❌ [FORK-PREVENTION] Failed to re-align trie: %v", err)
				} else {
					bp.chainState.InvalidateAllState()
					logger.Info("✅ [FORK-PREVENTION] Trie re-aligned from NOMT handle. New trieRoot=%s",
						bp.chainState.GetAccountStateDB().Trie().Hash().Hex()[:18]+"...")
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// TIMESTAMP REGRESSION GUARD (May 2026): Reject stale DAG commits BEFORE
	// executing any transactions. This MUST run before ProcessTransactions()
	// because EVM execution mutates in-memory state (account balances, nonces,
	// storage). If we detect regression after execution, the state is already
	// corrupted and cannot be cleanly reverted.
	//
	// ROOT CAUSE: When Rust's local committer evaluates a sparse/incomplete DAG
	// (e.g., after snapshot restore or during network partition), it may decide
	// a different leader whose commit has an OLD timestamp. This produces a block
	// with timestamp BEHIND the parent → different block hash → FORK.
	//
	// Observed: Block 2718 fork — m2's timestamp was 122 seconds behind the
	// cluster (leader 0xb014 vs cluster's 0xCCc7, timestamp 0x6a04b115 vs
	// 0x6a04b18f). Different leader = different TX set = different txRoot.
	//
	// SAFETY: Legitimate blocks NEVER regress >30s because Rust's
	// calculate_commit_timestamp() uses median stake-weighted algorithm which
	// always produces monotonically increasing timestamps under normal conditions.
	// ═══════════════════════════════════════════════════════════════════════════
	blockTimeSec := commitTimestampMs / 1000 // Convert ms→s for deterministic EVM block.timestamp
	if lastBlock != nil && blockTimeSec > 0 {
		parentTs := lastBlock.Header().TimeStamp()
		if parentTs > 0 && blockTimeSec < parentTs {
			regression := parentTs - blockTimeSec
			if regression > 30 {
				leaderAddr := bp.GetLeaderAddress(epochData.GetLeaderAddress(), epochData.GetLeaderAuthorIndex())
				logger.Error("🚨 [TIMESTAMP-REGRESSION] DROPPING commit BEFORE execution: "+
					"block #%d timestamp=%d is %ds BEHIND parent #%d timestamp=%d. "+
					"Stale Rust commit from sparse DAG local committer "+
					"(leader=%s, GEI=%d, epoch=%d, txs=%d). "+
					"Correct block will arrive via CertifiedCommit.",
					*currentBlockNumber, blockTimeSec, regression,
					lastBlock.Header().BlockNumber(), parentTs,
					leaderAddr.Hex(), globalExecIndex, epochNum, len(allTransactions))

				if epochData.GetBlockNumber() > 0 {
					logger.Info("🛡️ [GHOST-BLOCK-GUARD] Creating empty block for TIMESTAMP-REGRESSION to prevent gap. block_number=%d, GEI=%d", epochData.GetBlockNumber(), globalExecIndex)
					emptyResult := tx_processor.ProcessResult{Transactions: nil, Receipts: nil}
					lastB := bp.GetLastBlock()
					if lastB != nil {
						emptyResult.Root = lastB.Header().AccountStatesRoot()
						emptyResult.StakeStatesRoot = lastB.Header().StakeStatesRoot()
					}
					leaderBytes := epochData.GetLeaderAddress()
					var leader common.Address
					if len(leaderBytes) == 20 {
						leader = common.BytesToAddress(leaderBytes)
					}
					batchID := fmt.Sprintf("SYNC-%d-%d", globalExecIndex, time.Now().UnixNano())
					*currentBlockNumber = epochData.GetBlockNumber()
					emptyBlock := bp.createBlockFromResults(emptyResult, *currentBlockNumber, epochNum, true, batchID, epochData.GetCommitTimestampMs(), globalExecIndex, commitIndex, leader)
					if emptyBlock != nil {
						select {
						case bp.createdBlocksChan <- emptyBlock:
						default:
							logger.Warn("WARNING: createdBlocksChan full! Block creation goroutine will block.")
							bp.createdBlocksChan <- emptyBlock
						}
					}
				}

				// Update GEI so processor advances past this commit
				bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)
				*nextExpectedGlobalExecIndex = globalExecIndex + 1

				// NOTE (May 2026): InvalidateAllState() REMOVED here.
				// No EVM mutations happened (commit dropped BEFORE execution),
				// so state caches are still valid. Invalidating would destroy
				// the 200k-entry LRU cache, causing ~20k NOMT FFI reads on
				// the next block and contributing to pipeline stalls.

				// Process any pending blocks
				if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
					delete(pendingBlocks, *nextExpectedGlobalExecIndex)
					epochData = pendingBlock
					goto PROCESS_SINGLE_EPOCH_DATA_START
				}
				return
			}
		}
	}

	processTxStart := time.Now()
	// bp.transactionProcessor.logBackendStartMs()
	accumulatedResults, err := bp.transactionProcessor.ProcessTransactions(allTransactions, blockTimeSec, preloadChan)
	processTxDuration := time.Since(processTxStart)
	if err != nil {
		logger.Error("❌ [TX FLOW] Failed to process transactions for block #%d: %v", *currentBlockNumber, err)
		return // Skip this commit, wait for next one
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// LATE SILENT DROP (May 2026): If all transactions were duplicates, we must
	// drop the block here to maintain deterministic parity with continuous nodes
	// that dropped it early via Rust tx_recycler.
	// ═══════════════════════════════════════════════════════════════════════════
	if len(accumulatedResults.Transactions) == 0 && len(epochData.GetSystemTransactions()) == 0 && !isEpochBoundary {
		if epochData.GetBlockNumber() > 0 {
			logger.Info("🛡️ [GHOST-BLOCK-GUARD] LATE DROP: 0 valid txs out of %d (all duplicates), but Rust assigned block_number=%d. Creating empty block to prevent gap. GEI=%d", 
				len(allTransactions), epochData.GetBlockNumber(), globalExecIndex)
			emptyResult := tx_processor.ProcessResult{Transactions: nil, Receipts: nil}
			lastB := bp.GetLastBlock()
			if lastB != nil {
				emptyResult.Root = lastB.Header().AccountStatesRoot()
				emptyResult.StakeStatesRoot = lastB.Header().StakeStatesRoot()
			}
			leaderBytes := epochData.GetLeaderAddress()
			var leader common.Address
			if len(leaderBytes) == 20 {
				leader = common.BytesToAddress(leaderBytes)
			}
			batchID := fmt.Sprintf("SYNC-%d-%d", globalExecIndex, time.Now().UnixNano())
			*currentBlockNumber = epochData.GetBlockNumber()
			emptyBlock := bp.createBlockFromResults(emptyResult, *currentBlockNumber, epochNum, true, batchID, epochData.GetCommitTimestampMs(), globalExecIndex, commitIndex, leader)
			if emptyBlock != nil {
				select {
				case bp.createdBlocksChan <- emptyBlock:
				default:
					logger.Warn("WARNING: createdBlocksChan full! Block creation goroutine will block.")
					bp.createdBlocksChan <- emptyBlock
				}
			}
			bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)
			if globalExecIndex > 0 {
				*nextExpectedGlobalExecIndex = globalExecIndex + 1
				if pendingBlock, exists := pendingBlocks[*nextExpectedGlobalExecIndex]; exists {
					logger.Info("✅ [FORK-SAFETY] Found next pending block in buffer: global_exec_index=%d", *nextExpectedGlobalExecIndex)
					delete(pendingBlocks, *nextExpectedGlobalExecIndex)
					epochData = pendingBlock
					goto PROCESS_SINGLE_EPOCH_DATA_START
				} else if skippedBlock, exists := skippedCommitsWithTxs[*nextExpectedGlobalExecIndex]; exists {
					logger.Info("✅ [LAG-HANDLING] Processing skipped commit: global_exec_index=%d", *nextExpectedGlobalExecIndex)
					delete(skippedCommitsWithTxs, *nextExpectedGlobalExecIndex)
					epochData = skippedBlock
					goto PROCESS_SINGLE_EPOCH_DATA_START
				}
			}
			return
		} else {
			logger.Info("⏭️  [SKIP-EMPTY] LATE SILENT DROP: all transactions were duplicates: global_exec_index=%d", globalExecIndex)
			
			// NOTE (May 2026): InvalidateAllState() REMOVED here.
			// All transactions were duplicates — no state mutation occurred.
			// IntermediateRoot(true) already cleared dirty accounts during
			// ProcessTransactions. Invalidating would thrash the LRU cache.
			
			bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)
			
			if globalExecIndex > 0 {
				*nextExpectedGlobalExecIndex = globalExecIndex + 1
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
			}
			return
		}
	}

	logger.Debug("[PERF] ProcessTransactions: %d txs in %v (%.0f tx/s) for block #%d",
		len(allTransactions), processTxDuration, float64(len(allTransactions))/processTxDuration.Seconds(), *currentBlockNumber)

	// ⚠️ VALIDATION: Check if any transaction is missing its receipt
	if len(accumulatedResults.Receipts) != len(allTransactions) {
		logger.Warn("⚠️ [RECEIPT VALIDATION] MISMATCH: block #%d has %d transactions but only %d receipts! (Likely due to duplicate/stale TXs being dropped)",
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
				logger.Warn("   ⚠️ [DROPPED TX] Transaction bị Node loại bỏ (do lỗi Nonce/trùng lặp): hash=%s, from=%s, to=%s, tx.Nonce=%d",
					txHash.Hex(), tx.FromAddress().Hex(), tx.ToAddress().Hex(), tx.GetNonce())
			}
		}
		logger.Warn("   📋 [DROPPED TX] Total dropped txs: %d. Hashes: %v",
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

	// ═══════════════════════════════════════════════════════════════════════════
	// REVERT GATE HANDLER: If createBlockFromResults returned nil, the draft
	// block was discarded by verifyDraftBlock (local corruption detected).
	// Do NOT advance GEI — Rust will retry this commit on the next cycle.
	// State has been reset to the parent block by revertDraftBlock.
	// ═══════════════════════════════════════════════════════════════════════════
	if newBlock == nil {
		logger.Error("🔄 [REVERT] Block #%d (GEI=%d) was REVERTED. "+
			"State reset to parent. Waiting for Rust retry or CertifiedCommit.",
			*currentBlockNumber, globalExecIndex)
		return
	}

	blockHash := newBlock.Header().Hash().Hex()
	logger.Debug("⏱️  [PERF] createBlockFromResults: %d txs in %v for block #%d (hash=%s, gei=%d)",
		len(newBlock.Transactions()), createBlockDuration, *currentBlockNumber, blockHash[:16]+"...", globalExecIndex)

	// LAYER-9: Persist leader address for DAG-wipe recovery
	bp.PersistLeaderAddress(globalExecIndex, leaderAddr)

	// Save SystemTransactions if present
	sysTxs := epochData.GetSystemTransactions()
	if len(sysTxs) > 0 {
		err := bp.chainState.GetBlockDatabase().SaveSystemTransactions(*currentBlockNumber, sysTxs)
		if err != nil {
			logger.Error("❌ [SYSTEM-TX] Failed to save SystemTransactions for block #%d: %v", *currentBlockNumber, err)
		} else {
			logger.Info("📡 [TELEMETRY] [SYSTEM-TX] Saved %d SystemTransactions for block #%d", len(sysTxs), *currentBlockNumber)
		}
	}

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
	bp.PushAsyncGEIUpdate(globalExecIndex, epochData.GetCommitHash(), commitIndex, epochNum)

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
