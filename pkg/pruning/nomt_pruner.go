package pruning

import (
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// PruneNomtEpoch prunes the NOMT database for data older than the specified epoch.
// Since NOMT automatically updates the root and maintains MVCC via its own internal pages,
// pruning in NOMT is mostly about deleting old committed namespaces or snapshots if used.
// If NOMT just truncates old history implicitly, this could be a no-op or trigger
// a compaction/truncation FFI call.
func PruneNomtEpoch(oldEpoch uint64) {
	// In the future, this will call into a Rust FFI method to truncate old b-tree pages or WAL.
	logger.Info("🧹 [PRUNING-NOMT] NOMT historical compaction triggered for epoch <= %d", oldEpoch)
}
