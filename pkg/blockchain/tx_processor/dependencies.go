package tx_processor

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

type OffChainProcessor interface {
	ProcessTransactionOffChain(tx types.Transaction) (types.ExecuteSCResult, error)
	ProcessTransactionOnChainWithDeviceKey(tx types.Transaction, rawNewDeviceKey []byte) error
	GetDeviceKey(hash common.Hash) (common.Hash, error)
}
