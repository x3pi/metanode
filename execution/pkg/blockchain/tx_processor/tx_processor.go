package tx_processor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
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
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
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

	// Memory Pools for zero-allocation parallel processing
	txSlicePool = sync.Pool{
		New: func() interface{} {
			s := make([]types.Transaction, 0, 128)
			return &s
		},
	}
	receiptSlicePool = sync.Pool{
		New: func() interface{} {
			s := make([]types.Receipt, 0, 128)
			return &s
		},
	}
	scResultSlicePool = sync.Pool{
		New: func() interface{} {
			s := make([]types.ExecuteSCResult, 0, 128)
			return &s
		},
	}
	mvmIdMapPool = sync.Pool{
		New: func() interface{} {
			m := make(map[common.Hash]common.Address, 128)
			return &m
		},
	}
	failedSendersPool = sync.Pool{
		New: func() interface{} {
			m := make(map[common.Address]bool, 16)
			return &m
		},
	}
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

type groupResultExt struct {
	txPtr         *[]types.Transaction
	rcpPtr        *[]types.Receipt
	exPtr         *[]types.ExecuteSCResult
	mvmPtr        *map[common.Hash]common.Address
	Error         error
	DirtyAccounts []types.AccountState
	TotalGasFee   *big.Int
	readAccounts  map[common.Address]types.AccountState
	readStorage   map[common.Address][]string
}

// ProcessTransactions processes a batch of transactions.
// blockTime is the deterministic block timestamp (in seconds) from Rust consensus.
// This ensures all nodes use the same EVM block.timestamp for deterministic execution.
func ProcessTransactions(ctx context.Context, chainState *blockchain.ChainState, groupedGroups []grouptxns.RelativeGroup, enableTrace bool, isCache bool, blockTime uint64, leaderAddr common.Address, blockNum uint64) (
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
	allTransactions, allReceipts, allExecuteSCResults, mvmIdMap := ProcessTransactionsOptimistic(funcCtx, chainState, groupedGroups, *lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr)
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
		logger.Info("  [PERF]   IntermediateRoot (StakeDB): %v (Parallel)", stakeIRDuration)
		logger.Info("  [PERF]   TOTAL IR (Wall Clock): %v", trieDBIRDuration+utils.MaxDuration(accountIRDuration, stakeIRDuration))
	} else {
		logger.Debug("[PERF] IntermediateRoot (StakeState): %v, block: %v", stakeIRDuration, blockNum)
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
func ProcessTransactionsRemote(ctx context.Context, chainState *blockchain.ChainState, groupedGroups []grouptxns.RelativeGroup, enableTrace bool, isCache bool, blockTime uint64, leaderAddr common.Address, blockNum uint64) (
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
	allTransactions, allReceipts, allExecuteSCResults, mvmIdMap := ProcessTransactionsOptimistic(funcCtx, chainState, groupedGroups, *lastBlockHeader, enableTrace, isCache, blockTime, leaderAddr)

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
func processSingleGroup(
	ctx context.Context,
	chainState *blockchain.ChainState,
	localAccountDB types.AccountStateDB,
	localSmartContractDB types.SmartContractDB,
	groupItems []grouptxns.Item,
	mvmId common.Address,
	lastBlockHeader types.BlockHeader,
	enableTrace bool,
	isCache bool,
	blockTime uint64,
	leaderAddr common.Address,
) groupResultExt {
	// Acquire slices from memory pools to eliminate GC pressure
	txPtr := txSlicePool.Get().(*[]types.Transaction)
	txs := (*txPtr)[:0]

	rcpPtr := receiptSlicePool.Get().(*[]types.Receipt)
	rcps := (*rcpPtr)[:0]

	exPtr := scResultSlicePool.Get().(*[]types.ExecuteSCResult)
	exs := (*exPtr)[:0]

	mvmMapPtr := mvmIdMapPool.Get().(*map[common.Hash]common.Address)
	mvmMap := *mvmMapPtr
	clear(mvmMap) // Go 1.21+ fast clear

	gRs := grouptxns.GroupResult{
		Transactions:     txs,
		Receipts:         rcps,
		ExecuteSCResults: exs,
		Error:            nil,
		MvmIdMap:         mvmMap,
	}
	startGroup := time.Now()
	totalGasFee := big.NewInt(0)

	failedSendersPtr := failedSendersPool.Get().(*map[common.Address]bool)
	failedSenders := *failedSendersPtr
	clear(failedSenders) // Go 1.21+ fast clear
	defer failedSendersPool.Put(failedSendersPtr)

	// blockTime is now passed from Rust consensus for deterministic execution across all nodes
	for _, item := range groupItems {
		tx := item.Tx
		GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTION_START", "Transaction retrieved from consensus block for execution")
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
		// ❗ Nếu sender đã có lỗi trước đó, bài bỏ tx này mà không đưa vào block.
		// FORK-SAFETY & DATA INTEGRITY: Do NOT include skipped TXs in the block.
		// Including them without incrementing their nonce violates blockchain invariants.
		if failedSenders[tx.FromAddress()] {
			GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTION_SKIPPED", "Skipped execution due to previous transaction failure from this sender")
			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}

		// NOTE: VerifyTransaction skipped here — TXs from Rust consensus were already
		// verified by go-sub (AddTransactionToPool) before entering the pool.
		// Skipping saves signature verification + nonce check per TX.

		// Phần xử lý bình thường
		as, _ := localAccountDB.AccountState(tx.FromAddress())
		var err error

		// CRITICAL SECURITY FIX: Validate nonce strictly before processing.
		// Although tx_pool verified the signature and nonce, multiple TXs
		// with the same nonce might have entered the pool concurrently before
		// the state was updated. We MUST reject duplicate nonces here to
		// prevent them from being executed by the C++ EVM in the same block.
		if as == nil {
			as = state.NewAccountState(tx.FromAddress())
		}

		// Bổ sung xác thực lại giao dịch (BLS, Amount, MaxFee,...)
		// để chặn các giao dịch không hợp lệ lọt vào block (đặc biệt từ Sync Data của Rust)
		if errVerify := VerifyTransaction(tx, chainState, as); errVerify != nil {
			GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_VERIFY_REJECTED", errVerify.Description)
			logger.Warn("❌ [VERIFY-REJECT] %v for tx %s (From: %s) -> GIAO DỊCH BỊ VỨT BỎ KHỎI BLOCK", errVerify.Description, tx.Hash().Hex(), tx.FromAddress().Hex())

			if errVerify.Code != transaction.InvalidNonce.Code {
				failedSenders[tx.FromAddress()] = true
			}

			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}

		if tx.GetNonce() != as.Nonce() {
			if !NomtAheadReplayMode.Load() {
				err = fmt.Errorf("nonce mismatch: tx.Nonce()=%d, state.Nonce()=%d", tx.GetNonce(), as.Nonce())
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_NONCE_REJECTED", err.Error())
				// CRITICAL FIX: Changed from Debug to Warn so you can see exactly when a duplicate is rejected
				logger.Warn("❌ [NONCE-REJECT] %v for tx %s (From: %s) -> GIAO DỊCH BỊ VỨT BỎ KHỎI BLOCK", err, tx.Hash().Hex(), tx.FromAddress().Hex())

				// FORK-SAFETY & DATA INTEGRITY: Do NOT include invalid nonce TXs in the block.
				// Including them causes duplicate TX hashes across multiple blocks when a client
				// resends a batch, inflating the block's TX count and bloating the ledger.
				if tx.GetNonce() > as.Nonce() {
					failedSenders[tx.FromAddress()] = true // Ngừng parse các TX tiếp theo của sender này (giữ đúng thứ tự nonce)
				}

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			} else {
				logger.Warn("🛡️ [NOMT-AHEAD-REPLAY-TX] Bypassing nonce mismatch check for tx %s (From: %s, tx.Nonce=%d, state.Nonce=%d)",
					tx.Hash().Hex()[:16]+"...", tx.FromAddress().Hex(), tx.GetNonce(), as.Nonce())
			}
		}
		var rcp types.Receipt
		if tx.ToAddress() == mt_common.VALIDATOR_CONTRACT_ADDRESS {
			validatorHandler, err := GetValidatorHandler()
			if err != nil {
				logger.Error("Lỗi khi tạo ValidatorHandler: %v", err)
				rcp = createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
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

			if enableTrace && txSpan != nil {
				txSpan.End()
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
				// vm_processor handles VM interactions per transaction
				vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime, leaderAddr)
				vmP.SetAccountStateDB(localAccountDB)
				vmP.SetSmartContractDB(localSmartContractDB)
				
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
			ccHandler, err := cross_chain_handler.GetCrossChainHandler()
			if err != nil {
				logger.Error("Lỗi khi tạo CrossChainHandler: %v", err)
				rcp = createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			rcp, exRs, txFailed := ccHandler.HandleTransaction(txCtx, chainState, tx, mvmId, enableTrace, blockTime)
			gRs.MvmIdMap[tx.Hash()] = mvmId
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

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
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
				
				// Standardize Dirty Marking: Delegate to localAccountDB instead of manual gRs.DirtyAccounts slice
				localAccountDB.PublicSetDirtyAccountState(as)

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
				err = localAccountDB.SetAccountType(fromAddr, acType)
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
				localAccountDB.SetNonce(fromAddr, as.Nonce()+1)
				logger.Debug("[NONCE-TRACE] setAccountType-SetNonce: addr=%s, nonce=%d, txHash=%s", fromAddr.Hex(), as.Nonce()+1, tx.Hash().Hex())
				// 🔒 NONCE-FIX: Sync C++ State cache to prevent stale nonce for subsequent EVM TXs
				mvm.CallUpdateStateNonce(fromAddr, as.Nonce()+1)
				localAccountDB.SetLastHash(fromAddr, tx.Hash())
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

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}

			rcp := receipt.NewReceipt(
				tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
				pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
				mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
				[]types.EventLog{}, 0, common.Hash{}, 0,
			)

			var exRs types.ExecuteSCResult
			vmP := vm_processor.NewVmProcessor(chainState, tx.ToAddress(), enableTrace, blockTime, leaderAddr)

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

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
			localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
			localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

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
		vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime, leaderAddr)
		usedMvmId := mvmId

		if tx.IsRegularTransaction() {
			GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_START", "Executing native value transfer")
			// ═══════════════════════════════════════════════════════════════
			// BATCH MUTATIONS for Native TX: Use DB locks to prevent data races
			// with concurrent EVM groups modifying the same account.
			// ═══════════════════════════════════════════════════════════════
			_, isFree := chainState.GetFreeFeeAddress()[tx.ToAddress()]
			gasLimit := uint64(mt_common.TRANSFER_GAS_COST)
			if isFree {
				gasLimit = uint64(mt_common.MAX_GASS_FEE)
			}
			gasFee := new(big.Int).SetUint64(gasLimit * tx.MaxGasPrice())
			totalCost := new(big.Int).Add(tx.Amount(), gasFee)

			// Mutate sender (thread-safe)
			err = localAccountDB.SubTotalBalance(tx.FromAddress(), totalCost)
			if err != nil {
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_FAILED", "Insufficient balance for transfer")
				// Thread-safe check: if err is returned, it means insufficient balance (or lock issue)
				rcp := createErrorReceipt(tx, toAddress, fmt.Errorf("insufficient balance for transfer"))
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			localAccountDB.PlusOneNonce(tx.FromAddress())
			localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
			localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

			// Mutate receiver (thread-safe)
			localAccountDB.AddBalance(tx.ToAddress(), tx.Amount())

			// Accumulate gas fee for later batch update to coinbase
			totalGasFee.Add(totalGasFee, gasFee)

			// Sync C++ cache to prevent stale nonce for subsequent EVM TXs (if any)
			// Wait, we need the new nonce! Fetch it thread-safely:
			// as was fetched at the beginning, but its nonce might be stale now.
			// However, since FromAddress is grouped, no other native TX is modifying it,
			// and EVM doesn't modify nonces unless it's the sender of EVM tx (which is also grouped).
			// So we can just use as.Nonce() + 1
			mvm.CallUpdateStateNonce(tx.FromAddress(), as.Nonce()+1)

			// 🔒 BALANCE-FIX: Sync C++ State cache to prevent stale balance for subsequent EVM TXs
			asSender, _ := localAccountDB.AccountState(tx.FromAddress())
			if asSender != nil {
				mvm.CallUpdateStateBalance(tx.FromAddress(), asSender.TotalBalance())
			}

			asReceiver, _ := localAccountDB.AccountState(tx.ToAddress())
			if asReceiver != nil {
				mvm.CallUpdateStateBalance(tx.ToAddress(), asReceiver.TotalBalance())
			}

			// Generate fake MVM result
			exRs = smart_contract.NewExecuteSCResult(
				tx.Hash(), pb.RECEIPT_STATUS_RETURNED, pb.EXCEPTION_NONE, nil,
				mt_common.TRANSFER_GAS_COST, common.Hash{},
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			)

		} else {
			GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_START", "Executing EVM smart contract call")
			// CRITICAL FORK FIX: We removed `tx.ToAddress()` as the cache key and ALWAYS use `mvmId` (Group ID).
			// This prevents cross-block C++ EVM dirty state leaks and concurrency corruption.
			gRs.MvmIdMap[tx.Hash()] = usedMvmId
			// logger.Debug("1.ExecuteSmartContract MVMId:")
			startMVM := time.Now()
			exRs, err = vmP.ExecuteTransactionWithMvmId(txCtx, tx, false, isCache)
			mvmElapsed := time.Since(startMVM)
			if mvmElapsed.Milliseconds() > 50 {
				logger.Warn("🐌 [PERF-WARN] ExecuteTransactionWithMvmId (C++ EVM) took %v for tx %s (From: %s). EVM Execution is lagging!", mvmElapsed, tx.Hash().Hex(), tx.FromAddress().Hex())
			}
			if err != nil {
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_FAILED", err.Error())
				rcp = createErrorReceipt(tx, toAddress, err)
				if exRs != nil {
					rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
					gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
				}
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				logger.Error("executeTransactionWithMvmId failed for tx %s: %v", tx.Hash().Hex(), err)
				failedSenders[tx.FromAddress()] = true // ❗ Đánh dấu lỗi

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_SUCCESS", fmt.Sprintf("EVM contract execution completed, status: %d, gasUsed: %d", exRs.ReceiptStatus(), exRs.GasUsed()))
			logger.Debug("executeTransactionWithMvmId success for tx %s, exRs: %v", tx.Hash().Hex(), exRs)
			localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
			localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
		}
		rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
		localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

		gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)

		// ✅ Đảm bảo receipt luôn được đưa vào list, kể cả khi giao dịch bị revert (THREW/HALTED)
		// Giao dịch bị revert vẫn cần có receipt và đưa vào block để client biết được trạng thái
		if enableTrace && txSpan != nil {
			txSpan.End()
		}

		GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_RECEIPT_CREATED", fmt.Sprintf("Receipt created and added to block. Status: %d", rcp.Status()))

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

	// Update slice headers in pools before returning
	*txPtr = gRs.Transactions
	*rcpPtr = gRs.Receipts
	*exPtr = gRs.ExecuteSCResults
	*mvmMapPtr = gRs.MvmIdMap

	// Standardize extraction of DirtyAccounts for Block-STM
	var dirtyAccounts []types.AccountState
	if adb, ok := localAccountDB.(interface{ GetDirtyAccounts() map[common.Address]types.AccountState }); ok {
		dirtyMap := adb.GetDirtyAccounts()
		for _, as := range dirtyMap {
			dirtyAccounts = append(dirtyAccounts, as)
		}
	} else {
		// Fallback
		dirtyAccounts = gRs.DirtyAccounts
	}

	return groupResultExt{
		txPtr:         txPtr,
		rcpPtr:        rcpPtr,
		exPtr:         exPtr,
		mvmPtr:        mvmMapPtr,
		Error:         gRs.Error,
		DirtyAccounts: dirtyAccounts,
		TotalGasFee:   totalGasFee,
	}
}

func createErrorReceipt(tx types.Transaction, toAddress common.Address, err error) types.Receipt {
	return receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
	)
}
