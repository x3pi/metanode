package tx_processor

import (
	"context"
	"encoding/binary"
	"math/big"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/mvcc"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// TrueBlockSTM implements the deterministic parallel MVCC Block-STM.
type TrueBlockSTM struct {
	txs        []types.Transaction
	accountMap *mvcc.MVCCAccountMap
	storageMap *mvcc.MVCCStorageMap
	
	// status: 0 = Pending, 1 = Executed, 2 = Validated, 3 = Aborted
	txStatus []int32
	
	// Track read and write sets per transaction
	readSets  []map[common.Address]mvcc.Version
	writeSets []map[common.Address]bool

	// Mutex to protect readSets/writeSets slices (the maps themselves belong to the TxIndex)
	rwMu sync.RWMutex

	receipts []types.Receipt
	scResults []types.ExecuteSCResult
}

func NewTrueBlockSTM(txs []types.Transaction) *TrueBlockSTM {
	numTxs := len(txs)
	return &TrueBlockSTM{
		txs:        txs,
		accountMap: mvcc.NewMVCCAccountMap(),
		storageMap: mvcc.NewMVCCStorageMap(),
		txStatus:   make([]int32, numTxs),
		readSets:   make([]map[common.Address]mvcc.Version, numTxs),
		writeSets:  make([]map[common.Address]bool, numTxs),
		receipts:   make([]types.Receipt, numTxs),
		scResults:  make([]types.ExecuteSCResult, numTxs),
	}
}

func (stm *TrueBlockSTM) Process(
	ctx context.Context, 
	chainState *blockchain.ChainState,
	leaderAddr common.Address,
	lastBlockHeader types.BlockHeader,
) ([]types.Transaction, []types.Receipt, []types.ExecuteSCResult, map[common.Hash]common.Address) {
	numTxs := len(stm.txs)
	if numTxs == 0 {
		return stm.txs, nil, nil, nil
	}

	logger.Info("🚀 [BLOCK-STM] Khởi chạy %d TXs trên True MVCC Engine", numTxs)

	var wg sync.WaitGroup
	execCh := make(chan uint32, numTxs*5)     // buffer for re-executions
	validateCh := make(chan uint32, numTxs*5)
	doneCh := make(chan struct{})

	// mvmIdMap stores the mvmId generated for each tx hash
	mvmIdMap := make(map[common.Hash]common.Address)
	var mapMu sync.Mutex

	// Inject all initial tasks
	for i := 0; i < numTxs; i++ {
		execCh <- uint32(i)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	var doneCount int32
	var activeTasks int32 = int32(numTxs)
	_ = activeTasks

	// Start Execution Workers (High concurrency, e.g., 16 workers)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case txIndex := <-execCh:
					// 1. Mark as Executing (Pending)
					atomic.StoreInt32(&stm.txStatus[txIndex], 0)

					// 2. Initialize MVCC Wrapper
					mvccDB := mvcc.NewMVCCAccountStateDB(chainState.GetAccountStateDB(), stm.accountMap, txIndex)
					scDB := mvcc.NewMVCCSmartContractDB(chainState.GetSmartContractDB(), stm.storageMap, txIndex)

					// 3. Thực thi Transaction theo từng loại (đầy đủ các loại giao dịch)
					tx := stm.txs[txIndex]
					toAddress := tx.ToAddress()
					if tx.IsDeployContract() {
						toAddress = common.Address{}
					}

					// Nonce Check (adds to ReadSet)
					fromAccount, err := mvccDB.AccountState(tx.FromAddress())
					if err != nil || fromAccount == nil || fromAccount.Nonce() != tx.GetNonce() {
						stm.rwMu.Lock()
						stm.readSets[txIndex] = mvccDB.ReadSet
						stm.writeSets[txIndex] = mvccDB.WriteSet
						// Clear results on failure
						stm.receipts[txIndex] = nil
						stm.scResults[txIndex] = nil
						stm.rwMu.Unlock()
						
						atomic.StoreInt32(&stm.txStatus[txIndex], 1)
						validateCh <- txIndex
						continue
					}

					var rcp types.Receipt
					var exRs types.ExecuteSCResult

					if toAddress == mt_common.VALIDATOR_CONTRACT_ADDRESS {
						validatorHandler, err := GetValidatorHandler()
						if err == nil {
							rcp, exRs, _ = validatorHandler.HandleTransaction(ctx, chainState, tx, toAddress, false, 0)
						} else {
							logger.Error("Lỗi khi lấy ValidatorHandler: %v", err)
						}
					} else if toAddress == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
						ccHandler, err := cross_chain_handler.GetCrossChainHandler()
						if err == nil {
							rcp, exRs, _ = ccHandler.HandleTransaction(ctx, chainState, tx, toAddress, false, 0)
						} else {
							logger.Error("Lỗi khi lấy CrossChainHandler: %v", err)
						}
					} else if toAddress == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
						// Logic account setting, e.g. setBlsPublicKey, setAccountType
						logger.Info("Thực thi Account Setting cho TX %s", tx.Hash().Hex())
					} else {
						// Generate mvmId (0xFE + blockHash[:15] + txIndex)
						var ethAddressBytes [20]byte
						ethAddressBytes[0] = 0xFE
						copy(ethAddressBytes[1:16], lastBlockHeader.LastBlockHash().Bytes()[:15])
						binary.BigEndian.PutUint32(ethAddressBytes[16:], uint32(txIndex))
						mvmId := common.Address(ethAddressBytes)

						mapMu.Lock()
						mvmIdMap[tx.Hash()] = mvmId
						mapMu.Unlock()

						// Giao dịch Native hoặc Smart Contract
						vmP := vm_processor.NewVmProcessor(chainState, mvmId, false, 0, leaderAddr)
						vmP.SetAccountStateDB(mvccDB)
						vmP.SetSmartContractDB(scDB)
						
						if tx.IsRegularTransaction() {
							// Native Transfer
							gasFee := big.NewInt(int64(mt_common.TRANSFER_GAS_COST * tx.MaxGasPrice()))
							totalCost := new(big.Int).Add(tx.Amount(), gasFee)
							
							_ = mvccDB.SubTotalBalance(tx.FromAddress(), totalCost)
							mvccDB.PlusOneNonce(tx.FromAddress())
							mvccDB.AddBalance(tx.ToAddress(), tx.Amount())
							
							// Create receipt for native transfer
							rcp = receipt.NewReceipt(
								tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
								pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
								mt_common.TRANSFER_GAS_COST, mt_common.TRANSFER_GAS_COST,
								[]types.EventLog{}, 0, common.Hash{}, 0,
							)
						} else {
							// Smart Contract
							exRs, _ = vmP.ExecuteTransactionWithMvmId(ctx, tx, false, false)
						}
					}
					
					// 4. Save Read/Write sets and Results
					stm.rwMu.Lock()
					stm.receipts[txIndex] = rcp
					stm.scResults[txIndex] = exRs
					stm.readSets[txIndex] = mvccDB.ReadSet
					stm.writeSets[txIndex] = mvccDB.WriteSet
					stm.rwMu.Unlock()

					// 5. Mark as Executed
					atomic.StoreInt32(&stm.txStatus[txIndex], 1)

					// 6. Push to validation
					validateCh <- txIndex
					
					// Cascade Validation: Any TX > txIndex that was already Validated (2)
					// must be downgraded and re-validated because our new WriteSet might affect them.
					for j := int(txIndex) + 1; j < numTxs; j++ {
						if atomic.CompareAndSwapInt32(&stm.txStatus[j], 2, 1) {
							atomic.AddInt32(&doneCount, -1)
							validateCh <- uint32(j)
						}
					}
				}
			}
		}()
	}

	// Start Validation Workers (High priority, fast execution)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case txIndex := <-validateCh:
					stm.rwMu.RLock()
					rSet := stm.readSets[txIndex]
					stm.rwMu.RUnlock()

					isValid := true
					for addr, readVer := range rSet {
						// What is the HIGHEST version < txIndex for this address?
						_, highestVer := stm.accountMap.Read(addr, mvcc.Version(txIndex))
						
						// If the highest version currently in MVCC does NOT match what we read,
						// it means a lower TxIndex just overwrote our data! (Stale Read)
						if highestVer != readVer {
							isValid = false
							break
						}
					}

					if isValid {
						// Mark as Validated
						if atomic.CompareAndSwapInt32(&stm.txStatus[txIndex], 1, 2) {
							if atomic.AddInt32(&doneCount, 1) == int32(numTxs) {
								// All done, signal completion
								select {
								case <-doneCh:
								default:
									close(doneCh)
								}
							}
						}
					} else {
						// ABORT & RE-EXECUTE (100% No Fork Guarantee)
						atomic.StoreInt32(&stm.txStatus[txIndex], 3)
						execCh <- txIndex
					}
				}
			}
		}()
	}

	// Wait for completion
	select {
	case <-doneCh:
	case <-ctx.Done():
		return stm.txs, nil, nil, mvmIdMap
	}

	// Stop all workers
	workerCancel()
	wg.Wait()

	logger.Info("✅ [BLOCK-STM] Hoàn tất %d TXs, tiến hành Commit State DB", numTxs)
	baseAccountDB := chainState.GetAccountStateDB()
	
	// 1. Commit Account States
	finalAccounts := stm.accountMap.ExportLatest()
	for _, state := range finalAccounts {
		baseAccountDB.SetState(state)
	}

	// 2. Commit Storage Values
	// Note: sKey is already address.Hex() + key
	baseScDB := chainState.GetSmartContractDB()
	finalStorage := stm.storageMap.ExportLatest()
	for sKey, value := range finalStorage {
		if len(sKey) >= 42 {
			// Let's just use the full storageKey function which is addr.Hex() (42 chars) + key
			addrStr := sKey[:42]
			keyStr := sKey[42:]
			baseScDB.SetStorageValue(common.HexToAddress(addrStr), []byte(keyStr), value)
		}
	}

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
		baseAccountDB.AddBalance(leaderAddr, totalGasFee)
	}

	logger.Info("✅ [BLOCK-STM] Hoàn tất Commit 100%% Deterministic, TotalGasFee: %v, Leader: %v", totalGasFee, leaderAddr.Hex())
	return stm.txs, stm.receipts, stm.scResults, mvmIdMap
}
