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

// PendingNonceGetter queries the next available pending nonce from the transaction pool.
type PendingNonceGetter interface {
	GetPendingNonce(addr common.Address, stateNonce uint64) uint64
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
	Recipient common.Address // User wallet address receiving sponsored gas top-up
	Amount    *big.Int       // Amount of sponsored gas coin to transfer
	ResultCh  chan error     // Channel to receive confirmation error once committed in block
	attempts  int            // Number of attempts across different sponsor wallets in the pool
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

// IPRateLimitRecord tracks daily HelpPay allocations per client IP.
type IPRateLimitRecord struct {
	DayNumber  uint64   // Unix day number (now.Unix() / 86400)
	TotalTopUp *big.Int // Total wei allocated today
}

// RecipientRateLimitRecord tracks cooldown and daily top-up frequency for a recipient address.
type RecipientRateLimitRecord struct {
	LastTopUpUnix int64 // Unix timestamp of last successful top-up
	CountToday    uint64
	DayNumber     uint64
}

// ClusterStateResponse represents public cluster state returned over Raft sync without exposing private keys.
type ClusterStateResponse struct {
	Enabled       bool           `json:"enabled"`
	MinBalance    string         `json:"min_balance"`
	ExtraAmount   string         `json:"extra_amount"`
	MaxDailyPerIP string         `json:"max_daily_per_ip"`
	Wallets       []WalletStatus `json:"wallets"`
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
