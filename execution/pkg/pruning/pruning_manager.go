package pruning

import (
	"context"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/ethereum/go-ethereum/common"
)

type ChainState interface {
	GetCurrentEpoch() uint64
	GetEpochBoundaryBlock(epoch uint64) (uint64, bool)
}

// PruningManager manages historical data pruning for the node
type PruningManager struct {
	config     *config.PruningConfig
	chainState ChainState
	cancel     context.CancelFunc
	ctx        context.Context
	
	lastPrunedEpoch  uint64
	lastPrunedBlock  uint64
}

func NewPruningManager(cfg *config.PruningConfig, cs ChainState) *PruningManager {
	if cfg == nil {
		cfg = &config.PruningConfig{Mode: "archive"} // Default to archive
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &PruningManager{
		config:     cfg,
		chainState: cs,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start begins background pruning tasks if enabled
func (pm *PruningManager) Start() {
	if pm.config.Mode == "archive" {
		logger.Info("🧹 [PRUNING] Pruning disabled (archive mode)")
		return
	}

	logger.Info("🧹 [PRUNING] Starting background pruner (mode: %s, epochs_to_keep: %d)", pm.config.Mode, pm.config.EpochsToKeep)

	go pm.runLoop()
}

// Stop gracefully shuts down the pruning manager
func (pm *PruningManager) Stop() {
	pm.cancel()
}

func (pm *PruningManager) runLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			logger.Info("🧹 [PRUNING] Pruner stopped")
			return
		case <-ticker.C:
			pm.pruneTick()
		}
	}
}

func (pm *PruningManager) pruneTick() {
	if pm.chainState == nil {
		return
	}
	
	currentEpoch := pm.chainState.GetCurrentEpoch()
	
	// Check if we need to prune old epochs
	if pm.config.EpochsToKeep > 0 && currentEpoch > uint64(pm.config.EpochsToKeep) {
		targetPruneEpoch := currentEpoch - uint64(pm.config.EpochsToKeep)
		if targetPruneEpoch > pm.lastPrunedEpoch {
			pm.pruneEpoch(targetPruneEpoch)
			pm.lastPrunedEpoch = targetPruneEpoch
		}
	}
}

func (pm *PruningManager) pruneEpoch(epoch uint64) {
	logger.Info("🧹 [PRUNING] Triggering prune for data older than epoch %d", epoch)
	
	// 1. Snapshot / rollback old NOMT namespaces
	PruneNomtEpoch(epoch)
	
	// 2. Prune Block and Receipt DB
	boundaryBlock, ok := pm.chainState.GetEpochBoundaryBlock(epoch)
	if !ok {
		logger.Warn("🧹 [PRUNING] Cannot find boundary block for epoch %d, skipping DB prune", epoch)
		return
	}

	bc := blockchain.GetBlockChainInstance()
	if bc == nil {
		return
	}

	// Calculate range to prune
	lastPruned := bc.GetLastPrunedBlockNumber()
	if lastPruned >= boundaryBlock {
		return // Already pruned
	}

	startBlock := lastPruned + 1
	if startBlock == 1 {
		// Prune genesis block if starting from 1 (optional, usually safe to keep block 0)
		startBlock = 1
	}

	var blockDB interface {
		DeleteSystemTransactions(uint64) error
		DeleteBlockByHash(common.Hash) error
	}
	if chainState, ok := pm.chainState.(*blockchain.ChainState); ok {
		blockDB = chainState.GetBlockDatabase()
	}
	if blockDB == nil {
		return
	}

	// Init receipts interface for batch deletion
	var rcpDb interface{ BatchDeleteReceipts([]common.Hash) error }
	
	if chainState, ok := pm.chainState.(*blockchain.ChainState); ok {
		storageManager := chainState.GetStorageManager()
		if storageManager != nil {
			receiptsInterface, err := receipt.NewReceipts(storageManager.GetStorageReceipt())
			if err == nil {
				if r, ok := receiptsInterface.(interface{ BatchDeleteReceipts([]common.Hash) error }); ok {
					rcpDb = r
				}
			}
		}
	}

	var prunedCount int
	for bNum := startBlock; bNum <= boundaryBlock; bNum++ {
		hash, ok := bc.GetBlockHashByNumber(bNum)
		if !ok {
			continue
		}
		
		blk := bc.GetBlock(hash)
		if blk != nil {
			// Delete Receipts
			if rcpDb != nil {
				txHashes := blk.Transactions()
				if err := rcpDb.BatchDeleteReceipts(txHashes); err != nil {
					logger.Warn("🧹 [PRUNING] Failed to batch delete receipts for block %d: %v", bNum, err)
				}
			}
			
			// Delete System Transactions
			blockDB.DeleteSystemTransactions(bNum)
			
			// Delete Block
			blockDB.DeleteBlockByHash(hash)
		}
		
		// Delete Mapping
		bc.DeleteBlockHashMapping(bNum)
		prunedCount++
		
		// Prevent CPU/IO hogging by sleeping briefly every 100 blocks
		if prunedCount%100 == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Update last pruned state
	if err := bc.SetLastPrunedBlockNumber(boundaryBlock); err != nil {
		logger.Error("🧹 [PRUNING] Failed to save last pruned block number: %v", err)
	}
	
	logger.Info("🧹 [PRUNING] Successfully pruned DB up to block %d (epoch %d) - %d blocks removed", boundaryBlock, epoch, prunedCount)
}
