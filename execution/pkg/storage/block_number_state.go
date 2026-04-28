package storage

import (
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// BlockCommitCallback là callback được gọi khi block mới được commit
type BlockCommitCallback func(blockNumber uint64)

var (
	lastBlockNumber           uint64
	updateState               uint32
	firstUpdateInRam          uint64
	firstUpdateInDb           uint64
	connectState              uint32
	lastBlockNumberFromMaster uint64
	incrementingCounter       uint64 // Only this variable needed; no separate mutex or file constant required
	lastGlobalExecIndex       uint64 // Maps Go block number → Rust consensus commit index
	lastExecutedCommitHash    []byte // Rust DAG commit digest
	lastHandledCommitIndex    uint32 // Rust consensus commit index

	// Callback invoked when a new block is committed (used by SnapshotManager)
	blockCommitCallback BlockCommitCallback

	// blockchainInitDone is set to 1 after initBlockchain() has fully loaded
	// the blockchain state (including LevelDB verification). Rust uses this
	// via is_ready in LastBlockNumberResponse to know the returned block
	// number is the FINAL value, not a transient metadata.json value.
	blockchainInitDone uint32
)

// Update state constants
const (
	DoneSubscribe         uint32 = 1 // Subscription completed
	StateLoadingSnapshot  uint32 = 2 // Loading snapshot
	StateSnapshotLoaded   uint32 = 3 // Snapshot loaded
	StateDBReadCompleted  uint32 = 4 // All DB data read
	StateRAMReadCompleted uint32 = 5 // RAM data read

)

var StateChangeChan = make(chan uint32)
var ConnectChangeChan = make(chan uint32)

// TPS OPTIMIZATION: CommitLock removed — was causing 71% idle time by blocking
// ProcessorPool while blocks committed. AccountStateDB.lockedFlag already provides
// the necessary concurrent access safety.
// var commitLock uint32 // REMOVED

func InitIncrementingCounterFromBootTime() {
	// Lấy Unix timestamp hiện tại (số giây kể từ Epoch).
	// Bạn có thể dùng `time.Now().UnixMilli()` nếu cần độ chính xác mili giây.
	initialValue := uint64(time.Now().Unix())
	atomic.StoreUint64(&incrementingCounter, initialValue)
	logger.Info("Incrementing counter initialized to current Unix timestamp: %d", initialValue)
}

// GetIncrementingCounter trả về giá trị hiện tại của số tăng dần.
// Hàm này an toàn cho đa luồng vì sử dụng `atomic.LoadUint64`.
func GetIncrementingCounter() uint64 {
	return atomic.AddUint64(&incrementingCounter, 1) // Tăng giá trị cho lần gọi tiếp theo
}

// TPS OPTIMIZATION: CommitLock functions are now no-ops.
// Previously, SetCommitLock(true) was called at the start of ProcessTransactions,
// and GetCommitLock() blocked ProcessorPool from starting the next batch.
// This created a serial pipeline where only one block could process at a time.
// AccountStateDB.lockedFlag already prevents concurrent IntermediateRoot calls.
func SetCommitLock(lock bool) {
	// NO-OP: Removed to enable overlapping block execution
}

func GetCommitLock() bool {
	return false // Never locked — always allow processing
}

func UpdateLastBlockNumber(blockNumber uint64) {
	// MONOTONIC: Only update if new value is GREATER than current
	// This prevents race between sync handler (writing high block numbers)
	// and consensus goroutine (writing lower block numbers concurrently).
	for {
		current := atomic.LoadUint64(&lastBlockNumber)
		if blockNumber <= current {
			return // Don't go backwards
		}
		if atomic.CompareAndSwapUint64(&lastBlockNumber, current, blockNumber) {
			// Gọi callback nếu có (dùng cho SnapshotManager)
			if cb := blockCommitCallback; cb != nil {
				cb(blockNumber)
			}
			return
		}
		// CAS failed, another goroutine updated — retry
	}
}

func GetLastBlockNumber() uint64 {
	return atomic.LoadUint64(&lastBlockNumber)
}

// SetBlockchainInitDone marks blockchain initialization as complete.
// Call this ONCE after initBlockchain() has finished loading the chain state.
func SetBlockchainInitDone() {
	val := atomic.AddUint32(&blockchainInitDone, 1) // Use Add to see if called multiple times
	logger.Info("✅ [INIT-DEBUG] SetBlockchainInitDone() CALLED! flag=%d (should be 1), lastBlockNumber=%d", val, GetLastBlockNumber())
}

// IsBlockchainInitDone returns true if blockchain initialization is complete.
func IsBlockchainInitDone() bool {
	val := atomic.LoadUint32(&blockchainInitDone)
	logger.Info("🔍 [INIT-DEBUG] IsBlockchainInitDone() checked: flag=%d, lastBlockNumber=%d", val, GetLastBlockNumber())
	return val >= 1
}

// UpdateLastGlobalExecIndex updates the last processed GlobalExecIndex (monotonic)
// This tracks which Rust consensus commit was last processed by Go Master
func UpdateLastGlobalExecIndex(index uint64) {
	for {
		current := atomic.LoadUint64(&lastGlobalExecIndex)
		if index <= current {
			return // Don't go backwards
		}
		if atomic.CompareAndSwapUint64(&lastGlobalExecIndex, current, index) {
			return
		}
	}
}

// GetLastGlobalExecIndex returns the last processed GlobalExecIndex
func GetLastGlobalExecIndex() uint64 {
	return atomic.LoadUint64(&lastGlobalExecIndex)
}

// ForceSetLastGlobalExecIndex sets the GEI to an exact value, bypassing the
// monotonic guard. Used ONLY during snapshot restore correction when the GEI
// was inflated by P2P-synced blocks that were never executed by NOMT.
func ForceSetLastGlobalExecIndex(index uint64) {
	atomic.StoreUint64(&lastGlobalExecIndex, index)
}

func UpdateLastHandledCommitIndex(index uint32) {
	for {
		current := atomic.LoadUint32(&lastHandledCommitIndex)
		if index <= current {
			return // Don't go backwards
		}
		if atomic.CompareAndSwapUint32(&lastHandledCommitIndex, current, index) {
			return
		}
	}
}

func GetLastHandledCommitIndex() uint32 {
	return atomic.LoadUint32(&lastHandledCommitIndex)
}

func ForceSetLastHandledCommitIndex(index uint32) {
	atomic.StoreUint32(&lastHandledCommitIndex, index)
}

func UpdateLastExecutedCommitHash(hash []byte) {
	// Simple store, race condition here is minimal since it's only updated sequentially from commit worker
	lastExecutedCommitHash = hash
}

func GetLastExecutedCommitHash() []byte {
	return lastExecutedCommitHash
}

// ForceSetLastBlockNumber sets the block number to an exact value, bypassing
// the monotonic guard. Used ONLY during snapshot restore correction.
// CRITICAL FIX: Do NOT allow downgrades — if current block is already higher
// (e.g. loaded from LevelDB), forcing a lower value causes Rust to re-execute
// existing blocks and creates a permanent fork.
func ForceSetLastBlockNumber(blockNumber uint64) {
	for {
		current := atomic.LoadUint64(&lastBlockNumber)
		if blockNumber < current {
			logger.Warn("🛡️ [SNAPSHOT FIX] REFUSING to downgrade lastBlockNumber %d → %d. LevelDB already has higher block. Keeping %d.", current, blockNumber, current)
			return
		}
		if atomic.CompareAndSwapUint64(&lastBlockNumber, current, blockNumber) {
			logger.Info("🛡️ [SNAPSHOT FIX] ForceSetLastBlockNumber: %d → %d", current, blockNumber)
			return
		}
	}
}

// SetBlockCommitCallback đăng ký callback khi block mới commit
// Dùng để SnapshotManager theo dõi block commits mà không cần sửa từng nơi gọi UpdateLastBlockNumber
func SetBlockCommitCallback(cb BlockCommitCallback) {
	blockCommitCallback = cb
	logger.Info("📸 [SNAPSHOT] Block commit callback registered")
}

func UpdateLastBlockNumberFromMaster(blockNumber uint64) {
	atomic.StoreUint64(&lastBlockNumberFromMaster, blockNumber)
}

func GetLastBlockNumberFromMaster() uint64 {
	return atomic.LoadUint64(&lastBlockNumberFromMaster)
}

// Cập nhật trạng thái và gửi thông báo qua channel nếu thay đổi
func UpdateState(state uint32) {
	atomic.StoreUint32(&updateState, state)
	StateChangeChan <- state
}

// Lấy trạng thái cập nhật
func GetUpdateState() uint32 {
	return atomic.LoadUint32(&updateState)
}

// Cập nhật trạng thái kết nối và gửi thông báo qua channel nếu thay đổi
func UpdateConnectState(state uint32) {
	atomic.StoreUint32(&connectState, state)
	ConnectChangeChan <- state
}

// Lấy trạng thái kết nối
func GetConnectState() uint32 {
	return atomic.LoadUint32(&connectState)
}

// Hàm để lấy giá trị firstUpdateInRam
func GetFirstUpdateInRam() uint64 {
	return atomic.LoadUint64(&firstUpdateInRam)
}

// Hàm để cập nhật giá trị firstUpdateInRam
func UpdateFirstUpdateInRam(firstUpdate uint64) {
	atomic.StoreUint64(&firstUpdateInRam, firstUpdate)
}

// Hàm để lấy giá trị firstUpdateInRam
func GetFirstUpdateInDb() uint64 {
	return atomic.LoadUint64(&firstUpdateInDb)
}

// Hàm để cập nhật giá trị firstUpdateInRam
func UpdateFirstUpdateInDb(firstUpdate uint64) {
	atomic.StoreUint64(&firstUpdateInDb, firstUpdate)
}

// ============================================================================
// CLEAN TRANSITION HANDOFF - State variables and functions
// These track the block boundaries during sync/consensus mode transitions
// ============================================================================

var (
	// consensusStartBlock is the first block that consensus will produce after a SyncOnly -> Validator transition
	consensusStartBlock uint64

	// syncStartBlock is the first block that sync will process after a Validator -> SyncOnly transition
	syncStartBlock uint64
)

// SetConsensusStartBlock sets the first block number that consensus will produce
// Called by Rust before starting consensus to notify Go of the transition
func SetConsensusStartBlock(blockNumber uint64) {
	atomic.StoreUint64(&consensusStartBlock, blockNumber)
	logger.Info("📌 [TRANSITION STATE] ConsensusStartBlock set to %d", blockNumber)
}

// GetConsensusStartBlock returns the first block number that consensus will produce
func GetConsensusStartBlock() uint64 {
	return atomic.LoadUint64(&consensusStartBlock)
}

// SetSyncStartBlock sets the first block number that sync will process after consensus ends
// Called by Rust when transitioning from Validator to SyncOnly mode
func SetSyncStartBlock(blockNumber uint64) {
	atomic.StoreUint64(&syncStartBlock, blockNumber)
	logger.Info("📌 [TRANSITION STATE] SyncStartBlock set to %d", blockNumber)
}

// GetSyncStartBlock returns the first block number that sync will process after consensus ends
func GetSyncStartBlock() uint64 {
	return atomic.LoadUint64(&syncStartBlock)
}
