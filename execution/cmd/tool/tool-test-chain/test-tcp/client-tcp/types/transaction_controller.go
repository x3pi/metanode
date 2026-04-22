package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/types"
)

type TransactionController interface {
	SendTransaction(
		fromAddress common.Address,
		toAddress common.Address,
		pendingUse *big.Int,
		amount *big.Int,
		maxGas uint64,
		maxGasFee uint64,
		maxTimeUse uint64,
		data []byte,
		relatedAddress [][]byte,
		lastDeviceKey common.Hash,
		newDeviceKey common.Hash,
		nonce uint64,
		chainId uint64,
	) (types.Transaction, error)

	ReadTransaction(
		fromAddress common.Address,
		toAddress common.Address,
		pendingUse *big.Int,
		amount *big.Int,
		maxGas uint64,
		maxGasFee uint64,
		maxTimeUse uint64,
		data []byte,
		relatedAddress [][]byte,
		lastDeviceKey common.Hash,
		newDeviceKey common.Hash,
		nonce uint64,
		chainId uint64,
	) (types.Transaction, error)

	ReadTransactionWithoutNonce(
		fromAddress common.Address,
		toAddress common.Address,
		pendingUse *big.Int,
		amount *big.Int,
		maxGas uint64,
		maxGasFee uint64,
		maxTimeUse uint64,
		data []byte,
		relatedAddress [][]byte,
		lastDeviceKey common.Hash,
		newDeviceKey common.Hash,
		chainId uint64,
	) (types.Transaction, error)

	EstimateGas(
		fromAddress common.Address,
		toAddress common.Address,
		pendingUse *big.Int,
		amount *big.Int,
		maxGas uint64,
		maxGasFee uint64,
		maxTimeUse uint64,
		data []byte,
		relatedAddress [][]byte,
		lastDeviceKey common.Hash,
		newDeviceKey common.Hash,
		chainId uint64,
	) (types.Transaction, error)

	SendTransactionWithDeviceKey(
		fromAddress common.Address,
		toAddress common.Address,
		pendingUse *big.Int,
		amount *big.Int,
		maxGas uint64,
		maxGasFee uint64,
		maxTimeUse uint64,
		data []byte,
		relatedAddress [][]byte,
		lastDeviceKey common.Hash,
		newDeviceKey common.Hash,
		nonce uint64,
		deviceKey []byte,
		chainId uint64,
	) (types.Transaction, error)

	SaveTransactionWithDeviceKeyToFile(
		fromAddress common.Address,
		toAddress common.Address,
		pendingUse *big.Int,
		amount *big.Int,
		maxGas uint64,
		maxGasFee uint64,
		maxTimeUse uint64,
		data []byte,
		relatedAddress [][]byte,
		lastDeviceKey common.Hash,
		newDeviceKey common.Hash,
		nonce uint64,
		deviceKey []byte,
		chainId uint64,
	) error
	SendAllTransactionsInDirectory(
		directoryPath string,
	) error
	SendTransactions(transactions []types.Transaction) error

	SendNewTransactionWithDeviceKey(
		tx types.Transaction,
		deviceKey []byte,
	) (types.Transaction, error)
}
