package processor

// cross_chain_sig_accumulator.go
//
// Tích lũy vote từ các embassy cho từng batch.
// Vote KEY = sha256(batchSubmit calldata[4:]) — hash toàn bộ data gửi lên.
// Vì 3 embassy scan cùng block range → gửi cùng data → key giống nhau.
//
// Khi đủ 2/3 embassy vote cùng key → SetTxType(CC_EXECUTE=101).
// TX chưa đủ vote → SetTxType(CC_SIG_ACK=100) → master chỉ tăng nonce.
// Sau 1 phút kể từ khi execute xong → cleanup cacheEntry.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// ccBatchVoteState trạng thái vote cho 1 batch (key = sha256 của calldata).
type ccBatchVoteState struct {
	mu        sync.Mutex
	voters    map[string]struct{} // địa chỉ Hex của embassy đã vote
	readyTx   bool                // đã đạt quorum và TX execute đã được gửi
	createdAt time.Time
}

// CCBatchVoteAccumulator tích lũy vote cho các batchSubmit.
// Key = [32]byte (sha256 của calldata[4:]).
// Thread-safe: mỗi entry dùng lock riêng, totalEmbassies dùng atomic.
type CCBatchVoteAccumulator struct {
	entries        sync.Map // [32]byte → *ccBatchVoteState
	totalEmbassies atomic.Int32
}

var (
	globalCCBatchAccumulator     *CCBatchVoteAccumulator
	globalCCBatchAccumulatorOnce sync.Once

	CleanupCCBatchInterval = 1 * time.Second
	CleanupCCBatchExpiry   = 2 * time.Minute
)

// GetCCBatchVoteAccumulator trả về singleton.
func GetCCBatchVoteAccumulator() *CCBatchVoteAccumulator {
	globalCCBatchAccumulatorOnce.Do(func() {
		acc := &CCBatchVoteAccumulator{}
		globalCCBatchAccumulator = acc
		go acc.cleanupLoop()
	})
	return globalCCBatchAccumulator
}

// SetTotalEmbassies cập nhật tổng số embassy active.
func (a *CCBatchVoteAccumulator) SetTotalEmbassies(total int) {
	a.totalEmbassies.Store(int32(total))
	logger.Info("[CCVote] Embassy count updated: %d (quorum=%d)", total, a.quorum(total))
}

// GetTotalEmbassies trả về số embassy hiện tại.
func (a *CCBatchVoteAccumulator) GetTotalEmbassies() int {
	return int(a.totalEmbassies.Load())
}

// quorum tính ngưỡng tối thiểu: ceil(2N/3)
// N=1→1, N=2→2, N=3→2, N=4→3, N=5→4
func (a *CCBatchVoteAccumulator) quorum(total int) int {
	if total <= 0 {
		return 1
	}
	return (total*2 + 2) / 3
}

// AddVoteByKey thêm vote với key = sha256(calldata[4:]).
// Trả về (voteCount, isFirstToReachQuorum, error).
//   - error != nil: duplicate vote hoặc đã execute rồi
//   - isFirstToReachQuorum=true: lần đầu đủ quorum → gửi TX execute
func (a *CCBatchVoteAccumulator) AddVoteByKey(
	embassyAddr string,
	key [32]byte,
) (voteCount int, isFirstQuorum bool, err error) {
	actual, _ := a.entries.LoadOrStore(key, &ccBatchVoteState{
		voters:    make(map[string]struct{}),
		createdAt: time.Now(),
	})
	state := actual.(*ccBatchVoteState)

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.readyTx {
		return len(state.voters), false,
			fmt.Errorf("batch already executed (key=%x...)", key[:8])
	}
	if _, exists := state.voters[embassyAddr]; exists {
		return len(state.voters), false,
			fmt.Errorf("duplicate vote from %s (key=%x...)", embassyAddr, key[:8])
	}

	state.voters[embassyAddr] = struct{}{}
	voteCount = len(state.voters)

	total := a.GetTotalEmbassies()
	q := a.quorum(total)
	logger.Info("[CCVote] Vote recorded: addr=%s key=%x... votes=%d/%d quorum=%d",
		embassyAddr, key[:8], voteCount, total, q)

	if voteCount >= q {
		state.readyTx = true
		isFirstQuorum = true
		logger.Info("[CCVote] ✅ Quorum reached! key=%x... votes=%d/%d", key[:8], voteCount, total)
	}

	return voteCount, isFirstQuorum, nil
}

// cleanupLoop xóa entry cũ đã execute xong.
func (a *CCBatchVoteAccumulator) cleanupLoop() {
	ticker := time.NewTicker(CleanupCCBatchInterval)
	defer ticker.Stop()
	for range ticker.C {
		expireBefore := time.Now().Add(-1 * CleanupCCBatchExpiry)
		var deleted int
		a.entries.Range(func(k, v interface{}) bool {
			state := v.(*ccBatchVoteState)
			state.mu.Lock()
			shouldDelete := state.readyTx && state.createdAt.Before(expireBefore)
			state.mu.Unlock()
			if shouldDelete {
				a.entries.Delete(k)
				deleted++
			}
			return true
		})
		if deleted > 0 {
			logger.Info("[CCVote] 🧹 Cleaned up %d executed batch entries", deleted)
		}
	}
}
