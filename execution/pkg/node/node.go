package node

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

type ConnectionStatus int

const (
	Disconnected ConnectionStatus = iota
	Connecting
	Connected
	Failed
)
const (
	BackupStorageKey = "backup"
)

func (s ConnectionStatus) String() string {
	switch s {
	case Disconnected:
		return "Disconnected"
	case Connecting:
		return "Connecting"
	case Connected:
		return "Connected"
	case Failed:
		return "Failed"
	default:
		return "Unknown"
	}
}

// PeerInfo lưu thông tin peer (simplified — không dùng libp2p peer.AddrInfo)
type PeerInfo struct {
	Address        string
	Type           string
	Status         ConnectionStatus
	LastConnected  time.Time
	ReconnectCount int
	LastError      string
}

// HostNode là đối tượng chứa state cho node networking.
// Đã loại bỏ libp2p — giờ chỉ giữ: LRU cache, channels, peers, fee addresses.
type HostNode struct {
	NodeType            string
	Peers               map[string]*PeerInfo
	rootPath            string
	KeyValueStore       *lru.Cache[string, []byte]
	wg                  sync.WaitGroup
	reconnectMutex      sync.Mutex
	ctx                 context.Context
	TopicStorageMap     sync.Map
	fetchingBlocks      sync.Map

	// Kênh chuyên dụng để xử lý block, tách biệt với mạng
	BlockProcessingQueue chan *storage.BackUpDb

	FeeAddresses    []string
	topicStorageMap map[string]storage.Storage

	// Network components — thay thế libp2p
	ConnectionsManager t_network.ConnectionsManager
	MessageSender      t_network.MessageSender

	// Direct reference to backup storage for sync fallback
	backupStorage storage.Storage
}

// Config holds configuration parameters for the HostNode
type Config struct {
	InitialReconnectDelay time.Duration
	MaxReconnectDelay     time.Duration
	MaxReconnectAttempts  int
	PingInterval          time.Duration
	PingTimeout           time.Duration
}

// DefaultConfig returns a default configuration
func DefaultConfig() Config {
	return Config{
		InitialReconnectDelay: 50 * time.Millisecond,
		MaxReconnectDelay:     2 * time.Minute,
		MaxReconnectAttempts:  100,
		PingInterval:          3 * time.Second,
		PingTimeout:           1 * time.Second,
	}
}

// NewHostNode tạo HostNode mới (không dùng libp2p)
func NewHostNode(ctx context.Context, rootPath string, nodeType string,
	connMgr t_network.ConnectionsManager, msgSender t_network.MessageSender) (*HostNode, error) {

	const maxCacheSize = 200 // Reduced from 100000 to prevent memory bloat from large block backup blobs
	cache, err := lru.New[string, []byte](maxCacheSize)
	if err != nil {
		log.Fatalf("Không thể tạo LRU cache: %v", err)
	}

	const blockProcessingQueueSize = 10000 // Reduced from 2M to prevent memory bloat

	node := &HostNode{
		NodeType:             nodeType,
		Peers:                make(map[string]*PeerInfo),
		rootPath:             rootPath,
		ctx:                  ctx,
		KeyValueStore:        cache,
		BlockProcessingQueue: make(chan *storage.BackUpDb, blockProcessingQueueSize),
		topicStorageMap:      make(map[string]storage.Storage),
		ConnectionsManager:   connMgr,
		MessageSender:        msgSender,
	}

	// Periodic cleanup task for stale receiving states
	go func() {
		nodeCtx := node.ctx
		if nodeCtx == nil {
			nodeCtx = context.Background()
		}
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		logger.Info("Started periodic cleanup task for stale receiving states.")
		for {
			select {
			case <-ticker.C:
				logger.Debug("Running periodic cleanup for receiving states...")
				CleanupOldStates(24 * time.Hour)
			case <-nodeCtx.Done():
				logger.Info("Stopping receiving state cleanup task.")
				return
			}
		}
	}()

	node.DisplayNodeInfo()
	return node, nil
}

// SetBackupStorage sets a direct reference to backup storage for sync fallback.
func (node *HostNode) SetBackupStorage(s storage.Storage) {
	node.backupStorage = s
}

// GetBackupStorageDirect returns the direct backup storage reference.
func (node *HostNode) GetBackupStorageDirect() storage.Storage {
	return node.backupStorage
}

// SetFeeAddresses cập nhật danh sách địa chỉ phí một cách an toàn.
func (node *HostNode) SetFeeAddresses(addresses []string) {
	node.FeeAddresses = make([]string, len(addresses))
	copy(node.FeeAddresses, addresses)
	logger.Debug(fmt.Sprintf("Updated FeeAddresses: %v", node.FeeAddresses))
}

// GetFeeAddresses trả về bản sao của danh sách địa chỉ phí hiện tại một cách an toàn.
func (node *HostNode) GetFeeAddresses() []string {
	addressesCopy := make([]string, len(node.FeeAddresses))
	copy(addressesCopy, node.FeeAddresses)
	return addressesCopy
}

// AddTopicStorage thêm hoặc cập nhật một storage instance cho một topic cụ thể.
func (node *HostNode) AddTopicStorage(topicName string, storageInstance storage.Storage) {
	if storageInstance == nil {
		logger.Warn(fmt.Sprintf("Attempted to add nil storage for topic: %s", topicName))
		return
	}
	node.TopicStorageMap.Store(topicName, storageInstance)
	logger.Info(fmt.Sprintf("Added/Updated storage for topic: %s using sync.Map", topicName))
}

// RemoveTopicStorage xóa storage instance liên kết với một topic khỏi map quản lý.
func (node *HostNode) RemoveTopicStorage(topicName string) error {
	if _, loaded := node.TopicStorageMap.Load(topicName); !loaded {
		return fmt.Errorf("storage for topic '%s' not found in sync.Map", topicName)
	}
	node.TopicStorageMap.Delete(topicName)
	logger.Info(fmt.Sprintf("Removed storage reference for topic: %s using sync.Map", topicName))
	return nil
}

// GetTopicStorage lấy storage instance liên kết với một topic một cách an toàn.
func (node *HostNode) GetTopicStorage(topicName string) (storage.Storage, bool) {
	value, loaded := node.TopicStorageMap.Load(topicName)
	if !loaded {
		return nil, false
	}
	storageInstance, ok := value.(storage.Storage)
	if !ok {
		logger.Error(fmt.Sprintf("Invalid type stored in sync.Map for topic %s: expected storage.Storage, got %T", topicName, value))
		return nil, false
	}
	return storageInstance, true
}

// SetTopicStorageMap gán một map[string]storage.Storage vào trường topicStorageMap của HostNode.
func (node *HostNode) SetTopicStorageMap(m map[string]storage.Storage) {
	node.topicStorageMap = m
}

// DisplayNodeInfo hiển thị thông tin node
func (node *HostNode) DisplayNodeInfo() {
	fmt.Println("Node Type:", node.NodeType)
	fmt.Println("Node networking: TCP (pkg/network)")
}

// HasConnectedPeers returns true if the ConnectionsManager has at least one active connection.
func (node *HostNode) HasConnectedPeers() bool {
	if node.ConnectionsManager == nil {
		return false
	}
	// Check parent connection (sub -> master) explicitly if set
	if node.ConnectionsManager.ParentConnection() != nil {
		return true
	}
	// Check master connections (sub -> master) by type
	masterConns := node.ConnectionsManager.ConnectionsByType(
		common.MapConnectionTypeToIndex(common.MASTER_CONNECTION_TYPE))
	if len(masterConns) > 0 {
		return true
	}
	// Check child connections (master -> sub nodes)
	childConns := node.ConnectionsManager.ConnectionsByType(
		common.MapConnectionTypeToIndex(common.CHILD_NODE_CONNECTION_TYPE))
	return len(childConns) > 0
}

// SendRequestToMaster gửi request tới Master node qua TCP.
// message sẽ được gửi như command "FileRequest" với body là message string.
func (node *HostNode) SendRequestToMaster(ctx context.Context, message string) error {
	if node.ConnectionsManager == nil || node.MessageSender == nil {
		return fmt.Errorf("network components not initialized")
	}

	// Thử ParentConnection trước (sub -> master)
	parentConn := node.ConnectionsManager.ParentConnection()
	if parentConn != nil && parentConn.IsConnect() {
		err := node.MessageSender.SendBytes(parentConn, "FileRequest", []byte(message))
		if err != nil {
			logger.Error(fmt.Sprintf("❌ Request '%s' failed to Master (parent): %v", message, err))
			return fmt.Errorf("failed to send '%s' request to master: %w", message, err)
		}
		logger.Info(fmt.Sprintf("✅ Successfully sent '%s' request to Master.", message))
		return nil
	}

	// Fallback: Tìm master connections theo type
	masterConns := node.ConnectionsManager.ConnectionsByType(
		common.MapConnectionTypeToIndex(common.MASTER_CONNECTION_TYPE))
	for _, conn := range masterConns {
		if conn != nil && conn.IsConnect() {
			err := node.MessageSender.SendBytes(conn, "FileRequest", []byte(message))
			if err != nil {
				logger.Error(fmt.Sprintf("❌ Request '%s' failed to Master: %v", message, err))
				return fmt.Errorf("failed to send '%s' request to master: %w", message, err)
			}
			logger.Info(fmt.Sprintf("✅ Successfully sent '%s' request to Master.", message))
			return nil
		}
	}

	return fmt.Errorf("no active master connection available")
}

// Close gracefully shuts down the host node
func (node *HostNode) Close() error {
	node.wg.Wait()
	logger.Info("HostNode closed.")
	return nil
}
