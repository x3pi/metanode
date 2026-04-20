package tx_models

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// ChatTxOptions allows callers to override defaults when sending transactions.
type TxOptions struct {
	Amount      *big.Int
	Related     []common.Address
	MaxGas      uint64
	MaxGasPrice uint64
	MaxTimeUse  uint64
}
