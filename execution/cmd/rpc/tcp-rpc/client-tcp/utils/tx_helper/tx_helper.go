package tx_helper

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	clientpkg "github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp"
	com_pkg "github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp/common"
	c_config "github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/tcp-rpc/pkg/models/tx_models"
	"github.com/meta-node-blockchain/meta-node/types"
)

func NormalizeTxOptions(opts *tx_models.TxOptions) tx_models.TxOptions {
	if opts == nil {
		return tx_models.TxOptions{}
	}
	normalized := tx_models.TxOptions{
		Amount:      opts.Amount,
		MaxGas:      opts.MaxGas,
		MaxGasPrice: opts.MaxGasPrice,
		MaxTimeUse:  opts.MaxTimeUse,
	}
	if len(opts.Related) > 0 {
		normalized.Related = append([]common.Address(nil), opts.Related...)
	}
	return normalized
}

func SendReadTransactionWithoutNonce(
	action string,
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	contract common.Address,
	from common.Address,
	input []byte,
	opts *tx_models.TxOptions,
) (types.Receipt, error) {
	if cli == nil || cfg == nil {
		return nil, fmt.Errorf("client and config are required")
	}
	if (contract == common.Address{}) {
		return nil, fmt.Errorf("contract address is required")
	}
	if (from == common.Address{}) {
		return nil, fmt.Errorf("from address is required")
	}

	normalized := NormalizeTxOptions(opts)
	amount := normalized.Amount
	if amount == nil {
		amount = big.NewInt(0)
	}

	related := make([]common.Address, 0, len(normalized.Related)+1)
	if len(normalized.Related) > 0 {
		related = append(related, normalized.Related...)
	}
	related = append(related, cfg.Address())

	maxGas := ChooseOrDefault(normalized.MaxGas, com_pkg.DefaultMaxGas)
	maxGasPrice := ChooseOrDefault(normalized.MaxGasPrice, com_pkg.DefaultMaxGasPrice)
	maxTimeUse := ChooseOrDefault(normalized.MaxTimeUse, com_pkg.DefaultMaxExecution)

	callData := mt_transaction.NewCallData(input)
	payload, err := callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal calldata for %s: %w", action, err)
	}
	// Gửi read transaction
	receipt, err := cli.ReadTransactionWithoutNonce(
		from,
		contract,
		amount,
		payload,
		related,
		maxGas,
		maxGasPrice,
		maxTimeUse,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to send %s read transaction: %w", action, err)
	}
	if receipt == nil {
		return nil, fmt.Errorf("%s read transaction returned empty receipt", action)
	}
	status := receipt.Status()
	if status != pb.RECEIPT_STATUS_RETURNED && status != pb.RECEIPT_STATUS_HALTED {
		return nil, fmt.Errorf("%s read transaction failed with status %s returned %s", action, status.String(), receipt.Return())
	}
	logger.Info("✅ %s completed (status=%s)", action, status.String())
	return receipt, nil
}

func SendEstimateGas(
	action string,
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	contract common.Address,
	from common.Address,
	input []byte,
	opts *tx_models.TxOptions,
) (types.Receipt, error) {
	if cli == nil || cfg == nil {
		return nil, fmt.Errorf("client and config are required")
	}
	if (contract == common.Address{}) {
		return nil, fmt.Errorf("contract address is required")
	}
	if (from == common.Address{}) {
		return nil, fmt.Errorf("from address is required")
	}

	normalized := NormalizeTxOptions(opts)
	amount := normalized.Amount
	if amount == nil {
		amount = big.NewInt(0)
	}

	related := make([]common.Address, 0, len(normalized.Related)+1)
	if len(normalized.Related) > 0 {
		related = append(related, normalized.Related...)
	}
	related = append(related, cfg.Address())

	maxGas := ChooseOrDefault(normalized.MaxGas, com_pkg.DefaultMaxGas)
	maxGasPrice := ChooseOrDefault(normalized.MaxGasPrice, com_pkg.DefaultMaxGasPrice)
	maxTimeUse := ChooseOrDefault(normalized.MaxTimeUse, com_pkg.DefaultMaxExecution)

	callData := mt_transaction.NewCallData(input)
	payload, err := callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal calldata for %s: %w", action, err)
	}
	// Gửi estimate gas transaction
	receipt, err := cli.EstimateGas(
		from,
		contract,
		amount,
		payload,
		related,
		maxGas,
		maxGasPrice,
		maxTimeUse,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to send %s estimate gas transaction: %w", action, err)
	}
	if receipt == nil {
		return nil, fmt.Errorf("%s estimate gas transaction returned empty receipt", action)
	}
	status := receipt.Status()
	if status != pb.RECEIPT_STATUS_RETURNED && status != pb.RECEIPT_STATUS_HALTED {
		return nil, fmt.Errorf("%s estimate gas transaction failed with status %s returned %s", action, status.String(), receipt.Return())
	}
	logger.Info("✅ %s completed (status=%s)", action, status.String())
	return receipt, nil
}

func SendReadTransaction(
	action string,
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	contract common.Address,
	from common.Address,
	input []byte,
	opts *tx_models.TxOptions,
) (types.Receipt, error) {
	if cli == nil || cfg == nil {
		return nil, fmt.Errorf("client and config are required")
	}
	if (contract == common.Address{}) {
		return nil, fmt.Errorf("contract address is required")
	}
	if (from == common.Address{}) {
		return nil, fmt.Errorf("from address is required")
	}

	normalized := NormalizeTxOptions(opts)
	amount := normalized.Amount
	if amount == nil {
		amount = big.NewInt(0)
	}

	related := make([]common.Address, 0, len(normalized.Related)+1)
	if len(normalized.Related) > 0 {
		related = append(related, normalized.Related...)
	}
	related = append(related, cfg.Address())

	maxGas := ChooseOrDefault(normalized.MaxGas, com_pkg.DefaultMaxGas)
	maxGasPrice := ChooseOrDefault(normalized.MaxGasPrice, com_pkg.DefaultMaxGasPrice)
	maxTimeUse := ChooseOrDefault(normalized.MaxTimeUse, com_pkg.DefaultMaxExecution)

	callData := mt_transaction.NewCallData(input)
	payload, err := callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal calldata for %s: %w", action, err)
	}
	// Gửi read transaction
	receipt, err := cli.ReadTransaction(
		from,
		contract,
		amount,
		payload,
		related,
		maxGas,
		maxGasPrice,
		maxTimeUse,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to send %s read transaction: %w", action, err)
	}
	if receipt == nil {
		return nil, fmt.Errorf("%s read transaction returned empty receipt", action)
	}
	status := receipt.Status()
	if status != pb.RECEIPT_STATUS_RETURNED && status != pb.RECEIPT_STATUS_HALTED {
		return nil, fmt.Errorf("%s read transaction failed with status %s returned %s", action, status.String(), receipt.Return())
	}
	logger.Info("✅ %s completed (status=%s)", action, status.String())
	return receipt, nil
}

func SendTransaction(
	action string,
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	contract common.Address,
	from common.Address,
	input []byte,
	opts *tx_models.TxOptions,
) (types.Receipt, error) {
	if cli == nil || cfg == nil {
		return nil, fmt.Errorf("client and config are required")
	}
	if (contract == common.Address{}) {
		return nil, fmt.Errorf("contract address is required")
	}
	if (from == common.Address{}) {
		return nil, fmt.Errorf("from address is required")
	}

	normalized := NormalizeTxOptions(opts)
	amount := normalized.Amount
	if amount == nil {
		amount = big.NewInt(0)
	}

	related := make([]common.Address, 0, len(normalized.Related)+1)
	if len(normalized.Related) > 0 {
		related = append(related, normalized.Related...)
	}
	related = append(related, cfg.Address())

	maxGas := ChooseOrDefault(normalized.MaxGas, com_pkg.DefaultMaxGas)
	maxGasPrice := ChooseOrDefault(normalized.MaxGasPrice, com_pkg.DefaultMaxGasPrice)
	maxTimeUse := ChooseOrDefault(normalized.MaxTimeUse, com_pkg.DefaultMaxExecution)

	callData := mt_transaction.NewCallData(input)
	payload, err := callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal calldata for %s: %w", action, err)
	}
	// Gửi write transaction
	receipt, err := cli.SendTransactionWithDeviceKey(
		from,
		contract,
		amount,
		payload,
		related,
		maxGas,
		maxGasPrice,
		maxTimeUse,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to send %s transaction: %w", action, err)
	}
	if receipt == nil {
		return nil, fmt.Errorf("%s transaction returned empty receipt", action)
	}
	status := receipt.Status()
	if status != pb.RECEIPT_STATUS_RETURNED && status != pb.RECEIPT_STATUS_HALTED {
		return nil, fmt.Errorf("%s transaction failed with status %s", action, status.String())
	}
	logger.Info("✅ %s completed (status=%s)", action, status.String())
	return receipt, nil
}

func ChooseOrDefault(value uint64, fallback uint64) uint64 {
	if value == 0 {
		return fallback
	}
	return value
}
