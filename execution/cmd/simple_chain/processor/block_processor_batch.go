// @title processor/block_processor_batch.go
// @markdown processor/block_processor_batch.go - Batch block creation and storage operations
package processor

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/types"
)

// GenerateBlocksInBatch generates blocks in batch
func (bp *BlockProcessor) GenerateBlocksInBatch() {
	const txBatchSize = TxBatchSize
	const blockInBatch = BlockInBatch
	const maxWaitTime = MaxWaitTime

	var accumulatedResults []tx_processor.ProcessResult
	var totalTxs int
	timer := time.NewTimer(maxWaitTime)
	defer timer.Stop()

	for {
		select {
		case result := <-bp.transactionProcessor.ProcessResultChan:
			accumulatedResults = append(accumulatedResults, result)
			totalTxs += len(result.Transactions)
			if totalTxs >= txBatchSize {
				batchID := uuid.New().String()
				go bp.createBlockBatch(accumulatedResults, totalTxs, blockInBatch, batchID)
				accumulatedResults = nil
				totalTxs = 0
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(maxWaitTime)
			}
		case <-timer.C:
			if totalTxs > 0 {
				batchID := uuid.New().String()
				go bp.createBlockBatch(accumulatedResults, totalTxs, blockInBatch, batchID)
				accumulatedResults = nil
				totalTxs = 0
			}
			timer.Reset(maxWaitTime)
		}
	}
}

// createBlockBatch creates a batch of blocks
func (bp *BlockProcessor) createBlockBatch(results []tx_processor.ProcessResult, totalTxs int, blockCount int, batchID string) {
	// Only log when batch has many transactions
	if totalTxs > 5000 {
		logger.Info("TPS_BATCH_START: BatchID=%s, TotalTxs=%d", batchID, totalTxs)
	}
	batchStartTime := time.Now()
	bp.ProcessedVirtualTxCount.Add(uint64(totalTxs))

	mergedResult := tx_processor.ProcessResult{
		Transactions:     make([]types.Transaction, 0, totalTxs),
		Receipts:         make([]types.Receipt, 0, totalTxs),
		ExecuteSCResults: make([]types.ExecuteSCResult, 0, totalTxs),
	}
	for _, r := range results {
		mergedResult.Transactions = append(mergedResult.Transactions, r.Transactions...)
		mergedResult.Receipts = append(mergedResult.Receipts, r.Receipts...)
		mergedResult.ExecuteSCResults = append(mergedResult.ExecuteSCResults, r.ExecuteSCResults...)
		if r.Root != (common.Hash{}) {
			mergedResult.Root = r.Root
		}
	}

	txsPerBlock := (totalTxs + blockCount - 1) / blockCount
	actualBlocksToCreate := 0
	for i := 0; i < blockCount; i++ {
		if i*txsPerBlock >= totalTxs {
			break
		}
		actualBlocksToCreate++
	}

	if actualBlocksToCreate == 0 {
		return
	}

	blockNumberRange := uint64(actualBlocksToCreate)
	startBlockNumber := bp.nextBlockNumber.Add(blockNumberRange) - blockNumberRange

	var wg sync.WaitGroup
	wg.Add(actualBlocksToCreate)

	// Log number of goroutines to be created
	logger.Info("createBlockBatch: Creating %d goroutines to create blocks (batchID: %s)", actualBlocksToCreate, batchID)

	// Check channel capacity
	channelLen := len(bp.createdBlocksChan)
	channelCap := cap(bp.createdBlocksChan)
	if channelLen > channelCap*80/100 {
		logger.Warn("createBlockBatch: createdBlocksChan nearly full %d/%d, may cause goroutine blocking", channelLen, channelCap)
	}

	// Worker pool to limit concurrent goroutines (prevent goroutine leak)
	const maxConcurrentWorkers = MaxConcurrentWorkers
	semaphore := make(chan struct{}, maxConcurrentWorkers)

	for i := 0; i < actualBlocksToCreate; i++ {
		startIdx := i * txsPerBlock
		endIdx := startIdx + txsPerBlock
		if endIdx > totalTxs {
			endIdx = totalTxs
		}
		chunkResult := tx_processor.ProcessResult{
			Transactions:     mergedResult.Transactions[startIdx:endIdx],
			Receipts:         mergedResult.Receipts[startIdx:endIdx],
			ExecuteSCResults: mergedResult.ExecuteSCResults[startIdx:endIdx],
			Root:             mergedResult.Root,
		}
		currentBlockNumber := startBlockNumber + uint64(i)

		// Acquire semaphore before creating goroutine
		semaphore <- struct{}{}
		go func(result tx_processor.ProcessResult, blockNum uint64, id string) {
			// Release semaphore when goroutine ends
			defer func() { <-semaphore }()
			// CRITICAL FIX: Always call wg.Done() and catch panic
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("!!! PANIC OCCURRED !!! Block creation goroutine %d crashed with serious error: %v", blockNum, r)
				}
			}()

			newBlock := bp.createBlockFromResults(result, blockNum, 0, false, id, 0, 0)
			select {
			case bp.createdBlocksChan <- newBlock:
				// Block sent successfully
			default:
				logger.Warn("WARNING: createdBlocksChan full! stateCommitter may be slow. Block creation goroutine %d will block.", blockNum)
				bp.createdBlocksChan <- newBlock // Wait until space available
			}
		}(chunkResult, currentBlockNumber, batchID)
	}

	wg.Wait()
	// RACE FIX: Clear AccountBatch AFTER all goroutines have snapshotted it.
	// Previously, GetAccountBatch() used clear-after-read, causing only the first
	// goroutine to get the trie nodes; all others got nil → go-sub missing account state.
	bp.chainState.GetAccountStateDB().ClearAccountBatch()
	mvm.CallClearAllStateInstances()
	trie_database.GetTrieDatabaseManager().ClearAllTrieDatabases()
	logger.Info("createBlockBatch: Completed, all %d goroutines finished (batchID: %s)", actualBlocksToCreate, batchID)
	// Only log when batch has many transactions
	if totalTxs > 5000 {
		logger.Info("TPS_BATCH_END: BatchID=%s, Duration=%s", batchID, time.Since(batchStartTime))
	}
}

// applyBlockBatch applies a batch of blocks
func (bp *BlockProcessor) applyBlockBatch(blockBatch []*storage.BackUpDb) error {
	aggregatedBatches := make(map[string][][2][]byte)
	allFullDbLogs := []map[string][]byte{}
	allTrieDbBatches := make(map[string][][2][]byte)

	batchDataFields := map[string]func(*storage.BackUpDb) []byte{
		"Block":         func(db *storage.BackUpDb) []byte { return db.BockBatch },
		"Account":       func(db *storage.BackUpDb) []byte { return db.AccountBatch },
		"Code":          func(db *storage.BackUpDb) []byte { return db.CodeBatchPut },
		"SmartContract": func(db *storage.BackUpDb) []byte { return db.SmartContractBatch },
		"SC Storage":    func(db *storage.BackUpDb) []byte { return db.SmartContractStorageBatch },
		"Receipt":       func(db *storage.BackUpDb) []byte { return db.ReceiptBatchPut },
		"Transaction":   func(db *storage.BackUpDb) []byte { return db.TxBatchPut },
		"StakeState":    func(db *storage.BackUpDb) []byte { return db.StakeState },
	}

	for _, backupDB := range blockBatch {
		for name, getData := range batchDataFields {
			data := getData(backupDB)
			if len(data) > 0 {
				deserialized, err := storage.DeserializeBatch(data)
				if err != nil {
					return fmt.Errorf("error deserializing batch '%s' for block %d: %w", name, backupDB.BockNumber, err)
				}
				aggregatedBatches[name] = append(aggregatedBatches[name], deserialized...)
			}
		}

		allFullDbLogs = append(allFullDbLogs, backupDB.FullDbLogs...)

		for key, value := range backupDB.TrieDatabaseBatchPut {
			if len(value) > 0 {
				deserialized, err := storage.DeserializeBatch(value)
				if err != nil {
					return fmt.Errorf("error deserializing TrieDB batch '%s' for block %d: %w", key, backupDB.BockNumber, err)
				}
				allTrieDbBatches[key] = append(allTrieDbBatches[key], deserialized...)
			}
		}
	}

	storages := map[string]storage.Storage{
		"Block":         bp.storageManager.GetStorageBlock(),
		"Account":       bp.storageManager.GetStorageAccount(),
		"Code":          bp.storageManager.GetStorageCode(),
		"SmartContract": bp.storageManager.GetStorageSmartContract(),
		"SC Storage":    bp.storageManager.GetStorageSmartContract(),
		"Receipt":       bp.storageManager.GetStorageReceipt(),
		"Transaction":   bp.storageManager.GetStorageTransaction(),
		"StakeState":    bp.storageManager.GetStorageStake(),
	}

	// PERFORMANCE OPTIMIZATION: Intercept NOMT batches before they hit PebbleDB
	logger.Info("🔧 [Batch] ApplyNomtReplicationBatches START (batch_count=%d)", len(aggregatedBatches))
	nomtStart := time.Now()

	if err := p_trie.ApplyNomtReplicationBatches(aggregatedBatches); err != nil {
		return fmt.Errorf("error replicating NOMT batches: %w", err)
	}
	logger.Info("🔧 [Batch] ApplyNomtReplicationBatches DONE in %v", time.Since(nomtStart))

	// PERFORMANCE OPTIMIZATION: Parallel storage operations
	// Process storage writes in parallel to reduce I/O bottleneck
	var wg sync.WaitGroup
	type storageResult struct {
		name string
		err  error
	}
	resultChan := make(chan storageResult, len(storages))

	for name, storageDb := range storages {
		if combinedBatch, ok := aggregatedBatches[name]; ok && len(combinedBatch) > 0 {
			wg.Add(1)
			go func(storageName string, db storage.Storage, batch [][2][]byte) {
				defer wg.Done()
				err := db.BatchPut(batch)
				resultChan <- storageResult{name: storageName, err: err}
			}(name, storageDb, combinedBatch)
		}
	}

	// Wait for all parallel operations to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Check results
	for result := range resultChan {
		if result.err != nil {
			return fmt.Errorf("error writing aggregated batch '%s': %w", result.name, result.err)
		}
	}

	// PERFORMANCE OPTIMIZATION: Use TrieDB connection pool
	for key, combinedBatch := range allTrieDbBatches {
		if len(combinedBatch) > 0 {
			databasePath := filepath.Join(config.ConfigApp.Databases.RootPath+config.ConfigApp.Databases.Trie.Path, key)
			database, err := getTrieDBFromPool(databasePath)
			if err != nil {
				return fmt.Errorf("error getting TrieDB connection for '%s': %w", key, err)
			}
			err = database.BatchPut(combinedBatch)
			if err != nil {
				return fmt.Errorf("error writing aggregated batch 'TrieDB-%s': %w", key, err)
			}
			// NOTE: Connection stays open in pool, will be closed by closeTrieDBPool()
		}
	}

	logger.Warn("APPLY_BATCH_DEBUG: allFullDbLogs count=%d", len(allFullDbLogs))
	for _, logMap := range allFullDbLogs {
		logger.Warn("APPLY_BATCH_DEBUG: Replaying FullDbLogs for map with %d entries", len(logMap))
		mvm.CallReplayFullDbLogs(logMap)
	}

	// CRITICAL FORK-SAFETY: Clear C++ EVM State Cache after applying network blocks.
	// When Go applies Account/SmartContract batches directly to PebbleDB during catch-up,
	// the C++ EVM remains unaware and keeps stale nonces/balances in its memory cache.
	// Clearing it forces the next EVM transaction to fetch fresh state from Go DB,
	// preventing 'nonce mismatch' rejections and stateRoot divergence.
	mvm.ClearAllMVMApi()
	mvm.ClearAllProtectedMVMApi()
	mvm.CallClearAllStateInstances()
	trie_database.GetTrieDatabaseManager().ClearAllTrieDatabases()

	// FORK-SAFETY (Apr 2026): Clear Go-side AccountStateDB and StakeStateDB read caches.
	// applyBlockBatch writes directly to NOMT/PebbleDB, bypassing AccountStateDB.
	// Without this, loadedAccounts and lruCache retain stale pre-sync data,
	// causing RPC queries (eth_getBalance, mtn_getAccountState) to return old values
	// on Sub nodes — making them appear diverged from Master.
	bp.chainState.InvalidateAllState()

	return nil
}

// applyBlockBatchForMapping applies block batch for mapping
func (bp *BlockProcessor) applyBlockBatchForMapping(blockBatch []*storage.BackUpDb) error {
	aggregatedBatches := make(map[string][][2][]byte)
	allFullDbLogs := []map[string][]byte{}
	allTrieDbBatches := make(map[string][][2][]byte)

	batchDataFields := map[string]func(*storage.BackUpDb) []byte{
		"Mapping": func(db *storage.BackUpDb) []byte { return db.MapppingBatch },
	}

	for _, backupDB := range blockBatch {
		for name, getData := range batchDataFields {
			data := getData(backupDB)
			if len(data) > 0 {
				deserialized, err := storage.DeserializeBatch(data)
				if err != nil {
					return fmt.Errorf("error deserializing batch '%s' for block %d: %w", name, backupDB.BockNumber, err)
				}
				aggregatedBatches[name] = append(aggregatedBatches[name], deserialized...)
			}
		}

		allFullDbLogs = append(allFullDbLogs, backupDB.FullDbLogs...)

		for key, value := range backupDB.TrieDatabaseBatchPut {
			if len(value) > 0 {
				deserialized, err := storage.DeserializeBatch(value)
				if err != nil {
					return fmt.Errorf("error deserializing TrieDB batch '%s' for block %d: %w", key, backupDB.BockNumber, err)
				}
				allTrieDbBatches[key] = append(allTrieDbBatches[key], deserialized...)
			}
		}
	}

	storages := map[string]storage.Storage{
		"Mapping": bp.storageManager.GetStorageMapping(),
	}

	for name, storageDb := range storages {
		if combinedBatch, ok := aggregatedBatches[name]; ok && len(combinedBatch) > 0 {
			if err := storageDb.BatchPut(combinedBatch); err != nil {
				return fmt.Errorf("error writing aggregated batch '%s': %w", name, err)
			}
		}
	}

	for key, combinedBatch := range allTrieDbBatches {
		if len(combinedBatch) > 0 {
			databasePath := filepath.Join(config.ConfigApp.Databases.RootPath+config.ConfigApp.Databases.Trie.Path, key)
			database, err := getTrieDBFromPool(databasePath)
			if err != nil {
				return fmt.Errorf("error getting TrieDB connection for '%s': %w", key, err)
			}
			err = database.BatchPut(combinedBatch)
			if err != nil {
				return fmt.Errorf("error writing aggregated batch 'TrieDB-%s': %w", key, err)
			}
			// NOTE: Connection stays open in pool, will be closed by closeTrieDBPool()
		}
	}

	for _, logMap := range allFullDbLogs {
		mvm.CallReplayFullDbLogs(logMap)
	}

	return nil
}
