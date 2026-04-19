package vm_processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// combineHashes kết hợp hai hash.
func combineHashes(hash1, hash2 common.Hash) common.Hash {
	if hash1 == (common.Hash{}) {
		return hash2
	}
	if hash2 == (common.Hash{}) {
		return hash1
	}
	combinedBytes := append(hash1.Bytes(), hash2.Bytes()...)
	combinedHashBytes := crypto.Keccak256(combinedBytes)
	return common.BytesToHash(combinedHashBytes)
}

// isValidSmartContractCall kiểm tra tính hợp lệ của lời gọi SC.
func (vmP *VmProcessor) IsValidSmartContractCall(toAccountState types.AccountState, tx types.Transaction) bool {
	if toAccountState == nil {
		logger.Error("IsValidSmartContractCall FAILED! toAccountState is nil for address: %s", tx.ToAddress().Hex())
		return false
	}
	scState := toAccountState.SmartContractState()
	if scState == nil {
		logger.Error("IsValidSmartContractCall FAILED! scState is nil for address: %s", tx.ToAddress().Hex())
		return false
	}
	expectedStorageRoot := vmP.chainState.GetSmartContractDB().StorageRoot(tx.ToAddress())
	actualStorageRoot := scState.StorageRoot()
	isValid := actualStorageRoot == expectedStorageRoot
	if !isValid {
		logger.Error("IsValidSmartContractCall FAILED! Address: %s | Actual: %s | Expected: %s", tx.ToAddress().Hex(), actualStorageRoot.Hex(), expectedStorageRoot.Hex())
	}
	return isValid
}

// --- Debug and Sub functions ---

func (vmP *VmProcessor) ExecuteTransactionWithMvmIdDebug(
	ctx context.Context,
	tx types.Transaction, extendedMode bool,
) (types.ExecuteSCResult, error) {
	var span *trace.Span = nil         // Khởi tạo nil
	var debugCtx context.Context = ctx // Mặc định dùng context vào
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		debugCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.ExecuteTransactionWithMvmIdDebug", map[string]interface{}{
			"txHash":       tx.Hash().Hex(),
			"from":         tx.FromAddress().Hex(),
			"to":           tx.ToAddress().Hex(),
			"value":        tx.Amount().String(),
			"gasLimit":     tx.MaxGas(),
			"gasPrice":     tx.MaxGasPrice(),
			"nonce":        tx.GetNonce(),
			"extendedMode": extendedMode,
			"blockNumber":  lastBlockHeader.BlockNumber() + 1,
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	combinedHash := sha256.Sum256([]byte(fmt.Sprintf("%x%d%d", tx.Hash(), rand.Int63(), time.Now().UnixNano())))
	ethAddressBytes := combinedHash[12:]
	mvmIdDebug := common.BytesToAddress(ethAddressBytes)
	if span != nil { // GUARD
		span.SetAttribute("debugMvmId", mvmIdDebug.Hex())
	}

	mvmDebug := mvm.GetOrCreateMVMApi(mvmIdDebug, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), extendedMode)
	mvmDebug.SetRelatedAddresses(tx.RelatedAddresses())

	result := vmP.callDebug(debugCtx, tx, mvmDebug) // Truyền debugCtx
	if span != nil {                                // GUARD
		span.SetAttribute("debugResultStatus", result.ReceiptStatus().String())
		span.SetAttribute("debugResultGasUsed", result.GasUsed())
		span.SetAttribute("debugResultReturnHex", hex.EncodeToString(result.Return()))
	}
	// logger.Error("ClearMVM: 4", mvmIdDebug)

	mvm.ClearMVMApi(mvmIdDebug) // Luôn clear
	if span != nil {            // GUARD
		span.AddEvent("ClearedDebugMVMApi", map[string]interface{}{"mvmIdCleared": mvmIdDebug.Hex()})
	}

	return result, nil
}

func (vmP *VmProcessor) callDebug(
	ctx context.Context,
	tx types.Transaction, mvmE *mvm.MVMApi,
) types.ExecuteSCResult {
	var span *trace.Span = nil             // Khởi tạo nil
	var callDebugCtx context.Context = ctx // Mặc định dùng context vào
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		callDebugCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.callDebug", map[string]interface{}{
			"mvmId":       mvmE.GetKey().Hex(),
			"from":        tx.FromAddress().Hex(),
			"to":          tx.ToAddress().Hex(),
			"value":       tx.Amount().String(),
			"inputLen":    len(tx.CallData().Input()),
			"inputHex":    hex.EncodeToString(tx.CallData().Input()),
			"blockNumber": lastBlockHeader.BlockNumber() + 1,
			"blockTs":     vmP.blockTime,
			"leader":      lastBlockHeader.LeaderAddress().Hex(),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	if span != nil { // GUARD
		span.AddEvent("CallingMvmCallDebug", map[string]interface{}{
			"commit":  false,
			"isDebug": tx.GetIsDebug(),
			"nonce":   hex.EncodeToString(tx.GetNonce32Bytes()),
		})
	}

	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}
	mvmResult := mvmE.Call( // Luôn gọi MVM
		tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.CallData().Input(), tx.Amount(), tx.MaxGasPrice(), maxGas,
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(), false, tx.Hash().Bytes(), tx.RelatedAddresses(), tx.GetIsDebug(), false,
	)

	if span != nil { // GUARD
		span.AddEvent("MvmCallDebugFinished", map[string]interface{}{
			"status":           mvmResult.Status.String(),
			"exception":        mvmResult.Exception.String(),
			"gasUsed":          mvmResult.GasUsed,
			"returnLen":        len(mvmResult.Return),
			"returnHex":        hex.EncodeToString(mvmResult.Return),
			"numLogs":          len(mvmResult.JEventLogs.Logs),
			"potBalanceChange": len(mvmResult.MapAddBalance)+len(mvmResult.MapSubBalance) > 0,
			"potNonceChange":   len(mvmResult.MapNonce) > 0,
			"potCodeChange":    len(mvmResult.MapCodeChange) > 0,
			"potStorageChange": len(mvmResult.MapStorageChange) > 0,
		})
	}

	rs, err := vmP.mvmResultToExecuteResultDebug(callDebugCtx, tx, mvmResult, lastBlockHeader) // Truyền callDebugCtx
	if err != nil {
		wrappedErr := fmt.Errorf("error converting debug MVM result: %w", err)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("MvmResultConversionDebugFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
		errorBytes := []byte(wrappedErr.Error())
		return smart_contract.NewErrorExecuteSCResult(tx.Hash(), *pb.RECEIPT_STATUS_TRANSACTION_ERROR.Enum(), *pb.EXCEPTION_NONE.Enum(), errorBytes)
	}

	if span != nil { // GUARD
		span.SetAttribute("resultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("resultException", rs.Exception().String())
		span.SetAttribute("resultGasUsed", rs.GasUsed())
	}
	return rs
}

func (vmP *VmProcessor) mvmResultToExecuteResultDebug(
	ctx context.Context,
	transaction types.Transaction,
	mvmRs *mvm.MVMExecuteResult,
	lastBlockHeader types.BlockHeader,
) (types.ExecuteSCResult, error) {
	var span *trace.Span = nil // Khởi tạo nil

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.mvmResultToExecuteResultDebug", map[string]interface{}{
			"txHash":       transaction.Hash().Hex(),
			"mvmStatus":    mvmRs.Status.String(),
			"mvmException": mvmRs.Exception.String(),
			"mvmGasUsed":   mvmRs.GasUsed,
			"mvmReturnLen": len(mvmRs.Return),
			"mvmReturnHex": hex.EncodeToString(mvmRs.Return),
			"numLogs":      len(mvmRs.JEventLogs.Logs),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	transactionHash := transaction.Hash()
	eventLogs := mvmRs.EventLogs(transactionHash)
	logsHash := smart_contract.GetLogsHash(eventLogs)

	if span != nil { // GUARD
		span.AddEvent("ProcessingDebugResult", map[string]interface{}{
			"status":    mvmRs.Status.String(),
			"exception": mvmRs.Exception.String(),
			"gasUsed":   mvmRs.GasUsed,
			"returnHex": hex.EncodeToString(mvmRs.Return),
			"logCount":  len(eventLogs),
			"logsHash":  logsHash.Hex(),
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
			span.SetAttribute("debugEventLogSummaries", logSummaries)
		}
	}

	rs := smart_contract.NewExecuteSCResult(
		transactionHash, mvmRs.Status, mvmRs.Exception, mvmRs.Return, mvmRs.GasUsed,
		logsHash, nil, nil, nil, nil, nil, nil, nil, nil, nil, eventLogs,
	)

	if span != nil { // GUARD
		span.SetAttribute("finalDebugResultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("finalDebugResultException", rs.Exception().String())
		span.SetAttribute("finalDebugResultGasUsed", rs.GasUsed())
		span.SetAttribute("finalDebugLogsHash", rs.LogsHash().Hex())
		span.SetAttribute("finalDebugEventLogCount", len(rs.EventLogs()))
	}

	return rs, nil
}

func (vmP *VmProcessor) ExecuteTransactionWithMvmIdSub(
	ctx context.Context,
	tx types.Transaction, extendedMode bool,
) (types.ExecuteSCResult, bool, error) {
	var span *trace.Span = nil       // Khởi tạo nil
	var subCtx context.Context = ctx // Mặc định dùng context vào
	logger.Warn("ExecuteTransactionWithMvmIdSub using potentially shared MVM ID", "mvmId", vmP.mvmId.Hex())
	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		subCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.ExecuteTransactionWithMvmIdSub", map[string]interface{}{
			"txHash":       tx.Hash().Hex(),
			"from":         tx.FromAddress().Hex(),
			"to":           tx.ToAddress().Hex(),
			"value":        tx.Amount().String(),
			"extendedMode": extendedMode,
			"mvmIdUsed":    vmP.mvmId.Hex(),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	mvmSub := mvm.GetOrCreateMVMApi(vmP.mvmId, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), extendedMode)
	mvmSub.SetRelatedAddresses(tx.RelatedAddresses())
	rs, status := vmP.onlyCall(subCtx, tx, mvmSub) // Truyền subCtx
	if span != nil {                               // GUARD
		span.AddEvent("SubCallResult", map[string]interface{}{
			"resultStatus": rs.ReceiptStatus().String(),
			"resultGas":    rs.GasUsed(),
			"stateChanged": status,
		})
	}
	return rs, status, nil
}
func (vmP *VmProcessor) onlyCall(
	ctx context.Context,
	tx types.Transaction, mvmE *mvm.MVMApi,
) (types.ExecuteSCResult, bool) {
	var span *trace.Span = nil // Khởi tạo nil
	// var onlyCallCtx context.Context = ctx // Không cần nếu không truyền xuống
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()
	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.onlyCall", map[string]interface{}{
			"mvmId":       mvmE.GetKey().Hex(),
			"from":        tx.FromAddress().Hex(),
			"to":          tx.ToAddress().Hex(),
			"value":       tx.Amount().String(),
			"inputLen":    len(tx.CallData().Input()),
			"inputHex":    hex.EncodeToString(tx.CallData().Input()),
			"blockNumber": lastBlockHeader.BlockNumber() + 1,
			"blockTs":     vmP.blockTime,
			"leader":      lastBlockHeader.LeaderAddress().Hex(),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	if span != nil { // GUARD
		span.AddEvent("CallingMvmCallSub", map[string]interface{}{
			"commit":      false,
			"isDebug":     tx.GetIsDebug(),
			"nonceSource": "LastHash",
		})
	}
	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}
	mvmResult := mvmE.Call( // Luôn gọi MVM
		tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.CallData().Input(), tx.Amount(), tx.MaxGasPrice(), maxGas,
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(), false, tx.Hash().Bytes(), tx.RelatedAddresses(), tx.GetIsDebug(), false,
	)

	if span != nil { // GUARD
		span.AddEvent("MvmCallSubFinished", map[string]interface{}{
			"status":           mvmResult.Status.String(),
			"exception":        mvmResult.Exception.String(),
			"gasUsed":          mvmResult.GasUsed,
			"returnLen":        len(mvmResult.Return),
			"returnHex":        hex.EncodeToString(mvmResult.Return),
			"numLogs":          len(mvmResult.JEventLogs.Logs),
			"potBalanceChange": len(mvmResult.MapAddBalance)+len(mvmResult.MapSubBalance) > 0,
			"potNonceChange":   len(mvmResult.MapNonce) > 0,
			"potCodeChange":    len(mvmResult.MapCodeChange) > 0,
			"potStorageChange": len(mvmResult.MapStorageChange) > 0,
			"potFullDbChange":  len(mvmResult.MapFullDbHash) > 0,
		})
	}

	// checkStateDBStatus không trace, dùng ctx gốc
	stateChanged := vmP.checkStateDBStatus(ctx, tx, mvmResult)
	if span != nil { // GUARD
		span.SetAttribute("potentialStateChangeReported", stateChanged)
	}

	// ✅ Đưa exception message vào Return field nếu Return empty và có exception
	returnData := prepareReturnDataWithExceptionMessage(mvmResult.Return, mvmResult.Exmsg, mvmResult.Status, mvmResult.Exception)
	rs := smart_contract.NewExecuteSCResult(tx.Hash(), mvmResult.Status, mvmResult.Exception, returnData, mvmResult.GasUsed, common.Hash{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if span != nil { // GUARD
		span.SetAttribute("resultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("resultException", rs.Exception().String())
		span.SetAttribute("resultGasUsed", rs.GasUsed())
		if mvmResult.Exmsg != "" && len(mvmResult.Return) == 0 {
			span.SetAttribute("exceptionMessageIncluded", mvmResult.Exmsg)
		}
	}

	return rs, stateChanged
}

// prepareReturnDataWithExceptionMessage ưu tiên sử dụng returnData từ REVERT opcode, chỉ encode từ exmsg khi returnData empty
// ✅ Helper function để đảm bảo exception message từ C++ được đưa vào Return field của receipt theo chuẩn EVM Error(string)
// Quy tắc:
// 1. Nếu returnData đã có data (từ REVERT opcode) → Giữ nguyên (đây là revert data gốc từ contract)
// 2. Nếu returnData empty và có exmsg → Encode exmsg theo chuẩn EVM Error(string)
// 3. Nếu không có cả hai → Giữ nguyên (empty)
func prepareReturnDataWithExceptionMessage(returnData []byte, exmsg string, status pb.RECEIPT_STATUS, exception pb.EXCEPTION) []byte {
	// Nếu không có exception, giữ nguyên Return data
	if status == pb.RECEIPT_STATUS_RETURNED {
		return returnData
	}

	// ✅ Quan trọng: Nếu returnData đã có data (từ REVERT opcode), giữ nguyên
	// Đây là revert data gốc từ contract, có thể là Error(string), Panic, hoặc custom error
	// Không nên thay thế bằng exmsg vì sẽ làm mất revert data gốc từ contract
	if len(returnData) > 0 {
		return returnData
	}

	// ✅ Chỉ encode từ exmsg khi returnData empty
	// Trường hợp này xảy ra khi C++ throw exception (không phải từ REVERT opcode)
	if exmsg != "" {
		// ✅ Encode exception message theo chuẩn EVM Error(string) với selector 0x08c379a0
		// Format: 0x08c379a0 + offset (32 bytes) + length (32 bytes) + string data (padded)
		// Đây là format chuẩn mà các EVM client (ethers.js, web3.js) có thể decode
		return utils.EncodeRevertReason(exmsg)
	}

	// Nếu không có cả returnData và exmsg, giữ nguyên (empty)
	return returnData
}

// checkStateDBStatus checks MVM result maps to see if state *would* have changed.
// Currently does not perform tracing itself.
func (vmP *VmProcessor) checkStateDBStatus(
	ctx context.Context, // Context is available if tracing needed later
	transaction types.Transaction,
	mvmRs *mvm.MVMExecuteResult,
) bool {
	if mvmRs.Status == pb.RECEIPT_STATUS_THREW || mvmRs.Status == pb.RECEIPT_STATUS_HALTED {
		return false
	}
	if len(mvmRs.MapAddBalance) > 0 {
		return true
	}
	if len(mvmRs.MapSubBalance) > 0 {
		return true
	}
	if len(mvmRs.JEventLogs.Logs) > 0 {
		return true
	}
	if len(mvmRs.MapCodeHash) > 0 {
		return true
	}
	if len(mvmRs.MapCodeChange) > 0 {
		return true
	}
	if len(mvmRs.MapStorageChange) > 0 {
		return true
	}
	if len(mvmRs.MapNonce) > 0 {
		return true
	}
	if len(mvmRs.MapFullDbHash) > 0 {
		return true
	}
	return false
}

func (vmP *VmProcessor) ExecuteTransactionWithMvmIdSubDeploy(
	ctx context.Context,
	tx types.Transaction, mvmId common.Address, extendedMode bool,
) (types.ExecuteSCResult, bool, error) {
	var span *trace.Span = nil             // Khởi tạo nil
	var subDeployCtx context.Context = ctx // Mặc định dùng context vào

	logger.Warn("ExecuteTransactionWithMvmIdSubDeploy using potentially shared MVM ID", "mvmId", mvmId.Hex())

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		subDeployCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.ExecuteTransactionWithMvmIdSubDeploy", map[string]interface{}{
			"txHash":       tx.Hash().Hex(),
			"from":         tx.FromAddress().Hex(),
			"value":        tx.Amount().String(),
			"extendedMode": extendedMode,
			"mvmIdUsed":    mvmId.Hex(),
			"codeSize":     len(tx.DeployData().Code()),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	mvmSubDeploy := mvm.GetOrCreateMVMApi(mvmId, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), extendedMode)
	mvmSubDeploy.SetRelatedAddresses(tx.RelatedAddresses())

	rs, status := vmP.onlyDeploy(subDeployCtx, tx, mvmSubDeploy) // Truyền subDeployCtx

	if span != nil { // GUARD
		span.AddEvent("SubDeployResult", map[string]interface{}{
			"resultStatus": rs.ReceiptStatus().String(),
			"resultGas":    rs.GasUsed(),
			"stateChanged": status,
		})
	}

	return rs, status, nil
}

func (vmP *VmProcessor) onlyDeploy(
	ctx context.Context,
	tx types.Transaction, mvmE *mvm.MVMApi,
) (types.ExecuteSCResult, bool) {
	var span *trace.Span = nil // Khởi tạo nil
	// var onlyDeployCtx context.Context = ctx // Không cần nếu không truyền xuống
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.onlyDeploy", map[string]interface{}{
			"mvmId":       mvmE.GetKey().Hex(),
			"from":        tx.FromAddress().Hex(),
			"value":       tx.Amount().String(),
			"nonce":       tx.GetNonce(),
			"codeSize":    len(tx.DeployData().Code()),
			"blockNumber": lastBlockHeader.BlockNumber() + 1,
			"blockTs":     vmP.blockTime,
			"leader":      lastBlockHeader.LeaderAddress().Hex(),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	if span != nil { // GUARD
		span.AddEvent("CallingMvmDeploySub", map[string]interface{}{
			"commit":  false,
			"isDebug": tx.GetIsDebug(),
		})
	}

	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}
	mvmResult := mvmE.Deploy( // Luôn gọi MVM
		tx.FromAddress().Bytes(), tx.DeployData().Code(), tx.Amount(), tx.MaxGasPrice(), maxGas,
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(), tx.Hash().Bytes(), tx.GetIsDebug(), false, false,
	)

	if span != nil { // GUARD
		span.AddEvent("MvmDeploySubFinished", map[string]interface{}{
			"status":           mvmResult.Status.String(),
			"exception":        mvmResult.Exception.String(),
			"gasUsed":          mvmResult.GasUsed,
			"returnLen":        len(mvmResult.Return),
			"returnHex":        hex.EncodeToString(mvmResult.Return),
			"numLogs":          len(mvmResult.JEventLogs.Logs),
			"potBalanceChange": len(mvmResult.MapAddBalance)+len(mvmResult.MapSubBalance) > 0,
			"potNonceChange":   len(mvmResult.MapNonce) > 0,
			"potCodeChange":    len(mvmResult.MapCodeChange) > 0,
			"potStorageChange": len(mvmResult.MapStorageChange) > 0,
			"potFullDbChange":  len(mvmResult.MapFullDbHash) > 0,
		})
	}

	// checkStateDBStatus không trace, dùng ctx gốc
	stateChanged := vmP.checkStateDBStatus(ctx, tx, mvmResult)
	if span != nil { // GUARD
		span.SetAttribute("potentialStateChangeReported", stateChanged)
	}

	// ✅ Đưa exception message vào Return field nếu Return empty và có exception
	returnData := prepareReturnDataWithExceptionMessage(mvmResult.Return, mvmResult.Exmsg, mvmResult.Status, mvmResult.Exception)
	rs := smart_contract.NewExecuteSCResult(tx.Hash(), mvmResult.Status, mvmResult.Exception, returnData, mvmResult.GasUsed, common.Hash{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if span != nil { // GUARD
		span.SetAttribute("resultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("resultException", rs.Exception().String())
		span.SetAttribute("resultGasUsed", rs.GasUsed())
		if mvmResult.Exmsg != "" && len(mvmResult.Return) == 0 {
			span.SetAttribute("exceptionMessageIncluded", mvmResult.Exmsg)
		}
	}

	return rs, stateChanged
}

// executeNonceOnly xử lý một giao dịch chỉ để tăng nonce mà không thực thi EVM.
// Điều này hữu ích cho các giao dịch hệ thống cần cập nhật nonce một cách rõ ràng.
func (vmP *VmProcessor) ExecuteNonceOnly(
	ctx context.Context,
	tx types.Transaction,
	isCache bool,
) (types.ExecuteSCResult, error) {
	var execCtx context.Context = ctx
	var span *trace.Span = nil

	// Bắt đầu trace span nếu tracing được bật
	if vmP.tracingEnabled {
		var actualSpan *trace.Span
		execCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.executeNonceOnly", map[string]interface{}{
			"txHash":   tx.Hash().Hex(),
			"from":     tx.FromAddress().Hex(),
			"gasLimit": tx.MaxGas(),
			"gasPrice": tx.MaxGasPrice(),
			"nonce":    tx.GetNonce(),
			"isCache":  isCache,
			"mvmId":    vmP.mvmId.Hex(),
		})
		span = actualSpan
		defer span.End() // Kết thúc span khi hàm này thoát
	}

	if span != nil {
		span.AddEvent("HandlingNonceOnlyTransaction", nil)
	}

	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	// Lấy hoặc tạo MVM API instance
	mvmE := mvm.GetOrCreateMVMApi(vmP.mvmId, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), false)
	mvmE.SetRelatedAddresses(tx.RelatedAddresses()) // Đặt các địa chỉ liên quan cho tính nhất quán

	if span != nil {
		span.AddEvent("CallingMvmNoncePlusOne", map[string]interface{}{
			"blockNumber": lastBlockHeader.BlockNumber() + 1,
			"blockTs":     vmP.blockTime,
			"leader":      lastBlockHeader.LeaderAddress().Hex(),
		})
	}

	// Kiểm tra xem địa chỉ gửi có được miễn phí gas không
	_, isFreeSender := vmP.chainState.GetFreeFeeAddress()[tx.FromAddress()]

	// Gọi hàm NoncePlusOne từ MVM
	mvmResult := mvmE.NoncePlusOne(
		tx.FromAddress().Bytes(),
		tx.MaxGasPrice(),
		tx.MaxGas(),
		lastBlockHeader.TimeStamp(), // Sử dụng blockPrevrandao nhất quán với các cuộc gọi MVM khác
		mt_common.BLOCK_GAS_LIMIT,
		vmP.blockTime,
		mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1,
		lastBlockHeader.LeaderAddress(),
		vmP.mvmId,
	)

	if span != nil {
		span.AddEvent("MvmNoncePlusOneFinished", map[string]interface{}{
			"status":      mvmResult.Status.String(),
			"exception":   mvmResult.Exception.String(),
			"gasUsed":     mvmResult.GasUsed,
			"nonceChange": len(mvmResult.MapNonce) > 0,
		})
		span.AddEvent("UpdatingStateDBAfterNoncePlusOne", nil)
	}

	// Cập nhật trạng thái DB dựa trên kết quả từ MVM
	_, err := vmP.updateStateDB(execCtx, tx, mvmResult, vmP.mvmId, isFreeSender)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to update state DB after NoncePlusOne: %w", err)
		if span != nil {
			span.SetError(wrappedErr)
			span.AddEvent("StateDBUpdateAfterNoncePlusOneFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
		// Vẫn chuyển đổi kết quả MVM để trả về một ExecuteSCResult có ý nghĩa
		rs, _ := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult)
		return rs, wrappedErr
	}
	if span != nil {
		span.AddEvent("StateDBUpdateAfterNoncePlusOneFinished", nil)
	}

	// Chuyển đổi kết quả MVM sang ExecuteSCResult
	rs, errConvert := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult)
	if errConvert != nil {
		wrappedErr := fmt.Errorf("failed to convert MVM result to execute result after NoncePlusOne: %w", errConvert)
		if span != nil {
			span.SetError(wrappedErr)
			span.AddEvent("MvmResultToExecuteResultConversionFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
		// Vẫn trả về rs đã chuyển đổi, ngay cả khi có lỗi chuyển đổi
	}

	// Xóa MVM API instance nếu không ở chế độ cache, vì đây là một hoạt động thay đổi trạng thái
	if !isCache {
		mvm.ClearMVMApi(vmP.mvmId)
	}
	return rs, nil
}
