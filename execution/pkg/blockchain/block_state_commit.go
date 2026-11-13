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
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/failpoint"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ═══════════════════════════════════════════════════════════════════════════════
// CommitOption — Functional options for CommitBlockState
// ═══════════════════════════════════════════════════════════════════════════════

// CommitOption configures how CommitBlockState behaves for different callers.
// requiresBlockChain reports whether a commit with this config cannot run without the BlockChain singleton: every
// commit except a rebuild-only recovery commit (which persists no block and writes no hash mappings).
func (c *commitConfig) requiresBlockChain() bool {
	return !c.rebuildTries || c.persistToDB || c.saveTxMapping || c.commitMaps
}

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

	// Fail closed when the BlockChain singleton is missing: only a rebuild-only recovery commit (which touches
	// neither the block DB nor the hash mappings) may run without it. Silently skipping the block->hash / tx
	// mappings while still reporting success would leave a committed block that RPC lookups cannot find.
	if GetBlockChainInstance() == nil {
		if cfg.requiresBlockChain() {
			return blockNum, fmt.Errorf("blockchain singleton not initialized: refusing to commit block #%d", blockNum)
		}
		logger.Warn("⚠️ [COMMIT STATE] BlockChain singleton not initialized: rebuild-only commit of block #%d skips block caches and mappings", blockNum)
	}

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

	// EXACT DUPLICATE REJECT: Reject duplicate unless explicitly rebuilding tries or there is a hash mismatch (fork at tip)
	if blockNum == lastBlockNum && blockNum > 0 && !cfg.rebuildTries {
		bc := GetBlockChainInstance()
		if localHash, ok := bc.GetBlockHashByNumber(blockNum); ok && localHash != blockHash {
			logger.Warn("⚠️ [SEQUENTIAL GUARD] Tip fork detected at block #%d: localHash (%s) != incomingHash (%s). Bypassing duplicate reject to allow overwrite.",
				blockNum, localHash.Hex()[:18], blockHash.Hex()[:18])
		} else {
			logger.Warn("⚠️ [SEQUENTIAL GUARD] DUPLICATE REJECT: block #%d == last committed #%d (hash: %s) — skipping",
				blockNum, lastBlockNum, blockHash.Hex()[:18])
			return blockNum, nil // Return without error — silently skip
		}
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

	// ─── 1. Rebuild or realign state tries FIRST (fail-closed before mutating DB/mappings) ───
	// Verifying/rebuilding tries from header roots BEFORE SaveLastBlock and SetBlockNumberToHash
	// guarantees that a missing state batch / missing trie node returns an error without leaving
	// orphaned blocks or mappings in BlockDatabase / BlockChain dirtyStorage.
	var activeAccountRoot common.Hash
	if asDB := cs.GetAccountStateDB(); asDB != nil {
		activeAccountRoot = asDB.GetOriginRootHash()
	}
	var activeStakeRoot common.Hash
	if stakeDB := cs.GetStakeStateDB(); stakeDB != nil {
		activeStakeRoot = stakeDB.GetOriginRootHash()
	}
	headerAccountRoot := header.AccountStatesRoot()
	headerStakeRoot := common.Hash(header.StakeStatesRoot())
	prevHeaderPtr := cs.currentBlockHeader.Load()
	didLightweightNomtRealign := false

	if trie.GetStateBackend() == trie.BackendNOMT {
		if cfg.rebuildTries {
			if err := cs.updateStateForNewHeader(header); err != nil {
				logger.Error("❌ [COMMIT STATE] Failed to align/rebuild tries for block #%d: %v", blockNum, err)
				return blockNum, err
			}
			logger.Info("🔄 [COMMIT STATE] Aligned/rebuilt tries from block #%d header roots", blockNum)
		} else if activeAccountRoot != headerAccountRoot || activeStakeRoot != headerStakeRoot {
			// Lightweight realignment: update Go's root pointers without touching C++ handle
			if asDB := cs.GetAccountStateDB(); asDB != nil {
				asDB.SetOriginRootHash(headerAccountRoot)
				if nomtTrie, ok := asDB.Trie().(*trie.NomtStateTrie); ok {
					nomtTrie.RealignRoot(headerAccountRoot)
				}
			}
			if stakeDB := cs.GetStakeStateDB(); stakeDB != nil {
				stakeDB.SetOriginRootHash(headerStakeRoot)
				if nomtTrie, ok := stakeDB.Trie().(*trie.NomtStateTrie); ok {
					nomtTrie.RealignRoot(headerStakeRoot)
				}
			}
			didLightweightNomtRealign = true
			logger.Debug("🔧 [COMMIT STATE] Lightweight root realignment for block #%d (account=%s, stake=%s)",
				blockNum, headerAccountRoot.Hex()[:18], headerStakeRoot.Hex()[:18])
		}
	} else {
		// Legacy MPT/LevelDB backend behavior
		if cfg.rebuildTries || activeAccountRoot != headerAccountRoot || activeStakeRoot != headerStakeRoot {
			if err := cs.updateStateForNewHeader(header); err != nil {
				logger.Error("❌ [COMMIT STATE] Failed to align/rebuild MPT tries for block #%d: %v", blockNum, err)
				return blockNum, err
			}
			logger.Info("🔄 [COMMIT STATE] Aligned/rebuilt MPT tries from block #%d header roots", blockNum)
		}
	}

	// Capture pre-mutation state so any failure in persistence or durability barriers rolls back cleanly.
	var (
		prevLastBlock        types.Block
		hadPrevLastBlock     bool
		existedInBlockDB     bool
		savedToBlockDB       bool
		prevMappedHash       common.Hash
		hadPrevMappedHash    bool
		hadPrevDirtyBlockMap bool
		updatedBlockMap      bool
		updatedTxMap         bool
		attemptedCommitMaps  bool
		addedBlockCache      bool
		prevTxMappedBlocks   map[common.Hash]uint64
		prevDirtyTxMaps      map[common.Hash]bool
		mapDBToRestore       [][2][]byte
		mapDBToDelete        [][]byte
	)
	if cfg.persistToDB && cs.blockDatabase != nil {
		if lb, lbErr := cs.blockDatabase.GetLastBlock(); lbErr == nil && lb != nil {
			prevLastBlock = lb
			hadPrevLastBlock = true
		}
		if _, existErr := cs.blockDatabase.GetBlockByHash(blockHash); existErr == nil {
			existedInBlockDB = true
		}
	}
	blockMapKeyStr := blockNumberPrefix + strconv.FormatUint(blockNum, 10)
	if bc != nil {
		prevMappedHash, hadPrevMappedHash = bc.GetBlockHashByNumberFast(blockNum)
		bc.dirtyLock.RLock()
		if bc.dirtyStorage != nil {
			_, hadPrevDirtyBlockMap = bc.dirtyStorage.Load(blockMapKeyStr)
		}
		bc.dirtyLock.RUnlock()

		txs := blk.Transactions()
		if cfg.saveTxMapping && len(txs) > 0 {
			prevTxMappedBlocks = make(map[common.Hash]uint64, len(txs))
			prevDirtyTxMaps = make(map[common.Hash]bool, len(txs))
			seenTx := make(map[common.Hash]struct{}, len(txs))
			for _, txH := range txs {
				if _, dup := seenTx[txH]; dup {
					continue
				}
				seenTx[txH] = struct{}{}
				if prevBn, found := bc.GetBlockNumberByTxHashFast(txH); found {
					prevTxMappedBlocks[txH] = prevBn
				}
				bc.dirtyLock.RLock()
				if bc.dirtyStorage != nil {
					if _, inDirty := bc.dirtyStorage.Load(txHashPrefix + txH.Hex()); inDirty {
						prevDirtyTxMaps[txH] = true
					}
				}
				bc.dirtyLock.RUnlock()
			}
		}

		if cfg.commitMaps && bc.storageManager != nil && bc.storageManager.GetStorageMapping() != nil {
			mapStore := bc.storageManager.GetStorageMapping()
			captureMapKey := func(k []byte) {
				kCopy := append([]byte(nil), k...)
				if oldVal, getErr := mapStore.Get(k); getErr == nil && oldVal != nil {
					mapDBToRestore = append(mapDBToRestore, [2][]byte{kCopy, append([]byte(nil), oldVal...)})
				} else {
					mapDBToDelete = append(mapDBToDelete, kCopy)
				}
			}
			captureMapKey([]byte(blockMapKeyStr))
			if cfg.saveTxMapping && len(txs) > 0 {
				seenTx := make(map[common.Hash]struct{}, len(txs))
				for _, txH := range txs {
					if _, dup := seenTx[txH]; dup {
						continue
					}
					seenTx[txH] = struct{}{}
					captureMapKey([]byte(txHashPrefix + txH.Hex()))
				}
			}
		}
	}

	rollbackOnErr := func() {
		cs.currentBlockHeader.Store(prevHeaderPtr)
		if didLightweightNomtRealign {
			if asDB := cs.GetAccountStateDB(); asDB != nil {
				asDB.SetOriginRootHash(activeAccountRoot)
				if nomtTrie, ok := asDB.Trie().(*trie.NomtStateTrie); ok {
					nomtTrie.RealignRoot(activeAccountRoot)
				}
			}
			if stakeDB := cs.GetStakeStateDB(); stakeDB != nil {
				stakeDB.SetOriginRootHash(activeStakeRoot)
				if nomtTrie, ok := stakeDB.Trie().(*trie.NomtStateTrie); ok {
					nomtTrie.RealignRoot(activeStakeRoot)
				}
			}
		}
		if bc != nil {
			if attemptedCommitMaps && bc.storageManager != nil && bc.storageManager.GetStorageMapping() != nil {
				mapStore := bc.storageManager.GetStorageMapping()
				if len(mapDBToRestore) > 0 {
					_ = mapStore.BatchPut(mapDBToRestore)
				}
				if len(mapDBToDelete) > 0 {
					_ = mapStore.BatchDelete(mapDBToDelete)
				}
			}
			if updatedTxMap {
				bc.DiscardTxMappings(blk.Transactions())
				for txH, prevBn := range prevTxMappedBlocks {
					bc.txHashToBlockNumber.Store(txH, cachedUint64{value: prevBn})
					if !attemptedCommitMaps || prevDirtyTxMaps[txH] {
						bnBytes := make([]byte, 8)
						binary.BigEndian.PutUint64(bnBytes, prevBn)
						bc.storeToDirty(txHashPrefix+txH.Hex(), bnBytes)
					}
				}
			}
			if updatedBlockMap {
				if hadPrevMappedHash {
					_ = bc.SetBlockNumberToHash(blockNum, prevMappedHash)
					if attemptedCommitMaps && !hadPrevDirtyBlockMap {
						bc.removeFromDirty(blockMapKeyStr)
					}
				} else {
					bc.DiscardBlockMappings(blockNum)
				}
			}
			if addedBlockCache && !existedInBlockDB {
				bc.DeleteBlockFromCache(blockHash)
			}
		}
		if savedToBlockDB && cs.blockDatabase != nil {
			if !existedInBlockDB {
				_ = cs.blockDatabase.DeleteBlockByHash(blockHash)
			}
			if hadPrevLastBlock && prevLastBlock != nil {
				_ = cs.blockDatabase.SaveLastBlock(prevLastBlock)
			} else {
				_ = cs.blockDatabase.DeleteLastBlockKey()
			}
		}
	}

	// ─── 2. Update in-memory header pointer (always) ──────────────────────
	headerCopy := header
	cs.SetcurrentBlockHeader(&headerCopy)

	// ─── 3. Add block to in-memory block cache immediately (always) ──────
	// Doing this early satisfies concurrent read requests (e.g. from block hash checker)
	// without waiting for disk database persistence or block mapping updates.
	if bc != nil {
		bc.AddBlockToCache(blk)
		addedBlockCache = true
	}

	// ─── 4. Persist block to DB (optional) ───────────────────────────────
	// CRITICAL CONCURRENCY FIX: This MUST run BEFORE SetBlockNumberToHash and UpdateLastBlockNumber
	// to prevent race conditions where concurrent queries find mapping/block counter updated
	// but block database lookup fails with null.
	if cfg.persistToDB {
		if err := cs.blockDatabase.SaveLastBlock(blk); err != nil {
			logger.Error("❌ [COMMIT STATE] Failed to save block #%d to DB: %v", blockNum, err)
			rollbackOnErr()
			return blockNum, err
		}
		savedToBlockDB = true
		// PebbleDB handles durability via write ahead logging (WAL).
		// Forced memtable flushes are removed to prevent I/O bottlenecks.
		logger.Info("✅ [COMMIT STATE] Block #%d persisted to DB and flushed (hash: %s, parentHash: %s, txCount: %d, lastBlockNum_before: %d)",
			blockNum, blockHash.Hex()[:18], header.LastBlockHash().Hex()[:18], len(blk.Transactions()), lastBlockNum)
	}

	// ─── 5. Update block number → hash mapping (always) ──────────────────
	if bc != nil {
		if err := bc.SetBlockNumberToHash(blockNum, blockHash); err != nil {
			logger.Error("❌ [COMMIT STATE] Failed to set block→hash mapping for block #%d: %v", blockNum, err)
			rollbackOnErr()
			return blockNum, err
		}
		updatedBlockMap = true
	}

	// ─── 6. Save tx hash → block number mappings (optional) ──────────────
	if bc != nil && cfg.saveTxMapping {
		txs := blk.Transactions()
		if len(txs) > 0 {
			updatedTxMap = true
			if err := bc.SetTxHashMapBlockNumberBatch(txs, blockNum); err != nil {
				logger.Error("❌ [COMMIT STATE] Failed to set tx→block mappings for block #%d: %v", blockNum, err)
				rollbackOnErr()
				return blockNum, err
			}
		}
	}

	// ─── 7. Commit mappings to LevelDB (optional) ────────────────────────
	if bc != nil && cfg.commitMaps {
		attemptedCommitMaps = true
		if err := bc.Commit(); err != nil {
			logger.Error("❌ [COMMIT STATE] Failed to commit mappings for block #%d: %v", blockNum, err)
			rollbackOnErr()
			return blockNum, err
		}
	}

	// ─── 8a. Durable replay/query dependencies ────────────────────────────
	// A process crash may leave the canonical block visible from its Pebble
	// WAL even before the block barrier below. Recovery bypasses such a block,
	// so its receipt and transaction-state tries must already be readable.
	if cfg.persistToDB && cs.storageManager != nil {
		failpoint.Hit("before-receipts-barrier")
		if err := storage.SyncDurable(cs.storageManager.GetStorageReceipt()); err != nil {
			rollbackOnErr()
			return blockNum, err
		}
		failpoint.Hit("after-receipts-barrier")

		failpoint.Hit("before-transaction-state-barrier")
		if err := storage.SyncDurable(cs.storageManager.GetStorageTransaction()); err != nil {
			rollbackOnErr()
			return blockNum, err
		}
		failpoint.Hit("after-transaction-state-barrier")
	}

	// ─── 8b. Durability barrier: block DB BEFORE NOMT ────────────────────
	// The caller persists this block's NOMT payloads right after we return, and NOMT fsyncs its
	// own data. The block database, however, only buffers writes (Go-level cache flushed every 5s,
	// Pebble WAL written with NoSync), so without this a power loss leaves NOMT AHEAD of the block
	// database: the header/tip of the last blocks is gone while their state is already on disk,
	// and the node then refuses to start (NOMT root != tip header root). Making the block
	// database durable first guarantees NOMT can never be ahead of the durable tip.
	// On failure return the error so the caller does not persist NOMT for this block.
	if cfg.persistToDB {
		failpoint.Hit("before-block-barrier")
		if err := storage.SyncDurable(cs.blockDatabase.GetDB()); err != nil {
			logger.Error("❌ [COMMIT STATE] Failed to make block #%d durable before NOMT commit: %v", blockNum, err)
			rollbackOnErr()
			return blockNum, err
		}
		failpoint.Hit("after-block-barrier")
	}

	if cs.storageManager != nil {
		for _, txHash := range blk.Transactions() {
			_ = cs.storageManager.CommitDeviceKey(txHash)
		}
	}

	// ─── 9. Update storage block counter and auto-update epoch ────────────
	// Exposed only after block database and dependencies are fully durable.
	// The counter doubles as the Raft FSM's Durable() watermark (raftfeed Snapshot/Restore): once Raft compacts its
	// log the block DB is the only replay source, so "block DB durable" is exactly what that watermark must mean.
	// NOMT is persisted by the caller after we return and can only lag the block DB, never lead it. On restart a lagging NOMT
	// is handled by app_blockchain.go: the startup tip is aligned back to the block NOMT's roots belong to
	// (findBlockMatchingNomtRoots/alignStartupTipToBlock), or the tries are rebuilt from the durable changelog
	// (CommitBlockState + WithRebuildTries); the C0 spike kill-point matrix exercises both.
	storage.UpdateLastBlockNumber(blockNum)
	cs.CheckAndUpdateEpochFromBlock(header.Epoch(), header.TimeStamp())

	failpoint.Hit("before-done-signal")

	logger.Debug("✅ [COMMIT STATE] Block #%d committed (persist=%v, rebuild=%v, txMap=%v, commit=%v)",
		blockNum, cfg.persistToDB, cfg.rebuildTries, cfg.saveTxMapping, cfg.commitMaps)

	return blockNum, nil
}
