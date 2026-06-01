package executor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

// HandleGetLastBlockNumberRequest processes a GetLastBlockNumberRequest and returns a LastBlockNumberResponse.
// Used to initialize next_expected_index in Rust executor_client.
// CRITICAL FIX: Validate that block hash exists before returning, not just the counter!
func (rh *RequestHandler) HandleGetLastBlockNumberRequest(request *pb.GetLastBlockNumberRequest) (*pb.LastBlockNumberResponse, error) {
	logger.Debug("🔍 [INIT] Handling GetLastBlockNumberRequest (Rust executor_client initializing next_expected_index)")

	// Get last block number from counter
	committedBlockNumber := storage.GetLastBlockNumber()
	assignedBlockNumber := storage.GetLastAssignedBlockNumber()
	logger.Debug("🔍 [INIT] Block counter from storage: committed=%d, assigned=%d", committedBlockNumber, assignedBlockNumber)

	// CRITICAL FIX: With blockchainInitDone, the counter is AUTHORITATIVE.
	// We return the maximum of committed and assigned to prevent block numbering overlap
	// during epoch transitions where the pipeline is not fully flushed.
	validatedBlockNumber := committedBlockNumber
	if assignedBlockNumber > committedBlockNumber {
		validatedBlockNumber = assignedBlockNumber
	}

	// Guard against nil blockchain instance (during early startup or tests)
	blockchainInstance := blockchain.GetBlockChainInstance()
	if blockchainInstance == nil {
		logger.Warn("⚠️ [INIT] Blockchain instance is nil during GetLastBlockNumber request")
		validatedBlockNumber = 0
	}

	// FIX: Return the actual block number from Go's counter, not the LastGlobalExecIndex.
	// This ensures that `catchup` block syncer does not loop infinitely trying to fetch
	// empty blocks that were skipped by Go Master.
	returnBlockNumber := validatedBlockNumber

	// Also return LastGlobalExecIndex (tracks ALL commits including empty ones)
	// CRITICAL: Rust uses this for epoch transition SYNC WAIT comparison
	// BlockNumber tracks only non-empty commits, GEI tracks ALL commits
	lastGEI := storage.GetLastGlobalExecIndex()

	// FIX 4: Determine if Go Master is fully ready.
	// CRITICAL: Use the explicit blockchainInitDone flag instead of just checking
	// blockchainInstance != nil. The instance can exist while the block number is
	// still being loaded from metadata.json → LevelDB. Only report ready=true
	// AFTER initBlockchain() has fully verified and loaded the chain state.
	isReady := storage.IsBlockchainInitDone()
	if !isReady {
		if blockchainInstance == nil {
			logger.Warn("⚠️ [INIT] Go Master not ready: blockchain instance is nil.")
		} else {
			logger.Warn("⚠️ [INIT] Go Master not ready: blockchain init still in progress (block=%d).", returnBlockNumber)
		}
	}

	var lastEpoch uint64
	if blockchainInstance != nil && returnBlockNumber > 0 {
		hash, ok := blockchainInstance.GetBlockHashByNumber(returnBlockNumber)
		if ok && hash != (common.Hash{}) {
			block, err := rh.chainState.GetBlockDatabase().GetBlockByHash(hash)
			if err == nil && block != nil {
				lastEpoch = block.Header().Epoch()
			}
		} else if returnBlockNumber > committedBlockNumber {
			// PIPELINE FIX: This block was assigned by Rust but hasn't been committed to DB yet.
			// It is safely buffered in Go's memory. Do NOT scan backwards, as that would return
			// a stale block number and cause duplicate numbering in the next epoch.
			// We return the assigned block number with an empty hash.
			logger.Info("✅ [INIT] Block %d is in pipeline (assigned > committed). Bypassing backward scan.", returnBlockNumber)
			lastEpoch = rh.chainState.GetCurrentEpoch()
		} else {
			// EPOCH-BOUNDARY FIX: Counter reports block N but hash not persisted yet.
			// This happens when snapshot metadata records the epoch boundary block number,
			// but the block itself hasn't been committed to the chain DB.
			//
			// CRITICAL FIX (May 2026): The backward scan MUST NOT modify returnBlockNumber!
			// Previously, returnBlockNumber was overwritten with the fallback block number,
			// causing Rust to set next_block_number too low → block number overlap/gap.
			// The scan is only used to find hash/epoch metadata for Rust's POST-GATE-VERIFY.
			logger.Warn("⚠️ [INIT] Block %d exists in counter but hash not found in DB. "+
				"Using counter value (hash will be empty). Rust must use block NUMBER as authoritative.",
				returnBlockNumber)
			// Keep returnBlockNumber unchanged — it's the correct max(committed, assigned)
			// Only scan backwards for epoch metadata (not for block number)
			const maxFallbackScan = 10
			for delta := uint64(1); delta <= maxFallbackScan && delta <= returnBlockNumber; delta++ {
				fallbackBlock := returnBlockNumber - delta
				if fallbackBlock == 0 {
					break
				}
				fallbackHash, fallbackOk := blockchainInstance.GetBlockHashByNumber(fallbackBlock)
				if fallbackOk && fallbackHash != (common.Hash{}) {
					// Found a persisted block — use its epoch for metadata
					block, err := rh.chainState.GetBlockDatabase().GetBlockByHash(fallbackHash)
					if err == nil && block != nil {
						lastEpoch = block.Header().Epoch()
					}
					logger.Info("✅ [INIT] Block %d hash not found, but found epoch=%d from nearby block %d (delta=-%d). "+
						"Returning block=%d (unchanged) with empty hash.",
						returnBlockNumber, lastEpoch, fallbackBlock, delta, returnBlockNumber)
					break
				}
			}
			// If no nearby block found for epoch, use current epoch
			if lastEpoch == 0 {
				lastEpoch = rh.chainState.GetCurrentEpoch()
				logger.Info("✅ [INIT] No nearby block found for epoch lookup. Using current epoch=%d. block=%d",
					lastEpoch, returnBlockNumber)
			}
		}
	}

	response := &pb.LastBlockNumberResponse{
		LastBlockNumber:     returnBlockNumber,
		LastGlobalExecIndex: lastGEI,
		IsReady:             isReady,
		// CRITICAL FIX (2026-05-24): Return the true Rust DAG commit digest for anti-fork check on startup.
		// POST-GATE-VERIFY now queries local block hash via get_blocks_range, so we no longer
		// hijack this field with the Keccak block hash.
		LastExecutedCommitHash: storage.GetLastExecutedCommitHash(),
		LastEpoch:              lastEpoch,
	}

	logger.Debug("✅ [INIT] Returning last block number for Rust: block=%d, gei=%d, epoch=%d (counter=%d, validated=%d, is_ready=%v)",
		returnBlockNumber, lastGEI, lastEpoch, committedBlockNumber, validatedBlockNumber, isReady)
	return response, nil
}

// HandleGetCurrentEpochRequest processes a GetCurrentEpochRequest and returns the current epoch from Go state (Sui-style)
func (rh *RequestHandler) HandleGetCurrentEpochRequest(request *pb.GetCurrentEpochRequest) (*pb.GetCurrentEpochResponse, error) {
	defer func(start time.Time) {
		if d := time.Since(start); d > 100*time.Millisecond {
			logger.Warn("⚠️ [FFI STALL] HandleGetCurrentEpochRequest took %v (Slow!)", d)
		}
	}(time.Now())

	logger.Debug("🔍 [GET CURRENT EPOCH] Handling GetCurrentEpochRequest from Rust")

	// Get current epoch from blockchain state
	currentEpoch := rh.chainState.GetCurrentEpoch()
	logger.Debug("🔍 [GET CURRENT EPOCH] Current epoch from Go state", "epoch", currentEpoch)

	// NOTE: SaveEpochData() removed here - it was debug code causing unnecessary I/O
	// Epoch data is already saved correctly during AdvanceEpoch

	response := &pb.GetCurrentEpochResponse{
		Epoch: currentEpoch,
	}

	logger.Debug("✅ [GET CURRENT EPOCH] Returning current epoch to Rust", "epoch", currentEpoch)
	return response, nil
}

// HandleGetEpochStartTimestampRequest processes a GetEpochStartTimestampRequest and returns epoch start timestamp (Sui-style)
func (rh *RequestHandler) HandleGetEpochStartTimestampRequest(request *pb.GetEpochStartTimestampRequest) (*pb.GetEpochStartTimestampResponse, error) {
	logger.Info("Handling GetEpochStartTimestampRequest (Sui-style epoch transition)", "epoch", request.Epoch)

	// Get epoch start timestamp from blockchain state
	// This should be stored in the blockchain state similar to how Sui stores epoch_start_timestamp_ms
	epochTimestamp, err := rh.chainState.GetEpochStartTimestamp(request.Epoch)
	if err != nil {
		return nil, fmt.Errorf("could not get epoch start timestamp for epoch %d: %w", request.Epoch, err)
	}

	logger.Info("Epoch start timestamp from Go state", "epoch", request.Epoch, "timestamp_ms", epochTimestamp)

	response := &pb.GetEpochStartTimestampResponse{
		TimestampMs: epochTimestamp,
	}

	return response, nil
}

// HandleAdvanceEpochRequest processes a AdvanceEpochRequest and advances Go state epoch (Sui-style completion)
func (rh *RequestHandler) HandleAdvanceEpochRequest(request *pb.AdvanceEpochRequest) (*pb.AdvanceEpochResponse, error) {
	defer func(start time.Time) {
		if d := time.Since(start); d > 100*time.Millisecond {
			logger.Warn("⚠️ [FFI STALL] HandleAdvanceEpochRequest took %v (Slow!)", d)
		}
	}(time.Now())

	logger.Info("Handling AdvanceEpochRequest (Sui-style epoch transition completion)",
		"new_epoch", request.NewEpoch,
		"timestamp_ms", request.EpochStartTimestampMs,
		"boundary_block", request.BoundaryBlock,
		"boundary_gei", request.BoundaryGei)

	// ═══════════════════════════════════════════════════════════════════
	// THE EPOCH GUARD: Prevent duplicate advances & log divergence
	// ═══════════════════════════════════════════════════════════════════
	currentEpoch := rh.chainState.GetCurrentEpoch()
	lastCommittedBlock := storage.GetLastBlockNumber()
	lastGEI := storage.GetLastGlobalExecIndex()

	if request.NewEpoch < currentEpoch && request.NewEpoch > 0 {
		// Rust consensus catching up sequentially during snapshot recovery.
		// We silently accept but DO NOT modify Go state, to let Rust proceed.
		logger.Warn("🛡️ [EPOCH GUARD] Backwards AdvanceEpoch request ignored! Target Epoch %d, but Go is already at Epoch %d. (Likely a recovery catch-up).", request.NewEpoch, currentEpoch)
		return &pb.AdvanceEpochResponse{
			NewEpoch:              currentEpoch,
			EpochStartTimestampMs: rh.chainState.GetCurrentEpochStartTimestampMs(),
		}, nil
	}

	if request.NewEpoch == currentEpoch && request.NewEpoch > 0 {
		// Rust loop/monitor fired a duplicate advance.
		// We silently accept but DO NOT modify Go state, to let Rust proceed.
		logger.Warn("🛡️ [EPOCH GUARD] Duplicate AdvanceEpoch rejected! Target Epoch %d, but Go is already at Epoch %d.", request.NewEpoch, currentEpoch)
		return &pb.AdvanceEpochResponse{
			NewEpoch:              currentEpoch,
			EpochStartTimestampMs: rh.chainState.GetCurrentEpochStartTimestampMs(),
		}, nil
	}

	if request.BoundaryBlock > 0 && request.BoundaryBlock < lastCommittedBlock {
		logger.Warn("⚠️ [EPOCH GUARD] Boundary Block %d < Go Last Block %d. Allowing (likely a recovery replay).", request.BoundaryBlock, lastCommittedBlock)
	}

	if request.BoundaryGei > 0 && lastGEI > request.BoundaryGei {
		logger.Warn("⚠️ [EPOCH GUARD] Boundary GEI %d < Go Last GEI %d. Go has already executed blocks into this epoch!", request.BoundaryGei, lastGEI)
	}

	// CRITICAL FIX: If Rust sends timestamp_ms=0 (provisional placeholder),
	// derive the actual timestamp from the boundary block header.
	// This prevents Go from storing 0ms and returning it in get_epoch_boundary_data,
	// which would break consensus genesis block hash calculation.
	timestampMs := request.EpochStartTimestampMs
	if timestampMs == 0 && request.BoundaryBlock > 0 {
		logger.Info("⚠️ [ADVANCE EPOCH] Received timestamp_ms=0 (provisional). Deriving from boundary block %d header.", request.BoundaryBlock)
		blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(request.BoundaryBlock)
		if ok {
			blockData, err := rh.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
			if err == nil {
				headerTs := blockData.Header().TimeStamp()
				if headerTs > 0 {
					timestampMs = headerTs
					logger.Info("✅ [ADVANCE EPOCH] Derived timestamp from boundary block %d header: %d ms", request.BoundaryBlock, timestampMs)
				}
			}
		}
		if timestampMs == 0 {
			// FORK-SAFETY FIX (G-C2): Use genesis config timestamp as deterministic fallback.
			// time.Now() differs between nodes → epoch_start_timestamp_ms divergence → fork.
			if rh.genesisPath != "" {
				if genesisData, gErr := config.LoadGenesisData(rh.genesisPath); gErr == nil && genesisData.Config.EpochTimestampMs > 0 {
					timestampMs = genesisData.Config.EpochTimestampMs
					logger.Warn("⚠️ [ADVANCE EPOCH] Used genesis epoch_timestamp_ms as deterministic fallback: %d ms", timestampMs)
				}
			}
			if timestampMs == 0 {
				// No deterministic source available — this is a critical configuration error
				logger.Error("🚨 [ADVANCE EPOCH] CRITICAL: Cannot derive deterministic epoch timestamp! " +
					"boundary_block timestamp=0, genesis epoch_timestamp_ms=0. " +
					"This will cause fork divergence. Fix genesis.json to include epoch_timestamp_ms.")
				// Use boundary_block * 1000 as last resort (at least it's deterministic across nodes)
				timestampMs = request.BoundaryBlock * 1000
				if timestampMs == 0 {
					timestampMs = 1 // Avoid storing 0, use minimal deterministic value
				}
				logger.Warn("⚠️ [ADVANCE EPOCH] Using boundary_block-derived fallback timestamp: %d ms", timestampMs)
			}
		}
	}

	// Advance epoch in Go state with explicit boundary_block from Rust
	// This ensures deterministic epoch boundary instead of fallback to storage.GetLastBlockNumber()
	err := rh.chainState.AdvanceEpochWithBoundary(request.NewEpoch, timestampMs, request.BoundaryBlock, request.BoundaryGei)
	if err != nil {
		return nil, fmt.Errorf("could not advance epoch to %d: %w", request.NewEpoch, err)
	}

	// CRITICAL FORK-SAFETY FIX (May 2026): Reset commit index and epoch in GEIAuthority and DB.
	// When advancing to a new epoch via FFI, the new epoch commits will start with commitIndex=0 or 1.
	// We MUST synchronously reset Go's lastHandledCommitIndex to 0 and advance lastHandledCommitEpoch to newEpoch.
	// Otherwise, the idempotent execution guard (ShouldSkipCommit) will compare the new epoch's small commitIndex
	// against the old epoch's high commitIndex and incorrectly SKIP the first blocks of the new epoch!
	logger.Info("🔄 [ADVANCE EPOCH] Synchronously resetting GEIAuthority and storage commit index to 0 for new epoch %d", request.NewEpoch)
	storage.ForceSetLastHandledCommitIndex(0)
	storage.UpdateLastHandledCommitEpoch(request.NewEpoch)
	if rh.storageManager != nil && rh.storageManager.GetStorageBackupDb() != nil {
		rh.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitIndexHashKey.Bytes(), utils.Uint32ToBytes(0))
		rh.storageManager.GetStorageBackupDb().Put(storage.LastHandledCommitEpochHashKey.Bytes(), utils.Uint64ToBytes(request.NewEpoch))
	}
	if rh.resetCommitIndexCallback != nil {
		rh.resetCommitIndexCallback(request.NewEpoch)
	}

	// CRITICAL FIX: Push an async GEI update so that Go's LastGlobalExecIndex
	// actually advances to the boundary GEI. This prevents Go from staying at
	// the last executed block's GEI when empty commits are fast-skipped by Rust.
	// We also synchronously update the atomic variable to prevent a data race where
	// processRustEpochData reads the old GEI before the async persist completes.
	storage.UpdateLastGlobalExecIndex(request.BoundaryGei)
	if rh.pushAsyncGEIUpdateCallback != nil {
		rh.pushAsyncGEIUpdateCallback(request.BoundaryGei, nil, 0, request.NewEpoch)
	}

	logger.Info("✅ Successfully advanced Go state epoch",
		"new_epoch", request.NewEpoch,
		"timestamp_ms", timestampMs,
		"boundary_block", request.BoundaryBlock,
		"boundary_gei", request.BoundaryGei)

	// 📸 Notify snapshot manager about epoch transition
	if rh.snapshotManager != nil {
		rh.snapshotManager.OnEpochAdvanced(request.BoundaryBlock, request.NewEpoch)
	}

	// ═══════════════════════════════════════════════════════════════════
	// EPOCH VALIDATOR PERSISTENCE: Snapshot validators at boundary block.
	// This is the ONLY correct moment to capture them — NOMT state
	// contains the exact validators active at the epoch boundary.
	// After this, new blocks in the next epoch may change NOMT state.
	// The cached validators are persisted in epoch_data_backup.json,
	// which survives snapshot restore even when NOMT knownKeys is empty.
	// ═══════════════════════════════════════════════════════════════════
	if validators, vErr := rh.GetValidatorsAtBlockInternal(request.BoundaryBlock); vErr == nil && len(validators.Validators) > 0 {
		if serialized, sErr := json.Marshal(validators); sErr == nil {
			rh.chainState.SetEpochValidators(request.NewEpoch, serialized)
			logger.Info("💾 [EPOCH VALIDATORS] Cached %d validators for epoch %d at boundary block %d",
				len(validators.Validators), request.NewEpoch, request.BoundaryBlock)
			// Persist the epoch data again to include the validator cache
			if pErr := rh.chainState.SaveEpochDataSafe(); pErr != nil {
				logger.Warn("⚠️ [EPOCH VALIDATORS] Failed to persist epoch data with validators: %v", pErr)
			}
		} else {
			logger.Warn("⚠️ [EPOCH VALIDATORS] Failed to serialize validators: %v", sErr)
		}
	} else {
		logger.Warn("⚠️ [EPOCH VALIDATORS] Could not cache validators for epoch %d at boundary %d: vErr=%v",
			request.NewEpoch, request.BoundaryBlock, vErr)
	}

	response := &pb.AdvanceEpochResponse{
		NewEpoch:              request.NewEpoch,
		EpochStartTimestampMs: timestampMs,
	}

	return response, nil
}

// HandleGetEpochBoundaryDataRequest processes a GetEpochBoundaryDataRequest and returns unified epoch boundary data
// This is the single authoritative source for epoch transition data, ensuring consistency
func (rh *RequestHandler) HandleGetEpochBoundaryDataRequest(request *pb.GetEpochBoundaryDataRequest) (*pb.EpochBoundaryData, error) {
	defer func(start time.Time) {
		if d := time.Since(start); d > 100*time.Millisecond {
			logger.Warn("⚠️ [FFI STALL] HandleGetEpochBoundaryDataRequest took %v (Slow!)", d)
		}
	}(time.Now())

	epoch := request.GetEpoch()
	logger.Info("📊 [EPOCH BOUNDARY] Handling GetEpochBoundaryDataRequest", "epoch", epoch)

	// Get epoch boundary block and GEI
	currentEpoch := rh.chainState.GetCurrentEpoch()
	boundaryBlock, fromHistory := rh.chainState.GetEpochBoundaryBlock(epoch)
	boundaryGei := rh.chainState.GetEpochBoundaryGei(epoch)

	// SPECIAL CASE: Only epoch 0 uses boundary=0 (genesis)
	if epoch == 0 && !fromHistory {
		boundaryBlock = 0
		fromHistory = true
		logger.Info("✅ [EPOCH BOUNDARY] Using genesis boundary (block=0) for epoch 0")
	}

	if !fromHistory && epoch >= 1 {
		errMsg := fmt.Sprintf("epoch %d boundary block not stored (current_epoch=%d). "+
			"This node may not have witnessed the epoch transition. "+
			"Rust should fetch from peer or wait for sync to complete.", epoch, currentEpoch)
		logger.Error("❌ [EPOCH BOUNDARY] %s", errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	// =============================================================================
	// SYNC-AWARE TIMESTAMP: Handle when boundary block not yet synced
	// =============================================================================
	var epochTimestamp uint64
	var syncComplete bool = true
	queryBlock := boundaryBlock

	if epoch == 0 {
		// EPOCH 0: Genesis epoch - use genesis config timestamp
		epochTimestamp = rh.chainState.GetCurrentEpochStartTimestampMs()
		if epochTimestamp == 0 {
			if rh.genesisPath != "" {
				if genesisData, err := config.LoadGenesisData(rh.genesisPath); err == nil {
					if genesisData.Config.EpochTimestampMs > 0 {
						epochTimestamp = genesisData.Config.EpochTimestampMs
						logger.Info("✅ [EPOCH BOUNDARY] Loaded genesis timestamp from genesis.json: %d ms", epochTimestamp)
					}
				}
			}
			if epochTimestamp == 0 {
				// FORK-SAFETY FIX (G-C3): Genesis MUST provide epoch_timestamp_ms.
				// time.Now() is non-deterministic across nodes → fork.
				logger.Error("🚨 [EPOCH BOUNDARY] CRITICAL: No genesis timestamp found! " +
					"genesis.json must include epoch_timestamp_ms for deterministic consensus. " +
					"Using fallback value 1 to avoid crash, but this indicates misconfiguration.")
				epochTimestamp = 1 // Deterministic (same across all nodes) but signals misconfiguration
			}
		}
		logger.Info("✅ [EPOCH BOUNDARY] Epoch 0 using GENESIS timestamp: %d ms", epochTimestamp)
	} else {
		// EPOCH N (N >= 1): Retrieve the timestamp provided by Rust during AdvanceEpoch.
		// FORK-SAFETY: Check specific epoch start timestamp, fallback to current epoch start timestamp.
		var tsErr error
		epochTimestamp, tsErr = rh.chainState.GetEpochStartTimestamp(epoch)
		if tsErr != nil {
			// Fallback: Check if it is the current epoch
			if epoch == currentEpoch {
				epochTimestamp = rh.chainState.GetCurrentEpochStartTimestampMs()
			}
			if epochTimestamp == 0 {
				logger.Error("🚨 [EPOCH BOUNDARY] CRITICAL: Epoch %d start timestamp is 0! "+
					"AdvanceEpoch must have failed to record the timestamp. error: %v", epoch, tsErr)
				epochTimestamp = 1 // Fallback to avoid division by zero crashes
			}
		} else {
			logger.Info("✅ [EPOCH BOUNDARY] Epoch %d strictly using authoritative stored timestamp: %d ms",
				epoch, epochTimestamp)
		}

		// Check if boundary block is fully synced to correctly handle NOMT queries below
		lastBlock := storage.GetLastBlockNumber()
		_, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(boundaryBlock)
		queryBlock = boundaryBlock
		if !ok {
			if lastBlock >= boundaryBlock {
				// The block was skipped due to fast-skip/catch-up, but we are past its height
				syncComplete = true
				logger.Info("ℹ️ [EPOCH BOUNDARY] Epoch %d: boundary block %d not in DB but height passed (lastBlock=%d). Marking syncComplete = true.", epoch, boundaryBlock, lastBlock)
			} else if lastBlock >= boundaryBlock-1 && boundaryBlock > 0 {
				// The block before the boundary block is committed.
				// Since validator set changes are finalized at the end of the previous epoch (lastBlock),
				// we can safely query the committee from lastBlock to allow the boundary block to be processed and synced!
				syncComplete = true
				queryBlock = lastBlock // Redirect the query to lastBlock
				logger.Info("ℹ️ [EPOCH BOUNDARY] Epoch %d: boundary block %d not yet synced, but parent %d is committed. Querying parent state to resolve chicken-and-egg deadlock.", epoch, boundaryBlock, lastBlock)
			} else {
				syncComplete = false
				logger.Warn("⚠️ [EPOCH BOUNDARY] Epoch %d: boundary block %d not yet synced. (sync pending, lastBlock=%d)", epoch, boundaryBlock, lastBlock)
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════
	// THREE-PRIORITY VALIDATOR LOOKUP
	// ═══════════════════════════════════════════════════════════════════
	//
	// PRIORITY 1: Epoch validator cache (NOMT-independent)
	//   → Cached during AdvanceEpoch, persisted in epoch_data_backup.json.
	//   → Survives snapshot restore even when NOMT knownKeys is empty.
	//
	// PRIORITY 2: NOMT live state at boundary block
	//   → Only works when NOMT knownKeys is populated (normal operation).
	//   → Falls through when sync is incomplete or NOMT is empty.
	//
	// PRIORITY 3: Return error → Rust fetches from network peers
	//   → Safety net for edge cases (very old snapshot, data corruption).
	// ═══════════════════════════════════════════════════════════════════
	var validators *pb.ValidatorInfoList
	var err error

	// PRIORITY 1: Check epoch validator cache
	if cachedValidators := rh.chainState.GetEpochValidators(epoch); cachedValidators != nil {
		cachedList := &pb.ValidatorInfoList{}
		if jErr := json.Unmarshal(cachedValidators, cachedList); jErr == nil && len(cachedList.Validators) > 0 {
			validators = cachedList
			logger.Info("✅ [EPOCH BOUNDARY] PRIORITY 1: Loaded %d validators from epoch cache (NOMT-independent) for epoch %d",
				len(validators.Validators), epoch)
			// Skip NOMT query — cache is authoritative
			goto epochBoundaryResponse
		} else {
			logger.Warn("⚠️ [EPOCH BOUNDARY] Epoch validator cache exists but failed to deserialize or is empty for epoch %d: %v",
				epoch, jErr)
		}
	}

	// PRIORITY 2: Query NOMT live state at boundary block
	if syncComplete || epoch == 0 {
		validators, err = rh.GetValidatorsAtBlockInternal(queryBlock)
		if err != nil {
			logger.Error("❌ [EPOCH BOUNDARY] PRIORITY 2: NOMT query failed at query block %d: %v", queryBlock, err)
		} else if len(validators.Validators) == 0 && epoch > 0 {
			logger.Warn("⚠️ [EPOCH BOUNDARY] PRIORITY 2: NOMT returned 0 validators for epoch %d (knownKeys likely empty after snapshot restore)", epoch)
			err = fmt.Errorf("NOMT returned 0 validators for epoch %d at query block %d (knownKeys likely empty). Rust MUST fallback to peers.", epoch, queryBlock)
		}
	} else {
		// Sync incomplete — boundary block not available
		lastBlock := storage.GetLastBlockNumber()
		logger.Error("❌ [EPOCH BOUNDARY] Boundary block %d NOT synced yet (lastBlock=%d). "+
			"REFUSING to return validators from non-boundary block to prevent committee mismatch → fork! "+
			"Checking epoch cache or Rust should retry.", boundaryBlock, lastBlock)
		err = fmt.Errorf(
			"boundary block %d not synced yet (lastBlock=%d, epoch=%d). "+
				"Cannot return committee from non-boundary block", boundaryBlock, lastBlock, epoch)
	}

	// PRIORITY 3: If all local sources failed, return error for Rust to fetch from peers
	if err != nil || validators == nil || len(validators.Validators) == 0 {
		errMsg := "unknown error"
		if err != nil {
			errMsg = err.Error()
		}
		logger.Error("❌ [EPOCH BOUNDARY] All local validator sources failed for epoch %d: %s. "+
			"Rust should fetch from network peers.", epoch, errMsg)
		return nil, fmt.Errorf("no validators available locally for epoch %d: %s", epoch, errMsg)
	}

epochBoundaryResponse:
	// 🔍 DIAGNOSTIC: Log detailed committee information
	logger.Info("📊 [EPOCH BOUNDARY] === UNIFIED COMMITTEE DATA FOR EPOCH %d ===", epoch)
	logger.Info("   📊 boundary_block: %d", boundaryBlock)
	logger.Info("   📊 epoch_timestamp_ms: %d (sync_complete=%v)", epochTimestamp, syncComplete)
	logger.Info("   📊 validator_count: %d", len(validators.Validators))
	logger.Info("📊 [EPOCH BOUNDARY] === END COMMITTEE DATA ===")

	logger.Info("✅ [EPOCH BOUNDARY] Returning epoch boundary data",
		"epoch", epoch,
		"timestamp_ms", epochTimestamp,
		"boundary_block", boundaryBlock,
		"validator_count", len(validators.Validators),
		"sync_complete", syncComplete)

	// Opportunistically cache validators if they came from NOMT (for future use/snapshots)
	if rh.chainState.GetEpochValidators(epoch) == nil && len(validators.Validators) > 0 {
		if serialized, sErr := json.Marshal(validators); sErr == nil {
			rh.chainState.SetEpochValidators(epoch, serialized)
			logger.Info("💾 [EPOCH BOUNDARY] Opportunistically cached %d validators for epoch %d", len(validators.Validators), epoch)
			if pErr := rh.chainState.SaveEpochDataSafe(); pErr != nil {
				logger.Warn("⚠️ [EPOCH BOUNDARY] Failed to persist opportunistic cache: %v", pErr)
			}
		}
	}

	// Load epoch_duration_seconds from genesis config (authoritative source for all nodes)
	var epochDurationSeconds uint64 = 900 // default 15 minutes
	if rh.genesisPath != "" {
		if genesisData, err := config.LoadGenesisData(rh.genesisPath); err == nil {
			if genesisData.Config.EpochDurationSeconds > 0 {
				epochDurationSeconds = genesisData.Config.EpochDurationSeconds
				logger.Info("✅ [EPOCH BOUNDARY] Loaded epoch_duration_seconds from genesis: %ds", epochDurationSeconds)
			}
		}
	}

	return &pb.EpochBoundaryData{
		Epoch:                 epoch,
		EpochStartTimestampMs: epochTimestamp,
		BoundaryBlock:         boundaryBlock,
		BoundaryGei:           boundaryGei,
		Validators:            validators.Validators,
		EpochDurationSeconds:  epochDurationSeconds,
	}, nil
}
