// Package storagenode chứa logic để tạo và quản lý một storage node.
package storagenode

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	// Import các package cần thiết từ dự án gốc của bạn
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	typesnetwork "github.com/meta-node-blockchain/meta-node/types/network"
)

// Config chứa tất cả các tham số cấu hình cho một StorageNode.
type Config struct {
	ListenAddress   string `json:"ListenAddress"`
	DBPath          string `json:"DBPath"`
	DBEngine        string `json:"DBEngine"`
	NodeType        string `json:"NodeType"`
	Version         string `json:"Version"`
	BLSPrivateKey   string `json:"BLSPrivateKey"`
	SnapshotPath    string `json:"SnapshotPath"`    // Đường dẫn để lưu snapshot
	MaxPartSizeMB   int    `json:"MaxPartSizeMB"`   // Kích thước tối đa mỗi phần snapshot
	ArchiveBaseName string `json:"ArchiveBaseName"` // Tên gốc cho file archive
}

// StorageNode đại diện cho một instance của storage server, có thể quản lý được.
type StorageNode struct {
	config  *Config
	storage storage.Storage
	// SỬA LỖI: Sử dụng kiểu interface để tương thích
	socketServer typesnetwork.SocketServer
	cancelFunc   context.CancelFunc
}

// NewStorageNode khởi tạo một storage node mới với cấu hình cho trước nhưng chưa chạy nó.
func NewStorageNode(cfg *Config) (*StorageNode, error) {
	bls.Init()

	// --- Thiết lập Database ---
	var actualStorage storage.Storage
	var err error
	if cfg.DBEngine == "sharded" {
		actualStorage, err = storage.NewShardelDB(cfg.DBPath, 16, 2, storage.TypeLevelDB, "")
		if err != nil {
			return nil, fmt.Errorf("failed to initialize ShardelDB: %w", err)
		}
		if err := actualStorage.Open(); err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
		logger.Info("Successfully initialized storage engine: ShardedDB at path '%s'", cfg.DBPath)
	} else {
		return nil, fmt.Errorf("unsupported DBEngine specified: '%s'", cfg.DBEngine)
	}

	// --- Khởi tạo các thành phần Network cốt lõi ---
	blsPrivKeyBytes, err := hex.DecodeString(cfg.BLSPrivateKey)
	if err != nil {
		actualStorage.Close() // Dọn dẹp nếu thất bại
		return nil, fmt.Errorf("invalid BLS private key in config: %w", err)
	}
	keyPair := bls.NewKeyPair(blsPrivKeyBytes)
	logger.Info("Loaded BLS Key Pair. Server Address: %s", keyPair.Address().Hex())

	connectionsManager := network.NewConnectionsManager()
	messageSender := network.NewMessageSender(cfg.Version)

	// --- Khởi tạo Service và Routes ---
	remoteStorageSvc := storage.NewRemoteStorageService(actualStorage, messageSender)
	storageRoutes := remoteStorageSvc.GetRoutes()
	allRoutes := make(map[string]func(typesnetwork.Request) error)
	for command, handlerFunc := range storageRoutes {
		allRoutes[command] = handlerFunc
	}
	mainHandler := network.NewHandler(allRoutes, nil)
	logger.Info("Registered %d storage routes.", len(storageRoutes))

	// --- Khởi tạo Socket Server ---
	// network.NewSocketServer trả về một struct cụ thể, nhưng nó thỏa mãn interface typesnetwork.SocketServer
	socketServer, err := network.NewSocketServer(
		nil,
		keyPair,
		connectionsManager,
		mainHandler,
		cfg.NodeType,
		cfg.Version,
	)
	if err != nil {
		actualStorage.Close() // Dọn dẹp nếu thất bại
		return nil, fmt.Errorf("failed to create socket server: %w", err)
	}

	// --- Trả về node đã được cấu hình hoàn chỉnh, nhưng chưa chạy ---
	return &StorageNode{
		config:       cfg,
		storage:      actualStorage,
		socketServer: socketServer, // Phép gán này giờ đã hợp lệ
	}, nil
}

// Start khởi chạy socket server để lắng nghe kết nối. Hàm này không block.
func (sn *StorageNode) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	sn.cancelFunc = cancel
	sn.socketServer.SetContext(ctx, cancel)

	go func() {
		logger.Info("StorageNode is now listening on %s", sn.config.ListenAddress)
		if err := sn.socketServer.Listen(sn.config.ListenAddress); err != nil && err != context.Canceled {
			logger.Error("StorageNode listening error: %v", err)
		}
	}()
}

// Stop thực hiện graceful shutdown cho StorageNode.
func (sn *StorageNode) Stop() {
	logger.Info("Stopping StorageNode...")

	// 1. Tạo snapshot trước khi tắt
	sn.CreateSnapshot()

	// 2. Gửi tín hiệu dừng cho server
	if sn.cancelFunc != nil {
		sn.cancelFunc()
	}
	sn.socketServer.Stop()

	// 3. Đóng database
	logger.Info("Closing database...")
	if err := sn.storage.Close(); err != nil {
		logger.Error("Error closing database: %v", err)
	} else {
		logger.Info("Database closed successfully.")
	}

	// 4. (Tùy chọn) Xóa snapshot sau khi đã tắt hẳn
	if err := sn.removeSnapshot(); err != nil {
		logger.Error("Could not remove snapshot directory: %v", err)
	} else {
		logger.Info("Snapshot directory removed.")
	}

	logger.Info("StorageNode has been shut down.")
}

// CreateSnapshot nén thư mục dữ liệu và tạo snapshot.
func (sn *StorageNode) CreateSnapshot() {
	logger.Info("Creating snapshot...")
	if sn.config.SnapshotPath == "" || sn.config.DBPath == "" {
		logger.Warn("SnapshotPath or DBPath is not configured. Skipping snapshot creation.")
		return
	}

	_, err := node.CompressFolderAndSplitWithOptionalSnapshot(
		context.Background(),
		sn.config.DBPath,
		sn.config.SnapshotPath,
		sn.config.ArchiveBaseName,
		sn.config.MaxPartSizeMB,
	)
	if err != nil {
		logger.Error("Failed to create snapshot: %v", err)
	} else {
		logger.Info("Successfully created snapshot at %s", sn.config.SnapshotPath)
	}
}

// removeSnapshot xóa thư mục snapshot.
func (sn *StorageNode) removeSnapshot() error {
	if sn.config.SnapshotPath == "" {
		return nil // Không có gì để xóa
	}
	logger.Info("Removing snapshot directory: %s", sn.config.SnapshotPath)
	if _, err := os.Stat(sn.config.SnapshotPath); os.IsNotExist(err) {
		return nil // Thư mục không tồn tại
	}
	return os.RemoveAll(sn.config.SnapshotPath)
}
