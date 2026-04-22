package block

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	lastBlockHashKey common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastBlockHashKey")))
)

type BlockDatabase struct {
	db        storage.Storage
	bockBatch []byte
	backupDir string // Directory for lastBlock backup file (crash recovery)
}

func NewBlockDatabase(
	db storage.Storage,

) *BlockDatabase {
	return &BlockDatabase{
		db: db,
	}
}

// SetBackupDir sets the directory for lastBlock backup files.
// Must be called before SaveLastBlockBackup/LoadLastBlockBackup.
func (blockDatabase *BlockDatabase) SetBackupDir(dir string) {
	blockDatabase.backupDir = dir
}

func (blockDatabase *BlockDatabase) SetBlockBatch(batch []byte) {
	blockDatabase.bockBatch = batch
}

func (blockDatabase *BlockDatabase) GetBlockBatch() []byte {
	batch := blockDatabase.bockBatch
	blockDatabase.bockBatch = nil
	return batch
}

// GetDB returns the underlying ShardelDB instance.
func (blockDatabase *BlockDatabase) GetDB() storage.Storage {
	return blockDatabase.db
}

// SaveLastBlock saves the last block's hash to the database.
func (blockDatabase *BlockDatabase) SaveLastBlock(block types.Block) error {

	// Encode block to bytes
	blockBytes, err := block.Marshal()
	if err != nil {
		return err
	}

	var batch [][2][]byte

	batch = append(batch, [2][]byte{lastBlockHashKey.Bytes(), blockBytes})
	batch = append(batch, [2][]byte{block.Header().Hash().Bytes(), blockBytes})
	if err := blockDatabase.db.BatchPut(batch); err != nil {
		return err
	}
	if config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
		data, err := storage.SerializeBatch(batch)
		if err != nil {
			logger.Error(fmt.Sprintf("Error marshaling receipt: %v", err))
		}
		blockDatabase.SetBlockBatch(data)
	}
	return nil
}

// SaveLastBlockSync saves the last block with full synchronous disk flush.
// CRITICAL: This ensures the lastBlockHashKey survives crashes and SIGKILL.
// Used during clean shutdown and periodic checkpoints to guarantee restart
// finds the correct block instead of re-initializing genesis.
func (blockDatabase *BlockDatabase) SaveLastBlockSync(block types.Block) error {
	// First, do the normal batch save (writes to LazyPebbleDB memory cache)
	if err := blockDatabase.SaveLastBlock(block); err != nil {
		return fmt.Errorf("SaveLastBlock failed: %w", err)
	}
	// Then force-flush the underlying storage all the way to disk (SST files)
	if err := blockDatabase.db.Flush(); err != nil {
		return fmt.Errorf("storage flush failed: %w", err)
	}
	// Also write the file-based backup
	if err := blockDatabase.SaveLastBlockBackup(block); err != nil {
		logger.Warn("⚠️  [BLOCK-BACKUP] File backup failed (non-fatal): %v", err)
		// Don't return error — file backup is a best-effort fallback
	}
	return nil
}

// lastBlockBackupData is the JSON structure for the backup file.
type lastBlockBackupData struct {
	BlockNumber    uint64 `json:"block_number"`
	BlockHash      string `json:"block_hash"`
	AccountRoot    string `json:"account_root"`
	GlobalExecIdx  uint64 `json:"global_exec_index"`
	Epoch          uint64 `json:"epoch"`
	BlockBytes     []byte `json:"block_bytes"`
}

// SaveLastBlockBackup writes the last block to a JSON backup file.
// This provides a crash recovery fallback independent of PebbleDB.
func (blockDatabase *BlockDatabase) SaveLastBlockBackup(block types.Block) error {
	if blockDatabase.backupDir == "" {
		return nil // No backup directory configured
	}

	blockBytes, err := block.Marshal()
	if err != nil {
		return fmt.Errorf("marshal block: %w", err)
	}

	header := block.Header()
	data := lastBlockBackupData{
		BlockNumber:   header.BlockNumber(),
		BlockHash:     header.Hash().Hex(),
		AccountRoot:   header.AccountStatesRoot().Hex(),
		GlobalExecIdx: header.GlobalExecIndex(),
		Epoch:         header.Epoch(),
		BlockBytes:    blockBytes,
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}

	if err := os.MkdirAll(blockDatabase.backupDir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	backupPath := filepath.Join(blockDatabase.backupDir, "last_block_backup.json")
	tmpPath := backupPath + ".tmp"

	// Write to temp file first, then atomic rename (crash-safe)
	if err := os.WriteFile(tmpPath, jsonBytes, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, backupPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	logger.Debug("💾 [BLOCK-BACKUP] Saved lastBlock backup: block=#%d, hash=%s",
		header.BlockNumber(), header.Hash().Hex()[:18]+"...")
	return nil
}

// LoadLastBlockBackup attempts to recover the last block from the JSON backup file.
// Returns nil, nil if no backup exists (not an error).
func (blockDatabase *BlockDatabase) LoadLastBlockBackup() (types.Block, error) {
	if blockDatabase.backupDir == "" {
		return nil, nil
	}

	backupPath := filepath.Join(blockDatabase.backupDir, "last_block_backup.json")
	jsonBytes, err := os.ReadFile(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No backup file
		}
		return nil, fmt.Errorf("read backup: %w", err)
	}

	var data lastBlockBackupData
	if err := json.Unmarshal(jsonBytes, &data); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}

	if len(data.BlockBytes) == 0 {
		return nil, fmt.Errorf("empty block bytes in backup")
	}

	block := &Block{}
	if err := block.Unmarshal(data.BlockBytes); err != nil {
		return nil, fmt.Errorf("unmarshal block: %w", err)
	}

	logger.Info("📦 [BLOCK-BACKUP] Loaded block from backup file: block=#%d, hash=%s",
		data.BlockNumber, data.BlockHash[:18]+"...")
	return block, nil
}

// SaveBlockByHash saves block data ONLY under its hash key.
// Unlike SaveLastBlock, this does NOT update the global lastBlockHashKey pointer.
// Use this in sync handler to avoid racing with the consensus goroutine's SaveLastBlock.
func (blockDatabase *BlockDatabase) SaveBlockByHash(block types.Block) error {
	blockBytes, err := block.Marshal()
	if err != nil {
		return err
	}
	return blockDatabase.db.Put(block.Header().Hash().Bytes(), blockBytes)
}

func (blockDatabase *BlockDatabase) GetBlockByHash(blockHash common.Hash) (types.Block, error) {
	// Try to load the block from the database
	blockBytes, err := blockDatabase.db.Get(blockHash.Bytes())
	if err != nil {
		return nil, err
	}
	block := &Block{}

	err = block.Unmarshal(blockBytes)

	if err != nil {
		return nil, err
	}
	return block, nil
}

func (blockDatabase *BlockDatabase) GetBlockByHashFromDb(blockHash common.Hash) (types.Block, error) {
	// Try to load the block from the database
	blockBytes, err := blockDatabase.db.Get(blockHash.Bytes())
	if err != nil {
		return nil, err
	}
	block := &Block{}

	err = block.Unmarshal(blockBytes)

	if err != nil {
		return nil, err
	}

	return block, nil
}

// GetLastBlock retrieves the last block from the database.
// Falls back to backup file if the database key is missing.
func (blockDatabase *BlockDatabase) GetLastBlock() (types.Block, error) {
	bl, err := blockDatabase.GetBlockByHash(lastBlockHashKey)
	if err == nil {
		return bl, nil
	}

	// PRIMARY KEY MISSING — try to recover from backup file
	logger.Warn("⚠️  [BLOCK-DB] GetLastBlock from DB failed: %v. Trying backup file...", err)
	backupBlock, backupErr := blockDatabase.LoadLastBlockBackup()
	if backupErr != nil {
		logger.Error("❌ [BLOCK-DB] Backup recovery also failed: %v", backupErr)
		return nil, fmt.Errorf("lastBlock not in DB (%v) and backup recovery failed (%v)", err, backupErr)
	}
	if backupBlock == nil {
		return nil, err // No backup file exists
	}

	// Successfully recovered from backup — re-save to DB for next startup
	logger.Info("🔄 [BLOCK-DB] Recovered lastBlock #%d from backup file. Re-saving to database...",
		backupBlock.Header().BlockNumber())
	if saveErr := blockDatabase.SaveLastBlock(backupBlock); saveErr != nil {
		logger.Error("⚠️  [BLOCK-DB] Failed to re-save recovered block to DB: %v", saveErr)
	}

	return backupBlock, nil
}

// GetLastBlockFromDb retrieves the last block from the database (no backup fallback).
func (blockDatabase *BlockDatabase) GetLastBlockFromDb() (types.Block, error) {
	return blockDatabase.GetBlockByHashFromDb(lastBlockHashKey)
}

