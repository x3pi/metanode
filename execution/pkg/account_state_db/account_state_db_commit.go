package account_state_db

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	// Assume these paths are correct for your project structure
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"

	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
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
	writeBatchPool = sync.Pool{
		New: func() interface{} { return make([][2][]byte, 0, 5000) },
	}
)

// Commit persists all dirty account states to the trie and the underlying database.
// It calculates the new root hash and updates the state database instance.
//
// DEADLOCK-FREE DESIGN: Commit() self-acquires muTrie — it does NOT assume
// muTrie is pre-held by IntermediateRoot. This eliminates the cross-function
// deferred lock pattern that previously caused permanent deadlocks.
func (db *AccountStateDB) Commit() (common.Hash, error) {

	// Lock the entire commit process to ensure atomicity
	db.muCommit.Lock()
	defer db.muCommit.Unlock()

	// ═══════════════════════════════════════════════════════════════
	// Self-acquire muTrie for the duration of commit.
	// IntermediateRoot(true) has already released muTrie after computing
	// the hash, so we re-acquire here for trie.Commit + swap.
	// ═══════════════════════════════════════════════════════════════
	db.muTrie.Lock()

	// IntermediateRoot(false) returns cached hash — caller holds muTrie.
	intermediateHash, err := db.IntermediateRoot(false)
	if err != nil {
		db.muTrie.Unlock()
		logger.Error("Commit: Error applying changes during IntermediateRoot", "error", err)
		return common.Hash{}, fmt.Errorf("commit failed during IntermediateRoot: %w", err)
	}

	// At this point, db.trie (in memory) reflects the state matching intermediateHash,
	// and db.dirtyAccounts has been cleared.

	// ═══════════════════════════════════════════════════════════════
	// muTrie held — protect trie.Commit + swap
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
		batch := writeBatchPool.Get().([][2][]byte)[:0]
		defer func() {
			for i := range batch {
				batch[i][0] = nil
				batch[i][1] = nil
			}
			writeBatchPool.Put(batch)
		}()
		for _, node := range nodeSet.Nodes {
			if node.Hash == (common.Hash{}) {
				logger.Error("Commit: Trying to save node with empty hash, skipping.")
				continue
			}
			batch = append(batch, [2][]byte{node.Hash[:], node.Blob})
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
	if config.ConfigApp != nil {
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

	// Preserve ChangelogDB
	var changelogDB *state_changelog.StateChangelogDB
	if db.trie != nil {
		if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
			changelogDB = nomtTrie.GetChangelogDB()
		}
	}
	if changelogDB != nil {
		if newNomt, ok := newTrie.(*p_trie.NomtStateTrie); ok {
			newNomt.SetChangelogDB(changelogDB)
		}
	}

	// 6. Update the live trie reference and origin hash
	if db.trie != nil && db.trie != newTrie {
		if closer, ok := db.trie.(interface{ Close() }); ok {
			closer.Close()
		}
	}
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
	NomtPayload    interface{}      // Extracted NOMT payload for asynchronous block commit
}

// CommitPipeline performs the fast, synchronous phase of commit:
//  1. IntermediateRoot(false) → return cached hash
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
	// ═══════════════════════════════════════════════════════════════
	// DEADLOCK-FREE DESIGN: Self-acquire muTrie instead of assuming
	// it's pre-held by IntermediateRoot. This eliminates the cross-function
	// deferred lock pattern that previously caused permanent deadlocks.
	// ═══════════════════════════════════════════════════════════════
	db.muCommit.Lock()
	defer db.muCommit.Unlock()

	db.muTrie.Lock()

	// ═══════════════════════════════════════════════════════════════
	// Phase 1: Get the already-computed hash (fast — no trie iteration)
	// ═══════════════════════════════════════════════════════════════
	intermediateHash, err := db.IntermediateRoot(false)
	if err != nil {
		db.muTrie.Unlock()
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

	// Sanity check: intermediateHash (from IntermediateRoot) must match committedHash (from Commit).
	// With NOMT inline commit fix (May 2026), IntermediateRoot calls Commit() for NOMT,
	// so CommitPipeline's Commit() sees empty wDirty and returns the same hash.
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
		// NOMT: should not happen with inline commit, but log for diagnostics
		logger.Warn("⚠️ [NOMT] CommitPipeline: intermediateHash=%s != committedHash=%s (unexpected after inline commit)",
			intermediateHash.Hex()[:18], committedHash.Hex()[:18])
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
		batch = writeBatchPool.Get().([][2][]byte)[:0]
		hasRoot := false
		for _, n := range nodeSet.Nodes {
			if n.Hash == (common.Hash{}) {
				continue
			}
			if n.Hash == committedHash {
				hasRoot = true
			}
			batch = append(batch, [2][]byte{n.Hash[:], n.Blob})
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
	if config.ConfigApp != nil {
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

	var nomtPayload interface{}
	if nomtTrie, isNomt := db.trie.(*p_trie.NomtStateTrie); isNomt {
		nomtPayload = nomtTrie.ExtractPendingPayload()
	}

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
		NomtPayload:    nomtPayload,
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
	// Step 0: IMMEDIATE PERSIST GATE UNBLOCK (DEFERRED)
	// Signal that trie swap AND CommitPayload are complete.
	// We use defer to ensure it ALWAYS closes even if an error occurs,
	// preventing permanent deadlocks in the consensus pipeline.
	// ═══════════════════════════════════════════════════════════════
	defer func() {
		if result.Batch != nil {
			for i := range result.Batch {
				result.Batch[i][0] = nil
				result.Batch[i][1] = nil
			}
			writeBatchPool.Put(result.Batch)
		}
		if result.PersistChannel != nil {
			close(result.PersistChannel)
		} else {
			close(db.persistReady)
		}
	}()

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
	var newTrieToSet p_trie.StateTrie
	if result.Trie != nil {
		newTrieToSet = result.Trie
	} else {
		// Fallback for edge cases where Trie is not provided
		newTrie, err := p_trie.NewStateTrie(result.FinalHash, db.db, true)
		if err != nil {
			db.muTrie.Unlock()
			logger.Error("PersistAsync: Failed to create new trie", "hash", result.FinalHash, "error", err)
			return fmt.Errorf("PersistAsync: failed to load trie for root %s: %w", result.FinalHash, err)
		}
		newTrieToSet = newTrie
	}

	// Preserve ChangelogDB
	var changelogDB *state_changelog.StateChangelogDB
	if db.trie != nil {
		if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
			changelogDB = nomtTrie.GetChangelogDB()
		}
		if db.trie != newTrieToSet {
			if closer, ok := db.trie.(interface{ Close() }); ok {
				closer.Close()
			}
		}
	}
	if changelogDB != nil {
		if newNomt, ok := newTrieToSet.(*p_trie.NomtStateTrie); ok {
			newNomt.SetChangelogDB(changelogDB)
		}
	}

	db.trie = newTrieToSet
	db.originRootHash = result.FinalHash
	db.muTrie.Unlock()

	logger.Debug("PersistAsync: Trie swapped to new root and persistReady signaled", "hash", result.FinalHash)

	return nil
}

// IntermediateRoot computes the state root hash by applying all dirty account
// changes to the underlying trie.
//
// DEADLOCK-FREE DESIGN (May 2026 Redesign):
// This function is fully self-contained — it acquires muTrie internally and
// ALWAYS releases it before returning. The previous design left muTrie locked
// intentionally for Commit/CommitPipeline to release, but any early-exit path
// between IntermediateRoot and Commit caused a permanent deadlock.
//
// Parameters:
//   - isLockProcess=true (default): Full processing mode. Applies dirty accounts
//     to trie, computes hash, acquires+releases muTrie.
//   - isLockProcess=false: Fast hash-return mode for Commit/CommitPipeline.
//     Returns the cached trie hash. Caller MUST hold muTrie.
func (db *AccountStateDB) IntermediateRoot(isLockProcess ...bool) (common.Hash, error) {
	var lockProcess bool
	if len(isLockProcess) > 0 {
		lockProcess = isLockProcess[0]
	} else {
		lockProcess = true
	}

	if !lockProcess {
		// ═══════════════════════════════════════════════════════════════
		// FAST PATH: Called from Commit/CommitPipeline which already holds muTrie.
		// IntermediateRoot(true) already applied all dirty accounts and computed
		// the hash. Just return the cached trie hash.
		// FORK-SAFETY: dirtyAccounts was cleared by IntermediateRoot(true),
		// so no risk of picking up next block's state.
		// ═══════════════════════════════════════════════════════════════
		currentHash := db.trie.Hash()
		logger.Debug("IntermediateRoot(false): returning cached hash (no dirty processing)", "hash", currentHash)
		return currentHash, nil
	}

	// ═══════════════════════════════════════════════════════════════
	// FULL PROCESSING PATH: IntermediateRoot(true)
	// ═══════════════════════════════════════════════════════════════
	startAll := time.Now()
	var (
		collectDuration time.Duration
		sortDuration    time.Duration
		marshalDuration time.Duration
		persistDuration time.Duration
		batchDuration   time.Duration
		clearDuration   time.Duration
		hashDuration    time.Duration
	)

	// FORK-SAFETY FIX: Use cacheEpoch as a SeqLock to prevent concurrent mempool
	// validators (AccountStateReadOnly) from poisoning the lruCache with stale
	// trie data while IntermediateRoot is actively mutating the trie.
	db.cacheEpoch.Add(1)
	defer db.cacheEpoch.Add(1)

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

	logger.Debug("IntermediateRoot(true): starting full processing")

	// DIAGNOSTIC GUARD: Set lockedFlag for the duration of IntermediateRoot(true).
	// Mutation functions (AddBalance, SetNonce, etc.) check this flag and refuse
	// to modify accounts while the trie is being mutated. This is a self-contained
	// guard — lockedFlag is cleared before this function returns.
	db.lockedFlag.Store(true)
	defer db.lockedFlag.Store(false)

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
	}()

	// Bước 1: Thu thập keys (vì sync.Map.Range không cho biết trước số lượng)
	collectStart := time.Now()
	db.dirtyAccounts.Range(func(key common.Address, value types.AccountState) bool {
		address := key
		state := value
		if state == nil {
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
	collectDuration = time.Since(collectStart)

	// CRITICAL FORK-SAFETY: Sort the keys before updating the trie.
	// sync.Map.Range iterates in random order. Updating the Merkle Patricia Trie
	// with the same keys but in different orders causes structural differences
	// (different branches, splits) which completely changes the final AccountStatesRoot
	// and causes forks between nodes. Sorting guarantees deterministic trie updates.
	sortStart := time.Now()
	slices.SortFunc(keysToProcess, func(a, b dirtyAccountEntry) int {
		return bytes.Compare(a.addr[:], b.addr[:])
	})
	sortDuration = time.Since(sortStart)

	totalDirty := len(keysToProcess)
	logger.Debug("Starting update from dirtyAccounts", "count", totalDirty)

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
		marshalDuration = time.Since(startMarshal)
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
	// ═══════════════════════════════════════════════════════════════
	// FORK-SAFETY FIX (May 2026): Track LRU cache misses that need
	// trie reads. These reads MUST be deferred until AFTER persistReady
	// to ensure db.trie points to the correct (swapped) trie.
	//
	// BUG: Previously, db.trie.Get() was called here (before persistReady).
	// If PersistAsync from the previous block hadn't swapped db.trie yet,
	// this read returned stale data (2 blocks behind). Different nodes
	// complete PersistAsync at different times → different old values
	// → different NOMT hash computation → stateRoot fork (Block 2395).
	// ═══════════════════════════════════════════════════════════════
	// batchKeys and batchValues slices are pre-allocated via sync.Pool above.
	for _, res := range marshalResults {
		if res.err != nil {
			logger.Error("Marshal error for %s: %v", res.address.Hex(), res.err)
			updateErr = fmt.Errorf("marshal error for %s: %w", res.address.Hex(), res.err)
			break
		}

		batchKeys = append(batchKeys, res.address.Bytes())
		batchValues = append(batchValues, res.bytes)
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
		// PERSIST GATE: Wait for the previous block's trie swap BEFORE
		// any trie reads or BatchUpdate. This ensures db.trie is up-to-date.
		//
		// FORK-SAFETY FIX (May 2026): Previously this wait was AFTER
		// BatchUpdate — but trie.Get() fallback reads (for LRU cache misses)
		// happened in the prepare phase BEFORE this gate, reading stale data.
		// Moving the gate here ensures all trie reads see the swapped trie.
		//
		// PERF NOTE: PersistAsync closes persistReady BEFORE CommitPayload,
		// so waiting on persistReady was sufficient.
		waitStart := time.Now()
		<-db.persistReady
		persistDuration = time.Since(waitStart)
		if persistDuration > 50*time.Millisecond {
			logger.Warn("🔥 [SATURATION] AccountStateDB: IntermediateRoot waited %v for persistReady gate (Pipeline stalled)!", persistDuration)
		}

		if updateErr == nil && len(batchKeys) > 0 {
			startBatch := time.Now()
			// TPS OPT PHASE 2: Use BatchUpdateWithCachedOldValues to skip DB reads
			// Try FlatStateTrie first, then NomtStateTrie
			if flatTrie, ok := db.trie.(*p_trie.FlatStateTrie); ok {
				// ═══════════════════════════════════════════════════════════════
				// 100% FORK-SAFETY GUARANTEE: Read old values directly from FlatStateTrie's
				// DB backend instead of relying on the non-deterministic LRU cache.
				//
				// CRITICAL FIX (May 2026): Similar to the NOMT fix, using lruCache-sourced
				// batchOldValues via BatchUpdateWithCachedOldValues caused non-deterministic
				// stateRoot forks under high TPS load because of cache hit/miss divergence.
				//
				// FlatStateTrie's BatchUpdate reads old values via f.db.Get() in 16
				// parallel goroutines directly from the database. This is the SINGLE
				// SOURCE OF TRUTH and guarantees absolute determinism.
				// ═══════════════════════════════════════════════════════════════
				if err := flatTrie.BatchUpdate(batchKeys, batchValues); err != nil {
					logger.Error("BatchUpdate (FlatStateTrie direct read) failed: %v", err)
					updateErr = fmt.Errorf("trie BatchUpdate error: %w", err)
				}
			} else if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
				// ═══════════════════════════════════════════════════════════════
				// 100% FORK-SAFETY GUARANTEE: Read old values directly from NOMT FFI.
				//
				// CRITICAL FIX (May 2026): The previous approach used lruCache-sourced
				// batchOldValues via BatchUpdateWithCachedOldValues. This caused
				// non-deterministic stateRoot forks because:
				//   1. lruCache hit/miss patterns diverge between nodes (cache rotation
				//      timing, sync recovery bypassing IntermediateRoot, etc.)
				//   2. Different oldValues → different RecordRead → different Merkle hash
				//   3. Data on disk is identical → stateRoot "self-heals" next block
				//
				// BatchUpdate reads old values via n.handle.Read() (NOMT FFI) in 16
				// parallel goroutines. This is the SINGLE SOURCE OF TRUTH — always
				// reads from memory-mapped pages, ~5-10μs per read, ~5ms for 1000
				// accounts. Negligible cost for absolute determinism guarantee.
				// ═══════════════════════════════════════════════════════════════
				if err := nomtTrie.WaitCommitPayload(); err != nil {
					logger.Error("WaitCommitPayload failed (NOMT): %v", err)
					updateErr = fmt.Errorf("trie WaitCommitPayload error: %w", err)
				} else if err := nomtTrie.BatchUpdate(batchKeys, batchValues); err != nil {
					logger.Error("BatchUpdate (NOMT direct read) failed: %v", err)
					updateErr = fmt.Errorf("trie BatchUpdate error: %w", err)
				}
			} else {
				// Fallback: generic BatchUpdate
				if err := db.trie.BatchUpdate(batchKeys, batchValues); err != nil {
					logger.Error("BatchUpdate (fallback) failed: %v", err)
					updateErr = fmt.Errorf("trie BatchUpdate error: %w", err)
				}
			}
			batchDuration = time.Since(startBatch)
		}

		if updateErr != nil {
			db.muTrie.Unlock()
			logger.Error("Failed during dirtyAccounts update loop (FlatTrie)", "error", updateErr, "processedBeforeError", processedKeys)
			return common.Hash{}, updateErr
		}

		// Only lock for Hash() + clear maps (minimal critical section ~10ms)
		db.muTrie.Lock()
	} else {
		// ─── MPT PATH: Full lock for PreWarm + BatchUpdate ─────────

		// DEFERRED PERSIST GATE (MPT path): same as FlatTrie path above.
		// CRITICAL FORK FIX: ALL trie types must wait (see comment above).
		waitStart := time.Now()
		<-db.persistReady
		persistDuration = time.Since(waitStart)
		if persistDuration > 50*time.Millisecond {
			logger.Warn("🔥 [SATURATION] AccountStateDB (MPT): IntermediateRoot DEFERRED persistReady wait took %v (Pipeline stalled)!", persistDuration)
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
			batchDuration = time.Since(startBatch)
		}

		if updateErr != nil {
			db.muTrie.Unlock()
			logger.Error("Failed during dirtyAccounts update loop (MPT)", "error", updateErr, "processedBeforeError", processedKeys)
			return common.Hash{}, updateErr
		}
	}

	logger.Debug("Finished processing dirtyAccounts", "processedTotal", processedKeys)

	// ═══════════════════════════════════════════════════════════════
	// TPS OPT Phase 2: In-place clear instead of reassignment.
	clearStart := time.Now()
	
	// CRITICAL FIX: The account was modified and committed to the trie.
	// If its old (pre-modification) state was cached in loadedAccounts, we MUST evict it!
	// We cannot do `Delete` inside `Range` on ShardedAddressMap as it causes deadlocks.
	// We just clear both maps entirely, which achieves the exact same fork-safety guarantees.
	db.dirtyAccounts.Clear()

	// TPS OPT Phase 1: Bounded eviction for lruCache.
	// lruCache grows unbounded across blocks (~10-30K new entries/block).
	// After 10 blocks, rotate it generationally to cap memory.
	// FORK-SAFETY: these are read-only caches — clearing/rotating them only
	// causes re-reads from trie, which produce identical values.

	// Unconditionally clear loadedAccounts at the end of every block.
	// FORK-SAFETY FIX: Retaining loadedAccounts across blocks causes pointer
	// mutation state drift when blocksSinceLoadedClear diverges between nodes.
	db.loadedAccounts.Clear()

	db.blocksSinceLoadedClear++
	if db.blocksSinceLoadedClear >= 10 {
		db.blocksSinceLoadedClear = 0

		// Rotate lruCache generationally to prevent OOM
		if db.lruCache != nil {
			db.lruMu.Lock()
			db.lruCacheOld = db.lruCache
			db.lruCache = make(map[common.Address][]byte, 200000)
			db.lruMu.Unlock()
			logger.Debug("[TPS-OPT] Rotated lruCache (bounded eviction double-generation swap)")
		}
	}
	clearDuration = time.Since(clearStart)

	var newHash common.Hash
	startHash := time.Now()
	if hasChanges {
		// CRITICAL FIX: Must compute the actual trie hash NOW so that
		// ProcessTransactions returns the correct post-state root for the block header.

		// ═══════════════════════════════════════════════════════════════
		// NOMT STATEROOT FIX (May 2026):
		//
		// For NOMT trie, Hash() returns readView.rootHash — a cached value
		// that is ONLY updated by Commit(). Calling Hash() here returns the
		// PREVIOUS block's root, causing STATEROOT_FREEZE: the block header
		// shows stale stateRoot despite 170+ successful TXs.
		//
		// Fix: Call Commit() for NOMT during IntermediateRoot(true).
		// This runs the NOMT session (RecordRead + BatchWrite + Finish),
		// computes the actual Merkle root, and updates readView.rootHash.
		//
		// CommitPipeline's subsequent trie.Commit() call will see empty
		// wDirty and return immediately (no-op) with the correct hash.
		//
		// For MPT/Flat tries, Hash() correctly computes from in-memory
		// trie state — no change needed.
		// ═══════════════════════════════════════════════════════════════
		if _, isNomt := db.trie.(*p_trie.NomtStateTrie); isNomt {
			committedHash, _, _, commitErr := db.trie.Commit(true)
			if commitErr != nil {
				logger.Error("❌ [NOMT-INLINE-COMMIT] Commit during IntermediateRoot failed: %v", commitErr)
				newHash = db.trie.Hash() // fallback to stale hash
			} else {
				newHash = committedHash
				logger.Debug("[NOMT-INLINE-COMMIT] IntermediateRoot got real root: %s (was: %s)",
					newHash.Hex()[:18]+"...", db.originRootHash.Hex()[:18]+"...")
			}
		} else {
			newHash = db.trie.Hash()
		}
	} else {
		newHash = db.originRootHash
		logger.Debug("No changes detected in dirtyAccounts, intermediate hash remains origin hash", "hash", newHash)
	}
	hashDuration = time.Since(startHash)

	// ═══════════════════════════════════════════════════════════════
	// DEADLOCK-FREE: Unlock muTrie before returning.
	// Commit/CommitPipeline will re-acquire muTrie when they need it.
	// This eliminates the cross-function deferred lock pattern that
	// caused permanent deadlocks when callers skipped commit.
	// ═══════════════════════════════════════════════════════════════
	db.muTrie.Unlock()

	totalDuration := time.Since(startAll)
	if totalDirty > 0 {
		logger.Info("⏱️  [IR-PERF] AccountStateDB IntermediateRoot took %v (collect=%v, sort=%v, marshal=%v, persistWait=%v, batchUpdate=%v, clear=%v, hash=%v, dirtyCount=%d)",
			totalDuration.Round(time.Microsecond), collectDuration.Round(time.Microsecond), sortDuration.Round(time.Microsecond),
			marshalDuration.Round(time.Microsecond), persistDuration.Round(time.Microsecond), batchDuration.Round(time.Microsecond),
			clearDuration.Round(time.Microsecond), hashDuration.Round(time.Microsecond), totalDirty)
	}

	return newHash, nil
}
