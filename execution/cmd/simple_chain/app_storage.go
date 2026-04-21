package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// initStorage initializes all storage-related components
func (app *App) initStorage() error {
	// Initialize storage databases
	if err := app.initStorageDatabases(); err != nil {
		return err
	}
	return nil
}

// createDatabase determines whether to create a local or remote database.
// This version ensures the remote server starts successfully before continuing.
func (app *App) createDatabase(dbDetail config.DBDetail, backupPath string, dbType storage.DBType) (storage.Storage, error) {
	// Case 1: Run as a Remote Storage Node
	if app.config.Databases.NodeType == config.STORAGE_REMOTE {
		// Tạo một channel để nhận tín hiệu khởi động từ server.
		// Channel này sẽ nhận `nil` nếu thành công, hoặc `error` nếu thất bại.
		startupSignal := make(chan error)

		// Bắt đầu chạy server trong một goroutine và truyền channel vào.
		go func() {
			// Goroutine này sẽ chạy cho đến khi app bị dừng.
			// Lỗi trả về ở đây là lỗi lúc server đang chạy hoặc lúc tắt, không phải lỗi khởi động.
			if err := app.startStorageServer(app.ctx, dbDetail, startupSignal); err != nil {
				logger.Error("Storage server stopped with error: %v", err)
			}
		}()

		// **QUAN TRỌNG: Chặn và đợi tín hiệu từ goroutine của server**
		if err := <-startupSignal; err != nil {
			// Nếu nhận được lỗi, có nghĩa là server KHÔNG thể khởi động. Trả về lỗi ngay lập tức.
			return nil, fmt.Errorf("remote storage server failed to start: %w", err)
		}

		// Nếu không có lỗi, server đã khởi động thành công và đang lắng nghe.
		logger.Info("Remote storage server started successfully. Connecting client...")

		time.Sleep(1 * time.Second)
		// Bây giờ mới tiến hành kết nối client.
		return createRemoteDBClient(dbDetail.ListenAddress)
	}
	if app.config.Databases.NodeType == config.STORAGE_CLIENT {
		return createRemoteDBClient(dbDetail.ListenAddress)
	}

	// Case 2: Use a Local Database (không thay đổi)
	logger.Info("Initializing local ShardelDB at: %s", dbDetail.Path)
	db, err := storage.NewShardelDB(
		config.JoinPathIfNotURL(app.config.Databases.RootPath, dbDetail.Path),
		4,
		2,
		dbType,
		backupPath,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize local ShardelDB: %w", err)
	}

	if err := db.Open(); err != nil {
		return nil, fmt.Errorf("failed to open local ShardelDB: %w", err)
	}
	return db, nil
}

// startStorageServer encapsulates all logic for initializing and running the storage server.
// It now accepts a `startupSignal` channel to report its status back.
func (app *App) startStorageServer(ctx context.Context, dbDetail config.DBDetail, startupSignal chan<- error) error {
	// 1. Initialize the actual ShardelDB storage
	actualStorage, err := storage.NewShardelDB(config.JoinPathIfNotURL(app.config.Databases.RootPath, dbDetail.Path), 4, 2, app.config.DBType, "")
	if err != nil {
		startupSignal <- err // Gửi lỗi khởi động về và kết thúc
		return err
	}
	if err := actualStorage.Open(); err != nil {
		startupSignal <- err // Gửi lỗi khởi động về và kết thúc
		return err
	}

	// 2. Load BLS Key
	blsPrivKeyBytes, err := hex.DecodeString(app.config.Databases.BLSPrivateKey)
	if err != nil {
		startupSignal <- err // Gửi lỗi khởi động về và kết thúc
		return err
	}
	keyPair := bls.NewKeyPair(blsPrivKeyBytes)
	logger.Info("Loaded BLS Key Pair. Server Address: %s", keyPair.Address().Hex())

	// 3. Setup network and routes
	connectionsManager := network.NewConnectionsManager()
	messageSender := network.NewMessageSender(app.config.Databases.Version)
	remoteStorageSvc := storage.NewRemoteStorageService(actualStorage, messageSender)
	storageRoutes := remoteStorageSvc.GetRoutes()
	mainHandler := network.NewHandler(storageRoutes, nil)
	logger.Info("Registered %d storage routes.", len(storageRoutes))

	// 4. Setup Socket Server
	socketServer, err := network.NewSocketServer(
		nil,
		keyPair,
		connectionsManager,
		mainHandler,
		app.config.Databases.Version,
	)
	if err != nil {
		startupSignal <- err // Gửi lỗi khởi động về và kết thúc
		return err
	}
	socketServer.SetContext(ctx, app.cancel) // Use the app's context for lifecycle

	// **QUAN TRỌNG: Gửi tín hiệu khởi động thành công (nil) trước khi bắt đầu Listen()**
	// Listen() là một hàm blocking, nên tín hiệu phải được gửi trước nó.
	startupSignal <- nil

	// 5. Start listening for connections (this is a blocking call)
	logger.Info("Storage server is now listening on %s", dbDetail.ListenAddress)
	err = socketServer.Listen(dbDetail.ListenAddress)

	// The error will be returned when the server stops
	if err != nil && err != context.Canceled {
		return fmt.Errorf("server listening error: %w", err)
	}

	logger.Info("Storage server has stopped.")
	return nil
}

// createRemoteDBClient encapsulates the logic for creating a client that connects to the storage server.
func createRemoteDBClient(serverAddress string) (storage.Storage, error) {
	// 1. Establish connection to the server
	serverConnection := network.NewConnection(e_common.Address{}, "REMOTE_DB_CLIENT", nil)
	serverConnection.SetRealConnAddr(serverAddress)

	if err := serverConnection.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to server at %s: %w", serverAddress, err)
	}
	logger.Info("Successfully connected to remote storage server.")
	// IMPORTANT: Do NOT `defer serverConnection.Disconnect()` here.
	// The responsibility to disconnect belongs to the component that uses the remoteStorage.

	// Start a goroutine to read data from the server
	go serverConnection.ReadRequest()

	// 2. Initialize the remote storage client
	messageSender := network.NewMessageSender("1.0.0") // Or use version from config
	remoteStorage := storage.NewRemoteStorage(messageSender, serverConnection)

	return remoteStorage, nil
}

// initStorageDatabases initializes and opens all required databases
func (app *App) initStorageDatabases() error {
	logger.Info("Start initStorageDatabases")

	// Helper function for creating and adding storage
	createAndAdd := func(storageName string, dbDetail config.DBDetail, dbType storage.DBType, addFunc func(storage.Storage) error) error {
		fullBackupPath := app.config.BackupPath + dbDetail.Path

		var db storage.Storage
		var err error

		if app.config.StateBackend == "nomt" && (storageName == "account" || storageName == "stake" || storageName == "trie") {
			logger.Info("Using DummyStorage for %s because state_backend is nomt", storageName)
			db = storage.NewDummyStorage(fullBackupPath)
		} else {
			db, err = app.createDatabase(dbDetail, fullBackupPath, dbType)
			if err != nil {
				return fmt.Errorf("failed to create %s DB: %w", storageName, err)
			}
		}

		return addFunc(db)
	}

	databaseConfig := app.config.Databases

	// Account state database
	if err := createAndAdd("account", databaseConfig.AccountState, app.config.DBType, app.storageManager.AddStorageAccount); err != nil {
		return err
	}

	// Receipts database
	if err := createAndAdd("receipts", databaseConfig.Receipts, app.config.DBType, app.storageManager.AddStorageReceipt); err != nil {
		return err
	}

	// Transaction state database
	if err := createAndAdd("transaction state", databaseConfig.TransactionState, app.config.DBType, app.storageManager.AddStorageTransaction); err != nil {
		return err
	}

	// Device key storage
	if err := createAndAdd("device key", databaseConfig.BackupDeviceKey, app.config.DBType, app.storageManager.AddStorageBackupDeviceKey); err != nil {
		return err
	}

	// Smart contract database
	if err := createAndAdd("smart contract", app.config.Databases.SmartContractStorage, app.config.DBType, app.storageManager.AddStorageSmartContract); err != nil {
		return err
	}

	// Code database
	if err := createAndAdd("code", databaseConfig.SmartContractCode, app.config.DBType, app.storageManager.AddStorageCode); err != nil {
		return err
	}

	// Trie database
	if err := createAndAdd("trie", databaseConfig.Trie, app.config.DBType, app.storageManager.AddStorageDatabaseTrie); err != nil {
		return err
	}

	// Block database
	if err := createAndAdd("block", databaseConfig.Blocks, app.config.DBType, app.storageManager.AddStorageBlock); err != nil {
		return err
	}

	// Mapping database
	if err := createAndAdd("mapping", databaseConfig.Mapping, app.config.DBType, app.storageManager.AddStorageMapping); err != nil {
		return err
	}

	// Stake database
	if err := createAndAdd("stake", databaseConfig.Stake, app.config.DBType, app.storageManager.AddStorageStake); err != nil {
		return err
	}

	logger.Info("End initStorageDatabases")
	return nil
}
