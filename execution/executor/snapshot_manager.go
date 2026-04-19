package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// SnapshotMetadata chứa thông tin metadata của snapshot
type SnapshotMetadata struct {
	Epoch           uint64 `json:"epoch"`
	BlockNumber     uint64 `json:"block_number"`
	BoundaryBlock   uint64 `json:"boundary_block"`
	Timestamp       int64  `json:"timestamp"`
	CreatedAt       string `json:"created_at"`
	DataDir         string `json:"data_dir"`
	SnapshotName    string `json:"snapshot_name"`
	Method          string `json:"method"`
	GlobalExecIndex uint64 `json:"global_exec_index"`
	StateRoot       string `json:"state_root"`
	RustDAGEpoch    uint64 `json:"rust_dag_epoch"`
	RustCommitIndex uint64 `json:"rust_commit_index"`
}

// SnapshotManager quản lý việc tạo và xoay vòng snapshot
type SnapshotManager struct {
	dataDir           string // Thư mục gốc chứa LevelDB data (cho hardlink method)
	snapshotBaseDir   string // Thư mục chứa các snapshots
	maxSnapshots      int    // Số snapshot tối đa giữ lại (mặc định 2)
	blocksAfterEpoch  int    // Số blocks chờ sau epoch transition (mặc định 20)
	snapshotMethod    string // "hardlink", "rsync", hoặc "hybrid"
	snapshotSourceDir string // Thư mục cần snapshot (cho rsync/hybrid method, vd: data-write)

	// Filesystem capabilities
	reflinkSupported bool // true nếu filesystem hỗ trợ cp --reflink (btrfs, xfs)

	// State tracking
	mu                 sync.Mutex
	epochBoundaryBlock uint64 // Block number khi epoch transition
	currentEpoch       uint64 // Epoch hiện tại
	lastSeenEpoch      uint64 // Epoch cuối cùng đã thấy (để phát hiện thay đổi)
	snapshotPending    bool   // Đang chờ đủ blocks để tạo snapshot
	isCreating         bool   // Đang trong quá trình tạo snapshot
	enabled            bool   // Bật/tắt snapshot

	// LevelDB subdirectories cần snapshot (dùng cho hardlink và hybrid method)
	levelDBDirs []string
	// Xapian directories — cần copy thay vì hardlink (dùng cho hybrid method)
	// Nếu filesystem hỗ trợ reflink → dùng cp --reflink (tức thì)
	// Nếu không → dùng parallel Go copy
	xapianDirs []string

	// Khác: PebbleDB directories và SubNode directories cần bảo toàn
	pebbleDBDirs []string
	subNodeDirs  []string

	// Callback to forcefully flush all memory tables to disk before snapshotting
	forceFlushCallback func() error

	// Callback to create atomic PebbleDB checkpoints for all databases.
	// Takes snapshot destination path, creates checkpoints at destPath/<db_dir_name>.
	// When set, this replaces hardlink/copy for database directories.
	checkpointCallback func(destPath string) error

	// Callback to create snapshots of NOMT databases via locking + reflink/copy.
	nomtSnapshotCallback func(destPath string, useReflink bool) error

	// Callbacks for pausing/resuming execution
	pauseCallback      func()
	resumeCallback     func()
	rustPauseCallback  func()
	rustResumeCallback func()

	// Callback to get the current exact StateRoot
	stateRootCallback func() string
}

// NewSnapshotManager tạo instance mới của SnapshotManager
func NewSnapshotManager(dataDir, snapshotBaseDir string, maxSnapshots, blocksAfterEpoch int) *SnapshotManager {
	if maxSnapshots <= 0 {
		maxSnapshots = 2
	}
	if blocksAfterEpoch <= 0 {
		blocksAfterEpoch = 20
	}

	// Đảm bảo snapshotBaseDir tồn tại
	if err := os.MkdirAll(snapshotBaseDir, 0755); err != nil {
		logger.Error("📸 [SNAPSHOT] Failed to create snapshot directory: %v", err)
	}

	sm := &SnapshotManager{
		dataDir:          dataDir, // Note: This typically points to "data" (which contains "data/data" internally in some contexts, but actually it's the root of levelDB dirs)
		snapshotBaseDir:  snapshotBaseDir,
		maxSnapshots:     maxSnapshots,
		blocksAfterEpoch: blocksAfterEpoch,
		enabled:          true,
		levelDBDirs: []string{
			"account_state",
			"blocks",
			"receipts",
			"transaction_state",
			"mapping",
			"smart_contract_code",
			"smart_contract_storage",
			"stake_db",
			"trie_database",
			"backup_device_key_storage",
			"rust_consensus", // NEW: Rust DAG data
		},
		xapianDirs: []string{
			"xapian_node",
			"other",
		},
		pebbleDBDirs: []string{
			"../../back_up/backup_db",
			"../../back_up_write",
			"../../data-write",
		},
	}

	// Auto-detect filesystem reflink support (btrfs, xfs)
	sm.reflinkSupported = detectReflinkSupport(dataDir)
	if sm.reflinkSupported {
		logger.Info("📸 [SNAPSHOT] ✅ Filesystem supports reflink (btrfs/xfs) — instant copy for ALL files!")
	} else {
		logger.Info("📸 [SNAPSHOT] ℹ️ Filesystem does not support reflink — using hardlink+parallel copy")
	}

	logger.Info("📸 [SNAPSHOT] SnapshotManager initialized: data_dir=%s, snapshot_dir=%s, max_snapshots=%d, blocks_after_epoch=%d",
		dataDir, snapshotBaseDir, maxSnapshots, blocksAfterEpoch)

	return sm
}

// SetForceFlushCallback registers a callback to flush storage right before snapshots
func (sm *SnapshotManager) SetForceFlushCallback(cb func() error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.forceFlushCallback = cb
}

// SetCheckpointCallback registers a callback to create atomic PebbleDB checkpoints.
// The callback receives the snapshot destination path and creates checkpoints
// for all databases at destPath/<db_dir_name>.
// When set, this replaces hardlink/copy for database directories in CreateSnapshot/CreateHybridSnapshot.
func (sm *SnapshotManager) SetCheckpointCallback(cb func(destPath string) error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.checkpointCallback = cb
}

// SetNomtSnapshotCallback registers a callback to trigger NOMT native snapshots.
func (sm *SnapshotManager) SetNomtSnapshotCallback(cb func(destPath string, useReflink bool) error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.nomtSnapshotCallback = cb
}

// SetPauseCallback registers a callback to pause transaction execution
func (sm *SnapshotManager) SetPauseCallback(cb func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pauseCallback = cb
}

// SetStateRootCallback registers a callback to fetch the current NOMT state root.
func (sm *SnapshotManager) SetStateRootCallback(cb func() string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stateRootCallback = cb
}

// SetResumeCallback registers a callback to resume transaction execution
func (sm *SnapshotManager) SetResumeCallback(cb func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.resumeCallback = cb
}

// SetRustPauseCallback registers a callback to pause Rust consensus writing
func (sm *SnapshotManager) SetRustPauseCallback(cb func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.rustPauseCallback = cb
}

// SetRustResumeCallback registers a callback to resume Rust consensus writing
func (sm *SnapshotManager) SetRustResumeCallback(cb func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.rustResumeCallback = cb
}

// OnEpochAdvanced được gọi khi epoch transition xảy ra
func (sm *SnapshotManager) OnEpochAdvanced(boundaryBlock uint64, newEpoch uint64) {
	if !sm.enabled {
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.epochBoundaryBlock = boundaryBlock
	sm.currentEpoch = newEpoch
	sm.snapshotPending = true

	targetBlock := boundaryBlock + uint64(sm.blocksAfterEpoch)
	logger.Info("📸 [SNAPSHOT] Epoch advanced to %d (boundary_block=%d). Snapshot will trigger at block %d (+%d blocks)",
		newEpoch, boundaryBlock, targetBlock, sm.blocksAfterEpoch)
}

// OnBlockCommitted được gọi mỗi khi một block được commit vào DB
func (sm *SnapshotManager) OnBlockCommitted(blockNumber uint64) {
	if !sm.enabled {
		return
	}

	sm.mu.Lock()
	if sm.isCreating {
		sm.mu.Unlock()
		return
	}

	// Trigger snapshots every 50 blocks for testing purposes,
	// or if epoch advanced properly.
	isTestTrigger := blockNumber > 0 && blockNumber%50 == 0
	isStandardTrigger := sm.snapshotPending && blockNumber >= (sm.epochBoundaryBlock+uint64(sm.blocksAfterEpoch))

	if !isTestTrigger && !isStandardTrigger {
		sm.mu.Unlock()
		return
	}

	// Đã đủ blocks! Trigger snapshot
	sm.isCreating = true
	sm.snapshotPending = false
	epoch := sm.currentEpoch
	boundaryBlock := sm.epochBoundaryBlock
	sm.mu.Unlock()

	// Tạo snapshot trong goroutine riêng để không block block processing
	go func() {
		defer func() {
			sm.mu.Lock()
			sm.isCreating = false
			sm.mu.Unlock()
		}()

		logger.Info("📸 [SNAPSHOT] ⏰ Trigger! Creating snapshot at block %d (epoch=%d, boundary=%d, method=%s)",
			blockNumber, epoch, boundaryBlock, sm.snapshotMethod)

		// Trigger storage flush immediately before snapshot
		sm.mu.Lock()
		flushCb := sm.forceFlushCallback
		sm.mu.Unlock()
		if flushCb != nil {
			logger.Info("💾 [SNAPSHOT] Force flushing all memory tables to disk before snapshotting...")
			if err := flushCb(); err != nil {
				logger.Error("❌ [SNAPSHOT] Memory table flush failed: %v", err)
				// Continue with snapshot anyway to have _something_, though it may be incomplete
			} else {
				logger.Info("✅ [SNAPSHOT] Successfully flushed all memory tables to disk")
			}
		}

		var createErr, rotateErr error
		switch sm.snapshotMethod {
		case "rsync":
			createErr = sm.CreateRsyncSnapshot(epoch, blockNumber, boundaryBlock)
		case "hybrid":
			createErr = sm.CreateHybridSnapshot(epoch, blockNumber, boundaryBlock)
		default:
			createErr = sm.CreateSnapshot(epoch, blockNumber, boundaryBlock)
		}
		if createErr == nil {
			rotateErr = sm.RotateSnapshots()
		}

		if createErr != nil {
			logger.Error("📸 [SNAPSHOT] ❌ Failed to create snapshot: %v", createErr)
		}
		if rotateErr != nil {
			logger.Error("📸 [SNAPSHOT] ❌ Failed to rotate snapshots: %v", rotateErr)
		}
	}()
}

// CreateSnapshot tạo snapshot bằng hardlink copy
func (sm *SnapshotManager) CreateSnapshot(epoch, blockNumber, boundaryBlock uint64) error {
	startTime := time.Now()

	// Tên snapshot: snap_epoch_<epoch>_block_<block>
	snapshotName := fmt.Sprintf("snap_epoch_%d_block_%d", epoch, blockNumber)
	snapshotPath := filepath.Join(sm.snapshotBaseDir, snapshotName)

	// Kiểm tra xem snapshot đã tồn tại chưa
	if _, err := os.Stat(snapshotPath); err == nil {
		logger.Warn("📸 [SNAPSHOT] Snapshot already exists: %s", snapshotPath)
		return nil
	}

	// Tạo thư mục snapshot
	if err := os.MkdirAll(snapshotPath, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory %s: %w", snapshotPath, err)
	}

	sm.mu.Lock()
	checkpointCb := sm.checkpointCallback
	nomtCb := sm.nomtSnapshotCallback
	sm.mu.Unlock()

	if checkpointCb != nil {
		logger.Info("📸 [SNAPSHOT] Creating PebbleDB CHECKPOINT snapshot: %s", snapshotPath)
		checkpointStart := time.Now()
		if err := checkpointCb(snapshotPath); err != nil {
			return fmt.Errorf("PebbleDB checkpoint failed: %w", err)
		}
		logger.Info("📸 [SNAPSHOT] ✅ PebbleDB Checkpoint completed (took %v)", time.Since(checkpointStart))
	} else {
		// Fallback: hardlink copy (for LevelDB or when checkpoint not available)
		logger.Info("📸 [SNAPSHOT] Creating hardlink snapshot: %s → %s", sm.dataDir, snapshotPath)
		for _, dir := range sm.levelDBDirs {
			srcDir := filepath.Join(sm.dataDir, dir)
			dstDir := filepath.Join(snapshotPath, dir)

			if _, err := os.Stat(srcDir); os.IsNotExist(err) {
				logger.Warn("📸 [SNAPSHOT] Source directory not found, skipping: %s", srcDir)
				continue
			}

			if err := hardlinkCopyDir(srcDir, dstDir); err != nil {
				logger.Error("📸 [SNAPSHOT] Failed to hardlink copy %s: %v", dir, err)
				return fmt.Errorf("failed to hardlink copy %s: %w", dir, err)
			}

			logger.Info("📸 [SNAPSHOT] ✅ Hardlinked: %s", dir)
		}
	}

	// Snapshot NOMT databases if configured
	if nomtCb != nil {
		logger.Info("📸 [SNAPSHOT] Creating NOMT Database Snapshot")
		nomtStart := time.Now()
		if err := nomtCb(snapshotPath, sm.reflinkSupported); err != nil {
			return fmt.Errorf("NOMT snapshot failed: %w", err)
		}
		logger.Info("📸 [SNAPSHOT] ✅ NOMT Snapshot completed (took %v)", time.Since(nomtStart))
	} else {
		logger.Warn("📸 [SNAPSHOT] ⏭️ Skipping NOMT snapshot (callback not set)")
	}
	// COPY PebbleDB và SubNode dirs (ONLY when checkpoint is NOT active)
	// When checkpointCallback is set, all PebbleDB databases are already
	// atomically checkpointed via CheckpointAll (including back_up/backup_db).
	// The pebbleDBDirs list (back_up/backup_db, back_up_write, data-write)
	// would duplicate that work with unsafe parallelCopyDir that can corrupt
	// data if PebbleDB compaction runs during the copy.
	if checkpointCb == nil {
		for _, dir := range sm.pebbleDBDirs {
			srcDir := filepath.Join(sm.dataDir, dir)
			// Fix dst path to keep just the base directory name instead of "../back_up/backup_db"
			dstDirName := filepath.Base(dir)
			if strings.Contains(dir, "backup_db") {
				// For back_up/backup_db, we want the whole back_up folder structure
				dstDirName = "back_up"
				srcDir = filepath.Join(sm.dataDir, "../../back_up")
			}
			dstDir := filepath.Join(snapshotPath, dstDirName)

			if _, err := os.Stat(srcDir); os.IsNotExist(err) {
				logger.Warn("📸 [SNAPSHOT] PebbleDB source not found, skipping: %s", srcDir)
				continue
			}

			// CRITICAL: PebbleDB SSTs are MUTABLE — compaction deletes/replaces them.
			// Hardlinking shares the inode with the live DB, causing corruption when
			// snapshots are rotated (inode refcount → 0 → live file becomes 0 bytes).
			// Use parallel byte-copy instead for complete isolation.
			if err := parallelCopyDir(srcDir, dstDir, 8); err != nil {
				logger.Error("📸 [SNAPSHOT] Failed to copy %s: %v", dir, err)
				return fmt.Errorf("failed to copy %s: %w", dir, err)
			}
			// Validate: remove any 0-byte SST files (caught mid-compaction during copy)
			cleaned := cleanZeroByteSSTs(dstDir)
			if cleaned > 0 {
				logger.Warn("📸 [SNAPSHOT] Removed %d zero-byte SST files from %s (PebbleDB was mid-compaction)", cleaned, dir)
			}
			logger.Info("📸 [SNAPSHOT] ✅ Copied: %s", dir)
		}
	} else {
		logger.Info("📸 [SNAPSHOT] ⏭️ Skipping pebbleDBDirs copy (already handled by PebbleDB checkpoint)")
	}

	// Explicitly copy epoch_data_backup.json
	epochBackupSrc := filepath.Join(sm.dataDir, "../../back_up/epoch_data_backup.json")
	epochBackupDst := filepath.Join(snapshotPath, "back_up", "epoch_data_backup.json")
	if _, err := os.Stat(epochBackupSrc); err == nil {
		if err := os.MkdirAll(filepath.Dir(epochBackupDst), 0755); err == nil {
			if copyErr := regularCopyFile(epochBackupSrc, epochBackupDst, 0644); copyErr != nil {
				logger.Warn("📸 [SNAPSHOT] Failed to copy epoch backup file: %v", copyErr)
			} else {
				logger.Info("📸 [SNAPSHOT] ✅ Copied: epoch_data_backup.json")
			}
		}
	}

	// Ghi metadata
	metadata := SnapshotMetadata{
		Epoch:         epoch,
		BlockNumber:   blockNumber,
		BoundaryBlock: boundaryBlock,
		Timestamp:     time.Now().UnixMilli(),
		CreatedAt:     time.Now().Format(time.RFC3339),
		DataDir:       sm.dataDir,
		SnapshotName:  snapshotName,
		Method:        "hardlink",
	}

	metadataPath := filepath.Join(snapshotPath, "metadata.json")
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	if err := os.WriteFile(metadataPath, metadataJSON, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	elapsed := time.Since(startTime)
	logger.Info("📸 [SNAPSHOT] ✅ Snapshot created successfully: %s (took %v)", snapshotName, elapsed)

	return nil
}

// CreateRsyncSnapshot tạo snapshot bằng rsync — an toàn cho Xapian
// rsync -a copy toàn bộ thư mục nguồn (bao gồm Xapian) → snapshot directory
// Không cần sudo, chỉ copy đúng thư mục cần thiết
func (sm *SnapshotManager) CreateRsyncSnapshot(epoch, blockNumber, boundaryBlock uint64) error {
	startTime := time.Now()

	if sm.snapshotSourceDir == "" {
		return fmt.Errorf("snapshot_source_dir is not configured")
	}

	// Kiểm tra thư mục nguồn tồn tại
	if _, err := os.Stat(sm.snapshotSourceDir); os.IsNotExist(err) {
		return fmt.Errorf("snapshot source directory does not exist: %s", sm.snapshotSourceDir)
	}

	// Tên snapshot: snap_epoch_<epoch>_block_<block>
	snapshotName := fmt.Sprintf("snap_epoch_%d_block_%d", epoch, blockNumber)
	snapshotPath := filepath.Join(sm.snapshotBaseDir, snapshotName)

	// Kiểm tra xem snapshot đã tồn tại chưa
	if _, err := os.Stat(snapshotPath); err == nil {
		logger.Warn("📸 [SNAPSHOT] Snapshot already exists: %s", snapshotPath)
		return nil
	}

	// Tạo thư mục snapshot
	if err := os.MkdirAll(snapshotPath, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory %s: %w", snapshotPath, err)
	}

	logger.Info("📸 [SNAPSHOT] Creating rsync snapshot: %s → %s", sm.snapshotSourceDir, snapshotPath)

	// Dùng rsync -a để copy toàn bộ nội dung thư mục nguồn
	// -a = archive mode (recursive, preserve permissions, timestamps, symlinks)
	// --delete = xóa files ở đích không có ở nguồn
	// Trailing "/" trên source = copy nội dung, không copy thư mục cha
	srcPath := strings.TrimRight(sm.snapshotSourceDir, "/") + "/"

	cmd := exec.Command("rsync", "-a", "--delete", srcPath, snapshotPath+"/")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Cleanup nếu rsync thất bại
		os.RemoveAll(snapshotPath)
		return fmt.Errorf("rsync failed: %v, output: %s", err, string(output))
	}

	logger.Info("📸 [SNAPSHOT] ✅ rsync completed successfully")

	// Ghi metadata
	metadata := SnapshotMetadata{
		Epoch:         epoch,
		BlockNumber:   blockNumber,
		BoundaryBlock: boundaryBlock,
		Timestamp:     time.Now().UnixMilli(),
		CreatedAt:     time.Now().Format(time.RFC3339),
		DataDir:       sm.snapshotSourceDir,
		SnapshotName:  snapshotName,
		Method:        "rsync",
	}

	metadataPath := filepath.Join(snapshotPath, "metadata.json")
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	if err := os.WriteFile(metadataPath, metadataJSON, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	elapsed := time.Since(startTime)
	logger.Info("📸 [SNAPSHOT] ✅ Rsync snapshot created: %s (took %v)", snapshotName, elapsed)

	return nil
}

// CreateHybridSnapshot tạo snapshot bằng cách kết hợp hardlink + rsync
// - LevelDB dirs: dùng hardlink (tức thì, 0 disk space cho immutable files)
// - Xapian dirs: dùng rsync (an toàn cho mutable files)
// Kết quả: nhanh gần như hardlink nhưng safe cho Xapian
func (sm *SnapshotManager) CreateHybridSnapshot(epoch, blockNumber, boundaryBlock uint64) error {
	startTime := time.Now()

	// Tên snapshot: snap_epoch_<epoch>_block_<block>
	snapshotName := fmt.Sprintf("snap_epoch_%d_block_%d", epoch, blockNumber)
	snapshotPath := filepath.Join(sm.snapshotBaseDir, snapshotName)

	// Kiểm tra xem snapshot đã tồn tại chưa
	if _, err := os.Stat(snapshotPath); err == nil {
		logger.Warn("📸 [SNAPSHOT] Snapshot already exists: %s", snapshotPath)
		return nil
	}

	// Tạo thư mục snapshot
	if err := os.MkdirAll(snapshotPath, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory %s: %w", snapshotPath, err)
	}

	logger.Info("📸 [SNAPSHOT] Creating HYBRID snapshot")
	logger.Info("📸 [SNAPSHOT]    Source data dir: %s", sm.dataDir)
	logger.Info("📸 [SNAPSHOT]    Target snapshot: %s", snapshotPath)
	// Phase 1: Database dirs — use PebbleDB Checkpoint when available
	sm.mu.Lock()
	checkpointCb := sm.checkpointCallback
	pauseCb := sm.pauseCallback
	resumeCb := sm.resumeCallback
	stateRootCb := sm.stateRootCallback
	rustPauseCb := sm.rustPauseCallback
	rustResumeCb := sm.rustResumeCallback
	sm.mu.Unlock()

	// CRITICAL: Acquire global execution lock before starting PebbleDB & NOMT snapshots
	// This ensures both DBs are captured at exactly the same GEI
	if rustPauseCb != nil {
		logger.Info("📸 [SNAPSHOT] ⏸️  Pausing Rust consensus writing for atomic snapshot...")
		rustPauseCb()
	}
	if pauseCb != nil {
		logger.Info("📸 [SNAPSHOT] ⏸️  Pausing Go Master execution for atomic database snapshot...")
		pauseCb()
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// CAPTURE ATOMIC STATE METADATA
	// ═══════════════════════════════════════════════════════════════════════════
	// While the execution is frozen, capture the exact block number, GEI, and state root
	// that will be snapshotted. This prevents inflation metadata mismatches and ensures
	// restorations will accurately align with the preserved state data.
	actualBlockNumber := storage.GetLastBlockNumber()
	actualGEI := storage.GetLastGlobalExecIndex()
	actualStateRoot := ""
	if stateRootCb != nil {
		actualStateRoot = stateRootCb()
	}
	// Fallbacks if node was uninitialized or execution never properly captured
	if actualBlockNumber == 0 {
		actualBlockNumber = blockNumber
	}
	
	// Khởi tạo các giá trị cho metadata (nếu có bridge sang Rust)
	// (sẽ gọi Rust via FFI sau khi unpause)
	rustDAGEpoch := uint64(0)
	rustCommitIndex := uint64(0)
	
	logger.Info("📸 [SNAPSHOT] Atomic state captured: Block #%d, GEI=%d, StateRoot=%s", 
		actualBlockNumber, actualGEI, actualStateRoot)

	hardlinkStart := time.Now()
	if checkpointCb != nil {
		logger.Info("📸 [SNAPSHOT] Phase 1: PebbleDB CHECKPOINT for all database dirs")
		if err := checkpointCb(snapshotPath); err != nil {
			os.RemoveAll(snapshotPath)
			return fmt.Errorf("PebbleDB checkpoint failed: %w", err)
		}
		logger.Info("📸 [SNAPSHOT] ✅ Phase 1: PebbleDB Checkpoint completed (took %v)", time.Since(hardlinkStart))
	} else {
		// Fallback: Hardlink copy LevelDB dirs
		hardlinkedCount := 0
		for _, dir := range sm.levelDBDirs {
			srcDir := filepath.Join(sm.dataDir, dir)
			dstDir := filepath.Join(snapshotPath, dir)

			if _, err := os.Stat(srcDir); os.IsNotExist(err) {
				logger.Warn("📸 [SNAPSHOT] Source directory not found, skipping: %s", srcDir)
				continue
			}

			if err := hardlinkCopyDir(srcDir, dstDir); err != nil {
				os.RemoveAll(snapshotPath)
				return fmt.Errorf("failed to hardlink copy %s: %w", dir, err)
			}
			hardlinkedCount++
		}
		logger.Info("📸 [SNAPSHOT] ✅ Phase 1: Hardlinked %d LevelDB dirs (took %v)", hardlinkedCount, time.Since(hardlinkStart))
	}
	hardlinkElapsed := time.Since(hardlinkStart)

	// Phase 1.5: Copy/Reflink NOMT Databases
	sm.mu.Lock()
	nomtCb := sm.nomtSnapshotCallback
	sm.mu.Unlock()
	if nomtCb != nil {
		logger.Info("📸 [SNAPSHOT] Phase 1.5: NOMT Database Snapshot")
		nomtStart := time.Now()
		if err := nomtCb(snapshotPath, sm.reflinkSupported); err != nil {
			os.RemoveAll(snapshotPath)
			return fmt.Errorf("NOMT snapshot failed: %w", err)
		}
		logger.Info("📸 [SNAPSHOT] ✅ Phase 1.5: NOMT Snapshot completed (took %v)", time.Since(nomtStart))
	} else {
		logger.Warn("📸 [SNAPSHOT] ⏭️ Phase 1.5: Skipping NOMT snapshot (callback not set)")
	}

	// CRITICAL: Release global execution lock after both databases are snapped
	if resumeCb != nil {
		logger.Info("📸 [SNAPSHOT] ▶️  Resuming Go Master execution after DB snapshots")
		resumeCb()
	}
	if rustResumeCb != nil {
		logger.Info("📸 [SNAPSHOT] ▶️  Resuming Rust consensus writing after DB snapshots")
		rustResumeCb()
	}

	// Phase 2: Copy Xapian dirs — auto-chọn method tốt nhất
	// btrfs/xfs → reflink (tức thì, CoW)
	// ext4/other → parallel Go copy (8 workers)
	// Lưu ý: Xapian dirs có thể nằm ở dataDir HOẶC snapshotSourceDir
	copyStart := time.Now()
	copiedCount := 0
	copyMethod := "parallel-copy"
	if sm.reflinkSupported {
		copyMethod = "reflink"
	}
	for _, dir := range sm.xapianDirs {
		dstDir := filepath.Join(snapshotPath, dir)

		// Tìm thư mục nguồn: ưu tiên dataDir, fallback sang snapshotSourceDir
		srcDir := filepath.Join(sm.dataDir, dir)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			// Không tìm thấy trong dataDir, thử snapshotSourceDir
			if sm.snapshotSourceDir != "" {
				srcDir = filepath.Join(sm.snapshotSourceDir, dir)
				if _, err := os.Stat(srcDir); os.IsNotExist(err) {
					logger.Warn("📸 [SNAPSHOT] Xapian directory not found in either location, skipping: %s", dir)
					continue
				}
			} else {
				logger.Warn("📸 [SNAPSHOT] Xapian directory not found, skipping: %s", srcDir)
				continue
			}
		}

		var copyErr error
		if sm.reflinkSupported {
			// Reflink: tức thì trên btrfs/xfs (Copy-on-Write)
			copyErr = reflinkCopyDir(srcDir, dstDir)
		} else {
			// Parallel Go copy: nhanh hơn rsync
			copyErr = parallelCopyDir(srcDir, dstDir, 8)
		}
		if copyErr != nil {
			os.RemoveAll(snapshotPath)
			return fmt.Errorf("%s failed for %s: %w", copyMethod, dir, copyErr)
		}
		copiedCount++
		logger.Info("📸 [SNAPSHOT]    ✅ %s: %s → %s", copyMethod, srcDir, dstDir)
	}
	copyElapsed := time.Since(copyStart)
	logger.Info("📸 [SNAPSHOT] ✅ Phase 2: %s %d Xapian dirs (took %v)", copyMethod, copiedCount, copyElapsed)

	// Phase 2.5: COPY PebbleDB và SubNode dirs (ONLY when checkpoint is NOT active)
	// When checkpointCallback is set, all PebbleDB databases are already
	// atomically checkpointed via CheckpointAll (including back_up/backup_db).
	if checkpointCb == nil {
		pebbleStart := time.Now()
		pebbleCount := 0
		for _, dir := range sm.pebbleDBDirs {
			srcDir := filepath.Join(sm.dataDir, dir)
			// Fix dst path to keep just the base directory name instead of "../back_up/backup_db"
			dstDirName := filepath.Base(dir)
			if strings.Contains(dir, "backup_db") {
				// For back_up/backup_db, we want the whole back_up folder structure
				dstDirName = "back_up"
				srcDir = filepath.Join(sm.dataDir, "../../back_up")
			}
			dstDir := filepath.Join(snapshotPath, dstDirName)

			if _, err := os.Stat(srcDir); os.IsNotExist(err) {
				logger.Warn("📸 [SNAPSHOT] PebbleDB source not found, skipping: %s", srcDir)
				continue
			}

			// CRITICAL: PebbleDB SSTs are MUTABLE — compaction deletes/replaces them.
			// Hardlinking shares the inode with the live DB, causing corruption when
			// snapshots are rotated (inode refcount → 0 → live file becomes 0 bytes).
			// Use parallel byte-copy instead for complete isolation.
			if err := parallelCopyDir(srcDir, dstDir, 8); err != nil {
				logger.Error("📸 [SNAPSHOT] Failed to copy %s: %v", dir, err)
				return fmt.Errorf("failed to copy %s: %w", dir, err)
			}
			// Validate: remove any 0-byte SST files (caught mid-compaction during copy)
			cleaned := cleanZeroByteSSTs(dstDir)
			if cleaned > 0 {
				logger.Warn("📸 [SNAPSHOT] Removed %d zero-byte SST files from %s (PebbleDB was mid-compaction)", cleaned, dir)
			}
			pebbleCount++
			logger.Info("📸 [SNAPSHOT] ✅ Copied PebbleDB/SubNode dir: %s", dir)
		}
		pebbleElapsed := time.Since(pebbleStart)
		logger.Info("📸 [SNAPSHOT] ✅ Phase 2.5: Copied %d PebbleDB/SubNode dirs (took %v)", pebbleCount, pebbleElapsed)
	} else {
		logger.Info("📸 [SNAPSHOT] ⏭️ Phase 2.5: Skipping pebbleDBDirs copy (already handled by PebbleDB checkpoint)")
	}

	// Phase 3: Nếu có snapshotSourceDir, rsync các file/dir khác ngoài LevelDB và Xapian
	// (ví dụ: các file cấu hình, state files)
	if sm.snapshotSourceDir != "" {
		extraStart := time.Now()
		extraCount := 0
		// Đọc danh sách entries trong source dir
		entries, err := os.ReadDir(sm.snapshotSourceDir)
		if err == nil {
			// Tạo set các dir đã được xử lý
			processedDirs := make(map[string]bool)
			for _, d := range sm.levelDBDirs {
				processedDirs[d] = true
			}
			for _, d := range sm.xapianDirs {
				processedDirs[d] = true
			}

			for _, entry := range entries {
				name := entry.Name()
				if processedDirs[name] {
					continue // Đã xử lý ở phase 1 hoặc 2
				}
				srcPath := filepath.Join(sm.snapshotSourceDir, name)
				dstPath := filepath.Join(snapshotPath, name)

				if entry.IsDir() {
					// Copy directory bằng parallel Go copy
					if err := parallelCopyDir(srcPath, dstPath, 8); err != nil {
						logger.Warn("📸 [SNAPSHOT] Failed to copy extra dir %s: %v", name, err)
						continue
					}
				} else {
					// Copy file
					info, err := entry.Info()
					if err == nil {
						if copyErr := regularCopyFile(srcPath, dstPath, info.Mode()); copyErr != nil {
							logger.Warn("📸 [SNAPSHOT] Failed to copy extra file %s: %v", name, copyErr)
							continue
						}
					}
				}
				extraCount++
			}
		}
		extraElapsed := time.Since(extraStart)
		if extraCount > 0 {
			logger.Info("📸 [SNAPSHOT] ✅ Phase 3: Copied %d extra items (took %v)", extraCount, extraElapsed)
		}
	}

	// Explicitly copy epoch_data_backup.json
	epochBackupSrc := filepath.Join(sm.dataDir, "../../back_up/epoch_data_backup.json")
	epochBackupDst := filepath.Join(snapshotPath, "back_up", "epoch_data_backup.json")
	if _, err := os.Stat(epochBackupSrc); err == nil {
		if err := os.MkdirAll(filepath.Dir(epochBackupDst), 0755); err == nil {
			if copyErr := regularCopyFile(epochBackupSrc, epochBackupDst, 0644); copyErr != nil {
				logger.Warn("📸 [SNAPSHOT] Failed to copy epoch backup file: %v", copyErr)
			} else {
				logger.Info("📸 [SNAPSHOT] ✅ Copied: epoch_data_backup.json")
			}
		}
	}

	// Khởi tạo các giá trị cho metadata từ Rust
	// (TODO: Populate over FFI if available)

	// Ghi metadata
	metadata := SnapshotMetadata{
		Epoch:           epoch,
		BlockNumber:     actualBlockNumber,
		BoundaryBlock:   boundaryBlock,
		Timestamp:       time.Now().UnixMilli(),
		CreatedAt:       time.Now().Format(time.RFC3339),
		DataDir:         sm.dataDir,
		SnapshotName:    snapshotName,
		Method:          "hybrid",
		GlobalExecIndex: actualGEI,
		StateRoot:       actualStateRoot,
		RustDAGEpoch:    rustDAGEpoch,
		RustCommitIndex: rustCommitIndex,
	}

	metadataPath := filepath.Join(snapshotPath, "metadata.json")
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	if err := os.WriteFile(metadataPath, metadataJSON, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	elapsed := time.Since(startTime)
	logger.Info("📸 [SNAPSHOT] ✅ HYBRID snapshot created: %s (total: %v, hardlink: %v, %s: %v)",
		snapshotName, elapsed, hardlinkElapsed, copyMethod, copyElapsed)

	return nil
}

// RotateSnapshots giữ lại maxSnapshots gần nhất, xóa cũ
func (sm *SnapshotManager) RotateSnapshots() error {
	snapshots, err := sm.ListSnapshots()
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %w", err)
	}

	if len(snapshots) <= sm.maxSnapshots {
		logger.Info("📸 [SNAPSHOT] No rotation needed: %d snapshots (max: %d)", len(snapshots), sm.maxSnapshots)
		return nil
	}

	// Sắp xếp theo thời gian tạo (cũ nhất trước)
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp < snapshots[j].Timestamp
	})

	// Xóa snapshots cũ
	toDelete := len(snapshots) - sm.maxSnapshots
	for i := 0; i < toDelete; i++ {
		snapshotPath := filepath.Join(sm.snapshotBaseDir, snapshots[i].SnapshotName)
		logger.Info("📸 [SNAPSHOT] 🗑️  Deleting old snapshot: %s (epoch=%d, block=%d)",
			snapshots[i].SnapshotName, snapshots[i].Epoch, snapshots[i].BlockNumber)

		if err := os.RemoveAll(snapshotPath); err != nil {
			logger.Error("📸 [SNAPSHOT] Failed to delete snapshot %s: %v", snapshotPath, err)
			continue
		}

		logger.Info("📸 [SNAPSHOT] ✅ Deleted: %s", snapshots[i].SnapshotName)
	}

	return nil
}

// ListSnapshots liệt kê tất cả snapshots hiện có
func (sm *SnapshotManager) ListSnapshots() ([]SnapshotMetadata, error) {
	return sm.listHardlinkSnapshots()
}

// listHardlinkSnapshots liệt kê snapshots tạo bằng hardlink method
func (sm *SnapshotManager) listHardlinkSnapshots() ([]SnapshotMetadata, error) {
	entries, err := os.ReadDir(sm.snapshotBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read snapshot directory: %w", err)
	}

	var snapshots []SnapshotMetadata
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "snap_epoch_") {
			continue
		}

		metadataPath := filepath.Join(sm.snapshotBaseDir, entry.Name(), "metadata.json")
		metadataJSON, err := os.ReadFile(metadataPath)
		if err != nil {
			logger.Warn("📸 [SNAPSHOT] No metadata for %s, skipping", entry.Name())
			continue
		}

		var metadata SnapshotMetadata
		if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
			logger.Warn("📸 [SNAPSHOT] Invalid metadata for %s: %v", entry.Name(), err)
			continue
		}

		snapshots = append(snapshots, metadata)
	}

	return snapshots, nil
}

// GetSnapshotDir trả về thư mục chứa snapshots
func (sm *SnapshotManager) GetSnapshotDir() string {
	return sm.snapshotBaseDir
}

// IsEnabled trả về trạng thái bật/tắt
func (sm *SnapshotManager) IsEnabled() bool {
	return sm.enabled
}

// SetEnabled bật/tắt snapshot
func (sm *SnapshotManager) SetEnabled(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.enabled = enabled
}

// SetLastSeenEpoch đặt epoch ban đầu khi khởi tạo
func (sm *SnapshotManager) SetLastSeenEpoch(epoch uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.lastSeenEpoch = epoch
	logger.Info("📸 [SNAPSHOT] lastSeenEpoch set to %d", epoch)
}

// DetectEpochChange kiểm tra chainState và phát hiện epoch thay đổi
// Trả về true nếu epoch đã thay đổi và OnEpochAdvanced được gọi
func (sm *SnapshotManager) DetectEpochChange(currentEpoch uint64, chainState *blockchain.ChainState) bool {
	if !sm.enabled {
		return false
	}

	sm.mu.Lock()
	if currentEpoch <= sm.lastSeenEpoch {
		sm.mu.Unlock()
		return false
	}

	// Epoch đã thay đổi!
	sm.lastSeenEpoch = currentEpoch
	sm.mu.Unlock()

	// Lấy boundary block từ chainState
	boundaryBlock, ok := chainState.GetEpochBoundaryBlock(currentEpoch)
	if !ok {
		logger.Warn("📸 [SNAPSHOT] Epoch %d detected but no boundary block found, using block 0", currentEpoch)
		boundaryBlock = 0
	}

	logger.Info("📸 [SNAPSHOT] 🔔 Auto-detected epoch transition: epoch=%d, boundary_block=%d", currentEpoch, boundaryBlock)
	sm.OnEpochAdvanced(boundaryBlock, currentEpoch)
	return true
}

// ForceSnapshotNow tạo snapshot ngay lập tức — dùng cho epoch transition.
// Không cần đợi blocksAfterEpoch. Chạy ĐỒNG BỘ (block processing sẽ đợi).
func (sm *SnapshotManager) ForceSnapshotNow(blockNumber uint64, epoch uint64) {
	if !sm.enabled {
		return
	}

	sm.mu.Lock()
	if sm.isCreating {
		sm.mu.Unlock()
		logger.Warn("📸 [SNAPSHOT] ForceSnapshotNow skipped — already creating snapshot")
		return
	}
	sm.isCreating = true
	sm.snapshotPending = false
	sm.epochBoundaryBlock = blockNumber
	sm.currentEpoch = epoch
	sm.mu.Unlock()

	logger.Info("📸 [SNAPSHOT] 🔔 ForceSnapshotNow: Creating mandatory epoch boundary snapshot at block %d (epoch=%d)", blockNumber, epoch)

	// Tạo snapshot trong goroutine để không block block processing
	go func() {
		defer func() {
			sm.mu.Lock()
			sm.isCreating = false
			sm.mu.Unlock()
		}()

		// Trigger storage flush
		sm.mu.Lock()
		flushCb := sm.forceFlushCallback
		sm.mu.Unlock()
		if flushCb != nil {
			// logger.Info("💾 [SNAPSHOT] Force flushing before epoch boundary snapshot...")
			if err := flushCb(); err != nil {
				logger.Error("❌ [SNAPSHOT] Flush failed: %v", err)
			}
		}

		var createErr, rotateErr error
		switch sm.snapshotMethod {
		case "rsync":
			createErr = sm.CreateRsyncSnapshot(epoch, blockNumber, blockNumber)
		case "hybrid":
			createErr = sm.CreateHybridSnapshot(epoch, blockNumber, blockNumber)
		default:
			createErr = sm.CreateSnapshot(epoch, blockNumber, blockNumber)
		}
		if createErr == nil {
			rotateErr = sm.RotateSnapshots()
		}

		if createErr != nil {
			logger.Error("📸 [SNAPSHOT] ❌ Epoch boundary snapshot failed: %v", createErr)
		} else {
			logger.Info("📸 [SNAPSHOT] ✅ Epoch boundary snapshot created at block %d (epoch=%d)", blockNumber, epoch)
		}
		if rotateErr != nil {
			logger.Error("📸 [SNAPSHOT] ❌ Snapshot rotation failed: %v", rotateErr)
		}
	}()
}

// ============================================================================
// Hardlink Copy Utilities
// ============================================================================

// hardlinkCopyDir tạo hardlink copy của thư mục src → dst
// SST files (.ldb, .sst) → hardlink (immutable, an toàn)
// Metadata files (MANIFEST, CURRENT, LOG) → regular copy (bị modify in-place)
func hardlinkCopyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dst, err)
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Tính relative path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		// Quyết định hardlink hay copy dựa trên tên file
		if isImmutableFile(info.Name()) {
			// SST files: hardlink (instant, 0 space)
			if err := os.Link(path, dstPath); err != nil {
				// Fallback sang copy nếu hardlink không thành công (cross-device)
				logger.Warn("📸 [SNAPSHOT] Hardlink failed for %s, falling back to copy: %v", relPath, err)
				return regularCopyFile(path, dstPath, info.Mode())
			}
		} else {
			// Metadata files: regular copy (MANIFEST, CURRENT, LOG bị modify in-place)
			if err := regularCopyFile(path, dstPath, info.Mode()); err != nil {
				return fmt.Errorf("failed to copy %s: %w", relPath, err)
			}
		}

		return nil
	})
}

// isImmutableFile kiểm tra file có phải là immutable (SST file) hay không
// LevelDB SST files có đuôi .ldb hoặc .sst — chúng KHÔNG BAO GIỜ bị sửa sau khi tạo
// CRITICAL: .log (WAL) files are MUTABLE — they must NOT be hardlinked.
// Hardlinking WALs shares the physical inode between live DB and snapshot.
// When the snapshot is rotated/deleted, the WAL gets removed → live DB corruption.
// This was the root cause of the massive PebbleDB corruption (Mar 2026).
func isImmutableFile(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".ldb") ||
		strings.HasSuffix(lower, ".sst") ||
		strings.HasSuffix(lower, ".vlog") // BadgerDB value log (immutable)
	// NOTE: .log (WAL) files are intentionally excluded — they are MUTABLE
	// and must be regular-copied to prevent corruption.
}

// cleanZeroByteSSTs removes zero-byte .sst files from a directory tree.
// PebbleDB may be mid-compaction during snapshot copy: a new SST file is created
// (size 0) but not yet written when the copy walks the directory. These empty SSTs
// will cause "invalid table (file size is too small)" errors when PebbleDB opens them.
// Returns the number of files removed.
func cleanZeroByteSSTs(dir string) int {
	removed := 0
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		lower := strings.ToLower(info.Name())
		if strings.HasSuffix(lower, ".sst") && info.Size() == 0 {
			if removeErr := os.Remove(path); removeErr == nil {
				removed++
			}
		}
		return nil
	})
	return removed
}

// regularCopyFile copy file thông thường
func regularCopyFile(src, dst string, mode os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// parallelCopyDir copy thư mục src → dst bằng goroutines song song
// Nhanh hơn rsync vì không cần fork process, không scan checksums
// Dùng worker pool để giới hạn số goroutines đồng thời
func parallelCopyDir(src, dst string, workers int) error {
	if workers <= 0 {
		workers = 8
	}

	// Bước 1: Scan tất cả files và tạo thư mục
	type copyJob struct {
		srcPath string
		dstPath string
		mode    os.FileMode
	}
	var jobs []copyJob

	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		jobs = append(jobs, copyJob{
			srcPath: path,
			dstPath: dstPath,
			mode:    info.Mode(),
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to scan directory %s: %w", src, err)
	}

	if len(jobs) == 0 {
		return nil
	}

	// Bước 2: Copy song song bằng worker pool
	var wg sync.WaitGroup
	errCh := make(chan error, len(jobs))
	jobCh := make(chan copyJob, len(jobs))

	// Start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				if err := regularCopyFile(job.srcPath, job.dstPath, job.mode); err != nil {
					errCh <- fmt.Errorf("copy %s: %w", job.srcPath, err)
					return
				}
			}
		}()
	}

	// Send jobs
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)

	wg.Wait()
	close(errCh)

	// Check for errors
	for err := range errCh {
		return err
	}

	return nil
}

// ============================================================================
// Reflink (Copy-on-Write) Support — btrfs, xfs
// ============================================================================

// detectReflinkSupport kiểm tra filesystem có hỗ trợ reflink hay không
// Tạo file tạm, thử cp --reflink=always, nếu thành công → filesystem hỗ trợ
func detectReflinkSupport(dataDir string) bool {
	// Tạo file test tạm thời trong dataDir
	testSrc := filepath.Join(dataDir, ".reflink_test_src")
	testDst := filepath.Join(dataDir, ".reflink_test_dst")

	// Cleanup
	defer os.Remove(testSrc)
	defer os.Remove(testDst)

	// Tạo file test
	if err := os.WriteFile(testSrc, []byte("reflink_test"), 0644); err != nil {
		return false
	}

	// Thử cp --reflink=always
	cmd := exec.Command("cp", "--reflink=always", testSrc, testDst)
	if err := cmd.Run(); err != nil {
		return false // Filesystem không hỗ trợ reflink
	}

	return true
}

// reflinkCopyDir copy thư mục bằng cp -a --reflink=always
// Trên btrfs/xfs: tức thì (Copy-on-Write), 0 disk space cho đến khi file bị modify
// An toàn cho mọi loại file bao gồm Xapian
func reflinkCopyDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("failed to create parent dir: %w", err)
	}

	// cp -a --reflink=auto: CoW nếu hỗ trợ, fallback sang copy thường nếu không
	// -a = archive (recursive, preserve permissions, timestamps, symlinks)
	cmd := exec.Command("cp", "-a", "--reflink=auto", src, dst)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp --reflink failed: %v, output: %s", err, string(output))
	}
	return nil
}
