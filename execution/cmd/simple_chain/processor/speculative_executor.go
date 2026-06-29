package processor

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor/pipeline"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// SpeculativeResult holds the result of a speculative EVM execution
type SpeculativeResult struct {
	BlockNum        uint64
	GEI             uint64
	CommitIndex     uint32
	Epoch           uint64
	TimestampMs     uint64
	LeaderAddr      common.Address
	RawBlock        *pb.ExecutableBlock
	Txs             []types.Transaction
	ProcessResult   tx_processor.ProcessResult
	ClonedState     *blockchain.ChainState
	ExecuteErr      error
	IsEpochBoundary bool
	AuthRespCh      chan<- *pb.ExecuteBlockResponse
}

// SpeculativeExecutor handles background execution of incoming consensus commits
type SpeculativeExecutor struct {
	bp             *BlockProcessor
	resultChan     chan *SpeculativeResult
	activeSessions sync.Map      // Track speculative sessions by GEI
	concurrencySem chan struct{} // Bounded concurrency (max 2 sessions)
}

// NewSpeculativeExecutor creates a new SpeculativeExecutor
func NewSpeculativeExecutor(bp *BlockProcessor) *SpeculativeExecutor {
	return &SpeculativeExecutor{
		bp:             bp,
		resultChan:     make(chan *SpeculativeResult, 1000),
		concurrencySem: make(chan struct{}, 2), // Max 2 parallel speculative EVMs
	}
}

// ExecuteSpeculative starts background execution of a commit
func (se *SpeculativeExecutor) ExecuteSpeculative(epochData *pb.ExecutableBlock, lastBlockHeader types.BlockHeader, authRespCh chan<- *pb.ExecuteBlockResponse) {
	se.concurrencySem <- struct{}{} // Acquire concurrency slot
	go func() {
		defer func() {
			<-se.concurrencySem // Release concurrency slot
		}()

		gei := epochData.GetGlobalExecIndex()
		commitIndex := epochData.GetCommitIndex()
		epochNum := epochData.GetEpoch()

		// 1. Determine block number
		blockNum := epochData.GetBlockNumber()
		if blockNum == 0 {
			// Fallback block number calculation if Rust doesn't provide
			lastCommittedBlockNumber := storage.GetLastAssignedBlockNumber()
			if lastCommittedBlockNumber == 0 {
				lastCommittedBlockNumber = storage.GetLastBlockNumber()
			}
			blockNum = lastCommittedBlockNumber + 1
		}

		// 2. Prepare transactions (Deduplicate and sort lexicographically by TxHash)
		allTransactions := PrepareTransactions(epochData)

		// 3. Epoch boundary check
		isEpochBoundary := lastBlockHeader.Epoch() > 0 && epochNum > lastBlockHeader.Epoch()

		// 4. Handle empty block speculative shortcut (REMOVED)
		// We no longer skip empty blocks. They will be executed and created to ensure 100% fork-safety and sequential block progression.

		// 5. Clone chainState
		csCopy, err := se.bp.chainState.CloneSpeculative(lastBlockHeader)
		if err != nil {
			logger.Error("❌ [SPECULATIVE] Failed to clone ChainState for GEI=%d: %v", gei, err)
			if authRespCh != nil {
				authRespCh <- &pb.ExecuteBlockResponse{
					Success:      false,
					Error:        err.Error(),
					ActualGei:    gei,
					BlockNumber:  blockNum,
					GeisConsumed: 0,
				}
			}
			return
		}

		// 6. (Preload accounts removed - now handled exclusively in block_stm.go)

		// 7. Deterministic timestamp
		commitTimestampMs := epochData.GetCommitTimestampMs()
		if commitTimestampMs == 0 {
			commitTimestampMs = lastBlockHeader.TimeStamp() + 1000
		}
		blockTimeSec := commitTimestampMs / 1000

		// 8. Deterministic leader
		leaderAddr := se.bp.GetLeaderAddress(epochData.GetLeaderAddress(), epochData.GetLeaderAuthorIndex())

		// 9. Execute EVM speculatively
		items := make([]grouptxns.Item, 0, len(allTransactions))
		for i, tx := range allTransactions {
			items = append(items, grouptxns.Item{
				ID:    i,
				Array: grouptxns.BuildDeterministicGroupAddrs(tx),
				Tx:    tx,
			})
		}
		groupedGroups := grouptxns.GroupTransactionsDeterministic(items)

		// (Wait for preload removed)

		logger.Info("🔄 [SPECULATIVE] Executing GEI=%d speculatively with %d txs (block #%d)", gei, len(allTransactions), blockNum)
		startTime := time.Now()
		accumulatedResults, execErr := tx_processor.ProcessTransactions(context.Background(), csCopy, groupedGroups, false, true, blockTimeSec, leaderAddr, blockNum, true)
		execDuration := time.Since(startTime)
		pipeline.GlobalBlockTraceStore.AddConsensusAndExecTime(blockNum, len(accumulatedResults.Transactions), 0, execDuration.Microseconds())

		res := &SpeculativeResult{
			BlockNum:        blockNum,
			GEI:             gei,
			CommitIndex:     commitIndex,
			Epoch:           epochNum,
			TimestampMs:     commitTimestampMs,
			LeaderAddr:      leaderAddr,
			RawBlock:        epochData,
			Txs:             allTransactions,
			ProcessResult:   accumulatedResults,
			ClonedState:     csCopy,
			ExecuteErr:      execErr,
			IsEpochBoundary: isEpochBoundary,
			AuthRespCh:      authRespCh,
		}

		se.activeSessions.Store(gei, res)
		se.resultChan <- res
	}()
}

// ResultChan returns the result channel of the SpeculativeExecutor
func (se *SpeculativeExecutor) ResultChan() <-chan *SpeculativeResult {
	return se.resultChan
}

// CleanGEI cleans speculative results older than a GEI
func (se *SpeculativeExecutor) CleanGEI(gei uint64) {
	se.activeSessions.Range(func(key, value interface{}) bool {
		k := key.(uint64)
		if k <= gei {
			se.activeSessions.Delete(k)
		}
		return true
	})
}

// GetSpeculativeResult returns speculative result by GEI if available
func (se *SpeculativeExecutor) GetSpeculativeResult(gei uint64) (*SpeculativeResult, bool) {
	val, ok := se.activeSessions.Load(gei)
	if !ok {
		return nil, false
	}
	return val.(*SpeculativeResult), true
}

// StartCommitterLoop starts the sequential committer loop
func (bp *BlockProcessor) StartCommitterLoop() {
	logger.Info("🎧 [COMMITTER] Starting loop to commit speculative execution results...")

	var nextExpectedGEI uint64

	// Initialize nextExpectedGEI from DB
	lastGEI := storage.GetLastGlobalExecIndex()
	if lastGEI > 0 {
		nextExpectedGEI = lastGEI + 1
	} else {
		lastBlock := bp.GetLastBlock()
		if lastBlock != nil {
			nextExpectedGEI = lastBlock.Header().BlockNumber() + 1
		} else {
			nextExpectedGEI = 1
		}
	}

	epochFileLogger, _ := loggerfile.NewFileLogger("runSocketExecutor_committer.log")

	for range bp.speculativeExecutor.ResultChan() {
		// Vòng lặp kiểm tra và xử lý tuần tự các GEI liên tục đã hoàn thành
		for {
			// Fast-forward nextExpectedGEI if BlockSyncer has advanced the DB
			lastGEI := storage.GetLastGlobalExecIndex()
			if lastGEI >= nextExpectedGEI {
				logger.Warn("⚠️ [COMMITTER] Fast-forwarding nextExpectedGEI from %d to %d (BlockSyncer caught up via P2P)", nextExpectedGEI, lastGEI+1)
				// Clean up stale sessions that were skipped
				// for i := nextExpectedGEI; i <= lastGEI; i++ {
				// 	bp.speculativeExecutor.CleanGEI(i)
				// }
				// nextExpectedGEI = lastGEI + 1
			}

			specRes, exists := bp.speculativeExecutor.GetSpeculativeResult(nextExpectedGEI)
			if !exists {
				break
			}

			// BATCH-DRAIN OPTIMIZATION REMOVED: Every empty commit is now sequentially processed to prevent gaps.

			err := bp.commitSpeculativeResult(specRes, epochFileLogger)
			if err != nil {
				logger.Error("❌ [COMMITTER] Failed to commit speculative result for GEI=%d: %v", nextExpectedGEI, err)
			}

			// Dọn dẹp session cũ
			bp.speculativeExecutor.CleanGEI(nextExpectedGEI)
			nextExpectedGEI++
		}
	}
}

// commitSpeculativeResult commits a single speculative execution result
func (bp *BlockProcessor) commitSpeculativeResult(res *SpeculativeResult, fileLogger *loggerfile.FileLogger) (commitErr error) {
	logger.Info("📥 [COMMITTER] Processing speculative commit: GEI=%d, block=#%d, txs=%d", res.GEI, res.BlockNum, len(res.ProcessResult.Transactions))

	lastBlock := bp.GetLastBlock()
	var currentBlockNumber uint64
	if lastBlock != nil {
		currentBlockNumber = lastBlock.Header().BlockNumber()
	} else {
		currentBlockNumber = storage.GetLastBlockNumber()
	}

	// Defer sending authoritative execution response back to Rust
	defer func() {
		if res.AuthRespCh != nil {
			if commitErr != nil {
				res.AuthRespCh <- &pb.ExecuteBlockResponse{
					Success:      false,
					Error:        commitErr.Error(),
					ActualGei:    res.GEI,
					BlockNumber:  currentBlockNumber,
					GeisConsumed: 0,
				}
			} else {
				var stateRoot []byte
				if bp.chainState != nil && bp.chainState.GetAccountStateDB() != nil {
					stateRoot = bp.chainState.GetAccountStateDB().Trie().Hash().Bytes()
				}
				res.AuthRespCh <- &pb.ExecuteBlockResponse{
					Success:      true,
					ActualGei:    res.GEI,
					BlockNumber:  currentBlockNumber,
					GeisConsumed: 1,
					StateRoot:    stateRoot,
				}
			}
		}
	}()

	// 1. Kiểm tra conflict bằng cách so sánh Parent Hash
	var hasConflict bool
	if lastBlock == nil {
		hasConflict = false // Genesis
	} else if res.ClonedState == nil {
		hasConflict = false // Empty block, no speculative state, no conflict
	} else {
		parentHash := lastBlock.Header().Hash()
		var specParentHash common.Hash
		specHeaderPtr := res.ClonedState.GetcurrentBlockHeader()
		if specHeaderPtr != nil && *specHeaderPtr != nil {
			specParentHash = (*specHeaderPtr).Hash()
		}
		if specParentHash != parentHash {
			logger.Warn("🔄 [COMMITTER-CONFLICT] Conflict detected: specParentHash=%s ≠ actualParentHash=%s for GEI=%d. Re-executing sequentially.",
				specParentHash.Hex()[:16], parentHash.Hex()[:16], res.GEI)
			hasConflict = true
		}
	}

	if res.ExecuteErr != nil {
		logger.Warn("⚠️ [COMMITTER-EXEC-ERR] Speculative execution had error for GEI=%d: %v. Re-executing sequentially.", res.GEI, res.ExecuteErr)
		hasConflict = true
	}

	var accumulatedResults tx_processor.ProcessResult

	bp.ExecutionMutex.RLock()
	defer bp.ExecutionMutex.RUnlock()

	// 2. Xử lý trường hợp Conflict -> Re-execute tuần tự
	if hasConflict && len(res.Txs) > 0 {
		logger.Info("🔄 [COMMITTER] Re-executing GEI=%d sequentially...", res.GEI)

		// Clone state mới từ tip thực tế hiện tại
		csCopy, cloneErr := bp.chainState.CloneSpeculative(lastBlock.Header())
		if cloneErr != nil {
			commitErr = fmt.Errorf("failed to clone ChainState for sequential re-execution: %w", cloneErr)
			return commitErr
		}

		// Preload
		// (Preload removed)

		blockTimeSec := res.TimestampMs / 1000

		items := make([]grouptxns.Item, 0, len(res.Txs))
		for i, tx := range res.Txs {
			items = append(items, grouptxns.Item{
				ID:    i,
				Array: grouptxns.BuildDeterministicGroupAddrs(tx),
				Tx:    tx,
			})
		}
		groupedGroups := grouptxns.GroupTransactionsDeterministic(items)

		// (Wait removed)

		accumulatedResults, commitErr = tx_processor.ProcessTransactions(context.Background(), csCopy, groupedGroups, false, true, blockTimeSec, res.LeaderAddr, res.BlockNum, true)
		if commitErr != nil {
			commitErr = fmt.Errorf("sequential re-execution failed: %w", commitErr)
			return commitErr
		}

		// Gán database của ChainState sang csCopy database
		bp.chainState.SetAccountStateDB(csCopy.GetAccountStateDB())
		bp.chainState.SetSmartContractDB(csCopy.GetSmartContractDB())
		bp.chainState.SetStakeStateDB(csCopy.GetStakeStateDB())
	} else if len(res.Txs) > 0 {
		// Không conflict -> Sử dụng speculative results và database đã thực thi sẵn
		accumulatedResults = res.ProcessResult

		// Gán database của ChainState sang res.ClonedState database
		bp.chainState.SetAccountStateDB(res.ClonedState.GetAccountStateDB())
		bp.chainState.SetSmartContractDB(res.ClonedState.GetSmartContractDB())
		bp.chainState.SetStakeStateDB(res.ClonedState.GetStakeStateDB())
	}

	// 3. Tiến hành tạo block và commit

	// Trường hợp block trống (REMOVED)
	// We no longer skip block creation for empty blocks to guarantee zero gaps and 100% no-fork.

	if len(res.Txs) == 0 {
		// Ghost-block-guard: 0 transactions, tạo block trống để tránh gap
		emptyResult := tx_processor.ProcessResult{Transactions: nil, Receipts: nil}
		lastB := bp.GetLastBlock()
		if lastB != nil {
			emptyResult.Root = lastB.Header().AccountStatesRoot()
			emptyResult.StakeStatesRoot = lastB.Header().StakeStatesRoot()
		}
		batchID := fmt.Sprintf("SYNC-%d-%d", res.GEI, time.Now().UnixNano())

		currentBlockNumber = res.BlockNum
		storage.UpdateLastAssignedBlockNumber(currentBlockNumber)

		emptyBlock := bp.createBlockFromResults(emptyResult, currentBlockNumber, res.Epoch, true, batchID, res.TimestampMs, res.GEI, res.CommitIndex, res.LeaderAddr)
		if emptyBlock != nil {
			select {
			case bp.createdBlocksChan <- emptyBlock:
			default:
				bp.createdBlocksChan <- emptyBlock
			}
		}
		bp.PushAsyncGEIUpdate(res.GEI, res.RawBlock.GetCommitHash(), res.CommitIndex, res.Epoch)
		return nil
	}

	// Tạo block chính thức từ kết quả thực thi
	batchID := fmt.Sprintf("E%dC%dG%d", res.Epoch, res.CommitIndex, res.GEI)
	currentBlockNumber = res.BlockNum
	storage.UpdateLastAssignedBlockNumber(currentBlockNumber)

	newBlock := bp.createBlockFromResults(accumulatedResults, currentBlockNumber, res.Epoch, true, batchID, res.TimestampMs, res.GEI, res.CommitIndex, res.LeaderAddr)
	if newBlock == nil {
		commitErr = fmt.Errorf("failed to create block from speculative results (verifyDraftBlock reverted block)")
		return commitErr
	}

	// Lưu SystemTransactions nếu có
	sysTxs := res.RawBlock.GetSystemTransactions()
	if len(sysTxs) > 0 {
		err := bp.chainState.GetBlockDatabase().SaveSystemTransactions(currentBlockNumber, sysTxs)
		if err != nil {
			logger.Error("❌ [SYSTEM-TX] Failed to save SystemTransactions for block #%d: %v", currentBlockNumber, err)
		}
	}

	// Gửi block tới sub-nodes channel
	select {
	case bp.createdBlocksChan <- newBlock:
	default:
		bp.createdBlocksChan <- newBlock
	}

	bp.PushAsyncGEIUpdate(res.GEI, res.RawBlock.GetCommitHash(), res.CommitIndex, res.Epoch)
	return nil
}

// PrepareTransactions processes the raw ExecutableBlock transactions:
// 1. Unmarshal and collect all transactions from the DAG blocks
// 2. Filter out duplicate transactions by TxHash
// 3. Sort transactions lexicographically by TxHash (bytes comparison)
// 4. Returns a deterministic, sorted slice of transactions
func PrepareTransactions(epochData *pb.ExecutableBlock) []types.Transaction {
	if epochData == nil {
		return nil
	}

	// 1. Unmarshal all transactions in parallel
	rawTxs := ParallelUnmarshalTransactions(epochData.Transactions)

	// 2. Deduplicate transactions by TxHash
	seenTxs := make(map[common.Hash]bool, len(rawTxs))
	dedupedTxs := make([]types.Transaction, 0, len(rawTxs))
	for _, tx := range rawTxs {
		hash := tx.Hash()
		if seenTxs[hash] {
			continue // Skip duplicates
		}
		seenTxs[hash] = true
		dedupedTxs = append(dedupedTxs, tx)
	}

	// 3. Sort lexicographically by TxHash (bytes comparison)
	sort.Slice(dedupedTxs, func(i, j int) bool {
		hashI := dedupedTxs[i].Hash()
		hashJ := dedupedTxs[j].Hash()
		return bytes.Compare(hashI.Bytes(), hashJ.Bytes()) < 0
	})

	return dedupedTxs
}

// ParallelUnmarshalTransactions decodes transaction digests in parallel using multiple CPU workers.
func ParallelUnmarshalTransactions(txs []*pb.TransactionExe) []types.Transaction {
	if len(txs) == 0 {
		return nil
	}

	numTxs := len(txs)
	numWorkers := runtime.NumCPU()
	if numWorkers > 16 {
		numWorkers = 16
	}
	if numTxs < 200 {
		numWorkers = 1 // Sequential is faster for small slices due to goroutine scheduling overhead
	}

	if numWorkers <= 1 {
		res := make([]types.Transaction, 0, numTxs)
		for _, ms := range txs {
			if len(ms.Digest) == 0 {
				continue
			}
			if len(ms.Digest) == 64 {
				isZero := true
				for _, b := range ms.Digest {
					if b != 0 {
						isZero = false
						break
					}
				}
				if isZero {
					continue
				}
			}
			singleTx, err := transaction.UnmarshalTransaction(ms.Digest)
			if err == nil {
				res = append(res, singleTx)
				continue
			}
			multiTxs, err := transaction.UnmarshalTransactions(ms.Digest)
			if err == nil {
				res = append(res, multiTxs...)
			}
		}
		return res
	}

	chunks := make([][]types.Transaction, numWorkers)
	chunkSize := (numTxs + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		if start >= numTxs {
			break
		}
		end := start + chunkSize
		if end > numTxs {
			end = numTxs
		}

		wg.Add(1)
		go func(workerID int, slice []*pb.TransactionExe) {
			defer wg.Done()
			localTxs := make([]types.Transaction, 0, len(slice))
			for _, ms := range slice {
				if len(ms.Digest) == 0 {
					continue
				}
				if len(ms.Digest) == 64 {
					isZero := true
					for _, b := range ms.Digest {
						if b != 0 {
							isZero = false
							break
						}
					}
					if isZero {
						continue
					}
				}
				singleTx, err := transaction.UnmarshalTransaction(ms.Digest)
				if err == nil {
					localTxs = append(localTxs, singleTx)
					continue
				}
				multiTxs, err := transaction.UnmarshalTransactions(ms.Digest)
				if err == nil {
					localTxs = append(localTxs, multiTxs...)
				}
			}
			chunks[workerID] = localTxs
		}(w, txs[start:end])
	}
	wg.Wait()

	totalLen := 0
	for _, chunk := range chunks {
		totalLen += len(chunk)
	}
	res := make([]types.Transaction, 0, totalLen)
	for _, chunk := range chunks {
		res = append(res, chunk...)
	}
	return res
}
