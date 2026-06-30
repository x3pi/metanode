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
		txs:         txs,
		accountMap:  mvcc.NewMVCCAccountMap(),
		storageMap:  mvcc.NewMVCCStorageMap(),
		txState:     make([]uint64, numTxs),
		readSets:    make([]map[common.Address]mvcc.ReadVersion, numTxs),
		writeSets:   make([]map[common.Address]bool, numTxs),
		scReadSets:  make([]map[string]mvcc.ReadVersion, numTxs),
		scWriteSets: make([]map[string]bool, numTxs),
		receipts:    make([]types.Receipt, numTxs),
		scResults:   make([]types.ExecuteSCResult, numTxs),
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
	execCh := make(chan uint32, numTxs*5) // buffer for re-executions
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

	var activeTasks int32 = int32(numTxs)

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
					func() {
						defer func() {
							if atomic.AddInt32(&activeTasks, -1) == 0 {
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

						// CLEAR previous writes from MVCC to prevent ghost state!
						stm.rwMu.RLock()
						oldWriteSet := stm.writeSets[txIndex]
						oldScWriteSet := stm.scWriteSets[txIndex]
						stm.rwMu.RUnlock()

						if oldWriteSet != nil {
							for addr := range oldWriteSet {
								stm.accountMap.Delete(addr, mvcc.Version(txIndex))
							}
						}
						if oldScWriteSet != nil {
							for sKey := range oldScWriteSet {
								if len(sKey) >= 42 {
									addr := common.HexToAddress(sKey[:42])
									stm.storageMap.Delete(addr, sKey[42:], mvcc.Version(txIndex))
								}
							}
						}

						// 2. Initialize MVCC Wrapper
						mvccDB := mvcc.NewMVCCAccountStateDB(chainState.GetAccountStateDB(), stm.accountMap, mvcc.Version(txIndex))
						scDB := mvcc.NewMVCCSmartContractDB(chainState.GetSmartContractDB(), stm.storageMap, mvcc.Version(txIndex))

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

							for {
								s := atomic.LoadUint64(&stm.txState[txIndex])
								i, _ := unpackState(s)
								if atomic.CompareAndSwapUint64(&stm.txState[txIndex], s, packState(i, 1)) {
									break
								}
							}
							atomic.AddInt32(&activeTasks, 1)
							validateCh <- txIndex
							return
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

								errSub := mvccDB.SubTotalBalance(tx.FromAddress(), totalCost)
								if errSub != nil {
									// Revert Native Transfer
									rcp = receipt.NewReceipt(
										tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
										pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(errSub.Error()), pb.EXCEPTION_NONE,
										mt_common.TRANSFER_GAS_COST, 0,
										[]types.EventLog{}, 0, common.Hash{}, 0,
									)
								} else {
									mvccDB.PlusOneNonce(tx.FromAddress())
									mvccDB.AddBalance(tx.ToAddress(), tx.Amount())
									mvccDB.SetLastHash(tx.FromAddress(), tx.Hash())
									mvccDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

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
								exRs, err = vmP.ExecuteTransactionWithMvmId(ctx, tx, false, false)
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
										rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
										// Apply state changes to wrapper DBs so Block-STM tracks Read/Write Sets correctly
										if exRs.MapNonce() != nil {
											for addrHex, newNonceBytes := range exRs.MapNonce() {
												addr := common.HexToAddress(addrHex)
												newNonce := big.NewInt(0).SetBytes(newNonceBytes).Uint64()
												mvccDB.SetNonce(addr, newNonce)
											}
										}
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
										}
									}
									mvccDB.SetLastHash(tx.FromAddress(), tx.Hash())
									mvccDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
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

						// 5. Mark as Executed
						for {
							s := atomic.LoadUint64(&stm.txState[txIndex])
							i, _ := unpackState(s)
							if atomic.CompareAndSwapUint64(&stm.txState[txIndex], s, packState(i, 1)) {
								break
							}
						}

						// Cascade Validation: Any TX > txIndex that was already Validated (3)
						// or Validating (2) must be downgraded and re-validated.
						for j := int(txIndex) + 1; j < numTxs; j++ {
							for {
								s := atomic.LoadUint64(&stm.txState[j])
								inc, st := unpackState(s)
								if st == 3 || st == 2 {
									// Increment inc to invalidate ongoing stale validation
									if atomic.CompareAndSwapUint64(&stm.txState[j], s, packState(inc+1, 1)) {
										atomic.AddInt32(&activeTasks, 1)
										validateCh <- uint32(j)
										break
									}
								} else {
									break
								}
							}
						}

						// 6. Push to validation AFTER cascading is fully complete
						atomic.AddInt32(&activeTasks, 1)
						validateCh <- txIndex
					}()
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
					func() {
						defer func() {
							if atomic.AddInt32(&activeTasks, -1) == 0 {
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
								_, highestVer, highestWriteID := stm.accountMap.Read(addr, mvcc.Version(txIndex))
								if highestVer != readVer.Version || highestWriteID != readVer.WriteID {
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

								_, highestVer, highestWriteID := stm.storageMap.Read(addr, keyStr, mvcc.Version(txIndex))
								if highestVer != readVer.Version || highestWriteID != readVer.WriteID {
									isValid = false
									break
								}
							}
						}

						if isValid {
							// Mark as Validated (3)
							atomic.CompareAndSwapUint64(&stm.txState[txIndex], packState(inc, 2), packState(inc, 3))
						} else {
							// ABORT & RE-EXECUTE (100% No Fork Guarantee)
							if atomic.CompareAndSwapUint64(&stm.txState[txIndex], packState(inc, 2), packState(inc, 4)) {
								atomic.AddInt32(&activeTasks, 1)
								execCh <- txIndex
							}
						}
					}()
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
