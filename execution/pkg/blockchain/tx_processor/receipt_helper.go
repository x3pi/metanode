package tx_processor

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/types"
)

func HandleRevertedTransaction(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction, toAddress common.Address,
	blockTime uint64, enableTrace bool, revertReason string,
	parallel bool,
) (types.Receipt, types.ExecuteSCResult, bool) {
	// 1. Mã hóa lý do revert
	revertData := utils.EncodeRevertReason(revertReason)
	// 2. Tạo một receipt lỗi cơ bản
	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, revertData, pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
		nil, 0, common.Hash{}, 0,
	)
	// 3. Tăng nonce và cập nhật các thông tin tài khoản khác
	// Đây là phần code được tái sử dụng
	finalMvmId := tx.ToAddress()
	if parallel {
		finalMvmId = mvm.GenerateUniqueMvmId()
	}
	vmP := vm_processor.NewVmProcessor(chainState, finalMvmId, enableTrace, blockTime)
	exRs, err := vmP.ExecuteNonceOnly(ctx, tx, !parallel)
	if err != nil {
		errorReceipt := createErrorReceipt(tx, toAddress, fmt.Errorf("ExecuteNonceOnly failed during revert: %w", err))
		if exRs != nil {
			errorReceipt.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		}
		logger.Error("ExecuteNonceOnly failed for a reverted tx", "txHash", tx.Hash().Hex(), "error", err)
		return errorReceipt, exRs, true // hasFailed = true
	}

	// 4. Cập nhật receipt lỗi với kết quả từ ExecuteNonceOnly (quan trọng nhất là GasUsed)
	// Lưu ý: Chúng ta ghi đè lại Status và ReturnData để đảm bảo nó là lỗi
	rcp.UpdateExecuteResult(pb.RECEIPT_STATUS_TRANSACTION_ERROR, revertData, exRs.Exception(), exRs.GasUsed(), []types.EventLog{})

	// 5. Cập nhật lastHash và newDeviceKey
	chainState.GetAccountStateDB().SetLastHash(tx.FromAddress(), tx.Hash())
	chainState.GetAccountStateDB().SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
	return rcp, exRs, true
}

func HandleSuccessTransaction(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction, toAddress common.Address,
	blockTime uint64, enableTrace bool, eventLogs []types.EventLog, returnData []byte,
	parallel bool,
) (types.Receipt, types.ExecuteSCResult, bool) {
	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED, returnData, pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
		eventLogs, 0, common.Hash{}, 0,
	)
	finalMvmId := tx.ToAddress()
	if parallel {
		finalMvmId = mvm.GenerateUniqueMvmId()
	}
	vmP := vm_processor.NewVmProcessor(chainState, finalMvmId, enableTrace, blockTime)
	exRs, err := vmP.ExecuteNonceOnly(ctx, tx, !parallel)

	if err != nil {
		rcp := createErrorReceipt(tx, toAddress, err)
		if exRs != nil {
			rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		}
		logger.Error("ExecuteNonceOnly thất bại cho tx %s: %v", tx.Hash().Hex(), err)
		return rcp, exRs, true
	}
	rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), eventLogs)
	chainState.GetAccountStateDB().SetLastHash(tx.FromAddress(), tx.Hash())
	chainState.GetAccountStateDB().SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
	return rcp, exRs, false // hasFailed = false
}
