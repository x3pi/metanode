package pruning

import (
	"context"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type ChainState interface {
	GetCurrentEpoch() uint64
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
	
	// 2. We can add block / receipt deletion logic in PebbleDB here in the future
}
