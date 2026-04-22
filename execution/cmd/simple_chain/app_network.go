package main

import (
	"context"
	"fmt"
	"os"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// initNetwork initializes network-related components.
// Luồng khởi động:
//  1. initNetwork() — tạo node, setup storage (ĐỒNG BỘ, không network call)
//  2. initProcessors() — tạo processors (cần app.node sẵn sàng)
//  3. initRoutes() — tạo socketServer + đăng ký routes
//  4. Run() — Master: start chain | Sub: ConnectTo → sync → fee → start chain
func (app *App) initNetwork() error {
	// Initialize BLS cryptography
	bls.Init()

	// Load genesis data
	var err error
	app.genesis, err = config.LoadGenesisData(app.config.GenesisFilePath)
	if err != nil {
		return fmt.Errorf("failed to load genesis data: %v", err)
	}
	app.config.ChainId = app.genesis.Config.ChainId

	// Initialize key pair
	app.keyPair = bls.NewKeyPair(e_common.FromHex(app.config.PrivateKey))

	// Initialize network components
	app.connectionsManager = network.NewConnectionsManager()
	app.messageSender = network.NewMessageSender(app.config.Version)

	// Initialize host node — ĐỒNG BỘ để app.node sẵn sàng cho initProcessors/initRoutes
	if err := app.initHostNode(); err != nil {
		return fmt.Errorf("failed to init host node: %w", err)
	}

	return nil
}

// initHostNode tạo HostNode và setup storage maps.
// KHÔNG làm network call — chỉ chuẩn bị local state.
// Sub node sync và fee fetch sẽ được xử lý trong Run() sau khi TCP connection sẵn sàng.
func (app *App) initHostNode() error {
	// Kiểm tra và tạo thư mục RootPath nếu chưa tồn tại
	if _, err := os.Stat(app.config.Databases.RootPath); os.IsNotExist(err) {
		logger.Info("Thư mục RootPath '%s' không tồn tại, đang tạo mới...", app.config.Databases.RootPath)
		err = os.MkdirAll(app.config.Databases.RootPath, 0755)
		if err != nil {
			return fmt.Errorf("không thể tạo thư mục RootPath '%s': %w", app.config.Databases.RootPath, err)
		}
		logger.Info("Đã tạo thành công thư mục RootPath '%s'", app.config.Databases.RootPath)
	} else if err != nil {
		return fmt.Errorf("lỗi khi kiểm tra thư mục RootPath '%s': %w", app.config.Databases.RootPath, err)
	}

	ctx := context.Background()

	// Tạo HostNode mới (không dùng libp2p — dùng TCP networking từ pkg/network)
	hostNode, err := node.NewHostNode(
		ctx,
		app.config.Databases.RootPath,
		string(app.config.ServiceType),
		app.connectionsManager,
		app.messageSender,
	)
	if err != nil {
		return fmt.Errorf("failed to create host node: %w", err)
	}

	app.node = hostNode

	// Bản đồ lưu trữ dữ liệu
	topicStorageMap := map[string]storage.Storage{
		"backup": app.storageManager.GetStorageBackupDb(),
	}
	hostNode.SetTopicStorageMap(topicStorageMap)
	hostNode.AddTopicStorage(node.BackupStorageKey, app.storageManager.GetStorageBackupDb())
	hostNode.SetBackupStorage(app.storageManager.GetStorageBackupDb())

	// Setup fee addresses — chỉ Master load local, Sub sẽ fetch từ master trong Run()
	if app.config.ServiceType == common.ServiceTypeMaster {
		app.loadFreeFeeAddresses()
		freeFeeAddressesSlice := ConvertAddressMapToStringSlice(FreeFeeAddresses)
		app.node.SetFeeAddresses(freeFeeAddressesSlice)
	}

	return nil
}


// ConnectToPeer — legacy function, no longer used after libp2p removal.
func (app *App) ConnectToPeer(peerAddr string) error {
	logger.Warn("ConnectToPeer called but libp2p is removed. Use ConnectTo() instead.")
	return nil
}

// ListenForStateChanges listens for state changes from storage.StateChangeChan
func ListenForStateChanges() {
	for state := range storage.StateChangeChan {
		logger.Info("Listener received new state: %d", state)
		switch state {
		case storage.DoneSubscribe:
			logger.Info("Listener: Subscription completed")
		case storage.StateLoadingSnapshot:
			logger.Info("Listener: Loading snapshot")
		case storage.StateSnapshotLoaded:
			logger.Info("Listener: Snapshot loaded")
		case storage.StateDBReadCompleted:
			logger.Info("Listener: DB read completed")
		case storage.StateRAMReadCompleted:
			logger.Info("Listener: RAM read completed")
		default:
			logger.Info("Listener: Unknown state: %d", state)
		}
	}
}

