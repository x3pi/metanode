package account_state_db

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	// Assume these paths are correct for your project structure
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"

	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

type dirtyAccountEntry struct {
	addr  common.Address
	state types.AccountState
}

type marshalResult struct {
	address common.Address
	bytes   []byte
	err     error
}

var (
	keysToProcessPool = sync.Pool{
		New: func() interface{} { return make([]dirtyAccountEntry, 0, 5000) },
	}
	marshalResultsPool = sync.Pool{
		New: func() interface{} { return make([]marshalResult, 0, 5000) },
	}
	batchKeysPool = sync.Pool{
		New: func() interface{} { return make([][]byte, 0, 5000) },
	}
	batchValuesPool = sync.Pool{
		New: func() interface{} { return make([][]byte, 0, 5000) },
	}
	batchOldValuesPool = sync.Pool{
		New: func() interface{} { return make([][]byte, 0, 5000) },
	}
)

// Commit persists all dirty account states to the trie and the underlying database.
// It calculates the new root hash and updates the state database instance.
func (db *AccountStateDB) Commit() (common.Hash, error) {

	// 1. Apply dirty changes to the in-memory trie representation
	// IntermediateRoot handles locking muStruct for iterating and clearing dirtyAccounts.
	// intermediateHash, err := db.IntermediateRoot(false) // isLog = false
	// defer func() {
	// 	db.lockedFlag.Store(false) // CHANGED: Use atomic Store
	// }()
	// if err != nil {
	// 	logger.Error("Commit: Error applying changes during IntermediateRoot", "error", err)
	// 	// Attempt to discard changes to revert to the last known good state?
	// 	// Discard() acquires muStruct, which is fine as IntermediateRoot released it.
	// 	// However, discard might fail too.
	// 	// For now, return the error directly.
	// 	return common.Hash{}, fmt.Errorf("commit failed during IntermediateRoot: %w", err)
	// }

	if !db.lockedFlag.Load() {
		return common.Hash{}, errors.New("Commit: db.lockedFlag is not already locked")
	}
	// Lock the entire commit process to ensure atomicity
	db.muCommit.Lock()
	defer db.muCommit.Unlock()

	// ═══════════════════════════════════════════════════════════════
	// muTrie is already held by the preceding IntermediateRoot(true) call.
	// IntermediateRoot(false) below also skips locking. We hold muTrie
	// continuously through this entire Commit cycle.
	// ═══════════════════════════════════════════════════════════════

	// 1. Apply dirty changes to the in-memory trie representation
	// IntermediateRoot(false) skips its own muTrie lock since we hold it.
	intermediateHash, err := db.IntermediateRoot(false) // isLog = false
	defer func() {
		db.lockedFlag.Store(false) // CHANGED: Use atomic Store
	}()
	if err != nil {
		logger.Error("Commit: Error applying changes during IntermediateRoot", "error", err)
		// Attempt to discard changes to revert to the last known good state?
		// Discard() acquires muStruct, which is fine as IntermediateRoot released it.
		// However, discard might fail too.
		// For now, return the error directly.
		return common.Hash{}, fmt.Errorf("commit failed during IntermediateRoot: %w", err)
	}

	// At this point, db.trie (in memory) reflects the state matching intermediateHash,
	// and db.dirtyAccounts has been cleared.

	// ═══════════════════════════════════════════════════════════════
	// muTrie still held — protect trie.Commit + swap
	// ═══════════════════════════════════════════════════════════════

	// 2. Commit the in-memory trie to generate database nodes
	committedHash, nodeSet, oldKeys, err := db.trie.Commit(true)
	if err != nil {
		db.muTrie.Unlock()
		logger.Error("Commit: Error during trie Commit calculation", "error", err)
		return common.Hash{}, fmt.Errorf("trie Commit calculation failed: %w", err)
	}

	// Sanity check: Hash from applying updates should match hash from commit calculation.
	// NOTE: NomtStateTrie skips this check because NOMT computes the root hash only
	// during Commit() (not during Hash()). intermediateHash = old root, committedHash = new root.
	if _, isNomt := db.trie.(*p_trie.NomtStateTrie); !isNomt {
		if intermediateHash != committedHash {
			db.muTrie.Unlock()
			logger.Error("Commit: Root hash mismatch between IntermediateRoot and Commit calculation",
				"intermediate", intermediateHash, "commit", committedHash)
			return common.Hash{}, fmt.Errorf(
				"root hash mismatch after commit calculation (intermediate: %s, commit: %s)",
				intermediateHash, committedHash,
			)
		}
	}
	finalHash := committedHash

	// ═══════════════════════════════════════════════════════════════
	// GENESIS FIX (Apr 2026): For NOMT backend, CommitPayload() MUST be called
	// synchronously here BEFORE the trie swap (line ~192: db.trie = newTrie).
	//
	// ROOT CAUSE: trie.Commit(true) above sets pendingFinishedSession on the
	// CURRENT trie object. But this Commit() function creates a brand new trie
	// via NewStateTrie() and swaps it into db.trie, orphaning the old trie's
	// pendingFinishedSession. Without flushing here, genesis data is NEVER
	// written to NOMT's persistent storage, causing all nodes to return empty
	// state (balance=0, nonce=0, no BLS key) for genesis accounts.
	//
	// This only affects the non-pipeline Commit() path (used by genesis init).
	// Normal block commits use CommitPipeline() + PersistAsync() which handles
	// CommitPayload() correctly in the background worker.
	//
	// CommitPayload() is idempotent (returns nil if pendingFinishedSession is
	// already nil), so this is safe for non-NOMT backends too (they have no
	// pendingFinishedSession).
	// ═══════════════════════════════════════════════════════════════
	if nomtTrie, isNomt := db.trie.(*p_trie.NomtStateTrie); isNomt {
		if err := nomtTrie.CommitPayload(); err != nil {
			db.muTrie.Unlock()
			logger.Error("Commit: NOMT CommitPayload failed: %v", err)
			return common.Hash{}, fmt.Errorf("NOMT CommitPayload failed during Commit: %w", err)
		}
		logger.Debug("✅ [NOMT] CommitPayload flushed synchronously during Commit (genesis-safe)")
	}

	// 3. Handle old keys (optional)
	if len(oldKeys) > 0 {
		logger.Debug("Commit: Identified old keys to potentially prune", "count", len(oldKeys))
	}

	// 4. Persist the new trie nodes to the database
	// OPTIMIZATION: Also update lruCache with the newly committed `dirtyAccounts`
	// Since dirtyAccounts map was cleared inside IntermediateRoot, we rely on the `cloned` map
	// However, IntermediateRoot doesn't return the cloned map. Wait, we can iterate nodeSet?
	// No, nodeSet contains intermediate branch nodes, not the actual leaf state bytes.
	// Actually, `dirtyAccounts` is cleared in IntermediateRoot but its contents are applied to the trie.
	// The LRU cache works on `[]byte` values exactly as stored in the db.
	// The most reliable way is to let `Cache miss` repopulate the cache on the next block if we don't have the bytes here.
	// We CANNOT purge here because `Commit` doesn't invalidate old data, it only adds/updates.
	// Stale data in LRU cache doesn't matter for *this* block's committed accounts because their updated bytes are retrieved on the next `Get`.
	// Oh wait, if we read from `lruCache` first, and an account was updated in *this block*,
	// the `lruCache` will have the OLD `[]byte` value for the NEXT block!
	// We MUST purge the LRU cache OR update it with the new values.
	// Since getting the new values is hard here (they are marshaled inside IntermediateRoot), let's just purge the LRU cache on every commit to be 100% safe from stale reads.
	// But purging on every commit defeats the purpose of the cache across blocks!
	// Wait, we can safely update it. We need the marshaled bytes of `dirtyAccounts`.

	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		batch := make([][2][]byte, 0, len(nodeSet.Nodes))
		for _, node := range nodeSet.Nodes {
			if node.Hash == (common.Hash{}) {
				logger.Error("Commit: Trying to save node with empty hash, skipping.")
				continue
			}
			batch = append(batch, [2][]byte{node.Hash.Bytes(), node.Blob})
		}

		if len(batch) > 0 {
			logger.Debug("Commit: Writing batch to DB", "num_nodes", len(batch))
			err := db.db.BatchPut(batch)
			if err != nil {
				db.muTrie.Unlock()
				logger.Error("Commit: Error during DB BatchPut", "error", err)
				return common.Hash{}, fmt.Errorf("DB BatchPut failed: %w", err)
			}
		} else {
			logger.Debug("Commit: No new nodes generated by trie commit.")
		}

	} else {
		logger.Debug("Commit: No new nodes to write to DB (nodeSet is nil or empty)")
	}

	// Prepare network batch using the same logic as CommitPipeline to handle all backends
	var networkBatch [][2][]byte
	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		networkBatch = make([][2][]byte, 0, len(nodeSet.Nodes))
		for _, n := range nodeSet.Nodes {
			if n.Hash != (common.Hash{}) {
				networkBatch = append(networkBatch, [2][]byte{n.Hash.Bytes(), n.Blob})
			}
		}
	} else {
		networkBatch = db.trie.GetCommitBatch()
	}

	// Build AccountBatch for network replication.
	var accountBatchData []byte
	if config.ConfigApp != nil && config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
		if len(networkBatch) > 0 {
			data, serErr := storage.SerializeBatch(networkBatch)
			if serErr != nil {
				logger.Error("Commit: Failed to serialize commit batch for network transfer", "error", serErr)
			} else {
				accountBatchData = data
				logger.Debug("Commit: Serialized account batch for network transfer", "size_bytes", len(data), "entries", len(networkBatch))
			}
		}
		db.SetAccountBatch(accountBatchData)
	}

	// 5. Create a *new* trie instance reflecting the committed state.
	newTrie, err := p_trie.NewStateTrie(finalHash, db.db, true)
	if err != nil {
		db.muTrie.Unlock()
		logger.Error("Commit: Failed to create new trie instance after DB write", "hash", finalHash, "error", err)
		return common.Hash{}, fmt.Errorf("failed to load trie for new root %s after commit: %w", finalHash, err)
	}

	// 6. Update the live trie reference and origin hash
	db.trie = newTrie
	db.originRootHash = finalHash

	db.muTrie.Unlock()
	// --- Release structural lock ---
	return finalHash, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// PIPELINE COMMIT — Split persist out of critical path
// ═══════════════════════════════════════════════════════════════════════════════

// PipelineCommitResult holds data needed for async persistence after CommitPipeline.
// The caller should pass this to PersistAsync() in a background goroutine.
type PipelineCommitResult struct {
	FinalHash      common.Hash
	Batch          [][2][]byte      // node hash → blob pairs for LevelDB BatchPut
	AccountBatch   []byte           // serialized batch for network transfer to sub-nodes
	OldKeys        [][]byte         // old trie keys for potential pruning
	Trie           p_trie.StateTrie // The trie instance after Commit, to be re-used
	PersistChannel chan struct{}    // Channel created for THIS block's persist async
}

// CommitPipeline performs the fast, synchronous phase of commit:
//  1. IntermediateRoot(false) → return cached hash, release lockedFlag
//  2. trie.Commit(true) → generate nodeSet (creates internal copy, fast)
//  3. Verify intermediate == committed hash
//  4. Serialize batch for network transfer
//  5. Release muTrie IMMEDIATELY → unblocks next block's PreloadAccounts/reads
//
// FORK-SAFETY: stateRoot is still computed from trie.Hash() (unchanged).
// The original trie remains valid for Get() after trie.Commit() because
// Commit() operates on an internal copy and does NOT modify the original trie's root.
//
// The caller MUST call PersistAsync() with the returned result to eventually
// persist nodes to LevelDB and swap the trie reference.
func (db *AccountStateDB) CommitPipeline() (*PipelineCommitResult, error) {
	if !db.lockedFlag.Load() {
		return nil, errors.New("CommitPipeline: db.lockedFlag is not already locked")
	}
	db.muCommit.Lock()
	defer db.muCommit.Unlock()

	// ═══════════════════════════════════════════════════════════════
	// Phase 1: Get the already-computed hash (fast — no trie iteration)
	// ═══════════════════════════════════════════════════════════════
	intermediateHash, err := db.IntermediateRoot(false)
	defer func() {
		db.lockedFlag.Store(false)
	}()
	if err != nil {
		logger.Error("CommitPipeline: Error during IntermediateRoot(false)", "error", err)
		return nil, fmt.Errorf("commit pipeline failed during IntermediateRoot: %w", err)
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 2: Generate nodeSet (trie.Commit creates a copy internally)
	// The original trie object is NOT modified — it remains valid for Get()
	// ═══════════════════════════════════════════════════════════════

	startTrieCommit := time.Now()
	committedHash, nodeSet, oldKeys, err := db.trie.Commit(true)
	trieCommitDuration := time.Since(startTrieCommit)

	if err != nil {
		db.muTrie.Unlock()
		logger.Error("CommitPipeline: Error during trie.Commit()", "error", err)
		return nil, fmt.Errorf("trie Commit failed: %w", err)
	}

	if trieCommitDuration > 10*time.Millisecond {
		logger.Debug("[PERF-COMMIT] trie.Commit(true) took: %v", trieCommitDuration)
	}

	// Sanity check
	if intermediateHash != committedHash {
		if _, isNomt := db.trie.(*p_trie.NomtStateTrie); !isNomt {
			db.muTrie.Unlock()
			logger.Error("CommitPipeline: Root hash mismatch",
				"intermediate", intermediateHash, "committed", committedHash)
			return nil, fmt.Errorf(
				"root hash mismatch (intermediate: %s, committed: %s)",
				intermediateHash, committedHash,
			)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 3: Prepare batch data for async persist + network transfer
	// ═══════════════════════════════════════════════════════════════
	var batch [][2][]byte
	var accountBatchData []byte

	// ═══════════════════════════════════════════════════════════════
	// FIX: Handle both MPT (nodeSet) and Flat (commitBatch) structures
	// ═══════════════════════════════════════════════════════════════
	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		batch = make([][2][]byte, 0, len(nodeSet.Nodes))
		hasRoot := false
		for _, n := range nodeSet.Nodes {
			if n.Hash == (common.Hash{}) {
				continue
			}
			if n.Hash == committedHash {
				hasRoot = true
			}
			batch = append(batch, [2][]byte{n.Hash.Bytes(), n.Blob})
		}
		if hasRoot {
			logger.Debug("✅ [TRIE] CommitPipeline: Batch INCLUDES root hash %s!", committedHash.Hex())
		}
	} else {
		// FlatStateTrie doesn't use NodeSet. Extract directly from GetCommitBatch.
		flatBatch := db.trie.GetCommitBatch()
		if len(flatBatch) > 0 {
			batch = flatBatch
		} else {
			logger.Debug("➖ [TRIE] CommitPipeline: Batch is empty for root %s", committedHash.Hex())
		}
	}

	// Build AccountBatch for network replication.
	// We MUST use the 'batch' variable we just constructed above (which contains either MPT nodes or Flat entries).
	// We CANNOT call db.trie.GetCommitBatch() again because for FlatStateTrie it is a one-shot read
	// that clears its internal buffer, returning nil on the second call and breaking Sub-node state replication.
	if config.ConfigApp != nil && config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
		if len(batch) > 0 {
			// DEBUG MASTER COMMIT BATCH
			logger.Debug("[DEBUG MASTER DB] CommitPipeline: serializing %d entries for network transfer", len(batch))
			startSerialize := time.Now()
			data, serErr := storage.SerializeBatch(batch)
			serializeDuration := time.Since(startSerialize)
			if serializeDuration > 10*time.Millisecond {
				logger.Debug("[PERF-COMMIT] SerializeBatch took: %v for %d entries", serializeDuration, len(batch))
			}
			if serErr != nil {
				logger.Error("CommitPipeline: Failed to serialize commit batch", "error", serErr)
			} else {
				accountBatchData = data
			}
		}
	}

	// Store accountBatch for network transfer (same as original Commit)
	// ALWAYS call SetAccountBatch (even if nil) to clear any leftover batch 
	// from the previous block, ensuring we don't leak stale data to Sub nodes.
	db.SetAccountBatch(accountBatchData)

	// ═══════════════════════════════════════════════════════════════
	// Phase 4: RELEASE muTrie IMMEDIATELY
	// The original trie is still valid for Get() — trie.Commit() only
	// modified an internal copy. Next block can start PreloadAccounts
	// and getOrCreateAccountState right away.
	//
	// FORK-SAFETY: Create a NEW persistReady channel BEFORE releasing muTrie.
	// This channel will be closed by PersistAsync() AFTER the trie swap,
	// ensuring the next block's IntermediateRoot(true) waits for the swap.
	// ═══════════════════════════════════════════════════════════════
	newPersistReady := make(chan struct{}) // NEW unclosed channel → next IntermediateRoot will wait
	db.persistReady = newPersistReady
	db.muTrie.Unlock()
	logger.Debug("CommitPipeline: muTrie released early, persistReady gate set, next block can proceed")

	var persistBatch [][2][]byte
	if _, isNomt := db.trie.(*p_trie.NomtStateTrie); !isNomt {
		// Only persist to block storage if NOT using NOMT (i.e. MPT or FlatTrie).
		// NOMT handles its own DB persistence via CommitPayload.
		persistBatch = batch
	} else {
		logger.Debug("➖ [TRIE] CommitPipeline: Skipping PebbleDB persistBatch for NOMT (handled by CommitPayload)")
	}

	return &PipelineCommitResult{
		FinalHash:      committedHash,
		Batch:          persistBatch,
		AccountBatch:   accountBatchData,
		OldKeys:        oldKeys,
		Trie:           db.trie, // Pass the trie along
		PersistChannel: newPersistReady,
	}, nil
}

// PersistAsync performs the slow, background phase of commit:
//  1. BatchPut nodeSet to LevelDB (disk I/O)
//  2. Create new trie from committed hash
//  3. Swap trie reference and update originRootHash
//  4. Close persistReady to unblock the next block's IntermediateRoot
//  5. (NOMT) Run CommitPayload to flush changes to disk asynchronously.
//
// This method is designed to be called from a background goroutine.
// It briefly re-acquires muTrie for the trie swap (step 2-3).
//
// IMPORTANT: Between CommitPipeline() releasing muTrie and PersistAsync()
// completing the swap, the old trie (with all in-memory updates from
// IntermediateRoot) is still the live reference. Reads from it are safe
// because dirtyAccounts was already cleared and lruCache was updated.
func (db *AccountStateDB) PersistAsync(result *PipelineCommitResult) error {
	if result == nil {
		return nil // Nothing to persist (e.g., no state changes)
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 1: Persist to LevelDB (slow, disk I/O — this is the part
	// we moved out of the critical path)
	// ═══════════════════════════════════════════════════════════════
	if len(result.Batch) > 0 {
		if err := db.db.BatchPut(result.Batch); err != nil {
			logger.Error("PersistAsync: BatchPut failed", "error", err)
			return fmt.Errorf("PersistAsync BatchPut failed: %w", err)
		}
		logger.Debug("PersistAsync: BatchPut completed", "nodes", len(result.Batch))
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 2: Swamp the already warmed trie instance. Trie object is returned
	// from Pipeline Commit, preventing us from creating a cold trie and losing PreWarm cache.
	// ═══════════════════════════════════════════════════════════════

	db.muTrie.Lock()
	if result.Trie != nil {
		db.trie = result.Trie
	} else {
		// Fallback for edge cases where Trie is not provided
		newTrie, err := p_trie.NewStateTrie(result.FinalHash, db.db, true)
		if err != nil {
			db.muTrie.Unlock()
			logger.Error("PersistAsync: Failed to create new trie", "hash", result.FinalHash, "error", err)
			return fmt.Errorf("PersistAsync: failed to load trie for root %s: %w", result.FinalHash, err)
		}
		db.trie = newTrie
	}
	db.originRootHash = result.FinalHash
	db.muTrie.Unlock()

	logger.Debug("PersistAsync: Trie swapped to new root and persistReady signaled", "hash", result.FinalHash)

	// ═══════════════════════════════════════════════════════════════
	// Step 3: IMMEDIATE PERSIST GATE UNBLOCK
	// Signal that trie swap is complete. Ensure this always runs BEFORE
	// CommitPayload so the next block's IntermediateRoot(true) is unblocked
	// and EVM execution can overlap with disk I/O!
	// ═══════════════════════════════════════════════════════════════
	if result.PersistChannel != nil {
		close(result.PersistChannel)
	} else {
		close(db.persistReady)
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 4: BACKGROUND NOMT DISK FLUSH (CommitPayload)
	// With the gate opened, the next block proceeds at full speed.
	// We use nomtCommitGuard to serialize this background flush with the next
	// block's BatchUpdateWithCachedOldValues.
	// ═══════════════════════════════════════════════════════════════
	if nomtTrie, isNomt := result.Trie.(*p_trie.NomtStateTrie); isNomt {
		// Acquire guard to prevent concurrent modification during flush
		<-db.nomtCommitGuard
		defer func() {
			db.nomtCommitGuard <- struct{}{} // Release guard
		}()

		if err := nomtTrie.CommitPayload(); err != nil {
			logger.Error("PersistAsync: NOMT CommitPayload failed", "error", err)
			return fmt.Errorf("PersistAsync: NOMT CommitPayload failed: %w", err)
		}
	}

	return nil
}

// --- Hàm với logging chi tiết ---
func (db *AccountStateDB) IntermediateRoot(isLockProcess ...bool) (common.Hash, error) {
	var lockProcess bool
	if len(isLockProcess) > 0 {
		lockProcess = isLockProcess[0]
	} else {
		lockProcess = true // Gán mặc định là true
	}
	// Nếu isLockProcess là true và mutex đang bị lock, báo lỗi
	if lockProcess {
		// Thử lock mutex, nếu mutex đã bị lock thì sẽ báo lỗi
		if db.lockedFlag.Load() {
			errMsg := "IntermediateRoot (lockProcess=true): db.lockedFlag is already locked"
			log.Println("error:", errMsg)
			return common.Hash{}, errors.New(errMsg)
		}
		db.lockedFlag.Store(true) // CHANGED: Use atomic Store

		// ═══════════════════════════════════════════════════════════════
		// PIPELINE OVERLAP: persistReady wait has been MOVED to AFTER the
		// CPU-bound marshal phase (below), just before BatchUpdate which
		// actually reads/writes the trie. This allows marshal (10-50ms)
		// to overlap with the previous block's PersistAsync disk I/O
		// (200-1000ms), dramatically reducing block creation time under
		// large state. See "DEFERRED PERSIST GATE" comment below.
		//
		// FORK-SAFETY: Still guaranteed because we wait on persistReady
		// before ANY trie access (BatchUpdate/Hash). The marshal phase
		// only reads from dirtyAccounts (sync.Map) and lruCache, neither
		// of which is affected by PersistAsync's trie swap.
		// ═══════════════════════════════════════════════════════════════

		// Lock mutex thành công
		logger.Debug("Structure lock acquired, processing locked")
	} else {
		// ═══════════════════════════════════════════════════════════════
		// CRITICAL FORK-SAFETY: When called from Commit (lockProcess=false),
		// DO NOT iterate or process dirtyAccounts at all.
		//
		// IntermediateRoot(true) already:
		// 1. Applied ALL dirty accounts to the in-memory trie
		// 2. Cleared dirtyAccounts immediately after
		// 3. Computed and cached the correct hash
		//
		// If we iterate dirtyAccounts here, we risk picking up entries
		// from the NEXT block's PreloadAccounts (which may have already
		// started) and applying them to THIS block's trie → corrupted
		// state → different results on different nodes → FORK.
		//
		// So we just return the already-computed hash and release the lock.
		// ═══════════════════════════════════════════════════════════════
		if !db.lockedFlag.Load() {
			errMsg := "error: db.lockedFlag is not locked"
			log.Println(errMsg)
			return common.Hash{}, errors.New(errMsg)
		}
		defer func() {
			db.lockedFlag.Store(false)
			logger.Debug("Structure lock released, processing unlocked")
		}()

		// Just return the current trie hash — all updates were already applied
		currentHash := db.trie.Hash()
		logger.Debug("IntermediateRoot(false): returning cached hash (no dirty processing)", "hash", currentHash)
		return currentHash, nil
	}

	if db.trie == nil {
		logger.Error("Trie is nil, cannot proceed")
		return common.Hash{}, errors.New("cannot calculate intermediate root, trie is nil")
	}

	logger.Debug("Initial state", "originRootHash", db.originRootHash)

	var (
		updateErr     error
		processedKeys int  = 0
		hasChanges    bool = false
	)

	keysToProcess := keysToProcessPool.Get().([]dirtyAccountEntry)[:0]
	marshalResults := marshalResultsPool.Get().([]marshalResult)[:0]
	batchKeys := batchKeysPool.Get().([][]byte)[:0]
	batchValues := batchValuesPool.Get().([][]byte)[:0]
	var batchOldValues [][]byte
	if db.isFlatTrie {
		batchOldValues = batchOldValuesPool.Get().([][]byte)[:0]
	}

	defer func() {
		// Release slices back to pools (limit capacity to prevent memory leaks if spiked)
		if cap(keysToProcess) < 20000 {
			keysToProcessPool.Put(keysToProcess)
		}
		if cap(marshalResults) < 20000 {
			marshalResultsPool.Put(marshalResults)
		}
		if cap(batchKeys) < 20000 {
			for i := range batchKeys {
				batchKeys[i] = nil // Avoid pinning memory
			}
			batchKeysPool.Put(batchKeys)
		}
		if cap(batchValues) < 20000 {
			for i := range batchValues {
				batchValues[i] = nil
			}
			batchValuesPool.Put(batchValues)
		}
		if db.isFlatTrie && batchOldValues != nil && cap(batchOldValues) < 20000 {
			for i := range batchOldValues {
				batchOldValues[i] = nil
			}
			batchOldValuesPool.Put(batchOldValues)
		}
	}()

	// Bước 1: Thu thập keys (vì sync.Map.Range không cho biết trước số lượng)
	db.dirtyAccounts.Range(func(key, value interface{}) bool {
		address, ok1 := key.(common.Address)
		state, ok2 := value.(types.AccountState)
		if !ok1 || !ok2 || state == nil {
			logger.Warn("Skipping invalid entry in dirtyAccounts", "keyType", fmt.Sprintf("%T", key), "valueType", fmt.Sprintf("%T", value))
			return true // continue
		}

		// FORK-SAFE OPTIMIZATION: ONLY process accounts that were actually modified.
		// PreloadAccounts sets them up, but if they are read-only (e.g. signature check),
		// we should not unnecessarily recalculate their trie branch.
		if !state.IsDirty() {
			return true // Skip this account, it wasn't modified
		}

		keysToProcess = append(keysToProcess, dirtyAccountEntry{
			addr:  address,
			state: state,
		})
		return true
	})

	// CRITICAL FORK-SAFETY: Sort the keys before updating the trie.
	// sync.Map.Range iterates in random order. Updating the Merkle Patricia Trie
	// with the same keys but in different orders causes structural differences
	// (different branches, splits) which completely changes the final AccountStatesRoot
	// and causes forks between nodes. Sorting guarantees deterministic trie updates.
	slices.SortFunc(keysToProcess, func(a, b dirtyAccountEntry) int {
		return bytes.Compare(a.addr[:], b.addr[:])
	})

	totalDirty := len(keysToProcess)
	logger.Debug("Starting update from dirtyAccounts", "count", totalDirty)

	// ═══════════════════════════════════════════════════════════════
	// DEBUG: Log chi tiết từng account bị commit vào trie
	// So sánh log này giữa các node để tìm account nào bị lệch
	// ═══════════════════════════════════════════════════════════════
	if totalDirty > 0 {
		logger.Info("🔍 [TRIE-COMMIT-DEBUG] Committing %d dirty accounts to trie:", totalDirty)
		for _, entry := range keysToProcess {
			as := entry.state
			if as != nil {
				logger.Info("  📝 [TRIE-COMMIT-DEBUG] addr=%s nonce=%d balance=%s lastHash=%s",
					entry.addr.Hex(),
					as.Nonce(),
					as.Balance().String(),
					as.LastHash().Hex()[:16],
				)
			}
		}
	}

	if totalDirty > 0 {
		hasChanges = true
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 1.5: PARALLEL MARSHAL
	// Marshaling AccountState to []byte is CPU bound. Do this concurrently
	// before acquiring the exclusive muTrie.Lock().
	// ═══════════════════════════════════════════════════════════════
	// ═══════════════════════════════════════════════════════════════

	// Ensure marshalResults has the correct length before parallel mapping
	if cap(marshalResults) < totalDirty {
		marshalResults = make([]marshalResult, totalDirty) // fallback allocation if pool was too small
	} else {
		marshalResults = marshalResults[:totalDirty] // extend to full length
	}

	if totalDirty > 0 {
		startMarshal := time.Now()
		var wg sync.WaitGroup
		// TPS OPT Phase 5: Use runtime.NumCPU() instead of hardcoded 32.
		// 32 goroutines cause scheduling overhead when data chunks < 32.
		// Cap at 24 to avoid hyperthreading contention on most servers.
		numWorkers := runtime.NumCPU()
		if numWorkers > 24 {
			numWorkers = 24
		}
		if totalDirty < numWorkers {
			numWorkers = totalDirty
		}
		chunkSize := (totalDirty + numWorkers - 1) / numWorkers

		for i := 0; i < numWorkers; i++ {
			start := i * chunkSize
			end := start + chunkSize
			if start >= totalDirty {
				break
			}
			if end > totalDirty {
				end = totalDirty
			}

			wg.Add(1)
			go func(startIdx, endIdx int) {
				defer wg.Done()
				for j := startIdx; j < endIdx; j++ {
					entry := keysToProcess[j]
					addr := entry.addr
					as := entry.state
					if as == nil {
						marshalResults[j] = marshalResult{
							address: addr,
							err:     fmt.Errorf("missing or nil account state for %s", addr.Hex()),
						}
						continue
					}

					b, err := as.Marshal()
					marshalResults[j] = marshalResult{
						address: addr,
						bytes:   b,
						err:     err,
					}
				}
			}(start, end)
		}
		wg.Wait()
		marshalDuration := time.Since(startMarshal)
		if marshalDuration > 10*time.Millisecond {
			logger.Debug("[PERF] IntermediateRoot ParallelMarshal: %v (%d keys)", marshalDuration, totalDirty)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// PREPARE BATCH: Build keys/values from marshal results BEFORE lock.
	// Also update LRU cache here (OPTIMIZATION: previously done under muTrie.Lock).
	// This allows us to pre-warm the trie while NOT holding muTrie.
	//
	// TPS OPT PHASE 2: Also collect old LRU values for FlatStateTrie.
	// The LRU cache contains pre-commit serialized bytes — exactly what
	// FlatStateTrie needs for bucket hash computation (old contribution).
	// ═══════════════════════════════════════════════════════════════
	// batchKeys and batchValues slices are pre-allocated via sync.Pool above.
	for _, res := range marshalResults {
		if res.err != nil {
			logger.Error("Marshal error for %s: %v", res.address.Hex(), res.err)
			updateErr = fmt.Errorf("marshal error for %s: %w", res.address.Hex(), res.err)
			break
		}

		// Collect old value from LRU cache BEFORE updating it (Phase 2)
		if db.isFlatTrie {
			db.lruMu.RLock()
			oldData, ok := db.lruCache[res.address]
			if !ok {
				oldData, ok = db.lruCacheOld[res.address]
			}
			db.lruMu.RUnlock()

			if ok {
				batchOldValues = append(batchOldValues, oldData)
			} else {
				// BUG FIX: If not in LRU cache (e.g. cache evicted or PreloadAccounts hit loadedAccounts instead),
				// MUST fetch the original bytes directly from the trie. Otherwise NOMT receives nil
				// (assuming it's a new account), which corrupts the internal Merkle tree state and causes a fork!
				var trieOldData []byte
				if db.trie != nil {
					trieOldData, _ = db.trie.Get(res.address.Bytes())
				}
				if len(trieOldData) == 0 {
					batchOldValues = append(batchOldValues, nil) // true new account, no old value
				} else {
					batchOldValues = append(batchOldValues, trieOldData)
				}
			}
		}

		batchKeys = append(batchKeys, res.address.Bytes())
		batchValues = append(batchValues, res.bytes)

		// OPTIMIZATION: Update LRU cache HERE (before lock) instead of after BatchUpdate.
		// This reduces critical section time under muTrie.Lock.
		if db.lruCache != nil {
			db.lruMu.Lock()
			db.lruCache[res.address] = res.bytes
			db.lruMu.Unlock()
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// TPS OPT PHASE 3: FlatStateTrie-aware lock strategy.
	//
	// FlatStateTrie.BatchUpdate() has its own internal RWMutex and is
	// fully thread-safe. Running it OUTSIDE muTrie.Lock reduces the
	// critical section from ~300ms (PreWarm+BatchUpdate+Hash+clear)
	// to ~10ms (Hash+clear only).
	//
	// This allows the next block's PreloadAccounts to overlap with
	// BatchUpdate — eliminating 200-400ms idle time per block.
	//
	// MPT trie is NOT thread-safe for writes, so it still needs the
	// full muTrie.Lock around PreWarm + BatchUpdate.
	// ═══════════════════════════════════════════════════════════════

	if db.isFlatTrie {
		// ─── THREAD-SAFE TRIE PATH: BatchUpdate OUTSIDE muTrie.Lock ───────
		// Both FlatStateTrie and NomtStateTrie support this path.

		// ═══════════════════════════════════════════════════════════════
		// DEFERRED PERSIST GATE (MOVED TO CommitPipeline):
		// Since we now use BatchUpdateWithCachedOldValues which does NOT
		// touch C++ or read from DB, we no longer need to wait for
		// persistReady here for Flat/NOMT tries. This removes the
		// block generation bottleneck completely, allowing TPS to soar.
		// ═══════════════════════════════════════════════════════════════

		if updateErr == nil && len(batchKeys) > 0 {
			startBatch := time.Now()
			// TPS OPT PHASE 2: Use BatchUpdateWithCachedOldValues to skip DB reads
			// Try FlatStateTrie first, then NomtStateTrie
			if flatTrie, ok := db.trie.(*p_trie.FlatStateTrie); ok {
				if err := flatTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, batchOldValues); err != nil {
					logger.Error("BatchUpdateWithCachedOldValues failed: %v", err)
					updateErr = fmt.Errorf("trie BatchUpdateWithCachedOldValues error: %w", err)
				}
			} else if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
				// ═══════════════════════════════════════════════════════════════
				// BACKGROUND FLUSH GUARD
				// Wait for any running CommitPayload to finish before applying new state.
				// This protects NOMT's internal structures during concurrent I/O flush.
				// ═══════════════════════════════════════════════════════════════
				guardStart := time.Now()
				<-db.nomtCommitGuard
				guardWait := time.Since(guardStart)
				if guardWait > 5*time.Millisecond {
					logger.Debug("[PERF] IntermediateRoot guarded wait for CommitPayload: %v", guardWait)
				}

				if err := nomtTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, batchOldValues); err != nil {
					logger.Error("BatchUpdateWithCachedOldValues (NOMT) failed: %v", err)
					updateErr = fmt.Errorf("trie BatchUpdateWithCachedOldValues error: %w", err)
				}
				
				// Release guard immediately after update
				db.nomtCommitGuard <- struct{}{}
			} else {
				// Fallback: generic BatchUpdate
				if err := db.trie.BatchUpdate(batchKeys, batchValues); err != nil {
					logger.Error("BatchUpdate (fallback) failed: %v", err)
					updateErr = fmt.Errorf("trie BatchUpdate error: %w", err)
				}
			}
			logger.Debug("[PERF] IntermediateRoot BatchUpdate (thread-safe, lock-free, cached-old): %v (%d keys)", time.Since(startBatch), len(batchKeys))
		}

		if updateErr != nil {
			logger.Error("Failed during dirtyAccounts update loop", "error", updateErr, "processedBeforeError", processedKeys)
			return common.Hash{}, updateErr
		}

		// DEFERRED PERSIST GATE: Wait for the previous block's trie swap
		// to complete before we lock the trie and modify its structural state.
		// Since PersistAsync now closes persistReady BEFORE CommitPayload, this wait
		// is nearly instantaneous.
		persistWaitStart := time.Now()
		<-db.persistReady
		persistWaitDuration := time.Since(persistWaitStart)
		if persistWaitDuration > 5*time.Millisecond {
			logger.Debug("[PERF] IntermediateRoot DEFERRED persistReady wait: %v", persistWaitDuration)
		}

		// Only lock for Hash() + clear maps (minimal critical section ~10ms)
		db.muTrie.Lock()
	} else {
		// ─── MPT PATH: Full lock for PreWarm + BatchUpdate ─────────

		// DEFERRED PERSIST GATE (MPT path): same as FlatTrie path above.
		// CRITICAL FORK FIX: ALL trie types must wait (see comment above).
		persistWaitStart := time.Now()
		<-db.persistReady
		persistWaitDuration := time.Since(persistWaitStart)
		if persistWaitDuration > 5*time.Millisecond {
			logger.Debug("[PERF] IntermediateRoot DEFERRED persistReady wait (MPT): %v", persistWaitDuration)
		}

		db.muTrie.Lock()

		if updateErr == nil && len(batchKeys) > 0 {
			startPreWarm := time.Now()
			db.trie.PreWarm(batchKeys)
			preWarmDuration := time.Since(startPreWarm)
			if preWarmDuration > 10*time.Millisecond {
				logger.Debug("[PERF] IntermediateRoot PreWarm: %v (%d keys)", preWarmDuration, len(batchKeys))
			}
		}

		if updateErr == nil && len(batchKeys) > 0 {
			startBatch := time.Now()
			if err := db.trie.BatchUpdate(batchKeys, batchValues); err != nil {
				logger.Error("BatchUpdate failed: %v", err)
				updateErr = fmt.Errorf("trie BatchUpdate error: %w", err)
			}
			logger.Debug("[PERF] IntermediateRoot BatchUpdate: %v (%d keys)", time.Since(startBatch), len(batchKeys))
		}

		if updateErr != nil {
			db.muTrie.Unlock()
			logger.Error("Failed during dirtyAccounts update loop, clearing dirty map", "error", updateErr, "processedBeforeError", processedKeys)
			return common.Hash{}, updateErr
		}
	}

	logger.Debug("Finished processing dirtyAccounts", "processedTotal", processedKeys)

	// ═══════════════════════════════════════════════════════════════
	// CRITICAL FORK-SAFETY: Clear dirtyAccounts IMMEDIATELY after
	// applying to the trie, while we hold muTrie lock.
	//
	// TPS OPT PHASE 4: Keep loadedAccounts across blocks.
	// loadedAccounts contains read-only accounts that were NOT modified.
	// Their trie data hasn't changed, so they're still valid for the
	// next block's reads. Keeping them saves re-reading from trie/LRU
	// for hot accounts (~50-100ms per block with 20k+ accounts).
	// Only dirtyAccounts must be cleared (they were applied to the trie).
	// ═══════════════════════════════════════════════════════════════
	// TPS OPT Phase 2: In-place clear instead of reassignment.
	// `db.dirtyAccounts = sync.Map{}` creates a new map and drops the old one,
	// causing GC to scan+collect 30K+ interface{} pointers per block.
	// Range+Delete reuses the same map structure, avoiding the GC spike.
	db.dirtyAccounts.Range(func(key, _ interface{}) bool {
		db.dirtyAccounts.Delete(key)
		return true
	})

	// TPS OPT Phase 1: Bounded eviction for loadedAccounts.
	// loadedAccounts grows unbounded across blocks (~10-30K new entries/block).
	// After 10 blocks, clear it to cap memory at ~300K entries max.
	// This prevents GC pressure from scanning millions of stale interface{} entries.
	// FORK-SAFETY: loadedAccounts is a read-only cache — clearing it only
	// causes re-reads from LRU/trie, which produce identical values.
	db.blocksSinceLoadedClear++
	if db.blocksSinceLoadedClear >= 10 {
		db.loadedAccounts.Range(func(key, _ interface{}) bool {
			db.loadedAccounts.Delete(key)
			return true
		})
		db.blocksSinceLoadedClear = 0
		logger.Debug("[TPS-OPT] Cleared loadedAccounts cache (bounded eviction every 10 blocks)")
	}

	var newHash common.Hash
	if hasChanges {
		// CRITICAL FIX: Must compute the actual trie hash NOW so that
		// ProcessTransactions returns the correct post-state root for the block header.
		startHash := time.Now()
		newHash = db.trie.Hash()
		hashDuration := time.Since(startHash)
		if hashDuration > 10*time.Millisecond {
			logger.Debug("[PERF] IntermediateRoot Hash: %v (%d dirty)", hashDuration, totalDirty)
		}
		logger.Debug("IntermediateRoot(true): computed new hash after applying %d dirty accounts: %s -> %s",
			totalDirty, db.originRootHash.Hex(), newHash.Hex())
	} else {
		newHash = db.originRootHash
		logger.Debug("No changes detected in dirtyAccounts, intermediate hash remains origin hash", "hash", newHash)
	}

	// NOTE: muTrie stays LOCKED here intentionally.
	// Commit() will release muTrie after trie swap is complete.

	return newHash, nil
}
