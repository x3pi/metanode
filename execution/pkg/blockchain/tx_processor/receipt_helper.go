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
	"github.com/meta-node-blockchain/meta-node/types"
)

func HandleRevertedTransaction(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction, toAddress common.Address, mvmId common.Address,
	blockTime uint64, enableTrace bool, revertReason string,
) (types.Receipt, types.ExecuteSCResult, bool) {
	// 1. Mã hóa lý do revert
	revertData := utils.EncodeRevertReason(revertReason)
	// 2. Tạo một receipt lỗi cơ bản
	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, revertData, pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), mt_common.TRANSFER_GAS_COST,
		nil, 0, common.Hash{}, 0,
	)
	// 3. Tăng nonce và cập nhật các thông tin tài khoản khác
	// Đây là phần code được tái sử dụng
	vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime, common.Address{})
	exRs, err := vmP.ExecuteNonceOnly(ctx, tx, true)
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
	// 6. Consume the sender's nonce. ExecuteNonceOnly's own UpdateStateDB deliberately
	// SKIPS the sender's own address (vm_processor_state.go's "NONCE-FIX": regular
	// parallel-EVM transactions already have their nonce bumped beforehand via
	// mvccDB.PlusOneNonce in true_block_stm.go's runParallelSegment, so applying MVM's
	// returned nonce there too would double-increment). That assumption does not hold
	// here: HandleRevertedTransaction/HandleSuccessTransaction are called ONLY from the
	// barrier-tx path (runBarrierTx, via ValidatorHandler/GatewayHandler.HandleTransaction
	// -- confirmed by grep, no other caller exists), which never does any such
	// pre-increment. Without this, a barrier tx's sender nonce silently never advances at
	// all: found live 2026-08-26 -- a relayer's SECOND gateway transaction (nonce N+1)
	// stayed stuck as a "future" nonce forever because the FIRST one (nonce N) never
	// actually incremented the account's real nonce despite its own receipt reporting
	// success, permanently halting further block production on the chain (not just that
	// one transfer) since the executor's tx-pool never found another valid transaction to
	// build the next block from. Matches true_block_stm.go's own "always update nonce even
	// if [the rest of] the tx fails" rule for the regular path -- a nonce is consumed by
	// attempting a transaction, not by it succeeding.
	if err := chainState.GetAccountStateDB().SetNonce(tx.FromAddress(), tx.GetNonce()+1); err != nil {
		logger.Error("HandleRevertedTransaction: failed to consume sender nonce for %s: %v", tx.Hash().Hex(), err)
	}
	return rcp, exRs, true
}

func HandleSuccessTransaction(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction, toAddress common.Address, mvmId common.Address,
	blockTime uint64, enableTrace bool, eventLogs []types.EventLog, returnData []byte,
) (types.Receipt, types.ExecuteSCResult, bool) {
	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED, returnData, pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), mt_common.TRANSFER_GAS_COST,
		eventLogs, 0, common.Hash{}, 0,
	)
	vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime, common.Address{})
	exRs, err := vmP.ExecuteNonceOnly(ctx, tx, true)

	if err != nil {
		rcp := createErrorReceipt(tx, toAddress, err)
		if exRs != nil {
			rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		}
		logger.Error("ExecuteNonceOnly thất bại cho tx %s: %v", tx.Hash().Hex(), err)
		return rcp, exRs, true
	}
	ret := exRs.Return()
	if len(ret) == 0 && len(returnData) > 0 {
		ret = returnData
	}
	rcp.UpdateExecuteResult(exRs.ReceiptStatus(), ret, exRs.Exception(), exRs.GasUsed(), eventLogs)
	chainState.GetAccountStateDB().SetLastHash(tx.FromAddress(), tx.Hash())
	chainState.GetAccountStateDB().SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
	// Consume the sender's nonce -- see HandleRevertedTransaction's matching comment above
	// for the full root-cause explanation. Without this, a barrier tx's sender nonce never
	// advances at all, and a second gateway transaction from the same sender gets stuck as
	// a permanently-unfulfillable "future" nonce, halting all further block production.
	if err := chainState.GetAccountStateDB().SetNonce(tx.FromAddress(), tx.GetNonce()+1); err != nil {
		logger.Error("HandleSuccessTransaction: failed to consume sender nonce for %s: %v", tx.Hash().Hex(), err)
	}
	return rcp, exRs, false // hasFailed = false
}
