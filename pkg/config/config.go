package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"

	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/pathdetector"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// NodeType định nghĩa các loại node lưu trữ.
type NodeType string

const (
	// STORAGE_REMOTE chỉ định rằng node sử dụng lưu trữ từ xa.
	STORAGE_REMOTE NodeType = "STORAGE_REMOTE"
	STORAGE_CLIENT NodeType = "STORAGE_CLIENT"

	// STORAGE_LOCAL chỉ định rằng node sử dụng lưu trữ cục bộ.
	STORAGE_LOCAL NodeType = "STORAGE_LOCAL"
)

var (
	ConfigApp  *SimpleChainConfig
	loadConfig sync.Once
)

// DBDetail định nghĩa cấu trúc cho một database cụ thể, bao gồm đường dẫn và địa chỉ lắng nghe.
type DBDetail struct {
	Path          string `json:"Path"`
	ListenAddress string `json:"ListenAddress"` // Đã xóa omitempty, trường này là bắt buộc
	DBEngine      string `json:"DBEngine,omitempty"`
}

// DatabasesConfig định nghĩa cấu trúc cho đối tượng "Databases" trong file JSON.
type DatabasesConfig struct {
	RootPath               string   `json:"RootPath"`
	NodeType               NodeType `json:"NodeType"`
	Version                string   `json:"Version"`
	BLSPrivateKey          string   `json:"BLSPrivateKey"`
	SnapshotPath           string   `json:"SnapshotPath"`
	MaxPartSizeMB          int      `json:"MaxPartSizeMB"`
	ArchiveBaseName        string   `json:"ArchiveBaseName"`
	AccountState           DBDetail `json:"AccountState"`
	Trie                   DBDetail `json:"Trie"`
	SmartContractCode      DBDetail `json:"SmartContractCode"`
	SmartContractStorage   DBDetail `json:"SmartContractStorage"`
	Blocks                 DBDetail `json:"Blocks"`
	Receipts               DBDetail `json:"Receipts"`
	TxsEth                 DBDetail `json:"TxsEth"`
	BlocksHash             DBDetail `json:"BlocksHash"`
	BackupDeviceKey        DBDetail `json:"BackupDeviceKey"`
	TransactionBlockNumber DBDetail `json:"TransactionBlockNumber"`
	TransactionState       DBDetail `json:"TransactionState"`
	BlockHashToNumber      DBDetail `json:"BlockHashToNumber"`
	Wallets                DBDetail `json:"Wallets"`
	Mapping                DBDetail `json:"Mapping"`
	Backup                 DBDetail `json:"Backup"`
	Stake                  DBDetail `json:"Stake"`
	// XapianPath: sub-path relative to RootPath for Xapian full-text search databases.
	// Combined as: JoinPathIfNotURL(RootPath, XapianPath) — same pattern as other DB paths.
	// Default: "/xapian" (auto-created at startup if missing).
	XapianPath string `json:"XapianPath,omitempty"`
}

// NodesConfig khớp với cấu trúc của đối tượng "nodes" trong JSON.
type NodesConfig struct {
	MasterAddress      string   `json:"master_address"`
	ListSubAddress     []string `json:"list_sub_address"`
	NetworkSyncEnabled bool     `json:"network_sync_enabled"`
}

// CrossChainConfig chứa cấu hình cross-chain contracts
type CrossChainConfig struct {
	GatewayContract string `json:"gateway_contract"` // Contract xử lý giao dịch cross-chain (gateway)
	ConfigContract  string `json:"config_contract"`  // Contract chứa cấu hình (embassy pubkeys, chainId)
}

// PruningConfig configures the historical state pruning strategy
type PruningConfig struct {
	Mode                string `json:"mode"`                  // "archive" (keep all), "full" (prune old)
	EpochsToKeep        int    `json:"epochs_to_keep"`        // Number of target epochs to keep before pruning
	ReceiptsToKeep      int    `json:"receipts_to_keep"`      // Total transaction receipts to keep
	PruneIntervalBlocks int    `json:"prune_interval_blocks"` // Blocks between pruning ticks
}

// SimpleChainConfig là struct chính, đại diện cho toàn bộ file config JSON.
type SimpleChainConfig struct {
	Debug                   bool   `json:"debug"`
	Mode                    string `json:"mode"`
	ExplorereDbPath         string `json:"explorer_db_path"`
	ExplorereReadOnlyDbPath string `json:"explorer_read_only_db_path"`

	IsExplorer          bool `json:"is_explorer"`
	ExplorerQueueSize   int  `json:"explorer_queue_size"`
	ExplorerWorkerCount int  `json:"explorer_worker_count"`

	MiningDbPath           string `json:"mining_db_path"`
	IsMining               bool   `json:"is_mining"`
	ClientRpcUrl           string `json:"client_rpc_url"`
	RewardSenderPrivateKey string `json:"reward_sender_private_key"`
	RewardSenderAddress    string `json:"reward_sender_address"`

	ChainId                            *big.Int           `json:"chainId"`
	PrivateKey                         string             `json:"private_key"`
	Address                            string             `json:"address"`
	LogPath                            string             `json:"log_path"`
	EpochsToKeep                       *int               `json:"epochs_to_keep"`    // Số epoch giữ lại: 0=giữ tất cả (archive), nil/không set=mặc định 3, N=giữ N epoch gần nhất
	MaxCachedEpochs                    uint64             `json:"max_cached_epochs"` // Epoch boundary cache size: 0=unlimited, default 10
	BackupPath                         string             `json:"backup_path"`
	LastBlockSavePath                  string             `json:"last_block_save_path"`
	TransactionBlockNumberLastHashPath string             `json:"transaction_block_number_last_hash_path"`
	BlockHashToNumberDBRootPath        string             `json:"block_hash_to_number_db_root_path"`
	FreeFeeAddresses                   []string           `json:"free_fee_addresses"`
	ConnectionAddress                  string             `json:"connection_address"`
	DNSServerAddress                   string             `json:"dns_server_address"`
	Version                            string             `json:"version"`
	NodeType                           string             `json:"node_type"`
	ListTypeService                    string             `json:"list_type_service"`
	ServiceType                        common.ServiceType `json:"service_type"`
	RpcPort                            string             `json:"rpc_port"`
	DBType                             storage.DBType     `json:"db_type"`
	GenesisFilePath                    string             `json:"genesis_file_path"`
	Securepassword                     string             `json:"securepassword"`
	//
	PkAdminFileStorage string `json:"pk_admin_file_storage"`
	// Unix Domain Socket paths for communication with Rust MetaNode
	RustSendSocketPath        string   `json:"rust_send_socket_path"`       // Socket để Go gửi data cho Rust
	RustReceiveSocketPath     string   `json:"rust_receive_socket_path"`    // Socket để Go nhận data từ Rust
	RustTxSocketPath          string   `json:"rust_tx_socket_path"`         // UDS path cho Go Sub gửi TX đến Rust consensus (mỗi node có path riêng)
	RustConfigPath            string   `json:"rust_config_path,omitempty"`  // FFI: Path to Rust node-X.toml
	ValidatorForwardAddresses []string `json:"validator_forward_addresses"` // TCP addresses of validator Go Subs for sync-only nodes (e.g., ["192.168.1.1:4200"])
	MetaNodeRPCAddress        string   `json:"meta_node_rpc_address"`       // Address of Rust MetaNode RPC (fallback for TX)
	PeerRPCPort               int      `json:"peer_rpc_port"`               // TCP port for peer discovery (remote Rust nodes query this Go Master)
	BlsAdminStorage           string   `json:"bls_admin_storage"`
	OwnerFileStorageAddress   string   `json:"owner_file_storage_address"`

	// Cross-chain configuration
	CrossChain CrossChainConfig `json:"cross_chain"`

	// BLS Key Store: enables Master to store per-address BLS private keys
	// (previously only available in the RPC client proxy)
	MasterPassword string `json:"master_password,omitempty"`
	AppPepper      string `json:"app_pepper,omitempty"`

	// Snapshot configuration
	SnapshotEnabled     bool   `json:"snapshot_enabled"`      // Bật/tắt tự động snapshot
	SnapshotServerPort  int    `json:"snapshot_server_port"`  // Port HTTP server cho snapshot (default: 8700)
	SnapshotBlocksDelay int    `json:"snapshot_blocks_delay"` // Số blocks chờ sau epoch transition (default: 20)
	SnapshotMethod      string `json:"snapshot_method"`       // "hardlink" (default) hoặc "rsync" (safe cho Xapian)
	SnapshotSourceDir   string `json:"snapshot_source_dir"`   // Thư mục cần snapshot (mặc định = RootPath parent)

	// State trie backend: "nomt" (default, Rust NOMT), "mpt" (Merkle Patricia Trie) or "flat" (FlatStateTrie)
	// CAUTION: Changing backend requires data resync. All nodes must use the same backend.
	StateBackend string `json:"state_backend,omitempty"`

	// NOMT (Nearly Optimal Merkle Trie) configuration — only used when state_backend = "nomt"
	NomtCommitConcurrency int `json:"nomt_commit_concurrency,omitempty"` // Number of concurrent commit workers (default: 4)
	NomtPageCacheMB       int `json:"nomt_page_cache_mb,omitempty"`       // Page cache in MiB (default: 512)
	NomtLeafCacheMB       int `json:"nomt_leaf_cache_mb,omitempty"`       // Leaf cache in MiB (default: 512)

	// BLS Block Signing: Master signs blocks, Sub verifies before accepting.
	// MasterBLSPubKey: hex-encoded BLS public key of the Master node (for Sub nodes to verify signatures)
	MasterBLSPubKey string `json:"master_bls_pubkey,omitempty"`
	// SkipSignatureVerification: if true, Sub node accepts blocks without verifying signature (backward compatibility)
	SkipSignatureVerification bool `json:"skip_signature_verification,omitempty"`

	Pruning       PruningConfig   `json:"pruning,omitempty"`
	TraceEnabled  bool            `json:"trace_enabled,omitempty"`
	TraceEndpoint string          `json:"trace_endpoint,omitempty"`
	TlsCert       string          `json:"tls_cert,omitempty"`
	TlsKey        string          `json:"tls_key,omitempty"`
	Databases     DatabasesConfig `json:"Databases"`
	Nodes     NodesConfig     `json:"nodes"`
}

// joinPathIfNotURL nối path với base path chỉ khi path không phải là URL.
func JoinPathIfNotURL(basePath, path string) string {
	pathType := pathdetector.DetectPathType(path)
	if pathType == pathdetector.URL {
		return path
	}
	return filepath.Join(basePath, path)
}

// LoadConfig đọc và xử lý file cấu hình.
func LoadConfig(configPath string) (*SimpleChainConfig, error) {
	var err error
	loadConfig.Do(func() {
		ConfigApp = &SimpleChainConfig{}
		var raw []byte
		raw, err = os.ReadFile(configPath)
		if err != nil {
			err = fmt.Errorf("failed to read config file %s: %w", configPath, err)
			return
		}

		err = json.Unmarshal(raw, ConfigApp)
		if err != nil {
			err = fmt.Errorf("failed to parse config file %s: %w", configPath, err)
			return
		}

		if ConfigApp.ExplorerQueueSize == 0 {
			ConfigApp.ExplorerQueueSize = 8192
		}
		if ConfigApp.ExplorerWorkerCount == 0 {
			ConfigApp.ExplorerWorkerCount = 32
		}
		if ConfigApp.MaxCachedEpochs == 0 {
			ConfigApp.MaxCachedEpochs = 10 // Default: keep 10 epochs of boundary data
		}

		// Environment variable overrides for sensitive fields.
		// These take precedence over config.json values, allowing operators
		// to keep secrets out of configuration files.
		if v := os.Getenv("META_PRIVATE_KEY"); v != "" {
			ConfigApp.PrivateKey = v
		}
		if v := os.Getenv("META_REWARD_PRIVATE_KEY"); v != "" {
			ConfigApp.RewardSenderPrivateKey = v
		}
		if v := os.Getenv("META_SECURE_PASSWORD"); v != "" {
			ConfigApp.Securepassword = v
		}

	})
	return ConfigApp, err
}
