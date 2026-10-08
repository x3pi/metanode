package processor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
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

// ffiTraceEnabled gates the [FFI-TRACE] diagnostics added while profiling the
// Rust<->Go block-delivery round trip (see executor/ffi_bridge.go's
// same-named flag for the full rationale: opt-in via METANODE_FFI_TRACE=true,
// off by default since these are logger.Warn firing on every block).
var ffiTraceEnabled = os.Getenv("METANODE_FFI_TRACE") == "true"

// SpeculativeResult holds the result of a speculative EVM execution
type SpeculativeResult struct {
	mu              sync.Mutex
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
	// session is the in-flight record whose respCh a Rust retry may REPLACE while this result is still waiting for
	// the committer; respCh() always reads the latest one (a snapshot taken when execution finished would answer a
	// channel Rust already abandoned, costing it another full executeBlockResponseTimeout).
	session *inFlightSession
	// IsFinished distinguishes a real, completed result from the placeholder
	// registered in activeSessions at dispatch time (see ExecuteSpeculative).
	// Without this, GetSpeculativeResult could return the empty placeholder
	// to the committer before execution has actually produced anything.
	IsFinished bool
	// precomputeRootsChan receives precomputed Merkle roots calculated concurrently in background
	precomputeRootsChan chan *PrecomputedBlockRoots
}

// TakeClonedState atomically claims ownership of ClonedState.
// Returns nil if already claimed or discarded.
func (res *SpeculativeResult) TakeClonedState() *blockchain.ChainState {
	if res == nil {
		return nil
	}
	res.mu.Lock()
	defer res.mu.Unlock()
	cs := res.ClonedState
	res.ClonedState = nil
	return cs
}

// AbortClonedState atomically claims and aborts the speculative state.
func (res *SpeculativeResult) AbortClonedState() {
	if res == nil {
		return
	}
	res.mu.Lock()
	cs := res.ClonedState
	res.ClonedState = nil
	res.mu.Unlock()
	if cs != nil {
		cs.AbortSpeculative()
	}
}

// respCh returns the response channel of the most recent caller waiting on this result.
func (res *SpeculativeResult) respCh() chan<- *pb.ExecuteBlockResponse {
	return res.AuthResponseChannel()
}

// SetAuthRespCh atomically sets the AuthRespCh channel.
func (res *SpeculativeResult) SetAuthRespCh(ch chan<- *pb.ExecuteBlockResponse) {
	if res == nil {
		return
	}
	if res.session != nil {
		res.session.mu.Lock()
		res.session.respCh = ch
		res.session.mu.Unlock()
	}
	res.mu.Lock()
	defer res.mu.Unlock()
	res.AuthRespCh = ch
}

// AuthResponseChannel atomically returns the AuthRespCh channel.
func (res *SpeculativeResult) AuthResponseChannel() chan<- *pb.ExecuteBlockResponse {
	if res == nil {
		return nil
	}
	if res.session != nil {
		res.session.mu.Lock()
		ch := res.session.respCh
		res.session.mu.Unlock()
		if ch != nil {
			return ch
		}
	}
	res.mu.Lock()
	defer res.mu.Unlock()
	return res.AuthRespCh
}

// getStateRootForBlock retrieves the AccountStatesRoot for a committed block by block number.
// Returns nil if the block or state root cannot be found in BlockDatabase.
func getStateRootForBlock(bp *BlockProcessor, blockNum uint64) []byte {
	if bp == nil || bp.chainState == nil {
		return nil
	}
	bc := blockchain.GetBlockChainInstance()
	if bc == nil {
		return nil
	}
	blockHash, ok := bc.GetBlockHashByNumber(blockNum)
	if !ok {
		return nil
	}
	blockDb := bp.chainState.GetBlockDatabase()
	if blockDb == nil {
		return nil
	}
	block, err := blockDb.GetBlockByHash(blockHash)
	if err != nil || block == nil {
		return nil
	}
	return block.Header().AccountStatesRoot().Bytes()
}

// SpeculativeExecutor handles background execution of incoming consensus commits
type SpeculativeExecutor struct {
	bp             *BlockProcessor
	resultChan     chan *SpeculativeResult
	activeSessions sync.Map      // Completed speculative results by GEI
	concurrencySem chan struct{} // Bounded concurrency (max 2 sessions)
	inFlight       sync.Map      // GEI (uint64) -> *inFlightSession, executions currently running
	activeWorkers  atomic.Int32  // Number of EVM speculative worker goroutines currently executing

	// committedThrough is the highest GEI whose commit (or sequential re-execution) the committer has finished;
	// commitWake is closed and replaced on every advance so waiters can block without polling.
	commitMu         sync.Mutex
	committedThrough uint64
	commitWake       chan struct{}
}

// inFlightSession tracks the caller currently waiting on a GEI's speculative
// execution. When Rust retries a block after the CGO response timeout in
// ffi_bridge.go (executeBlockResponseTimeout) elapses, the original caller's
// response channel is already abandoned — Rust gave up on it and is now
// blocked on a brand-new channel for the retry. We only need to remember the
// most recent one to respond to.
type inFlightSession struct {
	mu     sync.Mutex
	respCh chan<- *pb.ExecuteBlockResponse
	cancel context.CancelFunc
}


// NewSpeculativeExecutor creates a new SpeculativeExecutor
func NewSpeculativeExecutor(bp *BlockProcessor) *SpeculativeExecutor {
	return &SpeculativeExecutor{
		bp:             bp,
		resultChan:     make(chan *SpeculativeResult, 1000),
		concurrencySem: make(chan struct{}, 2), // Max 2 parallel speculative EVMs
		commitWake:     make(chan struct{}),
	}
}

// ExecuteSpeculative starts background execution of a commit. If a previous
// call for the same GEI is still executing (Rust retried after the Go-side
// CGO response timeout fired while the original attempt was still running —
// see executeBlockResponseTimeout in ffi_bridge.go), this does NOT start a
// second concurrent execution: two overlapping executions of the same GEI
// share deterministic MVM/EVM handle IDs derived from the block content and
// previously caused fatal "concurrent map writes" crashes. Instead the new
// caller's response channel is attached to the one execution already running,
// and this returns immediately.
func (se *SpeculativeExecutor) ExecuteSpeculative(epochData *pb.ExecutableBlock, lastBlockHeader types.BlockHeader, authRespCh chan<- *pb.ExecuteBlockResponse) {
	gei := epochData.GetGlobalExecIndex()
	tDispatch := time.Now().UnixNano() // [FFI-TRACE] dequeued from authQueue by processRustEpochData

	ctx, cancel := context.WithCancel(context.Background())
	session := &inFlightSession{respCh: authRespCh, cancel: cancel}
	if existing, loaded := se.inFlight.LoadOrStore(gei, session); loaded {
		existingSession := existing.(*inFlightSession)
		existingSession.mu.Lock()
		existingSession.respCh = authRespCh
		existingSession.mu.Unlock()
		cancel() // Cancel unused newly created context

		// If this GEI already produced a finished speculative result waiting in activeSessions
		// (e.g. committer had a transient failure or was waiting), update its AuthRespCh and wake up committer!
		if val, ok := se.activeSessions.Load(gei); ok {
			if res, ok := val.(*SpeculativeResult); ok && res != nil && res.IsFinished {
				res.SetAuthRespCh(authRespCh)
				logger.Warn("🔄 [SPECULATIVE] GEI=%d already finished but uncommitted — notifying committer to retry", gei)
				select {
				case se.resultChan <- res:
				default:
				}
				return
			}
		}

		logger.Warn("⏳ [SPECULATIVE] GEI=%d already executing — attaching retry's response channel instead of starting a duplicate execution", gei)
		return
	}

	// 1. Determine block number (computed up front so the placeholder below
	// can carry an accurate BlockNum for CleanGEI's rescue response).
	blockNum := epochData.GetBlockNumber()
	if blockNum == 0 {
		// Fallback block number calculation if Rust doesn't provide
		lastCommittedBlockNumber := storage.GetLastAssignedBlockNumber()
		if lastCommittedBlockNumber == 0 {
			lastCommittedBlockNumber = storage.GetLastBlockNumber()
		}
		blockNum = lastCommittedBlockNumber + 1
	}

	// [FIX]: Bypass speculative execution if already committed to DB.
	// This prevents infinite FFI timeouts when Rust retries a block that Go already
	// finished executing and committed (e.g. if the original execution took >10s).
	lastGEI := storage.GetLastGlobalExecIndex()
	if gei <= lastGEI {
		logger.Warn("⚠️ [SPECULATIVE] GEI=%d (block #%d) is already committed (lastGEI=%d), bypassing execution to unblock Rust", gei, blockNum, lastGEI)
		se.verifyBypassBlock(blockNum, gei, epochData)
		se.inFlight.Delete(gei) // Clean up placeholder

		if authRespCh != nil {
			stateRoot := getStateRootForBlock(se.bp, blockNum)

			// ZERO-FORK INVARIANT: Never return Success: true with a nil StateRoot!
			if stateRoot == nil {
				logger.Warn("⚠️ [SPECULATIVE] Cannot find valid stateRoot for block #%d (gei=%d). Rejecting bypass to prevent Zero-Fork violation.", blockNum, gei)
				resp := &pb.ExecuteBlockResponse{
					Success:      false,
					Error:        fmt.Sprintf("stateRoot not found for committed block #%d", blockNum),
					ActualGei:    gei,
					BlockNumber:  blockNum,
					GeisConsumed: 0,
					StateRoot:    nil,
				}
				select {
				case authRespCh <- resp:
				default:
				}
				return
			}

			resp := &pb.ExecuteBlockResponse{
				Success:      true,
				ActualGei:    gei,
				BlockNumber:  blockNum,
				GeisConsumed: 1,
				StateRoot:    stateRoot,
			}

			select {
			case authRespCh <- resp:
			default:
			}
		}
		return
	}

	// [FIX DEADLOCK RACE CONDITION]: Register a placeholder immediately in the
	// calling goroutine (IsFinished=false) so CleanGEI can always find and
	// rescue this session — even if the goroutine below never reaches the
	// point where it stores a real result (e.g. it hangs).
	se.activeSessions.Store(gei, &SpeculativeResult{
		GEI:        gei,
		BlockNum:   blockNum,
		AuthRespCh: authRespCh,
		session:    session,
	})

	se.concurrencySem <- struct{}{} // Acquire concurrency slot
	se.activeWorkers.Add(1)
	go func() {
		tGoroutineStart := time.Now().UnixNano() // [FFI-TRACE] past the semaphore + goroutine-scheduling delay
		defer func() {
			se.activeWorkers.Add(-1)
			<-se.concurrencySem // Release concurrency slot
		}()

		// 🔒 CONCURRENCY & FORK-SAFETY GATE:
		// Acquire ExecutionMutex.RLock() so that when P2P Block Sync holds ExecutionMutex.Lock(),
		// this speculative EVM goroutine waits and cannot run concurrently with Block Sync.
		if se.bp != nil {
			se.bp.ExecutionMutex.RLock()
			defer se.bp.ExecutionMutex.RUnlock()
		}

		// ZERO-FORK SERIALIZATION GATE:
		// Blockchain state transition S_N = f(S_{N-1}, Block_N) strictly requires S_{N-1}
		// to be fully committed before Block N clones state and executes EVM transactions.
		if gei > 1 && !se.isCommitted(gei-1) {
			if se.bp != nil {
				se.bp.ExecutionMutex.RUnlock()
			}
			err := se.waitCommitted(ctx, gei-1)
			if se.bp != nil {
				se.bp.ExecutionMutex.RLock()
			}
			if err != nil {
				logger.Warn("⚠️ [SPECULATIVE] GEI=%d waitCommitted(gei-1=%d) aborted: %v", gei, gei-1, err)
				se.activeSessions.Delete(gei)
				se.inFlight.Delete(gei)
				return
			}
		}

		// If block was committed to DB while waiting for the lock (e.g. by P2P Sync),
		// bypass speculative execution immediately and unblock Rust.
		lastCommittedGEI := storage.GetLastGlobalExecIndex()
		if gei <= lastCommittedGEI {
			logger.Warn("⚠️ [SPECULATIVE] GEI=%d (block #%d) was already committed to DB while waiting for execution lock (lastGEI=%d). Bypassing.", gei, blockNum, lastCommittedGEI)
			se.verifyBypassBlock(blockNum, gei, epochData)
			se.inFlight.Delete(gei)
			se.activeSessions.Delete(gei)
			session.mu.Lock()
			latestRespCh := session.respCh
			session.mu.Unlock()
			if latestRespCh != nil {
				stateRoot := getStateRootForBlock(se.bp, blockNum)
				if stateRoot == nil {
					logger.Warn("⚠️ [SPECULATIVE] Cannot find valid stateRoot for block #%d (gei=%d) on lock-wait bypass. Rejecting bypass to prevent Zero-Fork violation.", blockNum, gei)
					resp := &pb.ExecuteBlockResponse{
						Success:      false,
						Error:        fmt.Sprintf("stateRoot not found for committed block #%d", blockNum),
						ActualGei:    gei,
						BlockNumber:  blockNum,
						GeisConsumed: 0,
						StateRoot:    nil,
					}
					select {
					case latestRespCh <- resp:
					default:
					}
					return
				}
				select {
				case latestRespCh <- &pb.ExecuteBlockResponse{
					Success:      true,
					ActualGei:    gei,
					BlockNumber:  blockNum,
					GeisConsumed: 0,
					StateRoot:    stateRoot,
				}:
				default:
				}
			}
			return
		}

		if ctx.Err() != nil {
			logger.Warn("⚠️ [SPECULATIVE] GEI=%d (block #%d) execution was cancelled (aborted by Sync/Consensus). Discarding state.", gei, blockNum)
			se.activeSessions.Delete(gei)
			se.inFlight.Delete(gei)
			return
		}

		// Refresh lastBlockHeader after predecessor commit completes
		if se.bp != nil {
			if latestBlock := se.bp.GetLastBlock(); latestBlock != nil {
				lastBlockHeader = latestBlock.Header()
			}
		}

		// NOTE: inFlight[gei] is intentionally NOT deleted here. Deleting it as
		// soon as this goroutine returns raced against the asynchronous
		// GEI-persistence pipeline (PushAsyncGEIUpdate -> geiUpdateChan ->
		// geiWorker -> commitChannel -> storage.UpdateLastGlobalExecIndex),
		// which can lag well behind the actual commit under load. A retry
		// landing in that gap saw neither inFlight[gei] (already deleted) nor
		// gei <= lastGEI (not yet updated) and fell through to a full,
		// redundant re-execution — corrupting the global, non-versioned
		// Xapian DB (XapianManager::instances is a single shared static
		// registry, not cloned per speculative session like account/token
		// state). inFlight[gei] is now deleted by CleanGEI instead, tying its
		// lifetime to the committer's own authoritative completion signal
		// instead of this goroutine's unrelated return timing.
		commitIndex := epochData.GetCommitIndex()
		epochNum := epochData.GetEpoch()

		// 2. Prepare transactions (Deduplicate and sort lexicographically by TxHash)
		allTransactions := PrepareTransactions(epochData)

		// 3. Epoch boundary check
		isEpochBoundary := lastBlockHeader.Epoch() > 0 && epochNum > lastBlockHeader.Epoch()

		// 4. Handle empty block speculative shortcut (REMOVED)
		// We no longer skip empty blocks. They will be executed and created to ensure 100% fork-safety and sequential block progression.

		// 5. Clone chainState
		tBeforeClone := time.Now().UnixNano()
		csCopy, err := se.bp.chainState.CloneSpeculative(lastBlockHeader)
		tAfterClone := time.Now().UnixNano()
		if err != nil {
			logger.Error("❌ [SPECULATIVE] Failed to clone ChainState for GEI=%d: %v", gei, err)
			session.mu.Lock()
			latestRespCh := session.respCh
			session.mu.Unlock()
			if latestRespCh != nil {
				latestRespCh <- &pb.ExecuteBlockResponse{
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
		tBeforeGroup := time.Now().UnixNano()
		groupedGroups := grouptxns.GroupTransactionsDeterministic(items, csCopy.HasCode)
		tAfterGroup := time.Now().UnixNano()

		// (Wait for preload removed)

		logger.Info("🔄 [SPECULATIVE] Executing GEI=%d speculatively with %d txs (block #%d)", gei, len(allTransactions), blockNum)
		startTime := time.Now()
		gatedCtx := tx_processor.WithIRGate(ctx, se.newIRGate(gei, lastBlockHeader.Hash(), func() (common.Hash, bool) {
			tip := se.bp.GetLastBlock()
			if tip == nil {
				return common.Hash{}, false
			}
			return tip.Header().Hash(), true
		}, &executionLock{release: se.bp.ExecutionMutex.RUnlock, reacquire: se.bp.ExecutionMutex.RLock}))
		accumulatedResults, execErr := tx_processor.ProcessTransactions(gatedCtx, csCopy, groupedGroups, false, true, blockTimeSec, leaderAddr, blockNum, true)
		execDuration := time.Since(startTime)
		pipeline.GlobalBlockTraceStore.AddConsensusAndExecTime(blockNum, len(accumulatedResults.Transactions), 0, execDuration.Microseconds())
		if ffiTraceEnabled {
			logger.Warn("⏱️ [FFI-TRACE] gei=%d stage=GO_SPEC goroutine_sched_ns=%d prepare_tx_ns=%d clone_ns=%d group_ns=%d exec_ns=%d",
				gei, tGoroutineStart-tDispatch, tBeforeClone-tGoroutineStart, tAfterClone-tBeforeClone, tAfterGroup-tBeforeGroup, execDuration.Nanoseconds())
		}

		if ctx.Err() != nil {
			logger.Warn("⚠️ [SPECULATIVE] GEI=%d (block #%d) execution was cancelled (aborted by Sync/Consensus). Discarding state.", gei, blockNum)
			if csCopy != nil {
				csCopy.AbortSpeculative()
			}
			se.activeSessions.Delete(gei)
			se.inFlight.Delete(gei)
			return
		}

		session.mu.Lock()
		latestRespCh := session.respCh
		session.mu.Unlock()

		// Precompute Merkle Roots concurrently in background right after EVM finishes
		precomputeRootsCh := make(chan *PrecomputedBlockRoots, 1)
		if execErr == nil && len(accumulatedResults.Transactions) > 0 && se.bp != nil {
			go func(txs []types.Transaction, rps []types.Receipt, bNum uint64, g uint64) {
				precomputeRootsCh <- se.bp.PrecomputeRoots(txs, rps, bNum, g)
			}(accumulatedResults.Transactions, accumulatedResults.Receipts, blockNum, gei)
		} else {
			precomputeRootsCh <- nil
		}

		res := &SpeculativeResult{
			BlockNum:            blockNum,
			GEI:                 gei,
			CommitIndex:         commitIndex,
			Epoch:               epochNum,
			TimestampMs:         commitTimestampMs,
			LeaderAddr:          leaderAddr,
			RawBlock:            epochData,
			Txs:                 allTransactions,
			ProcessResult:       accumulatedResults,
			ClonedState:         csCopy,
			ExecuteErr:          execErr,
			IsEpochBoundary:     isEpochBoundary,
			AuthRespCh:          latestRespCh,
			session:             session,
			IsFinished:          true,
			precomputeRootsChan: precomputeRootsCh,
		}

		// [FIX DEADLOCK / LEAK]: Check if block has already been committed to DB
		// (e.g. by P2P Sync or Committer fast-forward while this goroutine was running).
		lastCommittedGEI = storage.GetLastGlobalExecIndex()
		if gei <= lastCommittedGEI {
			logger.Warn("⚠️ [SPECULATIVE] Execution finished for GEI=%d (block #%d) but block is already committed to DB (lastGEI=%d). Discarding obsolete speculative state immediately.", gei, blockNum, lastCommittedGEI)
			res.AbortClonedState()
			authCh := res.AuthResponseChannel()
			if authCh != nil {
				stateRoot := getStateRootForBlock(se.bp, blockNum)
				if stateRoot == nil {
					logger.Warn("⚠️ [SPECULATIVE] Cannot find valid stateRoot for committed block #%d (gei=%d). Rejecting response to prevent Zero-Fork violation.", blockNum, gei)
					select {
					case authCh <- &pb.ExecuteBlockResponse{
						Success:      false,
						Error:        fmt.Sprintf("stateRoot not found for committed block #%d", blockNum),
						ActualGei:    res.GEI,
						BlockNumber:  res.BlockNum,
						GeisConsumed: 0,
					}:
					default:
					}
				} else {
					select {
					case authCh <- &pb.ExecuteBlockResponse{
						Success:      true, // Block already committed in DB, safe to unblock Rust
						ActualGei:    res.GEI,
						BlockNumber:  res.BlockNum,
						GeisConsumed: 0,
						StateRoot:    stateRoot,
					}:
					default:
					}
				}
			}
			se.activeSessions.Delete(gei)
			se.inFlight.Delete(gei)
			return
		}

		// If CleanGEI already swept the placeholder away (e.g. P2P sync
		// caught up past this GEI while we were executing), don't resurrect
		// it — discard the speculative clone and never push to resultChan.
		if _, exists := se.activeSessions.Load(gei); !exists {
			logger.Warn("⚠️ [SPECULATIVE] Execution finished for GEI=%d but session was aborted by CleanGEI", gei)
			res.AbortClonedState()
			authCh := res.AuthResponseChannel()
			if authCh != nil {
				stateRoot := getStateRootForBlock(se.bp, blockNum)
				if stateRoot == nil {
					logger.Warn("⚠️ [SPECULATIVE] Cannot find valid stateRoot for block #%d (gei=%d) swept by CleanGEI. Rejecting with Success: false to prevent Zero-Fork violation.", blockNum, gei)
					select {
					case authCh <- &pb.ExecuteBlockResponse{
						Success:      false,
						Error:        fmt.Sprintf("session swept by CleanGEI and stateRoot not found for block #%d", blockNum),
						ActualGei:    res.GEI,
						BlockNumber:  res.BlockNum,
						GeisConsumed: 0,
						StateRoot:    nil,
					}:
					default:
					}
				} else {
					select {
					case authCh <- &pb.ExecuteBlockResponse{
						Success:      true, // Session swept by CleanGEI, unblock Rust with valid state root
						ActualGei:    res.GEI,
						BlockNumber:  res.BlockNum,
						GeisConsumed: 0,
						StateRoot:    stateRoot,
					}:
					default:
					}
				}
			}
			se.inFlight.Delete(gei)
			return
		}

		se.activeSessions.Store(gei, res)
		if ffiTraceEnabled {
			logger.Warn("⏱️ [FFI-TRACE] gei=%d stage=GO_PUSHED_TO_COMMITTER t_ns=%d", gei, time.Now().UnixNano())
		}
		se.resultChan <- res
	}()
}

// ResultChan returns the result channel of the SpeculativeExecutor
func (se *SpeculativeExecutor) ResultChan() <-chan *SpeculativeResult {
	return se.resultChan
}

// CancelInFlight cancels active speculative execution workers for the specified GEIs (or all if geis is empty).
func (se *SpeculativeExecutor) CancelInFlight(geis ...uint64) {
	if len(geis) == 0 {
		se.inFlight.Range(func(key, value interface{}) bool {
			if session, ok := value.(*inFlightSession); ok && session != nil {
				session.mu.Lock()
				if session.cancel != nil {
					session.cancel()
				}
				session.mu.Unlock()
			}
			return true
		})
		return
	}
	for _, gei := range geis {
		if val, ok := se.inFlight.Load(gei); ok {
			if session, ok := val.(*inFlightSession); ok && session != nil {
				session.mu.Lock()
				if session.cancel != nil {
					session.cancel()
				}
				session.mu.Unlock()
			}
		}
	}
}

// WaitForInFlight waits for active in-flight speculative workers to drain/exit up to timeout.
func (se *SpeculativeExecutor) WaitForInFlight(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		if se.activeWorkers.Load() <= 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// AbortAllSpeculative forcefully cancels all in-flight speculative execution workers
// and aborts all completed speculative sessions currently waiting in activeSessions.
// It closes and aborts all cloned NOMT sessions immediately, resetting NOMT activeCount to 0
// and sending rescue responses to unblock any waiting Rust FFI channels.
func (se *SpeculativeExecutor) AbortAllSpeculative() {
	// 1. Cancel in-flight EVM worker contexts and wait for them to exit
	se.CancelInFlight()
	se.WaitForInFlight(200 * time.Millisecond)

	// 2. Discard and abort all FinishedSessions in activeSessions
	lastCommittedGEI := storage.GetLastGlobalExecIndex()
	se.activeSessions.Range(func(key, value interface{}) bool {
		gei := key.(uint64)
		if res, ok := value.(*SpeculativeResult); ok && res != nil {
			res.AbortClonedState()
			authCh := res.AuthResponseChannel()
			if authCh != nil {
				var resp *pb.ExecuteBlockResponse
				// ZERO-FORK INVARIANT: If the block was already committed in DB, return Success with verified stateRoot.
				// Otherwise, return Success: false so Rust triggers deliver_with_halt_retry instead of committing uncommitted block!
				if res.GEI <= lastCommittedGEI {
					if root := getStateRootForBlock(se.bp, res.BlockNum); root != nil {
						resp = &pb.ExecuteBlockResponse{
							Success:      true,
							ActualGei:    res.GEI,
							BlockNumber:  res.BlockNum,
							GeisConsumed: 0,
							StateRoot:    root,
						}
					}
				}
				if resp == nil {
					resp = &pb.ExecuteBlockResponse{
						Success:      false,
						Error:        fmt.Sprintf("speculative execution aborted for GEI=%d (block #%d uncommitted)", res.GEI, res.BlockNum),
						ActualGei:    res.GEI,
						BlockNumber:  res.BlockNum,
						GeisConsumed: 0,
					}
				}
				select {
				case authCh <- resp:
					logger.Info("✅ [AbortAllSpeculative] Sent rescue response for GEI=%d (success=%v)", res.GEI, resp.Success)
				default:
				}
			}
		}
		se.activeSessions.Delete(gei)
		se.inFlight.Delete(gei)
		return true
	})
}

// CleanGEI cleans speculative results older than or equal to a target GEI.
//
// ARCHITECTURAL NOTE: Why `if !res.IsFinished` was intentionally removed:
//  1. CleanGEI(gei) is only invoked when `storage.GetLastGlobalExecIndex() >= gei` (either
//     by the committer loop after a successful commit, or when fast-forwarding because
//     P2P BlockSyncer already caught up and persisted blocks directly to DB).
//  2. Therefore, for all k <= gei, the ground-truth state for block k is ALREADY finalized
//     in the DB. Any in-flight background goroutine executing EVM for k is computing on an
//     obsolete snapshot; its result will be discarded anyway.
//  3. If we skipped in-flight sessions (`if !res.IsFinished { return true }`), when those
//     goroutines finish later, the committer has already advanced past k. The session remains
//     orphaned in activeSessions forever, its cloned NOMT state is never closed, leaving
//     `h.activeCount > 0` and causing all future sessions to deadlock indefinitely in `sync.Cond.Wait`.
//  4. By proactively deleting the session and unblocking Rust with `Success: true, GeisConsumed: 0`,
//     Rust knows the block is safely handled (since DB already has it), and the goroutine, upon
//     completing, will detect that `gei <= lastCommittedGEI` (or that it was swept) and immediately
//     close its speculative NOMT state without deadlock.
func (se *SpeculativeExecutor) CleanGEI(gei uint64) {
	se.activeSessions.Range(func(key, value interface{}) bool {
		k := key.(uint64)
		if k <= gei {
			if res, ok := value.(*SpeculativeResult); ok && res != nil {
				res.AbortClonedState()
				authCh := res.AuthResponseChannel()
				if authCh != nil {
					stateRoot := getStateRootForBlock(se.bp, res.BlockNum)
					var resp *pb.ExecuteBlockResponse
					if stateRoot != nil {
						resp = &pb.ExecuteBlockResponse{
							Success:      true,
							ActualGei:    res.GEI,
							BlockNumber:  res.BlockNum,
							GeisConsumed: 0,
							StateRoot:    stateRoot,
						}
					} else {
						resp = &pb.ExecuteBlockResponse{
							Success:      false,
							Error:        fmt.Sprintf("stateRoot not found for block #%d on CleanGEI", res.BlockNum),
							ActualGei:    res.GEI,
							BlockNumber:  res.BlockNum,
							GeisConsumed: 0,
						}
					}
					select {
					case authCh <- resp:
						logger.Info("✅ [CleanGEI] Sent rescue response for GEI=%d (success=%v)", res.GEI, resp.Success)
					default:
						// Buffered(1) channel already has a response, or nobody's
						// listening anymore — safe to drop, matches ffi_bridge.go's
						// own "response after timeout is silently absorbed" contract.
					}
				}
			}
			se.activeSessions.Delete(k)
			if inf, ok := se.inFlight.Load(k); ok {
				if infSession, ok := inf.(*inFlightSession); ok && infSession != nil {
					infSession.mu.Lock()
					if infSession.cancel != nil {
						infSession.cancel()
					}
					infSession.mu.Unlock()
				}
			}
			// inFlight[k] is deleted here rather than by the execution goroutine
			// itself: this is the single authoritative point where the committer
			// (or a P2P-sync fast-forward) has confirmed GEI=k no longer needs
			// local dedup, independent of the async GEI-persistence pipeline's
			// lag. See the comment in ExecuteSpeculative's dispatch goroutine.
			se.inFlight.Delete(k)
		}
		return true
	})
}

// GetSpeculativeResult returns speculative result by GEI if available.
// Returns false for a session that has only registered its dispatch-time
// placeholder (IsFinished=false) — the committer must wait for the real
// result rather than committing an empty/incomplete one.
func (se *SpeculativeExecutor) GetSpeculativeResult(gei uint64) (*SpeculativeResult, bool) {
	val, ok := se.activeSessions.Load(gei)
	if !ok {
		return nil, false
	}
	res := val.(*SpeculativeResult)
	if !res.IsFinished {
		return nil, false
	}
	return res, true
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

	// Everything below nextExpectedGEI is already committed: let speculative executions waiting on it proceed.
	bp.speculativeExecutor.MarkCommitted(nextExpectedGEI - 1)

	epochFileLogger, _ := loggerfile.NewFileLogger("runSocketExecutor_committer.log")

	for range bp.speculativeExecutor.ResultChan() {
		// Vòng lặp kiểm tra và xử lý tuần tự các GEI liên tục đã hoàn thành
		for {
			// Fast-forward nextExpectedGEI if BlockSyncer has advanced the DB
			lastGEI := storage.GetLastGlobalExecIndex()
			if lastGEI >= nextExpectedGEI {
				logger.Warn("⚠️ [COMMITTER] Fast-forwarding nextExpectedGEI from %d to %d (BlockSyncer caught up via P2P)", nextExpectedGEI, lastGEI+1)
				// Xóa tất cả các session cũ bị bỏ qua trong một lần gọi
				bp.speculativeExecutor.CleanGEI(lastGEI)
				nextExpectedGEI = lastGEI + 1
				bp.speculativeExecutor.MarkCommitted(lastGEI)
			}

			specRes, exists := bp.speculativeExecutor.GetSpeculativeResult(nextExpectedGEI)
			if !exists {
				break
			}

			// BATCH-DRAIN OPTIMIZATION REMOVED: Every empty commit is now sequentially processed to prevent gaps.

			err := bp.commitSpeculativeResult(specRes, epochFileLogger)
			if err != nil {
				logger.Error("❌ [COMMITTER] Failed to commit speculative result for GEI=%d: %v", nextExpectedGEI, err)
				break
			}

			// Dọn dẹp session cũ
			bp.speculativeExecutor.CleanGEI(nextExpectedGEI)
			bp.speculativeExecutor.MarkCommitted(nextExpectedGEI)
			nextExpectedGEI++
		}
	}
}

// commitSpeculativeResult commits a single speculative execution result
func (bp *BlockProcessor) commitSpeculativeResult(res *SpeculativeResult, fileLogger *loggerfile.FileLogger) (commitErr error) {
	bp.ExecutionMutex.RLock()
	defer bp.ExecutionMutex.RUnlock()

	if ffiTraceEnabled {
		logger.Warn("⏱️ [FFI-TRACE] gei=%d stage=GO_COMMIT_DEQUEUED t_ns=%d", res.GEI, time.Now().UnixNano())
	}

	// Claim ownership of speculative state upfront so CleanGEI or worker cleanup cannot race with committer
	clonedState := res.TakeClonedState()
	defer func() {
		if clonedState != nil {
			clonedState.AbortSpeculative()
			clonedState = nil
		}
	}()

	// Check if already committed by P2P Sync
	lastGEI := storage.GetLastGlobalExecIndex()
	if res.GEI <= lastGEI {
		logger.Warn("⚠️ [COMMITTER] Committer received GEI=%d (block #%d) but block is already committed to DB (lastGEI=%d). Discarding obsolete speculative state immediately.",
			res.GEI, res.BlockNum, lastGEI)
		if clonedState != nil {
			clonedState.AbortSpeculative()
			clonedState = nil
		}
		authCh := res.AuthResponseChannel()
		if authCh != nil {
			stateRoot := getStateRootForBlock(bp, res.BlockNum)
			if stateRoot == nil {
				logger.Warn("⚠️ [COMMITTER] Cannot find valid stateRoot for committed block #%d (gei=%d). Rejecting bypass to prevent Zero-Fork violation.", res.BlockNum, res.GEI)
				select {
				case authCh <- &pb.ExecuteBlockResponse{
					Success:      false,
					Error:        fmt.Sprintf("stateRoot not found for committed block #%d", res.BlockNum),
					ActualGei:    res.GEI,
					BlockNumber:  res.BlockNum,
					GeisConsumed: 0,
				}:
				default:
				}
			} else {
				select {
				case authCh <- &pb.ExecuteBlockResponse{
					Success:      true, // Block already committed in DB, safe to unblock Rust
					ActualGei:    res.GEI,
					BlockNumber:  res.BlockNum,
					GeisConsumed: 0,
					StateRoot:    stateRoot,
				}:
				default:
				}
			}
		}
		return nil
	}

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
		if ffiTraceEnabled {
			logger.Warn("⏱️ [FFI-TRACE] gei=%d stage=GO_COMMIT_DONE t_ns=%d", res.GEI, time.Now().UnixNano())
		}
		authCh := res.AuthResponseChannel()
		if authCh != nil {
			if commitErr != nil {
				select {
				case authCh <- &pb.ExecuteBlockResponse{
					Success:      false,
					Error:        commitErr.Error(),
					ActualGei:    res.GEI,
					BlockNumber:  currentBlockNumber,
					GeisConsumed: 0,
				}:
				default:
				}
			} else {
				var stateRoot []byte
				if bp.chainState != nil && bp.chainState.GetAccountStateDB() != nil {
					stateRoot = bp.chainState.GetAccountStateDB().Trie().Hash().Bytes()
				}
				select {
				case authCh <- &pb.ExecuteBlockResponse{
					Success:      true,
					ActualGei:    res.GEI,
					BlockNumber:  currentBlockNumber,
					GeisConsumed: 1,
					StateRoot:    stateRoot,
				}:
				default:
				}
			}
		}
	}()

	// 1. Kiểm tra conflict bằng cách so sánh Parent Hash
	var hasConflict bool
	if lastBlock == nil {
		hasConflict = false // Genesis
	} else if clonedState == nil && len(res.Txs) > 0 {
		logger.Warn("⚠️ [COMMITTER] Speculative ClonedState is nil for GEI=%d (aborted/closed by Sync). Re-executing sequentially.", res.GEI)
		hasConflict = true
	} else if clonedState == nil {
		hasConflict = false // Empty block, no speculative state, no conflict
	} else {
		parentHash := lastBlock.Header().Hash()
		actualParentRoot := lastBlock.Header().AccountStatesRoot()
		var specParentHash common.Hash
		var specParentRoot common.Hash
		specHeaderPtr := clonedState.GetcurrentBlockHeader()
		if specHeaderPtr != nil && *specHeaderPtr != nil {
			specParentHash = (*specHeaderPtr).Hash()
			specParentRoot = (*specHeaderPtr).AccountStatesRoot()
		}
		if specParentHash != parentHash {
			logger.Warn("🔄 [COMMITTER-CONFLICT] Conflict detected: specParentHash=%s ≠ actualParentHash=%s for GEI=%d. Re-executing sequentially.",
				specParentHash.Hex()[:16], parentHash.Hex()[:16], res.GEI)
			hasConflict = true
		}
		if specParentRoot != actualParentRoot {
			logger.Warn("🔄 [COMMITTER-CONFLICT] Root conflict detected: specParentRoot=%s ≠ actualParentRoot=%s for GEI=%d. Re-executing sequentially.",
				specParentRoot.Hex()[:16], actualParentRoot.Hex()[:16], res.GEI)
			hasConflict = true
		}
	}

	if res.ExecuteErr != nil {
		logger.Warn("⚠️ [COMMITTER-EXEC-ERR] Speculative execution had error for GEI=%d: %v. Re-executing sequentially.", res.GEI, res.ExecuteErr)
		hasConflict = true
	}

	var accumulatedResults tx_processor.ProcessResult

	// 2. Xử lý trường hợp Conflict -> Re-execute tuần tự
	if hasConflict && len(res.Txs) > 0 {
		logger.Info("🔄 [COMMITTER] Re-executing GEI=%d sequentially...", res.GEI)

		// The speculative state is discarded. Its trie may hold a NOMT session that IntermediateRoot already
		// finished but nobody will ever persist; it keeps the handle's activeCount > 0, so the re-execution's own
		// IntermediateRoot -> BeginSession would wait for it forever (observed live). Abort it, do NOT persist it.
		if clonedState != nil {
			clonedState.AbortSpeculative()
			clonedState = nil
		}

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
		groupedGroups := grouptxns.GroupTransactionsDeterministic(items, csCopy.HasCode)

		// (Wait removed)

		accumulatedResults, commitErr = tx_processor.ProcessTransactions(context.Background(), csCopy, groupedGroups, false, true, blockTimeSec, res.LeaderAddr, res.BlockNum, true)
		if commitErr != nil {
			if csCopy != nil {
				csCopy.AbortSpeculative()
			}
			commitErr = fmt.Errorf("sequential re-execution failed: %w", commitErr)
			return commitErr
		}

		// Gán database của ChainState sang csCopy database
		bp.chainState.SetAccountStateDB(csCopy.GetAccountStateDB())
		bp.chainState.SetSmartContractDB(csCopy.GetSmartContractDB())
		bp.chainState.SetStakeStateDB(csCopy.GetStakeStateDB())
	} else if len(res.Txs) > 0 && clonedState != nil {
		// Không conflict -> Sử dụng speculative results và database đã thực thi sẵn
		accumulatedResults = res.ProcessResult

		// Gán database của ChainState sang clonedState database
		bp.chainState.SetAccountStateDB(clonedState.GetAccountStateDB())
		bp.chainState.SetSmartContractDB(clonedState.GetSmartContractDB())
		bp.chainState.SetStakeStateDB(clonedState.GetStakeStateDB())
		// CRITICAL ZERO-FORK FIX: Clear clonedState reference once adopted by chainState.
		// Otherwise, when CleanGEI runs after commit, it would discard the live tries (the removed CloseSpeculative path)
		// (and scDB.Discard()) on this state, wiping the live in-memory tries from
		// bp.chainState and causing subsequent blocks to read stale storage from disk.
		clonedState = nil
	}

	// 3. Tiến hành tạo block và commit

	// Trường hợp block trống (REMOVED)
	// We no longer skip block creation for empty blocks to guarantee zero gaps and 100% no-fork.

	if len(res.Txs) == 0 {
		if clonedState != nil {
			clonedState.AbortSpeculative()
			clonedState = nil
		}
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

		emptyBlock := bp.createBlockFromResults(emptyResult, currentBlockNumber, res.Epoch, true, batchID, res.TimestampMs, res.GEI, res.CommitIndex, nil, res.LeaderAddr)
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

	var precomputedRoots *PrecomputedBlockRoots
	if res.precomputeRootsChan != nil {
		precomputedRoots = <-res.precomputeRootsChan
	}

	newBlock := bp.createBlockFromResults(accumulatedResults, currentBlockNumber, res.Epoch, true, batchID, res.TimestampMs, res.GEI, res.CommitIndex, precomputedRoots, res.LeaderAddr)
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
		if tx == nil {
			continue
		}
		// P0-9: Drop transactions whose proto fields do not match their RawEnvelope
		// before deduplication, so an invalid variant cannot occupy the hash slot.
		if len(tx.RawEnvelope()) > 0 {
			if err := transaction.ValidateEnvelopeBinding(tx); err != nil {
				fmt.Printf("❌ [PrepareTransactions] dropping tx with invalid envelope binding: hash=%s err=%v\n", tx.Hash().Hex(), err)
				continue
			}
		}
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

	fmt.Printf("✅ [PrepareTransactions] rawTxs: %d, dedupedTxs: %d\n", len(rawTxs), len(dedupedTxs))

	return dedupedTxs
}

// ParallelUnmarshalTransactions decodes transaction digests in parallel using multiple CPU workers.
func ParallelUnmarshalTransactions(txs []*pb.TransactionExe) []types.Transaction {
	if len(txs) == 0 {
		return nil
	}

	numTxs := len(txs)
	// GOMAXPROCS(0), not NumCPU(): see native_fast_path.go for why.
	numWorkers := runtime.GOMAXPROCS(0)
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
				singleTx, singleErr := transaction.UnmarshalTransaction(ms.Digest)
				if singleErr == nil {
					localTxs = append(localTxs, singleTx)
					continue
				}
				multiTxs, err := transaction.UnmarshalTransactions(ms.Digest)
				if err == nil {
					localTxs = append(localTxs, multiTxs...)
				} else {
					fmt.Printf("❌ [SPECULATIVE] Failed to unmarshal transaction digest of len %d. First error: %v, Second error: %v\n", len(ms.Digest), singleErr, err)
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

// ═══════════════════════════════════════════════════════════════════════════
// ZERO-FORK INVARIANT VERIFICATION
// ═══════════════════════════════════════════════════════════════════════════
// verifyBypassBlock ensures that when Go bypasses an authoritative execution
// (because it already has a block committed for this GEI), the local DB block
// exactly matches the quorum-verified consensus payload.
func (se *SpeculativeExecutor) verifyBypassBlock(blockNum uint64, gei uint64, epochData *pb.ExecutableBlock) {
	bc := blockchain.GetBlockChainInstance()
	if bc == nil || se.bp == nil || se.bp.chainState == nil {
		return
	}
	blockHash, ok := bc.GetBlockHashByNumber(blockNum)
	if !ok {
		return
	}
	blockDb := se.bp.chainState.GetBlockDatabase()
	if blockDb == nil {
		return
	}
	committedBlock, err := blockDb.GetBlockByHash(blockHash)
	if err != nil || committedBlock == nil {
		return
	}

	authTxs := PrepareTransactions(epochData)
	committedTxs := committedBlock.Transactions()
	if len(committedTxs) != len(authTxs) {
		logger.Error("🚨 [FORK-SAFETY] FATAL MISMATCH! Committed DB block #%d (GEI=%d) has %d txs, but authoritative consensus block requires %d txs! Force crashing to prevent silent fork!",
			blockNum, gei, len(committedTxs), len(authTxs))
		panic(fmt.Sprintf("ZERO-FORK INVARIANT VIOLATION: DB block #%d (GEI=%d) has %d txs, Consensus requires %d txs",
			blockNum, gei, len(committedTxs), len(authTxs)))
	}
	for i := range authTxs {
		if committedTxs[i] != authTxs[i].Hash() {
			logger.Error("🚨 [FORK-SAFETY] FATAL MISMATCH! Committed DB block #%d tx[%d] hash=%s, but consensus block tx[%d] hash=%s! Force crashing to prevent silent fork!",
				blockNum, i, committedTxs[i].Hex(), i, authTxs[i].Hash().Hex())
			panic(fmt.Sprintf("ZERO-FORK INVARIANT VIOLATION: DB block #%d tx[%d] hash %s != Consensus tx hash %s",
				blockNum, i, committedTxs[i].Hex(), authTxs[i].Hash().Hex()))
		}
	}
}
