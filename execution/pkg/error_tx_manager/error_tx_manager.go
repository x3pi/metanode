package error_tx_manager

import (
	"errors" // Cần import package errors để sử dụng errors.Is
	"fmt"    // Cần import fmt để định dạng lỗi

	"github.com/ethereum/go-ethereum/common" // Import để sử dụng common.Hash
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage" // Import storage
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types" // Import để sử dụng types.Transaction
)

const (
	// rpcErrorPrefixString là tiền tố cho các key lỗi liên quan đến RPC.
	rcpErrorPrefixString = "error_rcp_"
	// txProcessingErrorPrefixString là tiền tố cho các key lỗi liên quan đến xử lý giao dịch.
	txProcessingErrorPrefixString = "error_tx_"
)

type ErrorTxManager struct {
	storage storage.Storage
}

func NewErrorTxManager(s storage.Storage) *ErrorTxManager {

	return &ErrorTxManager{
		storage: s,
	}
}

func (m *ErrorTxManager) generateKey(prefix []byte, txHash common.Hash) []byte {
	// Tạo một slice mới đủ lớn để chứa prefix và hash
	key := make([]byte, 0, len(prefix)+len(txHash.Bytes()))
	key = append(key, prefix...)
	key = append(key, txHash.Bytes()...)
	return key
}

func (m *ErrorTxManager) SetTransactionError(tx types.Transaction) error {
	if tx == nil {
		return errors.New("transaction cannot be nil")
	}
	key := m.generateKey([]byte(txProcessingErrorPrefixString), tx.Hash())
	value, err := tx.Marshal()
	if err != nil {
		return err
	}
	putErr := m.storage.Put(key, value)
	if putErr != nil {
		return fmt.Errorf("failed to set error info in storage for prefix %s: %w", tx.Hash(), putErr)
	}
	return nil
}

func (m *ErrorTxManager) GetTransactionError(txHash common.Hash) (types.Transaction, error) {
	key := m.generateKey([]byte(txProcessingErrorPrefixString), txHash)
	value, getErr := m.storage.Get(key)
	if getErr != nil {
		return nil, getErr
	}
	txM := &transaction.Transaction{}
	err := txM.Unmarshal(value)
	if err != nil {
		return nil, err
	}
	return txM, nil
}

// --- Các hàm Get/Set cụ thể cho RPC và TX ---

// SetRPCErrorTransaction marks a transaction as error in the RPC context.
func (m *ErrorTxManager) SetRCPErrorTransaction(rcp types.Receipt) error {
	if rcp == nil {
		return errors.New("transaction cannot be nil")
	}
	key := m.generateKey([]byte(rcpErrorPrefixString), rcp.TransactionHash())
	value, err := rcp.Marshal()
	if err != nil {
		return err
	}
	putErr := m.storage.Put(key, value)
	if putErr != nil {
		return fmt.Errorf("failed to set error info in storage for prefix %s: %w", rcp.TransactionHash(), putErr)
	}
	return nil
}

// GetRPCErrorTransactionInfo checks if a transaction is marked as error in the RPC context
// and retrieves the error message.
func (m *ErrorTxManager) GetRCPTransactionError(txHash common.Hash) (rcp types.Receipt, err error) {
	key := m.generateKey([]byte(rcpErrorPrefixString), txHash)
	value, getErr := m.storage.Get(key)

	if getErr != nil {
		return nil, getErr
	}
	receipt := &receipt.Receipt{} // Tạo một con trỏ đến Receipt rỗng

	err = receipt.Unmarshal(value)
	if err != nil {
		return nil, err
	}
	logger.Info("GetRCPTransactionError: ", receipt)
	return receipt, nil
}
