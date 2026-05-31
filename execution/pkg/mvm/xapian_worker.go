package mvm

import (
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// StartXapianBackgroundWorker starts a background goroutine to periodically flush Xapian data to disk.
// This allows high TPS by separating the expensive disk I/O from the block execution pipeline.
func StartXapianBackgroundWorker(interval time.Duration) {
	logger.Info("🔄 [XAPIAN WORKER] Starting background commit worker with interval %v", interval)

	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			// logger.Debug("🔄 [XAPIAN WORKER] Committing all Xapian databases to disk...")
			CommitAllXapian()
		}
	}()
}
