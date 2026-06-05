package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"

	"github.com/meta-node-blockchain/meta-node/pkg/pathdetector"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// Xóa cấu hình NodeType do hệ thống chỉ còn hỗ trợ lưu trữ cục bộ.

var (
	ConfigApp  *SimpleChainConfig
	loadConfig sync.Once
)

// Các hằng số đường dẫn con cho các thư mục database (tương đối so với RootPath)
const (
	PathAccountState           = "/consensus/account_state/"
	PathTrie                   = "/consensus/trie_database/"
	PathSmartContractCode      = "/consensus/smart_contract_code/"
	PathSmartContractStorage   = "/consensus/smart_contract_storage/"
	PathBlocks                 = "/history/blocks/"
	PathReceipts               = "/history/receipts/"
	PathTxsEth                 = "/history/txs_eth/"
	PathBlocksHash             = "/history/blocks_hash/"
	PathBackupDeviceKey        = "/consensus/backup_device_key_storage/"
	PathTransactionBlockNumber = "/history/transaction_block_number/"
	PathTransactionState       = "/history/transaction_state/"
	PathBlockHashToNumber      = "/history/block_hash_to_number/"
	PathWallets                = "/consensus/wallets/"
	PathMapping                = "/history/mapping/"
	PathBackup                 = "/consensus/backup_db/"
	PathStake                  = "/consensus/stake_db/"
	PathXapian                 = "/consensus/xapian"
)

// DatabasesConfig định nghĩa cấu trúc cho đối tượng "Databases" trong file JSON.
// Hiện tại chỉ cần cấu hình thư mục gốc (RootPath), các đường dẫn con được hardcode qua các hằng số.
type DatabasesConfig struct {
	RootPath               string `json:"RootPath"`
	Version                string `json:"Version"`
	BLSPrivateKey          string `json:"BLSPrivateKey"`
	SnapshotPath           string `json:"SnapshotPath"`
	MaxPartSizeMB          int    `json:"MaxPartSizeMB"`
	ArchiveBaseName        string `json:"ArchiveBaseName"`
	NumShardsDefault       int    `json:"num_shards_default,omitempty"`
	NumShardsSmartContract int    `json:"num_shards_smart_contract,omitempty"`
	NumShardsCode          int    `json:"num_shards_code,omitempty"`
	Parallelism            int    `json:"parallelism,omitempty"`
	PebbleCacheSizeMB      int    `json:"pebble_cache_size_mb,omitempty"`
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

// RpcRateLimitConfig configures IP-based rate limiting for RPC
type RpcRateLimitConfig struct {
	Enabled           bool `json:"enabled"`             // Turn on/off IP rate limiting
	GlobalRate        int  `json:"global_rate"`         // Max requests per second (across all IPs)
	PerIpRate         int  `json:"per_ip_rate"`         // Max requests per second per IP
	BlockDurationSecs int  `json:"block_duration_secs"` // Time in seconds to ban an IP if it exceeds limit
}

// LogConfig định nghĩa cấu hình cho logger (level, format, outputs).
type LogConfig struct {
	Level         string `json:"level"`          // "debug", "info", "warn", "error"
	Format        string `json:"format"`         // "text", "json"
	ConsoleOutput bool   `json:"console_output"` // output to stdout/stderr
	FileOutput    bool   `json:"file_output"`    // output to file
}

// SimpleChainConfig là struct chính, đại diện cho toàn bộ file config JSON.
type SimpleChainConfig struct {
	Debug                   bool   `json:"debug"`
	ExplorereDbPath         string `json:"explorer_db_path"`
	ExplorereReadOnlyDbPath string `json:"explorer_read_only_db_path"`

	IsExplorer          bool `json:"is_explorer"`
	IsRPCNode           bool `json:"is_rpc_node"`
	ExplorerQueueSize   int  `json:"explorer_queue_size"`
	ExplorerWorkerCount int  `json:"explorer_worker_count"`

	MiningDbPath           string `json:"mining_db_path"`
	IsMining               bool   `json:"is_mining"`
	ClientRpcUrl           string `json:"client_rpc_url"`
	RewardSenderPrivateKey string `json:"reward_sender_private_key"`
	RewardSenderAddress    string `json:"reward_sender_address"`

	ChainId                            *big.Int       `json:"chainId"`
	PrivateKey                         string         `json:"private_key"`
	Address                            string         `json:"address"`
	LogPath                            string         `json:"log_path"`
	EpochsToKeep                       *int           `json:"epochs_to_keep"`    // Số epoch giữ lại: 0=giữ tất cả (archive), nil/không set=mặc định 3, N=giữ N epoch gần nhất
	MaxCachedEpochs                    uint64         `json:"max_cached_epochs"` // Epoch boundary cache size: 0=unlimited, default 10
	BackupPath                         string         `json:"backup_path"`
	LastBlockSavePath                  string         `json:"last_block_save_path"`
	TransactionBlockNumberLastHashPath string         `json:"transaction_block_number_last_hash_path"`
	BlockHashToNumberDBRootPath        string         `json:"block_hash_to_number_db_root_path"`
	FreeFeeAddresses                   []string       `json:"free_fee_addresses"`
	ConnectionAddress                  string         `json:"connection_address"`
	DNSServerAddress                   string         `json:"dns_server_address"`
	Version                            string         `json:"version"`
	ListTypeService                    string         `json:"list_type_service"`
	ServiceType                        string         `json:"service_type"`
	RpcPort                            string         `json:"rpc_port"`
	DBType                             storage.DBType `json:"db_type"`
	GenesisFilePath                    string         `json:"genesis_file_path"`
	Securepassword                     string         `json:"securepassword"`
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
	SnapshotEnabled         bool   `json:"snapshot_enabled"`                    // Bật/tắt tự động snapshot
	SnapshotServerPort      int    `json:"snapshot_server_port"`                // Port HTTP server cho snapshot (default: 8700)
	SnapshotBlocksDelay     int    `json:"snapshot_blocks_delay"`               // Số blocks chờ sau epoch transition (default: 20)
	SnapshotMethod          string `json:"snapshot_method"`                     // "hardlink" (default) hoặc "rsync" (safe cho Xapian)
	SnapshotSourceDir       string `json:"snapshot_source_dir"`                 // Thư mục cần snapshot (mặc định = RootPath parent)
	SnapshotFrequencyBlocks int    `json:"snapshot_frequency_blocks,omitempty"` // Số blocks cố định để tạo snapshot (0 = chỉ tạo khi qua epoch mới)
	SnapshotBlockOffset     int    `json:"snapshot_block_offset,omitempty"`     // Offset per-node để stagger snapshot (vd: node0=0, node1=100, node2=200). Đảm bảo không tất cả nodes snapshot cùng lúc
	SnapshotMaxSnapshots    int    `json:"snapshot_max_snapshots,omitempty"`    // Số snapshot tối đa giữ lại (mặc định 5 hoặc 2 tùy config)

	// State trie backend: "nomt" (default, Rust NOMT), "mpt" (Merkle Patricia Trie) or "flat" (FlatStateTrie)
	// CAUTION: Changing backend requires data resync. All nodes must use the same backend.
	StateBackend string `json:"state_backend,omitempty"`

	// NOMT (Nearly Optimal Merkle Trie) configuration — only used when state_backend = "nomt"
	NomtCommitConcurrency int `json:"nomt_commit_concurrency,omitempty"` // Number of concurrent commit workers (default: 4)
	NomtPageCacheMB       int `json:"nomt_page_cache_mb,omitempty"`      // Page cache in MiB (default: 512)
	NomtLeafCacheMB       int `json:"nomt_leaf_cache_mb,omitempty"`      // Leaf cache in MiB (default: 512)

	// BLS Block Signing: Master signs blocks, Sub verifies before accepting.
	// MasterBLSPubKey: hex-encoded BLS public key of the Master node (for Sub nodes to verify signatures)
	MasterBLSPubKey string `json:"master_bls_pubkey,omitempty"`
	// SkipSignatureVerification: if true, Sub node accepts blocks without verifying signature (backward compatibility)
	SkipSignatureVerification bool `json:"skip_signature_verification,omitempty"`

	// Runtime memory tuning (per-node configurable, prevents OOM kill)
	GoMemLimitGB int `json:"go_mem_limit_gb,omitempty"` // Go soft memory limit in GB (default: 8). Set per-node to prevent OOM when running multiple nodes on same server.
	GoGCPercent  int `json:"go_gc_percent,omitempty"`   // GC target percentage (default: 800). Lower = more frequent GC = less memory but more CPU.

	Pruning       PruningConfig      `json:"pruning,omitempty"`
	RpcRateLimit  RpcRateLimitConfig `json:"rpc_rate_limit,omitempty"`
	TraceEnabled  bool               `json:"trace_enabled,omitempty"`
	TraceEndpoint string             `json:"trace_endpoint,omitempty"`
	TlsCert       string             `json:"tls_cert,omitempty"`
	TlsKey        string             `json:"tls_key,omitempty"`
	Databases     DatabasesConfig    `json:"Databases"`
	Nodes         NodesConfig        `json:"nodes"`
	Log           LogConfig          `json:"log"`
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

		// RPC Rate Limit default configuration if not fully specified
		if ConfigApp.RpcRateLimit.GlobalRate == 0 {
			ConfigApp.RpcRateLimit.Enabled = false // TEMPORARILY DISABLED FOR TESTING
			ConfigApp.RpcRateLimit.GlobalRate = 10000
			ConfigApp.RpcRateLimit.PerIpRate = 50
			ConfigApp.RpcRateLimit.BlockDurationSecs = 300 // 5 minutes default block
		}

		// Databases sharding defaults
		if ConfigApp.Databases.NumShardsDefault == 0 {
			ConfigApp.Databases.NumShardsDefault = 1 // TUNED: Restore to 1 to prevent massive OOM
		}
		if ConfigApp.Databases.NumShardsSmartContract == 0 {
			ConfigApp.Databases.NumShardsSmartContract = 1 // TUNED: Restore to 1 to prevent massive OOM
		}
		if ConfigApp.Databases.NumShardsCode == 0 {
			ConfigApp.Databases.NumShardsCode = 1 // TUNED: Restore to 1 to prevent massive OOM
		}
		if ConfigApp.Databases.Parallelism == 0 {
			ConfigApp.Databases.Parallelism = 2 // TUNED: Restore to 2 to match previous behavior
		}
		if ConfigApp.Databases.PebbleCacheSizeMB == 0 {
			ConfigApp.Databases.PebbleCacheSizeMB = 512
		}

		// Default log configuration if not specified
		if ConfigApp.Log.Level == "" {
			ConfigApp.Log.Level = "info"
			ConfigApp.Log.Format = "text"
			ConfigApp.Log.ConsoleOutput = true
			ConfigApp.Log.FileOutput = true
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
		if v := os.Getenv("META_IS_RPC_NODE"); v != "" {
			if v == "true" || v == "1" {
				ConfigApp.IsRPCNode = true
			} else if v == "false" || v == "0" {
				ConfigApp.IsRPCNode = false
			}
		}

	})
	return ConfigApp, err
}
