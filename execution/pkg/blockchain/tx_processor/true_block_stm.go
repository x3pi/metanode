package tx_processor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/mvcc"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// TrueBlockSTM implements the deterministic parallel MVCC Block-STM.
type TrueBlockSTM struct {
	txs        []types.Transaction
	accountMap *mvcc.MVCCAccountMap
	storageMap *mvcc.MVCCStorageMap

	// txState tracks incarnation and status.
	// top 32 bits: incarnation, bottom 32 bits: status (0=Pending, 1=Executed, 2=Validating, 3=Validated, 4=Aborted)
	txState []uint64

	// State Access Tracking
	// readSets tracks which versions/writeIDs of accounts a transaction read
	readSets []map[common.Address]mvcc.ReadVersion
	// writeSets tracks which accounts a transaction modified
	writeSets []map[common.Address]bool

	// Smart Contract Tracking
	scReadSets  []map[string]mvcc.ReadVersion
	scWriteSets []map[string]bool

	// Mutex to protect readSets/writeSets slices (the maps themselves belong to the TxIndex)
	rwMu sync.RWMutex

	receipts  []types.Receipt
	scResults []types.ExecuteSCResult

	// Reverse indices: address/storage-key -> set of TX indices that have read it.
	// Used by cascadeInvalidate to re-validate only TXs that actually depend on
	// a given write, instead of blindly re-checking every later TX in the block.
	addrReadersMu sync.Mutex
	addrReaders   map[common.Address]map[int]struct{}
	scReadersMu   sync.Mutex
	scReaders     map[string]map[int]struct{}

	// Statistics
	abortCount int32

	// mvmId generated per-TX, shared across the whole block.
	mvmIdMapMu sync.Mutex
	mvmIdMap   map[common.Hash]common.Address

	// Suspend/Wakeup state
	estimatedAccounts []map[common.Address]bool
	estimatedStorage  []map[string]bool
	waiters           [][]uint32
	waitersMu         []sync.Mutex
}

func packState(incarnation uint32, status int32) uint64 {
	return (uint64(incarnation) << 32) | uint64(uint32(status))
}

func unpackState(state uint64) (incarnation uint32, status int32) {
	return uint32(state >> 32), int32(state)
}

func NewTrueBlockSTM(txs []types.Transaction) *TrueBlockSTM {
	numTxs := len(txs)
	return &TrueBlockSTM{
		txs:               txs,
		accountMap:        mvcc.NewMVCCAccountMap(),
		storageMap:        mvcc.NewMVCCStorageMap(),
		txState:           make([]uint64, numTxs),
		readSets:          make([]map[common.Address]mvcc.ReadVersion, numTxs),
		writeSets:         make([]map[common.Address]bool, numTxs),
		scReadSets:        make([]map[string]mvcc.ReadVersion, numTxs),
		scWriteSets:       make([]map[string]bool, numTxs),
		receipts:          make([]types.Receipt, numTxs),
		scResults:         make([]types.ExecuteSCResult, numTxs),
		addrReaders:       make(map[common.Address]map[int]struct{}),
		scReaders:         make(map[string]map[int]struct{}),
		mvmIdMap:          make(map[common.Hash]common.Address),
		estimatedAccounts: make([]map[common.Address]bool, numTxs),
		estimatedStorage:  make([]map[string]bool, numTxs),
		waiters:           make([][]uint32, numTxs),
		waitersMu:         make([]sync.Mutex, numTxs),
	}
}

// Process runs the block's TXs through the MVCC engine.
//
// Validator-contract and cross-chain-contract TXs are executed as sequential
// "barrier" TXs (see runBarrierTx) rather than inside the parallel MVCC
// workers: they mutate StakeStateDB and trigger off-chain bridge calls
// directly against chainState with no read/write-set tracking, so Block-STM's
// conflict detector cannot protect them, and re-executing them on abort would
// double-apply side effects. blockTime is threaded through to these handlers
// for deterministic timestamp-dependent logic (e.g. reward accrual).
func (stm *TrueBlockSTM) Process(
	ctx context.Context,
	chainState *blockchain.ChainState,
	leaderAddr common.Address,
	lastBlockHeader types.BlockHeader,
	blockTime uint64,
) ([]types.Transaction, []types.Receipt, []types.ExecuteSCResult, map[common.Hash]common.Address) {
	numTxs := len(stm.txs)
	if numTxs == 0 {
		return stm.txs, nil, nil, nil
	}

	logger.Info("🚀 [BLOCK-STM] Khởi chạy %d TXs trên True MVCC Engine", numTxs)

	isBarrierTx := make([]bool, numTxs)
	for i, tx := range stm.txs {
		to := tx.ToAddress()
		if to == mt_common.VALIDATOR_CONTRACT_ADDRESS || to == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
			isBarrierTx[i] = true
		}
	}

	segStart := 0
	for i := 0; i <= numTxs; i++ {
		if i < numTxs && !isBarrierTx[i] {
			continue
		}

		if i > segStart {
			if !stm.runParallelSegment(ctx, chainState, leaderAddr, lastBlockHeader, blockTime, segStart, i) {
				return stm.txs, nil, nil, stm.mvmIdMap
			}
			stm.flushEventLogs(chainState, segStart, i)
		}

		if i < numTxs {
			// Commit everything settled so far so the barrier TX observes fresh
			// state, then run it alone before resuming parallel execution.
			stm.commitToBase(chainState)
			stm.runBarrierTx(ctx, chainState, blockTime, i)
		}

		segStart = i + 1
	}

	logger.Info("✅ [BLOCK-STM] Hoàn tất %d TXs, tiến hành Commit State DB | 📊 Đụng độ (Abort/Retry): %d lần", numTxs, atomic.LoadInt32(&stm.abortCount))
	stm.commitToBase(chainState)

	// Collect receipts and results, calculate Total Gas Fee
	totalGasFee := big.NewInt(0)
	for i := 0; i < numTxs; i++ {
		if stm.receipts[i] != nil {
			gasUsed := stm.receipts[i].GasUsed()
			if gasUsed > 0 {
				gasFee := new(big.Int).SetUint64(gasUsed * stm.txs[i].MaxGasPrice())
				totalGasFee.Add(totalGasFee, gasFee)
			}
		}
	}

	// Reward Leader
	if totalGasFee.Cmp(big.NewInt(0)) > 0 {
		chainState.GetAccountStateDB().AddBalance(leaderAddr, totalGasFee)
	}

	logger.Info("✅ [BLOCK-STM] Hoàn tất Commit 100%% Deterministic, TotalGasFee: %v, Leader: %v", totalGasFee, leaderAddr.Hex())
	return stm.txs, stm.receipts, stm.scResults, stm.mvmIdMap
}

// runParallelSegment runs the standard Block-STM execute/validate worker pool
// scoped to TX indices [lo, hi). Returns false if ctx was cancelled before the
// segment finished.
func (stm *TrueBlockSTM) runParallelSegment(
	ctx context.Context,
	chainState *blockchain.ChainState,
	leaderAddr common.Address,
	lastBlockHeader types.BlockHeader,
	blockTime uint64,
	lo, hi int,
) bool {
	segSize := hi - lo

	execIn := make(chan uint32, segSize)
	execOut := make(chan uint32, segSize)
	validateIn := make(chan uint32, segSize)
	validateOut := make(chan uint32, segSize)
	doneCh := make(chan struct{})

	for i := lo; i < hi; i++ {
		execIn <- uint32(i)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	go runUnboundedQueue(workerCtx, execIn, execOut)
	go runUnboundedQueue(workerCtx, validateIn, validateOut)

	var activeTasks int32 = int32(segSize)
	var wg sync.WaitGroup

	// Scale worker counts to the machine instead of hardcoding 16/8: that
	// under-uses large machines and oversubscribes small ones (e.g. CI
	// runners), and each goroutine here is only useful up to available cores.
	// GOMAXPROCS(0), not NumCPU(): see native_fast_path.go for why.
	numCPU := runtime.GOMAXPROCS(0)
	execWorkers := numCPU
	if execWorkers < 1 {
		execWorkers = 1
	}
	if segSize < execWorkers {
		execWorkers = segSize
	}
	validateWorkers := numCPU / 2
	if validateWorkers < 1 {
		validateWorkers = 1
	}
	if segSize < validateWorkers {
		validateWorkers = segSize
	}

	// Start Execution Workers
	for w := 0; w < execWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case txIndex := <-execOut:
					stm.execOne(workerCtx, chainState, leaderAddr, lastBlockHeader, blockTime, txIndex, execIn, validateIn, &activeTasks, doneCh)
				}
			}
		}()
	}

	// Start Validation Workers
	for w := 0; w < validateWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case txIndex := <-validateOut:
					stm.validateOne(workerCtx, txIndex, execIn, validateIn, &activeTasks, doneCh)
				}
			}
		}()
	}

	logger.Info("⏳ [BLOCK-STM DEBUG] Start Segment [%d-%d] (size=%d, execWorkers=%d, validateWorkers=%d)", lo, hi, segSize, execWorkers, validateWorkers)

	var completed bool
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-doneCh:
			completed = true
		case <-ctx.Done():
			completed = false
		case <-ticker.C:
			currentActive := atomic.LoadInt32(&activeTasks)
			validatedCount := 0
			for i := lo; i < hi; i++ {
				s := atomic.LoadUint64(&stm.txState[i])
				_, st := unpackState(s)
				if st == 3 {
					validatedCount++
				}
			}
			logger.Info("⏳ [BLOCK-STM DEBUG] Segment [%d-%d]: Đang có %d active tasks, %d/%d TXs đã validated", lo, hi, currentActive, validatedCount, segSize)
			continue // Wait for doneCh or ctx.Done()
		}
		break
	}

	workerCancel()
	wg.Wait()
	return completed
}

// pushTask enqueues v onto ch, giving up when ctx is cancelled. Sends into the
// unbounded-queue inlets can only block once the queue drainer goroutine has
// exited (i.e. the context was cancelled while the inlet buffer was full);
// without the ctx branch a worker stuck on such a send would never return and
// runParallelSegment's wg.Wait() would hang forever. Skipping the send on
// cancel is safe: the segment is being torn down and its result is discarded.
func pushTask(ctx context.Context, ch chan<- uint32, v uint32) {
	select {
	case ch <- v:
	case <-ctx.Done():
	}
}

// execOne executes a single TX incarnation (native transfer, EVM call/deploy,
// or account-setting) against a fresh MVCC wrapper and schedules it for
// validation. Validator/cross-chain TXs never reach here — Process() routes
// them to runBarrierTx instead.
func (stm *TrueBlockSTM) execOne(
	ctx context.Context,
	chainState *blockchain.ChainState,
	leaderAddr common.Address,
	lastBlockHeader types.BlockHeader,
	blockTime uint64,
	txIndex uint32,
	execCh chan<- uint32,
	validateCh chan<- uint32,
	activeTasks *int32,
	doneCh chan struct{},
) {
	var suspended bool

	// Take ownership of old estimates from a previous abort cycle.
	// Use per-tx waitersMu instead of global rwMu to avoid deadlock.
	stm.waitersMu[txIndex].Lock()
	oldAccEstimates := stm.estimatedAccounts[txIndex]
	oldStoreEstimates := stm.estimatedStorage[txIndex]
	stm.estimatedAccounts[txIndex] = nil
	stm.estimatedStorage[txIndex] = nil
	stm.waitersMu[txIndex].Unlock()

	defer func() {
		if !suspended {
			// Remove estimates from previous abort
			if oldAccEstimates != nil {
				for addr := range oldAccEstimates {
					stm.accountMap.RemoveEstimate(addr, mvcc.Version(txIndex))
				}
			}
			if oldStoreEstimates != nil {
				for fullKey := range oldStoreEstimates {
					if len(fullKey) >= 42 {
						addr := common.HexToAddress(fullKey[:42])
						keyStr := fullKey[42:]
						stm.storageMap.RemoveEstimate(addr, keyStr, mvcc.Version(txIndex))
					}
				}
			}

			// Wake up waiters
			stm.waitersMu[txIndex].Lock()
			toWakeup := stm.waiters[txIndex]
			stm.waiters[txIndex] = nil
			stm.waitersMu[txIndex].Unlock()

			// Micro-optimization: Sort waiters by txIndex to minimize out-of-order execution.
			// Việc sắp xếp (Sort) theo txIndex (tăng dần) đảm bảo các giao dịch đứng trước trong block
			// sẽ được ưu tiên đẩy vào execCh trước, giúp giảm thiểu rủi ro chạy sai thứ tự
			// và hạn chế tối đa các tình huống Abort/chạy lại lãng phí CPU.
			if len(toWakeup) > 1 {
				slices.Sort(toWakeup)
			}

			for _, w := range toWakeup {
				atomic.AddInt32(activeTasks, 1)
				pushTask(ctx, execCh, w)
			}
		} else {
			// If suspended, we didn't finish execution, so we MUST put the estimates
			// back into the arrays. Otherwise they are lost and the next re-execution
			// won't remove them, causing a livelock for anyone waiting on them!
			stm.waitersMu[txIndex].Lock()
			if stm.estimatedAccounts[txIndex] == nil {
				stm.estimatedAccounts[txIndex] = oldAccEstimates
			}
			if stm.estimatedStorage[txIndex] == nil {
				stm.estimatedStorage[txIndex] = oldStoreEstimates
			}
			stm.waitersMu[txIndex].Unlock()
		}

		if atomic.AddInt32(activeTasks, -1) == 0 {
			select {
			case <-doneCh:
			default:
				close(doneCh)
			}
		}
	}()

	// 1. Mark as Executing (Pending)
	state := atomic.LoadUint64(&stm.txState[txIndex])
	inc, _ := unpackState(state)
	atomic.StoreUint64(&stm.txState[txIndex], packState(inc+1, 0))

	// Remember what the previous incarnation wrote. We do NOT blanket-clear it
	// here: this incarnation's mvccDB/scDB start fresh regardless (their
	// localState/ReadSet/WriteSet are always empty maps), and AccountState()
	// reads versions strictly less than txIndex — so it can never observe its
	// own previous write even if left in place. Deferring cleanup to a
	// set-difference after execution (cleanupStaleWrites, below) means a key
	// written identically by both incarnations is simply overwritten in place
	// by this run's Write() call instead of being deleted-then-rewritten,
	// which avoids an unnecessary WriteID bump — and the spurious cascade
	// invalidation of any reader that already validated against that same
	// unchanged value.
	stm.rwMu.RLock()
	oldWriteSet := stm.writeSets[txIndex]
	oldScWriteSet := stm.scWriteSets[txIndex]
	stm.rwMu.RUnlock()

	// 2. Initialize MVCC Wrapper
	mvccDB := mvcc.NewMVCCAccountStateDB(chainState.GetAccountStateDB(), stm.accountMap, mvcc.Version(txIndex))
	scDB := mvcc.NewMVCCSmartContractDB(chainState.GetSmartContractDB(), stm.storageMap, mvcc.Version(txIndex))

	// 3. Thực thi Transaction theo từng loại (native, account-setting, hoặc EVM)
	tx := stm.txs[txIndex]
	toAddress := tx.ToAddress()
	if tx.IsDeployContract() {
		toAddress = common.Address{}
	}

	// Nonce Check (adds to ReadSet)
	fromAccount, err := mvccDB.AccountState(tx.FromAddress())

	// Allow ACCOUNT_SETTING_ADDRESS_SELECT to bypass nil account check for new registrations (e.g. tps_blast)
	if fromAccount == nil && tx.GetNonce() == 0 && tx.ToAddress() == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		fromAccount = mt_state.NewAccountState(tx.FromAddress())
	}

	if err != nil || fromAccount == nil || fromAccount.Nonce() != tx.GetNonce() {
		if errors.Is(err, mvcc.ErrEstimateHit) {
			atomic.AddInt32(&stm.abortCount, 1)
			suspended = true
			blockingVer := mvccDB.BlockingVersion
			if blockingVer == mvcc.BaseVersion {
				blockingVer = scDB.BlockingVersion
			}
			if blockingVer != mvcc.BaseVersion {
				stm.waitersMu[blockingVer].Lock()
				s := atomic.LoadUint64(&stm.txState[blockingVer])
				_, st := unpackState(s)
				// if st == 1 /*TX_STATUS_EXECUTED*/ || st == 2 /*TX_STATUS_VALIDATING*/ || st == 3 /*TX_STATUS_VALIDATED*/ {
				if st == 1 || st == 2 || st == 3 {
					stm.waitersMu[blockingVer].Unlock()
					atomic.AddInt32(activeTasks, 1)
					pushTask(ctx, execCh, txIndex)
					return
				}
				stm.waiters[blockingVer] = append(stm.waiters[blockingVer], uint32(txIndex))
				stm.waitersMu[blockingVer].Unlock()
			}
			return
		}

		stm.rwMu.Lock()
		stm.readSets[txIndex] = mvccDB.ReadSet
		stm.writeSets[txIndex] = mvccDB.WriteSet
		// scDB was never touched on this path, but must still be recorded so a
		// later re-execution (or validation) doesn't validate against a stale
		// scReadSets/scWriteSets entry left over from a prior incarnation.
		stm.scReadSets[txIndex] = scDB.ReadSet
		stm.scWriteSets[txIndex] = scDB.WriteSet
		// Clear results on failure
		stm.receipts[txIndex] = nil
		stm.scResults[txIndex] = nil
		stm.rwMu.Unlock()

		// This incarnation wrote nothing (nonce check failed before any write),
		// so every address/slot the previous incarnation wrote is now orphaned.
		stm.cleanupStaleWrites(txIndex, oldWriteSet, mvccDB.WriteSet, oldScWriteSet, scDB.WriteSet)
		stm.registerReaders(int(txIndex), mvccDB.ReadSet, scDB.ReadSet)

		for {
			s := atomic.LoadUint64(&stm.txState[txIndex])
			i, _ := unpackState(s)
			if atomic.CompareAndSwapUint64(&stm.txState[txIndex], s, packState(i, 1)) {
				break
			}
		}
		atomic.AddInt32(activeTasks, 1)
		pushTask(ctx, validateCh, txIndex)
		return
	}

	var rcp types.Receipt
	var exRs types.ExecuteSCResult

	if toAddress == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		dataInput := tx.CallData().Input()
		if len(dataInput) < 4 {
			logger.Error("Invalid calldata: less than 4 bytes for TX %s", tx.Hash().Hex())
			err := fmt.Errorf("invalid calldata")
			rcp = receipt.NewReceipt(
				tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
				pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
				mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
			)
		} else {
			selector := dataInput[:4]
			fromAddr := tx.FromAddress()

			if tx.GetNonce() == 0 && bytes.Equal(selector, utils.GetFunctionSelector("setBlsPublicKey(bytes)")) {
				plk, err := UnpackSetBlsPublicKeyInput(dataInput)
				if err != nil {
					logger.Error("UnpackSetBlsPublicKeyInput failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp = receipt.NewReceipt(
						tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
						pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
						mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
					)
				} else if fromAccount != nil && len(fromAccount.PublicKeyBls()) != 0 {
					logger.Warn("PublicKeyBls already exists for %s, skipping tx %s", fromAddr.Hex(), tx.Hash().Hex())
					rcp = receipt.NewReceipt(
						tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
						pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte("PublicKeyBls already exists"), pb.EXCEPTION_NONE,
						mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
					)
				} else {
					if setErr := fromAccount.SetPublicKeyBls(plk); setErr != nil {
						logger.Error("SetPublicKeyBls failed for tx %s: %v", tx.Hash().Hex(), setErr)
						rcp = receipt.NewReceipt(
							tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
							pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(setErr.Error()), pb.EXCEPTION_NONE,
							mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
						)
					} else {
						mvccDB.PlusOneNonce(fromAddr)
						mvccDB.SetLastHash(fromAddr, tx.Hash())
						// Commit the mutated account into MVCC wrapper. Write a copy —
						// fromAccount is the same object cached in mvccDB.localState;
						// writing the live pointer would let any later mutation of it
						// silently corrupt the version other TXs already read.
						stm.accountMap.Write(fromAddr, mvcc.Version(txIndex), fromAccount.Copy())
						mvccDB.WriteSet[fromAddr] = true

						rcp = receipt.NewReceipt(
							tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
							pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
							mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
							[]types.EventLog{}, 0, common.Hash{}, 0,
						)
					}
				}
			} else if tx.GetNonce() != 0 && bytes.Equal(selector, utils.GetFunctionSelector("setAccountType(uint8)")) {
				acType, err := UnpackSetAccountTypeInput(dataInput)
				if err != nil {
					logger.Error("UnpackSetAccountTypeInput failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp = receipt.NewReceipt(
						tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
						pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
						mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
					)
				} else {
					fromAccount.SetAccountType(acType)
					mvccDB.PlusOneNonce(fromAddr)
					mvccDB.SetLastHash(fromAddr, tx.Hash())
					// Commit the mutated account into MVCC wrapper (copy — see note above)
					stm.accountMap.Write(fromAddr, mvcc.Version(txIndex), fromAccount.Copy())
					mvccDB.WriteSet[fromAddr] = true

					rcp = receipt.NewReceipt(
						tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
						pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
						mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
						[]types.EventLog{}, 0, common.Hash{}, 0,
					)
				}
			} else {
				logger.Warn("Unknown Account Setting selector or invalid nonce for TX %s", tx.Hash().Hex())
				rcp = receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte("unknown account setting or invalid nonce"), pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
				)
			}
		}
	} else {
		// Generate mvmId (0xFE + blockHash[:15] + txIndex)
		var ethAddressBytes [20]byte
		ethAddressBytes[0] = 0xFE
		copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
		binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(txIndex))
		mvmId := common.Address(ethAddressBytes)

		mvm.ClearMVMApi(mvmId)

		stm.mvmIdMapMu.Lock()
		stm.mvmIdMap[tx.Hash()] = mvmId
		stm.mvmIdMapMu.Unlock()

		// Giao dịch Native hoặc Smart Contract
		vmP := vm_processor.NewVmProcessor(chainState, mvmId, false, blockTime, leaderAddr)
		vmP.SetAccountStateDB(mvccDB)
		vmP.SetSmartContractDB(scDB)

		if tx.IsRegularTransaction() {
			// Native Transfer
			gasFee := new(big.Int).Mul(new(big.Int).SetUint64(mt_common.TRANSFER_GAS_COST), new(big.Int).SetUint64(tx.MaxGasPrice()))
			totalCost := new(big.Int).Add(tx.Amount(), gasFee)

			errSub := mvccDB.SubTotalBalance(tx.FromAddress(), totalCost)
			
			// Always update nonce and state hashes even if balance deduction fails (prevents infinite replay)
			mvccDB.PlusOneNonce(tx.FromAddress())
			mvccDB.SetLastHash(tx.FromAddress(), tx.Hash())
			mvccDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
			
			if errSub != nil {
				// Revert Native Transfer
				rcp = receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
					pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(errSub.Error()), pb.EXCEPTION_NONE,
					mt_common.TRANSFER_GAS_COST, 0,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)
			} else {
				mvccDB.AddBalance(tx.ToAddress(), tx.Amount())

				// Create receipt for native transfer
				rcp = receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
					pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
					mt_common.TRANSFER_GAS_COST, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)
			}
		} else {
			// Smart Contract
			var err error
			mvm.ClearXapianTxBuffer(tx.Hash().Bytes())
			exRs, err = vmP.ExecuteTransactionWithMvmId(ctx, tx, false, false)

			blockingVer := mvccDB.BlockingVersion
			if blockingVer == mvcc.BaseVersion {
				blockingVer = scDB.BlockingVersion
			}
			if blockingVer != mvcc.BaseVersion {
				atomic.AddInt32(&stm.abortCount, 1)
				suspended = true
				stm.waitersMu[blockingVer].Lock()

				// Prevent Race Condition: Check if blockingVer has already finished executing.
				s := atomic.LoadUint64(&stm.txState[blockingVer])
				_, st := unpackState(s)
				// if st == 1 /*TX_STATUS_EXECUTED*/ || st == 2 /*TX_STATUS_VALIDATING*/ || st == 3 /*TX_STATUS_VALIDATED*/ {
				if st == 1 || st == 2 || st == 3 {
					stm.waitersMu[blockingVer].Unlock()
					atomic.AddInt32(activeTasks, 1)
					pushTask(ctx, execCh, txIndex)
					return
				}

				stm.waiters[blockingVer] = append(stm.waiters[blockingVer], uint32(txIndex))
				stm.waitersMu[blockingVer].Unlock()
				return
			}

			if err != nil {

				logger.Error("executeTransactionWithMvmId failed for tx %s: %v", tx.Hash().Hex(), err)
				rcp = receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
				)
			} else {
				rcp = receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					exRs.ReceiptStatus(), nil, exRs.Exception(),
					mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)
			}

			if err == nil {
				if exRs != nil {
					receiptStatus := exRs.ReceiptStatus()
					ret := exRs.Return()
					exception := exRs.Exception()
					gasUsed := exRs.GasUsed()
					eventLogs := exRs.EventLogs()

					// [FIX] Deduct Gas Fee from sender's balance
					gasFee := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), new(big.Int).SetUint64(tx.MaxGasPrice()))
					canPayGas := true
					if gasFee.Cmp(big.NewInt(0)) > 0 {
						errSub := mvccDB.SubTotalBalance(tx.FromAddress(), gasFee)
						if errSub != nil {
							canPayGas = false
							receiptStatus = pb.RECEIPT_STATUS_TRANSACTION_ERROR
							ret = []byte("insufficient balance for gas")
							exception = pb.EXCEPTION_NONE
							eventLogs = nil
							gasUsed = 0
						}
					}

					rcp.UpdateExecuteResult(receiptStatus, ret, exception, gasUsed, eventLogs)

					// Apply state changes to wrapper DBs so Block-STM tracks Read/Write Sets correctly
					// Always apply nonce updates even if canPayGas is false to prevent infinite replays
					if exRs.MapNonce() != nil {
						for addrHex, newNonceBytes := range exRs.MapNonce() {
							addr := common.HexToAddress(addrHex)
							newNonce := big.NewInt(0).SetBytes(newNonceBytes).Uint64()
							mvccDB.SetNonce(addr, newNonce)
						}
					}

					if canPayGas {
						if exRs.ReceiptStatus() == pb.RECEIPT_STATUS_RETURNED {
							if exRs.MapAddBalance() != nil {
							for addrHex, addAmtBytes := range exRs.MapAddBalance() {
								addr := common.HexToAddress(addrHex)
								addAmt := big.NewInt(0).SetBytes(addAmtBytes)
								mvccDB.AddBalance(addr, addAmt)
							}
						}
						if exRs.MapSubBalance() != nil {
							for addrHex, subAmtBytes := range exRs.MapSubBalance() {
								addr := common.HexToAddress(addrHex)
								subAmt := big.NewInt(0).SetBytes(subAmtBytes)
								mvccDB.SubTotalBalance(addr, subAmt)
							}
						}
						if exRs.MapStorageChange() != nil {
							for addrHex, changes := range exRs.MapStorageChange() {
								addr := common.HexToAddress(addrHex)
								var keys [][]byte
								var values [][]byte
								for keyHex, valueBytes := range changes {
									keys = append(keys, common.HexToHash(keyHex).Bytes())
									values = append(values, valueBytes)
								}
								scDB.BatchSetStorageValues(addr, keys, values)
							}
						}
						if exRs.MapCodeHash() != nil {
							mapCreator := exRs.MapCreatorPubkey()
							mapStorage := exRs.MapStorageAddress()
							for addrHex, newCodeHashBytes := range exRs.MapCodeHash() {
								addr := common.HexToAddress(addrHex)
								newCodeHash := common.BytesToHash(newCodeHashBytes)
								mvccDB.SetCodeHash(addr, newCodeHash)
								if mapCreator != nil {
									if creatorBytes, ok := mapCreator[addrHex]; ok {
										mvccDB.SetCreatorPublicKey(addr, mt_common.PubkeyFromBytes(creatorBytes))
									}
								}
								if mapStorage != nil {
									if storageAddr, ok := mapStorage[addrHex]; ok {
										mvccDB.SetStorageAddress(addr, storageAddr)
									}
								}
							}
						}
					}
				} // end of canPayGas
				}
				mvccDB.SetLastHash(tx.FromAddress(), tx.Hash())
				mvccDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

				// Check again if applying state changes hit an estimate
				blockingVer = mvccDB.BlockingVersion
				if blockingVer == mvcc.BaseVersion {
					blockingVer = scDB.BlockingVersion
				}
				if blockingVer != mvcc.BaseVersion {
					atomic.AddInt32(&stm.abortCount, 1)
					suspended = true

					// IMPORTANT: Save WriteSets so the next incarnation cleans up any partial writes
					stm.rwMu.Lock()
					stm.writeSets[txIndex] = mvccDB.WriteSet
					stm.scWriteSets[txIndex] = scDB.WriteSet
					stm.rwMu.Unlock()

					stm.waitersMu[blockingVer].Lock()
					s := atomic.LoadUint64(&stm.txState[blockingVer])
					_, st := unpackState(s)
					if st == 1 || st == 2 || st == 3 {
						stm.waitersMu[blockingVer].Unlock()
						atomic.AddInt32(activeTasks, 1)
						pushTask(ctx, execCh, txIndex)
						return
					}
					stm.waiters[blockingVer] = append(stm.waiters[blockingVer], uint32(txIndex))
					stm.waitersMu[blockingVer].Unlock()
					return
				}
			} else {
				// Nếu err != nil, giữ lại rcp lỗi đã tạo bên trên, bỏ qua cập nhật trạng thái
				if exRs != nil {
					rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
				}
			}
		}
	}

	// 4. Save Read/Write sets and Results
	stm.rwMu.Lock()
	stm.receipts[txIndex] = rcp
	stm.scResults[txIndex] = exRs
	stm.readSets[txIndex] = mvccDB.ReadSet
	stm.writeSets[txIndex] = mvccDB.WriteSet
	stm.scReadSets[txIndex] = scDB.ReadSet
	stm.scWriteSets[txIndex] = scDB.WriteSet
	stm.rwMu.Unlock()

	// Remove anything the PREVIOUS incarnation wrote that this one didn't
	// rewrite (e.g. a different branch taken this time). Keys written by both
	// incarnations were already overwritten in place during execution above.
	stm.cleanupStaleWrites(txIndex, oldWriteSet, mvccDB.WriteSet, oldScWriteSet, scDB.WriteSet)
	stm.registerReaders(int(txIndex), mvccDB.ReadSet, scDB.ReadSet)

	// 5. Mark as Executed
	for {
		s := atomic.LoadUint64(&stm.txState[txIndex])
		i, _ := unpackState(s)
		if atomic.CompareAndSwapUint64(&stm.txState[txIndex], s, packState(i, 1)) {
			break
		}
	}

	// Targeted cascade invalidation: only TXs that actually read one of the
	// addresses/slots this incarnation just wrote need to be re-validated.
	stm.cascadeInvalidate(ctx, int(txIndex), mvccDB.WriteSet, scDB.WriteSet, validateCh, activeTasks, doneCh)

	// 6. Push to validation AFTER cascading is fully complete
	atomic.AddInt32(activeTasks, 1)
	pushTask(ctx, validateCh, txIndex)
}

// validateOne checks whether txIndex's recorded read set is still consistent
// with the highest committed version of everything it read. If not, it is
// aborted and re-queued for execution.
func (stm *TrueBlockSTM) validateOne(
	ctx context.Context,
	txIndex uint32,
	execCh chan<- uint32,
	validateCh chan<- uint32,
	activeTasks *int32,
	doneCh chan struct{},
) {
	defer func() {
		if atomic.AddInt32(activeTasks, -1) == 0 {
			select {
			case <-doneCh:
			default:
				close(doneCh)
			}
		}
	}()

	state := atomic.LoadUint64(&stm.txState[txIndex])
	inc, st := unpackState(state)
	if st != 1 {
		return
	}

	if !atomic.CompareAndSwapUint64(&stm.txState[txIndex], state, packState(inc, 2)) {
		return
	}

	stm.rwMu.RLock()
	rSet := stm.readSets[txIndex]
	scRSet := stm.scReadSets[txIndex]
	stm.rwMu.RUnlock()

	isValid := true
	if rSet != nil {
		for addr, readVer := range rSet {
			_, highestVer, highestWriteID, blockingVer := stm.accountMap.Read(addr, mvcc.Version(txIndex))
			if blockingVer != mvcc.BaseVersion || highestVer != readVer.Version || highestWriteID != readVer.WriteID {
				isValid = false
				break
			}
		}
	}

	if isValid {
		for sKey, readVer := range scRSet {
			if len(sKey) < 42 {
				continue
			}
			addrHex := sKey[:42]
			keyStr := sKey[42:]
			addr := common.HexToAddress(addrHex)

			_, highestVer, highestWriteID, blockingVer := stm.storageMap.Read(addr, keyStr, mvcc.Version(txIndex))
			if blockingVer != mvcc.BaseVersion || highestVer != readVer.Version || highestWriteID != readVer.WriteID {
				isValid = false
				break
			}
		}
	}

	if isValid {
		// Mark as Validated (3)
		atomic.CompareAndSwapUint64(&stm.txState[txIndex], packState(inc, 2), packState(inc, 3))
	} else {
		atomic.AddInt32(&stm.abortCount, 1)
		// ABORT & RE-EXECUTE (100% No Fork Guarantee)
		if atomic.CompareAndSwapUint64(&stm.txState[txIndex], packState(inc, 2), packState(inc, 4)) {
			// ESTIMATE LOGIC: Add estimates for next execution
			stm.rwMu.RLock()
			writeSet := stm.writeSets[txIndex]
			scWriteSet := stm.scWriteSets[txIndex]
			stm.rwMu.RUnlock()

			// Build estimate maps FIRST (local vars), then publish under per-tx lock.
			var newAccEst map[common.Address]bool
			var newStoreEst map[string]bool
			if len(writeSet) > 0 {
				newAccEst = make(map[common.Address]bool, len(writeSet))
				for addr := range writeSet {
					stm.accountMap.AddEstimate(addr, mvcc.Version(txIndex))
					newAccEst[addr] = true
				}
			}
			if len(scWriteSet) > 0 {
				newStoreEst = make(map[string]bool, len(scWriteSet))
				for fullKey := range scWriteSet {
					if len(fullKey) >= 42 {
						addr := common.HexToAddress(fullKey[:42])
						keyStr := fullKey[42:]
						stm.storageMap.AddEstimate(addr, keyStr, mvcc.Version(txIndex))
						newStoreEst[fullKey] = true
					}
				}
			}
			// Publish under per-tx lock so execOne sees them atomically.
			stm.waitersMu[txIndex].Lock()
			stm.estimatedAccounts[txIndex] = newAccEst
			stm.estimatedStorage[txIndex] = newStoreEst
			stm.waitersMu[txIndex].Unlock()

			atomic.AddInt32(activeTasks, 1)
			pushTask(ctx, execCh, txIndex)
		}
	}
}

// cleanupStaleWrites removes MVCC entries the previous incarnation of txIndex
// wrote that the new incarnation (newWriteSet/newScWriteSet) did not rewrite.
// Keys present in both are left untouched here — they were already correctly
// overwritten in place by this incarnation's own Write() calls during
// execution, and deleting-then-rewriting them would only bump their WriteID
// for no reason, needlessly invalidating readers who already saw this exact
// value.
func (stm *TrueBlockSTM) cleanupStaleWrites(
	txIndex uint32,
	oldWriteSet map[common.Address]bool,
	newWriteSet map[common.Address]bool,
	oldScWriteSet map[string]bool,
	newScWriteSet map[string]bool,
) {
	for addr := range oldWriteSet {
		if !newWriteSet[addr] {
			stm.accountMap.Delete(addr, mvcc.Version(txIndex))
		}
	}
	for sKey := range oldScWriteSet {
		if !newScWriteSet[sKey] {
			if len(sKey) >= 42 {
				addr := common.HexToAddress(sKey[:42])
				stm.storageMap.Delete(addr, sKey[42:], mvcc.Version(txIndex))
			}
		}
	}
}

// registerReaders records that txIndex read the given addresses/storage-slots,
// so a future write to any of them can find and invalidate txIndex directly
// instead of every worker rescanning the whole remaining TX range.
func (stm *TrueBlockSTM) registerReaders(txIndex int, readSet map[common.Address]mvcc.ReadVersion, scReadSet map[string]mvcc.ReadVersion) {
	if len(readSet) > 0 {
		stm.addrReadersMu.Lock()
		for addr := range readSet {
			set, ok := stm.addrReaders[addr]
			if !ok {
				set = make(map[int]struct{})
				stm.addrReaders[addr] = set
			}
			set[txIndex] = struct{}{}
		}
		stm.addrReadersMu.Unlock()
	}
	if len(scReadSet) > 0 {
		stm.scReadersMu.Lock()
		for sKey := range scReadSet {
			set, ok := stm.scReaders[sKey]
			if !ok {
				set = make(map[int]struct{})
				stm.scReaders[sKey] = set
			}
			set[txIndex] = struct{}{}
		}
		stm.scReadersMu.Unlock()
	}
}

// cascadeInvalidate downgrades any TX known (via the reader registries) to
// have read one of writeSet/scWriteSet's keys, re-queuing it for validation.
// TXs that never read any of these keys are left untouched — unlike the
// naive approach of rechecking every TX with index > txIndex.
func (stm *TrueBlockSTM) cascadeInvalidate(
	ctx context.Context,
	txIndex int,
	writeSet map[common.Address]bool,
	scWriteSet map[string]bool,
	validateCh chan<- uint32,
	activeTasks *int32,
	doneCh chan struct{},
) {
	targets := make(map[int]struct{})

	if len(writeSet) > 0 {
		stm.addrReadersMu.Lock()
		for addr := range writeSet {
			for j := range stm.addrReaders[addr] {
				if j > txIndex {
					targets[j] = struct{}{}
				}
			}
		}
		stm.addrReadersMu.Unlock()
	}
	if len(scWriteSet) > 0 {
		stm.scReadersMu.Lock()
		for sKey := range scWriteSet {
			for j := range stm.scReaders[sKey] {
				if j > txIndex {
					targets[j] = struct{}{}
				}
			}
		}
		stm.scReadersMu.Unlock()
	}

	for j := range targets {
		for {
			s := atomic.LoadUint64(&stm.txState[j])
			inc, st := unpackState(s)
			if st == 3 || st == 2 {
				// Increment inc to invalidate ongoing stale validation
				if atomic.CompareAndSwapUint64(&stm.txState[j], s, packState(inc+1, 1)) {
					atomic.AddInt32(activeTasks, 1)
					pushTask(ctx, validateCh, uint32(j))
					break
				}
			} else {
				break
			}
		}
	}
}

// runBarrierTx executes a validator- or cross-chain-contract TX sequentially,
// directly against the global chainState (no MVCC tracking, no retry-on-abort).
// Callers must ensure no parallel segment is running concurrently and that
// commitToBase was just called, so this sees every prior TX's writes.
func (stm *TrueBlockSTM) runBarrierTx(
	ctx context.Context,
	chainState *blockchain.ChainState,
	blockTime uint64,
	txIndex int,
) {
	tx := stm.txs[txIndex]
	toAddress := tx.ToAddress()

	baseAccountDB := chainState.GetAccountStateDB()
	fromAccount, err := baseAccountDB.AccountState(tx.FromAddress())
	if err != nil || fromAccount == nil || fromAccount.Nonce() != tx.GetNonce() {
		stm.rwMu.Lock()
		stm.receipts[txIndex] = nil
		stm.scResults[txIndex] = nil
		stm.rwMu.Unlock()
		atomic.StoreUint64(&stm.txState[txIndex], packState(1, 3))
		return
	}

	// mvmId is a fixed per-contract constant here (unlike the per-TX mvmId used
	// for regular EVM calls) because barrier TXs never run concurrently with
	// each other — Process() guarantees exactly one is in flight at a time.
	// Still clear any residual C++ MVM state from a previous barrier TX at the
	// same address before reusing it.
	mvm.ClearMVMApi(toAddress)

	var rcp types.Receipt
	var exRs types.ExecuteSCResult

	if toAddress == mt_common.VALIDATOR_CONTRACT_ADDRESS {
		validatorHandler, herr := GetValidatorHandler()
		if herr != nil {
			logger.Error("Lỗi khi lấy ValidatorHandler: %v", herr)
		} else {
			rcp, exRs, _ = validatorHandler.HandleTransaction(ctx, chainState, tx, toAddress, false, blockTime)
		}
	} else if toAddress == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
		ccHandler, herr := cross_chain_handler.GetCrossChainHandler()
		if herr != nil {
			logger.Error("Lỗi khi lấy CrossChainHandler: %v", herr)
		} else {
			rcp, exRs, _ = ccHandler.HandleTransaction(ctx, chainState, tx, toAddress, false, blockTime)
		}
	}

	stm.rwMu.Lock()
	stm.receipts[txIndex] = rcp
	stm.scResults[txIndex] = exRs
	stm.rwMu.Unlock()
	atomic.StoreUint64(&stm.txState[txIndex], packState(1, 3))
}

// commitToBase flushes every MVCC-tracked write settled so far into the real
// chainState DBs. Called after every parallel segment (including right before
// each barrier TX, so it observes fresh state) and once more at the very end.
// Safe to call repeatedly: ExportLatest always reflects the highest version
// written per key, so repeated commits just re-write the same values.
func (stm *TrueBlockSTM) commitToBase(chainState *blockchain.ChainState) {
	baseAccountDB := chainState.GetAccountStateDB()
	finalAccounts := stm.accountMap.ExportLatest()
	for _, state := range finalAccounts {
		baseAccountDB.SetState(state)
	}

	baseScDB := chainState.GetSmartContractDB()
	finalStorage := stm.storageMap.ExportLatest()
	for sKey, value := range finalStorage {
		if len(sKey) >= 42 {
			addrStr := sKey[:42]
			keyStr := sKey[42:]
			baseScDB.SetStorageValue(common.HexToAddress(addrStr), []byte(keyStr), value)
		}
	}
}

// flushEventLogs pushes the buffered EVM event logs for TXs in [lo, hi) into
// the global SmartContractDB. eth_getLogs / bloom-filter queries read logs
// from there, not from receipts — but the MVCC-wrapped scDB used during
// execution only buffers AddEventLogs calls locally per TX, so without this
// they would never reach the global store. Must be called exactly once per
// index range, right after that range's segment finishes (call sites must not
// overlap), otherwise logs would be double-counted.
func (stm *TrueBlockSTM) flushEventLogs(chainState *blockchain.ChainState, lo, hi int) {
	baseScDB := chainState.GetSmartContractDB()
	for i := lo; i < hi; i++ {
		if exRs := stm.scResults[i]; exRs != nil {
			if logs := exRs.EventLogs(); len(logs) > 0 {
				baseScDB.AddEventLogs(logs)
			}
		}
	}
}
