package receipt_helper

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/types"
)

// GetTransactionReceipt lấy receipt của transaction từ blockchain
func GetTransactionReceipt(txHash common.Hash, bd *block.BlockDatabase, storageReceipt storage.Storage) (*receipt.Receipt, error) {
	// Kiểm tra xem transaction đã có receipt chưa
	blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(txHash)
	if !ok {
		return nil, fmt.Errorf("transaction not found in any block")
	}

	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, fmt.Errorf("block hash not found for block number %d", blockNumber)
	}

	blockData, err := bd.GetBlockByHash(blockHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), storageReceipt)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt DB: %w", err)
	}

	rcp, err := rcpDb.GetReceipt(txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}

	typedRcp, ok := rcp.(*receipt.Receipt)
	if !ok {
		return nil, fmt.Errorf("failed to assert receipt type")
	}
	return typedRcp, nil
}

// ══════════════════════════════════════════════════════════════════════════
//           SHARED RECEIPT HELPERS (dùng chung cho tx_processor,
//           cross_chain_handler, và các handler precompile khác)
// ══════════════════════════════════════════════════════════════════════════

// CreateErrorReceipt tạo receipt lỗi cơ bản (không tăng nonce, không cập nhật state)
func CreateErrorReceipt(tx types.Transaction, toAddress common.Address, err error) types.Receipt {
	return receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
	)
}

// ExecuteNonceAndFinalize tăng nonce + cập nhật lastHash + newDeviceKey
// Đây là phần logic chung bắt buộc sau mỗi transaction thành công hoặc revert có tăng nonce
func ExecuteNonceAndFinalize(
	ctx context.Context, chainState *blockchain.ChainState,
	tx types.Transaction, enableTrace bool, blockTime uint64,
	parallel bool,
) (types.ExecuteSCResult, error) {
	finalMvmId := tx.ToAddress()
	if parallel {
		finalMvmId = mvm.GenerateUniqueMvmId()
	}
	vmP := vm_processor.NewVmProcessor(chainState, finalMvmId, enableTrace, blockTime)
	// Nếu chạy song song (parallel=true), ta sử dụng mvmId ngẫu nhiên và không lưu cache (isCache=false)
	// để tránh giẫm chân lên các tiến trình khác và tránh rò rỉ bộ nhớ.
	exRs, err := vmP.ExecuteNonceOnly(ctx, tx, !parallel)
	if err != nil {
		return exRs, err
	}
	chainState.GetAccountStateDB().SetLastHash(tx.FromAddress(), tx.Hash())
	chainState.GetAccountStateDB().SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
	return exRs, nil
}

// HandleRevertedTx xử lý transaction bị revert:
// tạo receipt lỗi → tăng nonce → cập nhật lastHash/deviceKey
// Returns: (receipt, executeSCResult, hasFailed=true)
func HandleRevertedTx(
	ctx context.Context, chainState *blockchain.ChainState,
	tx types.Transaction, toAddress common.Address,
	blockTime uint64, enableTrace bool, revertReason string,
	parallel bool,
) (types.Receipt, types.ExecuteSCResult, bool) {
	revertData := []byte(revertReason)

	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, revertData, pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
		nil, 0, common.Hash{}, 0,
	)

	exRs, err := ExecuteNonceAndFinalize(ctx, chainState, tx, enableTrace, blockTime, parallel)
	if err != nil {
		errorReceipt := CreateErrorReceipt(tx, toAddress, fmt.Errorf("ExecuteNonceOnly failed during revert: %w", err))
		if exRs != nil {
			errorReceipt.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		}
		logger.Error("ExecuteNonceOnly failed for reverted tx %s: %v", tx.Hash().Hex(), err)
		return errorReceipt, exRs, true
	}

	rcp.UpdateExecuteResult(pb.RECEIPT_STATUS_TRANSACTION_ERROR, revertData, exRs.Exception(), exRs.GasUsed(), []types.EventLog{})
	return rcp, exRs, true
}

// HandleSuccessTx xử lý transaction thành công:
// tạo receipt → tăng nonce → cập nhật lastHash/deviceKey
// Returns: (receipt, executeSCResult, hasFailed=false)
func HandleSuccessTx(
	ctx context.Context, chainState *blockchain.ChainState,
	tx types.Transaction, toAddress common.Address,
	blockTime uint64, enableTrace bool,
	eventLogs []types.EventLog, returnData []byte,
	parallel bool,
) (types.Receipt, types.ExecuteSCResult, bool) {
	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED, returnData, pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
		eventLogs, 0, common.Hash{}, 0,
	)
	exRs, err := ExecuteNonceAndFinalize(ctx, chainState, tx, enableTrace, blockTime, parallel)
	if err != nil {
		rcp := CreateErrorReceipt(tx, toAddress, err)
		if exRs != nil {
			rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		}
		logger.Error("ExecuteNonceOnly failed for tx %s: %v", tx.Hash().Hex(), err)
		return rcp, exRs, true
	}

	rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), eventLogs)
	return rcp, exRs, false
}

// HandleSuccessTxWithExRs xử lý transaction thành công khi đã có ExecuteSCResult (ví dụ từ burn)
// Không gọi ExecuteNonceOnly vì nonce đã được tăng bởi caller
// Returns: (receipt, executeSCResult, hasFailed=false)
func HandleSuccessTxWithExRs(
	chainState *blockchain.ChainState,
	tx types.Transaction, toAddress common.Address,
	eventLogs []types.EventLog, exRsFromCaller types.ExecuteSCResult,
) (types.Receipt, types.ExecuteSCResult, bool) {
	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
		eventLogs, 0, common.Hash{}, 0,
	)
	rcp.UpdateExecuteResult(
		exRsFromCaller.ReceiptStatus(), exRsFromCaller.Return(),
		exRsFromCaller.Exception(), exRsFromCaller.GasUsed(), eventLogs,
	)
	chainState.GetAccountStateDB().SetLastHash(tx.FromAddress(), tx.Hash())
	chainState.GetAccountStateDB().SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
	return rcp, exRsFromCaller, false
}
