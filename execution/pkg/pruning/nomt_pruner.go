package pruning

import (
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// PruneNomtEpoch prunes the NOMT database for data older than the specified epoch.
func PruneNomtEpoch(oldEpoch uint64) {
	logger.Info("🧹 [PRUNING-NOMT] NOMT historical compaction triggered for epoch <= %d", oldEpoch)

	handles := trie.GetActiveNomtHandles()
	if len(handles) == 0 {
		logger.Debug("[PRUNING-NOMT] No active NOMT handles to prune")
		return
	}

	for ns, handle := range handles {
		logger.Info("🧹 [PRUNING-NOMT] Pruning namespace %s for epoch <= %d", ns, oldEpoch)
		if err := handle.StateDbPrune(oldEpoch); err != nil {
			logger.Error("❌ [PRUNING-NOMT] Failed to prune namespace %s: %v", ns, err)
		} else {
			logger.Info("✅ [PRUNING-NOMT] Successfully pruned namespace %s for epoch <= %d", ns, oldEpoch)
		}
	}
}
