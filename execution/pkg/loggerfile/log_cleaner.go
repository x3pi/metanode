package loggerfile

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Global LogCleaner instance — được set bởi main, dùng lại ở snapshot_init
var globalLogCleaner *LogCleaner

// SetGlobalLogCleaner lưu LogCleaner đã config vào global để các module khác dùng lại
func SetGlobalLogCleaner(lc *LogCleaner) {
	globalLogCleaner = lc
}

// GetGlobalLogCleaner trả về LogCleaner đã config. Có thể nil nếu chưa init.
func GetGlobalLogCleaner() *LogCleaner {
	return globalLogCleaner
}

// LogCleaner quản lý việc xóa logs theo epoch + periodic cleanup
type LogCleaner struct {
	logDir          string
	stop            chan struct{}
	maxEpochsToKeep int           // Số epoch tối đa giữ lại (mặc định 2)
	cleanInterval   time.Duration // Khoảng thời gian giữa các lần cleanup định kỳ
	disableCleanup  bool          // Khi true: không xóa log — giữ tất cả để debug
}

// NewLogCleaner tạo mới một LogCleaner
func NewLogCleaner(logDir string) *LogCleaner {
	return &LogCleaner{
		logDir:          logDir,
		stop:            make(chan struct{}),
		maxEpochsToKeep: 3,             // Giữ epoch hiện tại + 2 epoch trước
		cleanInterval:   1 * time.Hour, // Cleanup mỗi 1 giờ
	}
}

// SetMaxEpochsToKeep cấu hình số epoch logs giữ lại
// n=0: archive mode — giữ tất cả, không xóa gì
// n>=1: giữ n epoch gần nhất
func (lc *LogCleaner) SetMaxEpochsToKeep(n int) {
	if n == 0 {
		lc.disableCleanup = true
		log.Println("🔒 [LOG-CLEANER] epochs_to_keep=0 → Archive mode, giữ tất cả epoch logs")
		return
	}
	if n < 1 {
		n = 3
	}
	lc.maxEpochsToKeep = n
}

// SetCleanInterval cấu hình khoảng thời gian cleanup định kỳ
func (lc *LogCleaner) SetCleanInterval(d time.Duration) {
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	lc.cleanInterval = d
}

// SetDisableCleanup bật/tắt chế độ giữ tất cả logs (không xóa) để debug
func (lc *LogCleaner) SetDisableCleanup(disable bool) {
	lc.disableCleanup = disable
	if disable {
		log.Println("🔒 [LOG-CLEANER] Log cleanup DISABLED — giữ tất cả logs để debug")
	}
}

// IsCleanupDisabled trả về true nếu cleanup bị tắt (archive mode)
func (lc *LogCleaner) IsCleanupDisabled() bool {
	return lc.disableCleanup
}

// CleanOldEpochLogs xóa logs của các epoch cũ, chỉ giữ lại N epoch gần nhất
// Cấu trúc thư mục: logs/epoch_0/, logs/epoch_1/, logs/epoch_2/, ...
// Với maxEpochsToKeep=2: giữ epoch_2 và epoch_1, xóa epoch_0
func (lc *LogCleaner) CleanOldEpochLogs() error {
	if lc.disableCleanup {
		log.Println("🔒 [LOG-CLEANER] Cleanup disabled (--keep-all-logs), bỏ qua xóa logs")
		return nil
	}
	log.Printf("🧹 [LOG-CLEANER] Bắt đầu dọn dẹp logs (giữ %d epoch gần nhất)...\n", lc.maxEpochsToKeep)

	// Kiểm tra thư mục logs có tồn tại không
	dir, err := os.Stat(lc.logDir)
	if os.IsNotExist(err) {
		log.Println("🧹 [LOG-CLEANER] Thư mục logs không tồn tại, bỏ qua.")
		return nil
	}
	if !dir.IsDir() {
		return fmt.Errorf("đường dẫn log không phải là thư mục: %s", lc.logDir)
	}

	// Đọc tất cả entries
	entries, err := os.ReadDir(lc.logDir)
	if err != nil {
		return fmt.Errorf("không thể đọc thư mục logs %s: %w", lc.logDir, err)
	}

	// Thu thập các thư mục epoch_N
	type epochEntry struct {
		epoch uint64
		path  string
	}
	var epochs []epochEntry

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "epoch_") {
			continue
		}

		epochStr := strings.TrimPrefix(name, "epoch_")
		epochNum, err := strconv.ParseUint(epochStr, 10, 64)
		if err != nil {
			continue
		}
		epochs = append(epochs, epochEntry{
			epoch: epochNum,
			path:  filepath.Join(lc.logDir, name),
		})
	}

	if len(epochs) <= lc.maxEpochsToKeep {
		log.Printf("🧹 [LOG-CLEANER] Chỉ có %d epoch dirs, không cần xóa (giữ %d)\n", len(epochs), lc.maxEpochsToKeep)
		return nil
	}

	// Sắp xếp theo epoch giảm dần (mới nhất trước)
	sort.Slice(epochs, func(i, j int) bool {
		return epochs[i].epoch > epochs[j].epoch
	})

	// Xóa các epoch cũ (ngoài N epoch gần nhất)
	deletedCount := 0
	var freedBytes int64
	for i := lc.maxEpochsToKeep; i < len(epochs); i++ {
		ep := epochs[i]
		// Tính size trước khi xóa
		size := dirSize(ep.path)
		log.Printf("🧹 [LOG-CLEANER] Đang xóa logs epoch %d: %s (%.1f MB)\n",
			ep.epoch, ep.path, float64(size)/(1024*1024))
		if err := os.RemoveAll(ep.path); err != nil {
			log.Printf("🧹 [LOG-CLEANER] Không thể xóa %s: %v\n", ep.path, err)
			continue
		}
		deletedCount++
		freedBytes += size
	}

	if deletedCount > 0 {
		log.Printf("🧹 [LOG-CLEANER] ✅ Đã xóa %d epoch dirs cũ, giải phóng %.1f MB\n",
			deletedCount, float64(freedBytes)/(1024*1024))
	}

	// Xóa thư mục date cũ (YYYY/MM/DD) nếu còn sót từ hệ thống cũ
	lc.cleanLegacyDateDirs()

	log.Println("🧹 [LOG-CLEANER] Hoàn thành dọn dẹp logs.")
	return nil
}

// StartPeriodicCleanup bắt đầu cleanup định kỳ (mỗi giờ mặc định)
// Đảm bảo log không tăng quá lớn ngay cả khi epoch không thay đổi
func (lc *LogCleaner) StartPeriodicCleanup() {
	if lc.disableCleanup {
		log.Println("🔒 [LOG-CLEANER] Periodic cleanup disabled (--keep-all-logs)")
		return
	}
	go func() {
		ticker := time.NewTicker(lc.cleanInterval)
		defer ticker.Stop()

		log.Printf("🧹 [LOG-CLEANER] Đã khởi động periodic cleanup (mỗi %v)\n", lc.cleanInterval)

		for {
			select {
			case <-ticker.C:
				// Cleanup epoch dirs cũ
				if err := lc.CleanOldEpochLogs(); err != nil {
					log.Printf("🧹 [LOG-CLEANER] Lỗi cleanup định kỳ: %v\n", err)
				}

				// Log disk usage hiện tại
				lc.logDiskUsage()

			case <-lc.stop:
				log.Println("🧹 [LOG-CLEANER] Dừng periodic cleanup")
				return
			}
		}
	}()
}

// logDiskUsage log tổng dung lượng log hiện tại
func (lc *LogCleaner) logDiskUsage() {
	totalSize := dirSize(lc.logDir)
	if totalSize > 0 {
		log.Printf("🧹 [LOG-CLEANER] 📊 Tổng dung lượng logs: %.1f MB\n",
			float64(totalSize)/(1024*1024))
	}
}

// cleanLegacyDateDirs xóa các thư mục log theo ngày cũ (YYYY/MM/DD) nếu còn tồn tại
// từ hệ thống log cũ trước khi chuyển sang epoch-based
func (lc *LogCleaner) cleanLegacyDateDirs() {
	entries, err := os.ReadDir(lc.logDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Nếu là thư mục năm (4 chữ số, ví dụ "2026")
		if len(name) == 4 {
			if _, err := strconv.Atoi(name); err == nil {
				yearPath := filepath.Join(lc.logDir, name)
				log.Printf("🧹 [LOG-CLEANER] Xóa thư mục log cũ (date-based): %s\n", yearPath)
				os.RemoveAll(yearPath)
			}
		}
	}
}

// Stop dừng log cleaner
func (lc *LogCleaner) Stop() {
	close(lc.stop)
}

// dirSize tính tổng dung lượng một thư mục (recursive)
func dirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}

func removeIfEmpty(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	if len(entries) == 0 {
		_ = os.Remove(path)
	}
}
