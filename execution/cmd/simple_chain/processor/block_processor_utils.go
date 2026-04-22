// @title processor/block_processor_utils.go
// @markdown processor/block_processor_utils.go - Utility functions and helpers for block processor
package processor

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types"
)

// GenerateBlockData generates block data
// CRITICAL FORK-SAFETY: timestampSec should come from Rust consensus (commit_timestamp_ms / 1000)
// to ensure all nodes produce identical block hashes. Only use 0 for backward compatibility.
func GenerateBlockData(lastBlockHeader types.BlockHeader, validatorAddress common.Address, txs []types.Transaction, exrs []types.ExecuteSCResult, asRoot, stakeStatesRoot, receiptsRoot, txsRoot common.Hash, currentBlockNumber uint64, epoch uint64, timestampSec uint64, globalExecIndex uint64) (*block.Block, error) {
	// CRITICAL FORK-SAFETY: Use consensus timestamp from Rust instead of time.Now()
	// This ensures all nodes produce identical block hashes
	if timestampSec == 0 {
		panic("FORK-SAFETY PREVENTION: time.Now() fallback is forbidden! Timestamp must be provided by consensus.")
	}

	previousHash := lastBlockHeader.Hash()
	blockHeader := block.NewBlockHeader(previousHash, currentBlockNumber, asRoot, stakeStatesRoot, receiptsRoot, validatorAddress, timestampSec, txsRoot, epoch, globalExecIndex)
	transactionsHash := make([]common.Hash, len(txs))
	for i, tx := range txs {
		transactionsHash[i] = tx.Hash()
	}
	bl := block.NewBlock(blockHeader, transactionsHash, exrs)
	return bl, nil
}

// GenerateBlockDataReadOnly generates read-only block data
// CRITICAL FORK-SAFETY: timestampSec should come from Rust consensus (commit_timestamp_ms / 1000)
// to ensure all nodes produce identical block hashes. Only use 0 for backward compatibility.
func GenerateBlockDataReadOnly(validatorAddress common.Address, txs []types.Transaction, exrs []types.ExecuteSCResult, asRoot, stakeStatesRoot, receiptsRoot, txsRoot common.Hash, currentBlockNumber uint64, epoch uint64, timestampSec uint64, globalExecIndex uint64) (*block.Block, error) {
	// CRITICAL FORK-SAFETY: Use consensus timestamp from Rust instead of time.Now()
	// This ensures all nodes produce identical block hashes
	if timestampSec == 0 {
		panic("FORK-SAFETY PREVENTION: time.Now() fallback is forbidden! Timestamp must be provided by consensus.")
	}
	blockHeader := block.NewBlockHeader(common.Hash{}, currentBlockNumber, asRoot, stakeStatesRoot, receiptsRoot, validatorAddress, timestampSec, txsRoot, epoch, globalExecIndex)
	transactionsHash := make([]common.Hash, len(txs))
	for i, tx := range txs {
		transactionsHash[i] = tx.Hash()
	}
	bl := block.NewBlock(blockHeader, transactionsHash, exrs)
	return bl, nil
}

// PERFORMANCE OPTIMIZATION: TrieDB connection pool
// Avoid opening/closing databases repeatedly for better performance
var (
	trieDBPool  = make(map[string]*storage.ShardelDB)
	trieDBMutex sync.RWMutex
)

// getTrieDBFromPool returns a TrieDB connection from pool, creating if needed
func getTrieDBFromPool(databasePath string) (*storage.ShardelDB, error) {
	trieDBMutex.Lock()
	defer trieDBMutex.Unlock()

	if db, exists := trieDBPool[databasePath]; exists {
		return db, nil
	}

	// Create new connection
	db, err := storage.NewShardelDB(databasePath, 1, 2, config.ConfigApp.DBType, databasePath)
	if err != nil {
		return nil, err
	}

	err = db.Open()
	if err != nil {
		return nil, err
	}

	trieDBPool[databasePath] = db
	return db, nil
}

// closeTrieDBPool closes all connections in the pool
func closeTrieDBPool() {
	trieDBMutex.Lock()
	defer trieDBMutex.Unlock()

	for path, db := range trieDBPool {
		db.Close()
		delete(trieDBPool, path)
	}
}
