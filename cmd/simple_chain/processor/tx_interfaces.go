package processor

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/txsender"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// TxHashConnEntry stores connection metadata allowing the RPC client
// to match an asynchronously delivered receipt to its pending request.
type TxHashConnEntry struct {
	Conn      network.Connection
	MsgID     string
	CreatedAt time.Time
}

// IReceiptBroadcaster abstracts the BlockProcessor's capability to send receipts.
type IReceiptBroadcaster interface {
	BroadCastReceipts(receipts []types.Receipt)
}

// IConnectionManager abstracts the BlockProcessor's network connections manager.
type IConnectionManager interface {
	ConnectionByTypeAndAddress(connType int, addr common.Address) network.Connection
	ConnectionsByType(connType int) map[common.Address]network.Connection
}

// ITxHashConnMapper abstracts the mapping of transaction hashes to network connections.
type ITxHashConnMapper interface {
	StoreTxHashConnEntry(txHash common.Hash, entry TxHashConnEntry)
}

// ISystemConfig abstracts the system configuration context provided by BlockProcessor.
type ISystemConfig interface {
	GetRustTxSocketPath() string
}

// ITxClientProvider abstracts the UDS client provider used to forward transactions to Rust.
type ITxClientProvider interface {
	GetTxClient() *txsender.Client
}

// IBlockContext abstracts the chain context required by the transaction processor.
type IBlockContext interface {
	GetLastBlock() types.Block
}

// ITransactionProcessorEnvironment serves as the Dependency Injection boundary
// for TransactionProcessor. It replaces the concrete *BlockProcessor dependency,
// severing the circular dependency between the two God Objects.
type ITransactionProcessorEnvironment interface {
	IReceiptBroadcaster
	IConnectionManager
	ITxHashConnMapper
	ISystemConfig
	ITxClientProvider
	IBlockContext
}
