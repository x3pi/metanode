package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/types"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

// FreeFeeAddresses is a list of addresses that can transact without fees
var FreeFeeAddresses = map[e_common.Address]struct{}{}

// App defines the main application structure
type App struct {
	// Configuration
	config     *config.SimpleChainConfig
	genesis    *config.GenesisData
	keyPair    *bls.KeyPair
	chainState *blockchain.ChainState

	// Network components
	node               *node.HostNode
	connectionsManager t_network.ConnectionsManager
	messageSender      t_network.MessageSender
	socketServer       t_network.SocketServer

	// Storage components
	storageManager *storage.StorageManager
	// Transaction handling
	transactionPool *transaction_pool.TransactionPool
	startLastBlock  types.Block

	// Event system
	eventSystem *filters.EventSystem

	// Lifecycle management
	ctx    context.Context
	cancel context.CancelFunc
}

// NewApp creates and initializes a new blockchain application
func NewApp(configFilePath string, logLevel int) (*App, error) {
	log.Println("New App")
	storage.InitIncrementingCounterFromBootTime()
	// Initialize basic structure and lifecycle context
	app := &App{}
	app.ctx, app.cancel = context.WithCancel(context.Background())

	// Load configuration
	var err error
	app.config, err = config.LoadConfig(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %v", err)
	}
	app.storageManager = storage.NewStorageManager()

	// Initialize network components
	if err := app.initNetwork(); err != nil {
		return nil, fmt.Errorf("failed to initialize network: %v", err)
	}

	// Initialize storage
	if err := app.initStorage(); err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %v", err)
	}

	// Initialize blockchain components
	if err := app.initBlockchain(); err != nil {
		return nil, fmt.Errorf("failed to initialize blockchain: %v", err)
	}

	// Initialize processors and routes
	app.initProcessors()
	app.initRoutes()

	return app, nil
}

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
		16,
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
	actualStorage, err := storage.NewShardelDB(config.JoinPathIfNotURL(app.config.Databases.RootPath, dbDetail.Path), 16, 2, app.config.DBType, "")
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

	databaseConfig := app.config.Databases

	// Use unified SharedDB if running locally (not remote or client)
	if databaseConfig.NodeType != config.STORAGE_REMOTE && databaseConfig.NodeType != config.STORAGE_CLIENT {
		if err := app.storageManager.InitSharedDatabase(databaseConfig.RootPath, app.config.DBType); err != nil {
			return fmt.Errorf("failed to initialize unified shared database: %w", err)
		}
		logger.Info("Initialized unified SharedDB at %s/chaindata", databaseConfig.RootPath)
		return nil
	}

	// Legacy fallback for Remote/Client configs
	logger.Warn("Using legacy fragmented DB architecture for Remote/Client mode")
	
	// Helper function for creating and adding storage
	createAndAdd := func(storageName string, dbDetail config.DBDetail, dbType storage.DBType, addFunc func(storage.Storage) error) error {
		fullBackupPath := app.config.BackupPath + dbDetail.Path
		db, err := app.createDatabase(dbDetail, fullBackupPath, dbType)
		if err != nil {
			return fmt.Errorf("failed to create %s DB: %w", storageName, err)
		}
		return addFunc(db)
	}

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
	if err := createAndAdd("smart contract", databaseConfig.SmartContractStorage, app.config.DBType, app.storageManager.AddStorageSmartContract); err != nil {
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

	// Backup database (Legacy setup)
	backupPath := config.JoinPathIfNotURL(app.config.BackupPath, databaseConfig.Backup.Path)
	backupDB, err := storage.NewShardelDB(
		backupPath,
		16, 2,
		app.config.DBType,
		backupPath,
	)
	if err != nil {
		return fmt.Errorf("failed to create backup DB: %v", err)
	}
	if err := backupDB.Open(); err != nil {
		return fmt.Errorf("failed to open backup DB: %v", err)
	}
	if err := app.storageManager.AddStorageBackupDb(backupDB); err != nil {
		return err
	}

	logger.Info("End initStorageDatabases via legacy fallback")
	return nil
}

// initNetwork initializes network-related components
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

	// Initialize host node
	go app.initHostNode()

	return nil
}

func (app *App) initHostNode() error {
	// Kiểm tra và tạo thư mục RootPath nếu chưa tồn tại
	if _, err := os.Stat(app.config.Databases.RootPath); os.IsNotExist(err) {
		log.Printf("Thư mục RootPath '%s' không tồn tại, đang tạo mới...", app.config.Databases.RootPath)
		err = os.MkdirAll(app.config.Databases.RootPath, 0755)
		if err != nil {
			return fmt.Errorf("không thể tạo thư mục RootPath '%s': %w", app.config.Databases.RootPath, err)
		}
		log.Printf("Đã tạo thành công thư mục RootPath '%s'", app.config.Databases.RootPath)
	} else if err != nil {
		return fmt.Errorf("lỗi khi kiểm tra thư mục RootPath '%s': %w", app.config.Databases.RootPath, err)
	}

	ctx := context.Background()
	nodeType := "exec"

	// Tạo HostNode mới (không dùng libp2p)
	hostNode, err := node.NewHostNode(
		ctx,
		app.config.Databases.RootPath,
		nodeType,
		app.connectionsManager,
		app.messageSender,
	)
	if err != nil {
		return fmt.Errorf("failed to create host node: %w", err)
	}

	app.node = hostNode

	feeAddresses, err := app.node.GetFeeAddressesFromMaster()
	if err != nil {
		return fmt.Errorf("lỗi khi lấy danh sách địa chỉ miễn phí từ master: %w", err)
	}
	FreeFeeAddresses = ConvertStringSliceToAddressMap(feeAddresses)
	return nil
}

// initBlockchain initializes blockchain-related components
func (app *App) initBlockchain() error {

	blockDatabase := block.NewBlockDatabase(app.storageManager.GetStorageBlock())

	app.transactionPool = transaction_pool.NewTransactionPool()

	// Set up event system
	app.eventSystem = filters.NewEventSystem()

	// Initialize last block or create genesis block

	lastBlock, err := blockDatabase.GetLastBlock()
	log.Printf("lastblock header 1: %v ", lastBlock)

	if err != nil {
		return fmt.Errorf("GetLastBlock error: %w", err)

	} else {
		// Use existing last block
		app.startLastBlock = lastBlock
		log.Printf("lastblock header 2: %v ", app.startLastBlock.Header())
		storage.UpdateLastBlockNumber(app.startLastBlock.Header().BlockNumber())

		// Create account state trie from existing root
		accountStorage := app.storageManager.GetStorageAccount()
		_, err := trie.New(app.startLastBlock.Header().AccountStatesRoot(), accountStorage, true)
		if err != nil {
			return fmt.Errorf("failed to create account state trie: %v", err)
		}

		app.chainState, err = blockchain.NewChainState(app.storageManager, blockDatabase, app.startLastBlock.Header(), app.config, FreeFeeAddresses, app.config.BackupPath)
		if err != nil {
			return fmt.Errorf("failed NewChainState: %v", err)
		}

		// Initialize trie database manager
		trie_database.CreateTrieDatabaseManager(
			app.storageManager.GetStorageDatabaseTrie(),
			app.chainState.GetAccountStateDB())

		// Initialize blockchain
		blockchain.InitBlockChain(100, blockDatabase, app.storageManager)
	}

	return nil
}

// initProcessors initializes all processor components
func (app *App) initProcessors() {

}

// initRoutes initializes API routes
func (app *App) initRoutes() {
	r := map[string]func(t_network.Request) error{}
	limits := map[string]int{} // Request limits for each route

	// Create socket server
	handler := network.NewHandler(r, limits)
	app.socketServer, _ = network.NewSocketServer(
		nil,
		app.keyPair,
		app.connectionsManager,
		handler,
		app.config.Version,
	)
}

// ConnectToPeer connects to a peer node with the given address — now uses TCP
func (app *App) ConnectToPeer(peerAddr string) error {
	logger.Warn("ConnectToPeer called but libp2p is removed. Use ConnectTo() instead.")
	return nil
}

// Run starts the application and its components
func (app *App) Run() error {
	// Add startup delay
	time.Sleep(3 * time.Second)

	// Start socket server in a goroutine
	go app.socketServer.Listen(app.config.ConnectionAddress)
	go app.ConnectTo(app.config.Nodes.MasterAddress, p_common.MASTER_CONNECTION_TYPE)

	log.Println("App is running")
	storage.UpdateState(3)
	return nil
}

// Stop gracefully stops the application and releases resources
func (app *App) Stop() {
	logger.Info("Stopping app...")

	// Signal all background processes to stop
	app.cancel()

	// Stop server
	if app.socketServer != nil {
		app.socketServer.Stop()
	}

	// Close databases
	if trie_database.GetTrieDatabaseManager() != nil {
		trie_database.GetTrieDatabaseManager().CloseAllTrieDatabases()
	}
	if app.storageManager != nil {
		app.storageManager.CloseAll()
	}

	logger.Info("App stopped.")
}

// ConvertAddressMapToStringSlice converts a map of addresses to a slice of hex strings
func ConvertAddressMapToStringSlice(addressMap map[e_common.Address]struct{}) []string {
	addresses := make([]string, 0, len(addressMap))
	for addr := range addressMap {
		addresses = append(addresses, addr.Hex())
	}
	return addresses
}

// ConvertStringSliceToAddressMap converts a slice of hex strings to a map of addresses
func ConvertStringSliceToAddressMap(addresses []string) map[e_common.Address]struct{} {
	addressMap := make(map[e_common.Address]struct{}, len(addresses))
	for _, addrStr := range addresses {
		addr := e_common.HexToAddress(addrStr)
		addressMap[addr] = struct{}{}
	}
	return addressMap
}

func (app *App) ConnectTo(connectionAddress string, cType string) {
	masterConn := network.NewConnection(
		e_common.Address{},
		cType,
		nil,
	)
	masterConn.SetRealConnAddr(connectionAddress)

	connected := false
	for !connected {
		select {
		case <-app.ctx.Done(): // Exit if app is stopping
			logger.Info("Stopping connection attempt due to app shutdown.")
			return
		default:
			if err := masterConn.Connect(); err != nil {
				logger.Warn("Error connecting to master, retry in 5 seconds : %v : %v : %v", err, connectionAddress, cType)
				time.Sleep(5 * time.Second)
				continue
			}

			go app.socketServer.HandleConnection(masterConn)
			logger.Info("Connected to master successfully.")
			connected = true
		}
	}
}
