package helppay

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// TxPoolAdder is the interface implemented by TransactionProcessor
// to admit transactions directly into the node's mempool.
type TxPoolAdder interface {
	AddTransactionToPool(tx mt_types.Transaction) (int64, error)
}

// WalletWorker represents an active worker goroutine for a single sponsor wallet.
type WalletWorker struct {
	address    common.Address
	privKey    *ecdsa.PrivateKey
	privKeyHex string
	cancel     context.CancelFunc
	totalSent  atomic.Uint64
	lastUsed   atomic.Int64 // unix timestamp
}

// SponsorGasTask represents an in-flight gas sponsorship job assigned to a sponsor wallet worker.
type SponsorGasTask struct {
	Recipient common.Address // Địa chỉ ví người dùng nhận bảo trợ gas
	Amount    *big.Int       // Số lượng coin bảo trợ
	ResultCh  chan error     // Channel nhận kết quả sau khi giao dịch commit vào block
	attempts  int            // Số lần đã thử qua các ví bảo trợ khác nhau trong pool
}

// WalletStatus is the JSON representation returned to RPC callers.
type WalletStatus struct {
	Address   string `json:"address"`
	Balance   string `json:"balance"`
	IsActive  bool   `json:"is_active"`
	TotalSent uint64 `json:"total_sent"`
	LastUsed  string `json:"last_used"`
}

// SyncAction defines the type of cluster sync action.
type SyncAction string

const (
	ActionAddWallet    SyncAction = "add_wallet"
	ActionRemoveWallet SyncAction = "remove_wallet"
	ActionSetConfig    SyncAction = "set_config"
)

// SyncPayload is exchanged between Raft peers over the authenticated internal channel.
type SyncPayload struct {
	Action       SyncAction `json:"action"`
	SenderNodeID string     `json:"sender_node_id,omitempty"`
	PrivateKey   string     `json:"private_key,omitempty"`
	Address      string     `json:"address,omitempty"`
	Enabled      *bool      `json:"enabled,omitempty"`
	MinBalance   string     `json:"min_balance,omitempty"`
	ExtraAmount  string     `json:"extra_amount,omitempty"`
}

// PersistedConfig represents the JSON file structure saved on disk.
type PersistedConfig struct {
	Enabled     bool                  `json:"enabled"`
	MinBalance  string                `json:"min_balance"`
	ExtraAmount string                `json:"extra_amount"`
	Wallets     []PersistedWalletItem `json:"wallets"`
}

// PersistedWalletItem stores one wallet entry in the persistence file.
type PersistedWalletItem struct {
	Address    string `json:"address"`
	PrivateKey string `json:"private_key"`
	TotalSent  uint64 `json:"total_sent,omitempty"`
}
