package main

import (
	"fmt"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
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

// createDatabase initializes and opens a local database.
func (app *App) createDatabase(subPath string, listenAddress string, backupPath string, dbType storage.DBType) (storage.Storage, error) {
	logger.Info("Initializing local ShardelDB at: %s", subPath)
	db, err := storage.NewShardelDB(
		config.JoinPathIfNotURL(app.config.Databases.RootPath, subPath),
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

// initStorageDatabases initializes and opens all required databases
func (app *App) initStorageDatabases() error {
	logger.Info("Start initStorageDatabases")

	// Helper function for creating and adding storage
	createAndAdd := func(storageName string, subPath string, dbType storage.DBType, addFunc func(storage.Storage) error) error {
		fullBackupPath := app.config.BackupPath + subPath

		var db storage.Storage
		var err error

		if app.config.StateBackend == "nomt" && (storageName == "account" || storageName == "stake" || storageName == "trie") {
			logger.Info("Using DummyStorage for %s because state_backend is nomt", storageName)
			db = storage.NewDummyStorage(fullBackupPath)
		} else {
			db, err = app.createDatabase(subPath, "", fullBackupPath, dbType)
			if err != nil {
				return fmt.Errorf("failed to create %s DB: %w", storageName, err)
			}
		}

		return addFunc(db)
	}

	// Account state database
	if err := createAndAdd("account", config.PathAccountState, app.config.DBType, app.storageManager.AddStorageAccount); err != nil {
		return err
	}

	// Receipts database
	if err := createAndAdd("receipts", config.PathReceipts, app.config.DBType, app.storageManager.AddStorageReceipt); err != nil {
		return err
	}

	// Transaction state database
	if err := createAndAdd("transaction state", config.PathTransactionState, app.config.DBType, app.storageManager.AddStorageTransaction); err != nil {
		return err
	}

	// Device key storage
	if err := createAndAdd("device key", config.PathBackupDeviceKey, app.config.DBType, app.storageManager.AddStorageBackupDeviceKey); err != nil {
		return err
	}

	// Smart contract database
	if err := createAndAdd("smart contract", config.PathSmartContractStorage, app.config.DBType, app.storageManager.AddStorageSmartContract); err != nil {
		return err
	}

	// Code database
	if err := createAndAdd("code", config.PathSmartContractCode, app.config.DBType, app.storageManager.AddStorageCode); err != nil {
		return err
	}

	// Trie database
	if err := createAndAdd("trie", config.PathTrie, app.config.DBType, app.storageManager.AddStorageDatabaseTrie); err != nil {
		return err
	}

	// Block database
	if err := createAndAdd("block", config.PathBlocks, app.config.DBType, app.storageManager.AddStorageBlock); err != nil {
		return err
	}

	// Mapping database
	if err := createAndAdd("mapping", config.PathMapping, app.config.DBType, app.storageManager.AddStorageMapping); err != nil {
		return err
	}

	// Stake database
	if err := createAndAdd("stake", config.PathStake, app.config.DBType, app.storageManager.AddStorageStake); err != nil {
		return err
	}

	logger.Info("End initStorageDatabases")
	return nil
}
