package processor

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// TransactionRateTracker theo dõi tốc độ nhận giao dịch trong 1 giây gần nhất
// Thread-safe và được tối ưu cho high-concurrency scenarios
type TransactionRateTracker struct {
	mu              sync.RWMutex
	timestamps      []time.Time   // Lưu timestamp của các giao dịch (sorted chronologically)
	windowDuration  time.Duration // Thời gian cửa sổ (5 giây)
	lastCleanupTime time.Time     // Thời gian dọn dẹp lần cuối
	cleanupInterval time.Duration // Khoảng thời gian dọn dẹp

	// Thống kê cache để tránh tính toán lại liên tục cho multiple readers
	cachedCount        int
	cachedTps          float64
	lastCacheTime      time.Time
	cacheValidDuration time.Duration // Cache valid trong 100ms

	// Atomic counters cho hiệu suất cao
	totalTransactions int64 // Tổng số giao dịch từ khi bắt đầu (atomic)
}

// NewTransactionRateTracker tạo một tracker mới
func NewTransactionRateTracker() *TransactionRateTracker {
	return &TransactionRateTracker{
		timestamps:         make([]time.Time, 0, 1000), // Preallocate cho hiệu suất
		windowDuration:     1 * time.Second,            // Thay đổi từ 5s thành 1s
		lastCleanupTime:    time.Now(),
		cleanupInterval:    1 * time.Second,        // Dọn dẹp mỗi giây
		cacheValidDuration: 100 * time.Millisecond, // Cache valid 100ms
		totalTransactions:  0,
	}
}

// AddTransaction thêm một giao dịch mới vào tracker
// Thread-safe với minimal lock contention
func (trt *TransactionRateTracker) AddTransaction() {
	// Increment atomic counter trước (không cần lock)
	atomic.AddInt64(&trt.totalTransactions, 1)

	// Lock chỉ khi cần thêm timestamp
	trt.mu.Lock()
	defer trt.mu.Unlock()

	now := time.Now()
	trt.timestamps = append(trt.timestamps, now)

	// Invalidate cache khi có transaction mới
	trt.lastCacheTime = time.Time{}

	// Dọn dẹp định kỳ để tránh memory leak
	if now.Sub(trt.lastCleanupTime) >= trt.cleanupInterval {
		trt.cleanupOldEntries(now)
		trt.lastCleanupTime = now
	}
}

// GetTransactionRate trả về số giao dịch trong 5 giây gần nhất và TPS
// Optimized cho multiple concurrent readers với caching
func (trt *TransactionRateTracker) GetTransactionRate() (int, float64) {
	now := time.Now()

	// Check cache với read lock trước
	trt.mu.RLock()
	if !trt.lastCacheTime.IsZero() && now.Sub(trt.lastCacheTime) < trt.cacheValidDuration {
		// Cache còn valid, trả về giá trị cache
		count, tps := trt.cachedCount, trt.cachedTps
		trt.mu.RUnlock()
		return count, tps
	}
	trt.mu.RUnlock()

	// Cache hết hạn hoặc chưa có, cần tính toán lại với write lock
	trt.mu.Lock()
	defer trt.mu.Unlock()

	// Double-check sau khi có write lock (có thể thread khác đã update)
	if !trt.lastCacheTime.IsZero() && now.Sub(trt.lastCacheTime) < trt.cacheValidDuration {
		return trt.cachedCount, trt.cachedTps
	}

	// Tính toán thực tế
	cutoff := now.Add(-trt.windowDuration)
	count := 0

	// Đếm từ cuối về đầu (timestamps được sắp xếp theo thời gian)
	for i := len(trt.timestamps) - 1; i >= 0; i-- {
		if trt.timestamps[i].After(cutoff) {
			count++
		} else {
			break // Vì timestamps được sắp xếp theo thời gian
		}
	}

	tps := float64(count) / trt.windowDuration.Seconds()

	// Update cache
	trt.cachedCount = count
	trt.cachedTps = tps
	trt.lastCacheTime = now

	return count, tps
}

// cleanupOldEntries xóa các entry cũ hơn window duration
func (trt *TransactionRateTracker) cleanupOldEntries(now time.Time) {
	cutoff := now.Add(-trt.windowDuration)

	// Tìm vị trí đầu tiên còn valid
	validStart := 0
	for i, ts := range trt.timestamps {
		if ts.After(cutoff) {
			validStart = i
			break
		}
		validStart = len(trt.timestamps) // Nếu không tìm thấy, tất cả đều cũ
	}

	// Giữ lại chỉ những entry còn valid
	if validStart > 0 {
		copy(trt.timestamps, trt.timestamps[validStart:])
		trt.timestamps = trt.timestamps[:len(trt.timestamps)-validStart]
	}
}

// Biến toàn cục để theo dõi tốc độ giao dịch read-only
var GlobalReadTxRateTracker = NewTransactionRateTracker()

// Biến toàn cục để theo dõi tốc độ GỌI sendToAllConnectionsOfType trong ProcessReadTransaction
var GlobalSendToAllConnectionsTpsTracker = NewTransactionRateTracker()

// GetGlobalReadTransactionRate trả về thống kê toàn cục về tốc độ giao dịch read-only
func GetGlobalReadTransactionRate() (count int, tps float64) {
	return GlobalReadTxRateTracker.GetTransactionRate()
}

// GetGlobalSendToAllConnectionsTps trả về thống kê gọi sendToAllConnectionsOfType
func GetGlobalSendToAllConnectionsTps() (count int, tps float64) {
	return GlobalSendToAllConnectionsTpsTracker.GetTransactionRate()
}

// LogGlobalReadTransactionRate log thống kê hiện tại
func (tp *TransactionProcessor) LogGlobalReadTransactionRate() {
	count, tps := GlobalReadTxRateTracker.GetTransactionRate()
	total := GlobalReadTxRateTracker.GetTotalTransactions()
	// Chỉ log khi có hoạt động đáng kể
	if count > 100 {
		logger.Info("📊 GLOBAL_READ_TX_RATE: Transactions_Last_1s=%d, TPS_Last_1s=%.2f, Total=%d", count, tps, total)
	}
}

// LogGlobalSendToAllConnectionsTps log thống kê gọi sendToAllConnectionsOfType
func LogGlobalSendToAllConnectionsTps() {
	count, tps := GlobalSendToAllConnectionsTpsTracker.GetTransactionRate()
	total := GlobalSendToAllConnectionsTpsTracker.GetTotalTransactions()
	// Chỉ log khi có hoạt động đáng kể
	if count > 50 {
		logger.Info("📤 SEND_TO_ALL_CONNECTIONS_TPS: Calls_Last_1s=%d, CallTPS=%.2f, Total=%d", count, tps, total)
	}
}

// StartSendToAllConnectionsTpsLogger khởi động logger cho sendToAllConnectionsOfType TPS
func StartSendToAllConnectionsTpsLogger() {
	go func() {
		ticker := time.NewTicker(5 * time.Second) // Giảm từ 1s xuống 5s để giảm spam
		defer ticker.Stop()

		for {
			<-ticker.C
			count, tps := GlobalSendToAllConnectionsTpsTracker.GetTransactionRate()
			// Chỉ log khi có activity đáng kể
			if count > 100 {
				total := GlobalSendToAllConnectionsTpsTracker.GetTotalTransactions()
				logger.Info("📤 SEND_TO_ALL_CONNECTIONS_TPS: Calls_Last_1s=%d, TPS=%.2f, Total=%d", count, tps, total)
			}
		}
	}()
}

// GetTotalTransactions trả về tổng số giao dịch từ khi bắt đầu (thread-safe)
func (trt *TransactionRateTracker) GetTotalTransactions() int64 {
	return atomic.LoadInt64(&trt.totalTransactions)
}

// GetDetailedStats trả về thống kê chi tiết (thread-safe)
func (trt *TransactionRateTracker) GetDetailedStats() (recentCount int, recentTps float64, totalCount int64, avgTps float64) {
	recentCount, recentTps = trt.GetTransactionRate()
	totalCount = trt.GetTotalTransactions()

	// Tính TPS trung bình từ khi bắt đầu (cần thời gian start)
	trt.mu.RLock()
	uptime := time.Since(trt.lastCleanupTime) // Approximation
	trt.mu.RUnlock()

	if uptime.Seconds() > 0 {
		avgTps = float64(totalCount) / uptime.Seconds()
	}

	return
}

// Reset đặt lại tất cả thống kê (chỉ dùng cho testing hoặc restart)
func (trt *TransactionRateTracker) Reset() {
	trt.mu.Lock()
	defer trt.mu.Unlock()

	trt.timestamps = trt.timestamps[:0] // Clear slice nhưng giữ capacity
	atomic.StoreInt64(&trt.totalTransactions, 0)
	trt.lastCleanupTime = time.Now()
	trt.lastCacheTime = time.Time{} // Invalidate cache
}
