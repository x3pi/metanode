package main

// eth_tx_converter.go — Canonical EIP-2718 EthTx → MetaTx conversion.
// Shared by both TCP ingress (SendRawTransaction / SendRawTransactions)
// and RPC ingress (eth_sendRawTransaction).

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// ConvertRawEthTxToMetaTx is the canonical, unified converter for EIP-2718 raw Ethereum
// transaction envelopes into MetaNode Transactions. Shared by both TCP ingress (SendRawTransaction /
// SendRawTransactions) and RPC ingress (eth_sendRawTransaction).
//
// Pipeline:
// 1. Envelope size limit check (MaxStandardTxEnvelopeSize / MaxRawEthTxEnvelopeSize).
// 2. UnmarshalBinary into go-ethereum types.Transaction.
// 3. Strict envelope validation via ValidateEthTxEnvelope (anti-malleable, positive chainId match, no pre-EIP-155, recoverable sender).
// 4. Convert to MetaTx via NewTransactionFromEth.
// 5. EIP-4844: Persist blob sidecar into blob_store (if present) and strip sidecar before mempool.
func (app *App) ConvertRawEthTxToMetaTx(rawEth []byte) (mt_types.Transaction, *e_types.Transaction, error) {
	if len(rawEth) == 0 {
		return nil, nil, errors.New("empty raw transaction body")
	}
	if len(rawEth) > mt_transaction.MaxRawEthTxEnvelopeSize {
		return nil, nil, fmt.Errorf("transaction envelope size %d exceeds max allowed %d", len(rawEth), mt_transaction.MaxRawEthTxEnvelopeSize)
	}

	ethTx := new(types.Transaction)
	if err := ethTx.UnmarshalBinary(rawEth); err != nil {
		return nil, nil, fmt.Errorf("failed to decode Ethereum transaction: %w", err)
	}

	if ethTx.Type() != types.BlobTxType && len(rawEth) > mt_transaction.MaxStandardTxEnvelopeSize {
		return nil, nil, fmt.Errorf("standard transaction envelope size %d exceeds max allowed %d", len(rawEth), mt_transaction.MaxStandardTxEnvelopeSize)
	}

	if err := mt_transaction.ValidateEthTxEnvelope(ethTx, app.config.ChainId); err != nil {
		return nil, nil, err
	}

	metaTxIface, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build MetaTx from EthTx: %w", err)
	}
	metaTx, ok := metaTxIface.(*mt_transaction.Transaction)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected transaction type from NewTransactionFromEth")
	}

	// EIP-4844: persist sidecar to blob_store if present, then strip sidecar
	if metaTxProto, ok := metaTx.Proto().(*mt_proto.Transaction); ok && metaTxProto.Type == uint64(types.BlobTxType) && metaTxProto.Sidecar != nil {
		sidecar := metaTxProto.Sidecar
		if bs := app.chainState.GetBlobStore(); bs != nil {
			var blockNumber uint64
			if app.blockProcessor != nil && app.blockProcessor.GetLastBlock() != nil && app.blockProcessor.GetLastBlock().Header() != nil {
				blockNumber = app.blockProcessor.GetLastBlock().Header().BlockNumber() + 1
			}
			for i, vh := range metaTxProto.BlobVersionedHashes {
				if err := bs.Put(blockNumber, vh, sidecar.Commitments[i], sidecar.Proofs[i], sidecar.Blobs[i]); err != nil {
					return nil, nil, fmt.Errorf("failed to persist blob sidecar: %w", err)
				}
			}
		}
		metaTxProto.Sidecar = nil
	}

	logger.Debug("[ETH_TX_CONVERTER] Converted EthTx %s -> MetaTx %s", ethTx.Hash().Hex(), metaTx.Hash().Hex())
	return metaTx, ethTx, nil
}
