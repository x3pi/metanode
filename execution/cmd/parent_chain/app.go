package main

import (
	"fmt"
	"log"
	"path/filepath"
	
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/cmd/parent_chain/processor"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

type App struct {
	configPath     string
	rustConfigPath string
	dataDir        string
	httpAddr       string

	store          parentchain.Store
	blockProcessor *processor.BlockProcessor
	txBatcher      *processor.TxBatcher
	httpServer     *parentchain.HTTPServer
}

func NewApp(configPath, rustConfigPath, dataDir, httpAddr string) (*App, error) {
	dbPath := filepath.Join(dataDir, "parentchain_db")
	store, err := parentchain.NewDBStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open DBStore: %w", err)
	}
	tb := processor.NewTxBatcher(1000)
	httpServer := parentchain.NewHTTPServer(store, tb.Chan())
	bp := processor.NewBlockProcessor(store, httpServer.NotifyTxResult)

	return &App{
		configPath:     configPath,
		rustConfigPath: rustConfigPath,
		dataDir:        dataDir,
		httpAddr:       httpAddr,
		store:          store,
		blockProcessor: bp,
		txBatcher:      tb,
		httpServer:     httpServer,
	}, nil
}

func (a *App) Start() error {
	log.Println("Initializing FFI Bridge...")

	// Create RequestHandler with minimal callbacks
	reqHandler := &executor.RequestHandler{}

	// Register the block processor to receive ExecutableBlocks
	blockQueue := a.blockProcessor.GetQueue()

	storage.SetBlockchainInitDone()
	
	if err := executor.InitFFIBridge(a.rustConfigPath, a.dataDir, reqHandler, blockQueue); err != nil {
		return fmt.Errorf("failed to init FFI bridge: %w", err)
	}

	a.blockProcessor.Start()
	a.txBatcher.Start()

	log.Printf("Starting Parent Chain RPC server on %s", a.httpAddr)
	go func() {
		if err := a.httpServer.Start(a.httpAddr); err != nil {
			log.Fatalf("Parent Chain RPC server exited: %v", err)
		}
	}()

	return nil
}

func (a *App) Stop() {
	if a.txBatcher != nil {
		a.txBatcher.Stop()
	}
	if a.blockProcessor != nil {
		a.blockProcessor.Stop()
	}
}
