package tx_processor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"runtime/debug"

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

func init() {
	// Set MaxThreads to 100,000 to prevent CGO OS Thread exhaustion when Block-STM suspends txs
	debug.SetMaxThreads(100000)
}

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
	readSets [][]mvcc.AccountReadRecord
	// writeSets tracks which accounts a transaction modified
	writeSets [][]common.Address

	// Smart Contract Tracking
	scReadSets  [][]mvcc.SCReadRecord
	scWriteSets [][]string
	
	// ESTIMATE Tracking
	estimatedAccounts [][]common.Address
	estimatedStorage  [][]string

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
		readSets:         make([][]mvcc.AccountReadRecord, numTxs),
		writeSets:        make([][]common.Address, numTxs),
		scReadSets:       make([][]mvcc.SCReadRecord, numTxs),
		scWriteSets:      make([][]string, numTxs),
		estimatedAccounts: make([][]common.Address, numTxs),
		estimatedStorage:  make([][]string, numTxs),
		receipts:         make([]types.Receipt, numTxs),
		scResults:        make([]types.ExecuteSCResult, numTxs),
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

	var execCount, valCount, abortCount uint32
	var wg sync.WaitGroup
	execIn := make(chan uint32, numTxs*5) // buffer for re-executions
	execOut := make(chan uint32, numTxs*5)
	validateIn := make(chan uint32, numTxs*5)
	validateOut := make(chan uint32, numTxs*5)
	doneCh := make(chan struct{})

	// mvmIdMap stores the mvmId generated for each tx hash
	mvmIdMap := make(map[common.Hash]common.Address)
	var mapMu sync.Mutex

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	// Start unbounded queues
	go runUnboundedQueue(workerCtx, execIn, execOut)
	go runUnboundedQueue(workerCtx, validateIn, validateOut)

	// Inject all initial tasks
	for i := 0; i < numTxs; i++ {
		execIn <- uint32(i)
	}

	var activeTasks int32 = int32(numTxs)

	// Start Execution Dispatcher (Spawns a goroutine per execution to prevent deadlock when suspending)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-workerCtx.Done():
				return
			case txIndex := <-execOut:
				wg.Add(1)
				go func(txIndex uint32) {
					defer wg.Done()
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
						eCount := atomic.AddUint32(&execCount, 1)
						if eCount%10000 == 0 {
							logger.Warn("⚠️ [BLOCK-STM-DEBUG] Đã thực thi (Execution) %d lần, Aborts: %d", eCount, atomic.LoadUint32(&abortCount))
						}
						state := atomic.LoadUint64(&stm.txState[txIndex])
						inc, _ := unpackState(state)
						atomic.StoreUint64(&stm.txState[txIndex], packState(inc+1, 0))

						// CLEAR previous writes from MVCC to prevent ghost state!
						oldWriteSet := stm.writeSets[txIndex]
						oldScWriteSet := stm.scWriteSets[txIndex]

						if oldWriteSet != nil {
							for _, addr := range oldWriteSet {
								stm.accountMap.Delete(addr, mvcc.Version(txIndex))
							}
						}
						if oldScWriteSet != nil {
							for _, sKey := range oldScWriteSet {
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
						
						// Allow ACCOUNT_SETTING_ADDRESS_SELECT to bypass nil account check for new registrations (e.g. tps_blast)
						if fromAccount == nil && tx.GetNonce() == 0 && tx.ToAddress() == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
							fromAccount = mt_state.NewAccountState(tx.FromAddress())
						}

						if err != nil || fromAccount == nil || fromAccount.Nonce() != tx.GetNonce() {
							stm.readSets[txIndex] = mvccDB.ReadSet
							stm.writeSets[txIndex] = mvccDB.WriteSet
							// Clear results on failure
							stm.receipts[txIndex] = nil
							stm.scResults[txIndex] = nil

							for {
								s := atomic.LoadUint64(&stm.txState[txIndex])
								i, _ := unpackState(s)
								if atomic.CompareAndSwapUint64(&stm.txState[txIndex], s, packState(i, 1)) {
									break
								}
							}
							atomic.AddInt32(&activeTasks, 1)
							validateIn <- txIndex
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
											// Commit the mutated account into MVCC wrapper
											stm.accountMap.Write(fromAddr, mvcc.Version(txIndex), fromAccount)

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
										// Commit the mutated account into MVCC wrapper
										stm.accountMap.Write(fromAddr, mvcc.Version(txIndex), fromAccount)
										
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
								mvm.ClearXapianTxBuffer(tx.Hash().Bytes())
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

						// 3.5 Clear any Estimates left by previous aborts of THIS transaction
						if len(stm.estimatedAccounts[txIndex]) > 0 {
							for _, addr := range stm.estimatedAccounts[txIndex] {
								stm.accountMap.RemoveEstimate(addr, mvcc.Version(txIndex))
							}
							stm.estimatedAccounts[txIndex] = nil
						}
						if len(stm.estimatedStorage[txIndex]) > 0 {
							for _, fullKey := range stm.estimatedStorage[txIndex] {
								if len(fullKey) >= 42 {
									addr := common.HexToAddress(fullKey[:42])
									keyStr := fullKey[42:]
									stm.storageMap.RemoveEstimate(addr, keyStr, mvcc.Version(txIndex))
								}
							}
							stm.estimatedStorage[txIndex] = nil
						}

						// 4. Save Read/Write sets and Results
						stm.receipts[txIndex] = rcp
						stm.scResults[txIndex] = exRs
						stm.readSets[txIndex] = mvccDB.ReadSet
						stm.writeSets[txIndex] = mvccDB.WriteSet
						stm.scReadSets[txIndex] = scDB.ReadSet
						stm.scWriteSets[txIndex] = scDB.WriteSet

						// 5. Mark as Executed
						for {
							s := atomic.LoadUint64(&stm.txState[txIndex])
							i, _ := unpackState(s)
							if atomic.CompareAndSwapUint64(&stm.txState[txIndex], s, packState(i, 1)) {
								break
							}
						}

						// Cascade Validation: Only for transactions that actually read what we just wrote!
						newWriteSet := stm.writeSets[txIndex]
						newScWriteSet := stm.scWriteSets[txIndex]

						// 1. Invalidate Account Readers
						if len(newWriteSet) > 0 {
							for _, addr := range newWriteSet {
								readers := stm.accountMap.GetReaders(addr)
								for _, r := range readers {
									j := uint32(r)
									if j > txIndex {
										for {
											s := atomic.LoadUint64(&stm.txState[j])
											inc, st := unpackState(s)
											if st == 3 || st == 2 || st == 1 {
												// Increment inc to invalidate ongoing stale validation
												if atomic.CompareAndSwapUint64(&stm.txState[j], s, packState(inc+1, 1)) {
													atomic.AddInt32(&activeTasks, 1)
													validateIn <- uint32(j)
													break
												}
											} else {
												break
											}
										}
									}
								}
							}
						}
						
						// 2. Invalidate SC Readers
						if len(newScWriteSet) > 0 {
							for _, fullKey := range newScWriteSet {
								if len(fullKey) >= 42 {
									addr := common.HexToAddress(fullKey[:42])
									keyStr := fullKey[42:]
									readers := stm.storageMap.GetReaders(addr, keyStr)
									for _, r := range readers {
										j := uint32(r)
										if j > txIndex {
											for {
												s := atomic.LoadUint64(&stm.txState[j])
												inc, st := unpackState(s)
												if st == 3 || st == 2 || st == 1 {
													// Increment inc to invalidate ongoing stale validation
													if atomic.CompareAndSwapUint64(&stm.txState[j], s, packState(inc+1, 1)) {
														atomic.AddInt32(&activeTasks, 1)
														validateIn <- uint32(j)
														break
													}
												} else {
													break
												}
											}
										}
									}
								}
							}
						}

						// 6. Push to validation AFTER cascading is fully complete
						atomic.AddInt32(&activeTasks, 1)
						validateIn <- txIndex
					}()
				}(txIndex)
			}
		}
	}()

	// Start Validation Workers (High priority, fast execution)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case txIndex := <-validateOut:
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

						atomic.AddUint32(&valCount, 1)

						rSet := stm.readSets[txIndex]
						scRSet := stm.scReadSets[txIndex]

						isValid := true
						if rSet != nil {
							for _, readRec := range rSet {
								_, highestVer, highestWriteID, wakeup := stm.accountMap.Read(readRec.Address, mvcc.Version(txIndex))
								if wakeup != nil || highestVer != readRec.Version || highestWriteID != readRec.WriteID {
									isValid = false
									break
								}
							}
						}

						if isValid {
							for _, readRec := range scRSet {
								_, highestVer, highestWriteID, wakeup := stm.storageMap.Read(readRec.Address, readRec.Key, mvcc.Version(txIndex))
								if wakeup != nil || highestVer != readRec.Version || highestWriteID != readRec.WriteID {
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
							aCount := atomic.AddUint32(&abortCount, 1)
							if aCount%10000 == 0 {
								logger.Warn("⚠️ [BLOCK-STM-DEBUG] Đang bị Abort liên tục. Tổng aborts: %d, txIndex đang xét: %d", aCount, txIndex)
							}
							
							// ESTIMATE LOGIC: Add estimates for next execution
							stm.estimatedAccounts[txIndex] = nil
							stm.estimatedStorage[txIndex] = nil
							
							if len(stm.writeSets[txIndex]) > 0 {
								for _, addr := range stm.writeSets[txIndex] {
									stm.accountMap.AddEstimate(addr, mvcc.Version(txIndex))
									stm.estimatedAccounts[txIndex] = append(stm.estimatedAccounts[txIndex], addr)
								}
							}
							if len(stm.scWriteSets[txIndex]) > 0 {
								for _, fullKey := range stm.scWriteSets[txIndex] {
									if len(fullKey) >= 42 {
										addr := common.HexToAddress(fullKey[:42])
										keyStr := fullKey[42:]
										stm.storageMap.AddEstimate(addr, keyStr, mvcc.Version(txIndex))
										stm.estimatedStorage[txIndex] = append(stm.estimatedStorage[txIndex], fullKey)
									}
								}
							}
							
							if atomic.CompareAndSwapUint64(&stm.txState[txIndex], packState(inc, 2), packState(inc, 4)) {
								atomic.AddInt32(&activeTasks, 1)
								execIn <- txIndex
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

	logger.Info("✅ [BLOCK-STM] Hoàn tất %d TXs, tiến hành Commit State DB. Stats: Execs=%d, Vals=%d, Aborts=%d", numTxs, atomic.LoadUint32(&execCount), atomic.LoadUint32(&valCount), atomic.LoadUint32(&abortCount))
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
