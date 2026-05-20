// block_state_commit.go — Centralized block state management
//
// This file provides a single CommitBlockState method that atomically updates
// ALL chain state components when a new block is committed. This replaces the
// scattered state update pattern where callers had to remember to call 4-5
// separate methods (SetLastBlock, SetcurrentBlockHeader, UpdateLastBlockNumber,
// SetBlockNumberToHash, UpdateStateForNewHeader) independently.
//
// Usage:
//
//	cs.CommitBlockState(blk, WithPersistToDB(), WithRebuildTries())
package blockchain

import (
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ═══════════════════════════════════════════════════════════════════════════════
// CommitOption — Functional options for CommitBlockState
// ═══════════════════════════════════════════════════════════════════════════════

// CommitOption configures how CommitBlockState behaves for different callers.
type CommitOption func(*commitConfig)

type commitConfig struct {
	persistToDB   bool // Save block to block database (LevelDB)
	rebuildTries  bool // Rebuild account/stake/SC tries from header roots
	saveTxMapping bool // Save tx hash → block number mappings
	commitMaps    bool // Commit blockchain instance mappings to LevelDB
}

// WithPersistToDB saves the block to the block database.
// Use for: consensus blocks, P2P synced blocks, transition guard flush.
func WithPersistToDB() CommitOption {
	return func(c *commitConfig) { c.persistToDB = true }
}

// WithRebuildTries rebuilds account/stake/SC tries from the block's header roots.
// Use when: state tries may be stale (after P2P sync, SyncOnly→Validator transition).
// NOT needed when: block was just executed locally (tries already up-to-date).
func WithRebuildTries() CommitOption {
	return func(c *commitConfig) { c.rebuildTries = true }
}

// WithSaveTxMapping saves tx hash → block number mappings.
// Use for: master node consensus blocks.
func WithSaveTxMapping() CommitOption {
	return func(c *commitConfig) { c.saveTxMapping = true }
}

// WithCommitMappings commits the blockchain instance mappings (block→hash, tx→block) to LevelDB.
// Use when: you need mappings to be durable immediately (transition guard, sync completion).
// NOT needed when: commitWorker will handle it later (normal consensus flow).
func WithCommitMappings() CommitOption {
	return func(c *commitConfig) { c.commitMaps = true }
}

// ═══════════════════════════════════════════════════════════════════════════════
// CommitBlockState — Single entry point for ALL block state updates
// ═══════════════════════════════════════════════════════════════════════════════

// CommitBlockState atomically updates ALL chain state components for a new block.
// This is the centralized method that replaces scattered calls to:
//   - SetcurrentBlockHeader      (always)
//   - blockchain.SetBlockNumberToHash  (always)
//   - storage.UpdateLastBlockNumber    (always)
//   - UpdateStateForNewHeader    (when WithRebuildTries)
//   - blockDatabase.SaveLastBlock      (when WithPersistToDB)
//   - blockchain.Commit                (when WithCommitMappings)
//
// Returns the block number that was committed.
func (cs *ChainState) CommitBlockState(blk types.Block, opts ...CommitOption) (uint64, error) {
	if blk == nil {
		return 0, nil
	}

	cs.commitMutex.Lock()
	defer cs.commitMutex.Unlock()

	// Parse options
	cfg := &commitConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	header := blk.Header()
	blockNum := header.BlockNumber()
	blockHash := header.Hash()

	// ═══════════════════════════════════════════════════════════════════════
	// SEQUENTIAL GUARD: Reject duplicate/old blocks
	// Go chỉ thực thi tuần tự — block phải tăng dần.
	// Block đã thực thi (blockNum <= lastBlock) → reject ngay.
	// Đây là tuyến phòng thủ cuối cùng chống fork/inflation.
	//
	// EXCEPTION (2026-03-24): WithRebuildTries bypasses this guard.
	// After peer sync, HandleSyncBlocksRequest updates the counter to
	// the last synced block (e.g., #122). When LAZY REFRESH then calls
	// CommitBlockState(block #122, WithRebuildTries()), the guard would
	// reject it as duplicate, silently skipping the trie rebuild. This
	// leaves the MPT trie at the snapshot state → stateRoot freeze → FORK.
	// WithRebuildTries is explicitly requesting a trie rebuild from an
	// already-committed block's header roots — safe to allow through.
	// ═══════════════════════════════════════════════════════════════════════
	lastBlockNum := storage.GetLastBlockNumber()
	
	// STRICT REJECT: Never allow rewriting strictly older blocks, even with bypass.
	if blockNum < lastBlockNum && blockNum > 0 {
		logger.Error("🚨 [SEQUENTIAL GUARD] STRICT REJECT: block #%d is strictly older than last committed #%d (hash: %s) — THIS BLOCK WILL NOT BE PERSISTED!",
			blockNum, lastBlockNum, blockHash.Hex()[:18])
		return blockNum, nil
	}

	// EXACT DUPLICATE REJECT: Reject duplicate unless explicitly rebuilding tries
	if blockNum == lastBlockNum && blockNum > 0 && !cfg.rebuildTries {
		logger.Warn("⚠️ [SEQUENTIAL GUARD] DUPLICATE REJECT: block #%d == last committed #%d (hash: %s) — skipping",
			blockNum, lastBlockNum, blockHash.Hex()[:18])
		return blockNum, nil // Return without error — silently skip
	}

	if blockNum > lastBlockNum+1 && lastBlockNum > 0 && !cfg.rebuildTries {
		// ═══════════════════════════════════════════════════════════════════════
		// GAP WARNING (not a halt): Rust assigns sequential block_numbers to ALL
		// commits, including empty ones that Go skips. This creates legitimate
		// gaps during recovery/catch-up. For example:
		//   - Go skips empty commits 204-207 (block_numbers 128-131)
		//   - Next non-empty commit 208 arrives with block_number=132
		//   - Gap from 127→132 is expected and safe
		// We log a warning but ALLOW the commit to proceed.
		// ═══════════════════════════════════════════════════════════════════════
		logger.Warn("⚠️ [SEQUENTIAL GUARD] Block gap detected: received block #%d but last committed is #%d (gap=%d). "+
			"This is expected during recovery when empty commits are skipped.",
			blockNum, lastBlockNum, blockNum-lastBlockNum-1)
	}

	bc := GetBlockChainInstance()

	// ─── 1. Update in-memory header pointer (always) ──────────────────────
	headerCopy := header
	cs.SetcurrentBlockHeader(&headerCopy)

	// ─── 2. Add block to in-memory block cache immediately (always) ──────
	// Doing this early satisfies concurrent read requests (e.g. from block hash checker)
	// without waiting for disk database persistence or block mapping updates.
	bc.AddBlockToCache(blk)

	// ─── 3. Persist block to DB (optional) ───────────────────────────────
	// CRITICAL CONCURRENCY FIX: This MUST run BEFORE SetBlockNumberToHash and UpdateLastBlockNumber
	// to prevent race conditions where concurrent queries find mapping/block counter updated
	// but block database lookup fails with null.
	if cfg.persistToDB {
		if err := cs.blockDatabase.SaveLastBlock(blk); err != nil {
			logger.Error("❌ [COMMIT STATE] Failed to save block #%d to DB: %v", blockNum, err)
			return blockNum, err
		}
		logger.Info("✅ [COMMIT STATE] Block #%d persisted to DB (hash: %s, parentHash: %s, txCount: %d, lastBlockNum_before: %d)",
			blockNum, blockHash.Hex()[:18], header.LastBlockHash().Hex()[:18], len(blk.Transactions()), lastBlockNum)
	}

	// ─── 4. Update block number → hash mapping (always) ──────────────────
	if err := bc.SetBlockNumberToHash(blockNum, blockHash); err != nil {
		logger.Error("❌ [COMMIT STATE] Failed to set block→hash mapping for block #%d: %v", blockNum, err)
		return blockNum, err
	}

	// ─── 5. Save tx hash → block number mappings (optional) ──────────────
	if cfg.saveTxMapping {
		for _, txHash := range blk.Transactions() {
			bc.SetTxHashMapBlockNumber(txHash, blockNum)
		}
	}

	// ─── 6. Update storage block counter (always) ────────────────────────
	// This is updated LAST among the mappings/counter block metadata to ensure that
	// if a concurrent reader detects the height has advanced, all the underlying mapping entries 
	// and DB/cache records are fully queryable and resolved without returning null.
	storage.UpdateLastBlockNumber(blockNum)

	// ─── 7. Rebuild state tries from header roots (optional) ─────────────
	if cfg.rebuildTries {
		if err := cs.UpdateStateForNewHeader(header); err != nil {
			logger.Error("❌ [COMMIT STATE] Failed to rebuild tries for block #%d: %v", blockNum, err)
			return blockNum, err
		}
		logger.Info("🔄 [COMMIT STATE] Rebuilt tries from block #%d header roots", blockNum)
	}

	// ─── 8. Commit mappings to LevelDB (optional) ────────────────────────
	if cfg.commitMaps {
		if err := bc.Commit(); err != nil {
			logger.Error("❌ [COMMIT STATE] Failed to commit mappings for block #%d: %v", blockNum, err)
			return blockNum, err
		}
	}

	// ─── 9. Auto-update epoch from block header ──────────────────────────
	cs.CheckAndUpdateEpochFromBlock(header.Epoch(), header.TimeStamp())

	logger.Debug("✅ [COMMIT STATE] Block #%d committed (persist=%v, rebuild=%v, txMap=%v, commit=%v)",
		blockNum, cfg.persistToDB, cfg.rebuildTries, cfg.saveTxMapping, cfg.commitMaps)

	return blockNum, nil
}
