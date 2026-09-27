package main

import (
	"fmt"
	"log"
	"path/filepath"
	
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/cmd/parent_chain/processor"
)

type App struct {
	configPath     string
	rustConfigPath string
	dataDir        string
	
	store          parentchain.Store
	blockProcessor *processor.BlockProcessor
}

func NewApp(configPath, rustConfigPath, dataDir string) (*App, error) {
	dbPath := filepath.Join(dataDir, "parentchain_db")
	store, err := parentchain.NewDBStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open DBStore: %w", err)
	}
	bp := processor.NewBlockProcessor(store)
	
	return &App{
		configPath:     configPath,
		rustConfigPath: rustConfigPath,
		dataDir:        dataDir,
		store:          store,
		blockProcessor: bp,
	}, nil
}

func (a *App) Start() error {
	log.Println("Initializing FFI Bridge...")
	
	// Create RequestHandler with minimal callbacks
	reqHandler := &executor.RequestHandler{}
	
	// Register the block processor to receive ExecutableBlocks
	blockQueue := a.blockProcessor.GetQueue()
	
	if err := executor.InitFFIBridge(a.rustConfigPath, a.dataDir, reqHandler, blockQueue); err != nil {
		return fmt.Errorf("failed to init FFI bridge: %w", err)
	}
	
	a.blockProcessor.Start()
	
	return nil
}

func (a *App) Stop() {
	if a.blockProcessor != nil {
		a.blockProcessor.Stop()
	}
}
