package vm_processor

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// MvmResultToExecuteResult chuyển đổi kết quả từ MVM sang ExecuteSCResult.
func (vmP *VmProcessor) MvmResultToExecuteResult(
	ctx context.Context,
	transaction types.Transaction,
	mvmRs *mvm.MVMExecuteResult,
) (types.ExecuteSCResult, error) {
	var span *trace.Span = nil // Khởi tạo nil

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.MvmResultToExecuteResult", map[string]interface{}{
			"txHash":           transaction.Hash().Hex(),
			"mvmStatus":        mvmRs.Status.String(),
			"mvmException":     mvmRs.Exception.String(),
			"mvmGasUsed":       mvmRs.GasUsed,
			"mvmReturnLen":     len(mvmRs.Return),
			"mvmReturnHex":     hex.EncodeToString(mvmRs.Return),
			"numAddBalance":    len(mvmRs.MapAddBalance),
			"numSubBalance":    len(mvmRs.MapSubBalance),
			"numNonceChange":   len(mvmRs.MapNonce),
			"numCodeHash":      len(mvmRs.MapCodeHash),
			"numCodeChange":    len(mvmRs.MapCodeChange),
			"numStorageChange": len(mvmRs.MapStorageChange),
			"numEventLogs":     len(mvmRs.JEventLogs.Logs),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	transactionHash := transaction.Hash()

	// --- Revert Handling ---
	if mvmRs.Status != pb.RECEIPT_STATUS_RETURNED {
		if span != nil { // GUARD
			span.AddEvent("HandlingRevertedTransactionResult", map[string]interface{}{
				"status":    mvmRs.Status.String(),
				"exception": mvmRs.Exception.String(),
				"exmsg":     mvmRs.Exmsg,
			})
		}
		amount := transaction.Amount()
		mapAddBalance := make(map[string][]byte)
		mapSubBalance := make(map[string][]byte)
		if len(mvmRs.MapAddBalance) > 0 || len(mvmRs.MapSubBalance) > 0 {
			// Sao chép map để tránh nil pointer nếu map ban đầu là nil (dù trường hợp này ít xảy ra)
			mapAddBalance = make(map[string][]byte, len(mvmRs.MapAddBalance))
			for k, v := range mvmRs.MapAddBalance {
				mapAddBalance[k] = v
			}
			mapSubBalance = make(map[string][]byte, len(mvmRs.MapSubBalance))
			for k, v := range mvmRs.MapSubBalance {
				mapSubBalance[k] = v
			}
			if span != nil { // GUARD
				span.AddEvent("UsingMvmBalanceMapsForRevert", map[string]interface{}{
					"addCount": len(mapAddBalance),
					"subCount": len(mapSubBalance),
				})
			}
		} else if amount.Cmp(big.NewInt(0)) > 0 {
			fromAddress := transaction.FromAddress()
			toAddress := transaction.ToAddress()
			fromAddressHex := hex.EncodeToString(fromAddress.Bytes())
			toAddressHex := hex.EncodeToString(toAddress.Bytes())
			mapAddBalance[fromAddressHex] = amount.Bytes()
			mapSubBalance[toAddressHex] = amount.Bytes()
			if span != nil { // GUARD
				span.AddEvent("UsingTxAmountForRevert", map[string]interface{}{
					"from":   fromAddressHex,
					"to":     toAddressHex,
					"amount": amount.String(),
				})
			}
		}

		// ✅ Ưu tiên sử dụng exmsg khi có exception để đảm bảo exception message gốc được preserve
		returnData := prepareReturnDataWithExceptionMessage(mvmRs.Return, mvmRs.Exmsg, mvmRs.Status, mvmRs.Exception)
		if span != nil && mvmRs.Exmsg != "" && mvmRs.Status != pb.RECEIPT_STATUS_RETURNED { // GUARD
			span.AddEvent("IncludingExceptionMessageInReturn", map[string]interface{}{
				"exmsg": mvmRs.Exmsg,
			})
		}

		rs := smart_contract.NewExecuteSCResult(
			transactionHash, mvmRs.Status, mvmRs.Exception, returnData,
			mvmRs.GasUsed, common.Hash{}, mapAddBalance, mapSubBalance,
			mvmRs.MapNonce, nil, nil, nil, nil, nil, nil, nil,
		)
		if span != nil { // GUARD
			span.SetAttribute("finalResultStatus", rs.ReceiptStatus().String())
			span.SetAttribute("finalResultException", rs.Exception().String())
			span.SetAttribute("finalGasUsed", rs.GasUsed())
		}
		return rs, nil
	}

	// --- Success Handling ---
	if span != nil { // GUARD
		span.AddEvent("HandlingSuccessfulTransactionResult", nil)
	}

	// --- Storage Roots ---
	storageRoots := make(map[string][]byte)
	storageRootDetails := make(map[string]string)
	fetchErrors := []string{}
	if len(mvmRs.MapStorageChange) > 0 {
		storageAddresses := make([]string, 0, len(mvmRs.MapStorageChange))
		for address := range mvmRs.MapStorageChange {
			storageAddresses = append(storageAddresses, address)
		}
		if span != nil { // GUARD
			span.AddEvent("FetchingStorageRoots", map[string]interface{}{"addresses": storageAddresses})
		}
		for address := range mvmRs.MapStorageChange {
			addr := common.HexToAddress(address)
			as := vmP.chainState.GetAccountStateDB()
			if as != nil {
				accountState, err := as.AccountState(addr)
				if err != nil {
					wrappedErr := fmt.Errorf("error getting account state for %s: %w", address, err)
					errMsg := wrappedErr.Error()
					// fetchErrors = append(fetchErrors, errMsg)
					logger.Error(errMsg)
					storageRoots[address] = trie.EmptyRootHash.Bytes()
					storageRootDetails[address] = fmt.Sprintf("Error: %s -> %s", errMsg, trie.EmptyRootHash.Hex())
					continue
				}
				if accountState != nil {
					smartContractState := accountState.SmartContractState()
					if smartContractState == nil {
						warnErr := fmt.Errorf("smart contract state not found for address %s during root fetch", address)
						warnMsg := warnErr.Error()
						logger.Warn(warnMsg)
						// fetchErrors = append(fetchErrors, warnMsg)
						storageRoots[address] = trie.EmptyRootHash.Bytes()
						storageRootDetails[address] = fmt.Sprintf("Warning: %s -> %s", warnMsg, trie.EmptyRootHash.Hex())
						continue
					}
					root := smartContractState.StorageRoot()
					storageRoots[address] = root.Bytes()
					storageRootDetails[address] = root.Hex()
				} else {
					warnErr := fmt.Errorf("account state is nil for address %s during root fetch", address)
					warnMsg := warnErr.Error()
					logger.Warn(warnMsg)
					fetchErrors = append(fetchErrors, warnMsg)
					storageRoots[address] = trie.EmptyRootHash.Bytes()
					storageRootDetails[address] = fmt.Sprintf("Warning: %s -> %s", warnMsg, trie.EmptyRootHash.Hex())
				}
			} else {
				err := fmt.Errorf("account state db is nil")
				errMsg := err.Error()
				fetchErrors = append(fetchErrors, errMsg)
				logger.Error(errMsg)
				storageRoots[address] = trie.EmptyRootHash.Bytes()
				storageRootDetails[address] = fmt.Sprintf("Error: %s -> %s", errMsg, trie.EmptyRootHash.Hex())
			}
		}
		if span != nil { // GUARD
			if len(fetchErrors) > 0 {
				span.SetAttribute("storageRootFetchErrors", fetchErrors)
			}
			span.SetAttribute("storageRootsCollectedDetails", storageRootDetails)
		}
	}

	// --- Event Logs ---
	eventLogs := mvmRs.EventLogs(transactionHash)
	logsHash := smart_contract.GetLogsHash(eventLogs)
	if span != nil { // GUARD
		span.AddEvent("ProcessingEventLogs", map[string]interface{}{
			"eventLogCount": len(eventLogs),
			"logsHash":      logsHash.Hex(),
		})
	}
	if len(eventLogs) > 0 {
		logSummaries := []map[string]interface{}{}
		for i, log := range eventLogs {
			topic0Hex := "N/A"
			if len(log.Topics()) > 0 {
				topic0Hex = log.Topics()[0]
			}
			logSummaries = append(logSummaries, map[string]interface{}{
				"index": i, "address": log.Address().Hex(), "topic0": topic0Hex,
				"numTopics": len(log.Topics()), "dataSize": len(log.Data()),
			})
		}
		if span != nil { // GUARD
			span.SetAttribute("eventLogSummaries", logSummaries)
		}
		if vmP.chainState != nil && vmP.chainState.GetSmartContractDB() != nil {
			vmP.chainState.GetSmartContractDB().AddEventLogs(eventLogs) // DB Add happens regardless of trace
		} else {
			logger.Warn("Smart contract DB is nil, cannot add event logs")
		}
	}

	// --- Deploy Info ---
	mapCreatorPubkey := make(map[string][]byte)
	mapStorageAddress := make(map[string]common.Address)
	deployInfoDetails := make(map[string]map[string]string)
	extractErrors := []string{}
	if len(mvmRs.MapCodeHash) > 0 {
		codeHashAddresses := make([]string, 0, len(mvmRs.MapCodeHash))
		for a := range mvmRs.MapCodeHash {
			codeHashAddresses = append(codeHashAddresses, a)
		}
		if span != nil { // GUARD
			span.AddEvent("ExtractingDeployInfo", map[string]interface{}{"addresses": codeHashAddresses})
		}
		as := vmP.chainState.GetAccountStateDB()
		if as != nil {
			for a := range mvmRs.MapCodeHash {
				addr := common.HexToAddress(a)
				details := map[string]string{"address": a}
				accountState, err := as.AccountState(addr)
				if err != nil {
					wrappedErr := fmt.Errorf("error getting account state for deployed contract %s: %w", a, err)
					errMsg := wrappedErr.Error()
					extractErrors = append(extractErrors, errMsg)
					logger.Error(errMsg)
					details["error"] = errMsg
					deployInfoDetails[a] = details
					continue
				}
				if accountState != nil {
					scState := accountState.SmartContractState()
					if scState == nil {
						err := fmt.Errorf("smartContractState is nil for deployed contract %s", a)
						errMsg := err.Error()
						extractErrors = append(extractErrors, errMsg)
						logger.Error(errMsg)
						details["error"] = errMsg
						deployInfoDetails[a] = details
						continue
					}
					creatorKey := scState.CreatorPublicKey()
					storageAddr := scState.StorageAddress()
					mapCreatorPubkey[a] = creatorKey.Bytes()
					mapStorageAddress[a] = storageAddr
					details["creatorKey"] = hex.EncodeToString(creatorKey.Bytes())
					details["storageAddr"] = storageAddr.Hex()
					deployInfoDetails[a] = details
				} else {
					warnErr := fmt.Errorf("account state is nil for deployed contract %s", a)
					warnMsg := warnErr.Error()
					logger.Warn(warnMsg)
					extractErrors = append(extractErrors, warnMsg)
					details["error"] = warnMsg
					deployInfoDetails[a] = details
				}
			}
		} else {
			err := fmt.Errorf("account state db is nil")
			errMsg := err.Error()
			extractErrors = append(extractErrors, errMsg)
			logger.Error(errMsg)
		}
		if span != nil { // GUARD
			if len(extractErrors) > 0 {
				span.SetAttribute("deployInfoExtractErrors", extractErrors)
			}
			span.SetAttribute("deployInfoCollectedDetails", deployInfoDetails)
		}
	}

	// Enhance maps for tracing
	mapAddBalanceStr := mapBytesToString(mvmRs.MapAddBalance)
	mapSubBalanceStr := mapBytesToString(mvmRs.MapSubBalance)
	mapNonceStr := mapBytesToNonceString(mvmRs.MapNonce)
	mapCodeHashStr := mapBytesToHashString(mvmRs.MapCodeHash)
	mapStorageRootsStr := mapBytesToHashString(storageRoots)
	mapStorageAddressStr := mapAddressToString(mapStorageAddress)
	mapCreatorPubkeyStr := mapBytesToString(mapCreatorPubkey)

	// ✅ Đưa exception message vào Return field nếu Return empty và có exception
	returnData := prepareReturnDataWithExceptionMessage(mvmRs.Return, mvmRs.Exmsg, mvmRs.Status, mvmRs.Exception)

	rs := smart_contract.NewExecuteSCResult(
		transactionHash, mvmRs.Status, mvmRs.Exception, returnData, mvmRs.GasUsed,
		logsHash,
		mvmRs.MapAddBalance, mvmRs.MapSubBalance, mvmRs.MapNonce,
		mvmRs.MapCodeHash, storageRoots,
		mapStorageAddress, mapCreatorPubkey,
		nil, nil,
		eventLogs,
	)

	if span != nil { // GUARD for final attributes
		span.SetAttribute("finalResultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("finalResultException", rs.Exception().String())
		span.SetAttribute("finalGasUsed", rs.GasUsed())
		span.SetAttribute("finalLogsHash", rs.LogsHash().Hex())
		span.SetAttribute("finalMapAddBalance", mapAddBalanceStr)
		span.SetAttribute("finalMapSubBalance", mapSubBalanceStr)
		span.SetAttribute("finalMapNonce", mapNonceStr)
		span.SetAttribute("finalMapCodeHash", mapCodeHashStr)
		span.SetAttribute("finalMapStorageRoots", mapStorageRootsStr)
		span.SetAttribute("finalMapStorageAddress", mapStorageAddressStr)
		span.SetAttribute("finalMapCreatorPubkey", mapCreatorPubkeyStr)
		span.SetAttribute("finalEventLogCount", len(rs.EventLogs()))
	}

	return rs, nil
}

func (vmP *VmProcessor) MvmResultToExecuteResultOffChain(
	ctx context.Context,
	transaction types.Transaction,
	mvmRs *mvm.MVMExecuteResult,
) (types.ExecuteSCResult, error) {

	transactionHash := transaction.Hash()

	return smart_contract.NewExecuteSCResult(
		transactionHash,
		mvmRs.Status,
		mvmRs.Exception,
		mvmRs.Return,
		mvmRs.GasUsed,
		common.Hash{},

		mvmRs.MapAddBalance,
		mvmRs.MapSubBalance,

		mvmRs.MapCodeHash,
		nil,

		nil,
		nil,

		nil,

		nil,
		nil,
		nil,
	), nil
}

// updateStateDB cập nhật trạng thái DB dựa trên kết quả MVM.
func (vmP *VmProcessor) updateStateDB(
	ctx context.Context,
	transaction types.Transaction,
	mvmRs *mvm.MVMExecuteResult,
	mvmId common.Address,
	isFreeGass bool,
) (bool, error) {
	var span *trace.Span = nil // Khởi tạo nil

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.updateStateDB", map[string]interface{}{
			"txHash":       transaction.Hash().Hex(),
			"mvmStatus":    mvmRs.Status.String(),
			"mvmException": mvmRs.Exception.String(),
			"mvmId":        mvmId.Hex(),
			"isReverted":   mvmRs.Status == pb.RECEIPT_STATUS_THREW || mvmRs.Status == pb.RECEIPT_STATUS_HALTED,
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	hasChanges := false
	changesSummary := make(map[string]interface{})
	updateErrors := []string{}
	var finalErr error

	// --- Revert Handling ---
	if mvmRs.Status == pb.RECEIPT_STATUS_THREW || mvmRs.Status == pb.RECEIPT_STATUS_HALTED {
		if span != nil { // GUARD
			span.AddEvent("HandlingRevertedState", map[string]interface{}{
				"status":    mvmRs.Status.String(),
				"exception": mvmRs.Exception.String(),
			})
		}
		trie_database.GetTrieDatabaseManager().FindAndSetTrieDatabasesByMvmID(mvmId, trie_database.Reverted)
		if span != nil { // GUARD
			span.AddEvent("MarkedTrieDBAsReverted", map[string]interface{}{"mvmId": mvmId.Hex()})
		}

		// We MUST clear ALL instances because relatedAddresses does NOT include self-accesses.
		// Example: A calls B. A throws. C++ cache has B's state from A's call.
		// If we only clear B, C++ still holds stale data for A.
		mvm.CallClearAllStateInstances()

		amount := transaction.Amount()
		if amount.Cmp(big.NewInt(0)) > 0 {
			fromAddr := transaction.FromAddress()
			toAddr := transaction.ToAddress()
			if span != nil { // GUARD
				span.AddEvent("RevertingTransactionAmount", map[string]interface{}{
					"amount": amount.String(),
					"from":   fromAddr.Hex(),
					"to":     toAddr.Hex(),
				})
				span.SetAttribute("revertedAmountFrom", fromAddr.Hex())
				span.SetAttribute("revertedAmountTo", toAddr.Hex())
			}
			vmP.chainState.GetAccountStateDB().AddPendingBalance(fromAddr, amount)
			vmP.chainState.GetAccountStateDB().SubTotalBalance(toAddr, amount)
		}
		if span != nil { // GUARD before return
			span.SetAttribute("hasChanges", false)
			span.SetAttribute("changesSummary", changesSummary)
			if len(updateErrors) > 0 {
				span.SetAttribute("updateWarningsOrErrors", updateErrors)
			}
		}

		if len(mvmRs.MapNonce) > 0 {
			details := make(map[string]string)
			fatalError := false
			for address, nonceBytes := range mvmRs.MapNonce {
				fmtAddress := common.HexToAddress(address)
				newNonceBig := big.NewInt(0).SetBytes(nonceBytes)
				newNonce, err := utils.BigIntToUint64(newNonceBig)
				if err != nil {
					errMsg := fmt.Sprintf("failed to convert nonce %s for %s: %v", newNonceBig.String(), address, err)
					if span != nil { // GUARD
						span.SetAttribute("nonceConversionError_"+address, errMsg)
					}
					logger.Warn(errMsg)
					updateErrors = append(updateErrors, errMsg)
					details[address] = fmt.Sprintf("ConversionError: %s", newNonceBig.String())
					continue
				}
				// 🔒 NONCE-FIX: For sender address, enforce nonce = oldNonce + 1
				if fmtAddress == transaction.FromAddress() {
					senderAs, asErr := vmP.chainState.GetAccountStateDB().AccountState(fmtAddress)
					if asErr == nil && senderAs != nil {
						expectedNonce := senderAs.Nonce() + 1
						if newNonce != expectedNonce {
							logger.Warn("🚨 [NONCE-FIX] REVERT: EVM returned nonce=%d but expected=%d for sender=%s, txHash=%s. Clamping to expected.", newNonce, expectedNonce, fmtAddress.Hex(), transaction.Hash().Hex())
							newNonce = expectedNonce
						}
					}
				}
				err = vmP.chainState.GetAccountStateDB().SetNonce(fmtAddress, newNonce)
				logger.Debug("[NONCE-TRACE] updateStateDB-REVERT: addr=%s, newNonce=%d, txHash=%s", fmtAddress.Hex(), newNonce, transaction.Hash().Hex())
				if err != nil {
					finalErr = fmt.Errorf("failed to set nonce %d for %s: %w", newNonce, address, err)
					if span != nil { // GUARD
						span.SetError(finalErr)
					}
					logger.Error(finalErr.Error())
					updateErrors = append(updateErrors, finalErr.Error())
					details[address] = fmt.Sprintf("SetError: %d", newNonce)
					fatalError = true
					break
				} else {
					details[address] = strconv.FormatUint(newNonce, 10)
				}
			}
			if span != nil { // GUARD
				span.AddEvent("UpdatingNonces", map[string]interface{}{"count": len(mvmRs.MapNonce), "details": details})
			}
			changesSummary["nonce"] = details
			if len(mvmRs.MapNonce) > 0 {
				hasChanges = true
			}
			if fatalError {
				if span != nil { // GUARD before return
					span.SetAttribute("hasChanges", hasChanges)
					span.SetAttribute("changesSummary", changesSummary)
					if len(updateErrors) > 0 {
						span.SetAttribute("updateWarningsOrErrors", updateErrors)
					}
				}
				return hasChanges, finalErr
			}
		}

		return false, nil
	}

	// --- Success Handling ---
	if span != nil { // GUARD
		span.AddEvent("HandlingSuccessfulStateUpdate", nil)
	}

	// --- AddBalance ---
	if len(mvmRs.MapAddBalance) > 0 {
		details := make(map[string]string)
		for address, addAmountBytes := range mvmRs.MapAddBalance {
			fmtAddress := common.HexToAddress(address)
			addAmount := big.NewInt(0).SetBytes(addAmountBytes)
			vmP.chainState.GetAccountStateDB().AddPendingBalance(fmtAddress, addAmount)
			details[address] = addAmount.String()
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingAddedBalances", map[string]interface{}{"count": len(mvmRs.MapAddBalance), "details": details})
		}
		changesSummary["addBalance"] = details
		hasChanges = true
	}

	// --- SubBalance ---
	if len(mvmRs.MapSubBalance) > 0 {
		details := make(map[string]string)
		fatalError := false
		for address, subAmountBytes := range mvmRs.MapSubBalance {
			fmtAddress := common.HexToAddress(address)
			subAmount := big.NewInt(0).SetBytes(subAmountBytes)

			err := vmP.chainState.GetAccountStateDB().SubTotalBalance(fmtAddress, subAmount)
			if err != nil {
				acc, _ := vmP.chainState.GetAccountStateDB().AccountState(fmtAddress)
				logger.Error("balance %s (amount: %s): %w", acc.Balance().String(), err)
				finalErr = fmt.Errorf("failed to subtract total balance for %s (amount: %s): %w", address, subAmount.String(), err)
				if span != nil { // GUARD
					span.SetError(finalErr)
				}
				logger.Error(finalErr.Error())
				updateErrors = append(updateErrors, finalErr.Error())
				details[address] = fmt.Sprintf("Error: %s", subAmount.String())
				fatalError = true
				break
			} else {
				details[address] = subAmount.String()
			}
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingSubtractedBalances", map[string]interface{}{"count": len(mvmRs.MapSubBalance), "details": details})
		}
		changesSummary["subBalance"] = details
		if len(mvmRs.MapSubBalance) > 0 {
			hasChanges = true
		}
		if fatalError {
			if span != nil { // GUARD before return
				span.SetAttribute("hasChanges", hasChanges)
				span.SetAttribute("changesSummary", changesSummary)
				if len(updateErrors) > 0 {
					span.SetAttribute("updateWarningsOrErrors", updateErrors)
				}
			}
			return hasChanges, finalErr
		}
	}

	// --- Nonce ---
	if len(mvmRs.MapNonce) > 0 {
		details := make(map[string]string)
		fatalError := false
		for address, nonceBytes := range mvmRs.MapNonce {
			fmtAddress := common.HexToAddress(address)
			newNonceBig := big.NewInt(0).SetBytes(nonceBytes)
			newNonce, err := utils.BigIntToUint64(newNonceBig)
			if err != nil {
				errMsg := fmt.Sprintf("failed to convert nonce %s for %s: %v", newNonceBig.String(), address, err)
				if span != nil { // GUARD
					span.SetAttribute("nonceConversionError_"+address, errMsg)
				}
				logger.Warn(errMsg)
				updateErrors = append(updateErrors, errMsg)
				details[address] = fmt.Sprintf("ConversionError: %s", newNonceBig.String())
				continue
			}
			// 🔒 NONCE-FIX: For sender address, enforce nonce = oldNonce + 1
			if fmtAddress == transaction.FromAddress() {
				senderAs, asErr := vmP.chainState.GetAccountStateDB().AccountState(fmtAddress)
				if asErr == nil && senderAs != nil {
					expectedNonce := senderAs.Nonce() + 1
					if newNonce != expectedNonce {
						logger.Warn("🚨 [NONCE-FIX] SUCCESS: EVM returned nonce=%d but expected=%d for sender=%s, txHash=%s. Clamping to expected.", newNonce, expectedNonce, fmtAddress.Hex(), transaction.Hash().Hex())
						newNonce = expectedNonce
					}
				}
			}
			err = vmP.chainState.GetAccountStateDB().SetNonce(fmtAddress, newNonce)
			logger.Debug("[NONCE-TRACE] updateStateDB-SUCCESS: addr=%s, newNonce=%d, txHash=%s", fmtAddress.Hex(), newNonce, transaction.Hash().Hex())
			if err != nil {
				finalErr = fmt.Errorf("failed to set nonce %d for %s: %w", newNonce, address, err)
				if span != nil { // GUARD
					span.SetError(finalErr)
				}
				logger.Error(finalErr.Error())
				updateErrors = append(updateErrors, finalErr.Error())
				details[address] = fmt.Sprintf("SetError: %d", newNonce)
				fatalError = true
				break
			} else {
				details[address] = strconv.FormatUint(newNonce, 10)
			}
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingNonces", map[string]interface{}{"count": len(mvmRs.MapNonce), "details": details})
		}
		changesSummary["nonce"] = details
		if len(mvmRs.MapNonce) > 0 {
			hasChanges = true
		}
		if fatalError {
			if span != nil { // GUARD before return
				span.SetAttribute("hasChanges", hasChanges)
				span.SetAttribute("changesSummary", changesSummary)
				if len(updateErrors) > 0 {
					span.SetAttribute("updateWarningsOrErrors", updateErrors)
				}
			}
			return hasChanges, finalErr
		}
	}

	// --- Deploy State ---
	logger.Debug("[DEPLOY-STATE] ApplyMVMToStateDB MapCodeHash len: %d, txTypeDeploy=%v", len(mvmRs.MapCodeHash), transaction.IsDeployContract())

	if len(mvmRs.MapCodeHash) > 0 {
		details := make(map[string]map[string]string)
		var creatorPublicKey mt_common.PublicKey
		var storageAddress common.Address
		determinedDeployInfo := false
		fatalError := false
		// Determine creator/storage...
		if transaction.IsDeployContract() {
			storageAddress = transaction.DeployData().StorageAddress()
			determinedDeployInfo = true
		} else if transaction.IsCallContract() {
			originSmartContractAs, err := vmP.chainState.GetAccountStateDB().AccountState(transaction.ToAddress())
			if err != nil {
				finalErr = fmt.Errorf("failed to get origin contract state %s for internal deploy: %w", transaction.ToAddress().Hex(), err)
				fatalError = true
			} else if originSmartContractAs == nil || originSmartContractAs.SmartContractState() == nil {
				finalErr = errors.New("origin contract state " + transaction.ToAddress().Hex() + " is nil or not a contract for internal deploy")
				fatalError = true
			} else {
				originScState := originSmartContractAs.SmartContractState()
				creatorPublicKey = originScState.CreatorPublicKey()
				storageAddress = originScState.StorageAddress()
				determinedDeployInfo = true
				if span != nil { // GUARD
					span.AddEvent("DeterminedDeployInfoFromOriginContract", map[string]interface{}{
						"originContract": transaction.ToAddress().Hex(), "creatorKey": hex.EncodeToString(creatorPublicKey.Bytes()), "storageAddr": storageAddress.Hex(),
					})
				}
			}
			if fatalError {
				if span != nil {
					span.SetError(finalErr)
				}
				logger.Error(finalErr.Error())
				updateErrors = append(updateErrors, finalErr.Error())
				if span != nil { // GUARD before return
					span.SetAttribute("hasChanges", hasChanges)
					span.SetAttribute("changesSummary", changesSummary)
					if len(updateErrors) > 0 {
						span.SetAttribute("updateWarningsOrErrors", updateErrors)
					}
				}
				return hasChanges, finalErr
			}
		}
		if !determinedDeployInfo {
			finalErr = errors.New("could not determine creator/storage info for deploy state update")
			if span != nil {
				span.SetError(finalErr)
			}
			logger.Error(finalErr.Error())
			if span != nil { // GUARD before return
				span.SetAttribute("hasChanges", hasChanges)
				span.SetAttribute("changesSummary", changesSummary)
				if len(updateErrors) > 0 {
					span.SetAttribute("updateWarningsOrErrors", updateErrors)
				}
			}
			return hasChanges, finalErr
		}
		// Apply state...
		logger.Debug("[DEPLOY-STATE] Starting deploy state loop, MapCodeHash len=%d", len(mvmRs.MapCodeHash))
		for address, newCodeHashBytes := range mvmRs.MapCodeHash {
			addrDetails := map[string]string{}
			fmtAddress := common.HexToAddress(address)
			newCodeHash := common.BytesToHash(newCodeHashBytes)
			addrDetails["codeHash"] = newCodeHash.Hex()
			logger.Debug("[DEPLOY-STATE] Getting AccountState for %s", fmtAddress.Hex())
			asState, err := vmP.chainState.GetAccountStateDB().AccountState(fmtAddress)
			if err != nil {
				finalErr = fmt.Errorf("error getting account state for new contract %s: %w", address, err)
				fatalError = true
				logger.Error("[DEPLOY-STATE] ERROR getting account state: %v", finalErr)
			} else if asState == nil {
				finalErr = errors.New("account state is nil for new contract " + address + " after MVM execution")
				fatalError = true
				logger.Error("[DEPLOY-STATE] asState is nil: %v", finalErr)
			} else {
				logger.Debug("[DEPLOY-STATE] asState OK for %s, scState=%v", fmtAddress.Hex(), asState.SmartContractState() != nil)
			}
			if fatalError {
				if span != nil {
					span.SetError(finalErr)
				}
				logger.Error(finalErr.Error())
				addrDetails["error"] = finalErr.Error()
				details[address] = addrDetails
				break
			}
			asState.SetCreatorPublicKey(creatorPublicKey)
			asState.SetStorageAddress(storageAddress)
			asState.SetCodeHash(newCodeHash)
			logger.Debug("[DEPLOY-STATE] Set CodeHash=%s, StorageAddr=%s for %s", newCodeHash.Hex(), storageAddress.Hex(), fmtAddress.Hex())
			addrDetails["creatorKeySet"] = hex.EncodeToString(creatorPublicKey.Bytes())
			addrDetails["storageAddrSet"] = storageAddress.Hex()
			if _, storageChanged := mvmRs.MapStorageChange[address]; !storageChanged {
				asState.SetStorageRoot(trie.EmptyRootHash)
				addrDetails["storageRootSet"] = trie.EmptyRootHash.Hex()
				if span != nil { // GUARD
					span.AddEvent("SetEmptyStorageRootForNewContract", map[string]interface{}{"address": address})
				}
			} else {
				addrDetails["storageRootSet"] = "(Deferred)"
			}
			logger.Debug("[DEPLOY-STATE] Calling SetState for %s, scState=%v", fmtAddress.Hex(), asState.SmartContractState() != nil)
			vmP.chainState.GetAccountStateDB().SetState(asState)
			logger.Debug("[DEPLOY-STATE] SetState done for %s, dirty=%d", fmtAddress.Hex(), vmP.chainState.GetAccountStateDB().DirtyAccountCount())
			details[address] = addrDetails
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingDeployedContractsState", map[string]interface{}{"count": len(mvmRs.MapCodeHash), "details": details})
		}
		changesSummary["deployState"] = details
		if len(mvmRs.MapCodeHash) > 0 {
			hasChanges = true
		}
		if fatalError {
			if span != nil { // GUARD before return
				span.SetAttribute("hasChanges", hasChanges)
				span.SetAttribute("changesSummary", changesSummary)
				if len(updateErrors) > 0 {
					span.SetAttribute("updateWarningsOrErrors", updateErrors)
				}
			}
			return hasChanges, finalErr
		}
	}

	// --- Code Change ---
	if len(mvmRs.MapCodeChange) > 0 {
		details := make(map[string]map[string]string)
		for address, code := range mvmRs.MapCodeChange {
			addrDetails := map[string]string{}
			fmtAddress := common.HexToAddress(address)
			codeHashBytes, ok := mvmRs.MapCodeHash[address]
			if !ok {
				errMsg := fmt.Sprintf("code hash not found for code change at address %s. Skipping code save.", address)
				if span != nil { // GUARD
					span.SetAttribute("missingCodeHashError_"+address, errMsg)
				}
				logger.Warn(errMsg)
				updateErrors = append(updateErrors, errMsg)
				addrDetails["error"] = errMsg
				addrDetails["codeSize"] = strconv.Itoa(len(code))
				details[address] = addrDetails
				continue
			}
			codeHash := common.BytesToHash(codeHashBytes)
			vmP.chainState.GetSmartContractDB().SetCode(fmtAddress, codeHash, code)
			addrDetails["codeHash"] = codeHash.Hex()
			addrDetails["codeSize"] = strconv.Itoa(len(code))
			details[address] = addrDetails
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingContractCode", map[string]interface{}{"count": len(mvmRs.MapCodeChange), "details": details})
		}
		changesSummary["codeChange"] = details
		if len(mvmRs.MapCodeChange) > 0 {
			hasChanges = true
		}
	}

	// --- Storage Change ---
	// OPTIMIZATION: Uses BatchSetStorageValues to batch all slot writes per contract
	// into a single trie.BatchUpdate() call. This reduces N × (loadStorageTrie +
	// mutex + hex.EncodeToString + DB read) to 1 × loadStorageTrie + parallel DB reads.
	if len(mvmRs.MapStorageChange) > 0 {
		details := make(map[string]map[string]string)
		totalSlotsUpdated := 0
		for address, rawStorages := range mvmRs.MapStorageChange {
			slotDetails := make(map[string]string)
			fmtAddress := common.HexToAddress(address)

			// Collect all slots into batch arrays
			keys := make([][]byte, 0, len(rawStorages))
			vals := make([][]byte, 0, len(rawStorages))
			for slotHex, value := range rawStorages {
				slotBytes := common.FromHex(slotHex)
				keys = append(keys, slotBytes)
				vals = append(vals, value)
				slotDetails[slotHex] = hex.EncodeToString(value)
				totalSlotsUpdated++
			}

			// Single batch update: 1 loadStorageTrie + 1 mutex lock + parallel DB reads
			if err := vmP.chainState.GetSmartContractDB().BatchSetStorageValues(fmtAddress, keys, vals); err != nil {
				errMsg := fmt.Sprintf("failed to batch set storage for %s: %v", address, err)
				logger.Error(errMsg)
				updateErrors = append(updateErrors, errMsg)
			}

			details[address] = slotDetails
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingContractStorage", map[string]interface{}{
				"addressCount": len(mvmRs.MapStorageChange),
				"totalSlots":   totalSlotsUpdated,
				"details":      details,
			})
		}
		changesSummary["storageSlots"] = details
		if len(mvmRs.MapStorageChange) > 0 {
			hasChanges = true
		}
	}

	// --- Storage Root Update ---
	if len(mvmRs.MapStorageChange) > 0 {
		details := make(map[string]string)
		fatalError := false
		for address := range mvmRs.MapStorageChange {
			fmtAddress := common.HexToAddress(address)
			newStorageRoot := vmP.chainState.GetSmartContractDB().StorageRoot(fmtAddress)
			err := vmP.chainState.GetAccountStateDB().SetStorageRoot(fmtAddress, newStorageRoot)
			if err != nil {
				finalErr = fmt.Errorf("failed to set storage root %s for %s: %w", newStorageRoot.Hex(), address, err)
				if span != nil { // GUARD
					span.SetError(finalErr)
				}
				logger.Error(finalErr.Error())
				updateErrors = append(updateErrors, finalErr.Error())
				details[address] = fmt.Sprintf("SetError: %s", newStorageRoot.Hex())
				fatalError = true
				break
			} else {
				details[address] = newStorageRoot.Hex()
			}
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingStorageRootsInAccountState", map[string]interface{}{"count": len(mvmRs.MapStorageChange), "details": details})
		}
		changesSummary["storageRootUpdate"] = details
		if fatalError {
			if span != nil { // GUARD before return
				span.SetAttribute("hasChanges", hasChanges)
				span.SetAttribute("changesSummary", changesSummary)
				if len(updateErrors) > 0 {
					span.SetAttribute("updateWarningsOrErrors", updateErrors)
				}
			}
			return hasChanges, finalErr
		}
	}

	// --- MapFullDbHash ---
	if len(mvmRs.MapFullDbHash) > 0 {
		details := make(map[string]map[string]string)
		dbHashUpdateErrors := []string{}
		for addressHex, newHashBytes := range mvmRs.MapFullDbHash {
			addrDetails := map[string]string{"newPartialHash": common.Bytes2Hex(newHashBytes)}
			fmtAddress := common.HexToAddress(addressHex)
			accountState, err := vmP.chainState.GetAccountStateDB().AccountState(fmtAddress)
			if err != nil {
				errMsg := fmt.Sprintf("error getting account state for %s during MapFullDbHash update: %v", addressHex, err)
				dbHashUpdateErrors = append(dbHashUpdateErrors, errMsg)
				logger.Error(errMsg)
				addrDetails["error"] = errMsg
				details[addressHex] = addrDetails
				continue
			}
			smartContractState := accountState.SmartContractState()
			if smartContractState == nil {
				errMsg := fmt.Sprintf("account %s does not have SmartContractState, skipping MapFullDbHash update.", addressHex)
				dbHashUpdateErrors = append(dbHashUpdateErrors, errMsg)
				logger.Warn(errMsg)
				addrDetails["warning"] = errMsg
				details[addressHex] = addrDetails
				continue
			}
			currentMapFullDbHash := smartContractState.MapFullDbHash()
			newMapFullDbHash := common.BytesToHash(newHashBytes)
			combinedHash := combineHashes(currentMapFullDbHash, newMapFullDbHash)
			smartContractState.SetMapFullDbHash(combinedHash)
			vmP.chainState.GetAccountStateDB().SetState(accountState)
			addrDetails["previousHash"] = currentMapFullDbHash.Hex()
			addrDetails["combinedHash"] = combinedHash.Hex()
			details[addressHex] = addrDetails
		}
		if span != nil { // GUARD
			span.AddEvent("UpdatingMapFullDbHash", map[string]interface{}{"count": len(mvmRs.MapFullDbHash), "details": details})
			if len(dbHashUpdateErrors) > 0 {
				span.SetAttribute("mapFullDbHashUpdateErrors", dbHashUpdateErrors)
			}
		}
		changesSummary["mapFullDbHash"] = details
		if len(mvmRs.MapFullDbHash) > 0 {
			hasChanges = true
		}
	}

	if mvmRs.GasUsed > 0 && !isFreeGass {
		fromAddr := transaction.FromAddress()
		vmP.chainState.GetAccountStateDB().SubTotalBalance(fromAddr, new(big.Int).SetUint64(mvmRs.GasUsed))
	}

	// --- Final Return ---
	if span != nil { // GUARD
		span.SetAttribute("hasChanges", hasChanges)
		span.SetAttribute("changesSummary", changesSummary)
		if len(updateErrors) > 0 {
			span.SetAttribute("updateWarningsOrErrors", updateErrors)
		}
	}
	return hasChanges, finalErr // finalErr will be nil if no fatal error occurred
}

// --- Helper functions for tracing (mapBytesToString, etc.) ---
func mapBytesToString(m map[string][]byte) map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		res[k] = hex.EncodeToString(v)
	}
	return res
}
func mapBytesToNonceString(m map[string][]byte) map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		val := big.NewInt(0).SetBytes(v)
		u64Val, err := utils.BigIntToUint64(val)
		if err != nil {
			res[k] = fmt.Sprintf("ErrorConv(%s)", val.String())
		} else {
			res[k] = strconv.FormatUint(u64Val, 10)
		}
	}
	return res
}
func mapBytesToHashString(m map[string][]byte) map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		res[k] = common.BytesToHash(v).Hex()
	}
	return res
}
func mapAddressToString(m map[string]common.Address) map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		res[k] = v.Hex()
	}
	return res
}

// --- End Helper functions ---
