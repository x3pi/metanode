package account_handler

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	utilsPkg "github.com/meta-node-blockchain/meta-node/pkg/utils"
)

// SendOwnerTransfer đẩy transfer request vào owner queue (tuần tự).
// Exported để các package ngoài (vd: tcp_server handler) có thể inject vào callback.
func (h *AccountHandlerNoReceipt) SendOwnerTransfer(
	fromAddress, toAddress ethCommon.Address,
	amount *big.Int,
) *OwnerTxResult {
	resultCh := make(chan *OwnerTxResult, 1)
	h.ownerTxQueue <- &OwnerTxRequest{
		FromAddress: fromAddress,
		ToAddress:   toAddress,
		Amount:      amount,
		ResultCh:    resultCh,
	}
	return <-resultCh
}

// processOwnerTxQueue xử lý tuần tự các transaction từ ví owner.
func (h *AccountHandlerNoReceipt) processOwnerTxQueue() {
	logger.Info("🚀 Owner TX Queue worker started")
	for req := range h.ownerTxQueue {
		result := h.executeOwnerTransfer(req)
		req.ResultCh <- result
	}
}

// executeOwnerTransfer thực hiện 1 transfer transaction qua TCP
func (h *AccountHandlerNoReceipt) executeOwnerTransfer(req *OwnerTxRequest) *OwnerTxResult {
	chainConn, err := h.appCtx.ChainPool.Get()
	if err != nil {
		return &OwnerTxResult{Err: fmt.Errorf("get chain connection error: %w", err)}
	}

	metaTxData, _, releaseFunc, err := h.appCtx.ClientRpc.BuildTransferTransactionTCP(
		req.FromAddress, req.ToAddress, req.Amount, chainConn,
	)
	if err != nil {
		return &OwnerTxResult{Err: fmt.Errorf("failed to build transfer transaction: %w", err)}
	}

	txBLS, err := chainConn.SendTransactionWithDeviceKey(metaTxData, 120*time.Second)
	if releaseFunc != nil {
		releaseFunc()
	}
	if err != nil {
		return &OwnerTxResult{Err: fmt.Errorf("TCP send transfer error: %w", err)}
	}

	txHash := "0x" + hex.EncodeToString(txBLS)
	logger.Info("✅ Owner transfer sent via TCP, tx hash: %s", txHash)

	_, err = utilsPkg.WaitForReceiptTCP(chainConn, txHash, 30*time.Second)
	if err != nil {
		logger.Error("Wait for owner transfer receipt error: %v", err)
	}

	return &OwnerTxResult{TxHash: txHash}
}
