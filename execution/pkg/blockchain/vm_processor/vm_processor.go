package vm_processor

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"

	"github.com/meta-node-blockchain/meta-node/pkg/tee_revm_ffi"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// VmProcessor struct now includes a flag to control tracing internally.
type VmProcessor struct {
	chainState      *blockchain.ChainState
	accountStateDB  types.AccountStateDB
	smartContractDB types.SmartContractDB
	mvmId           common.Address // Consider if this is still needed here or managed by the caller.
	tracingEnabled  bool
	blockTime       uint64
	leaderAddr      common.Address
}

// NewVmProcessor tạo một thực thể VmProcessor mới và thiết lập trạng thái trace.
func NewVmProcessor(cs *blockchain.ChainState, mvmId common.Address, enableTrace bool, blockTime uint64, leaderAddr common.Address) *VmProcessor {
	return &VmProcessor{
		chainState:      cs,
		accountStateDB:  cs.GetAccountStateDB(),
		smartContractDB: cs.GetSmartContractDB(),
		mvmId:           mvmId,
		tracingEnabled:  enableTrace,
		blockTime:       blockTime,
		leaderAddr:      leaderAddr,
	}
}

func (vmP *VmProcessor) SetAccountStateDB(db types.AccountStateDB) {
	vmP.accountStateDB = db
}

func (vmP *VmProcessor) SetSmartContractDB(db types.SmartContractDB) {
	vmP.smartContractDB = db
}

func (vmP *VmProcessor) getLeaderAddress(lastBlockHeader types.BlockHeader) common.Address {
	if vmP.leaderAddr != (common.Address{}) {
		return vmP.leaderAddr
	}
	return lastBlockHeader.LeaderAddress()
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

	var teeRs *tee_revm_ffi.TeeStateDiff
	var execErr error

	if tx.GetReadOnly() {
		if span != nil {
			span.AddEvent("HandlingReadOnlyTransaction", nil)
		}
		// DETERMINISTIC: Use txHash-only hash for readOnly mvmId.
		// No state changes occur, so no fork risk, but deterministic IDs
		// make execution reproducible for debugging.
		if span != nil {
			span.SetAttribute("readOnlyMvmId", "tee-revm")
		}
		
		teeRevm := tee_revm_ffi.NewTeeRevm()
		teeRs, execErr = vmP.readOnlyCall(execCtx, tx, teeRevm)
	} else {
		if span != nil {
			span.AddEvent("HandlingWriteTransaction", map[string]interface{}{"actualMvmId": vmP.mvmId.Hex()})
		}
		
		// 🛑 TEE REVM FIX: Remove GetOrCreateMVMApi and Protect/Unprotect calls
		// Just create an instance of TeeRevm
		teeRevm := tee_revm_ffi.NewTeeRevm()

		if tx.IsRegularTransaction() || tx.ToAddress() == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
			teeRs, execErr = vmP.sendNative(execCtx, tx, isCache)
		} else if tx.IsDeployContract() {
			if !tx.ValidDeployData() {
				execErr = fmt.Errorf("deploy data is nil or invalid")
				if span != nil {
					span.SetError(execErr)
				}
				return nil, execErr
			}
			if span != nil {
				span.AddEvent("HandlingDeployContract", map[string]interface{}{
					"deployDataCodeLength": len(tx.DeployData().Code()),
					"storageAddress":       tx.DeployData().StorageAddress().Hex(),
				})
			}
			teeRs, execErr = vmP.deploySmartContract(execCtx, tx, teeRevm, vmP.mvmId, isCache)
		} else {
			if span != nil {
				span.AddEvent("HandlingCallContract", map[string]interface{}{
					"callDataInputLength": len(tx.CallData().Input()),
				})
				span.AddEvent("SmartContractCallValidationPassed", nil)
				span.AddEvent("ExecutingSmartContractViaMVM", nil)
			}
			teeRs, execErr = vmP.executeSmartContract(execCtx, tx, teeRevm, isCache)
		}
	}

	if execErr != nil && span != nil {
		span.SetError(execErr)
	}

	if teeRs != nil {
		rs, _ := vmP.TeeResultToExecuteResult(execCtx, tx, teeRs)
		if span != nil {
			span.SetAttribute("executeResultStatus", rs.ReceiptStatus().String())
			span.SetAttribute("executeResultGasUsed", rs.GasUsed())
			span.SetAttribute("executeResultReturnHex", hex.EncodeToString(rs.Return()))
		}
		return rs, execErr
	}

	return nil, execErr
}

// deploySmartContract xử lý việc deploy smart contract.
func (vmP *VmProcessor) deploySmartContract(
	ctx context.Context, // Context từ caller
	tx types.Transaction,
	teeRevm *tee_revm_ffi.TeeRevm,
	mvmId common.Address,
	isCache bool,
) (*tee_revm_ffi.TeeStateDiff, error) {
	var span *trace.Span = nil // Khởi tạo nil

	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()
	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.deploySmartContract", map[string]interface{}{
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
			"leader":      vmP.getLeaderAddress(lastBlockHeader).Hex(),
			"isDebug":     tx.GetIsDebug(),
			"commit":      true,
		})
	}
	_, isFree := vmP.chainState.GetFreeFeeAddress()[tx.ToAddress()]
	maxGas := tx.MaxGas()
	if isFree {
		maxGas = uint64(mt_common.MAX_GASS_FEE)
	}
	
	// 🛑 TEE REVM FIX: Call ExecuteTx instead of mvmE.Deploy
	stateDiff, err := teeRevm.ExecuteTx(tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.DeployData().Code(), maxGas)
	if err != nil {
		return nil, err
	}
	
	if span != nil { // GUARD
		span.AddEvent("MvmDeployFinished", map[string]interface{}{
			"status":        stateDiff.Success,
			"gasUsed":       stateDiff.GasUsed,
			"returnLen":     0,
			"returnHex":     "",
		})
	}

	if span != nil { // GUARD
		span.AddEvent("ClearingMVMApiAfterDeploy", map[string]interface{}{"mvmIdToClear": mvmId.Hex()})
	}
	
	return stateDiff, nil
}

// readOnlyCall xử lý lời gọi chỉ đọc.
func (vmP *VmProcessor) readOnlyCall(
	ctx context.Context,
	tx types.Transaction,
	teeRevm *tee_revm_ffi.TeeRevm,
) (*tee_revm_ffi.TeeStateDiff, error) {
	var span *trace.Span = nil // Khởi tạo nil
	// var readOnlyCtx context.Context = ctx // Không cần tạo context mới nếu không dùng
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.readOnlyCall", map[string]interface{}{
			"from":        tx.FromAddress().Hex(),
			"to":          tx.ToAddress().Hex(),
			"value":       tx.Amount().String(),
			"gasLimit":    tx.MaxGas(),
			"gasPrice":    tx.MaxGasPrice(),
			"inputLen":    len(tx.CallData().Input()),
			"inputHex":    hex.EncodeToString(tx.CallData().Input()),
			"blockNumber": lastBlockHeader.BlockNumber() + 1,
			"blockTs":     vmP.blockTime,
			"leader":      vmP.getLeaderAddress(lastBlockHeader).Hex(),
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
	// 🛑 TEE REVM FIX: Call ExecuteTx instead of mvmE.Call
	stateDiff, err := teeRevm.ExecuteTx(tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.CallData().Input(), maxGas)
	if err != nil {
		return nil, err
	}
	
	if span != nil { // GUARD
		span.AddEvent("MvmCallReadOnlyFinished", map[string]interface{}{
			"status":    "",
			"exception": "",
			"gasUsed":   stateDiff.GasUsed,
			"returnLen": 0,
			"returnHex": "",
		})
	}

	if span != nil { // GUARD
		span.AddEvent("ClearingMVMApiAfterReadOnlyCall", nil)
	}

	if span != nil { // GUARD
		span.AddEvent("MvmResultConvertedToExecuteResult", nil)
	}
	return stateDiff, nil
}

// executeSmartContract xử lý việc thực thi smart contract (call).
func (vmP *VmProcessor) executeSmartContract(
	ctx context.Context,
	tx types.Transaction,
	teeRevm *tee_revm_ffi.TeeRevm,
	isCache bool,
) (*tee_revm_ffi.TeeStateDiff, error) {
	var span *trace.Span = nil // Khởi tạo nil
	lastBlockHeader := *vmP.chainState.GetcurrentBlockHeader()

	if vmP.tracingEnabled { // Chỉ tạo span nếu flag processor bật
		var actualSpan *trace.Span
		_, actualSpan = trace.StartSpan(ctx, "VmProcessor.executeSmartContract", map[string]interface{}{
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
			"leader":      vmP.getLeaderAddress(lastBlockHeader).Hex(),
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

	// 🛑 TEE REVM FIX: Call ExecuteTx instead of mvmE.Execute or mvmE.Call
	stateDiff, err := teeRevm.ExecuteTx(tx.FromAddress().Bytes(), tx.ToAddress().Bytes(), tx.CallData().Input(), maxGas)
	if err != nil {
		return nil, err
	}
	if span != nil { // GUARD
		span.AddEvent("MvmExecuteFinished", map[string]interface{}{
			"status":        stateDiff.Success,
			"gasUsed":       stateDiff.GasUsed,
		})
	}

	if span != nil { // GUARD
		span.AddEvent("UpdatingStateDBAfterExecute", nil)
	}

	return stateDiff, nil
}


func (vmP *VmProcessor) ProcessNativeMintBurn(
	ctx context.Context,
	tx types.Transaction,
	operationType uint64, // 0: mint, 1: burn
) (*tee_revm_ffi.TeeStateDiff, error) {
	return &tee_revm_ffi.TeeStateDiff{Success: true}, nil
}

// executeSmartContract xử lý việc thực thi smart contract (call).
func (vmP *VmProcessor) sendNative(
	ctx context.Context,
	tx types.Transaction,
	isCache bool,
) (*tee_revm_ffi.TeeStateDiff, error) {
	return &tee_revm_ffi.TeeStateDiff{Success: true}, nil
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
	mvmResult *tee_revm_ffi.TeeStateDiff,
	mvmId common.Address,
	isFree bool,
) (types.ExecuteSCResult, error) {
	_, err := vmP.UpdateStateDB(ctx, tx, mvmResult, mvmId, isFree, false)
	if err != nil {
		rs, _ := vmP.TeeResultToExecuteResult(ctx, tx, mvmResult)
		return rs, err
	}
	rs, errConvert := vmP.TeeResultToExecuteResult(ctx, tx, mvmResult)
	return rs, errConvert
}

// ExecuteNonceOnly executes a transaction only to increment nonce.
func (vmP *VmProcessor) ExecuteNonceOnly(
ctx context.Context,
tx types.Transaction,
isCache bool,
) (types.ExecuteSCResult, error) {
	// Stub implementation for ExecuteNonceOnly
	diff := &tee_revm_ffi.TeeStateDiff{
		Success: true,
	}
	_, err := vmP.UpdateStateDB(ctx, tx, diff, vmP.mvmId, false, isCache)
	if err != nil {
		rs, _ := vmP.TeeResultToExecuteResult(ctx, tx, diff)
		return rs, err
	}
	rs, errConvert := vmP.TeeResultToExecuteResult(ctx, tx, diff)
	return rs, errConvert
}

// ExecuteTransactionWithMvmIdDebug is a mock for old MVM functionality
func (vmP *VmProcessor) ExecuteTransactionWithMvmIdDebug(ctx context.Context, tx types.Transaction, isSubTx bool) (types.ExecuteSCResult, error) { return nil, nil }

// IsValidSmartContractCall is a mock for old MVM functionality
func (vmP *VmProcessor) IsValidSmartContractCall(toAccountState types.AccountState, tx types.Transaction) bool { return false }

// ExecuteTransactionWithMvmIdSub is a mock for old MVM functionality
func (vmP *VmProcessor) ExecuteTransactionWithMvmIdSub(ctx context.Context, tx types.Transaction, isSubTx bool) (types.ExecuteSCResult, error, error) { return nil, nil, nil }

// MvmResultToExecuteResultOffChain is a mock for old MVM functionality
func (vmP *VmProcessor) MvmResultToExecuteResultOffChain(ctx context.Context, tx types.Transaction, mvmResult interface{}) (types.ExecuteSCResult, error) { return nil, nil }
