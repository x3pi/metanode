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
	incrementingCounter       uint64 // Chỉ cần biến này, không cần mutex riêng hoặc hằng số file
	lastGlobalExecIndex       uint64 // Maps Go block number → Rust consensus commit index

	// Callback khi block mới commit (dùng cho SnapshotManager)
	blockCommitCallback BlockCommitCallback
)

// Định nghĩa các trạng thái cập nhật
const (
	DoneSubscribe         uint32 = 1 // Đăng ký hoàn thành
	StateLoadingSnapshot  uint32 = 2 // Đang tải snapshot
	StateSnapshotLoaded   uint32 = 3 // Đã tải xong snapshot
	StateDBReadCompleted  uint32 = 4 // Đọc xong tất cả dữ liệu trong DB
	StateRAMReadCompleted uint32 = 5 // Đọc xong dữ liệu trong RAM

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

// ForceSetLastBlockNumber sets the block number to an exact value, bypassing
// the monotonic guard. Used ONLY during snapshot restore correction.
func ForceSetLastBlockNumber(blockNumber uint64) {
	atomic.StoreUint64(&lastBlockNumber, blockNumber)
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
