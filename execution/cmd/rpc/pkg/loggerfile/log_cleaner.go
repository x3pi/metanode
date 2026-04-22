package loggerfile

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// LogCleaner quản lý việc xóa logs tự động
type LogCleaner struct {
	logDir        string
	stop          chan struct{}
	retentionDays int
}

// NewLogCleaner tạo mới một LogCleaner
func NewLogCleaner(logDir string) *LogCleaner {
	return &LogCleaner{
		logDir:        logDir,
		stop:          make(chan struct{}),
		retentionDays: 7,
	}
}

// CleanLogs xóa tất cả file và thư mục con trong thư mục logs.
func (lc *LogCleaner) CleanLogs() error {
	log.Println("Bắt đầu xóa logs...")

	// Kiểm tra thư mục logs có tồn tại không
	dir, err := os.Stat(lc.logDir)
	if os.IsNotExist(err) {
		log.Println("Thư mục logs không tồn tại, bỏ qua việc xóa.")
		return nil
	}
	if !dir.IsDir() {
		return fmt.Errorf("đường dẫn log cung cấp không phải là một thư mục: %s", lc.logDir)
	}

	// Đọc tất cả các mục (file và thư mục con) trong thư mục log chính
	dirEntries, err := os.ReadDir(lc.logDir)
	if err != nil {
		log.Printf("Không thể đọc thư mục logs %s: %v\n", lc.logDir, err)
		return err
	}

	retention := lc.retentionDays
	if retention <= 0 {
		retention = 7
	}
	cutoff := time.Now().AddDate(0, 0, -retention)

	for _, yearEntry := range dirEntries {
		if !yearEntry.IsDir() {
			continue
		}
		year, err := strconv.Atoi(yearEntry.Name())
		if err != nil {
			continue
		}
		yearPath := filepath.Join(lc.logDir, yearEntry.Name())
		monthEntries, err := os.ReadDir(yearPath)
		if err != nil {
			log.Printf("Không thể đọc thư mục năm %s: %v\n", yearPath, err)
			continue
		}

		for _, monthEntry := range monthEntries {
			if !monthEntry.IsDir() {
				continue
			}
			month, err := strconv.Atoi(monthEntry.Name())
			if err != nil || month < 1 || month > 12 {
				continue
			}
			monthPath := filepath.Join(yearPath, monthEntry.Name())
			dayEntries, err := os.ReadDir(monthPath)
			if err != nil {
				log.Printf("Không thể đọc thư mục tháng %s: %v\n", monthPath, err)
				continue
			}

			for _, dayEntry := range dayEntries {
				if !dayEntry.IsDir() {
					continue
				}
				day, err := strconv.Atoi(dayEntry.Name())
				if err != nil || day < 1 || day > 31 {
					continue
				}
				dayPath := filepath.Join(monthPath, dayEntry.Name())
				dayTime := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
				if dayTime.Before(cutoff) {
					log.Printf("Đang xóa thư mục log cũ: %s\n", dayPath)
					if err := os.RemoveAll(dayPath); err != nil {
						log.Printf("Không thể xóa %s: %v\n", dayPath, err)
					}
				}
			}
			removeIfEmpty(monthPath)
		}
		removeIfEmpty(yearPath)
	}

	log.Println("Hoàn thành xóa logs.")
	return nil
}

// StartDailyCleanup bắt đầu lịch trình xóa logs hàng tuần vào 0h (múi giờ +7)
func (lc *LogCleaner) StartDailyCleanup() {
	go func() {
		const cleanupInterval = 7 * 24 * time.Hour
		for {
			now := time.Now()
			location, err := time.LoadLocation("Asia/Bangkok")
			if err != nil {
				location = time.FixedZone("UTC+7", 7*60*60)
			}

			nowInLocation := now.In(location)

			nextCleanup := time.Date(
				nowInLocation.Year(),
				nowInLocation.Month(),
				nowInLocation.Day(),
				0,
				0,
				0,
				0,
				location,
			)

			// Nếu đã qua thời điểm 00:00 hôm nay, đặt lịch cho 00:00 sau 7 ngày
			if nowInLocation.After(nextCleanup) {
				nextCleanup = nextCleanup.Add(cleanupInterval)
			}

			waitDuration := nextCleanup.Sub(nowInLocation)
			log.Printf("Lịch trình xóa logs tiếp theo: %s (sau %v)\n", nextCleanup.Format("2006-01-02 15:04:05"), waitDuration)

			select {
			case <-time.After(waitDuration):
				if err := lc.CleanLogs(); err != nil {
					log.Printf("Lỗi khi xóa logs tự động: %v\n", err)
				}
				nextCleanup = nextCleanup.Add(cleanupInterval)
			case <-lc.stop:
				log.Println("Dừng lịch trình xóa logs hàng ngày")
				return
			}
		}
	}()
}

// Stop dừng lịch trình xóa logs
func (lc *LogCleaner) Stop() {
	close(lc.stop)
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
