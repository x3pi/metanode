package tx_processor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"

	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	// NomtAheadReplayMode enables bypassing transaction validation checks when NOMT is ahead of PebbleDB
	NomtAheadReplayMode atomic.Bool

	// GlobalTxProcessCounter tracks the number of TXs processed since the last LazyPebbleDB flush
	GlobalTxProcessCounter uint64

	// FlushThresholdTxs defines how many TXs are allowed to accumulate in RAM before auto-flushing.
	// TUNED (June 2026): Reduced from 500K to 100K to prevent OOM under sustained stress tests.
	// With 64MB PebbleDB memtables (down from 256MB), smaller batches flush faster without
	// L0 compaction stalls. Keeps LazyPebbleDB memoryCache bounded during long-running tests.
	FlushThresholdTxs uint64 = 100000


)

type ProcessResult struct {
	Transactions     []types.Transaction
	Receipts         []types.Receipt
	ExecuteSCResults []types.ExecuteSCResult
	Root             common.Hash
	StakeStatesRoot  common.Hash
	Error            error
	EventLogs        map[common.Address][]types.EventLog
	MvmIdMap         map[common.Hash]common.Address
	TrieDBSnapshots  map[common.Hash]*trie_database.TrieDatabaseSnapshot
	ModifiedAccounts []common.Address
	FullDbLogs       []map[string][]byte
}



// ProcessTransactions processes a batch of transactions.
// blockTime is the deterministic block timestamp (in seconds) from Rust consensus.
// This ensures all nodes use the same EVM block.timestamp for deterministic execution.
func ProcessTransactions(ctx context.Context, chainState *blockchain.ChainState, groupedGroups []grouptxns.RelativeGroup, enableTrace bool, isCache bool, blockTime uint64, leaderAddr common.Address, blockNum uint64, skipSignatureVerify bool) (
	ProcessResult,
	error,
) {
	if cfg := chainState.GetConfig(); cfg == nil || cfg.MVMCacheEnabled == nil || !*cfg.MVMCacheEnabled {
		isCache = false
	}

	// Clear C++ EVM global state cache at the start of block execution to prevent virtual execution leakage
	if isCache {
		mvm.CallClearAllStateInstances()
	}

	defer func() {
		mvm.ClearAllMVMApi()
		if isCache {
			mvm.CallClearAllStateInstances()
		}
	}()

	lastBlockHeader := chainState.GetcurrentBlockHeader()

	var funcCtx context.Context
	var funcSpan *trace.Span
	if enableTrace {
		tracedCtx, actualSpan := trace.StartSpan(ctx, "TxProcessor.ProcessTransactionsOptimistic", map[string]interface{}{
			"groupCount": len(groupedGroups),
		})
		funcCtx = tracedCtx
		funcSpan = actualSpan
		defer funcSpan.End() // Kết thúc span khi hàm này thoát
	} else {
		funcCtx = ctx // Sử dụng context gốc (có thể là blockCtx)
		funcSpan = nil
	}

	// *** Call the new function for concurrent processing ***
	startExec := time.Now()
	allTransactions, allReceipts, allExecuteSCResults, mvmIdMap := ProcessTransactionsOptimistic(funcCtx, chainState, groupedGroups, *lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr, skipSignatureVerify)
	execDuration := time.Since(startExec)
	logger.Info("[PERF] Block Execution (Parallel): %v, txCount: %v, groups: %v", execDuration, len(allTransactions), len(groupedGroups))

	// Get event logs (potentially modified by concurrent processing)
	eventLogs := chainState.GetSmartContractDB().EventLogs()

	// Note: Ensure accountStateDB is safe for concurrent reads/writes or handle synchronization appropriately.

	// --- PERF TIMING: IntermediateRoot phases (PARALLEL) ---
	var root, stakeRoot common.Hash
	var accountIRDuration, stakeIRDuration time.Duration
	var accountErr, stakeErr error

	startTrieDBIR := time.Now()
	trie_database.GetTrieDatabaseManager().IntermediateRoot()
	trieDBIRDuration := time.Since(startTrieDBIR)

	// Set blockNumber for StateChangelog BEFORE IntermediateRoot(true).
	// This ensures NOMT writes changes to the correct block in StateChangelogDB.
	chainState.GetAccountStateDB().SetTrieCommitBlock(blockNum)
	chainState.GetStakeStateDB().SetTrieCommitBlock(blockNum)

	var irWg sync.WaitGroup
	irWg.Add(2)

	// CRITICAL FIX: SmartContractDB must bind its roots to AccountState before AccountStateDB computes the trie
	if err := chainState.GetSmartContractDB().LateBindRoots(); err != nil {
		logger.Error("Failed to late bind roots for SmartContractDB: %v", err)
		return ProcessResult{Error: err}, fmt.Errorf("LateBindRoots SmartContractDB failed: %w", err)
	}

	// Phase 1: AccountStateDB (Parallel)
	go func() {
		defer irWg.Done()
		s := time.Now()
		root, accountErr = chainState.GetAccountStateDB().IntermediateRoot(true)
		accountIRDuration = time.Since(s)
	}()

	// Phase 2: StakeStateDB (Parallel)
	go func() {
		defer irWg.Done()
		s := time.Now()
		stakeRoot, stakeErr = chainState.GetStakeStateDB().IntermediateRoot(true)
		stakeIRDuration = time.Since(s)
	}()

	irWg.Wait()

	if accountErr != nil {
		logger.Error("Failed to get IntermediateRoot for AccountStateDB: %v", accountErr)
		return ProcessResult{Error: accountErr}, fmt.Errorf("IntermediateRoot AccountStateDB failed: %w", accountErr)
	}
	if stakeErr != nil {
		logger.Error("Failed to get IntermediateRoot for StakeStateDB: %v", stakeErr)
		return ProcessResult{Error: stakeErr}, fmt.Errorf("IntermediateRoot StakeStateDB failed: %w", stakeErr)
	}

	// --- PERF SUMMARY for blocks with TXs ---
	if len(allTransactions) > 0 {
		logger.Info("[PERF] Block #%d Phase Breakdown (txCount=%d):", blockNum, len(allTransactions))
		logger.Info("  [PERF]   TX Execution (Parallel): %v", execDuration)
		logger.Info("  [PERF]   IntermediateRoot (TrieDB): %v", trieDBIRDuration)
		logger.Info("  [PERF]   IntermediateRoot (AccountDB): %v (Parallel)", accountIRDuration)
		totalIR := trieDBIRDuration + utils.MaxDuration(accountIRDuration, stakeIRDuration)
		logger.Info("  [PERF]   TOTAL IR (Wall Clock): %v", totalIR)
		metrics.TrieIRDuration.Observe(totalIR.Seconds())
	} else {
		logger.Debug("[PERF] IntermediateRoot (StakeState): %v, block: %v", stakeIRDuration, blockNum)
		metrics.TrieIRDuration.Observe(stakeIRDuration.Seconds())
	}
	// logger.Info("🔍 [FORK-DEBUG] Block #%d: POST-IR stakeRoot=%s", blockNum, stakeRoot.Hex())

	// Prepare and send the final result
	var modifiedAccounts []common.Address
	if chainState.GetAccountStateDB() != nil {
		modifiedAccounts = chainState.GetAccountStateDB().DirtyAccountAddresses()
	}

	var allFullDbLogs []map[string][]byte
	for _, scRs := range allExecuteSCResults {
		if logs := scRs.MapFullDbLogs(); len(logs) > 0 {
			allFullDbLogs = append(allFullDbLogs, logs)
		}
	}

	processResult := ProcessResult{
		Transactions:     allTransactions,
		Receipts:         allReceipts,
		ExecuteSCResults: allExecuteSCResults,
		Root:             root,
		Error:            nil,
		EventLogs:        eventLogs,
		StakeStatesRoot:  stakeRoot,
		MvmIdMap:         mvmIdMap,
		TrieDBSnapshots:  trie_database.GetTrieDatabaseManager().SnapshotAllTrieDatabases(),
		ModifiedAccounts: modifiedAccounts,
		FullDbLogs:       allFullDbLogs,
	}
	return processResult, nil
}

// ProcessTransactionsRemote processes a batch of transactions for remote execution.
func ProcessTransactionsRemote(ctx context.Context, chainState *blockchain.ChainState, groupedGroups []grouptxns.RelativeGroup, enableTrace bool, isCache bool, blockTime uint64, leaderAddr common.Address, blockNum uint64, skipSignatureVerify bool) (
	ProcessResult,
	error,
) {
	if cfg := chainState.GetConfig(); cfg == nil || cfg.MVMCacheEnabled == nil || !*cfg.MVMCacheEnabled {
		isCache = false
	}

	// Clear C++ EVM global state cache at the start of block execution to prevent virtual execution leakage
	if isCache {
		mvm.CallClearAllStateInstances()
	}

	defer func() {
		mvm.ClearAllMVMApi()
		if isCache {
			mvm.CallClearAllStateInstances()
		}
	}()

	lastBlockHeader := chainState.GetcurrentBlockHeader()

	var funcCtx context.Context
	var funcSpan *trace.Span
	if enableTrace {
		tracedCtx, actualSpan := trace.StartSpan(ctx, "TxProcessor.ProcessTransactionsOptimistic", map[string]interface{}{
			"groupCount": len(groupedGroups),
		})
		funcCtx = tracedCtx
		funcSpan = actualSpan
		defer funcSpan.End() // Kết thúc span khi hàm này thoát
	} else {
		funcCtx = ctx // Sử dụng context gốc (có thể là blockCtx)
		funcSpan = nil
	}

	// *** Call the new function for concurrent processing ***
	allTransactions, allReceipts, allExecuteSCResults, mvmIdMap := ProcessTransactionsOptimistic(funcCtx, chainState, groupedGroups, *lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr, skipSignatureVerify)

	// Get event logs (potentially modified by concurrent processing)
	eventLogs := chainState.GetSmartContractDB().EventLogs()

	// Note: Ensure accountStateDB is safe for concurrent reads/writes or handle synchronization appropriately.

	// CRITICAL FIX: Must call TrieDatabaseManager.IntermediateRoot() before AccountStateDB.IntermediateRoot()
	// to propagate storage trie roots of TrieDatabase-managed contracts into account state.
	// This was previously missing, causing stateRoot divergence between consensus path and sync path.
	trie_database.GetTrieDatabaseManager().IntermediateRoot()

	// PERF OPTIMIZATION: Run AccountStateDB and StakeStateDB IntermediateRoot in parallel.
	// They operate on completely independent state databases (different tries, different storage).
	// FORK-SAFETY: Both are deterministic pure computations on their respective dirty state.
	// The ordering constraint (TrieDBManager before AccountStateDB) is preserved above.
	startRemoteIR := time.Now()
	var root common.Hash
	var stakeRoot common.Hash
	var accountErr, stakeErr error
	// Set blockNumber for StateChangelog BEFORE IntermediateRoot(true).
	// This ensures NOMT writes changes to the correct block in StateChangelogDB.
	chainState.GetAccountStateDB().SetTrieCommitBlock(blockNum)
	chainState.GetStakeStateDB().SetTrieCommitBlock(blockNum)

	var rootWg sync.WaitGroup
	rootWg.Add(2)

	// CRITICAL FIX: SmartContractDB must bind its roots to AccountState before AccountStateDB computes the trie
	if err := chainState.GetSmartContractDB().LateBindRoots(); err != nil {
		logger.Error("Failed to late bind roots for SmartContractDB (Remote): %v", err)
		return ProcessResult{Error: err}, fmt.Errorf("LateBindRoots SmartContractDB failed: %w", err)
	}

	go func() {
		defer rootWg.Done()
		root, accountErr = chainState.GetAccountStateDB().IntermediateRoot(true)
	}()

	go func() {
		defer rootWg.Done()
		stakeRoot, stakeErr = chainState.GetStakeStateDB().IntermediateRoot(true)
	}()

	rootWg.Wait()

	if accountErr != nil {
		logger.Error("Failed to get IntermediateRoot for AccountStateDB (Remote): %v", accountErr)
		return ProcessResult{Error: accountErr}, fmt.Errorf("IntermediateRoot AccountStateDB failed: %w", accountErr)
	}
	if stakeErr != nil {
		logger.Error("Failed to get IntermediateRoot for StakeStateDB (Remote): %v", stakeErr)
		return ProcessResult{Error: stakeErr}, fmt.Errorf("IntermediateRoot StakeStateDB failed: %w", stakeErr)
	}

	metrics.TrieIRDuration.Observe(time.Since(startRemoteIR).Seconds())

	// Prepare and send the final result
	var modifiedAccounts []common.Address
	if chainState.GetAccountStateDB() != nil {
		modifiedAccounts = chainState.GetAccountStateDB().DirtyAccountAddresses()
	}

	var allFullDbLogs []map[string][]byte
	for _, scRs := range allExecuteSCResults {
		if logs := scRs.MapFullDbLogs(); len(logs) > 0 {
			allFullDbLogs = append(allFullDbLogs, logs)
		}
	}

	processResult := ProcessResult{
		Transactions:     allTransactions,
		Receipts:         allReceipts,
		ExecuteSCResults: allExecuteSCResults,
		Root:             root,
		Error:            nil,
		EventLogs:        eventLogs,
		StakeStatesRoot:  stakeRoot,
		MvmIdMap:         mvmIdMap,
		TrieDBSnapshots:  trie_database.GetTrieDatabaseManager().SnapshotAllTrieDatabases(),
		ModifiedAccounts: modifiedAccounts,
		FullDbLogs:       allFullDbLogs,
	}
	// Send result to channel
	// Consider if sending on the channel should happen outside the lock if it blocks
	// Return results
	return processResult, nil
}
