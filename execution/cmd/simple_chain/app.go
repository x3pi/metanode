package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/routes"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
		"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/explorer"
	"github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/mining"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	"github.com/meta-node-blockchain/meta-node/pkg/pruning"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/tracing"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/types"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

// FreeFeeAddresses is a list of addresses that can transact without fees
var FreeFeeAddresses = map[e_common.Address]struct{}{}

// App defines the main application structure
type App struct {
	// Configuration
	config                 *config.SimpleChainConfig
	genesis                *config.GenesisData
	keyPair                *bls.KeyPair
	chainState             *blockchain.ChainState
	explorerSearch         *explorer.ExplorerSearchService
	miningService          *mining.MiningService
	explorerReadOnlySearch *explorer.ExplorerSearchService

	// Network components
	node               *node.HostNode
	connectionsManager t_network.ConnectionsManager
	messageSender      t_network.MessageSender
	socketServer       t_network.SocketServer

	// Storage components
	storageManager     *storage.StorageManager
	transactionStateDB *transaction_state_db.TransactionStateDB
	// Transaction handling
	transactionPool *transaction_pool.TransactionPool
	startLastBlock  types.Block

	// Event system
	eventSystem *filters.EventSystem

	// Processors
	connectionProcessor  *processor.ConnectionProcessor
	stateProcessor       *processor.StateProcessor
	transactionProcessor *processor.TransactionProcessor
	blockProcessor       *processor.BlockProcessor
	subscribeProcessor   *processor.SubscribeProcessor

	// Lifecycle management
	ctx    context.Context
	cancel context.CancelFunc

	// BLS Key Store (merged from RPC client)
	blsKeyStore *PrivateKeyStore

	// Async transaction queue (separated send/receive streams)
	txAsyncQueue *TxAsyncQueue

	// Performance monitoring
	handler          *network.Handler
	metricsCollector *MetricsCollector

	// Pruning
	pruningManager *pruning.PruningManager
}

// NewApp creates and initializes a new blockchain application
func NewApp(configFilePath string, logLevel int) (*App, error) {

	logger.SetConfig(&logger.LoggerConfig{
		Flag:    logLevel,
		Outputs: []*os.File{os.Stdout},
	})

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

	// Configure Xapian base path — combine RootPath + Databases.XapianPath, same pattern as other DBs.
	// e.g. RootPath="./sample/node0/data-write/data" + XapianPath="/xapian"
	//   → fullXapianPath = "./sample/node0/data-write/data/xapian"
	// Falls back to RootPath+"/xapian" if XapianPath not set.
	{
		xapianRelPath := app.config.Databases.XapianPath
		if xapianRelPath == "" {
			xapianRelPath = "/xapian"
		}
		fullXapianPath := config.JoinPathIfNotURL(app.config.Databases.RootPath, xapianRelPath)
		if mkErr := os.MkdirAll(fullXapianPath, 0755); mkErr != nil {
			return nil, fmt.Errorf(
				"[STARTUP FATAL] cannot create xapian path %q (RootPath=%q, XapianPath=%q): %w\n"+
					"  → Check: path permissions, disk space, parent directory exists",
				fullXapianPath, app.config.Databases.RootPath, xapianRelPath, mkErr,
			)
		}
		mvm.ConfigureXapianBasePath(fullXapianPath)
	}

	// Set state trie backend from config (must be before any trie creation)
	mt_trie.SetStateBackend(app.config.StateBackend)

	// Initialize NOMT database if backend is "nomt" (must be before any NewStateTrie calls)
	if mt_trie.GetStateBackend() == mt_trie.BackendNOMT {
		nomtPath := config.JoinPathIfNotURL(app.config.Databases.RootPath, "/nomt_db")
		// Apply config with sensible defaults
		commitConcurrency := app.config.NomtCommitConcurrency
		if commitConcurrency <= 0 {
			commitConcurrency = 4
		}
		pageCacheMB := app.config.NomtPageCacheMB
		if pageCacheMB <= 0 {
			pageCacheMB = 512
		}
		leafCacheMB := app.config.NomtLeafCacheMB
		if leafCacheMB <= 0 {
			leafCacheMB = 512
		}
		if err := mt_trie.InitNomtDB(nomtPath, commitConcurrency, pageCacheMB, leafCacheMB); err != nil {
			return nil, fmt.Errorf("failed to initialize NOMT database: %v", err)
		}
	}

	app.storageManager = storage.NewStorageManager()

	// Backup database
	backupDB, err := storage.NewShardelDB(
		config.JoinPathIfNotURL(app.config.BackupPath, app.config.Databases.Backup.Path),
		4, 2,
		app.config.DBType,
		config.JoinPathIfNotURL(app.config.BackupPath, app.config.Databases.Backup.Path),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup DB: %v", err)
	}
	if err := backupDB.Open(); err != nil {
		return nil, fmt.Errorf("failed to open backup DB: %v", err)
	}
	if err := app.storageManager.AddStorageBackupDb(backupDB); err != nil {
		return nil, err
	}

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

	if app.config.IsExplorer {
		dbPath := app.config.ExplorereDbPath
		explorerSearch, err := explorer.NewExplorerSearchService(dbPath, app.config.ExplorerQueueSize, app.config.ExplorerWorkerCount)
		if err != nil {
			return nil, fmt.Errorf("không thể khởi tạo ExplorerSearchService: %v", err)
		}
		app.explorerSearch = explorerSearch
		app.storageManager.SetExplorerSearchService(explorerSearch)

		dbPathReadOnly := app.config.ExplorereReadOnlyDbPath
		explorerReadOnlySearch, err := explorer.NewExplorerSearchService(dbPathReadOnly, app.config.ExplorerQueueSize, app.config.ExplorerWorkerCount)
		if err != nil {
			return nil, fmt.Errorf("không thể khởi tạo ExplorerSearchService: %v", err)
		}
		app.explorerReadOnlySearch = explorerReadOnlySearch

		// Store the read-only explorer service separately so callers that
		// expect the read-only instance don't get a nil receiver.
		app.storageManager.SetExplorerSearchServiceReadOnly(explorerReadOnlySearch)
	}

	if app.config.IsMining {
		// 1. Khởi tạo EthTransactionBroadcaster
		ethBroadcaster, err := NewEthTransactionBroadcaster(config.ConfigApp.ClientRpcUrl)
		if err != nil {
			logger.Error("Failed to initialize Ethereum transaction broadcaster: %v", err)
			os.Exit(1)
		}

		dbPath := app.config.MiningDbPath
		miningService, err := mining.NewMiningService(dbPath, config.ConfigApp.RewardSenderPrivateKey,
			ethBroadcaster)
		if err != nil {
			return nil, fmt.Errorf("không thể khởi tạo NewMiningService: %v", err)
		}
		app.miningService = miningService
		app.storageManager.SetMiningService(miningService)
	}

	// Initialize processors and routes
	app.initProcessors()
	app.initRoutes()

	// Initialize BLS key store (merged from RPC client) — only if configured
	if app.config.MasterPassword != "" && app.config.AppPepper != "" {
		pks, err := NewPrivateKeyStore(
			app.config.Databases.RootPath,
			app.config.MasterPassword,
			app.config.AppPepper,
		)
		if err != nil {
			logger.Warn("BLS key store initialization failed (non-fatal): %v", err)
		} else {
			app.blsKeyStore = pks
			logger.Info("BLS key store initialized successfully")
		}
	}

	// Initialize async transaction queue
	app.txAsyncQueue = NewTxAsyncQueue(app, 0) // 0 = auto-detect worker count

	return app, nil
}

// initProcessors initializes all processor components
func (app *App) initProcessors() {
	app.connectionProcessor, app.stateProcessor, app.transactionProcessor,
		app.blockProcessor, app.subscribeProcessor = processor.InitProcessors(
		app.connectionsManager,
		app.messageSender,
		app.transactionPool,
		FreeFeeAddresses,
		app.startLastBlock,
		app.keyPair.Address(),
		app.transactionStateDB,
		app.eventSystem,
		app.config.ServiceType,
		app.config.Databases.SmartContractStorage.Path,
		app.config.ChainId.String(),
		app.node,
		app.storageManager,
		app.chainState,
		app.config.GenesisFilePath,
		app.config,
	)

	// Set node references
	app.blockProcessor.SetNode(app.node)
}

// initRoutes initializes API routes
func (app *App) initRoutes() {
	r := map[string]func(t_network.Request) error{}
	limits := map[string]int{} // Request limits for each route

	routes.InitRoutes(
		r,
		limits,
		app.connectionProcessor,
		app.blockProcessor,
		app.stateProcessor,
		app.transactionProcessor,
		app.subscribeProcessor,
		app.config.ServiceType,
		app.messageSender,
	)

	// Node-level TCP routes (replacing libp2p stream protocols)
	if app.node != nil {
		r["BlockRequest"] = app.node.HandleBlockRequest
		r["BlockResponse"] = app.node.HandleBlockResponse // CRITICAL: Sub receives block data here
		r["BlockRangeRequest"] = app.node.HandleBlockRangeRequest
		r["FileTransfer"] = app.node.HandleFileTransfer
		r["FileRequest"] = app.node.HandleFileRequest
		r["FreeFeeRequest"] = app.node.HandleFreeFeeRequest
		r["FreeFeeResponse"] = app.node.HandleFreeFeeResponse
	}

	// Create socket server
	handler := network.NewHandler(r, limits)
	app.handler = handler
	app.socketServer, _ = network.NewSocketServer(
		nil,
		app.keyPair,
		app.connectionsManager,
		handler,
		app.config.Version,
	)
}

// Run starts the application and its components
func (app *App) Run() error {
	// Initialize tracing if enabled
	if app.config.TraceEnabled {
		logger.Info("Initializing OpenTelemetry Tracing with endpoint: %s", app.config.TraceEndpoint)
		err := tracing.InitTracer("meta-node", app.config.TraceEndpoint)
		if err != nil {
			logger.Error("Failed to initialize OpenTelemetry: %v", err)
		}
	}

	// Add startup delay
	time.Sleep(3 * time.Second)

	// === START PPROF SERVER ===
	go func() {
		// Use a unique port for each node based on its existing RPC port (e.g. 8747 -> 6747)
		// Or simply parse from logic. For simplicity, we can use the config's RpcPort if available,
		// but since we want exactly 606X, we'll try to listen.
		// A simple way to avoid collisions is to try ports 6060-6069.
		for port := 6060; port < 6070; port++ {
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			logger.Info("Attempting to start pprof server on %s", addr)
			if err := http.ListenAndServe(addr, nil); err == nil {
				break // Successfully bound
			}
		}
	}()

	// Start async transaction queue workers
	if app.txAsyncQueue != nil {
		app.txAsyncQueue.Start()
	}

	// Initialize and start pruning manager
	if app.chainState != nil {
		app.pruningManager = pruning.NewPruningManager(&app.config.Pruning, app.chainState)
		app.pruningManager.Start()
	}

	// Start socket server in a goroutine
	go func() {
		if err := app.socketServer.Listen(app.config.ConnectionAddress); err != nil {
			logger.Error("FATAL ERROR: SocketServer Listen failed: %v", err)
			os.Exit(1)
		}
	}()
	//
	fileLogger, _ := loggerfile.NewFileLogger(fmt.Sprintf("App__" + ".log"))

	go func(logger *loggerfile.FileLogger) {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			numGoroutines := runtime.NumGoroutine()

			logger.Info("=== MEMORY & GOROUTINE DEBUG ===")
			logger.Info("Heap Alloc: %dMB | Sys: %dMB | NumGC: %d | Goroutines: %d",
				m.Alloc/1024/1024, m.Sys/1024/1024, m.NumGC, numGoroutines)

			// Log top goroutines
			if numGoroutines > 1000 {
				logger.Info("⚠️  ALERT: Goroutine count > 1000! (current: %d)", numGoroutines)
			}
		}
	}(fileLogger)
	// Start appropriate services based on node type
	// We now always run as the primary unified execution engine (Master)
	app.blockProcessor.StartBackgroundWorkers()
	go app.blockProcessor.TxsProcessor2()

	// ── CRASH SAFETY: Periodic disk flush every 5 seconds ──────────────
	// Keeps NoSync for maximum write throughput but limits crash data loss
	// to ~5 seconds. Lost data is recoverable via peer sync.
	go app.periodicStorageFlusher()

	logger.Info("App is running")

	// Log validator information after app is fully initialized
	if app.chainState != nil {
		allValidators, startupErr := app.chainState.GetStakeStateDB().GetAllValidators()
		if startupErr != nil {
			logger.Error("Failed to get validators at startup: %v", startupErr)
		} else {
			logger.Info("Startup: Found %d validators in stake state DB", len(allValidators))
			for i, val := range allValidators {
				stake := val.TotalStakedAmount()
				logger.Info("  Validator %d: %s (name=%s, stake=%s, p2p=%s)",
					i+1, val.Address().Hex(), val.Name(), stake.String(), val.P2PAddress())
			}
			if len(allValidators) == 0 {
				logger.Warn("No validators found at startup! Rust will not be able to load committee.")
			}
		}
	} else {
		logger.Warn("chainState is nil at startup!")
	}

	storage.UpdateState(3)

	// ─── Final Readiness Signal ──────────────────────────────────────────
	peerCount := 0
	if app.node != nil {
		peerCount = len(app.node.Peers)
	}
	lastBlock := storage.GetLastBlockNumber()
	logger.Info("✅ [READY] %s fully operational: block=%d, peers=%d, service=%s",
		app.config.ServiceType,
		lastBlock, peerCount, app.config.ServiceType)

	return nil
}

// GetAccountStateTrie retrieves the cached account state Trie or parses and caches it if missing.
func (app *App) GetAccountStateTrie(stateRoot e_common.Hash) (mt_trie.StateTrie, error) {
	trieCacheKey := stateRoot.Hex()
	if app.blockProcessor != nil {
		if cachedTrie, ok := app.blockProcessor.GetTrieCache(trieCacheKey); ok {
			return cachedTrie, nil
		}
	}

	accountStateTrie, err := mt_trie.NewStateTrie(
		stateRoot,
		app.storageManager.GetStorageAccount(),
		true,
	)
	if err != nil {
		return nil, err
	}
	if app.blockProcessor != nil {
		app.blockProcessor.SetTrieCache(trieCacheKey, accountStateTrie)
	}
	return accountStateTrie, nil
}

// Stop gracefully stops the application and releases resources
func (app *App) Stop() {
	logger.Info("Stopping app...")

	// Signal all background processes to stop
	app.cancel()

	// Stop async transaction queue (drain pending txs)
	if app.txAsyncQueue != nil {
		app.txAsyncQueue.Stop()
	}

	// Stop pruning manager
	if app.pruningManager != nil {
		app.pruningManager.Stop()
	}

	// Stop server non-blocking so it doesn't block the critical DB flush if clients hang
	if app.socketServer != nil {
		go app.socketServer.Stop()
	}

	// ── CRASH SAFETY: Flush all data to disk before closing ──────────
	// Wait for background persistence queues to drain so trie states are written to DB
	if app.blockProcessor != nil {
		app.blockProcessor.StopWait()
	}

	// CRITICAL (Mar 2026): Force-sync lastBlock to disk BEFORE FlushAll.
	// This ensures the lastBlockHashKey survives even if the process is killed
	// during FlushAll. Without this, a crash can lose the block pointer and
	// cause genesis re-initialization on restart, wiping all state.
	if app.blockProcessor != nil && app.chainState != nil {
		lastBlock := app.blockProcessor.GetLastBlock()
		if lastBlock != nil {
			blockDB := app.chainState.GetBlockDatabase()
			if blockDB != nil {
				if err := blockDB.SaveLastBlockSync(lastBlock); err != nil {
					logger.Error("❌ [SHUTDOWN] Failed to sync-save lastBlock: %v", err)
				} else {
					logger.Info("✅ [SHUTDOWN] lastBlock #%d synced to disk (hash=%s, gei=%d)",
						lastBlock.Header().BlockNumber(),
						lastBlock.Header().Hash().Hex()[:18]+"...",
						lastBlock.Header().GlobalExecIndex())
				}
			}
		}
	}

	// This ensures zero data loss during clean shutdown.
	// Without this, PebbleDB Close() flushes each DB independently,
	// but LazyPebbleDB memory caches may not be written yet.
	if app.storageManager != nil {
		logger.Info("💾 [SHUTDOWN] Flushing all databases to disk...")
		flushStart := time.Now()
		if err := app.storageManager.FlushAll(); err != nil {
			logger.Error("❌ [SHUTDOWN] FlushAll error: %v", err)
		} else {
			logger.Info("✅ [SHUTDOWN] FlushAll completed in %v", time.Since(flushStart))
		}
	}

	// Close NOMT database (must be before closing other databases)
	mt_trie.CloseNomtDB()

	// Close databases
	if trie_database.GetTrieDatabaseManager() != nil {
		trie_database.GetTrieDatabaseManager().CloseAllTrieDatabases()
	}
	if app.storageManager != nil {
		app.storageManager.CloseAll()
	}

	if app.explorerSearch != nil {
		app.explorerSearch.Close()
	}

	if app.blsKeyStore != nil {
		app.blsKeyStore.Close()
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

// periodicStorageFlusher runs every 10 seconds to flush all PebbleDB memtables
// to disk. This works with the "NoSync + Periodic Flush" strategy:
//   - Normal writes use pebble.NoSync (zero overhead)
//   - This goroutine periodically forces memtable → SST flush
//   - Maximum data loss on crash: ~10 seconds (recoverable from peers)
//   - Clean shutdown (SIGTERM → Stop()) calls FlushAll() → zero data loss
//
// CHANGED (Mar 2026): Interval increased from 5s to 10s because LazyPebbleDB.Flush()
// now does a full memtable→SST flush (not just Go cache→memtable). Also writes
// periodic lastBlock backup file for crash recovery.
func (app *App) periodicStorageFlusher() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	logger.Info("🔄 [CRASH SAFETY] Started periodic storage flusher (interval=10s)")

	flushCount := 0
	for {
		select {
		case <-app.ctx.Done():
			logger.Info("🛑 [CRASH SAFETY] Periodic storage flusher stopped")
			return
		case <-ticker.C:
			if app.storageManager != nil {
				if err := app.storageManager.FlushAll(); err != nil {
					logger.Error("❌ [CRASH SAFETY] Periodic FlushAll error: %v", err)
				}
			}

			// Periodic lastBlock backup (every 3rd flush = every 30 seconds)
			flushCount++
			if flushCount%3 == 0 && app.blockProcessor != nil && app.chainState != nil {
				lastBlock := app.blockProcessor.GetLastBlock()
				if lastBlock != nil {
					blockDB := app.chainState.GetBlockDatabase()
					if blockDB != nil {
						if err := blockDB.SaveLastBlockBackup(lastBlock); err != nil {
							logger.Warn("⚠️  [CRASH SAFETY] Periodic lastBlock backup failed: %v", err)
						}
					}
				}
			}
		}
	}
}

// REMOVED: initExecutor() - Không còn cần thiết, đã có runSocketExecutor() trong blockProcessor
