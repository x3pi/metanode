package executor

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
	"google.golang.org/protobuf/proto"
)

// shouldImportPeerCommitIndex decides whether a synced block's embedded
// header.CommitIndex() is safe to adopt as this node's own lastHandledCommitIndex.
//
// ARCHITECTURE FIX (2026-09-12, note/startup_sync_commit_index_import_fork_design_2026-09.md):
// CommitIndex is the SENDING peer's own node-local consensus-DAG round counter, not a
// value safe to graft onto a different node's bookkeeping -- fork_guard.rs (Rust side)
// documents that two honest validators can legitimately use different local commit-round
// numbers for the identical GEI/state. Importing it wholesale caused a real,
// live-reproduced fork: the receiving node's next_expected_index aliased to the wrong
// commit in its own DAG, producing different block content at the same GlobalExecIndex.
//
// Only a caller with NO DAG of its own (a genuine SyncOnly node, preserveOwnCommitIndex
// == false, the pre-existing default) should have this value imported -- a Validator
// (preserveOwnCommitIndex == true) owns this bookkeeping itself and must not have it
// overwritten from peer data.
func shouldImportPeerCommitIndex(preserveOwnCommitIndex bool, commitIndexFromHeader uint64) bool {
	return !preserveOwnCommitIndex && commitIndexFromHeader > 0
}

// HandleSetSyncStartBlockRequest - Called by Rust when consensus ends
// Tells Go: "Consensus ended at last_consensus_block, sync should start from last_consensus_block + 1"
func (rh *RequestHandler) HandleSetSyncStartBlockRequest(request *pb.SetSyncStartBlockRequest) (*pb.SetSyncStartBlockResponse, error) {
	lastConsensusBlock := request.GetLastConsensusBlock()
	syncStartBlock := lastConsensusBlock + 1
	logger.Info("🔄 [TRANSITION HANDOFF] SetSyncStartBlock: consensus ended at block %d, sync will start from block %d", lastConsensusBlock, syncStartBlock)

	// Get current last block from Go storage
	currentLastBlock := storage.GetLastBlockNumber()

	// Verify the transition makes sense
	if currentLastBlock > lastConsensusBlock {
		// Go has processed more blocks than consensus claims
		logger.Warn("⚠️ [TRANSITION HANDOFF] Unexpected state: current_last_block=%d > last_consensus_block=%d",
			currentLastBlock, lastConsensusBlock)
	}

	// Store sync start block for network sync to use
	storage.SetSyncStartBlock(syncStartBlock)

	logger.Info("✅ [TRANSITION HANDOFF] Sync start block set to %d (consensus ended at %d)", syncStartBlock, lastConsensusBlock)

	return &pb.SetSyncStartBlockResponse{
		Success:        true,
		SyncStartBlock: syncStartBlock,
		Message:        fmt.Sprintf("Sync will start from block %d", syncStartBlock),
	}, nil
}

// HandleWaitForSyncToBlockRequest - Called by Rust to wait for Go sync to reach a specific block
// Used during SyncOnly -> Validator transition to ensure sync is complete before consensus starts
func (rh *RequestHandler) HandleWaitForSyncToBlockRequest(request *pb.WaitForSyncToBlockRequest) (*pb.WaitForSyncToBlockResponse, error) {
	targetBlock := request.GetTargetBlock()
	timeoutSeconds := request.GetTimeoutSeconds()

	// Default timeout if not specified
	if timeoutSeconds == 0 {
		timeoutSeconds = 30 // 30 seconds default
	}

	logger.Info("⏳ [TRANSITION HANDOFF] WaitForSyncToBlock: waiting for sync to reach block %d (timeout: %ds)", targetBlock, timeoutSeconds)

	// Check immediately if already reached
	currentBlock := storage.GetLastBlockNumber()
	if currentBlock >= targetBlock {
		logger.Info("✅ [TRANSITION HANDOFF] Already at or past target block: current=%d, target=%d", currentBlock, targetBlock)
		return &pb.WaitForSyncToBlockResponse{
			Reached:      true,
			CurrentBlock: currentBlock,
			Message:      fmt.Sprintf("Already at block %d (target: %d)", currentBlock, targetBlock),
		}, nil
	}

	// Poll until target is reached or timeout
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		currentBlock = storage.GetLastBlockNumber()
		if currentBlock >= targetBlock {
			logger.Info("✅ [TRANSITION HANDOFF] Target block reached: current=%d, target=%d", currentBlock, targetBlock)
			return &pb.WaitForSyncToBlockResponse{
				Reached:      true,
				CurrentBlock: currentBlock,
				Message:      fmt.Sprintf("Reached block %d (target: %d)", currentBlock, targetBlock),
			}, nil
		}
		time.Sleep(pollInterval)
	}

	// Timeout
	logger.Warn("⚠️ [TRANSITION HANDOFF] Timeout waiting for sync: current=%d, target=%d", currentBlock, targetBlock)
	return &pb.WaitForSyncToBlockResponse{
		Reached:      false,
		CurrentBlock: currentBlock,
		Message:      fmt.Sprintf("Timeout after %ds: current=%d, target=%d", timeoutSeconds, currentBlock, targetBlock),
	}, nil
}

func hasAnyBackupDbPayload(b *storage.BackUpDb) bool {
	if b == nil {
		return false
	}
	if len(b.AccountBatch) > 0 ||
		len(b.CodeBatchPut) > 0 ||
		len(b.SmartContractBatch) > 0 ||
		len(b.SmartContractStorageBatch) > 0 ||
		len(b.ReceiptBatchPut) > 0 ||
		len(b.TxBatchPut) > 0 ||
		len(b.MapppingBatch) > 0 ||
		len(b.StakeState) > 0 ||
		len(b.DevicePut) > 0 ||
		len(b.DeviceDelete) > 0 {
		return true
	}
	for _, v := range b.TrieDatabaseBatchPut {
		if len(v) > 0 {
			return true
		}
	}
	for _, m := range b.FullDbLogs {
		for _, v := range m {
			if len(v) > 0 {
				return true
			}
		}
	}
	return false
}

// HandleSyncBlocksRequest processes a SyncBlocksRequest and syncs blocks to local storage
// This is used by nodes to receive blocks fetched from peers via Rust orchestration
func (rh *RequestHandler) HandleSyncBlocksRequest(request *pb.SyncBlocksRequest) (*pb.SyncBlocksResponse, error) {
	blocks := request.GetBlocks()
	blockCount := len(blocks)

	logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Handling SyncBlocksRequest: block_count=%d", blockCount)

	// ═══════════════════════════════════════════════════════════════════════════
	// DEADLOCK PREVENTION (May 2026): Reject all sync requests during snapshot.
	// HandleSyncBlocksRequest opens NOMT sessions that are NOT gated by snapshotGate
	// or ExecutionMutex natively. If a snapshot triggers while these sessions are active,
	// CloseForSnapshot waits forever → DEADLOCK.
	// Rust will retry this RPC after the snapshot completes.
	// ═══════════════════════════════════════════════════════════════════════════
	if sm := rh.getSnapshotManager(); sm != nil && sm.IsSnapshotInProgress() {
		logger.Warn("⏸️ [SYNC] SyncBlocksRequest rejected: snapshot in progress. Rust should retry.")
		return &pb.SyncBlocksResponse{
			SyncedCount:     0,
			LastSyncedBlock: 0,
			Error:           "snapshot in progress, retry later",
		}, nil
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PIPELINE SYNC: Always wait for commitWorker to flush pending block commits.
	// This ensures that before block sync processes and writes blocks to PebbleDB/NOMT,
	// all prior consensus block executions have been fully committed to disk,
	// preventing concurrent PebbleDB writes or stale parent root checks.
	// CRITICAL FIX (Jun 2026): Must wait for persistence BEFORE acquiring ExecutionMutex.Lock(),
	// otherwise commitWorker will deadlock waiting for ExecutionMutex.RLock().
	// ═══════════════════════════════════════════════════════════════════════════
	if sm := rh.getSnapshotManager(); sm != nil {
		logger.Info("⏳ [SYNC] Waiting for commitWorker to flush pending blocks before processing sync...")
		if err := sm.WaitForPersistence(); err != nil {
			logger.Error("🚨 [SYNC] WaitForPersistence reported error: %v — aborting sync to prevent state corruption", err)
			return &pb.SyncBlocksResponse{
				SyncedCount:     0,
				LastSyncedBlock: 0,
				Error:           fmt.Sprintf("prior commit failed: %v", err),
			}, nil
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// CONCURRENCY & FORK-SAFETY GATE (May 2026): Acquire ExecutionMutex lock.
	// This serializes block sync writes with consensus block execution, preventing
	// NOMT trie root invalidations (Changeset no longer valid FFI errors).
	// ═══════════════════════════════════════════════════════════════════════════
	if rh.lockExecutionCallback != nil {
		logger.Info("🔒 [SYNC-GATE] Acquiring ExecutionMutex lock for block synchronization")
		rh.lockExecutionCallback()
		defer func() {
			logger.Info("🔓 [SYNC-GATE] Releasing ExecutionMutex lock after block synchronization")
			rh.unlockExecutionCallback()
		}()
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// FAST PREEMPTION (Aug 2026): Cancel and abort ALL speculative executions
	// while holding ExecutionMutex.Lock().
	// This guarantees no new speculative workers can start, and all existing in-flight
	// and finished sessions are aborted, ensuring NOMT activeCount is 0 before sync writes.
	// ═══════════════════════════════════════════════════════════════════════════
	if rh.cancelSpeculativeCallback != nil {
		rh.cancelSpeculativeCallback()
	}

	if blockCount == 0 {
		return &pb.SyncBlocksResponse{
			SyncedCount:     0,
			LastSyncedBlock: 0,
			Error:           "No blocks to sync",
		}, nil
	}

	blockDatabase := block.NewBlockDatabase(rh.storageManager.GetStorageBlock())
	bc := blockchain.GetBlockChainInstance()

	var executedCount uint64 = 0
	var lastExecutedBlock uint64 = 0
	var lastExecutedGEI uint64 = 0
	var lastExecutedBlk types.Block
	// ═══════════════════════════════════════════════════════════════════════════
	// NOMT TRIE REBUILD DECISION:
	// When execute_mode=true, the caller is either:
	//   1. STARTUP-SYNC (consensus_node.rs) — runs BEFORE consensus starts
	//   2. SyncOnly node (sync_loop.rs) — never runs consensus
	// In both cases, there is NO concurrent consensus execution, so NOMT
	// trie rebuild is SAFE on the last block.
	//
	// CRITICAL FIX (May 2026): The old check `GetLastBlockNumber() == 0`
	// was too restrictive. When a node restarts with block=470, STARTUP-SYNC
	// syncs blocks 471-536 but SKIPS NOMT rebuild → NOMT trie stays at
	// block 470's root → new consensus blocks get wrong state_root → FORK.
	// ═══════════════════════════════════════════════════════════════════════════
	isPreConsensusSync := request.GetExecuteMode()
	if !isPreConsensusSync {
		logger.Info("🛡️ [SYNC-SAFETY] Forcing execute_mode=true to prevent state root freeze")
		isPreConsensusSync = true
	}
	storage.SetPreConsensusSyncActive(isPreConsensusSync)
	defer storage.SetPreConsensusSyncActive(false)

	if isPreConsensusSync {
		logger.Info("🔧 [STARTUP-SYNC] execute_mode=true: NOMT trie rebuild will be ENABLED on last block (no concurrent consensus)")
	}

	// R7: Crash-guard for Cache Invalidation
	// Guarantee cache is invalidated on exit if any blocks were executed
	defer func() {
		if executedCount > 0 {
			rh.chainState.InvalidateAllState()
			mvm.ClearAllMVMApi()
			mvm.ClearAllProtectedMVMApi() // CRITICAL: Clear protected instances that hold stale data
			mvm.CallClearAllStateInstances()
			logger.Debug("🧹 [SNAPSHOT-RESUME] Deferred cache invalidation complete after batch sync")
		}
	}()

	var allNomtSessions []trie.NomtSessionToFlush

	// finalizeSyncedPrefix flushes deferred NOMT sessions, aligns final tries, commits & flushes
	// BlockChain mappings, force-syncs the last committed block, and invokes state/GEI/nonce
	// callbacks for any committed prefix (executedCount > 0). Calling this on both normal loop
	// completion and mid-batch aborts prevents ADV-18 (lost mappings & stale GEIAuthority).
	finalizeSyncedPrefix := func() error {
		if executedCount == 0 {
			return nil
		}

		if len(allNomtSessions) > 0 {
			logger.Info("🔧 [NOMT-SYNC] Flushing %d deferred NOMT sessions to disk after chunk completion", len(allNomtSessions))
			if err := trie.FlushNomtSessions(allNomtSessions); err != nil {
				logger.Error("❌ [NOMT-SYNC] Failed to flush deferred NOMT sessions: %v", err)
				return fmt.Errorf("failed to flush deferred NOMT sessions: %w", err)
			}
			allNomtSessions = nil
		}

		targetBlk := lastExecutedBlk
		if targetBlk == nil && lastExecutedBlock > 0 {
			if hash, ok := bc.GetBlockHashByNumber(lastExecutedBlock); ok {
				if blkLoaded, err := blockDatabase.GetBlockByHash(hash); err == nil && blkLoaded != nil {
					targetBlk = blkLoaded
				}
			}
		}

		if isPreConsensusSync && targetBlk != nil {
			goTipBlock := storage.GetLastBlockNumber()
			if trie.GetStateBackend() == trie.BackendNOMT {
				if lastExecutedBlock >= goTipBlock {
					logger.Info("🔧 [STARTUP-SYNC] Forcing final NOMT trie alignment for last synced block #%d (Go tip is #%d)", lastExecutedBlock, goTipBlock)
					if err := rh.chainState.UpdateStateForNewHeader(targetBlk.Header()); err != nil {
						logger.Error("❌ [STARTUP-SYNC] Failed to align final NOMT trie for block #%d: %v", lastExecutedBlock, err)
						return fmt.Errorf("failed to align final NOMT trie for block %d: %w", lastExecutedBlock, err)
					}
					rh.chainState.InvalidateAllState()
					finalTrieRoot := rh.chainState.GetAccountStateDB().Trie().Hash()
					logger.Info("✅ [STARTUP-SYNC] Final NOMT trie aligned: trieRoot=%s (block=#%d). Ready.",
						finalTrieRoot.Hex()[:18]+"...", lastExecutedBlock)
				} else {
					logger.Info("🔧 [STARTUP-SYNC] Skipping final NOMT trie alignment for block #%d because Go tip is ahead (#%d)", lastExecutedBlock, goTipBlock)
				}
			} else {
				expectedAcc := targetBlk.Header().AccountStatesRoot()
				expectedStake := common.Hash(targetBlk.Header().StakeStatesRoot())
				if rh.chainState.GetAccountStateDB().Trie().Hash() != expectedAcc ||
					rh.chainState.GetStakeStateDB().Trie().Hash() != expectedStake {
					if err := rh.chainState.UpdateStateForNewHeader(targetBlk.Header()); err != nil {
						logger.Error("❌ [STARTUP-SYNC] Failed to align final MPT trie for block #%d: %v", lastExecutedBlock, err)
						return fmt.Errorf("failed to align final MPT trie for block %d: %w", lastExecutedBlock, err)
					}
				}
			}
		}

		if err := bc.Commit(); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to commit block number mappings: %v", err)
			return fmt.Errorf("failed to commit block mappings: %w", err)
		}
		if rh.storageManager != nil && rh.storageManager.GetStorageMapping() != nil {
			if flushErr := rh.storageManager.GetStorageMapping().Flush(); flushErr != nil {
				logger.Error("❌ [SNAPSHOT-RESUME] Failed to flush mapping DB: %v", flushErr)
				return fmt.Errorf("failed to flush mapping DB: %w", flushErr)
			}
			logger.Info("💾 [SNAPSHOT-RESUME] Successfully flushed mapping DB to disk after batch sync")
		}

		if targetBlk != nil {
			logger.Info("💾 [STARTUP-SYNC] Forcing synchronous flush of last synced block #%d to disk", lastExecutedBlock)
			if syncErr := blockDatabase.SaveLastBlockSync(targetBlk); syncErr != nil {
				logger.Error("❌ [STARTUP-SYNC] Failed to force-sync last block #%d: %v", lastExecutedBlock, syncErr)
			}
			if rh.updateLastBlockCallback != nil {
				rh.updateLastBlockCallback(targetBlk)
			}
		}

		if lastExecutedGEI > 0 && rh.pushAsyncGEIUpdateCallback != nil {
			var lastCommitIdx uint32 = 0
			var lastEpoch uint64 = 0
			if targetBlk != nil {
				lastEpoch = targetBlk.Header().Epoch()
				if shouldImportPeerCommitIndex(request.PreserveOwnCommitIndex, targetBlk.Header().CommitIndex()) {
					lastCommitIdx = uint32(targetBlk.Header().CommitIndex())
				}
			}
			rh.pushAsyncGEIUpdateCallback(lastExecutedGEI, nil, lastCommitIdx, lastEpoch)
			logger.Info("🔄 [STARTUP-SYNC] Synchronized GEIAuthority to %d (commitIndex: %d, epoch: %d)", lastExecutedGEI, lastCommitIdx, lastEpoch)

			if lastCommitIdx > 0 {
				currentEpoch := rh.chainState.GetCurrentEpoch()
				storage.UpdateLastHandledCommitIndex(lastCommitIdx)
				storage.UpdateLastHandledCommitEpoch(currentEpoch)

				if rh.storageManager != nil && rh.storageManager.GetStorageBackupDb() != nil {
					var batch [][2][]byte
					batch = append(batch, [2][]byte{storage.LastHandledCommitIndexHashKey.Bytes(), utils.Uint32ToBytes(lastCommitIdx)})
					batch = append(batch, [2][]byte{storage.LastHandledCommitEpochHashKey.Bytes(), utils.Uint64ToBytes(currentEpoch)})
					if err := rh.storageManager.GetStorageBackupDb().BatchPut(batch); err != nil {
						logger.Error("❌ [STARTUP-SYNC] Failed to persist LastHandledCommitIndex to BackupDb: %v", err)
					} else {
						logger.Info("✅ [STARTUP-SYNC] Synchronously persisted LastHandledCommitIndex=%d, Epoch=%d for immediate Rust consensus initialization", lastCommitIdx, currentEpoch)
					}
				}
			}
		}

		if rh.clearNoncesCacheCallback != nil {
			rh.clearNoncesCacheCallback()
		}
		return nil
	}

	abortSyncBatch := func(errMsg string) (*pb.SyncBlocksResponse, error) {
		if finErr := finalizeSyncedPrefix(); finErr != nil {
			errMsg = fmt.Sprintf("%s (prefix finalization error: %v)", errMsg, finErr)
		}
		return &pb.SyncBlocksResponse{
			SyncedCount:     executedCount,
			LastSyncedBlock: lastExecutedBlock,
			LastExecutedGei: lastExecutedGEI,
			Error:           errMsg,
		}, nil
	}

	// rollbackLocalChainToBlock atomically rewinds Xapian AND local chain state/storages
	// (ChainState tries, StateChangelogDBs, BlockDatabase, BlockChain mappings, BackupDb,
	// and persistent counters) back to targetBlock. Fail-closed if Xapian or ChainState
	// alignment fails so Xapian and ChainDB never diverge.
	rollbackLocalChainToBlock := func(targetBlock uint64, incomingHashToKeep common.Hash) error {
		maxTip := storage.GetLastBlockNumber()
		if lastBlk, err := blockDatabase.GetLastBlock(); err == nil && lastBlk != nil && lastBlk.Header() != nil {
			if lastBlk.Header().BlockNumber() > maxTip {
				maxTip = lastBlk.Header().BlockNumber()
			}
		}
		if rh.chainState != nil {
			for _, cl := range []*state_changelog.StateChangelogDB{
				rh.chainState.GetChangelogDB(),
				rh.chainState.GetStakeChangelogDB(),
			} {
				if cl != nil && cl.GetHighestBlock() > maxTip {
					maxTip = cl.GetHighestBlock()
				}
			}
			if scDB := rh.chainState.GetSmartContractDB(); scDB != nil {
				if scCl := scDB.GetChangelogDB(); scCl != nil && scCl.GetHighestBlock() > maxTip {
					maxTip = scCl.GetHighestBlock()
				}
			}
		}

		var targetBlk types.Block
		var targetHash common.Hash
		if th, ok := bc.GetBlockHashByNumber(targetBlock); ok && th != (common.Hash{}) {
			targetHash = th
			if bLoaded, err := blockDatabase.GetBlockByHash(th); err == nil && bLoaded != nil {
				targetBlk = bLoaded
			}
		}

		// Inspect BackupDb BEFORE deleting fork backup records or mutating Xapian so we can
		// reconstruct validator NOMT pre-images (when changelogDB == nil), stale EventLogs,
		// and latest FullDbLogs index entries.
		var backupStorage storage.Storage
		if rh.storageManager != nil {
			backupStorage = rh.storageManager.GetStorageBackupDb()
		}
		loadBackupAt := func(bn uint64) (*storage.BackUpDb, bool) {
			if backupStorage == nil {
				return nil, false
			}
			raw, gErr := backupStorage.Get([]byte(fmt.Sprintf("block_data_topic-%d", bn)))
			if gErr != nil || len(raw) == 0 {
				raw, gErr = backupStorage.Get([]byte(fmt.Sprintf("backup_%d", bn)))
			}
			if gErr != nil || len(raw) == 0 {
				return nil, false
			}
			bkp, dErr := storage.DeserializeBackupDb(raw)
			if dErr != nil {
				return nil, false
			}
			return &bkp, true
		}

		needValidatorNomtAcc := trie.GetStateBackend() == trie.BackendNOMT &&
			(rh.chainState == nil || rh.chainState.GetChangelogDB() == nil)
		needValidatorNomtStake := trie.GetStateBackend() == trie.BackendNOMT &&
			(rh.chainState == nil || rh.chainState.GetStakeChangelogDB() == nil)

		staleNomtAccKeys := make(map[string][]byte)
		accSeenAbove := make(map[string]bool)
		accHasResolved := make(map[string]bool)
		restoredNomtAcc := make(map[string][]byte)

		staleNomtStakeKeys := make(map[string][]byte)
		stakeSeenAbove := make(map[string]bool)
		stakeHasResolved := make(map[string]bool)
		restoredNomtStake := make(map[string][]byte)

		staleEventLogKeys := make(map[string][]byte)
		staleFullDbLogAddrs := make(map[string]struct{})

		// Pass A: Walk rolled-back blocks forward (targetBlock+1 -> maxTip) so the FIRST
		// block above targetBlock touching each key yields its exact pre-fork pre-image.
		for bn := targetBlock + 1; bn <= maxTip; bn++ {
			if needValidatorNomtAcc {
				if undoEntries, ok := storage.LoadNomtUndoBatch(backupStorage, storage.NomtUndoKindAccount, bn); ok {
					for _, kv := range undoEntries {
						ks := string(kv[0])
						staleNomtAccKeys[ks] = append([]byte(nil), kv[0]...)
						if !accSeenAbove[ks] {
							accSeenAbove[ks] = true
							accHasResolved[ks] = true
							if len(kv[1]) > 0 {
								restoredNomtAcc[ks] = append([]byte(nil), kv[1]...)
							} else {
								// Explicitly newly inserted at block bn -> delete on rollback.
								restoredNomtAcc[ks] = nil
							}
						}
					}
				}
			}
			if needValidatorNomtStake {
				if undoEntries, ok := storage.LoadNomtUndoBatch(backupStorage, storage.NomtUndoKindStake, bn); ok {
					for _, kv := range undoEntries {
						ks := string(kv[0])
						staleNomtStakeKeys[ks] = append([]byte(nil), kv[0]...)
						if !stakeSeenAbove[ks] {
							stakeSeenAbove[ks] = true
							stakeHasResolved[ks] = true
							if len(kv[1]) > 0 {
								restoredNomtStake[ks] = append([]byte(nil), kv[1]...)
							} else {
								restoredNomtStake[ks] = nil
							}
						}
					}
				}
			}

			bkp, ok := loadBackupAt(bn)
			if !ok {
				continue
			}
			if needValidatorNomtAcc && len(bkp.AccountBatch) > 0 {
				if batch, err := storage.DeserializeBatch(bkp.AccountBatch); err == nil {
					for _, kv := range batch {
						if len(kv[0]) >= 5 && string(kv[0][:5]) == "nomt:" {
							rawKey := append([]byte(nil), kv[0][5:]...)
							ks := string(rawKey)
							if _, exists := staleNomtAccKeys[ks]; !exists {
								staleNomtAccKeys[ks] = rawKey
							}
							if !accSeenAbove[ks] {
								accSeenAbove[ks] = true
							}
						}
					}
				}
			}
			if needValidatorNomtStake && len(bkp.StakeState) > 0 {
				if batch, err := storage.DeserializeBatch(bkp.StakeState); err == nil {
					for _, kv := range batch {
						if len(kv[0]) >= 5 && string(kv[0][:5]) == "nomt:" {
							rawKey := append([]byte(nil), kv[0][5:]...)
							ks := string(rawKey)
							if _, exists := staleNomtStakeKeys[ks]; !exists {
								staleNomtStakeKeys[ks] = rawKey
							}
							if !stakeSeenAbove[ks] {
								stakeSeenAbove[ks] = true
							}
						}
					}
				}
			}
			if len(bkp.SmartContractBatch) > 0 {
				if batch, err := storage.DeserializeBatch(bkp.SmartContractBatch); err == nil {
					for _, kv := range batch {
						if len(kv[0]) > 0 {
							staleEventLogKeys[string(kv[0])] = append([]byte(nil), kv[0]...)
						}
					}
				}
			}
			for _, logs := range bkp.FullDbLogs {
				for addrHex := range logs {
					if normHex := storage.NormalizeAddressHexKey(addrHex); normHex != "" {
						staleFullDbLogAddrs[normHex] = struct{}{}
					}
				}
			}
		}

		unresolvedAccTotal := 0
		for ks := range staleNomtAccKeys {
			if !accHasResolved[ks] {
				unresolvedAccTotal++
			}
		}
		unresolvedStakeTotal := 0
		for ks := range staleNomtStakeKeys {
			if !stakeHasResolved[ks] {
				unresolvedStakeTotal++
			}
		}

		staleEventLogs := make(map[string][]byte, len(staleEventLogKeys))
		restoredEventLogs := staleEventLogs
		restoredFullDbLogs := make(map[string][]byte, len(staleFullDbLogAddrs))

		// Pass B: Legacy / unindexed fallback — resolve remaining keys from BackupDb <= targetBlock,
		// then from flat "nomt:" store if present.
		accResolvedInBackup := make(map[string]struct{}, unresolvedAccTotal)
		stakeResolvedInBackup := make(map[string]struct{}, unresolvedStakeTotal)

		if backupStorage != nil &&
			(unresolvedAccTotal > 0 || unresolvedStakeTotal > 0 || len(staleEventLogKeys) > 0 || len(staleFullDbLogAddrs) > 0) {
			for b := targetBlock; ; b-- {
				allResolved := len(accResolvedInBackup) == unresolvedAccTotal &&
					len(stakeResolvedInBackup) == unresolvedStakeTotal &&
					len(restoredEventLogs) == len(staleEventLogKeys) &&
					len(restoredFullDbLogs) == len(staleFullDbLogAddrs)
				if allResolved {
					break
				}
				if bkp, ok := loadBackupAt(b); ok {
					if len(accResolvedInBackup) < unresolvedAccTotal && len(bkp.AccountBatch) > 0 {
						if batch, err := storage.DeserializeBatch(bkp.AccountBatch); err == nil {
							inBlock := make(map[string][]byte)
							for _, kv := range batch {
								if len(kv[0]) >= 5 && string(kv[0][:5]) == "nomt:" {
									ks := string(kv[0][5:])
									if _, wanted := staleNomtAccKeys[ks]; wanted && !accHasResolved[ks] {
										inBlock[ks] = append([]byte(nil), kv[1]...)
									}
								}
							}
							for ks, v := range inBlock {
								if !accHasResolved[ks] {
									restoredNomtAcc[ks] = v
									accHasResolved[ks] = true
									accResolvedInBackup[ks] = struct{}{}
								}
							}
						}
					}
					if len(stakeResolvedInBackup) < unresolvedStakeTotal && len(bkp.StakeState) > 0 {
						if batch, err := storage.DeserializeBatch(bkp.StakeState); err == nil {
							inBlock := make(map[string][]byte)
							for _, kv := range batch {
								if len(kv[0]) >= 5 && string(kv[0][:5]) == "nomt:" {
									ks := string(kv[0][5:])
									if _, wanted := staleNomtStakeKeys[ks]; wanted && !stakeHasResolved[ks] {
										inBlock[ks] = append([]byte(nil), kv[1]...)
									}
								}
							}
							for ks, v := range inBlock {
								if !stakeHasResolved[ks] {
									restoredNomtStake[ks] = v
									stakeHasResolved[ks] = true
									stakeResolvedInBackup[ks] = struct{}{}
								}
							}
						}
					}
					if len(restoredEventLogs) < len(staleEventLogKeys) && len(bkp.SmartContractBatch) > 0 {
						if batch, err := storage.DeserializeBatch(bkp.SmartContractBatch); err == nil {
							inBlock := make(map[string][]byte)
							for _, kv := range batch {
								ks := string(kv[0])
								if _, wanted := staleEventLogKeys[ks]; wanted {
									inBlock[ks] = append([]byte(nil), kv[1]...)
								}
							}
							for ks, v := range inBlock {
								if _, seen := restoredEventLogs[ks]; !seen {
									restoredEventLogs[ks] = v
								}
							}
						}
					}
					if len(restoredFullDbLogs) < len(staleFullDbLogAddrs) && len(bkp.FullDbLogs) > 0 {
						inBlock := make(map[string][]byte)
						for _, logs := range bkp.FullDbLogs {
							for addrHex, logBytes := range logs {
								normHex := storage.NormalizeAddressHexKey(addrHex)
								if normHex == "" {
									continue
								}
								if _, wanted := staleFullDbLogAddrs[normHex]; wanted {
									inBlock[normHex] = append([]byte(nil), logBytes...)
								}
							}
						}
						for normHex, logBytes := range inBlock {
							if _, seen := restoredFullDbLogs[normHex]; !seen {
								restoredFullDbLogs[normHex] = logBytes
							}
						}
					}
				}
				if b == 0 {
					break
				}
			}
		}

		if rh.storageManager != nil {
			if accStore := rh.storageManager.GetStorageAccount(); accStore != nil && len(accResolvedInBackup) < unresolvedAccTotal {
				for ks, rawKey := range staleNomtAccKeys {
					if accHasResolved[ks] {
						continue
					}
					if val, gErr := accStore.Get(append([]byte("nomt:"), rawKey...)); gErr == nil && len(val) > 0 {
						restoredNomtAcc[ks] = append([]byte(nil), val...)
						accHasResolved[ks] = true
					}
				}
			}
			if stakeStore := rh.storageManager.GetStorageStake(); stakeStore != nil && len(stakeResolvedInBackup) < unresolvedStakeTotal {
				for ks, rawKey := range staleNomtStakeKeys {
					if stakeHasResolved[ks] {
						continue
					}
					if val, gErr := stakeStore.Get(append([]byte("nomt:"), rawKey...)); gErr == nil && len(val) > 0 {
						restoredNomtStake[ks] = append([]byte(nil), val...)
						stakeHasResolved[ks] = true
					}
				}
			}
		}

		// Pass C: Fail-closed NOMT rollback with pre-rollback snapshot undo and root verification.
		nomtRootEq := func(r, exp common.Hash) bool {
			return r == exp ||
				(exp == trie.EmptyRootHash && r == (common.Hash{})) ||
				(exp == (common.Hash{}) && r == trie.EmptyRootHash)
		}
		var (
			hasExpectedAccRoot   bool
			hasExpectedStakeRoot bool
			expAccRoot           common.Hash
			expStakeRoot         common.Hash
		)
		if targetBlk != nil && targetBlk.Header() != nil {
			hasExpectedAccRoot = true
			hasExpectedStakeRoot = true
			expAccRoot = targetBlk.Header().AccountStatesRoot()
			expStakeRoot = common.Hash(targetBlk.Header().StakeStatesRoot())
		} else if targetBlock == 0 && rh.chainState != nil {
			if cl := rh.chainState.GetChangelogDB(); cl != nil {
				hasExpectedAccRoot = true
				if rootBytes, _, found, rErr := cl.GetRootAtOrBefore(0); rErr == nil && found {
					expAccRoot = common.BytesToHash(rootBytes)
				}
			}
			if scl := rh.chainState.GetStakeChangelogDB(); scl != nil {
				hasExpectedStakeRoot = true
				if rootBytes, _, found, rErr := scl.GetRootAtOrBefore(0); rErr == nil && found {
					expStakeRoot = common.BytesToHash(rootBytes)
				}
			}
		}

		unresolvedAccRemaining := 0
		for ks := range staleNomtAccKeys {
			if !accHasResolved[ks] {
				unresolvedAccRemaining++
			}
		}
		unresolvedStakeRemaining := 0
		for ks := range staleNomtStakeKeys {
			if !stakeHasResolved[ks] {
				unresolvedStakeRemaining++
			}
		}
		if (unresolvedAccRemaining > 0 && !hasExpectedAccRoot) || (unresolvedStakeRemaining > 0 && !hasExpectedStakeRoot) {
			return fmt.Errorf("validator NOMT rollback to #%d aborted fail-closed: %d account and %d stake keys lack pre-image and target block header is missing",
				targetBlock, unresolvedAccRemaining, unresolvedStakeRemaining)
		}

		if rh.chainState != nil && trie.GetStateBackend() == trie.BackendNOMT && (needValidatorNomtAcc || needValidatorNomtStake) {
			var liveTries []trie.StateTrie
			if asDB := rh.chainState.GetAccountStateDB(); asDB != nil {
				liveTries = append(liveTries, asDB.Trie())
			}
			if stakeDB := rh.chainState.GetStakeStateDB(); stakeDB != nil {
				liveTries = append(liveTries, stakeDB.Trie())
			}
			for _, lt := range liveTries {
				if nomtTrie, ok := lt.(*trie.NomtStateTrie); ok && nomtTrie != nil {
					_ = nomtTrie.WaitCommitPayload()
					nomtTrie.AbortPending()
				}
			}
		}

		var nomtRollbackUndos []func()
		undoNomtRollbacks := func() {
			for i := len(nomtRollbackUndos) - 1; i >= 0; i-- {
				nomtRollbackUndos[i]()
			}
		}

		applyValidatedNomtRollback := func(namespace string, staleKeys map[string][]byte, resolved map[string][]byte, hasExpRoot bool, expRoot common.Hash) error {
			if len(staleKeys) == 0 {
				return nil
			}
			sortedKS := make([]string, 0, len(staleKeys))
			for ks := range staleKeys {
				sortedKS = append(sortedKS, ks)
			}
			slices.Sort(sortedKS)

			keys := make([][]byte, len(sortedKS))
			vals := make([][]byte, len(sortedKS))
			for idx, ks := range sortedKS {
				keys[idx] = staleKeys[ks]
				vals[idx] = resolved[ks] // nil only if newly inserted above targetBlock or unproven new key
			}

			prevVals, snapErr := trie.GetNomtKeyValues(namespace, keys)
			if snapErr != nil {
				return fmt.Errorf("GetNomtKeyValues(%s, %d) failed: %w", namespace, targetBlock, snapErr)
			}
			if rbErr := trie.RollbackNomtNamespaceKeys(namespace, keys, vals); rbErr != nil {
				return fmt.Errorf("RollbackNomtNamespaceKeys(%s, %d) failed: %w", namespace, targetBlock, rbErr)
			}
			nomtRollbackUndos = append(nomtRollbackUndos, func() {
				_ = trie.RollbackNomtNamespaceKeys(namespace, keys, prevVals)
			})

			if hasExpRoot {
				postRoot, ok := trie.GetNomtHandleRoot(namespace)
				if !ok || !nomtRootEq(postRoot, expRoot) {
					return fmt.Errorf("validator NOMT rollback root mismatch on %s at #%d: got=%s expected=%s",
						namespace, targetBlock, postRoot.Hex(), expRoot.Hex())
				}
			}
			return nil
		}

		if err := applyValidatedNomtRollback("account_state", staleNomtAccKeys, restoredNomtAcc, hasExpectedAccRoot, expAccRoot); err != nil {
			undoNomtRollbacks()
			logger.Error("🚨 [SYNC-FORK-ROLLBACK] %v — restored NOMT to pre-rollback state (fail-closed)", err)
			return err
		}
		if err := applyValidatedNomtRollback("stake_db", staleNomtStakeKeys, restoredNomtStake, hasExpectedStakeRoot, expStakeRoot); err != nil {
			undoNomtRollbacks()
			logger.Error("🚨 [SYNC-FORK-ROLLBACK] %v — restored NOMT to pre-rollback state (fail-closed)", err)
			return err
		}

		// Align ChainState BEFORE rolling back Xapian so a NOMT failure aborts fail-closed without leaving Xapian behind.
		if targetBlk != nil && rh.chainState != nil {
			if alignErr := rh.chainState.UpdateStateForNewHeader(targetBlk.Header()); alignErr != nil {
				undoNomtRollbacks()
				logger.Error("🚨 [SYNC-FORK-ROLLBACK] UpdateStateForNewHeader(#%d) failed during rollback: %v", targetBlock, alignErr)
				return fmt.Errorf("UpdateStateForNewHeader(%d) failed during rollback: %w", targetBlock, alignErr)
			}
		} else if targetBlock == 0 && targetBlk == nil && rh.chainState != nil && trie.GetStateBackend() == trie.BackendNOMT {
			if asDB := rh.chainState.GetAccountStateDB(); asDB != nil {
				if nomtTrie, ok := asDB.Trie().(*trie.NomtStateTrie); ok {
					if rh.chainState.GetChangelogDB() != nil {
						if alignErr := nomtTrie.AlignWithExpectedRoot(asDB.Storage(), expAccRoot, 0); alignErr != nil {
							undoNomtRollbacks()
							return fmt.Errorf("AlignWithExpectedRoot(account_state, 0) failed during rollback: %w", alignErr)
						}
						originAcc := expAccRoot
						if originAcc == (common.Hash{}) {
							originAcc = trie.EmptyRootHash
						}
						asDB.SetOriginRootHash(originAcc)
					} else {
						postAccRoot, _ := trie.GetNomtHandleRoot("account_state")
						nomtTrie.RealignRoot(postAccRoot)
						originAcc := postAccRoot
						if originAcc == (common.Hash{}) {
							originAcc = trie.EmptyRootHash
						}
						asDB.SetOriginRootHash(originAcc)
					}
				}
			}
			if stakeDB := rh.chainState.GetStakeStateDB(); stakeDB != nil {
				if nomtTrie, ok := stakeDB.Trie().(*trie.NomtStateTrie); ok {
					if rh.chainState.GetStakeChangelogDB() != nil {
						if alignErr := nomtTrie.AlignWithExpectedRoot(stakeDB.GetStorage(), expStakeRoot, 0); alignErr != nil {
							undoNomtRollbacks()
							return fmt.Errorf("AlignWithExpectedRoot(stake_db, 0) failed during rollback: %w", alignErr)
						}
						originStake := expStakeRoot
						if originStake == (common.Hash{}) {
							originStake = trie.EmptyRootHash
						}
						stakeDB.SetOriginRootHash(originStake)
					} else {
						postStakeRoot, _ := trie.GetNomtHandleRoot("stake_db")
						nomtTrie.RealignRoot(postStakeRoot)
						originStake := postStakeRoot
						if originStake == (common.Hash{}) {
							originStake = trie.EmptyRootHash
						}
						stakeDB.SetOriginRootHash(originStake)
					}
				}
			}
			if scDB := rh.chainState.GetSmartContractDB(); scDB != nil {
				if scCl := scDB.GetChangelogDB(); scCl != nil && scCl.GetHighestBlock() > 0 {
					expectedScRoot, _, foundSc, scErr := scCl.GetRootAtOrBefore(0)
					if scErr != nil || !foundSc {
						undoNomtRollbacks()
						return fmt.Errorf("AlignWithExpectedRoot(smart_contract_storage, 0) missing recorded root: %v", scErr)
					}
					scHandle, hErr := trie.GetOrInitNomtHandle(trie.SharedContractStorageNamespace)
					if hErr != nil {
						undoNomtRollbacks()
						return fmt.Errorf("AlignWithExpectedRoot(smart_contract_storage, 0): %w", hErr)
					}
					scTrie := trie.NewNomtStateTrie(scHandle, true, trie.SharedContractStorageNamespace)
					scTrie.SetChangelogDB(scCl)
					if alignErr := scTrie.AlignWithExpectedRoot(nil, common.BytesToHash(expectedScRoot), 0); alignErr != nil {
						undoNomtRollbacks()
						return fmt.Errorf("AlignWithExpectedRoot(smart_contract_storage, 0) failed during rollback: %w", alignErr)
					}
				}
			}
			rh.chainState.InvalidateAllState()
		}

		if _, rbErr := mvm.RollbackXapianAboveBlock(targetBlock); rbErr != nil {
			logger.Error("🚨 [SYNC-FORK-ROLLBACK] RollbackXapianAboveBlock(%d) failed: %v — ABORTING sync to prevent Xapian/ChainDB divergence!", targetBlock, rbErr)
			return fmt.Errorf("RollbackXapianAboveBlock(%d) failed: %w", targetBlock, rbErr)
		}

		keepTxs := make(map[common.Hash]struct{})
		if targetBlk != nil {
			for _, txH := range targetBlk.Transactions() {
				keepTxs[txH] = struct{}{}
			}
		}

		for bn := maxTip; bn > targetBlock; bn-- {
			if staleHash, ok := bc.GetBlockHashByNumber(bn); ok && staleHash != (common.Hash{}) {
				if staleBlk, err := blockDatabase.GetBlockByHash(staleHash); err == nil && staleBlk != nil {
					staleTxs := staleBlk.Transactions()
					for _, txHash := range staleTxs {
						if _, keep := keepTxs[txHash]; keep {
							continue
						}
						existingBn, foundMap := bc.GetBlockNumberByTxHash(txHash)
						if !foundMap || existingBn > targetBlock {
							if rh.storageManager != nil {
								if mapStore := rh.storageManager.GetStorageMapping(); mapStore != nil {
									_ = mapStore.Delete([]byte("txHashPrefix" + txHash.Hex()))
								}
								if txStore := rh.storageManager.GetStorageTransaction(); txStore != nil {
									_ = txStore.Delete(txHash.Bytes())
								}
								if rcptStore := rh.storageManager.GetStorageReceipt(); rcptStore != nil {
									_ = rcptStore.Delete(txHash.Bytes())
								}
							}
							bc.DiscardTxMappings([]common.Hash{txHash})
						}
					}
				}
				bc.DeleteBlockFromCache(staleHash)
				if staleHash != incomingHashToKeep && staleHash != targetHash {
					_ = blockDatabase.DeleteBlockByHash(staleHash)
				}
			}
			bc.DiscardBlockMappings(bn)
			_ = bc.DeleteBlockHashMapping(bn)
			if backupStorage != nil {
				_ = backupStorage.Delete([]byte(fmt.Sprintf("block_data_topic-%d", bn)))
				_ = backupStorage.Delete([]byte(fmt.Sprintf("backup_%d", bn)))
				storage.DeleteNomtUndoForBlock(backupStorage, bn)
			}
		}

		// Clean up or restore stale EventLog entries from abandoned fork blocks.
		if len(staleEventLogKeys) > 0 && rh.storageManager != nil {
			if scStore := rh.storageManager.GetStorageSmartContract(); scStore != nil {
				for ks, rawKey := range staleEventLogKeys {
					if prevVal, existsAtTarget := restoredEventLogs[ks]; existsAtTarget {
						_ = scStore.Put(rawKey, prevVal)
					} else {
						_ = scStore.Delete(rawKey)
					}
				}
			}
		}

		// Clean up or restore latest FullDbLogs pointers touched in abandoned fork blocks.
		if len(staleFullDbLogAddrs) > 0 && backupStorage != nil {
			for normHex := range staleFullDbLogAddrs {
				latestKey := storage.FullDbLogsLatestKey(normHex)
				if prevLog, existsAtTarget := restoredFullDbLogs[normHex]; existsAtTarget {
					_ = backupStorage.Put(latestKey, prevLog)
				} else {
					_ = backupStorage.Delete(latestKey)
				}
			}
		}

		_ = bc.Commit()

		if targetBlk != nil {
			_ = blockDatabase.SaveLastBlockSync(targetBlk)
			if rh.updateLastBlockCallback != nil {
				rh.updateLastBlockCallback(targetBlk)
			}
		} else if targetBlock == 0 {
			_ = blockDatabase.DeleteLastBlockKey()
		}

		if rh.chainState != nil {
			// On NOMT backend, sync flat "nomt:" mirror keys in StorageAccount/StorageStake back to targetBlock.
			// Use ChangelogDB when present, or the BackupDb pre-images recovered above on validator nodes.
			if trie.GetStateBackend() == trie.BackendNOMT && rh.storageManager != nil {
				syncNomtFlatMirror := func(cl *state_changelog.StateChangelogDB, flatStore storage.Storage, staleKeys map[string][]byte, restored map[string][]byte) {
					if flatStore == nil {
						return
					}
					if cl != nil {
						if cl.GetHighestBlock() <= targetBlock {
							return
						}
						addrs, err := cl.GetAllUniqueAddresses()
						if err != nil {
							return
						}
						for _, addr := range addrs {
							nomtKey := append([]byte("nomt:"), addr...)
							valAtTarget, errAt := cl.GetStateAt(addr, targetBlock)
							if errAt == nil && len(valAtTarget) > 0 {
								if _, getErr := flatStore.Get(nomtKey); getErr == nil {
									_ = flatStore.Put(nomtKey, valAtTarget)
								}
							} else {
								_ = flatStore.Delete(nomtKey)
							}
						}
						return
					}
					for ks, rawKey := range staleKeys {
						nomtKey := append([]byte("nomt:"), rawKey...)
						if valAtTarget, ok := restored[ks]; ok && len(valAtTarget) > 0 {
							if _, getErr := flatStore.Get(nomtKey); getErr == nil {
								_ = flatStore.Put(nomtKey, valAtTarget)
							}
						} else {
							_ = flatStore.Delete(nomtKey)
						}
					}
				}
				syncNomtFlatMirror(rh.chainState.GetChangelogDB(), rh.storageManager.GetStorageAccount(), staleNomtAccKeys, restoredNomtAcc)
				syncNomtFlatMirror(rh.chainState.GetStakeChangelogDB(), rh.storageManager.GetStorageStake(), staleNomtStakeKeys, restoredNomtStake)
			}

			var scCl *state_changelog.StateChangelogDB
			if scDB := rh.chainState.GetSmartContractDB(); scDB != nil {
				scCl = scDB.GetChangelogDB()
			}
			for _, cl := range []*state_changelog.StateChangelogDB{
				rh.chainState.GetChangelogDB(),
				rh.chainState.GetStakeChangelogDB(),
				scCl,
			} {
				if cl != nil {
					if err := cl.RollbackAboveBlock(targetBlock); err != nil {
						logger.Warn("⚠️ [SYNC-FORK-ROLLBACK] StateChangelogDB.RollbackAboveBlock(%d) warning: %v", targetBlock, err)
					}
				}
			}
		}

		storage.ResetAllBlockCounters(targetBlock)
		if trie.GetStateBackend() == trie.BackendNOMT {
			storage.ForceSetLastNomtCommittedBlock(targetBlock)
		}
		targetGEI := targetBlock
		if targetBlk != nil && targetBlk.Header() != nil && (targetBlock == 0 || targetBlk.Header().GlobalExecIndex() > 0) {
			targetGEI = targetBlk.Header().GlobalExecIndex()
		}
		storage.ForceSetLastGlobalExecIndex(targetGEI)
		if rh.clearNoncesCacheCallback != nil {
			rh.clearNoncesCacheCallback()
		}
		return nil
	}

	for i, blockData := range blocks {
		isLastBlock := i == len(blocks)-1
		shouldCommitState := isLastBlock || (i > 0 && i%100 == 0)

		rawBytes := blockData.GetRawBlockBytes()
		backupBytes := blockData.GetBackupData()

		if len(rawBytes) == 0 {
			logger.Warn("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Block (wire_num=%d) has no raw_block_bytes, skipping", blockData.GetBlockNumber())
			continue
		}

		// Unmarshal the raw block
		blk := &block.Block{}
		if err := blk.Unmarshal(rawBytes); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to unmarshal block (wire_num=%d): %v", blockData.GetBlockNumber(), err)
			return abortSyncBatch(fmt.Sprintf("unmarshal block %d failed: %v", blockData.GetBlockNumber(), err))
		}

		header := blk.Header()
		blockHash := header.Hash()
		blockNum := header.BlockNumber()
		blockGEI := header.GlobalExecIndex()

		// 🔍 DIAGNOSTIC: Log TX order from SYNC (for SyncOnly node)
		txs := blk.Transactions()
		if len(txs) > 0 {
			logger.Info("📥 [SYNC-TX-ORDER] Block #%d (GEI=%d), txs=%d", blockNum, blockGEI, len(txs))
			for i, tx := range txs {
				if i < 5 || i == len(txs)-1 { // Log 5 first and 1 last tx to avoid spam
					logger.Info("  |_ [SYNC-TX-ORDER] pos[%d/%d]: hash=%s",
						i, len(txs)-1, tx.Hex()[:18]+"...")
				}
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// DEDUPLICATION: Skip blocks already executed (GEI-based)
		// NOTE: When a fork hash mismatch is detected, we ONLY mark forkOverwrite=true
		// here and defer destructive rollback until AFTER parent-hash, excessBlobGas,
		// and BackupDb validation pass, so a corrupt/garbage peer block cannot clobber
		// a healthy local block!
		// ═══════════════════════════════════════════════════════════════════════════
		currentGEI := storage.GetLastGlobalExecIndex()
		isFullyExecuted := false
		forkOverwrite := false

		if blockGEI > 0 && blockGEI <= currentGEI {
			if localHash, ok := bc.GetBlockHashByNumber(blockNum); ok {
				if localHash == blockHash {
					isFullyExecuted = true
				} else {
					if isPreConsensusSync {
						logger.Warn("⚠️ [STARTUP-SYNC-FORK] Local block #%d hash (%s) != leader hash (%s) during startup sync. "+
							"FORCING execution/overwrite of this block after validation to resolve local fork.",
							blockNum, localHash.Hex()[:18], blockHash.Hex()[:18])
					} else {
						logger.Warn("⚠️ [SYNC-HASH-MISMATCH] Local block #%d hash (%x) != leader hash (%x) during sync. "+
							"FORCING execution/overwrite of this block after validation to resolve local fork.",
							blockNum, localHash[:8], blockHash[:8])
					}
					forkOverwrite = true
					isFullyExecuted = false
				}
			}
		}

		// STRICT BLOCK NUMBER GUARD: If the block number is already committed and there is no hash mismatch,
		// it is fully executed. This prevents executing state batches for duplicate blocks.
		if !isFullyExecuted && !forkOverwrite && blockNum > 0 {
			lastBlockNum := storage.GetLastBlockNumber()
			if blockNum <= lastBlockNum {
				if localHash, ok := bc.GetBlockHashByNumber(blockNum); ok {
					if localHash == blockHash {
						isFullyExecuted = true
						logger.Info("🔄 [SYNC-DEDUPLICATE] Block #%d matches committed hash (tip=#%d). Skipping execution.", blockNum, lastBlockNum)
					} else {
						logger.Warn("⚠️ [SYNC-HASH-MISMATCH] Local block #%d hash (%x) != leader hash (%x) below/at tip #%d. "+
							"FORCING execution/overwrite of this block after validation to resolve local fork.",
							blockNum, localHash[:8], blockHash[:8], lastBlockNum)
						forkOverwrite = true
						isFullyExecuted = false
					}
				} else if blockNum < lastBlockNum {
					isFullyExecuted = true
					logger.Info("🔄 [SYNC-DEDUPLICATE] Block #%d is strictly older than last committed #%d (no hash entry). Skipping execution.", blockNum, lastBlockNum)
				}
			}
		}

		// Anti-drift check (MUST run AFTER both GEI deduplication and STRICT BLOCK NUMBER GUARD):
		// Even if the block was found in BlockDB/mappings, if the state backend is NOMT and
		// blockNum > LastNomtCommittedBlock, force re-execution on NOMT unless NOMT handle roots
		// already match this header and no BackupData was supplied.
		if isFullyExecuted && !forkOverwrite && trie.GetStateBackend() == trie.BackendNOMT && blockNum > 0 && blockNum > storage.GetLastNomtCommittedBlock() {
			nomtEq := func(r, exp common.Hash) bool {
				return r == exp || (exp == trie.EmptyRootHash && r == (common.Hash{})) || (exp == (common.Hash{}) && r == trie.EmptyRootHash)
			}
			accR, accOK := trie.GetNomtHandleRoot("account_state")
			stakeR, stakeOK := trie.GetNomtHandleRoot("stake_db")
			if len(backupBytes) == 0 && accOK && stakeOK &&
				nomtEq(accR, header.AccountStatesRoot()) &&
				nomtEq(stakeR, common.Hash(header.StakeStatesRoot())) {
				storage.UpdateLastNomtCommittedBlock(blockNum)
			} else {
				isFullyExecuted = false
				logger.Info("🔄 [NOMT-SYNC-RECOVERY] Block #%d exists in LevelDB (tip=#%d) but is NOT committed to NOMT (lastNomt=%d). Forcing re-execution on NOMT.",
					blockNum, storage.GetLastBlockNumber(), storage.GetLastNomtCommittedBlock())
			}
		}

		if isFullyExecuted {
			logger.Debug("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Block #%d (GEI=%d) already executed (current_gei=%d), skipping",
				blockNum, blockGEI, currentGEI)
			executedCount++
			if blockNum >= lastExecutedBlock {
				lastExecutedBlock = blockNum
				lastExecutedBlk = blk
			}
			if blockGEI > lastExecutedGEI {
				lastExecutedGEI = blockGEI
			}

			// Ensure block number to hash mapping exists in mapping DB
			if _, ok := bc.GetBlockHashByNumber(blockNum); !ok {
				if err := bc.SetBlockNumberToHash(blockNum, blockHash); err != nil {
					logger.Error("❌ [SNAPSHOT-RESUME] Failed to restore block mapping for skip block #%d: %v", blockNum, err)
				} else {
					logger.Info("🔄 [SNAPSHOT-RESUME] Restored block mapping for skip block #%d -> %s", blockNum, blockHash.Hex())
				}
			}

			// Ensure raw block bytes exist in PebbleDB block storage
			if _, err := blockDatabase.GetBlockByHash(blockHash); err != nil {
				logger.Warn("⚠️ [SYNC-INTEGRITY] Block #%d (hash=%s) has mapping but raw bytes are missing from DB. Force-restoring block data.", blockNum, blockHash.Hex())
				if saveErr := blockDatabase.SaveBlockByHash(blk); saveErr != nil {
					logger.Error("❌ [SYNC-INTEGRITY] Failed to save missing block #%d: %v", blockNum, saveErr)
				} else if flushErr := blockDatabase.GetDB().Flush(); flushErr != nil {
					logger.Error("❌ [SYNC-INTEGRITY] Failed to flush missing block #%d: %v", blockNum, flushErr)
				} else {
					logger.Info("🔄 [SYNC-INTEGRITY] Successfully restored and flushed missing block #%d bytes", blockNum)
				}
			}

			storage.UpdateLastBlockNumber(blockNum)
			if blockGEI > 0 {
				storage.UpdateLastGlobalExecIndex(blockGEI)
			}

			if shouldImportPeerCommitIndex(request.PreserveOwnCommitIndex, header.CommitIndex()) {
				commitIdx32 := uint32(header.CommitIndex())
				currentEpoch := header.Epoch()
				lastEpoch := storage.GetLastHandledCommitEpoch()

				if currentEpoch > lastEpoch || (currentEpoch == lastEpoch && commitIdx32 > storage.GetLastHandledCommitIndex()) {
					storage.ForceSetLastHandledCommitIndex(commitIdx32)
					storage.UpdateLastHandledCommitEpoch(currentEpoch)
					logger.Info("🔄 [STARTUP-SYNC] Restored lastHandledCommitIndex to %d (epoch %d) from fully-executed block #%d", commitIdx32, currentEpoch, blockNum)
				}
			}

			// Still persist backup data for Sub nodes
			if len(backupBytes) > 0 {
				rh.persistBackupForSub(backupBytes, blockNum)
			}

			continue
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// PARENT-HASH & BLOB-GAS CONSISTENCY CHECKS
		// ═══════════════════════════════════════════════════════════════════════════
		if blockNum > 1 {
			prevBlockNum := blockNum - 1
			if prevHash, ok := bc.GetBlockHashByNumber(prevBlockNum); ok && prevHash != (common.Hash{}) {
				expectedParentHash := prevHash
				actualParentHash := header.LastBlockHash()
				if actualParentHash != expectedParentHash {
					mismatchMsg := fmt.Sprintf("parent-hash mismatch at block %d: parentHash=%s but local block %d hash=%s",
						blockNum, actualParentHash.Hex(), prevBlockNum, expectedParentHash.Hex())
					if executedCount > 0 {
						logger.Error("🚨 [SYNC-FORK-GUARD] %s after committing %d block(s) in current batch — finalizing synced prefix up to #%d and aborting batch.",
							mismatchMsg, executedCount, lastExecutedBlock)
						return abortSyncBatch(mismatchMsg)
					}

					logger.Error("🚨 [SYNC-FORK-GUARD] parent-hash mismatch at block #%d! "+
						"Synced block has parentHash=%s, but local block #%d hash is %s. "+
						"Rolling back local chain + Xapian to %d to force resync of the mismatched block.",
						blockNum, actualParentHash.Hex(), prevBlockNum, expectedParentHash.Hex(), prevBlockNum-1)

					if rbErr := rollbackLocalChainToBlock(prevBlockNum-1, common.Hash{}); rbErr != nil {
						return &pb.SyncBlocksResponse{
							SyncedCount:     0,
							LastSyncedBlock: 0,
							Error:           fmt.Sprintf("parent-hash mismatch at block %d and rollback to %d failed: %v", blockNum, prevBlockNum-1, rbErr),
						}, nil
					}

					return &pb.SyncBlocksResponse{
						SyncedCount:     0,
						LastSyncedBlock: 0,
						Error:           mismatchMsg,
					}, nil
				}

				if parentBlock := bc.GetBlockByNumber(prevBlockNum); parentBlock != nil {
					expectedExcessBlobGas := block.NextExcessBlobGas(parentBlock.Header(), header.TimeStamp())
					if header.ExcessBlobGas() != expectedExcessBlobGas {
						mismatchMsg := fmt.Sprintf("excessBlobGas mismatch at block %d: got=%d want=%d",
							blockNum, header.ExcessBlobGas(), expectedExcessBlobGas)
						if executedCount > 0 {
							logger.Error("🚨 [SYNC-FORK-GUARD] %s after committing %d block(s) in current batch — finalizing synced prefix up to #%d and aborting batch.",
								mismatchMsg, executedCount, lastExecutedBlock)
							return abortSyncBatch(mismatchMsg)
						}

						logger.Error("🚨 [SYNC-FORK-GUARD] ExcessBlobGas mismatch at block #%d! "+
							"Synced block has ExcessBlobGas=%d, but expected %d from parent. "+
							"Rolling back local chain + Xapian to %d to force resync of the mismatched block.",
							blockNum, header.ExcessBlobGas(), expectedExcessBlobGas, prevBlockNum-1)

						if rbErr := rollbackLocalChainToBlock(prevBlockNum-1, common.Hash{}); rbErr != nil {
							return &pb.SyncBlocksResponse{
								SyncedCount:     0,
								LastSyncedBlock: 0,
								Error:           fmt.Sprintf("excessBlobGas mismatch at block %d and rollback to %d failed: %v", blockNum, prevBlockNum-1, rbErr),
							}, nil
						}

						return &pb.SyncBlocksResponse{
							SyncedCount:     0,
							LastSyncedBlock: 0,
							Error:           mismatchMsg,
						}, nil
					}
				}
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// PRE-VALIDATE BACKUP DATA IN MEMORY BEFORE ANY FORK ROLLBACK OR DB WRITES
		// ═══════════════════════════════════════════════════════════════════════════
		var parsedBackupDb *storage.BackUpDb
		var preParsedBatches *parsedBackupDbBatches

		if len(backupBytes) > 0 {
			logger.Debug("🔍 [NOMT-DEBUG] Block #%d: raw backupBytes=%d bytes. About to deserialize+validate.",
				blockNum, len(backupBytes))

			backupDb, deserErr := storage.DeserializeBackupDb(backupBytes)
			if deserErr != nil {
				logger.Error("🚨 [SYNC-FATAL] Failed to deserialize BackUpDb for block #%d: %v — ABORTING to prevent State Drift!", blockNum, deserErr)
				return abortSyncBatch(fmt.Sprintf("deserialize BackupDb failed for block %d: %v", blockNum, deserErr))
			}
			if len(txs) > 0 && !hasAnyBackupDbPayload(&backupDb) {
				logger.Error("🚨 [SYNC-FATAL] Empty BackupDb state payload for non-empty block #%d (%d txs) — ABORTING to prevent State Drift!", blockNum, len(txs))
				return abortSyncBatch(fmt.Sprintf("empty BackupDb state payload for non-empty block %d (%d txs)", blockNum, len(txs)))
			}
			validated, valErr := rh.parseAndValidateBackupDb(&backupDb)
			if valErr != nil {
				logger.Error("🚨 [SYNC-FATAL] Invalid BackupDb sub-batches for block #%d: %v — ABORTING to prevent State Drift!", blockNum, valErr)
				return abortSyncBatch(fmt.Sprintf("invalid BackupDb batches for block %d: %v", blockNum, valErr))
			}
			parsedBackupDb = &backupDb
			preParsedBatches = validated
		} else if len(txs) > 0 {
			logger.Error("🚨 [SYNC-FATAL] Missing BackupData for non-empty block #%d (%d txs) — ABORTING to prevent State Drift!", blockNum, len(txs))
			return abortSyncBatch(fmt.Sprintf("missing BackupData for non-empty block %d (%d txs)", blockNum, len(txs)))
		}

		// Execute deferred local fork rollback ONLY AFTER header continuity and BackupDb validation passed!
		if forkOverwrite && blockNum > 0 {
			if rbErr := rollbackLocalChainToBlock(blockNum-1, blockHash); rbErr != nil {
				return abortSyncBatch(fmt.Sprintf("fork rollback before block %d failed: %v", blockNum, rbErr))
			}
			currentGEI = storage.GetLastGlobalExecIndex()
		}
		if blockGEI > currentGEI {
			currentGEI = blockGEI
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 1: Apply BackupDb state batches with full reversible undo log
		// ═══════════════════════════════════════════════════════════════════════════
		var pendingBlockSessions []trie.NomtSessionToFlush
		var blockNomtPreImages map[string][][2][]byte
		rollbackBlockMutations := func() {}

		var expectedPreAccRoot, expectedPreStakeRoot common.Hash
		var hasExpectedPreRoots bool
		var preAccHandleRoot, preStakeHandleRoot common.Hash
		var prevBlockForAlign types.Block
		nomtMatchesExpected := func(handleRoot, expected common.Hash) bool {
			return handleRoot == expected ||
				(expected == trie.EmptyRootHash && handleRoot == (common.Hash{})) ||
				(expected == (common.Hash{}) && handleRoot == trie.EmptyRootHash)
		}
		readNomtRoot := func(ns string) common.Hash {
			if r, ok := trie.GetNomtHandleRoot(ns); ok {
				return r
			}
			if h, err := trie.GetOrInitNomtHandle(ns); err == nil && h != nil {
				if rb, rErr := h.Root(); rErr == nil {
					return common.BytesToHash(rb[:])
				}
			}
			return common.Hash{}
		}
		if trie.GetStateBackend() == trie.BackendNOMT {
			preAccHandleRoot = readNomtRoot("account_state")
			preStakeHandleRoot = readNomtRoot("stake_db")
			prevNum := uint64(0)
			if blockNum > 1 {
				prevNum = blockNum - 1
			}
			if prevHash, ok := bc.GetBlockHashByNumber(prevNum); ok && prevHash != (common.Hash{}) {
				if prevBlock, err := blockDatabase.GetBlockByHash(prevHash); err == nil && prevBlock != nil {
					prevBlockForAlign = prevBlock
					expectedPreAccRoot = prevBlock.Header().AccountStatesRoot()
					expectedPreStakeRoot = common.Hash(prevBlock.Header().StakeStatesRoot())
					hasExpectedPreRoots = true
				}
			} else if blockNum <= 1 && rh.chainState != nil {
				if curHdrPtr := rh.chainState.GetcurrentBlockHeader(); curHdrPtr != nil && *curHdrPtr != nil {
					curHdr := *curHdrPtr
					if curHdr.BlockNumber() == 0 {
						expectedPreAccRoot = curHdr.AccountStatesRoot()
						expectedPreStakeRoot = common.Hash(curHdr.StakeStatesRoot())
						hasExpectedPreRoots = true
					}
				}
			}

			if hasExpectedPreRoots &&
				(!nomtMatchesExpected(preAccHandleRoot, expectedPreAccRoot) ||
					!nomtMatchesExpected(preStakeHandleRoot, expectedPreStakeRoot)) {
				logger.Error("🚨 [NOMT-SYNC-VERIFY] CRITICAL: PRE-execution state root MISMATCH before block #%d! "+
					"acc=%s (expected %s), stake=%s (expected %s). Attempting self-heal before applying block...",
					blockNum, preAccHandleRoot.Hex(), expectedPreAccRoot.Hex(), preStakeHandleRoot.Hex(), expectedPreStakeRoot.Hex())
				if prevBlockForAlign != nil && rh.chainState != nil {
					if alignErr := rh.chainState.UpdateStateForNewHeader(prevBlockForAlign.Header()); alignErr != nil {
						return abortSyncBatch(fmt.Sprintf("pre-execution NOMT state alignment failed before block %d: %v", blockNum, alignErr))
					}
					preAccHandleRoot = readNomtRoot("account_state")
					preStakeHandleRoot = readNomtRoot("stake_db")
				}
			}
		}

		if parsedBackupDb != nil && preParsedBatches != nil {
			logger.Debug("🔍 [NOMT-DEBUG] Block #%d deserialized: AccountBatch=%d bytes, TrieDB=%d bytes, FullDbLogs=%d entries",
				blockNum,
				len(parsedBackupDb.AccountBatch),
				len(parsedBackupDb.TrieDatabaseBatchPut),
				len(parsedBackupDb.FullDbLogs),
			)

			if isPreConsensusSync && trie.GetStateBackend() == trie.BackendNOMT && i == 0 && hasExpectedPreRoots {
				if expectedPreAccRoot != (common.Hash{}) && expectedPreAccRoot != trie.EmptyRootHash {
					if !nomtMatchesExpected(preAccHandleRoot, expectedPreAccRoot) {
						logger.Error("🚨 [NOMT-SYNC-VERIFY] CRITICAL: PRE-execution state root MISMATCH! "+
							"localRoot=%s, expectedPreRoot=%s, block=#%d. "+
							"This node's state has diverged BEFORE this block was applied!",
							preAccHandleRoot.Hex(), expectedPreAccRoot.Hex(), blockNum)
					} else {
						logger.Debug("✅ [NOMT-SYNC-VERIFY] PRE-execution root matches: %s", preAccHandleRoot.Hex()[:18]+"...")
					}
				}
			}

			sessions, preImages, undoFn, applyErr := rh.applyParsedBackupDbBatches(parsedBackupDb, preParsedBatches, blockHash, blockNum, txs)
			if applyErr != nil {
				logger.Error("🚨 [SYNC-FATAL] Failed to apply backup batches for block #%d: %v — ABORTING to prevent State Drift!", blockNum, applyErr)
				return abortSyncBatch(fmt.Sprintf("apply backup batches failed for block %d: %v", blockNum, applyErr))
			}
			pendingBlockSessions = sessions
			blockNomtPreImages = preImages
			rollbackBlockMutations = undoFn

			if isPreConsensusSync && trie.GetStateBackend() == trie.BackendNOMT && i == 0 {
				if nomtRoot, ok := trie.GetNomtHandleRoot("account_state"); ok {
					expectedAccountRoot := header.AccountStatesRoot()
					logger.Debug("[FORK-DIAG][NOMT-PER-BLOCK] Block #%d (GEI=%d): beforeApply=%s → afterApply=%s, headerExpected=%s",
						blockNum, blockGEI,
						preAccHandleRoot.Hex()[:18]+"...",
						nomtRoot.Hex()[:18]+"...",
						expectedAccountRoot.Hex()[:18]+"...")
				}
			}
		} else if blockNum > 0 {
			if _, rbErr := mvm.RollbackXapianAboveBlock(blockNum - 1); rbErr != nil {
				logger.Error("🚨 [FORK-RISK] RollbackXapianAboveBlock(%d) failed while syncing empty block #%d without BackupData: %v",
					blockNum-1, blockNum, rbErr)
				return abortSyncBatch(fmt.Sprintf("RollbackXapianAboveBlock(%d) failed for block %d: %v", blockNum-1, blockNum, rbErr))
			}
		}

		if blockNum == 0 {
			logger.Debug("🚀 [SNAPSHOT-RESUME] Skipping empty block (GEI=%d), state already advanced", blockGEI)
			continue
		}

		// On NOMT backend, CommitBlockState skips WithRebuildTries(). Unconditionally verify that
		// post-apply NOMT handle roots match the incoming block header BEFORE calling CommitBlockState
		// so any missing, corrupt, or drifted NOMT state rolls back cleanly without persisting the block.
		if trie.GetStateBackend() == trie.BackendNOMT {
			expectedAcc := header.AccountStatesRoot()
			postAccRoot := readNomtRoot("account_state")
			if !nomtMatchesExpected(postAccRoot, expectedAcc) {
				logger.Error("🚨 [NOMT-SYNC-VERIFY] Post-apply NOMT account_state root mismatch for block #%d: got=%s expected=%s — rolling back all block mutations!",
					blockNum, postAccRoot.Hex(), expectedAcc.Hex())
				rollbackBlockMutations()
				return abortSyncBatch(fmt.Sprintf("NOMT accountStatesRoot mismatch at block %d: got=%s expected=%s",
					blockNum, postAccRoot.Hex()[:18], expectedAcc.Hex()[:18]))
			}

			expectedStake := common.Hash(header.StakeStatesRoot())
			postStakeRoot := readNomtRoot("stake_db")
			if !nomtMatchesExpected(postStakeRoot, expectedStake) {
				logger.Error("🚨 [NOMT-SYNC-VERIFY] Post-apply NOMT stake_db root mismatch for block #%d: got=%s expected=%s — rolling back all block mutations!",
					blockNum, postStakeRoot.Hex(), expectedStake.Hex())
				rollbackBlockMutations()
				return abortSyncBatch(fmt.Sprintf("NOMT stakeStatesRoot mismatch at block %d: got=%s expected=%s",
					blockNum, postStakeRoot.Hex()[:18], expectedStake.Hex()[:18]))
			}
		}

		// Write-ahead validator NOMT undo BEFORE CommitBlockState (same ordering as commitWorker): a block
		// whose undo is not durable must not be committed, or a later fork rollback across it fails closed.
		// On CommitBlockState failure below the record is kept on purpose: it is harmless (pre-images equal
		// the blockNum-1 values) and the next execution of this height overwrites it.
		if rh.storageManager != nil && trie.GetStateBackend() == trie.BackendNOMT {
			if backupStorage := rh.storageManager.GetStorageBackupDb(); backupStorage != nil {
				writeAccUndo := rh.chainState == nil || rh.chainState.GetChangelogDB() == nil
				writeStakeUndo := rh.chainState == nil || rh.chainState.GetStakeChangelogDB() == nil
				if uErr := storage.PersistNomtUndoForBlock(backupStorage, blockNum,
					writeAccUndo, blockNomtPreImages["account_state"],
					writeStakeUndo, blockNomtPreImages["stake_db"]); uErr != nil {
					logger.Error("🚨 [SYNC-FATAL] [EXECUTE SYNC] NOMT undo durability failed for block #%d: %v — rolling back block mutations and aborting sync batch!", blockNum, uErr)
					errMsg := fmt.Sprintf("NOMT undo durability failed for block %d: %v", blockNum, uErr)
					rollbackBlockMutations()
					if _, rbErr := mvm.RollbackXapianAboveBlock(blockNum - 1); rbErr != nil {
						logger.Error("🚨 [FORK-RISK] RollbackXapianAboveBlock(%d) also FAILED after NOMT undo error at block #%d: %v", blockNum-1, blockNum, rbErr)
						errMsg = fmt.Sprintf("%s (RollbackXapianAboveBlock(%d) also failed: %v)", errMsg, blockNum-1, rbErr)
					}
					return abortSyncBatch(errMsg)
				}
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 2 & 3: CommitBlockState (protected by commitMutex and SEQUENTIAL GUARD)
		// ═══════════════════════════════════════════════════════════════════════════
		var commitOpts []blockchain.CommitOption
		commitOpts = append(commitOpts, blockchain.WithPersistToDB())
		commitOpts = append(commitOpts, blockchain.WithSaveTxMapping()) // ALWAYS track/save tx mappings in memory batch

		if shouldCommitState {
			if trie.GetStateBackend() != trie.BackendNOMT {
				commitOpts = append(commitOpts, blockchain.WithRebuildTries())
			} else {
				logger.Debug("🛡️ [NOMT-SAFETY] Skipping WithRebuildTries() for NOMT backend (block #%d). "+
					"NOMT only keeps latest state — rebuilding from P2P block header would corrupt active trie. "+
					"Re-alignment deferred to finalizeSyncedPrefix.", blockNum)
			}
			// Ensure mapping batches from memory are flushed to DB periodically as well as on batch exit.
			commitOpts = append(commitOpts, blockchain.WithCommitMappings())
		}

		if _, err := rh.chainState.CommitBlockState(blk, commitOpts...); err != nil {
			logger.Error("🚨 [SYNC-FATAL] [EXECUTE SYNC] Failed to CommitBlockState for block #%d: %v — rolling back all block mutations (Chain KV + NOMT + Xapian) and aborting sync batch!", blockNum, err)
			errMsg := fmt.Sprintf("CommitBlockState failed for block %d: %v", blockNum, err)
			rollbackBlockMutations()
			if blockNum > 0 {
				if _, rbErr := mvm.RollbackXapianAboveBlock(blockNum - 1); rbErr != nil {
					logger.Error("🚨 [FORK-RISK] RollbackXapianAboveBlock(%d) also FAILED after CommitBlockState error at block #%d: %v", blockNum-1, blockNum, rbErr)
					errMsg = fmt.Sprintf("%s (RollbackXapianAboveBlock(%d) also failed: %v)", errMsg, blockNum-1, rbErr)
				}
			}
			return abortSyncBatch(errMsg)
		}

		if shouldCommitState && trie.GetStateBackend() != trie.BackendNOMT {
			localRoot := rh.chainState.GetAccountStateDB().Trie().Hash()
			expectedRoot := header.AccountStatesRoot()
			if localRoot != expectedRoot && expectedRoot != (common.Hash{}) && expectedRoot != trie.EmptyRootHash {
				logger.Error("🚨 [STATE VERIFY] Batch stateRoot MISMATCH! block=#%d local=%s expected=%s. Rolling back block and HALTING sync.",
					blockNum, localRoot.Hex(), expectedRoot.Hex())
				rollbackBlockMutations()
				if blockNum > 0 {
					_ = rollbackLocalChainToBlock(blockNum-1, common.Hash{})
				}
				return abortSyncBatch(fmt.Sprintf("stateRoot mismatch at block %d: local=%s expected=%s",
					blockNum, localRoot.Hex()[:18], expectedRoot.Hex()[:18]))
			}
			logger.Info("✅ [STATE VERIFY] Batch stateRoot VERIFIED: block=#%d root=%s", blockNum, localRoot.Hex()[:18]+"...")
		}

		executedCount++
		if blockNum >= lastExecutedBlock {
			lastExecutedBlock = blockNum
			lastExecutedBlk = blk
		}
		if blockGEI > lastExecutedGEI {
			lastExecutedGEI = blockGEI
		}
		if len(pendingBlockSessions) > 0 {
			allNomtSessions = append(allNomtSessions, pendingBlockSessions...)
		}

		// ═══════════════════════════════════════════════════════════════
		// BLOCK-BY-BLOCK REALIGNMENT (May 2026):
		// Realignment of Go's active in-memory trie state must happen block-by-block
		// immediately after each block is committed to PebbleDB.
		// ═══════════════════════════════════════════════════════════════
		if trie.GetStateBackend() == trie.BackendNOMT {
			newAccountRoot := header.AccountStatesRoot()
			newStakeRoot := common.Hash(header.StakeStatesRoot())

			// Realign account state trie block-by-block via Go cache pointers (lightweight)
			if asDB := rh.chainState.GetAccountStateDB(); asDB != nil {
				asDB.SetOriginRootHash(newAccountRoot)
				if nomtTrie, ok := asDB.Trie().(*trie.NomtStateTrie); ok {
					nomtTrie.RealignRoot(newAccountRoot)
				}
			}
			// Realign stake state trie block-by-block via Go cache pointers (lightweight)
			if stakeDB := rh.chainState.GetStakeStateDB(); stakeDB != nil {
				stakeDB.SetOriginRootHash(newStakeRoot)
				if nomtTrie, ok := stakeDB.Trie().(*trie.NomtStateTrie); ok {
					nomtTrie.RealignRoot(newStakeRoot)
				}
			}
			logger.Debug("🔧 [NOMT-SYNC-REALIGN] Block #%d: Go cache pointers realigned: account=%s, stake=%s",
				blockNum, newAccountRoot.Hex()[:18]+"...", newStakeRoot.Hex()[:18]+"...")
		}

		// Broadcast transaction receipts for SyncOnly nodes if callback is configured and receipts are present
		if rh.broadcastEventsAndReceiptsCallback != nil && parsedBackupDb != nil && len(parsedBackupDb.ReceiptBatchPut) > 0 {
			deserializedBatch, err := storage.DeserializeBatch(parsedBackupDb.ReceiptBatchPut)
			if err == nil && len(deserializedBatch) > 0 {
				var syncReceipts []types.Receipt
				var syncEventLogs []types.EventLog

				for _, kv := range deserializedBatch {
					serializedReceipt := kv[1]
					if len(serializedReceipt) > 0 {
						pbReceipt := &pb.Receipt{}
						if err := proto.Unmarshal(serializedReceipt, pbReceipt); err == nil {
							typesReceipt := receipt.ReceiptFromProto(pbReceipt)
							syncReceipts = append(syncReceipts, typesReceipt)

							for _, pbLog := range pbReceipt.EventLogs {
								typesLog := smart_contract.NewEventLogFromProto(pbLog)
								syncEventLogs = append(syncEventLogs, typesLog)
							}
						}
					}
				}

				if len(syncReceipts) > 0 {
					logger.Info("📤 [SYNC BROADCAST] Calling broadcastEventsAndReceiptsCallback for synced block #%d with %d receipts and %d event logs",
						blockNum, len(syncReceipts), len(syncEventLogs))
					rh.broadcastEventsAndReceiptsCallback(blk, syncReceipts, syncEventLogs)
				}
			}
		}

		if shouldCommitState && trie.GetStateBackend() == trie.BackendNOMT {
			localRoot := rh.chainState.GetAccountStateDB().Trie().Hash()
			expectedRoot := header.AccountStatesRoot()
			var nomtHandleRoot common.Hash
			var hasNomtRoot bool
			if i == 0 {
				nomtHandleRoot, hasNomtRoot = trie.GetNomtHandleRoot("account_state")
			}

			logger.Info("🔍 [NOMT-SYNC-VERIFY] Block #%d: trieRoot=%s, handleRoot=%s, expectedRoot=%s, handleOK=%v",
				blockNum,
				localRoot.Hex()[:18]+"...",
				func() string {
					if hasNomtRoot {
						return nomtHandleRoot.Hex()[:18] + "..."
					}
					return "N/A"
				}(),
				expectedRoot.Hex()[:18]+"...",
				hasNomtRoot)
		}

		// Update persistent counters (GEI, NOMT committed block, + optional peer commit index) AFTER CommitBlockState succeeds.
		if blockGEI > 0 {
			storage.UpdateLastGlobalExecIndex(blockGEI)
		}
		if trie.GetStateBackend() == trie.BackendNOMT && blockNum > 0 {
			storage.UpdateLastNomtCommittedBlock(blockNum)
		}

		if shouldImportPeerCommitIndex(request.PreserveOwnCommitIndex, header.CommitIndex()) {
			commitIdx32 := uint32(header.CommitIndex())
			currentEpoch := header.Epoch()
			lastEpoch := storage.GetLastHandledCommitEpoch()

			if currentEpoch > lastEpoch || (currentEpoch == lastEpoch && commitIdx32 > storage.GetLastHandledCommitIndex()) {
				storage.ForceSetLastHandledCommitIndex(commitIdx32)
				storage.UpdateLastHandledCommitEpoch(currentEpoch)
				logger.Info("🔄 [STARTUP-SYNC] Restored lastHandledCommitIndex to %d (epoch %d) from synced block #%d", commitIdx32, currentEpoch, blockNum)
			}
		}

		// Persist latest FullDbLogs index ONLY after CommitBlockState succeeded.
		if parsedBackupDb != nil && len(parsedBackupDb.FullDbLogs) > 0 {
			if backupStorage := rh.storageManager.GetStorageBackupDb(); backupStorage != nil {
				if errPut := storage.PersistLatestFullDbLogsBatch(backupStorage, parsedBackupDb.FullDbLogs); errPut != nil {
					logger.Error("❌ [BLOCK SYNC] Failed to persist latest full_db_logs index for block #%d: %v", blockNum, errPut)
				}
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 6: Persist backup data for Sub nodes (ONLY after CommitBlockState succeeded)
		// ═══════════════════════════════════════════════════════════════════════════
		if len(backupBytes) > 0 {
			rh.persistBackupForSub(backupBytes, blockNum)
		}
		logger.Debug("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] ✅ Executed block #%d: hash=%s, epoch=%d, gei=%d, txs=%d",
			blockNum, blockHash.Hex()[:18]+"...", header.Epoch(), blockGEI, len(blk.Transactions()))

		if executedCount%100 == 0 {
			logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Progress: executed %d/%d blocks", executedCount, blockCount)
		}
	}

	if finErr := finalizeSyncedPrefix(); finErr != nil {
		return &pb.SyncBlocksResponse{
			SyncedCount:     executedCount,
			LastSyncedBlock: lastExecutedBlock,
			LastExecutedGei: lastExecutedGEI,
			Error:           finErr.Error(),
		}, nil
	}

	logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] ✅ Completed: executed %d/%d blocks, last_block=#%d, last_gei=%d",
		executedCount, blockCount, lastExecutedBlock, lastExecutedGEI)

	// Cache invalidation is handled by the defer block at the start of the function

	return &pb.SyncBlocksResponse{
		SyncedCount:     executedCount,
		LastSyncedBlock: lastExecutedBlock,
		LastExecutedGei: lastExecutedGEI,
		Error:           "",
	}, nil
}

// persistBackupForSub saves backup data to PebbleDB for Sub node recovery.
func (rh *RequestHandler) persistBackupForSub(backupBytes []byte, blockNum uint64) {
	backupStorage := rh.storageManager.GetStorageBackupDb()
	if backupStorage == nil {
		return
	}
	backupKey := []byte(fmt.Sprintf("block_data_topic-%d", blockNum))
	if putErr := backupStorage.Put(backupKey, backupBytes); putErr != nil {
		logger.Error("❌ [EXECUTE SYNC] Failed to persist backup for block #%d: %v", blockNum, putErr)
	}
	legacyKey := []byte(fmt.Sprintf("backup_%d", blockNum))
	_ = backupStorage.Put(legacyKey, backupBytes)
}

type syncBatchEntry struct {
	name    string
	data    []byte
	storage storage.Storage
}

type parsedTrieDbBatch struct {
	key    string
	prefix string
	batch  [][2][]byte
}

type parsedBackupDbBatches struct {
	entries           []syncBatchEntry
	aggregatedBatches map[string][][2][]byte
	trieBatches       []parsedTrieDbBatch
}

func (rh *RequestHandler) buildSyncBatchEntries(backupDb *storage.BackUpDb) []syncBatchEntry {
	return []syncBatchEntry{
		{"Block", backupDb.BockBatch, rh.storageManager.GetStorageBlock()},
		{"Account", backupDb.AccountBatch, rh.storageManager.GetStorageAccount()},
		{"Code", backupDb.CodeBatchPut, rh.storageManager.GetStorageCode()},
		{"SmartContract", backupDb.SmartContractBatch, rh.storageManager.GetStorageSmartContract()},
		{"SC Storage", backupDb.SmartContractStorageBatch, rh.storageManager.GetStorageSmartContract()},
		{"Receipt", backupDb.ReceiptBatchPut, rh.storageManager.GetStorageReceipt()},
		{"Transaction", backupDb.TxBatchPut, rh.storageManager.GetStorageTransaction()},
		{"StakeState", backupDb.StakeState, rh.storageManager.GetStorageStake()},
		{"Mapping", backupDb.MapppingBatch, rh.storageManager.GetStorageMapping()},
	}
}

// parseAndValidateBackupDb deserializes and validates all KV batches, TrieDB batches, and FullDbLogs
// hex addresses in memory BEFORE any local fork rollback or database mutation occurs.
func (rh *RequestHandler) parseAndValidateBackupDb(backupDb *storage.BackUpDb) (*parsedBackupDbBatches, error) {
	entries := rh.buildSyncBatchEntries(backupDb)
	aggregatedBatches := make(map[string][][2][]byte)

	for _, entry := range entries {
		if len(entry.data) > 0 {
			deserialized, err := storage.DeserializeBatch(entry.data)
			if err != nil {
				return nil, fmt.Errorf("error deserializing batch '%s' for block %d: %w", entry.name, backupDb.BockNumber, err)
			}
			if len(deserialized) > 0 {
				aggregatedBatches[entry.name] = deserialized
				logger.Debug("[NOMT-DEBUG][APPLY-BATCH] Block #%d: namespace=%s keys=%d (raw=%d bytes)",
					backupDb.BockNumber, entry.name, len(deserialized), len(entry.data))
			}
		} else if entry.name == "Account" {
			logger.Error("[NOMT-DEBUG][APPLY-BATCH] Block #%d: AccountBatch is EMPTY — NOMT cannot rebuild account_state root! Snapshot may be missing AccountBatch data.",
				backupDb.BockNumber)
		}
	}

	var trieBatches []parsedTrieDbBatch
	if len(backupDb.TrieDatabaseBatchPut) > 0 {
		if rh.storageManager.GetStorageDatabaseTrie() == nil {
			return nil, fmt.Errorf("shared database trie is nil")
		}
		var keys []string
		for k := range backupDb.TrieDatabaseBatchPut {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, key := range keys {
			value := backupDb.TrieDatabaseBatchPut[key]
			if len(value) == 0 {
				continue
			}
			deserialized, err := storage.DeserializeBatch(value)
			if err != nil {
				return nil, fmt.Errorf("error deserializing TrieDB batch '%s' for block %d: %w", key, backupDb.BockNumber, err)
			}
			if len(deserialized) == 0 {
				continue
			}
			parts := strings.Split(key, "/")
			if len(parts) != 2 {
				logger.Warn("⚠️ Ignored invalid TrieDB batch key format: %s", key)
				continue
			}
			addressHex := parts[0]
			dbName := parts[1]
			dbNameHash := crypto.Keccak256([]byte(dbName))
			prefix := fmt.Sprintf("%x:%s:", dbNameHash, addressHex)
			trieBatches = append(trieBatches, parsedTrieDbBatch{
				key:    key,
				prefix: prefix,
				batch:  deserialized,
			})
		}
	}

	for _, logs := range backupDb.FullDbLogs {
		for addrHex := range logs {
			processed := strings.TrimSpace(addrHex)
			if strings.HasPrefix(processed, "0x") || strings.HasPrefix(processed, "0X") {
				processed = processed[2:]
			}
			if len(processed)%2 != 0 {
				processed = "0" + processed
			}
			if len(processed) != 40 {
				return nil, fmt.Errorf("invalid FullDbLogs address length (%d) for '%s'", len(processed), addrHex)
			}
			if _, err := hex.DecodeString(processed); err != nil {
				return nil, fmt.Errorf("invalid FullDbLogs hex address '%s': %w", addrHex, err)
			}
		}
	}

	return &parsedBackupDbBatches{
		entries:           entries,
		aggregatedBatches: aggregatedBatches,
		trieBatches:       trieBatches,
	}, nil
}

// applyStorageBatchWithUndo captures pre-image values for every unique key in batch before
// calling store.BatchPut(batch), returning an undo closure that restores overwritten keys
// and deletes newly-inserted keys on failure.
func applyStorageBatchWithUndo(store storage.Storage, batch [][2][]byte, updateBucketCache bool) (func(), error) {
	seen := make(map[string]struct{}, len(batch))
	var toRestore [][2][]byte
	var toDelete [][]byte
	var bucketUndo [][2][]byte

	for _, kv := range batch {
		k := kv[0]
		ks := string(k)
		if _, dup := seen[ks]; dup {
			continue
		}
		seen[ks] = struct{}{}
		kCopy := append([]byte(nil), k...)
		oldVal, getErr := store.Get(k)
		if getErr == nil {
			vCopy := append([]byte(nil), oldVal...)
			toRestore = append(toRestore, [2][]byte{kCopy, vCopy})
			if updateBucketCache {
				bucketUndo = append(bucketUndo, [2][]byte{kCopy, vCopy})
			}
		} else {
			toDelete = append(toDelete, kCopy)
			if updateBucketCache {
				bucketUndo = append(bucketUndo, [2][]byte{kCopy, nil})
			}
		}
	}

	if updateBucketCache {
		trie.UpdateBucketCacheFromBatch(store, batch)
	}
	if err := store.BatchPut(batch); err != nil {
		if updateBucketCache && len(bucketUndo) > 0 {
			trie.UpdateBucketCacheFromBatch(store, bucketUndo)
		}
		return nil, err
	}

	undo := func() {
		if len(toRestore) > 0 {
			_ = store.BatchPut(toRestore)
		}
		if len(toDelete) > 0 {
			_ = store.BatchDelete(toDelete)
		}
		if updateBucketCache && len(bucketUndo) > 0 {
			trie.UpdateBucketCacheFromBatch(store, bucketUndo)
		}
	}
	return undo, nil
}

// applyParsedBackupDbBatches applies NOMT, KV, TrieDB, and Xapian FullDbLogs batches while
// building a complete LIFO undo stack so any failure here OR later in CommitBlockState
// rolls back all chain data symmetrically alongside Xapian.
func (rh *RequestHandler) applyParsedBackupDbBatches(
	backupDb *storage.BackUpDb,
	parsed *parsedBackupDbBatches,
	blockHash common.Hash,
	blockNum uint64,
	txs []common.Hash,
) ([]trie.NomtSessionToFlush, map[string][][2][]byte, func(), error) {
	effectiveBlockNum := backupDb.BockNumber
	if effectiveBlockNum == 0 {
		effectiveBlockNum = blockNum
	}

	var undoFns []func()
	rolledBack := false
	rollbackAll := func() {
		if rolledBack {
			return
		}
		rolledBack = true
		for idx := len(undoFns) - 1; idx >= 0; idx-- {
			undoFns[idx]()
		}
		if bc := blockchain.GetBlockChainInstance(); bc != nil && effectiveBlockNum > storage.GetLastBlockNumber() {
			if blockHash != (common.Hash{}) {
				bc.DeleteBlockFromCache(blockHash)
			}
			if effectiveBlockNum > 0 {
				bc.DiscardBlockMappings(effectiveBlockNum)
			}
			if len(txs) > 0 {
				bc.DiscardTxMappings(txs)
			}
		}
		if rh.chainState != nil {
			rh.chainState.InvalidateAllState()
		}
	}

	var scChangelogDB *state_changelog.StateChangelogDB
	if rh.chainState != nil {
		if scDB := rh.chainState.GetSmartContractDB(); scDB != nil {
			scChangelogDB = scDB.GetChangelogDB()
		}
	}

	var accChangelog, stakeChangelog *state_changelog.StateChangelogDB
	if rh.chainState != nil {
		accChangelog = rh.chainState.GetChangelogDB()
		stakeChangelog = rh.chainState.GetStakeChangelogDB()
	}

	sessions, nomtPreImages, rollbackNomt, err := trie.ApplyNomtReplicationBatchesWithPreImages(
		parsed.aggregatedBatches,
		accChangelog,
		stakeChangelog,
		scChangelogDB,
		effectiveBlockNum,
	)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("error replicating NOMT batches in applyBackupDbBatches: %w", err)
	}
	undoFns = append(undoFns, func() {
		if rbErr := rollbackNomt(); rbErr != nil {
			logger.Error("🚨 [SYNC-ROLLBACK] rollbackNomt failed for block #%d: %v", effectiveBlockNum, rbErr)
		}
	})

	// Apply remaining KV batches (where 'nomt:' prefixed keys were stripped on NOMT backend)
	for _, entry := range parsed.entries {
		if batch, ok := parsed.aggregatedBatches[entry.name]; ok && len(batch) > 0 && entry.storage != nil {
			undoKV, writeErr := applyStorageBatchWithUndo(entry.storage, batch, true)
			if writeErr != nil {
				rollbackAll()
				return nil, nil, func() {}, fmt.Errorf("error writing batch '%s' for block %d: %w", entry.name, effectiveBlockNum, writeErr)
			}
			undoFns = append(undoFns, undoKV)
		}
	}

	// Apply TrieDB batches with pre-image capture
	if len(parsed.trieBatches) > 0 {
		sharedDB := rh.storageManager.GetStorageDatabaseTrie()
		for _, tb := range parsed.trieBatches {
			prefixedDB := storage.NewPrefixStorage(sharedDB, tb.prefix)
			undoTrieKV, writeErr := applyStorageBatchWithUndo(prefixedDB, tb.batch, false)
			if writeErr != nil {
				rollbackAll()
				return nil, nil, func() {}, fmt.Errorf("error writing TrieDB batch '%s' for block %d: %w", tb.key, effectiveBlockNum, writeErr)
			}
			undoFns = append(undoFns, undoTrieKV)
		}
	}

	// Replay MVM FullDbLogs to keep C++ Xapian DBs consistent with chain state
	if len(backupDb.FullDbLogs) > 0 {
		result := mvm.CallReplayFullDbLogsBatchAtBlock(backupDb.FullDbLogs, effectiveBlockNum)
		if result == 0 {
			var rbErr error
			if effectiveBlockNum > 0 {
				if _, rbErr = mvm.RollbackXapianAboveBlock(effectiveBlockNum - 1); rbErr != nil {
					logger.Error("🚨 [FORK-RISK] RollbackXapianAboveBlock(%d) also FAILED after ReplayFullDbLogs error at block #%d: %v", effectiveBlockNum-1, effectiveBlockNum, rbErr)
				}
			}
			rollbackAll()
			logger.Error("🚨 [FORK-RISK] ReplayFullDbLogsBatchAtBlock (epoch sync) FAILED for %d batches at block #%d — rolled back Chain KV + NOMT + Xapian!", len(backupDb.FullDbLogs), effectiveBlockNum)
			if rbErr != nil {
				return nil, nil, func() {}, fmt.Errorf("ReplayFullDbLogs failed for block #%d (%d batches) and rollback to #%d failed: %w", effectiveBlockNum, len(backupDb.FullDbLogs), effectiveBlockNum-1, rbErr)
			}
			return nil, nil, func() {}, fmt.Errorf("ReplayFullDbLogs failed for block #%d (%d batches)", effectiveBlockNum, len(backupDb.FullDbLogs))
		}
		logger.Info("📥 [BLOCK SYNC] ✅ Replayed %d FullDbLogs entries for block %d", len(backupDb.FullDbLogs), effectiveBlockNum)
		mvm.CommitAllXapianAtBlock(effectiveBlockNum)
		if effectiveBlockNum > 0 {
			undoFns = append(undoFns, func() {
				if _, rbErr := mvm.RollbackXapianAboveBlock(effectiveBlockNum - 1); rbErr != nil {
					logger.Error("🚨 [FORK-RISK] RollbackXapianAboveBlock(%d) failed while rolling back block #%d: %v", effectiveBlockNum-1, effectiveBlockNum, rbErr)
				}
			})
		}
	} else if effectiveBlockNum > 0 {
		if _, rbErr := mvm.RollbackXapianAboveBlock(effectiveBlockNum - 1); rbErr != nil {
			rollbackAll()
			logger.Error("🚨 [FORK-RISK] RollbackXapianAboveBlock(%d) failed while syncing block #%d with empty FullDbLogs: %v",
				effectiveBlockNum-1, effectiveBlockNum, rbErr)
			return nil, nil, func() {}, fmt.Errorf("RollbackXapianAboveBlock(%d) failed for block #%d: %w", effectiveBlockNum-1, effectiveBlockNum, rbErr)
		}
	}

	return sessions, nomtPreImages, rollbackAll, nil
}

// applyBackupDbBatches applies all state batch data from a BackUpDb to local storages.
func (rh *RequestHandler) applyBackupDbBatches(backupDb *storage.BackUpDb) ([]trie.NomtSessionToFlush, error) {
	parsed, err := rh.parseAndValidateBackupDb(backupDb)
	if err != nil {
		return nil, err
	}
	sessions, _, _, err := rh.applyParsedBackupDbBatches(backupDb, parsed, common.Hash{}, backupDb.BockNumber, nil)
	return sessions, err
}
