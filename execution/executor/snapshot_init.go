package executor

import (
	"path/filepath"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// Global snapshot manager instance
var globalSnapshotManager *SnapshotManager

// InitSnapshotSystem khởi tạo hệ thống snapshot dựa trên config
// Gọi 1 lần duy nhất khi khởi động node
// chainState dùng để tự phát hiện epoch transition (cho cả SUB-WRITE node)
func InitSnapshotSystem(cfg *config.SimpleChainConfig, chainState *blockchain.ChainState) *SnapshotManager {
	if !cfg.SnapshotEnabled || string(cfg.ServiceType) != "MASTER" {
		if string(cfg.ServiceType) != "MASTER" && cfg.SnapshotEnabled {
			logger.Warn("📸 [SNAPSHOT] Snapshot system DISABLED forcefully on SUB node to prevent concurrent write corruption")
		} else {
			logger.Info("📸 [SNAPSHOT] Snapshot system DISABLED — but log rotation callback will be registered")
		}

		// Tạo lightweight SnapshotManager (disabled) chỉ để đăng ký callback cho log rotation
		sm := &SnapshotManager{enabled: false}

		// Lấy epoch hiện tại từ chainState
		if chainState != nil {
			currentEpoch := chainState.GetCurrentEpoch()
			loggerfile.SetGlobalEpoch(currentEpoch)
			logger.Info("📸 [SNAPSHOT] Initial epoch from chainState: %d", currentEpoch)
		}

		// Đăng ký callback — log rotation LUÔN chạy
		storage.SetBlockCommitCallback(func(blockNumber uint64) {
			if chainState == nil {
				return
			}
			currentEpoch := chainState.GetCurrentEpoch()
			if currentEpoch > loggerfile.GetGlobalEpoch() {
				logger.Info("🔄 [LOG] Epoch change detected: %d → %d, rotating log files...",
					loggerfile.GetGlobalEpoch(), currentEpoch)
				logger.RotateToEpoch(currentEpoch)
				go func() {
					cleaner := loggerfile.GetGlobalLogCleaner()
					if cleaner == nil {
						return
					}
					if err := cleaner.CleanOldEpochLogs(); err != nil {
						logger.Warn("🧹 [LOG-CLEANER] Failed to clean old epoch logs: %v", err)
					}
				}()
			}
		})
		return sm
	}

	dataDir := cfg.Databases.RootPath
	snapshotDir := cfg.Databases.SnapshotPath
	if snapshotDir == "" {
		snapshotDir = "./snapshot_data"
	}

	blocksDelay := cfg.SnapshotBlocksDelay
	if blocksDelay <= 0 {
		blocksDelay = 20
	}

	serverPort := cfg.SnapshotServerPort
	if serverPort <= 0 {
		serverPort = 8700
	}

	// Tạo SnapshotManager
	sm := NewSnapshotManager(dataDir, snapshotDir, 2, blocksDelay)
	sm.SetSnapshotFrequency(cfg.SnapshotFrequencyBlocks)
	globalSnapshotManager = sm

	// Cấu hình snapshot method
	method := cfg.SnapshotMethod
	if method == "" {
		method = "hardlink" // Default
	}
	sm.snapshotMethod = method

	// Cấu hình rsync/hybrid source dir (thư mục cần snapshot)
	if method == "rsync" || method == "hybrid" {
		sm.snapshotSourceDir = cfg.SnapshotSourceDir
		if sm.snapshotSourceDir == "" {
			// Default: parent directory of RootPath (vd: data-write)
			sm.snapshotSourceDir = filepath.Dir(cfg.Databases.RootPath)
			logger.Warn("📸 [SNAPSHOT] snapshot_source_dir not set, using parent of RootPath: %s", sm.snapshotSourceDir)
		}
		logger.Info("📸 [SNAPSHOT] Source dir config: source_dir=%s", sm.snapshotSourceDir)
	}

	// Lấy epoch hiện tại từ chain state để khởi tạo lastSeenEpoch
	if chainState != nil {
		currentEpoch := chainState.GetCurrentEpoch()
		sm.SetLastSeenEpoch(currentEpoch)
		loggerfile.SetGlobalEpoch(currentEpoch)
		logger.Info("📸 [SNAPSHOT] Initial epoch from chainState: %d", currentEpoch)

		// Register synchronous storage flush callback
		if storageMgr := chainState.GetStorageManager(); storageMgr != nil {
			sm.SetForceFlushCallback(func() error {
				return storageMgr.FlushAll()
			})
			logger.Info("📸 [SNAPSHOT] Registered synchronous storage flush callback")

			// Register PebbleDB checkpoint callback for atomic snapshots
			sm.SetCheckpointCallback(func(destPath string) error {
				return storageMgr.CheckpointAll(destPath)
			})
			logger.Info("📸 [SNAPSHOT] Registered PebbleDB checkpoint callback")
			
			// Register NOMT snapshot callback for native atomic snapshots
			sm.SetNomtSnapshotCallback(func(destPath string, useReflink bool) error {
				return mt_trie.SnapshotAllNomtDBs(destPath, useReflink)
			})
			logger.Info("📸 [SNAPSHOT] Registered NOMT native snapshot callback")
		}
	}

	// Đăng ký callback vào storage.UpdateLastBlockNumber
	// Mỗi khi block mới commit → kiểm tra epoch thay đổi
	storage.SetBlockCommitCallback(func(blockNumber uint64) {
		if chainState == nil {
			return
		}

		currentEpoch := chainState.GetCurrentEpoch()

		// === Log rotation — LUÔN chạy, không phụ thuộc snapshot ===
		// So sánh với global epoch hiện tại để detect thay đổi
		if currentEpoch > loggerfile.GetGlobalEpoch() {
			logger.Info("🔄 [LOG] Epoch change detected: %d → %d, rotating log files...",
				loggerfile.GetGlobalEpoch(), currentEpoch)
			logger.RotateToEpoch(currentEpoch)
			go func() {
				cleaner := loggerfile.GetGlobalLogCleaner()
				if cleaner == nil {
					return
				}
				if err := cleaner.CleanOldEpochLogs(); err != nil {
					logger.Warn("🧹 [LOG-CLEANER] Failed to clean old epoch logs: %v", err)
				}
			}()
		}

		// === Snapshot — chỉ chạy khi enabled ===
		if sm.DetectEpochChange(currentEpoch, chainState) {
			logger.Info("📸 [SNAPSHOT] 🔔 Epoch change detected via block commit callback! New epoch: %d — forcing snapshot NOW", currentEpoch)
			// FORCED SNAPSHOT at epoch boundary — don't wait for blocksAfterEpoch
			sm.ForceSnapshotNow(blockNumber, currentEpoch)
		}

		sm.OnBlockCommitted(blockNumber)
	})

	logger.Info("📸 [SNAPSHOT] Block commit callback registered")

	// USE CONFIG VALUE STRICTLY
	// Dynamic port offset logic has been removed to respect snapshot_server_port exactly as configured


	// Khởi động HTTP server phục vụ tải snapshot
	StartSnapshotServer(snapshotDir, serverPort, sm)

	logger.Info("📸 [SNAPSHOT] ✅ Snapshot system initialized successfully")
	logger.Info("📸 [SNAPSHOT]    Method: %s", sm.snapshotMethod)
	logger.Info("📸 [SNAPSHOT]    Data dir: %s", dataDir)
	logger.Info("📸 [SNAPSHOT]    Snapshot dir: %s", snapshotDir)
	logger.Info("📸 [SNAPSHOT]    Blocks delay: %d", blocksDelay)
	logger.Info("📸 [SNAPSHOT]    HTTP server port: %d", serverPort)

	return sm
}

// GetGlobalSnapshotManager trả về global snapshot manager instance
func GetGlobalSnapshotManager() *SnapshotManager {
	return globalSnapshotManager
}
