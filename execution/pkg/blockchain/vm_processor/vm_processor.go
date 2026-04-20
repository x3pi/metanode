package vm_processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// VmProcessor struct now includes a flag to control tracing internally.
type VmProcessor struct {
	chainState     *blockchain.ChainState
	mvmId          common.Address // Consider if this is still needed here or managed by the caller.
	tracingEnabled bool
	blockTime      uint64
}

// NewVmProcessor tạo một thực thể VmProcessor mới và thiết lập trạng thái trace.
func NewVmProcessor(cs *blockchain.ChainState, mvmId common.Address, enableTrace bool, blockTime uint64) *VmProcessor {
	return &VmProcessor{
		chainState:     cs,
		mvmId:          mvmId,
		tracingEnabled: enableTrace,
		blockTime:      blockTime,
	}
}

// ExecuteTransactionWithMvmId thực thi giao dịch, sử dụng cờ tracingEnabled nội bộ.
func (vmP *VmProcessor) ExecuteTransactionWithMvmId(
	ctx context.Context, // Context gốc từ caller
	tx types.Transaction,
	extendedMode bool,
	isCache bool,
) (types.ExecuteSCResult, error) {
	var execCtx context.Context = ctx // Mặc định dùng context gốc
	var span *trace.Span = nil        // Mặc định span là nil

	if vmP.tracingEnabled { // Chỉ tạo span gốc nếu flag bật
		var actualSpan *trace.Span
		execCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.ExecuteTransactionWithMvmId", map[string]interface{}{
			"txHash":       tx.Hash().Hex(),
			"from":         tx.FromAddress().Hex(),
			"to":           tx.ToAddress().Hex(),
			"value":        tx.Amount().String(),
			"gasLimit":     tx.MaxGas(),
			"gasPrice":     tx.MaxGasPrice(),
			"nonce":        tx.GetNonce(),
			"isReadOnly":   tx.GetReadOnly(),
			"isDeploy":     tx.IsDeployContract(),
			"isCall":       tx.IsCallContract(),
			"extendedMode": extendedMode,
			"mvmId":        vmP.mvmId.Hex(),
		})
		span = actualSpan
		defer span.End() // Defer End cho span gốc này
	}

	if tx.GetReadOnly() {
		if span != nil {
			span.AddEvent("HandlingReadOnlyTransaction", nil)
		}
		mvmIdReadOnly := mvm.GenerateUniqueMvmId()if tx.GetReadOnly() {
		if span != nil {
			span.AddEvent("HandlingReadOnlyTransaction", nil)
		}
		mvmIdReadOnly := mvm.GenerateUniqueMvmId()
		if span != nil {
			span.SetAttribute("readOnlyMvmId", mvmIdReadOnly.Hex())
		}
		mvmROnly := mvm.GetOrCreateMVMApi(mvmIdReadOnly, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), extendedMode)
		mvmROnly.SetRelatedAddresses(tx.RelatedAddresses())
		result := vmP.readOnlyCall(execCtx, tx, mvmROnly)
		if span != nil {
			span.SetAttribute("readOnlyResultStatus", result.ReceiptStatus().String())
			span.SetAttribute("readOnlyResultGasUsed", result.GasUsed())
			span.SetAttribute("readOnlyResultReturnHex", hex.EncodeToString(result.Return()))
		}
		return result, nil
	}
		if span != nil {
			span.SetAttribute("readOnlyMvmId", mvmIdReadOnly.Hex())
		}
		mvmROnly := mvm.GetOrCreateMVMApi(mvmIdReadOnly, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), extendedMode)
		mvmROnly.SetRelatedAddresses(tx.RelatedAddresses())
		result := vmP.readOnlyCall(execCtx, tx, mvmROnly)
		if span != nil {
			span.SetAttribute("readOnlyResultStatus", result.ReceiptStatus().String())
			span.SetAttribute("readOnlyResultGasUsed", result.GasUsed())
			span.SetAttribute("readOnlyResultReturnHex", hex.EncodeToString(result.Return()))
		}
		return result, nil
	}

	if span != nil {
		span.AddEvent("HandlingWriteTransaction", map[string]interface{}{"actualMvmId": vmP.mvmId.Hex()})
	}
	if isCache {
		mvm.ProtectMVMApi(vmP.mvmId)
	}
	mvmE := mvm.GetOrCreateMVMApi(vmP.mvmId, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), extendedMode)
	mvmE.SetRelatedAddresses(tx.RelatedAddresses())
	if tx.IsRegularTransaction() || tx.ToAddress() == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		rs, err := vmP.sendNative(execCtx, tx, mvmE)
		if err != nil && span != nil {
			span.SetError(err)
		} else if span != nil {
			span.SetAttribute("executeResultStatus", rs.ReceiptStatus().String())
			span.SetAttribute("executeResultGasUsed", rs.GasUsed())
			span.SetAttribute("executeResultReturnHex", hex.EncodeToString(rs.Return()))
		}
		return rs, err

	}

	if tx.IsDeployContract() {
		if span != nil {
			span.AddEvent("HandlingDeployContract", map[string]interface{}{
				"deployDataCodeLength": len(tx.DeployData().Code()),
				"storageAddress":       tx.DeployData().StorageAddress().Hex(),
			})
		}
		result, err := vmP.deploySmartContract(execCtx, tx, mvmE, vmP.mvmId, isCache)
		if err != nil && span != nil {
			span.SetError(err)
		} else if span != nil {
			span.SetAttribute("deployResultStatus", result.ReceiptStatus().String())
			span.SetAttribute("deployResultGasUsed", result.GasUsed())
		}
		return result, err
	}

	if span != nil {
		span.AddEvent("HandlingCallContract", map[string]interface{}{
			"callDataInputLength": len(tx.CallData().Input()),
		})
	}

	if span != nil {
		span.AddEvent("SmartContractCallValidationPassed", nil)
	}

	if span != nil {
		span.AddEvent("ExecutingSmartContractViaMVM", nil)
	}
	rs, err := vmP.executeSmartContract(execCtx, tx, mvmE, isCache)
	if err != nil && span != nil {
		span.SetError(err)
	} else if span != nil {
		span.SetAttribute("executeResultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("executeResultGasUsed", rs.GasUsed())
		span.SetAttribute("executeResultReturnHex", hex.EncodeToString(rs.Return()))
	}
	return rs, err
}

// deploySmartContract xử lý việc deploy smart contract.
func (vmP *VmProcessor) deploySmartContract(
	ctx context.Context, // Context từ caller
	tx types.Transaction,
	mvmE *mvm.MVMApi,
	mvmId common.Address,
	isCache bool,
) (types.ExecuteSCResult, error) {
	var span *trace.Span = nil          // Khởi tạo nil
	var deployCtx context.Context = ctx // Mặc định dùng context vào
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()
	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		deployCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.deploySmartContract", map[string]interface{}{
			"mvmId":         mvmId.Hex(),
			"from":          tx.FromAddress().Hex(),
			"value":         tx.Amount().String(),
			"gasLimit":      tx.MaxGas(),
			"gasPrice":      tx.MaxGasPrice(),
			"nonce":         tx.GetNonce(),
			"codeSizeBytes": len(tx.DeployData().Code()),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	if span != nil { // GUARD
		span.AddEvent("CallingMvmDeploy", map[string]interface{}{
			"blockNumber": lastBlockHeader.BlockNumber() + 1,
			"blockTs":     vmP.blockTime,
			"leader":      lastBlockHeader.LeaderAddress().Hex(),
			"isDebug":     tx.GetIsDebug(),
			"commit":      true,
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
		lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmId, tx.Hash().Bytes(), tx.GetIsDebug(), isCache, false,
	)
	if span != nil { // GUARD
		span.AddEvent("MvmDeployFinished", map[string]interface{}{
			"status":        mvmResult.Status.String(),
			"exception":     mvmResult.Exception.String(),
			"gasUsed":       mvmResult.GasUsed,
			"returnLen":     len(mvmResult.Return),
			"returnHex":     hex.EncodeToString(mvmResult.Return),
			"balanceChange": len(mvmResult.MapAddBalance)+len(mvmResult.MapSubBalance) > 0,
			"nonceChange":   len(mvmResult.MapNonce) > 0,
			"codeChange":    len(mvmResult.MapCodeChange) > 0,
			"storageChange": len(mvmResult.MapStorageChange) > 0,
		})
		span.AddEvent("UpdatingStateDBAfterDeploy", nil)
	}

	_, err := vmP.updateStateDB(deployCtx, tx, mvmResult, mvmId, isFree) // Truyền deployCtx xuống
	if err != nil {
		wrappedErr := fmt.Errorf("failed to update state DB after deploy: %w", err)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("StateDBUpdateAfterDeployFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
		rs, _ := vmP.MvmResultToExecuteResult(deployCtx, tx, mvmResult) // Vẫn convert
		return rs, wrappedErr
	}
	if span != nil { // GUARD
		span.AddEvent("StateDBUpdateAfterDeployFinished", nil)
	}

	rs, errConvert := vmP.MvmResultToExecuteResult(deployCtx, tx, mvmResult) // Truyền deployCtx
	if errConvert != nil {
		wrappedErr := fmt.Errorf("failed to convert MVM result to execute result after deploy: %w", errConvert)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("MvmResultToExecuteResultFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
	}

	if span != nil { // GUARD
		span.AddEvent("ClearingMVMApiAfterDeploy", map[string]interface{}{"mvmIdToClear": mvmId.Hex()})
	}
	// logger.Error("ClearMVM: 3: %v", mvmId)

	mvm.ClearMVMApi(mvmId) // Luôn clear MVM API
	return rs, nil
}

// readOnlyCall xử lý lời gọi chỉ đọc.
func (vmP *VmProcessor) readOnlyCall(
	ctx context.Context,
	tx types.Transaction,
	mvmE *mvm.MVMApi,
) types.ExecuteSCResult {
	var span *trace.Span = nil // Khởi tạo nil
	// var readOnlyCtx context.Context = ctx // Không cần tạo context mới nếu không dùng
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.readOnlyCall", map[string]interface{}{
			"mvmId":       mvmE.GetKey().Hex(),
			"from":        tx.FromAddress().Hex(),
			"to":          tx.ToAddress().Hex(),
			"value":       tx.Amount().String(),
			"gasLimit":    tx.MaxGas(),
			"gasPrice":    tx.MaxGasPrice(),
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
		span.AddEvent("CallingMvmCallReadOnly", map[string]interface{}{
			"commit":  true, // Giữ nguyên logic gốc commit=true
			"isDebug": tx.GetIsDebug(),
		})
		span.SetAttribute("mvmCallCommitFlag", true)
	}
	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}
	mvmResult := mvmE.Call( // Luôn gọi MVM
		tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.CallData().Input(), tx.Amount(), tx.MaxGasPrice(), maxGas,
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(), true, tx.Hash().Bytes(), tx.RelatedAddresses(), tx.GetIsDebug(), true,
	)

	if span != nil { // GUARD
		span.AddEvent("MvmCallReadOnlyFinished", map[string]interface{}{
			"status":    mvmResult.Status.String(),
			"exception": mvmResult.Exception.String(),
			"gasUsed":   mvmResult.GasUsed,
			"returnLen": len(mvmResult.Return),
			"returnHex": hex.EncodeToString(mvmResult.Return),
		})
	}

	readOnlyMvmId := mvmE.GetKey()
	if span != nil { // GUARD
		span.AddEvent("ClearingMVMApiAfterReadOnlyCall", map[string]interface{}{"mvmIdToClear": readOnlyMvmId.Hex()})
	}
	// logger.Error("ClearMVM: 4", readOnlyMvmId)

	mvm.ClearMVMApi(readOnlyMvmId) // Luôn clear MVM API

	// ✅ Đưa exception message vào Return field nếu Return empty và có exception
	returnData := prepareReturnDataWithExceptionMessage(mvmResult.Return, mvmResult.Exmsg, mvmResult.Status, mvmResult.Exception)
	rs := smart_contract.NewExecuteSCResult(tx.Hash(), mvmResult.Status, mvmResult.Exception, returnData, mvmResult.GasUsed, common.Hash{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if span != nil { // GUARD
		span.SetAttribute("resultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("resultGasUsed", rs.GasUsed())
	}
	return rs
}

// executeSmartContract xử lý việc thực thi smart contract (call).
func (vmP *VmProcessor) executeSmartContract(
	ctx context.Context,
	tx types.Transaction,
	mvmE *mvm.MVMApi,
	isCache bool,
) (types.ExecuteSCResult, error) {
	var span *trace.Span = nil        // Khởi tạo nil
	var execCtx context.Context = ctx // Mặc định dùng context vào
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		execCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.executeSmartContract", map[string]interface{}{
			"mvmId":       mvmE.GetKey().Hex(),
			"from":        tx.FromAddress().Hex(),
			"to":          tx.ToAddress().Hex(),
			"value":       tx.Amount().String(),
			"gasLimit":    tx.MaxGas(),
			"gasPrice":    tx.MaxGasPrice(),
			"nonce":       hex.EncodeToString(tx.GetNonce32Bytes()),
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
		span.AddEvent("CallingMvmExecute", map[string]interface{}{
			"isDebug": tx.GetIsDebug(),
		})
	}
	var mvmResult *mvm.MVMExecuteResult
	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}

	startMVM := time.Now()
	if isCache {
		mvmResult = mvmE.Execute( // Luôn gọi MVM
			tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.CallData().Input(), tx.Amount(), tx.MaxGasPrice(), maxGas,
			lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
			lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(), tx.Hash().Bytes(), tx.RelatedAddresses(), tx.GetIsDebug(),
		)

	} else {
		mvmResult = mvmE.Call( // Luôn gọi MVM
			tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.CallData().Input(), tx.Amount(), tx.MaxGasPrice(), maxGas,
			lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
			lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(), false, tx.Hash().Bytes(), tx.RelatedAddresses(), tx.GetIsDebug(), false,
		)

	}
	mvmDuration := time.Since(startMVM)
	if span != nil { // GUARD
		span.AddEvent("MvmExecuteFinished", map[string]interface{}{
			"status":        mvmResult.Status.String(),
			"exception":     mvmResult.Exception.String(),
			"gasUsed":       mvmResult.GasUsed,
			"returnLen":     len(mvmResult.Return),
			"returnHex":     hex.EncodeToString(mvmResult.Return),
			"balanceChange": len(mvmResult.MapAddBalance)+len(mvmResult.MapSubBalance) > 0,
			"nonceChange":   len(mvmResult.MapNonce) > 0,
			"codeChange":    len(mvmResult.MapCodeChange) > 0,
			"storageChange": len(mvmResult.MapStorageChange) > 0,
		})
	}

	currentMvmId := mvmE.GetKey()
	if span != nil { // GUARD
		span.AddEvent("UpdatingStateDBAfterExecute", map[string]interface{}{"mvmIdToUpdate": currentMvmId.Hex()})
	}

	startStateDB := time.Now()
	_, err := vmP.updateStateDB(execCtx, tx, mvmResult, currentMvmId, isFree) // Pass execCtx xuống
	stateDBDuration := time.Since(startStateDB)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to update state DB after execute: %w", err)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("StateDBUpdateAfterExecuteFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
		rs, _ := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult) // Vẫn convert
		return rs, wrappedErr
	}
	if span != nil { // GUARD
		span.AddEvent("StateDBUpdateAfterExecuteFinished", nil)
	}

	rs, errConvert := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult) // Pass execCtx
	if errConvert != nil {
		wrappedErr := fmt.Errorf("failed to convert MVM result to execute result after execute: %w", errConvert)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("MvmResultToExecuteResultFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
	}

	// ⏱️ PERF: Log per-TX timing breakdown
	logger.Info("⏱️ [PERF-TX] tx=%s | MVM(C++): %v | StateDB: %v | Total: %v",
		tx.Hash().Hex()[:10], mvmDuration, stateDBDuration, mvmDuration+stateDBDuration)

	if span != nil { // GUARD
		span.AddEvent("MVMApiPersistsAfterExecute", map[string]interface{}{"mvmId": currentMvmId.Hex()})
	}
	if !isCache {
		mvm.ClearMVMApi(mvmE.GetKey())
	}
	return rs, nil
}

// ProcessNativeMintBurn xử lý mint/burn native token cho cross-cluster transfers
func (vmP *VmProcessor) ProcessNativeMintBurn(
	ctx context.Context,
	tx types.Transaction,
	operationType uint64, // 0: mint, 1: burn
) (types.ExecuteSCResult, error) {
	// 1. Khởi tạo MVMApi nội bộ cho quá trình chuyển tiền nghiêm ngặt
	mvmE := mvm.GetOrCreateMVMApi(vmP.mvmId, vmP.chainState.GetSmartContractDB(), vmP.chainState.GetAccountStateDB(), true)
	
	// 2. CHẶT CHẼ: Ràng buộc Related Addresses theo yêu cầu an toàn
	mvmE.SetRelatedAddresses(tx.RelatedAddresses())

	var span *trace.Span = nil        // Khởi tạo nil
	var execCtx context.Context = ctx // Mặc định dùng context vào
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		execCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.processNativeMintBurn", map[string]interface{}{
			"mvmId":         mvmE.GetKey().Hex(),
			"from":          tx.FromAddress().Hex(),
			"to":            tx.ToAddress().Hex(),
			"value":         tx.Amount().String(),
			"gasLimit":      tx.MaxGas(),
			"gasPrice":      tx.MaxGasPrice(),
			"operationType": operationType, // 0: mint, 1: burn
			"blockNumber":   lastBlockHeader.BlockNumber() + 1,
			"blockTs":       vmP.blockTime,
			"leader":        lastBlockHeader.LeaderAddress().Hex(),
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	if span != nil { // GUARD
		span.AddEvent("CallingMvmProcessNativeMintBurn", map[string]interface{}{
			"operationType": operationType,
		})
	}

	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}

	// Determine from and to addresses based on operation type
	var bFrom, bTo []byte
	if operationType == 1 { // burn operation: burn from 'from' address
		bFrom = tx.FromAddress().Bytes()
		bTo = tx.ToAddress().Bytes()
	} else { // mint operation: mint to 'to' address (from system)
		// For mint, we use a system address as 'from'
		systemAddr := common.HexToAddress("0x000000000000000000000000000000000000MINT")
		bFrom = systemAddr.Bytes()
		bTo = tx.ToAddress().Bytes()
	}

	mvmResult := mvmE.ProcessNativeMintBurn( // Call MVM ProcessNativeMintBurn
		bFrom, bTo, tx.Amount(), operationType, tx.MaxGasPrice(), maxGas,
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(),
	)

	if span != nil { // GUARD
		span.AddEvent("MvmProcessNativeMintBurnFinished", map[string]interface{}{
			"status":        mvmResult.Status.String(),
			"exception":     mvmResult.Exception.String(),
			"gasUsed":       mvmResult.GasUsed,
			"returnLen":     len(mvmResult.Return),
			"returnHex":     hex.EncodeToString(mvmResult.Return),
			"balanceChange": len(mvmResult.MapAddBalance)+len(mvmResult.MapSubBalance) > 0,
			"nonceChange":   len(mvmResult.MapNonce) > 0,
			"codeChange":    len(mvmResult.MapCodeChange) > 0,
			"storageChange": len(mvmResult.MapStorageChange) > 0,
		})
	}

	currentMvmId := mvmE.GetKey()
	if span != nil { // GUARD
		span.AddEvent("UpdatingStateDBAfterProcessNativeMintBurn", map[string]interface{}{"mvmIdToUpdate": currentMvmId.Hex()})
	}

	_, err := vmP.updateStateDB(execCtx, tx, mvmResult, currentMvmId, isFree) // Pass execCtx xuống
	if err != nil {
		wrappedErr := fmt.Errorf("failed to update state DB after processNativeMintBurn: %w", err)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("StateDBUpdateAfterProcessNativeMintBurnFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
		rs, _ := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult) // Vẫn convert
		return rs, wrappedErr
	}
	if span != nil { // GUARD
		span.AddEvent("StateDBUpdateAfterProcessNativeMintBurnFinished", nil)
	}

	rs, errConvert := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult) // Pass execCtx
	if errConvert != nil {
		wrappedErr := fmt.Errorf("failed to convert MVM result to execute result after processNativeMintBurn: %w", errConvert)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("MvmResultToExecuteResultFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
	}
	if span != nil { // GUARD
		span.AddEvent("MVMApiPersistsAfterProcessNativeMintBurn", map[string]interface{}{"mvmId": currentMvmId.Hex()})
	}
	return rs, nil
}

// executeSmartContract xử lý việc thực thi smart contract (call).
func (vmP *VmProcessor) sendNative(
	ctx context.Context,
	tx types.Transaction,
	mvmE *mvm.MVMApi,
) (types.ExecuteSCResult, error) {
	var span *trace.Span = nil        // Khởi tạo nil
	var execCtx context.Context = ctx // Mặc định dùng context vào
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		execCtx, actualSpan = trace.StartSpan(ctx, "VmProcessor.sendNative", map[string]interface{}{
			"mvmId":       mvmE.GetKey().Hex(),
			"from":        tx.FromAddress().Hex(),
			"to":          tx.ToAddress().Hex(),
			"value":       tx.Amount().String(),
			"gasLimit":    tx.MaxGas(),
			"gasPrice":    tx.MaxGasPrice(),
			"nonce":       hex.EncodeToString(tx.GetNonce32Bytes()),
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
		span.AddEvent("CallingMvmExecute", map[string]interface{}{
			"isDebug": tx.GetIsDebug(),
		})
	}
	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}
	mvmResult := mvmE.SendNative( // Luôn gọi MVM
		tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.Amount(), tx.MaxGasPrice(), maxGas,
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, vmP.blockTime, mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, lastBlockHeader.LeaderAddress(), mvmE.GetKey(),
	)
	if span != nil { // GUARD
		span.AddEvent("MvmExecuteFinished", map[string]interface{}{
			"status":        mvmResult.Status.String(),
			"exception":     mvmResult.Exception.String(),
			"gasUsed":       mvmResult.GasUsed,
			"returnLen":     len(mvmResult.Return),
			"returnHex":     hex.EncodeToString(mvmResult.Return),
			"balanceChange": len(mvmResult.MapAddBalance)+len(mvmResult.MapSubBalance) > 0,
			"nonceChange":   len(mvmResult.MapNonce) > 0,
			"codeChange":    len(mvmResult.MapCodeChange) > 0,
			"storageChange": len(mvmResult.MapStorageChange) > 0,
		})
	}

	currentMvmId := mvmE.GetKey()
	if span != nil { // GUARD
		span.AddEvent("UpdatingStateDBAfterExecute", map[string]interface{}{"mvmIdToUpdate": currentMvmId.Hex()})
	}

	_, err := vmP.updateStateDB(execCtx, tx, mvmResult, currentMvmId, isFree) // Pass execCtx xuống
	if err != nil {
		wrappedErr := fmt.Errorf("failed to update state DB after execute: %w", err)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("StateDBUpdateAfterExecuteFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
		rs, _ := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult) // Vẫn convert
		return rs, wrappedErr
	}
	if span != nil { // GUARD
		span.AddEvent("StateDBUpdateAfterExecuteFinished", nil)
	}

	rs, errConvert := vmP.MvmResultToExecuteResult(execCtx, tx, mvmResult) // Pass execCtx
	if errConvert != nil {
		wrappedErr := fmt.Errorf("failed to convert MVM result to execute result after execute: %w", errConvert)
		if span != nil { // GUARD
			span.SetError(wrappedErr)
			span.AddEvent("MvmResultToExecuteResultFailed", map[string]interface{}{"error": wrappedErr.Error()})
		}
	}

	if span != nil { // GUARD
		span.AddEvent("MVMApiPersistsAfterExecute", map[string]interface{}{"mvmId": currentMvmId.Hex()})
	}
	mvm.UnprotectMVMApi(currentMvmId)
	// mvm.ClearMVMApi(currentMvmId)
	return rs, nil
}

// invalidTransactionResponse tạo kết quả lỗi cho giao dịch không hợp lệ.
func (vmP *VmProcessor) invalidTransactionResponse(
	ctx context.Context,
	tx types.Transaction,
	reason string, // Expect lowercase reason
) types.ExecuteSCResult {
	var span *trace.Span = nil // Khởi tạo nil

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.invalidTransactionResponse", map[string]interface{}{
			"txHash": tx.Hash().Hex(),
			"reason": reason,
		})
		span = actualSpan
		defer func() {
			if span != nil {
				span.End()
			}
		}() // Defer có điều kiện
	}

	errorBytes := []byte(reason)
	rs := smart_contract.NewErrorExecuteSCResult(tx.Hash(), *pb.RECEIPT_STATUS_THREW.Enum(), *pb.EXCEPTION_NONE.Enum(), errorBytes)

	if span != nil { // GUARD
		span.SetAttribute("resultStatus", rs.ReceiptStatus().String())
		span.SetAttribute("resultException", rs.Exception().String())
		span.SetAttribute("errorBytesHex", hex.EncodeToString(errorBytes))
	}
	return rs
}

// ProcessMVMResult processes a pre-computed MVM result (used for batching).
func (vmP *VmProcessor) ProcessMVMResult(
	ctx context.Context,
	tx types.Transaction,
	mvmResult *mvm.MVMExecuteResult,
	mvmId common.Address,
	isFree bool,
) (types.ExecuteSCResult, error) {
	_, err := vmP.updateStateDB(ctx, tx, mvmResult, mvmId, isFree)
	if err != nil {
		rs, _ := vmP.MvmResultToExecuteResult(ctx, tx, mvmResult)
		return rs, err
	}
	rs, errConvert := vmP.MvmResultToExecuteResult(ctx, tx, mvmResult)
	return rs, errConvert
}
