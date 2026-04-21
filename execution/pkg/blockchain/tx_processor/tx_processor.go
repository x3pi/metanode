package tx_processor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	// GlobalTxProcessCounter tracks the number of TXs processed since the last LazyPebbleDB flush
	GlobalTxProcessCounter uint64

	// FlushThresholdTxs defines how many TXs are allowed to accumulate in RAM before auto-flushing.
	// Increased to 500,000 to prevent L0 compaction stalls during 50k+ TPS burst tests.
	// The 157GB server RAM easily sustains this cache size before asynchronous background flush.
	FlushThresholdTxs uint64 = 500000
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
}

// ProcessTransactions processes a batch of transactions.
// blockTime is the deterministic block timestamp (in seconds) from Rust consensus.
// This ensures all nodes use the same EVM block.timestamp for deterministic execution.
func ProcessTransactions(ctx context.Context, chainState *blockchain.ChainState, groupedGroups []grouptxns.RelativeGroup, enableTrace bool, isCache bool, blockTime uint64) (
	ProcessResult,
	error,
) {
	lastBlockHeader := chainState.GetcurrentBlockHeader()

	var funcCtx context.Context
	var funcSpan *trace.Span
	if enableTrace {
		tracedCtx, actualSpan := trace.StartSpan(ctx, "TxProcessor.processGroupsConcurrently", map[string]interface{}{
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
	allTransactions, allReceipts, allExecuteSCResults, mvmIdMap := processGroupsConcurrently(funcCtx, chainState, groupedGroups, *lastBlockHeader, enableTrace, isCache, blockTime)
	execDuration := time.Since(startExec)
	logger.Debug("[PERF] Block Execution (Parallel): %v, txCount: %v, groups: %v", execDuration, len(allTransactions), len(groupedGroups))

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
	blockNum := (*lastBlockHeader).BlockNumber() + 1
	if len(allTransactions) > 0 {
		logger.Debug("[PERF] Block #%d Phase Breakdown (txCount=%d):", blockNum, len(allTransactions))
		logger.Debug("  [PERF]   TX Execution (Parallel): %v", execDuration)
		logger.Debug("  [PERF]   IntermediateRoot (TrieDB): %v", trieDBIRDuration)
		logger.Debug("  [PERF]   IntermediateRoot (AccountDB): %v (Parallel)", accountIRDuration)
		logger.Debug("  [PERF]   IntermediateRoot (StakeDB): %v (Parallel)", stakeIRDuration)
		logger.Debug("  [PERF]   TOTAL IR (Wall Clock): %v", trieDBIRDuration+utils.MaxDuration(accountIRDuration, stakeIRDuration))
	} else {
		logger.Debug("[PERF] IntermediateRoot (StakeState): %v, block: %v", stakeIRDuration, blockNum)
	}
	// logger.Info("🔍 [FORK-DEBUG] Block #%d: POST-IR stakeRoot=%s", blockNum, stakeRoot.Hex())

	// Prepare and send the final result
	processResult := ProcessResult{
		Transactions:     allTransactions,
		Receipts:         allReceipts,
		ExecuteSCResults: allExecuteSCResults,
		Root:             root,
		Error:            nil,
		EventLogs:        eventLogs,
		StakeStatesRoot:  stakeRoot,
		MvmIdMap:         mvmIdMap,
	}
	return processResult, nil
}

// ProcessTransactionsRemote processes a batch of transactions for remote execution.
func ProcessTransactionsRemote(ctx context.Context, chainState *blockchain.ChainState, groupedGroups []grouptxns.RelativeGroup, enableTrace bool, isCache bool, blockTime uint64) (
	ProcessResult,
	error,
) {
	lastBlockHeader := chainState.GetcurrentBlockHeader()

	var funcCtx context.Context
	var funcSpan *trace.Span
	if enableTrace {
		tracedCtx, actualSpan := trace.StartSpan(ctx, "TxProcessor.processGroupsConcurrently", map[string]interface{}{
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
	allTransactions, allReceipts, allExecuteSCResults, mvmIdMap := processGroupsConcurrently(funcCtx, chainState, groupedGroups, *lastBlockHeader, enableTrace, isCache, blockTime)

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
	var root common.Hash
	var stakeRoot common.Hash
	var accountErr, stakeErr error
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

	// Prepare and send the final result

	processResult := ProcessResult{
		Transactions:     allTransactions,
		Receipts:         allReceipts,
		ExecuteSCResults: allExecuteSCResults,
		Root:             root,
		Error:            nil,
		EventLogs:        eventLogs,
		StakeStatesRoot:  stakeRoot,
		MvmIdMap:         mvmIdMap,
	}
	// Send result to channel
	// Consider if sending on the channel should happen outside the lock if it blocks
	// Return results
	return processResult, nil
}

func processGroupsConcurrently(
	ctx context.Context,
	chainState *blockchain.ChainState,
	groupedGroups []grouptxns.RelativeGroup,
	lastBlockHeader types.BlockHeader,
	enableTrace bool,
	isCache bool,
	blockTime uint64,
) (
	[]types.Transaction,
	[]types.Receipt,
	[]types.ExecuteSCResult,
	map[common.Hash]common.Address,
) {

	var funcCtx context.Context
	var funcSpan *trace.Span

	// Bắt đầu span cho hàm này (nếu được bật)
	if enableTrace {
		tracedCtx, actualSpan := trace.StartSpan(ctx, "TxProcessor.processGroupsConcurrently", map[string]interface{}{
			"groupCount": len(groupedGroups),
		})
		funcCtx = tracedCtx
		funcSpan = actualSpan
		defer funcSpan.End() // Kết thúc span khi hàm này thoát
	} else {
		funcCtx = ctx // Sử dụng context gốc (có thể là blockCtx)
		funcSpan = nil
	}

	// Pre-fetch ALL required state objects before parallel execution.
	// OPTIMIZATION: Collect unique addresses, then batch-load via PreloadAccounts()
	// which acquires muTrie.Lock() ONCE instead of N times (329ms → ~100ms for 11k addrs).
	if enableTrace {
		funcSpan.AddEvent("PreFetchingStateDBs", map[string]interface{}{"count": len(groupedGroups)})
	}
	startPreload := time.Now()

	// Step 1: Collect unique addresses (O(n) with map, then convert to slice)
	// FORK-SAFETY + PERF: Build addrSlice in deterministic order to avoid sorting overhead.
	// We iterate through groupedGroups (which are deterministically ordered) and append
	// new addresses to addrSlice, using uniqueMap to track seen ones.
	uniqueMap := make(map[common.Address]struct{}, len(groupedGroups)*2)
	addrSlice := make([]common.Address, 0, len(groupedGroups)*2)
	for _, group := range groupedGroups {
		for _, item := range group.Items {
			fromAddr := item.Tx.FromAddress()
			if _, seen := uniqueMap[fromAddr]; !seen {
				uniqueMap[fromAddr] = struct{}{}
				addrSlice = append(addrSlice, fromAddr)
			}
			if !item.Tx.IsDeployContract() {
				toAddr := item.Tx.ToAddress()
				if _, seen := uniqueMap[toAddr]; !seen {
					uniqueMap[toAddr] = struct{}{}
					addrSlice = append(addrSlice, toAddr)
				}
			}
		}
	}

	// NO SORT NEEDED: addrSlice is already deterministically ordered based on groupedGroups.
	chainState.GetAccountStateDB().PreloadAccounts(addrSlice)

	preloadDuration := time.Since(startPreload)
	logger.Debug("⚡ [PERF] Pre-fetched %d unique addresses (from %d groups) via BATCH in %v",
		len(addrSlice), len(groupedGroups), preloadDuration)

	// CRITICAL FORK-SAFETY: Use indexed slice instead of channel to collect results.
	// Goroutines write to results[i] (by group index), ensuring deterministic merge order
	// regardless of which goroutine finishes first. This prevents receipt ordering differences
	// between nodes that cause receiptsRoot divergence → fork.
	results := make([]grouptxns.GroupResult, len(groupedGroups))

	// ═══════════════════════════════════════════════════════════════
	// WORKER POOL: Use bounded goroutines instead of 1-per-group.
	// For 30K BLS TXs, this reduces goroutine count from 30K to NumCPU.
	// Each worker pulls the next group index via atomic counter.
	//
	// FORK-SAFETY: Results are written to results[idx] by group index,
	// so merge order is deterministic regardless of worker assignment.
	// ═══════════════════════════════════════════════════════════════

	// Pre-compute mvmIds for all groups (deterministic, no goroutine needed)
	type groupMeta struct {
		mvmId    common.Address
		groupCtx context.Context
		span     *trace.Span
	}
	groupMetas := make([]groupMeta, len(groupedGroups))
	for i, group := range groupedGroups {
		id := group.GroupID
		// PERF: Replace slow fmt.Sprintf + SHA256 with fast deterministic address generation
		// BUGFIX: Use a static 0xFE prefix to guarantee it NEVER overlaps with Xapian DB contract addresses
		var ethAddressBytes [20]byte
		ethAddressBytes[0] = 0xFE
		copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
		binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(id))
		mvmId := common.Address(ethAddressBytes)

		var gCtx context.Context
		var gSpan *trace.Span
		if enableTrace {
			tracedGroupCtx, actualGroupSpan := trace.StartSpan(funcCtx, fmt.Sprintf("TxProcessor.ProcessGroup-%d", i), map[string]interface{}{
				"groupID":   group.GroupID,
				"itemCount": len(group.Items),
			})
			gCtx = tracedGroupCtx
			gSpan = actualGroupSpan
		} else {
			gCtx = funcCtx
			gSpan = nil
		}
		groupMetas[i] = groupMeta{mvmId: mvmId, groupCtx: gCtx, span: gSpan}
	}

	numWorkers := runtime.NumCPU()
	if numWorkers > len(groupedGroups) {
		numWorkers = len(groupedGroups)
	}

	var nextIdx atomic.Int64
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// ── EVM EXECUTION (pure parallel wall-clock) ──────────────────
	startEVM := time.Now()

	chunkSize := len(groupedGroups) / (numWorkers * 4)
	if chunkSize < 1 {
		chunkSize = 1
	} else if chunkSize > 64 {
		chunkSize = 64
	}

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for {
				endIdxVal := int(nextIdx.Add(int64(chunkSize)))
				startIdx := endIdxVal - chunkSize
				if startIdx >= len(groupedGroups) {
					return
				}
				endIdx := endIdxVal
				if endIdx > len(groupedGroups) {
					endIdx = len(groupedGroups)
				}
				for idx := startIdx; idx < endIdx; idx++ {
					meta := groupMetas[idx]
					result := processSingleGroup(meta.groupCtx, chainState, groupedGroups[idx].Items, meta.mvmId, lastBlockHeader, enableTrace, isCache, blockTime)
					results[idx] = result // Write to indexed position — deterministic order
					if enableTrace && meta.span != nil {
						meta.span.End()
					}
				}
			}
		}()
	}

	if enableTrace {
		funcSpan.AddEvent("AllWorkersLaunched", map[string]interface{}{"numWorkers": numWorkers})
	}

	// Wait for all workers to complete
	wg.Wait()
	evmDuration := time.Since(startEVM)
	// ─────────────────────────────────────────────────────────────

	// ═══════════════════════════════════════════════════════════════
	// BATCH MUTATIONS: Apply deferred dirty accounts from all groups.
	// This runs single-threaded (no sync.Map contention) and in indexed
	// order (deterministic across all nodes).
	// ═══════════════════════════════════════════════════════════════
	startDirty := time.Now()
	for _, gRs := range results {
		for _, dirtyAs := range gRs.DirtyAccounts {
			chainState.GetAccountStateDB().PublicSetDirtyAccountState(dirtyAs)
		}
	}
	dirtyDuration := time.Since(startDirty)

	// FORK-SAFETY: Merge results in deterministic order (by group index)
	startMerge := time.Now()

	// PRE-ALLOCATE: Calculate total transactions to prevent slice growth (GC overhead)
	var totalTxs int
	var totalSCResults int
	for _, gRs := range results {
		totalTxs += len(gRs.Transactions)
		totalSCResults += len(gRs.ExecuteSCResults)
	}

	allTransactions := make([]types.Transaction, 0, totalTxs)
	allReceipts := make([]types.Receipt, 0, totalTxs)
	allExecuteSCResults := make([]types.ExecuteSCResult, 0, totalSCResults)
	allMvmIdMap := make(map[common.Hash]common.Address, totalTxs)

	for _, gRs := range results {
		allTransactions = append(allTransactions, gRs.Transactions...)
		allReceipts = append(allReceipts, gRs.Receipts...)
		allExecuteSCResults = append(allExecuteSCResults, gRs.ExecuteSCResults...)
		for h, addr := range gRs.MvmIdMap {
			allMvmIdMap[h] = addr
		}
	}
	mergeDuration := time.Since(startMerge)

	// ── SUMMARY ───────────────────────────────────────────────────
	txCount := len(allTransactions)
	groupCount := len(groupedGroups)
	var avgPerGroup time.Duration
	if groupCount > 0 {
		avgPerGroup = evmDuration / time.Duration(groupCount)
	}
	logger.Debug("🧮 [PERF-EVM] groups=%d | workers=%d | txCount=%d | EVM(parallel)=%v | dirty=%v | merge=%v | avg/group=%v | preload=%v",
		groupCount, numWorkers, txCount, evmDuration, dirtyDuration, mergeDuration, avgPerGroup, preloadDuration)
	// ─────────────────────────────────────────────────────────────

	if enableTrace {
		funcSpan.AddEvent("ResultsCollected", map[string]interface{}{
			"totalTxs":      len(allTransactions),
			"totalReceipts": len(allReceipts),
		})
	}

	return allTransactions, allReceipts, allExecuteSCResults, allMvmIdMap
}

func processSingleGroup(
	ctx context.Context,
	chainState *blockchain.ChainState,
	groupItems []grouptxns.Item,
	mvmId common.Address,
	lastBlockHeader types.BlockHeader,
	enableTrace bool,
	isCache bool,
	blockTime uint64,
) grouptxns.GroupResult {
	// PRE-ALLOCATE: Prevent internal slice re-allocation during block execution.
	gRs := grouptxns.GroupResult{
		Transactions:     make([]types.Transaction, 0, len(groupItems)),
		Receipts:         make([]types.Receipt, 0, len(groupItems)),
		ExecuteSCResults: make([]types.ExecuteSCResult, 0, len(groupItems)),
		Error:            nil,
		MvmIdMap:         make(map[common.Hash]common.Address, len(groupItems)),
	}
	startGroup := time.Now()

	failedSenders := make(map[common.Address]bool) // Đánh dấu nếu sender đã bị lỗi trong group này
	// blockTime is now passed from Rust consensus for deterministic execution across all nodes
	for _, item := range groupItems {
		tx := item.Tx
		var txCtx context.Context
		var txSpan *trace.Span
		if enableTrace {
			tracedTxCtx, actualTxSpan := trace.StartSpan(ctx, "TxProcessor.ProcessSingleTransaction", map[string]interface{}{
				"txHash":   tx.Hash().Hex(),
				"from":     tx.FromAddress().Hex(),
				"to":       tx.ToAddress().Hex(),
				"isCall":   tx.IsCallContract(),
				"isDeploy": tx.IsDeployContract(),
			})
			txCtx = tracedTxCtx
			txSpan = actualTxSpan
		} else {
			txCtx = ctx
			txSpan = nil
		}

		toAddress := tx.ToAddress()
		if tx.IsDeployContract() {
			toAddress = common.Address{}
		}
		// ❗ Nếu sender đã có lỗi trước đó, tạo receipt lỗi cho tx này và bài bỏ
		if failedSenders[tx.FromAddress()] {
			rcp := createErrorReceipt(tx, toAddress, fmt.Errorf("skipped due to previous transaction failure"))
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}

		// NOTE: VerifyTransaction skipped here — TXs from Rust consensus were already
		// verified by go-sub (AddTransactionToPool) before entering the pool.
		// Skipping saves signature verification + nonce check per TX.

		// Phần xử lý bình thường
		as, _ := chainState.GetAccountStateDB().AccountState(tx.FromAddress())
		var err error

		// CRITICAL SECURITY FIX: Validate nonce strictly before processing.
		// Although tx_pool verified the signature and nonce, multiple TXs
		// with the same nonce might have entered the pool concurrently before
		// the state was updated. We MUST reject duplicate nonces here to
		// prevent them from being executed by the C++ EVM in the same block.
		if as == nil {
			as = state.NewAccountState(tx.FromAddress())
		}
		if tx.GetNonce() != as.Nonce() {
			err = fmt.Errorf("nonce mismatch: tx.Nonce()=%d, state.Nonce()=%d", tx.GetNonce(), as.Nonce())
			// CRITICAL FIX: Downgrade from Error to Debug to prevent massive lock contention
			// when a client (e.g. tps_blast) resends duplicated batches under heavy load.
			logger.Debug("❌ [NONCE-REJECT] %v for tx %s", err, tx.Hash().Hex())
			rcp := createErrorReceipt(tx, toAddress, err)
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			failedSenders[tx.FromAddress()] = true // Ngừng parse các TX tiếp theo của sender này (giữ đúng thứ tự nonce)
			continue
		}
		var rcp types.Receipt
		if tx.ToAddress() == mt_common.VALIDATOR_CONTRACT_ADDRESS {
			validatorHandler, err := GetValidatorHandler()
			if err != nil {
				logger.Error("Lỗi khi tạo ValidatorHandler: %v", err)
				rcp = createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				failedSenders[tx.FromAddress()] = true
				continue
			}
			rcp, exRs, txFailed := validatorHandler.HandleTransaction(txCtx, chainState, tx, enableTrace, blockTime)
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			if exRs != nil { // exRs có thể nil trong một số trường hợp lỗi
				gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
			}
			if txFailed {
				failedSenders[tx.FromAddress()] = true
			}
			continue // Chuyển sang transaction tiếp theo
		}
		if tx.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
			// 🔍 [CROSS-CHAIN DEBUG] Log TX được đưa vào block number nào
			// So sánh log này giữa các node → nếu block# khác nhau → TX đến muộn → hash lệch
			blockNum := lastBlockHeader.BlockNumber() + 1
			logger.Info("📦 [BLOCK-INCLUDE] Cross-chain TX included in block #%d: hash=%s from=%s nonce=%d readOnly=%v",
				blockNum, tx.Hash().Hex()[:16], tx.FromAddress().Hex()[:10], tx.GetNonce(), tx.GetReadOnly())
			// Master tạo receipt ngay (ExecuteNonceOnly), không thay đổi state.
			// TX vẫn vào block → embassy nhận receipt → biết vote đã được ghi nhận.
			// ReadOnly=false (mặc định) → gọi HandleTransaction để execute đầy đủ.
			if tx.GetReadOnly() {
				logger.Info("[CC SIG_ACK] TX %s readOnly=true → nonce-only", tx.Hash().Hex())
				vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime)
				rcp = receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)

				var sigAckExRs types.ExecuteSCResult
				sigAckExRs, err = vmP.ExecuteNonceOnly(txCtx, tx, true)
				if err != nil {
					rcp = createErrorReceipt(tx, toAddress, err)
					if sigAckExRs != nil {
						rcp.UpdateExecuteResult(sigAckExRs.ReceiptStatus(), sigAckExRs.Return(), sigAckExRs.Exception(), sigAckExRs.GasUsed(), sigAckExRs.EventLogs())
					}
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					failedSenders[tx.FromAddress()] = true
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					logger.Error("❌ [CC SIG_ACK] TX %s type=100 → nonce-only failed: %v", tx.Hash().Hex(), err)
					continue
				}
				logger.Info("[CC SIG_ACK] TX %s nonce-only success: %v", tx.Hash().Hex(), tx.RelatedAddresses())
				rcp.UpdateExecuteResult(sigAckExRs.ReceiptStatus(), sigAckExRs.Return(), sigAckExRs.Exception(), sigAckExRs.GasUsed(), sigAckExRs.EventLogs())
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				if sigAckExRs != nil {
					gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, sigAckExRs)
				}
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			// ── ReadOnly=false: virtual đã đủ 2/3 vote (EXECUTE) ────────────
			// Master gọi HandleTransaction → phân loại EventKind trong batchSubmit.
			// Các call khác (lockAndBridge, sendMessage) cũng đi vào đây (ReadOnly mặc định false).
			logger.Info("[CC EXECUTE]__ TX %s readOnly=%v → HandleTransaction", tx.Hash().Hex(), tx.GetReadOnly())
			ccHandler, err := cross_chain_handler.GetCrossChainHandler()
			if err != nil {
				logger.Error("Lỗi khi tạo CrossChainHandler: %v", err)
				rcp = createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				failedSenders[tx.FromAddress()] = true
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			rcp, exRs, txFailed := ccHandler.HandleTransaction(txCtx, chainState, tx, mvmId, enableTrace, blockTime)
			logger.Info("[CC EXECUTE] TX %s type=%d → HandleTransaction result: %v", tx.Hash().Hex(), tx.GetType(), rcp)
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			if exRs != nil {
				gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
			}
			if txFailed {
				failedSenders[tx.FromAddress()] = true
			}
			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}
		if tx.ToAddress() == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
			dataInput := tx.CallData().Input()
			if len(dataInput) < 4 {
				logger.Error("Invalid calldata: less than 4 bytes")
				err := errors.New("invalid calldata")
				rcp := createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true
				continue
			}

			selector := dataInput[:4]
			fromAddr := tx.FromAddress()

			switch {
			case tx.GetNonce() == 0 && bytes.Equal(selector, utils.GetFunctionSelector("setBlsPublicKey(bytes)")):
				plk, err := UnpackSetBlsPublicKeyInput(dataInput)
				if err != nil {
					logger.Error("UnpackSetBlsPublicKeyInput failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp := createErrorReceipt(tx, toAddress, err)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					// NOTE: hasFailed NOT set — BLS operations are independent per-account
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				if as != nil && len(as.PublicKeyBls()) != 0 {
					logger.Warn("PublicKeyBls already exists for %s, skipping tx %s", fromAddr.Hex(), tx.Hash().Hex())
					rcp := createErrorReceipt(tx, toAddress, fmt.Errorf("PublicKeyBls already exists"))
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				// ═══════════════════════════════════════════════════════════════
				// BATCH MUTATIONS: Mutate account state directly (no DB calls).
				// This bypasses accountLock + getOrCreateAccountState + setDirty
				// for each call (3 DB calls → 0). Dirty marking is deferred to
				// post-parallel phase via gRs.DirtyAccounts.
				//
				// FORK-SAFETY: `as` is the same object pointer from PreloadAccounts.
				// Each group has a unique address, so no data race between groups.
				// DirtyAccounts are applied in indexed order after wg.Wait().
				// ═══════════════════════════════════════════════════════════════
				if setErr := as.SetPublicKeyBls(plk); setErr != nil {
					rcp := createErrorReceipt(tx, toAddress, setErr)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					logger.Error("SetPublicKeyBls failed for tx %s: %v", tx.Hash().Hex(), setErr)
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				if tx.GetNonce() != as.Nonce() {
					logger.Error("[NONCE-TRACE] BLS-SetPublicKey MISMATCH: addr=%s, tx.nonce=%d, state.nonce=%d, txHash=%s", tx.FromAddress().Hex(), tx.GetNonce(), as.Nonce(), tx.Hash().Hex())
					// This is a critical error, but we can't return here.
					// For now, we'll log and proceed, but this indicates a deeper issue.
				}
				logger.Debug("[NONCE-TRACE] BLS-SetPublicKey OK: addr=%s, tx.nonce=%d, state.nonce=%d, txHash=%s", tx.FromAddress().Hex(), tx.GetNonce(), as.Nonce(), tx.Hash().Hex())
				as.SetNonce(as.Nonce() + 1)
				logger.Debug("[NONCE-TRACE] BLS-SetNonce: addr=%s, nonce after +1=%d, txHash=%s", tx.FromAddress().Hex(), as.Nonce(), tx.Hash().Hex())
				// 🔒 NONCE-FIX: Sync C++ State cache to prevent stale nonce for subsequent EVM TXs
				mvm.CallUpdateStateNonce(tx.FromAddress(), as.Nonce())
				as.SetLastHash(tx.Hash())
				gRs.DirtyAccounts = append(gRs.DirtyAccounts, as)
				rcp := receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			case tx.GetNonce() != 0 && bytes.Equal(selector, utils.GetFunctionSelector("setAccountType(uint8)")):
				acType, err := UnpackSetAccountTypeInput(dataInput)
				if err != nil {
					logger.Error("UnpackSetAccountTypeInput failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp := createErrorReceipt(tx, toAddress, err)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				err = chainState.GetAccountStateDB().SetAccountType(fromAddr, acType)
				if err != nil {
					logger.Error("SetAccountType failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp := createErrorReceipt(tx, toAddress, err)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				// CRITICAL FORK-FIX: Create deterministic success receipt + nonce increment HERE.
				chainState.GetAccountStateDB().SetNonce(fromAddr, as.Nonce()+1)
				logger.Debug("[NONCE-TRACE] setAccountType-SetNonce: addr=%s, nonce=%d, txHash=%s", fromAddr.Hex(), as.Nonce()+1, tx.Hash().Hex())
				// 🔒 NONCE-FIX: Sync C++ State cache to prevent stale nonce for subsequent EVM TXs
				mvm.CallUpdateStateNonce(fromAddr, as.Nonce()+1)
				chainState.GetAccountStateDB().SetLastHash(fromAddr, tx.Hash())
				rcp := receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			case bytes.Equal(selector, utils.GetFunctionSelector("getAllAccount(bytes,bytes,uint,uint,uint,bool)")):
				logger.Info("etch call account getAllAccount _ %v", tx)
			case bytes.Equal(selector, utils.GetFunctionSelector("confirmAccount(address,bytes)")):
				logger.Info("etch call account confirmAccount_ %v", tx)
			default:
				err := fmt.Errorf("invalid selector: nonce=%d, selector=0x%x, txHash=%s", tx.GetNonce(), selector, tx.Hash().Hex())
				rcp := createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				logger.Error("Transaction failed for tx %s: %v", tx.Hash().Hex(), err)
				failedSenders[tx.FromAddress()] = true
				continue
			}

			rcp := receipt.NewReceipt(
				tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
				pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
				mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
				[]types.EventLog{}, 0, common.Hash{}, 0,
			)

			var exRs types.ExecuteSCResult
			vmP := vm_processor.NewVmProcessor(chainState, tx.ToAddress(), enableTrace, blockTime)

			exRs, err = vmP.ExecuteNonceOnly(txCtx, tx, true)
			if err != nil {
				rcp = createErrorReceipt(tx, toAddress, err)
				if exRs != nil {
					rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
					gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
				}
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				logger.Error("executeTransactionWithMvmId failed for tx %s: %v", tx.Hash().Hex(), err)
				failedSenders[tx.FromAddress()] = true // ❗ Đánh dấu lỗi
				continue
			}
			rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
			chainState.GetAccountStateDB().SetLastHash(tx.FromAddress(), tx.Hash())
			chainState.GetAccountStateDB().SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

			gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}

		rcp = receipt.NewReceipt(
			tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
			pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
			mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
			[]types.EventLog{}, 0, common.Hash{}, 0,
		)

		var exRs types.ExecuteSCResult
		vmP := vm_processor.NewVmProcessor(chainState, tx.ToAddress(), enableTrace, blockTime)
		usedMvmId := tx.ToAddress()
		if tx.IsDeployContract() || tx.IsRegularTransaction() || !isCache {
			vmP = vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime)
			usedMvmId = mvmId
		}
		gRs.MvmIdMap[tx.Hash()] = usedMvmId
		// logger.Debug("1.ExecuteSmartContract MVMId:")
		exRs, err = vmP.ExecuteTransactionWithMvmId(txCtx, tx, false, isCache)
		if err != nil {
			rcp = createErrorReceipt(tx, toAddress, err)
			if exRs != nil {
				rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
				gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
			}
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			logger.Error("executeTransactionWithMvmId failed for tx %s: %v", tx.Hash().Hex(), err)
			failedSenders[tx.FromAddress()] = true // ❗ Đánh dấu lỗi
			continue
		}
		logger.Debug("executeTransactionWithMvmId success for tx %s, exRs: %v", tx.Hash().Hex(), exRs)
		rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		chainState.GetAccountStateDB().SetLastHash(tx.FromAddress(), tx.Hash())
		chainState.GetAccountStateDB().SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

		gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)

		// ✅ Đảm bảo receipt luôn được đưa vào list, kể cả khi giao dịch bị revert (THREW/HALTED)
		// Giao dịch bị revert vẫn cần có receipt và đưa vào block để client biết được trạng thái
		if enableTrace && txSpan != nil {

			txSpan.End()

		}

		gRs.Receipts = append(gRs.Receipts, rcp)
		gRs.Transactions = append(gRs.Transactions, tx)

	}

	txCount := len(groupItems)
	elapsed := time.Since(startGroup)
	var avgPerTx time.Duration
	if txCount > 0 {
		avgPerTx = elapsed / time.Duration(txCount)
	}
	logger.Debug("⏱️ [PERF-GROUP] txCount=%d | groupTime=%v | avg=%v/tx",
		txCount, elapsed, avgPerTx)
	return gRs
}

func createErrorReceipt(tx types.Transaction, toAddress common.Address, err error) types.Receipt {
	return receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
	)
}
