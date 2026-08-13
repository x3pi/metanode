package tx_processor

import (
	"github.com/ethereum/go-ethereum/common"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/types"
)

func createErrorReceipt(tx types.Transaction, toAddress common.Address, err error) types.Receipt {
	return receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), 0, []types.EventLog{}, 0, common.Hash{}, 0,
	)
}
